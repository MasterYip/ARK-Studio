# Server Deployment and Operations

The server is one binary, `ark-server`: the release package (`.tar.gz`, `.zip`
or `.deb`, see `scripts/release/server-package/README.md`), or built from a
checkout with Go 1.27+. Deployment uses its CLI and an optional standard
systemd unit. Instance data and credentials belong outside the checkout.

## Initialize Once

```sh
# from a checkout: build and install the binary
git clone https://github.com/MasterYip/ARK-Studio.git ~/ARK-Studio
cd ~/ARK-Studio/ark_studio/server
go generate ./... && go build -o ~/.local/bin/ark-server ./cmd/ark-server

ark-server server-init \
  --config ~/.config/arkstudio/main.json \
  --data-dir ~/.local/share/arkstudio/main \
  --owner admin
```

Enter the initial password at the hidden prompts. Unattended setup may use
`--password-file /private/initial-password`; remove that file after initialization.
Passwords never need to appear in command arguments or an interactive shell's
environment. Initialization refuses to overwrite an existing configuration or
nonempty data directory.

`server-init` creates the owner in the organization database and writes a private
JSON configuration containing paths and settings, with no plaintext password.
The organization database stores salted PBKDF2-SHA256 password hashes. Later
accounts, project grants, password changes and personal agent tokens are managed
through Settings. Password changes remain authoritative after restart.

```sh
ark-server serve --config ~/.config/arkstudio/main.json
```

Paths inside JSON resolve relative to the configuration file. Explicit CLI
options override JSON. Configured deployments require authentication by default;
`--require-auth` is also available directly on `serve`. Existing authenticated
organizations restore account authentication from their database even without
bootstrap environment variables. An unreadable organization database, or missing
accounts alongside existing sign-in state, cannot silently enable anonymous mode.

For an existing installation, write a JSON file pointing to its actual paths:

```json
{
  "root": "/srv/ark/data/projects",
  "org_db": "/srv/ark/data/org.sqlite3",
  "private_dir": "/srv/ark/data/private",
  "sessions_root": "/srv/ark/data/agent-sessions",
  "gpu_servers_config": "/srv/ark/data/private/gpu_servers.yaml",
  "gpu_queue_db": "/srv/ark/data/gpu-queue.sqlite3",
  "blackbox_dir": "/srv/ark/data/blackbox",
  "require_auth": true,
  "host": "127.0.0.1",
  "port": 8890,
  "no_demo": true
}
```

Preserve the existing private directory and sealing key during migration. Do not
initialize a replacement organization database over an existing installation.
The former `scripts/start_server.sh` launcher is superseded by this native CLI.

## Supervise With systemd

```sh
mkdir -p ~/.config/systemd/user
cp scripts/release/server-package/ark-server@.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ark-server@main.service
sudo loginctl enable-linger "$USER"
```

The supplied unit runs `~/.local/bin/ark-server` with
`~/.config/arkstudio/<instance>.json`. For another binary location, use
`systemctl --user edit ark-server@main.service` and override `ExecStart`. A nondefault `ExecStart` needs
an empty `ExecStart=` line before its replacement.

Linger allows the user's service manager to start at boot and continue after
SSH logout. It requires administrator permission once; ordinary service restarts
do not. A dedicated OS user is recommended for a shared server.

```sh
systemctl --user restart ark-server@main.service
systemctl --user stop ark-server@main.service
systemctl --user status ark-server@main.service
journalctl --user -u ark-server@main.service -n 100
curl --fail http://127.0.0.1:8890/api/health
```

Health must report both `ok: true` and `auth_required: true`. The service restarts
after a process crash and limits repeated startup failures. It does not change
the server's OS account, GPU policy or provider credentials.

## Network Access

The backend binds to loopback. For private access:

```sh
ssh -N -L 8890:127.0.0.1:8890 user@your-server
```

Open `http://127.0.0.1:8890` locally. SSH encrypts transport and ARK still requires
account sign-in. For public access, configure an HTTPS reverse proxy and set
`public_url` in JSON to its stable URL. Set `trust_proxy: true` only when the
backend is reachable exclusively through a trusted proxy. See [mobile access](../guide/mobile.md).

Anonymous Cloudflare Quick Tunnel URLs can change independently of ARK and have
no stable hostname guarantee. Use a named tunnel or an owned HTTPS domain when
links must survive proxy and machine restarts.

## GPU and Agent Authorization

| Interface | Credential | Authorization |
| --- | --- | --- |
| `/api/gpu/jobs` | Signed-in browser or permitted paired-device session | Current project read/write grant |
| `/api/agent/v1/gpu/jobs` | Personal agent bearer token | Token scope capped by current account/project grant |
| `/api/task-agent/v1/gpu/jobs` | Task-session token | Bound project/session; submission and cancellation also require `full_access` and current write permission |
| Legacy agent API | Server token plus account session in account mode | Current account/project grant |

Agents can submit, list, inspect, read logs and cancel GPU jobs directly with
their own personal token. They never need to recover a browser password from
the server process environment. Use `X-ARK-Project` to select the owning project.
Tokens do not authorize arbitrary UI or administration endpoints. Project and
token permissions are enforced by the server, including after grants change.

An explicitly enabled GPU queue resumes its worker during server startup;
opening the GUI is unnecessary. Queue state is persisted in SQLite. Remote GPU
jobs run through the configured SSH identity and are separate from the ARK
service process. ARK coordinates resources; it does not provide OS or container
isolation between mutually untrusted users.

## Backup and Restore

Back up the complete data directory and server configuration together. Include:

- Organization database: users, password hashes, grants, personal token hashes
  and sealed provider credentials.
- Private directory: especially the organization credential sealing key and GPU configuration.
- Project folders and assets, hidden agent/mobile/auth state, and task session logs.
- GPU queue database, recovery snapshots and the JSON configuration.

Include each project's `history.git/` when [node text history](../guide/node-history.md)
is enabled. It belongs to the `.ark` project alongside its database and assets;
it is separate from the application source checkout. Blackbox SQLite snapshots
alone do not preserve Git history or attachment bytes.

For a consistent backup across these stores, briefly stop the service and take
the archive before restarting it. For the initialized layout above:

```sh
umask 077
mkdir -p ~/ark-backups
systemctl --user stop ark-server@main.service
tar -czf ~/ark-backups/main.tar.gz \
  -C "$HOME" .local/share/arkstudio/main .config/arkstudio/main.json
systemctl --user start ark-server@main.service
sha256sum ~/ark-backups/main.tar.gz
```

Check command failures before treating an archive as a backup. Keep it private,
retain a second copy on another host, and perform an actual restore drill.
Stop ARK before restoring to the original absolute paths; keep the previous
data directory as a rollback copy. Verify SQLite integrity, login, grants,
tokens, project records, asset reads and credential decryption after restore.
External symlink targets and externally hosted files need separate backup.

For a copy on a second machine that is refreshed every few minutes and can take
over, see [Standby server](standby.md).

Remembered browser sessions and personal tokens survive a restart. Ordinary
browser sessions require another sign-in. Provider subprocesses are stopped by
the service manager; their history remains, but active conversations require
explicit relaunch. A persistent queue does not imply that local provider
processes can resume automatically.

## Update From Git

```sh
git status --short
git pull --ff-only
systemctl --user restart ark-server@main.service
```

Resolve local changes before updating. Record the deployed Git commit, retain a
verified data backup before migrations, and verify authenticated health and the
interfaces used by your deployment after restarting. A clean checkout contains
reusable source and tests; instance credentials, logs, archives, proxy binaries
and one-off operator scripts stay outside it.

Only sanitized example configurations belong in Git. Never commit live server
inventories, account exports, tokens, private keys, environment files, databases,
or operation reports containing real hosts and deployment paths. Ignore rules
are a guardrail; review staged content before committing because force-adds and
already tracked files bypass them.
