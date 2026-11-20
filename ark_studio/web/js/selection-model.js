// Multi-selection: shared attributes for the inspector, batch-edit payloads
// and their undo, and the "same parent" test behind grouping siblings in the
// mind map (Ctrl+Enter).  Pure (no DOM, no API).

/** Attributes the multi-selection inspector shows and batch-edits. */
export const BATCH_FIELDS = ["type", "status", "importance", "assignee", "start_at", "end_at"];

const SYSTEM_TAG = /^(type|status|importance|needs):/;

const norm = (value) => (value === undefined || value === null ? "" : String(value));

/**
 * The value every unit shares for `key`, or `{ mixed: true }`.
 * Missing, null and "" count as the same empty value.
 */
export function commonValue(units = [], key) {
  if (!units.length) return { value: "", mixed: false };
  const first = norm(units[0]?.[key]);
  for (const unit of units) {
    if (norm(unit?.[key]) !== first) return { value: "", mixed: true };
  }
  return { value: first, mixed: false };
}

/** commonValue for each of BATCH_FIELDS. */
export function sharedAttributes(units = []) {
  return Object.fromEntries(BATCH_FIELDS.map((key) => [key, commonValue(units, key)]));
}

/** A unit's own tags: without type/status/importance and review (needs:) tags. */
export function ownTags(unit) {
  return (unit?.tags || []).filter((tag) => !SYSTEM_TAG.test(tag));
}

/** Plain tags are stored as "tag:value"; compare in that form. */
export function tagKey(tag) {
  const text = String(tag || "").trim();
  if (!text) return "";
  const at = text.indexOf(":");
  if (at < 0) return `tag:${text}`;
  return `${text.slice(0, at).trim().toLowerCase()}:${text.slice(at + 1).trim()}`;
}

/** Tags on every unit (`all`) and on only some (`some`, with counts). */
export function tagPresence(units = []) {
  const counts = new Map();
  for (const unit of units) {
    for (const tag of new Set(ownTags(unit))) counts.set(tag, (counts.get(tag) || 0) + 1);
  }
  const all = [];
  const some = [];
  for (const [tag, count] of [...counts].sort((a, b) => a[0].localeCompare(b[0]))) {
    if (count === units.length) all.push(tag);
    else some.push({ tag, count });
  }
  return { all, some };
}

/** Topics every unit belongs to and topics only some belong to. */
export function topicPresence(units = []) {
  const byId = new Map();
  for (const unit of units) {
    const seen = new Set();
    for (const topic of unit?.topics || []) {
      const key = String(topic.id ?? topic.uid);
      if (seen.has(key)) continue;
      seen.add(key);
      const entry = byId.get(key) || { topic, count: 0 };
      entry.count += 1;
      byId.set(key, entry);
    }
  }
  const list = [...byId.values()].sort((a, b) => String(a.topic.title || "").localeCompare(String(b.topic.title || "")));
  return {
    all: list.filter((entry) => entry.count === units.length).map((entry) => entry.topic),
    some: list.filter((entry) => entry.count < units.length),
  };
}

const refOf = (unit) => unit?.uid || unit?.id;

/**
 * The POST /api/units/batch body for one field edit (dates: "" clears;
 * assignee: "" unassigns).
 */
export function batchFieldPayload(units, field, value) {
  if (!BATCH_FIELDS.includes(field)) throw new Error(`not a batch field: ${field}`);
  const empty = field === "assignee" || field === "start_at" || field === "end_at";
  return { units: units.map(refOf), patch: { [field]: empty && !value ? null : value } };
}

/** The POST /api/units/batch body for adding / removing tags on all units. */
export function batchTagsPayload(units, { add = [], remove = [] } = {}) {
  const clean = (list) => [...new Set(list.map((tag) => String(tag || "").trim()).filter((tag) => tag && !SYSTEM_TAG.test(tag)))];
  return { units: units.map(refOf), tags_add: clean(add), tags_remove: clean(remove) };
}

/**
 * The per-unit `updates` body that puts back what `before` (the units as
 * they were) had for the fields a batch changed.  A status change can also
 * set end_at (done), so that is restored with it.
 */
export function undoPayload(before, { field = null, tags = false } = {}) {
  const updates = [];
  for (const unit of before) {
    const patch = {};
    if (field) {
      patch[field] = unit[field] ?? null;
      if (field === "status") patch.end_at = unit.end_at ?? null;
    }
    if (tags) patch.tags = ownTags(unit);
    updates.push({ unit: refOf(unit), patch });
  }
  return { updates };
}

/** The rows of a batch reply that a later undo needs (only the changed ones). */
export function changedBefore(before, reply) {
  const changed = new Set((reply?.updated || []).map((unit) => String(unit.id)));
  return before.filter((unit) => changed.has(String(unit.id)));
}

/**
 * Whether the selected node ids share one parent in the mind tree
 * (`parentOf`: Map child id -> parent id; top-level nodes have none).
 * @returns {{ ok: true, parent: any } | { ok: false, reason: "too_few" | "not_siblings" }}
 */
export function siblingGroup(ids = [], parentOf = new Map()) {
  const list = [...new Set(ids)];
  if (list.length < 2) return { ok: false, reason: "too_few" };
  const parentKey = (id) => (parentOf.has(id) ? parentOf.get(id) : null);
  const parent = parentKey(list[0]);
  for (const id of list) {
    if (parentKey(id) !== parent) return { ok: false, reason: "not_siblings" };
  }
  if (parent !== null && list.includes(parent)) return { ok: false, reason: "not_siblings" };
  return { ok: true, parent };
}

const seqOf = (unit) => {
  const tag = (unit?.tags || []).find((item) => String(item).startsWith("seq:"));
  const value = tag ? parseFloat(tag.split(":").slice(1).join(":")) : NaN;
  return Number.isFinite(value) ? value : null;
};

/**
 * Type and order of the new parent: the selection's type when they all
 * share one, else the type a new child of the parent gets (the parent's
 * own type, as Tab does), else "note"; it takes the place (seq) of the
 * first selected sibling.
 */
export function groupDefaults(nodes = [], parent = null) {
  const types = new Set(nodes.map((node) => node?.type || "note"));
  const type = types.size === 1 ? [...types][0] : (parent?.type || "note");
  const seqs = nodes.map(seqOf).filter((value) => value !== null);
  const tags = seqs.length ? [`seq:${Math.min(...seqs)}`] : [];
  return { type: type === "topic" ? "note" : type, tags };
}
