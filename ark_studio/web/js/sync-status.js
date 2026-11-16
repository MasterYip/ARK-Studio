// Sync state of cached data (docs/development/client-cache.md).
//
// Desktop (workbench shell, body.wb-on): one status-bar item, always shown —
// "Synced" (muted check), "Syncing…" (spinner) or "Offline · read-only" /
// "Server unreachable · read-only" in the warning colour.  The always-on
// "Synced" item answers "is this current?" without hunting for a chip, and
// the bar has room on the right.  Its tooltip shows the last successful sync
// and pending revalidations; clicking it re-checks the server and refreshes
// the view.  Going offline also tints the status bar and shows one toast.
// Narrow screens (no status bar): a floating chip while syncing and a
// read-only banner while offline.
import { h, fmtDateTime } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { checkConnectivity, perfMark, syncState } from "./api.js";
import { setStatusItem, shellActive } from "./workbench.js";
import { toast } from "./components.js";

let chip = null;
let item = null;
let showTimer = 0;
let firstRenderDone = false;
let lastSyncedAt = 0;
let wasOffline = false;

function ensureChip() {
  if (chip?.isConnected) return chip;
  chip = h("div", { class: "sync-chip hidden", role: "status", "aria-live": "polite" });
  document.body.appendChild(chip);
  return chip;
}

function ensureItem() {
  if (item) return item;
  item = h("button", {
    class: "wb-status-item wb-sync", type: "button", "aria-live": "polite",
    onclick: async () => {
      if (await checkConnectivity()) window.dispatchEvent(new CustomEvent("ark:manual-sync"));
    },
  });
  setStatusItem("sync", item);
  return item;
}

function offlineText() {
  const unreachable = syncState.connectivity === "unreachable";
  return {
    title: t(unreachable ? "cache.unreachable_title" : "cache.offline_title"),
    banner: t(unreachable ? "cache.unreachable_banner" : "cache.offline_banner"),
  };
}

function paintItem() {
  const el = ensureItem();
  const last = lastSyncedAt ? fmtDateTime(new Date(lastSyncedAt).toISOString()) : t("cache.never");
  const tip = [t("cache.last_sync", { time: last })];
  if (syncState.syncing > 0) tip.push(t("cache.pending", { n: syncState.syncing }));
  if (syncState.offline) {
    const { title, banner } = offlineText();
    el.className = "wb-status-item wb-sync offline";
    el.replaceChildren(icon("alert", "sm"), `${title} · ${t("cache.read_only")}`);
    tip.unshift(banner);
  } else if (syncState.syncing > 0) {
    el.className = "wb-status-item wb-sync syncing";
    el.replaceChildren(h("span", { class: "spinner" }), t("cache.syncing"));
  } else {
    el.className = "wb-status-item wb-sync synced";
    el.replaceChildren(icon("check", "sm"), t("cache.synced"));
  }
  tip.push(t("cache.click_to_sync"));
  el.title = tip.join("\n");
}

function paintChip() {
  const el = ensureChip();
  if (syncState.offline) {
    clearTimeout(showTimer);
    showTimer = 0;
    const { title, banner } = offlineText();
    el.className = "sync-chip offline";
    el.replaceChildren(h("b", null, title), h("span", null, banner));
    return;
  }
  if (syncState.syncing > 0) {
    // Only show for syncs that take a noticeable time.
    if (!showTimer && el.classList.contains("hidden")) {
      showTimer = setTimeout(() => {
        showTimer = 0;
        if (syncState.syncing > 0 && !syncState.offline) {
          el.className = "sync-chip syncing";
          el.replaceChildren(h("span", { class: "spinner" }), h("span", null, t("cache.syncing")));
        }
      }, 400);
    }
    return;
  }
  clearTimeout(showTimer);
  showTimer = 0;
  el.className = "sync-chip hidden";
  el.replaceChildren();
}

function paint() {
  document.body.classList.toggle("ark-offline", syncState.offline);
  if (!syncState.offline && syncState.syncing === 0) {
    lastSyncedAt = Date.now();
    if (firstRenderDone) perfMark("fresh");
  }
  if (syncState.offline && !wasOffline && shellActive()) {
    // Once per outage on desktop; the tinted status bar stays meanwhile.
    toast(offlineText().banner, "err");
  }
  wasOffline = syncState.offline;
  paintItem();
  paintChip();
}

/** First view painted (from cache or network); "fresh" follows once synced. */
export function markFirstRender() {
  perfMark("firstRender");
  firstRenderDone = true;
  if (syncState.syncing === 0) perfMark("fresh");
}

export function installSyncStatus() {
  window.addEventListener("ark:syncing", paint);
  window.addEventListener("ark:connectivity", paint);
  // The browser's online/offline flags are hints only (often wrong behind
  // proxies and VPNs): each one, and returning to the tab, re-checks the
  // server; the probe decides.
  const recheck = () => { if (syncState.offline) checkConnectivity(); };
  window.addEventListener("online", () => checkConnectivity());
  window.addEventListener("offline", () => checkConnectivity());
  window.addEventListener("focus", recheck);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) recheck(); });
  paint();
}
