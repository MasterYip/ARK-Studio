// Source <-> preview scroll sync (VS Code Markdown style): pure mapping
// between a 1-based (fractional) source line and a scroll position, by
// linear interpolation between anchors { line, top }.  On the preview side
// the anchors are blocks carrying data-source-line; on the source side they
// are the measured tops of the source lines.  DOM-free so
// tests/js/scroll_sync_model.test.mjs can import it.

// Sorted by line, one anchor per line (the first/lowest), tops made
// non-decreasing (a float or a collapsed block cannot move the map back).
// `end` ({ line, top }) closes the map after the last anchor (e.g. the
// line after the last one at the content's height).
export function normalizeAnchors(list, end = null) {
  const byLine = new Map();
  for (const item of list || []) {
    const line = Number(item?.line), top = Number(item?.top);
    if (!Number.isFinite(line) || !Number.isFinite(top) || line < 1) continue;
    if (!byLine.has(line) || top < byLine.get(line)) byLine.set(line, top);
  }
  const out = [...byLine].sort((a, b) => a[0] - b[0]).map(([line, top]) => ({ line, top }));
  if (!out.length || out[0].line > 1) out.unshift({ line: 1, top: 0 });
  if (end && Number.isFinite(end.line) && Number.isFinite(end.top) && end.line > out[out.length - 1].line) {
    out.push({ line: end.line, top: end.top });
  }
  let floor = -Infinity;
  for (const item of out) { item.top = Math.max(item.top, floor); floor = item.top; }
  return out;
}

// Scroll position of (fractional) `line`.
export function positionForLine(anchors, line) {
  if (!anchors?.length) return 0;
  const value = Math.max(1, Number(line) || 1);
  let i = 0;
  while (i + 1 < anchors.length && anchors[i + 1].line <= value) i += 1;
  const a = anchors[i], b = anchors[i + 1];
  if (!b) return a.top;
  return a.top + ((value - a.line) / (b.line - a.line)) * (b.top - a.top);
}

// (Fractional) source line at scroll position `top`.
export function lineForPosition(anchors, top) {
  if (!anchors?.length) return 1;
  const value = Number(top) || 0;
  if (value <= anchors[0].top) return anchors[0].line;
  let i = 0;
  while (i + 1 < anchors.length && anchors[i + 1].top <= value) i += 1;
  const a = anchors[i], b = anchors[i + 1];
  if (!b || b.top === a.top) return a.line;
  return a.line + ((value - a.top) / (b.top - a.top)) * (b.line - a.line);
}

// The anchored block for the caret on `line`: the last anchor at or before
// it (null before the first block).
export function blockLineFor(lines, line) {
  let best = null;
  for (const value of lines || []) if (value <= line && (best === null || value > best)) best = value;
  return best;
}

// Tops of wrapped source lines from the heights of each line's box
// (`heights[i]` for line i + 1), starting at `offset` (the padding).
export function lineTops(heights, offset = 0) {
  const tops = [];
  let y = offset;
  for (const height of heights) { tops.push(y); y += Math.max(0, Number(height) || 0); }
  return { tops, end: y };
}
