package com.masteryip.arkstudio.model;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.SessionStatus;

import org.json.JSONObject;
import org.junit.Test;

/** The WebView's chrome: bar colours from the site's theme, back to the server list, session refresh. */
public class WebChromeTest {
    private static final int DARK = 0xFF0F141C;
    private static final int LIGHT = 0xFFF5F6F8;

    @Test
    public void themeFromTheSite() {
        WebTheme dark = WebTheme.of("dark", "#0f141c", DARK, LIGHT);
        assertNotNull(dark);
        assertTrue(dark.dark);
        assertEquals(0xFF0F141C, dark.color);

        WebTheme light = WebTheme.of("light", " #fff ", DARK, LIGHT);
        assertFalse(light.dark);
        assertEquals(0xFFFFFFFF, light.color);

        // A custom background without a theme name: decided by its brightness.
        assertTrue(WebTheme.of("", "rgb(20, 24, 30)", DARK, LIGHT).dark);
        assertFalse(WebTheme.of(null, "rgba(250,250,250,1)", DARK, LIGHT).dark);
        // Theme name without a usable colour: the fallback of that theme.
        assertEquals(DARK, WebTheme.of("dark", "var(--x)", DARK, LIGHT).color);
        assertEquals(LIGHT, WebTheme.of("LIGHT", "", DARK, LIGHT).color);
        // Nothing usable: leave the bars alone.
        assertNull(WebTheme.of("auto", "transparent", DARK, LIGHT));
        assertNull(WebTheme.of("", "rgba(0, 0, 0, 0)", DARK, LIGHT));
    }

    @Test
    public void cssColours() {
        assertEquals(Integer.valueOf(0xFF112233), WebTheme.parse("#123"));
        assertEquals(Integer.valueOf(0xFFAABBCC), WebTheme.parse("#aabbccdd"));
        assertEquals(Integer.valueOf(0xFF010203), WebTheme.parse("rgb(1 2 3)"));
        assertNull(WebTheme.parse("rgb(300, 0, 0)"));
        assertNull(WebTheme.parse("#12"));
        assertNull(WebTheme.parse(null));
    }

    @Test
    public void backTwiceLeavesWithinTheWindow() {
        BackGate gate = new BackGate();
        assertFalse(gate.press(1000));
        assertTrue(gate.press(1000 + BackGate.WINDOW_MS));
        // Too slow: the second press only arms again.
        assertFalse(gate.press(10_000));
        assertFalse(gate.press(10_000 + BackGate.WINDOW_MS + 1));
        assertTrue(gate.press(10_000 + BackGate.WINDOW_MS + 2));
        // A sheet closed in between starts over.
        assertFalse(gate.press(20_000));
        gate.reset();
        assertFalse(gate.press(20_100));
    }

    @Test
    public void refreshReadsTheServersAnswer() throws Exception {
        AccountSession saved = new AccountSession("https://ark.example/", "arkm_x", "ana", "full",
                "p1", "One", "qr", 1L);
        JSONObject status = new JSONObject("{\"authenticated\":true,\"user\":\"ana\",\"permission\":\"read_only\","
                + "\"project\":{\"id\":\"p2\",\"name\":\"Two\"},\"scope\":\"user\"}");
        assertTrue(SessionStatus.signedIn(status));
        AccountSession updated = SessionStatus.apply(saved, status);
        assertEquals("p2", updated.projectId);
        assertEquals("Two", updated.projectName);
        assertTrue(updated.isReadOnly());
        assertEquals("arkm_x", updated.token);

        JSONObject out = new JSONObject("{\"authenticated\":false,\"user\":null,\"permission\":null,\"project\":null}");
        assertFalse(SessionStatus.signedIn(out));
        assertEquals(saved, SessionStatus.apply(saved, out));
        assertFalse(SessionStatus.signedIn(null));
    }
}
