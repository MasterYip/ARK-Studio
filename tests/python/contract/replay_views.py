"""Replay steps for the read-only views and the agent / task-agent read APIs.

Used by diffreplay.py.  Every step is (route id, method, path, body, headers);
a header ``X-Replay-NoCookie: 1`` sends the step from a client without the
signed-in session cookie (bearer-token-only callers).
"""
from __future__ import annotations

import sqlite3
from pathlib import Path
from urllib.parse import quote
from fixture import project_dir  # noqa: E402

NOCOOKIE = {"X-Replay-NoCookie": "1"}

# Module of a route id, for the per-module mismatch report.
MODULES = [
    ("agent-api", ("/api/agent/v1", "/api/agent/token")),
    ("remote-download", ("/api/remote/", "/download/")),
    ("task-agent-api", ("/api/task-agent/",)),
    ("org", ("/api/org",)),
    ("media", ("/api/media/",)),
    ("profile", ("/api/me", "/api/users/")),
    ("projects-bootstrap", ("/api/projects", "/api/bootstrap")),
    ("project-views", ("/api/project/overview", "/api/project/assignees", "/api/project/settings")),
    ("topics", ("/api/topics", "/api/units/([^/]+)/members")),
    ("export", ("/api/export",)),
    ("skills-insights", ("/api/skills", "/api/assistant/insights")),
]


def module_of(route: str) -> str:
    body = route.split(":", 1)[1].lstrip("^")
    for name, prefixes in MODULES:
        if any(body.startswith(p) for p in prefixes):
            return name
    return "pilot"


def fixture_ids(data_dir: Path) -> dict:
    """Ids the steps need: users, media and the designed topic."""
    out = {"users": {}, "media": [], "topic": "", "plain": ""}
    org = data_dir / "org.sqlite3"
    if org.is_file():
        con = sqlite3.connect(str(org))
        out["users"] = {row[0]: row[1] for row in con.execute("SELECT username, id FROM users")}
        con.close()
    project = project_dir(data_dir) / "ark.sqlite3"
    con = sqlite3.connect(str(project))
    host = con.execute("SELECT id FROM units WHERE title LIKE 'Media host%'").fetchone()[0]
    out["media"] = [row[0] for row in con.execute(
        "SELECT id FROM unit_media WHERE unit_id=? ORDER BY id", (host,))]
    out["topic"] = con.execute("SELECT uid FROM units WHERE title LIKE 'Curated%'").fetchone()[0]
    out["plain"] = con.execute("SELECT uid FROM units WHERE type='claim' ORDER BY id LIMIT 1").fetchone()[0]
    out["project"] = con.execute("SELECT value FROM meta WHERE key='name'").fetchone()
    con.close()
    return out


def view_steps(fixture: dict, ids: dict, auth: bool) -> list[tuple]:
    g = {"Accept-Encoding": "gzip"}
    steps = [("exact:/api/remote/info", "GET", "/api/remote/info", None, {}),
             ("exact:/api/remote/info", "GET", "/api/remote/info", None, {"Host": "contract.example"}),
             ("exact:/download/ark.apk", "GET", "/download/ark.apk", None, {}),
             ("exact:/download/ARK-Studio-webapp.apk", "GET", "/download/ARK-Studio-webapp.apk", None, g),
             ("exact:/download/ARK-Studio-android.apk", "HEAD", "/download/ARK-Studio-android.apk", None, {})]
    add = lambda route, path, headers=None, method="GET": steps.append(  # noqa: E731
        (route, method, path, None, dict(headers or {})))
    for path in ("/api/bootstrap", "/api/projects", "/api/org", "/api/org/", "/api/org/users",
                 "/api/org/teams", "/api/org/audit", "/api/org/audit?limit=2", "/api/org/audit?limit=x"):
        add("exact:" + path.split("?")[0], path, g)
    for name, uid in list(ids["users"].items())[:3] + [("nobody", "usr_nope")]:
        add(r"regex:^/api/org/users/([^/]+)/linux-accounts$", f"/api/org/users/{uid}/linux-accounts")
    for pid in (fixture["project"], "prj_nope", quote("中文")):
        add(r"regex:^/api/org/projects/([^/]+)$", f"/api/org/projects/{pid}")
    add("exact:/api/me", "/api/me")
    add("exact:/api/me", "/api/me", method="HEAD")
    avatar = r"regex:^/api/users/([^/]+)/avatar$"
    for user in list(ids["users"])[:4] + ["nobody", "user0001%40x", "user0001?v=abc"]:
        add(avatar, f"/api/users/{user}/avatar")
    add(avatar, "/api/users/user0001/avatar?v=1", method="HEAD")
    for query in ("", "?host=lab-host-1", "?host=!bad", "?host=" + "x" * 60):
        add("exact:/api/project/overview", "/api/project/overview" + query, g)
    add("exact:/api/project/overview", "/api/project/overview", {"X-ARK-Host": "hdr-host"})
    for path in ("/api/project/assignees", "/api/project/settings", "/api/topics", "/api/assistant/insights",
                 "/api/agent/token"):
        add("exact:" + path, path, g)
    members = r"regex:^/api/units/([^/]+)/members$"
    for ref in (ids["topic"], ids["topic"] + "?full=1", ids["plain"], "NOPE-1"):
        add(members, f"/api/units/{ref}/members", g)
    for fmt in ("json", "markdown", "csv", "svg", "JSON", "bogus", "csv&project=" + fixture["project"],
                "json&project=prj_nope"):
        add("exact:/api/export", "/api/export?format=" + fmt, g)
    add("exact:/api/export", "/api/export")
    raw = r"regex:^/api/media/(\d+)/raw$"
    ranges = ["bytes=0-99", "bytes=-100", "bytes=99999-", "bytes=50000-", "bytes=0-", "bytes=-0",
              "bytes=5-2", "bytes=0-1,5-6", "bytes = 0 - 9", "bytes=１-２", "bytes=0-999999999",
              "items=0-1", "bytes=-"]
    for media_id in ids["media"]:
        add(raw, f"/api/media/{media_id}/raw")
        add(raw, f"/api/media/{media_id}/raw", method="HEAD")
        add(raw, f"/api/media/{media_id}/raw?download=1")
    clip = ids["media"][0]
    for value in ranges:
        add(raw, f"/api/media/{clip}/raw", {"Range": value})
    add(raw, f"/api/media/{clip}/raw?download=true", {"Range": "bytes=10-19"})
    add(raw, f"/api/media/{ids['media'][7]}/raw", {"Range": "bytes=0-0"})  # empty.bin
    for bad in ("99999", "%EF%BC%91"):
        add(raw, f"/api/media/{bad}/raw")
    skills = [("exact:/api/skills", "/api/skills"), ("exact:/api/skills/interface", "/api/skills/interface"),
              ("exact:/api/skills/setup/state", "/api/skills/setup/state"),
              ("exact:/api/skills/setup/state", "/api/skills/setup/state?host=lab-host-1"),
              (r"regex:^/api/skills/([^/]+)$", "/api/skills/rrsi"),
              (r"regex:^/api/skills/([^/]+)$", "/api/skills/agent-team?docs=0"),
              (r"regex:^/api/skills/([^/]+)$", "/api/skills/nope"),
              (r"regex:^/api/skills/([^/]+)$", "/api/skills/"),
              (r"regex:^/api/skills/([^/]+)/(prompt|agent)$", "/api/skills/design/prompt"),
              (r"regex:^/api/skills/([^/]+)/(prompt|agent)$", "/api/skills/setup/agent"),
              (r"regex:^/api/skills/([^/]+)/(prompt|agent)$", "/api/skills/nope/prompt"),
              (r"regex:^/api/skills/([^/]+)$", "/api/skills/a/b/c")]
    for route, path in skills:
        add(route, path, g)
    return steps


def agent_steps(fixture: dict, ids: dict, units: list[dict], auth: bool) -> list[tuple]:
    tokens = fixture["tokens"]
    uid = units[0]["uid"]
    project = {"X-ARK-Project": fixture["project"]} if auth else {}
    creds = [("server", {"Authorization": "Bearer " + tokens["server"]}),
             ("x-ark-token", {"X-ARK-Token": tokens["server"], "X-Agent-Name": "replay 代理"}),
             ("personal-write", {"Authorization": "Bearer " + tokens["personal_write"], **project}),
             ("personal-read", {"Authorization": "bearer " + tokens["personal_read"], "X-Agent-Name": "r"}),
             ("revoked", {"Authorization": "Bearer " + tokens["revoked"]}),
             ("expired", {"Authorization": "Bearer " + tokens["expired"]}),
             ("bad-personal", {"Authorization": "Bearer arka_nope"}),
             ("wrong", {"Authorization": "Bearer wrong"}),
             ("none", {})]
    a = "/api/agent/v1"
    paths = [("exact:/api/agent/v1/whoami", a + "/whoami"), ("exact:/api/agent/v1/context", a + "/context"),
             ("exact:/api/agent/v1/units", a + "/units?limit=3&slim=1"),
             (r"regex:^/api/agent/v1/units/([^/]+)$", f"{a}/units/{uid}"),
             (r"regex:^/api/agent/v1/units/([^/]+)/comments$", f"{a}/units/{ids['plain']}/comments"),
             (r"regex:^/api/agent/v1/units/([^/]+)/members$", f"{a}/units/{ids['topic']}/members"),
             ("exact:/api/agent/v1/insights", a + "/insights"),
             ("exact:/api/agent/v1/project/palette", a + "/project/palette"),
             ("exact:/api/agent/v1/interface", a + "/interface"), ("exact:/api/agent/v1/skills", a + "/skills"),
             ("prefix:/api/agent/v1/skills/", a + "/skills/rrsi"),
             ("prefix:/api/agent/v1/skills/", a + "/skills/nope"),
             ("exact:/api/agent/v1/setup", a + "/setup"),
             ("exact:/api/agent/v1/export", a + "/export?format=csv"),
             (r"fullmatch:/api/agent/v1/media/(\d+)/raw", f"{a}/media/{ids['media'][1]}/raw"),
             ("exact:/api/agent/v1/whoami", a + "/nope")]
    steps = []
    for _name, headers in creds:
        for route, path in paths:
            headers = dict(headers, **({"X-Replay-NoCookie": "1"} if auth else {}))
            if path.endswith("/nope"):
                route = "exact:/api/agent/v1/whoami"  # unknown agent endpoint; same dispatcher
            steps.append((route, "GET", path, None, headers))
    read = tokens["read_token"]
    t = "/api/task-agent/v1"
    tpaths = [("exact:/api/task-agent/v1/context", t + "/context"),
              ("exact:/api/task-agent/v1/project", t + "/project"),
              ("exact:/api/task-agent/v1/units", t + "/units?limit=4&ids=1,2"),
              (r"regex:^/api/task-agent/v1/units/([^/]+)$", f"{t}/units/{uid}"),
              (r"regex:^/api/task-agent/v1/units/([^/]+)/comments$", f"{t}/units/{ids['plain']}/comments"),
              ("exact:/api/task-agent/v1/links", t + "/links"),
              ("exact:/api/task-agent/v1/snapshot", t + "/snapshot"),
              ("exact:/api/task-agent/v1/snapshot", t + "/nope")]
    for headers in ({"Authorization": "Bearer " + read}, {"X-ARK-Read-Token": read},
                    {"Authorization": "Bearer nope"}, {}):
        for route, path in tpaths:
            steps.append((route, "GET", path, None, dict(headers, **NOCOOKIE)))
    steps.append(("exact:/api/me", "GET", "/api/me", None, {"Authorization": "Bearer " + read}))
    steps.append(("exact:/api/project/overview", "GET", "/api/project/overview", None,
                  {"Authorization": "Bearer " + tokens["server"]}))
    return steps
