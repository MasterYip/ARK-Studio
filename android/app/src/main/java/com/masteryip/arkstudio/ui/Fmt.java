package com.masteryip.arkstudio.ui;

import android.content.Context;

import com.masteryip.arkstudio.R;

import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.format.DateTimeParseException;

/** Small display formatters. */
public final class Fmt {
    private Fmt() {
    }

    public static long parseIso(String iso) {
        if (iso == null || iso.isEmpty()) return 0;
        try {
            return OffsetDateTime.parse(iso).toInstant().toEpochMilli();
        } catch (DateTimeParseException notOffset) {
            try {
                return Instant.parse(iso).toEpochMilli();
            } catch (DateTimeParseException failed) {
                try {
                    return java.time.LocalDateTime.parse(iso).atZone(java.time.ZoneId.systemDefault())
                            .toInstant().toEpochMilli();
                } catch (DateTimeParseException alsoFailed) {
                    return 0;
                }
            }
        }
    }

    public static String ago(Context c, String iso) {
        long at = parseIso(iso);
        if (at == 0) return "";
        long seconds = Math.max(0, (System.currentTimeMillis() - at) / 1000);
        if (seconds < 60) return c.getString(R.string.time_just_now);
        if (seconds < 3600) return c.getString(R.string.time_minutes, (int) (seconds / 60));
        if (seconds < 86400) return c.getString(R.string.time_hours, (int) (seconds / 3600));
        return c.getString(R.string.time_days, (int) (seconds / 86400));
    }

    public static String mb(double mb) {
        if (mb >= 1024) return String.format(java.util.Locale.ROOT, "%.1f GB", mb / 1024);
        return String.format(java.util.Locale.ROOT, "%.0f MB", mb);
    }

    public static String pct(double pct) {
        return String.format(java.util.Locale.ROOT, "%.0f%%", pct);
    }

    /** "inbox" → "Inbox", "full_access" → "Full access". */
    public static String human(String raw) {
        if (raw == null || raw.isEmpty()) return "";
        String text = raw.replace('_', ' ');
        return Character.toUpperCase(text.charAt(0)) + text.substring(1);
    }
}
