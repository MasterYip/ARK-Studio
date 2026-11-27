// Pure helpers behind the rich Markdown renderer (dom.js richText) and the
// sandboxed HTML visualisation frames.  DOM-free and dependency-free so
// Node's test runner can import it (tests/js/rich_model.test.mjs).

export const VIDEO_EXTENSIONS = ["mp4", "webm", "mov", "m4v", "ogv"];
export const AUDIO_EXTENSIONS = ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"];
export const IMAGE_EXTENSIONS = ["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif"];

// Visualisation frame limits.
export const VIZ_MIN_HEIGHT = 60;
export const VIZ_MAX_HEIGHT = 4000;
export const VIZ_DEFAULT_HEIGHT = 320;
export const VIZ_MAX_SOURCE = 512 * 1024;
export const VIZ_FRAME_PATH = "viz-frame.html";

// ------------------------------------------------------------------- URLs
function extOf(url) {
  const path = String(url || "").split(/[?#]/)[0];
  const match = /\.([a-z0-9]+)$/i.exec(path);
  return match ? match[1].toLowerCase() : "";
}

function normalizeRel(path) {
  const out = [];
  for (const part of String(path || "").replace(/\\/g, "/").split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (!out.length) return null; // escapes the project root
      out.pop();
    } else out.push(part);
  }
  return out.join("/");
}

const MEDIA_RAW_RE = /^\/api\/media\/(\d+)\/raw(?:[?#].*)?$/;

/**
 * Resolve a URL written in Markdown.
 *   https://… / http://…      remote (as is)
 *   /api/media/7/raw, media:7 ARK attachment (mediaId 7)
 *   /anything, #anchor, ?query, mailto: as is
 *   file:…                    local file (opened through the host, not loaded)
 *   vscode://…, cursor://…    desktop app link (handed to the OS protocol handler)
 *   relative/path.png         project file: /api/workspace/raw?path=<baseDir/…>
 * Anything else (javascript:, data:, vbscript:, …) resolves to null.
 */
// Desktop apps that register a URL protocol.  These are safe to hand to the
// browser: they only open the app on the viewer's own machine.
export const APP_SCHEMES = ["vscode", "vscode-insiders", "vscodium", "cursor", "windsurf", "obsidian", "zotero"];
const APP_SCHEME_RE = new RegExp(`^(${APP_SCHEMES.join("|")}):`, "i");

/** Plain path from a file: URL for display/copy (file:///E:/a%20b -> E:/a b). */
export function fileUrlToPath(href) {
  let path = String(href || "").replace(/^file:(\/\/)?/i, "");
  if (/^\/[A-Za-z]:[\\/]/.test(path)) path = path.slice(1);
  try { path = decodeURIComponent(path); } catch { /* keep raw */ }
  return path;
}

/** True when the page is served from this computer, so the host can open local paths. */
export function isLoopbackHost(hostname) {
  const host = String(hostname || "").replace(/^\[|\]$/g, "").toLowerCase();
  return host === "localhost" || host === "::1" || /^127\./.test(host);
}

/** Default address of the loopback local-open bridge of a browser-side ARK (Python tray). */
export const LOCAL_OPEN_BRIDGE = "http://127.0.0.1:8890";

/**
 * How "Open on this computer" reaches the viewer's machine for a page served
 * by another host.  Inside the ARK Studio desktop app (window.__ARK_DESKTOP__)
 * the app opens the path itself, whatever port its own server uses;
 * otherwise the page posts to a loopback bridge (desktop.localOpenUrl if the
 * shell injected one, else the classic tray port 8890).
 */
export function localOpenRoute(desktop) {
  if (desktop && typeof desktop.openLocal === "function") return { kind: "desktop" };
  const base = String(desktop?.localOpenUrl || LOCAL_OPEN_BRIDGE).replace(/\/+$/, "");
  return { kind: "bridge", url: `${base}/api/local/open` };
}

/**
 * Whether a path from a file: link plausibly exists on the viewer's computer:
 * a drive path on Windows; inside the desktop app also an absolute POSIX path
 * on macOS/Linux.
 */
export function isLikelyLocalPath(path, { userAgent = "", desktop = null } = {}) {
  const text = String(path || "");
  const windows = desktop?.platform ? desktop.platform === "windows" : /Windows/i.test(String(userAgent));
  if (windows) return /^[A-Za-z]:[\\/]/.test(text);
  return Boolean(desktop?.platform) && text.startsWith("/") && !text.startsWith("//");
}

export function resolveRichUrl(raw, { baseDir = "" } = {}) {
  const url = String(raw ?? "").trim();
  if (!url) return null;
  if (/^https?:\/\//i.test(url)) return { kind: "remote", href: url };
  const media = /^media:(\d+)$/i.exec(url);
  if (media) return { kind: "media", mediaId: Number(media[1]), href: `/api/media/${media[1]}/raw` };
  const rawMedia = MEDIA_RAW_RE.exec(url);
  if (rawMedia) return { kind: "media", mediaId: Number(rawMedia[1]), href: url };
  if (url.startsWith("//")) return null; // protocol-relative: ambiguous host
  if (url.startsWith("/")) return { kind: "absolute", href: url };
  if (url.startsWith("#")) return { kind: "anchor", href: url };
  // "?move_plan=<id>" and similar: an in-app link on this page, not a file.
  if (url.startsWith("?")) return { kind: "query", href: url };
  if (/^mailto:/i.test(url)) return { kind: "mailto", href: url };
  if (/^file:/i.test(url)) return { kind: "file", href: url };
  const task = parseTaskRef(url);
  if (task) return { kind: "task", href: url, rel: task.rel, space: task.space };
  if (APP_SCHEME_RE.test(url)) return { kind: "app", href: url };
  if (/^[a-z][a-z0-9+.-]*:/i.test(url)) return null;
  const [pathPart, suffix = ""] = url.split(/(?=[?#])/, 2);
  let decoded = pathPart;
  try { decoded = decodeURIComponent(pathPart); } catch { /* keep raw */ }
  const path = normalizeRel(`${normalizeRel(baseDir) ?? ""}/${decoded}`);
  if (!path) return null;
  return {
    kind: "workspace", path,
    href: `/api/workspace/raw?path=${encodeURIComponent(path)}${suffix.startsWith("#") ? suffix : ""}`,
  };
}

// ------------------------------------------------------- task-space paths
// "task:<relative path>[?space=<id>]" names a place inside the project's
// task space (server: setup/pathtext.go).  Each device resolves it with its
// own task-space location, so the link works on every computer.

/** { rel, space } of a task: reference, or null (absolute or escaping paths are not task paths). */
export function parseTaskRef(raw) {
  const text = String(raw ?? "").trim();
  if (!/^task:/i.test(text)) return null;
  let rest = text.slice(5);
  let space = "";
  const q = rest.indexOf("?");
  if (q >= 0) {
    const params = new URLSearchParams(rest.slice(q + 1));
    space = String(params.get("space") || "").trim();
    rest = rest.slice(0, q);
  }
  try { rest = decodeURIComponent(rest); } catch { /* keep raw */ }
  rest = rest.replace(/\\/g, "/");
  if (rest.startsWith("/") || /^[A-Za-z]:/.test(rest)) return null;
  if (rest.split("/").includes("..")) return null;
  return { rel: rest.replace(/^\.\//, ""), space };
}

/** Absolute path of rel inside root, in root's separator style. */
export function joinTaskPath(root, rel) {
  const base = String(root || "");
  const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  let out = base.replace(/[\\/]+$/, "");
  if (!out || /^[A-Za-z]:$/.test(out)) out += sep;
  else if (rel) out += sep;
  return rel ? out + String(rel).replace(/\//g, sep) : out;
}

/**
 * Where a task: reference points on each of the viewer's devices, from the
 * setup payload (GET /api/skills/setup/state: resources + devices).  Returns
 * { space: {id, name} | null, rows: [{ id, label, current, path | null }] }.
 * The default task space is the server's choice (setup/taskspace.go): a
 * read-write one first, then the one on most devices, then setup order.
 */
export function taskPathsFor(state, ref) {
  const parsed = typeof ref === "string" ? parseTaskRef(ref) : ref;
  if (!parsed) return { space: null, rows: [] };
  const local = (p) => /^([A-Za-z]:[\\/]|\/|~)/.test(String(p || ""));
  const spaces = (state?.resources || []).filter((r) => r.kind === "task_space"
    && r.details?.kind !== "issue-tracker"
    && (!(r.locations || []).length || (r.locations || []).some((l) => local(l.path_or_ref))));
  let space = null;
  if (parsed.space) space = spaces.find((r) => r.id === parsed.space) || null;
  else {
    let best = -1;
    for (const r of spaces) {
      const hosts = new Set((r.locations || []).filter((l) => local(l.path_or_ref)).map((l) => l.host_id));
      const score = (r.details?.access === "read-write" ? 1000 : 0) + hosts.size;
      if (score > best) { best = score; space = r; }
    }
  }
  if (!space) return { space: null, rows: [] };
  const rows = (state?.devices || []).map((device) => {
    const entry = (device.paths || []).find((p) => p.resource === space.key);
    const root = entry?.path_or_ref && local(entry.path_or_ref) ? entry.path_or_ref : "";
    return { id: device.id, label: device.label || device.id, current: !!device.current, path: root ? joinTaskPath(root, parsed.rel) : null };
  });
  return { space: { id: space.id, name: space.name || space.id, key: space.key }, rows };
}

/** "video" | "audio" | "image" | "" for an embeddable URL. `kinds` maps media ids to their kind. */
export function embedKind(raw, { label = "", kinds = null } = {}) {
  const resolved = typeof raw === "object" && raw ? raw : resolveRichUrl(raw);
  if (!resolved) return "";
  const pick = (ext) => (VIDEO_EXTENSIONS.includes(ext) ? "video"
    : AUDIO_EXTENSIONS.includes(ext) ? "audio"
      : IMAGE_EXTENSIONS.includes(ext) ? "image" : "");
  const byUrl = pick(extOf(resolved.path || resolved.href));
  if (byUrl) return byUrl;
  if (resolved.kind === "media") {
    const known = kinds?.get?.(resolved.mediaId) ?? kinds?.[resolved.mediaId];
    const kind = String(known || "").toLowerCase();
    if (["video", "audio", "image"].includes(kind)) return kind;
    return pick(extOf(label));
  }
  return "";
}

/** Directory part of a project path ("docs/a/b.md" -> "docs/a"). */
export function dirOf(path) {
  const clean = normalizeRel(path) || "";
  const index = clean.lastIndexOf("/");
  return index < 0 ? "" : clean.slice(0, index);
}

// ---------------------------------------------------------------- blocks
/**
 * Fenced blocks in Markdown source, in order: { lang, source, start, end }
 * (start/end are line indexes of the fences).  Unclosed fences run to EOF,
 * matching richText().
 */
export function fencedBlocks(text) {
  const lines = String(text || "").replace(/\r\n/g, "\n").split("\n");
  const blocks = [];
  for (let i = 0; i < lines.length; i += 1) {
    if (!/^\s*```/.test(lines[i])) continue;
    const lang = lines[i].replace(/^\s*```\s*/, "").trim().split(/\s+/)[0].toLowerCase();
    const start = i;
    const body = [];
    i += 1;
    while (i < lines.length && !/^\s*```/.test(lines[i])) { body.push(lines[i]); i += 1; }
    blocks.push({ lang, source: body.join("\n"), start, end: Math.min(i, lines.length - 1) });
  }
  return blocks;
}

export function isVizLang(lang) {
  return ["html", "htm", "viz"].includes(String(lang || "").toLowerCase());
}

/** Summary used by the node editor status line. */
export function richSummary(text) {
  const blocks = fencedBlocks(text);
  const source = String(text || "");
  return {
    mermaid: blocks.filter((block) => block.lang === "mermaid").length,
    viz: blocks.filter((block) => isVizLang(block.lang)).length,
    math: (source.match(/\$\$[\s\S]+?\$\$/g) || []).length
      + (source.replace(/\$\$[\s\S]+?\$\$/g, "").match(/\$[^$\n]+\$/g) || []).length,
  };
}

// Stable short hash (FNV-1a) for caches and opt-in approvals.
export function hashText(text) {
  let hash = 0x811c9dc5;
  const source = String(text ?? "");
  for (let i = 0; i < source.length; i += 1) {
    hash ^= source.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(36);
}

// ------------------------------------------------------- viz frame protocol
// Parent -> frame: { type: "ark-viz-render", id, html, theme }
// Frame -> parent: { type: "ark-viz-size", id, height }
export function vizRenderMessage(id, html, theme = "light") {
  return {
    type: "ark-viz-render", id: String(id),
    html: String(html ?? "").slice(0, VIZ_MAX_SOURCE),
    theme: theme === "dark" ? "dark" : "light",
  };
}

/**
 * Validate a message posted by a visualisation frame.  `frames` maps each
 * frame's contentWindow to its id; anything not from a registered window, with
 * the wrong shape or id, or a non-finite height is rejected (null).  Valid
 * heights are clamped to [VIZ_MIN_HEIGHT, VIZ_MAX_HEIGHT].
 */
export function acceptVizMessage(event, frames) {
  if (!event || !frames || typeof frames.get !== "function") return null;
  const id = frames.get(event.source);
  if (id === undefined || id === null) return null;
  const data = event.data;
  if (!data || typeof data !== "object" || Array.isArray(data)) return null;
  if (data.type !== "ark-viz-size" || String(data.id) !== String(id)) return null;
  const height = Number(data.height);
  if (typeof data.height !== "number" || !Number.isFinite(height)) return null;
  return {
    id: String(id),
    height: Math.round(Math.max(VIZ_MIN_HEIGHT, Math.min(VIZ_MAX_HEIGHT, height))),
  };
}

// ---------------------------------------------------------------- anchors
// Review anchors (docs/agents/working-mode.md "Review anchors"; Go
// store/anchors.go AnchorSlug mirrors these rules): a heading's slug is its
// plain text, lower-cased, with every run of characters that are not letters
// or digits turned into "-" and leading/trailing "-" removed; repeated slugs
// get "-1", "-2".  A heading or paragraph ending in "{#id}" uses that id.
export function anchorSlug(text) {
  return String(text || "").trim().toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, "-").replace(/^-+|-+$/g, "");
}

export const EXPLICIT_ANCHOR_RE = /\s*\{#([A-Za-z0-9\p{L}][\w\p{L}\p{N}.-]*)\}\s*$/u;

/** Assigns ids to headings in order: `[{ text, explicit }] -> [id]`. */
export function headingAnchors(headings = []) {
  const counts = new Map();
  return headings.map(({ text, explicit }) => {
    if (explicit) return explicit;
    const slug = anchorSlug(text);
    if (!slug) return "";
    const n = counts.get(slug) || 0;
    counts.set(slug, n + 1);
    return n ? `${slug}-${n}` : slug;
  });
}

/**
 * Natural size of a rendered Mermaid SVG for the zoom viewer: Mermaid writes
 * width="100%" plus a viewBox and a max-width style; use the viewBox, else
 * the max-width with a 5:3 shape.  Whole pixels, at least 1.
 */
export function mermaidNaturalSize(viewBox, maxWidth = "") {
  const box = String(viewBox || "").trim().split(/[\s,]+/).map(Number);
  const ok = box.length === 4 && box.every(Number.isFinite) && box[2] > 0 && box[3] > 0;
  const width = ok ? box[2] : parseFloat(maxWidth) > 0 ? parseFloat(maxWidth) : 800;
  const height = ok ? box[3] : width * 0.6;
  return { width: Math.max(1, Math.ceil(width)), height: Math.max(1, Math.ceil(height)) };
}
