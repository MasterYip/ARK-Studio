import assert from "node:assert/strict";
import { test } from "node:test";
import { CONNECTION_POLL, connectionPollDelay, createConnectionPoller } from "../../ark_studio/web/js/home-model.js";

// Fake clock and timers: run() advances time and fires due timers in order.
function fakeTimers() {
  let clock = 0;
  let seq = 0;
  const due = new Map();
  return {
    now: () => clock,
    setTimeout: (fn, ms) => { seq += 1; due.set(seq, { at: clock + ms, fn }); return seq; },
    clearTimeout: (id) => { due.delete(id); },
    pending: () => due.size,
    async run(ms) {
      const end = clock + ms;
      for (;;) {
        const next = [...due.entries()].sort((a, b) => a[1].at - b[1].at)[0];
        if (!next || next[1].at > end) break;
        due.delete(next[0]);
        clock = next[1].at;
        next[1].fn();
        for (let i = 0; i < 5; i += 1) await Promise.resolve();
      }
      clock = end;
      for (let i = 0; i < 5; i += 1) await Promise.resolve();
    },
  };
}

const flush = async () => { for (let i = 0; i < 5; i += 1) await Promise.resolve(); };

test("poll delay grows to the cap and stops after the total time", () => {
  assert.equal(connectionPollDelay(0, 0), CONNECTION_POLL.firstMs);
  assert.ok(connectionPollDelay(1, 0) > CONNECTION_POLL.firstMs);
  assert.equal(connectionPollDelay(20, 0), CONNECTION_POLL.maxMs);
  assert.equal(connectionPollDelay(0, CONNECTION_POLL.totalMs), 0);
  assert.equal(connectionPollDelay(5, CONNECTION_POLL.totalMs - 1000), 0);
});

test("while waiting: bounded looks with backoff, one timer, nothing after ten minutes", async () => {
  const timers = fakeTimers();
  const calls = [];
  const poller = createConnectionPoller({ check: async (fresh) => { calls.push([timers.now(), fresh]); return true; },
    timers, now: timers.now });
  poller.start(timers.now());
  await flush();
  assert.equal(calls.length, 1);
  assert.equal(timers.pending(), 1);
  await timers.run(60 * 60000);
  // 15 s, 24 s, 38 s, then every 60 s until ten minutes: about a dozen looks, not 40.
  assert.ok(calls.length >= 8 && calls.length <= 14, `looks: ${calls.length}`);
  assert.ok(calls.every(([at]) => at <= CONNECTION_POLL.totalMs));
  const gaps = calls.slice(1).map(([at], i) => at - calls[i][0]);
  assert.ok(gaps.every((gap, i) => i === 0 || gap >= gaps[i - 1]), `gaps grow: ${gaps}`);
  assert.equal(timers.pending(), 0);
  assert.equal(poller.running, false);
});

test("stops on success, on stop() and when the line leaves the page", async () => {
  const timers = fakeTimers();
  let waiting = true;
  let calls = 0;
  const poller = createConnectionPoller({ check: async () => { calls += 1; return waiting; }, timers, now: timers.now });
  poller.start(0);
  await flush();
  await timers.run(16000);
  assert.equal(calls, 2);
  waiting = false;
  await timers.run(5 * 60000);
  assert.equal(calls, 3);
  assert.equal(timers.pending(), 0);

  let alive = true;
  calls = 0;
  const gone = createConnectionPoller({ check: async () => { calls += 1; return true; }, alive: () => alive,
    timers, now: timers.now });
  gone.start(timers.now());
  await flush();
  alive = false;
  await timers.run(10 * 60000);
  assert.equal(calls, 1);
  assert.equal(timers.pending(), 0);

  calls = 0;
  const stopped = createConnectionPoller({ check: async () => { calls += 1; return true; }, timers, now: timers.now });
  stopped.start(timers.now());
  await flush();
  stopped.stop();
  await timers.run(10 * 60000);
  assert.equal(calls, 1);
  assert.equal(timers.pending(), 0);
});

test("starting again replaces the loop; a check in flight from the old loop is ignored", async () => {
  const timers = fakeTimers();
  let calls = 0;
  let release;
  const slow = new Promise((resolve) => { release = resolve; });
  let first = true;
  const poller = createConnectionPoller({
    check: async () => { calls += 1; if (first) { first = false; await slow; } return true; },
    timers, now: timers.now,
  });
  poller.start(0); // in flight
  poller.start(0); // replaces it
  for (let i = 0; i < 5; i += 1) poller.start(0); // repeated Copy clicks
  await flush();
  release();
  await flush();
  assert.equal(timers.pending(), 1, "one timer, never one per click");
  const before = calls;
  await timers.run(15000);
  assert.equal(calls, before + 1);
  poller.stop();
});

test("a hidden tab fetches nothing and resumes once when shown; a copy older than ten minutes is looked at once", async () => {
  const timers = fakeTimers();
  let hidden = false;
  let listeners = 0;
  let show = null;
  let calls = 0;
  const poller = createConnectionPoller({
    check: async () => { calls += 1; return true; },
    hidden: () => hidden,
    onVisible: (cb) => { listeners += 1; show = cb; return () => { listeners -= 1; show = null; }; },
    timers, now: timers.now,
  });
  poller.start(0);
  await flush();
  hidden = true;
  await timers.run(5 * 60000);
  assert.equal(calls, 1);
  assert.equal(listeners, 1);
  assert.equal(timers.pending(), 0);
  hidden = false;
  show();
  await flush();
  assert.equal(calls, 2);
  assert.equal(listeners, 0);
  poller.stop();
  assert.equal(timers.pending(), 0);

  calls = 0;
  const late = createConnectionPoller({ check: async () => { calls += 1; return true; }, timers, now: timers.now });
  late.start(timers.now() - 11 * 60000, { fresh: false });
  await flush();
  await timers.run(30 * 60000);
  assert.equal(calls, 1);
  assert.equal(timers.pending(), 0);
});
