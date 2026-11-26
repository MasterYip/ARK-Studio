# Workbench: panels, Search, Local graph and layouts

On desktop (wider than 980 px) every workspace sits inside a VS Code style
workbench: an **activity bar** on the far left, a **left sidebar**, a **right
sidebar**, a **bottom panel** and a **status bar**. Sidebars and the panel are
positions; the views in them can be moved anywhere (drag a view's icon or tab
onto another container, or use its **…** menu → *Move to …*). Settings and
your account avatar are pinned to the bottom of the activity bar; the account
menu opens beside it. Phones and narrow windows keep the classic layout, with
Settings and the avatar at the right end of the top bar (phones: Settings in
the bottom tab bar).

| Shortcut | Action |
|---|---|
| Ctrl+B / Ctrl+Alt+B / Ctrl+J | Show/hide the left sidebar / right sidebar / bottom panel |
| Ctrl+Shift+F | Search (prefilled with the selected text) |
| F4 / Shift+F4 | Next / previous search result |
| Ctrl+K Ctrl+0 / Ctrl+K Ctrl+J | Editor: fold all / unfold all |
| Ctrl+K Ctrl+1 … Ctrl+K Ctrl+7 | Editor: fold to level 1 … 7 headings |
| Ctrl+Shift+[ / Ctrl+Shift+] | Editor: fold / unfold the current section |
| Ctrl+E | Editor: edit / view the active file |
| F11 | Editor: full screen (Esc leaves it) |

The folding keys work while the editor, the Markdown preview or the Outline
has focus, and can be changed in Settings › Appearance & input › Shortcuts.
Ctrl+K starts the two-key shortcuts there; the command palette is Ctrl+P and
lists the folding commands too.

## Views

| View | Default place | What it shows |
|---|---|---|
| Filters | left | The filter rail of Research and Map |
| Explorer | left | The Editor's Files, Outline, Timeline and Search sections |
| Search | left | Full-text search over nodes and project files |
| Changes | left | Git changes; diffs open as Editor tabs |
| Details | right | The node inspector |
| Local graph | right | The selected node's neighbourhood |
| Assistant | bottom | The Assistant chat |
| Terminal | bottom | The project terminal |

A view that does not apply to the current workspace is hidden from its
container (for example Filters in the Editor); an empty container collapses.
Views are plugins: you can add your own (sandboxed) views, see
[View plugins](plugins.md).

### Moving and reordering views

Activity bar icons and panel tabs are the same thing, a view, and drag the
same way:

- **Reorder** the activity bar by dragging an icon up or down; reorder a
  tab strip by dragging a tab left or right.
- **Move** an activity bar icon onto the tab strip of the right sidebar or
  the bottom panel: it becomes a tab there. Drag a tab onto the activity bar
  and it becomes an icon at the place you drop it.
- While dragging, the target bar is tinted and a line shows the exact slot.
  A place where the drop would change nothing shows no line and does not
  take the drop; Escape cancels.
- An open view stays open after a move; moving a view into another bar
  also shows it there.
- **Keyboard**: right-click a view (or press the context-menu key or
  Shift+F10 on a focused icon or tab) for *Move up / Move down* (*Move left
  / Move right* in tab strips) and *Move to Left sidebar / Bottom panel /
  Right sidebar*. On a focused icon or tab, Alt+Up/Down (Alt+Left/Right)
  moves it one place.
- **Touch** (tablets with the desktop layout): press and hold an icon or a
  tab until it lifts, then drag. Phones and narrow windows use the classic
  layout, which has no activity bar to rearrange.
- **Reset view layout** (in a view's menu, the Layout menu and the command
  palette) returns to the default layout: your default preset, or the
  built-in Default.

### Sections

A sidebar can show several views at once, stacked top to bottom (for
example Details over the Local graph, with the Assistant below). The bottom
panel places its sections side by side. Each section has a header: click it
to collapse or expand the section, use its buttons for the view's actions
and *Maximize section* (click again to restore). Drag the line between two
sections to resize them; double-click it for equal sizes. A section can also
hold several views as tabs.

- **Make a section**: drag a view (its tab, its section header or its
  activity bar icon) onto a sidebar. A line marks *above* / *below* a
  section (left / right in the bottom panel); the middle of a section adds
  the view to it as a tab. Escape cancels the drag.
- **Reorder or move a section**: drag its header to another place, in the
  same or another container.
- The view's **…** menu also has *Split above / below in …*.

Sections, their sizes and collapsed states are part of the layout and of
saved layouts. Layouts saved before sections existed open with one section
per container. A section whose views all do not apply to the current
workspace disappears.

## Search

Type in the box to search **Nodes** (titles, bodies, tags and comments) and
**Files** (project file contents) at once. The toggles next to the box are
*match case* (`Aa`), *whole word* (`ab`) and *regular expression* (`.*`);
**…** shows *files to include / exclude* (comma-separated globs such as
`*.md, docs/**`; they apply to files only). Results are grouped with counts;
click a node result to open it in Details, a file result to open the file in
the Editor at that line. Searches are limited in time and size (binary files
and files over 1 MiB are skipped); a notice says when results were cut.
Results appear while the search runs, and typing again cancels the running
search. Files ignored by git (`.gitignore`) are not searched. Route details:
[design note §17](../development/design.md#17-workbench-search-routes).

## Editor: Explorer, Outline and Timeline

In the Editor the Explorer holds four stacked sections: **Files**,
**Outline**, **Timeline** and **Search** (the Search view with Replace; in
other workspaces Search is its own view). Click a header to fold or unfold
its section, drag the line above a header to resize, and use the header's
last button (or double-click the header) to maximize a section and restore
the others. Ctrl+Shift+F in the Editor opens the Search section. The
arrangement is kept in this browser.

- **Reveal**: the Explorer follows the active tab: it opens the file's
  folders, highlights the file and scrolls to it. Turn this off in the
  Explorer's **…** menu (*Reveal the active file automatically*); *Reveal in
  Explorer* is also in a tab's right-click menu, the Explorer's header and
  the palette. Clicking a folder in the path above the editor lists that
  folder and opens it in the Explorer.
- **Outline**: a tree of the file's headings or symbols. Click an entry to go
  there; the entry for the section you are reading or typing in is
  highlighted. Folding a section in the Outline folds it in the Markdown
  preview and the other way round (per file, for this session).
- **Folding in the preview**: hover a heading and click the arrow left of it
  to fold its section. *Fold to level n* keeps headings down to level n
  visible and folds their sections. The source view is a plain text editor
  and does not hide lines; the folding keys then fold the Outline (the
  preview shows the same folds).
- **Timeline**: the commits that changed the file (renames followed), with
  author and age. Click a commit to open that version read-only, or use
  *Compare with current file* (also on the version's toolbar) for a diff.
  Projects that are not git repositories show no history.

## Editor: view first, HTML pages and full screen

- **View first**: files open for reading. Markdown and HTML show rendered,
  images, PDF and media in their viewers, other text read-only with syntax
  colours. **Edit** in the toolbar, Ctrl+E or Enter in the read-only text
  opens the editor for that tab; **View** goes back (after saving). People
  with read access only get the view. Turn it off in Settings › Appearance &
  input › Editor (*Open files in view mode*).
- **HTML pages** run in the preview with their scripts, and relative files
  (css, js, images, data next to the page) load. The page runs in a sandbox
  with an opaque origin: it cannot read your ARK session, cookies, storage
  or the ARK page, and its own requests may only reach the page's folder
  (scripts, styles and images may also come from https libraries). The
  toolbar has reload, open in a new tab (sandboxed the same way), page full
  screen and Rendered / Source. The link is valid for 30 minutes and is
  renewed while the tab is open. HTML attachments of nodes run their own
  text only, after *Run*.
- **Full screen**: the button at the right of the toolbar (or F11) lets the
  Editor cover the whole window, without the top bar, activity bar and
  sidebars; Esc or the button ends it.

## Search: Replace

The arrow left of the search box opens **Replace** (people with write access
only). *Replace all* (Ctrl+Enter in the replace box), or the button on a
file's result row, first lists every file with the changed lines, and writes
nothing until you confirm. Replace works on project files only; nodes are not
changed. With *regular expression* on, `$1`…`$99` and `$&` insert groups and
`$$` a dollar sign. A file changed on disk after the preview is skipped and
reported; open editor tabs reload replaced files.

## Editor: Source and Preview side by side

When the same node or Markdown file is open in two editor groups, one in
Source and the other in Preview, their scrolling stays in step: scrolling
either side brings the other to the same place, and the line with the
cursor in the source marks its block in the preview. A node tab's Split mode
syncs its two panes the same way. Each group keeps its own Source / Split /
Preview choice, and switching between them keeps your place. Turn it off
with **Sync scroll** in the tab's right-click menu or the open-files list.
Search results open the same way: a double-click (or Ctrl+Enter) opens a
node or file as a pinned tab at the match.

## Local graph

Shows the node open in Details (Map, Research, Topics) or the node tab open
in the Editor, with its neighbours. The toolbar is the main graph's: the
layout picker, zoom, *fit* and help (folded into **…** when the sidebar is
narrow).

- **Force** spreads the neighbourhood like the main graph.
- **Mind** puts the focused node at the root and its neighbours on levels
  by distance (a node hangs under the node it was reached from; other links
  are drawn as cross-links). `+`/`-` and `*`/`/` collapse and expand
  branches as in the main Mind view.

The filter button opens the neighbourhood options: depth (1–3 levels), link
direction, and the relation and node types to show. Click a node to select
it, double-click to open it in the Editor; scroll to zoom, drag to pan. The
layout and options are remembered. Arrows, colours and personal overrides
follow the graph settings. Very large neighbourhoods are capped at 300 nodes.

## Assistant history

The Assistant keeps one conversation per person and project: you see only
the messages you sent and the replies to them, never another member's, and
the Assistant's own prompt history is built from your conversation only.
*Clear history* (Assistant view **…** menu, or the button on the full
Assistant page) deletes your history in this project after a confirmation;
other members keep theirs and no project node is touched.

Messages written before ARK kept per-person history (before schema v10) have
no author. On a server with accounts they belong to the project owner: the
first time the server opens the project it gives them to the account that
created the project (or, if that account is gone or was the local user, to
the organization's owner) and records this in the project database
(`migration.chat_legacy_owner`), so it happens once. In local single-user
mode they simply stay part of your conversation.

A project ZIP ([Project export](project-export.md)) downloaded by a project
admin in the browser contains every member's conversation; any other download
(members, viewers, personal agent tokens, task agents, paired phones) keeps
only the requester's own.

## Layouts and presets

The arrangement (where each view sits, which views and containers are open,
sizes, the maximized panel) is saved with a signed-in user's account
(profile preference `prefs.workbench.layout`, next to the saved presets) and
follows them to other devices; this browser keeps a copy, which is all that
is used for read-only accounts. The account copy carries its save time
(`layout_at`): a browser takes it only when it is newer than its own, so an
older cached profile never undoes a later change. Editor groups and open tabs are remembered
per project. The **Layout** button in the top bar
(next to the panel toggles) and the command palette (`Layout: …`) apply a
preset:

- **Default**, **Focus (Zen)** (hides the sidebars and the panel),
  **Research** (Filters left; Details over Local graph right),
  **Writing** (Explorer over Search left; Local graph over Details right),
  **Review** (Changes over Explorer left; Terminal beside the Assistant below).
- **Save current layout as…** keeps your own layouts; each can be applied,
  renamed, deleted or set as default. Signed-in users' layouts are saved with
  their account and follow them to other devices.
- **Reset view layout** returns to the default preset.
