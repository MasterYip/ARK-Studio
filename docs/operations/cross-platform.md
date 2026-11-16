# Cross-platform notes

ARK Studio runs on Windows, Ubuntu/Linux and macOS with the same single-binary
Go server (`ark-server`) and the same vanilla ES-module workbench. It is a small local web
app, not an all-in-one document editor: ARK keeps the graph, planning and task
API in charge, and hands individual resources to the operating system's default
application when you ask it to.

## Requirements

- `ark-server` for your platform (release package or desktop app), or Go 1.27+
  to build it from `ark_studio/server/` (no cgo, no npm build).
- A modern browser for the workbench UI.
- A desktop session if you want **Open externally**. The server itself also runs
  headless; only the "open in the OS" action needs a graphical session.
- Linux: `xdg-open` (usually from `xdg-utils`) or `gio`; macOS: the built-in
  `open` command; Windows: the shell's default-app association with an
  `explorer` fallback.

## Start

```sh
cd <your ARK Studio checkout>
cd ark_studio/server && go generate ./... && go build ./cmd/ark-server && cd ../..
ark_studio/server/ark-server serve --port 8890     # ark-server.exe on Windows
```

Then open <http://127.0.0.1:8890/> in the default browser. On the first run
ARK creates a demo project under `runtime/projects/` (relative to the working
directory). The desktop app starts the same server for you.

The server binds to loopback by default. Keep it there. Setting `--host
0.0.0.0` is possible for a trusted LAN but is outside the supported security
model.

## Data, paths and permissions

```text
runtime/
  projects/                 project folders (<name>.ark/), ignored by git
    .agent-token            generated write token, never commit it
  agent-sessions/           private task-session workspaces, ignored by git
    <session-id>/
      meta.json
      workspace/
        artifacts/
```

- Create the project root somewhere your user can read and write. On Linux/macOS
  avoid `sudo`; a root-owned runtime folder breaks later saves.
- Project folders are portable: copying the whole `<name>.ark/` folder copies
  the project.
- Task sessions always live outside the project folder and are started with the
  session workspace as their current directory. The OS user running ARK owns
  both; the app never asks for administrator/root privileges.
- Case-sensitive filesystems (typical on Linux) are fine: media rows keep the
  exact path spelling. Avoid moving a project between case-sensitive and
  case-insensitive disks with names that differ only by case.
- Paths with spaces are supported because the server launches argv lists, not a
  concatenated shell string.
- Do not put the project or session root on a network share that changes
  symlink/permission semantics.

## Open a resource in the OS default app

The UI keeps its browser previews. Next to them it now exposes additive
**Open externally** / **Open in default app** actions:

- **Inspector → Media**: the external-link icon on a media row. Remote
  `http(s)` media opens in the default browser; local project files open in the
  OS default application.
- **Tasks → Sessions → Artifacts**: the external-link icon on a file row opens
  that workspace file with the OS handler.
- **Production → report/artifact cards**: **Open externally** is shown when the
  unit has a local media file; the rendered report reader stays available.

The HTTP route behind them is a normal page-origin write:

```http
POST /api/open
Content-Type: application/json

{ "unit": "F-0005" }
{ "session": "s_20261030120000abcdef", "path": "artifacts/report.svg" }
{ "path": "assets/figure.pdf" }
{ "url": "https://example.org/paper" }
```

- `unit` opens the unit's first local media file. If it has no local file the
  response is a clear `400`; remote-only media is not silently opened.
- `session` + `path` resolves inside that session's workspace and reuses the
  same artifact checks as the browser (absolute paths, `..` and escaping
  symlinks are rejected).
- `path` is project-relative. Absolute paths, Windows drive paths, UNC paths,
  `..` traversal and symlink escapes are rejected. A local `file://` URL is
  accepted only when it resolves inside the active project.
- `url` must be `http` or `https` and is opened in the default browser.
- The action returns a structured JSON result (`opened`, `kind`, `target`,
  `platform`, `opener`, `mode`). Failures return a clean `400`/`403`/`404` with
  a message the UI shows as a toast.

### Security model

- A task-agent **read token** can never call `/api/open`; requests carrying one
  are rejected with `403`. Read tokens remain GET-only under
  `/api/task-agent/v1/...`.
- `POST` still passes the page-origin check used by the other UI writes:
  cross-origin browser writes are rejected.
- Only the active project folder and the task-session workspace are openable.
  There is no arbitrary "open any path on the machine" route.
- ARK never uses `shell=True` for these actions. It invokes fixed argv lists:
  - Windows: the default-app association (`ShellExecute`, reported as opener `os.startfile`), falling back to `explorer path`.
  - macOS: `open path`.
  - Linux/Ubuntu: `xdg-open path`, falling back to `gio open path`.
  - URLs: the platform's default browser.
- Headless Linux (no `DISPLAY` / `WAYLAND_DISPLAY`) returns a clear error rather
  than pretending the file opened. On a remote SSH host, the default app opens
  on the **server's** desktop, not on your laptop.
- Opening is read-only for ARK: the OS handler may of course edit the file, just
  as opening it from a file manager would.

## Shell and process behavior

Task sessions are owned by the server and stopped without killing ARK:

- Windows: `taskkill /PID <pid> /T /F` (existing behavior).
- POSIX: the session is started in its own process group; ARK sends `SIGTERM`,
  waits briefly, then escalates to `SIGKILL`. It refuses to signal its own
  process group.

The local-shell preset is selected at runtime:

| Platform | Interactive default (prompt on stdin) | Command mode |
|---|---|---|
| Windows | `powershell -NoLogo -NoProfile -Command -` | `cmd.exe /d /s /c <command>` |
| macOS/Linux | `$SHELL -lc 'exec <shell> -l'` (fallback `/bin/sh`) | `$SHELL -lc <command>` (fallback `/bin/sh -lc`) |

The POSIX interactive form uses `-lc` and re-executes the login shell, so the
task-session stdin pipe (which carries the initial prompt) stays attached.

`ARK_TASK_SHELL_COMMAND` overrides the preset on any platform. The other
presets (`dsh`, `claude`, `codex`, custom) are ordinary argv commands and are
not Windows-specific.

### Project terminal (Editor › Terminal view)

The project terminal (the Editor's Terminal view, formerly Tasks → Files) reuses the platform shell but is a separate,
bounded command runner with an optional interactive PTY:

| Platform | PTY backend | Dependency |
|---|---|---|
| Windows | none: the stdio runner (raw `cmd.exe /d /s /c` command line) | - |
| Linux | `/dev/ptmx` PTY, `setsid`/`TIOCSCTTY`, `TIOCSWINSZ` | none |
| macOS | none: the stdio runner | - |

PTY mode keeps the raw UTF-8 byte stream (ANSI escapes included) and sends it
through the existing polling/SSE entries; the browser renders it with a small
ANSI screen model (SGR colour/bold/underline, clear, carriage-return redraw,
cursor addressing and the alternate screen). Columns and rows come from the
drawer's font metrics and a `ResizeObserver`;
`POST /api/workspace/terminals/{id}/resize` applies them to a live PTY. PTY Enter
is `\r`, which canonical tty input maps to a newline for line-oriented programs
and raw-mode TUIs receive as the Enter key.

When `pty=true` is requested but the backend is missing or ConPTY fails to
start, ARK starts the old stdio subprocess instead and reports
`pty_requested=true`, `pty=false`, a `pty_fallback` reason and
`pty_available=false` in `/api/workspace/info`. Nothing fails open or silently
pretends a PTY exists; hosts without a PTY backend run the full stdio terminal. On Windows the command is passed as one raw command line to
`cmd.exe /d /s /c` (the standard `subprocess` argv quoting escapes embedded
quotes with backslashes, which `cmd.exe` does not understand and which silently
breaks commands such as `python -c "print(1)"`). On POSIX the command is one argv element passed
to `$SHELL -lc`; ARK never interpolates paths or other values into the command
string. Each command has a 1–120 s timeout, a 256 KiB output cap, optional
stdin and a cancel action that stops the process tree with the same
`taskkill` / `SIGTERM`→`SIGKILL` policy as task sessions (PTY sessions are
session leaders, so the whole group is signalled). The command runs with the
project folder as its cwd, but this is not an OS sandbox: it runs as the same
user and can leave the folder if it chooses to.

## Third-party apps are intentional

ARG does not embed a PDF reader, image editor, video player, Office suite or
code editor. Those are deliberately left to the applications you already use.
ARK's job is the project graph, planning and the local/agent task API; the
default-app action is the handoff. This keeps the app small and portable, and
means the "right" application for a `.docx`, `.mp4` or `.ipynb` is whatever your
OS is configured to use.

## Use ARK as a third-party integration

The same HTTP server is the integration surface. Read the write token from the
terminal that printed it or from `runtime/projects/.agent-token` (do not commit
that file):

```sh
TOKEN=$(cat runtime/projects/.agent-token)

# Project context for an external agent
curl -s http://127.0.0.1:8890/api/agent/v1/context \
  -H "Authorization: Bearer $TOKEN"

# Create a unit through the same write path the UI uses
curl -s -X POST http://127.0.0.1:8890/api/agent/v1/units \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title":"From another app","type":"note"}'

# Hand a project file to the OS default application
curl -s -X POST http://127.0.0.1:8890/api/open \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"path":"assets/figure.pdf"}'
```

Task sessions get a separate, read-only token in `ARK_READ_TOKEN`:

```sh
curl -s http://127.0.0.1:8890/api/task-agent/v1/context \
  -H "X-ARK-Read-Token: $ARK_READ_TOKEN"
```

Read endpoints are `GET /context`, `/project`, `/units`, `/links`, `/snapshot`
and `/session`. Every non-GET under `/api/task-agent/v1` is `403`, and that read
token is also refused by `/api/open` and by the write API. See
[`external-agent.md`](../agents/external-agent.md) and the README's Agent API section for
the broader tool contract.

## Troubleshooting

- **"no default-application opener is available"**: install `xdg-utils`
  (provides `xdg-open`) or `gio`, then restart ARK. On a minimal server image
  there may be no MIME handler at all.
- **"no graphical session is available"**: you are on headless Linux/SSH. Use a
  desktop session or copy the file and open it locally.
- **macOS `open` does nothing**: check that the file type has a default app in
  Finder's Get Info panel.
- **Windows opens a folder instead of the document**: Windows has no registered
  handler for that extension; use `explorer` to pick one.
- **A path is rejected**: only files inside `runtime/projects/<name>.ark/` or
  `runtime/agent-sessions/<id>/workspace/` are allowed. Absolute paths from
  another folder, `..`, and symlinks pointing outside are intentionally refused.

Windows was the verification host for this slice; the macOS `open` path and the
Linux `xdg-open`/`gio` paths are covered by unit tests with mocked platform
selection and process launchers, not by a live desktop run.
