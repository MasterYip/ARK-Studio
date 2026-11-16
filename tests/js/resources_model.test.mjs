// Run: node --test tests/js/resources_model.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  buildResourcesModel, utilisation, occupancyShares, agoKey, ageSeconds, rowBox, CARD_W,
} from "../../web/js/resources-model.js";

const payload = {
  viewer: "alice",
  devices: [
    { id: "ark", kind: "ark", scope: "shared", status: { state: "online" }, metrics: { disk_pct: 40 } },
    { id: "gpu:lab", kind: "gpu-server", scope: "shared", status: { state: "online" },
      occupancy: { gpus_total: 4, gpus_ours: 1, gpus_busy_others: 2, gpus_free: 1, mem_total_mb: 160000, mem_ours_mb: 20000, mem_others_mb: 60000 } },
    { id: "gpu:old", kind: "gpu-server", scope: "shared", status: { state: "offline" } },
    { id: "host:alice", kind: "host", scope: "personal", mine: true, owner: { username: "alice" }, status: { state: "stale" } },
    { id: "host:bob", kind: "host", scope: "personal", mine: false, owner: { username: "bob" }, status: { state: "unknown" } },
  ],
  resources: [
    { id: "r1", device: "ark", kind: "database", label: "db" },
    { id: "r2", device: "ark", kind: "storage", label: "folder", metrics: { disk_pct: 40 } },
    { id: "g0", device: "gpu:lab", kind: "gpu", label: "GPU 0", metrics: { util_pct: 80, mem_used_mb: 30000, mem_total_mb: 40000, mem_ours_mb: 20000, mem_others_mb: 10000, ours: true } },
    { id: "g1", device: "gpu:lab", kind: "gpu", label: "GPU 1", metrics: { util_pct: 0, mem_used_mb: 0, mem_total_mb: 40000 } },
    { id: "c", device: "gpu:lab", kind: "cpu", label: "CPU", metrics: { cpu_pct: 50, mem_used_mb: 32, mem_total_mb: 128 } },
    { id: "repo", device: "host:alice", kind: "repository", label: "code", owner: "alice" },
    { id: "repo2", device: "host:bob", kind: "repository", label: "code", redacted: true },
  ],
  connections: [
    { id: "a", source: "ark", target: "gpu:lab", kind: "ssh", state: "ok" },
    { id: "b", source: "ark", target: "gpu:old", kind: "ssh", state: "failed" },
    { id: "c", source: "host:alice", target: "ark", kind: "agent-api", state: "ok" },
    { id: "d", source: "host:bob", target: "ark", kind: "agent-api", state: "unknown" },
  ],
};

test("devices become cards with sorted resource rows and sizes", () => {
  const model = buildResourcesModel(payload);
  assert.equal(model.cards.length, 5);
  const lab = model.cards.find((card) => card.id === "gpu:lab");
  assert.deepEqual(lab.rows.map((row) => row.id), ["g0", "g1", "c"], "GPUs before CPU");
  assert.equal(lab.w, CARD_W);
  assert.ok(lab.summary);
  const ark = model.cards.find((card) => card.id === "ark");
  assert.deepEqual(ark.rows.map((row) => row.kind), ["storage", "database"]);
  assert.ok(lab.h > model.cards.find((card) => card.id === "gpu:old").h);
  assert.equal(model.counts.types.gpu, 2);
  assert.equal(model.counts.kinds["gpu-server"], 2);
  const box = rowBox(lab, 1);
  assert.ok(box.y > rowBox(lab, 0).y && box.w < lab.w);
});

test("offline and stale devices are dimmed, personal ones flagged; links to them are dashed", () => {
  const model = buildResourcesModel(payload);
  const card = (id) => model.cards.find((c) => c.id === id);
  assert.equal(card("gpu:old").dim, true);
  assert.equal(card("host:alice").dim, true);
  assert.equal(card("gpu:lab").dim, false);
  assert.equal(card("host:bob").personal, true);
  const link = (id) => model.links.find((l) => l.id === id);
  assert.equal(link("a").dashed, false);
  assert.equal(link("b").dashed, true);
  assert.equal(link("b").failed, true);
  assert.equal(link("c").dashed, true, "the stale end makes the link dashed");
  assert.equal(link("d").dashed, true);
});

test("type filters keep only matching rows and drop empty devices", () => {
  const model = buildResourcesModel(payload, { types: new Set(["gpu"]) });
  assert.deepEqual(model.cards.map((c) => c.id), ["gpu:lab"]);
  assert.deepEqual(model.cards[0].rows.map((r) => r.id), ["g0", "g1"]);
  assert.deepEqual(model.links, [], "links need both ends");
  assert.equal(model.counts.types.repository, 2, "counts ignore the filter");
});

test("mine only keeps my devices, the server and devices where we hold resources", () => {
  const ids = buildResourcesModel(payload, { mineOnly: true }).cards.map((c) => c.id).sort();
  assert.deepEqual(ids, ["ark", "gpu:lab", "host:alice"]);
});

test("utilisation maths for GPUs, CPUs and disks", () => {
  const gpu = utilisation(payload.resources[2]);
  assert.equal(gpu.util, 0.8);
  assert.equal(gpu.mem, 0.75);
  assert.equal(gpu.ours, 0.5);
  assert.equal(gpu.others, 0.25);
  const idle = utilisation(payload.resources[3]);
  assert.deepEqual(idle, { util: 0, mem: 0, ours: 0, others: 0 });
  const derived = utilisation({ kind: "gpu", metrics: { mem_used_mb: 10, mem_total_mb: 20 } });
  assert.equal(derived.others, 0.5, "without a split, used memory counts as others'");
  assert.equal(derived.util, null);
  const cpu = utilisation(payload.resources[4]);
  assert.equal(cpu.util, 0.5);
  assert.equal(cpu.mem, 0.25);
  assert.equal(utilisation({ kind: "storage", metrics: { disk_pct: 150 } }).util, 1, "clamped");
  assert.deepEqual(utilisation({ kind: "repository" }), { util: null, mem: null, ours: null, others: null });
});

test("occupancy shares", () => {
  const shares = occupancyShares(payload.devices[1]);
  assert.deepEqual(shares, { ours: 0.25, others: 0.5, free: 0.25, memOurs: 0.125, memOthers: 0.375 });
  assert.equal(occupancyShares(payload.devices[0]), null);
});

test("relative times", () => {
  assert.deepEqual(agoKey(null), ["resources.ago.never", {}]);
  assert.deepEqual(agoKey(30), ["resources.ago.now", {}]);
  assert.deepEqual(agoKey(600), ["resources.ago.min", { n: 10 }]);
  assert.deepEqual(agoKey(3 * 3600), ["resources.ago.h", { n: 3 }]);
  assert.deepEqual(agoKey(3 * 86400), ["resources.ago.d", { n: 3 }]);
  const now = Date.parse("2026-11-14T12:00:00Z");
  assert.equal(ageSeconds("2026-11-14T11:00:00Z", now), 3600);
  assert.equal(ageSeconds(null, now), null);
});
