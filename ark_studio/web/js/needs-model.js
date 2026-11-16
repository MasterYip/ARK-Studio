// Pure helpers for the Home "Needs you" panel (dependency-free; see
// tests/js/needs_model.test.mjs).  The server decides what is in the list
// (`needs_you` in the project payload); this module only shapes it for the UI.

/** Number of items waiting on the user. */
// Values of the server-owned ``needs:`` tag, in lifecycle order (pending
// first, then outcomes).  Mirrors arkstudio/tags.py NEEDS_VALUES.
export const NEEDS_VALUES = ["review", "decision", "accepted", "sent-back", "decided", "dismissed"];

/** Every ``needs:`` tag, for "View history" (OR inside the namespace). */
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
