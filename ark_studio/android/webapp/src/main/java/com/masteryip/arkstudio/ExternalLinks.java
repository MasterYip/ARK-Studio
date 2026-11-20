package com.masteryip.arkstudio;

import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.graphics.Bitmap;
import android.net.Uri;
import android.os.Bundle;
import android.os.Message;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Toast;

import com.masteryip.arkstudio.data.LinkPolicy;

/** Carries out a {@link LinkPolicy} decision for links that leave the ARK WebView. */
final class ExternalLinks {
    /** Custom Tabs without the androidx.browser library: an (empty) session extra asks for a tab. */
    private static final String EXTRA_CUSTOM_TABS_SESSION = "android.support.customtabs.extra.SESSION";

    interface UrlSink {
        void accept(String url);
    }

    private ExternalLinks() {
    }

    /** Runs the decision; true when the WebView must not load the URL itself. */
    static boolean handle(Activity a, String url, LinkPolicy.Action action) {
        switch (action) {
            case STAY:
                return false;
            case BROWSER:
                browser(a, url);
                return true;
            case MAIL:
                mail(a, url);
                return true;
            case FILE:
                filePath(a, url);
                return true;
            case BLOCK:
            default:
                return true;
        }
    }

    /** http(s) in the browser: a Custom Tab when the default browser offers one. */
    static void browser(Activity a, String url) {
        String scheme = LinkPolicy.scheme(url);
        if (!"http".equals(scheme) && !"https".equals(scheme)) return;
        Intent intent = new Intent(Intent.ACTION_VIEW, Uri.parse(url.trim()));
        intent.addCategory(Intent.CATEGORY_BROWSABLE);
        Bundle extras = new Bundle();
        extras.putBinder(EXTRA_CUSTOM_TABS_SESSION, null);
        intent.putExtras(extras);
        start(a, intent);
    }

    static void mail(Activity a, String url) {
        if (!"mailto".equals(LinkPolicy.scheme(url))) return;
        start(a, new Intent(Intent.ACTION_SENDTO, Uri.parse(url.trim())));
    }

    /** file: links name a file on the ARK server: copy the path. */
    static void filePath(Activity a, String url) {
        String path = LinkPolicy.filePath(url);
        ClipboardManager clipboard = (ClipboardManager) a.getSystemService(Context.CLIPBOARD_SERVICE);
        if (clipboard != null) clipboard.setPrimaryClip(ClipData.newPlainText("path", path));
        Toast.makeText(a, a.getString(R.string.file_link_copied, path), Toast.LENGTH_LONG).show();
    }

    private static void start(Activity a, Intent intent) {
        try {
            a.startActivity(intent);
        } catch (Exception failed) {
            Toast.makeText(a, R.string.link_failed, Toast.LENGTH_SHORT).show();
        }
    }

    /**
     * WebChromeClient.onCreateWindow: target=_blank links and window.open()
     * get a throwaway WebView that only reports the first URL it is sent to.
     */
    static boolean catchPopup(Activity a, Message resultMsg, UrlSink sink) {
        if (resultMsg == null || !(resultMsg.obj instanceof WebView.WebViewTransport)) return false;
        WebView popup = new WebView(a);
        popup.setWebViewClient(new WebViewClient() {
            private boolean done;

            private void take(WebView view, String url) {
                if (done || url == null || url.isEmpty() || "about:blank".equals(url)) return;
                done = true;
                view.stopLoading();
                view.post(view::destroy);
                sink.accept(url);
            }

            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                take(view, request.getUrl() == null ? null : request.getUrl().toString());
                return true;
            }

            @Override
            public void onPageStarted(WebView view, String url, Bitmap favicon) {
                take(view, url);
            }
        });
        ((WebView.WebViewTransport) resultMsg.obj).setWebView(popup);
        resultMsg.sendToTarget();
        return true;
    }
}
