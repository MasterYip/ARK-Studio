// Pure helpers of the graph's 3D view (graph3d.js): remembered choices,
// which labels to show, node highlight states and screen-space picking.
// No DOM, no three.js: unit-tested in tests/js/graph3d.test.mjs.

export const VIEW_KEY = "ark.graph3d.view.v1";
export const SETTINGS_KEY = "ark.graph3d.settings.v1";

export const GRAPH3D_DEFAULTS = Object.freeze({
  labelDistance: 0.5, // 0..1 of the layout size: labels within this reach show
  nodeSize: 1,        // 0.5..2, times the 2D size rule
  lowPower: false,    // pixel ratio 1, no antialiasing
  autoRotate: false,
});

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));

/** Normalise stored 3D settings (unknown keys dropped, ranges clamped). */
export function normalizeGraph3DSettings(raw) {
  const src = raw && typeof raw === "object" ? raw : {};
  const num = (v, d, lo, hi) => (Number.isFinite(Number(v)) && v !== null && v !== "" ? clamp(Number(v), lo, hi) : d);
  return {
    labelDistance: num(src.labelDistance, GRAPH3D_DEFAULTS.labelDistance, 0, 1),
    nodeSize: num(src.nodeSize, GRAPH3D_DEFAULTS.nodeSize, 0.5, 2),
    lowPower: src.lowPower === true,
    autoRotate: src.autoRotate === true,
  };
}

/** Storage key of the 2D/3D choice for one user on one page. */
export function viewStorageKey(user, page) {
  return `${VIEW_KEY}:${user || "_"}:${page || "_"}`;
}

export function loadViewChoice(storage, user, page) {
  try { return storage?.getItem(viewStorageKey(user, page)) === "3d" ? "3d" : "2d"; } catch { return "2d"; }
}

export function saveViewChoice(storage, user, page, view) {
  try { storage?.setItem(viewStorageKey(user, page), view === "3d" ? "3d" : "2d"); } catch { /* storage unavailable */ }
}

export function loadGraph3DSettings(storage, user) {
  try { return normalizeGraph3DSettings(JSON.parse(storage?.getItem(`${SETTINGS_KEY}:${user || "_"}`) || "null")); }
  catch { return normalizeGraph3DSettings(null); }
}

export function saveGraph3DSettings(storage, user, settings) {
  try { storage?.setItem(`${SETTINGS_KEY}:${user || "_"}`, JSON.stringify(normalizeGraph3DSettings(settings))); }
  catch { /* storage unavailable */ }
}

/** 3D is offered on screens at least this wide, or with touch orbit. */
export function graph3DAvailable({ webgl = true, compact = false, backdrop = false } = {}) {
  return Boolean(webgl && !compact && !backdrop);
}

/**
 * Node states for drawing: 0 normal, 1 dimmed (outside the focused
 * neighbourhood), 2 neighbour of the focus, 3 hovered, 4 selected.
 * `focus`: the ids whose neighbourhood is highlighted (selection, else the
 * hovered node), over links whose relation is in `relations`.
 */
export function nodeStates(ids, edges, { selected = new Set(), hover = null, relations = null } = {}) {
  // A selection made elsewhere (a node this graph does not show) is no focus.
  const present = new Set(ids);
  const seeds = new Set([...selected].filter((id) => present.has(id)));
  if (!seeds.size && hover !== null && hover !== undefined && present.has(hover)) seeds.add(hover);
  const states = new Uint8Array(ids.length);
  if (!seeds.size) return states;
  const near = new Set(seeds);
  for (const edge of edges) {
    if (relations && !relations.has(edge.relation)) continue;
    if (seeds.has(edge.source)) near.add(edge.target);
    if (seeds.has(edge.target)) near.add(edge.source);
  }
  for (let i = 0; i < ids.length; i++) {
    const id = ids[i];
    states[i] = seeds.has(id) && selected.has(id) ? 4 : id === hover ? 3 : near.has(id) ? 2 : 1;
  }
  return states;
}

/**
 * Label opacity at camera distance `d`: 1 up to 60% of `reach`, then a
 * smooth fade to 0 at `reach`.
 */
export function labelFade(d, reach) {
  if (!(reach > 0)) return 0;
  const t = clamp((d - reach * 0.6) / (reach * 0.4), 0, 1);
  return 1 - t * t * (3 - 2 * t);
}

/**
 * Which nodes get a label: always the hovered and selected ones (and the
 * hovered node's neighbours), then the nearest others within `reach`, at
 * most `budget` in all.  `dist[i]`: camera distance; `onScreen(i)`.
 * Returns [{ i, alpha }], forced ones first.
 */
export function pickLabels({ n, dist, reach, budget = 60, forced = [], onScreen = () => true }) {
  const out = [];
  const seen = new Set();
  for (const i of forced) {
    if (i < 0 || i >= n || seen.has(i) || !onScreen(i)) continue;
    seen.add(i); out.push({ i, alpha: 1, forced: true });
  }
  const near = [];
  for (let i = 0; i < n; i++) if (!seen.has(i) && dist[i] < reach && onScreen(i)) near.push(i);
  near.sort((a, b) => dist[a] - dist[b] || a - b);
  for (const i of near) {
    if (out.length >= budget) break;
    const alpha = labelFade(dist[i], reach);
    if (alpha > 0.02) out.push({ i, alpha });
  }
  return out;
}

/**
 * Screen-space pick: the node under (px, py), given projected centres
 * `sx, sy` (css px), depth `sz` (NDC −1..1, smaller is nearer) and screen
 * radius `sr`.  Among overlapping hits the nearest one wins.  −1: none.
 */
export function pickNode(px, py, sx, sy, sz, sr, n, slop = 3) {
  let best = -1, bestZ = Infinity;
  for (let i = 0; i < n; i++) {
    if (!(sz[i] > -1 && sz[i] < 1)) continue;
    const dx = px - sx[i], dy = py - sy[i], r = sr[i] + slop;
    if (dx * dx + dy * dy > r * r) continue;
    if (sz[i] < bestZ) { bestZ = sz[i]; best = i; }
  }
  return best;
}

/** Camera distance that fits a sphere of `radius` in a `fov`° (vertical) view of `aspect`. */
export function fitDistance(radius, fov = 50, aspect = 1) {
  const v = (fov * Math.PI) / 180 / 2;
  const h = Math.atan(Math.tan(v) * Math.max(0.1, aspect));
  return radius / Math.sin(Math.min(v, h)) * 1.05;
}

/** Cap of the renderer pixel ratio. */
export function pixelRatioCap(devicePixelRatio, lowPower, coarse = false) {
  const dpr = Number(devicePixelRatio) || 1;
  if (lowPower) return 1;
  return Math.min(dpr, coarse ? 1.5 : 2);
}
