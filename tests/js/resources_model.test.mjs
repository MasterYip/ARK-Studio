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
  const openLab = open.cards.find((c) => c.id === "gpu:lab");
  assert.equal(lab.h, openLab.h - openLab.rowHeights.reduce((sum, height) => sum + height, 0));
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
    "+": "expand", "=": "expand", "-": "collapse", "*": "expand", "/": "collapse",
    ArrowLeft: "nav-left", ArrowRight: "nav-right", ArrowUp: "nav-up", ArrowDown: "nav-down",
    Enter: "open", Escape: "clear", Tab: "none", Backspace: "none", x: null, Delete: null, " ": null,
  };
  for (const [k, want] of Object.entries(table)) assert.equal(resourcesKeyAction(key(k)), want, k);
  assert.equal(resourcesKeyAction(key("a", { ctrlKey: true })), "select-all");
  assert.equal(resourcesKeyAction(key("A", { metaKey: true })), "select-all");
  assert.equal(resourcesKeyAction(key("a", { ctrlKey: true, shiftKey: true })), null);
  assert.equal(resourcesKeyAction(key("f", { ctrlKey: true })), null, "Ctrl+F stays the browser's");
  assert.equal(resourcesKeyAction(key("*", { shiftKey: true })), "expand", "* needs Shift on most layouts");
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

// ---------------------------------------------------------------- T-0019
import {
  foldTargets, rowKind, rowHeight, gpuDetail, cpuDetail, storageDetail, formatMb, mergeProbeDetails,
  USAGE_WARN_PCT, USAGE_DANGER_PCT,
} from "../../ark_studio/web/js/resources-model.js";

test("+ / = expand, - collapses (Shift or not); * and / stay aliases; Ctrl / Alt never fold", () => {
  for (const [k, shiftKey] of [["+", true], ["+", false], ["=", false], ["*", true]]) {
    assert.equal(resourcesKeyAction(key(k, { shiftKey })), "expand", `${k} shift=${shiftKey}`);
  }
  for (const k of ["-", "/"]) assert.equal(resourcesKeyAction(key(k)), "collapse", k);
  for (const mod of ["ctrlKey", "metaKey", "altKey"]) {
    for (const k of ["+", "=", "-"]) assert.equal(resourcesKeyAction(key(k, { [mod]: true })), null, `${mod}+${k}`);
  }
  assert.equal(resourcesKeyAction(key("-", { target: { tagName: "INPUT" } })), null, "typing a minus in a field");
});

test("fold targets: the selection, else the focused card, else nothing", () => {
  const { cards } = buildResourcesModel(withStorage);
  assert.deepEqual(foldTargets(cards, ["gpu:lab", "ark"]).sort(), ["ark", "gpu:lab"]);
  assert.deepEqual(foldTargets(cards, ["gpu:lab"], "ark"), ["gpu:lab"], "the selection wins over the focus");
  assert.deepEqual(foldTargets(cards, [], "ark"), ["ark"]);
  assert.deepEqual(foldTargets(cards, new Set(), "gpu:old"), [], "a card without details");
  assert.deepEqual(foldTargets(cards, [], null), []);
  assert.deepEqual(foldTargets(cards), []);
});

const gpuRow = (metrics, label = "GPU 1 · NVIDIA H20") => ({ id: "g", device: "gpu:lab", kind: "gpu", label, metrics });

test("each GPU gets a full detail row: index, model, utilisation and memory bars, temperature, power, jobs", () => {
  const d = gpuDetail(gpuRow({ index: 1, util_pct: 97, mem_used_mb: 23000, mem_total_mb: 24564, mem_ours_mb: 23000,
    temperature_c: 77, power_w: 350.5, job: "train", ours: true }, "GPU 1 · NVIDIA GeForce RTX 4090"));
  assert.equal(d.title, "GPU 1");
  assert.equal(d.model, "NVIDIA GeForce RTX 4090");
  assert.equal(d.util, 97);
  assert.equal(d.utilTone, "danger");
  assert.equal(Math.round(d.memPct), 94);
  assert.equal(d.memTone, "danger");
  assert.equal(Math.round(d.oursPct), 94);
  assert.equal(d.temperature, 77);
  assert.equal(d.power, 350.5);
  assert.deepEqual(d.jobs, ["train"]);
  assert.equal(d.ours, true);
  const idle = gpuDetail(gpuRow({ index: 0, util_pct: 0, mem_used_mb: 0, mem_total_mb: 97871 }));
  assert.equal(idle.utilTone, "ok");
  assert.equal(idle.memPct, 0);
  assert.equal(idle.temperature, null);
  assert.equal(idle.power, null);
  assert.deepEqual(idle.extra, []);
  const unknown = gpuDetail(gpuRow({ index: 3, util_pct: 5, mem_used_mb: 100, mem_total_mb: null }));
  assert.equal(unknown.memPct, null, "no total: no memory bar value");
  assert.equal(unknown.memTone, null);
});

test("detail rows are taller than one-line rows and the card grows with them", () => {
  const busy = gpuRow({ index: 0, util_pct: 1, mem_used_mb: 1, mem_total_mb: 2, job: "x" });
  const idle = gpuRow({ index: 1, util_pct: 1, mem_used_mb: 1, mem_total_mb: 2 });
  assert.equal(rowKind(idle), "gpu");
  assert.equal(rowHeight(idle), CARD_METRICS.GPU_ROW_H);
  assert.equal(rowHeight(busy), CARD_METRICS.GPU_ROW_H + CARD_METRICS.GPU_EXTRA_H, "a line for jobs / processes");
  assert.equal(rowKind({ kind: "cpu", metrics: { cpu_pct: 3 } }), "cpu");
  assert.equal(rowKind({ kind: "cpu", metrics: {} }), "line");
  assert.equal(rowKind({ kind: "storage", metrics: { disk_pct: 40 } }), "storage");
  assert.equal(rowKind({ kind: "storage", metrics: {} }), "line");
  assert.equal(rowKind({ kind: "database" }), "line");
  const { cards } = buildResourcesModel(withStorage);
  const lab = cards.find((c) => c.id === "gpu:lab");
  assert.deepEqual(lab.rows.map(rowKind), ["gpu", "gpu", "cpu", "storage", "storage"]);
  assert.deepEqual(lab.rowHeights, [CARD_METRICS.GPU_ROW_H, CARD_METRICS.GPU_ROW_H,
    CARD_METRICS.CPU_ROW_H, CARD_METRICS.STORAGE_ROW_H, CARD_METRICS.STORAGE_ROW_H]);
  const boxes = lab.rows.map((_, i) => rowBox(lab, i));
  for (let i = 1; i < boxes.length; i += 1) assert.equal(boxes[i].y, boxes[i - 1].y + lab.rowHeights[i - 1], `row ${i}`);
  const last = boxes.at(-1);
  assert.ok(last.y + last.h <= lab.h - CARD_METRICS.PAD, "rows fit in the card");
  assert.equal(last.h, CARD_METRICS.STORAGE_ROW_H - 4);
});

test("storage bars: mount, used / total, percent, tone; the data mount is marked", () => {
  const rows = storageResources(withStorage.devices[1]);
  const data = storageDetail(rows[0]);
  assert.deepEqual([data.label, data.pct, data.tone, data.data, data.used, data.total],
    ["/data", 90, "danger", true, 9 * 2 ** 40, 10 * 2 ** 40]);
  const root = storageDetail(rows[1]);
  assert.deepEqual([root.label, root.pct, root.tone, root.data], ["/", 25, "ok", false]);
  // The ARK server's project folder: sizes from the device (decimal GB).
  const ark = { metrics: { disk_pct: 72.5, disk_used_gb: 725, disk_total_gb: 1000 } };
  const folder = storageDetail({ kind: "storage", label: "Project folder", metrics: { disk_pct: 72.5 } }, ark);
  assert.deepEqual([folder.pct, folder.tone, folder.used, folder.total, folder.free], [72.5, "warn", 725e9, 1000e9, 275e9]);
  const bare = storageDetail({ kind: "storage", label: "x", metrics: { disk_pct: 10 } });
  assert.deepEqual([bare.used, bare.total, bare.tone], [null, null, "ok"]);
});

test("bar thresholds: amber from 60%, red from 85% for GPU memory, utilisation, CPU, RAM and disks", () => {
  assert.equal(USAGE_WARN_PCT, 60);
  assert.equal(USAGE_DANGER_PCT, 85);
  const tones = (pct) => [
    gpuDetail(gpuRow({ util_pct: pct, mem_used_mb: pct, mem_total_mb: 100 })).utilTone,
    gpuDetail(gpuRow({ util_pct: pct, mem_used_mb: pct, mem_total_mb: 100 })).memTone,
    cpuDetail({ kind: "cpu", metrics: { cpu_pct: pct, mem_used_mb: pct, mem_total_mb: 100 } }).cpuTone,
    cpuDetail({ kind: "cpu", metrics: { cpu_pct: pct, mem_used_mb: pct, mem_total_mb: 100 } }).ramTone,
    storageDetail({ kind: "storage", metrics: { disk_pct: pct } }).tone,
  ];
  for (const [pct, tone] of [[0, "ok"], [59.9, "ok"], [60, "warn"], [84.9, "warn"], [85, "danger"], [100, "danger"]]) {
    assert.deepEqual(tones(pct), Array(5).fill(tone), `${pct}%`);
  }
  assert.equal(formatMb(24564), "24 GiB");
  assert.equal(formatMb(1100), "1.1 GiB");
  assert.equal(formatMb(null), "?");
});

test("power, processes and jobs come from the GPU board's dashboard only when it is the same probe", () => {
  const graph = {
    devices: [{ id: "gpu:a", kind: "gpu-server", label: "a", probe: { probed_at: "2026-11-16T09:00:00Z", ok: true } }],
    resources: [
      { id: "g0", device: "gpu:a", kind: "gpu", label: "GPU 0 · X", metrics: { index: 0, util_pct: 3, temperature_c: 41 } },
      { id: "g1", device: "gpu:a", kind: "gpu", label: "GPU 1 · X", metrics: { index: 1, util_pct: 97 } },
      { id: "c", device: "gpu:a", kind: "cpu", label: "CPU", metrics: { cpu_pct: 3 } },
    ],
  };
  const dashboard = {
    checked_at: "2026-11-16T09:00:20Z",
    probe: { servers: [{ server: "a", ok: true,
      gpus: [{ index: 0, power_w: 70.25, temperature_c: 99 }, { index: 1, power_w: 350.5, temperature_c: 77 }],
      system: { processes: [{ pid: 1234, gpu_index: 0, memory_mb: 1100, job_id: "gpu_abc", sm_pct: 12 },
        { pid: 4567, gpu_index: 0, memory_mb: null, job_id: null, sm_pct: null }] } }] },
    jobs: [{ id: "gpu_abc", task: "Train baseline", owner: "alice" }],
  };
  const merged = mergeProbeDetails(graph, dashboard);
  const [g0, g1, cpu] = merged.resources;
  assert.equal(g0.metrics.power_w, 70.25);
  assert.equal(g0.metrics.temperature_c, 41, "the graph's own figure wins");
  assert.deepEqual(g0.metrics.processes.map((p) => [p.pid, p.memory_mb, p.job, p.owner]),
    [[1234, 1100, "Train baseline", "alice"], [4567, null, null, null]]);
  assert.deepEqual([g1.metrics.power_w, g1.metrics.temperature_c, g1.metrics.processes], [350.5, 77, undefined]);
  assert.equal(cpu, graph.resources[2], "other rows untouched");
  assert.equal(graph.resources[0].metrics.power_w, undefined, "input not mutated");
  const d = gpuDetail(g0);
  assert.deepEqual(d.jobs, ["Train baseline"]);
  assert.deepEqual(d.owners, ["alice"]);
  assert.deepEqual(d.extra.map((e) => e.key), ["jobs", "owners", "processes"]);
  // A later probe (Check now) or another server: nothing is merged.
  assert.equal(mergeProbeDetails(graph, { ...dashboard, checked_at: "2026-11-16T09:10:00Z" }), graph);
  assert.equal(mergeProbeDetails({ ...graph, devices: [{ ...graph.devices[0], label: "b" }] }, dashboard).resources[0].metrics.power_w, undefined);
  assert.equal(mergeProbeDetails(graph, null), graph);
  assert.equal(mergeProbeDetails(graph, { probe: { servers: [] } }), graph);
});

test("a server's mounts keep the probe's order (data mount first), after other storage rows", () => {
  const p = { devices: [{ id: "gpu:s", kind: "gpu-server", storage: [
    { mount: "/data", percent: 90, data: true }, { mount: "/", percent: 40 }, { mount: "/a", percent: 1 }] }],
  resources: [{ id: "loc", device: "gpu:s", kind: "storage", label: "zz location" }] };
  const [card] = buildResourcesModel(p).cards;
  assert.deepEqual(card.rows.map((r) => r.label), ["zz location", "/data", "/", "/a"]);
});

test("bar labels: used / total in the total's unit", async () => {
  const { formatUsedOf } = await import("../../ark_studio/web/js/resources-model.js");
  const GiB = 2 ** 30, TiB = 2 ** 40;
  assert.equal(formatUsedOf(11.7 * GiB, 62.5 * GiB), "11.7 / 62.5 GiB");
  assert.equal(formatUsedOf(1000 * 2 ** 20, 95.6 * GiB), "1 / 95.6 GiB");
  assert.equal(formatUsedOf(6.5 * TiB, 7.2 * TiB), "6.5 / 7.2 TiB");
  assert.equal(formatUsedOf(403 * GiB, 916 * GiB), "403 / 916 GiB");
  assert.equal(formatUsedOf(0, 24564 * 2 ** 20), "0 / 24 GiB");
  assert.equal(formatUsedOf(null, 10 * GiB), "? / 10 GiB");
  assert.equal(formatUsedOf(5, null), "5 B / ?");
});
