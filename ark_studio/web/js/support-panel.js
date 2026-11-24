// The Board's "Workspace support" deck and the Home "Agents and hosts"
// card over one read, GET /api/project/support (support-model.js has the
// merging rules).  Both views share the loaded reply for 15 s per project,
// so Home and Board agree; a render never waits for it: the cards fill in
// when the reply arrives.  People with read access see no actions.

import { h, clear, fmtDate, relativeTime } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { openSetupResources } from "./gpu-note.js";
import {
  agentRows, hostRows, libraryCounts, deadlineRows, supportSummary, sessionCounts, cardEmpty, sizeParts, daysUntil,
} from "./support-model.js";

const MAX_AGE = 15_000;
let cache = { project: "", at: 0, data: null, pending: null };

/** The cached reply of this project (or null). */
export function supportCached(projectId) {
  return cache.project === projectId ? cache.data : null;
}

/** The project's reply: cached for 15 s, one request at a time. */
export function loadSupport(projectId, { maxAge = MAX_AGE } = {}) {
  if (cache.project !== projectId) cache = { project: projectId, at: 0, data: null, pending: null };
  if (cache.data && Date.now() - cache.at < maxAge) return Promise.resolve(cache.data);
  if (cache.pending) return cache.pending;
  const mine = cache;
  mine.pending = api.projectSupport().then((data) => {
    if (cache === mine) { mine.data = data; mine.at = Date.now(); }
    return data;
  }).finally(() => { mine.pending = null; });
  return mine.pending;
}

/** Forget the cached reply (after launching a session, for example). */
export function invalidateSupport() {
  cache = { project: "", at: 0, data: null, pending: null };
}

// Fill `fill(data, loading)` now with what is cached, again when the reply
// arrives and differs.  (An element a re-render already replaced is filled
// off the page, which costs nothing.)
function withSupport(ctx, _el, fill) {
  const projectId = ctx.S.project?.id || "";
  const cached = supportCached(projectId);
  fill(cached, !cached);
  const shown = cached ? JSON.stringify(cached) : "";
  loadSupport(projectId).then((data) => {
    if (JSON.stringify(data) !== shown) fill(data, false);
  }).catch(() => {
    if (!cached) fill(null, false);
  });
}

// ------------------------------------------------------------- navigation

function openSession(ctx, id) {
  ctx.S.selectedSessionId = id;
  ctx.S.sessionThreadMode = "thread";
  if (ctx.navigateWorkspace) ctx.navigateWorkspace("execute", "sessions");
  else ctx.setTaskView?.("sessions");
}

function openRuns(ctx) {
  if (ctx.navigateWorkspace) ctx.navigateWorkspace("execute", "sessions");
  else ctx.setTaskView?.("sessions");
}

function openResources(ctx) {
  if (ctx.navigateWorkspace) ctx.navigateWorkspace("execute", "resources");
  else ctx.setTaskView?.("resources");
}

function openTokens(ctx) {
  if (ctx.openSettingsAt) { ctx.openSettingsAt("agentAccess"); return; }
  try { localStorage.setItem("ark.settingsTab", "agentAccess"); } catch { /* storage unavailable */ }
  ctx.navigate?.("settings");
}

function runAction(ctx, action) {
  if (action === "launch") ctx.openSessionLaunch?.();
  else if (action === "settings") openSetupResources(ctx);
}

function emptyLine(ctx, state) {
  if (!state) return null;
  const actionKey = state.action === "launch" ? "support.action.launch" : state.action === "settings" ? "support.action.settings" : "";
  return h("div", { class: "empty sm support-empty", dataset: { empty: state.key } },
    h("span", null, t(state.key)),
    actionKey ? h("button", { class: "link", type: "button", dataset: { supportAction: state.action }, onclick: () => runAction(ctx, state.action) },
      t(actionKey)) : null);
}

// ------------------------------------------------------------------ labels

// Countable text: "<base>_one" for 1, "<base>_n" otherwise.
function tn(base, n, vars = {}) {
  return t(n === 1 ? `${base}_one` : `${base}_n`, { n, ...vars });
}

function stateLabel(state) {
  return t(`support.state.${state}`);
}

function hostKindLabel(kind) {
  return t(`support.host_kind.${kind}`);
}

function busyText(busy) {
  if (!busy) return "";
  const parts = [];
  if (busy.subagents) parts.push(tn("support.subagents", busy.subagents));
  if (busy.background) parts.push(tn("support.background", busy.background));
  return parts.join(" · ");
}

function agentDetail(row) {
  const parts = [];
  if (row.kind === "session") {
    if (row.provider) parts.push(row.provider);
    if (row.model) parts.push(row.model);
    if (row.host) parts.push(t("support.on_host", { host: row.host }));
    const busy = busyText(row.busy);
    if (busy) parts.push(busy);
    if (row.children) parts.push(tn("support.children", row.children));
    if (row.viaToken) parts.push(row.writes ? `${t("support.session_token")} · ${tn("support.writes", row.writes)}` : t("support.session_token"));
  } else if (row.kind === "external") {
    parts.push(t("support.via_token"));
    if (row.writes) parts.push(tn("support.writes", row.writes));
    if (row.likely) parts.push(t("support.likely_session", { title: row.likely.title }));
  } else {
    parts.push(t("support.node"));
  }
  return parts.join(" · ");
}

// Three short lines: name and state; the unit it works on; provider,
// model, host and background work (or how the agent is connected).
function agentRow(ctx, row, compact = false) {
  const unit = row.unit;
  const click = row.kind === "session" ? () => openSession(ctx, row.id)
    : unit?.uid ? () => ctx.openUnit(unit.uid)
      : row.kind === "node" ? () => ctx.openUnit(row.id) : null;
  return h(click ? "button" : "div", {
    class: `support-row agent ${compact ? "compact" : ""}`.trim(), type: click ? "button" : undefined,
    dataset: { supportAgent: row.key, state: row.state }, onclick: click || undefined,
    title: row.error || undefined,
  },
    h("span", { class: `support-dot ${row.state}` }),
    h("span", { class: "support-main" },
      h("span", { class: "support-line" },
        h("span", { class: "support-title" }, row.title),
        h("span", { class: `support-state ${row.state}` }, stateLabel(row.state))),
      unit?.uid ? h("span", { class: "support-sub support-unit" }, h("b", null, unit.uid), unit.title ? ` ${unit.title}` : "") : null,
      h("span", { class: "support-line meta" },
        h("span", { class: "support-sub" }, agentDetail(row)),
        row.at && row.kind !== "node" ? h("span", { class: "support-time" }, relativeTime(row.at)) : null)));
}

function hostDetail(row) {
  const parts = [hostKindLabel(row.kind)];
  if (row.current) parts.push(t("support.this_device"));
  if (row.sessions) parts.push(tn("support.sessions_running", row.sessions));
  if (row.kind === "gpu-server") {
    parts.push(row.gpu ? t("support.gpu_free", { free: row.gpu.free, total: row.gpu.total }) : t("support.gpu_unchecked"));
    if (row.jobs && (row.jobs.running || row.jobs.queued)) parts.push(t("support.gpu_jobs", { r: row.jobs.running, q: row.jobs.queued }));
  }
  if (row.pathsMissing) parts.push(tn("support.paths_missing", row.pathsMissing));
  if (row.tags?.length) parts.push(row.tags.join(", "));
  return parts.join(" · ");
}

function hostRow(ctx, row, compact = false) {
  const click = row.unit ? () => ctx.openUnit(row.unit) : () => openResources(ctx);
  const when = row.kind === "ark" || row.kind === "node" ? ""
    : row.lastSeen ? t("support.seen", { when: relativeTime(row.lastSeen) })
      : row.probedAt ? t("support.checked", { when: relativeTime(row.probedAt) }) : "";
  return h("button", {
    class: `support-row host ${compact ? "compact" : ""}`.trim(), type: "button",
    dataset: { supportHost: row.key, state: row.state }, onclick: click,
  },
    h("span", { class: `support-dot host ${row.state}` }),
    h("span", { class: "support-main" },
      h("span", { class: "support-line" },
        h("span", { class: "support-title" }, row.label),
        h("span", { class: `support-state ${row.state}` }, t(`support.host_state.${row.state}`))),
      h("span", { class: "support-line meta" },
        h("span", { class: "support-sub" }, hostDetail(row)),
        when ? h("span", { class: "support-time" }, when) : null)));
}

function deadlineDetail(row) {
  if (row.kind === "unit") return row.overdue ? t("support.due.overdue", { date: fmtDate(row.at) }) : fmtDate(row.at);
  if (row.kind === "token") {
    const d = daysUntil(row.at);
    return d <= 0 ? t("support.due.token_today") : tn("support.due.token", d);
  }
  if (row.kind === "gpu_job") return t("support.due.gpu", { server: row.server || "GPU", when: relativeTime(row.at) });
  if (row.kind === "review") return tn("support.due.review", row.waitDays);
  return "";
}

const DEADLINE_ICONS = { unit: "calendar", token: "key", gpu_job: "cpu", review: "eye" };

function deadlineRow(ctx, row) {
  const click = row.kind === "token" ? () => openTokens(ctx) : row.uid ? () => ctx.openUnit(row.uid) : null;
  return h(click ? "button" : "div", {
    class: "action-item support-due", type: click ? "button" : undefined, dataset: { supportDue: row.key, unitRef: row.uid || undefined },
    onclick: click || undefined,
  },
    h("span", { class: `ic ${row.overdue ? "high" : row.kind === "unit" ? "" : "low"}`.trim() }, icon(DEADLINE_ICONS[row.kind] || "clock", "sm")),
    h("div", { class: "txt" },
      h("div", { class: "t" }, row.uid && row.kind !== "gpu_job" ? h("span", { class: "support-uid" }, `${row.uid} `) : null, row.label),
      h("div", { class: "d" }, h("span", { class: row.overdue ? "overdue" : "" }, deadlineDetail(row)))));
}

function more(n, onclick) {
  return n > 0 ? h("button", { class: "link support-more", type: "button", onclick }, t("support.more", { n })) : null;
}

// ------------------------------------------------------------ the model

function modelOf(ctx, data) {
  const units = ctx.S.units || [];
  const agents = agentRows(data, { units, legacy: units.filter((u) => u.type === "agent") });
  const hosts = hostRows(data, { legacy: units.filter((u) => u.type === "host") });
  const library = libraryCounts(data, units);
  const due = deadlineRows(data, units.filter((u) => ["task", "milestone", "experiment", "production"].includes(u.type)));
  return { agents, hosts, library, due, summary: supportSummary(agents, hosts, library), canWrite: data?.can_write === true };
}

/** Header text of the support panel ("2 agents · 3 hosts · 5 in library"). */
export function supportMeta(model) {
  return t("tasks.support.meta", { agents: model.summary.agents, hosts: model.summary.hosts, library: model.summary.library });
}

// ----------------------------------------------------------- Board deck

/**
 * The Board's four support cards (agents, hosts, library, deadlines).
 * `onMeta(text)` gets the header counts whenever the data changes.
 */
export function supportDeck(ctx, { onMeta } = {}) {
  const aside = h("aside", { class: "task-aside support-deck" });
  withSupport(ctx, aside, (data, loading) => {
    if (!data && !loading) {
      onMeta?.("");
      clear(aside);
      aside.append(h("div", { class: "empty sm support-empty" }, t("support.unavailable")));
      return;
    }
    const m = modelOf(ctx, data);
    onMeta?.(loading ? t("support.loading") : supportMeta(m));
    clear(aside);
    aside.append(agentsCard(ctx, m, loading), hostsCard(ctx, m, loading), libraryCard(ctx, m, loading), dueCard(ctx, m, loading));
  });
  return aside;
}

function agentsCard(ctx, m, loading) {
  const counts = sessionCounts(m.agents);
  const shown = m.agents.slice(0, 5);
  const head = [counts.running ? t("support.count.running", { n: counts.running }) : "",
    counts.approval ? t("support.count.approval", { n: counts.approval }) : "",
    counts.error ? tn("support.count.error", counts.error) : "",
    counts.external ? t("support.count.external", { n: counts.external }) : ""].filter(Boolean).join(" · ");
  return h("div", { class: "panel support-card", dataset: { supportCard: "agents" } },
    h("div", { class: "panel-head" }, h("h3", null, t("support.agents.title")),
      head ? h("span", { class: "support-head-meta" }, head) : null,
      h("button", { class: "link", type: "button", onclick: () => openRuns(ctx) }, t("action.see_all"))),
    h("div", { class: "support-list" }, shown.map((row) => agentRow(ctx, row))),
    emptyLine(ctx, cardEmpty("agents", { rows: m.agents.length, canWrite: m.canWrite, loading })),
    more(m.agents.length - shown.length, () => openRuns(ctx)));
}

function hostsCard(ctx, m, loading) {
  const shown = m.hosts.slice(0, 5);
  return h("div", { class: "panel support-card", dataset: { supportCard: "hosts" } },
    h("div", { class: "panel-head" }, h("h3", null, t("tasks.hosts")),
      h("span", { class: "support-head-meta" }, m.hosts.length ? String(m.hosts.length) : ""),
      h("button", { class: "link", type: "button", onclick: () => openResources(ctx) }, t("action.see_all"))),
    h("div", { class: "support-list" }, shown.map((row) => hostRow(ctx, row))),
    emptyLine(ctx, cardEmpty("hosts", { rows: m.hosts.length, canWrite: m.canWrite, loading })),
    more(m.hosts.length - shown.length, () => openResources(ctx)));
}

function tile(key, value, onclick) {
  return h(onclick ? "button" : "div", { class: "role-tile support-tile", type: onclick ? "button" : undefined, dataset: { supportTile: key }, onclick: onclick || undefined },
    h("div", { class: "k" }, t(`support.library.${key}`)),
    h("div", { class: "v" }, value));
}

function libraryCard(ctx, m, loading) {
  const lib = m.library;
  const size = sizeParts(lib.bytes);
  const assets = () => (ctx.navigateWorkspace ? ctx.navigateWorkspace("output", "assets") : ctx.navigate?.("production"));
  const notes = [size ? t("support.library.size", { n: size.n, unit: size.unit }) : "",
    lib.other ? tn("support.library.other", lib.other) : "",
    lib.resourceNodes ? tn("support.library.nodes", lib.resourceNodes) : ""].filter(Boolean).join(" · ");
  return h("div", { class: "panel support-card", dataset: { supportCard: "library" } },
    h("div", { class: "panel-head" }, h("h3", null, t("tasks.long_term")),
      h("button", { class: "link", type: "button", onclick: () => openResources(ctx) }, t("action.see_all"))),
    h("div", { class: "support-tiles" },
      tile("artifacts", lib.artifacts, assets),
      tile("repositories", lib.repositories, () => openResources(ctx)),
      tile("workspaces", lib.workspaces, () => openResources(ctx)),
      tile("storage", lib.storage, () => openResources(ctx))),
    notes ? h("div", { class: "support-note" }, notes) : null,
    emptyLine(ctx, cardEmpty("library", { rows: lib.total + lib.workspaces, canWrite: m.canWrite, loading })));
}

function dueCard(ctx, m, loading) {
  return h("div", { class: "panel support-card", dataset: { supportCard: "due" } },
    h("div", { class: "panel-head" }, h("h3", null, t("tasks.due_soon"))),
    h("div", { class: "action-list" }, m.due.map((row) => deadlineRow(ctx, row))),
    emptyLine(ctx, cardEmpty("due", { rows: m.due.length, canWrite: m.canWrite, loading })));
}

// ------------------------------------------------------------- Home card

/**
 * Home's "Agents and hosts" card: the same rows as the Board deck, shorter
 * (agents, hosts, the GPU line).
 */
export function homeSupportCard(ctx) {
  const panel = h("section", { class: "panel ov-runs support-home", dataset: { supportCard: "home" } });
  withSupport(ctx, panel, (data, loading) => {
    const m = modelOf(ctx, data);
    clear(panel);
    panel.append(h("div", { class: "panel-head" }, h("h3", null, t("support.home.title")),
      h("button", { class: "link", type: "button", onclick: () => openRuns(ctx) }, t("home.runs_link"))));
    if (!data && !loading) {
      panel.append(h("div", { class: "empty sm support-empty" }, t("support.unavailable")));
      return;
    }
    const agents = m.agents.filter((r) => r.kind !== "node").slice(0, 4);
    panel.append(
      h("div", { class: "ov-group agents", dataset: { group: "agents" } },
        h("div", { class: "ov-group-head" }, h("span", null, t("support.agents.title")), h("b", null, String(m.summary.agents))),
        agents.map((row) => agentRow(ctx, row, true)),
        emptyLine(ctx, cardEmpty("agents", { rows: agents.length, canWrite: m.canWrite, loading }))),
      h("div", { class: "ov-group hosts", dataset: { group: "hosts" } },
        h("div", { class: "ov-group-head" }, h("span", null, t("tasks.hosts")), h("b", null, String(m.hosts.length))),
        m.hosts.filter((r) => r.kind !== "node").slice(0, 3).map((row) => hostRow(ctx, row, true)),
        emptyLine(ctx, cardEmpty("hosts", { rows: m.hosts.length, canWrite: m.canWrite, loading }))),
      gpuLine(ctx, data?.gpu));
  });
  return panel;
}

function gpuLine(ctx, gpu) {
  if (!gpu) return null;
  if (!gpu.configured || !gpu.servers) {
    return h("div", { class: "ov-hint", dataset: { gpu: "0" } }, icon("cpu", "sm"), t("support.gpu.none"));
  }
  const jobs = gpu.jobs || {};
  const running = (jobs.running || 0) + (jobs.starting || 0);
  const queued = (jobs.queued || 0) + (jobs.pending || 0);
  return h("button", { class: "ov-gpu", type: "button", dataset: { gpu: "1" }, onclick: () => (ctx.navigateWorkspace ? ctx.navigateWorkspace("execute", "gpu") : null) },
    icon("cpu", "sm"),
    h("span", null, t("home.gpu")),
    h("b", null, t("home.gpu_jobs", { r: running, q: queued })),
    h("small", null, gpu.free_gpus == null ? t("home.gpu_not_probed") : t("home.gpu_free", { n: gpu.free_gpus, total: gpu.gpus ?? 0 })));
}

/**
 * Home's due card: the deadlines the server knows (expiring agent tokens,
 * GPU time limits, long reviews) under the unit groups it already shows.
 * Empty (and hidden) when there are none.
 */
export function homeDeadlines(ctx) {
  const box = h("div", { class: "ov-group support-other-due", dataset: { group: "other-due" } });
  withSupport(ctx, box, (data) => {
    clear(box);
    const rows = deadlineRows(data, []).slice(0, 3);
    box.classList.toggle("hidden", !rows.length);
    if (!rows.length) return;
    box.append(h("div", { class: "ov-group-head" }, h("span", null, t("support.due.other")), h("b", null, String(rows.length))),
      ...rows.map((row) => deadlineRow(ctx, row)));
  });
  return box;
}
