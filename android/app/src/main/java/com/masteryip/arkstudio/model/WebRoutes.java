package com.masteryip.arkstudio.model;

import java.io.UnsupportedEncodingException;
import java.net.URLEncoder;

/** Hash routes of the web app (see {@code parseHash} in {@code web/js/app.js}). */
public final class WebRoutes {
    public static final String HOME = "#/home";
    public static final String MAP = "#/map";
    public static final String IDEAS = "#/research/idea";
    public static final String PLAN = "#/research/story";
    public static final String BOARD = "#/execute/board";
    public static final String RUNS = "#/execute/sessions";
    public static final String GPU = "#/execute/gpu";
    public static final String FILES = "#/execute/files";
    public static final String OUTPUT = "#/output/assets";
    public static final String ASSISTANT = "#/assistant";
    public static final String SETTINGS = "#/settings";
    public static final String EDITOR = "#/editor";

    private WebRoutes() {
    }

    /** {@code encodeURIComponent} semantics (JS) for route keys. */
    public static String encodeComponent(String value) {
        try {
            return URLEncoder.encode(value == null ? "" : value, "UTF-8")
                    .replace("+", "%20").replace("%21", "!").replace("%27", "'")
                    .replace("%28", "(").replace("%29", ")").replace("%7E", "~");
        } catch (UnsupportedEncodingException impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    /** {@code #/editor/unit%3A<uid>} — the editor opened on one unit. */
    public static String editorUnit(String uid) {
        return EDITOR + "/" + encodeComponent("unit:" + uid);
    }

    /** Absolute web URL for a route on the given server base. */
    public static String url(String base, String route) {
        String root = base == null ? "" : base;
        int hash = root.indexOf('#');
        if (hash >= 0) root = root.substring(0, hash);
        if (!root.endsWith("/")) root = root + "/";
        String r = route == null || route.isEmpty() ? HOME : route;
        if (!r.startsWith("#")) r = "#" + (r.startsWith("/") ? r : "/" + r);
        return root + r;
    }
}
