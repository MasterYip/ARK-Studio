// Pure rules of the browser's project cache (docs/development/client-cache.md).
// No DOM or IndexedDB here, so Node tests cover keying, freshness, eviction and
// snapshot delta merging; client-cache.js does the storage.

// Bump when a cached payload shape changes in this client.  The server's
// bootstrap.server.cache_schema is part of every key too, so a server-side
// shape change also misses old entries.
export const CACHE_VERSION = 1;
// Default size cap for all cached projects of this browser profile.
export const DEFAULT_CAP_BYTES = 200 * 1024 * 1024;
// Account/session data inside aggregates (bootstrap, overview) is not covered
// by the project revision; re-check it in the background after this long.
export const AGGREGATE_FRESH_MS = 60_000;
// Reads this soon after the tab's own write wait for the server.
export const OWN_WRITE_WAIT_MS = 10_000;

// Cacheable GET routes.  Everything else (chat, files, terminals output,
// org, auth, assistant config, search) always goes to the network.
// ``scope``: "project" entries belong to the tab's project; "account"
// entries to the signed-in account only.  Every key also carries the
// account's opaque cache_id, and other accounts' entries are deleted when an
// account is confirmed, so per-person data (private agent sessions, session
// context) is stored only as the server returned it to this account.
// ``aggregate``: contains data outside the project revision.  ``volatile``:
// changes without a project revision (agent sessions, GPU, files): always
// shown from cache and revalidated in the background, never served as fresh.
const ROUTES = [
  { re: /^\/api\/bootstrap$/, scope: "project", kind: "bootstrap", aggregate: true },
  { re: /^\/api\/project$/, scope: "project", kind: "project", aggregate: true },
  { re: /^\/api\/project\/overview$/, scope: "project", kind: "overview", aggregate: true },
  { re: /^\/api\/project\/assignees$/, scope: "project", kind: "assignees", aggregate: true },
  { re: /^\/api\/skills\/setup\/state$/, scope: "project", kind: "setup", aggregate: true },
  { re: /^\/api\/workspace\/info$/, scope: "project", kind: "workspace", aggregate: true },
  { re: /^\/api\/graph\/snapshot$/, scope: "project", kind: "snapshot" },
  { re: /^\/api\/graph\/bodies$/, scope: "project", kind: "bodies" },
  { re: /^\/api\/units$/, scope: "project", kind: "units" },
  { re: /^\/api\/units\/[^/]+$/, scope: "project", kind: "unit" },
  { re: /^\/api\/units\/[^/]+\/comments$/, scope: "project", kind: "comments" },
  { re: /^\/api\/units\/[^/]+\/members$/, scope: "project", kind: "topic" },
  { re: /^\/api\/topics$/, scope: "project", kind: "topics" },
  { re: /^\/api\/timeline$/, scope: "project", kind: "timeline" },
  { re: /^\/api\/activity$/, scope: "project", kind: "activity" },
  // Execute: agent sessions, session context, GPU, resources, worktrees.
  { re: /^\/api\/agent-sessions$/, scope: "project", kind: "sessions", volatile: true },
  { re: /^\/api\/agent-sessions\/(?:presets|groups)$/, scope: "project", kind: "session-meta", volatile: true },
  { re: /^\/api\/agent-sessions\/[^/]+$/, scope: "project", kind: "session", volatile: true, detail: true },
  { re: /^\/api\/agent-sessions\/[^/]+\/transcript$/, scope: "project", kind: "transcript", volatile: true, detail: true },
  { re: /^\/api\/session-contexts$/, scope: "project", kind: "contexts", volatile: true },
  { re: /^\/api\/gpu\/dashboard$/, scope: "project", kind: "gpu", volatile: true },
  { re: /^\/api\/resources\/graph$/, scope: "project", kind: "resources", volatile: true },
  { re: /^\/api\/worktrees$/, scope: "project", kind: "worktrees", volatile: true },
  // Editor side panels: Changes and the file tree.
  { re: /^\/api\/workspace\/changes$/, scope: "project", kind: "changes", volatile: true },
  { re: /^\/api\/workspace\/tree$/, scope: "project", kind: "tree", volatile: true },
  { re: /^\/api\/me$/, scope: "account", kind: "me", aggregate: true },
  { re: /^\/api\/settings\/effective$/, scope: "account", kind: "settings", aggregate: true },
];

/** Route rule for a request path (with or without query), or null. */
export function cacheRoute(path) {
  const clean = String(path || "").replace(/^\.?\/?/, "/").split("#")[0];
  const pathname = clean.split("?")[0];
  const rule = ROUTES.find((item) => item.re.test(pathname));
  if (!rule) return null;
  return { ...rule, pathname, route: normalizeRoute(clean) };
}

/** Stable route key: path plus sorted query, without credentials. */
export function normalizeRoute(path) {
  const [pathname, query = ""] = String(path || "").split("?");
  const params = new URLSearchParams(query);
  for (const secret of ["mobile_token", "ark_project"]) params.delete(secret);
  const sorted = [...params.entries()].sort((a, b) => (a[0] + "=" + a[1]).localeCompare(b[0] + "=" + b[1]));
  const text = new URLSearchParams(sorted).toString();
  return text ? `${pathname}?${text}` : pathname;
}

/** Key prefix of one account on one server. */
export function identityScope({ origin, cacheId, schema = 0 }) {
  if (!origin || !cacheId) return "";
  return `v${CACHE_VERSION}.${schema}|${origin}|${cacheId}`;
}

/** Full entry key, or "" when the route cannot be cached right now. */
export function entryKey({ identity, project }, path) {
  const rule = cacheRoute(path);
  if (!rule || !identity) return "";
  if (rule.scope === "project") {
    if (!project) return "";
    return `${identity}|p:${project}|${rule.route}`;
  }
  return `${identity}|a|${rule.route}`;
}

export function projectPrefix(identity, project) {
  return `${identity}|p:${project}|`;
}

/** Unit id of a unit-detail route ("/api/units/12" -> "12"). */
export function unitRef(route) {
  const match = /^\/api\/units\/([^/?]+)$/.exec(String(route || "").split("?")[0]);
  return match ? decodeURIComponent(match[1]) : "";
}

/**
 * How to serve a cached entry: "fresh" (no request), "swr" (render it, then
 * revalidate in the background) or "network" (wait for a conditional request).
 * ``state``: { live, liveSince, ownWriteAt, now }.
 */
export function servePolicy(entry, rule, state = {}, { fresh = false } = {}) {
  if (!entry) return "network";
  if (fresh) return "network";
  const now = state.now ?? Date.now();
  // This tab wrote since the entry was checked: a read right after the write
  // shows its effect (wait for the server); later reads render the cache.
  if ((state.ownWriteAt || 0) > (entry.validatedAt || 0)) {
    return now - state.ownWriteAt < OWN_WRITE_WAIT_MS ? "network" : "swr";
  }
  if (entry.stale || rule?.volatile) return "swr";
  const coherent = state.live && (entry.validatedAt || 0) >= (state.liveSince || Infinity);
  if (!coherent) return "swr";
  if (rule?.aggregate && now - (entry.validatedAt || 0) > AGGREGATE_FRESH_MS) return "swr";
  return "fresh";
}

/**
 * Entries to evict so the cache fits ``cap`` bytes.  Whole projects go first,
 * least recently used first; the current project is kept as long as possible
 * and then trimmed entry by entry (its snapshot and bootstrap last).
 * entries: [{ key, project, bytes, used, kind }]
 */
export function planEviction(entries, cap, { protect = "" } = {}) {
  let total = entries.reduce((sum, item) => sum + (item.bytes || 0), 0);
  if (total <= cap) return [];
  const evict = [];
  const groups = new Map();
  for (const item of entries) {
    const group = groups.get(item.project || "") || { project: item.project || "", used: 0, items: [] };
    group.used = Math.max(group.used, item.used || 0);
    group.items.push(item);
    groups.set(group.project, group);
  }
  const order = [...groups.values()].filter((group) => group.project !== protect)
    .sort((a, b) => a.used - b.used);
  for (const group of order) {
    if (total <= cap) break;
    for (const item of group.items) {
      evict.push(item.key);
      total -= item.bytes || 0;
    }
  }
  if (total > cap && groups.has(protect)) {
    const keep = { snapshot: 2, bootstrap: 2, bodies: 1 };
    const items = [...groups.get(protect).items]
      .sort((a, b) => (keep[a.kind] || 0) - (keep[b.kind] || 0) || (a.used || 0) - (b.used || 0));
    for (const item of items) {
      if (total <= cap) break;
      evict.push(item.key);
      total -= item.bytes || 0;
    }
  }
  return evict;
}

const byNodeOrder = (a, b) => (a.updated_at === b.updated_at
  ? b.id - a.id : (a.updated_at < b.updated_at ? 1 : -1));

/**
 * Apply GET /api/changes to a cached graph snapshot (same rule as the Store
 * tests): replace upserted nodes, drop deleted ones, replace every edge that
 * touches a touched unit.  Returns a new snapshot; the input is not changed.
 * A lite snapshot (bodies: false) keeps no search_body; a full one keeps the
 * delta's.
 */
export function mergeSnapshotDelta(snapshot, delta) {
  if (!snapshot || !delta || delta.reset) return null;
  const touched = new Set(delta.touched || []);
  const drop = new Set([...(delta.deleted || []), ...(delta.units || []).map((node) => node.id)]);
  const lite = snapshot.bodies === false;
  const incoming = (delta.units || []).map((node) => {
    if (!lite || !("search_body" in node)) return node;
    const { search_body: _body, ...rest } = node;
    return rest;
  });
  const nodes = (snapshot.nodes || []).filter((node) => !drop.has(node.id)).concat(incoming);
  nodes.sort(byNodeOrder);
  const edges = (snapshot.edges || [])
    .filter((edge) => !touched.has(edge.source) && !touched.has(edge.target))
    .concat(delta.edges || []);
  edges.sort((a, b) => a.id - b.id);
  return { ...snapshot, nodes, edges, total: nodes.length, cursor: delta.cursor };
}

/** Apply a delta to a cached bodies list ({bodies: [[id, body]]}). */
export function mergeBodiesDelta(bodies, delta) {
  if (!bodies || !delta || delta.reset || delta.bodies === false) return null;
  const map = new Map(bodies.bodies || []);
  for (const id of delta.deleted || []) map.delete(id);
  for (const node of delta.units || []) map.set(node.id, node.search_body ?? "");
  return { ...bodies, cursor: delta.cursor, bodies: [...map.entries()].sort((a, b) => a[0] - b[0]) };
}

/**
 * Which cached routes a delta makes stale.  Unit details of touched units,
 * every topic page, and every aggregate/list (counts, Needs you, activity).
 * The snapshot itself is merged, not invalidated.
 */
export function staleAfterDelta(route, delta) {
  const rule = cacheRoute(route);
  if (!rule) return false;
  if (rule.kind === "snapshot" || rule.kind === "bodies") return Boolean(delta?.reset);
  if (rule.kind === "unit" || rule.kind === "comments") {
    if (delta?.reset) return true;
    const ref = unitRef(rule.pathname) || (rule.pathname.match(/^\/api\/units\/([^/]+)\/comments$/) || [])[1];
    const touched = new Set((delta?.touched || []).map(String));
    return touched.has(String(ref)) || !/^\d+$/.test(String(ref));
  }
  if (rule.scope === "account") return false;
  return true;
}

/**
 * Detail prefetch candidates, nearest first: the selected unit's neighbours,
 * then Needs you items, recently opened and visible units.  ``budget`` counts
 * units; ``slow`` (saveData / 2g) keeps only the neighbours.
 */
export function prefetchPlan({ neighbours = [], needs = [], recent = [], visible = [] },
  { cached = new Set(), budget = 24, slow = false } = {}) {
  const out = [];
  const seen = new Set();
  const groups = slow ? [neighbours] : [neighbours, needs, recent, visible];
  for (const group of groups) {
    for (const raw of group) {
      const id = String(raw ?? "");
      if (!id || seen.has(id) || cached.has(id)) continue;
      seen.add(id);
      out.push(id);
      if (out.length >= budget) return out;
    }
  }
  return out;
}

/** Normalize an entity tag for If-None-Match (proxies may weaken it). */
export function ifNoneMatch(etag) {
  const text = String(etag || "").trim();
  return text || "";
}

/** True for a connection where background downloads should pause. */
export function slowConnection(connection) {
  if (!connection) return false;
  if (connection.saveData) return true;
  return ["slow-2g", "2g"].includes(String(connection.effectiveType || ""));
}
