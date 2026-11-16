// App-wide workbench layout (VS Code style): an activity bar, a left
// sidebar, a right (secondary) sidebar and a bottom panel around the current
// workspace.  Sidebars and the panel are *positions*; registered views
// (Filters, Explorer, Details, Assistant, …) can be placed in any of them and
// the arrangement is a global, per-browser preference.
//
// A container holds one or more *sections*: stacked top to bottom in the
// sidebars, side by side in the bottom panel.  A section shows one of its
// views at a time (several views in a section are tabs).
//
// Pure and DOM-free so Node's test runner can import it
// (tests/js/workbench_model.test.mjs).  Every operation returns a new object.
//
// Layout shape:
//   { v: 1, containers: { left|right|bottom: {
//       views: [id],        // every view of the container (= the sections' views in order)
//       active: id,         // the container's focused view
//       visible: bool, size: px, maximized?: bool,   // maximized: bottom panel only
//       sections: [{ views: [id], active: id, size: px (relative weight),
//                    collapsed?: bool, maximized?: bool }] } } }
// Layouts without `sections` (saved before sections existed) get one
// section per container holding all its views.

export const WORKBENCH_STORE_KEY = "ark.workbench.v1";
export const CONTAINERS = ["left", "right", "bottom"];

// Registry metadata for the built-in views (the DOM side lives in
// workbench.js).  `container` is the default placement.
export const VIEW_DEFAULTS = [
  { id: "filters", container: "left", icon: "filter", titleKey: "workbench.view.filters" },
  { id: "explorer", container: "left", icon: "folder", titleKey: "editor.explorer" },
  { id: "search", container: "left", icon: "search", titleKey: "workbench.view.search" },
  { id: "changes", container: "left", icon: "git", titleKey: "workbench.view.changes" },
  { id: "details", container: "right", icon: "info", titleKey: "inspector.details" },
  { id: "localgraph", container: "right", icon: "graph", titleKey: "workbench.view.localgraph" },
  { id: "assistant", container: "bottom", icon: "sparkles", titleKey: "assistant.title" },
  { id: "terminal", container: "bottom", icon: "terminal", titleKey: "files.terminal" },
];

export const DEFAULT_SIZES = { left: 288, right: 430, bottom: 260 };
export const SIZE_LIMITS = { left: [200, 560], right: [320, 760], bottom: [120, 900] };
// Section sizes are relative weights in px (the height, or the width in the
// bottom panel, at the last resize); a section is never smaller than
// SECTION_MIN while expanded.
export const SECTION_SIZE = 240;
export const SECTION_LIMITS = [40, 4000];
export const SECTION_MIN = 80;
// Sidebars stack sections vertically, the bottom panel horizontally.
export const SECTION_AXIS = { left: "y", right: "y", bottom: "x" };

export function clampSize(container, value) {
  const [min, max] = SIZE_LIMITS[container] || [100, 1000];
  const size = Number(value);
  if (!Number.isFinite(size) || size <= 0) return DEFAULT_SIZES[container] || min;
  return Math.round(Math.max(min, Math.min(max, size)));
}

// The workspace between the sidebars keeps at least MIN_CONTENT_W px: on a
// narrow window both open sidebars are drawn narrower (proportionally, never
// below their minimum width).  Only the drawn width changes; the stored sizes
// come back when the window is wide enough again.
export const MIN_CONTENT_W = 480;

/** Drawn sidebar widths for `viewport` px (left/right: stored widths, 0 = hidden). */
export function fitSidebars(left, right, viewport, { activity = 44, minContent = MIN_CONTENT_W } = {}) {
  const room = viewport - activity - minContent;
  if (!(viewport > 0) || left + right <= room) return { left, right };
  const minL = left ? Math.min(left, SIZE_LIMITS.left[0]) : 0;
  const minR = right ? Math.min(right, SIZE_LIMITS.right[0]) : 0;
  const spare = (left - minL) + (right - minR);
  if (spare <= 0) return { left, right };
  const cut = Math.min(spare, left + right - Math.max(room, minL + minR));
  return {
    left: Math.round(left - (cut * (left - minL)) / spare),
    right: Math.round(right - (cut * (right - minR)) / spare),
  };
}

export function clampSectionSize(value) {
  const size = Number(value);
  if (!Number.isFinite(size) || size <= 0) return SECTION_SIZE;
  return Math.round(Math.max(SECTION_LIMITS[0], Math.min(SECTION_LIMITS[1], size)));
}

function knownIds(views = VIEW_DEFAULTS) {
  return views.map((view) => view.id);
}

function defaultContainerOf(id, views = VIEW_DEFAULTS) {
  return views.find((view) => view.id === id)?.container || "left";
}

function newSection(ids = [], size = SECTION_SIZE) {
  return { views: ids.slice(), active: ids[0] || "", size: clampSectionSize(size) };
}

export function createWorkbench(views = VIEW_DEFAULTS) {
  const containers = {};
  for (const name of CONTAINERS) {
    const ids = views.filter((view) => view.container === name).map((view) => view.id);
    containers[name] = {
      views: ids, active: ids[0] || "", visible: name !== "bottom", size: DEFAULT_SIZES[name],
      sections: ids.length ? [newSection(ids)] : [],
    };
  }
  return { v: 1, containers };
}

// Fix up containers edited in place: drop empty sections, derive `views`,
// repair actives and keep at most one maximized section.
function finalize(containers) {
  const out = {};
  for (const name of CONTAINERS) {
    const box = containers[name];
    const sections = [];
    let maximized = false;
    for (const section of box.sections) {
      if (!section.views.length) continue;
      const clean = { views: section.views, active: section.views.includes(section.active) ? section.active : section.views[0],
        size: clampSectionSize(section.size) };
      if (section.collapsed) clean.collapsed = true;
      if (section.maximized && !maximized) { clean.maximized = true; maximized = true; }
      sections.push(clean);
    }
    const ids = sections.flatMap((section) => section.views);
    const item = { views: ids, active: ids.includes(box.active) ? box.active : sections[0]?.active || "",
      visible: Boolean(box.visible), size: box.size, sections };
    if (box.maximized) item.maximized = true;
    out[name] = item;
  }
  return { v: 1, containers: out };
}

function cloneContainers(layout) {
  const out = {};
  for (const name of CONTAINERS) {
    const box = layout.containers[name];
    out[name] = { ...box, sections: box.sections.map((section) => ({ ...section, views: section.views.slice() })) };
  }
  return out;
}

// Apply `fn` to a mutable copy; `fn` returns false to keep the layout as is.
function edit(layout, fn) {
  const containers = cloneContainers(layout);
  if (fn(containers) === false) return layout;
  return finalize(containers);
}

// Make any stored or hand-built layout valid: every known view appears in
// exactly one section of one container (unknown ids are dropped, missing
// ones go to their default container), actives point at a member and sizes
// are clamped.  Layouts without sections get one section per container.
export function normalizeWorkbench(raw, views = VIEW_DEFAULTS) {
  const known = knownIds(views);
  const base = createWorkbench(views);
  const source = raw && typeof raw === "object" && raw.containers && typeof raw.containers === "object" ? raw.containers : {};
  const seen = new Set();
  const take = (list) => {
    const ids = [];
    for (const id of Array.isArray(list) ? list : []) {
      const key = String(id);
      if (!known.includes(key) || seen.has(key)) continue;
      seen.add(key);
      ids.push(key);
    }
    return ids;
  };
  const containers = {};
  for (const name of CONTAINERS) {
    const item = source[name] && typeof source[name] === "object" ? source[name] : base.containers[name];
    let sections;
    if (Array.isArray(item.sections) && item.sections.length) {
      sections = item.sections.map((section) => {
        const src = section && typeof section === "object" ? section : {};
        const out = { views: take(src.views), active: String(src.active || ""), size: clampSectionSize(src.size) };
        if (src.collapsed) out.collapsed = true;
        if (src.maximized) out.maximized = true;
        return out;
      });
    } else {
      sections = [{ views: take(item.views), active: String(item.active || ""), size: SECTION_SIZE }];
    }
    containers[name] = {
      active: String(item.active || ""),
      visible: item.visible === undefined ? base.containers[name].visible : Boolean(item.visible),
      size: clampSize(name, item.size ?? base.containers[name].size),
      sections,
    };
    if (item.maximized && name === "bottom") containers[name].maximized = true;
  }
  for (const id of known) {
    if (seen.has(id)) continue;
    const box = containers[defaultContainerOf(id, views)];
    const last = box.sections.filter((section) => section.views.length).pop();
    if (last) last.views.push(id); else box.sections.push(newSection([id]));
  }
  return finalize(containers);
}

export function containerOf(layout, id) {
  return CONTAINERS.find((name) => layout.containers[name].views.includes(id)) || "";
}

// { container, index } of the section holding `id` (index -1 when unknown).
export function sectionOf(layout, id) {
  const container = containerOf(layout, id);
  const index = container ? layout.containers[container].sections.findIndex((section) => section.views.includes(id)) : -1;
  return { container, index };
}

function withContainer(layout, name, patch) {
  return { ...layout, containers: { ...layout.containers, [name]: { ...layout.containers[name], ...patch } } };
}

function findSection(containers, id) {
  for (const name of CONTAINERS) {
    const section = containers[name].sections.find((item) => item.views.includes(id));
    if (section) return { name, section };
  }
  return { name: "", section: null };
}

// The section new tabs go to: the one holding the container's focused view.
function focusSection(box) {
  return box.sections.find((section) => section.views.length && section.views.includes(box.active))
    || box.sections.filter((section) => section.views.length).pop() || null;
}

function takeOut(containers, id, keepActiveIn = null) {
  const { name, section } = findSection(containers, id);
  if (!section) return null;
  section.views = section.views.filter((item) => item !== id);
  if (section.active === id && section !== keepActiveIn) section.active = section.views[0] || "";
  return { name, section };
}

function focus(containers, name, section, id) {
  section.active = id;
  section.collapsed = false;
  const box = containers[name];
  for (const other of box.sections) if (other !== section) other.maximized = false;
  box.active = id;
  box.visible = true;
}

// Move a view into a container's focused section (or reorder it within its
// own section when it is already in `to`).  The moved view becomes that
// container's active view and the container is shown; the old section
// falls back to its first remaining view.  `index` is the tab position.
export function moveView(layout, id, to, index = Infinity) {
  const from = containerOf(layout, id);
  if (!from || !CONTAINERS.includes(to)) return layout;
  return edit(layout, (containers) => {
    const own = findSection(containers, id).section;
    let target = from === to ? own : focusSection(containers[to]);
    takeOut(containers, id, target);
    if (!target) {
      target = newSection();
      containers[to].sections.push(target);
    }
    const at = Math.max(0, Math.min(target.views.length, Number.isFinite(index) ? index : target.views.length));
    target.views.splice(at, 0, id);
    focus(containers, to, target, id);
  });
}

// Move a view into section `sectionIndex` of `to` as a tab at `index`.
export function moveViewToSection(layout, id, to, sectionIndex, index = Infinity) {
  if (!containerOf(layout, id) || !layout.containers[to]?.sections[sectionIndex]) return layout;
  return edit(layout, (containers) => {
    const target = containers[to].sections[sectionIndex];
    takeOut(containers, id, target);
    const at = Math.max(0, Math.min(target.views.length, Number.isFinite(index) ? index : target.views.length));
    target.views.splice(at, 0, id);
    focus(containers, to, target, id);
  });
}

// Split: put a view in a new section of `to` inserted before section `at`
// (0 = first/top, sections.length = last/bottom), taking half of the space
// of the section it is split from.
export function splitView(layout, id, to, at = Infinity) {
  if (!containerOf(layout, id) || !layout.containers[to]) return layout;
  return edit(layout, (containers) => {
    const list = containers[to].sections;
    const position = Math.max(0, Math.min(list.length, Number.isFinite(at) ? at : list.length));
    const anchor = list[position] || null;
    const neighbour = anchor || list[position - 1] || null;
    const source = takeOut(containers, id);
    // Splitting the only view of a section next to itself changes nothing.
    if (source && !source.section.views.length && (source.section === anchor || source.section === list[position - 1]) && source.name === to) {
      source.section.views.push(id);
      return false;
    }
    const sizes = neighbour ? Math.max(SECTION_MIN, Math.round(neighbour.size / 2)) : SECTION_SIZE;
    if (neighbour) neighbour.size = Math.max(SECTION_MIN, neighbour.size - sizes);
    const section = newSection([id], sizes);
    list.splice(anchor ? list.indexOf(anchor) : list.length, 0, section);
    focus(containers, to, section, id);
  });
}

// Move a whole section (all its tabs) to position `at` of `to`.
export function moveSection(layout, from, index, to, at = Infinity) {
  const section = layout.containers[from]?.sections[index];
  if (!section || !layout.containers[to]) return layout;
  return edit(layout, (containers) => {
    const moving = containers[from].sections[index];
    const list = containers[to].sections;
    const position = Math.max(0, Math.min(list.length, Number.isFinite(at) ? at : list.length));
    const anchor = list[position] || null;
    if (anchor === moving || (from === to && list[position - 1] === moving)) return false;
    containers[from].sections.splice(index, 1);
    list.splice(anchor ? list.indexOf(anchor) : list.length, 0, moving);
    if (from !== to && moving.views.includes(containers[from].active)) containers[from].active = "";
    focus(containers, to, moving, moving.active);
  });
}

// Merge a whole section into another one as tabs.
export function mergeSection(layout, from, index, to, targetIndex) {
  const moving = layout.containers[from]?.sections[index];
  const target = layout.containers[to]?.sections[targetIndex];
  if (!moving || !target || moving === target) return layout;
  return edit(layout, (containers) => {
    const src = containers[from].sections[index];
    const dst = containers[to].sections[targetIndex];
    dst.views.push(...src.views);
    const shown = src.active;
    src.views = [];
    focus(containers, to, dst, shown);
  });
}

function editSections(layout, name, fn) {
  if (!layout.containers[name]) return layout;
  return edit(layout, (containers) => fn(containers[name].sections, containers[name]));
}

export function toggleSectionCollapsed(layout, name, index, force = null) {
  return editSections(layout, name, (sections) => {
    const section = sections[index];
    if (!section) return false;
    const next = force === null ? !section.collapsed : Boolean(force);
    if (Boolean(section.collapsed) === next) return false;
    section.collapsed = next;
    if (next) section.maximized = false;
  });
}

// Maximize one section (the others show only their headers); again restores.
export function toggleSectionMaximized(layout, name, index) {
  return editSections(layout, name, (sections) => {
    const section = sections[index];
    if (!section) return false;
    const next = !section.maximized;
    for (const item of sections) item.maximized = false;
    section.maximized = next;
    if (next) section.collapsed = false;
  });
}

// Sizes (px weights) of the sections by index; others keep theirs.
export function setSectionSizes(layout, name, sizes) {
  return editSections(layout, name, (sections) => {
    let changed = false;
    for (const [key, value] of Object.entries(sizes || {})) {
      const section = sections[Number(key)];
      if (!section) continue;
      const size = clampSectionSize(value);
      if (section.size !== size) { section.size = size; changed = true; }
    }
    return changed ? undefined : false;
  });
}

// Double-click on a splitter: every expanded section gets the same share.
export function equalizeSections(layout, name) {
  return editSections(layout, name, (sections) => {
    if (sections.every((section) => section.size === SECTION_SIZE && !section.maximized)) return false;
    for (const section of sections) { section.size = SECTION_SIZE; section.maximized = false; }
  });
}

// Dragging the splitter between sections i and j (> i) by `delta` px: i
// grows and j shrinks (or the reverse), neither below `min`.
export function resizePair(sizes, i, j, delta, min = SECTION_MIN) {
  const out = sizes.slice();
  const total = out[i] + out[j];
  const a = Math.max(min, Math.min(total - min, out[i] + delta));
  out[i] = Math.round(a);
  out[j] = Math.round(total - a);
  return out;
}

// Show a view: activate it in its section, expand that section and make
// the container visible.
export function activateView(layout, id) {
  const { container, index } = sectionOf(layout, id);
  if (!container) return layout;
  const box = layout.containers[container];
  const section = box.sections[index];
  const maximizedOther = box.sections.some((item, i) => item.maximized && i !== index);
  if (box.active === id && box.visible && section.active === id && !section.collapsed && !maximizedOther) return layout;
  return edit(layout, (containers) => focus(containers, container, containers[container].sections[index], id));
}

export function setContainerVisible(layout, name, visible) {
  if (!layout.containers[name] || layout.containers[name].visible === Boolean(visible)) return layout;
  return withContainer(layout, name, { visible: Boolean(visible) });
}

export function toggleContainer(layout, name) {
  if (!layout.containers[name]) return layout;
  return setContainerVisible(layout, name, !layout.containers[name].visible);
}

export function setContainerSize(layout, name, size) {
  if (!layout.containers[name]) return layout;
  const value = clampSize(name, size);
  if (layout.containers[name].size === value) return layout;
  return withContainer(layout, name, { size: value });
}

export function toggleMaximized(layout, name) {
  if (!layout.containers[name]) return layout;
  return withContainer(layout, name, { maximized: !layout.containers[name].maximized, visible: true });
}

// Views that do not apply to the current workspace (e.g. Filters in the
// Editor) are hidden from their container; a section with no applicable
// views disappears and a container with none collapses there.
export function applicableViews(layout, name, applies = () => true) {
  return (layout.containers[name]?.views || []).filter((id) => applies(id));
}

// What each section shows for the current workspace:
//   [{ index, views (applicable), shown (id or ""), collapsed }]
// Sections without applicable views are left out.  A lone section is never
// collapsed; a maximized section collapses the others.
export function sectionDisplay(layout, name, applies = () => true) {
  const box = layout.containers[name];
  if (!box) return [];
  const out = [];
  box.sections.forEach((section, index) => {
    const ids = section.views.filter((id) => applies(id));
    if (!ids.length) return;
    out.push({ index, views: ids, shown: ids.includes(section.active) ? section.active : ids[0],
      collapsed: Boolean(section.collapsed), maximized: Boolean(section.maximized) });
  });
  if (out.length === 1) {
    out[0].collapsed = false;
    out[0].maximized = false;
  } else if (out.some((item) => item.maximized)) {
    for (const item of out) item.collapsed = !item.maximized;
  }
  return out;
}

// Views currently on screen in a container (one per expanded section).
export function displayedViews(layout, name, applies = () => true) {
  return sectionDisplay(layout, name, applies).filter((item) => !item.collapsed).map((item) => item.shown);
}

// The container's main displayed view: its focused view when on screen,
// otherwise the first one shown.
export function displayedView(layout, name, applies = () => true) {
  const box = layout.containers[name];
  if (!box) return "";
  const shown = displayedViews(layout, name, applies);
  if (shown.includes(box.active)) return box.active;
  return shown[0] || sectionDisplay(layout, name, applies)[0]?.shown || "";
}

// A view is shown when its container is visible and its section displays
// it expanded.
export function isViewShown(layout, id, applies = () => true) {
  const name = containerOf(layout, id);
  return Boolean(name && layout.containers[name].visible && displayedViews(layout, name, applies).includes(id));
}

export function containerShown(layout, name, applies = () => true) {
  return Boolean(layout.containers[name]?.visible && sectionDisplay(layout, name, applies).length);
}

// ------------------------------------------------------------ persistence
export function serializeWorkbench(layout) {
  return JSON.stringify(normalizeWorkbench(layout));
}

// `legacy` carries the pre-workbench preferences so a first load keeps the
// user's setup: ark.filtersVisible, ark.inspectorPane, ark.sessionDock,
// ark.railWidth, ark.inspectorWidth, ark.sessionHeight (raw strings).
export function parseWorkbench(text, legacy = {}, views = VIEW_DEFAULTS) {
  let parsed = null;
  try { parsed = JSON.parse(text || "null"); } catch { parsed = null; }
  if (parsed && parsed.v === 1) return normalizeWorkbench(parsed, views);
  return migrateLegacy(legacy, views);
}

export function migrateLegacy(legacy = {}, views = VIEW_DEFAULTS) {
  let layout = createWorkbench(views);
  const flag = (value, fallback) => (value === null || value === undefined ? fallback : String(value) !== "0");
  layout = setContainerVisible(layout, "left", flag(legacy.filtersVisible, true));
  layout = setContainerVisible(layout, "right", flag(legacy.inspectorPane, true));
  layout = setContainerVisible(layout, "bottom", String(legacy.sessionDock ?? "0") === "1");
  if (legacy.railWidth) layout = setContainerSize(layout, "left", legacy.railWidth);
  if (legacy.inspectorWidth) layout = setContainerSize(layout, "right", legacy.inspectorWidth);
  if (legacy.sessionHeight) layout = setContainerSize(layout, "bottom", legacy.sessionHeight);
  return normalizeWorkbench(layout, views);
}

// ---------------------------------------------------------------- presets
// Built-in arrangements plus the user's saved ones.  User presets live in
// the account profile (prefs.workbench, validated by the server) with a
// browser fallback; this module only shapes them.
export const PRESET_STORE_KEY = "ark.workbench.presets.v1";
export const BUILTIN_PRESETS = ["default", "focus", "research", "writing", "review"];
export const MAX_PRESETS = 20;

// `sections`: [[views…], …] stacked in order (the first view is shown).
const BUILTIN_SPECS = {
  // Details over the Local graph.
  research: {
    left: { sections: [["filters", "search", "explorer", "changes"]], active: "filters", visible: true },
    right: { sections: [["details"], ["localgraph"]], active: "details", visible: true },
    bottom: { sections: [["assistant", "terminal"]], active: "assistant", visible: false },
  },
  // Explorer over Search; the Local graph over Details.
  writing: {
    left: { sections: [["explorer", "filters", "changes"], ["search"]], active: "explorer", visible: true },
    right: { sections: [["localgraph"], ["details"]], active: "localgraph", visible: true },
    bottom: { sections: [["assistant", "terminal"]], active: "assistant", visible: false },
  },
  // Changes over Explorer; Terminal beside the Assistant.
  review: {
    left: { sections: [["changes"], ["explorer", "search", "filters"]], active: "changes", visible: true },
    right: { sections: [["details", "localgraph"]], active: "details", visible: false },
    bottom: { sections: [["terminal"], ["assistant"]], active: "terminal", visible: true },
  },
};

function withMaximized(layout, value) {
  const bottom = { ...layout.containers.bottom };
  if (value) bottom.maximized = true; else delete bottom.maximized;
  return { ...layout, containers: { ...layout.containers, bottom } };
}

// The layout a built-in preset produces.  Focus/Zen keeps the current
// placement and hides every sidebar and the panel.
export function builtinPresetLayout(id, current = null, views = VIEW_DEFAULTS) {
  if (id === "default") return createWorkbench(views);
  if (id === "focus") {
    let next = normalizeWorkbench(current || createWorkbench(views), views);
    for (const name of CONTAINERS) next = setContainerVisible(next, name, false);
    return withMaximized(next, false);
  }
  const spec = BUILTIN_SPECS[id];
  if (!spec) return null;
  const containers = {};
  for (const name of CONTAINERS) {
    const { sections, ...rest } = spec[name];
    containers[name] = { ...rest, size: DEFAULT_SIZES[name], sections: sections.map((ids) => newSection(ids)) };
  }
  return normalizeWorkbench({ v: 1, containers }, views);
}

export function normalizePresetStore(raw, views = VIEW_DEFAULTS) {
  const src = raw && typeof raw === "object" ? raw : {};
  const presets = [];
  const ids = new Set();
  for (const item of Array.isArray(src.presets) ? src.presets : []) {
    const id = String(item?.id || "");
    const name = String(item?.name || "").trim().slice(0, 60);
    if (!/^u:[a-z0-9-]{1,40}$/.test(id) || ids.has(id) || !name || !item.layout || typeof item.layout !== "object") continue;
    ids.add(id);
    presets.push({ id, name, layout: normalizeWorkbench(item.layout, views) });
    if (presets.length >= MAX_PRESETS) break;
  }
  const def = String(src.default || "");
  return { presets, default: BUILTIN_PRESETS.includes(def) || ids.has(def) ? def : "" };
}

export function presetLayout(id, store, current = null, views = VIEW_DEFAULTS) {
  const user = (store?.presets || []).find((item) => item.id === id);
  if (user) return normalizeWorkbench(user.layout, views);
  return builtinPresetLayout(id, current, views);
}

// The layout "Reset layout" goes back to: the default preset.
export function defaultLayout(store, current = null, views = VIEW_DEFAULTS) {
  return presetLayout(store?.default || "default", store, current, views) || createWorkbench(views);
}

// What the server stores for a layout (integer sizes in its bounds).
export function storableLayout(layout) {
  const clean = normalizeWorkbench(layout);
  const containers = {};
  for (const name of CONTAINERS) {
    const box = clean.containers[name];
    containers[name] = { views: box.views, active: box.active, visible: box.visible,
      size: Math.max(80, Math.min(2000, Math.round(box.size))),
      sections: box.sections.map((section) => {
        const out = { views: section.views, active: section.active, size: clampSectionSize(section.size) };
        if (section.collapsed) out.collapsed = true;
        if (section.maximized) out.maximized = true;
        return out;
      }) };
    if (box.maximized) containers[name].maximized = true;
  }
  return { v: 1, containers };
}

function slug(name) {
  return String(name).toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32) || "layout";
}

// Save (or overwrite a same-named) user preset.  Returns { store, id }.
export function savePreset(store, name, layout) {
  const clean = normalizePresetStore(store);
  const label = String(name || "").trim().slice(0, 60);
  if (!label) return { store: clean, id: "" };
  const existing = clean.presets.find((item) => item.name.toLowerCase() === label.toLowerCase());
  if (existing) {
    return { id: existing.id, store: { ...clean, presets: clean.presets.map((item) => (item.id === existing.id ? { ...item, name: label, layout: storableLayout(layout) } : item)) } };
  }
  if (clean.presets.length >= MAX_PRESETS) return { store: clean, id: "" };
  const used = new Set(clean.presets.map((item) => item.id));
  let id = `u:${slug(label)}`;
  for (let n = 2; used.has(id); n += 1) id = `u:${slug(label).slice(0, 36)}-${n}`;
  return { id, store: { ...clean, presets: [...clean.presets, { id, name: label, layout: storableLayout(layout) }] } };
}

export function renamePreset(store, id, name) {
  const clean = normalizePresetStore(store);
  const label = String(name || "").trim().slice(0, 60);
  if (!label) return clean;
  return { ...clean, presets: clean.presets.map((item) => (item.id === id ? { ...item, name: label } : item)) };
}

export function deletePreset(store, id) {
  const clean = normalizePresetStore(store);
  return { presets: clean.presets.filter((item) => item.id !== id), default: clean.default === id ? "" : clean.default };
}

export function setDefaultPreset(store, id) {
  const clean = normalizePresetStore(store);
  const known = BUILTIN_PRESETS.includes(id) || clean.presets.some((item) => item.id === id);
  return { ...clean, default: known && id !== "default" ? id : "" };
}
