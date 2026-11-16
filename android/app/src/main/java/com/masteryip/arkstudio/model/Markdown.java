package com.masteryip.arkstudio.model;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Small, dependency-free Markdown reader for phone rendering.
 *
 * Covers what ARK bodies and agent replies mostly use: ATX headings, fenced
 * code, lists (nested by indent), quotes, rules, tables (kept as monospace
 * text) and inline code/bold/italic/strike/links. Mermaid, math and raw HTML
 * are detected by {@link #needsWebRender} so the UI can offer the web view.
 */
public final class Markdown {
    private Markdown() {
    }

    public enum Kind { HEADING, PARAGRAPH, CODE, LIST_ITEM, QUOTE, RULE, TABLE }

    public static final class Block {
        public final Kind kind;
        public final int level;      // heading level, or list depth
        public final String marker;  // list marker ("•", "1.", "☐", "☑")
        public final String lang;    // code language
        public final Inline inline;  // formatted text (not for CODE/TABLE/RULE)
        public final String raw;     // code/table text

        Block(Kind kind, int level, String marker, String lang, Inline inline, String raw) {
            this.kind = kind;
            this.level = level;
            this.marker = marker;
            this.lang = lang;
            this.inline = inline;
            this.raw = raw;
        }
    }

    public static final class Span {
        public static final int BOLD = 1;
        public static final int ITALIC = 2;
        public static final int CODE = 3;
        public static final int STRIKE = 4;
        public static final int LINK = 5;

        public final int start;
        public final int end;
        public final int type;
        public final String href;

        Span(int start, int end, int type, String href) {
            this.start = start;
            this.end = end;
            this.type = type;
            this.href = href;
        }
    }

    public static final class Inline {
        public final String text;
        public final List<Span> spans;

        Inline(String text, List<Span> spans) {
            this.text = text;
            this.spans = spans;
        }
    }

    private static final Pattern HEADING = Pattern.compile("^(#{1,6})\\s+(.*?)\\s*#*\\s*$");
    private static final Pattern FENCE = Pattern.compile("^\\s*(```+|~~~+)\\s*([\\w+#.-]*).*$");
    private static final Pattern BULLET = Pattern.compile("^(\\s*)[-*+]\\s+(.*)$");
    private static final Pattern ORDERED = Pattern.compile("^(\\s*)(\\d{1,9})[.)]\\s+(.*)$");
    private static final Pattern TASK = Pattern.compile("^\\[( |x|X)\\]\\s+(.*)$");
    private static final Pattern RULE = Pattern.compile("^\\s{0,3}([-*_])(\\s*\\1){2,}\\s*$");
    private static final Pattern TABLE_ROW = Pattern.compile("^\\s*\\|.*\\|\\s*$");

    public static List<Block> parse(String source) {
        List<Block> blocks = new ArrayList<>();
        if (source == null) return blocks;
        String[] lines = source.replace("\r\n", "\n").replace('\r', '\n').split("\n", -1);
        StringBuilder paragraph = new StringBuilder();
        StringBuilder quote = new StringBuilder();
        int i = 0;
        while (i < lines.length) {
            String line = lines[i];
            Matcher fence = FENCE.matcher(line);
            if (fence.matches()) {
                flush(blocks, paragraph, quote);
                String marker = fence.group(1);
                String lang = fence.group(2).toLowerCase(Locale.ROOT);
                StringBuilder code = new StringBuilder();
                i++;
                while (i < lines.length && !lines[i].trim().startsWith(marker)) {
                    if (code.length() > 0) code.append('\n');
                    code.append(lines[i]);
                    i++;
                }
                i++; // closing fence (or EOF)
                blocks.add(new Block(Kind.CODE, 0, null, lang, null, code.toString()));
                continue;
            }
            if (line.trim().isEmpty()) {
                flush(blocks, paragraph, quote);
                i++;
                continue;
            }
            Matcher heading = HEADING.matcher(line);
            if (heading.matches()) {
                flush(blocks, paragraph, quote);
                blocks.add(new Block(Kind.HEADING, heading.group(1).length(), null, null,
                        inline(heading.group(2)), null));
                i++;
                continue;
            }
            if (RULE.matcher(line).matches() && paragraph.length() == 0) {
                flush(blocks, paragraph, quote);
                blocks.add(new Block(Kind.RULE, 0, null, null, null, null));
                i++;
                continue;
            }
            if (TABLE_ROW.matcher(line).matches()) {
                flush(blocks, paragraph, quote);
                StringBuilder table = new StringBuilder();
                while (i < lines.length && TABLE_ROW.matcher(lines[i]).matches()) {
                    String row = lines[i].trim();
                    if (!row.replaceAll("[|:\\-\\s]", "").isEmpty()) {
                        if (table.length() > 0) table.append('\n');
                        table.append(tableRow(row));
                    }
                    i++;
                }
                blocks.add(new Block(Kind.TABLE, 0, null, null, null, table.toString()));
                continue;
            }
            if (line.trim().startsWith(">")) {
                if (paragraph.length() > 0) flush(blocks, paragraph, null);
                String body = line.trim().substring(1);
                if (body.startsWith(" ")) body = body.substring(1);
                if (quote.length() > 0) quote.append(' ');
                quote.append(body);
                i++;
                continue;
            }
            Matcher bullet = BULLET.matcher(line);
            Matcher ordered = ORDERED.matcher(line);
            if (bullet.matches() || ordered.matches()) {
                flush(blocks, paragraph, quote);
                boolean isBullet = bullet.matches();
                String indent = isBullet ? bullet.group(1) : ordered.group(1);
                String text = isBullet ? bullet.group(2) : ordered.group(3);
                String marker = isBullet ? "•" : ordered.group(2) + ".";
                Matcher task = TASK.matcher(text);
                if (task.matches()) {
                    marker = task.group(1).trim().isEmpty() ? "☐" : "☑";
                    text = task.group(2);
                }
                int depth = Math.min(4, indent.replace("\t", "    ").length() / 2);
                StringBuilder item = new StringBuilder(text);
                i++;
                // Lazy continuation lines belong to the item.
                while (i < lines.length && !lines[i].trim().isEmpty()
                        && Character.isWhitespace(lines[i].isEmpty() ? 'x' : lines[i].charAt(0))
                        && !BULLET.matcher(lines[i]).matches() && !ORDERED.matcher(lines[i]).matches()
                        && !FENCE.matcher(lines[i]).matches()) {
                    item.append(' ').append(lines[i].trim());
                    i++;
                }
                blocks.add(new Block(Kind.LIST_ITEM, depth, marker, null, inline(item.toString()), null));
                continue;
            }
            if (quote.length() > 0) flush(blocks, null, quote);
            if (paragraph.length() > 0) {
                // A trailing double space or backslash is a hard break.
                boolean hard = paragraph.toString().endsWith("  ") || paragraph.toString().endsWith("\\");
                if (paragraph.toString().endsWith("\\")) paragraph.setLength(paragraph.length() - 1);
                paragraph.append(hard ? "\n" : " ");
            }
            paragraph.append(line.trim());
            i++;
        }
        flush(blocks, paragraph, quote);
        return blocks;
    }

    private static String tableRow(String row) {
        String inner = row.substring(1, row.length() - 1);
        String[] cells = inner.split("\\|", -1);
        StringBuilder out = new StringBuilder();
        for (int c = 0; c < cells.length; c++) {
            if (c > 0) out.append("  │  ");
            out.append(cells[c].trim());
        }
        return out.toString();
    }

    private static void flush(List<Block> blocks, StringBuilder paragraph, StringBuilder quote) {
        if (paragraph != null && paragraph.length() > 0) {
            blocks.add(new Block(Kind.PARAGRAPH, 0, null, null, inline(paragraph.toString().trim()), null));
            paragraph.setLength(0);
        }
        if (quote != null && quote.length() > 0) {
            blocks.add(new Block(Kind.QUOTE, 0, null, null, inline(quote.toString().trim()), null));
            quote.setLength(0);
        }
    }

    // ---------------------------------------------------------------- inline

    private static final Pattern BARE_URL = Pattern.compile("https?://[^\\s<>()\\[\\]]+[^\\s<>()\\[\\].,;:!?'\"]");

    public static Inline inline(String source) {
        StringBuilder out = new StringBuilder();
        List<Span> spans = new ArrayList<>();
        parseInline(source == null ? "" : source, out, spans);
        linkifyBare(out, spans);
        return new Inline(out.toString(), spans);
    }

    private static void parseInline(String s, StringBuilder out, List<Span> spans) {
        int i = 0;
        while (i < s.length()) {
            char c = s.charAt(i);
            if (c == '\\' && i + 1 < s.length() && "\\`*_{}[]()#+-.!~|<>".indexOf(s.charAt(i + 1)) >= 0) {
                out.append(s.charAt(i + 1));
                i += 2;
                continue;
            }
            if (c == '`') {
                int ticks = 1;
                while (i + ticks < s.length() && s.charAt(i + ticks) == '`') ticks++;
                String fence = s.substring(i, i + ticks);
                int close = s.indexOf(fence, i + ticks);
                if (close > 0) {
                    int start = out.length();
                    out.append(s.substring(i + ticks, close).trim());
                    spans.add(new Span(start, out.length(), Span.CODE, null));
                    i = close + ticks;
                    continue;
                }
            }
            if ((c == '!' && i + 1 < s.length() && s.charAt(i + 1) == '[') || c == '[') {
                boolean image = c == '!';
                int open = image ? i + 1 : i;
                int closeText = matching(s, open, '[', ']');
                if (closeText > 0 && closeText + 1 < s.length() && s.charAt(closeText + 1) == '(') {
                    int closeUrl = matching(s, closeText + 1, '(', ')');
                    if (closeUrl > 0) {
                        String label = s.substring(open + 1, closeText);
                        String href = s.substring(closeText + 2, closeUrl).trim();
                        int space = href.indexOf(' ');
                        if (space > 0) href = href.substring(0, space);
                        if (href.startsWith("<") && href.endsWith(">")) href = href.substring(1, href.length() - 1);
                        int start = out.length();
                        if (image) {
                            out.append("[").append(label.isEmpty() ? "image" : label).append("]");
                        } else {
                            parseInline(label, out, spans);
                        }
                        spans.add(new Span(start, out.length(), Span.LINK, href));
                        i = closeUrl + 1;
                        continue;
                    }
                }
            }
            if (c == '<') {
                int close = s.indexOf('>', i);
                if (close > 0) {
                    String inner = s.substring(i + 1, close);
                    if (inner.matches("(?i)(https?://|mailto:)[^\\s]+")) {
                        int start = out.length();
                        out.append(inner);
                        spans.add(new Span(start, out.length(), Span.LINK, inner));
                        i = close + 1;
                        continue;
                    }
                }
            }
            if ((c == '*' || c == '_' || c == '~') && i + 1 < s.length()) {
                boolean doubled = s.charAt(i + 1) == c;
                if (c == '~' && !doubled) {
                    out.append(c);
                    i++;
                    continue;
                }
                String marker = doubled ? "" + c + c : "" + c;
                int from = i + marker.length();
                // Intraword underscores (snake_case) are literal.
                boolean leftOk = c != '_' || i == 0 || !Character.isLetterOrDigit(s.charAt(i - 1));
                if (leftOk && from < s.length() && !Character.isWhitespace(s.charAt(from))) {
                    int close = findClose(s, marker, from);
                    if (close > from) {
                        int start = out.length();
                        parseInline(s.substring(from, close), out, spans);
                        int type = c == '~' ? Span.STRIKE : doubled ? Span.BOLD : Span.ITALIC;
                        spans.add(new Span(start, out.length(), type, null));
                        i = close + marker.length();
                        continue;
                    }
                }
            }
            out.append(c);
            i++;
        }
    }

    private static int findClose(String s, String marker, int from) {
        int at = from;
        while (true) {
            int close = s.indexOf(marker, at);
            if (close < 0) return -1;
            boolean beforeOk = !Character.isWhitespace(s.charAt(close - 1));
            boolean afterOk = marker.charAt(0) != '_' || close + marker.length() >= s.length()
                    || !Character.isLetterOrDigit(s.charAt(close + marker.length()));
            // "**" must not close a single "*" run and vice versa.
            boolean exact = marker.length() == 2
                    || close + 1 >= s.length() || s.charAt(close + 1) != marker.charAt(0);
            if (beforeOk && afterOk && exact) return close;
            at = close + marker.length();
        }
    }

    private static int matching(String s, int open, char left, char right) {
        int depth = 0;
        for (int i = open; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c == '\\') {
                i++;
                continue;
            }
            if (c == left) depth++;
            else if (c == right && --depth == 0) return i;
        }
        return -1;
    }

    private static void linkifyBare(StringBuilder out, List<Span> spans) {
        Matcher m = BARE_URL.matcher(out);
        while (m.find()) {
            boolean covered = false;
            for (Span span : spans) {
                if ((span.type == Span.LINK || span.type == Span.CODE)
                        && m.start() < span.end && m.end() > span.start) {
                    covered = true;
                    break;
                }
            }
            if (!covered) spans.add(new Span(m.start(), m.end(), Span.LINK, m.group()));
        }
    }

    // ---------------------------------------------------------- web fallback

    private static final Pattern HTML_BLOCK = Pattern.compile(
            "(?im)^\\s*<(div|table|iframe|details|svg|img|video|audio|section|figure|p|span|br|style|script)\\b");
    private static final Pattern MATH = Pattern.compile("(?s)\\$\\$.+?\\$\\$|\\\\\\[.+?\\\\\\]|\\\\\\(.+?\\\\\\)");

    /** True when the body has Mermaid, LaTeX math or raw HTML that only the web renderer handles. */
    public static boolean needsWebRender(String body) {
        if (body == null || body.isEmpty()) return false;
        for (Block block : parse(body)) {
            if (block.kind == Kind.CODE && (block.lang.equals("mermaid") || block.lang.equals("math")
                    || block.lang.equals("latex") || block.lang.equals("katex"))) {
                return true;
            }
        }
        String withoutCode = body.replaceAll("(?s)```.*?```", "");
        return MATH.matcher(withoutCode).find() || HTML_BLOCK.matcher(withoutCode).find();
    }
}
