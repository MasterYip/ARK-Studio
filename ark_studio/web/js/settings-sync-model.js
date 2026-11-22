// When a change from elsewhere repaints Settings (app.js refreshFromRemote).
// Settings is one large form: rebuilding it resets drafts, focus, folds and
// scroll, and re-runs every panel's reads.  It repaints only when the data it
// was built from changed (project settings, name, the person's access, the
// palette presets), never for unit or activity changes, and never while a
// panel has unsaved edits.  Pure, so Node tests cover it.

/** Key of the app state the Settings view is built from. */
export function settingsViewKey(S = {}) {
  const project = S.project || {};
  const session = S.session || {};
  return JSON.stringify({
    project: [project.id || "", project.name || "", project.path || "", Boolean(project.demo), project.settings || null],
    session: [session.user || "", session.role || "", Boolean(session.is_admin), Boolean(session.local),
      session.project_permission || ""],
    infoHubPath: S.infoHubPath || "",
    theme: S.theme || "",
    palettePresets: S.palettePresets || null,
  });
}

/**
 * Whether a remote refresh should rebuild the mounted Settings view.
 * ``manual``: the person pressed Sync now (repaint unless something is unsaved).
 */
export function settingsNeedsRepaint({ key, shownKey, dirty = false, manual = false }) {
  if (dirty) return false;
  if (manual) return true;
  return key !== shownKey;
}
