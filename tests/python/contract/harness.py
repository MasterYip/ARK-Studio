"""Black-box HTTP contract cases, runnable against the Python or the Go server.

    python -B tests/python/contract/harness.py                       # Python reference
    ARK_CONTRACT_SERVER=ark_studio/server/ark-server.exe python -B tests/python/contract/harness.py
    python -B tests/python/contract/harness.py -k static --keep --list-files

Against ark-server the suite is Go-only by default: fixtures come from the
recorded archives (fixtures/*.zip, record_fixtures.py) and ``import
arkstudio`` is blocked (noarkstudio.py), so no Python server code is used.
``ARK_CONTRACT_FIXTURES=python`` builds fresh fixtures with fixture.py instead.

Each case names the contract route ids it exercises (docs/development/go-port/contract.json).
Against Go, a case runs only when every route it needs is in ``ark-server
routes`` (the Go server's ported list), so the same file measures progress.
Cases on Go-only routes (``goonly:`` ids, see go_only.py) are skipped against
the frozen Python server.
The servers are only ever driven over HTTP; fixtures come from fixture.py.
"""
from __future__ import annotations

import argparse
import json
import os
import re
from urllib.parse import quote
import shutil
import subprocess
import sys
import traceback
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from serverctl import (ASSET_VERSION, Client, Server, copy_fixture, make_fixture, recorded_fixture,  # noqa: E402
                       server_spec, temp_dir)
import go_only  # noqa: E402

CASES = []
DEFAULT_CSP = ("default-src 'self'; img-src 'self' data: blob: https: http:; "
               "style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "
               "font-src 'self' data:; worker-src 'self'; manifest-src 'self'; "
               "form-action 'self'; base-uri 'none'; object-src 'none'; "
               "frame-ancestors 'none'")


def case(*routes: str, mode: str = "local"):
    def wrap(fn):
        CASES.append({"name": fn.__name__, "fn": fn, "routes": list(routes), "mode": mode})
        return fn
    return wrap


def check(cond, message="check failed"):
    if not cond:
        raise AssertionError(message)


def eq(actual, expected, what=""):
    if actual != expected:
        raise AssertionError(f"{what}: expected {expected!r}, got {actual!r}")


def api_error(resp, status, code=None, message=None):
    eq(resp.status, status, "status")
    data = resp.json()
    eq(data.get("ok"), False, "ok")
    eq(data["error"]["status"], status, "error.status")
    if code is not None:
        eq(data["error"]["code"], code, "error.code")
    if message is not None:
        eq(data["error"]["message"], message, "error.message")
    eq(resp.header("connection").lower(), "close", "Connection header")


def security_headers(resp, *, cache="no-store", csp=DEFAULT_CSP, frame="DENY"):
    eq(resp.header("cache-control"), cache, "Cache-Control")
    eq(resp.header("x-content-type-options"), "nosniff", "X-Content-Type-Options")
    eq(resp.header("x-frame-options"), frame, "X-Frame-Options")
    eq(resp.header("referrer-policy"), "no-referrer", "Referrer-Policy")
    eq(resp.header("content-security-policy"), csp, "Content-Security-Policy")


# ------------------------------------------------------------- local mode
@case("exact:/api/health")
def health_local(ctx):
    c = ctx["client"]
    r = c.get("/api/health")
    eq(r.status, 200, "status")
    eq(r.header("content-type"), "application/json; charset=utf-8", "Content-Type")
    security_headers(r)
    eq(r.json(), {"ok": True, "app": "arkstudio", "version": "2.0", "root": str(ctx["server"].root)})
    # Python's json.dumps separators are part of the byte contract.
    check(r.body.startswith(b'{"ok": true, "app": "arkstudio"'), r.body[:60])
    head = c.request("HEAD", "/api/health")
    eq(head.status, 200, "HEAD status")
    eq(head.body, b"", "HEAD body")
    eq(head.header("content-length"), str(len(r.body)), "HEAD Content-Length")


@case("exact:/api/health")
def host_allowlist(ctx):
    server = ctx["server"]
    api_error(Client(server, host_header="evil.example").get("/api/health"), 403,
              "HOST_REJECTED", "Unexpected Host header")
    api_error(Client(server, host_header=f"127.0.0.1:{server.port + 1}").get("/api/health"),
              403, "HOST_REJECTED")
    eq(Client(server, host_header=f"localhost:{server.port}").get("/api/health").status, 200,
       "localhost:<port>")
    # ARK_ALLOWED_HOSTS entries match the hostname with any port.
    eq(Client(server, host_header="contract.example:4443").get("/api/health").status, 200,
       "ARK_ALLOWED_HOSTS alias")
    api_error(Client(server, host_header="").get("/api/health"), 403, "HOST_REJECTED")


@case("exact:/api/nope")
def unknown_api(ctx):
    api_error(ctx["client"].get("/api/nope"), 404, "error", "Unknown endpoint: /api/nope")


@case("static:/*", "exact:/")
def static_index(ctx):
    c = ctx["client"]
    for path in ("/", "/index.html", "/some/client/route"):
        r = c.get(path)
        eq(r.status, 200, path)
        eq(r.header("content-type"), "text/html; charset=utf-8", "Content-Type " + path)
        security_headers(r)
        eq(r.header("vary"), "Accept-Encoding", "Vary")
        check(f'<meta name="ark-build" content="{ASSET_VERSION}">' in r.text, "ark-build meta")
        check(f'href="./css/app.css?v={ASSET_VERSION}"' in r.text, "versioned css href")
    zipped = c.get("/", headers={"Accept-Encoding": "gzip"})
    eq(zipped.header("content-encoding"), "gzip", "Content-Encoding")
    eq(zipped.body, c.get("/").body, "gzip body")


@case("static:/*")
def static_assets(ctx):
    c = ctx["client"]
    versioned = c.get(f"/js/graph.js?v={ASSET_VERSION}")
    eq(versioned.status, 200, "status")
    eq(versioned.header("content-type"), "text/javascript; charset=utf-8", "js type")
    eq(versioned.header("cache-control"), "private, max-age=31536000, immutable", "immutable")
    check(re.search(r'from\s*["\']\./[^"\']+\.js\?v=' + ASSET_VERSION, versioned.text),
          "relative imports carry ?v=")
    eq(c.get("/js/graph.js").header("cache-control"), "no-store", "unversioned js")
    eq(c.get("/js/graph.js?v=old").header("cache-control"), "no-store", "stale version")
    eq(c.get(f"/sw.js?v={ASSET_VERSION}").header("cache-control"), "no-store", "sw.js")
    eq(c.get(f"/index.html?v={ASSET_VERSION}").header("cache-control"), "no-store", "html")
    css = c.get(f"/css/app.css?v={ASSET_VERSION}")
    eq(css.header("content-type"), "text/css; charset=utf-8", "css type")
    eq(c.get("/favicon.svg").header("content-type"), "image/svg+xml", "svg type")
    eq(c.get("/manifest.webmanifest").header("content-type"),
       "application/manifest+json; charset=utf-8", "manifest type")
    png = c.get("/brand/icon-180.png")
    eq(png.header("content-type"), "image/png", "png type")
    check(png.body.startswith(b"\x89PNG"), "png bytes")
    viz = c.get("/viz-frame.html")
    eq(viz.header("x-frame-options"), "SAMEORIGIN", "viz frame")
    check(viz.header("content-security-policy").startswith("sandbox allow-scripts;"), "viz CSP")
    for path in ("/login.html", "/missing.png", "/js/missing.js", "/%2e%2e/README.md"):
        api_error(c.get(path), 404, "error", "Not found")


@case("exact:/login", "exact:/api/auth/session")
def auth_routes_local(ctx):
    c = ctx["client"]
    r = c.get("/login")
    eq(r.status, 303, "local /login redirects")
    eq(r.header("location"), "/", "Location")
    s = c.get("/api/auth/session").json()
    eq(s, {"ok": True, "auth_required": False, "authenticated": True, "user": "local",
           "expires_at": None, "role": "owner", "is_admin": True, "local": True, "org": None})


@case("exact:/api/graph/snapshot")
def graph_snapshot(ctx):
    data = ctx["client"].get("/api/graph/snapshot", headers={"Accept-Encoding": "gzip"})
    eq(data.status, 200, "status")
    eq(data.header("content-encoding"), "gzip", "large JSON is gzipped")
    body = data.json()
    eq(body["total"], len(body["nodes"]), "total")
    node = next(n for n in body["nodes"] if n["title"].startswith("中文标题"))
    check(node["excerpt"].endswith("…") and len(node["excerpt"]) <= 241, "code-point excerpt")
    check("search_body" in node and "body" not in node, "snapshot carries search_body only")
    eq(list(node)[:6], ["id", "uid", "type", "title", "status", "importance"], "key order")
    eq(node["meta"]["big"], 1e16, "float")
    check(all(set(e) == {"id", "source", "target", "relation", "note"} for e in body["edges"]), "edges")


@case("exact:/api/units", r"regex:^/api/units/([^/]+)$", r"regex:^/api/units/([^/]+)/comments$")
def units_read(ctx):
    c = ctx["client"]
    listing = c.get("/api/units?slim=1&limit=5&sort=title").json()
    eq(len(listing["items"]), 5, "limit")
    check("facets" in listing, "facets default on")
    first = listing["items"][0]
    check("excerpt" in first and "body" not in first, "slim")
    unit = c.get(f"/api/units/{first['uid']}").json()["unit"]
    eq(unit["id"], first["id"], "get by uid")
    eq(c.get(f"/api/units/{first['id']}").json()["unit"]["uid"], first["uid"], "get by id")
    for key in ("links_out", "links_in", "comments", "topics", "activity", "raw_url", "body"):
        check(key in unit, key)
    api_error(c.get("/api/units/NOPE-9999"), 400, "error", "unit not found: NOPE-9999")
    claim = next(u for u in c.get("/api/units?q=" + quote("中文标题")).json()["items"])
    comments = c.get(f"/api/units/{claim['uid']}/comments").json()["comments"]
    eq([x["kind"] for x in comments], ["comment", "agent_reply"], "comment kinds")
    eq(c.get("/api/units?facets=0").json().get("facets"), None, "facets=0")


@case("exact:/api/revision", "exact:/api/activity", "exact:/api/timeline", "exact:/api/graph",
      "exact:/api/project")
def project_reads(ctx):
    c = ctx["client"]
    rev = c.get("/api/revision").json()["revision"]
    check(re.fullmatch(r"\d+:[^:]*(?::\d\d){2}(?:\+00:00)?:\d+:\d+:\d+:\d+:\d+:\d+:\d+:.*", rev), rev)
    act = c.get("/api/activity?limit=3").json()["activity"]
    eq(len(act), 3, "activity limit")
    timeline = c.get("/api/timeline").json()
    eq(timeline["total"], len(timeline["items"]), "timeline total")
    graph = c.get("/api/graph?context=1&limit=10").json()
    check(len(graph["nodes"]) >= 10 and "context_count" in graph, "graph context")
    project = c.get("/api/project").json()
    needs = project["needs_you"]
    eq(needs[0]["kind"], "decision", "decisions first")
    check(any(item["media"] and item["own_media"] == 0 for item in needs), "borrowed media")
    for key in ("manifest", "path", "stats", "activity", "next_actions", "summary"):
        check(key in project, key)


@case(r"regex:^/api/units/([^/]+)/comments$", r"regex:^/api/units/([^/]+)/comments/(\d+)/resolve$")
def comment_writes(ctx):
    c = ctx["client"]
    unit = c.get("/api/units?q=Evidence%20with%20media").json()["items"][0]
    path = f"/api/units/{unit['uid']}/comments"
    api_error(c.post(path, {"body": "x"}, headers={"Origin": "http://evil.example"}), 403,
              "error", "Cross-origin writes are rejected")
    api_error(c.post(path, {"body": "  "}), 400, "error", "comment text is required")
    api_error(c.post(path, {"body": "x", "kind": "agent_reply"}), 400, "error",
              "agent_reply comments come from agents")
    api_error(c.post(path, "not json"), 400, "error", "Request body must be JSON")
    before = c.get("/api/revision").json()["revision"]
    made = c.post(path, {"body": "新评论 😀"}).json()
    eq(made["comment"]["body"], "新评论 😀", "comment body")
    eq(made["comment"]["author"], "local", "author")
    check(made["comments"][-1]["id"] == made["comment"]["id"], "listed")
    check(c.get("/api/revision").json()["revision"] != before, "revision moves")
    done = c.post(f"{path}/{made['comment']['id']}/resolve", {"resolved": True}).json()
    eq(done["comment"]["resolved"], True, "resolved")
    eq(done["comment"]["resolved_by"], "local", "resolved_by")
    activity = c.get("/api/activity?limit=2").json()["activity"]
    eq([a["action"] for a in activity], ["comment.resolve", "comment.create"], "activity")


# -------------------------------------------------------------- auth mode
@case("exact:/api/health", "exact:/login", "static:/*", mode="auth")
def auth_gate(ctx):
    c = ctx["client"]
    eq(c.get("/api/health").json(), {"ok": True, "app": "arkstudio", "version": "2.0",
                                     "auth_required": True}, "health hides root")
    r = c.get("/")
    eq(r.status, 302, "app shell redirects")
    eq(r.header("location"), "/login?next=%2F", "Location")
    eq(c.get("/js/graph.js?x=1").header("location"), "/login?next=%2Fjs%2Fgraph.js%3Fx%3D1", "next")
    eq(c.get("/js/login.js").status, 200, "login asset is public")
    api_error(c.get("/api/units"), 401, "AUTH_REQUIRED", "Sign in with the ARK account to continue.")
    page = c.get("/login?next=/%23pair")
    eq(page.status, 200, "login page")
    eq(page.header("x-robots-tag"), "noindex, nofollow", "robots")
    check('name="csrf" value="csrf.' in page.text, "csrf")
    check("{{" not in page.text, "template rendered")


@case("exact:/login", "exact:/api/auth/session", "exact:/api/auth/logout", "exact:/api/units",
      mode="auth")
def auth_login_flow(ctx):
    server, user, password = ctx["server"], ctx["fixture"]["users"][0], ctx["fixture"]["password"]
    c = Client(server)
    bad = c.request("POST", "/login", body="username=x&password=y&csrf=bad",
                    headers={"Content-Type": "application/x-www-form-urlencoded",
                             "Origin": c.origin()})
    eq(bad.status, 403, "bad csrf")
    cross = c.request("POST", "/login", body="username=x",
                      headers={"Content-Type": "application/x-www-form-urlencoded",
                               "Origin": "http://evil.example"})
    eq(cross.status, 403, "cross-site sign-in")
    eq(c.login(user, "wrong-password").status, 401, "wrong password")
    ok = c.login(user, password)
    eq(ok.status, 303, "login redirect")
    eq(ok.header("location"), "/", "Location")
    cookie = ok.header("set-cookie")
    check(re.fullmatch(r"ark_session=v1\.[A-Za-z0-9_-]+\.\d+\.[A-Za-z0-9_-]+; Path=/; HttpOnly; "
                       r"SameSite=Lax; Max-Age=4320\d", cookie), cookie)
    session = c.get("/api/auth/session").json()
    eq((session["authenticated"], session["user"], session["role"], session["is_admin"]),
       (True, user, "member", False), "session")
    eq(c.get("/api/units?limit=1").status, 200, "API with cookie")
    eq(c.get("/").status, 200, "shell with cookie")
    out = c.post("/api/auth/logout")
    eq(out.json(), {"ok": True, "logged_out": True}, "logout")
    check("Max-Age=0" in out.header("set-cookie"), "cookie cleared")
    c.cookies["ark_session"] = cookie.split(";")[0].split("=", 1)[1]
    api_error(c.get("/api/units"), 401, "AUTH_REQUIRED")


# ------------------------------------------------- Go-only: agent docs API
DOCS = "/api/agent/v1/docs"


@case("goonly:exact:/api/agent/v1/docs", "goonly:prefix:/api/agent/v1/docs/", "exact:/api/agent/v1/interface")
def agent_docs_local(ctx):
    c = ctx["client"]
    tokens = ctx["fixture"]["tokens"]
    auth = {"Authorization": "Bearer " + tokens["server"]}
    r = c.get(DOCS, headers=auth)
    eq(r.status, 200, "index status")
    eq(r.header("content-type"), "application/json; charset=utf-8", "index type")
    security_headers(r)
    index = r.json()
    eq((index["ok"], index["format"], index["base"]), (True, "ark-agent-docs", DOCS), "index header")
    eq(index["start_here"][0]["path"], "agents/agent-interface.md", "start here")
    eq({k: v["path"] for k, v in index["endpoints"].items() if k in ("interface", "skills", "setup", "context")},
       {"interface": "/api/agent/v1/interface", "skills": "/api/agent/v1/skills",
        "setup": "/api/agent/v1/setup", "context": "/api/agent/v1/context"}, "endpoints")
    paths = {d["path"]: d for d in index["docs"]}
    for p in ("agents/agent-interface.md", "agents/external-agent.md", "agents/skills.md", "guide/topics.md",
              "guide/node-history.md", "operations/gpu.md"):
        check(p in paths, "index lists " + p)
    for p in ("development/release.md", "agents/skills-attribution.md", "operations/public-export.md"):
        check(p not in paths, "index hides " + p)
    for d in index["docs"]:
        eq(set(d), {"path", "title", "summary", "section", "bytes", "updated", "url"}, "doc keys " + d["path"])
        check(0 < len(d["summary"]) <= 300 and d["bytes"] > 0 and d["path"].startswith(d["section"] + "/"), d)
    md = c.get(DOCS + "/guide/topics.md", headers=auth)
    eq(md.status, 200, "markdown status")
    eq(md.header("content-type"), "text/markdown; charset=utf-8", "markdown type")
    check(md.text.startswith("# ") and len(md.body) == paths["guide/topics.md"]["bytes"], "markdown body")
    head = c.request("HEAD", DOCS + "/guide/topics.md", headers=auth)
    eq((head.status, head.body, head.header("content-length")), (200, b"", str(len(md.body))), "HEAD")
    doc = c.get(DOCS + "/agents/external-agent.md?format=json", headers=auth).json()
    eq(doc["path"], "agents/external-agent.md", "json path")
    check(doc["headings"] and all(set(h) == {"level", "text", "slug"} for h in doc["headings"]), "headings")
    check("](/api/agent/v1/docs/agents/agent-interface.md)" in doc["markdown"], "links rewritten in JSON mode")
    slug = doc["headings"][1]["slug"]
    sec = c.get(DOCS + "/agents/external-agent.md?section=" + quote(slug), headers=auth)
    eq(sec.status, 200, "section status")
    check(sec.text.startswith("#") and len(sec.body) < len(doc["markdown"].encode()), "one section")
    for path, status in ((DOCS + "/guide/nope.md", 404), (DOCS + "/%2e%2e/README.md", 404),
                         (DOCS + "/guide/..%2f..%2fgo%2fgo.mod", 404), (DOCS + "/development/release.md", 404),
                         (DOCS + "/guide/topics.md?section=zz-none", 404), (DOCS + "/guide/topics.md?format=x", 400)):
        eq(c.get(path, headers=auth).status, status, path)
    for headers in ({}, {"Authorization": "Bearer wrong"}, {"Authorization": "Bearer " + tokens["revoked"]}):
        eq(c.get(DOCS, headers=headers).status, 401, f"no agent auth {headers}")
    eq(c.get(DOCS, headers={"Authorization": "Bearer " + tokens["personal_read"]}).status, 200, "personal token")
    desc = c.get("/api/agent/v1/interface", headers=auth).json()
    ids = {cap["id"]: cap["path"] for cap in desc["capabilities"]}
    eq((ids.get("docs.index"), ids.get("docs.read")), (DOCS, DOCS + "/{path}"), "interface capabilities")
    eq(desc["docs"]["index"], DOCS, "interface docs entry")


@case("goonly:exact:/api/agent/v1/docs", mode="auth")
def agent_docs_auth(ctx):
    server, fixture = ctx["server"], ctx["fixture"]
    c = Client(server)
    eq(c.get(DOCS).status, 401, "signed out")
    c.login(fixture["users"][0], fixture["password"])
    eq(c.get("/api/units?limit=1").status, 200, "signed in")
    eq(c.get(DOCS).status, 401, "a browser session alone is not an agent token")
    eq(Client(server).get(DOCS, headers={"Authorization": "Bearer " + fixture["tokens"]["personal_write"]}).status,
       200, "personal agent token")


# ------------------------------------------- Go-only: ARK Studio desktop link
@case("goonly:exact:/api/desktop/link", "goonly:exact:/api/desktop/projects", "exact:/api/mobile/pair/claim",
      mode="auth")
def desktop_link_auth(ctx):
    server, fixture = ctx["server"], ctx["fixture"]
    api_error(Client(server).post("/api/desktop/link", {}), 401, "AUTH_REQUIRED")
    browser = Client(server)
    browser.login(fixture["users"][0], fixture["password"])
    link = browser.post("/api/desktop/link", {"device": "harness"})
    eq(link.status, 200, "link status")
    data = link.json()
    eq((data["expires_in"], data["claim_path"], data["pair"]["user"]),
       (120, "/api/mobile/pair/claim", fixture["users"][0]), "link reply")
    check(data["code"].startswith("arkp_"), "link code")
    app = Client(server)  # the desktop app: no cookie, the code only
    claim = app.post("/api/mobile/pair/claim", {"code": data["code"]}, same_origin=False)
    eq(claim.status, 200, "claim status")
    token = claim.json()["mobile_token"]
    eq((claim.json()["permission"], claim.json()["permanent"]), ("read_only", True), "claim reply")
    api_error(app.post("/api/mobile/pair/claim", {"code": data["code"]}, same_origin=False), 401, "PAIRING_USED")
    dev = {"X-ARK-Mobile-Token": token}
    listed = app.get("/api/desktop/projects", headers=dev)
    eq(listed.status, 200, "list status")
    body = listed.json()
    eq((body["user"], body["can_create"]), (fixture["users"][0], True), "list reply")
    check(any(p["id"] == fixture["project"] for p in body["projects"]), "fixture project listed")
    base = {"id", "name", "description", "created", "updated", "permission", "created_by_me"}
    people = {"creator", "last_actor"}  # only when known (project_people.go)
    check(all(base <= set(p) <= base | people for p in body["projects"]), "row keys")
    made = app.post("/api/desktop/projects", {"name": "Desktop made"}, headers=dev, same_origin=False)
    eq((made.status, made.json()["project"]["name"]), (200, "Desktop made"), "create")
    api_error(app.get("/api/units?limit=1", headers=dev), 403, "DESKTOP_LINK_SCOPE")
    api_error(app.get("/api/projects", headers=dev), 403, "DESKTOP_LINK_SCOPE")
    api_error(browser.get("/api/desktop/projects"), 401, "DESKTOP_LINK_REQUIRED")
    eq(app.request("DELETE", "/api/mobile/session", headers=dev).status, 200, "unlink")
    eq(app.get("/api/desktop/projects", headers=dev).status, 401, "after unlink")


# ----------------------------------------------------------------- runner
def go_ported(spec: str) -> set[str] | None:
    if spec == "python":
        return None
    out = subprocess.run([spec, "routes", "--json"], capture_output=True, text=True, check=True)
    return {item["id"] for item in json.loads(out.stdout)}


def go_only_run(spec: str) -> bool:
    """Against ark-server: recorded fixtures, no arkstudio (the default)."""
    return spec != "python" and os.environ.get("ARK_CONTRACT_FIXTURES", "recorded") != "python"


def run(spec: str, pattern: str = "", keep: bool = False, list_files: bool = False) -> int:
    if go_only_run(spec):
        import noarkstudio
        noarkstudio.install()
    ported = go_ported(spec)
    work = temp_dir("ark-contract-")
    results = []
    try:
        for mode in ("local", "auth"):
            cases = [c for c in CASES if c["mode"] == mode and pattern in c["name"]]
            if not cases:
                continue
            if go_only_run(spec):
                fixture = recorded_fixture(work / f"fixture-{mode}", mode)
            else:
                fixture = make_fixture(work / f"fixture-{mode}", auth=3 if mode == "auth" else 0)
            data = copy_fixture(Path(fixture["data_dir"]), work / f"data-{mode}")
            env = {"ARK_ALLOWED_HOSTS": "contract.example"}
            with Server(spec, data, env=env) as server:
                for item in cases:
                    if ported is None and any(go_only.is_go_only(r) for r in item["routes"]):
                        results.append((item["name"], "SKIP", "Go-only route (go_only.py)"))
                        continue
                    missing = [r for r in item["routes"] if ported is not None and r not in ported]
                    if missing:
                        results.append((item["name"], "SKIP", "not ported: " + ", ".join(missing)))
                        continue
                    ctx = {"server": server, "client": Client(server), "fixture": fixture}
                    try:
                        item["fn"](ctx)
                        results.append((item["name"], "PASS", ""))
                    except Exception as exc:  # noqa: BLE001 - report every case
                        detail = "".join(traceback.format_exception_only(type(exc), exc)).strip()
                        results.append((item["name"], "FAIL", detail))
            if list_files:
                print(f"\nfiles under {data} after the {mode} run:")
                for path in sorted(p for p in data.rglob("*") if p.is_file()):
                    print("  ", path.relative_to(data).as_posix(), path.stat().st_size)
    finally:
        if not keep:
            shutil.rmtree(work, ignore_errors=True)
        else:
            print("kept", work)
    width = max((len(r[0]) for r in results), default=10)
    for name, status, detail in results:
        print(f"{status:4}  {name:{width}}  {detail}")
    counts = {s: sum(1 for r in results if r[1] == s) for s in ("PASS", "FAIL", "SKIP")}
    print(f"server={spec} pass={counts['PASS']} fail={counts['FAIL']} skip={counts['SKIP']}")
    return 1 if counts["FAIL"] else 0


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("-k", default="", help="run cases whose name contains this text")
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--list-files", action="store_true")
    args = parser.parse_args(argv)
    return run(server_spec(), args.k, args.keep, args.list_files)


if __name__ == "__main__":
    raise SystemExit(main())
