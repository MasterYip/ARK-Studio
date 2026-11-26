package com.masteryip.arkstudio.data;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;

/**
 * The id the app sends so ARK counts every sign-in from this phone as one
 * device: SHA-256 of {@code Settings.Secure.ANDROID_ID} (stable per phone,
 * user and signing key) with an app salt, as 64 hex digits. The raw
 * ANDROID_ID never leaves the phone.
 */
public final class DeviceId {
    static final String SALT = "ark-android-device|v1|";
    /** ANDROID_ID of some old emulators and broken builds: not unique. */
    static final String BROKEN_ANDROID_ID = "9774d56d682e549c";

    private DeviceId() {
    }

    /** Whether an ANDROID_ID can identify the phone. */
    public static boolean usable(String androidId) {
        return androidId != null && !androidId.trim().isEmpty()
                && !BROKEN_ANDROID_ID.equalsIgnoreCase(androidId.trim());
    }

    public static String hash(String raw) {
        try {
            byte[] sum = MessageDigest.getInstance("SHA-256")
                    .digest((SALT + (raw == null ? "" : raw.trim())).getBytes(StandardCharsets.UTF_8));
            StringBuilder out = new StringBuilder(sum.length * 2);
            for (byte b : sum) out.append(String.format("%02x", b & 0xff));
            return out.toString();
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException(impossible);
        }
    }
}
