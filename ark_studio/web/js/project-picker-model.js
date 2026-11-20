// Project picker model (T-0034): personal pins and hidden projects, the
// order of the cards and the grid's keyboard moves.  Dependency-free so the
// node tests load it as is.
//
// Order: pinned projects first, in the order they were pinned; then the
// rest by the person's own recent activity (my_activity from GET
// /api/projects), ties by the last time they were active there (or, for a
// project they never touched, its own update time), then by name.  Hidden
// projects leave the grid for a collapsed "Hidden (N)" section, in the same
// order.  With the "Created by me" filter on (prefs.only_mine), both the
// grid and the hidden section keep only projects the server marks
// created_by_me (the viewer's own account created them; legacy projects
// without a known creator are not the viewer's).

export const PROJECT_PREFS_KEY = "ark.projectPicker.v1";
export const MAX_PROJECT_PREF_IDS = 200; // settings.maxProjectPrefIDs (Go)
const MAX_ID_LENGTH = 200;

function idList(value) {
  const out = [];
  const seen = new Set();
  for (const id of Array.isArray(value) ? value : []) {
    // eslint-disable-next-line no-control-regex
    if (typeof id !== "string" || !id || [...id].length > MAX_ID_LENGTH || /[\u0000-\u001f\u007f-\u009f]/.test(id)) continue;
    if (seen.has(id)) continue;
    seen.add(id);
    out.push(id);
    if (out.length >= MAX_PROJECT_PREF_IDS) break;
  }
  return out;
}

/**
 * {pinned, hidden} (+ only_mine: true when the "Created by me" filter is
 * on): deduplicated ids; an id is never both (hidden wins, like the server).
 */
export function normalizeProjectPrefs(raw) {
  const hidden = idList(raw?.hidden);
  const isHidden = new Set(hidden);
  const out = { pinned: idList(raw?.pinned).filter((id) => !isHidden.has(id)), hidden };
  if (raw?.only_mine === true) out.only_mine = true;
  return out;
}

export function prefsEmpty(prefs) {
  return !prefs?.pinned?.length && !prefs?.hidden?.length && prefs?.only_mine !== true;
}

/** Pin (to the end of the pin order) or unpin; pinning also unhides. */
export function togglePinned(prefs, id) {
  const p = normalizeProjectPrefs(prefs);
  if (p.pinned.includes(id)) return { ...p, pinned: p.pinned.filter((x) => x !== id) };
  return { ...p, pinned: [...p.pinned, id], hidden: p.hidden.filter((x) => x !== id) };
}

/** Hide or unhide; hiding also unpins. */
export function toggleHidden(prefs, id) {
  const p = normalizeProjectPrefs(prefs);
  if (p.hidden.includes(id)) return { ...p, hidden: p.hidden.filter((x) => x !== id) };
  return { ...p, pinned: p.pinned.filter((x) => x !== id), hidden: [...p.hidden, id] };
}

/** Turn the "Created by me" filter on or off (`on` forces a state). */
export function setOnlyMine(prefs, on) {
  const { only_mine: _was, ...rest } = normalizeProjectPrefs(prefs);
  return on ? { ...rest, only_mine: true } : rest;
}

function stampMs(value) {
  const ms = value ? Date.parse(value) : NaN;
  return Number.isFinite(ms) ? ms : 0;
}

/** When the person was last active in the project (else when it changed). */
export function lastActiveMs(project) {
  return stampMs(project?.my_last_active_at) || stampMs(project?.updated) || stampMs(project?.created);
}

function score(project) {
  const n = Number(project?.my_activity);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

export function compareByActivity(a, b) {
  return score(b) - score(a)
    || lastActiveMs(b) - lastActiveMs(a)
    || String(a?.name || "").localeCompare(String(b?.name || ""), undefined, { sensitivity: "base" })
    || String(a?.id || "").localeCompare(String(b?.id || ""));
}

/** {shown, hidden} project lists in picker order. */
export function arrangeProjects(projects, prefs) {
  const p = normalizeProjectPrefs(prefs);
  const pinAt = new Map(p.pinned.map((id, index) => [id, index]));
  const isHidden = new Set(p.hidden);
  const listed = (projects || []).filter((item) => item && item.id != null);
  // Only a list that says who created what can be filtered (older servers do not).
  const onlyMine = p.only_mine === true && listed.some((item) => "created_by_me" in item);
  const items = onlyMine ? listed.filter((item) => item.created_by_me === true) : listed;
  const pinned = items.filter((item) => pinAt.has(item.id)).sort((a, b) => pinAt.get(a.id) - pinAt.get(b.id));
  const rest = items.filter((item) => !pinAt.has(item.id) && !isHidden.has(item.id)).sort(compareByActivity);
  const hidden = items.filter((item) => isHidden.has(item.id)).sort(compareByActivity);
  return { shown: [...pinned, ...rest], hidden };
}

// Go-only members of GET /api/projects (server project_people.go).
const PEOPLE_KEYS = ["creator", "created_by_me", "last_actor"];
// last_actor names the change behind "updated" when its time is at most this
// much before it, or LAST_ACTOR_LAG_MS after it (same tolerance as the server).
const LAST_ACTOR_LEAD_MS = 60_000;
const LAST_ACTOR_LAG_MS = 5_000;

const sameUser = (a, b) => Boolean(a) && Boolean(b) && String(a).toLowerCase() === String(b).toLowerCase();

/**
 * Who a card names: {creator, updater}, each null or {user, name, me,
 * agent}.  `me` is the viewer's username; agent is the agent's label
 * ("MasterYip/视频字体") when an agent made the change.  A last_actor that
 * does not match the project's "updated" (an older list merged with a newer
 * one) is left out.
 */
export function cardPeople(project, me = "") {
  const c = project?.creator;
  const creator = c && c.user ? { user: c.user, name: c.name || c.user, me: project.created_by_me === true || sameUser(c.user, me), agent: null } : null;
  const a = project?.last_actor;
  let updater = null;
  if (a && (a.user || a.agent)) {
    const at = stampMs(a.at), updated = stampMs(project.updated);
    if (at && updated && at >= updated - LAST_ACTOR_LEAD_MS && at <= updated + LAST_ACTOR_LAG_MS) {
      updater = { user: a.user || "", name: a.name || a.user || "", me: !a.agent && sameUser(a.user, me), agent: a.agent || null };
    }
  }
  return { creator, updater };
}

/**
 * The card's two people strings.  `t` is i18n's t, `when` formats a time
 * (dom.js relativeTime); `name(user, fallback)` is the display name.
 * creator: "MasterYip 创建" / "Created by MasterYip" or ""; time: the
 * footer, "ChenZiyan 更新于 4分钟前" / "Updated 4 min ago by ChenZiyan",
 * else the footer the picker showed before.
 */
export function cardPeopleText(project, { t, when, me = "", name = (user, fallback) => fallback || user } = {}) {
  const { creator, updater } = cardPeople(project, me);
  const who = (p) => p.agent || name(p.user, p.name);
  const creatorText = !creator ? ""
    : creator.me ? t("project.picker.created_by_me") : t("project.picker.created_by", { name: who(creator) });
  let time = "";
  if (updater) {
    time = updater.me ? t("project.picker.updated_by_me", { time: when(project.updated) })
      : t("project.picker.updated_by", { name: who(updater), time: when(project.updated) });
  } else if (project?.my_last_active_at) {
    time = t("project.picker.active", { time: when(project.my_last_active_at) });
  } else {
    const changed = project?.updated || project?.created;
    time = changed ? t("project.picker.updated", { time: when(changed) }) : "";
  }
  return { creator: creatorText, time, people: { creator, updater } };
}

/**
 * Bootstrap replies list the projects without the per-person activity; keep
 * what the last GET /api/projects said for the ids both lists know.
 */
export function withKnownActivity(next, previous) {
  if (!Array.isArray(next)) return next;
  // Bootstrap lists also lack creator / last_actor (GET /api/projects only).
  const known = new Map((previous || []).filter((p) => p && "my_activity" in p).map((p) => [p.id, p]));
  return next.map((p) => {
    const old = p && !("my_activity" in p) ? known.get(p.id) : null;
    if (!old) return p;
    const out = { ...p, my_activity: old.my_activity, my_last_active_at: old.my_last_active_at };
    for (const key of PEOPLE_KEYS) if (!(key in p) && key in old) out[key] = old[key];
    return out;
  });
}

/**
 * A project list shown from memory is refreshed in the background.  The
 * cards already on screen keep their places (new projects go last, removed
 * ones drop out), so a click aimed at a card never lands on another project.
 * Returns null when nothing visible changed.
 */
export function refreshedProjectRows(shown, fresh) {
  const key = (items) => JSON.stringify((items || []).map((p) => [p.id, p.name, p.description, p.path, p.demo,
    p.my_last_active_at || null, p.updated || null, p.creator || null, p.last_actor || null, p.created_by_me === true]));
  const place = new Map((shown || []).map((p, index) => [p.id, index]));
  const ordered = [...(fresh || [])].map((p, index) => ({ p, index }))
    .sort((a, b) => (place.get(a.p.id) ?? Infinity) - (place.get(b.p.id) ?? Infinity) || a.index - b.index)
    .map((item) => item.p);
  return key(ordered) === key(shown) ? null : ordered;
}

/** Number of grid columns from the cards' top offsets (cards of one row share it). */
export function columnsFromTops(tops) {
  if (!tops?.length) return 1;
  let n = 0;
  while (n < tops.length && Math.abs(tops[n] - tops[0]) < 2) n += 1;
  return Math.max(1, n);
}

/**
 * The card a key moves to in a grid of `count` cards and `columns` columns
 * (row-major), or -1 when the key is not a grid move.  No wrapping; Down
 * into a shorter last row lands on its last card.
 */
export function gridMove(index, key, count, columns) {
  if (count <= 0 || index < 0) return -1;
  const cols = Math.max(1, Math.floor(columns) || 1);
  const last = count - 1;
  switch (key) {
    case "ArrowRight": return Math.min(index + 1, last);
    case "ArrowLeft": return Math.max(index - 1, 0);
    case "ArrowDown": {
      if (index + cols <= last) return index + cols;
      return Math.floor(index / cols) < Math.floor(last / cols) ? last : index;
    }
    case "ArrowUp": return index - cols >= 0 ? index - cols : index;
    case "Home": return 0;
    case "End": return last;
    default: return -1;
  }
}

/**
 * Pins and hidden projects, saved with the account when there is a savable
 * profile (PATCH /api/me prefs.projects, `save(value)` resolves to the
 * stored value) and always cached in `storage` (the fallback without one).
 * Updates apply at once; a failed save rolls back and calls onError.
 */
export function createProjectPrefsStore({ storage = null, save = null, onError = null } = {}) {
  let prefs = normalizeProjectPrefs(null);
  let persist = false;
  let seq = 0;
  const listeners = new Set();
  const readLocal = () => {
    try { return JSON.parse(storage?.getItem(PROJECT_PREFS_KEY) || "null"); } catch { return null; }
  };
  const writeLocal = (value) => {
    try { storage?.setItem(PROJECT_PREFS_KEY, JSON.stringify(value)); } catch { /* storage disabled */ }
  };
  const set = (value) => {
    prefs = normalizeProjectPrefs(value);
    writeLocal(prefs);
    for (const fn of listeners) fn(prefs);
  };
  return {
    init(profile) {
      persist = Boolean(save && profile && !profile.permissions?.read_only);
      const fromAccount = persist && profile.prefs?.projects;
      prefs = normalizeProjectPrefs(fromAccount || readLocal());
      if (fromAccount) writeLocal(prefs);
    },
    get: () => prefs,
    persisted: () => persist,
    subscribe(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    async update(change) {
      const previous = prefs;
      set(typeof change === "function" ? change(prefs) : change);
      if (!persist) return true;
      const mine = ++seq;
      try {
        const stored = await save(prefsEmpty(prefs) ? null : prefs);
        if (mine === seq) set(stored || {});
        return true;
      } catch (err) {
        if (mine === seq) set(previous);
        onError?.(err);
        return false;
      }
    },
  };
}
