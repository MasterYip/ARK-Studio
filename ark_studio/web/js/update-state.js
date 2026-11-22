// What the ARK Studio desktop app knows about updates, shared by the local
// Home's notices (desktop-update.js), Settings › About and App and the
// activity bar's About dot (about.js).  Every ARK page in the app window may
// read it (local and remote); only the local page may check, install or
// change the automatic check.  In a browser there is no update state.  No DOM.
import { desktopHub, hubContext } from "./desktop-hub-model.js";
import { shouldRecheck } from "./desktop-update-model.js";

const RECHECK_MS = 15000;
let latest = null;
let watching = null;
const listeners = new Set();

/** `{hub, role}` on an ARK page of the desktop app ("local" or "remote") that reports updates, else null. */
export function appUpdateHub(hub = desktopHub()) {
  if (!hub || typeof hub.update !== "function") return Promise.resolve(null);
  return hubContext(hub).then(
    (context) => (context?.role === "local" || context?.role === "remote" ? { hub, role: context.role } : null),
    () => null);
}

/** The desktop hub when this is the app's local ARK page and it reports updates, else null. */
export function localUpdateHub(hub = desktopHub()) {
  return appUpdateHub(hub).then((found) => (found?.role === "local" ? found.hub : null));
}

/** The last update state seen on this page, or null. */
export function latestUpdate() {
  return latest;
}

/** Record a new update state (hub.update(), a check's reply) and tell every listener. */
export function publishUpdate(info) {
  latest = info && typeof info === "object" ? info : null;
  for (const fn of [...listeners]) {
    try { fn(latest); } catch { /* one listener must not stop the others */ }
  }
  return latest;
}

/** Call `fn(info)` on every new state; returns the unsubscribe function. */
export function onUpdate(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Tell the app window's own notice (desktop shell) to look again, e.g. after "Check for updates". */
export function announceUpdate() {
  try { globalThis.window?.dispatchEvent(new Event("ark:desktop-update")); } catch { /* no window */ }
}

/** Ask the app what its last check found (no network). */
export async function refreshUpdate(hub = null) {
  const target = hub || (await appUpdateHub())?.hub;
  if (!target) return null;
  try { return publishUpdate(await target.update()); } catch { return latest; }
}

/**
 * Once per page: read the state now and, while nothing was found, once more
 * after the app's start-up check (a few seconds after it opens) had time to
 * run; then every 30 minutes (the app checks every 12 hours while running).
 */
export function watchUpdates({ hub = null, wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), every = 30 * 60 * 1000 } = {}) {
  if (!watching) {
    watching = (async () => {
      const target = hub || (await appUpdateHub())?.hub;
      if (!target) return null;
      let info = null;
      for (let attempt = 0; ; attempt++) {
        info = await refreshUpdate(target);
        if (!shouldRecheck(info, attempt)) break;
        await wait(RECHECK_MS);
      }
      if (every > 0 && typeof setInterval === "function") {
        const timer = setInterval(() => { refreshUpdate(target); }, every);
        timer?.unref?.();
      }
      return info;
    })();
  }
  return watching;
}

/** Tests only: forget the state and the running watch. */
export function resetUpdateState() {
  latest = null;
  watching = null;
  listeners.clear();
}
