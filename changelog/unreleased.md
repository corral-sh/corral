# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

- README: the `api_brokers` Jira example was invalid TOML (several keys on one line), so copying it into
  `~/.corral/config.toml` failed to parse; it is one key per line now, and a test parses every TOML example in
  the README and docs (#54).

### Security
