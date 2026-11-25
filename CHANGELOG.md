# Changelog

One file per release in [`changelog/`](changelog/); each GitHub release carries
its own notes. Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning: [SemVer](https://semver.org). Unreleased work accumulates in
[`changelog/unreleased.md`](changelog/unreleased.md).

| Version | Date | Highlights |
|---|---|---|
| [Unreleased](changelog/unreleased.md) | — | — |
| [0.10.1](changelog/0.10.1.md) | 2026-11-25 | New website and brand: the fence-ring mark, `✦` in the terminal and inside the box, ember accent. **Boxes show config changed until rebuilt** (login message and prompt) |
| [0.10.0](changelog/0.10.0.md) | 2026-11-25 | **Antigravity CLI** (Google) as a third agent: `corral agy`, shared sign-in, measured broker hosts; agent command aliases; aliased agent commands keep their arguments |
| [0.9.2](changelog/0.9.2.md) | 2026-11-25 | Tested with Lima 2.3.0 (`corral upgrade` pulls it in); `doctor` suggests a fix that works for an untested Lima; valid TOML in the README's `api_brokers` example |
| [0.9.1](changelog/0.9.1.md) | 2026-11-25 | Broker boxes no longer burn a full CPU core on `systemd-resolved`; DNS lookups in a broker box fail at once instead of timing out. **Rebuild broker boxes** |
| [0.9.0](changelog/0.9.0.md) | 2026-11-25 | **Codex CLI** as a second agent (`corral codex`, `agents` key, per-agent broker hosts and credentials); `toolchain_versions` pins node and go; built with Go 1.26.11 (twelve stdlib advisories). **Rebuild boxes with node or go** |
| [0.8.1](changelog/0.8.1.md) | 2026-09-25 | Built with Go 1.26.8 (stdlib advisories in `crypto/tls`, `net/http`); destructive commands fail without a terminal instead of silently doing nothing; no spurious hostagent error on stop |
| [0.8.0](changelog/0.8.0.md) | 2026-09-25 | Per-box egress log (`corral egress --log/--since/--json`); `broker`/`offline` drop `docker` and every root-equivalent group before project scripts; apt-daily timers masked; atomic box metadata; `upgrade` runs `brew update` first. **Rebuild every box** |
| [0.7.0](changelog/0.7.0.md) | 2026-08-31 | First public release: per-project Lima VMs, golden images, egress broker, `api_brokers`, snapshots/undo, unattended-host support, signed releases |
