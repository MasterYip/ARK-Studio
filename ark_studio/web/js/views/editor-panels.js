// The Editor's side views for the open file: Outline (its headings or
// functions, folded together with the Markdown preview) and Timeline (its
// git history).  views/editor.js owns the documents and passes a host:
//
//   host.doc()            the focused group's document (see editor.js docInfo)
//   host.jump(index)      show outline item `index` in the editor
//   host.toggle(id)       fold / unfold one outline item
//   host.fold(action)     a folding command (shortcuts.js FOLD_ACTIONS ids)
//   host.openRev(path, entry, { pin })   that version, read-only
//   host.openDiff(path, entry)           that version against the current file
//
// Both repaint on EDITOR_DOC (fired by editor.js when the document, its text,
// its folds or the cursor position change).

import { h, clear, relativeTime, fmtDateTime } from "../dom.js";
import { t, localizeServer } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { toast } from "../components.js";
import { openContextMenu } from "../context-menu.js";
import { buildTree } from "../outline-model.js";

export const EDITOR_DOC = "ark:editor-doc";

const KIND_MARK = { function: "ƒ", method: "m", class: "C", type: "T", enum: "E" }; // i18n-ignore: symbol glyphs

// ------------------------------------------------------------------ outline
export function createOutlinePanel(host) {
  const list = h("div", { class: "ed-outline-list", role: "tree", "aria-label": t("workbench.view.outline") });
  const note = h("div", { class: "ed-panel-note" });
  const el = h("div", { class: "ed-outline", dataset: { panel: "outline" } }, note, list);
  let signature = "";
  let current = -1;
  let rows = [];          // visible rows in order: { node, el }
  let selected = -1;      // keyboard selection (index into rows)

  function paint() {
    const doc = host.doc();
    const next = doc ? `${doc.key}|${doc.version}|${doc.supported}|${doc.loading}|${[...doc.collapsed].join("\u0001")}` : "";
    if (next !== signature) {
      signature = next;
      rebuild(doc);
    }
    mark(doc ? doc.current : -1);
  }

  function rebuild(doc) {
    clear(list);
    rows = [];
    note.hidden = true;
    const say = (key) => { note.textContent = t(key); note.hidden = false; };
    if (!doc) { say("editor.outline.no_file"); return; }
    if (doc.loading) { say("action.loading"); return; }
    if (!doc.supported) { say("editor.outline.unsupported"); return; }
    if (!doc.items.length) { say(doc.viewer === "markdown" ? "editor.outline.no_headings" : "editor.outline.no_symbols"); return; }
    const walk = (nodes, depth) => {
      for (const node of nodes) {
        const open = !doc.collapsed.has(node.id);
        const hasKids = node.children.length > 0;
        const row = h("div", {
          class: "ed-outline-row", role: "treeitem", tabindex: "-1", title: node.item.text,
          "aria-level": String(depth + 1), "aria-expanded": hasKids ? String(open) : undefined,
          dataset: { index: String(node.index), line: String(node.item.line) },
          style: { paddingLeft: `${6 + depth * 12}px` },
        },
          h("button", {
            class: `ed-outline-twisty ${hasKids ? "" : "leaf"}`.trim(), type: "button", tabindex: "-1",
            "aria-label": t(open ? "editor.fold.fold_section" : "editor.fold.unfold_section"),
            onclick: (event) => { event.stopPropagation(); if (hasKids) host.toggle(node.id); },
          }, hasKids ? icon(open ? "chevron" : "chevronRight", "sm") : null),
          h("span", { class: `ed-outline-kind kind-${node.item.kind}` },
            node.item.kind === "heading" ? `H${node.item.level}` : KIND_MARK[node.item.kind] || "·"),
          h("span", { class: "name" }, node.item.text || t("editor.outline.untitled")),
          h("span", { class: "ln" }, String(node.item.line)));
        row.addEventListener("mousedown", (event) => { if (!event.target.closest(".ed-outline-twisty")) event.preventDefault(); });
        row.addEventListener("click", () => { select(rows.findIndex((item) => item.el === row)); host.jump(node.index); });
        list.appendChild(row);
        rows.push({ node, el: row });
        if (hasKids && open) walk(node.children, depth + 1);
      }
    };
    walk(buildTree(doc.items), 0);
    current = -2;
  }

  function mark(index) {
    if (index === current) return;
    current = index;
    let shown = null;
    for (const { node, el: row } of rows) {
      const on = node.index === index;
      row.classList.toggle("on", on);
      if (on) shown = row;
    }
    // Inside a folded section: mark the visible ancestor.
    if (!shown && index >= 0) {
      const before = rows.filter((row) => row.node.index < index).pop();
      if (before) { before.el.classList.add("on"); shown = before.el; }
    }
    if (shown && !el.contains(document.activeElement)) shown.scrollIntoView({ block: "nearest" });
  }

  function select(index) {
    if (!rows.length) return;
    selected = Math.max(0, Math.min(rows.length - 1, index));
    rows.forEach((row, i) => row.el.classList.toggle("sel", i === selected));
    rows[selected].el.focus({ preventScroll: true });
    rows[selected].el.scrollIntoView({ block: "nearest" });
  }

  list.addEventListener("keydown", (event) => {
    const row = rows[selected];
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      select(selected + (event.key === "ArrowDown" ? 1 : -1));
    } else if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      select(event.key === "Home" ? 0 : rows.length - 1);
    } else if (event.key === "Enter" && row) {
      event.preventDefault();
      host.jump(row.node.index);
    } else if ((event.key === "ArrowLeft" || event.key === "ArrowRight") && row?.node.children.length) {
      const open = !host.doc()?.collapsed.has(row.node.id);
      if (open === (event.key === "ArrowLeft")) {
        event.preventDefault();
        const index = row.node.index;
        host.toggle(row.node.id);
        requestAnimationFrame(() => select(rows.findIndex((item) => item.node.index === index)));
      }
    }
  });
  list.addEventListener("focusin", () => { if (selected < 0 && rows.length) select(Math.max(0, rows.findIndex((row) => row.el.classList.contains("on")))); });

  let frame = 0;
  const schedule = () => {
    if (frame) return;
    frame = requestAnimationFrame(() => { frame = 0; if (el.isConnected) paint(); });
  };
  window.addEventListener(EDITOR_DOC, schedule);
  return {
    el,
    shown: () => { signature = ""; paint(); },
    // The current section's line for "fold current" while the Outline has focus.
    focusedLine: () => (el.contains(document.activeElement) && rows[selected] ? rows[selected].node.item.line : 0),
  };
}

// ----------------------------------------------------------------- timeline
export function createTimelinePanel(host) {
  const list = h("div", { class: "ed-timeline-list", role: "list", "aria-label": t("workbench.view.timeline") });
  const note = h("div", { class: "ed-panel-note" });
  const el = h("div", { class: "ed-timeline", dataset: { panel: "timeline" } }, note, list);
  const cache = new Map();   // path -> { loading, data, error }
  let painted = "";

  async function load(path, { force = false } = {}) {
    const known = cache.get(path);
    if (known && !force && (known.loading || known.data)) return;
    cache.set(path, { loading: true });
    paint(true);
    let next;
    try {
      next = { data: await api.workspaceTimeline(path, 100) };
    } catch (err) {
      next = { error: localizeServer(err.message) || err.message };
    }
    cache.set(path, next);
    paint(true);
  }

  function paint(force = false) {
    const doc = host.doc();
    const path = doc?.historyPath || "";
    const state = path ? cache.get(path) : null;
    const signature = `${path}|${doc?.rev || ""}|${state?.loading ? "l" : ""}|${state?.data ? "d" : ""}|${state?.error || ""}`;
    if (!force && signature === painted) return;
    painted = signature;
    clear(list);
    note.hidden = true;
    const say = (text, cls = "") => { note.textContent = text; note.className = `ed-panel-note ${cls}`.trim(); note.hidden = false; };
    if (!path) { say(t("editor.timeline.no_file")); return; }
    if (!state) { load(path); return; }
    if (state.loading) { say(t("action.loading")); return; }
    if (state.error) { say(state.error, "err"); return; }
    const data = state.data || {};
    if (!data.is_repo) { say(t("editor.timeline.no_repo")); return; }
    const entries = data.entries || [];
    if (!entries.length) { say(t("editor.timeline.empty")); return; }
    for (const entry of entries) {
      const row = h("div", {
        class: `ed-timeline-row ${doc.rev && entry.rev.startsWith(doc.rev) ? "on" : ""}`.trim(), role: "listitem", tabindex: "0",
        title: `${entry.subject}\n${entry.author} · ${fmtDateTime(entry.time)}\n${entry.short}${entry.path !== path ? ` · ${entry.path}` : ""}`,
        dataset: { rev: entry.rev },
      },
        h("span", { class: "ed-timeline-dot", "aria-hidden": "true" }),
        h("div", { class: "ed-timeline-main" },
          h("div", { class: "subject" }, entry.subject || entry.short),
          h("div", { class: "meta" }, `${entry.author} · ${relativeTime(entry.time)}`,
            entry.path !== path ? h("span", { class: "renamed" }, ` · ${entry.path}`) : null)),
        h("button", {
          class: "icon-btn ed-timeline-compare", type: "button", title: t("editor.timeline.compare"), "aria-label": t("editor.timeline.compare"),
          disabled: entry.inside === false ? true : undefined,
          onclick: (event) => { event.stopPropagation(); host.openDiff(path, entry); },
        }, icon("git", "sm")));
      const open = (pin) => {
        if (entry.inside === false) { toast(t("editor.timeline.outside"), "err"); return; }
        host.openRev(entry.path, entry, { pin });
      };
      row.addEventListener("click", () => open(false));
      row.addEventListener("dblclick", () => open(true));
      row.addEventListener("keydown", (event) => {
        if (event.key === "Enter") { event.preventDefault(); open(event.ctrlKey || event.metaKey); }
        if (event.key === "ArrowDown") { event.preventDefault(); row.nextElementSibling?.focus(); }
        if (event.key === "ArrowUp") { event.preventDefault(); row.previousElementSibling?.focus(); }
      });
      row.addEventListener("contextmenu", (event) => {
        event.preventDefault();
        openContextMenu({ x: event.clientX, y: event.clientY, items: [
          { label: t("editor.timeline.open_version"), icon: "file", onClick: () => open(true) },
          { label: t("editor.timeline.compare"), icon: "git", disabled: entry.inside === false, onClick: () => host.openDiff(path, entry) },
          { label: t("editor.timeline.copy_hash"), icon: "copy", onClick: () => navigator.clipboard?.writeText(entry.rev) },
        ] });
      });
      list.appendChild(row);
    }
    if (data.truncated) list.appendChild(h("div", { class: "ed-panel-note" }, t("editor.timeline.truncated", { n: entries.length })));
  }

  let frame = 0;
  window.addEventListener(EDITOR_DOC, () => {
    if (frame) return;
    frame = requestAnimationFrame(() => { frame = 0; if (el.isConnected) paint(); });
  });
  return {
    el,
    shown: () => paint(true),
    refresh: () => {
      const path = host.doc()?.historyPath;
      if (path) load(path, { force: true });
    },
    // A saved file has new history once committed; drop what we know.
    forget: (path) => { cache.delete(path); paint(true); },
  };
}
