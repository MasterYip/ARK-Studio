package com.masteryip.arkstudio;

import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import androidx.appcompat.app.AppCompatActivity;

import com.google.zxing.integration.android.IntentIntegrator;
import com.google.zxing.integration.android.IntentResult;
import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.data.ServerStore;
import com.masteryip.arkstudio.data.ServerUrl;
import com.masteryip.arkstudio.data.SessionStatus;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.platform.Notifications;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;

/**
 * Launcher: saved servers, the two sign-in paths and the app's settings.
 *
 * Like the former Web APK, every saved server shows its address and the
 * signed-in account, can be refreshed, and opens with its saved session (no new
 * sign-in). Sign-out and removing a server are explicit actions in the server's
 * menu; back from the site only returns here.
 *
 * <ul>
 *   <li>Scan QR: the desktop pairing URL carries {@code #pair=<code>}; the code is
 *   claimed once via {@code POST /api/mobile/pair/claim} and never stored.</li>
 *   <li>Account: server URL + username + password via {@code POST /api/mobile/login};
 *   the password is sent once and dropped, only the device token is kept.</li>
 * </ul>
 */
public class MainActivity extends AppCompatActivity {
    public static final String EXTRA_MESSAGE = "message";
    public static final String ACTION_SETUP = "com.masteryip.arkstudio.SETUP";
    static final String ACTION_DEBUG_PAIR = "com.masteryip.arkstudio.DEBUG_PAIR";

    private LinearLayout list;
    private LinearLayout settings;
    private boolean busy;
    private boolean refreshedOnOpen;
    private final Set<String> refreshing = new HashSet<>();

    /** The server list, shown even when a server is signed in (back from the site). */
    public static Intent serverList(Context c) {
        return new Intent(c, MainActivity.class).setAction(ACTION_SETUP)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TASK);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Notifications.sync(this);
        Notifications.checkSoon(this);
        com.masteryip.arkstudio.update.UpdateWorker.sync(this);
        String message = getIntent().getStringExtra(EXTRA_MESSAGE);
        if (message != null) Toast.makeText(this, message, Toast.LENGTH_LONG).show();
        boolean setup = ACTION_SETUP.equals(getIntent().getAction()) || message != null
                || ACTION_DEBUG_PAIR.equals(getIntent().getAction());
        if (!setup && savedInstanceState == null && Accounts.active(this) != null) {
            openPreferred();
            return;
        }
        build();
        handleDebugPairing(getIntent());
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (list != null) render();
    }

    private void build() {
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setBackgroundColor(Ui.bg(this));
        LinearLayout page = Ui.column(this);
        page.setPadding(Ui.dp(this, 18), Ui.dp(this, 24), Ui.dp(this, 18), Ui.dp(this, 24));
        scroll.addView(page);

        LinearLayout brand = Ui.row(this);
        ImageView logo = new ImageView(this);
        logo.setImageResource(R.drawable.ic_ark_prism);
        brand.addView(logo, Ui.lp(Ui.dp(this, 40), Ui.dp(this, 40)));
        LinearLayout names = Ui.column(this);
        names.setPadding(Ui.dp(this, 12), 0, 0, 0);
        names.addView(Ui.label(this, getString(R.string.app_name), 22, Ui.text(this), true));
        names.addView(Ui.small(this, getString(R.string.servers_subtitle)));
        brand.addView(names, Ui.lp(0, Ui.WRAP, 1));
        page.addView(brand);

        page.addView(Ui.heading(this, getString(R.string.servers_heading)));
        list = Ui.column(this);
        page.addView(list);

        page.addView(Ui.space(this, 12));
        Button scan = Ui.button(this, getString(R.string.sign_in_scan), true, false);
        scan.setOnClickListener(v -> launchScanner());
        page.addView(scan, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
        Button account = Ui.button(this, getString(R.string.sign_in_account), false, false);
        account.setOnClickListener(v -> showSignIn(null, null));
        page.addView(account, Ui.margins(this, Ui.MATCH, Ui.WRAP, 0, 4, 0, 4));
        TextView tip = Ui.small(this, getString(R.string.pairing_tip));
        tip.setPadding(0, Ui.dp(this, 10), 0, 0);
        page.addView(tip);

        page.addView(Ui.heading(this, getString(R.string.app_settings)));
        settings = Ui.column(this);
        page.addView(settings);
        setContentView(scroll);
        render();
        if (!refreshedOnOpen) {
            refreshedOnOpen = true;
            for (ServerEntry entry : Accounts.servers(this).entries()) {
                if (Accounts.session(this, entry.getUrl()) != null) refresh(entry, false);
            }
        }
    }

    // ------------------------------------------------------------- settings

    /** The native app's settings (also under More in native mode). */
    private void renderSettings() {
        if (settings == null) return;
        settings.removeAllViews();
        LinearLayout card = Ui.card(this);
        settingRow(card, getString(R.string.app_mode_label), getString(Accounts.MODE_NATIVE.equals(Accounts.appMode(this))
                ? R.string.app_mode_native : R.string.app_mode_web), v -> chooseAppMode());
        Locale locale = getResources().getConfiguration().getLocales().get(0);
        settingRow(card, getString(R.string.language), locale.getDisplayLanguage(locale), v -> openLanguageSettings());
        boolean on = Accounts.notificationsEnabled(this) && Notifications.permitted(this);
        settingRow(card, getString(R.string.notify_settings), getString(on ? R.string.notify_state_on : R.string.notify_state_off),
                v -> startActivity(NotifySettingsActivity.intent(this)));
        settingRow(card, getString(R.string.update_title), getString(R.string.version_short, BuildConfig.VERSION_NAME),
                v -> startActivity(new Intent(this, com.masteryip.arkstudio.update.UpdateActivity.class)));
        settings.addView(card);
        TextView version = Ui.small(this, getString(R.string.version_line, BuildConfig.VERSION_NAME,
                BuildConfig.VERSION_CODE));
        version.setPadding(0, Ui.dp(this, 10), 0, 0);
        settings.addView(version);
    }

    private void settingRow(LinearLayout card, String label, String value, View.OnClickListener open) {
        if (card.getChildCount() > 0) card.addView(Ui.divider(this));
        LinearLayout row = Ui.row(this);
        row.setPadding(0, Ui.dp(this, 10), 0, Ui.dp(this, 10));
        row.addView(Ui.body(this, label), Ui.lp(0, Ui.WRAP, 1));
        row.addView(Ui.small(this, value));
        row.addView(Ui.label(this, "  ›", 20, Ui.faint(this), false));
        Ui.clickable(row, open);
        card.addView(row);
    }

    private void chooseAppMode() {
        String[] labels = {getString(R.string.app_mode_web), getString(R.string.app_mode_native)};
        int checked = Accounts.MODE_NATIVE.equals(Accounts.appMode(this)) ? 1 : 0;
        new AlertDialog.Builder(this)
                .setTitle(R.string.app_mode_label)
                .setSingleChoiceItems(labels, checked, (dialog, which) -> {
                    Accounts.setAppMode(this, which == 1 ? Accounts.MODE_NATIVE : Accounts.MODE_WEB);
                    dialog.dismiss();
                    renderSettings();
                })
                .setNegativeButton(R.string.cancel, null)
                .show();
    }

    /** Android 13+: the per-app language page; older versions: the system language list. */
    private void openLanguageSettings() {
        Intent intent;
        if (Build.VERSION.SDK_INT >= 33) {
            intent = new Intent(Settings.ACTION_APP_LOCALE_SETTINGS)
                    .setData(android.net.Uri.fromParts("package", getPackageName(), null));
        } else {
            intent = new Intent(Settings.ACTION_LOCALE_SETTINGS);
        }
        try {
            startActivity(intent);
        } catch (RuntimeException missing) {
            startActivity(new Intent(Settings.ACTION_SETTINGS));
        }
    }

    private void render() {
        renderSettings();
        list.removeAllViews();
        ServerStore store = Accounts.servers(this);
        List<ServerEntry> entries = new ArrayList<>(store.entries());
        entries.sort((a, b) -> Long.compare(b.getLastUsedAt(), a.getLastUsedAt()));
        if (entries.isEmpty()) {
            LinearLayout empty = Ui.card(this);
            empty.addView(Ui.label(this, getString(R.string.no_servers_title), 16, Ui.text(this), true));
            empty.addView(Ui.small(this, getString(R.string.no_servers_body)));
            list.addView(empty);
            return;
        }
        for (ServerEntry entry : entries) list.addView(card(store, entry));
    }

    private View card(ServerStore store, ServerEntry entry) {
        AccountSession session = Accounts.session(this, entry.getUrl());
        LinearLayout card = Ui.card(this);
        LinearLayout top = Ui.row(this);
        String name = entry.getName().isEmpty() ? ServerUrl.defaultName(entry.getUrl()) : entry.getName();
        top.addView(Ui.oneLine(Ui.label(this, name, 16, Ui.text(this), true)), Ui.lp(0, Ui.WRAP, 1));
        TextView more = Ui.label(this, "⋯", 20, Ui.muted(this), true);
        more.setPadding(Ui.dp(this, 10), 0, Ui.dp(this, 4), 0);
        more.setContentDescription(getString(R.string.server_options));
        Ui.clickable(more, v -> options(entry, session));
        top.addView(more);
        card.addView(top);
        card.addView(Ui.oneLine(Ui.small(this, entry.getUrl())));

        LinearLayout chips = Ui.row(this);
        chips.setPadding(0, Ui.dp(this, 6), 0, 0);
        if (store.isSelected(entry) && session != null) {
            chips.addView(Ui.chip(this, getString(R.string.server_active), Ui.accent(this), Ui.accentSoft(this)));
        }
        if (session != null) {
            chips.addView(Ui.chip(this, getString(session.isReadOnly() ? R.string.perm_read_only : R.string.perm_full),
                    session.isReadOnly() ? Ui.warn(this) : Ui.ok(this),
                    session.isReadOnly() ? Ui.warnSoft(this) : Ui.okSoft(this)));
        }
        if (PairingLink.isInsecurePublic(entry.getUrl())) {
            chips.addView(Ui.chip(this, getString(R.string.chip_unencrypted), Ui.danger(this), Ui.dangerSoft(this)));
        }
        if (entry.getLastError() != null) {
            chips.addView(Ui.chip(this, getString(R.string.server_unreachable), Ui.danger(this), Ui.dangerSoft(this)));
        }
        if (chips.getChildCount() > 0) card.addView(chips);

        TextView who = Ui.small(this, session == null ? getString(R.string.not_signed_in)
                : session.projectName.isEmpty() ? getString(R.string.signed_in_user, displayUser(session))
                : getString(R.string.signed_in_as, displayUser(session), session.projectName,
                getString(AccountSession.SOURCE_PASSWORD.equals(session.source)
                        ? R.string.via_password : R.string.via_qr)));
        who.setPadding(0, Ui.dp(this, 6), 0, 0);
        card.addView(who);
        if (entry.getLastError() != null) {
            TextView error = Ui.oneLine(Ui.small(this, entry.getLastError()));
            error.setTextColor(Ui.danger(this));
            card.addView(error);
        }

        LinearLayout buttons = Ui.row(this);
        buttons.setPadding(0, Ui.dp(this, 10), 0, 0);
        Button open = Ui.button(this, getString(session != null ? R.string.open : R.string.sign_in), true, false);
        open.setOnClickListener(v -> openEntry(entry, session));
        buttons.addView(open, Ui.lp(0, Ui.WRAP, 1));
        if (session != null) {
            boolean busyHere = refreshing.contains(entry.getUrl());
            Button refresh = Ui.button(this, getString(busyHere ? R.string.refreshing : R.string.refresh), false, false);
            refresh.setEnabled(!busyHere);
            refresh.setOnClickListener(v -> refresh(entry, true));
            LinearLayout.LayoutParams lp = Ui.lp(0, Ui.WRAP, 1);
            lp.leftMargin = Ui.dp(this, 8);
            buttons.addView(refresh, lp);
        }
        card.addView(buttons);
        Ui.clickable(card, v -> openEntry(entry, session));
        return card;
    }

    /** Opens a server with its saved session (cookies included), or asks to sign in. */
    private void openEntry(ServerEntry entry, AccountSession session) {
        if (session != null) {
            Accounts.select(this, entry.getUrl());
            openPreferred();
        } else {
            showSignIn(entry.getUrl(), null);
        }
    }

    /**
     * Asks the server who this phone is signed in as ({@code GET /api/mobile/session})
     * and updates the card: account, project, reachability. A token the server
     * revoked is dropped (it no longer signs anything in); the server stays.
     */
    private void refresh(ServerEntry entry, boolean user) {
        AccountSession session = Accounts.session(this, entry.getUrl());
        if (session == null || !refreshing.add(entry.getUrl())) return;
        if (list != null) render();
        Async.run(this, () -> Accounts.client(session).withTimeouts(6000, 10000).get("/api/mobile/session"),
                status -> {
                    refreshing.remove(entry.getUrl());
                    ServerStore store = Accounts.servers(this);
                    store.clearError(entry.getUrl());
                    Accounts.save(this, store);
                    if (!SessionStatus.signedIn(status)) {
                        Accounts.forget(this, entry.getUrl());
                        Toast.makeText(this, R.string.signed_out_revoked, Toast.LENGTH_LONG).show();
                    } else {
                        Accounts.update(this, SessionStatus.apply(session, status));
                        if (user) Toast.makeText(this, R.string.server_refreshed, Toast.LENGTH_SHORT).show();
                    }
                    if (list != null) render();
                },
                error -> {
                    refreshing.remove(entry.getUrl());
                    if (error.isSignedOut()) {
                        Accounts.forget(this, entry.getUrl());
                        Toast.makeText(this, R.string.signed_out_revoked, Toast.LENGTH_LONG).show();
                    } else {
                        ServerStore store = Accounts.servers(this);
                        store.markError(entry.getUrl(), error.isNetwork() ? getString(R.string.state_offline)
                                : error.getMessage());
                        Accounts.save(this, store);
                    }
                    if (list != null) render();
                });
    }

    static String displayUser(AccountSession session) {
        return session.user.isEmpty() ? "—" : session.user;
    }

    private void options(ServerEntry entry, AccountSession session) {
        List<String> labels = new ArrayList<>();
        List<Runnable> handlers = new ArrayList<>();
        if (session != null) {
            labels.add(getString(R.string.open));
            handlers.add(() -> openEntry(entry, session));
            labels.add(getString(R.string.refresh));
            handlers.add(() -> refresh(entry, true));
            labels.add(getString(R.string.sign_out));
            handlers.add(() -> signOut(session));
        } else {
            labels.add(getString(R.string.sign_in_account));
            handlers.add(() -> showSignIn(entry.getUrl(), null));
        }
        labels.add(getString(R.string.delete));
        handlers.add(() -> confirmDelete(entry, session));
        new AlertDialog.Builder(this)
                .setTitle(entry.getName().isEmpty() ? ServerUrl.displayHost(entry.getUrl()) : entry.getName())
                .setItems(labels.toArray(new String[0]), (d, which) -> handlers.get(which).run())
                .show();
    }

    private void confirmDelete(ServerEntry entry, AccountSession session) {
        new AlertDialog.Builder(this)
                .setTitle(R.string.delete_server_title)
                .setMessage(getString(R.string.delete_server_message, entry.getUrl()))
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.delete, (d, w) -> {
                    if (session != null) revokeQuietly(session);
                    Accounts.forget(this, entry.getUrl());
                    ServerStore store = Accounts.servers(this);
                    store.remove(entry.getUrl());
                    Accounts.save(this, store);
                    render();
                })
                .show();
    }

    /** Sign-out revokes the device token on the server ({@code DELETE /api/mobile/session}). */
    private void signOut(AccountSession session) {
        Async.run(this, () -> Accounts.client(session).withTimeouts(5000, 8000).delete("/api/mobile/session"),
                ok -> {
                    Accounts.forget(this, session.serverUrl);
                    Toast.makeText(this, R.string.signed_out, Toast.LENGTH_SHORT).show();
                    render();
                },
                error -> {
                    Accounts.forget(this, session.serverUrl);
                    Toast.makeText(this, error.isNetwork() ? R.string.signed_out_offline : R.string.signed_out,
                            Toast.LENGTH_LONG).show();
                    render();
                });
    }

    private void revokeQuietly(AccountSession session) {
        Async.run(this, () -> Accounts.client(session).withTimeouts(5000, 8000).delete("/api/mobile/session"),
                null, null);
    }

    // ------------------------------------------------------------- sign-in

    private void showSignIn(String serverUrl, String error) {
        LinearLayout form = Ui.column(this);
        form.setPadding(Ui.dp(this, 20), Ui.dp(this, 8), Ui.dp(this, 20), 0);
        form.addView(Ui.small(this, getString(R.string.server_url_label)));
        EditText url = Ui.input(this, getString(R.string.url_hint), InputType.TYPE_CLASS_TEXT
                | InputType.TYPE_TEXT_VARIATION_URI);
        if (serverUrl != null) url.setText(serverUrl);
        form.addView(url);
        form.addView(Ui.small(this, getString(R.string.username)));
        EditText user = Ui.input(this, getString(R.string.username), InputType.TYPE_CLASS_TEXT
                | InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
        AccountSession known = serverUrl == null ? null : Accounts.session(this, serverUrl);
        if (known != null) user.setText(known.user);
        form.addView(user);
        form.addView(Ui.small(this, getString(R.string.password)));
        EditText password = Ui.input(this, getString(R.string.password), InputType.TYPE_CLASS_TEXT
                | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        password.setImportantForAutofill(View.IMPORTANT_FOR_AUTOFILL_YES);
        form.addView(password);
        CheckBox readOnly = new CheckBox(this);
        readOnly.setText(R.string.read_only_device);
        readOnly.setTextColor(Ui.text(this));
        form.addView(readOnly);
        TextView note = Ui.small(this, error != null ? error : getString(R.string.password_never_stored));
        if (error != null) note.setTextColor(Ui.danger(this));
        note.setPadding(0, Ui.dp(this, 6), 0, Ui.dp(this, 6));
        form.addView(note);
        ScrollView holder = new ScrollView(this);
        holder.addView(form);

        AlertDialog dialog = new AlertDialog.Builder(this)
                .setTitle(R.string.sign_in_account)
                .setView(holder)
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.sign_in, null)
                .create();
        dialog.setOnShowListener(d -> dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v -> {
            String base = ServerUrl.withoutFragment(ServerUrl.normalize(url.getText().toString()));
            if (base == null) {
                url.setError(getString(R.string.invalid_url));
                return;
            }
            if (user.getText().toString().trim().isEmpty()) {
                user.setError(getString(R.string.required));
                return;
            }
            String secret = password.getText().toString();
            password.setText("");
            dialog.dismiss();
            String finalBase = ArkClient.normalizeBase(base);
            Runnable go = () -> passwordLogin(finalBase, user.getText().toString().trim(), secret,
                    readOnly.isChecked());
            if (PairingLink.isInsecurePublic(finalBase)) confirmCleartext(finalBase, go);
            else go.run();
        }));
        dialog.show();
    }

    private void confirmCleartext(String base, Runnable go) {
        new AlertDialog.Builder(this)
                .setTitle(R.string.cleartext_title)
                .setMessage(getString(R.string.cleartext_message, ServerUrl.displayHost(base)))
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.cleartext_continue, (d, w) -> go.run())
                .show();
    }

    private void passwordLogin(String base, String username, String password, boolean readOnly) {
        if (busy) return;
        busy = true;
        Toast.makeText(this, R.string.signing_in, Toast.LENGTH_SHORT).show();
        Async.run(this, () -> ArkClient.login(base, username, password, readOnly, Accounts.deviceName()),
                response -> {
                    busy = false;
                    finishSignIn(base, response, AccountSession.SOURCE_PASSWORD);
                },
                error -> {
                    busy = false;
                    showSignIn(base, signInError(error));
                });
    }

    private String signInError(ApiException error) {
        if (error.isNetwork()) return getString(R.string.sign_in_unreachable);
        switch (error.getCode()) {
            case "AUTH_INVALID": return getString(R.string.sign_in_invalid);
            case "AUTH_RATE_LIMITED": return getString(R.string.sign_in_rate_limited,
                    Math.max(1, error.getRetryAfter()));
            case "AUTH_NOT_CONFIGURED": return getString(R.string.sign_in_no_accounts);
            case "PAIRING_USED": return getString(R.string.pair_used);
            case "PAIRING_EXPIRED": case "PAIRING_INVALID": return getString(R.string.pair_expired);
            case "HTTP_404": return getString(R.string.sign_in_old_server);
            default: return error.getMessage();
        }
    }

    private void finishSignIn(String base, JSONObject response, String source) {
        AccountSession session = AccountSession.fromLogin(base, response, source, System.currentTimeMillis());
        if (!session.isValid()) {
            Toast.makeText(this, R.string.sign_in_bad_response, Toast.LENGTH_LONG).show();
            return;
        }
        try {
            Accounts.signIn(this, session, "");
        } catch (Exception failed) {
            Toast.makeText(this, getString(R.string.secure_store_failed, failed.getMessage()),
                    Toast.LENGTH_LONG).show();
            return;
        }
        Toast.makeText(this, getString(R.string.signed_in_toast, displayUser(session),
                getString(session.isReadOnly() ? R.string.perm_read_only : R.string.perm_full)),
                Toast.LENGTH_SHORT).show();
        Notifications.sync(this);
        openPreferred();
    }

    private void openShell() {
        startActivity(ShellActivity.intent(this, ShellActivity.TAB_HOME));
        finish();
    }

    private void openPreferred() {
        if (Accounts.MODE_NATIVE.equals(Accounts.appMode(this))) {
            openShell();
            return;
        }
        startActivity(WebActivity.intent(this, com.masteryip.arkstudio.model.WebRoutes.HOME));
        finish();
    }

    // ------------------------------------------------------------------ QR

    private void launchScanner() {
        IntentIntegrator integrator = new IntentIntegrator(this);
        integrator.setDesiredBarcodeFormats(IntentIntegrator.QR_CODE);
        integrator.setPrompt(getString(R.string.scan_prompt));
        integrator.setBeepEnabled(false);
        integrator.setOrientationLocked(false);
        integrator.initiateScan();
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        IntentResult scan = IntentIntegrator.parseActivityResult(requestCode, resultCode, data);
        if (scan == null || scan.getContents() == null) return;
        handleScan(scan.getContents());
    }

    /** Also used by instrumentation/manual testing with a pasted pairing URL. */
    void handleScan(String contents) {
        PairingLink link = PairingLink.parse(contents);
        if (link == null) {
            Toast.makeText(this, R.string.scan_invalid, Toast.LENGTH_LONG).show();
            return;
        }
        if (!link.hasCode()) {
            // A plain server URL: continue with the account form.
            showSignIn(link.getBaseUrl(), null);
            return;
        }
        if (busy) return;
        busy = true;
        Toast.makeText(this, R.string.pairing, Toast.LENGTH_SHORT).show();
        String code = link.getCode();
        Async.run(this, () -> ArkClient.claim(link.getBaseUrl(), code),
                response -> {
                    busy = false;
                    finishSignIn(link.getBaseUrl(), response, AccountSession.SOURCE_QR);
                },
                error -> {
                    busy = false;
                    new AlertDialog.Builder(this)
                            .setTitle(R.string.pair_failed)
                            .setMessage(signInError(error))
                            .setPositiveButton(android.R.string.ok, null)
                            .show();
                });
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        if (list == null) build();
        handleDebugPairing(intent);
    }

    /**
     * Debug builds only: {@code am start -a com.masteryip.arkstudio.DEBUG_PAIR --es url <pairing URL>}
     * feeds a pairing URL through the same path as the camera (emulators have no
     * camera pointed at a screen). Release builds ignore it so other apps cannot
     * push a pairing into the launcher.
     */
    private void handleDebugPairing(Intent intent) {
        if (!BuildConfig.DEBUG || intent == null || !ACTION_DEBUG_PAIR.equals(intent.getAction())) return;
        String url = intent.getStringExtra("url");
        if (url != null) handleScan(url);
    }
}
