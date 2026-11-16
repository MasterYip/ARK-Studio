# ARK Studio for Android

Hybrid native app for ARK Studio: everyday screens are native, and the rest of the web
app opens in a same-origin WebView signed in with the same device session, so there's
no second login.

- Package `com.masteryip.arkstudio`, `versionCode 15`, `versionName 3.0`
- minSdk 26, target/compile 33, Java 11, AGP 7.4.2, Gradle 7.6
- Server side: `POST /api/mobile/login` and the project-switch endpoints
  (see `docs/guide/mobile.md` → *Android app*)

## Two apps from one project

| Module | App | Package | What it is |
|---|---|---|---|
| `:app` | **ARK Studio** (3.0) | `com.masteryip.arkstudio` | Hybrid native app described below |
| `:webapp` | **ARK Studio Web** (3.0-web) | `com.masteryip.arkstudio.web` | The original lightweight shell: a native server list (QR or URL) that opens the full web app in a WebView; pairing happens inside the page |

The package IDs differ, so both can be installed side by side. `:webapp` is the
pre-native shell (from `f1e1e95`) with only the ID, name, version and shortcut
target changed; its `ServerStoreTest` still runs.

## Screens

| Native | Web app (WebView, deep links) |
| --- | --- |
| **Servers**: saved servers, account / permission / transport chips, sign-in, sign-out, delete | Map `#/map` |
| **Home**: project switcher, setup status, counts, needs attention, recent activity | Editor `#/editor/unit%3A<uid>` |
| **Tasks**: board by status (inbox, open, active, review, blocked, done); task detail, status change, notes linked to the task | Research · Ideas / Plan |
| **Runs**: sessions list (5 s refresh); thread with Markdown, collapsible tool groups and outputs, steer / continue composer, stop (2 s polling while active) | Output, Assistant, Files |
| **GPU**: servers, per-GPU util and memory bars with the project-owned share (port of `gpu-model.js`), queue (read-only) | Settings |
| **Node viewer**: Markdown body, web fallback for Mermaid / math / HTML, zoomable images, video player streaming `/api/media/{id}/raw` with Range seeking, links | |
| **Share to ARK**: text, link or image → idea or note with the image attached | |

The UI has pull-to-refresh, offline / error states with Retry, and light and dark themes
that follow the system. Strings are in English (`values/`) and Chinese (`values-zh/`).
Bottom navigation: Home, Tasks, Runs, GPU, More.

## Sign-in and security

1. **Scan QR** (Settings → Mobile on the desktop). The app splits the URL into the
   base and `#pair=<code>`, claims the code once with `POST /api/mobile/pair/claim`,
   and keeps only the returned device token. The code is never stored.
2. **Server URL + account + password.** The app calls `POST /api/mobile/login` with
   JSON. The password is sent once, the response is a device token, and the password
   is dropped. "Read-only on this phone" asks for a read-only device.

- The device token (`arkm_…`) is sealed with AES-256-GCM using a non-exportable
  AndroidKeyStore key (`platform/SecureStorage.java`). Prefs hold only the ciphertext
  plus non-secret metadata, and `allowBackup=false`. Jetpack security-crypto was not
  used because Tink is only on Maven Central, which was unreachable (HTTP 429); the
  scheme is the same.
- Native calls send `X-ARK-Mobile-Token`. The WebView gets the same token as the
  HttpOnly `ark_mobile` cookie for that origin only.
- The WebView loads only the signed-in origin. Other links and redirects go to the
  system browser. Third-party cookies, file and content access, and mixed content are
  off. The only JS interface (theme) carries no data.
- Cleartext is allowed for LAN, tunnel and emulator servers. A plain-http public host
  is flagged `UNENCRYPTED`, and the app asks before sending a password to it.
- Sign-out revokes the token with `DELETE /api/mobile/session`. A revoked or expired
  token returns the app to the server list.
- The debug build accepts `am start -a com.masteryip.arkstudio.DEBUG_PAIR --es url
  '<pairing URL>'` (`src/debug` manifest) so emulators can exercise the QR path.
  Release builds don't include that filter.

## Notifications

Notifications are opt-in (More → Notifications, Android 13 permission prompt). A
WorkManager periodic job runs every 15 minutes when the network is available; there is
no FCM. It polls runs, review tasks and the GPU queue of the signed-in server and
notifies on transitions only. The diff logic is `model/Watch.java`.

## Build and test

```powershell
$env:JAVA_HOME = '<path to JDK 11>'
$env:ANDROID_HOME = '<path to Android SDK>'
cd android
gradle clean testDebugUnitTest lintDebug assembleDebug
```

JVM tests (`app/src/test`) cover:
- the API client against an in-process HTTP server: headers, PATCH, error codes,
  Retry-After, signed-out detection;
- pairing-link parsing, cleartext classification and origin checks;
- the session vault (no plaintext token stored, key loss forgets the session);
- GPU ownership (the six `tests/js/gpu_model.test.mjs` cases);
- the transcript → blocks port of `session_model.test.mjs`;
- board columns and importance, deep links, Markdown blocks and spans, the web-render
  fallback and notification diffs;
- the existing `ServerStoreTest`.

Dependencies added (Google Maven): `androidx.work:work-runtime:2.7.1` and
`androidx.swiperefreshlayout:swiperefreshlayout:1.1.0`. The test-only `org.json:json:20180813` came
from the Gradle cache.

APKs are not tracked. Build both with
`gradle :app:assembleDebug :webapp:assembleDebug`, then copy them to the server's `dist/`:

| Build output | Copy to | Download URL |
|---|---|---|
| `app/build/outputs/apk/debug/app-debug.apk` | `dist/ARK-Studio-android.apk` | `/download/ARK-Studio-android.apk` (alias `/download/ark.apk`) |
| `webapp/build/outputs/apk/debug/webapp-debug.apk` | `dist/ARK-Studio-webapp.apk` | `/download/ARK-Studio-webapp.apk` |

Settings → Mobile lists both (`GET /api/mobile/apps` reports which are present, their size
and date), and each file is re-read on every request, so replacing it publishes a new build.
