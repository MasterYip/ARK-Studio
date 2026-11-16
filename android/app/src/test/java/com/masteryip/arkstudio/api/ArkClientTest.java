package com.masteryip.arkstudio.api;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;

/** Drives the client against an in-process HTTP server that mimics ARK. */
public class ArkClientTest {
    private MiniHttpServer server;
    private String base;
    private final List<String> seen = new ArrayList<>();

    @Before
    public void start() throws IOException {
        server = new MiniHttpServer(this::handle);
        base = "http://127.0.0.1:" + server.port() + "/";
    }

    @After
    public void stop() {
        server.stop();
    }

    private static MiniHttpServer.Response json(int status, String text) {
        return new MiniHttpServer.Response(status, text);
    }

    private MiniHttpServer.Response handle(MiniHttpServer.Request exchange) throws Exception {
        String path = exchange.path();
        String token = exchange.header(ArkClient.TOKEN_HEADER);
        String origin = exchange.header("Origin");
        String body = exchange.body;
        seen.add(exchange.method + " " + exchange.target + " token=" + token
                + " origin=" + origin + " ctype=" + exchange.header("Content-Type")
                + " body=" + body);
        if (path.equals("/api/mobile/login")) {
            JSONObject in = new JSONObject(body);
            if (in.getString("password").equals("right")) {
                return json(200, "{\"ok\":true,\"mobile_token\":\"arkm_x\",\"user\":\"ark\","
                        + "\"permission\":\"" + in.getString("permission") + "\",\"source\":\"password\","
                        + "\"project\":\"Demo\",\"project_id\":\"p1\"}");
            } else if (in.getString("password").equals("spam")) {
                return json(429, "{\"ok\":false,\"error\":{\"code\":\"AUTH_RATE_LIMITED\","
                        + "\"message\":\"Too many\"}}").header("Retry-After", "42");
            }
            return json(401, "{\"ok\":false,\"error\":{\"code\":\"AUTH_INVALID\","
                    + "\"message\":\"Invalid username or password.\"}}");
        }
        if (path.equals("/api/units/u1") && exchange.method.equals("PATCH")) {
            return json(200, "{\"ok\":true,\"unit\":" + body + "}");
        }
        if (path.equals("/api/revoked")) {
            return json(401, "{\"ok\":false,\"error\":{\"code\":\"MOBILE_TOKEN_REVOKED\","
                    + "\"message\":\"This mobile session was revoked.\"}}");
        }
        if (path.equals("/api/redirect")) {
            return json(302, "").header("Location", "/login");
        }
        if (path.equals("/api/html")) {
            return json(500, "<html>boom</html>");
        }
        return json(200, "{\"ok\":true,\"path\":\"" + path + "\"}");
    }

    @Test
    public void loginSendsJsonWithoutOriginAndReturnsToken() throws Exception {
        JSONObject result = ArkClient.login(base + "#pair=ignored", " ark ", "right", true, "Pixel");
        assertEquals("arkm_x", result.getString("mobile_token"));
        assertEquals("read_only", result.getString("permission"));
        String request = seen.get(0);
        assertTrue(request, request.startsWith("POST /api/mobile/login token=null origin=null"));
        assertTrue(request, request.contains("application/json"));
        assertTrue(request, request.contains("\"username\":\"ark\""));
        assertTrue(request, request.contains("\"device\":\"Pixel\""));
    }

    @Test
    public void wrongPasswordAndRateLimitMapToCodes() {
        try {
            ArkClient.login(base, "ark", "wrong", false, null);
            fail();
        } catch (ApiException expected) {
            assertEquals(401, expected.getStatus());
            assertEquals("AUTH_INVALID", expected.getCode());
            assertEquals("Invalid username or password.", expected.getMessage());
            assertFalse(expected.isNetwork());
        }
        try {
            ArkClient.login(base, "ark", "spam", false, null);
            fail();
        } catch (ApiException expected) {
            assertEquals(429, expected.getStatus());
            assertEquals(42, expected.getRetryAfter());
        }
    }

    @Test
    public void tokenGoesInHeaderNeverInUrl() throws Exception {
        ArkClient client = new ArkClient(base, "arkm_secret");
        client.get("/api/bootstrap");
        assertTrue(seen.get(0).startsWith("GET /api/bootstrap token=arkm_secret"));
        assertFalse(seen.get(0).contains("mobile_token="));
    }

    @Test
    public void patchWorksAndCarriesBody() throws Exception {
        ArkClient client = new ArkClient(base, "arkm_t");
        JSONObject body = new JSONObject().put("status", "done");
        JSONObject result = client.patch("api/units/u1", body);
        assertEquals("done", result.getJSONObject("unit").getString("status"));
        assertTrue(seen.get(0).startsWith("PATCH /api/units/u1"));
    }

    @Test
    public void revokedTokenIsSignedOut() {
        try {
            new ArkClient(base, "arkm_t").get("/api/revoked");
            fail();
        } catch (ApiException expected) {
            assertTrue(expected.isSignedOut());
        }
        try {
            new ArkClient(base, "arkm_t").get("/api/redirect");
            fail();
        } catch (ApiException expected) {
            assertTrue(expected.isSignedOut());
        }
    }

    @Test
    public void nonJsonErrorAndNetworkFailure() {
        try {
            new ArkClient(base, "").get("/api/html");
            fail();
        } catch (ApiException expected) {
            assertEquals(500, expected.getStatus());
            assertEquals("HTTP_500", expected.getCode());
        }
        server.stop();
        try {
            new ArkClient(base, "").withTimeouts(500, 500).get("/api/health");
            fail();
        } catch (ApiException expected) {
            assertTrue(expected.isNetwork());
        }
    }

    @Test
    public void baseNormalization() {
        assertEquals("http://h:1/ark/", ArkClient.normalizeBase("http://h:1/ark//?x=1#pair=abc"));
        assertEquals("http://h:1/api/x", new ArkClient("http://h:1", null).url("/api/x"));
        assertEquals("a%20b%2Fc", ArkClient.enc("a b/c"));
        assertNull(null);
    }
}
