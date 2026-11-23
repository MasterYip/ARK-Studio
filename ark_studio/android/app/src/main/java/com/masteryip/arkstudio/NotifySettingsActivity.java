package com.masteryip.arkstudio;

import android.Manifest;
import android.app.AlertDialog;
import android.app.TimePickerDialog;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.widget.CompoundButton;
import android.widget.LinearLayout;
import android.widget.Switch;
import android.widget.TextView;

import androidx.annotation.NonNull;

import com.masteryip.arkstudio.model.NotifyFeed;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.platform.Notifications;
import com.masteryip.arkstudio.ui.Ui;
import com.masteryip.arkstudio.update.UpdateActivity;

import java.util.List;
import java.util.Locale;

/**
 * Notification settings: the opt-in (with the Android 13 permission), what
 * to notify about, quiet hours (notifications arrive silently) and muting
 * single projects, plus a link to About and updates.
 */
public final class NotifySettingsActivity extends BaseActivity {
    private static final int REQUEST_PERMISSION = 42;

    public static Intent intent(Context c) {
        return new Intent(c, NotifySettingsActivity.class);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        buildFrame(getString(R.string.notify_settings), true);
        render();
    }

    @Override
    protected void reload() {
        render();
        doneRefreshing();
    }

    private boolean enabled() {
        return Accounts.notificationsEnabled(this) && Notifications.permitted(this);
    }

    private void render() {
        content.removeAllViews();
        NotifyFeed.Settings s = Notifications.settings(this);

        LinearLayout master = Ui.card(this);
        master.addView(switchRow(getString(R.string.notify_toggle), enabled(), (v, on) -> {
            if (on) optIn();
            else turnOff();
        }));
        master.addView(Ui.small(this, getString(R.string.notify_detail)));
        if (Accounts.notificationsEnabled(this) && !Notifications.permitted(this)) {
            TextView blocked = Ui.small(this, getString(R.string.notify_blocked));
            blocked.setTextColor(Ui.danger(this));
            master.addView(blocked);
        }
        content.addView(master);

        content.addView(Ui.heading(this, getString(R.string.notify_what)));
        LinearLayout kinds = Ui.card(this);
        kinds.addView(switchRow(getString(R.string.notify_ch_review), s.reviews, (v, on) -> change(x -> x.reviews = on)));
        kinds.addView(switchRow(getString(R.string.notify_ch_decision), s.decisions, (v, on) -> change(x -> x.decisions = on)));
        kinds.addView(switchRow(getString(R.string.notify_ch_assigned), s.assigned, (v, on) -> change(x -> x.assigned = on)));
        kinds.addView(switchRow(getString(R.string.notify_ch_session), s.sessionErrors,
                (v, on) -> change(x -> x.sessionErrors = on)));
        kinds.addView(switchRow(getString(R.string.notify_kind_session_done), s.sessionsDone,
                (v, on) -> change(x -> x.sessionsDone = on)));
        content.addView(kinds);

        content.addView(Ui.heading(this, getString(R.string.notify_quiet)));
        LinearLayout quiet = Ui.card(this);
        quiet.addView(switchRow(getString(R.string.notify_quiet_toggle), s.quiet, (v, on) -> {
            change(x -> x.quiet = on);
            render();
        }));
        if (s.quiet) {
            quiet.addView(timeRow(getString(R.string.notify_quiet_from), s.quietStart, true));
            quiet.addView(timeRow(getString(R.string.notify_quiet_to), s.quietEnd, false));
        }
        quiet.addView(Ui.small(this, getString(R.string.notify_quiet_detail)));
        content.addView(quiet);

        content.addView(Ui.heading(this, getString(R.string.notify_projects)));
        LinearLayout projects = Ui.card(this);
        List<Object[]> known = Notifications.knownProjects(this);
        if (known.isEmpty()) {
            projects.addView(Ui.small(this, getString(R.string.notify_projects_empty)));
        } else {
            java.util.Set<Object> servers = new java.util.HashSet<>();
            for (Object[] p : known) servers.add(p[0]);
            for (Object[] p : known) {
                String key = NotifyFeed.Settings.muteKey((String) p[0], (String) p[2]);
                String name = (String) p[3];
                if (servers.size() > 1 && !String.valueOf(p[1]).isEmpty()) name = name + "  ·  " + p[1];
                projects.addView(switchRow(name, !s.mutedProjects.contains(key), (v, on) -> change(x -> {
                    if (on) x.mutedProjects.remove(key);
                    else x.mutedProjects.add(key);
                })));
            }
        }
        content.addView(projects);

        android.widget.Button system = Ui.button(this, getString(R.string.notify_system_settings), false, false);
        system.setOnClickListener(v -> openSystemSettings());
        content.addView(system, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 12, 0, 4));
        android.widget.Button about = Ui.button(this, getString(R.string.update_title), false, false);
        about.setOnClickListener(v -> startActivity(new Intent(this, UpdateActivity.class)));
        content.addView(about, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
    }

    private interface Edit {
        void apply(NotifyFeed.Settings s);
    }

    private void change(Edit edit) {
        NotifyFeed.Settings s = Notifications.settings(this);
        edit.apply(s);
        Notifications.saveSettings(this, s);
    }

    private LinearLayout switchRow(String label, boolean checked, CompoundButton.OnCheckedChangeListener listener) {
        LinearLayout row = Ui.row(this);
        row.setPadding(0, Ui.dp(this, 6), 0, Ui.dp(this, 6));
        row.addView(Ui.body(this, label), Ui.lp(0, Ui.WRAP, 1));
        Switch toggle = new Switch(this);
        toggle.setChecked(checked);
        toggle.setContentDescription(label);
        toggle.setOnCheckedChangeListener(listener);
        row.addView(toggle);
        return row;
    }

    private LinearLayout timeRow(String label, int minute, boolean start) {
        LinearLayout row = Ui.row(this);
        row.setPadding(0, Ui.dp(this, 10), 0, Ui.dp(this, 10));
        row.addView(Ui.body(this, label), Ui.lp(0, Ui.WRAP, 1));
        row.addView(Ui.label(this, String.format(Locale.ROOT, "%02d:%02d", minute / 60, minute % 60), 16,
                Ui.accent(this), true));
        Ui.clickable(row, v -> new TimePickerDialog(this, (picker, h, m) -> {
            change(x -> {
                if (start) x.quietStart = h * 60 + m;
                else x.quietEnd = h * 60 + m;
            });
            render();
        }, minute / 60, minute % 60, true).show());
        return row;
    }

    /** Explains what will be notified, then asks Android for the permission. */
    private void optIn() {
        new AlertDialog.Builder(this)
                .setTitle(R.string.notify_optin_title)
                .setMessage(R.string.notify_optin_body)
                .setNegativeButton(R.string.cancel, (d, w) -> render())
                .setOnCancelListener(d -> render())
                .setPositiveButton(R.string.notify_optin_go, (d, w) -> {
                    Accounts.setNotificationsEnabled(this, true);
                    Notifications.ensureChannels(this);
                    if (Build.VERSION.SDK_INT >= 33 && !Notifications.permitted(this)) {
                        requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, REQUEST_PERMISSION);
                    } else {
                        started();
                    }
                })
                .show();
    }

    private void started() {
        Notifications.schedulePolling(this);
        Notifications.checkSoon(this);
        toast(getString(R.string.notify_on));
        render();
    }

    private void turnOff() {
        Accounts.setNotificationsEnabled(this, false);
        Notifications.cancelPolling(this);
        Notifications.clearAll(this);
        render();
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, @NonNull String[] permissions, @NonNull int[] results) {
        super.onRequestPermissionsResult(requestCode, permissions, results);
        if (requestCode != REQUEST_PERMISSION) return;
        if (results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED) {
            started();
            return;
        }
        Accounts.setNotificationsEnabled(this, false);
        render();
        if (Build.VERSION.SDK_INT >= 33 && !shouldShowRequestPermissionRationale(Manifest.permission.POST_NOTIFICATIONS)) {
            new AlertDialog.Builder(this)
                    .setTitle(R.string.notify_denied_title)
                    .setMessage(R.string.notify_denied_body)
                    .setNegativeButton(R.string.cancel, null)
                    .setPositiveButton(R.string.notify_system_settings, (d, w) -> openSystemSettings())
                    .show();
        }
    }

    private void openSystemSettings() {
        Intent intent = new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName());
        try {
            startActivity(intent);
        } catch (RuntimeException missing) {
            startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                    Uri.fromParts("package", getPackageName(), null)));
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (content != null) render();
    }
}
