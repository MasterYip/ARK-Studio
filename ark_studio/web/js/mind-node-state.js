// Per-node Mind state: the card widths a person dragged and the images they
// pinned beside a card.  It is layout, not a display preference, so it is
// kept per project for the person: on the account profile
// (prefs.mind_layout[project_id], PATCH /api/me) when they have one, in this
// browser otherwise.  graph.js reads and writes it through the small store
// interface `{ get(uids), update({ widths, pinned }) }` (setMindNodeStore).
//
// Before T-0036 the widths and pins sat in localStorage keyed by node uid
// only ("ark.mindWidths", "ark.mindPinnedImages", shared by every project).
// They move lazily: when a project's Mind graph shows a node whose uid is in
// the old maps, that entry is adopted into the project and removed from the
// old map.  "ark.mindImageSizes" and "ark.mindPinnedMedia" were no longer
// written by anything and are dropped.

export const LEGACY_WIDTHS_KEY = "ark.mindWidths";
export const LEGACY_PINNED_KEY = "ark.mindPinnedImages";
export const RETIRED_KEYS = ["ark.mindImageSizes", "ark.mindPinnedMedia"];
export const LOCAL_LAYOUT_KEY = "ark.mindLayout.v1";
export const MIND_LAYOUT_NODE_LIMIT = 500;

const SIDES = ["right", "left", "top", "bottom"];
const finite = (value, lo, hi) => typeof value === "number" && Number.isFinite(value) && value >= lo && value <= hi;
const validUid = (uid) => typeof uid === "string" && uid.length > 0 && uid.length <= 200 && !/[\u0000-\u001f\u007f]/.test(uid);

function cleanWidth(value) {
  const n = Number(value);
  return finite(n, 60, 4000) ? Math.round(n) : null;
}

/** A pinned image in graph.js shape ({ mediaId, side, w, h, aspect }) or null. */
function cleanPinned(value) {
  if (!value || typeof value !== "object") return null;
  const mediaId = value.mediaId ?? value.media_id;
  if (!(typeof mediaId === "number" && Number.isInteger(mediaId)) && !(typeof mediaId === "string" && validUid(mediaId))) return null;
  const w = Number(value.w), h = Number(value.h);
  if (!finite(w, 8, 8000) || !finite(h, 8, 8000)) return null;
  const out = { mediaId, side: SIDES.includes(value.side) ? value.side : "right", w, h };
  const aspect = Number(value.aspect);
  if (finite(aspect, 0.01, 100)) out.aspect = aspect;
  return out;
}

/** Client state `{ widths: {uid: px}, pinned: {uid: image} }`, cleaned and bounded. */
export function normalizeMindNodeState(value) {
  const widths = {}, pinned = {};
  const src = value && typeof value === "object" ? value : {};
  for (const [uid, raw] of Object.entries(src.widths || {})) {
    const width = cleanWidth(raw);
    if (validUid(uid) && width !== null && Object.keys(widths).length < MIND_LAYOUT_NODE_LIMIT) widths[uid] = width;
  }
  for (const [uid, raw] of Object.entries(src.pinned || src.pinned_images || {})) {
    const image = cleanPinned(raw);
    if (validUid(uid) && image && Object.keys(pinned).length < MIND_LAYOUT_NODE_LIMIT) pinned[uid] = image;
  }
  return { widths, pinned };
}

/** Server entry (prefs.mind_layout[project]) from client state; null when empty. */
export function toServerEntry(state) {
  const clean = normalizeMindNodeState(state);
  const entry = {};
  if (Object.keys(clean.widths).length) entry.widths = clean.widths;
  if (Object.keys(clean.pinned).length) {
    entry.pinned_images = Object.fromEntries(Object.entries(clean.pinned).map(([uid, image]) => {
      const { mediaId, ...rest } = image;
      return [uid, { media_id: mediaId, ...rest }];
    }));
  }
  return Object.keys(entry).length ? entry : null;
}

/** Client state from a server entry. */
export function fromServerEntry(entry) {
  return normalizeMindNodeState({ widths: entry?.widths, pinned: entry?.pinned_images });
}

function parse(raw, fallback) {
  try { const value = raw ? JSON.parse(raw) : null; return value && typeof value === "object" ? value : fallback; }
  catch { return fallback; }
}

/**
 * Move old uid-keyed entries for `uids` into `state`.  An entry the
 * project already has wins.  Returns the new state, the remaining legacy
 * maps and whether anything moved.
 */
export function adoptLegacy(state, legacy, uids) {
  const next = normalizeMindNodeState(state);
  const rest = { widths: { ...(legacy?.widths || {}) }, pinned: { ...(legacy?.pinned || {}) } };
  let changed = false;
  for (const uid of uids || []) {
    if (uid in rest.widths) {
      const width = cleanWidth(rest.widths[uid]);
      // The old reader ignored widths under 126 px.
      if (width !== null && width >= 126 && !(uid in next.widths)) { next.widths[uid] = width; changed = true; }
      delete rest.widths[uid];
    }
    if (uid in rest.pinned) {
      const image = cleanPinned(rest.pinned[uid]);
      if (image && !(uid in next.pinned)) { next.pinned[uid] = image; changed = true; }
      delete rest.pinned[uid];
    }
  }
  return { state: changed ? normalizeMindNodeState(next) : next, legacy: rest, changed };
}

/**
 * The store graph.js uses.  `projectId()` names the open project;
 * `loadEntry(projectId)` returns its saved server entry (or null) and
 * `saveEntry(projectId, entry|null)` persists one (debounced here).
 */
export function createMindNodeStore({ projectId, loadEntry, saveEntry, storage = null, delay = 600 }) {
  const cache = new Map();
  let timer = 0;
  const dirty = new Set();
  const legacyRead = () => ({
    widths: parse(storage?.getItem(LEGACY_WIDTHS_KEY), {}),
    pinned: parse(storage?.getItem(LEGACY_PINNED_KEY), {}),
  });
  let legacy = null;
  try {
    for (const key of RETIRED_KEYS) storage?.removeItem(key);
    legacy = legacyRead();
  } catch { legacy = { widths: {}, pinned: {} }; }
  const writeLegacy = () => {
    try {
      for (const [key, map] of [[LEGACY_WIDTHS_KEY, legacy.widths], [LEGACY_PINNED_KEY, legacy.pinned]]) {
        if (Object.keys(map).length) storage?.setItem(key, JSON.stringify(map));
        else storage?.removeItem(key);
      }
    } catch { /* storage unavailable */ }
  };
  const current = () => {
    const id = String(projectId() || "");
    if (!cache.has(id)) cache.set(id, fromServerEntry(loadEntry(id)));
    return { id, state: cache.get(id) };
  };
  const flush = () => {
    timer = 0;
    for (const id of dirty) if (cache.has(id)) saveEntry(id, toServerEntry(cache.get(id)));
    dirty.clear();
  };
  const touch = (id) => {
    if (!id) return;
    dirty.add(id);
    clearTimeout(timer);
    timer = setTimeout(flush, delay);
  };
  return {
    get(uids = []) {
      const { id, state } = current();
      const hasLegacy = Object.keys(legacy.widths).length || Object.keys(legacy.pinned).length;
      if (id && hasLegacy && uids.length) {
        const result = adoptLegacy(state, legacy, uids);
        if (result.changed || Object.keys(result.legacy.widths).length !== Object.keys(legacy.widths).length
          || Object.keys(result.legacy.pinned).length !== Object.keys(legacy.pinned).length) {
          legacy = result.legacy;
          writeLegacy();
        }
        if (result.changed) { cache.set(id, result.state); touch(id); }
      }
      return cache.get(id) || state;
    },
    /** `{ widths: {uid: px|null}, pinned: {uid: image|null} }`; null removes. */
    update(patch) {
      const { id, state } = current();
      const next = { widths: { ...state.widths }, pinned: { ...state.pinned } };
      for (const [uid, width] of Object.entries(patch?.widths || {})) {
        if (width === null || width === undefined) delete next.widths[uid];
        else next.widths[uid] = width;
      }
      for (const [uid, image] of Object.entries(patch?.pinned || {})) {
        if (image === null || image === undefined) delete next.pinned[uid];
        else next.pinned[uid] = image;
      }
      cache.set(id, normalizeMindNodeState(next));
      touch(id);
      return cache.get(id);
    },
    /** Forget cached projects (a new account profile was adopted); pending saves go out first. */
    reset() {
      if (dirty.size) { clearTimeout(timer); flush(); }
      cache.clear();
    },
    flush,
  };
}

/** Browser-only fallback for people without a savable account profile. */
export function localLayoutBackend(storage) {
  const all = () => parse(storage?.getItem(LOCAL_LAYOUT_KEY), {});
  return {
    loadEntry: (id) => all()[id] || null,
    saveEntry: (id, entry) => {
      try {
        const map = all();
        if (entry) map[id] = entry; else delete map[id];
        storage?.setItem(LOCAL_LAYOUT_KEY, JSON.stringify(map));
      } catch { /* storage unavailable */ }
    },
  };
}
