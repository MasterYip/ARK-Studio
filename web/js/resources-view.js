// Execute › Resources: a node graph of devices (ARK server, GPU servers,
// members' machines, phones, git hosts), the resources on each device and
// how devices connect.  Data: GET /api/resources/graph (read-only, never
// probes).  Layout and curves come from node-graph-layout.js.
import { h, clear } from "./dom.js";
import { t, hasKey } from "./i18n.js";
import { icon } from "./icons.js";
import { api, cachedThenFresh } from "./api.js";
import { layeredLayout, routeNodeEdges } from "./node-graph-layout.js";
import {
  RESOURCE_TYPES, buildResourcesModel, utilisation, ageSeconds, agoKey, rowBox, CARD_METRICS,
} from "./resources-model.js";

const REFRESH_MS = 30000;
const FILTER_KEY = "ark.resourcesGraph.filters";

const ago = (iso) => {
  const [key, vars] = agoKey(ageSeconds(iso));
  return t(key, vars);
};

function palette() {
  const css = getComputedStyle(document.documentElement);
  const get = (name, fallback) => (css.getPropertyValue(name) || "").trim() || fallback;
  return {
    ink: get("--ink", "#e8eef6"), sub: get("--sub", "#93a2b6"), line: get("--line", "#273243"),
    bg: get("--bg", "#0f141c"), card: get("--card", "#171f2b"), accent: get("--accent", "#3b82f6"),
    ok: get("--green", "#10b981"), warn: get("--amber", "#f59e0b"), danger: get("--red", "#ef4444"),
    violet: get("--violet", "#a78bfa"),
    lane: get("--lane", "#161d27"),
  };
}

const STATE_COLOR = (pal, state) => ({ online: pal.ok, offline: pal.danger, stale: pal.warn }[state] || pal.sub);
const LINK_COLOR = (pal, kind) => ({ ssh: pal.accent, "git-remote": pal.violet, paired: pal.ok, "agent-api": pal.ok }[kind] || pal.sub);

// Status reasons arrive as codes with parameters (English fallback in
// `reason`) so both languages read naturally.
function reasonText(st) {
  const key = `resources.reason.${st?.code || ""}`;
  if (!st?.code || !hasKey(key)) return st?.reason || "";
  const params = st.params || {};
  return t(key, { error: params.error || "", kind: params.kind || "",
    signal: params.signal ? t(`resources.signal.${params.signal}`) : "" });
}

function roundRect(ctx, x, y, w, h, r) {
  const radius = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + radius, y);
  ctx.arcTo(x + w, y, x + w, y + h, radius);
  ctx.arcTo(x + w, y + h, x, y + h, radius);
  ctx.arcTo(x, y + h, x, y, radius);
  ctx.arcTo(x, y, x + w, y, radius);
  ctx.closePath();
}

function ellipsis(ctx, text, width) {
  const value = String(text ?? "");
  if (ctx.measureText(value).width <= width) return value;
  let lo = 0, hi = value.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (ctx.measureText(value.slice(0, mid) + "…").width <= width) lo = mid; else hi = mid - 1;
  }
  return value.slice(0, lo) + "…";
}

function loadFilters() {
  try {
    const raw = JSON.parse(localStorage.getItem(FILTER_KEY) || "{}");
    return { types: new Set(Array.isArray(raw.types) ? raw.types : []), mineOnly: raw.mineOnly === true };
  } catch { return { types: new Set(), mineOnly: false }; }
}

function saveFilters(filters) {
  try { localStorage.setItem(FILTER_KEY, JSON.stringify({ types: [...filters.types], mineOnly: filters.mineOnly })); }
  catch { /* storage unavailable */ }
}

export function resourcesGraphPanel(ctx) {
  const filters = loadFilters();
  const state = { payload: null, model: null, layout: null, routes: null, zoom: 1, pan: { x: 0, y: 0 },
    hover: null, selected: null, fitted: false, timer: 0, loading: false, error: "" };
  const canvas = h("canvas", { class: "resources-canvas", "aria-label": t("resources.title") });
  const tooltip = h("div", { class: "graph-tooltip hidden resources-tooltip" });
  const details = h("aside", { class: "resources-details hidden", "aria-live": "polite" });
  const filterBar = h("div", { class: "resources-filters", role: "group", "aria-label": t("resources.filter") });
  const status = h("span", { class: "resources-status muted" });
  const unknowns = h("details", { class: "resources-unknowns" });
  const stage = h("div", { class: "resources-stage" }, canvas, tooltip, details);
  const refreshBtn = h("button", { class: "ghost-btn sm", type: "button", onclick: () => load() },
    icon("refresh", "sm"), t("resources.refresh"));
  const fitBtn = h("button", { class: "ghost-btn sm", type: "button", onclick: () => { fit(); draw(); } },
    icon("fit", "sm"), t("resources.fit"));
  const panel = h("section", { class: "panel resources-panel", "data-resources-graph": "" },
    h("div", { class: "panel-head" },
      h("div", null, h("h3", null, t("resources.title")), h("p", { class: "muted resources-lead" }, t("resources.lead"))),
      h("div", { class: "row tight" }, status, fitBtn, refreshBtn)),
    filterBar, stage, legend(), unknowns);

  // ------------------------------------------------------------- data
  async function load() {
    if (state.loading) return;
    state.loading = true;
    status.textContent = t("resources.loading");
    try {
      await cachedThenFresh("/api/resources/graph", (options) => api.resourcesGraph(options), (payload) => {
        state.payload = payload;
        state.error = "";
        rebuild();
        status.textContent = t("resources.updated", { when: ago(state.payload.generated_at) });
      });
    } catch (err) {
      state.error = err.message;
      status.textContent = err.message;
    } finally {
      state.loading = false;
    }
  }

  function rebuild() {
    if (!state.payload) return;
    state.model = buildResourcesModel(state.payload, filters);
    const nodes = state.model.cards.map((card) => ({ id: card.id, w: card.w, h: card.h }));
    const edges = state.model.links.map((link) => ({ id: link.id, source: link.source, target: link.target }));
    // Pack unconnected devices to suit the stage's shape.
    const { w, h: hh } = size();
    const { positions, waypoints } = layeredLayout(nodes, edges, { maxLayer: 12, layerGap: 90, aspect: Math.max(0.6, Math.min(2, w / hh)) });
    state.layout = new Map(state.model.cards.map((card) => [card.id, { ...card, ...positions.get(card.id) }]));
    state.routes = routeNodeEdges([...state.layout.values()], edges, { waypoints });
    renderFilters();
    renderUnknowns();
    if (!state.fitted) { fit(); state.fitted = true; }
    if (state.selected && !state.layout.has(state.selected.device)) state.selected = null;
    renderDetails();
    draw();
  }

  function renderFilters() {
    clear(filterBar);
    const counts = state.model?.counts.types || {};
    for (const type of RESOURCE_TYPES.filter((kind) => counts[kind])) {
      const on = filters.types.has(type);
      filterBar.appendChild(h("button", {
        class: `type-chip${on ? " on" : ""}`, type: "button", "aria-pressed": String(on), "data-resource-type": type,
        onclick: () => { if (on) filters.types.delete(type); else filters.types.add(type); saveFilters(filters); state.fitted = false; rebuild(); },
      }, t(`resources.type.${type}`), h("span", { class: "n" }, String(counts[type]))));
    }
    const mine = h("label", { class: "resources-mine" },
      h("input", { type: "checkbox", checked: filters.mineOnly ? true : undefined,
        onchange: (event) => { filters.mineOnly = event.target.checked; saveFilters(filters); state.fitted = false; rebuild(); } }),
      t("resources.mine_only"));
    filterBar.appendChild(mine);
    if (filters.types.size || filters.mineOnly) {
      filterBar.appendChild(h("button", { class: "ghost-btn sm", type: "button",
        onclick: () => { filters.types.clear(); filters.mineOnly = false; saveFilters(filters); state.fitted = false; rebuild(); } },
      t("resources.clear_filters")));
    }
  }

  function renderUnknowns() {
    clear(unknowns);
    const items = state.payload?.unknowns || [];
    unknowns.appendChild(h("summary", null, t("resources.unknowns", { n: items.length })));
    unknowns.appendChild(h("ul", null, items.map((code) => h("li", null,
      hasKey(`resources.unknown.${code}`) ? t(`resources.unknown.${code}`) : code))));
  }

  function legend() {
    const swatch = (cls, label) => h("span", { class: "resources-legend-item" }, h("i", { class: cls }), label);
    return h("div", { class: "resources-legend", "aria-label": t("resources.legend") },
      swatch("sw ours", t("resources.legend.ours")), swatch("sw others", t("resources.legend.others")),
      swatch("sw free", t("resources.legend.free")),
      swatch("dot online", t("resources.state.online")), swatch("dot stale", t("resources.state.stale")),
      swatch("dot offline", t("resources.state.offline")), swatch("dot unknown", t("resources.state.unknown")),
      swatch("line ssh", t("resources.link.ssh")), swatch("line git", t("resources.link.git-remote")),
      swatch("line client", t("resources.link.client")), swatch("line dashed", t("resources.legend.dashed")),
      swatch("card personal", t("resources.legend.personal")));
  }

  // ----------------------------------------------------------- camera
  function size() {
    const rect = stage.getBoundingClientRect();
    return { w: Math.max(200, rect.width), h: Math.max(200, rect.height) };
  }

  function fit() {
    if (!state.layout?.size) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const card of state.layout.values()) {
      x0 = Math.min(x0, card.x - card.w / 2); x1 = Math.max(x1, card.x + card.w / 2);
      y0 = Math.min(y0, card.y - card.h / 2); y1 = Math.max(y1, card.y + card.h / 2);
    }
    const { w, h: hh } = size();
    state.zoom = Math.max(0.25, Math.min(1.2, Math.min((w - 40) / (x1 - x0), (hh - 40) / (y1 - y0))));
    state.pan = { x: w / 2 - (x0 + x1) / 2 * state.zoom, y: hh / 2 - (y0 + y1) / 2 * state.zoom };
  }

  const toScreen = (x, y) => ({ x: x * state.zoom + state.pan.x, y: y * state.zoom + state.pan.y });
  const toWorld = (x, y) => ({ x: (x - state.pan.x) / state.zoom, y: (y - state.pan.y) / state.zoom });

  // ------------------------------------------------------------- draw
  function draw() {
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const { w, h: hh } = size();
    const dpr = Math.min(2.5, window.devicePixelRatio || 1);
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(hh * dpr)) {
      canvas.width = Math.round(w * dpr); canvas.height = Math.round(hh * dpr);
      canvas.style.width = `${w}px`; canvas.style.height = `${hh}px`;
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, hh);
    if (!state.model) return;
    const pal = palette();
    if (!state.model.cards.length) {
      ctx.fillStyle = pal.sub;
      ctx.font = "13px system-ui, 'Segoe UI'";
      ctx.textAlign = "center";
      ctx.fillText(t(state.payload?.devices?.length ? "resources.empty_filtered" : "resources.empty"), w / 2, hh / 2);
      return;
    }
    ctx.save();
    ctx.translate(state.pan.x, state.pan.y);
    ctx.scale(state.zoom, state.zoom);
    drawLinks(ctx, pal);
    for (const card of state.layout.values()) drawCard(ctx, pal, card);
    ctx.restore();
  }

  function drawLinks(ctx, pal) {
    const hoverDevice = state.hover?.device;
    for (const link of state.model.links) {
      const route = state.routes.get(link.id);
      if (!route) continue;
      const touched = hoverDevice && (link.source === hoverDevice || link.target === hoverDevice);
      ctx.save();
      ctx.beginPath();
      route.segments.forEach((seg, i) => {
        if (!i) ctx.moveTo(seg[0], seg[1]);
        ctx.bezierCurveTo(seg[2], seg[3], seg[4], seg[5], seg[6], seg[7]);
      });
      ctx.strokeStyle = link.failed ? pal.danger : LINK_COLOR(pal, link.kind);
      ctx.globalAlpha = hoverDevice && !touched ? 0.25 : link.dashed ? 0.7 : 0.9;
      ctx.lineWidth = touched ? 2.6 : 1.8;
      if (link.dashed) ctx.setLineDash([6, 5]);
      ctx.stroke();
      ctx.setLineDash([]);
      // Arrow into the target's input port.
      const end = route.end;
      ctx.beginPath();
      ctx.moveTo(end.x - 1, end.y);
      ctx.lineTo(end.x - 10, end.y - 5);
      ctx.lineTo(end.x - 10, end.y + 5);
      ctx.closePath();
      ctx.fillStyle = ctx.strokeStyle;
      ctx.fill();
      // Label pill.
      const text = t(`resources.link.${link.kind}`);
      ctx.font = "600 10px system-ui, 'Segoe UI'";
      const tw = ctx.measureText(text).width + 10;
      roundRect(ctx, route.mid.x - tw / 2, route.mid.y - 8, tw, 16, 8);
      ctx.globalAlpha = 1;
      ctx.fillStyle = pal.bg;
      ctx.fill();
      ctx.strokeStyle = link.failed ? pal.danger : LINK_COLOR(pal, link.kind);
      ctx.lineWidth = 1;
      ctx.stroke();
      ctx.fillStyle = pal.ink;
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.fillText(text, route.mid.x, route.mid.y + 0.5);
      ctx.restore();
    }
  }

  function drawCard(ctx, pal, card) {
    const { device } = card;
    const x = card.x - card.w / 2, y = card.y - card.h / 2;
    const selected = state.selected?.device === card.id;
    ctx.save();
    ctx.globalAlpha = card.dim ? 0.6 : 1;
    ctx.shadowColor = "rgba(0,0,0,.14)";
    ctx.shadowBlur = 10;
    ctx.shadowOffsetY = 2;
    roundRect(ctx, x, y, card.w, card.h, 10);
    ctx.fillStyle = pal.card;
    ctx.fill();
    ctx.shadowColor = "transparent";
    // Personal devices: dashed outline in the accent colour.
    if (card.personal) ctx.setLineDash([5, 4]);
    ctx.strokeStyle = selected ? pal.accent : card.personal ? pal.accent : pal.line;
    ctx.lineWidth = selected ? 2.2 : card.personal ? 1.4 : 1.1;
    ctx.stroke();
    ctx.setLineDash([]);
    // Ports.
    for (const px of [x, x + card.w]) {
      ctx.beginPath();
      ctx.arc(px, card.y, 4, 0, Math.PI * 2);
      ctx.fillStyle = pal.bg;
      ctx.fill();
      ctx.strokeStyle = pal.sub;
      ctx.lineWidth = 1.2;
      ctx.stroke();
    }
    // Header: status dot, title, kind / owner.
    const stateName = device.status?.state || "unknown";
    ctx.beginPath();
    ctx.arc(x + 16, y + 18, 5, 0, Math.PI * 2);
    ctx.fillStyle = STATE_COLOR(pal, stateName);
    ctx.fill();
    ctx.fillStyle = pal.ink;
    ctx.font = "650 13px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    const owner = device.owner ? (device.owner.display_name || device.owner.username) : "";
    ctx.fillText(ellipsis(ctx, device.label, card.w - 46 - (owner ? 30 : 0)), x + 28, y + 18);
    if (owner) {
      ctx.beginPath();
      ctx.arc(x + card.w - 20, y + 18, 10, 0, Math.PI * 2);
      ctx.fillStyle = pal.accent;
      ctx.fill();
      ctx.fillStyle = "#fff";
      ctx.font = "700 10px system-ui";
      ctx.textAlign = "center";
      ctx.fillText(owner.slice(0, 1).toUpperCase(), x + card.w - 20, y + 18.5);
    }
    ctx.textAlign = "left";
    ctx.font = "10.5px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    ctx.fillStyle = pal.sub;
    const kind = t(`resources.kind.${device.kind}`);
    const scope = card.personal ? t("resources.personal_of", { name: owner || "?" }) : t("resources.shared");
    const stateText = stateName === "online" ? t("resources.state.online")
      : device.status?.last_seen ? t("resources.last_seen", { when: ago(device.status.last_seen) })
        : t(`resources.state.${stateName}`);
    ctx.fillText(ellipsis(ctx, `${kind} · ${scope} · ${stateText}`, card.w - 24), x + 12, y + 37);
    // Summary bar: GPU occupancy (ours / others / free) or disk / CPU.
    if (card.summary) {
      const by = y + CARD_METRICS.HEADER_H;
      const bw = card.w - 24;
      const shares = card.shares;
      roundRect(ctx, x + 12, by, bw, 8, 4);
      ctx.fillStyle = pal.lane;
      ctx.fill();
      if (shares) {
        let cx = x + 12;
        for (const [part, color] of [[shares.ours, pal.accent], [shares.others, pal.warn]]) {
          if (part > 0) { ctx.fillStyle = color; ctx.fillRect(cx, by, bw * part, 8); cx += bw * part; }
        }
        const o = device.occupancy;
        ctx.fillStyle = pal.sub;
        ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        ctx.fillText(t("resources.gpu_summary", { ours: o.gpus_ours, others: o.gpus_busy_others, free: o.gpus_free }), x + 12, by + 15);
      } else {
        const pct = (device.metrics?.disk_pct ?? device.metrics?.cpu_pct ?? 0) / 100;
        ctx.fillStyle = pct > 0.9 ? pal.danger : pal.accent;
        ctx.fillRect(x + 12, by, bw * Math.max(0, Math.min(1, pct)), 8);
        ctx.fillStyle = pal.sub;
        ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        const label = device.metrics?.disk_pct != null ? t("resources.disk_used", { pct: device.metrics.disk_pct })
          : t("resources.cpu_used", { pct: device.metrics.cpu_pct });
        ctx.fillText(label, x + 12, by + 15);
      }
    }
    // Resource rows.
    card.rows.forEach((res, index) => {
      const box = rowBox(card, index);
      const rx = x + box.x, ry = y + box.y;
      const hovered = state.hover?.resource?.id === res.id;
      roundRect(ctx, rx, ry, box.w, box.h, 5);
      ctx.fillStyle = hovered ? pal.lane : "transparent";
      if (hovered) ctx.fill();
      ctx.strokeStyle = res.metrics?.ours ? pal.accent : pal.line;
      ctx.lineWidth = res.metrics?.ours ? 1.4 : 0.8;
      ctx.stroke();
      ctx.font = "600 9px ui-monospace, Consolas, monospace";
      ctx.fillStyle = res.metrics?.ours ? pal.accent : pal.sub;
      const tag = t(`resources.type.${res.kind}`).toUpperCase();
      ctx.fillText(ellipsis(ctx, tag, 58), rx + 6, ry + box.h / 2);
      ctx.font = `${res.last_known ? "italic " : ""}11px system-ui, 'Segoe UI', 'Microsoft YaHei'`;
      ctx.fillStyle = pal.ink;
      const u = utilisation(res);
      const meterW = u.util !== null || u.mem !== null ? 70 : 0;
      const right = rowRight(res);
      ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      const rightW = right ? ctx.measureText(right).width + 8 : 0;
      ctx.font = `${res.last_known ? "italic " : ""}11px system-ui, 'Segoe UI', 'Microsoft YaHei'`;
      ctx.fillText(ellipsis(ctx, res.label, box.w - 70 - meterW - rightW), rx + 66, ry + box.h / 2);
      if (right) {
        ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        ctx.fillStyle = pal.sub;
        ctx.textAlign = "right";
        ctx.fillText(right, rx + box.w - 6 - meterW, ry + box.h / 2);
        ctx.textAlign = "left";
      }
      if (meterW) drawMeter(ctx, pal, res, u, rx + box.w - meterW - 4, ry + 4, meterW - 4, box.h - 8);
    });
    ctx.restore();
  }

  function rowRight(res) {
    const m = res.metrics || {};
    if (res.last_known) return t("resources.last_known");
    if (res.kind === "sessions") return t("resources.sessions_summary", { running: m.running ?? 0, mine: m.mine ?? 0 });
    if (res.redacted) return t("resources.path_hidden");
    if (res.kind === "gpu" && m.job) return m.job;
    if (res.kind === "compute" && m.gpus) return t("resources.gpus_n", { n: m.gpus });
    return "";
  }

  // Two thin bars: utilisation and memory (ours / others for GPUs).
  function drawMeter(ctx, pal, res, u, x, y, w, h) {
    const half = (h - 2) / 2;
    const bar = (top, value, color) => {
      roundRect(ctx, x, top, w, half, 2);
      ctx.fillStyle = pal.lane; ctx.fill();
      if (value !== null) { ctx.fillStyle = color; ctx.fillRect(x, top, w * value, half); }
    };
    bar(y, u.util, u.util !== null && u.util > 0.9 ? pal.danger : pal.ok);
    if (u.ours !== null || u.others !== null) {
      roundRect(ctx, x, y + half + 2, w, half, 2);
      ctx.fillStyle = pal.lane; ctx.fill();
      ctx.fillStyle = pal.accent; ctx.fillRect(x, y + half + 2, w * (u.ours || 0), half);
      ctx.fillStyle = pal.warn; ctx.fillRect(x + w * (u.ours || 0), y + half + 2, w * (u.others || 0), half);
    } else bar(y + half + 2, u.mem, pal.accent);
  }

  // -------------------------------------------------------- hit tests
  function hit(px, py) {
    if (!state.layout) return null;
    const p = toWorld(px, py);
    for (const card of [...state.layout.values()].reverse()) {
      const x = card.x - card.w / 2, y = card.y - card.h / 2;
      if (p.x < x || p.x > x + card.w || p.y < y || p.y > y + card.h) continue;
      const index = card.rows.findIndex((_, i) => {
        const box = rowBox(card, i);
        return p.y >= y + box.y && p.y <= y + box.y + box.h && p.x >= x + box.x && p.x <= x + box.x + box.w;
      });
      return { device: card.id, card, resource: index >= 0 ? card.rows[index] : null, header: p.y < y + CARD_METRICS.HEADER_H };
    }
    return null;
  }

  function showTooltip(target, px, py) {
    if (!target) { tooltip.classList.add("hidden"); return; }
    const device = target.card.device;
    const st = device.status || {};
    clear(tooltip);
    if (target.resource) {
      const res = target.resource;
      tooltip.append(...[h("b", null, res.label),
        h("div", null, `${t(`resources.type.${res.kind}`)}${res.owner ? ` · ${res.owner}` : ""}`),
        res.path ? h("div", { class: "mono" }, res.path) : res.redacted ? h("div", null, t("resources.path_hidden")) : null,
        ...metricLines(res).map((line) => h("div", null, line)),
        res.last_known ? h("div", null, t("resources.last_known_hint")) : null].filter(Boolean));
    } else {
      tooltip.append(...[h("b", null, device.label),
        h("div", null, `${t(`resources.state.${st.state || "unknown"}`)} — ${reasonText(st)}`),
        st.last_seen ? h("div", null, t("resources.last_seen", { when: `${ago(st.last_seen)} (${new Date(st.last_seen).toLocaleString()})` })) : null,
        h("div", null, t(`resources.source.${st.source || "none"}`))].filter(Boolean));
    }
    tooltip.classList.remove("hidden");
    const rect = stage.getBoundingClientRect();
    tooltip.style.left = `${Math.min(px + 14, rect.width - 260)}px`;
    tooltip.style.top = `${Math.min(py + 14, rect.height - 120)}px`;
  }

  function metricLines(res) {
    const m = res.metrics || {};
    const lines = [];
    if (res.kind === "gpu") {
      lines.push(t("resources.metric.gpu_util", { pct: m.util_pct ?? "?" }));
      lines.push(t("resources.metric.gpu_mem", { used: Math.round((m.mem_used_mb || 0) / 1024), total: Math.round((m.mem_total_mb || 0) / 1024),
        ours: Math.round((m.mem_ours_mb || 0) / 1024), others: Math.round((m.mem_others_mb || 0) / 1024) }));
      if (m.job) lines.push(t("resources.metric.job", { job: m.job }));
    } else if (res.kind === "cpu") {
      if (m.cpu_pct != null) lines.push(t("resources.cpu_used", { pct: m.cpu_pct }));
      if (m.mem_total_mb) lines.push(t("resources.metric.ram", { used: Math.round(m.mem_used_mb / 1024), total: Math.round(m.mem_total_mb / 1024) }));
    } else if (res.kind === "sessions") {
      lines.push(t("resources.sessions_summary", { running: m.running ?? 0, mine: m.mine ?? 0 }));
    } else if (m.disk_pct != null) lines.push(t("resources.disk_used", { pct: m.disk_pct }));
    if (res.verified_at) lines.push(t("resources.verified", { when: ago(res.verified_at) }));
    return lines;
  }

  // ---------------------------------------------------------- details
  function renderDetails() {
    clear(details);
    const target = state.selected;
    const card = target && state.layout?.get(target.device);
    if (!card) { details.classList.add("hidden"); return; }
    const device = card.device;
    const st = device.status || {};
    details.classList.remove("hidden");
    const actions = h("div", { class: "row tight wrap" });
    if (device.actions?.includes("check") && state.payload?.probe?.enabled) {
      actions.appendChild(h("button", { class: "btn sm", type: "button", "data-resources-check": device.id,
        onclick: async (event) => {
          event.currentTarget.disabled = true;
          try {
            await api.gpuProbe({ servers: [device.label] });
            ctx.toast?.(t("resources.checked"), "ok");
          } catch (err) { ctx.toast?.(err.message, "err"); }
          await load();
        } }, icon("refresh", "sm"), t("resources.check_now")));
    }
    if (device.kind === "gpu-server") {
      actions.appendChild(h("button", { class: "btn sm ghost", type: "button",
        onclick: () => ctx.navigateWorkspace?.("execute", "gpu") }, t("resources.open_gpu_board")));
    }
    const setupKey = target.resource?.setup_key || card.rows.find((row) => row.setup_key)?.setup_key;
    if (setupKey) {
      actions.appendChild(h("button", { class: "btn sm ghost", type: "button",
        onclick: () => {
          try { localStorage.setItem("ark.settingsTab", "setupResources"); } catch { /* ignore */ }
          ctx.navigateWorkspace?.("settings");
        } }, t("resources.open_setup")));
    }
    const privacy = device.mine && device.prefs ? h("div", { class: "resources-privacy" },
      h("div", { class: "physics-name" }, t("resources.privacy")),
      ...["share_details", "hidden"].map((key) => h("label", { class: "resources-toggle" }, // i18n-ignore: preference keys
        h("input", { type: "checkbox", "data-resources-pref": key, checked: device.prefs[key] ? true : undefined,
          onchange: async (event) => {
            try {
              await api.resourceDevicePrivacy(device.id, { [key]: event.target.checked });
              await load();
            } catch (err) { ctx.toast?.(err.message, "err"); event.target.checked = !event.target.checked; }
          } }),
        h("span", null, t(`resources.pref.${key}`)))),
      h("p", { class: "physics-hint" }, t("resources.privacy_hint"))) : null;
    const res = target.resource;
    details.append(...[
      h("div", { class: "resources-details-head" },
        h("b", null, device.label),
        h("button", { class: "icon-btn", type: "button", "aria-label": t("action.close"),
          onclick: () => { state.selected = null; renderDetails(); draw(); } }, icon("x", "sm"))),
      h("div", { class: `resources-state ${st.state || "unknown"}` }, h("i"), t(`resources.state.${st.state || "unknown"}`)),
      h("p", { class: "muted" }, reasonText(st)),
      st.last_seen ? h("p", { class: "muted" }, t("resources.last_seen", { when: `${ago(st.last_seen)} · ${new Date(st.last_seen).toLocaleString()}` })) : null,
      h("p", { class: "muted" }, `${t(`resources.kind.${device.kind}`)} · ${device.scope === "personal"
        ? t("resources.personal_of", { name: device.owner?.display_name || device.owner?.username || "?" }) : t("resources.shared")}`),
      res ? h("div", { class: "resources-res" },
        h("div", { class: "physics-name" }, `${t(`resources.type.${res.kind}`)} · ${res.label}`),
        res.path ? h("code", null, res.path) : res.redacted ? h("p", { class: "muted" }, t("resources.path_hidden")) : null,
        ...metricLines(res).map((line) => h("p", { class: "muted" }, line))) : null,
      actions, privacy].filter(Boolean));
  }

  // ------------------------------------------------------- interaction
  let drag = null;
  canvas.addEventListener("pointerdown", (event) => {
    canvas.setPointerCapture?.(event.pointerId);
    drag = { x: event.clientX, y: event.clientY, pan: { ...state.pan }, moved: false };
  });
  canvas.addEventListener("pointermove", (event) => {
    const rect = canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    if (drag && event.buttons) {
      const dx = event.clientX - drag.x, dy = event.clientY - drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 3) drag.moved = true;
      state.pan = { x: drag.pan.x + dx, y: drag.pan.y + dy };
      draw();
      return;
    }
    const target = hit(px, py);
    const changed = target?.device !== state.hover?.device || target?.resource?.id !== state.hover?.resource?.id;
    state.hover = target;
    canvas.style.cursor = target ? "pointer" : "grab";
    showTooltip(target, px, py);
    if (changed) draw();
  });
  canvas.addEventListener("pointerleave", () => { state.hover = null; showTooltip(null); draw(); });
  canvas.addEventListener("pointerup", (event) => {
    const moved = drag?.moved;
    drag = null;
    if (moved) return;
    const rect = canvas.getBoundingClientRect();
    const target = hit(event.clientX - rect.left, event.clientY - rect.top);
    state.selected = target ? { device: target.device, resource: target.resource } : null;
    renderDetails();
    draw();
  });
  canvas.addEventListener("wheel", (event) => {
    event.preventDefault();
    const rect = canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const before = toWorld(px, py);
    state.zoom = Math.max(0.2, Math.min(2.5, state.zoom * Math.exp(-event.deltaY * 0.0015)));
    state.pan = { x: px - before.x * state.zoom, y: py - before.y * state.zoom };
    draw();
  }, { passive: false });
  const resize = new ResizeObserver(() => { if (state.model) draw(); });
  resize.observe(stage);

  // Live refresh while the panel is on screen; data updates whenever
  // someone probes (GPU board, Check now) because the endpoint reads the
  // last probe.
  const tick = () => {
    if (!panel.isConnected) { clearInterval(state.timer); resize.disconnect(); return; }
    if (document.visibilityState === "visible") load();
  };
  state.timer = setInterval(tick, REFRESH_MS);
  setTimeout(load, 0);
  panel.__resources = state; // for QA scripts
  return panel;
}
