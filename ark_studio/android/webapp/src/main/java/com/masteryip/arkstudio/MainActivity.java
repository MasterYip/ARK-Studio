package com.masteryip.arkstudio;

import android.Manifest;
import android.app.AlertDialog;
import android.app.DownloadManager;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.content.res.ColorStateList;
import android.graphics.Bitmap;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.text.InputType;
import android.text.Spannable;
import android.text.SpannableString;
import android.text.style.StyleSpan;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.DownloadListener;
import android.webkit.SslErrorHandler;
import android.webkit.URLUtil;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import androidx.appcompat.app.AppCompatActivity;

import com.google.zxing.integration.android.IntentIntegrator;
import com.google.zxing.integration.android.IntentResult;
import com.masteryip.arkstudio.data.LinkPolicy;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.data.ServerStore;
import com.masteryip.arkstudio.data.ServerUrl;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;

/**
 * Native ARK shell: a saved-server home screen plus a WebView connection screen.
 *
 * <p>Cold start always lands on the native server list. The WebView keeps the
 * ARK mobile pairing token in its own localStorage, so reconnecting to the same
 * origin after an app restart does not need a fresh QR. The hardware back
 * button leaves the WebView for the server list instead of walking browser
 * history or closing the app.</p>
 */
public class MainActivity extends AppCompatActivity {
    private static final String PREFS = "ark";
    private static final String KEY_SERVERS_JSON = "servers_json_v1";
    private static final String KEY_LEGACY_URL = "server_url";
    private static final String SETUP_ACTION = "com.masteryip.arkstudio.SETUP";

    private static final int REQUEST_FILE = 4101;
    private static final int REQUEST_STORAGE = 4103;

    private static final int MATCH = ViewGroup.LayoutParams.MATCH_PARENT;
    private static final int WRAP = ViewGroup.LayoutParams.WRAP_CONTENT;

    private static final int C_BG = Color.rgb(248, 250, 252);
    private static final int C_CARD = Color.WHITE;
    private static final int C_TEXT = Color.rgb(15, 23, 42);
    private static final int C_MUTED = Color.rgb(100, 116, 139);
    private static final int C_FAINT = Color.rgb(148, 163, 184);
    private static final int C_BORDER = Color.rgb(226, 232, 240);
    private static final int C_BLUE = Color.rgb(37, 99, 235);
    private static final int C_BLUE_DARK = Color.rgb(29, 78, 216);
    private static final int C_BLUE_SOFT = Color.rgb(239, 246, 255);
    private static final int C_RED = Color.rgb(220, 38, 38);
    private static final int C_RED_SOFT = Color.rgb(254, 242, 242);
    private static final int C_RED_BORDER = Color.rgb(254, 202, 202);
    private static final int C_SLATE_DOT = Color.rgb(203, 213, 225);

    private FrameLayout root;
    private LinearLayout homeScreen;
    private LinearLayout webScreen;
    private FrameLayout webFrame;
    private View errorOverlay;
    private TextView errorServerView;
    private TextView errorDetailView;
    private ProgressBar progressBar;
    private WebView webView;

    private ServerStore store;
    private ServerEntry activeEntry;
    private String activeLoadUrl;
    private boolean showingWeb;
    private boolean pageHadError;
    private boolean webPaused = true;
    private boolean backArmed = false;
    private long backArmedAt = 0L;
    private boolean backEval = false;

    private ValueCallback<Uri[]> pendingFileCallback;
    private DownloadSpec pendingDownload;

    private final class ThemeBridge {
        @JavascriptInterface
        public void setTheme(String theme, String backgroundColor) {
            runOnUiThread(() -> applyWebTheme(theme, backgroundColor));
        }
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE);

        loadStore();
        root = new FrameLayout(this);
        root.setBackgroundColor(C_BG);
        setContentView(root);
        applyWebTheme("light", "");
        showHome();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        if (SETUP_ACTION.equals(intent.getAction())) {
            showHome();
            return;
        }
        if (intent.getData() != null) {
            addAndConnect("", intent.getData().toString());
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (showingWeb) {
            resumeWeb();
        }
    }

    @Override
    protected void onPause() {
        if (showingWeb) {
            pauseWeb();
        }
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        if (webView != null) {
            webView.stopLoading();
            if (webFrame != null) {
                webFrame.removeView(webView);
            }
            webView.destroy();
            webView = null;
        }
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        if (!showingWeb) {
            super.onBackPressed();
            return;
        }
        if (webView == null) {
            showHome();
            return;
        }
        if (backEval) return;
        backEval = true;
        webView.evaluateJavascript(
                "(window.ARKAndroidBack && window.ARKAndroidBack()) === true",
                value -> {
                    backEval = false;
                    if ("true".equals(value)) {
                        armBack();
                        return;
                    }
                    long now = System.currentTimeMillis();
                    if (backArmed && now - backArmedAt <= 2500) {
                        backArmed = false;
                        showHome();
                        return;
                    }
                    armBack();
                    Toast.makeText(this, R.string.swipe_back_again, Toast.LENGTH_SHORT).show();
                });
    }

    private void armBack() {
        backArmed = true;
        backArmedAt = System.currentTimeMillis();
    }

    // ------------------------------------------------------------------ store

    private SharedPreferences prefs() {
        return getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    private void loadStore() {
        String json = prefs().getString(KEY_SERVERS_JSON, null);
        store = ServerStore.fromJson(json);
        if (json == null) {
            // One-time migration from the previous single-server shell. We keep
            // the entry but never auto-open it on cold start.
            String legacy = prefs().getString(KEY_LEGACY_URL, "");
            String normalized = ServerUrl.normalize(legacy);
            if (normalized != null) {
                ServerStore.AddResult migrated = store.addOrUpdate("", ServerUrl.withoutFragment(normalized));
                if (migrated != null) {
                    store.select(migrated.getEntry().getUrl());
                    saveStore();
                }
            }
        }
    }

    private void saveStore() {
        if (store != null) {
            prefs().edit().putString(KEY_SERVERS_JSON, store.toJson()).apply();
        }
    }

    // ------------------------------------------------------------------- home

    private void showHome() {
        showingWeb = false;
        applyWebTheme("light", "");
        backArmed = false;
        pauseWeb();
        homeScreen = buildHomeScreen();
        root.removeAllViews();
        root.addView(homeScreen, new FrameLayout.LayoutParams(MATCH, MATCH));
    }

    private LinearLayout buildHomeScreen() {
        LinearLayout screen = column();
        screen.setBackgroundColor(C_BG);

        screen.addView(buildBrandHeader());

        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setClipToPadding(false);
        scroll.setPadding(dp(18), dp(2), dp(18), dp(12));

        LinearLayout list = column();
        List<ServerEntry> entries = sortedEntries();
        if (entries.isEmpty()) {
            list.addView(buildEmptyCard());
        } else {
            for (ServerEntry entry : entries) {
                LinearLayout card = buildServerCard(entry);
                LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(MATCH, WRAP);
                params.bottomMargin = dp(10);
                list.addView(card, params);
            }
        }
        scroll.addView(list, new ScrollView.LayoutParams(MATCH, WRAP));
        screen.addView(scroll, new LinearLayout.LayoutParams(MATCH, 0, 1f));

        screen.addView(buildActionPanel());
        return screen;
    }

    private View buildBrandHeader() {
        LinearLayout header = column();
        header.setPadding(dp(22), dp(18), dp(22), dp(12));

        LinearLayout brandRow = row();
        brandRow.setGravity(Gravity.CENTER_VERTICAL);

        ImageView mark = new ImageView(this);
        mark.setImageResource(R.drawable.ic_ark_prism);
        mark.setScaleType(ImageView.ScaleType.CENTER_INSIDE);
        mark.setPadding(dp(9), dp(9), dp(9), dp(9));
        mark.setBackground(rounded(C_CARD, C_BORDER, 14));
        mark.setElevation(dp(1));
        LinearLayout.LayoutParams markParams = new LinearLayout.LayoutParams(dp(46), dp(46));
        markParams.rightMargin = dp(12);
        brandRow.addView(mark, markParams);

        LinearLayout brandText = column();
        TextView title = label(getString(R.string.app_name), 24, C_TEXT, false);
        SpannableString brandName = new SpannableString(getString(R.string.app_name));
        int studioAt = brandName.toString().indexOf(" Studio");
        if (studioAt < 0) studioAt = Math.min(3, brandName.length());
        brandName.setSpan(new StyleSpan(Typeface.BOLD), 0, studioAt,
                Spannable.SPAN_EXCLUSIVE_EXCLUSIVE);
        title.setText(brandName);
        title.setLetterSpacing(0.01f);
        brandText.addView(title);
        TextView eyebrow = label(getString(R.string.app_eyebrow), 9, C_BLUE, true);
        eyebrow.setLetterSpacing(0.18f);
        eyebrow.setPadding(0, dp(3), 0, 0);
        brandText.addView(eyebrow);
        brandRow.addView(brandText, new LinearLayout.LayoutParams(0, WRAP, 1f));
        header.addView(brandRow);

        LinearLayout headingRow = row();
        headingRow.setGravity(Gravity.CENTER_VERTICAL);
        headingRow.setPadding(0, dp(24), 0, 0);
        headingRow.addView(label(getString(R.string.servers_heading), 21, C_TEXT, true),
                new LinearLayout.LayoutParams(0, WRAP, 1f));
        int count = store == null ? 0 : store.size();
        headingRow.addView(pill(getString(R.string.servers_count, count), C_BLUE, C_BLUE_SOFT));
        header.addView(headingRow);

        TextView subtitle = label(getString(R.string.servers_subtitle), 12.5f, C_MUTED, false);
        subtitle.setLineSpacing(dp(2), 1f);
        subtitle.setPadding(0, dp(7), 0, 0);
        header.addView(subtitle);
        return header;
    }

    private View buildEmptyCard() {
        LinearLayout card = column();
        card.setGravity(Gravity.CENTER_HORIZONTAL);
        card.setPadding(dp(24), dp(28), dp(24), dp(28));
        card.setBackground(rounded(C_CARD, C_BORDER, 18));
        card.setElevation(dp(1));

        ImageView mark = new ImageView(this);
        mark.setImageResource(R.drawable.ic_ark_prism);
        mark.setAlpha(0.9f);
        card.addView(mark, new LinearLayout.LayoutParams(dp(52), dp(52)));

        TextView title = label(getString(R.string.no_servers_title), 17, C_TEXT, true);
        title.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams titleParams = new LinearLayout.LayoutParams(WRAP, WRAP);
        titleParams.topMargin = dp(12);
        card.addView(title, titleParams);

        TextView body = label(getString(R.string.no_servers_body), 12.5f, C_MUTED, false);
        body.setGravity(Gravity.CENTER);
        body.setLineSpacing(dp(3), 1f);
        LinearLayout.LayoutParams bodyParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        bodyParams.topMargin = dp(8);
        card.addView(body, bodyParams);

        TextView hint = label(getString(R.string.empty_hint), 11, C_BLUE, true);
        hint.setGravity(Gravity.CENTER);
        hint.setPadding(0, dp(14), 0, 0);
        card.addView(hint);
        return card;
    }

    private LinearLayout buildServerCard(ServerEntry entry) {
        boolean active = store != null && store.isSelected(entry);
        boolean failed = entry.getLastError() != null;

        LinearLayout card = column();
        card.setPadding(dp(16), dp(15), dp(16), dp(14));
        card.setBackground(rounded(C_CARD, failed ? C_RED_BORDER : C_BORDER, 18));
        card.setElevation(dp(1));

        LinearLayout top = row();
        top.setGravity(Gravity.CENTER_VERTICAL);
        View dot = dot(failed ? C_RED : (active ? C_BLUE : C_SLATE_DOT), 8);
        LinearLayout.LayoutParams dotParams = new LinearLayout.LayoutParams(dp(8), dp(8));
        dotParams.rightMargin = dp(8);
        top.addView(dot, dotParams);

        TextView name = label(entry.getName(), 16, C_TEXT, true);
        name.setSingleLine(true);
        name.setEllipsize(android.text.TextUtils.TruncateAt.END);
        top.addView(name, new LinearLayout.LayoutParams(0, WRAP, 1f));

        String chip;
        int chipColor;
        int chipBg;
        if (active) {
            chip = getString(R.string.server_active);
            chipColor = C_BLUE;
            chipBg = C_BLUE_SOFT;
        } else if (failed) {
            chip = getString(R.string.server_unreachable);
            chipColor = C_RED;
            chipBg = C_RED_SOFT;
        } else if (entry.getLastUsedAt() > 0) {
            chip = getString(R.string.server_last_used);
            chipColor = C_MUTED;
            chipBg = Color.rgb(241, 245, 249);
        } else {
            chip = getString(R.string.server_new);
            chipColor = C_MUTED;
            chipBg = Color.rgb(241, 245, 249);
        }
        TextView status = pill(chip, chipColor, chipBg);
        LinearLayout.LayoutParams statusParams = new LinearLayout.LayoutParams(WRAP, WRAP);
        statusParams.leftMargin = dp(8);
        top.addView(status, statusParams);
        card.addView(top);

        TextView url = label(entry.getUrl(), 11.5f, C_MUTED, false);
        url.setTypeface(Typeface.MONOSPACE);
        url.setSingleLine(true);
        url.setEllipsize(android.text.TextUtils.TruncateAt.MIDDLE);
        url.setPadding(0, dp(6), 0, 0);
        card.addView(url);

        String meta = entry.getLastUsedAt() > 0
                ? getString(failed ? R.string.last_attempt : R.string.last_used, ago(entry.getLastUsedAt()))
                : getString(R.string.never_connected);
        TextView metaView = label(meta, 11.5f, C_FAINT, false);
        metaView.setPadding(0, dp(5), 0, 0);
        card.addView(metaView);

        if (failed) {
            LinearLayout errorRow = row();
            errorRow.setGravity(Gravity.CENTER_VERTICAL);
            errorRow.setPadding(dp(9), dp(7), dp(9), dp(7));
            errorRow.setBackground(rounded(C_RED_SOFT, 0, 9));
            LinearLayout.LayoutParams errorParams = new LinearLayout.LayoutParams(MATCH, WRAP);
            errorParams.topMargin = dp(9);
            View errorDot = dot(C_RED, 6);
            LinearLayout.LayoutParams errorDotParams = new LinearLayout.LayoutParams(dp(6), dp(6));
            errorDotParams.rightMargin = dp(7);
            errorRow.addView(errorDot, errorDotParams);
            TextView errorText = label(entry.getLastError(), 11.5f, C_RED, false);
            errorText.setSingleLine(true);
            errorText.setEllipsize(android.text.TextUtils.TruncateAt.END);
            errorRow.addView(errorText, new LinearLayout.LayoutParams(0, WRAP, 1f));
            card.addView(errorRow, errorParams);
        }

        LinearLayout actions = column();
        actions.setPadding(0, dp(12), 0, 0);
        Button connect = button(getString(failed ? R.string.retry : R.string.connect), true, false);
        connect.setOnClickListener(v -> connect(entry));
        actions.addView(connect, new LinearLayout.LayoutParams(MATCH, dp(42)));

        // Keep Reload/Delete on their own equal-width row.  The old
        // Connect/Reload/Delete single row squeezed "Reload" into two lines
        // on narrow launcher screens.
        LinearLayout secondary = row();
        LinearLayout.LayoutParams secondaryParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        secondaryParams.topMargin = dp(8);
        Button reload = button(getString(R.string.reload), false, false);
        reload.setOnClickListener(v -> connect(entry));
        secondary.addView(reload, new LinearLayout.LayoutParams(0, dp(42), 1f));

        Button delete = button(getString(R.string.delete), false, true);
        delete.setOnClickListener(v -> confirmDelete(entry));
        LinearLayout.LayoutParams deleteParams = new LinearLayout.LayoutParams(0, dp(42), 1f);
        deleteParams.leftMargin = dp(8);
        secondary.addView(delete, deleteParams);
        actions.addView(secondary, secondaryParams);
        card.addView(actions);
        return card;
    }

    private View buildActionPanel() {
        LinearLayout panel = column();
        panel.setPadding(dp(18), dp(14), dp(18), dp(16));
        panel.setBackground(topRounded(C_CARD, 20));
        panel.setElevation(dp(8));

        Button scan = button(getString(R.string.add_scan_qr), true, false);
        scan.setOnClickListener(v -> launchScanner());
        panel.addView(scan, new LinearLayout.LayoutParams(MATCH, dp(50)));

        Button manual = button(getString(R.string.add_manual), false, false);
        manual.setOnClickListener(v -> showAddDialog());
        LinearLayout.LayoutParams manualParams = new LinearLayout.LayoutParams(MATCH, dp(46));
        manualParams.topMargin = dp(8);
        panel.addView(manual, manualParams);

        TextView tip = label(getString(R.string.pairing_tip), 10, C_FAINT, false);
        tip.setGravity(Gravity.CENTER);
        tip.setPadding(0, dp(10), 0, 0);
        panel.addView(tip);
        return panel;
    }

    private List<ServerEntry> sortedEntries() {
        List<ServerEntry> entries = new ArrayList<>(store.entries());
        Collections.sort(entries, (first, second) -> {
            boolean firstActive = store.isSelected(first);
            boolean secondActive = store.isSelected(second);
            if (firstActive != secondActive) {
                return firstActive ? -1 : 1;
            }
            int used = Long.compare(second.getLastUsedAt(), first.getLastUsedAt());
            if (used != 0) {
                return used;
            }
            return first.getName().compareToIgnoreCase(second.getName());
        });
        return entries;
    }

    // -------------------------------------------------------------- add/delete

    private void showAddDialog() {
        LinearLayout form = column();
        form.setPadding(dp(22), dp(4), dp(22), 0);

        TextView urlLabel = label(getString(R.string.server_url_label), 11, C_MUTED, true);
        urlLabel.setLetterSpacing(0.04f);
        form.addView(urlLabel);

        EditText urlInput = input(getString(R.string.url_hint),
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        LinearLayout.LayoutParams urlParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        urlParams.topMargin = dp(6);
        form.addView(urlInput, urlParams);

        TextView nameLabel = label(getString(R.string.name_optional), 11, C_MUTED, true);
        nameLabel.setLetterSpacing(0.04f);
        LinearLayout.LayoutParams nameLabelParams = new LinearLayout.LayoutParams(WRAP, WRAP);
        nameLabelParams.topMargin = dp(14);
        form.addView(nameLabel, nameLabelParams);

        EditText nameInput = input(getString(R.string.name_hint), InputType.TYPE_CLASS_TEXT);
        LinearLayout.LayoutParams nameParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        nameParams.topMargin = dp(6);
        form.addView(nameInput, nameParams);

        TextView help = label(getString(R.string.manual_hint), 11, C_FAINT, false);
        help.setLineSpacing(dp(2), 1f);
        help.setPadding(0, dp(12), 0, 0);
        form.addView(help);

        AlertDialog dialog = new AlertDialog.Builder(this)
                .setTitle(R.string.add_server_title)
                .setView(form)
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.save_connect, null)
                .create();
        dialog.setOnShowListener(ignored -> {
            Button positive = dialog.getButton(AlertDialog.BUTTON_POSITIVE);
            positive.setTextColor(C_BLUE);
            positive.setAllCaps(false);
            dialog.getButton(AlertDialog.BUTTON_NEGATIVE).setTextColor(C_MUTED);
            positive.setOnClickListener(v -> {
                String rawUrl = urlInput.getText().toString().trim();
                if (ServerUrl.normalize(rawUrl) == null) {
                    urlInput.setError(getString(R.string.invalid_url));
                    urlInput.requestFocus();
                    return;
                }
                String name = nameInput.getText().toString().trim();
                dialog.dismiss();
                addAndConnect(name, rawUrl);
            });
            urlInput.requestFocus();
            if (dialog.getWindow() != null) {
                dialog.getWindow().setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE);
            }
        });
        dialog.show();
    }

    private void confirmDelete(ServerEntry entry) {
        AlertDialog dialog = new AlertDialog.Builder(this)
                .setTitle(R.string.delete_server_title)
                .setMessage(getString(R.string.delete_server_message, entry.getName()))
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.delete, (ignored, which) -> {
                    if (store.remove(entry.getUrl())) {
                        saveStore();
                        showHome();
                        Toast.makeText(this, R.string.server_deleted, Toast.LENGTH_SHORT).show();
                    }
                })
                .show();
        dialog.getButton(AlertDialog.BUTTON_POSITIVE).setTextColor(C_RED);
    }

    private void launchScanner() {
        IntentIntegrator integrator = new IntentIntegrator(this);
        integrator.setDesiredBarcodeFormats(IntentIntegrator.QR_CODE);
        integrator.setPrompt(getString(R.string.scan_prompt));
        integrator.setBeepEnabled(false);
        integrator.setOrientationLocked(false);
        integrator.initiateScan();
    }

    // -------------------------------------------------------------- connection

    private void connect(ServerEntry entry) {
        ServerEntry selected = store.touch(entry.getUrl(), System.currentTimeMillis());
        if (selected == null) {
            store.select(entry.getUrl());
            selected = entry;
        }
        saveStore();
        showWeb(selected, selected.getUrl());
    }

    /**
     * Persists the base URL (so a one-time QR token is not replayed later) but
     * loads the full pairing URL immediately when one was supplied. The mobile
     * token lives in WebView localStorage and survives restarts.
     */
    private void addAndConnect(String name, String rawUrl) {
        String normalized = ServerUrl.normalize(rawUrl);
        if (normalized == null) {
            Toast.makeText(this, R.string.invalid_url, Toast.LENGTH_LONG).show();
            return;
        }
        String storedUrl = ServerUrl.withoutFragment(normalized);
        ServerStore.AddResult result = store.addOrUpdate(name, storedUrl);
        if (result == null) {
            Toast.makeText(this, R.string.invalid_url, Toast.LENGTH_LONG).show();
            return;
        }
        store.select(result.getEntry().getUrl());
        ServerEntry selected = store.touch(result.getEntry().getUrl(), System.currentTimeMillis());
        if (selected == null) {
            selected = result.getEntry();
        }
        saveStore();
        Toast.makeText(this,
                result.isAdded() ? R.string.server_added : R.string.server_updated,
                Toast.LENGTH_SHORT).show();
        showWeb(selected, normalized);
    }

    private void showWeb(ServerEntry entry, String loadUrl) {
        activeEntry = entry;
        activeLoadUrl = loadUrl == null ? entry.getUrl() : loadUrl;
        ensureWebScreen();
        hideErrorOverlay();
        setProgress(true);
        pageHadError = false;

        root.removeAllViews();
        root.addView(webScreen, new FrameLayout.LayoutParams(MATCH, MATCH));
        showingWeb = true;
        resumeWeb();
        webView.stopLoading();
        webView.loadUrl(activeLoadUrl);
    }

    private void retryActive() {
        if (activeEntry == null) {
            showHome();
            return;
        }
        ServerEntry touched = store.touch(activeEntry.getUrl(), System.currentTimeMillis());
        if (touched != null) {
            activeEntry = touched;
            saveStore();
        }
        hideErrorOverlay();
        setProgress(true);
        pageHadError = false;
        if (webView != null) {
            webView.loadUrl(activeLoadUrl == null ? activeEntry.getUrl() : activeLoadUrl);
        }
    }

    // ---------------------------------------------------------------- web view

    private void applyWebTheme(String theme, String backgroundColor) {
        boolean dark = "dark".equalsIgnoreCase(String.valueOf(theme));
        int color;
        if (backgroundColor != null
                && backgroundColor.matches("(?i)#[0-9a-f]{6}")) {
            color = Color.parseColor(backgroundColor);
        } else {
            color = dark ? Color.rgb(15, 20, 28) : C_BG;
        }
        getWindow().setStatusBarColor(color);
        getWindow().setNavigationBarColor(color);
        int flags = getWindow().getDecorView().getSystemUiVisibility();
        if (dark) {
            flags &= ~View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            flags &= ~View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
        } else {
            flags |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            flags |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
        }
        getWindow().getDecorView().setSystemUiVisibility(flags);
    }

    private void ensureWebScreen() {
        if (webScreen != null) {
            return;
        }
        webScreen = column();
        webScreen.setBackgroundColor(C_BG);

        // No native address bar: the web app owns the full viewport.  Back
        // gestures are handled by onBackPressed() instead.
        webFrame = new FrameLayout(this);
        webFrame.setBackgroundColor(C_BG);
        webScreen.addView(webFrame, new LinearLayout.LayoutParams(MATCH, 0, 1f));

        buildWebView();
        errorOverlay = buildErrorOverlay();
        webFrame.addView(errorOverlay, new FrameLayout.LayoutParams(MATCH, MATCH));
    }

    @android.annotation.SuppressLint("SetJavaScriptEnabled")
    private void buildWebView() {
        webView = new WebView(this);
        webView.setBackgroundColor(C_CARD);
        webView.setLayoutParams(new FrameLayout.LayoutParams(MATCH, MATCH));
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setDatabaseEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(true);
        settings.setMediaPlaybackRequiresUserGesture(false);
        // target=_blank and window.open() reach onCreateWindow (openPopupUrl)
        // instead of replacing the ARK page.
        settings.setSupportMultipleWindows(true);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
        settings.setUserAgentString(settings.getUserAgentString() + " ARKAndroid/1.1");
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            settings.setForceDark(WebSettings.FORCE_DARK_OFF);
        }
        webView.addJavascriptInterface(new ThemeBridge(), "ARKAndroidTheme");
        CookieManager.getInstance().setAcceptCookie(true);
        CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true);
        if (BuildConfig.DEBUG) {
            WebView.setWebContentsDebuggingEnabled(true);
        }

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                // Server redirects (http -> https, sign-in) keep loading here.
                if (request.isRedirect()) {
                    return false;
                }
                return handleUri(request.getUrl(), request.isForMainFrame());
            }

            @Override
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return handleUri(Uri.parse(url), true);
            }

            @Override
            public void onPageStarted(WebView view, String url, Bitmap favicon) {
                pageHadError = false;
                setProgress(true);
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                setProgress(false);
                view.evaluateJavascript(
                        "(function(){try{if(!window.ARKAndroidTheme)return;"
                        + "var d=document.documentElement;"
                        + "var c=getComputedStyle(d).getPropertyValue('--bg').trim();"
                        + "window.ARKAndroidTheme.setTheme(d.dataset.theme||'light',c);"
                        + "}catch(e){}})()", null);
                if (!pageHadError) {
                    if (errorOverlay != null) {
                        hideErrorOverlay();
                    }
                    if (activeEntry != null) {
                        // A one-time #pair= token has been consumed by the web
                        // app; retries and later reloads should use the stable
                        // base URL (the mobile token is kept in localStorage).
                        if (activeLoadUrl != null && activeLoadUrl.contains("#pair=")) {
                            activeLoadUrl = activeEntry.getUrl();
                        }
                        if (store.clearError(activeEntry.getUrl())) {
                            saveStore();
                        }
                    }
                }
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
                if (request == null || !request.isForMainFrame()) {
                    return;
                }
                pageHadError = true;
                String friendly = networkErrorLabel(error);
                CharSequence description = error == null ? null : error.getDescription();
                recordActiveError(friendly, description == null ? "" : description.toString());
            }

            @Override
            public void onReceivedSslError(WebView view, SslErrorHandler handler, android.net.http.SslError error) {
                handler.cancel();
                pageHadError = true;
                recordActiveError(getString(R.string.error_ssl),
                        getString(R.string.error_ssl_detail));
            }
        });

        webView.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onCreateWindow(WebView view, boolean isDialog, boolean isUserGesture,
                                          android.os.Message resultMsg) {
                return ExternalLinks.catchPopup(MainActivity.this, resultMsg, MainActivity.this::openPopupUrl);
            }

            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback,
                                             FileChooserParams params) {
                if (pendingFileCallback != null) {
                    pendingFileCallback.onReceiveValue(null);
                }
                pendingFileCallback = callback;
                try {
                    Intent intent = params.createIntent();
                    if (params.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE) {
                        intent.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
                    }
                    startActivityForResult(Intent.createChooser(intent, getString(R.string.choose_file)),
                            REQUEST_FILE);
                    return true;
                } catch (Exception failed) {
                    pendingFileCallback = null;
                    Toast.makeText(MainActivity.this, R.string.file_chooser_failed, Toast.LENGTH_SHORT).show();
                    return false;
                }
            }
        });

        webView.setDownloadListener(new DownloadListener() {
            @Override
            public void onDownloadStart(String url, String userAgent, String contentDisposition,
                                        String mimeType, long contentLength) {
                DownloadSpec spec = new DownloadSpec(url, userAgent, contentDisposition, mimeType);
                if (needsLegacyStoragePermission()) {
                    pendingDownload = spec;
                    requestPermissions(new String[]{Manifest.permission.WRITE_EXTERNAL_STORAGE},
                            REQUEST_STORAGE);
                    return;
                }
                startDownload(spec);
            }
        });

        webFrame.addView(webView);
    }

    private View buildErrorOverlay() {
        FrameLayout overlay = new FrameLayout(this);
        overlay.setBackgroundColor(C_BG);
        overlay.setVisibility(View.GONE);

        LinearLayout card = column();
        card.setGravity(Gravity.CENTER_HORIZONTAL);
        card.setPadding(dp(24), dp(24), dp(24), dp(22));
        card.setBackground(rounded(C_CARD, C_BORDER, 20));
        card.setElevation(dp(8));

        TextView bang = label("!", 22, C_RED, true);
        bang.setGravity(Gravity.CENTER);
        GradientDrawable circle = new GradientDrawable();
        circle.setShape(GradientDrawable.OVAL);
        circle.setColor(C_RED_SOFT);
        bang.setBackground(circle);
        card.addView(bang, new LinearLayout.LayoutParams(dp(44), dp(44)));

        TextView title = label(getString(R.string.error_title), 18, C_TEXT, true);
        title.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams titleParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        titleParams.topMargin = dp(14);
        card.addView(title, titleParams);

        errorServerView = label("", 12, C_MUTED, false);
        errorServerView.setGravity(Gravity.CENTER);
        errorServerView.setLineSpacing(dp(2), 1f);
        LinearLayout.LayoutParams serverParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        serverParams.topMargin = dp(8);
        card.addView(errorServerView, serverParams);

        errorDetailView = label("", 12.5f, C_MUTED, false);
        errorDetailView.setGravity(Gravity.CENTER);
        errorDetailView.setLineSpacing(dp(3), 1f);
        LinearLayout.LayoutParams detailParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        detailParams.topMargin = dp(12);
        card.addView(errorDetailView, detailParams);

        TextView note = label(getString(R.string.error_note), 10.5f, C_FAINT, false);
        note.setGravity(Gravity.CENTER);
        note.setLineSpacing(dp(2), 1f);
        LinearLayout.LayoutParams noteParams = new LinearLayout.LayoutParams(MATCH, WRAP);
        noteParams.topMargin = dp(12);
        card.addView(note, noteParams);

        Button retry = button(getString(R.string.retry), true, false);
        retry.setOnClickListener(v -> retryActive());
        LinearLayout.LayoutParams retryParams = new LinearLayout.LayoutParams(MATCH, dp(46));
        retryParams.topMargin = dp(18);
        card.addView(retry, retryParams);

        Button servers = button(getString(R.string.server_list), false, false);
        servers.setOnClickListener(v -> showHome());
        LinearLayout.LayoutParams serversParams = new LinearLayout.LayoutParams(MATCH, dp(42));
        serversParams.topMargin = dp(8);
        card.addView(servers, serversParams);

        FrameLayout.LayoutParams cardParams = new FrameLayout.LayoutParams(MATCH, WRAP, Gravity.CENTER);
        cardParams.leftMargin = dp(22);
        cardParams.rightMargin = dp(22);
        overlay.addView(card, cardParams);
        return overlay;
    }

    private void recordActiveError(String friendly, String detail) {
        if (activeEntry != null) {
            ServerEntry updated = store.markError(activeEntry.getUrl(), friendly);
            if (updated != null) {
                activeEntry = updated;
                saveStore();
            }
        }
        showErrorOverlay(friendly, detail);
    }

    private void showErrorOverlay(String friendly, String detail) {
        if (errorOverlay == null) {
            return;
        }
        setProgress(false);
        if (errorServerView != null && activeEntry != null) {
            errorServerView.setText(getString(R.string.error_server,
                    activeEntry.getName(), activeEntry.getUrl()));
        }
        if (errorDetailView != null) {
            String extra = detail == null ? "" : detail.trim();
            String message = friendly;
            if (!extra.isEmpty()
                    && !extra.equalsIgnoreCase(friendly)
                    && !extra.contains("ERR_")) {
                message += "\n" + extra;
            }
            errorDetailView.setText(message);
        }
        errorOverlay.setVisibility(View.VISIBLE);
        if (webView != null) {
            webView.setVisibility(View.INVISIBLE);
        }
    }

    private void hideErrorOverlay() {
        if (errorOverlay != null) {
            errorOverlay.setVisibility(View.GONE);
        }
        if (webView != null) {
            webView.setVisibility(View.VISIBLE);
        }
    }

    private void setProgress(boolean visible) {
        if (progressBar != null) {
            progressBar.setVisibility(visible ? View.VISIBLE : View.GONE);
        }
    }

    private void pauseWeb() {
        if (webView != null && !webPaused) {
            webView.onPause();
            webPaused = true;
        }
    }

    private void resumeWeb() {
        if (webView != null && webPaused) {
            webView.onResume();
            webPaused = false;
        }
    }

    // ------------------------------------------------------------------ links

    /**
     * Pages of the active ARK server stay here; other sites open in the
     * browser, mailto: in a mail app, file: links copy their path and every
     * other scheme is dropped (LinkPolicy).
     */
    private boolean handleUri(Uri uri, boolean mainFrame) {
        if (uri == null) {
            return true;
        }
        String url = uri.toString();
        LinkPolicy.Action action = LinkPolicy.decide(url, mainFrame,
                activeEntry == null ? null : activeEntry.getUrl(),
                webView == null ? null : webView.getUrl());
        return ExternalLinks.handle(this, url, action);
    }

    /** target=_blank and window.open(): ARK pages load here, the rest leaves. */
    private void openPopupUrl(String url) {
        LinkPolicy.Action action = LinkPolicy.decide(url, true,
                activeEntry == null ? null : activeEntry.getUrl(),
                webView == null ? null : webView.getUrl());
        if (!ExternalLinks.handle(this, url, action) && webView != null) {
            webView.loadUrl(url);
        }
    }

    // --------------------------------------------------------------- downloads

    private boolean needsLegacyStoragePermission() {
        if (Build.VERSION.SDK_INT > 28) {
            return false;
        }
        return checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE)
                != PackageManager.PERMISSION_GRANTED;
    }

    private void startDownload(DownloadSpec spec) {
        try {
            String filename = URLUtil.guessFileName(spec.url, spec.contentDisposition, spec.mimeType);
            if (filename == null || filename.trim().isEmpty() || "downloadfile".equals(filename)) {
                filename = "ARK-download-" + System.currentTimeMillis();
            }
            DownloadManager.Request request = new DownloadManager.Request(Uri.parse(spec.url));
            if (spec.mimeType != null && !spec.mimeType.isEmpty()) {
                request.setMimeType(spec.mimeType);
            }
            if (spec.userAgent != null && !spec.userAgent.isEmpty()) {
                request.addRequestHeader("User-Agent", spec.userAgent);
            }
            String cookie = CookieManager.getInstance().getCookie(spec.url);
            if (cookie != null && !cookie.isEmpty()) {
                request.addRequestHeader("Cookie", cookie);
            }
            request.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
            request.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, filename);
            DownloadManager manager = (DownloadManager) getSystemService(Context.DOWNLOAD_SERVICE);
            if (manager == null) {
                throw new IllegalStateException("Download manager unavailable");
            }
            manager.enqueue(request);
            Toast.makeText(this, R.string.download_started, Toast.LENGTH_SHORT).show();
        } catch (Exception failed) {
            Toast.makeText(this, getString(R.string.download_failed, String.valueOf(failed.getMessage())),
                    Toast.LENGTH_LONG).show();
        }
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions, int[] grantResults) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults);
        if (requestCode == REQUEST_STORAGE) {
            DownloadSpec spec = pendingDownload;
            pendingDownload = null;
            if (grantResults.length > 0 && grantResults[0] == PackageManager.PERMISSION_GRANTED) {
                if (spec != null) {
                    startDownload(spec);
                }
            } else {
                Toast.makeText(this, R.string.download_permission, Toast.LENGTH_LONG).show();
            }
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQUEST_FILE) {
            if (pendingFileCallback != null) {
                pendingFileCallback.onReceiveValue(
                        WebChromeClient.FileChooserParams.parseResult(resultCode, data));
                pendingFileCallback = null;
            }
            return;
        }

        IntentResult scan = IntentIntegrator.parseActivityResult(requestCode, resultCode, data);
        if (scan == null) {
            return;
        }
        String contents = scan.getContents();
        if (contents == null || contents.trim().isEmpty()) {
            return;
        }
        String scannedUrl = ServerUrl.extractFromQr(contents);
        if (scannedUrl == null) {
            Toast.makeText(this, R.string.scan_invalid, Toast.LENGTH_LONG).show();
            return;
        }
        addAndConnect("", scannedUrl);
    }

    // ------------------------------------------------------------------- utils

    private static final class DownloadSpec {
        final String url;
        final String userAgent;
        final String contentDisposition;
        final String mimeType;

        DownloadSpec(String url, String userAgent, String contentDisposition, String mimeType) {
            this.url = url;
            this.userAgent = userAgent;
            this.contentDisposition = contentDisposition;
            this.mimeType = mimeType;
        }
    }

    private String networkErrorLabel(WebResourceError error) {
        if (error == null) {
            return getString(R.string.error_generic);
        }
        switch (error.getErrorCode()) {
            case WebViewClient.ERROR_HOST_LOOKUP:
                return getString(R.string.error_host_lookup);
            case WebViewClient.ERROR_CONNECT:
                return getString(R.string.error_connection_refused);
            case WebViewClient.ERROR_TIMEOUT:
                return getString(R.string.error_timeout);
            case WebViewClient.ERROR_IO:
                return getString(R.string.error_io);
            case WebViewClient.ERROR_FAILED_SSL_HANDSHAKE:
                return getString(R.string.error_ssl);
            case WebViewClient.ERROR_BAD_URL:
                return getString(R.string.error_bad_url);
            case WebViewClient.ERROR_UNSUPPORTED_SCHEME:
                return getString(R.string.error_unsupported_scheme);
            default:
                CharSequence description = error.getDescription();
                return description == null || description.length() == 0
                        ? getString(R.string.error_generic)
                        : description.toString();
        }
    }

    private String ago(long timestamp) {
        long delta = Math.max(0L, System.currentTimeMillis() - timestamp);
        if (delta < 60_000L) {
            return getString(R.string.time_just_now);
        }
        long minutes = delta / 60_000L;
        if (minutes < 60) {
            return getString(R.string.time_minutes, minutes);
        }
        long hours = minutes / 60;
        if (hours < 24) {
            return getString(R.string.time_hours, hours);
        }
        long days = hours / 24;
        if (days < 7) {
            return getString(R.string.time_days, days);
        }
        return android.text.format.DateUtils.formatDateTime(this, timestamp,
                android.text.format.DateUtils.FORMAT_SHOW_DATE | android.text.format.DateUtils.FORMAT_NO_YEAR
                        | android.text.format.DateUtils.FORMAT_ABBREV_MONTH);
    }

    private int dp(int value) {
        return Math.round(TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value,
                getResources().getDisplayMetrics()));
    }

    private LinearLayout column() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        return layout;
    }

    private LinearLayout row() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.HORIZONTAL);
        return layout;
    }

    private TextView label(String text, float sizeSp, int color, boolean bold) {
        TextView view = new TextView(this);
        view.setText(text);
        view.setTextSize(sizeSp);
        view.setTextColor(color);
        if (bold) {
            view.setTypeface(Typeface.DEFAULT_BOLD);
        }
        view.setIncludeFontPadding(false);
        return view;
    }

    private TextView pill(String text, int textColor, int backgroundColor) {
        TextView view = label(text, 8.5f, textColor, true);
        view.setLetterSpacing(0.08f);
        view.setGravity(Gravity.CENTER);
        view.setPadding(dp(8), dp(5), dp(8), dp(5));
        view.setBackground(rounded(backgroundColor, 0, 99));
        return view;
    }

    private Button button(String text, boolean primary, boolean danger) {
        Button button = new Button(this);
        button.setText(text);
        button.setAllCaps(false);
        button.setTextSize(14);
        button.setTypeface(Typeface.DEFAULT_BOLD);
        button.setMinWidth(0);
        button.setMinHeight(0);
        button.setPadding(dp(14), 0, dp(14), 0);
        button.setGravity(Gravity.CENTER);
        button.setStateListAnimator(null);
        if (primary) {
            GradientDrawable shape = rounded(C_BLUE, 0, 12);
            button.setTextColor(Color.WHITE);
            button.setBackground(new RippleDrawable(ColorStateList.valueOf(0x33FFFFFF), shape, shape));
        } else if (danger) {
            GradientDrawable shape = rounded(C_CARD, C_RED_BORDER, 12);
            button.setTextColor(C_RED);
            button.setBackground(new RippleDrawable(ColorStateList.valueOf(0x22DC2626), shape, shape));
        } else {
            GradientDrawable shape = rounded(C_CARD, C_BORDER, 12);
            button.setTextColor(C_BLUE);
            button.setBackground(new RippleDrawable(ColorStateList.valueOf(0x222563EB), shape, shape));
        }
        return button;
    }

    private EditText input(String hint, int inputType) {
        EditText input = new EditText(this);
        input.setHint(hint);
        input.setHintTextColor(C_FAINT);
        input.setTextColor(C_TEXT);
        input.setTextSize(14);
        input.setInputType(inputType);
        input.setSingleLine(true);
        input.setBackground(rounded(Color.rgb(248, 250, 252), C_BORDER, 10));
        input.setPadding(dp(12), dp(10), dp(12), dp(10));
        input.setMinHeight(dp(44));
        return input;
    }

    private View dot(int color, int sizeDp) {
        View view = new View(this);
        GradientDrawable shape = new GradientDrawable();
        shape.setShape(GradientDrawable.OVAL);
        shape.setColor(color);
        view.setBackground(shape);
        view.setLayoutParams(new LinearLayout.LayoutParams(dp(sizeDp), dp(sizeDp)));
        return view;
    }

    private GradientDrawable rounded(int fill, int stroke, int radiusDp) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        drawable.setCornerRadius(dp(radiusDp));
        if (stroke != 0) {
            drawable.setStroke(dp(1), stroke);
        }
        return drawable;
    }

    private GradientDrawable topRounded(int fill, int radiusDp) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        float radius = dp(radiusDp);
        drawable.setCornerRadii(new float[]{radius, radius, radius, radius, 0, 0, 0, 0});
        return drawable;
    }
}
