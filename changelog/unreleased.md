# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

- `TestSaveMetaIsAtomic` no longer writes a phantom `atom` box into the developer's real `~/.corral` when it fails: its writer goroutine is stopped and awaited before the test returns and `CORRAL_HOME` is restored (#29).

### Security
