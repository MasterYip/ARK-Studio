# Development Guide

> **Deprecation notice.** The Python server (`python -m arkstudio serve`) is
> deprecated in favour of `ark-server` (`go/`, see
> [go-port/README.md](go-port/README.md)). It answers every route byte for byte
> like Python and runs on the same data; Python stays runnable as the reference
> for the parity checks and as the rollback path
> ([switch-over.md](go-port/switch-over.md)). New server features are still
> written in Python first and then ported, until the Python code is retired.

ARK's core server requires Python 3.10+ and the standard library. Its frontend
is served directly from `web/`, with no bundler or npm install. Optional desktop
and Windows PTY dependencies are listed in `requirements-desktop.txt` and
`requirements-pty.txt`.

## Repository Layout

| Path | Responsibility |
| --- | --- |
| `go/` | `ark-server`: the production server and CLI, byte-compatible with `arkstudio/` |
| `arkstudio/` | Deprecated Python server (parity reference): CLI, HTTP API, SQLite storage, agents and execution adapters |
| `desktop/` | Tauri desktop app that bundles `ark-server` |
| `web/js/` | Browser views, shared graph logic and components |
| `web/css/` | Workbench, mobile and entry-screen styles |
| `web/vendor/` | Vendored browser dependencies and their notices |
| `tests/` | Python and JavaScript regression tests |
| `scripts/` | Launch helpers, release tooling, audits and layout benchmarks |
| `android/` | Android clients and build configuration |
| `docs/` | Setup, integration and development documentation |
| `runtime/` | Local generated data; keep it out of contributions |

## Isolated Development Server

Use a separate port and data directories so tests do not affect a running
research workspace:

```sh
python -m arkstudio serve --port 8891 --root ./runtime-dev/projects --sessions-root ./runtime-dev/agent-sessions
```

The server creates starter projects unless `--no-demo` is set. Do not point
destructive or load tests at a working project database.

## Checks

The backend gate (use it before a deploy) builds `ark-server` fresh and runs
gofmt, `go vet`, the agent-docs generate/`docs-check`, `go test ./...`, the
contract harness against the new binary, and the JS suite; it exits non-zero
on any failure and prints a summary:

```sh
scripts/dev/check-backend.sh             # Linux/macOS/Git Bash
scripts/dev/check-backend.sh --legacy    # also the differential replay against the frozen Python server
```

```powershell
scripts/dev/check-backend.ps1            # Windows PowerShell (-Legacy as above)
```

Set `ARK_PYTHON` to choose the Python interpreter used as test runner.

Run the suites relevant to the change, followed by broader checks when shared
behavior or contracts change:

```sh
cd go && go generate ./... && go vet ./... && go test ./... && cd ..
python -B -m unittest discover -s tests
node --experimental-vm-modules --test tests
```

Individual Python suites can be selected with `-p`, for example:

```sh
python -B -m unittest discover -s tests -p test_static_cache.py
node --experimental-vm-modules --test tests/js/module_parse.test.mjs tests/js/sw_cache.test.mjs
```

Keep generated runtime files and private project content out of commits.

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
Put user-visible strings in both language dictionaries in `web/js/i18n.js`.
Update the explicit app-shell asset list and bump `CACHE_VERSION` in `web/sw.js`
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
