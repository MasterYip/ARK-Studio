// Push channel for project changes (docs/development/client-cache.md).
//
// GET /api/events (server-sent events) announces every committed change with
// the new change cursor; the client then fetches GET /api/changes since the
// cursor of its cached snapshot and merges it.  If the stream cannot connect
// (a proxy blocks it) the old revision poll takes over.  ``denied`` and
// ``auth`` events report a revoked grant or an ended session.
import { api, applyDelta, authenticatedUrl, checkConnectivity, clientCache, syncState, tabProjectId } from "./api.js";
import { PROBE_MAX_MS, PROBE_MIN_MS, sseNextStep } from "./connectivity.js";

const POLL_MS = 3500;
const SNAPSHOT = "/api/graph/snapshot?bodies=0";

let source = null;
let pollTimer = 0;
let failures = 0;
let started = false;
let reconnectTimer = 0;
let reconnectDelay = PROBE_MIN_MS;
let knownCursor = "";
let knownRevision = "";
let pending = false;
let busy = false;
// The cache always follows the server; views decide themselves when to
// repaint (app.js waits while someone is editing).
let handlers = { onChange: () => {}, onDenied: () => {}, onAuth: () => {} };

function emit(name, detail) {
  window.dispatchEvent(new CustomEvent(name, { detail }));
}

function setLive(value) {
  if (syncState.live === value) return;
  syncState.live = value;
  if (value) syncState.liveSince = Date.now();
  emit("ark:live", { live: value });
}

/** Fetch and apply the changes since the cached snapshot (or announce one). */
async function pull(cursor, { own = false } = {}) {
  if (busy) { pending = true; return; }
  busy = true;
  try {
    const project = tabProjectId();
    const cached = clientCache.peek(SNAPSHOT, project) || await clientCache.get(SNAPSHOT, project);
    const since = cached?.cursor || knownCursor;
    if (since && since === cursor) {
      if (cached) clientCache.validated(SNAPSHOT, { project });
      knownCursor = cursor;
      return;
    }
    let delta = null;
    let changed = false;
    if (since) {
      delta = await api.changes(since, { lite: true });
      if (!delta.reset) changed = applyDelta(delta, project);
    }
    if (!since || delta?.reset) {
      // No cached snapshot to merge into, or too much changed: every cached
      // project entry is stale; views revalidate what they show.
      clientCache.markStale(() => true, project);
      changed = true;
    }
    knownCursor = delta?.cursor || cursor || knownCursor;
    if (changed) handlers.onChange({ delta, cursor: knownCursor, own });
  } catch {
    pending = true;
    setTimeout(() => retryPending(), 5000);
    return;
  } finally {
    busy = false;
  }
  // A notification that arrived while this pull ran.
  if (pending) setTimeout(() => retryPending(), 50);
}

function retryPending() {
  if (!pending) return;
  pending = false;
  pull(knownCursor);
}

function onRevision(data) {
  const ownWrite = Date.now() - Number(window.__ARK_LAST_WRITE_AT__ || 0) < 5000;
  const first = !knownRevision;
  const changed = data.revision !== knownRevision;
  knownRevision = data.revision;
  if (first) {
    // Connected: the cache is coherent from here on once it is checked.
    if (!knownCursor) knownCursor = data.cursor;
    pull(data.cursor);
    return;
  }
  if (!changed) return;
  // Our own writes still go through the delta so the cache stays in step;
  // the view is not rebuilt for them (the write path already updated it).
  pull(data.cursor, { own: ownWrite });
}

function connect() {
  if (typeof EventSource === "undefined") { startPolling(); return; }
  // presence=1: the agent presence frames too (presence.js, T-0043).
  const url = authenticatedUrl("/api/events?presence=1");
  source = new EventSource(url);
  let opened = false;
  const stream = source;
  source.onopen = () => { opened = true; failures = 0; reconnectDelay = PROBE_MIN_MS; };
  source.addEventListener("revision", (event) => {
    setLive(true);
    let data;
    try { data = JSON.parse(event.data); } catch { return; }
    onRevision(data);
  });
  // The agent sessions this account sees started, ended or were removed:
  // the cached session lists are stale; the Execute views refresh.
  let sessionsToken = null;
  source.addEventListener("sessions", (event) => {
    let data;
    try { data = JSON.parse(event.data); } catch { return; }
    const first = sessionsToken === null;
    const changed = data.token !== sessionsToken;
    sessionsToken = data.token;
    if (first || !changed) return;
    const project = tabProjectId();
    clientCache.markStale((route) => /^\/api\/(agent-sessions|session-contexts|worktrees|gpu\/dashboard)/.test(route), project);
    emit("ark:sessions-changed", { project });
  });
  // Which agent is on which node (the full live set each time).
  source.addEventListener("presence", (event) => {
    let data;
    try { data = JSON.parse(event.data); } catch { return; }
    emit("ark:presence", data);
  });
  source.addEventListener("denied", () => {
    stop();
    handlers.onDenied({ project: tabProjectId() });
  });
  source.addEventListener("auth", () => {
    stop();
    handlers.onAuth();
  });
  source.addEventListener("reconnect", () => {
    source?.close();
    setLive(false);
    knownRevision = "";
    setTimeout(connect, 200);
  });
  source.onerror = () => {
    if (source !== stream) return;
    setLive(false);
    knownRevision = "";
    if (!opened && stream.readyState === 2) failures += 1;
    const step = sseNextStep({ readyState: stream.readyState, opened, failures });
    if (step === "wait") return;
    stream.close();
    source = null;
    if (step === "poll") {
      // The stream never opened three times: a proxy is likely blocking it.
      startPolling();
      return;
    }
    // CLOSED (e.g. a proxy's 502/524): EventSource never retries by itself.
    scheduleReconnect();
  };
}

function scheduleReconnect() {
  clearTimeout(reconnectTimer);
  reconnectTimer = setTimeout(() => {
    reconnectTimer = 0;
    if (!started || source || pollTimer) return;
    checkConnectivity();
    connect();
  }, reconnectDelay);
  reconnectDelay = Math.min(PROBE_MAX_MS, reconnectDelay * 2);
}

function startPolling() {
  if (pollTimer) return;
  const tick = async () => {
    if (document.hidden) return;
    try {
      const data = await api.revision();
      onRevision(data);
    } catch { /* offline: retry next tick */ }
  };
  tick();
  pollTimer = window.setInterval(tick, POLL_MS);
  emit("ark:live", { live: false, polling: true });
}

function stop() {
  clearTimeout(reconnectTimer);
  reconnectTimer = 0;
  source?.close();
  source = null;
  if (pollTimer) {
    window.clearInterval(pollTimer);
    pollTimer = 0;
    emit("ark:live", { live: false, polling: false });
  }
  setLive(false);
}

/** Open a fresh stream unless one is live (also leaves polling to retry it). */
function reconnect() {
  if (!started || syncState.live) return;
  stop();
  failures = 0;
  reconnectDelay = PROBE_MIN_MS;
  knownRevision = "";
  connect();
}

export const liveSync = {
  start(options = {}) {
    handlers = { ...handlers, ...options };
    started = true;
    if (source || pollTimer) return;
    connect();
  },
  /** Project switched: reconnect for the new project. */
  restart() {
    stop();
    emit("ark:presence-reset", { project: tabProjectId() });
    knownRevision = "";
    knownCursor = "";
    connect();
  },
  stop() { started = false; stop(); },
  /** "Sync now": reconnect a stream that is down. */
  reconnect,
  retryPending,
  get pending() { return pending; },
  /** The stream is blocked and the revision poll runs instead. */
  get polling() { return Boolean(pollTimer); },
};

// The server is reachable again: open a fresh stream (an old one may be
// CLOSED for good) and catch up through the delta.
if (typeof window !== "undefined") {
  window.addEventListener("ark:reconnected", reconnect);
}
