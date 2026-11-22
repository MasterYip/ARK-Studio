// Node markers (web/js/markers.js): groups, one marker per exclusive group,
// toggle semantics of the marker menu, removal and the settings check.
import assert from "node:assert/strict";
import test from "node:test";

// i18n.js reads the page language at import time.
globalThis.location ??= { search: "", hash: "", href: "http://localhost/" };
const store = new Map();
globalThis.localStorage ??= { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: (k) => store.delete(k) };
globalThis.navigator ??= { language: "en" };
globalThis.document ??= { documentElement: { lang: "", dataset: {} } };

const M = await import("../../ark_studio/web/js/markers.js");
const node = (...tags) => ({ tags });

test("default groups: stars, flags, priority and progress are exclusive, symbols are not", () => {
  M.setMarkerGroups(null);
  const groups = M.markerGroups();
  assert.deepEqual(groups.map((g) => [g.id, g.exclusive]),
    [["star", true], ["flag", true], ["priority", true], ["progress", true], ["symbol", false]]);
  // Markers saved before groups existed keep their tags.
  for (const tag of ["marker:star-yellow", "marker:star-blue", "marker:flag-red", "marker:check-green", "marker:question", "marker:priority"]) {
    assert.ok(M.markerForTag(tag), tag);
  }
  assert.equal(M.markerForTag("tag:x"), null);
});

test("a node shows one marker per exclusive group (legacy duplicates collapse)", () => {
  M.setMarkerGroups(null);
  const shown = M.markersFor(node("marker:star-blue", "marker:star-yellow", "marker:question", "marker:check-green", "tag:x"));
  assert.deepEqual(shown.map((m) => m.tag), ["marker:star-yellow", "marker:question", "marker:check-green"]);
});

test("choosing a marker toggles it; another of the same group replaces it", () => {
  M.setMarkerGroups(null);
  const a = node("marker:star-yellow");
  // Same star again: removed (no duplicate).
  assert.deepEqual(M.toggleMarkerChange([a], "marker:star-yellow"), { add: [], remove: ["marker:star-yellow"] });
  // Another star: replaces the yellow one.
  assert.deepEqual(M.toggleMarkerChange([a], "marker:star-red"), { add: ["marker:star-red"], remove: ["marker:star-yellow"] });
  // Symbols are independent.
  assert.deepEqual(M.toggleMarkerChange([node("marker:question")], "marker:check-green"), { add: ["marker:check-green"], remove: [] });
  // Applied locally the same way the server does.
  assert.deepEqual(M.applyMarkerChange(["tag:x", "marker:star-yellow"], { add: ["marker:star-red"], remove: ["marker:star-yellow"] }),
    ["tag:x", "marker:star-red"]);
  assert.deepEqual(M.applyMarkerChange(["marker:star-red"], { add: ["marker:star-red"] }), ["marker:star-red"]);
});

test("several nodes: add to all unless all have it", () => {
  M.setMarkerGroups(null);
  const nodes = [node("marker:flag-red"), node()];
  assert.equal(M.markerState(nodes, "marker:flag-red"), "some");
  assert.deepEqual(M.toggleMarkerChange(nodes, "marker:flag-red"), { add: ["marker:flag-red"], remove: [] });
  const both = [node("marker:flag-red"), node("marker:flag-red", "marker:flag-blue")];
  assert.equal(M.markerState(both, "marker:flag-red"), "some", "the second node shows the blue flag");
  assert.deepEqual(M.toggleMarkerChange(both, "marker:flag-blue"), { add: ["marker:flag-blue"], remove: ["marker:flag-red"] });
});

test("remove one marker (with hidden duplicates of its group) or all", () => {
  M.setMarkerGroups(null);
  const legacy = node("marker:star-blue", "marker:star-yellow", "marker:question", "tag:x");
  assert.deepEqual(M.removeMarkersChange([legacy], "marker:star-yellow").remove.sort(), ["marker:star-blue", "marker:star-yellow"]);
  assert.deepEqual(M.removeMarkersChange([legacy], "marker:question").remove, ["marker:question"]);
  assert.deepEqual(M.removeMarkersChange([legacy]).remove.sort(), ["marker:question", "marker:star-blue", "marker:star-yellow"]);
});

test("menu: headings per group, checks on present markers, remove and clear entries", () => {
  M.setMarkerGroups(null);
  const applied = [];
  const items = M.markerMenuChildren([node("marker:star-red")], (change) => applied.push(change));
  assert.equal(items.filter((item) => item.heading).length, 5);
  const red = items.find((item) => item.label?.startsWith("★") && item.checked);
  assert.ok(red, "the red star is checked");
  assert.equal(items.filter((item) => item.checked).length, 1);
  red.onClick();
  assert.deepEqual(applied.pop(), { add: [], remove: ["marker:star-red"] });
  const remove = items.find((item) => item.label === "graph.menu.remove_marker" || item.icon === "x");
  assert.equal(remove.children.length, 1);
  const clear = items.find((item) => item.icon === "trash");
  assert.equal(clear.disabled, false);
  const empty = M.markerMenuChildren([node()], () => {});
  assert.equal(empty.find((item) => item.icon === "trash").disabled, true);
});

test("project groups replace the defaults", () => {
  assert.equal(M.setMarkerGroups({ groups: [{ id: "mood", name: "Mood", exclusive: true, markers: [
    { id: "mood-happy", glyph: "☺", color: "#22c55e", label: "" }, { id: "mood-sad", glyph: "☹", color: "#64748b", label: "Sad" }] }] }), true);
  assert.equal(M.markerGroups().length, 1);
  assert.equal(M.markerForTag("marker:star-yellow"), null);
  assert.equal(M.markerForTag("marker:mood-sad").label, "Sad");
  assert.deepEqual(M.toggleMarkerChange([node("marker:mood-happy")], "marker:mood-sad"),
    { add: ["marker:mood-sad"], remove: ["marker:mood-happy"] });
  assert.equal(M.setMarkerGroups({ groups: [{ id: "mood", name: "Mood", exclusive: true, markers: [
    { id: "mood-happy", glyph: "☺", color: "#22c55e", label: "" }, { id: "mood-sad", glyph: "☹", color: "#64748b", label: "Sad" }] }] }), false,
  "same setting: no change");
  M.setMarkerGroups(null);
});

test("cleanMarkerGroups checks what the server checks", () => {
  const ok = M.cleanMarkerGroups(M.effectiveMarkerGroups(null));
  assert.equal(ok.groups.length, 5);
  const bad = (groups) => assert.throws(() => M.cleanMarkerGroups(groups));
  bad([{ id: "A", markers: [{ id: "x", glyph: "x", color: "#000000" }] }]);
  bad([{ id: "a", markers: [] }]);
  bad([{ id: "a", markers: [{ id: "x", glyph: "abcd", color: "#000000" }] }]);
  bad([{ id: "a", markers: [{ id: "x", glyph: "x", color: "blue" }] }]);
  bad([{ id: "a", markers: [{ id: "x", glyph: "x", color: "#000000" }] }, { id: "b", markers: [{ id: "x", glyph: "y", color: "#000000" }] }]);
  assert.equal(M.freeMarkerId("star-red", ["star-red", "star-red-2"]), "star-red-3");
});
