import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

const module = new vm.SourceTextModule(await readFile(new URL("../../ark_studio/web/js/map-list-model.js", import.meta.url), "utf8"));
await module.link(() => { throw new Error("Map list model must remain dependency free"); });
await module.evaluate();
const { normalizeMapListPrefs, defaultMapListDirection, sortMapUnits, groupMapUnits, mapListPage } = module.namespace;
const ids = units => Array.from(units, unit => unit.id);
const prefs = (sort, direction = defaultMapListDirection(sort), group = "none") => ({ sort, direction, group });
const node = (id, extra = {}) => ({ id, uid: `C-${id}`, title: `Node ${id}`, type: "claim", status: "open", importance: "p2", ...extra });

test("invalid stored options fall back to the existing type/title layout", () => {
  assert.deepEqual(normalizeMapListPrefs(null), { sort: "title", direction: "asc", group: "type" });
  assert.deepEqual(normalizeMapListPrefs({ sort: "bogus", direction: "bogus", group: "bogus" }), { sort: "title", direction: "asc", group: "type" });
  assert.equal(normalizeMapListPrefs({ sort: "connections" }).direction, "desc");
  assert.equal(normalizeMapListPrefs({ sort: "end" }).direction, "asc");
});

test("connection rankings use global incoming/outgoing counts, including zero and missing counts", () => {
  const units = [node(1, { in_count: 1, out_count: 3, degree: 50 }), node(2, { in_count: 7, out_count: 1, degree: 0 }), node(3)];
  assert.deepEqual(ids(sortMapUnits(units, prefs("connections"))), [2, 1, 3]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("connections", "asc"))), [3, 1, 2]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("incoming"))), [2, 1, 3]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("outgoing"))), [1, 2, 3]);
  assert.deepEqual(ids(units), [1, 2, 3]);
});

test("all date sorts compare actual times and keep empty or invalid values last in either direction", () => {
  for (const [sort, field] of [["created", "created_at"], ["updated", "updated_at"], ["start", "start_at"], ["end", "end_at"]]) {
    const units = [node(1, { [field]: "2026-01-02T00:00:00+08:00" }), node(2, { [field]: "2026-01-01T20:00:00Z" }), node(3), node(4, { [field]: "bad date" })];
    assert.deepEqual(ids(sortMapUnits(units, prefs(sort, "asc"))), [1, 2, 3, 4]);
    assert.deepEqual(ids(sortMapUnits(units, prefs(sort, "desc"))), [2, 1, 3, 4]);
  }
});

test("title/UID ordering is natural, deterministic and does not reverse tie breakers", () => {
  const units = [node(10), node(2), node(1)];
  assert.deepEqual(ids(sortMapUnits(units, prefs("title"))), [1, 2, 10]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("uid", "desc"))), [10, 2, 1]);
  const tied = [node(10, { title: "Same" }), node(2, { title: "Same" })];
  assert.deepEqual(ids(sortMapUnits(tied, prefs("title", "desc"))), [2, 10]);
});

test("category sorts follow workflow and priority; inherited assignees participate in sorting and grouping", () => {
  const units = [node(1, { status: "done", importance: "p3", assignee: "bob" }), node(2, { status: "active", importance: "p0", assignee_effective: "alice" }), node(3)];
  assert.deepEqual(ids(sortMapUnits(units, prefs("status"))), [3, 2, 1]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("importance"))), [2, 3, 1]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("assignee"))), [2, 1, 3]);
  assert.deepEqual(ids(sortMapUnits(units, prefs("assignee", "desc"))), [1, 2, 3]);
  assert.deepEqual(Array.from(groupMapUnits(units, "assignee"), group => group.key), ["alice", "bob", ""]);
  assert.deepEqual(ids(sortMapUnits([node(1), node(2, { type: "question" })], prefs("type"))), [2, 1]);
});

test("grouping is independent of ranking, and pagination crosses groups without dropping or repeating nodes", () => {
  const units = Array.from({ length: 235 }, (_, i) => node(i + 1, { type: i < 110 ? "claim" : "question", in_count: i }));
  const ranked = sortMapUnits(units, prefs("connections"));
  assert.equal(ranked[0].id, 235);
  const groups = groupMapUnits(ranked, "type");
  assert.deepEqual(Array.from(groups, group => group.key), ["question", "claim"]);
  const pages = [0, 1, 2].map(page => mapListPage(groups, page));
  assert.equal(pages[0].groups[0].total, 125);
  assert.equal(pages[1].groups.length, 2);
  assert.equal(pages[2].start, 201);
  assert.equal(pages[2].end, 235);
  assert.deepEqual(pages.flatMap(page => page.groups.flatMap(group => ids(group.items))), groups.flatMap(group => ids(group.items)));
  assert.equal(mapListPage(groups, 999).page, 2);
  assert.equal(mapListPage([], 9).start, 0);
});
