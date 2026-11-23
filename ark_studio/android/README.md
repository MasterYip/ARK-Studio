# ARK Studio for Android

Single APK for ARK Studio with two selectable modes. Web mode is the default and opens
the full same-origin WebView workspace. Native mode opens the Android screens for the
parts implemented natively, with the rest available through the Web button. Both modes
use the same signed-in device session, so there's no second login.

- Package `com.masteryip.arkstudio`, `versionCode 20` (the ARK release sets `versionName`)
- minSdk 26, target/compile 33, Java 11, AGP 7.4.2, Gradle 7.6
- Server side: `POST /api/mobile/login` and the project-switch endpoints
  (see `docs/guide/mobile.md` → *Android app*)

The Gradle project has one app module, `:app`, plus the updater sources in
`shared/update`. The former standalone WebView module is not built. Web and native
mode selection is stored on the device and defaults to Web mode.

## Screens

| Native | Web app (WebView, deep links) |
| --- | --- |
| **Servers**: saved servers with address, signed-in account and project, Open / Refresh, sign-in, sign-out and delete in the server menu; app settings (open with Web or Native, language, notifications, updates and version) | Map `#/map` |
| **Home**: project switcher, setup status, counts, Needs you (the web's review cards: "Review this" note with anchors, round and previous round, changes since the last review, one card per delivery with a member table, review groups; Accept / Send back / Comment, Accept all with a checklist), recent activity | Editor `#/editor/unit%3A<uid>` |
| **Tasks**: board by status (inbox, open, active, review, blocked, done); task detail, status change, notes linked to the task | Research · Ideas / Plan |
| **Runs**: sessions list (5 s refresh); thread with Markdown, collapsible tool groups and outputs, steer / continue composer, stop (2 s polling while active) | Output, Assistant, Files |
| **GPU**: servers, per-GPU util and memory bars with the project-owned share (port of `gpu-model.js`), queue (read-only) | Settings |
| **Node viewer**: review section (note, round, changes) and Accept / Send back / Add comment under the comment box; review anchors scroll to the heading; Markdown body, web fallback for Mermaid / math / HTML, zoomable images, video player streaming `/api/media/{id}/raw` with Range seeking, links | |
| **Share to ARK**: text, link or image → idea or note with the image attached | |

The UI has pull-to-refresh, offline / error states with Retry, and light and dark themes
that follow the system. Strings are in English (`values/`) and Chinese (`values-zh/`),
and the app follows the system language. On Android 13+ the language can
also be set per app (Settings → Apps → ARK Studio → Language, from `res/xml/locales_config.xml`).
Server codes (unit types, statuses, relations, activity) are labelled in `ui/Labels.java`.
The WebView follows the phone's language too until a language is picked in ARK
(`web/js/startup-graph.js`, `ark.langAuto`).
Bottom navigation: Home, Tasks, Runs, GPU, More.

## Web mode

- The status and navigation bars take the site's background and light or dark
  icons (`window.ARKAndroidTheme.setTheme` from `web/js/state.js`, plus a probe
  of `data-theme`, `--bg` and theme-color after each load; `model/WebTheme.java`).
  The native screens and the server list follow the system theme.
- Back first closes the site's sheets and menus (`window.ARKAndroidBack`). At the
  site's root a second back within 2.5 s opens the server list; the device token
  and cookies stay (`model/BackGate.java`). Native mode's Home tab does the same.
- Cookies are flushed after each page load and when the app pauses, so the site
  stays signed in across restarts. When the site shows its sign-in page, the app
  asks `GET /api/mobile/session`; only a token the server rejects signs the phone
  out, otherwise the device cookie is set again and the site reloads.
- A pairing is the user's: the site's project list and the native switcher move
  the same device session, so both modes show the same project.

## Sign-in and security

1. **Scan QR** (Settings → Mobile on the desktop). The app splits the URL into the
   base and `#pair=<code>`, claims the code once with `POST /api/mobile/pair/claim`,
   and keeps only the returned device token. The code is never stored.
2. **Server URL + account + password.** The app calls `POST /api/mobile/login` with
   JSON. The password is sent once, the response is a device token, and the password
   is dropped. "Read-only on this phone" asks for a read-only device.

**Cloudflare Tunnel** (a local ARK reached through `*.trycloudflare.com` or a named
tunnel; `server/internal/server/mobile_tunnel.go`, `TunnelFlows.java`):

- A claim through the tunnel answers 202 with a 6-digit confirm code. The phone shows
  the code and polls `POST /api/mobile/pair/claim/status` every 2 s until the computer
  confirms (token), declines (`PAIRING_REJECTED`) or the request expires
  (`PAIRING_CONFIRM_EXPIRED`). LAN servers still answer the claim with the token.
- After pairing, and later for servers saved without it, the app asks
  `GET /api/mobile/identity?nonce=…` (random nonce, no token), checks the P-256
  signature over context, server id, host and nonce, and stores the server id and
  public key with the saved server (`data/ServerIdentity.java`, `ServerStore` fields
  `serverId`, `publicKey`, `tunnel`; older lists load unchanged).
- Re-link: a quick tunnel gets a new address after a restart. A re-link QR
  (`#relink=<server id>`, through Scan pairing QR or Scan QR in the dialog), the short
  code (`able-baker-charlie-delta` → `https://able-baker-charlie-delta.trycloudflare.com/`)
  or a pasted address moves the saved server, its device token and the WebView cookie
  to the new address only after the new address signs a fresh nonce with the stored
  key, with the stored server id and the host the phone connected to. Otherwise
  nothing moves and the app explains why; an unknown server id asks to pair first.
- Tunnel servers carry a Cloudflare Tunnel chip. When one is unreachable (DNS,
  timeout, or a Cloudflare error page such as 530 / 1033 or 502), the server list,
  native screens and web mode offer Re-link, with a note that trycloudflare.com can be
  slow or blocked on some networks.

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

Notifications are opt-in (More → Notification settings; an explanation, then the
Android 13 permission prompt). There is no FCM (Google services are not available in
China): a WorkManager job every 15 minutes (network required, exponential backoff on
errors) and a check when the app opens read the feed `GET /api/notifications` of every
signed-in server (`ark_studio/server/internal/server/notifications.go`):

- new reviews the person has not seen yet, one per Needs you card (review groups and
  automatic delivery groups count once), with the browser's seen marks: a review opened
  in the web app is withdrawn from the phone;
- open decisions and pending approval requests of the person's agent sessions (high
  priority);
- the person's sessions that errored or that ARK stopped when it restarted (high);
- finished sessions (off by default).

A password sign-in covers every project the account can open; a phone paired by QR code
only its project. Notifications are grouped per server and project with a summary
("PredActor_Rebuttal: 3 new for review · 1 session error"); tapping one switches to its
project when needed and opens the task, node or run (`NotifyOpenActivity`). Channels: New
reviews, Decisions and approvals, Session errors, Other, App updates. Settings: kinds,
quiet hours (notifications arrive silently), mute per project. The feed → notification
mapping (first poll only records, dedupe, withdraw, groups, quiet hours) is
`model/NotifyFeed.java`. Servers without the feed fall back to the older run / review /
GPU diff of `model/Watch.java` for the selected project.

## Updates

About and updates (More) shows the version, checks for a newer APK (button, and a daily
WorkManager check with one notification per version) and offers Download and install.
The code is in `shared/update` (`update.gradle` adds the sources, strings and tests):

1. `GET /download/android-apps.json` on the paired server
   (`server/internal/server/app_manifest.go`): version code and SHA-256 of a `dist/`
   build, or the latest GitHub release's size and SHA-256 (`SHA256SUMS.txt`). Without an
   answer the app asks the GitHub API itself.
2. The download goes to the app cache and must match the published SHA-256.
3. Package, version code and signing certificate of the download are compared with the
   installed app first. A different signing key (a debug build from another machine, or
   CI builds without a fixed key) is explained instead of letting the installer fail.
4. `PackageInstaller` installs it; Android asks once for "install unknown apps".

Release builds are debug-signed. Set the repository secrets `ANDROID_KEYSTORE_BASE64`,
`ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` (and `ANDROID_KEY_PASSWORD`) so every
release is signed with the same key; otherwise each CI run uses a new key and an installed
release cannot update to the next one. `-ParkVersionCode=<n>` builds a test update.

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
- review notes (anchors, chips, headline numbers, heading ids) and Needs you cards from the server's `auto_group` (`ReviewModelTest`);
- board columns and importance, deep links, Markdown blocks and spans, the web-render
  fallback and notification diffs;
- the notification feed mapping (`NotifyFeedTest`) and the updater: versions, manifest
  and GitHub parsing, checksums, signature check (`UpdateModelTest`);
- the saved server list (`ServerStoreTest`).
- Cloudflare Tunnel pairing: the 202 confirm flow and Cloudflare error pages
  (`TunnelClientTest`), `#relink=` links, short codes, identity signatures with a
  generated P-256 key, saved identity fields and re-link moves (`TunnelPairingTest`).

Dependencies added (Google Maven): `androidx.work:work-runtime:2.7.1` and
`androidx.swiperefreshlayout:swiperefreshlayout:1.1.0`. The test-only `org.json:json:20180813` came
from the Gradle cache.

APKs are not tracked. Build with `gradle :app:assembleDebug`, then copy
`app/build/outputs/apk/debug/app-debug.apk` to the server's `dist/ARK-Studio-android.apk`.
It is served at `/download/ARK-Studio-android.apk` (aliases `/download/ark.apk` and
`/download/ARK-Studio-webapp.apk`).

The file is re-read on every request, so replacing it publishes a new build; without
a local file these paths redirect (302) to the latest GitHub release. Settings → Mobile
links to the APK of the latest GitHub release (`GET /api/mobile/apps`, resolved by the
server; see [the mobile guide](../../docs/guide/mobile.md#android-app)).
