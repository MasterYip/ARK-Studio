// Run: node --test --experimental-vm-modules tests/js/sync_now.test.mjs
// Sync now in the top bar: the connection state, what the button shows, the
// double-click gate, the cache bypass after a click, the shortcut and the
// strings in both languages.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  SYNC_MIN_GAP_MS, connectionState, createSyncGate, syncButtonView,
} from "../../ark_studio/web/js/sync-now-model.js";
import { SYNC_NOW_WAIT_MS, servePolicy } from "../../ark_studio/web/js/client-cache-model.js";
import { DEFAULT_SHORTCUTS, SHORTCUT_ACTIONS, shortcutIsUsable } from "../../ark_studio/web/js/shortcuts.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const web = new URL("../../ark_studio/web/", import.meta.url);

test("connection state: offline wins, then live, polling, connecting", () => {
  assert.equal(connectionState({ live: true }), "live");
  assert.equal(connectionState({ live: false, polling: true }), "polling");
  assert.equal(connectionState({}), "connecting");
  assert.equal(connectionState({ live: true, offline: true, connectivity: "offline" }), "offline");
  assert.equal(connectionState({ offline: true, connectivity: "unreachable" }), "unreachable");
});

test("button: refresh icon at rest, spinning and disabled while syncing, a check when done", () => {
  const idle = syncButtonView({ phase: "idle", connection: "live" });
  assert.deepEqual([idle.icon, idle.spinning, idle.disabled, idle.dot], ["refresh", false, false, ""]);
  const busy = syncButtonView({ phase: "syncing", connection: "live" });
  assert.deepEqual([busy.icon, busy.spinning, busy.disabled, busy.busy], ["refresh", true, true, true]);
  const done = syncButtonView({ phase: "done", connection: "live" });
  assert.deepEqual([done.icon, done.spinning, done.disabled], ["check", false, false]);
});

test("button: a dot only when updates are not live, and the tooltip names the state", () => {
  for (const connection of ["polling", "connecting", "offline", "unreachable"]) {
    const view = syncButtonView({ connection });
    assert.equal(view.dot, connection);
    assert.deepEqual(view.lines[1], [`sync.state.${connection}`, {}]);
  }
  const live = syncButtonView({ connection: "live", shortcut: "Alt+R", lastSyncedAt: "10:42" });
  assert.deepEqual(live.lines, [
    ["sync.now_with_key", { key: "Alt+R" }],
    ["sync.state.live", {}],
    ["cache.last_sync", { time: "10:42" }],
  ]);
  assert.deepEqual(syncButtonView({}).lines[0], ["sync.now", {}]);
});

test("gate: a double click runs one sync, the second click gets the same promise", async () => {
  let runs = 0, release;
  const gate = createSyncGate(() => { runs += 1; return new Promise((resolve) => { release = resolve; }); });
  const first = gate.trigger();
  const second = gate.trigger();
  assert.equal(second, first);
  assert.equal(gate.busy, true);
  await Promise.resolve();
  release();
  await first;
  assert.equal(runs, 1);
  assert.equal(gate.busy, false);
});

test("gate: clicks right after a sync are ignored, later ones run again", async () => {
  let clock = 1000, runs = 0;
  const gate = createSyncGate(() => { runs += 1; }, { now: () => clock });
  await gate.trigger();
  clock += SYNC_MIN_GAP_MS - 1;
  assert.equal(gate.trigger(), null);
  clock += 2;
  await gate.trigger();
  assert.equal(runs, 2);
});

test("gate: a failed sync rejects and does not block the next one", async () => {
  let clock = 0, calls = 0;
  const gate = createSyncGate(() => { calls += 1; throw new Error("down"); }, { now: () => clock });
  await assert.rejects(gate.trigger(), /down/);
  clock += SYNC_MIN_GAP_MS;
  await assert.rejects(gate.trigger(), /down/);
  assert.equal(calls, 2);
});

test("after Sync now, entries checked before the click are read from the server", () => {
  const now = 50_000;
  const live = { live: true, liveSince: 1, now };
  const entry = { validatedAt: 10_000, data: {} };
  assert.equal(servePolicy(entry, {}, live), "fresh");
  assert.equal(servePolicy(entry, {}, { ...live, syncRequestedAt: 40_000 }), "network");
  // Checked again after the click: served from the cache as usual.
  assert.equal(servePolicy({ ...entry, validatedAt: 45_000 }, {}, { ...live, syncRequestedAt: 40_000 }), "fresh");
  // A click long ago no longer forces anything (an offline tab keeps its cache).
  assert.equal(servePolicy(entry, {}, { ...live, syncRequestedAt: now - SYNC_NOW_WAIT_MS }), "fresh");
});

test("Alt+R is the default Sync now shortcut, usable and free", () => {
  assert.equal(DEFAULT_SHORTCUTS.sync, "Alt+R");
  assert.ok(shortcutIsUsable("Alt+R", "sync"));
  const specs = Object.values(DEFAULT_SHORTCUTS);
  assert.equal(specs.filter((spec) => spec === "Alt+R").length, 1);
  assert.ok(SHORTCUT_ACTIONS.some((action) => action.id === "sync"));
});

test("strings exist in English and Chinese, without leftovers", () => {
  const keys = [
    "sync.now", "sync.now_with_key", "sync.failed", "sync.offline", "sync.unreachable",
    "sync.state.live", "sync.state.polling", "sync.state.connecting", "sync.state.offline", "sync.state.unreachable",
    "settings.sync_shortcut", "settings.sync_shortcut_hint", "cache.last_sync",
  ];
  for (const dict of [EN, ZH]) {
    for (const key of keys) {
      assert.equal(typeof dict[key], "string", key);
      assert.ok(dict[key].trim(), key);
      assert.doesNotMatch(dict[key], /TODO|TBD|\*\*|`/, key);
    }
  }
  assert.equal(EN["sync.now"], "Sync now");
  assert.equal(ZH["sync.now"], "立即同步");
  assert.match(EN["sync.now_with_key"], /\{key\}/);
  assert.match(ZH["sync.now_with_key"], /\{key\}/);
  assert.match(EN["sync.failed"], /\{error\}/);
  assert.match(ZH["sync.failed"], /\{error\}/);
});

test("the button sits in the top bar's right group and the modules are in the offline shell", async () => {
  const app = await readFile(new URL("js/app.js", web), "utf8");
  const actions = app.slice(app.indexOf('const actions = h("div", { class: "top-actions" }'));
  // Right after the search box, before Assistant (top-bar order).
  const sync = actions.indexOf("    sync,");
  assert.ok(sync > actions.indexOf('class: "search-btn top-ctl"') && actions.indexOf('class: "search-btn top-ctl"') > 0);
  assert.ok(sync < actions.indexOf('workspaceButton("assistant"'));
  assert.match(app, /const sync = syncTopButton\(\);/);
  const sw = await readFile(new URL("sw.js", web), "utf8");
  for (const file of ["sync-now.js", "sync-now-model.js"]) assert.match(sw, new RegExp(`"\\./js/${file.replace(".", "\\.")}"`));
});
