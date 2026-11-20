// View plugins: the manifest format, the registry and the permission gate of
// the postMessage bridge.  Built-in workbench views are first-party plugins
// registered through the same registry (plugin-host.js wires them to the
// workbench); user plugins run in a sandboxed iframe and reach ARK only
// through `gateRequest` (docs/development/plugin-api.md).
//
// Pure and DOM-free so Node's test runner can import it
// (tests/js/view_plugins.test.mjs).
//
// Lifecycle of a plugin implementation (built-in or the sandbox adapter):
//   mount(container, api)  once, with the view's element
//   update(state)          when the view is shown and when the host state
//                          changes while it is shown
//   unmount()              when the plugin is disabled or removed

import { VIEW_DEFAULTS, CONTAINERS } from "./workbench-model.js";
import {
  isUid, nodeRefParams, searchParams, openParams, clipboardParams, linkParams, chatParams, mediaParams,
  eventsParams, storageKeyParams, storageSetParams, validateWriteOps,
} from "./plugin-api.js";

export { isUid };

export const PLUGIN_API_VERSION = 1;
export const PLUGIN_LOCATIONS = CONTAINERS;   // left | right | bottom

// What a user plugin may ask for.  The manifest declares them; the user
// confirms them on install; the bridge refuses anything else
// (docs/development/plugin-api.md, decision D-0003).
export const PLUGIN_PERMISSIONS = [
  "selection:read", "graph:read", "events", "project:read", "storage", "node:open", "notify",
  "clipboard:write", "comment:write", "node:write", "link:open", "ai:chat",
];

// Permissions that still ask the user after install: "each" every call,
// "session" before the first write (allow once, or for this session).
export const PERMISSION_CONFIRM = Object.freeze({ "comment:write": "each", "link:open": "each", "node:write": "session" });
export const confirmMode = (permission) => PERMISSION_CONFIRM[permission] || "none";

// Icons a user plugin may pick (names from icons.js); anything else shows
// the plugin icon.
export const PLUGIN_ICONS = [
  "puzzle", "file", "folder", "search", "graph", "info", "list", "layers", "sparkles", "terminal",
  "message", "check", "clock", "link", "pen", "filter", "eye",
];
export const DEFAULT_PLUGIN_ICON = "puzzle";

export const PLUGIN_ID_RE = /^[a-z][a-z0-9-]{1,28}[a-z0-9]$/;
const VERSION_RE = /^\d{1,4}\.\d{1,4}\.\d{1,4}(?:[-+][0-9A-Za-z.-]{1,32})?$/;
// Workbench view ids of user plugins: "u-" + plugin id (32 chars at most,
// the profile's view id limit).
export const USER_VIEW_PREFIX = "u-";
export const MAX_BUNDLE_BYTES = 2 * 1024 * 1024;
export const MAX_NAME = 60;
export const MAX_TITLE = 40;
export const MAX_DESCRIPTION = 300;

const BUILTIN_IDS = VIEW_DEFAULTS.map((view) => view.id);

export function pluginViewId(manifest) {
  return manifest.builtin ? manifest.id : `${USER_VIEW_PREFIX}${manifest.id}`;
}

// The first-party manifests of the built-in views (placement, icon and
// title key from workbench-model's VIEW_DEFAULTS, the one list of built-in
// views).
export const BUILTIN_MANIFESTS = VIEW_DEFAULTS.map((view) => Object.freeze({
  id: view.id, name: view.id, version: String(PLUGIN_API_VERSION), icon: view.icon, location: view.container,
  titleKey: view.titleKey, entry: `builtin:${view.id}`, permissions: [], builtin: true,
}));

const str = (value) => (typeof value === "string" ? value.trim() : "");

// Validate a user plugin manifest.  Returns { ok, manifest, errors } where
// `manifest` is a clean copy (only known fields) and `errors` lists message
// keys with their parameters ({ key, field }).  `reserved`: plugin ids that
// are taken (installed plugins when importing a new one).
export function validateManifest(raw, { reserved = [] } = {}) {
  const errors = [];
  const fail = (key, field) => errors.push({ key, field });
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { ok: false, manifest: null, errors: [{ key: "plugins.err.manifest", field: "" }] };
  }
  const id = str(raw.id);
  if (!PLUGIN_ID_RE.test(id)) fail("plugins.err.id", "id");
  else if (BUILTIN_IDS.includes(id) || id.startsWith(USER_VIEW_PREFIX)) fail("plugins.err.id_reserved", "id");
  else if (reserved.includes(id)) fail("plugins.err.id_taken", "id");
  const name = str(raw.name);
  if (!name || name.length > MAX_NAME) fail("plugins.err.name", "name");
  const version = str(raw.version);
  if (!VERSION_RE.test(version)) fail("plugins.err.version", "version");
  const location = str(raw.location || "right");
  if (!PLUGIN_LOCATIONS.includes(location)) fail("plugins.err.location", "location");
  const icon = PLUGIN_ICONS.includes(str(raw.icon)) ? str(raw.icon) : DEFAULT_PLUGIN_ICON;
  // titles: { en, zh, ... } (at least one); a plain string counts as "en".
  const titles = {};
  const rawTitles = typeof raw.titles === "string" ? { en: raw.titles } : raw.titles;
  if (rawTitles && typeof rawTitles === "object" && !Array.isArray(rawTitles)) {
    for (const [lang, value] of Object.entries(rawTitles)) {
      if (!/^[a-z]{2}(?:-[A-Za-z]{2,4})?$/.test(lang)) continue;
      const title = str(value);
      if (title && title.length <= MAX_TITLE) titles[lang] = title;
    }
  }
  if (!Object.keys(titles).length) fail("plugins.err.titles", "titles");
  const entry = str(raw.entry || "index.html");
  if (!/^[A-Za-z0-9._/-]{1,120}$/.test(entry) || entry.includes("..") || entry.startsWith("/") || !/\.(html?|js)$/i.test(entry)) {
    fail("plugins.err.entry", "entry");
  }
  const permissions = [];
  for (const item of Array.isArray(raw.permissions) ? raw.permissions : []) {
    const permission = str(item);
    if (!PLUGIN_PERMISSIONS.includes(permission)) { fail("plugins.err.permission", permission); continue; }
    if (!permissions.includes(permission)) permissions.push(permission);
  }
  if (raw.permissions !== undefined && !Array.isArray(raw.permissions)) fail("plugins.err.permission", "permissions");
  const description = str(raw.description).slice(0, MAX_DESCRIPTION);
  const apiVersion = raw.apiVersion === undefined ? PLUGIN_API_VERSION : Number(raw.apiVersion);
  if (apiVersion !== PLUGIN_API_VERSION) fail("plugins.err.api_version", "apiVersion");
  const manifest = { id, name, version, icon, location, titles, entry, permissions, description, apiVersion: PLUGIN_API_VERSION };
  return { ok: errors.length === 0, manifest: errors.length ? null : manifest, errors };
}

// The title shown for a plugin in `lang` (user plugins: their titles, with
// English and then any title as fallback; built-ins: their i18n key).
export function pluginTitle(manifest, lang = "en", translate = (key) => key) {
  if (manifest?.titleKey) return translate(manifest.titleKey);
  const titles = manifest?.titles || {};
  const base = String(lang || "en").split("-")[0];
  return titles[lang] || titles[base] || titles.en || Object.values(titles)[0] || manifest?.name || manifest?.id || "";
}

// ------------------------------------------------------------------ registry
// One record per plugin: { manifest, impl, viewId, mounted }.  The registry
// only keeps the records; plugin-host.js mounts and updates them.
export function createRegistry() {
  const records = new Map();
  const listeners = new Set();
  const emit = (type, record) => { for (const fn of listeners) { try { fn(type, record); } catch { /* listener errors stay local */ } } };
  return {
    register(manifest, impl = {}) {
      if (!manifest?.id) throw new Error("plugin manifest needs an id");
      const viewId = pluginViewId(manifest);
      if (records.has(viewId)) throw new Error(`plugin view already registered: ${viewId}`);
      for (const hook of ["mount", "update", "unmount"]) {
        if (impl[hook] !== undefined && typeof impl[hook] !== "function") throw new Error(`plugin ${hook} must be a function`);
      }
      const record = { manifest, impl, viewId, mounted: false };
      records.set(viewId, record);
      emit("register", record);
      return record;
    },
    unregister(viewId) {
      const record = records.get(viewId);
      if (!record) return null;
      records.delete(viewId);
      emit("unregister", record);
      return record;
    },
    get: (viewId) => records.get(viewId) || null,
    has: (viewId) => records.has(viewId),
    list: () => [...records.values()],
    // Placement entries for workbench-model (built-ins first, in their
    // default order, then user plugins in registration order).
    viewDefaults() {
      const out = VIEW_DEFAULTS.filter((view) => records.has(view.id));
      for (const record of records.values()) {
        if (record.manifest.builtin) continue;
        out.push({ id: record.viewId, container: record.manifest.location, icon: record.manifest.icon, plugin: record.manifest.id });
      }
      return out;
    },
    onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); },
  };
}

// --------------------------------------------------------- bridge: requests
// A request from a plugin frame: { type: "ark-plugin-call", channel, id, method, params }.
// `gateRequest` decides whether the host may run it.  It never runs anything.
export const BRIDGE_METHODS = {
  // selection:read
  "selection.get": { permission: "selection:read", params: () => ({}) },
  "media.get": { permission: "selection:read", bucket: "media", params: mediaParams },
  // graph:read
  "graph.snapshot": { permission: "graph:read", params: () => ({}) },
  "node.get": { permission: "graph:read", params: nodeRefParams },
  "nodes.search": { permission: "graph:read", params: searchParams },
  "topics.list": { permission: "graph:read", params: () => ({}) },
  "topic.members": { permission: "graph:read", params: (p) => (isUid(p?.uid) ? { uid: p.uid } : null) },
  // events
  "events.subscribe": { permission: "events", params: (p) => eventsParams(p) },
  "events.unsubscribe": { permission: "events", params: (p) => eventsParams(p, { optional: true }) },
  // project:read
  "project.get": { permission: "project:read", params: () => ({}) },
  // storage
  "storage.get": { permission: "storage", bucket: "storage", params: storageKeyParams },
  "storage.set": { permission: "storage", bucket: "storage", params: storageSetParams },
  "storage.remove": { permission: "storage", bucket: "storage", params: storageKeyParams },
  "storage.keys": { permission: "storage", bucket: "storage", params: () => ({}) },
  "storage.clear": { permission: "storage", bucket: "storage", params: () => ({}) },
  // node:open, notify, clipboard:write
  "node.open": { permission: "node:open", params: openParams },
  notify: {
    permission: "notify",
    params: (p) => {
      const message = typeof p?.message === "string" ? p.message.trim().slice(0, 200) : "";
      if (!message) return null;
      return { message, kind: ["ok", "err", "info"].includes(p?.kind) ? p.kind : "info" };
    },
  },
  "clipboard.write": { permission: "clipboard:write", bucket: "clipboard", params: clipboardParams },
  // Writes and the outside world: confirmed (PERMISSION_CONFIRM) and rate-limited.
  "comment.create": {
    permission: "comment:write",
    bucket: "comment",
    params: (p) => {
      const body = typeof p?.body === "string" ? p.body.trim() : "";
      if (!isUid(p?.uid) || !body || body.length > 4000) return null;
      return { uid: p.uid, body };
    },
  },
  "nodes.write": { permission: "node:write", bucket: "write", params: (p) => { const ops = validateWriteOps(p?.ops); return ops ? { ops } : null; } },
  "link.open": { permission: "link:open", bucket: "link", params: linkParams },
  "ai.chat": { permission: "ai:chat", bucket: "ai", params: chatParams },
};

export const MAX_MESSAGE_BYTES = 64 * 1024;

// Returns { ok: true, id, method, params } or { ok: false, id, code }.
// `granted`: the permissions the user confirmed; `channel`: the per-load
// secret the frame must echo; `size`: the message's serialized size.
export function gateRequest(message, { granted = [], channel = "", size = 0 } = {}) {
  const id = Number.isInteger(message?.id) && message.id >= 0 && message.id < 2 ** 31 ? message.id : null;
  const deny = (code) => ({ ok: false, id, code });
  if (!message || typeof message !== "object" || message.type !== "ark-plugin-call") return deny("bad_message");
  if (!channel || message.channel !== channel) return deny("bad_channel");
  if (id === null) return deny("bad_id");
  if (size > MAX_MESSAGE_BYTES) return deny("too_large");
  const spec = Object.prototype.hasOwnProperty.call(BRIDGE_METHODS, message.method) ? BRIDGE_METHODS[message.method] : null;
  if (!spec) return deny("unknown_method");
  if (!granted.includes(spec.permission)) return deny("permission_denied");
  const params = spec.params(message.params && typeof message.params === "object" && !Array.isArray(message.params) ? message.params : {});
  if (!params) return deny("bad_params");
  return { ok: true, id, method: message.method, params, bucket: spec.bucket || "" };
}

// The state pushed to a plugin with update(): only what its permissions
// allow.  `host`: { selection, theme, lang, visible, location }.
export function pluginState(host, granted = []) {
  const state = {
    visible: Boolean(host?.visible),
    theme: host?.theme === "dark" ? "dark" : "light",
    lang: String(host?.lang || "en"),
    location: PLUGIN_LOCATIONS.includes(host?.location) ? host.location : "right",
  };
  if (granted.includes("selection:read")) state.selection = host?.selection ? publicNode(host.selection) : null;
  return state;
}

// What a plugin sees of a node (no internal ids, paths or secrets).
export function publicNode(unit, { body = false } = {}) {
  if (!unit || typeof unit !== "object") return null;
  const out = {
    uid: String(unit.uid || ""),
    title: String(unit.title || ""),
    type: String(unit.type || ""),
    status: String(unit.status || ""),
    importance: String(unit.importance || ""),
    tags: Array.isArray(unit.tags) ? unit.tags.map(String).slice(0, 50) : [],
  };
  if (body) out.body = String(unit.body || "").slice(0, 200000);
  return out;
}

// The graph snapshot a plugin gets: nodes by uid, links between uids.
export function publicGraph(snapshot, { maxNodes = 20000 } = {}) {
  const nodes = (snapshot?.nodes || []).slice(0, maxNodes);
  const uidOf = new Map(nodes.map((node) => [node.id, String(node.uid || "")]));
  return {
    nodes: nodes.map((node) => publicNode(node)),
    links: (snapshot?.edges || []).filter((edge) => uidOf.has(edge.source) && uidOf.has(edge.target))
      .map((edge) => ({ source: uidOf.get(edge.source), target: uidOf.get(edge.target), relation: String(edge.relation || "") })),
    truncated: (snapshot?.nodes || []).length > maxNodes,
  };
}

// A simple per-plugin rate limit: at most `limit` calls per `windowMs`.
export function createRateLimit(limit = 20, windowMs = 1000, now = () => Date.now()) {
  let start = 0;
  let count = 0;
  return () => {
    const time = now();
    if (time - start >= windowMs) { start = time; count = 0; }
    count += 1;
    return count <= limit;
  };
}
