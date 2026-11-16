// Workbench "Local graph" view (Obsidian style): the selected / open node
// and its neighbours up to N levels, drawn with the shared GraphView in a
// compact read-only mode (arrows, palette and personal overrides apply).
// It follows the node shown in Details (Map, Research, Topics …) and the
// node tab open in the Editor.
//
// The toolbar is the main graph's (layout picker, zoom, fit, help; folded
// into "…" when narrow) with one extra button: the neighbourhood popover
// (depth, link direction, relation and node-type filters) styled like the
// main graph's connection filter.  Layouts: Force, and Mind, which puts the
// focused node at the root and its neighbours on levels by distance.  Click
// a node to select it, double-click to open it in the Editor; in Mind,
// +/- and the branch toggles collapse/expand.

import { h, clear } from "./dom.js";
import { t, typeLabel, relLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { GraphView, getActiveGraphArrows } from "./graph.js";
import { relationReversed } from "./arrows.js";
import {
  LOCAL_GRAPH_STORE_KEY, LOCAL_GRAPH_CAP, LOCAL_LAYOUTS, normalizeOptions, neighbourhood, mindInput, relationsIn, typesIn, centerFor,
} from "./local-graph-model.js";

export const SELECTION_CHANGED = "ark:selection";
const SNAPSHOT_TTL = 30000;

function storedOptions() {
  try { return normalizeOptions(JSON.parse(localStorage.getItem(LOCAL_GRAPH_STORE_KEY) || "{}")); } catch { return normalizeOptions({}); }
}

export function createLocalGraphPanel(ctx) {
  let options = storedOptions();
  let snapshot = null;
  let snapshotProject = "";
  let snapshotAt = 0;
  let loading = null;
  let graph = null;
  let shownCenter = null;
  let shownLayout = "";
  let mindParents = new Map();
  let fitTimer = 0;
  const save = () => { try { localStorage.setItem(LOCAL_GRAPH_STORE_KEY, JSON.stringify(options)); } catch { /* storage disabled */ } };
  const filtered = () => options.hiddenRelations.length > 0 || options.hiddenTypes.length > 0
    || options.depth !== 1 || options.direction !== "both";

  // Neighbourhood popover (main graph connection-filter style).
  const pop = h("div", { class: "graph-edge-filter lg-pop hidden", role: "dialog", "aria-label": t("localgraph.filters") });
  const popBtn = h("button", {
    class: "lg-pop-btn", type: "button", title: t("localgraph.filters"), "aria-label": t("localgraph.filters"), "aria-expanded": "false",
    onclick: () => togglePop(),
  }, icon("filter", "sm"));
  const refreshBtn = h("button", {
    type: "button", title: t("action.refresh"), "aria-label": t("action.refresh"),
    onclick: () => { snapshotAt = 0; refresh(); },
  }, icon("refresh", "sm"));
  const notice = h("div", { class: "lg-notice", hidden: true });
  const empty = h("div", { class: "lg-empty" });
  const host = h("div", { class: "lg-graph" });
  const title = h("div", { class: "lg-center" });
  const root = h("div", { class: "lg-panel" }, title, notice, h("div", { class: "lg-stage" }, host, empty));

  document.addEventListener("pointerdown", (event) => {
    if (pop.classList.contains("hidden") || pop.contains(event.target) || popBtn.contains(event.target)) return;
    togglePop(false);
  }, true);

  function togglePop(force = null) {
    const show = force === null ? pop.classList.contains("hidden") : Boolean(force);
    pop.classList.toggle("hidden", !show);
    popBtn.setAttribute("aria-expanded", String(show));
    if (show) paintPop();
  }

  function setOption(patch) {
    options = normalizeOptions({ ...options, ...patch });
    save();
    paint({ fit: true });
    paintPop();
  }

  function paintPop() {
    popBtn.classList.toggle("on", filtered());
    if (pop.classList.contains("hidden")) return;
    clear(pop);
    const depth = h("input", { type: "range", min: "1", max: "3", step: "1", value: String(options.depth), "aria-label": t("localgraph.depth") });
    depth.addEventListener("change", () => setOption({ depth: Number(depth.value) }));
    depth.addEventListener("input", () => { depthValue.textContent = depth.value; });
    const depthValue = h("em", null, String(options.depth));
    const direction = h("select", { class: "input graph-primary-relation", "aria-label": t("localgraph.direction") },
      ["both", "out", "in"].map((id) => h("option", { value: id, selected: options.direction === id ? true : undefined }, t(`localgraph.dir.${id}`)))); // i18n-ignore: direction ids
    direction.addEventListener("change", () => setOption({ direction: direction.value }));
    const head = (text) => h("div", { class: "graph-edge-filter-head" }, h("b", null, text));
    const group = (values, hidden, key, label) => {
      const shown = values.filter((value) => !hidden.includes(value)).length;
      const all = h("input", { type: "checkbox", checked: shown === values.length ? true : undefined });
      all.indeterminate = shown > 0 && shown < values.length;
      all.addEventListener("change", () => setOption({ [key]: all.checked ? [] : values.slice() }));
      return [
        h("label", { class: "graph-edge-filter-row all" }, all, h("span", null, t("graph.edge_filter.select_all")), h("em", null, `${shown}/${values.length}`)),
        ...values.map((value) => {
          const box = h("input", { type: "checkbox", checked: !hidden.includes(value) ? true : undefined });
          box.addEventListener("change", () => {
            const set = new Set(options[key]);
            if (box.checked) set.delete(value); else set.add(value);
            setOption({ [key]: [...set] });
          });
          return h("label", { class: "graph-edge-filter-row" }, box, h("span", null, label(value)));
        }),
      ];
    };
    pop.append(
      head(t("localgraph.filters")),
      h("label", { class: "graph-edge-filter-primary lg-depth" }, h("span", null, t("localgraph.depth")), depth, depthValue),
      h("label", { class: "graph-edge-filter-primary" }, h("span", null, t("localgraph.direction")), direction),
      h("div", { class: "graph-edge-filter-note" }, t("localgraph.relations")),
      ...group(relationsIn(snapshot), options.hiddenRelations, "hiddenRelations", (value) => relLabel(value)),
      h("div", { class: "graph-edge-filter-note" }, t("localgraph.types")),
      ...group(typesIn(snapshot), options.hiddenTypes, "hiddenTypes", (value) => typeLabel(value)),
      h("button", {
        class: "graph-edge-filter-reset", type: "button",
        onclick: () => setOption({ depth: 1, direction: "both", hiddenRelations: [], hiddenTypes: [] }),
      }, t("localgraph.reset")));
  }

  async function ensureSnapshot() {
    const project = String(ctx.S.project?.id || "");
    if (snapshot && snapshotProject === project && Date.now() - snapshotAt < SNAPSHOT_TTL) return snapshot;
    if (!loading) {
      loading = api.graphSnapshot().then((data) => {
        snapshot = { nodes: data.nodes || [], edges: data.edges || [] };
        snapshotProject = project;
        snapshotAt = Date.now();
        return snapshot;
      }).finally(() => { loading = null; });
    }
    return loading;
  }

  function ensureGraph() {
    if (graph && host.isConnected) return graph;
    graph?.destroy?.();
    graph = new GraphView(host, {
      compact: true, compactLabels: true, readOnly: true, persistLayout: false, persistMode: false, cameraKey: null,
      layout: options.layout, allRelations: true, arrows: getActiveGraphArrows(),
      mindParents: () => mindParents,
      sidebarToolbar: {
        layouts: LOCAL_LAYOUTS,
        extra: [popBtn, refreshBtn],
        onLayout: (layout) => { setOption({ layout }); },
      },
      onSelect: (node, open) => {
        if (!node) return;
        if (open) ctx.openUnitInEditor?.(node);
        else ctx.openUnit?.(node.uid || node.id);
      },
    });
    host.append(pop);
    return graph;
  }

  function paint({ fit = false } = {}) {
    if (!root.isConnected) return;
    const center = centerFor(ctx.S, snapshot);
    const shownReversed = (relation) => relationReversed(getActiveGraphArrows(), relation);
    const data = snapshot && center !== null ? neighbourhood(snapshot, center, options, LOCAL_GRAPH_CAP, shownReversed) : null;
    if (!data?.center) {
      empty.hidden = false;
      empty.textContent = snapshot ? t("localgraph.empty") : t("action.loading");
      host.hidden = true;
      notice.hidden = true;
      clear(title);
      shownCenter = null;
      return;
    }
    empty.hidden = true;
    host.hidden = false;
    clear(title);
    title.append(h("span", { class: "type-badge" }, typeLabel(data.center.type)), h("b", null, data.center.title),
      h("span", { class: "lg-count" }, t("localgraph.count", { n: data.nodes.length, edges: data.edges.length })));
    title.title = `${data.center.uid} · ${data.center.title}`;
    notice.hidden = !data.truncated;
    notice.textContent = data.truncated ? t("localgraph.too_many", { n: LOCAL_GRAPH_CAP, total: data.total }) : "";
    const view = ensureGraph();
    const mind = options.layout === "mind";
    const input = mind ? mindInput(data) : data;
    mindParents = mind ? input.parents : new Map();
    const moved = shownCenter !== data.center.id || shownLayout !== options.layout;
    if (moved) view.collapsed?.clear?.();
    view.setData(input.nodes, input.edges, { selected: data.center.id, layout: options.layout, preserveCamera: !moved && !fit });
    view.setSelected(data.center.id);
    if (moved || fit) {
      // Fit now and again once the force layout has spread the nodes.
      requestAnimationFrame(() => view.fit({ animate: false }));
      clearTimeout(fitTimer);
      if (!mind) fitTimer = setTimeout(() => { if (shownCenter === data.center.id) view.fit({ animate: true }); }, 900);
    }
    shownCenter = data.center.id;
    shownLayout = options.layout;
    paintPop();
  }

  async function refresh(opts = {}) {
    if (!root.isConnected) return;
    try { await ensureSnapshot(); } catch { /* keep the old snapshot */ }
    paint(opts);
  }

  window.addEventListener(SELECTION_CHANGED, () => { if (root.isConnected && !root.closest("[hidden]")) refresh(); });
  window.addEventListener("ark:unit-saved", () => { snapshotAt = 0; });

  return {
    el: root,
    shown() { refresh({ fit: true }); },
    refresh,
  };
}
