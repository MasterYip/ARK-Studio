// Source <-> preview scroll sync between editor views of the same document
// (a node or a Markdown file open in Source in one group and Preview in
// another, or a node tab's Split mode).  Scrolling one side moves the
// other to the same source line; the caret line in a source highlights the
// matching preview block.  Positions are mapped through line anchors
// (./scroll-sync-model.js): preview blocks carry data-source-line
// (richText(..., { sourceLines: true })), source lines are measured.
//
// Scroll handling runs once per animation frame; a view moved by the sync
// ignores its own echoed scroll for a short window.  Preview size changes
// (images, diagrams) re-apply the leading view's line.  "Sync scroll" is a
// per-browser preference (on by default).  Independently of it, each
// group remembers the line it showed per document so switching between
// Source and Preview keeps the place.

import { normalizeAnchors, positionForLine, lineForPosition, blockLineFor, lineTops } from "./scroll-sync-model.js";

const PREF_KEY = "ark.editor.syncScroll";
const ECHO_MS = 180;
const surfaces = new Set();
const lastLead = new Map();     // doc -> { surface, line }
const remembered = new Map();   // `${doc}|${group}` -> line

export function syncScrollEnabled() {
  try { return localStorage.getItem(PREF_KEY) !== "0"; } catch { return true; }
}

export function setSyncScroll(on) {
  try { localStorage.setItem(PREF_KEY, on ? "1" : "0"); } catch { /* storage disabled */ }
}

const alive = (surface) => {
  const el = surface.scrollEl();
  if (el?.isConnected) return true;
  surfaces.delete(surface);
  surface.observer?.disconnect();
  return false;
};

function follow(leader) {
  const line = leader.lineAt();
  remembered.set(`${leader.doc}|${leader.group}`, line);
  if (!syncScrollEnabled()) return;
  lastLead.set(leader.doc, { surface: leader, line });
  const now = performance.now();
  for (const other of [...surfaces]) {
    if (other === leader || other.doc !== leader.doc || other.kind === leader.kind) continue;
    if (!alive(other) || !other.active()) continue;
    other.ignoreUntil = now + ECHO_MS;
    other.scrollToLine(line);
  }
}

// spec: { doc, group, kind: "source" | "preview", scrollEl(): Element,
//         listen?: [Element] (scroll event sources; default scrollEl()),
//         anchors(): [{ line, top }] normalized, in scrollEl content px,
//         active?(): bool, watch?: Element (preview content to observe) }
export function registerSurface(spec) {
  const surface = {
    active: () => true, ...spec, ignoreUntil: 0, frame: 0, observer: null,
    lineAt() { return lineForPosition(surface.anchors(), surface.scrollEl().scrollTop); },
    // Also follow size changes of `el` (re-rendered preview content).
    observe(el) { if (el && surface.observer) surface.observer.observe(el); },
    scrollToLine(line) {
      const el = surface.scrollEl();
      el.scrollTop = Math.max(0, Math.min(el.scrollHeight - el.clientHeight, positionForLine(surface.anchors(), line)));
    },
  };
  const onScroll = (event) => {
    if (event.target !== surface.scrollEl() || !surface.active()) return;
    if (performance.now() < surface.ignoreUntil) return;
    if (surface.frame) return;
    surface.frame = requestAnimationFrame(() => {
      surface.frame = 0;
      if (alive(surface)) follow(surface);
    });
  };
  for (const el of spec.listen || [spec.scrollEl()]) el?.addEventListener("scroll", onScroll, { passive: true });
  if (spec.watch && typeof ResizeObserver === "function") {
    surface.observer = new ResizeObserver(() => {
      if (!alive(surface) || !syncScrollEnabled() || !surface.active()) return;
      const lead = lastLead.get(surface.doc);
      if (!lead || lead.surface === surface || !alive(lead.surface) || lead.surface.kind === surface.kind) return;
      surface.ignoreUntil = performance.now() + ECHO_MS;
      surface.scrollToLine(lead.line);
    });
    surface.observer.observe(spec.watch);
  }
  surfaces.add(surface);
  return surface;
}

// Put a freshly rendered view where this group last was in the document
// (Source <-> Preview switches, re-renders).
export function restoreLine(surface) {
  const line = remembered.get(`${surface.doc}|${surface.group}`);
  if (!line) return;
  requestAnimationFrame(() => {
    if (!alive(surface)) return;
    surface.ignoreUntil = performance.now() + ECHO_MS;
    surface.scrollToLine(line);
  });
}

export function rememberLine(surface) {
  if (alive(surface)) remembered.set(`${surface.doc}|${surface.group}`, surface.lineAt());
}

// The caret moved to `line` in a source view: mark the matching block in
// the previews of the same document.
export function caretLine(doc, line) {
  if (!syncScrollEnabled()) return;
  for (const surface of [...surfaces]) {
    if (surface.doc !== doc || surface.kind !== "preview" || !alive(surface)) continue;
    const root = surface.watch || surface.scrollEl();
    const blocks = [...root.querySelectorAll("[data-source-line]")];
    const target = blockLineFor(blocks.map((el) => Number(el.dataset.sourceLine)), line);
    for (const el of root.querySelectorAll(".md-cursor-block")) el.classList.remove("md-cursor-block");
    if (target === null) continue;
    const block = blocks.filter((el) => Number(el.dataset.sourceLine) === target).pop();
    block?.classList.add("md-cursor-block");
  }
}

// ------------------------------------------------------------- anchors
// Preview: the blocks with data-source-line, relative to the scroller.
export function previewAnchors(scrollEl, root, totalLines) {
  const base = scrollEl.getBoundingClientRect().top - scrollEl.scrollTop;
  const list = [...root.querySelectorAll("[data-source-line]")].map((el) => ({
    line: Number(el.dataset.sourceLine), top: el.getBoundingClientRect().top - base,
  }));
  const rect = root.getBoundingClientRect();
  return normalizeAnchors(list, { line: Math.max(1, totalLines) + 1, top: rect.bottom - base });
}

// Source textarea: line tops.  Without wrapping every line has the same
// height; with wrapping a hidden mirror measures them (cached per text and
// width).
const mirrors = new WeakMap();

export function textareaAnchors(textarea) {
  const style = getComputedStyle(textarea);
  const padTop = parseFloat(style.paddingTop) || 0;
  const lineHeight = parseFloat(style.lineHeight) || (parseFloat(style.fontSize) || 13) * 1.5;
  const text = textarea.value;
  const count = text.split(/\r\n|\r|\n/).length;
  const wraps = style.whiteSpace !== "pre" && textarea.getAttribute("wrap") !== "off";
  if (!wraps) return [{ line: 1, top: padTop }, { line: count + 1, top: padTop + count * lineHeight }];
  const width = textarea.clientWidth;
  const cached = mirrors.get(textarea);
  if (cached && cached.text === text && cached.width === width) return cached.anchors;
  const mirror = document.createElement("div");
  for (const prop of ["fontFamily", "fontSize", "fontWeight", "lineHeight", "letterSpacing", "tabSize", "paddingLeft", "paddingRight", "paddingTop", "wordSpacing"]) {
    mirror.style[prop] = style[prop];
  }
  Object.assign(mirror.style, {
    position: "absolute", visibility: "hidden", left: "-99999px", top: "0", boxSizing: "border-box",
    width: `${width}px`, whiteSpace: "pre-wrap", overflowWrap: "break-word", wordBreak: style.wordBreak,
  });
  const rows = text.split(/\r\n|\r|\n/).map((line) => {
    const row = document.createElement("div");
    row.textContent = line || "​";
    return row;
  });
  mirror.append(...rows);
  document.body.appendChild(mirror);
  const { tops, end } = lineTops(rows.map((row) => row.getBoundingClientRect().height), padTop);
  mirror.remove();
  const anchors = normalizeAnchors(tops.map((top, i) => ({ line: i + 1, top })), { line: count + 1, top: end });
  mirrors.set(textarea, { text, width, anchors });
  return anchors;
}

// The 1-based line of the caret in a textarea.
export function caretLineOf(textarea) {
  return textarea.value.slice(0, textarea.selectionStart).split(/\r\n|\r|\n/).length;
}
