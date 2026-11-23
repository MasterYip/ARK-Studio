package com.masteryip.arkstudio.api;

/** A failed ARK API call: HTTP status (0 for network failures) plus ARK's error code. */
public final class ApiException extends Exception {
    public static final String NETWORK = "NETWORK";
    /** A Cloudflare error page: the tunnel address no longer leads to ARK. */
    public static final String TUNNEL_DOWN = "TUNNEL_DOWN";

    private final int status;
    private final String code;
    private final int retryAfter;

    public ApiException(int status, String code, String message) {
        this(status, code, message, 0, null);
    }

    public ApiException(int status, String code, String message, int retryAfter, Throwable cause) {
        super(message == null || message.isEmpty() ? ("HTTP " + status) : message, cause);
        this.status = status;
        this.code = code == null || code.isEmpty() ? "error" : code;
        this.retryAfter = Math.max(0, retryAfter);
    }

    public int getStatus() {
        return status;
    }

    public String getCode() {
        return code;
    }

    public int getRetryAfter() {
        return retryAfter;
    }

    /** Server unreachable, DNS/TLS/timeout: show the offline state with Retry. */
    public boolean isNetwork() {
        return status == 0;
    }

    /** The server could not be reached at all, or only Cloudflare answered. */
    public boolean isUnreachable() {
        return status == 0 || TUNNEL_DOWN.equals(code);
    }

    /** The device token is gone (revoked, expired, unknown): the user must sign in again. */
    public boolean isSignedOut() {
        return status == 401 && (code.startsWith("MOBILE_TOKEN") || "AUTH_REQUIRED".equals(code)
                || "PAIRING_REVOKED".equals(code));
    }

    public boolean isReadOnly() {
        return status == 403 && ("MOBILE_READ_ONLY".equals(code) || "PROJECT_FORBIDDEN".equals(code));
    }
}
