// "Home shows": which nodes the Home header graph draws in one project, per
// person (account prefs.home_filter[project id], settings/homefilter.go).
// The server applies it to GET /api/project/overview, so a large project's
// Home fetches only the nodes it draws: the `limit` most recently updated
// (0 = all) of the chosen types and statuses (empty = any), updated in the
// last `days` days (0 = any time).  Pure: no DOM, tested in Node.

export const HOME_LIMITS = [200, 500, 1000, 2000, 0];
export const HOME_DAYS = [0, 7, 30, 90];
// Measured (docs/guide/home-overview.md, "Home shows"): with 500 nodes the
// overview of a 30k-node project is ~80 KB and the header's force layout
// settles in ~6 s at ~7 ms of main thread per frame.
export const DEFAULT_HOME_LIMIT = 500;
// The header graph draws at most this many nodes (the newest): at 1,000 a
// frame costs ~17 ms, at 2,000 ~90 ms (the page stutters for half a minute).
export const HERO_NODE_BUDGET = 1000;

const MAX_LIMIT = 1_000_000;
const MAX_DAYS = 3650;
const MAX_VALUES = 40;

export function defaultHomeFilter() {
  return { limit: DEFAULT_HOME_LIMIT, types: [], statuses: [], days: 0 };
}

function whole(value, fallback, max) {
  const n = Number(value);
  return Number.isInteger(n) && n >= 0 && n <= max ? n : fallback;
}

function names(value) {
  if (!Array.isArray(value)) return [];
  const out = [...new Set(value.filter((item) => typeof item === "string" && item && item.length <= 64))];
  return out.slice(0, MAX_VALUES).sort();
}

/** A stored or edited filter in its canonical form (unknown parts dropped). */
export function normalizeHomeFilter(raw) {
  const base = defaultHomeFilter();
  if (!raw || typeof raw !== "object") return base;
  return {
    limit: whole(raw.limit, base.limit, MAX_LIMIT),
    types: names(raw.types),
    statuses: names(raw.statuses),
    days: whole(raw.days, 0, MAX_DAYS),
  };
}

export function sameHomeFilter(a, b) {
  return JSON.stringify(normalizeHomeFilter(a)) === JSON.stringify(normalizeHomeFilter(b));
}

export function homeFilterIsDefault(filter) {
  return sameHomeFilter(filter, defaultHomeFilter());
}

/** The prefs.home_filter entry to save: null restores the default. */
export function homeFilterEntry(filter) {
  const value = normalizeHomeFilter(filter);
  return homeFilterIsDefault(value) ? null : value;
}

/** The filter saved for a project in an account's prefs (default when none). */
export function homeFilterFromPrefs(prefs, projectId) {
  const entry = prefs?.home_filter?.[projectId];
  return entry ? normalizeHomeFilter(entry) : defaultHomeFilter();
}

/** Query parameters that ask the overview for this filter explicitly. */
export function homeFilterParams(filter) {
  const value = normalizeHomeFilter(filter);
  return {
    graph_limit: value.limit,
    graph_types: value.types.join(","),
    graph_statuses: value.statuses.join(","),
    graph_days: value.days,
  };
}

/**
 * What Home tells about its graph, from the overview's graph member
 * ({nodes, total, matched, limit, filter}): `shown` nodes drawn (at most
 * the header's budget) of `total`;
 * `limited` when some live node is not drawn (the limit or a filter cut
 * it).  An older server without the counts reports nothing as limited.
 */
export function homeGraphSummary(graph, budget = HERO_NODE_BUDGET) {
  const filter = normalizeHomeFilter(graph?.filter || {});
  const sent = Array.isArray(graph?.nodes) ? graph.nodes.length : 0;
  const shown = graph?.filter ? Math.min(sent, heroBudget(filter, budget)) : sent;
  const total = Number.isFinite(graph?.total) ? graph.total : sent;
  const matched = Number.isFinite(graph?.matched) ? graph.matched : total;
  const narrowed = Boolean(filter.types.length || filter.statuses.length || filter.days);
  return { shown, total, matched, limit: filter.limit, filter, narrowed, limited: shown < total };
}

/**
 * The header graph's nodes: the first `budget` of the overview's list (the
 * server sends them newest first) and the links among them.  Older servers
 * sent every node in id order: the newest are then the last ones.
 */
export function heroGraphSample(nodes = [], edges = [], budget = HERO_NODE_BUDGET, { newestLast = false } = {}) {
  const list = Array.isArray(nodes) ? nodes : [];
  if (list.length <= budget) return { nodes: list, edges: Array.isArray(edges) ? edges : [] };
  const keep = newestLast ? list.slice(list.length - budget) : list.slice(0, budget);
  const ids = new Set(keep.map((node) => node.id));
  const end = (value) => (value && typeof value === "object" ? value.id : value);
  return { nodes: keep, edges: (edges || []).filter((edge) => ids.has(end(edge.source)) && ids.has(end(edge.target))) };
}

/** Nodes the header graph simulates for a filter: its limit within the budget. */
export function heroBudget(filter, budget = HERO_NODE_BUDGET) {
  const limit = normalizeHomeFilter(filter).limit;
  return limit > 0 ? Math.min(limit, budget) : budget;
}
