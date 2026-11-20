// The effective graph display settings, in the camelCase shape graph.js and
// the Local graph read.  The values come from Settings › Appearance › Graphs
// (project default with this person's override on top, graph-display.js);
// app.js calls setActiveGraphDisplay whenever either layer changes.
import { GRAPH_DISPLAY_DEFAULTS, toMindSettings } from "./graph-display.js";

export const MIND_DEFAULTS = Object.freeze(toMindSettings(GRAPH_DISPLAY_DEFAULTS));

let active = MIND_DEFAULTS;

/** The current settings (one frozen object per change, cheap to compare). */
export function getMindSettings() {
  return active;
}

/** Adopt resolved {tab: {key: value}} values (graph-display.js resolveGraphDisplay). */
export function setActiveGraphDisplay(values) {
  active = Object.freeze(toMindSettings(values));
  return active;
}
