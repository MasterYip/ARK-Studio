package com.masteryip.arkstudio.ui;

import android.content.Context;
import android.graphics.Typeface;
import android.text.SpannableString;
import android.text.Spanned;
import android.text.method.LinkMovementMethod;
import android.text.style.BackgroundColorSpan;
import android.text.style.ForegroundColorSpan;
import android.text.style.RelativeSizeSpan;
import android.text.style.StrikethroughSpan;
import android.text.style.StyleSpan;
import android.text.style.TypefaceSpan;
import android.text.style.URLSpan;
import android.view.View;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.model.Markdown;
import com.masteryip.arkstudio.model.ReviewNote;

import java.util.List;
import java.util.Map;

/** Renders {@link Markdown} blocks as native views. */
public final class MarkdownView {
    /** Resolves relative links (e.g. {@code /api/media/3/raw}) and decides where they open. */
    public interface LinkHandler {
        void open(String href);
    }

    private MarkdownView() {
    }

    /** Tag prefix of anchorable blocks (headings, "{#id}" paragraphs): {@code findViewWithTag(ANCHOR_TAG + id)}. */
    public static final String ANCHOR_TAG = "anchor:";

    public static LinearLayout render(Context c, String source, LinkHandler links) {
        LinearLayout column = Ui.column(c);
        List<Markdown.Block> blocks = Markdown.parse(source);
        Map<Integer, String> ids = ReviewNote.blockIds(blocks);
        for (int i = 0; i < blocks.size(); i++) {
            View view = blockView(c, blocks.get(i), links);
            String id = ids.get(i);
            if (id != null) {
                view.setTag(ANCHOR_TAG + id);
                // The "{#id}" marker is for anchors only; the web hides it too.
                if (view instanceof TextView && !ReviewNote.explicitId(blocks.get(i).inline.text).isEmpty()) {
                    TextView text = (TextView) view;
                    CharSequence value = text.getText();
                    String plain = value.toString();
                    String kept = ReviewNote.withoutExplicitId(plain);
                    text.setText(value.subSequence(0, Math.min(kept.length(), value.length())));
                }
            }
            column.addView(view);
        }
        return column;
    }

    public static CharSequence spanned(Context c, Markdown.Inline inline, LinkHandler links) {
        SpannableString text = new SpannableString(inline.text);
        for (Markdown.Span span : inline.spans) {
            if (span.start < 0 || span.end > inline.text.length() || span.start >= span.end) continue;
            int flags = Spanned.SPAN_EXCLUSIVE_EXCLUSIVE;
            switch (span.type) {
                case Markdown.Span.BOLD:
                    text.setSpan(new StyleSpan(Typeface.BOLD), span.start, span.end, flags);
                    break;
                case Markdown.Span.ITALIC:
                    text.setSpan(new StyleSpan(Typeface.ITALIC), span.start, span.end, flags);
                    break;
                case Markdown.Span.STRIKE:
                    text.setSpan(new StrikethroughSpan(), span.start, span.end, flags);
                    break;
                case Markdown.Span.CODE:
                    text.setSpan(new TypefaceSpan("monospace"), span.start, span.end, flags);
                    text.setSpan(new BackgroundColorSpan(Ui.codeBg(c)), span.start, span.end, flags);
                    text.setSpan(new RelativeSizeSpan(0.92f), span.start, span.end, flags);
                    break;
                case Markdown.Span.LINK:
                    final String href = span.href;
                    text.setSpan(new URLSpan(href) {
                        @Override
                        public void onClick(View widget) {
                            if (links != null) links.open(href);
                        }
                    }, span.start, span.end, flags);
                    text.setSpan(new ForegroundColorSpan(Ui.accent(c)), span.start, span.end, flags);
                    break;
                default:
                    break;
            }
        }
        return text;
    }

    private static View blockView(Context c, Markdown.Block block, LinkHandler links) {
        switch (block.kind) {
            case HEADING: {
                float size = block.level <= 1 ? 22 : block.level == 2 ? 19 : block.level == 3 ? 17 : 15;
                TextView view = Ui.label(c, spanned(c, block.inline, links), size, Ui.text(c), true);
                view.setPadding(0, Ui.dp(c, 10), 0, Ui.dp(c, 4));
                return linkify(view);
            }
            case CODE:
            case TABLE: {
                HorizontalScrollView scroll = new HorizontalScrollView(c);
                scroll.setBackground(Ui.rounded(Ui.codeBg(c), Ui.border(c), c, 8));
                TextView code = Ui.label(c, block.raw, 12.5f, Ui.text(c), false);
                code.setTypeface(Typeface.MONOSPACE);
                code.setTextIsSelectable(true);
                code.setPadding(Ui.dp(c, 10), Ui.dp(c, 8), Ui.dp(c, 10), Ui.dp(c, 8));
                scroll.addView(code);
                scroll.setLayoutParams(Ui.margins(c, Ui.MATCH, Ui.WRAP, 0, 4, 0, 8));
                return scroll;
            }
            case LIST_ITEM: {
                LinearLayout row = Ui.row(c);
                row.setGravity(android.view.Gravity.TOP);
                row.setPadding(Ui.dp(c, 4 + 16 * block.level), Ui.dp(c, 2), 0, Ui.dp(c, 2));
                TextView marker = Ui.label(c, block.marker, 15, Ui.muted(c), false);
                marker.setMinWidth(Ui.dp(c, 20));
                row.addView(marker);
                TextView body = linkify(Ui.body(c, spanned(c, block.inline, links)));
                row.addView(body, Ui.lp(0, Ui.WRAP, 1));
                return row;
            }
            case QUOTE: {
                LinearLayout row = Ui.row(c);
                View bar = new View(c);
                bar.setBackgroundColor(Ui.border(c));
                row.addView(bar, Ui.lp(Ui.dp(c, 3), Ui.MATCH));
                TextView body = linkify(Ui.label(c, spanned(c, block.inline, links), 15, Ui.muted(c), false));
                body.setPadding(Ui.dp(c, 10), Ui.dp(c, 2), 0, Ui.dp(c, 2));
                row.addView(body, Ui.lp(0, Ui.WRAP, 1));
                row.setLayoutParams(Ui.margins(c, Ui.MATCH, Ui.WRAP, 0, 4, 0, 6));
                return row;
            }
            case RULE: {
                View rule = Ui.divider(c);
                rule.setLayoutParams(Ui.margins(c, Ui.MATCH, Ui.dp(c, 1), 0, 8, 0, 8));
                return rule;
            }
            case PARAGRAPH:
            default: {
                TextView view = linkify(Ui.body(c, spanned(c, block.inline, links)));
                view.setPadding(0, Ui.dp(c, 2), 0, Ui.dp(c, 6));
                return view;
            }
        }
    }

    private static TextView linkify(TextView view) {
        view.setMovementMethod(LinkMovementMethod.getInstance());
        view.setLinksClickable(true);
        return view;
    }
}
