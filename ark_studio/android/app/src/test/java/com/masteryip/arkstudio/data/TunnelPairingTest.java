package com.masteryip.arkstudio.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.json.JSONObject;
import org.junit.Test;

import java.nio.charset.StandardCharsets;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.Signature;
import java.security.spec.ECGenParameterSpec;
import java.util.Base64;

/** Cloudflare Tunnel pairing: re-link links, short codes, server identity, saved data, re-link moves. */
public class TunnelPairingTest {

    // ------------------------------------------------------------ links

    @Test
    public void relinkQrCarriesNewAddressAndServerId() {
        PairingLink link = PairingLink.parse(
                "https://new-words-here-now.trycloudflare.com/#relink=0123456789ABCDEFabcd");
        assertNotNull(link);
        assertEquals("https://new-words-here-now.trycloudflare.com/", link.getBaseUrl());
        assertTrue(link.isRelink());
        assertEquals("0123456789abcdefabcd", link.getRelinkId());
        assertFalse(link.hasCode());

        PairingLink pair = PairingLink.parse("https://a-b-c-d.trycloudflare.com/#pair=arkp_x1");
        assertFalse(pair.isRelink());
        assertEquals("arkp_x1", pair.getCode());

        // Not 20 hex digits: no re-link id.
        assertFalse(PairingLink.parse("https://x.example/#relink=abc").isRelink());
        assertFalse(PairingLink.parse("https://x.example/#relink=0123456789abcdefabcd0").isRelink());
    }

    @Test
    public void shortCodeBecomesQuickTunnelUrl() {
        assertEquals("https://able-baker-charlie-delta.trycloudflare.com/",
                PairingLink.shortCodeUrl(" Able-Baker-Charlie-Delta "));
        assertNull(PairingLink.shortCodeUrl("hello"));
        assertNull(PairingLink.shortCodeUrl("bad code-x"));
        assertNull(PairingLink.shortCodeUrl("https://a-b.trycloudflare.com/"));

        assertEquals("https://able-baker-charlie-delta.trycloudflare.com/",
                PairingLink.parseRelinkInput("able-baker-charlie-delta").getBaseUrl());
        // A typed host without a scheme: quick tunnels are https only.
        assertEquals("https://able-baker-charlie-delta.trycloudflare.com/",
                PairingLink.parseRelinkInput("able-baker-charlie-delta.trycloudflare.com").getBaseUrl());
        PairingLink pasted = PairingLink.parseRelinkInput(
                "https://w-x-y-z.trycloudflare.com/#relink=0123456789abcdefabcd");
        assertEquals("0123456789abcdefabcd", pasted.getRelinkId());
        // A pairing URL pasted into re-link never carries its code along.
        assertFalse(PairingLink.parseRelinkInput("https://w-x-y-z.trycloudflare.com/#pair=arkp_1").hasCode());
        assertEquals("http://192.168.1.5:8890/", PairingLink.parseRelinkInput("192.168.1.5:8890").getBaseUrl());
        assertNull(PairingLink.parseRelinkInput("hello"));
        // Typed with a Chinese keyboard: full-width punctuation.
        assertEquals("http://192.168.1.5:8890/", PairingLink.parseRelinkInput("192。168。1。5：8890").getBaseUrl());
        assertEquals("https://able-baker-charlie-delta.trycloudflare.com/",
                PairingLink.parseRelinkInput("ａｂｌｅ－baker－charlie－delta").getBaseUrl());

        assertTrue(PairingLink.isQuickTunnel("https://a-b-c-d.trycloudflare.com/"));
        assertFalse(PairingLink.isQuickTunnel("https://trycloudflare.com/"));
        assertFalse(PairingLink.isQuickTunnel("https://eviltrycloudflare.com/"));
        assertFalse(PairingLink.isQuickTunnel("http://192.168.1.5/"));
    }

    // ---------------------------------------------------------- identity

    static final class Server {
        final KeyPair keys;
        final String publicKey;
        final String id;

        Server() throws Exception {
            KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(new ECGenParameterSpec("secp256r1"));
            keys = gen.generateKeyPair();
            byte[] der = keys.getPublic().getEncoded();
            publicKey = Base64.getEncoder().encodeToString(der);
            id = ServerIdentity.serverIdFor(der);
        }

        String sign(String host, String nonce) throws Exception {
            Signature s = Signature.getInstance("SHA256withECDSA");
            s.initSign(keys.getPrivate());
            s.update(("ark-server-identity/1\n" + id + "\n" + host + "\n" + nonce).getBytes(StandardCharsets.UTF_8));
            return Base64.getEncoder().encodeToString(s.sign());
        }

        JSONObject answer(String host, String nonce) throws Exception {
            return new JSONObject().put("ok", true).put("server_id", id).put("public_key", publicKey)
                    .put("algorithm", "ECDSA-P256-SHA256").put("context", "ark-server-identity/1")
                    .put("host", host).put("nonce", nonce).put("signature", sign(host, nonce)).put("tunnel", true);
        }
    }

    @Test
    public void serverIdIsShaPrefixOfKey() throws Exception {
        Server s = new Server();
        assertEquals(20, s.id.length());
        assertTrue(ServerIdentity.validServerId(s.id));
        String nonce = ServerIdentity.newNonce();
        assertTrue(ServerIdentity.validNonce(nonce));
        assertTrue(ServerIdentity.verify(s.publicKey, s.id, "a-b.trycloudflare.com", nonce,
                s.sign("a-b.trycloudflare.com", nonce)));
    }

    @Test
    public void identityCheckFirstContactAndStoredKey() throws Exception {
        Server s = new Server();
        String base = "https://able-baker-charlie-delta.trycloudflare.com/";
        String host = "able-baker-charlie-delta.trycloudflare.com";
        String nonce = ServerIdentity.newNonce();

        ServerIdentity.Check first = ServerIdentity.check(base, nonce, s.answer(host, nonce), null, null);
        assertTrue(first.ok());
        assertTrue(first.tunnel);
        assertEquals(s.id, first.identity.serverId);
        assertEquals(s.publicKey, first.identity.publicKey);

        ServerIdentity.Check later = ServerIdentity.check(base, nonce, s.answer(host, nonce), s.id, s.publicKey);
        assertTrue(later.ok());
    }

    @Test
    public void identityCheckRefusesWrongHostNonceKeyAndSignature() throws Exception {
        Server s = new Server();
        Server other = new Server();
        String base = "https://new-a-b-c.trycloudflare.com/";
        String host = "new-a-b-c.trycloudflare.com";
        String nonce = ServerIdentity.newNonce();

        // Signed for another host (a replayed answer from a look-alike address).
        JSONObject wrongHost = s.answer("old-a-b-c.trycloudflare.com", nonce);
        assertEquals(ServerIdentity.Problem.WRONG_HOST,
                ServerIdentity.check(base, nonce, wrongHost, s.id, s.publicKey).problem);
        // Says the right host but the signature covers another one.
        JSONObject forged = s.answer(host, nonce).put("signature", s.sign("old-a-b-c.trycloudflare.com", nonce));
        assertEquals(ServerIdentity.Problem.BAD_SIGNATURE,
                ServerIdentity.check(base, nonce, forged, s.id, s.publicKey).problem);
        // An old nonce.
        String stale = ServerIdentity.newNonce();
        assertEquals(ServerIdentity.Problem.WRONG_NONCE,
                ServerIdentity.check(base, nonce, s.answer(host, stale), s.id, s.publicKey).problem);
        JSONObject staleSigned = s.answer(host, nonce).put("signature", s.sign(host, stale));
        assertEquals(ServerIdentity.Problem.BAD_SIGNATURE,
                ServerIdentity.check(base, nonce, staleSigned, s.id, s.publicKey).problem);
        // Another server with its own valid key.
        assertEquals(ServerIdentity.Problem.WRONG_SERVER,
                ServerIdentity.check(base, nonce, other.answer(host, nonce), s.id, s.publicKey).problem);
        // Claims the stored id but brings another key.
        JSONObject swapped = other.answer(host, nonce).put("server_id", s.id);
        assertEquals(ServerIdentity.Problem.WRONG_KEY,
                ServerIdentity.check(base, nonce, swapped, s.id, s.publicKey).problem);
        // Claims the stored id and key, signed with another private key.
        JSONObject stolenKey = s.answer(host, nonce).put("signature", other.sign(host, nonce));
        assertEquals(ServerIdentity.Problem.BAD_SIGNATURE,
                ServerIdentity.check(base, nonce, stolenKey, s.id, s.publicKey).problem);
        // First contact: an id that is not the key's hash.
        JSONObject mismatched = other.answer(host, nonce).put("server_id", s.id).put("signature", "");
        assertEquals(ServerIdentity.Problem.MALFORMED,
                ServerIdentity.check(base, nonce, mismatched, null, null).problem);
        JSONObject idNotKey = other.answer(host, nonce).put("server_id", s.id);
        assertEquals(ServerIdentity.Problem.BAD_SIGNATURE,
                ServerIdentity.check(base, nonce, idNotKey, null, null).problem);
        assertFalse(ServerIdentity.verify("not-base64!", s.id, host, nonce, s.sign(host, nonce)));
    }

    // ------------------------------------------------------- saved data

    @Test
    public void storeRoundTripsIdentityAndReadsOldJson() throws Exception {
        Server s = new Server();
        ServerStore store = new ServerStore();
        store.addOrUpdate("Home", "https://able-baker-charlie-delta.trycloudflare.com/");
        store.addOrUpdate("LAN", "http://192.168.1.5:8890/");
        store.setIdentity("https://able-baker-charlie-delta.trycloudflare.com/", s.id, s.publicKey, true);

        ServerStore back = ServerStore.fromJson(store.toJson());
        ServerEntry tunnel = back.get("https://able-baker-charlie-delta.trycloudflare.com/");
        assertEquals(s.id, tunnel.getServerId());
        assertEquals(s.publicKey, tunnel.getPublicKey());
        assertTrue(tunnel.isTunnel());
        assertTrue(tunnel.canRelink());
        assertEquals(tunnel, back.findByServerId(s.id));
        ServerEntry lan = back.get("http://192.168.1.5:8890/");
        assertFalse(lan.hasIdentity());
        assertFalse(lan.isTunnel());

        String old = "{\"version\":1,\"lastSelectedUrl\":\"http://192.168.1.5:8890/\",\"servers\":["
                + "{\"name\":\"LAN\",\"url\":\"http://192.168.1.5:8890/\",\"lastUsedAt\":5,\"lastError\":\"\"}]}";
        ServerStore legacy = ServerStore.fromJson(old);
        assertEquals(1, legacy.size());
        assertNull(legacy.get("http://192.168.1.5:8890/").getServerId());
        assertEquals("http://192.168.1.5:8890/", legacy.getLastSelectedUrl());
        // Garbage identity fields are dropped, the entry stays.
        String odd = "{\"servers\":[{\"name\":\"X\",\"url\":\"http://10.0.0.2/\",\"serverId\":\"zz\","
                + "\"publicKey\":\"k\",\"tunnel\":\"yes\"}]}";
        ServerEntry x = ServerStore.fromJson(odd).get("http://10.0.0.2/");
        assertNotNull(x);
        assertFalse(x.hasIdentity());
        assertFalse(x.isTunnel());
        // Pairing again keeps the stored identity.
        store.addOrUpdate("", "https://able-baker-charlie-delta.trycloudflare.com/");
        assertEquals(s.id, store.get("https://able-baker-charlie-delta.trycloudflare.com/").getServerId());
    }

    // --------------------------------------------------------- re-link

    @Test
    public void relinkMovesEntryAndSession() throws Exception {
        Server s = new Server();
        String oldUrl = "https://old-a-b-c.trycloudflare.com/";
        String newUrl = "https://new-d-e-f.trycloudflare.com/";
        ServerStore store = new ServerStore();
        store.addOrUpdate("Studio", oldUrl);
        store.addOrUpdate("LAN", "http://192.168.1.5:8890/");
        store.setIdentity(oldUrl, s.id, s.publicKey, true);
        store.touch(oldUrl, 1000L);
        store.markError(oldUrl, "Server unreachable");

        SessionTest.MapStore kv = new SessionTest.MapStore();
        SessionVault vault = new SessionVault(kv, new SessionTest.FakeCipher());
        vault.save(new AccountSession(oldUrl, "arkm_tok", "ark", "full", "p1", "Demo", "qr", 7L));

        ServerEntry moved = store.relink(oldUrl, newUrl + "#relink=" + s.id);
        AccountSession session = vault.move(oldUrl, moved.getUrl());

        assertEquals(newUrl, moved.getUrl());
        assertEquals("Studio", moved.getName());
        assertEquals(s.id, moved.getServerId());
        assertEquals(s.publicKey, moved.getPublicKey());
        assertNull(moved.getLastError());
        assertEquals(1000L, moved.getLastUsedAt());
        assertNull(store.get(oldUrl));
        assertEquals(2, store.size());
        assertEquals(newUrl, store.getLastSelectedUrl());
        assertEquals(moved, store.findByServerId(s.id));

        assertEquals(newUrl, session.serverUrl);
        assertEquals("arkm_tok", session.token);
        assertEquals("Demo", session.projectName);
        assertNull(vault.get(oldUrl));
        assertEquals("arkm_tok", vault.get(newUrl).token);
        for (Object v : kv.map.values()) assertFalse(String.valueOf(v).contains("arkm_tok"));

        // Survives a save/load.
        ServerEntry reloaded = ServerStore.fromJson(store.toJson()).get(newUrl);
        assertEquals(s.id, reloaded.getServerId());
        assertTrue(reloaded.isTunnel());
    }

    @Test
    public void relinkReplacesAnEntryAlreadyAtTheNewAddress() throws Exception {
        Server s = new Server();
        String oldUrl = "https://old-a-b-c.trycloudflare.com/";
        String newUrl = "https://new-d-e-f.trycloudflare.com/";
        ServerStore store = new ServerStore();
        store.addOrUpdate("Studio", oldUrl);
        store.setIdentity(oldUrl, s.id, s.publicKey, true);
        store.addOrUpdate("", newUrl);
        store.select(newUrl);

        ServerEntry moved = store.relink(oldUrl, newUrl);
        assertEquals(1, store.size());
        assertEquals("Studio", moved.getName());
        assertEquals(newUrl, store.getLastSelectedUrl());
        assertNull(store.relink("https://unknown-x-y.trycloudflare.com/", newUrl));

        SessionVault vault = new SessionVault(new SessionTest.MapStore(), new SessionTest.FakeCipher());
        assertNull(vault.move(oldUrl, newUrl));
    }

    @Test
    public void unknownServerIdFindsNothing() {
        ServerStore store = new ServerStore();
        store.addOrUpdate("LAN", "http://192.168.1.5:8890/");
        assertNull(store.findByServerId("0123456789abcdefabcd"));
        assertNull(store.findByServerId(null));
    }
}
