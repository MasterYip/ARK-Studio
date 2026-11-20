// Run: node --experimental-vm-modules --test tests/js/home_filter.test.mjs
// "Home shows" (perf/home-large-projects): the per-person, per-project
// filter of the Home header graph, its persistence on the account, the
// header's node budget, and the pieces that keep Home fast on large projects
// (lazy needs rows, no eager videos, list thumbnails, no snapshot on Home).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import {
  DEFAULT_HOME_LIMIT, HERO_NODE_BUDGET, HOME_DAYS, HOME_LIMITS, defaultHomeFilter, heroBudget, heroGraphSample,
  homeFilterEntry, homeFilterFromPrefs, homeFilterIsDefault, homeFilterParams, homeGraphSummary, normalizeHomeFilter,
  sameHomeFilter,
} from "../../ark_studio/web/js/home-filter-model.js";
import { fillProgressively, initialRowCount, nextRowCount } from "../../ark_studio/web/js/progressive-list.js";

const web = new URL("../../ark_studio/web/", import.meta.url);
const read = (rel) => readFile(new URL(rel, web), "utf8");

test("the filter model: defaults, normalisation, saved entry, query", () => {
  assert.deepEqual(defaultHomeFilter(), { limit: 500, types: [], statuses: [], days: 0 });
  assert.equal(DEFAULT_HOME_LIMIT, 500);
  assert.deepEqual(HOME_LIMITS, [200, 500, 1000, 2000, 0]);
  assert.deepEqual(HOME_DAYS, [0, 7, 30, 90]);
  assert.deepEqual(normalizeHomeFilter({ limit: 200, types: ["task", "claim", "task", 3, ""], statuses: ["open"], days: 7, extra: 1 }),
    { limit: 200, types: ["claim", "task"], statuses: ["open"], days: 7 });
  // Invalid values fall back to the default, never throw.
  assert.deepEqual(normalizeHomeFilter({ limit: -1, days: 1.5, types: "task" }), defaultHomeFilter());
  assert.deepEqual(normalizeHomeFilter(null), defaultHomeFilter());
  assert.equal(normalizeHomeFilter({ limit: 0 }).limit, 0); // all
  assert.ok(homeFilterIsDefault({}));
  assert.ok(sameHomeFilter({ types: ["b", "a"] }, { types: ["a", "b"] }));
  // The default is stored as null (restores the server's default).
  assert.equal(homeFilterEntry(defaultHomeFilter()), null);
  assert.deepEqual(homeFilterEntry({ limit: 1000 }), { limit: 1000, types: [], statuses: [], days: 0 });
  assert.deepEqual(homeFilterParams({ limit: 200, types: ["task", "claim"], days: 30 }),
    { graph_limit: 200, graph_types: "claim,task", graph_statuses: "", graph_days: 30 });
  const prefs = { home_filter: { prj_a: { limit: 2000, types: [], statuses: ["review"], days: 0 } } };
  assert.equal(homeFilterFromPrefs(prefs, "prj_a").limit, 2000);
  assert.deepEqual(homeFilterFromPrefs(prefs, "prj_b"), defaultHomeFilter());
  assert.deepEqual(homeFilterFromPrefs(undefined, "prj_a"), defaultHomeFilter());
});

test("header graph: newest first within the budget, links among the kept nodes", () => {
  const nodes = Array.from({ length: 3000 }, (_, i) => ({ id: 3000 - i })); // server order: newest first
  const edges = [{ source: 3000, target: 2999 }, { source: 3000, target: 1 }, { source: { id: 2001 }, target: 2002 }];
  const sample = heroGraphSample(nodes, edges, 1000);
  assert.equal(sample.nodes.length, 1000);
  assert.equal(sample.nodes[0].id, 3000);
  assert.equal(sample.nodes.at(-1).id, 2001);
  assert.deepEqual(sample.edges, [edges[0], edges[2]]);
  // Old servers listed every node in id order: the newest are the last.
  assert.equal(heroGraphSample([{ id: 1 }, { id: 2 }, { id: 3 }], [], 2, { newestLast: true }).nodes[0].id, 2);
  // Small graphs pass through untouched.
  const small = heroGraphSample(nodes.slice(0, 10), edges, 1000);
  assert.equal(small.nodes.length, 10);
  assert.equal(small.edges, edges);
  assert.equal(HERO_NODE_BUDGET, 1000);
  assert.equal(heroBudget({ limit: 200 }), 200);
  assert.equal(heroBudget({ limit: 500 }), 500);
  assert.equal(heroBudget({ limit: 2000 }), 1000);
  assert.equal(heroBudget({ limit: 0 }), 1000);
});

test("graph summary: what Home says about its header graph", () => {
  const nodes = (n) => Array.from({ length: n }, (_, i) => ({ id: i }));
  const limited = homeGraphSummary({ nodes: nodes(500), total: 12340, matched: 12340, limit: 500, filter: { limit: 500 } });
  assert.deepEqual([limited.shown, limited.total, limited.limited, limited.narrowed], [500, 12340, true, false]);
  const small = homeGraphSummary({ nodes: nodes(80), total: 80, matched: 80, limit: 500, filter: { limit: 500 } });
  assert.equal(small.limited, false);
  const narrowed = homeGraphSummary({ nodes: nodes(40), total: 900, matched: 40, filter: { limit: 500, types: ["task"] } });
  assert.deepEqual([narrowed.shown, narrowed.narrowed, narrowed.limited], [40, true, true]);
  // Beyond the header's budget the drawn count is reported, not the sent one.
  const all = homeGraphSummary({ nodes: nodes(3000), total: 3000, matched: 3000, filter: { limit: 0 } });
  assert.deepEqual([all.shown, all.limited], [1000, true]);
  // An older server (no counts, no filter): nothing claimed as limited.
  assert.equal(homeGraphSummary({ nodes: nodes(10), edges: [] }).limited, false);
});

test("progressive lists: counts and building as the sentinel comes into view", () => {
  assert.equal(nextRowCount(0, 100, 25), 25);
  assert.equal(nextRowCount(90, 100, 25), 100);
  assert.equal(initialRowCount(100, 25, 0), 25);
  assert.equal(initialRowCount(100, 25, 60), 60); // a redraw keeps what was shown
  assert.equal(initialRowCount(10, 25, 60), 10);
  // Fake DOM + IntersectionObserver.
  let observer = null;
  const el = () => ({ children: [], appendChild(child) { this.children.push(...(child.children || [child])); return child; },
    after(node) { this.next = node; node.removed = false; }, remove() { this.removed = true; }, setAttribute() {} });
  const context = {
    document: { createDocumentFragment: el, createElement: el },
    IntersectionObserver: class { constructor(cb) { this.cb = cb; observer = this; } observe() {} unobserve() {} disconnect() { this.off = true; } },
  };
  const run = vm.runInNewContext(`(${fillProgressively.toString()})`, { ...context, initialRowCount, nextRowCount });
  const list = el();
  const counts = [];
  const items = Array.from({ length: 70 }, (_, i) => i);
  run(list, items, (item) => ({ item }), { initial: 30, chunk: 30, onCount: (n) => counts.push(n) });
  assert.equal(list.children.length, 30);
  observer.cb([{ isIntersecting: false }]);
  assert.equal(list.children.length, 30);
  observer.cb([{ isIntersecting: true }]);
  assert.equal(list.children.length, 60);
  observer.cb([{ isIntersecting: true }]);
  assert.equal(list.children.length, 70);
  assert.ok(observer.off, "stops once every row is built");
  assert.ok(list.next.removed, "the sentinel goes away");
  assert.deepEqual(counts, [30, 60, 70]);
});

async function loadProfile({ profile, storage = new Map(), saved = [] }) {
  const context = vm.createContext({
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v)), removeItem: (k) => storage.delete(k),
    },
    location: { search: "" }, window: { addEventListener() {}, matchMedia: () => null, dispatchEvent() {} },
    CustomEvent: class {}, setTimeout, clearTimeout, URLSearchParams,
  });
  const stub = (names, values = {}) => function () { for (const name of names) this.setExport(name, values[name] || (() => ({}))); };
  const module = new vm.SourceTextModule(await read("js/profile.js"), { context });
  await module.link(async (specifier, parent) => {
    const source = await read(`js/${specifier.slice(2)}`);
    if (specifier === "./home-filter-model.js") {
      const real = new vm.SourceTextModule(source, { context: parent.context });
      await real.link(() => { throw new Error("no imports"); });
      return real;
    }
    const names = [...new Set([...source.matchAll(/export (?:async )?(?:function\*?|class|const|let) (\w+)/g)].map((m) => m[1]))];
    if (specifier === "./api.js") {
      const api = { me: async () => ({ profile: null }), updateMe: async (body) => { saved.push(body); return { profile: { ...profile, prefs: { home_filter: body.prefs.home_filter } } }; } };
      return new vm.SyntheticModule(names, stub(names, { api }), { context: parent.context });
    }
    if (specifier === "./state.js") return new vm.SyntheticModule(names, stub(names, { S: {} }), { context: parent.context });
    return new vm.SyntheticModule(names, stub(names), { context: parent.context });
  });
  await module.evaluate();
  return module.namespace;
}

const plain = (value) => JSON.parse(JSON.stringify(value));

test("the filter is saved on the account per project; a read-only device keeps it in this browser", async () => {
  const saved = [];
  const profile = { prefs: {}, permissions: { read_only: false } };
  const account = await loadProfile({ profile, saved });
  account.initProfilePrefs(profile);
  assert.deepEqual(plain(account.homeFilterFor("prj_a")), defaultHomeFilter());
  const next = await account.saveHomeFilter("prj_a", { limit: 200, types: ["task"] });
  assert.equal(next.limit, 200);
  assert.deepEqual(plain(saved.at(-1)), { prefs: { home_filter: { prj_a: { limit: 200, types: ["task"], statuses: [], days: 0 } } } });
  assert.equal(account.homeFilterFor("prj_a").limit, 200);
  // The server reads the account copy: no query parameters needed.
  assert.deepEqual(plain(account.homeFilterQuery("prj_a")), {});
  // Back to the default: stored as null.
  await account.saveHomeFilter("prj_a", defaultHomeFilter());
  assert.deepEqual(plain(saved.at(-1)), { prefs: { home_filter: { prj_a: null } } });

  const storage = new Map();
  const device = await loadProfile({ profile: null, storage, saved: [] });
  device.initProfilePrefs({ prefs: {}, permissions: { read_only: true } });
  await device.saveHomeFilter("prj_b", { limit: 1000, days: 7 });
  assert.equal(JSON.parse(storage.get("ark.homeFilter:prj_b")).limit, 1000);
  assert.equal(device.homeFilterFor("prj_b").days, 7);
  assert.deepEqual(plain(device.homeFilterQuery("prj_b")), { graph_limit: 1000, graph_types: "", graph_statuses: "", graph_days: 7 });
  await device.saveHomeFilter("prj_b", defaultHomeFilter());
  assert.equal(storage.has("ark.homeFilter:prj_b"), false);
});

test("Home and Review stay fast on large projects (source checks)", async () => {
  const overview = await read("js/views/overview.js");
  // Header graph: the filter's nodes within the budget; Home says when a limit is active.
  assert.match(overview, /heroGraphSample\(data\.nodes, data\.edges \|\| \[\], heroBudget\(data\.filter\)\)/);
  assert.match(overview, /homeFilterLine\(ctx, ov\.graph\)/);
  // Needs you: four rows, the rest built as scrolled after "Show all"; rows kept across renders.
  const needs = overview.slice(overview.indexOf("function needsYouPanel"));
  assert.match(needs, /entries\.slice\(0, NEEDS_LIMIT\)\.map\(row\)/);
  assert.match(needs, /fillProgressively\(list, rest, row/);
  assert.match(needs, /needsRowCache\.get\(key\)/);
  assert.doesNotMatch(needs, /items\.map\(\(item, index\) => \{\s*const row = needsRow/);
  // Thumbnails: server previews, no video element with a src before it is on screen.
  const ui = await read("js/needs-ui.js");
  assert.doesNotMatch(ui, /EAGER_VIDEOS/);
  assert.match(ui, /"data-src": `\$\{src\}#t=0\.1`/);
  assert.doesNotMatch(ui, /h\("video", \{\s*src:/);
  assert.match(ui, /media\.thumb_url : media\.poster_url/);
  // Review: progressive rows.
  assert.match(await read("js/views/review.js"), /fillProgressively\(list, groupNeeds\(items\),/);
  // No megabyte graph snapshot on Home: not prefetched for big projects, not
  // loaded by the Local graph panel without a selected node.
  const app = await read("js/app.js");
  assert.match(app, /smallGraph \? \["\/api\/graph\/snapshot\?bodies=0"\] : \[\]/);
  assert.match(app, /api\.overview\(opts, homeFilterQuery\(S\.project\?\.id\)\)/);
  const local = await read("js/local-graph-view.js");
  assert.match(local, /if \(wantsCenter\(\)\) \{\s*try \{ await ensureSnapshot\(\); \}/);
  // Settings › Display has the same control.
  assert.match(await read("js/views/settings.js"), /display: \[appearancePanel\(\), \.\.\.\(ctx\.S\.project\?\.id \? \[homeFilterPanel\(ctx\)\] : \[\]\)\]/);
});

test("Home filter strings exist in both languages", async () => {
  const en = await read("js/i18n-en.js");
  const zh = await read("js/i18n-zh.js");
  const used = new Set();
  for (const file of ["js/home-filter-ui.js"]) {
    for (const [, key] of (await read(file)).matchAll(/t\("(home\.filter\.[a-z_]+)"/g)) used.add(key);
  }
  assert.ok(used.size >= 15);
  for (const key of used) {
    assert.ok(en.includes(`"${key}"`), `en ${key}`);
    assert.ok(zh.includes(`"${key}"`), `zh ${key}`);
  }
  assert.match(zh, /"home\.filter\.limited": "显示最新 \{n\} 个节点 · 共 \{total\} 个"/);
});
