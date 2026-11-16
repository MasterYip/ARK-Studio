# Shared project setup

Every project member can run setup repeatedly. ARK keeps a shared register of
repositories, task spaces, GPU servers, environments, storage, commands and
secret references. Members and their agents see all registered resources and
paths, including additions made after their own setup.

There are no resource visibility levels, sharing lists or access requests.
The project's existing membership and read/write permissions still apply.
Knowing a path does not grant operating-system, SSH or storage access.

A project can register several primary repositories (for example a paper
repository and a code repository).  `readiness.primary_repositories` exposes
all of them; task manifests still name the specific repository they use.

## Recording resources

Each resource has an ID, kind, name, purpose, contributor and kind-specific
details. Each location records a machine ID, readable machine name, path,
contributor and timestamp. A resource may have several locations, including
several paths on one machine. Locations from all contributors are visible.

Contributions are stored per account so another person's setup cannot replace
them. A repeated setup upserts resources by ID and retains earlier entries
and paths. Removal is explicit, using the remove action. Shared project facts
(objective and existing action policies) retain their admin confirmation.
Each user can confirm their own contribution without invalidating anyone
else's confirmation.

Only references such as `env:WANDB_API_KEY` are recorded. Actual credentials
are rejected and never belong in this register.

## Device names

Use a stable device name for `X-ARK-Host`, and a readable device label such as
the machine's hostname (`laptop`, `gpu-node-01`).  The host ID is the join key
for locations; the label is what the UI shows.  Do not reuse a host ID on a
second machine.  Another computer should send its own `X-ARK-Host` and add its
locations with the same resource keys, rather than overwriting the first
machine's paths.

## Agents

### Repeated setup starts from existing configuration

Read the complete register, effective policies and earlier answers before
asking questions. Reuse facts already supplied by any member and ask only
about missing details, conflicts or changes for the current person and device.
Keep established policies and skipped areas unless the user requests a change.

Run read-only `POST /api/agent/v1/setup/discover {paths}` with user-provided
paths before asking about compute or agents; use `paths: []` if none have been
provided. Its configured GPU server names and provider presets can already
exist even when those resources have not been registered for this project.
Show those names and ask which apply, then ask about missing environments,
artifact locations or permissions. Never ask someone to recreate an existing
server configuration or disclose credentials. Reconcile generated questions
against the register and conversation instead of asking every question again.

Discovery executes on the ARK server, including filesystem and provider
checks. `X-ARK-Host` identifies the caller for setup records; it does not run
discovery on that device. Label the source machine, verify provided local
paths locally, and do not interpret a server-side missing path as proof that
the caller's checkout is absent. Configured server names do not prove local
SSH access or authorize a probe or job.

### Setup API

`GET /api/agent/v1/setup` returns:

- `resources`: the complete project register, including all machine paths;
- `accessible`: resources with a recorded location on the requested host, or
  without a machine-specific location requirement;
- `known_but_inaccessible`: resources with no recorded location on this host,
  still including their other locations and contributor;
- `host`, `policies`, `readiness`, summaries and confirmation information.

`accessible` and `usable_here` describe recorded configuration. They do not
prove access. Verify local paths and obtain SSH or storage credentials before
using a resource. A path on another member's laptop is not a local path.
Read the register again before task work to see recent additions.

Send `X-ARK-Host: <stable-machine-id>` when running away from the ARK server.
The browser defaults to the ARK server's machine, not the phone or laptop
displaying the page. Verification only checks the server's local machine;
it does not probe another machine's filesystem.

```text
POST /api/agent/v1/setup/draft     {resources: [...], propose: true}
POST /api/agent/v1/setup/resource  {resource: {id, kind, name, purpose, details, path_or_ref}}
POST /api/agent/v1/setup/location  {resource: "u:alice/code", path_or_ref, host_label}
POST /api/agent/v1/setup/remove    {id}
POST /api/agent/v1/setup/host      {label}
POST /api/agent/v1/setup/verify
```

Human confirmation remains in the UI. Personal tokens identify the contributor
and retain their user's project permissions. The legacy server token can read
the complete register and writes project records as before.

## Compatibility

Old `environment.json` data is backed up and migrated without losing paths,
resource details or secret references. Its confirmation is carried forward
when the digest still matches. Setup on another machine does not mark the
whole project stale. Flat legacy profile inputs remain accepted and are merged.

The implementation retains per-user files and existing lifecycle APIs, but
does not expose an additional resource permission system. Setup mutations are
serialized within the ARK server process to prevent lost concurrent updates.
