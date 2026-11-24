// Standby server notice (docs/operations/standby.md).  A read-only replica
// marks its pages with <meta name="ark-standby" data-last-sync="...">.  The
// notice is a one-line banner above the top bar; hiding it (for this browser
// session) leaves a small "Read-only" chip in the top bar, and the chip brings
// the banner back.  While the banner shows, html.ark-standby-banner moves the
// whole layout down by --ark-banner-h (app.css "Standby banner").
import { h } from "./dom.js";
import { t, getLang } from "./i18n.js";

const HIDDEN_KEY = "ark.standbyBannerHidden";

export function standbyInfo(doc = globalThis.document) {
  const meta = doc?.querySelector?.('meta[name="ark-standby"]');
  return meta ? { lastSync: meta.dataset?.lastSync || meta.getAttribute?.("data-last-sync") || "" } : null;
}

export function formatSyncTime(iso, lang) {
  const when = iso ? new Date(iso) : null;
  if (!when || Number.isNaN(when.getTime())) return "";
  return when.toLocaleString(lang === "zh" ? "zh-CN" : "en-GB",
    { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

export function standbyText(info, lang = getLang(), { short = false } = {}) {
  const time = formatSyncTime(info?.lastSync, lang);
  if (short) return time ? t("standby.banner_short", { time }) : t("standby.chip");
  return time ? t("standby.banner", { time }) : t("standby.banner_no_time");
}

function bannerHidden() {
  try { return sessionStorage.getItem(HIDDEN_KEY) === "1"; } catch { return false; }
}

function setBannerHidden(hidden) {
  try {
    if (hidden) sessionStorage.setItem(HIDDEN_KEY, "1");
    else sessionStorage.removeItem(HIDDEN_KEY);
  } catch { /* private mode: the choice lasts for this page only */ }
}

let onToggle = () => {};

// Install once at startup; `rerender` redraws the top bar (chip on/off).
export function installStandby({ rerender } = {}) {
  const info = standbyInfo();
  if (!info) return false;
  onToggle = rerender || onToggle;
  document.documentElement.classList.add("ark-standby");
  renderBanner(!bannerHidden());
  return true;
}

function renderBanner(show) {
  const root = document.documentElement;
  document.getElementById("ark-standby-banner")?.remove();
  root.classList.toggle("ark-standby-banner", show);
  if (!show) return;
  const banner = h("div", { id: "ark-standby-banner", role: "status" },
    h("span", { class: "standby-banner-text" }, standbyText(standbyInfo())),
    h("span", { class: "standby-banner-text short" }, standbyText(standbyInfo(), getLang(), { short: true })),
    h("button", {
      class: "standby-banner-close", type: "button", title: t("standby.dismiss"), "aria-label": t("standby.dismiss"),
      onclick: () => { setBannerHidden(true); renderBanner(false); onToggle(); },
    }, "×")); // i18n-ignore: close glyph
  document.body.prepend(banner);
}

// The top-bar chip, shown while the banner is hidden on a standby server.
export function standbyChip() {
  const info = standbyInfo();
  if (!info || document.documentElement.classList.contains("ark-standby-banner")) return null;
  return h("button", {
    class: "standby-chip top-ctl", type: "button", title: `${standbyText(info)} ${t("standby.chip_show")}`,
    onclick: () => { setBannerHidden(false); renderBanner(true); onToggle(); },
  }, t("standby.chip"));
}

// Language change: redraw the banner text.
export function refreshStandby() {
  if (document.getElementById("ark-standby-banner")) renderBanner(true);
}
