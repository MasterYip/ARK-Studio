package com.masteryip.arkstudio;

import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.swiperefreshlayout.widget.SwipeRefreshLayout;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

/** Signed-in home: bottom navigation (Home, Tasks, Runs, GPU, More). */
public class ShellActivity extends BaseActivity {
    public static final int TAB_HOME = 0;
    public static final int TAB_TASKS = 1;
    public static final int TAB_RUNS = 2;
    public static final int TAB_GPU = 3;
    public static final int TAB_MORE = 4;
    private static final String EXTRA_TAB = "tab";

    private static final int[] ICONS = {R.drawable.ic_nav_home, R.drawable.ic_nav_tasks, R.drawable.ic_nav_runs,
            R.drawable.ic_nav_gpu, R.drawable.ic_nav_more};
    private static final int[] LABELS = {R.string.nav_home, R.string.nav_tasks, R.string.nav_runs,
            R.string.nav_gpu, R.string.nav_more};

    private final Page[] pages = new Page[5];
    private final LinearLayout[] tabs = new LinearLayout[5];
    private int current = -1;

    public static Intent intent(Context c, int tab) {
        return new Intent(c, ShellActivity.class).putExtra(EXTRA_TAB, tab)
                .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
    }

    /** One bottom-navigation destination. */
    public abstract static class Page {
        protected final ShellActivity a;
        protected final SwipeRefreshLayout refresher;
        protected final LinearLayout content;
        protected boolean loaded;
        private final Runnable ticker = this::tick;

        Page(ShellActivity activity) {
            a = activity;
            refresher = new SwipeRefreshLayout(a);
            refresher.setColorSchemeColors(Ui.accent(a));
            refresher.setProgressBackgroundColorSchemeColor(Ui.cardColor(a));
            refresher.setOnRefreshListener(() -> load(true));
            ScrollView scroll = new ScrollView(a);
            scroll.setFillViewport(true);
            content = Ui.column(a);
            content.setPadding(Ui.dp(a, 14), Ui.dp(a, 6), Ui.dp(a, 14), Ui.dp(a, 24));
            scroll.addView(content, new FrameLayout.LayoutParams(Ui.MATCH, Ui.WRAP));
            refresher.addView(scroll);
        }

        View view() {
            return refresher;
        }

        abstract CharSequence title();

        /** Fetch and render; {@code user} is true for pull-to-refresh/Retry. */
        abstract void load(boolean user);

        /** Auto-refresh period while visible (0 = none). */
        long periodMs() {
            return 0;
        }

        void show() {
            if (!loaded) {
                loading();
                load(false);
            } else if (periodMs() > 0) {
                load(false);
            }
            schedule();
        }

        void hide() {
            Async.main().removeCallbacks(ticker);
        }

        private void schedule() {
            Async.main().removeCallbacks(ticker);
            if (periodMs() > 0) Async.main().postDelayed(ticker, periodMs());
        }

        private void tick() {
            if (a.isFinishing() || a.pages[a.current] != this) return;
            load(false);
            schedule();
        }

        void loading() {
            content.removeAllViews();
            TextView text = Ui.small(a, a.getString(R.string.loading));
            text.setGravity(Gravity.CENTER);
            text.setPadding(0, Ui.dp(a, 48), 0, 0);
            content.addView(text, Ui.lp(Ui.MATCH, Ui.WRAP));
        }

        void failed(ApiException error) {
            refresher.setRefreshing(false);
            if (error.isSignedOut()) {
                a.signedOut();
                return;
            }
            if (loaded && !error.isUnreachable()) {
                a.toastError(error);
                return;
            }
            content.removeAllViews();
            content.addView(stateView(a, error, () -> {
                loading();
                load(true);
            }));
        }

        void rendered() {
            loaded = true;
            refresher.setRefreshing(false);
        }
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        buildFrame(getString(R.string.nav_home), false);
        stage.removeAllViews();
        pages[TAB_HOME] = new HomePage(this);
        pages[TAB_TASKS] = new TasksPage(this);
        pages[TAB_RUNS] = new RunsPage(this);
        pages[TAB_GPU] = new GpuPage(this);
        pages[TAB_MORE] = new MorePage(this);
        for (Page page : pages) {
            page.view().setVisibility(View.GONE);
            stage.addView(page.view(), new FrameLayout.LayoutParams(Ui.MATCH, Ui.MATCH));
        }
        root.addView(Ui.divider(this));
        root.addView(bottomNav(), Ui.lp(Ui.MATCH, Ui.WRAP));
        updateSubtitle();
        select(getIntent().getIntExtra(EXTRA_TAB, TAB_HOME));
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        if (ready() && intent.hasExtra(EXTRA_TAB)) select(intent.getIntExtra(EXTRA_TAB, TAB_HOME));
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (!ready() || current < 0) return;
        com.masteryip.arkstudio.data.AccountSession latest =
                com.masteryip.arkstudio.platform.Accounts.active(this);
        if (latest == null) {
            toServers(null);
            return;
        }
        boolean switched = !latest.serverUrl.equals(session.serverUrl) || !latest.projectId.equals(session.projectId);
        session = latest;
        api = com.masteryip.arkstudio.platform.Accounts.client(latest);
        updateSubtitle();
        if (switched) invalidatePages();
        pages[current].show();
        com.masteryip.arkstudio.platform.Notifications.checkSoon(this);
    }

    @Override
    protected void onPause() {
        super.onPause();
        if (current >= 0) pages[current].hide();
    }

    void updateSubtitle() {
        String project = session.projectName.isEmpty() ? session.serverUrl : session.projectName;
        setSubtitle(project + " · " + MainActivity.displayUser(session)
                + (session.isReadOnly() ? " · " + getString(R.string.perm_read_only) : ""));
    }

    /** After a project switch every page must refetch. */
    void invalidatePages() {
        for (Page page : pages) page.loaded = false;
    }

    void sessionChanged(com.masteryip.arkstudio.data.AccountSession updated) {
        session = updated;
        api = com.masteryip.arkstudio.platform.Accounts.client(updated);
        updateSubtitle();
        invalidatePages();
        pages[current].show();
    }

    private View bottomNav() {
        LinearLayout nav = Ui.row(this);
        nav.setBackgroundColor(Ui.color(this, R.color.ark_bar));
        nav.setPadding(0, Ui.dp(this, 4), 0, Ui.dp(this, 4));
        for (int i = 0; i < 5; i++) {
            LinearLayout tab = Ui.column(this);
            tab.setGravity(Gravity.CENTER);
            tab.setPadding(0, Ui.dp(this, 4), 0, Ui.dp(this, 4));
            ImageView icon = new ImageView(this);
            icon.setImageResource(ICONS[i]);
            tab.addView(icon, Ui.lp(Ui.dp(this, 24), Ui.dp(this, 24)));
            TextView label = Ui.label(this, getString(LABELS[i]), 11, Ui.muted(this), true);
            label.setGravity(Gravity.CENTER);
            tab.addView(label);
            tab.setContentDescription(getString(LABELS[i]));
            final int index = i;
            Ui.clickable(tab, v -> select(index));
            tabs[i] = tab;
            nav.addView(tab, Ui.lp(0, Ui.WRAP, 1));
        }
        return nav;
    }

    void select(int index) {
        if (index < 0 || index >= pages.length) index = TAB_HOME;
        if (current >= 0) {
            pages[current].hide();
            pages[current].view().setVisibility(View.GONE);
        }
        current = index;
        for (int i = 0; i < tabs.length; i++) {
            boolean on = i == index;
            int color = on ? Ui.accent(this) : Ui.muted(this);
            ((ImageView) tabs[i].getChildAt(0)).setColorFilter(color);
            ((TextView) tabs[i].getChildAt(1)).setTextColor(color);
            tabs[i].setSelected(on);
        }
        Page page = pages[index];
        page.view().setVisibility(View.VISIBLE);
        titleView.setText(page.title());
        page.show();
    }

    @Override
    protected void reload() {
        if (current >= 0) pages[current].load(true);
    }

    private final com.masteryip.arkstudio.model.BackGate backGate = new com.masteryip.arkstudio.model.BackGate();

    /** Home tab: a second back within 2.5 s goes to the server list, still signed in. */
    @Override
    public void onBackPressed() {
        if (current != TAB_HOME) {
            select(TAB_HOME);
            return;
        }
        if (backGate.press(System.currentTimeMillis())) {
            startActivity(MainActivity.serverList(this));
            finish();
        } else {
            toast(getString(R.string.back_again_servers));
        }
    }
}
