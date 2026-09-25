# Unreleased

**Upgrading:** `corral upgrade`, then `corral rebuild <box>` for every box — the template changed (every box shows as drifted in `list`).

### Added

- Contributor workflow: issue forms with status labels, a pull request template, a Code of Conduct, Dependabot, and an issue-first process documented in CONTRIBUTING.md. `main` is protected: every change lands via pull request with all CI checks green, so a work item is never closed by a broken build.

### Changed

- Under `network = "broker"` / `"offline"`, docker is installed but not usable by the box user, and repository `provision` scripts run without `sudo` (use `packages = [...]`). **Template hash changes for broker/offline boxes: `corral rebuild` them.**

### Fixed

- Dashboard: while a start/stop/delete runs, the busy line now shows the elapsed time and `l` opens the log pane in follow mode so the whole operation can be trailed; previously every key was blocked and nothing moved until the state change landed.
- The test suite no longer writes a stray box metadata stub (`boxes/x.json`) into the developer's real `~/.corral` — `TestSessionBeforeMetadataIsAdopted` now runs under a temp `CORRAL_HOME`.

### Security

- **`network = "broker"` / `"offline"` no longer leave guest root reachable through the `docker` group** (#16). The lockdown removed `sudo` but not the `docker` group, and `toolchains = ["docker"]` is a key a repository may set, so a repo-owned `.corral.toml` got guest root back (the daemon runs a container with `/` mounted). A new `corral-drop-privileges` step now removes the box user from `sudo`, `admin`, `docker`, `lxd`, `incus-admin` and `disk`, makes `/run/docker.sock` root-only (a process that kept a stale docker gid cannot reach it either) and fails closed if any member remains. It runs **before the project's `provision` scripts**: those were already user-only in these modes, but still ran with the box user's NOPASSWD `sudo`, so a repository script could pre-empt the lockdown the same way. The broker/offline units call it again before every session. `doctor <box>` / `run --preflight` gain a `control privileges` check; the launcher warns that docker is root-only in these modes. The VM/Mac boundary was never affected. `toolchains` stays project-ok — the group, not the install, is the problem.

- **No unattended package installer inside a box** (#17). Ubuntu's `apt-daily` and `apt-daily-upgrade` timers were enabled in every box: they fire on a schedule and try to download and install `.deb`s. Under `network = "broker"` the broker refused them, burying the denials that matter in `corral egress`; under `network = "full"` they could change a box's toolchain under a running agent. `base.sh` now masks both timers and their services and turns the `APT::Periodic` knobs off before its own `apt-get` runs; `apt-get` in toolchains and `packages` is unaffected. **Template change:** every box shows drifted — `corral rebuild`.
