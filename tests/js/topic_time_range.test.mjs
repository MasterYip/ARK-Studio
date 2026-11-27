// The Time slider on a topic page spans the topic's own members, never the
// project, with dates compared as ms (not strings).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { GraphFilterIndex, nodeTimeSpan, timeMs } from "../../ark_studio/web/js/graph-filter.js";
import { filterTopic, topicTimelineItems } from "../../ark_studio/web/js/topic-model.js";

const unit = (id, extra = {}) => ({ id, uid: `N-${id}`, title: `Node ${id}`, type: "note", status: "open",
  importance: "p2", tags: [], created_at: "2026-11-25T08:00:00+00:00", updated_at: "2026-11-26T09:00:00+00:00",
  member: { parent: null, depth: 0, children: 0, path: String(id) }, ...extra });

// A project whose planned dates reach far beyond the topic's.
const project = [
  unit(1, { start_at: "2026-01-05" }),
  unit(2, { end_at: "2026-12-31" }),
  unit(10, { start_at: "2026-11-22", end_at: "2026-11-24" }),
  unit(11, { tags: ["branch:predactor/textscale-arch"] }),
  unit(12, { start_at: "2026-11-23T10:00:00+00:00" }),
];
const members = project.filter((node) => node.id >= 10);
const stored = { topic: { id: 99, uid: "TP-0001", type: "topic" }, members, edges: [{ source: 10, target: 11 }, { source: 11, target: 12 }] };
// GET /api/topics/branch:… (Store.BranchTopic): same payload, a virtual topic.
const line = { topic: { id: null, uid: "branch:predactor/textscale-arch", type: "topic", virtual: true, branch: "predactor/textscale-arch" },
  members, edges: stored.edges, subgraph: { rule: { tag_trees: ["branch:predactor/textscale-arch"] } } };

const ms = (info) => [timeMs(info.min), timeMs(info.max, true)];

test("a topic's slider extent is its members' earliest and latest time, not the project's", () => {
  const projectTime = new GraphFilterIndex({ nodes: project }).facets().time;
  assert.deepEqual([projectTime.min, projectTime.max], ["2026-01-05", "2026-12-31"]);
  const time = filterTopic(stored).facets.time;
  // Node 11 has no planned dates: it counts from created_at to updated_at.
  assert.deepEqual([time.min, time.max], ["2026-11-22", "2026-11-26T09:00:00+00:00"]);
  assert.equal(time.undated, 0);
  const all = members.flatMap((node) => {
    const span = nodeTimeSpan(node, { activity: true });
    return [span.start, span.end].filter(Number.isFinite);
  });
  assert.deepEqual(ms(time), [Math.min(...all), Math.max(...all)]);
});

test("the extent does not move with the topic's other filters", () => {
  const time = filterTopic(stored, { types: ["claim"], statuses: ["done"] }).facets.time;
  assert.deepEqual([time.min, time.max], ["2026-11-22", "2026-11-26T09:00:00+00:00"]);
});

test("mixed date-only values and timestamps with offsets compare as instants", () => {
  // As strings "2026-11-20T23:00:00-05:00" sorts first; as instants it is
  // 04:00Z on the 21st, after "2026-11-21T02:00:00+00:00".
  const nodes = [
    unit(1, { start_at: "2026-11-20T23:00:00-05:00" }),
    unit(2, { start_at: "2026-11-21T02:00:00+00:00" }),
    unit(3, { end_at: "2026-11-30T12:00:00Z" }),
    unit(4, { end_at: "2026-12-01" }),
  ];
  const time = new GraphFilterIndex({ nodes }).facets().time;
  assert.deepEqual([time.min, time.max], ["2026-11-21T02:00:00+00:00", "2026-12-01"]);
  const topicTime = filterTopic({ topic: { id: 1 }, members: nodes, edges: [] }).facets.time;
  assert.deepEqual([topicTime.min, topicTime.max], [time.min, time.max]);
  // A date-only max is the end of that day; a date-only min its start.
  assert.equal(timeMs("2026-12-01", true), new Date(2026, 11, 1, 23, 59, 59, 999).getTime());
  assert.equal(timeMs("2026-12-01"), new Date(2026, 11, 1).getTime());
  // The filter compares the same way: 03:00Z..05:00Z holds node 1 only.
  const ids = (query) => new GraphFilterIndex({ nodes }).filter(query).nodes.map((node) => node.id).sort();
  assert.deepEqual(ids({ from: "2026-11-21T03:00:00.000Z", to: "2026-11-21T05:00:00.000Z" }), [1]);
  // A date-only end matches anywhere within its day.
  const noon = new Date(2026, 11, 1, 12).toISOString();
  assert.deepEqual(ids({ from: noon }), [4]);
});

test("the topic's Time filter, extent and Timeline use the same member times", () => {
  const ids = (query) => filterTopic(stored, query).members.map((node) => node.id);
  // Only node 11's created..updated span (25th 08:00Z – 26th 09:00Z) reaches here.
  assert.deepEqual(ids({ from: "2026-11-25T12:00:00.000Z" }), [11]);
  assert.deepEqual(ids({ to: "2026-11-22T12:00:00.000Z" }), [10]);
  const items = topicTimelineItems(members);
  assert.deepEqual(items.map((item) => item.id), [10, 11, 12]);
  const activity = items.find((item) => item.id === 11);
  assert.equal(activity.time_source, "activity");
  assert.match(activity.start_at, /^\d{4}-\d{2}-\d{2}$/);
  assert.equal(items.find((item) => item.id === 10), members[0], "planned members are passed through");
});

test("project pages keep planned dates only", () => {
  const index = new GraphFilterIndex({ nodes: project });
  assert.equal(index.facets().time.undated, 1);
  const ids = index.filter({ from: "2026-11-25T12:00:00.000Z", to: "2026-11-27T00:00:00.000Z" }).nodes.map((node) => node.id);
  assert.deepEqual(ids, []);
  assert.equal(nodeTimeSpan(project[3]).source, null);
});

test("Min. links on a topic page keeps the topic's own time extent", () => {
  const full = filterTopic(stored).facets.time;
  const linked = filterTopic(stored, { min_links: 2 });
  assert.deepEqual(linked.members.map((node) => node.id), [11]);
  assert.deepEqual(linked.facets.time, full);
});

test("the app never lays project facets over a topic page", async () => {
  const app = await readFile(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  // Route data for the topic tab carries no project facets.
  const load = app.slice(app.indexOf("async function loadRouteData"));
  assert.ok(load.indexOf('if (S.tab === "plan")') < load.indexOf("linkAwareFacets("));
  assert.match(load, /if \(S\.tab === "plan"\) \{[\s\S]*?return \{ units: \[\], total: 0, graphIndex: null, facets: \{\} \};/);
  // Filter changes (Min. links included) on an open topic go to the topic
  // page before any project facet is fetched or computed.
  const apply = app.slice(app.indexOf("async function applyFiltersIncremental"));
  const topicBranch = apply.indexOf("ctx.topicPage.applyFilters");
  assert.ok(topicBranch > 0);
  for (const marker of ["linkAwareFacets(", "api.units(", "S.facets ="]) {
    assert.ok(topicBranch < apply.indexOf(marker), marker);
  }
});

test("a line's automatic topic takes the same path as a stored topic", () => {
  const a = filterTopic(stored, { min_links: 1 });
  const b = filterTopic(line, { min_links: 1 });
  assert.deepEqual(b.facets.time, a.facets.time);
  assert.deepEqual(b.members.map((node) => node.id), a.members.map((node) => node.id));
  assert.deepEqual(filterTopic(line).facets.time, filterTopic(stored).facets.time);
});
