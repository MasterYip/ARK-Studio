// Pure helpers shared by the inspector and the Editor's node tabs: the
// editable draft of a unit, validation and the PATCH /api/units/{id} body.
// DOM-free and dependency-free (tests/js/node_model.test.mjs).

export const MAX_TITLE = 240;     // mirrors arkstudio/store.py
export const MAX_BODY = 200_000;
export const BODY_MODES = ["source", "split", "preview"];
const RESERVED_TAG = /^(type|status|importance):/;

export function freeTags(tags) {
  return [...new Set((Array.isArray(tags) ? tags : []).map(String).filter((tag) => tag && !RESERVED_TAG.test(tag)))];
}

export function draftFromUnit(unit = {}) {
  return {
    title: String(unit.title ?? ""),
    body: String(unit.body ?? ""),
    type: String(unit.type ?? ""),
    status: String(unit.status ?? ""),
    importance: String(unit.importance ?? ""),
    tags: freeTags(unit.tags),
    start_at: unit.start_at || "",
    end_at: unit.end_at || "",
  };
}

export function draftChanged(a, b) {
  if (!a || !b) return Boolean(a) !== Boolean(b);
  for (const key of ["title", "body", "type", "status", "importance", "start_at", "end_at"]) {
    if (String(a[key] ?? "") !== String(b[key] ?? "")) return true;
  }
  const tagsA = freeTags(a.tags);
  const tagsB = freeTags(b.tags);
  return tagsA.length !== tagsB.length || tagsA.some((tag, index) => tag !== tagsB[index]);
}

/** Field names that differ between two drafts. */
export function changedFields(a, b) {
  const out = [];
  for (const key of ["title", "body", "type", "status", "importance", "start_at", "end_at"]) {
    if (String(a?.[key] ?? "") !== String(b?.[key] ?? "")) out.push(key);
  }
  if (draftChanged({ ...a, title: "", body: "", type: "", status: "", importance: "", start_at: "", end_at: "" },
    { ...b, title: "", body: "", type: "", status: "", importance: "", start_at: "", end_at: "" })) out.push("tags");
  return out;
}

/** Client-side checks; the server repeats them.  Returns [{ field, key }]. */
export function validateDraft(draft) {
  const errors = [];
  const title = String(draft?.title ?? "").trim();
  if (!title) errors.push({ field: "title", key: "node.title_required" });
  else if (title.length > MAX_TITLE) errors.push({ field: "title", key: "node.title_too_long" });
  if (String(draft?.body ?? "").length > MAX_BODY) errors.push({ field: "body", key: "node.body_too_long" });
  if (draft?.start_at && draft?.end_at && draft.end_at < draft.start_at) errors.push({ field: "end_at", key: "node.end_before_start" });
  return errors;
}

/** Body for PATCH /api/units/{id} (same shape the inspector always sent). */
export function patchFromDraft(draft) {
  return {
    title: String(draft.title ?? "").trim(),
    body: String(draft.body ?? ""),
    type: draft.type,
    status: draft.status,
    importance: draft.importance,
    tags: freeTags(draft.tags),
    start_at: draft.start_at || null,
    end_at: draft.end_at || null,
  };
}

export function addTag(tags, value) {
  const tag = String(value ?? "").trim().replace(/,+$/, "");
  const list = freeTags(tags);
  if (!tag || RESERVED_TAG.test(tag) || list.includes(tag)) return list;
  return [...list, tag];
}

export function normalizeMode(mode, { phone = false } = {}) {
  if (BODY_MODES.includes(mode)) return mode === "split" && phone ? "preview" : mode;
  return phone ? "preview" : "split";
}

/** Outgoing + incoming links as uniform rows. */
export function linkRows(unit = {}) {
  const rows = [];
  for (const link of unit.links_out || []) {
    const other = link.target || {};
    rows.push({ id: link.id, relation: link.relation || "unset", direction: "out", uid: other.uid, title: other.title || "", type: other.type || "", status: other.status || "" });
  }
  for (const link of unit.links_in || []) {
    const other = link.source || {};
    rows.push({ id: link.id, relation: link.relation || "unset", direction: "in", uid: other.uid, title: other.title || "", type: other.type || "", status: other.status || "" });
  }
  return rows.filter((row) => row.uid);
}

/** Markdown that embeds an attachment in a body (media:N resolves to /api/media/N/raw). */
export function mediaMarkdown(item = {}) {
  const label = String(item.caption || item.kind || `attachment ${item.id}`).replace(/[[\]\n]/g, " ").trim();
  const kind = String(item.kind || "").toLowerCase();
  const embed = item.is_image || kind === "image" || kind === "video" || kind === "audio";
  return `${embed ? "!" : ""}[${label}](media:${Number(item.id) || 0})`;
}

/** Map of media id -> kind used by the renderer to pick <img>/<video>/<audio>. */
export function mediaKinds(media = []) {
  const kinds = new Map();
  for (const item of media || []) {
    const kind = item.is_image ? "image" : String(item.kind || "").toLowerCase();
    if (item.id) kinds.set(Number(item.id), kind);
  }
  return kinds;
}

/** Gallery card kind for an attachment. */
export function galleryKind(item = {}) {
  const url = String(item.url || "").split(/[?#]/)[0].toLowerCase();
  const ext = (/\.([a-z0-9]+)$/.exec(url) || [])[1] || "";
  const kind = String(item.kind || "").toLowerCase();
  if (item.is_image || kind === "image" || ["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif"].includes(ext)) return "image";
  if (kind === "video" || ["mp4", "webm", "mov", "m4v", "ogv"].includes(ext)) return "video";
  if (kind === "audio" || ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"].includes(ext)) return "audio";
  if (kind === "pdf" || ext === "pdf") return "pdf";
  if (["html", "htm"].includes(ext)) return "html";
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(item.url || "") && !/^file:/i.test(item.url || "")) return "link";
  return "file";
}

/**
 * What the Gantt draws for a unit, or null when it has no date.
 * `{ start, end, point, ongoing }` with YYYY-MM-DD strings.  An open unit
 * that has started but has no end_at (the default since start_at is set on
 * creation) runs to `today` as an ongoing bar; a planned start in the
 * future, or a done unit without end_at, is a point.
 */
export function ganttSpan(unit = {}, today = "") {
  const startAt = unit.start_at ? String(unit.start_at).slice(0, 10) : "";
  const endAt = unit.end_at ? String(unit.end_at).slice(0, 10) : "";
  const start = startAt || endAt;
  if (!start) return null;
  const open = !["done", "abandoned", "archived"].includes(unit.status);
  if (startAt && !endAt && open && today && startAt <= today) {
    return { start, end: String(today).slice(0, 10), point: false, ongoing: true };
  }
  const end = endAt || start;
  return { start, end, point: !startAt || !endAt || start === end, ongoing: false };
}
