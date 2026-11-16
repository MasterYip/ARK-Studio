// Pure state helpers for the Editor workspace (VS Code style file tabs).
//
// Deliberately DOM-free and dependency-free so Node's test runner can import
// it in isolation (tests/js/editor_model.test.mjs).  Every operation returns a new
// state object; callers own persistence and rendering.
//
// A tab entry:
//   { key, title, kind: "ws" | "media" | "url" | "unit" | "diff", path?, mediaId?,
//     url?, uid?, unit?, base?, preview: bool, dirty: bool }
// key is stable per source: "ws:<project-relative path>", "media:<id>",
// "url:<href>", "unit:<uid>" (an ARK node) or "diff:<base>/<path>" (a git
// diff of a project file against a base ref); reopening the same source
// focuses the existing tab.

export const EDITOR_STORE_PREFIX = "ark.editor.v1.";
export const EDITOR_TAB_LIMIT = 40;
export const RECENT_LIMIT = 12;
// Text viewers never load more than this; it mirrors workspace MAX_FILE_BYTES.
export const TEXT_LIMIT_BYTES = 1024 * 1024;
export const CSV_ROW_LIMIT = 2000;

const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif"]);
const VIDEO_EXT = new Set(["mp4", "webm", "mov", "m4v", "ogv"]);
const AUDIO_EXT = new Set(["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"]);
const MARKDOWN_EXT = new Set(["md", "markdown", "mdx"]);
const MERMAID_EXT = new Set(["mmd", "mermaid"]);
const HTML_EXT = new Set(["html", "htm"]);
const JSON_EXT = new Set(["json", "webmanifest", "ipynb"]);
const CSV_EXT = new Set(["csv", "tsv"]);
// Known binary formats that must never be fetched as text.
const BINARY_EXT = new Set([
  "zip", "gz", "tgz", "bz2", "xz", "7z", "rar", "tar", "exe", "dll", "so", "dylib",
  "bin", "dat", "db", "sqlite", "sqlite3", "pkl", "pt", "pth", "ckpt", "safetensors",
  "npy", "npz", "h5", "hdf5", "parquet", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
  "key", "pages", "numbers", "psd", "ai", "eps", "ttf", "otf", "woff", "woff2",
  "class", "jar", "pyc", "o", "a", "lib", "iso", "dmg", "apk", "msi", "tiff", "tif", "heic",
]);

// Extension -> highlighter language understood by dom.js highlightCode().
/* i18n-ignore-start: language ids */
const LANGUAGES = {
  py: "python", pyi: "python", js: "javascript", mjs: "javascript", cjs: "javascript",
  jsx: "jsx", ts: "typescript", mts: "typescript", tsx: "tsx", json: "json",
  jsonl: "jsonl", ndjson: "jsonl", md: "markdown", markdown: "markdown", mdx: "markdown",
  sh: "bash", bash: "bash", zsh: "bash", ps1: "powershell", psm1: "powershell",
  bat: "bat", cmd: "bat", c: "c", h: "c", cc: "cpp", cpp: "cpp", cxx: "cpp", hpp: "cpp",
  cs: "csharp", css: "css", go: "go", html: "html", htm: "html", ini: "ini", cfg: "ini",
  conf: "ini", java: "java", rb: "ruby", rs: "rust", sql: "sql", toml: "toml",
  xml: "xml", svg: "xml", yaml: "yaml", yml: "yaml", webmanifest: "json", ipynb: "json",
};
/* i18n-ignore-end */
const TEXT_EXT = new Set([
  ...Object.keys(LANGUAGES), "txt", "text", "log", "csv", "tsv", "tex", "bib", "rst",
  "env", "gitignore", "gitattributes", "editorconfig", "lock", "r", "jl", "lua", "kt",
  "swift", "scala", "php", "pl", "vue", "svelte", "dockerfile", "makefile", "cmake", "gradle",
  "properties", "srt", "vtt",
]);
const TEXT_NAMES = new Set(["makefile", "dockerfile", "license", "readme", "procfile", "gemfile", "rakefile"]);

export const VIEWERS = ["node", "diff", "image", "video", "audio", "pdf", "markdown", "mermaid", "html", "json", "csv", "code", "link", "unsupported"];
const TEXT_VIEWERS = new Set(["markdown", "mermaid", "html", "json", "csv", "code"]);

// ------------------------------------------------------------------- keys
export function normalizePath(path) {
  return String(path || "").replace(/\\/g, "/").replace(/^\.\/+/, "").replace(/^\/+/, "")
    .split("/").filter((part) => part && part !== ".").join("/");
}

export function workspaceKey(path) { return `ws:${normalizePath(path)}`; }
export function mediaKey(id) { return `media:${Number(id) || 0}`; }
export function linkKey(url) { return `url:${String(url || "").trim()}`; }
// Unit uids look like "IDEA-0007"; numeric ids are accepted too.
const UID_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
export function isUnitRef(value) { return UID_RE.test(String(value ?? "")); }
export function unitKey(uid) { return `unit:${String(uid ?? "").trim()}`; }
// Git refs never contain "/"-free escapes we rely on, so the base is
// URI-encoded and the first "/" separates it from the path.
export function diffKey(path, base = "HEAD") {
  return `diff:${encodeURIComponent(String(base || "HEAD").trim() || "HEAD")}/${normalizePath(path)}`;
}

export function parseKey(key) {
  const text = String(key || "");
  const index = text.indexOf(":");
  if (index <= 0) return null;
  const kind = text.slice(0, index);
  const value = text.slice(index + 1);
  if (kind === "ws") {
    const path = normalizePath(value);
    return path ? { kind, path } : null;
  }
  if (kind === "media") {
    const id = Number(value);
    return Number.isInteger(id) && id > 0 ? { kind, mediaId: id } : null;
  }
  if (kind === "url") return /^[a-z][a-z0-9+.-]*:/i.test(value) ? { kind, url: value } : null;
  if (kind === "unit") return isUnitRef(value) ? { kind, uid: value } : null;
  if (kind === "diff") {
    const slash = value.indexOf("/");
    if (slash <= 0) return null;
    let base = "";
    try { base = decodeURIComponent(value.slice(0, slash)).trim(); } catch { return null; }
    const path = normalizePath(value.slice(slash + 1));
    return base && path ? { kind, path, base } : null;
  }
  return null;
}

export function routeForKey(key) {
  return key ? `#/editor/${encodeURIComponent(key)}` : "#/editor";
}

// `rest` is everything after "#/editor/" (it may itself contain slashes when a
// hand-written link was not encoded).
export function keyFromRoute(rest) {
  const text = String(rest || "").replace(/^\/+/, "");
  if (!text) return "";
  let key = text;
  try { key = decodeURIComponent(text); } catch { /* keep the raw text */ }
  return parseKey(key) ? key : "";
}

// ------------------------------------------------------------------ types
export function baseName(value) {
  const text = String(value || "").split(/[?#]/)[0].replace(/\\/g, "/");
  return text.split("/").filter(Boolean).pop() || String(value || "");
}

export function fileExtension(name) {
  const base = baseName(name).toLowerCase();
  if (base.startsWith(".") && base.indexOf(".", 1) < 0) return base.slice(1);
  const match = /\.([a-z0-9]+)$/.exec(base);
  return match ? match[1] : "";
}

export function languageFor(name) {
  const ext = fileExtension(name);
  if (LANGUAGES[ext]) return LANGUAGES[ext];
  const base = baseName(name).toLowerCase();
  if (base === "dockerfile" || base === "makefile") return "bash";
  return "";
}

function mimeViewer(mime) {
  const value = String(mime || "").toLowerCase();
  if (value === "image/svg+xml" || value.startsWith("image/")) return "image";
  if (value.startsWith("video/")) return "video";
  if (value.startsWith("audio/")) return "audio";
  if (value === "application/pdf") return "pdf";
  if (value === "text/markdown") return "markdown";
  if (value === "application/json") return "json";
  if (value === "text/csv") return "csv";
  if (value.startsWith("text/")) return "code";
  return "";
}

// Pick the viewer for one source.  `entry` may carry name/path/url/mime/kind.
export function viewerFor(entry = {}) {
  if (entry.kind === "unit") return "node";
  if (entry.kind === "diff") return "diff";
  const name = entry.name || entry.path || entry.url || "";
  const url = String(entry.url || "");
  const ext = fileExtension(name);
  const remote = /^https?:\/\//i.test(url);
  const otherScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(url) && !remote && !/^file:\/\//i.test(url);
  if (otherScheme) return "link";
  if (IMAGE_EXT.has(ext)) return "image";
  if (remote) return "link";
  if (VIDEO_EXT.has(ext)) return "video";
  if (AUDIO_EXT.has(ext)) return "audio";
  if (ext === "pdf") return "pdf";
  if (MARKDOWN_EXT.has(ext)) return "markdown";
  if (MERMAID_EXT.has(ext)) return "mermaid";
  if (HTML_EXT.has(ext)) return "html";
  if (JSON_EXT.has(ext)) return "json";
  if (CSV_EXT.has(ext)) return "csv";
  if (BINARY_EXT.has(ext)) return "unsupported";
  if (TEXT_EXT.has(ext) || TEXT_NAMES.has(baseName(name).toLowerCase())) return "code";
  const byMime = mimeViewer(entry.mime);
  if (byMime) return byMime;
  const kind = String(entry.mediaKind || entry.kind || "").toLowerCase();
  if (["image", "video", "audio", "pdf"].includes(kind)) return kind;
  // Unknown project files are tried as UTF-8 text (the server rejects binary);
  // unknown attachments get the info card.
  return entry.kind === "ws" || entry.source === "ws" ? "code" : "unsupported";
}

export function isTextViewer(viewer) { return TEXT_VIEWERS.has(viewer); }

// Project workspace text files and ARK nodes are editable; attachments are
// read-only.
export function isEditable(tab, viewer = viewerFor(tab)) {
  if (tab?.kind === "unit") return true;
  return Boolean(tab && tab.kind === "ws" && isTextViewer(viewer));
}

// --------------------------------------------------------------- tab state
export function createState() {
  return { tabs: [], active: "", mru: [] };
}

function cleanTab(raw) {
  if (!raw || typeof raw !== "object") return null;
  const parsed = parseKey(raw.key);
  if (!parsed) return null;
  const tab = {
    key: String(raw.key),
    kind: parsed.kind,
    title: String(raw.title || "").slice(0, 200)
      || (parsed.kind === "diff" ? `${baseName(parsed.path)} (${parsed.base})` : "")
      || parsed.uid || baseName(parsed.path || parsed.url || `attachment ${parsed.mediaId}`),
    preview: Boolean(raw.preview),
    dirty: Boolean(raw.dirty),
  };
  if (parsed.path) tab.path = parsed.path;
  if (parsed.mediaId) tab.mediaId = parsed.mediaId;
  if (parsed.url) tab.url = parsed.url;
  if (parsed.uid) tab.uid = parsed.uid;
  if (parsed.base) tab.base = parsed.base;
  for (const field of ["name", "mime", "mediaKind", "mediaUrl", "unit", "unitTitle", "caption", "rawUrl"]) {
    if (raw[field] !== undefined && raw[field] !== null && raw[field] !== "") {
      tab[field] = String(raw[field]).slice(0, 2000);
    }
  }
  return tab;
}

export function makeTab(source = {}) {
  let key = source.key;
  if (!key) {
    if (source.kind === "media") key = mediaKey(source.mediaId ?? source.id);
    else if (source.kind === "url") key = linkKey(source.url);
    else if (source.kind === "unit") key = unitKey(source.uid ?? source.id);
    else if (source.kind === "diff") key = diffKey(source.path, source.base);
    else key = workspaceKey(source.path);
  }
  return cleanTab({ ...source, key });
}

export function normalizeState(value) {
  const source = value && typeof value === "object" ? value : {};
  const seen = new Set();
  const tabs = [];
  let previewSeen = false;
  for (const raw of Array.isArray(source.tabs) ? source.tabs : []) {
    const tab = cleanTab(raw);
    if (!tab || seen.has(tab.key)) continue;
    // At most one preview tab may exist.
    if (tab.preview) {
      if (previewSeen) tab.preview = false;
      previewSeen = true;
    }
    seen.add(tab.key);
    tabs.push(tab);
    if (tabs.length >= EDITOR_TAB_LIMIT) break;
  }
  const keys = tabs.map((tab) => tab.key);
  const mru = [...new Set((Array.isArray(source.mru) ? source.mru : []).map(String))]
    .filter((key) => keys.includes(key));
  for (const key of keys) if (!mru.includes(key)) mru.push(key);
  let active = String(source.active || "");
  if (!keys.includes(active)) active = mru[0] || "";
  return { tabs, active, mru: active ? touch(mru, active) : mru };
}

function touch(mru, key) {
  return [key, ...mru.filter((item) => item !== key)];
}

export function findTab(state, key) {
  return (state?.tabs || []).find((tab) => tab.key === key) || null;
}

export function activeTab(state) {
  return findTab(state, state?.active);
}

// Open (or focus) a source.  With preview=true the tab is temporary: it
// replaces the existing clean preview tab instead of adding another one.
export function openTab(state, source, { preview = false } = {}) {
  const tab = makeTab(source);
  if (!tab) return state;
  const current = normalizeState(state);
  const existing = findTab(current, tab.key);
  if (existing) {
    // A source without its own title (e.g. "unit:<uid>" from a bare uid)
    // keeps the title the tab already shows.
    const title = source?.title ? tab.title : existingTitle(current, tab.key) || tab.title;
    const tabs = current.tabs.map((item) => (item.key === tab.key
      ? { ...item, ...tab, title, dirty: item.dirty, preview: preview ? item.preview : false }
      : item));
    return { tabs, active: tab.key, mru: touch(current.mru, tab.key) };
  }
  tab.preview = Boolean(preview);
  tab.dirty = false;
  let tabs = current.tabs.slice();
  let mru = current.mru.slice();
  const previewIndex = preview ? tabs.findIndex((item) => item.preview && !item.dirty) : -1;
  if (previewIndex >= 0) {
    mru = mru.filter((key) => key !== tabs[previewIndex].key);
    tabs[previewIndex] = tab;
  } else {
    const activeIndex = tabs.findIndex((item) => item.key === current.active);
    const at = activeIndex >= 0 ? activeIndex + 1 : tabs.length;
    tabs.splice(at, 0, tab);
    // Over the limit: drop the least recently used clean, non-active tabs.
    while (tabs.length > EDITOR_TAB_LIMIT) {
      const victim = [...mru].reverse().find((key) => {
        const item = tabs.find((entry) => entry.key === key);
        return item && !item.dirty && key !== tab.key;
      });
      if (!victim) break;
      tabs = tabs.filter((item) => item.key !== victim);
      mru = mru.filter((key) => key !== victim);
    }
  }
  return { tabs, active: tab.key, mru: touch(mru, tab.key) };
}

function existingTitle(state, key) {
  return findTab(state, key)?.title || "";
}

export function pinTab(state, key) {
  if (!findTab(state, key)) return state;
  return { ...state, tabs: state.tabs.map((tab) => (tab.key === key ? { ...tab, preview: false } : tab)) };
}

export function activateTab(state, key) {
  if (!findTab(state, key)) return state;
  return { ...state, active: key, mru: touch(state.mru || [], key) };
}

// Editing a preview tab pins it, like VS Code.
export function setDirty(state, key, dirty) {
  const tab = findTab(state, key);
  if (!tab || (tab.dirty === Boolean(dirty) && (!dirty || !tab.preview))) return state;
  return {
    ...state,
    tabs: state.tabs.map((item) => (item.key === key
      ? { ...item, dirty: Boolean(dirty), preview: dirty ? false : item.preview }
      : item)),
  };
}

export function renameTab(state, key, title) {
  if (!findTab(state, key)) return state;
  return { ...state, tabs: state.tabs.map((tab) => (tab.key === key ? { ...tab, title: String(title) } : tab)) };
}

export function moveTab(state, key, toIndex) {
  const from = state.tabs.findIndex((tab) => tab.key === key);
  if (from < 0) return state;
  const tabs = state.tabs.slice();
  const [tab] = tabs.splice(from, 1);
  const at = Math.max(0, Math.min(tabs.length, Number(toIndex) || 0));
  tabs.splice(at, 0, tab);
  return { ...state, tabs };
}

// Keys an action would close.  action: close | others | right | all | saved.
export function closeTargets(state, action, key = state?.active) {
  const tabs = state?.tabs || [];
  const index = tabs.findIndex((tab) => tab.key === key);
  switch (action) {
    case "close": return index >= 0 ? [key] : [];
    case "others": return index >= 0 ? tabs.filter((tab) => tab.key !== key).map((tab) => tab.key) : [];
    case "right": return index >= 0 ? tabs.slice(index + 1).map((tab) => tab.key) : [];
    case "left": return index >= 0 ? tabs.slice(0, index).map((tab) => tab.key) : [];
    case "all": return tabs.map((tab) => tab.key);
    case "saved": return tabs.filter((tab) => !tab.dirty).map((tab) => tab.key);
    default: return [];
  }
}

export function dirtyAmong(state, keys) {
  const wanted = new Set(keys);
  return (state?.tabs || []).filter((tab) => wanted.has(tab.key) && tab.dirty);
}

// Remove tabs; the active tab falls back to the most recently used survivor.
export function closeTabs(state, keys) {
  const remove = new Set(keys);
  const tabs = state.tabs.filter((tab) => !remove.has(tab.key));
  const mru = (state.mru || []).filter((key) => !remove.has(key));
  let active = state.active;
  if (!tabs.some((tab) => tab.key === active)) active = mru[0] || tabs[tabs.length - 1]?.key || "";
  return { tabs, active, mru: active ? touch(mru, active) : mru };
}

// ------------------------------------------------------------ persistence
export function storeKey(projectId) {
  return `${EDITOR_STORE_PREFIX}${String(projectId || "default")}`;
}

export function serializeState(state, recent = []) {
  const clean = normalizeState(state);
  return JSON.stringify({
    tabs: clean.tabs.map(({ dirty, ...tab }) => tab),
    active: clean.active,
    mru: clean.mru,
    recent: normalizeRecent(recent),
  });
}

export function parseStoredState(text) {
  let parsed = null;
  try { parsed = JSON.parse(text || "null"); } catch { parsed = null; }
  const state = normalizeState(parsed);
  // Unsaved buffers are not persisted, so nothing restored can be dirty.
  state.tabs = state.tabs.map((tab) => ({ ...tab, dirty: false }));
  return { state, recent: normalizeRecent(parsed?.recent) };
}

export function normalizeRecent(list) {
  const out = [];
  const seen = new Set();
  for (const raw of Array.isArray(list) ? list : []) {
    const tab = cleanTab(raw);
    if (!tab || seen.has(tab.key)) continue;
    seen.add(tab.key);
    const { preview, dirty, ...rest } = tab;
    out.push(rest);
    if (out.length >= RECENT_LIMIT) break;
  }
  return out;
}

export function pushRecent(list, source) {
  const tab = makeTab(source);
  if (!tab) return normalizeRecent(list);
  return normalizeRecent([tab, ...(Array.isArray(list) ? list : []).filter((item) => item?.key !== tab.key)]);
}

// ----------------------------------------------------------- data viewers
export function prettyJson(text) {
  try {
    return { ok: true, text: JSON.stringify(JSON.parse(String(text || "")), null, 2) };
  } catch (err) {
    return { ok: false, text: String(text || ""), error: String(err?.message || err) };
  }
}

// RFC 4180-ish CSV/TSV parser: quoted fields, doubled quotes, CRLF.
export function parseCsv(text, { delimiter = ",", maxRows = CSV_ROW_LIMIT } = {}) {
  const rows = [];
  let row = [];
  let field = "";
  let quoted = false;
  let truncated = false;
  const source = String(text || "");
  for (let i = 0; i < source.length; i += 1) {
    const ch = source[i];
    if (quoted) {
      if (ch === '"') {
        if (source[i + 1] === '"') { field += '"'; i += 1; } else quoted = false;
      } else field += ch;
      continue;
    }
    if (ch === '"' && field === "") quoted = true;
    else if (ch === delimiter) { row.push(field); field = ""; }
    else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && source[i + 1] === "\n") i += 1;
      row.push(field); field = "";
      rows.push(row); row = [];
      if (rows.length >= maxRows) { truncated = i < source.length - 1; break; }
    } else field += ch;
  }
  if (!truncated && (field !== "" || row.length)) { row.push(field); rows.push(row); }
  return { rows, truncated };
}

export function csvDelimiter(name, text = "") {
  if (fileExtension(name) === "tsv") return "\t";
  const first = String(text).split(/\r?\n/, 1)[0] || "";
  return (first.match(/\t/g) || []).length > (first.match(/,/g) || []).length ? "\t" : ",";
}
