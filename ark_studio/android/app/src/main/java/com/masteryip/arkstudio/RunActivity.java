package com.masteryip.arkstudio;

import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.graphics.Typeface;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.Transcript;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Labels;
import com.masteryip.arkstudio.ui.MarkdownView;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * One agent run as a phone-readable thread. Polls the same endpoints as the
 * web Runs view ({@code GET /api/agent-sessions/{id}} and {@code …/transcript})
 * and repaints only when the transcript signature changes.
 */
public class RunActivity extends BaseActivity {
    private static final String EXTRA_ID = "id";
    private static final String EXTRA_TITLE = "title";
    private static final int MAX_OUTPUT = 6000;

    private String id;
    private JSONObject run;
    private String signature = "";
    private JSONObject lastTranscript;
    private boolean lastShown;
    private final Set<String> expanded = new HashSet<>();
    private EditText composer;
    private Button send;
    private TextView stop;
    private TextView hint;
    private boolean polling;
    private final Runnable poller = this::poll;

    public static Intent intent(Context c, String id, String title) {
        return new Intent(c, RunActivity.class).putExtra(EXTRA_ID, id).putExtra(EXTRA_TITLE, title);
    }

    static String statusLabel(Context c, String status) {
        switch (status == null ? "" : status) {
            case "running": return c.getString(R.string.run_running);
            case "idle": return c.getString(R.string.run_idle);
            case "starting": return c.getString(R.string.run_starting);
            case "stopping": return c.getString(R.string.run_stopping);
            case "errored": return c.getString(R.string.run_errored);
            case "stopped": return c.getString(R.string.run_stopped);
            case "archived": return c.getString(R.string.run_archived);
            default: return Labels.of(c, status);
        }
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        id = getIntent().getStringExtra(EXTRA_ID);
        String title = getIntent().getStringExtra(EXTRA_TITLE);
        buildFrame(title == null || title.isEmpty() ? id : title, true);
        stop = addAction(getString(R.string.stop), v -> confirmStop());
        stop.setTextColor(Ui.danger(this));
        stop.setVisibility(View.GONE);
        addAction(getString(R.string.web_short), v -> openWeb(WebRoutes.RUNS));
        buildComposer();
        showLoading();
    }

    private void buildComposer() {
        LinearLayout bar = Ui.column(this);
        bar.setBackgroundColor(Ui.color(this, R.color.ark_bar));
        bar.setPadding(Ui.dp(this, 10), Ui.dp(this, 6), Ui.dp(this, 10), Ui.dp(this, 8));
        hint = Ui.small(this, "");
        hint.setTextSize(12);
        bar.addView(hint);
        LinearLayout row = Ui.row(this);
        composer = Ui.multiline(this, getString(R.string.composer_hint));
        composer.setMinLines(1);
        composer.setMaxLines(4);
        row.addView(composer, Ui.lp(0, Ui.WRAP, 1));
        send = Ui.button(this, getString(R.string.send), true, false);
        send.setOnClickListener(v -> sendMessage());
        LinearLayout.LayoutParams params = Ui.lp(Ui.WRAP, Ui.WRAP);
        params.setMargins(Ui.dp(this, 8), 0, 0, 0);
        row.addView(send, params);
        bar.addView(row);
        root.addView(Ui.divider(this));
        root.addView(bar, Ui.lp(Ui.MATCH, Ui.WRAP));
        if (session.isReadOnly()) {
            composer.setEnabled(false);
            send.setEnabled(false);
            hint.setText(R.string.read_only_hint);
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (!ready()) return;
        polling = true;
        poll();
    }

    @Override
    protected void onPause() {
        super.onPause();
        polling = false;
        Async.main().removeCallbacks(poller);
    }

    private void poll() {
        Async.main().removeCallbacks(poller);
        if (!polling) return;
        reload();
    }

    @Override
    protected void reload() {
        ArkClient client = api;
        String path = "/api/agent-sessions/" + ArkClient.enc(id);
        Async.run(this, () -> new JSONObject[]{client.get(path), client.get(path + "/transcript")}, result -> {
            doneRefreshing();
            run = result[0].optJSONObject("session");
            render(result[1]);
            schedule();
        }, error -> {
            showError(error);
            schedule();
        });
    }

    private void schedule() {
        if (!polling) return;
        boolean active = run != null && Transcript.isActive(run);
        Async.main().postDelayed(poller, active ? 2000 : 10000);
    }

    private void render(JSONObject transcript) {
        lastTranscript = transcript;
        if (run != null) {
            String status = run.optString("status");
            String meta = statusLabel(this, status);
            String model = run.optString("model", "");
            if (!model.isEmpty() && !"null".equals(model)) meta += " · " + model;
            setSubtitle(meta);
            boolean active = Transcript.isActive(run);
            stop.setVisibility(active && !session.isReadOnly() ? View.VISIBLE : View.GONE);
            if (!session.isReadOnly()) {
                hint.setText(active ? R.string.composer_steer : R.string.composer_continue);
                send.setText(active ? R.string.steer : R.string.continue_run);
            }
        }
        String next = transcript.optString("signature", "") + "|" + (run == null ? "" : run.optString("status"));
        boolean userToggled = signature.isEmpty() && lastShown;
        if (next.equals(signature) && content.getChildCount() > 0) return;
        lastShown = true;
        signature = next;
        boolean atBottom = !scroll.canScrollVertically(1);
        content.removeAllViews();
        JSONArray items = transcript.optJSONArray("items");
        List<Transcript.Block> blocks = Transcript.threadBlocks(items);
        if (blocks.isEmpty()) content.addView(Ui.small(this, getString(R.string.thread_empty)));
        for (Transcript.Block block : blocks) content.addView(blockView(block));
        JSONObject stats = transcript.optJSONObject("stats");
        if (stats != null && stats.optInt("pending_permissions") > 0) {
            TextView pending = Ui.small(this, getString(R.string.approvals_pending, stats.optInt("pending_permissions")));
            pending.setTextColor(Ui.warn(this));
            content.addView(pending);
        }
        if ((atBottom || content.getChildCount() < 3) && !userToggled) scroll.post(() -> scroll.fullScroll(View.FOCUS_DOWN));
    }

    private View blockView(Transcript.Block block) {
        switch (block.kind) {
            case "separator": {
                TextView sep = Ui.small(this, Fmt.ago(this, block.at));
                sep.setGravity(Gravity.CENTER);
                sep.setPadding(0, Ui.dp(this, 10), 0, Ui.dp(this, 4));
                sep.setTextSize(11);
                return sep;
            }
            case "user": {
                LinearLayout wrap = Ui.row(this);
                wrap.setGravity(Gravity.END);
                TextView bubble = Ui.body(this, block.item.optString("text"));
                bubble.setTextIsSelectable(true);
                bubble.setBackground(Ui.rounded(Ui.accentSoft(this), 0, this, 14));
                bubble.setPadding(Ui.dp(this, 12), Ui.dp(this, 8), Ui.dp(this, 12), Ui.dp(this, 8));
                LinearLayout.LayoutParams params = Ui.lp(Ui.WRAP, Ui.WRAP);
                params.setMargins(Ui.dp(this, 48), Ui.dp(this, 4), 0, Ui.dp(this, 4));
                wrap.addView(bubble, params);
                return wrap;
            }
            case "assistant": {
                String text = block.item.optString("text");
                if (block.item.optBoolean("markdown", true)) {
                    LinearLayout md = MarkdownView.render(this, text, href -> Links.open(this, href));
                    md.setPadding(0, Ui.dp(this, 4), 0, Ui.dp(this, 4));
                    return md;
                }
                TextView plain = Ui.body(this, text);
                plain.setTextIsSelectable(true);
                return plain;
            }
            case "tools":
                return toolsView(block);
            case "error": {
                TextView error = Ui.body(this, block.item.optString("text"));
                error.setTextColor(Ui.danger(this));
                error.setBackground(Ui.rounded(Ui.dangerSoft(this), 0, this, 10));
                error.setPadding(Ui.dp(this, 10), Ui.dp(this, 8), Ui.dp(this, 10), Ui.dp(this, 8));
                error.setLayoutParams(Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
                return error;
            }
            case "reasoning": {
                String key = block.key;
                boolean open = expanded.contains(key);
                String text = block.item.optString("text");
                TextView view = Ui.small(this, (open ? "▾ " : "▸ ") + getString(R.string.reasoning)
                        + (open ? "\n" + text : ""));
                view.setTypeface(Typeface.create("sans-serif", Typeface.ITALIC));
                view.setPadding(0, Ui.dp(this, 4), 0, Ui.dp(this, 4));
                Ui.clickable(view, v -> toggle(key));
                return view;
            }
            case "permission": {
                LinearLayout card = Ui.card(this);
                card.addView(Ui.label(this, getString(R.string.permission_request), 14, Ui.warn(this), true));
                String desc = block.item.optString("description", block.item.optString("command",
                        block.item.optString("tool")));
                card.addView(Ui.small(this, desc));
                card.addView(Ui.small(this, Labels.of(this, block.item.optString("status", block.item.optString("decision")))));
                return card;
            }
            default: {
                TextView notice = Ui.small(this, block.item.optString("text"));
                notice.setPadding(0, Ui.dp(this, 2), 0, Ui.dp(this, 2));
                return notice;
            }
        }
    }

    private View toolsView(Transcript.Block block) {
        LinearLayout group = Ui.column(this);
        group.setBackground(Ui.rounded(Ui.cardColor(this), Ui.border(this), this, 10));
        group.setPadding(Ui.dp(this, 10), Ui.dp(this, 6), Ui.dp(this, 10), Ui.dp(this, 6));
        group.setLayoutParams(Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
        boolean open = expanded.contains(block.key);
        boolean running = false;
        for (JSONObject tool : block.items) if ("running".equals(tool.optString("status"))) running = true;
        TextView header = Ui.oneLine(Ui.label(this, (open ? "▾  " : "▸  ")
                + Transcript.toolSummary(block.items), 13, running ? Ui.accent(this) : Ui.muted(this), true));
        header.setPadding(0, Ui.dp(this, 6), 0, Ui.dp(this, 6));
        Ui.clickable(header, v -> toggle(block.key));
        group.addView(header);
        if (!open) {
            // Collapsed: the latest tool only, like the web's live row.
            JSONObject last = block.items.get(block.items.size() - 1);
            group.addView(Ui.oneLine(Ui.small(this, Transcript.toolTitle(last))));
            return group;
        }
        for (JSONObject tool : block.items) {
            String key = block.key + "/" + tool.optString("id");
            boolean toolOpen = expanded.contains(key);
            String status = tool.optString("status");
            String mark = "error".equals(status) ? "✕ " : "running".equals(status) ? "… " : "✓ ";
            TextView row = Ui.oneLine(Ui.label(this, mark + Transcript.toolTitle(tool), 13,
                    "error".equals(status) ? Ui.danger(this) : Ui.text(this), false));
            row.setTypeface(Typeface.MONOSPACE);
            row.setPadding(0, Ui.dp(this, 6), 0, Ui.dp(this, 6));
            Ui.clickable(row, v -> toggle(key));
            group.addView(row);
            if (toolOpen) {
                String output = Transcript.outputText(tool);
                if (output.length() > MAX_OUTPUT) {
                    output = output.substring(0, MAX_OUTPUT) + "\n… " + getString(R.string.output_truncated);
                }
                if (output.isEmpty()) output = getString(R.string.no_output);
                HorizontalScrollView scrollOut = new HorizontalScrollView(this);
                TextView out = Ui.label(this, output, 11.5f, Ui.text(this), false);
                out.setTypeface(Typeface.MONOSPACE);
                out.setTextIsSelectable(true);
                out.setPadding(Ui.dp(this, 8), Ui.dp(this, 6), Ui.dp(this, 8), Ui.dp(this, 6));
                scrollOut.setBackground(Ui.rounded(Ui.codeBg(this), 0, this, 6));
                scrollOut.addView(out);
                group.addView(scrollOut, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 0, 0, 6));
            }
        }
        return group;
    }

    private void toggle(String key) {
        if (!expanded.remove(key)) expanded.add(key);
        signature = "";
        int y = scroll.getScrollY();
        if (lastTranscript != null) render(lastTranscript);
        scroll.post(() -> scroll.scrollTo(0, y));
    }

    private void reloadSoon() {
        Async.main().removeCallbacks(poller);
        Async.main().post(poller);
    }

    private void sendMessage() {
        String text = composer.getText().toString().trim();
        if (text.isEmpty() || run == null) return;
        String mode = Transcript.composerMode(run);
        String path = "/api/agent-sessions/" + ArkClient.enc(id) + ("steer".equals(mode) ? "/steer" : "/continue");
        send.setEnabled(false);
        Async.run(this, () -> api.post(path, new JSONObject().put("text", text)), result -> {
            send.setEnabled(true);
            composer.setText("");
            JSONObject steer = result.optJSONObject("steer");
            toast(steer != null ? getString(R.string.steer_status, Labels.of(this, steer.optString("status")))
                    : getString(R.string.continued));
            reloadSoon();
        }, error -> {
            send.setEnabled(true);
            toastError(error);
        });
    }

    private void confirmStop() {
        new AlertDialog.Builder(this)
                .setTitle(R.string.stop_run)
                .setMessage(R.string.stop_run_message)
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.stop, (d, w) -> call(
                        () -> api.post("/api/agent-sessions/" + ArkClient.enc(id) + "/stop", new JSONObject()),
                        result -> {
                            toast(getString(R.string.stopping));
                            reloadSoon();
                        }))
                .show();
    }
}
