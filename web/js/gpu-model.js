// How much of one GPU belongs to this project's running ARK jobs.
//
// Attribution comes from the probe: every GPU process carries the
// ARK_GPU_JOB_ID it inherited from the queue launcher (job_id), its memory from
// `nvidia-smi --query-compute-apps` (memory_mb, null when the driver reports
// [N/A]) and its compute share from `nvidia-smi pmon -s u` (sm_pct, null when
// pmon is unavailable).  The queue's running state decides ownership; task
// filters or task status never hide a live job's usage.

const clampPct = (value) => Math.max(0, Math.min(100, Number(value) || 0));

export function gpuOwnership(gpu, processes, jobs, { processUtilAvailable = false } = {}) {
  const index = gpu?.index;
  const total = Number(gpu?.memory_total_mb) || 0;
  const used = Number(gpu?.memory_used_mb) || 0;
  const util = clampPct(gpu?.utilization_pct);
  const byId = new Map((jobs || [])
    .filter((job) => ["starting", "running"].includes(job.status)
      && (job.gpu_indices || []).map(Number).includes(Number(index)))
    .map((job) => [String(job.id), job]));
  const owned = (processes || []).filter((proc) => Number(proc.gpu_index) === Number(index)
    && proc.job_id && byId.has(String(proc.job_id)));

  let memMb = 0;
  let unknownMemory = 0;
  const memByJob = {};
  for (const proc of owned) {
    if (proc.memory_mb == null) { unknownMemory += 1; continue; }
    memMb += Number(proc.memory_mb) || 0;
    memByJob[proc.job_id] = (memByJob[proc.job_id] || 0) + (Number(proc.memory_mb) || 0);
  }

  let ownedUtil = 0;
  let utilEstimated = false;
  const measured = processUtilAvailable && owned.length && owned.every((proc) => proc.sm_pct != null);
  if (measured) {
    ownedUtil = clampPct(owned.reduce((sum, proc) => sum + (Number(proc.sm_pct) || 0), 0));
  } else if (owned.length) {
    // No per-process sampler: split utilization by share of *used* memory.
    utilEstimated = true;
    ownedUtil = used > 0 ? clampPct(util * Math.min(1, memMb / used)) : 0;
  }

  return {
    jobIds: [...new Set(owned.map((proc) => String(proc.job_id)))],
    jobs: [...new Set(owned.map((proc) => String(proc.job_id)))].map((id) => byId.get(id)),
    memMb,
    memByJob,
    memPct: total ? clampPct(memMb / total * 100) : 0,
    unknownMemory,
    utilPct: ownedUtil,
    utilEstimated,
    // pmon and utilization.gpu are separate samples; keep the total bar >= owned.
    totalUtilPct: Math.max(util, ownedUtil),
    totalMemPct: total ? clampPct(used / total * 100) : 0,
  };
}
