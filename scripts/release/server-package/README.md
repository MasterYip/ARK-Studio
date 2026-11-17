# ark-server

`ark-server` is the ARK Studio server: one self-contained binary with the web
UI embedded. It needs no Python, no runtime and no C libraries. Open the URL it
prints in any modern browser, or connect the ARK Studio desktop app
("Remote server") or the Android app to it.

Full documentation: <https://github.com/MasterYip/ARK-Studio/tree/main/docs>
(server operations: `docs/operations/server-operations.md`; running ark-server
in place of the Python server: `docs/development/go-port/switch-over.md`).

## Quick start (this computer only)

```sh
./ark-server serve --root ~/ark/projects            # Windows: ark-server.exe serve --root %USERPROFILE%\ark\projects
# open http://127.0.0.1:8890/
```

Without sign-in configured, ark-server binds loopback (127.0.0.1) only and
treats every local connection as the owner. Anyone who can log in to this
computer can reach it; use sign-in on shared machines.

## Server use (sign-in, reverse proxy, systemd)

1. Install the binary: `install -m 0755 ark-server ~/.local/bin/ark-server`
   (or install the `.deb`, which puts it in `/usr/bin`).
2. Create an initial owner password file, readable only by you:
   `umask 077; printf '%s' 'a long random password' > ~/.config/arkstudio/admin.password`
3. Copy `server.example.json` to `~/.config/arkstudio/main.json` and edit
   `root`, `public_url`, `auth_user` and `auth_password_file`. Keep `host` on
   `127.0.0.1` and put an HTTPS reverse proxy (Caddy, nginx) in front;
   `trust_proxy` then takes the client address from the proxy.
4. Install and start the systemd user unit (the comments at the top of
   `ark-server@.service` list the commands):
   `systemctl --user enable --now ark-server@main.service`
5. Check: `curl -s http://127.0.0.1:8890/api/health` and
   `journalctl --user -u ark-server@main -n 50`.

Every option of `ark-server serve --help` can also be given as a server JSON
config key or an `ARK_*` environment variable. `ark-server version` prints the
version; `ark-server --help` lists the other subcommands (account management,
backups, skills, ...).

## Optional tools

ARK uses these when they are on `PATH` and degrades clearly when they are not:
`git` (node history and project history), `ssh` (GPU servers, tunnels),
`rg` (faster file search), and the agent CLIs you want to run from ARK
(Claude Code, Codex, ...).

## Verify the download

Compare the archive with the `SHA256SUMS` file published with the release:
`sha256sum -c SHA256SUMS --ignore-missing` (macOS: `shasum -a 256 -c`).

License: AGPL-3.0 (see `LICENSE`). Commercial licences: raymon-yip@qq.com.
