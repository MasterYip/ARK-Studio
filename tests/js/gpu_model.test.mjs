import test from "node:test";
import assert from "node:assert/strict";
import { gpuOwnership } from "../../ark_studio/web/js/gpu-model.js";

const gpu = { index: 0, memory_total_mb: 24000, memory_used_mb: 13000, utilization_pct: 100 };
const job = (id, extra = {}) => ({ id, status: "running", gpu_indices: [0], ...extra });

test("measured compute share comes from pmon, not memory share", () => {
  const own = gpuOwnership(gpu, [{ gpu_index: 0, job_id: "j1", memory_mb: 12000, sm_pct: 97 }],
    [job("j1")], { processUtilAvailable: true });
  assert.equal(own.utilPct, 97);           // old formula showed 100 * 12000/24000 = 50
  assert.equal(own.utilEstimated, false);
  assert.equal(own.memPct, 50);
});

test("without pmon, utilization is split by share of used memory", () => {
  const own = gpuOwnership(gpu, [{ gpu_index: 0, job_id: "j1", memory_mb: 12000, sm_pct: null }],
    [job("j1")], { processUtilAvailable: false });
  assert.equal(Math.round(own.utilPct), 92); // 100 * 12000 / 13000
  assert.equal(own.utilEstimated, true);
});

test("a running job counts regardless of task filters or task status", () => {
  const own = gpuOwnership(gpu, [{ gpu_index: 0, job_id: "j1", memory_mb: 6000, sm_pct: 40 }],
    [job("j1", { task: "T-0099" })], { processUtilAvailable: true });
  assert.equal(own.memMb, 6000);
  assert.deepEqual(own.jobIds, ["j1"]);
});

test("unknown memory is reported, other GPUs and foreign processes are ignored", () => {
  const own = gpuOwnership(gpu, [
    { gpu_index: 0, job_id: "j1", memory_mb: null, sm_pct: 10 },
    { gpu_index: 0, job_id: "j1", memory_mb: 2000, sm_pct: 20 },
    { gpu_index: 1, job_id: "j1", memory_mb: 9000, sm_pct: 90 },
    { gpu_index: 0, job_id: "", memory_mb: 5000, sm_pct: 50 },
    { gpu_index: 0, job_id: "other", memory_mb: 1000, sm_pct: 5 },
  ], [job("j1"), job("done", { status: "succeeded" })], { processUtilAvailable: true });
  assert.equal(own.memMb, 2000);
  assert.equal(own.unknownMemory, 1);
  assert.equal(own.utilPct, 30);
});

test("total bar never drops below the owned share and caps at 100", () => {
  const quiet = { ...gpu, utilization_pct: 20 };
  const own = gpuOwnership(quiet, [
    { gpu_index: 0, job_id: "j1", memory_mb: 1000, sm_pct: 70 },
    { gpu_index: 0, job_id: "j1", memory_mb: 1000, sm_pct: 60 },
  ], [job("j1")], { processUtilAvailable: true });
  assert.equal(own.utilPct, 100);
  assert.equal(own.totalUtilPct, 100);
});

test("no owned processes means nothing highlighted", () => {
  const own = gpuOwnership(gpu, [], [job("j1")], { processUtilAvailable: true });
  assert.equal(own.utilPct, 0);
  assert.equal(own.memPct, 0);
  assert.equal(own.utilEstimated, false);
});
