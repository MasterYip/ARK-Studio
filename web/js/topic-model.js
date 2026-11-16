// Pure helpers for topic units (a node that holds a curated subgraph).
// DOM-free (tests/js/topic_model.test.mjs).
import { GraphFilterIndex } from "./graph-filter.js";

/** Filter a topic's members, preserving narrative order and the saved design. */
export function filterTopic(data, query = {}, me = "") {
  const index = new GraphFilterIndex({ nodes: data.members || [], edges: data.edges || [] });
  const result = index.filter(query, me, { all: true });
  const selected = new Map(result.nodes.map(node => [node.id, node]));
  const all = new Map((data.members || []).map(node => [node.id, node]));
  const shown = new Map();
  const members = (data.members || []).filter(node => selected.has(node.id)).map(node => {
    let parent = node.member?.parent;
    while (parent != null && !shown.has(parent)) parent = all.get(parent)?.member?.parent;
    const member = { ...node.member, parent: parent ?? null,
      depth: parent == null ? 0 : shown.get(parent).member.depth + 1, children: 0 };
    const display = { ...selected.get(node.id), member };
    shown.set(node.id, display);
    if (parent != null) shown.get(parent).member.children++;
    return display;
  });
  // Each facet ignores its own filter, using only this topic's members.
  const candidates = (...keys) => index.filter({ ...query, context: 0,
    ...Object.fromEntries(keys.map(key => [key, undefined])) }, me, { all: true }).nodes;
  const counts = (nodes, field) => {
    const out = Object.create(null);
    for (const node of nodes) if (node[field]) out[node[field]] = (out[node[field]] || 0) + 1;
    return { ...out };
  };
  const tags = new Map(), namespaces = Object.create(null), unset = Object.create(null);
  const tagged = candidates("tags", "tags_unset");
  for (const node of tagged) for (const tag of node.tags || []) {
    if (/^(type|status|importance):/.test(tag)) continue;
    tags.set(tag, (tags.get(tag) || 0) + 1);
  }
  for (const [tag, count] of tags) {
    const at = tag.indexOf(":");
    const namespace = at < 0 ? "tag" : tag.slice(0, at), value = at < 0 ? tag : tag.slice(at + 1);
    (namespaces[namespace] ||= []).push({ tag, namespace, value, count });
  }
  for (const [ns, values] of Object.entries(namespaces)) {
    values.sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
    unset[ns] = tagged.filter(node => !(node.tags || []).some(tag => ns === "tag" ? !tag.includes(":") : tag.startsWith(ns + ":"))).length;
  }
  const assigned = candidates("assignee", "assignees");
  const dates = (data.members || []).flatMap(node => [node.start_at, node.end_at]).filter(Boolean).sort();
  const facets = { types: counts(candidates("types", "types_none", "origins"), "type"),
    statuses: counts(candidates("statuses"), "status"), importance: counts(candidates("importance"), "importance"),
    namespaces: { ...namespaces }, unset: { ...unset }, assignees: counts(assigned, "assignee_effective"),
    unassigned: assigned.filter(node => !node.assignee_effective && !node.assignee).length,
    time: { min: dates[0] || null, max: dates.at(-1) || null,
      undated: (data.members || []).filter(node => !node.start_at && !node.end_at).length } };
  return { ...data, members, edges: result.edges, facets, filtered: members.length < (data.members || []).length };
}

/** Layout key GraphView uses for a node in the "node" layout. */
export function layoutKey(unit) {
  return unit?.uid || String(unit?.id ?? "");
}

/** Saved member positions as the {positions, bends} state GraphView loads. */
export function layoutFromMembers(members = []) {
  const positions = {};
  const widths = {};
  for (const unit of members || []) {
    const x = unit?.member?.x;
    const y = unit?.member?.y;
    if (Number.isFinite(x) && Number.isFinite(y)) positions[layoutKey(unit)] = { x, y };
    // Node-graph card width the user dragged (absent = automatic).
    if (Number.isFinite(unit?.member?.w)) widths[layoutKey(unit)] = unit.member.w;
  }
  return { positions, bends: {}, widths };
}

/**
 * Members whose position or card width differs from what the server has, as
 * `[{unit, x, y}]` / `[{unit, w}]` (or both) for PATCH
 * /api/units/{topic}/members.  Positions are compared at 0.5 px so float
 * noise does not cause writes; `w: null` returns a card to its automatic
 * width.  States without `widths` (older callers) never touch widths.
 */
export function layoutChanges(members = [], state = {}) {
  const positions = state?.positions || {};
  const widths = state?.widths && typeof state.widths === "object" ? state.widths : null;
  const out = [];
  for (const unit of members || []) {
    const key = layoutKey(unit);
    const item = {};
    const next = positions[key];
    if (next && Number.isFinite(next.x) && Number.isFinite(next.y)) {
      const x = Math.round(next.x * 100) / 100;
      const y = Math.round(next.y * 100) / 100;
      const prevX = unit?.member?.x;
      const prevY = unit?.member?.y;
      if (!(Number.isFinite(prevX) && Number.isFinite(prevY)
          && Math.abs(prevX - x) < 0.5 && Math.abs(prevY - y) < 0.5)) Object.assign(item, { x, y });
    }
    if (widths && key in widths) {
      const raw = widths[key];
      const w = Number.isFinite(raw) ? Math.round(raw * 10) / 10 : null;
      const prev = Number.isFinite(unit?.member?.w) ? unit.member.w : null;
      if (w === null ? prev !== null : prev === null || Math.abs(prev - w) >= 0.5) item.w = w;
    }
    if (Object.keys(item).length) out.push({ unit: unit.id, ...item });
  }
  return out;
}

/**
 * A topic rule from the filter rail's state.  `baseTypes` are the types the
 * current tab shows when no type is selected, so "save as topic" keeps the
 * scope the user was looking at.  Returns null when nothing restricts.
 */
export function ruleFromFilters(filters = {}, baseTypes = []) {
  const rule = {};
  const q = String(filters.q || "").trim();
  if (q) rule.q = q;
  const types = filters.typesNone ? [] : (filters.types?.length ? filters.types : baseTypes);
  if (types?.length) rule.types = [...types];
  if (!filters.statusesNone && filters.statuses?.length) rule.statuses = [...filters.statuses];
  if (!filters.importanceNone && filters.importanceLevels?.length) rule.importance = [...filters.importanceLevels];
  if (filters.tags?.length) rule.tags = [...filters.tags];
  if (filters.tagsUnset?.length) rule.tags_unset = [...filters.tagsUnset];
  if (filters.assignees?.length) rule.assignees = [...filters.assignees];
  if (filters.roots) rule.roots = true;
  if (filters.timeMode === "timeline") {
    if (filters.from) rule.start_from = filters.from;
    if (filters.to) rule.start_to = filters.to;
  }
  return Object.keys(rule).length ? rule : null;
}

/** Rule as short `[label-key, value]` pairs for chips. */
export function ruleParts(rule) {
  if (!rule) return [];
  const parts = [];
  if (rule.q) parts.push(["topic.rule.q", rule.q]);
  for (const key of ["types", "statuses", "importance", "tags", "tags_unset", "assignees"]) {
    if (rule[key]?.length) parts.push([`topic.rule.${key}`, rule[key].join(", ")]);
  }
  if (rule.roots) parts.push(["topic.rule.roots", ""]);
  if (rule.start_from || rule.start_to) parts.push(["topic.rule.dates", `${rule.start_from || "…"} → ${rule.start_to || "…"}`]);
  for (const key of ["created_since", "updated_since"]) {
    if (rule[key]) parts.push([`topic.rule.${key}`, String(rule[key]).slice(0, 10)]);
  }
  return parts;
}

/** Narrative order after moving `id` by `delta` (-1 up, +1 down). */
export function moveInOrder(ids = [], id, delta) {
  const list = [...ids];
  const index = list.findIndex((value) => String(value) === String(id));
  const next = index + delta;
  if (index < 0 || next < 0 || next >= list.length) return list;
  [list[index], list[next]] = [list[next], list[index]];
  return list;
}

/** Edges the canvas should draw: hidden ones only while editing links. */
export function visibleEdges(edges = [], { showHidden = false } = {}) {
  return (edges || []).filter((edge) => showHidden || !edge.hidden);
}

/**
 * Normalized points for a topic card thumbnail (0..1 in both axes).  Saved
 * positions are used when every member has one; otherwise members sit on a
 * circle in narrative order.
 */
export function previewPoints(members = []) {
  const list = (members || []).slice(0, 60);
  if (!list.length) return [];
  const placed = list.every((m) => Number.isFinite(m.x) && Number.isFinite(m.y));
  if (placed && list.length > 1) {
    const xs = list.map((m) => m.x), ys = list.map((m) => m.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs);
    const minY = Math.min(...ys), maxY = Math.max(...ys);
    const span = Math.max(maxX - minX, maxY - minY, 1);
    const offX = (1 - (maxX - minX) / span) / 2, offY = (1 - (maxY - minY) / span) / 2;
    return list.map((m) => ({ id: m.id, type: m.type, x: offX + (m.x - minX) / span, y: offY + (m.y - minY) / span }));
  }
  if (list.length === 1) return [{ id: list[0].id, type: list[0].type, x: 0.5, y: 0.5 }];
  return list.map((m, index) => {
    const angle = -Math.PI / 2 + (index / list.length) * Math.PI * 2;
    return { id: m.id, type: m.type, x: 0.5 + Math.cos(angle) * 0.42, y: 0.5 + Math.sin(angle) * 0.42 };
  });
}

// ------------------------------------------------------------------ outline
// Members arrive in outline pre-order with member.{depth, parent}.  The
// helpers below edit that outline and compute VS Code style folding.

function outlineIndex(members = []) {
  const list = (members || []).map((unit) => ({
    id: unit.id,
    parent: unit.member?.parent ?? null,
    depth: unit.member?.depth ?? 0,
  }));
  const at = new Map(list.map((item, index) => [item.id, index]));
  // End (exclusive) of each item's subtree in pre-order.
  const end = list.map((item, index) => {
    let next = index + 1;
    while (next < list.length && list[next].depth > item.depth) next += 1;
    return next;
  });
  return { list, at, end };
}

function siblings(index, id) {
  const item = index.list[index.at.get(id)];
  return index.list.filter((other) => other.parent === item.parent).map((other) => other.id);
}

/**
 * Move one outline item: "up" / "down" among its siblings, "indent" under the
 * previous sibling, "outdent" to just after its parent.  Returns the PATCH
 * body `{order, items: [{unit, parent}]}` (parent null = top level), or null
 * when the move is not possible.
 */
export function outlineMove(members = [], id, op) {
  const index = outlineIndex(members);
  const position = index.at.get(id);
  if (position === undefined) return null;
  const item = index.list[position];
  const ids = index.list.map((entry) => entry.id);
  const block = (i) => ids.slice(i, index.end[i]);
  const peers = siblings(index, id);
  const k = peers.indexOf(id);
  if (op === "up" || op === "down") {
    const other = peers[op === "up" ? k - 1 : k + 1];
    if (other === undefined) return null;
    const [first, second] = op === "up" ? [index.at.get(other), position] : [position, index.at.get(other)];
    const order = [...ids.slice(0, first), ...block(second), ...block(first), ...ids.slice(index.end[second])];
    return { order, items: [] };
  }
  if (op === "indent") {
    const previous = peers[k - 1];
    if (previous === undefined) return null;
    return { order: ids, items: [{ unit: id, parent: previous }] };
  }
  if (op === "outdent") {
    if (item.parent == null) return null;
    const parentAt = index.at.get(item.parent);
    const grand = index.list[parentAt].parent ?? null;
    const mine = block(position);
    const rest = ids.filter((value) => !mine.includes(value));
    // After the rest of the parent's subtree.
    const parentBlock = block(parentAt).filter((value) => !mine.includes(value));
    const insertAt = rest.indexOf(parentBlock[parentBlock.length - 1]) + 1;
    const order = [...rest.slice(0, insertAt), ...mine, ...rest.slice(insertAt)];
    return { order, items: [{ unit: id, parent: grand }] };
  }
  return null;
}

/** Ids that have content to fold (a body, media or children). */
export function foldable(members = []) {
  const out = new Set();
  for (const unit of members || []) {
    if ((unit.member?.children || 0) > 0 || String(unit.body ?? unit.excerpt ?? "").trim() || (unit.media || []).length
        || unit.type === "topic") out.add(unit.id);
  }
  return out;
}

/** VS Code "Fold Level N": fold every item at level N (1 = top), keep the rest. */
export function foldLevel(members = [], folded = new Set(), level = 1) {
  const next = new Set(folded);
  for (const unit of members || []) {
    if ((unit.member?.depth ?? 0) === level - 1) next.add(unit.id);
  }
  return next;
}

/** The item and every descendant (fold/unfold recursively). */
export function subtreeIds(members = [], id) {
  const index = outlineIndex(members);
  const position = index.at.get(id);
  if (position === undefined) return [];
  return index.list.slice(position, index.end[position]).map((item) => item.id);
}

/** Items visible under the fold state (descendants of folded items are hidden). */
export function visibleOutline(members = [], folded = new Set()) {
  const out = [];
  let hideBelow = Infinity;
  for (const unit of members || []) {
    const depth = unit.member?.depth ?? 0;
    if (depth > hideBelow) continue;
    hideBelow = Infinity;
    out.push(unit);
    if (folded.has(unit.id)) hideBelow = depth;
  }
  return out;
}

/**
 * Ctrl+K chords on the article view (VS Code bindings).  `second` is the key
 * pressed after Ctrl+K, with or without Ctrl.  Returns a command or null.
 */
export function foldChord(second = {}) {
  const key = String(second.key || "");
  const code = String(second.code || "");
  if (/^[jJ]$/.test(key) || code === "KeyJ") return { cmd: "unfoldAll" };
  if (key === "0" || code === "Digit0") return { cmd: "foldAll" };
  const digit = /^Digit([1-7])$/.exec(code)?.[1] || (/^[1-7]$/.test(key) ? key : "");
  if (digit) return { cmd: "foldLevel", level: Number(digit) };
  if (key === "[" || code === "BracketLeft") return { cmd: "foldRecursive" };
  if (key === "]" || code === "BracketRight") return { cmd: "unfoldRecursive" };
  if (/^[lL]$/.test(key) || code === "KeyL") return { cmd: "toggle" };
  return null;
}

/**
 * Drag an outline item onto another: "before" / "after" it as a sibling or
 * "inside" it as its last child.  Returns the PATCH body or null when the
 * drop changes nothing or would put an item inside itself.
 */
export function outlineDrop(members = [], dragId, targetId, position) {
  const index = outlineIndex(members);
  const from = index.at.get(dragId);
  const to = index.at.get(targetId);
  if (from === undefined || to === undefined || dragId === targetId) return null;
  const ids = index.list.map((entry) => entry.id);
  const block = ids.slice(from, index.end[from]);
  if (block.includes(targetId)) return null;
  const rest = ids.filter((id) => !block.includes(id));
  const target = index.list[to];
  let at;
  let parent;
  if (position === "before") {
    at = rest.indexOf(targetId);
    parent = target.parent ?? null;
  } else {
    const tail = ids.slice(to, index.end[to]).filter((id) => !block.includes(id));
    at = rest.indexOf(tail[tail.length - 1]) + 1;
    parent = position === "inside" ? targetId : (target.parent ?? null);
  }
  const order = [...rest.slice(0, at), ...block, ...rest.slice(at)];
  const unchanged = order.every((id, i) => id === ids[i]) && (index.list[from].parent ?? null) === parent;
  if (unchanged) return null;
  return { order, items: [{ unit: dragId, parent }] };
}

/** Where a pointer at `ratio` (0 top .. 1 bottom) of a row drops. */
export function dropPosition(ratio) {
  if (ratio < 0.28) return "before";
  if (ratio > 0.72) return "after";
  return "inside";
}
