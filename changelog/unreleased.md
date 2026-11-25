# Unreleased

**Upgrading:** —

### Added

### Changed

- **New website and brand** (#64). [corral-sh.github.io/corral](https://corral-sh.github.io/corral/) is now a
  short landing page (what Corral is, the three-command start, the agents, a short FAQ) instead of the
  README rendered as HTML; the reference material stays in the README and `docs/`. It is set up to be found:
  description, canonical URL, Open Graph/Twitter cards with a share image, `SoftwareApplication` and `FAQPage`
  structured data, a sitemap, favicons and a web manifest. The new mark — a post-and-rail fence bent into a
  ring, open like a gate, with a spark inside for the agent — replaces the 🐎 in the README and the
  installer, and the CLI banner shows `✦` (the spark). The VM's login banner and prompt keep the old glyph
  until the next release that changes the box template anyway, so this release forces no rebuild.
- The site is hand-authored static files in `site/`, published as is; the README-rendering `tools/site`, its
  `make site` target and the goldmark dependency are gone. `tools/brand/render.sh` re-renders the share image
  and icons with headless Chrome.

### Fixed

### Security
