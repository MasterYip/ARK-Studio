// The Topics view: a workbench view (left sidebar by default, movable like
// the others) with stacked sections like the Editor's Explorer: Topics
// (nested by part_of), Lines (branch: tags as a tree) and the open topic's
// Outline.  Without the workbench shell (narrow windows, phones) the same
// element is a drawer on the Research › Topics page.  One instance per app;
// the topic page feeds it the open topic (views/topics.js).  Sections and
// folds are kept in topic-explorer-model.js.
import { h, clear, typeColor } from "./dom.js";
import { api } from "./api.js";
import { t, typeLabel, statusLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { branchBadge } from "./needs-ui.js";
import { branchTopicEntries } from "./needs-model.js";
import { outlineMove, outlineDrop, dropPosition, foldChord } from "./topic-model.js";
import { openNewTopic } from "./topic-actions.js";
import { shellActive, isViewDisplayed } from "./workbench.js";
import {
  EXPLORER_KEY, parseExplorer, sections as model,
  topicTree, lineTree, visibleRows, searchRows, ancestorKeys, outlineRows, foldCommand,
  lineRowRef, lineAssociations, rowActive,
} from "./topic-explorer-model.js";

const OUTLINE_FOLD_KEY = "ark.topic.tree.fold.v1";
const SECTION_TITLES = { topics: "topic.ex.topics", lines: "topic.ex.lines", outline: "topic.ex.outline" };
export const TOPICS_VIEW = "topics";

// Without the shell the explorer is a drawer on the topic page.
const isClassic = () => !shellActive();

// Default topics keep their built-in English title; show it in the UI language.
export const DEFAULT_TITLES = { story: "Story", related: "Related work", method: "Method", experiment: "Experiment" }; // i18n-ignore: stored titles of the built-in topics

export function topicTitle(topic) {
  const key = topic?.default || topic?.meta?.default;
  if (key && DEFAULT_TITLES[key] && topic.title === DEFAULT_TITLES[key]) return t(`nav.${key}`);
  return topic?.title || "";
}

function loadOutlineFold(uid) {
  try {
    const saved = JSON.parse(localStorage.getItem(`${OUTLINE_FOLD_KEY}:${uid}`) || "null");
    if (Array.isArray(saved)) return new Set(saved);
  } catch { /* storage unavailable */ }
  return new Set();
}

function saveOutlineFold(uid, folded) {
  try { localStorage.setItem(`${OUTLINE_FOLD_KEY}:${uid}`, JSON.stringify([...folded])); } catch { /* storage unavailable */ }
}

let instance = null;

/**
 * The app's Topics view (created once).  Returns { el, backdrop,
 * drawerButton(), load(), sync(), setTopics(topics, lines), setActive(ref),
 * setOutline(outline | null), markSelected(uid), toggleDrawer(force), shown() }.
 */
export function topicsExplorer(ctx) {
  instance ||= createExplorer(ctx);
  return instance;
}

function createExplorer(ctx) {
  const S = ctx.S;
  let state = (() => {
    try { return parseExplorer(localStorage.getItem(EXPLORER_KEY)); } catch { return parseExplorer(""); }
  })();
  let drawerOpen = false;
  let loaded = "";         // project the lists belong to
  let loading = null;
  let query = "";
  let topics = [];
  let lines = [];
  let activeRef = "";
  let outline = null;      // { topic, data, allMembers, design, locate(uid), reveal(uid) }
  let outlineFolded = new Set();
  let rootFolded = false;  // the topic's own row (level 0)
  let outlineFocus = null; // unit id the per-row fold commands act on
  let selectedUid = "";
  let armed = null;
  const folded = { topics: new Set(state.folded.topics), lines: new Set(state.folded.lines) };

  const save = () => {
    state = { ...state, folded: { topics: [...folded.topics], lines: [...folded.lines] } };
    try { localStorage.setItem(EXPLORER_KEY, JSON.stringify(state)); } catch { /* storage unavailable */ }
  };

  // ------------------------------------------------------------ chrome
  const search = h("input", {
    class: "input topic-ex-search", type: "search", placeholder: t("topic.ex.filter"), "aria-label": t("topic.ex.filter"),
    oninput: () => { query = search.value; paintTopics(); paintLines(); paintOutline(); },
    onkeydown: (event) => { if (event.key === "Escape" && search.value) { event.stopPropagation(); search.value = ""; query = ""; paintAll(); } },
  });
  const chordHint = h("span", { class: "topic-ex-chord muted hidden" }, t("topic.chord_waiting"));
  // The drawer's own head; in the workbench the container header is the chrome.
  const closeBtn = h("button", {
    class: "icon-btn", type: "button", title: t("topic.ex.hide"), "aria-label": t("topic.ex.hide"),
    dataset: { topicExClose: "1" }, onclick: () => toggleDrawer(false),
  }, icon("x", "sm"));
  const wrap = h("div", { class: "ed-sections topic-ex-sections" });
  const bodies = {};
  const el = h("div", { class: "topic-explorer", "aria-label": t("topic.ex.title"), dataset: { topicExplorer: "1" } },
    h("div", { class: "topic-ex-head" },
      h("span", { class: "topic-ex-title" }, t("topic.ex.title")), h("span", { class: "spacer" }), closeBtn),
    h("div", { class: "topic-ex-filter" }, search, chordHint),
    wrap);
  const backdrop = h("div", { class: "topic-ex-backdrop", "aria-hidden": "true", onclick: () => toggleDrawer(false) });

  // Navigation: on the topic page the page swaps the topic in place;
  // elsewhere a click opens Research › Topics.
  const page = () => (ctx.topicPage?.mounted?.() ? ctx.topicPage : null);
  const afterPick = () => { if (isClassic()) toggleDrawer(false); };
  const hooks = {
    titleOf: topicTitle,
    openTopic: (topic) => { if (page()?.openPicked) page().openPicked(topic); else ctx.openTopic(topic.uid); afterPick(); },
    openLine: (row) => { if (page()?.openLine) page().openLine(row); else ctx.openTopic(row.ref); afterPick(); },
    newTopic: () => openNewTopic(ctx),
    lineSettings: () => ctx.openSettingsAt?.("lines"),
    drill: (unit) => outline?.drill?.(unit),
  };

  const sectionButton = (iconName, label, onClick, data = {}) => h("button", {
    class: "icon-btn", type: "button", title: t(label), "aria-label": t(label), dataset: data,
    onclick: (event) => { event.stopPropagation(); onClick(); },
  }, icon(iconName, "sm"));
  const actions = {
    topics: () => [
      sectionButton("plus", "topic.new", () => hooks.newTopic(), { topicNew: "1" }),
      sectionButton("collapseAll", "topic.fold_all", () => { foldAllRows("topics"); }),
    ],
    lines: () => [
      sectionButton("settings", "topic.ex.line_settings", () => hooks.lineSettings(), { settingsLines: "1" }),
      sectionButton("collapseAll", "topic.fold_all", () => { foldAllRows("lines"); }),
    ],
    outline: () => [
      sectionButton("collapseAll", "topic.fold_all", () => runOutline({ cmd: "foldAll" }), { outlineFold: "fold" }),
      sectionButton("expandAll", "topic.unfold_all", () => runOutline({ cmd: "unfoldAll" }), { outlineFold: "unfold" }),
    ],
  };
  model.ids.forEach((id, index) => {
    const toggleSec = h("button", {
      class: "ed-sec-toggle", type: "button", dataset: { secToggle: id },
      onclick: () => setSections(model.toggleSection(state.sections, id)),
    }, icon("chevron", "sm"), h("span", { class: "ed-sec-title" }, t(SECTION_TITLES[id])), h("span", { class: "topic-ex-sec-count" }));
    const max = h("button", {
      class: "icon-btn ed-sec-max", type: "button", dataset: { secMax: id },
      onclick: (event) => { event.stopPropagation(); setSections(model.toggleMaximized(state.sections, id)); },
    });
    const head = h("div", { class: "ed-sec-head" }, toggleSec, h("div", { class: "ed-sec-actions" }, ...actions[id](), max));
    head.addEventListener("dblclick", (event) => { if (!event.target.closest("button")) setSections(model.toggleMaximized(state.sections, id)); });
    const body = h("div", { class: "ed-sec-body" });
    bodies[id] = h("div", { class: "topic-ex-tree", role: "tree", "aria-label": t(SECTION_TITLES[id]) });
    body.append(bodies[id]);
    const section = h("section", { class: "ed-sec", dataset: { sec: id }, "aria-label": t(SECTION_TITLES[id]) });
    if (index > 0) section.append(sectionSash(id));
    section.append(head, body);
    wrap.append(section);
  });

  function setSections(next) {
    state = { ...state, sections: next };
    save();
    paintSections();
  }

  function sectionSash(id) {
    const line = h("div", { class: "ed-sec-sash", role: "separator", "aria-orientation": "horizontal", title: t("editor.section.resize") });
    line.addEventListener("pointerdown", (event) => {
      event.preventDefault();
      const heights = {};
      for (const sec of wrap.querySelectorAll(":scope > .ed-sec")) heights[sec.dataset.sec] = sec.getBoundingClientRect().height;
      const start = event.clientY;
      let last = null;
      line.setPointerCapture?.(event.pointerId);
      wrap.classList.add("resizing");
      const move = (ev) => {
        const sizes = model.resizeAt(state.sections, id, heights, ev.clientY - start);
        if (!sizes) return;
        last = sizes;
        state = { ...state, sections: { ...state.sections, sizes } };
        paintSections();
      };
      const up = () => {
        line.removeEventListener("pointermove", move);
        line.removeEventListener("pointerup", up);
        line.removeEventListener("pointercancel", up);
        wrap.classList.remove("resizing");
        if (last) setSections({ ...state.sections, sizes: last });
      };
      line.addEventListener("pointermove", move);
      line.addEventListener("pointerup", up);
      line.addEventListener("pointercancel", up);
    });
    return line;
  }

  function paintSections() {
    for (const item of model.sectionLayout(state.sections)) {
      const section = wrap.querySelector(`:scope > [data-sec="${item.id}"]`);
      if (!section) continue;
      section.classList.toggle("open", item.open);
      section.classList.toggle("collapsed", !item.open);
      section.style.flex = item.open ? `${item.weight} 1 0px` : "0 0 auto";
      section.querySelector(":scope > .ed-sec-head .ed-sec-toggle")?.setAttribute("aria-expanded", item.open ? "true" : "false");
      const max = section.querySelector(":scope > .ed-sec-head .ed-sec-max");
      const on = state.sections.max === item.id;
      const label = t(on ? "editor.section.restore" : "editor.section.maximize");
      max.title = label;
      max.setAttribute("aria-label", label);
      max.setAttribute("aria-pressed", on ? "true" : "false");
      max.replaceChildren(icon(on ? "minimize" : "maximize", "sm"));
      section.querySelector(":scope > .ed-sec-body").hidden = !item.open;
    }
  }

  // ------------------------------------------------------ shown, drawer
  function shown() {
    return isClassic() ? drawerOpen && el.isConnected : isViewDisplayed(TOPICS_VIEW);
  }

  let drawerButtonEl = null;
  function applyShown() {
    const host = el.closest(".topic-page");
    host?.classList.toggle("topic-ex-drawer-open", drawerOpen);
    el.classList.toggle("in-drawer", isClassic());
    drawerButtonEl?.setAttribute("aria-pressed", drawerOpen ? "true" : "false");
    drawerButtonEl?.classList.toggle("on", drawerOpen);
  }

  function toggleDrawer(force) {
    drawerOpen = typeof force === "boolean" ? force : !drawerOpen;
    if (drawerOpen) requestAnimationFrame(() => revealActive());
    applyShown();
  }

  /** The topic page's button for the drawer (classic layout only). */
  function drawerButton() {
    drawerButtonEl = h("button", {
      class: "ghost-btn topic-ex-toggle", type: "button", dataset: { topicExToggle: "1" },
      title: t("topic.ex.toggle"), "aria-label": t("topic.ex.toggle"), onclick: () => toggleDrawer(),
    }, icon("list", "sm"), h("span", { class: "label" }, t("topic.ex.title")));
    return drawerButtonEl;
  }

  // --------------------------------------------------------------- rows
  function treeRow({ depth, hasChildren, open, on, onFold, onClick, iconEl, name, count, extra = [], data = {}, title }) {
    const chevron = hasChildren
      ? h("span", {
        class: "topic-ex-chevron", role: "presentation",
        onclick: (event) => { event.stopPropagation(); onFold(); },
      }, icon(open ? "chevron" : "chevronRight", "sm"))
      : h("span", { class: "ed-tree-spacer" });
    return h("button", {
      class: `ed-tree-row topic-ex-row ${on ? "on" : ""}`.trim(), type: "button", role: "treeitem",
      "aria-level": String(depth + 1), "aria-selected": on ? "true" : "false",
      ...(hasChildren ? { "aria-expanded": open ? "true" : "false" } : {}),
      style: { paddingLeft: `${4 + depth * 12}px` }, dataset: data, title,
      onclick: onClick,
      onkeydown: (event) => {
        if (event.key === "ArrowRight" && hasChildren && !open) { event.preventDefault(); onFold(); }
        else if (event.key === "ArrowLeft" && hasChildren && open) { event.preventDefault(); onFold(); }
        else if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); moveFocus(event.target, event.key === "ArrowDown" ? 1 : -1); }
      },
    }, chevron, iconEl, h("span", { class: "name" }, name), ...extra,
    count != null ? h("span", { class: "topic-ex-count" }, String(count)) : null);
  }

  function moveFocus(row, delta) {
    const rows = [...row.closest(".topic-ex-tree").querySelectorAll(".topic-ex-row")];
    rows[rows.indexOf(row) + delta]?.focus();
  }

  function toggleFold(kind, key) {
    if (folded[kind].has(key)) folded[kind].delete(key); else folded[kind].add(key);
    save();
    if (kind === "topics") paintTopics(); else paintLines();
    bodies[kind].querySelector(`[data-key="${CSS.escape(String(key))}"]`)?.focus();
  }

  function foldAllRows(kind) {
    const rows = kind === "topics" ? topicTree(topics) : lineTree(lines);
    folded[kind] = new Set(rows.filter((row) => row.children).map((row) => row.key));
    save();
    if (kind === "topics") paintTopics(); else paintLines();
  }

  const sectionCount = (id, n) => {
    const elCount = wrap.querySelector(`[data-sec="${id}"] .topic-ex-sec-count`);
    if (elCount) elCount.textContent = n ? String(n) : "";
  };

  function paintTopics() {
    const host = bodies.topics;
    clear(host);
    const all = topicTree(topics);
    sectionCount("topics", topics.length);
    if (!all.length) { host.append(h("div", { class: "ed-tree-note" }, t("topic.none_yet"))); return; }
    const searching = Boolean(query.trim());
    const matched = searchRows(all, query, (row) => `${hooks.titleOf(row.topic)} ${row.topic.uid}`);
    const rows = searching ? matched : visibleRows(all, folded.topics);
    if (!rows.length) { host.append(h("div", { class: "ed-tree-note" }, t("common.no_results"))); return; }
    const { linesOf } = lineAssociations(lines);
    for (const row of rows) {
      const topic = row.topic;
      const open = searching || !folded.topics.has(row.key);
      // The lines associated with this topic; the badge opens the first.
      const linked = linesOf.get(topic.uid) || [];
      host.append(treeRow({
        depth: row.depth, hasChildren: row.children > 0, open, on: rowActive("topics", row, activeRef),
        onFold: () => toggleFold("topics", row.key),
        onClick: () => hooks.openTopic(topic),
        iconEl: h("i", { class: "topic-ex-glyph", style: { color: typeColor("topic") } }, icon("layers", "sm")),
        name: hooks.titleOf(topic),
        count: topic.member_count ?? topic.preview?.members?.length ?? null,
        extra: [
          topic.preview?.rule ? h("span", { class: "topic-rule-badge", title: t("topic.rule_badge_hint") }, icon("filter", "sm")) : null,
          linked.length ? linkChip(t("branch.line"), t("topic.ex.linked_line", { name: linked.map((line) => line.name).join(", ") }),
            () => hooks.openLine({ key: linked[0].name, line: linked[0], ref: lineRowRef({ line: linked[0] }) }),
            { topicLine: linked[0].name }) : null,
        ],
        data: { key: row.key, topicRow: topic.uid, unitRef: topic.uid, topicDefault: topic.default || "" },
        title: `${hooks.titleOf(topic)} · ${topic.uid}`,
      }));
    }
  }

  function paintLines() {
    const host = bodies.lines;
    clear(host);
    const all = lineTree(lines);
    sectionCount("lines", lines.length);
    const section = wrap.querySelector('[data-sec="lines"]');
    if (!all.length) {
      host.append(h("div", { class: "ed-tree-note" }, t("topic.ex.no_lines")));
      return;
    }
    section?.classList.remove("empty");
    const searching = Boolean(query.trim());
    const rows = searching ? searchRows(all, query, (row) => row.key) : visibleRows(all, folded.lines);
    if (!rows.length) { host.append(h("div", { class: "ed-tree-note" }, t("common.no_results"))); return; }
    for (const row of rows) {
      const line = row.line;
      const ref = lineRowRef(row, lines);
      const open = searching || !folded.lines.has(row.key);
      const topic = line?.topic || null;
      host.append(treeRow({
        depth: row.depth, hasChildren: row.children > 0, open, on: rowActive("lines", row, activeRef, lines),
        onFold: () => toggleFold("lines", row.key),
        onClick: () => hooks.openLine({ ...row, ref }),
        iconEl: icon("git", "sm"),
        name: row.label,
        count: row.count,
        extra: [
          topic ? linkChip(t("topic.ex.pinned"), t("topic.ex.linked_topic", { title: topic.title || topic.uid }),
            () => hooks.openTopic(topics.find((item) => item.uid === topic.uid) || { uid: topic.uid, title: topic.title }),
            { lineTopic: topic.uid }) : null,
          line ? branchBadge(line.status === "active" ? null : { status: line.status }) : null,
        ],
        data: { key: row.key, lineRow: row.key, lineRef: ref },
        title: line ? `${line.tag || ref} · ${t("topic.members_n", { n: line.units })}` : ref,
      }));
    }
  }

  // A badge on a row that opens the other side of a line/topic association
  // (a span: the row itself is a button).
  function linkChip(label, title, onOpen, data) {
    return h("span", {
      class: "topic-line-badge link", role: "link", title, dataset: data,
      onclick: (event) => { event.stopPropagation(); onOpen(); },
    }, label);
  }

  // ------------------------------------------------------------ outline
  function outlineAll() { return outline ? outlineRows(outline.data.members || []) : []; }

  function runOutline(command) {
    if (!outline) return;
    const rows = outlineAll();
    outlineFolded = foldCommand(rows, outlineFolded, command, { focus: outlineFocus });
    saveOutlineFold(outline.topic.uid, outlineFolded);
    paintOutline();
  }

  function paintOutline() {
    const host = bodies.outline;
    clear(host);
    if (!outline || !page()) {
      sectionCount("outline", 0);
      host.append(h("div", { class: "ed-tree-note" }, t(page() ? "topic.loading" : "topic.ex.no_outline")));
      return;
    }
    const members = outline.data.members || [];
    sectionCount("outline", members.length);
    // Level 0: the topic itself (its title and body, the first section of
    // the document); the members sit below it.
    const topic = outline.topic;
    const searching = Boolean(query.trim());
    host.append(treeRow({
      depth: 0, hasChildren: members.length > 0, open: searching || !rootFolded, on: selectedUid === topic.uid,
      onFold: () => { rootFolded = !rootFolded; paintOutline(); focusOutlineRow(topic.uid); },
      onClick: () => {
        select(topic.uid);
        outline.locate?.(topic.uid);
        afterPick();
        if (!topic.virtual) ctx.openUnit(topic.uid);
      },
      iconEl: h("i", { class: "topic-ex-glyph", style: { color: typeColor("topic") } }, icon("layers", "sm")),
      name: topicTitle(topic),
      data: { key: "root", outlineRow: topic.uid, outlineRoot: "1", depth: "-1" },
      title: `${topic.uid} · ${topicTitle(topic)}`,
    }));
    if (!members.length) {
      host.append(h("div", { class: "ed-tree-note" }, t(outline.data.filtered ? "common.no_results" : "topic.no_members")));
      return;
    }
    if (rootFolded && !searching) return;
    const all = outlineRows(members);
    const rows = searching
      ? searchRows(all, query, (row) => `${row.unit.title} ${row.unit.uid}`)
      : visibleRows(all, outlineFolded);
    if (!rows.length) { host.append(h("div", { class: "ed-tree-note" }, t("common.no_results"))); return; }
    const allMembers = outline.allMembers || members;
    let dragId = null;
    const clearMarks = () => host.querySelectorAll(".drop-before, .drop-after, .drop-inside")
      .forEach((item) => item.classList.remove("drop-before", "drop-after", "drop-inside"));
    const positionOf = (event, row) => {
      const rect = row.getBoundingClientRect();
      return dropPosition((event.clientY - rect.top) / Math.max(1, rect.height));
    };
    const act = async (unit, op) => {
      const body = outlineMove(allMembers, unit.id, op);
      if (!body) return;
      await outline.design(body.items.length ? body : { order: body.order });
      focusOutlineRow(unit.uid);
    };
    for (const row of rows) {
      const unit = row.unit;
      const m = unit.member || {};
      const open = searching || !outlineFolded.has(row.key);
      const item = treeRow({
        depth: row.depth + 1, hasChildren: row.children > 0, open, on: unit.uid === selectedUid,
        onFold: () => { outlineFocus = unit.id; runOutline({ cmd: "toggle" }); focusOutlineRow(unit.uid); },
        onClick: () => {
          outlineFocus = unit.id;
          select(unit.uid);
          outline.locate?.(unit.uid);
          afterPick();
          ctx.openUnit(unit.uid);
        },
        iconEl: h("span", { class: `topic-ex-step ${m.source === "rule" ? "rule" : ""}`.trim(),
          title: m.source === "rule" ? t("topic.from_rule") : t("topic.manual") }, m.path || String(m.step || "")),
        name: [h("i", { class: "topic-pick-dot", style: { background: typeColor(unit.type) } }), " ", unit.title],
        extra: [
          unit.type === "topic" ? h("span", {
            class: "topic-ex-open", role: "link", title: t("topic.open"),
            onclick: (event) => { event.stopPropagation(); hooks.drill(unit); },
          }, icon("arrowUpRight", "sm")) : null,
          h("span", {
            class: "topic-ex-remove", role: "presentation", title: outline.removeLabel || t("topic.remove_from"),
            onclick: (event) => { event.stopPropagation(); outline.design({ remove: [unit.id] }); },
          }, icon("x", "sm")),
        ],
        data: { key: String(unit.id), unitRef: unit.uid, outlineRow: unit.uid, depth: String(row.depth) },
        title: `${unit.uid} · ${typeLabel(unit.type)} · ${statusLabel(unit.status)}\n${t("topic.outline_keys_hint")}`,
      });
      item.draggable = !searching;
      item.addEventListener("focus", () => { outlineFocus = unit.id; });
      item.addEventListener("keydown", (event) => {
        if (event.target !== item) return;
        if (event.key === "Tab") { event.preventDefault(); act(unit, event.shiftKey ? "outdent" : "indent"); }
        else if (event.altKey && event.key === "ArrowUp") { event.preventDefault(); event.stopImmediatePropagation(); act(unit, "up"); }
        else if (event.altKey && event.key === "ArrowDown") { event.preventDefault(); event.stopImmediatePropagation(); act(unit, "down"); }
        else if (event.key === " ") { event.preventDefault(); outlineFocus = unit.id; runOutline({ cmd: "toggle" }); focusOutlineRow(unit.uid); }
      }, true);
      item.addEventListener("dragstart", (event) => {
        dragId = unit.id;
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", unit.uid);
        item.classList.add("dragging");
      });
      item.addEventListener("dragend", () => { dragId = null; item.classList.remove("dragging"); clearMarks(); });
      item.addEventListener("dragover", (event) => {
        if (dragId == null || dragId === unit.id) return;
        const position = positionOf(event, item);
        if (!outlineDrop(allMembers, dragId, unit.id, position)) { clearMarks(); return; }
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        clearMarks();
        item.classList.add(`drop-${position}`);
      });
      item.addEventListener("dragleave", (event) => {
        if (!item.contains(event.relatedTarget)) item.classList.remove("drop-before", "drop-after", "drop-inside");
      });
      item.addEventListener("drop", async (event) => {
        event.preventDefault();
        const body = dragId == null ? null : outlineDrop(allMembers, dragId, unit.id, positionOf(event, item));
        const moved = members.find((other) => other.id === dragId);
        clearMarks();
        dragId = null;
        if (!body) return;
        // The parent opens so the dropped row stays in sight.
        if (event.target && positionOf(event, item) === "inside") outlineFolded.delete(unit.id);
        await outline.design(body);
        if (moved) focusOutlineRow(moved.uid);
      });
      host.append(item);
    }
  }

  function focusOutlineRow(uid) {
    requestAnimationFrame(() => bodies.outline.querySelector(`[data-outline-row="${CSS.escape(uid)}"]`)?.focus({ preventScroll: false }));
  }

  function select(uid) {
    selectedUid = uid || "";
    for (const row of bodies.outline.querySelectorAll(".topic-ex-row")) {
      const on = row.dataset.outlineRow === selectedUid;
      row.classList.toggle("on", on);
      row.setAttribute("aria-selected", on ? "true" : "false");
    }
  }

  /** A node selected elsewhere (graph, document): highlight its outline row, unfolding the way to it. */
  function markSelected(uid) {
    if (!outline || !uid) { select(""); return; }
    if (uid === outline.topic.uid) { select(uid); return; }
    const rows = outlineAll();
    const unit = (outline.data.members || []).find((item) => item.uid === uid);
    if (!unit) { select(""); return; }
    const up = ancestorKeys(rows, unit.id);
    if (rootFolded || up.some((key) => outlineFolded.has(key))) {
      rootFolded = false;
      for (const key of up) outlineFolded.delete(key);
      saveOutlineFold(outline.topic.uid, outlineFolded);
      selectedUid = uid;
      paintOutline();
    } else select(uid);
    outlineFocus = unit.id;
    bodies.outline.querySelector(`[data-outline-row="${CSS.escape(uid)}"]`)?.scrollIntoView({ block: "nearest" });
  }

  // Ctrl+K chords fold the outline while the topic shows the graph or the
  // timeline (the document view keeps its own); Ctrl+Shift+[ / ] fold the
  // focused row.
  const typing = (target) => target?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable='true'], .cm-editor");
  const onKey = (event) => {
    if (!outline || !page() || outline.ownChords || !shown()) return;
    if (state.sections.collapsed.outline && state.sections.max !== "outline") return;
    if (state.sections.max && state.sections.max !== "outline") return;
    if (typing(event.target) || document.querySelector(".modal-backdrop, .palette-backdrop")) return;
    if (armed) {
      if (["Control", "Meta", "Shift", "Alt"].includes(event.key)) return; // i18n-ignore: key names
      event.preventDefault();
      event.stopImmediatePropagation();
      clearTimeout(armed);
      armed = null;
      chordHint.classList.add("hidden");
      const command = foldChord(event);
      if (command) runOutline(command);
      return;
    }
    const mod = event.ctrlKey || event.metaKey;
    if (mod && !event.shiftKey && !event.altKey && (event.key === "k" || event.key === "K" || event.code === "KeyK")) {
      event.preventDefault();
      event.stopImmediatePropagation();
      chordHint.classList.remove("hidden");
      armed = setTimeout(() => {
        armed = null;
        chordHint.classList.add("hidden");
        ctx.openPalette?.();
      }, 1000);
      return;
    }
    if (mod && event.shiftKey && (event.code === "BracketLeft" || event.code === "BracketRight")) {
      if (outlineFocus == null) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      runOutline({ cmd: event.code === "BracketLeft" ? "fold" : "unfold" });
    }
  };
  window.addEventListener("keydown", onKey, true);

  // ---------------------------------------------------------------- api
  function paintAll() { paintTopics(); paintLines(); paintOutline(); }

  function revealActive() {
    for (const kind of ["topics", "lines"]) {
      const rows = kind === "topics" ? topicTree(topics) : lineTree(lines);
      const row = rows.find((item) => rowActive(kind, item, activeRef, lines));
      if (!row) continue;
      const up = ancestorKeys(rows, row.key);
      if (up.some((key) => folded[kind].has(key))) {
        for (const key of up) folded[kind].delete(key);
        save();
        if (kind === "topics") paintTopics(); else paintLines();
      }
    }
    el.querySelector(".topic-ex-row.on")?.scrollIntoView({ block: "nearest" });
  }

  // The lists, when the topic page did not bring them (another page, a
  // project switch).
  async function load({ force = false } = {}) {
    const project = S.project?.id || "";
    if (!project || (!force && loaded === project)) return;
    if (loading) return loading;
    loading = (async () => {
      try {
        const [list, branches] = await Promise.all([api.topics(), api.branches().catch(() => ({ items: [] }))]);
        if ((S.project?.id || "") !== project) return;
        loaded = project;
        setTopics(list.items || [], branchTopicEntries(branches.items || []));
      } catch { /* the view keeps what it shows */ } finally { loading = null; }
    })();
    return loading;
  }

  // After every route: outside the topic page nothing is open, and the
  // outline shows its empty state.
  function sync() {
    if (!page()) {
      outline = null;
      activeRef = "";
      drawerOpen = false;
      markActive();
      paintOutline();
    }
    applyShown();
    if (shown()) load();
  }
  window.addEventListener("ark:selection", sync);
  window.addEventListener("popstate", () => requestAnimationFrame(sync));
  window.addEventListener("ark:project-switched", () => {
    loaded = "";
    topics = [];
    lines = [];
    paintAll();
    if (shown()) load();
  });

  function setTopics(nextTopics, nextLines) {
    topics = nextTopics || [];
    lines = nextLines || [];
    if (S.project?.id) loaded = S.project.id;
    paintTopics();
    paintLines();
    revealActive();
  }

  function markActive() {
    for (const row of el.querySelectorAll("[data-topic-row], [data-line-row]")) {
      const on = (row.dataset.topicRow || row.dataset.lineRef) === activeRef;
      row.classList.toggle("on", on);
      row.setAttribute("aria-selected", on ? "true" : "false");
    }
  }

  paintSections();
  paintAll();

  return {
    el, backdrop, drawerButton,
    shown,
    toggleDrawer,
    applyShown,
    load,
    sync,
    // The workbench showed the view.
    shownInView() { applyShown(); load(); },
    setTopics,
    setActive(ref) {
      activeRef = ref || "";
      markActive();
      revealActive();
    },
    setOutline(next) {
      const changed = next?.topic?.uid !== outline?.topic?.uid;
      outline = next;
      if (changed) {
        outlineFolded = next ? loadOutlineFold(next.topic.uid) : new Set();
        rootFolded = false;
        outlineFocus = null;
        selectedUid = "";
      }
      paintOutline();
    },
    markSelected,
  };
}
