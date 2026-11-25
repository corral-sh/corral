# Unreleased

**Upgrading:** **rebuild needed** for boxes with `node` (the default) or `go`: their provisioning scripts changed (#44), so those boxes show as drifted until `corral rebuild <box>`, and the next create builds a new golden image.

### Added

- `toolchain_versions` pins **node** and **go**, the way it pins flutter (#44):
  `toolchain_versions = { node = "22.23.2", go = "1.25.14" }`. Node installs `v<version>` verified against that
  release's `SHASUMS256.txt`; go looks the release up in go.dev's full listing (`?mode=json&include=all`), so a
  pin older than the two supported releases still finds its checksum. The pin is part of the golden image's
  identity (each pinned set gets its own golden) and `doctor <box>` fails the toolchain check on a mismatch.
  Values are exact releases — `22` or `v22.23.2` for node, `go1.25.14` for go, and `@<commit>` for either are
  refused with the expected shape. Unpinned behaviour is unchanged, but the provisioning log now prints the
  release it resolved as a ready-to-paste pin.

### Changed

### Fixed

- `toolchain-go.sh` read the checksum from the first entry of go.dev's listing while choosing the version
  from the first *stable* entry; it now reads the chosen release's own entry, so the two cannot diverge (#44).
  It already failed closed when they did.
- `doctor <box>` matched a pinned version as a substring, so a pin of `1.25.1` passed against an installed
  go1.25.14; it now matches the whole version token (#44).
- `doctor <box>` preflighted against the built-in defaults when the project's configuration failed to load
  (the fallback that keeps `stop`/`delete` working), so an invalid config reported "everything works". It
  now stops with the config error (#44).
- Docs no longer call node and go "pinned": unpinned they install the newest release, checksum-verified
  (README, `FEATURES.md`, and a correction note in the 0.7.0 changelog) (#44).

### Security

- Built with **Go 1.26.11** (#42). Go 1.26.8 carries twelve standard-library advisories that govulncheck traces into Corral — nine in `net/http` and `net/textproto` (HTTP/1 CONNECT desynchronisation, HTTP/2 flow-control, HPACK and trailer handling, a `MaxBytesReader` race, MIME header memory limits), reachable from the egress broker; GO-2026-6809 (`crypto/x509` distrusts compromised intermediates); GO-2026-6607 (`crypto/tls` ECH); GO-2026-6600 (`html/template`, site generator only). Dependency bumps: pflag 1.1.0, goldmark 1.8.7, x/sys 0.50.0, x/term 0.48.0.
