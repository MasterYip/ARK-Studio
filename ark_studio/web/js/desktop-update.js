// Notices at the top of Home:
// * "ARK Studio was updated to 2.x.y" with the key changes, after the
//   desktop app's first start of a new version (local page of the app).
// * "This server was updated to 2.x.y", once per person and version, when
//   the server reports a new release version (any page but the app's local one).
// * "ARK Studio 2.x.y is available · Update / Later" on the app's local
//   Home, only when the app window does not show this notice itself (older
//   apps; newer ones show it on every page: desktop shell).
// Models: desktop-update-model.js; shared update state: update-state.js.
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { toast } from "./components.js";
import {
  loadDismissed, releaseUrl, saveDismissed, serverNotice, shellShowsUpdates, updateBanner,
} from "./desktop-update-model.js";
import { appUpdateHub, latestUpdate, onUpdate, refreshUpdate, watchUpdates } from "./update-state.js";
import { serverVersion, updatedNoticeCard } from "./about.js";
import { saveSeenServerVersion, seenServerVersion } from "./profile.js";

/** A placeholder that fills with the notices that apply. */
export function desktopUpdateBanner() {
  const slot = h("div", { class: "desktop-update-slot" });
  const parts = { updated: null, server: null, available: null };
  const draw = () => {
    slot.replaceChildren(...Object.values(parts).filter(Boolean));
  };
  appUpdateHub().then((found) => {
    if (found?.role !== "local") showServerNotice(parts, draw);
    if (!found) return;
    const { hub, role } = found;
    const show = (info) => {
      if (role === "local" && !parts.updated) {
        parts.updated = updatedNoticeCard(info, hub, { onClose: () => { parts.updated = null; } }) || null;
      }
      if (role === "local" && !parts.available && !shellShowsUpdates()) {
        const banner = updateBanner(info, loadDismissed());
        if (banner) parts.available = availableBanner(hub, banner, () => { parts.available = null; draw(); });
      }
      draw();
    };
    // Home is re-rendered often: stop listening once this slot was shown and is gone.
    let seen = false;
    let misses = 0;
    const off = onUpdate((info) => {
      if (slot.isConnected) { seen = true; show(info); } else if (seen || ++misses > 2) off();
    });
    show(latestUpdate());
    watchUpdates({ hub });
    refreshUpdate(hub);
  }).catch(() => { /* no notices */ });
  return slot;
}

function showServerNotice(parts, draw) {
  const version = serverVersion();
  const step = serverNotice(version, seenServerVersion());
  if (step === "store") { saveSeenServerVersion(version); return; }
  if (step !== "show") return;
  const close = () => { parts.server = null; draw(); saveSeenServerVersion(version); };
  parts.server = h("section", { class: "panel server-updated", "data-server-updated": version,
    "aria-label": t("server.updated.title", { version }) },
  icon("info", "sm"),
  h("b", null, t("server.updated.title", { version })),
  h("a", { class: "server-updated-link", href: releaseUrl(version), target: "_blank", rel: "noopener noreferrer" },
    t("server.updated.changes")),
  h("button", { class: "btn sm ghost", type: "button", "data-server-updated-close": "", onclick: close }, t("app.updated.close")));
  draw();
}

function availableBanner(hub, banner, onClose) {
  const status = h("span", { class: "muted desktop-update-status", role: "status" });
  const primary = h("button", { class: "btn sm primary", type: "button" },
    t(banner.action === "install" ? "desktop.update.install" : "desktop.update.download"));
  const later = h("button", { class: "btn sm ghost", type: "button" }, t("desktop.update.later"));
  const box = h("section", { class: "panel desktop-update-banner", "aria-label": t("desktop.update.available", { version: banner.version }) },
    icon("download", "sm"),
    h("b", { class: "desktop-update-title" }, t("desktop.update.available", { version: banner.version })),
    status,
    h("span", { class: "desktop-update-actions" }, primary, later));
  later.onclick = () => {
    saveDismissed(banner.version);
    Promise.resolve(hub.dismissUpdate?.(banner.version)).catch(() => {});
    onClose();
  };
  primary.onclick = async () => {
    if (banner.action === "download") {
      Promise.resolve(hub.openReleases(banner.version)).catch((err) => toast(String(err?.message || err), "err"));
      return;
    }
    primary.disabled = true;
    later.disabled = true;
    status.textContent = t("desktop.update.installing");
    try {
      // Installs and restarts the app; returning means nothing was installed.
      await hub.installUpdate();
      status.textContent = "";
    } catch (err) {
      status.textContent = "";
      toast(t("desktop.update.failed", { error: String(err?.message || err) }), "err");
    }
    primary.disabled = false;
    later.disabled = false;
  };
  return box;
}
