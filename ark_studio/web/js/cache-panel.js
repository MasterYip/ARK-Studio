// Settings › Personal › Account: size of the browser's project cache and a
// "Clear cached data" button (docs/development/client-cache.md).
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { clearClientData, clientCache } from "./api.js";
import { toast } from "./components.js";

export function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}

export function cachePanel() {
  const status = h("span", { class: "settings-status cache-usage" }, "…");
  const paint = async () => {
    if (typeof indexedDB === "undefined") {
      status.textContent = t("settings.cache.unavailable");
      return;
    }
    const usage = await clientCache.usage().catch(() => null);
    status.textContent = !usage ? t("settings.cache.unavailable")
      : usage.entries ? t("settings.cache.size", { size: formatBytes(usage.bytes), projects: usage.projects })
        : t("settings.cache.empty");
  };
  const button = h("button", {
    class: "ghost-btn", type: "button",
    onclick: async () => {
      button.disabled = true;
      try {
        await clearClientData({ signOut: false });
        toast(t("settings.cache.cleared"), "ok");
      } finally {
        button.disabled = false;
        paint();
      }
    },
  }, icon("trash", "sm"), t("settings.cache.clear"));
  paint();
  return h("section", { class: "panel settings-panel cache-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.cache.title"))),
    h("p", { class: "settings-lead" }, t("settings.cache.lead")),
    h("div", { class: "assistant-api-actions" }, button, status));
}
