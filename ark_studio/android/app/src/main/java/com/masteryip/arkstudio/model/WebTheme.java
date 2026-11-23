package com.masteryip.arkstudio.model;

import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The web app's theme as reported by the page ({@code window.ARKAndroidTheme.setTheme(theme, bg)}
 * from {@code web/js/state.js}, or the snippet WebActivity runs after each load): the
 * status and navigation bars take the page background and light or dark icons.
 * Pure Java so it can be unit-tested on the JVM.
 */
public final class WebTheme {
    private static final Pattern HEX = Pattern.compile("#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})");
    private static final Pattern RGB = Pattern.compile(
            "rgba?\\(\\s*(\\d{1,3})[\\s,]+(\\d{1,3})[\\s,]+(\\d{1,3})(?:[\\s,/]+([0-9.]+%?))?\\s*\\)");

    /** Opaque ARGB bar colour. */
    public final int color;
    /** Dark page: light bar icons. */
    public final boolean dark;

    private WebTheme(int color, boolean dark) {
        this.color = color;
        this.dark = dark;
    }

    /**
     * {@code theme} is "dark" or "light" (anything else: decided by the colour);
     * {@code background} is a CSS colour ({@code #rgb}, {@code #rrggbb},
     * {@code rgb()}). Returns {@code null} when neither says anything usable.
     */
    public static WebTheme of(String theme, String background, int darkFallback, int lightFallback) {
        Integer parsed = parse(background);
        String mode = theme == null ? "" : theme.trim().toLowerCase(Locale.ROOT);
        boolean known = mode.equals("dark") || mode.equals("light");
        if (parsed == null && !known) return null;
        boolean dark = known ? mode.equals("dark") : luminance(parsed) < 0.5;
        int color = parsed != null ? parsed : (dark ? darkFallback : lightFallback);
        return new WebTheme(color, dark);
    }

    /** CSS colour to opaque ARGB, or {@code null}. Alpha is ignored (bars are opaque). */
    public static Integer parse(String css) {
        if (css == null) return null;
        String text = css.trim();
        Matcher hex = HEX.matcher(text);
        if (hex.matches()) {
            String h = hex.group(1);
            if (h.length() == 3) {
                h = "" + h.charAt(0) + h.charAt(0) + h.charAt(1) + h.charAt(1) + h.charAt(2) + h.charAt(2);
            }
            return 0xFF000000 | Integer.parseInt(h.substring(0, 6), 16);
        }
        Matcher rgb = RGB.matcher(text.toLowerCase(Locale.ROOT));
        if (rgb.matches()) {
            int r = Integer.parseInt(rgb.group(1));
            int g = Integer.parseInt(rgb.group(2));
            int b = Integer.parseInt(rgb.group(3));
            if (r > 255 || g > 255 || b > 255) return null;
            String alpha = rgb.group(4);
            if (alpha != null && Double.parseDouble(alpha.replace("%", "")) == 0) return null; // transparent
            return 0xFF000000 | (r << 16) | (g << 8) | b;
        }
        return null;
    }

    /** Relative luminance (sRGB), 0 black … 1 white. */
    public static double luminance(int argb) {
        return 0.2126 * channel((argb >> 16) & 0xFF) + 0.7152 * channel((argb >> 8) & 0xFF)
                + 0.0722 * channel(argb & 0xFF);
    }

    private static double channel(int value) {
        double c = value / 255.0;
        return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
    }

    /**
     * Runs after each page load: reports the current theme even when the page
     * set it before the bridge existed. Reads the {@code data-theme} attribute,
     * the {@code --bg} variable and the theme-color meta tag.
     */
    public static final String PROBE_JS = "(function(){try{if(!window.ARKAndroidTheme)return;"
            + "var d=document.documentElement;"
            + "var c=getComputedStyle(d).getPropertyValue('--bg').trim();"
            + "if(!c){var m=document.querySelector('meta[name=\"theme-color\"]');c=m?m.content:'';}"
            + "if(!c&&document.body){c=getComputedStyle(document.body).backgroundColor;}"
            + "window.ARKAndroidTheme.setTheme(d.dataset.theme||'',c||'');"
            + "}catch(e){}})()";
}
