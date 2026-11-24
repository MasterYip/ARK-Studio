# Changelog

Notable changes to ARK Studio are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [2.1.6] - 2026-11-24

### Added

- Sessions started by ARK are woken when their GPU jobs finish, fail, time
  out or are cancelled, and when their background tasks end, so an agent
  goes on with its work without anyone nudging it.
- Board and Home show the agents and hosts actually at work: your running
  sessions with their state, model and task, agents that wrote to the
  project in the last day, and the devices and GPU servers that are set up.
- Run an agent: "Start with" picks the session's first step from the prompt
  library (set up this device, continue a task, plan an idea, and your own
  prompts) or leaves it free. ARK suggests one: setup when the device is not
  set up yet, continue when a task is open. The message box hint follows
  the choice.
- Standby servers: a second server can run read-only beside the main one,
  kept in sync every few minutes. Its pages show a banner with the last sync
  time; hide it for the session and a Read-only chip stays in the top bar.
- Resources: each machine's SSH address is stored once and reused by the
  device dialog, the GPU tab and the Resources graph. The device dialog is
  filled in from what ARK already knows, and Open in VS Code, Cursor or a
  terminal works right after setup. The desktop app's Open in terminal
  opens a local terminal in the folder.
- Filters: a Min. links slider after Time shows only nodes with at least N
  links (incoming and outgoing, counted over the whole project graph; on a
  topic page, over the topic's links). It works on the Map, Ideas and topic
  pages, shows a "Links ≥ N" chip and is remembered per tab.
- The PredActor example project links its code repository
  (https://github.com/MasterYip/PredActor) in its description and in the
  story "PredActor — the paper as a graph". Example projects created
  before 2.1.6 keep their description.

### Changed

- Agents put work up for review only with a review note that says what to
  look at; a hand-off without one is refused with an example of what to
  send.
- Agents keep files outside the repository in this device's task space and
  ask you to choose one when there is none, instead of inventing folders.
  Paths written in ARK are absolute and name the device they are on.
- Sessions started by ARK use their own token, and the agents panel lists
  each agent once.
- Android app: phones with 2.1.2 to 2.1.5 update in the app.

### Fixed

- Open in VS Code no longer uses an SSH alias that was never written to
  `~/.ssh/config`; it uses the machine's own alias or writes it first.
- Links typed in Chinese text end where the Chinese text or full-width
  punctuation begins.
- Filters: clicking a tag or removing an active chip no longer scrolls the
  filter panel back to the top, and tag groups you opened stay open.


## [2.1.5] - 2026-11-23

### Added

- Settings → Mobile on the desktop app (or any local `ark-server`) can let
  phones connect through a free Cloudflare quick tunnel, or your own named
  tunnel, without a public address and without an ARK relay. A new phone is
  confirmed on the computer with a 6-digit code. When the quick tunnel's
  address changes, phones re-link with a QR code or a short code and keep
  their access. The address may be slow or unreachable on some networks.
- GPU tab and Resources: Open in VS Code and Open SSH terminal sign in with
  your key or password. ARK uses a matching Host entry in `~/.ssh/config`,
  or offers to add one (key login or password login; ARK never stores the
  password). The SSH button works on Windows too.

### Changed

- Android app: phones with 2.1.2, 2.1.3 or 2.1.4 update in the app.
- Run an agent in ARK: the dialog shows platform, model, reasoning effort,
  access (read only or full access), task and an optional first message;
  title, worktree, working subfolder and agent setup are under Advanced.
  Reasoning effort works for Claude Code and Codex, with the provider's
  default named ("Default (High)"); the model list names the configured
  default. The task is filled in from the open node, the title from the task,
  and the last platform, model, effort, access and worktree are remembered
  per person and project, also for "Run with agent". "Agent setup" lists
  Claude Code agents and Codex profiles instead of a free-text field.
- Codex sessions read Codex's event stream: messages, commands with their
  output, file changes and plans show as in Claude Code sessions, without
  Codex's log lines, escape codes or repeated answers.
- Phones in Web mode, the default, list and switch between all projects
  your account can open. Settings > Mobile on a phone shows the app
  downloads instead of the pairing settings.

### Fixed

- Import, split and merge plans that include decision nodes can be applied
  again; before, the plan's own approval decision took one of the new IDs
  and the plan was always refused as out of date. New IDs are assigned when
  a plan is applied, so work in either project while a plan waits no longer
  makes it out of date. A plan goes out of date only when the nodes it
  covers, their links, attachments or comments change; the plan view then
  lists what changed and offers "Refresh plan", which keeps the approval
  request.
- Links to a split, merge or import plan in a decision node open the plan
  view instead of an error page.
- Agents on the same address are no longer locked out because another agent
  keeps using a revoked token: a valid token is never rate-limited.
- Codex sessions on Windows are woken by the ARK watcher again. When a
  wake-up cannot be given, the watcher says why. Sessions started by ARK
  are woken for reviews whatever the old "Nudge idle sessions" setting
  was; the setting is gone.

## [2.1.4] - 2026-11-23

### Changed

- Pairing a phone signs it in as you, not into one project: a paired phone
  (QR code or password) switches between all projects you can open, in Web
  mode and in Native mode, with your role in each. Settings → Mobile lists
  your paired devices on every project with their current project and last
  use, and sets each to full access or read-only, or revokes it. Phones paired
  earlier switch projects too.
- Android app: in Web mode the status and navigation bars follow the site's
  light or dark theme. Back twice at the site's root returns to the server
  list without signing out. The server list shows each server's address and
  signed-in account with Open and Refresh, and holds the app settings (open
  with Web or Native, language, notifications, updates and version).
  Phones with 2.1.2 or 2.1.3 update in the app.
- Assistant: it works on the open project by default and can also read
  your other projects, with your access in each. It changes another project
  only when you ask and your access there is write or admin. A node from
  another project shows the project's name and opens it when clicked.

## [2.1.3] - 2026-11-23

### Added

- Android app: one app with two modes. Web mode, the default, opens the
  full ARK workspace full screen; Native mode opens the Android screens and
  reaches the rest through the Web button. Switch modes in the app's
  settings. Phones with 2.1.2 update in the app; phones with 2.1.1 or
  earlier uninstall once and install 2.1.3.
- Needs you: when someone assigns nodes to you, they appear under
  "Assigned to me", the project selector shows them, and the Android app
  sends a notification. Several nodes assigned at once arrive as one notice.

### Changed

- Personal agent tokens have no count limit any more: create as many as you
  need in Settings > Personal > Agent access, and Copy prompt no longer stops
  at 10 active tokens. The token list shows how many are active.
- Agents lay out a planned idea as a small tree of nodes: the question, the
  plan task, one to three claims and the tasks that test each claim. The
  starter prompts' "Plan this idea" option and core rule G1 describe it, and
  the agent checks the tree before it hands the plan over.

### Fixed

- Sessions started by ARK get "ARK UPDATE" messages again after you accept,
  send back or comment on a review, also while their background tasks or
  sub-agents run, and no update is lost when the server restarts.

## [2.1.2] - 2026-11-22

### Added

- Split and merge projects: move weakly linked branches into a new project,
  or merge a whole project into another. ARK suggests what to split, an
  agent proposes the plan as a decision in Needs you, and nothing moves until
  a person approves it. Moves can be undone for 30 days; node details show
  where a node came from and links to other projects.
- Agents can copy nodes from another project into this one with an import
  plan that a person approves first (`/api/agent/v1/import-ark`); the agent
  guide "Project content" explains inspecting, planning, applying and
  reporting.
- Home prompt library: More prompts opens built-in prompts (organize
  project content, sort the review backlog, continue a task, plan an idea,
  status check, resume interrupted work, set up this device) in English and
  Chinese. Pin the ones you use to Home, reorder them, and write your own
  personal or project prompts with `{project}`, `{unit}` and `{user}`;
  export and import them as JSON. Agents can save a prompt for you.
- Node markers: choosing a marker a node already has removes it; stars,
  flags, priority and progress allow one marker each per node. Settings >
  Appearance > Node markers edits the project's marker groups; the graph
  menu has Remove marker and Clear markers.
- Android app: About and updates with a daily check. The app downloads the
  update from your ARK server when the server has the APK, else from GitHub,
  and checks the download's SHA-256 and signing key before installing. From
  2.1.2 on, releases are signed with one fixed key, so each one installs over
  the last. An APK from 2.1.1 or earlier has a different key: uninstall it
  once, then install 2.1.2.
- Android app: system notifications for new reviews (a review already opened
  in the browser does not notify), decisions, approval requests and session
  errors across the account's projects, grouped per project, with quiet hours
  and per-project mute (no push service: checked every 15 minutes and when the
  app opens).
- Runs on phones: full-screen conversation with one compact header, tabs, a
  composer that stays above the keyboard, the session list and session
  properties as bottom sheets, long press for menus and larger touch targets.
- Agent sessions keep running when the Linux server restarts or is updated;
  the server reconnects to them when it starts again.
- Runs: the page is the conversation, with tabs for sub-agents and forks.
  Fork in new tab continues a session in a separate conversation (Claude
  Code and Codex fork natively).
- Locate in map in every node menu (Alt+M): opens the Map, shows the node
  even when filters hide it, centers it and opens it in Details. Filter chips
  have Only this, Select all and Invert (right click or long press;
  Alt+click is Only this).
- Resources: open a location in VS Code or Cursor (local folders and Remote
  SSH), open an SSH terminal (desktop app), or copy the SSH command or path.
- Desktop app: downloads such as "Download project ZIP" are saved to
  Downloads and shown in the app window, with Show in folder.
- Sessions: a session whose sub-agents or background tasks still run shows
  Running with the reason ("Running · 2 sub-agents").
- One delivery, one review (project preference, on by default): when an
  agent puts several nodes up for review at once, they arrive as one review
  group with a note that says what to look at.
- Desktop app: Settings > App inside ARK lists the app's own settings
  (version and updates, saved servers, start-up, window, data) next to the
  server's settings; the Settings header says whose settings you are looking
  at. The app's settings window links back to the server's Settings.
- Desktop app: when a newer version is found, a system notification (once
  per version) and a notice with Update / Later on every page in the app
  window, local or remote. The app checks at start and every 12 hours while
  it runs (can be turned off).
- Desktop app: after an update, the local Home and Settings > About show
  "ARK Studio was updated to ..." with the main changes of the release.
- Servers: after an upgrade, each person sees "This server was updated to
  ..." once on Home.
- Switching projects keeps each project's page: back in a project you get
  the page you left with its view, the node open in Details and its scroll
  position, the graph's zoom, position and layout, the selected run and its
  list filters, and the session open in the Sessions view. A project you
  open for the first time starts at Home. After the page hung, ARK still
  starts at Home and forgets that project's page.
- Project picker: each project shows how many items in Needs you are new
  for you since you last looked ("3 new") and how many there are in all;
  the project selector shows a dot when another project has new ones.
  Opening Needs you or an item marks it seen, on every device.

### Changed

- Servers: `ARK_DOWNLOAD_DIR` names a folder with the Android app's APK;
  phones then download the app and its updates from the server instead of
  GitHub, which some networks cannot reach.
- Home: Copy prompt reuses one personal token per project instead of creating
  a new one on every copy, so copies no longer run into the 10-token limit.
  The dialog has a New token button (revokes the previous one) and, at the
  limit, offers to revoke the project's older prompt tokens. Settings >
  Personal > Agent access can revoke old starter-prompt tokens at once.

- Settings > About is a top-level page next to the overview (it was under
  Personal; old links still open it). It shows the version, and in the
  desktop app the install type, the last update check, "Check for updates",
  "Update and restart" (or "Download" for the portable build) and the
  start-up check switch. On a server it shows the server's version.
- The Settings overview starts with an "About ARK Studio" card: version and
  update state at a glance.
- The activity bar has an About button above Settings; a dot marks a newer
  version of the desktop app.

### Fixed

- Settings no longer repaints every few seconds on a public server, and a
  change made elsewhere refreshes Settings only when the data it shows
  changed, never over unsaved edits. The status bar says "Syncing" only
  for requests that take longer than a moment.
- Search palette: clicking a result opens it again (hovering no longer
  replaced the row under the pointer).
- Settings: unticking "enabled" for a project coding agent stays unticked
  after Save.
- Needs you no longer lists closed decisions (done, abandoned or archived).
- Phones: the session list and session details sheets on Runs respond to
  taps again.
- Node details opens in Read at every start; only the Read/Edit buttons
  change what it remembers.
- Queued messages leave the queued-turns list once delivered; a failed one
  can be retried or removed.
- Graph checks find tasks without a parent and can attach them.
- Android app updates work on servers that cannot reach GitHub's download
  host: the checksum comes from the release itself.

### Removed

- The WebView Android app (ARK Studio Web, `com.masteryip.arkstudio.web`) is
  no longer built or attached to releases; the Android app is ARK Studio
  (`com.masteryip.arkstudio`). `/download/ARK-Studio-webapp.apk` now downloads
  ARK Studio.

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
