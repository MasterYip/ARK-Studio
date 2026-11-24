// Per-tab filter memory and user defaults for the Filters rail.
// Dependency-free (state.js injects emptyFilters / normalizeFilters /
// FILTER_MODEL_VERSION), so `node --test tests/js/filter_store.test.mjs` covers it.
//
// localStorage keys, one value per project and route key (routeKey() in
// state.js: "map", "ideas", "plan/story", ...):
//   ark.filters.v1.<project>.<tab>          last filters used on that tab
//   ark.filters.v1.<project>.<tab>.default  the user's "Set as default"
// Each value is JSON { v: FILTER_MODEL_VERSION, filters: {...} }.  Older
// model versions go through migrateFilters (version steps) and then
// normalizeFilters (which also accepts v1 shapes); a newer/unknown version or
// malformed JSON is ignored and the tab falls back to its default.
//
// Model versions: v1 legacy shapes, v2 tag/status/importance groups,
// v3 adds `assignees` (["me" | "none" | <username>], OR within the group).
// `minLinks` (0 = off) is an additive v3 field: normalizeFilters defaults it to
// 0, so saves without it need no migration step.

export const STORE_PREFIX = "ark.filters.v1";

function part(value) {
  return encodeURIComponent(String(value ?? "") || "_").replaceAll(".", "%2E");
}

export function storageKey(projectId, tabKey, kind = "current") {
  const base = `${STORE_PREFIX}.${part(projectId)}.${part(tabKey)}`;
  return kind === "default" ? `${base}.default` : base;
}

export function encodeFilters(filters, version) {
  return JSON.stringify({ v: version, filters });
}

/**
 * Upgrade a stored filter object from model version `from` to the current one.
 * Each step only adds or reshapes fields; nothing the user chose is dropped.
 */
export function migrateFilters(filters, from) {
  const out = { ...(filters || {}) };
  if (from < 3) {
    // v3: assignee filter group.  Older saves never filtered by assignee.
    out.assignees = [];
  }
  return out;
}

/** Clean an assignee filter list: "me", "none" or usernames, de-duplicated. */
export function normalizeAssigneeValues(raw) {
  const list = Array.isArray(raw) ? raw : (typeof raw === "string" ? raw.split(",") : []);
  const out = [];
  for (const item of list) {
    let value = String(item ?? "").trim().slice(0, 64);
    if (!value) continue;
    const low = value.toLowerCase();
    if (low === "me" || low === "none" || low === "unassigned") value = low === "unassigned" ? "none" : low;
    if (!out.some((x) => x.toLowerCase() === value.toLowerCase())) out.push(value);
  }
  return out;
}

/** Client-side twin of the server's assignee filter (effective assignee). */
export function assigneeMatches(node, wanted, me = "") {
  if (!wanted?.length) return true;
  const name = String(node?.assignee_effective || node?.assignee || "").toLowerCase();
  return wanted.some((value) => {
    if (value === "none") return !name;
    if (value === "me") return Boolean(me) && name === String(me).toLowerCase();
    return Boolean(name) && name === String(value).toLowerCase();
  });
}

/** Parse a stored value; returns normalized filters or null (use defaults). */
export function decodeFilters(raw, { version, normalize, migrate = migrateFilters }) {
  if (typeof raw !== "string" || !raw) return null;
  let data;
  try { data = JSON.parse(raw); } catch { return null; }
  if (!data || typeof data !== "object" || !data.filters || typeof data.filters !== "object") return null;
  const v = Number(data.v);
  if (!Number.isInteger(v) || v < 1 || v > version) return null;
  const filters = v < version && migrate ? migrate({ ...data.filters }, v) : { ...data.filters };
  return normalize(filters);
}

/** Canonical form for comparisons: normalized, arrays sorted, version dropped. */
export function canonicalFilters(filters, normalize) {
  const out = normalize(filters || {});
  delete out.v;
  for (const [key, value] of Object.entries(out)) {
    if (Array.isArray(value)) out[key] = [...value].sort();
  }
  return out;
}

/** Keys whose value differs between `current` and `reference`. */
export function diffFilters(current, reference, normalize) {
  const a = canonicalFilters(current, normalize);
  const b = canonicalFilters(reference, normalize);
  return [...new Set([...Object.keys(a), ...Object.keys(b)])]
    .filter((key) => JSON.stringify(a[key]) !== JSON.stringify(b[key]))
    .sort();
}

export function filtersEqual(a, b, normalize) {
  return diffFilters(a, b, normalize).length === 0;
}

const clone = (value) => JSON.parse(JSON.stringify(value));

/**
 * @param {object} opts
 * @param {Storage|null} opts.storage   localStorage-like; failures are ignored
 * @param {() => object} opts.empty     built-in default (emptyFilters)
 * @param {(raw: object) => object} opts.normalize
 * @param {number} opts.version         FILTER_MODEL_VERSION
 */
export function createFilterStore({ storage = null, empty, normalize, version, migrate = migrateFilters }) {
  // In-memory mirror so tab memory still works when storage is unavailable
  // (private mode, quota) for the lifetime of the page.
  const memory = new Map();
  const read = (key) => {
    if (memory.has(key)) return memory.get(key);
    try { return storage?.getItem?.(key) ?? null; } catch { return null; }
  };
  const write = (key, value) => {
    memory.set(key, value);
    try {
      if (value === null) storage?.removeItem?.(key);
      else storage?.setItem?.(key, value);
    } catch { /* storage full or blocked: memory copy still applies */ }
  };
  const decode = (raw) => decodeFilters(raw, { version, normalize, migrate });

  const savedDefault = (projectId, tabKey) => decode(read(storageKey(projectId, tabKey, "default")));
  const resolveDefault = (projectId, tabKey) => savedDefault(projectId, tabKey) || empty();

  return {
    /** Filters to show when entering a tab: last used, else the tab default. */
    load(projectId, tabKey) {
      return decode(read(storageKey(projectId, tabKey))) || resolveDefault(projectId, tabKey);
    },
    save(projectId, tabKey, filters) {
      write(storageKey(projectId, tabKey), encodeFilters(normalize(clone(filters || {})), version));
    },
    savedDefault,
    hasSavedDefault: (projectId, tabKey) => Boolean(savedDefault(projectId, tabKey)),
    resolveDefault,
    setDefault(projectId, tabKey, filters) {
      write(storageKey(projectId, tabKey, "default"), encodeFilters(normalize(clone(filters || {})), version));
    },
    clearDefault(projectId, tabKey) {
      write(storageKey(projectId, tabKey, "default"), null);
    },
    diffFromDefault(projectId, tabKey, filters) {
      return diffFilters(filters, resolveDefault(projectId, tabKey), normalize);
    },
  };
}
