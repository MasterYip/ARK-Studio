package com.masteryip.arkstudio.data;

import org.json.JSONObject;

/**
 * Reading {@code GET /api/mobile/session} for a saved server: whether its
 * device token still signs the phone in, and the account, permission and
 * current project to show. Pure Java for JVM tests.
 */
public final class SessionStatus {
    private SessionStatus() {
    }

    /** {@code true} when the server still accepts the device token. */
    public static boolean signedIn(JSONObject status) {
        return status != null && status.optBoolean("authenticated", false);
    }

    /** The session with the account, permission and project the server reports. */
    public static AccountSession apply(AccountSession session, JSONObject status) {
        if (!signedIn(status)) return session;
        JSONObject project = status.optJSONObject("project");
        return session.withStatus(status.isNull("user") ? "" : status.optString("user", ""),
                status.isNull("permission") ? "" : status.optString("permission", ""),
                project == null ? "" : project.optString("id", ""),
                project == null ? "" : project.optString("name", ""));
    }
}
