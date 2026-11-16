package com.masteryip.arkstudio.ui;

import android.app.Activity;
import android.os.Handler;
import android.os.Looper;

import com.masteryip.arkstudio.api.ApiException;

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Runs API calls off the main thread and delivers results only to a live activity. */
public final class Async {
    public interface Work<T> {
        T run() throws Exception;
    }

    public interface Done<T> {
        void ok(T value);
    }

    public interface Failed {
        void error(ApiException error);
    }

    private static final ExecutorService POOL = Executors.newFixedThreadPool(4);
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private Async() {
    }

    public static Handler main() {
        return MAIN;
    }

    public static <T> void run(Activity owner, Work<T> work, Done<T> done, Failed failed) {
        POOL.execute(() -> {
            T value = null;
            ApiException error = null;
            try {
                value = work.run();
            } catch (ApiException e) {
                error = e;
            } catch (Exception e) {
                error = new ApiException(-1, "CLIENT", e.getMessage() == null ? e.toString() : e.getMessage(),
                        0, e);
            }
            final T result = value;
            final ApiException problem = error;
            MAIN.post(() -> {
                if (owner.isFinishing() || owner.isDestroyed()) return;
                if (problem == null) {
                    if (done != null) done.ok(result);
                } else if (failed != null) {
                    failed.error(problem);
                }
            });
        });
    }
}
