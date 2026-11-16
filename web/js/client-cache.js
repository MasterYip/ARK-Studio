// Browser storage for the project cache (docs/development/client-cache.md).
//
// IndexedDB holds cached API responses per server origin, account (the
// server's opaque cache_id) and project; a memory map keeps decoded payloads
// of this tab so switching views reuses one copy.  Large bodies are stored
// gzip-compressed with CompressionStream when the browser has it.  The rules
// (which routes, keys, freshness, eviction, delta merge) are in
// client-cache-model.js.
import {
  CACHE_VERSION, DEFAULT_CAP_BYTES, cacheRoute, entryKey, identityScope, planEviction,
  projectPrefix,
} from "./client-cache-model.js";

const DB_NAME = `ark-client-cache-v${CACHE_VERSION}`;
const META = "meta";
const BODY = "body";
const COMPRESS_FROM = 16 * 1024;
const MEMORY_LIMIT = 96 * 1024 * 1024;

let dbPromise = null;
let metaIndex = null;            // key -> meta record (no body)
const memory = new Map();        // key -> { data, bytes }
let memoryBytes = 0;
let identity = "";
let cacheId = "";
let capBytes = DEFAULT_CAP_BYTES;
let disabled = false;
const pendingWrites = new Map(); // key -> { meta, text }
let writeTimer = 0;

function idbRequest(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function txDone(tx) {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  });
}

async function openDb() {
  if (disabled || typeof indexedDB === "undefined") return null;
  dbPromise ||= (async () => {
    // Earlier schema versions are dropped, never read.
    try {
      const names = (await indexedDB.databases?.()) || [];
      for (const item of names) {
        if (/^ark-client-cache-v\d+$/.test(item.name || "") && item.name !== DB_NAME) {
          indexedDB.deleteDatabase(item.name);
        }
      }
    } catch { /* databases() is optional */ }
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(META)) db.createObjectStore(META, { keyPath: "key" });
      if (!db.objectStoreNames.contains(BODY)) db.createObjectStore(BODY);
    };
    const db = await idbRequest(request);
    db.onversionchange = () => db.close();
    return db;
  })().catch(() => { disabled = true; return null; });
  return dbPromise;
}

// The index is published only once it is complete, and concurrent callers
// share one load.  (Publishing an empty map before the read finished let the
// startup requests, which start in parallel, see "nothing cached": the warm
// start went to the network and the account check missed other accounts'
// entries.)
let metaLoading = null;
function loadMeta() {
  if (metaIndex) return Promise.resolve(metaIndex);
  metaLoading ||= (async () => {
    const index = new Map();
    const db = await openDb();
    if (db) {
      try {
        const tx = db.transaction(META, "readonly");
        const rows = await idbRequest(tx.objectStore(META).getAll());
        for (const row of rows || []) index.set(row.key, row);
      } catch { /* unreadable cache: start empty */ }
    }
    metaIndex = index;
    return index;
  })();
  return metaLoading;
}

async function gzip(text) {
  if (typeof CompressionStream === "undefined" || text.length < COMPRESS_FROM) return null;
  try {
    const stream = new Blob([text]).stream().pipeThrough(new CompressionStream("gzip"));
    return await new Response(stream).arrayBuffer();
  } catch {
    return null;
  }
}

async function gunzip(buffer) {
  const stream = new Blob([buffer]).stream().pipeThrough(new DecompressionStream("gzip"));
  return new Response(stream).text();
}

function remember(key, data, bytes) {
  const old = memory.get(key);
  if (old) memoryBytes -= old.bytes;
  memory.delete(key);
  memory.set(key, { data, bytes });
  memoryBytes += bytes;
  while (memoryBytes > MEMORY_LIMIT && memory.size > 1) {
    const [oldest, value] = memory.entries().next().value;
    memory.delete(oldest);
    memoryBytes -= value.bytes;
  }
}

function forget(key) {
  const old = memory.get(key);
  if (old) memoryBytes -= old.bytes;
  memory.delete(key);
}

function scheduleWrites() {
  if (writeTimer) return;
  writeTimer = setTimeout(flushWrites, 400);
}

async function flushWrites() {
  writeTimer = 0;
  const batch = [...pendingWrites.entries()];
  pendingWrites.clear();
  const db = await openDb();
  if (!db || !batch.length) return;
  const prepared = [];
  for (const [key, item] of batch) {
    if (item.text == null) { prepared.push([key, item.meta, undefined]); continue; }
    const packed = await gzip(item.text);
    item.meta.gz = Boolean(packed);
    item.meta.stored = packed ? packed.byteLength : item.text.length * 2;
    prepared.push([key, item.meta, packed || item.text]);
  }
  try {
    const tx = db.transaction([META, BODY], "readwrite");
    for (const [key, meta, body] of prepared) {
      if (!metaIndex?.has(key)) continue; // removed meanwhile
      tx.objectStore(META).put(meta);
      if (body !== undefined) tx.objectStore(BODY).put(body, key);
    }
    await txDone(tx);
  } catch { /* quota or private mode: keep memory only */ }
  enforceCap();
}

async function deleteKeys(keys) {
  if (!keys.length) return;
  for (const key of keys) {
    metaIndex?.delete(key);
    pendingWrites.delete(key);
    forget(key);
  }
  const db = await openDb();
  if (!db) return;
  try {
    const tx = db.transaction([META, BODY], "readwrite");
    for (const key of keys) {
      tx.objectStore(META).delete(key);
      tx.objectStore(BODY).delete(key);
    }
    await txDone(tx);
  } catch { /* ignore */ }
}

function enforceCap() {
  if (!metaIndex) return;
  const entries = [...metaIndex.values()].map((meta) => ({
    key: meta.key, project: meta.project, bytes: meta.stored || meta.bytes || 0,
    used: meta.used || 0, kind: meta.kind,
  }));
  const evict = planEviction(entries, capBytes, { protect: state.project });
  if (evict.length) deleteKeys(evict);
}

const state = { project: "" };

export const clientCache = {
  get identity() { return identity; },
  get cacheId() { return cacheId; },
  get project() { return state.project; },
  set project(value) { state.project = String(value || ""); },

  /**
   * Bind the cache to the signed-in account.  Entries of any other account
   * on this browser profile are deleted (account switch on a shared
   * browser); an empty id (signed out, unknown) disables reads and writes.
   */
  async configure({ cacheId: id = "", schema = 0, project = "", cap } = {}) {
    cacheId = String(id || "");
    identity = identityScope({ origin: location.origin, cacheId, schema });
    if (project !== undefined) state.project = String(project || "");
    if (cap) capBytes = cap;
    if (!identity) return;
    const index = await loadMeta();
    const foreign = [...index.keys()].filter((key) => !key.startsWith(`${identity}|`));
    if (foreign.length) await deleteKeys(foreign);
  },

  keyFor(path, project = state.project) {
    return entryKey({ identity, project }, path);
  },

  cacheable(path) {
    return Boolean(this.keyFor(path));
  },

  /** Cached entry {data, etag, validatedAt, stale, cursor} or null. */
  async get(path, project = state.project) {
    const key = this.keyFor(path, project);
    if (!key) return null;
    const index = await loadMeta();
    const meta = index.get(key);
    if (!meta) return null;
    meta.used = Date.now();
    let held = memory.get(key);
    if (!held) {
      const pending = pendingWrites.get(key);
      let text = pending?.text;
      if (text == null) {
        const db = await openDb();
        if (!db) return null;
        try {
          const tx = db.transaction(BODY, "readonly");
          const body = await idbRequest(tx.objectStore(BODY).get(key));
          if (body == null) return null;
          text = typeof body === "string" ? body : await gunzip(body);
        } catch {
          return null;
        }
      }
      let data;
      try { data = JSON.parse(text); } catch { await deleteKeys([key]); return null; }
      if (!metaIndex.has(key)) return null;
      remember(key, data, meta.bytes || text.length);
      held = memory.get(key);
    } else {
      remember(key, held.data, held.bytes);
    }
    return { ...meta, data: held.data };
  },

  /** ETag of a cached route for a project under any account (or ""). */
  async anyEtag(path, project = state.project) {
    const rule = cacheRoute(path);
    if (!rule || (rule.scope === "project" && !project)) return "";
    const index = await loadMeta();
    const suffix = rule.scope === "project" ? `|p:${project}|${rule.route}` : `|a|${rule.route}`;
    for (const meta of index.values()) {
      if (meta.key.endsWith(suffix) && meta.etag && !meta.stale) return meta.etag;
    }
    return "";
  },

  /** Decoded entry already in memory ({data, cursor, ...}) or null; no I/O. */
  peek(path, project = state.project) {
    const key = this.keyFor(path, project);
    const meta = key && metaIndex?.get(key);
    const held = key && memory.get(key);
    return meta && held ? { ...meta, data: held.data } : null;
  },

  /** Store a response.  ``text`` is the JSON body when already at hand. */
  put(path, data, { etag = "", cursor = "", text = null, project = state.project } = {}) {
    const key = this.keyFor(path, project);
    if (!key || !metaIndex) return;
    const rule = cacheRoute(path);
    const body = text ?? JSON.stringify(data);
    const now = Date.now();
    const meta = {
      key, project: rule.scope === "project" ? project : "", route: rule.route, kind: rule.kind,
      etag, cursor: cursor || data?.cursor || "", validatedAt: now, savedAt: now, used: now,
      stale: false, bytes: body.length,
    };
    metaIndex.set(key, meta);
    remember(key, data, body.length);
    pendingWrites.set(key, { meta, text: body });
    scheduleWrites();
  },

  /** A 304 or fresh delta confirmed the entry. */
  validated(path, { project = state.project, cursor } = {}) {
    const key = this.keyFor(path, project);
    const meta = key && metaIndex?.get(key);
    if (!meta) return;
    meta.validatedAt = Date.now();
    meta.stale = false;
    if (cursor) meta.cursor = cursor;
    if (!pendingWrites.has(key)) pendingWrites.set(key, { meta, text: null });
    scheduleWrites();
  },

  /** Mark entries of the project stale when ``test(route, meta)`` is true. */
  markStale(test, project = state.project) {
    if (!metaIndex || !identity) return 0;
    const prefix = projectPrefix(identity, project);
    let count = 0;
    for (const meta of metaIndex.values()) {
      if (!meta.key.startsWith(prefix) || meta.stale) continue;
      if (test(meta.route, meta)) {
        meta.stale = true;
        count += 1;
        if (!pendingWrites.has(meta.key)) pendingWrites.set(meta.key, { meta, text: null });
      }
    }
    if (count) scheduleWrites();
    return count;
  },

  /** Unit detail ids cached for a project (prefetch skips them). */
  cachedUnitIds(project = state.project) {
    const out = new Set();
    if (!metaIndex || !identity) return out;
    const prefix = projectPrefix(identity, project);
    for (const meta of metaIndex.values()) {
      if (meta.key.startsWith(prefix) && meta.kind === "unit" && !meta.stale) {
        out.add(meta.route.split("/").pop());
      }
    }
    return out;
  },

  /** Forget one cached route (the server answered 403/404 for it). */
  async remove(path, project = state.project) {
    const key = this.keyFor(path, project);
    if (key) await deleteKeys([key]);
  },

  /** Forget one project of this account (grant revoked, project deleted). */
  async dropProject(project) {
    if (!project) return;
    const index = await loadMeta();
    const marker = `|p:${project}|`;
    await deleteKeys([...index.keys()].filter((key) => key.includes(marker)));
  },

  /**
   * Delete every cached entry of every account.  Sign-out and an invalid
   * session also unbind the account (``signOut``); Settings › Clear keeps
   * caching for the signed-in account afterwards.
   */
  async clearAll({ signOut = true } = {}) {
    const index = await loadMeta();
    await deleteKeys([...index.keys()]);
    memory.clear();
    memoryBytes = 0;
    if (signOut) {
      identity = "";
      cacheId = "";
    }
  },

  async usage() {
    const index = await loadMeta();
    let bytes = 0;
    const projects = new Set();
    for (const meta of index.values()) {
      bytes += meta.stored || meta.bytes || 0;
      if (meta.project) projects.add(meta.project);
    }
    return { bytes, entries: index.size, projects: projects.size, cap: capBytes };
  },

  flush: () => flushWrites(),
};
