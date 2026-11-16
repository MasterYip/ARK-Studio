# ARK on a phone: web app / PWA, public mode and QR pairing

ARK remains a loopback-first desktop workbench. This guide covers the optional
phone client: the same responsive web app opened on an Android phone through a
LAN address, an SSH tunnel, or a deliberately public HTTPS deployment, paired by
scanning a QR code in **Settings → Project → Access → Mobile / Pair phone**, and the native
Android app in `android/` (see [Android app](#android-app)). There is no external
QR service, no telemetry and no phone-side SSH client.

A pairing QR is bound to the open project and to the account that created it,
so it lives under the project's **Access** tab next to the remote-access (SSH
tunnel) panel. The server connection facts, the pairing list and the Android
downloads are folded behind one-line summaries; open them from the summary row.
Old `?settings=mobile` and `?settings=remote` links open the Access tab.

## What works

- Installable PWA shell: manifest, maskable icons, `theme-color`, standalone
  display, safe-area insets and a bottom tab bar.
- Mobile layouts for Overview, Ideas, Map, Tasks/session workspace, Settings and
  the Files workspace (tree, editor, git review, terminal).
- A pairing QR generated locally by `arkstudio.qr` (byte-mode QR, versions
  1–10 at ECC level M). No CDN or third-party QR library is loaded.
- One-time pairing codes with project and permission scope, expiries,
  rate-limited claims, revocation and a mobile session token stored in the
  phone's `localStorage`.
- Read-only by default; a full-access code can be generated explicitly.
- Optional account/password sign-in for public deployments: an ARK-style login
  page, signed HttpOnly `SameSite=Lax` session cookie, CSRF-protected form,
  per-IP/per-user login rate limits and server-side session expiry.
- **Automatic QR generation** when ARK is publicly reachable: Settings derives
  the base URL from `--public-url`, the request Host, an explicit public IP or a
  detected LAN address. No host/port entry is needed in public/LAN mode.

## Authentication and exposure modes

There are three deliberately different modes. The server never turns a public
bind into an unauthenticated one.

| Mode | Bind | Sign-in | Pairing QR base |
| --- | --- | --- | --- |
| Desktop / loopback (default) | `127.0.0.1` | not configured | none; SSH tunnel fields |
| LAN / public bind | `0.0.0.0` via `--expose` or `--host` | **required** | request Host or detected LAN IP |
| Reverse proxy / HTTPS | loopback + `--public-url` | strongly recommended | configured public URL |

### Refusing accidental public exposure

A non-loopback bind without authentication is rejected before the socket is
opened:

```bash
$ python -m arkstudio serve --expose
error: refusing to bind ARK to 0.0.0.0 without account authentication. Set
--auth-user and ARK_AUTH_PASSWORD (or --auth-password-hash / --auth-password-file),
or leave the default 127.0.0.1 bind. ARK never exposes itself publicly without a
sign-in.
```

`--expose` is equivalent to setting `ARK_PUBLIC=1`; it binds `0.0.0.0` unless
`--host` is given explicitly. `--host 0.0.0.0` has the same authentication
requirement even when `--expose` is omitted.

### Configure the account

For a persistent deployment, use native `server-init` and `serve --config` as
described in [server operations](../operations/server-operations.md). Account passwords then
survive restart without bootstrap environment variables. The environment flags
below remain available for compatibility and temporary instances; keep their
values out of argv, shell history and tracked files.

```bash
export ARK_AUTH_USER=ark
export ARK_AUTH_PASSWORD='<initial-password>'
python -m arkstudio serve --expose --port 8890
```

Equivalent environment configuration:

| Variable | Meaning |
| --- | --- |
| `ARK_PUBLIC` | `1`/`true` enables the public bind (like `--expose`) |
| `ARK_PUBLIC_URL` | advertised base URL, e.g. `https://ark.example.net/ark/` |
| `ARK_AUTH_USER` | sign-in username |
| `ARK_AUTH_PASSWORD` | plaintext password read once and hashed in memory |
| `ARK_AUTH_PASSWORD_FILE` | file containing the password (Docker/secret mounts) |
| `ARK_AUTH_PASSWORD_HASH` | pre-computed `pbkdf2_sha256$…` hash; no plaintext at all |
| `ARK_AUTH_SESSION_TTL` | sign-in session seconds (default 43200 = 12 h, max 7 days) |
| `ARK_PUBLIC_IP` | explicit public IP for QR URLs (no lookup) |
| `ARK_DETECT_PUBLIC_IP` | `1` opts into a short public-IP lookup |
| `ARK_TRUST_PROXY` | `1` trusts `X-Forwarded-For/Proto/Prefix` from your proxy |
| `ARK_ALLOWED_HOSTS` | extra Host-header allow-list entries |

The equivalent flags are `--auth-user`, `--auth-password`,
`--auth-password-file`, `--auth-password-hash`, `--auth-session-ttl`,
`--public-url`, `--public-ip`, `--detect-public-ip` and `--trust-proxy`.

`--auth-password` works but is visible to other processes through the process
list. Prefer `ARK_AUTH_PASSWORD`, a password file, or a hash. ARK never writes
the plaintext password, the password hash, the session cookie or the agent token
to a URL, a log line or a QR payload. Only the username and the fact that
sign-in is required are reported.

Generate a hash without putting the password in the process list:

```bash
python - <<'PY'
from getpass import getpass
from arkstudio.auth import hash_password
print(hash_password(getpass("ARK password: ")))
PY
# then:
export ARK_AUTH_USER=ark
export ARK_AUTH_PASSWORD_HASH='<pbkdf2-password-hash>'
python -m arkstudio serve --expose --public-url https://ark.example.net/
```

### What the login gate covers

When an account is configured, a valid signed cookie is required for:

- the app shell (`/`), all static assets and the service worker;
- every `/api/*` endpoint, including the agent API and pairing management;
- `/login` assets remain public so the page can render; `/api/health`,
  `/api/auth/session`, `/api/mobile/session` and the one-time
  `/api/mobile/pair/claim` have narrow exceptions.

A phone scanning a QR code in public mode opens the login page with `#pair=…`.
`js/login.js` exchanges that one-time code for a project-scoped paired-device
session (permission and project from the QR); the password is not requested
again. The claim is single-use, rate-limited, same-origin and bound to a QR
created by the signed-in desktop. It does **not** mint a desktop account
session, so read-only QR codes stay read-only and revoking the pairing ends the
device session immediately.
Loopback desktop browsers still sign in normally when authentication is
configured; pairing management stays desktop-loopback-only.

The cookie is `HttpOnly`, `SameSite=Lax`, `Path=/` (or the public URL's path
prefix), host-only, and gains `Secure` when the request/public URL uses HTTPS.
Sessions live in memory, are signed with a per-process secret, expire, and are
revoked by logout or restart. The login form also carries a short-lived signed
CSRF token; writes additionally require a same-origin `Origin`/`Sec-Fetch-Site`
context. Login failures are limited per client IP and per username (generic
error message, `429` with `Retry-After` after the limit).

## Start the desktop server

```bash
python -m arkstudio serve --root runtime/projects --port 8890
```

This remains loopback-only. Optional reverse-proxy mode:

```bash
ARK_AUTH_USER=ark ARK_AUTH_PASSWORD='…' \
  python -m arkstudio serve --root runtime/projects --port 8890 \
  --public-url https://ark.example.net/ark/
```

Optional direct public bind (still put HTTPS in front for real internet use):

```bash
export ARK_AUTH_USER=ark
export ARK_AUTH_PASSWORD='…'
python -m arkstudio serve --root runtime/projects --port 8890 --expose \
  --public-url https://ark.example.net/
```

`ARK_PUBLIC_URL` is equivalent to `--public-url`. The CLI flag wins over the
environment variable; both are normalised to an `http(s)` base ending in `/`.

### Persistent Server Configuration

Initialize accounts with `python3 -m arkstudio server-init`, then start with
`python3 -m arkstudio serve --config ~/.config/arkstudio/main.json`. This native
CLI stores the owner password hash in the organization database and restores
authentication independently of the invoking terminal's environment. JSON
configuration requires sign-in by default. No custom startup shell script or
plaintext password in the server environment is needed.

See [server operations](../operations/server-operations.md) for the complete clone, initialize,
systemd, restart, backup and restore workflow. Put the HTTPS proxy address in
the JSON `public_url` setting. Quick Tunnel URLs can change and must be updated
there before restarting ARK; a named tunnel or owned domain gives a stable URL.

## Advertised URL selection (honest states)

The QR payload is built from the first available source, in this order:

1. **Configured public URL** — `ARK_PUBLIC_URL` / `--public-url`. Best option:
   it can include a path prefix and HTTPS, so the PWA is installable and the QR
   is stable across DHCP changes.
2. **Explicit public IP** — `ARK_PUBLIC_IP` / `--public-ip`, combined with the
   server port.
3. **Request Host** — the non-loopback `Host` the desktop browser used (or that
   a reverse proxy forwarded). Useful when ARK binds a public interface but no
   public URL is configured.
4. **Detected LAN IPv4** — when the server binds a non-loopback interface, ARK
   enumerates private addresses and offers `http://<lan-ip>:<port>/`.
5. **Opt-in public-IP lookup** — only with `--detect-public-ip` /
   `ARK_DETECT_PUBLIC_IP=1`. One short request to a public IP echo service,
   cached and never run at startup. Detection can fail offline; that is not a
   server error.
6. **Loopback-only** — the default bind (`127.0.0.1`). No phone can reach
   `127.0.0.1` on another device. Settings shows the exact SSH command to run
   on a phone-reachable computer and the manual tunnel fields. The phone cannot
   run SSH by itself.

Selected addresses are reported as **detected**, not guaranteed. NAT, port
forwarding, carrier-grade NAT and firewall rules are outside ARK's knowledge.
Settings and `GET /api/mobile/connection` say so explicitly; test the link from
the phone before relying on it.

Public/LAN mode automatically creates a read-only QR when Settings → Project → Access
opens (`POST /api/mobile/pair` with an empty JSON object does the same through
the API). Loopback mode keeps the manual tunnel host/port fields because only
the user knows which laptop/desktop can reach the phone.

## Automatic pairing in public/LAN mode

1. The desktop opens **Settings → Project → Access** after signing in.
2. The server selects the base URL using the order above and returns a locally
   rendered QR `<base>/#pair=<code>`.
3. The QR is shown immediately; **Regenerate pairing QR** creates a fresh code.
   No host/port fields are displayed in public/LAN mode.
4. Scanning the QR opens ARK. With authentication enabled `js/login.js` claims
   `#pair=` immediately, stores the project-scoped mobile token, receives its
   HttpOnly device cookie, and opens the app without a password prompt. The
   one-time code is removed from the redirect target so it cannot be replayed.
5. Pairing creation and revocation are accepted only from the desktop loopback
   window, not from a paired phone or a public browser.

If no base URL can be derived (for example an unusual network with no detected
interface and no request Host), Settings says so and asks for
`--public-url`/`ARK_PUBLIC_URL`; it does not fabricate a reachable address.

## SSH LAN tunnel (loopback ARK)

Run ARK on the server, loopback-bound (the default):

```bash
# on gpu-server-1 or wherever ARK runs
python -m arkstudio serve --port 8890
```

On a **phone-reachable computer** (a laptop/desktop on the same Wi-Fi as the
phone), open an SSH local forward that binds the LAN interface:

```bash
ssh -N -L 0.0.0.0:8891:127.0.0.1:8890 user@gpu-server-1
```

or use the helper, which prints the command, URL and warnings:

```bash
python -m arkstudio mobile-tunnel \
  --ssh user@gpu-server-1 \
  --ark-port 8890 \
  --local-port 8891 \
  --bind 0.0.0.0
```

Notes:

- `0.0.0.0` binds the laptop's LAN interface; use a concrete laptop LAN IP if
  you want to limit exposure.
- The phone connects to `http://<laptop-lan-ip>:8891/`. Find the address with
  `ipconfig` (Windows), `ip addr` (Linux) or `ifconfig` (macOS).
- In ARK Settings → Project → Access, ARK is in loopback mode: copy the exact command,
  enter the laptop's address and port (for example `192.168.1.20` / `8891`),
  choose read-only or full access, then press **Generate pairing QR** and scan
  it. Automatic generation is impossible here: the phone cannot run SSH, and
  the server cannot know which laptop is phone-reachable.
- Keep the SSH session running while the phone is connected. Stop it with
  `Ctrl+C`; the tunnel helper prints how.
- SSH host-key prompts and authentication belong to your normal SSH setup. ARK
  never stores SSH credentials and never puts a private key in a QR code.

### Firewall

Opening a port on a LAN interface is a deliberate exposure. On the tunnel
laptop:

- allow inbound TCP `8891` only from the private/phone subnet, never from the
  public internet;
- mark the network as private;
- stop the tunnel when you are done.

Windows example (run as administrator, adjust the subnet):

```powershell
New-NetFirewallRule -DisplayName "ARK phone tunnel" -Direction Inbound `
  -Protocol TCP -LocalPort 8891 -RemoteAddress 192.168.1.0/24 -Action Allow
Remove-NetFirewallRule -DisplayName "ARK phone tunnel"   # when finished
```

Linux example with `ufw`:

```bash
sudo ufw allow from 192.168.1.0/24 to any port 8891 proto tcp
sudo ufw delete allow from 192.168.1.0/24 to any port 8891 proto tcp
```

Do not port-forward this port on your router and do not expose it to the
internet. For remote access use a VPN, an SSH tunnel, or an authenticated HTTPS
reverse proxy.

## HTTPS, reverse proxies and the PWA caveat

Service workers, installation and offline caching require a **secure context**:
HTTPS, or `http://localhost`/`http://127.0.0.1`. A phone opening
`http://192.168.1.20:8891/` is **not** in a secure context. The app loads and
pairing works, but Chrome will not register the service worker or offer full
installation; "Add to Home screen" may only create a shortcut. ARK reports this
honestly in Settings and in the pairing result. It does not pretend the PWA is
installable when it is not.

For a real installable PWA, terminate TLS in front of ARK:

### Caddy

```caddyfile
ark.example.net {
    handle_path /ark/* {
        reverse_proxy 127.0.0.1:8890
    }
}
```

### nginx

```nginx
location /ark/ {
    proxy_pass http://127.0.0.1:8890/;   # trailing slash strips /ark/
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Prefix /ark;
    proxy_buffering off;                 # NDJSON chat and SSE terminal streams
    proxy_read_timeout 600s;
    client_max_body_size 24m;
}
```

Then advertise the public URL and account:

```bash
ARK_AUTH_USER=ark ARK_AUTH_PASSWORD='…' \
ARK_PUBLIC_URL=https://ark.example.net/ark/ \
ARK_TRUST_PROXY=1 \
python -m arkstudio serve --port 8890
```

`ARK_TRUST_PROXY=1` is only for a proxy you control: it makes rate limiting and
redirects use the forwarded client/prefix instead of the proxy's loopback
socket. Without it, leave it unset and configure `--public-url` explicitly.

The UI uses relative module/asset URLs and an API base derived from the page, so
a path prefix works when the proxy strips it (as above). The advertised URL and
the proxy must agree; if the proxy serves ARK under `/ark/` without stripping the
prefix, the stdlib server will not find the routes. The login cookie path is
derived from the public URL path, so a prefix does not lose the session.

If the proxy forwards WebSocket/SSE/NDJSON, keep buffering off. HTTPS also makes
the QR payload `https://…`, which is what enables installation, offline shell
caching and the standalone window.

## Login and session API

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/login?next=…` | public | ARK-styled sign-in form with signed CSRF token |
| `POST` | `/login` | CSRF + same origin | verify username/password, set the session cookie |
| `POST` | `/api/mobile/pair/claim` | one-time code + same origin | exchange a desktop-issued QR for a project-scoped device session |
| `POST` | `/api/mobile/login` | username + password (JSON) | native clients: exchange the account password for a device token |
| `POST` | `/logout` | session | clear the cookie and return to login |
| `GET` | `/api/auth/session` | public | `auth_required`, `authenticated`, user, expiry |
| `POST` | `/api/auth/logout` | session | JSON logout used by Settings |

The login form posts `application/x-www-form-urlencoded` fields `username`,
`password`, `csrf` and `next`. `next` must be a local path (and may include a
fragment such as `#pair=…`); scheme-relative and external URLs are rejected.
Wrong credentials return `401` and the same generic message for unknown users.
Repeated failures return `429 AUTH_RATE_LIMITED` with `Retry-After`.

The cookie is named `ark_session` (per-process signing key, in-memory session
table). Restarting ARK signs every browser out. There is no "remember me"
persistence outside the cookie's `Max-Age`.

## Pairing and authorization model

### Create

Desktop Settings → **Mobile / Pair phone** → permission and lifetime → QR. In
public/LAN mode this happens automatically on panel open. The API:

```http
POST /api/mobile/pair
Content-Type: application/json

{"permission": "read_only", "expires_in": 600}
```

In loopback mode only, the desktop may also send `tunnel_host` / `tunnel_port`
for the validated tunnel computer. The response contains the one-time `code`,
`pairing_url`, `qr_svg`, `base_source`, `auto` and `requires_login`. The code is
never written to the activity log. The QR payload is `<base>/#pair=<code>`; the
fragment is not sent to the server in HTTP requests (it is preserved through the
login redirect by the login page's small script).

Pairing lifetimes are 5, 10 or 15 minutes (30–3600 seconds accepted by the API).
Pairing creation and revocation are accepted only from the desktop loopback
window, not from a paired phone.

### Claim

The phone opens the QR URL. The login page exchanges the one-time `#pair=` code
for a scoped device session without asking for the password, then the app boots:

```http
POST /api/mobile/pair/claim
Content-Type: application/json

{"token": "arkp_…"}
```

On success the server marks the pairing code used, issues a separate mobile
session token (`arkm_…`, 12 hours by default), sets it as the HttpOnly
`ark_mobile` cookie and returns it once. Replaying the pairing code fails with
`PAIRING_USED`; expired codes return `PAIRING_EXPIRED`; a code for another
project returns `MOBILE_WRONG_PROJECT`.

The claim deliberately never creates a desktop `ark_session`: a QR is a
project-scoped paired-device credential, so it cannot bypass the read-only
scope and survives revocation correctly. `/api/auth/session` keeps reporting
`authenticated: false` for a device cookie.

In multi-user mode every pairing record is bound to the account that created
it. Claiming a code can only produce a session for that same user; a
signed-in different account gets `403 PAIRING_WRONG_USER`, a code whose owner
was deleted or lost access is rejected, and a mobile session can never exceed
that user's project permission (a `read_only` pairing stays read-only, and the
device is pinned to the project it was paired for).

The phone stores only the mobile session token in `localStorage`
(`ark.mobile.token`, plus permission/expiry hints) and strips `#pair=` from the
URL. Send it on every API request:

```http
X-ARK-Mobile-Token: arkm_…
```

For browser-owned resources that cannot set a header (`<img>`, `<video>`,
download links, export links), use the query form:

```text
/api/media/3/raw?mobile_token=arkm_…
```

Prefer the header. Query tokens can appear in reverse-proxy access logs, browser
history and copied links; use HTTPS and a trusted proxy if you use them. The
login redirect deliberately strips `mobile_token` from `next` so a token never
ends up in the login URL or Referer.

### Scopes and enforcement

- The pairing record binds the current project id and the chosen permission.
  Switching the desktop project makes the old phone fail with
  `MOBILE_WRONG_PROJECT`.
- `read_only` allows `GET`/`HEAD`; any `POST`, `PATCH` or `DELETE` from that token
  returns `403 MOBILE_READ_ONLY`.
- `full` allows writes, but never exposes the desktop agent token
  (`/api/agent/token` → `403 MOBILE_SECRET_BLOCKED`) and never lets the phone
  open host files/apps (`/api/open` → `403 MOBILE_HOST_ACTION_BLOCKED`).
- Revoking a pairing immediately invalidates every mobile session token issued
  from it. Restarting ARK forgets all pairings and sessions (in-memory store).
- Failed claims are rate-limited (8 per minute per client IP by default) and
  return `429 RATE_LIMITED`.
- Revoked/expired/invalid tokens return `401` with `MOBILE_TOKEN_*` codes.
- With an account configured, every phone API call needs **both** the account
  cookie and a valid mobile token when it is not a desktop-loopback request.
  A stolen mobile token alone is not enough to cross the login gate.

### Network gate and Host header

ARK treats a request as desktop-local only when **both** the socket peer and the
`Host` header are loopback. A phone arriving through the SSH forward has a
loopback socket peer but a non-loopback `Host`; a direct LAN or public client is
non-loopback too. Such requests must present a valid mobile token for all
`/api/*` calls (except health and the narrowly redacted status/claim paths) and
cannot manage pairings.

Loopback `Host` values and explicitly advertised hosts (`ARK_PUBLIC_URL`,
`ARK_ALLOWED_HOSTS`, or a generated QR host) are accepted. With a configured
public URL the Host allow-list becomes strict, so a DNS-rebinding page cannot
reuse the cookie through an unexpected Host; add real aliases through
`ARK_ALLOWED_HOSTS`. A bare `--expose` without a public URL accepts the request
Host (the account cookie, not the Host name, is the authentication boundary) and
prints a recommendation to set a public URL. There is no CORS wildcard. Writes
require a same-origin `Origin` (or a same-origin `Sec-Fetch-Site`) and login
forms additionally require the signed CSRF token plus `SameSite=Lax`.

## Why a private SSH key must never be put in a QR code

It is technically easy to put text in a QR code and tempting to encode a private
key so a phone could connect "without setup". Do not do it:

- **The QR is a public broadcast.** Anyone who sees, photographs or screenshots
  the code can read the payload; QR images are commonly kept in camera apps,
  chat previews and scanners. There is no confidentiality and no way to revoke a
  photographed key.
- **A QR has no authentication or origin.** A malicious poster can replace it;
  the scanner cannot verify that ARK produced it.
- **The key is a long-lived credential,** usually without an expiry and with
  server-wide access — far more dangerous than the one-time, project-scoped
  `arkp_` pairing code used here.
- **It would not even solve the phone problem.** The APK/mobile web app cannot
  open an SSH tunnel or run an SSH client by itself; it has no `ssh` binary,
  key agent or forwarding setup. A key in the QR would only leak the credential,
  not create a tunnel.
- **There is a supported alternative:** public HTTPS + account sign-in (the QR
  then contains only a short-lived pairing code), or an SSH tunnel run on a
  phone-reachable laptop/desktop (the phone connects to the laptop's LAN URL).

ARK therefore never asks for, stores, logs or encodes `id_rsa`, `id_ed25519` or
any other private key. SSH authentication stays in your normal SSH client.

## Public-mode security checklist

- Terminate TLS in front of ARK (Caddy/nginx/VPN). The stdlib server has no TLS.
- Use a long, unique password and supply it through `ARK_AUTH_PASSWORD`,
  `ARK_AUTH_PASSWORD_FILE` or `ARK_AUTH_PASSWORD_HASH`, not `--auth-password`.
- Prefer `--public-url` so the QR host is stable and the Host allow-list is
  strict; add aliases with `ARK_ALLOWED_HOSTS`.
- Restrict the listening port with a firewall or bind loopback behind the proxy.
- Keep pairing codes read-only unless writes are needed; revoke old sessions.
- Treat `?mobile_token=` URLs as sensitive; prefer the header over plain HTTP.
- Sign out explicitly on shared browsers; sessions expire (default 12 hours).
- Back up and protect `runtime/projects/.agent-token`; it is not a login password
  and is never exposed to phones.

## API summary

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/health` | public | liveness; no project root when auth is on |
| `GET` | `/api/auth/session` | public | account session state |
| `GET` | `/login` | public | login page |
| `POST` | `/login` | CSRF, same origin | sign in and set cookie |
| `POST` | `/logout` | session | clear cookie, redirect to login |
| `GET` | `/api/mobile/connection` | desktop loopback + account | bind mode, detected IPs, advertised URLs, auth requirement, exact SSH command |
| `POST` | `/api/mobile/pair` | desktop loopback + account | auto-select base, create one-time code + QR SVG |
| `GET` | `/api/mobile/pairs` | desktop loopback + account | list pairings (never tokens) |
| `POST` | `/api/mobile/pairs/{id}/revoke` | desktop loopback + account | revoke pairing and phone sessions |
| `GET` | `/api/mobile/session` | optional | redacted pre-login; full state with a session |
| `POST` | `/api/mobile/pair/claim` | pairing code + account | exchange a one-time code for a mobile token |
| `DELETE` | `/api/mobile/session` | mobile token | log this phone out |
| `POST` | `/api/mobile/login` | username + password | native sign-in; returns a device token, sets no cookie |
| `GET` | `/api/mobile/session/projects` | mobile token | projects this device may switch to |
| `POST` | `/api/mobile/session/project` | mobile token (password sign-in only) | move the device session to another granted project |

`GET /api/mobile/session` is safe to call before pairing: it returns whether
pairing (and, in account mode, sign-in) is required, but no project name or
secret until the caller is authenticated.

## PWA implementation

- `web/manifest.webmanifest` — relative `start_url`/`scope`, standalone display,
  192/512 px icons plus a maskable icon, theme colour.
- `web/sw.js` — installs an explicit shell cache (HTML, CSS, modules, icons,
  KaTeX). API requests are never intercepted or cached. Navigation is
  network-first with a cached shell fallback; static assets are
  stale-while-revalidate. Bump `CACHE_VERSION` when changing the shell.
- `web/js/mobile-core.js` — DOM-free nav/QR/PWA helpers with Node tests.
- `web/js/mobile-ui.js` — service worker registration, install prompt, pairing
  gate and PWA status.
- `web/login.html`, `web/css/login.css`, `web/js/login.js` — the server-rendered
  login surface, styled to match ARK, with the hash-preserving helper.
- `web/css/mobile.css` — phone-only layout (≤760 px): bottom tabs, safe-area
  insets, off-canvas session drawers, settings nav, Files/terminal adjustments.
  Desktop styles remain in `app.css`.

Reload once after the first service-worker install to be controlled by it. The
cache stores no project data; offline mode only guarantees the app skeleton.

## Known limitations

- **No phone SSH.** The phone browser cannot open an SSH tunnel. Someone must
  run the tunnel on a reachable computer, or ARK must bind a LAN interface or sit
  behind HTTPS. This is deliberate, not a missing button.
- **No TLS in the stdlib server.** Use Caddy/nginx/a VPN. Plain HTTP public mode
  exposes the password and session cookie to the network; ARK warns about this
  at startup and on the login page but cannot create a certificate for you.
- **LAN HTTP is not installable.** Service workers require HTTPS or localhost.
  The app still works over HTTP; only install/offline are unavailable.
- **Detected addresses are not connectivity guarantees.** NAT, port forwarding
  and firewall configuration are outside ARK. The opt-in public-IP lookup only
  reports what an echo service sees; it does not test the ARK port.
- **No native features in the PWA.** The web app/PWA has no push, background
  sync, in-app scanner or share target. The native [Android app](#android-app)
  adds an in-app QR scanner, share-to-ARK and opt-in polling notifications.
- **Terminal is a convenience.** The Files terminal streams output and accepts
  stdin, but a phone soft keyboard is not a full shell experience; ANSI-heavy
  TUIs may be awkward. PTY resize works when the optional backend is available.
- **Session data is line-buffered.** The session workspace renders complete
  messages/tool cards; it is not token-level streaming. This matches desktop.
- **One project per server.** A QR is scoped to the project open on the desktop
  when it was generated. Switching projects invalidates the phone until you
  generate a new code.
- **Browser sign-in is in memory.** Restarting ARK signs browsers out
  (`ark_session`). Claimed device sessions (QR or app sign-in) are persisted as
  hashes in `.mobile-sessions.json` and survive restarts until revoked.
- **DHCP/mDNS changes.** If a detected/entered host changes, regenerate the QR;
  the old URL will not resolve. Configure `--public-url` for stability.
- **Proxy path support requires stripping.** Relative assets/API work under a
  path prefix, but the proxy must remove that prefix before forwarding to ARK.
  Set `ARK_PUBLIC_URL` (and `ARK_TRUST_PROXY` only for a proxy you control).
- **Phones cannot use the GPU queue.** `/api/gpu/jobs*` stays off the
  paired-device allow-list, so the Android GPU screen is read-only (cancel from
  the desktop or a task agent).

## Android app

`android/` builds two apps that install side by side, both downloadable from
Settings → Project → Access:

- **ARK Studio** (`:app`, `com.masteryip.arkstudio`, 3.0): the native app below,
  served at `/download/ARK-Studio-android.apk`.
- **ARK Studio Web** (`:webapp`, `com.masteryip.arkstudio.web`, 3.0-web): the
  lightweight shell that opens the full web app in a WebView and pairs inside the
  page, served at `/download/ARK-Studio-webapp.apk`.

`GET /api/mobile/apps` reports which builds are present on the server.

The native app is described next.
It is a hybrid: everyday screens are native, the rest is the web app in a
same-origin WebView signed in with the same device session.

| Native | WebView (deep-linked hash routes) |
| --- | --- |
| Servers + sign-in, Home, Tasks board + task detail, Runs + run thread, GPU, node viewer + media, share-to-ARK, More | Map `#/map`, Editor `#/editor/unit%3A<uid>`, Research/Plan `#/research/…`, Output `#/output/assets`, Assistant, Files, Settings |

### Sign-in

Both paths end with the same kind of credential: a device token (`arkm_…`)
that is persisted on the server as a hash, listed in Settings → Project → Access, revocable
there, limited by the paired-device allow-list and capped at the account's
project permission.

1. **Scan QR.** The app parses `<base>/#pair=<code>`, calls
   `POST <base>/api/mobile/pair/claim {"token": code}` once and keeps the
   returned `mobile_token`. The code is never stored; a replay fails with
   `PAIRING_USED`. QR devices stay on the project the code was made for.
2. **Server URL + account + password.** The app calls
   `POST <base>/api/mobile/login` with JSON
   `{"username", "password", "permission": "full"|"read_only", "device"}`.
   The server checks the password with the same per-IP/per-user limits as the
   login form (`401 AUTH_INVALID`, `429 AUTH_RATE_LIMITED` + `Retry-After`),
   then creates and immediately claims a pairing bound to that account
   (`source: "password"`). The response is the claim payload plus `projects`.
   The password is sent once and never stored. Servers without accounts return
   `400 AUTH_NOT_CONFIGURED` (use the QR). Form-encoded posts get `415`, and a
   foreign `Origin` gets `403`.

Why not reuse the `ark_session` cookie: it is in memory (a restart signs every
client out), lasts 12 h by default, and isn't listed or revocable per device.
A native app would then need the password again, which it must not store.
Password-signed devices may switch projects
(`GET /api/mobile/session/projects`, `POST /api/mobile/session/project`); QR
devices return `MOBILE_PROJECT_PINNED`.

### Session storage and the WebView

- The token is sealed with AES-256-GCM under a non-exportable AndroidKeyStore
  key and kept in private prefs (`allowBackup=false`); only account/project
  metadata is in clear. Every native request sends `X-ARK-Mobile-Token`; the
  token never appears in a URL.
- The WebView gets the token as the HttpOnly `ark_mobile` cookie for that origin
  only, so page JavaScript cannot read it. Navigation is limited to the server's
  origin; other links and redirects open in the system browser. Third-party
  cookies, file access and mixed content are off. The only JS interface reports
  the page theme and carries no credentials.
- Sign-out calls `DELETE /api/mobile/session` and then forgets the token. If the
  server is unreachable, the token is forgotten locally and the app tells you to
  revoke the device in Settings → Project → Access.
- http is accepted for LAN, tunnel and emulator hosts (the app shows an
  `HTTP · LAN` chip); an http server outside private ranges is marked
  `UNENCRYPTED`, and the app asks before sending a password to it.

### Notifications and sharing

- Opt-in in More → Notifications: a WorkManager periodic job (15 min, network
  required, no FCM) polls `/api/agent-sessions`,
  `/api/units?type=task&status=review` and `/api/gpu/dashboard`, and notifies
  only on transitions (run finished/failed, task moved to review, your GPU job
  finished). The first poll is a silent baseline.
- Share text, a link or an image from any app to **Save to ARK**. It creates an
  idea or note (`POST /api/units`) and uploads an image with
  `POST /api/media/upload` (12 MB; jpg/png/gif/webp/svg).

## Verification

```bash
python -m unittest tests.python.auth.test_auth tests.python.mobile.test_qr tests.python.mobile.test_mobile
node --test tests/js/mobile.test.mjs
```

Pairing and auth tests are stdlib-only and never contact an external network
service; public-IP detection is mocked in tests.
