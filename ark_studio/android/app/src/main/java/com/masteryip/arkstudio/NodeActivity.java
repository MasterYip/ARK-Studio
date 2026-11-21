package com.masteryip.arkstudio;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.view.View;
import android.widget.HorizontalScrollView;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import com.masteryip.arkstudio.model.Markdown;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Fmt;
import com.masteryip.arkstudio.ui.Labels;
import com.masteryip.arkstudio.ui.Images;
import com.masteryip.arkstudio.ui.MarkdownView;
import com.masteryip.arkstudio.ui.Ui;

import org.json.JSONArray;
import org.json.JSONObject;

/** Node (unit) viewer: rendered body, media and links; web editor for the rest. */
public class NodeActivity extends BaseActivity {
    static final String EXTRA_UID = "uid";
    /** A heading slug or "{#id}" of the body to scroll to (review anchors). */
    static final String EXTRA_ANCHOR = "anchor";
    protected String uid;
    protected JSONObject unit;
    private String pendingAnchor = "";

    public static Intent intent(Context c, String uid) {
        return new Intent(c, NodeActivity.class).putExtra(EXTRA_UID, uid);
    }

    public static Intent intent(Context c, String uid, String anchor) {
        return intent(c, uid).putExtra(EXTRA_ANCHOR, anchor == null ? "" : anchor);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        uid = getIntent().getStringExtra(EXTRA_UID);
        String anchor = getIntent().getStringExtra(EXTRA_ANCHOR);
        pendingAnchor = anchor == null ? "" : anchor;
        buildFrame(uid, true);
        addAction(getString(R.string.open_in_web), v -> openWeb(WebRoutes.editorUnit(uid)));
        showLoading();
        reload();
    }

    @Override
    protected void reload() {
        Async.run(this, () -> api.get("/api/units/" + com.masteryip.arkstudio.api.ArkClient.enc(uid)),
                result -> {
                    unit = result.optJSONObject("unit");
                    doneRefreshing();
                    if (unit == null) return;
                    content.removeAllViews();
                    render();
                    revealAnchor();
                }, this::showError);
    }

    /** Scrolls to the anchored heading or block once, with a short highlight. */
    private void revealAnchor() {
        if (pendingAnchor.isEmpty()) return;
        String id = pendingAnchor;
        pendingAnchor = "";
        View target = content.findViewWithTag(MarkdownView.ANCHOR_TAG + id);
        if (target == null) {
            toast(getString(R.string.review_anchor_missing));
            return;
        }
        // The review section above the body loads on its own; let it lay out first.
        scroll.postDelayed(() -> {
            int y = 0;
            for (View v = target; v != null && v != content; v = (View) v.getParent()) y += v.getTop();
            scroll.smoothScrollTo(0, Math.max(0, y - Ui.dp(this, 24)));
            target.setBackground(Ui.rounded(Ui.warnSoft(this), 0, this, 6));
            target.postDelayed(() -> target.setBackground(null), 2000);
        }, 600);
    }

    /** Called after an action that changes the unit (accept, send back, comment). */
    protected void changed() {
        reload();
    }

    protected void render() {
        titleView.setText(unit.optString("title", uid));
        setSubtitle(unit.optString("uid") + " · " + Labels.of(this, unit.optString("type")));
        header(this, content, unit);
        ReviewUi.unitReview(this, content, unit);
        body(this, content, unit);
        media(this, content, unit);
        ReviewUi.commentBox(this, content, unit, this::changed);
        links(this, content, unit);
    }

    // ------------------------------------------------------- shared sections

    static void header(BaseActivity a, LinearLayout content, JSONObject unit) {
        TextView title = Ui.label(a, unit.optString("title"), 21, Ui.text(a), true);
        title.setTextIsSelectable(true);
        content.addView(title);
        LinearLayout chips = Ui.row(a);
        chips.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 4));
        chips.addView(Ui.chip(a, Labels.of(a, unit.optString("type")), Ui.muted(a), Ui.codeBg(a)));
        String status = unit.optString("status", "");
        if (!status.isEmpty()) {
            chips.addView(Ui.chip(a, TasksPage.statusLabel(a, status), Ui.statusColor(a, status),
                    Ui.statusSoft(a, status)));
        }
        String importance = unit.optString("importance", "");
        if (!importance.isEmpty()) chips.addView(Ui.chip(a, importance.toUpperCase(java.util.Locale.ROOT), Ui.muted(a), Ui.codeBg(a)));
        HorizontalScrollView strip = new HorizontalScrollView(a);
        strip.setHorizontalScrollBarEnabled(false);
        JSONArray tags = unit.optJSONArray("tags");
        for (int i = 0; tags != null && i < tags.length(); i++) {
            String tag = tags.optString(i);
            if (tag.startsWith("type:") || tag.startsWith("status:") || tag.startsWith("importance:")) continue;
            chips.addView(Ui.chip(a, "#" + tag, Ui.accent(a), Ui.accentSoft(a)));
        }
        strip.addView(chips);
        content.addView(strip);
        StringBuilder dates = new StringBuilder();
        String start = clean(unit.optString("start_at"));
        String end = clean(unit.optString("end_at"));
        if (!start.isEmpty()) dates.append(a.getString(R.string.starts, start));
        if (!end.isEmpty()) dates.append(dates.length() > 0 ? "  ·  " : "").append(a.getString(R.string.due, end));
        String updated = Fmt.ago(a, unit.optString("updated_at"));
        if (!updated.isEmpty()) dates.append(dates.length() > 0 ? "  ·  " : "")
                .append(a.getString(R.string.updated, updated));
        if (dates.length() > 0) content.addView(Ui.small(a, dates.toString()));
    }

    static String clean(String value) {
        if (value == null || value.isEmpty() || "null".equals(value)) return "";
        return value.length() > 16 ? value.substring(0, 16).replace('T', ' ') : value;
    }

    static void body(BaseActivity a, LinearLayout content, JSONObject unit) {
        String body = unit.optString("body", "");
        content.addView(Ui.heading(a, a.getString(R.string.body)));
        if (body.trim().isEmpty()) {
            content.addView(Ui.small(a, a.getString(R.string.empty_body)));
            return;
        }
        if (Markdown.needsWebRender(body)) {
            LinearLayout banner = Ui.card(a);
            banner.setBackground(Ui.rounded(Ui.warnSoft(a), 0, a, 10));
            banner.addView(Ui.label(a, a.getString(R.string.rich_body), 14, Ui.text(a), true));
            banner.addView(Ui.small(a, a.getString(R.string.rich_body_detail)));
            String uid = unit.optString("uid");
            Ui.clickable(banner, v -> a.openWeb(WebRoutes.editorUnit(uid)));
            content.addView(banner);
        }
        content.addView(MarkdownView.render(a, body, href -> Links.open(a, href)));
    }

    static void media(BaseActivity a, LinearLayout content, JSONObject unit) {
        JSONArray media = unit.optJSONArray("media");
        if (media == null || media.length() == 0) return;
        content.addView(Ui.heading(a, a.getString(R.string.media_count, media.length())));
        for (int i = 0; i < media.length(); i++) {
            JSONObject item = media.optJSONObject(i);
            if (item == null) continue;
            String raw = item.optString("raw_url", "");
            String url = raw.isEmpty() ? item.optString("url") : Links.absolute(a, raw);
            String kind = item.optString("kind", "");
            boolean image = item.optBoolean("is_image") || "image".equals(kind);
            String caption = item.optString("caption", "");
            LinearLayout card = Ui.card(a);
            if (image) {
                ImageView view = new ImageView(a);
                view.setAdjustViewBounds(true);
                view.setScaleType(ImageView.ScaleType.FIT_CENTER);
                view.setBackgroundColor(Ui.codeBg(a));
                view.setMinimumHeight(Ui.dp(a, 120));
                view.setContentDescription(caption.isEmpty() ? a.getString(R.string.image) : caption);
                card.addView(view, Ui.lp(Ui.MATCH, Ui.WRAP));
                Images.load(a, a.api, url, 1080, view);
            } else {
                card.addView(Ui.label(a, "▶  " + (MediaActivity.isVideo(kind, url)
                        ? a.getString(R.string.video) : Labels.of(a, kind.isEmpty() ? "file" : kind)), 15,
                        Ui.text(a), true));
                card.addView(Ui.oneLine(Ui.small(a, item.optString("url"))));
            }
            if (!caption.isEmpty()) card.addView(Ui.small(a, caption));
            Ui.clickable(card, v -> {
                if (image || MediaActivity.isVideo(kind, url)) {
                    a.startActivity(MediaActivity.intent(a, url, caption, image ? "image" : "video"));
                } else {
                    Links.open(a, url);
                }
            });
            content.addView(card);
        }
    }

    static void links(BaseActivity a, LinearLayout content, JSONObject unit) {
        JSONArray out = unit.optJSONArray("links_out");
        JSONArray in = unit.optJSONArray("links_in");
        int total = (out == null ? 0 : out.length()) + (in == null ? 0 : in.length());
        if (total == 0) return;
        content.addView(Ui.heading(a, a.getString(R.string.links_count, total)));
        addLinks(a, content, out, "target", "→");
        addLinks(a, content, in, "source", "←");
    }

    private static void addLinks(BaseActivity a, LinearLayout content, JSONArray links, String key, String arrow) {
        for (int i = 0; links != null && i < links.length(); i++) {
            JSONObject link = links.optJSONObject(i);
            JSONObject other = link == null ? null : link.optJSONObject(key);
            if (other == null) continue;
            LinearLayout row = Ui.column(a);
            row.setPadding(0, Ui.dp(a, 8), 0, Ui.dp(a, 8));
            row.addView(Ui.oneLine(Ui.body(a, arrow + "  " + other.optString("title"))));
            row.addView(Ui.oneLine(Ui.small(a, Labels.of(a, link.optString("relation")) + " · "
                    + other.optString("uid") + " · " + Labels.of(a, other.optString("type")))));
            String uid = other.optString("uid");
            String type = other.optString("type");
            Ui.clickable(row, v -> a.startActivity("task".equals(type) ? TaskActivity.intent(a, uid)
                    : NodeActivity.intent(a, uid)));
            content.addView(row);
            content.addView(Ui.divider(a));
        }
    }
}
