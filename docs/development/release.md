# Releasing ARK Studio

A release produces, from one commit:

* the **desktop app** (Tauri 2, `ark_studio/desktop/`) with the Go `ark-server` as its
  sidecar: Windows NSIS installer + MSI + portable zip, macOS `.dmg` (arm64 and
  x64), Ubuntu `.deb` + AppImage;
* **standalone `ark-server`** archives for linux amd64/arm64, darwin
  arm64/amd64 and windows amd64, plus `ark-server` `.deb` packages
  (amd64, arm64);
* `SHA256SUMS.txt` over all files, and the updater's `latest.json` when the
  updater key is configured.

No Python is shipped. The Python desktop pieces (`ark_studio/python/arkstudio/desktop.py`,
`ark_studio/python/arkstudio/launcher.py`, `scripts/release/build-tray-launcher.ps1`) stay in the
repository with the deprecated Python server but are not part of a release.

## Version

`VERSION` (repository root) is the single source. `scripts/release/version.mjs`
copies it into `ark_studio/python/pyproject.toml`, `ark_studio/python/arkstudio/__init__.py`,
`ark_studio/desktop/package.json`, `ark_studio/desktop/src-tauri/Cargo.toml` and
`ark_studio/desktop/src-tauri/tauri.conf.json`, and checks them; `ark-server` gets it at
build time (`-ldflags "-X main.version=<VERSION>"`, see
`scripts/release/build-server.sh`; a plain `go build` reports `2.0-go`).

```sh
node scripts/release/version.mjs --set 2.1.0   # VERSION + all copies
node scripts/release/version.mjs --check       # CI runs this
```

## Cutting a release

1. On the release branch: `node scripts/release/version.mjs --set X.Y.Z`,
   update `CHANGELOG.md`, run the JS and Python suites and `go vet ./... &&
   go test ./...`, commit.
2. Tag and push: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. The **Release** workflow (`.github/workflows/release.yml`) builds every
   target and creates a **draft** GitHub release named "ARK Studio X.Y.Z"
   with all files attached. A tag with a hyphen (`v2.1.0-rc.1`) is marked as a
   pre-release. The workflow fails early if the tag does not equal
   `v` + `VERSION`.
4. Download a few artifacts, check them (install, first run, local and remote
   mode), then **publish** the draft. Publishing is what makes the updater's
   `latest.json` (served from `releases/latest/download/latest.json`) visible
   to installed apps, and runs **Updater manifest**
   (`.github/workflows/updater-manifest.yml`), which copies it to the fixed
   `updater` release (see [Updater](#updater)).

`workflow_dispatch` (Actions > Release > Run workflow) builds the same files
as workflow artifacts without creating a release, for testing a branch.

**Which versions are built.** Only minor versions get release builds: tags
`v<major>.<minor>.0` and their pre-releases (`v2.1.0`, `v2.1.0-rc.1`). Patch
versions (`v2.0.1`) are tagged but not built; their fixes ship in the next
minor release, or run the workflow by hand if a patch must ship packages.
To check a desktop (Tauri) change without the full matrix, run **Desktop check**
(`desktop-check.yml`, one Linux runner: sidecar build and `cargo test --lib`).

## CI workflow

| job | runner | does |
| --- | --- | --- |
| `version` | ubuntu-22.04 | `version.mjs --check`; on tags `--tag` |
| `server` | ubuntu-22.04 | `go vet`, desktop-gate tests, `build-server.sh` (all five targets, web UI and agent docs embedded), `go test ./internal/agentdocs/` with `ARK_REQUIRE_EMBEDDED_DOCS=1`, `build-server-deb.sh` (amd64, arm64), smoke test of the linux binary (`docs-check`, `/api/health`, `GET /api/agent/v1/docs` with the agent token); artifact `ark-server` |
| `desktop` | windows-latest, macos-15 (aarch64 native, x86_64 cross-built), ubuntu-22.04 | Go sidecar for the target triple (`ark_studio/desktop/src-tauri/binaries/ark-server-<triple>`) and `docs-check` on it (native targets), Linux deps (`libwebkit2gtk-4.1-dev`, `libayatana-appindicator3-dev`, `librsvg2-dev`, ...), optional signing setup, `tauri-action` with `--target <triple>`; Windows portable zip; artifact `desktop-<name>` |
| `android` | ubuntu-22.04 | the Android app (`:app`, debug build signed with the release key, see [Android](#android)) as `ARK-Studio-<version>-android.apk`; artifact `android`. Keep this name: the server finds the APK of the latest release by it (`ARK-Studio-*-android.apk`; `ark_studio/server/internal/server/app_release.go`, [mobile guide](../guide/mobile.md#android-app)) |
| `checksums` | ubuntu-22.04 | `SHA256SUMS.txt` over all artifacts; on tags uploads server packages, portable zip, APKs and checksums to the draft release |

Actions are pinned by commit SHA (the tag is in a comment). To bump one, look
up the new tag's commit (`git ls-remote --tags https://github.com/<owner>/<repo>`)
and replace both.

## Building locally

```sh
# standalone server packages (any OS with Go and bash; no C toolchain)
bash scripts/release/build-server.sh                       # -> dist/server/
bash scripts/release/build-server.sh linux/amd64           # one target

# desktop app for this machine
bash scripts/release/build-server.sh --no-archive --sidecar ark_studio/desktop/src-tauri/binaries <goos>/<goarch>
cd desktop && npm ci && npx tauri build
```

### Embedded web UI and agent docs

`build-server.sh` runs `go generate ./internal/webui ./internal/agentdocs`
before `go build`: `cmd/webgen` copies `ark_studio/web/`, `cmd/docsgen` copies the
documentation agents read through `GET /api/agent/v1/docs` (the include list
is in `ark_studio/server/internal/agentdocs/include.go`; see
[external agents](../agents/external-agent.md#documentation-for-agents)).
It then runs `ark-server docs-check --docs ../docs`, which fails when the
embedded docs are missing, lack a start-here doc or differ from `docs/`, and
runs `docs-check` again on every built binary the host can execute. A
hand-built binary needs the same step:

```sh
cd ark_studio/server && go generate ./... && go build ./cmd/ark-server && ./ark-server docs-check
```

Without `go generate` a binary has no embedded docs (or web UI) and reads
`docs/` from disk beside the `ark_studio/web/` folder it serves (development).

The sidecar must be named for the Rust target triple Tauri builds for
(`ark-server-x86_64-pc-windows-msvc.exe`, `ark-server-aarch64-apple-darwin`,
...); `build-server.sh --sidecar` does that. With the GNU Rust toolchain on
Windows (no Visual Studio) copy the `-msvc.exe` sidecar to
`ark-server-x86_64-pc-windows-gnu.exe`; such builds also ship
`WebView2Loader.dll` (MSVC builds link it statically). Linux needs the same
packages as CI; see the Tauri prerequisites.

`npx tauri dev` runs the app from source; set `ARK_DESKTOP_SERVER` to an
`ark-server` binary and `ARK_DESKTOP_DATA_DIR` to a scratch folder to keep test
data out of your real data folder.

## Signing

Every signing step is optional and skipped when its secret is absent; an
unsigned build still completes. Secrets go to *Settings > Secrets and
variables > Actions*; values marked *variable* are repository variables (not
secret).

### Windows

Unsigned installers trigger SmartScreen ("Windows protected your PC"). Pick
one:

* **Azure Trusted Signing** (recommended, ~USD 10/month, needs an Azure
  subscription and identity validation of the publisher):
  variables `AZURE_TS_ENDPOINT` (e.g. `https://wus2.codesigning.azure.net`),
  `AZURE_TS_ACCOUNT`, `AZURE_TS_PROFILE`; secrets `AZURE_TENANT_ID`,
  `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET` (an app registration with the
  "Trusted Signing Certificate Profile Signer" role). CI installs
  `trusted-signing-cli` and sets Tauri's `bundle.windows.signCommand`.
* **Code-signing certificate (PFX)** from a CA (OV or EV): secrets
  `WINDOWS_CERTIFICATE` (base64 of the `.pfx`) and
  `WINDOWS_CERTIFICATE_PASSWORD`; optional variable `WINDOWS_TIMESTAMP_URL`.
  CI imports it and sets `certificateThumbprint`. EV certificates on hardware
  tokens cannot be used from hosted runners.

SmartScreen reputation builds up per certificate over downloads; EV and Trusted
Signing start with better reputation.

### macOS

Without an Apple Developer ID the app is ad-hoc signed (`signingIdentity "-"`,
which Apple silicon requires) and Gatekeeper asks users to confirm the first
start (see [install](../guide/install.md#unsigned-builds-windows-smartscreen-and-macos-gatekeeper)).
For a signed and notarized app (Apple Developer Program, USD 99/year):

* `APPLE_CERTIFICATE`: base64 of the exported "Developer ID Application"
  `.p12`; `APPLE_CERTIFICATE_PASSWORD`; `APPLE_SIGNING_IDENTITY`, e.g.
  `Developer ID Application: Name (TEAMID)`;
* notarization: `APPLE_ID`, `APPLE_PASSWORD` (an app-specific password),
  `APPLE_TEAM_ID`.

The sidecar is signed together with the app.

### Android

The app updates itself only when every release is signed with the same key:
Android refuses an APK signed with a different key ("package conflicts").
The `android` job signs the debug build with a fixed key when these secrets
are set (`ark_studio/android/shared/update/update.gradle`):

| Name | Kind | Value |
|---|---|---|
| `ANDROID_KEYSTORE_BASE64` | secret | base64 of the keystore (`.jks`, PKCS12) |
| `ANDROID_KEYSTORE_PASSWORD` | secret | the keystore password |
| `ANDROID_KEY_ALIAS` | secret | the key's alias |
| `ANDROID_KEY_PASSWORD` | secret | the key's password (defaults to the keystore password) |

Without them each run signs with the runner's throwaway debug key, and no
release can update the previous one. Releases up to 2.1.1 were built that
way; 2.1.2 is the first with the fixed key, so a phone with an older APK
uninstalls it once and installs 2.1.2. Keep the keystore backed up outside
the repository; losing it means another one-time uninstall for every user.
Bump `versionCode` in `ark_studio/android/app/build.gradle` for every release
(`version.mjs` does not touch it): the app installs only a higher version code.

### Updater

The Tauri updater only installs updates signed with the project's updater
key. The public key is committed in `tauri.conf.json`
(`plugins.updater.pubkey`); a build-time `ARK_UPDATER_PUBKEY` overrides it.
CI also needs the private key to sign the update bundles:

| Name | Kind | Value |
|---|---|---|
| `TAURI_SIGNING_PRIVATE_KEY` | secret | the private key file (`npx tauri signer generate -w <file>`) |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | secret | its password |
| `ARK_UPDATER_PUBKEY` | variable | the public key (same as in `tauri.conf.json`) |

Set them on every repository that runs `release.yml`. With all three, the
desktop jobs sign their update bundles (`createUpdaterArtifacts`, via
`scripts/release/ci-tauri-config.mjs`) and the `checksums` job writes one
`latest.json` for every platform from the `.sig` files
(`scripts/release/updater-json.mjs`: Windows NSIS and MSI, macOS arm64 and
x64, Linux AppImage and .deb) and attaches it to the draft release. A
missing platform fails the job.

Apps check two addresses in order (`plugins.updater.endpoints`):

1. `https://github.com/MasterYip/ARK-Studio/releases/latest/download/latest.json`,
   the latest published release;
2. `https://github.com/MasterYip/ARK-Studio/releases/download/updater/latest.json`,
   a fixed release with the tag `updater` that holds a copy of the newest
   published `latest.json`.

The updater takes the first address that answers with a manifest; a 404
moves on to the next. The second address covers a newer release published
without `latest.json` (for example a patch release built by hand), which
would otherwise make every check fail.

**Updater manifest** (`.github/workflows/updater-manifest.yml`) keeps the
copy current: when a release is published it copies that release's
`latest.json` to the `updater` release, creating it when missing. It skips
pre-releases, releases without `latest.json`, a manifest whose version does
not match the tag and versions older than the current copy
(`scripts/release/updater-mirror.mjs`, `tests/js/updater_mirror.test.mjs`).
The `updater` release is a pre-release, so `releases/latest` never resolves
to it; leave it in place. To refresh the copy by hand, run the workflow with
the tag of a published release.

Apps check at start and every 12 hours while running (Settings > About or
App, can be switched off), and on "Check for updates". When a newer version
is found, the app shows a system notification once per version and a notice
(**Update** / **Later**, dismissed per version) on every ARK page in its
window, and the activity bar's About button and the tray menu mark it.
Settings > About and App on the local page and the app's own settings
window offer the check, the install and the automatic-check switch. After
the first start of a new version, the local Home and About show "ARK Studio
was updated to ..." with the first items of that version's CHANGELOG.md
section (bundled at build time; keep the section filled before tagging).
`ark-server` puts its release version in `<meta name="ark-version">`; the web
app shows "This server was updated to ..." once per person and version.

Keep the private key backed up outside the repository: losing it means
installed apps can no longer update automatically, and a new key needs a
release carrying the new public key that users install by hand.

## Platform notes

* **Windows upgrades** stop an `ark-server` left running from the install
  folder before files are replaced (`ark_studio/desktop/src-tauri/windows/hooks.nsh` runs
  `ark-studio.exe --stop-sidecars`; tauri-apps/tauri#15134). The app also
  stops a sidecar recorded in `sidecar.json` at start, and runs its sidecar in
  a kill-on-close job object. The MSI has no such hook; MSI upgrades rely on
  the job object and the restart manager.
* **Local-mode security**: the app starts `ark-server` with
  `ARK_DESKTOP_SECRET` (environment), which turns on ark-server's desktop gate
  (`ark_studio/server/internal/api/desktopgate`): only the app's window (cookie from a one-time
  signed claim URL) and tokens the server issued are admitted on the loopback
  port. With the variable set, the server also exits when its stdin closes.
* **What CI verifies that a Windows machine cannot**: the macOS `.app`/`.dmg`
  (signing, Overlay title bar, traffic lights) and the Linux `.deb`/AppImage
  (WebKitGTK, tray via libayatana-appindicator, undecorated window resizing).
