import assert from "node:assert/strict";
import test from "node:test";
import {
  AGGREGATE_FRESH_MS, OWN_WRITE_WAIT_MS, cacheRoute, entryKey, identityScope, mergeBodiesDelta,
  mergeSnapshotDelta, normalizeRoute, planEviction, prefetchPlan, servePolicy, slowConnection,
  staleAfterDelta,
} from "../../ark_studio/web/js/client-cache-model.js";

const origin = "https://ark.example";

test("keys separate servers, accounts, schema and projects; never carry credentials", () => {
  const alice = identityScope({ origin, cacheId: "aaa", schema: 1 });
  const bob = identityScope({ origin, cacheId: "bbb", schema: 1 });
  const other = identityScope({ origin: "https://other.example", cacheId: "aaa", schema: 1 });
  const newer = identityScope({ origin, cacheId: "aaa", schema: 2 });
  assert.equal(new Set([alice, bob, other, newer]).size, 4);
  assert.equal(identityScope({ origin, cacheId: "" }), "", "no account, no cache");
  const a = entryKey({ identity: alice, project: "p1" }, "/api/graph/snapshot?bodies=0");
  const b = entryKey({ identity: alice, project: "p2" }, "/api/graph/snapshot?bodies=0");
  assert.notEqual(a, b);
  assert.equal(entryKey({ identity: alice, project: "" }, "/api/bootstrap"), "", "project data needs a project");
  assert.ok(entryKey({ identity: alice, project: "" }, "/api/me"), "account data does not");
  assert.equal(normalizeRoute("/api/units?types=a&limit=5&mobile_token=SECRET&ark_project=p1"),
    "/api/units?limit=5&types=a");
  assert.equal(normalizeRoute("/api/units?b=2&a=1"), normalizeRoute("/api/units?a=1&b=2"));
  assert.ok(!entryKey({ identity: alice, project: "p" }, "/api/units?mobile_token=SECRET").includes("SECRET"));
});

test("private and live routes are never cacheable", () => {
  for (const path of ["/api/session-contexts/7/transcript", "/api/session-contexts/7/raw",
    "/api/agent-sessions/s1/logs?offset=0", "/api/agent-sessions/s1/artifacts/read?path=a",
    "/api/chat", "/api/workspace/file?path=a", "/api/workspace/terminals/t1", "/api/org/users",
    "/api/auth/session", "/api/personal/settings?scope=assistant", "/api/search/nodes?q=x",
    "/api/events", "/api/changes?since=x", "/api/revision", "/api/mobile/session"]) {
    assert.equal(cacheRoute(path), null, path);
  }
  for (const [path, kind] of [["/api/bootstrap", "bootstrap"], ["/api/units/12", "unit"],
    ["/api/units/T-0001/members?full=1", "topic"], ["/api/graph/snapshot?bodies=0", "snapshot"],
    ["/api/project/overview", "overview"], ["/api/units?limit=2", "units"]]) {
    assert.equal(cacheRoute(path).kind, kind, path);
  }
});

test("serve policy: fresh, stale-while-revalidate or wait", () => {
  const now = 1_000_000;
  const rule = cacheRoute("/api/units/1");
  const aggregate = cacheRoute("/api/bootstrap");
  const entry = { validatedAt: now - 1000 };
  const live = { live: true, liveSince: now - 5000, now };
  assert.equal(servePolicy(null, rule, live), "network");
  assert.equal(servePolicy(entry, rule, live), "fresh");
  assert.equal(servePolicy(entry, rule, live, { fresh: true }), "network");
  assert.equal(servePolicy({ ...entry, stale: true }, rule, live), "swr");
  assert.equal(servePolicy(entry, rule, { ...live, live: false }), "swr", "no stream: revalidate");
  assert.equal(servePolicy({ validatedAt: now - 9000 }, rule, live), "swr", "checked before the stream");
  assert.equal(servePolicy({ validatedAt: now - AGGREGATE_FRESH_MS - 1 }, aggregate,
    { ...live, liveSince: 0 }), "swr");
  assert.equal(servePolicy(entry, rule, { ...live, ownWriteAt: now - 10 }), "network", "own write: show it");
  assert.equal(servePolicy(entry, rule, { ...live, ownWriteAt: now - OWN_WRITE_WAIT_MS - 1, now: now + 0 }),
    servePolicy(entry, rule, { ...live, ownWriteAt: now - OWN_WRITE_WAIT_MS - 1 }));
});

test("eviction drops least recently used projects first and keeps the current one", () => {
  const entries = [
    { key: "a1", project: "a", bytes: 40, used: 1, kind: "snapshot" },
    { key: "a2", project: "a", bytes: 10, used: 2, kind: "unit" },
    { key: "b1", project: "b", bytes: 40, used: 5, kind: "snapshot" },
    { key: "c1", project: "c", bytes: 40, used: 9, kind: "snapshot" },
    { key: "c2", project: "c", bytes: 30, used: 3, kind: "unit" },
    { key: "c3", project: "c", bytes: 20, used: 4, kind: "unit" },
  ];
  assert.deepEqual(planEviction(entries, 1000), []);
  assert.deepEqual(planEviction(entries, 150, { protect: "c" }).sort(), ["a1", "a2"]);
  assert.deepEqual(planEviction(entries, 100, { protect: "c" }).sort(), ["a1", "a2", "b1"]);
  // Still too big: trim the current project, details before its snapshot.
  assert.deepEqual(planEviction(entries, 45, { protect: "c" }), ["a1", "a2", "b1", "c2", "c3"]);
});

const node = (id, updated, extra = {}) => ({ id, updated_at: updated, title: `N${id}`, search_body: `body ${id}`, ...extra });

test("snapshot delta merge equals the server's full reload", () => {
  const before = {
    nodes: [node(3, "2025-01-03"), node(2, "2025-01-02"), node(1, "2025-01-01")],
    edges: [{ id: 1, source: 2, target: 1 }, { id: 2, source: 3, target: 2 }, { id: 3, source: 3, target: 1 }],
    total: 3, cursor: "x.5", bodies: true,
  };
  // Server side: node 2 renamed (now newest), node 3 archived, node 4 added
  // with a link to 1; link 1 deleted.
  const delta = {
    cursor: "x.9", reset: false,
    units: [node(2, "2025-02-01", { title: "Renamed" }), node(4, "2025-01-05"), node(1, "2025-01-01")],
    deleted: [3], touched: [1, 2, 3, 4],
    edges: [{ id: 7, source: 4, target: 1 }],
  };
  const after = mergeSnapshotDelta(before, delta);
  assert.deepEqual(after.nodes.map((n) => n.id), [2, 4, 1]);
  assert.equal(after.nodes[0].title, "Renamed");
  assert.deepEqual(after.edges, [{ id: 7, source: 4, target: 1 }]);
  assert.equal(after.total, 3);
  assert.equal(after.cursor, "x.9");
  assert.equal(before.nodes.length, 3, "input unchanged");
  // Equal timestamps order by id descending, as the server does.
  const tie = mergeSnapshotDelta({ nodes: [node(5, "t")], edges: [] },
    { cursor: "y", units: [node(9, "t"), node(7, "t")], deleted: [], touched: [7, 9], edges: [] });
  assert.deepEqual(tie.nodes.map((n) => n.id), [9, 7, 5]);
  assert.equal(mergeSnapshotDelta(before, { ...delta, reset: true }), null);
});

test("lite snapshots stay lite; bodies merge separately", () => {
  const lite = { nodes: [{ id: 1, updated_at: "a" }], edges: [], bodies: false, cursor: "c.1" };
  const delta = { cursor: "c.2", units: [node(1, "b"), node(2, "c")], deleted: [], touched: [1, 2], edges: [] };
  const merged = mergeSnapshotDelta(lite, delta);
  assert.ok(merged.nodes.every((n) => !("search_body" in n)));
  assert.equal(merged.bodies, false);
  const bodies = mergeBodiesDelta({ cursor: "c.1", bodies: [[1, "old"], [3, "gone"]] },
    { ...delta, deleted: [3] });
  assert.deepEqual(bodies.bodies, [[1, "body 1"], [2, "body 2"]]);
  assert.equal(mergeBodiesDelta({ bodies: [] }, { ...delta, bodies: false }), null);
});

test("a delta marks touched details and every aggregate stale, not account data", () => {
  const delta = { touched: [4, 9], deleted: [] };
  assert.equal(staleAfterDelta("/api/units/4", delta), true);
  assert.equal(staleAfterDelta("/api/units/5", delta), false);
  assert.equal(staleAfterDelta("/api/units/4/comments", delta), true);
  assert.equal(staleAfterDelta("/api/units/T-0004", delta), true, "uid refs are checked conservatively");
  assert.equal(staleAfterDelta("/api/bootstrap", delta), true);
  assert.equal(staleAfterDelta("/api/units?limit=5", delta), true);
  assert.equal(staleAfterDelta("/api/units/T-1/members", delta), true);
  assert.equal(staleAfterDelta("/api/graph/snapshot?bodies=0", delta), false, "merged instead");
  assert.equal(staleAfterDelta("/api/graph/snapshot?bodies=0", { reset: true }), true);
  assert.equal(staleAfterDelta("/api/me", delta), false);
  assert.equal(staleAfterDelta("/api/units/5", { reset: true }), true);
});

test("prefetch plan: neighbours first, skips cached, slow links keep only neighbours", () => {
  const groups = { neighbours: [2, 3], needs: [3, 4], recent: ["5"], visible: [6, 7, 8] };
  assert.deepEqual(prefetchPlan(groups, { budget: 10 }), ["2", "3", "4", "5", "6", "7", "8"]);
  assert.deepEqual(prefetchPlan(groups, { budget: 3, cached: new Set(["2"]) }), ["3", "4", "5"]);
  assert.deepEqual(prefetchPlan(groups, { slow: true }), ["2", "3"]);
  assert.equal(slowConnection({ saveData: true }), true);
  assert.equal(slowConnection({ effectiveType: "2g" }), true);
  assert.equal(slowConnection({ effectiveType: "4g" }), false);
  assert.equal(slowConnection(undefined), false);
});

test("Execute data is cached per account and project and always revalidated", () => {
  const alice = identityScope({ origin, cacheId: "aaa", schema: 1 });
  const bob = identityScope({ origin, cacheId: "bbb", schema: 1 });
  for (const [path, kind] of [["/api/agent-sessions", "sessions"], ["/api/agent-sessions/presets", "session-meta"],
    ["/api/agent-sessions/s-1", "session"], ["/api/agent-sessions/s-1/transcript", "transcript"],
    ["/api/session-contexts", "contexts"], ["/api/gpu/dashboard", "gpu"], ["/api/resources/graph", "resources"],
    ["/api/worktrees", "worktrees"], ["/api/workspace/changes?base=HEAD", "changes"]]) {
    const rule = cacheRoute(path);
    assert.equal(rule?.kind, kind, path);
    assert.equal(rule.scope, "project", path);
    assert.notEqual(entryKey({ identity: alice, project: "p" }, path), entryKey({ identity: bob, project: "p" }, path));
    // Never served as fresh: no project revision covers these.
    assert.equal(servePolicy({ validatedAt: 10 }, rule, { live: true, liveSince: 1, now: 11 }), "swr", path);
  }
});
