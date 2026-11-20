import { icon } from "./icons.js";
import { t, locale, typeLabel } from "./i18n.js";
import { authenticatedUrl } from "./api.js";
import { resolveRichUrl, embedKind, hashText, isVizLang, EXPLICIT_ANCHOR_RE, headingAnchors } from "./rich-model.js";
import { vizBlock } from "./viz.js";
import { sanitizeRichHtml } from "./rich-html.js";
import { markdownTokens, markdownInlineTokens } from "./markdown.js";

import { TYPE_COLORS as FORCE_TYPE_COLORS, typeColor as graphTypeColor } from "./graph-core.js";
export const TYPE_COLORS = FORCE_TYPE_COLORS;
export function typeColor(type) { return graphTypeColor(type); }
export const STATUS_COLORS = {
  inbox: "#94a3b8", open: "#60a5fa", active: "#22c55e", review: "#f59e0b",
  blocked: "#ef4444", done: "#14b8a6", abandoned: "#b45309", archived: "#64748b",
};
export const IMP_COLORS = { p0: "#ef4444", p1: "#f59e0b", p2: "#3b82f6", p3: "#64748b" };

export function statusColor(status) { return STATUS_COLORS[status] || "#94a3b8"; }
export function impColor(imp) { return IMP_COLORS[imp] || "#94a3b8"; }

export function h(tag, props, ...children) {
  const el = document.createElement(tag);
  if (props && typeof props === "object" && !(props instanceof Node) && !Array.isArray(props)) {
    for (const [key, value] of Object.entries(props)) {
      if (value === undefined || value === null || value === false) continue;
      if (key === "class" || key === "className") el.className = value;
      else if (key === "text") el.textContent = value;
      else if (key === "html") el.innerHTML = value;
      else if (key === "style") {
        if (typeof value === "string") el.setAttribute("style", value.replace(/(font-size\s*:\s*)([\d.]+)px/gi,
          (_, prefix, size) => `${prefix}${Number(size) / 16}rem`));
        else {
          for (const [prop, raw] of Object.entries(value)) {
            if (raw === null || raw === undefined) continue;
            if (prop.startsWith("--")) el.style.setProperty(prop, String(raw));
            else el.style[prop] = prop === "fontSize" && /^[\d.]+px$/.test(String(raw))
              ? `${parseFloat(raw) / 16}rem` : raw;
          }
        }
      } else if (key === "dataset") Object.assign(el.dataset, value);
      else if (key.startsWith("on") && typeof value === "function") {
        el.addEventListener(key.slice(2).toLowerCase(), value);
      } else if (key === "value") el.value = value;
      else if (key === "checked" || key === "disabled" || key === "selected") el[key] = Boolean(value);
      else if (key === "ref" && typeof value === "function") value(el);
      else if (value === true) el.setAttribute(key, "");
      else el.setAttribute(key, value);
    }
  } else if (props !== undefined) {
    children.unshift(props);
  }
  append(el, children);
  return el;
}

export function append(el, children) {
  for (const child of children.flat(Infinity)) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else if (child instanceof Node) el.appendChild(child);
    else el.appendChild(document.createTextNode(String(child)));
  }
  return el;
}

export function clear(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
  return el;
}

export function mount(el, ...children) {
  clear(el);
  append(el, children);
  return el;
}

export function iconBtn(name, title, onclick, cls = "icon-btn") {
  return h("button", { class: cls, title, "aria-label": title, onclick }, icon(name));
}

export function typeBadge(type, extra = "") {
  return h("span", { class: `type-badge ${extra}`.trim(), style: { "--type-color": typeColor(type) } }, typeLabel(type));
}

export function statusPill(status, extra = "") {
  return h("span", { class: `status-pill status-${status} ${extra}`.trim(), style: { "--status-color": statusColor(status) } },
    h("i"), t(`status.${status}`) === `status.${status}` ? status : t(`status.${status}`));
}

export function impDot(importance) {
  return h("span", { class: "importance", style: { "--imp-color": impColor(importance) }, title: `${t("common.importance")}: ${importance}` },
    importance.toUpperCase());
}

export function tagChip(tag, { onClick = null, removable = false, onRemove = null, cls = "" } = {}) {
  const idx = tag.indexOf(":");
  const ns = idx > 0 ? tag.slice(0, idx) : "";
  const value = idx > 0 ? tag.slice(idx + 1) : tag;
  // ``needs:`` is set by ARK: show readable labels and never offer removal.
  const needs = ns === "needs";
  const el = h("span", { class: `tag ${cls}`.trim(), title: tag, onclick: onClick || undefined },
    ns ? h("span", { class: "tag-ns" }, needs ? t("needs.group") : ns) : null,
    ns ? ":" : null,
    h("span", null, needs ? t(`needs.value.${value}`) : value));
  if (removable && !needs) {
    el.appendChild(h("button", {
      class: "tag-remove", title: t("action.remove"), "aria-label": t("action.remove"),
      onclick: (e) => { e.stopPropagation(); onRemove ? onRemove(tag) : null; },
    }, "×"));
  }
  return el;
}

export function avatarFor(unit, size = "") {
  const name = unit.title || "?";
  const initials = name.replace(/[^\p{L}\p{N} ]/gu, "").split(/\s+/).filter(Boolean).slice(0, 2)
    .map((x) => x[0]).join("").toUpperCase() || "?";
  const color = typeColor(unit.type);
  return h("span", {
    class: `avatar ${size}`.trim(),
    style: { "--agent-color": color },
    title: `${unit.uid} · ${name}`,
  }, initials);
}

export function isImageUrl(url = "") {
  return /\.(png|jpe?g|webp|gif|svg|avif)(\?|#|$)/i.test(url);
}

export function fmtDate(value, { withYear = false } = {}) {
  if (!value) return "—";
  const d = new Date(value.length <= 10 ? `${value}T00:00:00` : value);
  if (Number.isNaN(d.getTime())) return value;
  const opts = { month: "short", day: "numeric" };
  if (withYear || d.getFullYear() !== new Date().getFullYear()) opts.year = "numeric";
  return new Intl.DateTimeFormat(locale(), opts).format(d);
}

export function fmtDateTime(value) {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return new Intl.DateTimeFormat(locale(), {
    year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  }).format(d);
}

export function relativeTime(value) {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  const diff = d.getTime() - Date.now();
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: "auto" });
  if (abs < 60_000) return t("common.just_now");
  if (abs < 3_600_000) return rtf.format(Math.round(diff / 60_000), "minute");
  if (abs < 86_400_000) return rtf.format(Math.round(diff / 3_600_000), "hour");
  if (abs < 2_592_000_000) return rtf.format(Math.round(diff / 86_400_000), "day");
  return fmtDate(value);
}

export function todayISO() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function daysBetween(a, b) {
  const left = new Date(`${a.length <= 10 ? a : a.slice(0, 10)}T00:00:00`);
  const right = new Date(`${b.length <= 10 ? b : b.slice(0, 10)}T00:00:00`);
  return Math.round((right - left) / 86_400_000);
}

export function dueState(unit) {
  if (!unit.end_at || ["done", "abandoned", "archived"].includes(unit.status)) return "";
  const today = todayISO();
  if (unit.end_at < today) return "overdue";
  if (daysBetween(today, unit.end_at) <= 3) return "soon";
  return "";
}

export function slug(text) {
  return String(text || "").toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 60);
}

export function clamp(n, min, max) { return Math.max(min, Math.min(max, n)); }

export function debounce(fn, ms = 180) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}

// Resolve a Markdown URL to something safe to put in src/href (see
// rich-model.js resolveRichUrl).  ARK API paths get the mobile token (if any)
// so attachments also load on paired phones.
function richUrl(raw, opts) {
  const resolved = resolveRichUrl(raw, { baseDir: opts?.baseDir || "" });
  if (!resolved) return null;
  const apiPath = /^\/api\//.test(resolved.href);
  return { ...resolved, src: apiPath ? authenticatedUrl(resolved.href) : resolved.href };
}

function mediaElement(kind, resolved, label) {
  if (kind === "video") {
    return h("video", {
      class: "rich-video", src: resolved.src, controls: true, preload: "metadata",
      playsinline: true, title: label || undefined,
    });
  }
  if (kind === "audio") {
    return h("audio", { class: "rich-audio", src: resolved.src, controls: true, preload: "metadata", title: label || undefined });
  }
  return h("img", { class: "rich-img", src: resolved.src, alt: label || "", loading: "lazy" });
}

const NOT_EMBEDDABLE = new Set(["file", "anchor", "mailto", "app"]);

// A tiny, dependency-free highlighter. It only ever creates text nodes and
// spans (no innerHTML), so code from an agent stays inert. Unknown languages
// fall back to plain monospace text.
const HIGHLIGHT_LANGUAGES = new Set([
  "bash", "bat", "c", "cpp", "csharp", "css", "go", "html", "ini", "java",
  "javascript", "json", "jsonl", "jsx", "markdown", "powershell", "python",
  "ruby", "rust", "sh", "sql", "toml", "tsx", "typescript", "xml", "yaml",
]);
const HIGHLIGHT_KEYWORDS = {
  python: new Set(("and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield self print".split(" "))),
  javascript: new Set(("async await break case catch class const continue debugger default delete do else export extends false finally for from function if import in instanceof let new null of return static super switch this throw true try typeof undefined var void while yield".split(" "))),
  typescript: new Set(("abstract any as async await boolean break case catch class const continue declare default delete do else enum export extends false finally for from function if implements import in instanceof interface let namespace never new null number of private protected public readonly return static string super switch this throw true try type typeof undefined var void while yield".split(" "))),
  bash: new Set(("case do done elif else esac fi for function if in select then until while time export local readonly return echo cd set unset".split(" "))),
  sh: new Set(("case do done elif else esac fi for function if in select then until while time export local readonly return echo cd set unset".split(" "))),
  sql: new Set(("select from where insert into update delete create table drop alter join left right inner outer on group by order having limit offset as and or not null values union distinct".split(" "))),
  yaml: new Set(("true false null yes no on off".split(" "))),
  json: new Set(("true false null".split(" "))),
  markdown: new Set([]),
};
HIGHLIGHT_KEYWORDS.jsx = HIGHLIGHT_KEYWORDS.javascript;
HIGHLIGHT_KEYWORDS.tsx = HIGHLIGHT_KEYWORDS.typescript;
HIGHLIGHT_KEYWORDS.jsonl = HIGHLIGHT_KEYWORDS.json;
HIGHLIGHT_KEYWORDS.toml = HIGHLIGHT_KEYWORDS.yaml;
HIGHLIGHT_KEYWORDS.ini = HIGHLIGHT_KEYWORDS.yaml;
const HIGHLIGHT_COMMENTS = {
  python: ["#"], bash: ["#"], sh: ["#"], powershell: ["#"], ruby: ["#"],
  yaml: ["#"], toml: ["#"], ini: ["#"], markdown: [],
  javascript: ["//", "/*"], typescript: ["//", "/*"], jsx: ["//", "/*"],
  tsx: ["//", "/*"], c: ["//", "/*"], cpp: ["//", "/*"], csharp: ["//", "/*"],
  java: ["//", "/*"], go: ["//", "/*"], rust: ["//", "/*"], css: ["/*"],
  sql: ["--"], bat: ["::"], html: ["<!--"], xml: ["<!--"],
};
const HIGHLIGHT_TOKEN = /(\/\*[\s\S]*?\*\/|\/\/[^\n]*|#[^\n]*|--[^\n]*|::[^\n]*|<!--[\s\S]*?-->|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`|\b\d+(?:\.\d+)?\b|\b[A-Za-z_$][\w$]*\b)/g;

export function highlightCode(source, language = "") {
  const text = String(source ?? "");
  const fragment = document.createDocumentFragment();
  const lang = String(language || "").toLowerCase();
  if (!HIGHLIGHT_LANGUAGES.has(lang) || text.length > 80000) {
    fragment.appendChild(document.createTextNode(text));
    return fragment;
  }
  const keywords = HIGHLIGHT_KEYWORDS[lang] || HIGHLIGHT_KEYWORDS.javascript;
  const comments = HIGHLIGHT_COMMENTS[lang] || [];
  const literals = new Set(["true", "false", "null", "True", "False", "None", "undefined"]); // i18n-ignore: code literals
  let last = 0;
  for (const match of text.matchAll(HIGHLIGHT_TOKEN)) {
    if (match.index > last) {
      fragment.appendChild(document.createTextNode(text.slice(last, match.index)));
    }
    const token = match[0];
    let cls = "";
    if (token.startsWith("//") || token.startsWith("#") || token.startsWith("--")
        || token.startsWith("::") || token.startsWith("/*") || token.startsWith("<!--")) {
      if (comments.some((prefix) => token.startsWith(prefix))) cls = "tok-com";
    } else if (/^["'`]/.test(token)) {
      cls = "tok-str";
    } else if (/^\d/.test(token)) {
      cls = "tok-num";
    } else if (literals.has(token)) {
      cls = "tok-lit";
    } else if (keywords.has(token)) {
      cls = "tok-kw";
    }
    if (cls) fragment.appendChild(h("span", { class: cls }, token));
    else fragment.appendChild(document.createTextNode(token));
    last = match.index + token.length;
  }
  if (last < text.length) fragment.appendChild(document.createTextNode(text.slice(last)));
  return fragment;
}

export function unitReference(ref) {
  const href = new URL(location.href);
  href.searchParams.set("unit", ref);
  return h("a", {
    class: "rich-unit-link", href: href.href, "data-unit-ref": ref,
    "aria-label": t("refs.open", { ref }),
  }, icon("file", "sm"), h("span", { class: "rich-unit-label" }, ref));
}

function inlineNodes(text, opts = {}) {
  const tokens = Array.isArray(text) ? text : markdownInlineTokens(text);
  const slots = [];
  const source = tokens.map((token) => {
    if (token.type === "html") return token.raw;
    const index = slots.push(inlineTokenNodes(token, opts)) - 1;
    return `<ark-md-slot data-ark-slot="${index}"></ark-md-slot>`;
  }).join("");
  return [...htmlNodes(source, opts, slots).childNodes];
}

// Block tokens that get a source-line marker.
const BLOCK_TOKENS = new Set(["heading", "paragraph", "list", "table", "code", "blockquote", "hr", "html", "arkMathBlock"]);

// Line numbers for lexer tokens: each token's raw text is found in the
// source after the previous one (list items and nested blocks inside
// their parent), and its offset turned into a 1-based line.
function sourceLineAnnotator(source) {
  const breaks = [];
  for (let i = source.indexOf("\n"); i >= 0; i = source.indexOf("\n", i + 1)) breaks.push(i);
  const lineAt = (offset) => {
    let lo = 0, hi = breaks.length;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (breaks[mid] < offset) lo = mid + 1; else hi = mid; }
    return lo + 1;
  };
  const annotate = (items, from) => {
    let cursor = from;
    for (const token of items || []) {
      const raw = String(token.raw || "");
      // Skip the blank lines a raw block may start with.
      const lead = raw.length - raw.replace(/^\n+/, "").length;
      const at = raw ? source.indexOf(raw, cursor) : -1;
      if (at < 0) continue;
      token.__line = lineAt(at + lead);
      // Lists: their items, then the blocks inside each item.
      if (token.type === "list") annotate(token.items, at);
      else if (token.type === "list_item") annotate(token.tokens, at);
      cursor = at + raw.length;
    }
  };
  return { annotate };
}

// Move each <ark-line> marker's line onto the block it precedes (or onto
// its list item) and drop the marker.
function applySourceLines(root) {
  for (const marker of [...root.querySelectorAll("ark-line")]) {
    const parent = marker.parentElement;
    let target = parent && parent !== root && parent.tagName === "LI" && parent.firstChild === marker
      ? parent : marker.nextElementSibling;
    while (target && target.tagName === "ARK-LINE") target = target.nextElementSibling;
    if (target && !target.hasAttribute("data-source-line")) target.setAttribute("data-source-line", marker.getAttribute("data-line"));
    marker.remove();
  }
}

function htmlNodes(source, opts, slots = []) {
  const fragment = sanitizeRichHtml(source);
  // Raw HTML blocks bypass Markdown parsing, but their visible references
  // should still use the same capsules. Never turn literal code into links.
  const walker = document.createTreeWalker(fragment, NodeFilter.SHOW_TEXT);
  const texts = [];
  while (walker.nextNode()) texts.push(walker.currentNode);
  for (const text of texts) {
    if (text.parentElement?.closest("a,code,pre,kbd,samp,ark-md-slot")) continue;
    const nodes = [], pattern = /\\?\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]/g;
    let last = 0;
    for (const match of text.data.matchAll(pattern)) {
      nodes.push(document.createTextNode(text.data.slice(last, match.index)),
        match[0].startsWith("\\") ? document.createTextNode(match[0].slice(1)) : unitReference(match[1]));
      last = match.index + match[0].length;
    }
    if (last) text.replaceWith(...nodes, document.createTextNode(text.data.slice(last)));
  }
  for (const slot of fragment.querySelectorAll("ark-md-slot")) {
    slot.replaceWith(...(slots[Number(slot.getAttribute("data-ark-slot"))] || []));
  }
  for (const element of fragment.querySelectorAll("[src],[poster]")) {
    for (const attribute of ["src", "poster"]) {
      if (!element.hasAttribute(attribute)) continue;
      const resolved = richUrl(element.getAttribute(attribute), opts);
      if (resolved && !NOT_EMBEDDABLE.has(resolved.kind)) element.setAttribute(attribute, resolved.src);
      else element.removeAttribute(attribute);
    }
  }
  for (const image of fragment.querySelectorAll("img")) {
    image.classList.add("rich-img");
    image.loading = "lazy";
  }
  for (const media of fragment.querySelectorAll("video,audio")) {
    media.classList.add(`rich-${media.localName}`);
    media.controls = true;
    media.preload = "metadata";
    if (media.localName === "video") media.playsInline = true;
  }
  for (const link of fragment.querySelectorAll("a[href]")) {
    if (link.classList.contains("rich-unit-link")) continue;
    const resolved = richUrl(link.getAttribute("href"), opts);
    if (!resolved) { link.removeAttribute("href"); continue; }
    link.href = resolved.src;
    link.rel = "noreferrer noopener";
    link.removeAttribute("target");
    if (resolved.kind === "file") {
      link.classList.add("rich-local-link");
      link.dataset.localHref = resolved.href;
    } else if (resolved.kind === "app") link.classList.add("rich-app-link");
    else if (resolved.kind === "workspace") {
      link.classList.add("rich-ws-link");
      link.dataset.wsPath = resolved.path;
    } else if (resolved.kind !== "anchor") link.target = "_blank";
    if (resolved.kind === "media") link.dataset.mediaId = resolved.mediaId;
  }
  for (const table of fragment.querySelectorAll("table")) table.classList.add("md-table");
  return fragment;
}

function inlineTokenNodes(token, opts) {
  const children = () => inlineNodes(token.tokens || [{ type: "text", text: token.text || "" }], opts);
  if (token.type === "strong") return [h("strong", null, children())];
  if (token.type === "em") return [h("em", null, children())];
  if (token.type === "del") return [h("del", null, children())];
  if (token.type === "br") return [h("br")];
  if (token.type === "codespan") return [h("code", null, token.text)];
  if (token.type === "arkReference") return [unitReference(token.text)];
  if (token.type === "arkLiteral") return [document.createTextNode(`[[${token.text}]]`)];
  if (token.type === "arkCitation") return [h("sup", { class: "citation" }, token.raw)];
  if (token.type === "arkMath") return [h("span", {
    class: "math-inline", "data-tex": token.text, title: token.text,
  }, token.text)];
  if (["link", "image", "arkMedia"].includes(token.type)) {
    const resolved = richUrl(token.href, opts);
    if (!resolved) return [document.createTextNode(token.raw)];
    const kind = !NOT_EMBEDDABLE.has(resolved.kind)
      ? embedKind(resolved, { label: token.text, kinds: opts.mediaKinds }) : "";
    if (token.type === "image") {
      return NOT_EMBEDDABLE.has(resolved.kind) ? [document.createTextNode(token.raw)]
        : [mediaElement(kind || "image", resolved, token.text)];
    }
    if (kind === "video" || kind === "audio" || (kind === "image" && resolved.kind === "media")) {
      return [h("span", { class: "rich-media-figure" }, mediaElement(kind, resolved, token.text),
        token.type === "link" && token.raw.startsWith("[") ? h("a", {
          class: "rich-media-caption", href: resolved.src, target: "_blank", rel: "noreferrer noopener",
        }, children()) : null)];
    }
    const local = resolved.kind === "file", app = resolved.kind === "app";
    return [h("a", {
      href: local ? resolved.href : resolved.src,
      target: local || app || resolved.kind === "anchor" ? undefined : "_blank",
      rel: "noreferrer noopener", title: token.title || (local || app ? resolved.href : undefined),
      class: local ? "rich-local-link" : app ? "rich-app-link" : resolved.kind === "workspace" ? "rich-ws-link" : undefined,
      "data-local-href": local ? resolved.href : undefined,
      "data-ws-path": resolved.kind === "workspace" ? resolved.path : undefined,
      "data-media-id": resolved.kind === "media" ? resolved.mediaId : undefined,
    }, children())];
  }
  // Decode character entities as text without interpreting tags.
  const decoder = document.createElement("textarea");
  decoder.innerHTML = String(token.text ?? token.raw ?? "").replace(/</g, "&lt;");
  return [document.createTextNode(decoder.value)];
}

let katexPromise = null;

export function typesetMath(root) {
  const nodes = [...root.querySelectorAll("[data-tex]:not(.math-done)")];
  if (!nodes.length) return;
  if (!katexPromise) {
    katexPromise = new Promise((resolve, reject) => {
      if (window.katex) { resolve(window.katex); return; }
      const script = document.createElement("script");
      script.src = "/vendor/katex/katex.min.js";
      script.async = true;
      script.onload = () => window.katex ? resolve(window.katex) : reject(new Error("KaTeX unavailable"));
      script.onerror = () => reject(new Error("KaTeX failed to load"));
      document.head.appendChild(script);
    }).catch(() => null);
  }
  katexPromise.then((katex) => {
    if (!katex) {
      for (const node of nodes) node.classList.add("math-fallback");
      return;
    }
    if (!root.isConnected) return;
    for (const node of nodes) {
      if (!node.isConnected) continue;
      try {
        katex.render(node.dataset.tex || "", node, {
          displayMode: node.dataset.display === "1",
          throwOnError: false,
          strict: false,
          trust: false,
        });
        node.classList.add("math-done");
      } catch {
        node.classList.add("math-fallback");
        /* keep the TeX text */
      }
    }
  });
}

let mermaidPromise = null;

function loadMermaid() {
  if (!mermaidPromise) {
    mermaidPromise = new Promise((resolve, reject) => {
      if (window.mermaid) { resolve(window.mermaid); return; }
      const script = document.createElement("script");
      script.src = "/vendor/mermaid/mermaid.min.js";
      script.async = true;
      script.onload = () => window.mermaid ? resolve(window.mermaid) : reject(new Error("Mermaid unavailable"));
      script.onerror = () => reject(new Error("Mermaid failed to load"));
      document.head.appendChild(script);
    }).catch(() => null);
  }
  return mermaidPromise;
}

// Rendered diagrams are cached by theme+source so live previews re-use the
// SVG synchronously while typing (no flicker), and renders are serialised
// because Mermaid keeps global state.
const mermaidCache = new Map();
const MERMAID_CACHE_LIMIT = 80;
let mermaidQueue = Promise.resolve();
let mermaidSeq = 0;

function mermaidTheme() {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "default";
}

function mermaidCacheKey(source) {
  return `${mermaidTheme()}|${hashText(source)}|${source.length}`;
}

function paintMermaid(node, entry, source) {
  if (entry.svg) {
    // securityLevel "strict": Mermaid sanitises labels (DOMPurify) and
    // disables click handlers/HTML labels before producing this SVG.
    node.innerHTML = entry.svg;
    node.classList.add("rendered");
    node.classList.remove("error");
    return;
  }
  node.classList.add("error");
  node.classList.remove("rendered");
  node.replaceChildren(
    h("div", { class: "mermaid-error-head" }, icon("alert", "sm"), h("span", null, `Mermaid: ${entry.error}`)),
    h("pre", { class: "mermaid-error-source" }, source));
}

function rememberMermaid(key, entry) {
  mermaidCache.set(key, entry);
  if (mermaidCache.size > MERMAID_CACHE_LIMIT) mermaidCache.delete(mermaidCache.keys().next().value);
}

// Diagrams follow ARK's light/dark theme: when <html data-theme> flips,
// every rendered diagram is re-rendered (its old SVG stays until then).
let mermaidThemeWatch = false;
function watchMermaidTheme() {
  if (mermaidThemeWatch || typeof MutationObserver !== "function") return;
  mermaidThemeWatch = true;
  new MutationObserver(() => {
    const blocks = [...document.querySelectorAll(".mermaid-block.rendered, .mermaid-block.error")];
    const roots = new Set();
    for (const block of blocks) {
      block.classList.remove("rendered", "error");
      roots.add(block.closest(".rich") || block.parentElement);
    }
    for (const root of roots) if (root) typesetMermaid(root);
  }).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
}

export function typesetMermaid(root) {
  const nodes = [...root.querySelectorAll("[data-mermaid]")].filter((node) => !node.classList.contains("rendered") && !node.classList.contains("error"));
  if (!nodes.length) return;
  watchMermaidTheme();
  for (const node of nodes) {
    const cached = mermaidCache.get(mermaidCacheKey(node.dataset.mermaid || ""));
    if (cached) paintMermaid(node, cached, node.dataset.mermaid || "");
  }
  const pending = nodes.filter((node) => !node.classList.contains("rendered") && !node.classList.contains("error"));
  if (!pending.length) return;
  loadMermaid().then((mermaid) => {
    if (!mermaid) {
      for (const node of pending) node.classList.add("mermaid-unavailable");
      return;
    }
    mermaidQueue = mermaidQueue.then(async () => {
      if (!root.isConnected) return;
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        theme: mermaidTheme(),
        fontFamily: "system-ui, 'Segoe UI', 'Microsoft YaHei'",
      });
      for (const node of pending) {
        if (!node.isConnected) continue;
        const source = node.dataset.mermaid || "";
        const key = mermaidCacheKey(source);
        let entry = mermaidCache.get(key);
        if (!entry) {
          mermaidSeq += 1;
          const id = `ark-mermaid-${mermaidSeq}`;
          try {
            const result = await mermaid.render(id, source);
            entry = { svg: result.svg };
          } catch (err) {
            entry = { error: String(err?.message || err).split("\n")[0].slice(0, 300) };
          } finally {
            // A failed render can leave its scratch element in <body>.
            document.getElementById(id)?.remove();
            document.getElementById(`d${id}`)?.remove();
          }
          rememberMermaid(key, entry);
        }
        if (node.isConnected) paintMermaid(node, entry, source);
      }
    }).catch(() => {});
  });
}

// Marked parses the document; DOMPurify sanitises embedded HTML. ARK's
// references, media, math and approved visualisations keep their own controls.
// opts: { baseDir: project dir for relative URLs, mediaKinds: Map(id -> kind),
//         vizScope: stable prefix so a run visualisation stays approved }
// `opts.sourceLines`: tag rendered blocks and list items with
// data-source-line (1-based line of the Markdown source) so a preview can
// scroll in step with its source (scroll-sync.js).  The attributes are
// added after sanitising, from markers ARK inserts itself.
export function richText(text = "", opts = {}) {
  const source = String(text || "").replace(/^\uFEFF/, "").replace(/\r\n/g, "\n");
  const tokens = markdownTokens(source);
  const slots = [];
  let vizIndex = 0;
  const slot = (nodes) => `<ark-md-slot data-ark-slot="${slots.push(nodes) - 1}"></ark-md-slot>`;
  const lines = opts.sourceLines ? sourceLineAnnotator(source) : null;
  if (lines) lines.annotate(tokens, 0);
  const mark = (token) => {
    if (!lines || !token.__line) return "";
    const marker = document.createElement("ark-line");
    marker.setAttribute("data-line", String(token.__line));
    return slot([marker]);
  };
  const inline = (items) => slot(inlineNodes(items, opts));
  const markup = (items) => items.map((token) => (BLOCK_TOKENS.has(token.type) ? mark(token) : "") + markupOne(token)).join("");
  const markupOne = (token) => {
    if (token.type === "html") return token.raw;
    if (token.type === "space" || token.type === "def") return "";
    if (token.type === "checkbox") return slot([h("input", {
      type: "checkbox", disabled: true, checked: token.checked, "aria-label": t("status.done"),
    })]);
    if (token.type === "hr") return "<hr>";
    if (token.type === "heading") return `<h${token.depth}>${inline(token.tokens)}</h${token.depth}>`;
    if (token.type === "blockquote") return `<blockquote>${markup(token.tokens)}</blockquote>`;
    if (token.type === "list") {
      const tag = token.ordered ? "ol" : "ul";
      return `<${tag}${token.ordered ? ` start="${Number(token.start) || 1}"` : ""}>`
        + token.items.map((item) => `<li>${mark(item)}${markup(item.tokens)}</li>`).join("") + `</${tag}>`;
    }
    if (token.type === "table") {
      const cell = (value, index, tag) => `<${tag}${token.align[index] ? ` align="${token.align[index]}"` : ""}>${inline(value.tokens)}</${tag}>`;
      return `<table><thead><tr>${token.header.map((v, i) => cell(v, i, "th")).join("")}</tr></thead>`
        + `<tbody>${token.rows.map((row) => `<tr>${row.map((v, i) => cell(v, i, "td")).join("")}</tr>`).join("")}</tbody></table>`;
    }
    if (token.type === "arkMathBlock") return slot([h("div", {
      class: "math-block", "data-tex": token.text, "data-display": "1",
    }, token.text)]);
    if (token.type === "code") {
      vizIndex += 1;
      return slot([markdownCodeBlock(token.text, token.lang || "", opts, vizIndex)]);
    }
    if (token.type === "paragraph") return `<p>${inline(token.tokens)}</p>`;
    if (token.type === "text") return inline(token.tokens || [{ type: "text", text: token.text }]);
    return slot([document.createTextNode(token.raw || "")]);
  };
  const root = h("div", { class: "rich" }, htmlNodes(markup(tokens), opts, slots));
  if (lines) applySourceLines(root);
  applyAnchors(root);
  typesetMath(root);
  typesetMermaid(root);
  return root;
}

// Review anchors: headings get data-anchor="<slug>" (rich-model.js
// anchorSlug), and a heading or paragraph ending in "{#id}" gets that id
// with the marker hidden.  Review notes link to them (needs-ui.js).
function takeExplicitAnchor(el) {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  let last = null;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) if (node.nodeValue.trim()) last = node;
  const match = last ? EXPLICIT_ANCHOR_RE.exec(last.nodeValue) : null;
  if (!match) return "";
  last.nodeValue = last.nodeValue.slice(0, match.index);
  return match[1];
}

function applyAnchors(root) {
  const headings = [...root.querySelectorAll("h1, h2, h3, h4, h5, h6")];
  const ids = headingAnchors(headings.map((el) => {
    const explicit = takeExplicitAnchor(el);
    return { text: el.textContent, explicit };
  }));
  headings.forEach((el, index) => { if (ids[index]) el.dataset.anchor = ids[index]; });
  for (const el of root.querySelectorAll("p, li")) {
    const explicit = takeExplicitAnchor(el);
    if (explicit) el.dataset.anchor = explicit;
  }
}

function markdownCodeBlock(source, lang, opts, vizIndex) {
  const langId = String(lang).toLowerCase().split(/\s+/)[0];
  if (langId === "mermaid") {
    const block = h("div", { class: "mermaid-block", "data-mermaid": source }, source);
    const cached = mermaidCache.get(mermaidCacheKey(source));
    if (cached) paintMermaid(block, cached, source);
    return block;
  }
  if (isVizLang(langId)) return vizBlock(source, { key: opts.vizScope ? `${opts.vizScope}#${vizIndex}` : "" });
  return h("div", { class: "code-block" },
    h("div", { class: "code-block-head" },
      h("span", { class: "code-lang" }, lang || "code"), // i18n-ignore: language id
      h("button", {
        class: "code-copy", type: "button",
        onclick: async (event) => {
          const button = event.currentTarget;
          await copyText(source);
          button.textContent = t("action.copied");
          setTimeout(() => { button.textContent = t("action.copy"); }, 1200);
        },
      }, t("action.copy"))),
    h("pre", { class: "code-block-pre" }, h("code", {
      class: langId ? `language-${langId}` : "",
    }, highlightCode(source, langId))));
}

export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = h("textarea", { style: { position: "fixed", left: "-9999px" } });
    area.value = text;
    document.body.appendChild(area);
    area.select();
    let ok = false;
    try { ok = document.execCommand("copy"); } catch { ok = false; }
    area.remove();
    return ok;
  }
}

export function download(filename, content, type = "text/plain;charset=utf-8") {
  const blob = content instanceof Blob ? content : new Blob([content], { type });
  const url = URL.createObjectURL(blob);
  const a = h("a", { href: url, download: filename });
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1200);
}

export function percent(a, b) { return b ? Math.round((a / b) * 100) : 0; }

export function groupBy(arr, fn) {
  const out = new Map();
  for (const item of arr) {
    const key = fn(item);
    if (!out.has(key)) out.set(key, []);
    out.get(key).push(item);
  }
  return out;
}

export function linksLabel(n) {
  return `${n} ${n === 1 ? t("common.link") : t("common.links")}`;
}

export function emptyState(title, hint = "", action = null) {
  return h("div", { class: "empty" }, h("b", null, title), hint ? h("div", null, hint) : null, action);
}

export function iconText(name, text, cls = "") {
  return h("span", { class: cls, style: { display: "inline-flex", alignItems: "center", gap: "5px" } }, icon(name, "sm"), text);
}
