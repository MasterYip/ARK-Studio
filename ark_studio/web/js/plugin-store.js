// Installed user plugins, per user, in this browser (IndexedDB "ark-plugins").
// Records: { key, user, id, manifest, html, enabled, granted, source,
// installedAt }.  Plugin code never goes to the server or the account.
// Without IndexedDB (private windows of some browsers) the list lives in
// memory for this page only.
//
// The "user plugins" switch (off by default) is a per-browser opt-in kept
// in localStorage: until it is on, nothing is imported or run.

const DB_NAME = "ark-plugins";
const STORE = "plugins";
const OPT_IN_KEY = "ark.plugins.userEnabled";
const memory = new Map();
let dbPromise = null;

function openDb() {
  if (dbPromise) return dbPromise;
  dbPromise = new Promise((resolve) => {
    try {
      const request = indexedDB.open(DB_NAME, 1);
      request.onupgradeneeded = () => {
        const store = request.result.createObjectStore(STORE, { keyPath: "key" });
        store.createIndex("user", "user");
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => resolve(null);
      request.onblocked = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
  return dbPromise;
}

const keyOf = (user, id) => `${user}\u0000${id}`;

function run(mode, fn) {
  return openDb().then((db) => new Promise((resolve, reject) => {
    if (!db) { resolve(fn(null)); return; }
    const tx = db.transaction(STORE, mode);
    const result = fn(tx.objectStore(STORE));
    tx.oncomplete = () => resolve(result?.result ?? result);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  }));
}

export function userPluginsEnabled() {
  try { return localStorage.getItem(OPT_IN_KEY) === "1"; } catch { return false; }
}

export function setUserPluginsEnabled(on) {
  try {
    if (on) localStorage.setItem(OPT_IN_KEY, "1"); else localStorage.removeItem(OPT_IN_KEY);
  } catch { /* storage disabled */ }
}

export async function listPlugins(user) {
  const owner = String(user || "");
  const db = await openDb();
  if (!db) return [...memory.values()].filter((item) => item.user === owner);
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, "readonly");
    const request = tx.objectStore(STORE).index("user").getAll(owner);
    request.onsuccess = () => resolve((request.result || []).sort((a, b) => (a.installedAt || 0) - (b.installedAt || 0)));
    request.onerror = () => reject(request.error);
  });
}

export async function savePlugin(user, record) {
  const item = { ...record, user: String(user || ""), key: keyOf(user, record.id) };
  await run("readwrite", (store) => {
    if (!store) { memory.set(item.key, item); return item; }
    return store.put(item);
  });
  return item;
}

export async function removePlugin(user, id) {
  await run("readwrite", (store) => {
    if (!store) { memory.delete(keyOf(user, id)); return true; }
    return store.delete(keyOf(user, id));
  });
}

export async function persistent() {
  return Boolean(await openDb());
}

// ------------------------------------------------- plugin key/value data
// The `storage` permission (plugin-api.js createPluginStorage): one entry per
// "user \0 plugin \0 project \0 key" in IndexedDB "ark-plugin-data", values
// as JSON strings.  Never sent to the server.  Memory fallback as above.
const DATA_DB = "ark-plugin-data";
const DATA_STORE = "kv";
const dataMemory = new Map();
let dataDbPromise = null;

function openDataDb() {
  if (dataDbPromise) return dataDbPromise;
  dataDbPromise = new Promise((resolve) => {
    try {
      const request = indexedDB.open(DATA_DB, 1);
      request.onupgradeneeded = () => request.result.createObjectStore(DATA_STORE, { keyPath: "key" });
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => resolve(null);
      request.onblocked = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
  return dataDbPromise;
}

function dataTx(mode, fn) {
  return openDataDb().then((db) => new Promise((resolve, reject) => {
    if (!db) { resolve(fn(null)); return; }
    const tx = db.transaction(DATA_STORE, mode);
    const request = fn(tx.objectStore(DATA_STORE));
    tx.oncomplete = () => resolve(request?.result);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  }));
}

export const pluginDataBackend = {
  async list(prefix) {
    const db = await openDataDb();
    if (!db) return [...dataMemory.entries()].filter(([key]) => key.startsWith(prefix)).map(([key, value]) => ({ key, value }));
    const rows = await dataTx("readonly", (store) => store.getAll(IDBKeyRange.bound(prefix, `${prefix}\uffff`)));
    return (rows || []).map((row) => ({ key: row.key, value: row.value }));
  },
  async get(key) {
    const db = await openDataDb();
    if (!db) return dataMemory.get(key);
    const row = await dataTx("readonly", (store) => store.get(key));
    return row?.value;
  },
  async put(key, value) {
    const db = await openDataDb();
    if (!db) { dataMemory.set(key, value); return; }
    await dataTx("readwrite", (store) => store.put({ key, value }));
  },
  async remove(key) {
    const db = await openDataDb();
    if (!db) { dataMemory.delete(key); return; }
    await dataTx("readwrite", (store) => store.delete(key));
  },
};
