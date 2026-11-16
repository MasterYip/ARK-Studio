// Editor breadcrumbs (VS Code style): pure helpers for the path segments and
// the dropdown's folder listing (inline tree, type-to-filter).  DOM-free so
// tests/js/breadcrumb_model.test.mjs can import it.

export function normalizePath(path) {
  return String(path || "").replace(/\\/g, "/").split("/").filter((part) => part && part !== ".").join("/");
}

export function parentOf(path) {
  const clean = normalizePath(path);
  const at = clean.lastIndexOf("/");
  return at < 0 ? "" : clean.slice(0, at);
}

// One segment per folder plus the file.  A folder segment lists that
// folder (highlighting the next segment); the file segment lists its
// siblings (highlighting the file).
export function crumbSegments(path) {
  const parts = normalizePath(path).split("/").filter(Boolean);
  return parts.map((label, index) => {
    const full = parts.slice(0, index + 1).join("/");
    const file = index === parts.length - 1;
    return {
      label, path: full, kind: file ? "file" : "dir",
      list: file ? parts.slice(0, index).join("/") : full,
      highlight: file ? full : parts.slice(0, index + 2).join("/"),
    };
  });
}

function matches(name, query) {
  return !query || String(name).toLowerCase().includes(query);
}

// Rows of the dropdown: depth-first through loaded folders.  `tree` maps a
// folder path to { entries, truncated, loading, error }; `expanded` holds
// the open folders.  With a filter, files must match; folders show when
// they match or contain a (loaded) match, and are walked even when closed.
export function dropdownRows(root, tree, expanded, filter = "") {
  const query = String(filter || "").trim().toLowerCase();
  const rows = [];
  const hasMatch = (dir, seen = new Set()) => {
    if (seen.has(dir)) return false;
    seen.add(dir);
    const node = tree.get(dir);
    return (node?.entries || []).some((entry) => matches(entry.name, query)
      || (entry.type === "dir" && hasMatch(entry.path, seen)));
  };
  const walk = (dir, depth) => {
    const node = tree.get(dir);
    if (!node) { rows.push({ kind: "pending", path: dir, depth }); return; }
    if (node.loading) { rows.push({ kind: "loading", path: dir, depth }); return; }
    if (node.error) { rows.push({ kind: "error", path: dir, depth, error: node.error }); return; }
    for (const entry of node.entries || []) {
      const isDir = entry.type === "dir";
      const open = expanded.has(entry.path) || (query && isDir && hasMatch(entry.path));
      if (query && !matches(entry.name, query) && !(isDir && hasMatch(entry.path))) continue;
      rows.push({ kind: isDir ? "dir" : "file", entry, path: entry.path, depth, open });
      if (isDir && open) walk(entry.path, depth + 1);
    }
    if (node.truncated) rows.push({ kind: "truncated", path: dir, depth });
  };
  walk(normalizePath(root), 0);
  return rows;
}

// Keyboard movement over selectable rows (dirs and files).
export function moveSelection(rows, index, delta) {
  const selectable = rows.map((row, i) => (row.kind === "dir" || row.kind === "file" ? i : -1)).filter((i) => i >= 0);
  if (!selectable.length) return -1;
  const at = selectable.indexOf(index);
  if (at < 0) return delta < 0 ? selectable[selectable.length - 1] : selectable[0];
  return selectable[Math.max(0, Math.min(selectable.length - 1, at + delta))];
}
