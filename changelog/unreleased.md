# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

- Dashboard: start/stop/delete now stream limactl's progress lines under the busy spinner while the operation runs, instead of showing only the spinner until the state change lands (the report plumbing existed but was wired to a no-op).

- The test suite no longer writes a stray box metadata stub (`boxes/x.json`) into the developer's real `~/.corral` — `TestSessionBeforeMetadataIsAdopted` now runs under a temp `CORRAL_HOME`.

### Security
