// "Show advanced features" (Settings › Personal › Display, kept in this
// browser).  While it is off, GPU, Resources, Distribution, the design-system
// panels, Agent fleet / Hosts and the InfoHub entries appear only when the
// project already uses them or you are on that page.  Everything stays
// reachable from the command palette.
//
// Default: a browser that used ARK before this switch existed starts with it
// ON (nothing its user relied on disappears); only a brand-new browser starts
// OFF so a new user's views stay small.  The decision is stored once, on the
// first read, so later toggles stick and the browser's own new keys (written
// after that) never flip it.

const KEY = "ark.advanced";
export const ADVANCED_CHANGED = "ark:advanced-changed";

/**
 * Keys that only a browser which already used ARK has: the workbench layout
 * and presets, the local graph, the client project cache, recent nodes and
 * filters.  Not the keys i18n.js / the login page write on any first visit
 * (ark.lang, ark.theme, ark.prefsV2, ark.loginTheme, ...).
 */
export const USED_BEFORE_KEYS = [
  "ark.workbench.v1", "ark.workbench.presets.v1", "ark.workbench.layoutAt",
  "ark.localGraph.v1", "ark.localGraph.v2",
  "ark.cache.lastProject", "ark.cache.identity",
  "ark.recentUnits", "ark.filters.v1", "ark.onboarding.hidden",
];

/**
 * The switch's state in `storage`; when nothing is stored yet, decide from
 * USED_BEFORE_KEYS and store the decision.
 */
export function resolveAdvanced(storage) {
  const stored = storage.getItem(KEY);
  if (stored === "1" || stored === "0") return stored === "1";
  const usedBefore = USED_BEFORE_KEYS.some((key) => storage.getItem(key) !== null);
  try { storage.setItem(KEY, usedBefore ? "1" : "0"); } catch { /* storage full or read-only */ }
  return usedBefore;
}

// Decide as early as possible, before this page load writes any of the keys.
try { resolveAdvanced(localStorage); } catch { /* storage unavailable */ }

export function advancedEnabled() {
  try { return resolveAdvanced(localStorage); } catch { return false; }
}

export function setAdvancedEnabled(on) {
  try { localStorage.setItem(KEY, on ? "1" : "0"); } catch { /* storage unavailable */ }
  window.dispatchEvent(new CustomEvent(ADVANCED_CHANGED, { detail: { on: Boolean(on) } }));
}
