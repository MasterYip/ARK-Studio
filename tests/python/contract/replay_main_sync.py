"""Replay steps for server-side changes followed from main (branch feat/go-backend-cli).

Each function covers one main merge; ``steps(fixture)`` returns all of them
in the format of replay_writes.py (route id, method, path, body, headers) and
is appended to the write steps by diffreplay.py.

* main a765ee8 (cold start): index.html #ark-modules list, gzip level 9,
  new static modules.
* main 480f4ad (link reversal): arrows.relations.<rel>.reverse (boolean) in
  project settings and prefs.appearance; non-boolean rejected on save.
* main 8e022a0 (stacked sidebar views): PATCH /api/me ``prefs.workbench``
  layouts may carry ``containers.<name>.sections`` (workbench_prefs._sections).
* main e41dfbf (client cache): conditional GETs (If-None-Match with each
  server's own ETag, see diffreplay N19), X-ARK-Project-Id, cursors on
  /api/revision and the graph snapshot (``bodies=0``), /api/graph/bodies,
  /api/changes after each of several writes and with stale, foreign, future
  and garbage cursors, /api/unit-details, media validators, paired phones,
  and GET /api/events read as frames (N20) around a write.
"""
from __future__ import annotations

ME = "exact:/api/me"


def workbench_sections_steps() -> list[tuple]:
    m = {"X-Replay-Module": "main-sync"}
    steps: list[tuple] = []
    add = lambda body: steps.append((ME, "PATCH", "/api/me", body, dict(m)))  # noqa: E731

    def prefs(sections, views=("explorer", "search", "outline"), **box):
        container = {"views": list(views), "active": views[0] if views else "", "visible": True, "size": 300,
                     **box}
        if sections is not ...:
            container["sections"] = sections
        layout = {"v": 1, "containers": {"left": container, "bottom": {"views": ["terminal"], "size": 200}}}
        return {"prefs": {"workbench": {"presets": [{"id": "u:stack", "name": "Stack", "layout": layout}],
                                        "default": "u:stack"}}}

    good = [{"views": ["explorer", "search"], "active": "search", "size": 240.7},
            {"views": ["outline"], "active": "", "size": 40, "collapsed": True, "maximized": False}]
    for sections in (
        ...,                                                        # no sections: still valid
        good,
        [{"views": ["explorer", "search", "outline"]}],             # defaults: active "", size 240
        [{"views": ["explorer"], "size": 4000, "maximized": True}, {"views": ["search", "outline"], "collapsed": False}],
        [{"views": ["explorer"], "size": 39}, {"views": ["search", "outline"]}],
        [{"views": ["explorer"], "size": 4000.5}, {"views": ["search", "outline"]}],
        [{"views": ["explorer"], "size": True}, {"views": ["search", "outline"]}],
        [{"views": ["explorer"], "size": "240"}, {"views": ["search", "outline"]}],
        [{"views": ["explorer"], "size": 10 ** 30}, {"views": ["search", "outline"]}],
        [{"views": ["search", "explorer", "outline"]}],             # wrong order
        [{"views": ["explorer", "search"]}],                        # missing a view
        [{"views": ["explorer", "search", "outline", "outline"]}],  # duplicate
        [{"views": []}, {"views": ["explorer", "search", "outline"]}],
        [{"views": ["explorer", "search", "outline"], "active": "nope"}],
        [{"views": ["explorer", "search", "outline"], "active": 1}],
        [{"views": ["explorer", "search", "outline"], "collapsed": 1}],
        [{"views": ["explorer", "search", "outline"], "maximized": "yes"}],
        [{"views": ["explorer", "search", "outline"], "bogus": 1}],
        [{"views": "explorer"}], [{"active": "explorer"}], ["explorer"], {"views": []}, None, [],
        [{"views": [v]} for v in ("explorer", "search", "outline")] + [{"views": ["x"]}] * 22,
        [{"views": ["explorer", 7, "outline"]}],
    ):
        add(prefs(sections))
    add(prefs([], views=()))                                        # empty container, empty sections
    add(prefs([{"views": ["explorer", "search", "outline"]}], maximized=True))
    steps.append((ME, "GET", "/api/me", None, dict(m)))
    return steps


def learn_cursor(vars_: dict, name: str, status: int, body: bytes) -> None:
    """X-Replay-Learn: keep the Python reply's ``cursor`` as @name@ (plus
    @name_id@ / @name_seq@); both servers mint the same cursors."""
    import json
    if status != 200:
        return
    try:
        cursor = json.loads(body).get("cursor")
    except (ValueError, AttributeError):
        return
    if isinstance(cursor, str):
        vars_[name] = cursor
        vars_[name + "_id"], _, vars_[name + "_seq"] = cursor.partition(".")


def client_cache_steps(fixture: dict) -> list[tuple]:
    """main e41dfbf (client cache): conditional GETs, X-ARK-Project-Id, cache
    ids, cursors, GET /api/changes, /api/unit-details, /api/graph/bodies and
    the revision event stream (docs/development/client-cache.md)."""
    from pathlib import Path
    from urllib.parse import quote
    from replay_views import fixture_ids
    from replay_writes import fixture_refs
    m = {"X-Replay-Module": "client-cache"}
    data_dir = Path(fixture["data_dir"])
    refs, ids = fixture_refs(data_dir), fixture_ids(data_dir)
    live = refs["live"]
    a, b, c, d = live[30], live[31], live[32], live[33]
    users = fixture.get("users") or []
    owner = users[0] if users else "local"
    unit, comments = r"regex:^/api/units/([^/]+)$", r"regex:^/api/units/([^/]+)/comments$"
    snap, bodies_, changes = "exact:/api/graph/snapshot", "exact:/api/graph/bodies", "exact:/api/changes"
    details, events, revision = "exact:/api/unit-details", "exact:/api/events", "exact:/api/revision"
    steps: list[tuple] = []

    def add(route, method, path, body=None, headers=None, **extra):
        steps.append((route, method, path, body, {**m, **(headers or {}), **extra}))

    def cond(route, path, *, star=False, head=False, headers=None):
        """A GET, then the same GET revalidated with this server's own ETag."""
        h = dict(headers or {})
        add(route, "GET", path, None, h)
        add(route, "GET", path, None, {**h, "If-None-Match": "{etag}"})
        add(route, "GET", path, None, {**h, "If-None-Match": '"zzz", {etag-strong}'})
        add(route, "GET", path, None, {**h, "If-None-Match": 'W/"nope"'})
        if star:
            add(route, "GET", path, None, {**h, "If-None-Match": "*"})
        if head:
            add(route, "HEAD", path, None, {**h, "If-None-Match": "{etag}"})

    add("exact:/api/projects/open", "POST", "/api/projects/open", {"id": fixture["project"]})
    gz = {"Accept-Encoding": "gzip"}
    for route, path in (
            ("exact:/api/bootstrap", "/api/bootstrap"), ("exact:/api/project", "/api/project"),
            ("exact:/api/project/overview", "/api/project/overview"),
            ("exact:/api/project/assignees", "/api/project/assignees"),
            ("exact:/api/project/settings", "/api/project/settings"), ("exact:/api/topics", "/api/topics"),
            ("exact:/api/me", "/api/me"), ("exact:/api/org", "/api/org"), ("exact:/api/projects", "/api/projects"),
            ("exact:/api/mobile/session", "/api/mobile/session"), ("exact:/api/skills", "/api/skills"),
            ("exact:/api/health", "/api/health"), ("exact:/api/auth/session", "/api/auth/session"),
            ("exact:/api/activity", "/api/activity?limit=5"), ("exact:/api/timeline", "/api/timeline"),
            ("exact:/api/graph", "/api/graph?limit=20"), (revision, "/api/revision"),
            (unit, f"/api/units/{a}"), (comments, f"/api/units/{a}/comments")):
        cond(route, path)
    for route, path in ((snap, "/api/graph/snapshot"), (snap, "/api/graph/snapshot?bodies=0"),
                        (snap, "/api/graph/snapshot?bodies=false"), (snap, "/api/graph/snapshot?bodies=no&x=1"),
                        (snap, "/api/graph/snapshot?bodies="), (bodies_, "/api/graph/bodies"),
                        ("exact:/api/units", "/api/units?limit=5"),
                        ("exact:/api/units", "/api/units?limit=5&facets=0")):
        cond(route, path, star=True, head=True, headers=gz)
    add(unit, "HEAD", f"/api/units/{a}", None, {"If-None-Match": "*"})
    for raw in ids["media"][:2]:
        cond(r"regex:^/api/media/(\d+)/raw$", f"/api/media/{raw}/raw", star=True, head=True)
        add(r"regex:^/api/media/(\d+)/raw$", "GET", f"/api/media/{raw}/raw?download=1")
        add(r"regex:^/api/media/(\d+)/raw$", "GET", f"/api/media/{raw}/raw",
            None, {"If-None-Match": "*", "Range": "bytes=0-3"})

    # Cursors: revision and snapshot agree; a delta after mixed writes.
    add(revision, "GET", "/api/revision", None, None, **{"X-Replay-Learn": "c0"})
    add(snap, "GET", "/api/graph/snapshot?bodies=0", None, gz, **{"X-Replay-Learn": "s0"})
    add(changes, "GET", "/api/changes?since=@c0@")
    add(changes, "GET", "/api/changes?since=@c0@&bodies=0")
    writes = [
        (unit, "PATCH", f"/api/units/{a}", {"title": "Cache delta A", "status": "review"}),
        ("exact:/api/units", "POST", "/api/units", {"title": "Cache delta new", "type": "note", "body": "x" * 300}),
        ("exact:/api/links", "POST", "/api/links", {"source": b, "target": c, "relation": "part_of"}),
        (unit, "PATCH", f"/api/units/{c}", {"assignee": owner}),
        (comments, "POST", f"/api/units/{d}/comments", {"body": "delta comment"}),
        (r"regex:^/api/units/([^/]+)/members$", "POST", f"/api/units/{refs['topic']}/members", {"add": [d]}),
        (r"regex:^/api/units/([^/]+)/archive$", "POST", f"/api/units/{b}/archive", {}),
    ]
    for n, (route, method, path, body) in enumerate(writes):
        add(route, method, path, body)
        add(changes, "GET", f"/api/changes?since=@c{n}@", None, None, **{"X-Replay-Learn": f"c{n + 1}"})
        add(snap, "GET", "/api/graph/snapshot", None, {**gz, "If-None-Match": "{etag}"})
    for query in ("since=@c0@", "since=@c0@&bodies=0", "since=@s0@&bodies=false", f"since=@c{len(writes)}@",
                  "since=@c3@&bodies=1", "since=bogus.1", "since=", "", "since=@c0_id@",
                  "since=@c0_id@.abc", "since=@c0_id@.-1", "since=@c0_id@.999999999",
                  "since=@c0_id@.99999999999999999999999", "since=@c0_id@.%205%20", "since=@c0_id@.%EF%BC%95",
                  "since=@c0_id@.1_0", "since=deadbeefdeadbeef.1", "since=@c0_id@.0",
                  "since=@c0@&since=bogus", "since=@c0_seq@", "since=.@c0_seq@"):
        add(changes, "GET", "/api/changes" + ("?" + query if query else ""), None, gz)
    add(changes, "HEAD", "/api/changes?since=@c0@")
    cond(changes, "/api/changes?since=@c1@")
    # Batched details: ids or uids, duplicates, missing, the 60 limit.
    many = ",".join(str(n) for n in range(1, 80))
    for query in (f"ids={a},{c},NOPE-1,999999", f"ids={a}&ids={a},{d}", "ids=1,2,3", f"ids={many}", "ids=",
                  "", "ids=%C2%B2", "ids=1,,%202%20,abc", f"ids={quote('中文')}", f"ids={b}"):
        add(details, "GET", "/api/unit-details" + ("?" + query if query else ""), None, gz)
    add(details, "GET", "/api/unit-details?ids=1,2,3")
    for n in (1, 2, 3):
        add(unit, "GET", f"/api/units/{n}", None, {"If-None-Match": f"{{detail-etag:{n}}}"})
    cond(details, f"/api/unit-details?ids={a},{c}")
    # Stale cursor: a snapshot restore gives the database a new log identity.
    add("exact:/api/blackbox", "POST", "/api/blackbox", {"reason": "cachecursor"})
    add(revision, "GET", "/api/revision", None, None, **{"X-Replay-Learn": "before_restore"})
    add("exact:/api/blackbox", "GET", "/api/blackbox")
    add(r"regex:^/api/blackbox/([^/]+)/restore$", "POST", "/api/blackbox/@snap_cachecursor@/restore", {})
    add(changes, "GET", "/api/changes?since=@before_restore@")
    add(revision, "GET", "/api/revision", None, None, **{"X-Replay-Learn": "after_restore"})
    add(changes, "GET", "/api/changes?since=@after_restore@")
    # The revision stream: first event, then a write and the event it causes.
    patch = (unit, "PATCH", f"/api/units/{a}", {"title": "Pushed by the stream"}, dict(m))
    add(events, "GET", "/api/events", None, None, **{"X-Replay-Events": {"during": [patch]}})
    add(events, "GET", f"/api/events?ark_project={fixture['project']}", None, None,
        **{"X-Replay-Events": {"before": 2, "after": 0}})
    add(events, "GET", "/api/events?ark_project=prj_nope", None, None,
        **{"X-Replay-Events": {"before": 2, "after": 0}})
    add(events, "GET", "/api/events", None, {"X-Replay-NoCookie": "1"}, **{"X-Replay-Events": {"before": 2, "after": 0}})
    # A paired phone may use the delta, details and stream routes.
    nc = {"X-Replay-NoCookie": "1"}
    dev = {**nc, "X-ARK-Mobile-Token": "@mtoken@"}
    add("exact:/api/mobile/pair", "POST", "/api/mobile/pair", {"tunnel_host": "192.168.1.60"})
    add("exact:/api/mobile/pair/claim", "POST", "/api/mobile/pair/claim", {"token": "@pair_code@"}, nc)
    add("exact:/api/mobile/session", "GET", "/api/mobile/session", None, dev)
    add(changes, "GET", "/api/changes?since=@c0@&bodies=0", None, dev)
    add(details, "GET", f"/api/unit-details?ids={a},{c}", None, dev)
    cond(snap, "/api/graph/snapshot?bodies=0", headers=dev)
    add(events, "GET", "/api/events?mobile_token=@mtoken@", None, nc,
        **{"X-Replay-Events": {"before": 2, "after": 0}})
    add(events, "GET", "/api/events", None, dev, **{"X-Replay-Events": {"during": [
        (comments, "POST", f"/api/units/{a}/comments", {"body": "from the stream test"}, dict(m))]}})
    add(changes, "GET", "/api/changes?since=@c0@", None, {**nc, "X-ARK-Mobile-Token": "arkm_bogus"})
    if users:
        # Signing out ends the stream with an "auth" event (re-checked every 10 s).
        add(events, "GET", "/api/events", None, {"X-Replay-As": "fresh1"}, **{"X-Replay-Events": {
            "during": [("exact:/logout", "GET", "/logout", None, dict(m))], "after": 1, "wait": 15.0}})
    return steps


RES_GRAPH = "exact:/api/resources/graph"
RES_DEVICE = "fullmatch:/api/resources/devices/([^/]+)"


def _setup_files(viewers: list) -> dict:
    """main db0be95: files under <project>/setup/ (relative path -> JSON value or text)."""
    import hashlib
    import json
    import re

    def slug(username: str) -> str:  # setup_layers._user_slug
        name = str(username or "").strip().lower()
        stem = re.sub(r"[^a-z0-9._-]+", "_", name)[:40].strip("._") or "user"
        return f"{stem}-{hashlib.sha1(name.encode('utf-8')).hexdigest()[:8]}"

    me, other = viewers[0], viewers[1] if len(viewers) > 1 else "someone"
    history = [json.dumps({"at": "2026-02-01T00:00:00+00:00", "event": "location", "host": f"{me}-pc"}),
               "not json", json.dumps({"at": "2026-02-02T00:00:00Z", "host": f"{me}-laptop"})]
    return {
        "project.json": {
            "state": "draft",
            "verification": {"host": "lab-box", "at": "2026-01-02T03:04:05+00:00"},
            "profile": {"resources": [
                {"id": "code", "kind": "repository", "name": "Code",
                 "details": {"remote": "https://bot:secret@github.com/lab/code.git", "role": "primary"}},
                {"id": "data", "kind": "storage", "name": "Datasets", "details": {"kind": "datasets"}},
                {"id": "slurm", "kind": "compute", "name": "Cluster", "details": {"kind": "slurm", "gpus": "8"}},
                {"id": "nowhere", "kind": "environment", "name": "Env"},
                {"id": "gate", "kind": "command", "details": {"run": "make test"}}],
                "repositories": [{"id": "paper", "path": "/p", "remote": "git@gitlab.com:lab/paper.git"}]},
            "locations": [
                {"resource": "project/code", "host_id": "lab-box", "host_label": "Lab box", "path_or_ref": "/srv/code",
                 "verified_at": "2026-01-02T03:04:05+00:00"},
                {"resource": "project/data", "host_id": f"{me}-pc", "path_or_ref": "D:/data"},
                {"resource": "project/paper", "host_id": "bad id!", "path_or_ref": "/x"}],
        },
        f"users/{slug(me)}.json": {
            "owner": me,
            "profile": {"resources": [{"id": "nb", "kind": "repository", "name": "Notebooks"},
                                      {"id": "gpu", "kind": "compute", "name": "Desk GPU",
                                       "details": {"kind": "local", "gpus": "1"}}]},
            "locations": [{"resource": f"u:{me}/nb", "host_id": f"{me}-pc", "path_or_ref": "C:/Users/me/nb"},
                          {"resource": "project/code", "host_id": f"{me}-laptop", "path_or_ref": "C:/code"}],
            "hosts": [{"id": f"{me}-pc", "label": "My PC"}, {"id": f"{me}-laptop", "label": "Laptop"}],
        },
        f"users/{slug(other)}.json": {
            "owner": other,
            "locations": [{"resource": "project/code", "host_id": f"{other}-box", "path_or_ref": "/home/o/code"}],
            "hosts": [{"id": f"{other}-box", "label": "Other box"}],
            "verification": {"host": f"{other}-box", "at": "2026-03-01T00:00:00Z"},
        },
        "users/stray.json": {"owner": "nobody-else", "hosts": [{"id": "stray"}]},
        "history.jsonl": "\n".join(history) + "\n",
    }


def _setup_action(fixture: dict, files: dict | None):
    """A file action: write ``files`` under <project>/setup/ of both data dirs, or remove it (None)."""
    import json
    import shutil
    from pathlib import Path

    def action(py_dir, go_dir) -> None:
        for data_dir in (py_dir, go_dir):
            setup = Path(data_dir) / "projects" / fixture["folder"] / "setup"
            if files is None:
                shutil.rmtree(setup, ignore_errors=True)
                continue
            for rel, value in files.items():
                target = setup / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, indent=2)
                target.write_text(text, encoding="utf-8", newline="\n")
    return action


def resources_graph_steps(fixture: dict) -> list[tuple]:
    """main db0be95: GET /api/resources/graph as each viewer, POST /api/resources/devices/<id>."""
    m = {"X-Replay-Module": "resources", "X-ARK-Project": fixture["project"]}
    users = list(fixture.get("users") or [])
    viewers = users or ["local"]
    others = [str(i) for i in range(1, len(users))]  # X-Replay-As indexes of the other members
    steps: list[tuple] = []

    def graph(**extra):
        steps.append((RES_GRAPH, "GET", "/api/resources/graph", None, {**m, **extra}))

    def device(device_id: str, body, **extra):
        steps.append((RES_DEVICE, "POST", f"/api/resources/devices/{device_id}", body, {**m, **extra}))

    def everyone():
        graph()
        for who in others:
            graph(**{"X-Replay-As": who})

    everyone()
    graph(**{"Accept-Encoding": "gzip"})
    # Same second as the GET before it: the body (and its ETag) carries each server's data dir.
    steps.append((RES_GRAPH, "HEAD", "/api/resources/graph", None,
                  {**m, "Accept-Encoding": "gzip", "X-Replay-SameSecond": "1"}))
    graph(**{"X-ARK-Project": "prj_nope"})
    for device_id, body in (("host:nope", {"hidden": True}), ("ark", {"hidden": True}), ("a%2Fb", {}),
                            ("phone:x", "[1]"), ("%E4%B8%AD", {"share_details": True})):
        device(device_id, body)
    device("host:nope", {"hidden": True}, Origin="http://evil.example")
    # A paired phone: a personal device of the default viewer.
    mob = {"X-Replay-Module": "resources"}
    steps.append(("exact:/api/mobile/pair", "POST", "/api/mobile/pair", {"tunnel_host": "192.168.1.70"}, dict(mob)))
    steps.append(("exact:/api/mobile/pair/claim", "POST", "/api/mobile/pair/claim", {"token": "@pair_code@"},
                  {**mob, "X-Replay-NoCookie": "1",
                   "User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)"}))
    everyone()
    phone = "phone:@pair_id@"
    for body in ({}, {"hidden": "yes"}, {"hidden": 1}, {"bogus": True}, {"hidden": True, "extra": False},
                 {"share_details": True}):
        device(phone, body)
    for who in others:
        device(phone, {"hidden": True}, **{"X-Replay-As": who})
    everyone()
    device(phone, {"hidden": True})
    everyone()
    device(phone, {"hidden": False, "share_details": False})
    device(phone, {"hidden": False})
    graph()
    # Setup layers and history in the project folder, then removed again.
    steps.append((_setup_action(fixture, _setup_files(viewers)), RES_GRAPH, "", None, {}))
    everyone()
    graph(**{"Accept-Encoding": "gzip"})
    mine = f"host:{viewers[0]}-laptop"
    device(f"host:{viewers[0]}-pc", {"hidden": True})  # shared: the project layer locates a resource there
    device("compute:desk-gpu", {"share_details": True})
    device(mine, {"share_details": True})
    everyone()
    for who in others:
        device(mine, {"hidden": False}, **{"X-Replay-As": who})
        device(f"host:{users[int(who)]}-box", {"hidden": True}, **{"X-Replay-As": who})
    device(mine, {"hidden": True, "share_details": False})
    everyone()
    device(mine, {"hidden": False})
    device("compute:desk-gpu", {"share_details": False})
    graph()
    steps.append((_setup_action(fixture, None), RES_GRAPH, "", None, {}))
    graph()
    return steps


def arrow_reverse_steps() -> list[tuple]:
    """main 480f4ad: per-relation arrow entries take an optional boolean
    ``reverse`` (project settings and the personal appearance override)."""
    m = {"X-Replay-Module": "main-sync"}
    sett, me = "exact:/api/project/settings", "exact:/api/me"
    relations = (
        {"derived_from": {"reverse": False}, "depends_on": {"reverse": True, "head": "open", "size": 2}},
        {"cites": {"size": 1.5, "reverse": True, "placement": "both"}},
        {"cites": {"reverse": 1}}, {"cites": {"reverse": "true"}}, {"cites": {"reverse": None}},
        {"supports": {"reverse": True, "zz": 1}},
    )
    out: list[tuple] = []
    for rel in relations:
        out.append((sett, "PATCH", "/api/project/settings", {"arrows": {"relations": rel}}, dict(m)))
    out.append((sett, "PATCH", "/api/project/settings", {"arrows": {"style": {"reverse": True}}}, dict(m)))
    out.append(("exact:/api/project/settings", "GET", "/api/project/settings", None, dict(m)))
    for rel in relations:
        out.append((me, "PATCH", "/api/me", {"prefs": {"appearance": {"arrows": {"relations": rel}}}}, dict(m)))
    out.append((me, "PATCH", "/api/me", {"prefs": {"appearance": {"arrows": {"style": {"reverse": False}}}}}, dict(m)))
    out.append((me, "GET", "/api/me", None, dict(m)))
    return out


def cold_start_steps() -> list[tuple]:
    """main a765ee8: index.html carries the static import graph of js/app.js
    (#ark-modules), static files gzip at level 9, new static modules."""
    m = {"X-Replay-Module": "main-sync"}
    gz = {**m, "Accept-Encoding": "gzip"}
    out: list[tuple] = []
    for path in ("/", "/index.html", "/js/app.js", "/js/i18n-en.js", "/js/i18n-zh.js", "/js/i18n_server_zh.js",
                 "/js/editor-entry.js", "/js/editor-media.js", "/vendor/d3-quadtree/d3-quadtree.js", "/js/boot.js"):
        out.append(("static:/*", "GET", path, None, dict(m)))
        out.append(("static:/*", "GET", path, None, dict(gz)))
    return out


def steps(fixture: dict) -> list[tuple]:
    return cold_start_steps() + arrow_reverse_steps() + workbench_sections_steps() + client_cache_steps(fixture) + resources_graph_steps(fixture)
