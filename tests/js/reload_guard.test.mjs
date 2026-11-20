// Run: node --test tests/js/reload_guard.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import {
  RELOAD_GUARD_KEY, RECORD_MAX_AGE_MS, decideStart, reopenTarget, safeUrl, stalledRecently, startReloadGuard,
} from "../../ark_studio/web/js/reload-guard.js";

function memoryStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => { map.set(key, String(value)); },
    removeItem: (key) => { map.delete(key); },
  };
}

// A document: location, longtask observer, pagehide/pageshow listeners.
function fakeWindow(href, { pendingLongTasks = [], perfNow = 60000, wasDiscarded = false, observer = true } = {}) {
  const url = new URL(href);
  const listeners = {};
  const win = {
    location: { pathname: url.pathname, search: url.search, hash: url.hash },
    document: { hidden: false, wasDiscarded },
    performance: { now: () => perfNow },
    setInterval: () => 0,
    addEventListener: (type, fn) => { (listeners[type] ||= []).push(fn); },
    fire: (type, event = {}) => (listeners[type] || []).forEach((fn) => fn(event)),
  };
  if (observer) {
    win.PerformanceObserver = class {
      constructor(callback) { this.callback = callback; }
      observe() {}
      takeRecords() { const out = pendingLongTasks.splice(0); return out; }
    };
  }
  return win;
}

test("a hang right before leaving makes the next load in the tab start safely", () => {
  const storage = memoryStorage();
  let now = 1_000_000;
  const first = fakeWindow("https://ark.test/app/?view=graph#/execute/sessions", {
    // The blocking task ended 1 s before pagehide; its entry is still queued.
    pendingLongTasks: [{ startTime: 30000, duration: 29000 }], perfNow: 60000,
  });
  assert.equal(startReloadGuard({ win: first, storage, now: () => now }).safe, false);
  first.fire("pagehide");
  const record = JSON.parse(storage.getItem(RELOAD_GUARD_KEY));
  assert.equal(record.stalled, true);
  assert.equal(record.open, false);

  now += 2000;
  const second = fakeWindow("https://ark.test/app/?view=graph#/execute/sessions");
  const decision = startReloadGuard({ win: second, storage, now: () => now });
  assert.deepEqual(decision, { safe: true, reason: "stalled", previousUrl: "/app/?view=graph#/execute/sessions" });
  // That load leaves normally: the one after it restores the view again.
  second.fire("pagehide");
  now += 2000;
  assert.equal(startReloadGuard({ win: fakeWindow("https://ark.test/app/#/map"), storage, now: () => now }).safe, false);
});

test("a normal reload, even after a short task or an old hang, restores the view", () => {
  const storage = memoryStorage();
  const now = 5_000_000;
  const first = fakeWindow("https://ark.test/#/map", {
    pendingLongTasks: [{ startTime: 100, duration: 900 }, { startTime: 1000, duration: 8000 }], perfNow: 120000,
  });
  startReloadGuard({ win: first, storage, now: () => now });
  first.fire("pagehide");
  assert.equal(JSON.parse(storage.getItem(RELOAD_GUARD_KEY)).stalled, false);
  assert.equal(startReloadGuard({ win: fakeWindow("https://ark.test/#/map"), storage, now: () => now + 1000 }).safe, false);
});

test("a document that never ran pagehide (crash, ended hung page) counts as unclean", () => {
  const storage = memoryStorage();
  startReloadGuard({ win: fakeWindow("https://ark.test/#/map"), storage, now: () => 10_000 });
  const decision = startReloadGuard({ win: fakeWindow("https://ark.test/#/map"), storage, now: () => 20_000 });
  assert.equal(decision.safe, true);
  assert.equal(decision.reason, "unclean");
  // A tab discarded by the browser to save memory also skips pagehide.
  const discarded = memoryStorage();
  startReloadGuard({ win: fakeWindow("https://ark.test/#/map"), storage: discarded, now: () => 10_000 });
  assert.equal(startReloadGuard({ win: fakeWindow("https://ark.test/#/map", { wasDiscarded: true }), storage: discarded, now: () => 20_000 }).safe, false);
});

test("decideStart: ?safe=1, stale records, bfcache and missing storage", () => {
  assert.deepEqual(decideStart(null, { now: 1, safeParam: true }), { safe: true, reason: "param", previousUrl: "" });
  assert.equal(decideStart({ open: true, at: 0 }, { now: RECORD_MAX_AGE_MS + 1 }).safe, false);
  assert.equal(decideStart({ stalled: true, at: 0 }, { now: RECORD_MAX_AGE_MS + 1 }).safe, false);
  assert.equal(decideStart({ open: false, stalled: false, at: 5 }, { now: 6 }).safe, false);
  assert.equal(decideStart({ open: "x", at: "bad" }, { now: 6 }).safe, false);
  // A page restored from the back/forward cache is open again.
  const storage = memoryStorage();
  const win = fakeWindow("https://ark.test/#/map");
  startReloadGuard({ win, storage, now: () => 1 });
  win.fire("pagehide");
  win.fire("pageshow", { persisted: true });
  assert.equal(JSON.parse(storage.getItem(RELOAD_GUARD_KEY)).open, true);
  // No sessionStorage, no PerformanceObserver: the page still starts.
  assert.equal(startReloadGuard({ win: fakeWindow("https://ark.test/", { observer: false }), storage: null, now: () => 1 }).safe, false);
});

test("stalledRecently needs one long task that ended shortly before leaving", () => {
  assert.equal(stalledRecently([{ startTime: 0, duration: 2999 }], 3000), false);
  assert.equal(stalledRecently([{ startTime: 0, duration: 3000 }], 3000), true);
  assert.equal(stalledRecently([{ startTime: 0, duration: 5000 }], 30000), false);
  assert.equal(stalledRecently([], 0), false);
});

test("safe start drops view parameters and opens Home; reopen keeps the previous address", () => {
  assert.equal(safeUrl("https://ark.test/app/?safe=1&lang=zh&unit=12&view=graph#/map"), "/app/?lang=zh#/overview");
  assert.equal(reopenTarget("/app/?safe=1#/execute/sessions", "https://ark.test/app/"), "/app/#/execute/sessions");
  assert.equal(reopenTarget("/app/?unit=12#/overview", "https://ark.test/app/"), "/app/?unit=12#/overview");
  assert.equal(reopenTarget("/app/#/overview", "https://ark.test/app/"), "");
  assert.equal(reopenTarget("/app/", "https://ark.test/app/"), "");
  assert.equal(reopenTarget("", "https://ark.test/app/"), "");
});

test("app.js applies the guard before reading the route and offers to reopen", async () => {
  const src = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  const guard = src.indexOf("const startGuard = startReloadGuard()");
  assert.ok(guard > 0 && guard < src.indexOf("init();"));
  assert.match(src, /history\.replaceState\(null, "", safeUrl\(location\.href\)\)/);
  assert.match(src, /if \(startGuard\.safe\) offerReopen\(\);/);
  const sw = await readFile(new URL("../../ark_studio/web/sw.js", import.meta.url), "utf8");
  assert.match(sw, /"\.\/js\/reload-guard\.js"/);
});
