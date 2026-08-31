# Corral — notes for Claude Code

Corral (`corral`) runs AI coding agents inside per-project Lima VMs on
macOS. Go 1.25, Cobra CLI, Bubble Tea/lipgloss/huh TUI. Module path
`github.com/corral-sh/corral`.

## Commands

```bash
make build            # → bin/corral (version from git describe)
make test             # go test ./...
make lint             # vet + gofmt + golangci-lint (gosec etc.)
make security         # lint + govulncheck + shellcheck + gitleaks — same as the CI security stage
make e2e              # throwaway boxes under a temp CORRAL_HOME; asserts mounts, hide, broker, offline, snapshots (~4 min)
make dist             # darwin arm64/amd64 binaries + SHA256SUMS
./install.sh          # from-source install: brew deps + build + install
bin/corral doctor     # host prerequisite check
CORRAL_PLAIN=1 bin/corral run -- claude --version   # e2e against a real VM (~3 min first time)
```

Never run `limactl` against the user's default `~/.lima`; Corral owns
`~/.corral/lima` (`LIMA_HOME`). Set `CORRAL_HOME` to a temp dir for
experiments — tests already do (`t.Setenv`).

## Layout

- `cmd/corral/main.go` — entry; blank-imports agent packages to register them.
- `internal/cli` — commands (`root.go`, `launch.go`, `lifecycle.go`, `insight.go`, `setup.go`, `dashboard.go`). Only this package prints to the terminal.
- `internal/box` — the core: box naming, metadata, **Lima template rendering** (`Render`), launch env (`BuildLaunch`), audit log.
- `internal/broker` — egress allow-list proxy (`network = "broker"`); host-side lifecycle in `internal/box/broker.go`, guest funnel in `scripts/broker.sh`.
- `internal/lima` — `limactl` wrapper + `ssh -F <instance>/ssh.config` builder. Keep it policy-free.
- `internal/agent` — `Agent` interface + registry; `internal/agent/claude` is the reference implementation.
- `internal/guest/scripts/*.sh` — embedded scripts run inside the VM. `base.sh` (root), `toolchain-*.sh` (root), `user-base.sh` (user). Keep them idempotent; they run once via cloud-init and again on `rebuild`.
- `internal/config` — TOML schema. Pointer fields = "unset"; lists are unioned across layers. Layers: defaults < global < project (restricted) < `~/.corral/projects/<box>.toml` (trusted) < flags.
- `internal/policy` — the only place that decides what config is acted on: trust class per key (`trust.go`), path confinement and refused mounts (`paths.go`), `policy.Load`. New rules go here, not in `box` or `cli`.
- `docs/` — `FEASIBILITY.md` (why Lima, measurements), `ARCHITECTURE.md`, `SECURITY.md`, `FEATURES.md` (**generated**: `make docs` after any command/config change — `TestFeaturesCatalogUpToDate` fails otherwise; one-line descriptions live in `internal/cli/docs.go`). `llms.txt` at the root points AI assistants at these.

## Conventions

- Changing anything that ends up in the Lima template (mounts, provisioning, resources) changes the **template hash**; existing boxes then show as drifted and need `corral rebuild`. That is intended — mention it in the changelog.
- Secrets: never put values on a command line or in logs. Forwarding goes through `CORRAL_FWD_*` env + SSH `SendEnv`; the guest `AcceptEnv` list is in `base.sh`. Audit entries carry variable *names* only.
- Adding an agent: new package under `internal/agent/<name>`, implement the interface, `agent.Register` in `init()`, blank import in `main.go`, add a test like `claude_test.go`. Do not special-case agents elsewhere.
- Refuse dangerous mounts/paths in `internal/policy` (`ExtraMount`, `ProjectPath`, `ProvisionPath`); add to the list rather than loosening it. Adding a config key requires a trust class in `policy.trustTable` (a test enforces this).
- Box names must fit Lima's UNIX socket path (`paths.MaxBoxNameLen`). Do not lengthen the default `~/.corral` layout.
- Guest scripts must not use `curl | bash` except the agent vendor's own installer; download + checksum-verify instead (see `toolchain-node.sh`).
- Keep `changelog/unreleased.md` (Keep a Changelog format) current; one file per release under `changelog/`, `CHANGELOG.md` is only the index. The release workflow publishes `changelog/<version>.md` as the GitHub release notes, so never write "see CHANGELOG.md" — each release is self-contained. Releases are annotated git tags `vX.Y.Z` pushed to `main`.
- Work items: reference the GitHub issue with `Fixes #N` in the commit or PR so GitHub closes it on merge.
- Error messages tell the user the next command to run (`corral logs <box>`, `corral rebuild`).

## Release checklist

1. `git mv changelog/unreleased.md changelog/X.Y.Z.md`, set its heading to `# X.Y.Z — YYYY-MM-DD`, add a row to `CHANGELOG.md`, recreate an empty `changelog/unreleased.md`.
2. Bump `tag` in `Formula/corral.rb` **and** push the identical file to the tap repo
   `https://github.com/corral-sh/homebrew-tap` (Formula/corral.rb).
3. `make security test build && bin/corral version`, then `make e2e` (real boxes on this Mac, ~4 min; `scripts/e2e.sh`).
4. `git tag -a vX.Y.Z -m "..." && git push origin main --tags`.
5. CI does the rest on the tag (`.github/workflows/release.yml`): verify → build + sign → GitHub release
   with `changelog/X.Y.Z.md` as the notes and the binaries, `SHA256SUMS` and `SHA256SUMS.minisig` attached.
   Manual fallback: `make dist`, sign with `go run ./tools/sign -sign dist/SHA256SUMS`, then
   `gh release create vX.Y.Z dist/* --notes-file changelog/X.Y.Z.md`.

## Facts that cost time to rediscover

- Tests that set `CORRAL_HOME` must use a **short** directory (`os.MkdirTemp("/tmp", …)`, not
  `t.TempDir()`): LIMA_HOME feeds a UNIX socket path capped at 104 bytes and `paths` refuses long ones.
- The idle sweep runs on launch, in the dashboard and via `gc` — **never from `list`** (a supervisor
  polls it). A session is registered *before* the box boots (`SessionStart` with a nil Meta is adopted by
  `Create`), so a booting box is never idle; keep that ordering when touching `launch`/`ensureRunning`.
- `~/.corral/projects/<box>.toml` is keyed on the **effective** box name (`--box`, `name =`, else
  derived) — `loadConfig(project, boxName)`; pass the box's real name for existing boxes.
- `profile = "strict"` intentionally drops `forward_env`; the launcher must warn and `config` must show
  the suppression. `keychain_env` errors are typed (`ErrKeychainNotFound` / `NoInteraction` /
  `Denied`, `security` exit 44/36/51) — use `box.KeychainRemedy`, never a generic "add one".
- Release signing: `tools/sign` (pure Go minisign). Secret = one base64 line in the GitHub Actions secret
  `CORRAL_MINISIGN_KEY`; public key `release/minisign.pub`. A tag build without the key fails by
  design. Never regenerate the key pair (`-gen` refuses if the .pub exists).
- Live checks that need a real box: use a scratch project under the session scratchpad with `--box`,
  `CORRAL_PLAIN=1`, and `delete --yes` afterwards. To test exit 69, `corral stop <box>` from a
  second shell while `run -- sleep 120` is attached.
