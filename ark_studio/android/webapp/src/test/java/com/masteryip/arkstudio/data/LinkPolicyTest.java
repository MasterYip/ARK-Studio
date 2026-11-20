package com.masteryip.arkstudio.data;

import static org.junit.Assert.assertEquals;

import com.masteryip.arkstudio.data.LinkPolicy.Action;

import org.junit.Test;

public class LinkPolicyTest {
    private static final String SERVER = "https://ark.example.com/";
    private static final String PAGE = "https://ark.example.com/#/tasks";

    @Test
    public void arkPagesStayInTheWebView() {
        assertEquals(Action.STAY, LinkPolicy.decide("https://ark.example.com/#/join/abc", true, SERVER, PAGE));
        assertEquals(Action.STAY, LinkPolicy.decide("https://ARK.example.com:443/api/media/3/raw", true, SERVER));
        assertEquals(Action.STAY, LinkPolicy.decide("http://192.168.1.20:8891/#/home", true, "http://192.168.1.20:8891"));
        // The page shown counts too (a server saved as http that redirected to https).
        assertEquals(Action.STAY, LinkPolicy.decide("https://ark.example.com/x", true, "http://ark.example.com/", PAGE));
    }

    @Test
    public void otherSitesOpenInTheBrowser() {
        assertEquals(Action.BROWSER, LinkPolicy.decide("https://github.com/MasterYip/ARK-Studio", true, SERVER, PAGE));
        assertEquals(Action.BROWSER, LinkPolicy.decide("http://ark.example.com/", true, SERVER));
        assertEquals(Action.BROWSER, LinkPolicy.decide("https://ark.example.com:8443/", true, SERVER));
        assertEquals(Action.BROWSER, LinkPolicy.decide("https://other-ark.example.org/#/home", true, SERVER));
        assertEquals(Action.BROWSER, LinkPolicy.decide("https://example.org/", true));
        assertEquals(Action.BROWSER, LinkPolicy.decide("https://example.org/", true, (String) null));
    }

    @Test
    public void framesKeepTheirEmbeddedContent() {
        assertEquals(Action.STAY, LinkPolicy.decide("https://www.youtube.com/embed/x", false, SERVER));
        assertEquals(Action.STAY, LinkPolicy.decide("about:blank", false, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("about:blank", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("javascript:alert(1)", false, SERVER));
    }

    @Test
    public void onlyKnownSchemesLeave() {
        assertEquals(Action.MAIL, LinkPolicy.decide("mailto:a@example.com", true, SERVER));
        assertEquals(Action.MAIL, LinkPolicy.decide("MAILTO:a@example.com", true, SERVER));
        assertEquals(Action.FILE, LinkPolicy.decide("file:///E:/tasks/20261120_X", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("javascript:alert(1)", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide(" JavaScript:alert(1)", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("data:text/html,<b>x</b>", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("intent://scan/#Intent;scheme=zxing;package=x.y;end", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("vscode://file/E:/x", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("tel:123", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("", true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide(null, true, SERVER));
        assertEquals(Action.BLOCK, LinkPolicy.decide("https://", true, SERVER));
    }

    @Test
    public void filePathsAreReadable() {
        assertEquals("E:/tasks/20261120_X", LinkPolicy.filePath("file:///E:/tasks/20261120_X"));
        assertEquals("/home/u/a b", LinkPolicy.filePath("file:///home/u/a%20b"));
    }

    @Test
    public void originsIgnoreDefaultPortsAndCase() {
        assertEquals("https://ark.example.com", LinkPolicy.origin("HTTPS://Ark.Example.com:443/x?y#z"));
        assertEquals("http://h:8891", LinkPolicy.origin("http://h:8891/"));
        assertEquals("", LinkPolicy.origin("mailto:a@b"));
        assertEquals("", LinkPolicy.origin("not a url"));
    }
}
