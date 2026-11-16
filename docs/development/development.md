# Development Guide

> **The backend is Go.** Production runs `ark-server` (`ark_studio/server/`), and every
> backend change is made in Go only.

The backend is one Go binary, `ark-server` (Go 1.27+, pure Go SQLite, no cgo).
The frontend is served from `ark_studio/web/` with no bundler or npm install; release
builds embed `ark_studio/web/` and the agent docs into the binary.

## Repository Layout

| Path | Responsibility |
| --- | --- |
| `ark_studio/server/cmd/ark-server` | the server and CLI (`serve`, `new`, `export`, `skills`, `session-watch`, `docs-check`, ...) |
| `ark_studio/server/internal/api/...` | HTTP handler packages, one per domain (parse, authorize, encode) |
| `ark_studio/server/internal/server` | request pipeline (host check, sign-in, device gate, project scope, same-origin), router tables, the handlers not yet moved to `api/` |
| `ark_studio/server/internal/<domain>` | domain packages (store, org, sessions, workspace, gpu, setup, agentdocs, ...) |
| `ark_studio/server/internal/platform/*`, `ark_studio/server/internal/compat/*` | clock, processes, files; Python-compatible JSON, text and regex where replies must stay byte-identical |
| `ark_studio/web/js/`, `ark_studio/web/css/`, `ark_studio/web/vendor/` | browser views, styles and vendored browser dependencies |
| `tests/js/` | JavaScript tests |
| `tests/python/contract/` | black-box contract suite for ark-server (harness, golden replay) |
| `scripts/`, `ark_studio/android/`, `docs/` | launch helpers and smoke checks, Android clients, documentation |
| `runtime/` | local generated data; keep it out of contributions |

The layering, the dependency rules and the restructuring plan are in
[go-port/architecture.md](go-port/architecture.md).

## Build and Run

```sh
cd ark_studio/server
go build -o ark-server ./cmd/ark-server            # ark-server.exe on Windows
./ark-server serve --port 8891 --root ../../runtime-dev/projects --sessions-root ../../runtime-dev/agent-sessions
go generate ./...                                  # embed ark_studio/web/ and the agent docs (release builds)
GOOS=linux GOARCH=amd64 go build ./cmd/ark-server  # cross-compiles, no C toolchain
```

Without `go generate` the binary serves `ark_studio/web/` from disk (`--web-root`, or a
`ark_studio/web/` folder beside the binary or in a parent of the working directory). Use a
separate port and data directories so tests do not touch a working project; the
server creates starter projects unless `--no-demo` is set.

## Checks (deploy gate)

One command runs every backend check and is the deploy gate:

```sh
scripts/dev/check-backend.sh            # Linux, macOS, Git Bash
scripts/dev/check-backend.ps1           # Windows PowerShell
```

It runs `gofmt`, `go vet ./...`, `go test ./...` and `ark-server docs-check`,
builds a fresh `ark-server`, then runs the black-box contract suite against that
binary alone (no Python server; `import arkstudio` is blocked):

* `tests/python/contract/harness.py` with `ARK_CONTRACT_SERVER=<binary>`:
  hand-written HTTP cases on the recorded fixtures (`fixtures/*.zip`).
* `tests/python/contract/golden.py --go <binary>`: the differential replay's
  read and write requests (about 9,900, signed out and signed in) compared with
  the recorded goldens (`goldens/*.json.gz`), plus the resulting databases and
  files. It needs `go` on PATH (it compiles the fake ssh of the GPU steps) and
  is recorded from the gate's build (`go generate ./internal/agentdocs` before
  `go build`).

and finally the JS suite (`node --experimental-vm-modules --test tests/js/`).
`--quick` / `-Quick` skips the golden replay. A Python 3.10+ interpreter is
only the test runner (`ARK_PYTHON` picks one).

While working, run the narrow checks first:

```sh
cd ark_studio/server && go test ./internal/store/... ./internal/api/...
python -B tests/python/contract/golden.py --go ark_studio/server/ark-server.exe --mode local --only unit-writes
node --experimental-vm-modules --test tests/js/module_parse.test.mjs
```

## Changing the Backend

Adding or changing a route:

1. Domain logic goes into the domain package (`ark_studio/server/internal/<domain>`), with
   table-driven Go tests. Domain and store packages never import `net/http`,
   `internal/api` or `internal/server`; `internal/archtest` fails `go test`
   otherwise (rules in [architecture.md](go-port/architecture.md#4-interfaces-and-dependency-rules)).
2. The HTTP handler goes into `ark_studio/server/internal/api/<domain>`: it gets an `api.Ctx`
   (query, JSON body, project and permission checks, replies) and returns
   `*api.Error` for error replies. Test it with `api/apitest.Ctx`.
3. Register it in `ark_studio/server/internal/server/router.go`, in the dispatch table of the
   right phase (`getRoutes`, `postRoutes`, ...; the order inside a table
   decides overlapping paths), with `exact`, `match`, `prefix` or `sub`, and
   declare its route id. `ark-server routes` lists the registrations.
4. A route the frozen Python server does not have is Go-only: add its id to
   `GoOnlyRoutes` (`ark-server routes` prints it with a `goonly:` prefix) and
   to `ROUTES` in `tests/python/contract/go_only.py`. Extra members on an
   existing reply go into `AUGMENTED`; a deliberate behaviour change on an
   existing route (a bug fix) goes into `FIXES` with a narrow predicate, so the
   legacy differential replay reports it as an intentional divergence instead
   of a mismatch.
5. Add harness cases for the new behaviour and run `golden.py`: it lists every
   reply that changed. When exactly the intended replies changed, re-record
   with `golden.py --go <binary> --record` and commit the goldens with the
   change.
6. Generated files stay generated: run `go generate ./...` and the generator
   named in the file header; never edit `*_gen.go` by hand.

Schema changes go through the `ark_studio/server/internal/store` migrations and only add
columns or tables, so the previous binary keeps reading the data; see the
rollback notes in [switch-over.md](go-port/switch-over.md).

## Browser Checks

Inspect screenshots as well as assertions for layout changes, including narrow
screens and both themes. Keep generated runtime files and private project
content out of commits.

The optional HTML-renderer browser regression uses Playwright with a temporary
static server and no project data:

```sh
npm install --no-save playwright
npx playwright install chromium
node --test tests/browser/rich_html.test.cjs
```

Set `ARK_TEST_BROWSER` to use an existing browser executable instead. This test
checks raw HTML, Markdown and references together, workspace URL resolution,
media controls, inline colours, literal code, and removal of executable markup.

## Frontend Changes

Keep shared behavior in the existing graph, inspector and rich-content modules.
Put user-visible strings in both language dictionaries in `ark_studio/web/js/i18n.js`.
Update the explicit app-shell asset list and bump `CACHE_VERSION` in `ark_studio/web/sw.js`
when changing the browser shell. The cache tests guard asset completeness and
keep API responses out of the offline cache.

## Reporting and Contributing

File issues and pull requests in the
[source repository](https://github.com/MasterYip/ARK-Studio). Include the observed
and expected behavior, reproduction steps, platform and relevant sanitized logs.
For a pull request, explain the resulting behavior and list checks performed.
Keep contributions focused, preserve existing user data and include regression
coverage for behavior that could otherwise break unnoticed.

Provider and GPU tests should use fixtures or mocked commands; do not require
real model credentials or submit work to a shared GPU queue during routine tests.
