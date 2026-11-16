import { h, clear, emptyState, typeColor, fmtDate, relativeTime, dueState, percent } from "../dom.js";
import { api } from "../api.js";
import { t, typeLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { GraphView } from "../graph.js";
import { mindGraphOptions, preferredGraphLayout } from "../mind.js";
import { attachGraphHeight, unitCard, viewToggle } from "../components.js";
import { openInfoHubPreview, closeInfoHubPreview } from "../infohub-panel.js";

const KIND_COLORS = {topic: "#3b82f6", todo: "#ef4444", event: "#a855f7", decision: "#f59e0b",
  question: "#eab308", announcement: "#14b8a6", resource: "#10b981", digest: "#64748b", note: "#94a3b8"};

export function renderIdeas(host, ctx) {
  const S = ctx.S;
  ctx.updateInfoHubGraph = null;
  closeInfoHubPreview();
  clear(host);
  const ideas = S.units || [];
  const imported = ideas.filter((u) => u.origin === "infohub").length;
  const page = h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, `${t("nav.ideas")} · ${t("stage.capture")}`),
        h("h1", { class: "page-title" }, t("ideas.title")),
        h("p", { class: "page-sub" }, t("ideas.subtitle"))),
      h("div", { class: "head-actions" },
        h("button", { class: "ghost-btn", onclick: () => ctx.openExtract() }, icon("list", "sm"), t("ideas.extract")),
        h("button", {
          class: `ghost-btn info-toggle ${S.infoHubGraphVisible ? "on" : ""}`.trim(),
          "aria-pressed": S.infoHubGraphVisible ? "true" : "false",
          title: t("ideas.infohub_toggle_title"),
          onclick: () => ctx.toggleInfoHubGraph(),
        }, icon("database", "sm"), t("ideas.import")),
        h("button", { class: "primary-btn", onclick: () => ctx.openCreate({ type: "idea" }) }, icon("plus", "sm"), t("ideas.capture")))),
    toolbar(ctx, S, ideas.length, imported),
    S.view === "graph" ? ideasGraphSplit(ctx, S) : listPanel(ctx, S, ideas));
  host.appendChild(page);
}

function toolbar(ctx, S, count, imported = 0) {
  const sort = h("select", {
    class: "select", style: { width: "150px", height: "29px" }, value: S.sort || "updated",
    onchange: (e) => ctx.setSort(e.target.value),
  },
    h("option", { value: "updated" }, `${t("action.sort")}: ${t("common.updated")}`),
    h("option", { value: "importance" }, `${t("action.sort")}: ${t("common.importance")}`),
    h("option", { value: "created" }, `${t("action.sort")}: ${t("common.created")}`),
    h("option", { value: "title" }, `${t("action.sort")}: ${t("common.title")}`));
  sort.value = S.sort || "updated";
  return h("div", { class: "toolbar" },
    viewToggle(S.view, [["graph", t("view.graph"), "graph"], ["list", t("view.list"), "list"]], (v) => ctx.setView(v)),
    h("span", { class: "count" }, t("ideas.count", { n: S.total ?? count })),
    imported ? h("span", {
      class: "pill", title: t("ideas.imported", { n: imported }),
      style: { display: "inline-flex", alignItems: "center", gap: "4px" },
    }, icon("database", "sm"), t("ideas.infohub_count", { n: imported })) : null,
    S.graph?.context_count ? h("span", { class: "muted", style: { fontSize: "10px" } }, t("graph.context_nodes", { n: S.graph.context_count })) : null,
    h("div", { class: "spacer" }),
    S.view === "list" ? sort : null,
    h("button", { class: "btn sm ghost", onclick: () => ctx.openAssistant() }, icon("sparkles", "sm"), t("assistant.title")));
}

function graphPanel(ctx, S, onReady = null) {
  const panel = h("div", { class: "graph-panel" });
  const host = h("div", { class: "graph-host" });
  panel.appendChild(host);
  const graph = new GraphView(host, {
    cameraKey: `ideas:${S.view}`,
    nodeGraphKey: `ark.nodeGraph.v1:${ctx.S.project?.path || ""}:ideas:${S.view}`,
    isInspectorOpen: () => Boolean(ctx.S.inspectorPane !== false || ctx.S.drawerOpen),
    onSelect: (node, dbl, event) => { if (node) ctx.openUnit(node.uid || node.id, event ? { x: event.clientX, y: event.clientY } : null); },
    ...mindGraphOptions(ctx),
  });
  ctx.trackGraph(graph);
  onReady?.(graph);
  graph.setData(S.graph?.nodes || [], S.graph?.edges || [], { selected: null, layout: preferredGraphLayout("force") });
  const dropHint = h("div", { class: "graph-drop-hint hidden" }, t("ideas.infohub_drop"));
  host.appendChild(dropHint);
  let dragDepth = 0;
  const hasHub = (e) => Array.from(e.dataTransfer?.types || []).includes("application/x-ark-infohub");
  host.addEventListener("dragenter", (e) => {
    if (!hasHub(e)) return;
    e.preventDefault();
    dragDepth += 1;
    dropHint.classList.remove("hidden");
  });
  host.addEventListener("dragover", (e) => {
    if (!hasHub(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  });
  host.addEventListener("dragleave", (e) => {
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) dropHint.classList.add("hidden");
  });
  host.addEventListener("drop", async (e) => {
    if (!hasHub(e)) return;
    e.preventDefault();
    dragDepth = 0;
    dropHint.classList.add("hidden");
    let payload;
    try { payload = JSON.parse(e.dataTransfer.getData("application/x-ark-infohub")); } catch { return; }
    if (!payload?.key) return;
    const target = graph._hit(e);
    try {
      const result = await api.infoHubImport({ path: payload.path || S.infoHubPath, key: payload.key, as_idea: 1 });
      if (result.imported && target) {
        try {
          await api.createLink({ source: result.unit.id, target: target.id, relation: "part_of" });
        } catch { /* the imported node still exists */ }
      }
      ctx.toast(result.skipped ? t("ideas.infohub_already") : t("ideas.infohub_imported_one"), "ok");
      ctx.refresh();
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  });
  return panel;
}

function ideasGraphSplit(ctx, S) {
  let rightGraph = null;
  const rightPane = ideasArkPane(ctx, S, (graph) => { rightGraph = graph; });
  if (!S.infoHubGraphVisible) {
    return attachGraphHeight(h("div", { class: "ideas-graph-wrap" },
      h("div", { class: "ideas-split solo" }, rightPane)), ctx, ".ideas-split");
  }
  const leftPane = ideasInfoHubPane(ctx, S, () => rightGraph);
  const split = h("div", { class: "ideas-split" }, leftPane, rightPane);
  return attachGraphHeight(h("div", { class: "ideas-graph-wrap" }, split), ctx, ".ideas-split");
}

function paneHead(title, note, badge) {
  return h("div", { class: "ideas-pane-head" },
    h("div", null, h("div", { class: "ideas-pane-title" }, title), h("div", { class: "ideas-pane-sub" }, note)),
    badge ? h("span", { class: "pill" }, badge) : null);
}

function ideasArkPane(ctx, S, onReady) {
  const panel = graphPanel(ctx, S, onReady);
  panel.dataset.infohubDrop = "1";
  panel.classList.add("ark-drop-zone");
  return h("section", { class: "ideas-pane ark-pane" },
    paneHead(t("ideas.pane.ark_title"), t("ideas.pane.ark_note"), `${S.total ?? S.units.length}`),
    panel);
}

function ideasInfoHubPane(ctx, S, getRightGraph) {
  const host = h("div", { class: "graph-host ideas-graph" });
  const status = h("span", { class: "ih-graph-status" }, "—");
  const legend = h("div", { class: "ih-graph-legend" });
  const pane = h("section", { class: "ideas-pane infohub-pane" },
    paneHead(t("ideas.pane.infohub_title"), t("ideas.pane.infohub_note"), status),
    host);
  const graph = new GraphView(host, {
    cameraKey: "infohub:graph",
    persistLayout: false,
    labelBudget: true,
    externalDrag: true,
    readOnly: true,
    clipboardSource: "infohub",
    onSelect: (node, dbl, event) => {
      if (node) openInfoHubPreview(node, event ? { x: event.clientX, y: event.clientY } : null, S.infoHubPath);
    },
    onExternalDrop: async (sourceNodes, event) => {
      const right = getRightGraph();
      if (!right) return;
      const nodes = (Array.isArray(sourceNodes) ? sourceNodes : [sourceNodes]).filter(Boolean);
      if (!nodes.length) return;
      const rect = right.canvas.getBoundingClientRect();
      const base = right._world(event.clientX - rect.left, event.clientY - rect.top);
      const target = right._hit(event);
      let imported = 0;
      let skipped = 0;
      for (let index = 0; index < nodes.length; index++) {
        const node = nodes[index];
        const angle = index * 2.399963;
        const radius = index ? 48 + Math.floor(index / 6) * 24 : 0;
        const position = {
          x: base.x + Math.cos(angle) * radius,
          y: base.y + Math.sin(angle) * radius,
        };
        try {
          const result = await api.infoHubImport({ path: S.infoHubPath, key: node.uid, as_idea: 1 });
          let link = null;
          if (target) {
            try {
              link = await api.createLink({ source: result.unit.id, target: target.id, relation: "part_of" });
            } catch { /* keep the imported node even if the optional link fails */ }
          }
          right.addNodeAt(result.unit, position, link ? { ...link, source: result.unit.id, target: target.id, relation: "part_of" } : null);
          if (result.skipped) skipped++; else imported++;
        } catch (err) {
          ctx.toast(`${node.title || node.uid}: ${err.message}`, "err");
        }
      }
      if (nodes.length === 1) {
        ctx.toast(skipped ? t("ideas.infohub_already") : t("ideas.infohub_imported_one"), "ok");
      } else {
        ctx.toast(t("ideas.infohub_imported_many", { n: imported, skipped }), "ok");
      }
    },
  });
  ctx.trackGraph(graph);
  let loadVersion = 0;
  async function loadInfoHubGraph(preserveCamera = false) {
    const version = ++loadVersion;
    try {
      const filters = S.infoHubFilters || {};
      const data = await api.infoHubGraph({
        path: S.infoHubPath,
        q: filters.q || "",
        kind: (filters.kinds || []).join(","),
        tags: (filters.tags || []).join(","),
        status: filters.status || "",
        days: filters.days || 0,
        mine: filters.mine ? 1 : 0,
        roots: filters.roots ? 1 : 0,
        context: filters.context ? 1 : 0,
        order: filters.order || "activity",
        connected: filters.connected !== false ? 1 : 0,
        limit: 1000,
      });
      if (version !== loadVersion) return;
      S.infoHubPath = data.path;
      try { if (data.path) localStorage.setItem("ark.infoHubPath", data.path); } catch { /* ignore */ }
      S.infoHubData = data;
      if (data.max_days) S.infoHubMaxDays = data.max_days;
      clear(status);
      status.append(`${data.nodes.length}`);
      clear(legend);
      const counts = new Map();
      for (const node of data.nodes) counts.set(node.kind, (counts.get(node.kind) || 0) + 1);
      for (const item of data.kinds || []) {
        if (!counts.has(item.kind)) continue;
        legend.appendChild(h("span", null,
          h("i", { style: { background: KIND_COLORS[item.kind] || "#94a3b8" } }),
          item.kind, h("em", null, counts.get(item.kind))));
      }
      const coloredNodes = data.nodes.map((node) => ({
        ...node,
        customColor: KIND_COLORS[node.kind] || "#94a3b8",
      }));
      graph.setData(coloredNodes, data.edges, { selected: null, layout: "force", preserveCamera });
    } catch (err) {
      if (version !== loadVersion) return;
      if (!preserveCamera) {
        clear(host);
        host.appendChild(h("div", { class: "empty sm", style: { padding: "24px" } }, err.message));
      } else {
        ctx.toast(err.message, "err");
      }
    }
  }
  ctx.updateInfoHubGraph = () => loadInfoHubGraph(true);
  loadInfoHubGraph(false);
  return pane;
}

function listPanel(ctx, S, units) {
  const wrap = h("div", null);
  const sorted = sortUnits([...units], S.sort || "updated");
  if (!sorted.length) {
    wrap.appendChild(emptyState(t("ideas.empty"), t("ideas.empty_hint"),
      h("button", { class: "btn primary", onclick: () => ctx.openCreate({ type: "idea" }) }, t("ideas.add"))));
    return wrap;
  }
  const cards = h("div", { class: "cards" });
  for (const unit of sorted) {
    cards.appendChild(unitCard(unit, ctx, {
      onTag: (tag) => ctx.toggleFilterValue("tags", tag),
    }));
  }
  wrap.appendChild(cards);
  return wrap;
}

export function sortUnits(units, sort) {
  const rank = { p0: 0, p1: 1, p2: 2, p3: 3 };
  return units.sort((a, b) => {
    if (sort === "importance") return (rank[a.importance] ?? 9) - (rank[b.importance] ?? 9) || (b.updated_at || "").localeCompare(a.updated_at || "");
    if (sort === "created") return (b.created_at || "").localeCompare(a.created_at || "");
    if (sort === "title") return (a.title || "").localeCompare(b.title || "");
    if (sort === "start") return (a.start_at || "9999").localeCompare(b.start_at || "9999");
    return (b.updated_at || "").localeCompare(a.updated_at || "");
  });
}
