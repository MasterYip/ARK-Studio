---
name: setup
description: Register and verify the resources used by an ARK project.
---

# ARK environment setup

Use this skill before task work in a new environment, or whenever a member
wants to add resources or machine paths. Several people can maintain the same
project. Setup is repeatable and additive: never replace earlier contributions.

## Workflow

1. Read `GET /api/agent/v1/setup` and `/api/agent/v1/whoami` with the user's
   personal token. Send `X-ARK-Host` with a stable machine ID when away from
   the ARK server. Read the complete `resources` register, including additions
   by other members. All members see all registered resources and paths.
2. Before asking setup questions, reuse the registered profile, earlier answers
   and configured resources. Run read-only discovery with
   `POST /api/agent/v1/setup/discover {paths}` using only user-provided paths
   (an empty list is allowed when no paths have been provided). Read configured
   GPU server names and provider presets from its response; they may already
   exist even when the project register has no compute or agent resources.
   Discovery runs on the ARK server: `X-ARK-Host` does not make it inspect the
   caller's computer. Label observations by their actual source machine and
   verify user-provided local paths locally. Do not crawl disks, SSH to
   servers, execute project commands or modify repositories during setup.
3. Show the existing facts and discovery findings for correction, then ask only
   for missing, conflicting or changed details, one area at a time: scope,
   code, team, compute, commands and authority. Do not restart a blank
   questionnaire or repeat a question already answered by the user or register.
   For example, when GPU servers are configured, show their names and ask
   which apply here; ask only about environments or artifact paths still
   missing. Configuration does not prove access or authorize a GPU probe/job.
   Reuse an existing resource and add this machine's location rather than
   making a duplicate. Preserve established policies and skipped areas unless
   the user requests changes. A user may skip an area or record unknowns.
   Reconcile discovery's generated questions with the existing setup and
   conversation before asking them; rerun discovery only for new paths or
   changed configuration.
4. Add resources with `POST /api/agent/v1/setup/draft`:
   `{resources: [{id, kind, name, purpose, details, path_or_ref}], propose: true}`.
   Kinds are `repository`, `task_space`, `compute`, `environment`, `storage`,
   `command`, `secret`. `details` uses the existing per-kind profile fields.
   Repeated drafts merge by ID and preserve previous resources and paths.
   Use `POST /api/agent/v1/setup/location {resource, path_or_ref, host_label}`
   for another location. Deletion requires an explicit `remove {id}` action.
5. Project objective, action policies, agents, cadence and task-ID conventions
   use `{layer: "project", profile: {...}, propose: true}`. Keep established
   policies unless the user requests changes. Your contribution defaults to
   `layer: "user"`; you cannot edit another person's contribution.
6. Show `user_summary_markdown` and its `layers.user.digest`, then let the
   person review and confirm in Settings -> Setup / Resources. Only a human
   confirms; shared project facts retain project-admin confirmation. One
   person's new resources do not invalidate someone else's confirmation.
7. Run `POST /api/agent/v1/setup/verify`. Report failures, unknowns and missing
   machine paths. Checks are read-only and only run on the ARK server's host.
8. Before later task work, read setup again to see everyone's latest resources.
   Follow `readiness` and existing `allow` / `ask` / `deny` action policies.

## Resource Use

The register records where assets exist. It does not grant filesystem, SSH or
storage access. `accessible` and `usable_here` mean configuration is recorded
for this host; verify actual reachability before use. `known_but_inaccessible`
still contains other machines' paths and owners. Ask the contributor when a
resource is on their personal computer. Do not treat that path as local or
recreate an asset merely because it cannot be reached.

There are no private/listed/shared settings or per-resource sharing lists.
Store credentials only as references (`env:NAME`, `alias:name`,
`ssh-config:host`); every member can see those references, never secret values.
Never infer permission to execute an action from tool availability.

## API And CLI

```text
GET  /api/agent/v1/setup
POST /api/agent/v1/setup/discover {paths}
POST /api/agent/v1/setup/draft {layer?, profile?, resources?, propose?}
POST /api/agent/v1/setup/resource {layer?, resource}
POST /api/agent/v1/setup/location {resource, path_or_ref, host_label?}
POST /api/agent/v1/setup/remove {layer?, id}
POST /api/agent/v1/setup/host {label}
POST /api/agent/v1/setup/propose {layer?}
POST /api/agent/v1/setup/verify
GET  /api/skills/setup/state
POST /api/skills/setup/confirm {layer?, digest, note?}  human only

python -m arkstudio skills setup state --project <path.ark>
python -m arkstudio skills setup discover --project <path.ark> --paths <dir>
python -m arkstudio skills setup draft --project <path.ark> --payload profile.json --propose
python -m arkstudio skills setup confirm --project <path.ark> --digest <digest>
python -m arkstudio skills setup verify --project <path.ark>
```

Existing flat profile fields (`repositories`, `task_spaces`, `compute`,
`environments`, `storage`, `commands`, `secrets`) are accepted for compatibility.
See `docs/operations/multi-user-setup.md` and `docs/agents/skills.md` for the record format.

## Multiple primary repositories

A project may register more than one repository with role `primary` (for example a paper repository and a code repository). Readiness lists all of them in `primary_repositories`; each task manifest names the repository it uses. At least one primary must be read-write for implementation work.

## Device names

Use one stable `X-ARK-Host` per computer and a readable device label (normally the hostname). Locations belong to the host; another computer adds its own locations for the same resource instead of overwriting existing paths.
