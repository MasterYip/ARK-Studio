// Research › Topics explorer (right side of the topic page): stacked sections
// for Topics, Lines and the open topic's Outline, like the Editor's Explorer.
// This module keeps the trees, the search filter, the fold commands and the
// panel's stored state; pure and DOM-free (tests/js/topic_explorer_model.test.mjs).
import { sectionsModel } from "./explorer-sections-model.js";

export const EXPLORER_KEY = "ark.topic.explorer.v1";
export const EXPLORER_WIDTH = { min: 200, max: 560, default: 280 };

export const sections = sectionsModel({
  ids: ["topics", "lines", "outline"],
  sizes: { topics: 260, lines: 180, outline: 320 },
  collapsed: { topics: false, lines: false, outline: false },
});

/** The panel's stored state: shown, width and the sections. */
export function parseExplorer(text) {
  let raw = null;
  try { raw = JSON.parse(text || "null"); } catch { raw = null; }
  const src = raw && typeof raw === "object" ? raw : {};
  const width = Number(src.width);
  return {
    open: src.open !== false,
    width: Number.isFinite(width) ? clampWidth(width) : EXPLORER_WIDTH.default,
    sections: sections.normalizeSections(src.sections),
    folded: {
      topics: stringList(src.folded?.topics),
      lines: stringList(src.folded?.lines),
    },
  };
}

function stringList(value) {
  return Array.isArray(value) ? value.filter((item) => typeof item === "string").slice(0, 500) : [];
}

export function clampWidth(value) {
  const width = Math.round(Number(value) || EXPLORER_WIDTH.default);
  return Math.max(EXPLORER_WIDTH.min, Math.min(EXPLORER_WIDTH.max, width));
}

// ------------------------------------------------------------------- trees
// A tree is a flat pre-order list of rows { key, depth, parent, children }.

/**
 * Topics nested by part_of: a topic whose parents include another listed
 * topic sits under the first such parent.  Built-in topics stay first; the
 * order of the list is kept among siblings.  Cycles fall back to top level.
 */
export function topicTree(topics = []) {
  const list = (Array.isArray(topics) ? topics : []).filter((topic) => topic?.uid);
  const byUid = new Map(list.map((topic) => [topic.uid, topic]));
  const parentOf = new Map();
  for (const topic of list) {
    const parent = (topic.parents || []).map((item) => item?.uid).find((uid) => uid && uid !== topic.uid && byUid.has(uid));
    if (parent) parentOf.set(topic.uid, parent);
  }
  // Break cycles: a topic whose parent chain comes back to it is top level.
  for (const topic of list) {
    const seen = new Set([topic.uid]);
    for (let up = parentOf.get(topic.uid); up; up = parentOf.get(up)) {
      if (seen.has(up)) { parentOf.delete(topic.uid); break; }
      seen.add(up);
    }
  }
  const kids = new Map();
  for (const topic of list) {
    const parent = parentOf.get(topic.uid) || "";
    if (!kids.has(parent)) kids.set(parent, []);
    kids.get(parent).push(topic);
  }
  const rows = [];
  const walk = (parent, depth) => {
    for (const topic of kids.get(parent) || []) {
      rows.push({ key: topic.uid, depth, parent: parent || null, children: (kids.get(topic.uid) || []).length, topic });
      walk(topic.uid, depth + 1);
    }
  };
  walk("", 0);
  return rows;
}

/**
 * Lines of work as a tree by their path ("a/b/c" under "a/b" under "a").  A
 * path that is only the parent of other lines still gets a row (line: null).
 * Rows are sorted by name; `count` adds up the line's nodes and those of the
 * lines below it (what opening the line shows).
 */
export function lineTree(lines = []) {
  const list = (Array.isArray(lines) ? lines : []).filter((line) => line?.name);
  const byName = new Map();
  for (const line of list) if (!byName.has(line.name)) byName.set(line.name, line);
  const names = new Set();
  for (const name of byName.keys()) {
    const parts = name.split("/");
    for (let i = 1; i <= parts.length; i += 1) names.add(parts.slice(0, i).join("/"));
  }
  const sorted = [...names].sort((a, b) => a.localeCompare(b));
  const childCount = new Map();
  for (const name of sorted) {
    const parent = name.includes("/") ? name.slice(0, name.lastIndexOf("/")) : "";
    if (parent) childCount.set(parent, (childCount.get(parent) || 0) + 1);
  }
  // Pre-order: sorting by path segments keeps children right after their parent.
  sorted.sort((a, b) => {
    const x = a.split("/"), y = b.split("/");
    for (let i = 0; i < Math.min(x.length, y.length); i += 1) {
      if (x[i] !== y[i]) return x[i].localeCompare(y[i]);
    }
    return x.length - y.length;
  });
  const total = (name) => {
    let sum = 0;
    for (const [other, line] of byName) {
      if (other === name || other.startsWith(`${name}/`)) sum += Number(line.units) || 0;
    }
    return sum;
  };
  return sorted.map((name) => {
    const line = byName.get(name) || null;
    const parent = name.includes("/") ? name.slice(0, name.lastIndexOf("/")) : null;
    return {
      key: name, depth: name.split("/").length - 1, parent, children: childCount.get(name) || 0,
      label: name.split("/").at(-1), line, count: total(name),
    };
  });
}

/** Rows shown under the fold state: descendants of a folded row are hidden. */
export function visibleRows(rows = [], folded = new Set()) {
  const out = [];
  let hideBelow = Infinity;
  for (const row of rows) {
    if (row.depth > hideBelow) continue;
    hideBelow = Infinity;
    out.push(row);
    if (folded.has(row.key) && row.children) hideBelow = row.depth;
  }
  return out;
}

/**
 * Rows whose text matches `query` (case-insensitive, every word), with the
 * rows above them so the tree still reads.  An empty query keeps every row.
 */
export function searchRows(rows = [], query = "", textOf = (row) => row.key) {
  const words = String(query || "").toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return rows;
  const byKey = new Map(rows.map((row) => [row.key, row]));
  const keep = new Set();
  for (const row of rows) {
    const text = String(textOf(row) || "").toLowerCase();
    if (!words.every((word) => text.includes(word))) continue;
    for (let at = row; at && !keep.has(at.key); at = byKey.get(at.parent)) keep.add(at.key);
  }
  return rows.filter((row) => keep.has(row.key));
}

/** The keys from the root down to `key` (to unfold the way to a row). */
export function ancestorKeys(rows = [], key) {
  const byKey = new Map(rows.map((row) => [row.key, row]));
  const out = [];
  for (let at = byKey.get(byKey.get(key)?.parent); at; at = byKey.get(at.parent)) out.unshift(at.key);
  return out;
}

// ----------------------------------------------------------------- outline
/** A topic's members as outline rows (members arrive in pre-order with member.depth/parent). */
export function outlineRows(members = []) {
  const list = Array.isArray(members) ? members : [];
  return list.map((unit, index) => {
    const depth = unit.member?.depth || 0;
    let children = 0;
    for (let next = index + 1; next < list.length; next += 1) {
      const d = list[next].member?.depth || 0;
      if (d <= depth) break;
      if (d === depth + 1) children += 1;
    }
    return { key: unit.id, depth, parent: unit.member?.parent ?? null, children, unit };
  });
}

/**
 * One fold command over outline rows (the Ctrl+K chords, the buttons):
 * returns the new folded set.  `foldable` limits which keys can be folded;
 * `focus` is the row the per-row commands act on.
 */
export function foldCommand(rows = [], folded = new Set(), command = {}, { foldable = null, focus = null } = {}) {
  const can = foldable || new Set(rows.filter((row) => row.children).map((row) => row.key));
  let next = new Set(folded);
  const subtree = (key) => {
    const at = rows.findIndex((row) => row.key === key);
    if (at < 0) return [];
    const out = [key];
    for (let i = at + 1; i < rows.length && rows[i].depth > rows[at].depth; i += 1) out.push(rows[i].key);
    return out;
  };
  switch (command.cmd) {
    case "unfoldAll": next = new Set(); break;
    case "foldAll": next = new Set(can); break;
    case "foldLevel": for (const row of rows) if (row.depth === (command.level || 1) - 1) next.add(row.key); break;
    case "showLevel":
      next = new Set(rows.filter((row) => row.depth >= (command.level || 1) - 1 && can.has(row.key)).map((row) => row.key));
      break;
    case "foldRecursive": for (const key of subtree(focus)) next.add(key); break;
    case "unfoldRecursive": for (const key of subtree(focus)) next.delete(key); break;
    case "fold": if (focus != null) next.add(focus); break;
    case "unfold": if (focus != null) next.delete(focus); break;
    case "toggle":
      if (focus != null) { if (next.has(focus)) next.delete(focus); else next.add(focus); }
      break;
    default: return next;
  }
  return new Set([...next].filter((key) => can.has(key)));
}
