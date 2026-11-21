package com.masteryip.arkstudio;

import android.view.View;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.model.NeedsCards;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/**
 * Home's "Needs you", the same cards as the web (needs-ui.js): reviews and
 * decisions, the units of one delivery in one card with a member table, and
 * review groups.  The first {@link #FIRST} cards render at once, the rest on
 * "Show all".
 */
final class NeedsSection {
    static final int FIRST = 6;

    private NeedsSection() {
    }

    static void render(BaseActivity a, LinearLayout content, JSONArray needs, ReviewUi.Done refresh) {
        int total = needs == null ? 0 : needs.length();
        LinearLayout head = Ui.row(a);
        head.addView(Ui.heading(a, a.getString(R.string.needs_you)));
        if (total > 0) {
            TextView count = Ui.chip(a, String.valueOf(total), Ui.color(a, R.color.ark_on_accent), Ui.warn(a));
            LinearLayout.LayoutParams lp = Ui.lp(Ui.WRAP, Ui.WRAP);
            lp.setMargins(Ui.dp(a, 8), Ui.dp(a, 14), 0, 0);
            head.addView(count, lp);
        }
        content.addView(head);
        if (total == 0) {
            content.addView(Ui.small(a, a.getString(R.string.needs_empty)));
            return;
        }
        int mine = 0;
        String me = a.session.user == null ? "" : a.session.user;
        for (int i = 0; i < total; i++) {
            JSONObject item = needs.optJSONObject(i);
            if (item != null && !me.isEmpty() && me.equalsIgnoreCase(ReviewUi.str(item, "assignee_effective"))) mine++;
        }
        content.addView(Ui.small(a, a.getString(R.string.needs_summary, total, mine)));
        List<NeedsCards.Card> cards = NeedsCards.cards(needs);
        LinearLayout list = Ui.column(a);
        content.addView(list);
        for (int i = 0; i < Math.min(FIRST, cards.size()); i++) list.addView(card(a, cards.get(i), refresh));
        if (cards.size() > FIRST) {
            Button more = Ui.button(a, a.getString(R.string.needs_show_all, cards.size()), false, false);
            more.setOnClickListener(v -> {
                list.removeView(more);
                for (int i = FIRST; i < cards.size(); i++) list.addView(card(a, cards.get(i), refresh));
            });
            list.addView(more, Ui.lp(Ui.MATCH, Ui.WRAP));
        }
    }

    static View card(BaseActivity a, NeedsCards.Card card, ReviewUi.Done refresh) {
        switch (card.type) {
            case NeedsCards.DELIVERY: return deliveryCard(a, card, refresh);
            case NeedsCards.REVIEW_GROUP: return reviewGroupCard(a, card.item, refresh);
            default: return itemCard(a, card.item, refresh);
        }
    }

    private static TextView kindChip(BaseActivity a, String text, boolean decision) {
        return Ui.chip(a, text, decision ? Ui.warn(a) : Ui.ok(a), decision ? Ui.warnSoft(a) : Ui.okSoft(a));
    }

    private static LinearLayout titleRow(BaseActivity a, String uid, String title) {
        LinearLayout row = Ui.row(a);
        row.setPadding(0, Ui.dp(a, 6), 0, Ui.dp(a, 2));
        if (uid != null && !uid.isEmpty()) {
            TextView id = Ui.label(a, uid, 12, Ui.muted(a), true);
            id.setTypeface(android.graphics.Typeface.MONOSPACE, android.graphics.Typeface.BOLD);
            id.setPadding(0, 0, Ui.dp(a, 8), 0);
            row.addView(id);
        }
        TextView text = Ui.label(a, title, 16, Ui.text(a), true);
        text.setMaxLines(2);
        text.setEllipsize(android.text.TextUtils.TruncateAt.END);
        row.addView(text, Ui.lp(0, Ui.WRAP, 1));
        return row;
    }

    private static LinearLayout metaRow(BaseActivity a, int round, int comments, String at) {
        LinearLayout row = Ui.row(a);
        row.setPadding(0, Ui.dp(a, 2), 0, Ui.dp(a, 2));
        if (round > 0) row.addView(ReviewUi.roundChip(a, round));
        if (comments > 0) row.addView(Ui.chip(a, "✎ " + comments, Ui.accent(a), Ui.accentSoft(a)));
        row.addView(new View(a), Ui.lp(0, 1, 1));
        row.addView(Ui.small(a, Fmt.ago(a, at)));
        return row;
    }

    // ------------------------------------------------------------- items

    static View itemCard(BaseActivity a, JSONObject item, ReviewUi.Done refresh) {
        LinearLayout card = Ui.card(a);
        String uid = item.optString("uid");
        boolean decision = "decision".equals(NeedsCards.kind(item));
        card.addView(kindChip(a, a.getString(decision ? R.string.needs_kind_decision : R.string.needs_kind_review), decision));
        LinearLayout title = titleRow(a, uid, item.optString("title", uid));
        Ui.clickable(title, v -> ReviewUi.openUnit(a, uid, item.optString("type")));
        card.addView(title);
        String since = ReviewUi.str(item, "review_since");
        card.addView(metaRow(a, item.optInt("review_round"), item.optInt("comments_open"),
                since.isEmpty() ? ReviewUi.str(item, "updated_at") : since));
        if (!decision) {
            String focus = ReviewUi.str(item, "review_focus");
            if (focus.isEmpty()) focus = ReviewUi.str(item, "excerpt_plain");
            card.addView(ReviewUi.noteBox(a, ReviewUi.str(item, "review_note"), focus, uid, item.optJSONArray("review_anchors")));
            View prev = ReviewUi.previousRound(a, item.optJSONObject("review_prev"));
            if (prev != null) card.addView(prev);
            View changes = ReviewUi.changesToggle(a, uid, item.optJSONObject("review_changes"), item.optJSONObject("review_base"), null);
            if (changes != null) card.addView(changes);
        } else {
            String excerpt = ReviewUi.str(item, "excerpt_plain");
            if (!excerpt.isEmpty()) {
                TextView text = Ui.body(a, excerpt);
                text.setMaxLines(4);
                text.setEllipsize(android.text.TextUtils.TruncateAt.END);
                card.addView(text);
            }
        }
        JSONArray media = item.optJSONArray("media");
        if (media != null && media.length() > 0) card.addView(ReviewUi.thumbStrip(a, media, item.optString("title")));
        List<Button> buttons = new ArrayList<>();
        if (!a.session.isReadOnly()) {
            if (decision) {
                buttons.add(button(a, R.string.needs_decide, true, v -> ReviewUi.decide(a, uid, refresh)));
            } else {
                buttons.add(button(a, R.string.needs_accept, true, v -> ReviewUi.accept(a, uid, "", refresh)));
                buttons.add(button(a, R.string.needs_send_back, false, v -> ReviewUi.sendBack(a, uid, "", refresh)));
            }
            buttons.add(button(a, R.string.needs_comment, false, v -> ReviewUi.comment(a, uid, "", refresh)));
        }
        buttons.add(button(a, R.string.needs_open, false, v -> ReviewUi.openUnit(a, uid, item.optString("type"))));
        card.addView(ReviewUi.buttonGrid(a, buttons));
        card.setTag("needs:" + uid);
        return card;
    }

    private static Button button(BaseActivity a, int label, boolean primary, View.OnClickListener listener) {
        Button b = Ui.button(a, a.getString(label), primary, false);
        b.setOnClickListener(listener);
        return b;
    }

    // --------------------------------------------------------- deliveries

    static View deliveryCard(BaseActivity a, NeedsCards.Card group, ReviewUi.Done refresh) {
        LinearLayout card = Ui.card(a);
        card.addView(kindChip(a, a.getString(R.string.review_delivery), false));
        LinearLayout title = titleRow(a, "", group.title + "  ·  " + ReviewUi.countLabel(a, group.members));
        String parent = group.parent == null ? "" : group.parent.optString("uid");
        Ui.clickable(title, v -> ReviewUi.openUnit(a, parent.isEmpty() ? group.uid() : parent,
                parent.isEmpty() ? group.item.optString("type") : "task"));
        card.addView(title);
        String newest = "";
        for (JSONObject m : group.members) {
            String at = ReviewUi.str(m, "review_since");
            if (at.isEmpty()) at = ReviewUi.str(m, "updated_at");
            if (at.compareTo(newest) > 0) newest = at;
        }
        card.addView(metaRow(a, 0, 0, newest));
        card.addView(Ui.small(a, "ⓘ " + explain(a, group)));
        card.addView(ReviewUi.memberTable(a, group.members, false));
        LinearLayout rows = Ui.column(a);
        rows.setVisibility(View.GONE);
        List<Button> buttons = new ArrayList<>();
        if (!a.session.isReadOnly()) {
            buttons.add(button(a, R.string.review_accept_all, true,
                    v -> ReviewUi.deliveryDecide(a, group.title, group.members, true, refresh)));
            buttons.add(button(a, R.string.needs_send_back, false,
                    v -> ReviewUi.deliveryDecide(a, group.title, group.members, false, refresh)));
        }
        Button oneByOne = Ui.button(a, a.getString(R.string.review_one_by_one), false, false);
        oneByOne.setOnClickListener(v -> {
            boolean open = rows.getVisibility() != View.VISIBLE;
            if (open && rows.getChildCount() == 0) {
                for (JSONObject m : group.members) rows.addView(itemCard(a, m, refresh));
            }
            rows.setVisibility(open ? View.VISIBLE : View.GONE);
            oneByOne.setText(open ? R.string.review_collapse : R.string.review_one_by_one);
        });
        buttons.add(oneByOne);
        card.addView(ReviewUi.buttonGrid(a, buttons));
        card.addView(rows);
        card.setTag("needs:" + group.key);
        return card;
    }

    static String explain(BaseActivity a, NeedsCards.Card group) {
        NeedsCards.Explain why = NeedsCards.explain(group);
        List<String> parts = new ArrayList<>();
        if (!why.parents.isEmpty()) parts.add(a.getString(R.string.review_why_parent, android.text.TextUtils.join(", ", why.parents)));
        if (why.linked) parts.add(a.getString(R.string.review_why_linked));
        if (!why.delivery.isEmpty()) parts.add(a.getString(R.string.review_why_delivery, why.deliveryName()));
        else parts.add(a.getString(R.string.review_why_window, Math.max(1, why.minutes)));
        return android.text.TextUtils.join(" · ", parts);
    }

    // ------------------------------------------------------ review groups

    static View reviewGroupCard(BaseActivity a, JSONObject group, ReviewUi.Done refresh) {
        LinearLayout card = Ui.card(a);
        List<JSONObject> members = new ArrayList<>();
        JSONArray ms = group.optJSONArray("members");
        for (int i = 0; ms != null && i < ms.length(); i++) {
            JSONObject m = ms.optJSONObject(i);
            if (m != null) members.add(m);
        }
        card.addView(kindChip(a, a.getString(R.string.review_group), false));
        card.addView(titleRow(a, "", group.optString("title") + "  ·  " + ReviewUi.countLabel(a, members)));
        card.addView(metaRow(a, Math.max(1, group.optInt("round")), group.optInt("comments_open"), ReviewUi.str(group, "opened_at")));
        String summary = ReviewUi.str(group, "summary");
        if (!summary.isEmpty()) card.addView(Ui.small(a, summary));
        card.addView(ReviewUi.noteBox(a, ReviewUi.str(group, "review_note"), "", "", group.optJSONArray("anchors")));
        JSONArray comments = group.optJSONArray("comments");
        if (comments != null && comments.length() > 0) {
            LinearLayout box = ReviewUi.barBox(a, Ui.warn(a), 0x00000000);
            int shown = 0;
            for (int i = comments.length() - 1; i >= 0 && shown < 3; i--) {
                JSONObject c = comments.optJSONObject(i);
                if (c == null || "agent_reply".equals(c.optString("kind"))) continue;
                boolean resolved = c.optBoolean("resolved");
                box.addView(Ui.label(a, (resolved ? "✓ " : "● ") + ReviewUi.str(c, "body"), 14,
                        resolved ? Ui.muted(a) : Ui.text(a), false));
                shown++;
            }
            if (shown > 0) card.addView(box);
        }
        card.addView(ReviewUi.memberTable(a, members, true));
        boolean pending = false;
        for (JSONObject m : members) if (ReviewUi.str(m, "outcome").isEmpty()) pending = true;
        List<Button> buttons = new ArrayList<>();
        if (!a.session.isReadOnly()) {
            Button accept = button(a, R.string.needs_accept, true, v -> ReviewUi.groupDecide(a, group, true, refresh));
            Button back = button(a, R.string.needs_send_back, false, v -> ReviewUi.groupDecide(a, group, false, refresh));
            accept.setEnabled(pending);
            back.setEnabled(pending);
            buttons.add(accept);
            buttons.add(back);
            buttons.add(button(a, R.string.needs_comment, false, v -> ReviewUi.groupComment(a, group, refresh)));
        }
        if (!buttons.isEmpty()) card.addView(ReviewUi.buttonGrid(a, buttons));
        card.setTag("needs:" + group.optString("id"));
        return card;
    }
}
