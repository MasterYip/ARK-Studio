package com.masteryip.arkstudio;

import android.view.View;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.GpuOwnership;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

/**
 * GPU servers and queue (read-only on phones: the server keeps queue submit
 * and cancel off the paired-device allow-list). Owned share is highlighted
 * with the web's {@code gpuOwnership} rules.
 */
final class GpuPage extends ShellActivity.Page {
    GpuPage(ShellActivity activity) {
        super(activity);
    }

    @Override
    CharSequence title() {
        return a.getString(R.string.nav_gpu);
    }

    @Override
    long periodMs() {
        return 15000;
    }

    @Override
    void load(boolean user) {
        ArkClient api = a.api;
        Async.run(a, () -> api.get("/api/gpu/dashboard"), this::render, error -> {
            if (!error.isNetwork() && !error.isSignedOut() && error.getStatus() >= 400
                    && error.getStatus() != 401) {
                unavailable(error);
            } else {
                failed(error);
            }
        });
    }

    private void unavailable(ApiException error) {
        content.removeAllViews();
        LinearLayout card = Ui.card(a);
        card.addView(Ui.label(a, a.getString(R.string.gpu_unavailable), 16, Ui.text(a), true));
        card.addView(Ui.small(a, error.getMessage()));
        content.addView(card);
        rendered();
    }

    private void render(JSONObject dash) {
        content.removeAllViews();
        JSONObject probe = dash.optJSONObject("probe");
        JSONArray servers = probe == null ? null : probe.optJSONArray("servers");
        JSONArray jobs = dash.optJSONArray("jobs");
        String checked = dash.optString("checked_at", "");
        if (!checked.isEmpty()) content.addView(Ui.small(a, a.getString(R.string.gpu_checked, Fmt.ago(a, checked))));
        LinearLayout legend = Ui.row(a);
        legend.setPadding(0, Ui.dp(a, 4), 0, Ui.dp(a, 4));
        legend.addView(Ui.chip(a, a.getString(R.string.gpu_owned_legend), Ui.accent(a), Ui.accentSoft(a)));
        content.addView(legend);
        int shown = 0;
        for (int i = 0; servers != null && i < servers.length(); i++) {
            JSONObject server = servers.optJSONObject(i);
            if (server == null) continue;
            content.addView(serverCard(server, jobs));
            shown++;
        }
        if (shown == 0) content.addView(Ui.small(a, a.getString(R.string.gpu_no_servers)));
        content.addView(Ui.heading(a, a.getString(R.string.gpu_queue)));
        int queued = 0;
        for (int i = 0; jobs != null && i < jobs.length(); i++) {
            JSONObject job = jobs.optJSONObject(i);
            if (job == null) continue;
            content.addView(jobRow(job));
            queued++;
        }
        if (queued == 0) content.addView(Ui.small(a, a.getString(R.string.gpu_queue_empty)));
        TextView note = Ui.small(a, a.getString(R.string.gpu_read_only_note));
        note.setPadding(0, Ui.dp(a, 12), 0, 0);
        content.addView(note);
        TextView web = Ui.label(a, a.getString(R.string.open_gpu_web), 14, Ui.accent(a), true);
        web.setPadding(0, Ui.dp(a, 12), 0, Ui.dp(a, 8));
        Ui.clickable(web, v -> a.openWeb(WebRoutes.GPU));
        content.addView(web);
        rendered();
    }

    private View serverCard(JSONObject server, JSONArray jobs) {
        LinearLayout card = Ui.card(a);
        LinearLayout head = Ui.row(a);
        head.addView(Ui.oneLine(Ui.label(a, server.optString("server", server.optString("host")), 16,
                Ui.text(a), true)), Ui.lp(0, Ui.WRAP, 1));
        boolean ok = server.optBoolean("ok");
        head.addView(Ui.chip(a, a.getString(ok ? R.string.gpu_online : R.string.gpu_offline),
                ok ? Ui.ok(a) : Ui.danger(a), ok ? Ui.okSoft(a) : Ui.dangerSoft(a)));
        card.addView(head);
        if (!ok) {
            String error = server.optString("error", "");
            if (!error.isEmpty()) card.addView(Ui.small(a, error));
            return card;
        }
        JSONObject system = server.optJSONObject("system");
        JSONArray processes = system == null ? null : system.optJSONArray("processes");
        boolean pmon = system != null && system.optBoolean("process_util_available");
        // Jobs for this server only, as in the web view.
        JSONArray serverJobs = new JSONArray();
        String name = server.optString("server");
        for (int i = 0; jobs != null && i < jobs.length(); i++) {
            JSONObject job = jobs.optJSONObject(i);
            if (job != null && name.equals(job.optString("server"))) serverJobs.put(job);
        }
        JSONArray gpus = server.optJSONArray("gpus");
        for (int i = 0; gpus != null && i < gpus.length(); i++) {
            JSONObject gpu = gpus.optJSONObject(i);
            if (gpu == null) continue;
            GpuOwnership own = GpuOwnership.compute(gpu, processes, serverJobs, pmon);
            LinearLayout box = Ui.column(a);
            box.setPadding(0, Ui.dp(a, 10), 0, 0);
            LinearLayout title = Ui.row(a);
            title.addView(Ui.oneLine(Ui.label(a, "GPU " + gpu.optInt("index") + " · " + gpu.optString("name"),
                    14, Ui.text(a), true)), Ui.lp(0, Ui.WRAP, 1));
            if (!own.jobIds.isEmpty()) {
                title.addView(Ui.chip(a, a.getString(R.string.gpu_yours, own.jobIds.size()), Ui.accent(a),
                        Ui.accentSoft(a)));
            }
            box.addView(title);
            double util = own.totalUtilPct;
            box.addView(Ui.small(a, a.getString(R.string.gpu_util, Fmt.pct(util))
                    + (own.utilPct > 0 ? "  ·  " + a.getString(own.utilEstimated ? R.string.gpu_owned_est
                    : R.string.gpu_owned, Fmt.pct(own.utilPct)) : "")));
            box.addView(Ui.bar(a, util, own.utilPct, bandColor(util)));
            double used = gpu.optDouble("memory_used_mb", 0);
            double total = gpu.optDouble("memory_total_mb", 0);
            box.addView(Ui.small(a, a.getString(R.string.gpu_mem, Fmt.mb(used), Fmt.mb(total))
                    + (own.memMb > 0 ? "  ·  " + a.getString(R.string.gpu_owned, Fmt.mb(own.memMb)) : "")
                    + (own.unknownMemory > 0 ? "  (+" + own.unknownMemory + " ?)" : "")));
            box.addView(Ui.bar(a, own.totalMemPct, own.memPct, bandColor(own.totalMemPct)));
            card.addView(box);
        }
        return card;
    }

    private int bandColor(double pct) {
        switch (GpuOwnership.band(pct)) {
            case "ok": return Ui.ok(a);
            case "warn": return Ui.warn(a);
            default: return Ui.danger(a);
        }
    }

    private View jobRow(JSONObject job) {
        LinearLayout card = Ui.card(a);
        String status = job.optString("status");
        LinearLayout row = Ui.row(a);
        row.addView(Ui.chip(a, Fmt.human(status), Ui.statusColor(a, status), Ui.statusSoft(a, status)));
        String rawLabel = job.optString("job", "");
        final String label = rawLabel.isEmpty() || "null".equals(rawLabel) ? job.optString("id") : rawLabel;
        row.addView(Ui.oneLine(Ui.label(a, label, 14, Ui.text(a), true)), Ui.lp(0, Ui.WRAP, 1));
        card.addView(row);
        JSONArray indices = job.optJSONArray("gpu_indices");
        StringBuilder meta = new StringBuilder(job.optString("server", ""));
        if (indices != null && indices.length() > 0) {
            meta.append(" · GPU ");
            for (int i = 0; i < indices.length(); i++) meta.append(i > 0 ? "," : "").append(indices.opt(i));
        }
        String owner = job.optString("owner", "");
        if (!owner.isEmpty()) meta.append(" · ").append(owner);
        String task = job.optString("task", "");
        if (!task.isEmpty() && !"null".equals(task)) meta.append(" · ").append(task);
        card.addView(Ui.oneLine(Ui.small(a, meta.toString())));
        String session = job.optString("session", "");
        if (!session.isEmpty() && !"null".equals(session)) {
            Ui.clickable(card, v -> a.startActivity(RunActivity.intent(a, session, label)));
        }
        return card;
    }
}
