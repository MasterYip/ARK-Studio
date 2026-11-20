// Size badge and refresh for a resource location (Settings > Resources and
// the Resources graph).  A path is measured on the device that holds it:
// the ARK server measures its own host (setup action "measure"), the ARK
// desktop app measures this computer (window.__ARK_DESKTOP__.measurePaths)
// and reports through setup action "location-stats".
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { sizeView, desktopReports } from "./location-size-model.js";

/** "2.3 GB · 12,340 files · 5 min ago" (+ git split, old marker, ↻ button). */
export function sizeBadge(stats, { onRefresh = null } = {}) {
  const view = sizeView(stats, { t });
  return h("span", { class: `loc-size ${view.state}${view.stale && view.state !== "none" ? " stale" : ""}`,
    title: view.title, "data-loc-size": view.state },
  h("span", { class: "loc-size-text" }, view.text),
  view.split ? h("span", { class: "loc-size-split" }, view.split) : null,
  view.stale && view.state !== "none" ? h("span", { class: "pill warn" }, t("size.stale")) : null,
  onRefresh ? h("button", { class: "icon-btn loc-size-refresh", type: "button", title: t("size.refresh"),
    "aria-label": t("size.refresh"), "data-loc-size-refresh": "1",
    onclick: (event) => { event.stopPropagation(); onRefresh(event.currentTarget); } }, icon("refresh", "sm")) : null);
}

let desktopId = null;

/** This computer's device id when the page runs in the ARK desktop app ("" otherwise). */
export async function desktopDeviceId() {
  const desk = typeof window !== "undefined" ? window.__ARK_DESKTOP__ : null;
  if (!desk?.deviceId || !desk?.measurePaths) return "";
  if (desktopId === null) {
    try { desktopId = String((await desk.deviceId())?.id || ""); } catch { desktopId = ""; }
  }
  return desktopId;
}

/** Measure paths of this computer in the desktop app and report them. */
export async function measureOnDesktop(targets, hostId) {
  const results = await window.__ARK_DESKTOP__.measurePaths(targets.map((x) => x.path));
  const items = desktopReports(results, targets);
  if (!items.length) return null;
  return api.setupAction("location-stats", { host_id: hostId, items });
}

/**
 * The ↻ button: measure one location again.  Returns { done | queued |
 * wait | elsewhere: true, message }.
 */
export async function refreshLocationSize({ resource, path, hostId = "", deviceLabel = "" }) {
  const mine = await desktopDeviceId();
  if (mine && hostId && mine === hostId) {
    await measureOnDesktop([{ resource, path }], mine);
    return { done: true, message: t("size.measured") };
  }
  try {
    const r = await api.setupAction("measure", { resource, path_or_ref: path });
    if (r.retry_after) return { wait: true, message: t("size.wait", { n: r.retry_after }) };
    return { queued: true, message: t("size.queued") };
  } catch (err) {
    if (err.code === "SETUP_SIZE_ON_DEVICE") {
      return { elsewhere: true, message: t("size.elsewhere", { device: deviceLabel || hostId || "?" }) };
    }
    throw err;
  }
}
