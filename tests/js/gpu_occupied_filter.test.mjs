import test from "node:test";
import assert from "node:assert/strict";
import {
  serverOccupied, occupiedView, occupiedKey, loadOccupiedOnly, saveOccupiedOnly,
} from "../../ark_studio/web/js/gpu-model.js";

const gpu = (index) => ({ index, memory_total_mb: 24000, memory_used_mb: 8000, utilization_pct: 50 });
const server = (name, processes = []) => ({ server: name, ok: true, gpus: [gpu(0), gpu(1)], system: { processes } });
const job = (id, srv, extra = {}) => ({ id, server: srv, status: "running", gpu_indices: [1], ...extra });

const s1 = server("gpu-n1");
const s2 = server("gpu-n2", [{ gpu_index: 1, job_id: "j2", memory_mb: 4000 }]);
const s3 = server("gpu-n3", [{ gpu_index: 0, job_id: "other", memory_mb: 9000 }]);
const s4 = server("gpu-n4", [{ gpu_index: 1, job_id: "j4", memory_mb: 2000 }]);
const jobs = [job("j2", "gpu-n2"), job("j4", "gpu-n4")];

test("occupied means one of this project's running jobs owns a GPU process (the blue highlight)", () => {
  assert.equal(serverOccupied(s2, jobs), true);
  assert.equal(serverOccupied(s1, jobs), false);
  assert.equal(serverOccupied(s3, jobs), false, "a foreign process is not ours");
  // A queued or finished job, or a job on another server, does not count.
  assert.equal(serverOccupied(s2, [job("j2", "gpu-n2", { status: "queued" })]), false);
  assert.equal(serverOccupied(s2, [job("j2", "gpu-n2", { status: "done" })]), false);
  assert.equal(serverOccupied(s2, [job("j2", "gpu-n1")]), false);
  // A running job with no process on the GPU yet carries no highlight.
  assert.equal(serverOccupied(s1, [job("j9", "gpu-n1")]), false);
});

test("off: every server in probe order, with the occupied count", () => {
  const view = occupiedView([s1, s2, s3, s4], jobs, false);
  assert.deepEqual(view.shown.map((x) => x.server.server), ["gpu-n1", "gpu-n2", "gpu-n3", "gpu-n4"]);
  assert.equal(view.count, 2);
  assert.equal(view.empty, false);
});

test("on: only occupied servers, probe order kept among them", () => {
  const view = occupiedView([s4, s1, s2, s3], jobs, true);
  assert.deepEqual(view.shown.map((x) => x.server.server), ["gpu-n4", "gpu-n2"]);
  assert.ok(view.shown.every((x) => x.occupied));
  assert.equal(view.count, 2);
  assert.equal(view.empty, false);
});

test("on with nothing occupied: empty state; off never reports empty", () => {
  const on = occupiedView([s1, s3], jobs, true);
  assert.deepEqual(on.shown, []);
  assert.equal(on.count, 0);
  assert.equal(on.empty, true);
  assert.equal(occupiedView([s1, s3], jobs, false).empty, false);
  // No servers at all is the registration note's case, not this one.
  assert.equal(occupiedView([], jobs, true).empty, false);
});

test("the toggle is remembered per account and project", () => {
  const data = new Map();
  const storage = { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, v), removeItem: (k) => data.delete(k) };
  assert.equal(loadOccupiedOnly(storage, "alice", "prj_a"), false);
  saveOccupiedOnly(storage, "alice", "prj_a", true);
  assert.equal(loadOccupiedOnly(storage, "alice", "prj_a"), true);
  assert.equal(loadOccupiedOnly(storage, "alice", "prj_b"), false);
  assert.equal(loadOccupiedOnly(storage, "bob", "prj_a"), false);
  assert.notEqual(occupiedKey("a.b", "c"), occupiedKey("a", "b.c"));
  saveOccupiedOnly(storage, "alice", "prj_a", false);
  assert.equal(loadOccupiedOnly(storage, "alice", "prj_a"), false);
  assert.equal(loadOccupiedOnly(null, "alice", "prj_a"), false);
  const broken = { getItem() { throw new Error("denied"); }, setItem() { throw new Error("full"); } };
  assert.equal(loadOccupiedOnly(broken, "a", "p"), false);
  saveOccupiedOnly(broken, "a", "p", true);
});
