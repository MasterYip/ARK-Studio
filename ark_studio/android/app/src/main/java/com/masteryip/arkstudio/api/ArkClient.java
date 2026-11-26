package com.masteryip.arkstudio.api;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.lang.reflect.Field;
import java.net.HttpURLConnection;
import java.net.ProtocolException;
import java.net.URL;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;

/**
 * Minimal JSON client for the ARK HTTP API.
 *
 * Every call carries the device token in {@code X-ARK-Mobile-Token} (never in
 * the URL). The client sends no {@code Origin}/{@code Referer}, which the
 * server's same-origin check treats as a non-browser caller. Plain Java so it
 * runs in JVM unit tests against an in-process HTTP server.
 */
public final class ArkClient {
    public static final String TOKEN_HEADER = "X-ARK-Mobile-Token";
    public static final String USER_AGENT = "ARKStudioAndroid/3.0";
    /** The phone's hashed device id ({@code data.DeviceId}), sent with every request. */
    public static final String DEVICE_HEADER = "X-ARK-Device";

    private static volatile String deviceId = "";

    /** Set once at start-up ({@code Accounts}); empty until then. */
    public static void setDeviceId(String id) {
        deviceId = id == null ? "" : id;
    }

    public static String deviceId() {
        return deviceId;
    }

    private final String base;
    private final String token;
    private int connectTimeoutMs = 8000;
    private int readTimeoutMs = 20000;

    public ArkClient(String baseUrl, String token) {
        this.base = normalizeBase(baseUrl);
        this.token = token == null ? "" : token;
    }

    public ArkClient withTimeouts(int connectMs, int readMs) {
        this.connectTimeoutMs = connectMs;
        this.readTimeoutMs = readMs;
        return this;
    }

    public String getBase() {
        return base;
    }

    public String getToken() {
        return token;
    }

    /** Base URL with exactly one trailing slash and no query/fragment. */
    public static String normalizeBase(String raw) {
        String value = raw == null ? "" : raw.trim();
        int cut = value.indexOf('#');
        if (cut >= 0) value = value.substring(0, cut);
        cut = value.indexOf('?');
        if (cut >= 0) value = value.substring(0, cut);
        while (value.endsWith("/")) value = value.substring(0, value.length() - 1);
        return value + "/";
    }

    /** Absolute URL for an API path such as {@code /api/bootstrap}. */
    public String url(String path) {
        String p = path == null ? "" : path;
        while (p.startsWith("/")) p = p.substring(1);
        return base + p;
    }

    public static String enc(String value) {
        try {
            return URLEncoder.encode(value == null ? "" : value, "UTF-8").replace("+", "%20");
        } catch (java.io.UnsupportedEncodingException impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    public JSONObject get(String path) throws ApiException {
        return request("GET", path, null);
    }

    public JSONObject post(String path, JSONObject body) throws ApiException {
        return request("POST", path, body == null ? new JSONObject() : body);
    }

    public JSONObject patch(String path, JSONObject body) throws ApiException {
        return request("PATCH", path, body == null ? new JSONObject() : body);
    }

    public JSONObject delete(String path) throws ApiException {
        return request("DELETE", path, null);
    }

    // ------------------------------------------------------------- sign-in

    /** {@code POST /api/mobile/login}: password in, device token out (password never kept). */
    public static JSONObject login(String baseUrl, String username, String password,
                                   boolean readOnly, String device) throws ApiException {
        JSONObject body = new JSONObject();
        try {
            body.put("username", username == null ? "" : username.trim());
            body.put("password", password == null ? "" : password);
            body.put("permission", readOnly ? "read_only" : "full");
            if (device != null && !device.isEmpty()) body.put("device", device);
            if (!deviceId.isEmpty()) body.put("device_id", deviceId);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return new ArkClient(baseUrl, null).request("POST", "/api/mobile/login", body);
    }

    /**
     * {@code POST /api/mobile/pair/claim}: one-time QR code in, device token out.
     * Through a Cloudflare Tunnel the server answers 202 with {@code pending},
     * {@code claim_id}, {@code claim_secret} and {@code confirm_code} instead
     * (see {@link #isPending}); the phone then polls {@link #claimStatus}.
     */
    public static JSONObject claim(String baseUrl, String code) throws ApiException {
        return claim(baseUrl, code, null);
    }

    public static JSONObject claim(String baseUrl, String code, String device) throws ApiException {
        JSONObject body = new JSONObject();
        try {
            body.put("token", code == null ? "" : code);
            if (device != null && !device.isEmpty()) body.put("device", device);
            if (!deviceId.isEmpty()) body.put("device_id", deviceId);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return new ArkClient(baseUrl, null).request("POST", "/api/mobile/pair/claim", body);
    }

    /**
     * {@code POST /api/mobile/pair/claim/status}: still pending (202, same shape
     * without the secret), the claim result with {@code mobile_token} once the
     * computer confirmed, 403 {@code PAIRING_REJECTED} or 401
     * {@code PAIRING_CONFIRM_EXPIRED}.
     */
    public static JSONObject claimStatus(String baseUrl, String code, String claimId, String claimSecret)
            throws ApiException {
        JSONObject body = new JSONObject();
        try {
            body.put("code", code == null ? "" : code);
            body.put("claim_id", claimId == null ? "" : claimId);
            body.put("claim_secret", claimSecret == null ? "" : claimSecret);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return new ArkClient(baseUrl, null).withTimeouts(10000, 15000)
                .request("POST", "/api/mobile/pair/claim/status", body);
    }

    /** Withdraws a claim that waits for the computer (the phone cancelled). */
    public static JSONObject cancelClaim(String baseUrl, String claimId, String claimSecret) throws ApiException {
        JSONObject body = new JSONObject();
        try {
            body.put("claim_id", claimId == null ? "" : claimId);
            body.put("claim_secret", claimSecret == null ? "" : claimSecret);
            body.put("cancel", true);
        } catch (JSONException impossible) {
            throw new IllegalStateException(impossible);
        }
        return new ArkClient(baseUrl, null).withTimeouts(10000, 15000)
                .request("POST", "/api/mobile/pair/claim/status", body);
    }

    /** A claim waiting for the computer's confirmation (no token yet). */
    public static boolean isPending(JSONObject response) {
        return response != null && response.optBoolean("pending", false)
                && response.optString("mobile_token", "").isEmpty();
    }

    /**
     * {@code GET /api/mobile/identity?nonce=…} without the device token: a new
     * address is checked before the token is sent there.
     */
    public static JSONObject identity(String baseUrl, String nonce) throws ApiException {
        return new ArkClient(baseUrl, null).withTimeouts(10000, 15000)
                .get("/api/mobile/identity?nonce=" + enc(nonce));
    }

    // ------------------------------------------------------------- transport

    public JSONObject request(String method, String path, JSONObject body) throws ApiException {
        HttpURLConnection connection = null;
        try {
            connection = (HttpURLConnection) new URL(url(path)).openConnection();
            connection.setConnectTimeout(connectTimeoutMs);
            connection.setReadTimeout(readTimeoutMs);
            connection.setInstanceFollowRedirects(false);
            connection.setUseCaches(false);
            setMethod(connection, method);
            connection.setRequestProperty("Accept", "application/json");
            connection.setRequestProperty("User-Agent", USER_AGENT);
            if (!token.isEmpty()) connection.setRequestProperty(TOKEN_HEADER, token);
            if (!deviceId.isEmpty()) connection.setRequestProperty(DEVICE_HEADER, deviceId);
            if (body != null) {
                byte[] bytes = body.toString().getBytes(StandardCharsets.UTF_8);
                connection.setDoOutput(true);
                connection.setRequestProperty("Content-Type", "application/json; charset=utf-8");
                // Stream only large uploads; buffered small bodies keep error
                // bodies readable for 401 responses on every HTTP stack.
                if (bytes.length > 256 * 1024) connection.setFixedLengthStreamingMode(bytes.length);
                try (OutputStream out = connection.getOutputStream()) {
                    out.write(bytes);
                }
            }
            int status = connection.getResponseCode();
            InputStream stream = status >= 400 ? connection.getErrorStream() : connection.getInputStream();
            String text = stream == null ? "" : readAll(stream);
            if (status >= 300 && status < 400) {
                // The login gate redirects browsers; for the API it means "sign in".
                throw new ApiException(401, "AUTH_REQUIRED", "Sign in again.");
            }
            JSONObject json = parse(text);
            if (json == null && cloudflareDown(status, connection.getHeaderField("Server"), text)) {
                throw new ApiException(status, ApiException.TUNNEL_DOWN,
                        "Cloudflare could not reach this server (HTTP " + status + ").");
            }
            if (status >= 400) {
                throw errorFrom(status, json, connection.getHeaderField("Retry-After"));
            }
            return json == null ? new JSONObject() : json;
        } catch (ApiException failed) {
            throw failed;
        } catch (IOException failed) {
            throw new ApiException(0, ApiException.NETWORK,
                    failed.getMessage() == null ? failed.getClass().getSimpleName() : failed.getMessage(),
                    0, failed);
        } finally {
            if (connection != null) connection.disconnect();
        }
    }

    /**
     * A Cloudflare error page instead of ARK's JSON: the tunnel behind the
     * address is gone (530 / error 1033 when a quick tunnel stopped) or
     * cloudflared cannot reach ARK (502, 504, 52x).
     */
    public static boolean cloudflareDown(int status, String serverHeader, String body) {
        if (status < 400) return false;
        String server = serverHeader == null ? "" : serverHeader.toLowerCase(java.util.Locale.ROOT);
        String text = body == null ? "" : body.toLowerCase(java.util.Locale.ROOT);
        boolean cloudflare = server.contains("cloudflare") || text.contains("cloudflare");
        if (status == 530) return true;
        if (!cloudflare) return false;
        return status == 502 || status == 503 || status == 504 || (status >= 520 && status <= 527)
                || status == 404 && (text.contains("error 1033") || text.contains("tunnel"))
                || text.contains("error 1033") || text.contains("error code: 1033");
    }

    static ApiException errorFrom(int status, JSONObject json, String retryAfter) {
        String code = "HTTP_" + status;
        String message = "HTTP " + status;
        if (json != null) {
            JSONObject error = json.optJSONObject("error");
            if (error != null) {
                code = error.optString("code", code);
                message = error.optString("message", message);
            } else if (json.has("error")) {
                message = json.optString("error", message);
            }
        }
        int retry = 0;
        try {
            retry = retryAfter == null ? 0 : Integer.parseInt(retryAfter.trim());
        } catch (NumberFormatException ignored) {
            retry = 0;
        }
        return new ApiException(status, code, message, retry, null);
    }

    private static JSONObject parse(String text) {
        if (text == null || text.trim().isEmpty()) return null;
        try {
            return new JSONObject(text);
        } catch (JSONException notJson) {
            return null;
        }
    }

    static String readAll(InputStream stream) throws IOException {
        try (InputStream in = stream) {
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buffer = new byte[8192];
            int read;
            while ((read = in.read(buffer)) != -1) out.write(buffer, 0, read);
            return new String(out.toByteArray(), StandardCharsets.UTF_8);
        }
    }

    /**
     * Android's HttpURLConnection accepts PATCH; the desktop JDK used by unit
     * tests does not, so fall back to setting the field directly.
     */
    private static void setMethod(HttpURLConnection connection, String method) throws ProtocolException {
        try {
            connection.setRequestMethod(method);
        } catch (ProtocolException unsupported) {
            try {
                Field field = HttpURLConnection.class.getDeclaredField("method");
                field.setAccessible(true);
                field.set(connection, method);
            } catch (ReflectiveOperationException | RuntimeException failed) {
                throw unsupported;
            }
        }
    }
}
