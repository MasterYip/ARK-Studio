// How a data update touches a drawn graph (Force, Mind, Node and the Local
// graph).  No DOM here, so it can be tested in Node (tests/js).
//
//   "none"       nothing the graph keeps changed
//   "data"       attribute-only (assignee, dates, importance, body, ...):
//                the node objects are updated in place, nothing is redrawn
//   "visual"     a drawn field (title, type, status ring, tags/markers,
//                media thumbnails, ...) or an edge's relation: the node or
//                edge is redrawn in place, no relayout, no refit, no reheat
//   "structure"  nodes or links were added or removed: GraphView.setData
//                (with remote: true the camera and existing positions stay)

/** Node fields the renderers read (graph.js, graph-core.js, node-card.js, markers.js). */
export const DRAWN_NODE_FIELDS = new Set([
  "title", "type", "status", "tags", "customColor", "custom_color", "media",
  "member_count", "degree", "radius", "uid", "for_me", "context",
]);

/** Edge fields that are drawn (line style, arrow, label). */
export const DRAWN_EDGE_FIELDS = new Set(["relation", "label", "weight", "hidden"]);

// Fields the GraphView owns (positions, simulation state, measured card
// geometry, memoised drawing).  Incoming data never overwrites them.
export const GRAPH_OWNED_FIELDS = new Set([
  "x", "y", "vx", "vy", "fx", "fy", "w", "h", "titleW", "imageW", "contentH", "mediaH",
  "imageMedia", "imageMediaList", "pinnedMeta", "pinnedImage", "customW", "nodeW",
  "mindManual", "depth", "color", "shownReversed", "routeBend",
]);

const RANK = { none: 0, data: 1, visual: 2, structure: 3 };

/** The larger of two change kinds. */
export function maxChange(a, b) {
  return (RANK[b] || 0) > (RANK[a] || 0) ? b : a;
}

function owned(key) {
  return key.startsWith("_") || GRAPH_OWNED_FIELDS.has(key);
}

function same(a, b) {
  if (a === b) return true;
  if (a == null || b == null) return (a ?? null) === (b ?? null);
  if (typeof a !== "object" || typeof b !== "object") return false;
  try { return JSON.stringify(a) === JSON.stringify(b); } catch { return false; }
}

/**
 * Fields of `next` that differ from `prev` (graph-owned fields ignored).
 * @returns {string[]}
 */
export function changedFields(prev, next) {
  const out = [];
  for (const key of Object.keys(next || {})) {
    if (owned(key)) continue;
    if (!same(prev?.[key], next[key])) out.push(key);
  }
  return out;
}

/**
 * Classify one node update.
 * @param {object} prev  the node as the graph holds it
 * @param {object} next  the incoming node / saved unit
 * @param {{ drawn?: Set<string> }} options  drawn: override DRAWN_NODE_FIELDS
 *   (e.g. add "importance" for a view that draws it)
 * @returns {{ kind: "none"|"data"|"visual", fields: string[] }}
 */
export function classifyNodeChange(prev, next, { drawn = DRAWN_NODE_FIELDS } = {}) {
  const fields = changedFields(prev, next);
  if (!fields.length) return { kind: "none", fields };
  return { kind: fields.some((key) => drawn.has(key)) ? "visual" : "data", fields };
}

/** Classify one edge update (same id / endpoints). */
export function classifyEdgeChange(prev, next, { drawn = DRAWN_EDGE_FIELDS } = {}) {
  const fields = changedFields(prev, next).filter((key) => key !== "source" && key !== "target");
  if (!fields.length) return { kind: "none", fields };
  return { kind: fields.some((key) => drawn.has(key)) ? "visual" : "data", fields };
}

/** Identity of an edge: its id, else its endpoints. */
export function edgeKey(edge) {
  return edge?.id != null ? `#${edge.id}` : `${edge?.source}>${edge?.target}`;
}

/**
 * Compare what a graph shows with incoming data.
 * @param {{nodes: object[], edges: object[]}} current
 * @param {{nodes: object[], edges: object[]}} next
 * @param {{ drawn?: Set<string>, drawnEdges?: Set<string>, structuralRelations?: (relation: string) => boolean }} options
 *   structuralRelations: relations that shape the layout (Mind/tree parent
 *   links); changing such an edge's relation is a structure change.
 * @returns {{ kind: string, nodes: Array<{id, kind, fields}>, edges: Array<{key, kind, fields}> }}
 */
export function classifyGraphUpdate(current, next, options = {}) {
  const curNodes = current?.nodes || [];
  const nextNodes = next?.nodes || [];
  const result = { kind: "none", nodes: [], edges: [] };
  const structure = () => ({ ...result, kind: "structure" });
  if (curNodes.length !== nextNodes.length) return structure();
  const byId = new Map(curNodes.map((node) => [node.id, node]));
  for (const node of nextNodes) {
    const prev = byId.get(node.id);
    if (!prev) return structure();
    const change = classifyNodeChange(prev, node, options);
    if (change.kind === "none") continue;
    result.nodes.push({ id: node.id, ...change });
    result.kind = maxChange(result.kind, change.kind);
  }
  // Edges between nodes the graph does not hold are dropped by setData too.
  const ids = new Set(nextNodes.map((node) => node.id));
  const inGraph = (edge) => ids.has(edge.source) && ids.has(edge.target);
  const curEdges = (current?.edges || []).filter(inGraph);
  const nextEdges = (next?.edges || []).filter(inGraph);
  if (curEdges.length !== nextEdges.length) return structure();
  const edgeMap = new Map(curEdges.map((edge) => [edgeKey(edge), edge]));
  for (const edge of nextEdges) {
    const prev = edgeMap.get(edgeKey(edge));
    if (!prev || prev.source !== edge.source || prev.target !== edge.target) return structure();
    const change = classifyEdgeChange(prev, edge, { drawn: options.drawnEdges });
    if (change.kind === "none") continue;
    if (change.fields.includes("relation") && options.structuralRelations
        && (options.structuralRelations(prev.relation) || options.structuralRelations(edge.relation))) return structure();
    result.edges.push({ key: edgeKey(edge), ...change });
    result.kind = maxChange(result.kind, change.kind);
  }
  return result;
}

/** Copy incoming fields onto a graph node, keeping graph-owned ones. */
export function assignNodeData(node, next) {
  for (const key of Object.keys(next || {})) {
    if (!owned(key)) node[key] = next[key];
  }
  return node;
}
