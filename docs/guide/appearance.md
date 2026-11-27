# Appearance and Text

**Settings > Personal > Appearance & input > Appearance & text** contains the theme preference
(light, dark or system), high contrast and text size from 100% to 150%.
Defaults preserve the existing text sizes. Text size changes typography;
it does not zoom graph coordinates or distort the SVG brand lockups.
Signed-in accounts save these preferences with their profile. Browser storage
provides the immediate local fallback, including the login and loading screens.

**Settings > Project > Appearance > Project palette** stores independent light and dark
palettes. The top-bar theme button switches between the corresponding palettes.
Each mode has presets and editable color swatches. Save applies both palettes
to the project; disabling the project palette restores ARK's native themes.
Enter a theme name and use its save button to store both modes together in
the project. Saved themes can be selected, edited, overwritten or deleted.
Selecting one previews it; Save applies it. Up to 24 named themes are stored
with the project, including when it is copied to another server.

Project colors derive all shared UI tokens, including secondary text, inputs,
menus, chips, buttons, scrollbars and graph colors. Native form controls use
the matching browser color scheme. High contrast adjusts primary and secondary
text toward a 10:1 contrast ratio on the background and surface, and boundaries
toward 3:1. Arbitrary custom colors can limit achievable contrast if their
background and surface have very different luminance.

Older single-palette projects migrate on read: the existing colors fill the
mode matching their background; the other mode starts with ARK's defaults.
The legacy flat palette fields remain available to existing agent clients.
New clients can independently patch `light` and `dark` on
`POST /api/agent/v1/project/palette`:

```json
{
  "enabled": true,
  "light": { "preset": "forest-light" },
  "dark": { "preset": "indigo" }
}
```

`palette.saved` maps theme names (1 to 64 characters) to complete `light` and
`dark` palettes. A patch merges named entries; a `null` entry deletes that
name. Changes to active colors leave saved themes intact.

## Cluster by type

The graph physics panel has an optional **Cluster by type** switch (off by
default). When it is on, nodes of the same type pull together and nodes of
different types push apart, so each type forms its own group in the Force
graph. **Same-type pull** and **Cross-type push** (0 to 1) set the strength;
**Group by** chooses type (default), status or a node's first tag.

Stored with the physics as `cluster` (true/false), `cluster_attract`,
`cluster_repel` (0 to 1, default 0.5) and `cluster_by` (`type`, `status` or
`tag`). Projects that never set them lay out exactly as before.

## Timeline graph

The **Timeline graph** layout (graph toolbar, next to Node graph) shows the
node cards and links of the node graph, ordered by time from left to right,
with one horizontal lane per research line, so parallel lines and the
places where they meet are visible.

- **Order**: a node's time is its start date, else when it was created,
  else when it was last updated. Columns are ranks, not a time scale: the
  next node in time takes the same column when it is on another lane, and
  nodes of one lane from the same day (or within two hours) stack in one
  column, up to four. *depends on* and *derived from* links, in their shown
  direction, keep a node right of its inputs even when the dates say
  otherwise. *part of* only orders nodes whose dates agree; when the whole
  came first, the link runs from the whole to its parts (arrowheads keep
  their meaning). Dashed rules with a date mark where the latest day reached
  so far changes.
- **Lanes**: one per line tag (`branch:<line>`, or the project's line
  namespace); sub-lines (`a/b`) follow their parent line. A node tagged with
  several lines sits in the biggest one, and the links from the other lines
  run across lanes into it. Untagged nodes join the line of their nearest
  *part of* parent, else the **Not in a line** lane at the bottom. When most
  nodes have no line, they get one lane per *part of* tree instead.
- **Links**: the timeline first shows how work led to the next piece:
  *derived from*, *depends on*, *part of*, *produces*, *validates*,
  *inspires*, *supports* and *supersedes*. The edge filter shows the others.
- **Fit** shows the whole timeline when it fits; a longer one opens at its
  start, with every lane in view.
- The layout is always automatic: cards can be moved for a closer look, but
  positions are not saved and the next refresh lays them out again. The node
  graph keeps its own arrangement. Card widths set in the node graph apply.

## 3D graph

The **3D** button in a graph's toolbar (Map, a topic's Graph view, Ideas)
is shown with the Force layout and
shows the same nodes and links in a 3D force layout, drawn in perspective:
nearer nodes are larger, farther ones smaller and fade into the background.
Nodes are flat discs that always face you, with the same colours and status
icons as in 2D; labels show for the nearest nodes and for the hovered and
selected ones.

- Left click selects a node (the inspector follows, as in 2D); dragging a
  node moves it in the plane facing you while the layout reacts, and on
  release it rejoins the simulation (nothing is pinned), as in 2D. A left
  drag on empty space does nothing.
- Right drag rotates, Ctrl+right drag (or middle drag) pans, the wheel
  zooms, double-click flies to a node. A right click on a node (without
  dragging) opens the same node menu as in 2D. On a phone: one finger
  rotates, two fingers zoom and pan, a tap selects. The graph's **?** help
  lists these while 3D is on.
- Hovering highlights a node's neighbours.
- Filters (including Min links), the visible-relations choice, search and
  selection are shared with the 2D graph. The forces are the graph physics settings above
  (including Cluster by type), plus a collision force so nodes do not overlap.
- The toolbar adds Reset view, Auto-rotate and 3D settings: label distance
  (0 shows only hovered and selected labels), node size and Low power
  (1x pixel ratio, no antialiasing). Labels never overlap on screen: where
  two would, the nearer node keeps its label.

Picking another layout leaves 3D; picking Force again returns to 3D when
it was the last choice. The 2D/3D choice is remembered per account and page; the 3D settings per
account. three.js (MIT) loads the first time 3D opens. Without WebGL the
graph stays in 2D and says so. 3D is for viewing: adding nodes, links and
editing stay in 2D.

## Arrowheads (graph line ends)

**Settings > Project > Appearance > Graphs > General > Arrowheads** sets
how graph connections show direction, like line ends in Visio. The live preview
beside it redraws on every change; Save, Discard and Reset apply to physics and
arrowheads together (see *Graphs* below for the other tabs).

- **Arrowhead**: none, filled triangle (default), open arrow, stealth, diamond
  (hollow), filled diamond, dot or bar.
- **Placement**: target end (default), source end, both ends or no arrow. The
  target is the node a relation points to: for *A part of B* it is B.
- **Size**: Small (75%), Medium (100%) or Large (150%), or any value from 50%
  to 250% on the slider. The size still follows the line width and zoom within
  a legible band, so heads stay readable zoomed out and do not grow huge when
  zoomed in.
- **Mind-map branches**: also decorate Mind-map tree branches (off by default;
  the tree layout already shows the hierarchy). Cross-links always use the
  arrow settings.
- **Per relation**: override any of the three values for one relation, for
  example a diamond for *depends on* or no arrow for *part of*. *Default* keeps
  the value above. *relates to* and *unset* have no direction and start with
  no arrow.

The edge filter in each graph shows the resulting line end next to each
relation. Arrowheads use the edge colour and its hover, selection and dimmed
states in every palette. As before, quiet arrowheads hide below 30% zoom (like
labels), and very dense views (more than 2,500 heads of one kind on screen,
counting both ends) skip them; the hovered or selected connection keeps its
heads.

Project settings store them in `settings.arrows` (`PATCH /api/project/settings`):

```json
{
  "arrows": {
    "style": { "head": "triangle", "placement": "target", "size": 1 },
    "relations": { "depends_on": { "head": "diamond" }, "part_of": { "placement": "none" } },
    "mind_tree": false
  }
}
```

`head` is one of `none`, `triangle`, `open`, `stealth`, `diamond`,
`diamond_filled`, `circle`, `bar`; `placement` one of `target`, `source`,
`both`, `none`; `size` 0.5 to 2.5. A `style` patch merges; `relations` replaces
the whole per-relation table (omit it to keep the table).

### Reverse direction (display only)

Some relations read backwards to the flow of work: *A derived from B* and
*A depends on B* both flow from B to A. A relation can be **reversed for
display**: graphs draw and lay it out from target to source. Arrowheads flip;
node-graph layers and port sides follow the shown direction (derived work sits
right of its source material, also in the timeline graph); Mind uses it for parent/child;
the local graph's *outgoing*/*incoming* filter follows it. Stored links, the
inspector's relation labels, APIs and exports keep the stored direction.

- **Defaults**: `derived_from` and `depends_on` are shown reversed. Every other
  relation reads naturally as stored (part of, supports, cites, targets,
  produces, uses, ...); `supersedes` is left as stored because "newer →
  older" is the usual reading.
- **Project / personal**: the Reverse column of the per-relation arrow table
  sets `relations.<relation>.reverse` (`true`/`false`; *Default* removes it).
  A personal *My arrowheads* override carries its own values.
- **Quick toggle**: the ← button next to each relation in a graph's
  *Visible connections* filter flips it for that graph in this browser (it
  wins over the settings; turning it back to the setting clears it). In the
  node graph, *Auto layout with these directions* re-lays out the cards.

```json
{ "arrows": { "relations": { "derived_from": { "reverse": false }, "cites": { "reverse": true } } } }
```

`reverse` is only valid inside `relations` (not in `style`) and must be a
boolean; other values are rejected on save and ignored when read.

## Graphs: settings by graph type

**Settings > Project > Appearance > Graphs** groups every graph setting by the
graph it affects, with one small tab per graph type (click or use Left/Right,
Home/End). Each setting has a one-line explanation; the live preview stays
beside the tabs.

| Tab | Settings |
| --- | --- |
| General (every graph) | arrowheads and per-relation line ends, node fade in / fade out |
| Force & node | graph physics (repulsion, distance, gravity, damping); circular node markers: size, distance, spacing angle |
| Mind | node alignment, auto layout, Mind marker size, show image attachments (also node-graph cards), pinned image size |
| Local graph | default depth (1 to 3), history rings |

Topics and the Resources graph have no settings of their own: Topic graphs use
General and Force & node; the Resources graph keeps only its filters and
positions (view state, in this browser).

These values are **project defaults**, saved for everyone with the Save bar
(project admins). Everyone can change any of them for themselves in
**Settings > Personal > Appearance & input > My graphs**, which has the same
tabs. Graph physics and arrowheads keep their group override (below). The
other settings are overridden **one at a time**: changing a control makes it
yours (badge *Yours*; the others show *Project* or *Default*), the ↺ button on
a row goes back to the project default for that setting, and **Reset to
project** in a tab's header clears all your values in that tab. Your values
follow your account to every device and project.

Resolution, per setting: your value, else the project default, else ARK's
built-in default.

Before this change the Mind and marker settings were stored in each browser
(*Personal > This browser*, which now only points here). On the first load the
values you had changed there become your personal overrides, saved to your
account once (values your account already has win); the browser copy is then
retired. Values equal to the old defaults are not copied, so a project default
set later reaches you. The old place keeps a short pointer.

Card widths you drag on a Mind card and images you pin beside a card are
layout, not preferences: they are kept per project for you (on your account
profile, `prefs.mind_layout`; in this browser without an account) and move
there from the old browser storage the first time the project's Mind graph
shows those nodes. Choices made in a graph's own toolbar (layout, read/edit
mode, connection filters, Local graph depth and filters, node-graph positions,
panel heights) stay in this browser.

Storage (Go server only; the frozen Python server refuses these members):

```json
// PATCH /api/project/settings  (absent until set; null or {} clears)
{ "graph_display": { "mind": { "show_images": false }, "local": { "depth": 3 } } }

// PATCH /api/me  (sparse personal override; null or {} clears)
{ "prefs": { "appearance": { "graph_display": { "force": { "marker_scale": 2 } } } } }

// PATCH /api/me  (per project; an entry replaces that project, null removes it)
{ "prefs": { "mind_layout": { "prj_x": {
  "widths": { "U-1": 240 },
  "pinned_images": { "U-1": { "media_id": 7, "side": "right", "w": 260, "h": 130, "aspect": 2 } } } } } }
```

Keys and ranges: `general.transitions`, `general.agent_presence` (bool); `force.marker_scale` 0.5–2.5,
`force.marker_gap` 0–2, `force.marker_arc` 0.1–1; `mind.align` `left` or
`center`, `mind.auto_layout`, `mind.show_images` (bool), `mind.marker_scale`
0.3–2, `mind.image_size` 80–620 (whole px); `local.depth` 1–3 (whole),
`local.rings` (bool). Unknown tabs or keys and out-of-range values are
rejected on save and ignored when read. `mind_layout` keeps the 30 most
recently saved projects and at most 500 nodes per map.

## Node markers

Markers are the small icons next to a node: stars, flags, priority,
progress and symbols. Right-click a node (or several selected nodes) and
open **Markers**, or use the **Markers** row in the inspector:

- A check shows the markers the node already has. Choosing one of them
  again removes it, so a node never shows the same marker twice.
- Stars, flags, priority and progress are **one per node**: choosing a red
  star on a node with a yellow star replaces the yellow one. Symbols (check,
  question, important) can be combined.
- **Remove marker** removes one marker, **Clear markers** removes all of
  them. In the inspector, click a marker chip to remove it.
- A tag shortcut set to a marker tag (Settings › Personal › Appearance & input, for
  example `marker:star-yellow`) toggles that marker the same way.
- Every change is one undo step (Ctrl+Z on the graph) and appears in the
  activity of each node.

**Settings > Project > Appearance > Node markers** (project admins) lists the
marker groups the project uses. Each group has a name, a **One per node**
switch and its markers (symbol, colour, label). You can add, reorder and
remove groups and markers; an empty name or label shows the built-in text.
**Restore defaults** goes back to the built-in groups. A marker is stored on
the node as the tag `marker:<id>`; agents read the groups with
`GET /api/agent/v1/markers` and ARK applies the same rules to their writes.
Nodes that got several markers of one group before these rules existed show
one of them, and keep only that one the next time they are saved.

## Personal overrides

Some project appearance settings can be overridden for yourself in
**Settings > Personal > Appearance & input** (*My palette*; under *My graphs*:
*My graph physics*, *My arrowheads* and the per-setting graph display values
described above).
Resolution is project, then person, per group:

| Group | Project value | Personal override |
| --- | --- | --- |
| Palette | enabled flag, light and dark palettes | the same three values; saved themes can be picked from the project library but are not copied into your profile |
| Graph physics | repulsion, distance, gravity, damping; optional cluster by type | the four force values; the cluster fields are optional |
| Arrowheads | default style, per-relation table, Mind-map branches | a complete default style; the table and branch toggle default to ARK's defaults when omitted |

If you have saved an override for a group, it wins for you and the project
value for that group is not applied to you. Groups you have not overridden
inherit the project value, so later project changes still reach you. Each group
has **Use project setting** / **Override for me** and **Reset to project**,
which clears the override. A badge shows whether you see the inherited project
value, your saved override, or an override that is not saved yet. When your own
view is overridden, the project editors say so: saving there changes the
project for everyone else but not what you see.

Overrides belong to the person, not the project: they follow you into every
project. Signed-in accounts store them with their profile; without an account
profile (or on a read-only paired phone) they stay in this browser. Personal
overrides never write `project.json`, so a member without project-admin rights
can still set them. Theme mode, high contrast and text size were already
personal and are unchanged. Graph display settings (Mind, markers, Local
graph) are overridden per setting, see *Graphs* above. `GET/POST /api/agent/v1/project/palette` always reads and writes the
project palette, never anyone's override.

`PATCH /api/me` sets or clears groups; `null` clears one:

```json
{
  "prefs": {
    "appearance": {
      "palette": { "enabled": true, "light": { "preset": "sunset-light" }, "dark": { "preset": "indigo" } },
      "graph": null
    }
  }
}
```

A palette override needs `light` and `dark` (each a preset and/or `#RRGGBB`
colors whose background matches the mode); a graph override needs all four
physics values within the project ranges; an arrowheads override needs a
complete `style` (`head`, `placement`, `size`).

In a topic's Canvas, drag the divider between the graph and outline to resize
them. The divider also supports arrow keys and Home/End; double-click resets
the width. The ratio is remembered per project in the browser. Narrow screens
stack the two views. In Outline, **Display body** controls bodies and attachments
for all levels independently of the hierarchy's collapse state. It defaults
off and is remembered in the browser; full bodies load only when enabled.
The adjacent **Display info** toggle independently shows type, status, ID,
dates, owners, tags and topic navigation. It defaults on and is remembered in
the browser. Turn both toggles off for a title-only outline; neither changes
the hierarchy's collapse state.

Personal display preferences use `PATCH /api/me`:

```json
{
  "prefs": {
    "display": { "font_scale": 1.25, "high_contrast": true }
  }
}
```

The setting is personal, so it does not change another collaborator's text size
or contrast preference. Embedded third-party HTML and images retain their own
typography and colors.
