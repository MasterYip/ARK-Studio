// Run: node --test tests/js/resources_model.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  buildResourcesModel, utilisation, occupancyShares, agoKey, ageSeconds, rowBox, CARD_W,
} from "../../ark_studio/web/js/resources-model.js";

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

// ------------------------------------------------- storage, fold, controls
import { readFileSync } from "node:fs";
import {
  storageResources, usageTone, formatBytes, setCollapsed, toggleCollapsed, collapsibleIds,
  resourcesKeyAction, nearestInDirection, cardsInBox, collapseToggleBox, CARD_METRICS,
} from "../../ark_studio/web/js/resources-model.js";

const withStorage = {
  ...payload,
  devices: payload.devices.map((d) => (d.id !== "gpu:lab" ? d : { ...d, storage: [
    { mount: "/data", used_bytes: 9 * 2 ** 40, total_bytes: 10 * 2 ** 40, free_bytes: 2 ** 40, percent: 90, data: true },
    { mount: "/", used_bytes: 2 ** 30, total_bytes: 4 * 2 ** 30, free_bytes: 3 * 2 ** 30, percent: 25, data: false },
  ] })),
};

test("a GPU server's storage becomes storage rows after its GPUs and CPU", () => {
  const rows = storageResources(withStorage.devices[1]);
  assert.deepEqual(rows.map((r) => [r.kind, r.label, r.metrics.disk_pct, r.metrics.data]),
    [["storage", "/data", 90, true], ["storage", "/", 25, false]]);
  assert.deepEqual(storageResources({ id: "x" }), []);
  assert.deepEqual(storageResources({ id: "x", storage: [{ percent: 3 }] }), [], "rows need a mount");
  const model = buildResourcesModel(withStorage);
  const lab = model.cards.find((c) => c.id === "gpu:lab");
  assert.deepEqual(lab.rows.map((r) => r.kind), ["gpu", "gpu", "cpu", "storage", "storage"]);
  assert.equal(model.counts.types.storage, 3, "filter chips count server storage");
  assert.equal(utilisation(lab.rows.find((r) => r.label === "/data")).util, 0.9);
  const storageOnly = buildResourcesModel(withStorage, { types: new Set(["storage"]) });
  assert.deepEqual(storageOnly.cards.map((c) => c.id), ["ark", "gpu:lab"]);
});

test("usage colours use the GPU board's memory thresholds; byte sizes are binary", () => {
  assert.equal(usageTone(null), null);
  assert.equal(usageTone(59.9), "ok");
  assert.equal(usageTone(60), "warn");
  assert.equal(usageTone(84.9), "warn");
  assert.equal(usageTone(85), "danger");
  assert.equal(usageTone("90"), "danger");
  assert.equal(formatBytes(512), "512 B");
  assert.equal(formatBytes(1536 * 2 ** 20), "1.5 GiB");
  assert.equal(formatBytes(10 * 2 ** 40), "10 TiB");
  assert.equal(formatBytes(916 * 2 ** 30), "916 GiB");
  assert.equal(formatBytes(null), "?");
});

test("folded cards keep their header and summary, hide their rows and count them", () => {
  const open = buildResourcesModel(withStorage);
  const folded = buildResourcesModel(withStorage, { collapsed: new Set(["gpu:lab", "gpu:old"]) });
  const lab = folded.cards.find((c) => c.id === "gpu:lab");
  assert.equal(lab.collapsed, true);
  assert.deepEqual(lab.rows, []);
  assert.equal(lab.allRows.length, 5);
  assert.equal(lab.hiddenCount, 5);
  assert.equal(lab.h, open.cards.find((c) => c.id === "gpu:lab").h - 5 * CARD_METRICS.ROW_H);
  const old = folded.cards.find((c) => c.id === "gpu:old");
  assert.equal(old.collapsible, false, "nothing to fold");
  assert.equal(old.collapsed, false);
  assert.deepEqual(collapsibleIds(folded.cards).sort(), ["ark", "gpu:lab", "host:alice", "host:bob"]);
  assert.deepEqual(collapsibleIds(folded.cards, ["gpu:old", "ark"]), ["ark"]);
  const box = collapseToggleBox(lab);
  assert.ok(box.x + box.w <= lab.w && box.y + box.h <= CARD_METRICS.HEADER_H);
  const owned = folded.cards.find((c) => c.id === "host:alice");
  assert.ok(collapseToggleBox(owned).x < box.x, "left of the owner avatar");
});

test("fold state: set, toggle one card, toggle a multi-selection together", () => {
  const start = new Set(["a"]);
  assert.deepEqual([...setCollapsed(start, ["b", "c"], true)].sort(), ["a", "b", "c"]);
  assert.deepEqual([...setCollapsed(start, ["a", "x"], false)], []);
  assert.deepEqual([...start], ["a"], "inputs are not mutated");
  assert.deepEqual([...toggleCollapsed(start, "a")], []);
  assert.deepEqual([...toggleCollapsed(start, "b")].sort(), ["a", "b"]);
  // The clicked card decides the direction; the selection follows it.
  assert.deepEqual([...toggleCollapsed(start, "b", ["a", "b", "c"])].sort(), ["a", "b", "c"]);
  assert.deepEqual([...toggleCollapsed(start, "a", ["a", "b"])], []);
  assert.deepEqual([...toggleCollapsed(start, "z", ["a", "b"])].sort(), ["a", "z"], "outside the selection: alone");
});

const key = (k, extra = {}) => ({ key: k, ctrlKey: false, metaKey: false, altKey: false, shiftKey: false,
  target: { tagName: "DIV" }, ...extra });

test("keyboard map matches the Node graph and Mind's fold keys", () => {
  const table = {
    "?": "help", f: "fit", F: "fit", r: "mode-read", R: "mode-read", e: "mode-edit", E: "mode-edit",
    "+": "expand", "=": "expand", "-": "collapse", "*": "expand-all", "/": "collapse-all",
    ArrowLeft: "nav-left", ArrowRight: "nav-right", ArrowUp: "nav-up", ArrowDown: "nav-down",
    Enter: "open", Escape: "clear", Tab: "none", Backspace: "none", x: null, Delete: null, " ": null,
  };
  for (const [k, want] of Object.entries(table)) assert.equal(resourcesKeyAction(key(k)), want, k);
  assert.equal(resourcesKeyAction(key("a", { ctrlKey: true })), "select-all");
  assert.equal(resourcesKeyAction(key("A", { metaKey: true })), "select-all");
  assert.equal(resourcesKeyAction(key("a", { ctrlKey: true, shiftKey: true })), null);
  assert.equal(resourcesKeyAction(key("f", { ctrlKey: true })), null, "Ctrl+F stays the browser's");
  assert.equal(resourcesKeyAction(key("*", { shiftKey: true })), "expand-all", "* needs Shift on most layouts");
  assert.equal(resourcesKeyAction(key("+", { ctrlKey: true })), null, "Ctrl++ stays browser zoom");
  assert.equal(resourcesKeyAction(key("Escape"), { helpOpen: true }), "close-help");
  assert.equal(resourcesKeyAction(key("Enter"), { mode: "edit" }), null, "edit mode: Enter does not open");
  assert.equal(resourcesKeyAction(key("Tab"), { mode: "edit" }), null);
  for (const tagName of ["INPUT", "TEXTAREA", "SELECT"]) {
    assert.equal(resourcesKeyAction(key("f", { target: { tagName } })), null, tagName);
  }
  assert.equal(resourcesKeyAction(key("/", { target: { tagName: "DIV", isContentEditable: true } })), null);
});

test("Mind's fold keys in graph.js are the ones the Resources graph mirrors", () => {
  const source = readFileSync(new URL("../../ark_studio/web/js/graph.js", import.meta.url), "utf8");
  const body = source.slice(source.indexOf("  _onKeyDown(event) {"), source.indexOf("  _mindParents() {"));
  const pairs = [...body.matchAll(/event\.key === "([*/+=-])"[^]*?this\._(expand|collapse)Selected\((true|false)\)/g)]
    .map((m) => `${m[1]}:${m[2]}:${m[3]}`);
  assert.deepEqual(pairs, ["+:expand:false", "-:collapse:false", "*:expand:true", "/:collapse:true"]);
  for (const k of ["f", "r", "e", "?"]) assert.ok(body.includes(`event.key === "${k}"`), k);
});

test("arrow navigation and box selection", () => {
  const cards = [
    { id: "a", x: 0, y: 0, w: 100, h: 50 }, { id: "b", x: 300, y: 10, w: 100, h: 50 },
    { id: "c", x: 300, y: 400, w: 100, h: 50 }, { id: "d", x: 0, y: 300, w: 100, h: 50 },
  ];
  assert.equal(nearestInDirection(cards, "a", "right"), "b");
  assert.equal(nearestInDirection(cards, "a", "down"), "d");
  assert.equal(nearestInDirection(cards, "b", "down"), "c");
  assert.equal(nearestInDirection(cards, "a", "left"), null);
  assert.equal(nearestInDirection(cards, null, "left"), "a", "no selection: the first card");
  assert.equal(nearestInDirection([], null, "up"), null);
  const identity = (x, y) => ({ x, y });
  assert.deepEqual(cardsInBox(cards, { x1: -10, y1: -10, x2: 260, y2: 20 }, identity), ["a", "b"], "touching");
  assert.deepEqual(cardsInBox(cards, { x1: 260, y1: 20, x2: -10, y2: -10 }, identity, true), [], "contain");
  assert.deepEqual(cardsInBox(cards, { x1: -60, y1: -30, x2: 60, y2: 30 }, identity, true), ["a"]);
});
