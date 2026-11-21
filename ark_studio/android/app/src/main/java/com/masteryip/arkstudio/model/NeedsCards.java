package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;

/**
 * Home's "Needs you" as cards, the same ones the web shows
 * ({@code web/js/needs-model.js groupNeeds}): a unit or decision on its own,
 * the units of one delivery in one card (the server groups them and sends the
 * card as {@code auto_group} on every member, store/needsgroups.go), and the
 * review groups agents submit ({@code kind: "review_group"}).
 */
public final class NeedsCards {
    private NeedsCards() {
    }

    public static final int ITEM = 0;
    public static final int DELIVERY = 1;
    public static final int REVIEW_GROUP = 2;

    public static final class Card {
        public final int type;
        /** The item (ITEM, REVIEW_GROUP) or the primary member (DELIVERY). */
        public final JSONObject item;
        public final String key;
        public final String title;
        public final String delivery;
        public final JSONObject parent;
        public final List<JSONObject> members;
        public final List<JSONObject> reasons;
        public final int span;

        Card(int type, JSONObject item, String key, String title, String delivery, JSONObject parent,
             List<JSONObject> members, List<JSONObject> reasons, int span) {
            this.type = type;
            this.item = item;
            this.key = key;
            this.title = title;
            this.delivery = delivery;
            this.parent = parent;
            this.members = members;
            this.reasons = reasons;
            this.span = span;
        }

        public String uid() {
            return item.optString("uid");
        }
    }

    /** "review" | "decision" | "review_group". */
    public static String kind(JSONObject item) {
        String kind = item.optString("kind", "");
        if (kind.equals("decision") || kind.equals("review") || kind.equals("review_group")) return kind;
        return "decision".equals(item.optString("type")) ? "decision" : "review";
    }

    /** The cards of {@code needs_you}, in list order. */
    public static List<Card> cards(JSONArray needs) {
        List<JSONObject> list = new ArrayList<>();
        for (int i = 0; needs != null && i < needs.length(); i++) {
            JSONObject item = needs.optJSONObject(i);
            if (item != null) list.add(item);
        }
        // key -> members present, in list order
        Map<String, LinkedHashMap<String, JSONObject>> byKey = new LinkedHashMap<>();
        Map<String, JSONObject> cardOf = new LinkedHashMap<>();
        Map<String, Integer> firstAt = new LinkedHashMap<>();
        for (int i = 0; i < list.size(); i++) {
            JSONObject item = list.get(i);
            JSONObject card = item.optJSONObject("auto_group");
            String uid = item.optString("uid", "");
            if (card == null || uid.isEmpty() || "review_group".equals(kind(item))) continue;
            String key = card.optString("key", "");
            if (key.isEmpty()) continue;
            if (!byKey.containsKey(key)) {
                byKey.put(key, new LinkedHashMap<>());
                cardOf.put(key, card);
                firstAt.put(key, i);
            }
            byKey.get(key).put(uid, item);
        }
        Map<Integer, Card> groupAt = new LinkedHashMap<>();
        Set<JSONObject> grouped = new LinkedHashSet<>();
        for (Map.Entry<String, LinkedHashMap<String, JSONObject>> entry : byKey.entrySet()) {
            LinkedHashMap<String, JSONObject> present = entry.getValue();
            if (present.size() < 2) continue;
            JSONObject card = cardOf.get(entry.getKey());
            List<String> order = new ArrayList<>();
            JSONArray members = card.optJSONArray("members");
            for (int i = 0; members != null && i < members.length(); i++) {
                String uid = members.optString(i);
                if (present.containsKey(uid) && !order.contains(uid)) order.add(uid);
            }
            for (String uid : present.keySet()) if (!order.contains(uid)) order.add(uid);
            List<JSONObject> items = new ArrayList<>();
            for (String uid : order) items.add(present.get(uid));
            List<JSONObject> reasons = new ArrayList<>();
            JSONArray rs = card.optJSONArray("reasons");
            for (int i = 0; rs != null && i < rs.length(); i++) {
                JSONObject r = rs.optJSONObject(i);
                if (r != null && present.containsKey(r.optString("a")) && present.containsKey(r.optString("b"))) reasons.add(r);
            }
            JSONObject primary = items.get(0);
            String title = card.optString("title", "");
            if (title.isEmpty()) title = primary.optString("title", primary.optString("uid"));
            groupAt.put(firstAt.get(entry.getKey()), new Card(DELIVERY, primary, entry.getKey(), title,
                    card.optString("delivery", ""), card.optJSONObject("parent"), items, reasons, card.optInt("span")));
            grouped.addAll(items);
        }
        List<Card> out = new ArrayList<>();
        for (int i = 0; i < list.size(); i++) {
            JSONObject item = list.get(i);
            if (groupAt.containsKey(i)) {
                out.add(groupAt.get(i));
            } else if (!grouped.contains(item)) {
                boolean group = "review_group".equals(kind(item));
                List<JSONObject> members = new ArrayList<>();
                if (group) {
                    JSONArray ms = item.optJSONArray("members");
                    for (int k = 0; ms != null && k < ms.length(); k++) {
                        JSONObject m = ms.optJSONObject(k);
                        if (m != null) members.add(m);
                    }
                }
                out.add(new Card(group ? REVIEW_GROUP : ITEM, item, item.optString("uid"),
                        item.optString("title", item.optString("uid")), "", null, members,
                        new ArrayList<>(), 0));
            }
        }
        return out;
    }

    /** Why a delivery card's units are together: shared parents, linked, the delivery or the minutes. */
    public static final class Explain {
        public final List<String> parents;
        public final boolean linked;
        public final String delivery;
        public final int minutes;

        Explain(List<String> parents, boolean linked, String delivery, int minutes) {
            this.parents = parents;
            this.linked = linked;
            this.delivery = delivery;
            this.minutes = minutes;
        }

        /** The branch or commit without its "branch:" / "commit:" prefix. */
        public String deliveryName() {
            return delivery.replaceFirst("^(branch|commit):", "");
        }
    }

    public static Explain explain(Card card) {
        TreeSet<String> parents = new TreeSet<>();
        boolean linked = false;
        for (JSONObject r : card.reasons) {
            JSONArray ps = r.optJSONArray("parents");
            for (int i = 0; ps != null && i < ps.length(); i++) parents.add(ps.optString(i));
            if (r.optJSONObject("link") != null) linked = true;
        }
        return new Explain(new ArrayList<>(parents), linked, card.delivery == null ? "" : card.delivery, card.span);
    }

    /** Review items that are units (Accept several applies to these). */
    public static List<JSONObject> pendingMembers(Card card) {
        List<JSONObject> out = new ArrayList<>();
        for (JSONObject m : card.members) if (str(m, "outcome").isEmpty()) out.add(m);
        return out;
    }

    /** A string member; "" for a missing or null one (org.json gives "null" otherwise). */
    public static String str(JSONObject o, String key) {
        if (o == null || o.isNull(key)) return "";
        return o.optString(key, "");
    }

    /** "+3 −1" / "+3 −1 · n" parts of a change summary, or null when there is none. */
    public static int[] changes(JSONObject changes) {
        if (changes == null) return null;
        return new int[]{changes.optInt("added"), changes.optInt("removed"), changes.optInt("media")};
    }

    /** True when nothing changed (no lines, no files). */
    public static boolean empty(int[] changes) {
        return changes == null || (changes[0] == 0 && changes[1] == 0 && changes[2] == 0);
    }
}
