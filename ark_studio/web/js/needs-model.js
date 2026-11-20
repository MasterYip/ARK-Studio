// Pure helpers for the Home "Needs you" panel (dependency-free; see
// tests/js/needs_model.test.mjs).  The server decides what is in the list
// (`needs_you` in the project payload); this module only shapes it for the UI.

/** Number of items waiting on the user. */
// Values of the server-owned ``needs:`` tag, in lifecycle order (pending
// first, then outcomes).  Mirrors arkstudio/tags.py NEEDS_VALUES.
export const NEEDS_VALUES = ["review", "decision", "accepted", "sent-back", "decided", "dismissed"];

/** Every ``needs:`` tag, for the Map filter "open in Map" (OR inside the namespace). */
export function needsHistoryTags() {
  return NEEDS_VALUES.map((value) => `needs:${value}`);
}

/** Sort ``needs`` facet values by lifecycle order. */
export function sortNeedsValues(values = []) {
  const rank = (v) => { const i = NEEDS_VALUES.indexOf(v.value); return i < 0 ? 99 : i; };
  return [...values].sort((a, b) => rank(a) - rank(b) || String(a.value).localeCompare(String(b.value)));
}

export function needsCount(items) {
  return Array.isArray(items) ? items.length : 0;
}

/** "review" | "decision" with a safe fallback for older payloads. */
export function needsKind(item = {}) {
  if (item.kind === "decision" || item.kind === "review") return item.kind;
  return item.type === "decision" ? "decision" : "review";
}

/**
 * Linked units that previews were borrowed from (``media[].from``), in order,
 * once each.  Empty when every preview is the node's own.
 */
export function borrowedSources(media) {
  const out = [];
  const seen = new Set();
  for (const item of Array.isArray(media) ? media : []) {
    const uid = item?.from?.uid;
    if (!uid || seen.has(uid)) continue;
    seen.add(uid);
    out.push(item.from);
  }
  return out;
}

/**
 * List items under an "Options" heading of a decision body, for quick picks.
 * Accepts `## Options`, `**Options**` or `Options:` headings and -, *, + or 1.
 * bullets; stops at the next heading.
 */
export function decisionOptions(body) {
  const lines = String(body || "").replace(/\r\n/g, "\n").split("\n");
  const heading = /^\s*(#{1,6}\s*options?\b|\*\*options?\*\*|options?\s*:)/i;
  const nextHeading = /^\s*(#{1,6}\s|\*\*[^*]+\*\*\s*:?\s*$)/;
  const out = [];
  let inside = false;
  for (const line of lines) {
    if (heading.test(line)) { inside = true; continue; }
    if (!inside) continue;
    if (nextHeading.test(line)) break;
    const match = /^\s*(?:[-*+]|\d+[.)])\s+(.+?)\s*$/.exec(line);
    if (match) out.push(match[1]);
  }
  return out.slice(0, 8);
}

// ------------------------------------------------------------- filters
// Category filters for the panel.  State stores *excluded* values per group,
// so a value that appears later (a new type, a new assignee) is shown.
export const NEEDS_FILTER_GROUPS = ["type", "kind", "assignee"];

/** The value an item has in a filter group ("" = unassigned). */
export function needsFacetValue(item = {}, group) {
  if (group === "kind") return needsKind(item);
  if (group === "assignee") return String(item.assignee_effective || "");
  return String(item.type || "");
}

/** `{group: [{value, n}]}` for the current items; kinds in lifecycle order, others by count. */
export function needsFacets(items = []) {
  const out = {};
  for (const group of NEEDS_FILTER_GROUPS) {
    const counts = new Map();
    for (const item of items) {
      const value = needsFacetValue(item, group);
      counts.set(value, (counts.get(value) || 0) + 1);
    }
    const order = group === "kind" ? ["review", "decision"] : [];
    out[group] = [...counts].map(([value, n]) => ({ value, n })).sort((a, b) => {
      if (order.length) return order.indexOf(a.value) - order.indexOf(b.value);
      if (group === "assignee" && (!a.value || !b.value)) return a.value ? -1 : 1; // unassigned last
      return b.n - a.n || a.value.localeCompare(b.value);
    });
  }
  return out;
}

export function normalizeNeedsFilter(raw) {
  const out = {};
  for (const group of NEEDS_FILTER_GROUPS) {
    const values = Array.isArray(raw?.[group]) ? raw[group] : [];
    out[group] = [...new Set(values.map((value) => String(value ?? "")))];
  }
  return out;
}

export function needsFilterActive(filter) {
  return NEEDS_FILTER_GROUPS.some((group) => (filter?.[group] || []).length > 0);
}

export function filterNeeds(items = [], filter = {}) {
  const excluded = normalizeNeedsFilter(filter);
  return items.filter((item) => NEEDS_FILTER_GROUPS.every(
    (group) => !excluded[group].includes(needsFacetValue(item, group))));
}

/** Show or hide one value. */
export function toggleNeedsValue(filter, group, value) {
  const next = normalizeNeedsFilter(filter);
  const list = next[group];
  next[group] = list.includes(value) ? list.filter((item) => item !== value) : [...list, value];
  return next;
}

/** The group's "All" switch: on shows every value, off hides every current value. */
export function setNeedsGroupAll(filter, group, on, values = []) {
  const next = normalizeNeedsFilter(filter);
  next[group] = on ? [] : [...new Set(values.map(String))];
  return next;
}

/**
 * A card's previews as items for the shared media lightbox
 * (image-viewer.js openMediaLightbox), in strip order: ``kind`` is the
 * preview kind, ``caption`` the thumbnail label plus "from S-0001" for a
 * preview borrowed from a linked node.  ``fromLabel(uid)`` localises that.
 */
export function needsLightboxItems(media, fromLabel = (uid) => `from ${uid}`) {
  return (Array.isArray(media) ? media : []).filter((m) => m && previewKind(m)).map((m) => {
    const label = m.caption || m.from?.title || "";
    const from = m.from?.uid ? fromLabel(m.from.uid) : "";
    return { ...m, kind: previewKind(m), caption: [label, from].filter(Boolean).join(" · ") };
  });
}

/** Thumbnail source kind for a preview: "image" | "video" | "". */
export function previewKind(media = {}) {
  if (media.preview_kind === "image" || media.preview_kind === "video") return media.preview_kind;
  const url = String(media.url || "").split(/[?#]/)[0].toLowerCase();
  const kind = String(media.kind || "").toLowerCase();
  if (media.is_image || kind === "image" || /\.(png|jpe?g|webp|gif|svg|avif|bmp)$/.test(url)) return "image";
  if (kind === "video" || /\.(mp4|webm|mov|m4v|ogv)$/.test(url)) return "video";
  return "";
}

/** What "Accept several" offers: reviews only (a decision needs an answer). */
export function bulkAcceptable(items = []) {
  return items.filter((item) => item?.uid && needsKind(item) === "review" && item.kind !== "review_group");
}

/** Items that are units (a review group is decided on its own card). */
export function unitItems(items = []) {
  return items.filter((item) => item?.kind !== "review_group");
}

/**
 * Runs `fn(item)` over `items`, at most `limit` at a time.  Resolves to
 * `{ done, failed }` in input order; one failure does not stop the others.
 */
export async function runBatch(items, fn, limit = 4) {
  const results = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const index = next++;
      try { await fn(items[index]); results[index] = { ok: true }; } catch (error) { results[index] = { ok: false, error }; }
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  const done = [], failed = [];
  results.forEach((r, i) => (r.ok ? done : failed).push(r.ok ? items[i] : { item: items[i], error: r.error }));
  return { done, failed };
}

// ------------------------------------------------------- review page
// Research › Review (views/review.js): every item that needs you, with a
// checkbox per row and actions on the selection.

/** Items assigned to `me` first; otherwise the server order (decisions, newest). */
export function sortMineFirst(items = [], me = "") {
  const who = String(me || "").toLowerCase();
  const mine = (item) => Boolean(who) && String(item?.assignee_effective || "").toLowerCase() === who;
  return [...items].sort((a, b) => mine(b) - mine(a));
}

/** The selection limited to uids still in `items` (a filter or a refresh can drop some). */
export function pruneSelection(selected, items = []) {
  const present = new Set(items.map((item) => item?.uid).filter(Boolean));
  return new Set([...(selected || [])].filter((uid) => present.has(uid)));
}

/** Select or clear every item in `items` (the filtered list), keeping the others as they are. */
export function selectAll(selected, items = [], on = true) {
  const next = new Set(selected || []);
  for (const item of items) {
    if (!item?.uid) continue;
    if (on) next.add(item.uid); else next.delete(item.uid);
  }
  return next;
}

/** `{ n, all, some }` for the select-all checkbox over `items`. */
export function selectionState(selected, items = []) {
  const uids = items.map((item) => item?.uid).filter(Boolean);
  const n = uids.filter((uid) => selected?.has(uid)).length;
  return { n, all: uids.length > 0 && n === uids.length, some: n > 0 && n < uids.length };
}

/** The selected items, in list order. */
export function selectedItems(selected, items = []) {
  return items.filter((item) => item?.uid && selected?.has(item.uid));
}

/**
 * Splits a selection for Accept / Send back: reviews take the action,
 * decisions are skipped (each needs its own answer).
 */
export function splitForReview(items = []) {
  const reviews = [], decisions = [];
  for (const item of items) (needsKind(item) === "decision" ? decisions : reviews).push(item);
  return { reviews, decisions };
}

// Outcome values of the ``needs:`` tag (no longer waiting on anyone).
export const NEEDS_OUTCOMES = ["accepted", "sent-back", "decided", "dismissed"];

/** Tags that list the units which needed you before (Review › History). */
export function needsOutcomeTags() {
  return NEEDS_OUTCOMES.map((value) => `needs:${value}`);
}

/** A unit's ``needs:`` value ("accepted", "review", ...) from its tags, or "". */
export function needsValueOf(tags = []) {
  const values = (Array.isArray(tags) ? tags : [])
    .map((tag) => (typeof tag === "string" ? tag : tag?.tag || tag?.name || ""))
    .filter((tag) => tag.startsWith("needs:"))
    .map((tag) => tag.slice(6));
  // A pending value wins over an outcome; otherwise the latest in lifecycle order.
  for (const value of ["review", "decision"]) if (values.includes(value)) return value;
  return [...values].sort((a, b) => NEEDS_VALUES.indexOf(b) - NEEDS_VALUES.indexOf(a))[0] || "";
}

// Routes of the Review page: #/research/review (waiting) and
// #/research/review/history.
export const REVIEW_VIEWS = ["waiting", "history"];

export function reviewView(value) {
  return value === "history" ? "history" : "waiting";
}

export function reviewHash(view) {
  return reviewView(view) === "history" ? "#/research/review/history" : "#/research/review";
}

// ------------------------------------------------------ review grouping
// Fallback for deliveries that put several units up for review one by one
// (agents should submit a review group instead, store/reviewgroups.go).
// Two review items belong to one card when
//   - their bodies name the same delivery (branch, or commit when no branch
//     is named) and they are linked, share a parent or entered review within
//     DELIVERY_SPAN_MS of each other, or
//   - neither delivery is known for both, they are linked to each other or
//     share a parent (part_of / derived_from the same unit), and they entered
//     review within GROUP_WINDOW_MS of each other.
// Two items naming different deliveries are never grouped.  Groups are the connected sets of
// such pairs, with at least two members; decisions and review groups stay
// on their own.  The rules only read the payload, so the result is the same
// on every render.
export const GROUP_WINDOW_MS = 30 * 60 * 1000;
// Units naming the same branch / commit belong together without a link
// when they entered review within this span.
export const DELIVERY_SPAN_MS = 24 * 60 * 60 * 1000;

// Primary member: the task the person asked for, else a result.
const PRIMARY_RANK = { task: 0, experiment: 1, artifact: 1, result: 1, run: 1, claim: 2, evidence: 3 };

function sinceMs(item) {
  const value = Date.parse(item?.review_since || item?.updated_at || "");
  return Number.isFinite(value) ? value : 0;
}

/** The delivery an item names: branch, else commit ("" = none). */
export function deliveryKey(item = {}) {
  const d = item.delivery;
  if (!d) return "";
  return d.branch ? `branch:${d.branch}` : d.commit ? `commit:${d.commit}` : "";
}

function parentUids(item) {
  return new Set((item.parents || []).map((p) => p?.uid).filter(Boolean));
}

/** Why a and b belong together (null = they do not). */
export function groupReason(a, b, windowMs = GROUP_WINDOW_MS) {
  const da = deliveryKey(a), db = deliveryKey(b);
  if (da && db && da !== db) return null;
  const sameDelivery = Boolean(da && da === db);
  const gap = Math.abs(sinceMs(a) - sinceMs(b));
  const minutes = Math.round(gap / 60000);
  const link = (a.links || []).find((l) => l?.uid === b.uid) || null;
  const pa = parentUids(a);
  const shared = [...parentUids(b)].filter((uid) => pa.has(uid)).sort();
  if (sameDelivery) {
    // The same branch / commit: one delivery even without a link, within a day.
    if (!link && !shared.length && gap > DELIVERY_SPAN_MS) return null;
  } else if (gap > windowMs || (!link && !shared.length)) return null;
  return {
    link: link ? { from: link.dir === "in" ? b.uid : a.uid, to: link.dir === "in" ? a.uid : b.uid, relation: link.relation } : null,
    parents: shared, delivery: sameDelivery ? da : "", minutes,
  };
}

function primaryOf(members) {
  const inGroup = new Set(members.map((m) => m.uid));
  const childOf = (m) => [...parentUids(m)].some((uid) => inGroup.has(uid)) ? 1 : 0;
  return [...members].sort((a, b) => (PRIMARY_RANK[a.type] ?? 4) - (PRIMARY_RANK[b.type] ?? 4)
    || childOf(a) - childOf(b) || sinceMs(a) - sinceMs(b) || String(a.uid).localeCompare(String(b.uid)))[0];
}

/**
 * Splits `items` (already sorted / filtered) into cards, in list order:
 * `{ kind: "item", item }` or `{ kind: "group", key, primary, members,
 * parent, delivery, title, span, reasons }` (members in list order, the
 * primary first).  `parent` is the unit every member is part of /
 * derived from (outside the group) when there is one.
 */
export function groupNeeds(items = [], { windowMs = GROUP_WINDOW_MS } = {}) {
  const list = Array.isArray(items) ? items : [];
  const candidates = list.map((item, index) => ({ item, index }))
    .filter(({ item }) => item?.uid && item.kind !== "review_group" && needsKind(item) === "review");
  const parent = new Map(candidates.map(({ index }) => [index, index]));
  const find = (i) => { while (parent.get(i) !== i) i = parent.get(i); return i; };
  const reasons = [];
  for (let x = 0; x < candidates.length; x++) {
    for (let y = x + 1; y < candidates.length; y++) {
      const reason = groupReason(candidates[x].item, candidates[y].item, windowMs);
      if (!reason) continue;
      reasons.push({ a: candidates[x].item.uid, b: candidates[y].item.uid, ...reason });
      const rx = find(candidates[x].index), ry = find(candidates[y].index);
      if (rx !== ry) parent.set(Math.max(rx, ry), Math.min(rx, ry));
    }
  }
  const byRoot = new Map();
  for (const { index } of candidates) {
    const root = find(index);
    if (!byRoot.has(root)) byRoot.set(root, []);
    byRoot.get(root).push(index);
  }
  const groupAt = new Map(); // first index -> group
  const grouped = new Set();
  for (const indexes of byRoot.values()) {
    if (indexes.length < 2) continue;
    const members = indexes.map((i) => list[i]);
    const uids = new Set(members.map((m) => m.uid));
    const primary = primaryOf(members);
    // The parent most members share (at least half of them), outside the group.
    const parentCount = new Map();
    const parentRefs = new Map();
    for (const m of members) {
      for (const p of m.parents || []) {
        if (!p?.uid || uids.has(p.uid) || parentRefs.get(p.uid)?.seen === m.uid) continue;
        parentCount.set(p.uid, (parentCount.get(p.uid) || 0) + 1);
        parentRefs.set(p.uid, { ref: p, seen: m.uid });
      }
    }
    const [parentUid = "", parentN = 0] = [...parentCount].sort((x, y) => y[1] - x[1] || x[0].localeCompare(y[0]))[0] || [];
    const parentRef = parentUid && parentN * 2 >= members.length ? parentRefs.get(parentUid).ref : null;
    // The delivery label most members name (at least half of them).
    const counts = new Map();
    for (const m of members) if (m.delivery?.label) counts.set(m.delivery.label, (counts.get(m.delivery.label) || 0) + 1);
    const [label = "", n = 0] = [...counts].sort((x, y) => y[1] - x[1] || x[0].localeCompare(y[0]))[0] || [];
    const titleLabel = n * 2 >= members.length ? label : "";
    const keys = new Set(members.map(deliveryKey));
    const times = members.map(sinceMs);
    const groupReasons = reasons.filter((r) => uids.has(r.a) && uids.has(r.b));
    groupAt.set(indexes[0], {
      kind: "group", key: `group:${[...uids].sort().join(",")}`, primary,
      members: [primary, ...members.filter((m) => m !== primary)],
      parent: parentRef || null, delivery: keys.size === 1 && !keys.has("") ? [...keys][0] : "",
      title: titleLabel || shortTitle(parentRef?.title) || primary.title || primary.uid,
      span: Math.round((Math.max(...times) - Math.min(...times)) / 60000),
      reasons: groupReasons,
    });
    indexes.forEach((i) => grouped.add(i));
  }
  const out = [];
  list.forEach((item, index) => {
    if (groupAt.has(index)) out.push(groupAt.get(index));
    else if (!grouped.has(index)) out.push({ kind: "item", item });
  });
  return out;
}

/** "HOI-215 · Large sandbox ..." -> "HOI-215" (the part before the first " · "), else the title. */
export function shortTitle(title) {
  const text = String(title || "").trim();
  if (!text) return "";
  const head = text.split(" · ")[0].trim();
  return head.length >= 2 && head.length < text.length ? head : text;
}

/** What a heuristic group's explanation line says: `{parents, link, delivery, minutes}`. */
export function groupExplain(group) {
  const parents = [...new Set((group?.reasons || []).flatMap((r) => r.parents))].sort();
  const linked = (group?.reasons || []).some((r) => r.link);
  return { parents, linked, delivery: group?.delivery || "", minutes: group?.span || 0 };
}
