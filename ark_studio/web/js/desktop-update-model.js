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
  // "Later" in the app window's notice (stored by the app for every page).
  if (typeof info.dismissed === "string" && info.dismissed.replace(/^v/, "") === version) return null;
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

/** Whether the app found a newer version than the running one (the activity bar's About dot). */
export function hasUpdate(info) {
  return !!updateBanner(info && typeof info === "object" ? { ...info, dismissed: "" } : info);
}

const cleanVersion = (value) => (typeof value === "string" ? value.trim().replace(/^v/, "") : "");

/**
 * Settings › About › Version and updates, as data.
 * @param {object|null} info hub.update() on the desktop app's local page, else null
 * @param {{serverVersion?: string}} page the version the ARK server reports in its page
 */
export function aboutState(info, { serverVersion = "" } = {}) {
  if (!info || typeof info !== "object") {
    return { kind: "server", version: cleanVersion(serverVersion), enabled: false, available: "",
      action: "", portable: false, autoCheck: false, lastCheck: 0 };
  }
  // "Later" only hides the notice: About still names the version.
  const banner = updateBanner({ ...info, dismissed: "" });
  const lastCheck = Number(info.lastCheck);
  return {
    kind: "desktop",
    version: cleanVersion(info.current) || cleanVersion(serverVersion),
    enabled: info.enabled !== false,
    available: banner ? banner.version : "",
    action: banner ? banner.action : "",
    portable: info.selfInstall === false,
    autoCheck: !!info.autoCheck,
    // The app reports seconds since the epoch; 0 when it never checked.
    lastCheck: Number.isFinite(lastCheck) && lastCheck > 0 ? lastCheck * 1000 : 0,
  };
}

/**
 * The update status line: [i18n key, params], or null when there is nothing
 * to say.  `checked`: the person just ran "Check for updates" here.
 */
export function aboutStatus(state, { checked = false } = {}) {
  if (!state || state.kind !== "desktop") return null;
  if (!state.enabled) return ["about.update.no_updater", {}];
  if (state.available) {
    return [state.portable ? "about.update.available_portable" : "about.update.available", { version: state.available }];
  }
  return checked ? ["about.update.up_to_date", {}] : null;
}

// ------------------------------------------------ app and server notices

/** Whether this page runs in the ARK Studio desktop app (Settings › App, the "settings of this app" pointers). */
export function inDesktopApp(desktop = globalThis.window?.__ARK_DESKTOP__) {
  return !!(desktop && typeof desktop === "object" && desktop.hub && typeof desktop.hub.context === "function");
}

/** The app window shows the "available" notice itself (desktop shell), so pages skip their banner. */
export function shellShowsUpdates(desktop = globalThis.window?.__ARK_DESKTOP__) {
  return !!(desktop && typeof desktop === "object" && desktop.updateNotice === true);
}

/**
 * "ARK Studio was updated to 2.x.y" after the app's first start of a new
 * version: {version, previous, changes, url}, or null.
 */
export function updatedNotice(info) {
  const notice = info && typeof info === "object" ? info.notice : null;
  if (!notice || typeof notice !== "object") return null;
  const version = cleanVersion(notice.version);
  if (!version) return null;
  const changes = (Array.isArray(notice.changes) ? notice.changes : [])
    .filter((item) => typeof item === "string" && item.trim()).map((item) => item.trim()).slice(0, 6);
  const url = typeof notice.url === "string" && /^https:\/\//.test(notice.url) ? notice.url : releaseUrl(version);
  return { version, previous: cleanVersion(notice.previous), changes, url };
}

/** The GitHub release page of a version. */
export function releaseUrl(version) {
  const v = cleanVersion(version);
  return v ? `https://github.com/MasterYip/ARK-Studio/releases/tag/v${encodeURIComponent(v)}`
    : "https://github.com/MasterYip/ARK-Studio/releases/latest";
}

/**
 * "This server was updated to ...", once per person and version.
 * @returns {"show"|"store"|""} show the notice; only record the version
 *   (first visit: nothing to compare with); or nothing to do
 */
export function serverNotice(current, seen) {
  const now = cleanVersion(current);
  if (!now) return "";
  const before = cleanVersion(seen);
  if (!before) return "store";
  return before === now ? "" : "show";
}
