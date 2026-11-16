package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Background polling diff for notifications: compares the previous snapshot
 * with fresh API data and reports only transitions (never the first sight).
 */
public final class Watch {
    public static final String RUN_FINISHED = "run_finished";
    public static final String RUN_FAILED = "run_failed";
    public static final String TASK_REVIEW = "task_review";
    public static final String GPU_FINISHED = "gpu_finished";

    public static final class Event {
        public final String type;
        public final String id;
        public final String title;
        public final String detail;

        Event(String type, String id, String title, String detail) {
            this.type = type;
            this.id = id;
            this.title = title;
            this.detail = detail;
        }
    }

    /** id → status for runs, review tasks and GPU jobs. */
    public static final class Snapshot {
        public final Map<String, String> runs = new LinkedHashMap<>();
        public final Map<String, String> reviews = new LinkedHashMap<>();
        public final Map<String, String> gpu = new LinkedHashMap<>();
        public boolean initialized;

        public String toJson() {
            JSONObject json = new JSONObject();
            try {
                json.put("runs", new JSONObject(runs));
                json.put("reviews", new JSONObject(reviews));
                json.put("gpu", new JSONObject(gpu));
                json.put("initialized", initialized);
            } catch (JSONException impossible) {
                throw new IllegalStateException(impossible);
            }
            return json.toString();
        }

        public static Snapshot fromJson(String text) {
            Snapshot snapshot = new Snapshot();
            if (text == null || text.isEmpty()) return snapshot;
            try {
                JSONObject json = new JSONObject(text);
                copy(json.optJSONObject("runs"), snapshot.runs);
                copy(json.optJSONObject("reviews"), snapshot.reviews);
                copy(json.optJSONObject("gpu"), snapshot.gpu);
                snapshot.initialized = json.optBoolean("initialized", false);
            } catch (JSONException broken) {
                return new Snapshot();
            }
            return snapshot;
        }

        private static void copy(JSONObject from, Map<String, String> to) {
            if (from == null) return;
            Iterator<String> keys = from.keys();
            while (keys.hasNext()) {
                String key = keys.next();
                to.put(key, from.optString(key));
            }
        }
    }

    private Watch() {
    }

    /**
     * Builds the next snapshot and appends events to {@code events}.
     *
     * @param sessions  {@code GET /api/agent-sessions} "sessions" (may be null when unavailable)
     * @param reviewTasks {@code GET /api/units?type=task&status=review} "items" (may be null)
     * @param gpuJobs   {@code GET /api/gpu/dashboard} "jobs" (may be null)
     * @param user      only GPU jobs owned by this account notify (empty = all)
     */
    public static Snapshot diff(Snapshot previous, JSONArray sessions, JSONArray reviewTasks, JSONArray gpuJobs,
                                String user, List<Event> events) {
        Snapshot next = new Snapshot();
        next.initialized = true;
        boolean notify = previous != null && previous.initialized;
        if (sessions == null && previous != null) next.runs.putAll(previous.runs);
        for (int i = 0; sessions != null && i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s == null) continue;
            String id = s.optString("id");
            String status = s.optString("status");
            next.runs.put(id, status);
            String before = previous == null ? null : previous.runs.get(id);
            if (notify && before != null && Transcript.isActiveStatus(before) && !Transcript.isActiveStatus(status)) {
                String title = s.optString("title", id);
                events.add(new Event("errored".equals(status) ? RUN_FAILED : RUN_FINISHED, id, title,
                        s.optString("error", "")));
            }
        }
        if (reviewTasks == null && previous != null) next.reviews.putAll(previous.reviews);
        for (int i = 0; reviewTasks != null && i < reviewTasks.length(); i++) {
            JSONObject t = reviewTasks.optJSONObject(i);
            if (t == null || !"review".equals(t.optString("status"))) continue;
            String uid = t.optString("uid", String.valueOf(t.opt("id")));
            next.reviews.put(uid, "review");
            if (notify && (previous == null || !previous.reviews.containsKey(uid))) {
                events.add(new Event(TASK_REVIEW, uid, t.optString("title", uid), ""));
            }
        }
        if (gpuJobs == null && previous != null) next.gpu.putAll(previous.gpu);
        for (int i = 0; gpuJobs != null && i < gpuJobs.length(); i++) {
            JSONObject j = gpuJobs.optJSONObject(i);
            if (j == null) continue;
            String owner = j.optString("owner", "");
            if (user != null && !user.isEmpty() && !owner.isEmpty() && !owner.equalsIgnoreCase(user)) continue;
            String id = j.optString("id");
            String status = j.optString("status");
            next.gpu.put(id, status);
            String before = previous == null ? null : previous.gpu.get(id);
            boolean wasLive = "queued".equals(before) || "starting".equals(before) || "running".equals(before);
            boolean done = "completed".equals(status) || "errored".equals(status) || "canceled".equals(status)
                    || "succeeded".equals(status) || "failed".equals(status);
            if (notify && wasLive && done) {
                String label = j.optString("job", j.optString("task", id));
                events.add(new Event(GPU_FINISHED, id, label, status));
            }
        }
        return next;
    }

    public static List<Event> list() {
        return new ArrayList<>();
    }
}
