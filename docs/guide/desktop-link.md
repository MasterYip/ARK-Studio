# Desktop links: a server's projects in ARK Studio

ARK Studio (the [desktop app](desktop.md)) shows the projects of your ARK
servers next to your local ones and creates projects on a server from the
same list. For that it needs a **desktop link**: a revocable device token for
your account on that server, obtained once after you sign in there. No
password is ever stored by the app.

## Linking a server

1. Save the server in ARK Studio (**Settings > Servers > Connect to a
   server…**, or the tray's **Servers > Connect to a server…**).
2. Open it and sign in on its own page.
3. Choose **Link this desktop**: in the project switcher (**This computer >
   Link this desktop**), or from the local Home: the server's **Link this
   desktop** button in the switcher, or **Settings > Servers > Link…**, which
   opens the server with the prompt (`?ark_link_desktop=1`).
4. Confirm. The server's projects now appear under the server in the local
   Home's switcher and Projects dialog; click one to open it. **New or open
   project… > Location: <server>** creates the project there directly and
   opens it.

Unlinked servers still work as before: they open in the window, and a new
project on them opens the server's own new-project dialog.

## What the token may do

The token acts as the account that linked it and may only:

| Request | Purpose |
| --- | --- |
| `GET /api/desktop/projects` | the account's projects: `id`, `name`, `description`, `updated`, `permission` (no paths) and `can_create` |
| `POST /api/desktop/projects` `{name, description?, template?}` | create a project (the same rule as `POST /api/projects`: organization owner, admin or member); nothing is switched or opened |
| `GET` / `DELETE /api/mobile/session` | read or end its own session (unlink) |
| `GET /api/health` | reachability |

Everything else answers `403 DESKTOP_LINK_SCOPE`: no units, files, agent
tokens, settings, organization data or other projects' contents. The browser
session in the window is separate (cookie), as with any browser.

## Revoking

* **On the server:** the link is a row in the device list of the project you
  linked from (**Settings > Mobile > devices**, labelled "ARK Studio desktop
  link"); **Revoke** ends it at once. Changing the account's password or an
  admin's "sign out everywhere" ends it too, and it stops working while the
  account has no read access to that project.
* **In ARK Studio:** **Settings > Servers > Unlink**, or removing the server
  from the list, deletes the token and ends the session on the server
  (`DELETE /api/mobile/session`; if the server is unreachable, revoke it there
  later). A revoked or expired link shows as "The link expired or was
  revoked" with **Link this desktop** again.

## How it works

The flow reuses ARK's phone pairing (`internal/mobile`):

1. The signed-in page asks its server for a one-time **link code**:
   `POST /api/desktop/link` (browser session required; the trusted local owner
   on a server without accounts). The code is a pairing with source
   `desktop`, read-only on the current project, valid for 120 seconds, and
   its session lasts until revoked.
2. The page hands the code to ARK Studio (`window.__ARK_DESKTOP__.hub.linkServer`,
   allowed on saved servers' pages only, and only for the page's own
   server: the app derives the server from the calling page's address, never
   from the request).
3. ARK Studio claims it from Rust with the ordinary pairing claim
   (`POST /api/mobile/pair/claim {"code": ...}`) and keeps the returned device
   token (`X-ARK-Mobile-Token`).
4. The local ARK page asks the app for the server's projects or to create one
   (`serverProjects`, `createServerProject`); the app calls the server with
   the token and returns the results. The token never reaches a page.

Storage: `<data dir>/private/server-links.json` (one entry per server: token,
account, pairing id, time), written atomically with mode `0600` inside the
`0700` private folder on macOS and Linux; on Windows the per-user data
folder's permissions apply. See [where your data lives](install.md#where-your-data-lives).

Servers without this version answer `404` for `/api/desktop/projects`; ARK
Studio then shows "This server is too old to share its project list" and the
server still opens normally.

## For operators

* The routes are Go-only (`ark_studio/server/internal/server/routes_desktop.go`,
  scope in `ark_studio/server/internal/mobile/desktop.go`); they are listed by
  `ark-server routes` with the `goonly:` prefix and in
  `tests/python/contract/go_only.py`.
* Link codes are rate limited like pairing codes (30 per hour per client
  address); claims share the pairing claim limit (8 per minute).
* The server logs `desktop.link.create` (project activity and organization
  audit log) and `desktop.project.create`; the claim logs `mobile.pair.claim`.
* Rolling back to the frozen Python server: it does not know desktop links
  and would treat a linked desktop's token as a read-only phone of the
  project it was linked from. Revoke desktop links (device list) before a
  rollback.
