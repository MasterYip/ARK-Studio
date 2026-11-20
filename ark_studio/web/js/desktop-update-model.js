// The local Home's update banner (no DOM): when the ARK Studio desktop app
// found a newer version, Home offers it until the person installs it or
// dismisses that version.  Rendering is in desktop-update.js; the app side
// is hub.update() / installUpdate() / openReleases() (desktop/src-tauri).

export const DISMISS_KEY = "ark.desktopUpdate.dismissed";

/**
 * The banner to show, or null.
 * @param {object|null} info hub.update(): {current, available, enabled, selfInstall}
 * @param {string|null} dismissed the version the person chose "Later" for
 * @returns {{version: string, current: string, action: "install"|"download"}|null}
 */
export function updateBanner(info, dismissed = null) {
  if (!info || typeof info !== "object" || info.enabled === false) return null;
  const version = typeof info.available === "string" ? info.available.trim().replace(/^v/, "") : "";
  if (!version) return null;
  const current = typeof info.current === "string" ? info.current.trim().replace(/^v/, "") : "";
  if (current && current === version) return null;
  if (dismissed && String(dismissed).replace(/^v/, "") === version) return null;
  return { version, current, action: info.selfInstall === false ? "download" : "install" };
}

/** Whether to ask again later: the start-up check runs a few seconds after the app opens. */
export function shouldRecheck(info, attempt) {
  return !!info && info.enabled !== false && !info.available && attempt < 1;
}

export function loadDismissed(storage = globalThis.localStorage) {
  try { return storage?.getItem(DISMISS_KEY) || null; } catch { return null; }
}

export function saveDismissed(version, storage = globalThis.localStorage) {
  try { storage?.setItem(DISMISS_KEY, String(version)); } catch { /* storage unavailable */ }
}
