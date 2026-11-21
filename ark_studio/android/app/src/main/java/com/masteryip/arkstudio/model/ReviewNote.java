package com.masteryip.arkstudio.model;

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Review notes, as {@code web/js/review-model.js}: the anchors of a note
 * ({@code #slug}, {@code T-0042}, {@code T-0042#slug}, {@code media:421@0:08-0:12}),
 * the note split into text runs and anchor chips, the headline number of a
 * result title, and heading ids of a body (rich-model.js anchorSlug /
 * store/anchors.go BodyAnchors) so the unit view can scroll to an anchor.
 */
public final class ReviewNote {
    private ReviewNote() {
    }

    public static final String HEADING = "heading";
    public static final String UNIT = "unit";
    public static final String MEDIA = "media";

    /** One anchor of a note. {@code start}/{@code end} are seconds, or -1. */
    public static final class Anchor {
        public final String raw;
        public final String kind;
        public final String unit;
        public final String slug;
        public final long media;
        public final double start;
        public final double end;
        public final String label;

        Anchor(String raw, String kind, String unit, String slug, long media, double start, double end, String label) {
            this.raw = raw;
            this.kind = kind;
            this.unit = unit;
            this.slug = slug;
            this.media = media;
            this.start = start;
            this.end = end;
            this.label = label;
        }

        Anchor withLabel(String value) {
            return new Anchor(raw, kind, unit, slug, media, start, end, value == null ? "" : value.trim());
        }

        /** The unit this anchor opens ("" for media, or the reviewed unit for {@code #slug}). */
        public String unitOr(String selfUid) {
            if (MEDIA.equals(kind)) return "";
            return unit.isEmpty() ? (selfUid == null ? "" : selfUid) : unit;
        }
    }

    /** A run of the note: plain {@code text}, or an {@code anchor} chip. */
    public static final class Part {
        public final String text;
        public final Anchor anchor;

        Part(String text, Anchor anchor) {
            this.text = text;
            this.anchor = anchor;
        }
    }

    private static final Pattern LINK = Pattern.compile("\\[([^\\]\\n]*)\\]\\(\\s*<?([^)\\s>]+)>?(?:\\s+\"[^\"]*\")?\\s*\\)");
    private static final Pattern BARE_MEDIA = Pattern.compile("(^|[^\\w/(:])(media:\\d+(?:@[0-9:.]+(?:-[0-9:.]+)?)?)");
    private static final Pattern UNIT_RE = Pattern.compile("^([A-Z]{1,4}-\\d{1,9})(?:#(.+))?$");
    private static final Pattern MEDIA_RE = Pattern.compile("^media:(\\d+)(?:@([0-9:.]+)(?:-([0-9:.]+))?)?$");
    private static final Pattern MARKUP = Pattern.compile("(\\*\\*|__|`|~~)");

    /** "8", "0:08", "1:02:03" -> seconds, or -1. */
    public static double parseClock(String text) {
        if (text == null || text.isEmpty()) return -1;
        String[] parts = text.split(":", -1);
        if (parts.length > 3) return -1;
        double total = 0;
        for (int i = 0; i < parts.length; i++) {
            if (parts[i].isEmpty()) return -1;
            double v;
            try {
                v = Double.parseDouble(parts[i]);
            } catch (NumberFormatException e) {
                return -1;
            }
            if (v < 0 || (i > 0 && v >= 60)) return -1;
            total = total * 60 + v;
        }
        return total;
    }

    /** Seconds -> "0:08" / "1:02:03". */
    public static String clockLabel(double seconds) {
        long s = Math.max(0, (long) Math.floor(seconds));
        long h = s / 3600, m = (s % 3600) / 60, r = s % 60;
        return h > 0 ? String.format(Locale.ROOT, "%d:%02d:%02d", h, m, r) : String.format(Locale.ROOT, "%d:%02d", m, r);
    }

    /** One anchor target, or null. */
    public static Anchor parse(String raw) {
        String text = raw == null ? "" : raw.trim();
        if (text.startsWith("#") && text.length() > 1) return new Anchor(text, HEADING, "", text.substring(1), 0, -1, -1, "");
        Matcher m = MEDIA_RE.matcher(text);
        if (m.matches()) {
            double start = m.group(2) == null ? -1 : parseClock(m.group(2));
            double end = m.group(3) == null ? -1 : parseClock(m.group(3));
            if ((m.group(2) != null && start < 0) || (m.group(3) != null && (end < 0 || end < start))) return null;
            return new Anchor(text, MEDIA, "", "", Long.parseLong(m.group(1)), start, end, "");
        }
        m = UNIT_RE.matcher(text);
        if (m.matches()) {
            return m.group(2) != null
                    ? new Anchor(text, HEADING, m.group(1), m.group(2), 0, -1, -1, "")
                    : new Anchor(text, UNIT, m.group(1), "", 0, -1, -1, "");
        }
        return null;
    }

    private static final class Token {
        final int index;
        final int length;
        final String raw;
        final String label;

        Token(int index, int length, String raw, String label) {
            this.index = index;
            this.length = length;
            this.raw = raw;
            this.label = label;
        }
    }

    /** The note as runs: text and anchor chips; other Markdown links keep their text. */
    public static List<Part> parts(String note) {
        String text = note == null ? "" : note;
        List<Token> tokens = new ArrayList<>();
        Matcher link = LINK.matcher(text);
        while (link.find()) tokens.add(new Token(link.start(), link.end() - link.start(), link.group(2), link.group(1)));
        Matcher bare = BARE_MEDIA.matcher(text);
        while (bare.find()) {
            int index = bare.start(2);
            boolean inside = false;
            for (Token t : tokens) {
                if (index >= t.index && index < t.index + t.length) {
                    inside = true;
                    break;
                }
            }
            if (!inside) tokens.add(new Token(index, bare.group(2).length(), bare.group(2), ""));
        }
        Collections.sort(tokens, (a, b) -> Integer.compare(a.index, b.index));
        List<Part> out = new ArrayList<>();
        int last = 0;
        for (Token t : tokens) {
            addText(out, text.substring(last, t.index));
            Anchor anchor = parse(t.raw);
            if (anchor != null) out.add(new Part(null, anchor.withLabel(t.label)));
            else addText(out, t.label.isEmpty() ? t.raw : t.label);
            last = t.index + t.length;
        }
        addText(out, text.substring(last));
        return out;
    }

    private static void addText(List<Part> out, String value) {
        if (value == null || value.isEmpty()) return;
        out.add(new Part(MARKUP.matcher(value).replaceAll(""), null));
    }

    /** A short label for an anchor chip without link text. */
    public static String label(Anchor anchor, String selfUid) {
        if (anchor == null) return "";
        if (!anchor.label.isEmpty()) return anchor.label;
        if (MEDIA.equals(anchor.kind)) {
            String range = anchor.start >= 0
                    ? " @" + clockLabel(anchor.start) + (anchor.end >= 0 ? "–" + clockLabel(anchor.end) : "") : "";
            return "media:" + anchor.media + range;
        }
        String unit = !anchor.unit.isEmpty() && !anchor.unit.equals(selfUid) ? anchor.unit : "";
        if (HEADING.equals(anchor.kind)) return unit.isEmpty() ? "§ " + anchor.slug : unit + " › " + anchor.slug;
        return unit.isEmpty() ? anchor.raw : unit;
    }

    // ------------------------------------------------------ headline numbers

    private static final String NUMBER = "(?<![\\w.-])[~≈<>≤≥]?\\d+(?:[.,]\\d+)?(?:\\s?[–-]\\s?\\d+(?:[.,]\\d+)?)?";
    private static final Pattern[] NUMBER_PATTERNS = {
            Pattern.compile(NUMBER + "\\s?%"),
            Pattern.compile(NUMBER + "\\s?(?:ms|µs|us|ns|s|sec|min|h)\\b"),
            Pattern.compile(NUMBER + "\\s?(?:[KMGT]i?B|[kKMG]\\b|mm|cm|m\\b)"),
            Pattern.compile(NUMBER + "\\s?[x×](?![\\d])"),
            Pattern.compile("(?<![\\w.-])[~≈]?\\d+\\.\\d+"),
    };

    /** The headline number of a result title ("" when it has none). */
    public static String keyNumber(String title) {
        String text = title == null ? "" : title;
        for (Pattern p : NUMBER_PATTERNS) {
            Matcher m = p.matcher(text);
            if (m.find()) return m.group().replaceAll("\\s+", " ").trim();
        }
        return "";
    }

    // ------------------------------------------------------ heading ids

    private static final Pattern EXPLICIT_ID = Pattern.compile("\\s*\\{#([A-Za-z0-9\\p{L}][\\w\\p{L}\\p{N}.-]*)\\}\\s*$");

    /** rich-model.js anchorSlug: lower-case, runs of non letters/digits -> "-", trimmed. */
    public static String slug(String text) {
        String lower = (text == null ? "" : text).toLowerCase(Locale.ROOT);
        StringBuilder out = new StringBuilder();
        boolean dash = false;
        for (int i = 0; i < lower.length(); ) {
            int cp = lower.codePointAt(i);
            i += Character.charCount(cp);
            if (Character.isLetterOrDigit(cp)) {
                if (dash && out.length() > 0) out.append('-');
                dash = false;
                out.appendCodePoint(cp);
            } else {
                dash = true;
            }
        }
        return out.toString();
    }

    /** "Title {#id}" -> "id" (or "" when the text carries no explicit id). */
    public static String explicitId(String text) {
        Matcher m = EXPLICIT_ID.matcher(text == null ? "" : text);
        return m.find() ? m.group(1) : "";
    }

    /** The text without a trailing "{#id}". */
    public static String withoutExplicitId(String text) {
        return EXPLICIT_ID.matcher(text == null ? "" : text).replaceFirst("");
    }

    /**
     * Ids of the anchorable blocks, by block index: a heading gets its slug
     * ("-1", "-2" for repeats) or its explicit id; a paragraph only an explicit id.
     */
    public static Map<Integer, String> blockIds(List<Markdown.Block> blocks) {
        Map<Integer, String> out = new HashMap<>();
        Map<String, Integer> counts = new HashMap<>();
        for (int i = 0; i < blocks.size(); i++) {
            Markdown.Block b = blocks.get(i);
            if (b.inline == null) continue;
            String explicit = explicitId(b.inline.text);
            if (b.kind == Markdown.Kind.HEADING) {
                if (!explicit.isEmpty()) {
                    out.put(i, explicit);
                    continue;
                }
                String s = slug(b.inline.text);
                if (s.isEmpty()) continue;
                int n = counts.containsKey(s) ? counts.get(s) : 0;
                counts.put(s, n + 1);
                out.put(i, n > 0 ? s + "-" + n : s);
            } else if (!explicit.isEmpty()) {
                out.put(i, explicit);
            }
        }
        return out;
    }
}
