package com.masteryip.arkstudio.model;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/** Tasks board rules shared with {@code web/js/views/tasks.js}. */
public final class Board {
    /** Column order of the web board. */
    public static final List<String> COLUMNS = Arrays.asList("inbox", "open", "active", "review", "blocked", "done");
    /** Every status a task can be moved to. */
    public static final List<String> STATUSES = Arrays.asList("inbox", "open", "active", "review", "blocked",
            "done", "archived");

    private Board() {
    }

    /** ARK importance is p0 (highest) … p3; unknown sorts last. */
    public static int importanceRank(String importance) {
        String value = importance == null ? "" : importance.trim().toLowerCase(Locale.ROOT);
        if (value.length() == 2 && value.charAt(0) == 'p' && Character.isDigit(value.charAt(1))) {
            return value.charAt(1) - '0';
        }
        return 9;
    }

    /** Groups task units by status in column order; cards sorted by importance then title. */
    public static Map<String, List<JSONObject>> columns(JSONArray units) {
        Map<String, List<JSONObject>> out = new LinkedHashMap<>();
        for (String column : COLUMNS) out.put(column, new ArrayList<>());
        for (int i = 0; units != null && i < units.length(); i++) {
            JSONObject unit = units.optJSONObject(i);
            if (unit == null || !"task".equals(unit.optString("type", "task"))) continue;
            String status = unit.optString("status", "inbox");
            if (unit.optBoolean("archived") || "archived".equals(status)) continue;
            List<JSONObject> column = out.get(status);
            if (column == null) column = out.get("inbox");
            column.add(unit);
        }
        for (List<JSONObject> column : out.values()) {
            column.sort((a, b) -> {
                int rank = importanceRank(a.optString("importance")) - importanceRank(b.optString("importance"));
                if (rank != 0) return rank;
                return a.optString("title", "").compareToIgnoreCase(b.optString("title", ""));
            });
        }
        return out;
    }

    /** Request body for a note linked to a task (ARK has no task comments). */
    public static JSONObject noteFor(JSONObject task, String text) {
        JSONObject body = new JSONObject();
        try {
            String clean = text == null ? "" : text.trim();
            String title = clean.split("\n", 2)[0];
            if (title.length() > 80) title = title.substring(0, 77) + "…";
            body.put("type", "note");
            body.put("title", title.isEmpty() ? "Note" : title);
            body.put("body", clean);
            JSONObject link = new JSONObject();
            link.put("target", task.optString("uid", String.valueOf(task.opt("id"))));
            link.put("relation", "relates_to");
            body.put("links", new JSONArray().put(link));
            JSONArray tags = new JSONArray().put("mobile");
            body.put("tags", tags);
        } catch (org.json.JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return body;
    }
}
