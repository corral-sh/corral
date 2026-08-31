# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

- Dashboard: `l` now works while a start/stop/delete is running — it opens the log pane in follow mode so the whole operation can be trailed (a `l full log` hint shows under the spinner); previously every key was blocked until the state change landed.
- Dashboard: start/stop/delete now stream limactl's progress lines under the busy spinner while the operation runs, instead of showing only the spinner until the state change lands (the report plumbing existed but was wired to a no-op).

- The test suite no longer writes a stray box metadata stub (`boxes/x.json`) into the developer's real `~/.corral` — `TestSessionBeforeMetadataIsAdopted` now runs under a temp `CORRAL_HOME`.

### Security
