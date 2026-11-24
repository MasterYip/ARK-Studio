// Ideas › Import from another ARK project.  The Import pane works as it does
// for InfoHub: the source graph on the left (read only), drag nodes onto
// this project's graph to import them (a copy), drop on a node to put them
// under it; the second filter tab holds ARK's own filters for the source.
// Server: /api/import-ark/{graph,unit,preview,apply} (routes_importark.go).

import { h, clear, richText, typeBadge, statusPill } from "./dom.js";
import { api } from "./api.js";
import { t, getLang } from "./i18n.js";
import { icon } from "./icons.js";
import { GraphView } from "./graph.js";
import { openModal, toast, renderFilterRail } from "./components.js";
import { emptyFilters, normalizeFilters, toggleTypeFilter, toggleStatusFilter, toggleImportanceFilter, RECENT_DAYS, baseTypes } from "./state.js";
import {
  INFOHUB_SOURCE, importSources, pickImportSource, isArkSource, importGraphParams, importRequest, importSummaryRows, markImported,
} from "./import-ark-model.js";
import { advancedEnabled } from "./advanced.js";
import { applyRailGroup } from "./filter-group-model.js";

const SOURCE_KEY = "ark.importSource";

function savedSource(projectId) {
  try { return localStorage.getItem(`${SOURCE_KEY}.${projectId}`) || ""; } catch { return ""; }
}

// The source list and the chosen source, kept on S.
export function syncImportSource(S) {
  const sources = importSources(S.projects, S.project?.id, { infohub: advancedEnabled() || S.importSource === INFOHUB_SOURCE });
  S.importSources = sources;
  S.importSource = pickImportSource(S.importSource || savedSource(S.project?.id), sources);
  S.importSourceName = sources.find((s) => s.id === S.importSource)?.name || "";
  return sources;
}

export function setImportSource(ctx, id) {
  const S = ctx.S;
  if (S.importSource === id) return;
  S.importSource = id;
  try { localStorage.setItem(`${SOURCE_KEY}.${S.project?.id}`, id); } catch { /* ignore */ }
  S.importFilters = emptyFilters();
  S.importData = null;
  S.filterTab = "infohub";
  ctx.refresh();
}

export function importSourceSelect(ctx) {
  const S = ctx.S;
  const select = h("select", {
    class: "select import-source", "aria-label": t("import_ark.source"),
    onchange: (event) => setImportSource(ctx, event.target.value),
  }, ...(S.importSources || []).map((s) => h("option", { value: s.id, selected: s.id === S.importSource ? true : undefined }, s.name)));
  select.value = S.importSource || "";
  return select;
}

// ------------------------------------------------------------------ pane
export function arkImportPane(ctx, S, getRightGraph) {
  const project = S.importSource;
  const host = h("div", { class: "graph-host ideas-graph" });
  const count = h("span", { class: "pill" }, "—");
  const importedNote = h("span", { class: "import-ark-imported", hidden: true });
  const children = h("input", {
    type: "checkbox", checked: S.importWithChildren ? true : undefined,
    onchange: (event) => { S.importWithChildren = event.target.checked; },
  });
  // Head: the source picker and the count; below, the note and the import
  // controls (the pane is half the page wide).
  const pane = h("section", { class: "ideas-pane infohub-pane import-ark-pane", "data-import-source": project },
    h("div", { class: "ideas-pane-head import-ark-head" },
      h("div", { class: "import-ark-row" },
        h("div", { class: "ideas-pane-title" }, importSourceSelect(ctx)), importedNote, count),
      h("div", { class: "import-ark-row" },
        h("div", { class: "ideas-pane-sub", title: t("import_ark.pane_note") }, t("import_ark.pane_note")),
        h("div", { class: "import-ark-actions" },
          h("label", { class: "import-children" }, children, h("span", null, t("import_ark.with_children"))),
          h("button", {
            class: "btn sm", type: "button", "data-import-filtered": "",
            onclick: () => openImportPreview(ctx, (S.importData?.nodes || []).filter((n) => !n.context).map((n) => n.uid)),
          }, icon("download", "sm"), t("import_ark.import_shown"))))),
    host);
  const graph = new GraphView(host, {
    cameraKey: `import-ark:${project}`,
    persistLayout: false,
    labelBudget: true,
    externalDrag: true,
    readOnly: true,
    foreign: true, // node ids of another project: no hover prefetch here
    onSelect: (node) => { if (node) openSourceUnit(ctx, project, node.uid, node.importedAs); },
    onExternalDrop: (nodes, event) => dropImport(ctx, project, nodes, event, getRightGraph()),
  });
  ctx.trackGraph(graph);
  let version = 0;
  async function load(preserveCamera = false) {
    const mine = ++version;
    try {
      const data = await api.importArkGraph(importGraphParams(project, S.importFilters));
      if (mine !== version) return;
      if (data.project?.id && data.project.id !== project) return; // a reply for another source
      const marked = markImported(data.nodes, data.imported);
      S.importData = { ...data, nodes: marked.nodes };
      S.importFacets = data.facets || {};
      clear(count);
      count.append(`${data.nodes.length}`);
      importedNote.hidden = !marked.count;
      importedNote.textContent = marked.count ? t("import_ark.imported_n", { n: marked.count }) : "";
      graph.setData(marked.nodes, data.edges, { selected: null, layout: "force", preserveCamera });
      repaintRail(ctx);
    } catch (err) {
      if (mine !== version) return;
      clear(host);
      host.appendChild(h("div", { class: "empty sm", style: { padding: "24px" } }, err.message));
    }
  }
  ctx.updateArkImportGraph = () => load(true);
  load(false);
  return pane;
}

async function dropImport(ctx, project, sourceNodes, event, right) {
  const nodes = (Array.isArray(sourceNodes) ? sourceNodes : [sourceNodes]).filter(Boolean);
  if (!nodes.length || !right) return;
  const withChildren = Boolean(ctx.S.importWithChildren);
  let result;
  try {
    result = await api.importArkApply(importRequest({ project, uids: nodes.map((n) => n.uid), withChildren, lang: getLang() }));
  } catch (err) { toast(err.message, "err"); return; }
  const rect = right.canvas.getBoundingClientRect();
  const base = right._world(event.clientX - rect.left, event.clientY - rect.top);
  const target = right._hit(event);
  const newUid = new Map((result.units || []).map((u) => [u.uid, u]));
  const created = new Map((result.created || []).map((u) => [u.uid, u]));
  for (let index = 0; index < nodes.length; index++) {
    const plan = newUid.get(nodes[index].uid);
    const unit = plan && created.get(plan.new_uid);
    if (!unit) continue;
    let link = null;
    if (target) {
      try { link = await api.createLink({ source: unit.id, target: target.id, relation: "part_of" }); } catch { /* the copy stays */ }
    }
    const angle = index * 2.399963, radius = index ? 48 + Math.floor(index / 6) * 24 : 0;
    right.addNodeAt(unit, { x: base.x + Math.cos(angle) * radius, y: base.y + Math.sin(angle) * radius },
      link ? { ...link, source: unit.id, target: target.id, relation: "part_of" } : null);
  }
  toast(result.imported ? t("import_ark.done", { n: result.imported, skipped: result.skipped || 0 }) : t("import_ark.already"), "ok");
  if (withChildren || (result.imported || 0) > nodes.length) ctx.refresh();
}

// A source node: read-only preview with Import buttons.  Its [[refs]] point
// into the source project.
async function openSourceUnit(ctx, project, uid, importedAs = "") {
  let data;
  try { data = await api.importArkUnit(project, uid); } catch (err) { toast(err.message, "err"); return; }
  const unit = data.unit;
  const body = richText(unit.body || "");
  for (const link of body.querySelectorAll?.("a.rich-unit-link[data-unit-ref]") || []) {
    if (!link.dataset.unitRef.includes("/")) link.dataset.unitRef = `${project}/${link.dataset.unitRef}`;
  }
  const run = (withChildren) => async (event, close) => {
    close(true);
    await importNow(ctx, project, [uid], withChildren);
  };
  openModal({
    title: unit.title, wide: true,
    body: h("div", { class: "import-ark-unit" },
      h("div", { class: "row tight" }, typeBadge(unit.type), h("code", null, unit.uid), statusPill(unit.status),
        h("span", { class: "muted" }, t("import_ark.from", { name: data.project?.name || project })),
        importedAs ? h("span", { class: "pill" }, t("import_ark.imported_as", { uid: importedAs })) : null),
      h("div", { class: "rich" }, body)),
    actions: [
      { label: t("action.close"), ghost: true }, { spacer: true },
      { label: t("import_ark.import_with_children"), close: false, onClick: run(true) },
      { label: t("import_ark.import_one"), primary: true, close: false, onClick: run(false) },
    ],
  });
}

async function importNow(ctx, project, uids, withChildren) {
  try {
    const result = await api.importArkApply(importRequest({ project, uids, withChildren, lang: getLang() }));
    toast(result.imported ? t("import_ark.done", { n: result.imported, skipped: result.skipped || 0 }) : t("import_ark.already"), "ok");
    ctx.refresh();
  } catch (err) { toast(err.message, "err"); }
}

// "Import shown…": preview what the current filters show, then import.
export async function openImportPreview(ctx, uids) {
  const S = ctx.S;
  const project = S.importSource;
  if (!uids.length) { toast(t("import_ark.nothing"), "err"); return; }
  const withChildren = Boolean(S.importWithChildren);
  let summary;
  try {
    summary = await api.importArkPreview(importRequest({ project, uids, withChildren, lang: getLang() }));
  } catch (err) { toast(err.message, "err"); return; }
  const list = (summary.units || []).slice(0, 100);
  openModal({
    title: t("import_ark.preview_title", { name: S.importSourceName || project }), wide: true,
    body: h("div", { class: "import-ark-preview" },
      h("ul", null, ...importSummaryRows(summary).map(([key, params]) => h("li", null, t(key, params)))),
      h("details", { open: list.length <= 12 }, h("summary", null, t("import_ark.list", { n: (summary.units || []).length })),
        h("ul", { class: "import-ark-list" }, ...list.map((u) => h("li", { class: u.skipped ? "muted" : "" },
          typeBadge(u.type), ` ${u.uid}${u.new_uid && u.new_uid !== u.uid ? ` → ${u.new_uid}` : ""} · ${u.title}`,
          u.skipped ? ` (${t("import_ark.skipped")})` : ""))))),
    actions: [
      { label: t("action.cancel"), ghost: true }, { spacer: true },
      {
        label: t("import_ark.import"), primary: true, disabled: !summary.unit_count, close: false,
        onClick: async (event, close) => { close(true); await importNow(ctx, project, uids, withChildren); },
      },
    ],
  });
}

// ------------------------------------------------------------------ rail
function repaintRail(ctx) {
  const rail = document.querySelector(".rail");
  if (!rail || ctx.S.tab !== "ideas" || ctx.S.filterTab !== "infohub" || !isArkSource(ctx.S.importSource)) return;
  renderFilterRail(rail, ctx); // keeps scroll, focus and open groups
}

// ARK's own filter rail, bound to the source's filters (S.importFilters).
export function renderArkImportRail(host, ctx) {
  const S = ctx.S;
  S.importFilters = normalizeFilters(S.importFilters || emptyFilters());
  const state = {
    filters: S.importFilters, facets: S.importFacets || {}, tab: "map", sub: null, filterTab: "ark",
    vocab: S.vocab, session: S.session, project: { id: S.importSource }, units: [], railOpen: S.railOpen,
  };
  const changed = () => { S.importFilters = state.filters; ctx.updateArkImportGraph?.(); };
  const proxy = {
    S: state,
    setFilter: (key, value) => { state.filters[key] = value; changed(); },
    setTimeMode: (mode) => { state.filters.timeMode = ["all", "timeline", "recent", "overdue"].includes(mode) ? mode : "all"; changed(); },
    setRecentDays: (days) => {
      state.filters.recentDays = RECENT_DAYS.includes(Number(days)) ? Number(days) : 7;
      state.filters.timeMode = "recent";
      changed();
    },
    setTimeRange: (from, to) => { state.filters.timeMode = "timeline"; state.filters.from = from || ""; state.filters.to = to || ""; changed(); },
    toggleFilterValue: (key, value) => {
      // Every type: the rail shows the Map scope, not this tab's.
      if (key === "types") toggleTypeFilter(state.filters, value, baseTypes("map"));
      else {
        const list = state.filters[key] || [];
        state.filters[key] = list.includes(value) ? list.filter((x) => x !== value) : [...list, value];
      }
      changed();
    },
    toggleAllTypes: (selected) => { state.filters.types = []; state.filters.typesNone = !selected; changed(); },
    toggleStatus: (status, base) => { toggleStatusFilter(state.filters, status, base); changed(); },
    toggleAllStatuses: (selected) => { state.filters.statuses = []; state.filters.statusesNone = !selected; changed(); },
    toggleImportance: (value, base) => { toggleImportanceFilter(state.filters, value, base); changed(); },
    toggleAllImportance: (selected) => { state.filters.importanceLevels = []; state.filters.importanceNone = !selected; changed(); },
    clearFilters: () => { state.filters = emptyFilters(); changed(); },
    applyFilterGroup: (group, action, base, value) => { applyRailGroup(state.filters, group, action, base, value); changed(); },
    closeFilters: () => ctx.closeFilters?.(),
  };
  renderFilterRail(host, proxy, { embedded: true });
}
