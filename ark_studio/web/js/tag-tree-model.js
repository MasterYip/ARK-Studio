// Hierarchical tag toggles (tag-tree.js): line tags are paths
// ("sandbox/infra"), shown as a tree whose rows each switch on and off.
// A parent row switches its whole subtree; it is "mixed" while only part of
// it is on.  Pure and dependency-free: the filter rail (components.js) and
// Needs you's line filter (needs-ui.js) keep their own filter state and use
// these helpers to read and change it.

/**
 * The tree of `entries` ([{ path, n }]).  `n` is the count of the path
 * itself; with `rolledUp` it already includes the paths below it.  Paths
 * above an entry that are not entries themselves, and entries marked
 * `own: false`, become rows that only switch the subtree below them.
 * Nodes: { path, label (last part), depth, own, n, total, children }.
 * Siblings: largest total first (`order: "count"`), by name ("name") or
 * as given ("given").
 */
export function buildTagTree(entries = [], { rolledUp = false, order = "count" } = {}) {
  const nodes = new Map();
  const roots = [];
  const node = (path) => {
    let found = nodes.get(path);
    if (found) return found;
    const parts = path.split("/");
    found = { path, label: parts.at(-1), depth: parts.length - 1, own: false, given: false, n: 0, total: 0, children: [] };
    nodes.set(path, found);
    const parent = parts.length > 1 ? node(parts.slice(0, -1).join("/")) : null;
    (parent ? parent.children : roots).push(found);
    return found;
  };
  for (const entry of entries) {
    const path = String(entry?.path ?? "");
    if (!path || path.split("/").some((part) => part === "")) continue;
    const item = node(path);
    // own: false is a row that only groups the paths below it.
    if (entry.own !== false) item.own = true;
    item.given = true;
    item.n += Number(entry.n) || 0;
  }
  const total = (item) => {
    const below = item.children.reduce((sum, child) => sum + total(child), 0);
    item.total = rolledUp && item.given ? item.n : item.n + below;
    return item.total;
  };
  roots.forEach(total);
  if (order !== "given") {
    const sort = (list) => {
      list.sort((a, b) => (order === "count" ? b.total - a.total : 0) || a.label.localeCompare(b.label));
      list.forEach((item) => sort(item.children));
    };
    sort(roots);
  }
  return roots;
}

/** Every path a row stands for: its own and the own paths below it. */
export function subtreePaths(item) {
  const out = [];
  const walk = (n) => {
    if (n.own) out.push(n.path);
    n.children.forEach(walk);
  };
  walk(item);
  return out;
}

/** "on", "off" or "mixed" (part of the subtree on); `isOn(path)`. */
export function nodeState(item, isOn) {
  const paths = subtreePaths(item);
  const on = paths.filter((path) => isOn(path)).length;
  return on === 0 ? "off" : on === paths.length ? "on" : "mixed";
}

/**
 * The paths a click on the row changes and whether they switch on: a row
 * that is fully on switches its subtree off, otherwise on.
 */
export function toggleTarget(item, isOn) {
  return { paths: subtreePaths(item), on: nodeState(item, isOn) !== "on" };
}

/** `selected` (a list) after switching `paths` on or off; order kept. */
export function applyToggle(selected = [], paths = [], on = true) {
  const set = new Set(paths);
  const kept = selected.filter((value) => !set.has(value));
  return on ? [...kept, ...paths.filter((path) => !kept.includes(path))] : kept;
}

/** Rows to draw, top to bottom; children of folded rows are left out. */
export function visibleRows(roots = [], folded = new Set()) {
  const out = [];
  const walk = (item) => {
    out.push(item);
    if (!folded.has(item.path)) item.children.forEach(walk);
  };
  roots.forEach(walk);
  return out;
}

/** Every own path of the tree. */
export function treePaths(roots = []) {
  return roots.flatMap(subtreePaths);
}

// Fold state per tree ("rail:<project>:branch", "needs:<project>"), kept in
// localStorage as the folded paths.
export const FOLD_PREFIX = "ark.tagTreeFold.";

function storageOrNull(storage) {
  if (storage !== undefined) return storage;
  try { return globalThis.localStorage || null; } catch { return null; }
}

/** The folded paths stored under `key`, or null when none were stored. */
export function loadFolded(key, { storage } = {}) {
  try {
    const raw = storageOrNull(storage)?.getItem(FOLD_PREFIX + key);
    if (raw == null) return null;
    const list = JSON.parse(raw);
    return new Set(Array.isArray(list) ? list.map(String) : []);
  } catch { return null; }
}

export function saveFolded(key, folded, { storage } = {}) {
  try { storageOrNull(storage)?.setItem(FOLD_PREFIX + key, JSON.stringify([...folded].sort())); } catch { /* private browsing */ }
}

/**
 * The fold state to draw: the stored one, else every parent folded when the
 * unfolded tree would be longer than `maxRows` rows, else nothing folded.
 */
export function initialFolded(roots = [], stored = null, { maxRows = 16 } = {}) {
  if (stored) return stored;
  if (visibleRows(roots).length <= maxRows) return new Set();
  const parents = new Set();
  const walk = (item) => {
    if (item.children.length) parents.add(item.path);
    item.children.forEach(walk);
  };
  roots.forEach(walk);
  return parents;
}

/** `folded` with `path` folded or unfolded (a new Set). */
export function toggleFold(folded, path) {
  const next = new Set(folded);
  if (next.has(path)) next.delete(path); else next.add(path);
  return next;
}
