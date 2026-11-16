package com.masteryip.arkstudio;

import android.Manifest;
import android.app.AlertDialog;
import android.content.Intent;
import android.os.Build;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.Switch;
import android.widget.TextView;

import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.platform.Notifications;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

/** Account, web workspaces, notifications, switch server and sign-out. */
final class MorePage extends ShellActivity.Page {
    static final int REQUEST_NOTIFY = 41;

    MorePage(ShellActivity activity) {
        super(activity);
    }

    @Override
    CharSequence title() {
        return a.getString(R.string.nav_more);
    }

    @Override
    void load(boolean user) {
        render();
    }

    private void render() {
        content.removeAllViews();
        AccountSession s = a.session;
        LinearLayout account = Ui.card(a);
        account.addView(Ui.label(a, MainActivity.displayUser(s), 18, Ui.text(a), true));
        account.addView(Ui.small(a, s.serverUrl));
        LinearLayout chips = Ui.row(a);
        chips.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 4));
        chips.addView(Ui.chip(a, a.getString(s.isReadOnly() ? R.string.perm_read_only : R.string.perm_full),
                s.isReadOnly() ? Ui.warn(a) : Ui.ok(a), s.isReadOnly() ? Ui.warnSoft(a) : Ui.okSoft(a)));
        chips.addView(Ui.chip(a, a.getString(AccountSession.SOURCE_PASSWORD.equals(s.source)
                ? R.string.via_password : R.string.via_qr), Ui.muted(a), Ui.codeBg(a)));
        if (!PairingLink.isHttps(s.serverUrl)) {
            boolean lan = PairingLink.isPrivateHost(s.serverUrl);
            chips.addView(Ui.chip(a, a.getString(lan ? R.string.chip_http_lan : R.string.chip_unencrypted),
                    lan ? Ui.muted(a) : Ui.danger(a), lan ? Ui.codeBg(a) : Ui.dangerSoft(a)));
        }
        account.addView(chips);
        account.addView(Ui.small(a, a.getString(R.string.project_line, s.projectName)));
        content.addView(account);

        content.addView(Ui.heading(a, a.getString(R.string.web_workspaces)));
        addLink(R.string.web_map, WebRoutes.MAP);
        addLink(R.string.web_editor, WebRoutes.EDITOR);
        addLink(R.string.web_ideas, WebRoutes.IDEAS);
        addLink(R.string.web_plan, WebRoutes.PLAN);
        addLink(R.string.web_output, WebRoutes.OUTPUT);
        addLink(R.string.web_assistant, WebRoutes.ASSISTANT);
        addLink(R.string.web_files, WebRoutes.FILES);
        addLink(R.string.web_settings, WebRoutes.SETTINGS);

        content.addView(Ui.heading(a, a.getString(R.string.notifications)));
        LinearLayout notify = Ui.card(a);
        LinearLayout row = Ui.row(a);
        TextView label = Ui.body(a, a.getString(R.string.notify_toggle));
        row.addView(label, Ui.lp(0, Ui.WRAP, 1));
        Switch toggle = new Switch(a);
        toggle.setChecked(Accounts.notificationsEnabled(a) && Notifications.permitted(a));
        toggle.setContentDescription(a.getString(R.string.notify_toggle));
        toggle.setOnCheckedChangeListener((v, on) -> setNotifications(on));
        row.addView(toggle);
        notify.addView(row);
        notify.addView(Ui.small(a, a.getString(R.string.notify_detail)));
        content.addView(notify);

        content.addView(Ui.heading(a, a.getString(R.string.account)));
        LinearLayout buttons = Ui.column(a);
        android.widget.Button servers = Ui.button(a, a.getString(R.string.switch_server), false, false);
        servers.setOnClickListener(v -> {
            Intent intent = new Intent(a, MainActivity.class).setAction(MainActivity.ACTION_SETUP);
            a.startActivity(intent);
        });
        buttons.addView(servers, Ui.margins(a, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
        android.widget.Button out = Ui.button(a, a.getString(R.string.sign_out), false, true);
        out.setOnClickListener(v -> confirmSignOut());
        buttons.addView(out, Ui.margins(a, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
        content.addView(buttons);
        TextView version = Ui.small(a, a.getString(R.string.version_line, BuildConfig.VERSION_NAME,
                BuildConfig.VERSION_CODE));
        version.setPadding(0, Ui.dp(a, 16), 0, 0);
        content.addView(version);
        rendered();
    }

    private void addLink(int label, String route) {
        LinearLayout row = Ui.row(a);
        row.setPadding(Ui.dp(a, 4), Ui.dp(a, 12), Ui.dp(a, 4), Ui.dp(a, 12));
        row.addView(Ui.body(a, a.getString(label)), Ui.lp(0, Ui.WRAP, 1));
        row.addView(Ui.label(a, "›", 20, Ui.faint(a), false));
        Ui.clickable(row, v -> a.openWeb(route));
        content.addView(row);
        content.addView(Ui.divider(a));
    }

    private void setNotifications(boolean on) {
        Accounts.setNotificationsEnabled(a, on);
        if (on) {
            Notifications.ensureChannel(a);
            if (Build.VERSION.SDK_INT >= 33 && !Notifications.permitted(a)) {
                a.requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, REQUEST_NOTIFY);
            }
            Notifications.schedulePolling(a);
            a.toast(a.getString(R.string.notify_on));
        } else {
            Notifications.cancelPolling(a);
        }
    }

    private void confirmSignOut() {
        new AlertDialog.Builder(a)
                .setTitle(R.string.sign_out)
                .setMessage(a.getString(R.string.sign_out_confirm, a.session.serverUrl))
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.sign_out, (d, w) -> signOut())
                .show();
    }

    /** Revokes this device token on the server, then forgets it locally. */
    private void signOut() {
        AccountSession s = a.session;
        Async.run(a, () -> Accounts.client(s).withTimeouts(5000, 8000).delete("/api/mobile/session"),
                ok -> {
                    Accounts.forget(a, s.serverUrl);
                    a.toServers(a.getString(R.string.signed_out));
                },
                error -> {
                    Accounts.forget(a, s.serverUrl);
                    a.toServers(a.getString(error.isNetwork() ? R.string.signed_out_offline : R.string.signed_out));
                });
    }
}
