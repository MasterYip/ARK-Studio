package com.masteryip.arkstudio.platform;

import android.content.Context;
import android.content.SharedPreferences;
import android.os.Build;
import android.webkit.CookieManager;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.data.ServerIdentity;
import com.masteryip.arkstudio.data.ServerStore;
import com.masteryip.arkstudio.data.ServerUrl;
import com.masteryip.arkstudio.data.SessionVault;

import java.net.URI;

/**
 * Saved servers plus the signed-in device session of each.
 *
 * The server list keeps the existing {@code ark/servers_json_v1} format; the
 * session token lives in {@link SecureStorage}. The WebView receives the same
 * token as the HttpOnly {@code ark_mobile} cookie for that origin only, so the
 * embedded web app is signed in without a second login and page JavaScript
 * cannot read the token.
 */
public final class Accounts {
    private static final String PREFS = "ark";
    private static final String KEY_SERVERS_JSON = "servers_json_v1";
    private static final String KEY_LEGACY_URL = "server_url";
    private static final String KEY_NOTIFY = "notifications_enabled";
    private static final String KEY_APP_MODE = "app_mode";
    public static final String MODE_WEB = "web";
    public static final String MODE_NATIVE = "native";

    private Accounts() {
    }

    private static SharedPreferences prefs(Context c) {
        return c.getApplicationContext().getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    public static ServerStore servers(Context c) {
        String json = prefs(c).getString(KEY_SERVERS_JSON, null);
        ServerStore store = ServerStore.fromJson(json);
        if (json == null) {
            String legacy = ServerUrl.normalize(prefs(c).getString(KEY_LEGACY_URL, ""));
            if (legacy != null) {
                ServerStore.AddResult migrated = store.addOrUpdate("", ServerUrl.withoutFragment(legacy));
                if (migrated != null) store.select(migrated.getEntry().getUrl());
                save(c, store);
            }
        }
        return store;
    }

    public static void save(Context c, ServerStore store) {
        prefs(c).edit().putString(KEY_SERVERS_JSON, store.toJson()).apply();
    }

    public static AccountSession session(Context c, String serverUrl) {
        return serverUrl == null ? null : SecureStorage.vault(c).get(serverUrl);
    }

    /** Session of the last selected server, or {@code null} when signed out. */
    public static AccountSession active(Context c) {
        ServerEntry selected = servers(c).getLastSelected();
        return selected == null ? null : session(c, selected.getUrl());
    }

    public static ArkClient client(AccountSession session) {
        return new ArkClient(session.serverUrl, session.token);
    }

    /** Persists a fresh sign-in, selects the server and signs the WebView in. */
    public static void signIn(Context c, AccountSession session, String name) throws Exception {
        ServerStore store = servers(c);
        ServerStore.AddResult added = store.addOrUpdate(name, session.serverUrl);
        if (added != null) {
            store.touch(added.getEntry().getUrl());
            store.clearError(added.getEntry().getUrl());
        }
        save(c, store);
        SecureStorage.vault(c).save(session);
        applyWebCookie(session);
    }

    public static void update(Context c, AccountSession session) {
        try {
            SecureStorage.vault(c).save(session);
        } catch (Exception ignored) {
            // keep the previous copy
        }
    }

    public static void select(Context c, String serverUrl) {
        ServerStore store = servers(c);
        store.touch(serverUrl);
        save(c, store);
        AccountSession session = session(c, serverUrl);
        if (session != null) applyWebCookie(session);
    }

    /** Forgets the token locally; the caller revokes it on the server first when reachable. */
    public static void forget(Context c, String serverUrl) {
        AccountSession session = session(c, serverUrl);
        SecureStorage.vault(c).remove(serverUrl);
        if (session != null) clearWebCookie(session.serverUrl);
        if (active(c) == null) Notifications.cancelPolling(c);
    }

    /** Stores a checked server identity with the saved server. */
    public static ServerEntry storeIdentity(Context c, String serverUrl, ServerIdentity identity, boolean tunnel) {
        ServerStore store = servers(c);
        ServerEntry updated = store.setIdentity(serverUrl, identity.serverId, identity.publicKey, tunnel);
        if (updated != null) save(c, store);
        return updated;
    }

    /**
     * Moves a saved server, its device session and the WebView's device cookie
     * to a new address (re-link). The caller has checked the server's
     * signature at {@code newUrl} against the stored key.
     */
    public static ServerEntry relink(Context c, String oldUrl, String newUrl) throws Exception {
        ServerStore store = servers(c);
        ServerEntry moved = store.relink(oldUrl, newUrl);
        if (moved == null) return null;
        SessionVault vault = SecureStorage.vault(c);
        AccountSession session = vault.move(oldUrl, moved.getUrl());
        save(c, store);
        clearWebCookie(oldUrl);
        if (session != null) applyWebCookie(session);
        return moved;
    }

    public static boolean notificationsEnabled(Context c) {
        return prefs(c).getBoolean(KEY_NOTIFY, false);
    }

    public static void setNotificationsEnabled(Context c, boolean enabled) {
        prefs(c).edit().putBoolean(KEY_NOTIFY, enabled).apply();
    }

    /** The single APK opens the complete WebView workspace by default. */
    public static String appMode(Context c) {
        String mode = prefs(c).getString(KEY_APP_MODE, MODE_WEB);
        return MODE_NATIVE.equals(mode) ? MODE_NATIVE : MODE_WEB;
    }

    public static void setAppMode(Context c, String mode) {
        prefs(c).edit().putString(KEY_APP_MODE,
                MODE_NATIVE.equals(mode) ? MODE_NATIVE : MODE_WEB).apply();
    }

    // ---------------------------------------------------------------- cookies

    static String cookiePath(String serverUrl) {
        try {
            String path = new URI(serverUrl).getPath();
            if (path == null || path.isEmpty()) return "/";
            return path.endsWith("/") ? path : path + "/";
        } catch (Exception failed) {
            return "/";
        }
    }

    public static void applyWebCookie(AccountSession session) {
        try {
            CookieManager cookies = CookieManager.getInstance();
            cookies.setAcceptCookie(true);
            StringBuilder value = new StringBuilder("ark_mobile=").append(session.token)
                    .append("; Path=").append(cookiePath(session.serverUrl))
                    .append("; HttpOnly; SameSite=Lax; Max-Age=31536000");
            if (PairingLink.isHttps(session.serverUrl)) value.append("; Secure");
            cookies.setCookie(session.serverUrl, value.toString());
            cookies.flush();
        } catch (RuntimeException noWebView) {
            // WebView provider missing/updating: the native screens still work.
        }
    }

    public static void clearWebCookie(String serverUrl) {
        try {
            CookieManager cookies = CookieManager.getInstance();
            String value = "ark_mobile=; Path=" + cookiePath(serverUrl) + "; Max-Age=0";
            if (PairingLink.isHttps(serverUrl)) value += "; Secure";
            cookies.setCookie(serverUrl, value);
            cookies.flush();
        } catch (RuntimeException noWebView) {
            // nothing to clear
        }
    }

    public static String deviceName() {
        String model = Build.MODEL == null ? "Android" : Build.MODEL;
        String maker = Build.MANUFACTURER == null ? "" : Build.MANUFACTURER;
        return (model.toLowerCase(java.util.Locale.ROOT).startsWith(maker.toLowerCase(java.util.Locale.ROOT)) ? model : maker + " " + model).trim();
    }
}
