package com.masteryip.arkstudio;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.widget.Toast;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.model.NotifyFeed;
import com.masteryip.arkstudio.platform.Accounts;

import org.json.JSONObject;

/**
 * Opens a notification's item: selects its server, switches the device
 * session to the item's project when the phone may switch (password
 * sign-in; a QR pairing only ever notifies about its own project), then
 * shows Home with the unit, review or run on top. Not exported; only the
 * app's own PendingIntents start it.
 */
public final class NotifyOpenActivity extends Activity {
    static final String EXTRA_SERVER = "notify_server";
    static final String EXTRA_PROJECT = "notify_project";
    static final String EXTRA_TYPE = "notify_type";
    static final String EXTRA_REF = "notify_ref";
    static final String EXTRA_UNIT_TYPE = "notify_unit_type";
    static final String EXTRA_TITLE = "notify_title";

    public static Intent intent(Context c, String server, NotifyFeed.Item item) {
        return new Intent(c, NotifyOpenActivity.class)
                .putExtra(EXTRA_SERVER, server).putExtra(EXTRA_PROJECT, item.projectId)
                .putExtra(EXTRA_TYPE, item.linkType).putExtra(EXTRA_REF, item.linkRef)
                .putExtra(EXTRA_UNIT_TYPE, item.unitType).putExtra(EXTRA_TITLE, item.title)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
    }

    /** A project summary: Home of that project. */
    public static Intent projectIntent(Context c, String server, String projectId) {
        return new Intent(c, NotifyOpenActivity.class)
                .putExtra(EXTRA_SERVER, server).putExtra(EXTRA_PROJECT, projectId).putExtra(EXTRA_TYPE, "project")
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Intent in = getIntent();
        String server = in.getStringExtra(EXTRA_SERVER);
        String project = in.getStringExtra(EXTRA_PROJECT);
        AccountSession session = Accounts.session(this, server);
        if (session == null) {
            startActivity(new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK
                    | Intent.FLAG_ACTIVITY_CLEAR_TASK));
            finish();
            return;
        }
        Accounts.select(this, session.serverUrl);
        if (project == null || project.isEmpty() || project.equals(session.projectId) || !session.canSwitchProjects()) {
            open(in);
            return;
        }
        AccountSession current = session;
        new Thread(() -> {
            String error = null;
            try {
                JSONObject result = Accounts.client(current).withTimeouts(8000, 15000).post("/api/mobile/session/project",
                        new JSONObject().put("project_id", project)
                                .put("permission", current.isReadOnly() ? "read_only" : "full"));
                Accounts.update(this, current.withProject(result.optString("project_id", project),
                        result.optString("project", current.projectName),
                        result.optString("permission", current.permission)));
            } catch (ApiException failed) {
                error = failed.isUnreachable() ? getString(R.string.state_offline) : failed.getMessage();
            } catch (Exception failed) {
                error = failed.getMessage();
            }
            String shown = error;
            runOnUiThread(() -> {
                if (shown != null) Toast.makeText(this, shown, Toast.LENGTH_LONG).show();
                open(in);
            });
        }, "ark-notify-open").start();
    }

    private void open(Intent in) {
        String type = in.getStringExtra(EXTRA_TYPE);
        String ref = in.getStringExtra(EXTRA_REF);
        Intent home = ShellActivity.intent(this, ShellActivity.TAB_HOME).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        Intent target = null;
        if ("unit".equals(type) && ref != null && !ref.isEmpty()) {
            target = "task".equals(in.getStringExtra(EXTRA_UNIT_TYPE)) ? TaskActivity.intent(this, ref)
                    : NodeActivity.intent(this, ref);
        } else if ("session".equals(type) && ref != null && !ref.isEmpty()) {
            target = RunActivity.intent(this, ref, in.getStringExtra(EXTRA_TITLE));
        }
        if (target == null) startActivity(home);
        else startActivities(new Intent[]{home, target});
        finish();
    }
}
