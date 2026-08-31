# Corral — feature catalog

One line per command, config key, mode and guest control. Generated from the code by `corral docs`
(`make docs` rewrites this file; a test fails when it is stale). `corral docs --json` prints the same
catalog as JSON. Narrative and examples: [README](../README.md) · threat model: [SECURITY.md](SECURITY.md) ·
design: [ARCHITECTURE.md](ARCHITECTURE.md) · measurements: [FEASIBILITY.md](FEASIBILITY.md).

**What it is:** each project gets its own Linux VM (Lima + Apple Virtualization) in which an AI coding agent
runs with no permission prompts; the project is mounted at its real path, the Mac's home, keys and other
repositories are not there. A repository's own config can shape the guest but never widen what the box reaches.

## Commands

| Command | Group | Does |
|---|---|---|
| `corral agents` | Insight & setup | List supported agents and manage their persistent state |
| `corral agents import <agent>` |  | Copy host settings/skills (never credentials) into the agent's box state |
| `corral agents logout <agent>` |  | Remove the stored login for an agent from ~/.corral/agents |
| `corral audit` | Insight & setup | Show the session audit log (who launched what, where, with which env) |
| `corral claude [-- agent args...]` | Agents | Launch Claude Code (Anthropic) — installed from the official installer |
| `corral code` | Agents | Open the project inside the box in VS Code (or Cursor / JetBrains) over SSH |
| `corral config` | Insight & setup | Show the resolved configuration for the current project |
| `corral config init` |  | Write a commented .corral.toml into the project (--host: your per-project file instead) |
| `corral config path` |  | Print the global config file path |
| `corral delete [box]` | Box lifecycle | Delete a box (VM disk); the project and agent login are kept |
| `corral docs` | Insight & setup | Print the feature catalog (every command, config key, mode, control) — for humans and AI assistants |
| `corral doctor [box]` | Insight & setup | Check the host; with a box name, preflight what the project declared from inside the box |
| `corral egress [box]` | Insight & setup | Show a box's network mode, allowed destinations and recent denials |
| `corral gc` | Box lifecycle | Stop boxes idle longer than their idle_stop (also runs on launch and in the dashboard; `list` never stops anything) |
| `corral golden` | Box lifecycle | Golden images: provisioned once per toolchain set, cloned per project |
| `corral golden build` |  | Build (or verify) the golden image for this project's configuration |
| `corral golden prune` |  | Delete golden images no existing box was cloned from (also runs after upgrade and golden build) |
| `corral info [box]` | Insight & setup | Show details of a box: mounts, resources, agents, forwarded env |
| `corral list` | Insight & setup | List boxes and their state (read-only; --json adds live guest metrics for running boxes) |
| `corral logs [box]` | Insight & setup | Show the box's boot/provisioning log (Lima hostagent + serial console) |
| `corral rebuild [box]` | Box lifecycle | Delete and re-create a box with the current configuration |
| `corral restart [box]` | Box lifecycle | Stop and start a box |
| `corral run <command> [args...]` | Agents | Run a single command inside the project's box |
| `corral setup` | Insight & setup | Interactive first-time setup: prerequisites, defaults, login |
| `corral shell` | Agents | Open an interactive shell inside the project's box |
| `corral snapshot` | Box lifecycle | Snapshot a box's disk so you can roll back after an agent session |
| `corral snapshot create [box] <tag>` |  | Create a snapshot |
| `corral snapshot delete [box] <tag>` |  | Delete a snapshot |
| `corral snapshot list [box]` |  | List snapshots |
| `corral snapshot restore [box] <tag>` |  | Roll the box disk back to a snapshot |
| `corral start [box]` | Box lifecycle | Start a stopped box without launching anything |
| `corral stop [box]` | Box lifecycle | Stop a box (default: the current project's box) |
| `corral undo [box]` | Box lifecycle | Roll the box disk back to the newest automatic snapshot (or --tag) |
| `corral uninstall` | Insight & setup | Delete every box and Corral state (agent logins included) |
| `corral up` | Box lifecycle | Create/start the project's box without launching anything |
| `corral upgrade` | Insight & setup | Upgrade corral (Homebrew tap or source checkout) and Lima |
| `corral version` |  | Print version information |

Every command takes `-C <dir>` (project) and `--box <name>`. Layers: defaults < `~/.corral/config.toml` <
`<project>/.corral.toml` (restricted) < `~/.corral/projects/<box>.toml` (trusted) < flags.

## Configuration keys

Trust: **project-ok** — a repository may set it (guest-only effect) · **project-may-tighten** — a repository may only
make it stricter · **trusted-only** — refused in the repository file (it would widen what the box reaches on the Mac).

| Key | Type | Default | Trust | Meaning |
|---|---|---|---|---|
| `default_agent` | string | `claude` | project-ok | Agent started by the bare dashboard `enter` and used for login hints. |
| `cpus` | int | `4` | project-ok | vCPUs per box; a project may set it, capped against the host. |
| `memory` | string | `4GiB` | project-ok | RAM ceiling per box (e.g. `4GiB`); the guest never returns memory to the Mac, so this is the eventual cost. Capped against the host. |
| `disk` | string | `60GiB` | project-ok | Sparse VM disk size; only used blocks cost anything. |
| `yolo` | bool | `true` | project-may-tighten | Skip the agent's own permission prompts inside the box (the VM is the boundary). `--ask` keeps them for one run. |
| `stop_on_exit` | bool | `false` | project-ok | Stop the box when the session ends. |
| `readonly_project` | bool | `false` | project-may-tighten | Mount the project read-only in the box. |
| `shared_agent_state` | bool | `true` | project-may-tighten | Deprecated alias of `agent_state`: true = shared, false = isolated. |
| `agent_state` | string |  | project-may-tighten | Where the agent login lives: `shared` (host dir mounted rw), `seeded` (copied in once), `isolated` (log in per box). |
| `git_identity` | bool | `true` | project-may-tighten | Forward git user.name / user.email (never keys) into the box. |
| `ssh_agent` | bool | `false` | trusted-only | Forward the SSH agent socket into the box. |
| `no_env_passthrough` | bool | `false` | project-may-tighten | Disable automatic forwarding of the agent's credential variables (`forward_env`). |
| `protect_git_metadata` | bool | `true` | project-may-tighten | Shadow `.git/config` and `.git/hooks` in the box so in-box edits cannot reach what the Mac executes. |
| `name` | string |  | trusted-only | Override the box name (default: `<dir-slug>-<hash>`). |
| `network` | string | `full` | project-may-tighten | `full` (internet), `broker` (only `egress` hosts through the allow-list proxy on the Mac, DNS closed, sudo removed), `offline` (nothing but the Mac, sudo removed). |
| `rosetta` | bool | `false` | project-ok | Run amd64 binaries and `--platform linux/amd64` containers inside the arm64 box (Apple Silicon). |
| `idle_stop` | string | `30m` | project-ok | Stop a box with no live session after this long (`30m`, `off`). |
| `golden` | bool | `true` | project-ok | Build the box by cloning the shared golden image (toolchains + agents) instead of from scratch. |
| `source` | string | `mount` | project-may-tighten | `mount` (checkout mounted live at its real path) or `clone` (nothing mounted; the box clones the repo at session start). |
| `snapshot` | string | `off` | project-ok | `auto` takes an APFS clone of the box disk at each session start from stopped; `corral undo` restores it. |
| `snapshots_keep` | int | `3` | project-ok | How many auto snapshots to keep. |
| `profile` | string | `default` | project-may-tighten | Named security floor: `default`, `offline`, `strict` (broker + isolated login + no ssh agent + no passthrough + git shadow). Keys may tighten beyond it, never loosen. |
| `forward_env` | []string | `[ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN]` | trusted-only | Host variables forwarded by name when set (over SSH SendEnv, never argv). |
| `env` | []string |  | project-may-tighten | `KEY=value` literals set in the box; a bare `KEY` forwards the host value (trusted layers only). |
| `env_from_host` | []string |  | trusted-only | `GUEST_VAR=HOST_VAR` aliases; the session refuses to start if the host variable is unset. |
| `keychain_env` | []string |  | trusted-only | Variables whose value is read from the macOS Keychain (service = name) at launch when not exported; a missing item refuses to start. |
| `env_file` | string |  | trusted-only | `KEY=value` file consulted after the exported environment and before the Keychain — the credential path for an unattended host (launchd). Must be 0600, yours, under your home; refused otherwise. |
| `max_running` | int |  | trusted-only | Admission: refuse to start another box when this many are running (exit 75, requeue); 0 = no limit. |
| `memory_reserve` | string | `8GiB` | trusted-only | Admission: RAM that must stay free for macOS — a box is refused (exit 75) when the running boxes' measured footprint plus its `memory` would exceed host RAM minus this. |
| `timeout` | string |  | trusted-only | Default for `run --timeout`: end the session after this long (SIGTERM, then SIGKILL after 10 s), exit 124. |
| `packages` | []string |  | project-ok | Extra Ubuntu apt packages installed in the box. |
| `toolchains` | []string | `[node]` | project-ok | Preinstalled toolchains (see Toolchains below); they define the golden image. Unioned across layers; an explicit `[]` drops the default node. |
| `toolchain_versions` | map[string]string |  | project-ok | `{ flutter = "3.44.2" }` or `"3.44.2@<commit>"`: pin a toolchain's release (flutter today); part of the golden image's identity, verified against the commit when given. |
| `mounts` | []string |  | trusted-only | Extra host directories mounted into the box (`host:guest[:ro]`); home and credential directories are refused. |
| `provision` | []string |  | project-ok | Repository scripts run at the end of provisioning as the box user (`# corral: system` for root in full mode); a failure fails the start. |
| `hide` | []string |  | project-ok | Project paths shown empty inside the box (`.env`, `secrets/`). |
| `box_dirs` | []string |  | project-ok | Project directories kept on the box disk and bind-mounted over the mount (`node_modules`, `build`): fast installs, empty on the Mac. |
| `egress` | []string | `[api.anthropic.com *.anthropic.com platform.claude.com]` | trusted-only | Allow-list for `network = "broker"`: exact hosts or `*.suffix`, `:port` to widen beyond 80/443; git_tokens hosts are added automatically. |
| `git_tokens` | map[string]config.GitToken |  | trusted-only | `{ "<host>" = "<HOST_VAR>" }` or `{ "<host>" = { token = "<HOST_VAR>", user = "gitlab+deploy-token-<id>" } }`: HTTPS git credential for the box, offered only to that host; the variable may come from `keychain_env`. |
| `api_brokers` | []config.APIBroker |  | trusted-only | `[[api_brokers]] name, upstream, token, auth (header\|bearer\|basic), header/user, allow = ["METHOD /path/**"]`: a credential-holding proxy on the Mac — the box calls `$CORRAL_API_<NAME>/…`, the Mac adds the token and forwards only allow-listed method+path calls; every call audited. |

## Modes

| | |
|---|---|
| `network = full | broker | offline` | Internet · allow-list proxy on the Mac (DNS closed, sudo removed) · nothing but the Mac (sudo removed). A repository may only tighten. |
| `source = mount | clone` | Checkout mounted live at its real path · nothing mounted, the box clones the repository at session start with `git_tokens`. |
| `agent_state = shared | seeded | isolated` | One login for all boxes (host dir mounted) · copied in once · per box. Tighten-only. |
| `profile = default | offline | strict` | Named floors; `strict` = broker + isolated + no ssh agent + no env passthrough + git shadow. |
| `snapshot = off | auto` | APFS clone of the box disk at each session start; `corral undo` rolls back. |
| `golden = true | false` | Clone the shared golden image (15 s) or build from scratch (2–4 min). |
| `api_brokers` | Credential-holding API proxy on the Mac for GitLab/Jira-style REST calls: scoped by method+path, token never in the box, works in `full` and `broker` network modes (not `offline`). |

## Guest controls

What enforces the configuration inside the box (systemd units, re-applied before every session) and on the Mac.

| | |
|---|---|
| `corral-broker` | nftables funnel: only the broker port on the Mac gateway; DNS closed; sudo removed. Re-applied before every session; a session refuses to start if it is not active. |
| `corral-offline` | nftables reject-all except the Mac; sudo removed. Same session gate. |
| `corral-git-shadow` | Guest-local `.git/config`, empty `.git/hooks` over the mount (`protect_git_metadata`). |
| `corral-hide` | Empty box-owned file/tmpfs over each `hide` path. |
| `corral-boxdirs` | Box-disk directory bind-mounted over each `box_dirs` path. |
| `provision failure record` | A repository provision script that exits non-zero is recorded in `/corral/runtime/provision/`; corral refuses to start after create/start. |
| `egress broker (host)` | Per-box CONNECT/forward proxy on `127.0.0.1:<port>`, allow-list decided on the Mac, denials audited by name (`corral egress`). |
| `audit log` | `~/.corral/logs/sessions.jsonl`: launches, variable *names*, denials, snapshots, deletes (`corral audit`). |

## Toolchains

`toolchains = [...]`; every download is pinned and checksum-verified, no `curl | bash` except the agent vendor's own installer.

| | |
|---|---|
| `android` | Android SDK cmdline-tools (SHA-256 pinned), platform-tools, android-35, build-tools 35.0.0, JDK; amd64 multiarch so aapt2 runs under Rosetta. |
| `docker` | Docker Engine + compose + buildx inside the box; the box user is in the docker group. |
| `flutter` | Flutter stable (3.47.1 by default; `toolchain_versions` pins another release), release commit pinned and verified, Dart + Android artifacts precached. |
| `go` | Latest stable Go from go.dev (SHA-256 verified). |
| `java` | OpenJDK 17, JAVA_HOME. |
| `node` | Node.js 22 LTS from nodejs.org (SHA-256 verified), npm `min-release-age = 7`. |
| `python` | Python 3, pip, venv, pipx (Ubuntu apt). |

## Environment inside the box

| | |
|---|---|
| `CORRAL=1` | Set in every shell inside a box (also `/etc/corral-release`). |
| `CORRAL_NAME / CORRAL_PROJECT / CORRAL_VERSION / CORRAL_AGENT` | Session identity variables. |
| `CORRAL_NETWORK / CORRAL_SOURCE` | The box's network and source mode. |
| `CORRAL_YOLO` | 1 when the agent wrapper adds its skip-prompts flags; 0 with `--ask`. |
| `CORRAL_USER / CORRAL_HOME` | In provision scripts: the box user (carries the Mac uid) and their home. |
| `HTTP_PROXY / HTTPS_PROXY / NO_PROXY` | In broker mode: the allow-list proxy on the Mac gateway. |
| `CORRAL_API_<NAME>` | Base URL of an `api_brokers` route (`http://192.168.5.2:<port>/<name>`); the credential is added on the Mac, never present in the box. |
| `CLAUDE_CONFIG_DIR` | Claude Code state relocated to `/corral/agents/claude`; `DISABLE_AUTOUPDATER=1`. |

## Host layout

| | |
|---|---|
| `~/.corral/config.toml` | Global config (`corral setup`). |
| `<project>/.corral.toml` | Repository config — can only shape the guest (see trust column). |
| `~/.corral/projects/<box>.toml` | Your per-project trusted layer (egress, tokens, mounts). |
| `~/.corral/lima/<box>/` | Lima instance: disks, per-box SSH key, ssh.config (LIMA_HOME). |
| `~/.corral/boxes/<box>.json` | Metadata, rendered template and its hash (drift detection). |
| `~/.corral/agents/<agent>/` | Shared agent login/state. |
| `~/.corral/snapshots/<box>/` | APFS-clone snapshots. |
| `~/.corral/ssh/config` | One `Include` per box for `corral code` / `ssh lima-<box>`. |

## Exit codes and outcomes

`corral run` for queues and scripts: the agent's status passes through; Corral's own outcomes use sysexits(3) codes agents do not.

| | |
|---|---|
| `0–255 (agent's)` | The command/agent finished; this is its own exit status (`outcome = "exit"`). |
| `78` | `EX_CONFIG` — `--preflight` refused the session: a declared grant does not work from inside the box. Fix the configuration; do not retry (`outcome = "preflight-refused"`). |
| `75` | `EX_TEMPFAIL` — admission refused: `max_running` reached or the memory budget (`memory_reserve`) would be exceeded. Requeue (`outcome = "admission-refused"`). |
| `69` | `EX_UNAVAILABLE` — the box became unreachable during the session (SSH lost, instance gone, guest OOM-kill when detectable). Requeue and alert (`outcome = "unreachable"`). |
| `124` | `--timeout` elapsed; the session was terminated (`outcome = "timeout"`). |
| `1` | Any other Corral error (configuration refused by policy, Lima failure); the message names the next command. |
| `run --result <file>` | Writes one JSON object per run — `box, agent, outcome, exit_code, reason, started, ended, duration, forwarded_env` — the same record the audit log gets. |
