package com.masteryip.arkstudio.model;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;

/** Review notes and Needs you cards (ports of tests/js/review_requests.test.mjs cases). */
public class ReviewModelTest {
    // ----------------------------------------------------------- notes

    @Test
    public void anchorsParseLikeTheWeb() {
        ReviewNote.Anchor heading = ReviewNote.parse("#results");
        assertEquals(ReviewNote.HEADING, heading.kind);
        assertEquals("", heading.unit);
        assertEquals("results", heading.slug);
        ReviewNote.Anchor other = ReviewNote.parse("T-0042#setup");
        assertEquals("T-0042", other.unit);
        assertEquals("setup", other.slug);
        assertEquals(ReviewNote.UNIT, ReviewNote.parse("T-0042").kind);
        ReviewNote.Anchor media = ReviewNote.parse("media:421@0:08-0:12");
        assertEquals(421, media.media);
        assertEquals(8, media.start, 1e-9);
        assertEquals(12, media.end, 1e-9);
        assertNull(ReviewNote.parse("media:4@0:12-0:08"));
        assertNull(ReviewNote.parse("https://example.org"));
        assertEquals(68, ReviewNote.parseClock("1:08"), 1e-9);
        assertEquals("1:02:03", ReviewNote.clockLabel(3723));
    }

    @Test
    public void noteSplitsIntoTextAndAnchorChips() {
        List<ReviewNote.Part> parts = ReviewNote.parts("See **[the baseline](#baseline)** and media:12@0:08, not [docs](https://x.org)");
        List<String> shape = new ArrayList<>();
        for (ReviewNote.Part p : parts) shape.add(p.anchor != null ? "<" + p.anchor.raw + "|" + p.anchor.label + ">" : p.text);
        assertEquals(Arrays.asList("See ", "<#baseline|the baseline>", " and ", "<media:12@0:08|>", ", not ", "docs"), shape);
        assertEquals("§ baseline", ReviewNote.label(ReviewNote.parse("#baseline"), "X-1"));
        assertEquals("T-2 › plot", ReviewNote.label(ReviewNote.parse("T-2#plot"), "X-1"));
        assertEquals("media:12 @0:08–0:12", ReviewNote.label(ReviewNote.parse("media:12@0:08-0:12"), ""));
        assertEquals("X-1", ReviewNote.parse("#a").unitOr("X-1"));
    }

    @Test
    public void headlineNumbers() {
        assertEquals("4.7 ms", ReviewNote.keyNumber("Latency 4.7 ms per step"));
        assertEquals("~41%", ReviewNote.keyNumber("Throughput ~41% idle"));
        assertEquals("0.2 MB", ReviewNote.keyNumber("Memory 0.2 MB per world"));
        assertEquals("", ReviewNote.keyNumber("No numbers here"));
    }

    @Test
    public void headingIdsMatchTheServer() {
        List<Markdown.Block> blocks = Markdown.parse("# Results\nx\n\n## Results\n\n## Base line!\n\nKept {#keep}\n\n# 结果");
        Map<Integer, String> ids = ReviewNote.blockIds(blocks);
        List<String> list = new ArrayList<>();
        for (int i = 0; i < blocks.size(); i++) if (ids.containsKey(i)) list.add(ids.get(i));
        assertEquals(Arrays.asList("results", "results-1", "base-line", "keep", "结果"), list);
    }

    // ----------------------------------------------------------- cards

    private static JSONObject item(String uid, String kind, JSONObject group) throws Exception {
        JSONObject o = new JSONObject().put("uid", uid).put("kind", kind).put("type", "evidence").put("title", uid + " 1.5 ms");
        if (group != null) o.put("auto_group", group);
        else if (kind.equals("review")) o.put("auto_group", JSONObject.NULL);
        return o;
    }

    private static JSONObject card() throws Exception {
        return new JSONObject().put("key", "group:E-1,E-2,E-3").put("primary", "E-2")
                .put("members", new JSONArray("[\"E-2\",\"E-1\",\"E-3\"]"))
                .put("parent", new JSONObject().put("uid", "T-0020").put("title", "HOI-215 · Large"))
                .put("delivery", "branch:agent/x").put("title", "HOI-215 B1").put("span", 3)
                .put("reasons", new JSONArray()
                        .put(new JSONObject().put("a", "E-1").put("b", "E-2").put("link", JSONObject.NULL)
                                .put("parents", new JSONArray("[\"T-0020\"]")).put("delivery", "branch:agent/x").put("minutes", 1))
                        .put(new JSONObject().put("a", "E-1").put("b", "E-3")
                                .put("link", new JSONObject().put("from", "E-1").put("to", "E-3").put("relation", "supports"))
                                .put("parents", new JSONArray("[\"T-0020\"]")).put("delivery", "branch:agent/x").put("minutes", 3)));
    }

    @Test
    public void serverCardsGroupTheDelivery() throws Exception {
        JSONObject c = card();
        JSONArray needs = new JSONArray()
                .put(new JSONObject().put("uid", "RG-1").put("kind", "review_group").put("type", "review_group").put("title", "Figures")
                        .put("members", new JSONArray().put(new JSONObject().put("uid", "F-1").put("outcome", JSONObject.NULL))
                                .put(new JSONObject().put("uid", "F-2").put("outcome", "accepted"))))
                .put(item("D-1", "decision", null))
                .put(item("E-1", "review", c))
                .put(item("X-9", "review", null))
                .put(item("E-2", "review", c))
                .put(item("E-3", "review", c));
        List<NeedsCards.Card> cards = NeedsCards.cards(needs);
        List<String> shape = new ArrayList<>();
        for (NeedsCards.Card k : cards) shape.add(k.type + ":" + k.key);
        assertEquals(Arrays.asList("2:RG-1", "0:D-1", "1:group:E-1,E-2,E-3", "0:X-9"), shape);
        NeedsCards.Card delivery = cards.get(2);
        assertEquals("HOI-215 B1", delivery.title);
        assertEquals("E-2", delivery.uid());
        List<String> members = new ArrayList<>();
        for (JSONObject m : delivery.members) members.add(m.optString("uid"));
        assertEquals(Arrays.asList("E-2", "E-1", "E-3"), members);
        NeedsCards.Explain why = NeedsCards.explain(delivery);
        assertEquals(Arrays.asList("T-0020"), why.parents);
        assertTrue(why.linked);
        assertEquals("agent/x", why.deliveryName());
        assertEquals(1, NeedsCards.pendingMembers(cards.get(0)).size());
        assertEquals("review_group", NeedsCards.kind(cards.get(0).item));
    }

    @Test
    public void aCardWithOneMemberLeftShowsTheItemAlone() throws Exception {
        JSONObject c = card();
        List<NeedsCards.Card> cards = NeedsCards.cards(new JSONArray().put(item("E-1", "review", c)).put(item("X-9", "review", null)));
        assertEquals(2, cards.size());
        assertEquals(NeedsCards.ITEM, cards.get(0).type);
        assertEquals(NeedsCards.ITEM, cards.get(1).type);
        List<NeedsCards.Card> two = NeedsCards.cards(new JSONArray().put(item("E-3", "review", c)).put(item("E-1", "review", c)));
        assertEquals(1, two.size());
        assertEquals("E-1", two.get(0).members.get(0).optString("uid"));
        assertEquals(1, two.get(0).reasons.size());
    }

    @Test
    public void changeSummary() throws Exception {
        assertNull(NeedsCards.changes(null));
        int[] c = NeedsCards.changes(new JSONObject().put("added", 3).put("removed", 1).put("media", 0));
        assertEquals(3, c[0]);
        assertFalse(NeedsCards.empty(c));
        assertTrue(NeedsCards.empty(new int[]{0, 0, 0}));
        assertEquals("", NeedsCards.str(new JSONObject().put("x", JSONObject.NULL), "x"));
    }
}
