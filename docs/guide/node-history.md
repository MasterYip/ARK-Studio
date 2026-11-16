# Node Text History

ARK can record Git versions of selected nodes. Tracking is off by default and
is independent for each node. Git must be installed **on the ARK server**;
browsers and desktop clients need no Git installation. The Git repository is
**part of the ARK project files**, created on the first enable operation:

```text
research.ark/
  project.json       Project manifest and settings
  ark.sqlite3        Current nodes, tracking switches and revision heads
  assets/            Project media
  history.git/       Bare Git repository for opted-in node titles and bodies
```

It does not use the ARK Studio source checkout's `.git/` directory or require a
GitHub remote. Copying only the SQLite database does not copy the Git history.

## In the App

Open **Version history** from a node's right-click menu, its inspector clock
button, or the Editor toolbar. Enable **Record text versions** to save an initial
version. Agent/API text edits create revisions immediately. Human edits in the
GUI save to SQLite immediately but are grouped for version history: after five
minutes without another text edit, a checkpoint is recorded when the cumulative
change reaches 100 characters or 10% of the last recorded text, whichever is
smaller (at least one character). The server checks every 15 seconds. Small
changes keep accumulating until that threshold is reached. Large changed spans
use a bounded estimate. Identical text and metadata-only edits create no revision
and do not reset the idle timer.

**Save version** records the current saved text immediately, even for a small
change. Save or discard any unsaved editor/inspector draft first. Repeating it
without changing the text does not create duplicates. Pending automatic
checkpoints survive server restarts; the live text remains in SQLite. Before an
agent edit or restore replaces pending human text, ARK checkpoints that text too.

Choose **From** and **To** to compare revisions. A wide window shows the old and
new text side by side with line numbers and aligned changes. Long lines wrap
automatically, and both sides share one scrollbar so corresponding lines stay
aligned even when they wrap to different heights. A faint background marks each
changed line; a stronger highlight marks the actual inserted or deleted text
within it. Similar changed paragraphs are aligned before inline highlighting, so
an inserted paragraph does not shift all following replacements into the wrong
rows. Very large change blocks fall back to bounded positional alignment. Narrow
windows automatically use a unified diff with the same wrapping
and inline highlights. **Previous revision** compares
the target against its parent, or against an empty document for the initial
version. **Preview** renders the selected title and Markdown body with ARK's
normal references and media links. The arrows page through older/newer versions.

The right arrow beside a filename applies that file's **From** text to the
current node. Arrows beside individual change blocks apply only that block,
keeping other changes and the other file intact. Each application records a new
revision and refreshes the comparison, retaining the From revision so remaining
blocks can be applied one by one. These arrows are also available in the narrow
unified view. **To** must be the current recorded text; use **Save version** first
if human edits are pending. Historical-only comparisons and read-only users
cannot apply changes. Concurrent edits and unsaved local drafts are protected
as with full-version restore.

**Restore this version** replaces the title and body after confirmation and
records the result as a new revision. Status, tags, ownership, dates, attachments
and comments stay as they are. Save or discard pending edits in other views of
the same node before restoring. A concurrent text edit invalidates the restore
request until the history is refreshed.

Turning tracking off retains all previous versions. Edits while it is off are
not recorded; enabling it again captures the current text if it differs from the
last revision. Restoring requires tracking to be enabled.

## Agent API

Use a personal bearer token and `X-ARK-Project: <project-id>`, as documented in
[external agent access](../agents/external-agent.md). Reading requires project read access;
toggling and restoring require write access and a write-scoped token.

| Method | Endpoint | Result |
| --- | --- | --- |
| GET | `/api/agent/v1/units/{uid}/history?limit=50&offset=0` | State and revisions, newest first; `has_more` indicates another page |
| POST | `/api/agent/v1/units/{uid}/history` | Set `{"enabled": true}` or `{"enabled": false}` |
| GET | `/api/agent/v1/units/{uid}/history/{hash}` | Exact title/body for a revision |
| GET | `/api/agent/v1/units/{uid}/history/diff?from={hash}&to={hash}` | Unified Git diff of title/body; both parameters are optional |
| POST | `/api/agent/v1/units/{uid}/history/restore` | Restore text with `{"revision": "<full-hash>", "expected_hash": "<current_hash>"}` |
| POST | `/api/agent/v1/units/{uid}/history/checkpoint` | Save the current text immediately with `{"expected_hash": "<current_hash>"}` |
| POST | `/api/agent/v1/units/{uid}/history/apply` | Apply old text for a file or change block; payload below |

`diff` also returns `changes` with `file`, per-file zero-based `index`, and
zero-based `before_start`/`after_start` plus line counts; `can_apply` indicates
whether To is current and tracking is enabled, and `current_hash` is the edit
guard. To apply a single change, send:

```json
{"from": "<full-hash>", "to": "<current-revision-hash>", "file": "body.md", "change": 0, "expected_hash": "<current_hash>"}
```

Omit `change` (or use `null`) to apply the entire file. Only `body.md` and
`title.txt` are accepted. Re-fetch the diff after each application because block
indices change. Both revisions must belong to this node, and To must match its
current text. Apply/checkpoint require write access and enabled tracking.

The list includes `enabled`, `available` (Git installed), `head`, `current_hash`,
`versions`, `pending`, `limit`, `offset`, and `has_more`. Each version has `id`, `author`,
`created_at`, and `message`. `current_hash` is a content hash used to prevent
restoring over a concurrent edit, not a Git revision ID. Use full Git hashes for
revision reads and comparisons; revisions belonging to another node are rejected.
List limits range from 1 to 200.

Browser sessions use equivalent `/api/units/{uid}/history...` endpoints. The
optional `X-ARK-Project` header pins these operations to the displayed project
when multiple tabs are open; project permissions are checked again for that
project. Paired devices remain confined to their paired project.

## Storage and Recovery

The project's `history.git/` is a separate bare Git repository. Each commit tree
contains exactly `title.txt` and `body.md`. Media bytes, the database, project
configuration, comments and credentials are never added to it. Markdown may
contain attachment paths, but it does not freeze the attachment contents. An old
preview uses the media currently available in the project.

SQLite stores each node's tracking switch and published revision hash. Git
objects are written and retained before that hash and the edited text are
committed in the same SQLite transaction. A Git write failure rolls back the
node text update, so an enabled node cannot silently lose a revision. Failed SQL
transactions may leave an unlisted Git object; they do not publish a revision.
Direct SQL modifications bypass history and are unsupported for normal editing.

Back up or move **the entire `.ark` directory**, including `history.git/`, to
preserve both the records and history. The existing Blackbox database snapshots
do not include this Git repository or media; they are not a substitute for a full
project backup. Stopping ARK before a filesystem copy gives a consistent backup.
History does not depend on shell environment variables or a developer checkout.

For manual inspection, use a revision returned by the API:

```sh
git --git-dir=/path/to/project.ark/history.git show <full-hash>:body.md
git --git-dir=/path/to/project.ark/history.git log <full-hash>
```

ARK retains immutable revision refs. Do not rewrite or remove them while ARK is
using the project. Disabling tracking or deleting a node does not securely erase
old Git objects. Full project backups and any retention policy should treat
history as private project content.
