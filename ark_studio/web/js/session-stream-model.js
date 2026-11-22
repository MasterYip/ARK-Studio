// One live stream per agent session, shared by every view that shows it
// (the Sessions page and the Sessions sidebar view).  Dependency-free: the
// fetchers and the timer are injected (session-stream.js wires api.js).
//
//   subscribeSessions(fn, { active })  the session list, polled every
//       `interval` ms while at least one subscriber is active (one request
//       per tick, however many views listen)
//   subscribeTranscript(key, fn)       a transcript, refreshed on the list
//       tick while the session runs or its list entry changed.  `key` is a
//       session id, or transcriptKey(id, agent) for one sub-agent.  Only the
//       last `tail` items are fetched (setTail widens it); an unchanged
//       transcript costs one small reply (`known` signature).
//   transcript(id, { maxAge })         one coalesced fetch: concurrent callers
//       share the request in flight, and a result younger than `maxAge` ms
//       is reused
//   contexts                           context attached to the next message,
//       per session (a Map that notifies `subscribeContext` listeners)

export const SESSION_POLL_MS = 1600;
// Two views asking for the same transcript in one tick share one request.
export const TRANSCRIPT_SHARE_MS = 700;
// Items per transcript page; "Show earlier" asks for more.
export const TRANSCRIPT_TAIL = 200;

export function transcriptKey(id, agent = "") {
  return agent ? `${id}::${agent}` : String(id || "");
}

export function splitTranscriptKey(key) {
  const [id, agent = ""] = String(key || "").split("::");
  return { id, agent };
}

export function sessionEntrySignature(session) {
  if (!session) return "";
  return [session.id, session.status, session.last_activity_at, session.exit_code, session.error,
    session.counts?.stdout || 0, session.counts?.stderr || 0].join(":");
}

const ACTIVE = ["starting", "running", "idle", "stopping"];

class ContextMap extends Map {
  constructor(onChange) {
    super();
    this.onChange = onChange;
  }

  set(key, value) {
    super.set(key, value);
    this.onChange?.(key);
    return this;
  }

  delete(key) {
    const had = super.delete(key);
    if (had) this.onChange?.(key);
    return had;
  }
}

export function createSessionStream({
  listSessions, fetchTranscript, now = () => Date.now(),
  setTimer = (fn, ms) => setInterval(fn, ms), clearTimer = (id) => clearInterval(id),
  interval = SESSION_POLL_MS, shareMs = TRANSCRIPT_SHARE_MS,
} = {}) {
  const sessionListeners = new Set();     // { fn, active }
  const transcriptListeners = new Map();  // id -> Set(fn)
  const transcripts = new Map();          // key -> { data, at, tail }
  const tails = new Map();                // key -> items requested
  const inflight = new Map();             // id -> Promise
  const contextListeners = new Set();
  const entrySignatures = new Map();      // id -> list entry signature at the last tick
  let latest = null;                      // { sessions, groups, at }
  let listInflight = null;
  let timer = null;
  const stats = { listFetches: 0, transcriptFetches: 0 };

  const contexts = new ContextMap((id) => {
    for (const fn of [...contextListeners]) {
      try { fn(id); } catch { /* listener errors stay local */ }
    }
  });

  function anyActive() {
    for (const entry of sessionListeners) {
      try { if (entry.active()) return true; } catch { /* treat as inactive */ }
    }
    return false;
  }

  function startTimer() {
    if (timer == null && sessionListeners.size) timer = setTimer(tick, interval);
  }

  function stopTimer() {
    if (timer != null && !sessionListeners.size) {
      clearTimer(timer);
      timer = null;
    }
  }

  async function tick() {
    if (!anyActive()) return;
    try { await refreshSessions(); } catch { /* transient; keep polling */ }
  }

  function deliverSessions() {
    for (const entry of [...sessionListeners]) {
      try { entry.fn(latest); } catch { /* listener errors stay local */ }
    }
  }

  // Refresh the transcripts someone follows: running sessions every tick,
  // others when their list entry changed (a new turn finished, an error).
  function followTranscripts() {
    const byId = new Map((latest?.sessions || []).map((session) => [session.id, session]));
    for (const key of transcriptListeners.keys()) {
      const session = byId.get(splitTranscriptKey(key).id);
      const signature = sessionEntrySignature(session);
      const changed = entrySignatures.get(key) !== signature;
      entrySignatures.set(key, signature);
      if (!session) continue;
      if (changed || ACTIVE.includes(session.status)) transcript(key, { maxAge: shareMs }).catch(() => {});
    }
  }

  function refreshSessions() {
    if (listInflight) return listInflight;
    stats.listFetches += 1;
    listInflight = Promise.resolve()
      .then(() => listSessions())
      .then((data) => {
        latest = { sessions: data?.sessions || [], groups: data?.groups || [], project: data?.project || "", at: now() };
        deliverSessions();
        followTranscripts();
        return latest;
      })
      .finally(() => { listInflight = null; });
    return listInflight;
  }

  function subscribeSessions(fn, { active = () => true, immediate = true } = {}) {
    const entry = { fn, active };
    sessionListeners.add(entry);
    startTimer();
    if (immediate) {
      if (latest) {
        try { fn(latest); } catch { /* listener errors stay local */ }
      }
      if (!latest || now() - latest.at > interval) refreshSessions().catch(() => {});
    }
    return () => {
      sessionListeners.delete(entry);
      stopTimer();
    };
  }

  function notifyTranscript(id, data) {
    for (const fn of [...(transcriptListeners.get(id) || [])]) {
      try { fn(data); } catch { /* listener errors stay local */ }
    }
  }

  function transcript(id, { maxAge = 0 } = {}) {
    if (!id) return Promise.resolve(null);
    if (inflight.has(id)) return inflight.get(id);
    const cached = transcripts.get(id);
    const tail = tails.get(id) || TRANSCRIPT_TAIL;
    if (cached && cached.tail === tail && maxAge > 0 && now() - cached.at <= maxAge) return Promise.resolve(cached.data);
    stats.transcriptFetches += 1;
    // The signature we hold, when it is for the same page size.
    const known = cached && cached.tail === tail ? cached.data?.signature || "" : "";
    const request = Promise.resolve()
      .then(() => fetchTranscript(id, { tail, known }))
      .then((data) => {
        if (data?.unchanged && cached) {
          transcripts.set(id, { ...cached, at: now() });
          return cached.data;
        }
        const previous = transcripts.get(id)?.data;
        transcripts.set(id, { data, at: now(), tail });
        if (!previous || previous.signature !== data?.signature || !data?.signature || previous.offset !== data?.offset) notifyTranscript(id, data);
        return data;
      })
      .finally(() => { inflight.delete(id); });
    inflight.set(id, request);
    return request;
  }

  // Ask for (at least) `n` items of a transcript; the next fetch gets them.
  function setTail(key, n) {
    const next = Math.max(TRANSCRIPT_TAIL, Math.round(Number(n) || 0));
    if ((tails.get(key) || TRANSCRIPT_TAIL) >= next) return transcript(key, { maxAge: shareMs });
    tails.set(key, next);
    return transcript(key);
  }

  function subscribeTranscript(id, fn) {
    if (!id) return () => {};
    let set = transcriptListeners.get(id);
    if (!set) {
      set = new Set();
      transcriptListeners.set(id, set);
    }
    set.add(fn);
    const cached = transcripts.get(id);
    if (cached) {
      try { fn(cached.data); } catch { /* listener errors stay local */ }
    }
    entrySignatures.set(id, sessionEntrySignature((latest?.sessions || []).find((session) => session.id === splitTranscriptKey(id).id)));
    transcript(id, { maxAge: shareMs }).catch(() => {});
    return () => {
      const current = transcriptListeners.get(id);
      if (!current) return;
      current.delete(fn);
      if (!current.size) {
        transcriptListeners.delete(id);
        entrySignatures.delete(id);
      }
    };
  }

  function subscribeContext(fn) {
    contextListeners.add(fn);
    return () => contextListeners.delete(fn);
  }

  // Put a session's new state (a reply to send/stop) into the shared list.
  function patchSession(payload) {
    if (!payload?.id || !latest) return;
    const index = latest.sessions.findIndex((session) => session.id === payload.id);
    if (index < 0) latest = { ...latest, sessions: [payload, ...latest.sessions] };
    else {
      const sessions = latest.sessions.slice();
      sessions[index] = { ...sessions[index], ...payload };
      latest = { ...latest, sessions };
    }
    deliverSessions();
  }

  return {
    subscribeSessions,
    subscribeTranscript,
    subscribeContext,
    refreshSessions,
    transcript,
    setTail,
    patchSession,
    contexts,
    cachedTranscript: (id) => transcripts.get(id)?.data || null,
    latestSessions: () => latest,
    transcriptFollowers: (id) => transcriptListeners.get(id)?.size || 0,
    sessionFollowers: () => sessionListeners.size,
    polling: () => timer != null,
    tick,
    stats,
  };
}
