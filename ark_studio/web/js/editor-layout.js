// Editor groups (VS Code style split editor): pure, DOM-free layout model.
//
// Each group is an editor-model tab state ({ tabs, active, mru }) plus an id.
// Groups are arranged in a grid tree: a leaf is a group, an inner node splits
// its children in a row (side by side) or a column (stacked) with relative
// sizes.  The same document may be open in several groups; dirty flags and
// titles are kept in step across groups (the buffer itself lives in the view
// layer, keyed by tab key, so there is only ever one copy).  Every operation
// returns a new layout; unchanged groups keep their object identity so the
// view can repaint only what changed.
//
// Shape: { v: 2, groups: [{ id, tabs, active, mru }], focused: id,
//          tree: Node, seq: n }
// Node:  { type: "group", id } | { type: "split", id, dir: "row" | "column",
//          children: Node[], sizes: number[] (sum 1) }

import {
  createState, normalizeState, openTab, closeTabs, setDirty, renameTab, moveTab as moveTabInState,
  findTab, activateTab, normalizeRecent,
} from "./editor-model.js";

export const MAX_GROUPS = 4;
export const LAYOUT_VERSION = 2;
export const SIDES = ["left", "right", "up", "down"];

const clone = (value) => JSON.parse(JSON.stringify(value));

function asGroup(id, state) {
  return { id, tabs: state.tabs, active: state.active, mru: state.mru };
}

export function createLayout(state = createState()) {
  return { v: LAYOUT_VERSION, groups: [asGroup("g1", normalizeState(state))], focused: "g1", tree: { type: "group", id: "g1" }, seq: 1 };
}

export function getGroup(layout, id = layout?.focused) {
  return (layout?.groups || []).find((group) => group.id === id) || null;
}

// Groups in reading order (left-to-right, top-to-bottom through the tree).
export function groupOrder(layout) {
  const out = [];
  const walk = (node) => {
    if (!node) return;
    if (node.type === "group") out.push(node.id);
    else node.children.forEach(walk);
  };
  walk(layout?.tree);
  return out;
}

export function focusGroup(layout, id) {
  if (!getGroup(layout, id) || layout.focused === id) return layout;
  return { ...layout, focused: id };
}

// Apply an editor-model operation to one group.  fn(groupState) -> state.
export function updateGroup(layout, id, fn) {
  const group = getGroup(layout, id);
  if (!group) return layout;
  const next = fn(group);
  if (!next || next === group) return layout;
  return { ...layout, groups: layout.groups.map((item) => (item.id === id ? asGroup(id, next) : item)) };
}

// -------------------------------------------------------------- the tree
function findPath(node, id, path = []) {
  if (!node) return null;
  if (node.type === "group") return node.id === id ? path : null;
  for (let index = 0; index < node.children.length; index += 1) {
    const found = findPath(node.children[index], id, [...path, index]);
    if (found) return found;
  }
  return null;
}

function nodeAt(tree, path) {
  let node = tree;
  for (const index of path) node = node.children[index];
  return node;
}

function findSplit(node, id) {
  if (!node || node.type !== "split") return null;
  if (node.id === id) return node;
  for (const child of node.children) {
    const found = findSplit(child, id);
    if (found) return found;
  }
  return null;
}

function normalizeSizes(sizes) {
  const clean = sizes.map((value) => (Number.isFinite(Number(value)) && Number(value) > 0 ? Number(value) : 1));
  const total = clean.reduce((sum, value) => sum + value, 0) || 1;
  return clean.map((value) => value / total);
}

export function splitDirection(side) {
  return side === "left" || side === "right" ? "row" : "column";
}

// Open a new group next to `targetId`.  With `key`, the tab (from `fromId`)
// is opened in the new group; `move` removes it from its old group (which
// collapses when emptied).  Returns the layout with the new group focused.
export function splitGroup(layout, targetId, side = "right", { key = "", fromId = targetId, move = false } = {}) {
  if (!SIDES.includes(side) || !getGroup(layout, targetId) || layout.groups.length >= MAX_GROUPS) return layout;
  const source = getGroup(layout, fromId);
  const tab = key ? findTab(source, key) : null;
  if (key && !tab) return layout;
  // Moving a group's only tab beside itself would leave it empty: no-op.
  if (move && tab && fromId === targetId && source.tabs.length < 2) return layout;
  const dir = splitDirection(side);
  const before = side === "left" || side === "up";
  let seq = layout.seq + 1;
  const newId = `g${seq}`;
  let tree = clone(layout.tree);
  const path = findPath(tree, targetId);
  const parent = path.length ? nodeAt(tree, path.slice(0, -1)) : null;
  const index = path.length ? path[path.length - 1] : 0;
  const leaf = { type: "group", id: newId };
  if (parent && parent.dir === dir) {
    const share = parent.sizes[index] / 2;
    parent.sizes[index] = share;
    const at = before ? index : index + 1;
    parent.children.splice(at, 0, leaf);
    parent.sizes.splice(at, 0, share);
  } else {
    seq += 1;
    const target = { type: "group", id: targetId };
    const split = { type: "split", id: `s${seq}`, dir, children: before ? [leaf, target] : [target, leaf], sizes: [0.5, 0.5] };
    if (!parent) tree = split;
    else parent.children[index] = split;
  }
  let next = { ...layout, tree, seq, groups: [...layout.groups, asGroup(newId, createState())], focused: newId };
  if (tab) {
    next = placeTab(next, newId, tab, null);
    if (move) next = closeInGroup(next, fromId, [key]);
    next = focusGroup(next, newId);
  }
  return next;
}

// Remove a group from the grid; its parent split collapses when it has a
// single child left (and merges into a same-direction grandparent).
export function removeGroup(layout, id) {
  if (layout.groups.length <= 1 || !getGroup(layout, id)) return layout;
  let tree = clone(layout.tree);
  const path = findPath(tree, id);
  const parentPath = path.slice(0, -1);
  const parent = nodeAt(tree, parentPath);
  const index = path[path.length - 1];
  parent.children.splice(index, 1);
  const [freed] = parent.sizes.splice(index, 1);
  parent.sizes[Math.max(0, index - 1)] += freed;
  parent.sizes = normalizeSizes(parent.sizes);
  if (parent.children.length === 1) {
    const only = parent.children[0];
    if (!parentPath.length) {
      tree = only;
    } else {
      const grand = nodeAt(tree, parentPath.slice(0, -1));
      const at = parentPath[parentPath.length - 1];
      if (only.type === "split" && only.dir === grand.dir) {
        const share = grand.sizes[at];
        grand.children.splice(at, 1, ...only.children);
        grand.sizes.splice(at, 1, ...only.sizes.map((value) => value * share));
      } else {
        grand.children[at] = only;
      }
    }
  }
  const order = groupOrder(layout);
  const position = order.indexOf(id);
  const groups = layout.groups.filter((group) => group.id !== id);
  let focused = layout.focused;
  if (focused === id) focused = order[position - 1] || order[position + 1] || groups[0].id;
  return { ...layout, tree, groups, focused };
}

// ---------------------------------------------------------------- tabs
export function isDirtyAnywhere(layout, key) {
  return layout.groups.some((group) => Boolean(findTab(group, key)?.dirty));
}

export function groupsWithKey(layout, key) {
  return groupOrder(layout).filter((id) => findTab(getGroup(layout, id), key));
}

export function allKeys(layout) {
  return [...new Set(layout.groups.flatMap((group) => group.tabs.map((tab) => tab.key)))];
}

export function hasDirty(layout) {
  return layout.groups.some((group) => group.tabs.some((tab) => tab.dirty));
}

// Open (or focus) a source in a group, keeping a shared dirty flag.
export function openInGroup(layout, id, source, { preview = false } = {}) {
  let next = updateGroup(layout, id, (group) => openTab(group, source, { preview }));
  const key = getGroup(next, id)?.active;
  if (key && isDirtyAnywhere(layout, key)) next = updateGroup(next, id, (group) => setDirty(group, key, true));
  return focusGroup(next, id);
}

function placeTab(layout, toId, tab, index) {
  const dirty = isDirtyAnywhere(layout, tab.key);
  return updateGroup(layout, toId, (group) => {
    let state = findTab(group, tab.key) ? activateTab(group, tab.key) : openTab(group, { ...tab, preview: false }, { preview: false });
    if (dirty) state = setDirty(state, tab.key, true);
    if (index !== null && index !== undefined) state = moveTabInState(state, tab.key, index);
    return state;
  });
}

// Move (or copy) a tab into another group at `index` (end when omitted).
// Within one group this is a reorder.  An emptied source group collapses.
export function moveTabToGroup(layout, fromId, key, toId, index = null, { copy = false } = {}) {
  const source = getGroup(layout, fromId);
  const tab = findTab(source, key);
  if (!tab || !getGroup(layout, toId)) return layout;
  if (fromId === toId) {
    if (index === null || index === undefined) return layout;
    return updateGroup(layout, fromId, (group) => moveTabInState(group, key, index));
  }
  let next = placeTab(layout, toId, tab, index);
  if (!copy) next = closeInGroup(next, fromId, [key]);
  return focusGroup(next, toId);
}

// Close tabs in one group; an emptied group collapses unless it is the last.
export function closeInGroup(layout, id, keys) {
  let next = updateGroup(layout, id, (group) => closeTabs(group, keys));
  const group = getGroup(next, id);
  if (group && !group.tabs.length && next.groups.length > 1) next = removeGroup(next, id);
  return next;
}

export function setDirtyEverywhere(layout, key, dirty) {
  let next = layout;
  for (const group of layout.groups) {
    if (findTab(group, key)) next = updateGroup(next, group.id, (state) => setDirty(state, key, dirty));
  }
  return next;
}

export function renameEverywhere(layout, key, title) {
  let next = layout;
  for (const group of layout.groups) {
    if (findTab(group, key)) next = updateGroup(next, group.id, (state) => renameTab(state, key, title));
  }
  return next;
}

// Merge every group's tabs into the focused one (reading order).
export function joinGroups(layout) {
  if (layout.groups.length < 2) return layout;
  let merged = getGroup(layout);
  for (const id of groupOrder(layout)) {
    if (id === layout.focused) continue;
    for (const tab of getGroup(layout, id).tabs) {
      if (findTab(merged, tab.key)) continue;
      merged = { ...merged, tabs: [...merged.tabs, { ...tab, preview: false }], mru: [...merged.mru, tab.key] };
    }
  }
  const id = layout.focused;
  return { ...layout, groups: [asGroup(id, normalizeState(merged))], tree: { type: "group", id } };
}

export function neighbourGroup(layout, id, delta) {
  const order = groupOrder(layout);
  const index = order.indexOf(id);
  return index < 0 ? "" : order[index + delta] || "";
}

// ---------------------------------------------------------------- sizes
export function resizeSplit(layout, splitId, sizes) {
  const tree = clone(layout.tree);
  const split = findSplit(tree, splitId);
  if (!split || !Array.isArray(sizes) || sizes.length !== split.children.length) return layout;
  split.sizes = normalizeSizes(sizes);
  return { ...layout, tree };
}

export function equalizeSplit(layout, splitId) {
  const split = findSplit(layout.tree, splitId);
  if (!split) return layout;
  return resizeSplit(layout, splitId, split.children.map(() => 1));
}

// ---------------------------------------------------------- persistence
export function serializeLayout(layout, recent = []) {
  return JSON.stringify({
    v: LAYOUT_VERSION,
    groups: layout.groups.map((group) => {
      const clean = normalizeState(group);
      return { id: group.id, tabs: clean.tabs.map(({ dirty, ...tab }) => tab), active: clean.active, mru: clean.mru };
    }),
    tree: layout.tree, focused: layout.focused, seq: layout.seq,
    recent: normalizeRecent(recent),
  });
}

function validTree(node, ids, seen) {
  if (!node || typeof node !== "object") return false;
  if (node.type === "group") {
    if (!ids.has(node.id) || seen.has(node.id)) return false;
    seen.add(node.id);
    return true;
  }
  return node.type === "split" && (node.dir === "row" || node.dir === "column")
    && Array.isArray(node.children) && node.children.length >= 2
    && Array.isArray(node.sizes) && node.sizes.length === node.children.length
    && node.children.every((child) => validTree(child, ids, seen));
}

function cleanTree(node) {
  if (node.type === "group") return { type: "group", id: String(node.id) };
  return { type: "split", id: String(node.id || ""), dir: node.dir, children: node.children.map(cleanTree), sizes: normalizeSizes(node.sizes) };
}

// Old (v1) stores hold one tab state: it becomes a single group.
export function parseStoredLayout(text) {
  let parsed = null;
  try { parsed = JSON.parse(text || "null"); } catch { parsed = null; }
  const recent = normalizeRecent(parsed?.recent);
  const restore = (raw) => {
    const state = normalizeState(raw);
    state.tabs = state.tabs.map((tab) => ({ ...tab, dirty: false }));
    return state;
  };
  if (!parsed || parsed.v !== LAYOUT_VERSION || !Array.isArray(parsed.groups) || !parsed.groups.length) {
    return { layout: createLayout(restore(parsed)), recent };
  }
  const groups = [];
  const ids = new Set();
  for (const raw of parsed.groups.slice(0, MAX_GROUPS)) {
    const id = String(raw?.id || "");
    if (!/^g\d+$/.test(id) || ids.has(id)) continue;
    ids.add(id);
    groups.push(asGroup(id, restore(raw)));
  }
  if (!groups.length) return { layout: createLayout(), recent };
  let tree = parsed.tree;
  const seen = new Set();
  if (!validTree(tree, ids, seen) || seen.size !== ids.size) {
    tree = groups.length === 1 ? { type: "group", id: groups[0].id }
      : { type: "split", id: "s0", dir: "row", children: groups.map((group) => ({ type: "group", id: group.id })), sizes: groups.map(() => 1 / groups.length) };
  } else {
    tree = cleanTree(tree);
  }
  // Give split nodes unique ids and compute the next free sequence number.
  let seq = Math.max(0, ...[...ids].map((id) => Number(id.slice(1)) || 0), Number(parsed.seq) || 0);
  const splitIds = new Set();
  const fix = (node) => {
    if (node.type !== "split") return;
    if (!/^s\d+$/.test(node.id) || splitIds.has(node.id)) { seq += 1; node.id = `s${seq}`; }
    splitIds.add(node.id);
    seq = Math.max(seq, Number(node.id.slice(1)) || 0);
    node.children.forEach(fix);
  };
  fix(tree);
  const focused = ids.has(parsed.focused) ? parsed.focused : groupOrder({ tree })[0];
  return { layout: { v: LAYOUT_VERSION, groups, focused, tree, seq }, recent };
}
