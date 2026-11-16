package com.masteryip.arkstudio.platform;

import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import com.masteryip.arkstudio.data.SessionVault;

import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.util.Map;

import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * Android Keystore backed storage for device tokens.
 *
 * A non-exportable AES-256-GCM key lives in {@code AndroidKeyStore}; tokens are
 * sealed with a random 12-byte IV and only the ciphertext is written to the
 * private {@code ark_secure} SharedPreferences file (excluded from backups by
 * {@code allowBackup=false}). This is the same scheme Jetpack's
 * EncryptedSharedPreferences uses, without the Tink dependency.
 */
public final class SecureStorage {
    private static final String ALIAS = "ark_device_token_v1";
    private static final String PREFS = "ark_secure";
    private static SessionVault vault;

    private SecureStorage() {
    }

    public static synchronized SessionVault vault(Context context) {
        if (vault == null) {
            SharedPreferences prefs = context.getApplicationContext()
                    .getSharedPreferences(PREFS, Context.MODE_PRIVATE);
            vault = new SessionVault(new PrefsKeyValue(prefs), new KeystoreCipher());
        }
        return vault;
    }

    static final class PrefsKeyValue implements SessionVault.KeyValue {
        private final SharedPreferences prefs;

        PrefsKeyValue(SharedPreferences prefs) {
            this.prefs = prefs;
        }

        @Override
        public String get(String key) {
            return prefs.getString(key, null);
        }

        @Override
        public void put(String key, String value) {
            prefs.edit().putString(key, value).apply();
        }

        @Override
        public void remove(String key) {
            prefs.edit().remove(key).apply();
        }

        @Override
        public Map<String, ?> all() {
            return prefs.getAll();
        }
    }

    static final class KeystoreCipher implements SessionVault.Cipher {
        private static final String TRANSFORM = "AES/GCM/NoPadding";

        private SecretKey key() throws Exception {
            KeyStore store = KeyStore.getInstance("AndroidKeyStore");
            store.load(null);
            KeyStore.Entry entry = store.getEntry(ALIAS, null);
            if (entry instanceof KeyStore.SecretKeyEntry) {
                return ((KeyStore.SecretKeyEntry) entry).getSecretKey();
            }
            KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(new KeyGenParameterSpec.Builder(ALIAS,
                    KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build());
            return generator.generateKey();
        }

        @Override
        public String encrypt(String plain) throws Exception {
            javax.crypto.Cipher cipher = javax.crypto.Cipher.getInstance(TRANSFORM);
            cipher.init(javax.crypto.Cipher.ENCRYPT_MODE, key());
            byte[] iv = cipher.getIV();
            byte[] sealed = cipher.doFinal(plain.getBytes(StandardCharsets.UTF_8));
            byte[] out = new byte[iv.length + sealed.length];
            System.arraycopy(iv, 0, out, 0, iv.length);
            System.arraycopy(sealed, 0, out, iv.length, sealed.length);
            return "v1:" + Base64.encodeToString(out, Base64.NO_WRAP);
        }

        @Override
        public String decrypt(String sealed) throws Exception {
            if (sealed == null || !sealed.startsWith("v1:")) throw new IllegalArgumentException("format");
            byte[] raw = Base64.decode(sealed.substring(3), Base64.NO_WRAP);
            javax.crypto.Cipher cipher = javax.crypto.Cipher.getInstance(TRANSFORM);
            cipher.init(javax.crypto.Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, raw, 0, 12));
            byte[] plain = cipher.doFinal(raw, 12, raw.length - 12);
            return new String(plain, StandardCharsets.UTF_8);
        }
    }
}
