// Workbench Search (VS Code style): pure helpers for the query options,
// result grouping, highlighting and keyboard/F4 navigation.  DOM-free so
// tests/js/search_model.test.mjs can import it.

export const SEARCH_STORE_KEY = "ark.search.v1";

export function normalizeQuery(value = {}) {
  const src = value && typeof value === "object" ? value : {};
  const text = (key, max) => String(src[key] ?? "").slice(0, max);
  return {
    q: text("q", 500),
    case: Boolean(src.case),
    word: Boolean(src.word),
    regex: Boolean(src.regex),
    include: text("include", 400),
    exclude: text("exclude", 400),
    details: Boolean(src.details),
    replace: text("replace", 1000),
    replaceOpen: Boolean(src.replaceOpen),
  };
}

// Body of POST /api/workspace/replace (files only; nodes are not replaced).
export function replaceBody(query, extra = {}) {
  return {
    q: query.q, case: query.case, word: query.word, regex: query.regex,
    include: query.include, exclude: query.exclude,
    replace: query.replace, ...extra,
  };
}

// Query-string parameters for /api/search/nodes and /api/workspace/grep;
// null when there is nothing to search.
export function searchParams(query) {
  const q = normalizeQuery(query);
  if (!q.q.trim()) return null;
  const params = { q: q.q };
  for (const key of ["case", "word", "regex"]) if (q[key]) params[key] = 1;
  return { nodes: params, files: { ...params, include: q.include, exclude: q.exclude } };
}

// Client-side check mirroring the server: an invalid regex is reported
// before any request is sent.
export function queryError(query) {
  const q = normalizeQuery(query);
  if (!q.regex || !q.q) return "";
  try { new RegExp(q.q); return ""; } catch (err) { return String(err.message || err); }
}

// Groups in display order with counts.  `nodes` / `files` are the route
// payloads (either may be null when that half failed or was not run).
export function groupResults(nodes, files) {
  const groups = [];
  const nodeItems = (nodes?.nodes || []).map((node) => ({
    kind: "node", key: `node:${node.id}`, id: node.id, uid: node.uid, type: node.type, title: node.title,
    status: node.status, matches: node.matches || [],
  }));
  const fileItems = (files?.files || []).map((file) => ({
    kind: "file", key: `file:${file.path}`, path: file.path, title: file.name || file.path.split("/").pop(),
    matches: file.matches || [], capped: Boolean(file.capped),
  }));
  groups.push({ kind: "nodes", items: nodeItems, count: nodeItems.reduce((n, item) => n + item.matches.length, 0),
    truncated: Boolean(nodes?.truncated), error: nodes?.error || "" });
  groups.push({ kind: "files", items: fileItems, count: fileItems.reduce((n, item) => n + item.matches.length, 0),
    truncated: Boolean(files?.truncated), error: files?.error || "" });
  return groups;
}

export function resultSummary(groups) {
  const count = groups.reduce((n, group) => n + group.count, 0);
  const items = groups.reduce((n, group) => n + group.items.length, 0);
  return { count, items, truncated: groups.some((group) => group.truncated) };
}

// Visible rows (group headers, items, matches) given collapsed keys.
export function resultRows(groups, collapsed = new Set()) {
  const rows = [];
  for (const group of groups) {
    if (!group.items.length) continue;
    const groupKey = `group:${group.kind}`;
    const groupOpen = !collapsed.has(groupKey);
    rows.push({ type: "group", key: groupKey, group, open: groupOpen });
    if (!groupOpen) continue;
    for (const item of group.items) {
      const open = !collapsed.has(item.key);
      rows.push({ type: "item", key: item.key, item, open });
      if (!open) continue;
      item.matches.forEach((match, index) => rows.push({ type: "match", key: `${item.key}#${index}`, item, match }));
    }
  }
  return rows;
}

// F4 / Shift+F4: next/previous match row, wrapping; -1 when none.
export function nextMatch(rows, index, delta = 1) {
  const matches = rows.map((row, i) => (row.type === "match" ? i : -1)).filter((i) => i >= 0);
  if (!matches.length) return -1;
  if (delta > 0) return matches.find((i) => i > index) ?? matches[0];
  return [...matches].reverse().find((i) => i < index) ?? matches[matches.length - 1];
}

// Arrow keys: any row.
export function moveRow(rows, index, delta) {
  if (!rows.length) return -1;
  return Math.max(0, Math.min(rows.length - 1, (index < 0 ? (delta > 0 ? -1 : rows.length) : index) + delta));
}

// A result line is shown from a little before its first match (with "…"),
// so the match stays visible in a narrow sidebar.  Ranges are shifted.
export function matchPreview(text, ranges = [], lead = 14) {
  const line = String(text ?? "").replace(/	/g, " ");
  const first = ranges.length ? Math.min(...ranges.map((r) => r[0])) : 0;
  const leading = line.length - line.trimStart().length;
  let cut = Math.max(leading, first - lead);
  if (cut <= leading) return { text: line.slice(leading), ranges: ranges.map(([a, b]) => [a - leading, b - leading]) };
  // Start at a word boundary when one is close.
  const space = line.lastIndexOf(" ", cut);
  if (space > leading && space >= cut - 6) cut = space + 1;
  return { text: `…${line.slice(cut)}`, ranges: ranges.map(([a, b]) => [a - cut + 1, b - cut + 1]) };
}

// Streaming (stream=1): fold one NDJSON event into a route-shaped payload
// ({nodes: [...]} / {files: [...]} plus the summary flags), so groupResults
// works on partial and final results alike.  True when the payload changed.
export function emptyPayload(kind) {
  return { [kind]: [], partial: true };
}

export function applySearchEvent(payload, kind, event) {
  const one = kind === "files" ? "file" : "node";
  if (!payload || !event || typeof event !== "object") return false;
  if (event.type === one && event[one]) {
    payload[kind].push(event[one]);
    return true;
  }
  if (event.type === "done") {
    for (const key of ["total", "truncated", "timed_out", "visited", "skipped", "backend", "elapsed_ms", "cancelled"]) {
      if (key in event) payload[key] = event[key];
    }
    payload.partial = false;
    return true;
  }
  if (event.type === "error") {
    payload.error = String(event.message || "error");
    payload.partial = false;
    return true;
  }
  return false;
}

// Virtualised result list: fixed-height rows; only the rows in (or near)
// the viewport are in the DOM.
export const SEARCH_ROW_HEIGHT = 22;

export function visibleRange(scrollTop, viewport, count, rowHeight = SEARCH_ROW_HEIGHT, overscan = 10) {
  const top = Math.max(0, Number(scrollTop) || 0);
  const height = Math.max(0, Number(viewport) || 0);
  const start = Math.min(count, Math.max(0, Math.floor(top / rowHeight) - overscan));
  const end = Math.min(count, Math.ceil((top + height) / rowHeight) + overscan);
  return { start, end: Math.max(start, end) };
}

// scrollTop that brings row `index` fully into view; the current value when
// it already is.
export function scrollForRow(index, scrollTop, viewport, rowHeight = SEARCH_ROW_HEIGHT) {
  if (index < 0) return scrollTop;
  const top = index * rowHeight;
  if (top < scrollTop) return top;
  if (top + rowHeight > scrollTop + viewport) return Math.max(0, top + rowHeight - viewport);
  return scrollTop;
}

// Split a line into plain / highlighted pieces from [start, end] ranges.
export function highlightSegments(text, ranges = []) {
  const line = String(text ?? "");
  const out = [];
  let at = 0;
  const sorted = [...ranges].filter((r) => Array.isArray(r) && r[1] > r[0]).sort((a, b) => a[0] - b[0]);
  for (const [start, end] of sorted) {
    const s = Math.max(at, Math.min(start, line.length));
    const e = Math.max(s, Math.min(end, line.length));
    if (s > at) out.push({ text: line.slice(at, s), hit: false });
    if (e > s) out.push({ text: line.slice(s, e), hit: true });
    at = e;
  }
  if (at < line.length || !out.length) out.push({ text: line.slice(at), hit: false });
  return out;
}
