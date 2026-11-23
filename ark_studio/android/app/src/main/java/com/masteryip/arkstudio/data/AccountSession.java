package com.masteryip.arkstudio.data;

import org.json.JSONException;
import org.json.JSONObject;

/**
 * A signed-in device session on one ARK server. Holds the device token
 * ({@code arkm_…}) and what the UI shows: account, permission, project and how
 * the phone signed in. The password is never part of this object.
 */
public final class AccountSession {
    public static final String SOURCE_QR = "qr";
    public static final String SOURCE_PASSWORD = "password";

    public final String serverUrl;
    public final String token;
    public final String user;
    public final String permission;
    public final String projectId;
    public final String projectName;
    public final String source;
    public final long signedInAt;

    public AccountSession(String serverUrl, String token, String user, String permission,
                          String projectId, String projectName, String source, long signedInAt) {
        this.serverUrl = serverUrl == null ? "" : serverUrl;
        this.token = token == null ? "" : token;
        this.user = user == null ? "" : user;
        this.permission = "read_only".equals(permission) ? "read_only" : "full";
        this.projectId = projectId == null ? "" : projectId;
        this.projectName = projectName == null ? "" : projectName;
        this.source = SOURCE_PASSWORD.equals(source) ? SOURCE_PASSWORD : SOURCE_QR;
        this.signedInAt = signedInAt;
    }

    /** Builds a session from a login/claim response. */
    public static AccountSession fromLogin(String serverUrl, JSONObject response, String fallbackSource,
                                           long now) {
        String user = response.optString("user", "");
        if (user.isEmpty()) user = response.optString("mobile_user", "");
        String source = response.optString("source", fallbackSource);
        return new AccountSession(serverUrl, response.optString("mobile_token", ""), user,
                response.optString("permission", "full"), response.optString("project_id", ""),
                response.optString("project", ""), source, now);
    }

    public boolean isReadOnly() {
        return "read_only".equals(permission);
    }

    /**
     * A device session belongs to the user, not to one project: QR-paired and
     * password phones both switch between the account's projects (the server
     * answers 403 MOBILE_PROJECT_PINNED on servers older than 2.1.4).
     */
    public boolean canSwitchProjects() {
        return true;
    }

    public boolean isValid() {
        return token.startsWith("arkm_") && !serverUrl.isEmpty();
    }

    public AccountSession withProject(String id, String name, String newPermission) {
        return new AccountSession(serverUrl, token, user, newPermission == null ? permission : newPermission,
                id, name, source, signedInAt);
    }

    public AccountSession withStatus(String newUser, String newPermission, String id, String name) {
        return new AccountSession(serverUrl, token, newUser == null || newUser.isEmpty() ? user : newUser,
                newPermission == null || newPermission.isEmpty() ? permission : newPermission,
                id == null || id.isEmpty() ? projectId : id,
                name == null || name.isEmpty() ? projectName : name, source, signedInAt);
    }

    /** Metadata only: the token is stored separately, encrypted. */
    public JSONObject metadataJson() {
        JSONObject json = new JSONObject();
        try {
            json.put("server", serverUrl);
            json.put("user", user);
            json.put("permission", permission);
            json.put("project_id", projectId);
            json.put("project", projectName);
            json.put("source", source);
            json.put("signed_in_at", signedInAt);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return json;
    }

    public static AccountSession fromMetadata(JSONObject json, String token) {
        return new AccountSession(json.optString("server", ""), token, json.optString("user", ""),
                json.optString("permission", "full"), json.optString("project_id", ""),
                json.optString("project", ""), json.optString("source", SOURCE_QR),
                json.optLong("signed_in_at", 0L));
    }
}
