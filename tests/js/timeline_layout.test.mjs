// Run: node --experimental-vm-modules --test tests/js/timeline_layout.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import vm from "node:vm";
import { loadNodeGraphLayout } from "./load_graph_core.mjs";

const { namespace: L } = await loadNodeGraphLayout(vm.createContext({ Date }));

const day = (d, h = 9) => `2026-11-${String(d).padStart(2, "0")}T${String(h).padStart(2, "0")}:00:00Z`;
const node = (id, created, tags = [], extra = {}) => ({ id, created_at: created, tags, ...extra });
const link = (source, target, relation = "derived_from") => ({ source, target, relation });
const layout = (nodes, edges = [], options = {}) => L.timelineLayout(nodes, edges, options);
const xs = (out, id) => out.positions.get(id).x;
const lane = (out, id) => out.laneOf.get(id);
// Values from the module's realm compared by content.
const same = (actual, expected, message) => assert.deepEqual(JSON.parse(JSON.stringify(actual)), expected, message);

test("time: start_at, else created_at, else updated_at; date-only is local midnight", () => {
  assert.equal(L.timelineTime({ start_at: "2026-11-02", created_at: day(1) }), new Date(2026, 10, 2).getTime());
  assert.equal(L.timelineTime({ created_at: day(1) }), Date.parse(day(1)));
  assert.equal(L.timelineTime({ updated_at: day(3) }), Date.parse(day(3)));
  assert.ok(Number.isNaN(L.timelineTime({})));
  same([...L.nodeLines({ tags: ["branch:a", "x", "branch:a", "lab:b", "branch:"] })], ["a"]);
  same([...L.nodeLines({ tags: ["lab:b"] }, "lab")], ["b"]);
});

test("nodes run left to right in time order; columns are ranks, not time distances", () => {
  const out = layout([node("c", day(20)), node("a", day(1)), node("b", day(2))], [], { closeMs: 0 });
  same([...out.order], ["a", "b", "c"]);
  assert.ok(xs(out, "a") < xs(out, "b") && xs(out, "b") < xs(out, "c"));
  // 1 day and 18 days apart: the same step.
  assert.equal(Math.round(xs(out, "b") - xs(out, "a")), Math.round(xs(out, "c") - xs(out, "b")));
});

test("one lane per line; sub-lines follow their parent; parallel lines share columns", () => {
  const nodes = [
    node("a1", day(1), ["branch:main"]), node("b1", day(2), ["branch:side"]),
    node("a2", day(3), ["branch:main"]), node("b2", day(4), ["branch:side"]),
    node("s1", day(5), ["branch:main/sub"]),
  ];
  const out = layout(nodes, [], { closeMs: 0 });
  same(out.lanes.map((item) => item.key), ["line:main", "line:main/sub", "line:side"]);
  same(out.lanes.map((item) => item.depth), [0, 1, 0]);
  assert.equal(lane(out, "b1"), "line:side");
  // b1 follows a1 in time, in another lane: same column.
  assert.equal(xs(out, "a1"), xs(out, "b1"));
  assert.ok(out.positions.get("a1").y < out.positions.get("s1").y);
  assert.ok(out.positions.get("s1").y < out.positions.get("b1").y);
  // Lanes do not overlap and are stacked top to bottom.
  for (let i = 1; i < out.lanes.length; i++) assert.ok(out.lanes[i].y0 >= out.lanes[i - 1].y1);
  for (const item of nodes) {
    const band = out.lanes.find((entry) => entry.key === lane(out, item.id));
    const y = out.positions.get(item.id).y;
    assert.ok(y > band.y0 && y < band.y1, `${item.id} inside its lane`);
  }
});

test("untagged nodes take their part_of parent's line, else the untagged lane (last)", () => {
  const out = layout([
    node("p", day(1), ["branch:x"]), node("q", day(2), ["branch:x"]), node("r", day(3), ["branch:y"]),
    node("child", day(4)), node("lone", day(5)),
  ], [link("child", "p", "part_of")]);
  assert.equal(lane(out, "child"), "line:x");
  assert.equal(lane(out, "lone"), "loose");
  assert.equal(out.lanes.at(-1).kind, "loose");
});

test("edges point forward: derived work sits right of its sources even against the dates", () => {
  // `late` derives from `early`, but carries an older date.
  const out = layout([node("early", day(5)), node("late", day(1)), node("other", day(3))],
    [link("early", "late"), link("x", "late")]);
  assert.ok(xs(out, "late") > xs(out, "early"));
  // A cycle still places every node.
  const loop = layout([node("a", day(1)), node("b", day(2))], [link("a", "b"), link("b", "a")]);
  assert.equal(loop.positions.size, 2);
  assert.ok(xs(loop, "a") < xs(loop, "b"));
});

test("a merge node sits after its inputs from every line and joins the biggest line", () => {
  const out = layout([
    node("a1", day(1), ["branch:a"]), node("a2", day(2), ["branch:a"]), node("a3", day(6), ["branch:a"]),
    node("b1", day(1, 10), ["branch:b"]), node("b2", day(9), ["branch:b"]),
    node("m", day(3), ["branch:a", "branch:b"]),
  ], [link("a2", "m"), link("b2", "m")], { closeMs: 0 });
  assert.equal(lane(out, "m"), "line:a");
  assert.ok(xs(out, "m") > xs(out, "a2"));
  assert.ok(xs(out, "m") > xs(out, "b2"), "after the late input from the other line");
});

test("close nodes of one lane stack in a column (up to maxStack), never on top of an input", () => {
  const nodes = ["a", "b", "c", "d"].map((id, i) => node(id, `2026-11-01T09:0${i}:00Z`, ["branch:x"]));
  const out = layout(nodes, [], { maxStack: 3 });
  assert.equal(new Set(["a", "b", "c"].map((id) => xs(out, id))).size, 1);
  assert.ok(xs(out, "d") > xs(out, "a"));
  const chained = layout(nodes.slice(0, 2), [link("a", "b")]);
  assert.ok(xs(chained, "b") > xs(chained, "a"));
});

test("no dates: order follows the links, then the given order", () => {
  const out = layout([{ id: "x" }, { id: "y" }, { id: "z" }], [link("z", "x")], { closeMs: 0 });
  same([...out.order], ["y", "z", "x"]);
  assert.ok(xs(out, "z") < xs(out, "x"));
  assert.ok(out.columns.every((column) => Number.isNaN(column.time)));
});

test("mostly untagged: lanes per part_of tree, singles in the untagged lane", () => {
  const out = layout([
    node("r1", day(1)), node("c1", day(2)), node("c2", day(3)),
    node("r2", day(4)), node("d1", day(5)), node("single", day(6)),
  ], [link("c1", "r1", "part_of"), link("c2", "c1", "part_of"), link("d1", "r2", "part_of")]);
  same(out.lanes.map((item) => item.kind), ["group", "group", "loose"]);
  assert.equal(out.lanes[0].root, "r1");
  assert.equal(lane(out, "c2"), lane(out, "r1"));
  assert.equal(lane(out, "single"), "loose");
});

test("a few hundred nodes lay out quickly", () => {
  const nodes = [], edges = [];
  for (let i = 0; i < 600; i++) {
    nodes.push(node(`n${i}`, new Date(Date.UTC(2026, 9, 1) + i * 3_600_000 * 5).toISOString(), [`branch:l${i % 7}`]));
    if (i > 3) edges.push(link(`n${i - 3}`, `n${i}`));
  }
  const started = performance.now();
  const out = layout(nodes, edges);
  assert.ok(performance.now() - started < 500);
  assert.equal(out.positions.size, 600);
  assert.equal(out.lanes.length, 7);
  for (const edge of edges) assert.ok(xs(out, edge.target) > xs(out, edge.source));
});

test("part_of orders only when the dates agree", () => {
  // child part_of parent (shown child -> parent); the whole came first.
  const older = layout([node("parent", day(1)), node("child", day(3))], [link("child", "parent", "part_of")], { closeMs: 0 });
  assert.ok(xs(older, "parent") < xs(older, "child"));
  // Same day: the parts come before the whole they make up.
  const same = layout([node("parent", day(1, 9)), node("child", day(1, 9))], [link("child", "parent", "part_of")], { closeMs: 0 });
  assert.ok(xs(same, "child") < xs(same, "parent"));
});
