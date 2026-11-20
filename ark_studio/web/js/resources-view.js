// Execute › Resources: a node graph of devices (ARK server, GPU servers,
// members' machines, phones, git hosts), the resources on each device and
// how devices connect.  Data: GET /api/resources/graph (read-only, never
// probes).  Layout and curves come from node-graph-layout.js.
//
// Controls follow the Node graph (graph.js): the same toolbar (read / edit
// mode, zoom, fit, ? help), keys (resources-model.js resourcesKeyAction),
// mouse (left-drag box select, right-drag pan, wheel zoom, Ctrl / Shift +
// wheel pan, right-click menu, drag a card to move it) and selection /
// details behaviour (read mode: a click opens the details panel; edit mode:
// a click only selects).  Each card's details fold like Mind's cards.
import { h, clear } from "./dom.js";
import { t, hasKey } from "./i18n.js";
import { icon } from "./icons.js";
import { api, cachedThenFresh, peekCached } from "./api.js";
import { openContextMenu } from "./context-menu.js";
import { layeredLayout, routeNodeEdges } from "./node-graph-layout.js";
import {
  RESOURCE_TYPES, buildResourcesModel, utilisation, ageSeconds, agoKey, rowBox, CARD_METRICS,
  usageTone, formatBytes, collapseToggleBox, setCollapsed, toggleCollapsed, collapsibleIds,
  resourcesKeyAction, nearestInDirection, cardsInBox, foldTargets,
  rowKind, gpuDetail, cpuDetail, storageDetail, formatMb, formatUsedOf, mergeProbeDetails, separateCards,
} from "./resources-model.js";
import { sizeView, formatSize, graphHostId, isLocalPath } from "./location-size-model.js";
import { sizeBadge, refreshLocationSize } from "./location-size.js";

const REFRESH_MS = 30000;
const FILTER_KEY = "ark.resourcesGraph.filters";
const COLLAPSED_KEY = "ark.resourcesGraph.collapsed";
const POSITIONS_KEY = "ark.resourcesGraph.positions";
const MODE_KEY = "ark.graphMode.resources";
// The Node graph's camera limits (graph.js zoomBy / _wheel / fit).
const ZOOM_MIN = 0.18, ZOOM_MAX = 3.2, FIT_MAX = 1.35, FIT_FILL = 0.8;

const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));

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
const TONE_COLOR = (pal, tone) => ({ ok: pal.ok, warn: pal.warn, danger: pal.danger }[tone] || pal.accent);

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

function loadJSON(key, fallback) {
  try { return JSON.parse(localStorage.getItem(key) || "null") ?? fallback; } catch { return fallback; }
}

function saveJSON(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* storage unavailable */ }
}

export function resourcesGraphPanel(ctx) {
  const filters = loadFilters();
  const savedMode = (() => { try { return localStorage.getItem(MODE_KEY); } catch { return null; } })();
  const state = { payload: null, model: null, layout: null, routes: null, zoom: 1, pan: { x: 0, y: 0 },
    hover: null, selected: null, selectedIds: new Set(), detailsOpen: false, fitted: false, timer: 0,
    loading: false, error: "", mode: savedMode === "edit" ? "edit" : "read",
    collapsed: new Set(loadJSON(COLLAPSED_KEY, [])),
    positions: new Map(Object.entries(loadJSON(POSITIONS_KEY, {}))),
    selectBox: null, anim: 0 };
  const canvas = h("canvas", { class: "resources-canvas", "aria-label": t("resources.title") });
  const tooltip = h("div", { class: "graph-tooltip hidden resources-tooltip" });
  const details = h("aside", { class: "resources-details hidden", "aria-live": "polite" });
  const helpEl = h("div", { class: "graph-help hidden resources-help", role: "dialog", "aria-label": t("graph.help.title") });
  const filterBar = h("div", { class: "resources-filters", role: "group", "aria-label": t("resources.filter") });
  const status = h("span", { class: "resources-status muted" });
  const unknowns = h("details", { class: "resources-unknowns" });
  const tool = (title, content, onClick, extra = {}) => h("button", {
    type: "button", title, "aria-label": title, onclick: onClick, ...extra,
  }, content);
  const modeReadBtn = tool(t("graph.mode.read_title"), t("graph.mode.read"), () => setMode("read"), { class: "graph-mode-btn" });
  const modeEditBtn = tool(t("graph.mode.edit_title"), t("graph.mode.edit"), () => setMode("edit"), { class: "graph-mode-btn" });
  const fullscreenBtn = tool(t("graph.fullscreen"), icon("expand", "sm"), () => toggleFullscreen(),
    { "data-resources-tool": "fullscreen" });
  const tools = h("div", { class: "graph-tools resources-tools", "data-resources-tools": "" },
    h("div", { class: "graph-mode" }, modeReadBtn, modeEditBtn),
    h("span", { class: "sep" }),
    tool(t("resources.expand_all_title"), "+", () => foldAll(false), { "data-resources-tool": "expand-all", class: "resources-fold-btn" }),
    tool(t("resources.collapse_all_title"), "−", () => foldAll(true), { "data-resources-tool": "collapse-all", class: "resources-fold-btn" }),
    tool(t("resources.reset_layout"), icon("refresh", "sm"), () => resetLayout(), { "data-resources-tool": "reset-layout" }),
    h("span", { class: "sep" }),
    tool(t("graph.zoom_in"), icon("zoomIn", "sm"), () => zoomBy(1.2), { "data-resources-tool": "zoom-in" }),
    tool(t("graph.zoom_out"), icon("zoomOut", "sm"), () => zoomBy(1 / 1.2), { "data-resources-tool": "zoom-out" }),
    tool(t("graph.fit"), icon("fit", "sm"), () => fit({ animate: true }), { "data-resources-tool": "fit" }),
    fullscreenBtn,
    tool(t("graph.help.title"), "?", () => toggleHelp(), { class: "graph-help-btn", "aria-expanded": "false" }));
  const stage = h("div", { class: "resources-stage", tabindex: "0", "data-mode": state.mode },
    canvas, tools, helpEl, tooltip, details);
  const refreshBtn = h("button", { class: "ghost-btn sm", type: "button", onclick: () => load() },
    icon("refresh", "sm"), t("resources.refresh"));
  const panel = h("section", { class: "panel resources-panel", "data-resources-graph": "" },
    h("div", { class: "panel-head" },
      h("div", null, h("h3", null, t("resources.title")), h("p", { class: "muted resources-lead" }, t("resources.lead"))),
      h("div", { class: "row tight" }, status, refreshBtn)),
    filterBar, stage, legend(), unknowns);

  // ------------------------------------------------------------- data
  async function load() {
    if (state.loading) return;
    state.loading = true;
    status.textContent = t("resources.loading");
    try {
      // Power, processes and jobs per GPU come from the GPU board's last
      // dashboard when it is the same probe (never requested from here:
      // the dashboard probes).
      const dashboard = await peekCached("/api/gpu/dashboard").catch(() => null);
      await cachedThenFresh("/api/resources/graph", (options) => api.resourcesGraph(options), (payload) => {
        state.payload = mergeProbeDetails(payload, dashboard);
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

  // relayout: a fresh layered layout (first load, filters, Reset layout);
  // otherwise cards keep their place (top edge) and only new ones are placed.
  function rebuild({ relayout = false } = {}) {
    if (!state.payload) return;
    const previous = relayout || !state.fitted ? null : state.layout;
    state.model = buildResourcesModel(state.payload, { ...filters, collapsed: state.collapsed });
    const nodes = state.model.cards.map((card) => ({ id: card.id, w: card.w, h: card.h }));
    const edges = state.model.links.map((link) => ({ id: link.id, source: link.source, target: link.target }));
    // Pack unconnected devices to suit the stage's shape.
    const { w, h: hh } = size();
    const { positions, waypoints } = layeredLayout(nodes, edges, { maxLayer: 12, layerGap: 90, aspect: Math.max(0.6, Math.min(2, w / hh)) });
    let pristine = true;
    state.layout = new Map(state.model.cards.map((card) => {
      const old = previous?.get(card.id);
      const manual = state.positions.get(card.id);
      let pos = positions.get(card.id);
      if (old) {
        const fresh = positions.get(card.id);
        pos = { x: old.x, y: old.y - old.h / 2 + card.h / 2 };
        if (!fresh || fresh.x !== pos.x || fresh.y !== pos.y) pristine = false;
      } else if (manual && Number.isFinite(manual.x) && Number.isFinite(manual.top)) {
        pos = { x: manual.x, y: manual.top + card.h / 2 };
        pristine = false;
      }
      return [card.id, { ...card, ...pos }];
    }));
    // Hand-placed cards and changed heights can overlap: push them apart.
    const pinned = new Set([...state.layout.keys()].filter((id) => state.positions.has(id)));
    if (pinned.size || !pristine) {
      const separated = separateCards([...state.layout.values()], pinned);
      for (const [id, pos] of separated) {
        const card = state.layout.get(id);
        if (card.x !== pos.x || card.y !== pos.y) { state.layout.set(id, { ...card, ...pos }); pristine = false; }
      }
    }
    state.waypoints = pristine ? waypoints : null;
    reroute();
    renderFilters();
    renderUnknowns();
    if (!state.fitted) { fit(); state.fitted = true; }
    for (const id of [...state.selectedIds]) if (!state.layout.has(id)) state.selectedIds.delete(id);
    if (state.selected && !state.layout.has(state.selected.device)) state.selected = null;
    if (state.selected?.resource) {
      const fresh = state.layout.get(state.selected.device).rows.find((row) => row.id === state.selected.resource.id);
      state.selected = { device: state.selected.device, resource: fresh || null };
    }
    renderDetails();
    draw();
  }

  function reroute() {
    const edges = state.model.links.map((link) => ({ id: link.id, source: link.source, target: link.target }));
    state.routes = routeNodeEdges([...state.layout.values()], edges, state.waypoints ? { waypoints: state.waypoints } : {});
  }

  function renderFilters() {
    clear(filterBar);
    const counts = state.model?.counts.types || {};
    for (const type of RESOURCE_TYPES.filter((kind) => counts[kind])) {
      const on = filters.types.has(type);
      filterBar.appendChild(h("button", {
        class: `type-chip${on ? " on" : ""}`, type: "button", "aria-pressed": String(on), "data-resource-type": type,
        onclick: () => { if (on) filters.types.delete(type); else filters.types.add(type); saveFilters(filters); state.fitted = false; rebuild({ relayout: true }); },
      }, t(`resources.type.${type}`), h("span", { class: "n" }, String(counts[type]))));
    }
    const mine = h("label", { class: "resources-mine" },
      h("input", { type: "checkbox", checked: filters.mineOnly ? true : undefined,
        onchange: (event) => { filters.mineOnly = event.target.checked; saveFilters(filters); state.fitted = false; rebuild({ relayout: true }); } }),
      t("resources.mine_only"));
    filterBar.appendChild(mine);
    if (filters.types.size || filters.mineOnly) {
      filterBar.appendChild(h("button", { class: "ghost-btn sm", type: "button",
        onclick: () => { filters.types.clear(); filters.mineOnly = false; saveFilters(filters); state.fitted = false; rebuild({ relayout: true }); } },
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
      swatch("sw usage", t("resources.legend.usage")),
      swatch("dot online", t("resources.state.online")), swatch("dot stale", t("resources.state.stale")),
      swatch("dot offline", t("resources.state.offline")), swatch("dot unknown", t("resources.state.unknown")),
      swatch("line ssh", t("resources.link.ssh")), swatch("line git", t("resources.link.git-remote")),
      swatch("line client", t("resources.link.client")), swatch("line dashed", t("resources.legend.dashed")),
      swatch("card personal", t("resources.legend.personal")));
  }

  // ------------------------------------------------------- mode, help
  function setMode(mode) {
    state.mode = mode === "edit" ? "edit" : "read";
    stage.dataset.mode = state.mode;
    modeReadBtn.classList.toggle("on", state.mode === "read");
    modeEditBtn.classList.toggle("on", state.mode === "edit");
    try { localStorage.setItem(MODE_KEY, state.mode); } catch { /* ignore */ }
    if (!helpEl.classList.contains("hidden")) renderHelp();
    draw();
  }

  function toggleHelp(force = null) {
    const show = force === null ? helpEl.classList.contains("hidden") : Boolean(force);
    if (show) renderHelp();
    helpEl.classList.toggle("hidden", !show);
    tools.querySelector(".graph-help-btn")?.setAttribute("aria-expanded", String(show));
  }

  function renderHelp() {
    clear(helpEl);
    const row = (keys, text) => h("div", { class: "graph-help-row" }, h("kbd", null, keys), h("span", null, text));
    const readMode = state.mode === "read";
    helpEl.append(
      h("div", { class: "graph-help-head" },
        h("b", null, t(readMode ? "graph.help.title_read" : "graph.help.title_edit")),
        h("button", { class: "icon-btn", type: "button", title: t("action.close"), onclick: () => toggleHelp(false) }, icon("x", "sm"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.navigation")),
        row("← → ↑ ↓", t("graph.help.move_selection")),
        row("R / E", t("graph.help.read_edit_mode")),
        row("F", t("graph.help.fit_graph")),
        row("?", t("graph.help.this_help")),
        row("Enter", t("resources.help.open_details")),
        row("Esc", t("graph.help.clear_selection"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.mouse")),
        row(t("graph.help.left_click"), t(readMode ? "resources.help.click_read" : "graph.help.select_only")),
        row(t("resources.help.mod_click"), t("resources.help.toggle_selection")),
        row(t("graph.help.left_drag"), t("graph.help.box_select")),
        row(t("resources.help.drag_card"), t("resources.help.move_card")),
        row(t("resources.help.dblclick"), t("resources.help.focus_card")),
        row(t("graph.help.right_drag"), t("graph.help.pan")),
        row(t("graph.help.wheel"), t("graph.help.zoom")),
        row(t("graph.help.ctrl_wheel"), t("graph.help.pan_vertical")),
        row(t("graph.help.shift_wheel"), t("graph.help.pan_horizontal")),
        row(t("graph.help.right_click"), t("graph.help.context_menu"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("resources.help.fold")),
        row("+ / =", t("resources.help.expand_one")),
        row("-", t("resources.help.collapse_one")),
        row("* / /", t("resources.help.fold_all")),
        row(t("resources.help.toggle_key"), t("resources.help.fold_toggle")),
        row("Ctrl+A", t("graph.help.select_visible"))),
      h("div", { class: "graph-help-note" }, t(readMode ? "resources.help.read_note" : "resources.help.edit_note")),
      h("div", { class: "graph-help-note" }, t("graph.help.note_gesture")));
  }

  // ----------------------------------------------------------- camera
  function size() {
    const rect = stage.getBoundingClientRect();
    return { w: Math.max(200, rect.width), h: Math.max(200, rect.height) };
  }

  function bounds(cards) {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const card of cards) {
      x0 = Math.min(x0, card.x - card.w / 2); x1 = Math.max(x1, card.x + card.w / 2);
      y0 = Math.min(y0, card.y - card.h / 2); y1 = Math.max(y1, card.y + card.h / 2);
    }
    return { x0, y0, x1, y1 };
  }

  function fit({ animate = false } = {}) {
    if (!state.layout?.size) return;
    const { x0, y0, x1, y1 } = bounds(state.layout.values());
    const { w, h: hh } = size();
    const zoom = clamp(Math.min((w * FIT_FILL) / Math.max(1, x1 - x0), (hh * FIT_FILL) / Math.max(1, y1 - y0)), ZOOM_MIN, FIT_MAX);
    camera(zoom, { x: w / 2 - (x0 + x1) / 2 * zoom, y: hh / 2 - (y0 + y1) / 2 * zoom }, animate);
  }

  function focusCard(id) {
    const card = state.layout?.get(id);
    if (!card) return;
    const { w, h: hh } = size();
    camera(state.zoom, { x: w / 2 - card.x * state.zoom, y: hh / 2 - card.y * state.zoom }, true);
  }

  function camera(zoom, pan, animate) {
    cancelAnimationFrame(state.anim);
    if (!animate || matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      state.zoom = zoom; state.pan = pan; draw(); return;
    }
    const from = { zoom: state.zoom, pan: { ...state.pan } };
    const started = performance.now();
    const step = (now) => {
      const k = Math.min(1, (now - started) / 260);
      const e = 1 - (1 - k) ** 3;
      state.zoom = from.zoom + (zoom - from.zoom) * e;
      state.pan = { x: from.pan.x + (pan.x - from.pan.x) * e, y: from.pan.y + (pan.y - from.pan.y) * e };
      draw();
      if (k < 1) state.anim = requestAnimationFrame(step);
    };
    state.anim = requestAnimationFrame(step);
  }

  function zoomBy(factor) {
    const { w, h: hh } = size();
    zoomAt(w / 2, hh / 2, state.zoom * factor);
  }

  function zoomAt(px, py, target) {
    cancelAnimationFrame(state.anim);
    const before = toWorld(px, py);
    state.zoom = clamp(target, ZOOM_MIN, ZOOM_MAX);
    state.pan = { x: px - before.x * state.zoom, y: py - before.y * state.zoom };
    draw();
  }


  // Full screen for the whole panel; the same button (Lucide expand/shrink)
  // switches back, and Esc exits as usual.
  function toggleFullscreen() {
    if (document.fullscreenElement === panel) document.exitFullscreen?.();
    else panel.requestFullscreen?.().catch((err) => ctx.toast?.(err.message, "err"));
  }

  const onFullscreen = () => {
    const on = document.fullscreenElement === panel;
    panel.classList.toggle("fullscreen", on);
    const label = t(on ? "graph.exit_fullscreen" : "graph.fullscreen");
    fullscreenBtn.title = label;
    fullscreenBtn.setAttribute("aria-label", label);
    fullscreenBtn.replaceChildren(icon(on ? "shrink" : "expand", "sm"));
    requestAnimationFrame(() => { draw(); if (on) stage.focus({ preventScroll: true }); });
  };
  document.addEventListener("fullscreenchange", onFullscreen);

  const toScreen = (x, y) => ({ x: x * state.zoom + state.pan.x, y: y * state.zoom + state.pan.y });
  const toWorld = (x, y) => ({ x: (x - state.pan.x) / state.zoom, y: (y - state.pan.y) / state.zoom });

  // -------------------------------------------------- selection, fold
  function select(ids, { additive = false, resource = null, primary = null } = {}) {
    if (!additive) state.selectedIds = new Set();
    for (const id of ids) {
      if (additive && state.selectedIds.has(id)) state.selectedIds.delete(id);
      else state.selectedIds.add(id);
    }
    const last = primary && state.selectedIds.has(primary) ? primary : [...state.selectedIds].pop();
    state.selected = last ? { device: last, resource: last === primary ? resource : null } : null;
  }

  function openDetails(open = true) {
    state.detailsOpen = open && Boolean(state.selected);
    renderDetails();
  }

  // Every fold/expand changes card heights: lay the cards out again (hand-
  // placed cards keep their place) so nothing overlaps; the view is not
  // re-fitted.
  function applyFold(next) {
    state.collapsed = next;
    saveJSON(COLLAPSED_KEY, [...next]);
    rebuild({ relayout: true });
  }

  // Mind's semantics: the selected cards; with nothing selected the focused
  // card (under the pointer); with neither, nothing to do.
  function foldSelected(collapse) {
    const ids = foldTargets(state.model?.cards, [...state.selectedIds], state.hover?.device || null);
    if (!ids.length) return;
    applyFold(setCollapsed(state.collapsed, ids, collapse));
  }

  function foldAll(collapse) {
    applyFold(setCollapsed(state.collapsed, collapsibleIds(state.model?.cards), collapse));
  }

  function toggleFold(id) {
    applyFold(toggleCollapsed(state.collapsed, id, [...state.selectedIds]));
  }

  function resetLayout() {
    state.positions = new Map();
    saveJSON(POSITIONS_KEY, {});
    state.fitted = false;
    rebuild({ relayout: true });
  }

  function savePositions(ids) {
    for (const id of ids) {
      const card = state.layout.get(id);
      if (card) state.positions.set(id, { x: Math.round(card.x), top: Math.round(card.y - card.h / 2) });
    }
    saveJSON(POSITIONS_KEY, Object.fromEntries(state.positions));
  }

  // ------------------------------------------------------------- draw
  function draw() {
    const ctx2 = canvas.getContext("2d");
    if (!ctx2) return;
    const { w, h: hh } = size();
    const dpr = Math.min(2.5, window.devicePixelRatio || 1);
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(hh * dpr)) {
      canvas.width = Math.round(w * dpr); canvas.height = Math.round(hh * dpr);
      canvas.style.width = `${w}px`; canvas.style.height = `${hh}px`;
    }
    ctx2.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx2.clearRect(0, 0, w, hh);
    if (!state.model) return;
    const pal = palette();
    if (!state.model.cards.length) {
      ctx2.fillStyle = pal.sub;
      ctx2.font = "13px system-ui, 'Segoe UI'";
      ctx2.textAlign = "center";
      ctx2.fillText(t(state.payload?.devices?.length ? "resources.empty_filtered" : "resources.empty"), w / 2, hh / 2);
      return;
    }
    ctx2.save();
    ctx2.translate(state.pan.x, state.pan.y);
    ctx2.scale(state.zoom, state.zoom);
    drawLinks(ctx2, pal);
    for (const card of state.layout.values()) drawCard(ctx2, pal, card);
    ctx2.restore();
    if (state.selectBox?.moved) {
      const b = state.selectBox;
      ctx2.save();
      ctx2.fillStyle = "rgba(59,130,246,.08)";
      ctx2.strokeStyle = pal.accent;
      ctx2.setLineDash([4, 3]);
      ctx2.fillRect(Math.min(b.x1, b.x2), Math.min(b.y1, b.y2), Math.abs(b.x2 - b.x1), Math.abs(b.y2 - b.y1));
      ctx2.strokeRect(Math.min(b.x1, b.x2), Math.min(b.y1, b.y2), Math.abs(b.x2 - b.x1), Math.abs(b.y2 - b.y1));
      ctx2.restore();
    }
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
    const primary = state.selected?.device === card.id;
    const selected = primary || state.selectedIds.has(card.id);
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
    if (card.personal && !selected) ctx.setLineDash([5, 4]);
    ctx.strokeStyle = selected ? pal.accent : card.personal ? pal.accent : pal.line;
    ctx.lineWidth = primary ? 2.4 : selected ? 1.8 : card.personal ? 1.4 : 1.1;
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
    // Header: status dot, title, fold toggle, owner, kind / scope / state.
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
    const reserve = (owner ? 30 : 0) + (card.collapsible ? 30 : 0);
    ctx.fillText(ellipsis(ctx, device.label, card.w - 46 - reserve), x + 28, y + 18);
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
    if (card.collapsible) drawFoldToggle(ctx, pal, card, x, y);
    ctx.textAlign = "left";
    ctx.font = "10.5px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    ctx.fillStyle = pal.sub;
    const kind = t(`resources.kind.${device.kind}`);
    const scope = card.personal ? t("resources.personal_of", { name: owner || "?" }) : t("resources.shared");
    const stateText = stateName === "online" ? t("resources.state.online")
      : device.status?.last_seen ? t("resources.last_seen", { when: ago(device.status.last_seen) })
        : t(`resources.state.${stateName}`);
    const folded = card.collapsed ? ` · ${t("resources.folded", { n: card.hiddenCount })}` : "";
    ctx.fillText(ellipsis(ctx, `${kind} · ${scope} · ${stateText}${folded}`, card.w - 24), x + 12, y + 37);
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
        // Disk (the project folder's disk on the ARK server) or CPU, in the
        // usage colours (amber from 60%, red from 85%).
        const m = device.metrics || {};
        const disk = m.disk_pct != null;
        const pct = disk ? m.disk_pct : m.cpu_pct;
        usageBar(ctx, pal, x + 12, by, bw, 8, pct ?? 0, usageTone(pct));
        ctx.fillStyle = pal.sub;
        ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        const sizes = disk && m.disk_total_gb != null && m.disk_used_gb != null;
        const label = sizes ? t("resources.disk_summary", { sizes: formatUsedOf(m.disk_used_gb * 1e9, m.disk_total_gb * 1e9),
          pct: Math.round(m.disk_pct) })
          : disk ? t("resources.disk_used", { pct: m.disk_pct }) : t("resources.cpu_used", { pct: m.cpu_pct });
        ctx.fillText(ellipsis(ctx, label, bw), x + 12, by + 15);
      }
    }
    // Resource rows (the foldable details).
    card.rows.forEach((res, index) => {
      const box = rowBox(card, index);
      const rx = x + box.x, ry = y + box.y;
      const hovered = state.hover?.resource?.id === res.id;
      const picked = primary && state.selected?.resource?.id === res.id;
      roundRect(ctx, rx, ry, box.w, box.h, 5);
      ctx.fillStyle = hovered || picked ? pal.lane : "transparent";
      if (hovered || picked) ctx.fill();
      ctx.strokeStyle = res.metrics?.ours || picked ? pal.accent : pal.line;
      ctx.lineWidth = res.metrics?.ours || picked ? 1.4 : 0.8;
      ctx.stroke();
      const kind = rowKind(res);
      if (kind !== "line") {
        ctx.save();
        if (res.last_known) ctx.globalAlpha *= 0.75;
        drawDetailRow(ctx, pal, card, res, kind, rx, ry, box.w, box.h);
        ctx.restore();
        return;
      }
      ctx.font = "600 9px ui-monospace, Consolas, monospace";
      ctx.fillStyle = res.metrics?.ours || res.metrics?.data ? pal.accent : pal.sub;
      const tag = (res.metrics?.data ? t("resources.storage.data_tag") : t(`resources.type.${res.kind}`)).toUpperCase();
      ctx.fillText(ellipsis(ctx, tag, 58), rx + 6, ry + box.h / 2);
      const u = utilisation(res);
      const meterW = u.util !== null || u.mem !== null ? 70 : 0;
      const right = rowRight(res);
      ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      const rightW = right ? ctx.measureText(right).width + 8 : 0;
      ctx.font = `${res.last_known ? "italic " : ""}11px system-ui, 'Segoe UI', 'Microsoft YaHei'`;
      ctx.fillStyle = pal.ink;
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

  // "+" (folded: expand) / "−" (details shown: collapse) in the card header,
  // as on Mind's cards (graph.js) and the + / - keys.
  function drawFoldToggle(ctx, pal, card, x, y) {
    const box = collapseToggleBox(card);
    const bx = x + box.x, by = y + box.y;
    const hovered = state.hover?.device === card.id && state.hover?.toggle;
    roundRect(ctx, bx, by, box.w, box.h, 5);
    ctx.fillStyle = card.collapsed ? pal.accent : hovered ? pal.lane : "transparent";
    ctx.fill();
    ctx.strokeStyle = card.collapsed || hovered ? pal.accent : pal.line;
    ctx.lineWidth = 1;
    ctx.stroke();
    const cx = bx + box.w / 2, cy = by + box.h / 2;
    ctx.beginPath();
    ctx.moveTo(cx - 4.5, cy); ctx.lineTo(cx + 4.5, cy);
    if (card.collapsed) { ctx.moveTo(cx, cy - 4.5); ctx.lineTo(cx, cy + 4.5); }
    ctx.strokeStyle = card.collapsed ? pal.bg : hovered ? pal.ink : pal.sub;
    ctx.lineWidth = 1.8;
    ctx.lineCap = "round";
    ctx.stroke();
    ctx.lineCap = "butt";
  }

  // A usage bar: track, fill in the usage tone (60% / 85%), and our share
  // over it in the accent colour (GPU board style).
  function usageBar(ctx, pal, x, y, w, h, pct, tone, oursPct = null) {
    roundRect(ctx, x, y, w, h, h / 2);
    ctx.fillStyle = pal.lane;
    ctx.fill();
    if (pct === null || pct === undefined) return;
    const fill = (value, color) => {
      const width = w * Math.max(0, Math.min(100, value)) / 100;
      if (width <= 0) return;
      roundRect(ctx, x, y, Math.max(width, h), h, h / 2);
      ctx.fillStyle = color;
      ctx.fill();
    };
    fill(pct, TONE_COLOR(pal, tone));
    if (oursPct) fill(Math.min(oursPct, pct), pal.accent);
  }

  // One labelled bar line inside a detail row: LABEL [bar] value.
  function barLine(ctx, pal, rx, cy, w, label, pct, tone, value, oursPct = null, valueW = 84) {
    ctx.font = "600 8.5px ui-monospace, Consolas, monospace";
    ctx.fillStyle = pal.sub;
    ctx.textAlign = "left";
    ctx.fillText(label, rx + 8, cy);
    usageBar(ctx, pal, rx + 42, cy - 3, w - 42 - valueW - 8, 6, pct, tone, oursPct);
    ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    ctx.fillStyle = pal.ink;
    ctx.textAlign = "right";
    ctx.fillText(ellipsis(ctx, value, valueW), rx + w - 8, cy);
    ctx.textAlign = "left";
  }

  // Title line of a detail row: bold title, muted subtitle, right-hand meta.
  function titleLine(ctx, pal, rx, cy, w, title, sub, meta, accent = false) {
    ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    const metaW = meta ? Math.min(ctx.measureText(meta).width, w * 0.45) + 10 : 0;
    ctx.font = "650 11px system-ui, 'Segoe UI', 'Microsoft YaHei'";
    ctx.fillStyle = accent ? pal.accent : pal.ink;
    ctx.textAlign = "left";
    const shownTitle = ellipsis(ctx, title, w - 16 - metaW);
    ctx.fillText(shownTitle, rx + 8, cy);
    const tw = ctx.measureText(shownTitle).width;
    if (sub) {
      ctx.font = "10.5px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      ctx.fillStyle = pal.sub;
      const room = w - 16 - metaW - tw - 6;
      if (room > 24) ctx.fillText(ellipsis(ctx, sub, room), rx + 8 + tw + 6, cy);
    }
    if (meta) {
      ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      ctx.fillStyle = pal.sub;
      ctx.textAlign = "right";
      ctx.fillText(ellipsis(ctx, meta, w * 0.45), rx + w - 8, cy);
      ctx.textAlign = "left";
    }
  }

  function gpuMeta(d) {
    return [d.temperature !== null ? `${Math.round(d.temperature)}°C` : "", d.power !== null ? `${Math.round(d.power)} W` : ""] // i18n-ignore: units
      .filter(Boolean).join(" · ");
  }

  const pctText = (pct) => (pct === null || pct === undefined ? "—" : `${Math.round(pct)}%`);
  const memText = (used, total) => (total ? formatUsedOf((used ?? 0) * 2 ** 20, total * 2 ** 20) : "—");
  const storageText = (d) => (d.total !== null ? `${formatUsedOf(d.used, d.total)} · ${pctText(d.pct)}` : pctText(d.pct));

  function gpuExtraText(d) {
    return d.extra.map((item) => item.key === "jobs" ? t("resources.gpu.jobs", { jobs: item.jobs.join(", ") })
      : item.key === "owners" ? t("resources.gpu.owners", { owners: item.owners.join(", ") })
        : t("resources.gpu.processes", { n: item.n })).join(" · ");
  }

  // Expanded rows: one block per GPU, CPU & memory, one bar per mount.
  function drawDetailRow(ctx, pal, card, res, kind, rx, ry, w, h) {
    if (kind === "gpu") {
      const d = gpuDetail(res);
      titleLine(ctx, pal, rx, ry + 10, w, d.title, d.model, gpuMeta(d), d.ours);
      barLine(ctx, pal, rx, ry + 23, w, t("resources.bar.util"), d.util, d.utilTone, pctText(d.util));
      barLine(ctx, pal, rx, ry + 36, w, t("resources.bar.mem"), d.memPct, d.memTone, memText(d.memUsedMb, d.memTotalMb), d.oursPct);
      if (d.extra.length) {
        ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        ctx.fillStyle = d.ours ? pal.accent : pal.sub;
        ctx.fillText(ellipsis(ctx, gpuExtraText(d), w - 16), rx + 8, ry + 49);
      }
    } else if (kind === "cpu") {
      const d = cpuDetail(res);
      const meta = [d.load1 !== null ? t("resources.cpu.load", { load: d.load1 }) : "",
        d.cores ? t("resources.cpu.cores", { n: d.cores }) : ""].filter(Boolean).join(" · ");
      titleLine(ctx, pal, rx, ry + 10, w, res.label, "", meta);
      barLine(ctx, pal, rx, ry + 23, w, t("resources.bar.cpu"), d.cpu, d.cpuTone, pctText(d.cpu));
      barLine(ctx, pal, rx, ry + 36, w, t("resources.bar.ram"), d.ramPct, d.ramTone, memText(d.ramUsedMb, d.ramTotalMb));
    } else {
      const d = storageDetail(res, card.device);
      let lx = rx + 8;
      if (d.data) {
        ctx.font = "700 8.5px ui-monospace, Consolas, monospace";
        const tag = t("resources.storage.data_tag").toUpperCase();
        const tw = ctx.measureText(tag).width + 8;
        roundRect(ctx, lx, ry + 4, tw, 12, 3);
        ctx.fillStyle = pal.accent;
        ctx.fill();
        ctx.fillStyle = "#fff";
        ctx.fillText(tag, lx + 4, ry + 10.5);
        lx += tw + 6;
      }
      const sizes = storageText(d);
      ctx.font = "10px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      const sizesW = ctx.measureText(sizes).width;
      ctx.fillStyle = pal.sub;
      ctx.textAlign = "right";
      ctx.fillText(sizes, rx + w - 8, ry + 10);
      ctx.textAlign = "left";
      ctx.font = `${res.last_known ? "italic " : ""}600 11px system-ui, 'Segoe UI', 'Microsoft YaHei'`;
      ctx.fillStyle = d.data ? pal.accent : pal.ink;
      ctx.fillText(ellipsis(ctx, d.label, rx + w - 8 - sizesW - 8 - lx), lx, ry + 10);
      usageBar(ctx, pal, rx + 8, ry + 19, w - 16, 7, d.pct, d.tone);
    }
  }

  function rowRight(res) {
    const m = res.metrics || {};
    if (res.last_known) return t("resources.last_known");
    if (res.kind === "sessions") return t("resources.sessions_summary", { running: m.running ?? 0, mine: m.mine ?? 0 });
    if (res.redacted) return t("resources.path_hidden");
    if (m.storage) return t("resources.storage.used_of", { used: formatBytes(m.used_bytes), total: formatBytes(m.total_bytes) });
    if (res.kind === "gpu" && m.job) return m.job;
    if (res.kind === "compute" && m.gpus) return t("resources.gpus_n", { n: m.gpus });
    if (res.size && (res.size.kind || "dir") !== "missing" && res.size.bytes != null) {
      return `${res.size.capped ? "≥ " : ""}${formatSize(res.size.bytes)}`;
    }
    return "";
  }

  // A resource at a path on its device's file system (repository, workspace,
  // storage, database): its folder size is shown.
  const sizedPath = (res) => Boolean(res.path && !res.metrics?.storage && !res.redacted && isLocalPath(res.path));

  async function refreshSize(res, device, button) {
    button.disabled = true;
    try {
      const r = await refreshLocationSize({ resource: res.setup_key, path: res.path,
        hostId: graphHostId(res.device) ?? "", deviceLabel: device.label });
      ctx.toast?.(r.message, r.done || r.queued ? "ok" : "");
      if (r.done) await load();
      else if (r.queued) setTimeout(() => { load(); }, 4000);
    } catch (err) { ctx.toast?.(err.message, "err"); }
    finally { button.disabled = false; }
  }

  // One-line rows: two thin bars, utilisation and memory (GPU, CPU and
  // storage rows have their own detail layout, drawDetailRow).
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
      const tb = collapseToggleBox(card);
      const toggle = card.collapsible && p.x >= x + tb.x && p.x <= x + tb.x + tb.w && p.y >= y + tb.y && p.y <= y + tb.y + tb.h;
      const index = card.rows.findIndex((_, i) => {
        const box = rowBox(card, i);
        return p.y >= y + box.y && p.y <= y + box.y + box.h && p.x >= x + box.x && p.x <= x + box.x + box.w;
      });
      return { device: card.id, card, resource: index >= 0 ? card.rows[index] : null, header: p.y < y + CARD_METRICS.HEADER_H, toggle };
    }
    return null;
  }

  function showTooltip(target, px, py) {
    if (!target) { tooltip.classList.add("hidden"); return; }
    const device = target.card.device;
    const st = device.status || {};
    clear(tooltip);
    if (target.toggle) {
      tooltip.append(h("b", null, t(target.card.collapsed ? "resources.expand" : "resources.collapse")),
        h("div", null, t("resources.fold_hint")));
    } else if (target.resource) {
      const res = target.resource;
      tooltip.append(...[h("b", null, res.label),
        h("div", null, `${t(`resources.type.${res.kind}`)}${res.owner ? ` · ${res.owner}` : ""}`),
        res.path && !res.metrics?.storage ? h("div", { class: "mono" }, res.path) : res.redacted ? h("div", null, t("resources.path_hidden")) : null,
        sizedPath(res) ? h("div", { "data-resources-size": res.id }, `${t("size.label")}: ${sizeView(res.size, { t }).text}`) : null,
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
      if (m.temperature_c != null) lines.push(t("resources.metric.temperature", { c: Math.round(m.temperature_c) }));
      if (m.power_w != null) lines.push(t("resources.metric.power", { w: Math.round(m.power_w) }));
      if (m.job) lines.push(t("resources.metric.job", { job: m.job }));
      if (Array.isArray(m.processes) && m.processes.length) lines.push(t("resources.gpu.processes", { n: m.processes.length }));
    } else if (res.kind === "cpu") {
      if (m.cpu_pct != null) lines.push(t("resources.cpu_used", { pct: m.cpu_pct }));
      if (m.mem_total_mb) lines.push(t("resources.metric.ram", { used: Math.round(m.mem_used_mb / 1024), total: Math.round(m.mem_total_mb / 1024) }));
    } else if (res.kind === "sessions") {
      lines.push(t("resources.sessions_summary", { running: m.running ?? 0, mine: m.mine ?? 0 }));
    } else if (m.storage) {
      lines.push(t("resources.storage.usage", { used: formatBytes(m.used_bytes), total: formatBytes(m.total_bytes),
        pct: m.disk_pct ?? "?", free: formatBytes(m.free_bytes) }));
      if (m.data) lines.push(t("resources.storage.data_hint"));
    } else if (m.disk_pct != null) lines.push(t("resources.disk_used", { pct: m.disk_pct }));
    if (res.verified_at) lines.push(t("resources.verified", { when: ago(res.verified_at) }));
    return lines;
  }

  // ---------------------------------------------------------- details
  function renderDetails() {
    clear(details);
    const target = state.selected;
    const card = state.detailsOpen && target && state.layout?.get(target.device);
    if (!card) { details.classList.add("hidden"); return; }
    const device = card.device;
    const st = device.status || {};
    details.classList.remove("hidden");
    const actions = h("div", { class: "row tight wrap" });
    if (device.actions?.includes("check") && state.payload?.probe?.enabled) {
      actions.appendChild(h("button", { class: "btn sm", type: "button", "data-resources-check": device.id,
        onclick: (event) => checkNow(device, event.currentTarget) }, icon("refresh", "sm"), t("resources.check_now")));
    }
    if (device.kind === "gpu-server") {
      actions.appendChild(h("button", { class: "btn sm ghost", type: "button",
        onclick: () => ctx.navigateWorkspace?.("execute", "gpu") }, t("resources.open_gpu_board")));
    }
    if (card.collapsible) {
      actions.appendChild(h("button", { class: "btn sm ghost", type: "button", "data-resources-fold": device.id,
        onclick: () => toggleFold(device.id) }, t(card.collapsed ? "resources.expand" : "resources.collapse")));
    }
    const setupKey = target.resource?.setup_key || card.allRows.find((row) => row.setup_key)?.setup_key;
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
    const multi = state.selectedIds.size > 1 ? h("p", { class: "muted" }, t("resources.selected_n", { n: state.selectedIds.size })) : null;
    // The whole card, expanded or not: every GPU, CPU & memory, every mount.
    const section = (titleKey, rows) => (rows.length ? h("div", { class: "resources-res" },
      h("div", { class: "physics-name" }, t(titleKey)), ...rows.map((row) => detailBlock(row, device))) : null);
    const ofKind = (kind) => card.allRows.filter((row) => rowKind(row) === kind);
    const overview = res ? [] : [section("resources.gpus_title", ofKind("gpu")), section("resources.type.cpu", ofKind("cpu")),
      section("resources.storage.title", ofKind("storage"))];
    details.append(...[
      h("div", { class: "resources-details-head" },
        h("b", null, device.label),
        h("button", { class: "icon-btn", type: "button", "aria-label": t("action.close"),
          onclick: () => { openDetails(false); draw(); } }, icon("x", "sm"))),
      multi,
      h("div", { class: `resources-state ${st.state || "unknown"}` }, h("i"), t(`resources.state.${st.state || "unknown"}`)),
      h("p", { class: "muted" }, reasonText(st)),
      st.last_seen ? h("p", { class: "muted" }, t("resources.last_seen", { when: `${ago(st.last_seen)} · ${new Date(st.last_seen).toLocaleString()}` })) : null,
      h("p", { class: "muted" }, `${t(`resources.kind.${device.kind}`)} · ${device.scope === "personal"
        ? t("resources.personal_of", { name: device.owner?.display_name || device.owner?.username || "?" }) : t("resources.shared")}`),
      ...overview,
      res ? h("div", { class: "resources-res" },
        h("div", { class: "physics-name" }, `${t(`resources.type.${res.kind}`)} · ${res.label}`),
        rowKind(res) !== "line" ? detailBlock(res, device) : null,
        res.path && !res.metrics?.storage ? h("code", null, res.path) : res.redacted ? h("p", { class: "muted" }, t("resources.path_hidden")) : null,
        sizedPath(res) ? h("p", { class: "muted resources-size" }, sizeBadge(res.size, {
          onRefresh: res.setup_key ? (button) => refreshSize(res, device, button) : null })) : null,
        ...metricLines(res).map((line) => h("p", { class: "muted" }, line))) : null,
      actions, privacy].filter(Boolean));
  }

  // DOM version of a detail row for the details panel: bars in the usage
  // tones, plus everything the card has no room for (processes).
  function domBar(label, pct, tone, value, oursPct = null) {
    const width = (v) => `${Math.max(0, Math.min(100, Number(v) || 0))}%`;
    return h("div", { class: "resources-bar-row" },
      h("span", { class: "lbl" }, label),
      h("div", { class: "resources-bar", role: "meter", "aria-valuemin": "0", "aria-valuemax": "100",
        "aria-valuenow": pct === null || pct === undefined ? undefined : String(Math.round(pct)), "aria-label": label },
      pct === null || pct === undefined ? null : h("i", { class: `fill ${tone || ""}`.trim(), style: { width: width(pct) } }),
      oursPct ? h("i", { class: "ours", style: { width: width(Math.min(oursPct, pct ?? 0)) } }) : null),
      h("b", null, value));
  }

  function detailBlock(res, device) {
    const kind = rowKind(res);
    if (kind === "gpu") {
      const d = gpuDetail(res);
      const meta = gpuMeta(d);
      return h("div", { class: `resources-detail gpu${d.ours ? " ours" : ""}`, "data-resources-detail": res.id },
        h("div", { class: "resources-detail-head" }, h("b", null, d.title), d.model ? h("span", null, d.model) : null,
          meta ? h("em", null, meta) : null),
        domBar(t("resources.bar.util"), d.util, d.utilTone, pctText(d.util)),
        domBar(t("resources.bar.mem"), d.memPct, d.memTone, memText(d.memUsedMb, d.memTotalMb), d.oursPct),
        d.jobs.length ? h("p", { class: "muted" }, t("resources.gpu.jobs", { jobs: d.jobs.join(", ") })) : null,
        d.owners.length ? h("p", { class: "muted" }, t("resources.gpu.owners", { owners: d.owners.join(", ") })) : null,
        d.processes.length ? h("ul", { class: "resources-procs" }, d.processes.map((p) => h("li", null,
          [`PID ${p.pid ?? "?"}`, p.memory_mb !== null ? formatMb(p.memory_mb) : "", // i18n-ignore: process id
            p.sm_pct !== null ? t("resources.gpu.sm", { pct: Math.round(p.sm_pct) }) : "",
            p.job || "", p.owner || ""].filter(Boolean).join(" · ")))) : null);
    }
    if (kind === "cpu") {
      const d = cpuDetail(res);
      const meta = [d.load1 !== null ? t("resources.cpu.load", { load: d.load1 }) : "",
        d.cores ? t("resources.cpu.cores", { n: d.cores }) : ""].filter(Boolean).join(" · ");
      return h("div", { class: "resources-detail cpu", "data-resources-detail": res.id },
        h("div", { class: "resources-detail-head" }, h("b", null, res.label), meta ? h("em", null, meta) : null),
        domBar(t("resources.bar.cpu"), d.cpu, d.cpuTone, pctText(d.cpu)),
        domBar(t("resources.bar.ram"), d.ramPct, d.ramTone, memText(d.ramUsedMb, d.ramTotalMb)));
    }
    const d = storageDetail(res, device);
    const sizes = storageText(d);
    return h("div", { class: `resources-detail storage${d.data ? " data" : ""}`, "data-resources-detail": res.id },
      h("div", { class: "resources-detail-head" },
        d.data ? h("span", { class: "resources-data-tag" }, t("resources.storage.data_tag")) : null,
        h("b", { class: "mono" }, d.label), h("em", null, sizes)),
      domBar(t("resources.bar.disk"), d.pct, d.tone, d.free !== null ? t("resources.storage.free", { free: formatBytes(d.free) }) : ""));
  }

  async function checkNow(device, button) {
    if (button) button.disabled = true;
    try {
      await api.gpuProbe({ servers: [device.label] });
      ctx.toast?.(t("resources.checked"), "ok");
    } catch (err) { ctx.toast?.(err.message, "err"); }
    await load();
  }

  // ------------------------------------------------------ context menu
  function contextMenu(event, target) {
    const items = [];
    if (target) {
      if (!state.selectedIds.has(target.device)) select([target.device], { resource: target.resource, primary: target.device });
      const card = target.card;
      const many = state.selectedIds.size > 1;
      items.push({ label: t("resources.menu.open_details"), icon: "info", shortcut: "Enter",
        onClick: () => { select([target.device], { additive: false, resource: target.resource, primary: target.device }); openDetails(true); draw(); } });
      if (card.collapsible) {
        const folding = !card.collapsed;
        items.push({ label: t(many ? (folding ? "resources.collapse_selected" : "resources.expand_selected")
          : (folding ? "resources.collapse" : "resources.expand")), icon: folding ? "chevronRight" : "chevron",
        shortcut: folding ? "-" : "+", onClick: () => foldSelected(folding) });
      }
      if (card.device.actions?.includes("check") && state.payload?.probe?.enabled) {
        items.push({ label: t("resources.check_now"), icon: "refresh", onClick: () => checkNow(card.device) });
      }
      if (card.device.kind === "gpu-server") {
        items.push({ label: t("resources.open_gpu_board"), icon: "cpu", onClick: () => ctx.navigateWorkspace?.("execute", "gpu") });
      }
      items.push({ separator: true });
      items.push({ label: t("graph.help.select_visible"), icon: "layers", shortcut: "Ctrl+A", onClick: () => selectAll() });
    }
    items.push({ label: t("graph.menu.fit_graph"), icon: "fit", shortcut: "F", onClick: () => fit({ animate: true }) });
    if (!target) {
      items.push({ label: t("resources.expand_all"), icon: "chevron", onClick: () => foldAll(false) });
      items.push({ label: t("resources.collapse_all"), icon: "chevronRight", onClick: () => foldAll(true) });
      items.push({ label: t("resources.reset_layout"), icon: "refresh", onClick: () => resetLayout() });
      items.push({ separator: true });
      items.push({ label: t("graph.menu.shortcuts"), icon: "info", shortcut: "?", onClick: () => toggleHelp(true) });
      items.push({
        label: t(state.mode === "read" ? "graph.menu.switch_to_edit" : "graph.menu.switch_to_read"),
        icon: "settings", shortcut: state.mode === "read" ? "E" : "R",
        onClick: () => setMode(state.mode === "read" ? "edit" : "read"),
      });
    }
    renderDetails();
    draw();
    openContextMenu({ x: event.clientX, y: event.clientY, items });
  }

  function selectAll() {
    select(state.model?.cards.map((card) => card.id) || []);
    renderDetails();
    draw();
  }

  // ------------------------------------------------------- interaction
  let drag = null;
  const pointers = new Map();
  let pinch = null;
  const local = (event) => {
    const rect = canvas.getBoundingClientRect();
    return { px: event.clientX - rect.left, py: event.clientY - rect.top };
  };
  const touch = (event) => event.pointerType === "touch";

  canvas.addEventListener("pointerdown", (event) => {
    cancelAnimationFrame(state.anim);
    try { canvas.setPointerCapture(event.pointerId); } catch { /* the pointer is already gone */ }
    stage.focus({ preventScroll: true });
    const { px, py } = local(event);
    pointers.set(event.pointerId, { x: px, y: py });
    if (pointers.size >= 2) {
      // Two fingers: pinch to zoom, move the midpoint to pan.
      const [a, b] = [...pointers.values()];
      pinch = { distance: Math.max(1, Math.hypot(b.x - a.x, b.y - a.y)), midX: (a.x + b.x) / 2, midY: (a.y + b.y) / 2,
        zoom: state.zoom, pan: { ...state.pan } };
      drag = null; state.selectBox = null;
      return;
    }
    if (event.button === 2) {
      // Right-drag pans; a right click without movement opens the menu.
      event.preventDefault();
      drag = { pan: true, right: true, x: event.clientX, y: event.clientY, pan0: { ...state.pan }, moved: false };
      canvas.style.cursor = "grabbing";
      return;
    }
    if (event.button !== 0) return;
    const target = hit(px, py);
    if (target?.toggle) {
      if (!state.selectedIds.has(target.device)) select([target.device], { primary: target.device });
      toggleFold(target.device);
      return;
    }
    if (event.ctrlKey || event.metaKey || event.shiftKey) {
      if (target) {
        select([target.device], { additive: true, resource: target.resource, primary: target.device });
        renderDetails();
        draw();
        return;
      }
      state.selectBox = { x1: px, y1: py, x2: px, y2: py, moved: false, additive: event.ctrlKey || event.metaKey, contain: event.shiftKey };
      canvas.style.cursor = "crosshair";
      return;
    }
    if (target) {
      const keepMulti = state.selectedIds.has(target.device) && state.selectedIds.size > 1;
      const group = keepMulti ? [...state.selectedIds] : [target.device];
      drag = { target, keepMulti, x: event.clientX, y: event.clientY, moved: false,
        start: new Map(group.map((id) => [id, { x: state.layout.get(id).x, y: state.layout.get(id).y }])) };
      canvas.style.cursor = "grabbing";
    } else if (touch(event)) {
      drag = { pan: true, x: event.clientX, y: event.clientY, pan0: { ...state.pan }, moved: false };
    } else {
      state.selectBox = { x1: px, y1: py, x2: px, y2: py, moved: false, additive: false, contain: false };
      canvas.style.cursor = "crosshair";
    }
  });

  canvas.addEventListener("pointermove", (event) => {
    const { px, py } = local(event);
    if (pointers.has(event.pointerId)) pointers.set(event.pointerId, { x: px, y: py });
    if (pinch && pointers.size >= 2) {
      const [a, b] = [...pointers.values()];
      const zoom = clamp(pinch.zoom * Math.hypot(b.x - a.x, b.y - a.y) / pinch.distance, ZOOM_MIN, ZOOM_MAX);
      const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      const world = { x: (pinch.midX - pinch.pan.x) / pinch.zoom, y: (pinch.midY - pinch.pan.y) / pinch.zoom };
      state.zoom = zoom;
      state.pan = { x: mid.x - world.x * zoom, y: mid.y - world.y * zoom };
      draw();
      return;
    }
    if (state.selectBox) {
      state.selectBox.x2 = px; state.selectBox.y2 = py;
      if (Math.abs(px - state.selectBox.x1) + Math.abs(py - state.selectBox.y1) > 4) state.selectBox.moved = true;
      draw();
      return;
    }
    if (drag) {
      const dx = event.clientX - drag.x, dy = event.clientY - drag.y;
      if (Math.abs(dx) + Math.abs(dy) > 3) drag.moved = true;
      if (!drag.moved) return;
      if (drag.pan) {
        state.pan = { x: drag.pan0.x + dx, y: drag.pan0.y + dy };
      } else {
        for (const [id, start] of drag.start) {
          const card = state.layout.get(id);
          if (card) { card.x = start.x + dx / state.zoom; card.y = start.y + dy / state.zoom; }
        }
        state.waypoints = null;
        reroute();
        showTooltip(null);
      }
      draw();
      return;
    }
    const target = hit(px, py);
    const changed = target?.device !== state.hover?.device || target?.resource?.id !== state.hover?.resource?.id
      || Boolean(target?.toggle) !== Boolean(state.hover?.toggle);
    state.hover = target;
    canvas.style.cursor = target ? "pointer" : "default";
    showTooltip(target, px, py);
    if (changed) draw();
  });

  canvas.addEventListener("pointerleave", () => {
    if (drag || state.selectBox) return;
    state.hover = null; showTooltip(null); draw();
  });

  const pointerUp = (event) => {
    pointers.delete(event.pointerId);
    if (pinch) { if (pointers.size < 2) pinch = null; drag = null; return; }
    canvas.style.cursor = "default";
    if (state.selectBox) {
      const box = state.selectBox;
      state.selectBox = null;
      if (box.moved) {
        const ids = cardsInBox([...state.layout.values()], box, toScreen, box.contain);
        select(ids, { additive: box.additive });
      } else if (!box.additive) {
        select([]);
        if (state.mode === "read") state.detailsOpen = false;
      }
      renderDetails();
      draw();
      return;
    }
    const current = drag;
    drag = null;
    if (!current) return;
    if (current.right) {
      const { px, py } = local(event);
      if (!current.moved && event.type !== "pointercancel") contextMenu(event, hit(px, py));
      return;
    }
    if (current.pan) {
      if (!current.moved) { select([]); if (state.mode === "read") state.detailsOpen = false; renderDetails(); draw(); }
      return;
    }
    const { target } = current;
    if (current.moved) {
      savePositions([...current.start.keys()]);
      return;
    }
    if (!current.keepMulti) select([target.device], { resource: target.resource, primary: target.device });
    else state.selected = { device: target.device, resource: target.resource };
    // Read mode opens the details; edit mode only selects (the panel follows
    // the selection once open), as the Node graph's inspector does.
    if (state.mode === "read") state.detailsOpen = true;
    renderDetails();
    draw();
  };
  canvas.addEventListener("pointerup", pointerUp);
  canvas.addEventListener("pointercancel", pointerUp);
  canvas.addEventListener("contextmenu", (event) => event.preventDefault());
  canvas.addEventListener("dblclick", (event) => {
    const { px, py } = local(event);
    const target = hit(px, py);
    if (!target || target.toggle) return;
    select([target.device], { resource: target.resource, primary: target.device });
    openDetails(true);
    focusCard(target.device);
  });

  canvas.addEventListener("wheel", (event) => {
    event.preventDefault();
    cancelAnimationFrame(state.anim);
    const { px, py } = local(event);
    const unit = event.deltaMode === 1 ? 18 : event.deltaMode === 2 ? size().h : 1;
    const dy = (event.deltaY || 0) * unit;
    if (event.ctrlKey || event.metaKey) state.pan = { x: state.pan.x, y: state.pan.y - dy };
    else if (event.shiftKey) state.pan = { x: state.pan.x - dy, y: state.pan.y };
    else { zoomAt(px, py, state.zoom * Math.exp(-dy * 0.0012)); return; }
    draw();
  }, { passive: false });

  stage.addEventListener("keydown", (event) => {
    if (details.contains(event.target) || helpEl.contains(event.target)) return;
    if (tools.contains(event.target) && (event.key === "Enter" || event.key === " ")) return;
    const action = resourcesKeyAction(event, { mode: state.mode, helpOpen: !helpEl.classList.contains("hidden") });
    if (!action) return;
    event.preventDefault();
    const cards = state.model?.cards || [];
    switch (action) {
      case "help": toggleHelp(); break;
      case "close-help": toggleHelp(false); break;
      case "clear": select([]); state.detailsOpen = false; renderDetails(); draw(); break;
      case "fit": fit({ animate: true }); break;
      case "mode-read": setMode("read"); break;
      case "mode-edit": setMode("edit"); break;
      case "select-all": selectAll(); break;
      case "expand": foldSelected(false); break;
      case "collapse": foldSelected(true); break;
      case "open": if (state.selected) { openDetails(true); draw(); } break;
      case "none": break;
      default:
        if (action.startsWith("nav-") && cards.length) {
          const next = nearestInDirection([...state.layout.values()], state.selected?.device, action.slice(4));
          if (next) {
            select([next], { primary: next });
            renderDetails();
            draw();
          }
        }
    }
  });

  const resize = new ResizeObserver(() => { if (state.model) draw(); });
  resize.observe(stage);

  // Live refresh while the panel is on screen; data updates whenever
  // someone probes (GPU board, Check now) because the endpoint reads the
  // last probe.
  const tick = () => {
    if (!panel.isConnected) {
      clearInterval(state.timer); resize.disconnect();
      document.removeEventListener("fullscreenchange", onFullscreen);
      return;
    }
    if (document.visibilityState === "visible") load();
  };
  state.timer = setInterval(tick, REFRESH_MS);
  setMode(state.mode);
  setTimeout(load, 0);
  panel.__resources = state; // for QA scripts
  return panel;
}
