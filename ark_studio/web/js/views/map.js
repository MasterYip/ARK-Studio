import { h, clear, emptyState, typeColor, statusColor } from "../dom.js";
import { t, typeLabel, statusLabel, impLabel, locale } from "../i18n.js";
import { icon } from "../icons.js";
import { GanttView } from "../gantt.js";
import { attachGraphHeight, unitCard, viewToggle } from "../components.js";
import { displayName } from "../users.js";
import {
  MAP_LIST_SORTS, MAP_LIST_GROUPS, normalizeMapListPrefs, defaultMapListDirection,
  sortMapUnits, groupMapUnits, mapListPage,
} from "../map-list-model.js";
import { graphPanel } from "./plan.js";

export function renderMap(host, ctx) {
  const S = ctx.S;
  clear(host);
  const page = h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, `${t("nav.map")} · ${t("stage.evidence")}`),
        h("h1", { class: "page-title" }, t("map.title")),
        h("p", { class: "page-sub" }, t("map.subtitle"))),
      h("div", { class: "head-actions" },
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("sparkles", "sm"), t("assistant.title")),
        h("button", { class: "primary-btn", onclick: () => ctx.openCreate({ type: "claim" }) }, icon("plus", "sm"), t("action.new")))),
    h("div", { class: "toolbar" },
      viewToggle(S.view, [["graph", t("view.graph"), "graph"], ["list", t("view.list"), "list"], ["gantt", t("view.gantt"), "gantt"]], (v) => {
        if (v === "gantt") ctx.setView("gantt"); else ctx.setView(v);
      }),
      h("span", { class: "count" }, `${S.total ?? S.units.length} ${t("overview.stats.total")}`),
      h("div", { class: "spacer" }),
      h("label", { class: "row", style: { fontSize: "11px", color: "var(--sub)", gap: "5px" } },
        h("input", {
          type: "checkbox", checked: S.filters.context ? true : undefined,
          onchange: (e) => ctx.setFilter("context", e.target.checked),
        }), t("map.context")),
      h("span", { class: "muted", style: { fontSize: "10px" } }, t("map.trace_hint"))),
    S.view === "graph" ? graphBody(ctx, S) :
      S.view === "gantt" ? ganttBody(ctx, S) : listBody(ctx, S));
  host.appendChild(page);
}

function graphBody(ctx, S) {
  const wrap = h("div", null);
  if (S.graph?.context_count) {
    wrap.appendChild(h("div", { class: "banner" }, icon("info", "sm"),
      h("div", null, t("graph.context_banner", { n: S.graph.context_count }))));
  }
  wrap.appendChild(graphPanel(ctx, S));
  return attachGraphHeight(wrap, ctx, ".graph-panel");
}

function ganttBody(ctx, S) {
  const items = S.timeline?.items || S.units || [];
  if (!items.some((u) => u.start_at || u.end_at)) {
    return emptyState(t("gantt.no_dates"));
  }
  const host = h("div");
  const gantt = new GanttView(host, {
    items,
    groupBy: S.ganttGroup || "type",
    zoom: S.ganttZoom || 1,
    onSelect: (unit, dbl, event) => ctx.openUnit(unit.uid, event ? { x: event.clientX, y: event.clientY } : null),
  });
  ctx.trackGantt(gantt);
  return host;
}

function listBody(ctx, S) {
  let prefs;
  try { prefs = JSON.parse(localStorage.getItem("ark.map.list.v1")); } catch {}
  prefs = normalizeMapListPrefs(prefs);
  const options = { locale: locale(), types: (S.vocab?.types || []).map(item => item.id), statuses: (S.vocab?.statuses || []).map(item => item.id) };
  let page = 0, groups = [];
  const list = h("div", { class: "stack map-list-items" });
  const pager = h("div", { class: "map-list-pager" });
  const update = () => {
    page = 0;
    try { localStorage.setItem("ark.map.list.v1", JSON.stringify(prefs)); } catch {}
    groups = groupMapUnits(sortMapUnits(S.units || [], prefs, options), prefs.group, options);
    direction.replaceChildren(icon(prefs.direction === "asc" ? "arrowUp" : "arrowDown", "sm"));
    direction.title = t(`map.list.${prefs.direction}`);
    direction.setAttribute("aria-label", direction.title);
    paint();
  };
  const sort = h("select", {
    class: "select", "aria-label": t("action.sort"), "data-map-list-sort": "1",
    onchange: () => { prefs.sort = sort.value; prefs.direction = defaultMapListDirection(prefs.sort); update(); },
  }, MAP_LIST_SORTS.map(([value, key]) => h("option", { value }, t(key))));
  sort.value = prefs.sort;
  const direction = h("button", {
    class: "icon-btn", type: "button", "data-map-list-direction": "1",
    onclick: () => { prefs.direction = prefs.direction === "asc" ? "desc" : "asc"; update(); },
  });
  const group = h("select", {
    class: "select", "aria-label": t("map.list.group"), "data-map-list-group": "1",
    onchange: () => { prefs.group = group.value; update(); },
  }, MAP_LIST_GROUPS.map(value => h("option", { value }, t(value === "none" ? "map.list.ungrouped" : value === "assignee" ? "assignee.label" : `common.${value}`))));
  group.value = prefs.group;
  const controls = h("div", { class: "map-list-controls" },
    h("label", null, t("action.sort"), sort), direction,
    h("label", null, t("map.list.group"), group));
  const wrap = h("div", { class: "map-list" }, controls, list, pager);
  function paint() {
    clear(list); clear(pager);
    const current = mapListPage(groups, page);
    page = current.page;
    if (!current.total) { list.appendChild(emptyState(t("map.empty"))); return; }
    for (const { key, items, total } of current.groups) {
      const label = prefs.group === "type" ? typeLabel(key)
        : prefs.group === "status" ? statusLabel(key)
          : prefs.group === "importance" ? impLabel(key)
            : key ? displayName(key) : t("assignee.none");
      const color = prefs.group === "type" ? typeColor(key) : prefs.group === "status" ? statusColor(key) : null;
      list.appendChild(h("section", { "data-map-list-section": key },
        prefs.group === "none" ? null : h("div", { class: "section-head" },
          h("h2", { class: "row" }, color ? h("i", { class: "map-list-group-dot", style: { background: color } }) : null, label),
          h("span", { class: "sub" }, String(total))),
        h("div", { class: "cards" }, items.map(unit => unitCard(unit, ctx, { onTag: tag => ctx.toggleFilterValue("tags", tag) })))));
    }
    if (current.pages <= 1) return;
    const move = next => { page = next; paint(); controls.scrollIntoView({ block: "nearest" }); };
    const button = (name, target, disabled, symbol) => h("button", {
      class: "icon-btn", type: "button", title: t(`map.list.${name}`), "aria-label": t(`map.list.${name}`), disabled,
      onclick: () => move(target),
    }, icon(symbol, "sm"));
    pager.append(
      h("span", { role: "status" }, `${current.start}\u2013${current.end} / ${current.total}`),
      button("first", 0, page === 0, "chevronsLeft"),
      button("previous", page - 1, page === 0, "chevronLeft"),
      h("span", null, `${page + 1} / ${current.pages}`),
      button("next", page + 1, page === current.pages - 1, "chevronRight"),
      button("last", current.pages - 1, page === current.pages - 1, "chevronsRight"));
  }
  update();
  return wrap;
}
