package com.masteryip.arkstudio.update;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.json.JSONObject;
import org.junit.Test;

import java.io.ByteArrayInputStream;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;

public class UpdateModelTest {
    private static final String SHA_A = "a".repeat(64);
    private static final String SHA_B = "b".repeat(64);

    @Test
    public void comparesReleaseVersions() {
        assertTrue(UpdateModel.compareVersions("v2.1.1", "2.1.0") > 0);
        assertTrue(UpdateModel.compareVersions("2.1.0", "v2.1.1") < 0);
        assertEquals(0, UpdateModel.compareVersions("v2.1", "2.1.0"));
        assertTrue(UpdateModel.compareVersions("2.10.0", "2.9.9") > 0);
        assertTrue(UpdateModel.compareVersions("2.1.0-rc.2", "2.1.0") < 0);
        assertTrue(UpdateModel.compareVersions("2.1.0", "2.1.0-rc.2") > 0);
        assertTrue(UpdateModel.compareVersions("2.1.0-rc.10", "2.1.0-rc.2") > 0);
        assertEquals(0, UpdateModel.compareVersions("2.1.0-rc.2", "v2.1.0-rc.2"));
    }

    @Test
    public void readsTheServerManifest() throws Exception {
        JSONObject manifest = new JSONObject("{\"apps\":{"
                + "\"native\":{\"package\":\"com.masteryip.arkstudio\",\"file\":\"ARK-Studio-android.apk\","
                + "\"url\":\"/download/ARK-Studio-android.apk\",\"source\":\"server\",\"version_code\":17,"
                + "\"version_name\":\"3.1\",\"size\":4000000,\"sha256\":\"" + SHA_A.toUpperCase() + "\","
                + "\"release_version\":\"v2.1.1\"}},"
                + "\"release\":{\"version\":\"v2.1.1\"}}");
        UpdateModel.Entry n = UpdateModel.parseServer(manifest, UpdateModel.KEY);
        assertEquals("server", n.source);
        assertEquals(17, n.versionCode);
        assertEquals(SHA_A, n.sha256);
        assertEquals("3.1 (17)", UpdateModel.label(n));
        assertEquals(UpdateModel.NEWER, UpdateModel.decide(n, 16, "2.1.1"));
        assertEquals(UpdateModel.CURRENT, UpdateModel.decide(n, 17, "2.1.1"));

        JSONObject fromRelease = new JSONObject("{\"apps\":{"
                + "\"native\":{\"package\":\"com.masteryip.arkstudio\",\"file\":\"ARK-Studio-android.apk\","
                + "\"url\":\"/download/ARK-Studio-android.apk\",\"source\":\"release\",\"version_code\":null,"
                + "\"version_name\":null,\"size\":3000000,\"sha256\":\"" + SHA_B + "\",\"release_version\":\"v2.1.1\"}},"
                + "\"release\":{\"version\":\"v2.1.1\"}}");
        UpdateModel.Entry w = UpdateModel.parseServer(fromRelease, UpdateModel.KEY);
        assertEquals(-1, w.versionCode);
        assertEquals("v2.1.1", UpdateModel.label(w));
        // A release asset compares the release tag with the release the app was built from.
        assertEquals(UpdateModel.NEWER, UpdateModel.decide(w, 99, "2.1.0"));
        assertEquals(UpdateModel.CURRENT, UpdateModel.decide(w, 1, "2.1.1"));
        assertEquals(UpdateModel.UNKNOWN, UpdateModel.decide(w, 1, ""));

        JSONObject none = new JSONObject("{\"apps\":{\"native\":{\"url\":\"/download/x.apk\",\"source\":\"none\"}}}");
        assertEquals(UpdateModel.UNKNOWN, UpdateModel.decide(UpdateModel.parseServer(none, "native"), 1, "2.1.0"));
        assertNull(UpdateModel.parseServer(new JSONObject("{}"), "native"));
    }

    @Test
    public void readsAGitHubReleaseWithItsChecksums() throws Exception {
        JSONObject release = new JSONObject("{\"tag_name\":\"v2.2.0\",\"assets\":["
                + "{\"name\":\"ARK-Studio-2.2.0-android-web.apk\",\"size\":3,\"browser_download_url\":\"https://x/web.apk\"},"
                + "{\"name\":\"ARK-Studio-2.2.0-android.apk\",\"size\":4,\"browser_download_url\":\"https://x/app.apk\"},"
                + "{\"name\":\"SHA256SUMS.txt\",\"size\":1,\"browser_download_url\":\"https://x/SHA256SUMS.txt\"}]}");
        assertEquals("https://x/SHA256SUMS.txt", UpdateModel.sumsUrl(release));
        Map<String, String> sums = UpdateModel.parseSums(SHA_A + "  ARK-Studio-2.2.0-android.apk\n"
                + SHA_B.toUpperCase() + " *ARK-Studio-2.2.0-android-web.apk\nnot a line\n");
        assertEquals(2, sums.size());
        UpdateModel.Entry app = UpdateModel.parseGitHub(release, "native", sums);
        assertEquals("https://x/app.apk", app.url);
        assertEquals(SHA_A, app.sha256);
        assertEquals(4, app.size);
        assertEquals("github", app.source);
        // An older release's WebView APK (-android-web) is never taken for the app.
        assertEquals("ARK-Studio-2.2.0-android.apk", app.file);
        assertEquals("", UpdateModel.parseGitHub(release, "native", null).sha256);
        // GitHub's asset digest wins.
        release.getJSONArray("assets").getJSONObject(1).put("digest", "sha256:" + SHA_B.toUpperCase());
        assertEquals(SHA_B, UpdateModel.parseGitHub(release, "native", sums).sha256);
    }

    @Test
    public void verifiesTheChecksum() throws Exception {
        byte[] data = "ARK".getBytes(StandardCharsets.UTF_8);
        String sum = UpdateModel.sha256Hex(new ByteArrayInputStream(data));
        assertEquals(64, sum.length());
        assertEquals(sum, UpdateModel.sha256Hex(data));
        assertTrue(UpdateModel.checksumMatches(sum.toUpperCase(), sum));
        assertFalse(UpdateModel.checksumMatches(SHA_A, sum));
        assertFalse(UpdateModel.checksumMatches("", sum));
        assertFalse(UpdateModel.checksumMatches(null, sum));
        // Known value: sha256("abc").
        assertEquals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
                UpdateModel.sha256Hex("abc".getBytes(StandardCharsets.UTF_8)));
    }

    @Test
    public void judgesTheDownloadedApk() {
        Set<String> debugKey = Collections.singleton("d1");
        Set<String> releaseKey = Collections.singleton("r1");
        String pkg = "com.masteryip.arkstudio";
        assertEquals(UpdateModel.OK, UpdateModel.checkArchive(pkg, 16, debugKey, pkg, 17, new HashSet<>(debugKey)));
        assertEquals(UpdateModel.SIGNATURE_MISMATCH, UpdateModel.checkArchive(pkg, 16, debugKey, pkg, 17, releaseKey));
        assertEquals(UpdateModel.NOT_NEWER, UpdateModel.checkArchive(pkg, 17, debugKey, pkg, 17, debugKey));
        assertEquals(UpdateModel.WRONG_PACKAGE, UpdateModel.checkArchive(pkg, 16, debugKey, pkg + ".web", 17, debugKey));
        assertEquals(UpdateModel.UNREADABLE, UpdateModel.checkArchive(pkg, 16, debugKey, "", 17, debugKey));
        assertEquals(UpdateModel.UNREADABLE, UpdateModel.checkArchive(pkg, 16, debugKey, pkg, 17, Collections.emptySet()));
        // Several signers must all match.
        Set<String> two = new HashSet<>(Arrays.asList("d1", "d2"));
        assertEquals(UpdateModel.SIGNATURE_MISMATCH, UpdateModel.checkArchive(pkg, 16, two, pkg, 17, debugKey));
    }

    @Test
    public void joinsServerPaths() {
        assertEquals("http://10.0.2.2:8890/download/a.apk", Updater.join("http://10.0.2.2:8890/", "/download/a.apk"));
        assertEquals("https://h/ark/download/a.apk", Updater.join("https://h/ark#/home", "/download/a.apk"));
        assertEquals("https://x/a.apk", Updater.join("http://h", "https://x/a.apk"));
    }
}
