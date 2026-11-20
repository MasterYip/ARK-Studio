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

## GPU servers are per project

The ARK server's GPU server list is shared by every project, but a project
only sees, probes and queues on the servers it registers here: a `compute`
resource of kind `ssh` whose `gpu_server` names a server from the list, in the
project layer or in any member's own layer (any state). Registering one in your
own layer makes it visible to every member of the project, so a member with
write access can opt the project in; the server's address, user and config
path stay visible only to organization owners/admins (and in local mode).
Without a registration the GPU views show a hint and an **Open Setup** button
that leads here (they do not list the server names; setup discovery offers
them). Details and the migration steps for existing projects:
[gpu.md](gpu.md#project-scope-and-who-sees-what).

## Device names

Use the device id for `X-ARK-Host`: sha256 of `ark|` + the OS machine id,
first 12 hex (`ark-server agent host-id`; per-OS commands in
[External agents](../agents/external-agent.md#device-id)), and a readable
device label such as the machine's hostname (`laptop`, `gpu-node-01`).
The ARK server uses the same id for itself; its older fingerprint
(hostname, system and home folder) is an alias of it.  The host ID is the join key
for locations; the label is what the UI shows.  Do not reuse a host ID on a
second machine.  Another computer should send its own `X-ARK-Host` and add its
locations with the same resource keys, rather than overwriting the first
machine's paths.

Setup is repeatable per person and per device.  A location is not part of a
layer's confirmed profile: adding one (for a new person, or a person's new
device) changes no confirmation and does not move an unset layer to draft.
Settings > Resources lists *My devices*: each device with its last-seen time
and its path for every resource, and adds or edits a device's path.
`POST /api/agent/v1/setup/alias {from}` folds an old host id into the
caller's current one (its own devices, or any for a project admin).

### Location sizes

The Resources graph and Settings > Resources show how much each location
holds on its device: `2.3 GB · 12,340 files · 5 min ago`, for a git
checkout also `working tree 1.1 GB / .git 1.2 GB`, marked as old after a
day and `not measured` until a size arrives.  Each device measures its own
paths, so sizes are listed per device and never added up:

- **The ARK server** measures the locations on its own host in the
  background: after the setup or the graph is read, at most every ten
  minutes per project, for sizes missing or older than 12 hours (3 days for
  trees that hit a cap).  One path at a time, on a background-priority
  thread, at most 20 s and two million entries per path (the size is then
  shown as `≥`); links are counted but not followed, other file systems
  mounted inside and network paths are not measured, unreadable folders are
  skipped.  `ARK_LOCATION_SIZES=off` turns this off.
- **The ARK desktop app** measures this computer's paths when Settings >
  Resources is open (sizes missing or older than a day) and reports them.
- **An agent** may report the sizes of its own device's locations, for
  example when it registers or verifies a path.

The ↻ button measures one location again (`POST /api/skills/setup/measure
{resource, path_or_ref}`, once a minute per location); for a path on another
device it answers `409 SETUP_SIZE_ON_DEVICE`.  Sizes are kept in
`setup/location-stats.json`, outside the layers: they change no digest and
no confirmation.

```text
POST /api/agent/v1/setup/location-stats   (X-ARK-Host: <this device>)
{items: [{resource: "project/code", path_or_ref: "/home/ana/code",
          bytes: 2300000000, files: 12340, git_bytes: 1200000000,
          capped: false, measured_at: "2026-11-20T08:00:00Z"}]}
```

`resource` must have a location registered on the calling device
(`path_or_ref` is needed only when it has several there); a report for
another device's path is refused.  `kind` may be `dir` (default), `file`,
`missing`, `network` or `error`.  It needs write access.  Example on Linux:
`du -sb <path>` for bytes and `find <path> -type f | wc -l` for files.

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
Discovery returns the server **names only** (no address, tags or note).
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
POST /api/agent/v1/setup/propose   {}
POST /api/agent/v1/setup/verify
POST /api/agent/v1/setup/location-stats  {items: [{resource, path_or_ref, bytes, files}]}
```

`propose: true` on a write, or `propose {}` without a layer, sends every layer
with changes since its last confirmation (the project layer and the caller's
own) in one call; the reply lists them in `pending` with their digests.
`{layer}` still proposes one layer.

Human confirmation remains in the UI. The review lists what changed in each
layer since its last confirmation (added, changed with the old value struck
through, removed) and one Confirm covers every pending layer the person may
confirm: `POST /api/skills/setup/confirm {layers: [{layer, digest}]}` checks
all of them before writing any. The project layer needs a project admin; a
member confirms their own layer and the project change waits for an admin.
`GET /api/skills/setup/state` gives each layer's `changes` under
`layers.<layer>.changes`. Personal tokens identify the contributor
and retain their user's project permissions. The legacy server token can read
the complete register and writes project records as before.

## Assistant chat

The Assistant chat is per person and project (see
[Assistant history](../guide/workbench.md#assistant-history)): members never
see each other's conversations, and a project ZIP carries everyone's only
when a project admin downloads it from the browser.

## Compatibility

Old `environment.json` data is backed up and migrated without losing paths,
resource details or secret references. Its confirmation is carried forward
when the digest still matches. Setup on another machine does not mark the
whole project stale. Flat legacy profile inputs remain accepted and are merged.

The implementation retains per-user files and existing lifecycle APIs, but
does not expose an additional resource permission system. Setup mutations are
serialized within the ARK server process to prevent lost concurrent updates.
