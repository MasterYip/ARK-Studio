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

/**
 * Build cards and links.
 * @param {object} payload  GET /api/resources/graph
 * @param {{types?: Set<string>|null, kinds?: Set<string>|null, mineOnly?: boolean}} filters
 */
export function buildResourcesModel(payload, filters = {}) {
  const devices = payload?.devices || [];
  const resources = payload?.resources || [];
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
    const h = HEADER_H + (summary ? SUMMARY_H : 0) + rows.length * ROW_H + PAD;
    cards.push({
      id: device.id, device, rows, w: CARD_W, h, summary: Boolean(summary),
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
