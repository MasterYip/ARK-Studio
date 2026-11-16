// Local graph (Obsidian style): the neighbourhood of one node, computed from
// the client graph snapshot ({ nodes, edges }).  Pure and DOM-free so
// tests/js/local_graph_model.test.mjs can import it.

export const LOCAL_GRAPH_STORE_KEY = "ark.localGraph.v1";
export const LOCAL_GRAPH_CAP = 300;
export const DIRECTIONS = ["both", "out", "in"];
// Layouts offered in the Local graph (the main graph's Force and Mind).
export const LOCAL_LAYOUTS = ["force", "mind"];

export function normalizeOptions(value = {}) {
  const src = value && typeof value === "object" ? value : {};
  const depth = Math.round(Number(src.depth));
  const list = (items) => (Array.isArray(items) ? [...new Set(items.map(String))].slice(0, 100) : []);
  return {
    depth: depth >= 1 && depth <= 3 ? depth : 1,
    direction: DIRECTIONS.includes(src.direction) ? src.direction : "both",
    layout: LOCAL_LAYOUTS.includes(src.layout) ? src.layout : "force",
    // Hidden relation / node types (empty = show everything).
    hiddenRelations: list(src.hiddenRelations),
    hiddenTypes: list(src.hiddenTypes),
  };
}

export function relationsIn(snapshot) {
  return [...new Set((snapshot?.edges || []).map((edge) => edge.relation).filter(Boolean))].sort();
}

export function typesIn(snapshot) {
  return [...new Set((snapshot?.nodes || []).map((node) => node.type).filter(Boolean))].sort();
}

// Breadth-first from `centerId` up to `depth` levels along edges that pass
// the relation/direction filters, skipping hidden node types (the centre is
// always kept).  Returns nodes (with `level`), the edges between them,
// `parents` (Map id -> the node it was reached from; the centre has none)
// and `truncated` when the cap stopped the walk.  `isReversed(relation)`
// (display-only reversal from the arrow settings) makes "out"/"in" follow
// the direction links are shown in.
export function neighbourhood(snapshot, centerId, options = {}, cap = LOCAL_GRAPH_CAP, isReversed = null) {
  const opts = normalizeOptions(options);
  const byId = new Map((snapshot?.nodes || []).map((node) => [node.id, node]));
  const center = byId.get(centerId);
  if (!center) return { nodes: [], edges: [], parents: new Map(), truncated: false, total: 0, center: null };
  const hiddenRelations = new Set(opts.hiddenRelations);
  const hiddenTypes = new Set(opts.hiddenTypes);
  const edges = (snapshot.edges || []).filter((edge) => !hiddenRelations.has(edge.relation));
  const out = new Map(), inc = new Map();
  for (const edge of edges) {
    const [from, to] = isReversed?.(edge.relation) ? [edge.target, edge.source] : [edge.source, edge.target];
    if (!out.has(from)) out.set(from, []);
    if (!inc.has(to)) inc.set(to, []);
    out.get(from).push(to);
    inc.get(to).push(from);
  }
  const levels = new Map([[centerId, 0]]);
  const parents = new Map();
  let frontier = [centerId];
  let truncated = false;
  let seen = 1;
  for (let level = 1; level <= opts.depth && frontier.length && !truncated; level += 1) {
    const next = [];
    for (const id of frontier) {
      const around = [
        ...(opts.direction !== "in" ? out.get(id) || [] : []),
        ...(opts.direction !== "out" ? inc.get(id) || [] : []),
      ];
      for (const other of around) {
        if (levels.has(other)) continue;
        const node = byId.get(other);
        if (!node || hiddenTypes.has(node.type)) continue;
        seen += 1;
        if (levels.size >= cap) { truncated = true; continue; }
        levels.set(other, level);
        parents.set(other, id);
        next.push(other);
      }
    }
    frontier = next;
  }
  const nodes = [...levels].map(([id, level]) => ({ ...byId.get(id), level }));
  const kept = edges.filter((edge) => levels.has(edge.source) && levels.has(edge.target)
    && (opts.direction === "both" || edge.source === centerId || edge.target === centerId
      || Math.abs(levels.get(edge.source) - levels.get(edge.target)) <= 1));
  return { nodes, edges: kept, parents, truncated, total: seen, center };
}

// Mind layout input for a neighbourhood: the centre is the root and every
// node hangs under the node it was reached from (so levels = distance).
// Mind draws a branch for an edge child -> parent, so the edge that joins a
// node to its parent is given in that orientation (`reversed` marks an edge
// that points the other way in the project); every other edge stays a
// cross-link.  Returns { nodes, edges, parents, root, levels }.
export function mindInput(data) {
  if (!data?.center) return { nodes: [], edges: [], parents: new Map(), root: null, levels: [] };
  const parents = data.parents || new Map();
  const treeEdge = new Map();   // child id -> edge id joining it to its parent
  for (const edge of data.edges) {
    for (const [child, parent] of [[edge.source, edge.target], [edge.target, edge.source]]) {
      if (parents.get(child) !== parent) continue;
      const best = treeEdge.get(child);
      // Prefer an edge already pointing child -> parent.
      if (!best || (best.source !== child && edge.source === child)) treeEdge.set(child, edge);
    }
  }
  const chosen = new Set([...treeEdge.values()].map((edge) => edge.id));
  const edges = data.edges.map((edge) => {
    if (!chosen.has(edge.id)) return edge;
    const child = [...treeEdge].find(([, item]) => item.id === edge.id)[0];
    // `treeEdge`: oriented for the tree; display-only reversal leaves it alone.
    return edge.source === child ? { ...edge, treeEdge: true }
      : { ...edge, source: edge.target, target: edge.source, reversed: true, treeEdge: true };
  });
  const levels = [];
  for (const node of data.nodes) (levels[node.level] ||= []).push(node.id);
  return { nodes: data.nodes, edges, parents, root: data.center.id, levels };
}

// The node a view should centre on: an open node tab in the Editor wins,
// then the node shown in Details / selected on a graph.
export function centerFor(state = {}, snapshot = null) {
  const key = String(state.editorKey || "");
  if (state.tab === "editor" && key.startsWith("unit:")) {
    const uid = key.slice(5);
    const node = (snapshot?.nodes || []).find((item) => item.uid === uid);
    if (node) return node.id;
  }
  const id = state.detail?.id ?? state.selectedId ?? null;
  return id === null || id === undefined ? null : id;
}
