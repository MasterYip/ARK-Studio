package com.masteryip.arkstudio.platform;

import android.Manifest;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;

import androidx.annotation.NonNull;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import androidx.work.Constraints;
import androidx.work.ExistingPeriodicWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.PeriodicWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import com.masteryip.arkstudio.R;
import com.masteryip.arkstudio.ShellActivity;
import com.masteryip.arkstudio.RunActivity;
import com.masteryip.arkstudio.TaskActivity;
import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.api.ArkClient;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.ServerUrl;
import com.masteryip.arkstudio.model.Watch;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

/**
 * Opt-in "push-like" notifications without FCM: a WorkManager periodic job
 * (15 minutes, network required) polls the signed-in server and notifies on
 * run finished/failed, task moved to review and own GPU job finished.
 */
public final class Notifications {
    public static final String CHANNEL = "ark_activity";
    private static final String WORK = "ark_poll";
    private static final String WATCH_PREFS = "ark_watch";

    private Notifications() {
    }

    public static void ensureChannel(Context c) {
        NotificationManager manager = c.getSystemService(NotificationManager.class);
        if (manager == null || manager.getNotificationChannel(CHANNEL) != null) return;
        NotificationChannel channel = new NotificationChannel(CHANNEL,
                c.getString(R.string.notify_channel), NotificationManager.IMPORTANCE_DEFAULT);
        channel.setDescription(c.getString(R.string.notify_channel_desc));
        manager.createNotificationChannel(channel);
    }

    public static boolean permitted(Context c) {
        return Build.VERSION.SDK_INT < 33 || c.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                == PackageManager.PERMISSION_GRANTED;
    }

    public static void schedulePolling(Context c) {
        Constraints constraints = new Constraints.Builder()
                .setRequiredNetworkType(NetworkType.CONNECTED).build();
        PeriodicWorkRequest request = new PeriodicWorkRequest.Builder(PollWorker.class, 15, TimeUnit.MINUTES)
                .setConstraints(constraints).build();
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

    public static final class PollWorker extends Worker {
        public PollWorker(@NonNull Context context, @NonNull WorkerParameters params) {
            super(context, params);
        }

        @NonNull
        @Override
        public Result doWork() {
            Context c = getApplicationContext();
            if (!Accounts.notificationsEnabled(c)) return Result.success();
            AccountSession session = Accounts.active(c);
            if (session == null) return Result.success();
            ArkClient client = Accounts.client(session).withTimeouts(8000, 15000);
            JSONArray runs;
            try {
                runs = client.get("/api/agent-sessions").optJSONArray("sessions");
            } catch (ApiException failed) {
                // Signed out or offline: never notify, never loop.
                return failed.isNetwork() ? Result.retry() : Result.success();
            }
            JSONArray review = optional(client, "/api/units?type=task&status=review&limit=200", "items");
            JSONObject gpu = null;
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
            return Result.success();
        }

        private static JSONArray optional(ArkClient client, String path, String key) {
            try {
                return client.get(path).optJSONArray(key);
            } catch (ApiException failed) {
                return null;
            }
        }
    }

    static void post(Context c, Watch.Event event) {
        if (!permitted(c)) return;
        ensureChannel(c);
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
