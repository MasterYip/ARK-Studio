package com.masteryip.arkstudio;

import com.masteryip.arkstudio.data.LinkPolicy;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Decides where a link from a native screen opens. */
final class Links {
    private static final Pattern MEDIA_RAW = Pattern.compile("/api/media/(\\d+)/raw");
    private static final Pattern UNIT_REF = Pattern.compile("^(?:unit:)?([A-Z]{1,6}-\\d{1,8})$");

    private Links() {
    }

    /** Absolute URL for a server-relative path. */
    static String absolute(BaseActivity a, String href) {
        if (href == null) return "";
        String value = href.trim();
        if (value.startsWith("http://") || value.startsWith("https://")) return value;
        String base = a.session.serverUrl.endsWith("/") ? a.session.serverUrl : a.session.serverUrl + "/";
        while (value.startsWith("/")) value = value.substring(1);
        return base + value;
    }

    static void open(BaseActivity a, String href) {
        if (href == null || href.trim().isEmpty()) return;
        String value = href.trim();
        Matcher unit = UNIT_REF.matcher(value);
        if (unit.matches()) {
            a.startActivity(NodeActivity.intent(a, unit.group(1)));
            return;
        }
        if (value.startsWith("#")) {
            a.openWeb(value);
            return;
        }
        String scheme = LinkPolicy.scheme(value);
        if (!scheme.isEmpty() && !"http".equals(scheme) && !"https".equals(scheme)) {
            // mailto: to a mail app, file: shows the path, other schemes are dropped.
            ExternalLinks.handle(a, value, LinkPolicy.decide(value, true, a.session.serverUrl));
            return;
        }
        String url = absolute(a, value);
        if (LinkPolicy.decide(url, true, a.session.serverUrl) == LinkPolicy.Action.STAY) {
            Matcher media = MEDIA_RAW.matcher(url);
            if (media.find()) {
                a.startActivity(MediaActivity.intent(a, url, "", null));
                return;
            }
            int hash = url.indexOf('#');
            a.openWeb(hash >= 0 ? url.substring(hash) : null);
            return;
        }
        external(a, url);
    }

    /** Other origins always go to the system browser, never into the signed-in WebView. */
    static void external(BaseActivity a, String url) {
        ExternalLinks.browser(a, url);
    }
}
