package com.masteryip.arkstudio;

import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.Board;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONObject;

/** Task detail: status change ({@code PATCH /api/units/{uid}}) and notes linked to the task. */
public class TaskActivity extends NodeActivity {
    public static Intent intent(Context c, String uid) {
        return new Intent(c, TaskActivity.class).putExtra(EXTRA_UID, uid);
    }

    @Override
    protected void render() {
        titleView.setText(unit.optString("title", uid));
        setSubtitle(unit.optString("uid") + " · " + getString(R.string.task));
        header(this, content, unit);
        LinearLayout actions = Ui.row(this);
        actions.setPadding(0, Ui.dp(this, 10), 0, Ui.dp(this, 4));
        Button status = Ui.button(this, getString(R.string.change_status), true, false);
        status.setEnabled(!session.isReadOnly());
        status.setOnClickListener(v -> chooseStatus());
        actions.addView(status, Ui.lp(0, Ui.WRAP, 1));
        content.addView(actions);
        if (session.isReadOnly()) content.addView(Ui.small(this, getString(R.string.read_only_hint)));
        body(this, content, unit);
        media(this, content, unit);
        if (!session.isReadOnly()) noteComposer();
        links(this, content, unit);
    }

    private void chooseStatus() {
        String currentStatus = unit.optString("status", "inbox");
        String[] labels = new String[Board.STATUSES.size()];
        int checked = -1;
        for (int i = 0; i < labels.length; i++) {
            labels[i] = TasksPage.statusLabel(this, Board.STATUSES.get(i));
            if (Board.STATUSES.get(i).equals(currentStatus)) checked = i;
        }
        new AlertDialog.Builder(this)
                .setTitle(R.string.change_status)
                .setSingleChoiceItems(labels, checked, (d, which) -> {
                    d.dismiss();
                    String next = Board.STATUSES.get(which);
                    if (!next.equals(currentStatus)) setStatus(next);
                })
                .setNegativeButton(R.string.cancel, null)
                .show();
    }

    private void setStatus(String status) {
        call(() -> api.patch("/api/units/" + ArkClient.enc(uid), new JSONObject().put("status", status)),
                result -> {
                    toast(getString(R.string.status_changed, TasksPage.statusLabel(this, status)));
                    reload();
                });
    }

    private void noteComposer() {
        content.addView(Ui.heading(this, getString(R.string.add_note)));
        EditText note = Ui.multiline(this, getString(R.string.note_hint));
        content.addView(note);
        Button save = Ui.button(this, getString(R.string.add_note), false, false);
        save.setOnClickListener(v -> {
            String text = note.getText().toString().trim();
            if (text.isEmpty()) return;
            save.setEnabled(false);
            call(() -> api.post("/api/units", Board.noteFor(unit, text)), result -> {
                note.setText("");
                save.setEnabled(true);
                toast(getString(R.string.note_added));
                reload();
            });
            save.postDelayed(() -> save.setEnabled(true), 4000);
        });
        content.addView(save, Ui.lp(Ui.MATCH, Ui.WRAP));
        content.addView(Ui.small(this, getString(R.string.note_explainer)));
    }
}
