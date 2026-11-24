// Run: node --experimental-vm-modules --test tests/js/filter_store.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const storeSource = await readFile(new URL("../../ark_studio/web/js/filter-store.js", import.meta.url), "utf8");

async function loadStore(context) {
  const module = new vm.SourceTextModule(storeSource, { identifier: "filter-store.js", context });
  await module.link(() => { throw new Error("filter-store must stay dependency-free"); });
  await module.evaluate();
  return module;
}

function memoryStorage() {
  const data = new Map();
  return {
    data,
    getItem: (key) => (data.has(key) ? data.get(key) : null),
    setItem: (key, value) => data.set(key, String(value)),
    removeItem: (key) => data.delete(key),
  };
}

// state.js wired to the real filter-store and a fake localStorage.
async function stateFixture(storage = memoryStorage()) {
  const context = vm.createContext({ URLSearchParams, location: { search: "" }, localStorage: storage, document: { documentElement: { dataset: {} } } });
  const store = await loadStore(context);
  const module = new vm.SourceTextModule(await readFile(new URL("../../ark_studio/web/js/state.js", import.meta.url), "utf8"), { context });
  await module.link((specifier) => {
    if (specifier === "./filter-store.js") return store;
    if (specifier === "./appearance.js") return new vm.SyntheticModule(["applyAppearance"], function () {
      this.setExport("applyAppearance", () => {});
    }, { context });
    return new vm.SyntheticModule(["getLang", "setLang"], function () {
      this.setExport("getLang", () => "en");
      this.setExport("setLang", (lang) => lang);
    }, { context });
  });
  await module.evaluate();
  const state = module.namespace;
  state.S.project = { id: "p1" };
  return { state, storage };
}

const m = (await loadStore(vm.createContext({}))).namespace;
const VERSION = 2;
const empty = () => ({ v: VERSION, q: "", tags: [], roots: false });
const normalize = (raw = {}) => ({
  v: VERSION, q: String(raw.q || ""), tags: Array.isArray(raw.tags) ? [...raw.tags] : [], roots: Boolean(raw.roots),
});
const plain = (value) => JSON.parse(JSON.stringify(value));

test("storage keys are per project, per tab and per kind", () => {
  assert.equal(m.storageKey("p1", "map"), "ark.filters.v1.p1.map");
  assert.equal(m.storageKey("p1", "plan/story", "default"), "ark.filters.v1.p1.plan%2Fstory.default");
  assert.equal(m.storageKey("", "map"), "ark.filters.v1._.map");
  assert.notEqual(m.storageKey("a.b", "map"), m.storageKey("a", "b.map"));
});

test("decode validates the model version and falls back on mismatch", () => {
  const opts = { version: VERSION, normalize };
  assert.deepEqual(plain(m.decodeFilters(m.encodeFilters({ q: "x" }, VERSION), opts)), { v: 2, q: "x", tags: [], roots: false });
  assert.deepEqual(plain(m.decodeFilters(JSON.stringify({ v: 1, filters: { q: "old" } }), opts)).q, "old", "older versions migrate");
  assert.equal(m.decodeFilters(JSON.stringify({ v: 3, filters: { q: "new" } }), opts), null, "newer versions are ignored");
  assert.equal(m.decodeFilters(JSON.stringify({ filters: { q: "x" } }), opts), null);
  assert.equal(m.decodeFilters("{not json", opts), null);
  assert.equal(m.decodeFilters(JSON.stringify({ v: 2, filters: null }), opts), null);
  assert.equal(m.decodeFilters(null, opts), null);
});

test("diffFilters ignores array order and version", () => {
  assert.deepEqual(plain(m.diffFilters({ tags: ["a", "b"] }, { tags: ["b", "a"], v: 1 }, normalize)), []);
  assert.deepEqual(plain(m.diffFilters({ q: "x", roots: true }, {}, normalize)), ["q", "roots"]);
  assert.ok(m.filtersEqual({}, empty(), normalize));
});

test("store remembers per tab, resolves user default, then built-in", () => {
  const storage = memoryStorage();
  const store = m.createFilterStore({ storage, empty, normalize, version: VERSION });
  assert.deepEqual(plain(store.load("p1", "map")), empty());
  store.save("p1", "map", { q: "map-only" });
  assert.equal(store.load("p1", "map").q, "map-only");
  assert.equal(store.load("p1", "ideas").q, "");
  assert.equal(store.load("p2", "map").q, "", "projects do not share filters");

  assert.equal(store.hasSavedDefault("p1", "ideas"), false);
  store.setDefault("p1", "ideas", { tags: ["t1"] });
  assert.deepEqual(plain(store.resolveDefault("p1", "ideas")).tags, ["t1"]);
  assert.deepEqual(plain(store.load("p1", "ideas")).tags, ["t1"], "unseen tab starts at its default");
  assert.deepEqual(plain(store.diffFromDefault("p1", "ideas", { tags: ["t1"] })), []);
  assert.deepEqual(plain(store.diffFromDefault("p1", "ideas", { tags: [] })), ["tags"]);
  store.clearDefault("p1", "ideas");
  assert.equal(store.hasSavedDefault("p1", "ideas"), false);
  assert.deepEqual(plain(store.resolveDefault("p1", "ideas")), empty());
  assert.ok(!storage.data.has("ark.filters.v1.p1.ideas.default"));
});

test("store survives broken storage via its in-memory mirror", () => {
  const broken = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("quota"); }, removeItem() { throw new Error("x"); } };
  const store = m.createFilterStore({ storage: broken, empty, normalize, version: VERSION });
  store.save("p", "map", { q: "kept" });
  assert.equal(store.load("p", "map").q, "kept");
  store.setDefault("p", "map", { q: "d" });
  store.clearDefault("p", "map");
  assert.equal(store.hasSavedDefault("p", "map"), false);
});

test("state.setTab keeps per-tab filters across switches and reloads", async () => {
  const { state, storage } = await stateFixture();
  state.setTab("map");
  state.S.filters.q = "kept";
  state.S.filters.tags = ["x"];
  state.setTab("plan", "story");
  assert.equal(state.S.filters.q, "", "Research tab starts clean");
  state.S.filters.q = "story-q";
  state.setTab("plan", "method");
  assert.equal(state.S.filters.q, "");
  state.setTab("map");
  assert.equal(state.S.filters.q, "kept");
  assert.deepEqual([...state.S.filters.tags], ["x"]);
  state.setTab("plan", "story");
  assert.equal(state.S.filters.q, "story-q");
  state.persistFilters();

  const reloaded = (await stateFixture(storage)).state;
  reloaded.setTab("map");
  assert.equal(reloaded.S.filters.q, "kept", "survives a reload");
});

test("state reset / set-as-default / restore built-in", async () => {
  const { state } = await stateFixture();
  state.setTab("map");
  assert.equal(state.filtersDifferFromDefault(), false);
  state.S.filters.q = "fav";
  assert.equal(state.filtersDifferFromDefault(), true);
  state.saveFiltersAsDefault();
  assert.equal(state.hasFilterDefault(), true);
  assert.equal(state.filtersDifferFromDefault(), false);
  state.S.filters.q = "other";
  state.S.filters.roots = true;
  assert.deepEqual(plain(state.filterDiffFromDefault()), ["q", "roots"]);
  state.resetFilters();
  assert.equal(state.S.filters.q, "fav");
  assert.equal(state.S.filters.roots, false);
  state.clearFilterDefault();
  assert.equal(state.hasFilterDefault(), false);
  assert.equal(state.filtersDifferFromDefault(), true, "fav now differs from the built-in default");
  state.resetFilters();
  assert.equal(state.S.filters.q, "");
  assert.equal(state.activeFilterCount(), 0);
});

test("a project switch does not leak filters into the new project", async () => {
  const { state } = await stateFixture();
  state.setTab("map");
  state.S.filters.q = "p1-only";
  state.S.project = { id: "p2" };
  state.setTab("map");
  assert.equal(state.S.filters.q, "");
  state.S.project = { id: "p1" };
  state.setTab("map");
  assert.equal(state.S.filters.q, "p1-only");
});

// ---- v3: assignee filter group and the v1/v2 -> v3 migration
test("migrateFilters adds an empty assignee group to v1/v2 saves", () => {
  const old = { q: "x", statuses: ["open"], tags: ["topic:a"] };
  assert.deepEqual(plain(m.migrateFilters(old, 2)), { ...old, assignees: [] });
  assert.deepEqual(plain(m.migrateFilters(old, 1)), { ...old, assignees: [] });
  const current = { q: "y", assignees: ["me"] };
  assert.deepEqual(plain(m.migrateFilters(current, 3)), current, "current saves pass through");
  assert.deepEqual(plain(old), { q: "x", statuses: ["open"], tags: ["topic:a"] }, "input is not mutated");
});

test("decode runs the migration only for older versions", () => {
  const seen = [];
  const migrate = (filters, from) => { seen.push(from); return { ...filters, migrated: true }; };
  const keep = (raw) => ({ ...raw });
  const opts = { version: 3, normalize: keep, migrate };
  assert.equal(m.decodeFilters(JSON.stringify({ v: 2, filters: { q: "a" } }), opts).migrated, true);
  assert.equal(m.decodeFilters(JSON.stringify({ v: 3, filters: { q: "a" } }), opts).migrated, undefined);
  assert.equal(m.decodeFilters(JSON.stringify({ v: 4, filters: { q: "a" } }), opts), null);
  assert.deepEqual(seen, [2]);
});

test("assignee values are cleaned and keywords canonicalized", () => {
  assert.deepEqual(plain(m.normalizeAssigneeValues(["ME", " alice ", "Unassigned", "ALICE", "", null])), ["me", "alice", "none"]);
  assert.deepEqual(plain(m.normalizeAssigneeValues("me,bob")), ["me", "bob"]);
  assert.deepEqual(plain(m.normalizeAssigneeValues(undefined)), []);
});

test("assigneeMatches uses the effective assignee", () => {
  const own = { assignee: "Alice", assignee_effective: "Alice" };
  const inherited = { assignee: null, assignee_effective: "bob" };
  const none = { assignee: null, assignee_effective: null };
  assert.equal(m.assigneeMatches(own, []), true);
  assert.equal(m.assigneeMatches(own, ["alice"]), true);
  assert.equal(m.assigneeMatches(inherited, ["bob"]), true);
  assert.equal(m.assigneeMatches(inherited, ["me"], "BOB"), true);
  assert.equal(m.assigneeMatches(inherited, ["me"], ""), false);
  assert.equal(m.assigneeMatches(none, ["none"]), true);
  assert.equal(m.assigneeMatches(own, ["none", "carol"]), false);
});

test("state loads v2 saved filters as v3 with an empty assignee group", async () => {
  const storage = memoryStorage();
  storage.setItem("ark.filters.v1.p1.map", JSON.stringify({ v: 2, filters: { q: "old", statuses: ["open"], roots: true } }));
  storage.setItem("ark.filters.v1.p1.map.default", JSON.stringify({ v: 2, filters: { q: "fav" } }));
  const { state } = await stateFixture(storage);
  assert.equal(state.FILTER_MODEL_VERSION, 3);
  state.setTab("map");
  assert.equal(state.S.filters.v, 3);
  assert.equal(state.S.filters.q, "old");
  assert.deepEqual(plain(state.S.filters.statuses), ["open"]);
  assert.equal(state.S.filters.roots, true);
  assert.deepEqual(plain(state.S.filters.assignees), []);
  assert.equal(state.hasFilterDefault(), true, "an old saved default still counts");
  state.S.filters.assignees = ["me", "none"];
  assert.deepEqual(plain(state.filtersToParams(state.S.filters)).assignee, ["me", "none"]);
  assert.equal(state.activeFilterCount(state.S.filters), 5, "q + status + roots + two assignee values");
  state.setTab("ideas");
  state.setTab("map");
  assert.deepEqual(plain(state.S.filters.assignees), ["me", "none"], "v3 saves round-trip");
  const saved = JSON.parse(storage.getItem("ark.filters.v1.p1.map"));
  assert.equal(saved.v, 3);
  assert.deepEqual(saved.filters.assignees, ["me", "none"]);
  state.resetFilters();
  assert.equal(state.S.filters.q, "fav");
  assert.deepEqual(plain(state.S.filters.assignees), []);
});

test("min links is part of the filter model: normalized, counted, sent and remembered per tab", async () => {
  const storage = memoryStorage();
  const { state } = await stateFixture(storage);
  assert.equal(state.emptyFilters().minLinks, 0);
  assert.equal(state.normalizeFilters({ minLinks: "3" }).minLinks, 3);
  assert.equal(state.normalizeFilters({ min_links: 2.9 }).minLinks, 2);
  assert.equal(state.normalizeFilters({ minLinks: -4 }).minLinks, 0);
  assert.equal(state.normalizeFilters({ minLinks: "abc" }).minLinks, 0);
  assert.equal(state.normalizeFilters({ q: "old v3 save" }).minLinks, 0, "older saves load with the filter off");
  state.setTab("map");
  assert.equal(state.activeFilterCount(state.S.filters), 0);
  assert.equal(plain(state.filtersToParams(state.S.filters)).min_links, undefined, "0 sends nothing");
  state.S.filters.minLinks = 3;
  state.persistFilters();
  assert.equal(state.activeFilterCount(state.S.filters), 1);
  assert.equal(plain(state.filtersToParams(state.S.filters)).min_links, 3);
  assert.deepEqual(plain(state.filterDiffFromDefault()), ["minLinks"]);
  state.setTab("ideas");
  assert.equal(state.S.filters.minLinks, 0, "each tab has its own value");
  state.setTab("map");
  assert.equal(state.S.filters.minLinks, 3, "the Map's value round-trips through storage");
  assert.equal(JSON.parse(storage.getItem("ark.filters.v1.p1.map")).filters.minLinks, 3);
  state.saveFiltersAsDefault();
  state.S.filters.minLinks = 0;
  state.resetFilters();
  assert.equal(state.S.filters.minLinks, 3, "a saved default keeps it");
  state.clearFilterDefault();
  state.resetFilters();
  assert.equal(state.S.filters.minLinks, 0);
});
