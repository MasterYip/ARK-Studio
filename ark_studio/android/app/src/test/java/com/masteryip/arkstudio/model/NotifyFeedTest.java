package com.masteryip.arkstudio.model;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;

import java.util.ArrayList;
import java.util.List;

/** The feed of GET /api/notifications to notifications (NotifyFeed). */
public class NotifyFeedTest {
    private static final String SERVER = "ark.example:8443";
    private static final long NOW = 1_800_000_000_000L;

    private static JSONObject item(String id, String project, String kind, String title, int count) throws Exception {
        return new JSONObject().put("id", id).put("kind", kind).put("title", title).put("count", count)
                .put("priority", "review".equals(kind) ? "default" : "high")
                .put("project", new JSONObject().put("id", project).put("name", project.toUpperCase()))
                .put("deep_link", new JSONObject().put("type", kind.startsWith("session") || "approval".equals(kind)
                        ? "session" : "unit").put("ref", "T-1").put("unit_type", "task").put("project_id", project)
                        .put("route", "#/editor/unit%3AT-1"))
                .put("created_at", "2026-11-22T10:00:00.000000Z").put("body", JSONObject.NULL);
    }

    private static NotifyFeed.Reply reply(String cursor, List<JSONObject> items, String... active) throws Exception {
        JSONArray list = new JSONArray();
        for (JSONObject o : items) list.put(o);
        JSONArray act = new JSONArray();
        for (String a : active) act.put(a);
        return NotifyFeed.parse(new JSONObject().put("cursor", cursor).put("scope", "account").put("items", list)
                .put("active", act).put("projects", new JSONArray()
                        .put(new JSONObject().put("id", "p1").put("name", "PredActor_Rebuttal"))
                        .put(new JSONObject().put("id", "p2").put("name", "Other"))));
    }

    private static List<JSONObject> list(JSONObject... items) {
        List<JSONObject> out = new ArrayList<>();
        for (JSONObject o : items) out.add(o);
        return out;
    }

    @Test
    public void parsesItems() throws Exception {
        NotifyFeed.Reply r = reply("c1", list(item("review:p1:a", "p1", "review", "Part A", 3)), "review:p1:a");
        assertEquals("c1", r.cursor);
        assertEquals(1, r.items.size());
        NotifyFeed.Item it = r.items.get(0);
        assertEquals("p1", it.projectId);
        assertEquals(3, it.count);
        assertEquals("unit", it.linkType);
        assertEquals("task", it.unitType);
        assertEquals("", it.body);
        assertEquals("PredActor_Rebuttal", r.projects.get("p1"));
        assertEquals(NotifyFeed.CH_DECISION, NotifyFeed.channelOf("approval"));
        assertEquals(NotifyFeed.CH_SESSION, NotifyFeed.channelOf("session_error"));
        assertEquals(NotifyFeed.CH_SESSION_DONE, NotifyFeed.channelOf("session_done"));
        assertEquals(NotifyFeed.CH_OTHER, NotifyFeed.channelOf("something_new"));
    }

    @Test
    public void firstPollOnlyRecordsThenNewItemsPostOnce() throws Exception {
        NotifyFeed.Settings s = new NotifyFeed.Settings();
        JSONObject a = item("review:p1:a", "p1", "review", "Part A", 2);
        NotifyFeed.Plan first = NotifyFeed.plan(null, reply("c1", list(a), a.getString("id")), s, SERVER, NOW, 600);
        assertTrue(first.post.isEmpty());
        assertTrue(first.groups.isEmpty());
        assertEquals("c1", first.next.cursor);

        JSONObject b = item("decision:p1:d", "p1", "decision", "Pick one", 1);
        JSONObject c = item("session_error:s1:t", "p2", "session_error", "Fix tests", 1);
        // The overlap repeats a; it was recorded and does not post again.
        NotifyFeed.Plan second = NotifyFeed.plan(first.next, reply("c2", list(a, b, c),
                a.getString("id"), b.getString("id"), c.getString("id")), s, SERVER, NOW + 1000, 600);
        assertEquals(2, second.post.size());
        assertEquals("decision:p1:d", second.post.get(0).id);
        assertEquals(2, second.groups.size());
        assertEquals(1, (int) second.groups.get("p1").counts.get("decision"));
        assertEquals("P1", second.groups.get("p1").projectName);

        NotifyFeed.Plan third = NotifyFeed.plan(second.next, reply("c3", list(b, c),
                a.getString("id"), b.getString("id"), c.getString("id")), s, SERVER, NOW + 2000, 600);
        assertTrue("already posted", third.post.isEmpty());
        assertTrue(third.cancel.isEmpty());
        assertEquals(2, third.groups.size());
    }

    @Test
    public void groupsPerProjectAndWithdrawsWhatWasSeen() throws Exception {
        NotifyFeed.Settings s = new NotifyFeed.Settings();
        NotifyFeed.State start = new NotifyFeed.State();
        start.cursor = "c0";
        JSONObject r1 = item("review:p1:1", "p1", "review", "Card 1", 1);
        JSONObject r2 = item("review:p1:2", "p1", "review", "Card 2", 3);
        JSONObject r3 = item("review:p1:3", "p1", "review", "Card 3", 1);
        NotifyFeed.Plan plan = NotifyFeed.plan(start, reply("c1", list(r1, r2, r3), "review:p1:1", "review:p1:2",
                "review:p1:3"), s, SERVER, NOW, 600);
        assertEquals(3, plan.post.size());
        NotifyFeed.Group g = plan.groups.get("p1");
        // "PredActor_Rebuttal: 3 new for review": one per card.
        assertEquals(3, (int) g.counts.get("review"));
        assertEquals(3, g.total());
        assertEquals(3, g.lines.size());

        // Two cards were opened in the browser: they leave "active" and are withdrawn.
        NotifyFeed.Plan later = NotifyFeed.plan(plan.next, reply("c2", list(), "review:p1:3"), s, SERVER, NOW + 1000, 600);
        assertEquals(2, later.cancel.size());
        assertTrue(later.cancel.contains("review:p1:1"));
        assertEquals(1, later.groups.get("p1").total());
        assertTrue(later.emptied.isEmpty());

        NotifyFeed.Plan gone = NotifyFeed.plan(later.next, reply("c3", list()), s, SERVER, NOW + 2000, 600);
        assertEquals(1, gone.cancel.size());
        assertTrue(gone.groups.isEmpty());
        assertTrue(gone.emptied.contains("p1"));
    }

    @Test
    public void settingsFilterKindsAndMutedProjects() throws Exception {
        NotifyFeed.Settings s = new NotifyFeed.Settings();
        assertTrue("finished sessions are on by default", s.sessionsDone);
        s.sessionsDone = false;
        s.reviews = false;
        s.mutedProjects.add(NotifyFeed.Settings.muteKey(SERVER, "p2"));
        NotifyFeed.State start = new NotifyFeed.State();
        start.cursor = "c0";
        JSONObject review = item("review:p1:1", "p1", "review", "Card", 1);
        JSONObject approval = item("approval:s1:a", "p1", "approval", "Run", 1);
        JSONObject muted = item("decision:p2:d", "p2", "decision", "Muted", 1);
        JSONObject done = item("session_done:s2:t", "p1", "session_done", "Docs", 1);
        NotifyFeed.Plan plan = NotifyFeed.plan(start, reply("c1", list(review, approval, muted, done),
                "review:p1:1", "approval:s1:a", "decision:p2:d", "session_done:s2:t"), s, SERVER, NOW, 600);
        assertEquals(1, plan.post.size());
        assertEquals("approval:s1:a", plan.post.get(0).id);
        // Settings survive a round trip.
        NotifyFeed.Settings back = NotifyFeed.Settings.fromJson(s.toJson());
        assertFalse(back.reviews);
        assertFalse("a saved off stays off", back.sessionsDone);
        // Settings saved before version 2 had finished sessions off by default: they turn on once.
        assertTrue(NotifyFeed.Settings.fromJson("{\"sessions_done\":false,\"reviews\":true}").sessionsDone);
        assertTrue(back.mutedProjects.contains(NotifyFeed.Settings.muteKey(SERVER, "p2")));
        // Muting later withdraws what was shown.
        s.mutedProjects.add(NotifyFeed.Settings.muteKey(SERVER, "p1"));
        NotifyFeed.Plan after = NotifyFeed.plan(plan.next, reply("c2", list(), "approval:s1:a"), s, SERVER, NOW + 1, 600);
        assertEquals(1, after.cancel.size());
        assertTrue(after.emptied.contains("p1"));
    }

    @Test
    public void assignedItems() throws Exception {
        JSONObject one = item("assigned:p1:a_1:a:T-1@x", "p1", "assigned", "Write the intro", 1)
                .put("by", "ana").put("by_name", "Ana");
        JSONObject many = item("assigned:p1:b_2:a:T-2@x,a:T-3@x", "p1", "assigned", "Two", 2).put("by", "bo")
                .put("by_name", JSONObject.NULL);
        NotifyFeed.Reply r = reply("c1", list(one, many), "assigned:p1:a_1:a:T-1@x", "assigned:p1:b_2:a:T-2@x,a:T-3@x");
        assertEquals("Ana", r.items.get(0).byName);
        assertEquals("bo", r.items.get(1).byName);
        assertEquals(NotifyFeed.CH_ASSIGNED, NotifyFeed.channelOf("assigned"));
        NotifyFeed.Settings s = new NotifyFeed.Settings();
        assertTrue(s.wants("assigned"));
        NotifyFeed.State start = new NotifyFeed.State();
        start.cursor = "c0";
        NotifyFeed.Plan plan = NotifyFeed.plan(start, r, s, SERVER, NOW, 600);
        assertEquals(2, plan.post.size());
        s.assigned = false;
        assertFalse(NotifyFeed.Settings.fromJson(s.toJson()).assigned);
        assertTrue(NotifyFeed.Settings.fromJson("{}").assigned);
    }

    @Test
    public void quietHoursPostSilently() throws Exception {
        NotifyFeed.Settings s = new NotifyFeed.Settings();
        assertFalse(NotifyFeed.inQuietHours(s, 23 * 60));
        s.quiet = true; // 22:00 - 08:00
        assertTrue(NotifyFeed.inQuietHours(s, 23 * 60));
        assertTrue(NotifyFeed.inQuietHours(s, 7 * 60 + 59));
        assertFalse(NotifyFeed.inQuietHours(s, 8 * 60));
        assertFalse(NotifyFeed.inQuietHours(s, 12 * 60));
        assertTrue(NotifyFeed.inQuietHours(s, 22 * 60));
        s.quietStart = 13 * 60;
        s.quietEnd = 14 * 60;
        assertTrue(NotifyFeed.inQuietHours(s, 13 * 60 + 30));
        assertFalse(NotifyFeed.inQuietHours(s, 14 * 60));
        s.quietEnd = s.quietStart;
        assertFalse(NotifyFeed.inQuietHours(s, 13 * 60));

        s.quietStart = 22 * 60;
        s.quietEnd = 8 * 60;
        NotifyFeed.State start = new NotifyFeed.State();
        start.cursor = "c0";
        JSONObject a = item("approval:s1:a", "p1", "approval", "Run", 1);
        NotifyFeed.Plan night = NotifyFeed.plan(start, reply("c1", list(a), "approval:s1:a"), s, SERVER, NOW, 23 * 60);
        assertEquals(1, night.post.size());
        assertTrue(night.silent);
    }

    @Test
    public void stateRoundTripsAndForgetsOldIds() throws Exception {
        NotifyFeed.State start = new NotifyFeed.State();
        start.cursor = "c0";
        start.posted.put("old", NOW - NotifyFeed.KEEP_POSTED_MS - 1);
        start.posted.put("live", NOW - NotifyFeed.KEEP_POSTED_MS - 1);
        NotifyFeed.Plan plan = NotifyFeed.plan(start, reply("c1", list(), "live"), new NotifyFeed.Settings(), SERVER, NOW, 0);
        assertFalse(plan.next.posted.containsKey("old"));
        assertTrue("still active", plan.next.posted.containsKey("live"));
        NotifyFeed.State back = NotifyFeed.State.fromJson(plan.next.toJson());
        assertEquals("c1", back.cursor);
        assertTrue(back.posted.containsKey("live"));
        assertEquals("Other", back.projects.get("p2"));
        assertEquals(0, NotifyFeed.State.fromJson("{broken").posted.size());
    }
}
