# Unreleased

**Upgrading:** —

### Added

### Changed

- Tested with **Lima 2.3.0** (#56), the release `corral upgrade` now pulls in as a Homebrew dependency; `make e2e`
  passes on it. `doctor` no longer flags 2.3.x as untested.

### Fixed

- `doctor`'s untested-Lima check suggested `brew install lima`, which changes nothing when a newer Lima is
  installed (Homebrew offers no older one). An older Lima now gets `brew upgrade lima`; a newer one is told
  it usually works and where to report a problem (#56).

- README: the `api_brokers` Jira example was invalid TOML (several keys on one line), so copying it into
  `~/.corral/config.toml` failed to parse; it is one key per line now, and a test parses every TOML example in
  the README and docs (#54).

### Security
