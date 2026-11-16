package com.masteryip.arkstudio.data;

import java.net.URI;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * A scanned ARK QR: the server base URL plus the optional one-time
 * {@code #pair=<code>}. The code is used once for the claim and never stored.
 */
public final class PairingLink {
    private static final Pattern PAIR = Pattern.compile("(?:^|[#?&/])pair=([A-Za-z0-9_\\-.~%]+)");

    private final String baseUrl;
    private final String code;

    private PairingLink(String baseUrl, String code) {
        this.baseUrl = baseUrl;
        this.code = code;
    }

    /** Parses QR text; returns {@code null} when it holds no http(s) server URL. */
    public static PairingLink parse(String qrContent) {
        if (qrContent == null) return null;
        String text = qrContent.trim().replace("\\/", "/");
        String code = null;
        int hash = text.indexOf('#');
        if (hash >= 0) {
            Matcher matcher = PAIR.matcher(text.substring(hash));
            if (matcher.find()) code = decode(matcher.group(1));
        }
        String url = ServerUrl.extractFromQr(text);
        if (url == null) return null;
        String base = ServerUrl.withoutFragment(url);
        if (base == null) return null;
        int query = base.indexOf('?');
        if (query >= 0) base = base.substring(0, query);
        if (!base.endsWith("/")) base = base + "/";
        return new PairingLink(base, code == null || code.isEmpty() ? null : code);
    }

    private static String decode(String value) {
        try {
            return java.net.URLDecoder.decode(value, "UTF-8");
        } catch (Exception failed) {
            return value;
        }
    }

    public String getBaseUrl() {
        return baseUrl;
    }

    /** The one-time pairing code, or {@code null} for a plain server URL. */
    public String getCode() {
        return code;
    }

    public boolean hasCode() {
        return code != null;
    }

    // ------------------------------------------------------------- transport

    public static boolean isHttps(String url) {
        return url != null && url.trim().toLowerCase(Locale.ROOT).startsWith("https://");
    }

    /**
     * True for loopback, RFC1918/CGNAT/link-local IPv4, unique-local/link-local
     * IPv6, {@code .local}/{@code .lan} names and the emulator host alias.
     * Cleartext http is only quietly accepted for these; any other http server
     * needs an explicit confirmation before a password is sent.
     */
    public static boolean isPrivateHost(String url) {
        String host;
        try {
            host = new URI(url.trim()).getHost();
        } catch (Exception failed) {
            return false;
        }
        if (host == null) return false;
        host = host.toLowerCase(Locale.ROOT);
        if (host.startsWith("[") && host.endsWith("]")) host = host.substring(1, host.length() - 1);
        if (host.equals("localhost") || host.endsWith(".local") || host.endsWith(".lan")
                || host.endsWith(".home.arpa") || host.equals("10.0.2.2")) {
            return true;
        }
        if (host.contains(":")) {
            return host.equals("::1") || host.startsWith("fe80:") || host.startsWith("fc")
                    || host.startsWith("fd");
        }
        String[] parts = host.split("\\.");
        if (parts.length != 4) return false;
        int[] octets = new int[4];
        for (int i = 0; i < 4; i++) {
            try {
                octets[i] = Integer.parseInt(parts[i]);
            } catch (NumberFormatException notIp) {
                return false;
            }
            if (octets[i] < 0 || octets[i] > 255) return false;
        }
        int a = octets[0];
        int b = octets[1];
        return a == 10 || a == 127 || (a == 172 && b >= 16 && b <= 31) || (a == 192 && b == 168)
                || (a == 169 && b == 254) || (a == 100 && b >= 64 && b <= 127);
    }

    /** http to a non-private host: credentials and tokens would cross the internet in clear. */
    public static boolean isInsecurePublic(String url) {
        return !isHttps(url) && !isPrivateHost(url);
    }

    /** {@code scheme://host[:port]} of a URL, for same-origin checks. */
    public static String origin(String url) {
        try {
            URI uri = new URI(url.trim());
            String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
            String host = uri.getHost() == null ? "" : uri.getHost().toLowerCase(Locale.ROOT);
            if (host.isEmpty() || !(scheme.equals("http") || scheme.equals("https"))) return "";
            int port = uri.getPort();
            if (port == -1 || (scheme.equals("http") && port == 80) || (scheme.equals("https") && port == 443)) {
                return scheme + "://" + host;
            }
            return scheme + "://" + host + ":" + port;
        } catch (Exception failed) {
            return "";
        }
    }

    public static boolean sameOrigin(String a, String b) {
        String first = origin(a);
        return !first.isEmpty() && first.equals(origin(b));
    }
}
