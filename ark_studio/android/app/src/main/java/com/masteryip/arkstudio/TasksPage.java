package com.masteryip.arkstudio;

import android.view.View;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.Board;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Labels;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.List;
import java.util.Map;

/** Tasks board: one status column at a time with counts, phone-sized cards. */
final class TasksPage extends ShellActivity.Page {
    private String column = "active";
    private Map<String, List<JSONObject>> columns;

    TasksPage(ShellActivity activity) {
        super(activity);
    }

    @Override
    CharSequence title() {
        return a.getString(R.string.nav_tasks);
    }

    @Override
    long periodMs() {
        return 30000;
    }

    @Override
    void load(boolean user) {
        ArkClient api = a.api;
        Async.run(a, () -> api.get("/api/units?type=task&limit=1000&facets=0&slim=1"), result -> {
            JSONArray items = result.optJSONArray("items");
            columns = Board.columns(items);
            if (!loaded && columns.get(column).isEmpty()) {
                for (String name : Board.COLUMNS) {
                    if (!columns.get(name).isEmpty() && !name.equals("done")) {
                        column = name;
                        break;
                    }
                }
            }
            render();
        }, this::failed);
    }

    private void render() {
        content.removeAllViews();
        HorizontalScrollView strip = new HorizontalScrollView(a);
        strip.setHorizontalScrollBarEnabled(false);
        LinearLayout chips = Ui.row(a);
        chips.setPadding(0, Ui.dp(a, 6), 0, Ui.dp(a, 6));
        for (String name : Board.COLUMNS) {
            boolean on = name.equals(column);
            int count = columns.get(name).size();
            TextView chip = Ui.label(a, statusLabel(a, name) + "  " + count, 14,
                    on ? Ui.color(a, R.color.ark_on_accent) : Ui.text(a), true);
            chip.setBackground(Ui.rounded(on ? Ui.accent(a) : Ui.cardColor(a), on ? 0 : Ui.border(a), a, 999));
            chip.setPadding(Ui.dp(a, 14), Ui.dp(a, 7), Ui.dp(a, 14), Ui.dp(a, 7));
            LinearLayout.LayoutParams params = Ui.lp(Ui.WRAP, Ui.WRAP);
            params.setMargins(0, 0, Ui.dp(a, 8), 0);
            chips.addView(chip, params);
            Ui.clickable(chip, v -> {
                column = name;
                render();
            });
        }
        strip.addView(chips);
        content.addView(strip);
        List<JSONObject> cards = columns.get(column);
        if (cards.isEmpty()) {
            TextView empty = Ui.small(a, a.getString(R.string.column_empty, statusLabel(a, column)));
            empty.setPadding(0, Ui.dp(a, 24), 0, Ui.dp(a, 12));
            content.addView(empty);
        }
        for (JSONObject task : cards) content.addView(card(task));
        TextView web = Ui.label(a, a.getString(R.string.open_board_web), 14, Ui.accent(a), true);
        web.setPadding(0, Ui.dp(a, 16), 0, Ui.dp(a, 8));
        Ui.clickable(web, v -> a.openWeb(WebRoutes.BOARD));
        content.addView(web);
        rendered();
    }

    static String statusLabel(android.content.Context c, String status) {
        switch (status == null ? "" : status) {
            case "inbox": return c.getString(R.string.status_inbox);
            case "open": return c.getString(R.string.status_open);
            case "active": return c.getString(R.string.status_active);
            case "review": return c.getString(R.string.status_review);
            case "blocked": return c.getString(R.string.status_blocked);
            case "done": return c.getString(R.string.status_done);
            case "archived": return c.getString(R.string.status_archived);
            default: return Labels.of(c, status);
        }
    }

    private View card(JSONObject task) {
        LinearLayout card = Ui.card(a);
        LinearLayout row = Ui.row(a);
        String importance = task.optString("importance", "");
        if (!importance.isEmpty()) {
            int rank = Board.importanceRank(importance);
            int color = rank == 0 ? Ui.danger(a) : rank == 1 ? Ui.warn(a) : Ui.muted(a);
            int soft = rank == 0 ? Ui.dangerSoft(a) : rank == 1 ? Ui.warnSoft(a) : Ui.codeBg(a);
            row.addView(Ui.chip(a, importance.toUpperCase(java.util.Locale.ROOT), color, soft));
        }
        row.addView(Ui.oneLine(Ui.small(a, task.optString("uid"))), Ui.lp(0, Ui.WRAP, 1));
        String end = task.optString("end_at", "");
        if (!end.isEmpty() && !"null".equals(end)) row.addView(Ui.small(a, end.length() > 10 ? end.substring(0, 10) : end));
        card.addView(row);
        TextView title = Ui.label(a, task.optString("title"), 15, Ui.text(a), true);
        title.setPadding(0, Ui.dp(a, 4), 0, 0);
        card.addView(title);
        JSONArray assignees = task.optJSONArray("assignees");
        if (assignees != null && assignees.length() > 0) {
            StringBuilder names = new StringBuilder();
            for (int i = 0; i < assignees.length(); i++) {
                Object who = assignees.opt(i);
                String name = who instanceof JSONObject ? ((JSONObject) who).optString("name",
                        ((JSONObject) who).optString("username", "")) : String.valueOf(who);
                if (names.length() > 0) names.append(", ");
                names.append(name);
            }
            card.addView(Ui.oneLine(Ui.small(a, "@ " + names)));
        }
        Ui.clickable(card, v -> a.startActivity(TaskActivity.intent(a, task.optString("uid"))));
        return card;
    }
}
