package com.masteryip.arkstudio.update;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.IOException;
import java.io.InputStream;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * Update check logic of the app (pure Java, covered by JVM tests):
 * reading the server's {@code /download/android-apps.json}
 * ({@code ark_studio/server/internal/server/app_manifest.go}) or the GitHub
 * latest release, comparing versions, verifying the checksum and judging
 * the downloaded APK before it is installed.
 */
public final class UpdateModel {
    public static final String MANIFEST_PATH = "/download/android-apps.json";
    public static final String GITHUB_LATEST = "https://api.github.com/repos/MasterYip/ARK-Studio/releases/latest";
    public static final String SUMS_ASSET = "SHA256SUMS.txt";
    /** The app's key in the server manifest and in {@link #ASSETS}. */
    public static final String KEY = "native";

    /** Release asset names (.github/workflows/release.yml). */
    static final Map<String, Pattern> ASSETS = new HashMap<>();

    static {
        ASSETS.put(KEY, Pattern.compile("^ARK-Studio-.+-android\\.apk$"));
    }

    private UpdateModel() {
    }

    /** One downloadable APK. */
    public static final class Entry {
        public String packageName = "";
        public String file = "";
        /** Absolute, or a path on the server ("/download/..."). */
        public String url = "";
        /** "server", "release" or "github" (fetched from GitHub by the app). */
        public String source = "none";
        /** -1 when unknown (a release asset: compare releaseVersion). */
        public long versionCode = -1;
        public String versionName = "";
        public long size = -1;
        public String sha256 = "";
        public String releaseVersion = "";

        public boolean usable() {
            return !url.isEmpty() && !"none".equals(source);
        }
    }

    /** Reads one app of the server manifest; null when absent. */
    public static Entry parseServer(JSONObject manifest, String key) {
        JSONObject apps = manifest == null ? null : manifest.optJSONObject("apps");
        JSONObject o = apps == null ? null : apps.optJSONObject(key);
        if (o == null) return null;
        Entry e = new Entry();
        e.packageName = o.optString("package");
        e.file = o.optString("file");
        e.url = o.optString("url");
        e.source = o.optString("source", "none");
        e.versionCode = o.isNull("version_code") ? -1 : o.optLong("version_code", -1);
        e.versionName = o.isNull("version_name") ? "" : o.optString("version_name");
        e.size = o.isNull("size") ? -1 : o.optLong("size", -1);
        e.sha256 = o.isNull("sha256") ? "" : o.optString("sha256").toLowerCase(Locale.ROOT);
        e.releaseVersion = o.isNull("release_version") ? "" : o.optString("release_version");
        return e;
    }

    /** The SHA256SUMS.txt asset's download URL of a GitHub release, or "". */
    public static String sumsUrl(JSONObject release) {
        JSONArray assets = release == null ? null : release.optJSONArray("assets");
        for (int i = 0; assets != null && i < assets.length(); i++) {
            JSONObject a = assets.optJSONObject(i);
            if (a != null && SUMS_ASSET.equals(a.optString("name"))) return a.optString("browser_download_url");
        }
        return "";
    }

    /** Reads one app from a GitHub latest-release reply; null when the release has no such APK. */
    public static Entry parseGitHub(JSONObject release, String key, Map<String, String> sums) {
        Pattern pattern = ASSETS.get(key);
        JSONArray assets = release == null ? null : release.optJSONArray("assets");
        for (int i = 0; pattern != null && assets != null && i < assets.length(); i++) {
            JSONObject a = assets.optJSONObject(i);
            if (a == null || !pattern.matcher(a.optString("name")).matches()) continue;
            String url = a.optString("browser_download_url");
            if (!url.startsWith("https://")) continue;
            Entry e = new Entry();
            e.file = a.optString("name");
            e.url = url;
            e.source = "github";
            e.size = a.optLong("size", -1);
            e.releaseVersion = release.optString("tag_name");
            // GitHub's own digest of the asset, else the release's SHA256SUMS.txt.
            String digest = a.optString("digest");
            String sum = sums == null ? null : sums.get(e.file);
            if (digest.startsWith("sha256:") && digest.length() == 71) {
                e.sha256 = digest.substring(7).toLowerCase(Locale.ROOT);
            } else {
                e.sha256 = sum == null ? "" : sum;
            }
            return e;
        }
        return null;
    }

    /** "<sha256>  <file>" lines (sha256sum output). */
    public static Map<String, String> parseSums(String text) {
        Map<String, String> out = new HashMap<>();
        if (text == null) return out;
        for (String line : text.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length != 2 || fields[0].length() != 64) continue;
            String name = fields[1].startsWith("*") ? fields[1].substring(1) : fields[1];
            out.put(name, fields[0].toLowerCase(Locale.ROOT));
        }
        return out;
    }

    /**
     * Compares release versions ("v2.1.1", "2.1.0-rc.2"): numeric parts in
     * order, and a pre-release sorts before its release.
     */
    public static int compareVersions(String a, String b) {
        String[] x = split(a);
        String[] y = split(b);
        int[] nx = numbers(x[0]);
        int[] ny = numbers(y[0]);
        for (int i = 0; i < Math.max(nx.length, ny.length); i++) {
            int p = i < nx.length ? nx[i] : 0;
            int q = i < ny.length ? ny[i] : 0;
            if (p != q) return p < q ? -1 : 1;
        }
        if (x[1].isEmpty() != y[1].isEmpty()) return x[1].isEmpty() ? 1 : -1;
        int[] px = numbers(x[1]);
        int[] py = numbers(y[1]);
        for (int i = 0; i < Math.max(px.length, py.length); i++) {
            int p = i < px.length ? px[i] : 0;
            int q = i < py.length ? py[i] : 0;
            if (p != q) return p < q ? -1 : 1;
        }
        return x[1].compareTo(y[1]) < 0 ? -1 : x[1].equals(y[1]) ? 0 : 1;
    }

    private static String[] split(String v) {
        String s = v == null ? "" : v.trim();
        if (s.startsWith("v") || s.startsWith("V")) s = s.substring(1);
        int dash = s.indexOf('-');
        return dash < 0 ? new String[]{s, ""} : new String[]{s.substring(0, dash), s.substring(dash + 1)};
    }

    private static int[] numbers(String s) {
        if (s.isEmpty()) return new int[0];
        String[] parts = s.split("[^0-9]+");
        int n = 0;
        int[] out = new int[parts.length];
        for (String p : parts) {
            if (p.isEmpty()) continue;
            try {
                out[n++] = Integer.parseInt(p.length() > 9 ? p.substring(0, 9) : p);
            } catch (NumberFormatException skip) {
                // not a number
            }
        }
        int[] trimmed = new int[n];
        System.arraycopy(out, 0, trimmed, 0, n);
        return trimmed;
    }

    public static final int NEWER = 1;
    public static final int CURRENT = 0;
    public static final int UNKNOWN = -1;

    /**
     * Whether the entry is newer than the installed app: by version code when
     * the server read it from the APK, else by the release the app was built
     * from (BuildConfig.ARK_RELEASE).
     */
    public static int decide(Entry e, long installedCode, String builtRelease) {
        if (e == null || !e.usable()) return UNKNOWN;
        if (e.versionCode >= 0) return e.versionCode > installedCode ? NEWER : CURRENT;
        if (e.releaseVersion.isEmpty() || builtRelease == null || builtRelease.isEmpty()) return UNKNOWN;
        return compareVersions(e.releaseVersion, builtRelease) > 0 ? NEWER : CURRENT;
    }

    /** What to show for an available entry: its version name, else the release. */
    public static String label(Entry e) {
        if (e == null) return "";
        if (!e.versionName.isEmpty()) return e.versionCode >= 0 ? e.versionName + " (" + e.versionCode + ")" : e.versionName;
        return e.releaseVersion;
    }

    public static String sha256Hex(InputStream in) throws IOException {
        MessageDigest digest;
        try {
            digest = MessageDigest.getInstance("SHA-256");
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException(impossible);
        }
        byte[] buffer = new byte[64 * 1024];
        int n;
        while ((n = in.read(buffer)) > 0) digest.update(buffer, 0, n);
        return hex(digest.digest());
    }

    public static String sha256Hex(byte[] data) {
        try {
            return hex(MessageDigest.getInstance("SHA-256").digest(data));
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    static String hex(byte[] bytes) {
        StringBuilder sb = new StringBuilder(bytes.length * 2);
        for (byte b : bytes) sb.append(String.format(Locale.ROOT, "%02x", b & 0xff));
        return sb.toString();
    }

    /** The download matches the published checksum (a missing checksum never matches). */
    public static boolean checksumMatches(String expected, String actual) {
        return expected != null && expected.length() == 64 && expected.equalsIgnoreCase(actual);
    }

    public static final int OK = 0;
    public static final int WRONG_PACKAGE = 1;
    public static final int NOT_NEWER = 2;
    public static final int SIGNATURE_MISMATCH = 3;
    public static final int UNREADABLE = 4;

    /**
     * Judges a downloaded APK against the installed app before handing it to
     * the installer: same package, a higher version code, and the same
     * signing certificates (Android refuses anything else, with an unclear
     * error; a debug-signed test build and a CI build differ).
     */
    public static int checkArchive(String installedPackage, long installedCode, Set<String> installedSigners,
                                   String archivePackage, long archiveCode, Set<String> archiveSigners) {
        if (archivePackage == null || archivePackage.isEmpty()) return UNREADABLE;
        if (!archivePackage.equals(installedPackage)) return WRONG_PACKAGE;
        if (archiveCode <= installedCode) return NOT_NEWER;
        if (archiveSigners == null || archiveSigners.isEmpty() || installedSigners == null
                || installedSigners.isEmpty()) {
            return UNREADABLE;
        }
        return archiveSigners.equals(installedSigners) ? OK : SIGNATURE_MISMATCH;
    }
}
