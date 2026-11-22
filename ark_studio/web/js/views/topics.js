// Research > Topics: nodes that hold a curated subgraph.  A topic shows the
// members it lists or that match its rule, minus the ones it excludes, in a
// narrative order on its own canvas; it chooses which of their links to show.
import {
  h, clear, emptyState, typeColor, typeBadge, statusPill, richText, fmtDate, relativeTime, typesetMath, typesetMermaid,
} from "../dom.js";
import { api, mediaUrl } from "../api.js";
import { t, typeLabel, statusLabel, relLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { filtersToParams } from "../state.js";
import { GraphView } from "../graph.js";
import { GanttView } from "../gantt.js";
import { galleryKind } from "../node-model.js";
import { mindGraphOptions } from "../mind.js";
import { openModal, viewToggle, attachGraphHeight } from "../components.js";
import { openNewTopic, openMemberPicker } from "../topic-actions.js";
import {
  layoutFromMembers, layoutChanges, visibleEdges, previewPoints, ruleParts, filterTopic,
  outlineMove, outlineDrop, dropPosition, foldable, foldLevel, subtreeIds, foldChord,
} from "../topic-model.js";

const SELECTED_KEY = "ark.topic.selected.v1";
const BODY_KEY = "ark.topic.body.v1";
const INFO_KEY = "ark.topic.info.v1";

function outlineBodyVisible() {
  try { return localStorage.getItem(BODY_KEY) === "true"; } catch { return false; }
}

function outlineInfoVisible() {
  try { return localStorage.getItem(INFO_KEY) !== "false"; } catch { return true; }
}

function rememberTopic(ctx, uid) {
  try { localStorage.setItem(`${SELECTED_KEY}:${ctx.S.project?.path || ""}`, uid || ""); } catch { /* ignore */ }
}

function rememberedTopic(ctx) {
  try { return localStorage.getItem(`${SELECTED_KEY}:${ctx.S.project?.path || ""}`) || ""; } catch { return ""; }
}

// Default topics keep their built-in English title; show it in the UI language.
const DEFAULT_TITLES = { story: "Story", related: "Related work", method: "Method", experiment: "Experiment" }; // i18n-ignore: stored titles of the built-in topics

export function topicTitle(topic) {
  const key = topic?.default || topic?.meta?.default;
  if (key && DEFAULT_TITLES[key] && topic.title === DEFAULT_TITLES[key]) return t(`nav.${key}`);
  return topic?.title || "";
}

export function renderTopics(host, ctx, { defaultKey = null } = {}) {
  const S = ctx.S;
  clear(host);
  const listHost = h("div", { class: "topic-bar-items", role: "tablist" }, h("div", { class: "muted" }, t("topic.loading")));
  const mainHost = h("section", { class: "topic-main" });
  const search = h("input", { class: "input topic-search", placeholder: t("filters.keyword"), value: S.filters.q || "" });
  const page = h("div", { class: "page topic-page" },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, t("nav.research")),
        h("h1", { class: "page-title" }, t("plan.topic.title")),
        h("p", { class: "page-sub" }, t("plan.topic.subtitle"))),
      h("div", { class: "head-actions" },
        search,
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("check", "sm"), t("assistant.title")),
        h("button", { class: "primary-btn", "data-topic-new": "1", onclick: () => openNewTopic(ctx) }, icon("plus", "sm"), t("topic.new")))),
    h("div", { class: "topic-layout" },
      h("div", { class: "topic-bar" }, listHost),
      mainHost));
  host.appendChild(page);

  let topics = [];
  const state = { graph: null, data: null, full: false, seq: 0, pendingLayout: null };
  const known = (ref) => topics.find((topic) => topic.uid === ref || String(topic.id) === String(ref));
  const markActive = () => {
    for (const el of listHost.querySelectorAll(".topic-pill")) {
      const on = el.dataset.topicCard === S.topicRef;
      el.classList.toggle("on", on);
      el.setAttribute("aria-selected", on ? "true" : "false");
    }
  };
  // Switching topics, views or history entries replaces only the topic area;
  // the page, the topic bar and the tab's data stay as they are.
  const open = (topic, { push = true, reload = false } = {}) => {
    const same = !reload && topic.uid === S.topicRef && state.data?.topic?.uid === topic.uid;
    S.topicRef = topic.uid;
    if (push) {
      const hash = topicHash(topic);
      if (location.hash !== hash) history.pushState(null, "", hash);
    }
    markActive();
    listHost.querySelector(".topic-pill.on")?.scrollIntoView({ block: "nearest", inline: "nearest" });
    if (!same) openTopicMain(ctx, mainHost, topic.uid, state);
  };
  ctx.topicPage = {
    mounted: () => page.isConnected,
    applyFilters: (options) => {
      search.value = S.filters.q || "";
      return state.applyFilters?.(options);
    },
    openRef: (ref, { keepTrail = false } = {}) => {
      if (!keepTrail) S.topicTrail = [];
      // An explicit open (Map, inspector, agent) refreshes the topic area.
      open(known(ref) || { uid: String(ref) }, { reload: true });
    },
    openRoute: (route) => {
      if (!topics.length) return false;
      let topic = route.sub && route.sub !== "topic"
        ? topics.find((item) => item.default === route.sub)
        : known(route.topicRef || S.topicRef || rememberedTopic(ctx));
      if (!topic && route.topicRef) {
        // A topic created after the bar loaded (e.g. by an agent): open it
        // directly and refresh the bar so its card appears.
        topic = { uid: route.topicRef };
        api.topics().then((data) => { topics = data.items || topics; paintList(); }).catch(() => {});
      }
      topic ||= topics[0];
      if (!topic) return false;
      S.topicTrail = [];
      open(topic, { push: false });
      return true;
    },
    setView: (view) => {
      if (view) S.topicView = view;
      openTopicMain(ctx, mainHost, S.topicRef, state, { reuse: true });
    },
    reload: async () => {
      try {
        topics = (await api.topics()).items || [];
        paintList();
      } catch { /* the topic area still reloads */ }
      openTopicMain(ctx, mainHost, S.topicRef, state);
    },
  };

  const paintList = () => {
    clear(listHost);
    if (!topics.length) {
      listHost.appendChild(h("div", { class: "topic-list-empty muted" }, t("topic.none_yet")));
      return;
    }
    let previousDefault = null;
    for (const topic of topics) {
      if (previousDefault && !topic.default) listHost.appendChild(h("span", { class: "topic-bar-divider", "aria-hidden": "true" }));
      previousDefault = Boolean(topic.default);
      listHost.appendChild(topicPill(topic, topic.uid === S.topicRef, () => { S.topicTrail = []; open(topic); }));
    }
    listHost.querySelector(".topic-pill.on")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  };
  let searchTimer;
  search.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      if (page.isConnected) ctx.setFilter("q", search.value.trim());
    }, 120);
  });

  state.onMissing = (missing) => {
    if (S.topicRef !== missing) return false;
    if (rememberedTopic(ctx) === missing) rememberTopic(ctx, "");
    const next = topics.find((topic) => topic.uid !== missing);
    S.topicRef = next?.uid || "";
    paintList();
    if (next) { open(next, { push: false }); return true; }
    clear(mainHost);
    mainHost.appendChild(emptyState(t("topic.empty_title"), t("topic.empty_hint"),
      h("button", { class: "btn primary", onclick: () => openNewTopic(ctx) }, t("topic.new"))));
    return true;
  };
  const load = async () => {
    try {
      topics = (await api.topics()).items || [];
    } catch (err) {
      clear(listHost);
      listHost.appendChild(h("div", { class: "muted" }, err.message));
      return;
    }
    let ref;
    if (defaultKey) {
      ref = topics.find((topic) => topic.default === defaultKey)?.uid || "";
    } else {
      ref = S.topicRef;
      if (!ref || !known(ref)) {
        const remembered = rememberedTopic(ctx);
        // A deep link to a nested or unknown topic is still tried directly.
        ref = ref || (known(remembered) ? remembered : topics[0]?.uid || "");
      }
    }
    S.topicRef = ref || "";
    paintList();
    if (!S.topicRef) {
      clear(mainHost);
      mainHost.appendChild(emptyState(t("topic.empty_title"), t("topic.empty_hint"),
        h("button", { class: "btn primary", onclick: () => openNewTopic(ctx) }, t("topic.new"))));
      return;
    }
    await openTopicMain(ctx, mainHost, S.topicRef, state);
  };
  load();
}

// Built-in topics keep their old addresses (#/research/story, ...).
function topicHash(topic) {
  const key = topic.default || topic.meta?.default;
  return key && DEFAULT_TITLES[key] ? `#/research/${key}` : `#/research/topic/${encodeURIComponent(topic.uid)}`;
}

function topicPill(topic, active, onOpen) {
  const preview = topic.preview || { members: [], edges: [] };
  return h("button", {
    type: "button", role: "tab", "aria-selected": active ? "true" : "false",
    class: `topic-pill ${active ? "on" : ""} ${topic.default ? "default" : ""}`.replace(/\s+/g, " ").trim(),
    dataset: { unitRef: topic.uid, topicCard: topic.uid, topicDefault: topic.default || "" },
    title: `${topic.title} · ${topic.uid}`, onclick: onOpen,
  },
    thumbnail(preview),
    h("div", { class: "topic-pill-text" },
      h("div", { class: "topic-pill-title" }, topicTitle(topic)),
      h("div", { class: "topic-pill-meta" },
        h("span", { class: "uid" }, topic.uid),
        h("span", null, t("topic.members_n", { n: topic.member_count ?? preview.members.length })),
        preview.rule ? h("span", { class: "topic-rule-badge", title: t("topic.rule_badge_hint") }, icon("filter", "sm")) : null),
      h("div", { class: "topic-pill-time muted" }, relativeTime(topic.updated_at))));
}

function thumbnail(preview) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 100 100");
  svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
  svg.setAttribute("class", "topic-thumb");
  svg.setAttribute("aria-hidden", "true");
  const points = previewPoints(preview.members || []);
  const at = new Map(points.map((p) => [p.id, { x: 10 + p.x * 80, y: 10 + p.y * 80 }]));
  for (const [a, b] of preview.edges || []) {
    const p = at.get(a), q = at.get(b);
    if (!p || !q) continue;
    const line = document.createElementNS(ns, "line");
    line.setAttribute("x1", p.x); line.setAttribute("y1", p.y);
    line.setAttribute("x2", q.x); line.setAttribute("y2", q.y);
    line.setAttribute("class", "topic-thumb-edge");
    svg.appendChild(line);
  }
  for (const point of points) {
    const p = at.get(point.id);
    const dot = document.createElementNS(ns, "circle");
    dot.setAttribute("cx", p.x); dot.setAttribute("cy", p.y);
    dot.setAttribute("r", points.length > 24 ? 2.2 : 3.4);
    dot.setAttribute("fill", typeColor(point.type));
    svg.appendChild(dot);
  }
  return svg;
}

async function openTopicMain(ctx, host, ref, state, { reuse = false } = {}) {
  const S = ctx.S;
  const view = ["canvas", "graph", "outline", "timeline"].includes(S.topicView) ? S.topicView : "canvas";
  const needsBody = view === "outline" && outlineBodyVisible();
  const seq = ++state.seq;
  let data = state.data;
  // Metadata-only outlines can reuse the graph's lightweight response.
  if (!(reuse && data?.topic?.uid === ref && (!needsBody || state.full))) {
    try {
      data = await api.topic(ref, { full: needsBody });
    } catch (err) {
      if (seq !== state.seq) return;
      // A remembered topic that no longer exists (deleted, or a demo that
      // was rebuilt): forget it and fall back to the first topic or the list.
      if ((err.status === 404 || /not found/i.test(err.message || "")) && state.onMissing?.(ref)) return;
      clear(host);
      host.appendChild(emptyState(t("topic.missing"), err.message));
      return;
    }
    if (seq !== state.seq) return;  // a newer switch won
    state.full = needsBody;
  }
  state.data = data;
  if (state.graph) {
    state.graph.destroy();
    ctx.untrackGraph?.(state.graph);
    state.graph = null;
  }
  const topic = data.topic;
  S.topicRef = topic.uid;
  // Keep the route in step without re-rendering the page.
  S.sub = topic.meta?.default && DEFAULT_TITLES[topic.meta.default] ? topic.meta.default : "topic";
  rememberTopic(ctx, topic.uid);
  // The URL always names the open topic so it can be shared or reloaded.
  const hash = topicHash(topic);
  if (location.hash.startsWith("#/research/") && location.hash !== hash) history.replaceState(null, "", hash);
  const previous = S.topicTrail || [];
  const back = previous.findIndex((item) => item.uid === topic.uid);
  const trail = S.topicTrail = (back >= 0 ? previous.slice(0, back) : previous).slice(-4);
  const design = async (body, { quiet = false } = {}) => {
    try {
      let next = await api.designTopic(topic.id, body);
      const full = Boolean(S.filters.q?.trim()) || (view === "outline" && outlineBodyVisible());
      if (full) next = await api.topic(topic.uid, { full: true });
      state.data = next;
      state.full = full;
      if (!quiet) paint(next);
      return next;
    } catch (err) {
      ctx.toast(err.message, "err");
      return null;
    }
  };

  const hasGraph = view === "canvas" || view === "graph";
  const linksMode = hasGraph && Boolean(S.topicLinksMode);
  const canvasHost = h("div", { class: "graph-host" });
  const panel = h("div", { class: "graph-panel topic-canvas" }, canvasHost);
  const outline = h("div", { class: "topic-outline" });
  const timeline = h("div", { class: "topic-timeline" });
  const article = h("div", { class: "topic-article-wrap" });
  const linkRow = h("div", { class: "topic-link-row" });
  const foot = h("div", { class: "topic-foot" });
  const counts = h("span", { class: "count" });

  const toolbar = h("div", { class: "toolbar topic-toolbar" },
    viewToggle(view, [["canvas", t("topic.view.canvas"), "layers"], ["graph", t("topic.view.graph"), "graph"],
      ["outline", t("topic.view.outline"), "list"], ["timeline", t("topic.view.timeline"), "gantt"]], (value) => {
      ctx.topicPage?.setView(value);
    }),
    counts,
    h("div", { class: "spacer" }),
    h("button", { class: "btn sm", "data-topic-add": "1", onclick: () => openMemberPicker(ctx, state.data, (ids) => design({ add: ids })) },
      icon("plus", "sm"), t("topic.add_nodes")),
    hasGraph ? h("button", {
      class: "btn sm ghost", title: t("topic.auto_layout_hint"),
      onclick: () => { state.graph?.autoLayout(); },
    }, icon("refresh", "sm"), t("topic.auto_layout")) : null);

  clear(host);
  host.append(
    h("div", { class: "topic-head" },
      trail.length ? h("div", { class: "topic-trail" },
        ...trail.map((item) => h("button", { class: "linklike", type: "button", onclick: () => ctx.openTopic(item.uid, { keepTrail: true }) }, item.title)),
        h("span", { class: "muted" }, "›")) : null,
      h("div", { class: "topic-title-row" },
        h("i", { class: "topic-glyph", style: { background: typeColor("topic") } }, icon("layers", "sm")),
        h("h2", { class: "topic-title", dataset: { unitRef: topic.uid }, title: t("topic.title_hint"),
          onclick: (event) => ctx.openUnit(topic.uid, { x: event.clientX, y: event.clientY }) }, topicTitle(topic)),
        h("span", { class: "uid" }, topic.uid),
        statusPill(topic.status)),
      topicBody(topic.body)),
    toolbar, ...(linksMode ? [linkRow] : []),
    view === "canvas"
      ? topicCanvasSplit(ctx, panel, outline)
      : view === "graph" ? attachGraphHeight(h("div", { class: "topic-split solo" }, panel), ctx, ".topic-canvas")
        : view === "timeline" ? timeline : article,
    foot);

  // ---------------------------------------------------------------- canvas
  let saveTimer = null;
  const flushLayout = async () => {
    saveTimer = null;
    const pending = state.pendingLayout;
    state.pendingLayout = null;
    if (!pending) return;
    const items = layoutChanges(state.data.members, pending);
    if (!items.length) return;
    for (const item of items) {
      const member = state.data.members.find((unit) => unit.id === item.unit);
      if (!member) continue;
      if ("x" in item) { member.member.x = item.x; member.member.y = item.y; }
      if ("w" in item) member.member.w = item.w;
    }
    try {
      await api.designTopic(topic.id, { items });
    } catch { /* read-only accounts can arrange locally; nothing to save */ }
  };
  if (hasGraph) {
    const base = mindGraphOptions(ctx);
    const graph = new GraphView(canvasHost, {
      ...base,
      layout: view === "graph" ? "force" : "node",
      persistLayout: false,
      cameraKey: `${ctx.S.project?.id || ""}:topic:${topic.uid}`,
      edgeFilterKey: `ark.edgeFilter.v1:topic:${topic.uid}`,
      showSteps: true,
      edgeLabels: true,
      isInspectorOpen: () => Boolean(S.inspectorPane !== false || S.drawerOpen),
      nodeLayoutStore: {
        load: () => layoutFromMembers(state.data.members),
        save: (layout) => {
          state.pendingLayout = layout;
          clearTimeout(saveTimer);
          saveTimer = setTimeout(flushLayout, 450);
        },
        reset: () => {
          for (const unit of state.data.members) { unit.member.x = null; unit.member.y = null; }
        },
      },
      onSelect: (node, dbl, event) => {
        if (node) ctx.openUnit(node.uid || node.id, event ? { x: event.clientX, y: event.clientY } : null);
      },
      onOpenTopic: (node) => {
        drillInto(ctx, topic, node);
      },
      // Deleting inside a topic takes the node out of the topic; the node
      // itself stays in the project.
      onDelete: async (nodes) => {
        const next = await design({ remove: nodes.map((node) => node.id) }, { quiet: true });
        if (!next) return false;
        ctx.toast(t("topic.removed_n", { n: nodes.length }), "ok");
        // The canvas already dropped the nodes; keep its undo history.
        setTimeout(() => paint(state.data, { canvas: false }), 0);
        return true;
      },
      // Undo after a removal puts the nodes back into this topic.
      // Rule matches come back through the rule; manual members are re-added.
      onRestoreNodes: async (nodes) => {
        const ids = nodes.map((node) => node.id);
        const after = await design({ restore: ids }, { quiet: true });
        const present = new Set((after?.members || []).map((unit) => unit.id));
        const missing = ids.filter((id) => !present.has(id));
        if (missing.length) await design({ add: missing });
        else if (after) paint(after);
      },
      onAddOrphan: async (world = {}) => {
        const created = await api.createUnit({ type: "note", title: t("mind.new_node"), status: "open", importance: "p2", origin: "topic" });
        await api.designTopic(topic.id, { add: [{ unit: created.unit.id, x: world.x, y: world.y }] });
        return { unit: created.unit, link: null, parentId: null, orphan: true, world };
      },
      // While choosing links a click shows/hides the link in this topic;
      // otherwise links behave as everywhere else (double-click: relation).
      onEdgeSelect: (edge) => {
        if (S.topicLinksMode) design(edge.hidden ? { show_links: [edge.id] } : { hide_links: [edge.id] });
      },
      onEdgeClick: (edge, source, target) => {
        if (!S.topicLinksMode) base.onEdgeClick?.(edge, source, target);
      },
      nodeMenuExtras: (nodes, node) => memberMenu(ctx, state, nodes, node, design, base.nodeMenuExtras),
      // Del / "Remove from topic" keeps the node in the project (undoable);
      // "Delete node" moves it to the recycle bin, as on the Map.
      deleteMenuItems: (nodes, { edit, removeSelected }) => [
        { label: t("topic.remove_from"), icon: "x", onClick: edit(() => removeSelected()) },
        {
          label: t("topic.delete_node"), icon: "trash",
          onClick: edit(async () => {
            if (await base.onDelete?.(nodes)) ctx.topicPage?.reload();
          }),
        },
      ],
    });
    ctx.trackGraph(graph);
    state.graph = graph;
  } else {
    state.graph = null;
  }

  let fullRequest = null;
  state.applyFilters = async ({ repaintRail = true, remote = false } = {}) => {
    if (seq !== state.seq || !host.isConnected) return;
    try {
      if (S.filters.q?.trim() && !state.full) {
        fullRequest ||= api.topic(topic.uid, { full: true });
        const full = await fullRequest;
        fullRequest = null;
        if (seq !== state.seq || !host.isConnected) return;
        state.data = full;
        state.full = true;
      }
      paint(state.data, { repaintRail, remote });
    } catch (err) {
      fullRequest = null;
      if (seq === state.seq) ctx.toast(err.message, "err");
    }
  };
  // `remote`: a change from another device; the graph keeps camera and positions.
  function paint(next, { canvas = true, repaintRail = true, remote = false } = {}) {
    const original = next;
    next = filterTopic(original, { ...filtersToParams(S.filters), context: S.filters.context }, S.session?.user || "");
    const members = next.members || [];
    const edges = next.edges || [];
    S.units = members;
    S.total = members.length;
    S.facets = next.facets;
    if (repaintRail) ctx.refreshFilters?.();
    const hiddenCount = edges.filter((edge) => edge.hidden).length;
    clear(counts);
    counts.append(t("topic.counts", { n: members.length, links: edges.length - hiddenCount }),
      hiddenCount ? h("span", { class: "muted" }, ` · ${t("topic.hidden_links", { n: hiddenCount })}`) : "");
    if (linksMode) paintLinkFilter(ctx, linkRow, edges, next.subgraph, design);
    if (view === "canvas") paintOutline(ctx, outline, next, design, original.members);
    if (view === "timeline") paintTimeline(ctx, timeline, next);
    if (view === "outline") paintArticle(ctx, article, next, {
      allMembers: original.members,
      full: state.full,
      loadFull: async () => {
        const full = await api.topic(topic.uid, { full: true });
        if (seq !== state.seq) return null;
        state.data = full;
        state.full = true;
        return full;
      },
    });
    paintFoot(foot, next, design);
    if (state.graph && canvas) {
      const initialized = Boolean(state.graph._fitted);
      state.graph.setData(members, visibleEdges(edges, { showHidden: Boolean(S.topicLinksMode) }),
        { selected: members.some((node) => node.id === state.graph.selected) ? state.graph.selected : null,
          preserveCamera: initialized, animate: initialized, warm: !initialized, remote: remote && initialized });
    }
  }
  await state.applyFilters();
  if (state.graph && !data.members.length) state.graph.emptyEl?.classList.remove("hidden");
}

function topicCanvasSplit(ctx, panel, outline) {
  const key = `ark.topicSplit.v1:${ctx.S.project?.path || "default"}`;
  let ratio = null;
  try {
    const saved = Number(localStorage.getItem(key));
    if (saved >= .2 && saved <= .8) ratio = saved;
  } catch { /* storage unavailable */ }
  const handle = h("div", { class: "topic-split-resize", role: "separator", tabindex: 0,
    "aria-orientation": "vertical", "aria-label": t("topic.resize_outline"),
    "aria-valuemin": "20", "aria-valuemax": "80", "aria-valuenow": String(Math.round((ratio ?? .65) * 100)) });
  const wrap = h("div", { class: "topic-split" }, panel, handle, outline);
  const apply = (value, save = false) => {
    const width = wrap.clientWidth - handle.offsetWidth;
    const min = width > 460 ? Math.max(.2, 240 / width) : .2;
    const max = width > 460 ? Math.min(.8, 1 - 220 / width) : .8;
    ratio = Math.max(min, Math.min(max, value));
    wrap.style.setProperty("--topic-graph-width", `${ratio}fr`);
    wrap.style.setProperty("--topic-outline-width", `${1 - ratio}fr`);
    handle.setAttribute("aria-valuenow", String(Math.round(ratio * 100)));
    if (save) try { localStorage.setItem(key, String(ratio)); } catch { /* storage unavailable */ }
  };
  if (ratio != null) apply(ratio);
  let drag = null;
  handle.addEventListener("pointerdown", event => {
    if (event.button !== 0) return;
    event.preventDefault();
    const width = wrap.clientWidth - handle.offsetWidth;
    drag = { id: event.pointerId, x: event.clientX, width, graph: panel.getBoundingClientRect().width };
    handle.setPointerCapture(event.pointerId);
    handle.focus();
  });
  handle.addEventListener("pointermove", event => {
    if (drag?.id !== event.pointerId) return;
    apply((drag.graph + event.clientX - drag.x) / Math.max(1, drag.width));
  });
  const finish = event => {
    if (drag?.id !== event.pointerId) return;
    drag = null;
    if (ratio != null) apply(ratio, true);
  };
  for (const name of ["pointerup", "pointercancel", "lostpointercapture"]) handle.addEventListener(name, finish);
  handle.addEventListener("keydown", event => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const current = panel.getBoundingClientRect().width / Math.max(1, wrap.clientWidth - handle.offsetWidth);
    apply(event.key === "Home" ? .2 : event.key === "End" ? .8 : current + (event.key === "ArrowLeft" ? -.025 : .025), true);
  });
  handle.addEventListener("dblclick", () => {
    ratio = null;
    wrap.style.removeProperty("--topic-graph-width");
    wrap.style.removeProperty("--topic-outline-width");
    handle.setAttribute("aria-valuenow", "65");
    try { localStorage.removeItem(key); } catch { /* storage unavailable */ }
  });
  return attachGraphHeight(wrap, ctx, ".topic-canvas");
}

// The introduction stays short above the canvas; long ones fold open.
function topicBody(body) {
  if (!String(body || "").trim()) return null;
  const text = h("div", { class: "topic-body read-body" }, richText(body));
  if (String(body).length < 220) return text;
  text.classList.add("clamped");
  const label = h("span", null, t("topic.more"));
  const toggle = h("button", { type: "button", "aria-expanded": "false" }, label, icon("chevron", "sm"));
  const wrap = h("div", { class: "topic-body-wrap" }, text, h("div", { class: "topic-body-expand" }, toggle));
  toggle.addEventListener("click", () => {
    const open = text.classList.toggle("clamped") === false;
    wrap.classList.toggle("open", open);
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
    label.textContent = t(open ? "topic.less" : "topic.more");
  });
  return wrap;
}

function memberMenu(ctx, state, nodes, node, design, baseExtras) {
  const items = [];
  if (nodes.length === 1) {
    items.push({
      label: t("topic.add_linked"), icon: "link",
      onClick: async () => {
        try {
          const unit = (await api.unit(nodes[0].uid || nodes[0].id)).unit;
          const inTopic = new Set(state.data.members.map((member) => member.id));
          const ids = [...(unit.links_out || []).map((link) => link.target?.id), ...(unit.links_in || []).map((link) => link.source?.id)]
            .filter((id) => id != null && !inTopic.has(id) && id !== state.data.topic.id);
          if (!ids.length) { ctx.toast(t("topic.no_linked"), "ok"); return; }
          await design({ add: [...new Set(ids)] });
          ctx.toast(t("topic.added", { n: new Set(ids).size, title: state.data.topic.title }), "ok");
        } catch (err) {
          ctx.toast(err.message, "err");
        }
      },
    });
  }
  return [...items, ...(baseExtras?.(nodes, node) || [])];
}

function paintLinkFilter(ctx, host, edges, subgraph, design) {
  clear(host);
  const present = [...new Set(edges.map((edge) => edge.relation))].sort();
  const shown = subgraph?.relations ? new Set(subgraph.relations) : null;
  host.append(h("span", { class: "topic-rule-label" }, icon("link", "sm"), t("topic.links_relations")));
  if (!present.length) host.append(h("span", { class: "muted" }, t("topic.no_links")));
  for (const relation of present) {
    const on = !shown || shown.has(relation);
    host.append(h("button", {
      type: "button", class: `chip ${on ? "on" : ""}`.trim(), "aria-pressed": on ? "true" : "false",
      dataset: { relation },
      onclick: () => {
        const current = shown ? new Set(shown) : new Set(present);
        if (on) current.delete(relation); else current.add(relation);
        const all = present.every((value) => current.has(value));
        design({ relations: all && !(subgraph?.relations || []).some((r) => !present.includes(r)) ? null : [...current] });
      },
    }, relLabel(relation), h("em", null, String(edges.filter((edge) => edge.relation === relation).length))));
  }
  host.append(h("span", { class: "muted topic-link-hint" }, t("topic.links_click_hint")));
  host.append(h("button", { class: "btn sm primary", type: "button", "data-topic-links-done": "1",
    onclick: () => { ctx.S.topicLinksMode = false; ctx.topicPage?.setView(); } }, t("topic.links_done")));
  if (subgraph?.hidden_links?.length) {
    host.append(h("button", { class: "linklike", type: "button", onclick: () => design({ hidden_links: [] }) },
      t("topic.links_show_all", { n: subgraph.hidden_links.length })));
  }
}

function paintOutline(ctx, host, data, design, allMembers = data.members || []) {
  clear(host);
  const members = data.members || [];
  host.append(h("div", { class: "topic-outline-head" },
    h("b", null, t("topic.narrative")),
    h("span", { class: "muted" }, t("topic.narrative_hint"))));
  if (!members.length) {
    host.append(h("div", { class: "muted topic-outline-empty" }, t(data.filtered ? "common.no_results" : "topic.no_members")));
    return;
  }
  const act = async (unit, op) => {
    const body = outlineMove(allMembers, unit.id, op);
    if (!body) return;
    await design(body.items.length ? body : { order: body.order });
    host.querySelector(`.topic-step[data-unit-ref="${CSS.escape(unit.uid)}"]`)?.focus();
  };
  const list = h("div", { class: "topic-steps topic-tree", role: "tree" });
  let dragId = null;
  const clearMarks = () => list.querySelectorAll(".drop-before, .drop-after, .drop-inside")
    .forEach((el) => el.classList.remove("drop-before", "drop-after", "drop-inside"));
  const positionOf = (event, row) => {
    const rect = row.getBoundingClientRect();
    return dropPosition((event.clientY - rect.top) / Math.max(1, rect.height));
  };
  for (const unit of members) {
    const m = unit.member || {};
    const row = h("div", {
      class: "topic-step topic-node", role: "treeitem", tabindex: 0, draggable: "true",
      "aria-level": String((m.depth || 0) + 1),
      dataset: { unitRef: unit.uid, depth: String(m.depth || 0) }, style: { "--depth": String(m.depth || 0) },
      title: t("topic.outline_keys_hint"),
      onkeydown: (event) => {
        if (event.target !== row) return;
        if (event.key === "Tab") { event.preventDefault(); act(unit, event.shiftKey ? "outdent" : "indent"); }
        else if (event.altKey && event.key === "ArrowUp") { event.preventDefault(); act(unit, "up"); }
        else if (event.altKey && event.key === "ArrowDown") { event.preventDefault(); act(unit, "down"); }
        else if (event.key === "Enter") ctx.openUnit(unit.uid);
      },
      ondragstart: (event) => {
        dragId = unit.id;
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", unit.uid);
        row.classList.add("dragging");
      },
      ondragend: () => { dragId = null; row.classList.remove("dragging"); clearMarks(); },
      ondragover: (event) => {
        if (dragId == null || dragId === unit.id) return;
        const position = positionOf(event, row);
        if (!outlineDrop(allMembers, dragId, unit.id, position)) { clearMarks(); return; }
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        clearMarks();
        row.classList.add(`drop-${position}`);
      },
      ondragleave: (event) => { if (!row.contains(event.relatedTarget)) row.classList.remove("drop-before", "drop-after", "drop-inside"); },
      ondrop: async (event) => {
        event.preventDefault();
        const body = dragId == null ? null : outlineDrop(allMembers, dragId, unit.id, positionOf(event, row));
        const moved = members.find((other) => other.id === dragId);
        clearMarks();
        dragId = null;
        if (!body) return;
        await design(body);
        if (moved) host.querySelector(`.topic-step[data-unit-ref="${CSS.escape(moved.uid)}"]`)?.focus();
      },
    },
      h("span", { class: "topic-step-grip", "aria-hidden": "true" }, "⋮⋮"),
      h("span", { class: `topic-step-no ${m.source === "rule" ? "rule" : ""}`.trim(),
        title: m.source === "rule" ? t("topic.from_rule") : t("topic.manual") }, m.path || String(m.step || "")),
      h("div", { class: "topic-step-body" },
        h("button", { class: "topic-step-title linklike", type: "button", onclick: () => ctx.openUnit(unit.uid) },
          h("i", { class: "topic-pick-dot", style: { background: typeColor(unit.type) } }), unit.title),
        h("div", { class: "topic-step-meta" },
          h("span", { class: "uid" }, unit.uid), h("span", null, typeLabel(unit.type)), h("span", null, statusLabel(unit.status)),
          m.parent_source === "link" ? h("span", { class: "muted", title: t("topic.parent_link_hint") }, t("topic.parent_link")) : null,
          unit.type === "topic" ? h("button", { class: "linklike", type: "button", onclick: () => drillInto(ctx, data.topic, unit) }, t("topic.open")) : null),
      ),
      h("button", { class: "icon-btn topic-step-remove", type: "button", title: t("topic.remove_from"),
        onclick: () => design({ remove: [unit.id] }) }, icon("x", "sm")));
    list.append(row);
  }
  host.append(list);
}

// ------------------------------------------------------------------ timeline
function paintTimeline(ctx, host, data) {
  clear(host);
  const items = (data.members || []).filter((unit) => unit.start_at || unit.end_at);
  if (!items.length) {
    host.append(data.filtered && !data.members.length ? emptyState(t("common.no_results"))
      : emptyState(t("topic.timeline_empty"), t("topic.timeline_empty_hint")));
    return;
  }
  const gantt = new GanttView(host, { items, groupBy: "type", onSelect: (unit) => ctx.openUnit(unit.uid) });
  ctx.trackGantt?.(gantt);
}

// ------------------------------------------------------------------- article
// The Outline view reads the topic as one document: each member is a section
// (heading by outline depth) holding its body, attachments and children.  A
// folded section shows only its title.  Folding follows VS Code: Ctrl+K Ctrl+0
// fold all, Ctrl+K Ctrl+J unfold all, Ctrl+K Ctrl+1..7 fold level N,
// Ctrl+K Ctrl+[ / ] fold / unfold recursively, Ctrl+K Ctrl+L toggle,
// Ctrl+Shift+[ / ] fold / unfold the focused section.
const FOLD_KEY = "ark.topic.fold.v1";

function loadFold(uid, members) {
  try {
    const saved = JSON.parse(localStorage.getItem(`${FOLD_KEY}:${uid}`) || "null");
    if (Array.isArray(saved)) return new Set(saved.filter((id) => members.some((unit) => unit.id === id)));
  } catch { /* ignore */ }
  return new Set(foldable(members));
}

function saveFold(uid, folded) {
  try { localStorage.setItem(`${FOLD_KEY}:${uid}`, JSON.stringify([...folded])); } catch { /* ignore */ }
}

function freeTags(unit) {
  return (unit.tags || []).filter((tag) => !/^(type|status|importance|needs|seq):/.test(tag));
}

function mediaGallery(unit) {
  const body = String(unit.body || "");
  // Attachments the body already embeds (media:N) are not repeated.
  const media = (unit.media || []).filter((item) => !body.includes(`media:${item.id}`));
  if (!media.length) return null;
  const gallery = h("div", { class: "topic-art-media" });
  for (const item of media) {
    const kind = galleryKind(item);
    const url = mediaUrl(item);
    const label = item.caption || item.url || "";
    let el;
    if (kind === "image") el = h("figure", { class: "topic-art-figure" }, h("img", { src: url, alt: label, loading: "lazy" }), label ? h("figcaption", null, label) : null);
    else if (kind === "video") el = h("figure", { class: "topic-art-figure" }, h("video", { src: url, controls: true, preload: "metadata" }), label ? h("figcaption", null, label) : null);
    else if (kind === "audio") el = h("figure", { class: "topic-art-figure" }, h("audio", { src: url, controls: true, preload: "metadata" }), label ? h("figcaption", null, label) : null);
    else if (kind === "pdf") el = h("figure", { class: "topic-art-figure" }, h("iframe", { src: url, class: "topic-art-frame", loading: "lazy", title: label }), h("figcaption", null, label));
    else if (kind === "html") el = h("figure", { class: "topic-art-figure" }, h("iframe", { src: url, class: "topic-art-frame", loading: "lazy", sandbox: "allow-scripts", title: label }), h("figcaption", null, label));
    else el = h("a", { class: "topic-art-file", href: url, target: "_blank", rel: "noopener" }, icon(kind === "link" ? "external" : "file", "sm"), label);
    gallery.append(el);
  }
  return gallery;
}

// Open a nested topic and keep the way back in the breadcrumb.
function drillInto(ctx, parentTopic, unit) {
  if (parentTopic) ctx.S.topicTrail = [...(ctx.S.topicTrail || []), { uid: parentTopic.uid, title: topicTitle(parentTopic) }];
  ctx.openTopic(unit.uid, { keepTrail: Boolean(parentTopic) });
}

function sectionContent(ctx, unit, parentTopic) {
  const facts = h("div", { class: "topic-art-facts" },
    typeBadge(unit.type), statusPill(unit.status), h("span", { class: "uid" }, unit.uid),
    unit.start_at || unit.end_at ? h("span", null, `${unit.start_at ? fmtDate(unit.start_at) : "…"} → ${unit.end_at ? fmtDate(unit.end_at) : "…"}`) : null,
    unit.assignee_effective ? h("span", null, `@${unit.assignee_effective}`) : null,
    ...freeTags(unit).slice(0, 8).map((tag) => h("span", { class: "tag static" }, tag)),
    h("button", { class: "linklike", type: "button", onclick: () => ctx.openUnit(unit.uid) }, t("topic.details")));
  const parts = [facts];
  if (unit.type === "topic") {
    // A nested topic reads as a link to its own document.
    parts.push(h("div", { class: "topic-art-nested" }, icon("layers", "sm"),
      t("topic.members_n", { n: unit.member_count ?? 0 }),
      h("button", { class: "btn sm", type: "button", onclick: () => drillInto(ctx, parentTopic, unit) }, t("topic.open"))));
  }
  return parts;
}

function sectionBody(unit) {
  const parts = [];
  if (String(unit.body || "").trim()) parts.push(h("div", { class: "read-body topic-art-body" }, richText(unit.body, { vizScope: `topic:${unit.uid}` })));
  const gallery = mediaGallery(unit);
  if (gallery) parts.push(gallery);
  return parts;
}

function paintArticle(ctx, host, data, { full, loadFull, allMembers = data.members || [] }) {
  clear(host);
  const members = data.members || [];
  const uid = data.topic.uid;
  if (!members.length) {
    host.append(h("div", { class: "muted topic-outline-empty" }, t(data.filtered ? "common.no_results" : "topic.no_members")));
    return;
  }
  let folded = loadFold(uid, allMembers);
  let showBody = outlineBodyVisible();
  let showInfo = outlineInfoVisible();
  let focusId = members[0].id;
  const sections = new Map();
  const doc = h("article", { class: "topic-article", "aria-label": topicTitle(data.topic) });
  const stack = [{ depth: -1, el: doc }];
  for (const unit of members) {
    const depth = unit.member?.depth || 0;
    while (stack[stack.length - 1].depth >= depth) stack.pop();
    const level = Math.min(6, depth + 2);
    const content = h("div", { class: "topic-art-content" });
    const info = h("div", { class: "topic-art-info", hidden: !showInfo });
    const body = h("div", { class: "topic-art-detail", hidden: !showBody });
    const children = h("div", { class: "topic-art-children" });
    const head = h("div", {
      class: "topic-art-head", tabindex: 0, role: "button",
      onclick: (event) => {
        if (event.target.closest("a,button:not(.topic-art-head)")) return;
        focusId = unit.id;
        run({ cmd: "toggle" });
      },
      onkeydown: (event) => {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); focusId = unit.id; run({ cmd: "toggle" }); }
      },
      onfocus: () => { focusId = unit.id; },
    },
      h("span", { class: "topic-art-chevron" }, icon("chevronRight", "sm")),
      h("span", { class: "topic-art-path" }, unit.member?.path || ""),
      h(`h${level}`, { class: "topic-art-title" }, unit.title),
      h("i", { class: "topic-pick-dot", style: { background: typeColor(unit.type) } }));
    const section = h("section", {
      class: "topic-art-section", dataset: { unitRef: unit.uid, depth: String(depth) },
      style: { "--depth": String(depth) },
    }, head, content, children);
    stack[stack.length - 1].el.append(section);
    stack.push({ depth, el: children });
    sections.set(unit.id, { section, head, content, info, body, unit, filled: false, bodyFilled: false });
  }
  const levels = Math.max(...members.map((unit) => (unit.member?.depth || 0) + 1));
  const levelButtons = Array.from({ length: Math.min(7, levels) }, (_, i) => h("button", {
    class: "btn sm ghost", type: "button", title: t("topic.fold_level_hint", { n: i + 1 }),
    onclick: () => run({ cmd: "showLevel", level: i + 1 }),
  }, `L${i + 1}`));
  const hint = h("span", { class: "topic-art-chord muted hidden" }, t("topic.chord_waiting"));
  const bodyToggle = h("input", { type: "checkbox", checked: showBody, "data-topic-display-body": "1" });
  const infoToggle = h("input", { type: "checkbox", checked: showInfo, "data-topic-display-info": "1",
    onchange: () => {
      showInfo = infoToggle.checked;
      try { localStorage.setItem(INFO_KEY, String(showInfo)); } catch { /* storage unavailable */ }
      apply();
    } });
  bodyToggle.addEventListener("change", async () => {
    bodyToggle.disabled = true;
    try {
      if (bodyToggle.checked && !full) {
        const loaded = await loadFull();
        if (!loaded || !doc.isConnected) return;
        const units = new Map(loaded.members.map(unit => [unit.id, unit]));
        for (const [id, item] of sections) item.unit = units.get(id) || item.unit;
        full = true;
      }
      showBody = bodyToggle.checked;
      try { localStorage.setItem(BODY_KEY, String(showBody)); } catch { /* storage unavailable */ }
      apply();
    } catch (err) {
      bodyToggle.checked = showBody;
      ctx.toast(err.message, "err");
    } finally { bodyToggle.disabled = false; }
  });
  host.append(
    h("div", { class: "topic-art-tools" },
      h("label", { class: "topic-display-body" }, bodyToggle, t("topic.display_body")),
      h("label", { class: "topic-display-body" }, infoToggle, t("topic.display_info")),
      h("button", { class: "btn sm", type: "button", "data-fold": "unfold", onclick: () => run({ cmd: "unfoldAll" }) }, icon("arrowDown", "sm"), t("topic.unfold_all")),
      h("button", { class: "btn sm", type: "button", "data-fold": "fold", onclick: () => run({ cmd: "foldAll" }) }, icon("arrowRight", "sm"), t("topic.fold_all")),
      ...levelButtons,
      h("span", { class: "muted topic-art-keys" }, t("topic.fold_keys")),
      hint),
    doc);

  function apply() {
    for (const [id, item] of sections) {
      const isFolded = folded.has(id);
      item.section.classList.toggle("folded", isFolded);
      item.head.setAttribute("aria-expanded", isFolded ? "false" : "true");
      if (!isFolded && !item.filled) {
        item.filled = true;
        item.info.append(...sectionContent(ctx, item.unit, data.topic));
        item.content.append(item.info, item.body);
      }
      item.info.hidden = !showInfo;
      item.body.hidden = !showBody;
      item.content.hidden = !showInfo && !showBody;
      if (!isFolded && showBody && full && !item.bodyFilled) {
        item.bodyFilled = true;
        item.body.append(...sectionBody(item.unit));
        try { typesetMath(item.body); typesetMermaid(item.body); } catch { /* optional renderers */ }
      }
    }
    saveFold(uid, folded);
  }

  function run(command) {
    const all = new Set(foldable(allMembers));
    if (command.cmd === "unfoldAll") folded = new Set();
    else if (command.cmd === "foldAll") folded = all;
    else if (command.cmd === "foldLevel") folded = foldLevel(members, folded, command.level);
    else if (command.cmd === "showLevel") {
      // Toolbar: everything above level N open, level N and deeper folded.
      folded = new Set(members.filter((unit) => (unit.member?.depth || 0) >= command.level - 1 && all.has(unit.id)).map((unit) => unit.id));
    } else if (command.cmd === "foldRecursive") for (const id of subtreeIds(members, focusId)) folded.add(id);
    else if (command.cmd === "unfoldRecursive") for (const id of subtreeIds(members, focusId)) folded.delete(id);
    else if (command.cmd === "fold") folded.add(focusId);
    else if (command.cmd === "unfold") folded.delete(focusId);
    else if (command.cmd === "toggle") {
      if (folded.has(focusId)) folded.delete(focusId); else folded.add(focusId);
    }
    folded = new Set([...folded].filter((id) => all.has(id)));
    apply();
  }

  let armed = null;
  const typing = (target) => target?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable='true'], .cm-editor");
  const onKey = (event) => {
    if (!doc.isConnected) { window.removeEventListener("keydown", onKey, true); return; }
    if (typing(event.target) || document.querySelector(".modal-backdrop, .palette-backdrop")) return;
    if (armed) {
      if (["Control", "Meta", "Shift", "Alt"].includes(event.key)) return; // i18n-ignore: key names
      event.preventDefault();
      event.stopImmediatePropagation();
      clearTimeout(armed);
      armed = null;
      hint.classList.add("hidden");
      const command = foldChord(event);
      if (command) run(command);
      return;
    }
    const mod = event.ctrlKey || event.metaKey;
    if (mod && !event.shiftKey && !event.altKey && (event.key === "k" || event.key === "K" || event.code === "KeyK")) {
      // Ctrl+K starts a fold chord here; alone it still opens the palette.
      event.preventDefault();
      event.stopImmediatePropagation();
      hint.classList.remove("hidden");
      armed = setTimeout(() => {
        armed = null;
        hint.classList.add("hidden");
        ctx.openPalette?.();
      }, 1000);
      return;
    }
    if (mod && event.shiftKey && (event.code === "BracketLeft" || event.code === "BracketRight")) {
      event.preventDefault();
      event.stopImmediatePropagation();
      run({ cmd: event.code === "BracketLeft" ? "fold" : "unfold" });
    }
  };
  window.addEventListener("keydown", onKey, true);
  apply();
}

function paintFoot(host, data, design) {
  clear(host);
  if (data.excluded?.length) {
    host.append(h("span", { class: "muted" }, t("topic.excluded", { n: data.excluded.length })),
      ...data.excluded.map((unit) => h("button", {
        class: "chip", type: "button", title: t("topic.restore_hint"),
        onclick: () => design({ restore: [unit.id] }),
      }, unit.title, " ↺")));
  }
  if (data.archived) host.append(h("span", { class: "muted" }, t("topic.archived_members", { n: data.archived })));
}

