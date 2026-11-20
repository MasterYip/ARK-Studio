# View plugins

Every view of the desktop workbench (Filters, Explorer, Search, Changes,
Details, Local graph, Assistant, Terminal) is a **view plugin**. The built-in
views ship with ARK; you can add your own views as plugins and place them
like any other view: drag them between the activity bar and the panels, move
them from the view menu, save them in layout presets
([Workbench](workbench.md)).

Settings → Personal → **Plugins** lists the built-in views and your own
plugins.

## Turning user plugins on

User plugins are third-party code, so they are off until you turn them on:
tick **Run user plugins in this browser (experimental)** and confirm. The
switch is per browser. While it is off nothing is imported or run, and
installed plugins stay stored but inactive.

## Importing a plugin

- **Import file…** takes one of:
  - a single **.html** file whose manifest is a
    `<script type="application/ark-plugin+json">` block;
  - a single **.js** file that starts with an `/*! ark-plugin { … } */`
    comment holding the manifest;
  - a **.zip** with `manifest.json` and the entry file; scripts, styles and
    images the entry references by relative path are bundled in on install.
- **Import from URL…** fetches a bundle from this ARK server (for example a
  project file link). ARK cannot fetch from other sites; download the file
  and import it instead.
- **Add the example plugin** installs *Node word count*
  (`ark_studio/web/plugins-examples/node-word-count.html`), which counts the
  words, characters and reading time of the selected node.
- **Add the assistant example** installs *Assistant example*
  (`ark_studio/web/plugins-examples/assistant-plugin.html`): it reads the
  selected node, asks the AI model you set up for the ARK Assistant to
  summarise it into a claim linked to the node, shows the proposed changes
  and, after you confirm, adds them.

Before anything is stored, ARK shows the plugin's name, version, where its
view goes and **the permissions it asks for**. Install grants exactly those
permissions. Importing a plugin with the id of an installed one updates it
(the dialog names the version it replaces).

## Permissions

| Permission | What the plugin may do | Asks you |
| --- | --- | --- |
| `selection:read` | read the selected node: title, text, type, status, importance, tags, its media (the plugin gets a copy of the file to show, never a link with your session) and its links in and out | no |
| `graph:read` | read the project graph: every node's title, type, status, importance and tags, the links between nodes, any node in full, search results (at most 50) and topics with their members | no |
| `events` | be told when the selection changes, when the graph changes (changes from you, other people or agents) and when you switch projects | no |
| `project:read` | know the project's id and name, your user name and role, the theme (light or dark and ARK's colours) and the language | no |
| `storage` | keep its own settings or notes in this browser, separately per project, about 1 MB at most; it cannot read anything else | no |
| `node:open` | show a node in Details, select it on the map or open it in the Editor | no |
| `notify` | show a short notification, prefixed with the plugin's name | no |
| `clipboard:write` | put text on your clipboard (a few times a minute at most); it cannot read the clipboard | no |
| `comment:write` | add a comment to a node; the comment says which plugin posted it | **every comment** |
| `node:write` | create and change nodes (title, text, type, status, importance, tags) and add or remove links, with your account | **before writing**: you see a summary and choose **Allow once** or **Allow for this session** |
| `link:open` | open a web page (http or https) in a new tab | **every link**, showing the address |
| `ai:chat` | send messages to the AI model set up for the ARK Assistant (Settings › Assistant); the key stays on the server; calls are limited and listed in Activity; changes the model suggests still go through `node:write` | no |

A plugin can do nothing else: it cannot delete nodes or projects, change
members or settings, and it never sees your session cookie, agent tokens,
API keys or any other ARK data.

### Confirming writes

When a plugin with `node:write` wants to change the project, ARK shows what
it will do (for example "New nodes: 3 · Changed nodes: 1 · New links: 2",
then each change, such as "Change T-0012: status → done"):

- **Allow once** applies this batch; the next one asks again.
- **Allow for this session** applies it and lets the plugin write without
  asking until you reload the page or switch projects. While this is on, a
  bar above the plugin's view says so; **Revoke** ends it at once.
- **Cancel** writes nothing.

Changes are made with your own account and permissions, and Activity and
node history show them as made by the plugin (*You · plugin
assistant-example*).

## Managing plugins

Each installed plugin has an **Enabled** switch (a disabled plugin keeps its
place in your layout and comes back there when enabled again) and
**Remove** (which also deletes what the plugin stored with `storage`).
Plugins and their data are stored in this browser for your account only;
they are not synced to other devices or shared with other users. The places
of plugin views in your layout do follow your account.

## Security model

- A user plugin never runs in ARK's page. Its view is an
  `<iframe sandbox="allow-scripts">` without `allow-same-origin`, so the
  plugin's document has an opaque origin: no access to ARK's DOM, cookies,
  local storage, IndexedDB, session or tokens. It cannot open pop-ups,
  submit forms, show dialogs or navigate the ARK tab.
- The frame is `/plugin-frame.html`, served with its own strict
  Content-Security-Policy: `sandbox allow-scripts`, no network at all
  (`connect-src 'none'`, images/fonts only from `data:`/`blob:`), no forms,
  no nested frames or workers, no `eval`. ARK's own page only allows frames
  from ARK itself, so the plugin frame cannot be navigated to another site.
- The plugin talks to ARK only through `postMessage`, using a small
  allow-list of calls. Every call is checked against the permissions you
  granted, its parameters are validated and size-limited, and calls are
  rate-limited (stricter for AI, writes, the clipboard and links). Messages
  that do not come from the plugin's own frame or do not carry the frame's
  per-load channel id are dropped.
- Confirmation dialogs (comments, writes, links) name the plugin, and
  their buttons wait a moment before they can be clicked, so a plugin
  cannot get a click meant for something else.

See [Plugin API](../development/plugin-api.md) to write a plugin.
