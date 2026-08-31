# Contributing to Corral

Thanks for taking an interest. Corral is a security tool, so the bar for
changes is "small, reviewed, and verified" rather than "fast".

## Getting set up

macOS 13.5+ with [Homebrew](https://brew.sh). Then:

```bash
git clone https://github.com/corral-sh/corral.git
cd corral
./install.sh          # installs Lima/Go if missing, builds, installs
make build && bin/corral doctor
```

## Before you open a PR

Run the same gates CI runs:

```bash
make test             # unit tests (no VM needed)
make lint             # vet + gofmt + golangci-lint (incl. gosec)
make security         # lint + govulncheck + shellcheck + gitleaks
make docs             # regenerate docs/FEATURES.md after any command/config change
```

`make security` needs `brew install golangci-lint shellcheck gitleaks` and
`go install golang.org/x/vuln/cmd/govulncheck@latest`.

For changes that touch the Lima template, guest scripts, mounts or the broker,
also run the end-to-end suite on your Mac (it builds real throwaway VMs under a
temp `CORRAL_HOME`, ~4 minutes):

```bash
make e2e
```

## Conventions that matter

The full set lives in [CLAUDE.md](CLAUDE.md) (they apply to humans too). The
short version:

- **Security rules live in `internal/policy`** — trust classes per config key,
  path confinement, refused mounts. New restrictions go there, not in `box` or
  `cli`. Every new config key needs a trust class (a test enforces it).
- **Secrets never appear on a command line or in a log.** Values travel via SSH
  `SendEnv` (`CORRAL_FWD_*`); audit entries carry names only.
- **Guest scripts must be idempotent** and must not `curl | bash` (except the
  agent vendor's own installer); download and checksum-verify instead.
- Anything that changes the rendered Lima template changes the template hash and
  makes existing boxes show as drifted — intended, but say so in
  `changelog/unreleased.md`.
- Error messages tell the user the next command to run.

## Workflow: every change starts as an issue

Every change — feature, fix, docs — references a work item, so the history
explains itself:

1. **Open an issue first** (or pick an existing one). New issues land as
   `status: triage`; the maintainer moves them to `status: accepted` (free to
   pick up — comment that you're taking it and it becomes
   `status: in progress`), `status: deferred`, or closes them with a reason.
   `status: blocked` marks work waiting on something external.
2. Branch and open a **pull request** — `main` only moves by PR with every CI
   check green (a ruleset enforces it), so a closed issue always corresponds
   to a green build. Reference the issue with **`Fixes #N`** in the
   PR (or the commit) so GitHub closes it on merge.
3. Issues good for a first contribution carry `good first issue`; ones where
   help is explicitly wanted carry `help wanted`. Releases are tracked with
   milestones (e.g. `0.7.1`).

Exempt from issue-first: dependency bumps from Dependabot and pure typo fixes.

## Reporting bugs and proposing features

Use the issue forms. For anything security-sensitive, do **not** open a
public issue — use GitHub's private vulnerability reporting
(Security → Report a vulnerability); see [docs/SECURITY.md](docs/SECURITY.md).

## Licence

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
