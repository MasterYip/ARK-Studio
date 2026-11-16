// Workbench "Search" view (VS Code style): one query over node text
// (/api/search/nodes) and project files (/api/workspace/grep) with match
// case / whole word / regex toggles and files-to-include/exclude, results
// grouped as Nodes and Files with highlighted matching lines.  Results
// stream in (stream=1, NDJSON) and are painted once per frame into a
// virtualised list (only visible rows exist, highlighted on demand).
// Debounced; a newer query aborts the running one (the server stops too).
// F4 / Shift+F4 step through results.

import { h, clear, typeColor } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import {
  SEARCH_STORE_KEY, normalizeQuery, searchParams, queryError, groupResults, resultSummary,
  resultRows, nextMatch, moveRow, highlightSegments, matchPreview,
  emptyPayload, applySearchEvent, visibleRange, scrollForRow, SEARCH_ROW_HEIGHT,
} from "./search-model.js";

const DEBOUNCE = 150;
const FIELD_LABEL = { title: "search.field.title", body: "search.field.body", tags: "search.field.tags", comment: "search.field.comment" };

function storedQuery() {
  try { return normalizeQuery(JSON.parse(localStorage.getItem(SEARCH_STORE_KEY) || "{}")); } catch { return normalizeQuery({}); }
}

export function createSearchPanel(ctx) {
  let query = storedQuery();
  let groups = groupResults(null, null);
  let rows = [];
  let index = -1;
  let status = "";          // "", "searching", "error"
  let message = "";
  let seq = 0;
  let timer = 0;
  let controller = null;
  let frame = 0;
  let rowCache = new Map();   // row identity -> element (visible rows only)
  const collapsed = new Set();
  const sid = `s${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;

  const save = () => { try { localStorage.setItem(SEARCH_STORE_KEY, JSON.stringify(query)); } catch { /* storage disabled */ } };
  const input = h("input", {
    class: "input wb-search-input", type: "search", placeholder: t("search.placeholder"), "aria-label": t("search.placeholder"),
    value: query.q, spellcheck: "false",
  });
  const toggle = (key, label, text) => {
    const button = h("button", {
      class: `wb-search-toggle ${query[key] ? "on" : ""}`.trim(), type: "button", title: t(label), "aria-label": t(label),
      "aria-pressed": query[key] ? "true" : "false", dataset: { option: key },
      onclick: () => {
        query = { ...query, [key]: !query[key] };
        button.classList.toggle("on", query[key]);
        button.setAttribute("aria-pressed", query[key] ? "true" : "false");
        save();
        schedule(0);
      },
    }, text);
    return button;
  };
  const includeInput = h("input", { class: "input wb-search-glob", placeholder: t("search.include_hint"), "aria-label": t("search.include"), value: query.include, spellcheck: "false" });
  const excludeInput = h("input", { class: "input wb-search-glob", placeholder: t("search.exclude_hint"), "aria-label": t("search.exclude"), value: query.exclude, spellcheck: "false" });
  const details = h("div", { class: "wb-search-details", hidden: !query.details },
    h("label", null, h("span", null, t("search.include")), includeInput),
    h("label", null, h("span", null, t("search.exclude")), excludeInput));
  const detailsBtn = h("button", {
    class: `icon-btn wb-search-more ${query.details ? "on" : ""}`.trim(), type: "button", title: t("search.details"), "aria-label": t("search.details"),
    "aria-expanded": query.details ? "true" : "false",
    onclick: () => {
      query = { ...query, details: !query.details };
      details.hidden = !query.details;
      detailsBtn.classList.toggle("on", query.details);
      detailsBtn.setAttribute("aria-expanded", query.details ? "true" : "false");
      save();
    },
  }, icon("more", "sm"));
  const summary = h("div", { class: "wb-search-summary", "aria-live": "polite" });
  const canvas = h("div", { class: "wb-search-canvas" });
  const list = h("div", { class: "wb-search-results", role: "tree", tabindex: "0", "aria-label": t("search.results") }, canvas);
  list.addEventListener("scroll", () => queuePaint(false));
  if (typeof ResizeObserver === "function") new ResizeObserver(() => queuePaint(false)).observe(list);
  const root = h("div", { class: "wb-search" },
    h("div", { class: "wb-search-box" },
      h("div", { class: "wb-search-field" }, input,
        toggle("case", "search.case", "Aa"), toggle("word", "search.word", "ab"), toggle("regex", "search.regex", ".*")), // i18n-ignore: VS Code option glyphs
      detailsBtn),
    details, summary, list);

  input.addEventListener("input", () => { query = { ...query, q: input.value }; save(); schedule(); });
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); schedule(0); }
    if (event.key === "ArrowDown" && rows.length) { event.preventDefault(); index = moveRow(rows, -1, 1); paintRows(true); list.focus(); }
    if (event.key === "Escape" && input.value) { event.preventDefault(); input.value = ""; query = { ...query, q: "" }; save(); schedule(0); }
  });
  for (const [el, key] of [[includeInput, "include"], [excludeInput, "exclude"]]) {
    el.addEventListener("input", () => { query = { ...query, [key]: el.value }; save(); schedule(); });
    el.addEventListener("keydown", (event) => { if (event.key === "Enter") { event.preventDefault(); schedule(0); } });
  }
  list.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      index = moveRow(rows, index, event.key === "ArrowDown" ? 1 : -1);
      paintRows(true);
    } else if (event.key === "Enter") {
      event.preventDefault();
      // Ctrl/Cmd+Enter or Alt+Enter (ARK's "Open in Editor"): pinned Editor tab.
      if (event.ctrlKey || event.metaKey || event.altKey) openInEditorTab(rows[index]);
      else activate(rows[index]);
    } else if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      const row = rows[index];
      if (row && row.type !== "match") {
        event.preventDefault();
        if (event.key === "ArrowLeft") collapsed.add(row.key); else collapsed.delete(row.key);
        rebuild();
      }
    }
  });

  function schedule(delay = DEBOUNCE) {
    clearTimeout(timer);
    timer = setTimeout(run, delay);
  }

  async function run() {
    const params = searchParams(query);
    controller?.abort();
    controller = null;
    const mine = ++seq;
    if (!params) {
      groups = groupResults(null, null);
      status = ""; message = "";
      rebuild();
      return;
    }
    const regexError = queryError(query);
    if (regexError) {
      status = "error"; message = regexError;
      groups = groupResults(null, null);
      rebuild();
      return;
    }
    controller = new AbortController();
    const signal = controller.signal;
    status = "searching"; message = "";
    paintSummary();
    // Old results stay until the first new one arrives (no flash of empty).
    const payloads = { nodes: emptyPayload("nodes"), files: emptyPayload("files") };
    let fresh = false;
    const update = () => {
      if (mine !== seq) return;
      if (!fresh) { fresh = true; index = -1; rowCache = new Map(); list.scrollTop = 0; }
      groups = groupResults(payloads.nodes, payloads.files);
      queuePaint(true);
    };
    const stream = (kind, params) => api.searchStream(kind, { ...params, sid: `${sid}:${kind}` }, (event) => {
      if (mine === seq && applySearchEvent(payloads[kind], kind, event)) update();
    }, { signal }).catch((err) => {
      if (err?.name === "AbortError" || mine !== seq) return;
      payloads[kind].error = err.message || String(err);
    });
    await Promise.all([stream("nodes", params.nodes), stream("files", params.files)]);
    if (mine !== seq) return;   // a newer search started
    controller = null;
    if (!fresh) { index = -1; rowCache = new Map(); }
    groups = groupResults(payloads.nodes, payloads.files);
    const errors = [payloads.nodes.error, payloads.files.error].filter(Boolean);
    status = errors.length && !groups.some((group) => group.items.length) ? "error" : "";
    message = errors[0] || "";
    rebuild();
  }

  // Streamed results and scrolling repaint at most once per frame.
  function queuePaint(full) {
    if (frame) { frame.full ||= full; return; }
    const job = { full };
    frame = job;
    requestAnimationFrame(() => {
      frame = 0;
      if (job.full) rebuild(); else paintRows();
    });
  }

  function rebuild() {
    rows = resultRows(groups, collapsed);
    if (index >= rows.length) index = rows.length - 1;
    paintSummary();
    paintRows();
  }

  function paintSummary() {
    const info = resultSummary(groups);
    clear(summary);
    if (status === "searching" && info.count) summary.append(h("span", { class: "spinner sm" }), t("search.summary", { n: info.count, items: info.items }));
    else if (status === "searching") summary.append(h("span", { class: "spinner sm" }), t("search.searching"));
    else if (status === "error") summary.append(h("span", { class: "err" }, message));
    else if (!query.q.trim()) summary.append(h("span", { class: "muted" }, t("search.hint")));
    else if (!info.count) summary.append(t("search.no_results"));
    else {
      summary.append(t("search.summary", { n: info.count, items: info.items }));
      if (info.truncated) summary.append(h("span", { class: "warn" }, ` · ${t("search.truncated")}`));
      summary.append(h("button", { class: "linklike", type: "button", onclick: collapseAll }, t("search.collapse_all")));
    }
  }

  function collapseAll() {
    const anyOpen = rows.some((row) => row.type === "item" && row.open);
    for (const group of groups) for (const item of group.items) {
      if (anyOpen) collapsed.add(item.key); else collapsed.delete(item.key);
    }
    rebuild();
  }

  function activate(row) {
    if (!row) return;
    if (row.type === "group" || row.type === "item") {
      if (collapsed.has(row.key)) collapsed.delete(row.key); else collapsed.add(row.key);
      rebuild();
      return;
    }
    const { item, match } = row;
    if (item.kind === "node") ctx.openUnit?.(item.uid || item.id);
    else ctx.openInEditor?.({ kind: "ws", path: item.path }, { preview: true, line: match.line });
  }

  // Double-click / Ctrl+Enter: open the result as a pinned (non-preview) tab
  // in the focused editor group, at the matching field or line.
  function openInEditorTab(row) {
    if (!row || row.type === "group") return;
    const item = row.item;
    const match = row.match || item.matches?.[0] || null;
    if (item.kind === "node") {
      const reveal = match ? { field: match.field || "body", line: match.line || 1, commentId: match.comment_id ?? null } : null;
      ctx.openUnitInEditor?.({ uid: item.uid || item.id, id: item.id, title: item.title, type: item.type }, { preview: false, reveal });
    } else {
      ctx.openInEditor?.({ kind: "ws", path: item.path }, { preview: false, line: match?.line || 0 });
    }
  }

  // Node results: the node glyph, its core in the node's type colour.
  function nodeGlyph(type) {
    const svg = icon("node", "sm");
    svg.classList.add("node-glyph");
    if (type) svg.style.setProperty("--icon-fill", typeColor(type));
    return svg;
  }

  function lineEl(match) {
    const preview = matchPreview(match.text, match.ranges);
    return h("span", { class: "wb-search-line", title: match.text },
      highlightSegments(preview.text, preview.ranges).map((part) => (part.hit ? h("mark", null, part.text) : part.text)));
  }

  // Only the rows in (or near) the viewport are rendered, absolutely
  // positioned on a canvas as tall as the whole list.  Elements are reused
  // while their row is unchanged, so streaming in more results is cheap.
  function paintRows(reveal = false) {
    const viewport = list.clientHeight || 800;
    if (reveal && index >= 0) list.scrollTop = scrollForRow(index, list.scrollTop, viewport);
    canvas.style.height = `${rows.length * SEARCH_ROW_HEIGHT}px`;
    const { start, end } = visibleRange(list.scrollTop, viewport, rows.length);
    const next = new Map();
    const els = [];
    for (let i = start; i < end; i += 1) {
      const row = rows[i];
      const id = rowIdentity(row);
      const el = rowCache.get(id) || rowEl(row);
      next.set(id, el);
      el.classList.toggle("sel", i === index);
      el.dataset.row = String(i);
      el.style.top = `${i * SEARCH_ROW_HEIGHT}px`;
      els.push(el);
    }
    rowCache = next;
    canvas.replaceChildren(...els);
  }

  function rowIdentity(row) {
    if (row.type === "group") return `${row.key}|${row.open}|${row.group.count}`;
    if (row.type === "item") return `${row.key}|${row.open}|${row.item.matches.length}`;
    return row.key;
  }

  function rowEl(row) {
    let el;
    if (row.type === "group") {
      el = h("div", { class: "wb-search-row group", role: "treeitem", "aria-expanded": String(row.open) },
        icon(row.open ? "chevron" : "chevronRight", "sm"),
        row.group.kind === "nodes" ? nodeGlyph("") : icon("file", "sm"),
        h("span", { class: "name" }, t(`search.${row.group.kind}`)),
        h("span", { class: "count" }, String(row.group.count)));
    } else if (row.type === "item") {
      const node = row.item.kind === "node";
      el = h("div", { class: "wb-search-row item", role: "treeitem", "aria-expanded": String(row.open), title: node ? `${row.item.uid} · ${row.item.title}` : row.item.path,
        dataset: { kind: row.item.kind } },
        icon(row.open ? "chevron" : "chevronRight", "sm"),
        node ? nodeGlyph(row.item.type) : icon("file", "sm"),
        h("span", { class: "name" }, row.item.title),
        h("span", { class: "dir" }, node ? row.item.uid : row.item.path.split("/").slice(0, -1).join("/")),
        h("span", { class: "count" }, String(row.item.matches.length)));
    } else {
      const node = row.item.kind === "node";
      el = h("div", { class: "wb-search-row match", role: "treeitem", title: `${t("search.open_at", { line: row.match.line })} · ${t("search.dblclick_editor")}` },
        node ? h("span", { class: "field" }, t(FIELD_LABEL[row.match.field] || "search.field.body")) : h("span", { class: "ln" }, String(row.match.line)),
        lineEl(row.match));
    }
    el.addEventListener("mousedown", (event) => event.preventDefault());
    el.addEventListener("click", () => {
      index = Number(el.dataset.row);
      paintRows();
      list.focus({ preventScroll: true });
      activate(rows[index]);
    });
    el.addEventListener("dblclick", (event) => {
      event.preventDefault();
      window.getSelection?.()?.removeAllRanges?.();
      openInEditorTab(rows[Number(el.dataset.row)]);
    });
    return el;
  }

  // F4 / Shift+F4 (global while the view has results): next/previous result.
  function step(delta) {
    const next = nextMatch(rows, index, delta);
    if (next < 0) return false;
    index = next;
    paintRows(true);
    activate(rows[index]);
    return true;
  }

  paintSummary();
  if (query.q.trim()) schedule(0);
  return {
    el: root,
    shown() {
      paintRows();
      if (!input.value && !document.activeElement?.closest?.(".wb-search")) input.focus();
    },
    focus(text = "") {
      if (text) { input.value = text; query = { ...query, q: text }; save(); schedule(0); }
      input.focus();
      input.select();
    },
    step,
    hasResults: () => rows.some((row) => row.type === "match"),
  };
}
