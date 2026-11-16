# Changelog

Notable changes to ARK Studio are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Documentation is grouped into `docs/guide/`, `docs/operations/`,
  `docs/agents/` and `docs/development/`.
- Scripts are grouped into `scripts/bench/`, `scripts/release/`,
  `scripts/agents/` and `scripts/dev/`.
- Tests are grouped into `tests/python/<area>/`, `tests/js/` and
  `tests/browser/`; the test commands are unchanged.
- Example host names in the demo project, docs and GPU example configuration
  are generic placeholders.

### Removed

- Old logo boards, the brand-lockup tuning tool, finished planning documents,
  outdated screenshots, one-off smoke scripts and unused code.

### Added

- Browser project cache: pages, node details and tab switches render from
  IndexedDB at once and revalidate with ETags and snapshot deltas
  (`GET /api/changes`); project changes arrive over `GET /api/events`
  instead of 3.5 s polling. Cached data is per account and removed on
  sign-out, account switch and revoked grants; offline shows it read-only.
  Settings › Account can clear it. See `docs/development/client-cache.md`.
- Contributing guide, code of conduct, security policy, support file, issue
  and pull request templates, and `.editorconfig`.

## [2.0.0]

- Version declared in `pyproject.toml` before this changelog was started.
