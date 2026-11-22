package com.masteryip.arkstudio.update;

import android.content.Context;

import androidx.annotation.NonNull;
import androidx.work.BackoffPolicy;
import androidx.work.Constraints;
import androidx.work.ExistingPeriodicWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.PeriodicWorkRequest;
import androidx.work.WorkManager;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import java.util.concurrent.TimeUnit;

/** The daily automatic update check (WorkManager, network required). */
public final class UpdateWorker extends Worker {
    private static final String WORK = "ark_update_check";

    public UpdateWorker(@NonNull Context context, @NonNull WorkerParameters params) {
        super(context, params);
    }

    /** Schedules or cancels the daily check per the setting. */
    public static void sync(Context c) {
        WorkManager wm = WorkManager.getInstance(c.getApplicationContext());
        if (!Updater.autoCheck(c)) {
            wm.cancelUniqueWork(WORK);
            return;
        }
        Constraints constraints = new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build();
        PeriodicWorkRequest request = new PeriodicWorkRequest.Builder(UpdateWorker.class, 24, TimeUnit.HOURS)
                .setConstraints(constraints)
                .setInitialDelay(1, TimeUnit.HOURS)
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.MINUTES)
                .build();
        wm.enqueueUniquePeriodicWork(WORK, ExistingPeriodicWorkPolicy.KEEP, request);
    }

    @NonNull
    @Override
    public Result doWork() {
        Context c = getApplicationContext();
        if (!Updater.autoCheck(c)) return Result.success();
        Updater.Check check = Updater.check(c);
        if (check.entry == null && !check.error.isEmpty()) return Result.retry();
        if (check.decision == UpdateModel.NEWER) Updater.notifyAvailable(c, check.entry);
        return Result.success();
    }
}
