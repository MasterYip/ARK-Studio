package com.masteryip.arkstudio.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class ServerStoreTest {

    @Test
    public void normalizesBareHostAndCanonicalPorts() {
        assertEquals("http://192.168.1.20:8891/", ServerUrl.normalize(" 192.168.1.20:8891 "));
        assertEquals("http://example.com/", ServerUrl.normalize("HTTP://EXAMPLE.COM:80"));
        assertEquals("https://example.com/", ServerUrl.normalize("https://Example.COM:443"));
        assertEquals(
                "https://example.com/ark?x=1#pair=tok",
                ServerUrl.normalize("https://EXAMPLE.com:443/ark?x=1#pair=tok"));
        assertEquals("http://[::1]:8891/", ServerUrl.normalize("http://[::1]:8891"));
    }

    @Test
    public void rejectsNonHttpAndMalformedUrls() {
        assertNull(ServerUrl.normalize(""));
        assertNull(ServerUrl.normalize(null));
        assertNull(ServerUrl.normalize("ftp://example.com"));
        assertNull(ServerUrl.normalize("javascript:alert(1)"));
        assertNull(ServerUrl.normalize("http://"));
        assertNull(ServerUrl.normalize("not a url"));
        assertNull(ServerUrl.normalize("http://example.com:99999/"));
    }

    @Test
    public void identityIgnoresFragmentQueryAndTrailingSlash() {
        assertEquals(
                ServerUrl.identity("http://Example.com:8891/"),
                ServerUrl.identity("http://example.com:8891/#pair=abc"));
        assertEquals(
                ServerUrl.identity("http://example.com:8891/ark/"),
                ServerUrl.identity("http://example.com:8891/ark?token=1"));
        assertTrue(ServerUrl.sameServer(
                "http://example.com:8891",
                "HTTP://example.com:8891/#pair=one-time"));
        assertEquals(
                "http://example.com:8891/#pair=abc",
                ServerUrl.normalize("http://example.com:8891/#pair=abc"));
        assertEquals(
                "http://example.com:8891/",
                ServerUrl.withoutFragment("http://example.com:8891/#pair=abc"));
    }

    @Test
    public void extractsUrlFromQrPayloads() {
        assertEquals(
                "http://192.168.1.20:8891/#pair=tok",
                ServerUrl.extractFromQr("  http://192.168.1.20:8891/#pair=tok  "));
        assertEquals(
                "http://192.168.1.20:8891/",
                ServerUrl.extractFromQr("{\"url\":\"http://192.168.1.20:8891\"}"));
        assertEquals(
                "http://192.168.1.20:8891/",
                ServerUrl.extractFromQr("192.168.1.20:8891"));
        assertNull(ServerUrl.extractFromQr("hello"));
    }

    @Test
    public void deduplicatesByNormalizedUrlAndRefreshesToken() {
        ServerStore store = new ServerStore();
        ServerStore.AddResult first = store.addOrUpdate("", "http://Example.com:80/");
        assertNotNull(first);
        assertTrue(first.isAdded());
        assertEquals("http://example.com/", first.getEntry().getUrl());
        assertEquals("ARK \u00b7 example.com", first.getEntry().getName());

        ServerStore.AddResult second = store.addOrUpdate(
                "Lab server", "HTTP://example.com/#pair=one-time");
        assertNotNull(second);
        assertFalse(second.isAdded());
        assertEquals(1, store.size());
        assertEquals("Lab server", second.getEntry().getName());
        assertEquals("http://example.com/#pair=one-time", second.getEntry().getUrl());

        ServerStore.AddResult third = store.addOrUpdate("", "http://example.com:80");
        assertFalse(third.isAdded());
        assertEquals("Lab server", third.getEntry().getName());
        assertEquals(1, store.size());
    }

    @Test
    public void touchAndErrorStateSurviveJsonRoundTrip() {
        ServerStore store = new ServerStore();
        store.addOrUpdate("Home", "http://192.168.1.20:8891/");
        ServerEntry touched = store.touch("http://192.168.1.20:8891/", 123456789L);
        assertNotNull(touched);
        assertEquals(123456789L, touched.getLastUsedAt());
        assertEquals(touched.getUrl(), store.getLastSelectedUrl());

        store.markError("http://192.168.1.20:8891/", "Host not found");
        String json = store.toJson();

        ServerStore loaded = ServerStore.fromJson(json);
        assertEquals(1, loaded.size());
        ServerEntry entry = loaded.get("192.168.1.20:8891");
        assertNotNull(entry);
        assertEquals("Home", entry.getName());
        assertEquals(123456789L, entry.getLastUsedAt());
        assertEquals("Host not found", entry.getLastError());
        assertEquals("http://192.168.1.20:8891/", loaded.getLastSelectedUrl());

        ServerEntry reloaded = loaded.get("http://192.168.1.20:8891/");
        assertEquals(entry, reloaded);
        assertTrue(loaded.clearError("http://192.168.1.20:8891/"));
        assertNull(loaded.get("http://192.168.1.20:8891/").getLastError());
        assertFalse(loaded.clearError("http://192.168.1.20:8891/"));
    }

    @Test
    public void removeDeletesOnlyTheChosenServerAndMovesSelection() {
        ServerStore store = new ServerStore();
        store.addOrUpdate("One", "http://one.local:8891/");
        store.touch("http://one.local:8891/", 10L);
        store.addOrUpdate("Two", "http://two.local:8891/");
        store.touch("http://two.local:8891/", 20L);

        assertEquals(2, store.size());
        assertTrue(store.remove("http://one.local:8891/#stale"));
        assertEquals(1, store.size());
        assertNotNull(store.get("http://two.local:8891/"));
        assertEquals("http://two.local:8891/", store.getLastSelectedUrl());

        assertTrue(store.remove("http://two.local:8891/"));
        assertEquals(0, store.size());
        assertEquals("", store.getLastSelectedUrl());
        assertFalse(store.remove("http://two.local:8891/"));
    }

    @Test
    public void malformedOrPartialJsonProducesSafeEmptyStore() {
        assertEquals(0, ServerStore.fromJson("not json").size());
        assertEquals(0, ServerStore.fromJson("{\"servers\":[{\"url\":\"ftp://nope\"}]}").size());
        assertEquals(0, ServerStore.fromJson("{\"servers\":").size());

        ServerStore loaded = ServerStore.fromJson(
                "{\"servers\":[{\"name\":\"A\",\"url\":\"http://a.local\",\"lastUsedAt\":5},"
                        + "{\"name\":\"B\",\"url\":\"http://b.local\",\"lastUsedAt\":6}],"
                        + "\"lastSelectedUrl\":\"http://b.local\"}");
        assertEquals(2, loaded.size());
        assertEquals("http://b.local/", loaded.getLastSelectedUrl());
    }

    @Test
    public void fragmentSelectionResolvesAcrossJsonReload() {
        ServerStore store = new ServerStore();
        store.addOrUpdate("Phone", "http://example.com/#pair=one-time");
        store.select("http://example.com/#pair=one-time");
        assertEquals("http://example.com/#pair=one-time", store.getLastSelectedUrl());

        ServerStore loaded = ServerStore.fromJson(store.toJson());
        assertEquals("http://example.com/#pair=one-time", loaded.getLastSelectedUrl());
        assertTrue(loaded.isSelected(loaded.get("http://example.com/")));
    }

    @Test
    public void jsonEscapingHandlesQuotesAndNewlines() {
        ServerStore store = new ServerStore();
        store.addOrUpdate("A \"quoted\"\nname", "http://example.com/");
        ServerStore loaded = ServerStore.fromJson(store.toJson());
        assertEquals("A \"quoted\"\nname", loaded.entries().get(0).getName());
    }
}
