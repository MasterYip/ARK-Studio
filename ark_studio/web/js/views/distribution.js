import { h, clear, emptyState, fmtDate, dueState, typeColor, statusPill, tagChip, relativeTime } from "../dom.js";
import { t, typeLabel, statusLabel } from "../i18n.js";
import { icon } from "../icons.js";
import { GanttView, ganttControls } from "../gantt.js";
import { unitCard } from "../components.js";

const CHANNELS = ["x", "wechat", "web", "talk", "other"];

export function renderDistribution(host, ctx) {
  const S = ctx.S;
  clear(host);
  const outreach = (S.units || []).filter((u) => u.type === "outreach");
  const milestones = (S.units || []).filter((u) => u.type === "milestone");
  const channels = new Map(CHANNELS.map((c) => [c, []]));
  for (const unit of outreach) {
    const tag = (unit.tags || []).find((x) => x.startsWith("channel:"));
    const key = tag ? tag.split(":")[1] : "other";
    if (!channels.has(key)) channels.set(key, []);
    channels.get(key).push(unit);
  }
  const page = h("div", { class: "page" },
    h("div", { class: "page-head" },
      h("div", null,
        h("div", { class: "eyebrow" }, t("nav.output")),
        h("h1", { class: "page-title" }, t("distribution.title")),
        h("p", { class: "page-sub" }, t("distribution.subtitle"))),
      h("div", { class: "head-actions" },
        h("button", { class: "ghost-btn", onclick: () => ctx.openAssistant() }, icon("check", "sm"), t("assistant.title")),
        h("button", { class: "primary-btn", onclick: () => ctx.openCreate({ type: "outreach" }) }, icon("plus", "sm"), t("distribution.add")))),
    h("div", { class: "notice" }, icon("shield", "sm"), h("div", null, t("distribution.notice"))),
    h("section", null,
      h("div", { class: "section-head" }, h("h2", null, t("distribution.channels")),
        h("span", { class: "sub" }, `${outreach.length} ${t("common.results")}`)),
      outreach.length ? channelLanes(ctx, channels) : emptyState(t("distribution.empty"), "", h("button", { class: "btn primary", onclick: () => ctx.openCreate({ type: "outreach" }) }, t("distribution.add")))),
    h("div", { class: "two-col", style: { marginTop: "16px" } },
      h("section", null,
        h("div", { class: "section-head" }, h("h2", null, t("distribution.calendar")),
          h("span", { class: "sub" }, `${outreach.length + milestones.length}`)),
        calendar(ctx, S, [...outreach, ...milestones])),
      h("section", null,
        h("div", { class: "section-head" }, h("h2", null, t("plan.experiment.tasks")),
          h("span", { class: "sub" }, t("distribution.pending"))),
        pendingPanel(ctx, outreach))));
  host.appendChild(page);
}

function channelLanes(ctx, channels) {
  const wrap = h("div", { class: "channel-lanes" });
  for (const [key, items] of channels) {
    if (!items.length && key === "other") continue;
    const lane = h("div", { class: "channel-lane" });
    lane.appendChild(h("div", { class: "channel-head" },
      h("span", { style: { width: "7px", height: "7px", borderRadius: "2px", background: channelColor(key), display: "inline-block" } }),
      channelLabel(key),
      h("span", { class: "n" }, items.length)));
    const body = h("div", { class: "channel-body" });
    if (!items.length) body.appendChild(h("div", { class: "task-empty" }, "—"));
    for (const unit of items.sort((a, b) => (a.start_at || "9999").localeCompare(b.start_at || "9999"))) {
      const risk = (unit.tags || []).includes("risk:external");
      body.appendChild(h("div", { class: "outreach-card", dataset: { unitRef: unit.uid }, onclick: () => ctx.openUnit(unit.uid) },
        h("div", { class: "t" }, unit.title),
        h("div", { class: "d" },
          statusPill(unit.status),
          unit.start_at ? h("span", null, fmtDate(unit.start_at)) : h("span", { class: "muted" }, t("distribution.unscheduled"))),
        risk ? h("div", { class: "d", style: { color: "var(--amber)" } }, icon("shield", "sm"), t("distribution.pending")) : null));
    }
    lane.appendChild(body);
    wrap.appendChild(lane);
  }
  return wrap;
}

function calendar(ctx, S, items) {
  if (!items.some((u) => u.start_at || u.end_at)) return emptyState(t("gantt.no_dates"));
  const host = h("div");
  const gantt = new GanttView(host, { items, groupBy: "status", zoom: S.ganttZoom || 1, onSelect: (u) => ctx.openUnit(u.uid) });
  ctx.trackGantt(gantt);
  return host;
}

function pendingPanel(ctx, outreach) {
  const pending = outreach.filter((u) => ["inbox", "open"].includes(u.status) || (u.tags || []).includes("risk:external"));
  if (!pending.length) return h("div", { class: "empty sm" }, t("overview.no_actions"));
  const list = h("div", { class: "action-list" });
  for (const unit of pending) {
    list.appendChild(h("div", { class: "action-item", dataset: { unitRef: unit.uid }, onclick: () => ctx.openUnit(unit.uid) },
      h("span", { class: "ic" }, icon("send", "sm")),
      h("div", { class: "txt" },
        h("div", { class: "t" }, unit.title),
        h("div", { class: "d" }, `${statusLabel(unit.status)} · ${unit.start_at ? fmtDate(unit.start_at) : t("distribution.unscheduled")}`))));
  }
  return h("div", { class: "panel" }, h("div", { class: "panel-head" }, h("h3", null, t("distribution.pending"))), list);
}

function channelLabel(key) {
  const keyFor = { wechat: "distribution.channel.wechat", web: "distribution.channel.web", talk: "distribution.channel.talk", other: "distribution.channel.other" };
  if (key === "x") return "X / Twitter"; // i18n-ignore: brand name
  return keyFor[key] ? t(keyFor[key]) : key;
}
function channelColor(key) {
  return { x: "#e8eef6", wechat: "#07c160", web: "#60a5fa", talk: "#f59e0b", other: "#94a3b8" }[key] || "#94a3b8";
}
