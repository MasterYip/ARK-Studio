// Sub-agent and background-task chips above a session composer (shared by
// the Sessions page and the Sessions sidebar view): "3 sub-agents running",
// "2 background tasks", each hidden at zero.  A chip opens a list with two
// sections (sub-agents, background tasks; the clicked one first): status,
// elapsed time, last output line.  A sub-agent opens in its own tab
// (onOpenAgent), an ARK sub-session in the Sessions page (onOpenSession), a
// background task at its place in the conversation (onOpenItem).  update()
// rebuilds nothing unless the derived activity changed.

import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { sessionActivity, activityList, activitySignature } from "./session-activity-model.js";
import { fmtDuration } from "./session-thread-parts.js";

const STATUS_KEYS = {
  running: "session.activity.status_running", completed: "session.activity.status_completed",
  failed: "session.activity.status_failed", stopped: "session.activity.status_stopped",
};
export const KIND_ICON = { subagent: "sparkles", background: "terminal" };

export function elapsed(entry, now = Date.now()) {
  if (!entry?.startedAt) return "";
  return fmtDuration(entry.startedAt, entry.endedAt || new Date(now).toISOString());
}

export function statusLabel(status) {
  return t(STATUS_KEYS[status] || STATUS_KEYS.running);
}

/** One entry as a row (chip popover and the details rail). */
export function activityRow(entry, { onOpenItem, onOpenSession, onOpenAgent, compact = false } = {}) {
  const target = entry.sessionId ? () => onOpenSession?.(entry.sessionId)
    : entry.kind === "subagent" && entry.source === "tool" && onOpenAgent ? () => onOpenAgent(entry)
      : entry.itemId && onOpenItem ? () => onOpenItem(entry.itemId) : null;
  const meta = [entry.detail, entry.model, entry.background && entry.kind === "subagent" ? t("session.activity.background") : "",
    entry.tokens ? t("session.activity.tokens", { n: entry.tokens.toLocaleString() }) : ""].filter(Boolean).join(" · ");
  return h("button", {
    class: `sa-row ${entry.status} ${compact ? "compact" : ""}`.trim(), type: "button", disabled: !target,
    dataset: { activityKey: entry.key, activityKind: entry.kind },
    onclick: () => target?.(),
  },
    h("i", { class: `sa-dot ${entry.status}`.trim(), title: statusLabel(entry.status) }),
    h("span", { class: "sa-row-main" },
      h("span", { class: "sa-row-title" }, h("b", null, entry.label || entry.id || "—"),
        meta ? h("span", { class: "sa-row-kind" }, meta) : null),
      entry.lastOutput ? h("span", { class: "sa-row-last" }, entry.lastOutput) : null),
    h("span", { class: "sa-row-side" },
      h("span", { class: `sa-row-state ${entry.status}` }, statusLabel(entry.status)),
      h("span", { class: "sa-row-time", dataset: { activityTime: entry.key } }, elapsed(entry))));
}

export function createActivityChips({ onOpenItem = () => {}, onOpenSession = () => {}, onOpenAgent = null } = {}) {
  let activity = sessionActivity([]);
  let signature = "";
  let open = "";        // "", "subagent", "background"
  let ticker = null;

  const agentChip = h("button", { class: "sa-chip", type: "button", hidden: true, dataset: { activityChip: "subagent" },
    onclick: () => toggle("subagent") });
  const backgroundChip = h("button", { class: "sa-chip", type: "button", hidden: true, dataset: { activityChip: "background" },
    onclick: () => toggle("background") });
  const list = h("div", { class: "sa-pop", hidden: true, role: "dialog" });
  const el = h("div", { class: "sa-bar", hidden: true }, list, agentChip, backgroundChip);

  function chip(button, n, key, kind) {
    button.hidden = !n;
    clear(button);
    button.append(h("i", { class: "sa-dot running" }), icon(KIND_ICON[kind], "sm"),
      h("span", null, t(n === 1 ? key.replace(/_n$/, "_one") : key, { n })));
    button.classList.toggle("on", open === kind);
    button.setAttribute("aria-expanded", open === kind ? "true" : "false");
  }

  function toggle(which) {
    open = open === which ? "" : which;
    paint();
  }

  function close() {
    if (!open) return;
    open = "";
    paint();
  }

  const callbacks = {
    onOpenItem: (id) => { close(); onOpenItem(id); },
    onOpenSession: (id) => { close(); onOpenSession(id); },
    onOpenAgent: onOpenAgent ? (entry) => { close(); onOpenAgent(entry); } : null,
  };

  function section(kind) {
    const rows = activityList(activity, kind);
    if (!rows.length) return null;
    return h("section", { class: "sa-section", dataset: { activitySection: kind } },
      h("h5", null, icon(KIND_ICON[kind], "sm"),
        t(kind === "background" ? "session.activity.background_title" : "session.activity.subagents_title"),
        h("span", { class: "sa-count" }, rows.length)),
      rows.map((entry) => activityRow(entry, callbacks)));
  }

  function paintList() {
    clear(list);
    list.hidden = !open;
    if (!open) return;
    const order = open === "background" ? ["background", "subagent"] : ["subagent", "background"];
    list.append(h("div", { class: "sa-pop-head" },
      h("span", { class: "spacer" }),
      h("button", { class: "icon-btn", type: "button", title: t("action.close"), "aria-label": t("action.close"), onclick: close }, icon("x", "sm"))));
    const sections = order.map(section).filter(Boolean);
    if (!sections.length) list.append(h("div", { class: "sa-empty" }, t("session.activity.none")));
    list.append(...sections);
  }

  // Elapsed times of running entries tick while the list is open.
  function tick() {
    if (!open || !el.isConnected) { clearInterval(ticker); ticker = null; return; }
    for (const entry of activity.entries) {
      if (entry.status !== "running") continue;
      const node = list.querySelector(`[data-activity-time="${CSS.escape(entry.key)}"]`);
      if (node) node.textContent = elapsed(entry);
    }
  }

  function paint() {
    chip(agentChip, activity.runningSubagents, "session.activity.subagents_running_n", "subagent");
    chip(backgroundChip, activity.runningBackground, "session.activity.background_n", "background");
    el.hidden = !activity.runningSubagents && !activity.runningBackground && !open;
    paintList();
    if (open && !ticker) ticker = setInterval(tick, 1000);
  }

  document.addEventListener("pointerdown", (event) => {
    if (open && !el.contains(event.target)) close();
  }, true);

  return {
    el,
    /** New transcript items / session state; repaints only when the activity changed. */
    update(items, { session = null, sessions = [] } = {}) {
      const next = sessionActivity(items || [], { session, sessions });
      const sig = `${activitySignature(next)}#${next.runningSubagents}:${next.runningBackground}`;
      activity = next;
      if (sig === signature) return activity;
      signature = sig;
      paint();
      return activity;
    },
    relocalize() { signature = ""; paint(); },
    get activity() { return activity; },
  };
}
