// Home: is the user's agent connected to this project?  One status line for
// the agent card (onboarding.js) and the agent rows of the Running card.
//
// Sources, no new polling of the project: the overview's "agents" member
// (agent writes, refreshed with Home whenever the live sync reports a change)
// and, only after this browser copied a prompt here and while no agent has
// written yet, the user's Home starter tokens (GET /api/personal/agent-tokens:
// was the copied token used?).  That one cheap request repeats with growing
// waits (15 s up to 60 s) while the card waits and the tab is visible, and
// stops ten minutes after the copy.
import { h, clear, relativeTime } from "../dom.js";
import { t } from "../i18n.js";
import { api } from "../api.js";
import { actorLabel } from "../components.js";
import { agentConnection, createConnectionPoller, runningAgents } from "../home-model.js";

const CACHE_MS = 15000;

let cache = null; // { at, tokens }
let pending = null; // the request in flight, shared by every caller

async function starterTokens(fresh = false) {
  if (!fresh && cache && Date.now() - cache.at < CACHE_MS) return cache.tokens;
  if (pending) return pending;
  pending = api.agentTokens()
    .then((data) => data.tokens || [], () => null)
    .then((tokens) => { cache = { at: Date.now(), tokens }; return tokens; })
    .finally(() => { pending = null; });
  return pending;
}

function onVisible(callback) {
  const listener = () => { if (!document.hidden) callback(); };
  document.addEventListener("visibilitychange", listener);
  return () => document.removeEventListener("visibilitychange", listener);
}

export function connectionText(c) {
  const when = c.at ? relativeTime(c.at) : "";
  if (c.state === "connected") {
    return t(c.agents > 1 ? "home.connect.connected_more" : "home.connect.connected",
      { agent: actorLabel(c.actor), n: c.writes, when, more: c.agents - 1 });
  }
  return t(`home.connect.${c.state}`, { when });
}

/**
 * The status line.  Returns {el, refresh}; refresh() re-reads the tokens
 * (after Copy minted one).
 */
export function connectionStatus(ctx) {
  const el = h("span", { class: "agent-conn", role: "status", "aria-live": "polite" });
  const agents = ctx.S.overview?.agents || null;
  let shown = "";
  const paint = (tokens) => {
    const c = agentConnection({ agents, tokens, projectId: ctx.S.project?.id || "" });
    const text = connectionText(c);
    const title = c.at ? new Date(c.at).toLocaleString() : "";
    // Rebuild only on a change: an unchanged line keeps its nodes (and the
    // dot's short pulse does not start over on every look).
    const key = `${c.state}|${c.live}|${text}|${title}`;
    if (key === shown) return c;
    shown = key;
    el.dataset.state = c.state;
    clear(el);
    el.append(h("span", { class: `agent-conn-dot ${c.live ? "live" : ""}`.trim(), "aria-hidden": "true" }),
      h("span", { class: "agent-conn-text", title: title || undefined }, text));
    return c;
  };
  // Waiting for the copied token's first use: look again with growing
  // waits, never from a hidden tab, and not after ten minutes from the copy
  // (home-model.js createConnectionPoller).  An agent's first write arrives
  // through the live sync (Home re-renders).
  const poller = createConnectionPoller({
    check: async (fresh) => paint(await starterTokens(fresh)).state === "waiting",
    alive: () => el.isConnected,
    hidden: () => document.hidden,
    onVisible,
  });
  const copiedKey = `ark.home.promptCopied:${ctx.S.project?.id || "local"}`;
  const first = paint(null);
  let copiedAt = null;
  try { copiedAt = localStorage.getItem(copiedKey); } catch { /* storage unavailable */ }
  // The line is built before it is on the page: the first look is now.
  if (first.state === "none" && copiedAt) {
    const since = Date.parse(copiedAt);
    poller.start(Number.isFinite(since) ? since : 0, { fresh: false });
  }
  const refresh = () => {
    if (first.state !== "none") return;
    // Already waiting for this copy's token (a reused token copied again):
    // keep the running look-ups and their timing.
    if (poller.running) return;
    const now = new Date();
    try { localStorage.setItem(copiedKey, now.toISOString()); } catch { /* storage unavailable */ }
    poller.start(now.getTime());
  };
  return { el, state: first.state, refresh, stop: poller.stop };
}

/** Rows for the Running card: agents that wrote in the last 30 minutes. */
export function runningAgentRows(ctx, agents) {
  return runningAgents(agents).map((item) => h("div", {
    class: "ov-row ov-agent", dataset: { agent: item.actor },
    title: `${item.actor} · ${new Date(item.last_at).toLocaleString()}`,
  },
    h("span", { class: "ov-dot live" }),
    h("span", { class: "ov-row-title" }, actorLabel(item.actor)),
    h("span", { class: "ov-row-d" }, t("home.agent_last", { when: relativeTime(item.last_at) }))));
}
