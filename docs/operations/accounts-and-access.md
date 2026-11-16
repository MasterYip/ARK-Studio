# Accounts and Project Access

ARK supports a single local user or an organization with multiple accounts,
teams and project grants. The HTTP API enforces access independently of the UI.

## Start With Authentication

For a server deployment, use `ark-server server-init` once, then
`ark-server serve --config <server.json>`. Account passwords are stored
as salted hashes in the organization database; restart does not need bootstrap
credentials in the shell environment. See [server operations](server-operations.md).

The default loopback server maps unauthenticated local use to a reserved local
owner. To require sign-in, configure an account before starting the server.
For example, in PowerShell:

```powershell
$env:ARK_AUTH_USER = 'your-username'
$env:ARK_AUTH_PASSWORD = 'replace-with-your-password'
ark-server serve --port 8890
```

Or in a POSIX shell:

```sh
export ARK_AUTH_USER='your-username'
export ARK_AUTH_PASSWORD='replace-with-your-password'
ark-server serve --port 8890
```

Use a private environment or a password file (`--auth-password-file`) for a
persistent deployment. `--expose` binds to a public interface and requires an
account; it does not itself provide HTTPS. Configure a trusted HTTPS proxy or
SSH tunnel as described in [mobile and remote access](../guide/mobile.md).

On first startup, the configured account becomes the organization owner.
Subsequent restarts keep the database's password, including changes made in the
UI. `ARK_AUTH_SYNC=1` explicitly reapplies the bootstrap password on startup;
remove it after a deliberate reset.

## Organization Roles

| Role | Access |
| --- | --- |
| `owner` | Organization administration and admin access to all projects |
| `admin` | Organization administration and admin access to all projects |
| `member` | Only projects granted directly or through a team |
| `viewer` | Read access across the organization |

Project grants are `read`, `write` or `admin`. Effective access is the highest
permission granted by the organization role, a direct grant or a team grant.
Project lists and task-session records are filtered to the caller's access.

Owners and administrators manage accounts, teams, grants and Linux identities
under **Settings > Admin**. Project settings require project admin permission;
content changes require write permission. A project grant does not grant access
to the server's operating-system accounts, SSH hosts or external storage.

## Personal Configuration

Settings separates shared project values from a person's own configuration.
Effective values resolve in this order:

1. Personal override
2. Project configuration
3. Server or environment configuration
4. Built-in default

Configure model credentials and agent commands under **Settings > Personal**.
Project configuration can declare supported credential variable names and
provider availability; it cannot replace personal launch commands. Personal
credential values are sealed in the organization database with a separate
machine-local key. API responses expose credential presence and masked hints.

On Linux, an administrator can assign existing OS accounts to ARK users for
provider execution. Selecting an account requires the server's non-interactive
sudo configuration and workspace permissions; a failed identity check does not
fall back to a different account. See provider and deployment notes before
enabling this on a shared host.

## Phone Pairing

Pairing creates a device session scoped to one project and the account that
issued the code. Its access cannot exceed the issuer's project permission.
A paired session cannot administer the organization, switch projects or access
personal provider credentials. See [mobile access](../guide/mobile.md) for pairing,
device revocation and transport details.

## Storage and Recovery

The organization database defaults to `runtime/org.sqlite3`, separately from
project folders. Override it with `ARK_ORG_DB`. Credential sealing uses the
private directory configured by `ARK_PRIVATE_DIR`, or an explicitly supplied
`ARK_ORG_SECRET_KEY`.

Back up the database and its sealing key together when preserving personal
credentials. Treat both as private server data. Project folders alone do not
contain the organization accounts or personal credentials.
