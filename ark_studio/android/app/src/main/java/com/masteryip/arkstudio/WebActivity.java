package com.masteryip.arkstudio;

import android.Manifest;
import android.app.DownloadManager;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Bitmap;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.view.View;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.SslErrorHandler;
import android.webkit.URLUtil;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.ProgressBar;
import android.widget.Toast;

import androidx.core.view.WindowCompat;
import androidx.core.view.WindowInsetsControllerCompat;

import com.masteryip.arkstudio.api.ApiException;
import com.masteryip.arkstudio.data.AccountSession;
import com.masteryip.arkstudio.data.LinkPolicy;
import com.masteryip.arkstudio.data.PairingLink;
import com.masteryip.arkstudio.data.SessionStatus;
import com.masteryip.arkstudio.model.BackGate;
import com.masteryip.arkstudio.model.WebRoutes;
import com.masteryip.arkstudio.model.WebTheme;
import com.masteryip.arkstudio.platform.Accounts;
import com.masteryip.arkstudio.ui.Async;
import com.masteryip.arkstudio.ui.Ui;

/**
 * The existing web app (Map, Editor, Research/Plan, Output, Settings …) in a
 * WebView signed in with the same device session.
 *
 * Security: the device token is handed over only as the HttpOnly
 * {@code ark_mobile} cookie for the signed-in origin, so page scripts never see
 * it; navigation is restricted to that origin and every other link opens in the
 * system browser. The only JS interface reports the page theme and carries no
 * credentials, and it is reachable only by pages from the server's origin.
 *
 * Like the former Web APK: the status and navigation bars take the page's
 * theme, back at the site's root goes to the server list after a second press
 * (the session stays), and cookies are flushed so the site stays signed in
 * across restarts.
 */
public class WebActivity extends BaseActivity {
    private static final String EXTRA_ROUTE = "route";
    private static final int REQUEST_FILE = 7;
    private static final int REQUEST_STORAGE = 8;

    private WebView web;
    private ProgressBar progress;
    private ValueCallback<Uri[]> pendingFiles;
    private String[] pendingDownload;
    private boolean backPending;
    private final BackGate backGate = new BackGate();
    private boolean checkingLogin;

    public static Intent intent(Context c, String route) {
        return new Intent(c, WebActivity.class).putExtra(EXTRA_ROUTE, route);
    }

    /** {@code web/js/state.js} calls this whenever the site's theme changes. */
    private final class ThemeBridge {
        @JavascriptInterface
        public void setTheme(String theme, String background) {
            runOnUiThread(() -> applyWebTheme(theme, background));
        }
    }

    /** Bars and the frame behind the page follow the site's theme. */
    private void applyWebTheme(String theme, String background) {
        if (isFinishing() || root == null) return;
        WebTheme t = WebTheme.of(theme, background, 0xFF0F141C, 0xFFF5F6F8);
        if (t == null) return;
        getWindow().setStatusBarColor(t.color);
        getWindow().setNavigationBarColor(t.color);
        WindowInsetsControllerCompat bars = WindowCompat.getInsetsController(getWindow(), getWindow().getDecorView());
        bars.setAppearanceLightStatusBars(!t.dark);
        bars.setAppearanceLightNavigationBars(!t.dark);
        root.setBackgroundColor(t.color);
        stage.setBackgroundColor(t.color);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (!ready()) return;
        String route = getIntent().getStringExtra(EXTRA_ROUTE);
        // Web mode matches the former standalone Web APK: no native toolbar or
        // padding, so the ARK page owns the entire app content viewport.
        buildFrame(getString(R.string.web_title), false);
        topBar.setVisibility(View.GONE);
        if (root.getChildCount() > 1) root.getChildAt(1).setVisibility(View.GONE);
        stage.removeAllViews();
        web = new WebView(this);
        stage.addView(web, new FrameLayout.LayoutParams(Ui.MATCH, Ui.MATCH));
        progress = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        progress.setIndeterminate(true);
        stage.addView(progress, new FrameLayout.LayoutParams(Ui.MATCH, Ui.dp(this, 4)));
        configure();
        Accounts.applyWebCookie(session);
        if (savedInstanceState != null) web.restoreState(savedInstanceState);
        else web.loadUrl(WebRoutes.url(session.serverUrl, route));
    }

    private void configure() {
        WebSettings settings = web.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setDatabaseEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        // target=_blank and window.open() reach onCreateWindow (openPopupUrl)
        // instead of replacing the ARK page.
        settings.setSupportMultipleWindows(true);
        settings.setJavaScriptCanOpenWindowsAutomatically(false);
        settings.setUserAgentString(settings.getUserAgentString() + " ARKAndroid/3.0");
        CookieManager.getInstance().setAcceptCookie(true);
        CookieManager.getInstance().setAcceptThirdPartyCookies(web, false);
        if (BuildConfig.DEBUG) WebView.setWebContentsDebuggingEnabled(true);
        web.addJavascriptInterface(new ThemeBridge(), "ARKAndroidTheme");
        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                return intercept(request.getUrl(), request.isForMainFrame());
            }

            @Override
            public void onPageStarted(WebView view, String url, Bitmap favicon) {
                progress.setVisibility(View.VISIBLE);
                if (!PairingLink.sameOrigin(session.serverUrl, url)) {
                    // Redirected off-origin: never render it here.
                    view.stopLoading();
                    Links.external(WebActivity.this, url);
                }
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                progress.setVisibility(View.GONE);
                view.evaluateJavascript(WebTheme.PROBE_JS, null);
                CookieManager.getInstance().flush();
                if (url != null && url.contains("/login")) checkLogin();
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
                if (request == null || !request.isForMainFrame()) return;
                progress.setVisibility(View.GONE);
                CharSequence detail = error == null ? "" : error.getDescription();
                stage.removeAllViews();
                stage.addView(refresher);
                content.removeAllViews();
                content.addView(stateView(WebActivity.this, new ApiException(0, ApiException.NETWORK,
                        String.valueOf(detail)), () -> {
                    stage.removeAllViews();
                    stage.addView(web, new FrameLayout.LayoutParams(Ui.MATCH, Ui.MATCH));
                    stage.addView(progress, new FrameLayout.LayoutParams(Ui.MATCH, Ui.dp(WebActivity.this, 4)));
                    web.reload();
                }));
            }

            @Override
            public void onReceivedSslError(WebView view, SslErrorHandler handler, android.net.http.SslError error) {
                handler.cancel();
                Toast.makeText(WebActivity.this, R.string.error_ssl_detail, Toast.LENGTH_LONG).show();
            }
        });
        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onCreateWindow(WebView view, boolean isDialog, boolean isUserGesture,
                                          android.os.Message resultMsg) {
                return ExternalLinks.catchPopup(WebActivity.this, resultMsg, WebActivity.this::openPopupUrl);
            }

            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback, FileChooserParams params) {
                if (pendingFiles != null) pendingFiles.onReceiveValue(null);
                pendingFiles = callback;
                try {
                    Intent chooser = params.createIntent();
                    if (params.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE) {
                        chooser.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
                    }
                    startActivityForResult(Intent.createChooser(chooser, getString(R.string.choose_file)), REQUEST_FILE);
                    return true;
                } catch (Exception failed) {
                    pendingFiles = null;
                    return false;
                }
            }
        });
        web.setDownloadListener((url, agent, disposition, mime, length) -> {
            if (!PairingLink.sameOrigin(session.serverUrl, url)) {
                Links.external(this, url);
                return;
            }
            pendingDownload = new String[]{url, agent, disposition, mime};
            if (Build.VERSION.SDK_INT <= 28 && checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE)
                    != PackageManager.PERMISSION_GRANTED) {
                requestPermissions(new String[]{Manifest.permission.WRITE_EXTERNAL_STORAGE}, REQUEST_STORAGE);
                return;
            }
            download();
        });
    }

    /** Same-origin http(s) stays; everything else leaves the WebView (LinkPolicy). */
    private boolean intercept(Uri uri, boolean mainFrame) {
        if (uri == null) return true;
        String url = uri.toString();
        return ExternalLinks.handle(this, url, LinkPolicy.decide(url, mainFrame, session.serverUrl));
    }

    /** target=_blank and window.open(): ARK pages load here, the rest leaves. */
    private void openPopupUrl(String url) {
        if (!ExternalLinks.handle(this, url, LinkPolicy.decide(url, true, session.serverUrl))) web.loadUrl(url);
    }

    private void download() {
        String[] spec = pendingDownload;
        pendingDownload = null;
        if (spec == null) return;
        try {
            String name = URLUtil.guessFileName(spec[0], spec[2], spec[3]);
            DownloadManager.Request request = new DownloadManager.Request(Uri.parse(spec[0]));
            if (spec[3] != null && !spec[3].isEmpty()) request.setMimeType(spec[3]);
            request.addRequestHeader("User-Agent", spec[1]);
            String cookie = CookieManager.getInstance().getCookie(spec[0]);
            if (cookie != null) request.addRequestHeader("Cookie", cookie);
            request.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
            request.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name);
            DownloadManager manager = (DownloadManager) getSystemService(DOWNLOAD_SERVICE);
            manager.enqueue(request);
            toast(getString(R.string.download_started));
        } catch (Exception failed) {
            toast(getString(R.string.download_failed, String.valueOf(failed.getMessage())));
        }
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] permissions, int[] results) {
        super.onRequestPermissionsResult(code, permissions, results);
        if (code == REQUEST_STORAGE) {
            if (results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED) download();
            else toast(getString(R.string.download_permission));
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQUEST_FILE && pendingFiles != null) {
            pendingFiles.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(resultCode, data));
            pendingFiles = null;
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        if (web != null) web.saveState(outState);
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (web != null) web.onResume();
    }

    @Override
    protected void onPause() {
        super.onPause();
        if (web != null) web.onPause();
        // Keep the site's cookies (sign-in, preferences) across app restarts.
        CookieManager.getInstance().flush();
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            web.removeJavascriptInterface("ARKAndroidTheme");
            web.destroy();
        }
        super.onDestroy();
    }

    @Override
    protected void reload() {
        doneRefreshing();
        if (web != null) web.reload();
    }

    /**
     * Back first closes web sheets and menus (ARKAndroidBack); at the site's
     * root a second press within 2.5 s goes to the server list, still signed in.
     */
    @Override
    public void onBackPressed() {
        if (web == null) {
            toServerList();
            return;
        }
        if (backPending) return;
        backPending = true;
        web.evaluateJavascript("(window.ARKAndroidBack && window.ARKAndroidBack()) === true", value -> {
            backPending = false;
            if ("true".equals(value)) {
                backGate.reset();
                return;
            }
            if (backGate.press(System.currentTimeMillis())) toServerList();
            else Toast.makeText(this, R.string.back_again_servers, Toast.LENGTH_SHORT).show();
        });
    }

    /** The server list without signing out (the device token and cookies stay). */
    private void toServerList() {
        CookieManager.getInstance().flush();
        startActivity(MainActivity.serverList(this));
        finish();
    }

    /**
     * The site showed its sign-in page. Only a token the server rejects signs
     * the phone out; otherwise the device cookie is set again and the site
     * reloads (a lost cookie, a server restart or no network never log out).
     */
    private void checkLogin() {
        if (checkingLogin || session == null) return;
        checkingLogin = true;
        AccountSession current = session;
        Async.run(this, () -> api.withTimeouts(6000, 10000).get("/api/mobile/session"), status -> {
            checkingLogin = false;
            if (!SessionStatus.signedIn(status)) {
                signedOut();
                return;
            }
            session = SessionStatus.apply(current, status);
            Accounts.update(this, session);
            Accounts.applyWebCookie(session);
            if (web != null) web.loadUrl(WebRoutes.url(session.serverUrl, WebRoutes.HOME));
        }, error -> {
            checkingLogin = false;
            if (error.isSignedOut()) signedOut();
        });
    }
}
