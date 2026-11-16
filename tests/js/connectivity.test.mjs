import assert from "node:assert/strict";
import test from "node:test";
import {
  PROBE_MAX_MS, PROBE_MIN_MS, createConnectivity, isProxyFailure, sseNextStep,
} from "../../ark_studio/web/js/connectivity.js";

function harness(results) {
  const timers = [];
  const changes = [];
  let probes = 0;
  const net = createConnectivity({
    probe: async () => {
      probes += 1;
      const next = results.shift();
      if (next instanceof Error) throw next;
      return next;
    },
    onChange: (change) => changes.push(change.state),
    setTimer: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearTimer: () => {},
  });
  return {
    net, timers, changes, get probes() { return probes; },
    async fire() { const timer = timers.shift(); timer.fn(); await net.check(); },
  };
}

test("network failure -> offline, probe backs off, success -> online", async () => {
  const h = harness([new Error("down"), new Error("down"), { status: 200, data: { ok: true } }]);
  h.net.failure("offline");
  assert.equal(h.net.state, "offline");
  assert.equal(h.timers[0].ms, PROBE_MIN_MS, "first probe after 2 s");
  await h.fire();
  assert.equal(h.net.state, "offline");
  assert.equal(h.timers.at(-1).ms, PROBE_MIN_MS * 2, "doubling backoff");
  await h.fire();
  await h.fire();
  assert.equal(h.net.state, "online");
  assert.deepEqual(h.changes, ["offline", "online"]);
  assert.equal(h.net.nextDelay, PROBE_MIN_MS, "backoff resets after recovery");
});

test("backoff is capped at 30 s", async () => {
  const h = harness(Array.from({ length: 12 }, () => new Error("down")));
  h.net.failure("offline");
  for (let i = 0; i < 10; i += 1) await h.fire();
  assert.equal(Math.max(...h.timers.map((t) => t.ms)), PROBE_MAX_MS);
});

test("any successful request ends the offline state without waiting for the probe", () => {
  const h = harness([]);
  h.net.failure("offline");
  h.net.success();
  assert.equal(h.net.state, "online");
  assert.equal(h.probes, 0);
});

test("a proxy answer is 'unreachable', not offline, and recovers on the next probe", async () => {
  const h = harness([{ status: 524, data: null }, { status: 200, data: { ok: true } }]);
  h.net.failure("unreachable");
  assert.equal(h.net.state, "unreachable");
  assert.equal(h.net.offline, true);
  await h.fire();
  assert.equal(h.net.state, "unreachable");
  await h.fire();
  assert.equal(h.net.state, "online");
});

test("check() on demand (online event, focus, a write) recovers immediately and is deduplicated", async () => {
  const h = harness([{ status: 200, data: { app: "arkstudio" } }]);
  h.net.failure("offline");
  const [a, b] = [h.net.check(), h.net.check()];
  assert.equal(await a, true);
  assert.equal(await b, true);
  assert.equal(h.probes, 1);
  assert.equal(h.net.state, "online");
});

test("proxy statuses are told apart from ARK errors", () => {
  assert.equal(isProxyFailure(524, null), true);
  assert.equal(isProxyFailure(502, { raw: "<html>Bad gateway</html>" }), true);
  assert.equal(isProxyFailure(503, { ok: false, error: { message: "GPU queue missing" } }), false,
    "an ARK JSON error is a real answer");
  assert.equal(isProxyFailure(500, null), false);
  assert.equal(isProxyFailure(404, null), false);
});

test("SSE errors: browser retries, CLOSED streams reconnect, never-opened streams fall back to polling", () => {
  assert.equal(sseNextStep({ readyState: 0, opened: true, failures: 0 }), "wait");
  // A proxy's 524 closes the stream for good: open a new one.
  assert.equal(sseNextStep({ readyState: 2, opened: true, failures: 0 }), "reconnect");
  assert.equal(sseNextStep({ readyState: 2, opened: false, failures: 1 }), "reconnect");
  assert.equal(sseNextStep({ readyState: 2, opened: false, failures: 3 }), "poll");
});
