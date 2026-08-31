# Security model

## The boundary

Each project gets its own Linux virtual machine (Apple Virtualization
framework via Lima). The agent runs inside that VM as your user, with `sudo`
unless `network = "offline"` removed it. The VM sees:

| Inside the box | Source | Mode |
|---|---|---|
| your project directory, at its real path | virtiofs mount (`source = "mount"`, default) | read/write (or read-only with `readonly_project`); paths in `hide` are shown empty. **`source = "clone"`: not mounted at all** — the box clones the repository at session start and the only way back is a `git push` |
| `/corral/agents/<agent>` | `~/.corral/agents/<agent>` | read/write — the agent's login and settings, shared by your boxes |
| extra `mounts` you configured | your choice | as configured; `~`, `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.kube`, `~/.docker`, `~/.claude`, shell rc files and `~/.netrc` are refused |
| environment variables you forward | see below | values only, over SSH |
| the network | Lima user-mode network | outbound internet by default; `network = "broker"` allows only the `egress` hosts through an allow-list proxy on the Mac (decision outside the boundary, DNS blocked in the guest); `network = "offline"` rejects everything but the Lima gateway subnet (the Mac, DNS) |
| amd64 binaries and containers | Rosetta 2 via Lima (`rosetta = true`) | run inside the arm64 guest; no extra host exposure |

It does **not** see your home directory, Keychain, SSH keys, cloud credentials,
other repositories, the host Docker socket or the host's processes. A kernel
exploit inside the box lands in a throwaway Ubuntu VM.

## Configuration trust

`.corral.toml` in a repository is written by whoever controls that
repository — the code being sandboxed. The invariant is therefore:

> **Project configuration never runs on the host and never widens what the box
> can reach on the host.**

Every config key has a trust class (`internal/policy/trust.go`; a test fails if
a key is added without one):

| Class | Keys | Rule |
|---|---|---|
| project-ok | `toolchains`, `packages`, `provision`, `hide`, `box_dirs`, `rosetta`, `idle_stop`, `golden`, `default_agent`, `stop_on_exit`, `disk`, `cpus`, `memory` | guest-only effect; `cpus`/`memory` capped against the host; `provision` entries must resolve (symlinks followed) to regular files inside the project; `hide` / `box_dirs` entries are confined to the project (never `.git`, never `..`) |
| project-may-tighten | `yolo`, `readonly_project`, `agent_state` (`shared → seeded → isolated`; `shared_agent_state` is its alias), `git_identity`, `no_env_passthrough`, `protect_git_metadata`, `network` (`full → broker → offline`), `profile`, `env` | a repo may only make the box stricter (a `profile` is a floor for its bundle of keys — see README); `env` accepts literal `KEY=value` only |
| trusted-only | `ssh_agent`, `mounts`, `git_tokens`, `egress`, `api_brokers`, `forward_env`, `env_from_host`, `keychain_env`, `env_file`, `max_running`, `memory_reserve`, `timeout`, `name` | refused in the project file; `~/.corral/config.toml`, `~/.corral/projects/<box>.toml` or flags |

The same test that enforces a trust class for every key is what keeps this
table honest: a new key cannot ship without a decision about who may set it.

Per-project privilege has a legitimate home in `~/.corral/projects/<box>.toml`
— owned by you, outside the repository, so enabling the SSH agent for one
project does not require enabling it for all.

A violation fails the load with every offending key listed — nothing is
silently dropped, because a silently ignored `ssh_agent = true` is exactly what
a hostile repository would hope for. A project file that is a symlink is
refused. This closes the class of "the repo turns on its own privileges"
completely for Corral, because unlike container-based tools there is no
config key that executes anything on the host.

## What the repository can make your Mac run

The project mount is a live view of the checkout on your Mac, so anything the
agent writes there is also on your Mac. Most of it is harmless until *you* run
it, and most of it shows up in a diff you review. Two places do neither:

| Path | Executed on the host by | Visible in a diff |
|---|---|---|
| `.git/hooks/*` | your next `git commit`, `git push`, `git checkout`, … | no — untracked by design |
| `.git/config` (`core.hooksPath`, `core.sshCommand`, `core.fsmonitor`, `core.pager`, `filter.*.clean/smudge`, `diff.*.command`, `alias.*`) | your next matching git command | no |

Corral therefore **shadows both inside the box** (`protect_git_metadata`,
default on, a project can only turn it on): at every boot and again before every
session, a systemd unit bind-mounts a guest-local copy of `.git/config` and an
empty, guest-owned `.git/hooks` over the mounted ones — for the top-level
repository and each submodule under `.git/modules`. Commits, branches, fetches
and pushes work as usual; hooks the agent installs run in the box only. If the
shadow cannot be applied, the session refuses to start rather than run without
it.

One visible cost: git rewrites `.git/config` atomically by rename, which cannot
replace a mountpoint, so **`git config --local`, `git remote add/set-url`,
`git branch -u` and other commands that write the repository config fail inside
the box** with `could not write config file .git/config: Device or resource
busy`. Make those changes on the host (they are yours to make), or use
`git config --global` for box-local preferences. The config copy is refreshed
from the host at every boot and session start.

What this does **not** cover — it is a mitigation for mount mode, not a
boundary:

* `Makefile`, `package.json` scripts, `.pre-commit-config.yaml`, `.vscode/tasks.json`,
  `.envrc` and similar: they run on your Mac when you (or your editor) invoke
  them, and they *are* in the diff. Read the diff before running the build.
* A submodule added during a session: it is shadowed at the next session start.
* `.gitattributes` alone cannot execute anything; the drivers it names live in
  `.git/config`, which is shadowed.

The full answer is a checkout the host never runs from — `source = "clone"` —
or `readonly_project = true`; both make the shadow unnecessary and it is skipped.

## Known cross-boundary paths

These are the ways the box reaches the host today, by design, and what limits
each. They are the items to weigh before running an untrusted repository.

| Path | Why it exists | Limit today | Tracked |
|---|---|---|---|
| **Shared agent login** — `~/.corral/agents/<agent>` is mounted read-write into every box (`agent_state = "shared"`, default) | Claude Code rewrites its credentials on OAuth refresh and its settings on use; read-only breaks it | `agent_state = "seeded"` copies the login in once (read-only seed mount, first boot) so an overwrite stays inside that box; `"isolated"` gives a box its own empty state (log in once per box) and is what `profile = "strict"` uses. A compromised box can read or overwrite the login used by all the others; the OAuth token is the same secret in every copy, so isolation limits *overwrite*, not *read*, until per-box tokens exist | shipped; per-box tokens depend on the vendor |
| **Forwarded secrets** — agent auth variables and anything you list in `forward_env` / `env_from_host` / `git_tokens` | the agent has to authenticate | values live in the box's process environment for the session; the agent can read and send them anywhere the network allows. Forward the narrowest token; use `env_from_host` aliases and per-host `git_tokens`; the audit log records names only. For REST APIs prefer `api_brokers`: the token stays on the Mac and the box gets only allow-listed method+path calls | — (inherent) |
| **API broker credential** (`api_brokers`) | scoped API access for review/bot work | the token is resolved and held by the broker process on the Mac; the box reaches `http://192.168.5.2:<port>/<name>/…` and can make exactly the `allow` calls — a compromised session can post the notes you allowed, not rotate the token or read other projects. Bodies are forwarded unread; method, path and status are audited | 0.6.0 |
| **Open egress** — the box can reach the internet, the Mac (`host.lima.internal`) and the LAN | installs, package registries, the agent's API | `network = "broker"`: only `egress` hosts, decided by a proxy on the Mac the agent cannot reach or change; the *funnel* to that proxy is still in-guest nftables with `sudo` removed, so a guest kernel bug is what it takes to bypass it. `network = "offline"` for no egress at all. A fully host-side funnel (vzNAT + `pf`) is a possible hardening tier, see ARCHITECTURE.md | shipped (tier 1) |
| **The project mount itself** — writes are live on the Mac | that is the product | `readonly_project`, `protect_git_metadata`, `hide`; **`source = "clone"` removes the path entirely** (opt-in; the repo may ask for it, never for `mount`); `"sync"` is planned | shipped; `sync` open |

## What the agent can still do

* Modify or delete the **project directory** — that is the point of mounting
  it. Use git, `readonly_project = true` or `corral snapshot` for
  rollback of the VM disk.
* Use the **network**: by default the guest has outbound internet and can reach
  the Mac at `host.lima.internal` and the LAN, like a Docker container can.
  Nothing on the Mac is exposed unless a service listens on all interfaces.
  `network = "offline"` rejects all egress inside the guest except the Lima
  gateway subnet (`192.168.5.0/24`: the Mac and DNS) via nftables, and
  removes the box user's `sudo` so the agent cannot lift it; the rule is applied
  after provisioning finishes and re-applied before every session (the session
  refuses to start otherwise). This is enforced *inside* the boundary, so it is
  weaker than a host-side allow-list (planned) — but with `sudo` gone it
  takes a kernel or guest-agent bug to undo, not a shell command.
* Exfiltrate anything it can read: the project and any forwarded secret.
  Forward the narrowest token that works (`env_from_host`).
* Undo in-guest controls with `sudo` — the `hide` shadow and the git-metadata
  shadow included — unless `network = "offline"`, which removes it. The VM is
  the boundary; in-guest policy is hygiene against accidental exposure (an
  agent `cat .env`-ing into a prompt), not a defence against a hostile one.

## Secrets

* **Nothing is forwarded implicitly except** the agent's own auth variables
  (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`) and terminal variables
  (`TERM`, `LANG`, `TZ`). `no_env_passthrough = true` disables even that.
* `env_from_host = ["GH_TOKEN=CORRAL_RO_TOKEN"]` gives the box a narrower
  credential under the name tools expect. It **fails closed**: if
  `CORRAL_RO_TOKEN` is unset the box does not start, so the broader `GH_TOKEN`
  can never slip in.
* `git_tokens = { "git.example.com" = "GITLAB_TOKEN" }` installs a
  git credential helper that offers the token **only** to that host over
  HTTPS.
* Values travel inside the SSH channel (`SendEnv`/`AcceptEnv`); they are not
  on any command line and never written to disk by Corral. The audit log
  records variable **names** only.
* The Claude login created with `/login` inside a box is stored in
  `~/.corral/agents/claude/.credentials.json` (mode 0600) on the Mac —
  outside `~/.claude`, so the host Claude Code login is never read or
  modified. `corral agents logout claude` removes it.

## Supply chain of the box itself

| Component | Origin | Verification |
|---|---|---|
| Guest OS | Ubuntu 24.04 cloud image | sha256 digest pinned in the Lima release you installed |
| System packages | Ubuntu apt archive | apt signature |
| Node.js | nodejs.org/dist | `SHASUMS256.txt` checked before extraction |
| Go | go.dev/dl | sha256 from go.dev metadata checked |
| Docker | Ubuntu `docker.io` package | apt signature |
| Claude Code | `https://claude.ai/install.sh` (Anthropic) | vendor's installer; the only `curl \| bash` in the box |
| Lima | homebrew-core's `lima` formula, built from the upstream tarball (sha256 in the formula) | Lima pins the guest OS image by digest per release; `doctor` reports a Lima release other than the tested one |
| corral | this repository, built locally by `install.sh` / the Homebrew formula from the git tag | you can read it; release binaries on the GitHub release come with `SHA256SUMS` **signed by minisign** (`SHA256SUMS.minisig`; public key `release/minisign.pub`, secret key only in a GitHub Actions secret) — `minisign -Vm SHA256SUMS -p minisign.pub && shasum -a 256 -c SHA256SUMS` |
| Golden image | built on your Mac from the components above, once per toolchain set | same inputs as a from-scratch box; `corral golden` lists them, `golden prune` removes old ones |

There is no Corral container image or download server.

Provision scripts (ours and a project's `provision` list) are re-run by Lima on
**every boot**, not only at creation — that is what makes a golden clone's
first boot cheap. Two consequences: every script must be idempotent, and a
repository's `provision` scripts run again each time its box starts (as the
box user; as root only if the script says `# corral: system` and the box is
not offline). They still cannot touch the host — the box is the boundary — but
review them as you would a `Makefile`.

`npm` inside the box is configured with `min-release-age = 7` days to avoid freshly published
packages (the window most hijacked releases live in) — override with
`CORRAL_NPM_MIN_RELEASE_AGE` in a provision script if a project needs to.

## Audit

Every `launch`, `exit`, `create`, `stop`, `idle-stop`, `delete`, `golden-build`
and `golden-prune` is appended to `~/.corral/logs/sessions.jsonl` with box,
project, agent, argv, whether prompts were skipped, forwarded variable names,
exit code and duration. `corral audit` renders it. Guest-side controls log
to the box's journal (`journalctl -u corral-git-shadow`, `-u corral-hide`,
`-u corral-offline`).

## Reporting

Security issues: report privately via GitHub — **Security → Report a
vulnerability** on https://github.com/corral-sh/corral (GitHub private
vulnerability reporting). Please do not post exploits in public issues.
