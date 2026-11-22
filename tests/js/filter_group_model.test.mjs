// Run: node --experimental-vm-modules --test tests/js/filter_group_model.test.mjs
// Filter chip groups: Only this / Select all / Invert (a chip's menu, Alt+click)
// for the filter rail's groups and Needs you's filter bar.
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  groupSelection, railGroupSelected, railGroupCan, applyRailGroup, needsGroupAction, isOnlyThisClick,
} from "../../ark_studio/web/js/filter-group-model.js";

const empty = () => ({ types: [], typesNone: false, statuses: [], statusesNone: false, importanceLevels: [], importanceNone: false,
  assignees: [], tags: [], tagsUnset: [] });
const TYPES = ["story", "claim", "evidence", "task"];

test("the group selection: only, all, invert (in base order)", () => {
  assert.deepEqual(groupSelection("only", TYPES, TYPES, "claim"), ["claim"]);
  assert.deepEqual(groupSelection("all", TYPES, ["task"], null), TYPES);
  assert.deepEqual(groupSelection("invert", TYPES, ["task", "story"], null), ["claim", "evidence"]);
  assert.deepEqual(groupSelection("invert", TYPES, [], null), TYPES);
  assert.deepEqual(groupSelection("only", TYPES, ["task"], "unknown"), ["task"], "a value outside the group changes nothing");
});

test("types, status and importance: [] is every value, <group>None is none", () => {
  const filters = empty();
  applyRailGroup(filters, "types", "only", TYPES, "claim");
  assert.deepEqual([filters.types, filters.typesNone], [["claim"], false]);
  applyRailGroup(filters, "types", "invert", TYPES);
  assert.deepEqual(filters.types, ["story", "evidence", "task"]);
  applyRailGroup(filters, "types", "all", TYPES);
  assert.deepEqual([filters.types, filters.typesNone], [[], false]);
  applyRailGroup(filters, "types", "invert", TYPES);
  assert.deepEqual([filters.types, filters.typesNone], [[], true], "inverting every type selects none");
  assert.deepEqual(railGroupSelected(filters, "types", TYPES), []);
  applyRailGroup(filters, "types", "invert", TYPES);
  assert.deepEqual([filters.types, filters.typesNone], [[], false]);

  applyRailGroup(filters, "statuses", "only", ["open", "active", "done"], "active");
  assert.deepEqual([filters.statuses, filters.statusesNone], [["active"], false]);
  applyRailGroup(filters, "importance", "only", ["p0", "p1", "p2"], "p0");
  applyRailGroup(filters, "importance", "invert", ["p0", "p1", "p2"]);
  assert.deepEqual(filters.importanceLevels, ["p1", "p2"]);
  assert.deepEqual(filters.types, [], "other groups stay");
});

test("assignee and tag namespaces are include lists", () => {
  const filters = empty();
  const people = ["me", "none", "alice", "bob"];
  applyRailGroup(filters, "assignees", "only", people, "alice");
  assert.deepEqual(filters.assignees, ["alice"]);
  applyRailGroup(filters, "assignees", "invert", people);
  assert.deepEqual(filters.assignees, ["me", "none", "bob"]);
  applyRailGroup(filters, "assignees", "all", people);
  assert.deepEqual(filters.assignees, []);
  assert.equal(railGroupCan(filters, "assignees", "invert", people), false, "everything cannot invert to nothing");
  assert.equal(railGroupCan(filters, "assignees", "all", people), false, "already all");
  assert.equal(railGroupCan(filters, "assignees", "only", people, "bob"), true);

  filters.tags = ["stage:draft", "topic:rl"];
  filters.tagsUnset = ["stage"];
  const stage = ["stage:draft", "stage:review", "stage:done"];
  applyRailGroup(filters, "tag:stage", "only", stage, "stage:review");
  assert.deepEqual(filters.tags, ["topic:rl", "stage:review"], "other namespaces stay");
  assert.deepEqual(filters.tagsUnset, [], "Only this drops the namespace's unset option");
  applyRailGroup(filters, "tag:stage", "invert", stage);
  assert.deepEqual(filters.tags, ["topic:rl", "stage:draft", "stage:done"]);
  applyRailGroup(filters, "tag:stage", "all", stage);
  assert.deepEqual(filters.tags, ["topic:rl"]);
});

test("Needs you keeps the hidden values per group", () => {
  const values = ["review", "decision", "question"];
  assert.deepEqual(needsGroupAction({ kind: [], type: ["task"] }, "kind", "only", values, "decision"),
    { kind: ["review", "question"], type: ["task"] });
  assert.deepEqual(needsGroupAction({ kind: ["review"] }, "kind", "invert", values).kind, ["decision", "question"]);
  assert.deepEqual(needsGroupAction({ kind: ["review"] }, "kind", "all", values).kind, []);
});

test("Alt+click alone is Only this", () => {
  assert.equal(isOnlyThisClick({ altKey: true }), true);
  assert.equal(isOnlyThisClick({ altKey: true, ctrlKey: true }), false);
  assert.equal(isOnlyThisClick({ altKey: false }), false);
});
