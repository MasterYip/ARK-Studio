package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;

/**
 * Port of {@code web/js/gpu-model.js#gpuOwnership}: how much of one GPU belongs
 * to this project's running ARK jobs. Ownership comes from the probe's
 * per-process {@code job_id}; only starting/running queue jobs on this GPU count.
 */
public final class GpuOwnership {
    public final List<String> jobIds = new ArrayList<>();
    public final List<JSONObject> jobs = new ArrayList<>();
    public final Map<String, Double> memByJob = new LinkedHashMap<>();
    public double memMb;
    public double memPct;
    public int unknownMemory;
    public double utilPct;
    public boolean utilEstimated;
    public double totalUtilPct;
    public double totalMemPct;

    static double clampPct(double value) {
        if (Double.isNaN(value)) return 0;
        return Math.max(0, Math.min(100, value));
    }

    private static double num(JSONObject obj, String key) {
        if (obj == null || obj.isNull(key)) return 0;
        double value = obj.optDouble(key, 0);
        return Double.isNaN(value) ? 0 : value;
    }

    public static GpuOwnership compute(JSONObject gpu, JSONArray processes, JSONArray jobList,
                                       boolean processUtilAvailable) {
        GpuOwnership out = new GpuOwnership();
        long index = gpu == null ? Long.MIN_VALUE : gpu.optLong("index", Long.MIN_VALUE);
        double total = num(gpu, "memory_total_mb");
        double used = num(gpu, "memory_used_mb");
        double util = clampPct(num(gpu, "utilization_pct"));

        Map<String, JSONObject> byId = new LinkedHashMap<>();
        for (int i = 0; jobList != null && i < jobList.length(); i++) {
            JSONObject job = jobList.optJSONObject(i);
            if (job == null) continue;
            String status = job.optString("status", "");
            if (!status.equals("starting") && !status.equals("running")) continue;
            JSONArray indices = job.optJSONArray("gpu_indices");
            boolean onThisGpu = false;
            for (int j = 0; indices != null && j < indices.length(); j++) {
                if (indices.optLong(j, Long.MIN_VALUE + 1) == index) onThisGpu = true;
            }
            if (onThisGpu) byId.put(job.optString("id", ""), job);
        }
        List<JSONObject> owned = new ArrayList<>();
        for (int i = 0; processes != null && i < processes.length(); i++) {
            JSONObject proc = processes.optJSONObject(i);
            if (proc == null || proc.optLong("gpu_index", Long.MIN_VALUE + 2) != index) continue;
            String jobId = proc.isNull("job_id") ? "" : proc.optString("job_id", "");
            if (!jobId.isEmpty() && byId.containsKey(jobId)) owned.add(proc);
        }

        boolean allMeasured = !owned.isEmpty();
        double smSum = 0;
        for (JSONObject proc : owned) {
            String jobId = proc.optString("job_id");
            if (proc.isNull("memory_mb") || !proc.has("memory_mb")) {
                out.unknownMemory += 1;
            } else {
                double mem = num(proc, "memory_mb");
                out.memMb += mem;
                Double before = out.memByJob.get(jobId);
                out.memByJob.put(jobId, (before == null ? 0 : before) + mem);
            }
            if (proc.isNull("sm_pct") || !proc.has("sm_pct")) allMeasured = false;
            else smSum += num(proc, "sm_pct");
        }
        if (processUtilAvailable && allMeasured) {
            out.utilPct = clampPct(smSum);
        } else if (!owned.isEmpty()) {
            out.utilEstimated = true;
            out.utilPct = used > 0 ? clampPct(util * Math.min(1, out.memMb / used)) : 0;
        }
        LinkedHashSet<String> ids = new LinkedHashSet<>();
        for (JSONObject proc : owned) ids.add(proc.optString("job_id"));
        for (String id : ids) {
            out.jobIds.add(id);
            out.jobs.add(byId.get(id));
        }
        out.memPct = total > 0 ? clampPct(out.memMb / total * 100) : 0;
        out.totalUtilPct = Math.max(util, out.utilPct);
        out.totalMemPct = total > 0 ? clampPct(used / total * 100) : 0;
        return out;
    }

    /** Web bar colour band: &lt;60 ok, &lt;85 warn, else hot. */
    public static String band(double pct) {
        if (pct < 60) return "ok";
        if (pct < 85) return "warn";
        return "hot";
    }
}
