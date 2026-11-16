# ARK Studio Go server (ark-server)

`ark-server` (`go/`) is ARK Studio's production server and CLI. It answers
every route, subcommand and configuration input of the deprecated Python
server (`arkstudio/`, frozen as the reference implementation and rollback
path) byte for byte, and runs on the same data folders. New backend features
are written in Go only. Running it in place of Python, data compatibility
and rollback: [switch-over.md](switch-over.md). Concurrency design and load
measurements: [concurrency.md](concurrency.md).

## Layout

| path | what |
|---|---|
| `go/cmd/ark-server` | the server and the CLI (every `python -m arkstudio` subcommand, see "Command line"), plus `routes [--json]`, `version`, `demo-agent`, `docs-check [--docs DIR]` |
| `go/cmd/ark-load` | load generator (real sign-ins, weighted read/write mix, process sampling) |
| `go/cmd/webgen` | copies `web/` into the embed folder (`go generate`) |
| `go/cmd/docsgen` | copies the agent docs (`docs/`, include list in `internal/agentdocs`) into their embed folder (`go generate`); served at `GET /api/agent/v1/docs` |
| `go/internal/server` | HTTP pipeline, auth, static files, every route (`routes*.go`), response caching; `contract_gen.go` and `static_gen.go` are generated |
| `go/internal/store`, `sqlitedb` | project database (schema, migrations, reads and writes; `schema_gen.go` is generated); one writer plus a read pool per SQLite file |
| `go/internal/org`, `auth`, `project`, `settings`, `sessions`, `setup`, `gpu`, ... | accounts, projects, agent sessions, setup, GPU queue and the other services |
| `go/internal/pyjson`, `pytext`, `pyurl`, `pyos`, `pyre`, `pyargparse` | Python-compatible JSON, str semantics, urllib parsing, shell helpers, Python 3.10 `re` and argparse, tested against CPython-generated vectors (`testdata/`) |
| `go/internal/limits`, `stream` | bounded subprocess pool, leak-free SSE pattern |
| `go/internal/desktopgate` | loopback gate used by the desktop app |
| `tests/python/contract/` | generators (contract, schema, static payloads, route tables), fixtures, black-box harness, differential replay against Python, load test |

## Build and run

Go 1.27 or newer. Third-party modules: `modernc.org/sqlite` (pure Go
SQLite, so cross-compiling needs no C toolchain) and `golang.org/x/sys`
(Windows registry for content types, as CPython's `mimetypes` reads it; it
was already a dependency of modernc). `modernc.org/libc`, `mathutil`,
`memory` and small helper modules come in transitively. `go.sum` pins them
all.

```sh
cd go
go generate ./...            # copy ../web into the binary (optional for development)
go build ./cmd/ark-server    # ark-server(.exe)
go build ./cmd/ark-load
GOOS=linux GOARCH=amd64 go build ./cmd/ark-server   # cross-compiles, no cgo

./ark-server serve --root ../runtime/projects --host 127.0.0.1 --port 8890
./ark-server routes          # contract ids this build implements
```

`serve` takes the same flags, defaults and environment fallbacks as
`python -m arkstudio serve` (`--config`, `--root`, `--host`, `--port`,
`--auth-user`, `ARK_AUTH_PASSWORD`, `--public-url`, `--expose`,
`--trust-proxy`, ...; see the `serve` rows of [contract.md](contract.md)).
Go-only tuning flags: `--web-root`, `--read-pool`, `--heavy-limit`,
`--max-subprocesses`, `--max-in-flight`, `--request-timeout`.

Without `go generate` only a placeholder is embedded. The server then serves
`web/` from disk: `--web-root`/`ARK_WEB_ROOT`, or a `web/` folder beside the
binary or in a parent of the working directory. The embedded build records
the same asset version Python computes (newest file mtime in hex), so asset
URLs and caches stay valid across a switch between the two servers.

The Go server opens existing data unchanged: project databases (identical
schema, migrations and `journal_mode`), `org.sqlite3`, `.agent-token`,
`.auth-secret` and `.auth-sessions.json`. On an empty root it creates the
first-run project (and the demo) like Python (`--fresh` replay).

For the Tauri shell: `ark-server serve --root <dir> --port <p>` is a single
binary with `web/` embedded, has the same CLI shape as Python, and exits
cleanly on SIGINT/SIGTERM (and on console close on Windows).

Go-only, for the ARK Studio desktop app (`desktop/`): with `ARK_DESKTOP_SECRET`
set (environment only; removed from the process environment at start so
children never see it) `serve` puts `internal/desktopgate` in front of the
app. On the loopback port it then admits only requests carrying the desktop
cookie (set by the one-time HMAC-signed `/__ark_desktop/claim?t=...` URL the
app opens), requests with a server-issued agent or task read token, and
`GET /api/health`; everything else gets 401. With the variable set the server
also shuts down gracefully when its stdin reaches EOF (the app quit or died).
Without the variable nothing changes. Release builds set the version with
`-ldflags "-X main.version=<VERSION>"` (`scripts/release/build-server.sh`;
see [release](../release.md)); a plain build reports `2.0-go`.

## Tests and the backend gate

Before a change is merged or deployed, run the gate from the repository root.
It builds `ark-server` fresh and runs gofmt, `go vet`, the agent-docs
generate/`docs-check`, `go test ./...`, the contract tools, the contract
harness against the new binary and the JS suite:

```sh
scripts/dev/check-backend.sh             # Linux, macOS, Git Bash
scripts/dev/check-backend.sh --legacy    # also the differential replay against the Python server
scripts/dev/check-backend.ps1            # Windows PowerShell (-Legacy)
```

Individual checks:

```sh
cd go && go generate ./... && go vet ./... && go test ./...
python -B tests/python/contract/gen_contract.py --check     # contract inventory is current
ARK_CONTRACT_SERVER=go/ark-server python -B tests/python/contract/harness.py
python -B tests/python/contract/diffreplay.py --go go/ark-server [--auth] [--nodes 2000]
python -B tests/python/contract/cli_diff.py --go go/ark-server
python -B tests/python/contract/loadtest.py --users 600 --nodes 2000 --duration 60
```

`harness.py` is a black-box test of the HTTP contract. `diffreplay.py`
replays the same requests against Python and Go on copies of one fixture and
compares replies byte for byte (after the normalizations listed in its
docstring) and the data left behind; `diffreplay.py --go python` replays
Python against itself to check the normalizations. `cli_diff.py` does the
same for the command line. Generated files (`gen_contract.py`,
`gen_schema.py`, `gen_static.py`, `gen_cli.py`, `route_table.py`) each have a
`--check` mode run by the Python test suite. `go test`
(`server/coverage_test.go`) fails when a contract route can answer
`NOT_PORTED`.

## Adding a route

The contract (`contract.json`, `routes.md`) is generated from the frozen
Python server, so a new route exists in Go only:

1. Implement it in the matching `go/internal/server/routes_*.go` file and
   dispatch it from the handler for its method and prefix (see how
   `agentDocsGet` is called from `routes_views.go`).
2. List it in `GoOnlyRoutes` (`routes_agentdocs.go`) so `ark-server routes`
   reports it with the `goonly:` prefix.
3. Add it to `ROUTES` in `tests/python/contract/go_only.py`: the harness then
   runs cases that name it against Go only, and the replay does not expect
   Python to answer it. Extra members on an existing Python route go in
   `AUGMENTED`, intentional behaviour fixes on one in `FIXES`.
4. Cover it with a Go test next to the handler (it is no longer compared with
   Python), document it under `docs/`, and run the backend gate.

## Route status

<!-- ROUTE-SUMMARY:BEGIN -->

Ported: **253 of 253** endpoint records (393 of 393 method+route pairs). Full list: [routes.md](routes.md).

Dispatcher branches (route probe, `probe.json`): **246 of 246** probe requests answered without `NOT_PORTED` (0 NOT_PORTED).

| area | endpoints | ported | method+route pairs ported |
|---|---:|---:|---:|
| `/api/activity` | 1 | 1 | 2/2 |
| `/api/agent-sessions` | 26 | 26 | 37/37 |
| `/api/agent/token` | 1 | 1 | 2/2 |
| `/api/agent/v1` | 34 | 34 | 53/53 |
| `/api/assistant` | 7 | 7 | 10/10 |
| `/api/auth` | 3 | 3 | 5/5 |
| `/api/blackbox` | 3 | 3 | 4/4 |
| `/api/bootstrap` | 1 | 1 | 2/2 |
| `/api/changes` | 1 | 1 | 2/2 |
| `/api/chat` | 5 | 5 | 7/7 |
| `/api/events` | 1 | 1 | 2/2 |
| `/api/export` | 1 | 1 | 2/2 |
| `/api/gpu` | 11 | 11 | 18/18 |
| `/api/graph` | 3 | 3 | 6/6 |
| `/api/health` | 1 | 1 | 2/2 |
| `/api/import` | 3 | 3 | 5/5 |
| `/api/infohub` | 5 | 5 | 9/9 |
| `/api/links` | 3 | 3 | 3/3 |
| `/api/local` | 1 | 1 | 1/1 |
| `/api/me` | 3 | 3 | 6/6 |
| `/api/media` | 5 | 5 | 6/6 |
| `/api/mobile` | 11 | 11 | 16/16 |
| `/api/open` | 1 | 1 | 1/1 |
| `/api/org` | 21 | 21 | 28/28 |
| `/api/personal` | 5 | 5 | 7/7 |
| `/api/project` | 7 | 7 | 11/11 |
| `/api/projects` | 3 | 3 | 4/4 |
| `/api/providers` | 2 | 2 | 3/3 |
| `/api/remote` | 1 | 1 | 2/2 |
| `/api/resources` | 2 | 2 | 3/3 |
| `/api/revision` | 1 | 1 | 2/2 |
| `/api/search` | 1 | 1 | 2/2 |
| `/api/session-contexts` | 5 | 5 | 7/7 |
| `/api/settings` | 1 | 1 | 2/2 |
| `/api/skills` | 5 | 5 | 10/10 |
| `/api/task-agent/v1` | 13 | 13 | 24/24 |
| `/api/timeline` | 1 | 1 | 2/2 |
| `/api/topics` | 1 | 1 | 2/2 |
| `/api/unit-details` | 1 | 1 | 2/2 |
| `/api/units` | 17 | 17 | 22/22 |
| `/api/users` | 1 | 1 | 5/5 |
| `/api/workspace` | 22 | 22 | 34/34 |
| `/api/worktrees` | 3 | 3 | 4/4 |
| `static, login, downloads` | 9 | 9 | 16/16 |

<!-- ROUTE-SUMMARY:END -->

## Compatibility notes

Where `ark-server` deliberately differs from the Python reference:

* The one-time creator-assignee backfill runs at startup; Python runs it on
  the first request that needs the project. The resulting data are identical.
* `Server` header: `ARKStudio/2.0 Go`.
* Assistant connection test (POST /api/assistant/config/test): a reply body
  that is not JSON gets the fixed text "Expecting value: line 1 column 1
  (char 0)" (Python reports the position of the first JSON syntax error);
  a non-UTF-8 reply gets one fixed message.
* Session uploads: redaction uses `internal/pyre` (backtracking, like Python's `re`) and is about 4x slower than CPython on megabyte chunks. On Windows `Path.glob` in the store is case-insensitive in both servers.
* Agent sessions: a bare program name in a preset command is resolved the way
  CreateProcess does on Windows for Python's `subprocess.Popen` (`.exe` is
  appended, PATHEXT is not applied, the search starts in the server's own
  folder), so `claude` installed as `claude.cmd` fails to start in both
  servers with the same `[WinError 2] ...` text; on POSIX the child's PATH is
  searched like Popen. Exit codes of signalled children are `-signal` like
  Popen. Other OS error texts of a failed start are Go's.
* An artifact download whose file name is not Latin-1 is broken in Python
  (`http.server` cannot encode the Content-Disposition header: the client
  gets a truncated error body); Go sends the UTF-8 header. Not replayed.
* Workbench Search regexes run on `internal/pyre`, a port of Python 3.10's
  `re` checked against 316 CPython vector patterns. A search whose regex
  backtracking exceeds a step budget (200M steps) or nests deeper than 100k
  reports `timed_out`; Python would keep running. `\N{...}` does not accept
  name aliases or named sequences (Python does).
* Editor terminals: the Go server has a PTY backend on Linux only (`/dev/ptmx`,
  the same `posix` backend Python uses). On Windows it reports no PTY and runs
  the stdio terminal, as Python does without the optional pywinpty package.
* The echo (local demo) task preset runs `ark-server demo-agent` (its own
  binary) where Python runs `demo_agent.py` with its interpreter, so the
  provider catalog shows a different `command` and `version` for it.
* Provider `--version` probes and model-list commands share the bounded
  subprocess pool (`--max-subprocesses`); worktree git calls share the git
  pool (8) with node history and the workspace.
* Terminal SSE streams and git calls of the workspace leave the request
  admission pool / share the bounded git process pool (8) with node history.

## Command line

`ark-server` takes every subcommand and flag of `python -m arkstudio`
(`arkstudio/__main__.py`). Parsing is `internal/pyargparse`, a port of the
Python 3.10 argparse parse loop: unique-prefix abbreviations, `--opt=value`,
`--`, nargs `?`/`*`, choices, required options, int/float values, `-h`. It
runs over `cli_spec.json`, a snapshot of the Python parser tree with its
80-column usage and help texts, written by
`tests/python/contract/gen_cli.py`. `--check` mode fails when the Python
parser changes; `test_cli_contract.py` runs it. Error texts, the usage block
and exit code 2 are argparse's. Python's 107 recorded argv vectors (exit
code, stdout, stderr, parsed namespace; `go/cmd/ark-server/testdata`) pass in
`go test ./cmd/ark-server`.

| subcommand | Go |
|---|---|
| `serve` (all flags, `--config`, env fallbacks) | yes. Go-only tuning flags match only when spelled in full, so abbreviations mean what they mean in Python |
| `server-init` | yes (getpass with echo off on Windows and Linux consoles) |
| `new`, `demo` | yes. Demo images come from `demo-assets/` beside `web/`, found like the server finds `web/` |
| `export` (json, markdown, csv, svg, bundle; `--out` file or folder) | yes |
| `agent context` | yes |
| `skills list`, `show`, `interface` | yes (static payloads from `gen_static.py`) |
| `skills brief` | yes (`internal/skillbrief`, 1,501 CPython vectors) |
| `skills setup` (state, summary, questions, discover, draft, confirm, verify) | yes (`setup.Load/StatePayload/SaveDraft/Confirm/RecordVerification`, `cmd/ark-server/cli_skills.go`) |
| `skills render`, `skills validate` | yes, every skill and kind (`internal/skillkit`, 3,024 CPython vectors; `verify_artifact` parses SVGs with an expat-compatible well-formedness check that supports UTF-8 documents only) |
| `session-watch` | yes (`internal/sessionwatch`, compared request for request with `session_watch.run` when `ARK_PYTHON` is set) |
| `remote`, `mobile-tunnel` | yes (ssh/sshfs child processes; Ctrl+C terminates them) |
| `desktop` | no: the pywebview tray window stays in Python (exit 2 with a hint) |
| `routes`, `version`, `docs-check` | Go only |

Output is Python's text stream: `\n` becomes CRLF on Windows (stdout,
stderr and `--out` files). Errors Python catches (`ProjectError`,
`StoreError`, `ValueError` and its subclasses) print `error: ...` and exit
2. Python's uncaught exceptions (exit 1, traceback) become one line,
`module.Class: message`, with exit 1.

Deliberate differences: Go always writes UTF-8, where Python uses the
console or locale code page unless `PYTHONIOENCODING` is set. Help is always
80 columns, where argparse follows `COLUMNS`/the terminal. A bare
`ark-server --root x` is an argparse error, as in Python, so use
`ark-server serve --root x`. The serve banner says "(Go)".
