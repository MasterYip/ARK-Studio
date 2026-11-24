import { assigneeMatches } from "./filter-store.js";

const asciiFold = (text) => String(text || "").replace(/[A-Z]/g, (c) => c.toLowerCase());
const values = (value) => Array.isArray(value) ? value : String(value || "").split(",").filter(Boolean);
const enabled = (value) => [true, 1, "1", "true", "yes"].includes(value);
const compare = (a, b) => a === b ? 0 : a == null ? -1 : b == null ? 1 : a < b ? -1 : 1;

function likePattern(query) {
  return new RegExp([...asciiFold(query)].map((c) => c === "%" ? "[\\s\\S]*"
    : c === "_" ? "[\\s\\S]" : c.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(""), "u");
}

/** A Min. links value: a whole number >= 0 (0 = no filter). */
export function minLinksOf(value) {
  const n = Math.floor(Number(value));
  return Number.isFinite(n) && n > 0 ? Math.min(n, 1_000_000) : 0;
}

/** Map node id -> number of edges touching it (in + out; a self-link once). */
export function linkCounts(edges = []) {
  const out = new Map();
  for (const edge of edges || []) {
    out.set(edge.source, (out.get(edge.source) || 0) + 1);
    if (edge.target !== edge.source) out.set(edge.target, (out.get(edge.target) || 0) + 1);
  }
  return out;
}

export class GraphFilterIndex {
  constructor(snapshot) {
    this.edges = snapshot.edges || [];
    this.rows = (snapshot.nodes || []).map((node) => ({
      node, tags: new Set(node.tags || []),
      namespaces: new Set((node.tags || []).map((tag) => tag.includes(":") ? tag.split(":", 1)[0] : "tag")),
      search: [node.title, node.uid, node.search_body ?? node.body ?? node.excerpt].map(asciiFold),
    }));
    this.byId = new Map(this.rows.map((row) => [row.node.id, row]));
    this.neighbors = new Map();
    // Links per node: every edge of the snapshot that touches it, incoming
    // and outgoing, whatever the current filters show (a self-link counts once).
    this.degree = linkCounts(this.edges);
    for (const edge of this.edges) {
      for (const [a, b] of [[edge.source, edge.target], [edge.target, edge.source]]) {
        if (!this.neighbors.has(a)) this.neighbors.set(a, new Set());
        this.neighbors.get(a).add(b);
      }
    }
    this.orders = new Map();
    // A lite snapshot (GET /api/graph/snapshot?bodies=0) has no search_body;
    // text queries need attachBodies() first (GET /api/graph/bodies).
    this.hasBodies = snapshot.bodies !== false;
  }

  /**
   * A unit saved in this tab: update its row (fields the snapshot node
   * already has) so the next filter pass sees it.  Returns false if absent.
   */
  updateNode(unit) {
    const row = unit ? this.byId.get(unit.id) : null;
    if (!row) return false;
    for (const key of Object.keys(unit)) if (key in row.node) row.node[key] = unit[key];
    row.tags = new Set(row.node.tags || []);
    row.namespaces = new Set((row.node.tags || []).map((tag) => tag.includes(":") ? tag.split(":", 1)[0] : "tag"));
    row.search[0] = asciiFold(row.node.title);
    if (typeof unit.body === "string" && row.search[2] != null) row.search[2] = asciiFold(unit.body);
    this.orders.clear();
    return true;
  }

  /** Links of a node (in + out) in the whole snapshot. */
  degreeOf(id) {
    return this.degree.get(id) || 0;
  }

  /** Largest link count among the nodes (of `types`, if given): the Min. links slider's end. */
  maxDegree(types = null) {
    const wanted = types?.length ? new Set(types) : null;
    let max = 0;
    for (const { node } of this.rows) {
      if (wanted && !wanted.has(node.type)) continue;
      max = Math.max(max, this.degreeOf(node.id));
    }
    return max;
  }

  /**
   * Filter-rail counts in the shape of GET /api/units?facets=1: each group
   * ignores its own filter.  Used where the server cannot apply the query
   * (topic pages, and Min. links, which only the browser knows).
   * `linkTypes`: the types whose link counts set `links_max`.
   */
  facets(query = {}, me = "", { linkTypes = null } = {}) {
    const candidates = (...keys) => this.filter({ ...query, context: 0, reveal: undefined,
      ...Object.fromEntries(keys.map((key) => [key, undefined])) }, me, { all: true }).nodes;
    const counts = (nodes, field) => {
      const out = Object.create(null);
      for (const node of nodes) if (node[field]) out[node[field]] = (out[node[field]] || 0) + 1;
      return { ...out };
    };
    const tags = new Map(), namespaces = Object.create(null), unset = Object.create(null);
    const tagged = candidates("tags", "tags_unset");
    for (const node of tagged) for (const tag of node.tags || []) {
      if (/^(type|status|importance):/.test(tag)) continue;
      tags.set(tag, (tags.get(tag) || 0) + 1);
    }
    for (const [tag, count] of tags) {
      const at = tag.indexOf(":");
      const namespace = at < 0 ? "tag" : tag.slice(0, at), value = at < 0 ? tag : tag.slice(at + 1);
      (namespaces[namespace] ||= []).push({ tag, namespace, value, count });
    }
    for (const [ns, list] of Object.entries(namespaces)) {
      list.sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
      unset[ns] = tagged.filter((node) => !(node.tags || []).some((tag) => ns === "tag" ? !tag.includes(":") : tag.startsWith(ns + ":"))).length;
    }
    const assigned = candidates("assignee", "assignees");
    const nodes = this.rows.map((row) => row.node);
    const dates = nodes.flatMap((node) => [node.start_at, node.end_at]).filter(Boolean).sort();
    return { types: counts(candidates("types", "types_none", "origins"), "type"),
      statuses: counts(candidates("statuses"), "status"), importance: counts(candidates("importance"), "importance"),
      namespaces: { ...namespaces }, unset: { ...unset }, assignees: counts(assigned, "assignee_effective"),
      unassigned: assigned.filter((node) => !node.assignee_effective && !node.assignee).length,
      links_max: this.maxDegree(linkTypes),
      time: { min: dates[0] || null, max: dates.at(-1) || null,
        undated: nodes.filter((node) => !node.start_at && !node.end_at).length } };
  }

  /** Add full bodies for text search ({bodies: [[id, body], ...]}). */
  attachBodies(data) {
    for (const [id, body] of data?.bodies || []) {
      const row = this.byId.get(id);
      if (row) row.search[2] = asciiFold(body);
    }
    this.hasBodies = true;
  }

  ordered(sort = "updated") {
    if (this.orders.has(sort)) return this.orders.get(sort);
    const priority = (n) => ({ p0: 0, p1: 1, p2: 2 }[n.importance] ?? 3);
    const order = (a, b) => {
      if (sort === "title") return compare(asciiFold(a.title), asciiFold(b.title));
      if (sort === "type") return compare(a.type, b.type) || compare(asciiFold(a.title), asciiFold(b.title));
      if (sort === "start" || sort === "end") {
        const field = sort === "start" ? "start_at" : "end_at";
        const other = sort === "start" ? "end_at" : "start_at";
        return compare(a[field] == null, b[field] == null) || compare(a[field], b[field]) || compare(a[other], b[other]);
      }
      if (sort === "importance") return compare(priority(a), priority(b)) || compare(b.updated_at, a.updated_at);
      const field = sort === "created" ? "created_at" : "updated_at";
      return compare(b[field], a[field]) || compare(b.id, a.id);
    };
    const rows = [...this.rows].sort((a, b) => order(a.node, b.node) || compare(a.node.id, b.node.id));
    this.orders.set(sort, rows);
    return rows;
  }

  filter(query = {}, me = "", { all = false } = {}) {
    const types = new Set(values(query.types)), origins = new Set(values(query.origins));
    const statuses = new Set(values(query.statuses)), importance = new Set(values(query.importance));
    const groups = new Map();
    for (const tag of values(query.tags)) {
      const ns = tag.includes(":") ? tag.split(":", 1)[0] : "tag";
      if (!groups.has(ns)) groups.set(ns, new Set());
      groups.get(ns).add(tag);
    }
    const unset = new Set(values(query.tags_unset));
    const namespaces = [...new Set([...groups.keys(), ...unset])];
    const pattern = query.q?.trim() ? likePattern(query.q.trim()) : null;
    const from = query.from, to = query.to, today = new Date().toISOString().slice(0, 10);
    const inRange = (at) => at != null && (!from || at >= from) && (!to || at <= to);
    const assignees = values(query.assignee || query.assignees);
    const minLinks = minLinksOf(query.min_links ?? query.minLinks);
    const matches = ({ node: n, tags, namespaces: ownNamespaces, search }) => {
      if (enabled(query.types_none)) return false;
      if ((types.size || origins.size) && !types.has(n.type) && !origins.has(n.origin)) return false;
      if (statuses.size && !statuses.has(n.status)) return false;
      if (importance.size && !importance.has(n.importance)) return false;
      if (pattern && !search.some((text) => pattern.test(text))) return false;
      if (enabled(query.roots) && n.parents?.length) return false;
      if (!assigneeMatches(n, assignees, me)) return false;
      if ((from || to) && !inRange(n.start_at) && !inRange(n.end_at)) return false;
      if (enabled(query.has_dates) && !n.start_at && !n.end_at) return false;
      if (query.created_since && n.created_at < query.created_since) return false;
      if (query.updated_since && n.updated_at < query.updated_since) return false;
      if (minLinks && this.degreeOf(n.id) < minLinks) return false;
      if (enabled(query.overdue) && !(n.end_at && n.end_at < today && !["done", "abandoned", "archived"].includes(n.status))) return false;
      for (const ns of namespaces) {
        const wanted = groups.get(ns);
        if (!(wanted && [...wanted].some((tag) => tags.has(tag))) && !(unset.has(ns) && !ownNamespaces.has(ns))) return false;
      }
      return true;
    };
    const matched = this.ordered(query.sort).filter(matches);
    const limit = all ? matched.length : Math.max(1, Math.min(Number(query.limit) || 700, 10000));
    const selected = matched.slice(0, limit).map((row) => row.node);
    const ids = new Set(selected.map((n) => n.id));
    const extra = new Set();
    if (enabled(query.context)) {
      // Only neighbors of direct matches, never neighbors of newly added context.
      for (const id of ids) for (const other of this.neighbors.get(id) || []) {
        if (!ids.has(other) && this.byId.has(other)) extra.add(other);
      }
    }
    const context = [...extra].sort((a, b) => a - b).slice(0, 120);
    for (const id of context) ids.add(id);
    // `reveal` (uids or ids, "Locate in research map"): shown even when the
    // filters or the node limit leave them out, marked with why.
    const revealed = [];
    const wanted = new Set(values(query.reveal).map(String));
    if (wanted.size) {
      const matchedIds = new Set(matched.map((row) => row.node.id));
      for (const row of this.rows) {
        const n = row.node;
        if (ids.has(n.id) || !(wanted.has(String(n.uid)) || wanted.has(String(n.id)))) continue;
        ids.add(n.id);
        revealed.push({ node: n, reason: matchedIds.has(n.id) ? "limit" : "filter" });
      }
    }
    const edges = this.edges.filter((edge) => ids.has(edge.source) && ids.has(edge.target));
    const degree = new Map();
    for (const edge of edges) {
      degree.set(edge.source, (degree.get(edge.source) || 0) + 1);
      degree.set(edge.target, (degree.get(edge.target) || 0) + 1);
    }
    const shown = (node, extraFields) => {
      const { search_body, ...display } = node;
      return { ...display, ...extraFields, degree: degree.get(node.id) || 0 };
    };
    const nodes = [
      ...selected.map((node) => shown(node, { context: false })),
      ...context.map((id) => shown(this.byId.get(id).node, { context: true })),
      ...revealed.map(({ node, reason }) => shown(node, { context: false, revealed: reason })),
    ];
    return { nodes, edges, context_count: context.length, total: matched.length, revealed_count: revealed.length };
  }
}
