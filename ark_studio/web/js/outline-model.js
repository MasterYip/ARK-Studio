// Outline and folding for the Editor (pure, DOM-free; tests/js/outline_model.test.mjs).
//
// An outline item is { level, text, line, kind }: Markdown headings (level =
// heading level, line = 1-based source line) or, for a few code languages,
// functions and classes found with simple line patterns (level = nesting).
// Folding state is a Set of item ids; the Outline view and the Markdown
// preview share it per file, so folding in one shows in the other.

const FENCE_RE = /^ {0,3}(`{3,}|~{3,})/;
const ATX_RE = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
const SETEXT_RE = /^ {0,3}(=+|-+)[ \t]*$/;
const LIST_RE = /^\s*(?:[-*+]|\d+[.)])\s/;

// Inline Markdown reduced to its text (links, emphasis, code, images).
export function plainHeading(text) {
  return String(text || "")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]*)\]\[[^\]]*\]/g, "$1")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/(\*\*|__)(.+?)\1/g, "$2")
    .replace(/(^|[^\w*])[*_]([^*_]+)[*_](?=[^\w*]|$)/g, "$1$2")
    .replace(/<[^>]+>/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

/** Markdown headings: ATX (# Title) and setext (Title / ===), outside fenced code and front matter. */
export function markdownHeadings(text) {
  const lines = String(text || "").replace(/^﻿/, "").split(/\r?\n/);
  const out = [];
  let start = 0;
  // YAML front matter at the very top.
  if (/^---\s*$/.test(lines[0] || "")) {
    const end = lines.findIndex((line, index) => index > 0 && /^(---|\.\.\.)\s*$/.test(line));
    if (end > 0) start = end + 1;
  }
  let fence = null;
  let para = null;       // { start, text } of the paragraph a setext underline would turn into a heading
  for (let i = start; i < lines.length; i += 1) {
    const line = lines[i];
    if (fence) {
      const close = FENCE_RE.exec(line);
      if (close && close[1][0] === fence[0] && close[1].length >= fence.length && !line.slice(line.indexOf(close[1]) + close[1].length).trim()) fence = null;
      para = null;
      continue;
    }
    const open = FENCE_RE.exec(line);
    if (open) { fence = open[1]; para = null; continue; }
    const atx = ATX_RE.exec(line);
    if (atx) {
      out.push({ level: atx[1].length, text: plainHeading(atx[2] || ""), line: i + 1, kind: "heading" });
      para = null;
      continue;
    }
    const setext = SETEXT_RE.exec(line);
    if (setext && para) {
      out.push({ level: setext[1][0] === "=" ? 1 : 2, text: plainHeading(para.text), line: para.start, kind: "heading" });
      para = null;
      continue;
    }
    const plain = line.trim() && !/^ {4,}|^\t/.test(line) && !LIST_RE.test(line) && !/^\s*[>|]/.test(line);
    // A setext heading's text may span lines; it starts at the paragraph's first line.
    if (!plain) para = null;
    else if (para) para.text += ` ${line.trim()}`;
    else para = { start: i + 1, text: line.trim() };
  }
  return out;
}

const JS_KEYWORDS = new Set(["if", "for", "while", "switch", "catch", "function", "return", "with", "else", "do", "try", "new", "typeof", "await"]);

function indentOf(line) {
  const match = /^[ \t]*/.exec(line)[0];
  return match.replace(/\t/g, "    ").length;
}

function jsSymbols(lines) {
  const out = [];
  let cls = null;   // { indent } while inside a class body
  lines.forEach((line, index) => {
    const n = index + 1;
    const indent = indentOf(line);
    if (cls && line.trim() && indent <= cls.indent && !/^\s*[})\]]/.test(line)) cls = null;
    if (cls && indent === cls.indent && /^\s*}/.test(line)) { cls = null; return; }
    let m = /^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)/.exec(line.trim());
    if (m && indent === 0) { out.push({ level: 1, text: m[1], line: n, kind: "class" }); cls = { indent }; return; }
    if (indent === 0) {
      m = /^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)/.exec(line)
        || /^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::[^=]+)?=>|[A-Za-z_$][\w$]*\s*=>)/.exec(line);
      if (m) { out.push({ level: 1, text: m[1], line: n, kind: "function" }); return; }
      m = /^(?:export\s+)?(?:declare\s+)?(interface|type|enum)\s+([A-Za-z_$][\w$]*)/.exec(line);
      if (m) { out.push({ level: 1, text: m[2], line: n, kind: m[1] === "enum" ? "enum" : "type" }); return; }
    }
    if (cls && indent > cls.indent) {
      m = /^\s*(?:(?:public|private|protected|static|readonly|override|async|get|set)\s+)*\*?\s*(#?[A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\([^)]*\)?\s*(?::[^{]+)?\{?\s*$/.exec(line);
      if (m && !JS_KEYWORDS.has(m[1]) && indent <= cls.indent + 4) out.push({ level: 2, text: m[1], line: n, kind: "method" });
    }
  });
  return out;
}

function goSymbols(lines) {
  const out = [];
  lines.forEach((line, index) => {
    let m = /^func\s+(?:\(\s*\w*\s*\*?\s*([A-Za-z_]\w*)(?:\[[^\]]*\])?\s*\)\s*)?([A-Za-z_]\w*)/.exec(line);
    if (m) { out.push({ level: 1, text: m[1] ? `${m[1]}.${m[2]}` : m[2], line: index + 1, kind: m[1] ? "method" : "function" }); return; }
    m = /^type\s+([A-Za-z_]\w*)(?:\[[^\]]*\])?\s+(struct|interface)?/.exec(line);
    if (m) out.push({ level: 1, text: m[1], line: index + 1, kind: m[2] === "interface" ? "type" : m[2] === "struct" ? "class" : "type" });
  });
  return out;
}

function pythonSymbols(lines) {
  const out = [];
  const stack = [];   // indents of the open defs/classes
  lines.forEach((line, index) => {
    const m = /^([ \t]*)(?:async\s+)?(def|class)\s+([A-Za-z_]\w*)/.exec(line);
    if (!m) return;
    const indent = indentOf(m[1]);
    while (stack.length && stack[stack.length - 1] >= indent) stack.pop();
    out.push({ level: stack.length + 1, text: m[3], line: index + 1, kind: m[2] === "class" ? "class" : stack.length ? "method" : "function" });
    stack.push(indent);
  });
  return out;
}

/* i18n-ignore-start: language ids */
const CODE_OUTLINES = {
  javascript: jsSymbols, jsx: jsSymbols, typescript: jsSymbols, tsx: jsSymbols,
  go: goSymbols, python: pythonSymbols,
};
/* i18n-ignore-end */

/** The outline of a document: Markdown headings or code symbols ([] when unsupported). */
export function outlineFor(text, { viewer = "", language = "" } = {}) {
  if (viewer === "markdown" || language === "markdown") return markdownHeadings(text);
  const parse = CODE_OUTLINES[language];
  if (!parse) return [];
  return parse(String(text || "").split(/\r?\n/));
}

export function supportsOutline({ viewer = "", language = "" } = {}) {
  return viewer === "markdown" || language === "markdown" || Boolean(CODE_OUTLINES[language]);
}

/** Stable ids: level, text and the occurrence of that pair (survive edits elsewhere). */
export function itemIds(items) {
  const seen = new Map();
  return items.map((item) => {
    const base = `${item.level}:${item.text}`;
    const n = (seen.get(base) || 0) + 1;
    seen.set(base, n);
    return `${base}#${n}`;
  });
}

/** Indices of the items that contain item `index` (nearest last). */
export function ancestorsOf(items, index) {
  const out = [];
  let level = items[index]?.level ?? 0;
  for (let i = index - 1; i >= 0 && level > 1; i -= 1) {
    if (items[i].level < level) {
      out.unshift(i);
      level = items[i].level;
    }
  }
  return out;
}

/** Nested tree: [{ index, item, id, children }]. */
export function buildTree(items) {
  const ids = itemIds(items);
  const roots = [];
  const stack = [];
  items.forEach((item, index) => {
    const node = { index, item, id: ids[index], children: [] };
    while (stack.length && stack[stack.length - 1].item.level >= item.level) stack.pop();
    (stack.length ? stack[stack.length - 1].children : roots).push(node);
    stack.push(node);
  });
  return roots;
}

/** Index of the item whose section holds `line` (-1 before the first item). */
export function currentIndex(items, line) {
  let found = -1;
  for (let i = 0; i < items.length; i += 1) {
    if (items[i].line <= line) found = i;
    else break;
  }
  return found;
}

/** Last line of item `index`'s section (the line before the next item at the same or a higher level). */
export function sectionEnd(items, index, totalLines) {
  const level = items[index]?.level ?? 0;
  for (let i = index + 1; i < items.length; i += 1) {
    if (items[i].level <= level) return items[i].line - 1;
  }
  return totalLines;
}

/**
 * Which entries a fold state hides.  `entries` is the document in order:
 * headings { level, id } and content { level: 0 }.  A collapsed heading hides
 * everything after it up to the next heading at its level or higher.
 */
export function foldVisibility(entries, collapsed) {
  const hidden = [];
  let hideBelow = 0;   // level of the collapsed heading that hides; 0 = none
  for (const entry of entries) {
    const level = Number(entry.level) || 0;
    if (level > 0) {
      if (hideBelow && level > hideBelow) { hidden.push(true); continue; }
      hideBelow = 0;
      hidden.push(false);
      if (entry.id && collapsed.has(entry.id)) hideBelow = level;
    } else {
      hidden.push(Boolean(hideBelow));
    }
  }
  return hidden;
}

/** Fold all: every item. */
export function foldAllIds(items) {
  return new Set(itemIds(items));
}

/**
 * Fold to level n (Ctrl+K Ctrl+n): items at level n and deeper collapse, so
 * only headings down to level n stay visible.
 */
export function foldLevelIds(items, n) {
  const ids = itemIds(items);
  const out = new Set();
  items.forEach((item, index) => { if (item.level >= n) out.add(ids[index]); });
  return out;
}

/**
 * Fold or unfold the section at `line`.  Folding an already folded section
 * folds its parent (VS Code); unfolding also opens folded ancestors, so the
 * line becomes visible.
 */
export function foldAt(items, line, collapsed, fold) {
  const index = currentIndex(items, line);
  const next = new Set(collapsed);
  if (index < 0) return next;
  const ids = itemIds(items);
  const chain = [...ancestorsOf(items, index), index];
  if (fold) {
    const target = [...chain].reverse().find((i) => !next.has(ids[i]));
    if (target !== undefined) next.add(ids[target]);
    return next;
  }
  for (const i of chain) next.delete(ids[i]);
  return next;
}

/** Ids to unfold so item `index` is visible (its folded ancestors). */
export function revealIds(items, index, collapsed) {
  const ids = itemIds(items);
  return ancestorsOf(items, index).map((i) => ids[i]).filter((id) => collapsed.has(id));
}
