# Unreleased

**Upgrading:** —

### Added

### Changed

### Fixed

### Security

- Built with **Go 1.26.11** (#42). Go 1.26.8 carries twelve standard-library advisories that govulncheck traces into Corral — nine in `net/http` and `net/textproto` (HTTP/1 CONNECT desynchronisation, HTTP/2 flow-control, HPACK and trailer handling, a `MaxBytesReader` race, MIME header memory limits), reachable from the egress broker; GO-2026-6809 (`crypto/x509` distrusts compromised intermediates); GO-2026-6607 (`crypto/tls` ECH); GO-2026-6600 (`html/template`, site generator only). Dependency bumps: pflag 1.1.0, goldmark 1.8.7, x/sys 0.50.0, x/term 0.48.0.
