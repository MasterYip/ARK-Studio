# Session context management

ARK keeps the conversation history of agent sessions so a person can see what an
agent did and pick the work up again later. There are two sources:

- **Internal sessions** are started by ARK from **Execute › Runs** (Claude Code,
  Codex, DSH and other presets). ARK already writes their output.
- **External sessions** run outside ARK, for example a Claude Code window that a
  Home starter prompt was pasted into. When the project allows it, such an agent
  (or a watcher in a terminal) uploads its own session JSONL to ARK.

Both appear in one list under **Execute › Runs › Session context**, where they can
be read, continued, renamed, linked to nodes and deleted.

## What existed before (v0)

| Area | State before this feature |
| --- | --- |
| Internal storage | `runtime/agent-sessions/<s_…>/{meta.json,stdout.jsonl,stderr.jsonl,workspace/}`, outside every project folder. `meta.project` names the project. |
| Transcript | `session_transcript.build_transcript` turns log lines into messages and tool cards. It recognises Claude `stream-json` and Codex `--json` shapes. |
| Continue | `POST /api/agent-sessions/{id}/continue` starts a new turn in the same session with the original prompt, the last output and the correction. No provider-native resume existed and the provider's own session id was not recorded. |
| External agents | A personal `arka_…` token copied from a Home starter prompt reaches `/api/agent/v1/…` with `X-ARK-Project`. ARK did not see the agent's conversation. |

## Data model

### Ids

| Kind | Id | Issued by |
| --- | --- | --- |
| internal | `s_<utc stamp><hex>` (unchanged) | `SessionManager.start` |
| external | `ark-ses-<10 lowercase base32/hex chars>` (`^ark-ses-[a-z0-9]{6,32}$`) | the browser, each time a Home prompt is copied or previewed |

An external id needs no server registration. ARK creates the record on the first
accepted upload. The prompt asks the agent to quote the id, so the id also
appears inside the agent's own JSONL. The watcher uses that to find the file.

### Internal session (existing `meta.json`, new keys)

```json
{ "native_session_id": "4f0c…",          // Claude session_id / Codex thread id, when seen
  "linked_units": ["T-0004"],             // in addition to task_ref
  "shared": false }                       // private to `actor` (the creator) unless true
```

ARK records `native_session_id` from the agent's stdout or stderr:
`{"type":"system","session_id":…}` (Claude stream-json),
`{"type":"thread.started","thread_id":…}` (Codex `--json`), or the
`session id: …` header line that `codex exec` prints.

### External session

```
<project>.ark/sessions/
  ark-ses-k3v9q2m7xa.jsonl   redacted transcript, one JSON object per line
  ark-ses-k3v9q2m7xa.json    metadata (below)
```

```json
{ "id": "ark-ses-k3v9q2m7xa", "kind": "external",
  "agent": "claude",                       // claude | codex | dsh | other
  "title": "Plan the ablation", "model": "claude-opus-…", "cwd": "E:/repo",
  "native_session_id": "1458aee0-…",       // sniffed from the JSONL or sent by the uploader
  "created_at": "…", "updated_at": "…", "uploaded_by": "agent:alice",
  "owner": "alice", "shared": false,       // the token's user owns it; private until shared
  "received_bytes": 123456,                // source-file bytes accepted so far (the next offset)
  "bytes": 120001, "lines": 840,           // stored (redacted) size
  "redactions": 3, "truncated_lines": 1,
  "chunks": [{"offset": 0, "length": 65536, "sha256": "…"}],   // last 64, for idempotent retries
  "linked_units": ["T-0004"], "prompt": "plan" }
```

## Storage layout and why

External sessions are stored in the **project folder** (`sessions/`). They belong
to the project, are deleted along with the project, and are removed one by one
in the UI.

Internal sessions keep their **live** copy in `runtime/agent-sessions/<s_…>/`,
which also holds a read token and the agent's private workspace. ARK mirrors
each one into the project, so a copied project carries it:

```
<project>.ark/sessions/internal/<s_…>/
  meta.json      the runtime meta without read_token, read_env, api,
                 command_preview, linux_account_home, approval_rules,
                 steer_queue and pid; strings redacted
  stdout.jsonl   redacted log lines
  stderr.jsonl
```

The mirror is rewritten when a session ends, on rename, link and share, when
the Session context list finds it stale (runtime files newer than the mirror),
and before a project export with sessions. Deleting the session deletes its
mirror. A mirror whose runtime session is gone (a copied or restored project, or
an archived session) is listed as an **ARK copy** (`copy: true`). It can be read,
downloaded, renamed, shared, deleted, and continued only by seeding a new
session. Mirrors do not count toward the upload quota.

## Visibility

Every session is **private to its creator** by default. For an internal session
that is `meta.actor`, the account that started it. For an external session it is
the user of the token whose first upload created it. Setting `shared: true` lets
every project member with read access see the session.

| Action | Who |
| --- | --- |
| List, get, transcript, raw, context, seed-continue | Creator, project admins, and everyone in the project if shared |
| Resume in place, rename, link, share/unshare, delete | Creator and project admins |
| Upload to an existing external id | Its owner and project admins (otherwise `403 SESSION_UPLOAD_NOT_OWNER`) |
| `GET /api/agent/v1/sessions/{id}` for an existing id | Its owner and project admins (otherwise 404) |

The legacy server token counts as a project admin, and so does every request
when account mode is off (local single-user). Hidden sessions return
`404 SESSION_NOT_ACCESSIBLE`. A visible session without manage rights returns
`403 SESSION_NOT_OWNER`. The same rules apply to the Runs workspace
(`/api/agent-sessions…`): read routes need view rights, and every write (input,
steer, stop, approvals, fork, archive, relaunch, delete) needs manage rights.
Every record includes `owner`, `shared` and `can_manage` for the current
viewer.

## API

All routes use the normal guards: Host check, account or personal-token
identity, and project scope (read for GET, write for other methods). The
`/api/agent/v1/…` routes accept a personal `arka_…` token (or the legacy server
token).

### Agent API (external uploaders)

`GET /api/agent/v1/sessions/{id}` returns the resync state:

```json
{ "ok": true, "uploads_enabled": false, "exists": true,
  "received_bytes": 123456, "limits": {"chunk_bytes": 4194304, "session_bytes": 52428800,
  "project_bytes": 524288000, "line_bytes": 262144} }
```

`POST /api/agent/v1/sessions/{id}/upload` has the request body:

```json
{ "offset": 123456,                  // byte offset of `content` in the source file
  "content": "{…}\n{…}\n",          // UTF-8 JSONL, complete lines only
  "sha256": "…",                     // optional: hex SHA-256 of content (checked if sent)
  "mode": "append",                  // append (default) | replace (offset must be 0)
  "length": 4096,                    // optional: bytes this chunk covers in the source file
                                     // (when the uploader shortened an oversize line)
  "agent": "claude", "model": "", "cwd": "", "native_session_id": "",
  "title": "", "task": "T-0004", "prompt": "plan" }
```

Rules, in order:

1. `403 SESSION_UPLOADS_DISABLED` unless the project setting
   `sessions.accept_uploads` is true.
2. `400 SESSION_ID_INVALID` for a malformed id.
3. `413 SESSION_CHUNK_TOO_LARGE` when `content` exceeds the chunk limit.
4. `400 SESSION_NOT_JSONL` when content does not end with `\n`;
   `SESSION_LINE_NOT_JSON` / `SESSION_LINE_NOT_OBJECT` when a non-empty line is
   not a JSON object (nothing of that chunk is stored). Other 400 codes:
   `SESSION_CONTENT_INVALID`, `SESSION_HASH_MISMATCH`, `SESSION_MODE_INVALID`,
   `SESSION_OFFSET_INVALID`, `SESSION_REPLACE_OFFSET`, `SESSION_LENGTH_INVALID`.
5. Idempotency:
   - `offset == received_bytes` appends.
   - A chunk with the same `(offset, sha256)` as a recorded one returns
     `200 {"duplicate": true}`.
   - Any other offset returns `409 SESSION_OFFSET_MISMATCH`, with the expected
     offset in the message. The uploader re-reads `GET …/{id}`.
   - `mode: "replace"` with offset 0 rewrites the transcript. Use it when the
     source file was rewritten or shrank.
6. Every line is parsed and its strings run through the secret redactor
   (`arkstudio/secret_redact.py`). A line longer than the line limit is stored as
   `{"type":"ark_truncated","bytes":N,"preview":"…"}`.
7. `413 SESSION_QUOTA` when the stored session, or `413 SESSION_PROJECT_QUOTA`
   when the project's `sessions/` folder, would exceed its cap.

The response is
`{ok, session, received_bytes, appended_lines, redactions, duplicate}`.

`GET /api/agent/v1/sessions/watcher.py` serves the standard-library watcher as a
single file, so an agent on another machine can run it without installing ARK.

### UI API

| Method and path | Purpose |
| --- | --- |
| `GET /api/session-contexts` | `{sessions:[…], settings:{accept_uploads}, usage:{bytes,limit}}`. Lists internal and external sessions for the current project, newest first. |
| `GET /api/session-contexts/{id}` | One record (`source`, `agent`, `model`, `cwd`, `native_session_id`, `updated_at`, `bytes`, `linked_units`, `task_ref`, `status`, `resume`) |
| `GET /api/session-contexts/{id}/transcript` | `build_transcript` items (Codex rollout lines are normalised first) |
| `GET /api/session-contexts/{id}/raw` | Download the stored JSONL (internal: stdout log) |
| `GET /api/session-contexts/{id}/context` | `{summary}`: compact Markdown summary of the last turns, for "Copy context" |
| `PATCH /api/session-contexts/{id}` | `{title?, linked_units?, shared?}`: rename, link to nodes, share or unshare (manage rights) |
| `DELETE /api/session-contexts/{id}` | Delete (internal: same rules as `DELETE /api/agent-sessions/{id}`) |
| `POST /api/session-contexts/{id}/continue` | `{mode: "resume"\|"seed", text, platform?, model?}` (below) |

The `resume` field of a record:

```json
{ "native": true,                         // ARK can resume it with the provider's own resume
  "command": "claude --resume 1458aee0-…",  // exact CLI command (external, or internal with an id)
  "cwd": "E:/repo" }
```

The **Continue** flow:

- **Internal, resumable.** The platform is `claude` or `codex` and
  `native_session_id` is known. `mode=resume` runs
  `continue_session(…, resume=True)` in place: Claude gets `--resume <id>`, and
  Codex runs as `codex exec … resume <id> "<text>"`. The prompt is only the new
  text, because the provider holds the history.
- **Everything else** (`mode=seed`) starts a new internal session. Its prompt is
  the text plus "Context from ARK session …", a summary of at most 6000
  characters (title, agent, cwd, linked nodes and the last messages).
- **External in a terminal.** The UI shows `resume.command`: Claude
  `claude --resume <id>` or Codex `codex resume <id>`, run in the recorded `cwd`.
  DSH and other agents have no documented resume flag; use **Copy context** and
  paste it into a new conversation.

### Settings and export

- Project settings section `sessions`: `{"accept_uploads": false}` (default off,
  admin only, via `PATCH /api/project/settings`). It can be changed in
  **Settings › Project › Access › Session uploads** (unified save bar) or with
  the toggle in Runs › Session context.
- `GET /api/export?format=project&sessions=1` refreshes the mirrors, then adds
  the project's `sessions/` folder: external transcripts and
  `sessions/internal/<s_…>/`. Without `sessions=1`, `sessions/` is excluded and
  listed in `ARK-EXPORT.json` `excluded`. Export needs project read access and
  includes every session in the folder, including other people's private ones.
  The export is a project-level copy, just like copying the folder.

### CLI

```
ark-server session-watch --id ark-ses-… [--file PATH] [--agent claude|codex|dsh]
                         [--server URL] [--project ID] [--token-env ARK_TOKEN] [--interval 60] [--once]
```

The watcher reads the token from the environment variable (default `ARK_TOKEN`),
never from argv. Each pass it gets `received_bytes`, reads the file from that
offset up to the last newline, and uploads in chunks of 1 MB or less. If the file
shrank, it re-uploads with `replace`. On `409` it resyncs. On `403` it prints the
consent hint and exits with code 3. Without `--file`, it searches the agent's
default session folder for the newest JSONL that contains the id:

| Agent | Default location |
| --- | --- |
| Claude Code | `~/.claude/projects/<cwd-slug>/<session uuid>.jsonl` |
| Codex CLI | `~/.codex/sessions/YYYY/MM/DD/rollout-…-<uuid>.jsonl` (`$CODEX_HOME` respected) |
| DSH | `~/.dsh/sessions/<cwd-slug>/session-<uuid>/session.v4.jsonl.zstd`. This file is zstd-compressed, so the watcher reads it only when Python's `compression.zstd` (3.14+) or the `zstandard` package is available. Otherwise pass `--file` to an exported JSONL. |

## Consent flow

1. Uploads are **off** per project until an admin turns on **Accept session
   uploads from external agents** (Runs › Session context). While uploads are
   off, the agent API returns `403 SESSION_UPLOADS_DISABLED` and nothing is
   written.
2. Every Home starter prompt carries an `ark-ses-…` id. The upload section is
   worded as optional. The agent must ask the user first. If the user says no,
   it does not upload and does not start a watcher. When uploads are off, the
   prompt only says so and gives no upload instructions.
3. The watcher runs only when the user (or the agent, with the user's yes)
   starts it in a terminal. Stopping it stops the uploads.
4. Any project writer can delete a stored session. Deleting removes the
   `.jsonl` and `.json` files.

## Privacy and limits

- **Redaction** runs before anything is stored. The redactor replaces these with
  `[REDACTED:<kind>]`:
  - token shapes: `arka_…`, `sk-…`, `sk-ant-…`, GitHub, AWS and Slack tokens,
    and JWTs;
  - `Authorization: Bearer …` values;
  - PEM private-key blocks;
  - `password`, `api_key`, `token` or `secret` assignments;
  - the server's own legacy token.

  The redaction count is stored and shown.
- **Default limits**: 4 MB per upload request, 256 KB per line (longer lines
  are truncated with a marker), 50 MB per session and 500 MB per project. The
  last three can be changed with `ARK_SESSION_LINE_KB`,
  `ARK_SESSION_MAX_MB` and `ARK_SESSION_PROJECT_MAX_MB`.
- Stored only under `<project>.ark/sessions/`. Never served to task-agent read
  tokens or paired phones in read-only mode (write routes need write scope).
- **Export** leaves sessions out unless **Include session transcripts** is
  ticked.

## Out of scope for v1

- Live streaming: uploads are periodic and the list refreshes on demand.
- Uploading attachments or images referenced by the JSONL.
- Server-side reservation of ids, or a "waiting for upload" placeholder row.
- Merging an external session into an internal one, or two-way sync.
- A native resume for providers other than Claude Code and Codex.
- Finer sharing (to selected people or teams), and hiding mirrors from people
  with direct filesystem access to the project folder or a project export.
