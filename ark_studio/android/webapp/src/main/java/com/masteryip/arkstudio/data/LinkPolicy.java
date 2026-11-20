package com.masteryip.arkstudio.data;

import java.net.URI;
import java.util.Locale;

/**
 * Where a link from an ARK page goes (pure Java, shared with the JVM tests).
 *
 * <p>Pages of the ARK server itself stay in the WebView. Other http(s) pages
 * open in the browser (never inside the signed-in WebView), mailto: in a mail
 * app and file: links show their path. Every other scheme (javascript:,
 * intent:, data:, custom app schemes) is dropped.</p>
 */
public final class LinkPolicy {
    public enum Action {
        /** Load in the ARK WebView. */
        STAY,
        /** Open in the browser (Custom Tab when the browser supports it). */
        BROWSER,
        /** Open in a mail app. */
        MAIL,
        /** A file on the ARK server: show its path. */
        FILE,
        /** Do nothing. */
        BLOCK
    }

    private LinkPolicy() {
    }

    /**
     * @param url       the link target
     * @param mainFrame false for navigations inside an iframe (embedded content)
     * @param arkPages  URLs whose origin counts as the ARK server (the saved
     *                  server address, the page currently shown)
     */
    public static Action decide(String url, boolean mainFrame, String... arkPages) {
        String scheme = scheme(url);
        switch (scheme) {
            case "http":
            case "https":
                if (!mainFrame) return Action.STAY;
                String target = origin(url);
                if (target.isEmpty()) return Action.BLOCK;
                if (arkPages != null) {
                    for (String page : arkPages) {
                        if (target.equals(origin(page))) return Action.STAY;
                    }
                }
                return Action.BROWSER;
            case "mailto":
                return Action.MAIL;
            case "file":
                return Action.FILE;
            case "about":
            case "blob":
                // Blank and generated frames inside the page.
                return mainFrame ? Action.BLOCK : Action.STAY;
            default:
                return Action.BLOCK;
        }
    }

    /** Lower-case scheme, or "" when the text has none. */
    public static String scheme(String url) {
        if (url == null) return "";
        String value = url.trim();
        int colon = value.indexOf(':');
        if (colon <= 0) return "";
        String scheme = value.substring(0, colon);
        if (!scheme.matches("[A-Za-z][A-Za-z0-9+.-]*")) return "";
        return scheme.toLowerCase(Locale.ROOT);
    }

    /** scheme://host[:port] with default ports dropped, or "" for non-http(s) URLs. */
    public static String origin(String url) {
        if (url == null) return "";
        try {
            URI uri = new URI(url.trim());
            String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
            String host = uri.getHost() == null ? "" : uri.getHost().toLowerCase(Locale.ROOT);
            if (!("http".equals(scheme) || "https".equals(scheme)) || host.isEmpty()) return "";
            int port = uri.getPort();
            if (port == -1 || ("http".equals(scheme) && port == 80) || ("https".equals(scheme) && port == 443)) {
                return scheme + "://" + host;
            }
            return scheme + "://" + host + ":" + port;
        } catch (Exception bad) {
            return "";
        }
    }

    /** The local path of a file: URL (for showing or copying it). */
    public static String filePath(String url) {
        if (url == null) return "";
        try {
            String path = new URI(url.trim()).getPath();
            if (path == null) return url;
            // file:///E:/x -> E:/x
            if (path.matches("^/[A-Za-z]:.*")) path = path.substring(1);
            return path;
        } catch (Exception bad) {
            String value = url.trim();
            return value.replaceFirst("(?i)^file:/*", "/");
        }
    }
}
