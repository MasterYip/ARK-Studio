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
// (tap) a node to open its floating preview (the same floating inspector
// preview as a node reference capsule; page level, not confined to this
// panel) without moving anything; double-click (double-tap) selects it in
// Details and re-centres here; Enter and the context menu select as before.
// Like the reference preview, it closes when it loses focus (a press
// outside it, focus moving elsewhere) or on Esc.  The last few centres get a fading accent ring.  In
// Mind, +/- and the branch toggles collapse/expand.
//
// The default depth and the history rings come from Settings › Appearance ›
// Graphs › Local graph (project default with the person's override); the
// toolbar choices (a depth picked here, direction, layout, filters) are view
// state kept in this browser.

import { h, clear } from "./dom.js";
import { t, typeLabel, relLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { createInspectorPreview, placeFloating } from "./inspector.js";
import { activationPoint } from "./floating-position.js";
import { GraphView, getActiveGraphArrows } from "./graph.js";
import { getMindSettings } from "./mind-settings.js";
import { relationReversed } from "./arrows.js";
import {
  LOCAL_GRAPH_STORE_KEY, LOCAL_GRAPH_STORE_KEY_V1, LOCAL_GRAPH_STORE_KEY_V3, loadLocalOptions, storableOptions, LOCAL_GRAPH_CAP, LOCAL_LAYOUTS, normalizeOptions, neighbourhood, mindInput, relationsIn, typesIn, centerFor,
  pushCenterHistory, centerRings, createTapClassifier, localGraphClickAction,
} from "./local-graph-model.js";

export const SELECTION_CHANGED = "ark:selection";
const SNAPSHOT_TTL = 30000;

const defaultDepth = () => getMindSettings().localDepth || 2;

function storedOptions() {
  try {
    return loadLocalOptions(localStorage.getItem(LOCAL_GRAPH_STORE_KEY_V3), localStorage.getItem(LOCAL_GRAPH_STORE_KEY),
      localStorage.getItem(LOCAL_GRAPH_STORE_KEY_V1), defaultDepth());
  } catch { return { options: normalizeOptions({}, defaultDepth()), depthChosen: false }; }
}

export function createLocalGraphPanel(ctx) {
  let { options, depthChosen } = storedOptions();
  let snapshot = null;
  let snapshotProject = "";
  let snapshotAt = 0;
  let loading = null;
  let graph = null;
  let shownCenter = null;
  let shownLayout = "";
  let mindParents = new Map();
  let fitTimer = 0;
  // Previous centres (most recent first) for the fading rings; reset when
  // the project changes.
  let centerHistory = [];
  let lastCenter = null;
  let historyProject = null;
  let cardNode = null;
  let cardNodeObj = null;
  let card = null;
  let cardRequest = null;
  let cardVersion = 0;
  const taps = createTapClassifier();
  const save = () => {
    try { localStorage.setItem(LOCAL_GRAPH_STORE_KEY_V3, JSON.stringify(storableOptions(options, depthChosen))); }
    catch { /* storage disabled */ }
  };
  const filtered = () => options.hiddenRelations.length > 0 || options.hiddenTypes.length > 0
    || options.depth !== defaultDepth() || options.direction !== "both";

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
  const host = h("div", { class: "lg-graph", "aria-description": t("localgraph.click_hint") });
  const title = h("div", { class: "lg-center" });
  const root = h("div", { class: "lg-panel" }, title, notice, h("div", { class: "lg-stage" }, host, empty));

  document.addEventListener("pointerdown", (event) => {
    // A press outside the preview closes it (a press on another node
    // reopens it there through the click).
    if (cardNode !== null && !card?.contains(event.target)) closeCard();
    if (pop.classList.contains("hidden") || pop.contains(event.target) || popBtn.contains(event.target)) return;
    togglePop(false);
  }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && cardNode !== null) closeCard();
  });
  // Focus leaving the preview (tabbing away, another window) closes it, as
  // for the node reference preview.
  document.addEventListener("focusout", (event) => {
    if (!card?.contains(event.target)) return;
    const next = event.relatedTarget;
    if (next && card.contains(next)) return;
    setTimeout(() => { if (card && !card.contains(document.activeElement) && !card.matches(":hover")) closeCard(); }, 0);
  });

  // The press selected the clicked node in the graph; the centre keeps its
  // highlight (nothing moved), the card shows which node was clicked.
  const keepCenterSelected = () => { if (graph && shownCenter !== null && graph.selected !== shownCenter) graph.setSelected(shownCenter); };

  async function openCard(node, event = null) {
    closeCard();
    cardNode = node.id;
    cardNodeObj = node;
    keepCenterSelected();
    let point = null;
    try { point = event?.type ? activationPoint(event) : null; } catch { point = null; }
    const current = ++cardVersion, controller = new AbortController();
    cardRequest = controller;
    try {
      const { unit } = await api.unit(node.uid || node.id, { signal: controller.signal });
      if (current !== cardVersion) return;
      card = createInspectorPreview(ctx, unit, point, () => closeCard());
      card.classList.add("lg-preview");
      if (!card.isConnected) document.getElementById("overlay-root")?.appendChild(card);
      placeFloating(card, point);
    } catch (error) {
      if (current === cardVersion && error?.name !== "AbortError") closeCard();
    } finally {
      if (current === cardVersion) cardRequest = null;
    }
  }

  function closeCard() {
    keepCenterSelected();
    cardVersion++;
    cardRequest?.abort(); cardRequest = null;
    card?.remove(); card = null;
    cardNode = null;
    cardNodeObj = null;
  }

  // Double click / double tap / Enter: the node goes to Details (which
  // re-centres this graph through the selection event).
  function recenter(node) {
    closeCard();
    ctx.openUnit?.(node.uid || node.id);
  }

  // Alt+Enter (as in the main graph): open in the Editor the node whose
  // card is shown, else the centre.
  function openInEditor(selected) {
    const node = cardNodeObj || selected;
    if (!node || !ctx.openUnitInEditor) return;
    closeCard();
    ctx.openUnitInEditor(node);
  }

  function onNodeSelect(node, open, event) {
    const now = typeof performance !== "undefined" ? performance.now() : Date.now();
    const action = localGraphClickAction(taps, node, open, event, now);
    if (action === "card") openCard(node, event);
    else if (action === "recenter") recenter(node);
    else if (action === "close") closeCard();
    // A swallowed dblclick still ran the graph's own focus(): the centre
    // keeps the highlight.
    else if (open) keepCenterSelected();
  }

  // The centre moved (any route: this graph, Details, the main graph,
  // search, the Editor): the old centre joins the history.
  function noteCenter(id) {
    const project = String(ctx.S.project?.id || "");
    if (project !== historyProject) {
      historyProject = project;
      centerHistory = [];
      lastCenter = null;
    }
    if (id === null || id === undefined || id === lastCenter) return;
    centerHistory = pushCenterHistory(centerHistory, lastCenter, id);
    lastCenter = id;
  }

  function togglePop(force = null) {
    const show = force === null ? pop.classList.contains("hidden") : Boolean(force);
    pop.classList.toggle("hidden", !show);
    popBtn.setAttribute("aria-expanded", String(show));
    if (show) paintPop();
  }

  function setOption(patch) {
    if ("depth" in patch) depthChosen = Number(patch.depth) !== defaultDepth();
    options = normalizeOptions({ ...options, ...patch }, defaultDepth());
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
        onclick: () => setOption({ depth: defaultDepth(), direction: "both", hiddenRelations: [], hiddenTypes: [] }),
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
      compact: true, localGraph: true, compactLabels: true, readOnly: true, persistLayout: false, persistMode: false, cameraKey: null,
      layout: options.layout, allRelations: true, arrows: getActiveGraphArrows(),
      mindParents: () => mindParents,
      sidebarToolbar: {
        layouts: LOCAL_LAYOUTS,
        extra: [popBtn, refreshBtn],
        onLayout: (layout) => { setOption({ layout }); },
      },
      onSelect: onNodeSelect,
      onOpenEditor: openInEditor,
    });
    host.append(pop);
    return graph;
  }

  function paint({ fit = false } = {}) {
    if (!root.isConnected) return;
    const center = centerFor(ctx.S, snapshot);
    if (snapshot && snapshotProject === String(ctx.S.project?.id || "")) noteCenter(center);
    const shownReversed = (relation) => relationReversed(getActiveGraphArrows(), relation);
    const data = snapshot && center !== null ? neighbourhood(snapshot, center, options, LOCAL_GRAPH_CAP, shownReversed) : null;
    if (!data?.center) {
      empty.hidden = false;
      empty.textContent = snapshot || !wantsCenter() ? t("localgraph.empty") : t("action.loading");
      host.hidden = true;
      notice.hidden = true;
      clear(title);
      closeCard();
      shownCenter = null;
      return;
    }
    empty.hidden = true;
    host.hidden = false;
    clear(title);
    title.append(h("span", { class: "type-badge" }, typeLabel(data.center.type)), h("b", null, data.center.title),
      h("span", { class: "lg-count", title: t("localgraph.click_hint") }, t("localgraph.count", { n: data.nodes.length, edges: data.edges.length })));
    title.title = `${data.center.uid} · ${data.center.title}`;
    notice.hidden = !data.truncated;
    notice.textContent = data.truncated ? t("localgraph.too_many", { n: LOCAL_GRAPH_CAP, total: data.total }) : "";
    const view = ensureGraph();
    const mind = options.layout === "mind";
    const input = mind ? mindInput(data) : data;
    mindParents = mind ? input.parents : new Map();
    const moved = shownCenter !== data.center.id || shownLayout !== options.layout;
    if (moved) { view.collapsed?.clear?.(); closeCard(); }
    view.rings = getMindSettings().localRings === false ? new Map() : centerRings(centerHistory, input.nodes, data.center.id);
    // Same centre (a save, a remote change, the inspector reopening): same
    // nodes and links are patched in place by setData (graph-change.js); new
    // or removed neighbours settle with the others held (remote).  Neither
    // moves the camera nor reheats the whole graph.
    const keep = !moved && !fit;
    view.setData(input.nodes, input.edges, { selected: data.center.id, layout: options.layout, preserveCamera: keep, remote: keep });
    view.setSelected(data.center.id);
    view._presenceChip?.update();
    if (moved || fit) {
      const layout = options.layout;
      const stillShown = () => graph === view && shownCenter === data.center.id
        && shownLayout === layout && !document.hidden && view.canvas.getClientRects().length > 0;
      requestAnimationFrame(() => { if (stillShown()) view.fit({ animate: false }); });
      clearTimeout(fitTimer);
      if (!mind) fitTimer = setTimeout(() => {
        if (stillShown() && !view._userInteracted) view.fit({ animate: true });
      }, 900);
    }
    shownCenter = data.center.id;
    shownLayout = options.layout;
    paintPop();
  }

  // Nothing selected (Home, a list): no centre to show, so the project graph
  // is not loaded for it (megabytes in a large project, parsed on the page).
  function wantsCenter() {
    return centerFor(ctx.S, null) !== null || String(ctx.S.editorKey || "").startsWith("unit:");
  }

  async function refresh(opts = {}) {
    if (!root.isConnected) return;
    if (wantsCenter()) {
      try { await ensureSnapshot(); } catch { /* keep the old snapshot */ }
    }
    paint(opts);
  }

  window.addEventListener(SELECTION_CHANGED, () => {
    // Steps taken while the panel is hidden still count.
    if (snapshot && snapshotProject === String(ctx.S.project?.id || "")) noteCenter(centerFor(ctx.S, snapshot));
    if (root.isConnected && !root.closest("[hidden]")) refresh();
  });
  window.addEventListener("ark:unit-saved", (event) => {
    snapshotAt = 0;
    // The saved node, if shown here, changes in place (no relayout or refit).
    const unit = event.detail?.unit;
    if (unit && graph && root.isConnected) graph.patchNode?.(unit);
  });
  // A new default depth applies unless a depth was picked here; rings
  // follow the setting.
  window.addEventListener("ark:graph-display", () => {
    if (!depthChosen) options = normalizeOptions({ ...options, depth: defaultDepth() }, defaultDepth());
    if (root.isConnected && !root.closest("[hidden]")) { paint(); paintPop(); }
  });

  return {
    el: root,
    shown() { refresh(); graph?._resumeCanvas?.(); },
    refresh,
  };
}
