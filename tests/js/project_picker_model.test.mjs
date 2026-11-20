// Run: node --experimental-vm-modules --test tests/js/project_picker_model.test.mjs
// T-0034: project picker order (pins, hidden, personal activity), the pins
// and hidden projects store, and the grid's keyboard moves.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const web = new URL("../../ark_studio/web/js/", import.meta.url);
const source = await readFile(new URL("project-picker-model.js", web), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "project-picker-model.js" });
await module.link(() => { throw new Error("project-picker-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;

const P = (id, extra = {}) => ({ id, name: id.toUpperCase(), updated: "2026-01-01T00:00:00+00:00", ...extra });
const ids = (list) => list.map((p) => p.id);

test("order: personal activity first, then last activity, then name", () => {
  const projects = [
    P("quiet"),
    P("busy", { my_activity: 9.5, my_last_active_at: "2026-11-16T09:00:00+00:00" }),
    P("some", { my_activity: 1.2, my_last_active_at: "2026-11-10T09:00:00+00:00" }),
    P("fresh", { updated: "2026-11-15T00:00:00+00:00" }), // never touched by me: its own update time
    P("older-me", { my_activity: 0, my_last_active_at: "2026-09-01T00:00:00+00:00" }),
  ];
  assert.deepEqual(ids(m.arrangeProjects(projects, null).shown), ["busy", "some", "fresh", "older-me", "quiet"]);
});

test("ties: equal scores by last activity, then by name (case-insensitive), then id", () => {
  const at = "2026-11-16T09:00:00+00:00";
  const projects = [
    P("b", { name: "beta", my_activity: 2, my_last_active_at: at }),
    P("a", { name: "Alpha", my_activity: 2, my_last_active_at: at }),
    P("c", { name: "gamma", my_activity: 2, my_last_active_at: "2026-11-16T10:00:00+00:00" }),
    P("z2", { name: "same" }), P("z1", { name: "same" }),
  ];
  assert.deepEqual(ids(m.arrangeProjects(projects, {}).shown), ["c", "a", "b", "z1", "z2"]);
  // Bad or missing figures count as no activity.
  const odd = [P("x", { my_activity: "NaN" }), P("y", { my_activity: -3 }), P("w", { my_activity: 0.01 })];
  assert.deepEqual(ids(m.arrangeProjects(odd, {}).shown), ["w", "x", "y"]);
});

test("pinned projects come first in pin order; hidden ones go to their own list", () => {
  const projects = [
    P("busy", { my_activity: 5 }), P("pin2"), P("pin1"), P("mid", { my_activity: 1 }),
    P("gone", { my_activity: 8 }), P("gone2"),
  ];
  const { shown, hidden } = m.arrangeProjects(projects, { pinned: ["pin1", "pin2", "missing"], hidden: ["gone2", "gone"] });
  assert.deepEqual(ids(shown), ["pin1", "pin2", "busy", "mid"]);
  assert.deepEqual(ids(hidden), ["gone", "gone2"]); // by activity, not by hide order
  assert.deepEqual(m.arrangeProjects(null, null), { shown: [], hidden: [] });
});

test("prefs: normalized, and an id is never both pinned and hidden", () => {
  assert.deepEqual({ ...m.normalizeProjectPrefs({ pinned: ["a", "a", 3, "", "b", "c\n"], hidden: ["b"] }) },
    { pinned: ["a"], hidden: ["b"] });
  assert.deepEqual({ ...m.normalizeProjectPrefs("junk") }, { pinned: [], hidden: [] });
  const many = Array.from({ length: 250 }, (_, i) => `p${i}`);
  assert.equal(m.normalizeProjectPrefs({ pinned: many }).pinned.length, m.MAX_PROJECT_PREF_IDS);
  let prefs = m.togglePinned({}, "a");
  prefs = m.togglePinned(prefs, "b");
  assert.deepEqual(prefs.pinned, ["a", "b"]);
  prefs = m.toggleHidden(prefs, "a"); // hiding unpins
  assert.deepEqual({ ...prefs }, { pinned: ["b"], hidden: ["a"] });
  prefs = m.togglePinned(prefs, "a"); // pinning unhides, pinned last
  assert.deepEqual({ ...prefs }, { pinned: ["b", "a"], hidden: [] });
  prefs = m.togglePinned(prefs, "b");
  assert.deepEqual({ ...prefs }, { pinned: ["a"], hidden: [] });
  assert.equal(m.prefsEmpty(m.toggleHidden(m.toggleHidden(prefs, "z"), "z")), false);
  assert.equal(m.prefsEmpty({ pinned: [], hidden: [] }), true);
});

test("bootstrap lists keep the activity the last project list reported", () => {
  const previous = [P("a", { my_activity: 3, my_last_active_at: "x" }), P("b")];
  const next = [P("a", { name: "Renamed" }), P("b"), P("c")];
  const merged = m.withKnownActivity(next, previous);
  assert.equal(merged[0].name, "Renamed");
  assert.equal(merged[0].my_activity, 3);
  assert.equal(merged[0].my_last_active_at, "x");
  assert.equal("my_activity" in merged[1], false);
  assert.equal(merged[2], next[2]);
  // A list that already has the figures wins.
  const fresh = [P("a", { my_activity: 0, my_last_active_at: null })];
  assert.equal(m.withKnownActivity(fresh, previous)[0].my_activity, 0);
  assert.equal(m.withKnownActivity(undefined, previous), undefined);
});

function memoryStorage(initial = {}) {
  const data = new Map(Object.entries(initial));
  return { getItem: (k) => (data.has(k) ? data.get(k) : null), setItem: (k, v) => data.set(k, String(v)), data };
}

test("store: the account copy wins and every change is saved there", async () => {
  const storage = memoryStorage({ [m.PROJECT_PREFS_KEY]: JSON.stringify({ pinned: ["local-only"] }) });
  const saved = [];
  const store = m.createProjectPrefsStore({ storage, save: async (value) => { saved.push(value); return value || {}; } });
  store.init({ prefs: { projects: { pinned: ["acc"], hidden: [] } } });
  assert.equal(store.persisted(), true);
  assert.deepEqual(store.get().pinned, ["acc"]);
  assert.deepEqual(JSON.parse(storage.data.get(m.PROJECT_PREFS_KEY)).pinned, ["acc"]);
  const seen = [];
  store.subscribe((p) => seen.push(p.pinned.length));
  assert.equal(await store.update((p) => m.togglePinned(p, "x")), true);
  assert.deepEqual(saved.at(-1).pinned, ["acc", "x"]);
  await store.update((p) => m.togglePinned(m.togglePinned(p, "x"), "acc"));
  assert.equal(saved.at(-1), null, "no pins and nothing hidden clears the preference");
  assert.ok(seen.length >= 2);
});

test("store: without a savable profile the browser keeps the prefs", async () => {
  const storage = memoryStorage({ [m.PROJECT_PREFS_KEY]: JSON.stringify({ hidden: ["h"] }) });
  let calls = 0;
  const store = m.createProjectPrefsStore({ storage, save: async () => { calls += 1; return {}; } });
  store.init(null);
  assert.equal(store.persisted(), false);
  assert.deepEqual(store.get().hidden, ["h"]);
  await store.update((p) => m.toggleHidden(p, "h"));
  assert.equal(calls, 0);
  assert.deepEqual(JSON.parse(storage.data.get(m.PROJECT_PREFS_KEY)), { pinned: [], hidden: [] });
  // A read-only profile is not savable either; an empty account copy falls back to the browser.
  store.init({ permissions: { read_only: true }, prefs: { projects: { pinned: ["acc"] } } });
  assert.equal(store.persisted(), false);
  storage.setItem(m.PROJECT_PREFS_KEY, JSON.stringify({ pinned: ["b"] }));
  store.init({ prefs: {} });
  assert.deepEqual(store.get().pinned, ["b"]);
  // Broken browser storage is ignored.
  const broken = m.createProjectPrefsStore({ storage: { getItem() { throw new Error("x"); }, setItem() { throw new Error("x"); } } });
  broken.init(null);
  assert.equal(await broken.update({ pinned: ["a"] }), true);
  assert.deepEqual(broken.get().pinned, ["a"]);
});

test("store: a failed save rolls back and reports; a stale reply never overwrites a newer change", async () => {
  const errors = [];
  let fail = true;
  const store = m.createProjectPrefsStore({
    storage: memoryStorage(),
    save: async (value) => { if (fail) throw new Error("offline"); return value; },
    onError: (err) => errors.push(err.message),
  });
  store.init({ prefs: {} });
  assert.equal(await store.update((p) => m.togglePinned(p, "a")), false);
  assert.deepEqual(store.get().pinned, []);
  assert.deepEqual(errors, ["offline"]);

  fail = false;
  const replies = [];
  const slow = m.createProjectPrefsStore({
    storage: memoryStorage(),
    save: (value) => new Promise((resolve) => replies.push(() => resolve(value))),
  });
  slow.init({ prefs: {} });
  const first = slow.update((p) => m.togglePinned(p, "a"));
  const second = slow.update((p) => m.togglePinned(p, "b"));
  replies[1]();
  await second;
  replies[0]();
  await first;
  assert.deepEqual(slow.get().pinned, ["a", "b"]);
});

test("grid moves: arrows across rows and columns, no wrap, short last row", () => {
  // 3 columns, 8 cards: rows [0 1 2] [3 4 5] [6 7]
  const move = (i, key) => m.gridMove(i, key, 8, 3);
  assert.equal(move(0, "ArrowRight"), 1);
  assert.equal(move(2, "ArrowRight"), 3); // reading order continues on the next row
  assert.equal(move(7, "ArrowRight"), 7);
  assert.equal(move(0, "ArrowLeft"), 0);
  assert.equal(move(1, "ArrowDown"), 4);
  assert.equal(move(5, "ArrowDown"), 7); // into the shorter last row: its last card
  assert.equal(move(4, "ArrowDown"), 7);
  assert.equal(move(6, "ArrowDown"), 6); // already in the last row
  assert.equal(move(7, "ArrowUp"), 4);
  assert.equal(move(1, "ArrowUp"), 1);
  assert.equal(move(5, "Home"), 0);
  assert.equal(move(1, "End"), 7);
  assert.equal(move(1, "Enter"), -1);
  assert.equal(m.gridMove(0, "ArrowDown", 0, 3), -1);
  assert.equal(m.gridMove(-1, "ArrowDown", 4, 3), -1);
  // One column (phones): Up/Down walk the list.
  assert.equal(m.gridMove(1, "ArrowDown", 3, 1), 2);
  assert.equal(m.gridMove(2, "ArrowDown", 3, 1), 2);
  assert.equal(m.gridMove(1, "ArrowDown", 3, 0), 2);
});

test("grid columns come from the cards' row offsets", () => {
  assert.equal(m.columnsFromTops([10, 10, 10, 96, 96, 96, 182]), 3);
  assert.equal(m.columnsFromTops([10, 96, 182]), 1);
  assert.equal(m.columnsFromTops([10, 11, 96]), 2); // sub-pixel rounding
  assert.equal(m.columnsFromTops([]), 1);
});

test("the picker is wired into the menu, the Open dialog and the bootstrap", async () => {
  const app = await readFile(new URL("app.js", web), "utf8");
  const menu = app.slice(app.indexOf("function projectMenuSheet"), app.indexOf("function mobileMenuSheet"));
  assert.match(menu, /liveProjectPicker\(ctx,/);
  assert.match(menu, /withDesktopGroups\(list,/);
  assert.match(app, /initProjectPrefs\(data\.profile/);
  assert.match(app, /S\.projects = withKnownActivity\(data\.projects, S\.projects\)/);
  assert.match(app, /if \(contextMenuOpen\(\)\) return;/);
  const components = await readFile(new URL("components.js", web), "utf8");
  const modal = components.slice(components.indexOf("export async function openProjectModal"));
  assert.match(modal, /liveProjectPicker\(ctx, \{ beforeOpen: \(\) => close\(\) \}\)/);
  assert.match(modal, /withDesktopGroups\(list,/);
  const picker = await readFile(new URL("project-picker.js", web), "utf8");
  assert.match(picker, /event\.key === "ContextMenu" \|\| \(event\.shiftKey && event\.key === "F10"\)/);
  assert.match(picker, /api\.updateMe\(\{ prefs: \{ projects: value \} \}\)/);
  for (const file of ["i18n-en.js", "i18n-zh.js"]) {
    const strings = await readFile(new URL(file, web), "utf8");
    for (const key of ["pin", "unpin", "hide", "unhide", "hidden", "active", "updated"]) {
      assert.match(strings, new RegExp(`"project\\.picker\\.${key}":`), `${file} project.picker.${key}`);
    }
  }
});

test("a background refresh keeps the cards in place; new ones go last", () => {
  const shown = [P("b"), P("a"), P("c")];
  assert.equal(m.refreshedProjectRows(shown, [P("a"), P("b"), P("c")]), null, "same cards: nothing to redraw");
  const fresh = [P("new"), P("a", { name: "A2" }), P("c")];
  assert.deepEqual(ids(m.refreshedProjectRows(shown, fresh)), ["a", "c", "new"]);
  // A new activity time redraws (the time label) without moving the card.
  const later = [P("c"), P("a"), P("b", { my_last_active_at: "2026-11-16T10:00:00+00:00" })];
  assert.deepEqual(ids(m.refreshedProjectRows(shown, later)), ["b", "a", "c"]);
});

// ------------------------------------------- creator and latest writer on the cards
async function dictionary(file, name) {
  const src = await readFile(new URL(file, web), "utf8");
  const mod = new vm.SourceTextModule(src, { identifier: file });
  await mod.link(() => { throw new Error(`${file} must stay dependency-free`); });
  await mod.evaluate();
  const strings = mod.namespace[name];
  return (key, vars = {}) => {
    assert.ok(key in strings, `${file} lacks ${key}`);
    return strings[key].replace(/\{(\w+)\}/g, (_, k) => String(vars[k]));
  };
}
const zh = await dictionary("i18n-zh.js", "ZH");
const en = await dictionary("i18n-en.js", "EN");
const when = (value) => `<${value}>`; // stands in for relativeTime, unchanged
const UPDATED = "2026-11-18T10:00:00+00:00";

test("card people: creator and latest writer, zh and en", () => {
  const project = P("p", {
    updated: UPDATED, created_by_me: false,
    creator: { user: "MasterYip", name: "MasterYip" },
    last_actor: { user: "ChenZiyan", name: "ChenZiyan", agent: null, at: "2026-11-18T09:59:58+00:00" },
  });
  assert.deepEqual(
    (({ creator, time }) => ({ creator, time }))(m.cardPeopleText(project, { t: zh, when, me: "ana" })),
    { creator: "MasterYip 创建", time: `ChenZiyan 更新于 <${UPDATED}>` });
  assert.deepEqual(
    (({ creator, time }) => ({ creator, time }))(m.cardPeopleText(project, { t: en, when, me: "ana" })),
    { creator: "Created by MasterYip", time: `Updated <${UPDATED}> by ChenZiyan` });
  // Display names come from the directory when it knows the account.
  const named = m.cardPeopleText(project, { t: en, when, name: (user) => (user === "ChenZiyan" ? "Chen Ziyan" : user) });
  assert.equal(named.time, `Updated <${UPDATED}> by Chen Ziyan`);
});

test("card people: an agent shows its presence label; the viewer is 我 / you", () => {
  const byAgent = P("a", {
    updated: UPDATED, created_by_me: true, creator: { user: "MasterYip", name: "MasterYip" },
    last_actor: { user: "MasterYip", name: "MasterYip", agent: "MasterYip/视频字体", at: UPDATED },
  });
  const zhText = m.cardPeopleText(byAgent, { t: zh, when, me: "MasterYip" });
  assert.equal(zhText.creator, "我创建");
  assert.equal(zhText.time, `MasterYip/视频字体 更新于 <${UPDATED}>`, "an agent of mine is still named, not 我");
  assert.equal(zhText.people.updater.agent, "MasterYip/视频字体");
  const mine = P("b", { updated: UPDATED, last_actor: { user: "masteryip", name: "MasterYip", agent: null, at: UPDATED } });
  assert.equal(m.cardPeopleText(mine, { t: zh, when, me: "MasterYip" }).time, `我更新于 <${UPDATED}>`);
  assert.equal(m.cardPeopleText(mine, { t: en, when, me: "MasterYip" }).time, `Updated <${UPDATED}> by you`);
});

test("card people: unknown people fall back to the earlier footer", () => {
  // Legacy project: no creator, no latest writer.
  const legacy = P("l", { updated: UPDATED });
  assert.deepEqual((({ creator, time }) => ({ creator, time }))(m.cardPeopleText(legacy, { t: zh, when })),
    { creator: "", time: `更新于 <${UPDATED}>` });
  assert.equal(m.cardPeopleText(P("l2", { updated: UPDATED, my_last_active_at: UPDATED }), { t: en, when }).time,
    `You · <${UPDATED}>`);
  // A writer from before the latest change (an older list merged into a newer one) is not shown.
  const stale = P("s", { updated: UPDATED, last_actor: { user: "x", name: "x", agent: null, at: "2026-11-18T09:00:00+00:00" } });
  assert.equal(m.cardPeople(stale).updater, null);
  assert.equal(m.cardPeopleText(stale, { t: zh, when }).time, `更新于 <${UPDATED}>`);
});

test("Created by me: filters the grid and the hidden section; legacy and admin-only projects are not mine", () => {
  const projects = [
    P("mine", { created_by_me: true }),
    P("admin-only", { created_by_me: false, creator: { user: "ana", name: "ana" } }),
    P("legacy", { created_by_me: false }),
    P("mine-hidden", { created_by_me: true }),
    P("theirs-hidden", { created_by_me: false }),
  ];
  const prefs = m.setOnlyMine({ hidden: ["mine-hidden", "theirs-hidden"], pinned: ["admin-only"] }, true);
  assert.equal(prefs.only_mine, true);
  const { shown, hidden } = m.arrangeProjects(projects, prefs);
  assert.deepEqual(ids(shown), ["mine"]);
  assert.deepEqual(ids(hidden), ["mine-hidden"], "the Hidden (N) count follows the filter");
  // Off again: everything is back; pins and hidden ids are kept.
  const off = m.setOnlyMine(prefs, false);
  assert.deepEqual(off, { pinned: ["admin-only"], hidden: ["mine-hidden", "theirs-hidden"] });
  assert.equal(m.arrangeProjects(projects, off).shown.length, 3);
  // Nothing matches: an empty grid (the picker shows its empty line).
  assert.deepEqual(m.arrangeProjects([P("x", { created_by_me: false })], prefs).shown, []);
  // A list without creators (an older server) is not filtered.
  assert.deepEqual(ids(m.arrangeProjects([P("old")], prefs).shown), ["old"]);
});

test("Created by me: kept with the prefs (account and browser) and by pin / hide", () => {
  const prefs = m.setOnlyMine(null, true);
  assert.deepEqual(m.normalizeProjectPrefs(prefs), { pinned: [], hidden: [], only_mine: true });
  assert.equal(m.prefsEmpty(prefs), false, "the filter alone is worth saving");
  assert.equal(m.togglePinned(prefs, "a").only_mine, true);
  assert.equal(m.toggleHidden(prefs, "a").only_mine, true);
  assert.equal(m.normalizeProjectPrefs({ only_mine: "yes" }).only_mine, undefined);
  const storage = new Map();
  const store = m.createProjectPrefsStore({ storage: { getItem: (k) => storage.get(k) ?? null, setItem: (k, v) => storage.set(k, v) } });
  store.init(null);
  store.update((p) => m.setOnlyMine(p, true));
  assert.equal(JSON.parse(storage.get(m.PROJECT_PREFS_KEY)).only_mine, true);
  const again = m.createProjectPrefsStore({ storage: { getItem: (k) => storage.get(k) ?? null, setItem: () => {} } });
  again.init(null);
  assert.equal(again.get().only_mine, true);
});

test("bootstrap lists keep the creator and latest writer the last GET /api/projects gave", () => {
  const previous = [P("a", { my_activity: 1, creator: { user: "u", name: "u" }, created_by_me: true,
    last_actor: { user: "u", name: "u", agent: null, at: UPDATED } })];
  const merged = m.withKnownActivity([P("a")], previous)[0];
  assert.deepEqual(merged.creator, { user: "u", name: "u" });
  assert.equal(merged.created_by_me, true);
  assert.equal(merged.last_actor.user, "u");
  // A changed writer redraws the card without moving it.
  const shown = [P("a", { last_actor: { user: "u", at: UPDATED } })];
  assert.deepEqual(ids(m.refreshedProjectRows(shown, [P("a", { last_actor: { user: "v", at: UPDATED } })])), ["a"]);
});
