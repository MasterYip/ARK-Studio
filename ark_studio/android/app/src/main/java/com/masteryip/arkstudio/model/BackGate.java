package com.masteryip.arkstudio.model;

/**
 * "Press back again" as in the former Web APK: the first back at the site's
 * root only arms, a second one within {@link #WINDOW_MS} leaves to the server
 * list. Signing out is never done by back. Pure Java for JVM tests.
 */
public final class BackGate {
    public static final long WINDOW_MS = 2500L;

    private long armedAt = -1L;

    /** Returns {@code true} when this press should leave (second press in time). */
    public boolean press(long now) {
        if (armedAt >= 0 && now - armedAt <= WINDOW_MS && now >= armedAt) {
            armedAt = -1L;
            return true;
        }
        armedAt = now;
        return false;
    }

    /** Something else handled back (a sheet closed): start over. */
    public void reset() {
        armedAt = -1L;
    }
}
