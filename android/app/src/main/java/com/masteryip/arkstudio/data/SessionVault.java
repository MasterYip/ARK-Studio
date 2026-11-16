package com.masteryip.arkstudio.data;

import org.json.JSONException;
import org.json.JSONObject;

import java.util.Map;

/**
 * Stores one {@link AccountSession} per saved server.
 *
 * The device token is encrypted with {@link Cipher} (AES-GCM under an
 * AndroidKeyStore key on a device) before it reaches the key-value store;
 * only non-secret metadata is stored in clear. Passwords and one-time
 * pairing codes are never given to this class.
 */
public final class SessionVault {
    /** String persistence (SharedPreferences on Android, a map in tests). */
    public interface KeyValue {
        String get(String key);

        void put(String key, String value);

        void remove(String key);

        Map<String, ?> all();
    }

    /** Symmetric encryption of the token. */
    public interface Cipher {
        String encrypt(String plain) throws Exception;

        String decrypt(String sealed) throws Exception;
    }

    private static final String META = "meta:";
    private static final String TOKEN = "token:";

    private final KeyValue store;
    private final Cipher cipher;

    public SessionVault(KeyValue store, Cipher cipher) {
        this.store = store;
        this.cipher = cipher;
    }

    private static String key(String serverUrl) {
        String identity = ServerUrl.identity(serverUrl);
        return identity == null ? String.valueOf(serverUrl) : identity;
    }

    public void save(AccountSession session) throws Exception {
        if (session == null || !session.isValid()) throw new IllegalArgumentException("invalid session");
        String id = key(session.serverUrl);
        store.put(TOKEN + id, cipher.encrypt(session.token));
        store.put(META + id, session.metadataJson().toString());
    }

    /** The stored session, or {@code null} when absent or the token cannot be decrypted. */
    public AccountSession get(String serverUrl) {
        String id = key(serverUrl);
        String meta = store.get(META + id);
        String sealed = store.get(TOKEN + id);
        if (meta == null || sealed == null) return null;
        try {
            AccountSession session = AccountSession.fromMetadata(new JSONObject(meta), cipher.decrypt(sealed));
            return session.isValid() ? session : null;
        } catch (JSONException broken) {
            return null;
        } catch (Exception keyLost) {
            // A wiped Keystore key (backup restore, lock-screen reset) makes the
            // token unreadable: forget it so the user simply signs in again.
            remove(serverUrl);
            return null;
        }
    }

    public boolean has(String serverUrl) {
        return get(serverUrl) != null;
    }

    public void remove(String serverUrl) {
        String id = key(serverUrl);
        store.remove(TOKEN + id);
        store.remove(META + id);
    }
}
