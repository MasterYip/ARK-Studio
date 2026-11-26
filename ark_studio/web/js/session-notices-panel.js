// Settings › Personal › Session alerts: the person's switches for "a session
// finished" and "a session stopped with an error" (saved on the server per
// person, so the web page, the desktop app and the phone app follow them),
// and the browser's permission for system notifications.
import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { toast } from "./components.js";
import {
  refreshSessionNotices, requestSystemNotifications, sessionNotices, setSessionNoticePrefs, systemNotificationState,
} from "./session-notices.js";

export function sessionNoticesPanel(ctx) {
  const prefs = { ...sessionNotices(ctx).prefs };
  const toggles = {};
  const toggle = (key) => {
    const input = h("input", {
      type: "checkbox", checked: prefs[key], "data-session-notice": key,
      onchange: async (event) => {
        const on = event.target.checked;
        input.disabled = true;
        try {
          const saved = await setSessionNoticePrefs(ctx, { [key]: on });
          Object.assign(prefs, saved);
          toast(t("settings.pref_saved"), "ok");
        } catch (err) {
          event.target.checked = !on;
          toast(err.message, "err");
        } finally {
          input.disabled = false;
        }
      },
    });
    toggles[key] = input;
    return h("div", null,
      h("label", { class: "display-toggle session-notice-toggle" }, input, h("span", null, t(`settings.notify.${key}`))),
      h("p", { class: "muted small" }, t(`settings.notify.${key}_hint`)));
  };
  const browserLine = h("div", { class: "session-notice-browser" });
  const paintBrowser = () => {
    clear(browserLine);
    const state = systemNotificationState();
    browserLine.append(h("span", null, t("settings.notify.browser")));
    if (state === "default") {
      browserLine.append(h("button", {
        class: "btn sm", type: "button",
        onclick: async () => { await requestSystemNotifications(); paintBrowser(); },
      }, t("settings.notify.browser_allow")));
    } else {
      browserLine.append(h("span", { class: `pill ${state === "granted" ? "ok" : "warn"}` },
        t(`settings.notify.browser_${state}`)));
    }
    browserLine.append(h("p", { class: "muted small" }, t("settings.notify.browser_hint")));
  };
  paintBrowser();
  // The switches as saved (another device may have changed them).
  refreshSessionNotices(ctx)?.then(() => {
    const saved = sessionNotices(ctx).prefs;
    for (const [key, input] of Object.entries(toggles)) input.checked = saved[key];
    Object.assign(prefs, saved);
  });
  return h("section", { class: "panel settings-panel", "data-settings-section": "notifications" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.notify.title"))),
    h("p", { class: "muted small" }, t("settings.notify.lead")),
    toggle("done"),
    toggle("error"),
    browserLine);
}
