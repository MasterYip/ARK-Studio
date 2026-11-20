// User plugin bundles: turn what the user imports (a .zip, a single .html or
// .js file, or the text fetched from a URL) into { manifest, html }, the one
// self-contained HTML document the sandbox frame runs.
//
//   * .html: the manifest is a <script type="application/ark-plugin+json">
//     block in the document.
//   * .js: the file starts with a /*! ark-plugin { ...manifest... } */
//     comment; the script is wrapped in a minimal document.
//   * .zip: manifest.json plus the entry file (manifest.entry, an .html or
//     .js); scripts, styles and images the entry references by relative path
//     are inlined (the frame cannot load anything: connect/img/script
//     sources outside the document are blocked by its CSP).
//
// Pure: no DOM.  Zip inflation is injected (`inflate(bytes) -> Promise<bytes>`;
// the browser passes DecompressionStream("deflate-raw"), tests pass zlib).

import { validateManifest, MAX_BUNDLE_BYTES } from "./view-plugins.js";

export const MANIFEST_SCRIPT_TYPE = "application/ark-plugin+json";
const MANIFEST_SCRIPT_RE = /<script\b[^>]*\btype\s*=\s*["']application\/ark-plugin\+json["'][^>]*>([\s\S]*?)<\/script\s*>/i;
const JS_HEADER_RE = /^\s*\/\*!?\s*ark-plugin\b([\s\S]*?)\*\//;

export class BundleError extends Error {
  constructor(key, params = {}) {
    super(key);
    this.key = key;
    this.params = params;
  }
}

function parseJson(text, key) {
  try { return JSON.parse(text); } catch { throw new BundleError(key); }
}

function checked(raw, reserved) {
  const result = validateManifest(raw, { reserved });
  if (!result.ok) throw new BundleError(result.errors[0].key, { field: result.errors[0].field, errors: result.errors });
  return result.manifest;
}

function checkSize(text) {
  if (new TextEncoder().encode(text).length > MAX_BUNDLE_BYTES) throw new BundleError("plugins.err.too_large");
}

// Embedding JS in an HTML <script>: "</script" inside it would end the block.
const scriptSafe = (code) => String(code).replace(/<\/(script)/gi, "<\\/$1");

export function wrapScript(code, title = "") {
  const safeTitle = String(title).replace(/[<>&"]/g, "");
  return `<!doctype html><html><head><meta charset="utf-8"><title>${safeTitle}</title></head>`
    + `<body><div id="app"></div><script>${scriptSafe(code)}</script></body></html>`;
}

export function parseHtmlBundle(text, { reserved = [] } = {}) {
  checkSize(text);
  const match = MANIFEST_SCRIPT_RE.exec(text);
  if (!match) throw new BundleError("plugins.err.no_manifest");
  const manifest = checked(parseJson(match[1], "plugins.err.manifest_json"), reserved);
  return { manifest, html: text };
}

export function parseJsBundle(text, { reserved = [] } = {}) {
  checkSize(text);
  const match = JS_HEADER_RE.exec(text);
  if (!match) throw new BundleError("plugins.err.no_manifest");
  const manifest = checked(parseJson(match[1], "plugins.err.manifest_json"), reserved);
  return { manifest, html: wrapScript(text, manifest.name) };
}

// Text bundles by file name (or content type for URLs).
export function parseTextBundle(text, name = "", { reserved = [] } = {}) {
  const lower = String(name).toLowerCase();
  if (/\.js(\?|#|$)/.test(lower) || (!/\.html?(\?|#|$)/.test(lower) && JS_HEADER_RE.test(text))) return parseJsBundle(text, { reserved });
  return parseHtmlBundle(text, { reserved });
}

// --------------------------------------------------------------------- zip
// A minimal reader (central directory; stored and deflated entries; no
// encryption, no zip64).  Returns Map(path -> Uint8Array).
const MAX_ZIP_ENTRIES = 200;

export async function readZip(bytes, inflate) {
  const data = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  if (data.length > MAX_BUNDLE_BYTES * 2) throw new BundleError("plugins.err.too_large");
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let eocd = -1;
  for (let i = data.length - 22; i >= Math.max(0, data.length - 22 - 65535); i -= 1) {
    if (view.getUint32(i, true) === 0x06054b50) { eocd = i; break; }
  }
  if (eocd < 0) throw new BundleError("plugins.err.zip");
  const count = view.getUint16(eocd + 10, true);
  let offset = view.getUint32(eocd + 16, true);
  if (count > MAX_ZIP_ENTRIES) throw new BundleError("plugins.err.zip");
  const files = new Map();
  let total = 0;
  for (let n = 0; n < count; n += 1) {
    if (offset + 46 > data.length || view.getUint32(offset, true) !== 0x02014b50) throw new BundleError("plugins.err.zip");
    const flags = view.getUint16(offset + 8, true);
    const method = view.getUint16(offset + 10, true);
    const compressed = view.getUint32(offset + 20, true);
    const size = view.getUint32(offset + 24, true);
    const nameLength = view.getUint16(offset + 28, true);
    const extra = view.getUint16(offset + 30, true);
    const comment = view.getUint16(offset + 32, true);
    const local = view.getUint32(offset + 42, true);
    const name = new TextDecoder().decode(data.subarray(offset + 46, offset + 46 + nameLength));
    offset += 46 + nameLength + extra + comment;
    if (name.endsWith("/")) continue;
    if (flags & 1) throw new BundleError("plugins.err.zip");
    total += size;
    if (total > MAX_BUNDLE_BYTES) throw new BundleError("plugins.err.too_large");
    if (local + 30 > data.length || view.getUint32(local, true) !== 0x04034b50) throw new BundleError("plugins.err.zip");
    const start = local + 30 + view.getUint16(local + 26, true) + view.getUint16(local + 28, true);
    const raw = data.subarray(start, start + compressed);
    let content;
    if (method === 0) content = raw;
    else if (method === 8) content = await inflate(raw);
    else throw new BundleError("plugins.err.zip");
    if (content.length !== size) throw new BundleError("plugins.err.zip");
    files.set(normalizePath(name), content);
  }
  return files;
}

function normalizePath(path) {
  const parts = [];
  for (const part of String(path).replace(/\\/g, "/").split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") parts.pop(); else parts.push(part);
  }
  return parts.join("/");
}

// A zip may wrap everything in one top folder (what "compress folder" makes).
function stripCommonRoot(files) {
  const names = [...files.keys()];
  if (names.includes("manifest.json")) return files;
  const roots = new Set(names.map((name) => name.split("/")[0]));
  if (roots.size !== 1) return files;
  const [root] = roots;
  if (!files.has(`${root}/manifest.json`)) return files;
  return new Map(names.map((name) => [name.slice(root.length + 1), files.get(name)]));
}

const MIME = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", svg: "image/svg+xml", webp: "image/webp" };

function base64(bytes) {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

function resolveRel(from, ref) {
  if (/^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(ref) || ref.startsWith("/")) return "";
  const base = from.includes("/") ? from.slice(0, from.lastIndexOf("/") + 1) : "";
  return normalizePath(base + ref.split(/[?#]/)[0]);
}

// Inline what the entry document references by relative path.
export function inlineAssets(html, entry, files) {
  const text = (path) => new TextDecoder().decode(files.get(path));
  let out = html.replace(/<script\b([^>]*)\bsrc\s*=\s*["']([^"']+)["']([^>]*)>\s*<\/script\s*>/gi, (all, before, src, after) => {
    const path = resolveRel(entry, src);
    if (!path || !files.has(path)) return all;
    const attrs = `${before} ${after}`.replace(/\s+/g, " ").trim();
    return `<script${attrs ? ` ${attrs}` : ""}>${scriptSafe(text(path))}</script>`;
  });
  out = out.replace(/<link\b[^>]*\brel\s*=\s*["']stylesheet["'][^>]*>/gi, (tag) => {
    const href = /\bhref\s*=\s*["']([^"']+)["']/i.exec(tag)?.[1] || "";
    const path = resolveRel(entry, href);
    if (!path || !files.has(path)) return tag;
    return `<style>${text(path).replace(/<\/(style)/gi, "<\\/$1")}</style>`;
  });
  out = out.replace(/(<img\b[^>]*\bsrc\s*=\s*["'])([^"']+)(["'])/gi, (all, head, src, tail) => {
    const path = resolveRel(entry, src);
    const ext = path.split(".").pop().toLowerCase();
    if (!path || !files.has(path) || !MIME[ext]) return all;
    return `${head}data:${MIME[ext]};base64,${base64(files.get(path))}${tail}`;
  });
  return out;
}

export async function parseZipBundle(bytes, inflate, { reserved = [] } = {}) {
  const files = stripCommonRoot(await readZip(bytes, inflate));
  if (!files.has("manifest.json")) throw new BundleError("plugins.err.no_manifest");
  const manifest = checked(parseJson(new TextDecoder().decode(files.get("manifest.json")), "plugins.err.manifest_json"), reserved);
  const entry = normalizePath(manifest.entry);
  if (!files.has(entry)) throw new BundleError("plugins.err.entry_missing", { entry });
  const source = new TextDecoder().decode(files.get(entry));
  const html = /\.js$/i.test(entry) ? wrapScript(source, manifest.name) : inlineAssets(source, entry, files);
  checkSize(html);
  return { manifest, html };
}

// Browser inflate for zip entries.
export async function browserInflate(bytes) {
  if (typeof DecompressionStream !== "function") throw new BundleError("plugins.err.zip_unsupported");
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}
