// "Home shows" controls (home-filter-model.js): the line under the Home title
// that says how many nodes the header graph draws, the dialog it opens, and
// the same fields in Settings › Display.
import { h } from "./dom.js";
import { t, getLang, statusLabel, typeLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { openModal, toast } from "./components.js";
import { homeFilterFor, homeFilterQuery, saveHomeFilter } from "./profile.js";
import {
  DEFAULT_HOME_LIMIT, HERO_NODE_BUDGET, HOME_DAYS, HOME_LIMITS, defaultHomeFilter, homeGraphSummary, normalizeHomeFilter, sameHomeFilter,
} from "./home-filter-model.js";

const STATUSES = ["inbox", "open", "active", "review", "blocked", "done", "abandoned"];

function count(n) {
  return Number(n || 0).toLocaleString(getLang() === "zh" ? "zh-CN" : "en-US");
}

/** The words for a limit option: "Newest 500" / "All". */
export function limitLabel(limit) {
  return limit ? t("home.filter.newest", { n: count(limit) }) : t("home.filter.all");
}

/**
 * The line under the Home title: when the graph leaves nodes out, how many
 * it shows of how many; always a way to change it.
 */
export function homeFilterLine(ctx, graph) {
  const summary = homeGraphSummary(graph);
  const open = () => openHomeFilterDialog(ctx);
  if (!graph || !summary.limited) {
    return h("button", {
      class: "link home-filter-link", type: "button", dataset: { homeFilter: "all" },
      title: t("home.filter.title"), onclick: open,
    }, icon("filter", "sm"), t("home.filter.button"));
  }
  const text = summary.narrowed || summary.matched <= summary.shown
    ? t("home.filter.narrowed", { n: count(summary.shown), total: count(summary.total) })
    : t("home.filter.limited", { n: count(summary.shown), total: count(summary.total) });
  return h("span", { class: "home-filter-line", dataset: { homeFilter: "limited", shown: String(summary.shown), total: String(summary.total) } },
    icon("filter", "sm"), h("span", null, text), " · ",
    h("button", { class: "link home-filter-link", type: "button", onclick: open }, t("home.filter.change")));
}

/** Fields of the filter; `onChange(filter)` after each edit. */
export function homeFilterFields(ctx, value, onChange) {
  let filter = normalizeHomeFilter(value);
  const set = (patch) => { filter = normalizeHomeFilter({ ...filter, ...patch }); sync(); onChange?.(filter); };
  const limit = h("select", { class: "input", "data-home-filter-limit": "", "aria-label": t("home.filter.count"),
    onchange: () => set({ limit: Number(limit.value) }) },
    HOME_LIMITS.map((n) => h("option", { value: String(n) },
      n === DEFAULT_HOME_LIMIT ? `${limitLabel(n)} · ${t("home.filter.default")}` : limitLabel(n))));
  const days = h("select", { class: "input", "data-home-filter-days": "", "aria-label": t("home.filter.time"),
    onchange: () => set({ days: Number(days.value) }) },
    HOME_DAYS.map((n) => h("option", { value: String(n) }, n ? t("home.filter.days", { n }) : t("home.filter.any_time"))));
  const stats = ctx.S.stats || {};
  const present = (counts, all) => [...new Set([...all.filter((id) => counts?.[id]), ...Object.keys(counts || {})])];
  const chipGroup = (key, ids, label) => {
    const box = h("div", { class: "chips home-filter-chips", dataset: { homeFilterGroup: key } });
    const draw = () => {
      box.replaceChildren(
        h("button", { class: `chip ${filter[key].length ? "" : "on"}`.trim(), type: "button", dataset: { value: "" },
          "aria-pressed": String(!filter[key].length), onclick: () => set({ [key]: [] }) }, t("home.filter.any")),
        ...[...new Set([...ids, ...filter[key]])].map((id) => {
          const on = filter[key].includes(id);
          return h("button", {
            class: `chip ${on ? "on" : ""}`.trim(), type: "button", dataset: { value: id }, "aria-pressed": String(on),
            onclick: () => set({ [key]: on ? filter[key].filter((x) => x !== id) : [...filter[key], id] }),
          }, label(id));
        }));
    };
    box._draw = draw;
    return box;
  };
  const types = chipGroup("types", present(stats.by_type, []).filter((id) => id !== "topic"), typeLabel);
  const statuses = chipGroup("statuses", present(stats.by_status, STATUSES), statusLabel);
  const warning = h("div", { class: "settings-note home-filter-warning", hidden: true }, icon("alert", "sm"),
    t("home.filter.budget", { n: count(HERO_NODE_BUDGET) }));
  const sync = () => {
    limit.value = String(filter.limit);
    days.value = String(filter.days);
    types._draw();
    statuses._draw();
    warning.hidden = filter.limit !== 0 ? filter.limit <= HERO_NODE_BUDGET : Number(stats.total || 0) <= HERO_NODE_BUDGET;
  };
  sync();
  const field = (label, control) => h("div", { class: "field" }, h("label", null, label), control);
  const element = h("div", { class: "home-filter-fields" },
    field(t("home.filter.count"), limit), warning,
    field(t("home.filter.time"), days),
    field(t("home.filter.types"), types),
    field(t("home.filter.statuses"), statuses));
  return {
    element,
    value: () => filter,
    reset: () => { filter = defaultHomeFilter(); sync(); onChange?.(filter); },
  };
}

// Save, then read the overview for the new filter and repaint Home.
async function applyHomeFilter(ctx, filter) {
  const project = ctx.S.project?.id || "";
  await saveHomeFilter(project, filter);
  if (ctx.S.tab === "overview") {
    ctx.S.overview = await api.overview({ fresh: true }, homeFilterQuery(project)).catch(() => ctx.S.overview);
    ctx.refresh?.();
  }
}

export function openHomeFilterDialog(ctx) {
  const project = ctx.S.project?.id || "";
  const saved = homeFilterFor(project);
  const fields = homeFilterFields(ctx, saved);
  openModal({
    title: t("home.filter.title"),
    body: h("div", { class: "home-filter-dialog" }, h("p", { class: "muted" }, t("home.filter.lead")), fields.element),
    actions: [
      { label: t("home.filter.reset"), ghost: true, close: false, onClick: () => fields.reset() },
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: async () => {
          if (sameHomeFilter(fields.value(), saved)) return true;
          try {
            await applyHomeFilter(ctx, fields.value());
            toast(t("home.filter.saved"), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          return true;
        },
      },
    ],
  });
}

/** Settings › Display: the same fields for the open project, saved on change. */
export function homeFilterPanel(ctx) {
  const project = ctx.S.project?.id || "";
  const status = h("span", { class: "settings-status" });
  let timer = 0;
  const fields = homeFilterFields(ctx, homeFilterFor(project), (filter) => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      try {
        await saveHomeFilter(project, filter);
        status.textContent = t("home.filter.saved");
      } catch (err) { status.textContent = err.message; }
    }, 300);
  });
  return h("section", { class: "panel settings-panel", "data-home-filter-settings": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("home.filter.title")), status),
    h("p", { class: "settings-lead" }, t("home.filter.lead")),
    ctx.S.project?.name ? h("p", { class: "muted" }, t("home.filter.project", { name: ctx.S.project.name })) : null,
    fields.element,
    h("div", { class: "settings-actions" }, h("button", { class: "ghost-btn", type: "button", onclick: () => fields.reset() },
      icon("refresh", "sm"), t("home.filter.reset"))));
}
