package com.masteryip.arkstudio.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.json.JSONObject;
import org.junit.Test;

import java.util.HashMap;
import java.util.Map;

public class SessionTest {
    static final class MapStore implements SessionVault.KeyValue {
        final Map<String, String> map = new HashMap<>();

        @Override
        public String get(String key) {
            return map.get(key);
        }

        @Override
        public void put(String key, String value) {
            map.put(key, value);
        }

        @Override
        public void remove(String key) {
            map.remove(key);
        }

        @Override
        public Map<String, ?> all() {
            return map;
        }
    }

    /** Reversible fake: proves the vault never stores the raw token. */
    static final class FakeCipher implements SessionVault.Cipher {
        boolean broken;

        @Override
        public String encrypt(String plain) {
            return "sealed:" + new StringBuilder(plain).reverse();
        }

        @Override
        public String decrypt(String sealed) throws Exception {
            if (broken) throw new Exception("key lost");
            return new StringBuilder(sealed.substring(7)).reverse().toString();
        }
    }

    @Test
    public void pairingLinkSplitsBaseAndOneTimeCode() {
        PairingLink link = PairingLink.parse("http://192.168.1.20:8890/#pair=arkp_AbC-123_x");
        assertNotNull(link);
        assertEquals("http://192.168.1.20:8890/", link.getBaseUrl());
        assertEquals("arkp_AbC-123_x", link.getCode());

        PairingLink prefixed = PairingLink.parse("https://ark.example.net/ark/?view=x#/home?pair=arkp_9");
        assertEquals("https://ark.example.net/ark/", prefixed.getBaseUrl());
        assertEquals("arkp_9", prefixed.getCode());

        PairingLink plain = PairingLink.parse("http://10.0.2.2:8899");
        assertEquals("http://10.0.2.2:8899/", plain.getBaseUrl());
        assertFalse(plain.hasCode());

        assertNull(PairingLink.parse("hello world"));
        assertNull(PairingLink.parse(null));
    }

    @Test
    public void cleartextClassification() {
        assertTrue(PairingLink.isPrivateHost("http://192.168.1.4:8890/"));
        assertTrue(PairingLink.isPrivateHost("http://10.0.2.2:8899/"));
        assertTrue(PairingLink.isPrivateHost("http://172.20.0.5/"));
        assertTrue(PairingLink.isPrivateHost("http://100.100.1.1/"));
        assertTrue(PairingLink.isPrivateHost("http://[fd00::1]:8890/"));
        assertTrue(PairingLink.isPrivateHost("http://nas.local:8890/"));
        assertFalse(PairingLink.isPrivateHost("http://172.32.0.1/"));
        assertFalse(PairingLink.isPrivateHost("http://203.0.113.7:8890/"));
        assertFalse(PairingLink.isPrivateHost("http://ark.example.net/"));
        assertTrue(PairingLink.isInsecurePublic("http://203.0.113.7:8890/"));
        assertFalse(PairingLink.isInsecurePublic("https://203.0.113.7/"));
        assertFalse(PairingLink.isInsecurePublic("http://192.168.0.2/"));
    }

    @Test
    public void originComparison() {
        assertEquals("http://h:8890", PairingLink.origin("http://H:8890/x#/map"));
        assertEquals("https://h", PairingLink.origin("https://h:443/"));
        assertTrue(PairingLink.sameOrigin("http://h:8890/", "http://h:8890/api/media/1/raw"));
        assertFalse(PairingLink.sameOrigin("http://h:8890/", "http://h:8891/"));
        assertFalse(PairingLink.sameOrigin("http://h/", "https://h/"));
        assertFalse(PairingLink.sameOrigin("http://h/", "javascript:alert(1)"));
        assertFalse(PairingLink.sameOrigin("http://h/", "http://h.evil.com/"));
    }

    @Test
    public void vaultEncryptsTokenAndRoundTrips() throws Exception {
        MapStore store = new MapStore();
        FakeCipher cipher = new FakeCipher();
        SessionVault vault = new SessionVault(store, cipher);
        AccountSession session = new AccountSession("http://192.168.1.2:8890/", "arkm_secret", "ark",
                "read_only", "p1", "Demo", "password", 5L);
        vault.save(session);
        for (String value : store.map.values()) {
            assertFalse("raw token stored: " + value, value.contains("arkm_secret"));
        }
        // Same server identity (trailing slash, fragment) finds it.
        AccountSession back = vault.get("http://192.168.1.2:8890#/map");
        assertNotNull(back);
        assertEquals("arkm_secret", back.token);
        assertEquals("ark", back.user);
        assertTrue(back.isReadOnly());
        assertTrue(back.canSwitchProjects());
        assertEquals("Demo", back.projectName);
        assertNull(vault.get("http://192.168.1.3:8890/"));

        cipher.broken = true;
        assertNull(vault.get(session.serverUrl));
        assertTrue("unreadable token is forgotten", store.map.isEmpty());
    }

    @Test
    public void removeForgetsOnlyThatServer() throws Exception {
        MapStore store = new MapStore();
        SessionVault vault = new SessionVault(store, new FakeCipher());
        vault.save(new AccountSession("http://a:1/", "arkm_a", "u", "full", "", "", "qr", 1L));
        vault.save(new AccountSession("http://b:1/", "arkm_b", "u", "full", "", "", "qr", 1L));
        vault.remove("http://a:1/");
        assertFalse(vault.has("http://a:1/"));
        assertTrue(vault.has("http://b:1/"));
    }

    @Test
    public void sessionFromLoginAndClaimResponses() throws Exception {
        JSONObject login = new JSONObject("{\"mobile_token\":\"arkm_1\",\"user\":\"ark\","
                + "\"permission\":\"full\",\"project\":\"P\",\"project_id\":\"pid\",\"source\":\"password\"}");
        AccountSession a = AccountSession.fromLogin("http://h/", login, "qr", 9L);
        assertEquals("password", a.source);
        assertEquals("pid", a.projectId);
        assertFalse(a.isReadOnly());
        JSONObject claim = new JSONObject("{\"mobile_token\":\"arkm_2\",\"mobile_user\":\"bo\","
                + "\"permission\":\"read_only\",\"project\":\"Q\"}");
        AccountSession b = AccountSession.fromLogin("http://h/", claim, "qr", 9L);
        assertEquals("qr", b.source);
        assertEquals("bo", b.user);
        assertTrue(b.isReadOnly());
        assertFalse(b.canSwitchProjects());
        assertFalse(new AccountSession("http://h/", "nope", "", "", "", "", "", 0).isValid());
    }
}
