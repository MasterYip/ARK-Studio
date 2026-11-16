// Reachability of the ARK server (docs/development/client-cache.md).
//
// "online": requests work.  "offline": a request failed without any response
// (no network, DNS, connection refused).  "unreachable": a proxy in front of
// ARK answered instead of it (502/503/504/52x without an ARK error body,
// e.g. a Cloudflare tunnel blip).  Both non-online states show cached data
// read-only.  They are left by a successful request or by the probe, which
// re-checks GET /api/health with backoff (2 s doubling to 30 s) and at once
// when the browser reports online, the tab becomes visible or gains focus.
// navigator.onLine is never trusted on its own: it is often wrong behind
// proxies and VPNs.

export const PROXY_STATUSES = new Set([502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 530]);
export const PROBE_MIN_MS = 2000;
export const PROBE_MAX_MS = 30000;

/** A response came from a proxy rather than from ARK. */
export function isProxyFailure(status, data) {
  return PROXY_STATUSES.has(Number(status)) && data?.ok !== false;
}

/**
 * The state machine, with injectable probe and timers for tests.
 * probe() resolves to {status, data} or rejects on a network failure.
 */
export function createConnectivity({
  probe, onChange = () => {}, setTimer = (fn, ms) => setTimeout(fn, ms),
  clearTimer = (id) => clearTimeout(id), now = () => Date.now(),
} = {}) {
  let state = "online";
  let delay = PROBE_MIN_MS;
  let timer = 0;
  let probing = null;
  let lastOnline = now();

  const set = (next) => {
    if (next === state) return;
    const previous = state;
    state = next;
    if (next === "online") lastOnline = now();
    onChange({ state, previous, offline: state !== "online" });
  };

  const schedule = () => {
    if (timer) clearTimer(timer);
    timer = setTimer(() => { timer = 0; check(); }, delay);
    delay = Math.min(PROBE_MAX_MS, delay * 2);
  };

  function success() {
    if (timer) clearTimer(timer);
    timer = 0;
    delay = PROBE_MIN_MS;
    set("online");
  }

  function failure(kind = "offline") {
    set(kind === "unreachable" ? "unreachable" : "offline");
    if (!timer && !probing) schedule();
  }

  /** Probe now (deduplicated); resolves to true when the server answered. */
  function check() {
    if (probing) return probing;
    probing = (async () => {
      try {
        const { status, data } = await probe();
        if (isProxyFailure(status, data)) {
          set("unreachable");
          schedule();
          return false;
        }
        success();
        return true;
      } catch {
        set("offline");
        schedule();
        return false;
      } finally {
        probing = null;
      }
    })();
    return probing;
  }

  return {
    get state() { return state; },
    get offline() { return state !== "online"; },
    get nextDelay() { return delay; },
    get lastOnline() { return lastOnline; },
    success, failure, check,
  };
}

/**
 * What the push channel does after an EventSource error.  "wait": the
 * browser is reconnecting by itself (CONNECTING).  "reconnect": the stream is
 * CLOSED, which EventSource never retries (a proxy answered 502/524, or the
 * server refused): open a new one after a backoff and re-check reachability.
 * "poll": the stream never opened in three attempts; a proxy likely blocks
 * it, so fall back to polling.
 */
export function sseNextStep({ readyState, opened, failures }) {
  if (readyState !== 2) return "wait";
  if (!opened && failures >= 3) return "poll";
  return "reconnect";
}
