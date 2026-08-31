<!-- Every change references a work item: open an issue first if none exists. -->
Fixes #

## What & why

<!-- A sentence or two: the problem, and the shape of the fix. -->

## Checklist

- [ ] `make test lint` green locally (`make security` for anything touching deps, scripts or CI)
- [ ] `make docs` re-run if a command or config key changed (`TestFeaturesCatalogUpToDate` enforces it)
- [ ] `make e2e` run if the Lima template, guest scripts, mounts or broker changed
- [ ] `changelog/unreleased.md` updated (user-visible changes; note when a rebuild is needed)
