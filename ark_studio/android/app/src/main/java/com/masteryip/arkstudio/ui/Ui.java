package com.masteryip.arkstudio.ui;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.text.InputType;
import android.text.TextUtils;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.core.content.ContextCompat;

import com.masteryip.arkstudio.R;

/** Code-built view helpers with day/night colours from resources. */
public final class Ui {
    public static final int MATCH = ViewGroup.LayoutParams.MATCH_PARENT;
    public static final int WRAP = ViewGroup.LayoutParams.WRAP_CONTENT;

    private Ui() {
    }

    public static int color(Context c, int res) {
        return ContextCompat.getColor(c, res);
    }

    public static int bg(Context c) { return color(c, R.color.ark_bg); }
    public static int cardColor(Context c) { return color(c, R.color.ark_card); }
    public static int text(Context c) { return color(c, R.color.ark_text); }
    public static int muted(Context c) { return color(c, R.color.ark_muted); }
    public static int faint(Context c) { return color(c, R.color.ark_faint); }
    public static int border(Context c) { return color(c, R.color.ark_border); }
    public static int accent(Context c) { return color(c, R.color.ark_accent); }
    public static int accentSoft(Context c) { return color(c, R.color.ark_accent_soft); }
    public static int danger(Context c) { return color(c, R.color.ark_danger); }
    public static int dangerSoft(Context c) { return color(c, R.color.ark_danger_soft); }
    public static int ok(Context c) { return color(c, R.color.ark_ok); }
    public static int okSoft(Context c) { return color(c, R.color.ark_ok_soft); }
    public static int warn(Context c) { return color(c, R.color.ark_warn); }
    public static int warnSoft(Context c) { return color(c, R.color.ark_warn_soft); }
    public static int codeBg(Context c) { return color(c, R.color.ark_code_bg); }

    public static int dp(Context c, float value) {
        return Math.round(TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value,
                c.getResources().getDisplayMetrics()));
    }

    public static LinearLayout column(Context c) {
        LinearLayout layout = new LinearLayout(c);
        layout.setOrientation(LinearLayout.VERTICAL);
        return layout;
    }

    public static LinearLayout row(Context c) {
        LinearLayout layout = new LinearLayout(c);
        layout.setOrientation(LinearLayout.HORIZONTAL);
        layout.setGravity(Gravity.CENTER_VERTICAL);
        return layout;
    }

    public static LinearLayout.LayoutParams lp(int w, int h) {
        return new LinearLayout.LayoutParams(w, h);
    }

    public static LinearLayout.LayoutParams lp(int w, int h, float weight) {
        return new LinearLayout.LayoutParams(w, h, weight);
    }

    public static LinearLayout.LayoutParams margins(Context c, int w, int h, int l, int t, int r, int b) {
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(w, h);
        params.setMargins(dp(c, l), dp(c, t), dp(c, r), dp(c, b));
        return params;
    }

    public static TextView label(Context c, CharSequence value, float sp, int color, boolean bold) {
        TextView view = new TextView(c);
        view.setText(value);
        view.setTextSize(TypedValue.COMPLEX_UNIT_SP, sp);
        view.setTextColor(color);
        view.setTypeface(Typeface.create("sans-serif", bold ? Typeface.BOLD : Typeface.NORMAL));
        view.setLineSpacing(0, 1.12f);
        return view;
    }

    public static TextView title(Context c, CharSequence value) {
        return label(c, value, 20, text(c), true);
    }

    public static TextView heading(Context c, CharSequence value) {
        TextView view = label(c, value, 13, muted(c), true);
        view.setAllCaps(true);
        view.setLetterSpacing(0.06f);
        view.setPadding(0, dp(c, 14), 0, dp(c, 6));
        return view;
    }

    public static TextView body(Context c, CharSequence value) {
        return label(c, value, 15, text(c), false);
    }

    public static TextView small(Context c, CharSequence value) {
        return label(c, value, 13, muted(c), false);
    }

    public static TextView oneLine(TextView view) {
        view.setSingleLine(true);
        view.setEllipsize(TextUtils.TruncateAt.END);
        return view;
    }

    public static GradientDrawable rounded(int fill, int stroke, Context c, int radiusDp) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        drawable.setCornerRadius(dp(c, radiusDp));
        if (stroke != 0) drawable.setStroke(dp(c, 1), stroke);
        return drawable;
    }

    /** Rounded card with ripple when clickable. */
    public static LinearLayout card(Context c) {
        LinearLayout card = column(c);
        card.setBackground(rounded(cardColor(c), border(c), c, 14));
        card.setPadding(dp(c, 14), dp(c, 12), dp(c, 14), dp(c, 12));
        LinearLayout.LayoutParams params = lp(MATCH, WRAP);
        params.setMargins(0, dp(c, 5), 0, dp(c, 5));
        card.setLayoutParams(params);
        return card;
    }

    public static void clickable(View view, View.OnClickListener listener) {
        view.setOnClickListener(listener);
        view.setClickable(true);
        view.setFocusable(true);
        if (view.getBackground() != null) {
            view.setForeground(new RippleDrawable(ColorStateList.valueOf(0x22000000), null,
                    view.getBackground()));
        }
    }

    public static TextView chip(Context c, CharSequence value, int textColor, int fill) {
        TextView view = label(c, value, 11, textColor, true);
        view.setBackground(rounded(fill, 0, c, 999));
        view.setPadding(dp(c, 8), dp(c, 3), dp(c, 8), dp(c, 3));
        view.setSingleLine(true);
        LinearLayout.LayoutParams params = lp(WRAP, WRAP);
        params.setMargins(0, 0, dp(c, 6), 0);
        view.setLayoutParams(params);
        return view;
    }

    public static Button button(Context c, CharSequence value, boolean primary, boolean danger) {
        Button button = new Button(c);
        button.setText(value);
        button.setAllCaps(false);
        button.setTextSize(TypedValue.COMPLEX_UNIT_SP, 14);
        button.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
        button.setMinHeight(dp(c, 44));
        button.setMinimumHeight(dp(c, 44));
        button.setPadding(dp(c, 14), 0, dp(c, 14), 0);
        button.setStateListAnimator(null);
        int fill = primary ? (danger ? danger(c) : accent(c)) : cardColor(c);
        int stroke = primary ? 0 : (danger ? danger(c) : border(c));
        int textColor = primary ? color(c, R.color.ark_on_accent) : (danger ? danger(c) : text(c));
        button.setTextColor(textColor);
        button.setBackground(new RippleDrawable(ColorStateList.valueOf(0x33000000),
                rounded(fill, stroke, c, 10), null));
        return button;
    }

    public static EditText input(Context c, CharSequence hint, int type) {
        EditText edit = new EditText(c);
        edit.setHint(hint);
        edit.setInputType(type);
        edit.setTextSize(TypedValue.COMPLEX_UNIT_SP, 16);
        edit.setTextColor(text(c));
        edit.setHintTextColor(faint(c));
        edit.setBackground(rounded(cardColor(c), border(c), c, 10));
        edit.setPadding(dp(c, 12), dp(c, 10), dp(c, 12), dp(c, 10));
        LinearLayout.LayoutParams params = lp(MATCH, WRAP);
        params.setMargins(0, dp(c, 4), 0, dp(c, 8));
        edit.setLayoutParams(params);
        return edit;
    }

    public static EditText multiline(Context c, CharSequence hint) {
        EditText edit = input(c, hint, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE
                | InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);
        edit.setMinLines(2);
        edit.setMaxLines(6);
        edit.setGravity(Gravity.TOP | Gravity.START);
        return edit;
    }

    /** Horizontal usage bar with an optional highlighted (owned) share. */
    public static View bar(Context c, double totalPct, double ownedPct, int color) {
        FrameLayout frame = new FrameLayout(c);
        frame.setBackground(rounded(color(c, R.color.ark_track), 0, c, 999));
        LinearLayout.LayoutParams frameParams = lp(MATCH, dp(c, 8));
        frameParams.setMargins(0, dp(c, 4), 0, dp(c, 4));
        frame.setLayoutParams(frameParams);
        LinearLayout fill = row(c);
        View used = new View(c);
        used.setBackground(rounded(color, 0, c, 999));
        float total = (float) Math.max(0, Math.min(100, totalPct));
        float owned = (float) Math.max(0, Math.min(total, ownedPct));
        View ownedView = new View(c);
        ownedView.setBackground(rounded(accent(c), 0, c, 999));
        fill.addView(ownedView, lp(0, MATCH, owned));
        fill.addView(used, lp(0, MATCH, total - owned));
        fill.addView(new View(c), lp(0, MATCH, 100 - total));
        frame.addView(fill, new FrameLayout.LayoutParams(MATCH, MATCH));
        return frame;
    }

    public static View divider(Context c) {
        View view = new View(c);
        view.setBackgroundColor(border(c));
        view.setLayoutParams(lp(MATCH, Math.max(1, dp(c, 1) / 2)));
        return view;
    }

    public static View space(Context c, int heightDp) {
        View view = new View(c);
        view.setLayoutParams(lp(1, dp(c, heightDp)));
        return view;
    }

    public static int statusColor(Context c, String status) {
        switch (status == null ? "" : status) {
            case "running": case "active": case "starting": return accent(c);
            case "idle": case "review": case "queued": case "stopping": return warn(c);
            case "errored": case "blocked": case "failed": return danger(c);
            case "done": case "completed": case "stopped": case "succeeded": return ok(c);
            default: return muted(c);
        }
    }

    public static int statusSoft(Context c, String status) {
        switch (status == null ? "" : status) {
            case "running": case "active": case "starting": return accentSoft(c);
            case "idle": case "review": case "queued": case "stopping": return warnSoft(c);
            case "errored": case "blocked": case "failed": return dangerSoft(c);
            case "done": case "completed": case "stopped": case "succeeded": return okSoft(c);
            default: return color(c, R.color.ark_code_bg);
        }
    }
}
