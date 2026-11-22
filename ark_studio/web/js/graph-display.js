// Graph display settings, grouped by graph type (Settings › Appearance ›
// Graphs).  Pure and DOM-free so tests/js/graph_display.test.mjs can import it.
//
// Every setting has three layers, resolved per setting:
//   personal override (prefs.appearance.graph_display, follows the account)
//   -> project default (settings.graph_display, set by project admins)
//   -> the built-in default below.
// Both stored layers are sparse {tab: {key: value}}; the server
// (ark_studio/server/internal/settings/graphdisplay.go) keeps the same table.
//
// Before T-0036 these values lived in this browser only (localStorage
// "ark.mind.v1", camelCase); `legacy` names the old key so the first load
// can move a person's look into their account (migrateLegacyMind).

export const LEGACY_MIND_KEY = "ark.mind.v1";
export const LEGACY_MIND_DONE_KEY = "ark.mind.v1.migrated";

// Tabs in display order.  "general" holds what applies to every graph type;
// physics and line ends (separate groups, see appearance-overrides.js) are
// shown in the Force and General tabs too.
export const GRAPH_DISPLAY_TABS = [
  { id: "general", fields: [
    { key: "transitions", kind: "bool", default: true, legacy: "nodeTransitions" },
    { key: "agent_presence", kind: "bool", default: true },
  ] },
  { id: "force", fields: [
    { key: "marker_scale", kind: "number", min: 0.5, max: 2.5, step: 0.05, default: 1.35, legacy: "markerScale" },
    { key: "marker_gap", kind: "number", min: 0, max: 2, step: 0.05, default: 0.55, legacy: "markerGap" },
    { key: "marker_arc", kind: "number", min: 0.1, max: 1, step: 0.01, default: 0.4, legacy: "markerArc" },
  ] },
  { id: "mind", fields: [
    { key: "align", kind: "choice", choices: ["left", "center"], default: "left", legacy: "align" },
    { key: "auto_layout", kind: "bool", default: true, legacy: "autoLayout" },
    { key: "marker_scale", kind: "number", min: 0.3, max: 2, step: 0.05, default: 0.7, legacy: "mindMarkerScale" },
    { key: "show_images", kind: "bool", default: true, legacy: "showMindImages" },
    { key: "image_size", kind: "int", min: 80, max: 620, step: 10, default: 260 },
  ] },
  { id: "local", fields: [
    { key: "depth", kind: "int", min: 1, max: 3, step: 1, default: 2 },
    { key: "rings", kind: "bool", default: true },
  ] },
];

export const GRAPH_TAB_IDS = GRAPH_DISPLAY_TABS.map((tab) => tab.id);

export const GRAPH_DISPLAY_DEFAULTS = Object.freeze(Object.fromEntries(GRAPH_DISPLAY_TABS.map((tab) => [
  tab.id, Object.freeze(Object.fromEntries(tab.fields.map((field) => [field.key, field.default]))),
])));

export function graphDisplayField(tab, key) {
  return GRAPH_DISPLAY_TABS.find((item) => item.id === tab)?.fields.find((field) => field.key === key) || null;
}

/** One value, cleaned; `undefined` when it is not valid for the field. */
export function cleanGraphDisplayValue(field, value) {
  if (!field || value === null || value === undefined) return undefined;
  if (field.kind === "bool") return typeof value === "boolean" ? value : undefined;
  if (field.kind === "choice") return field.choices.includes(value) ? value : undefined;
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  if (value < field.min || value > field.max) return undefined;
  if (field.kind === "int" && !Number.isInteger(value)) return undefined;
  return value;
}

/** Sparse, valid {tab: {key: value}}; anything else is dropped. */
export function normalizeGraphDisplay(value) {
  const out = {};
  if (!value || typeof value !== "object" || Array.isArray(value)) return out;
  for (const tab of GRAPH_DISPLAY_TABS) {
    const raw = value[tab.id];
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) continue;
    const clean = {};
    for (const field of tab.fields) {
      const item = cleanGraphDisplayValue(field, raw[field.key]);
      if (item !== undefined) clean[field.key] = item;
    }
    if (Object.keys(clean).length) out[tab.id] = clean;
  }
  return out;
}

/**
 * Effective values for one person: `{ values, source }`, where
 * `source[tab][key]` is "personal", "project" or "default".
 */
export function resolveGraphDisplay(project, personal) {
  const mine = normalizeGraphDisplay(personal);
  const shared = normalizeGraphDisplay(project);
  const values = {};
  const source = {};
  for (const tab of GRAPH_DISPLAY_TABS) {
    values[tab.id] = {};
    source[tab.id] = {};
    for (const field of tab.fields) {
      const own = mine[tab.id]?.[field.key];
      const team = shared[tab.id]?.[field.key];
      values[tab.id][field.key] = own !== undefined ? own : team !== undefined ? team : field.default;
      source[tab.id][field.key] = own !== undefined ? "personal" : team !== undefined ? "project" : "default";
    }
  }
  return { values, source };
}

/** Set (or with `undefined`/`null` clear) one setting of a sparse override. */
export function setGraphDisplayValue(override, tab, key, value) {
  const next = normalizeGraphDisplay(override);
  const field = graphDisplayField(tab, key);
  if (!field) return next;
  const clean = cleanGraphDisplayValue(field, value);
  if (clean === undefined) {
    if (next[tab]) {
      delete next[tab][key];
      if (!Object.keys(next[tab]).length) delete next[tab];
    }
  } else {
    next[tab] = { ...(next[tab] || {}), [key]: clean };
  }
  return next;
}

/** Drop a whole tab from a sparse override ("Reset tab to project"). */
export function clearGraphDisplayTab(override, tab) {
  const next = normalizeGraphDisplay(override);
  delete next[tab];
  return next;
}

/**
 * The camelCase view graph.js and the Local graph read (getMindSettings).
 * `values` is the resolved {tab: {key: value}} (defaults filled in).
 */
export function toMindSettings(values) {
  const v = values || GRAPH_DISPLAY_DEFAULTS;
  const pick = (tab, key) => {
    const value = v[tab]?.[key];
    return value === undefined ? GRAPH_DISPLAY_DEFAULTS[tab][key] : value;
  };
  return {
    align: pick("mind", "align"),
    autoLayout: pick("mind", "auto_layout"),
    markerScale: pick("force", "marker_scale"),
    markerGap: pick("force", "marker_gap"),
    markerArc: pick("force", "marker_arc"),
    mindMarkerScale: pick("mind", "marker_scale"),
    showMindImages: pick("mind", "show_images"),
    nodeTransitions: pick("general", "transitions"),
    agentPresence: pick("general", "agent_presence"),
    imageSize: pick("mind", "image_size"),
    localDepth: pick("local", "depth"),
    localRings: pick("local", "rings"),
  };
}

// ------------------------------------------------------------ migration

/**
 * The person's old browser-only values (the raw "ark.mind.v1" text) as a
 * sparse override.  Only values that differ from the built-in default are
 * kept: until now no project could set a default, so a value equal to the
 * default looked the same as "not set", and keeping it would pin the
 * person to it once their project picks a default.  Numbers are clamped the
 * way the old settings code clamped them.
 */
export function legacyMindOverride(raw) {
  let parsed;
  try { parsed = typeof raw === "string" ? JSON.parse(raw) : raw; } catch { return {}; }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  let out = {};
  for (const tab of GRAPH_DISPLAY_TABS) {
    for (const field of tab.fields) {
      if (!field.legacy || !(field.legacy in parsed)) continue;
      let value = parsed[field.legacy];
      if (field.kind === "number") {
        const n = Number(value);
        if (!Number.isFinite(n)) continue;
        value = Math.round(Math.max(field.min, Math.min(field.max, n)) * 1000) / 1000;
      } else if (field.kind === "bool") {
        // The old reader treated anything but `false` as on.
        value = value !== false;
      } else if (field.kind === "choice") {
        value = field.choices.includes(value) ? value : field.default;
      }
      if (value === field.default) continue;
      out = setGraphDisplayValue(out, tab.id, field.key, value);
    }
  }
  return out;
}

/**
 * Merge the old browser values into the person's saved override: a setting
 * the account already has wins (another device migrated first, or the
 * person chose it since).  `changed` says whether anything new was added.
 */
export function migrateLegacyMind(raw, current) {
  const base = normalizeGraphDisplay(current);
  let next = base;
  let changed = false;
  for (const [tab, values] of Object.entries(legacyMindOverride(raw))) {
    for (const [key, value] of Object.entries(values)) {
      if (base[tab]?.[key] !== undefined) continue;
      next = setGraphDisplayValue(next, tab, key, value);
      changed = true;
    }
  }
  return { override: next, changed };
}

// ------------------------------------------------------- classification

// Every browser-storage key a graph setting or graph state used before
// T-0036, with its category and where it is kept now:
//   a: display preference -> Appearance › Graphs (project default + personal override)
//   b: per-node / per-view state -> per project for the person, or local when transient
//   c: device-specific -> stays in this browser
//   retired: never written any more; removed on first load
export const GRAPH_STORAGE_CLASSES = {
  "ark.mind.v1": { category: "a", store: "settings.graph_display + prefs.appearance.graph_display" },
  "ark.mindWidths": { category: "b", store: "prefs.mind_layout[project].widths" },
  "ark.mindPinnedImages": { category: "b", store: "prefs.mind_layout[project].pinned_images" },
  "ark.mindImageSizes": { category: "retired", store: "" },
  "ark.mindPinnedMedia": { category: "retired", store: "" },
  "ark.graphLayout": { category: "b", store: "localStorage (last layout picked)" },
  "ark.graphMode.*": { category: "retired", store: "" },
  "ark.graphMode": { category: "retired", store: "" },
  "ark.graphMode.resources": { category: "b", store: "localStorage (Resources graph read/edit mode)" },
  "ark.readEditMode": { category: "b", store: "localStorage (read/edit mode, all layouts and Node details)" },
  "ark.edgeFilter.v1:*": { category: "b", store: "localStorage (relation filter per view)" },
  "ark.nodeGraph.v1:*": { category: "b", store: "localStorage (node-graph positions per view)" },
  "ark.localGraph.v2": { category: "b", store: "localStorage (Local graph toolbar choices)" },
  "ark.resourcesGraph.*": { category: "b", store: "localStorage (Resources graph filters and positions)" },
  "ark.graphHeight": { category: "c", store: "localStorage (panel height on this screen)" },
  "ark.infoHubPath": { category: "c", store: "localStorage (path on this machine)" },
  "ark.remote*": { category: "c", store: "localStorage (SSH tunnel from this machine)" },
};
