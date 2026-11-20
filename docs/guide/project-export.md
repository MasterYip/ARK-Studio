# Project Export

Open **Settings > Project > Data** and select **Download project ZIP**. The
download works in the browser and the native desktop window. It names the
project displayed in that tab, rather than relying on another tab's selection.

The ZIP contains a restorable `.ark` project folder:

- `project.json`: identity, project settings and display metadata.
- `ark.sqlite3`: an online SQLite backup, including nodes, links, tags, topics,
  comments, media records, Assistant chat and pending text checkpoints. A
  project admin downloading from the browser (and anyone in local mode) gets
  every member's Assistant conversation; for everyone else, and for every
  personal agent token or task agent, the copy keeps only the requester's own
  conversation (the others are deleted from the copy and the file is
  compacted, so no trace of them remains), and `ARK-EXPORT.json` lists
  "other users' Assistant chat" under exclusions.
- `history.git/`: existing Git text history, when enabled.
- Internal attachments, project-local files and activity logs, retaining their
  relative paths. Attachments are streamed into the ZIP, including large videos.

The export excludes previous `exports/`, development caches, checkout `.git/`,
symlinks and Windows junctions. Stored agent session transcripts
([Session context](session-context.md)) are excluded too, unless you tick
**Include session transcripts** before downloading. Then the ZIP also contains
`sessions/` (uploaded external sessions) and
`sessions/internal/<session id>/` (ARK task-session logs). The internal logs
are redacted and their metadata has no read tokens. References to external files or remote URLs
remain in the database; their contents are not downloaded or copied. Account
passwords, personal tokens and server configuration live outside the project
and are not part of the archive. This is a project export, not an instance backup.

SQLite and Git text history are captured under the project's write lock. Bulk
attachments are then read without holding that lock; avoid replacing attachment
files externally while an export is running. `ARK-EXPORT.json` at the ZIP root
records the format, timestamp, project folder and exclusions.

## Restore

Extract the `.ark` folder into the destination server's project directory, then
open it from the project switcher. For a local browser instance:

```sh
ark-server serve --root /path/to/projects
```

If that project ID already exists on the destination, keep the original safe
and restore into a separate projects directory. Organization memberships and
grants are managed by the destination server, rather than imported from the ZIP.

## API

`GET /api/export?format=project&project=<project-id>` returns `application/zip`.
Add `&sessions=1` to include stored session transcripts; `ARK-EXPORT.json`
then reports `"sessions_included": true` and the number of internal sessions.
`HEAD` returns the download headers without a response body. Read access to the
specified project is required. Personal agent tokens may use either that endpoint
or `GET /api/agent/v1/export?format=project` with `X-ARK-Project`.

```sh
curl --fail --header "Authorization: Bearer $ARK_TOKEN" \
  --header "X-ARK-Project: $ARK_PROJECT" \
  "$ARK_URL/api/agent/v1/export?format=project" --output project.ark.zip
```

See [Markdown bundles](markdown-bundles.md) for exporting selected nodes into
ordinary Markdown instead, and [server operations](../operations/server-operations.md) for
backing up an entire instance.
