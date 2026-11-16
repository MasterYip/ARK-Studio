import { t } from "./i18n.js";

// `label` is a getter so menus built later pick up the current language.
const marker = (tag, glyph, color, labelKey) => ({
  tag, glyph, color, labelKey,
  get label() { return t(this.labelKey); },
});

export const MARKERS = [
  marker("marker:star-yellow", "★", "#f59e0b", "markers.star_yellow"),
  marker("marker:star-blue", "★", "#3b82f6", "markers.star_blue"),
  marker("marker:star-red", "★", "#ef4444", "markers.star_red"),
  marker("marker:star-green", "★", "#22c55e", "markers.star_green"),
  marker("marker:flag-red", "⚑", "#ef4444", "markers.flag_red"),
  marker("marker:check-green", "✓", "#22c55e", "markers.check_green"),
  marker("marker:question", "?", "#38bdf8", "markers.question"),
  marker("marker:priority", "!", "#fb923c", "markers.priority"),
];

const BY_TAG = new Map(MARKERS.map((marker) => [marker.tag, marker]));

export function markerForTag(tag) {
  return BY_TAG.get(tag) || null;
}

export function markersFor(node) {
  return (node?.tags || []).map(markerForTag).filter(Boolean);
}

/**
 * Circular-node markers: an upper-right arc, clockwise in ~20° steps. The
 * marker size is derived from the node radius so the angular spacing stays
 * visually meaningful at every zoom level rather than collapsing to one blob.
 */
export function markerArc(markers, cx, cy, radius, start = -Math.PI / 4, step = Math.PI / 9) {
  return (markers || []).map((marker, index) => {
    const angle = start + index * step;
    return {
      marker,
      x: cx + Math.cos(angle) * radius,
      y: cy + Math.sin(angle) * radius,
    };
  });
}
