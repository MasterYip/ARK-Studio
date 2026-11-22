// The Home prompt library without DOM (Home → More prompts): the built-in
// prompts (starter-prompts.js) and a person's and the project's custom
// prompts (GET /api/prompts), which of them are pinned on Home and in what
// order, and the JSON export / import of custom prompts.
import { BUILTIN_PROMPTS, DEFAULT_PINS, pickLine } from "./starter-prompts.js";

export const EXPORT_FORMAT = "ark-prompts";
export const MAX_PINS = 12;

/** One list: built-in prompts first, then the person's, then the project's. */
export function libraryEntries(state = {}) {
  const custom = (scope) => (state?.[scope] || []).map((p) => ({
    id: p.id, custom: true, scope, icon: "pen", title: p.title || "", desc: p.description || "", body: p.body || "",
    unit: /\{unit\}/.test(p.body || ""), version: p.version || "", updatedBy: p.updated_by || p.created_by || "",
  }));
  return [...BUILTIN_PROMPTS.map((p) => ({ ...p, builtin: true })), ...custom("personal"), ...custom("project")];
}

export function entryTitle(entry, lang) {
  return entry?.custom ? entry.title : pickLine(entry?.title || "", lang);
}

export function entryDesc(entry, lang) {
  return entry?.custom ? entry.desc : pickLine(entry?.desc || "", lang);
}

/** The pin list in force: the stored one, or the defaults (null = never chosen). */
export function effectivePins(pins) {
  return Array.isArray(pins) ? pins.slice() : DEFAULT_PINS.slice();
}

/** The pinned entries in pin order; pins of deleted prompts are skipped. */
export function pinnedEntries(pins, entries) {
  const byId = new Map(entries.map((e) => [e.id, e]));
  return effectivePins(pins).map((id) => byId.get(id)).filter(Boolean);
}

export function isPinned(pins, id) {
  return effectivePins(pins).includes(id);
}

/** Pin (at the end) or unpin one prompt; returns the new list. */
export function togglePin(pins, id) {
  const list = effectivePins(pins);
  const at = list.indexOf(id);
  if (at >= 0) list.splice(at, 1);
  else if (list.length < MAX_PINS) list.push(id);
  return list;
}

/** Move a pinned prompt up (-1) or down (+1); returns the new list. */
export function movePin(pins, id, delta) {
  const list = effectivePins(pins);
  const at = list.indexOf(id);
  const to = at + delta;
  if (at < 0 || to < 0 || to >= list.length) return list;
  [list[at], list[to]] = [list[to], list[at]];
  return list;
}

/** Keep only pins of prompts that still exist (after a delete). */
export function prunePins(pins, entries) {
  if (!Array.isArray(pins)) return pins;
  const ids = new Set(entries.map((e) => e.id));
  return pins.filter((id) => ids.has(id));
}

export function sameList(a, b) {
  return effectivePins(a).join("\n") === effectivePins(b).join("\n");
}

/** The JSON file of an export (title, description and body only). */
export function exportPrompts(prompts, { project = "", scope = "" } = {}) {
  return `${JSON.stringify({
    format: EXPORT_FORMAT, version: 1, exported_at: new Date().toISOString(), project, scope,
    prompts: prompts.map((p) => ({ title: p.title || "", description: p.description ?? p.desc ?? "", body: p.body || "" })),
  }, null, 2)}\n`;
}

/** Prompts from an export file (or a bare list); throws with a reason. */
export function parseImport(text) {
  let data;
  try { data = JSON.parse(String(text || "")); } catch { throw new Error("not JSON"); }
  const list = Array.isArray(data) ? data : data?.prompts;
  if (!Array.isArray(list)) throw new Error("no prompts list");
  if (!Array.isArray(data) && data.format && data.format !== EXPORT_FORMAT) throw new Error(`format ${data.format}`);
  const out = list.map((p) => ({
    title: String(p?.title ?? "").trim(), description: String(p?.description ?? "").trim(), body: String(p?.body ?? "").trim(),
  })).filter((p) => p.title || p.body);
  if (!out.length) throw new Error("no prompts");
  const bad = out.findIndex((p) => !p.title || !p.body);
  if (bad >= 0) throw new Error(`prompt ${bad + 1} needs a title and a body`);
  return out;
}

/** Placeholder names used in a body that are not filled at copy time. */
export function unknownPlaceholders(body) {
  const out = [];
  for (const [, name] of String(body || "").matchAll(/\{([A-Za-z_]\w*)\}/g)) {
    if (!["project", "unit", "user"].includes(name) && !out.includes(name)) out.push(name);
  }
  return out;
}
