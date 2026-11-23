package com.masteryip.arkstudio.api;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;

/** The confirmed claim through a Cloudflare Tunnel, the identity call and Cloudflare error pages. */
public class TunnelClientTest {
    private MiniHttpServer server;
    private String base;
    private final List<String> seen = new ArrayList<>();
    private int polls;
    private String outcome = "approve";

    @Before
    public void start() throws IOException {
        server = new MiniHttpServer(this::handle);
        base = "http://127.0.0.1:" + server.port() + "/";
    }

    @After
    public void stop() {
        server.stop();
    }

    private MiniHttpServer.Response handle(MiniHttpServer.Request r) throws Exception {
        seen.add(r.method + " " + r.target + " token=" + r.header(ArkClient.TOKEN_HEADER) + " body=" + r.body);
        switch (r.path()) {
            case "/api/mobile/pair/claim": {
                JSONObject in = new JSONObject(r.body);
                if (!"arkp_ok".equals(in.optString("token"))) {
                    return new MiniHttpServer.Response(401,
                            "{\"ok\":false,\"error\":{\"code\":\"PAIRING_INVALID\",\"message\":\"x\"}}");
                }
                return new MiniHttpServer.Response(202, "{\"ok\":true,\"pending\":true,\"claim_id\":\"claim_1\","
                        + "\"claim_secret\":\"s3cret\",\"confirm_code\":\"042917\",\"expires_in\":180,"
                        + "\"poll_after\":2,\"status_path\":\"api/mobile/pair/claim/status\"}");
            }
            case "/api/mobile/pair/claim/status": {
                JSONObject in = new JSONObject(r.body);
                if (!"s3cret".equals(in.optString("claim_secret"))) {
                    return new MiniHttpServer.Response(401,
                            "{\"ok\":false,\"error\":{\"code\":\"PAIRING_CONFIRM_EXPIRED\",\"message\":\"x\"}}");
                }
                if (++polls < 2) {
                    return new MiniHttpServer.Response(202, "{\"ok\":true,\"pending\":true,\"claim_id\":\"claim_1\","
                            + "\"confirm_code\":\"042917\",\"expires_in\":170,\"poll_after\":2}");
                }
                if ("reject".equals(outcome)) {
                    return new MiniHttpServer.Response(403,
                            "{\"ok\":false,\"error\":{\"code\":\"PAIRING_REJECTED\",\"message\":\"declined\"}}");
                }
                return new MiniHttpServer.Response(200, "{\"ok\":true,\"mobile_token\":\"arkm_t\",\"user\":\"ark\","
                        + "\"permission\":\"full\",\"project\":\"Demo\"}");
            }
            case "/gone":
                return new MiniHttpServer.Response(530, "<html><title>Cloudflare Tunnel error</title>"
                        + "Error 1033</html>").header("Server", "cloudflare");
            case "/bad-gateway":
                return new MiniHttpServer.Response(502, "<html>Bad gateway</html>").header("Server", "cloudflare");
            case "/plain-502":
                return new MiniHttpServer.Response(502, "<html>Bad gateway</html>");
            default:
                return new MiniHttpServer.Response(404, "{\"ok\":false}");
        }
    }

    @Test
    public void pendingClaimThenConfirmedToken() throws Exception {
        JSONObject first = ArkClient.claim(base, "arkp_ok", "Pixel 7");
        assertTrue(ArkClient.isPending(first));
        assertEquals("042917", first.getString("confirm_code"));
        assertTrue(seen.get(0).contains("\"device\":\"Pixel 7\""));
        assertTrue(seen.get(0).contains("token=null"));

        JSONObject waiting = ArkClient.claimStatus(base, "arkp_ok", "claim_1", "s3cret");
        assertTrue(ArkClient.isPending(waiting));
        JSONObject done = ArkClient.claimStatus(base, "arkp_ok", "claim_1", "s3cret");
        assertFalse(ArkClient.isPending(done));
        assertEquals("arkm_t", done.getString("mobile_token"));
        String poll = seen.get(seen.size() - 1);
        assertTrue(poll.contains("\"claim_id\":\"claim_1\""));
        assertTrue(poll.contains("\"code\":\"arkp_ok\""));
    }

    @Test
    public void lanServersStillAnswerWithTheTokenDirectly() throws Exception {
        assertFalse(ArkClient.isPending(new JSONObject("{\"ok\":true,\"mobile_token\":\"arkm_x\"}")));
        assertFalse(ArkClient.isPending(null));
    }

    @Test
    public void rejectedAndExpiredClaims() throws Exception {
        outcome = "reject";
        ArkClient.claimStatus(base, "arkp_ok", "claim_1", "s3cret");
        try {
            ArkClient.claimStatus(base, "arkp_ok", "claim_1", "s3cret");
            fail("expected PAIRING_REJECTED");
        } catch (ApiException e) {
            assertEquals(403, e.getStatus());
            assertEquals("PAIRING_REJECTED", e.getCode());
            assertFalse(e.isSignedOut());
        }
        try {
            ArkClient.claimStatus(base, "arkp_ok", "claim_1", "wrong");
            fail("expected PAIRING_CONFIRM_EXPIRED");
        } catch (ApiException e) {
            assertEquals("PAIRING_CONFIRM_EXPIRED", e.getCode());
        }
    }

    @Test
    public void identitySendsNonceWithoutToken() throws Exception {
        try {
            ArkClient.identity(base, "abcdefghijklmnop_-12");
        } catch (ApiException expected404) {
            assertEquals(404, expected404.getStatus());
        }
        assertTrue(seen.get(0).startsWith("GET /api/mobile/identity?nonce=abcdefghijklmnop_-12 token=null"));
    }

    @Test
    public void cloudflareErrorPagesAreUnreachable() {
        try {
            new ArkClient(base, "arkm_t").get("/gone");
            fail();
        } catch (ApiException e) {
            assertEquals(ApiException.TUNNEL_DOWN, e.getCode());
            assertTrue(e.isUnreachable());
            assertFalse(e.isNetwork());
            assertFalse(e.isSignedOut());
        }
        try {
            new ArkClient(base, "arkm_t").get("/bad-gateway");
            fail();
        } catch (ApiException e) {
            assertEquals(ApiException.TUNNEL_DOWN, e.getCode());
        }
        try {
            new ArkClient(base, "arkm_t").get("/plain-502");
            fail();
        } catch (ApiException e) {
            assertEquals("HTTP_502", e.getCode());
            assertFalse(e.isUnreachable());
        }
        assertTrue(ArkClient.cloudflareDown(530, null, null));
        assertTrue(ArkClient.cloudflareDown(404, "cloudflare", "error code: 1033"));
        assertFalse(ArkClient.cloudflareDown(404, "cloudflare", "not found"));
        assertFalse(ArkClient.cloudflareDown(200, "cloudflare", ""));
    }
}
