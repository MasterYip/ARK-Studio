// Pure maths for the shared zoom/pan image viewer (web/js/image-viewer.js).
// No DOM access, so `node --test tests/js/image_viewer_model.test.mjs` covers it.
//
// A view is { scale, x, y }: the image's top-left corner sits at (x, y) in
// stage pixels and every image pixel is drawn `scale` stage pixels wide, i.e.
// CSS `transform: translate(x, y) scale(scale)` with transform-origin 0 0.

export const MIN_SCALE = 0.1;
export const MAX_SCALE = 20;
export const STEP = 1.25;
// Wheel deltas are turned into a zoom factor exp(-delta * WHEEL_RATE), so a
// classic 100px mouse-wheel notch zooms ~14% and trackpads zoom continuously.
export const WHEEL_RATE = 0.0015;
// Per-event clamp so one fast flick cannot jump straight to the limits.
const MAX_WHEEL_FACTOR = 2;

const finite = (value, fallback = 0) => (Number.isFinite(value) ? value : fallback);

export function clampScale(scale, min = MIN_SCALE, max = MAX_SCALE) {
  return Math.min(max, Math.max(min, finite(scale, 1)));
}

/** Scale that fits the whole image in the stage; never upscales past 1:1. */
export function fitScale(imageW, imageH, stageW, stageH, { padding = 0, upscale = false } = {}) {
  const w = finite(imageW), hgt = finite(imageH);
  const sw = Math.max(1, finite(stageW) - 2 * padding), sh = Math.max(1, finite(stageH) - 2 * padding);
  if (w <= 0 || hgt <= 0) return 1;
  const fit = Math.min(sw / w, sh / hgt);
  return clampScale(upscale ? fit : Math.min(1, fit));
}

/** View with the image centred in the stage at `scale`. */
export function centeredView(imageW, imageH, stageW, stageH, scale) {
  const s = clampScale(scale);
  return { scale: s, x: (stageW - imageW * s) / 2, y: (stageH - imageH * s) / 2 };
}

export function fitView(imageW, imageH, stageW, stageH, options) {
  return centeredView(imageW, imageH, stageW, stageH, fitScale(imageW, imageH, stageW, stageH, options));
}

/**
 * Zoom to `scale` keeping the image point under stage point (px, py) fixed.
 * That is what makes wheel zoom follow the cursor.
 */
export function zoomAt(view, scale, px, py) {
  const next = clampScale(scale);
  const ratio = next / view.scale;
  return { scale: next, x: px - (px - view.x) * ratio, y: py - (py - view.y) * ratio };
}

/** Zoom factor for one wheel event (deltaMode 0 = px, 1 = lines, 2 = pages). */
export function wheelFactor(deltaY, deltaMode = 0, pageHeight = 800) {
  const unit = deltaMode === 1 ? 16 : deltaMode === 2 ? pageHeight : 1;
  const factor = Math.exp(-finite(deltaY) * unit * WHEEL_RATE);
  return Math.min(MAX_WHEEL_FACTOR, Math.max(1 / MAX_WHEEL_FACTOR, factor));
}

export function stepScale(scale, direction) {
  return clampScale(direction > 0 ? scale * STEP : scale / STEP);
}

export function panBy(view, dx, dy) {
  return { scale: view.scale, x: view.x + finite(dx), y: view.y + finite(dy) };
}

/**
 * Keep at least `keep` stage pixels of the image inside the stage (or the
 * whole image when it is smaller than that), so it can never be lost off-screen.
 */
export function clampPan(view, imageW, imageH, stageW, stageH, keep = 48) {
  const w = imageW * view.scale, hgt = imageH * view.scale;
  const kx = Math.min(keep, w), ky = Math.min(keep, hgt);
  const x = Math.min(stageW - kx, Math.max(kx - w, view.x));
  const y = Math.min(stageH - ky, Math.max(ky - hgt, view.y));
  return { scale: view.scale, x, y };
}

/**
 * Two-finger pinch: scale by the change in finger distance around the start
 * midpoint, then follow the midpoint as it moves (pan while pinching).
 */
export function pinchView(start, startDist, dist, startMid, mid) {
  const zoomed = zoomAt(start, start.scale * (finite(dist, 1) / Math.max(1, finite(startDist, 1))), startMid.x, startMid.y);
  return panBy(zoomed, mid.x - startMid.x, mid.y - startMid.y);
}

export function distance(a, b) {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

export function midpoint(a, b) {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

export function formatZoom(scale) {
  const pct = finite(scale, 1) * 100;
  return `${pct < 10 ? pct.toFixed(1).replace(/\.0$/, "") : Math.round(pct)}%`;
}

export function sameView(a, b, epsilon = 0.01) {
  return Boolean(a && b) && Math.abs(a.scale - b.scale) < epsilon / 100
    && Math.abs(a.x - b.x) < epsilon && Math.abs(a.y - b.y) < epsilon;
}
