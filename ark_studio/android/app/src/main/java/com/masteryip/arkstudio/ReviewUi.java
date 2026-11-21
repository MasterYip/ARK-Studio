package com.masteryip.arkstudio;

import android.app.AlertDialog;
import android.content.Context;
import android.graphics.Typeface;
import android.text.SpannableStringBuilder;
import android.text.Spanned;
import android.text.TextPaint;
import android.text.method.LinkMovementMethod;
import android.text.style.BackgroundColorSpan;
import android.text.style.ClickableSpan;
import android.text.style.ForegroundColorSpan;
import android.text.style.StyleSpan;
import android.view.Gravity;
import android.view.View;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.HorizontalScrollView;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.model.NeedsCards;
import com.masteryip.arkstudio.model.ReviewNote;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Images;
import com.masteryip.arkstudio.ui.Labels;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * Review requests on native screens, the same model as the web's Needs you
 * (needs-ui.js): the "Review this" note with tappable anchors, the round and
 * the previous round, the changes since the last review, member tables, and
 * the Accept / Send back / Comment actions (comment rows plus a status change,
 * or a review group decision).
 */
final class ReviewUi {
    interface Done {
        void run();
    }

    private interface TextDone {
        void run(String text);
    }

    private ReviewUi() {
    }

    static String str(JSONObject o, String key) {
        return NeedsCards.str(o, key);
    }

    static String outcome(Context c, String value) {
        switch (value) {
            case "accepted": return c.getString(R.string.needs_outcome_accepted);
            case "sent-back": case "sent_back": return c.getString(R.string.needs_outcome_sent_back);
            case "decided": return c.getString(R.string.needs_outcome_decided);
            case "dismissed": return c.getString(R.string.needs_outcome_dismissed);
            default: return value;
        }
    }

    /** "Since your last review · +3 −1 · 2 new files", or "" without changes data. */
    static String changesText(Context c, JSONObject changes, JSONObject base) {
        int[] sum = NeedsCards.changes(changes);
        if (sum == null) return "";
        String head = c.getString("review".equals(str(base, "kind")) ? R.string.review_since_review : R.string.review_since_entered);
        if (NeedsCards.empty(sum)) return head + " · " + c.getString(R.string.review_no_changes);
        StringBuilder out = new StringBuilder(head);
        if (sum[0] != 0 || sum[1] != 0) out.append(" · +").append(sum[0]).append(" −").append(sum[1]);
        if (sum[2] != 0) out.append(" · ").append(c.getString(R.string.review_new_files, sum[2]));
        return out.toString();
    }

    /** "3 × Evidence" / "3 units". */
    static String countLabel(Context c, List<JSONObject> members) {
        Set<String> types = new LinkedHashSet<>();
        for (JSONObject m : members) types.add(m.optString("type"));
        return types.size() == 1
                ? c.getString(R.string.review_n_of_type, members.size(), Labels.of(c, types.iterator().next()))
                : c.getString(R.string.review_n_units, members.size());
    }

    static TextView roundChip(Context c, int round) {
        return Ui.chip(c, c.getString(R.string.review_round, Math.max(1, round)), Ui.muted(c), Ui.codeBg(c));
    }

    // ------------------------------------------------------------- note

    /** A left-barred box; {@code color} is the bar. */
    static LinearLayout barBox(Context c, int color, int fill) {
        LinearLayout box = Ui.column(c);
        android.graphics.drawable.GradientDrawable bg = Ui.rounded(fill, 0, c, 6);
        android.graphics.drawable.GradientDrawable bar = new android.graphics.drawable.GradientDrawable();
        bar.setColor(color);
        android.graphics.drawable.LayerDrawable layers = new android.graphics.drawable.LayerDrawable(
                new android.graphics.drawable.Drawable[]{bg, bar});
        layers.setLayerWidth(1, Ui.dp(c, 3));
        layers.setLayerGravity(1, Gravity.START | Gravity.FILL_VERTICAL);
        box.setBackground(layers);
        box.setPadding(Ui.dp(c, 12), Ui.dp(c, 8), Ui.dp(c, 10), Ui.dp(c, 8));
        box.setLayoutParams(Ui.margins(c, Ui.MATCH, Ui.WRAP, 0, 6, 0, 2));
        return box;
    }

    /**
     * The "Review this" box: the note with anchors as tappable chips, or, with
     * no note, "No note on what to review" and the newest part of the body.
     */
    static View noteBox(BaseActivity a, String note, String focus, String selfUid, JSONArray resolved) {
        if (note == null || note.trim().isEmpty()) {
            LinearLayout box = barBox(a, Ui.border(a), 0x00000000);
            TextView label = Ui.label(a, a.getString(R.string.review_no_note), 13, Ui.muted(a), false);
            label.setTypeface(Typeface.defaultFromStyle(Typeface.ITALIC));
            box.addView(label);
            if (focus != null && !focus.trim().isEmpty()) {
                TextView text = Ui.body(a, focus.trim());
                text.setMaxLines(4);
                text.setEllipsize(android.text.TextUtils.TruncateAt.END);
                box.addView(text);
            }
            return box;
        }
        LinearLayout box = barBox(a, Ui.ok(a), Ui.okSoft(a));
        SpannableStringBuilder text = new SpannableStringBuilder();
        String label = a.getString(R.string.review_look_here);
        text.append(label);
        text.setSpan(new StyleSpan(Typeface.BOLD), 0, label.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        text.setSpan(new ForegroundColorSpan(Ui.ok(a)), 0, label.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        text.append("  ");
        for (ReviewNote.Part part : ReviewNote.parts(note)) {
            if (part.anchor == null) {
                text.append(part.text);
                continue;
            }
            ReviewNote.Anchor anchor = part.anchor;
            boolean broken = isBroken(anchor, resolved);
            int start = text.length();
            text.append(" ").append(ReviewNote.label(anchor, selfUid)).append(" ↗ ");
            int end = text.length();
            text.setSpan(new ClickableSpan() {
                @Override
                public void onClick(View widget) {
                    openAnchor(a, anchor, selfUid, resolved);
                }

                @Override
                public void updateDrawState(TextPaint ds) {
                    ds.setColor(broken ? Ui.muted(a) : Ui.accent(a));
                    ds.setUnderlineText(broken);
                }
            }, start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            text.setSpan(new BackgroundColorSpan(Ui.accentSoft(a)), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        }
        TextView view = Ui.label(a, text, 15, Ui.text(a), false);
        view.setLineSpacing(Ui.dp(a, 3), 1f);
        view.setMovementMethod(LinkMovementMethod.getInstance());
        view.setHighlightColor(Ui.accentSoft(a));
        box.addView(view);
        return box;
    }

    private static JSONObject resolvedFor(ReviewNote.Anchor anchor, JSONArray resolved) {
        for (int i = 0; resolved != null && i < resolved.length(); i++) {
            JSONObject r = resolved.optJSONObject(i);
            if (r != null && anchor.raw.equals(r.optString("raw"))) return r;
        }
        return null;
    }

    private static boolean isBroken(ReviewNote.Anchor anchor, JSONArray resolved) {
        JSONObject r = resolvedFor(anchor, resolved);
        return r != null && !r.optBoolean("ok", true);
    }

    /** Heading and unit anchors open the unit at that place; media anchors the viewer at the time. */
    static void openAnchor(BaseActivity a, ReviewNote.Anchor anchor, String selfUid, JSONArray resolved) {
        if (ReviewNote.MEDIA.equals(anchor.kind)) {
            JSONObject r = resolvedFor(anchor, resolved);
            JSONObject media = r == null ? null : r.optJSONObject("media_item");
            if (media == null) {
                a.toast(a.getString(R.string.review_anchor_missing));
                return;
            }
            String url = mediaUrl(a, media);
            String kind = str(media, "preview_kind");
            if (kind.isEmpty()) kind = anchor.start >= 0 || MediaActivity.isVideo(str(media, "kind"), url) ? "video" : "image";
            a.startActivity(MediaActivity.intent(a, url, str(media, "caption"), kind)
                    .putExtra(MediaActivity.EXTRA_START_MS, anchor.start >= 0 ? (int) Math.round(anchor.start * 1000) : 0)
                    .putExtra(MediaActivity.EXTRA_END_MS, anchor.end >= 0 ? (int) Math.round(anchor.end * 1000) : 0));
            return;
        }
        String uid = anchor.unitOr(selfUid);
        if (uid.isEmpty()) return;
        a.startActivity(NodeActivity.intent(a, uid, ReviewNote.HEADING.equals(anchor.kind) ? anchor.slug : ""));
    }

    static String mediaUrl(BaseActivity a, JSONObject media) {
        String url = str(media, "url");
        String raw = str(media, "raw_url");
        if ((url.startsWith("http://") || url.startsWith("https://")) && !url.isEmpty()) return url;
        return raw.isEmpty() ? Links.absolute(a, url) : Links.absolute(a, raw);
    }

    /** "Round 1 · Sent back" and the last three comments of the previous round. */
    static View previousRound(BaseActivity a, JSONObject prev) {
        if (prev == null) return null;
        JSONArray comments = prev.optJSONArray("comments");
        String out = str(prev, "outcome");
        if (out.isEmpty() && (comments == null || comments.length() == 0)) return null;
        LinearLayout box = barBox(a, Ui.warn(a), 0x00000000);
        String head = prev.optInt("round") > 0 ? a.getString(R.string.review_round, prev.optInt("round"))
                : a.getString(R.string.review_prev_feedback);
        if (!out.isEmpty()) head += " · " + outcome(a, out);
        box.addView(Ui.label(a, head, 14, Ui.warn(a), true));
        int from = comments == null ? 0 : Math.max(0, comments.length() - 3);
        for (int i = from; comments != null && i < comments.length(); i++) {
            JSONObject c = comments.optJSONObject(i);
            if (c == null) continue;
            boolean resolved = c.optBoolean("resolved");
            TextView line = Ui.label(a, (resolved ? "✓ " : "● ") + str(c, "body"), 14,
                    resolved ? Ui.muted(a) : Ui.text(a), false);
            line.setMaxLines(3);
            line.setEllipsize(android.text.TextUtils.TruncateAt.END);
            box.addView(line);
        }
        return box;
    }

    /** The changes toggle: loads the line diff on first open. */
    static View changesToggle(BaseActivity a, String uid, JSONObject changes, JSONObject base, JSONObject loaded) {
        String label = changesText(a, changes, base);
        if (label.isEmpty()) return null;
        LinearLayout wrap = Ui.column(a);
        TextView toggle = Ui.label(a, "◷  " + label, 14, Ui.accent(a), false);
        toggle.setMinHeight(Ui.dp(a, 40));
        toggle.setGravity(Gravity.CENTER_VERTICAL);
        LinearLayout panel = Ui.column(a);
        panel.setVisibility(View.GONE);
        wrap.addView(toggle);
        wrap.addView(panel);
        if (NeedsCards.empty(NeedsCards.changes(changes))) {
            toggle.setTextColor(Ui.muted(a));
            return wrap;
        }
        final boolean[] filled = {false};
        Ui.clickable(toggle, v -> {
            boolean open = panel.getVisibility() != View.VISIBLE;
            panel.setVisibility(open ? View.VISIBLE : View.GONE);
            if (!open || filled[0]) return;
            filled[0] = true;
            if (loaded != null) {
                panel.addView(diffView(a, loaded));
                return;
            }
            panel.addView(Ui.small(a, a.getString(R.string.loading)));
            a.call(() -> a.api.get("/api/units/" + ArkClient.enc(uid) + "/review"), info -> {
                panel.removeAllViews();
                panel.addView(diffView(a, info));
            });
        });
        return wrap;
    }

    /** The line diff and the new attachments of {@code GET /api/units/{uid}/review}. */
    static View diffView(BaseActivity a, JSONObject info) {
        LinearLayout box = Ui.column(a);
        box.setBackground(Ui.rounded(Ui.cardColor(a), Ui.border(a), a, 8));
        box.setPadding(Ui.dp(a, 10), Ui.dp(a, 8), Ui.dp(a, 10), Ui.dp(a, 8));
        JSONObject diff = info == null ? null : info.optJSONObject("diff");
        JSONArray hunks = diff == null ? null : diff.optJSONArray("hunks");
        int shown = 0;
        for (int i = 0; hunks != null && i < hunks.length(); i++) {
            JSONObject h = hunks.optJSONObject(i);
            if (h == null) continue;
            String op = h.optString("op");
            if ("skip".equals(op)) {
                TextView skip = Ui.small(a, a.getString(R.string.review_unchanged_lines, h.optInt("n")));
                skip.setGravity(Gravity.CENTER_HORIZONTAL);
                box.addView(skip);
                continue;
            }
            StringBuilder text = new StringBuilder();
            JSONArray lines = h.optJSONArray("lines");
            for (int k = 0; lines != null && k < lines.length(); k++) {
                if (k > 0) text.append('\n');
                text.append(lines.optString(k));
            }
            if ("add".equals(op)) {
                LinearLayout add = barBox(a, Ui.ok(a), Ui.okSoft(a));
                add.addView(Ui.body(a, text.toString()));
                box.addView(add);
            } else {
                TextView line = Ui.label(a, text.toString(), 12.5f, Ui.muted(a), false);
                line.setTypeface(Typeface.MONOSPACE);
                if ("del".equals(op)) line.setPaintFlags(line.getPaintFlags() | android.graphics.Paint.STRIKE_THRU_TEXT_FLAG);
                box.addView(line);
            }
            shown++;
        }
        if (shown == 0) box.addView(Ui.small(a, a.getString(R.string.review_no_text_changes)));
        JSONArray media = info == null ? null : info.optJSONArray("media_new");
        if (media != null && media.length() > 0) {
            box.addView(Ui.small(a, a.getString(R.string.review_new_files, media.length())));
            box.addView(thumbStrip(a, media, info.optString("title")));
        }
        box.setLayoutParams(Ui.margins(a, Ui.MATCH, Ui.WRAP, 0, 2, 0, 6));
        return box;
    }

    /** Thumbnails of images and videos; tap opens the viewer. */
    static View thumbStrip(BaseActivity a, JSONArray media, String title) {
        HorizontalScrollView scroll = new HorizontalScrollView(a);
        scroll.setHorizontalScrollBarEnabled(false);
        LinearLayout row = Ui.row(a);
        int shown = 0;
        for (int i = 0; media != null && i < media.length() && shown < 8; i++) {
            JSONObject m = media.optJSONObject(i);
            if (m == null) continue;
            String url = mediaUrl(a, m);
            String kind = str(m, "preview_kind");
            boolean image = "image".equals(kind) || m.optBoolean("is_image") || "image".equals(str(m, "kind"));
            boolean video = "video".equals(kind) || MediaActivity.isVideo(str(m, "kind"), url);
            if (!image && !video) continue;
            String caption = str(m, "caption");
            View thumb;
            if (image) {
                ImageView view = new ImageView(a);
                view.setScaleType(ImageView.ScaleType.CENTER_CROP);
                view.setBackground(Ui.rounded(Ui.codeBg(a), 0, a, 8));
                view.setClipToOutline(true);
                view.setContentDescription(caption.isEmpty() ? title : caption);
                Images.load(a, a.api, url, 360, view);
                thumb = view;
            } else {
                TextView view = Ui.label(a, "▶  " + a.getString(R.string.video), 13, Ui.text(a), true);
                view.setGravity(Gravity.CENTER);
                view.setBackground(Ui.rounded(Ui.codeBg(a), Ui.border(a), a, 8));
                thumb = view;
            }
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(Ui.dp(a, 120), Ui.dp(a, 76));
            lp.setMargins(0, Ui.dp(a, 4), Ui.dp(a, 8), Ui.dp(a, 4));
            row.addView(thumb, lp);
            String c = caption;
            Ui.clickable(thumb, v -> a.startActivity(MediaActivity.intent(a, url, c, image ? "image" : "video")));
            shown++;
        }
        scroll.addView(row);
        return shown == 0 ? new View(a) : scroll;
    }

    // ------------------------------------------------------- member table

    /**
     * One row per member: uid, claim (two lines), headline number, the
     * independent review verdict and what changed; the whole row opens the
     * member at the first place its note points to.
     */
    static View memberTable(BaseActivity a, List<JSONObject> members, boolean outcome) {
        LinearLayout table = Ui.column(a);
        table.setBackground(Ui.rounded(Ui.cardColor(a), Ui.border(a), a, 8));
        table.setLayoutParams(Ui.margins(a, Ui.MATCH, Ui.WRAP, 0, 6, 0, 6));
        for (int i = 0; i < members.size(); i++) {
            JSONObject m = members.get(i);
            if (i > 0) table.addView(Ui.divider(a));
            LinearLayout row = Ui.column(a);
            row.setMinimumHeight(Ui.dp(a, 48));
            row.setPadding(Ui.dp(a, 10), Ui.dp(a, 8), Ui.dp(a, 10), Ui.dp(a, 8));
            LinearLayout top = Ui.row(a);
            top.setGravity(Gravity.TOP);
            TextView uid = Ui.label(a, m.optString("uid"), 12, Ui.muted(a), true);
            uid.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
            uid.setPadding(0, Ui.dp(a, 2), Ui.dp(a, 8), 0);
            top.addView(uid);
            String out = str(m, "outcome");
            if (outcome && !out.isEmpty()) {
                top.addView(Ui.chip(a, outcome(a, out), "accepted".equals(out) ? Ui.ok(a) : Ui.warn(a),
                        "accepted".equals(out) ? Ui.okSoft(a) : Ui.warnSoft(a)));
            }
            TextView title = Ui.label(a, m.optString("title"), 14, "accepted".equals(out) ? Ui.muted(a) : Ui.text(a), false);
            title.setMaxLines(2);
            title.setEllipsize(android.text.TextUtils.TruncateAt.END);
            top.addView(title, Ui.lp(0, Ui.WRAP, 1));
            String number = ReviewNote.keyNumber(m.optString("title"));
            if (!number.isEmpty()) {
                TextView num = Ui.label(a, number, 12.5f, Ui.text(a), true);
                num.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
                num.setPadding(Ui.dp(a, 8), Ui.dp(a, 2), 0, 0);
                top.addView(num);
            }
            row.addView(top);
            String verdict = str(m, "verdict");
            JSONObject changes = m.optJSONObject("review_changes");
            if (changes == null) changes = m.optJSONObject("changes");
            int[] sum = NeedsCards.changes(changes);
            if (!verdict.isEmpty() || (sum != null && !NeedsCards.empty(sum))) {
                LinearLayout meta = Ui.row(a);
                meta.setPadding(0, Ui.dp(a, 2), 0, 0);
                meta.addView(Ui.small(a, verdict), Ui.lp(0, Ui.WRAP, 1));
                if (sum != null && !NeedsCards.empty(sum)) {
                    String text = "+" + sum[0] + " −" + sum[1]
                            + (sum[2] > 0 ? " · " + a.getString(R.string.review_new_files, sum[2]) : "");
                    meta.addView(Ui.label(a, text, 12, Ui.ok(a), false));
                }
                row.addView(meta);
            }
            JSONArray anchors = m.optJSONArray("review_anchors");
            if (anchors == null) anchors = m.optJSONArray("anchors");
            String note = str(m, "note");
            if (note.isEmpty()) note = str(m, "review_note");
            if (!note.isEmpty()) {
                TextView n = Ui.small(a, stripAnchors(note));
                n.setMaxLines(2);
                n.setEllipsize(android.text.TextUtils.TruncateAt.END);
                row.addView(n);
            }
            String memberUid = m.optString("uid");
            ReviewNote.Anchor first = firstHeading(anchors, memberUid);
            Ui.clickable(row, v -> {
                if (first != null) openAnchor(a, first, memberUid, null);
                else openUnit(a, memberUid, m.optString("type"));
            });
            table.addView(row);
        }
        return table;
    }

    private static String stripAnchors(String note) {
        StringBuilder out = new StringBuilder();
        for (ReviewNote.Part p : ReviewNote.parts(note)) out.append(p.anchor == null ? p.text : ReviewNote.label(p.anchor, ""));
        return out.toString();
    }

    private static ReviewNote.Anchor firstHeading(JSONArray anchors, String uid) {
        for (int i = 0; anchors != null && i < anchors.length(); i++) {
            JSONObject r = anchors.optJSONObject(i);
            if (r == null || !r.optBoolean("ok", true)) continue;
            ReviewNote.Anchor anchor = ReviewNote.parse(r.optString("raw"));
            if (anchor != null && ReviewNote.HEADING.equals(anchor.kind) && (anchor.unit.isEmpty() || anchor.unit.equals(uid))) {
                return anchor;
            }
        }
        return null;
    }

    static void openUnit(BaseActivity a, String uid, String type) {
        if (uid == null || uid.isEmpty()) return;
        a.startActivity("task".equals(type) ? TaskActivity.intent(a, uid) : NodeActivity.intent(a, uid));
    }

    // ------------------------------------------------------------ actions

    /** A row of equal-width buttons that wraps two per line. */
    static LinearLayout buttonGrid(Context c, List<Button> buttons) {
        LinearLayout grid = Ui.column(c);
        grid.setPadding(0, Ui.dp(c, 6), 0, 0);
        for (int i = 0; i < buttons.size(); i += 2) {
            LinearLayout row = Ui.row(c);
            for (int k = i; k < Math.min(i + 2, buttons.size()); k++) {
                LinearLayout.LayoutParams lp = Ui.lp(0, Ui.WRAP, 1);
                lp.setMargins(k == i ? 0 : Ui.dp(c, 4), Ui.dp(c, 3), k == i && k + 1 < buttons.size() ? Ui.dp(c, 4) : 0, Ui.dp(c, 3));
                row.addView(buttons.get(k), lp);
            }
            grid.addView(row);
        }
        return grid;
    }

    private static void noteDialog(BaseActivity a, String title, String label, boolean optional, String initial,
                                   String action, TextDone done) {
        LinearLayout body = Ui.column(a);
        body.setPadding(Ui.dp(a, 20), Ui.dp(a, 8), Ui.dp(a, 20), 0);
        body.addView(Ui.small(a, label));
        EditText input = Ui.multiline(a, label);
        if (initial != null) input.setText(initial);
        body.addView(input);
        AlertDialog dialog = new AlertDialog.Builder(a).setTitle(title).setView(body)
                .setNegativeButton(R.string.cancel, null).setPositiveButton(action, null).create();
        dialog.setOnShowListener(d -> dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v -> {
            String text = input.getText().toString().trim();
            if (text.isEmpty() && !optional) {
                input.setError(a.getString(R.string.needs_reason_required));
                return;
            }
            dialog.dismiss();
            done.run(text);
        }));
        dialog.show();
    }

    /** One comment row of kind {@code kind} (when there is text), then the status. */
    private static void apply(BaseActivity a, String uid, String text, String kind, String status, String toast, Done done) {
        a.call(() -> {
            if (!text.isEmpty()) {
                a.api.post("/api/units/" + ArkClient.enc(uid) + "/comments", new JSONObject().put("body", text).put("kind", kind));
            }
            if (status != null) a.api.patch("/api/units/" + ArkClient.enc(uid), new JSONObject().put("status", status));
            return Boolean.TRUE;
        }, ok -> {
            a.toast(toast);
            if (done != null) done.run();
        });
    }

    static void accept(BaseActivity a, String uid, String initial, Done done) {
        noteDialog(a, a.getString(R.string.needs_accept_title, uid), a.getString(R.string.needs_accept_note), true, initial,
                a.getString(R.string.needs_accept),
                text -> apply(a, uid, text, "accepted", "done", a.getString(R.string.needs_accepted, uid), done));
    }

    static void sendBack(BaseActivity a, String uid, String initial, Done done) {
        noteDialog(a, a.getString(R.string.needs_send_back_title, uid), a.getString(R.string.needs_send_back_reason), false,
                initial, a.getString(R.string.needs_send_back),
                text -> apply(a, uid, text, "sent_back", "active", a.getString(R.string.needs_sent_back, uid), done));
    }

    static void comment(BaseActivity a, String uid, String initial, Done done) {
        noteDialog(a, a.getString(R.string.needs_comment_title, uid), a.getString(R.string.needs_comment_label), false,
                initial, a.getString(R.string.needs_add_comment),
                text -> apply(a, uid, text, "comment", null, a.getString(R.string.needs_commented, uid), done));
    }

    static void decide(BaseActivity a, String uid, Done done) {
        noteDialog(a, a.getString(R.string.needs_decide_title, uid), a.getString(R.string.needs_decide_answer), false, "",
                a.getString(R.string.needs_decide),
                text -> apply(a, uid, text, "decided", "done", a.getString(R.string.needs_decided, uid), done));
    }

    /** A checklist of members plus a note; {@code done} gets the checked ones and the text. */
    private interface ChecklistDone {
        void run(List<JSONObject> checked, List<JSONObject> unchecked, String text);
    }

    private static void checklist(BaseActivity a, String title, String hint, List<JSONObject> members, boolean showList,
                                  String label, String action, boolean noteRequired, ChecklistDone done) {
        LinearLayout body = Ui.column(a);
        body.setPadding(Ui.dp(a, 20), Ui.dp(a, 8), Ui.dp(a, 20), 0);
        body.addView(Ui.small(a, hint));
        List<CheckBox> boxes = new ArrayList<>();
        if (showList) {
            for (JSONObject m : members) {
                CheckBox box = new CheckBox(a);
                box.setChecked(true);
                box.setText(m.optString("uid") + "  " + m.optString("title"));
                box.setTextColor(Ui.text(a));
                box.setMinHeight(Ui.dp(a, 44));
                box.setMaxLines(2);
                boxes.add(box);
                body.addView(box);
            }
        }
        body.addView(Ui.small(a, label));
        EditText input = Ui.multiline(a, label);
        body.addView(input);
        android.widget.ScrollView scroll = new android.widget.ScrollView(a);
        scroll.addView(body);
        AlertDialog dialog = new AlertDialog.Builder(a).setTitle(title).setView(scroll)
                .setNegativeButton(R.string.cancel, null).setPositiveButton(action, null).create();
        dialog.setOnShowListener(d -> dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v -> {
            List<JSONObject> checked = new ArrayList<>();
            List<JSONObject> unchecked = new ArrayList<>();
            for (int i = 0; i < members.size(); i++) {
                if (!showList || boxes.get(i).isChecked()) checked.add(members.get(i));
                else unchecked.add(members.get(i));
            }
            String text = input.getText().toString().trim();
            if (showList && checked.isEmpty() && noteRequired) {
                a.toast(a.getString(R.string.needs_none_selected));
                return;
            }
            if (noteRequired && text.isEmpty()) {
                input.setError(a.getString(R.string.needs_reason_required));
                return;
            }
            dialog.dismiss();
            done.run(checked, unchecked, text);
        }));
        dialog.show();
    }

    /** Accept all / send back for the units of one delivery: the checked ones take the action. */
    static void deliveryDecide(BaseActivity a, String title, List<JSONObject> members, boolean accept, Done done) {
        checklist(a, (accept ? a.getString(R.string.review_accept_all) : a.getString(R.string.needs_send_back)) + " · " + title,
                a.getString(accept ? R.string.review_accept_all_hint : R.string.review_send_back_hint), members, true,
                a.getString(accept ? R.string.needs_bulk_note : R.string.needs_send_back_reason),
                accept ? a.getString(R.string.review_accept_all) : a.getString(R.string.needs_send_back), !accept,
                (checked, unchecked, text) -> {
                    if (checked.isEmpty()) {
                        a.toast(a.getString(R.string.needs_none_selected));
                        return;
                    }
                    a.call(() -> {
                        for (JSONObject m : checked) {
                            String uid = ArkClient.enc(m.optString("uid"));
                            if (!text.isEmpty()) {
                                a.api.post("/api/units/" + uid + "/comments", new JSONObject().put("body", text)
                                        .put("kind", accept ? "accepted" : "sent_back"));
                            }
                            a.api.patch("/api/units/" + uid, new JSONObject().put("status", accept ? "done" : "active"));
                        }
                        return checked.size();
                    }, n -> {
                        a.toast(a.getString(accept ? R.string.needs_accepted_n : R.string.needs_sent_back_n, n));
                        if (done != null) done.run();
                    });
                });
    }

    /** A review group: Accept (unchecked members go back with the note) or Send back every pending member. */
    static void groupDecide(BaseActivity a, JSONObject group, boolean accept, Done done) {
        List<JSONObject> members = new ArrayList<>();
        JSONArray ms = group.optJSONArray("members");
        for (int i = 0; ms != null && i < ms.length(); i++) {
            JSONObject m = ms.optJSONObject(i);
            if (m != null && str(m, "outcome").isEmpty()) members.add(m);
        }
        String id = group.optString("id", group.optString("uid"));
        String title = group.optString("title", id);
        checklist(a, (accept ? a.getString(R.string.needs_accept) : a.getString(R.string.needs_send_back)) + " · " + title,
                a.getString(accept ? R.string.review_group_accept_hint : R.string.review_group_send_back_hint), members, accept,
                a.getString(accept ? R.string.needs_accept_note : R.string.needs_send_back_reason),
                accept ? a.getString(R.string.needs_accept) : a.getString(R.string.needs_send_back), !accept,
                (checked, unchecked, text) -> {
                    List<JSONObject> back = accept ? unchecked : checked;
                    if (!back.isEmpty() && text.isEmpty()) {
                        a.toast(a.getString(R.string.review_reason_for_back));
                        return;
                    }
                    a.call(() -> {
                        JSONObject decide = new JSONObject();
                        for (JSONObject m : back) decide.put(m.optString("uid"), "send_back");
                        return a.api.post("/api/reviews/" + ArkClient.enc(id) + "/decide", new JSONObject()
                                .put("action", accept ? "accept" : "send_back").put("note", text).put("members", decide));
                    }, out -> {
                        a.toast(a.getString(out.optBoolean("closed") ? R.string.review_group_closed : R.string.review_group_decided, title));
                        if (done != null) done.run();
                    });
                });
    }

    static void groupComment(BaseActivity a, JSONObject group, Done done) {
        String id = group.optString("id", group.optString("uid"));
        String title = group.optString("title", id);
        noteDialog(a, a.getString(R.string.review_group_comment_title, title), a.getString(R.string.needs_comment_label), false, "",
                a.getString(R.string.needs_add_comment), text -> a.call(
                        () -> a.api.post("/api/reviews/" + ArkClient.enc(id) + "/comments", new JSONObject().put("body", text)),
                        out -> {
                            a.toast(a.getString(R.string.needs_commented, id));
                            if (done != null) done.run();
                        }));
    }

    // -------------------------------------------------------- unit screen

    /**
     * The unit screen's review section (a unit in review, or in a review
     * group): round, note with anchors, previous round, review groups and the
     * changes since the last review.
     */
    static void unitReview(BaseActivity a, LinearLayout content, JSONObject unit) {
        JSONArray groups = unit.optJSONArray("review_groups");
        if (!"review".equals(unit.optString("status")) && (groups == null || groups.length() == 0)) return;
        LinearLayout section = Ui.column(a);
        content.addView(section);
        String uid = unit.optString("uid");
        a.call(() -> a.api.get("/api/units/" + ArkClient.enc(uid) + "/review"), info -> {
            boolean inReview = info.optBoolean("in_review");
            JSONArray gs = info.optJSONArray("groups");
            if (!inReview && (gs == null || gs.length() == 0)) return;
            LinearLayout head = Ui.row(a);
            head.addView(Ui.heading(a, a.getString(R.string.review_title)), Ui.lp(0, Ui.WRAP, 1));
            if (inReview && info.optInt("round") > 0) head.addView(roundChip(a, info.optInt("round")));
            section.addView(head);
            if (inReview) {
                section.addView(noteBox(a, str(info, "note"), "", uid, info.optJSONArray("anchors")));
                View prev = previousRound(a, info.optJSONObject("previous"));
                if (prev != null) section.addView(prev);
            }
            for (int i = 0; gs != null && i < gs.length(); i++) {
                JSONObject g = gs.optJSONObject(i);
                if (g == null) continue;
                section.addView(Ui.small(a, a.getString(R.string.review_in_group, g.optString("title", g.optString("id")))));
            }
            JSONObject diff = info.optJSONObject("diff");
            if (inReview && diff != null) {
                JSONArray media = info.optJSONArray("media_new");
                JSONObject changes = new JSONObject();
                try {
                    changes.put("added", diff.optInt("added")).put("removed", diff.optInt("removed"))
                            .put("media", media == null ? 0 : media.length());
                } catch (org.json.JSONException ignored) {
                    // put() with a non-null key and int values does not throw
                }
                View toggle = changesToggle(a, uid, changes, info.optJSONObject("base"), info);
                if (toggle != null) section.addView(toggle);
            }
        });
    }

    /**
     * The comment box under a unit with Accept, Send back and Add comment on
     * one row (Accept / Send back only while the unit is in review); the typed
     * text becomes the accept note or the send-back reason.
     */
    static void commentBox(BaseActivity a, LinearLayout content, JSONObject unit, Done done) {
        if (a.session.isReadOnly()) return;
        String uid = unit.optString("uid");
        JSONArray comments = unit.optJSONArray("comments");
        content.addView(Ui.heading(a, a.getString(R.string.needs_comment)));
        int from = comments == null ? 0 : Math.max(0, comments.length() - 5);
        for (int i = from; comments != null && i < comments.length(); i++) {
            JSONObject c = comments.optJSONObject(i);
            if (c == null) continue;
            LinearLayout row = Ui.column(a);
            row.setPadding(0, Ui.dp(a, 6), 0, Ui.dp(a, 6));
            String kind = str(c, "kind");
            boolean decision = kind.equals("accepted") || kind.equals("sent_back") || kind.equals("decided");
            String meta = str(c, "author") + (decision ? " · " + outcome(a, kind) : "")
                    + " · " + Fmt.ago(a, str(c, "created_at"));
            row.addView(Ui.small(a, meta));
            boolean resolved = !str(c, "resolved_at").isEmpty() || c.optBoolean("resolved");
            row.addView(Ui.label(a, str(c, "body"), 14, resolved ? Ui.muted(a) : Ui.text(a), false));
            content.addView(row);
        }
        EditText box = Ui.multiline(a, a.getString(R.string.needs_comment_label));
        content.addView(box);
        content.addView(Ui.small(a, a.getString(R.string.review_comment_hint)));
        boolean inReview = "review".equals(unit.optString("status"));
        LinearLayout row = Ui.row(a);
        row.setPadding(0, Ui.dp(a, 6), 0, Ui.dp(a, 6));
        Done after = () -> {
            box.setText("");
            if (done != null) done.run();
        };
        if (inReview) {
            Button accept = Ui.button(a, a.getString(R.string.needs_accept), true, false);
            accept.setOnClickListener(v -> accept(a, uid, box.getText().toString().trim(), after));
            row.addView(accept, wrapLp(a));
            Button back = Ui.button(a, a.getString(R.string.needs_send_back), false, false);
            back.setOnClickListener(v -> sendBack(a, uid, box.getText().toString().trim(), after));
            row.addView(back, wrapLp(a));
        }
        Button add = Ui.button(a, a.getString(R.string.needs_add_comment), !inReview, false);
        add.setOnClickListener(v -> {
            String text = box.getText().toString().trim();
            if (text.isEmpty()) {
                box.setError(a.getString(R.string.needs_reason_required));
                return;
            }
            apply(a, uid, text, "comment", null, a.getString(R.string.needs_commented, uid), after);
        });
        row.addView(add, wrapLp(a));
        content.addView(row);
    }

    private static LinearLayout.LayoutParams wrapLp(Context c) {
        LinearLayout.LayoutParams lp = Ui.lp(Ui.WRAP, Ui.WRAP);
        lp.setMargins(0, 0, Ui.dp(c, 8), 0);
        return lp;
    }
}
