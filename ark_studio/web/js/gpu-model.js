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

// ---------------------------------------------------------------- occupied-only
// "Occupied" = a server where at least one GPU has a process of this project's
// running job: exactly the cards that carry the blue highlight (gpuOwnership
// above, same probe and job data; no extra probe).

const ACTIVE_JOB = ["starting", "running"];

/** True when one of `jobs` (this project's) owns a process on any GPU of the server. */
export function serverOccupied(server, jobs) {
  const system = server?.system || {};
  const mine = (jobs || []).filter((job) => ACTIVE_JOB.includes(job.status) && job.server === server?.server);
  if (!mine.length) return false;
  return (server?.gpus || []).some((gpu) => gpuOwnership(gpu, system.processes || [], mine).jobs.length > 0);
}

/**
 * The cards to draw.  Off: every server in probe order.  On: only occupied
 * servers, occupied first, probe order kept among them.  `count` feeds the
 * toggle label; `empty` asks for the "not using any server" line.
 */
export function occupiedView(servers, jobs, occupiedOnly = false) {
  const list = (servers || []).map((server) => ({ server, occupied: serverOccupied(server, jobs) }));
  const count = list.filter((item) => item.occupied).length;
  const ordered = occupiedOnly
    ? [...list.filter((item) => item.occupied), ...list.filter((item) => !item.occupied)]
    : list;
  const shown = occupiedOnly ? ordered.filter((item) => item.occupied) : ordered;
  return { shown, count, empty: Boolean(occupiedOnly && list.length && !shown.length) };
}

// The toggle is remembered per account and project on this browser.
export const OCCUPIED_PREFIX = "ark.gpuOccupiedOnly.v1";

export function occupiedKey(user = "", projectId = "") {
  const part = (value) => encodeURIComponent(String(value || "_")).replaceAll(".", "%2E");
  return `${OCCUPIED_PREFIX}.${part(user)}.${part(projectId)}`;
}

export function loadOccupiedOnly(storage, user, projectId) {
  try { return storage?.getItem(occupiedKey(user, projectId)) === "1"; } catch { return false; }
}

export function saveOccupiedOnly(storage, user, projectId, on) {
  try {
    if (on) storage?.setItem(occupiedKey(user, projectId), "1");
    else storage?.removeItem(occupiedKey(user, projectId));
  } catch { /* storage full or disabled */ }
}
