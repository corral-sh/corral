# Unreleased

**Upgrading:** `corral upgrade`, then `corral rebuild <box>` when convenient: the box's login message and shell prompt changed (#68), so existing boxes show *config changed* until rebuilt. Nothing else in the box changed.

### Added

### Changed

- **New website and brand** (#64). [corral-sh.github.io/corral](https://corral-sh.github.io/corral/) is now a
  short landing page (what Corral is, the three-command start, the agents, a short FAQ) instead of the
  README rendered as HTML; the reference material stays in the README and `docs/`. It is set up to be found:
  description, canonical URL, Open Graph/Twitter cards with a share image, `SoftwareApplication` and `FAQPage`
  structured data, a sitemap, favicons and a web manifest. The new mark — a post-and-rail fence bent into a
  ring, open like a gate, with a spark inside for the agent — replaces the 🐎 in the README and the
  installer, and the CLI banner shows `✦` (the spark).
- The 🐎 is gone inside the box too (#68): the login message and shell prompt show `✦`. The terminal amber
  (dark themes) is now the site's ember `#FFB224`, so the dashboard wordmark and the website share one
  accent; light terminals keep the deeper `#B45309` for contrast.
- The site is hand-authored static files in `site/`, published as is; the README-rendering `tools/site`, its
  `make site` target and the goldmark dependency are gone. `tools/brand/render.sh` re-renders the share image
  and icons with headless Chrome.

### Fixed

### Security
