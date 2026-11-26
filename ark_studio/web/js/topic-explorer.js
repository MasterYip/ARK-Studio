// Research › Topics: the explorer beside the topic (right side; a drawer on
// phones).  Stacked sections like the Editor's Explorer: Topics (nested by
// part_of), Lines (branch: tags as a tree) and the open topic's Outline.
// State (shown, width, sections, folds) lives in topic-explorer-model.js.
import { h, clear, typeColor } from "./dom.js";
import { t, typeLabel, statusLabel } from "./i18n.js";
import { icon } from "./icons.js";
import { branchBadge } from "./needs-ui.js";
import { outlineMove, outlineDrop, dropPosition, foldChord } from "./topic-model.js";
import {
  EXPLORER_KEY, EXPLORER_WIDTH, parseExplorer, clampWidth, sections as model,
  topicTree, lineTree, visibleRows, searchRows, ancestorKeys, outlineRows, foldCommand,
} from "./topic-explorer-model.js";

const PHONE_QUERY = "(max-width: 760px)";
const OUTLINE_FOLD_KEY = "ark.topic.tree.fold.v1";
const SECTION_TITLES = { topics: "topic.ex.topics", lines: "topic.ex.lines", outline: "topic.ex.outline" };

const isPhone = () => Boolean(globalThis.matchMedia?.(PHONE_QUERY)?.matches);

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

/**
 * `hooks`: titleOf(topic), openTopic(topic), openLine(row), newTopic(),
 * lineSettings(), drill(unit), onToggle(open).
 * Returns { el, toggleButton, setTopics(topics, lines), setActive(ref),
 * setOutline(outline | null), markSelected(uid), toggle(force) }.
 */
export function topicExplorer(ctx, hooks) {
  let state = (() => {
    try { return parseExplorer(localStorage.getItem(EXPLORER_KEY)); } catch { return parseExplorer(""); }
  })();
  let phoneOpen = false;
  let query = "";
  let topics = [];
  let lines = [];
  let activeRef = "";
  let outline = null;      // { topic, data, allMembers, design, locate(uid), reveal(uid) }
  let outlineFolded = new Set();
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
  const closeBtn = h("button", {
    class: "icon-btn", type: "button", title: t("topic.ex.hide"), "aria-label": t("topic.ex.hide"),
    dataset: { topicExClose: "1" }, onclick: () => toggle(false),
  }, icon("x", "sm"));
  const wrap = h("div", { class: "ed-sections topic-ex-sections" });
  const bodies = {};
  const el = h("aside", { class: "topic-explorer", "aria-label": t("topic.ex.title"), dataset: { topicExplorer: "1" } },
    h("div", { class: "topic-ex-head" },
      h("span", { class: "topic-ex-title" }, t("topic.ex.title")), chordHint, h("span", { class: "spacer" }), closeBtn),
    h("div", { class: "topic-ex-filter" }, search),
    wrap);
  const backdrop = h("div", { class: "topic-ex-backdrop", "aria-hidden": "true", onclick: () => toggle(false) });
  const sash = h("div", {
    class: "topic-ex-sash", role: "separator", tabindex: 0, "aria-orientation": "vertical", title: t("topic.ex.resize"),
    "aria-label": t("topic.ex.resize"), "aria-valuemin": String(EXPLORER_WIDTH.min), "aria-valuemax": String(EXPLORER_WIDTH.max),
  });
  const toggleButton = h("button", {
    class: "ghost-btn topic-ex-toggle", type: "button", dataset: { topicExToggle: "1" },
    title: t("topic.ex.toggle"), "aria-label": t("topic.ex.toggle"), onclick: () => toggle(),
  }, icon("panelLeft", "sm"), h("span", { class: "label" }, t("topic.ex.title")));

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

  // ------------------------------------------------- shown, width, drawer
  function shown() { return isPhone() ? phoneOpen : state.open; }

  function applyShown() {
    const page = el.closest(".topic-page");
    const open = shown();
    page?.classList.toggle("topic-ex-closed", !state.open);
    page?.classList.toggle("topic-ex-phone-open", phoneOpen);
    page?.style.setProperty("--topic-ex-w", `${state.width}px`);
    sash.setAttribute("aria-valuenow", String(state.width));
    toggleButton.setAttribute("aria-pressed", open ? "true" : "false");
    toggleButton.classList.toggle("on", open);
  }

  function toggle(force) {
    if (isPhone()) {
      phoneOpen = typeof force === "boolean" ? force : !phoneOpen;
      if (phoneOpen) requestAnimationFrame(() => revealActive());
    } else {
      state = { ...state, open: typeof force === "boolean" ? force : !state.open };
      save();
    }
    applyShown();
    hooks.onToggle?.(shown());
  }

  const setWidth = (value, persist = false) => {
    state = { ...state, width: clampWidth(value) };
    applyShown();
    if (persist) save();
  };
  let drag = null;
  sash.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    drag = { id: event.pointerId, x: event.clientX, width: state.width };
    sash.setPointerCapture?.(event.pointerId);
    sash.classList.add("active");
  });
  sash.addEventListener("pointermove", (event) => {
    if (drag?.id !== event.pointerId) return;
    setWidth(drag.width - (event.clientX - drag.x));
  });
  const endDrag = (event) => {
    if (drag?.id !== event.pointerId) return;
    drag = null;
    sash.classList.remove("active");
    setWidth(state.width, true);
    hooks.onToggle?.(shown());
  };
  for (const name of ["pointerup", "pointercancel", "lostpointercapture"]) sash.addEventListener(name, endDrag);
  sash.addEventListener("keydown", (event) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === "Home" ? EXPLORER_WIDTH.max : event.key === "End" ? EXPLORER_WIDTH.min
      : state.width + (event.key === "ArrowLeft" ? 16 : -16);
    setWidth(next, true);
  });
  sash.addEventListener("dblclick", () => setWidth(EXPLORER_WIDTH.default, true));

  // --------------------------------------------------------------- rows
  const textMatch = (text) => !query.trim() || String(text || "").toLowerCase().includes(query.trim().toLowerCase());

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
    const lineOf = new Map(lines.filter((line) => !line.auto).map((line) => [line.ref, line]));
    for (const row of rows) {
      const topic = row.topic;
      const open = searching || !folded.topics.has(row.key);
      const line = lineOf.get(topic.uid);
      host.append(treeRow({
        depth: row.depth, hasChildren: row.children > 0, open, on: topic.uid === activeRef,
        onFold: () => toggleFold("topics", row.key),
        onClick: () => { hooks.openTopic(topic); if (isPhone()) toggle(false); },
        iconEl: h("i", { class: "topic-ex-glyph", style: { color: typeColor("topic") } }, icon("layers", "sm")),
        name: hooks.titleOf(topic),
        count: topic.member_count ?? topic.preview?.members?.length ?? null,
        extra: [
          topic.preview?.rule ? h("span", { class: "topic-rule-badge", title: t("topic.rule_badge_hint") }, icon("filter", "sm")) : null,
          line ? h("span", { class: "topic-line-badge", title: line.tag }, t("branch.line")) : null,
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
      const ref = lineRef(row);
      const open = searching || !folded.lines.has(row.key);
      host.append(treeRow({
        depth: row.depth, hasChildren: row.children > 0, open, on: ref === activeRef,
        onFold: () => toggleFold("lines", row.key),
        onClick: () => { hooks.openLine({ ...row, ref }); if (isPhone()) toggle(false); },
        iconEl: icon("git", "sm"),
        name: row.label,
        count: row.count,
        extra: [
          line && !line.auto ? h("span", { class: "topic-line-badge", title: line.ref }, t("topic.ex.pinned")) : null,
          line ? branchBadge(line.status === "active" ? null : { status: line.status }) : null,
        ],
        data: { key: row.key, lineRow: row.key, lineRef: ref },
        title: line ? `${line.tag || ref} · ${t("topic.members_n", { n: line.units })}` : ref,
      }));
    }
  }

  // A line's topic ref; a path that only holds other lines opens by its tag
  // (the project's line prefix, "branch" by default).
  function lineRef(row) {
    if (row.line) return row.line.ref;
    const prefix = String(lines.find((line) => line.tag?.includes(":"))?.tag || "branch:").split(":")[0];
    return `${prefix}:${row.key}`;
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
    if (!outline) { host.append(h("div", { class: "ed-tree-note" }, t("topic.loading"))); return; }
    const members = outline.data.members || [];
    sectionCount("outline", members.length);
    if (!members.length) {
      host.append(h("div", { class: "ed-tree-note" }, t(outline.data.filtered ? "common.no_results" : "topic.no_members")));
      return;
    }
    const all = outlineRows(members);
    const searching = Boolean(query.trim());
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
        depth: row.depth, hasChildren: row.children > 0, open, on: unit.uid === selectedUid,
        onFold: () => { outlineFocus = unit.id; runOutline({ cmd: "toggle" }); focusOutlineRow(unit.uid); },
        onClick: () => {
          outlineFocus = unit.id;
          select(unit.uid);
          outline.locate?.(unit.uid);
          if (isPhone()) toggle(false);
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
    const rows = outlineAll();
    const unit = (outline.data.members || []).find((item) => item.uid === uid);
    if (!unit) { select(""); return; }
    const up = ancestorKeys(rows, unit.id);
    if (up.some((key) => outlineFolded.has(key))) {
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
    if (!el.isConnected) { window.removeEventListener("keydown", onKey, true); return; }
    if (!outline || outline.ownChords || !shown()) return;
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
      const row = rows.find((item) => (kind === "topics" ? item.key : lineRef(item)) === activeRef);
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

  paintSections();
  paintAll();
  queueMicrotask(applyShown);

  return {
    el, sash, backdrop, toggleButton,
    shown,
    toggle,
    applyShown,
    setTopics(nextTopics, nextLines) {
      topics = nextTopics || [];
      lines = nextLines || [];
      paintTopics();
      paintLines();
      revealActive();
    },
    setActive(ref) {
      activeRef = ref || "";
      for (const row of el.querySelectorAll("[data-topic-row], [data-line-row]")) {
        const on = (row.dataset.topicRow || row.dataset.lineRef) === activeRef;
        row.classList.toggle("on", on);
        row.setAttribute("aria-selected", on ? "true" : "false");
      }
      revealActive();
    },
    setOutline(next) {
      const changed = next?.topic?.uid !== outline?.topic?.uid;
      outline = next;
      if (changed) {
        outlineFolded = next ? loadOutlineFold(next.topic.uid) : new Set();
        outlineFocus = null;
        selectedUid = "";
      }
      paintOutline();
    },
    markSelected,
    textMatch,
  };
}
