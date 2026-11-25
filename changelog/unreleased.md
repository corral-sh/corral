# Unreleased

**Upgrading:** `corral upgrade`, then `corral rebuild <box>` for every box with `network = "broker"` — the lockdown script changed (#48); other boxes are not drifted.

### Added

### Changed

### Fixed

- **Broker boxes no longer burn a full CPU core** (#48). With DNS closed by the funnel, Ubuntu 24.04's
  `systemd-resolved` kept an error pending on each blocked query's socket and spun in `epoll` at ~100% of a
  vCPU for the life of the box — several broker boxes could stall the Mac for seconds at a time, long enough
  for the broker's readiness check to fail with `egress broker … did not come up`. The lockdown now stops
  and masks it for the current boot only (`systemctl mask --runtime`), so the next boot's provisioning still
  resolves names before the lockdown applies again; the box's own hostname, which resolved used to answer,
  goes into `/etc/hosts`; and a lookup now fails at once (the 127.0.0.53 stub refuses) instead of after a
  resolver timeout. Offline boxes keep their DNS to the gateway and are unchanged.

### Security
