// The local Home's update banner in the ARK Studio desktop app ("ARK Studio
// 2.x.y is available · Update / Later").  The model is desktop-update-model.js.
// Update runs the same download-install-restart as Settings > About; portable
// builds open the release page instead.  Later hides it until the next version.
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { toast } from "./components.js";
import { desktopHub, hubContext } from "./desktop-hub-model.js";
import { loadDismissed, saveDismissed, shouldRecheck, updateBanner } from "./desktop-update-model.js";

const RECHECK_MS = 15000;

/** A placeholder that becomes the banner when the local desktop app has an update. */
export function desktopUpdateBanner() {
  const slot = h("div", { class: "desktop-update-slot" });
  const hub = desktopHub();
  if (!hub || typeof hub.update !== "function") return slot;
  hubContext(hub).then(async (context) => {
    if (context?.role !== "local") return;
    for (let attempt = 0; ; attempt++) {
      let info = null;
      try { info = await hub.update(); } catch { return; }
      const banner = updateBanner(info, loadDismissed());
      if (banner) { render(slot, hub, banner); return; }
      if (!shouldRecheck(info, attempt)) return;
      await new Promise((resolve) => setTimeout(resolve, RECHECK_MS));
      if (!slot.isConnected) return; // Home was left meanwhile
    }
  }).catch(() => { /* no banner */ });
  return slot;
}

function render(slot, hub, banner) {
  if (!slot.isConnected) return;
  const status = h("span", { class: "muted desktop-update-status", role: "status" });
  const primary = h("button", { class: "btn sm primary", type: "button" },
    t(banner.action === "install" ? "desktop.update.install" : "desktop.update.download"));
  const later = h("button", { class: "btn sm ghost", type: "button" }, t("desktop.update.later"));
  const box = h("section", { class: "panel desktop-update-banner", "aria-label": t("desktop.update.available", { version: banner.version }) },
    icon("download", "sm"),
    h("b", { class: "desktop-update-title" }, t("desktop.update.available", { version: banner.version })),
    status,
    h("span", { class: "desktop-update-actions" }, primary, later));
  later.onclick = () => { saveDismissed(banner.version); slot.replaceChildren(); };
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
  slot.replaceChildren(box);
}
