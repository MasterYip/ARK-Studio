import { h, clear, fmtDate, fmtDateTime, relativeTime, percent } from "../dom.js";
import { t, getLang, logLabel, statusLabel, typeLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { actorLabel, openModal, toast } from "../components.js";
import { api } from "../api.js";
import {
  bulkAcceptable, filterNeeds, groupNeeds, needsFacets, needsFilterActive, needsKind, normalizeNeedsFilter, sortMineFirst,
} from "../needs-model.js";
import {
  NEEDS_CHANGED, bulkAcceptDialog, closeNeedsFilters, needsEntryCard, needsFilterBar, needsFilterFor, needsProjectKey, saveNeedsFilter,
  watchNeedsSeen,
} from "../needs-ui.js";
import { currentUsername, displayName, userAvatar } from "../users.js";
import { startPanel, setupNeedsRow } from "./onboarding.js";
import { runningAgentRows } from "./agent-connect.js";
import {
  agentConnection, checklistCard, checklistItems, checklistRestorable, dismissChecklist, restoreChecklist, tutorialChapters, tutorialTopic,
} from "../home-model.js";
import { GraphView } from "../graph.js";
import { fillProgressively } from "../progressive-list.js";
import { backdropSample } from "../graph-core.js";
import { heroBudget, heroGraphSample } from "../home-filter-model.js";
import { homeFilterLine } from "../home-filter-ui.js";
import { desktopUpdateBanner } from "../desktop-update.js";

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
  const project = S.project || {};
  const page = h("div", { class: "page home-page ov-page" });
  // ARK Studio desktop app, local Home: a newer version (desktop-update.js).
  page.appendChild(desktopUpdateBanner());
  if (project.id === TUTORIAL_ID) page.appendChild(tutorialBanner(ctx));
  page.append(...[
    h("div", { class: "ov-top" }, heroCard(ctx, ov), pulsePanel(ctx, ov)),
    startPanel(ctx),
    // New projects: the getting-started steps, under Connect your agent.
    checklistPanel(ctx, ov),
    h("div", { class: "ov-grid" },
      needsYouPanel(ctx, S.needsYou || []),
      h("div", { class: "ov-side" },
        pipelinePanel(ctx, ov),
        teamPanel(ctx, ov)),
      duePanel(ctx, ov),
      researchPanel(ctx, ov),
      runsPanel(ctx, ov),
      activityPanel(ctx, ov)),
  ].filter(Boolean));
  host.appendChild(page);
}

const TUTORIAL_ID = "ark-demo";

// The getting-started card's state for this person and project
// (home-model.js checklistCard): started, completed, dismissed, expired.
function checklistKey(ctx) {
  return `ark.home.checklist:${ctx.S.session?.user || "local"}:${ctx.S.project?.id || ""}`;
}

function loadChecklistRecord(ctx) {
  try { return JSON.parse(localStorage.getItem(checklistKey(ctx)) || "null"); } catch { return null; }
}

function saveChecklistRecord(ctx, record) {
  try { localStorage.setItem(checklistKey(ctx), JSON.stringify(record)); } catch { /* storage unavailable */ }
}

function showChecklist(ctx) {
  saveChecklistRecord(ctx, restoreChecklist(loadChecklistRecord(ctx)));
  ctx.refresh();
}

// ------------------------------------------------------- getting started
// The steps to the first accepted result, each ticked from data ARK already
// has (home-model.js checklistItems).  Shown on new projects until closed, or
// for three days once every step is done (checklistCard).
function checklistPanel(ctx, ov) {
  const connection = agentConnection({ agents: ov?.agents });
  const items = checklistItems({ stats: ctx.S.stats, connection, team: !ctx.S.session?.local, members: ov?.members || [] });
  const card = checklistCard({ project: ctx.S.project, stats: ctx.S.stats, items, record: loadChecklistRecord(ctx) });
  // Without the overview the agent step is unknown: show, but store nothing.
  if (card.record && ov) saveChecklistRecord(ctx, card.record);
  if (!card.show) return null;
  const required = items.filter((item) => !item.optional);
  const done = required.filter((item) => item.done).length;
  const focus = (selector) => {
    const el = document.querySelector(selector);
    if (!el) return;
    el.classList.remove("collapsed");
    el.scrollIntoView({ block: "start", behavior: "smooth" });
    el.querySelector("button.primary")?.focus({ preventScroll: true });
  };
  const actions = {
    question: () => h("button", { class: "btn sm", type: "button", onclick: () => ctx.openCreate({ type: "question" }) },
      icon("plus", "sm"), t("home.start.question.action")),
    agent: () => h("button", { class: "btn sm", type: "button", onclick: () => focus(".start-panel") },
      icon("copy", "sm"), t("home.start.agent.action")),
    accept: () => ((ctx.S.needsYou || []).length
      ? h("button", { class: "btn sm", type: "button", onclick: () => focus(".needs-panel") }, t("home.start.accept.action"))
      : null),
    invite: () => h("button", { class: "btn sm ghost", type: "button", onclick: () => ctx.navigate("settings") },
      t("home.start.invite.action")),
  };
  const next = items.find((item) => !item.done && !item.optional);
  const panel = h("section", {
    class: `panel ov-checklist ${card.done ? "all-done" : ""}`.trim(), "aria-label": t("home.start.title"),
    dataset: { done: card.done ? "1" : "0" },
  });
  // Closing asks nothing but can be undone: an Undo toast, and Show getting
  // started at the top of Home afterwards.
  const close = h("button", {
    class: "icon-btn ov-checklist-close", type: "button", title: t("home.start.hide"), "aria-label": t("home.start.hide"),
    onclick: () => {
      saveChecklistRecord(ctx, dismissChecklist(loadChecklistRecord(ctx)));
      ctx.refresh();
      ctx.toast(t("home.start.hidden_toast"), "", {
        duration: 8000,
        action: { label: t("home.start.undo"), onClick: () => showChecklist(ctx) },
      });
    },
  }, icon("x", "sm"));
  panel.append(
    panelHead(t("home.start.title"),
      h("span", { class: "ov-checklist-count", dataset: { done: String(done), total: String(required.length) } },
        card.done ? t("home.start.all_done") : t("home.start.count", { done, total: required.length })),
      close),
    h("ol", { class: "ov-steps" }, items.map((item, index) => h("li", {
      class: `ov-step ${item.done ? "done" : ""} ${item === next ? "next" : ""}`.trim(),
      dataset: { step: item.id, done: item.done ? "1" : "0" },
    },
      h("span", { class: "ov-step-mark", "aria-hidden": "true" }, item.done ? icon("check", "sm") : String(index + 1)),
      h("span", { class: "ov-step-text" },
        h("b", null, t(`home.start.${item.id}.title`), item.optional ? h("small", null, ` · ${t("home.start.optional")}`) : null),
        h("span", { class: "muted" }, t(`home.start.${item.id}.hint`))),
      item.done ? h("span", { class: "ov-step-done" }, t("home.start.done")) : (actions[item.id]?.() || null)))));
  return panel;
}

// ------------------------------------------------------- tutorial banner
// ARK Studio Demo: the chapters (units tagged tutorial:chapter) one click
// from Home instead of three levels down in Research > Topics > Story, in
// the UI language (lang:zh chapters in 中文, the English ones otherwise).
// A chapter opens in the Read view, where its links to other nodes work;
// the tutorial topic (tutorial:topic) shows every chapter as one graph.
function tutorialBanner(ctx) {
  const chips = h("div", { class: "tutorial-chapters" });
  const graphLink = h("button", { class: "btn sm ghost tutorial-graph-link", type: "button", hidden: true },
    icon("graph", "sm"), t("home.tutorial.graph"));
  const banner = h("section", { class: "panel tutorial-banner", "aria-label": t("home.tutorial.title") },
    h("div", { class: "tutorial-head" },
      h("span", { class: "tutorial-copy" },
        h("b", null, t("home.tutorial.title")),
        h("span", { class: "muted" }, t("home.tutorial.body"))),
      graphLink),
    chips);
  api.units({ tags: "tutorial:chapter", limit: 40, slim: 1 }).then((data) => {
    const chapters = tutorialChapters(data?.items || data?.units || [], getLang());
    if (!chapters.length || !banner.isConnected) return;
    chips.append(...chapters.map((unit, index) => h("button", {
      class: "pill tutorial-chip", type: "button", dataset: { unitRef: unit.uid }, title: `${unit.uid} · ${unit.title}`,
      onclick: () => ctx.openUnit(unit.uid, null, { readOnly: true, revealPane: true }),
    }, h("b", null, String(index)), " ", String(unit.title || "").split(/[:：]/)[0])));
  }).catch(() => { /* the banner still names the tutorial */ });
  api.units({ tags: "tutorial:topic", limit: 10, slim: 1 }).then((data) => {
    const topic = tutorialTopic(data?.items || data?.units || [], getLang());
    if (!topic || !banner.isConnected) return;
    graphLink.hidden = false;
    graphLink.dataset.topicRef = topic.uid;
    graphLink.onclick = () => ctx.openTopic(topic.uid);
  }).catch(() => { /* chapters only */ });
  return banner;
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
        h("span", { class: "muted" }, `${t("overview.updated")} ${relativeTime(project.updated || new Date().toISOString())}`),
        ov?.graph ? h("span", { class: "dot-sep" }, "·") : null,
        ov?.graph ? homeFilterLine(ctx, ov.graph) : null,
        checklistRestorable({ project, record: loadChecklistRecord(ctx) })
          ? h("button", { class: "link hero-start-link", type: "button", "data-show-checklist": "", onclick: () => showChecklist(ctx) },
            t("home.start.show"))
          : null),
      h("div", { class: "hero-actions ov-actions" },
        h("button", { class: "primary-btn", type: "button", onclick: () => ctx.navigate("map") },
          icon("map", "sm"), t("overview.open_map")),
        h("button", { class: "ghost-btn hero-secondary", type: "button", onclick: () => editProject(ctx) },
          icon("edit", "sm"), t("overview.edit")),
        h("button", { class: "ghost-btn hero-secondary", type: "button", onclick: () => ctx.openCreate({ type: "idea" }) },
          icon("bulb", "sm"), t("ideas.capture")),
        // "New node" opens the full New dialog; Import stays a small icon.
        h("span", { class: "hero-more" },
          h("button", { class: "ghost-btn hero-secondary", type: "button", "data-home-new": "",
            onclick: () => ctx.openCreate({}) }, icon("plus", "sm"), t("action.new_node")),
          h("button", { class: "icon-btn", type: "button", "data-home-import": "", title: t("home.import"), "aria-label": t("home.import"),
            onclick: () => ctx.openImport() }, icon("upload", "sm"))))));
}

// The header graph is built once per project and kept across Home renders:
// every live-sync change re-renders Home, and a new GraphView each time
// restarted the force layout and its per-frame redraw behind the page (on a
// machine without GPU compositing that alone held the main thread busy).
// A re-render moves the same canvas into the new header and passes the new
// data in place (setData remote: positions kept, only new nodes settle).
// The view parks itself while a dialog is open, the tab is hidden or the
// header is off the page (graph.js backdropParked).
let hero = null; // { graph, stage, project, key }

function dropHero() {
  try { hero?.graph.destroy(); } catch { /* noop */ }
  hero = null;
}

function heroKey(nodes, edges) {
  const end = (value) => (value && typeof value === "object" ? value.id : value);
  return `${nodes.map((n) => `${n.id}:${n.status || ""}`).join(",")}|${edges.map((e) => `${end(e.source)}>${end(e.target)}`).join(",")}`;
}

function heroGraph(ctx, data) {
  const host = h("div", { class: "hero-graph", "aria-hidden": "true", inert: true });
  const project = ctx.S.project?.id || "";
  if (!data?.nodes?.length) { if (hero?.project === project) dropHero(); return host; }
  // The server sends the nodes of the Home filter newest first
  // (home-filter-model.js); the header simulates at most heroBudget of them.
  // An older server sent every node: the best connected are kept.
  const { nodes, edges } = data.filter
    ? heroGraphSample(data.nodes, data.edges || [], heroBudget(data.filter))
    : backdropSample(data.nodes, data.edges || []);
  const key = heroKey(nodes, edges);
  if (hero && hero.project === project) {
    host.appendChild(hero.stage);
    if (hero.key !== key) {
      hero.key = key;
      hero.graph.setData(nodes, edges, { remote: true });
    }
    // The new header is on the page after this render: continue then.
    const graph = hero.graph;
    requestAnimationFrame(() => graph.wakeBackdrop());
    return host;
  }
  dropHero();
  const stage = h("div", { class: "hero-graph-stage" });
  host.appendChild(stage);
  // After the page's first paint: the header decoration never delays Home.
  requestAnimationFrame(() => setTimeout(() => {
    if (!stage.isConnected || hero) return;
    const graph = new GraphView(stage, {
      compact: true, readOnly: true, persistLayout: false, backdrop: true,
    });
    graph.showLabels = false;
    graph.setData(nodes, edges);
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
      graph.running = false;
      cancelAnimationFrame(graph.frame);
    }
    hero = { graph, stage, project, key };
  }));
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

// M7: everything on this card is the same 14 days: the bars, the two
// totals and the Map filter it opens.
function trend(ctx, ov) {
  const days = ov.days || [];
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
    title: t("home.trend_title_14"),
    onclick: () => mapWith(ctx, { timeMode: "recent", recentDays: 14 }),
  },
    h("div", { class: "ov-trend-head" },
      h("span", { class: "ov-kpi-k" }, t("home.trend")),
      h("span", { class: "ov-legend" },
        h("span", { class: "ov-key created" }), t("home.created"),
        h("b", { dataset: { total: "created" } }, count(totalCreated)),
        h("span", { class: "ov-key done" }), t("home.completed"),
        h("b", { dataset: { total: "done" } }, count(totalDone)))),
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
  // A group with nothing in it is null; DOM append() would print "null".
  panel.append(...[
    group("overdue", t("home.kpi.overdue"), due.overdue_total, (due.overdue || []).slice(0, 3),
      (u) => h("span", { class: "overdue" }, t("home.days_late", { n: daysBetween(u.end_at, today) }))),
    group("soon", t("home.next_7"), due.soon_total, (due.soon || []).slice(0, 3),
      (u) => (u.end_at === today ? t("home.due_today") : t("home.due_in", { n: daysBetween(today, u.end_at) }))),
    group("blocked", t("home.kpi.blocked"), blocked.total, (blocked.items || []).slice(0, 3), blockerText),
  ].filter(Boolean));
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
    : null;
  // Agents working through the agent API (Claude Code, Codex, ...) are not
  // ARK sessions; show the ones that wrote in the last 30 minutes too (M5).
  const agentRows = runningAgentRows(ctx, ov.agents);
  const agentsBlock = agentRows.length
    ? h("div", { class: "ov-group agents", dataset: { group: "agents" } },
      h("div", { class: "ov-group-head" }, h("span", null, t("home.agents_writing")), h("b", null, count(agentRows.length))),
      agentRows)
    : null;
  const idle = !runsBlock && !agentsBlock ? h("div", { class: "ov-hint" }, icon("terminal", "sm"), t("home.runs_none")) : null;
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
  panel.append(...[agentsBlock, runsBlock, idle, gpuLine].filter(Boolean));
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
// The row, filters and dialogs are shared with Research › Review (needs-ui.js).
function needsYouPanel(ctx, all) {
  const projectKey = needsProjectKey(ctx);
  const expandedByProject = ctx.S.homeNeedsExpanded ||= {};
  if (!(projectKey in expandedByProject)) {
    try { expandedByProject[projectKey] = localStorage.getItem(`ark.needsExpanded:${projectKey}`) === "1"; }
    catch { expandedByProject[projectKey] = false; }
  }
  let expanded = expandedByProject[projectKey];
  // Items assigned to me first; otherwise the server order (decisions, newest).
  const me = String(ctx.S.overview?.me || currentUsername() || "").toLowerCase();
  const mine = (item) => Boolean(me) && String(item.assignee_effective || "").toLowerCase() === me;
  const sorted = sortMineFirst(all, me);
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
  const acceptable = bulkAcceptable(items);
  // Header actions are plain links, like every other Home panel.
  const panel = h("section", { class: "panel needs-panel", "aria-label": t("needs.title") },
    h("div", { class: "panel-head" },
      h("h3", { title: t("needs.hint") }, t("needs.title"),
        h("span", { class: `needs-count ${items.length ? "" : "zero"}`.trim(),
          title: filtered ? t("needs.filter.shown", { n: items.length, total: sorted.length }) : undefined },
        filtered ? `${items.length}/${sorted.length}` : String(items.length))),
      summary,
      h("span", { class: "needs-head-actions" },
        acceptable.length > 1 ? h("button", {
          class: "link needs-bulk", type: "button", dataset: { n: String(acceptable.length) },
          onclick: () => bulkAcceptDialog(ctx, acceptable),
        }, t("needs.bulk_accept")) : null,
        h("button", {
          class: "link needs-view-all", type: "button", title: t("needs.view_all_title"),
          onclick: () => ctx.navigateWorkspace("research", "review"),
        }, t("needs.view_all")))));
  // A setup layer waiting for this user's confirmation leads the list
  // (filled in once the setup state arrives; not a unit, so not filtered).
  panel.appendChild(setupNeedsRow({
    onShown: () => {
      const n = panel.querySelector(".needs-count");
      if (n && !filtered) { n.textContent = String(items.length + 1); n.classList.remove("zero"); }
      if (!sorted.length) panel.querySelector(":scope > .empty")?.remove();
    },
    onChange: () => ctx.refresh?.(),
  }));
  const setFilter = (next) => {
    saveNeedsFilter(ctx, projectKey, next);
    panel.replaceWith(needsYouPanel(ctx, all));
  };
  if (sorted.length) panel.appendChild(needsFilterBar(facets, filter, setFilter));
  if (!items.length) {
    if (!sorted.length) watchNeedsSeen(ctx, panel, null, [], { filtered: false });
    panel.appendChild(h("div", { class: "empty sm" }, sorted.length ? t("needs.filter.empty") : t("needs.empty"),
      sorted.length ? h("button", { class: "link needs-filter-clear", type: "button",
        onclick: () => { closeNeedsFilters(); setFilter(normalizeNeedsFilter({})); } }, t("filters.clear")) : null));
    return panel;
  }
  const list = h("div", { class: "needs-list" });
  panel.appendChild(list);
  // Rows are built when shown: the first NEEDS_LIMIT, and after "Show all"
  // the rest as the list is scrolled (progressive-list.js).  A row whose
  // item did not change is the same element as in the previous render (live
  // sync re-renders Home on every change), so its thumbnails are not loaded
  // again.
  // One card per item, per delivery of several units, per review group.
  const entries = groupNeeds(items);
  const row = (entry) => homeNeedsRow(ctx, projectKey, entry);
  list.append(...entries.slice(0, NEEDS_LIMIT).map(row));
  const rest = entries.slice(NEEDS_LIMIT);
  const shownRest = ctx.S.homeNeedsShown ||= {};
  let stop = null;
  const showRest = () => {
    stop?.();
    stop = fillProgressively(list, rest, row, {
      initial: NEEDS_CHUNK, chunk: NEEDS_CHUNK, previous: shownRest[projectKey] || 0,
      onCount: (n) => { shownRest[projectKey] = n; },
    });
  };
  const hideRest = () => {
    stop?.();
    stop = null;
    shownRest[projectKey] = 0;
    while (list.children.length > NEEDS_LIMIT) list.lastElementChild.remove();
  };
  pruneNeedsRows(projectKey, entries);
  watchNeedsSeen(ctx, panel, list, entries, { filtered });
  if (rest.length) {
    const more = h("button", { class: "link needs-toggle", type: "button", "aria-expanded": String(expanded),
      dataset: { open: expanded ? "1" : "0" } }, expanded ? t("needs.show_less") : t("needs.show_all", { n: items.length }));
    more.onclick = () => {
      expanded = !expanded;
      expandedByProject[projectKey] = expanded;
      try { localStorage.setItem(`ark.needsExpanded:${projectKey}`, expanded ? "1" : "0"); } catch { /* storage unavailable */ }
      if (expanded) showRest(); else hideRest();
      more.dataset.open = expanded ? "1" : "0";
      more.setAttribute("aria-expanded", String(expanded));
      more.textContent = expanded ? t("needs.show_less") : t("needs.show_all", { n: items.length });
    };
    panel.appendChild(more);
    if (expanded) showRest();
  }
  return panel;
}

// Rows shown before "Show all"; rows built at a time after it.
const NEEDS_LIMIT = 4;
const NEEDS_CHUNK = 20;
// Needs-you rows kept across Home renders, by everything the row shows.
const needsRowCache = new Map();

function itemKey(item) {
  return [item.uid, item.updated_at, item.status, item.type, item.title, item.kind,
    item.excerpt_plain || item.excerpt || "", item.comments, item.comments_open, item.assignee_effective, item.own_media,
    (item.media || []).map((m) => m.id), item.review_round, item.review_note, item.review_changes,
    item.review_prev, item.review_focus, item.round, (item.members || []).map(itemKey)];
}

// An entry of needs-model.js groupNeeds: one item or a delivery group.
function needsRowKey(projectKey, entry) {
  return JSON.stringify([projectKey, entry.kind === "group" ? entry.members.map(itemKey) : itemKey(entry.item)]);
}

function homeNeedsRow(ctx, projectKey, entry) {
  const key = needsRowKey(projectKey, entry);
  let row = needsRowCache.get(key);
  if (!row) {
    row = needsEntryCard(ctx, entry);
    needsRowCache.set(key, row);
  }
  return row;
}

// Forget rows of entries that left the list (or changed).
function pruneNeedsRows(projectKey, entries) {
  const live = new Set(entries.map((entry) => needsRowKey(projectKey, entry)));
  for (const key of needsRowCache.keys()) if (!live.has(key)) needsRowCache.delete(key);
}

// After an accept / send back / comment: redraw only this panel, keeping the
// row the user was looking at in place (no route change, no jump).
document.addEventListener(NEEDS_CHANGED, (event) => {
  const panel = document.querySelector(".needs-panel");
  const ctx = event.detail?.ctx;
  if (!panel || !ctx) return;
  const anchors = [...panel.querySelectorAll(".needs-row:not([hidden])")].map((row) => ({
    ref: row.dataset.unitRef, top: row.getBoundingClientRect().top, bottom: row.getBoundingClientRect().bottom,
  })).filter((row) => row.bottom > 58 && row.top < innerHeight);
  const replacement = needsYouPanel(ctx, ctx.S.needsYou);
  panel.replaceWith(replacement);
  const rows = [...replacement.querySelectorAll(".needs-row:not([hidden])")];
  const anchor = anchors.find((item) => rows.some((row) => row.dataset.unitRef === item.ref));
  if (anchor) {
    const row = rows.find((item) => item.dataset.unitRef === anchor.ref);
    window.scrollBy({ top: row.getBoundingClientRect().top - anchor.top, behavior: "instant" });
  }
});
