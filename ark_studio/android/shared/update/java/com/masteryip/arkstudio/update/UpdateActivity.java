package com.masteryip.arkstudio.update;

import android.Manifest;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.text.format.DateUtils;
import android.util.TypedValue;
import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.Switch;
import android.widget.TextView;

import androidx.appcompat.app.AppCompatActivity;

import com.masteryip.arkstudio.BuildConfig;
import com.masteryip.arkstudio.R;

import java.io.File;

/**
 * About and updates: the installed version, "Check for updates", the daily
 * automatic check, and "Download and install" when a newer build exists.
 * Shared by both APKs, so it builds its views without the app's UI kit.
 */
public final class UpdateActivity extends AppCompatActivity {
    private TextView status;
    private ProgressBar progress;
    private Button install;
    private Button checkButton;
    private Updater.Check last;
    private boolean busy;
    private boolean resumeInstall;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Updater.ensureChannel(this);
        UpdateWorker.sync(this);
        int pad = dp(18);
        ScrollView scroll = new ScrollView(this);
        LinearLayout page = new LinearLayout(this);
        page.setOrientation(LinearLayout.VERTICAL);
        page.setPadding(pad, pad, pad, pad);
        scroll.addView(page);

        TextView back = text("‹  " + getString(R.string.update_title), 20, true);
        back.setOnClickListener(v -> finish());
        back.setContentDescription(getString(R.string.update_back));
        page.addView(back);
        page.addView(space(14));
        page.addView(text(getString(R.string.app_name), 17, true));
        page.addView(text(getString(R.string.update_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE), 14, false));
        if (!BuildConfig.ARK_RELEASE.isEmpty()) {
            page.addView(text(getString(R.string.update_built_from, BuildConfig.ARK_RELEASE), 14, false));
        }
        String base = UpdateServer.base(this);
        page.addView(text(base == null || base.isEmpty() ? getString(R.string.update_source_github_only)
                : getString(R.string.update_source_line, base), 13, false));
        page.addView(space(16));

        LinearLayout autoRow = new LinearLayout(this);
        autoRow.setOrientation(LinearLayout.HORIZONTAL);
        TextView autoLabel = text(getString(R.string.update_auto), 15, false);
        autoRow.addView(autoLabel, new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1));
        Switch auto = new Switch(this);
        auto.setChecked(Updater.autoCheck(this));
        auto.setContentDescription(getString(R.string.update_auto));
        auto.setOnCheckedChangeListener((v, on) -> {
            Updater.setAutoCheck(this, on);
            if (on && Build.VERSION.SDK_INT >= 33
                    && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 43);
            }
        });
        autoRow.addView(auto);
        page.addView(autoRow);
        page.addView(text(getString(R.string.update_auto_detail), 13, false));
        page.addView(space(16));

        checkButton = new Button(this);
        checkButton.setText(R.string.update_check);
        checkButton.setOnClickListener(v -> runCheck());
        page.addView(checkButton);
        status = text("", 14, false);
        status.setPadding(0, dp(10), 0, dp(10));
        page.addView(status);
        progress = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        progress.setMax(1000);
        progress.setVisibility(View.GONE);
        page.addView(progress);
        install = new Button(this);
        install.setText(R.string.update_install);
        install.setVisibility(View.GONE);
        install.setOnClickListener(v -> startInstall());
        page.addView(install);
        setContentView(scroll);
        long when = Updater.lastCheck(this);
        if (when > 0) status.setText(getString(R.string.update_last_check, DateUtils.getRelativeTimeSpanString(when)));
        runCheck();
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (resumeInstall && Updater.mayInstall(this)) {
            resumeInstall = false;
            startInstall();
        }
    }

    private int dp(int v) {
        return (int) TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, getResources().getDisplayMetrics());
    }

    private TextView text(String s, int sp, boolean bold) {
        TextView t = new TextView(this);
        t.setText(s);
        t.setTextSize(sp);
        if (bold) t.setTypeface(t.getTypeface(), android.graphics.Typeface.BOLD);
        t.setPadding(0, dp(2), 0, dp(2));
        return t;
    }

    private View space(int h) {
        View v = new View(this);
        v.setLayoutParams(new LinearLayout.LayoutParams(1, dp(h)));
        return v;
    }

    private void setBusy(boolean on) {
        busy = on;
        checkButton.setEnabled(!on);
        install.setEnabled(!on);
    }

    private void runCheck() {
        if (busy) return;
        setBusy(true);
        status.setText(R.string.update_checking);
        install.setVisibility(View.GONE);
        new Thread(() -> {
            Updater.Check check = Updater.check(this);
            runOnUiThread(() -> showCheck(check));
        }, "ark-update-check").start();
    }

    private void showCheck(Updater.Check check) {
        if (isFinishing()) return;
        setBusy(false);
        last = check;
        if (check.entry == null) {
            status.setText(check.error.isEmpty() ? getString(R.string.update_none_found)
                    : getString(R.string.update_offline));
            return;
        }
        String from = getString("github".equals(check.entry.source) ? R.string.update_from_github : R.string.update_from_server);
        if (check.decision == UpdateModel.NEWER) {
            status.setText(getString(R.string.update_available, UpdateModel.label(check.entry)) + "\n" + from);
            install.setVisibility(View.VISIBLE);
        } else if (check.decision == UpdateModel.CURRENT) {
            status.setText(getString(R.string.update_latest) + "\n" + from);
        } else {
            status.setText(getString(R.string.update_unknown, UpdateModel.label(check.entry)) + "\n" + from);
            install.setVisibility(View.VISIBLE);
        }
    }

    private void startInstall() {
        if (busy || last == null || last.entry == null) return;
        if (!Updater.mayInstall(this)) {
            new AlertDialog.Builder(this)
                    .setTitle(R.string.update_permission_title)
                    .setMessage(R.string.update_permission_body)
                    .setNegativeButton(android.R.string.cancel, null)
                    .setPositiveButton(R.string.update_permission_go, (d, w) -> {
                        resumeInstall = true;
                        startActivity(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                                Uri.parse("package:" + getPackageName())));
                    })
                    .show();
            return;
        }
        if (last.entry.sha256.isEmpty()) {
            message(getString(R.string.update_failed, getString(R.string.update_no_checksum)));
            return;
        }
        setBusy(true);
        progress.setProgress(0);
        progress.setVisibility(View.VISIBLE);
        Updater.Check check = last;
        new Thread(() -> {
            try {
                File apk = Updater.download(this, check, (done, total) -> runOnUiThread(() -> {
                    if (total > 0) {
                        progress.setProgress((int) (done * 1000 / total));
                        status.setText(getString(R.string.update_downloading, (int) (done * 100 / total)));
                    }
                }));
                runOnUiThread(() -> status.setText(R.string.update_verifying));
                if (!UpdateModel.checksumMatches(check.entry.sha256, Updater.sha256(apk))) {
                    //noinspection ResultOfMethodCallIgnored
                    apk.delete();
                    done(getString(R.string.update_bad_checksum), false);
                    return;
                }
                Updater.Archive archive = Updater.inspect(this, apk);
                int verdict = UpdateModel.checkArchive(getPackageName(), Updater.installedCode(),
                        Updater.installedSigners(this), archive.packageName, archive.versionCode, archive.signers);
                switch (verdict) {
                    case UpdateModel.OK:
                        runOnUiThread(() -> status.setText(R.string.update_installing));
                        Updater.install(this, apk);
                        done(null, false);
                        return;
                    case UpdateModel.SIGNATURE_MISMATCH:
                        done(getString(R.string.update_mismatch_body, archive.versionName), true);
                        return;
                    case UpdateModel.NOT_NEWER:
                        done(getString(R.string.update_not_newer, archive.versionCode, Updater.installedCode()), false);
                        return;
                    case UpdateModel.WRONG_PACKAGE:
                        done(getString(R.string.update_wrong_package, archive.packageName), false);
                        return;
                    default:
                        done(getString(R.string.update_unreadable), false);
                }
            } catch (Exception failed) {
                done(getString(R.string.update_failed, String.valueOf(failed.getMessage())), false);
            }
        }, "ark-update-install").start();
    }

    private void done(String problem, boolean signature) {
        runOnUiThread(() -> {
            if (isFinishing()) return;
            setBusy(false);
            progress.setVisibility(View.GONE);
            if (problem == null) return;
            status.setText(problem);
            if (signature) {
                new AlertDialog.Builder(this)
                        .setTitle(R.string.update_mismatch_title)
                        .setMessage(problem)
                        .setPositiveButton(android.R.string.ok, null)
                        .show();
            }
        });
    }

    private void message(String text) {
        status.setText(text);
    }
}
