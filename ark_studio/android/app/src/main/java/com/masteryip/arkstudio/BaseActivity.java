package com.masteryip.arkstudio;

import android.content.Intent;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import androidx.appcompat.app.AppCompatActivity;
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

/**
 * Shared frame for signed-in native screens: top bar, pull-to-refresh content
 * and loading/offline/error states with Retry. Leaves to the server list when
 * the device token is revoked or missing.
 */
public abstract class BaseActivity extends AppCompatActivity {
    protected AccountSession session;
    protected ArkClient api;
    protected LinearLayout root;
    protected LinearLayout topBar;
    protected TextView titleView;
    protected TextView subtitleView;
    protected LinearLayout actions;
    protected SwipeRefreshLayout refresher;
    protected ScrollView scroll;
    protected LinearLayout content;
    protected FrameLayout stage;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        session = Accounts.active(this);
        if (session == null) {
            if (needsSession()) toServers(null);
            return;
        }
        api = Accounts.client(session);
    }

    /** Screens that work without a signed-in server (settings) return {@code false}. */
    protected boolean needsSession() {
        return true;
    }

    protected boolean ready() {
        return (session != null || !needsSession()) && !isFinishing();
    }

    /** Builds the frame; {@code back} shows the up arrow. */
    protected void buildFrame(CharSequence title, boolean back) {
        root = Ui.column(this);
        root.setBackgroundColor(Ui.bg(this));
        topBar = Ui.row(this);
        topBar.setBackgroundColor(Ui.color(this, R.color.ark_bar));
        topBar.setPadding(Ui.dp(this, back ? 4 : 16), Ui.dp(this, 8), Ui.dp(this, 8), Ui.dp(this, 8));
        topBar.setMinimumHeight(Ui.dp(this, 56));
        if (back) {
            TextView up = Ui.label(this, "‹", 30, Ui.text(this), false);
            up.setGravity(Gravity.CENTER);
            up.setContentDescription(getString(R.string.back));
            Ui.clickable(up, v -> finish());
            topBar.addView(up, Ui.lp(Ui.dp(this, 44), Ui.dp(this, 44)));
        }
        LinearLayout titles = Ui.column(this);
        titleView = Ui.oneLine(Ui.label(this, title, 18, Ui.text(this), true));
        subtitleView = Ui.oneLine(Ui.small(this, ""));
        subtitleView.setVisibility(View.GONE);
        titles.addView(titleView);
        titles.addView(subtitleView);
        topBar.addView(titles, Ui.lp(0, Ui.WRAP, 1));
        actions = Ui.row(this);
        topBar.addView(actions);
        root.addView(topBar, Ui.lp(Ui.MATCH, Ui.WRAP));
        root.addView(Ui.divider(this));

        stage = new FrameLayout(this);
        refresher = new SwipeRefreshLayout(this);
        refresher.setColorSchemeColors(Ui.accent(this));
        refresher.setProgressBackgroundColorSchemeColor(Ui.cardColor(this));
        refresher.setOnRefreshListener(this::reload);
        scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        content = Ui.column(this);
        content.setPadding(Ui.dp(this, 14), Ui.dp(this, 8), Ui.dp(this, 14), Ui.dp(this, 24));
        scroll.addView(content, new FrameLayout.LayoutParams(Ui.MATCH, Ui.WRAP));
        refresher.addView(scroll);
        stage.addView(refresher, new FrameLayout.LayoutParams(Ui.MATCH, Ui.MATCH));
        root.addView(stage, Ui.lp(Ui.MATCH, 0, 1));
        setContentView(root);
    }

    protected void setSubtitle(CharSequence text) {
        if (subtitleView == null) return;
        subtitleView.setText(text);
        subtitleView.setVisibility(text == null || text.length() == 0 ? View.GONE : View.VISIBLE);
    }

    protected TextView addAction(CharSequence label, View.OnClickListener listener) {
        TextView action = Ui.label(this, label, 14, Ui.accent(this), true);
        action.setGravity(Gravity.CENTER);
        action.setPadding(Ui.dp(this, 10), Ui.dp(this, 8), Ui.dp(this, 10), Ui.dp(this, 8));
        Ui.clickable(action, listener);
        actions.addView(action);
        return action;
    }

    protected ImageView addIconAction(int icon, CharSequence description, View.OnClickListener listener) {
        ImageView view = new ImageView(this);
        view.setImageResource(icon);
        view.setColorFilter(Ui.text(this));
        view.setContentDescription(description);
        view.setPadding(Ui.dp(this, 10), Ui.dp(this, 10), Ui.dp(this, 10), Ui.dp(this, 10));
        Ui.clickable(view, listener);
        actions.addView(view, Ui.lp(Ui.dp(this, 44), Ui.dp(this, 44)));
        return view;
    }

    /** Pull-to-refresh / Retry target. */
    protected abstract void reload();

    protected void showLoading() {
        content.removeAllViews();
        TextView loading = Ui.small(this, getString(R.string.loading));
        loading.setGravity(Gravity.CENTER);
        loading.setPadding(0, Ui.dp(this, 48), 0, 0);
        content.addView(loading, Ui.lp(Ui.MATCH, Ui.WRAP));
    }

    protected void doneRefreshing() {
        if (refresher != null) refresher.setRefreshing(false);
    }

    /** Offline / error state in the content area. */
    protected void showError(ApiException error) {
        doneRefreshing();
        if (error.isSignedOut()) {
            signedOut();
            return;
        }
        content.removeAllViews();
        content.addView(stateView(this, error, this::reload));
    }

    public static LinearLayout stateView(BaseActivity a, ApiException error, Runnable retry) {
        LinearLayout box = Ui.column(a);
        box.setGravity(Gravity.CENTER_HORIZONTAL);
        box.setPadding(Ui.dp(a, 12), Ui.dp(a, 48), Ui.dp(a, 12), Ui.dp(a, 24));
        boolean offline = error.isUnreachable();
        TextView title = Ui.label(a, a.getString(offline ? R.string.state_offline : R.string.state_error),
                18, Ui.text(a), true);
        title.setGravity(Gravity.CENTER);
        box.addView(title);
        String detail = offline ? a.getString(R.string.state_offline_detail, a.session == null ? ""
                : a.session.serverUrl) : error.getMessage();
        if (ApiException.TUNNEL_DOWN.equals(error.getCode())) {
            detail = a.getString(R.string.state_tunnel_down_detail, a.session == null ? "" : a.session.serverUrl);
        }
        if (error.isReadOnly()) detail = a.getString(R.string.read_only_blocked);
        TextView body = Ui.small(a, detail);
        body.setGravity(Gravity.CENTER);
        body.setPadding(0, Ui.dp(a, 6), 0, Ui.dp(a, 14));
        box.addView(body);
        if (retry != null) {
            android.widget.Button button = Ui.button(a, a.getString(R.string.retry), true, false);
            button.setOnClickListener(v -> retry.run());
            box.addView(button, Ui.lp(Ui.WRAP, Ui.WRAP));
        }
        ServerEntry entry = a.session == null ? null : Accounts.servers(a).get(a.session.serverUrl);
        if (offline && entry != null && entry.isTunnel()) {
            // A quick tunnel restarted under a new address: re-link (scan or short code).
            android.widget.Button relink = Ui.button(a, a.getString(R.string.relink_prompt), false, false);
            relink.setOnClickListener(v -> {
                a.startActivity(MainActivity.relink(a, entry.getUrl()));
                a.finish();
            });
            box.addView(relink, Ui.margins(a, Ui.WRAP, Ui.WRAP, 0, 10, 0, 0));
            TextView note = Ui.small(a, a.getString(R.string.relink_network_note));
            note.setGravity(Gravity.CENTER);
            note.setPadding(0, Ui.dp(a, 10), 0, 0);
            box.addView(note);
        }
        return box;
    }

    protected void toast(CharSequence text) {
        Toast.makeText(this, text, Toast.LENGTH_SHORT).show();
    }

    /** For writes: explain read-only / failures without replacing the screen. */
    protected void toastError(ApiException error) {
        if (error.isSignedOut()) {
            signedOut();
            return;
        }
        toast(error.isReadOnly() ? getString(R.string.read_only_blocked)
                : error.isUnreachable() ? getString(R.string.state_offline) : error.getMessage());
    }

    protected <T> void call(Async.Work<T> work, Async.Done<T> done) {
        Async.run(this, work, done, this::toastError);
    }

    /** Token revoked or expired: forget it and go back to the server list. */
    protected void signedOut() {
        if (session != null) Accounts.forget(this, session.serverUrl);
        toServers(getString(R.string.signed_out_revoked));
    }

    protected void toServers(String message) {
        Intent intent = new Intent(this, MainActivity.class);
        intent.addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_NEW_TASK
                | Intent.FLAG_ACTIVITY_CLEAR_TASK);
        if (message != null) intent.putExtra(MainActivity.EXTRA_MESSAGE, message);
        startActivity(intent);
        finish();
    }

    /** Opens the embedded web app on a route (same session, same origin only). */
    public void openWeb(String route) {
        startActivity(WebActivity.intent(this, route == null ? WebRoutes.HOME : route));
    }
}
