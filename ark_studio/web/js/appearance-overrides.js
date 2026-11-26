// Personal appearance overrides layered over the project's shared values.
//
// Resolution is project -> person, per setting group: when the signed-in
// person has set a group, their value wins for them and the project value is
// not applied; groups they have not set fall back to the project.  Overrides
// belong to the person (they apply in every project) and are stored with the
// account profile (PATCH /api/me prefs.appearance); browser storage is the
// immediate cache and the fallback when there is no account profile.
//
//   palette  {enabled, light, dark}  - project palette (saved themes stay project-wide)
//   graph    {repulsion, distance, gravity, damping, cluster*} - force-graph physics
//   arrows   {style, relations, mind_tree}            - edge line ends (arrows.js)
//   graph_display {tab: {key: value}}                 - graph display by graph type
//
// graph_display is the exception to "the group wins": it is sparse and
// resolved per setting (graph-display.js), so a person can change one Mind
// option and keep following the project for every other one.

import { normalizePersonalArrows } from "./arrows.js";
import { normalizeGraphDisplay, resolveGraphDisplay } from "./graph-display.js";

export const OVERRIDE_GROUPS = ["palette", "graph", "arrows", "graph_display"];
export const APPEARANCE_CHANGED = "ark:appearance-overrides";

const STORAGE_KEY = "ark.appearanceOverrides";
const HEX = /^#[0-9a-f]{6}$/i;
const GRAPH_KEYS = ["repulsion", "distance", "gravity", "damping"];
const CLUSTER_GROUPINGS = ["type", "status", "tag"]; // graph-core.js CLUSTER_GROUPINGS

function plainColors(value) {
  if (!value || typeof value !== "object") return null;
  const out = {};
  for (const [key, raw] of Object.entries(value)) {
    if (key === "preset" && typeof raw === "string") out.preset = raw;
    else if (typeof raw === "string" && HEX.test(raw)) out[key] = raw.toLowerCase();
  }
  return out.background ? out : null;
}

function normalizePalette(value) {
  if (!value || typeof value !== "object") return null;
  const light = plainColors(value.light), dark = plainColors(value.dark);
  if (!light || !dark) return null;
  return { enabled: value.enabled !== false, light, dark };
}

function normalizeGraph(value) {
  if (!value || typeof value !== "object") return null;
  const out = {};
  for (const key of GRAPH_KEYS) {
    const number = Number(value[key]);
    if (value[key] === null || value[key] === "" || !Number.isFinite(number)) return null;
    out[key] = number;
  }
  // Optional clustering fields (physics.js normalizeClusterSettings).
  if (typeof value.cluster === "boolean") out.cluster = value.cluster;
  for (const key of ["cluster_attract", "cluster_repel"]) {
    if (typeof value[key] === "number" && value[key] >= 0 && value[key] <= 1) out[key] = value[key];
  }
  if (CLUSTER_GROUPINGS.includes(value.cluster_by)) out.cluster_by = value.cluster_by;
  return out;
}

function normalizeDisplayGroup(value) {
  const clean = normalizeGraphDisplay(value);
  return Object.keys(clean).length ? clean : null;
}

const NORMALIZE = {
  palette: normalizePalette, graph: normalizeGraph, arrows: normalizePersonalArrows, graph_display: normalizeDisplayGroup,
};

/** Keep only well-formed groups; anything else counts as "not overridden". */
export function normalizeOverrides(value) {
  const out = {};
  if (!value || typeof value !== "object") return out;
  for (const group of OVERRIDE_GROUPS) {
    const normalized = NORMALIZE[group](value[group]);
    if (normalized) out[group] = normalized;
  }
  return out;
}

/**
 * Effective appearance for one person: `{ palette, graph, arrows, graph_display, source }`
 * where `source[group]` is "personal" when the override won and "project" when the
 * project value was inherited (graph_display: when any setting is the
 * person's own; `graph_display.source` has the per-setting answer).  The
 * project's saved-theme library is kept.
 */
export function resolveAppearance(projectSettings, overrides) {
  const project = projectSettings && typeof projectSettings === "object" ? projectSettings : {};
  const mine = normalizeOverrides(overrides);
  const source = Object.fromEntries(OVERRIDE_GROUPS.map((group) => [group, mine[group] ? "personal" : "project"]));
  const palette = mine.palette
    ? { ...mine.palette, saved: project.palette?.saved || {} }
    : project.palette;
  const graph = mine.graph ? { ...mine.graph } : project.graph;
  const arrows = mine.arrows ? JSON.parse(JSON.stringify(mine.arrows)) : project.arrows;
  const graphDisplay = resolveGraphDisplay(project.graph_display, mine.graph_display);
  return { palette, graph, arrows, graph_display: graphDisplay, source };
}

/** Apply a `{group: value | null}` patch; `null` clears that group. */
export function mergeOverrides(current, patch) {
  const next = { ...normalizeOverrides(current) };
  for (const group of OVERRIDE_GROUPS) {
    if (!patch || !(group in patch)) continue;
    const normalized = patch[group] === null ? null : NORMALIZE[group](patch[group]);
    if (normalized) next[group] = normalized;
    else delete next[group];
  }
  return next;
}

// ------------------------------------------------------------- live state
let overrides = readStored();

function readStored() {
  try { return normalizeOverrides(JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}")); }
  catch { return {}; }
}

function store() {
  try {
    if (Object.keys(overrides).length) localStorage.setItem(STORAGE_KEY, JSON.stringify(overrides));
    else localStorage.removeItem(STORAGE_KEY);
  } catch { /* private browsing */ }
}

export function appearanceOverrides() {
  return JSON.parse(JSON.stringify(overrides));
}

/**
 * Change the person's overrides locally.  `replace` adopts a whole saved set
 * (the account profile on boot); otherwise `value` is a `{group: value|null}`
 * patch.  Persisting to the account is profile.js's job.
 */
export function setAppearanceOverrides(value, { replace = false } = {}) {
  const patch = replace
    ? Object.fromEntries(OVERRIDE_GROUPS.map((group) => [group, value?.[group] ?? null]))
    : value || {};
  overrides = mergeOverrides(overrides, patch);
  store();
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(APPEARANCE_CHANGED, { detail: appearanceOverrides() }));
  }
  return appearanceOverrides();
}
