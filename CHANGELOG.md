# Changelog

One file per release in [`changelog/`](changelog/); each GitHub release carries
its own notes. Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning: [SemVer](https://semver.org). Unreleased work accumulates in
[`changelog/unreleased.md`](changelog/unreleased.md).

| Version | Date | Highlights |
|---|---|---|
| [Unreleased](changelog/unreleased.md) | — | — |
| [0.8.1](changelog/0.8.1.md) | 2026-09-25 | Built with Go 1.26.8 (stdlib advisories in `crypto/tls`, `net/http`); destructive commands fail without a terminal instead of silently doing nothing; no spurious hostagent error on stop |
| [0.8.0](changelog/0.8.0.md) | 2026-09-25 | Per-box egress log (`corral egress --log/--since/--json`); `broker`/`offline` drop `docker` and every root-equivalent group before project scripts; apt-daily timers masked; atomic box metadata; `upgrade` runs `brew update` first. **Rebuild every box** |
| [0.7.0](changelog/0.7.0.md) | 2026-08-31 | First public release: per-project Lima VMs, golden images, egress broker, `api_brokers`, snapshots/undo, unattended-host support, signed releases |
