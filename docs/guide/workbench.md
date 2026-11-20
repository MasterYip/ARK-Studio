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

## Views

| View | Default place | What it shows |
|---|---|---|
| Filters | left | The filter rail of Research and Map |
| Explorer | left | Project files (Editor) |
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
