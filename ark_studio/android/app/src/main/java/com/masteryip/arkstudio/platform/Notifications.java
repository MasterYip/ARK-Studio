package com.masteryip.arkstudio.platform;

import android.Manifest;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.os.Build;

import androidx.annotation.NonNull;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import androidx.work.BackoffPolicy;
import androidx.work.Constraints;
import androidx.work.ExistingPeriodicWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.PeriodicWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import com.masteryip.arkstudio.NotifyOpenActivity;
import com.masteryip.arkstudio.R;
import com.masteryip.arkstudio.RunActivity;
import com.masteryip.arkstudio.ShellActivity;
import com.masteryip.arkstudio.TaskActivity;
import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.ServerEntry;
import com.masteryip.arkstudio.data.ServerUrl;
import com.masteryip.arkstudio.model.NotifyFeed;
import com.masteryip.arkstudio.model.Watch;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.Calendar;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

/**
 * Opt-in notifications without FCM (no Google services in China): a
 * WorkManager periodic job (15 minutes, network required, exponential
 * backoff on errors) plus a one-off check when the app comes to the
 * foreground read each signed-in server's feed ({@code GET
 * /api/notifications}) and post what needs the person: new reviews,
 * decisions and approval requests, session errors. Notifications are grouped
 * per server and project with a summary; tapping one opens the item
 * ({@link NotifyOpenActivity}). Servers without the feed fall back to the
 * older run / review / GPU diff of {@link Watch} for the selected project.
 */
public final class Notifications {
    /** Channel of the older diff ({@link Watch}) for servers without the feed. */
    public static final String CHANNEL = "ark_activity";
    private static final String WORK = "ark_poll";
    private static final String WATCH_PREFS = "ark_watch";
    private static final String FEED_PREFS = "ark_feed";
    private static final String KEY_SETTINGS = "settings";
    private static final String KEY_LAST_POLL = "last_poll";
    private static final long NOW_THROTTLE_MS = 60_000;
    private static final String GROUP_PREFIX = "ark:";

    private Notifications() {
    }

    // ------------------------------------------------------------ channels

    public static void ensureChannels(Context c) {
        NotificationManager manager = c.getSystemService(NotificationManager.class);
        if (manager == null) return;
        channel(manager, NotifyFeed.CH_REVIEW, c.getString(R.string.notify_ch_review),
                c.getString(R.string.notify_ch_review_desc), NotificationManager.IMPORTANCE_DEFAULT);
        channel(manager, NotifyFeed.CH_DECISION, c.getString(R.string.notify_ch_decision),
                c.getString(R.string.notify_ch_decision_desc), NotificationManager.IMPORTANCE_HIGH);
        channel(manager, NotifyFeed.CH_SESSION, c.getString(R.string.notify_ch_session),
                c.getString(R.string.notify_ch_session_desc), NotificationManager.IMPORTANCE_HIGH);
        channel(manager, NotifyFeed.CH_SESSION_DONE, c.getString(R.string.notify_ch_session_done),
                c.getString(R.string.notify_ch_session_done_desc), NotificationManager.IMPORTANCE_DEFAULT);
        channel(manager, NotifyFeed.CH_ASSIGNED, c.getString(R.string.notify_ch_assigned),
                c.getString(R.string.notify_ch_assigned_desc), NotificationManager.IMPORTANCE_DEFAULT);
        channel(manager, NotifyFeed.CH_OTHER, c.getString(R.string.notify_ch_other),
                c.getString(R.string.notify_ch_other_desc), NotificationManager.IMPORTANCE_LOW);
        channel(manager, CHANNEL, c.getString(R.string.notify_channel), c.getString(R.string.notify_channel_desc),
                NotificationManager.IMPORTANCE_DEFAULT);
    }

    /** Older name, kept for callers. */
    public static void ensureChannel(Context c) {
        ensureChannels(c);
    }

    private static void channel(NotificationManager manager, String id, String name, String desc, int importance) {
        if (manager.getNotificationChannel(id) != null) return;
        NotificationChannel channel = new NotificationChannel(id, name, importance);
        channel.setDescription(desc);
        manager.createNotificationChannel(channel);
    }

    public static boolean permitted(Context c) {
        return Build.VERSION.SDK_INT < 33 || c.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                == PackageManager.PERMISSION_GRANTED;
    }

    // ---------------------------------------------------------- scheduling

    private static Constraints network() {
        return new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build();
    }

    public static void schedulePolling(Context c) {
        PeriodicWorkRequest request = new PeriodicWorkRequest.Builder(PollWorker.class, 15, TimeUnit.MINUTES)
                .setConstraints(network())
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 1, TimeUnit.MINUTES)
                .build();
        WorkManager.getInstance(c.getApplicationContext())
                .enqueueUniquePeriodicWork(WORK, ExistingPeriodicWorkPolicy.KEEP, request);
    }

    public static void cancelPolling(Context c) {
        WorkManager.getInstance(c.getApplicationContext()).cancelUniqueWork(WORK);
    }

    /** Re-arm on app start when enabled and signed in. */
    public static void sync(Context c) {
        if (Accounts.notificationsEnabled(c) && Accounts.active(c) != null) schedulePolling(c);
        else cancelPolling(c);
    }

    private static final java.util.concurrent.atomic.AtomicBoolean CHECKING =
            new java.util.concurrent.atomic.AtomicBoolean();

    /**
     * One check now (app opened or resumed), at most once a minute. It runs
     * on a plain background thread while the app is open: WorkManager would
     * wait for a validated network, which a LAN-only server (or an emulator)
     * may never have.
     */
    public static void checkSoon(Context c) {
        if (!Accounts.notificationsEnabled(c) || !permitted(c) || Accounts.active(c) == null) return;
        long last = feedPrefs(c).getLong(KEY_LAST_POLL, 0);
        if (System.currentTimeMillis() - last < NOW_THROTTLE_MS) return;
        if (!CHECKING.compareAndSet(false, true)) return;
        Context app = c.getApplicationContext();
        new Thread(() -> {
            try {
                pollAll(app);
            } finally {
                CHECKING.set(false);
            }
        }, "ark-notify-check").start();
    }

    // ------------------------------------------------------------- settings

    private static SharedPreferences feedPrefs(Context c) {
        return c.getApplicationContext().getSharedPreferences(FEED_PREFS, Context.MODE_PRIVATE);
    }

    public static NotifyFeed.Settings settings(Context c) {
        return NotifyFeed.Settings.fromJson(feedPrefs(c).getString(KEY_SETTINGS, ""));
    }

    public static void saveSettings(Context c, NotifyFeed.Settings settings) {
        feedPrefs(c).edit().putString(KEY_SETTINGS, settings.toJson()).apply();
    }

    /** Projects the feed reported per signed-in server: server url -> (id -> name). */
    public static List<Object[]> knownProjects(Context c) {
        List<Object[]> out = new ArrayList<>();
        for (ServerEntry entry : Accounts.servers(c).entries()) {
            AccountSession session = Accounts.session(c, entry.getUrl());
            if (session == null) continue;
            NotifyFeed.State state = NotifyFeed.State.fromJson(feedPrefs(c).getString(stateKey(session), ""));
            for (Map.Entry<String, String> p : state.projects.entrySet()) {
                out.add(new Object[]{serverKey(session), entry.getName().isEmpty() ? session.serverUrl : entry.getName(),
                        p.getKey(), p.getValue()});
            }
        }
        return out;
    }

    static String serverKey(AccountSession session) {
        return ServerUrl.identity(session.serverUrl);
    }

    private static String stateKey(AccountSession session) {
        return "state|" + serverKey(session) + "|" + session.user;
    }

    // ---------------------------------------------------------------- worker

    public static final class PollWorker extends Worker {
        public PollWorker(@NonNull Context context, @NonNull WorkerParameters params) {
            super(context, params);
        }

        @NonNull
        @Override
        public Result doWork() {
            Context c = getApplicationContext();
            if (!Accounts.notificationsEnabled(c)) return Result.success();
            return pollAll(c) ? Result.success() : Result.retry();
        }
    }

    /**
     * Polls every signed-in server; false when all of them were unreachable.
     * Synchronized: the periodic job and the on-open check never overlap.
     */
    static synchronized boolean pollAll(Context c) {
        feedPrefs(c).edit().putLong(KEY_LAST_POLL, System.currentTimeMillis()).apply();
        NotifyFeed.Settings settings = settings(c);
        boolean offline = false;
        boolean anyOk = false;
        for (ServerEntry entry : Accounts.servers(c).entries()) {
            AccountSession session = Accounts.session(c, entry.getUrl());
            if (session == null) continue;
            try {
                pollFeed(c, session, settings);
                anyOk = true;
            } catch (ApiException failed) {
                if (failed.getStatus() == 404) {
                    if (legacy(c, session)) anyOk = true;
                } else if (failed.isNetwork() || failed.getStatus() >= 500) {
                    offline = true;
                }
                // Signed out or refused: never notify, never loop.
            }
        }
        return !(offline && !anyOk);
    }

    static void pollFeed(Context c, AccountSession session, NotifyFeed.Settings settings) throws ApiException {
        ArkClient client = Accounts.client(session).withTimeouts(8000, 15000);
        String key = stateKey(session);
        NotifyFeed.State state = NotifyFeed.State.fromJson(feedPrefs(c).getString(key, ""));
        String path = "/api/notifications" + (state.initialized() ? "?since=" + ArkClient.enc(state.cursor) : "");
        NotifyFeed.Reply reply = NotifyFeed.parse(client.get(path));
        // Notifications the person swiped away (or that a reboot cleared) are
        // no longer shown: drop them so the summaries count what is there.
        String server = serverKey(session);
        java.util.Set<String> pruned = new java.util.HashSet<>();
        java.util.Set<Integer> live = activeIds(c);
        if (live != null) {
            java.util.Iterator<Map.Entry<String, JSONObject>> it = state.shown.entrySet().iterator();
            while (it.hasNext()) {
                Map.Entry<String, JSONObject> e = it.next();
                if (!live.contains(notificationId(server, e.getKey()))) {
                    pruned.add(e.getValue().optString("project"));
                    it.remove();
                }
            }
        }
        Calendar now = Calendar.getInstance();
        int minute = now.get(Calendar.HOUR_OF_DAY) * 60 + now.get(Calendar.MINUTE);
        NotifyFeed.Plan plan = NotifyFeed.plan(state, reply, settings, server,
                System.currentTimeMillis(), minute);
        feedPrefs(c).edit().putString(key, plan.next.toJson()).apply();
        for (String project : pruned) {
            if (!plan.groups.containsKey(project)) plan.emptied.add(project);
        }
        show(c, session, plan);
    }

    /** Ids of this app's notifications Android still shows (null when unknown). */
    static java.util.Set<Integer> activeIds(Context c) {
        NotificationManager manager = c.getSystemService(NotificationManager.class);
        if (manager == null) return null;
        try {
            java.util.Set<Integer> out = new java.util.HashSet<>();
            for (android.service.notification.StatusBarNotification n : manager.getActiveNotifications()) {
                out.add(n.getId());
            }
            return out;
        } catch (RuntimeException unavailable) {
            return null;
        }
    }

    /** Posts and withdraws per plan and refreshes the project summaries. */
    static void show(Context c, AccountSession session, NotifyFeed.Plan plan) {
        NotificationManagerCompat manager = NotificationManagerCompat.from(c);
        String server = serverKey(session);
        for (String id : plan.cancel) manager.cancel(notificationId(server, id));
        for (String project : plan.emptied) manager.cancel(summaryId(server, project));
        if (!permitted(c)) return;
        ensureChannels(c);
        try {
            for (NotifyFeed.Item item : plan.post) {
                manager.notify(notificationId(server, item.id), build(c, session, item, plan.silent).build());
            }
            for (NotifyFeed.Group group : plan.groups.values()) {
                manager.notify(summaryId(server, group.projectId), summary(c, session, group, plan.silent).build());
            }
        } catch (SecurityException revoked) {
            // permission withdrawn between check and post
        }
    }

    static int notificationId(String server, String id) {
        return (server + "|" + id).hashCode();
    }

    static int summaryId(String server, String project) {
        return (GROUP_PREFIX + server + "|" + project).hashCode();
    }

    static String kindLabel(Context c, String kind) {
        switch (kind) {
            case NotifyFeed.KIND_REVIEW:
                return c.getString(R.string.notify_kind_review);
            case NotifyFeed.KIND_DECISION:
                return c.getString(R.string.notify_kind_decision);
            case NotifyFeed.KIND_APPROVAL:
                return c.getString(R.string.notify_kind_approval);
            case NotifyFeed.KIND_SESSION_ERROR:
                return c.getString(R.string.notify_kind_session_error);
            case NotifyFeed.KIND_ASSIGNED:
                return c.getString(R.string.notify_kind_assigned);
            default:
                return c.getString(R.string.notify_kind_session_done);
        }
    }

    private static NotificationCompat.Builder build(Context c, AccountSession session, NotifyFeed.Item item,
                                                    boolean silent) {
        String server = serverKey(session);
        Intent open = NotifyOpenActivity.intent(c, session.serverUrl, item);
        PendingIntent pending = PendingIntent.getActivity(c, notificationId(server, item.id), open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        String title = item.projectName.isEmpty() ? kindLabel(c, item.kind)
                : c.getString(R.string.notify_item_title, item.projectName, kindLabel(c, item.kind));
        String text = itemText(c, item);
        NotificationCompat.Builder b = new NotificationCompat.Builder(c, NotifyFeed.channelOf(item.kind))
                .setSmallIcon(R.drawable.ic_stat_ark)
                .setContentTitle(title)
                .setContentText(text)
                .setAutoCancel(true)
                .setContentIntent(pending)
                .setGroup(GROUP_PREFIX + server + "|" + item.projectId)
                .setSilent(silent)
                .setCategory("high".equals(item.priority) ? NotificationCompat.CATEGORY_REMINDER
                        : NotificationCompat.CATEGORY_STATUS)
                .setPriority("high".equals(item.priority) ? NotificationCompat.PRIORITY_HIGH
                        : "low".equals(item.priority) ? NotificationCompat.PRIORITY_LOW
                        : NotificationCompat.PRIORITY_DEFAULT);
        if (!item.body.isEmpty()) {
            b.setStyle(new NotificationCompat.BigTextStyle().bigText(text + "\n" + item.body));
        }
        return b;
    }

    /** The notification's line: the title (with a count), or who assigned what. */
    static String itemText(Context c, NotifyFeed.Item item) {
        if (NotifyFeed.KIND_SESSION_DONE.equals(item.kind)) return c.getString(R.string.notify_session_done, item.title);
        if (NotifyFeed.KIND_SESSION_ERROR.equals(item.kind)) {
            return item.body.isEmpty() ? c.getString(R.string.notify_session_error, item.title)
                    : c.getString(R.string.notify_session_error_reason, item.title, item.body);
        }
        if (NotifyFeed.KIND_ASSIGNED.equals(item.kind) && !item.byName.isEmpty()) {
            return item.count > 1 ? c.getString(R.string.notify_assigned_many, item.byName, item.count)
                    : c.getString(R.string.notify_assigned_one, item.byName, item.title);
        }
        return item.count > 1 ? c.getString(R.string.notify_item_count, item.title, item.count) : item.title;
    }

    /** "PredActor_Rebuttal: 3 new for review · 1 decision" with one line per item. */
    static String summaryText(Context c, NotifyFeed.Group g) {
        List<String> parts = new ArrayList<>();
        int[][] order = {
                {0, R.string.notify_sum_review}, {1, R.string.notify_sum_decision}, {2, R.string.notify_sum_approval},
                {3, R.string.notify_sum_error}, {4, R.string.notify_sum_done}, {5, R.string.notify_sum_assigned}};
        String[] kinds = {NotifyFeed.KIND_REVIEW, NotifyFeed.KIND_DECISION, NotifyFeed.KIND_APPROVAL,
                NotifyFeed.KIND_SESSION_ERROR, NotifyFeed.KIND_SESSION_DONE, NotifyFeed.KIND_ASSIGNED};
        for (int[] o : order) {
            Integer n = g.counts.get(kinds[o[0]]);
            if (n != null && n > 0) parts.add(c.getString(o[1], n));
        }
        return android.text.TextUtils.join(c.getString(R.string.notify_sum_sep), parts);
    }

    private static NotificationCompat.Builder summary(Context c, AccountSession session, NotifyFeed.Group g,
                                                      boolean silent) {
        String server = serverKey(session);
        String text = summaryText(c, g);
        String name = g.projectName.isEmpty() ? c.getString(R.string.app_name) : g.projectName;
        NotificationCompat.InboxStyle inbox = new NotificationCompat.InboxStyle()
                .setBigContentTitle(name).setSummaryText(text);
        for (String line : g.lines) inbox.addLine(line);
        Intent open = NotifyOpenActivity.projectIntent(c, session.serverUrl, g.projectId);
        PendingIntent pending = PendingIntent.getActivity(c, summaryId(server, g.projectId), open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        String channel = NotifyFeed.CH_REVIEW;
        for (String kind : g.counts.keySet()) {
            if (!NotifyFeed.KIND_REVIEW.equals(kind)) channel = NotifyFeed.channelOf(kind);
        }
        return new NotificationCompat.Builder(c, channel)
                .setSmallIcon(R.drawable.ic_stat_ark)
                .setContentTitle(name)
                .setContentText(text)
                .setStyle(inbox)
                .setNumber(g.total())
                .setGroup(GROUP_PREFIX + server + "|" + g.projectId)
                .setGroupSummary(true)
                .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
                .setOnlyAlertOnce(true)
                .setSilent(silent)
                .setAutoCancel(true)
                .setContentIntent(pending);
    }

    /** Removes every feed notification and forgets the feed state (notifications turned off). */
    public static void clearAll(Context c) {
        NotificationManagerCompat.from(c).cancelAll();
        SharedPreferences prefs = feedPrefs(c);
        SharedPreferences.Editor edit = prefs.edit();
        for (String key : prefs.getAll().keySet()) {
            if (key.startsWith("state|")) edit.remove(key);
        }
        edit.apply();
    }

    // ----------------------------------------------- servers without the feed

    /** The older diff for the selected project of a server without the feed. */
    static boolean legacy(Context c, AccountSession session) {
        ArkClient client = Accounts.client(session).withTimeouts(8000, 15000);
        JSONArray runs;
        try {
            runs = client.get("/api/agent-sessions").optJSONArray("sessions");
        } catch (ApiException failed) {
            return false;
        }
        JSONArray review = optional(client, "/api/units?type=task&status=review&limit=200", "items");
        JSONObject gpu;
        try {
            gpu = client.get("/api/gpu/dashboard");
        } catch (ApiException ignored) {
            gpu = null;
        }
        JSONArray jobs = gpu == null ? null : gpu.optJSONArray("jobs");
        String key = ServerUrl.identity(session.serverUrl) + "|" + session.projectId;
        String saved = c.getSharedPreferences(WATCH_PREFS, Context.MODE_PRIVATE).getString(key, "");
        List<Watch.Event> events = new ArrayList<>();
        Watch.Snapshot next = Watch.diff(Watch.Snapshot.fromJson(saved), runs, review, jobs, session.user, events);
        c.getSharedPreferences(WATCH_PREFS, Context.MODE_PRIVATE).edit().putString(key, next.toJson()).apply();
        for (Watch.Event event : events) post(c, event);
        return true;
    }

    private static JSONArray optional(ArkClient client, String path, String key) {
        try {
            return client.get(path).optJSONArray(key);
        } catch (ApiException failed) {
            return null;
        }
    }

    static void post(Context c, Watch.Event event) {
        if (!permitted(c)) return;
        ensureChannels(c);
        String title;
        Intent open;
        switch (event.type) {
            case Watch.RUN_FAILED:
                title = c.getString(R.string.notify_run_failed);
                open = RunActivity.intent(c, event.id, event.title);
                break;
            case Watch.RUN_FINISHED:
                title = c.getString(R.string.notify_run_finished);
                open = RunActivity.intent(c, event.id, event.title);
                break;
            case Watch.TASK_REVIEW:
                title = c.getString(R.string.notify_task_review);
                open = TaskActivity.intent(c, event.id);
                break;
            default:
                title = c.getString(R.string.notify_gpu_finished, event.detail);
                open = ShellActivity.intent(c, ShellActivity.TAB_GPU);
                break;
        }
        open.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        PendingIntent pending = PendingIntent.getActivity(c, (event.type + event.id).hashCode(), open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        NotificationCompat.Builder builder = new NotificationCompat.Builder(c, CHANNEL)
                .setSmallIcon(R.drawable.ic_stat_ark)
                .setContentTitle(title)
                .setContentText(event.title)
                .setAutoCancel(true)
                .setContentIntent(pending)
                .setPriority(NotificationCompat.PRIORITY_DEFAULT);
        try {
            NotificationManagerCompat.from(c).notify((event.type + event.id).hashCode(), builder.build());
        } catch (SecurityException revoked) {
            // permission withdrawn between check and post
        }
    }
}
