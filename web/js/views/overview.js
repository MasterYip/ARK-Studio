import { h, clear, fmtDate, fmtDateTime, relativeTime, percent, typeColor } from "../dom.js";
import { t, getLang, logLabel, statusLabel, typeLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { actorLabel, openModal, toast } from "../components.js";
import { api, mediaUrl } from "../api.js";
import { openImageLightbox } from "../image-viewer.js";
import {
  borrowedSources, decisionOptions, filterNeeds, needsFacets, needsFilterActive, needsHistoryTags, needsKind,
  normalizeNeedsFilter, NEEDS_FILTER_GROUPS, previewKind, setNeedsGroupAll, toggleNeedsValue,
} from "../needs-model.js";
import { authorName, threadComments } from "../comments-model.js";
import { currentUsername, displayName, userAvatar } from "../users.js";
import { startPanel } from "./onboarding.js";
import { assigneeChip } from "../assignee.js";
import { GraphView } from "../graph.js";

// Home is a dashboard over GET /api/project/overview (see
// docs/guide/home-overview.md): one card per question, numbers first, and every
// count links to the filtered view it summarises.

const OPEN = ["inbox", "open", "active", "review", "blocked"];
const STAGE_ICONS = { capture: "bulb", plan: "route", evidence: "target", execute: "tasks", make: "palette", share: "send" };
const RUN_ACTIVE = new Set(["starting", "running", "stopping"]);

export function renderOverview(host, ctx) {
  const S = ctx.S;
  clear(host);
  const ov = S.overview || null;
  host.appendChild(h("div", { class: "page home-page ov-page" },
    h("div", { class: "ov-top" }, heroCard(ctx, ov), pulsePanel(ctx, ov)),
    startPanel(ctx),
    h("div", { class: "ov-grid" },
      needsYouPanel(ctx, S.needsYou || []),
      h("div", { class: "ov-side" },
        pipelinePanel(ctx, ov),
        teamPanel(ctx, ov)),
      duePanel(ctx, ov),
      researchPanel(ctx, ov),
      runsPanel(ctx, ov),
      activityPanel(ctx, ov))));
}

// Map with exactly these filters (everything else cleared).
function mapWith(ctx, filters) {
  ctx.navigate("map", null, filters);
}

function count(n) {
  return Number(n || 0).toLocaleString(getLang() === "zh" ? "zh-CN" : "en-US");
}

function panelHead(title, ...extra) {
  return h("div", { class: "panel-head" }, h("h3", null, title), ...extra);
}

function headLink(label, onclick, title = "") {
  return h("button", { class: "link", type: "button", onclick, title: title || undefined }, label);
}

function unavailable() {
  return h("div", { class: "empty sm" }, t("error.load"));
}

// --------------------------------------------------------------------- hero
function heroCard(ctx, ov) {
  const S = ctx.S;
  const project = S.project || {};
  const stats = S.stats || {};
  return h("section", { class: "hero ov-head" },
    heroGraph(ctx, ov?.graph),
    h("div", { class: "hero-content" },
      h("div", { class: "hero-kicker" },
        h("span", { class: "dot" }), t("overview.hero_kicker"),
        project.demo ? h("span", { class: "demo-badge" }, t("common.demo")) : null),
      h("h1", { class: "ov-title" }, project.name || t("overview.project_fallback")),
      project.description ? h("p", { class: "hero-desc", title: project.description }, project.description) : null,
      h("div", { class: "hero-meta" },
        h("span", { class: "pill" }, `${count(stats.total)} ${t("overview.stats.total")}`),
        h("span", { class: "pill" }, `${count(stats.links)} ${t("common.links")}`),
        h("span", { class: "pill" }, `${count(stats.by_type?.claim)} ${typeLabel("claim")}`),
        h("span", { class: "pill" }, `${count(stats.by_type?.task)} ${typeLabel("task")}`),
        h("span", { class: "dot-sep" }, "·"),
        h("span", { class: "muted" }, `${t("overview.updated")} ${relativeTime(project.updated || new Date().toISOString())}`)),
      h("div", { class: "hero-actions ov-actions" },
        h("button", { class: "primary-btn", type: "button", onclick: () => ctx.navigate("map") },
          icon("map", "sm"), t("overview.open_map")),
        h("button", { class: "ghost-btn hero-secondary", type: "button", onclick: () => editProject(ctx) },
          icon("edit", "sm"), t("overview.edit")),
        h("button", { class: "ghost-btn hero-secondary", type: "button", onclick: () => ctx.openCreate({ type: "idea" }) },
          icon("bulb", "sm"), t("ideas.capture")),
        h("span", { class: "hero-more" },
          h("button", { class: "icon-btn", type: "button", title: t("home.new"), "aria-label": t("home.new"),
            onclick: () => ctx.openCreate({}) }, icon("plus", "sm")),
          h("button", { class: "icon-btn", type: "button", title: t("home.import"), "aria-label": t("home.import"),
            onclick: () => ctx.openImport() }, icon("upload", "sm"))))));
}

function heroGraph(ctx, data) {
  const host = h("div", { class: "hero-graph", "aria-hidden": "true", inert: true });
  if (!data?.nodes?.length) return host;
  requestAnimationFrame(() => {
    if (!host.isConnected) return;
    const graph = new GraphView(host, {
      compact: true, readOnly: true, persistLayout: false, backdrop: true,
    });
    graph.showLabels = false;
    graph.setData(data.nodes, data.edges || []);
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
      graph.running = false;
      cancelAnimationFrame(graph.frame);
    }
    ctx.trackGraph(graph);
  });
  return host;
}

// -------------------------------------------------------------------- pulse
// Is the project moving?  Old-style stat tiles (icon + label, big number,
// a sub-line with the change over 7 days) and the 14-day chart under them.
function pulsePanel(ctx, ov) {
  const stats = ctx.S.stats || {};
  const block = h("section", { class: "stat-grid ov-pulse", "aria-label": t("home.pulse") });
  if (!ov) { block.appendChild(unavailable()); return block; }
  const k = ov.kpis || {};
  const tiles = [
    { key: "active", ic: "play", value: k.active?.now, delta: k.active?.delta, worse: 0,
      onclick: () => mapWith(ctx, { statuses: ["active"] }) },
    { key: "blocked", ic: "alert", value: k.blocked?.now, delta: k.blocked?.delta, worse: 1, tone: k.blocked?.now ? "warn" : "",
      onclick: () => mapWith(ctx, { statuses: ["blocked"] }) },
    { key: "overdue", ic: "clock", value: k.overdue?.now, delta: k.overdue?.delta, worse: 1, tone: k.overdue?.now ? "bad" : "",
      onclick: () => mapWith(ctx, { timeMode: "overdue" }) },
    { key: "review", ic: "check", value: k.review?.now, delta: k.review?.delta, worse: 0,
      onclick: () => mapWith(ctx, { statuses: ["review"] }) },
    { key: "open", ic: "tasks", value: k.open?.now, sub: t("home.kpi_open_hint"),
      onclick: () => mapWith(ctx, { statuses: OPEN }) },
    { key: "nodes", ic: "node", value: stats.total, sub: t("home.nodes_links", { n: count(stats.links) }),
      label: t("home.kpi.nodes"), onclick: () => ctx.navigate("map") },
  ];
  block.append(...tiles.map(kpiTile), trend(ctx, ov));
  return block;
}

function kpiTile({ key, ic, value, delta, worse, tone = "", sub = "", label = "", onclick }) {
  let cls = "flat";
  let text = null;
  if (delta != null) {
    if (delta > 0) cls = worse ? "worse" : "up";
    else if (delta < 0) cls = worse ? "better" : "down";
    text = h("span", { title: delta === 0 ? t("home.delta_same") : t("home.delta_title", { d: signed(delta) }) },
      h("b", null, delta === 0 ? "±0" : signed(delta)), h("span", { class: "ov-kpi-dl" }, ` ${t("home.vs_last_week")}`));
  }
  const name = label || t(`home.kpi.${key}`);
  return h("button", {
    class: `stat ov-kpi ${tone}`.trim(), type: "button", dataset: { kpi: key, count: String(value ?? 0) }, onclick,
    title: t("home.kpi_title", { label: name }),
  },
    h("span", { class: "k ov-kpi-k" }, icon(ic, "sm"), name),
    h("span", { class: "v ov-kpi-v" }, count(value)),
    text ? h("span", { class: `d ov-kpi-d ${cls}` }, text) : h("span", { class: "d ov-kpi-d" }, sub));
}

function signed(n) {
  return `${n > 0 ? "+" : "−"}${Math.abs(n)}`;
}

function trend(ctx, ov) {
  const days = ov.days || [];
  const k = ov.kpis || {};
  const max = Math.max(1, ...days.map((d) => Math.max(d.created, d.completed)));
  const totalCreated = days.reduce((s, d) => s + d.created, 0);
  const totalDone = days.reduce((s, d) => s + d.completed, 0);
  const bars = h("div", {
    class: "ov-bars", role: "img",
    "aria-label": t("home.trend_aria", { c: totalCreated, d: totalDone }),
  }, days.map((d) => h("div", {
    class: "ov-day", title: t("home.day_tip", { date: fmtDate(d.date), c: d.created, d: d.completed }),
    dataset: { date: d.date },
  },
    h("span", { class: "ov-bar created", style: { height: `${barPct(d.created, max)}%` } }),
    h("span", { class: "ov-bar done", style: { height: `${barPct(d.completed, max)}%` } }))));
  const first = days[0]?.date;
  const last = days[days.length - 1]?.date;
  return h("button", {
    class: "ov-trend", type: "button", dataset: { kpi: "trend" },
    title: t("home.trend_title"),
    onclick: () => mapWith(ctx, { timeMode: "recent", recentDays: 7 }),
  },
    h("div", { class: "ov-trend-head" },
      h("span", { class: "ov-kpi-k" }, t("home.trend")),
      h("span", { class: "ov-legend" },
        h("span", { class: "ov-key created" }), t("home.created"),
        h("b", null, count(k.created?.week)), h("small", null, t("home.prev_week", { n: k.created?.prev ?? 0 })),
        h("span", { class: "ov-key done" }), t("home.completed"),
        h("b", null, count(k.completed?.week)), h("small", null, t("home.prev_week", { n: k.completed?.prev ?? 0 })))),
    bars,
    h("div", { class: "ov-axis" }, h("span", null, first ? fmtDate(first) : ""), h("span", null, t("home.today"))));
}

// One meter segment; empty ones are left out so the 2 px gaps stay even.
function seg(kind, n, total) {
  return n ? h("span", { class: `seg ${kind}`, style: { width: `${percent(n, total)}%` } }) : null;
}

function barPct(value, max) {
  if (!value) return 0;
  return Math.max(8, Math.round((value / max) * 100));
}

// ---------------------------------------------------------------- pipeline
// Where is the work? Each node contributes to one status segment.
function pipelinePanel(ctx, ov) {
  const stats = ctx.S.stats || {};
  const stages = ov?.stages || stats.stages || [];
  const panel = h("section", { class: "panel ov-pipeline" },
    panelHead(t("overview.stages"), headLink(t("home.open_map"), () => ctx.navigate("map"))));
  const rows = h("div", { class: "ov-stages" });
  for (const stage of stages) {
    const total = stage.count || 0;
    const done = stage.done || 0;
    const active = stage.active || 0;
    const review = stage.review || 0;
    const tip = t("home.stage_tip", { done, count: total, active, review, blocked: stage.blocked || 0 });
    rows.appendChild(h("button", {
      class: `ov-stage ${total ? "" : "is-empty"}`.trim(), type: "button", dataset: { stage: stage.id, count: String(total) },
      title: `${t(`stage.${stage.id}`)} · ${tip}`,
      onclick: () => mapWith(ctx, { types: stage.types || [], statuses: [...OPEN, "done"] }),
    },
      h("span", { class: "ov-stage-name" }, icon(STAGE_ICONS[stage.id] || "layers", "sm"), t(`stage.${stage.id}`)),
      h("span", { class: "ov-meter", "aria-hidden": "true" },
        seg("done", done, total), seg("active", active, total), seg("review", review, total), seg("blocked", stage.blocked, total)),
      h("span", { class: "ov-stage-n" }, h("b", null, count(done)), `/${count(total)}`)));
  }
  panel.append(rows, h("div", { class: "ov-meter-key" },
    h("span", null, h("i", { class: "seg done" }), t("status.done")),
    h("span", null, h("i", { class: "seg active" }), t("status.active")),
    h("span", null, h("i", { class: "seg review" }), t("status.review")),
    h("span", null, h("i", { class: "seg blocked" }), t("status.blocked")),
    h("span", null, h("i", { class: "seg rest" }), t("home.other_open"))));
  return panel;
}

// -------------------------------------------------------------------- team
// Who is doing what?  Open work per effective assignee.
function teamPanel(ctx, ov) {
  const panel = h("section", { class: "panel ov-team" }, panelHead(t("home.team")));
  if (!ov) { panel.appendChild(unavailable()); return panel; }
  const me = String(ov.me || currentUsername() || "").toLowerCase();
  const members = [...(ov.members || [])].sort((a, b) =>
    (b.username.toLowerCase() === me) - (a.username.toLowerCase() === me) || b.open - a.open);
  const un = ov.unassigned || {};
  if (!members.length && !un.open) {
    panel.appendChild(h("div", { class: "empty sm" }, t("home.team_empty")));
    return panel;
  }
  const table = h("div", { class: "ov-team-rows", role: "table", "aria-label": t("home.team") },
    h("div", { class: "ov-team-row head", role: "row" },
      h("span", { role: "columnheader" }),
      h("span", { role: "columnheader" }, t("home.col.open")),
      h("span", { role: "columnheader" }, t("home.col.active")),
      h("span", { role: "columnheader" }, t("home.col.review")),
      h("span", { role: "columnheader" }, t("home.col.overdue"))));
  const row = (who, data, avatar, name, filters, extra = "") => h("button", {
    class: `ov-team-row ${extra}`.trim(), type: "button", role: "row", dataset: { assignee: who, open: String(data.open || 0) },
    title: t("home.member_title", { name: typeof name === "string" ? name : who }),
    onclick: () => mapWith(ctx, filters),
  },
    h("span", { class: "ov-who", role: "cell" }, avatar, h("span", { class: "ov-who-name" }, name)),
    h("span", { class: "n", role: "cell" }, count(data.open)),
    h("span", { class: "n", role: "cell" }, count(data.active)),
    h("span", { class: `n ${data.review ? "hl" : ""}`.trim(), role: "cell" }, count(data.review)),
    h("span", { class: `n ${data.overdue ? "bad" : ""}`.trim(), role: "cell" }, count(data.overdue)));
  const LIMIT = 5;
  for (const m of members.slice(0, LIMIT)) {
    const isMe = m.username.toLowerCase() === me;
    const label = displayName(m.username) + (isMe ? ` (${t("common.you")})` : "");
    table.appendChild(row(m.username, m, userAvatar(m.username, { size: "xs" }), label,
      { assignees: [m.username], statuses: OPEN }, m.member ? "" : "former"));
  }
  if (un.open) {
    table.appendChild(row("none", un, h("span", { class: "user-avatar xs ov-none" }, "–"), t("home.unassigned"),
      { assignees: ["none"], statuses: OPEN }, "unassigned"));
  }
  panel.appendChild(table);
  if (members.length > LIMIT) {
    panel.appendChild(h("div", { class: "ov-more muted" }, t("home.more_members", { n: members.length - LIMIT })));
  }
  return panel;
}

// --------------------------------------------------------- due & blocked
function duePanel(ctx, ov) {
  const panel = h("section", { class: "panel ov-due home-due" },
    panelHead(t("home.due"), headLink(t("home.kpi.overdue"), () => mapWith(ctx, { timeMode: "overdue" }))));
  if (!ov) { panel.appendChild(unavailable()); return panel; }
  const due = ov.due || {};
  const blocked = ov.blocked || {};
  if (!due.overdue_total && !due.soon_total && !blocked.total) {
    panel.appendChild(h("div", { class: "empty sm" }, t("home.no_due")));
    return panel;
  }
  const today = ov.today;
  const group = (key, label, n, items, detail) => (n ? h("div", { class: `ov-group ${key}`, dataset: { group: key } },
    h("div", { class: "ov-group-head" }, h("span", null, label), h("b", null, count(n))),
    items.map((u) => unitRow(ctx, u, detail(u)))) : null);
  panel.append(
    group("overdue", t("home.kpi.overdue"), due.overdue_total, (due.overdue || []).slice(0, 3),
      (u) => h("span", { class: "overdue" }, t("home.days_late", { n: daysBetween(u.end_at, today) }))),
    group("soon", t("home.next_7"), due.soon_total, (due.soon || []).slice(0, 3),
      (u) => (u.end_at === today ? t("home.due_today") : t("home.due_in", { n: daysBetween(today, u.end_at) }))),
    group("blocked", t("home.kpi.blocked"), blocked.total, (blocked.items || []).slice(0, 3), blockerText));
  return panel;
}

function blockerText(unit) {
  const b = (unit.blockers || []).find((x) => x.status !== "done") || (unit.blockers || [])[0];
  if (!b) return t("home.no_blocker");
  return h("span", { title: `${b.uid} · ${b.title}` },
    t(b.relation === "blocks" ? "home.blocked_by" : "home.waits_on", { uid: b.uid }));
}

function daysBetween(a, b) {
  if (!a || !b) return 0;
  return Math.max(0, Math.round((Date.parse(`${b}T00:00:00Z`) - Date.parse(`${a}T00:00:00Z`)) / 86400000));
}

function unitRow(ctx, unit, detail) {
  const who = unit.assignee_effective;
  return h("button", {
    class: "ov-row", type: "button", dataset: { unitRef: unit.uid },
    title: `${unit.uid} · ${unit.title}`,
    onclick: () => ctx.openUnit(unit.uid),
  },
    h("span", { class: "ov-uid" }, unit.uid),
    h("span", { class: "ov-row-title" }, unit.title),
    detail ? h("span", { class: "ov-row-d" }, detail) : null,
    who ? userAvatar(who, { size: "xs" }) : null);
}

// ---------------------------------------------------------------- research
function researchPanel(ctx, ov) {
  const panel = h("section", { class: "panel ov-research" },
    panelHead(t("home.research"), headLink(t("action.see_all"), () => mapWith(ctx, { types: ["question", "claim", "evidence", "experiment"] }))));
  if (!ov) { panel.appendChild(unavailable()); return panel; }
  const r = ov.research || {};
  const q = r.questions || {};
  const c = r.claims || {};
  const x = r.experiments || {};
  if (!q.total && !c.total && !x.total) {
    panel.appendChild(h("div", { class: "empty sm" }, t("home.research_empty")));
    return panel;
  }
  const xs = x.by_status || {};
  const stat = (key, value, label, sub, onclick) => h("button", {
    class: "ov-rstat", type: "button", dataset: { research: key, count: String(value ?? 0) }, onclick,
  }, h("b", null, count(value)), h("span", null, label), sub ? h("small", null, sub) : null);
  panel.appendChild(h("div", { class: "ov-rstats" },
    stat("questions", q.open, t("home.open_questions"), t("home.of_total", { n: q.total || 0 }),
      () => mapWith(ctx, { types: ["question"], statuses: OPEN })),
    stat("claims", c.supported, t("home.claims_supported"), t("home.of_total", { n: c.total || 0 }),
      () => mapWith(ctx, { types: ["claim"] })),
    stat("experiments", (xs.active || 0) + (xs.open || 0) + (xs.review || 0), t("home.experiments_running"),
      t("home.of_total", { n: x.total || 0 }), () => mapWith(ctx, { types: ["experiment"] }))));
  if (c.total) {
    const lacking = c.lacking || 0;
    panel.appendChild(h("div", {
      class: "ov-claims", role: "img",
      "aria-label": t("home.claims_aria", { s: c.supported || 0, x: c.contested || 0, l: lacking }),
    },
      h("span", { class: "ov-meter" },
        seg("done", c.supported, c.total), seg("blocked", c.contested, c.total)),
      h("span", { class: "ov-claims-key" },
        h("span", null, h("i", { class: "seg done" }), t("home.supported", { n: c.supported || 0 })),
        c.contested ? h("span", null, h("i", { class: "seg blocked" }), t("home.contested", { n: c.contested })) : null,
        h("span", null, h("i", { class: "seg rest" }), t("home.lacking", { n: lacking })))));
  }
  const items = c.lacking_items || [];
  if (items.length) {
    panel.appendChild(h("div", { class: "ov-group lacking", dataset: { group: "lacking" } },
      h("div", { class: "ov-group-head" }, h("span", null, t("home.lacking_title")), h("b", null, count(c.lacking_open))),
      items.slice(0, 3).map((u) => unitRow(ctx, u, statusLabel(u.status)))));
  } else if (x.total) {
    panel.appendChild(h("div", { class: "ov-chips" }, Object.entries(xs).map(([status, n]) => h("button", {
      class: "pill", type: "button", onclick: () => mapWith(ctx, { types: ["experiment"], statuses: [status] }),
    }, `${statusLabel(status)} ${n}`))));
  }
  return panel;
}

// ----------------------------------------------------------------- running
// What is running?  Agent sessions of this project and the GPU queue.
function runsPanel(ctx, ov) {
  const panel = h("section", { class: "panel ov-runs" },
    panelHead(t("home.running"), headLink(t("home.runs_link"), () => ctx.navigateWorkspace("execute", "sessions"))));
  if (!ov) { panel.appendChild(unavailable()); return panel; }
  const runs = ov.runs || { total: 0, recent: [] };
  const gpu = ov.gpu || {};
  const runsBlock = runs.total
    ? h("div", { class: "ov-group runs", dataset: { group: "runs" } },
      h("div", { class: "ov-group-head" }, h("span", null, t("home.runs")),
        h("b", { dataset: { active: String(runs.active || 0) } }, t("home.runs_count", { a: runs.active || 0, n: runs.total }))),
      (runs.recent || []).slice(0, 4).map((run) => h("button", {
        class: "ov-row", type: "button", dataset: { session: run.id },
        onclick: () => { ctx.S.selectedSessionId = run.id; ctx.navigateWorkspace("execute", "sessions"); },
      },
        h("span", { class: `ov-dot ${RUN_ACTIVE.has(run.status) ? "live" : run.status === "errored" ? "bad" : ""}`.trim() }),
        h("span", { class: "ov-row-title" }, run.title || run.id),
        h("span", { class: "ov-row-d" }, t(`tasks.status.${run.status}`) === `tasks.status.${run.status}`
          ? run.status : t(`tasks.status.${run.status}`), " · ", relativeTime(run.last_activity_at)))))
    : h("div", { class: "ov-hint" }, icon("terminal", "sm"), t("home.runs_none"));
  const jobs = gpu.jobs || {};
  const running = (jobs.running || 0) + (jobs.starting || 0);
  const queued = (jobs.queued || 0) + (jobs.pending || 0);
  const gpuLine = gpu.configured
    ? h("button", { class: "ov-gpu", type: "button", dataset: { gpu: "1" }, onclick: () => ctx.navigateWorkspace("execute", "gpu") },
      icon("cpu", "sm"),
      h("span", null, t("home.gpu")),
      h("b", null, t("home.gpu_jobs", { r: running, q: queued })),
      h("small", null, gpu.free_gpus == null ? t("home.gpu_not_probed")
        : t("home.gpu_free", { n: gpu.free_gpus, total: gpu.gpus ?? 0 })))
    : h("div", { class: "ov-hint", dataset: { gpu: "0" } }, icon("cpu", "sm"), t("home.gpu_off"));
  panel.append(runsBlock, gpuLine);
  return panel;
}

// ---------------------------------------------------------------- activity
function activityPanel(ctx, ov) {
  const items = (ov?.activity || ctx.S.activity || []).slice(0, 14);
  const panel = h("section", { class: "panel ov-activity home-activity" }, panelHead(t("overview.recent")));
  if (!items.length) {
    panel.appendChild(h("div", { class: "empty sm" }, t("inspector.no_activity")));
    return panel;
  }
  const groups = new Map();
  for (const item of items) {
    const key = localDay(item.at);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(item);
  }
  const list = h("div", { class: "ov-feed" });
  for (const [day, rows] of groups) {
    list.appendChild(h("div", { class: "ov-feed-day" }, dayLabel(day)));
    for (const item of rows) {
      const ref = item.unit_uid;
      list.appendChild(h(ref ? "button" : "div", {
        class: "ov-feed-row", type: ref ? "button" : undefined, dataset: ref ? { unitRef: ref } : {},
        title: fmtDateTime(item.at),
        onclick: ref ? () => ctx.openUnit(ref) : undefined,
      },
        h("span", { class: "ov-feed-time" }, timeOf(item.at)),
        h("span", { class: "ov-feed-txt" },
          h("b", null, actorLabel(item.actor)), " ", logLabel(item.action), " ",
          ref ? h("span", { class: "ov-uid" }, ref) : null,
          item.unit_title ? ` ${item.unit_title}` : "")));
    }
  }
  panel.appendChild(list);
  return panel;
}

function localDay(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

function dayLabel(day) {
  const now = new Date();
  const today = localDay(now.toISOString());
  const y = new Date(now); y.setDate(now.getDate() - 1);
  if (day === today) return t("home.today");
  if (day === localDay(y.toISOString())) return t("home.yesterday");
  return fmtDate(day);
}

function timeOf(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function editProject(ctx) {
  const name = h("input", { class: "input", value: ctx.S.project?.name || "" });
  const desc = h("textarea", { class: "textarea", style: { minHeight: "70px" }, value: ctx.S.project?.description || "" });
  const modal = openModal({
    title: t("overview.edit"),
    body: h("div", null,
      h("div", { class: "field" }, h("label", null, t("project.name")), name),
      h("div", { class: "field" }, h("label", null, t("project.desc")), desc)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: async () => {
          try {
            const data = await api.updateProject({ name: name.value.trim(), description: desc.value.trim() });
            ctx.applyProject(data);
            modal.close();
            toast(t("toast.saved"), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

// ------------------------------------------------------------- needs you
// Units waiting on the human (server: Store.needs_you): `review` = finished,
// waiting to be accepted; open `decision` = a question only the user answers.
function needsYouPanel(ctx, all) {
  const projectKey = String(ctx.S.project?.id || ctx.S.project?.name || "local");
  const expandedByProject = ctx.S.homeNeedsExpanded ||= {};
  if (!(projectKey in expandedByProject)) {
    try { expandedByProject[projectKey] = localStorage.getItem(`ark.needsExpanded:${projectKey}`) === "1"; }
    catch { expandedByProject[projectKey] = false; }
  }
  let expanded = expandedByProject[projectKey];
  // Items assigned to me first; otherwise the server order (decisions, newest).
  const me = String(ctx.S.overview?.me || currentUsername() || "").toLowerCase();
  const mine = (item) => Boolean(me) && String(item.assignee_effective || "").toLowerCase() === me;
  const sorted = [...all].sort((a, b) => mine(b) - mine(a));
  const filter = needsFilterFor(ctx, projectKey);
  const facets = needsFacets(sorted);
  const items = filterNeeds(sorted, filter);
  const filtered = needsFilterActive(filter);
  const kinds = {};
  for (const item of items) kinds[needsKind(item)] = (kinds[needsKind(item)] || 0) + 1;
  const oldest = items.reduce((min, item) => (!min || (item.updated_at && item.updated_at < min) ? item.updated_at : min), "");
  const mineCount = items.filter(mine).length;
  const summary = items.length ? h("span", { class: "needs-summary" },
    Object.entries(kinds).map(([kind, n]) => h("span", { class: `needs-sum kind-${kind}`, dataset: { kind, n: String(n) } },
      h("b", null, String(n)), " ", t(`needs.kind.${kind}`))),
    mineCount ? h("span", { class: "needs-sum mine", dataset: { mine: String(mineCount) } },
      t("home.needs_mine", { n: mineCount })) : null,
    oldest ? h("span", { class: "needs-sum age", title: fmtDateTime(oldest) },
      t("home.needs_oldest", { age: relativeTime(oldest) })) : null) : null;
  const panel = h("section", { class: "panel needs-panel", "aria-label": t("needs.title") },
    h("div", { class: "panel-head" },
      h("h3", { title: t("needs.hint") }, t("needs.title"),
        h("span", { class: `needs-count ${items.length ? "" : "zero"}`.trim(),
          title: filtered ? t("needs.filter.shown", { n: items.length, total: sorted.length }) : undefined },
        filtered ? `${items.length}/${sorted.length}` : String(items.length))),
      summary,
      // Every node that has ever needed you keeps a needs: tag (set by ARK).
      h("button", {
        class: "link needs-history", type: "button", title: t("needs.view_history_title"),
        onclick: () => ctx.navigate("map", null, { tags: needsHistoryTags() }),
      }, t("needs.view_history"))));
  if (sorted.length) panel.appendChild(needsFilterBar(ctx, projectKey, all, facets, filter));
  if (!items.length) {
    panel.appendChild(h("div", { class: "empty sm" }, sorted.length ? t("needs.filter.empty") : t("needs.empty"),
      sorted.length ? h("button", { class: "link needs-filter-clear", type: "button",
        onclick: () => updateNeedsFilter(ctx, projectKey, all, normalizeNeedsFilter({})) }, t("filters.clear")) : null));
    return panel;
  }
  const LIMIT = 4;
  const list = h("div", { class: "needs-list" }, items.map((item, index) => {
    const row = needsRow(ctx, item);
    if (index >= LIMIT) showNeedsRow(row, expanded);
    return row;
  }));
  panel.appendChild(list);
  if (items.length > LIMIT) {
    const more = h("button", { class: "link needs-toggle", type: "button", "aria-expanded": String(expanded),
      dataset: { open: expanded ? "1" : "0" } }, expanded ? t("needs.show_less") : t("needs.show_all", { n: items.length }));
    more.onclick = () => {
      expanded = !expanded;
      expandedByProject[projectKey] = expanded;
      try { localStorage.setItem(`ark.needsExpanded:${projectKey}`, expanded ? "1" : "0"); } catch { /* storage unavailable */ }
      list.querySelectorAll(".needs-row").forEach((row, index) => { if (index >= LIMIT) showNeedsRow(row, expanded); });
      more.dataset.open = expanded ? "1" : "0";
      more.setAttribute("aria-expanded", String(expanded));
      more.textContent = expanded ? t("needs.show_less") : t("needs.show_all", { n: items.length });
    };
    panel.appendChild(more);
  }
  return panel;
}

// Collapsed rows keep their thumbnails unloaded: on a slow link the browser
// may fetch every lazy image of a long hidden list at once.
function showNeedsRow(row, visible) {
  row.hidden = !visible;
  for (const img of row.querySelectorAll("img")) {
    if (!visible && img.getAttribute("src")) {
      img.dataset.deferredSrc = img.getAttribute("src");
      img.removeAttribute("src");
    } else if (visible && img.dataset.deferredSrc) {
      img.setAttribute("src", img.dataset.deferredSrc);
      delete img.dataset.deferredSrc;
    }
  }
}

// Category filters (type / kind / assignee), remembered per project.  Each
// group is a drop-down of capsule chips with an "All" switch, like the Map
// filter rail.
let needsOpenGroup = "";

function needsFilterFor(ctx, projectKey) {
  const byProject = ctx.S.homeNeedsFilter ||= {};
  if (!(projectKey in byProject)) {
    let raw = {};
    try { raw = JSON.parse(localStorage.getItem(`ark.needsFilter:${projectKey}`) || "{}"); } catch { raw = {}; }
    byProject[projectKey] = normalizeNeedsFilter(raw);
  }
  return byProject[projectKey];
}

function updateNeedsFilter(ctx, projectKey, all, next) {
  (ctx.S.homeNeedsFilter ||= {})[projectKey] = next;
  try { localStorage.setItem(`ark.needsFilter:${projectKey}`, JSON.stringify(next)); } catch { /* storage unavailable */ }
  const panel = document.querySelector(".needs-panel");
  if (panel) panel.replaceWith(needsYouPanel(ctx, all));
}

function needsValueLabel(group, value) {
  if (group === "type") return typeLabel(value);
  if (group === "kind") return t(`needs.kind.${value}`);
  return value ? displayName(value) : t("assignee.none");
}

function needsFilterBar(ctx, projectKey, all, facets, filter) {
  const groupLabel = { type: t("filters.types"), kind: t("needs.filter.kind"), assignee: t("needs.filter.assignee") };
  const bar = h("div", { class: "needs-filters", role: "group", "aria-label": t("needs.filter.label") });
  for (const group of NEEDS_FILTER_GROUPS) {
    const values = facets[group] || [];
    if (!values.length) continue;
    const hidden = (filter[group] || []).filter((value) => values.some((item) => item.value === value));
    const shown = values.length - hidden.length;
    const apply = (next) => { needsOpenGroup = group; updateNeedsFilter(ctx, projectKey, all, next); };
    const allToggle = h("input", {
      type: "checkbox", checked: hidden.length === 0,
      "aria-label": `${t("filters.all")} ${groupLabel[group]}`,
      onchange: (event) => apply(setNeedsGroupAll(filter, group, event.target.checked, values.map((item) => item.value))),
    });
    allToggle.indeterminate = hidden.length > 0 && shown > 0;
    const chips = h("div", { class: "type-filter" }, values.map(({ value, n }) => {
      const on = !hidden.includes(value);
      return h("button", {
        type: "button", class: `type-chip ${on ? "on" : ""}`.trim(), "aria-pressed": String(on),
        dataset: { group, value },
        onclick: () => apply(toggleNeedsValue(filter, group, value)),
      }, group === "type" ? h("i", { style: { "--dot": typeColor(value) } }) : null,
      needsValueLabel(group, value), h("span", { class: "n" }, String(n)));
    }));
    const details = h("details", {
      class: `needs-filter ${hidden.length ? "active" : ""}`.trim(), dataset: { group },
      open: needsOpenGroup === group ? true : undefined,
    },
    h("summary", null, h("span", null, groupLabel[group]),
      h("b", null, hidden.length ? `${shown}/${values.length}` : t("filters.all")), icon("chevron", "sm")),
    h("div", { class: "needs-filter-pop" },
      h("label", { class: "type-all-toggle" }, allToggle, t("filters.all")),
      chips));
    details.addEventListener("toggle", () => {
      if (details.open) {
        needsOpenGroup = group;
        // Keep the drop-down on screen: open it leftwards near the right edge.
        const pop = details.querySelector(".needs-filter-pop");
        pop.classList.remove("align-end");
        if (pop.getBoundingClientRect().right > document.documentElement.clientWidth - 8) pop.classList.add("align-end");
        bar.querySelectorAll("details[open]").forEach((other) => { if (other !== details) other.open = false; });
      } else if (needsOpenGroup === group) needsOpenGroup = "";
    });
    bar.appendChild(details);
  }
  if (needsFilterActive(filter)) {
    bar.appendChild(h("button", { class: "link needs-filter-clear", type: "button",
      onclick: () => { needsOpenGroup = ""; updateNeedsFilter(ctx, projectKey, all, normalizeNeedsFilter({})); } },
    t("filters.clear")));
  }
  return bar;
}

// Close an open filter drop-down on an outside click or Escape.
document.addEventListener("pointerdown", (event) => {
  const open = document.querySelector(".needs-filter[open]");
  if (open && !open.contains(event.target)) { open.open = false; needsOpenGroup = ""; }
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  const open = document.querySelector(".needs-filter[open]");
  if (!open) return;
  event.stopPropagation();
  open.open = false; needsOpenGroup = ""; open.querySelector("summary")?.focus();
}, true);

// One compact row per item: kind + title + age, a two-line excerpt, small
// media thumbnails, then actions.  "Comment" leaves a note the agent reads
// without changing the status.
function needsRow(ctx, item) {
  const kind = needsKind(item);
  const open = () => ctx.openUnit(item.uid);
  const comment = h("button", { class: "btn sm ghost", type: "button", onclick: () => commentUnit(ctx, item) },
    icon("message", "sm"), t("needs.comment"));
  const actions = kind === "decision"
    ? [
      h("button", { class: "btn sm primary", type: "button", onclick: () => decideUnit(ctx, item) }, icon("check", "sm"), t("needs.decide")),
      comment,
      h("button", { class: "btn sm ghost", type: "button", onclick: open }, t("needs.open")),
    ]
    : [
      h("button", { class: "btn sm primary", type: "button", onclick: () => acceptUnit(ctx, item) }, icon("check", "sm"), t("needs.accept")),
      h("button", { class: "btn sm ghost", type: "button", onclick: () => sendBackUnit(ctx, item) }, icon("arrowLeft", "sm"), t("needs.send_back")),
      comment,
      h("button", { class: "btn sm ghost", type: "button", onclick: open }, t("needs.open")),
    ];
  const media = (item.media || []).filter((m) => previewKind(m));
  // Unresolved user comments: what the agent still has to act on.
  const notes = item.comments_open || 0;
  // Previews from linked nodes are labelled; the node itself has none.
  const borrowed = !item.own_media;
  const noMedia = borrowed ? h("span", { class: "needs-no-media", title: t("needs.no_media_hint") },
    icon("image", "sm"), t("needs.no_media"),
    borrowedSources(media).length ? ` · ${t("needs.previews_from_links")}` : "") : null;
  return h("article", { class: `needs-row kind-${kind}`, dataset: { unitRef: item.uid, needsKind: kind } },
    h("span", { class: `needs-chip kind-${kind}` }, t(`needs.kind.${kind}`)),
    h("div", { class: "needs-main" },
      h("div", { class: "needs-line" },
        h("button", { class: "needs-title", type: "button", onclick: open, title: `${item.uid} · ${item.title}` },
          h("span", { class: "needs-uid" }, item.uid), " ", item.title),
        assigneeChip(item),
        notes ? h("span", { class: "needs-notes", title: t("needs.comments_open", { n: notes }), dataset: { open: notes } },
          icon("message", "sm"), String(notes)) : null,
        h("span", { class: "needs-age", title: fmtDateTime(item.updated_at) }, relativeTime(item.updated_at))),
      item.excerpt ? h("div", { class: "needs-excerpt", onclick: open }, item.excerpt_plain || stripMd(item.excerpt)) : null,
      h("div", { class: "needs-foot" },
        media.length || noMedia ? h("div", { class: "needs-media-col" },
          media.length ? needsStrip(ctx, media) : null,
          noMedia) : null,
        h("div", { class: "needs-actions" }, actions))));
}

function stripMd(text) {
  return String(text || "")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^\s{0,3}(#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)/gm, "")
    .replace(/(\*\*|__|`|~~)/g, "")
    .replace(/\s+/g, " ").trim();
}

// Every preview, newest first, in one horizontal strip.  The mouse wheel
// scrolls it sideways; clicking a thumbnail opens the usual image viewer or
// video player.  Videos past the first few load only when scrolled into view.
const NEEDS_EAGER_VIDEOS = 4;

function needsStrip(ctx, media) {
  const strip = h("div", { class: "needs-media" }, media.map((m, index) => needsThumb(ctx, m, index)));
  strip.addEventListener("wheel", (event) => {
    if (strip.scrollWidth <= strip.clientWidth + 1) return; // nothing to scroll: page scrolls
    const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
    if (!delta) return;
    const max = strip.scrollWidth - strip.clientWidth;
    // At either end, let the page scroll on instead of trapping the wheel.
    if ((delta < 0 && strip.scrollLeft <= 0) || (delta > 0 && strip.scrollLeft >= max - 1)) return;
    event.preventDefault();
    strip.scrollLeft = Math.max(0, Math.min(max, strip.scrollLeft + delta));
  }, { passive: false });
  const lazy = [...strip.querySelectorAll("video[data-src]")];
  if (lazy.length && "IntersectionObserver" in window) {
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        const video = entry.target;
        video.src = video.dataset.src;
        video.removeAttribute("data-src");
        observer.unobserve(video);
      }
    }, { root: strip, rootMargin: "0px 160px" });
    lazy.forEach((video) => observer.observe(video));
  } else {
    lazy.forEach((video) => { video.src = video.dataset.src; video.removeAttribute("data-src"); });
  }
  return strip;
}

function needsThumb(ctx, media, index = 0) {
  const kind = previewKind(media);
  const src = mediaUrl(media);
  const label = media.caption || media.from?.title || media.kind || "";
  const from = media.from ? t("needs.from", { uid: media.from.uid }) : "";
  const title = [label, from].filter(Boolean).join(" · ");
  const eager = index < NEEDS_EAGER_VIDEOS;
  const stage = kind === "image"
    ? h("img", { src, alt: label, loading: "lazy" })
    : h("span", { class: "needs-video" },
      h("video", {
        src: eager ? `${src}#t=0.1` : undefined, "data-src": eager ? undefined : `${src}#t=0.1`,
        muted: true, playsinline: true, preload: "metadata",
      }),
      h("span", { class: "needs-play", "aria-hidden": "true" }, icon("play", "sm")));
  const thumb = h("button", {
    class: `needs-thumb kind-${kind}`, type: "button", title,
    "aria-label": kind === "video" ? `${t("needs.play_video")}: ${title}` : title,
    onclick: () => (kind === "image"
      ? openImageLightbox({ src, alt: label, title: label, meta: from, className: "nd-lightbox" })
      : openVideo(src, label, from)),
  }, stage, h("span", { class: "needs-thumb-cap" }, label || from));
  if (!media.from) return thumb;
  // Borrowed from a linked node: say which, and link to it.
  return h("span", { class: "needs-thumb-wrap borrowed" }, thumb,
    h("a", {
      class: "needs-from", href: `?unit=${encodeURIComponent(media.from.uid)}#/home`,
      title: t("needs.from_title", { uid: media.from.uid, title: media.from.title || "" }),
      dataset: { unitRef: media.from.uid },
      onclick: (event) => { event.preventDefault(); ctx.openUnit(media.from.uid); },
    }, from));
}

function openVideo(src, label, from) {
  const preview = openModal({
    title: [label, from].filter(Boolean).join(" · ") || t("needs.play_video"),
    wide: true,
    body: h("video", { class: "needs-video-player", src, controls: true, autoplay: true, playsinline: true, preload: "metadata" }),
  });
  preview.modal.classList.add("media-preview-dialog");
  preview.backdrop.classList.add("media-preview-backdrop");
}
async function refreshNeeds(ctx) {
  try {
    const data = await api.project();
    ctx.S.needsYou = data.needs_you || [];
    if (data.summary && ctx.S.project) ctx.S.project = { ...ctx.S.project, ...data.summary };
    // Update only the Needs-you panel and the topbar badge.  Calling
    // applyProject() re-routes the whole page, which loses the user's
    // position and makes an accept feel like a browser refresh.
    const panel = document.querySelector(".needs-panel");
    if (panel) {
      const anchors = [...panel.querySelectorAll('.needs-row:not([hidden])')].map((row) => ({
        ref: row.dataset.unitRef, top: row.getBoundingClientRect().top, bottom: row.getBoundingClientRect().bottom,
      })).filter((row) => row.bottom > 58 && row.top < innerHeight);
      const replacement = needsYouPanel(ctx, ctx.S.needsYou);
      panel.replaceWith(replacement);
      const rows = [...replacement.querySelectorAll('.needs-row:not([hidden])')];
      const anchor = anchors.find((item) => rows.some((row) => row.dataset.unitRef === item.ref));
      if (anchor) {
        const row = rows.find((item) => item.dataset.unitRef === anchor.ref);
        window.scrollBy({ top: row.getBoundingClientRect().top - anchor.top, behavior: "instant" });
      }
    }
    ctx.syncNeedsBadge?.();
  } catch (err) { toast(err.message, "err"); }
}

// Each action writes one comment row on the unit (kind comment / sent_back /
// decided / accepted) and, except a plain comment, then sets the status.
// Comments are their own entries on the node, so agents rewriting the body
// cannot lose them; agents read the unresolved ones before they resume.
async function earlierComments(item) {
  try { return (await api.unit(item.uid)).unit?.comments || []; } catch { return []; }
}

function noteDialog(ctx, item, {
  title, action, label, kind, status, done, options = [], history = [], placeholder = "", optional = false,
}) {
  const input = h("textarea", { class: "textarea", style: { minHeight: "72px" }, placeholder: placeholder || label, "aria-label": label });
  const threads = threadComments(history).slice(-5);
  const line = (c) => h("div", { class: `needs-history-item kind-${c.kind}${c.resolved ? " resolved" : ""}` },
    h("b", null, `${t(`comments.kind.${c.kind}`)} · ${displayName(authorName(c)) || t(`comments.author.${c.kind === "agent_reply" ? "agent" : "user"}`)} · ${fmtDate(c.created_at)}`),
    " ", c.body);
  const past = threads.length ? h("div", { class: "needs-history" },
    h("div", { class: "muted" }, t("needs.previous_notes")),
    threads.map((thread) => h("div", { class: "needs-history-thread" },
      line(thread.comment),
      thread.replies.map((reply) => h("div", { class: "needs-history-reply" }, line(reply)))))) : null;
  const picks = options.length ? h("div", { class: "needs-options" }, options.map((option) => h("button", {
    class: "btn sm ghost", type: "button",
    onclick: () => { input.value = option; input.focus(); },
  }, option))) : null;
  openModal({
    title,
    body: h("div", null,
      past,
      h("div", { class: "field" }, h("label", null, label), picks, input)),
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: action, primary: true,
        onClick: async () => {
          const text = input.value.trim();
          if (!text && !optional) { toast(t("needs.reason_required"), "err"); input.focus(); return false; }
          try {
            if (text) await api.addComment(item.uid, { body: text, kind });
            if (status) await api.updateUnit(item.uid, { status });
            toast(t(done, { uid: item.uid }), "ok");
          } catch (err) { toast(err.message, "err"); return false; }
          refreshNeeds(ctx);
        },
      },
    ],
  });
}

// Accept closes the node; the note is optional.
async function acceptUnit(ctx, item) {
  noteDialog(ctx, item, {
    title: t("needs.accept_title", { uid: item.uid }), action: t("needs.accept"), label: t("needs.accept_note"),
    kind: "accepted", status: "done", done: "needs.accepted", optional: true, history: await earlierComments(item),
  });
}

async function sendBackUnit(ctx, item) {
  noteDialog(ctx, item, {
    title: t("needs.send_back_title", { uid: item.uid }), action: t("needs.send_back"), label: t("needs.send_back_reason"),
    kind: "sent_back", status: "active", done: "needs.sent_back", history: await earlierComments(item),
  });
}

async function decideUnit(ctx, item) {
  let unit = null;
  try { unit = (await api.unit(item.uid)).unit; } catch { /* quick picks are optional */ }
  noteDialog(ctx, item, {
    title: t("needs.decide_title", { uid: item.uid }), action: t("needs.decide"), label: t("needs.decide_answer"),
    kind: "decided", status: "done", done: "needs.decided", options: decisionOptions(unit?.body),
    history: unit?.comments || [],
  });
}

// A comment keeps the status.
async function commentUnit(ctx, item) {
  noteDialog(ctx, item, {
    title: t("needs.comment_title", { uid: item.uid }), action: t("needs.comment_save"), label: t("needs.comment_label"),
    kind: "comment", status: null, done: "needs.commented", history: await earlierComments(item),
    placeholder: t("needs.comment_placeholder"),
  });
}
