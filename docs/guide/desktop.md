# ARK Studio desktop app

The desktop app is a small native shell (Tauri 2, in `ark_studio/desktop/`) around the
ARK web UI. It has no Python and no bundled web UI of its own: its window
always shows a server's own pages.

* **Local mode** starts the bundled `ark-server` on `127.0.0.1` and opens it.
* **Remote mode** opens an ARK server's address, e.g. `https://ark.example.com`.

Download, installation, unsigned-build warnings, data locations and uninstall:
[Install ARK Studio](install.md).

## Windows and chrome

Both windows are borderless and drawn in ARK's style, light or dark like the
rest of ARK.

* **Connection window**: a compact card with a soft shadow and rounded corners
  (Windows/Linux), title bar with minimize and close. It offers local mode
  (project folder, port) and remote mode (address, recent servers), the
  "open automatically next time" option, and **Settings** (trusted folders,
  close-to-tray, data folder, server log, updates).
* **Main window**: ARK's top bar is the title bar. Drag its empty space to move
  the window, double-click to maximize; the minimize / maximize / close buttons
  sit at its right end. Edges and corners resize the window; Windows Snap
  works. On Windows 11 the window has rounded corners and the system shadow.
* **macOS**: the native traffic lights sit at the top left over ARK's top bar
  (the app keeps the native window frame with a hidden title bar), so
  fullscreen, zoom and window tiling behave like any Mac app.
* **Linux**: borderless with ARK's buttons; the shadow and rounded corners of
  the connection window need a compositing window manager.

The ARK web app recognizes the desktop app (`window.__ARK_DESKTOP__`) and
marks its top bar as the drag region (`ark_studio/web/js/desktop-shell.js`); browsers are
unaffected. Pages that do not know about the desktop app (the sign-in page, an
older ARK server) still get the window buttons and a drag region from the
shell (`ark_studio/desktop/src-tauri/src/shell.js`).

## Tray

The tray icon (menu bar on macOS; GNOME needs the AppIndicator extension)
offers: Open ARK Studio, Switch server…, Open in browser, Start / Stop local
server, Open data folder, Show server log, Settings…, Quit ARK Studio. A left
click opens the window. Only one instance runs; starting it again brings the
window forward.

Closing the window quits ARK Studio and stops the local server, unless
**Settings > Keep running in the tray when the window is closed** is on.

## Local mode

* The server listens on `127.0.0.1` only, on the chosen port (default
  `18890`); if that port is busy it takes a free one for this session (browser
  storage such as layout preferences is per port).
* Only the ARK Studio window can use it. Other users and programs on the
  computer get "This ARK server belongs to the ARK Studio desktop app"; agent
  CLIs started by ARK keep working with the tokens ARK gives them.
  **Open in browser** in the tray opens a one-time sign-in link.
* Data: the project folder you chose. The default is `projects` next to the
  app for the Windows installer and portable zip (keys, logs and settings in
  `data` beside it), else `<data dir>/projects` in the per-user data dir; an
  existing per-user folder keeps being used, and an app folder that is not
  writable falls back to the per-user dir with a notice
  ([locations per package](install.md#where-your-data-lives)).
* Quitting stops the server (gracefully, then forcibly after 10 s). If the
  server stops unexpectedly the window says so and offers **Try again** and
  the log.

## Remote mode

The window opens the server address directly; you sign in on the server's own
page. Recent addresses are remembered (no passwords). If the server does not
answer, the connection window says so; **Open anyway** opens it regardless.

`file:///` links in notes refer to files on the server, but "Open on this
computer" in such a link's dialog opens the same path locally through the
desktop app: inside the folders under **Settings > Folders remote servers may
open** directly, elsewhere after asking. Programs and scripts are revealed in
the file manager, never started.

## Development

See [Releasing ARK Studio](../development/release.md#building-locally) for
building the app and the sidecar.
