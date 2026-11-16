// Graph physics shared by the workbench and the Settings tab.
//
// These values intentionally mirror Info Hub's current graph defaults and its
// 0–100 control curves:
//
//   repulsion  = 3286.064      slider 59
//   distance   = 0             slider  0
//   gravity    = 0.0115061     slider 95
//   damping    = 0.30          slider 30
//
// `controlToRaw` / `rawToControl` are the same non-linear mappings Info Hub uses
// (cube root for repulsion, reciprocal cube root for gravity), so a value read
// in ARK means the same thing it means in Info Hub.

export const GRAPH_RANGES = {
  repulsion: [0, 16000],
  distance: [0, 320],
  gravity: [0, 0.02],
  damping: [0, 1],
};

import { GRAPH_DEFAULTS as FORCE_DEFAULTS } from "./graph-core.js";
export const GRAPH_DEFAULTS = FORCE_DEFAULTS;

const clamp = (n, min, max) => Math.max(min, Math.min(max, n));

function transform(key, value) {
  if (key === "repulsion") return Math.cbrt(value);
  if (key === "gravity") {
    const offset = GRAPH_RANGES.gravity[1] / 124;
    return -Math.cbrt(1 / (value + offset));
  }
  return value;
}

function restore(key, value) {
  if (key === "repulsion") return value ** 3;
  if (key === "gravity") {
    const offset = GRAPH_RANGES.gravity[1] / 124;
    return -1 / value ** 3 - offset;
  }
  return value;
}

export function controlToRaw(key, control) {
  if (!GRAPH_RANGES[key]) return GRAPH_DEFAULTS[key];
  const [min, max] = GRAPH_RANGES[key];
  const value = Number(control);
  if (!Number.isFinite(value)) return GRAPH_DEFAULTS[key];
  if (value <= 0) return min;
  if (value >= 100) return max;
  const lo = transform(key, min);
  const hi = transform(key, max);
  return clamp(restore(key, lo + (hi - lo) * value / 100), min, max);
}

export function rawToControl(key, raw) {
  if (!GRAPH_RANGES[key]) return 0;
  const [min, max] = GRAPH_RANGES[key];
  const value = clamp(Number(raw), min, max);
  const lo = transform(key, min);
  const hi = transform(key, max);
  return 100 * (transform(key, value) - lo) / (hi - lo);
}

export function normalizeGraphPhysics(value) {
  const source = value && typeof value === "object" ? value : {};
  const out = {};
  for (const key of Object.keys(GRAPH_RANGES)) {
    const raw = Number(source[key]);
    const fallback = GRAPH_DEFAULTS[key];
    const [min, max] = GRAPH_RANGES[key];
    out[key] = Number.isFinite(raw) ? clamp(raw, min, max) : fallback;
  }
  return out;
}

export function formatPhysicsValue(key, value) {
  if (key === "repulsion") return Math.round(value).toLocaleString();
  if (key === "distance") return Math.round(value).toLocaleString();
  if (key === "gravity") return Number(value).toFixed(5);
  return Number(value).toFixed(2);
}
