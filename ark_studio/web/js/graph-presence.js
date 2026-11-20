// Agent presence overlay for a GraphView (Force, Tree, Mind, Node and the
// Local graph; T-0043).  A second canvas over the graph's own canvas draws a
// coloured badge per agent next to the node it is on, a pulsing ring while
// it writes (a dimmer one while it reads), lets the badge glide when the
// agent moves to another node and fades it out when it leaves.
//
// Presence is overlay-only state: this module reads the view's nodes and
// camera and never writes them, never calls setData, _draw, fit or a layout,
// so a presence update cannot relayout, refit or reheat a graph.  Only the
// overlay canvas is redrawn, and only while an agent is shown.
// tests/js/presence_overlay.test.mjs checks both.
import { presenceAlpha, presenceByUnit, presenceGlide } from "./presence-model.js";

const BADGE_R = 8.5;
const BADGE_STEP = 15;
const FRAME_MS = 33; // ~30 fps is plenty for a pulse
const MAX_BADGES = 4; // per node; more show as "+N"

export function presenceNodeInView(view, unit) {
  const find = (id) => id == null ? null : view.byId?.get(id) ?? view.byId?.get(Number(id)) ?? view.byId?.get(String(id)) ?? null;
  let node = find(unit);
  const hidden = view.hiddenIds;
  if (!node || !hidden?.has(node.id)) return node;
  const parentOf = view.layout === "mind" ? view.mindParentOf : null;
  const seen = new Set();
  while (node && hidden.has(node.id) && !seen.has(node.id)) {
    seen.add(node.id);
    node = find(parentOf?.get(node.id));
  }
  return node && !hidden.has(node.id) ? node : null;
}

function rounded(ctx, x, y, w, hgt, r) {
  const rr = Math.max(0, Math.min(r, w / 2, hgt / 2));
  ctx.beginPath();
  ctx.moveTo(x + rr, y);
  ctx.arcTo(x + w, y, x + w, y + hgt, rr);
  ctx.arcTo(x + w, y + hgt, x, y + hgt, rr);
  ctx.arcTo(x, y + hgt, x, y, rr);
  ctx.arcTo(x, y, x + w, y, rr);
  ctx.closePath();
}

function hexAlpha(color, alpha) {
  const m = /^#([0-9a-f]{6})$/i.exec(color || "");
  if (!m) return color;
  const n = parseInt(m[1], 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${Math.max(0, Math.min(1, alpha)).toFixed(3)})`;
}

/**
 * @param {object} view  a GraphView (reads host, canvas, width, height, layout,
 *   nodes/byId, hiddenIds, _transform(), _nodeRadius(), _dpr())
 * @param {{ model: () => object, subscribe: (fn) => () => void, enabled: () => boolean }} store
 * @param {{ document?: Document, raf?: Function, caf?: Function, now?: () => number, reducedMotion?: boolean }} env
 */
export function createPresenceOverlay(view, store, env = {}) {
  const doc = env.document || globalThis.document;
  const raf = env.raf || ((fn) => globalThis.requestAnimationFrame(fn));
  const caf = env.caf || ((id) => globalThis.cancelAnimationFrame(id));
  const clock = env.now || (() => globalThis.performance?.now?.() ?? Date.now());
  const wall = env.wall || (() => Date.now());
  const reduced = env.reducedMotion ?? Boolean(globalThis.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches);
  const canvas = doc.createElement("canvas");
  canvas.className = "graph-presence-layer";
  canvas.setAttribute("aria-hidden", "true");
  Object.assign(canvas.style, { display: "none", position: "absolute", left: "0px", top: "0px", pointerEvents: "none", zIndex: "1" });
  if (view.canvas?.after) view.canvas.after(canvas);
  else view.host?.appendChild?.(canvas);
  const ctx = canvas.getContext("2d");
  let frame = 0;
  let lastDraw = -Infinity;
  let painted = false;
  let destroyed = false;
  let size = { w: 0, h: 0, dpr: 0, left: null, top: null };
  const stats = { frames: 0, badges: 0 };

  const sync = () => {
    const dpr = typeof view._dpr === "function" ? view._dpr() : 1;
    const w = Math.max(1, view.width || 1), hgt = Math.max(1, view.height || 1);
    if (size.w !== w || size.h !== hgt || size.dpr !== dpr) {
      canvas.width = view.canvas?.width ?? Math.round(w * dpr);
      canvas.height = view.canvas?.height ?? Math.round(hgt * dpr);
      canvas.style.width = `${w}px`;
      canvas.style.height = `${hgt}px`;
      size = { ...size, w, h: hgt, dpr };
    }
    if (size.left === null || size.resizes !== view.resizeCount) {
      const left = view.canvas?.offsetLeft ?? 0, top = view.canvas?.offsetTop ?? 0;
      if (left !== size.left || top !== size.top) {
        canvas.style.left = `${left}px`;
        canvas.style.top = `${top}px`;
      }
      size = { ...size, left, top, resizes: view.resizeCount };
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  };

  // Where a node is on screen: a circle (Force, Tree) or a card (Mind, Node).
  const geometry = (node) => {
    if (!node || !Number.isFinite(node.x) || !Number.isFinite(node.y)) return null;
    if (view.hiddenIds?.has?.(node.id)) return null;
    const tr = view._transform();
    const sx = node.x * tr.z + tr.x, sy = node.y * tr.z + tr.y;
    if (view.layout === "mind" || view.layout === "node") {
      const w = (node.w || 180) * tr.z, hgt = (node.h || 40) * tr.z;
      return { card: true, x: sx, y: sy, left: sx - w / 2, top: sy - hgt / 2, w, h: hgt };
    }
    const r = typeof view._nodeRadius === "function" ? view._nodeRadius(node) : 8;
    return { card: false, x: sx, y: sy, r };
  };

  // The i-th badge slot of a node.
  const slot = (geo, i) => {
    if (geo.card) return { x: geo.left + geo.w - BADGE_R - 2 - i * BADGE_STEP, y: geo.top - BADGE_R + 1 };
    const d = geo.r * 0.72 + BADGE_R * 0.9;
    return { x: geo.x + d + i * BADGE_STEP, y: geo.y - d };
  };

  const visible = (geo) => {
    const pad = 60;
    const x0 = geo.card ? geo.left : geo.x - geo.r, x1 = geo.card ? geo.left + geo.w : geo.x + geo.r;
    const y0 = geo.card ? geo.top : geo.y - geo.r, y1 = geo.card ? geo.top + geo.h : geo.y + geo.r;
    return x1 > -pad && y1 > -pad && x0 < size.w + pad && y0 < size.h + pad;
  };

  const ring = (geo, entry, index, now, alpha) => {
    const writing = entry.state === "writing", working = entry.state === "working";
    const period = writing ? 1100 : working ? 1700 : 2000;
    const phase = reduced ? 0.35 : ((now + index * 260) % period) / period;
    const base = writing ? 0.62 : working ? 0.5 : 0.28;
    const grow = 3 + index * 3 + phase * (writing ? 9 : 6);
    ctx.lineWidth = writing || working ? 2 : 1.3;
    // A steady ring, then the pulse spreading out from it.
    ctx.strokeStyle = hexAlpha(entry.color, (writing || working ? 0.75 : 0.4) * alpha);
    if (geo.card) rounded(ctx, geo.left - 3 - index * 3, geo.top - 3 - index * 3, geo.w + 6 + index * 6, geo.h + 6 + index * 6, 8);
    else { ctx.beginPath(); ctx.arc(geo.x, geo.y, geo.r + 3 + index * 3, 0, Math.PI * 2); }
    ctx.stroke();
    ctx.strokeStyle = hexAlpha(entry.color, base * (1 - phase) * alpha);
    if (geo.card) rounded(ctx, geo.left - grow, geo.top - grow, geo.w + grow * 2, geo.h + grow * 2, 8 + grow / 2);
    else { ctx.beginPath(); ctx.arc(geo.x, geo.y, geo.r + grow, 0, Math.PI * 2); }
    ctx.stroke();
  };

  const badge = (pos, entry, alpha) => {
    ctx.globalAlpha = alpha;
    ctx.beginPath();
    ctx.arc(pos.x, pos.y, BADGE_R, 0, Math.PI * 2);
    ctx.fillStyle = entry.color;
    ctx.fill();
    ctx.lineWidth = 1.6;
    ctx.strokeStyle = "#fff";
    ctx.stroke();
    if (entry.state === "reading") {
      // Reading: a hollow centre ring, so it reads as "looking".
      ctx.beginPath();
      ctx.arc(pos.x, pos.y, BADGE_R - 2.6, 0, Math.PI * 2);
      ctx.lineWidth = 1;
      ctx.strokeStyle = "rgba(255,255,255,.55)";
      ctx.stroke();
    }
    ctx.fillStyle = "#fff";
    ctx.font = "700 8px system-ui, -apple-system, 'Segoe UI', sans-serif";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(entry.initials, pos.x, pos.y + 0.5);
    ctx.globalAlpha = 1;
  };

  // The name of the node's first agent ("+N" for the others), right of the stack.
  const pill = (pos, entry, others, alpha) => {
    ctx.globalAlpha = alpha;
    ctx.font = "600 10px system-ui, -apple-system, 'Segoe UI', sans-serif";
    ctx.textBaseline = "middle";
    const name = entry.label.length > 18 ? `${entry.label.slice(0, 17)}…` : entry.label;
    const text = others > 0 ? `${name} +${others}` : name;
    const tw = ctx.measureText(text).width;
    const x = pos.x + BADGE_R + 3, y = pos.y - 7;
    rounded(ctx, x, y, tw + 10, 14, 7);
    ctx.fillStyle = entry.color;
    ctx.fill();
    ctx.fillStyle = "#fff";
    ctx.textAlign = "left";
    ctx.fillText(text, x + 5, y + 7.5);
    ctx.globalAlpha = 1;
  };

  const more = (pos, count, alpha) => {
    ctx.globalAlpha = alpha;
    ctx.beginPath();
    ctx.arc(pos.x, pos.y, BADGE_R, 0, Math.PI * 2);
    ctx.fillStyle = "rgba(60,64,72,.9)";
    ctx.fill();
    ctx.fillStyle = "#fff";
    ctx.font = "700 8px system-ui, -apple-system, 'Segoe UI', sans-serif";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(`+${count}`, pos.x, pos.y + 0.5);
    ctx.globalAlpha = 1;
  };

  const draw = (now, wallNow) => {
    stats.badges = 0;
    const model = store.model();
    // Units collapsed into the same shown node share its badge stack.
    const groups = new Map();
    for (const [unit, list] of store.enabled() ? presenceByUnit(model) : []) {
      const node = presenceNodeInView(view, unit);
      if (!node) continue;
      if (!groups.has(node.id)) groups.set(node.id, { node, actors: new Map() });
      const { actors } = groups.get(node.id);
      for (const entry of list) {
        const prior = actors.get(entry.actor);
        if (!prior || (prior.leaving && !entry.leaving) || (prior.leaving === entry.leaving && entry.at > prior.at)) {
          actors.set(entry.actor, entry);
        }
      }
    }
    if (!groups.size) {
      if (painted) ctx.clearRect(0, 0, size.w, size.h);
      if (canvas.style.display !== "none") canvas.style.display = "none";
      painted = false;
      return false;
    }
    if (canvas.style.display === "none") {
      canvas.style.display = "block";
      size.left = null;
    }
    sync();
    if (painted) ctx.clearRect(0, 0, size.w, size.h);
    const labelled = new Set();
    for (const { node, actors } of groups.values()) {
      const list = [...actors.values()].sort((left, right) => Number(Boolean(left.leaving)) - Number(Boolean(right.leaving)) || right.at - left.at);
      const geo = geometry(node);
      if (!geo) continue;
      const shown = list.slice(0, MAX_BADGES);
      // Rings under the badges, one per agent (at most 3).
      shown.slice(0, 3).forEach((entry, i) => ring(geo, entry, i, now, presenceAlpha(entry, wallNow)));
      let pillAt = null;
      shown.forEach((entry, i) => {
        let pos = slot(geo, i);
        const g = reduced ? 1 : presenceGlide(entry, wallNow);
        if (g < 1) {
          const fromNode = presenceNodeInView(view, entry.prevUnit);
          const fromGeo = fromNode?.id === node.id ? null : geometry(fromNode);
          if (fromGeo) {
            const from = slot(fromGeo, 0);
            // A faint trail along the way.
            ctx.strokeStyle = hexAlpha(entry.color, 0.35 * (1 - g));
            ctx.lineWidth = 2;
            ctx.setLineDash?.([3, 4]);
            ctx.beginPath();
            ctx.moveTo(from.x, from.y);
            ctx.lineTo(from.x + (pos.x - from.x) * g, from.y + (pos.y - from.y) * g);
            ctx.stroke();
            ctx.setLineDash?.([]);
            pos = { x: from.x + (pos.x - from.x) * g, y: from.y + (pos.y - from.y) * g };
          }
        }
        if (!visible(geo) && g >= 1) return;
        badge(pos, entry, presenceAlpha(entry, wallNow));
        stats.badges += 1;
        // Cards stack leftwards from the corner, circles rightwards.
        if (i === 0 || !geo.card) pillAt = pos;
      });
      if (list.length > MAX_BADGES) {
        pillAt = geo.card ? pillAt : slot(geo, MAX_BADGES);
        more(slot(geo, MAX_BADGES), list.length - MAX_BADGES, 1);
      }
      const lead = list.find((entry) => !entry.leaving && !labelled.has(entry.actor));
      if (lead && pillAt) {
        labelled.add(lead.actor);
        const others = list.filter((entry) => !entry.leaving && entry.actor !== lead.actor).length;
        pill(pillAt, lead, others, presenceAlpha(lead, wallNow));
      }
    }
    painted = true;
    stats.frames += 1;
    return true;
  };

  const tick = () => {
    frame = 0;
    if (destroyed) return;
    if (globalThis.document?.hidden) return; // resumes on the next change
    const now = clock(), wallNow = wall();
    const model = store.model();
    const live = store.enabled() && model.entries.size > 0;
    if (now - lastDraw >= FRAME_MS || !live) {
      lastDraw = now;
      draw(now, wallNow);
    }
    // Keep going while an agent is shown (the pulse, a moving camera) or a
    // badge still fades out.
    if (live) frame = raf(tick);
  };

  // Badges follow the graph in the same frame: when the view redraws (a
  // drag, pan, zoom or layout step) the overlay redraws right after it,
  // instead of on its own timer a frame or two later.
  const viewDraw = typeof view._draw === "function" ? view._draw : null;
  if (viewDraw) {
    view._draw = function drawWithPresence(...args) {
      const out = viewDraw.apply(this, args);
      // Offscreen scene tiles (_renderSceneTile) also go through _draw.
      if (!destroyed && !this._tileRender && (painted || (store.enabled() && store.model().entries.size > 0))) {
        const now = clock();
        lastDraw = now;
        draw(now, wall());
      }
      return out;
    };
  }

  const kick = () => {
    if (!destroyed && !frame) frame = raf(tick);
  };
  const unsubscribe = store.subscribe(kick);
  const onVisible = () => { if (!globalThis.document?.hidden) kick(); };
  globalThis.document?.addEventListener?.("visibilitychange", onVisible);
  kick();

  return {
    canvas,
    stats,
    kick,
    realign() { size.left = null; },
    /** Draw one frame now (tests, the camera following a badge). */
    drawNow(now = clock(), wallNow = wall()) { return draw(now, wallNow); },
    destroy() {
      destroyed = true;
      if (viewDraw && view._draw !== viewDraw) view._draw = viewDraw;
      if (frame) caf(frame);
      frame = 0;
      unsubscribe();
      globalThis.document?.removeEventListener?.("visibilitychange", onVisible);
      canvas.remove?.();
    },
  };
}

/**
 * Centre a view on a unit without changing its zoom, layout or positions.
 * Returns the node, or null when the view does not show it.
 */
export function centreOnUnit(view, unit) {
  const node = presenceNodeInView(view, unit);
  if (!node || !Number.isFinite(node.x) || !Number.isFinite(node.y)) return null;
  const pan = { x: -node.x * view.zoom, y: -node.y * view.zoom };
  if (typeof view._animateCamera === "function") view._animateCamera(view.zoom, pan);
  else { view.pan = pan; view._draw?.(); }
  return node;
}
