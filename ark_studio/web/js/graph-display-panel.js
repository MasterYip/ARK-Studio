// Settings › Appearance › Graphs: the small per-graph-type tab strip and
// the rows of the graph display settings (graph-display.js).  Used twice:
// the project defaults (Project › Appearance) and the person's overrides
// (Personal › Appearance & input); views/settings.js wires saving.

import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { GRAPH_DISPLAY_TABS } from "./graph-display.js";

// Graph-type tabs in order; "general" applies to every graph.
export const GRAPH_SETTING_TABS = ["general", "force", "mind", "local"];
const TAB_STORE_KEY = "ark.settingsGraphTab";

export function storedGraphTab() {
  try {
    const value = localStorage.getItem(TAB_STORE_KEY);
    return GRAPH_SETTING_TABS.includes(value) ? value : "general";
  } catch { return "general"; }
}

function rememberGraphTab(id) {
  try { localStorage.setItem(TAB_STORE_KEY, id); } catch { /* storage unavailable */ }
}

/**
 * The tab strip (the segmented control Settings uses for its override
 * choice, as an ARIA tablist): click or Left/Right/Home/End to switch.
 * `panes` maps tab id -> element; only the active one is shown.
 */
export function graphTabStrip({ panes, active = storedGraphTab(), onSelect = null, idPrefix }) {
  const buttons = new Map();
  const strip = h("div", { class: "segmented settings-graph-tabs", role: "tablist", "aria-label": t("settings.graphs.tabs_label") });
  let current = GRAPH_SETTING_TABS.includes(active) ? active : "general";
  function select(id, { focus = false, remember = true } = {}) {
    if (!buttons.has(id)) return;
    current = id;
    for (const [key, button] of buttons) {
      const on = key === id;
      button.classList.toggle("on", on);
      button.setAttribute("aria-selected", String(on));
      button.tabIndex = on ? 0 : -1;
      panes[key]?.classList.toggle("hidden", !on);
    }
    if (focus) buttons.get(id).focus();
    if (remember) rememberGraphTab(id);
    onSelect?.(id);
  }
  for (const id of GRAPH_SETTING_TABS) {
    const pane = panes[id];
    if (!pane) continue;
    const buttonId = `${idPrefix}-tab-${id}`;
    const paneId = `${idPrefix}-pane-${id}`;
    pane.id = paneId;
    pane.setAttribute("role", "tabpanel");
    pane.setAttribute("aria-labelledby", buttonId);
    pane.dataset.graphPane = id;
    const button = h("button", {
      class: "seg-btn", type: "button", role: "tab", id: buttonId, "aria-controls": paneId,
      "data-graph-tab": id, title: t(`settings.graphs.tab.${id}_hint`),
      onclick: () => select(id),
    }, t(`settings.graphs.tab.${id}`));
    buttons.set(id, button);
    strip.appendChild(button);
  }
  strip.addEventListener("keydown", (event) => {
    const ids = [...buttons.keys()];
    const index = ids.indexOf(current);
    const next = { ArrowRight: index + 1, ArrowLeft: index - 1, Home: 0, End: ids.length - 1 }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    select(ids[(next + ids.length) % ids.length], { focus: true });
  });
  select(current, { remember: false });
  return { el: strip, select, current: () => current };
}

function formatValue(tab, field, value) {
  if (field.kind === "number" && field.key === "marker_gap") return `${Number(value).toFixed(2)}×`;
  if (field.kind === "number" && field.key === "marker_arc") return `${Number(value).toFixed(2)} rad`; // i18n-ignore: unit
  if (field.kind === "number") return `${Math.round(Number(value) * 100)}%`;
  if (field.key === "image_size") return `${value} px`; // i18n-ignore: unit
  return String(value);
}

/**
 * Rows for one tab.  `value(key)` is the value to show, `source(key)` one
 * of "personal" | "project" | "default" (personal mode only), `onChange(key,
 * value)` stores a draft value and `onReset(key)` (personal mode) drops the
 * person's value for that setting.  Returns `{ elements, sync, inputs }`.
 */
export function graphDisplayRows({ tab, value, onChange, source = null, onReset = null }) {
  const spec = GRAPH_DISPLAY_TABS.find((item) => item.id === tab);
  const rows = [];
  for (const field of spec.fields) {
    const label = t(`settings.graphs.${tab}.${field.key}`);
    let control;
    let output = null;
    if (field.kind === "bool") {
      control = h("input", { type: "checkbox", class: "settings-check", "aria-label": label,
        onchange: () => onChange(field.key, control.checked) });
    } else if (field.kind === "choice") {
      control = h("select", { class: "select", "aria-label": label,
        onchange: () => onChange(field.key, control.value) },
      ...field.choices.map((choice) => h("option", { value: choice }, t(`settings.graphs.${tab}.${field.key}_${choice}`))));
    } else {
      output = h("span", { class: "physics-raw" });
      control = h("input", { type: "range", class: "physics-range", "aria-label": label,
        min: String(field.min), max: String(field.max), step: String(field.step),
        oninput: () => {
          const n = field.kind === "int" ? Math.round(Number(control.value)) : Math.round(Number(control.value) * 1000) / 1000;
          output.textContent = formatValue(tab, field, n);
          onChange(field.key, n);
        } });
    }
    const status = source ? h("span", { class: "pill settings-graph-source", "data-graph-source": field.key }) : null;
    const reset = onReset ? h("button", {
      class: "icon-btn sm settings-graph-reset", type: "button", "data-graph-reset": field.key,
      title: t("settings.graphs.reset_setting"), "aria-label": `${t("settings.graphs.reset_setting")}: ${label}`,
      onclick: () => onReset(field.key),
    }, icon("refresh", "sm")) : null;
    const el = h("div", { class: "shortcut-row settings-graph-row", "data-graph-setting": `${tab}.${field.key}` },
      h("div", null,
        h("div", { class: "physics-name" }, label, status ? " " : null, status),
        h("div", { class: "physics-hint" }, t(`settings.graphs.${tab}.${field.key}_hint`))),
      h("div", { class: "row tight" }, control, output, reset));
    const sync = () => {
      const current = value(field.key);
      if (field.kind === "bool") control.checked = Boolean(current);
      else control.value = String(current);
      if (output) output.textContent = formatValue(tab, field, current);
      if (status) {
        const from = source(field.key);
        status.className = `pill settings-graph-source${from === "personal" ? " ok" : ""}`;
        status.textContent = t(`settings.graphs.source_${from}`);
      }
      if (reset) reset.hidden = source(field.key) !== "personal";
    };
    rows.push({ el, sync, control });
  }
  return {
    elements: rows.map((row) => row.el),
    inputs: rows.map((row) => row.control),
    sync: () => { for (const row of rows) row.sync(); },
  };
}

/** A panel for one tab's display rows: title, one-line lead, rows. */
export function graphDisplayPanel({ tab, rows, actions = null, note = null }) {
  return h("section", { class: "panel settings-panel", "data-graph-display": tab },
    h("div", { class: "panel-head" }, h("h3", null, t(`settings.graphs.${tab}.title`)), actions),
    h("p", { class: "settings-lead" }, t(`settings.graphs.${tab}.lead`)),
    note,
    ...rows.elements);
}

