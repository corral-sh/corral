# Unreleased

**Upgrading:** —

### Added

- Contributor workflow: issue forms with status labels, a pull request template, a Code of Conduct, Dependabot, and an issue-first process documented in CONTRIBUTING.md. `main` is protected: every change lands via pull request with all CI checks green, so a work item is never closed by a broken build.

### Changed

### Fixed

- Dashboard: while a start/stop/delete runs, the busy line now shows the elapsed time and `l` opens the log pane in follow mode so the whole operation can be trailed; previously every key was blocked and nothing moved until the state change landed.
- The test suite no longer writes a stray box metadata stub (`boxes/x.json`) into the developer's real `~/.corral` — `TestSessionBeforeMetadataIsAdopted` now runs under a temp `CORRAL_HOME`.

### Security
