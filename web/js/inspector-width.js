// Docked inspector width: one model shared by the selected-unit pane and the
// "No node selected" placeholder so both resize the same --inspector-w.
export const INSPECTOR_MIN_W = 320;
export const INSPECTOR_MAX_W = 680;
export const INSPECTOR_DEFAULT_W = 430;
export const INSPECTOR_WIDTH_KEY = "ark.inspectorWidth";

const limit = (n) => Math.round(Math.max(INSPECTOR_MIN_W, Math.min(INSPECTOR_MAX_W, n)));

/** Stored/state width -> usable width; missing or bogus values use the default. */
export function clampInspectorWidth(value) {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? limit(n) : INSPECTOR_DEFAULT_W;
}

/** The pane is anchored to the right edge: dragging left widens it. */
export function dragInspectorWidth(startWidth, startX, clientX) {
  return limit(clampInspectorWidth(startWidth) + startX - clientX);
}
