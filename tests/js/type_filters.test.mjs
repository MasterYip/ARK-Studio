// Run: node --experimental-vm-modules --test tests/js/type_filters.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function fixture() {
  const context = vm.createContext({
    URLSearchParams, location: { search: "" },
    localStorage: { getItem() { return null; } },
    document: { documentElement: { dataset: {} } },
  });
  const module = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/state.js", import.meta.url), "utf8"), { context },
  );
  const store = new vm.SourceTextModule(
    await readFile(new URL("../../ark_studio/web/js/filter-store.js", import.meta.url), "utf8"), { context },
  );
  await store.link(() => { throw new Error("filter-store must stay dependency-free"); });
  await module.link((specifier) => (specifier === "./filter-store.js" ? store : specifier === "./appearance.js"
    ? new vm.SyntheticModule(["applyAppearance"], function () { this.setExport("applyAppearance", () => {}); }, { context })
    : new vm.SyntheticModule(["getLang", "setLang"], function () {
    this.setExport("getLang", () => "en");
    this.setExport("setLang", (lang) => lang);
  }, { context })));
  await module.evaluate();
  return module.namespace;
}

test("defaults and legacy [] select all route types without activating a filter", async () => {
  const state = await fixture();
  for (const [route, base] of Object.entries(state.TAB_TYPES)) {
    const [tab, sub] = route.split("/");
    state.setTab(tab, sub);
    assert.deepEqual([...state.selectedTypes()], [...base]);
    assert.equal(state.activeFilterCount(), 0);
    delete state.S.filters.typesNone;
    assert.deepEqual([...state.selectedTypes()], [...base]);
    assert.equal(state.filtersToParams().types_none, undefined);
  }
});

test("deselecting a default chip excludes it; reselecting restores unrestricted all", async () => {
  const state = await fixture();
  state.setTab("map");
  state.toggleTypeFilter(state.S.filters, "evidence");
  assert.equal(state.selectedTypes().includes("evidence"), false);
  assert.equal(state.selectedTypes().length, state.baseTypes().length - 1);
  state.toggleTypeFilter(state.S.filters, "evidence");
  assert.equal(state.S.filters.types.length, 0);
  assert.equal(state.S.filters.typesNone, false);
  assert.equal(state.activeFilterCount(), 0);
});

test("last deselection means none, serializes explicitly, and a chip can recover", async () => {
  const state = await fixture();
  state.setTab("map");
  for (const type of state.baseTypes()) state.toggleTypeFilter(state.S.filters, type);
  assert.equal(state.selectedTypes().length, 0);
  assert.equal(state.S.filters.typesNone, true);
  assert.equal(state.activeFilterCount(), 1);
  assert.equal(state.filtersToParams(state.S.filters, { types: state.baseTypes() }).types_none, "1");
  state.toggleTypeFilter(state.S.filters, "claim");
  assert.deepEqual([...state.selectedTypes()], ["claim"]);
  assert.equal(state.filtersToParams().types_none, undefined);
});

test("topic routes filter every member type and navigation keeps other scopes", async () => {
  const state = await fixture();
  state.setTab("plan", "method");
  assert.ok(state.baseTypes().includes("claim"));
  assert.ok(state.baseTypes().includes("note"));
  assert.ok(state.baseTypes().includes("topic"));
  for (const type of state.baseTypes()) state.toggleTypeFilter(state.S.filters, type);
  assert.equal(state.selectedTypes().length, 0);
  state.toggleTypeFilter(state.S.filters, "method");
  assert.deepEqual([...state.selectedTypes()], ["method"]);
  assert.equal(state.activeFilterCount(), 1);
  state.S.filters.typesNone = true;
  state.setTab("ideas");
  assert.deepEqual([...state.selectedTypes()], ["idea", "source", "question"]);
  assert.equal(state.activeFilterCount(), 0);
});

test("the Map scope is every known type, notes included, with nothing hidden by default", async () => {
  const state = await fixture();
  state.setTab("map");
  assert.deepEqual([...state.baseTypes()], [...state.ALL_TYPES]);
  for (const type of ["note", "agent", "host", "production", "outreach", "related_work", "source", "milestone", "run"]) {
    assert.ok(state.selectedTypes().includes(type), type);
  }
  assert.equal(state.ALL_TYPES.length, 21);
  assert.equal(state.activeFilterCount(), 0);
  assert.deepEqual([...state.filtersToParams(state.S.filters, { types: state.baseTypes() }).types], [...state.ALL_TYPES]);
});

test("Map type chips list the types present plus any switched off", async () => {
  const state = await fixture();
  state.setTab("map");
  const counts = { idea: 7, note: 3, agent: 4, topic: 4 };
  const all = state.selectedTypes();
  assert.deepEqual([...state.typeChips(state.baseTypes(), counts, all, true)], ["idea", "topic", "agent", "note"]);
  // Before the counts arrive, or outside the Map, every scope type is drawn.
  assert.equal(state.typeChips(state.baseTypes(), {}, all, true).length, 21);
  assert.equal(state.typeChips(state.baseTypes(), counts, all, false).length, 21);
  // A switched-off type stays visible (unchecked) even with no nodes.
  state.toggleTypeFilter(state.S.filters, "run");
  state.toggleTypeFilter(state.S.filters, "note");
  const chips = state.typeChips(state.baseTypes(), counts, state.selectedTypes(), true);
  assert.ok(chips.includes("run") && chips.includes("note"));
  assert.equal(state.activeFilterCount(), 19);
});
