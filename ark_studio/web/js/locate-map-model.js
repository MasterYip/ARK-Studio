// "Locate in research map" (locate-map.js): the rules that need no DOM.

/** Settings › Shortcuts id of the action (default Alt+M, shortcuts.js). */
export const LOCATE_SHORTCUT_ID = "locateMap";

/**
 * Why a node cannot be located on the Map, or "" when it can:
 * "group" (a review group is not a unit), "bin" (in the recycle bin),
 * "external" (a reference into another project, "<project>/<uid>"),
 * "missing" (no uid at all).
 */
export function locateBlockReason(unit) {
  if (!unit || typeof unit !== "object") return "missing";
  if (unit.reviewGroup || unit.review_group || unit.kind === "review_group") return "group";
  if (unit.deleted_at || unit.trashed || unit.in_trash) return "bin";
  const ref = String(unit.uid ?? unit.id ?? "").trim();
  if (!ref) return "missing";
  if (ref.includes("/")) return "external";
  return "";
}

/** The reveal list for a new locate: only the node asked for. */
export function revealList(uid) {
  const ref = String(uid ?? "").trim();
  return ref ? [ref] : [];
}

/**
 * Nodes of a filtered graph that are shown only because they were located
 * (GraphFilterIndex.filter with `reveal`): [{ id, uid, title, reason }] where
 * reason is "filter" (the filters or type scope exclude it) or "limit" (it
 * matches, but the Map's node limit cut it off).
 */
export function revealedNodes(graph) {
  return (graph?.nodes || []).filter((node) => node.revealed)
    .map((node) => ({ id: node.id, uid: node.uid, title: node.title || "", reason: node.revealed === "limit" ? "limit" : "filter" }));
}

/** The notice for the revealed nodes: null (nothing to say), or its text key and whether "Clear filters" helps. */
export function revealNotice(graph) {
  const shown = revealedNodes(graph);
  if (!shown.length) return null;
  const filtered = shown.some((node) => node.reason === "filter");
  return { key: filtered ? "locate.revealed_filter" : "locate.revealed_limit", clearFilters: filtered, nodes: shown };
}

/**
 * The camera for a located node: centred on it, zoomed in to at least a
 * readable level (an already closer zoom is kept).
 */
export function locateCamera(node, zoom, layout = "force") {
  const floor = layout === "force" ? 1 : 0.85;
  const next = Math.max(Number(zoom) || 1, floor);
  return { zoom: next, pan: { x: -node.x * next, y: -node.y * next } };
}
