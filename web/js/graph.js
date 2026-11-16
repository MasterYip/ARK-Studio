import { h, clear, typeColor, statusColor, clamp, copyText } from "./dom.js";
import { t, typeLabel, statusLabel, relLabel } from "./i18n.js";
import { icon, drawStatusIcon } from "./icons.js";
import { normalizeGraphPhysics, GRAPH_DEFAULTS } from "./physics.js";
import { getMindSettings } from "./mind-settings.js";
import { getTagShortcuts, shortcutMatches } from "./shortcuts.js";
import { MARKERS, markersFor, markerArc } from "./markers.js";
import { openContextMenu } from "./context-menu.js";
import { assignMenuItem } from "./assign-menu.js";
import { exportBundleMenuItem } from "./export-menu.js";
import { historyMenuItem } from "./node-history.js";
import { authenticatedUrl } from "./api.js";
import { applyRepulsion } from "./graph-force.js";
import {
  stepForce, forceSettleStep, FORCE_SETTLE, forceNodeRadius, drawForceNode, focusNeighborhood, arrowSize, addArrowhead, arrowInset,
  paintArrowheads, arrowResolver, normalizeArrowSettings, ARROW_HEADS,
} from "./graph-core.js";
import { layeredLayout, placeNewNodes, routeNodeEdges, cubicAt, NODE_W, NODE_H } from "./node-graph-layout.js";
import { cardMetrics, applyCardMetrics, cleanCardWidth, CARD_KINDS, THUMB } from "./node-card.js";
import { SpriteCache, spriteBucket } from "./graph-sprites.js";
import { mindHierarchy, mindLayout, TREE_GAP } from "./mind-layout.js";

const POS_CACHE = new Map(); // graphId -> Map(nodeId -> {x,y})
const GRAPH_INSTANCES = new Set();
const GRAPH_CAMERAS = new Map();
let ACTIVE_PHYSICS = normalizeGraphPhysics(GRAPH_DEFAULTS);
let GRAPH_SEQ = 0;
let GRAPH_CLIPBOARD = null;
const ALL_RELATIONS = [
  "part_of", "derived_from", "supports", "contradicts", "depends_on", "blocks",
  "cites", "inspires", "targets", "produces", "validates", "assigned_to",
  "runs_on", "supersedes", "uses", "relates_to", "unset",
];
const MIND_IMAGE_CACHE = new Map();
const ARROW_BUDGET = 2500;
// Cards are drawn in world units (see _paintCard); fonts are world px.
const CARD_TITLE_FONT = "600 12px system-ui, 'Segoe UI', 'Microsoft YaHei'";
const CARD_META_FONT = "8.5px ui-monospace, Consolas, monospace";
// Room around a card in its sprite: ports, step pill, topic stack, frames.
const CARD_SPRITE_PAD = 14;
// Node-graph bezier segments longer than CURVE_WHOLE_PX (screen px) are cut
// into pieces of at most CURVE_PIECE_PX before drawing (addViewCubic);
// shorter ones are drawn whole.  Small pieces rasterise like the whole curve.
const CURVE_WHOLE_PX = 4096;
const CURVE_PIECE_PX = 128;
// Scene tiles for settled Force and node graphs (see _drawSceneTiles).
const SCENE_TILE_SIZE = 512;
const SCENE_TILE_MIN_NODES = 1500;
const SCENE_TILE_BUDGET_MS = 10;
// Above this direct-draw cost, a zoom gesture shows scaled tiles instead.
const SCENE_TILE_DIRECT_MS = 16;
// Idle tile rendering waits until the zoom has been still this long.
const SCENE_TILE_SETTLE_MS = 150;
// 48 tiles x 1 MB: about two screens of 1920 x 1080 at dpr 2.
const SCENE_TILE_MAX = 48;

// Marker/label settings live in localStorage; their raw text is part of the
// scene signature.
function localStorageSig() {
  try { return localStorage.getItem("ark.mind.v1") || ""; } catch { return ""; }
}

const IMAGE_MEDIA_RE = /\.(png|jpe?g|gif|svg|webp|avif)(?:[?#]|$)/i;

// Bounding box of laid-out cards ({x, y, w, h} centres and sizes): its centre.
function layoutBox(items) {
  let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
  for (const item of items) {
    if (!Number.isFinite(item.x) || !Number.isFinite(item.y)) continue;
    const hw = (item.w || 0) / 2, hh = (item.h || 0) / 2;
    x0 = Math.min(x0, item.x - hw); x1 = Math.max(x1, item.x + hw);
    y0 = Math.min(y0, item.y - hh); y1 = Math.max(y1, item.y + hh);
  }
  return Number.isFinite(x0) ? { cx: (x0 + x1) / 2, cy: (y0 + y1) / 2 } : null;
}

// Bumped whenever web fonts finish loading: cached text measurements expire.
let FONT_GENERATION = 0;
function fontGeneration() {
  if (FONT_GENERATION === 0) {
    FONT_GENERATION = 1;
    try { document.fonts?.addEventListener?.("loadingdone", () => { FONT_GENERATION += 1; }); } catch { /* no font API */ }
  }
  return FONT_GENERATION;
}

// A press on a card thumbnail that moves less than this (px) is a click.
const MEDIA_CLICK_SLOP = 4;

function isImageMedia(item) {
  return Boolean(item && (item.is_image || IMAGE_MEDIA_RE.test(item.url || "") || /^data:image\//i.test(item.url || "")));
}

function imageMediaSrc(item) {
  if (!item) return "";
  if (/^(https?:|data:)/i.test(item.url || "")) return authenticatedUrl(item.url);
  return authenticatedUrl(item.raw_url || "");
}

function cachedMindImage(src, onChange) {
  if (!src) return null;
  const existing = MIND_IMAGE_CACHE.get(src);
  if (existing) return existing.complete ? existing : (existing.failed ? null : existing);
  const image = new Image();
  image.onload = () => onChange?.();
  image.onerror = () => { image.failed = true; };
  image.src = src;
  MIND_IMAGE_CACHE.set(src, image);
  return image;
}

function randomStartPositions(nodes, width, height) {
  const spreadX = Math.max(200, width * 0.35);
  const spreadY = Math.max(150, height * 0.35);
  for (const node of nodes) {
    node.x = (Math.random() - 0.5) * spreadX * 2;
    node.y = (Math.random() - 0.5) * spreadY * 2;
    node.vx = 0;
    node.vy = 0;
  }
}

export function setActiveGraphPhysics(value) {
  ACTIVE_PHYSICS = normalizeGraphPhysics(value);
  for (const graph of GRAPH_INSTANCES) graph.setPhysics(ACTIVE_PHYSICS);
  return { ...ACTIVE_PHYSICS };
}

// Line-end settings (project value with the person's override on top).  A
// view may pin its own copy with setArrows() (the Settings preview).
let ACTIVE_ARROWS = normalizeArrowSettings(null);
export function setActiveGraphArrows(value) {
  ACTIVE_ARROWS = normalizeArrowSettings(value);
  for (const graph of GRAPH_INSTANCES) if (!graph.arrowSettings) graph._draw();
  return ACTIVE_ARROWS;
}

export function getActiveGraphArrows() {
  return ACTIVE_ARROWS;
}

/**
 * A small canvas showing a line with the given resolved style (from
 * resolveArrowStyle; null draws a plain line).  Used by the edge filter and
 * the Settings arrow table.
 */
export function arrowGlyph(style, { width = 34, height = 14, color = null } = {}) {
  const canvas = document.createElement("canvas");
  const dpr = Math.min(3, window.devicePixelRatio || 1);
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  canvas.className = "arrow-glyph";
  canvas.style.width = `${width}px`;
  canvas.style.height = `${height}px`;
  canvas.setAttribute("aria-hidden", "true");
  const ctx = canvas.getContext?.("2d");
  if (!ctx) return canvas;
  const css = getComputedStyle(document.documentElement);
  const ink = color || (css.getPropertyValue("--sub") || "").trim() || "#93a2b6";
  ctx.scale(dpr, dpr);
  const y = height / 2, size = Math.min(height - 3, arrowSize(1.2, 1, style?.size || 1) * 0.8);
  const x0 = 3, x1 = width - 3;
  const inset = style ? arrowInset(style.head, size) : 0;
  ctx.beginPath();
  ctx.moveTo(style?.start ? x0 + inset : x0, y);
  ctx.lineTo(style?.end ? x1 - inset : x1, y);
  ctx.strokeStyle = ink;
  ctx.lineWidth = 1.3;
  ctx.stroke();
  if (style && (style.start || style.end)) {
    ctx.beginPath();
    if (style.end) addArrowhead(ctx, x1, y, 1, 0, size, style.head);
    if (style.start) addArrowhead(ctx, x0, y, -1, 0, size, style.head);
    // Hollow heads stay see-through so the glyph follows any theme change.
    paintArrowheads(ctx, ctx, style.head, ink, "rgba(0,0,0,0)", 1.3);
  }
  return canvas;
}

export function getActiveGraphPhysics() {
  return { ...ACTIVE_PHYSICS };
}

function circlePositions(nodes, radius = 240) {
  const n = Math.max(1, nodes.length);
  nodes.forEach((node, i) => {
    const a = (Math.PI * 2 * i) / n - Math.PI / 2;
    node.x = Math.cos(a) * radius;
    node.y = Math.sin(a) * radius;
  });
}

function treeLayout(nodes, edges, radial = false) {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const outgoing = new Map(nodes.map((n) => [n.id, []]));
  const incoming = new Map(nodes.map((n) => [n.id, []]));
  const neighbours = new Map(nodes.map((n) => [n.id, []]));
  for (const edge of edges) {
    if (byId.has(edge.source) && byId.has(edge.target)) {
      outgoing.get(edge.source).push(edge.target);
      incoming.get(edge.target).push(edge.source);
      neighbours.get(edge.source).push(edge.target);
      neighbours.get(edge.target).push(edge.source);
    }
  }
  // Prefer the paper's spine node, then a root, then the highest-degree node.
  const spine = nodes.find((n) => n.type === "story" && /spine|paper|narrative|story/i.test(n.title))
    || nodes.find((n) => n.type === "story" && incoming.get(n.id).length === 0)
    || nodes.slice().sort((a, b) => (b.degree || 0) - (a.degree || 0))[0];
  const root = spine || nodes[0];
  const depth = new Map();
  const queue = [root.id];
  depth.set(root.id, 0);
  while (queue.length) {
    const id = queue.shift();
    for (const next of neighbours.get(id) || []) {
      if (!depth.has(next)) { depth.set(next, depth.get(id) + 1); queue.push(next); }
    }
  }
  // Disconnected context goes to the right, grouped by type.
  let maxDepth = Math.max(0, ...depth.values());
  for (const node of nodes) {
    if (!depth.has(node.id)) { depth.set(node.id, maxDepth + 1); }
  }
  const levels = new Map();
  for (const node of nodes) {
    const d = depth.get(node.id) || 0;
    if (!levels.has(d)) levels.set(d, []);
    levels.get(d).push(node);
  }
  const seqOf = (n) => {
    const tag = (n.tags || []).find((x) => x.startsWith("seq:"));
    const value = tag ? parseInt(tag.split(":")[1], 10) : NaN;
    return Number.isFinite(value) ? value : 999;
  };
  for (const [d, group] of levels) {
    group.sort((a, b) => seqOf(a) - seqOf(b) || (a.type === "story" ? -1 : 1) - (b.type === "story" ? -1 : 1) || a.title.localeCompare(b.title));
  }
  if (!radial) {
    // Put the root at the left, then spread each depth around the vertical centre.
    const span = Math.max(1, ...Array.from(levels.values(), (g) => g.length));
    for (const [d, group] of levels) {
      group.forEach((node, i) => {
        node.x = d * 265;
        node.y = (i - (group.length - 1) / 2) * Math.min(104, 720 / Math.max(1, group.length));
      });
    }
  } else {
    for (const [d, group] of levels) {
      const radius = d * 155;
      group.forEach((node, i) => {
        const a = (Math.PI * 2 * i) / Math.max(1, group.length) - Math.PI / 2;
        node.x = Math.cos(a) * radius;
        node.y = Math.sin(a) * radius;
      });
    }
  }
  return { levels: maxDepth + 1, roots: 1 };
}

// World-space bounds of a node-graph route and of each of its bezier
// segments' control hulls ([minX, minY, maxX, maxY] per segment), cached on
// the route object.
function routeBounds(route) {
  if (route._bounds) return route._bounds;
  const segments = new Float64Array(route.segments.length * 4);
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  route.segments.forEach((seg, i) => {
    let a = Infinity, b = Infinity, c = -Infinity, d = -Infinity;
    for (let j = 0; j < 8; j += 2) {
      a = Math.min(a, seg[j]); c = Math.max(c, seg[j]);
      b = Math.min(b, seg[j + 1]); d = Math.max(d, seg[j + 1]);
    }
    segments.set([a, b, c, d], i * 4);
    minX = Math.min(minX, a); maxX = Math.max(maxX, c); minY = Math.min(minY, b); maxY = Math.max(maxY, d);
  });
  route._bounds = { minX, maxX, minY, maxY, segments };
  return route._bounds;
}

// Add the part of cubic `seg` (world units, [x0,y0,c1x,c1y,c2x,c2y,x1,y1])
// that can reach `view` (world rect) to the current path.  A segment longer
// than `whole` world units is split in halves (de Casteljau: the same curve)
// down to `limit`, and pieces whose control hull misses the view are
// dropped, so a curve spanning a million pixels is not tessellated along its
// whole length.  `state.open` tells whether the path is at this piece's start.
function addViewCubic(ctx, seg, view, whole, limit, sx, sy, state, depth = 0) {
  const x0 = seg[0], y0 = seg[1], x1 = seg[2], y1 = seg[3], x2 = seg[4], y2 = seg[5], x3 = seg[6], y3 = seg[7];
  const minX = Math.min(x0, x1, x2, x3), maxX = Math.max(x0, x1, x2, x3);
  const minY = Math.min(y0, y1, y2, y3), maxY = Math.max(y0, y1, y2, y3);
  if (maxX < view.x0 || minX > view.x1 || maxY < view.y0 || minY > view.y1) {
    state.open = false;
    return;
  }
  const extent = Math.max(maxX - minX, maxY - minY);
  if (depth < 32 && extent > (depth ? limit : whole)) {
    const ax = (x0 + x1) / 2, ay = (y0 + y1) / 2, bx = (x1 + x2) / 2, by = (y1 + y2) / 2, cx = (x2 + x3) / 2, cy = (y2 + y3) / 2;
    const dx = (ax + bx) / 2, dy = (ay + by) / 2, ex = (bx + cx) / 2, ey = (by + cy) / 2;
    const mx = (dx + ex) / 2, my = (dy + ey) / 2;
    addViewCubic(ctx, [x0, y0, ax, ay, dx, dy, mx, my], view, whole, limit, sx, sy, state, depth + 1);
    addViewCubic(ctx, [mx, my, ex, ey, cx, cy, x3, y3], view, whole, limit, sx, sy, state, depth + 1);
    return;
  }
  if (!state.open) ctx.moveTo(sx(x0), sy(y0));
  ctx.bezierCurveTo(sx(x1), sy(y1), sx(x2), sy(y2), sx(x3), sy(y3));
  state.open = true;
}

// `parents` (optional Map child id -> parent id) replaces the primary
// relation: the Local graph roots its Mind layout at the focused node and
// uses the breadth-first parents of its neighbourhood.
export class GraphView {
  constructor(host, opts = {}) {
    this.host = host;
    this.opts = { compact: false, onSelect: null, onConnect: null, externalDrag: false, onExternalDrop: null, labelBudget: false, layout: "force", ...opts };
    this.cameraKey = opts.cameraKey || null;
    this.host.tabIndex = 0;
    this.host.classList.add("graph-focusable");
    this.id = `g${++GRAPH_SEQ}`;
    this.readOnly = Boolean(opts.readOnly);
    this.clipboardSource = opts.clipboardSource || "ark";
    this.nodeGraphKey = opts.nodeGraphKey || `ark.nodeGraph.${opts.cameraKey || this.id || "default"}`;
    this.layout = ["force", "tree", "mind", "node"].includes(this.opts.layout) ? this.opts.layout : "force";
    this.edgeRouteDrag = null;
    this.mindResizeDrag = null;
    this.history = [];
    this._undoing = false;
    this._restoring = false;
    this.pinnedMedia = new Set((() => { try { return JSON.parse(localStorage.getItem("ark.mindPinnedMedia") || "[]"); } catch { return []; } })());
    this.imageSizes = (() => { try { return JSON.parse(localStorage.getItem("ark.mindImageSizes") || "{}"); } catch { return {}; } })();
    this.mindWidths = (() => { try { return JSON.parse(localStorage.getItem("ark.mindWidths") || "{}"); } catch { return {}; } })();
    this.mindPinnedImages = (() => { try { return JSON.parse(localStorage.getItem("ark.mindPinnedImages") || "{}"); } catch { return {}; } })();
    this.nodes = [];
    this.edges = [];
    this.byId = new Map();
    this.zoom = 1;
    this.pan = { x: 0, y: 0 };
    this.alpha = 1;
    this.hover = null;
    this.hoverEdge = null;
    this.selected = null;
    this.selectedEdge = null;
    this.selectedNodes = new Set();
    this.collapsed = new Set();
    this.hiddenIds = new Set();
    this.relationTypes = [...new Set([...(opts.relations || []), ...ALL_RELATIONS])];
    this.edgeFilterKey = opts.edgeFilterKey
      || `ark.edgeFilter.v1:${opts.nodeGraphKey || opts.cameraKey || this.id || "default"}`;
    this.primaryRelation = this._loadPrimaryRelation(this.layout);
    // Restore the last relation selection for this exact graph view.  If none
    // exists, Mind starts with hierarchy only and Force/Node with everything.
    this.visibleRelations = this._initialRelations(this.layout);
    this._nodeFilterInitialized = this.layout !== "node";
    this.editor = null;
    this.editingNodeId = null;
    this.linkDrag = null;
    this._adding = false;
    this._multiDown = null;
    this.selectBox = null;
    this._lastNodeDown = null;
    this.edgePick = null;
    this.externalDrag = null;
    this.imageResizeDrag = null;
    this.imageSideDrag = null;
    this.imageHover = null;
    this.imageSideHover = null;
    this._ghost = null;
    this._suppressContextMenu = false;
    this.drag = null;
    this.pointers = new Map();
    this.pinch = null;
    this.physics = normalizeGraphPhysics(this.opts.physics || ACTIVE_PHYSICS);
    this.arrowSettings = this.opts.arrows ? normalizeArrowSettings(this.opts.arrows) : null;
    const savedMode = (() => { try { return localStorage.getItem(`ark.graphMode.${this.layout}`); } catch { return null; } })();
    this.mode = this.readOnly ? "read" : (opts.mode || ((savedMode === "read" || savedMode === "edit") ? savedMode : (this.layout === "mind" ? "edit" : "read")));
    this.showLabels = true;
    this.connectMode = false;
    this.connectSource = null;
    this.running = false;
    this.frame = 0;
    this._cameraFrame = 0;
    this._wheelFrame = 0;
    this._wheelTarget = null;
    this._userInteracted = false;
    this._pendingForceFit = false;
    // Rasterised cards and labels (shared painter, see _drawSprite).
    this._spriteCache = this.opts.backdrop ? null : new SpriteCache();
    GRAPH_INSTANCES.add(this);
    this._build();
    this._bind();
    if (this.modeReadBtn) this.setMode(this.mode, { persist: false });
    if (!this.opts.compact) { this._syncEdgeFilter(); this._syncNodeTools(); }
    this._resize();
  }

  _build() {
    clear(this.host);
    this.host.classList.add("graph-host-inner");
    this.canvas = h("canvas", { "aria-label": t("graph.layout_node") });
    // A block canvas: an inline one sits on the text baseline and leaves a
    // few px of descender space below it, which overflows its container.
    this.canvas.style.display = "block";
    this.host.appendChild(this.canvas);
    this.ctx = this.canvas.getContext("2d");
    if (this.opts.compact) {
      this.host.classList.add("compact");
      if (this.opts.sidebarToolbar) this._buildSidebarTools();
      return;
    }
    this.countEl = h("span", { class: "graph-count" }, "—");
    this.modeReadBtn = h("button", {
      class: "graph-mode-btn", type: "button", title: t("graph.mode.read_title"),
      onclick: () => this.setMode("read"),
    }, t("graph.mode.read"));
    this.modeEditBtn = h("button", {
      class: "graph-mode-btn", type: "button", title: t("graph.mode.edit_title"),
      onclick: () => this.setMode("edit"),
    }, t("graph.mode.edit"));
    const modeBar = this.readOnly
      ? h("span", { class: "graph-readonly-pill" }, t("node.readonly"))
      : h("div", { class: "graph-mode" }, this.modeReadBtn, this.modeEditBtn);
    this.tools = h("div", { class: "graph-tools" },
      modeBar,
      h("span", { class: "sep" }),
      h("select", {
        class: "graph-sel", title: t("graph.layout"), "aria-label": t("graph.layout"),
        onchange: (e) => this.setLayout(e.target.value),
      },
        h("option", { value: "force" }, t("graph.force")),
        h("option", { value: "tree" }, t("graph.tree")),
        h("option", { value: "mind" }, t("graph.mind")),
        h("option", { value: "node" }, t("graph.layout_node"))),
      h("button", {
        class: "graph-node-auto hidden", title: t("graph.auto_layout_node"),
        "aria-label": t("graph.auto_layout_node"), onclick: () => this.autoLayout(),
      }, icon("refresh", "sm")),
      h("button", {
        class: "graph-edge-filter-btn hidden", title: t("graph.visible_connections"),
        "aria-label": t("graph.visible_connections"), onclick: () => this._toggleEdgeFilter(),
      }, icon("link", "sm")),
      h("span", { class: "sep" }),
      h("button", { title: t("graph.zoom_in"), "aria-label": t("graph.zoom_in"), onclick: () => this.zoomBy(1.2) }, icon("zoomIn", "sm")),
      h("button", { title: t("graph.zoom_out"), "aria-label": t("graph.zoom_out"), onclick: () => this.zoomBy(1 / 1.2) }, icon("zoomOut", "sm")),
      h("button", { title: t("graph.fit"), "aria-label": t("graph.fit"), onclick: () => this.fit({ animate: true }) }, icon("fit", "sm")),
      h("button", {
        class: "graph-help-btn", title: t("graph.help.title"), "aria-label": t("graph.help.title"),
        "aria-expanded": "false", onclick: () => this._toggleHelp(),
      }, "?"));
    this.legendEl = h("div", { class: "graph-legend" });
    this.hintEl = h("div", { class: "graph-hint hidden" });
    this.helpEl = h("div", { class: "graph-help hidden", role: "dialog", "aria-label": t("graph.help.title") });
    this.edgeFilterEl = h("div", { class: "graph-edge-filter hidden", role: "dialog", "aria-label": t("graph.visible_connections") });
    this.tooltip = h("div", { class: "graph-tooltip hidden" });
    this.emptyEl = h("div", { class: "graph-empty hidden" });
    this.connectBar = h("div", { class: "graph-connect hidden" });
    this.host.append(
      h("div", { class: "graph-top" }, this.countEl),
      this.tools, this.legendEl, this.hintEl, this.helpEl, this.edgeFilterEl, this.tooltip, this.emptyEl, this.connectBar);
  }

  _buildSidebarTools() {
    const spec = this.opts.sidebarToolbar || {};
    const labels = { force: "graph.force", tree: "graph.tree", mind: "graph.mind", node: "graph.layout_node" };
    const layouts = (spec.layouts || ["force", "mind"]).filter((id) => labels[id]);
    const tool = (title, content, onClick, extra = "") => h("button", {
      class: `graph-tool-opt ${extra}`.trim(), type: "button", title, "aria-label": title, onclick: onClick,
    }, content);
    const actions = [
      { label: t("graph.zoom_in"), icon: "zoomIn", run: () => this.zoomBy(1.2) },
      { label: t("graph.zoom_out"), icon: "zoomOut", run: () => this.zoomBy(1 / 1.2) },
      { label: t("graph.fit"), icon: "fit", run: () => this.fit({ animate: true }) },
    ];
    this.tools = h("div", { class: "graph-tools graph-tools-sidebar" },
      h("select", {
        class: "graph-sel", title: t("graph.layout"), "aria-label": t("graph.layout"),
        onchange: (event) => spec.onLayout?.(event.target.value),
      }, layouts.map((id) => h("option", { value: id, selected: id === this.layout ? true : undefined }, t(labels[id])))),
      ...(spec.extra || []),
      h("span", { class: "sep" }),
      ...actions.map((action) => tool(action.label, icon(action.icon, "sm"), action.run)),
      tool(t("graph.help.title"), "?", () => this._toggleHelp(), "graph-help-btn"),
      h("button", {
        class: "graph-more-btn", type: "button", title: t("graph.more_tools"), "aria-label": t("graph.more_tools"),
        onclick: (event) => {
          const rect = event.currentTarget.getBoundingClientRect();
          openContextMenu({ x: rect.left, y: rect.bottom + 4, items: [
            ...actions.map((action) => ({ label: action.label, icon: action.icon, onClick: action.run })),
            { label: t("graph.help.title"), icon: "info", onClick: () => this._toggleHelp(true) },
          ] });
        },
      }, icon("more", "sm")));
    this.helpEl = h("div", { class: "graph-help hidden", role: "dialog", "aria-label": t("graph.help.title") });
    this.host.append(this.tools, this.helpEl);
  }

  _bind() {
    this._onResize = () => this._resize();
    this._resizeObserver = new ResizeObserver(this._onResize);
    this._resizeObserver.observe(this.host);
    if (this.opts.backdrop) {
      this.host.tabIndex = -1;
      return;
    }
    this.canvas.addEventListener("pointerdown", (e) => this._pointerDown(e));
    this.canvas.addEventListener("pointermove", (e) => this._pointerMove(e));
    this.canvas.addEventListener("pointerup", (e) => this._pointerUp(e));
    this.canvas.addEventListener("pointercancel", (e) => this._pointerUp(e));
    // Capture ends implicitly after pointerup; any other loss means the
    // release will never reach the canvas, so a line drag must not survive it.
    this.canvas.addEventListener("lostpointercapture", () => {
      if (this._lineDragActive()) this._finishLineDrag(null, { cancel: true });
    });
    // Some browser/OS gesture stacks consume right-button pointermove. Keep a
    // mouse-event fallback for right-drag panning.
    this.canvas.addEventListener("mousedown", (e) => {
      if (e.button !== 2 || this.drag?.pan || this.selectBox) return;
      e.preventDefault();
      this._suppressContextMenu = false;
      const startX = e.clientX, startY = e.clientY;
      const ox = this.pan.x, oy = this.pan.y;
      this.drag = { pan: true, right: true, moved: false, startX, startY, ox, oy };
      this.canvas.style.cursor = "grabbing";
      const move = (ev) => {
        if (!this.drag?.pan) return;
        const dx = ev.clientX - startX, dy = ev.clientY - startY;
        if (Math.abs(dx) + Math.abs(dy) > 3) this.drag.moved = true;
        this.pan.x = ox + dx;
        this.pan.y = oy + dy;
        this._draw();
      };
      const up = (ev) => {
        if (ev.button !== 2) return;
        window.removeEventListener("mousemove", move, true);
        window.removeEventListener("mouseup", up, true);
        this._pointerUp(ev);
      };
      window.addEventListener("mousemove", move, true);
      window.addEventListener("mouseup", up, true);
      this._draw();
    });
    this.canvas.addEventListener("pointerleave", () => {
      if (!this.drag) {
        this.hover = null; this.hoverEdge = null; this.imageHover = null;
        this._hideTooltip(); this._draw();
      }
    });
    this.canvas.addEventListener("wheel", (e) => this._wheel(e), { passive: false });
    this.canvas.addEventListener("auxclick", (e) => { if (e.button === 2) e.preventDefault(); });
    this.canvas.addEventListener("dragstart", (e) => e.preventDefault());
    this.canvas.addEventListener("mousedown", (e) => { if (e.button === 2) e.preventDefault(); }, { capture: true });
    this.canvas.addEventListener("selectstart", (e) => e.preventDefault());
    this.canvas.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      // Firefox/Linux can dispatch this on press, before a drag is known.
      // Defer that request until release; Windows may dispatch it after release.
      if (this.drag?.right) { this.drag.contextMenuPending = true; return; }
      if (this._suppressContextMenu && e.button === 2) { this._suppressContextMenu = false; return; }
      if (this.selectBox?.moved) return;
      this._showContextMenu(e);
    });
    this.canvas.addEventListener("dblclick", (e) => {
      if (this._hitMindImage(e)) { e.preventDefault(); return; }
      const gripNode = this._hitMindResize(e);
      if (gripNode) {
        // Double-clicking the resize grip restores the automatic width.
        e.preventDefault();
        const stored = this.layout === "node" ? (gripNode.nodeW ?? null) : (gripNode.customW ?? null);
        if (stored === null) return;
        const layout = this.layout;
        this._setCardWidth(gripNode, null);
        this._pushHistory({ undo: () => { if (this.layout === layout) this._setCardWidth(gripNode, stored); } });
        return;
      }
      // Resolve the node before the edge.  Edges are drawn through node
      // footprints and their hit tolerance can otherwise steal a node
      // double-click, opening the relation editor instead of the title.
      let node = this._hit(e);
      const recent = this._lastNodeDown;
      if (!node && recent && this.byId.has(recent.node.id)
          && performance.now() - recent.at < 900) {
        const rect = this.canvas.getBoundingClientRect();
        const point = this._screen(recent.node);
        if (Math.hypot(e.clientX - rect.left - point.x,
                       e.clientY - rect.top - point.y) < 48) node = recent.node;
      }
      if (node) {
        if (this.mode === "edit") {
          e.preventDefault();
          this._setSelection([node], false);
          this.startEdit(node);
        } else if (node.type === "topic" && this.opts.onOpenTopic) {
          this.opts.onOpenTopic(node);
        } else {
          this.focus(node);
          this.opts.onSelect?.(node, true, e);
        }
        return;
      }
      const edge = this._hitEdge(e);
      if (edge) {
        const source = this.byId.get(edge.source), target = this.byId.get(edge.target);
        this.selectedEdge = edge;
        this._draw();
        this.opts.onEdgeClick?.(edge, source, target);
        return;
      }
      if (this.mode === "edit" && this.opts.onAddOrphan) this._addOrphanNode(e);
    });
    this.host.addEventListener("keydown", (e) => this._onKeyDown(e));
    this._pasteImageHandler = async (event) => {
      if (!this.opts.onAttachImage) return;
      if (event.target?.closest?.(".modal-backdrop, .palette-backdrop")) return;
      const data = event.clipboardData;
      let file = [...(data?.files || [])].find((item) => item.type.startsWith("image/"));
      if (!file) {
        for (const item of data?.items || []) {
          if (item.kind === "file" && item.type.startsWith("image/")) {
            file = item.getAsFile();
            if (file) break;
          }
        }
      }
      if (!file) return;
      const node = this._selectedNodeObjects()[0]
        || this.byId.get(this.opts.getSelectedId?.())
        || null;
      if (!node) return;
      event.preventDefault();
      event.stopPropagation();
      try {
        const result = await this.opts.onAttachImage(node, file);
        if (!result?.media) return;
        node.media = result.media;
        if (this.layout === "mind") this._applyLayout(false);
        else this._draw();
        this.opts.onNotice?.(t("graph.toast.image_attached"));
      } catch (err) {
        this.opts.onError?.(err);
      }
    };
    document.addEventListener("paste", this._pasteImageHandler, true);
    this._helpOutside = (event) => {
      if (!this.helpEl || this.helpEl.classList.contains("hidden")) return;
      if (this.helpEl.contains(event.target) || this.tools?.contains(event.target)) return;
      this._toggleHelp(false);
    };
    document.addEventListener("pointerdown", this._helpOutside, true);
    this._edgeFilterOutside = (event) => {
      if (!this.edgeFilterEl || this.edgeFilterEl.classList.contains("hidden")) return;
      if (this.edgeFilterEl.contains(event.target) || this.tools?.contains(event.target)) return;
      this._toggleEdgeFilter(false);
    };
    document.addEventListener("pointerdown", this._edgeFilterOutside, true);
  }

  _showContextMenu(event) {
    const imageHit = this._hitMindImage(event);
    if (imageHit) {
      this._openMindImageMenu(imageHit, event);
      return;
    }
    const node = this._hit(event);
    if (node && !this.selectedNodes.has(node.id)) this._setSelection([node], false);
    this._draw();
    this._openGraphContextMenu(node, event);
  }

  _toggleHelp(force = null) {
    if (!this.helpEl) return;
    const show = force === null ? this.helpEl.classList.contains("hidden") : Boolean(force);
    if (show) this._renderHelp();
    this.helpEl.classList.toggle("hidden", !show);
    if (show && this.tools) this.helpEl.style.top = `${this.tools.offsetTop + this.tools.offsetHeight + 6}px`;
    this.tools?.querySelector(".graph-help-btn")?.setAttribute("aria-expanded", String(show));
  }

  _renderHelp() {
    if (!this.helpEl) return;
    clear(this.helpEl);
    const row = (keys, text) => h("div", { class: "graph-help-row" },
      h("kbd", null, keys), h("span", null, text));
    const readMode = this.mode === "read";
    this.helpEl.append(
      h("div", { class: "graph-help-head" },
        h("b", null, t(readMode ? "graph.help.title_read" : "graph.help.title_edit")),
        h("button", { class: "icon-btn", title: t("action.close"), onclick: () => this._toggleHelp(false) }, icon("x", "sm"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.navigation")),
        row("← → ↑ ↓", t("graph.help.move_selection")),
        row("R / E", t("graph.help.read_edit_mode")),
        row("F", t("graph.help.fit_graph")),
        row("?", t("graph.help.this_help")),
        row("Esc", t("graph.help.clear_selection"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.mouse")),
        row(t("graph.help.left_drag"), t("graph.help.box_select")),
        row(t("graph.help.right_drag"), t("graph.help.pan")),
        row(t("graph.help.wheel"), t("graph.help.zoom")),
        row(t("graph.help.ctrl_wheel"), t("graph.help.pan_vertical")),
        row(t("graph.help.shift_wheel"), t("graph.help.pan_horizontal")),
        row(t("graph.help.right_click"), t("graph.help.context_menu"))),
      h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.mind_collapse")),
        row("+ / -", t("graph.help.expand_one")),
        row("* / /", t("graph.help.expand_all")),
        row("Ctrl+A", t("graph.help.select_visible")),
        row("Ctrl+Shift+A", t("graph.help.select_level")),
        row("Ctrl+C / V", t("graph.help.copy_paste")),
        row("Ctrl+X", t("graph.help.cut_subtree")),
        row("Ctrl+Shift+C", t("graph.help.copy_subtree")),
        row("Ctrl+Z", t("graph.help.undo"))),
      readMode
        ? h("div", { class: "graph-help-note" },
          t("graph.help.read_note"))
        : h("div", { class: "graph-help-group" }, h("h5", null, t("graph.help.editing")),
          row(t("graph.help.left_click"), t("graph.help.select_only")),
          row("Shift+Enter", t("graph.help.new_line")),
          row("Ctrl+Enter", t("graph.help.insert_between")),
          row(t("graph.help.space_dblclick"), t("graph.help.edit_title")),
          row("Enter", t("graph.help.sibling_below")),
          row("Shift+Enter", t("graph.help.sibling_above")),
          row("Tab", t("graph.help.child_node")),
          row("Delete", t("graph.help.delete_selected")),
          row(t("graph.help.alt_drag"), t("graph.help.create_link"))),
      h("div", { class: "graph-help-note" },
        t("graph.help.note_backspace"),
        t("graph.help.note_labels"),
        t("graph.help.note_gesture")));
  }

  _toggleEdgeFilter(force = null) {
    if (!this.edgeFilterEl || !["mind", "node", "force"].includes(this.layout)) return;
    const show = force === null ? this.edgeFilterEl.classList.contains("hidden") : Boolean(force);
    if (show) this._renderEdgeFilter();
    this.edgeFilterEl.classList.toggle("hidden", !show);
    this.tools?.querySelector(".graph-edge-filter-btn")?.setAttribute("aria-expanded", String(show));
  }

  _reheatRelationFilter() {
    if (this.layout === "force") {
      this.alpha = Math.max(this.alpha, 0.35);
      this._start();
    } else {
      this._draw();
    }
  }

  // The relations a layout shows before the user changes the filter: the
  // stored choice, else hierarchy only (Mind) or everything.  With
  // `allRelations` (Local graph) every relation in the data is shown.
  _initialRelations(layout = this.layout) {
    if (this.opts.allRelations) return new Set([...this.relationTypes, ...(this.edges || []).map((edge) => edge.relation)]);
    return this._loadRelationFilter(layout)
      || new Set(layout === "mind" ? [this.primaryRelation || "part_of"] : this.relationTypes);
  }

  _loadPrimaryRelation(layout = this.layout) {
    try {
      const saved = localStorage.getItem(`${this.edgeFilterKey}:primary:${layout}`);
      if (saved && this.relationTypes.includes(saved)) return saved;
    } catch { /* storage can be unavailable */ }
    return "part_of";
  }

  _savePrimaryRelation() {
    try {
      localStorage.setItem(`${this.edgeFilterKey}:primary:${this.layout}`,
        this.primaryRelation || "part_of");
    } catch { /* storage can be unavailable */ }
  }

  _loadRelationFilter(layout = this.layout) {
    try {
      const raw = localStorage.getItem(`${this.edgeFilterKey}:${layout}`);
      if (!raw) return null;
      const list = JSON.parse(raw);
      if (!Array.isArray(list)) return null;
      return new Set(list.filter((relation) => this.relationTypes.includes(relation)));
    } catch { return null; }
  }

  _saveRelationFilter() {
    try {
      localStorage.setItem(`${this.edgeFilterKey}:${this.layout}`,
        JSON.stringify([...this.visibleRelations]));
    } catch { /* storage can be unavailable */ }
  }

  _syncEdgeFilter() {
    const supported = ["mind", "node", "force"].includes(this.layout);
    this.tools?.querySelector(".graph-edge-filter-btn")?.classList.toggle("hidden", !supported);
    if (!supported && this.edgeFilterEl) {
      this.edgeFilterEl.classList.add("hidden");
      this.tools?.querySelector(".graph-edge-filter-btn")?.setAttribute("aria-expanded", "false");
    }
  }

  _pushHistory(entry) {
    if (this._undoing || this._restoring || !entry?.undo) return;
    this.history.push(entry);
    if (this.history.length > 50) this.history.shift();
  }

  _removeNodesLocal(nodes) {
    const ids = new Set(nodes.map((node) => node.id));
    this.nodes = this.nodes.filter((node) => !ids.has(node.id));
    this.edges = this.edges.filter((edge) => !ids.has(edge.source) && !ids.has(edge.target));
    this.byId = new Map(this.nodes.map((node) => [node.id, node]));
    this.selectedNodes = new Set();
    this.selected = null;
    this._draw();
  }

  async _undoLast() {
    if (this._undoing || this._restoring || !this.history.length) return;
    const entry = this.history.pop();
    this._undoing = true;
    try {
      await entry.undo();
      this.opts.onNotice?.(t("graph.toast.undid"));
    } catch (err) {
      this.opts.onError?.(err);
    } finally {
      this._undoing = false;
    }
  }

  // The layout picker and Node-only tools follow the current layout.  A view
  // created directly in a non-default layout (the Topic canvas opens in
  // "node") would otherwise show the picker's first option, "Force".
  _syncNodeTools() {
    const select = this.tools?.querySelector("select.graph-sel");
    if (select) select.value = this.layout;
    const auto = this.tools?.querySelector(".graph-node-auto");
    if (auto) {
      auto.classList.toggle("hidden", this.layout !== "node" && this.layout !== "mind");
      const label = t(this.layout === "mind" ? "graph.auto_layout_mind" : "graph.auto_layout_node");
      auto.title = label;
      auto.setAttribute?.("aria-label", label);
    }
  }

  _loadNodeGraphState() {
    try {
      const parsed = this.opts.nodeLayoutStore
        ? (this.opts.nodeLayoutStore.load() || {})
        : JSON.parse(localStorage.getItem(this.nodeGraphKey) || "{}");
      return {
        positions: parsed?.positions && typeof parsed.positions === "object" ? parsed.positions : {},
        bends: parsed?.bends && typeof parsed.bends === "object" ? parsed.bends : {},
        widths: parsed?.widths && typeof parsed.widths === "object" ? parsed.widths : {},
      };
    } catch {
      return { positions: {}, bends: {}, widths: {} };
    }
  }

  _saveNodeGraphState() {
    if (this.layout !== "node") return;
    try {
      const state = { positions: {}, bends: {}, widths: {} };
      for (const node of this.nodes) {
        if (Number.isFinite(node.x) && Number.isFinite(node.y)) {
          state.positions[node.uid || String(node.id)] = { x: node.x, y: node.y };
        }
        // The card width the user dragged (null = automatic).
        state.widths[node.uid || String(node.id)] = cleanCardWidth(node.nodeW);
      }
      for (const edge of this.edges) {
        if (edge.routeBend) state.bends[String(edge.id)] = { x: edge.routeBend.x, y: edge.routeBend.y };
      }
      if (this.opts.nodeLayoutStore) {
        this.opts.nodeLayoutStore.save(state);
        return;
      }
      localStorage.setItem(this.nodeGraphKey, JSON.stringify(state));
    } catch { /* storage can be unavailable */ }
  }

  // Layered auto layout for every node.  Long edges keep the dummy-node
  // waypoints as routing channels until one of their endpoints moves.
  _nodeAutoPositions() {
    const { positions, waypoints } = layeredLayout(
      this.nodes.map((node) => ({ id: node.id, w: node.w || NODE_W, h: node.h || NODE_H })),
      this._shownEdges().filter((edge) => this.visibleRelations.has(edge.relation)));
    // Centre the layout's bounding box on the Force gravity centre (the
    // world origin), waypoints too, so switching layouts keeps one centre.
    const sizes = new Map(this.nodes.map((node) => [node.id, node]));
    const box = layoutBox([...positions].map(([id, p]) => ({ x: p.x, y: p.y, w: sizes.get(id)?.w || NODE_W, h: sizes.get(id)?.h || NODE_H })));
    if (box) {
      for (const p of positions.values()) { p.x -= box.cx; p.y -= box.cy; }
      for (const points of waypoints.values()) for (const point of points) { point.x -= box.cx; point.y -= box.cy; }
    }
    const stamp = new Map();
    for (const [id, p] of positions) stamp.set(id, `${p.x},${p.y}`);
    this._nodeWaypoints = { map: waypoints, stamp };
    return positions;
  }

  _layoutNodeGraph(fit = true, ignoreSaved = false, { animate = false, onSettled = null } = {}) {
    if (!this.nodes.length) return;
    const state = this._loadNodeGraphState();
    const before = animate ? new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }])) : null;
    // Card widths survive Auto layout; only positions are replaced.
    for (const node of this.nodes) {
      node.nodeW = cleanCardWidth(state.widths[node.uid || String(node.id)]);
      node.vx = 0;
      node.vy = 0;
    }
    this._applyNodeCardMetrics();
    const saved = (node) => (!ignoreSaved ? state.positions[node.uid || String(node.id)] : null);
    const anySaved = this.nodes.some((node) => saved(node));
    if (!anySaved) {
      // Nothing arranged yet (or Auto layout): lay out the whole graph.
      const positions = this._nodeAutoPositions();
      for (const node of this.nodes) Object.assign(node, positions.get(node.id) || { x: 90, y: 80 });
    } else {
      // Keep every saved position; only place nodes that have none.
      this._nodeWaypoints = null;
      const placed = this.nodes.map((node) => {
        const position = saved(node);
        return { id: node.id, w: node.w, h: node.h, ...(position ? { x: position.x, y: position.y } : {}) };
      });
      const fresh = placeNewNodes(placed, this._shownEdges());
      for (const [i, node] of this.nodes.entries()) {
        const position = fresh.get(node.id) || placed[i];
        node.x = position.x;
        node.y = position.y;
      }
    }
    for (const edge of this.edges) {
      const bend = state.bends[String(edge.id)];
      if (!ignoreSaved && bend && Number.isFinite(bend.x) && Number.isFinite(bend.y)) edge.routeBend = { ...bend };
      else delete edge.routeBend;
    }
    this._nodeRouteCache = null;
    // Persist and fit the final positions before any transition eases
    // towards them.
    if (fit || !ignoreSaved || before) this._saveNodeGraphState();
    if (fit) this.fit();
    const animating = Boolean(before) && this._animateNodePositions(before, 320, onSettled);
    if (!animating) {
      this._draw();
      onSettled?.();
    }
  }

  // Node cards use the Mind card model: wrapped title, footer markers,
  // image strip or pinned main image; width from the resize handle.
  _applyNodeCardMetrics() {
    const showImages = getMindSettings().showMindImages !== false;
    for (const node of this.nodes) {
      applyCardMetrics(node, cardMetrics(node, { kind: "node", showImages, customW: node.nodeW }));
    }
    this._nodeRouteCache = null;
  }

  // Auto layout replaces every position (manual ones too) and is undoable.
  /**
   * Auto layout (toolbar button, topic page, edge filter): the node graph's
   * layered layout, or Mind's tree layout with every card the user dragged
   * released.  Animated and one undo step.
   */
  autoLayout() {
    if (this.layout === "node") { this._autoLayoutNodeGraph(); return; }
    if (this.layout !== "mind" || !this.nodes.length) return;
    const snapshot = this._mindSnapshot();
    // Every tree and the orphan area from scratch: compact for the current
    // fold state, dragged cards released, reflow offsets dropped.
    for (const node of this.nodes) delete node.mindManual;
    this._mindOffsets = new Map();
    this._mindShift = null;
    this._applyLayout(false);
    if (!this._animateNodePositions(snapshot.positions, 320, () => this.fit({ animate: true }))) this.fit({ animate: true });
    this._pushHistory({ undo: () => this._restoreMindSnapshot(snapshot) });
  }

  // Positions, dragged flags, fold state and tree offsets of a Mind view (for
  // undo of Auto layout and of fold reflows).
  _mindSnapshot() {
    return {
      positions: new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }])),
      manual: new Set(this.nodes.filter((node) => node.mindManual).map((node) => node.id)),
      collapsed: new Set(this.collapsed),
      offsets: new Map(this._mindOffsets || []),
      shift: this._mindShift ? { ...this._mindShift } : null,
    };
  }

  _restoreMindSnapshot(snapshot) {
    if (this._nodeAnimation) cancelAnimationFrame(this._nodeAnimation.frame);
    this._nodeAnimation = null;
    const current = new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }]));
    this.collapsed = new Set([...snapshot.collapsed].filter((id) => this.byId.has(id)));
    this._mindOffsets = new Map(snapshot.offsets);
    this._mindShift = snapshot.shift ? { ...snapshot.shift } : null;
    for (const node of this.nodes) {
      const old = snapshot.positions.get(node.id);
      if (old && Number.isFinite(old.x)) { node.x = old.x; node.y = old.y; }
      if (snapshot.manual.has(node.id)) node.mindManual = true;
      else delete node.mindManual;
    }
    if (!this._animateNodePositions(current, 200)) this._draw();
  }

  _autoLayoutNodeGraph() {
    if (this.layout === "mind") { this.autoLayout(); return; }
    if (this.layout !== "node") return;
    const oldPositions = new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }]));
    const oldBends = new Map(this.edges.filter((edge) => edge.routeBend).map((edge) => [edge.id, { ...edge.routeBend }]));
    this._layoutNodeGraph(false, true, { animate: true, onSettled: () => this.fit({ animate: true }) });
    this._pushHistory({
      undo: () => {
        const current = new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }]));
        for (const node of this.nodes) {
          const old = oldPositions.get(node.id);
          if (old && Number.isFinite(old.x)) { node.x = old.x; node.y = old.y; }
        }
        for (const edge of this.edges) {
          if (oldBends.has(edge.id)) edge.routeBend = { ...oldBends.get(edge.id) };
          else delete edge.routeBend;
        }
        this._nodeWaypoints = null;
        this._saveNodeGraphState();
        this._animateNodePositions(current);
      },
    });
  }

  // Ease nodes from `from` (id -> {x, y}) to their current positions.
  _animateNodePositions(from, duration = 320, onDone = null) {
    if (this._nodeAnimation) cancelAnimationFrame(this._nodeAnimation.frame);
    this._nodeAnimation = null;
    if (getMindSettings().nodeTransitions === false || typeof requestAnimationFrame !== "function") return false;
    const targets = this.nodes
      .map((node) => ({ node, to: { x: node.x, y: node.y }, from: from.get(node.id) }))
      .filter((item) => item.from && Number.isFinite(item.from.x) && (item.from.x !== item.to.x || item.from.y !== item.to.y));
    if (!targets.length) return false;
    const started = performance.now();
    const animation = { frame: 0 };
    this._nodeAnimation = animation;
    const step = () => {
      const t = Math.min(1, (performance.now() - started) / duration);
      const e = 1 - (1 - t) ** 3;
      for (const { node, from: a, to: b } of targets) {
        node.x = a.x + (b.x - a.x) * e;
        node.y = a.y + (b.y - a.y) * e;
      }
      this._draw();
      if (t < 1 && this._nodeAnimation === animation) animation.frame = requestAnimationFrame(step);
      else if (this._nodeAnimation === animation) {
        for (const { node, to } of targets) { node.x = to.x; node.y = to.y; }
        this._nodeAnimation = null;
        this._draw();
        onDone?.();
      }
    };
    for (const { node, from: a } of targets) { node.x = a.x; node.y = a.y; }
    animation.frame = requestAnimationFrame(step);
    return true;
  }

  // World-space routes for the visible edges, recomputed only when node
  // positions, sizes, bends or the visible relations change.  While nodes
  // animate (or a big graph is being dragged) curves skip obstacle checks.
  _nodeRoutes() {
    const edges = this._shownEdges().filter((edge) => this.visibleRelations.has(edge.relation));
    let hash = edges.length * 7919 + this.nodes.length;
    for (const edge of edges) if (edge.shownReversed) hash = (hash * 13 + String(edge.id).length + 7) % 1e12;
    for (const node of this.nodes) hash = (hash * 31 + node.x * 7 + node.y * 13 + (node.w || 0) + (node.h || 0) * 3) % 1e12;
    for (const edge of edges) if (edge.routeBend) hash = (hash * 17 + edge.routeBend.x * 3 + edge.routeBend.y) % 1e12;
    const fast = Boolean(this._nodeAnimation) || (this.nodes.length > 400 && Boolean(this.drag?.moved));
    const key = `${hash}:${fast}`;
    if (this._nodeRouteCache?.key === key) return this._nodeRouteCache.routes;
    let waypoints = null;
    if (this._nodeWaypoints && !fast) {
      const { map, stamp } = this._nodeWaypoints;
      const at = (node) => `${node.x},${node.y}`;
      waypoints = new Map();
      for (const edge of edges) {
        const pair = `${edge.source}->${edge.target}`;
        const points = map.get(pair);
        const source = this.byId.get(edge.source), target = this.byId.get(edge.target);
        if (!points || !source || !target) continue;
        if (stamp.get(source.id) !== at(source) || stamp.get(target.id) !== at(target)) continue;
        waypoints.set(pair, points);
      }
    }
    const routes = routeNodeEdges(this.nodes,
      edges.map((edge) => ({ id: edge.id, source: edge.source, target: edge.target, bend: edge.routeBend })),
      { waypoints, fast });
    this._nodeRouteCache = { key, routes };
    return routes;
  }

  _renderEdgeFilter() {
    if (!this.edgeFilterEl) return;
    clear(this.edgeFilterEl);
    const counts = new Map();
    for (const relation of this.relationTypes) counts.set(relation, 0);
    for (const edge of this.edges) counts.set(edge.relation, (counts.get(edge.relation) || 0) + 1);
    const relations = [...counts.entries()]
      .filter(([, count]) => count > 0)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([relation]) => relation);
    const selectedCount = relations.filter((relation) => this.visibleRelations.has(relation)).length;
    const all = relations.length > 0 && selectedCount === relations.length;
    this.edgeFilterEl.append(
      h("div", { class: "graph-edge-filter-head" },
        h("b", null, t("graph.visible_connections")),
        h("button", { class: "icon-btn", title: t("action.close"), onclick: () => this._toggleEdgeFilter(false) }, icon("x", "sm"))));
    const allInput = h("input", {
      type: "checkbox", checked: all ? true : undefined,
      onchange: (event) => {
        this.visibleRelations = event.target.checked ? new Set(relations) : new Set();
        this._saveRelationFilter();
        this._renderEdgeFilter();
        this._reheatRelationFilter();
      },
    });
    this.edgeFilterEl.append(h("label", { class: "graph-edge-filter-row all" },
      allInput, h("span", null, t("graph.edge_filter.select_all")), h("em", null, `${selectedCount}/${relations.length}`)));
    allInput.indeterminate = selectedCount > 0 && selectedCount < relations.length;
    if (this.layout === "mind") {
      const primaryOptions = [...relations];
      if (!primaryOptions.includes(this.primaryRelation)) primaryOptions.unshift(this.primaryRelation);
      const primarySelect = h("select", {
        class: "input graph-primary-relation",
        onchange: (event) => {
          this.primaryRelation = event.target.value;
          this._savePrimaryRelation();
          this.visibleRelations.add(this.primaryRelation);
          this._saveRelationFilter();
          this._renderEdgeFilter();
          this._applyLayout(false);
          this.fit({ animate: true });
          this._draw();
        },
      }, primaryOptions.map((relation) => h("option", {
        value: relation, selected: relation === this.primaryRelation ? true : undefined,
      }, `${relLabel(relation)} (${counts.get(relation) || 0})`)));
      this.edgeFilterEl.append(h("label", { class: "graph-edge-filter-primary" },
        h("span", null, t("graph.primary_relation")), primarySelect));
      const cycles = this._mindTree().cycleEdges.length;
      this.edgeFilterEl.append(h("div", { class: "graph-edge-filter-note" },
        t("graph.primary_relation_hint")));
      if (cycles) {
        this.edgeFilterEl.append(h("div", { class: "graph-edge-filter-warning" },
          t("graph.cycle_ignored", { n: cycles })));
      }
    }
    for (const relation of relations) {
      const checked = this.visibleRelations.has(relation);
      const input = h("input", {
        type: "checkbox", checked: checked ? true : undefined,
        onchange: (event) => {
          if (event.target.checked) this.visibleRelations.add(relation);
          else this.visibleRelations.delete(relation);
          this._saveRelationFilter();
          this._renderEdgeFilter();
          this._reheatRelationFilter();
        },
      });
      const style = this._arrows()(relation);
      const reversed = this._isReversed(relation);
      // Quick, display-only direction toggle for this graph (browser-local).
      const reverseBtn = h("button", {
        class: `graph-edge-reverse${reversed ? " on" : ""}`, type: "button",
        "aria-pressed": String(reversed), "data-reverse-relation": relation,
        title: t("graph.edge_filter.reverse_title"), "aria-label": `${t("graph.edge_filter.reverse_title")}: ${relLabel(relation)}`,
        onclick: (event) => {
          event.preventDefault();
          event.stopPropagation();
          this._setQuickReverse(relation, !reversed);
          this._renderEdgeFilter();
        },
      }, icon("arrowLeft", "sm"));
      this.edgeFilterEl.append(h("label", { class: "graph-edge-filter-row" },
        input, h("span", null, relLabel(relation)),
        arrowGlyph(style && reversed ? { ...style, start: style.end, end: style.start } : style),
        reverseBtn, h("em", null, String(counts.get(relation) || 0))));
    }
    this.edgeFilterEl.append(h("div", { class: "graph-edge-filter-note" }, t("graph.edge_filter.reverse_note")));
    if (this.layout === "node" && !this.readOnly) {
      this.edgeFilterEl.append(h("button", {
        class: "graph-edge-filter-reset", type: "button", "data-reverse-relayout": "",
        onclick: () => this.autoLayout(),
      }, t("graph.edge_filter.relayout")));
    }
    this.edgeFilterEl.append(h("button", {
      class: "graph-edge-filter-reset", type: "button",
      onclick: () => {
        this.visibleRelations = new Set(this.layout === "mind"
          ? [this.primaryRelation || "part_of"] : this.relationTypes);
        this._saveRelationFilter();
        this._renderEdgeFilter();
        this._reheatRelationFilter();
      },
    }, t(this.layout === "mind" ? "graph.edge_filter.reset" : "graph.edge_filter.reset_all")));
  }

  _onKeyDown(event) {
    if (this.editor) return;
    const tag = event.target?.tagName;
    if (["INPUT", "TEXTAREA", "SELECT"].includes(tag) || event.target?.isContentEditable) return;
    if ((event.ctrlKey || event.metaKey) && !event.shiftKey && event.key.toLowerCase() === "z") {
      event.preventDefault();
      this._undoLast();
      return;
    }
    if (event.key === "?") {
      event.preventDefault();
      this._toggleHelp();
      return;
    }
    if (event.key === "Escape" && this.helpEl && !this.helpEl.classList.contains("hidden")) {
      event.preventDefault();
      this._toggleHelp(false);
      return;
    }
    if (!event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
      && (event.key === "f" || event.key === "F")) {
      event.preventDefault();
      this.fit({ animate: true });
      return;
    }
    if (!event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
      && (event.key === "r" || event.key === "R")) {
      event.preventDefault();
      this.setMode("read");
      return;
    }
    if (!event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey
      && (event.key === "e" || event.key === "E")) {
      event.preventDefault();
      this.setMode("edit");
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "x") {
      event.preventDefault();
      this._cutSelection();
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "c") {
      event.preventDefault();
      this._copySelection(event.shiftKey);
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v") {
      event.preventDefault();
      this._pasteClipboard();
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a") {
      event.preventDefault();
      if (event.shiftKey && this.layout === "mind") this._selectSameLevel();
      else this._selectAllVisible();
      return;
    }
    if (this.layout === "mind" && !event.ctrlKey && !event.metaKey && !event.altKey) {
      if (event.key === "+" || event.key === "=") {
        event.preventDefault();
        this._expandSelected(false);
        return;
      }
      if (event.key === "-") {
        event.preventDefault();
        this._collapseSelected(false);
        return;
      }
      if (event.key === "*") {
        event.preventDefault();
        this._expandSelected(true);
        return;
      }
      if (event.key === "/") {
        event.preventDefault();
        this._collapseSelected(true);
        return;
      }
    }
    if (this.mode === "edit") {
      for (const [shortcutTag, spec] of Object.entries(getTagShortcuts())) {
        if (!shortcutMatches(event, spec)) continue;
        const selected = this._selectedNodeObjects();
        if (!selected.length) return;
        event.preventDefault();
        this._applyMindTag(shortcutTag);
        return;
      }
    }
    if (["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) {
      if (!this.nodes.length) return;
      event.preventDefault();
      this._navigateMind(event.key);
      return;
    }
    if (event.key === " ") {
      if (this.mode !== "edit") return;
      const node = this._selectedNodeObjects()[0];
      if (node) { event.preventDefault(); this.startEdit(node); }
      return;
    }
    if (event.key === "Tab") {
      if (this.mode !== "edit") { event.preventDefault(); return; }
      if (!this._selectedNodeObjects().length) return;
      event.preventDefault();
      this._addMindNode("child");
      return;
    }
    if (event.altKey && !event.ctrlKey && !event.metaKey && event.key === "Enter") {
      const node = this._selectedNodeObjects()[0];
      if (!node || !this.opts.onOpenEditor) return;
      event.preventDefault();
      this.opts.onOpenEditor(node);
      return;
    }
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      if (this.layout !== "mind" || this.mode !== "edit") return;
      event.preventDefault();
      this._insertBetween();
      return;
    }
    if (event.key === "Enter") {
      if (!this._selectedNodeObjects().length) return;
      event.preventDefault();
      if (this.mode === "read") {
        const node = this._selectedNodeObjects()[0];
        this.opts.onSelect?.(node, false, event);
      } else {
        this._addMindNode(event.shiftKey ? "above" : "below");
      }
      return;
    }
    if (event.key === "Backspace") {
      // Backspace is deliberately not a delete shortcut on graph focus.
      event.preventDefault();
      return;
    }
    if (event.key === "Delete") {
      event.preventDefault();
      if (this.mode === "edit") this._deleteSelected();
      return;
    }
    if (event.key === "Escape") {
      if (this._lineDragActive()) {
        this._finishLineDrag(null, { cancel: true });
        return;
      }
      this._setSelection([], false);
      this._draw();
    }
  }

  _mindParents() {
    const parents = this.opts.mindParents?.();
    return parents instanceof Map ? parents : null;
  }

  _mindTree() {
    const { parentOf, children, cycleEdges, extraParentEdges, roots } =
      mindHierarchy(this.nodes, this._shownEdges(), this.primaryRelation, this._mindParents());
    const hidden = new Set();
    for (const node of this.nodes) {
      let parent = parentOf.get(node.id);
      const seen = new Set();
      while (parent && !seen.has(parent)) {
        if (this.collapsed.has(parent)) { hidden.add(node.id); break; }
        seen.add(parent);
        parent = parentOf.get(parent);
      }
    }
    const depth = new Map();
    const resolveDepth = (id, seen = new Set()) => {
      if (depth.has(id)) return depth.get(id);
      if (seen.has(id)) return 0;
      seen.add(id);
      const parent = parentOf.get(id);
      const value = parent ? resolveDepth(parent, seen) + 1 : 0;
      depth.set(id, value);
      return value;
    };
    for (const node of this.nodes) resolveDepth(node.id);
    return { parentOf, children, hidden, depth, cycleEdges, extraParentEdges, roots };
  }

  _visibleMindNodes() {
    const { hidden } = this._mindTree();
    return this.nodes.filter((node) => !hidden.has(node.id));
  }

  _collapseSelected(recursive = false) {
    this._setSelectedCollapsed(true, recursive);
  }

  _expandSelected(recursive = false) {
    this._setSelectedCollapsed(false, recursive);
  }

  _setSelectedCollapsed(collapse, recursive) {
    const selected = this._selectedNodeObjects();
    if (!selected.length) return;
    const { children, hidden: hiddenBefore } = this._mindTree();
    const snapshot = this._mindSnapshot();
    const stack = selected.map((node) => node.id);
    const visited = new Set();
    while (stack.length) {
      const id = stack.pop();
      if (visited.has(id)) continue;
      visited.add(id);
      if (collapse) this.collapsed.add(id);
      else this.collapsed.delete(id);
      if (recursive) stack.push(...(children.get(id) || []));
    }
    // Only the trees of the selected cards reflow (each anchored at its
    // first selected card), so the camera does not move either.
    const { hidden } = this._mindTree();
    for (const id of [...this.selectedNodes]) {
      if (hidden.has(id)) this.selectedNodes.delete(id);
    }
    const remaining = this._selectedNodeObjects();
    this.selected = remaining.length ? remaining[remaining.length - 1].id : null;
    this._applyLayout(false, { reflow: { changed: selected.map((node) => node.id), anchor: selected[0], hiddenBefore } });
    this._pushHistory({ undo: () => this._restoreMindSnapshot(snapshot) });
  }

  _selectAllVisible() {
    this._setSelection(this._visibleMindNodes(), false);
    this._draw();
  }

  _selectSameLevel() {
    const selected = this._selectedNodeObjects()[0];
    if (!selected) return;
    const { hidden, depth } = this._mindTree();
    const level = depth.get(selected.id) ?? 0;
    const same = this.nodes.filter((node) =>
      !hidden.has(node.id) && (depth.get(node.id) ?? 0) === level);
    this._setSelection(same, false);
    this._draw();
  }

  _navigateMind(key) {
    const current = this._selectedNodeObjects()[0] || this.nodes[0];
    if (!current) return;
    const dir = {
      ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1],
    }[key];
    if (!dir) return;
    const cx = current.x + (current.w || 160) / 2;
    const cy = current.y + (current.h || 46) / 2;
    let best = null;
    let bestScore = Infinity;
    for (const node of this.nodes) {
      if (this.hiddenIds?.has(node.id)) continue;
      if (node === current) continue;
      const dx = node.x + (node.w || 160) / 2 - cx;
      const dy = node.y + (node.h || 46) / 2 - cy;
      const along = dir[0] ? dx * dir[0] : dy * dir[1];
      if (along <= 1) continue;
      const cross = dir[0] ? Math.abs(dy) : Math.abs(dx);
      const score = along + cross * 2.2;
      if (score < bestScore) { bestScore = score; best = node; }
    }
    if (best) this._setSelection([best], false);
    this._draw();
  }

  async _applyMindTag(tag) {
    const nodes = this._selectedNodeObjects();
    if (!nodes.length || !this.opts.onAddTag) return;
    try {
      await this.opts.onAddTag(nodes, tag);
      for (const node of nodes) {
        node.tags = [...new Set([...(node.tags || []), tag])];
      }
      this._draw();
    } catch (err) {
      this.opts.onError?.(err);
    }
  }

  /**
   * Preview a card's media from a clicked thumbnail: the media lightbox
   * (image viewer, video/audio player, PDF viewer) over all of the node's
   * attachments, starting at the clicked one.  `opts.onMediaPreview(items,
   * index, node)` replaces it.
   */
  _previewCardMedia(hit) {
    const node = hit.node;
    const items = (node.media || []).filter((item) => item && (item.url || item.raw_url));
    let index = items.findIndex((item) => String(item.id) === String(hit.item.id));
    if (index < 0) { items.unshift(hit.item); index = 0; }
    this._lastMediaPreview = { node, items, index };
    if (this.opts.onMediaPreview) { this.opts.onMediaPreview(items, index, node); return; }
    import("./image-viewer.js").then(({ openMediaLightbox }) => openMediaLightbox({
      items, index, title: node.title || "", srcOf: imageMediaSrc,
      onOpen: this.opts.onMediaOpen ? (item) => this.opts.onMediaOpen(item) : null,
    })).catch((err) => this.opts.onError?.(err));
  }

  _openMindImageMenu(hit, event) {
    const item = hit.item;
    const node = hit.node;
    const edit = (action) => () => {
      if (this.mode !== "edit") this.setMode("edit");
      action();
    };
    const pinned = node.pinnedMeta && String(node.pinnedMeta.item.id) === String(item.id);
    const image = cachedMindImage(imageMediaSrc(item));
    const aspect = image?.naturalWidth ? image.naturalWidth / image.naturalHeight : 1;
    openContextMenu({
      x: event.clientX,
      y: event.clientY,
      items: [
        {
          label: t(pinned ? "graph.menu.unpin_image" : "graph.menu.pin_image"),
          icon: "pin",
          onClick: edit(() => {
            if (pinned) {
              delete node.pinnedImage;
            } else {
              node.pinnedImage = {
                mediaId: item.id, side: "right",
                w: 260, h: 260 / Math.max(0.2, aspect), aspect,
              };
            }
            this._saveMindPinnedImages();
            this._applyLayout(false);
          }),
        },
        {
          label: t("graph.menu.reset_image_size"),
          icon: "refresh",
          onClick: edit(() => {
            if (node.pinnedImage) {
              node.pinnedImage = {
                ...node.pinnedImage,
                w: 260, h: 260 / Math.max(0.2, aspect), aspect,
              };
              this._saveMindPinnedImages();
              this._applyLayout(false);
            }
          }),
        },
        { separator: true },
        { label: t("graph.menu.open_image"), icon: "external", onClick: () => this.opts.onMediaOpen?.(item) },
        this.opts.onMediaEditor ? { label: t("editor.open_in_editor"), icon: "edit", onClick: () => this.opts.onMediaEditor(item, node) } : null,
      ],
    });
  }

  _openGraphContextMenu(node, event) {
    const nodes = node
      ? (this.selectedNodes.has(node.id) ? this._selectedNodeObjects() : [node])
      : [];
    const edit = (action) => () => {
      if (this.mode !== "edit") this.setMode("edit");
      action();
    };
    const items = [];
    if (node) {
      items.push(historyMenuItem(node));
      items.push({ label: t("unit_menu.open_details"), icon: "search", shortcut: "Enter", onClick: () => this.opts.onSelect?.(node, false, event) });
      if (this.opts.onOpenEditor) {
        items.push({ label: t("editor.open_in_editor"), icon: "edit", shortcut: "Alt+Enter", onClick: () => this.opts.onOpenEditor(node) });
      }
      items.push({ label: t("action.copy"), icon: "copy", shortcut: "Ctrl+C", onClick: () => this._copySelection(false) });
      items.push({ label: t("graph.menu.copy_subtree"), icon: "copy", shortcut: "Ctrl+Shift+C", onClick: () => this._copySelection(true) });
      items.push({ separator: true });
      items.push({ label: t("graph.menu.edit_title"), icon: "pen", shortcut: "Space", onClick: edit(() => this.startEdit(node)) });
      if (this.opts.onAddNode) {
        items.push({ label: t("graph.menu.add_child"), icon: "plus", shortcut: "Tab", onClick: edit(() => this._addMindNode("child")) });
        items.push({ label: t("graph.menu.add_sibling_below"), icon: "plus", shortcut: "Enter", onClick: edit(() => this._addMindNode("below")) });
        items.push({ label: t("graph.menu.add_sibling_above"), icon: "plus", shortcut: "Shift+Enter", onClick: edit(() => this._addMindNode("above")) });
      }
      items.push({ separator: true });
      items.push({
        label: t("graph.menu.markers"), icon: "sparkles", children: MARKERS.map((marker) => ({
          label: `${marker.glyph} ${marker.label}`,
          onClick: edit(() => this._applyMindTag(marker.tag)),
        })),
      });
      const tags = Object.keys(getTagShortcuts());
      if (tags.length) {
        items.push({
          label: t("graph.menu.tags"), icon: "filter", children: tags.map((tag) => ({
            label: tag,
            shortcut: getTagShortcuts()[tag],
            onClick: edit(() => this._applyMindTag(tag)),
          })),
        });
      }
      // Account owner for the clicked node, or every selected node.
      const assign = assignMenuItem(nodes);
      if (assign) items.push(assign);
      items.push({ label: t("graph.menu.connect_to"), icon: "link", onClick: () => this.startConnect() });
      const extra = this.opts.nodeMenuExtras?.(nodes, node) || [];
      if (extra.length) items.push({ separator: true }, ...extra);
      items.push({ separator: true });
      items.push({ label: t("graph.menu.bulk_edit"), icon: "layers", onClick: edit(() => this.opts.onContextMenu?.(nodes, event)) });
      const exportItem = exportBundleMenuItem(nodes);
      if (exportItem) items.push(exportItem);
      items.push({
        label: t("unit_menu.copy_uid"), icon: "copy", onClick: () => copyText(nodes.map((item) => item.uid || item.id).join("\n")),
      });
      items.push({ separator: true });
      const deleteItems = this.opts.deleteMenuItems?.(nodes, { edit, removeSelected: () => this._deleteSelected() });
      if (deleteItems) items.push(...deleteItems);
      else items.push({ label: t("action.delete"), icon: "trash", danger: true, onClick: edit(() => this._deleteSelected()) });
    } else {
      items.push({ label: t("graph.menu.fit_graph"), icon: "fit", shortcut: "F", onClick: () => this.fit({ animate: true }) });
      items.push({ label: t("graph.menu.shortcuts"), icon: "info", shortcut: "?", onClick: () => this._toggleHelp(true) });
      items.push({ separator: true });
      items.push({
        label: t(this.mode === "read" ? "graph.menu.switch_to_edit" : "graph.menu.switch_to_read"),
        icon: "settings", shortcut: this.mode === "read" ? "E" : "R",
        onClick: () => this.setMode(this.mode === "read" ? "edit" : "read"),
      });
    }
    openContextMenu({ x: event.clientX, y: event.clientY, items });
  }

  _resize() {
    // The host's content box, floored to whole device pixels, so the canvas
    // never comes out larger than its container (no overflow, no scrollbar
    // that would change the width and resize it back: a feedback loop).
    const dpr = Math.min(2.5, window.devicePixelRatio || 1);
    const style = getComputedStyle(this.host);
    const rect = this.host.getBoundingClientRect();
    const px = (name) => parseFloat(style[name]) || 0;
    const contentW = rect.width - px("borderLeftWidth") - px("borderRightWidth") - px("paddingLeft") - px("paddingRight");
    const contentH = rect.height - px("borderTopWidth") - px("borderBottomWidth") - px("paddingTop") - px("paddingBottom");
    // Hidden or collapsed: keep the last size.  A view created hidden gets a
    // provisional size until it is shown (the observer resizes it then).
    if (contentW <= 0 || contentH <= 0) {
      if (this._deviceSize) return;
      this.width = this.width || 80;
      this.height = this.height || 80;
      this.canvas.width = Math.round(this.width * dpr);
      this.canvas.height = Math.round(this.height * dpr);
      this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      return;
    }
    const deviceW = Math.floor(contentW * dpr), deviceH = Math.floor(contentH * dpr);
    // Only a real change of size or dpr resizes (and clears) the canvas.
    if (this._deviceSize && this._deviceSize.w === deviceW && this._deviceSize.h === deviceH && this._deviceSize.dpr === dpr) return;
    this._deviceSize = { w: deviceW, h: deviceH, dpr };
    this.resizeCount = (this.resizeCount || 0) + 1;
    this.width = deviceW / dpr;
    this.height = deviceH / dpr;
    // Never move the camera because a panel opened, closed or the canvas
    // changed size. A zoomed-in user must stay exactly where they were.
    this.canvas.width = deviceW;
    this.canvas.height = deviceH;
    this.canvas.style.width = `${this.width}px`;
    this.canvas.style.height = `${this.height}px`;
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    // Scene tiles were rendered for the old size.
    this._sceneTiles = null;
    this._sceneTilesPrev = null;
    this.tools?.classList.toggle("narrow", this.tools.classList.contains("graph-tools-sidebar") && this.width < 340);
    const broken = this.nodes.filter((node) => !Number.isFinite(node.x) || !Number.isFinite(node.y));
    if (broken.length) {
      for (const node of broken) {
        node.x = (Math.random() - 0.5) * Math.max(120, this.width * 0.7);
        node.y = (Math.random() - 0.5) * Math.max(120, this.height * 0.7);
        node.vx = 0; node.vy = 0;
      }
      this.alpha = Math.max(this.alpha, 0.35);
      if (this.layout === "force") this._start();
    }
    if (this.nodes.length && !this._userInteracted) this.fit();
    else this._draw();
  }

  /**
   * Replace the data.  `remote` (a change from another device or user): the
   * camera and every existing position stay; new nodes start next to a
   * neighbour (or in the middle of the view); in Force only the new nodes
   * settle (low alpha, existing nodes held), nothing is refitted.
   */
  setData(nodes, edges, { selected = null, layout = null, preserveCamera = false, animate = false, warm = !animate, remote = false } = {}) {
    if (remote) preserveCamera = true;
    animate = animate && getMindSettings().nodeTransitions !== false;
    // A remote/revision update must not call fit(): fit() changes the camera
    // and resets the user-interaction flag.  Capture first, then restore after
    // positions/data change so only the graph content moves.
    if (preserveCamera) this._cancelCameraAnimation();
    const previousCamera = preserveCamera
      ? { zoom: this.zoom, pan: { ...this.pan } }
      : null;
    const cache = POS_CACHE.get(this.id) || new Map();
    POS_CACHE.set(this.id, cache);
    const old = new Map(this.nodes.map((n) => [n.id, n]));
    const incomingIds = new Set(nodes.map((n) => n.id));
    const now = performance.now();
    if (animate) {
      // Keep removed nodes as short-lived ghosts so a filter change reads as
      // nodes leaving the graph instead of the whole canvas blinking.
      this._ghosts = (this._ghosts || []).filter((ghost) => ghost._until > now && !incomingIds.has(ghost.id));
      for (const node of this.nodes) {
        if (!incomingIds.has(node.id)) {
          this._ghosts.push({ ...node, _born: now, _until: now + 240, _opacity: 1 });
        }
      }
    } else {
      this._ghosts = [];
      if (this._transitionFrame) cancelAnimationFrame(this._transitionFrame);
      this._transitionFrame = null;
    }
    let entered = 0;
    this.nodes = nodes.map((raw) => {
      const node = { ...raw };
      const prev = old.get(node.id) || cache.get(node.id);
      if (prev && Number.isFinite(prev.x) && Number.isFinite(prev.y)) {
        node.x = prev.x; node.y = prev.y;
        // A card dragged in Mind keeps its place through data refreshes.
        if (prev.mindManual) node.mindManual = true;
      }
      if (animate) {
        if (old.has(node.id)) node._appear = 1;
        else { node._appear = 0; entered += 1; }
      }
      return node;
    });
    this.byId = new Map(this.nodes.map((n) => [n.id, n]));
    this.history = [];
    for (const node of this.nodes) {
      const savedSize = this.imageSizes?.[node.uid];
      if (savedSize && Number.isFinite(savedSize.w) && Number.isFinite(savedSize.h)) node.imageSize = { ...savedSize };
      const savedWidth = Number(this.mindWidths?.[node.uid]);
      if (Number.isFinite(savedWidth) && savedWidth >= 126) node.customW = savedWidth;
      const pinned = this.mindPinnedImages?.[node.uid];
      if (pinned && pinned.mediaId != null) node.pinnedImage = { ...pinned };
    }
    this.collapsed = new Set([...this.collapsed].filter((id) => this.byId.has(id)));
    this.hiddenIds = new Set();
    this.edges = edges.filter((e) => this.byId.has(e.source) && this.byId.has(e.target));
    this.selected = selected;
    this.selectedNodes = new Set(selected ? [selected] : []);
    this.selectedEdge = null;
    this.hoverEdge = null;
    for (const node of this.nodes) {
      if (!Number.isFinite(node.vx)) node.vx = 0;
      if (!Number.isFinite(node.vy)) node.vy = 0;
    }
    if (layout) {
      const previousLayout = this.layout;
      this.layout = ["force", "tree", "mind", "node"].includes(layout) ? layout : "force";
      if (previousLayout !== this.layout) {
        this.primaryRelation = this._loadPrimaryRelation(this.layout);
        this.visibleRelations = this._initialRelations(this.layout);
      }
      if (!this.opts.compact && this.opts.persistLayout !== false) localStorage.setItem("ark.graphLayout", layout);
      this._syncNodeTools();
    }
    const missing = this.nodes.filter((n) => !Number.isFinite(n.x));
    if (missing.length === this.nodes.length) {
      if (this.layout === "force") randomStartPositions(this.nodes, this.width, this.height);
      else circlePositions(this.nodes, 230);
    } else if (missing.length) {
      // New nodes start near their first neighbour, or in a small ring.
      const firstEdge = new Map();
      for (const edge of this.edges) {
        if (!firstEdge.has(edge.source)) firstEdge.set(edge.source, edge);
        if (!firstEdge.has(edge.target)) firstEdge.set(edge.target, edge);
      }
      // Unrelated new nodes appear in the middle of the view, not at the
      // world origin (which may be far off screen).
      const centre = this.width ? this._world(this.width / 2, this.height / 2) : { x: 0, y: 0 };
      for (const node of missing) {
        const edge = firstEdge.get(node.id);
        const other = edge ? this.byId.get(edge.source === node.id ? edge.target : edge.source) : null;
        const anchor = other && Number.isFinite(other.x) ? other : centre;
        node.x = anchor.x + (Math.random() - 0.5) * 90;
        node.y = anchor.y + (Math.random() - 0.5) * 90;
      }
    }
    this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
    this.hintEl && (this.hintEl.textContent = this.layout === "mind" ? t("graph.mind_hint") : t("graph.hint"));
    this._rebuildLegend();
    this.emptyEl?.classList.toggle("hidden", this.nodes.length > 0);
    if (this.opts.allRelations) this.visibleRelations = this._initialRelations(this.layout);
    if (this.layout === "tree" || this.layout === "mind") {
      this._applyLayout(!preserveCamera);
    } else if (this.layout === "node") {
      this._layoutNodeGraph(false);
    } else if (remote) {
      // Only new nodes move: existing ones are held while they settle.  (A
      // simulation still running just takes the new nodes along.)
      this._held = missing.length && !this.running ? new Set(this.nodes.filter((node) => !missing.includes(node)).map((node) => node.id)) : null;
      if (this._held) this.alpha = Math.max(this.alpha, 0.3);
    } else if (!warm) {
      // Do not run the synchronous warm-up during a filter transition: it
      // moves every existing node before the first painted frame.  A gentle
      // reheat lets the force layout rebalance after the complete first paint.
      this.alpha = Math.max(this.alpha, 0.24);
    } else {
      this._warm();
      this.alpha = 1;
    }
    if (preserveCamera) {
      this._pendingForceFit = false;
      this.zoom = previousCamera.zoom;
      this.pan = previousCamera.pan;
      this._draw();
    } else {
      this.fit();
    }
    const camera = this.cameraKey ? GRAPH_CAMERAS.get(this.cameraKey) : null;
    if (camera && camera.layout === this.layout) {
      this.zoom = camera.zoom;
      this.pan = { ...camera.pan };
      this._draw();
    }
    this._fitted = true;
    if (remote) {
      // Nothing new: nothing moves.  New nodes: settle them, existing held.
      if (this._held) {
        this._remoteStart = true;
        try { this._start(); } finally { this._remoteStart = false; }
      } else this._draw();
    } else this._start();
    if (animate && (entered || this._ghosts.length)) this._animateTransition();
  }

  _animateTransition() {
    if (this._transitionFrame) cancelAnimationFrame(this._transitionFrame);
    const born = performance.now();
    const entering = this.nodes.filter((node) => node._appear < 1);
    const step = () => {
      const progress = Math.min(1, (performance.now() - born) / 240);
      for (const node of entering) node._appear = progress;
      for (const ghost of this._ghosts || []) ghost._opacity = 1 - progress;
      if (!this.running) this._draw();
      if (progress < 1) {
        this._transitionFrame = requestAnimationFrame(step);
      } else {
        this._ghosts = [];
        this._transitionFrame = null;
        this._draw();
      }
    };
    step();
  }

  _palette() {
    const css = getComputedStyle(document.documentElement);
    const get = (name, fallback) => (css.getPropertyValue(name) || "").trim() || fallback;
    const pal = {
      ink: get("--ink", "#e8eef6"),
      sub: get("--sub", "#93a2b6"),
      line: get("--line", "#273243"),
      bg: get("--bg", "#0f141c"),
      accent: get("--accent", "#3b82f6"),
      lane: get("--lane", "#161d27"),
      card: get("--card", "#171f2b"),
    };
    pal.key = `${pal.ink}${pal.sub}${pal.bg}${pal.accent}${pal.card}`;
    return pal;
  }

  _warm() {
    if (!this.nodes.length) return;
    const ticks = this.nodes.length > 300 ? 14 : this.nodes.length > 120 ? 34 : 72;
    for (let i = 0; i < ticks; i++) {
      this._physics();
      this.alpha *= 0.97;
    }
    this.alpha = 1;
  }

  setPhysics(value) {
    this.physics = normalizeGraphPhysics(value);
    this.alpha = 1;
    if (this.layout === "force") this._start();
    this._draw();
  }

  // Pin this view's line-end settings (null: follow the active settings).
  setArrows(value) {
    this.arrowSettings = value ? normalizeArrowSettings(value) : null;
    this._draw();
  }

  _arrows() {
    return arrowResolver(this.arrowSettings || ACTIVE_ARROWS);
  }

  // ---- Display-only direction ---------------------------------------
  // A relation can be shown reversed (target -> source): arrow settings set
  // the default (project, then personal) and the link filter keeps quick
  // per-graph toggles in this browser.  Stored links never change; only
  // drawing, layout input and port sides use the shown direction.
  _reverseOverrideKey() {
    return `${this.edgeFilterKey}:reverse`;
  }

  _loadReverseOverrides() {
    try {
      const raw = JSON.parse(localStorage.getItem(this._reverseOverrideKey()) || "{}");
      if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
      return Object.fromEntries(Object.entries(raw).filter(([, value]) => typeof value === "boolean"));
    } catch {
      return {};
    }
  }

  _reverseOverrides() {
    if (!this._quickReverse) this._quickReverse = this._loadReverseOverrides();
    return this._quickReverse;
  }

  /** relation -> shown reversed?  Memoized per settings + quick toggles. */
  _isReversed(relation) {
    const resolver = this._arrows();
    const overrides = this._reverseOverrides();
    let cache = this._reverseCache;
    if (!cache || cache.resolver !== resolver || cache.overrides !== overrides) {
      cache = this._reverseCache = { resolver, overrides, map: new Map() };
    }
    if (!cache.map.has(relation)) {
      const quick = overrides[relation];
      cache.map.set(relation, typeof quick === "boolean" ? quick : resolver.reversed(relation));
    }
    return cache.map.get(relation);
  }

  _setQuickReverse(relation, reversed) {
    const next = { ...this._reverseOverrides() };
    if (reversed === this._arrows().reversed(relation)) delete next[relation];
    else next[relation] = reversed;
    this._quickReverse = next;
    try {
      if (Object.keys(next).length) localStorage.setItem(this._reverseOverrideKey(), JSON.stringify(next));
      else localStorage.removeItem(this._reverseOverrideKey());
    } catch { /* storage can be unavailable */ }
    this._nodeRouteCache = null;
    this._nodeWaypoints = null;
    if (this.layout === "mind" || this.layout === "tree") this._applyLayout(false);
    this._draw();
  }

  /**
   * Edges in their shown direction: reversed relations become copies with
   * source/target swapped (`shownReversed`).  Edges a caller oriented for
   * structure (`treeEdge` / `reversed`, the local graph's mind tree) are left
   * alone.  Identity is by `id`; hit tests map back to the stored edge.
   */
  _shownEdges(edges = this.edges) {
    return edges.map((edge) => (!edge.reversed && !edge.treeEdge && this._isReversed(edge.relation)
      ? { ...edge, source: edge.target, target: edge.source, shownReversed: true } : edge));
  }

  reheat() {
    if (!this.nodes.length) return;
    if (this.layout === "mind" || this.layout === "node") {
      // Re-layout from scratch (dragged Mind cards included).
      for (const node of this.nodes) delete node.mindManual;
      this._mindOffsets = new Map();
      this._mindShift = null;
      if (this.layout === "node") this._layoutNodeGraph(true, true);
      else this._applyLayout(true);
      return;
    }
    randomStartPositions(this.nodes, this.width, this.height);
    this.alpha = 1;
    this.fit();
    this._start();
    this._draw();
  }

  /**
   * Lay out the current view.  Mind: every tree has the compact shape for
   * the current fold state (mind-layout.js), moved by the offset a fold
   * reflow gave it (`_mindOffsets`, cleared by Auto layout); dragged cards
   * keep their positions.  `reflow: { changed, anchor }` is a fold or
   * unfold: see _reflowMindTrees.
   */
  _applyLayout(fit = true, { reflow = null } = {}) {
    if (!this.nodes.length) return;
    if (this.layout === "node") {
      this._layoutNodeGraph(false);
      return;
    }
    if (this.layout === "mind") {
      const settings = getMindSettings();
      const before = new Map(this.nodes.map((node) => [node.id, { x: node.x, y: node.y }]));
      const hiddenBefore = reflow ? (reflow.hiddenBefore || new Set()) : null;
      const { treeOf, hidden } = mindLayout(this.nodes, this._shownEdges(), {
        align: settings.align, primaryRelation: this.primaryRelation, parents: this._mindParents(),
        collapsed: this.collapsed, showImages: settings.showMindImages !== false,
      }) || {};
      // The map's bounding box sits on the Force gravity centre.  The shift
      // is taken once (first layout, layout switch, Auto layout) and kept, so
      // folds and refreshes never move the whole map.
      if (!this._mindShift) {
        const box = layoutBox(this.nodes.filter((node) => !hidden?.has(node.id)));
        this._mindShift = box ? { dx: -box.cx, dy: -box.cy } : { dx: 0, dy: 0 };
      }
      for (const node of this.nodes) { node.x += this._mindShift.dx; node.y += this._mindShift.dy; }
      const rootOf = (node) => (treeOf?.get(node.id) ?? node.id);
      if (reflow) this._reflowMindTrees(before, rootOf, reflow);
      const offsets = this._mindOffsets || new Map();
      for (const node of this.nodes) {
        const offset = offsets.get(rootOf(node));
        if (offset) { node.x += offset.dx; node.y += offset.dy; }
        const old = before.get(node.id);
        if (node.mindManual && old && Number.isFinite(old.x)) { node.x = old.x; node.y = old.y; }
      }
      if (reflow) {
        // Cards that appear grow out of the card that was unfolded.
        const from = new Map(before);
        const { hidden } = this._mindTree();
        const start = reflow.changed.map((id) => before.get(id)).find(Boolean);
        for (const node of this.nodes) {
          if (start && !hidden.has(node.id) && hiddenBefore.has(node.id)) from.set(node.id, start);
        }
        if (this._animateNodePositions(from, 200)) return;
      }
    } else treeLayout(this.nodes, this._shownEdges(), false);
    if (fit) this.fit();
    else this._draw();
  }

  /**
   * A fold or unfold (XMind-like): the trees holding the `changed` cards
   * reflow to their compact shape, translated so the `anchor` card (the one
   * clicked) keeps its place, and their dragged cards rejoin the layout.
   * Every other tree and the orphan area keep their positions.  A reflowed
   * tree that would overlap another tree or an orphan moves up or down by
   * the smallest amount that clears it.  Sets `_mindOffsets`.
   */
  _reflowMindTrees(before, rootOf, { changed, anchor }) {
    const offsets = new Map();
    const affected = new Set(changed.map((id) => this.byId.get(id)).filter(Boolean).map(rootOf));
    const anchorOf = new Map();
    for (const id of [anchor?.id, ...changed]) {
      const node = this.byId.get(id);
      if (node && !anchorOf.has(rootOf(node))) anchorOf.set(rootOf(node), node);
    }
    for (const node of this.nodes) {
      const root = rootOf(node);
      if (affected.has(root)) {
        delete node.mindManual;
        continue;
      }
      // Unaffected trees and orphans stay where they were (measured on a
      // card that was not dragged).
      if (node.mindManual || offsets.has(root)) continue;
      const old = before.get(node.id);
      if (old && Number.isFinite(old.x)) offsets.set(root, { dx: old.x - node.x, dy: old.y - node.y });
    }
    for (const root of affected) {
      const node = anchorOf.get(root);
      const old = node && before.get(node.id);
      offsets.set(root, old && Number.isFinite(old.x) ? { dx: old.x - node.x, dy: old.y - node.y } : { dx: 0, dy: 0 });
    }
    // Boxes of each tree's visible cards after the reflow.
    const { hidden } = this._mindTree();
    const boxes = new Map();
    for (const node of this.nodes) {
      if (hidden.has(node.id) || node.mindManual) continue;
      const root = rootOf(node);
      const off = offsets.get(root) || { dx: 0, dy: 0 };
      const x = node.x + off.dx, y = node.y + off.dy, hw = (node.w || 160) / 2, hh = (node.h || 40) / 2;
      const box = boxes.get(root) || { x0: Infinity, x1: -Infinity, y0: Infinity, y1: -Infinity };
      box.x0 = Math.min(box.x0, x - hw); box.x1 = Math.max(box.x1, x + hw);
      box.y0 = Math.min(box.y0, y - hh); box.y1 = Math.max(box.y1, y + hh);
      boxes.set(root, box);
    }
    for (const root of affected) {
      const box = boxes.get(root);
      if (!box) continue;
      const others = [...boxes].filter(([id]) => id !== root).map(([, b]) => b)
        .filter((b) => b.x0 < box.x1 && b.x1 > box.x0);
      const clear = (dy) => others.every((b) => b.y1 + TREE_GAP <= box.y0 + dy || b.y0 - TREE_GAP >= box.y1 + dy);
      if (clear(0)) continue;
      const shifts = others.flatMap((b) => [b.y1 + TREE_GAP - box.y0, b.y0 - TREE_GAP - box.y1])
        .filter(clear).sort((a, b) => Math.abs(a) - Math.abs(b));
      if (!shifts.length) continue;
      offsets.get(root).dy += shifts[0];
      box.y0 += shifts[0];
      box.y1 += shifts[0];
    }
    this._mindOffsets = offsets;
  }

  setMode(mode, { persist = true } = {}) {
    if (this.readOnly) mode = "read";
    this.mode = mode === "edit" ? "edit" : "read";
    this.host.dataset.mode = this.mode;
    this.modeReadBtn?.classList.toggle("on", this.mode === "read");
    this.modeEditBtn?.classList.toggle("on", this.mode === "edit");
    if (persist && this.opts.persistMode !== false) {
      try { localStorage.setItem(`ark.graphMode.${this.layout}`, this.mode); } catch { /* ignore */ }
    }
    if (this.mode === "read") {
      this._closeEditor(true);
      this.cancelConnect?.();
    }
    this.opts.onModeChange?.(this.mode);
    this._draw();
  }

  setLayout(layout) {
    if (!["force", "tree", "mind", "node"].includes(layout)) return;
    this.layout = layout;
    this.primaryRelation = this._loadPrimaryRelation(layout);
    this.visibleRelations = this._initialRelations(layout);
    if (!this.opts.compact && this.opts.persistLayout !== false) localStorage.setItem("ark.graphLayout", layout);
    this._closeEditor();
    if (this.opts.compact) return;
    const savedMode = (() => { try { return localStorage.getItem(`ark.graphMode.${layout}`); } catch { return null; } })();
    this.setMode(savedMode === "read" || savedMode === "edit" ? savedMode
      : (layout === "mind" ? "edit" : "read"), { persist: false });
    this._syncEdgeFilter();
    this._syncNodeTools();
    this.hintEl && (this.hintEl.textContent = layout === "mind" ? t("graph.mind_hint") : t("graph.hint"));
    this._pendingForceFit = layout === "force";
    if (layout === "mind") { this._mindShift = null; this._mindOffsets = new Map(); }
    if (layout === "force") {
      this._centreOnGravity();
      this.alpha = 1;
      this._start();
      this.fit({ animate: true });
    } else if (layout === "node") {
      if (!this._nodeFilterInitialized) {
        this.visibleRelations = new Set(this.relationTypes);
        this._nodeFilterInitialized = true;
      }
      // Cards ease from their previous (force/mind) positions.
      this._layoutNodeGraph(false, false, { animate: true, onSettled: () => this.fit({ animate: true }) });
    } else {
      this._applyLayout(false);
      this.fit({ animate: true });
    }
    this._draw();
  }

  // Move every node so the centroid is the Force gravity centre (the world
  // origin), keeping relative positions; the camera moves by the same amount,
  // so the picture does not jump.
  _centreOnGravity() {
    const nodes = this.nodes.filter((node) => Number.isFinite(node.x) && Number.isFinite(node.y));
    if (!nodes.length) return;
    const cx = nodes.reduce((sum, node) => sum + node.x, 0) / nodes.length;
    const cy = nodes.reduce((sum, node) => sum + node.y, 0) / nodes.length;
    if (Math.abs(cx) < 1e-6 && Math.abs(cy) < 1e-6) return;
    for (const node of nodes) { node.x -= cx; node.y -= cy; }
    // A camera animation in flight aimed at the old coordinates.
    this._cancelCameraAnimation();
    this.pan.x += cx * this.zoom;
    this.pan.y += cy * this.zoom;
  }

  _rebuildLegend() {
    if (!this.legendEl) return;
    const counts = new Map();
    for (const node of this.nodes) counts.set(node.type, (counts.get(node.type) || 0) + 1);
    clear(this.legendEl);
    for (const [type, count] of [...counts.entries()].sort((a, b) => b[1] - a[1])) {
      this.legendEl.appendChild(h("span", null,
        h("i", { style: { background: typeColor(type) } }), `${typeLabel(type)} ${count}`));
    }
  }

  _start() {
    if (!this.nodes.length) return;
    for (const node of this.nodes) {
      if (!Number.isFinite(node.x) || !Number.isFinite(node.y)) {
        node.x = (Math.random() - 0.5) * Math.max(120, this.width * 0.6);
        node.y = (Math.random() - 0.5) * Math.max(120, this.height * 0.6);
      }
      if (!Number.isFinite(node.vx)) node.vx = 0;
      if (!Number.isFinite(node.vy)) node.vy = 0;
    }
    // Every (re)start measures settling afresh; anything but a remote update
    // moves the whole graph again.
    this._settle = { ticks: 0, quiet: 0 };
    if (!this._remoteStart) this._held = null;
    if (this.running) return;
    this.running = true;
    const step = () => {
      if (!this.running) return;
      // Run until the nodes have settled (FORCE_SETTLE), not for a fixed
      // number of ticks; scene tiles wait for the same signal (`running`).
      let verdict = "stop";
      if (this.layout === "force") {
        const motion = this._physics();
        this.alpha *= 0.985;
        verdict = forceSettleStep(this._settle, motion, this.opts.backdrop ? 1 : this.zoom, this.alpha, Boolean(this.drag?.node));
        this._lastSettle = { ...this._settle, verdict, mean: motion.mean, max: motion.max, zoom: this.zoom };
        this._draw();
      }
      if (verdict === "run") {
        this.frame = requestAnimationFrame(step);
      } else {
        this.running = false;
        const shouldFit = this._pendingForceFit && this.layout === "force" && !this._userInteracted;
        this._pendingForceFit = false;
        if (shouldFit) this.fit({ animate: true });
        else this._draw();
      }
    };
    this.frame = requestAnimationFrame(step);
  }

  _physics() {
    const nodes = this.nodes;
    if (!nodes.length) return { mean: 0, max: 0 };
    // Held nodes (a remote update settling only its new nodes) keep their place.
    const held = this._held ? nodes.filter((node) => this._held.has(node.id)).map((node) => [node, node.x, node.y]) : null;
    stepForce(nodes, this.edges, this.byId, this.physics, this.visibleRelations, this.drag?.node, applyRepulsion);
    if (held) for (const [node, x, y] of held) { node.x = x; node.y = y; node.vx = 0; node.vy = 0; }
    // Past the classic stopping point, cool the motion with alpha so the
    // graph settles instead of drifting on.
    const cool = this.alpha < FORCE_SETTLE.coolFrom ? this.alpha / FORCE_SETTLE.coolFrom : 1;
    let sum = 0, max = 0;
    for (const node of nodes) {
      if (cool < 1) {
        node.x -= node.vx * (1 - cool); node.y -= node.vy * (1 - cool);
        node.vx *= cool; node.vy *= cool;
      }
      const moved = Math.hypot(node.vx, node.vy);
      sum += moved;
      if (moved > max) max = moved;
    }
    const cache = POS_CACHE.get(this.id);
    if (cache) for (const node of nodes) cache.set(node.id, { x: node.x, y: node.y });
    return { mean: sum / nodes.length, max };
  }

  _transform() {
    if (this._tileTransform) return this._tileTransform;
    return { x: this.width / 2 + this.pan.x, y: this.height / 2 + this.pan.y, z: this.zoom };
  }

  _screen(node) {
    const tr = this._transform();
    return { x: node.x * tr.z + tr.x, y: node.y * tr.z + tr.y };
  }

  _keepNodeScreen(node, before) {
    const after = this._screen(node);
    this.pan.x += before.x - after.x;
    this.pan.y += before.y - after.y;
    this._draw();
  }

  _world(px, py) {
    const tr = this._transform();
    return { x: (px - tr.x) / tr.z, y: (py - tr.y) / tr.z };
  }

  _draw() {
    const ctx = this.ctx;
    if (!ctx) return;
    if (this.layout === "mind") { this._drawMind(); return; }
    if (this.layout === "node") { this._drawNode(); return; }
    if (!this._tileRender && this._sceneTilesUsable() && this._drawSceneTiles()) return;
    const directStart = this._tileRender ? 0 : performance.now();
    this.hiddenIds = new Set();
    const { width: w, height: hgt } = this;
    ctx.clearRect(0, 0, w, hgt);
    if (!this.nodes.length && !(this._ghosts || []).length) return;
    if (this.opts.backdrop) this._fitBackdrop();
    const camera = this._transform();
    const tr = this.opts.backdrop ? { x: 0, y: 0, z: 1 } : camera;
    if (this.opts.backdrop) {
      // Scale the entire Map rendering, including markers and edge widths.
      ctx.save();
      ctx.translate(camera.x, camera.y);
      ctx.scale(camera.z, camera.z);
    }
    const focusId = this.selected || this.hover?.id;
    const focusSeeds = new Set(this.selectedNodes || []);
    if (this.selected) focusSeeds.add(this.selected);
    if (!focusSeeds.size && this.hover?.id) focusSeeds.add(this.hover.id);
    const focused = focusNeighborhood(this.edges, focusSeeds, this.visibleRelations);
    const markerSettings = getMindSettings();
    const pal = this._palette();

    // Edges first.  Honour the same visibility set used by Mind/Node.
    // Arrowheads are batched into one path per tone.  Like labels they have a
    // level of detail: quiet ones disappear at extreme zoom-out, and a tone
    // with more on-screen arrows than ARROW_BUDGET (a hairball, where heads are
    // noise and each costs canvas calls) is skipped.  The hovered or selected
    // edge always keeps its arrow.
    const edgePaths = new Map();
    const arrowColors = [pal.accent, withAlpha(pal.accent, 0.92), withAlpha(pal.sub, 0.18), withAlpha(pal.sub, 0.62)];
    const arrowTips = [[], [], [], []];
    const arrowOf = this._arrows();
    const cull = !this.opts.backdrop;
    const margin = 40, maxX = w + margin, maxY = hgt + margin;
    const quietArrows = !this.opts.backdrop && tr.z >= 0.3;
    for (const edge of this._shownEdges()) {
      if (!this.visibleRelations.has(edge.relation)) continue;
      const a = this.byId.get(edge.source), b = this.byId.get(edge.target);
      if (!a || !b) continue;
      const ax = a.x * tr.z + tr.x, ay = a.y * tr.z + tr.y;
      const bx = b.x * tr.z + tr.x, by = b.y * tr.z + tr.y;
      if (cull && ((ax < -margin && bx < -margin) || (ay < -margin && by < -margin)
        || (ax > maxX && bx > maxX) || (ay > maxY && by > maxY))) continue;
      const hovered = this.hoverEdge && edge.id === this.hoverEdge.id;
      const selected = this.selectedEdge && edge.id === this.selectedEdge.id;
      const active = hovered || selected || (focusId && (edge.source === focusId || edge.target === focusId))
        || this.selectedNodes?.has(edge.source) || this.selectedNodes?.has(edge.target);
      const dim = focused && !active;
      const color = hovered || selected ? pal.accent : active ? withAlpha(pal.accent, 0.85) : dim ? withAlpha(pal.sub, 0.12) : withAlpha(pal.sub, 0.28);
      const width = hovered ? 2.6 : selected ? 2.4 : active ? 1.6 : 1;
      const key = `${color}:${width}`;
      if (!edgePaths.has(key)) edgePaths.set(key, { path: new Path2D(), color, width });
      const path = edgePaths.get(key).path;
      path.moveTo(ax, ay);
      path.lineTo(bx, by);
      const style = active || quietArrows ? arrowOf(edge.relation) : null;
      if (style) {
        // Tips stop at the node's rim (and its selection ring), so they are
        // never hidden under the node drawn on top.
        const dx = bx - ax, dy = by - ay, len = Math.hypot(dx, dy);
        const rimB = this._nodeRadius(b, tr.z) + (this.selectedNodes.has(b.id) ? 6.5
          : b.type === "topic" || b.status === "blocked" ? 4.5 : 1.5);
        const rimA = this._nodeRadius(a, tr.z) + (this.selectedNodes.has(a.id) ? 6.5
          : a.type === "topic" || a.status === "blocked" ? 4.5 : 1.5);
        const size = arrowSize(width, tr.z, style.size);
        if (len > rimA + rimB + size * (style.start && style.end ? 2.4 : 1.2) + 2) {
          const ux = dx / len, uy = dy / len;
          const tips = arrowTips[hovered || selected ? 0 : active ? 1 : dim ? 2 : 3];
          const head = ARROW_HEADS.indexOf(style.head);
          if (style.end) tips.push(bx - ux * rimB, by - uy * rimB, ux, uy, size, head);
          if (style.start) tips.push(ax + ux * rimA, ay + uy * rimA, -ux, -uy, size, head);
        }
      }
    }
    for (const { path, color, width } of edgePaths.values()) {
      ctx.strokeStyle = color; ctx.lineWidth = width; ctx.stroke(path);
    }
    // One path per (head, tone).
    for (let tone = 0; tone < arrowTips.length; tone++) {
      const tips = arrowTips[tone];
      // A scene tile sees only part of the view: it repeats the last direct
      // frame's decision instead of counting its own arrows.
      const skip = this._tileRender ? Boolean(this._tileArrowSkip?.[tone]) : Boolean(tone && tips.length > ARROW_BUDGET * 6);
      if (!this._tileRender) (this._arrowSkip ||= [])[tone] = skip;
      if (!tips.length || skip) continue;
      const paths = new Map();
      for (let i = 0; i < tips.length; i += 6) {
        const head = tips[i + 5];
        let path = paths.get(head);
        if (!path) paths.set(head, path = new Path2D());
        addArrowhead(path, tips[i], tips[i + 1], tips[i + 2], tips[i + 3], tips[i + 4], ARROW_HEADS[head]);
      }
      for (const [head, path] of paths) {
        paintArrowheads(ctx, path, ARROW_HEADS[head], arrowColors[tone], pal.bg, tone === 0 ? 2 : 1.4);
      }
    }

    // Link-type labels sit above all edge strokes but below every node.
    if (this.selected && tr.z > 0.7) {
      ctx.font = "9.5px ui-monospace, Consolas, monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "top";
      for (const edge of this.edges) {
        if (!this.visibleRelations.has(edge.relation)) continue;
        if (edge.source !== this.selected && edge.target !== this.selected) continue;
        const a = this.byId.get(edge.source), b = this.byId.get(edge.target);
        if (!a || !b) continue;
        const ax = a.x * tr.z + tr.x, ay = a.y * tr.z + tr.y;
        const bx = b.x * tr.z + tr.x, by = b.y * tr.z + tr.y;
        const mx = (ax + bx) / 2, my = (ay + by) / 2;
        const text = relLabel(edge.relation);
        const w = ctx.measureText(text).width;
        ctx.fillStyle = withAlpha(pal.bg, 0.88);
        roundRect(ctx, mx - w / 2 - 3, my - 8, w + 6, 14, 3);
        ctx.fill();
        ctx.fillStyle = withAlpha(pal.sub, 0.95);
        ctx.fillText(text, mx - w / 2, my - 6);
      }
    }

    // Removed nodes linger for a few frames while the filter transition runs.
    for (const ghost of this._ghosts || []) {
      if (!Number.isFinite(ghost.x) || !Number.isFinite(ghost.y)) continue;
      const gx = ghost.x * tr.z + tr.x, gy = ghost.y * tr.z + tr.y;
      if (!this.opts.backdrop && (gx < -60 || gy < -60 || gx > this.width + 60 || gy > this.height + 60)) continue;
      const gr = this._nodeRadius(ghost, tr.z);
      ctx.globalAlpha = Math.max(0, ghost._opacity || 0) * 0.7;
      ctx.beginPath();
      ctx.arc(gx, gy, gr, 0, Math.PI * 2);
      ctx.fillStyle = ghost.customColor || typeColor(ghost.type);
      ctx.fill();
    }
    ctx.globalAlpha = 1;

    // Nodes.
    for (const node of this.nodes) {
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      if (!this.opts.backdrop && (sx < -60 || sy < -60 || sx > this.width + 60 || sy > this.height + 60)) continue;
      const r = this._nodeRadius(node, tr.z);
      const dim = focused && !focused.has(node.id);
      const color = node.customColor || typeColor(node.type);
      const appear = Number.isFinite(node._appear) ? node._appear : 1;
      ctx.globalAlpha = (dim ? 0.22 : node.context ? 0.45 : 1) * appear;
      const sprite = this._nodeSprite(node, r, color, pal.bg);
      ctx.drawImage(sprite.canvas, sx - sprite.size / 2, sy - sprite.size / 2, sprite.size, sprite.size);
      if (node.type === "topic") {
        ctx.beginPath();
        ctx.arc(sx, sy, r + 3.5, 0, Math.PI * 2);
        ctx.strokeStyle = withAlpha(color, 0.9);
        ctx.lineWidth = 1.4;
        ctx.setLineDash([3, 2.5]);
        ctx.stroke();
        ctx.setLineDash([]);
        if (node.member_count && tr.z > 0.6) {
          ctx.save();
          ctx.font = `700 ${clamp(r * 0.95, 7, 12)}px system-ui, 'Segoe UI'`;
          ctx.textAlign = "center";
          ctx.textBaseline = "middle";
          ctx.fillStyle = pal.bg;
          ctx.fillText(String(node.member_count), sx, sy + 0.5);
          ctx.restore();
        }
      }
      const nodeMarks = markersFor(node).slice(0, 4);
      if (nodeMarks.length && tr.z > 0.55) {
        const markerSize = Math.max(4, r * 0.52 * markerSettings.markerScale);
        const arc = markerArc(nodeMarks, sx, sy, r + markerSize * markerSettings.markerGap, -Math.PI / 4, markerSettings.markerArc);
        ctx.save();
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.font = `700 ${markerSize}px system-ui, 'Segoe UI Symbol'`;
        ctx.lineJoin = "round";
        ctx.strokeStyle = "rgba(255,255,255,.94)";
        ctx.lineWidth = Math.max(1, markerSize * 0.15);
        for (const spot of arc) {
          ctx.strokeText(spot.marker.glyph, spot.x, spot.y);
          ctx.fillStyle = spot.marker.color;
          ctx.fillText(spot.marker.glyph, spot.x, spot.y);
        }
        ctx.restore();
      }
      if (this.selectedNodes.has(node.id)) {
        ctx.beginPath(); ctx.arc(sx, sy, r + (node.id === this.selected ? 5 : 4), 0, Math.PI * 2);
        ctx.strokeStyle = pal.accent; ctx.lineWidth = node.id === this.selected ? 2.4 : 2; ctx.stroke();
      }
      if (node.id === this.hover?.id) {
        ctx.beginPath(); ctx.arc(sx, sy, r + 4, 0, Math.PI * 2);
        ctx.strokeStyle = withAlpha(pal.ink, 0.65); ctx.lineWidth = 1; ctx.stroke();
      }
      ctx.globalAlpha = 1;
    }

    // Scene tiles hold everything up to here; labels (chosen per view) and
    // overlays are drawn per frame on top.
    if (this._tileRender) return;
    this._drawForceLabels(ctx, tr, focused, pal);
    this._lastDirectMs = performance.now() - directStart;
    if (this.linkDrag) this._drawLinkDrag(ctx, this._screen(this.linkDrag.source), pal);
    this._drawSelectionBox(ctx);
    if (this.opts.backdrop) ctx.restore();
    this._positionEditor();
  }

  // Force labels in screen space for crisp text and collision control: the
  // focus always, others above zoom 0.3 by degree within a budget, never
  // overlapping (the 7f52d0d rules).
  _drawForceLabels(ctx, tr, focused, pal) {
    if (!this.showLabels) return;
    {
      const labels = [];
      const focusedSet = focused || new Set();
      for (const node of this.nodes) {
        const positions = this._screen(node);
        if (positions.x < 0 || positions.y < 0 || positions.x > this.width || positions.y > this.height) continue;
        const selected = this.selectedNodes.has(node.id);
        const hovered = node.id === this.hover?.id;
        const must = selected || hovered || focusedSet.has(node.id);
        // Compact graphs label only the focus, unless asked (local graph).
        if (!must && this.opts.compact && !this.opts.compactLabels) continue;
        if (!must && !this.opts.compact && tr.z < 0.3) continue;
        labels.push({ node, x: positions.x, y: positions.y, must, selected, hovered, dim: focused && !focusedSet.has(node.id) });
      }
      // Priority: the hovered node, selected nodes, highlighted neighbours,
      // then the rest; within each, larger nodes (radius, then degree)
      // first.  A label that would overlap a higher-priority one is hidden.
      const rank = (item) => (item.hovered ? 0 : item.selected ? 1 : item.must ? 2 : 3);
      const size = (node) => node.radius || 7 + Math.sqrt(node.degree || 0) * 2.1;
      labels.sort((a, b) => rank(a) - rank(b) || size(b.node) - size(a.node) || (b.node.degree || 0) - (a.node.degree || 0));
      const boxes = [];
      const budget = this.opts.labelBudget || this.nodes.length > 400 ? Math.max(4, Math.ceil(16 * tr.z ** 2)) : Infinity;
      const candidateBudget = this.nodes.length > 400 ? 200 : Infinity;
      let candidates = 0;
      let normalLabels = 0;
      // Every line is centred under its node.  Set here, never inherited from
      // other text (topic counts, markers, port labels of other layouts).
      ctx.textAlign = "center";
      ctx.textBaseline = "top";
      ctx.direction = "ltr";
      for (const item of labels) {
        if (!item.must && (normalLabels >= budget || candidates++ >= candidateBudget)) continue;
        const { node, x, y } = item;
        const r = clamp((node.radius || 7 + Math.sqrt(node.degree || 0) * 2.1) * Math.min(tr.z, 1.4), 3.5, 28);
        const font = "11px system-ui, 'Segoe UI', 'Microsoft YaHei'";
        ctx.font = font;
        // One wrap width in every state: hovering or selecting a node never
        // changes its title's line breaks.
        const maxWidth = 150;
        // Wrapped lines and their width per node, title and width (and font
        // loads): measuring text is the bulk of a frame otherwise.
        const key = `${maxWidth}|${fontGeneration()}|${node.title}`;
        let layout = node._forceLabelLayout;
        if (!layout || layout.key !== key) {
          const lines = wrap(ctx, node.title, maxWidth, 2);
          layout = node._forceLabelLayout = { key, lines, textWidth: Math.max(...lines.map((l) => ctx.measureText(l).width)) };
        }
        const { lines, textWidth } = layout;
        const boxW = textWidth + 6;
        const boxH = lines.length * 14 + 2;
        // Let labels use their natural position. If the node/text is at the
        // viewport boundary, the canvas clips it at the frame edge instead of
        // pulling unrelated off-screen text back inside.
        const ly = y + r + 5;
        const lx = x - boxW / 2;
        const box = { x: lx, y: ly, w: boxW, h: boxH };
        if (boxes.some((b) => !(box.x + box.w < b.x - 2 || box.x > b.x + b.w + 2 || box.y + box.h < b.y - 1 || box.y > b.y + b.h + 1))) continue;
        if (!item.must) normalLabels++;
        boxes.push(box);
        // Hover and click keep the same quiet label: no background, no bold,
        // just black text with a thin contrast halo. Selection is shown by the ring.
        ctx.globalAlpha = item.dim ? 0.2 : 1;
        if (item.hovered || item.selected) {
          ctx.save();
          ctx.lineJoin = "round";
          ctx.lineWidth = 3;
          ctx.strokeStyle = pal.bg;
          lines.forEach((line, i) => ctx.strokeText(line, x, ly + i * 14));
          ctx.fillStyle = pal.ink;
          lines.forEach((line, i) => ctx.fillText(line, x, ly + i * 14));
          ctx.restore();
        } else {
          ctx.fillStyle = pal.sub;
          lines.forEach((line, i) => ctx.fillText(line, x, ly + i * 14));
        }
        ctx.globalAlpha = 1;
      }
    }
  }

  // ---- Scene tiles ---------------------------------------------------
  //
  // A settled Force or node graph whose camera moves (pan, zoom) is
  // composited from world-aligned 512 device-px tiles of the full scene
  // (edges, nodes or cards, labels, focus dimming, selection and hover)
  // rendered with the same drawing code at the current zoom.  The camera
  // offset is rounded to whole device pixels so a tile at its native
  // resolution is the same picture as drawing the scene directly.  Any scene
  // change (positions, data, focus, palette, settings) gives a new signature
  // and drops the tiles.  Missing tiles render within a frame budget; the
  // rest show the previous zoom's tiles scaled (a zoom gesture) or the whole
  // frame is drawn directly, and finish in idle time.  Not used while the simulation runs, nodes move or
  // fade in, a card is being edited, or a drag edits the scene.  Option
  // `sceneTiles` (or the older `forceTiles`): node-count threshold, false
  // disables.

  _sceneTilesUsable() {
    if (this.opts.backdrop || this._transitionFrame) return false;
    // Panning and pinch-zooming only move the camera; any other drag edits
    // the scene.
    if ((this.drag && !this.drag.pan) || this.linkDrag || this.edgeRouteDrag || this.mindResizeDrag
      || this.imageResizeDrag || this.imageSideDrag) return false;
    if (this.layout === "force") {
      if (this.running || this._ghosts?.length) return false;
    } else if (this.layout === "node") {
      if (this._nodeAnimation || this.editingNodeId != null) return false;
    } else {
      return false;
    }
    const threshold = this.opts.sceneTiles ?? this.opts.forceTiles ?? SCENE_TILE_MIN_NODES;
    return threshold !== false && this.nodes.length >= threshold;
  }

  // Everything the scene drawing depends on, apart from the camera.
  // Changes whenever the effective arrow settings object changes.
  _arrowGeneration() {
    const arrows = this._arrows();
    if (arrows !== this._tileArrows) {
      this._tileArrows = arrows;
      this._tileArrowGen = (this._tileArrowGen || 0) + 1;
    }
    return this._tileArrowGen;
  }

  _nextTileTick() {
    this._tileTick = (this._tileTick || 0) + 1;
    return this._tileTick;
  }

  _sceneSignature() {
    if (this.layout === "node") return this._nodeSceneSignature();
    let sum = this.nodes.length * 31 + this.edges.length * 17;
    for (const node of this.nodes) {
      sum = (sum * 33 + node.x * 7.31 + node.y * 3.17 + (node.title?.length || 0) * 5 + (node.tags?.length || 0) * 11
        + (node.status?.charCodeAt(0) || 0) + (node._appear ?? 1) * 13) % 1e15;
    }
    const pal = this._palette();
    const sel = this.selectedNodes?.size ? [...this.selectedNodes].join(",") : "";
    return [sum, this.selected, sel, this.hover?.id ?? "", this.hoverEdge?.id ?? "", this.selectedEdge?.id ?? "",
      this.showLabels ? 1 : 0, [...this.visibleRelations].join(","), pal.key, localStorageSig(),
      this._arrowGeneration(), this._reverseCache?.overrides ? JSON.stringify(this._reverseCache.overrides) : "",
      this.width, this.height].join("|");
  }

  // The node graph: routes (positions, sizes, bends, shown edges) and every
  // card's picture (its memoised hash changes with title, status, markers,
  // loaded thumbnails, steps, palette, ...).
  _nodeSceneSignature() {
    const routes = this._nodeRoutes();
    const pal = this._palette();
    const markerSettings = getMindSettings();
    for (const node of this.nodes) this._cardHash(node, "node", pal, markerSettings);
    let hidden = 0;
    for (const edge of this.edges) if (edge.hidden) hidden += 1;
    const sel = this.selectedNodes?.size ? [...this.selectedNodes].join(",") : "";
    return ["node", this._nodeRouteCache?.key, routes.size, this._cardMemoGen || 0,
      hidden, this.selected, sel, this.hover?.id ?? "", this.hoverEdge?.id ?? "", this.selectedEdge?.id ?? "",
      [...this.visibleRelations].join(","), pal.key, localStorageSig(), this.opts.showSteps ? 1 : 0, this.mode,
      this._arrowGeneration(), this._reverseCache?.overrides ? JSON.stringify(this._reverseCache.overrides) : "",
      this.width, this.height].join("|");
  }

  _drawSceneTiles() {
    const ctx = this.ctx;
    const dpr = this._dpr();
    const camera = this._transform();
    const z = camera.z;
    // Whole device pixels for the camera offset.
    const ox = Math.round(camera.x * dpr), oy = Math.round(camera.y * dpr);
    const signature = this._sceneSignature();
    let set = this._sceneTiles;
    if (!set || set.signature !== signature || set.dpr !== dpr) {
      this._sceneTilesPrev = null;
      set = this._sceneTiles = { signature, dpr, z, tiles: new Map() };
    } else if (set.z !== z) {
      // Zoom changed: keep this set as the stand-in for the new zoom.
      if (set.tiles.size) this._sceneTilesPrev = set;
      set = this._sceneTiles = { signature, dpr, z, tiles: new Map() };
    }
    const size = SCENE_TILE_SIZE;
    const viewW = Math.ceil(this.width * dpr), viewH = Math.ceil(this.height * dpr);
    const i0 = Math.floor(-ox / size), i1 = Math.floor((viewW - ox - 1) / size);
    const j0 = Math.floor(-oy / size), j1 = Math.floor((viewH - oy - 1) / size);
    const missing = [];
    for (let j = j0; j <= j1; j++) for (let i = i0; i <= i1; i++) if (!set.tiles.has(`${i},${j}`)) missing.push([i, j]);
    // While the zoom changes, tiles for each intermediate zoom would be
    // wasted work: show the previous zoom's tiles scaled when drawing
    // directly is expensive, otherwise draw directly.  Tiles for the final
    // zoom render in idle time.
    const zooming = this._tileLastZoom !== undefined && this._tileLastZoom !== z;
    this._tileLastZoom = z;
    if (zooming) this._tileZoomAt = performance.now();
    if (zooming && !(this._sceneTilesPrev && (this._lastDirectMs || 0) > SCENE_TILE_DIRECT_MS)) {
      this._sceneTilesDone = false;
      this._scheduleSceneTiles();
      return false;
    }
    // Render within the frame budget.
    const start = performance.now();
    const budget = zooming ? 0 : SCENE_TILE_BUDGET_MS;
    while (missing.length && performance.now() - start < budget) {
      const [i, j] = missing.shift();
      this._renderSceneTile(set, i, j);
    }
    this._sceneTilesDone = !missing.length;
    if (missing.length && !this._sceneTilesPrev) {
      // Nothing to stand in: draw this frame directly, finish tiles later.
      this._scheduleSceneTiles();
      return false;
    }
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    ctx.imageSmoothingEnabled = true;
    if (missing.length) {
      // Zoom gesture: the previous zoom's tiles, scaled, under the new ones.
      const prev = this._sceneTilesPrev;
      const k = z / prev.z;
      for (const [key, tile] of prev.tiles) {
        const [i, j] = key.split(",").map(Number);
        ctx.drawImage(tile.canvas, ox + i * size * k, oy + j * size * k, size * k, size * k);
      }
      this._scheduleSceneTiles();
    }
    for (const [key, tile] of set.tiles) {
      const [i, j] = key.split(",").map(Number);
      if (i < i0 || i > i1 || j < j0 || j > j1) continue;
      tile.used = this._nextTileTick();
      ctx.drawImage(tile.canvas, ox + i * size, oy + j * size);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const pal = this._palette();
    if (this.layout === "force") {
      // Labels at the rounded camera the tiles were placed with.
      this._tileTransform = { x: ox / dpr, y: oy / dpr, z };
      try {
        const focusSeeds = new Set(this.selectedNodes || []);
        if (this.selected) focusSeeds.add(this.selected);
        if (!focusSeeds.size && this.hover?.id) focusSeeds.add(this.hover.id);
        this._drawForceLabels(ctx, this._tileTransform, focusNeighborhood(this.edges, focusSeeds, this.visibleRelations), pal);
      } finally {
        this._tileTransform = null;
      }
    }
    if (this.linkDrag) this._drawLinkDrag(ctx, this._screen(this.linkDrag.source), pal);
    this._drawSelectionBox(ctx);
    this._positionEditor();
    this._evictSceneTiles();
    return true;
  }

  _renderSceneTile(set, i, j) {
    const size = SCENE_TILE_SIZE;
    const dpr = set.dpr;
    const canvas = typeof OffscreenCanvas === "function" ? new OffscreenCanvas(size, size)
      : Object.assign(document.createElement("canvas"), { width: size, height: size });
    const tctx = canvas.getContext("2d");
    tctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const saved = { ctx: this.ctx, width: this.width, height: this.height };
    this._tileArrowSkip = this._arrowSkip;
    this._tileRender = true;
    this._tileTransform = { x: -(i * size) / dpr, y: -(j * size) / dpr, z: set.z };
    this.ctx = tctx;
    this.width = size / dpr;
    this.height = size / dpr;
    try {
      this._draw();
    } finally {
      Object.assign(this, saved);
      this._tileRender = false;
      this._tileTransform = null;
    }
    set.tiles.set(`${i},${j}`, { canvas, used: this._nextTileTick() });
  }

  _scheduleSceneTiles() {
    if (this._sceneTilePending) return;
    this._sceneTilePending = true;
    const run = (deadline) => {
      if (!this.host?.isConnected || !this._sceneTilesUsable() || this._sceneTilesDone) {
        this._sceneTilePending = false;
        return;
      }
      // Wait until the zoom has been still for a moment: tiles for each
      // intermediate zoom of a gesture would be thrown away.
      const still = performance.now() - (this._tileZoomAt || 0);
      if (still < SCENE_TILE_SETTLE_MS) {
        setTimeout(() => {
          if (typeof requestIdleCallback === "function") requestIdleCallback(run, { timeout: 60 });
          else run();
        }, SCENE_TILE_SETTLE_MS - still);
        return;
      }
      this._sceneTilePending = false;
      // Render missing tiles for the current camera without painting; paint
      // once they are all there.
      const set = this._sceneTiles;
      const dpr = this._dpr();
      const camera = this._transform();
      if (!set || set.z !== camera.z || set.signature !== this._sceneSignature() || set.dpr !== dpr) {
        this._draw();
        return;
      }
      const size = SCENE_TILE_SIZE;
      const ox = Math.round(camera.x * dpr), oy = Math.round(camera.y * dpr);
      const viewW = Math.ceil(this.width * dpr), viewH = Math.ceil(this.height * dpr);
      const budget = deadline?.timeRemaining ? Math.max(8, deadline.timeRemaining()) : 12;
      const start = performance.now();
      let left = 0;
      for (let j = Math.floor(-oy / size); j <= Math.floor((viewH - oy - 1) / size); j++) {
        for (let i = Math.floor(-ox / size); i <= Math.floor((viewW - ox - 1) / size); i++) {
          if (set.tiles.has(`${i},${j}`)) continue;
          if (performance.now() - start < budget) this._renderSceneTile(set, i, j);
          else left += 1;
        }
      }
      if (left) this._scheduleSceneTiles();
      else this._draw();
    };
    if (typeof requestIdleCallback === "function") requestIdleCallback(run, { timeout: 60 });
    else setTimeout(run, 16);
  }

  _evictSceneTiles() {
    const sets = [this._sceneTiles, this._sceneTilesPrev].filter(Boolean);
    let count = sets.reduce((n, set) => n + set.tiles.size, 0);
    if (count <= SCENE_TILE_MAX) return;
    // The stand-in set goes first, then least recently drawn tiles.
    if (this._sceneTilesPrev) { count -= this._sceneTilesPrev.tiles.size; this._sceneTilesPrev = null; }
    if (count <= SCENE_TILE_MAX) return;
    const tiles = [...this._sceneTiles.tiles].sort((a, b) => a[1].used - b[1].used);
    for (const [key] of tiles.slice(0, count - SCENE_TILE_MAX)) this._sceneTiles.tiles.delete(key);
  }

  /** Bytes held by scene tiles (for benchmarks). */
  _sceneTileBytes() {
    const n = (this._sceneTiles?.tiles.size || 0) + (this._sceneTilesPrev?.tiles.size || 0);
    return n * SCENE_TILE_SIZE * SCENE_TILE_SIZE * 4;
  }

  _nodeSprite(node, radius, color, background) {
    const r = Math.round(radius * 4) / 4;
    const dpr = Math.min(2.5, window.devicePixelRatio || 1);
    const key = `${r}:${color}:${background}:${node.status}:${!!node.for_me}:${dpr}`;
    const cache = this._nodeSprites ||= new Map();
    if (cache.has(key)) return cache.get(key);
    const size = (r + 5) * 2;
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = Math.ceil(size * dpr);
    const ctx = canvas.getContext("2d");
    ctx.scale(dpr, dpr);
    const center = size / 2;
    drawForceNode(ctx, node, center, center, r, color, background);
    const sprite = { canvas, size };
    cache.set(key, sprite);
    if (cache.size > 256) cache.delete(cache.keys().next().value);
    return sprite;
  }

  // The in-progress link: a dashed line whose arrow points at the cursor.
  _drawLinkDrag(ctx, a, pal) {
    const p = this.linkDrag.current;
    const dx = p.x - a.x, dy = p.y - a.y, len = Math.hypot(dx, dy);
    // The default line end; with "none" the cursor end stays a dot.
    const style = (this.arrowSettings || ACTIVE_ARROWS).style;
    const head = style.head === "none" ? null : style.head;
    const size = arrowSize(1.8, 1, style.size);
    const ux = len ? dx / len : 1, uy = len ? dy / len : 0;
    const showArrow = head && len > size * 1.5;
    const inset = showArrow ? arrowInset(head, size) : 0;
    ctx.beginPath();
    ctx.moveTo(a.x, a.y);
    ctx.lineTo(p.x - ux * inset, p.y - uy * inset);
    ctx.setLineDash([5, 4]);
    ctx.strokeStyle = pal.accent;
    ctx.lineWidth = 1.8;
    ctx.stroke();
    ctx.setLineDash([]);
    ctx.beginPath();
    if (showArrow) {
      addArrowhead(ctx, p.x, p.y, ux, uy, size, head);
      paintArrowheads(ctx, ctx, head, pal.accent, pal.bg, 1.8);
      return;
    }
    ctx.arc(p.x, p.y, 4, 0, Math.PI * 2);
    ctx.fillStyle = pal.accent;
    ctx.fill();
  }

  _drawSelectionBox(ctx) {
    if (!this.selectBox?.moved) return;
    const x1 = Math.min(this.selectBox.startX, this.selectBox.currentX);
    const y1 = Math.min(this.selectBox.startY, this.selectBox.currentY);
    const w = Math.abs(this.selectBox.currentX - this.selectBox.startX);
    const h = Math.abs(this.selectBox.currentY - this.selectBox.startY);
    ctx.save();
    ctx.fillStyle = withAlpha(this._palette().accent, 0.08);
    ctx.strokeStyle = withAlpha(this._palette().accent, 0.85);
    ctx.lineWidth = 1;
    ctx.setLineDash([4, 3]);
    ctx.fillRect(x1, y1, w, h);
    ctx.strokeRect(x1, y1, w, h);
    ctx.restore();
  }

  // Screen position of an edge's route handle (its bend, or the route middle).
  _nodeEdgeHandle(edge, tr = this._transform()) {
    const route = this._nodeRoutes().get(edge.id);
    if (!route) return null;
    return { x: route.mid.x * tr.z + tr.x, y: route.mid.y * tr.z + tr.y };
  }

  _drawNode() {
    if (!this._tileRender && this._sceneTilesUsable() && this._drawSceneTiles()) return;
    const directStart = this._tileRender ? 0 : performance.now();
    const ctx = this.ctx, tr = this._transform(), pal = this._palette();
    const w = this.width, hgt = this.height;
    ctx.clearRect(0, 0, w, hgt);
    this.hiddenIds = new Set();
    const selected = this.selectedNodes;
    const arrowOf = this._arrows();
    const routes = this._nodeRoutes();
    // Hovering a card highlights its incoming and outgoing curves.
    const hoverId = this.hover?.id ?? null;
    const sx = (x) => x * tr.z + tr.x, sy = (y) => y * tr.z + tr.y;
    // Relation labels sit at each input port and scale with the cards.
    const portLabels = true;
    const slotDots = [];
    const labels = [];
    const portGroups = [];
    // The view (with a margin for line width and arrowheads) in world units.
    const view = { x0: (-40 - tr.x) / tr.z, x1: (w + 40 - tr.x) / tr.z, y0: (-40 - tr.y) / tr.z, y1: (hgt + 40 - tr.y) / tr.z };
    // Curve splitting thresholds in world units.  Fixed screen lengths (not
    // the view size), so a scene tile splits curves exactly as the full view.
    const curveWhole = CURVE_WHOLE_PX / tr.z, curvePiece = CURVE_PIECE_PX / tr.z;
    // Inactive edges first, so highlighted curves are drawn on top.
    const isActive = (edge) => this.hoverEdge?.id === edge.id || this.selectedEdge?.id === edge.id
      || selected.has(edge.source) || selected.has(edge.target)
      || (hoverId !== null && (edge.source === hoverId || edge.target === hoverId));
    const inactive = [], activeEdges = [];
    for (const edge of this._shownEdges()) {
      if (!this.visibleRelations.has(edge.relation) || !routes.has(edge.id)) continue;
      (isActive(edge) ? activeEdges : inactive).push(edge);
    }
    const ordered = inactive.concat(activeEdges);
    // Colours per tone, built once per frame.
    const tones = {
      touched: withAlpha(pal.accent, 0.9), active: withAlpha(pal.sub, 0.85), quiet: withAlpha(pal.sub, 0.2), plain: withAlpha(pal.sub, 0.5),
      headActive: withAlpha(pal.sub, 0.9), headQuiet: withAlpha(pal.sub, 0.25), headPlain: withAlpha(pal.sub, 0.62),
    };
    // Ports, heads and relation labels of a curve that only crosses the view
    // are off screen: skip them (labels reach ~340 world units left of the
    // port and a stack ~300 above or below it).
    const near = (x, y, left, up) => x >= -40 && x - left <= w + 40 && y >= -40 - up && y <= hgt + 40 + up;
    const portNearCache = new Map();
    const portNear = (id) => {
      let hit = portNearCache.get(id);
      if (hit === undefined) {
        const node = this.byId.get(id);
        // The card's left edge, its height and up to 12 stacked labels.
        const half = ((node?.h || NODE_H) / 2 + 12 * 13) * tr.z;
        hit = !node || near(sx(node.x - (node.w || NODE_W) / 2), sy(node.y), 400 * tr.z, half);
        portNearCache.set(id, hit);
      }
      return hit;
    };
    for (const edge of ordered) {
      const route = routes.get(edge.id);
      const hovered = this.hoverEdge?.id === edge.id;
      const edgeSelected = this.selectedEdge?.id === edge.id;
      const touched = hoverId !== null && (edge.source === hoverId || edge.target === hoverId);
      const active = hovered || edgeSelected || touched || selected.has(edge.source) || selected.has(edge.target);
      const quiet = hoverId !== null && !active;
      const color = hovered || edgeSelected ? pal.accent
        : touched ? tones.touched : active ? tones.active : quiet ? tones.quiet : tones.plain;
      const lineWidth = hovered || edgeSelected ? 2.4 : active ? 1.8 : 1.25;
      // Cull curves whose control hull is off screen (bounds cached on the
      // route, which lives as long as the layout).
      const bounds = routeBounds(route);
      if (bounds.maxX < view.x0 || bounds.minX > view.x1 || bounds.maxY < view.y0 || bounds.minY > view.y1) continue;
      if (edge.hidden) {
        // A link the topic does not show (visible only while editing links).
        ctx.save();
        ctx.globalAlpha = hovered || edgeSelected ? 0.9 : 0.4;
        ctx.setLineDash([5, 4]);
      }
      const style = arrowOf(edge.relation);
      const size = style ? arrowSize(lineWidth, tr.z, style.size) : 0;
      const port = Math.max(3, 4 * tr.z) + 1;
      ctx.beginPath();
      // Only segments whose control hull reaches the view: a long edge
      // routed through many layers has hundreds, mostly off screen, and
      // every one would be tessellated.  A bezier stays inside its hull, so
      // the picture on screen is the same.  (A dashed link keeps every
      // segment so its dash pattern does not shift.)
      const segBounds = bounds.segments;
      const state = { open: false };
      for (let i = 0, k = 0; i < route.segments.length; i++, k += 4) {
        const seg = route.segments[i];
        if (edge.hidden) {
          if (!i) ctx.moveTo(sx(seg[0]), sy(seg[1]));
          ctx.bezierCurveTo(sx(seg[2]), sy(seg[3]), sx(seg[4]), sy(seg[5]), sx(seg[6]), sy(seg[7]));
        } else if (segBounds[k + 2] < view.x0 || segBounds[k] > view.x1 || segBounds[k + 3] < view.y0 || segBounds[k + 1] > view.y1) {
          state.open = false;
        } else {
          addViewCubic(ctx, seg, view, curveWhole, curvePiece, sx, sy, state);
        }
      }
      ctx.strokeStyle = color;
      ctx.lineWidth = lineWidth;
      ctx.stroke();
      if (edge.hidden) {
        ctx.restore();
        ctx.globalAlpha = hovered || edgeSelected ? 0.9 : 0.4;
      }
      const start = { x: sx(route.start.x), y: sy(route.start.y) };
      const end = { x: sx(route.end.x), y: sy(route.end.y) };
      const reach = size * 3 + port + 4;
      const endNear = near(end.x + reach, end.y, 2 * reach, reach);
      const startNear = near(start.x + reach, start.y, 2 * reach, reach);
      if (style && ((style.end && endNear) || (style.start && startNear))) {
        // Curves leave and enter ports horizontally, so heads do too.
        ctx.beginPath();
        if (style.end) addArrowhead(ctx, end.x - port, end.y, 1, 0, size, style.head);
        if (style.start) addArrowhead(ctx, start.x + port, start.y, -1, 0, size, style.head);
        paintArrowheads(ctx, ctx, style.head,
          hovered || edgeSelected || touched ? pal.accent : active ? tones.headActive : quiet ? tones.headQuiet : tones.headPlain, pal.bg, 1.4);
      }
      ctx.globalAlpha = 1;
      if (startNear) slotDots.push(start);
      if (endNear) slotDots.push(end);
      if (portLabels && !portNear(edge.target)) {
        // The target card's input side is far off screen: so is its whole
        // label stack (decided per card, so stacks and counts never change).
      } else if (portLabels) {
        portGroups.push({ target: edge.target, x: end.x - 20 * tr.z, y: end.y, text: relLabel(edge.relation), active, quiet });
      } else if (this.opts.edgeLabels && tr.z > 0.55) {
        labels.push({ x: sx(route.mid.x), y: sy(route.mid.y), text: relLabel(edge.relation), mid: true, active, quiet, hidden: edge.hidden });
      }
      if (hovered || edgeSelected) {
        const handle = { x: sx(route.mid.x), y: sy(route.mid.y) };
        ctx.beginPath();
        ctx.rect(handle.x - 4, handle.y - 4, 8, 8);
        ctx.fillStyle = pal.accent;
        ctx.fill();
        ctx.strokeStyle = pal.bg;
        ctx.lineWidth = 1;
        ctx.stroke();
      }
    }
    // Zoomed in, relation labels sit before each input port: one label per
    // relation (with a count), stacked so many inputs stay readable.
    const portFont = 8.5 * tr.z;
    const byPort = new Map();
    for (const item of portGroups) {
      if (!byPort.has(item.target)) byPort.set(item.target, new Map());
      const group = byPort.get(item.target);
      const entry = group.get(item.text) || { ...item, count: 0, ys: [] };
      entry.count += 1;
      entry.ys.push(item.y);
      entry.x = Math.min(entry.x, item.x);
      entry.active ||= item.active;
      entry.quiet &&= item.quiet;
      group.set(item.text, entry);
    }
    for (const group of byPort.values()) {
      const entries = [...group.values()].map((entry) => ({ ...entry, y: entry.ys.reduce((a, b) => a + b, 0) / entry.ys.length }))
        .sort((a, b) => a.y - b.y);
      const step = portFont + 4 * tr.z;
      const center = entries.reduce((sum, entry) => sum + entry.y, 0) / entries.length;
      const x = Math.min(...entries.map((entry) => entry.x));
      entries.forEach((entry, i) => labels.push({
        x, y: center + (i - (entries.length - 1) / 2) * step + portFont / 2 - (entries.length === 1 ? 4 * tr.z : 0),
        text: entry.count > 1 ? `${entry.text} ×${entry.count}` : entry.text, active: entry.active, quiet: entry.quiet,
      }));
    }
    // Relation labels: at the middle of the curve, or at the input port.
    for (const label of labels) {
      const fontSize = label.mid ? 9 * tr.z : 8.5 * tr.z;
      ctx.font = `${fontSize}px system-ui, 'Segoe UI', 'Microsoft YaHei'`;
      ctx.textAlign = label.mid ? "center" : "right";
      ctx.textBaseline = label.mid ? "middle" : "bottom";
      const width = ctx.measureText(label.text).width;
      ctx.globalAlpha = label.hidden ? 0.45 : label.quiet ? 0.35 : 0.92;
      if (label.mid) {
        roundRect(ctx, label.x - width / 2 - 4 * tr.z, label.y - 7 * tr.z, width + 8 * tr.z, 14 * tr.z, 4);
      } else {
        roundRect(ctx, label.x - width - 3 * tr.z, label.y - fontSize - 3 * tr.z, width + 6 * tr.z, fontSize + 3 * tr.z, 3 * tr.z);
      }
      ctx.fillStyle = pal.bg;
      ctx.fill();
      ctx.fillStyle = label.active ? pal.ink : pal.sub;
      ctx.fillText(label.text, label.x, label.mid ? label.y : label.y - 2 * tr.z);
      ctx.globalAlpha = 1;
    }
    // Cards: the shared card picture, uniformly scaled at every zoom.
    const markerSettings = getMindSettings();
    for (const node of this.nodes) {
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      const hw = ((node.w || NODE_W) / 2 + CARD_SPRITE_PAD) * tr.z;
      const hh = ((node.h || NODE_H) / 2 + CARD_SPRITE_PAD) * tr.z;
      if (sx + hw < 0 || sy + hh < 0 || sx - hw > w || sy - hh > hgt) continue;
      this._drawCardItem(ctx, node, "node", sx, sy, tr.z, pal, markerSettings);
    }
    // Port slots: a small dot where each curve meets its card.
    if (tr.z > 0.45) {
      ctx.beginPath();
      const r = Math.max(1.6, 2.2 * Math.min(tr.z, 1.4));
      for (const p of slotDots) { ctx.moveTo(p.x + r, p.y); ctx.arc(p.x, p.y, r, 0, Math.PI * 2); }
      ctx.fillStyle = withAlpha(pal.sub, 0.85);
      ctx.fill();
    }
    if (this._tileRender) return;
    this._lastDirectMs = performance.now() - directStart;
    this._drawSelectionBox(ctx);
    this._positionEditor();
  }

  // ---- Cards: one picture per card, uniformly scaled ------------------
  //
  // A card (Mind and node layouts) is painted by _paintCard in world units:
  // the same drawing at every zoom.  Title wrapping, fonts, padding, markers,
  // footer, thumbnail slots, ports and step pills never depend on zoom, so
  // zooming only scales the picture.  _drawCardItem draws it as a cached
  // sprite (graph-sprites.js) or, while the sprite is being rasterised,
  // directly with the same painter.  Selection, hover, frames and the resize
  // grip are a separate overlay (_paintCardOverlay), also in world units.

  _cardGeometry(node) {
    const w = node.w || NODE_W, h = node.h || NODE_H;
    let titleBox = { x: 0, y: 0, w: Math.min(w, node.titleW || w), h };
    let pinnedBox = null;
    if (node.pinnedMeta) {
      const pw = node.pinnedMeta.w, ph = node.pinnedMeta.h;
      const side = node.pinnedMeta.side;
      if (side === "left") {
        pinnedBox = { x: 4, y: h / 2 - ph / 2, w: pw, h: ph };
        titleBox = { x: pw + 7, y: 0, w: Math.max(8, w - pw - 7), h };
      } else if (side === "top") {
        pinnedBox = { x: w / 2 - pw / 2, y: 4, w: pw, h: ph };
        titleBox = { x: 0, y: ph + 7, w, h: Math.max(8, h - ph - 7) };
      } else if (side === "bottom") {
        pinnedBox = { x: w / 2 - pw / 2, y: h - ph - 4, w: pw, h: ph };
        titleBox = { x: 0, y: 0, w, h: Math.max(8, h - ph - 7) };
      } else {
        pinnedBox = { x: w - pw - 4, y: h / 2 - ph / 2, w: pw, h: ph };
        titleBox = { x: 0, y: 0, w: Math.max(8, w - pw - 7), h };
      }
    }
    const thumbs = [];
    if (!node.pinnedMeta) {
      let ix = titleBox.x + titleBox.w + 4;
      for (const item of node.imageMediaList || []) {
        thumbs.push({ item, box: { x: ix, y: (h - THUMB) / 2, w: THUMB, h: THUMB } });
        ix += THUMB + 4;
      }
    }
    return { w, h, titleBox, pinnedBox, thumbs };
  }

  // Wrapped lines for a text in a font and width, cached on the node.
  _wrapCached(ctx, node, slot, text, font, width, lines) {
    const key = `${font}|${width}|${lines}|${text}`;
    const cache = node._wrap || (node._wrap = {});
    const hit = cache[slot];
    if (hit && hit.key === key) return hit.lines;
    ctx.font = font;
    const value = wrap(ctx, text, width, lines);
    cache[slot] = { key, lines: value };
    return value;
  }

  // Thumbnails: an image is only fetched once its card is on screen at a
  // size where it is visible; until then its slot shows the placeholder.
  _cardImages(node) {
    return node.pinnedMeta ? [node.pinnedMeta.item] : (node.imageMediaList || []);
  }

  _imageReady(item) {
    const image = MIND_IMAGE_CACHE.get(imageMediaSrc(item));
    return Boolean(image && image.complete && image.naturalWidth);
  }

  _requestCardImages(node) {
    for (const item of this._cardImages(node)) cachedMindImage(imageMediaSrc(item), () => this._draw());
  }

  _cardHash(node, kind, pal, markerSettings) {
    // Memoised per node; thumbnails' loaded state is re-checked every frame
    // only for cards that have images.
    const images = node.pinnedMeta || (node.imageMediaList && node.imageMediaList.length)
      ? this._cardImages(node).map((item) => (this._imageReady(item) ? 1 : 0)).join("") : "";
    const editing = this.editingNodeId === node.id;
    const steps = kind === "node" && this.opts.showSteps ? node.member : null;
    const memo = node._cardMemo;
    if (memo && memo.kind === kind && memo.title === node.title && memo.type === node.type && memo.status === node.status
      && memo.custom === node.customColor && memo.uid === node.uid && memo.tags === node.tags && memo.w === node.w
      && memo.h === node.h && memo.titleW === node.titleW && memo.pinned === node.pinnedMeta
      && memo.list === node.imageMediaList && memo.images === images && memo.depth === node.depth
      && memo.steps === steps && memo.count === node.member_count && memo.pal === pal.key
      && memo.scale === markerSettings.mindMarkerScale && memo.editing === editing) return memo.hash;
    const hash = this._cardHashFresh(node, kind, pal, markerSettings);
    this._cardMemoGen = (this._cardMemoGen || 0) + 1;
    node._cardMemo = { kind, title: node.title, type: node.type, status: node.status, custom: node.customColor,
      uid: node.uid, tags: node.tags, w: node.w, h: node.h, titleW: node.titleW, pinned: node.pinnedMeta,
      list: node.imageMediaList, images, depth: node.depth, steps, count: node.member_count, pal: pal.key,
      scale: markerSettings.mindMarkerScale, editing, hash };
    return hash;
  }

  _cardHashFresh(node, kind, pal, markerSettings) {
    const marks = markersFor(node).slice(0, 5).map((m) => m.glyph + m.color).join("");
    const images = this._cardImages(node).map((item) => `${item.id}:${this._imageReady(item) ? 1 : 0}`).join(",");
    const pin = node.pinnedMeta ? `${node.pinnedMeta.side}:${node.pinnedMeta.w}:${node.pinnedMeta.h}` : "";
    const steps = kind === "node" && this.opts.showSteps && node.member?.step
      ? `${node.member.path || node.member.step}:${node.member.source}` : "";
    return [kind, node.title, node.type, node.customColor || "", node.status, node.uid || "", marks,
      node.w, node.h, node.titleW, images, pin, kind === "mind" && node.depth === 0 ? 1 : 0, steps, node.member_count ?? "",
      pal.key, markerSettings.mindMarkerScale, this.editingNodeId === node.id ? 1 : 0].join("|");
  }

  // The static card in world units with its top-left corner at (0, 0).
  _paintCard(ctx, node, kind, pal, markerSettings) {
    const { w, h, titleBox, pinnedBox, thumbs } = this._cardGeometry(node);
    const color = node.customColor || typeColor(node.type);
    if (kind === "node" && node.type === "topic") {
      // A nested topic reads as a stack of cards.
      for (const offset of [8, 4]) {
        roundRect(ctx, offset, -offset, w, h, 7);
        ctx.fillStyle = pal.card;
        ctx.fill();
        ctx.strokeStyle = withAlpha(color, 0.45);
        ctx.lineWidth = 1;
        ctx.stroke();
      }
    }
    roundRect(ctx, 0, 0, w, h, Math.min(8, h / 4));
    ctx.fillStyle = kind === "mind" && node.depth === 0 ? withAlpha(color, 0.16) : pal.card;
    ctx.fill();
    ctx.lineWidth = 1;
    ctx.strokeStyle = withAlpha(color, 0.55);
    ctx.stroke();
    roundRect(ctx, 0, 0, Math.min(4, w), h, 2);
    ctx.fillStyle = color;
    ctx.fill();
    const contentBottom = titleBox.y + titleBox.h;
    if (this.editingNodeId !== node.id) {
      const pad = 9;
      const marks = markersFor(node).slice(0, 5);
      const markerFont = Math.max(1, Math.min(11 * markerSettings.mindMarkerScale,
        (titleBox.w - pad * 2) / ((marks.length + 1) * 1.1)));
      const markerGap = markerFont * 1.1;
      const markerRight = titleBox.x + titleBox.w - pad - markerFont / 2;
      const firstX = markerRight - markerGap * marks.length;
      const markerY = contentBottom - markerFont / 2 - 2;
      const footerH = Math.max(14, markerFont + 4);
      ctx.save();
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.font = `700 ${markerFont}px system-ui, 'Segoe UI Symbol'`;
      ctx.lineJoin = "round";
      ctx.strokeStyle = "rgba(255,255,255,.94)";
      ctx.lineWidth = Math.max(0.4, markerFont * 0.15);
      const statusFill = statusColor(node.status);
      ctx.beginPath();
      ctx.arc(firstX, markerY, markerFont / 2, 0, Math.PI * 2);
      ctx.fillStyle = statusFill;
      ctx.fill();
      drawStatusIcon(ctx, node.status, firstX, markerY, markerFont / 2, statusFill);
      marks.forEach((marker, index) => {
        const mx = firstX + (index + 1) * markerGap;
        ctx.strokeText(marker.glyph, mx, markerY);
        ctx.fillStyle = marker.color;
        ctx.fillText(marker.glyph, mx, markerY);
      });
      ctx.restore();
      const titleFont = CARD_TITLE_FONT;
      ctx.textAlign = "left";
      ctx.textBaseline = "top";
      ctx.fillStyle = pal.ink;
      const maxTitleLines = Math.max(1, Math.min(8, Math.round((titleBox.h - 24) / 15)));
      const lines = this._wrapCached(ctx, node, "title", node.title || "", titleFont,
        Math.max(8, titleBox.w - pad * 2 - 4), maxTitleLines);
      ctx.font = titleFont;
      const lineH = 15;
      let ty = titleBox.y + Math.max(2, (titleBox.h - footerH - lines.length * lineH) / 2);
      for (const line of lines) { ctx.fillText(line, titleBox.x + pad + 2, ty); ty += lineH; }
      const metaX = titleBox.x + pad + 2;
      const metaWidth = firstX - markerFont / 2 - 4 - metaX;
      if (metaWidth > 12) {
        const count = node.type === "topic" && Number.isFinite(node.member_count) ? ` · ${node.member_count}` : "";
        const [meta] = this._wrapCached(ctx, node, "meta", `${node.uid || ""} · ${typeLabel(node.type)}${count}`,
          CARD_META_FONT, metaWidth, 1);
        ctx.font = CARD_META_FONT;
        ctx.fillStyle = withAlpha(pal.sub, 0.9);
        ctx.save();
        ctx.beginPath();
        ctx.rect(metaX, contentBottom - footerH, metaWidth, footerH);
        ctx.clip();
        ctx.fillText(meta, metaX, contentBottom - 12);
        ctx.restore();
      }
    }
    // Media slots have their final size before the image loads.
    const paintImage = (item, box, rounded) => {
      ctx.save();
      roundRect(ctx, box.x, box.y, box.w, box.h, rounded);
      ctx.clip();
      ctx.fillStyle = withAlpha(pal.ink, 0.06);
      ctx.fillRect(box.x, box.y, box.w, box.h);
      const image = MIND_IMAGE_CACHE.get(imageMediaSrc(item));
      if (image && image.complete && image.naturalWidth) {
        const scale = Math.max(box.w / image.naturalWidth, box.h / image.naturalHeight);
        const dw = image.naturalWidth * scale, dh = image.naturalHeight * scale;
        ctx.drawImage(image, box.x + (box.w - dw) / 2, box.y + (box.h - dh) / 2, dw, dh);
      }
      ctx.restore();
      roundRect(ctx, box.x, box.y, box.w, box.h, rounded);
      ctx.strokeStyle = withAlpha(color, 0.55);
      ctx.lineWidth = 1;
      ctx.stroke();
    };
    if (pinnedBox) paintImage(node.pinnedMeta.item, pinnedBox, 6);
    for (const { item, box } of thumbs) paintImage(item, box, 5);
    if (kind === "node") {
      // Input (left) and output (right) ports.
      ctx.beginPath();
      ctx.arc(0, h / 2, 4, 0, Math.PI * 2);
      ctx.moveTo(w + 4, h / 2);
      ctx.arc(w, h / 2, 4, 0, Math.PI * 2);
      ctx.fillStyle = pal.bg;
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.5;
      ctx.fill();
      ctx.stroke();
      if (node.member?.step && this.opts.showSteps) {
        // Outline number (1, 1.2, ...) as a pill on the card's top-left corner.
        const label = String(node.member.path || node.member.step);
        ctx.font = "700 9.5px system-ui, 'Segoe UI'";
        const ph = 18;
        const pw = Math.max(ph, ctx.measureText(label).width + 10);
        roundRect(ctx, -4, -ph / 2, pw, ph, ph / 2);
        ctx.fillStyle = node.member.source === "rule" ? pal.card : pal.accent;
        ctx.fill();
        ctx.strokeStyle = pal.accent;
        ctx.lineWidth = 1.2;
        ctx.stroke();
        ctx.fillStyle = node.member.source === "rule" ? pal.accent : pal.bg;
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillText(label, -4 + pw / 2, 0.5);
        ctx.textAlign = "left";
        ctx.textBaseline = "top";
      }
    }
  }

  // Selection, hover, image frames and the resize grip (world units, card
  // top-left at (0, 0)).
  _paintCardOverlay(ctx, node, pal) {
    const { w, h, pinnedBox, thumbs } = this._cardGeometry(node);
    const isSel = this.selectedNodes.has(node.id);
    const hovered = this.hover?.id === node.id;
    if (isSel) {
      roundRect(ctx, 0, 0, w, h, Math.min(8, h / 4));
      ctx.lineWidth = node.id === this.selected ? 2 : 1.4;
      ctx.strokeStyle = pal.accent;
      ctx.stroke();
    }
    if (this.imageSideHover?.node?.id === node.id) {
      ctx.save();
      ctx.strokeStyle = withAlpha(pal.accent, 0.95);
      ctx.lineWidth = 3;
      const side = this.imageSideHover.side;
      ctx.beginPath();
      if (side === "left") { ctx.moveTo(4, 4); ctx.lineTo(4, h - 4); }
      else if (side === "right") { ctx.moveTo(w - 4, 4); ctx.lineTo(w - 4, h - 4); }
      else if (side === "top") { ctx.moveTo(4, 4); ctx.lineTo(w - 4, 4); }
      else { ctx.moveTo(4, h - 4); ctx.lineTo(w - 4, h - 4); }
      ctx.stroke();
      ctx.restore();
    }
    if (this.mode === "edit") {
      const frame = (box, handles) => {
        ctx.save();
        ctx.strokeStyle = withAlpha(pal.accent, 0.95);
        ctx.lineWidth = 1.4;
        roundRect(ctx, box.x, box.y, box.w, box.h, 6);
        ctx.stroke();
        if (handles) {
          for (const [hx, hy] of [[box.x, box.y], [box.x + box.w, box.y], [box.x, box.y + box.h], [box.x + box.w, box.y + box.h]]) {
            ctx.fillStyle = pal.card;
            ctx.strokeStyle = pal.accent;
            ctx.lineWidth = 1.2;
            ctx.beginPath();
            ctx.rect(hx - 2.5, hy - 2.5, 5, 5);
            ctx.fill();
            ctx.stroke();
          }
        }
        ctx.restore();
      };
      if (pinnedBox && (isSel || this.imageHover?.node?.id === node.id)) frame(pinnedBox, true);
      for (const { item, box } of thumbs) if (this.imageHover?.item?.id === item.id) frame(box, false);
      if (!node.pinnedMeta && (isSel || hovered)) {
        ctx.save();
        ctx.strokeStyle = withAlpha(pal.accent, 0.8);
        ctx.lineWidth = 1.2;
        const grip = clamp(w * 0.05, 5, 12);
        for (let offset = 0; offset < 3; offset++) {
          const step = (grip / 3) * offset;
          ctx.beginPath();
          ctx.moveTo(w - 4, h - 4 - grip + step);
          ctx.lineTo(w - 4 - grip + step, h - 4);
          ctx.stroke();
        }
        ctx.restore();
      }
    }
    if (hovered && !isSel) {
      ctx.strokeStyle = withAlpha(pal.ink, 0.5);
      ctx.lineWidth = 1;
      roundRect(ctx, -2, -2, w + 4, h + 4, 9);
      ctx.stroke();
    }
  }

  // Draw one card centred at screen (sx, sy) at `scale` screen px per world
  // unit.  `afterCard(ctx)` paints extra decorations in card space.
  _drawCardItem(ctx, node, kind, sx, sy, scale, pal, markerSettings, afterCard = null) {
    const w = node.w || NODE_W, h = node.h || NODE_H;
    const left = sx - (w / 2) * scale, top = sy - (h / 2) * scale;
    if (scale * THUMB * this._dpr() >= 12) this._requestCardImages(node);
    ctx.globalAlpha = node.context ? 0.62 : (Number.isFinite(node._appear) ? node._appear : 1);
    const pad = CARD_SPRITE_PAD;
    this._drawSprite(ctx, `card:${node.id}`, this._cardHash(node, kind, pal, markerSettings),
      left - pad * scale, top - pad * scale, w + pad * 2, h + pad * 2, scale,
      (c) => { c.translate(pad, pad); this._paintCard(c, node, kind, pal, markerSettings); });
    ctx.globalAlpha = 1;
    ctx.save();
    ctx.translate(left, top);
    ctx.scale(scale, scale);
    this._paintCardOverlay(ctx, node, pal);
    afterCard?.(ctx, w, h);
    ctx.restore();
  }

  _dpr() {
    return Math.min(2.5, (typeof window !== "undefined" && window.devicePixelRatio) || 1);
  }

  /**
   * Draw a world-unit picture (`paint(ctx)`, `w`×`h` world units) with its
   * top-left at screen (x, y) and `scale` screen px per world unit: a cached
   * sprite when available, otherwise the same painter directly.
   */
  _drawSprite(ctx, id, hash, x, y, w, h, scale, paint) {
    const cache = this._spriteCache;
    const bucket = spriteBucket(scale * this._dpr() * (this._backdropScale || 1));
    let entry = null;
    if (cache && this._spritesEnabled !== false && cache.cacheable(w, h, bucket)) {
      if (this._tileRender) {
        // A scene tile is painted once: an exact bitmap if there is one (the
        // same picture as the direct path), else the painter -- no stand-ins,
        // and no requests that would churn the cache.
        entry = cache.peek(id, hash, bucket);
      } else {
        entry = cache.get(id, hash, bucket, { w, h, paint });
        if (cache.queue.size) this._scheduleSpritePump();
      }
    }
    if (entry) {
      // High-quality downscaling keeps text weight the same at every zoom.
      ctx.imageSmoothingQuality = this._smoothing || "low";
      ctx.drawImage(entry.surface, entry.sx, entry.sy, entry.w * entry.bucket, entry.h * entry.bucket, x, y, w * scale, h * scale);
      return;
    }
    ctx.save();
    ctx.translate(x, y);
    ctx.scale(scale, scale);
    paint(ctx);
    ctx.restore();
  }

  // Rasterise queued sprites in idle time, then repaint once.
  _scheduleSpritePump() {
    if (this._spritePumpPending || this._spritesEnabled === false) return;
    this._spritePumpPending = true;
    const run = (deadline) => {
      this._spritePumpPending = false;
      if (!this._spriteCache || !this.host?.isConnected) return;
      const budget = deadline?.timeRemaining ? Math.max(4, Math.min(12, deadline.timeRemaining())) : 8;
      const before = this._spriteCache.stats.rasterised;
      const left = this._spriteCache.pump(budget);
      if (this._spriteCache.stats.rasterised !== before && !this.running) {
        if (!this._spriteRedraw) {
          this._spriteRedraw = requestAnimationFrame(() => { this._spriteRedraw = 0; this._draw(); });
        }
      }
      if (left) this._scheduleSpritePump();
    };
    if (typeof requestIdleCallback === "function") requestIdleCallback(run, { timeout: 120 });
    else setTimeout(run, 16);
  }

  _drawMind() {
    const ctx = this.ctx;
    const { width: w, height: hgt } = this;
    ctx.clearRect(0, 0, w, hgt);
    if (!this.nodes.length) return;
    const tr = this._transform();
    const pal = this._palette();
    const markerSettings = getMindSettings();
    const selected = this.selectedNodes;
    const { hidden, children: mindChildren, parentOf } = this._mindTree();
    this.hiddenIds = hidden;
    const shownEdges = this._shownEdges();
    const arrowOf = this._arrows();
    const treeArrows = arrowOf.mindTree ? new Map() : null;

    // Xmind-style branches: parent right edge → child left edge, batched
    // into one path per style; branches entirely off screen are skipped.
    const branchStyles = [
      { color: pal.accent, width: 2.8, path: null },                    // hovered
      { color: withAlpha(pal.accent, 0.9), width: 1.8, path: null },    // active
      { color: withAlpha(pal.sub, 0.32), width: 1.1, path: null },      // quiet
    ];
    for (const edge of shownEdges) {
      const child = this.byId.get(edge.source), parent = this.byId.get(edge.target);
      if (!child || !parent || hidden.has(child.id) || hidden.has(parent.id)) continue;
      if (parentOf.get(edge.source) !== edge.target) continue;
      if (!this.visibleRelations.has(edge.relation)) continue;
      const a = parent;
      const b = child;
      const ax = (a.x + a.w / 2) * tr.z + tr.x;
      const ay = a.y * tr.z + tr.y;
      const bx = (b.x - b.w / 2) * tr.z + tr.x;
      const by = b.y * tr.z + tr.y;
      const hovered = this.hoverEdge && edge.id === this.hoverEdge.id;
      const edgeSelected = this.selectedEdge && edge.id === this.selectedEdge.id;
      const active = hovered || edgeSelected || selected.has(edge.source) || selected.has(edge.target);
      if ((ax < 0 && bx < 0) || (ax > w && bx > w) || (ay < 0 && by < 0) || (ay > hgt && by > hgt)) continue;
      const mid = (ax + bx) / 2;
      const branch = branchStyles[hovered ? 0 : active ? 1 : 2];
      const path = branch.path || (branch.path = new Path2D());
      path.moveTo(ax, ay);
      path.bezierCurveTo(mid, ay, mid, by, bx, by);
      const style = treeArrows && Math.abs(bx - ax) > 6 ? arrowOf(edge.relation) : null;
      if (style) {
        // Optional (Settings > arrows): the target is the parent, whose right
        // edge carries the collapse toggle, so its tip sits just past it.
        const size = arrowSize(branch.width, tr.z, style.size);
        const color = hovered || active ? branch.color : withAlpha(pal.sub, 0.7);
        const key = `${style.head}|${color}`;
        if (!treeArrows.has(key)) treeArrows.set(key, { head: style.head, color, path: new Path2D() });
        const path = treeArrows.get(key).path;
        const dir = Math.sign(bx - ax) || 1;
        if (style.end) addArrowhead(path, ax + dir * (14 * tr.z + 1), ay, -dir, 0, size, style.head);
        if (style.start) addArrowhead(path, bx, by, dir, 0, size, style.head);
      }
    }
    for (let i = branchStyles.length - 1; i >= 0; i--) {
      const { color, width, path } = branchStyles[i];
      if (!path) continue;
      ctx.strokeStyle = color;
      ctx.lineWidth = width;
      ctx.stroke(path);
    }
    for (const { head, color, path } of treeArrows?.values() || []) paintArrowheads(ctx, path, head, color, pal.bg, 1.4);

    const orphanNodes = this.nodes.filter((node) => node.mindOrphan && !hidden.has(node.id));
    if (orphanNodes.length) {
      const left = Math.min(...orphanNodes.map((n) => n.x - (n.w || 160) / 2)) - 50;
      const top = Math.min(...orphanNodes.map((n) => n.y - (n.h || 46) / 2)) - 22;
      const right = Math.max(...orphanNodes.map((n) => n.x + (n.w || 160) / 2)) + 10;
      const bottom = Math.max(...orphanNodes.map((n) => n.y + (n.h || 46) / 2)) + 10;
      ctx.save();
      ctx.strokeStyle = withAlpha(pal.sub, 0.22);
      ctx.setLineDash([3, 5]);
      ctx.strokeRect(left * tr.z + tr.x, top * tr.z + tr.y, (right - left) * tr.z, (bottom - top) * tr.z);
      ctx.setLineDash([]);
      ctx.font = `600 ${9 * tr.z}px ui-monospace, Consolas, monospace`;
      ctx.fillStyle = withAlpha(pal.sub, 0.85);
      ctx.fillText(t("mind.orphans"), left * tr.z + tr.x, (top - 7) * tr.z + tr.y);
      ctx.restore();
    }

    for (const node of this.nodes) {
      if (hidden.has(node.id)) continue;
      const sx = node.x * tr.z + tr.x;
      const sy = node.y * tr.z + tr.y;
      // Card plus the collapse toggle on its right.
      const hw = ((node.w || 160) / 2 + 16) * tr.z, hh = ((node.h || 38) / 2 + CARD_SPRITE_PAD) * tr.z;
      if (sx + hw < 0 || sy + hh < 0 || sx - hw > w || sy - hh > hgt) continue;
      const kids = mindChildren.get(node.id) || [];
      this._drawCardItem(ctx, node, "mind", sx, sy, tr.z, pal, markerSettings, kids.length ? (c, cw, ch) => {
        const collapsed = this.collapsed.has(node.id);
        c.beginPath();
        c.arc(cw + 7, ch / 2, 7, 0, Math.PI * 2);
        c.fillStyle = collapsed ? pal.accent : withAlpha(pal.card, 0.96);
        c.fill();
        c.strokeStyle = withAlpha(pal.accent, 0.9);
        c.lineWidth = 1;
        c.stroke();
        c.fillStyle = collapsed ? pal.bg : pal.sub;
        c.font = "700 10px ui-monospace, Consolas, monospace";
        c.textAlign = "center";
        c.textBaseline = "middle";
        c.fillText(collapsed ? "+" : "−", cw + 7, ch / 2);
      } : null);
    }

    // Non-hierarchy links are drawn over the cards with a small arc so Alt-drag
    // connections are always visible, even when both nodes sit close together.
    const crossIndex = new Map(shownEdges
      .filter((edge) => parentOf.get(edge.source) !== edge.target)
      .map((edge, i) => [edge.id, i]));
    const anchor = (node, tx, ty) => {
      const cx = node.x * tr.z + tr.x, cy = node.y * tr.z + tr.y;
      const dx = tx - cx, dy = ty - cy;
      const halfW = Math.max(2, (node.w || 160) * tr.z / 2);
      const halfH = Math.max(2, (node.h || 38) * tr.z / 2);
      const scale = Math.min(Math.abs(dx) > 0.01 ? halfW / Math.abs(dx) : Infinity,
        Math.abs(dy) > 0.01 ? halfH / Math.abs(dy) : Infinity);
      return { x: cx + dx * Math.min(1, scale), y: cy + dy * Math.min(1, scale) };
    };
    for (const edge of shownEdges) {
      if (parentOf.get(edge.source) === edge.target) continue;
      if (!this.visibleRelations.has(edge.relation)) continue;
      const a = this.byId.get(edge.source), b = this.byId.get(edge.target);
      if (!a || !b) continue;
      const bx0 = b.x * tr.z + tr.x, by0 = b.y * tr.z + tr.y;
      const ax0 = a.x * tr.z + tr.x, ay0 = a.y * tr.z + tr.y;
      const p1 = anchor(a, bx0, by0), p2 = anchor(b, ax0, ay0);
      const dx = p2.x - p1.x, dy = p2.y - p1.y;
      const len = Math.hypot(dx, dy) || 1;
      const bend = (crossIndex.get(edge.id) % 3 - 1) * 22;
      const mx = (p1.x + p2.x) / 2 - dy / len * bend;
      const my = (p1.y + p2.y) / 2 + dx / len * bend;
      const hovered = this.hoverEdge && edge.id === this.hoverEdge.id;
      const edgeSelected = this.selectedEdge && edge.id === this.selectedEdge.id;
      const active = hovered || edgeSelected || selected.has(edge.source) || selected.has(edge.target);
      const color = hovered || edgeSelected ? pal.accent : withAlpha(pal.accent, active ? 0.82 : 0.55);
      const lineWidth = hovered ? 2.8 : edgeSelected ? 2.4 : 1.7;
      const style = arrowOf(edge.relation);
      const size = style ? arrowSize(lineWidth, tr.z, style.size) : 0;
      const fits = style && len > size * (style.start && style.end ? 3.2 : 1.6);
      // The curve stops behind each head; tips touch the card borders.
      const tx = p2.x - mx, ty = p2.y - my, tl = Math.hypot(tx, ty) || 1;
      const sx = p1.x - mx, sy = p1.y - my, sl = Math.hypot(sx, sy) || 1;
      const ends = [];
      if (fits && style.end) ends.push([p2, tx / tl, ty / tl]);
      if (fits && style.start) ends.push([p1, sx / sl, sy / sl]);
      const inset = fits ? arrowInset(style.head, size) : 0;
      const trim = (point, ux, uy, on) => (on ? { x: point.x - ux * inset, y: point.y - uy * inset } : point);
      const start = trim(p1, sx / sl, sy / sl, fits && style.start);
      const end = trim(p2, tx / tl, ty / tl, fits && style.end);
      ctx.beginPath();
      ctx.moveTo(start.x, start.y);
      ctx.quadraticCurveTo(mx, my, end.x, end.y);
      ctx.setLineDash([5, 4]);
      ctx.strokeStyle = color;
      ctx.lineWidth = lineWidth;
      ctx.stroke();
      ctx.setLineDash([]);
      if (ends.length) {
        ctx.beginPath();
        for (const [tip, ux, uy] of ends) addArrowhead(ctx, tip.x, tip.y, ux, uy, size, style.head);
        paintArrowheads(ctx, ctx, style.head,
          hovered || edgeSelected || active ? color : withAlpha(pal.accent, 0.75), pal.bg, 1.5);
      }
    }

    if (this.linkDrag) {
      const source = this.linkDrag.source;
      this._drawLinkDrag(ctx, { x: source.x * tr.z + tr.x, y: source.y * tr.z + tr.y }, pal);
    }
    this._drawSelectionBox(ctx);
    this._positionEditor();
  }

  _showGhost(node, event, count = 1) {
    this._hideGhost();
    this._ghost = h("div", { class: "infohub-drag-ghost" }, count > 1 ? t("graph.drag.infohub_nodes", { n: count }) : (node.title || node.uid || t("graph.drag.infohub_node")));
    this._moveGhost(event);
    document.body.appendChild(this._ghost);
    this.canvas.style.cursor = "grabbing";
  }

  _moveGhost(event) {
    if (!this._ghost) return;
    this._ghost.style.left = `${event.clientX + 14}px`;
    this._ghost.style.top = `${event.clientY + 12}px`;
  }

  _hideGhost() {
    this._ghost?.remove();
    this._ghost = null;
  }

  _hitEdge(event) {
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    const dist = (ax, ay, bx, by) => {
      const dx = bx - ax, dy = by - ay;
      const len = dx * dx + dy * dy;
      const t = len ? Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / len)) : 0;
      return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
    };
    let best = null, bestD = 8;
    // Node layout: a 10-step polyline per bezier segment, only for segments whose control hull (which holds
    // the polyline) comes within reach of the pointer.
    const routes = this.layout === "node" ? this._nodeRoutes() : null;
    const wx = (px - tr.x) / tr.z, wy = (py - tr.y) / tr.z, reach = bestD / tr.z;
    for (const edge of this._shownEdges()) {
      if (routes) {
        if (!this.visibleRelations.has(edge.relation)) continue;
        const route = routes.get(edge.id);
        if (!route) continue;
        const bounds = routeBounds(route);
        if (wx < bounds.minX - reach || wx > bounds.maxX + reach || wy < bounds.minY - reach || wy > bounds.maxY + reach) continue;
        const hull = bounds.segments;
        route.segments.forEach((seg, k) => {
          const j = k * 4;
          if (wx < hull[j] - reach || wx > hull[j + 2] + reach || wy < hull[j + 1] - reach || wy > hull[j + 3] + reach) return;
          let prev = cubicAt(seg, 0);
          for (let i = 1; i <= 10; i++) {
            const p = cubicAt(seg, i / 10);
            const d = dist(prev.x * tr.z + tr.x, prev.y * tr.z + tr.y, p.x * tr.z + tr.x, p.y * tr.z + tr.y);
            if (d < bestD) { best = edge; bestD = d; }
            prev = p;
          }
        });
        continue;
      }
      if (!this.visibleRelations.has(edge.relation)) continue;
      const a = this.byId.get(edge.source), b = this.byId.get(edge.target);
      if (!a || !b) continue;
      let ax = a.x * tr.z + tr.x, ay = a.y * tr.z + tr.y;
      let bx = b.x * tr.z + tr.x, by = b.y * tr.z + tr.y;
      if (this.layout === "mind" && edge.relation === this.primaryRelation) {
        ax = (a.x - a.w / 2) * tr.z + tr.x;
        bx = (b.x + b.w / 2) * tr.z + tr.x;
      }
      const d = dist(ax, ay, bx, by);
      if (d < bestD) { best = edge; bestD = d; }
    }
    // Shown edges may be reversed copies; callers get the stored edge.
    return best?.shownReversed ? this.edges.find((edge) => edge.id === best.id) || best : best;
  }

  _hitMindCollapse(event) {
    if (this.layout !== "mind") return null;
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    const { hidden, children } = this._mindTree();
    for (const node of this.nodes) {
      if (hidden.has(node.id) || !(children.get(node.id) || []).length) continue;
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      const nw = Math.max(2, (node.w || 160) * tr.z);
      const bx = sx + nw / 2 + 7 * tr.z;
      const by = sy;
      if (Math.hypot(px - bx, py - by) <= Math.max(7, 9 * tr.z)) return node;
    }
    return null;
  }

  _toggleNodeCollapse(node) {
    if (!node || this.layout !== "mind") return;
    const { children, hidden: hiddenBefore } = this._mindTree();
    if (!(children.get(node.id) || []).length) return;
    const snapshot = this._mindSnapshot();
    if (this.collapsed.has(node.id)) this.collapsed.delete(node.id);
    else this.collapsed.add(node.id);
    const { hidden } = this._mindTree();
    for (const id of [...this.selectedNodes]) if (hidden.has(id)) this.selectedNodes.delete(id);
    const remaining = this._selectedNodeObjects();
    this.selected = remaining.length ? remaining[remaining.length - 1].id : null;
    // The branch's tree reflows around the clicked card (undoable).
    this._applyLayout(false, { reflow: { changed: [node.id], anchor: node, hiddenBefore } });
    this._pushHistory({ undo: () => this._restoreMindSnapshot(snapshot) });
  }

  _hitNodeEdgeHandle(event) {
    if (this.layout !== "node" || this.mode !== "edit") return null;
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    for (const edge of this.edges) {
      if (!this.visibleRelations.has(edge.relation)) continue;
      const handle = this._nodeEdgeHandle(edge, tr);
      if (handle && Math.hypot(px - handle.x, py - handle.y) <= 9) return edge;
    }
    return null;
  }

  // Mind and node layouts draw the same cards (resize grip, images).
  _cardLayout() {
    return this.layout === "mind" || this.layout === "node";
  }

  _cardHidden() {
    return this.layout === "mind" ? this._mindTree().hidden : new Set();
  }

  // Width limits of the card resize handle for this layout.
  _cardWidthRange(node) {
    const spec = CARD_KINDS[this.layout === "node" ? "node" : "mind"];
    const imageW = node.imageW || 0;
    return [spec.minW + imageW, spec.maxW + imageW];
  }

  // Store a new card width: Mind keeps widths per node in this browser, the
  // node graph in its layout store (topics: on the member).
  _setCardWidth(node, width) {
    if (this.layout === "node") {
      node.nodeW = width === null ? null : cleanCardWidth(width);
      this._applyNodeCardMetrics();
      this._saveNodeGraphState();
      this._draw();
      return;
    }
    if (width === null) {
      delete node.customW;
      if (node.uid && this.mindWidths) delete this.mindWidths[node.uid];
      try { localStorage.setItem("ark.mindWidths", JSON.stringify(this.mindWidths || {})); } catch { /* storage */ }
    } else {
      node.customW = width;
      this._saveMindWidths();
    }
    this._applyLayout(false);
  }

  _hitMindResize(event) {
    if (!this._cardLayout() || this.mode !== "edit") return null;
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    const hidden = this._cardHidden();
    for (const node of this.nodes) {
      if (hidden.has(node.id) || node.pinnedMeta) continue;
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      const nw = Math.max(2, (node.w || 160) * tr.z);
      const nh = Math.max(2, (node.h || 38) * tr.z);
      const grip = clamp(nw * 0.05, 5, 12);
      if (px >= sx + nw / 2 - grip - 3 && px <= sx + nw / 2 + 2
          && py >= sy + nh / 2 - grip - 3 && py <= sy + nh / 2 + 2) return node;
    }
    return null;
  }

  _hitMindImage(event) {
    if (!this._cardLayout()) return null;
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    const hidden = this._cardHidden();
    const showImages = getMindSettings().showMindImages !== false;
    for (const node of this.nodes) {
      if (hidden.has(node.id)) continue;
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      const nw = Math.max(2, (node.w || 160) * tr.z), nh = Math.max(2, (node.h || 38) * tr.z);
      const x = sx - nw / 2, y = sy - nh / 2;
      if (node.pinnedMeta) {
        const pw = Math.max(2, node.pinnedMeta.w * tr.z), ph = Math.max(2, node.pinnedMeta.h * tr.z);
        const side = node.pinnedMeta.side;
        let bx, by;
        if (side === "left") { bx = x + 4 * tr.z; by = sy - ph / 2; }
        else if (side === "top") { bx = sx - pw / 2; by = y + 4 * tr.z; }
        else if (side === "bottom") { bx = sx - pw / 2; by = y + nh - ph - 4 * tr.z; }
        else { bx = x + nw - pw - 4 * tr.z; by = sy - ph / 2; }
        if (px >= bx && px <= bx + pw && py >= by && py <= by + ph) {
          const near = (hx, hy) => Math.hypot(px - hx, py - hy) <= 13;
          let corner = "";
          if (near(bx, by)) corner = "nw";
          else if (near(bx + pw, by)) corner = "ne";
          else if (near(bx, by + ph)) corner = "sw";
          else if (near(bx + pw, by + ph)) corner = "se";
          if (this.mode !== "edit") corner = "";
          return { node, item: node.pinnedMeta.item, pinned: true, corner, resize: Boolean(corner), x: bx, y: by, w: pw, h: ph };
        }
        continue;
      }
      const marked = (node.media || []).filter(isImageMedia).slice(0, 4);
      const images = showImages ? marked : [];
      if (!images.length) continue;
      const titleW = Math.min(nw, (node.titleW || node.w || 160) * tr.z);
      const iw = 44 * tr.z, ih = 44 * tr.z, gap = 4 * tr.z;
      let ix = x + titleW + 4 * tr.z;
      const iy = sy - ih / 2;
      for (const item of images) {
        if (px >= ix && px <= ix + iw && py >= iy && py <= iy + ih) {
          const near = (hx, hy) => Math.hypot(px - hx, py - hy) <= 12;
          let corner = "";
          if (near(ix, iy)) corner = "nw";
          else if (near(ix + iw, iy)) corner = "ne";
          else if (near(ix, iy + ih)) corner = "sw";
          else if (near(ix + iw, iy + ih)) corner = "se";
          return { node, item, pinned: false, corner: "", resize: false, x: ix, y: iy, w: iw, h: ih };
        }
        ix += iw + gap;
      }
    }
    return null;
  }

  _saveMindPinnedImages() {
    try {
      const state = { ...(this.mindPinnedImages || {}) };
      for (const node of this.nodes) {
        if (node.uid && node.pinnedImage) state[node.uid] = { ...node.pinnedImage };
        else if (node.uid) delete state[node.uid];
      }
      this.mindPinnedImages = state;
      localStorage.setItem("ark.mindPinnedImages", JSON.stringify(state));
    } catch { /* storage can be unavailable */ }
  }

  _saveMindWidths() {
    try {
      const widths = { ...(this.mindWidths || {}) };
      for (const node of this.nodes) {
        if (node.uid && node.customW) widths[node.uid] = Math.round(node.customW);
      }
      this.mindWidths = widths;
      localStorage.setItem("ark.mindWidths", JSON.stringify(widths));
    } catch { /* storage can be unavailable */ }
  }

  _saveMindImagePrefs() {
    try {
      const sizes = { ...(this.imageSizes || {}) };
      for (const node of this.nodes) {
        if (node.uid && node.imageSize) sizes[node.uid] = { ...node.imageSize };
      }
      this.imageSizes = sizes;
      localStorage.setItem("ark.mindImageSizes", JSON.stringify(sizes));
      localStorage.setItem("ark.mindPinnedMedia", JSON.stringify([...this.pinnedMedia]));
    } catch { /* storage can be unavailable */ }
  }

  _hit(event) {
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const tr = this._transform();
    for (let i = this.nodes.length - 1; i >= 0; i--) {
      const node = this.nodes[i];
      if (this.layout === "mind" && this.hiddenIds?.has(node.id)) continue;
      const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
      if (this.layout === "mind") {
        const nw = (node.w || 160) * tr.z / 2 + 4;
        const nh = (node.h || 46) * tr.z / 2 + 4;
        if (Math.abs(px - sx) <= nw && Math.abs(py - sy) <= nh) return node;
        continue;
      }
      if (this.layout === "node") {
        const nw = (node.w || 200) * tr.z / 2 + 4;
        const nh = (node.h || 64) * tr.z / 2 + 4;
        if (Math.abs(px - sx) <= nw && Math.abs(py - sy) <= nh) return node;
        continue;
      }
      const r = clamp((node.radius || 7 + Math.sqrt(node.degree || 0) * 2.1) * Math.min(tr.z, 1.4), 4, 28);
      const d = Math.hypot(px - sx, py - sy);
      if (d <= r + 8) return node;
    }
    return null;
  }

  _isTouchInput(event) {
    // Decide by the active input/display mode, never by the graph's pixel
    // width.  A narrow split pane with a mouse must still box-select.
    if (event?.pointerType === "touch") return true;
    if (event?.pointerType === "pen") return false;
    try {
      return Boolean(window.matchMedia?.("(pointer: coarse)")?.matches);
    } catch {
      return false;
    }
  }

  _pointerDown(event) {
    this._cancelCameraAnimation();
    this.canvas.setPointerCapture?.(event.pointerId);
    this.host.focus?.({ preventScroll: true });
    this._userInteracted = true;
    if (this._isTouchInput(event) && this.pointers.size === 0) {
      // Remember the pre-gesture selection.  A second finger turns this into
      // a pinch; any incidental hit from the first finger must not stick.
      this._touchSelection = {
        nodes: new Set(this.selectedNodes),
        selected: this.selected,
        selectedEdge: this.selectedEdge,
        hover: this.hover,
      };
    }
    if (event.button === 0 && this.layout === "node") {
      const routeEdge = this._hitNodeEdgeHandle(event);
      if (routeEdge) {
        this.selectedEdge = routeEdge;
        this.edgeRouteDrag = {
          edge: routeEdge,
          oldBend: routeEdge.routeBend ? { ...routeEdge.routeBend } : null,
        };
        this._watchLineDrag();
        this.canvas.style.cursor = "move";
        this._draw();
        return;
      }
    }
    if (event.button === 0 && this._cardLayout()) {
      const resizeNode = this._hitMindResize(event);
      if (resizeNode) {
        this._setSelection([resizeNode], false);
        this.mindResizeDrag = {
          node: resizeNode,
          startX: event.clientX,
          startW: (resizeNode.titleW || resizeNode.w || 160) + (resizeNode.imageW || 0),
          // The stored value to restore on undo (null = automatic width).
          startStored: this.layout === "node" ? (resizeNode.nodeW ?? null) : (resizeNode.customW ?? null),
          moved: false,
        };
        this.canvas.style.cursor = "ew-resize";
        this._draw();
        return;
      }
      const imageHit = this._hitMindImage(event);
      if (imageHit) {
        this._setSelection([imageHit.node], false);
        // A click (no movement past MEDIA_CLICK_SLOP) opens the preview on
        // release; moving further drags (pinned image: side or size).
        this.thumbPress = { hit: imageHit, startX: event.clientX, startY: event.clientY, moved: false };
        if (imageHit.corner && imageHit.pinned) {
          const meta = imageHit.node.pinnedMeta;
          this.imageResizeDrag = {
            node: imageHit.node, corner: imageHit.corner,
            startX: event.clientX, startY: event.clientY,
            startW: meta.w, startH: meta.h, aspect: meta.aspect || 1,
            startImage: imageHit.node.pinnedImage ? { ...imageHit.node.pinnedImage } : null,
            moved: false,
          };
          this.canvas.style.cursor = `${imageHit.corner}-resize`;
          this._draw();
          return;
        }
        if (imageHit.pinned) {
          this.imageSideDrag = {
            node: imageHit.node, startX: event.clientX, startY: event.clientY,
            moved: false, side: imageHit.node.pinnedMeta.side,
            originalSide: imageHit.node.pinnedMeta.side,
          };
          this.canvas.style.cursor = "move";
          this._draw();
          return;
        }
        this._draw();
        return;
      }
      const collapseNode = this._hitMindCollapse(event);
      if (collapseNode) {
        this._setSelection([collapseNode], false);
        this._toggleNodeCollapse(collapseNode);
        return;
      }
    }
    const node = this._hit(event);
    if (node) this._lastNodeDown = { node, at: performance.now() };
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    this.pointers.set(event.pointerId, { x: px, y: py });
    this._suppressContextMenu = false;
    if (this.pointers.size >= 2) {
      // Two-finger gesture: pinch to zoom and drag the midpoint to pan.  This
      // must run before node hit-testing so a second finger cannot start a
      // node drag or a selection rectangle on touch devices.
      const [a, b] = [...this.pointers.values()];
      this.pinch = {
        distance: Math.max(1, Math.hypot(b.x - a.x, b.y - a.y)),
        midX: (a.x + b.x) / 2,
        midY: (a.y + b.y) / 2,
        zoom: this.zoom,
        pan: { ...this.pan },
      };
      this.drag = null;
      this.selectBox = null;
      this.edgePick = null;
      this.linkDrag = null;
      this._multiDown = null;
      this.hover = null;
      this.hoverEdge = null;
      this._hideTooltip();
      const before = this._touchSelection;
      if (before) {
        this.selectedNodes = new Set(before.nodes);
        this.selected = before.selected;
        this.selectedEdge = before.selectedEdge;
      }
      this.canvas.style.cursor = "grabbing";
      this._draw();
      return;
    }
    if (event.button === 2) {
      // Right-drag is pan. A right click without movement still opens the
      // context menu on pointerup/contextmenu.
      event.preventDefault();
      this.selectBox = null;
      this.drag = { pan: true, right: true, moved: false, startX: event.clientX, startY: event.clientY,
        ox: this.pan.x, oy: this.pan.y };
      this.canvas.style.cursor = "grabbing";
      this._draw();
      return;
    }
    if (event.altKey && node) {
      this.linkDrag = { source: node, current: { x: px, y: py } };
      this._watchLineDrag();
      this.drag = null;
      this.canvas.style.cursor = "crosshair";
      this._draw();
      return;
    }
    if (event.ctrlKey || event.metaKey || event.shiftKey) {
      if (node) {
        this._setSelection([node], true);
        this._multiDown = node.id;
        this.drag = null;
        this.canvas.style.cursor = "pointer";
        this._draw();
        return;
      }
      this.selectBox = { startX: px, startY: py, currentX: px, currentY: py,
        moved: false, additive: event.ctrlKey || event.metaKey, contain: event.shiftKey };
      this.drag = null;
      this.canvas.style.cursor = "crosshair";
      this._draw();
      return;
    }
    if (node) {
      const touch = this._isTouchInput(event);
      const keepMulti = this.selectedNodes.has(node.id) && this.selectedNodes.size > 1;
      // On touch, defer selection to pointerup.  If the second finger starts
      // a pinch, the node is never selected or highlighted.
      if (!keepMulti && !touch) this._setSelection([node], false);
      this.drag = { node, moved: false, keepMulti, touchTap: touch,
        startX: event.clientX, startY: event.clientY,
        ox: node.x, oy: node.y, startPos: { x: node.x, y: node.y } };
      this.hover = touch ? null : node;
      this.canvas.style.cursor = "grabbing";
    } else {
      const edge = this._hitEdge(event);
      if (edge) {
        this._setSelection([], false);
        this.edgePick = { edge, startX: event.clientX, startY: event.clientY, moved: false };
        this.canvas.style.cursor = "pointer";
      } else if (this._isTouchInput(event)) {
        // Touch display mode pans with a single finger from empty space.  A
        // mouse (or pen) always gets box selection, even in a narrow pane.
        this.selectBox = null;
        this.drag = {
          pan: true, moved: false,
          startX: event.clientX, startY: event.clientY,
          ox: this.pan.x, oy: this.pan.y,
        };
        this.canvas.style.cursor = "grabbing";
      } else {
        this.selectBox = { startX: px, startY: py, currentX: px, currentY: py,
          moved: false, additive: false, contain: false };
        this.drag = null;
        this.canvas.style.cursor = "crosshair";
      }
    }
    this._draw();
  }

  async _deleteSelected({ skipConfirm = false } = {}) {
    const nodes = this._selectedNodeObjects();
    if (!nodes.length || !this.opts.onDelete) return;
    const ok = await this.opts.onDelete(nodes, { skipConfirm });
    if (!ok) return false;
    const removedNodes = nodes.map((node) => ({ ...node }));
    const ids = new Set(nodes.map((node) => node.id));
    const removedEdges = this.edges.filter((edge) => ids.has(edge.source) || ids.has(edge.target))
      .map((edge) => ({ ...edge }));
    this.nodes = this.nodes.filter((node) => !ids.has(node.id));
    this.edges = this.edges.filter((edge) => !ids.has(edge.source) && !ids.has(edge.target));
    this.byId = new Map(this.nodes.map((node) => [node.id, node]));
    this.selectedNodes = new Set();
    this.selected = null;
    this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
    if (this.layout === "node") this._layoutNodeGraph(false);
    else if (getMindSettings().autoLayout && this.layout !== "force") this._applyLayout(false);
    else {
      if (this.layout === "force") {
        this.alpha = Math.max(this.alpha, 0.35);
        this._start();
      }
      this._draw();
    }
    this._pushHistory({
      undo: async () => {
        if (this.opts.onRestoreNodes) await this.opts.onRestoreNodes(removedNodes);
        if (this.opts.onRestoreLinks) await this.opts.onRestoreLinks(removedEdges);
        for (const node of removedNodes) {
          if (!this.byId.has(node.id)) this.nodes.push(node);
        }
        this.byId = new Map(this.nodes.map((node) => [node.id, node]));
        for (const edge of removedEdges) {
          if (!this.edges.some((item) => item.id === edge.id)) this.edges.push(edge);
        }
        if (this.layout === "mind") this._applyLayout(false);
        else if (this.layout === "node") this._layoutNodeGraph(false);
        else this._draw();
      },
    });
    return true;
  }

  addNodeAt(unit, world, link = null) {
    if (!unit) return null;
    let node = this.byId.get(unit.id);
    if (node) {
      node.x = world.x; node.y = world.y; node.vx = 0; node.vy = 0;
    } else {
      node = {
        id: unit.id, uid: unit.uid || unit.id, title: unit.title || t("common.untitled"),
        type: unit.type || "idea", status: unit.status || "open",
        importance: unit.importance || "p2", tags: unit.tags || [],
        summary: unit.summary || "", degree: 0, x: world.x, y: world.y,
        vx: 0, vy: 0,
      };
      this.nodes.push(node);
    }
    this.byId = new Map(this.nodes.map((item) => [item.id, item]));
    if (link && link.source && link.target) {
      const exists = this.edges.some((edge) => edge.source === link.source && edge.target === link.target && edge.relation === link.relation);
      if (!exists) this.edges.push({ id: link.id || `local-${link.source}-${link.target}`, source: link.source, target: link.target, relation: link.relation || "derived_from" });
    }
    this._setSelection([node], false);
    // Keep the imported node exactly where it was dropped. Do not wake the
    // force simulation here: that would move the rest of the graph away.
    if (this.layout === "node") this._saveNodeGraphState();
    this._draw();
    this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
    return node;
  }

  _insertLocalLink(source, target, link, relation = "unset") {
    if (!source || !target) return;
    if (this.edges.some((edge) => edge.source === source.id && edge.target === target.id && edge.relation === relation)) return;
    const created = { id: link?.id || `local-${source.id}-${target.id}`, source: source.id, target: target.id, relation };
    this.edges.push(created);
    this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
    if (this.layout === "node") this._saveNodeGraphState();
    this._pushHistory({
      undo: async () => {
        if (this.opts.onDeleteLink && /^\d+$/.test(String(created.id))) {
          await this.opts.onDeleteLink(created.id);
        }
        this.edges = this.edges.filter((edge) => edge.id !== created.id);
        this._draw();
      },
    });
    this._draw();
  }

  _selectedNodeObjects() {
    return [...this.selectedNodes].map((id) => this.byId.get(id)).filter(Boolean);
  }

  _setSelection(nodes, additive = false) {
    this.selectedEdge = null;
    if (!additive) this.selectedNodes = new Set();
    for (const node of nodes) {
      if (!node) continue;
      if (additive && this.selectedNodes.has(node.id)) this.selectedNodes.delete(node.id);
      else this.selectedNodes.add(node.id);
    }
    const list = this._selectedNodeObjects();
    this.selected = list.length ? list[list.length - 1].id : null;
  }

  _pointerMove(event) {
    if (this.thumbPress && Math.hypot(event.clientX - this.thumbPress.startX, event.clientY - this.thumbPress.startY) > MEDIA_CLICK_SLOP) {
      this.thumbPress.moved = true;
    }
    if (this._lineDragActive() && event.pointerType === "mouse" && event.buttons === 0) {
      // The release was lost (another window, an OS gesture).  Never let the
      // line keep following a hovering mouse.
      this._finishLineDrag(event, { missedRelease: true });
      return;
    }
    const rect0 = this.canvas.getBoundingClientRect();
    if (this.pointers.has(event.pointerId)) {
      this.pointers.set(event.pointerId, {
        x: event.clientX - rect0.left, y: event.clientY - rect0.top,
      });
    }
    if (this.pinch && this.pointers.size >= 2) {
      const [a, b] = [...this.pointers.values()];
      const distance = Math.max(1, Math.hypot(b.x - a.x, b.y - a.y));
      const midX = (a.x + b.x) / 2;
      const midY = (a.y + b.y) / 2;
      const zoom = Math.max(0.18, Math.min(3.2, this.pinch.zoom * (distance / this.pinch.distance)));
      const worldX = (this.pinch.midX - this.width / 2 - this.pinch.pan.x) / this.pinch.zoom;
      const worldY = (this.pinch.midY - this.height / 2 - this.pinch.pan.y) / this.pinch.zoom;
      this.zoom = zoom;
      this.pan.x = midX - this.width / 2 - worldX * zoom;
      this.pan.y = midY - this.height / 2 - worldY * zoom;
      this._draw();
      return;
    }
    if (this.mindResizeDrag) {
      const drag = this.mindResizeDrag;
      drag.moved = true;
      const dx = (event.clientX - drag.startX) / Math.max(0.18, this.zoom);
      const width = clamp(drag.startW + dx, ...this._cardWidthRange(drag.node));
      if (this.layout === "node") {
        // Height follows the wrapped title while dragging.
        drag.node.nodeW = width;
        applyCardMetrics(drag.node, cardMetrics(drag.node, {
          kind: "node", showImages: getMindSettings().showMindImages !== false, customW: width }));
        this._nodeRouteCache = null;
      } else {
        const imageW = drag.node.imageW || 0;
        drag.node.customW = width;
        drag.node.w = drag.node.customW;
        drag.node.titleW = Math.max(126, drag.node.customW - imageW);
      }
      this._draw();
      return;
    }
    if (this.edgeRouteDrag) {
      const rect = this.canvas.getBoundingClientRect();
      const world = this._world(event.clientX - rect.left, event.clientY - rect.top);
      this.edgeRouteDrag.edge.routeBend = { x: world.x, y: world.y };
      this._draw();
      return;
    }
    if (this.imageResizeDrag) {
      const drag = this.imageResizeDrag;
      drag.moved = true;
      const dx = (event.clientX - drag.startX) / Math.max(0.18, this.zoom);
      const dy = (event.clientY - drag.startY) / Math.max(0.18, this.zoom);
      const signX = drag.corner.includes("e") ? 1 : -1;
      const signY = drag.corner.includes("s") ? 1 : -1;
      const byWidth = drag.startW + dx * signX;
      const byHeight = (drag.startH + dy * signY) * drag.aspect;
      const candidate = Math.abs(byWidth - drag.startW) > Math.abs(byHeight - drag.startW)
        ? byWidth : byHeight;
      const width = clamp(candidate, 80, 620);
      const height = width / Math.max(0.2, drag.aspect);
      const meta = drag.node.pinnedMeta;
      if (!meta) return;
      drag.node.pinnedImage = {
        ...(drag.node.pinnedImage || {}),
        mediaId: meta.item?.id,
        side: meta.side || "right",
        w: width, h: height, aspect: drag.aspect,
      };
      drag.node.pinnedMeta = { ...meta, w: width, h: height };
      if (meta.side === "left" || meta.side === "right") {
        drag.node.w = (drag.node.titleW || 160) + width + 8;
        drag.node.h = Math.max(drag.node.contentH || 38, height);
      } else {
        drag.node.w = Math.max(drag.node.titleW || 160, width);
        drag.node.h = (drag.node.contentH || 38) + height + 8;
      }
      this._draw();
      return;
    }
    if (this.imageSideDrag) {
      const drag = this.imageSideDrag;
      const node = drag.node;
      if (Math.abs(event.clientX - drag.startX) + Math.abs(event.clientY - drag.startY) > 4) drag.moved = true;
      if (drag.moved) {
        const rect = this.canvas.getBoundingClientRect();
        const px = event.clientX - rect.left, py = event.clientY - rect.top;
        const tr = this._transform();
        const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
        const nx = (px - sx) / Math.max(1, (node.w || 160) * tr.z / 2);
        const ny = (py - sy) / Math.max(1, (node.h || 38) * tr.z / 2);
        const side = Math.abs(nx) > Math.abs(ny) ? (nx >= 0 ? "right" : "left") : (ny >= 0 ? "bottom" : "top");
        drag.side = side;
        this.imageSideHover = { node, side };
        this._draw();
      }
      return;
    }
    if (this._cardLayout() && !this.drag && !this.imageResizeDrag && !this.imageSideDrag) {
      const hit = this._hitMindImage(event);
      const changed = String(hit?.item?.id || "") !== String(this.imageHover?.item?.id || "");
      this.imageHover = hit;
      if (changed) {
        if (hit?.corner) this.canvas.style.cursor = `${hit.corner}-resize`;
        else if (hit?.pinned) this.canvas.style.cursor = "move";
        this._draw();
      }
      if (hit) return;
    }
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    if (this.edgePick) {
      if (Math.abs(event.clientX - this.edgePick.startX) + Math.abs(event.clientY - this.edgePick.startY) > 3) this.edgePick.moved = true;
      return;
    }
    if (this.selectBox) {
      this.selectBox.currentX = px;
      this.selectBox.currentY = py;
      if (Math.abs(px - this.selectBox.startX) + Math.abs(py - this.selectBox.startY) > 3) this.selectBox.moved = true;
      this._draw();
      return;
    }
    if (this.linkDrag) {
      this.linkDrag.current = { x: px, y: py };
      this._draw();
      return;
    }
    if (this._multiDown) return;
    if (this.drag) {
      const dx = event.clientX - this.drag.startX, dy = event.clientY - this.drag.startY;
      if (Math.abs(dx) + Math.abs(dy) > 3) this.drag.moved = true;
      if (this.drag.node) {
        if (this.opts.externalDrag) {
          const r = this.canvas.getBoundingClientRect();
          const outside = event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom;
          if (outside && !this.externalDrag) {
            const picked = this.selectedNodes.has(this.drag.node.id) && this.selectedNodes.size > 1
              ? [...this.selectedNodes].map((id) => this.byId.get(id)).filter(Boolean)
              : [this.drag.node];
            this.externalDrag = { node: this.drag.node, nodes: picked };
            this.drag.node.x = this.drag.ox;
            this.drag.node.y = this.drag.oy;
            this._showGhost(this.drag.node, event, picked.length);
            this._draw();
          }
          if (this.externalDrag) { this._moveGhost(event); return; }
        }
        if (this.layout === "mind" && getMindSettings().autoLayout && !this.drag.node.mindOrphan) return;
        const world = this._world(px, py);
        this.drag.node.x = world.x;
        this.drag.node.y = world.y;
        this.drag.node.vx = 0; this.drag.node.vy = 0;
        if (this.layout === "force") {
          this.alpha = Math.max(this.alpha, 0.3);
          this._start();
        }
        this._draw();
      } else if (this.drag.pan) {
        this.pan.x = this.drag.ox + dx;
        this.pan.y = this.drag.oy + dy;
        this._draw();
      }
      return;
    }
    const node = this._hit(event);
    const edge = node ? null : this._hitEdge(event);
    if (node !== this.hover || (edge?.id || null) !== (this.hoverEdge?.id || null)) {
      // Hover intent prefetches the node's details (app.js).
      if (node !== this.hover && !this.opts?.backdrop && typeof CustomEvent === "function") {
        globalThis.dispatchEvent?.(new CustomEvent("ark:graph-hover", { detail: { id: node?.id ?? null } }));
      }
      this.hover = node;
      this.hoverEdge = edge;
      this.canvas.style.cursor = (node || edge) ? "pointer" : "grab";
      this._draw();
    }
    if (node) {
      this._showTooltip(node, px, py);
    } else if (edge) {
      this._showEdgeTooltip(edge, px, py);
    } else {
      this._hideTooltip();
    }
  }

  _finishSelectBox() {
    if (!this.selectBox) return;
    const box = this.selectBox;
    this.selectBox = null;
    this.canvas.style.cursor = "grab";
    if (box.moved) {
      const x1 = Math.min(box.startX, box.currentX), x2 = Math.max(box.startX, box.currentX);
      const y1 = Math.min(box.startY, box.currentY), y2 = Math.max(box.startY, box.currentY);
      const tr = this._transform();
      const hidden = this.layout === "mind" ? this._mindTree().hidden : new Set();
      const hits = this.nodes.filter((node) => {
        if (hidden.has(node.id)) return false;
        const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
        if (!Number.isFinite(sx) || !Number.isFinite(sy)) return false;
        if (this.layout === "mind" || this.layout === "node") {
          const mind = this.layout === "mind";
          const hw = Math.max(mind ? 2 : 12, (node.w || (mind ? 160 : 200)) * tr.z) / 2;
          const hh = Math.max(mind ? 2 : 4, (node.h || (mind ? 38 : 64)) * tr.z) / 2;
          return box.contain
            ? sx - hw >= x1 && sx + hw <= x2 && sy - hh >= y1 && sy + hh <= y2
            : sx + hw >= x1 && sx - hw <= x2 && sy + hh >= y1 && sy - hh <= y2;
        }
        const r = this._nodeRadius(node, tr.z);
        if (box.contain) return sx - r >= x1 && sx + r <= x2 && sy - r >= y1 && sy + r <= y2;
        const dx = sx - clamp(sx, x1, x2), dy = sy - clamp(sy, y1, y2);
        return dx * dx + dy * dy <= r * r;
      });
      this._setSelection(hits, box.additive);
      this._suppressContextMenu = true;
    } else if (!box.additive) {
      this._setSelection([], false);
    }
    this._draw();
  }

  _lineDragActive() {
    return Boolean(this.linkDrag || this.edgeRouteDrag);
  }

  // Alt-drag links and node-graph route handles follow the pointer until the
  // button is released.  The release can land anywhere (inspector, menus,
  // outside the window) or never arrive (window blur, OS gestures), so these
  // drags also listen on the window and always end.
  _watchLineDrag() {
    this._unwatchLineDrag();
    const up = (event) => { if (this._lineDragActive()) this._pointerUp(event); };
    const blur = () => { if (this._lineDragActive()) this._finishLineDrag(null, { cancel: true }); };
    const key = (event) => {
      if (event.key !== "Escape" || !this._lineDragActive()) return;
      event.preventDefault();
      event.stopPropagation();
      this._finishLineDrag(null, { cancel: true });
    };
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
    window.addEventListener("blur", blur);
    window.addEventListener("keydown", key, true);
    this._lineDragUnwatch = () => {
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
      window.removeEventListener("blur", blur);
      window.removeEventListener("keydown", key, true);
    };
  }

  _unwatchLineDrag() {
    this._lineDragUnwatch?.();
    this._lineDragUnwatch = null;
  }

  // cancel: Escape/blur/pointercancel; nothing is created and a route handle
  // returns to where it was.  missedRelease: the button is already up but the
  // release was never seen; keep the route where it is, create no link.
  _finishLineDrag(event, { cancel = false, missedRelease = false } = {}) {
    this._unwatchLineDrag();
    if (event?.pointerId != null) this.pointers.delete(event.pointerId);
    this.canvas.style.cursor = "grab";
    const route = this.edgeRouteDrag;
    const link = this.linkDrag;
    this.edgeRouteDrag = null;
    this.linkDrag = null;
    if (route) {
      const { edge, oldBend } = route;
      const restore = () => {
        if (oldBend) edge.routeBend = { ...oldBend };
        else delete edge.routeBend;
      };
      if (cancel) restore();
      else {
        const bend = edge.routeBend;
        if (bend && (!oldBend || bend.x !== oldBend.x || bend.y !== oldBend.y)) {
          this._saveNodeGraphState();
          this._pushHistory({ undo: () => { restore(); this._saveNodeGraphState(); this._draw(); } });
        }
      }
    }
    this._draw();
    if (!link || cancel || missedRelease || !event) return;
    const source = link.source;
    const target = this._releaseOnCanvas(event) ? this._hit(event) : null;
    if (target && target.id !== source.id) {
      Promise.resolve(this.opts.onConnect?.(source, target)).then((result) => {
        if (result?.link) this._insertLocalLink(source, target, result.link, result.relation || "unset");
      });
    }
  }

  _releaseOnCanvas(event) {
    const rect = this.canvas.getBoundingClientRect();
    if (Number.isFinite(rect.right) && (event.clientX < rect.left || event.clientX > rect.right
      || event.clientY < rect.top || event.clientY > rect.bottom)) return false;
    // A release over a toolbar, legend or menu above the canvas is not a drop.
    const top = typeof document !== "undefined" ? document.elementFromPoint?.(event.clientX, event.clientY) : null;
    return !top || top === this.canvas;
  }

  _pointerUp(event) {
    this.pointers.delete(event.pointerId);
    if (this.pinch) {
      if (this.pointers.size < 2) {
        this.pinch = null;
        this.canvas.style.cursor = "grab";
      }
      if (this.pointers.size === 0) this._touchSelection = null;
      return;
    }
    if (this.pointers.size === 0) this._touchSelection = null;
    if (this._lineDragActive()) {
      this._finishLineDrag(event, { cancel: event.type === "pointercancel" });
      return;
    }
    const press = this.thumbPress;
    this.thumbPress = null;
    if (press && !press.moved && event.type !== "pointercancel" && !this.imageResizeDrag?.moved && !this.imageSideDrag?.moved) {
      this.imageResizeDrag = null;
      this.imageSideDrag = null;
      this.imageSideHover = null;
      this.canvas.style.cursor = "grab";
      this._previewCardMedia(press.hit);
      return;
    }
    if (this.mindResizeDrag) {
      const drag = this.mindResizeDrag;
      const node = drag.node;
      this.mindResizeDrag = null;
      this.canvas.style.cursor = "grab";
      if (!drag.moved) return;
      const width = this.layout === "node" ? node.nodeW : node.customW;
      this._setCardWidth(node, width);
      const layout = this.layout;
      this._pushHistory({
        undo: () => {
          if (this.layout !== layout || !this.byId.has(node.id)) return;
          this._setCardWidth(node, drag.startStored);
        },
      });
      return;
    }
    if (this.imageResizeDrag) {
      const drag = this.imageResizeDrag;
      const node = drag.node;
      if (!drag.moved) {
        this.imageResizeDrag = null;
        this.canvas.style.cursor = "grab";
        return;
      }
      const old = drag.startImage ? { ...drag.startImage } : null;
      this.imageResizeDrag = null;
      this.canvas.style.cursor = "grab";
      this._saveMindPinnedImages();
      this._applyLayout(false);
      this._pushHistory({
        undo: () => {
          if (old) node.pinnedImage = { ...old };
          else delete node.pinnedImage;
          this._saveMindPinnedImages();
          this._applyLayout(false);
        },
      });
      return;
    }
    if (this.imageSideDrag) {
      const drag = this.imageSideDrag;
      this.imageSideDrag = null;
      this.imageSideHover = null;
      this.canvas.style.cursor = "grab";
      if (drag.moved) {
        if (drag.node.pinnedImage) drag.node.pinnedImage = { ...drag.node.pinnedImage, side: drag.side };
        this._saveMindPinnedImages();
        this._applyLayout(false);
        this._pushHistory({
          undo: () => {
            if (drag.node.pinnedImage) drag.node.pinnedImage = { ...drag.node.pinnedImage, side: drag.originalSide };
            this._saveMindPinnedImages();
            this._applyLayout(false);
          },
        });
      }
      return;
    }
    if (this.edgePick) {
      const pick = this.edgePick;
      this.edgePick = null;
      this.canvas.style.cursor = "grab";
      if (!pick.moved) {
        this._setSelection([], false);
        this.selectedEdge = pick.edge;
        this._draw();
        this.opts.onEdgeSelect?.(pick.edge);
      }
      return;
    }
    if (this.externalDrag) {
      const node = this.externalDrag.node;
      const nodes = this.externalDrag.nodes || [node];
      this.externalDrag = null;
      this._hideGhost();
      this.drag = null;
      this.canvas.style.cursor = "grab";
      this._draw();
      const target = document.elementFromPoint(event.clientX, event.clientY);
      const zone = target?.closest?.("[data-infohub-drop]");
      if (zone) this.opts.onExternalDrop?.(nodes, event, zone);
      return;
    }
    if (this.selectBox) {
      this._finishSelectBox();
      return;
    }
    if (this._multiDown) {
      this._multiDown = null;
      this.canvas.style.cursor = "grab";
      return;
    }
    if (!this.drag) return;
    const drag = this.drag;
    if (drag.startX != null && drag.startY != null) {
      const dx = event.clientX - drag.startX, dy = event.clientY - drag.startY;
      if (Math.abs(dx) + Math.abs(dy) > 5) drag.moved = true;
    }
    this.drag = null;
    this.canvas.style.cursor = "grab";
    if (drag.right) {
      const cancelled = event.type === "pointercancel";
      this._suppressContextMenu = drag.moved || cancelled || drag.contextMenuPending;
      this._draw();
      if (!drag.moved && !cancelled && drag.contextMenuPending) this._showContextMenu(event);
      return;
    }
    if (drag.node && !drag.moved) {
      if (this.connectMode) this._connectClick(drag.node);
      else {
        if (!drag.keepMulti) this._setSelection([drag.node], false);
        this._draw();
        if (this.mode === "read" || this.opts.isInspectorOpen?.()) {
          this.opts.onSelect?.(drag.node, false, event);
        }
      }
    } else if (!drag.node && !drag.moved) {
      if (this.connectMode) this.cancelConnect();
      else {
        this._setSelection([], false);
        this._draw();
        if (this.mode === "read") this.opts.onSelect?.(null, false, event);
      }
    }
    if (drag.pan && drag.moved) this._suppressContextMenu = true;
    if (drag.node && drag.moved && this.layout === "force") this._start();
    if (drag.node && drag.moved) {
      const node = drag.node;
      const start = drag.startPos;
      // A card dragged in Mind stays where it was put through later layouts.
      const wasManual = node.mindManual;
      if (this.layout === "mind") node.mindManual = true;
      if (start && (start.x !== node.x || start.y !== node.y)) {
        this._pushHistory({
          undo: () => {
            if (!this.byId.has(node.id)) return;
            node.mindManual = wasManual;
            node.x = start.x;
            node.y = start.y;
            node.vx = 0; node.vy = 0;
            if (this.layout === "node") this._saveNodeGraphState();
            this._draw();
          },
        });
      }
      if (this.layout === "node") this._saveNodeGraphState();
    }
  }

  _cancelWheelSmoothing() {
    if (this._wheelFrame) cancelAnimationFrame(this._wheelFrame);
    this._wheelFrame = 0;
    this._wheelTarget = null;
  }

  _startWheelSmoothing() {
    if (this._wheelFrame) return;
    const tick = () => {
      const target = this._wheelTarget;
      if (!target) { this._wheelFrame = 0; return; }
      const dz = target.zoom - this.zoom;
      const dx = target.pan.x - this.pan.x;
      const dy = target.pan.y - this.pan.y;
      if (Math.abs(dz) < 0.0004 && Math.abs(dx) < 0.25 && Math.abs(dy) < 0.25) {
        this.zoom = target.zoom;
        this.pan = { ...target.pan };
        this._wheelFrame = 0;
        this._wheelTarget = null;
        this._draw();
        return;
      }
      const ease = 0.24;
      this.zoom += dz * ease;
      this.pan.x += dx * ease;
      this.pan.y += dy * ease;
      this._draw();
      this._wheelFrame = requestAnimationFrame(tick);
    };
    this._wheelFrame = requestAnimationFrame(tick);
  }

  _wheel(event) {
    this._cancelCameraAnimation();
    this._cancelWheelSmoothing();
    event.preventDefault();
    this._userInteracted = true;
    const rect = this.canvas.getBoundingClientRect();
    const px = event.clientX - rect.left, py = event.clientY - rect.top;
    const unit = event.deltaMode === 1 ? 18 : event.deltaMode === 2 ? this.height : 1;
    const dy = (event.deltaY || 0) * unit;
    if (!this._wheelTarget) this._wheelTarget = { zoom: this.zoom, pan: { ...this.pan } };
    const target = this._wheelTarget;
    if (event.ctrlKey || event.metaKey) {
      target.pan.y -= dy;
    } else if (event.shiftKey) {
      target.pan.x -= dy;
    } else {
      const before = {
        x: (px - this.width / 2 - target.pan.x) / target.zoom,
        y: (py - this.height / 2 - target.pan.y) / target.zoom,
      };
      target.zoom = clamp(target.zoom * Math.exp(-dy * 0.0012), 0.18, 3.2);
      const after = {
        x: (px - this.width / 2 - target.pan.x) / target.zoom,
        y: (py - this.height / 2 - target.pan.y) / target.zoom,
      };
      target.pan.x += (after.x - before.x) * target.zoom;
      target.pan.y += (after.y - before.y) * target.zoom;
    }
    this._startWheelSmoothing();
  }

  _showTooltip(node, px, py) {
    if (!this.tooltip) return;
    clear(this.tooltip);
    this.tooltip.append(
      h("b", null, node.title),
      h("div", null, `${node.kind ? `${node.kind} · ` : `${typeLabel(node.type)} · `}${statusLabel(node.status)}${node.n_messages != null ? ` · ${t("graph.tooltip.messages", { n: node.n_messages })}` : ""}${node.for_me ? ` · ${t("graph.tooltip.for_me")}` : ""}`),
      h("div", { style: { color: "var(--mute)", marginTop: "2px" } }, `${node.uid} · ${node.degree || 0} ${t("common.links")}`));
    this.tooltip.classList.remove("hidden");
    const tw = 250, th = 70;
    this.tooltip.style.left = `${clamp(px + 14, 6, this.width - tw)}px`;
    this.tooltip.style.top = `${clamp(py + 14, 6, this.height - th)}px`;
  }

  _showEdgeTooltip(edge, px, py) {
    if (!this.tooltip) return;
    const a = this.byId.get(edge.source), b = this.byId.get(edge.target);
    clear(this.tooltip);
    this.tooltip.append(
      h("b", null, edge.relation === "unset" ? t("graph.tooltip.unset_relation") : relLabel(edge.relation)),
      h("div", null, `${a?.title || ""} → ${b?.title || ""}`),
      h("div", { style: { color: "var(--mute)", marginTop: "2px" } }, t("graph.tooltip.change_relation")));
    this.tooltip.classList.remove("hidden");
    this.tooltip.style.left = `${clamp(px + 14, 6, this.width - 250)}px`;
    this.tooltip.style.top = `${clamp(py + 14, 6, this.height - 70)}px`;
  }

  _hideTooltip() { this.tooltip?.classList.add("hidden"); }

  zoomBy(factor) {
    this._userInteracted = true;
    const cx = this.width / 2, cy = this.height / 2;
    const before = this._world(cx, cy);
    this.zoom = clamp(this.zoom * factor, 0.18, 3.2);
    const after = this._world(cx, cy);
    this.pan.x += (after.x - before.x) * this.zoom;
    this.pan.y += (after.y - before.y) * this.zoom;
    this._draw();
  }

  repairView() {
    if (!this.nodes.length) return;
    let repaired = false;
    for (const node of this.nodes) {
      if (!Number.isFinite(node.x) || !Number.isFinite(node.y)) {
        node.x = (Math.random() - 0.5) * Math.max(160, this.width * 0.6);
        node.y = (Math.random() - 0.5) * Math.max(160, this.height * 0.6);
        node.vx = 0; node.vy = 0;
        repaired = true;
      }
    }
    const tr = this._transform();
    const visible = this.nodes.some((node) => {
      const x = node.x * tr.z + tr.x, y = node.y * tr.z + tr.y;
      return x > 8 && x < this.width - 8 && y > 8 && y < this.height - 8;
    });
    if (!repaired && visible) return;
    this.alpha = 0.5;
    this._start();
    this.fit();
    this._draw();
  }

  _cancelCameraAnimation() {
    if (this._cameraFrame) cancelAnimationFrame(this._cameraFrame);
    this._cameraFrame = 0;
    this._cancelWheelSmoothing();
  }

  _animateCamera(targetZoom, targetPan, duration = 340) {
    this._cancelCameraAnimation();
    if (window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
      this.zoom = targetZoom;
      this.pan = { ...targetPan };
      this._draw();
      return;
    }
    const startZoom = this.zoom;
    const startPan = { ...this.pan };
    const started = performance.now();
    const ease = (t) => 1 - Math.pow(1 - t, 3);
    const tick = (now) => {
      const raw = Math.min(1, Math.max(0, (now - started) / duration));
      const t = ease(raw);
      this.zoom = startZoom + (targetZoom - startZoom) * t;
      this.pan = {
        x: startPan.x + (targetPan.x - startPan.x) * t,
        y: startPan.y + (targetPan.y - startPan.y) * t,
      };
      this._draw();
      if (raw < 1) this._cameraFrame = requestAnimationFrame(tick);
      else this._cameraFrame = 0;
    };
    this._cameraFrame = requestAnimationFrame(tick);
  }

  _nodeRadius(node, zoom = this.zoom) {
    const compact = this.opts.compact && !this.opts.backdrop;
    // The backdrop uses Map world sizes; its canvas transform applies zoom.
    return forceNodeRadius(node, this.opts.backdrop ? 1 : zoom, compact);
  }

  _fitBackdrop() {
    let radius = 1;
    for (const node of this.nodes) {
      if (Number.isFinite(node.x) && Number.isFinite(node.y)) {
        radius = Math.max(radius, Math.hypot(node.x, node.y));
      }
    }
    // The node envelope is 40% tighter than the initial 2H composition.
    this.zoom = this.nodes.length > 1 ? (1.2 * this.height) / radius : 1;
    this.pan = { x: this.width * 0.5, y: 0 };
  }

  fit({ animate = false } = {}) {
    if (!this.nodes.length) return;
    if (this.opts.backdrop) {
      this._fitBackdrop();
      this._draw();
      return;
    }
    this._cancelCameraAnimation();
    this._userInteracted = false;
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    const fitNodes = this.layout === "mind" ? this._visibleMindNodes() : this.nodes;
    for (const node of fitNodes) {
      if (!Number.isFinite(node.x)) continue;
      const hw = this.layout === "mind" ? (node.w || 160) / 2 : this.layout === "node" ? (node.w || 200) / 2 : 0;
      const hh = this.layout === "mind" ? (node.h || 38) / 2 : this.layout === "node" ? (node.h || 64) / 2 : 0;
      minX = Math.min(minX, node.x - hw); maxX = Math.max(maxX, node.x + hw);
      minY = Math.min(minY, node.y - hh); maxY = Math.max(maxY, node.y + hh);
    }
    if (!Number.isFinite(minX)) return;
    // Fit force graphs symmetrically around their fixed world origin. Changing
    // the visible subset may change zoom, but must not introduce a new centre.
    if (this.layout === "force") {
      maxX = Math.max(Math.abs(minX), Math.abs(maxX)); minX = -maxX;
      maxY = Math.max(Math.abs(minY), Math.abs(maxY)); minY = -maxY;
    }
    const w = Math.max(1, maxX - minX), hh = Math.max(1, maxY - minY);
    const fill = this.opts.compact ? 0.88 : 0.8;
    const targetZoom = clamp(
      Math.min((this.width * fill) / w, (this.height * fill) / hh),
      0.18, 1.35,
    );
    const cx = (minX + maxX) / 2, cy = (minY + maxY) / 2;
    const targetPan = { x: -cx * targetZoom, y: -cy * targetZoom };
    if (animate) this._animateCamera(targetZoom, targetPan);
    else {
      this.zoom = targetZoom;
      this.pan = targetPan;
      this._draw();
    }
  }

  focus(node) {
    this.selected = node.id;
    this.selectedNodes = new Set([node.id]);
    this.pan = { x: -node.x * this.zoom, y: -node.y * this.zoom };
    this._draw();
  }

  startConnect() {
    if (this.opts.compact || !this.connectBar) return;
    this.connectMode = true;
    this.connectSource = null;
    this.canvas.style.cursor = "crosshair";
    this._renderConnectBar();
  }

  cancelConnect() {
    this.connectMode = false;
    this.connectSource = null;
    this.canvas.style.cursor = "grab";
    this.connectBar?.classList.add("hidden");
  }

  _connectClick(node) {
    if (!this.connectSource) {
      this.connectSource = node;
      this._renderConnectBar();
      return;
    }
    if (node.id === this.connectSource.id) return;
    const source = this.connectSource;
    this.cancelConnect();
    Promise.resolve(this.opts.onConnect?.(source, node)).then((result) => {
      if (result?.link) this._insertLocalLink(source, node, result.link, result.relation || "unset");
    });
  }

  _renderConnectBar() {
    if (!this.connectBar) return;
    clear(this.connectBar);
    this.connectBar.classList.remove("hidden");
    if (!this.connectSource) {
      this.connectBar.append(h("span", null, t("graph.connect_hint")),
        h("button", { class: "btn sm ghost", onclick: () => this.cancelConnect() }, t("graph.cancel")));
    } else {
      this.connectBar.append(
        h("span", null, t("graph.connect_source", { name: this.connectSource.title })),
        h("button", { class: "btn sm ghost", onclick: () => this.cancelConnect() }, t("graph.cancel")));
    }
  }

  updateEdge(edgeId, patch) {
    const edge = this.edges.find((item) => item.id === edgeId);
    if (!edge) return;
    Object.assign(edge, patch);
    this._draw();
  }

  setSelected(id) {
    this.selected = id;
    if (id) this.selectedNodes = new Set([id]);
    else this.selectedNodes = new Set();
    this._draw();
  }

  setNodeType(ref, type) {
    if (!type) return false;
    const node = this.byId?.get(ref)
      || (this.nodes || []).find((item) => item.uid === ref || String(item.id) === String(ref));
    if (!node) return false;
    node.type = type;
    node.color = null;
    this._draw();
    return true;
  }

  _positionEditor() {
    const node = this.editingNodeId ? this.byId.get(this.editingNodeId) : null;
    if (!node || !this.editor) return;
    const sx = node.x * this.zoom + this.width / 2 + this.pan.x;
    const sy = node.y * this.zoom + this.height / 2 + this.pan.y;
    let width;
    let height;
    let left;
    let top;
    if (this._cardLayout()) {
      const cardW = (node.w || 190) * this.zoom;
      width = Math.max(60, (node.titleW || node.w || 190) * this.zoom);
      height = Math.max(24, (node.h || 44) * this.zoom);
      left = clamp(sx - cardW / 2, 4, this.width - width - 4);
      top = clamp(sy - height / 2, 4, this.height - height - 4);
    } else {
      const ctx = this.ctx;
      ctx.save();
      ctx.font = "600 11px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      const maxWidth = this.selectedNodes.has(node.id) ? 210 : 150;
      const lines = wrap(ctx, node.title || "", maxWidth, 2);
      const textWidth = lines.length ? Math.max(...lines.map((line) => ctx.measureText(line).width)) : 0;
      ctx.restore();
      width = Math.max(60, textWidth + 10);
      height = Math.max(22, lines.length * 14 + 4);
      const r = this._nodeRadius(node);
      left = clamp(sx - width / 2, 4, this.width - width - 4);
      top = clamp(sy + r + 3, 4, this.height - height - 4);
    }
    this.editor.style.left = `${left}px`;
    this.editor.style.top = `${top}px`;
    this.editor.style.width = `${width}px`;
    this.editor.style.height = `${height}px`;
  }

  startEdit(node) {
    if (!node) return;
    this._closeEditor(true);
    const sx = node.x * this.zoom + this.width / 2 + this.pan.x;
    const sy = node.y * this.zoom + this.height / 2 + this.pan.y;
    let width;
    let height;
    let left;
    let top;
    let fontStyle = {};
    if (this._cardLayout()) {
      const cardW = (node.w || 190) * this.zoom;
      width = Math.max(60, (node.titleW || node.w || 190) * this.zoom);
      height = Math.max(24, (node.h || 44) * this.zoom);
      left = clamp(sx - cardW / 2, 4, this.width - width - 4);
      top = clamp(sy - height / 2, 4, this.height - height - 4);
      fontStyle = { lineHeight: "1.3", whiteSpace: "pre-wrap" };
    } else {
      const ctx = this.ctx;
      ctx.save();
      ctx.font = "600 11px system-ui, 'Segoe UI', 'Microsoft YaHei'";
      const maxWidth = this.selectedNodes.has(node.id) ? 210 : 150;
      const lines = wrap(ctx, node.title || "", maxWidth, 2);
      const textWidth = lines.length ? Math.max(...lines.map((line) => ctx.measureText(line).width)) : 0;
      ctx.restore();
      width = Math.max(60, textWidth + 10);
      height = Math.max(22, lines.length * 14 + 4);
      const r = this._nodeRadius(node);
      left = clamp(sx - width / 2, 4, this.width - width - 4);
      top = clamp(sy + r + 3, 4, this.height - height - 4);
      fontStyle = { font: "600 11px/1.3 system-ui, 'Segoe UI', 'Microsoft YaHei'", padding: "1px 4px" };
    }
    const input = h("textarea", {
      class: "graph-mind-editor",
      "aria-label": t("mind.edit_title"),
      rows: "1",
      style: {
        left: `${left}px`,
        top: `${top}px`,
        width: `${width}px`,
        height: `${height}px`,
        background: node.depth === 0 ? withAlpha(typeColor(node.type), 0.16) : "var(--card)",
        ...fontStyle,
      },
    });
    input.value = node.title || "";
    this.editingNodeId = node.id;
    this.editor = input;
    this.host.appendChild(input);
    this._positionEditor();
    this._draw();
    let closing = false;
    const finish = async (commit, restoreFocus = false) => {
      if (closing) return;
      closing = true;
      const title = input.value.trim();
      this._closeEditor(true);
      if (restoreFocus) this.host.focus({ preventScroll: true });
      if (!commit || !title || title === node.title) return;
      const previous = node.title;
      node.title = title;
      if (this.layout === "mind" && getMindSettings().autoLayout) this._applyLayout(false);
      this._draw();
      try {
        await this.opts.onRename?.(node, title);
        this._pushHistory({
          undo: async () => {
            node.title = previous;
            await this.opts.onRename?.(node, previous);
            if (this.layout === "mind" && getMindSettings().autoLayout) this._applyLayout(false);
            this._draw();
          },
        });
      } catch (err) {
        node.title = previous;
        if (this.layout === "mind" && getMindSettings().autoLayout) this._applyLayout(false);
        this._draw();
        this.opts.onError?.(err);
      }
    };
    input.addEventListener("keydown", (e) => {
      e.stopPropagation();
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); finish(true, true); }
      else if (e.key === "Escape") { e.preventDefault(); finish(false, true); }
    });
    input.addEventListener("input", () => {
      input.style.height = "auto";
      input.style.height = `${Math.min(220, Math.max(24, input.scrollHeight))}px`;
    });
    input.addEventListener("blur", () => finish(true, false));
    input.addEventListener("pointerdown", (e) => e.stopPropagation());
    setTimeout(() => { input.focus(); input.select(); }, 20);
  }

  _closeEditor(silent = false) {
    if (!this.editor) return;
    const input = this.editor;
    this.editor = null;
    this.editingNodeId = null;
    if (silent) {
      input.onblur = null;
      input.blur?.();
    }
    input.remove();
    this._draw();
  }

  async _insertBetween() {
    const node = this._selectedNodeObjects()[0];
    if (!node || !this.opts.onInsertBetween) return;
    try {
      const result = await this.opts.onInsertBetween(node);
      if (!result?.unit) return;
      const created = {
        ...result.unit,
        x: node.x + 150,
        y: node.y,
        vx: 0,
        vy: 0,
        w: 170,
        h: 46,
      };
      const removedId = result.removed_link?.id;
      this.edges = this.edges.filter((edge) => edge.id !== removedId);
      this.nodes.push(created);
      this.byId.set(created.id, created);
      const linkTargets = [
        { source: created.id, target: result.parent?.id },
        { source: result.child?.id, target: created.id },
      ];
      (result.links || []).forEach((link, index) => {
        const pair = linkTargets[index];
        if (!pair?.source || !pair?.target) return;
        const edge = { id: link.id, source: pair.source, target: pair.target, relation: link.relation || "part_of" };
        if (!this.edges.some((item) => item.id === edge.id)) this.edges.push(edge);
      });
      this._setSelection([created], false);
      this._applyLayout(false);
      setTimeout(() => this.startEdit(created), 40);
      this.opts.onNotice?.(t("graph.toast.inserted_between"));
    } catch (err) {
      this.opts.onError?.(err);
    }
  }

  _reconcileLocalNode(temp, result) {
    if (!temp || !result?.unit) return;
    const oldId = temp.id;
    const real = result.unit;
    const wasSelected = this.selectedNodes.has(oldId);
    const wasEditing = this.editingNodeId === oldId;
    Object.assign(temp, real);
    temp._pendingCreate = null;
    if (!Number.isFinite(temp.x)) temp.x = 0;
    if (!Number.isFinite(temp.y)) temp.y = 0;
    this.nodes = this.nodes.filter((node) => node !== temp);
    this.byId.delete(oldId);
    this.nodes.push(temp);
    this.byId.set(temp.id, temp);
    if (wasSelected) {
      this.selectedNodes.delete(oldId);
      this.selectedNodes.add(temp.id);
      this.selected = temp.id;
    }
    if (wasEditing) this.editingNodeId = temp.id;
    for (const edge of this.edges) {
      if (edge.source === oldId) edge.source = temp.id;
      if (edge.target === oldId) edge.target = temp.id;
      if (result.link?.id && edge.id === `local-${oldId}`) edge.id = result.link.id;
    }
    if (result.link && !this.edges.some((edge) => String(edge.id) === String(result.link.id))) {
      this.edges.push({ ...result.link, source: temp.id,
                        target: result.parentId || result.link.target,
                        relation: result.link.relation || "part_of" });
    }
    this.countEl && (this.countEl.textContent =
      `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
    this._draw();
  }

  async _addMindNode(where) {
    if (this._adding || !this.opts.onAddNode) return;
    const node = this._selectedNodeObjects()[0];
    if (!node || !this.opts.onAddNode) return;
    this._adding = true;
    try {
      const result = await this.opts.onAddNode(node, where);
      if (!result?.unit) return;
      const unit = result.unit;
      if (this.byId.has(unit.id)) return;
      const autoLayout = getMindSettings().autoLayout;
      const sibling = where !== "child";
      const forceLayout = this.layout === "force";
      let createdX = forceLayout
        ? node.x + (sibling ? 110 : 100)
        : node.x + (sibling ? 0 : (autoLayout ? 300 : 120));
      let createdY = forceLayout
        ? node.y + (sibling ? (where === "above" ? -70 : 70) : 0)
        : node.y + (sibling ? (where === "above" ? -90 : 90) : 0);
      if (forceLayout && sibling && result.parentId) {
        const parent = this.byId.get(result.parentId);
        if (parent) {
          const dx = node.x - parent.x;
          const dy = node.y - parent.y;
          const distance = Math.hypot(dx, dy);
          if (distance > 8) {
            // Preserve this level's parent distance; rotate slightly around
            // the parent so the new sibling does not cover the selected one.
            const angle = Math.atan2(dy, dx) + (where === "above" ? -0.35 : 0.35);
            createdX = parent.x + Math.cos(angle) * distance;
            createdY = parent.y + Math.sin(angle) * distance;
          }
        }
      }
      const created = {
        ...unit,
        x: createdX,
        y: createdY,
        vx: 0,
        vy: 0,
        w: 170,
        h: 46,
        degree: result.parentId ? 1 : 0,
      };
      this.nodes.push(created);
      this.byId.set(created.id, created);
      if (result.parentId) {
        this.edges.push({
          id: result.link?.id || `local-${created.id}`,
          source: created.id,
          target: result.parentId,
          relation: "part_of",
        });
      }
      this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
      if (result.pending) {
        result.pending.then((resolved) => this._reconcileLocalNode(created, resolved))
          .catch((err) => {
            this.nodes = this.nodes.filter((node) => node !== created);
            this.byId.delete(created.id);
            this.selectedNodes.delete(created.id);
            this.edges = this.edges.filter((edge) => edge.source !== created.id
              && edge.target !== created.id);
            if (this.editingNodeId === created.id) this._closeEditor(false);
            this._draw();
            this.opts.onError?.(err);
          });
      }
      this._setSelection([created], false);
      if (this.layout === "node") this._layoutNodeGraph(false);
      else if (autoLayout && this.layout !== "force") this._applyLayout(false);
      else if (this.layout === "force") {
        this.alpha = Math.max(this.alpha, 0.5);
        this._start();
      } else this._draw();
      setTimeout(() => this.startEdit(created), 40);
      if (result.orphan) this.opts.onNotice?.(t("mind.orphan_created"));
    } catch (err) {
      this.opts.onError?.(err);
    } finally {
      this._adding = false;
    }
  }

  async _addOrphanNode(event) {
    if (this._adding || this.mode !== "edit" || !this.opts.onAddOrphan) return;
    const rect = this.canvas.getBoundingClientRect();
    const world = this._world(event.clientX - rect.left, event.clientY - rect.top);
    this._adding = true;
    try {
      const result = await this.opts.onAddOrphan(world);
      if (!result?.unit) return;
      const unit = result.unit;
      if (this.byId.has(unit.id)) return;
      const created = {
        ...unit,
        x: world.x,
        y: world.y,
        vx: 0,
        vy: 0,
        w: 170,
        h: 46,
        degree: 0,
        mindOrphan: this.layout === "mind",
      };
      this.nodes.push(created);
      this.byId.set(created.id, created);
      this.countEl && (this.countEl.textContent = `${this.nodes.length} ${t("overview.stats.total")} · ${this.edges.length} ${t("common.links")}`);
      this._setSelection([created], false);
      if (this.layout === "force") {
        this.alpha = Math.max(this.alpha, 0.45);
        this._start();
      }
      this._draw();
      setTimeout(() => this.startEdit(created), 40);
      this.opts.onNotice?.(t("graph.toast.orphan_created"));
    } catch (err) {
      this.opts.onError?.(err);
    } finally {
      this._adding = false;
    }
  }

  _writeNodeReferenceClipboard(items) {
    const unique = [];
    const seen = new Set();
    for (const item of items || []) {
      const uid = String(item.uid || item.key || "").trim();
      if (!uid || seen.has(uid)) continue;
      seen.add(uid);
      unique.push({ uid, title: String(item.title || "").trim(), type: item.type || "" });
    }
    if (!unique.length || typeof navigator === "undefined") return;
    const plain = "Referenced ARK nodes:\n" + unique // i18n-ignore: clipboard text for coding agents
      .map((item) => `- ${item.uid}${item.title ? ` — ${item.title}` : ""}`).join("\n");
    const payload = encodeURIComponent(JSON.stringify({ version: 1, items: unique }));
    const escape = (value) => String(value).replace(/[&<>"]/g, (ch) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;",
    }[ch]));
    const html = `<div data-ark-node-refs="${payload}"><strong>Referenced ARK nodes</strong>`
      + `<ul>${unique.map((item) => `<li>${escape(item.uid)} — ${escape(item.title)}</li>`).join("")}</ul></div>`;
    const legacyCopy = () => {
      try {
        const area = document.createElement("textarea");
        area.value = plain;
        area.setAttribute("readonly", "");
        area.style.position = "fixed";
        area.style.left = "-9999px";
        document.body.appendChild(area);
        area.select();
        document.execCommand("copy");
        area.remove();
      } catch { /* clipboard permissions may deny all writes */ }
    };
    const write = async () => {
      if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
        await navigator.clipboard.write([new ClipboardItem({
          "text/plain": new Blob([plain], { type: "text/plain" }),
          "text/html": new Blob([html], { type: "text/html" }),
        })]);
      } else if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(plain);
      } else {
        legacyCopy();
      }
    };
    write().catch(legacyCopy);
  }

  _copySelection(recursive = false, { silent = false } = {}) {
    const selected = this._selectedNodeObjects();
    if (!selected.length) return;
    const ids = new Set(selected.map((node) => node.id));
    if (recursive) {
      const { children } = this._mindTree();
      const stack = selected.map((node) => node.id);
      while (stack.length) {
        const id = stack.pop();
        for (const child of children.get(id) || []) {
          if (ids.has(child)) continue;
          ids.add(child);
          stack.push(child);
        }
      }
    }
    const keyOf = (node) => String(node.uid || node.id);
    const copies = this.nodes.filter((node) => ids.has(node.id));
    const items = copies.map((node) => ({
      key: keyOf(node),
      title: node.title,
      type: node.type,
      status: node.status,
      importance: node.importance,
      body: node.body || node.excerpt || "",
      tags: [...(node.tags || [])],
      start_at: node.start_at || null,
      end_at: node.end_at || null,
      uid: node.uid || node.key || String(node.id),
    }));
    const links = this.edges
      .filter((edge) => ids.has(edge.source) && ids.has(edge.target))
      .map((edge) => {
        const source = this.byId.get(edge.source), target = this.byId.get(edge.target);
        return source && target ? { source: keyOf(source), target: keyOf(target), relation: edge.relation } : null;
      })
      .filter(Boolean);
    const copiedIds = new Set(copies.map((node) => node.id));
    const hierarchy = new Set([this.primaryRelation || "part_of"]);
    const copiedParent = new Map();
    for (const edge of this.edges) {
      if (!hierarchy.has(edge.relation) || !copiedIds.has(edge.source) || !copiedIds.has(edge.target)) continue;
      const child = this.byId.get(edge.source);
      if (child && !copiedParent.has(child.id)) copiedParent.set(child.id, edge.target);
    }
    const roots = copies
      .filter((node) => !copiedParent.has(node.id))
      .map((node) => keyOf(node));
    GRAPH_CLIPBOARD = { source: this.clipboardSource, items, links, roots };
    this._writeNodeReferenceClipboard(items);
    if (!silent) this.opts.onNotice?.(t(items.length === 1 ? "graph.toast.copied_one" : "graph.toast.copied", { n: items.length }));
  }

  async _cutSelection() {
    const selected = this._selectedNodeObjects();
    if (!selected.length || !this.opts.onDelete) return;
    const { children } = this._mindTree();
    const ids = new Set(selected.map((node) => node.id));
    const stack = selected.map((node) => node.id);
    while (stack.length) {
      const id = stack.pop();
      for (const child of children.get(id) || []) {
        if (ids.has(child)) continue;
        ids.add(child);
        stack.push(child);
      }
    }
    const subtree = this.nodes.filter((node) => ids.has(node.id));
    this._copySelection(true, { silent: true });
    this._setSelection(subtree, false);
    const ok = await this._deleteSelected({ skipConfirm: true });
    if (ok) this.opts.onNotice?.(t(subtree.length === 1 ? "graph.toast.cut_one" : "graph.toast.cut", { n: subtree.length }));
  }

  async _pasteClipboard() {
    if (!GRAPH_CLIPBOARD || this._pasting || !this.opts.onPasteNodes) return;
    const targets = this._selectedNodeObjects();
    // Pasting is also a first-class way to add roots.  With no selected
    // target the imported nodes stay orphans, centered in the visible graph.
    if (this.mode === "read") this.setMode("edit");
    this._pasting = true;
    try {
      const result = await this.opts.onPasteNodes(GRAPH_CLIPBOARD, targets);
      if (!result?.nodes?.length) return;
      const orphanPaste = !targets.length;
      const center = this._world((this.canvas.clientWidth || 800) / 2,
                                 (this.canvas.clientHeight || 600) / 2);
      const anchor = targets[0] || center;
      const columns = orphanPaste ? Math.max(1, Math.ceil(Math.sqrt(result.nodes.length))) : 1;
      const added = [];
      let index = 0;
      for (const item of result.nodes) {
        const unit = item.unit;
        if (!unit || this.byId.has(unit.id)) continue;
        const target = this.byId.get(item.targetId) || anchor;
        const angle = -Math.PI / 4 + (index % 6) * 0.45;
        const column = index % columns;
        const row = Math.floor(index / columns);
        const created = {
          ...unit,
          x: orphanPaste
            ? anchor.x + (column - (columns - 1) / 2) * 190
            : target.x + Math.cos(angle) * (80 + (index % 4) * 24),
          y: orphanPaste
            ? anchor.y + row * 92
            : target.y + Math.sin(angle) * (60 + (index % 4) * 20),
          vx: 0,
          vy: 0,
          w: 170,
          h: 38,
          degree: orphanPaste ? 0 : 1,
        };
        this.nodes.push(created);
        this.byId.set(created.id, created);
        added.push(created);
        index += 1;
      }
      for (const link of result.links || []) {
        if (!link?.id || !this.byId.has(link.source) || !this.byId.has(link.target)) continue;
        if (this.edges.some((edge) => edge.id === link.id)) continue;
        this.edges.push({ id: link.id, source: link.source, target: link.target, relation: link.relation || "unset" });
      }
      if (!added.length) return;
      this._setSelection(added, false);
      if (this.layout === "mind" && getMindSettings().autoLayout) this._applyLayout(false);
      else if (this.layout === "force") {
        this.alpha = Math.max(this.alpha, 0.45);
        this._start();
      } else this._draw();
      const pasted = [...added];
      this._pushHistory({
        undo: async () => {
          if (this.opts.onDelete) {
            const ok = await this.opts.onDelete(pasted, { skipConfirm: true });
            if (!ok) return;
          }
          this._removeNodesLocal(pasted);
          if (this.layout === "mind") this._applyLayout(false);
          else if (this.layout === "node") this._layoutNodeGraph(false);
        },
      });
      this.opts.onNotice?.(orphanPaste
        ? t(added.length === 1 ? "graph.toast.pasted_orphan_one" : "graph.toast.pasted_orphan", { n: added.length })
        : t(added.length === 1 ? "graph.toast.pasted_one" : "graph.toast.pasted", { n: added.length }));
    } catch (err) {
      this.opts.onError?.(err);
    } finally {
      this._pasting = false;
    }
  }

  destroy() {
    if (this._pasteImageHandler) {
      document.removeEventListener("paste", this._pasteImageHandler, true);
      this._pasteImageHandler = null;
    }
    this._hideGhost();
    if (this.cameraKey) GRAPH_CAMERAS.set(this.cameraKey, { zoom: this.zoom, pan: { ...this.pan }, layout: this.layout });
    this._closeEditor(true);
    this._unwatchLineDrag();
    this.linkDrag = null;
    this.edgeRouteDrag = null;
    this.running = false;
    cancelAnimationFrame(this.frame);
    this._cancelCameraAnimation();
    this._cancelWheelSmoothing();
    if (this._transitionFrame) cancelAnimationFrame(this._transitionFrame);
    this._transitionFrame = null;
    this._ghosts = [];
    this._resizeObserver?.disconnect();
    // Leave the host empty: a view rebuilt in the same host must not find
    // the old canvas (a stale frame, a double image) or toolbar behind it.
    this.canvas?.remove();
    this.tools?.remove();
    this.helpEl?.remove();
    if (this._helpOutside) document.removeEventListener("pointerdown", this._helpOutside, true);
    if (this._edgeFilterOutside) document.removeEventListener("pointerdown", this._edgeFilterOutside, true);
    this._spriteCache?.clear();
    if (this._spriteRedraw) cancelAnimationFrame(this._spriteRedraw);
    GRAPH_INSTANCES.delete(this);
    POS_CACHE.delete(this.id);
  }
}

function wrap(ctx, text, maxWidth, maxLines = 2) {
  const chars = [...String(text || "")];
  const lines = [];
  let line = "";
  let truncated = false;
  for (const ch of chars) {
    if (ch === "\n") {
      lines.push(line.trimEnd());
      line = "";
      if (lines.length >= maxLines) { truncated = true; break; }
      continue;
    }
    const test = line + ch;
    if (ctx.measureText(test).width <= maxWidth || !line) {
      line = test;
      continue;
    }
    lines.push(line.trimEnd());
    line = ch.trimStart();
    if (lines.length >= maxLines) { truncated = true; break; }
  }
  if (!truncated && line && lines.length < maxLines) lines.push(line.trimEnd());
  if (truncated) {
    let last = lines.length ? lines[lines.length - 1] : "";
    while (last && ctx.measureText(`${last}…`).width > maxWidth) last = last.slice(0, -1);
    if (lines.length) lines[lines.length - 1] = `${last}…`;
    else lines.push("…");
  }
  return lines.length ? lines : [""];
}

function roundRect(ctx, x, y, w, hgt, r) {
  const rr = Math.min(r, w / 2, hgt / 2);
  ctx.beginPath();
  ctx.moveTo(x + rr, y);
  ctx.arcTo(x + w, y, x + w, y + hgt, rr);
  ctx.arcTo(x + w, y + hgt, x, y + hgt, rr);
  ctx.arcTo(x, y + hgt, x, y, rr);
  ctx.arcTo(x, y, x + w, y, rr);
  ctx.closePath();
}

function withAlpha(color, alpha) {
  const text = String(color || "").trim();
  if (text.startsWith("#")) {
    const hex = text.slice(1);
    const value = hex.length === 3 ? hex.split("").map((c) => c + c).join("") : hex.padEnd(6, "0").slice(0, 6);
    const n = parseInt(value, 16);
    if (Number.isFinite(n)) return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${alpha})`;
  }
  if (text.startsWith("rgb")) return text.replace(/^rgba?\(/, "rgba(").replace(/\)$/, `,${alpha})`);
  return `rgba(148,163,184,${alpha})`;
}
