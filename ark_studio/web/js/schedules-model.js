// Pure helpers for Settings › Lines and Settings › Scheduled tasks
// (views/settings-lines.js; server: store/branches.go, internal/schedules).
// Dependency-free; see tests/js/schedules_model.test.mjs.

/** The line tag namespace: lowercase letter first, then letters, digits, - or _, at most 24. */
export function validPrefix(value) {
  const v = String(value ?? "").trim();
  return /^[a-z][a-z0-9_-]{0,23}$/.test(v) && !["type", "status", "importance", "needs", "marker"].includes(v);
}

/**
 * The suggestion helper's answer as reviewable rows:
 * `[{ key, kind: "inherit" | "line", units: [uid], tags: [tag], label, reason, count }]`.
 * Inherit rows are one per unit; a new line is one row for all its units
 * (its root topic or goal included, so a topic carrying the tag shows the line).
 */
export function suggestionRows(data = {}) {
  const rows = [];
  for (const item of data.inherit || []) {
    if (!item?.uid || !(item.tags || []).length) continue;
    rows.push({ key: `i:${item.uid}`, kind: "inherit", units: [item.uid], tags: [...item.tags],
      label: `${item.uid} · ${item.title || ""}`, reason: (item.via || []).join(", "), status: item.status || "", count: 1 });
  }
  for (const line of data.new_lines || []) {
    if (!line?.tag || !(line.units || []).length) continue;
    const units = [...new Set([...(line.include_root || []), ...line.units])];
    rows.push({ key: `n:${line.tag}`, kind: "line", units, tags: [line.tag],
      label: `${line.root?.uid || ""} · ${line.root?.title || ""}`, reason: line.root?.uid || "", count: units.length });
  }
  return rows;
}

/**
 * POST /api/units/batch bodies applying `rows` (at most `max` units per
 * call): `[{ updates: [{ unit, tags_add }] }]`, one update per unit with every
 * tag the rows give it.
 */
export function applyBatches(rows = [], max = 400) {
  const byUnit = new Map();
  for (const row of rows) {
    for (const uid of row.units || []) {
      const tags = byUnit.get(uid) || new Set();
      for (const tag of row.tags || []) tags.add(tag);
      byUnit.set(uid, tags);
    }
  }
  const updates = [...byUnit].map(([unit, tags]) => ({ unit, tags_add: [...tags] }));
  const out = [];
  for (let i = 0; i < updates.length; i += max) out.push({ updates: updates.slice(i, i + max) });
  return out;
}

// ------------------------------------------------------- scheduled tasks

export const SCHEDULE_KINDS = ["daily", "weekly", "hours"];

/** Minutes east of UTC of this browser (the schedule's time zone). */
export function browserOffset(date = new Date()) {
  return -date.getTimezoneOffset();
}

/** A task's schedule in words: `t(key, vars)` translates. */
export function scheduleLabel(spec = {}, t = (key, vars) => `${key} ${JSON.stringify(vars || {})}`) {
  const tz = Number(spec.tz_offset) || 0;
  const zone = `UTC${tz >= 0 ? "+" : "-"}${Math.floor(Math.abs(tz) / 60)}${Math.abs(tz) % 60 ? `:${String(Math.abs(tz) % 60).padStart(2, "0")}` : ""}`;
  if (spec.kind === "hours") return t("schedule.every_hours", { n: spec.every || 1 });
  if (spec.kind === "weekly") return t("schedule.weekly_at", { day: t(`schedule.weekday.${spec.weekday || 0}`), at: spec.at || "", zone });
  return t("schedule.daily_at", { at: spec.at || "", zone });
}

/** The form's values as a task body for POST {action: "save"}; `errors` lists what is wrong. */
export function taskFromForm(form = {}) {
  const errors = [];
  const name = String(form.name || "").trim();
  const prompt = String(form.prompt || "").trim();
  if (!name) errors.push("name");
  if (!prompt) errors.push("prompt");
  const kind = SCHEDULE_KINDS.includes(form.kind) ? form.kind : "daily";
  const schedule = { kind, tz_offset: Number(form.tz_offset) || 0 };
  if (kind === "hours") {
    const every = Math.round(Number(form.every));
    if (!(every >= 1 && every <= 168)) errors.push("every");
    schedule.every = every;
  } else {
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(String(form.at || ""))) errors.push("at");
    schedule.at = String(form.at || "");
    if (kind === "weekly") schedule.weekday = Math.min(6, Math.max(0, Number(form.weekday) || 0));
  }
  const task = {
    name, prompt, schedule, mode: form.mode === "apply" ? "apply" : "suggest", enabled: form.enabled !== false,
    template: form.template || "",
    max_steps: Number(form.max_steps) || 0, max_writes: Number(form.max_writes) || 0, max_tokens: Number(form.max_tokens) || 0,
  };
  if (form.id) task.id = form.id;
  return { task, errors };
}

/** The form for a template (a new task) or a saved task (editing). */
export function formFor(source = {}, offset = 0) {
  const spec = source.schedule || {};
  return {
    id: source.owner ? source.id : "", template: source.owner ? source.template || "" : source.id || "",
    name: source.owner ? source.name : source.name || "", prompt: source.prompt || "",
    kind: spec.kind || "daily", at: spec.at || "07:00", weekday: spec.weekday || 0, every: spec.every || 6,
    tz_offset: source.owner ? spec.tz_offset || 0 : offset,
    mode: source.mode || "suggest", enabled: source.owner ? Boolean(source.enabled) : true,
    max_steps: source.max_steps || "", max_writes: source.max_writes || "", max_tokens: source.max_tokens || "",
  };
}

/** Status of a run or a task's last run: "ok" | "failed" | "stopped" | "running" | "never". */
export function runStatus(state = {}) {
  if (state?.running_since) return "running";
  return ["ok", "failed", "stopped"].includes(state?.last_status) ? state.last_status : "never";
}
