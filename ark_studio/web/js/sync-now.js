// "Sync now" button in the top bar (state and tooltip in sync-now-model.js).
// Clicking it, Alt+R, or the status bar's sync item reads the project from
// the server again (app.js syncNow) and repaints the page in place.  The
// button spins while that runs, shows a check for a moment when done, and
// carries a dot when updates are not arriving live (polling, reconnecting,
// offline).  The top bar is rebuilt often: every button carries
// .sync-top-btn and is repainted from the state kept here.
import { h, fmtDateTime } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { syncState } from "./api.js";
import { liveSync } from "./live-sync.js";
import { toast } from "./components.js";
import { formatShortcut, getShortcut, SHORTCUTS_CHANGED } from "./shortcuts.js";
import { lastSyncedAt } from "./sync-status.js";
import { SYNC_DONE_MS, connectionState, createSyncGate, syncButtonView } from "./sync-now-model.js";

let phase = "idle";
let doneTimer = 0;
let runSync = async () => {};
const gate = createSyncGate(() => runSync());

function view() {
  const last = lastSyncedAt();
  return syncButtonView({
    phase,
    connection: connectionState({ ...syncState, polling: liveSync.polling }),
    shortcut: formatShortcut(getShortcut("sync")),
    lastSyncedAt: last ? fmtDateTime(new Date(last).toISOString()) : "",
  });
}

function paint(button, v = view()) {
  // ark:syncing fires for every request: repaint only on a visible change,
  // so the spinning icon is not restarted.
  const key = JSON.stringify(v);
  if (button.dataset.syncView === key) return;
  button.dataset.syncView = key;
  button.disabled = v.disabled;
  button.classList.toggle("syncing", v.spinning);
  button.classList.toggle("done", v.icon === "check");
  if (v.busy) button.setAttribute("aria-busy", "true");
  else button.removeAttribute("aria-busy");
  button.title = v.lines.map(([key, params]) => t(key, params)).join("\n");
  button.dataset.connection = v.dot || "live";
  const glyph = icon(v.icon, "sm");
  const dot = v.dot ? h("span", { class: `sync-top-dot ${v.dot}`, "aria-hidden": "true" }) : null;
  button.replaceChildren(...[glyph, dot].filter(Boolean));
}

export function paintSyncButtons() {
  if (typeof document === "undefined") return;
  const v = view();
  for (const button of document.querySelectorAll(".sync-top-btn")) paint(button, v);
}

/** A new top-bar button showing the current state. */
export function syncTopButton() {
  const button = h("button", {
    class: "icon-btn sync-top-btn", type: "button", "aria-label": t("sync.now"),
    onclick: () => requestSync(),
  });
  paint(button);
  return button;
}

/**
 * Start a sync unless one is running or just ended.  Returns the running
 * sync's promise (resolves true when it worked) or null when ignored.
 */
export function requestSync() {
  const already = gate.busy;
  const run = gate.trigger();
  if (!run || already) return run;
  clearTimeout(doneTimer);
  phase = "syncing";
  paintSyncButtons();
  return run.then(() => {
    phase = "done";
    paintSyncButtons();
    doneTimer = setTimeout(() => { phase = "idle"; paintSyncButtons(); }, SYNC_DONE_MS);
    return true;
  }, (err) => {
    phase = "idle";
    paintSyncButtons();
    toast(t("sync.failed", { error: err?.message || String(err) }), "err");
    return false;
  });
}

/** `sync`: async () => void, throws with a readable message on failure. */
export function installSyncNow(sync) {
  runSync = sync;
  for (const name of ["ark:live", "ark:connectivity", "ark:syncing", SHORTCUTS_CHANGED]) window.addEventListener(name, paintSyncButtons);
  // The status bar's sync item does the same as the button.
  window.addEventListener("ark:manual-sync", () => requestSync());
}
