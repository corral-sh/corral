# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

- `corral stop` and the shutdown step of `rebuild` no longer flash `error: … [hostagent] accept tcp … use of closed network connection` (#35). Lima's host agent logs that at `level=error` when it closes its own listener on every clean stop; the progress line now treats it as noise. Every other Lima error still shows, and a stop that fails still reports its own error.

- **Destructive commands no longer exit 0 having done nothing when there is no terminal** (#33). `golden prune`, `rebuild`, `delete`, `delete --all` and `uninstall` treated an unanswerable confirmation (a script, CI, an agent's shell) as "no" and returned success silently — `golden prune` even listed what it would delete first. They now fail with `` nothing done: … re-run with `<command> --yes` ``. A "no" typed at the prompt is still a quiet exit.

- `TestSaveMetaIsAtomic` no longer writes a phantom `atom` box into the developer's real `~/.corral` when it fails: its writer goroutine is stopped and awaited before the test returns and `CORRAL_HOME` is restored (#29).

### Security

- Built with **Go 1.26.8** (#31). The go-deps bumps (goldmark 1.8.6, x/sys 0.48.0, x/term 0.46.0) require Go 1.26, and Go 1.26.0 carries three standard-library advisories fixed in 1.26.6 — GO-2026-6090 (`crypto/tls` post-handshake messages) and GO-2026-6089 (`net/http` h2c `ReadHeaderTimeout`), both reachable from the egress broker, and GO-2026-6091 (`html/template`, site generator only). Building from source now needs Go 1.26 (`GOTOOLCHAIN=auto` fetches it).
