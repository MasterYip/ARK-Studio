// Read / Edit for every graph layout and the Node details panel: one choice
// per person in this browser (not per node, not per layout).
//
// Read is the default.  Only the Read / Edit control (toolbar buttons, the
// panel's Edit / Read button, the R / E keys, the graph menu) changes the
// remembered choice.  Edit actions that switch to Edit on their own (paste,
// context-menu edits) keep Edit for the current visit only, so the next start
// is in Read again.
//
// "ark.graphMode" held the mode until 2026-11-22; implicit switches wrote it
// too, so a stored "edit" may not be the person's choice.  It is dropped and
// everyone starts in Read once.
export const MODE_KEY = "ark.readEditMode";
export const LEGACY_MODE_KEY = "ark.graphMode";

function browserStorage() {
  try { return globalThis.localStorage || null; } catch { return null; }
}

export function normalizeMode(mode) {
  return mode === "edit" ? "edit" : "read";
}

/** The remembered choice: "edit" only when the person picked Edit. */
export function savedMode(storage = browserStorage()) {
  try {
    storage?.removeItem?.(LEGACY_MODE_KEY);
    return normalizeMode(storage?.getItem?.(MODE_KEY));
  } catch { return "read"; }
}

/** Remember an explicit Read / Edit choice. */
export function rememberMode(mode, storage = browserStorage()) {
  try { storage?.setItem?.(MODE_KEY, normalizeMode(mode)); } catch { /* storage disabled */ }
}

/**
 * Whether Node details opens read-only.  `readOnly` is a caller's explicit
 * request (Needs You review opens in Read); otherwise the mode of this visit
 * (`sessionMode`), then the graph showing the node, then the saved choice.
 */
export function inspectorReadOnly({ readOnly, sessionMode, graphMode, saved } = {}) {
  if (typeof readOnly === "boolean") return readOnly;
  return (sessionMode || graphMode || normalizeMode(saved)) !== "edit";
}
