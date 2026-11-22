package com.masteryip.arkstudio.update;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.IntentSender;
import android.content.SharedPreferences;
import android.content.pm.PackageInfo;
import android.content.pm.PackageInstaller;
import android.content.pm.PackageManager;
import android.content.pm.Signature;
import android.os.Build;

import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;

import com.masteryip.arkstudio.BuildConfig;
import com.masteryip.arkstudio.R;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.Set;

/**
 * Finds, downloads, verifies and installs app updates. The paired ARK server
 * is asked first ({@code /download/android-apps.json}); GitHub (often
 * unreachable in China) only when the server has no answer. The download is
 * checked against the published SHA-256, then its package, version code and
 * signing certificate are compared with the installed app before it goes to
 * {@link PackageInstaller}.
 */
public final class Updater {
    public static final String CHANNEL = "ark_update";
    private static final String PREFS = "ark_update";
    private static final String KEY_AUTO = "auto";
    private static final String KEY_NOTIFIED = "notified";
    private static final String KEY_LAST = "last_check";
    private static final int NOTIFY_ID = 0x41524b55;
    static final String ACTION_INSTALLED = "com.masteryip.arkstudio.update.INSTALL_STATUS";

    private Updater() {
    }

    /** The result of one check. */
    public static final class Check {
        public UpdateModel.Entry entry;
        public int decision = UpdateModel.UNKNOWN;
        /** Base URL used for relative entry URLs ("" for GitHub). */
        public String base = "";
        public String error = "";
    }

    public interface Progress {
        void update(long done, long total);
    }

    // ------------------------------------------------------------- settings

    private static SharedPreferences prefs(Context c) {
        return c.getApplicationContext().getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    public static boolean autoCheck(Context c) {
        return prefs(c).getBoolean(KEY_AUTO, true);
    }

    public static void setAutoCheck(Context c, boolean on) {
        prefs(c).edit().putBoolean(KEY_AUTO, on).apply();
        UpdateWorker.sync(c);
    }

    public static long lastCheck(Context c) {
        return prefs(c).getLong(KEY_LAST, 0);
    }

    public static long installedCode() {
        return BuildConfig.VERSION_CODE;
    }

    // ---------------------------------------------------------------- check

    /** Asks the server, then GitHub. Runs on a background thread. */
    public static Check check(Context c) {
        Check out = new Check();
        String key = UpdateModel.KEY;
        String base = UpdateServer.base(c);
        String serverError = "";
        if (base != null && !base.isEmpty()) {
            try {
                JSONObject manifest = new JSONObject(fetchText(join(base, UpdateModel.MANIFEST_PATH), 256 << 10));
                UpdateModel.Entry entry = UpdateModel.parseServer(manifest, key);
                if (entry != null && entry.usable()) {
                    out.entry = entry;
                    out.base = base;
                }
            } catch (Exception failed) {
                serverError = String.valueOf(failed.getMessage());
            }
        }
        if (out.entry == null) {
            try {
                JSONObject release = new JSONObject(fetchText(UpdateModel.GITHUB_LATEST, 4 << 20));
                out.entry = UpdateModel.parseGitHub(release, key, null);
                String sums = UpdateModel.sumsUrl(release);
                if (out.entry != null && out.entry.sha256.isEmpty() && !sums.isEmpty()) {
                    out.entry = UpdateModel.parseGitHub(release, key, UpdateModel.parseSums(fetchText(sums, 256 << 10)));
                }
            } catch (Exception failed) {
                out.error = serverError.isEmpty() ? String.valueOf(failed.getMessage()) : serverError;
            }
        }
        out.decision = UpdateModel.decide(out.entry, installedCode(), BuildConfig.ARK_RELEASE);
        prefs(c).edit().putLong(KEY_LAST, System.currentTimeMillis()).apply();
        return out;
    }

    static String join(String base, String path) {
        if (path.startsWith("http://") || path.startsWith("https://")) return path;
        String b = base;
        int hash = b.indexOf('#');
        if (hash >= 0) b = b.substring(0, hash);
        while (b.endsWith("/")) b = b.substring(0, b.length() - 1);
        return b + (path.startsWith("/") ? path : "/" + path);
    }

    /** Opens url following redirects, also between http and https (the server redirects to GitHub). */
    static HttpURLConnection open(String url) throws IOException {
        String current = url;
        for (int hops = 0; hops < 6; hops++) {
            HttpURLConnection conn = (HttpURLConnection) new URL(current).openConnection();
            conn.setConnectTimeout(10000);
            conn.setReadTimeout(30000);
            conn.setInstanceFollowRedirects(false);
            conn.setRequestProperty("User-Agent", "ARKStudioAndroid/" + BuildConfig.VERSION_NAME);
            int code = conn.getResponseCode();
            if (code >= 300 && code < 400) {
                String location = conn.getHeaderField("Location");
                conn.disconnect();
                if (location == null) throw new IOException("redirect without a location");
                current = new URL(new URL(current), location).toString();
                continue;
            }
            if (code != 200) {
                conn.disconnect();
                throw new IOException("HTTP " + code);
            }
            return conn;
        }
        throw new IOException("too many redirects");
    }

    static String fetchText(String url, int limit) throws IOException {
        HttpURLConnection conn = open(url);
        try (InputStream in = conn.getInputStream()) {
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buffer = new byte[16 * 1024];
            int n;
            while ((n = in.read(buffer)) > 0) {
                out.write(buffer, 0, n);
                if (out.size() > limit) throw new IOException("reply too large");
            }
            return new String(out.toByteArray(), StandardCharsets.UTF_8);
        } finally {
            conn.disconnect();
        }
    }

    // ------------------------------------------------------------- download

    /** Downloads the entry into the app's cache (no storage permission needed). */
    public static File download(Context c, Check check, Progress progress) throws IOException {
        File dir = new File(c.getCacheDir(), "updates");
        if (!dir.isDirectory() && !dir.mkdirs()) throw new IOException("no cache folder");
        File[] old = dir.listFiles();
        if (old != null) for (File f : old) //noinspection ResultOfMethodCallIgnored
            f.delete();
        File target = new File(dir, "update.apk");
        HttpURLConnection conn = open(join(check.base, check.entry.url));
        long total = conn.getContentLengthLong() > 0 ? conn.getContentLengthLong() : check.entry.size;
        try (InputStream in = conn.getInputStream(); OutputStream out = new FileOutputStream(target)) {
            byte[] buffer = new byte[64 * 1024];
            long done = 0;
            int n;
            while ((n = in.read(buffer)) > 0) {
                out.write(buffer, 0, n);
                done += n;
                if (done > (200L << 20)) throw new IOException("download too large");
                if (progress != null) progress.update(done, total);
            }
        } finally {
            conn.disconnect();
        }
        return target;
    }

    public static String sha256(File file) throws IOException {
        try (InputStream in = new FileInputStream(file)) {
            return UpdateModel.sha256Hex(in);
        }
    }

    // -------------------------------------------------------------- inspect

    /** The archive's package, version code and signer digests. */
    public static final class Archive {
        public String packageName = "";
        public long versionCode = -1;
        public String versionName = "";
        public Set<String> signers = new HashSet<>();
    }

    @SuppressWarnings("deprecation")
    public static Archive inspect(Context c, File apk) {
        Archive out = new Archive();
        PackageManager pm = c.getPackageManager();
        int flags = Build.VERSION.SDK_INT >= 28 ? PackageManager.GET_SIGNING_CERTIFICATES : PackageManager.GET_SIGNATURES;
        PackageInfo info = pm.getPackageArchiveInfo(apk.getAbsolutePath(), flags);
        if (info == null) return out;
        out.packageName = info.packageName == null ? "" : info.packageName;
        out.versionCode = Build.VERSION.SDK_INT >= 28 ? info.getLongVersionCode() : info.versionCode;
        out.versionName = info.versionName == null ? "" : info.versionName;
        out.signers = digests(info);
        return out;
    }

    /** SHA-256 digests of the installed app's signing certificates. */
    @SuppressWarnings("deprecation")
    public static Set<String> installedSigners(Context c) {
        try {
            int flags = Build.VERSION.SDK_INT >= 28 ? PackageManager.GET_SIGNING_CERTIFICATES : PackageManager.GET_SIGNATURES;
            return digests(c.getPackageManager().getPackageInfo(c.getPackageName(), flags));
        } catch (PackageManager.NameNotFoundException impossible) {
            return new HashSet<>();
        }
    }

    /** The APK's current signers (not past rotated keys), as Android compares them on install. */
    @SuppressWarnings("deprecation")
    static Set<String> digests(PackageInfo info) {
        Set<String> out = new HashSet<>();
        Signature[] signatures = null;
        if (Build.VERSION.SDK_INT >= 28 && info.signingInfo != null) {
            signatures = info.signingInfo.getApkContentsSigners();
        }
        if (signatures == null) signatures = info.signatures;
        if (signatures == null) return out;
        for (Signature s : signatures) out.add(UpdateModel.sha256Hex(s.toByteArray()));
        return out;
    }

    // -------------------------------------------------------------- install

    public static boolean mayInstall(Context c) {
        return Build.VERSION.SDK_INT < 26 || c.getPackageManager().canRequestPackageInstalls();
    }

    /** Hands the APK to the system installer; the result arrives in {@link InstallReceiver}. */
    public static void install(Context c, File apk) throws IOException {
        PackageInstaller installer = c.getPackageManager().getPackageInstaller();
        PackageInstaller.SessionParams params = new PackageInstaller.SessionParams(
                PackageInstaller.SessionParams.MODE_FULL_INSTALL);
        params.setAppPackageName(c.getPackageName());
        params.setSize(apk.length());
        int id = installer.createSession(params);
        try (PackageInstaller.Session session = installer.openSession(id)) {
            try (InputStream in = new FileInputStream(apk); OutputStream out = session.openWrite("base.apk", 0, apk.length())) {
                byte[] buffer = new byte[64 * 1024];
                int n;
                while ((n = in.read(buffer)) > 0) out.write(buffer, 0, n);
                session.fsync(out);
            }
            Intent status = new Intent(c, InstallReceiver.class).setAction(ACTION_INSTALLED);
            int flags = PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 31 ? PendingIntent.FLAG_MUTABLE : 0);
            PendingIntent pending = PendingIntent.getBroadcast(c, id, status, flags);
            IntentSender sender = pending.getIntentSender();
            session.commit(sender);
        }
    }

    // --------------------------------------------------------- notification

    public static void ensureChannel(Context c) {
        NotificationManager manager = c.getSystemService(NotificationManager.class);
        if (manager == null || manager.getNotificationChannel(CHANNEL) != null) return;
        NotificationChannel channel = new NotificationChannel(CHANNEL, c.getString(R.string.update_channel),
                NotificationManager.IMPORTANCE_DEFAULT);
        channel.setDescription(c.getString(R.string.update_channel_desc));
        manager.createNotificationChannel(channel);
    }

    /** Posts "update available" once per version. */
    public static void notifyAvailable(Context c, UpdateModel.Entry entry) {
        String label = UpdateModel.label(entry);
        String key = entry.versionCode >= 0 ? "code:" + entry.versionCode : "release:" + entry.releaseVersion;
        if (key.equals(prefs(c).getString(KEY_NOTIFIED, ""))) return;
        ensureChannel(c);
        Intent open = new Intent(c, UpdateActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        PendingIntent pending = PendingIntent.getActivity(c, NOTIFY_ID, open,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        NotificationCompat.Builder b = new NotificationCompat.Builder(c, CHANNEL)
                .setSmallIcon(android.R.drawable.stat_sys_download_done)
                .setContentTitle(c.getString(R.string.update_notify_title))
                .setContentText(c.getString(R.string.update_notify_text, label))
                .setAutoCancel(true)
                .setContentIntent(pending);
        try {
            NotificationManagerCompat.from(c).notify(NOTIFY_ID, b.build());
            prefs(c).edit().putString(KEY_NOTIFIED, key).apply();
        } catch (SecurityException noPermission) {
            // notifications not allowed: the settings screen still shows the update
        }
    }
}
