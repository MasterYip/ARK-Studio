# Go server concurrency design

Target: more than 500 signed-in people active on one ARK server, with the
answers byte-identical to the Python server (`tests/python/contract/diffreplay.py`).

## Why the Python server tops out

`ThreadingHTTPServer` gives every keep-alive connection a thread, but the GIL
lets one of them run Python at a time. Every request also opens several short
`org.sqlite3` connections for the sign-in and permission checks. Measured below:
the Python server uses about 1.2 cores and serves about 16 requests/s with 600
users. Queues grow until the clients time out. Its listen backlog is 5
(`socketserver` default), so bursts of new connections are refused (the load
generator retries sign-ins for that reason).

## Design (as implemented in `ark_studio/server/`)

| concern | mechanism | where |
|---|---|---|
| connections | `net/http`, one goroutine per connection; `ReadHeaderTimeout` 15 s, `IdleTimeout` 120 s, 64 KB header limit | `cmd/ark-server/main.go` |
| admission | `limits.Pool` of `--max-in-flight` (4096) request slots; a request beyond it waits on its context instead of piling up work | `server/handler.go` |
| deadlines | every non-streaming request runs under a `--request-timeout` (30 s) context. Pools and SQLite calls honor it; a timeout answers `503 TIMEOUT` | `server/handler.go` |
| SQLite writes | per project database: exactly one writer connection behind a 1-slot queue. Waiting honors the request context; a started transaction is never cancelled halfway. File mirrors (`logs/activity.jsonl`, `project.json`) are written in commit order while the writer is still held | `sqlitedb/sqlitedb.go`, `store/writes.go` |
| SQLite reads | WAL, read pool of `--read-pool` (default max(8, 2×CPUs)) `query_only` connections. A multi-statement read runs inside one read transaction, so it sees one snapshot, like Python's single connection plus lock. `busy_timeout` 30 s, `synchronous=NORMAL`, `foreign_keys=ON`, an 8 MB page cache per connection (SQLite's 2 MB default re-reads a 10k-unit project through read syscalls on every scan). The schema, migrations and `journal_mode` are exactly Python's (`schema_gen.go` is generated from `db.py`/`org.py`) | `sqlitedb`, `store/db.go` |
| org database | stays in `journal_mode=DELETE` as in Python. Account, role and permission lookups are memoized until the file or this process's write generation changes, which removes 6–8 queries from every request | `org/org.go` |
| heavy reads | `--heavy-limit` (default min(CPUs/2, read-pool/2)) concurrent builds of large payloads, so cheap reads (revision polling, unit detail) always find a free connection | `server/cache.go` |
| request coalescing | units list + facets, graph snapshot, graph, timeline and the project/home payload go through a response cache keyed by project, request target, database state and UTC date (and the actor when the query uses `assignee`). A request reuses a body only if it was built for the exact state the request saw on arrival, or if its build started after the request arrived, so it never misses a write that finished before it was sent. One build per key at a time. gzip is computed once per body | `server/cache.go` |
| graph snapshot | node JSON is cached per unit. Writes by this process log the units they touched; a comment write changes only that node's comment counts, so the next snapshot re-encodes those nodes only. Each write records the file stamp it started from, which makes the log a verifiable chain: a write that does not start from the expected stamp (another process changed the files) breaks the cache and forces a full build. The write hook never waits for a build (separate locks) | `store/snapshot.go` |
| child processes | `limits.Pool.Run`: bounded slots (`--max-subprocesses`), timeout, capped output, own process group and whole-tree kill on cancel (`taskkill /T` on Windows, `kill(-pgid)` elsewhere). Python spawns git, agent CLIs and ssh per request without a bound; every ported subprocess feature must use this | `limits/` |
| streaming | `stream.ServeSSE` runs the loop in the handler goroutine (nothing spawned per connection). It ends on client disconnect, on `Hub.Close` (registered with `http.Server.RegisterOnShutdown`) or at end of data, and every write has a deadline. Subscribers have bounded buffers; slow ones are dropped. A test connects 300 clients and checks that no goroutine or subscriber is left and that shutdown does not wait for open streams | `stream/` |
| shutdown | SIGINT/SIGTERM → `Shutdown` (10 s for in-flight requests) → streams closed → databases closed | `cmd/ark-server/main.go` |
| startup work | the one-time v5 creator-assignee backfill (a scan of `activity` per unassigned unit; seconds on a 10k-node project) runs before the listener opens. Python runs it inside the first request, which blocks every request that arrives meanwhile | `server/app.go` |

Opt-in profiling: `ARK_PPROF=127.0.0.1:6061` serves `net/http/pprof` on a
separate loopback listener (non-loopback addresses are refused).

## Problems the load test found (all fixed, with tests where practical)

1. **Connection-pool deadlock.** `VisibleProjectIDs` called `OrgID()` (a pooled
   read) while holding a pooled read. With 600 first requests every connection
   was held by an outer read: 99% errors at the 30 s deadline.
   `org_test.go` reproduces it (fails without the fix, passes with it).
2. **Path resolution per request.** `filepath.EvalSymlinks` on Windows cost
   about 10% of CPU. Open projects are now cached by the reference too.
3. **First-request backfill stall.** It caused a ~6 s p99 on every endpoint of
   the 10k-node project. It now runs at startup.
4. **Facet counts.** Python runs one full-scan `COUNT` per tag namespace. The
   port computes the same numbers in one pass (89 ms → 24 ms on 2k nodes;
   Python takes 58 ms). Excerpt regexes run only when their literal trigger is
   present (checked against 4,884 CPython vectors).
5. **Windows `project.json` sharing violation.** Python rewrites `project.json`
   (tmp + rename) on every write. A concurrent directory scan can fail to read
   it and the project then "disappears" for that request (1 in 35,752 requests,
   a 501 from the not-ported tutorial fallback). Already open projects now use
   their in-memory manifest. Python has the same race.

Phase 2 (snapshot hot spot):

6. **Write hook behind the snapshot lock.** The first incremental snapshot
   took its mutex inside the write hook while full builds held the same
   mutex, so every comment waited for a snapshot build (comment POST p50
   6 ms → 155 ms on 2k units, 13 s on 10k). Fixed with separate locks; a
   concurrent writers/readers test compares the result with a full build.
7. **Cold page caches.** A warm 8 MB page cache matches a memory map in a
   single-request benchmark, but under load 32 read connections each keep a
   cold private cache (10k units: 314 req/s). A memory map on the read-only
   connections shares the OS page cache instead.
8. **A stalled run with the memory map on every connection.** One 10k run
   (with the CPU profiler attached) stalled: 33 req/s, 18% errors, 622 OS
   threads, idle CPU. The cause was not isolated; the writer was then left
   unmapped, since on Windows a mapped file cannot be truncated by another
   connection. Runs after that change are reported below.
9. **Comparisons across fixtures.** The phase 2 fixture has six topics with
   type rules, which every unit detail (`topics_of`) and every hydrated topic
   evaluates, in Python too. Deltas below therefore compare the phase 1 and
   phase 2 binaries on the same fixture.

## Load test

`python -B tests/python/contract/loadtest.py --users N --nodes M --duration 60 --think 1.0`
builds one fixture (`tests/python/contract/fixture.py`: the demo project plus M
generated units, N members with a write grant, PBKDF2 at 1000 iterations so
sign-in is cheap) and gives each server a byte-identical copy. Then
`ark_studio/server/cmd/ark-load` drives the server:

* every user signs in through the real `/login` form (CSRF token, PBKDF2) and
  keeps its own cookie jar and keep-alive connections, like a browser tab;
* each user then loops: weighted request, then think time drawn from an
  exponential distribution with a 1 s mean (capped at 5 s). The mix is 25%
  units list (slim; facets on, off, search, status filter), 20% unit detail,
  20% revision poll, 8% home payload (needs-you), 7% graph snapshot, 7%
  activity, 5% comments, 8% comment POST (writes);
* the server process tree is sampled every 0.5 s for CPU time, working set and
  threads (a Windows venv `python.exe` is a launcher with a child interpreter;
  both are counted).

Machine: Intel i7-10875H (8 cores / 16 threads), 15.9 GB RAM, Windows 10. The
load generator runs on the same machine. Both servers are on loopback with
account sign-in enabled. Errors are non-200 answers or transport errors; the
client timeout is 60 s.

| dataset | server | users | requests in 60 s | req/s | p50 ms | p95 ms | p99 ms | errors | CPU cores avg / peak | RSS MB avg / peak | threads peak |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 2,095 units | Python | 600 | 1,300 | 16.5 | 30,188 | 52,955 | 60,001 | 3.31% (43 client timeouts) | 1.17 / 1.59 | 109 / 119 | 607 |
| 2,095 units | Go | 600 | 35,752 | 595.7 | 6.4 | 103.5 | 157.1 | 0 | 6.76 / 10.74 | 123 / 136 | 33 |
| 10,095 units | Python | 600 | 1,021 | 9.1 | 48,024 | 60,001 | 60,001 | 22.53% (230 client timeouts) | 1.43 / 2.10 | 120 / 178 | 607 |
| 10,095 units | Go | 600 | 30,040 | 496.7 | 24.2 | 871.8 | 1,157.5 | 0 | 8.95 / 12.42 | 262 / 296 | 49 |
| 2,095 units | Go | 1,500 | 72,740 | 1,207.7 | 165.4 | 697.9 | 2,117.6 | 0 | 10.96 / 12.44 | 232 / 265 | 804 |

The offered load at 600 users with a 1 s think time is about 600 req/s. Go
serves all of it on 2k units and 83% of it on 10k units. On 10k units the
graph snapshot (8.9 MB JSON, 0.35 MB gzipped; 1.9 MB on 2k units) and the home payload dominate: p50
914 ms and 422 ms, while unit detail stays at 25 ms and revision polling at
3 ms. Python serves 2.8% (2k) and 1.5% (10k) of the offered load.

Rerun `loadtest.py` to reproduce the per-endpoint numbers.

### Phase 2: snapshot hot spot, same machine and mix

The phase 2 fixture adds topics with type rules, media and org data, so the
phase 1 binary was rebuilt from git (`e77806d`) and rerun on the phase 2
fixtures. All rows below use the phase 2 fixtures (2,101 and 10,101 units).
Another agent was running test suites on the same machine during these runs;
repeated runs of one binary varied by about ±10%.

| dataset | server | req/s | p50 ms | p95 ms | p99 ms | errors | snapshot p50 | units list p50 | home p50 | comment POST p50 | CPU avg | RSS MB avg / peak |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 2k | Python | 17.8 | 29,537 | 42,106 | 47,895 | 0 | 29,994 | 29,675 | 29,652 | 30,298 | 1.19 | 110 / 120 |
| 2k | Go, phase 1 code | 579.9 | 19.0 | 179.7 | 254.0 | 0 | 187.0 | 66.7 | 81.6 | 6.8 | 8.51 | 152 / 178 |
| 2k | Go, phase 2 | 601.1 | 6.3 | 47.4 | 68.8 | 0 | 11.9 | 23.5 | 30.8 | 5.7 | 5.14 | 113 / 125 |
| 10k | Python | 9.0 | 46,371 | 60,001 | 60,001 | 22.3% (client timeouts) | 44,022 | 44,180 | 60,000 | 60,000 | 1.38 | 115 / 153 |
| 10k | Go, phase 1 code | 332.9 | 195.8 | 3,213.5 | 4,479.2 | 0 | 1,870.0 | 1,572.0 | 1,897.6 | 7.3 | 10.00 | 302 / 356 |
| 10k | Go, phase 2 | 540.4 | 25.9 | 469.4 | 816.3 | 0 | 64.0 | 229.0 | 281.6 | 8.3 | 9.62 | 300 / 345 |

Deltas, phase 2 vs phase 1 code on the same fixture: on 10k units
throughput +62% (333 → 540 req/s), p50 −87%, p99 −82%, snapshot p50
1,870 → 64 ms (−97%), units list −85%, home payload −85%. On 2k units the
offered load (~600 req/s) was already served; p99 254 → 69 ms, snapshot p50
187 → 12 ms, CPU 8.5 → 5.1 cores.

What changed: incremental snapshot nodes (40-second counter sample at 10k:
728 patches, 81 cache hits, 3 full rebuilds), one query per related table for
full builds, slice-backed JSON objects, and memory-mapped reads with a larger
page cache.

## Write mix, 600 and 1,500 users

Integrated tree after the coverage audit (253/253 endpoints), plus the three
writer fixes below (reported runs: build 4bed219; 23b47e5 only adds the
idle retry of the WAL truncation). Same machine as above (i7-10875H,
8 cores / 16 threads, 15.9 GB, Windows 10), load generator on the same
machine, about 4.5 GB free RAM and 10-12% background CPU before each run
(editor, browsers, other agents idle). One server at a time.

Dataset: `fixture.py` with `--nodes 2000` (2,102 live units, the fixture topics and the
demo seed), N members with a write grant, built once per run and copied
byte for byte for each server. `loadtest.py --duration 90 --think 1.0
--write-pct 15`: real `/login` sign-ins, then per user a weighted request
and an exponential think time (1 s mean). Writes are 14.8% of all Go requests (12.1% of Python's 1,808 at 600 users):
comment POST (40% of writes), node edit (GET then PATCH title/body/status/
tags, 30%), topic canvas layout save (PATCH members with x/y, 15%), topic
member add/remove (15%). Client timeout 60 s. Errors = non-2xx (409 counted
apart) or transport errors.

| server | users | req/s | p50 ms | p95 ms | p99 ms | read p50 / p95 / p99 ms | write p50 / p95 / p99 ms | writes OK | errors | CPU cores avg / peak | RSS MB avg / peak |
|---|---:|---:|---:|---:|---:|---|---|---:|---|---:|---:|
| Python | 600 | 16.5 | 32,153 | 45,639 | 50,167 | 32,080 / 45,589 / 50,554 | 33,020 / 45,965 / 48,446 | 218 | 0 | 1.18 / 1.75 | 125 / 135 |
| Go | 600 | 583.7 | 24.4 | 223.5 | 1,416 | 23.0 / 227.5 / 1,487 | 30.1 / 208.9 / 1,074 | 7,807 | 0 | 10.63 / 13.90 | 167 / 222 |
| Python | 1,500 | 23.9 | 60,000 | 60,001 | 60,001 | all client timeouts | all client timeouts | 0 answered (43 comments committed after the client gave up) | 99.97% (2,999 client timeouts, 11 refused connections) | 1.03 / 1.25 | 339 / 422 |
| Go | 1,500 | 667.7 | 63.1 | 8,309 | 9,294 | 40.4 / 1,815 / 7,274 | 6,296 / 9,269 / 9,557 | 9,539 | 0 | 11.61 / 14.69 | 249 / 303 |

Per operation (Go, p50 / p99 ms): at 600 users comment POST 13 / 1,276,
node PATCH 36 / 561, layout save 42 / 1,654, unit detail 21 / 1,283,
revision poll 3 / 1,260, units list 68 / 1,742, graph snapshot 200 / 1,467.
At 1,500 users every write type has a p50 of 6.2-7.7 s, reads stay at
3-110 ms p50 except the snapshot (350 ms).

SQLite writer contention (Go, `ark_sqlite` deltas over the measured 90 s):

| users | project writes | queued | queue wait p50 / p95 / p99 | max wait | max depth | mean hold | busy / locked errors | org db writes (sign-ins), wait p99 |
|---:|---:|---:|---|---:|---:|---:|---:|---|
| 600 | 11,140 | 73% | 10 / 100 / 200 ms | 236 ms | 30 | 5.65 ms | 0 / 0 | 599, 35 ms |
| 1,500 | 9,800 | 96% | 9.6 / 9.6 / 9.6 s (histogram bucket) | 9.6 s | 922 | 8.33 ms | 0 / 0 | 1,499, 42 ms |

No SQLITE_BUSY, no SQLITE_LOCKED, no 409, no server timeouts, no 5xx in any
run. The writer is one connection behind a FIFO queue, so contention shows
up only as queue wait. At 600 users the offered load (about 600 req/s) is
served and a write waits 10 ms at the median. At 1,500 users the offered
load is about 1,500 req/s; the machine (server ~11.6 cores, the load
generator the rest of the 16 threads) serves 668 req/s. Reads degrade
gracefully, but the single project writer saturates at about 100 writes/s
under that read load (mean hold 8.3 ms, plus the wake-up latency of the
next waiter on a saturated CPU; idle, one writer manages about 245
writes/s with 32 closed-loop clients), so the write queue holds up to 922
requests and writes wait 6-10 s. Every write still succeeds.

Integrity after each run: `PRAGMA integrity_check` = ok on every project
and org database (both servers); the comment rows added equal the
successful comment POSTs exactly (Go 3,166 and 3,830, Python 92); the
`project.json` keys are identical between the two servers apart from
`updated`.

### Problems this test found (fixed)

10. **Unbounded WAL growth.** In the first 1,500-user run the project WAL
    reached 419 MB for a 5.6 MB database (Python: 3.4 MB). The read pool
    always has a transaction open, so SQLite's automatic PASSIVE checkpoint
    copies frames back but the WAL never starts over; the file then keeps
    its peak size (`journal_size_limit` is unset, as in Python). Python's
    single connection never reads while it writes, so it does not have
    this. The writer now tries `wal_checkpoint(TRUNCATE)` every 64 commits
    once the WAL is past 16 MiB, without waiting, and retries once a second
    after a failed attempt: under sustained load the attempt fails cheaply
    (WAL max 397 MiB at 600 users, 445 MiB at 1,500), and it resets the
    file to 0 bytes at the first quiet moment (131 MB → 0 within 3 s of the
    end of a 600-user run). `TestWALTruncatedAfterLoad`. A first version
    that waited 100 ms for readers failed 102 of 108 times and cost about
    10 s of writer time per 90 s; it was replaced.
11. **Writer hold time.** Two things done while holding the writer:
    the `project.json` rewrite (create + rename, ~40% of a small write's
    CPU on Windows) now happens only when no other writer is queued,
    otherwise the next writer or a flusher writes it, so the file still
    ends with the last write's manifest (sequential requests, and therefore
    the replays, rewrite it exactly as before; black-box snapshots and
    Close flush first; `TestManifestRewriteUnderQueuedWriters`); and the
    snapshot log's after-write stamp is now read on the writer connection
    instead of a read-pool connection, which first waited for a free
    connection and then reloaded every page the write had invalidated.
    Idle closed-loop write throughput 196 → 245 writes/s. A/B at 600 users
    against the audit build (two runs each, same conditions): same
    throughput (566 / 558 vs 566 / 571 req/s, the offered load), write p95
    630-670 → 240-250 ms, reads within run-to-run noise.

Not fixed (design limits, measured): SQLite's page cache in modernc is
built with `SQLITE_ENABLE_MEMORY_MANAGEMENT`, so all connections share one
global page-cache mutex; under load the writer's page fetches wait behind
32 readers on it (goroutine dumps of the writer showed it in
`pcache1Fetch`/`pcache1Truncate`). Raising write throughput beyond ~100/s
under heavy reads needs a different SQLite build (cgo `mattn/go-sqlite3`,
or a modernc build without memory management), batching several requests'
writes into one transaction (a behaviour change Python does not have), or
splitting projects across processes/hosts.

## Known limits and next steps

* modernc SQLite (pure Go) runs scan-heavy queries about 2× slower than
  CPython's C SQLite (facets without the one-pass rewrite: 105 ms vs 58 ms).
  At 10k units the remaining hot spots are the units list with facets and
  the home payload (p50 229 and 282 ms under load); both are rebuilt after
  every write. They could follow the snapshot's write log the same way.
  Under load, SQLite's page-cache mutex is the top contended lock. Options:
  `mattn/go-sqlite3` (cgo, needs a C toolchain per target) or more
  pre-aggregation in the store.
* The response cache still recognizes writes by other processes through the
  WAL/database file size and mtime, which Windows updates lazily for open
  files (the snapshot cache moved to `PRAGMA data_version` for this reason).
  Run one server per data directory.
* One process per data directory, like Python. Horizontal scale needs a shared
  writer and cache invalidation, which this design does not attempt.
* Snapshot payloads grow linearly with the project. At 10k units a single
  snapshot is 8.9 MB uncompressed (0.35 MB gzipped). Clients that poll it should poll
  `/api/revision` first (the web UI already does).
