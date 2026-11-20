# ARK Studio desktop app

The desktop app is a small native shell (Tauri 2, in `ark_studio/desktop/`) around the
ARK web UI. It has no Python and no bundled web UI of its own: its window
always shows a server's own pages.

* It always runs the bundled `ark-server` on `127.0.0.1` and opens the local
  ARK **Home** at start: one default page, no connection chooser.
* Saved ARK servers (addresses, no passwords) open in the same window, one
  click away from the local Home, and the local Home is one click away from
  them.

Download, installation, unsigned-build warnings, data locations and uninstall:
[Install ARK Studio](install.md).

## One window, this computer and servers

* **Start:** the local server starts in the background with the saved project
  folder and port (default `18890`; a busy port falls back to a free one for
  the session). Meanwhile the window shows "Starting ARK…"; if the server
  cannot start, it shows the error with **Try again**, **Settings** and the
  server log.
* **Project switcher** (the project name at the top left, or the ARK brand) on
  the local Home: **This computer** lists the local projects as before;
  **Servers** lists the saved servers with an online / offline / checking
  mark, and **Connect to a server…**. Clicking a server opens it in the window
  (a server that does not answer stays closed and a message says so; the local
  Home is not blocked by slow or offline servers).
* **Linked servers** ([desktop links](desktop-link.md)): after **Link this
  desktop** on a server (once, signed in there), its projects are listed under
  it; a click opens the project on the server. An unlinked server offers
  **Link this desktop**; a revoked link says so.
* **New or open project…** (the Projects dialog) shows the same groups. Its
  new-project form has a **Location**: **This computer**, or one of the saved
  servers. On a linked server the app creates the project there and opens it;
  on an unlinked one the server's own new-project dialog opens in the window
  (`?ark_new_project=1`; sign in there if asked).
* **On a server's page:** the switcher starts with **This computer > Back to
  this computer** and, until linked, **Link this desktop** (servers with this
  ARK version). Local and remote projects are both reached through the
  switcher; on a server's sign-in page, **Settings > Open the local Home**
  goes back.
* A server's web app also honours `?ark_open_project=<id>` (opens that
  project), `?ark_new_project=1` (opens the Projects dialog) and, in the app,
  `?ark_link_desktop=1` (the link prompt).

## Settings

Tray > **Settings…** (or **Settings** on the status page, or **Connect to a
server…** in the switcher) opens a Settings window:

* **This computer**: project folder and port, **Apply and open the local
  Home** (restarts the local server when they changed), whether it runs and
  on which port.
* **Servers**: the saved servers (open, **Link…** / **Unlink**, remove) and
  **Connect to a server…**
  (address field; an address that does not answer can still be opened with
  **Open anyway**).
* **Folders remote servers may open**, **Window** (keep running in the tray
  when the window is closed; on by default), **Data** (data folder, server
  log), **Updates**.

Settings from earlier versions are migrated (`desktop.json` version 2): the
connection chooser's last remote address joins the saved servers, and
"open this choice automatically" is dropped because the app always opens the
local Home.

## Windows and chrome

Both windows are borderless and drawn in ARK's style, light or dark like the
rest of ARK.

* **Settings window**: a card with a soft shadow and rounded corners
  (Windows/Linux), title bar with minimize and close.
* **Main window**: ARK's top bar is the title bar. Drag its empty space to move
  the window, double-click to maximize; the minimize / maximize / close buttons
  sit at its right end (and, on a server's page, the house button). Edges and
  corners resize the window; Windows Snap works. On Windows 11 the window has
  rounded corners and the system shadow.
* **macOS**: the native traffic lights sit at the top left over ARK's top bar
  (the app keeps the native window frame with a hidden title bar), so
  fullscreen, zoom and window tiling behave like any Mac app.
* **Linux**: borderless with ARK's buttons; the shadow and rounded corners of
  the Settings window need a compositing window manager.

The ARK web app recognizes the desktop app (`window.__ARK_DESKTOP__`) and
marks its top bar as the drag region (`ark_studio/web/js/desktop-shell.js`); browsers are
unaffected. Pages that do not know about the desktop app (the sign-in page, an
older ARK server) still get the window buttons and a drag region from the
shell (`ark_studio/desktop/src-tauri/src/shell.js`).

## Tray

The tray icon (menu bar on macOS; GNOME needs the AppIndicator extension)
offers: Open ARK Studio, Servers (each saved
server, Connect to a server…), Open in browser, Start / Stop local server,
Settings…, Open data folder, Show server log, Quit ARK Studio, under a header
with the version and whether the local server runs. A left click opens the
window. On Windows a right click opens these entries in a small ARK-styled
window by the tray (`ark_studio/desktop/ui/tray.html`, placed by
`src-tauri/src/tray_popup.rs`; arrow keys, Enter and Escape work, clicking
elsewhere closes it); macOS and Linux use the native menu. Both follow the
system language (English or Chinese). Only one instance runs; starting it again brings the window
forward.

Closing the window keeps ARK Studio in the tray (the default; **Settings >
Window**). With that setting off, closing the window quits ARK Studio and
stops the local server.

## The local server

* The server listens on `127.0.0.1` only. Only the ARK Studio window can use
  it. Other users and programs on the computer get "This ARK server belongs to
  the ARK Studio desktop app"; agent CLIs started by ARK keep working with the
  tokens ARK gives them. **Open in browser** in the tray opens a one-time
  sign-in link.
* Data: the project folder you chose. The default is `projects` next to the
  app for the Windows installer and portable zip (keys, logs and settings in
  `data` beside it), else `<data dir>/projects` in the per-user data dir; an
  existing per-user folder keeps being used, and an app folder that is not
  writable falls back to the per-user dir with a notice
  ([locations per package](install.md#where-your-data-lives)).
* Quitting stops the server (gracefully, then forcibly after 10 s). If the
  server stops unexpectedly the window says so and offers **Try again** and
  the log.

## Servers

A server opens at its own address; you sign in on the server's own page.
Saved addresses are remembered (no passwords) in `desktop.json`. A
[desktop link](desktop-link.md) adds a revocable per-server device token
(`private/server-links.json`) so the local Home can list and create the
server's projects.

`file:///` links in notes refer to files on the server, but "Open on this
computer" in such a link's dialog opens the same path locally through the
desktop app: inside the folders under **Settings > Folders remote servers may
open** directly, elsewhere after asking. Programs and scripts are revealed in
the file manager, never started.

## What pages may call (desktop bridge)

The shell injects `window.__ARK_DESKTOP__` into every page of its windows;
what a page may actually call is decided by the app per origin (Tauri
capabilities, checked again inside each command from the calling page's
URL, never from arguments):

| Page | May call |
| --- | --- |
| Bundled pages (Settings, status page) | everything they need |
| The local ARK (`http://127.0.0.1:<port>`, the running sidecar) | window controls, the local-open bridge, the hub: saved servers (with link state, never tokens), their online state, open a saved server (Home, a project, its new-project dialog, the link prompt), open Settings, back to the local Home, a linked server's projects, create a project there, unlink |
| A remote ARK page | window controls, the local-open bridge, its own desktop context (`role: "remote"`, whether it is linked), back to the local Home (the switcher's **This computer**), and `linkServer(code)`: hand over a one-time link code of **its own** saved server |

Remote pages never see the saved servers, the local port, the device tokens
or anything else of the local side, and cannot add servers: only Settings
can. Calls to linked servers are made by the app (Rust), not by a page; see
[Desktop links](desktop-link.md). The web app's
`ark_studio/web/js/desktop-hub.js` uses the hub; in a browser it does
nothing.

## Development

See [Releasing ARK Studio](../development/release.md#building-locally) for
building the app and the sidecar.
