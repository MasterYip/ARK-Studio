package com.masteryip.arkstudio.model;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;

/** Ports of tests/js/gpu_model.test.mjs and tests/js/session_model.test.mjs plus native-only rules. */
public class ModelTest {
    private static JSONObject gpu() throws Exception {
        return new JSONObject("{\"index\":0,\"memory_total_mb\":24000,\"memory_used_mb\":13000,\"utilization_pct\":100}");
    }

    private static JSONObject job(String id) throws Exception {
        return new JSONObject().put("id", id).put("status", "running").put("gpu_indices", new JSONArray("[0]"));
    }

    private static JSONObject proc(int gpu, String job, Integer mem, Integer sm) throws Exception {
        return new JSONObject().put("gpu_index", gpu).put("job_id", job)
                .put("memory_mb", mem == null ? JSONObject.NULL : mem)
                .put("sm_pct", sm == null ? JSONObject.NULL : sm);
    }

    // ------------------------------------------------------------- GPU ownership

    @Test
    public void measuredComputeShareComesFromPmon() throws Exception {
        GpuOwnership own = GpuOwnership.compute(gpu(), new JSONArray().put(proc(0, "j1", 12000, 97)),
                new JSONArray().put(job("j1")), true);
        assertEquals(97, own.utilPct, 1e-9);
        assertFalse(own.utilEstimated);
        assertEquals(50, own.memPct, 1e-9);
    }

    @Test
    public void withoutPmonUtilizationIsSplitByUsedMemory() throws Exception {
        GpuOwnership own = GpuOwnership.compute(gpu(), new JSONArray().put(proc(0, "j1", 12000, null)),
                new JSONArray().put(job("j1")), false);
        assertEquals(92, Math.round(own.utilPct));
        assertTrue(own.utilEstimated);
    }

    @Test
    public void runningJobCountsRegardlessOfTask() throws Exception {
        GpuOwnership own = GpuOwnership.compute(gpu(), new JSONArray().put(proc(0, "j1", 6000, 40)),
                new JSONArray().put(job("j1").put("task", "T-0099")), true);
        assertEquals(6000, own.memMb, 1e-9);
        assertEquals(Arrays.asList("j1"), own.jobIds);
    }

    @Test
    public void unknownMemoryReportedForeignIgnored() throws Exception {
        JSONArray procs = new JSONArray()
                .put(proc(0, "j1", null, 10))
                .put(proc(0, "j1", 2000, 20))
                .put(proc(1, "j1", 9000, 90))
                .put(proc(0, "", 5000, 50))
                .put(proc(0, "other", 1000, 5));
        JSONArray jobs = new JSONArray().put(job("j1")).put(job("done").put("status", "succeeded"));
        GpuOwnership own = GpuOwnership.compute(gpu(), procs, jobs, true);
        assertEquals(2000, own.memMb, 1e-9);
        assertEquals(1, own.unknownMemory);
        assertEquals(30, own.utilPct, 1e-9);
    }

    @Test
    public void totalBarNeverBelowOwnedAndCaps() throws Exception {
        JSONObject quiet = gpu().put("utilization_pct", 20);
        GpuOwnership own = GpuOwnership.compute(quiet,
                new JSONArray().put(proc(0, "j1", 1000, 70)).put(proc(0, "j1", 1000, 60)),
                new JSONArray().put(job("j1")), true);
        assertEquals(100, own.utilPct, 1e-9);
        assertEquals(100, own.totalUtilPct, 1e-9);
    }

    @Test
    public void noOwnedProcessesNothingHighlighted() throws Exception {
        GpuOwnership own = GpuOwnership.compute(gpu(), new JSONArray(), new JSONArray().put(job("j1")), true);
        assertEquals(0, own.utilPct, 1e-9);
        assertEquals(0, own.memPct, 1e-9);
        assertFalse(own.utilEstimated);
        assertEquals("ok", GpuOwnership.band(59));
        assertEquals("warn", GpuOwnership.band(60));
        assertEquals("hot", GpuOwnership.band(85));
    }

    // ------------------------------------------------------------- transcript

    @Test
    public void threadBlocksGroupToolsAndSeparateUserTurns() throws Exception {
        JSONArray items = new JSONArray("["
                + "{\"id\":\"m-1\",\"type\":\"message\",\"role\":\"user\",\"text\":\"fix it\",\"at\":\"2026-11-03T08:00:00Z\"},"
                + "{\"id\":\"c1\",\"type\":\"tool\",\"name\":\"shell\",\"input\":{\"command\":\"git status\"},\"status\":\"ok\",\"output\":\"M a\"},"
                + "{\"id\":\"c2\",\"type\":\"tool\",\"name\":\"grep\",\"input\":{\"pattern\":\"x\"},\"status\":\"ok\",\"output\":\"\"},"
                + "{\"id\":\"m-2\",\"type\":\"message\",\"role\":\"assistant\",\"text\":\"done\",\"at\":\"2026-11-03T08:00:05Z\"},"
                + "{\"id\":\"e1\",\"type\":\"tool\",\"name\":\"apply_patch\",\"input\":{\"patch\":\"*** Update File: a.js\\n*** Add File: b.css\\n\"},\"status\":\"ok\"},"
                + "{\"id\":\"e2\",\"type\":\"tool\",\"name\":\"write_file\",\"input\":{\"path\":\"a.js\"},\"status\":\"running\"},"
                + "{\"id\":\"m-3\",\"type\":\"message\",\"role\":\"user\",\"text\":\"again\",\"at\":\"2026-11-03T09:00:00Z\"},"
                + "{\"id\":\"x-1\",\"type\":\"error\",\"text\":\"stream disconnected\"},"
                + "{\"id\":\"n-1\",\"type\":\"notice\",\"level\":\"reasoning\",\"text\":\"hmm\",\"streaming\":true}]");
        List<Transcript.Block> blocks = Transcript.threadBlocks(items);
        List<String> kinds = new ArrayList<>();
        for (Transcript.Block block : blocks) kinds.add(block.kind);
        assertEquals(Arrays.asList("separator", "user", "tools", "assistant", "tools", "separator", "user",
                "error", "reasoning"), kinds);
        assertEquals("tools-c1", blocks.get(2).key);
        assertEquals("c1", blocks.get(2).items.get(0).getString("id"));
        assertEquals("c2", blocks.get(2).items.get(1).getString("id"));
        assertEquals("git status", Transcript.toolCommand(items.getJSONObject(1)));
        assertEquals("command", Transcript.toolKind(new JSONObject("{\"name\":\"tool\",\"input\":{\"command\":\"ls\"}}")));
        assertEquals("read", Transcript.toolKind(new JSONObject("{\"name\":\"Read\"}")));
        assertEquals("edit", Transcript.toolKind(items.getJSONObject(4)));
        assertEquals(Arrays.asList("a.js", "b.css"), Transcript.toolPaths(items.getJSONObject(4)));
        assertEquals("2 edits · 1 running", Transcript.toolSummary(blocks.get(4).items));
        assertEquals("1 command · 1 search", Transcript.toolSummary(blocks.get(2).items));
        assertEquals("$ git status", Transcript.toolTitle(items.getJSONObject(1)));
    }

    @Test
    public void sessionOrderingAndComposerMode() throws Exception {
        JSONArray sessions = new JSONArray("["
                + "{\"id\":\"a\",\"status\":\"stopped\",\"last_activity_at\":\"2026-01-02\"},"
                + "{\"id\":\"b\",\"status\":\"running\",\"last_activity_at\":\"2026-01-01\"},"
                + "{\"id\":\"c\",\"status\":\"stopped\",\"last_activity_at\":\"2026-01-03\"},"
                + "{\"id\":\"d\",\"status\":\"idle\"}]");
        List<JSONObject> sorted = Transcript.sortSessions(sessions);
        assertEquals("b", sorted.get(0).getString("id"));
        assertEquals("d", sorted.get(1).getString("id"));
        assertEquals("c", sorted.get(2).getString("id"));
        assertEquals("steer", Transcript.composerMode(sorted.get(1)));
        assertEquals("continue", Transcript.composerMode(sorted.get(2)));
        assertEquals("none", Transcript.composerMode(null));
    }

    // ------------------------------------------------------------- board + routes

    @Test
    public void boardColumnsOrderAndImportance() throws Exception {
        JSONArray units = new JSONArray("["
                + "{\"uid\":\"t1\",\"type\":\"task\",\"status\":\"open\",\"importance\":\"p2\",\"title\":\"b\"},"
                + "{\"uid\":\"t2\",\"type\":\"task\",\"status\":\"open\",\"importance\":\"p0\",\"title\":\"z\"},"
                + "{\"uid\":\"t3\",\"type\":\"task\",\"status\":\"weird\",\"title\":\"w\"},"
                + "{\"uid\":\"t4\",\"type\":\"task\",\"status\":\"archived\",\"title\":\"gone\"},"
                + "{\"uid\":\"i1\",\"type\":\"idea\",\"status\":\"open\",\"title\":\"not a task\"}]");
        Map<String, List<JSONObject>> columns = Board.columns(units);
        assertEquals(Board.COLUMNS, new ArrayList<>(columns.keySet()));
        assertEquals("t2", columns.get("open").get(0).getString("uid"));
        assertEquals("t1", columns.get("open").get(1).getString("uid"));
        assertEquals("t3", columns.get("inbox").get(0).getString("uid"));
        int total = 0;
        for (List<JSONObject> column : columns.values()) total += column.size();
        assertEquals(3, total);
        JSONObject note = Board.noteFor(new JSONObject("{\"uid\":\"t1\"}"), "  Looks good\nship it ");
        assertEquals("note", note.getString("type"));
        assertEquals("Looks good", note.getString("title"));
        assertEquals("t1", note.getJSONArray("links").getJSONObject(0).getString("target"));
    }

    @Test
    public void webRoutes() {
        assertEquals("#/editor/unit%3AU-0001", WebRoutes.editorUnit("U-0001"));
        assertEquals("http://h:1/ark/#/map", WebRoutes.url("http://h:1/ark/#pair=x", WebRoutes.MAP));
        assertEquals("http://h:1/#/home", WebRoutes.url("http://h:1", null));
        assertEquals("http://h:1/#/settings", WebRoutes.url("http://h:1/", "/settings"));
        assertEquals("a%20b!(c)", WebRoutes.encodeComponent("a b!(c)"));
    }

    // ------------------------------------------------------------- markdown

    @Test
    public void markdownBlocks() {
        List<Markdown.Block> blocks = Markdown.parse("# Title\n\nSome **bold** and `code`.\nnext line\n\n"
                + "- one\n  - nested\n1. first\n- [x] done\n\n> quoted\n\n```python\nprint(1)\n```\n---\n"
                + "| a | b |\n|---|---|\n| 1 | 2 |\n");
        List<Markdown.Kind> kinds = new ArrayList<>();
        for (Markdown.Block block : blocks) kinds.add(block.kind);
        assertEquals(Arrays.asList(Markdown.Kind.HEADING, Markdown.Kind.PARAGRAPH, Markdown.Kind.LIST_ITEM,
                Markdown.Kind.LIST_ITEM, Markdown.Kind.LIST_ITEM, Markdown.Kind.LIST_ITEM, Markdown.Kind.QUOTE,
                Markdown.Kind.CODE, Markdown.Kind.RULE, Markdown.Kind.TABLE), kinds);
        assertEquals(1, blocks.get(0).level);
        assertEquals("Some bold and code. next line", blocks.get(1).inline.text);
        assertEquals(1, blocks.get(3).level);
        assertEquals("1.", blocks.get(4).marker);
        assertEquals("☑", blocks.get(5).marker);
        assertEquals("python", blocks.get(7).lang);
        assertEquals("print(1)", blocks.get(7).raw);
        assertEquals("a  │  b\n1  │  2", blocks.get(9).raw);
    }

    @Test
    public void markdownInlineSpans() {
        Markdown.Inline inline = Markdown.inline("**bold** *it* `x*y` [ARK](http://a/b) snake_case_name ~~old~~ https://e.org/x.");
        assertEquals("bold it x*y ARK snake_case_name old https://e.org/x.", inline.text);
        int bold = 0, italic = 0, code = 0, link = 0, strike = 0;
        for (Markdown.Span span : inline.spans) {
            switch (span.type) {
                case Markdown.Span.BOLD: bold++; break;
                case Markdown.Span.ITALIC: italic++; break;
                case Markdown.Span.CODE: code++; break;
                case Markdown.Span.LINK: link++; break;
                case Markdown.Span.STRIKE: strike++; break;
                default: break;
            }
        }
        assertEquals(1, bold);
        assertEquals(1, italic);
        assertEquals(1, code);
        assertEquals(2, link);
        assertEquals(1, strike);
        Markdown.Span ark = null;
        for (Markdown.Span span : inline.spans) if (span.type == Markdown.Span.LINK && ark == null) ark = span;
        assertEquals("http://a/b", ark.href);
        assertEquals("ARK", inline.text.substring(ark.start, ark.end));
    }

    @Test
    public void webRenderFallbackDetection() {
        assertTrue(Markdown.needsWebRender("```mermaid\ngraph TD; A-->B\n```"));
        assertTrue(Markdown.needsWebRender("Energy $$E=mc^2$$"));
        assertTrue(Markdown.needsWebRender("<div align=center>x</div>"));
        assertFalse(Markdown.needsWebRender("costs $5 and $6"));
        assertFalse(Markdown.needsWebRender("```html\n<div>code sample</div>\n```"));
        assertFalse(Markdown.needsWebRender("plain **text**"));
    }

    // ------------------------------------------------------------- notifications

    @Test
    public void watchReportsTransitionsOnly() throws Exception {
        List<Watch.Event> events = new ArrayList<>();
        JSONArray runs = new JSONArray("[{\"id\":\"s1\",\"status\":\"running\",\"title\":\"Train\"},"
                + "{\"id\":\"s2\",\"status\":\"running\",\"title\":\"Eval\"}]");
        JSONArray review = new JSONArray("[{\"uid\":\"T1\",\"status\":\"review\",\"title\":\"Check\"}]");
        JSONArray gpu = new JSONArray("[{\"id\":\"g1\",\"status\":\"running\",\"owner\":\"ark\",\"job\":\"fit\"},"
                + "{\"id\":\"g2\",\"status\":\"running\",\"owner\":\"bob\"}]");
        Watch.Snapshot first = Watch.diff(Watch.Snapshot.fromJson(""), runs, review, gpu, "ark", events);
        assertTrue("first poll is a silent baseline", events.isEmpty());
        Watch.Snapshot restored = Watch.Snapshot.fromJson(first.toJson());

        runs = new JSONArray("[{\"id\":\"s1\",\"status\":\"stopped\",\"title\":\"Train\"},"
                + "{\"id\":\"s2\",\"status\":\"errored\",\"title\":\"Eval\",\"error\":\"boom\"},"
                + "{\"id\":\"s3\",\"status\":\"stopped\",\"title\":\"Old\"}]");
        review = new JSONArray("[{\"uid\":\"T1\",\"status\":\"review\"},{\"uid\":\"T2\",\"status\":\"review\",\"title\":\"New\"}]");
        gpu = new JSONArray("[{\"id\":\"g1\",\"status\":\"completed\",\"owner\":\"ark\",\"job\":\"fit\"},"
                + "{\"id\":\"g2\",\"status\":\"completed\",\"owner\":\"bob\"}]");
        Watch.Snapshot second = Watch.diff(restored, runs, review, gpu, "ark", events);
        List<String> types = new ArrayList<>();
        for (Watch.Event event : events) types.add(event.type + ":" + event.id);
        assertEquals(Arrays.asList("run_finished:s1", "run_failed:s2", "task_review:T2", "gpu_finished:g1"), types);

        events.clear();
        Watch.diff(second, null, review, null, "ark", events);
        assertTrue("unavailable endpoints keep state and stay quiet", events.isEmpty());
    }
}
