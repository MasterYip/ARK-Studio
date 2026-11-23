package com.masteryip.arkstudio;

import android.app.AlertDialog;
import android.view.Gravity;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Labels;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

/** Project overview: switcher, setup status, counts, Needs you, recent activity. */
final class HomePage extends ShellActivity.Page {
    HomePage(ShellActivity activity) {
        super(activity);
    }

    @Override
    CharSequence title() {
        return a.getString(R.string.nav_home);
    }

    @Override
    void load(boolean user) {
        ArkClient api = a.api;
        Async.run(a, () -> {
            JSONObject boot = api.get("/api/bootstrap");
            JSONObject setup = null;
            try {
                setup = api.get("/api/skills/setup/state");
            } catch (ApiException ignored) {
                setup = null; // older servers or no setup skill
            }
            JSONObject status = null;
            try {
                status = api.get("/api/mobile/session");
            } catch (ApiException ignored) {
                status = null;
            }
            return new JSONObject[]{boot, setup, status};
        }, result -> render(result[0], result[1], result[2]), this::failed);
    }

    private String access = "";

    private void render(JSONObject boot, JSONObject setup, JSONObject status) {
        // The account's role in this project ("read", "write", "admin"); the
        // device permission is a ceiling over every project.
        access = status == null || status.isNull("access") ? "" : status.optString("access", "");
        if (status != null && status.optBoolean("authenticated")) {
            JSONObject project = status.optJSONObject("project");
            AccountSession updated = a.session.withStatus(status.optString("user", ""),
                    status.optString("permission", ""), project == null ? "" : project.optString("id"),
                    project == null ? "" : project.optString("name"));
            if (!updated.metadataJson().toString().equals(a.session.metadataJson().toString())) {
                Accounts.update(a, updated);
                a.session = updated;
                a.updateSubtitle();
            }
        }
        content.removeAllViews();
        JSONObject summary = boot.optJSONObject("summary");
        JSONObject stats = boot.optJSONObject("stats");
        content.addView(projectCard(summary));
        if (setup != null) content.addView(setupCard(setup));
        if (stats != null) content.addView(counts(stats));
        NeedsSection.render(a, content, boot.optJSONArray("needs_you"), () -> load(true));
        content.addView(Ui.heading(a, a.getString(R.string.recent_activity)));
        JSONArray activity = boot.optJSONArray("activity");
        int shown = 0;
        for (int i = 0; activity != null && i < activity.length() && shown < 12; i++) {
            JSONObject item = activity.optJSONObject(i);
            if (item == null) continue;
            String action = item.optString("action");
            if (action.equals("link.create") || action.equals("link.delete")) continue;
            content.addView(activityRow(item));
            shown++;
        }
        if (shown == 0) content.addView(Ui.small(a, a.getString(R.string.no_activity)));
        rendered();
    }

    private View projectCard(JSONObject summary) {
        LinearLayout card = Ui.card(a);
        LinearLayout row = Ui.row(a);
        String name = summary == null ? a.session.projectName : summary.optString("name", a.session.projectName);
        row.addView(Ui.oneLine(Ui.label(a, name, 19, Ui.text(a), true)), Ui.lp(0, Ui.WRAP, 1));
        TextView switcher = Ui.label(a, a.getString(R.string.switch_project), 14, Ui.accent(a), true);
        switcher.setPadding(Ui.dp(a, 8), Ui.dp(a, 6), 0, Ui.dp(a, 6));
        Ui.clickable(switcher, v -> openSwitcher());
        row.addView(switcher);
        card.addView(row);
        if (summary != null && !summary.optString("description").isEmpty()) {
            card.addView(Ui.small(a, summary.optString("description")));
        }
        LinearLayout chips = Ui.row(a);
        chips.setPadding(0, Ui.dp(a, 8), 0, 0);
        chips.addView(Ui.chip(a, MainActivity.displayUser(a.session), Ui.text(a), Ui.codeBg(a)));
        boolean readOnly = a.session.isReadOnly() || "read".equals(access);
        chips.addView(Ui.chip(a, a.getString(readOnly ? R.string.perm_read_only : R.string.perm_full),
                readOnly ? Ui.warn(a) : Ui.ok(a), readOnly ? Ui.warnSoft(a) : Ui.okSoft(a)));
        chips.addView(Ui.chip(a, com.masteryip.arkstudio.data.ServerUrl.displayHost(a.session.serverUrl),
                Ui.muted(a), Ui.codeBg(a)));
        card.addView(chips);
        return card;
    }

    private View setupCard(JSONObject setup) {
        String state = setup.optString("state", "unset");
        LinearLayout card = Ui.card(a);
        LinearLayout row = Ui.row(a);
        row.addView(Ui.label(a, a.getString(R.string.setup_status), 15, Ui.text(a), true), Ui.lp(0, Ui.WRAP, 1));
        boolean good = state.equals("confirmed") || state.equals("verified");
        row.addView(Ui.chip(a, Labels.of(a, state), good ? Ui.ok(a) : Ui.warn(a), good ? Ui.okSoft(a) : Ui.warnSoft(a)));
        card.addView(row);
        if (!good) {
            card.addView(Ui.small(a, a.getString(R.string.setup_hint)));
            Ui.clickable(card, v -> a.openWeb(WebRoutes.HOME));
        }
        return card;
    }

    private View counts(JSONObject stats) {
        JSONObject byType = stats.optJSONObject("by_type");
        JSONObject tasks = stats.optJSONObject("tasks");
        String[][] cells = {
                {a.getString(R.string.count_units), String.valueOf(stats.optInt("total"))},
                {a.getString(R.string.count_open_tasks), String.valueOf(tasks == null ? 0 : tasks.optInt("open"))},
                {a.getString(R.string.count_ideas), String.valueOf(byType == null ? 0 : byType.optInt("idea"))},
                {a.getString(R.string.count_overdue), String.valueOf(stats.optInt("overdue"))},
                {a.getString(R.string.count_links), String.valueOf(stats.optInt("links"))},
                {a.getString(R.string.count_media), String.valueOf(stats.optInt("media"))},
        };
        LinearLayout grid = Ui.column(a);
        for (int r = 0; r < 2; r++) {
            LinearLayout row = Ui.row(a);
            for (int c = 0; c < 3; c++) {
                String[] cell = cells[r * 3 + c];
                LinearLayout box = Ui.card(a);
                box.setGravity(Gravity.CENTER_HORIZONTAL);
                TextView value = Ui.label(a, cell[1], 20, Ui.text(a), true);
                value.setGravity(Gravity.CENTER);
                box.addView(value);
                TextView label = Ui.oneLine(Ui.small(a, cell[0]));
                label.setGravity(Gravity.CENTER);
                box.addView(label);
                LinearLayout.LayoutParams params = Ui.lp(0, Ui.WRAP, 1);
                params.setMargins(c == 0 ? 0 : Ui.dp(a, 4), Ui.dp(a, 4), c == 2 ? 0 : Ui.dp(a, 4), Ui.dp(a, 4));
                row.addView(box, params);
                if (r * 3 + c == 1) Ui.clickable(box, v -> a.select(ShellActivity.TAB_TASKS));
                if (r * 3 + c == 0) Ui.clickable(box, v -> a.openWeb(WebRoutes.MAP));
            }
            grid.addView(row);
        }
        return grid;
    }

    private View activityRow(JSONObject item) {
        LinearLayout row = Ui.column(a);
        row.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 8));
        String title = item.optString("unit_title", "");
        String action = Labels.of(a, item.optString("action", ""));
        TextView line = Ui.oneLine(Ui.body(a, title.isEmpty() ? action : title));
        row.addView(line);
        row.addView(Ui.oneLine(Ui.small(a, item.optString("actor", "") + " · " + action + " · "
                + Fmt.ago(a, item.optString("at")))));
        String uid = item.optString("unit_uid", "");
        if (!uid.isEmpty()) {
            Ui.clickable(row, v -> openUnit(uid, item.optString("unit_type")));
        }
        LinearLayout holder = Ui.column(a);
        holder.addView(row);
        holder.addView(Ui.divider(a));
        return holder;
    }

    private void openUnit(String uid, String type) {
        if (uid == null || uid.isEmpty()) return;
        if ("task".equals(type)) a.startActivity(TaskActivity.intent(a, uid));
        else a.startActivity(NodeActivity.intent(a, uid));
    }

    // ----------------------------------------------------------- projects

    private void openSwitcher() {
        ArkClient api = a.api;
        Async.run(a, () -> api.get("/api/mobile/session/projects"), result -> {
            JSONArray projects = result.optJSONArray("projects");
            boolean switchable = result.optBoolean("switchable");
            if (!switchable || projects == null || projects.length() <= 1) {
                new AlertDialog.Builder(a)
                        .setTitle(R.string.switch_project)
                        .setMessage(switchable ? R.string.only_one_project : R.string.project_switch_unsupported)
                        .setPositiveButton(android.R.string.ok, null)
                        .show();
                return;
            }
            String[] names = new String[projects.length()];
            int checked = -1;
            for (int i = 0; i < projects.length(); i++) {
                JSONObject p = projects.optJSONObject(i);
                names[i] = p.optString("name") + "  (" + Labels.of(a, p.optString("permission")) + ")";
                if (p.optString("id").equals(result.optString("current"))) checked = i;
            }
            new AlertDialog.Builder(a)
                    .setTitle(R.string.switch_project)
                    .setSingleChoiceItems(names, checked, (d, which) -> {
                        d.dismiss();
                        switchTo(projects.optJSONObject(which));
                    })
                    .setNegativeButton(R.string.cancel, null)
                    .show();
        }, error -> {
            if (error.getStatus() == 404 || error.getStatus() == 403) {
                new AlertDialog.Builder(a).setTitle(R.string.switch_project)
                        .setMessage(R.string.project_switch_unsupported)
                        .setPositiveButton(android.R.string.ok, null).show();
            } else {
                a.toastError(error);
            }
        });
    }

    private void switchTo(JSONObject project) {
        ArkClient api = a.api;
        String id = project.optString("id");
        Async.run(a, () -> api.post("/api/mobile/session/project", new JSONObject()
                        .put("project_id", id)
                        .put("permission", a.session.isReadOnly() ? "read_only" : "full")),
                result -> {
                    AccountSession updated = a.session.withProject(result.optString("project_id", id),
                            result.optString("project", project.optString("name")),
                            result.optString("permission", a.session.permission));
                    Accounts.update(a, updated);
                    a.sessionChanged(updated);
                    a.toast(a.getString(R.string.switched_project, updated.projectName));
                }, a::toastError);
    }
}
