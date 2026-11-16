# Client Project Cache

ARK keeps project data in the browser so pages, node details and tab
switches render at once on slow links, then revalidate in the background.
Writes still go to the server immediately; there is no offline editing.

## Behaviour

- **What is cached.** GET responses listed in `web/js/client-cache-model.js`
  (`ROUTES`): bootstrap, project, overview, setup state, workspace info,
  assignees, graph snapshot and bodies, unit lists, unit details, comments,
  topics and topic pages, timeline, activity, `/api/me` and effective
  settings; for Execute and the Editor side panels also the agent-session
  list, presets and groups, a session's detail and transcript, the
  session-context list, the GPU board, the resources graph, worktrees, and
  the workspace tree and Changes list. These "volatile" routes change without
  a project revision: they are always shown from the cache and revalidated
  (never served as fresh), and views that show live data paint the cached
  copy, then the server's (`cachedThenFresh`); their polls ask the server
  directly. Session logs/artifacts, terminals, chat, file contents,
  organization admin, auth, assistant configuration and search are never
  cached.
- **Keys.** `v<CACHE_VERSION>.<server cache_schema>|<origin>|<cache_id>|p:<project>|<route>`.
  `cache_id` is an opaque per-account id from the server; routes are
  normalized (sorted query, no `mobile_token`/`ark_project`).
- **Serving.** A cached entry is served without a request when the revision
  stream is live, the entry was checked after the stream connected and no
  delta marked it stale (aggregates such as bootstrap are also re-checked
  after 60 s). Otherwise the cached data renders at once and a conditional
  request (`If-None-Match`) runs in the background; unchanged data costs a
  304. A read within 10 s after this tab's own write waits for the server.
  A newer response fires `ark:cache-updated`; the view refreshes in place.
  Identical in-flight reads share one request.
- **Snapshot deltas.** The graph snapshot is loaded without bodies
  (`?bodies=0`); bodies load in the background or before a text filter. Both
  revalidate through `GET /api/changes?since=<cursor>`, merged locally
  (`mergeSnapshotDelta`); a `reset` falls back to a full reload. A delta also
  marks touched unit details and every list/aggregate stale.
- **Push.** `GET /api/events` (SSE) replaces the 3.5 s poll. Each `revision`
  event triggers a delta pull. If the stream never opens three times (a
  proxy blocks it), the old `/api/revision` poll runs instead.
- **Editing.** Views are not refreshed while an input has focus, a popup or
  the inspector is open; the change stays pending. Clean inspector and Editor
  node tabs take newer data in place (scroll kept); an Editor tab with edits
  shows the existing "changed elsewhere" conflict banner.
- **Prefetch.** After first paint, at idle: entry data of the other
  workspaces (only when nothing is cached yet), and details of likely next
  units (selected unit's neighbours, Needs you, recently opened, visible
  units) in one `GET /api/unit-details` request. Hovering a unit card, row
  or graph node for 150 ms prefetches it. With `saveData` or a 2g
  `effectiveType` only neighbours are prefetched and the workspace prefetch
  is skipped.
- **Offline.** A request that gets no response puts the tab "offline"; a
  502/503/504/52x without an ARK JSON error body (a proxy such as Cloudflare
  answering) puts it "server unreachable". Both keep cached data on screen
  read-only with a banner (`web/js/connectivity.js`). Any response ends the
  state; so does the health probe (`GET /api/health`, no-store), which runs
  with backoff from 2 s doubling to 30 s and at once on the browser's
  `online`/`offline` events, when the tab becomes visible or gains focus, and
  before a write (a write is refused only if that check also fails).
  `navigator.onLine` is never trusted by itself. On recovery the banner
  clears, the event stream is reopened and cached views revalidate. An event
  stream that the browser closed for good (a proxy's 502/524 to
  `/api/events`) is reopened with the same backoff; three streams that never
  open fall back to polling. An offline start uses the account this browser
  last confirmed.
- **Storage.** IndexedDB `ark-client-cache-v<CACHE_VERSION>`; bodies over
  16 KB are gzip-compressed with `CompressionStream`. Default cap 200 MB:
  least recently used projects are evicted first, the current project last
  (details before its snapshot). Settings › Account shows the size and has
  **Clear cached data**.
- **Images.** The service worker caches `GET /api/media/<id>/raw` image
  requests cache-first per account (`ark-media-<cache_id>`, at most 400
  entries / 80 MB, 6 MB per image), revalidating each hit in the
  background; 401/403/404 delete the entry.

## Privacy

- Cached data renders only after the server confirmed the account
  (`/api/mobile/session` → `cache_id`), never from a guess. Entries of any
  other account on the same browser profile are deleted when another account
  is confirmed (account switch).
- Sign-out (`api.logout`), `401 AUTH_REQUIRED` and the stream's `auth`
  event delete every entry and the worker's media caches.
- A revoked grant drops that project's entries: a 403/404/409 project error,
  a reply served for another project (`X-ARK-Project-Id`), or the stream's
  `denied` event.
- Agent sessions and session context can be private to one person. They are
  stored only as the server returned them to this account (the server
  filters by owner/shared/admin), under that account's `cache_id`, and are
  deleted with the rest on sign-out, account switch (another `cache_id` is
  confirmed) and revoke. A 403/404/410 for a single cached route (a session
  deleted or made private by its owner) removes that entry only; the project
  cache is dropped only for `PROJECT_NOT_ACCESSIBLE` / `NO_PROJECT_ACCESS`
  or a reply served for another project. Logs, artifacts and terminals are
  never stored.
- Offline start is the one case without server confirmation: it shows the
  account last confirmed on this browser profile, read-only, until sign-out.

## Server Contract (for other server implementations)

- **Validators.** Every `GET /api/*` JSON 200 reply carries a weak `ETag`
  of its bytes; `If-None-Match` (weak comparison, lists and `*`) answers
  `304` with the same `ETag` and no body. `/api/graph/snapshot`,
  `/api/graph/bodies` and `/api/units` use a revision-keyed validator
  (schema, project, account, permission, request path+query, revision)
  checked before the body is built. `/api/project/overview` excludes
  `timing`, `generated_ms` and `store_ms` from its validator.
- **`X-ARK-Project-Id`** names the project a JSON reply was served from.
- **Identity.** `session.cache_id` (bootstrap) and `cache_id` +
  `cache_schema` on `/api/mobile/session` (`null` before sign-in):
  `sha256(<per-server salt>:<lowercase username>)[:20]`, the salt stored in
  `<projects root>/.client-cache-salt`. `bootstrap.server.cache_schema`
  (currently 1) changes when a cached payload shape changes.
- **`GET /api/graph/snapshot?bodies=0|1`** adds `cursor` and `bodies`;
  `bodies=0` omits `search_body`.
- **`GET /api/graph/bodies`** → `{cursor, bodies: [[id, body], ...]}` (id order).
- **`GET /api/changes?since=<cursor>&bodies=0|1`** →
  `{cursor, reset, reason?, units, deleted, edges, touched, bodies}`.
  `units` are snapshot nodes, `deleted` ids left the snapshot, `edges` are
  all snapshot edges incident to a `touched` unit. Client rule: drop
  `deleted` and upserted ids, add `units`, sort by `updated_at` desc then id
  desc; drop edges touching a touched id, add `edges`, sort by id.
  `touched` = changed units, their link neighbours (both directions), units
  whose inherited owner is a changed unit, descendants (part_of) of an
  assignee or part_of change, and every topic. `reset: true` (`reason`
  `cursor` or `size`) for an unknown/foreign/future/pruned cursor, more than
  50,000 log rows or more than 2,000 touched units.
- **Cursor** = `<meta.change_log_id>.<max change_log.seq>`.
- **`GET /api/unit-details?ids=a,b,...`** (ids or uids, at most 60) →
  `{units, missing, cursor, etags}`; `units[i]` equals `GET /api/units/<id>`
  `.unit`, and `etags[id]` is the `ETag` that GET would send.
- **`GET /api/events`** (`ark_project`/`mobile_token` query like media
  URLs): `text/event-stream`, `Cache-Control: no-cache, no-transform`,
  `X-Accel-Buffering: no`, never compressed. Sends `retry: 3000`, then an
  `event: revision` (`id: <cursor>`, data `{revision, cursor, project}`) on
  connect and after each change (checked every 1 s); `: keep-alive` comments
  every 20 s; access re-checked every 10 s: `event: auth` (session ended) or
  `event: denied` (grant revoked), then close; after 600 s `event: reconnect`
  and close. Every 3 s it also compares the agent sessions this account sees
  in the project (ids and statuses, filtered like `GET /api/agent-sessions`)
  and sends `event: sessions` (data `{token, project}`, `token` an opaque
  hash) on connect and whenever it changes; clients mark their cached session
  lists stale.
- **`GET /api/revision`** adds `cursor`.
- **Media** `GET /api/media/<id>/raw`: `ETag "f<mtime_ns hex>-<size hex>"`,
  `Last-Modified`, `Cache-Control: private, no-cache`; `If-None-Match` →
  304 (authorization still runs first).
- Paired phones may call `/api/changes`, `/api/events` and
  `/api/unit-details`.
- **Database schema v11.** Table `change_log(seq INTEGER PRIMARY KEY
  AUTOINCREMENT, kind TEXT, ref INTEGER, tree INTEGER DEFAULT 0)` filled by
  triggers (`arkstudio/db.py`, `CHANGE_LOG_SCHEMA`): units insert/update/delete
  (`tree` when `assignee` changes); unit_links (`link` row plus both ends,
  `tree` on the source of a part_of link); topic_members (topic and member);
  unit_tags, unit_media, unit_comments (their unit). `meta.change_log_id` is
  random per database file and regenerated after a snapshot restore. The
  newest 20,000 rows are kept (pruned every 500 writes).

## Measurements

Headless Edge benchmark (account mode, synthetic
projects with varied ~1.6 KB Markdown bodies; fast3g = 1.5 Mbps / 300 ms RTT,
slow3g = 400 kbps / 2 s RTT). Seconds until the view's content is visible;
"before" is `222ecac`. Local HTTP/1.1 server, one run each.

| Scenario | 2k fast3g | 10k fast3g | 2k slow3g | 10k slow3g |
| --- | ---: | ---: | ---: | ---: |
| Cold start, Home | 10.9 → 10.6 | 18.7 → 17.6 | 58.0 → 55.6 | 85.6 → 65.9 |
| Warm reload, Home | 3.1 → 1.5 | 10.4 → 6.3 | 11.2 → 3.7 | 47.1 → 6.4 |
| Reload after another user's edit: page / edit shown | 2.8 / 2.8 → 1.5 / 3.4 | 10.3 / 10.3 → 6.0 / 13.2 | 11.3 / 11.3 → 3.6 / 7.8 | 42.2 / 42.2 → 6.2 / 19.2 |
| First Map visit (graph) | 10.3 → 2.3 | 44.9 → 3.5 | 39.5 → 13.3 | 177.4 → 39.2 |
| Warm reload, Map | 10.1 → 1.3 | 44.5 → 2.5 | 38.7 → 2.8 | 159.8 → 2.8 |
| Map list after another user's edit: list / edit shown | 9.9 → 1.3 / 2.3 | 44.0 → 1.6 / 6.8 | 37.1 → 2.8 / 8.9 | 159.6 → 3.1 / 13.4 |
| Click node → details (new / reopen) | 0.42 / 0.76 → 0.42 / 0.11 | 0.40 / 0.41 → 0.45 / 0.10 | 2.2 / 2.1 → 2.2 / 0.09 | 2.2 / 2.2 → 2.2 / 0.09 |
| Tab → Research (topics) | 5.4 → 0.46 | 0.9 → 0.74 | 4.2 → 4.2 | 4.4 → 4.5 |
| Tab → Research › Idea | 13.8 → 0.06 | 40.2 → 0.08 | 30.9 → 2.1 | 145.4 → 2.0 |
| Tab → Map | 15.7 → 0.26 | 40.5 → 0.37 | 32.8 → 0.20 | 147.5 → 0.26 |
| Tab → Execute (board) | 3.6 → 0.71 | 1.1 → 1.0 | 3.8 → 3.7 | 5.2 → 4.3 |
| Tab → Settings | 0.87 → 0.24 | 2.9 → 0.21 | 3.9 → 0.14 | 10.6 → 0.12 |
| Tab → Home | 1.5 → 1.1 | 6.4 → 5.0 | 4.9 → 1.1 | 16.8 → 4.2 |

API bytes for a warm Home reload fell from 195 KB / 899 KB (2k / 10k) to
1.3 KB (revalidation 304s); a warm Map reload from 1.5 MB / 7.2 MB to 2 KB.
The lite snapshot is 1.4 MB → 0.42 MB on the wire at 2k (bodies follow in
the background only when needed). A node detail already cached or
prefetched shows in 0.1 s; a first click still costs one round trip.

Remaining costs, not addressed here:

- Cold start still waits for ~770 KB of static modules (8 s on fast3g,
  47 s on slow3g) before any API call; bundling or preloading the module graph
  is the next step.
- Home at 10k nodes spends ~3 s of CPU in the hero graph's force simulation
  (`graph-force.js`), independent of the network.
- `/api/bootstrap` (and `/api/project`) carry the full Needs you list:
  420 KB gzip at 10k nodes with 2,000 review items. It is cached and
  revalidated, but the first download and a change both cost it.
- Settings still issues ~21 small requests on entry (each panel loads its own
  data); they run in parallel.
- Measured over local HTTP/1.1 (6 connections per host). Through Cloudflare
  the browser uses HTTP/2 or HTTP/3; the event stream is unbuffered there
  (`Cache-Control: no-cache, no-transform`, comments every 20 s, below the
  100 s idle timeout). Not verified against the production tunnel in this
  change.

## Status display

On desktop (workbench shell) the sync state is a status-bar item on the
right: "Synced" (muted check), "Syncing…" with a spinner, or "Offline ·
read-only" / "Server unreachable · read-only" in the warning colour. It stays
visible when idle so "is this current?" has a fixed answer; the tooltip shows
the last successful sync and pending checks, and clicking it re-checks the
server and refreshes the view. Going offline also tints the status bar and
shows one toast. Below 980 px (no status bar) the floating "Syncing…" chip
and the read-only banner are used instead.

Execute sub-tab switches (2k nodes, fast3g, first visit / after reload, ms
until the view's data is painted; before = 88db2cc):

| Sub-tab | Before | After |
| --- | ---: | ---: |
| Board | 535 / 484 | 233 / 170 |
| Runs | 370 / 340 | 41 / 35 |
| Runs, entered from Home | 381 / 382 | 54 / 55 |
| Resources | 694 / 649 | 35 / 24 |
| Agents | 334 / 351 | 21 / 21 |
| Library | 331 / 343 | 21 / 22 |
| GPU | 789 / 788 | 358 / 358 |

The GPU board needs a GPU queue; on a server without one it answers 409,
which is not cached, so it still costs a round trip there. Board time is
mostly rendering.
