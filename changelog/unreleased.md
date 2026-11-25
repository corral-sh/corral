# Unreleased

**Upgrading:** **rebuild needed** for boxes with `node` (the default) or `go`: their provisioning scripts changed (#44), so those boxes show as drifted until `corral rebuild <box>`, and the next create builds a new golden image. Nothing to rebuild for Codex — a box without `codex` in `agents` renders exactly as before. To add Codex to a box: `agents = ["codex"]` in `~/.corral/config.toml` (every box) or `~/.corral/projects/<box>.toml` (one box), then `corral rebuild <box>`.

### Added

- **Codex CLI (OpenAI) as a second agent** (#46): `corral codex`, next to `corral claude`. It installs the
  release binary from `github.com/openai/codex` (a static build — no npm, no Node), verified against the sha256
  digest GitHub records for the asset; its state lives in `~/.corral/agents/codex` via `CODEX_HOME`, shared
  between boxes like Claude's. Log in once with `codex login --device-auth` inside the box, or
  `printenv OPENAI_API_KEY | codex login --with-api-key`; `OPENAI_API_KEY` / `CODEX_API_KEY` are forwarded to
  codex sessions only. Yolo runs `--dangerously-bypass-approvals-and-sandbox` (placed after `exec` / `resume` /
  `fork`, left out of `login`, `review`, `mcp` …); `--ask` keeps the approval prompts with
  `--sandbox danger-full-access`, because Codex's own Linux sandbox needs unprivileged user namespaces, which
  Ubuntu 24.04's AppArmor denies in the box — the VM is the sandbox. `--import-config` copies `config.toml`,
  `AGENTS.md`, `prompts/` and `skills/` from `~/.codex`, never `auth.json`.
- **`agents` config key** (#46): which agents a box installs (default `["claude"]`; `default_agent` is always
  included). Lists are unioned across layers; `agents = []` drops the layers below, so
  `agents = [], default_agent = "codex"` is a Codex-only box. Launching an agent the box does not have says which
  file to edit and to `rebuild`. `corral golden` shows each golden's agents.
- `toolchain_versions` pins **node** and **go**, the way it pins flutter (#44):
  `toolchain_versions = { node = "22.23.2", go = "1.25.14" }`. Node installs `v<version>` verified against that
  release's `SHASUMS256.txt`; go looks the release up in go.dev's full listing (`?mode=json&include=all`), so a
  pin older than the two supported releases still finds its checksum. The pin is part of the golden image's
  identity (each pinned set gets its own golden) and `doctor <box>` fails the toolchain check on a mismatch.
  Values are exact releases — `22` or `v22.23.2` for node, `go1.25.14` for go, and `@<commit>` for either are
  refused with the expected shape. Unpinned behaviour is unchanged, but the provisioning log now prints the
  release it resolved as a ready-to-paste pin.

### Changed

- Broker allow-list: each agent now declares its own API hosts, and a box allows those of the agents it has
  (#46). Claude's (`api.anthropic.com`, `*.anthropic.com`, `platform.claude.com`) moved from the `egress`
  default into the Claude agent — the effective list of a Claude box is unchanged (`corral egress`,
  `corral config`). Codex adds `api.openai.com`, `auth.openai.com`, `chatgpt.com`.
- The dashboard's `enter` launches the box's own `default_agent`, not the global one.

### Fixed

- `toolchain-go.sh` read the checksum from the first entry of go.dev's listing while choosing the version
  from the first *stable* entry; it now reads the chosen release's own entry, so the two cannot diverge (#44).
  It already failed closed when they did.
- `doctor <box>` matched a pinned version as a substring, so a pin of `1.25.1` passed against an installed
  go1.25.14; it now matches the whole version token (#44).
- `doctor <box>` preflighted against the built-in defaults when the project's configuration failed to load
  (the fallback that keeps `stop`/`delete` working), so an invalid config reported "everything works". It
  now stops with the config error (#44).
- Docs no longer call node and go "pinned": unpinned they install the newest release, checksum-verified
  (README, `FEATURES.md`, and a correction note in the 0.7.0 changelog) (#44).

### Security

- `agents` is project-ok (it only installs a CLI in the guest), but a repository cannot use it to reach
  anything of yours (#46). An agent only the repository enabled gets **its own empty login in that box** — your
  shared `~/.corral/agents/<agent>` (with Codex's `auth.json`) is mounted only for agents a trusted layer
  (your global or per-box file) enables, whatever `agent_state` says — and in broker mode its API hosts stay
  off the allow-list. The launcher says so and names the line to add.
- Credential variables go only to their own agent's sessions (#46): `corral codex` does not receive
  `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` from the default `forward_env`, and `corral claude` never
  gets `OPENAI_API_KEY`. `shell` and `run` are unchanged. `~/.codex` joins the host paths `mounts` refuses.

- Built with **Go 1.26.11** (#42). Go 1.26.8 carries twelve standard-library advisories that govulncheck traces into Corral — nine in `net/http` and `net/textproto` (HTTP/1 CONNECT desynchronisation, HTTP/2 flow-control, HPACK and trailer handling, a `MaxBytesReader` race, MIME header memory limits), reachable from the egress broker; GO-2026-6809 (`crypto/x509` distrusts compromised intermediates); GO-2026-6607 (`crypto/tls` ECH); GO-2026-6600 (`html/template`, site generator only). Dependency bumps: pflag 1.1.0, goldmark 1.8.7, x/sys 0.50.0, x/term 0.48.0.
