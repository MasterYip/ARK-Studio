# Changelog

Notable changes to ARK Studio are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [2.1.1] - 2026-11-21

### Added

- Settings > Admin > Project storage location: shows the folder where new
  projects are created, its free space and the folders that hold projects,
  and lets you choose another folder (folder picker in the desktop app).
  Existing projects are never moved or deleted. Local mode and organization
  admins only.
- Needs you on phones and in the Android app follows the review flow:
  review and decision cards with the "Review this" note and its anchors,
  rounds, changes since the last review and Accept / Send back. The Android
  app's Home shows these cards instead of the old "Needs attention" list.

### Changed

- ARK watcher: `ark-server agent watch` is the preferred watcher. The
  PowerShell and shell scripts are saved as files and run, no longer piped
  into a shell, which some antivirus programs flagged.

### Fixed

- Desktop app: the local server accepts personal agent tokens again.
  Tokens created in Settings > Agent tokens or by "Copy prompt", and the
  token of each session ARK starts when sign-in is enabled, were refused
  with "This ARK server belongs to the ARK Studio desktop app". Revoked,
  expired and unknown tokens are still refused, and read-only tokens stay
  read-only.
- Desktop app: connecting to a server no longer reports "not responding"
  for a server that is reachable, and when a connection does fail it says
  why. The check follows the current proxy settings.
- Sessions started by ARK are woken by ARK when there is news, so they no
  longer also run their own ARK watcher.
- The top bar stays in place when the page is pulled past its top with a
  trackpad, precision wheel or touch.

## [2.1.0] - 2026-11-20

### Desktop app

- One default page: ARK Studio starts the local server and opens the local
  Home. Saved servers open in the same window; local and remote projects are
  reached through the project switcher. Desktop links list and create
  projects on linked servers.
- Updates: Settings > About checks for updates (at most once a day at start,
  or on demand) and installers update themselves. When a newer version is
  found, the local Home shows "ARK Studio x.y.z is available" with Update
  and Later. The updater asks the latest release first and then a fixed
  `updater` release, which keeps a copy of the newest `latest.json`.
- Installing over an existing copy, updating and reinstalling keep projects,
  settings, page preferences (theme, language) and shortcuts; uninstalling
  keeps the data folder.
- Windows: the tray menu is a small ARK-styled window (version, local server
  state, keyboard navigation); the top bar's buttons keep clear of the window
  controls at every scale and width.
- External links open in the system browser; "Link this desktop" shows its
  progress and times out after 15 seconds instead of hanging.
- Closing the window keeps ARK Studio in the tray by default.

### Projects and nodes

- Recycle bin (Settings > Project > Recycle bin): restore, delete
  permanently or empty. Deleting a node is called Delete / Move to recycle
  bin everywhere.
- Multi-selection: group siblings under a new parent (Ctrl+Enter) and edit
  their shared attributes at once in the inspector.
- Map: Delete / Backspace remove the selected link; links have a context
  menu.
- Ideas: import from InfoHub or from another ARK project.
- Project picker: cards with personal order, pinning and hiding; creator and
  latest writer; a "Created by me" filter. Project invite links let admins
  bring new people in.
- Filters: only Type chips carry colour dots.
- Image previews: right-click to copy the image, copy its link or open it.

### Review

- Review requests: a review note with anchors to the exact places, review
  rounds with the changes since the last review, and review groups for a
  delivery of several nodes.
- Node details: Accept, Send back and Add comment on one row for nodes in
  review; the review note at the top.
- Needs you and Research > Review: accept several at once with a shared note
  and Undo.

### Home

- Faster on large projects; "Home shows" limits Home to the newest nodes per
  person and project.
- KPI tiles, a 14-day card, Connect your agent, a getting-started checklist,
  a first-run welcome and an interactive guide.
- After a page hang, reloading starts at Home.

### Agents and sessions

- Agent presence: the graphs show which agent works on which node;
  sub-agents are shown separately.
- Sessions sidebar: tabs, Send now, images, rename, start without a prompt,
  sub-agents and background tasks apart, long-lived Claude Code sessions;
  long sessions no longer freeze the page.
- Agent rules in Settings (catalog, composer, `GET /api/agent/v1/rules`),
  setup per person and per device, and core rules in the starter prompts.
- The watcher wakes idle agents (Claude Code and Codex) when people reply;
  ARK-launched sessions get `ARK_URL`, `ARK_PROJECT` and `ARK_TOKEN`.
- Agent API: list, delete and retype links.
- Run panel: lists the models from the configuration and detects how Claude
  Code is signed in.

### Resources

- The folder size of each resource location, per device.

### Android

- Both apps follow the system language (English or Chinese).

### Interface

- Top bar: one control style for search, sync, Assistant, layout, theme and
  language; Sync now reloads the project data. Pull to sync on phones.
- Workbench views can be dragged; the layout follows the account. View
  plugins.
- Settings opens on an overview and shows each setting's scope.
- Graph walkthroughs for Force, Mind and Node graph; node size follows the
  number of links; full screen for the Map.

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

- Map graphs: a selected link is deleted with Delete or Backspace (after a
  confirmation; Ctrl+Z restores it), and a link's right-click menu offers
  Delete link and Change relation.
- Agents can list, retype and remove links: `GET /api/agent/v1/units/{uid}/links`,
  `DELETE /api/agent/v1/links` with `{source, target, relation?}` (409 lists the
  relations when the pair has several), `DELETE /api/agent/v1/links/{id}` and
  `PATCH /api/agent/v1/links/{id}` `{relation}`.
- Agents watch ARK for replies: `GET /api/agent/v1/updates` returns what
  people did since a cursor (comments, replies, Send back, Accept, decisions,
  changes to the agent's units, assigned tasks, setup confirmations), with
  an optional long poll; `ark-server agent watch` prints them. The Home
  starter prompts (R6) and docs/agents/working-mode.md tell agents when to
  check.
- Experimental, off by default (Settings > Advanced): send a message to your
  agent from Home or a node; agents get it in the update feed and reply
  through the agent API (docs/development/agent-messaging.md).
- Browser project cache: pages, node details and tab switches render from
  IndexedDB at once and revalidate with ETags and snapshot deltas
  (`GET /api/changes`); project changes arrive over `GET /api/events`
  instead of 3.5 s polling. Cached data is per account and removed on
  sign-out, account switch and revoked grants; offline shows it read-only.
  Settings › Account can clear it. See `docs/development/client-cache.md`.
- Contributing guide, code of conduct, security policy, support file, issue
  and pull request templates, and `.editorconfig`.

### Licence

- ARK Studio is licensed under the GNU AGPL-3.0, with commercial licences on
  request.

## [2.0.0]

- Version declared in `pyproject.toml` before this changelog was started.
