// The pure parts of the view plugin API (D-0003): parameter checks of the
// newer bridge methods, the node:write plan (validation, the confirmation
// summary, the once / session grant, applying the batch), the per-plugin
// key/value storage with its size cap, event subscriptions and the
// per-bucket rate limits.  plugin-host.js wires them to ARK; view-plugins.js
// lists the methods (BRIDGE_METHODS) and gates every call.
//
// DOM-free so Node's test runner can import it (tests/js/view_plugins.test.mjs).

// ----------------------------------------------------------- small checks
const UID_RE = /^[A-Za-z][A-Za-z0-9]{0,7}-[0-9A-Za-z]{1,12}$/;
const REF_RE = /^\$[A-Za-z0-9_-]{1,20}$/;
const TYPE_RE = /^[a-z][a-z0-9_]{0,31}$/;
const STATUS_RE = /^[a-z][a-z0-9_-]{0,31}$/;
const RELATION_RE = /^[a-z][a-z0-9_]{0,31}$/;
const IMPORTANCE = ["p0", "p1", "p2", "p3"];
// Control characters other than tab / newline / carriage return.
const CONTROL_RE = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/;

export function isUid(value) {
  return typeof value === "string" && UID_RE.test(value);
}

export const isRef = (value) => typeof value === "string" && REF_RE.test(value);
const isId = (value) => Number.isInteger(value) && value > 0 && value < 2 ** 31;
const text = (value) => (typeof value === "string" ? value.trim() : "");

export const utf8Length = (value) => new TextEncoder().encode(String(value)).length;

// ------------------------------------------------------------ write plans
// nodes.write takes { ops: [...] }, applied in order:
//   { op: "create", ref?: "$a", title, type?, body?, status?, importance?, tags? }
//   { op: "update", uid, title?, type?, body?, status?, importance?, tags? }
//   { op: "link", source, target, relation, note? }
//   { op: "unlink", id }
// A "$ref" names a node created earlier in the same batch.
export const MAX_WRITE_OPS = 50;
export const MAX_TITLE_CHARS = 300;
export const MAX_BODY_CHARS = 60000;
export const WRITE_FIELDS = ["title", "type", "body", "status", "importance", "tags"];

function nodeFields(p) {
  const out = {};
  if (p.title !== undefined) {
    const title = text(p.title);
    if (!title || title.length > MAX_TITLE_CHARS || CONTROL_RE.test(title) || /[\r\n]/.test(title)) return null;
    out.title = title;
  }
  if (p.type !== undefined) {
    if (!TYPE_RE.test(String(p.type))) return null;
    out.type = p.type;
  }
  if (p.body !== undefined) {
    if (typeof p.body !== "string" || p.body.length > MAX_BODY_CHARS || CONTROL_RE.test(p.body)) return null;
    out.body = p.body;
  }
  if (p.status !== undefined) {
    if (!STATUS_RE.test(String(p.status))) return null;
    out.status = p.status;
  }
  if (p.importance !== undefined) {
    if (!IMPORTANCE.includes(p.importance)) return null;
    out.importance = p.importance;
  }
  if (p.tags !== undefined) {
    if (!Array.isArray(p.tags) || p.tags.length > 50) return null;
    const tags = [];
    for (const tag of p.tags) {
      const value = text(tag);
      if (!value || value.length > 60 || CONTROL_RE.test(value) || /[\r\n,]/.test(value)) return null;
      if (!tags.includes(value)) tags.push(value);
    }
    out.tags = tags;
  }
  return out;
}

// A clean copy of the ops, or null when anything is off.
export function validateWriteOps(raw) {
  if (!Array.isArray(raw) || !raw.length || raw.length > MAX_WRITE_OPS) return null;
  const refs = new Set();
  const nodeRef = (value) => (isUid(value) || (isRef(value) && refs.has(value)) ? value : null);
  const ops = [];
  for (const item of raw) {
    if (!item || typeof item !== "object" || Array.isArray(item)) return null;
    if (item.op === "create") {
      const fields = nodeFields(item);
      if (!fields || !fields.title) return null;
      const op = { op: "create", ...fields };
      if (item.ref !== undefined) {
        if (!isRef(item.ref) || refs.has(item.ref)) return null;
        refs.add(item.ref);
        op.ref = item.ref;
      }
      ops.push(op);
    } else if (item.op === "update") {
      const uid = nodeRef(item.uid);
      const fields = nodeFields(item);
      if (!uid || !fields || !Object.keys(fields).length) return null;
      ops.push({ op: "update", uid, ...fields });
    } else if (item.op === "link") {
      const source = nodeRef(item.source);
      const target = nodeRef(item.target);
      const relation = String(item.relation || "");
      if (!source || !target || source === target || !RELATION_RE.test(relation)) return null;
      const op = { op: "link", source, target, relation };
      if (item.note !== undefined) {
        if (typeof item.note !== "string" || item.note.length > 500 || CONTROL_RE.test(item.note)) return null;
        op.note = item.note;
      }
      ops.push(op);
    } else if (item.op === "unlink") {
      if (!isId(item.id)) return null;
      ops.push({ op: "unlink", id: item.id });
    } else {
      return null;
    }
  }
  return ops;
}

const shorten = (value, n = 60) => {
  const s = String(value ?? "");
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
};

// The confirmation summary: { headline: [{ key, vars }], lines: [{ key, vars }] }.
// headline: "Create 3 nodes · change 1 node · add 2 links · remove 1 link";
// lines: one per op ("T-0012: status → done", "Create claim “…”", …).
export function summarizeWriteOps(ops) {
  const count = { create: 0, update: 0, link: 0, unlink: 0 };
  const lines = [];
  const name = (ref, titles) => (titles.has(ref) ? `“${shorten(titles.get(ref), 40)}”` : ref);
  const titles = new Map();
  for (const op of ops) {
    count[op.op] += 1;
    if (op.op === "create") {
      if (op.ref) titles.set(op.ref, op.title);
      lines.push({ key: "plugins.write.line_create", vars: { type: op.type || "note", title: shorten(op.title) } });
    } else if (op.op === "update") {
      const changes = WRITE_FIELDS.filter((field) => op[field] !== undefined).map((field) => {
        if (field === "body") return `body (${op.body.length})`;
        if (field === "tags") return `tags → ${shorten(op.tags.join(", "), 40)}`;
        return `${field} → ${shorten(op[field], 40)}`;
      });
      lines.push({ key: "plugins.write.line_update", vars: { uid: name(op.uid, titles), changes: changes.join(", ") } });
    } else if (op.op === "link") {
      lines.push({ key: "plugins.write.line_link", vars: { source: name(op.source, titles), relation: op.relation, target: name(op.target, titles) } });
    } else {
      lines.push({ key: "plugins.write.line_unlink", vars: { id: op.id } });
    }
  }
  const headline = [];
  if (count.create) headline.push({ key: "plugins.write.create_n", vars: { n: count.create } });
  if (count.update) headline.push({ key: "plugins.write.update_n", vars: { n: count.update } });
  if (count.link) headline.push({ key: "plugins.write.link_n", vars: { n: count.link } });
  if (count.unlink) headline.push({ key: "plugins.write.unlink_n", vars: { n: count.unlink } });
  return { headline, lines, count };
}

// Session grants of node:write: per plugin, for the project they were given
// in.  Lost on reload (memory only), on a project switch and on revoke.
export function createWriteGrants() {
  const session = new Map();
  const listeners = new Set();
  const changed = (pluginId) => { for (const fn of listeners) { try { fn(pluginId); } catch { /* listener errors stay local */ } } };
  return {
    has: (pluginId, project) => session.has(pluginId) && session.get(pluginId) === String(project || ""),
    grant(pluginId, project) { session.set(pluginId, String(project || "")); changed(pluginId); },
    revoke(pluginId) { const had = session.delete(pluginId); if (had) changed(pluginId); return had; },
    // A project switch ends every grant given for another project.
    projectChanged(project) {
      for (const [pluginId, granted] of [...session]) {
        if (granted !== String(project || "")) { session.delete(pluginId); changed(pluginId); }
      }
    },
    list: () => [...session.keys()],
    onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); },
  };
}

// Decide one node:write call.  `ask()` shows the summary dialog and resolves
// "once", "session" or null (declined).  Throws { code: "declined" }.
export async function authorizeWrite({ grants, pluginId, project, ask }) {
  if (grants.has(pluginId, project)) return "session";
  const answer = await ask();
  if (answer === "session") { grants.grant(pluginId, project); return "session"; }
  if (answer === "once") return "once";
  throw Object.assign(new Error("declined"), { code: "declined" });
}

// Apply a validated batch through `call(kind, payload)` (the host's ARK API
// requests).  Stops at the first failure.  Returns
// { ok, results: [{ op, ok, uid?, id?, error? }], created: { $ref: uid } }.
export async function applyWriteOps(ops, call) {
  const created = {};
  const results = [];
  const resolve = (value) => (isRef(value) ? created[value] : value);
  for (const op of ops) {
    try {
      if (op.op === "create") {
        const { op: _op, ref, ...fields } = op;
        const data = await call("create", { type: "note", ...fields });
        const uid = String(data?.unit?.uid || data?.uid || "");
        if (ref) created[ref] = uid;
        results.push({ op: "create", ok: true, uid, ...(ref ? { ref } : {}) });
      } else if (op.op === "update") {
        const { op: _op, uid, ...fields } = op;
        const target = resolve(uid);
        await call("update", { uid: target, fields });
        results.push({ op: "update", ok: true, uid: target });
      } else if (op.op === "link") {
        const data = await call("link", { source: resolve(op.source), target: resolve(op.target), relation: op.relation, ...(op.note ? { note: op.note } : {}) });
        results.push({ op: "link", ok: true, id: data?.id ?? null });
      } else {
        await call("unlink", { id: op.id });
        results.push({ op: "unlink", ok: true, id: op.id });
      }
    } catch (err) {
      results.push({ op: op.op, ok: false, error: String(err?.serverMessage || err?.message || "failed").slice(0, 300) });
      return { ok: false, results, created };
    }
  }
  return { ok: true, results, created };
}

// ------------------------------------------------------- other parameters
export const MAX_SEARCH_RESULTS = 50;
export const MAX_CLIPBOARD_CHARS = 100000;
export const MAX_URL_CHARS = 2000;
export const MAX_CHAT_MESSAGES = 20;
export const MAX_CHAT_MESSAGE_CHARS = 8000;
export const MAX_CHAT_TOTAL_CHARS = 48000;
export const MAX_CHAT_SYSTEM_CHARS = 4000;
export const MAX_MEDIA_BYTES = 10 * 1024 * 1024;

export function nodeRefParams(p) {
  if (isUid(p?.uid)) return { uid: p.uid };
  if (isId(p?.id)) return { id: p.id };
  return null;
}

export function searchParams(p) {
  const q = text(p?.q);
  if (!q || q.length > 200) return null;
  const limit = p?.limit === undefined ? 20 : Number(p.limit);
  if (!Number.isInteger(limit) || limit < 1) return null;
  const out = { q, limit: Math.min(limit, MAX_SEARCH_RESULTS) };
  if (p?.type !== undefined) {
    if (!TYPE_RE.test(String(p.type))) return null;
    out.type = p.type;
  }
  return out;
}

export function openParams(p) {
  if (!isUid(p?.uid)) return null;
  const target = p?.target === undefined ? "details" : p.target;
  if (!["details", "map", "editor"].includes(target)) return null;
  return { uid: p.uid, target };
}

export function clipboardParams(p) {
  if (typeof p?.text !== "string" || !p.text || p.text.length > MAX_CLIPBOARD_CHARS) return null;
  return { text: p.text };
}

// External links: http(s) only, no user:password@, at most 2,000 characters.
export function linkParams(p) {
  const raw = text(p?.url);
  if (!raw || raw.length > MAX_URL_CHARS) return null;
  let url;
  try { url = new URL(raw); } catch { return null; }
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || !url.hostname) return null;
  return { url: url.href, host: url.host };
}

export function chatParams(p) {
  if (!Array.isArray(p?.messages) || !p.messages.length || p.messages.length > MAX_CHAT_MESSAGES) return null;
  const messages = [];
  let total = 0;
  for (const item of p.messages) {
    const role = item?.role;
    const content = text(item?.content);
    if (!["user", "assistant"].includes(role) || !content || content.length > MAX_CHAT_MESSAGE_CHARS) return null;
    total += content.length;
    messages.push({ role, content });
  }
  if (total > MAX_CHAT_TOTAL_CHARS || messages.at(-1).role !== "user") return null;
  const out = { messages, tools: p?.tools !== false };
  if (p?.system !== undefined) {
    const system = text(p.system);
    if (system.length > MAX_CHAT_SYSTEM_CHARS) return null;
    if (system) out.system = system;
  }
  return out;
}

export function mediaParams(p) {
  return isId(p?.id) ? { id: p.id } : null;
}

// ------------------------------------------------------------------ events
export const PLUGIN_EVENTS = ["selection", "graph", "project"];

export function eventsParams(p, { optional = false } = {}) {
  if (p?.events === undefined && optional) return { events: [...PLUGIN_EVENTS] };
  if (!Array.isArray(p?.events) || !p.events.length) return null;
  const events = [];
  for (const name of p.events) {
    if (!PLUGIN_EVENTS.includes(name)) return null;
    if (!events.includes(name)) events.push(name);
  }
  return { events };
}

// Subscriptions per plugin frame (`key`).  Selection and project events go
// out at once; graph changes (the live-sync change events) are merged and
// delivered once per `debounceMs`: { uids: [...] } or { uids: null } when
// the whole graph may have changed.
export function createEventHub({ deliver, debounceMs = 500, setTimer = setTimeout, clearTimer = clearTimeout } = {}) {
  const subs = new Map();
  let pending = null;
  let timer = 0;
  const flush = () => {
    timer = 0;
    if (!pending) return;
    const data = { uids: pending === "all" ? null : [...pending].slice(0, 500) };
    pending = null;
    for (const [key, set] of subs) if (set.has("graph")) deliver(key, "graph", data);
  };
  return {
    subscribe(key, events) {
      const set = subs.get(key) || new Set();
      for (const name of events) if (PLUGIN_EVENTS.includes(name)) set.add(name);
      subs.set(key, set);
      return [...set];
    },
    unsubscribe(key, events = PLUGIN_EVENTS) {
      const set = subs.get(key);
      if (!set) return [];
      for (const name of events) set.delete(name);
      if (!set.size) subs.delete(key);
      return [...set];
    },
    drop(key) { subs.delete(key); },
    subscribed: (key, event) => Boolean(subs.get(key)?.has(event)),
    emit(event, data = {}) {
      if (event === "graph") {
        if (![...subs.values()].some((set) => set.has("graph"))) return;
        const uids = Array.isArray(data?.uids) ? data.uids : null;
        if (!uids || pending === "all") pending = "all";
        else { pending ||= new Set(); for (const uid of uids) pending.add(uid); }
        if (!timer) timer = setTimer(flush, debounceMs);
        return;
      }
      for (const [key, set] of subs) if (set.has(event)) deliver(key, event, data);
    },
    flush() { if (timer) clearTimer(timer); flush(); },
  };
}

// ----------------------------------------------------------------- storage
// The plugin's own key/value store: per user, plugin and project, values are
// JSON, at most STORAGE_CAP_BYTES per plugin and project (keys + values).
export const STORAGE_CAP_BYTES = 1024 * 1024;
export const MAX_STORAGE_KEY = 200;

export function storageKeyParams(p) {
  const key = typeof p?.key === "string" ? p.key : "";
  if (!key || key.length > MAX_STORAGE_KEY || CONTROL_RE.test(key)) return null;
  return { key };
}

export function storageSetParams(p) {
  const base = storageKeyParams(p);
  if (!base || p.value === undefined) return null;
  let json;
  try { json = JSON.stringify(p.value); } catch { return null; }
  if (json === undefined) return null;
  return { key: base.key, json };
}

// `backend`: async { list(prefix) -> [{ key, value }], get(key), put(key, value), remove(key) }
// (IndexedDB in plugin-store.js, a Map in tests).  Values are JSON strings.
export function createPluginStorage(backend, { cap = STORAGE_CAP_BYTES } = {}) {
  const sep = "\u0000";
  return {
    scope(user, pluginId, project) {
      const prefix = [user, pluginId, project, ""].join(sep);
      const usage = async () => {
        let used = 0;
        for (const entry of await backend.list(prefix)) used += utf8Length(entry.key.slice(prefix.length)) + utf8Length(entry.value);
        return used;
      };
      return {
        prefix,
        usage,
        async get(key) {
          const raw = await backend.get(prefix + key);
          return raw === undefined || raw === null ? null : JSON.parse(raw);
        },
        async set(key, json) {
          const entries = await backend.list(prefix);
          let used = 0;
          for (const entry of entries) {
            if (entry.key === prefix + key) continue;
            used += utf8Length(entry.key.slice(prefix.length)) + utf8Length(entry.value);
          }
          const size = utf8Length(key) + utf8Length(json);
          if (used + size > cap) throw Object.assign(new Error("quota_exceeded"), { code: "quota_exceeded" });
          await backend.put(prefix + key, json);
          return { used: used + size, cap };
        },
        async remove(key) { await backend.remove(prefix + key); return true; },
        async keys() { return (await backend.list(prefix)).map((entry) => entry.key.slice(prefix.length)).sort(); },
        async clear() {
          for (const entry of await backend.list(prefix)) await backend.remove(entry.key);
          return true;
        },
      };
    },
    // Everything a plugin stored for `user` (all projects): on remove.
    async forget(user, pluginId) {
      for (const entry of await backend.list([user, pluginId, ""].join(sep))) await backend.remove(entry.key);
    },
  };
}

// ------------------------------------------------------------- rate limits
// Every plugin: 20 calls per second overall, plus stricter windows for the
// expensive or visible methods (BRIDGE_METHODS[...].bucket).
export const RATE_LIMITS = Object.freeze({
  calls: [{ limit: 20, windowMs: 1000 }],
  write: [{ limit: 5, windowMs: 10_000 }, { limit: 30, windowMs: 60_000 }],
  ai: [{ limit: 3, windowMs: 10_000 }, { limit: 20, windowMs: 60_000 }],
  clipboard: [{ limit: 3, windowMs: 5_000 }],
  link: [{ limit: 3, windowMs: 10_000 }],
  comment: [{ limit: 5, windowMs: 60_000 }],
  media: [{ limit: 10, windowMs: 10_000 }],
  storage: [{ limit: 60, windowMs: 10_000 }],
});

// Sliding windows per bucket; a refused call does not count.
export function createRateLimits(rules = RATE_LIMITS, now = () => Date.now()) {
  const log = new Map();
  return (bucket) => {
    const windows = rules[bucket];
    if (!windows) return true;
    const time = now();
    const longest = Math.max(...windows.map((w) => w.windowMs));
    const stamps = (log.get(bucket) || []).filter((stamp) => time - stamp < longest);
    for (const { limit, windowMs } of windows) {
      if (stamps.filter((stamp) => time - stamp < windowMs).length >= limit) { log.set(bucket, stamps); return false; }
    }
    stamps.push(time);
    log.set(bucket, stamps);
    return true;
  };
}

// --------------------------------------------------------- what plugins see
// Palette variables a plugin can copy to match ARK (project:read).
export const THEME_VARS = [
  "--bg", "--bg2", "--bg3", "--lane", "--card", "--card2", "--line", "--line-soft", "--ink", "--sub", "--mute",
  "--accent", "--accent-2", "--accent-ink", "--accent-soft", "--green", "--amber", "--red", "--violet", "--pink",
  "--input-bg", "--input-border", "--btn", "--btn-ink", "--btn-hover", "--chip-bg", "--chip-ink",
  "--radius", "--radius-sm", "--font", "--mono",
];

// Colours, lengths and font lists only (no url(), no ; or braces).
export function safeCssValue(value) {
  const v = String(value ?? "").trim();
  if (!v || v.length > 160 || /url\s*\(|[;{}<>\\@]|expression/i.test(v)) return "";
  return v;
}

export function publicTheme(mode, readVar) {
  const vars = {};
  for (const name of THEME_VARS) {
    const value = safeCssValue(readVar(name));
    if (value) vars[name] = value;
  }
  return { mode: mode === "dark" ? "dark" : "light", vars };
}

const linkPeer = (link, side) => {
  const peer = link?.[side] || {};
  return {
    id: Number(link?.id) || null, relation: String(link?.relation || ""), note: String(link?.note || "").slice(0, 500),
    uid: String(peer.uid || ""), title: String(peer.title || ""), type: String(peer.type || ""), status: String(peer.status || ""),
  };
};

// The full node for selection:read / node.get: base fields, body, media
// (ids, kinds, captions; the bytes through media.get) and links in and out.
export function publicNodeDetail(unit, base) {
  if (!unit || typeof unit !== "object") return null;
  const out = base(unit, { body: true });
  out.media = (Array.isArray(unit.media) ? unit.media : []).slice(0, 100).map((item) => ({
    id: Number(item?.id) || null,
    kind: String(item?.kind || ""),
    caption: String(item?.caption || "").slice(0, 500),
    image: Boolean(item?.is_image),
    name: String(item?.url || "").split(/[\\/]/).pop().slice(0, 200),
  }));
  out.links = {
    out: (Array.isArray(unit.links_out) ? unit.links_out : []).slice(0, 500).map((link) => linkPeer(link, "target")),
    in: (Array.isArray(unit.links_in) ? unit.links_in : []).slice(0, 500).map((link) => linkPeer(link, "source")),
  };
  return out;
}
