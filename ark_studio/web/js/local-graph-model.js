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

// ---- Centre history: the last few centres get a fading outer ring --------

export const CENTER_HISTORY_MAX = 4;
// Ring opacity per step back (most recent previous centre first).
export const CENTER_RING_ALPHAS = [0.8, 0.55, 0.35, 0.2];

const sameId = (a, b) => a !== null && a !== undefined && b !== null && b !== undefined && String(a) === String(b);

// The history after the centre moves from `previous` to `next`: `previous`
// goes first, `next` (now the centre) leaves the list and each node keeps
// only its most recent place.  Pure: returns a new array.
export function pushCenterHistory(history, previous, next, max = CENTER_HISTORY_MAX) {
  const list = Array.isArray(history) ? history : [];
  const head = previous === null || previous === undefined || sameId(previous, next) ? [] : [previous];
  const out = [];
  for (const id of [...head, ...list]) {
    if (sameId(id, next) || out.some((item) => sameId(item, id))) continue;
    out.push(id);
  }
  return out.slice(0, Math.max(0, max));
}

// Ring opacity for the node `steps` back (0 = the previous centre).
export function centerRingAlpha(steps) {
  return Number.isInteger(steps) && steps >= 0 && steps < CENTER_RING_ALPHAS.length ? CENTER_RING_ALPHAS[steps] : 0;
}

// Map shown node id -> ring opacity for the history entries that are in
// the current neighbourhood (`nodes`), never the current centre.
export function centerRings(history, nodes, current = null) {
  const rings = new Map();
  (Array.isArray(history) ? history : []).forEach((id, steps) => {
    if (sameId(id, current)) return;
    const node = (nodes || []).find((item) => sameId(item.id, id));
    const alpha = centerRingAlpha(steps);
    if (node && alpha > 0 && !rings.has(node.id)) rings.set(node.id, alpha);
  });
  return rings;
}

// ---- Click / double-click on a node --------------------------------------

export const DOUBLE_TAP_MS = 350;
export const DOUBLE_TAP_SLOP = 24;

// Classifies node taps (mouse click or touch tap): the first is "single"
// (show the node card), a second on the same node within `windowMs` and
// `slop` px is "double" (re-centre).  A third tap starts over.  The
// browser's own dblclick that follows a double is reported once by
// `dblclick()` (true = act, false = already handled); it is swallowed
// whatever node it hits, since the re-centred graph may have put another
// node under the pointer by then.
export function createTapClassifier({ windowMs = DOUBLE_TAP_MS, slop = DOUBLE_TAP_SLOP } = {}) {
  let last = null;       // { id, at, x, y } of a pending single tap
  let lastDouble = null; // { id, at } of the last double
  return {
    tap(id, at, x = 0, y = 0) {
      const double = last && sameId(last.id, id) && at - last.at <= windowMs
        && Math.hypot(x - last.x, y - last.y) <= slop;
      if (double) {
        last = null;
        lastDouble = { id, at };
        return "double";
      }
      last = { id, at, x, y };
      return "single";
    },
    dblclick(id, at) {
      if (lastDouble && at - lastDouble.at <= windowMs * 2) {
        lastDouble = null;
        return false;
      }
      last = null;
      lastDouble = { id, at };
      return true;
    },
    reset() { last = null; lastDouble = null; },
  };
}

// What a GraphView onSelect(node, open, event) means in the Local graph:
// "card" (pin the node card), "recenter" (Details + centre), "close" (close
// the card) or "none".  Pointer clicks and taps go through `taps`; the
// browser's dblclick (`open`) re-centres unless the taps already did; Enter
// and the context menu (not pointer clicks) re-centre as before.
export function localGraphClickAction(taps, node, open, event, now) {
  const pointer = event?.type === "pointerup" && typeof event.pointerType === "string";
  if (!node) {
    taps.reset();
    return pointer ? "close" : "none";
  }
  if (open) return taps.dblclick(node.id, now) ? "recenter" : "none";
  if (!pointer) return "recenter";
  return taps.tap(node.id, now, event.clientX, event.clientY) === "double" ? "recenter" : "card";
}
