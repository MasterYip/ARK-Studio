// Research > Topics: nodes that hold a curated subgraph.  A topic shows the
// members it lists or that match its rule, minus the ones it excludes, in a
// narrative order on its own graph; it chooses which of their links to show.
// The Topics workbench view (topic-explorer.js) lists topics, lines and the
// open topic's outline; without the workbench shell it is a drawer here.
import {
  h, clear, emptyState, typeColor, typeBadge, statusPill, richText, fmtDate, typesetMath, typesetMermaid,
} from "../dom.js";
import { api, mediaUrl } from "../api.js";
import { t, relLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { filtersToParams } from "../state.js";
import { GraphView } from "../graph.js";
import { GanttView } from "../gantt.js";
import { galleryKind } from "../node-model.js";
import { mindGraphOptions } from "../mind.js";
import { openModal, viewToggle, attachGraphHeight } from "../components.js";
import { openNewTopic, openMemberPicker } from "../topic-actions.js";
import {
  layoutFromMembers, layoutChanges, visibleEdges, ruleParts, filterTopic, plainShortBody,
  foldable, foldChord,
} from "../topic-model.js";
import { branchTopicEntries, lineLabel } from "../needs-model.js";
import { topicsExplorer, topicTitle, DEFAULT_TITLES, TOPICS_VIEW } from "../topic-explorer.js";
import { shellActive, revealView } from "../workbench.js";
import { outlineRows, foldCommand, ancestorKeys } from "../topic-explorer-model.js";
import { openLineRename } from "./settings-lines.js";

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

export { topicTitle };

// The Topics view comes forward once per visit to the page in a session.
let viewRevealed = false;

export function renderTopics(host, ctx, { defaultKey = null } = {}) {
  const S = ctx.S;
  clear(host);
  const mainHost = h("section", { class: "topic-main" });
  const search = h("input", { class: "input topic-search", placeholder: t("filters.keyword"), value: S.filters.q || "" });
  let topics = [];
  // Lines of work (GET /api/project/branches): each line is a topic, a stored
  // one when a topic's rule is its tag, else an automatic one ("auto").
  let lines = [];
  const state = { graph: null, data: null, full: false, seq: 0, pendingLayout: null, lineOf: new Map() };
  const lineTopic = (line) => ({ uid: line.ref, title: line.name, virtual: true, branch: line.name, updated_at: line.last,
    member_count: line.units, preview: { members: [], edges: [] }, line });
  const known = (ref) => topics.find((topic) => topic.uid === ref || String(topic.id) === String(ref))
    || lines.filter((line) => line.auto).map(lineTopic).find((topic) => topic.uid === ref);
  const explorer = topicsExplorer(ctx);
  state.explorer = explorer;
  // Without the workbench shell the view is a drawer on this page.
  const classic = !shellActive();
  const page = h("div", { class: "page topic-page" },
    h("div", { class: "topic-page-head" },
      h("h1", { class: "topic-page-title", title: t("plan.topic.subtitle") }, icon("layers", "sm"), t("plan.topic.title")),
      h("div", { class: "head-actions" },
        search,
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("check", "sm"), t("assistant.title")),
        h("button", { class: "primary-btn", "data-topic-new": "1", onclick: () => openNewTopic(ctx) }, icon("plus", "sm"), t("topic.new")),
        classic ? explorer.drawerButton() : null)),
    h("div", { class: "topic-layout" }, mainHost),
    ...(classic ? [explorer.el, explorer.backdrop] : []));
  host.appendChild(page);
  if (classic) explorer.toggleDrawer(false);
  else {
    ctx.reattachTopicsView?.();
    if (!viewRevealed) { viewRevealed = true; revealView(TOPICS_VIEW); }
  }

  const loadLines = async () => {
    try {
      const data = await api.branches();
      lines = branchTopicEntries(data.items || []);
      state.lineOf = new Map(lines.filter((line) => !line.auto && line.via === "rule").map((line) => [line.ref, line.tag]));
    } catch { lines = []; /* an older server has no lines */ }
  };
  const paintList = () => explorer.setTopics(topics, lines);
  // Switching topics, views or history entries replaces only the topic area;
  // the page, the explorer and the tab's data stay as they are.
  const open = (topic, { push = true, reload = false } = {}) => {
    const same = !reload && topic.uid === S.topicRef && state.data?.topic?.uid === topic.uid;
    S.topicRef = topic.uid;
    if (push) {
      const hash = topicHash(topic);
      if (location.hash !== hash) history.pushState(null, "", hash);
    }
    explorer.setActive(topic.uid);
    if (!same) openTopicMain(ctx, mainHost, topic.uid, state);
  };
  ctx.topicPage = {
    mounted: () => page.isConnected,
    // From the Topics view: a topic, or a line (a stored topic when the line
    // has one; a path that only holds other lines opens as a line too).
    openPicked: (topic) => { S.topicTrail = []; open(topic); },
    openLine: (row) => {
      S.topicTrail = [];
      if (row.line && !row.line.auto) open(known(row.line.ref) || { uid: row.line.ref });
      else if (row.line) open(lineTopic(row.line));
      else open({ uid: row.ref, title: row.key, virtual: true });
    },
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
        // A topic created after the list loaded (e.g. by an agent): open it
        // directly and refresh the list so it appears.
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
        [topics] = await Promise.all([api.topics().then((data) => data.items || []), loadLines()]);
        paintList();
      } catch { /* the topic area still reloads */ }
      openTopicMain(ctx, mainHost, S.topicRef, state);
    },
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
    explorer.setActive(S.topicRef);
    if (next) { open(next, { push: false }); return true; }
    explorer.setOutline(null);
    clear(mainHost);
    mainHost.appendChild(emptyState(t("topic.empty_title"), t("topic.empty_hint"),
      h("button", { class: "btn primary", onclick: () => openNewTopic(ctx) }, t("topic.new"))));
    return true;
  };
  const load = async () => {
    try {
      [topics] = await Promise.all([api.topics().then((data) => data.items || []), loadLines()]);
    } catch (err) {
      clear(mainHost);
      mainHost.appendChild(h("div", { class: "muted" }, err.message));
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
    explorer.setActive(S.topicRef);
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

// The graph's layout per topic: Force until another one (Node: the topic's
// saved arrangement, Tree, Mind) is picked in the graph toolbar.
const LAYOUT_KEY = "ark.topic.layout.v1";

function topicLayout(ctx, uid) {
  try {
    const saved = localStorage.getItem(`${LAYOUT_KEY}:${ctx.S.project?.path || ""}:${uid}`);
    return ["force", "tree", "mind", "node"].includes(saved) ? saved : "force";
  } catch { return "force"; }
}

function rememberTopicLayout(ctx, uid, layout) {
  try { localStorage.setItem(`${LAYOUT_KEY}:${ctx.S.project?.path || ""}:${uid}`, layout); } catch { /* storage unavailable */ }
}

// The document view: scroll to a member's section, unfolding the way to it
// (the topic's own uid: its level-0 section).
function revealSection(article, uid) {
  const section = article.querySelector(`.topic-art-section[data-unit-ref="${CSS.escape(uid)}"]`);
  if (!section) return;
  if (section.classList.contains("topic-art-root")) {
    section.classList.remove("folded");
    section.scrollIntoView({ block: "start", behavior: "smooth" });
    return;
  }
  section.dispatchEvent(new CustomEvent("topic-reveal", { bubbles: true }));
  section.querySelector(":scope > .topic-art-head")?.scrollIntoView({ block: "center", behavior: "smooth" });
}

// Built-in topics keep their old addresses (#/research/story, ...).
function topicHash(topic) {
  const key = topic.default || topic.meta?.default;
  return key && DEFAULT_TITLES[key] ? `#/research/${key}` : `#/research/topic/${encodeURIComponent(topic.uid)}`;
}

async function openTopicMain(ctx, host, ref, state, { reuse = false } = {}) {
  const S = ctx.S;
  // Views: the graph (default), the document (each member as a section with
  // its body) and the timeline.  The outline lives in the explorer.
  const view = { outline: "document", canvas: "graph" }[S.topicView] || (["graph", "document", "timeline"].includes(S.topicView) ? S.topicView : "graph");
  const needsBody = view === "document" && outlineBodyVisible();
  const seq = ++state.seq;
  let data = state.data;
  // Metadata-only outlines can reuse the graph's lightweight response.
  if (!(reuse && data?.topic?.uid === ref && (!needsBody || state.full))) {
    try {
      data = await api.topic(ref, { full: needsBody, exact: Boolean(S.lineExact) });
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
  // A line's topic (automatic, or stored with the line's tag as its rule):
  // adding and removing nodes edits their line tag; an automatic topic keeps
  // no layout of its own.
  const lineTag = topic.virtual ? topic.uid : state.lineOf?.get(topic.uid) || "";
  const design = async (body, { quiet = false } = {}) => {
    try {
      let next;
      if (lineTag && (body.add || body.remove || (topic.virtual && body.restore))) {
        await editLine(lineTag, body);
        next = await api.topic(topic.uid, { full: state.full, exact: Boolean(S.lineExact) });
        state.data = next;
        if (!quiet) paint(next);
        return next;
      }
      if (topic.virtual) return state.data;
      next = await api.designTopic(topic.id, body);
      const full = Boolean(S.filters.q?.trim()) || (view === "document" && outlineBodyVisible());
      if (full) next = await api.topic(topic.uid, { full: true, exact: Boolean(ctx.S.lineExact) });
      state.data = next;
      state.full = full;
      if (!quiet) paint(next);
      return next;
    } catch (err) {
      ctx.toast(err.message, "err");
      return null;
    }
  };

  const hasGraph = view === "graph";
  const linksMode = hasGraph && Boolean(S.topicLinksMode);
  const canvasHost = h("div", { class: "graph-host" });
  const panel = h("div", { class: "graph-panel topic-canvas" }, canvasHost);
  const timeline = h("div", { class: "topic-timeline" });
  const article = h("div", { class: "topic-article-wrap" });
  const linkRow = h("div", { class: "topic-link-row" });
  const foot = h("div", { class: "topic-foot" });
  const counts = h("span", { class: "count" });

  // Auto layout arranges the Node and Mind layouts; Force lays itself out.
  const layout = topicLayout(ctx, topic.uid);
  const autoLayoutBtn = hasGraph ? h("button", {
    class: `btn sm ghost ${["node", "mind"].includes(layout) ? "" : "hidden"}`.trim(), title: t("topic.auto_layout_hint"),
    dataset: { topicAutoLayout: "1" }, onclick: () => { state.graph?.autoLayout(); },
  }, icon("refresh", "sm"), t("topic.auto_layout")) : null;
  const toolbar = h("div", { class: "toolbar topic-toolbar" },
    viewToggle(view, [["graph", t("topic.view.graph"), "graph"],
      ["document", t("topic.view.document"), "book"], ["timeline", t("topic.view.timeline"), "gantt"]], (value) => {
      ctx.topicPage?.setView(value);
    }),
    counts,
    h("div", { class: "spacer" }),
    h("button", { class: "btn sm", "data-topic-add": "1", title: lineTag ? t("branch.add_hint", { tag: lineTag }) : undefined,
      onclick: () => openMemberPicker(ctx, state.data, (ids) => design({ add: ids })) },
    icon("plus", "sm"), lineTag ? t("branch.add_nodes") : t("topic.add_nodes")),
    autoLayoutBtn);

  clear(host);
  host.append(
    h("div", { class: "topic-head" },
      trail.length ? h("div", { class: "topic-trail" },
        ...trail.map((item) => h("button", { class: "linklike", type: "button", onclick: () => ctx.openTopic(item.uid, { keepTrail: true }) }, item.title)),
        h("span", { class: "muted" }, "›")) : null,
      h("div", { class: "topic-title-row" },
        h("i", { class: "topic-glyph", style: { background: typeColor("topic") } }, icon("layers", "sm")),
        topic.virtual
          ? h("h2", { class: "topic-title line", title: t("branch.auto_hint") }, topicTitle(topic))
          : h("h2", { class: "topic-title", dataset: { unitRef: topic.uid }, title: t("topic.title_hint"),
            onclick: (event) => ctx.openUnit(topic.uid, { x: event.clientX, y: event.clientY }) }, topicTitle(topic)),
        h("span", { class: "uid" }, topic.uid),
        topic.virtual ? h("span", { class: "topic-auto-badge", title: t("branch.auto_hint") }, t("branch.auto"))
          : lineTag ? h("span", { class: "topic-line-badge", title: lineTag }, t("branch.line")) : null,
        topic.virtual ? null : statusPill(topic.status),
        topic.virtual ? h("label", { class: "row tight topic-line-exact", title: t("branch.exact_hint") },
          h("input", { type: "checkbox", checked: Boolean(S.lineExact),
            onchange: (event) => { S.lineExact = event.target.checked; ctx.topicPage?.openRef(topic.uid, { keepTrail: true }); } }),
          t("branch.exact")) : null,
        topic.virtual ? h("button", {
          class: "btn sm", type: "button", title: t("branch.pin_hint"),
          onclick: () => pinLine(ctx, topic, Boolean(S.lineExact)),
        }, icon("plus", "sm"), t("branch.pin")) : null,
        lineTag ? h("button", {
          class: "btn sm ghost", type: "button", title: t("lines.rename_hint"),
          onclick: () => openLineRename(ctx, lineTag.slice(lineTag.indexOf(":") + 1), () => ctx.topicPage?.reload()),
        }, t("lines.rename")) : null),
      headBody(topic)),
    toolbar, ...(linksMode ? [linkRow] : []),
    view === "graph" ? attachGraphHeight(h("div", { class: "topic-split solo" }, panel), ctx, ".topic-canvas")
      : view === "timeline" ? timeline : article,
    foot);

  // ---------------------------------------------------------------- canvas
  let saveTimer = null;
  const flushLayout = async () => {
    saveTimer = null;
    const pending = state.pendingLayout;
    state.pendingLayout = null;
    if (!pending || topic.virtual) return;
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
      layout,
      onLayoutChange: (next) => {
        rememberTopicLayout(ctx, topic.uid, next);
        autoLayoutBtn?.classList.toggle("hidden", !["node", "mind"].includes(next));
      },
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
        state.explorer?.markSelected(node?.uid || "");
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
        const created = await api.createUnit({ type: "note", title: t("mind.new_node"), status: "open", importance: "p2", origin: "topic",
          ...(lineTag ? { tags: [lineTag] } : {}) });
        if (!topic.virtual) await api.designTopic(topic.id, { add: [{ unit: created.unit.id, x: world.x, y: world.y }] });
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
      nodeMenuExtras: (nodes, node) => memberMenu(ctx, state, nodes, node, design, base.nodeMenuExtras, lineTag),
      // Del / "Remove from topic" keeps the node in the project (undoable);
      // "Delete node" moves it to the recycle bin, as on the Map.
      deleteMenuItems: (nodes, { edit, removeSelected }) => [
        { label: lineTag ? t("branch.remove_from") : t("topic.remove_from"), icon: "x", onClick: edit(() => removeSelected()) },
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
        fullRequest ||= api.topic(topic.uid, { full: true, exact: Boolean(ctx.S.lineExact) });
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
    state.explorer?.setOutline({
      topic, data: next, allMembers: original.members, design,
      drill: (unit) => drillInto(ctx, topic, unit),
      removeLabel: lineTag ? t("branch.remove_from") : t("topic.remove_from"),
      // The document view keeps its own Ctrl+K chords.
      ownChords: view === "document",
      locate: (uid) => {
        if (state.graph) state.graph.locate(uid);
        else if (view === "document") revealSection(article, uid);
      },
    });
    if (view === "timeline") paintTimeline(ctx, timeline, next);
    if (view === "document") paintArticle(ctx, article, next, {
      onSelect: (uid) => state.explorer?.markSelected(uid),
      allMembers: original.members,
      full: state.full,
      loadFull: async () => {
        const full = await api.topic(topic.uid, { full: true, exact: Boolean(ctx.S.lineExact) });
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

// The head shows the topic's body only when it is short plain text that
// fits in two lines at the current width (checked again on resize); the
// whole body is always level 0 of Document.
function headBody(topic) {
  if (!plainShortBody(topic.body)) return null;
  const el = h("div", { class: "topic-head-body read-body" }, richText(topic.body));
  let width = -1;
  const observer = new ResizeObserver(() => fit());
  function fit() {
    const box = el.parentElement;
    if (!el.isConnected || !box) { observer.disconnect(); return; }
    if (box.clientWidth === width) return;
    width = box.clientWidth;
    el.hidden = false;
    const line = parseFloat(getComputedStyle(el).lineHeight) || 18;
    el.hidden = el.scrollHeight > line * 2 + 2;
  }
  requestAnimationFrame(() => {
    if (!el.parentElement) return;
    observer.observe(el.parentElement);
    fit();
  });
  return el;
}

// Level 0 of the document: the topic's own title and body, above the
// members' sections.  The body is edited in Node details (the title and
// the Edit button open it); a click on the heading folds the body.
function rootSection(ctx, topic) {
  const body = String(topic.body || "").trim();
  const content = h("div", { class: "topic-art-content topic-art-root-body" },
    body ? h("div", { class: "read-body topic-art-body" }, richText(topic.body, { vizScope: `topic:${topic.uid}` }))
      : topic.virtual ? null : h("div", { class: "muted" }, t("topic.no_intro")));
  const section = h("section", { class: "topic-art-section topic-art-root", dataset: { unitRef: topic.uid, depth: "-1" } });
  const head = h("div", {
    class: "topic-art-head", tabindex: 0, role: "button", "aria-expanded": "true",
    onclick: (event) => {
      if (event.target.closest?.("a, button")) return;
      const folded = section.classList.toggle("folded");
      head.setAttribute("aria-expanded", folded ? "false" : "true");
    },
    onkeydown: (event) => {
      if (event.target !== head || event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === "Enter" && !topic.virtual) { event.preventDefault(); ctx.openUnit(topic.uid); }
      else if (event.key === " ") { event.preventDefault(); head.click(); }
    },
  },
    h("span", { class: "topic-art-chevron" }, icon("chevronRight", "sm")),
    h("h1", { class: "topic-art-title" }, topicTitle(topic)),
    topic.virtual ? null : h("button", {
      class: "btn sm ghost topic-art-edit", type: "button", title: t("topic.title_hint"), dataset: { topicEditBody: "1" },
      onclick: (event) => ctx.openUnit(topic.uid, { x: event.clientX, y: event.clientY }),
    }, icon("edit", "sm"), t("action.edit")));
  section.append(head, content);
  try { typesetMath(content); typesetMermaid(content); } catch { /* optional renderers */ }
  return section;
}

function memberMenu(ctx, state, nodes, node, design, baseExtras, lineTag = "") {
  const items = [];
  if (lineTag) {
    items.push({
      label: t("branch.remove_from"), icon: "x",
      onClick: async () => {
        if (await design({ remove: nodes.map((item) => item.id) })) ctx.toast(t("branch.removed_n", { n: nodes.length, tag: lineTag }), "ok");
      },
    });
  }
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

// Line topics: adding a node gives it the line's tag, removing takes the tag
// away (POST /api/units/batch); the nodes themselves stay.
async function editLine(tag, body) {
  const ids = (list) => [...new Set((list || []).map((item) => (item && typeof item === "object" ? item.unit : item)).filter((id) => id != null))];
  const add = [...ids(body.add), ...ids(body.restore)];
  const remove = ids(body.remove);
  if (add.length) await api.batchUpdate({ units: add, tags_add: [tag] });
  if (remove.length) await api.batchUpdate({ units: remove, tags_remove: [tag] });
}

// "Pin as topic": a stored topic whose rule is the line's tag; it grows with
// the line and can be edited like any topic.
async function pinLine(ctx, topic, exact = false) {
  try {
    // The rule keeps the lines below it unless "only this line" is on.
    const created = await api.createUnit({ type: "topic", title: lineLabel(topic.title), status: "open", importance: "p2",
      design: { rule: exact ? { tags: [topic.uid] } : { tag_trees: [topic.uid] } } });
    ctx.toast(t("branch.pinned", { title: topic.title }), "ok");
    await ctx.topicPage?.reload();
    ctx.openTopic(created.unit.uid);
  } catch (err) {
    ctx.toast(err.message, "err");
  }
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

function paintArticle(ctx, host, data, { full, loadFull, allMembers = data.members || [], onSelect = null }) {
  clear(host);
  const members = data.members || [];
  const uid = data.topic.uid;
  const root = rootSection(ctx, data.topic);
  if (!members.length) {
    host.append(h("article", { class: "topic-article" }, root,
      h("div", { class: "muted topic-outline-empty" }, t(data.filtered ? "common.no_results" : "topic.no_members"))));
    return;
  }
  let folded = loadFold(uid, allMembers);
  let showBody = outlineBodyVisible();
  let showInfo = outlineInfoVisible();
  let focusId = members[0].id;
  let lastToggle = null;
  const sections = new Map();
  const canFold = (id) => foldable(allMembers).has(id);
  // Links, chips and controls inside a heading keep their own clicks.
  const ownClick = (event) => Boolean(event.target.closest?.("a, button, input, select, textarea, .chip, .tag"));
  const select = (id, { notify = true } = {}) => {
    for (const [other, item] of sections) item.head.classList.toggle("selected", other === id);
    if (notify) onSelect?.(sections.get(id)?.unit.uid || "");
  };
  const doc = h("article", { class: "topic-article", "aria-label": topicTitle(data.topic) }, root);
  const stack = [{ depth: -1, el: doc }];
  for (const unit of members) {
    const depth = unit.member?.depth || 0;
    while (stack[stack.length - 1].depth >= depth) stack.pop();
    const level = Math.min(6, depth + 2);
    const content = h("div", { class: "topic-art-content" });
    const info = h("div", { class: "topic-art-info", hidden: !showInfo });
    const body = h("div", { class: "topic-art-detail", hidden: !showBody });
    const children = h("div", { class: "topic-art-children" });
    // Click toggles the fold at once (a row with nothing to fold is only
    // selected); double-click opens Node details instead of the Editor and
    // undoes the fold its first click made.  Enter: details, Space / ← / →:
    // fold, Alt+Enter and the menu still open the Editor.
    const head = h("div", {
      class: "topic-art-head", tabindex: 0, role: "button", title: t("topic.art_head_hint"),
      onclick: (event) => {
        if (ownClick(event)) return;
        focusId = unit.id;
        select(unit.id);
        if (event.detail > 1) return; // the second click of a double click
        if (canFold(unit.id)) { run({ cmd: "toggle" }); lastToggle = { id: unit.id, at: Date.now() }; }
      },
      ondblclick: (event) => {
        if (ownClick(event)) return;
        event.stopPropagation(); // not the global double-click-to-Editor
        event.preventDefault();
        window.getSelection?.()?.removeAllRanges?.();
        focusId = unit.id;
        if (lastToggle?.id === unit.id && Date.now() - lastToggle.at < 1000) run({ cmd: "toggle" });
        lastToggle = null;
        ctx.openUnit(unit.uid, { x: event.clientX, y: event.clientY });
      },
      onkeydown: (event) => {
        if (event.target !== head || event.ctrlKey || event.metaKey || event.altKey) return;
        focusId = unit.id;
        if (event.key === "Enter") { event.preventDefault(); select(unit.id); ctx.openUnit(unit.uid); }
        else if (event.key === " ") { event.preventDefault(); select(unit.id); run({ cmd: "toggle" }); }
        else if (event.key === "ArrowLeft") { event.preventDefault(); run({ cmd: "fold" }); }
        else if (event.key === "ArrowRight") { event.preventDefault(); run({ cmd: "unfold" }); }
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
    folded = foldCommand(outlineRows(members), folded, command, { foldable: foldable(allMembers), focus: focusId });
    apply();
  }

  // The explorer's outline asks for a section: unfold the way to it, select it.
  doc.addEventListener("topic-reveal", (event) => {
    const id = [...sections].find(([, item]) => item.section === event.target)?.[0];
    if (id == null) return;
    const up = ancestorKeys(outlineRows(members), id);
    if (up.some((key) => folded.has(key))) {
      for (const key of up) folded.delete(key);
      apply();
    }
    focusId = id;
    select(id, { notify: false });
  });

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

