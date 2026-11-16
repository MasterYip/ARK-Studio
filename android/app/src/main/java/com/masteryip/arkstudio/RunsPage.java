package com.masteryip.arkstudio;

import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.Transcript;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONObject;

import java.util.List;

/** Agent runs (sessions) list, refreshed while visible like the web Runs view. */
final class RunsPage extends ShellActivity.Page {
    RunsPage(ShellActivity activity) {
        super(activity);
    }

    @Override
    CharSequence title() {
        return a.getString(R.string.nav_runs);
    }

    @Override
    long periodMs() {
        return 5000;
    }

    @Override
    void load(boolean user) {
        ArkClient api = a.api;
        Async.run(a, () -> api.get("/api/agent-sessions"), result -> render(result), this::failed);
    }

    private void render(JSONObject result) {
        content.removeAllViews();
        List<JSONObject> sessions = Transcript.sortSessions(result.optJSONArray("sessions"));
        int active = 0;
        for (JSONObject s : sessions) if (Transcript.isActive(s)) active++;
        content.addView(Ui.heading(a, a.getString(R.string.runs_summary, active, sessions.size())));
        if (sessions.isEmpty()) {
            TextView empty = Ui.small(a, a.getString(R.string.no_runs));
            empty.setPadding(0, Ui.dp(a, 12), 0, Ui.dp(a, 12));
            content.addView(empty);
        }
        for (JSONObject s : sessions) {
            if ("archived".equals(s.optString("status"))) continue;
            content.addView(card(s));
        }
        TextView web = Ui.label(a, a.getString(R.string.open_runs_web), 14, Ui.accent(a), true);
        web.setPadding(0, Ui.dp(a, 16), 0, Ui.dp(a, 8));
        Ui.clickable(web, v -> a.openWeb(WebRoutes.RUNS));
        content.addView(web);
        rendered();
    }

    private View card(JSONObject s) {
        LinearLayout card = Ui.card(a);
        String status = s.optString("status");
        LinearLayout row = Ui.row(a);
        row.addView(Ui.chip(a, RunActivity.statusLabel(a, status), Ui.statusColor(a, status), Ui.statusSoft(a, status)));
        row.addView(Ui.oneLine(Ui.label(a, s.optString("title", s.optString("id")), 15, Ui.text(a), true)),
                Ui.lp(0, Ui.WRAP, 1));
        card.addView(row);
        StringBuilder meta = new StringBuilder();
        String platform = s.optString("platform_label", s.optString("platform", ""));
        if (!platform.isEmpty()) meta.append(platform);
        String model = s.optString("model", "");
        if (!model.isEmpty() && !"null".equals(model)) meta.append(meta.length() > 0 ? " · " : "").append(model);
        String task = s.optString("task_ref", "");
        if (!task.isEmpty() && !"null".equals(task)) meta.append(meta.length() > 0 ? " · " : "").append(task);
        String ago = Fmt.ago(a, s.optString("last_activity_at", s.optString("started_at", "")));
        if (!ago.isEmpty()) meta.append(meta.length() > 0 ? " · " : "").append(ago);
        TextView line = Ui.oneLine(Ui.small(a, meta.toString()));
        line.setPadding(0, Ui.dp(a, 4), 0, 0);
        card.addView(line);
        JSONObject approvals = s.optJSONObject("approvals");
        if (approvals != null && approvals.optInt("pending") > 0) {
            card.addView(Ui.small(a, a.getString(R.string.approvals_pending, approvals.optInt("pending"))));
        }
        String error = s.optString("error", "");
        if (!error.isEmpty() && !"null".equals(error)) {
            TextView err = Ui.oneLine(Ui.small(a, error));
            err.setTextColor(Ui.danger(a));
            card.addView(err);
        }
        Ui.clickable(card, v -> a.startActivity(RunActivity.intent(a, s.optString("id"), s.optString("title"))));
        return card;
    }
}
