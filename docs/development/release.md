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
   to installed apps.

`workflow_dispatch` (Actions > Release > Run workflow) builds the same files
as workflow artifacts without creating a release, for testing a branch.

## CI workflow

| job | runner | does |
| --- | --- | --- |
| `version` | ubuntu-22.04 | `version.mjs --check`; on tags `--tag` |
| `server` | ubuntu-22.04 | `go vet`, desktop-gate tests, `build-server.sh` (all five targets, web UI and agent docs embedded), `go test ./internal/agentdocs/` with `ARK_REQUIRE_EMBEDDED_DOCS=1`, `build-server-deb.sh` (amd64, arm64), smoke test of the linux binary (`docs-check`, `/api/health`, `GET /api/agent/v1/docs` with the agent token); artifact `ark-server` |
| `desktop` | windows-latest, macos-15 (aarch64 native, x86_64 cross-built), ubuntu-22.04 | Go sidecar for the target triple (`ark_studio/desktop/src-tauri/binaries/ark-server-<triple>`) and `docs-check` on it (native targets), Linux deps (`libwebkit2gtk-4.1-dev`, `libayatana-appindicator3-dev`, `librsvg2-dev`, ...), optional signing setup, `tauri-action` with `--target <triple>`; Windows portable zip; artifact `desktop-<name>` |
| `checksums` | ubuntu-22.04 | `SHA256SUMS.txt` over all artifacts; on tags uploads server packages, portable zip and checksums to the draft release |

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

### Updater

The Tauri updater only installs updates signed with the project's updater
key. Until the key exists the updater is compiled out and Settings says
"Automatic updates are not enabled in this build".

```sh
cd desktop && npx tauri signer generate -w ~/.tauri/ark-studio.key
```

Store the private key as secret `TAURI_SIGNING_PRIVATE_KEY` (and its password
as `TAURI_SIGNING_PRIVATE_KEY_PASSWORD`) and the public key as variable
`ARK_UPDATER_PUBKEY`. With both set, CI builds the updater into the app
(`ARK_UPDATER_PUBKEY` at compile time), signs the update bundles
(`createUpdaterArtifacts`) and `tauri-action` uploads `latest.json` to the
release. Apps check
`https://github.com/MasterYip/ARK-Studio/releases/latest/download/latest.json`
(`plugins.updater.endpoints` in `tauri.conf.json`). That URL must be publicly
readable: if you build from a private fork, publish releases to a public
repository and change the endpoint.
Keep the private key backed up: losing it means installed apps can no longer
update automatically.

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
