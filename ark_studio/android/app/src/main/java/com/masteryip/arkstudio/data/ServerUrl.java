package com.masteryip.arkstudio.data;

import java.net.URI;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Pure-Java normalization for ARK server URLs.
 *
 * <p>The Android UI and the JVM unit tests share this class, so it deliberately
 * avoids {@code android.net.Uri} and any other Android API. Normalization is
 * intentionally forgiving for LAN use: a bare {@code host:port} gets an
 * {@code http://} prefix, default ports are removed, and scheme/host casing is
 * canonicalized. The path, query and pairing fragment are preserved.</p>
 */
public final class ServerUrl {
    private static final Pattern HAS_SCHEME = Pattern.compile("^[A-Za-z][A-Za-z0-9+.-]*://");
    private static final Pattern HTTP_IN_TEXT = Pattern.compile("(?i)https?://[^\\s\"'<>]+");

    private ServerUrl() {
    }

    /** Returns a canonical http(s) URL, or {@code null} when the input is unusable. */
    public static String normalize(String raw) {
        Parsed parsed = parse(raw);
        return parsed == null ? null : parsed.build(true);
    }

    /** Removes a pairing fragment while keeping a valid canonical URL. */
    public static String withoutFragment(String raw) {
        String normalized = normalize(raw);
        if (normalized == null) {
            return null;
        }
        int hash = normalized.indexOf('#');
        return hash < 0 ? normalized : normalized.substring(0, hash);
    }

    /**
     * Stable identity used for de-duplication. Query and fragment are ignored so
     * the same server saved from a QR pairing URL and added manually matches;
     * a trailing slash is also insignificant.
     */
    public static String identity(String raw) {
        Parsed parsed = parse(raw);
        return parsed == null ? null : parsed.identity();
    }

    public static boolean sameServer(String first, String second) {
        String a = identity(first);
        return a != null && a.equals(identity(second));
    }

    /** Human-readable host[:port], e.g. {@code 192.168.1.20:8891}. */
    public static String displayHost(String raw) {
        Parsed parsed = parse(raw);
        if (parsed == null) {
            return "";
        }
        return parsed.authority();
    }

    /** Friendly default name for a saved server. */
    public static String defaultName(String raw) {
        String host = displayHost(raw);
        return host.isEmpty() ? "ARK server" : "ARK \u00b7 " + host;
    }

    /**
     * Extracts an http(s) URL from a QR payload. ARK pairing QRs contain a plain
     * pairing URL, but accepting the first URL inside a JSON/text payload makes
     * the scanner tolerant of other QR generators too.
     */
    public static String extractFromQr(String content) {
        if (content == null) {
            return null;
        }
        String value = content.trim().replace("\\/", "/");
        if (value.isEmpty()) {
            return null;
        }
        Matcher matcher = HTTP_IN_TEXT.matcher(value);
        if (matcher.find()) {
            return normalize(matcher.group());
        }
        // Scheme-less payloads are accepted only when they look like a host
        // and/or port; arbitrary QR text ("hello") must not become a server.
        boolean looksLikeHost = value.indexOf('.') >= 0
                || value.indexOf(':') >= 0
                || value.indexOf('/') >= 0
                || value.indexOf('[') >= 0;
        return looksLikeHost ? normalize(value) : null;
    }

    private static Parsed parse(String raw) {
        if (raw == null) {
            return null;
        }
        String value = raw.trim();
        if (value.isEmpty()) {
            return null;
        }
        if (!HAS_SCHEME.matcher(value).find()) {
            value = "http://" + value;
        }

        URI uri;
        try {
            uri = new URI(value);
        } catch (Exception failed) {
            return null;
        }

        String scheme = uri.getScheme();
        if (scheme == null) {
            return null;
        }
        scheme = scheme.toLowerCase(Locale.ROOT);
        if (!"http".equals(scheme) && !"https".equals(scheme)) {
            return null;
        }

        String host = uri.getHost();
        int port = uri.getPort();
        if (host == null || host.isEmpty()) {
            Authority authority = parseAuthority(uri.getRawAuthority());
            if (authority == null) {
                return null;
            }
            host = authority.host;
            port = authority.port;
        }
        host = stripBrackets(host).toLowerCase(Locale.ROOT);
        if (host.isEmpty() || host.indexOf(' ') >= 0 || host.indexOf('/') >= 0) {
            return null;
        }
        if (port < -1 || port > 65535) {
            return null;
        }

        String path = uri.getRawPath();
        if (path == null || path.isEmpty()) {
            path = "/";
        } else if (!path.startsWith("/")) {
            path = "/" + path;
        }

        return new Parsed(scheme, host, port, path, uri.getRawQuery(), uri.getRawFragment());
    }

    private static Authority parseAuthority(String rawAuthority) {
        if (rawAuthority == null || rawAuthority.isEmpty()) {
            return null;
        }
        String authority = rawAuthority;
        int at = authority.lastIndexOf('@');
        if (at >= 0) {
            authority = authority.substring(at + 1);
        }
        if (authority.isEmpty()) {
            return null;
        }

        String host;
        int port = -1;
        if (authority.startsWith("[")) {
            int end = authority.indexOf(']');
            if (end < 0) {
                return null;
            }
            host = authority.substring(0, end + 1);
            String rest = authority.substring(end + 1);
            if (!rest.isEmpty()) {
                if (!rest.startsWith(":")) {
                    return null;
                }
                port = parsePort(rest.substring(1));
                if (port == Integer.MIN_VALUE) {
                    return null;
                }
            }
        } else {
            int colon = authority.lastIndexOf(':');
            if (colon >= 0 && authority.indexOf(':') == colon) {
                host = authority.substring(0, colon);
                port = parsePort(authority.substring(colon + 1));
                if (port == Integer.MIN_VALUE) {
                    return null;
                }
            } else {
                // No port, or an unbracketed IPv6 literal.
                host = authority;
            }
        }
        host = stripBrackets(host);
        if (host.isEmpty() || host.indexOf(' ') >= 0) {
            return null;
        }
        return new Authority(host, port);
    }

    private static int parsePort(String value) {
        if (value == null || value.isEmpty()) {
            return Integer.MIN_VALUE;
        }
        long result = 0;
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            if (c < '0' || c > '9') {
                return Integer.MIN_VALUE;
            }
            result = result * 10 + (c - '0');
            if (result > 65535) {
                return Integer.MIN_VALUE;
            }
        }
        return (int) result;
    }

    private static String stripBrackets(String host) {
        if (host != null && host.length() >= 2 && host.charAt(0) == '[' && host.charAt(host.length() - 1) == ']') {
            return host.substring(1, host.length() - 1);
        }
        return host;
    }

    private static boolean isDefaultPort(String scheme, int port) {
        return ("http".equals(scheme) && port == 80) || ("https".equals(scheme) && port == 443);
    }

    private static final class Authority {
        final String host;
        final int port;

        Authority(String host, int port) {
            this.host = host;
            this.port = port;
        }
    }

    private static final class Parsed {
        final String scheme;
        final String host;
        final int port;
        final String path;
        final String query;
        final String fragment;

        Parsed(String scheme, String host, int port, String path, String query, String fragment) {
            this.scheme = scheme;
            this.host = host;
            this.port = port;
            this.path = path;
            this.query = query;
            this.fragment = fragment;
        }

        String authority() {
            String value = host.indexOf(':') >= 0 ? "[" + host + "]" : host;
            if (port >= 0 && !isDefaultPort(scheme, port)) {
                value += ":" + port;
            }
            return value;
        }

        String build(boolean includeQueryAndFragment) {
            StringBuilder out = new StringBuilder();
            out.append(scheme).append("://").append(authority()).append(path);
            if (includeQueryAndFragment && query != null) {
                out.append('?').append(query);
            }
            if (includeQueryAndFragment && fragment != null) {
                out.append('#').append(fragment);
            }
            return out.toString();
        }

        String identity() {
            String trimmedPath = path;
            while (trimmedPath.length() > 1 && trimmedPath.endsWith("/")) {
                trimmedPath = trimmedPath.substring(0, trimmedPath.length() - 1);
            }
            if ("/".equals(trimmedPath)) {
                trimmedPath = "";
            }
            return scheme + "://" + authority() + trimmedPath;
        }
    }
}
