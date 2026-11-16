// Resources graph model: turns GET /api/resources/graph into device cards
// (with resource rows), links between devices and utilisation figures.
// Pure (no DOM) so tests can check grouping, filters and maths.

export const RESOURCE_TYPES = ["gpu", "cpu", "storage", "database", "repository", "environment",
  "task_space", "compute", "worktree", "sessions"];
export const DEVICE_KINDS = ["ark", "gpu-server", "host", "compute", "phone", "git-host", "group"];
export const CARD_W = 280;
const HEADER_H = 50;
const SUMMARY_H = 22;
const ROW_H = 26;
const PAD = 10;

const clamp01 = (n) => Math.max(0, Math.min(1, n));
const num = (value) => (Number.isFinite(Number(value)) && value !== null && value !== "" ? Number(value) : null);

/**
 * Utilisation of one resource row: `{ util, mem, ours, others }` as 0..1
 * fractions (null when unknown).  For GPUs `ours`/`others` split the used
 * memory between our jobs and everyone else.
 */
export function utilisation(resource) {
  const m = resource?.metrics || {};
  if (resource?.kind === "gpu") {
    const total = num(m.mem_total_mb);
    const used = num(m.mem_used_mb);
    const ours = num(m.mem_ours_mb) ?? 0;
    const others = num(m.mem_others_mb) ?? Math.max(0, (used ?? 0) - ours);
    return {
      util: num(m.util_pct) === null ? null : clamp01(num(m.util_pct) / 100),
      mem: total ? clamp01((used ?? ours + others) / total) : null,
      ours: total ? clamp01(ours / total) : null,
      others: total ? clamp01(others / total) : null,
    };
  }
  if (resource?.kind === "cpu") {
    const total = num(m.mem_total_mb);
    return {
      util: num(m.cpu_pct) === null ? null : clamp01(num(m.cpu_pct) / 100),
      mem: total ? clamp01((num(m.mem_used_mb) ?? 0) / total) : null,
      ours: null, others: null,
    };
  }
  if (num(m.disk_pct) !== null) return { util: clamp01(num(m.disk_pct) / 100), mem: null, ours: null, others: null };
  return { util: null, mem: null, ours: null, others: null };
}

/** Device-level occupancy shares (0..1) of its GPUs: ours / others / free. */
export function occupancyShares(device) {
  const o = device?.occupancy;
  if (!o || !o.gpus_total) return null;
  const total = o.gpus_total;
  return {
    ours: o.gpus_ours / total,
    others: o.gpus_busy_others / total,
    free: Math.max(0, total - o.gpus_ours - o.gpus_busy_others) / total,
    memOurs: o.mem_total_mb ? o.mem_ours_mb / o.mem_total_mb : 0,
    memOthers: o.mem_total_mb ? o.mem_others_mb / o.mem_total_mb : 0,
  };
}

/** Seconds since an ISO time (null when absent). */
export function ageSeconds(iso, now = Date.now()) {
  if (!iso) return null;
  const ts = Date.parse(iso);
  return Number.isFinite(ts) ? Math.max(0, (now - ts) / 1000) : null;
}

/** `[key, params]` for a relative time ("just now", "5 min ago", ...). */
export function agoKey(seconds) {
  if (seconds === null || seconds === undefined) return ["resources.ago.never", {}];
  if (seconds < 90) return ["resources.ago.now", {}];
  if (seconds < 5400) return ["resources.ago.min", { n: Math.round(seconds / 60) }];
  if (seconds < 172800) return ["resources.ago.h", { n: Math.round(seconds / 3600) }];
  return ["resources.ago.d", { n: Math.round(seconds / 86400) }];
}

// Offline and stale devices are dimmed; their links are dashed.
export const DIM_STATES = new Set(["offline", "stale"]);

// Usage colours: the GPU board's memory thresholds (views/tasks.js barColor):
// amber from 60%, red from 85%.
export const USAGE_WARN_PCT = 60;
export const USAGE_DANGER_PCT = 85;

/** "ok" | "warn" | "danger" for a usage percentage (null when unknown). */
export function usageTone(pct) {
  const value = num(pct);
  if (value === null) return null;
  return value >= USAGE_DANGER_PCT ? "danger" : value >= USAGE_WARN_PCT ? "warn" : "ok";
}

/** Binary byte size: "512 B", "1.5 GiB", "7.2 TiB" (one decimal below 100). */
export function formatBytes(bytes) {
  let value = num(bytes);
  if (value === null || value < 0) return "?";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  const text = unit === 0 || value >= 100 ? String(Math.round(value)) : value.toFixed(1).replace(/\.0$/, "");
  return `${text} ${units[unit]}`;
}

/**
 * A GPU server's storage (device.storage from the probe's df) as resource
 * rows of kind "storage", so filters, rows, tooltips and the inspector treat
 * them like any other resource.  `metrics.storage` marks them.
 */
export function storageResources(device) {
  const items = Array.isArray(device?.storage) ? device.storage : [];
  return items.filter((item) => item && item.mount).map((item, index) => ({
    id: `${device.id}/storage/df/${index}`, device: device.id, kind: "storage", label: String(item.mount),
    path: String(item.mount), redacted: false, owner: null, setup_key: null, last_known: false,
    metrics: { storage: true, disk_pct: num(item.percent), used_bytes: num(item.used_bytes),
      total_bytes: num(item.total_bytes), free_bytes: num(item.free_bytes), data: item.data === true },
  }));
}

/**
 * Build cards and links.
 * @param {object} payload  GET /api/resources/graph
 * @param {{types?: Set<string>|null, kinds?: Set<string>|null, mineOnly?: boolean,
 *   collapsed?: Set<string>|null}} filters  `collapsed`: device ids whose details are folded
 */
export function buildResourcesModel(payload, filters = {}) {
  const devices = payload?.devices || [];
  const resources = [...(payload?.resources || []), ...devices.flatMap(storageResources)];
  const collapsed = filters.collapsed instanceof Set ? filters.collapsed : null;
  const types = filters.types && filters.types.size ? filters.types : null;
  const kinds = filters.kinds && filters.kinds.size ? filters.kinds : null;
  const counts = { types: {}, kinds: {} };
  for (const res of resources) counts.types[res.kind] = (counts.types[res.kind] || 0) + 1;
  for (const device of devices) counts.kinds[device.kind] = (counts.kinds[device.kind] || 0) + 1;
  const rowsByDevice = new Map();
  for (const res of resources) {
    if (types && !types.has(res.kind)) continue;
    if (!rowsByDevice.has(res.device)) rowsByDevice.set(res.device, []);
    rowsByDevice.get(res.device).push(res);
  }
  const cards = [];
  for (const device of devices) {
    if (kinds && !kinds.has(device.kind)) continue;
    if (filters.mineOnly && device.scope === "personal" && !device.mine) continue;
    if (filters.mineOnly && device.scope !== "personal" && !rowsByDevice.get(device.id)?.some((r) => r.metrics?.ours || r.owner === payload?.viewer)
      && device.kind !== "ark" && !(device.occupancy?.gpus_ours)) continue;
    const rows = (rowsByDevice.get(device.id) || []).slice()
      .sort((a, b) => RESOURCE_TYPES.indexOf(a.kind) - RESOURCE_TYPES.indexOf(b.kind) || String(a.label).localeCompare(String(b.label)));
    if (types && !rows.length) continue;
    const summary = device.occupancy?.gpus_total || device.metrics?.disk_pct != null || device.metrics?.cpu_pct != null;
    const folded = Boolean(rows.length && collapsed?.has(device.id));
    const h = HEADER_H + (summary ? SUMMARY_H : 0) + (folded ? 0 : rows.length * ROW_H) + PAD;
    cards.push({
      id: device.id, device, rows: folded ? [] : rows, allRows: rows, collapsed: folded,
      collapsible: rows.length > 0, hiddenCount: folded ? rows.length : 0, w: CARD_W, h, summary: Boolean(summary),
      dim: DIM_STATES.has(device.status?.state), personal: device.scope === "personal",
      shares: occupancyShares(device),
    });
  }
  const ids = new Set(cards.map((card) => card.id));
  const byId = new Map(devices.map((device) => [device.id, device]));
  const links = (payload?.connections || [])
    .filter((link) => ids.has(link.source) && ids.has(link.target))
    .map((link) => {
      const states = [byId.get(link.source)?.status?.state, byId.get(link.target)?.status?.state];
      return {
        ...link,
        dashed: link.state !== "ok" || states.some((state) => DIM_STATES.has(state)),
        failed: link.state === "failed",
      };
    });
  return { cards, links, counts };
}

/** Row geometry inside a card (card-local, top-left origin). */
export function rowBox(card, index) {
  const top = HEADER_H + (card.summary ? SUMMARY_H : 0) + index * ROW_H;
  return { x: PAD, y: top, w: card.w - PAD * 2, h: ROW_H - 4 };
}

export const CARD_METRICS = { HEADER_H, SUMMARY_H, ROW_H, PAD };

/**
 * The fold toggle in a card's header (card-local box): left of the owner
 * avatar when there is one.
 */
export function collapseToggleBox(card) {
  const owner = Boolean(card?.device?.owner);
  return { x: card.w - (owner ? 58 : 30), y: 8, w: 22, h: 20 };
}

// ------------------------------------------------------------ fold state
// Mind's keys (graph.js _onKeyDown): + / = expand and - collapse the
// selected cards one level, * and / recursively.  Resource cards have one
// level of details, so both pairs fold the selected cards' details; with no
// selection nothing happens (as in Mind).

/** A new collapsed set with `ids` folded (`collapse`) or unfolded. */
export function setCollapsed(collapsed, ids, collapse) {
  const next = new Set(collapsed || []);
  for (const id of ids || []) {
    if (collapse) next.add(id); else next.delete(id);
  }
  return next;
}

/**
 * Clicking a card's fold toggle: the clicked card flips; when it is part of
 * a multi-selection, the whole selection follows it (batch fold).
 */
export function toggleCollapsed(collapsed, id, selected = []) {
  const collapse = !(collapsed || new Set()).has(id);
  const ids = selected.length > 1 && selected.includes(id) ? selected : [id];
  return setCollapsed(collapsed, ids, collapse);
}

/** Device ids of collapsible cards among `ids` (all cards when ids is null). */
export function collapsibleIds(cards, ids = null) {
  const wanted = ids ? new Set(ids) : null;
  return (cards || []).filter((card) => card.collapsible && (!wanted || wanted.has(card.id))).map((card) => card.id);
}

// ------------------------------------------------------------- keyboard
const ARROWS = { ArrowLeft: "left", ArrowRight: "right", ArrowUp: "up", ArrowDown: "down" };

/**
 * The Resources graph's keyboard map, the Node graph's (graph.js
 * _onKeyDown) minus editing keys: returns an action name or null.
 *   ? help, Esc close help / clear selection, F fit, R / E read / edit,
 *   Ctrl+A select all, + or = expand, - collapse, * expand all, / collapse all,
 *   arrows move the selection, Enter opens details (read mode);
 *   Tab (read mode) and Backspace are swallowed ("none").
 */
export function resourcesKeyAction(event, { mode = "read", helpOpen = false } = {}) {
  const tag = event?.target?.tagName;
  if (["INPUT", "TEXTAREA", "SELECT"].includes(tag) || event?.target?.isContentEditable) return null;
  const key = event.key;
  const ctrl = event.ctrlKey || event.metaKey;
  const plain = !ctrl && !event.altKey && !event.shiftKey;
  if (key === "?") return "help";
  if (key === "Escape") return helpOpen ? "close-help" : "clear";
  if (plain && (key === "f" || key === "F")) return "fit";
  if (plain && (key === "r" || key === "R")) return "mode-read";
  if (plain && (key === "e" || key === "E")) return "mode-edit";
  if (ctrl && !event.shiftKey && String(key).toLowerCase() === "a") return "select-all";
  if (!ctrl && !event.altKey) {
    if (key === "+" || key === "=") return "expand";
    if (key === "-") return "collapse";
    if (key === "*") return "expand-all";
    if (key === "/") return "collapse-all";
  }
  if (ARROWS[key] && !ctrl && !event.altKey) return `nav-${ARROWS[key]}`;
  if (key === "Enter" && !ctrl && !event.altKey) return mode === "read" ? "open" : null;
  if (key === "Tab" && mode !== "edit" && plain) return "none";
  if (key === "Backspace") return "none";
  return null;
}

/**
 * The card an arrow key moves to: nearest in that direction, weighting the
 * cross-axis distance like the Node graph (graph.js _navigateMind).  Cards
 * are `{id, x, y}` centres; without a current card the first one is chosen.
 */
export function nearestInDirection(cards, fromId, direction) {
  const list = [...(cards || [])];
  const current = list.find((card) => card.id === fromId);
  if (!current) return list[0]?.id ?? null;
  const dir = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] }[direction];
  if (!dir) return null;
  let best = null, bestScore = Infinity;
  for (const card of list) {
    if (card === current) continue;
    const dx = card.x - current.x, dy = card.y - current.y;
    const along = dir[0] ? dx * dir[0] : dy * dir[1];
    if (along <= 1) continue;
    const score = along + (dir[0] ? Math.abs(dy) : Math.abs(dx)) * 2.2;
    if (score < bestScore) { bestScore = score; best = card; }
  }
  return best ? best.id : null;
}

/**
 * Cards inside a screen-space selection box (`toScreen(x, y)` maps world to
 * screen): touching it, or fully inside with `contain` (Shift), as in the
 * Node graph's box selection.
 */
export function cardsInBox(cards, box, toScreen, contain = false) {
  const x1 = Math.min(box.x1, box.x2), x2 = Math.max(box.x1, box.x2);
  const y1 = Math.min(box.y1, box.y2), y2 = Math.max(box.y1, box.y2);
  return (cards || []).filter((card) => {
    const a = toScreen(card.x - card.w / 2, card.y - card.h / 2);
    const b = toScreen(card.x + card.w / 2, card.y + card.h / 2);
    return contain ? a.x >= x1 && b.x <= x2 && a.y >= y1 && b.y <= y2
      : b.x >= x1 && a.x <= x2 && b.y >= y1 && a.y <= y2;
  }).map((card) => card.id);
}
