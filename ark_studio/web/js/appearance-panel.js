import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { displayPrefs, setDisplayPrefs } from "./appearance.js";
import { applyThemePref, themePref } from "./profile.js";

// Theme preference values; the visible option labels are profile.theme_<mode>.
const THEME_MODES = ["system", "light", "dark"];

export function appearancePanel() {
  const prefs = displayPrefs();
  const theme = h("select", { class: "input", "data-theme-pref": "", "aria-label": t("profile.theme") },
    ...THEME_MODES.map(mode => h("option", {
      value: mode, selected: themePref() === mode,
    }, t(`profile.theme_${mode}`))));
  theme.addEventListener("change", () => {
    applyThemePref(theme.value);
    window.dispatchEvent(new CustomEvent("ark:pref-change", { detail: { theme: theme.value } }));
    window.dispatchEvent(new CustomEvent("ark:theme-applied"));
  });
  const high = h("input", { type: "checkbox", checked: prefs.high_contrast,
    onchange: () => setDisplayPrefs({ high_contrast: high.checked }) });
  const value = h("output", { class: "mono", for: "appearance-font-scale" }, `${Math.round(prefs.font_scale * 100)}%`);
  const range = h("input", { id: "appearance-font-scale", type: "range", min: 100, max: 150, step: 5,
    value: Math.round(prefs.font_scale * 100), "aria-label": t("settings.display.font_scale"),
    oninput: () => {
      value.value = `${range.value}%`;
      setDisplayPrefs({ font_scale: Number(range.value) / 100 });
    } });
  return h("section", { class: "panel settings-panel", "data-display-settings": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.display.title"))),
    h("label", { class: "field" }, h("span", null, t("profile.theme")), theme),
    h("label", { class: "display-toggle" }, high, h("span", null, t("settings.display.high_contrast"))),
    h("div", { class: "display-scale" },
      h("label", { for: "appearance-font-scale" }, t("settings.display.font_scale")), range, value),
    h("div", { class: "display-sample" },
      h("strong", null, t("settings.display.sample")),
      h("span", { class: "sub" }, t("settings.palette.muted")),
      h("span", { class: "muted" }, t("settings.palette.line"))),
    h("div", { class: "settings-actions" }, h("button", { class: "ghost-btn", type: "button",
      onclick: () => {
        high.checked = false; range.value = "100"; value.value = "100%";
        setDisplayPrefs({ font_scale: 1, high_contrast: false });
      } }, icon("refresh", "sm"), t("action.reset"))));
}
