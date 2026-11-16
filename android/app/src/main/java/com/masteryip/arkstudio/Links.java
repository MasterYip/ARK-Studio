package com.masteryip.arkstudio;

import android.content.Intent;
import android.net.Uri;
import android.widget.Toast;

import com.masteryip.arkstudio.data.PairingLink;

import java.util.Locale;
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
        String lower = value.toLowerCase(Locale.ROOT);
        if (lower.startsWith("mailto:") || lower.startsWith("tel:")) {
            external(a, value);
            return;
        }
        String url = absolute(a, value);
        if (PairingLink.sameOrigin(a.session.serverUrl, url)) {
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
        try {
            a.startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url)));
        } catch (Exception failed) {
            Toast.makeText(a, R.string.link_failed, Toast.LENGTH_SHORT).show();
        }
    }
}
