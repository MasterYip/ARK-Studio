package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * The server's notification feed ({@code GET /api/notifications}, see
 * {@code ark_studio/server/internal/server/notifications.go}) and what to do
 * with it: which items to post, which posted ones to withdraw, how they group
 * per project. Pure Java so the JVM tests cover it; the Android side is
 * {@code platform/Notifications}.
 */
public final class NotifyFeed {
    public static final String KIND_REVIEW = "review";
    public static final String KIND_DECISION = "decision";
    public static final String KIND_APPROVAL = "approval";
    public static final String KIND_SESSION_ERROR = "session_error";
    public static final String KIND_SESSION_DONE = "session_done";
    public static final String KIND_ASSIGNED = "assigned";

    /** Notification channels; decisions and approvals share one. */
    public static final String CH_REVIEW = "ark_review";
    public static final String CH_DECISION = "ark_decision";
    public static final String CH_SESSION = "ark_session_error";
    public static final String CH_ASSIGNED = "ark_assigned";
    public static final String CH_OTHER = "ark_other";

    /** Posted ids are remembered this long after they left the feed. */
    static final long KEEP_POSTED_MS = 7L * 24 * 3600 * 1000;
    static final int MAX_POSTED = 500;

    private NotifyFeed() {
    }

    public static String channelOf(String kind) {
        switch (kind) {
            case KIND_REVIEW:
                return CH_REVIEW;
            case KIND_DECISION:
            case KIND_APPROVAL:
                return CH_DECISION;
            case KIND_SESSION_ERROR:
                return CH_SESSION;
            case KIND_ASSIGNED:
                return CH_ASSIGNED;
            default:
                return CH_OTHER;
        }
    }

    /** One feed item. */
    public static final class Item {
        public final String id;
        public final String projectId;
        public final String projectName;
        public final String kind;
        public final String priority;
        public final String title;
        public final String body;
        public final int count;
        public final String createdAt;
        public final String linkType;
        public final String linkRef;
        public final String unitType;
        public final String route;
        /** assigned: who assigned (username and display name). */
        public final String by;
        public final String byName;

        Item(JSONObject o) {
            JSONObject p = o.optJSONObject("project");
            JSONObject link = o.optJSONObject("deep_link");
            id = o.optString("id");
            projectId = p == null ? "" : p.optString("id");
            projectName = p == null ? "" : p.optString("name");
            kind = o.optString("kind");
            priority = o.optString("priority", "default");
            title = o.optString("title");
            body = o.isNull("body") ? "" : o.optString("body");
            count = Math.max(1, o.optInt("count", 1));
            createdAt = o.isNull("created_at") ? "" : o.optString("created_at");
            linkType = link == null ? "" : link.optString("type");
            linkRef = link == null ? "" : link.optString("ref");
            unitType = link == null ? "" : link.optString("unit_type");
            route = link == null ? "" : link.optString("route");
            by = o.isNull("by") ? "" : o.optString("by");
            String name = o.isNull("by_name") ? "" : o.optString("by_name");
            byName = name.isEmpty() ? by : name;
        }
    }

    /** A parsed reply. */
    public static final class Reply {
        public final String cursor;
        public final String scope;
        public final List<Item> items = new ArrayList<>();
        public final Set<String> active = new HashSet<>();
        public final Map<String, String> projects = new LinkedHashMap<>();

        Reply(String cursor, String scope) {
            this.cursor = cursor;
            this.scope = scope;
        }
    }

    public static Reply parse(JSONObject json) {
        Reply reply = new Reply(json.optString("cursor", ""), json.optString("scope", "account"));
        JSONArray items = json.optJSONArray("items");
        for (int i = 0; items != null && i < items.length(); i++) {
            JSONObject o = items.optJSONObject(i);
            if (o != null && !o.optString("id").isEmpty()) reply.items.add(new Item(o));
        }
        JSONArray active = json.optJSONArray("active");
        for (int i = 0; active != null && i < active.length(); i++) reply.active.add(active.optString(i));
        JSONArray projects = json.optJSONArray("projects");
        for (int i = 0; projects != null && i < projects.length(); i++) {
            JSONObject p = projects.optJSONObject(i);
            if (p != null) reply.projects.put(p.optString("id"), p.optString("name"));
        }
        return reply;
    }

    // ------------------------------------------------------------ settings

    /** What the person chose in Notification settings. */
    public static final class Settings {
        public boolean reviews = true;
        public boolean decisions = true;
        public boolean sessionErrors = true;
        public boolean sessionsDone = false;
        public boolean assigned = true;
        public final Set<String> mutedProjects = new HashSet<>();
        public boolean quiet = false;
        /** Minutes after midnight. */
        public int quietStart = 22 * 60;
        public int quietEnd = 8 * 60;

        public boolean wants(String kind) {
            switch (kind) {
                case KIND_REVIEW:
                    return reviews;
                case KIND_DECISION:
                case KIND_APPROVAL:
                    return decisions;
                case KIND_SESSION_ERROR:
                    return sessionErrors;
                case KIND_SESSION_DONE:
                    return sessionsDone;
                case KIND_ASSIGNED:
                    return assigned;
                default:
                    return false;
            }
        }

        /** Muted per server and project: the key is {@code server|project}. */
        public static String muteKey(String server, String projectId) {
            return server + "|" + projectId;
        }

        public String toJson() {
            JSONObject o = new JSONObject();
            try {
                o.put("reviews", reviews).put("decisions", decisions).put("session_errors", sessionErrors)
                        .put("sessions_done", sessionsDone).put("assigned", assigned).put("quiet", quiet).put("quiet_start", quietStart)
                        .put("quiet_end", quietEnd).put("muted", new JSONArray(new ArrayList<>(mutedProjects)));
            } catch (JSONException impossible) {
                throw new IllegalStateException(impossible);
            }
            return o.toString();
        }

        public static Settings fromJson(String text) {
            Settings s = new Settings();
            if (text == null || text.isEmpty()) return s;
            try {
                JSONObject o = new JSONObject(text);
                s.reviews = o.optBoolean("reviews", true);
                s.decisions = o.optBoolean("decisions", true);
                s.sessionErrors = o.optBoolean("session_errors", true);
                s.sessionsDone = o.optBoolean("sessions_done", false);
                s.assigned = o.optBoolean("assigned", true);
                s.quiet = o.optBoolean("quiet", false);
                s.quietStart = clampMinute(o.optInt("quiet_start", s.quietStart));
                s.quietEnd = clampMinute(o.optInt("quiet_end", s.quietEnd));
                JSONArray muted = o.optJSONArray("muted");
                for (int i = 0; muted != null && i < muted.length(); i++) s.mutedProjects.add(muted.optString(i));
            } catch (JSONException broken) {
                return new Settings();
            }
            return s;
        }
    }

    static int clampMinute(int m) {
        return ((m % 1440) + 1440) % 1440;
    }

    /** Quiet hours from start to end (may wrap past midnight; start == end means none). */
    public static boolean inQuietHours(Settings s, int minuteOfDay) {
        if (!s.quiet || s.quietStart == s.quietEnd) return false;
        int m = clampMinute(minuteOfDay);
        if (s.quietStart < s.quietEnd) return m >= s.quietStart && m < s.quietEnd;
        return m >= s.quietStart || m < s.quietEnd;
    }

    // ---------------------------------------------------------------- state

    /** Per server: the cursor, what was posted (id -> when) and the projects seen. */
    public static final class State {
        public String cursor = "";
        public final Map<String, Long> posted = new LinkedHashMap<>();
        /** Posted items still shown, by id (for the per-project summaries). */
        public final Map<String, JSONObject> shown = new LinkedHashMap<>();
        public final Map<String, String> projects = new LinkedHashMap<>();

        public boolean initialized() {
            return !cursor.isEmpty();
        }

        public String toJson() {
            JSONObject o = new JSONObject();
            try {
                o.put("cursor", cursor).put("posted", new JSONObject(posted)).put("projects", new JSONObject(projects));
                JSONObject shownJson = new JSONObject();
                for (Map.Entry<String, JSONObject> e : shown.entrySet()) shownJson.put(e.getKey(), e.getValue());
                o.put("shown", shownJson);
            } catch (JSONException impossible) {
                throw new IllegalStateException(impossible);
            }
            return o.toString();
        }

        public static State fromJson(String text) {
            State s = new State();
            if (text == null || text.isEmpty()) return s;
            try {
                JSONObject o = new JSONObject(text);
                s.cursor = o.optString("cursor", "");
                JSONObject posted = o.optJSONObject("posted");
                if (posted != null) {
                    for (Iterator<String> k = posted.keys(); k.hasNext(); ) {
                        String id = k.next();
                        s.posted.put(id, posted.optLong(id));
                    }
                }
                JSONObject shown = o.optJSONObject("shown");
                if (shown != null) {
                    for (Iterator<String> k = shown.keys(); k.hasNext(); ) {
                        String id = k.next();
                        JSONObject item = shown.optJSONObject(id);
                        if (item != null) s.shown.put(id, item);
                    }
                }
                JSONObject projects = o.optJSONObject("projects");
                if (projects != null) {
                    for (Iterator<String> k = projects.keys(); k.hasNext(); ) {
                        String id = k.next();
                        s.projects.put(id, projects.optString(id));
                    }
                }
            } catch (JSONException broken) {
                return new State();
            }
            return s;
        }
    }

    /** A shown notification as the summaries need it. */
    static JSONObject shownOf(Item item) {
        JSONObject o = new JSONObject();
        try {
            o.put("project", item.projectId).put("name", item.projectName).put("kind", item.kind)
                    .put("title", item.title);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return o;
    }

    /** One project's summary: counts by kind and the lines of its items. */
    public static final class Group {
        public final String projectId;
        public String projectName = "";
        public final Map<String, Integer> counts = new LinkedHashMap<>();
        public final List<String> lines = new ArrayList<>();
        public final List<String> ids = new ArrayList<>();

        Group(String projectId) {
            this.projectId = projectId;
        }

        public int total() {
            int n = 0;
            for (int c : counts.values()) n += c;
            return n;
        }
    }

    /** What one poll does. */
    public static final class Plan {
        public final List<Item> post = new ArrayList<>();
        public final List<String> cancel = new ArrayList<>();
        /** Every project with shown notifications after this poll (summaries to refresh). */
        public final Map<String, Group> groups = new LinkedHashMap<>();
        /** Projects whose last notification went away (their summaries are removed). */
        public final Set<String> emptied = new HashSet<>();
        public boolean silent;
        public State next;
    }

    /**
     * Plans one poll of one server. The first poll after enabling only
     * records what is already there; later polls post items the server
     * reports as new since the cursor and not posted before, that the
     * settings want and whose project is not muted. Items no longer active
     * are withdrawn.
     */
    public static Plan plan(State previous, Reply reply, Settings settings, String server, long nowMs, int minuteOfDay) {
        State prev = previous == null ? new State() : previous;
        Plan plan = new Plan();
        State next = new State();
        next.cursor = reply.cursor.isEmpty() ? prev.cursor : reply.cursor;
        next.projects.putAll(reply.projects);
        boolean first = !prev.initialized();
        plan.silent = inQuietHours(settings, minuteOfDay);
        // Keep what was posted (and is either still active or recent).
        for (Map.Entry<String, Long> e : prev.posted.entrySet()) {
            boolean live = reply.active.contains(e.getKey());
            if (live || nowMs - e.getValue() < KEEP_POSTED_MS) next.posted.put(e.getKey(), e.getValue());
        }
        Set<String> affected = new HashSet<>();
        for (Map.Entry<String, JSONObject> e : prev.shown.entrySet()) {
            String project = e.getValue().optString("project");
            boolean muted = settings.mutedProjects.contains(Settings.muteKey(server, project));
            if (reply.active.contains(e.getKey()) && settings.wants(e.getValue().optString("kind")) && !muted) {
                next.shown.put(e.getKey(), e.getValue());
            } else {
                plan.cancel.add(e.getKey());
                affected.add(project);
            }
        }
        for (Item item : reply.items) {
            if (next.posted.containsKey(item.id)) continue;
            next.posted.put(item.id, nowMs);
            if (first || !reply.active.contains(item.id) || !settings.wants(item.kind)
                    || settings.mutedProjects.contains(Settings.muteKey(server, item.projectId))) {
                continue;
            }
            plan.post.add(item);
            next.shown.put(item.id, shownOf(item));
            affected.add(item.projectId);
        }
        trim(next.posted);
        for (Map.Entry<String, JSONObject> e : next.shown.entrySet()) {
            JSONObject s = e.getValue();
            String project = s.optString("project");
            Group g = plan.groups.get(project);
            if (g == null) {
                g = new Group(project);
                g.projectName = s.optString("name");
                plan.groups.put(project, g);
            }
            String kind = s.optString("kind");
            Integer n = g.counts.get(kind);
            g.counts.put(kind, n == null ? 1 : n + 1);
            g.lines.add(s.optString("title"));
            g.ids.add(e.getKey());
        }
        for (String project : affected) {
            if (!plan.groups.containsKey(project)) plan.emptied.add(project);
        }
        plan.next = next;
        return plan;
    }

    /** Drops the oldest posted ids beyond MAX_POSTED. */
    static void trim(Map<String, Long> posted) {
        if (posted.size() <= MAX_POSTED) return;
        List<Map.Entry<String, Long>> entries = new ArrayList<>(posted.entrySet());
        Collections.sort(entries, (x, y) -> Long.compare(x.getValue(), y.getValue()));
        for (int i = 0; i < entries.size() - MAX_POSTED; i++) posted.remove(entries.get(i).getKey());
    }
}
