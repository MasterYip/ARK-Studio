// Agent presence on the graphs (T-0043): which agent is on which node.
// Pure and DOM-free so tests/js/presence_model.test.mjs can import it.
//
// The server (ark_studio/server/internal/server/routes_presence.go) sends the
// project's live entries, in full, with GET /api/presence and as
// "event: presence" on GET /api/events?presence=1:
//   {project, now, ttl, entries: [{actor, label, color_actor, color_key,
//    kind: "cursor"|"working",
//    state: "reading"|"writing"|"working", unit, uid, title, action, note,
//    at, expires_at}]}
// An agent has at most one cursor (the node it touched last) and one working
// entry (the node it announced).  This model keeps what the overlay draws:
// entries that left fade out for FADE_MS, an entry whose node changed
// remembers where it came from so its badge can glide over.

export const FADE_MS = 700;
export const GLIDE_MS = 520;

// Distinct, readable on light and dark canvases.
export const PRESENCE_COLORS = [
  "#e5484d", "#3e63dd", "#30a46c", "#f76b15", "#8e4ec6",
  "#0894b3", "#d6409f", "#978365", "#5b5bd6", "#2b9a66",
];

const STATE_RANK = { working: 0, writing: 1, reading: 2 };

function hash(text) {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i += 1) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

/** Up to two letters for a badge: "claude" -> "CL", "gemini-cli" -> "GC", "研究助手" -> "研". */
export function presenceInitials(label) {
  const text = String(label || "").trim();
  if (!text) return "?";
  const first = [...text][0];
  if (/[^\u0000-ɏ]/.test(first)) return first;
  const words = text.replace(/([a-z])([A-Z])/g, "$1 $2").split(/[^A-Za-z0-9]+/).filter(Boolean);
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
  return (words[0] || text).slice(0, 2).toUpperCase();
}

export function createPresenceModel() {
  return {
    entries: new Map(), // key -> entry (live and fading)
    colors: new Map(), // color key -> colour index, kept for the session
    skew: 0, // server clock minus this clock (ms)
    project: null,
  };
}

function activeColorKeys(model, except = null) {
  const out = new Set();
  for (const entry of model.entries.values()) {
    if (!entry.leaving && entry.colorKey !== except) out.add(entry.colorKey || entry.actor);
  }
  return out;
}

/**
 * The agent's colour: stable for the session, and different from every other
 * agent shown at the same time while the palette lasts.
 */
export function presenceColor(model, actor, colorKey = actor) {
  if (model.colors.has(colorKey)) return PRESENCE_COLORS[model.colors.get(colorKey)];
  const taken = new Set([...activeColorKeys(model, colorKey)].map((other) => model.colors.get(other)).filter((i) => i !== undefined));
  const start = hash(colorKey) % PRESENCE_COLORS.length;
  let index = start;
  for (let step = 0; step < PRESENCE_COLORS.length; step += 1) {
    const candidate = (start + step) % PRESENCE_COLORS.length;
    if (!taken.has(candidate)) { index = candidate; break; }
  }
  model.colors.set(colorKey, index);
  return PRESENCE_COLORS[index];
}

function parseTime(value) {
  const ms = Date.parse(value || "");
  return Number.isFinite(ms) ? ms : null;
}

/**
 * Merge a server payload (the full set of live entries).
 * @returns {{changed: boolean, added: string[], moved: string[], removed: string[]}}
 */
export function mergePresence(model, payload, now = Date.now()) {
  const result = { changed: false, added: [], moved: [], removed: [] };
  if (!payload || !Array.isArray(payload.entries)) return result;
  if (payload.project && model.project && payload.project !== model.project) {
    // Another project: nothing of the old one glides or fades here.
    model.entries.clear();
  }
  if (payload.project) model.project = payload.project;
  const serverNow = parseTime(payload.now);
  if (serverNow !== null) model.skew = serverNow - now;
  const incoming = new Map();
  for (const raw of payload.entries) {
    if (!raw || !raw.actor || raw.unit == null) continue;
    incoming.set(`${raw.actor}|${raw.kind || "cursor"}`, raw);
  }
  // Entries that leave now, by actor: a new entry of the same agent glides
  // from there (e.g. "done" turning a working entry into a cursor elsewhere).
  const leavingNow = new Map();
  for (const [key, entry] of model.entries) {
    if (incoming.has(key) || entry.leaving) continue;
    entry.leaving = true;
    entry.leftAt = now;
    leavingNow.set(entry.actor, entry);
    result.removed.push(key);
  }
  for (const [key, raw] of incoming) {
    let entry = model.entries.get(key);
    const fields = {
      actor: raw.actor, label: raw.label || raw.actor, colorKey: raw.color_key || raw.color_actor || raw.actor,
      kind: raw.kind || "cursor",
      state: raw.state || "reading", unit: raw.unit, uid: raw.uid || "", title: raw.title || "",
      action: raw.action || "", note: raw.note || "",
      at: parseTime(raw.at) ?? now, expiresAt: parseTime(raw.expires_at) ?? (now + 30000),
    };
    if (!entry) {
      entry = { key, ...fields, initials: presenceInitials(fields.label), color: presenceColor(model, raw.actor, fields.colorKey),
        prevUnit: null, movedAt: 0, appearedAt: now, leaving: false, leftAt: 0 };
      const from = leavingNow.get(raw.actor) || [...model.entries.values()].find((other) => other.actor === raw.actor && other !== entry);
      if (from && String(from.unit) !== String(fields.unit)) { entry.prevUnit = from.unit; entry.movedAt = now; }
      // The same agent on the same node under another kind (a working entry
      // turning into a cursor on "done"): one badge throughout, no fade.
      else if (from) entry.appearedAt = now - 1000;
      model.entries.set(key, entry);
      result.added.push(key);
      result.changed = true;
      continue;
    }
    const moved = String(entry.unit) !== String(fields.unit);
    const before = `${entry.state}|${entry.action}|${entry.note}|${entry.label}|${entry.leaving}`;
    if (moved) { entry.prevUnit = entry.unit; entry.movedAt = now; result.moved.push(key); }
    Object.assign(entry, fields, { leaving: false, leftAt: 0 });
    entry.initials = presenceInitials(entry.label);
    entry.color = presenceColor(model, entry.actor, entry.colorKey);
    if (moved || before !== `${entry.state}|${entry.action}|${entry.note}|${entry.label}|false`) result.changed = true;
  }
  if (result.removed.length) result.changed = true;
  return result;
}

/**
 * Expire entries the server has not refreshed (a stalled stream) and forget
 * the ones that finished fading.  Returns whether anything changed.
 */
export function expirePresence(model, now = Date.now()) {
  let changed = false;
  const serverNow = now + model.skew;
  for (const [key, entry] of model.entries) {
    if (!entry.leaving && entry.expiresAt <= serverNow) {
      entry.leaving = true;
      entry.leftAt = now;
      changed = true;
    }
    if (entry.leaving && now - entry.leftAt >= FADE_MS) {
      model.entries.delete(key);
      changed = true;
    }
  }
  return changed;
}

/** Drop everything at once (project switch, setting off). */
export function clearPresence(model) {
  model.entries.clear();
  model.project = null;
}

/** Entries by unit id (as a string), stacked: working, writing, reading; then by agent. */
export function presenceByUnit(model) {
  const out = new Map();
  for (const entry of model.entries.values()) {
    const id = String(entry.unit);
    if (!out.has(id)) out.set(id, []);
    out.get(id).push(entry);
  }
  for (const [id, all] of out) {
    // A fading badge of an agent that is still on the node is not drawn twice.
    const live = new Set(all.filter((entry) => !entry.leaving).map((entry) => entry.actor));
    const list = all.filter((entry) => !entry.leaving || !live.has(entry.actor));
    out.set(id, list);
    list.sort((a, b) => (a.leaving - b.leaving)
      || ((STATE_RANK[a.state] ?? 3) - (STATE_RANK[b.state] ?? 3))
      || a.actor.localeCompare(b.actor) || a.kind.localeCompare(b.kind));
  }
  return out;
}

/** Live entries on one unit (by id or uid), most active first. */
export function presenceOnUnit(model, unit) {
  const ids = new Set([unit?.id, unit?.uid].filter((v) => v != null && v !== "").map(String));
  return [...model.entries.values()]
    .filter((entry) => !entry.leaving && (ids.has(String(entry.unit)) || (entry.uid && ids.has(entry.uid))))
    .sort((a, b) => ((STATE_RANK[a.state] ?? 3) - (STATE_RANK[b.state] ?? 3)) || b.at - a.at);
}

/** The agents shown now, one item each, with their entries (most recent first). */
export function activeAgents(model) {
  const byActor = new Map();
  for (const entry of model.entries.values()) {
    if (entry.leaving) continue;
    if (!byActor.has(entry.actor)) byActor.set(entry.actor, { actor: entry.actor, label: entry.label, color: entry.color, initials: entry.initials, entries: [] });
    byActor.get(entry.actor).entries.push(entry);
  }
  const out = [...byActor.values()];
  for (const agent of out) agent.entries.sort((a, b) => b.at - a.at);
  return out.sort((a, b) => a.label.localeCompare(b.label) || a.actor.localeCompare(b.actor));
}

/** Badge opacity at `now`: fades in, and out after leaving. */
export function presenceAlpha(entry, now) {
  if (entry.leaving) return Math.max(0, 1 - (now - entry.leftAt) / FADE_MS);
  return Math.min(1, Math.max(0, (now - (entry.appearedAt || 0)) / 260));
}

/** Glide progress 0..1 (eased) since the entry moved; 1 when it did not. */
export function presenceGlide(entry, now) {
  if (entry.prevUnit == null || !entry.movedAt) return 1;
  const raw = Math.min(1, Math.max(0, (now - entry.movedAt) / GLIDE_MS));
  return 1 - Math.pow(1 - raw, 3);
}

/** Whether anything is still animating (gliding, fading in or out). */
export function presenceAnimating(model, now) {
  for (const entry of model.entries.values()) {
    if (entry.leaving) return true;
    if (now - (entry.appearedAt || 0) < 260) return true;
    if (entry.prevUnit != null && now - entry.movedAt < GLIDE_MS) return true;
  }
  return false;
}
