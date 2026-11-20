// Pure rules behind Home's first-run surfaces (views/overview.js,
// views/onboarding.js, views/welcome.js): the agent connection state, the
// getting-started checklist and when the Welcome screen shows.  No DOM here,
// so tests/js/home_model.test.mjs covers them.

/** Agents that wrote within this many minutes count as running. */
export const LIVE_MINUTES = 30;
/** Below this many nodes (topics not counted) a project is "new". */
export const NEW_PROJECT_NODES = 15;
/** Label prefix of the tokens Home's Copy prompt mints (agent-tokens.js). */
export const STARTER_TOKEN_LABEL = "Home starter prompt"; // i18n-ignore: token label, matches agent-tokens.js HOME_PROMPT_LABEL

function time(value) {
  const ms = Date.parse(value || "");
  return Number.isNaN(ms) ? null : ms;
}

function latest(values) {
  return values.reduce((best, value) => (time(value) != null && (best == null || time(value) > time(best)) ? value : best), null);
}

/**
 * Where the user's agent stands for this project.
 *   agents: overview.agents ({first_at, recent: [{actor, writes, last_at}]})
 *   tokens: GET /api/personal/agent-tokens items, or null when unknown
 *   projectId: when set, only this project's starter tokens count (labels
 *     "Home starter prompt (<project>) – <day>", UX-18) plus the older
 *     unscoped ones ("Home starter prompt – <day>")
 * Returns {state, actor, writes, at, live}:
 *   "connected"  an agent wrote here (recent[0] is the latest one)
 *   "quiet"      an agent wrote here, but not in overview.agents.days
 *   "signed_in"  a starter token was used, nothing written here yet
 *   "waiting"    a starter token exists and was never used
 *   "none"       nothing yet
 */
export function isStarterToken(item, projectId = "") {
  const label = String(item?.label || "");
  if (!label.startsWith(STARTER_TOKEN_LABEL)) return false;
  if (!projectId) return true;
  const rest = label.slice(STARTER_TOKEN_LABEL.length);
  return !rest.startsWith(" (") || rest.startsWith(` (${projectId})`);
}

export function agentConnection({ agents = null, tokens = null, now = Date.now(), projectId = "" } = {}) {
  const recent = Array.isArray(agents?.recent) ? agents.recent : [];
  if (recent.length) {
    const top = recent[0];
    const at = top.last_at || null;
    const live = time(at) != null && now - time(at) < LIVE_MINUTES * 60000;
    const writes = recent.reduce((n, item) => n + (Number(item.writes) || 0), 0);
    return { state: "connected", actor: top.actor || "", writes, at, live, agents: recent.length };
  }
  if (agents?.first_at) return { state: "quiet", actor: "", writes: 0, at: agents.first_at, live: false, agents: 0 };
  const starters = (Array.isArray(tokens) ? tokens : [])
    .filter((item) => !item.expired && isStarterToken(item, projectId));
  const used = latest(starters.map((item) => item.last_used_at));
  if (used) return { state: "signed_in", actor: "", writes: 0, at: used, live: false, agents: 0 };
  if (starters.length) {
    return { state: "waiting", actor: "", writes: 0, at: latest(starters.map((item) => item.created_at)), live: false, agents: 0 };
  }
  return { state: "none", actor: "", writes: 0, at: null, live: false, agents: 0 };
}

/** Agents (activity actors agent:...) that wrote in the last LIVE_MINUTES. */
export function runningAgents(agents, now = Date.now()) {
  const recent = Array.isArray(agents?.recent) ? agents.recent : [];
  return recent.filter((item) => time(item.last_at) != null && now - time(item.last_at) < LIVE_MINUTES * 60000);
}

/**
 * After Copy prompt, Home looks whether the copied token was used yet.
 * The first look is FIRST_MS after the copy, then each wait grows by FACTOR
 * up to MAX_MS; nothing is fetched later than TOTAL_MS after the copy.
 */
export const CONNECTION_POLL = { firstMs: 15000, maxMs: 60000, factor: 1.6, totalMs: 10 * 60000 };

/** Wait before look number `attempt` (0-based), or 0 when the time is up. */
export function connectionPollDelay(attempt, elapsed, poll = CONNECTION_POLL) {
  const delay = Math.min(poll.maxMs, Math.round(poll.firstMs * poll.factor ** Math.max(0, attempt)));
  return elapsed + delay > poll.totalMs ? 0 : delay;
}

/**
 * One poll loop for the agent connection line (views/agent-connect.js).
 * check(fresh) resolves true while the state is still "waiting".
 * start(since) runs one check now, then repeats with connectionPollDelay
 * counted from `since` (the copy time).  The loop stops when check() says
 * done, when the time is up, when alive() is false (the line left the page)
 * or on stop(); while hidden() it fetches nothing and resumes once
 * onVisible(callback) fires (onVisible returns its unsubscribe).  Starting
 * again replaces the running loop, so there is never more than one.
 */
export function createConnectionPoller({
  check, alive = () => true, hidden = () => false, onVisible = null,
  timers = globalThis, now = () => Date.now(), poll = CONNECTION_POLL,
} = {}) {
  let timer = 0;
  let unwatch = null;
  let run = 0; // bumps on start/stop: a check still in flight is then ignored
  let attempt = 0;
  let since = 0;
  const halt = () => {
    if (timer) timers.clearTimeout(timer);
    timer = 0;
    if (unwatch) unwatch();
    unwatch = null;
  };
  const stop = () => { run += 1; halt(); };
  const schedule = (mine) => {
    const delay = connectionPollDelay(attempt, now() - since, poll);
    if (!delay) { stop(); return; }
    timer = timers.setTimeout(() => { timer = 0; tick(mine); }, delay);
  };
  const look = async (mine, fresh) => {
    let waiting = false;
    try { waiting = await check(fresh); } catch { waiting = false; }
    if (mine !== run) return;
    // alive() is asked when the timer fires: the first look may run before
    // the line is on the page.
    if (!waiting) { stop(); return; }
    schedule(mine);
  };
  const tick = (mine) => {
    if (mine !== run) return;
    if (!alive()) { stop(); return; }
    if (hidden()) {
      // No requests from a background tab; look again when it is shown.
      if (onVisible && !unwatch) {
        unwatch = onVisible(() => {
          if (unwatch) unwatch();
          unwatch = null;
          tick(mine);
        });
      }
      if (!onVisible) stop();
      return;
    }
    attempt += 1;
    look(mine, true);
  };
  return {
    start(startedAt = now(), { fresh = true } = {}) {
      stop();
      attempt = 0;
      since = Number.isFinite(startedAt) ? startedAt : now();
      look(run, fresh);
    },
    stop,
    get running() { return Boolean(timer || unwatch); },
  };
}

/** Nodes that are the user's content: every live node except topics. */
export function contentNodes(stats) {
  const total = Number(stats?.total) || 0;
  return Math.max(0, total - (Number(stats?.by_type?.topic) || 0));
}

/** A finished checklist stays this long after its last step was done. */
export const CHECKLIST_DONE_MS = 3 * 24 * 60 * 60 * 1000;

/**
 * The getting-started card on Home (below Connect your agent; the dashboard
 * always shows).  `record` is what was stored for this person and project
 * ({ since, completedAt, dismissed, expired }; ISO times); the result says
 * whether to show the card, whether every required step is done, and the
 * record to store (null: nothing to store).
 *
 * - Never on a demo project, or once dismissed or expired.
 * - Starts only on a new project (fewer than NEW_PROJECT_NODES nodes, topics
 *   not counted); once started (`since`) it stays until dismissed or done,
 *   however many nodes the agent adds.
 * - When every required step is done, `completedAt` is set; the card shows a
 *   done state and expires CHECKLIST_DONE_MS later.  A step undone before
 *   that clears `completedAt`.
 */
export function checklistCard({ project = null, stats = null, items = [], record = null, now = Date.now() } = {}) {
  const hidden = (rec = null) => ({ show: false, done: false, record: rec });
  if (!project?.id || project.demo) return hidden();
  const rec = { ...(record || {}) };
  if (rec.dismissed || rec.expired) return hidden();
  if (!rec.since) {
    if (!stats || contentNodes(stats) >= NEW_PROJECT_NODES) return hidden();
    rec.since = new Date(now).toISOString();
  }
  const required = (items || []).filter((item) => !item.optional);
  const done = required.length > 0 && required.every((item) => item.done);
  if (done) {
    rec.completedAt = time(rec.completedAt) != null ? rec.completedAt : new Date(now).toISOString();
    if (now - time(rec.completedAt) >= CHECKLIST_DONE_MS) {
      rec.expired = new Date(now).toISOString();
      return { show: false, done: true, record: rec };
    }
  } else delete rec.completedAt;
  const changed = JSON.stringify(rec) !== JSON.stringify(record || {});
  return { show: true, done, record: changed ? rec : null };
}

/** The record for a dismissed card (the close button). */
export function dismissChecklist(record = null, now = Date.now()) {
  return { ...(record || {}), dismissed: new Date(now).toISOString() };
}

/** Undo a dismissal (the toast's Undo, or Show getting started on Home). */
export function restoreChecklist(record = null) {
  const { dismissed, ...rest } = record || {};
  return rest;
}

/**
 * Offer "Show getting started" on Home?  Only for a card the person closed
 * on this (non-demo) project; a finished, expired card is not offered.
 */
export function checklistRestorable({ project = null, record = null } = {}) {
  return Boolean(project?.id && !project.demo && record?.dismissed && !record.expired);
}

/**
 * The checklist, each item ticked from data the server already has.
 *   stats: bootstrap stats (by_type, by_status)
 *   connection: agentConnection(...)
 *   team: whether accounts are on (the invite item is shown only then)
 *   members: overview members
 */
export function checklistItems({ stats = null, connection = null, team = false, members = [] } = {}) {
  const byType = stats?.by_type || {};
  const byStatus = stats?.by_status || {};
  const n = (key) => Number(byType[key]) || 0;
  const items = [
    { id: "question", done: n("question") + n("idea") > 0 },
    { id: "agent", done: ["connected", "quiet"].includes(connection?.state) },
    { id: "plan", done: n("task") + n("claim") > 0 },
    { id: "accept", done: (Number(byStatus.done) || 0) > 0 },
  ];
  if (team) items.push({ id: "invite", done: (members || []).filter((m) => m?.member).length > 1, optional: true });
  return items;
}

/**
 * Show the first-run Welcome?  Once per user (stored = this user's flag),
 * and only where it helps: the current project is an example (a fresh
 * install opens PredActor) or nearly empty (a server started without the
 * demo).  A person already working in a project of their own never sees it.
 */
export function shouldShowWelcome({ stored = null, project = null, stats = null } = {}) {
  if (stored) return false;
  if (!project) return false;
  return Boolean(project.demo) || contentNodes(stats) <= 1;
}

/**
 * The tutorial chapters (units tagged tutorial:chapter), in seq order, in
 * the UI language: chapters tagged lang:<lang> when there are any, else the
 * English ones (lang:en, or no lang: tag in tutorials built before zh).
 */
export function tutorialChapters(units, lang = "en") {
  const seq = (unit) => {
    const tag = (unit.tags || []).find((value) => String(value).startsWith("seq:"));
    return tag ? Number(String(tag).slice(4)) : 999;
  };
  const langOf = (unit) => {
    const tag = (unit.tags || []).find((value) => String(value).startsWith("lang:"));
    return tag ? String(tag).slice(5) : "en";
  };
  const chapters = (units || []).filter((unit) => (unit.tags || []).includes("tutorial:chapter"));
  const want = chapters.some((unit) => langOf(unit) === lang) ? lang : "en";
  return chapters
    .filter((unit) => langOf(unit) === want)
    .sort((a, b) => seq(a) - seq(b) || String(a.uid).localeCompare(String(b.uid)));
}

/** The tutorial topic graph (tagged tutorial:topic) in the UI language, else English; null if none. */
export function tutorialTopic(units, lang = "en") {
  const topics = (units || []).filter((unit) => (unit.tags || []).includes("tutorial:topic"));
  const has = (unit, value) => (unit.tags || []).includes(`lang:${value}`);
  return topics.find((unit) => has(unit, lang)) || topics.find((unit) => has(unit, "en")) || topics[0] || null;
}
