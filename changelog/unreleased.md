# Unreleased

**Upgrading:** —

### Added

- Contributor workflow: issue forms with status labels, a pull request template, a Code of Conduct, Dependabot, and an issue-first process documented in CONTRIBUTING.md.

### Changed

### Fixed

- Dashboard: while a start/stop/delete runs, the busy line now shows the elapsed time and `l` opens the log pane in follow mode so the whole operation can be trailed; previously every key was blocked and nothing moved until the state change landed.
- The test suite no longer writes a stray box metadata stub (`boxes/x.json`) into the developer's real `~/.corral` — `TestSessionBeforeMetadataIsAdopted` now runs under a temp `CORRAL_HOME`.

### Security
