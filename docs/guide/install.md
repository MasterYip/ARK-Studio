# Install ARK Studio

ARK Studio ships in two forms. Neither needs Python.

| Download | Use it for |
| --- | --- |
| **ARK Studio desktop app** (Windows, macOS, Ubuntu) | Working on this computer (a private local ARK) and on your ARK servers, from one window. |
| **`ark-server`** (single binary, Linux/macOS/Windows) | Running ARK headless on a server, behind HTTPS, for a team or for your phone. See [Server packages](#server-packages-ark-server). |

Downloads are on the project's GitHub **Releases** page. Every release lists
`SHA256SUMS.txt`. To verify a download, run `sha256sum -c SHA256SUMS.txt --ignore-missing`
(on macOS: `shasum -a 256 -c SHA256SUMS.txt`, on Windows: `Get-FileHash <file>`).

## Desktop app

| OS | File | Notes |
| --- | --- | --- |
| Windows 10/11 (x64) | `ARK Studio_<version>_x64-setup.exe` | Recommended. Installs for the current user, no administrator rights. |
| | `ARK Studio_<version>_x64_en-US.msi` | For managed/IT deployment (per-machine, needs administrator rights). |
| | `ARK-Studio_<version>_x64-portable.zip` | No installer: unzip and run `ark-studio.exe`. Keep `ark-server.exe` beside it. |
| macOS 10.15+ (Apple silicon) | `ARK Studio_<version>_aarch64.dmg` | M1 and newer. |
| macOS 10.15+ (Intel) | `ARK Studio_<version>_x64.dmg` | |
| Ubuntu 22.04+ / Debian 12+ (x64) | `ARK Studio_<version>_amd64.deb` | `sudo apt install "./ARK Studio_<version>_amd64.deb"` (package name `ark-studio`) |
| Other Linux (x64) | `ARK Studio_<version>_amd64.AppImage` | `chmod +x` it and run. Needs FUSE 2 (`libfuse2` on Ubuntu). |

Windows uses the Microsoft Edge WebView2 runtime, which Windows 10/11 already
include; the installer adds it if it is missing. On Linux the `.deb` pulls in
WebKitGTK (`libwebkit2gtk-4.1`). For the tray icon on GNOME, install the
"AppIndicator and KStatusNotifierItem Support" extension.

### Unsigned builds: Windows SmartScreen and macOS Gatekeeper

Until the project has code-signing certificates, the installers are
**unsigned** and the OS warns you the first time. Check the download against
`SHA256SUMS.txt` before you continue.

* **Windows (SmartScreen):** "Windows protected your PC". Click **More info**,
  then **Run anyway**. Some browsers also mark the download as "uncommon":
  choose **Keep** in the download list.
* **macOS (Gatekeeper):** "ARK Studio can't be opened because Apple cannot check
  it for malicious software". Open the `.dmg`, drag **ARK Studio** to
  **Applications**, then in Finder **Control-click** the app and choose **Open**,
  and confirm **Open**. On macOS 15 and newer, open the app once, then go to
  **System Settings > Privacy & Security** and click **Open Anyway**.
  You only do this once per version. (Terminal alternative:
  `xattr -dr com.apple.quarantine "/Applications/ARK Studio.app"`.)
* **Linux:** no warning. For the AppImage, mark it executable first.

Signed releases (when available) open without these steps.

### First run

ARK Studio starts its built-in ARK server in the background and opens the
local ARK **Home**: there is no connection window. The default project folder
and port (`18890`, or a free one when it is busy) are used; the first start
creates a demo project you can explore or delete.

Your ARK servers are one click away:

* **Add a server:** tray menu > **Servers > Connect to a server…** (or
  **Settings > Servers**, or **Connect to a server…** in the project switcher
  of the local Home). Enter its address, for example `https://ark.example.com`.
  The address is saved; you sign in on the server's own page and ARK Studio
  stores no passwords.
* **Switch:** the project switcher (click the project name at the top left)
  lists **This computer** (your local projects) and **Servers** (the saved
  servers, with an online/offline mark). Click a server to open it in the
  window. **New or open project…** has a **Location**: this computer or one of
  the servers (which opens that server's new-project dialog).
* **Back to this computer:** on a server's page, the switcher's **Back to
  this computer**, the house button beside the window buttons, or the tray's
  **This computer**.
* **See a server's projects at home:** sign in on the server once and choose
  **Link this desktop** ([desktop links](desktop-link.md)); its projects then
  appear under it in the local switcher, and new projects can be created there
  from the local Home. Revoke the link in the server's device list.

The project folder and port can be changed under **Settings > This computer**
(tray > **Settings…**).

The window has no system title bar: drag the empty part of ARK's top bar to
move it, double-click it to maximize, and use the buttons at its right end
(on macOS the usual traffic lights at the left). The window edges resize it.

**Git** is not bundled. ARK works without it, but node history and project
history need it: install Git from <https://git-scm.com> (macOS:
`xcode-select --install`; Ubuntu: `sudo apt install git`) and restart
ARK Studio. **Settings** shows a notice when Git is missing.
`ssh` (GPU servers, tunnels) and agent CLIs (Claude Code, Codex, ...) are also
used from your system when present.

### This computer and servers

| | This computer | A server |
| --- | --- | --- |
| Where ARK runs | the bundled `ark-server`, on this computer, bound to `127.0.0.1` only | on the server you connect to |
| Who can reach it | only the ARK Studio window: other users and programs on this computer get "This ARK server belongs to the ARK Studio desktop app" | whoever the server admits (its sign-in) |
| Data | the project folder you chose (see below) | on the server |
| Open in a browser | tray menu > **Open in browser** (a one-time sign-in link) | the same address in any browser |
| Stops | when you quit ARK Studio or choose **Stop local server** | when the server stops |

Local files linked from notes on a remote server (`file:///...` links)
open on your computer: inside folders listed in **Settings > Folders remote
servers may open** they open directly, anywhere else ARK Studio asks first.
Programs and scripts are never started this way; they are shown in the file
manager instead.

### Tray menu and closing

The tray (menu bar on macOS) icon offers: Open ARK Studio, Servers (the saved servers and Connect to a server…), Open in
browser, Start / Stop local server, Settings…, Open data folder, Show server
log, Quit ARK Studio. Closing the window keeps ARK Studio running in the tray
(the default). To quit on close instead, turn off **Settings > Keep running
in the tray when the window is closed**; quitting stops the local server.

### Where your data lives

Where ARK keeps local projects, keys, logs and settings depends on the package:

| Package | Default project folder | Keys, logs, settings |
| --- | --- | --- |
| Windows installer (`…-setup.exe`, per user) | `<install folder>\projects`, by default `%LOCALAPPDATA%\ARK Studio\projects` | `<install folder>\data` |
| Windows portable zip | `projects\` next to `ark-studio.exe` | `data\` next to `ark-studio.exe` |
| Windows MSI (Program Files) | `%LOCALAPPDATA%\ARK\projects` | `%LOCALAPPDATA%\ARK` |
| macOS `.app` | `~/Library/Application Support/ARK/projects` | `~/Library/Application Support/ARK` |
| Linux `.deb`, AppImage | `$XDG_DATA_HOME/ark/projects` (`~/.local/share/ark/projects`) | `$XDG_DATA_HOME/ark` |

* The app folder is only used when ARK can write there; otherwise ARK uses the
  per-user folder of the last three rows and **Settings** says so.
* If you used an earlier version, ARK keeps using your existing per-user folder
  (`%LOCALAPPDATA%\ARK` on Windows); nothing is moved.
* **Settings > Data** shows the folder in use, and **Settings > This
  computer** picks any other project folder.

The data folder holds `private/` (sealed personal credentials and keys),
`logs/ark-server.log`, `desktop.json` (settings and saved server addresses, no passwords),
`private/server-links.json` (desktop-link device tokens, see [desktop links](desktop-link.md)) and
`sidecar.json`. **Settings > Open data folder** opens it. Back up the project
folder and `private/` together.

The local server uses the same data format as any `ark-server` or Python ARK,
so a project folder can be moved to a server later (see
[server operations](../operations/server-operations.md)).

### Updates

Builds with automatic updates enabled check GitHub Releases from
**Settings > Updates** and install with one click. When a newer version is
found, the local Home shows a banner: **Update** installs it the same way,
**Later** hides the banner until the next version. Other builds say so there;
download the new version and install it over the old one (your data stays).
The portable zip never updates itself.

* **Installing over an existing copy** (a newer or older `…-setup.exe`) keeps
  the install folder, its `projects\` and `data\`, your settings, saved
  servers and desktop links, and the shortcuts. ARK Studio is closed first
  (the installer asks). Either choice on the "already installed" page keeps
  your data; there is no need to uninstall first.
* **Portable zip:** quit ARK Studio, unzip the new version, and copy
  `projects\` and `data\` from the old folder into the new one (or copy the
  new files into the old folder, replacing the old ones). A portable copy in
  a new, empty folder starts without your projects, unless
  `%LOCALAPPDATA%\ARK` holds data from an earlier version (then it uses
  that).

### Uninstall

Uninstalling removes the app, never your projects or data.

* **Windows installer:** Settings > Apps > ARK Studio > Uninstall. The
  uninstaller removes only the program files; `projects\` and `data\` stay in
  the install folder (it tells you where). The "Delete the application data"
  checkbox (off by default) removes only the app's WebView data: window
  size and position, page settings such as theme and language, and sign-ins
  to remote servers. Projects, keys, settings and saved servers stay.
* **Windows portable:** delete the program files; keep `projects\` and `data\`
  if you want your work.
* **Windows MSI:** uninstall from Settings > Apps; `%LOCALAPPDATA%\ARK` stays.
* **macOS:** quit ARK Studio, drag it from Applications to the Trash.
* **Ubuntu:** `sudo apt remove ark-studio`. AppImage: delete the file.

To remove your data as well, delete the folders listed in [Where your data lives](#where-your-data-lives).

## Server packages (`ark-server`)

For headless/server use download `ark-server-<version>-<os>-<arch>.tar.gz`
(`.zip` on Windows) or, on Debian/Ubuntu, `ark-server_<version>_<arch>.deb`
(amd64, arm64). Each archive contains the binary, `LICENSE`, a `README.md`,
an example server config and a systemd user unit.

```sh
tar -xzf ark-server-<version>-linux-amd64.tar.gz
cd ark-server-<version>-linux-amd64
./ark-server version
./ark-server serve --root ~/ark/projects        # http://127.0.0.1:8890/
```

The `.deb` installs `/usr/bin/ark-server` and the unit template
`/usr/lib/systemd/user/ark-server@.service` without starting anything. For a
permanent server with sign-in, HTTPS and systemd, follow the package README
and [server operations](../operations/server-operations.md); moving an existing
Python installation is described in
[switch-over](../development/go-port/switch-over.md).

The desktop app's remote mode, browsers and the Android app all connect to
such a server.
