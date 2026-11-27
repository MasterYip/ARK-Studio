// What the camera does after the shown node set changed (a filter change,
// a refresh): keep it while the view still shows nodes, fit when it shows
// none.  Used by the 2D graph (graph.js) and the 3D view (graph3d.js).

/**
 * How many of the points lie inside the viewport.  A point counts when it
 * is inside the view inset by `margin` px (default 6% of the smaller side),
 * so a node cut off at the edge does not count as "still on screen".
 * `points`: [{x, y}] in screen pixels; entries that are not finite count as
 * off screen.
 */
export function pointsOnScreen(points, width, height, margin = null) {
  if (!(width > 0) || !(height > 0)) return 0;
  const inset = margin ?? Math.min(width, height) * 0.06;
  let count = 0;
  for (const point of points || []) {
    if (!point || !Number.isFinite(point.x) || !Number.isFinite(point.y)) continue;
    if (point.x >= inset && point.x <= width - inset && point.y >= inset && point.y <= height - inset) count += 1;
  }
  return count;
}

/**
 * "empty": no nodes (the empty state shows; the camera stays).
 * "keep": at least one node is on screen.
 * "fit": nodes exist but none is on screen.
 */
export function viewportAction({ total, onScreen }) {
  if (!total) return "empty";
  return onScreen > 0 ? "keep" : "fit";
}

/**
 * The shift that keeps the user's place when a layout is computed afresh
 * for a new node set (the timeline graph re-centres on its new bounding
 * box): the node that was nearest the view centre and is still shown keeps
 * its old world position.  `before`/`after`: Map(id -> {x, y}); `centre`:
 * the view centre in world coordinates.  Null when no node is in both.
 */
export function anchorShift(before, after, centre) {
  let best = null, bestDistance = Infinity;
  for (const [id, old] of before || []) {
    const next = after?.get(id);
    if (!next || !Number.isFinite(old?.x) || !Number.isFinite(old?.y) || !Number.isFinite(next.x) || !Number.isFinite(next.y)) continue;
    const distance = centre ? Math.hypot(old.x - centre.x, old.y - centre.y) : 0;
    if (distance < bestDistance) { bestDistance = distance; best = { dx: old.x - next.x, dy: old.y - next.y }; }
  }
  return best;
}
