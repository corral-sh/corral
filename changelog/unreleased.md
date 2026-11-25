# Unreleased

**Upgrading:** `corral upgrade`. Nothing to rebuild — a box without `antigravity` in `agents` renders exactly as before. To add it: `agents = ["antigravity"]` in `~/.corral/config.toml` (every box) or `~/.corral/projects/<box>.toml` (one box), then `corral rebuild <box>`.

### Added

- **Antigravity CLI (Google) as a third agent** (#60): `corral agy` (or `corral antigravity`), next to Claude
  Code and Codex — Google's terminal agent, which replaced Gemini CLI in 2026. It installs with Google's own
  installer (downloaded to a file, then run; it checks the sha512 in Google's release manifest). Log in once by
  running `agy` in the box: open the URL it prints on your Mac, sign in, paste the code back. Over SSH agy keeps
  the login in a file, and since it has no variable to relocate its state, `~/.gemini` and
  `~/.config/antigravity` in the box are links into `~/.corral/agents/antigravity` — so the login is shared
  between boxes and follows `agent_state` like the others. Self-update is off in the box
  (`AGY_CLI_DISABLE_AUTO_UPDATE=true`; only `true` works, measured). Yolo is `--dangerously-skip-permissions`;
  agy's own `--sandbox` stays opt-in. Broker mode allows six `*.googleapis.com` hosts, four of them measured
  from a real sign-in and prompt (`oauth2`, `www`, `daily-cloudcode-pa`, `aicode`) and two production siblings
  the binary picks by account tier (`cloudcode-pa`, `businessaicode`). Its usage logging to
  `play.googleapis.com` has no opt-out and stays denied — expect recurring denials in `corral egress`.
  API-key auth is not forwarded: it is not verified to work without a sign-in.
- Agents may declare a shorter command name (`agent.Aliaser`): `corral agy`. The feature catalog lists every
  command's aliases (`corral ls`, `corral rm` …).

### Changed

### Fixed

- An agent command typed by an alias lost every argument after it (`corral agy -p x` ran plain `agy`): the
  launcher searched the command line for the agent's name only. It now uses the name actually typed (#60).

### Security

- `~/.gemini` and `~/.config/antigravity` (Antigravity's login and plugin secrets) join the host paths `mounts`
  refuses, like `~/.claude` and `~/.codex` (#60).
