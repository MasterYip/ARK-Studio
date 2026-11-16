# ark-server target architecture (restructuring plan)

Branch `refactor/go-architecture`, started from `go-switch-candidate-1`
(50be96a). Goal: turn the port, which was written module by module against
the Python sources, into a codebase organised by domain with a thin HTTP
layer, a store split by aggregate and explicit dependency rules. The
behaviour stays byte-compatible: the differential replay, the route probe,
the CLI diff and the contract harness must show 0 differences after every
step. This document is written before any code moves; the progress log is in
[README.md](README.md#restructuring-refactorgo-architecture--resume-here).

## 1. Current state (measured at 50be96a)

| | lines |
|---|---:|
| Go source (excluding tests and generated files) | 79,302 |
| tests | 3,998 |
| generated | 2,326 (plus embedded data files) |

46 packages (3 commands, 43 under `internal/`). The largest:

| package | source | tests | files | test coverage |
|---|---:|---:|---:|---:|
| `server` | 13,106 | 233 | 47 | 1.9% |
| `store` | 9,738 | 363 | 25 | 22.7% |
| `sessions` | 5,637 | 69 | 12 | 20.1% |
| `gpu` | 4,892 | 470 | 14 | 45.6% |
| `skillkit` | 4,318 | 147 | 10 | 74.9% |
| `setup` | 3,810 | 91 | 8 | 34.6% |
| `org` | 3,732 | 87 | 10 | 10.0% |
| `workspace` | 2,589 | 135 | 11 | 38.1% |
| `chat` | 2,336 | 0 | 11 | 0% |
| `providers` | 2,213 | 0 | 3 | 0% |

22 of 46 packages have no Go test at all (among them `auth`, `chat`,
`infohub`, `mobile`, `nodehist`, `project`, `providers`, `search`,
`worktrees`). Parity is proven almost entirely by the Python-driven replay.

Main problems:

1. **The HTTP package is a monolith.** `server` has 47 files and 259 methods
   on one `reqCtx`. Requests are dispatched by a cascade
   (`apiGetViews` → `servicesGet` → `settingsGet` → `clientCacheGet` → a
   `switch` on the path in `routes.go`, with similar chains for POST and
   PATCH/DELETE). The `Ported` list is maintained by hand in `routes.go`
   plus 12 `init()` appends. Handlers carry domain logic:
   `routes_mobile.go` is 1,372 lines, `routes_sessions.go` 832 and
   `routes_sessionctx.go` 629.
2. **`store` is a hub, not a layer.** Besides SQLite access it holds:
   - validation;
   - read models (`filters`, `overview`, `dashboard`, `snapshot`);
   - exports (`bundle.go`, 1,125 lines);
   - the assistant (348) and chat rows;
   - infrastructure that 18 other packages import for reasons unrelated to
     the database:
     - the clock: `UTCNow`, `Clock`, `ReplayClock` and `LastReplayClock`,
       used by blackbox, gpu, org, project, setup, skillkit, workspace,
       mobile, worktrees and sessions;
     - `TextFile` (CRLF-aware file writing), used by 5 packages;
     - the Python exception model (`Error`, `ValueError`, `InternalError`),
       used by assistant, chat, infohub, resources and workspace;
     - Python value helpers (`PyTypeName`, `PyInt`, `PyEqual`).

   Every change to `store` rebuilds everything, and it cannot be split while
   it is the shared utility package.
3. **Duplicated helpers**, each written per module during the port:

   | helper | copies |
   |---|---|
   | `hideWindow` | 4 packages, 8 files |
   | `killTree` | `limits`, `workspace` |
   | `osErrorText` | `chat`, `gpu`, `sessions` |
   | `setCmdline` | `chat`, `gpu` |
   | `exitCode` | `sessions`, `workspace`, `cmd/ark-server` |
   | `UTCNow` | 4 (`providers` and `worktrees` read the wall clock, `store` the replay clock) |
   | `pyInt` | 5 |
   | `pyFloat` | 5 (next to `pytext.Float`) |
   | `expandUser` | 5 |
   | `readText` | 4 |
   | `samePath` | 4 |
   | `pyTypeName` | 4 |
   | `parseISO` | 3 |
   | `truthy` | 3 (next to `pyjson.Truthy`) |
4. **Dead code.** A name-reference scan finds little: `workspace.SamePath`
   and `var _ = filepath.Join` in `app.go`. A real `deadcode`/`staticcheck`
   pass is step 1.4, below.
5. **Tests depend on Python.** The 12-case black-box harness is the only
   suite that runs against `ark-server` alone.

Import graph today (internal packages only):

- `cmd/ark-server` imports 14 packages.
- `server` imports 38.
- `store` imports `nodehist pydiff pyjson pytext pyurl rnd sqlitedb`.
- `skillkit` imports `gpu` and `setup`.
- `resources` imports `mobile org sessions`.
- `org` imports `settings` and `auth`.

There are no cycles, but the direction is not designed.

## 2. Target layout

```
ark_studio/server/
  cmd/ark-server      main(): flag parsing only, calls internal/cli
  cmd/ark-load, cmd/webgen
  internal/
    compat/           Python-compatibility leaves (section 5)
      pyjson pyre pytext pyurl pyos pydiff pyargparse mimetypes
    platform/         infrastructure without domain knowledge
      clock           wall clock + replay clock (X-Replay-Clock), UTCNow, ISO parse/format
      pyerr           Error / ValueError / InternalError / TypeName: the Python
                      exception model that error replies are built from
      textfile        CRLF/universal-newline text I/O
      pathx           pathlib rules: SamePath, ExpandUser (added in step 1.3)
      procx           bounded process pool (today limits), hideWindow, setCmdline,
                      killTree, exitCode, osErrorText
      sqlitedb        one writer + read pool per file (unchanged), WAL control
      stream          leak-free SSE/NDJSON writer
      rnd blake2b qr webui
    store/            persistence, split by aggregate (section 3)
      core            Open, schema_gen, migrations, Tx, writer, change log, revision,
                      manifest (project.json), snapshot cache, data_version
      units links media topics comments history chat activity
      views           read models: filters/facets, graph snapshot, timeline,
                      overview, dashboard, home payload
      exports         json/markdown/csv/svg, Markdown bundle, project archive
      orgdb           org.sqlite3 + agent tokens schema (today in org/store)
    <domains>         business rules; no net/http
      units           create/patch/archive/restore/delete/insert-between, tags, needs, members
      graph           graph, snapshot, timeline, activity, revision (read side)
      topics          topic design, members (x/y/w), default topics
      comments        comments, resolve
      history         node text history (nodehist), checkpoints, pending-edit worker
      projects        project manager, lifecycle, seeds/demo, blackbox, archive import
      org             users, teams, grants, audit, linux accounts, profiles/avatars
      auth            sign-in, sessions, CSRF, agent and personal tokens, SecretBox
      settings        project settings, personal settings, effective settings, workbench prefs
      sessions        agent sessions, transcripts, demoagent, session context store,
                      internal mirrors, secret redaction
      providers       provider catalog, presets, assistant config, worktrees
      workspace       files, changes/diff, terminals, host open
      search          node search, workspace grep, file-text cache
      events          change cursor, deltas, SSE event broker (today clientcache.go)
      gpu             config, probe, queue, dispatch
      resources       resources graph, device prefs
      setup           environment setup, layers, skillkit, skillbrief
      chat            chat, assistant dispatch/apply/extract, InfoHub import
      mobile          pairing, device sessions, connection info
    api/              HTTP layer
      (core)          Router, Ctx (today reqCtx), pipeline (Host allow-list, sign-in,
                      device gate, project scope, same-origin), request parsing,
                      replies and error mapping, response cache/coalescing, static
                      files, contract table (contract_gen.go)
      units graph topics comments history projects org auth settings sessions
      providers workspace search events gpu resources setup chat mobile
                      one handler package per domain: parse, authorize, call
                      the domain, encode; each exports Register(*api.Router, Deps)
    cli/              ark-server subcommands (today cmd/ark-server/*.go and
                      sessionwatch); uses domains, never api
```

Where today's packages go:

| today | target |
|---|---|
| `server` handlers (`routes_*.go`, `views.go`, `authroutes.go`, `tokens.go`, `media.go`) | `api/<domain>` |
| `server` core (`handler.go`, `pipeline.go`, `respond.go`, `cache.go`, `static.go`, `mimetypes*`, `routes.go` matching) | `api` |
| `server/clientcache.go`, `events_sessions.go` | `events` + `api/events` |
| `server/modulegraph.go` | `api` (static) |
| `store` (25 files) | `store/core`, the aggregates, `store/views`, `store/exports`; validation goes to the domains; `store/assistant.go` → `chat`; `tags.go` → `units` |
| `limits` | `platform/procx` |
| `nodehist` | `history` (git plumbing stays in `history/git`) |
| `project`, `blackbox` | `projects` |
| `org`, `linuxacct` | `org` + `store/orgdb` |
| `auth` | `auth` |
| `settings`, `assistcfg` | `settings` (assistcfg → `providers`) |
| `sessions`, `sessionctx`, `demoagent`, `secretredact` | `sessions` (subpackages `transcript`, `contexts`, `redact`, `demoagent`) |
| `providers`, `worktrees` | `providers` (+ `providers/worktrees`) |
| `workspace`, `hostopen` | `workspace` (+ `workspace/terminal`) |
| `chat`, `assistant`, `infohub` | `chat` (+ `chat/infohub`) |
| `setup`, `skillkit`, `skillbrief` | `setup` (+ `setup/skillkit`, `setup/brief`) |
| `sessionwatch` | `cli/sessionwatch` |
| `pyjson`, `pyre`, `pytext`, `pyurl`, `pyos`, `pydiff`, `pyargparse` | `compat/*` |

## 3. Store layer

`store/core` owns the connection pair (one writer, a read pool), the
schema and migrations, and the write transaction:

```go
// store/core
type DB struct { /* sqlitedb handles, snapshot cache, manifest */ }
func (db *DB) Read(ctx context.Context, fn func(q Querier) error) error
func (db *DB) Write(ctx context.Context, who Who, fn func(tx *Tx) error) error
// Tx appends to change_log, bumps change_counter, marks project.json
// dirty and notes the write for the snapshot cache: the four
// side effects every write path must have today (Store.noteWrite).
```

Each aggregate package (`units`, `links`, `media`, `topics`, `comments`,
`history`, `chat`, `activity`) contains only SQL and row mapping over
`Querier`/`*Tx`. It exposes a repository struct that a domain declares as
an interface:

```go
// domain side (comments)
type Repo interface {
    List(ctx context.Context, q core.Querier, unit int64) ([]Comment, error)
    Insert(tx *core.Tx, unit int64, c NewComment) (Comment, error)
    Resolve(tx *core.Tx, unit, id int64, by string) (Comment, error)
}
```

A write that touches several aggregates (a unit create with links, members
and a comment) is composed in the domain service inside one `db.Write`, so
transaction boundaries and the order of SQL statements stay as in Python.
That order decides `change_log` ids and `activity.jsonl` lines, and the
replay compares both. Read models (`store/views`) may join across tables;
they are read-only and are used by `graph` and the overview.

## 4. Interfaces and dependency rules

Allowed imports (an arrow means "may import"):

```
cmd/*   -> cli, api, platform, compat
cli     -> domains, store/*, platform, compat
api     -> domains, platform, compat              (api/core: platform, compat only)
api/<d> -> api, domain d (and domains it reads), platform, compat
domain  -> other domains (acyclic, listed), store/*, platform, compat
store/* -> store/core, platform, compat
platform/* -> compat, stdlib (sqlitedb: database/sql + modernc)
compat/*   -> compat/pytext only, stdlib
```

- No domain or store package imports `net/http`, `api` or `cli`.
- Domains talk to each other through small interfaces declared by the
  consumer. For example, `resources` declares `type Devices interface`
  instead of importing `mobile` wholesale; `skillkit` stops importing `gpu`
  and `setup` directly.
- `store/*` packages never import a domain.
- Global mutable state is limited to `platform/clock` (replay clock) and
  `api` (the router table). The `init()` appends to `Ported` disappear: the
  route list printed by `ark-server routes` is derived from the router's
  registrations.

Enforcement: `internal/archtest/arch_test.go` runs `go list -deps -json` on
`./...`, maps each package to its layer and fails on a forbidden edge. It
starts with an allowlist of today's violations, and every refactoring step
must shrink the allowlist, never grow it. Once `server` is gone the
allowlist must be empty.

HTTP routing: `api.Router` resolves a request to a contract id with today's
`contractMatch` (contract order equals Python's dispatch order), then calls
the handler registered for `(id, method)`. Dispatchers keep a second-level
key switch inside their handler (skills, setup, history `action`, export
`format`, settings `scope`), exactly the keys `replay_probe.py` enumerates.
Unregistered ids answer through the single `unknownOrNotPorted` fallback,
as today; `coverage_test.go` keeps checking it.

## 5. Python-compatibility helpers

| helper | required, client-visible | not needed (stdlib internally) |
|---|---|---|
| `pyjson` | every JSON reply; every JSON/JSONL file Python also reads or writes (`project.json`, `meta.json`, `.auth-sessions.json`, `assistant.json`, `activity.jsonl`, session mirrors, ZIP manifests); key order, float repr, raw UTF-8; request bodies (duplicate keys, NaN, decode error texts) | values a domain only passes around can become typed structs encoded by `pyjson` at the edge. `*pyjson.Object` stays where payloads are dynamic dicts (JSON columns, settings, meta). New internal state (pools, caches, router) uses no pyjson |
| `pyre` | user regexes (Workbench Search, workspace grep); patterns Python applies to user text where the match decides output (secret redaction, GPU output parsers, setup checks) | fixed patterns on ASCII input: stdlib `regexp` (route matching already uses `pytext.MustCompile` on `regexp`). Secret redaction (fixed patterns, ~4× slower on pyre) is a candidate for stdlib `regexp` once the 658 CPython vectors pass on it |
| `pytext` | code-point slicing and lengths, `str.isspace/isdigit/upper/lower` tables, `int()`/`float()` parsing of request values, CRLF rules | internal string work |
| `pyurl` | request parsing: `urlparse`, `parse_qs`, `unquote(errors='replace')`, POST routing on the undecoded path | building internal URLs (`net/url`) |
| `pyos` | `shutil.which`, `shlex.split/quote`, `list2cmdline` wherever the command line is shown or decides which program runs (sessions, providers, setup, chat DSH) | fixed internal git invocations: `exec.Command` with an argv |
| `pydiff` | `difflib.unified_diff` output (workspace diff, node history diff) | - |
| `pyargparse` | CLI usage, help and error texts | - |
| `mimetypes` (`server/mimetypes*`) | `Content-Type` of media and static files, as CPython reads the host | - |
| `blake2b` | hashes that appear in URLs and files (avatars, workspace) | - |
| `platform/pyerr` | error reply texts (`str(exc)` and exception class decide status and body) | internal errors use Go `error` and are mapped at the edge |

Rule: a `compat` helper may be removed from a code path only when the value
never reaches a reply, a file or a log Python also writes. The replay proves
each such change.

## 6. Generated files (stay generated)

| file | generator |
|---|---|
| `server/contract_gen.go` (→ `api/contract_gen.go`) | `tests/python/contract/gen_contract.py` |
| `store/schema_gen.go` (→ `store/core`) | `gen_schema.py` |
| `server/static_gen.go`, static payload templates, mimetypes tables | `gen_static.py` |
| `pyargparse/cli_spec.json` | `gen_cli.py` |
| `pytext/upper_gen.go`, `skillkit/assets_gen.go` | `gen_skillkit.py` |
| `project/seeds_gen.go` | `gen_seeds.py` |
| `pyre/names.txt.gz` | `ark_studio/server/internal/pyre/gen_names.py` |
| `webui/dist` (embed) | `go generate ./...` (`cmd/webgen`) |
| `routes.md`, README route summary, `probe.json` | `route_table.py`, `replay_probe.py --write` |
| `config-parity.md` | `config_parity.py` |
| `agentdocs/dist` (embed) | `go generate ./...` (`cmd/docsgen`; `ark-server docs-check` verifies) |
| `tests/python/contract/fixtures/*.zip` | `record_fixtures.py` (once per fixture change) |
| `goldens/*.json.gz` (not exported: record them with `golden.py --record`) | `golden.py --record` (with every intended behaviour change) |

Moving a generated file means updating its generator's output path and its
`--check` mode in the same commit. Generated files are never edited by hand.

## 7. Testing strategy

Since the switch-over the backend is Go only and the Python server is frozen
(reference and rollback path). The test layers, primary first:

1. **Go-native unit tests**, table-driven, one `_test.go` per domain, store
   and `api/*` package, written when the package is moved:
   - validation messages and bounds;
   - SQL round trips on a temporary database (`store/*`);
   - authorization matrices (owner/member/viewer/agent/device);
   - process fakes for sessions, providers, workspace and GPU (the fakes
     from the replay's sealed agent environment, in Go).

   Target: every package except generated and `cmd` has tests; at least 60%
   statement coverage for domains and `store/*`, and 40% for `api/*`, where
   the e2e suite does the rest.
2. **Black-box contract suite against ark-server alone** (no Python server;
   `noarkstudio.py` makes `import arkstudio` fail):
   - `harness.py` with `ARK_CONTRACT_SERVER=<binary>`: hand-written HTTP
     cases on the recorded fixtures (`fixtures/{local,auth}.zip`, built once
     with `fixture.py` by `record_fixtures.py`);
   - `golden.py --go <binary>`: the differential replay's request sequences
     (read and write, signed out and signed in; about 9,900 replies) against
     one ark-server, each reply compared in diffreplay's canonical form
     (N1-N21 plus G1-G3: the run's work folder, the host fingerprint,
     loopback ports) with `goldens/{local,auth}.json.gz`, then the data dir
     with the recorded state dump (statediff S1-S12; `statediff` opens sealed
     values with its own stdlib opener). The replay clock starts at a fixed
     instant (2030-01-01) so ids and stamps repeat; recording runs twice and
     stores replies that differ between the runs as unstable (not compared).
     Bodies over 2 KiB and state entries over 4 KiB are stored as SHA-256.
     Step groups and calls that need the Python code (environment setup
     records) are skipped and listed.
   - Intended behaviour change: Go change + Go unit test, `golden.py` lists
     exactly the changed replies, then `golden.py --record` and commit the
     goldens with the change.
3. **Deploy gate:** `scripts/dev/check-backend.{sh,ps1}`: gofmt, vet,
   `go test ./...`, `docs-check`, a fresh build, the harness and the golden
   replay against it, the JS suite (`--quick` skips the goldens, `--legacy`
   adds the differential replay).
4. **Legacy parity tools** (regression checks against the frozen Python
   behaviour, not the gate): the differential replay (`diffreplay.py`, all
   modes), the route probe, the CLI diff, `--prod-copy`. Intentional Go-only
   behaviour is declared in `go_only.py`: `ROUTES` (new routes), `AUGMENTED`
   (extra reply members, stripped before comparing) and `FIXES` (behaviour
   fixes on existing routes; a narrow predicate over the reply pair turns a
   mismatch into a listed *intentional divergence*).
5. **Every refactoring step runs:** `go build`, `go vet ./...` (also
   `GOOS=linux`), `go test ./...`; the quick replay of the touched modules
   (`diffreplay.py --only ...` and/or `golden.py --only ...`) plus the route
   probe; `cli_diff.py` when `cmd`/`cli` is touched; `test_contract_inventory`
   when generators move.
6. **At each milestone:** the gate script; replays local, `--auth`,
   `--owner`, `--fresh`, `--handoff` and `--nodes 2000`, run one at a time;
   the full CLI diff.

## 8. Migration order and milestones

Every step is a small commit that moves or extracts code without changing
output. A replay mismatch means the step is wrong: revert it rather than
adjusting the replay.

**Milestone 1: foundations and leaf domains** (progress: README, "Restructuring"; 1.1-1.3 and the dead-code pass done)

1. **Step 1.1, next:**
   - add `internal/archtest` with today's import graph as the allowlist;
   - extract `platform/clock`, `platform/pyerr` and `platform/textfile` out
     of `store`, leaving forwarding aliases in `store` that are removed in
     step 1.3;
   - add Go tests for each.
2. **Step 1.2:** `platform/procx` (from `limits`) and remove the duplicate
   `hideWindow`/`killTree`/`osErrorText`/`setCmdline`/`exitCode`. Settle
   `UTCNow` on `clock` (`providers` and `worktrees` read the wall clock
   today; the replay pins both, so this must keep their current behaviour,
   with a test).
3. **Step 1.3:**
   - move `py*` packages to `compat/*`;
   - dedupe `pyInt`/`pyFloat`/`truthy`/`pyTypeName`/`expandUser`/
     `readText`/`samePath`/`parseISO` onto `compat` or `platform`;
   - drop the `store` aliases.
4. **Step 1.4:**
   - `api` core: `Router` and `Ctx`, with `Ported` derived from
     registrations;
   - move the existing handlers unchanged into `api/<domain>` files that
     still sit in one package, so the dispatch cascade disappears without
     handler changes;
   - run the dead-code pass (`deadcode`/`staticcheck` via
     `go run pkg@version` outside the module, so `go.sum` stays unchanged).
5. **Step 1.5:** move the leaf domains out with their handlers and tests:
   `gpu` (done), `resources` (done), `search`, `workspace` (+ `hostopen`),
   `chat` (+ `assistant`, `infohub`). Handler packages implement a
   `Handlers` struct over `api.Ctx` (internal/api). `mobile` moves with
   `auth` in milestone 3: its handlers use the sign-in and device-session
   state of the pipeline throughout.
6. Full milestone verification; report.

**Milestone 2: the store split and the content domains**

1. `store/core` (DB, Tx, writer, change log, manifest, snapshot cache).
2. The aggregates: `units`, `links`, `media`, `topics`, `comments`,
   `history`, `chat`, `activity`. Then `store/views` and `store/exports`.
3. The domains `units`, `graph`, `topics`, `comments`, `history` with
   their `api/*` packages; `events` (change cursor, deltas, SSE).
4. `projects` (`project`, `blackbox`, seeds, archive).
5. Full milestone verification, plus a 600-user load spot check (the
   writer path changed); report.

**Milestone 3: accounts, agents, CLI and cleanup**

1. `auth`, `org` (+ `store/orgdb`, `linuxacct`), `settings`.
2. `sessions` (`sessionctx`, `demoagent`, `secretredact`), `providers`
   (`assistcfg`, `worktrees`).
3. `setup` (`skillkit`, `skillbrief`).
4. `cli` (from `cmd/ark-server` and `sessionwatch`); `cmd/ark-server`
   reduced to `main()`.
5. Delete `server`, empty the archtest allowlist, final dead-code pass, and
   the golden e2e suite as the primary suite.
6. Final verification: everything in section 7 plus the load test
   (section 9); report.

Main keeps moving: merge main at least between steps, port Python changes
into the new layout, and re-record `probe.json`. Fixes to the switch
candidate (for example the personal-settings secret repair) land on
`feat/go-backend` first and are then carried over here.

## 9. Performance guard

Before and after, on the same machine and dataset, re-run the 600-user
write-mix load test (`loadtest.py --users 600 --nodes 2000 --duration 90
--write-pct 15`; see [concurrency.md](concurrency.md#write-mix-600-and-1500-users)).
The baseline from `go-switch-candidate-1`:

| | req/s | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| all requests | 583.7 | 24 ms | 224 ms | 1,416 ms |
| writes | | 30 ms | 209 ms | 1,074 ms |

0 errors, and a writer queue wait of 10 / 100 / 200 ms (p50 / p95 / p99).

Acceptance: no errors, throughput within 5%, p95 and p99 within 10%. A run
at the end of milestone 2 catches regressions from the new transaction
plumbing early. Interface indirection must stay out of row loops: an
aggregate returns slices, not per-row callbacks through interfaces.
