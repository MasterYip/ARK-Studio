# Session context

ARK keeps the conversations of your agent sessions so that you can read them
again and continue the work later. Open **Execute › Runs** and scroll to the
**Session context** panel below the session workspace.

The panel lists two kinds of sessions for the open project:

- **ARK** sessions were started from Runs (Claude Code, Codex, Gemini CLI, DSH
  and the other presets). ARK records them automatically.
- **External** sessions ran outside ARK, for example in your own Claude Code
  or Codex window, and uploaded their transcript to ARK. This only happens if
  you allow it, as described below.

Each row shows the following:

- the source;
- the agent and model;
- when the session was last updated;
- the stored size and how many secrets were redacted;
- the task or nodes the session is linked to.

## Reading a session

Select **View** to read the conversation. Messages are shown in order. Tool
calls can be expanded, and errors are highlighted. **Copy context** copies a
short Markdown summary that you can paste into any agent. **Download JSONL**
saves the stored transcript.

## Continuing a session

Select **Continue**:

- **ARK session with a provider session id** (Claude Code, Codex or Gemini
  CLI reported one): ARK resumes the same provider conversation and sends only
  your new message. The dialog also shows the command to resume it yourself in
  a terminal, for example `claude --resume <id>` or `gemini --resume <id>` (run
  it in the session's folder: Gemini keeps its conversations per folder).
- **Other ARK sessions and all external sessions**: **Start new session**
  starts a new ARK session for the agent you choose. Its prompt is your
  message followed by a summary of the old session (original request, latest
  turns, working directory, linked nodes).
- **External Claude Code or Codex sessions** also show the exact command to
  resume them in their own CLI (`claude --resume <id>` / `codex resume <id>`),
  run in the recorded working directory.

From the **⋯** menu you can rename a session, link it to nodes (for example
`T-0004, F-0012`), download it, or delete it. Running ARK sessions must be
stopped before you delete or continue them.

## Allowing external agents to upload (optional)

Uploads are **off** for every project until a project admin ticks **Accept
session uploads from external agents**, either in the Session context panel or
in **Settings › Project › Access › Session uploads**. While the
box is unticked, ARK rejects every upload and stores nothing.

When uploads are on:

1. Copy a Home starter prompt (**Home › Start with an agent**). Every copy
   carries a new ARK session id such as `ark-ses-k3v9q2m7xa`, shown under the
   card.
2. The prompt tells the agent to **ask you** before uploading. If you say no,
   nothing is uploaded.
3. If you say yes, the agent (or you) starts the watcher in a separate
   terminal. The token comes from the environment, never the command line:

   ```sh
   export ARK_TOKEN=arka_...        # PowerShell: $env:ARK_TOKEN = "arka_..."
   ark-server session-watch --id ark-ses-k3v9q2m7xa --server http://127.0.0.1:8890 --project <project id>
   ```

   On a machine without ARK installed, download the single-file watcher first:

   ```sh
   curl -fsS -H "Authorization: Bearer $ARK_TOKEN" -H "X-ARK-Project: <project id>" \
     http://127.0.0.1:8890/api/agent/v1/sessions/watcher.py -o ark-session-watch.py
   python ark-session-watch.py --id ark-ses-k3v9q2m7xa --server http://127.0.0.1:8890 --project <project id>
   ```

   The watcher finds the agent's session file because the transcript mentions
   the session id. It looks in Claude Code's `~/.claude/projects`, Codex's
   `~/.codex/sessions` and DSH's `~/.dsh/sessions`. If it cannot find the file,
   pass `--file <path>`. It uploads new lines every `--interval` seconds
   (default 60) and stops when you press Ctrl+C. Use `--once` for a single
   upload.
4. The session appears in **Session context** after the first upload. Select
   the refresh button to update the list.

## Privacy

- Before storing anything, ARK redacts secrets: API keys, `arka_` tokens,
  bearer headers, private keys and `password=`/`api_key=` style values.
- Uploaded transcripts are stored only in the project folder, under
  `<project>.ark/sessions/`. Deleting a session removes its files.
- Each session is **private** to the person who started it (or whose token
  uploaded it). Choose **⋯ › Share with project** to let every project member
  read it and continue it in a new session. Only the creator and project admins
  can rename, link, share, resume in place or delete a session. Project admins
  see every session, so they can clean up.
- ARK sessions are also copied, redacted and without tokens, into
  `<project>.ark/sessions/internal/`, so a copied project keeps its
  conversations. If the original is no longer on the server, the copy is listed
  as **ARK copy** and can be read and continued as a new session.
- Limits: 4 MB per upload, 256 KB per line (longer lines are replaced by a
  marker), 50 MB per session and 500 MB per project. Server operators can
  change the last three with `ARK_SESSION_LINE_KB`, `ARK_SESSION_MAX_MB` and
  `ARK_SESSION_PROJECT_MAX_MB`.
- **Project export** leaves sessions out unless you tick **Include session
  transcripts** (see [Project export](project-export.md)).

For the API and data model, see
[Session context management](../agents/session-context.md).
