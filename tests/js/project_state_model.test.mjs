// Run: node --test tests/js/project_state_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import {
  MAX_BYTES, MAX_PROJECTS, SNAPSHOT_VERSION, createProjectStateStore, emptySnapshot, normalizeSnapshot, projectCameras,
  restorableRoute, restorePlan, scrollSelector, sessionPatch, storageKey,
} from "../../ark_studio/web/js/project-state-model.js";
import { decideStart } from "../../ark_studio/web/js/reload-guard.js";
import { needsSeenBody, needsSeenKey, needsUnitKey } from "../../ark_studio/web/js/needs-model.js";
import {
  otherProjectsNew, refreshedProjectRows, reviewBadge, withKnownActivity, withReviewBadge,
} from "../../ark_studio/web/js/project-picker-model.js";

function memoryStorage() {
  const map = new Map();
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => { map.set(key, String(value)); },
    removeItem: (key) => { map.delete(key); },
    map,
  };
}

const mapSnapshot = {
  v: SNAPSHOT_VERSION,
  route: "#/map",
  view: "graph",
  graphLayout: "force",
  unit: { ref: "T-0012", floating: false, scroll: 340 },
  sessions: { selected: "ark-ses-1", query: "fix", filter: "running", groupBy: "status", railTab: "subagents", threadMode: "thread" },
  cameras: { "pA:map:story:graph": { zoom: 2.4, pan: { x: -120, y: 40 }, layout: "force" } },
  scrollY: 0,
  scrolls: [{ sel: "div.needs-list", index: 0, top: 220 }],
};

test("snapshot: a map with a zoomed camera, an open unit and a session round-trips", () => {
  let now = 1000;
  const store = createProjectStateStore({ storage: memoryStorage(), user: () => "ana", now: () => now });
  store.put("pA", mapSnapshot);
  const back = store.get("pA");
  assert.equal(back.route, "#/map");
  assert.equal(back.view, "graph");
  assert.deepEqual(back.unit, { ref: "T-0012", floating: false, scroll: 340 });
  assert.deepEqual(back.cameras["pA:map:story:graph"], { zoom: 2.4, pan: { x: -120, y: 40 }, layout: "force" });
  assert.equal(back.sessions.selected, "ark-ses-1");
  assert.equal(back.sessions.railTab, "subagents");
  assert.equal(back.at, 1000);
  assert.equal(store.get("pB"), null, "a project never left has no snapshot: defaults");
});

test("snapshots are per account on this browser", () => {
  const storage = memoryStorage();
  let user = "ana";
  const store = createProjectStateStore({ storage, user: () => user });
  store.put("pA", mapSnapshot);
  user = "bob";
  assert.equal(store.get("pA"), null);
  assert.ok(storage.map.has(storageKey("ana")));
  assert.notEqual(storageKey("ana"), storageKey("bob"));
});

test("restore plan: first visit and an empty snapshot use the defaults", () => {
  assert.equal(restorePlan(null), null);
  assert.equal(restorePlan(normalizeSnapshot({ v: 1, route: "#/home" })), null);
  assert.ok(emptySnapshot(normalizeSnapshot({ v: 1 })));
  const plan = restorePlan(normalizeSnapshot(mapSnapshot));
  assert.equal(plan.route, "#/map");
  assert.equal(plan.unit.ref, "T-0012");
  // Home with an open unit restores Home and the unit.
  const home = restorePlan(normalizeSnapshot({ v: 1, route: "#/home", unit: { ref: "N-1" } }));
  assert.equal(home.route, "#/home");
  assert.equal(home.unit.ref, "N-1");
});

test("reload guard: a safe start after a hang wins over the snapshot", () => {
  const store = createProjectStateStore({ storage: memoryStorage(), user: () => "ana" });
  store.put("pA", mapSnapshot);
  // The tab hung on the map and was reloaded: the guard decides a safe start.
  const decision = decideStart({ open: false, stalled: true, url: "/#/map", at: 5000 }, { now: 6000 });
  assert.equal(decision.safe, true);
  assert.equal(restorePlan(store.get("pA"), { safe: decision.safe }), null, "nothing restored on that load");
  // ... and app.js forgets the project's page, so switching back later starts at Home too.
  store.drop("pA");
  assert.equal(restorePlan(store.get("pA")), null);
  assert.ok(store.get("pB") === null);
});

test("app.js: a safe start drops the tab project's snapshot before any restore", async () => {
  const app = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  const drop = app.indexOf("if (startGuard.safe) projectStates.drop(S.project?.id);");
  const restore = app.indexOf("bootPlan = restorePlan(projectStates.get(S.project?.id));");
  assert.ok(drop > 0 && restore > drop, "drop comes first");
  assert.match(app, /if \(!startGuard\.safe && desktopHub\(\)/);
  // A switch saves the page being left before the menu closes the inspector.
  assert.ok(app.indexOf("saveProjectState();\n  beforeOpen?.(project);") > 0);
});

test("stale or hostile values are cleaned", () => {
  const raw = {
    v: 1, route: "javascript:alert(1)", view: "<img>", graphLayout: "spiral",
    unit: { ref: "T-1\"><script>", scroll: -5 },
    sessions: { selected: "x".repeat(300), query: 5 },
    cameras: { "pA:map": { zoom: "nan", pan: { x: 0, y: 0 } }, "pA:ok": { zoom: 1, pan: { x: 1, y: 2 }, layout: "tree" } },
    scrolls: [{ sel: "div[onclick]", index: 0, top: 1 }, { sel: "section.page", index: 2, top: -4 }],
  };
  const out = normalizeSnapshot(raw);
  assert.equal(out.route, "");
  assert.equal(out.view, "");
  assert.equal(out.graphLayout, "");
  assert.equal(out.unit, null);
  assert.equal(out.sessions.selected, "");
  assert.equal(out.sessions.query, "");
  assert.deepEqual(Object.keys(out.cameras), ["pA:ok"]);
  assert.deepEqual(out.scrolls, [{ sel: "section.page", index: 2, top: 0 }]);
  assert.equal(normalizeSnapshot({ v: 2, route: "#/map" }), null, "a newer format is ignored");
  assert.equal(normalizeSnapshot("{"), null);
});

test("routes: places are kept, Home and one-off links are not", () => {
  assert.equal(restorableRoute("#/map"), "#/map");
  assert.equal(restorableRoute("#/execute/sessions"), "#/execute/sessions");
  assert.equal(restorableRoute("#/research/topic/TOP-3"), "#/research/topic/TOP-3");
  assert.equal(restorableRoute("#/editor/units%2FT-1.md"), "#/editor/units%2FT-1.md");
  assert.equal(restorableRoute("#/home"), "");
  assert.equal(restorableRoute("#/overview"), "");
  assert.equal(restorableRoute(""), "");
  assert.equal(restorableRoute("#/join/abc"), "");
  assert.equal(restorableRoute("#/map?pair=abc"), "");
});

test("size caps: oldest projects go first", () => {
  let now = 0;
  const storage = memoryStorage();
  const store = createProjectStateStore({ storage, user: () => "ana", now: () => ++now });
  for (let i = 0; i < MAX_PROJECTS + 5; i++) store.put(`p${i}`, mapSnapshot);
  const stored = JSON.parse(storage.getItem(storageKey("ana"))).projects;
  assert.equal(Object.keys(stored).length, MAX_PROJECTS);
  assert.equal(store.get("p0"), null);
  assert.ok(store.get(`p${MAX_PROJECTS + 4}`));
  // Bytes: big snapshots push old ones out, the newest stays.
  const small = createProjectStateStore({ storage: memoryStorage(), user: () => "ana", now: () => ++now, maxBytes: 1500 });
  for (let i = 0; i < 10; i++) small.put(`q${i}`, mapSnapshot);
  assert.ok(small.get("q9"));
  assert.equal(small.get("q0"), null);
  assert.ok(MAX_BYTES >= 32 * 1024);
});

test("storage full or disabled: no error", () => {
  const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("full"); } };
  const store = createProjectStateStore({ storage: broken, user: () => "ana" });
  store.put("pA", mapSnapshot);
  assert.equal(store.get("pA"), null);
  const none = createProjectStateStore({ storage: null });
  none.put("pA", mapSnapshot);
  assert.equal(none.get("pA"), null);
});

test("session patch, scroll selectors and per-project cameras", () => {
  assert.deepEqual(sessionPatch({ selected: "s1", query: "", filter: "running" }), { selectedSessionId: "s1", sessionFilter: "running" });
  assert.equal(scrollSelector("DIV", ["needs-list", "open", "review-list"]), "div.needs-list.review-list");
  assert.equal(scrollSelector("x y", []), "");
  const cams = projectCameras([
    ["pA:map:story:graph", { zoom: 1.5, pan: { x: 1, y: 2 }, layout: "force" }],
    ["pB:map:story:graph", { zoom: 3, pan: { x: 0, y: 0 }, layout: "force" }],
    ["map:story:graph", { zoom: 3, pan: { x: 0, y: 0 }, layout: "force" }],
  ], "pA");
  assert.deepEqual(Object.keys(cams), ["pA:map:story:graph"]);
});

// ------------------------------------------------------------- review badges

test("seen keys: units, decisions and review groups", () => {
  assert.equal(needsSeenKey({ uid: "T-1", kind: "review" }), "u:T-1");
  assert.equal(needsSeenKey({ id: "RG-3", uid: "RG-3", kind: "review_group" }), "rg:RG-3");
  assert.equal(needsSeenKey({}), "");
  assert.equal(needsUnitKey({ uid: "T-1", status: "review" }), "u:T-1");
  assert.equal(needsUnitKey({ uid: "D-1", type: "decision", status: "active" }), "u:D-1");
  assert.equal(needsUnitKey({ uid: "D-2", type: "decision", status: "done" }), "");
  assert.equal(needsUnitKey({ uid: "T-2", status: "done" }), "");
});

test("seen body: the list watermark only when every card was shown unfiltered", () => {
  const entries = [
    { kind: "item", item: { kind: "review_group", id: "RG-1", uid: "RG-1" } },
    { kind: "group", members: [{ uid: "T-1" }, { uid: "T-2" }] },
    { kind: "item", item: { uid: "D-1", kind: "decision" } },
  ];
  assert.deepEqual(needsSeenBody(entries, 3), { all: true, items: ["rg:RG-1", "u:T-1", "u:T-2", "u:D-1"] });
  assert.deepEqual(needsSeenBody(entries, 2), { items: ["rg:RG-1", "u:T-1", "u:T-2"] }, "the rest was not on screen");
  assert.deepEqual(needsSeenBody(entries, 3, { filtered: true }), { items: ["rg:RG-1", "u:T-1", "u:T-2", "u:D-1"] });
  assert.deepEqual(needsSeenBody([], 0), { all: true, items: [] }, "an empty list is seen too");
  assert.equal(needsSeenBody(entries, 0, { filtered: true }), null);
});

test("badge counts: per card, the dot counts other projects only", () => {
  assert.deepEqual(reviewBadge({ review_new: 3, review_total: 7 }), { fresh: 3, total: 7 });
  assert.deepEqual(reviewBadge({}), { fresh: 0, total: 0 });
  assert.deepEqual(reviewBadge({ review_new: "2", review_total: 1 }), { fresh: 2, total: 2 });
  const projects = [{ id: "a", review_new: 2, review_total: 4 }, { id: "b", review_new: 3, review_total: 3 }, { id: "c" }];
  assert.equal(otherProjectsNew(projects, "a"), 3);
  assert.equal(otherProjectsNew(projects, "b"), 2);
  assert.equal(otherProjectsNew(null, "a"), 0);
  // After POST /api/needs/seen: the current project's numbers change in place.
  const after = withReviewBadge(projects, "b", { review_new: 0, review_total: 3 });
  assert.equal(otherProjectsNew(after, "a"), 0);
  assert.equal(after[0], projects[0]);
  // A bootstrap list (no badges) keeps the known ones; a refreshed list repaints the cards.
  const boot = withKnownActivity([{ id: "a" }], [{ id: "a", my_activity: 1, review_new: 2, review_total: 4 }]);
  assert.equal(boot[0].review_new, 2);
  assert.ok(refreshedProjectRows(projects, after), "badge change repaints");
  assert.equal(refreshedProjectRows(projects, projects.map((p) => ({ ...p }))), null);
});

test("picker card and topbar wire the badges", async () => {
  const picker = await readFile(new URL("../../ark_studio/web/js/project-picker.js", import.meta.url), "utf8");
  assert.match(picker, /reviewPill\(project\),/);
  const app = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  assert.match(app, /syncReviewDot\(picker\);/);
  for (const lang of ["en", "zh"]) {
    const dict = await readFile(new URL(`../../ark_studio/web/js/i18n-${lang}.js`, import.meta.url), "utf8");
    for (const key of ["project.picker.review_new", "project.picker.review_total", "project.picker.review_title",
      "project.picker.review_seen_title", "app.menu.projects_new"]) assert.ok(dict.includes(`"${key}"`), `${lang} ${key}`);
  }
});
