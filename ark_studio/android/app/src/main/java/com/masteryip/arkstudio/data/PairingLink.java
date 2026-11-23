package com.masteryip.arkstudio.data;

import java.net.URI;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * A scanned ARK QR: the server base URL plus the optional one-time
 * {@code #pair=<code>}. The code is used once for the claim and never stored.
 * A re-link QR ({@code #relink=<server id>}) carries the new address of a
 * server the phone already paired with (a Cloudflare quick tunnel that
 * restarted).
 */
public final class PairingLink {
    private static final Pattern PAIR = Pattern.compile("(?:^|[#?&/])pair=([A-Za-z0-9_\\-.~%]+)");
    private static final Pattern RELINK = Pattern.compile("(?:^|[#?&/])relink=([0-9A-Fa-f]{20})(?![0-9A-Za-z])");
    /** The label of a quick-tunnel host, e.g. {@code able-baker-charlie-delta}. */
    private static final Pattern SHORT_CODE = Pattern.compile("^[a-z0-9]+(?:-[a-z0-9]+){1,9}$");
    public static final String QUICK_TUNNEL_SUFFIX = ".trycloudflare.com";

    private final String baseUrl;
    private final String code;
    private final String relinkId;

    private PairingLink(String baseUrl, String code, String relinkId) {
        this.baseUrl = baseUrl;
        this.code = code;
        this.relinkId = relinkId;
    }

    /** Parses QR text; returns {@code null} when it holds no http(s) server URL. */
    public static PairingLink parse(String qrContent) {
        if (qrContent == null) return null;
        String text = qrContent.trim().replace("\\/", "/");
        String code = null;
        String relink = null;
        int hash = text.indexOf('#');
        if (hash >= 0) {
            Matcher matcher = PAIR.matcher(text.substring(hash));
            if (matcher.find()) code = decode(matcher.group(1));
            Matcher again = RELINK.matcher(text.substring(hash));
            if (again.find()) relink = again.group(1).toLowerCase(Locale.ROOT);
        }
        String url = ServerUrl.extractFromQr(text);
        if (url == null) return null;
        String base = ServerUrl.withoutFragment(url);
        if (base == null) return null;
        int query = base.indexOf('?');
        if (query >= 0) base = base.substring(0, query);
        if (!base.endsWith("/")) base = base + "/";
        base = quickTunnelHttps(base);
        return new PairingLink(base, code == null || code.isEmpty() ? null : code, relink);
    }

    /**
     * What a person typed or pasted in the re-link dialog: a re-link or plain
     * URL, a quick-tunnel host, or the short code (the host's label, e.g.
     * {@code able-baker-charlie-delta} for
     * {@code https://able-baker-charlie-delta.trycloudflare.com/}).
     */
    public static PairingLink parseRelinkInput(String input) {
        if (input == null) return null;
        String text = halfWidth(input).trim();
        String url = shortCodeUrl(text);
        if (url != null) return new PairingLink(url, null, null);
        PairingLink link = parse(text);
        if (link == null || link.hasCode()) return link == null ? null : new PairingLink(link.baseUrl, null, link.relinkId);
        return link;
    }

    /** Full-width letters, digits and punctuation (Chinese keyboards) as ASCII; 。 as a dot. */
    static String halfWidth(String text) {
        StringBuilder out = new StringBuilder(text.length());
        for (int i = 0; i < text.length(); i++) {
            char c = text.charAt(i);
            if (c >= '！' && c <= '～') c = (char) (c - 0xFEE0);
            else if (c == '。') c = '.';
            else if (c == '　') c = ' ';
            out.append(c);
        }
        return out.toString();
    }

    /** {@code https://<code>.trycloudflare.com/} for a short code, else {@code null}. */
    public static String shortCodeUrl(String input) {
        if (input == null) return null;
        String code = input.trim().toLowerCase(Locale.ROOT);
        if (!SHORT_CODE.matcher(code).matches() || code.length() > 63) return null;
        return "https://" + code + QUICK_TUNNEL_SUFFIX + "/";
    }

    /** True for a Cloudflare quick-tunnel address ({@code *.trycloudflare.com}). */
    public static boolean isQuickTunnel(String url) {
        String host = ServerIdentity.hostOf(url == null ? "" : url);
        return host.endsWith(QUICK_TUNNEL_SUFFIX) && host.length() > QUICK_TUNNEL_SUFFIX.length();
    }

    /** Quick tunnels only answer https; a typed host without a scheme becomes https. */
    private static String quickTunnelHttps(String base) {
        if (base.startsWith("http://") && isQuickTunnel(base)) {
            String rest = base.substring("http://".length());
            int slash = rest.indexOf('/');
            String authority = slash < 0 ? rest : rest.substring(0, slash);
            String path = slash < 0 ? "/" : rest.substring(slash);
            if (authority.endsWith(":80")) authority = authority.substring(0, authority.length() - 3);
            return "https://" + authority + path;
        }
        return base;
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

    /** The server id of a re-link QR ({@code #relink=…}), or {@code null}. */
    public String getRelinkId() {
        return relinkId;
    }

    public boolean isRelink() {
        return relinkId != null;
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
