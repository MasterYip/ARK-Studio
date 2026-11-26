// Settings › Lines (project): the line tag namespace (settings.branches,
// default "branch") and "Suggest line tags" (GET /api/project/branches/suggest,
// read only; rows are applied with POST /api/units/batch only when the
// person confirms).  Settings › Scheduled tasks (project): fixed prompts the
// built-in Assistant runs on a schedule as the admin who saved them
// (GET/POST /api/project/schedules; server internal/schedules).
import { h, clear, fmtDateTime, relativeTime } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { openModal, toast, confirmDialog } from "../components.js";
import {
  validPrefix, suggestionRows, applyBatches, scheduleLabel, taskFromForm, formFor, runStatus, browserOffset, SCHEDULE_KINDS,
} from "../schedules-model.js";
import { lineLabel, validLinePath, branchTopicEntries } from "../needs-model.js";

export const LINES_SECTION = "lines";
export const SCHEDULES_SECTION = "schedules";

const canWrite = (ctx) => Boolean(ctx.S.session?.local) || ["write", "admin"].includes(ctx.S.session?.project_permission);
const isAdmin = (ctx) => Boolean(ctx.S.session?.local) || ctx.S.session?.project_permission === "admin";

// ------------------------------------------------------------------ lines

export function linesPanel(ctx) {
  const current = () => ctx.S.project?.settings?.branches?.prefix || "branch";
  const admin = isAdmin(ctx);
  const input = h("input", { class: "input", value: current(), maxlength: "24", style: { width: "160px" },
    disabled: admin ? undefined : true, "aria-label": t("lines.prefix") });
  const status = h("span", { class: "muted" });
  const save = h("button", {
    class: "btn sm primary", type: "button", disabled: admin ? undefined : true,
    onclick: async () => {
      const value = input.value.trim();
      if (!validPrefix(value)) { status.textContent = t("lines.prefix_invalid"); return; }
      try {
        const data = await api.updateSettings({ branches: value === "branch" ? null : { prefix: value } });
        if (data?.settings && ctx.S.project) ctx.S.project = { ...ctx.S.project, settings: data.settings };
        status.textContent = t("lines.prefix_saved", { prefix: value });
        ctx.S.branches = null;
      } catch (err) { toast(err.message, "err"); }
    },
  }, t("action.save"));
  const results = h("div", { class: "lines-suggest", "data-lines-suggest": "" });
  const suggest = h("button", {
    class: "btn sm", type: "button", "data-lines-suggest-run": "1", disabled: canWrite(ctx) ? undefined : true,
    onclick: () => runSuggest(ctx, results),
  }, icon("sparkles", "sm"), t("lines.suggest"));
  return h("section", { class: "panel settings-panel lines-panel", "data-lines-panel": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("lines.title"))),
    h("p", { class: "settings-lead" }, t("lines.lead")),
    h("div", { class: "settings-row" },
      h("label", { class: "settings-label" }, t("lines.prefix")),
      h("div", { class: "row tight" }, input, h("code", { class: "muted" }, `${input.value || "branch"}:<${t("lines.name")}>`), save, status)),
    h("p", { class: "muted small" }, admin ? t("lines.prefix_hint") : t("lines.prefix_admin")),
    h("div", { class: "settings-sub" }, t("lines.list_title")),
    linesList(ctx),
    h("div", { class: "settings-sub" }, t("lines.suggest_title")),
    h("p", { class: "muted small" }, t("lines.suggest_hint")),
    h("div", { class: "row tight" }, suggest,
      h("button", { class: "btn sm ghost", type: "button", onclick: () => ctx.openSettingsAt?.(SCHEDULES_SECTION) },
        icon("clock", "sm"), t("lines.to_schedules"))),
    results);
}

async function runSuggest(ctx, host) {
  clear(host);
  host.appendChild(h("div", { class: "muted" }, t("topic.loading")));
  let data;
  try { data = await api.branchSuggest({ limit: 400 }); } catch (err) { clear(host); host.appendChild(h("div", { class: "muted" }, err.message)); return; }
  const rows = suggestionRows(data);
  const picked = new Set(rows.map((row) => row.key));
  const paint = () => {
    clear(host);
    if (!rows.length) { host.appendChild(h("div", { class: "empty sm" }, t("lines.suggest_none"))); return; }
    const units = rows.reduce((n, row) => n + row.count, 0);
    host.appendChild(h("div", { class: "lines-suggest-head" },
      h("span", null, t("lines.suggest_found", { rows: rows.length, units, total: data.inherit_total ?? rows.length })),
      h("button", { class: "btn sm primary", type: "button", "data-lines-apply-all": "1",
        disabled: picked.size ? undefined : true, onclick: () => apply(rows.filter((row) => picked.has(row.key))) },
      t("lines.apply_selected", { n: picked.size }))));
    const table = h("div", { class: "lines-suggest-list", role: "table" });
    for (const row of rows) {
      const box = h("input", { type: "checkbox", checked: picked.has(row.key),
        onchange: (event) => { if (event.target.checked) picked.add(row.key); else picked.delete(row.key); paint(); } });
      table.appendChild(h("div", { class: `lines-suggest-row kind-${row.kind}`, role: "row", dataset: { key: row.key } },
        box,
        h("button", { class: "linklike lines-suggest-unit", type: "button", title: row.label,
          onclick: () => ctx.openUnit?.(row.units[row.kind === "line" ? 0 : 0]) }, row.label),
        h("span", { class: "lines-suggest-tags" }, row.tags.map((tag) => h("span", { class: "tag tag-branch" }, tag))),
        h("span", { class: "muted lines-suggest-why" },
          row.kind === "line" ? t("lines.new_line", { n: row.count }) : t("lines.from", { via: row.reason })),
        h("button", { class: "btn sm ghost", type: "button", onclick: () => apply([row]) }, t("lines.apply"))));
    }
    host.appendChild(table);
  };
  const apply = async (chosen) => {
    if (!chosen.length) return;
    const n = chosen.reduce((sum, row) => sum + row.count, 0);
    if (!(await confirmDialog(t("lines.apply_confirm", { n }), { confirmLabel: t("lines.apply") }))) return;
    try {
      for (const body of applyBatches(chosen)) await api.batchUpdate(body);
      toast(t("lines.applied", { n }), "ok");
      for (const row of chosen) { rows.splice(rows.indexOf(row), 1); picked.delete(row.key); }
      ctx.S.branches = null;
      paint();
    } catch (err) { toast(err.message, "err"); }
  };
  paint();
}

// ------------------------------------------------------- scheduled tasks

export function schedulesPanel(ctx) {
  const body = h("div", { class: "schedules-body", "data-schedules": "" }, h("div", { class: "muted" }, t("topic.loading")));
  const panel = h("section", { class: "panel settings-panel schedules-panel", "data-schedules-panel": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("schedule.title"))),
    h("p", { class: "settings-lead" }, t("schedule.lead")),
    body);
  const load = async () => {
    try { paintSchedules(ctx, body, await api.schedules(), load); } catch (err) { clear(body); body.appendChild(h("div", { class: "muted" }, err.message)); }
  };
  load();
  return panel;
}

const statusPill = (status) => h("span", { class: `schedule-status status-${status}` }, t(`schedule.status.${status}`));

function paintSchedules(ctx, host, data, reload) {
  clear(host);
  const edit = Boolean(data.can_edit);
  const post = async (body) => {
    try { paintSchedules(ctx, host, await api.schedulesPost(body), reload); return true; } catch (err) { toast(err.message, "err"); return false; }
  };
  if ((data.failing || []).length) {
    host.appendChild(h("div", { class: "schedule-failing", role: "alert" }, icon("alert", "sm"),
      data.failing.map((f) => h("span", null, t("schedule.failed_line", { name: f.name, error: f.error || "" })))));
  }
  const templates = data.templates || [];
  if (edit) {
    host.appendChild(h("div", { class: "row tight schedule-new" },
      h("span", { class: "muted" }, t("schedule.new_from")),
      ...templates.map((tpl) => h("button", { class: "btn sm", type: "button", dataset: { template: tpl.id },
        onclick: () => editTask(ctx, { ...tpl, name: getLang() === "zh" ? tpl.name : tpl.name_en }, post) },
      icon("plus", "sm"), getLang() === "zh" ? tpl.name : tpl.name_en)),
      h("button", { class: "btn sm ghost", type: "button", onclick: () => editTask(ctx, {}, post) }, icon("plus", "sm"), t("schedule.blank"))));
  } else {
    host.appendChild(h("p", { class: "muted small" }, t("schedule.admin_only")));
  }
  const tasks = data.tasks || [];
  if (!tasks.length) host.appendChild(h("div", { class: "empty sm" }, t("schedule.none")));
  for (const task of tasks) {
    const status = runStatus(task.state);
    host.appendChild(h("article", { class: `schedule-row ${task.enabled ? "" : "off"}`.trim(), dataset: { schedule: task.id } },
      h("div", { class: "schedule-main" },
        h("div", { class: "schedule-line" },
          h("b", null, task.name),
          h("span", { class: `schedule-mode mode-${task.mode}` }, t(`schedule.mode.${task.mode}`)),
          task.enabled ? null : h("span", { class: "muted" }, t("schedule.disabled")),
          statusPill(status)),
        h("div", { class: "muted small" },
          scheduleLabel(task.schedule, t), " · ",
          task.enabled ? t("schedule.next", { at: fmtDateTime(task.next_at) }) : "",
          task.state?.last_run_at ? ` · ${t("schedule.last", { age: relativeTime(task.state.last_run_at) })}` : "",
          ` · ${t("schedule.runs_as", { owner: task.owner })}`),
        status === "failed" ? h("div", { class: "schedule-error" }, task.state.last_error || "") : null),
      edit ? h("div", { class: "schedule-actions" },
        h("button", { class: "btn sm", type: "button", disabled: status === "running" ? true : undefined,
          onclick: async () => { if (await post({ action: "run", id: task.id })) toast(t("schedule.started", { name: task.name }), "ok"); setTimeout(reload, 4000); } },
        icon("play", "sm"), t("schedule.run_now")),
        h("button", { class: "btn sm ghost", type: "button", onclick: () => editTask(ctx, task, post) }, t("action.edit")),
        h("button", { class: "btn sm ghost", type: "button",
          onclick: async () => { if (await confirmDialog(t("schedule.delete_confirm", { name: task.name }), { danger: true })) post({ action: "delete", id: task.id }); } },
        icon("trash", "sm"))) : null));
  }
  const runs = data.runs || [];
  if (runs.length) {
    host.appendChild(h("div", { class: "settings-sub" }, t("schedule.log")));
    host.appendChild(h("div", { class: "schedule-runs" }, runs.map((run) => h("div", { class: `schedule-run status-${run.status}`, dataset: { run: run.id } },
      h("div", { class: "schedule-line" },
        statusPill(run.status === "running" ? "running" : run.status),
        h("b", null, run.name),
        h("span", { class: "muted", title: fmtDateTime(run.started_at) }, relativeTime(run.started_at)),
        h("span", { class: "muted" }, t("schedule.run_stats", { steps: run.steps || 0, tokens: run.tokens || 0, n: (run.touched || []).length })),
        run.review ? h("button", { class: "linklike", type: "button", onclick: () => ctx.openUnit?.(run.review) }, t("schedule.review", { uid: run.review })) : null,
        h("button", { class: "linklike", type: "button", onclick: () => showTranscript(run) }, t("schedule.transcript"))),
      run.error ? h("div", { class: "schedule-error" }, run.error) : null,
      run.summary ? h("div", { class: "schedule-summary" }, run.summary) : null,
      (run.touched || []).length ? h("div", { class: "muted small" }, run.touched.slice(0, 20).map((uid) =>
        h("button", { class: "linklike", type: "button", onclick: () => ctx.openUnit?.(uid) }, uid, " "))) : null))));
  }
}

async function showTranscript(run) {
  let data;
  try { data = await api.scheduleTranscript(run.id); } catch (err) { toast(err.message, "err"); return; }
  const list = (data.transcript || []).filter((m) => m && m.role !== "system");
  openModal({
    title: `${run.name} · ${fmtDateTime(run.started_at)}`, wide: true,
    body: h("div", { class: "schedule-transcript" }, list.map((m) => h("div", { class: `schedule-msg role-${m.role}` },
      h("b", null, m.role), " ",
      h("pre", null, [m.content || "", ...(m.tool_calls || []).map((c) => `→ ${c.function?.name}(${c.function?.arguments || ""})`)].filter(Boolean).join("\n"))))),
    actions: [{ label: t("action.close"), ghost: true }],
  });
}

function editTask(ctx, source, post) {
  const form = formFor(source, browserOffset());
  const field = (label, control, hint = "") => h("label", { class: "schedule-field" }, h("span", null, label), control,
    hint ? h("small", { class: "muted" }, hint) : null);
  const input = (key, attrs = {}) => {
    const el = h("input", { class: "input", value: form[key] ?? "", ...attrs });
    el.addEventListener("input", () => { form[key] = el.value; });
    return el;
  };
  const select = (key, options) => {
    const el = h("select", { class: "input" }, options.map(([value, label]) => h("option", { value, selected: String(form[key]) === String(value) ? true : undefined }, label)));
    el.addEventListener("change", () => { form[key] = el.value; paintKind(); });
    return el;
  };
  const prompt = h("textarea", { class: "input", rows: "10" }, form.prompt);
  prompt.addEventListener("input", () => { form.prompt = prompt.value; });
  const kindRow = h("div", { class: "row tight" });
  const paintKind = () => {
    clear(kindRow);
    kindRow.append(select("kind", SCHEDULE_KINDS.map((k) => [k, t(`schedule.kind.${k}`)])));
    if (form.kind === "hours") kindRow.append(input("every", { type: "number", min: "1", max: "168", style: { width: "80px" } }), h("span", null, t("schedule.hours")));
    else {
      if (form.kind === "weekly") kindRow.append(select("weekday", [0, 1, 2, 3, 4, 5, 6].map((d) => [d, t(`schedule.weekday.${d}`)])));
      kindRow.append(input("at", { type: "time" }));
    }
  };
  paintKind();
  const enabled = h("input", { type: "checkbox", checked: form.enabled });
  enabled.addEventListener("change", () => { form.enabled = enabled.checked; });
  const body = h("div", { class: "schedule-form" },
    field(t("schedule.name"), input("name", { maxlength: "80" })),
    field(t("schedule.prompt"), prompt, t("schedule.prompt_hint")),
    field(t("schedule.when"), kindRow, t("schedule.when_hint")),
    field(t("schedule.mode_label"), select("mode", [["suggest", t("schedule.mode.suggest")], ["apply", t("schedule.mode.apply")]]), t("schedule.mode_hint")),
    h("div", { class: "row tight" },
      field(t("schedule.max_steps"), input("max_steps", { type: "number", min: "1", max: "24", placeholder: "8", style: { width: "90px" } })),
      field(t("schedule.max_writes"), input("max_writes", { type: "number", min: "1", max: "100", placeholder: "20", style: { width: "90px" } })),
      field(t("schedule.max_tokens"), input("max_tokens", { type: "number", min: "1000", max: "400000", placeholder: "60000", style: { width: "110px" } }))),
    h("label", { class: "row tight" }, enabled, t("schedule.enabled")),
    h("p", { class: "muted small" }, t("schedule.owner_hint")));
  openModal({
    title: form.id ? t("schedule.edit_title") : t("schedule.new_title"), body, wide: true,
    actions: [
      { label: t("action.cancel"), ghost: true },
      { label: t("action.save"), primary: true, onClick: async () => {
        const { task, errors } = taskFromForm(form);
        if (errors.length) { toast(t("schedule.form_errors", { fields: errors.join(", ") }), "err"); return false; }
        return post({ action: "save", task });
      } },
    ],
  });
}

// The project's lines as a tree, each with "Rename / move".
function linesList(ctx) {
  const host = h("div", { class: "lines-list", "data-lines-list": "" }, h("div", { class: "muted" }, t("topic.loading")));
  const load = async () => {
    let data;
    try { data = await api.branches(); } catch (err) { clear(host); host.appendChild(h("div", { class: "muted" }, err.message)); return; }
    clear(host);
    const lines = branchTopicEntries(data.items || []).sort((a, b) => a.name.localeCompare(b.name));
    if (!lines.length) { host.appendChild(h("div", { class: "empty sm" }, t("lines.none", { prefix: data.prefix || "branch" }))); return; }
    for (const line of lines) {
      const depth = line.name.split("/").length - 1;
      host.appendChild(h("div", { class: "lines-list-row", style: { paddingLeft: `${8 + depth * 16}px` }, dataset: { line: line.name } },
        h("button", { class: "linklike", type: "button", title: line.tag, onclick: () => ctx.openTopic?.(line.ref) },
          depth ? line.name.split("/").at(-1) : line.name),
        h("span", { class: "muted small" }, t("lines.units", { n: line.units }), line.pending ? ` · ${t("lines.pending", { n: line.pending })}` : ""),
        line.status !== "active" ? h("span", { class: `branch-badge status-${line.status}` }, t(`branch.status.${line.status}`)) : null,
        canWrite(ctx) ? h("button", { class: "btn sm ghost", type: "button", onclick: () => openLineRename(ctx, line.name, load) },
          t("lines.rename")) : null));
    }
  };
  load();
  return host;
}

/**
 * "Rename / move line": the new path, a preview of every tag that changes
 * (the line and the lines below it), then the change in one batch with an
 * Undo in the toast.
 */
export function openLineRename(ctx, from, onDone = () => {}) {
  const input = h("input", { class: "input", value: from, "aria-label": t("lines.rename_to") });
  const preview = h("div", { class: "lines-rename-preview" });
  let shown = "";
  const check = async () => {
    const to = input.value.trim();
    clear(preview);
    if (!validLinePath(to) || to === from) { preview.appendChild(h("div", { class: "muted" }, t("lines.rename_invalid"))); return null; }
    try {
      const data = await api.renameLine({ from, to, dry_run: true });
      shown = to;
      preview.appendChild(h("div", null, t("lines.rename_preview", { n: data.units, tags: data.changes.length, topics: data.topics.length })));
      preview.appendChild(h("div", { class: "lines-rename-rows" }, data.changes.slice(0, 200).map((c) =>
        h("div", { class: "muted small" }, `${c.uid} · ${c.from} → ${c.to}`))));
      return data;
    } catch (err) { preview.appendChild(h("div", { class: "muted" }, err.message)); return null; }
  };
  let timer = 0;
  input.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(check, 300); });
  openModal({
    title: t("lines.rename_title", { name: lineLabel(from) }), wide: true,
    body: h("div", { class: "schedule-form" },
      h("label", { class: "schedule-field" }, h("span", null, t("lines.rename_to")), input, h("small", { class: "muted" }, t("lines.rename_hint"))),
      preview),
    actions: [
      { label: t("action.cancel"), ghost: true },
      { label: t("lines.rename"), primary: true, onClick: async () => {
        const to = input.value.trim();
        if (shown !== to && !(await check())) return false;
        try {
          const done = await api.renameLine({ from, to });
          const uids = [...new Set((done.changes || []).map((c) => c.uid))];
          ctx.S.branches = null;
          toast(t("lines.renamed", { from: lineLabel(from), to: lineLabel(to), n: uids.length }), "ok", {
            duration: 10000,
            action: { label: t("lines.undo"), onClick: async () => {
              try { await api.renameLine({ from: to, to: from, units: uids }); ctx.S.branches = null; onDone(); toast(t("lines.rename_undone"), "ok"); } catch (err) { toast(err.message, "err"); }
            } },
          });
          onDone();
          return true;
        } catch (err) { toast(err.message, "err"); return false; }
      } },
    ],
  });
  setTimeout(() => { input.focus(); input.select(); }, 30);
}
