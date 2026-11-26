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

/** Statuses that end a unit (server: store.ClosedStatuses); every other status is open. */
export const CLOSED_STATUSES = ["done", "abandoned", "archived"];

/** A decision still waiting for an answer (server: store.IsOpenDecision). */
export function isOpenDecision(unit = {}) {
  return unit?.type === "decision" && !CLOSED_STATUSES.includes(unit.status);
}

/** Whether a unit belongs in Needs you: in review or an open decision, not in the recycle bin. */
export function inNeedsYou(unit = {}) {
  return !!unit && !unit.archived && (unit.status === "review" || isOpenDecision(unit));
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
// "branch": the item's lines (``branches``, server: store/branches.go); an
// item on two lines counts in both, "" = no line.
export const NEEDS_FILTER_GROUPS = ["type", "kind", "assignee", "branch"];

/** The value an item has in a filter group ("" = unassigned / no line; the first line for "branch"). */
export function needsFacetValue(item = {}, group) {
  return needsFacetValues(item, group)[0];
}

/** Every value an item has in a filter group (several lines for "branch"). */
export function needsFacetValues(item = {}, group) {
  if (group === "kind") return [needsKind(item)];
  if (group === "assignee") return [String(item.assignee_effective || "")];
  if (group === "branch") {
    const lines = itemBranches(item);
    return lines.length ? lines : [""];
  }
  return [String(item.type || "")];
}

/** `{group: [{value, n}]}` for the current items; kinds in lifecycle order, others by count. */
export function needsFacets(items = []) {
  const out = {};
  for (const group of NEEDS_FILTER_GROUPS) {
    const counts = new Map();
    for (const item of items) {
      for (const value of needsFacetValues(item, group)) counts.set(value, (counts.get(value) || 0) + 1);
    }
    const order = group === "kind" ? ["review", "decision"] : [];
    out[group] = [...counts].map(([value, n]) => ({ value, n })).sort((a, b) => {
      if (order.length) return order.indexOf(a.value) - order.indexOf(b.value);
      if ((group === "assignee" || group === "branch") && (!a.value || !b.value)) return a.value ? -1 : 1; // unassigned / no line last
      return b.n - a.n || a.value.localeCompare(b.value);
    });
    // Without any line the group says nothing: leave it out of the bar.
    if (group === "branch") out[group] = out[group].every((item) => !item.value) ? [] : branchFacetTree(items);
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

/** The items the filter keeps: in each group at least one of the item's values is shown. */
export function filterNeeds(items = [], filter = {}) {
  const excluded = normalizeNeedsFilter(filter);
  return items.filter((item) => NEEDS_FILTER_GROUPS.every(
    (group) => needsFacetValues(item, group).some((value) => !excluded[group].includes(value))));
}

/**
 * Show or hide one value.  A line (group "branch") takes the lines below
 * it along (`values`: the values the bar lists).
 */
export function toggleNeedsValue(filter, group, value, values = []) {
  const next = normalizeNeedsFilter(filter);
  const list = next[group];
  const affected = group === "branch" && value
    ? [value, ...values.map(String).filter((v) => v.startsWith(`${value}/`))] : [value];
  next[group] = list.includes(value) ? list.filter((item) => !affected.includes(item))
    : [...new Set([...list, ...affected])];
  return next;
}

/** "Only this" on a line keeps the lines below it shown too. */
export function keepLineChildren(filter, value) {
  if (!value) return filter;
  return { ...filter, branch: (filter.branch || []).filter((v) => !String(v).startsWith(`${value}/`)) };
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
  // The server groups the items (store/needsgroups.go, "auto_group" on each
  // review item) so the web and the Android app show the same cards; the
  // rules below only run for an older server that does not send it.
  if (list.some((item) => item && Object.prototype.hasOwnProperty.call(item, "auto_group"))) return serverGroups(list);
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

/**
 * Cards from the server's `auto_group` (same shape as groupNeeds).  Only the
 * members present in `list` (after the filters) count; a card with fewer
 * than two left shows its items on their own.
 */
function serverGroups(list) {
  const byKey = new Map();
  list.forEach((item, index) => {
    const card = item?.auto_group;
    if (!card?.key || !item.uid || item.kind === "review_group") return;
    if (!byKey.has(card.key)) byKey.set(card.key, { card, first: index, items: new Map() });
    byKey.get(card.key).items.set(item.uid, item);
  });
  const groupAt = new Map();
  const grouped = new Set();
  for (const { card, first, items } of byKey.values()) {
    if (items.size < 2) continue;
    const order = (card.members || []).filter((uid) => items.has(uid));
    for (const uid of items.keys()) if (!order.includes(uid)) order.push(uid);
    const members = order.map((uid) => items.get(uid));
    const present = new Set(order);
    groupAt.set(first, {
      kind: "group", key: card.key, primary: members[0], members,
      parent: card.parent || null, delivery: card.delivery || "",
      title: card.title || members[0].title || members[0].uid,
      span: Number(card.span) || 0,
      reasons: (card.reasons || []).filter((r) => present.has(r.a) && present.has(r.b)),
    });
    for (const item of members) grouped.add(item);
  }
  const out = [];
  list.forEach((item, index) => {
    if (groupAt.has(index)) out.push(groupAt.get(index));
    else if (!grouped.has(item)) out.push({ kind: "item", item });
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

// ------------------------------------------------------------- seen marks
// The project picker's "new for review" badges (server: store/needscards.go,
// POST /api/needs/seen) count cards the person has not looked at yet.

/** The seen-mark key of a Needs you item: "rg:RG-3" for a review group, "u:<uid>" else. */
export function needsSeenKey(item = {}) {
  if (item.kind === "review_group") return item.id || item.uid ? `rg:${item.id || item.uid}` : "";
  return item.uid ? `u:${item.uid}` : "";
}

/**
 * POST /api/needs/seen body for the first `shown` cards of `entries`
 * (groupNeeds output): their keys, and `all` (the list watermark) only when
 * every card was on screen and no filter hid any.  null: nothing to send.
 */
export function needsSeenBody(entries = [], shown = entries.length, { filtered = false, max = 200 } = {}) {
  const keys = [];
  for (const entry of (entries || []).slice(0, Math.max(0, shown))) {
    const items = entry?.kind === "group" ? entry.members || [] : [entry?.item];
    for (const item of items) {
      const key = needsSeenKey(item || {});
      if (key && !keys.includes(key)) keys.push(key);
    }
  }
  const all = !filtered && shown >= (entries || []).length;
  if (!all && !keys.length) return null;
  return all ? { all: true, items: keys.slice(0, max) } : { items: keys.slice(0, max) };
}

/**
 * A unit the inspector opened counts as seen while it waits in Needs you,
 * or while it is one of the units assigned to the person (`assigned`, the
 * assignedEntries list; the server reads its assignment notice too).
 */
export function needsUnitKey(unit = {}, assigned = []) {
  return unit?.uid && (inNeedsYou(unit) || isAssigned(assigned, unit.uid)) ? `u:${unit.uid}` : "";
}

// ------------------------------------------------------- assigned to me
// Units someone else assigned to the person (GET /api/needs/assigned,
// server: store/assignments.go), one entry per batch of assignments.  They
// count as read once the person opened the unit or cleared the entry
// (POST /api/needs/seen {clear}); showing the list does not read them.

/**
 * The entries to show: units already in Needs you (in review, an open
 * decision) are left out, so each unit appears once; entries left empty go.
 */
export function assignedEntries(assigned = [], needsYou = []) {
  const shown = new Set((Array.isArray(needsYou) ? needsYou : []).map((item) => item?.uid).filter(Boolean));
  const out = [];
  for (const entry of Array.isArray(assigned) ? assigned : []) {
    const units = (entry?.units || []).filter((unit) => unit?.uid && !shown.has(unit.uid));
    if (!units.length) continue;
    out.push({ ...entry, units, new: units.some((unit) => unit.new) });
  }
  return out;
}

/** Number of assigned units (for the Home badge). */
export function assignedCount(entries = []) {
  return (entries || []).reduce((n, entry) => n + (entry?.units?.length || 0), 0);
}

/** The seen keys an entry's Clear sends ("a:<uid>" per unit). */
export function assignedClearKeys(entry = {}) {
  return (entry.units || []).map((unit) => unit.key || (unit.uid ? `a:${unit.uid}` : "")).filter(Boolean);
}

/** The entries with `uid` read (after the person opened it); unchanged when it is not there. */
export function markAssignedRead(entries = [], uid = "") {
  if (!uid) return entries;
  return (entries || []).map((entry) => {
    if (!(entry.units || []).some((unit) => unit.uid === uid && unit.new)) return entry;
    const units = entry.units.map((unit) => (unit.uid === uid ? { ...unit, new: false } : unit));
    return { ...entry, units, new: units.some((unit) => unit.new) };
  });
}

/** The entries without the cleared one (by batch). */
export function dropAssigned(entries = [], batch = "") {
  return (entries || []).filter((entry) => entry.batch !== batch);
}

/** Whether uid is one of the assigned units. */
export function isAssigned(entries = [], uid = "") {
  return Boolean(uid) && (entries || []).some((entry) => (entry.units || []).some((unit) => unit.uid === uid));
}

// ------------------------------------------------------------ lines
// Lines of work are tags "branch:<name>" (server: store/branches.go; the
// namespace is a project setting).  Needs you items carry their line names
// as ``branches``; GET /api/project/branches describes each line.

/** The line names of an item, once each, in the server's order. */
export function itemBranches(item = {}) {
  return [...new Set((Array.isArray(item?.branches) ? item.branches : []).filter((b) => typeof b === "string" && b))];
}

/** A line's status: "stalled" | "done" | "active" | "quiet". */
export function branchStatus(info = {}) {
  return ["stalled", "done", "active", "quiet"].includes(info?.status) ? info.status : "active";
}

/**
 * Needs you split by line: `[{ key, name, info, items, extra }]`.  Each item
 * shows once, under its first line in section order (`extra[uid]` lists its
 * other lines for a "+1" chip), so counts and bulk actions see it once.
 * Sections: stalled lines first, then by the newest waiting item; items
 * without a line last (key "", name "").  `infos` is the items list of
 * GET /api/project/branches (may be empty while it loads).
 */
export function groupNeedsByBranch(items = [], infos = []) {
  const byName = new Map((Array.isArray(infos) ? infos : []).filter((b) => b?.name).map((b) => [b.name, b]));
  const newest = new Map();
  for (const item of items) {
    const at = String(item?.review_since || item?.updated_at || "");
    for (const name of itemBranches(item)) if (!newest.has(name) || at > newest.get(name)) newest.set(name, at);
  }
  const rank = (name) => (byName.get(name)?.status === "stalled" ? 0 : 1);
  const order = [...newest.keys()].sort((a, b) => rank(a) - rank(b)
    || String(newest.get(b)).localeCompare(String(newest.get(a))) || a.localeCompare(b));
  const position = new Map(order.map((name, i) => [name, i]));
  const sections = new Map(order.map((name) => [name, { key: name, name, info: byName.get(name) || null, items: [], extra: {} }]));
  const loose = { key: "", name: "", info: null, items: [], extra: {} };
  for (const item of items) {
    const lines = itemBranches(item).sort((a, b) => position.get(a) - position.get(b));
    if (!lines.length) { loose.items.push(item); continue; }
    const section = sections.get(lines[0]);
    section.items.push(item);
    if (lines.length > 1 && item?.uid) section.extra[item.uid] = lines.slice(1);
  }
  const out = order.map((name) => sections.get(name)).filter((section) => section.items.length);
  if (loose.items.length) out.push(loose);
  return out;
}

/** localStorage key of the Needs you layout, per person and project. */
export function needsLayoutKey(user = "", project = "") {
  return `ark.needsLayout:${user || "-"}:${project || "local"}`;
}

/** The stored layout: "flat" when chosen, else "branch" (grouped by line). */
export function needsLayout(stored) {
  return stored === "flat" ? "flat" : "branch";
}

/**
 * The lines for the Topics bar: those without a stored topic first ("auto",
 * a virtual topic page), then the ones a topic already shows; newest first.
 */
export function branchTopicEntries(infos = []) {
  return (Array.isArray(infos) ? infos : []).filter((b) => b?.name).map((b) => ({
    name: b.name, tag: b.tag, ref: b.ref || b.tag, auto: !b.topic, topic: b.topic || null,
    // "rule": the topic's rule is the tag (adding a node tags it); "tag": a
    // topic that carries the tag and keeps its own members.
    via: b.topic ? b.topic.via || "rule" : "",
    units: Number(b.units) || 0, pending: Number(b.pending) || 0, status: branchStatus(b),
    last: b.last_activity || "",
  })).sort((a, b) => Number(b.auto) - Number(a.auto) || String(b.last).localeCompare(String(a.last)) || a.name.localeCompare(b.name));
}

/** Whether a topic ref is a line without a stored topic ("branch:x"; unit ids never hold ":"). */
export function isBranchRef(ref) {
  return /^[^:\s]+:\S/.test(String(ref || ""));
}

// ------------------------------------------------------- line paths
// A line name is a path ("hoi/pilot/stairs", 主线/分线/分分线): lines nest.

/** "hoi/pilot/stairs" -> "hoi › pilot › stairs". */
export function lineLabel(name = "") {
  return String(name || "").split("/").join(" › ");
}

/** The lines above a path, top first: "a/b/c" -> ["a", "a/b"]. */
export function lineAncestors(name = "") {
  const parts = String(name || "").split("/");
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join("/"));
}

/** A line path is non-empty names separated by "/". */
export function validLinePath(name = "") {
  const text = String(name || "");
  return Boolean(text) && text.split("/").every((part) => part.trim() !== "") && !/\s/.test(text);
}

/**
 * The line filter's values as a tree: every line and the lines above it,
 * counts rolled up (`exact`: items on that line itself), "" last.
 */
export function branchFacetTree(items = []) {
  const counts = new Map();
  const exact = new Map();
  let loose = 0;
  for (const item of items) {
    const lines = itemBranches(item);
    if (!lines.length) { loose++; continue; }
    const paths = new Set(lines.flatMap((line) => [...lineAncestors(line), line]));
    for (const path of paths) counts.set(path, (counts.get(path) || 0) + 1);
    for (const line of new Set(lines)) exact.set(line, (exact.get(line) || 0) + 1);
  }
  const out = [...counts].map(([value, n]) => ({ value, n, depth: value.split("/").length - 1, exact: exact.get(value) || 0 }))
    .sort((a, b) => a.value.localeCompare(b.value));
  if (loose) out.push({ value: "", n: loose, depth: 0, exact: loose });
  return out;
}

const STATUS_RANK = { stalled: 0, active: 1, quiet: 2, done: 3 };

/**
 * Needs you as a tree of lines: `{ nodes, loose }`.  Each node is
 * `{ key: path, name: path, label: last part, depth, info, items (its own,
 * as groupNeedsByBranch places them), extra, children, total, last, status }`;
 * total, last activity and status roll up from the lines below (a parent is
 * stalled when any line below it is).  A line nobody tagged directly
 * (only its children) still gets a node.  `loose` is the "No line" section
 * or null.
 */
export function groupNeedsTree(items = [], infos = []) {
  const sections = groupNeedsByBranch(items, infos);
  const byName = new Map((Array.isArray(infos) ? infos : []).filter((b) => b?.name).map((b) => [b.name, b]));
  const nodes = new Map();
  const node = (path) => {
    if (nodes.has(path)) return nodes.get(path);
    const n = { key: path, name: path, label: path.split("/").at(-1), depth: path.split("/").length - 1,
      info: byName.get(path) || null, items: [], extra: {}, children: [], total: 0, last: "", status: "" };
    nodes.set(path, n);
    const parent = lineAncestors(path).at(-1);
    if (parent) node(parent).children.push(n);
    return n;
  };
  let loose = null;
  for (const section of sections) {
    if (!section.key) { loose = section; continue; }
    const n = node(section.key);
    n.items = section.items;
    n.extra = section.extra;
  }
  const itemAt = (item) => String(item?.review_since || item?.updated_at || "");
  const roll = (n) => {
    n.children.forEach(roll);
    n.total = n.items.length + n.children.reduce((sum, c) => sum + c.total, 0);
    const times = [n.info?.last_activity || "", ...n.items.map(itemAt), ...n.children.map((c) => c.last)];
    n.last = times.reduce((a, b) => (b > a ? b : a), "");
    const own = n.info?.status || (n.items.length ? "active" : "");
    const all = [own, ...n.children.map((c) => c.status)].filter(Boolean);
    n.status = all.includes("stalled") ? "stalled" : all.length && all.every((x) => x === "done") ? "done"
      : all.includes("active") ? "active" : all[0] || "active";
    n.children.sort(order);
  };
  const order = (a, b) => (STATUS_RANK[a.status] ?? 1) - (STATUS_RANK[b.status] ?? 1)
    || String(b.last).localeCompare(String(a.last)) || a.name.localeCompare(b.name);
  const roots = [...nodes.values()].filter((n) => n.depth === 0 || !nodes.has(lineAncestors(n.name).at(-1)));
  roots.forEach(roll);
  roots.sort(order);
  return { nodes: roots, loose };
}

/** Every item of a node and the lines below it (for "select this group"). */
export function treeItems(node) {
  return node ? [...node.items, ...node.children.flatMap(treeItems)] : [];
}

// ------------------------------------------------------- line tree folding
// The Needs you / Review line tree folds like the Editor outline (the same
// configurable keys: shortcuts.js FOLD_ACTIONS).  Fold state is a set of
// line paths.

/** Every foldable line of a tree (it has items or lines below it), with its depth (0 = top). */
export function treeFoldPaths(tree = {}) {
  const out = [];
  const walk = (node) => {
    if (!node?.key) return;
    if ((node.items || []).length || (node.children || []).length) out.push({ path: node.key, depth: node.depth || 0 });
    (node.children || []).forEach(walk);
  };
  (tree.nodes || []).forEach(walk);
  return out;
}

/** The fold command of a shortcut id (FOLD_ACTIONS), or null. */
export function foldCommandOf(id = "") {
  if (id === "editorFoldAll") return { cmd: "foldAll" };
  if (id === "editorUnfoldAll") return { cmd: "unfoldAll" };
  if (id === "editorFold") return { cmd: "fold" };
  if (id === "editorUnfold") return { cmd: "unfold" };
  const level = /^editorFoldLevel(\d)$/.exec(id)?.[1];
  return level ? { cmd: "foldLevel", level: Number(level) } : null;
}

/**
 * The folded paths after a command: foldAll / unfoldAll; foldLevel N (fold
 * the lines at level N, 1 = top, as the Editor's Fold Level N); showLevel N
 * (toolbar L1-L3: levels above N open, N and deeper folded); fold / unfold /
 * toggle `focus` (a path).  Paths that are not in the tree are dropped.
 */
export function foldTree(folded = new Set(), tree = {}, command = {}) {
  const paths = treeFoldPaths(tree);
  const known = new Set(paths.map((p) => p.path));
  let next = new Set([...folded].filter((p) => known.has(p)));
  const focus = command.focus || "";
  switch (command.cmd) {
    case "foldAll": next = new Set(known); break;
    case "unfoldAll": next = new Set(); break;
    case "foldLevel": for (const p of paths) if (p.depth === command.level - 1) next.add(p.path); break;
    case "showLevel": next = new Set(paths.filter((p) => p.depth >= command.level - 1).map((p) => p.path)); break;
    case "fold": if (known.has(focus)) next.add(focus); break;
    case "unfold": next.delete(focus); break;
    case "toggle": if (next.has(focus)) next.delete(focus); else if (known.has(focus)) next.add(focus); break;
    default: break;
  }
  return next;
}

/** The deepest level of a tree (1 = only top lines). */
export function treeDepth(tree = {}) {
  return treeFoldPaths(tree).reduce((max, p) => Math.max(max, p.depth + 1), 0);
}

// ------------------------------------------------------------ unseen
// An item is seen once the person opened it in the node details (the user,
// 2026-11-26: "看过：在属性栏点开过"; server: store.OpenedPrefix).  A new
// review round makes it unseen again.  `fresh` is the set of seen-mark keys
// the server says are not opened (GET /api/needs/new, POST /api/needs/seen
// new_keys); null while unknown, which counts everything as new.

/** Whether an item has not been opened yet. */
export function itemUnseen(item = {}, fresh = null) {
  if (!fresh) return true;
  const key = needsSeenKey(item);
  return Boolean(key) && fresh.has(key);
}

/** How many of `items` are unseen. */
export function unseenCount(items = [], fresh = null) {
  return (items || []).filter((item) => itemUnseen(item, fresh)).length;
}

/**
 * A count bubble: `{ text, unseen, state }` with state "new" (orange, at
 * least one item not opened) or "seen" (grey).  `label(n)` formats "N new"
 * when only some are new: "3 · 1 新".
 */
export function countBubble(total = 0, unseen = 0, label = (n) => `${n} new`) {
  if (!total) return { text: "0", unseen: 0, state: "seen" };
  if (!unseen) return { text: String(total), unseen: 0, state: "seen" };
  return { text: unseen < total ? `${total} · ${label(unseen)}` : String(total), unseen, state: "new" };
}

/** The seen-mark keys a card stands for: the item, or a group and its members. */
export function cardKeys(item = {}) {
  const keys = [needsSeenKey(item)];
  for (const m of item.members || []) if (m?.uid) keys.push(`u:${m.uid}`);
  return [...new Set(keys.filter(Boolean))];
}

/** Whether a card shows the unseen dot: any of its keys not opened. */
export function cardUnseen(keys = [], fresh = null) {
  if (!fresh) return false; // unknown: no dot rather than a dot on everything
  return keys.some((key) => fresh.has(key));
}

/** Opening a unit: the keys to mark opened (the unit, and the review groups it belongs to). */
export function openedKeys(unit = {}, needsYou = []) {
  if (!unit?.uid || !inNeedsYou(unit)) return [];
  const keys = [`u:${unit.uid}`];
  for (const item of needsYou || []) {
    if (item?.kind === "review_group" && (item.members || []).some((m) => m?.uid === unit.uid)) keys.push(needsSeenKey(item));
  }
  return keys.filter(Boolean);
}
