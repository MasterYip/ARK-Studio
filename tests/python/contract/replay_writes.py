"""Write sequences for the differential replay (unit/link/media/topic writes).

Steps are (route id, method, path, body, headers) like replay_views.py.  Paths
and bodies may name objects created earlier in the same replay:

    @unit@  @uid@   the unit of the last successful POST /api/units (id / uid)
    @link@          the id of the last created link
    @media@         the id of the last created media row
    @hash@ @head@ @rev0@ @rev1@ ...  node text history of the last history read

diffreplay.py fills them from the *Python* response, so both servers receive
byte-identical requests.  Header ``X-Replay-Module`` names the report module.
"""
from __future__ import annotations

import json
from urllib.parse import quote
import sqlite3
from pathlib import Path

UNITS = "unit-writes"
LINKS = "link-media-writes"
TOPICS = "topic-writes"


import replay_agents  # noqa: E402
from fixture import project_dir  # noqa: E402


def placeholders(vars_: dict, text: str) -> str:
    for key, value in vars_.items():
        text = text.replace(f"@{key}@", str(value))
    return text


def learn(vars_: dict, method: str, path: str, status: int, body: bytes) -> None:
    """Remember ids created by a successful step (Python side)."""
    if status != 200:
        return
    try:
        data = json.loads(body)
    except ValueError:
        return
    if not isinstance(data, dict):
        return
    if "current_hash" in data and "/history" in path:
        vars_["hash"] = data["current_hash"]
        if data.get("head"):
            vars_["head"] = data["head"]
        for n, item in enumerate(data.get("versions") or []):
            vars_[f"rev{n}"] = item.get("id")
    if path == "/api/blackbox" and isinstance(data.get("snapshots"), list):
        for item in data["snapshots"]:
            vars_.setdefault("snap_" + str(item.get("reason")), item.get("id"))
    if method != "POST":
        return
    if path.endswith("/gpu/jobs") and isinstance(data.get("job"), dict):
        vars_["gpujob_" + str(data["job"].get("name"))] = data["job"].get("id")
    if path == "/api/mobile/pair" and data.get("code"):
        vars_["pair_code"] = data["code"]
        vars_["pair_id"] = (data.get("pair") or {}).get("id")
    if path in ("/api/mobile/pair/claim", "/api/mobile/login") and data.get("mobile_token"):
        vars_["mtoken"] = data["mobile_token"]
    if path == "/api/org/users" and isinstance(data.get("user"), dict):
        vars_["new_user"] = data["user"].get("id")
    elif path == "/api/org/teams" and isinstance(data.get("team"), dict):
        vars_["team"] = data["team"].get("id")
    elif path == "/api/personal/agent-tokens" and isinstance(data.get("item"), dict):
        vars_["token_id"] = data["item"].get("id")
    if path in ("/api/units", "/api/agent/v1/units") and isinstance(data.get("unit"), dict):
        vars_["unit"], vars_["uid"] = data["unit"]["id"], data["unit"]["uid"]
    elif path == "/api/links" and "id" in data and not data.get("already"):
        vars_["link"] = data["id"]
    elif path == "/api/media" and "id" in data:
        vars_["media"] = data["id"]
    elif path == "/api/projects" and isinstance(data.get("summary"), dict) and data["summary"].get("id"):
        vars_.setdefault("proj:" + str(data["summary"].get("name")), data["summary"]["id"])
    elif path == "/api/workspace/terminals" and isinstance(data.get("terminal"), dict):
        vars_["term"] = data["terminal"]["id"]


def fixture_refs(data_dir: Path) -> dict:
    project = project_dir(data_dir) / "ark.sqlite3"
    con = sqlite3.connect(str(project))
    try:
        live = [r[0] for r in con.execute("SELECT uid FROM units WHERE archived=0 AND type<>'topic' ORDER BY id")]
        child = con.execute("SELECT u.uid FROM unit_links l JOIN units u ON u.id=l.source_id "
                            "WHERE l.relation='part_of' ORDER BY l.id LIMIT 1").fetchone()[0]
        orphan = con.execute("SELECT uid FROM units u WHERE archived=0 AND NOT EXISTS "
                             "(SELECT 1 FROM unit_links WHERE source_id=u.id AND relation='part_of') "
                             "ORDER BY id LIMIT 1").fetchone()[0]
        topic = con.execute("SELECT uid FROM units WHERE title LIKE 'Curated%'").fetchone()[0]
        archived = con.execute("SELECT uid FROM units WHERE archived=1 ORDER BY id LIMIT 1").fetchone()
        link = con.execute("SELECT id FROM unit_links ORDER BY id DESC LIMIT 1").fetchone()[0]
        media = con.execute("SELECT id FROM unit_media ORDER BY id DESC LIMIT 1").fetchone()[0]
        media_host = con.execute("SELECT uid FROM units WHERE title LIKE 'Media host%'").fetchone()[0]
        trap = con.execute("SELECT uid FROM units WHERE title LIKE 'Bundle trap%'").fetchone()[0]
    finally:
        con.close()
    return {"live": live, "child": child, "orphan": orphan, "topic": topic,
            "archived": archived[0] if archived else None, "link": link, "media": media,
            "media_host": media_host, "trap": trap}


def unit_write_steps(refs: dict) -> list[tuple]:
    units, unit = "exact:/api/units", r"regex:^/api/units/([^/]+)$"
    m = {"X-Replay-Module": UNITS}
    live = refs["live"]
    a, b, c = live[0], live[5], live[10]
    steps = []
    post = lambda body, headers=m: steps.append((units, "POST", "/api/units", body, dict(headers)))  # noqa: E731
    patch = lambda ref, body: steps.append((unit, "PATCH", f"/api/units/{ref}", body, dict(m)))  # noqa: E731
    for body in (
        {"title": "New 中文 😀", "type": "idea"},
        {"title": ""}, {"title": "   "}, {"title": None}, {"title": "x" * 241}, {"title": True},
        {"title": {"a": 1}}, {"title": "nul\u0000byte", "body": "b\u0000ody"},
        {"title": "t", "type": "bogus"}, {"title": "t", "type": " TASK "}, {"title": "t", "type": 5},
        {"title": "t", "status": 5}, {"title": "t", "status": ""}, {"title": "t", "status": None},
        {"title": "t", "status": "Done", "importance": "P1"}, {"title": "t", "importance": ["p1"]},
        {"title": "t", "tags": ["area:a", "needs:review", "Tag Space"]},
        {"title": "t", "tags": ["area:a", "needs:review", "plain", " NS : v "]},
        {"title": "t", "tags": "ab"}, {"title": "t", "tags": 5}, {"title": "t", "tags": {"k:v": 1}},
        {"title": "t", "tags": ["type:task", "type:idea"]}, {"title": "t", "tags": ["type:task"], "type": "idea"},
        {"title": "t", "tags": ["大话题:模型", "x:y:z", "_bad:x", "9ns:x", "Ünï:çode"]},
        {"title": "t", "tags": [1, 2.5, True, None, ["l"]]},
        {"title": "t", "tags": ["status:REVIEW"]}, {"title": "t", "tags": ["a" * 73]},
        {"title": "t", "start_at": "2026-02-30"}, {"title": "t", "start_at": "２０２６-01-01"},
        {"title": "t", "start_at": "2026-1-1"}, {"title": "t", "start_at": 20260101},
        {"title": "t", "start_at": "2026-03-01", "end_at": "2026-02-01"},
        {"title": "t", "end_at": "2000-01-01"}, {"title": "t", "start_at": "2999-01-01", "status": "done"},
        {"title": "t", "start_at": " 2026-05-05 "},
        {"title": "t", "meta": [1]}, {"title": "t", "meta": {"k": "v", "n": [1, 2.5]}}, {"title": "t", "meta": []},
        {"title": "t", "assignee": "local"}, {"title": "t", "assignee": "LOCAL"},
        {"title": "t", "assignee": "nobody"}, {"title": "t", "assignee": 5}, {"title": "t", "assignee": None},
        {"title": "t", "assignee": ""}, {"title": "t", "assignee": "user0001"}, {"title": "t", "assignee": "owner"},
        {"title": "t", "source_ref": "ref", "source_title": "st", "origin": "o" * 50},
        {"title": "t", "source_ref": "x" * 401},
        {"title": "links", "links": [{"target": a, "relation": "supports"}, {"target": "NOPE-9"},
                                     {"target_id": 3, "relation": "bogus"}, {"target": b, "note": "n"}]},
        {"title": "bad links", "links": "ab"}, {"title": "bad links 2", "links": 7},
        {"title": "media", "media": [{"url": "https://example.com/a.png", "caption": "c"},
                                     {"url": "javascript://x"}, {"url": ""}]},
        {"title": "decision", "type": "decision"}, {"title": "review", "type": "task", "status": "review"},
        {"title": "Topic A", "type": "topic", "members": [a, b]},
        {"title": "Topic B", "type": "topic", "design": {"rule": {"types": ["claim"]}, "relations": ["supports"]}},
        {"title": "Topic C", "type": "topic", "design": "x"},
        {"title": "Topic D", "type": "topic", "members": ["NOPE-1"]},
        {"title": "Topic E", "type": "topic", "design": {"rule": {"types": ["bogus"]}}},
        {"title": "Topic F", "type": "topic", "design": {"items": [{"unit": a, "x": 1, "y": 2, "parent": b}],
                                                          "order": [b, a], "add": [a, b]}},
    ):
        post(body)
    post({"title": "agent made"}, {**m, "X-Agent-Name": "bot 机器人"})
    # Edits on the last created unit and on fixture units.
    post({"title": "Edit target", "type": "task", "tags": ["area:edit"]})
    for body in (
        {"title": "renamed 改名"}, {}, {"title": "renamed 改名"}, {"status": "done"}, {"status": "open"},
        {"status": "review"}, {"status": "done"}, {"tags": ["area:b", "needs:x"]}, {"tags": None},
        {"tags": []}, {"tags": "ab"}, {"tags": 3}, {"type": "decision"}, {"type": None}, {"type": 7},
        {"meta": {"a": 1}}, {"meta": {"a": 1}}, {"meta": "x"}, {"assignee": "local"}, {"assignee": "LOCAL"},
        {"assignee": " local "}, {"assignee": None}, {"assignee": "ghost"}, {"assignee": 5},
        {"start_at": "2026-01-05", "end_at": "2026-01-01"}, {"start_at": "2026-01-05", "end_at": None},
        {"importance": 7}, {"importance": "p0"}, {"body": "x" * 200001}, {"body": "new body\n# h"},
        {"title": ""}, {"end_at": "bad"},
    ):
        patch("@unit@", body)
    patch("@uid@", {"title": "by uid"})
    patch("NOPE-1", {"title": "x"})
    patch(a, {"tags": ["area:fixture"]})
    patch("%41", {"title": "x"})
    patch("99999999999999999999", {"title": "x"})
    patch("%C2%B2", {"title": "x"})
    steps.append((unit, "PATCH", f"/api/units/{b}", "[1]", dict(m)))
    steps.append((unit, "PATCH", f"/api/units/{b}", "{bad", dict(m)))
    steps.append((unit, "PATCH", f"/api/units/{b}", {"title": "x"}, {**m, "Origin": "http://evil.example"}))
    # Archive / restore / delete (= archive).
    arch, rest = r"regex:^/api/units/([^/]+)/archive$", r"regex:^/api/units/([^/]+)/restore$"
    steps += [
        (arch, "POST", "/api/units/@unit@/archive", {}, dict(m)),
        (arch, "POST", "/api/units/@unit@/archive", {}, dict(m)),
        (rest, "POST", "/api/units/@unit@/restore", {}, dict(m)),
        (rest, "POST", "/api/units/NOPE-1/restore", {}, dict(m)),
        (unit, "DELETE", f"/api/units/{c}", None, dict(m)),
        (unit, "DELETE", "/api/units/NOPE-1", None, dict(m)),
        (rest, "POST", f"/api/units/{c}/restore", {}, dict(m)),
        (arch, "POST", "/api/units/%C2%B2/archive", {}, dict(m)),
    ]
    post({"title": "Review then archive", "type": "task", "status": "review"})
    steps += [(arch, "POST", "/api/units/@unit@/archive", {}, dict(m)),
              (rest, "POST", "/api/units/@unit@/restore", {}, dict(m))]
    ins = r"regex:^/api/units/([^/]+)/insert-between$"
    steps += [
        (ins, "POST", f"/api/units/{refs['child']}/insert-between", {"title": "Between 中", "type": "method"}, dict(m)),
        (ins, "POST", f"/api/units/{refs['child']}/insert-between", {}, dict(m)),
        (ins, "POST", f"/api/units/{refs['orphan']}/insert-between", {}, dict(m)),
        (ins, "POST", f"/api/units/{refs['child']}/insert-between", {"type": "bogus"}, dict(m)),
        (ins, "POST", "/api/units/NOPE-1/insert-between", {}, dict(m)),
    ]
    return steps


def link_media_steps(refs: dict) -> list[tuple]:
    m = {"X-Replay-Module": LINKS}
    live = refs["live"]
    a, b, c = live[1], live[2], live[3]
    links, link = "exact:/api/links", r"regex:^/api/links/(\d+)$"
    media, one = "exact:/api/media", r"regex:^/api/media/(\d+)$"
    steps = []
    for body in ({"source": a, "target": b, "relation": "supports"}, {"source": a, "target": b, "relation": "supports"},
                 {"source": a, "target": a}, {"source": a, "target": b, "relation": "bogus"},
                 {"source": a, "target": b, "relation": 5}, {"source": a, "target": "NOPE"},
                 {"source": None, "target": b}, {"source": True, "target": b, "relation": "cites"},
                 {"source": a, "target": c, "note": "x" * 501}, {"source": a, "target": c, "note": 7},
                 {"source": 3, "target": "4", "relation": " depends_on "}):
        steps.append((links, "POST", "/api/links", body, dict(m)))
    for body in ({"relation": "cites"}, {}, {"relation": "bogus"}, {"relation": ["x"]}):
        steps.append((link, "PATCH", "/api/links/@link@", body, dict(m)))
    steps += [
        (link, "PATCH", "/api/links/999999", {"relation": "cites"}, dict(m)),
        (link, "PATCH", "/api/links/%D9%A3", {"relation": "cites"}, dict(m)),
        (link, "DELETE", "/api/links/@link@", None, dict(m)),
        (link, "DELETE", "/api/links/@link@", None, dict(m)),
        (link, "DELETE", f"/api/links/{refs['link']}", None, dict(m)),
        (link, "DELETE", "/api/links/99999999999999999999", None, dict(m)),
        (link, "DELETE", "/api/links/%D9%A3", None, dict(m)),
    ]
    for body in ({"unit": a, "url": "https://example.com/x.png"},
                 {"unit": a, "url": "zotero://select/items/1", "kind": "paper", "caption": "cap"},
                 {"unit": a, "url": "JavaScript://x"}, {"unit": a, "url": "data:text/html,x"},
                 {"unit": a, "url": "1bad://x"}, {"unit": a, "url": "relative/path.md"},
                 {"unit": a, "url": "C:\\files\\a.png"}, {"unit": a, "url": ""}, {"unit": a, "url": None},
                 {"unit": a, "url": "u" * 2049}, {"unit": "NOPE", "url": "https://x"},
                 {"unit": a, "url": "https://x", "kind": "k" * 41}, {"unit": a, "url": "https://x", "caption": 5},
                 {"unit": a, "url": "https://example.com/last.mp4", "kind": "video"}):
        steps.append((media, "POST", "/api/media", body, dict(m)))
    for body in ({"caption": "  spaced  "}, {"kind": ""}, {"kind": "image"}, {}, {"caption": None},
                 {"caption": "c" * 301}, {"url": "ignored"}):
        steps.append((one, "PATCH", "/api/media/@media@", body, dict(m)))
    steps += [
        (one, "PATCH", "/api/media/999999", {"caption": "x"}, dict(m)),
        (one, "DELETE", "/api/media/@media@", None, dict(m)),
        (one, "DELETE", "/api/media/@media@", None, dict(m)),
        (one, "DELETE", f"/api/media/{refs['media']}", None, dict(m)),
        ("exact:/api/revision", "GET", "/api/revision", None, dict(m)),
    ]
    return steps


def topic_write_steps(refs: dict) -> list[tuple]:
    m = {"X-Replay-Module": TOPICS}
    live = refs["live"]
    t = refs["topic"]
    a, b, c, d = live[20], live[21], live[22], live[23]
    mem, delete = r"regex:^/api/units/([^/]+)/members$", r"regex:^/api/units/([^/]+)/members/([^/]+)$"
    steps = []
    post = lambda body, ref=t: steps.append((mem, "POST", f"/api/units/{ref}/members", body, dict(m)))  # noqa: E731
    patch = lambda body, ref=t: steps.append((mem, "PATCH", f"/api/units/{ref}/members", body, dict(m)))  # noqa: E731
    post({"add": [a, b]})
    post({"members": [c]})
    post({"add": [a]})
    post({"add": [{"unit": d, "x": 10.126, "y": "-20", "caption": "cap 中"}]})
    post({"add": [{"id": a}]})
    post({"add": "x"})
    post({"add": [t]})
    post({"add": ["NOPE"]})
    post({"add": [None]})
    post({}, ref=live[0])
    post({"add": [a]}, ref="NOPE-1")
    if refs["archived"]:
        post({"add": [refs["archived"]]})
    for body in (
        {"items": [{"unit": a, "x": 1.234, "y": 2}]}, {"items": [{"unit": a, "x": "abc"}]},
        {"items": [{"unit": a, "x": 1e8}]}, {"items": [{"unit": a, "x": "nan"}]}, {"items": [{"unit": a, "x": True}]},
        {"items": [{"unit": a, "x": None, "y": ""}]}, {"items": [{"unit": a, "caption": "new cap"}]},
        {"items": [{"unit": a, "caption": "new cap"}]}, {"items": [{"unit": a, "caption": "x" * 501}]},
        {"items": [{"unit": b, "parent": a}]}, {"items": [{"unit": b, "parent": "auto"}]},
        {"items": [{"unit": b, "parent": 0}]}, {"items": [{"unit": b, "parent": False}]},
        {"items": [{"unit": b, "parent": b}]}, {"items": [{"unit": b, "parent": live[40]}]},
        {"items": [{"unit": live[40], "x": 1}]}, {"items": [a]}, {"items": "x"},
        {"order": [c, a]}, {"order": [c, a]}, {"order": [live[40], "NOPE"]}, {"order": "x"},
        {"rule": {"types": ["claim"], "q": "  ", "tags": "area:x, ,y", "roots": 1, "start_from": " 2026 "}},
        {"rule": {"types": "claim,bogus"}}, {"rule": {"statuses": 5}}, {"rule": None}, {"rule": 0}, {"rule": []},
        {"relations": ["supports", "supports", "cites"]}, {"relations": ["bogus"]}, {"relations": None},
        {"relations": "x"}, {"relations": [["x"]]}, {"relations": ["bogus", {"a": 1}]},
        {"hidden_links": [3, "4", "x", 4.0, "٥"]}, {"hidden_links": "x"}, {"hide_links": [9]},
        {"show_links": [3, 9]}, {"hide_links": 1},
        {"remove": [a]}, {"remove": [a]}, {"exclude": [live[30]]}, {"restore": [live[30]]},
        {"restore": [live[31]]}, {"remove": [live[30]]},
        {"items": [{"unit": c, "x": 5, "y": 6}], "order": [d], "add": [a], "rule": {"types": ["idea"]}},
    ):
        patch(body)
    # v12 node-card width (topic_members.w): bounds, null/"" = automatic,
    # rounding to 0.1, bool/str/NaN/inf rejection, w with and without x/y.
    for w in (320, 139, 140, 640, 641, 139.99, 640.04, 255.55, "300", " 300 ", "abc", "", None, True, False,
              "nan", "inf", "-inf", "1e3", "2.5e2", [300], {"w": 1}, "٣٠٠", "3_00", 10**30, 10**400):
        patch({"items": [{"unit": a, "w": w}]})
    patch({"items": [{"unit": b, "x": 3, "y": 4, "w": 200}, {"unit": c, "w": 600.06}]})
    patch({"items": [{"unit": b, "x": 7}]})
    patch({"items": [{"unit": b, "x": "٣", "y": " 1_0 "}]})
    patch({"items": [{"unit": b, "x": 1, "w": "bad"}]})
    patch({"items": [{"unit": d, "w": 480}], "order": [d, a]})
    steps.append((mem, "GET", f"/api/units/{t}/members", None, dict(m)))
    steps.append((r"regex:^/api/units/([^/]+)$", "GET", f"/api/units/{t}", None, dict(m)))
    patch({"items": [{"unit": a}]}, ref=live[0])
    steps += [
        (delete, "DELETE", f"/api/units/{t}/members/{c}", None, dict(m)),
        (delete, "DELETE", f"/api/units/{t}/members/{c}", None, dict(m)),
        (delete, "DELETE", f"/api/units/{t}/members/NOPE", None, dict(m)),
        (delete, "DELETE", f"/api/units/{t}/members/%4E%4F", None, dict(m)),
        ("exact:/api/topics", "GET", "/api/topics", None, dict(m)),
        (r"regex:^/api/units/([^/]+)/members$", "GET", f"/api/units/{t}/members", None, dict(m)),
    ]
    return steps


def settings_steps() -> list[tuple]:
    m = {"X-Replay-Module": "project-settings"}
    proj, sett = "exact:/api/project", "exact:/api/project/settings"
    steps = []
    for body in ({"name": "Renamed 项目 😀"}, {"name": "  "}, {"name": None, "description": " desc "},
                 {"accent": "#123456", "extra": 1}, {}, {"name": 5}, {"description": "x" * 700},
                 {"name": ["a"]}, {"name": "Contract fixture"}):
        steps.append((proj, "PATCH", "/api/project", body, dict(m)))
    palette_pair = {"light": {"preset": "mono-light"}, "dark": {"preset": "mono"}}
    for body in (
        {"repulsion": 100}, {"repulsion": "abc"}, {"bogus": 1}, {"graph": {"distance": 50}},
        {"graph": {"distance": 999}}, {"graph": "x"}, {"graph": {}, "zz": 1},
        {"arrows": {"style": {"head": "open"}}}, {"arrows": {"style": {"size": 9}}},
        {"arrows": {"relations": {"cites": {"head": "bar"}}, "mind_tree": True}}, {"arrows": {"mind_tree": 1}},
        {"arrows": {"zz": 1}}, {"arrows": "x"},
        {"assistant": {"base_url": "https://api.example.com/v1/", "model": "m1", "reasoning_effort": "HIGH",
                       "timeout": "30", "api_key_env": "OPENAI_API_KEY", "enabled": 1}},
        {"assistant": {"model": None}}, {"assistant": {"timeout": 2}}, {"assistant": {"timeout": 12.9}},
        {"assistant": {"api_key_env": "ARK_TOKEN"}}, {"assistant": {"api_key_env": "MY_KEY"}},
        {"assistant": {"api_key_env": "bad name"}}, {"assistant": {"base_url": "ftp://x"}},
        {"assistant": {"zz": 1}}, {"assistant": "x"}, {"assistant": {"model": "has space"}},
        {"assistant": {"timeout": [1]}}, {"assistant": {"timeout": " 45 "}}, {"assistant": None},
        {"agents": {"providers": {"claude": {"enabled": True, "env_names": ["ANTHROPIC_BASE", "X", "X"]}}}},
        {"agents": {"providers": {"claude": {"env_names": ["MY_SECRET"]}}}},
        {"agents": {"providers": {"nope": {}}}}, {"agents": {"providers": {"codex": {"command": "x"}}}},
        {"agents": {"providers": {"Codex ": {"enabled": False}}}}, {"agents": {"providers": {"claude": None}}},
        {"agents": {"providers": {"claude": "x"}}}, {"agents": {"providers": "x"}}, {"agents": {"zz": 1}},
        {"agents": {"providers": {"gemini": {"env_names": "X"}}}}, {"agents": {"providers": {"gemini": {}}}},
        {"agents": None},
        {"palette": {"preset": "forest"}},
        {"palette": {"enabled": True, "light": {"preset": "sunset-light"}, "dark": {"accent": "#FF0000"}}},
        {"palette": {"light": {"background": "#000000"}}}, {"palette": {"accent": "red"}},
        {"palette": {"saved": {"Mine 我的": palette_pair}}}, {"palette": {"saved": {" bad": palette_pair}}},
        {"palette": {"saved": {"Mine 我的": None}}}, {"palette": {"saved": "x"}}, {"palette": {"preset": "bogus"}},
        {"palette": "x"}, {"palette": {"preset": "custom", "background": "#FFFFFF"}},
        {"palette": {"enabled": 0}},
        {"sessions": {"accept_uploads": True}}, {"sessions": {"accept_uploads": 1}}, {"sessions": []},
        {"sessions": {"zz": 1, "accept_uploads": False}}, {"sessions": {}}, {"sessions": None},
    ):
        steps.append((sett, "PATCH", "/api/project/settings", body, dict(m)))
    steps += [
        (sett, "GET", "/api/project/settings", None, dict(m)),
        (proj, "GET", "/api/project", None, dict(m)),
        ("exact:/api/projects", "GET", "/api/projects", None, dict(m)),
    ]
    return steps


def history_steps(refs: dict, tokens: dict) -> list[tuple]:
    m = {"X-Replay-Module": "node-history"}
    u = refs["live"][40]
    hist = r"fullmatch:/api/units/([^/]+)/history(?:/([^/]+))?"
    ahist = r"fullmatch:/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?"
    unit = r"regex:^/api/units/([^/]+)$"
    agent = {**m, "X-ARK-Token": tokens["server"], "X-Agent-Name": "historian"}
    get = lambda sub="", headers=m: steps.append((hist, "GET", f"/api/units/{u}/history{sub}", None, dict(headers)))  # noqa: E731
    post = lambda sub, body, headers=m: steps.append((hist, "POST", f"/api/units/{u}/history{sub}", body, dict(headers)))  # noqa: E731
    edit = lambda body, headers=m: steps.append((unit, "PATCH", f"/api/units/{u}", body, dict(headers)))  # noqa: E731
    steps: list[tuple] = []
    get()
    post("", {"enabled": "yes"})
    post("", {})
    get("/diff")
    post("", {"enabled": True})
    get()
    edit({"body": "first human edit\nline two"})
    get()
    post("/checkpoint", {"expected_hash": "nope"})
    post("/checkpoint", {"expected_hash": "@hash@"})
    get()
    edit({"title": "Agent rewrote the title", "body": "agent body\nline two\nline three"}, agent)
    get()
    edit({"body": "agent body\nline 2 changed\nline three"}, agent)
    get()
    get("?limit=1")
    get("?limit=1&offset=1")
    get("?limit=abc&offset=-3")
    get("/diff")
    get("/diff?from=@rev2@&to=@rev0@")
    get("/diff?from=@rev0@")
    get("/diff?to=nothash")
    get("/@rev1@")
    get("/nothash")
    get("/" + "0" * 40)
    post("/restore", {"revision": "@rev2@", "expected_hash": "stale"})
    post("/restore", {"revision": 5, "expected_hash": "@hash@"})
    post("/restore", {"revision": "@rev2@", "expected_hash": "@hash@"})
    get()
    post("/apply", {"from": "@rev2@", "to": "@head@", "file": "body.md", "expected_hash": "@hash@", "change": -1})
    post("/apply", {"from": "@rev2@", "to": "@head@", "file": "x.md", "expected_hash": "@hash@"})
    post("/apply", {"from": "@rev2@", "to": "@head@", "file": "body.md", "expected_hash": "@hash@", "change": True})
    post("/apply", {"from": "@rev2@", "to": "@rev3@", "file": "body.md", "expected_hash": "@hash@"})
    post("/apply", {"from": "@rev2@", "to": "@head@", "file": "body.md", "expected_hash": "@hash@", "change": 99})
    post("/apply", {"from": "@rev2@", "to": "@head@", "file": "body.md", "expected_hash": "@hash@", "change": 0})
    get()
    post("/apply", {"from": "@rev3@", "to": "@head@", "file": "title.txt", "expected_hash": "@hash@"})
    get()
    post("/bogus", {})
    get("", {**m, "X-ARK-Project": "prj_nope"})
    steps.append((ahist, "GET", f"/api/agent/v1/units/{u}/history", None, dict(agent)))
    steps.append((ahist, "POST", f"/api/agent/v1/units/{u}/history/checkpoint", {"expected_hash": "@hash@"}, dict(agent)))
    steps.append((ahist, "GET", f"/api/agent/v1/units/{u}/history", None, {**m, "Authorization": "Bearer wrong"}))
    post("", {"enabled": False})
    post("/checkpoint", {"expected_hash": "@hash@"})
    get()
    steps.append((hist, "GET", "/api/units/NOPE-1/history", None, dict(m)))
    return steps


def lifecycle_steps(fixture: dict) -> list[tuple]:
    m = {"X-Replay-Module": "project-lifecycle"}
    create, open_, proj = "exact:/api/projects", "exact:/api/projects/open", "exact:/api/project"
    steps: list[tuple] = []
    for body in ({"name": ""}, {"name": "   "}, {"name": "x" * 81}, {"name": 5},
                 {"name": "Second 项目", "template": "paper", "description": "  described  "},
                 {"name": "Second 项目"}, {"name": "Odd!!  name--", "template": "bogus"},
                 {"name": "Demo copy", "template": "demo", "description": "d" * 700}):
        steps.append((create, "POST", "/api/projects", body, dict(m)))
    steps += [
        ("exact:/api/projects", "GET", "/api/projects", None, dict(m)),
        ("exact:/api/bootstrap", "GET", "/api/bootstrap", None, dict(m)),
        (proj, "GET", "/api/project", None, dict(m)),
    ]
    for body in ({}, {"id": "prj_nope"}, {"path": "../outside.ark"}, {"path": "nope.ark"},
                 {"path": "second.ark"}, {"id": "ark-demo"}):
        steps.append((open_, "POST", "/api/projects/open", body, dict(m)))
        # A writer's GET /api/topics creates the default topics once per project.
        steps.append(("exact:/api/topics", "GET", "/api/topics", None, dict(m)))
    steps += [
        ("exact:/api/topics", "GET", "/api/topics", None, dict(m)),
        (proj, "GET", "/api/project", None, dict(m)),
        (proj, "DELETE", "/api/project", {"name": "ARK Studio Demo"}, dict(m)),
        (open_, "POST", "/api/projects/open", {"path": "second-2.ark"}, dict(m)),
        (proj, "DELETE", "/api/project", {"name": "wrong"}, dict(m)),
        (proj, "DELETE", "/api/project", "[1]", dict(m)),
        (proj, "DELETE", "/api/project", {"name": "Second 项目"}, dict(m)),
        ("exact:/api/projects", "GET", "/api/projects", None, dict(m)),
        (open_, "POST", "/api/projects/open", {"id": fixture["project"]}, dict(m)),
        (proj, "GET", "/api/project", None, dict(m)),
    ]
    return steps


def blackbox_steps() -> list[tuple]:
    m = {"X-Replay-Module": "blackbox"}
    box, restore = "exact:/api/blackbox", r"regex:^/api/blackbox/([^/]+)/restore$"
    return [
        (box, "GET", "/api/blackbox", None, dict(m)),
        (box, "POST", "/api/blackbox", {}, dict(m)),
        (box, "POST", "/api/blackbox", {"reason": "  before ../restore 中文!  "}, dict(m)),
        (box, "POST", "/api/blackbox", {"reason": 7}, dict(m)),
        (box, "GET", "/api/blackbox", None, dict(m)),
        (restore, "POST", "/api/blackbox/nope/restore", {}, dict(m)),
        (restore, "POST", "/api/blackbox/@snap_fixture@/restore", {}, dict(m)),
        (box, "GET", "/api/blackbox", None, dict(m)),
        ("exact:/api/units", "GET", "/api/units?limit=5", None, dict(m)),
        ("exact:/api/revision", "GET", "/api/revision", None, dict(m)),
    ]


def export_steps(refs: dict) -> list[tuple]:
    m = {"X-Replay-Module": "exports"}
    exp = "exact:/api/export"
    live = refs["live"]
    queries = [
        "format=bundle", "format=bundle&units=" + "bad%20ref!", f"format=bundle&units={live[0]}",
        f"format=bundle&units={live[0]},{live[5]}&depth=2", f"format=bundle&units={live[0]}&depth=abc",
        f"format=bundle&units={live[0]}&depth=-1", f"format=bundle&units={refs['media_host']}&depth=1",
        f"format=bundle&units={refs['media_host']}&check=1", f"format=bundle&units={refs['topic']}&depth=3",
        f"format=bundle&units={live[0]}%20,NOPE-1", "format=bundle&units=1,2,3&check=true",
        f"format=bundle&units={refs['trap']}&depth=2", f"format=bundle&units={refs['trap']}&check=1",
        "format=project", "format=bogus",
    ]
    steps = [(exp, "GET", "/api/export?" + q, None, dict(m)) for q in queries]
    steps.append((exp, "HEAD", "/api/export?format=project", None, dict(m)))
    return steps


def org_admin_steps(fixture: dict) -> list[tuple]:
    import base64
    from fixture import TINY_PNG
    m = {"X-Replay-Module": "org-admin"}
    pid = fixture["project"]
    steps: list[tuple] = []
    add = lambda route, method, path, body=None: steps.append((route, method, path, body, dict(m)))  # noqa: E731
    users, user = "exact:/api/org/users", r"regex:^/api/org/users/([^/]+)$"
    for body in ({"username": "newbie", "password": "longpassword", "role": "member", "display_name": " New 新 "},
                 {"username": "newbie", "password": "longpassword"}, {"username": "bad name", "password": "x"},
                 {"username": "other", "password": ""}, {"username": "boss", "password": "longpassword", "role": "owner"},
                 {"username": "weird", "password": "longpassword", "role": "bogus"}):
        add(users, "POST", "/api/org/users", body)
    add(users, "POST", "/api/org/users", {"username": "newbie2", "password": "pw-two-long", "role": "viewer"})
    add(users, "POST", "/api/org/users", {"username": "newbie", "password": "longpassword", "role": "member"})
    add(users, "GET", "/api/org/users")
    for body in ({"display_name": "Renamed 改", "role": "admin"}, {"disabled": True}, {"disabled": False},
                 {"role": "bogus"}, {"role": "member"}, {}):
        add(user, "PATCH", "/api/org/users/@new_user@", body)
    add(user, "PATCH", "/api/org/users/usr_nope", {"display_name": "x"})
    pw = r"regex:^/api/org/users/([^/]+)/password$"
    add(pw, "POST", "/api/org/users/@new_user@/password", {"new_password": "anotherpassword"})
    add(pw, "POST", "/api/org/users/@new_user@/password", {"new_password": ""})
    add(pw, "POST", "/api/org/users/usr_nope/password", {"new_password": "anotherpassword"})
    linux = r"regex:^/api/org/users/([^/]+)/linux-accounts$"
    for body in ({"account": "alice"}, {"account": "Bad"}, {"account": "root"}, {}):
        add(linux, "POST", "/api/org/users/@new_user@/linux-accounts", body)
    add(r"regex:^/api/org/users/([^/]+)/linux-accounts/([^/]+)$", "DELETE", "/api/org/users/@new_user@/linux-accounts/alice")
    teams, team = "exact:/api/org/teams", r"regex:^/api/org/teams/([^/]+)$"
    for body in ({"name": "Team Two 二", "description": " d "}, {"name": "team two 二"}, {"name": "  "}, {}):
        add(teams, "POST", "/api/org/teams", body)
    add(teams, "POST", "/api/org/teams", {"name": "Team Three"})
    for body in ({"name": "Team Renamed", "description": "x"}, {"name": "Team Three"}, {"description": None, "name": ""}):
        add(team, "PATCH", "/api/org/teams/@team@", body)
    tm = r"regex:^/api/org/teams/([^/]+)/members$"
    for body in ({"user": "newbie"}, {"user": "newbie"}, {"user_id": "usr_nope"}, {"username": "newbie2"}):
        add(tm, "POST", "/api/org/teams/@team@/members", body)
    tmd = r"regex:^/api/org/teams/([^/]+)/members/([^/]+)$"
    add(tmd, "DELETE", "/api/org/teams/@team@/members/newbie")
    add(tmd, "DELETE", "/api/org/teams/@team@/members/newbie")
    add(tmd, "DELETE", "/api/org/teams/team_nope/members/newbie")
    grants, revoke = r"regex:^/api/org/projects/([^/]+)/grants$", r"regex:^/api/org/projects/([^/]+)/grants/revoke$"
    for body in ({"user": "newbie", "permission": "write"}, {"team": "@team@", "permission": "read"},
                 {"user": "nobody"}, {"user": "newbie", "permission": "bogus"}, {"user_id": "@new_user@"}):
        add(grants, "POST", f"/api/org/projects/{pid}/grants", body)
    add(grants, "POST", "/api/org/projects/prj_nope/grants", {"user": "newbie"})
    for body in ({"user": "newbie"}, {"team": "@team@"}, {"team": "nope"}):
        add(revoke, "POST", f"/api/org/projects/{pid}/grants/revoke", body)
    add(team, "DELETE", "/api/org/teams/@team@")
    add(team, "DELETE", "/api/org/teams/@team@")
    add(teams, "GET", "/api/org/teams")
    tok, tokdel = "exact:/api/personal/agent-tokens", r"regex:^/api/personal/agent-tokens/([A-Za-z0-9_]+)$"
    add(tok, "GET", "/api/personal/agent-tokens")
    for body in ({"label": " my   token 令牌 ", "scope": "read", "expires_days": 30}, {"scope": "bogus"},
                 {"expires_days": 7}, {"expires_days": "90"}, {}):
        add(tok, "POST", "/api/personal/agent-tokens", body)
    add(tokdel, "DELETE", "/api/personal/agent-tokens/@token_id@")
    add(tokdel, "DELETE", "/api/personal/agent-tokens/@token_id@")
    add(tok, "GET", "/api/personal/agent-tokens")
    me, avatar = "exact:/api/me", "exact:/api/me/avatar"
    for body in ({"display_name": " Me 我  \n x "}, {"bio": "line1\nline2   x"}, {"email": "bad"}, {"email": "a@b.co"},
                 {"avatar": {"kind": "initials", "color": "#4F46E5"}}, {"avatar": {"kind": "image"}},
                 {"prefs": {"lang": "zh", "theme": "dark", "display": {"font_scale": 1.25}}},
                 {"prefs": {"display": {"font_scale": True}}},
                 {"prefs": {"appearance": {"graph": {"repulsion": 1, "distance": 2, "gravity": 0, "damping": 0.5}}}},
                 {"prefs": {"appearance": {"graph": None, "arrows": {"style": {"head": "open", "placement": "both",
                                                                                "size": 2}}}}},
                 {"prefs": {"appearance": {"bogus": 1}}}, {"prefs": {"bogus": 1}}, {"username": "x"}, {"display_name": ""}):
        add(me, "PATCH", "/api/me", body)
    png = base64.b64encode(TINY_PNG).decode()
    for body in ({"data": png, "content_type": "image/png"}, {"data": "data:image/png;base64," + png, "content_type": "image/png"},
                 {"data": "not base64!", "content_type": "image/png"}, {"data": png, "content_type": "image/svg+xml"},
                 {"data": png, "content_type": "image/jpeg"}, {"data": "", "content_type": "image/png"}):
        add(avatar, "POST", "/api/me/avatar", body)
    layout = {"v": 1, "containers": {"left": {"views": ["explorer", "search"], "active": "search",
                                              "visible": True, "size": 320.9},
                                     "bottom": {"views": ["terminal"], "maximized": True, "size": 80}}}
    preset = {"id": "u:mine-1", "name": "  Mine 我的 ", "layout": layout}
    for prefs in ({"workbench": {"presets": [preset], "default": "u:mine-1"}},
                  {"workbench": {"presets": [], "default": "focus"}},
                  {"workbench": {"presets": [preset, preset]}},
                  {"workbench": {"presets": [dict(preset, id="U:x")]}},
                  {"workbench": {"presets": [dict(preset, layout=dict(layout, v=True))], "default": ""}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1.0, "containers": {"top": {}}})]}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1, "containers": {
                      "left": {"views": ["a", "a"]}}})]}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1, "containers": {
                      "left": {"views": ["a"], "active": "b"}}})]}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1, "containers": {
                      "left": {"views": ["a"], "size": 79}}})]}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1, "containers": {
                      "left": {"views": ["view\n"], "size": True}}})]}},
                  {"workbench": {"presets": [dict(preset, layout={"v": 1, "containers": {
                      "right": {"views": ["view\n"], "size": 2000, "visible": False}}})]}},
                  {"workbench": {"presets": [dict(preset, name=" ")]}},
                  {"workbench": {"presets": [dict(preset, name="x" * 61)]}},
                  {"workbench": {"presets": [preset], "default": "nope"}},
                  {"workbench": {"presets": [preset], "extra": 1}},
                  {"workbench": {"presets": [{"id": "u:a", "name": "a"}]}},
                  {"workbench": {"presets": [dict(preset, id=f"u:p{i}") for i in range(21)]}},
                  {"workbench": "x"}, {"workbench": {}},
                  {"workbench": {"presets": [preset]}, "lang": "en"},
                  {"workbench": None}, {"workbench": {"presets": [preset], "default": "review"}}):
        add(me, "PATCH", "/api/me", {"prefs": prefs})
    add(me, "GET", "/api/me")
    add(avatar, "DELETE", "/api/me/avatar")
    add(user, "DELETE", "/api/org/users/@new_user@")
    add(user, "DELETE", "/api/org/users/@new_user@")
    add(users, "GET", "/api/org/users")
    add("exact:/api/org/audit", "GET", "/api/org/audit?limit=80")
    return steps


def mobile_steps(fixture: dict, refs: dict) -> list[tuple]:
    m = {"X-Replay-Module": "mobile"}
    nc = {**m, "X-Replay-NoCookie": "1"}
    dev = {**nc, "X-ARK-Mobile-Token": "@mtoken@"}
    pair, claim = "exact:/api/mobile/pair", "exact:/api/mobile/pair/claim"
    session, projects = "exact:/api/mobile/session", "exact:/api/mobile/session/projects"
    unit = r"regex:^/api/units/([^/]+)$"
    a = refs["live"][2]
    steps: list[tuple] = []

    def add(route, method, path, body=None, headers=m):
        steps.append((route, method, path, body, dict(headers)))

    add("exact:/api/mobile/apps", "GET", "/api/mobile/apps")
    add("exact:/api/mobile/connection", "GET", "/api/mobile/connection")
    add(session, "GET", "/api/mobile/session")
    add(session, "GET", "/api/mobile/session", None, nc)
    add(pair, "POST", "/api/mobile/pair", {})
    for body in ({"tunnel_host": "bad host!"}, {"tunnel_host": "192.168.1.50", "tunnel_port": "x"},
                 {"public_url": "ftp://x"}, {"tunnel_host": "192.168.1.50", "permission": "bogus"},
                 {"tunnel_host": "[::1]", "tunnel_port": 99999}):
        add(pair, "POST", "/api/mobile/pair", body)
    # A full-access, permanent pairing; the phone claims it and works.
    add(pair, "POST", "/api/mobile/pair", {"tunnel_host": "192.168.1.50", "tunnel_port": 8891})
    add(claim, "POST", "/api/mobile/pair/claim", {"token": "nope"}, nc)
    add(claim, "POST", "/api/mobile/pair/claim", {"code": "@pair_code@"}, nc)
    add(claim, "POST", "/api/mobile/pair/claim", {"token": "@pair_code@"}, nc)
    for route, method, path, body in (
            (session, "GET", "/api/mobile/session", None), (projects, "GET", "/api/mobile/session/projects", None),
            ("exact:/api/project", "GET", "/api/project", None), ("exact:/api/org", "GET", "/api/org", None),
            ("exact:/api/projects", "GET", "/api/projects", None), ("exact:/api/me", "GET", "/api/me", None),
            ("exact:/api/org/users", "GET", "/api/org/users", None),
            ("exact:/api/agent/token", "GET", "/api/agent/token", None),
            ("exact:/api/mobile/pairs", "GET", "/api/mobile/pairs", None),
            (unit, "PATCH", f"/api/units/{a}", {"title": "Edited from a phone"}),
            ("exact:/api/me/password", "POST", "/api/me/password", {"current_password": "x", "new_password": "y"}),
            ("exact:/api/personal/agent-tokens", "GET", "/api/personal/agent-tokens", None),
            ("exact:/api/projects/open", "POST", "/api/projects/open", {"id": fixture["project"]}),
            ("exact:/api/mobile/session/project", "POST", "/api/mobile/session/project",
             {"project_id": fixture["project"]})):
        add(route, method, path, body, dev)
    add("exact:/api/project", "GET", "/api/project", None, {**nc, "X-ARK-Mobile-Token": "arkm_bogus"})
    # A read-only pairing with a session lifetime: writes are refused.
    add(pair, "POST", "/api/mobile/pair", {"tunnel_host": "phone-host.lan", "permission": "read_only",
                                            "session_ttl": 3600, "expires_in": 5})
    add(claim, "POST", "/api/mobile/pair/claim", {"token": "@pair_code@"}, nc)
    add(unit, "PATCH", f"/api/units/{a}", {"title": "read only"}, dev)
    add(unit, "GET", f"/api/units/{a}", None, dev)
    add(session, "DELETE", "/api/mobile/session", None, dev)
    add(session, "DELETE", "/api/mobile/session", None, dev)
    add("exact:/api/mobile/pairs", "GET", "/api/mobile/pairs")
    revoke = r"regex:^/api/mobile/pairs/([^/]+)/revoke$"
    add(revoke, "POST", "/api/mobile/pairs/@pair_id@/revoke", {})
    add(revoke, "POST", "/api/mobile/pairs/pair_nope/revoke", {})
    add(pair, "POST", "/api/mobile/pair", {"tunnel_host": "10.0.0.7", "session_ttl": "permanent"})
    add(claim, "POST", "/api/mobile/pair/claim", {"token": "@pair_code@"}, nc)
    add(revoke, "POST", "/api/mobile/pairs/@pair_id@/revoke", {})
    add("exact:/api/project", "GET", "/api/project", None, dev)
    login = "exact:/api/mobile/login"
    password = fixture.get("password") or "x"
    users = fixture.get("users") or ["local"]
    json_nc = {**nc, "Content-Type": "application/json"}
    add(login, "POST", "/api/mobile/login", {"username": users[0], "password": "wrong"}, json_nc)
    add(login, "POST", "/api/mobile/login", {"username": users[0], "password": password},
        {**nc, "Content-Type": "text/plain"})
    add(login, "POST", "/api/mobile/login", {"username": users[0], "password": password, "device": "Pixel",
                                            "permission": "full", "session_ttl": 7200}, json_nc)
    add(projects, "GET", "/api/mobile/session/projects", None, dev)
    add("exact:/api/mobile/session/project", "POST", "/api/mobile/session/project",
        {"project_id": fixture["project"], "permission": "read_only"}, dev)
    add("exact:/api/mobile/session/project", "POST", "/api/mobile/session/project", {"project_id": "prj_nope"}, dev)
    add(session, "GET", "/api/mobile/session", None, dev)
    add("exact:/api/mobile/pairs", "GET", "/api/mobile/pairs")
    me_pw, org_pw = "exact:/api/me/password", "exact:/api/org/password"
    for body in ({}, {"current_password": "a", "new_password": "bbbbbbbbbb", "confirm_password": "c"},
                 {"current_password": "a", "new_password": "a", "confirm_password": "a"},
                 {"current_password": "a", "new_password": "short", "confirm_password": "short"},
                 {"current_password": "wrong-one", "new_password": "longenough1", "confirm_password": "longenough1"}):
        add(me_pw, "POST", "/api/me/password", body)
    add(org_pw, "POST", "/api/org/password", {"current_password": "wrong", "new_password": "longenough1"})
    if fixture.get("users"):
        add(me_pw, "POST", "/api/me/password", {"current_password": password, "new_password": "temp-password-9",
                                                "confirm_password": "temp-password-9", "revoke_agent_tokens": True})
        add(org_pw, "POST", "/api/org/password", {"current_password": "temp-password-9", "new_password": password,
                                                  "revoke_agent_tokens": True})
    return steps


def same_second_steps(refs: dict) -> list[tuple]:
    """Several writes stamped with one second: only the change counter in the
    revision token (db.REVISION_SCHEMA) tells them apart."""
    m = {"X-Replay-Module": "revision"}
    same = {**m, "X-Replay-SameSecond": "1"}
    unit, rev = r"regex:^/api/units/([^/]+)$", "exact:/api/revision"
    mem = r"regex:^/api/units/([^/]+)/members$"
    a, t = refs["live"][4], refs["topic"]
    return [
        (rev, "GET", "/api/revision", None, dict(m)),
        (unit, "PATCH", f"/api/units/{a}", {"title": "same second 1"}, dict(same)),
        (rev, "GET", "/api/revision", None, dict(same)),
        (unit, "PATCH", f"/api/units/{a}", {"title": "same second 2"}, dict(same)),
        (rev, "GET", "/api/revision", None, dict(same)),
        (mem, "PATCH", f"/api/units/{t}/members", {"items": [{"unit": refs["live"][21], "x": 3}]}, dict(same)),
        (rev, "GET", "/api/revision", None, dict(same)),
        ("exact:/api/graph/snapshot", "GET", "/api/graph/snapshot", None, dict(same)),
        (unit, "PATCH", f"/api/units/{a}", {"tags": ["area:same"]}, dict(same)),
        ("exact:/api/graph/snapshot", "GET", "/api/graph/snapshot", None, dict(same)),
        (rev, "GET", "/api/revision", None, dict(same)),
    ]


def agent_write_steps(fixture: dict) -> list[tuple]:
    """Agent API writes; run first, before the read steps' bad tokens fill the
    per-client failure limiter."""
    import base64
    from fixture import TINY_PNG
    refs = fixture_refs(Path(fixture["data_dir"]))
    m = {"X-Replay-Module": "agent-writes"}
    agent = {**m, "X-ARK-Token": fixture["tokens"]["server"], "X-Agent-Name": "writer bot"}
    a, topic = refs["live"][6], refs["topic"]
    png = base64.b64encode(TINY_PNG).decode()
    steps: list[tuple] = []

    def add(route, path, body, headers=agent, method="POST"):
        steps.append((route, method, path, body, dict(headers)))

    # A personal token without X-ARK-Project resolves to its owner's last
    # project (newest readable when there is none yet).
    add("exact:/api/agent/v1/units", "/api/agent/v1/units?limit=2", None,
        {**m, "X-Replay-NoCookie": "1", "Authorization": "Bearer " + fixture["tokens"]["personal_write"]}, "GET")
    add("exact:/api/agent/v1/units", "/api/agent/v1/units", {"title": "Agent unit", "type": "task", "tags": ["area:x"]})
    add("exact:/api/agent/v1/units", "/api/agent/v1/units", {"title": ""})
    add(r"regex:^/api/agent/v1/units/([^/]+)$", "/api/agent/v1/units/@uid@", {"status": "review", "body": "from agent"})
    add(r"regex:^/api/agent/v1/units/([^/]+)$", "/api/agent/v1/units/NOPE-1", {"status": "review"})
    add("exact:/api/agent/v1/links", "/api/agent/v1/links", {"source": "@uid@", "target": a, "relation": "supports"})
    add("exact:/api/agent/v1/links", "/api/agent/v1/links", {"source": "@uid@", "target": "@uid@"})
    add("exact:/api/agent/v1/media", "/api/agent/v1/media", {"unit": "@uid@", "url": "https://example.com/y.png"})
    for body in ({"unit": "@uid@", "filename": "fig.PNG", "data": png, "caption": "fig"},
                 {"unit": "@uid@", "filename": "fig.bmp", "data": png}, {"unit": "@uid@", "filename": "a.png", "data": "!!"},
                 {"unit": "@uid@", "filename": "a.png", "data": ""}, {"unit": "NOPE-1", "filename": "a.gif", "data": png}):
        add("exact:/api/agent/v1/media/upload", "/api/agent/v1/media/upload", body)
    for body in ({"unit": "@uid@", "filename": "../dir/notes v2.md", "data": base64.b64encode(b"hello").decode()},
                 {"unit": "@uid@", "filename": ".hidden", "data": base64.b64encode(b"x").decode(), "kind": "data"},
                 {"unit": "@uid@", "filename": "clip.MP4", "data": base64.b64encode(b"vid").decode()},
                 {"unit": "@uid@", "data": base64.b64encode(b"y").decode()}):
        add("exact:/api/agent/v1/assets/upload", "/api/agent/v1/assets/upload", body)
    comments = r"regex:^/api/agent/v1/units/([^/]+)/comments$"
    add(comments, "/api/agent/v1/units/@uid@/comments", {"body": "agent says"})
    add(comments, "/api/agent/v1/units/@uid@/comments", {"body": "x", "kind": "comment"})
    add(r"regex:^/api/agent/v1/units/([^/]+)/comments/(\d+)/resolve$",
        "/api/agent/v1/units/@uid@/comments/99999/resolve", {})
    add(r"regex:^/api/agent/v1/units/([^/]+)/members$", f"/api/agent/v1/units/{topic}/members", {"add": [a]})
    add(r"regex:^/api/agent/v1/units/([^/]+)/members$", f"/api/agent/v1/units/{topic}/members", {"members": ["NOPE-1"]})
    add("exact:/api/agent/v1/project/palette", "/api/agent/v1/project/palette", {"preset": "forest"})
    add("exact:/api/agent/v1/project/palette", "/api/agent/v1/project/palette", {"light": {"background": "#000000"}})
    add("exact:/api/agent/v1/project/palette", "/api/agent/v1/project/palette", None, agent, "GET")
    add("exact:/api/nope", "/api/agent/v1/bogus", {})
    add("exact:/api/agent/v1/units", "/api/agent/v1/units", {"title": "no token"}, {**m, "X-Replay-NoCookie": "1"})
    add("exact:/api/media/upload", "/api/media/upload", {"unit": a, "filename": "ui.png", "data": png}, m)
    return steps


WAIT_TERMINAL = "__wait_terminal__"


def workspace_steps(fixture: dict) -> list[tuple]:
    """The Editor's files, Changes and Terminal views (workspace.py).

    The fixture project folder is a Git working tree (fixture._git_workspace).
    Terminal commands are deterministic; WAIT_TERMINAL polls both servers
    until the terminal @term@ has finished before the next step."""
    import hashlib
    import os
    import sys
    from urllib.parse import quote
    m = {"X-Replay-Module": "workspace"}
    steps: list[tuple] = []

    def get(route, path, headers=None):
        steps.append((route, "GET", path, None, dict(headers or m)))

    def post(route, path, body, headers=None):
        steps.append((route, "POST", path, body, dict(headers or m)))

    def wait():
        steps.append((WAIT_TERMINAL, "", "@term@", None, {}))

    get("exact:/api/workspace/info", "/api/workspace/info")
    for query in ("", "?depth=2", "?depth=9", "?path=src", "?path=docs&depth=0", "?path=README.md",
                  "?path=../x", "?path=/abs", "?path=C:/x", "?path=.git", "?path=setup",
                  "?path=setup/x", "?path=nope", "?path=src%5C..%5Cx", "?path=node_modules/x",
                  "?path=a%00b", "?path=assets&depth=3", "?path=./src/./"):
        get("exact:/api/workspace/tree", "/api/workspace/tree" + query)
    for query in ("?q=py", "?q=MEDIA", "?q=", "?q=.md&limit=2", "?q=zzz", "?q=%20UTIL%20",
                  "?q=.&limit=500", "?q=png&limit=x"):
        get("exact:/api/workspace/search", "/api/workspace/search" + query)
    for target in ("src/main.py", "README.md", "crlf.txt", "data/blob.bin", "bom.txt", "latin1.txt", "",
                   "src", "ark.sqlite3", "nope.txt", "assets/media-range/notes.txt", "SRC/MAIN.PY",
                   "project.json", "x.pyc", "empty.txt", "setup/a"):
        get("exact:/api/workspace/file", "/api/workspace/file?path=" + quote(target))
    raw = "exact:/api/workspace/raw"
    for query in ("?path=assets/media-range/pic.png", "?path=assets/media-range/doc.pdf&download=1",
                  "?path=src", "?path=", "?path=src/main.py&download=true", "?path=nope.bin",
                  "?path=assets/media-range/" + quote("图表 😀.png")):
        get(raw, "/api/workspace/raw" + query)
    get(raw, "/api/workspace/raw?path=assets/media-range/clip.mp4", {**m, "Range": "bytes=10-19"})
    for query in ("", "?base=main", "?base=nope", "?base=-x", "?base=HEAD~1", "?base=", "?base=a%20b"):
        get("exact:/api/workspace/changes", "/api/workspace/changes" + query)
    for target in ("src/util.py", "docs/old.md", "notes/new.md", "data/blob.bin", "bin2.dat", "empty.txt",
                   "latin1.txt", "nope.txt", "src", "", "src/main.py", "bom.txt", "crlf.txt", ".git/x"):
        get("exact:/api/workspace/diff", "/api/workspace/diff?path=" + quote(target))
    get("exact:/api/workspace/diff", "/api/workspace/diff?path=src/util.py&base=nope")

    f, create = "exact:/api/workspace/file", "exact:/api/workspace/create"
    rename, delete = "exact:/api/workspace/rename", "exact:/api/workspace/delete"
    x_hash = hashlib.blake2b(b"x", digest_size=16).hexdigest()
    for body in ({"path": "notes/a.md", "content": "x"},
                 {"path": "notes/a.md", "content": "x", "create": True},
                 {"path": "notes/a.md", "content": "y"},
                 {"path": "notes/a.md", "content": "y", "base_hash": "0" * 32},
                 {"path": "notes/a.md", "content": "y", "base_mtime": "abc"},
                 {"path": "notes/a.md", "content": "y", "base_mtime": 1},
                 {"path": "notes/a.md", "content": "y \u65b0\r\nz", "base_hash": " " + x_hash + " "},
                 {"path": "notes/a.md", "content": 5},
                 {"path": "src", "content": "x"},
                 {"path": "missing/dir/x.txt", "content": "x", "create": True},
                 {"path": ".git/config", "content": "x"},
                 {"path": "../x", "content": "x"},
                 {"path": "", "content": "x"},
                 {"path": "big.txt", "content": "a" * (1024 * 1024 + 1), "create": True},
                 {"path": "src/main.py", "content": "print(1)\n", "base_mtime": None, "base_hash": ""},
                 {"content": "x", "create": 1}):
        post(f, "/api/workspace/file", body)
    for body in ({"type": "dir", "path": "newdir"}, {"type": " DIR ", "path": "newdir/sub"},
                 {"path": "newdir/f.txt", "content": "abc"}, {"path": "newdir/f.txt"},
                 {"type": "bogus", "path": "x"}, {"path": "y.txt", "content": 5},
                 {"path": "missing/y.txt"}, {"path": ""}, {"type": 5, "path": "z"},
                 {"path": "newdir/e.txt", "content": None},
                 {"path": "huge.txt", "content": "b" * (1024 * 1024 + 1)}):
        post(create, "/api/workspace/create", body)
    for body in ({"from": "newdir/f.txt", "to": "newdir/g.txt"}, {"from": "nope", "to": "x"},
                 {"from": "newdir/g.txt", "to": "newdir/e.txt"}, {"from": "", "to": "x"},
                 {"from": "newdir", "to": "newdir/sub/inner"}, {"from": "newdir/g.txt", "to": "missing/g.txt"},
                 {"from": "newdir/e.txt", "to": "notes/e.txt"}, {"from": "newdir/g.txt", "to": "ark.sqlite3"}):
        post(rename, "/api/workspace/rename", body)
    for body in ({"path": "newdir"}, {"path": "newdir", "recursive": True}, {"path": ""},
                 {"path": "nope"}, {"path": "notes/a.md"}, {"path": "notes/e.txt", "recursive": "yes"},
                 {"path": ".git"}):
        post(delete, "/api/workspace/delete", body)
    get("exact:/api/workspace/changes", "/api/workspace/changes")
    get("exact:/api/workspace/tree", "/api/workspace/tree?path=notes")

    terms = "exact:/api/workspace/terminals"
    term = r"regex:^/api/workspace/terminals/([^/]+)$"
    stream = r"regex:^/api/workspace/terminals/([^/]+)/stream$"
    stdin = r"regex:^/api/workspace/terminals/([^/]+)/stdin$"
    resize = r"regex:^/api/workspace/terminals/([^/]+)/resize$"
    cancel = r"regex:^/api/workspace/terminals/([^/]+)/cancel$"
    sleeper = "ping -n 30 127.0.0.1 >nul" if os.name == "nt" else "sleep 30"
    short = "ping -n 5 127.0.0.1 >nul" if os.name == "nt" else "sleep 5"
    py = '"' + sys.executable + '" -c "print(input()[::-1])"'
    for body in ({}, {"command": "  "}, {"command": "echo a\nb"}, {"command": "echo a\x00"},
                 {"command": "x" * 2001}, {"command": "echo hi", "cwd": "nope"},
                 {"command": "echo hi", "cwd": "src/main.py"}, {"command": "echo hi", "cwd": "../"},
                 {"command": "echo hi", "pty": True, "cols": "abc"},
                 {"command": "echo hi", "pty": "on", "rows": 9999},
                 {"command": "echo hi", "pty": 1, "cols": 1}):
        post(terms, "/api/workspace/terminals", body)
    get(terms, "/api/workspace/terminals")
    post(terms, "/api/workspace/terminals", {"command": "echo hello", "timeout": "5", "cols": None})
    wait()
    get(term, "/api/workspace/terminals/@term@")
    get(stream, "/api/workspace/terminals/@term@/stream")
    post(stdin, "/api/workspace/terminals/@term@/stdin", {"text": "x"})
    post(resize, "/api/workspace/terminals/@term@/resize", {"cols": 100, "rows": 30})
    post(cancel, "/api/workspace/terminals/@term@/cancel", {})
    post(stdin, "/api/workspace/terminals/term_nope/stdin", {"text": "x"})
    get(term, "/api/workspace/terminals/term_nope")
    get(stream, "/api/workspace/terminals/term_nope/stream")
    steps.append((term, "DELETE", "/api/workspace/terminals/@term@", None, dict(m)))
    get(term, "/api/workspace/terminals/@term@")
    post(terms, "/api/workspace/terminals", {"command": py, "cwd": "src"})
    post(stdin, "/api/workspace/terminals/@term@/stdin", {"text": ""})
    post(stdin, "/api/workspace/terminals/@term@/stdin", {"text": "y" * 8001})
    post(stdin, "/api/workspace/terminals/@term@/stdin", {"text": "hello \u4e16\u754c\r\n"})
    wait()
    get(term, "/api/workspace/terminals/@term@?limit=0")
    post(terms, "/api/workspace/terminals", {"command": sleeper})
    post(resize, "/api/workspace/terminals/@term@/resize", {"cols": 100, "rows": 30})
    post(resize, "/api/workspace/terminals/@term@/resize", {"cols": 1, "rows": 30})
    post(resize, "/api/workspace/terminals/@term@/resize", {})
    post(cancel, "/api/workspace/terminals/@term@/cancel", {})
    wait()
    get(term, "/api/workspace/terminals/@term@")
    post(terms, "/api/workspace/terminals", {"command": short, "timeout": 1.9})
    wait()
    get(term, "/api/workspace/terminals/@term@")
    post(terms, "/api/workspace/terminals", {"command": "echo err 1>&2", "pty": True})
    wait()
    get(stream, "/api/workspace/terminals/@term@/stream?offset=0")
    get(terms, "/api/workspace/terminals")
    get("prefix:/api/workspace/", "/api/workspace/bogus")
    post("prefix:/api/workspace/", "/api/workspace/bogus", {})
    get("exact:/api/workspace", "/api/workspace")
    read = {**m, "X-ARK-Read-Token": fixture["tokens"]["read_token"]}
    post(f, "/api/workspace/file", {"path": "x.txt", "content": "x", "create": True}, read)
    steps.append((term, "DELETE", "/api/workspace/terminals/@term@", None, read))
    return steps


def search_steps() -> list[tuple]:
    """Workbench Search (search.py): nodes and file contents, every option."""
    m = {"X-Replay-Module": "search"}
    nodes, grep = "exact:/api/search/nodes", "exact:/api/workspace/grep"
    steps: list[tuple] = []
    queries = ["Media", "media", "trap", "打包", "load", "x", "the", "Contract", "a", "line", "def", "中",
               "é", "😀", "assets/", "TODO", "area:", "·", "  ", "", "x" * 501]
    regexes = [r"\bMedia\b", r"[A-Z]\w+", r"(?i)media|trap", r"\d+", r"^line \d", r"(a)\1", r"(?<=line )\d",
               r"(?<!\w)host", r"(", r"a{2,1}", r"a*", r"x?", r"\bx\b|", r"\w+(?=\s)", r"[一-鿿]+",
               r"(?x) m e d i a", r"\N{EM DASH}", r"a{99999999999}", r"(?(1)a|b)", r"(?au)x", r".{300}",
               r"line (?P<n>\d+)", r"(?#c)trap", r"[", r"\q", "a\nb(", r"(?P<1>x)", r"Hello|hello"]
    for q in queries:
        for opts in ("", "&case=1", "&word=1", "&case=1&word=1"):
            steps.append((nodes, "GET", "/api/search/nodes?q=" + quote(q) + opts, None, dict(m)))
            steps.append((grep, "GET", "/api/workspace/grep?q=" + quote(q) + opts, None, dict(m)))
    for q in regexes:
        for opts in ("&regex=1", "&regex=1&case=1", "&regex=true&word=yes"):
            steps.append((nodes, "GET", "/api/search/nodes?q=" + quote(q) + opts, None, dict(m)))
            steps.append((grep, "GET", "/api/workspace/grep?q=" + quote(q) + opts, None, dict(m)))
    for filt in ("&include=*.py", "&include=src", "&include=src/*", "&include=docs/**", "&exclude=assets",
                 "&include=*.MD,*.txt&exclude=notes", "&include=src/**/*.py", "&exclude=*", "&include=%5Csrc%5C",
                 "&include=[sd]*", "&include=[!s]*.md", "&include=*.p?", "&include=,,", "&include=SRC",
                 "&exclude=/assets/media-range/"):
        steps.append((grep, "GET", "/api/workspace/grep?q=line" + filt, None, dict(m)))
        steps.append((grep, "GET", "/api/workspace/grep?q=e" + filt, None, dict(m)))
    steps.append((grep, "GET", "/api/workspace/grep", None, dict(m)))
    steps.append((nodes, "GET", "/api/search/nodes", None, dict(m)))
    # stream=1: NDJSON lines (gzip when accepted), sid, validation errors.
    gz = {**m, "Accept-Encoding": "gzip"}
    for q, extra in (("line", ""), ("Media", "&case=1"), ("e", ""), (r"\d+", "&regex=1"), ("tracked", ""),
                     ("ignored", ""), ("zzz-none", ""), ("line", "&include=src&sid=panel%3Afiles"),
                     ("(", "&regex=1"), ("", ""), ("a*", "&regex=1"), ("x" * 501, "")):
        for headers in (m, gz):
            steps.append((grep, "GET", "/api/workspace/grep?stream=1&q=" + quote(q) + extra, None, dict(headers)))
            steps.append((nodes, "GET", "/api/search/nodes?stream=1&q=" + quote(q) + extra, None, dict(headers)))
    steps.append((grep, "GET", "/api/workspace/grep?stream=true&q=line&sid=" + "s" * 90, None, dict(m)))
    steps.append((nodes, "GET", "/api/search/nodes?stream=0&q=line", None, dict(m)))
    chat = "exact:/api/chat"
    steps += [(chat, "DELETE", "/api/chat", None, dict(m)), (chat, "DELETE", "/api/chat", None, dict(m)),
              ("exact:/api/activity", "GET", "/api/activity?limit=5", None, dict(m))]
    return steps


def tab_project_steps(fixture: dict) -> list[tuple]:
    """Account mode: the active project belongs to the request (tab header or
    ark_project, session, the person's last project, newest readable), never
    the server.  X-Replay-As runs a step as fixture user N (its own session);
    "fresh<N>" signs in again first, "remember<N>" with remember-me."""
    m = {"X-Replay-Module": "tab-project"}
    home = fixture["project"]
    a_tab = {**m, "X-ARK-Project": home}
    b = {**m, "X-Replay-As": "1"}
    steps: list[tuple] = []

    def add(route, method, path, body=None, headers=m):
        steps.append((route, method, path, body, dict(headers)))

    boot, create, open_ = "exact:/api/bootstrap", "exact:/api/projects", "exact:/api/projects/open"
    add(boot, "GET", "/api/bootstrap")
    add(create, "POST", "/api/projects", {"name": "Tab Two Lab", "template": "blank"}, a_tab)
    add(boot, "GET", "/api/bootstrap", None, a_tab)
    add("exact:/api/units", "GET", "/api/units?limit=3", None, a_tab)
    add(boot, "GET", "/api/bootstrap")
    add(boot, "GET", f"/api/bootstrap?ark_project={home}")
    add(boot, "GET", "/api/bootstrap?ark_project=prj_nope")
    add(boot, "GET", "/api/bootstrap", None, {**m, "X-ARK-Project": "  @proj:Tab Two Lab@  "})
    add(boot, "GET", "/api/bootstrap", None, {**m, "X-ARK-Project": "ark-demo"})
    add(boot, "GET", "/api/bootstrap", None, b)
    add(boot, "GET", "/api/bootstrap", None, {**b, "X-ARK-Project": "@proj:Tab Two Lab@"})
    add("exact:/api/units", "GET", "/api/units?limit=2", None, {**b, "X-ARK-Project": "@proj:Tab Two Lab@"})
    add(create, "POST", "/api/projects", {"name": "Bob Lab", "template": "blank"}, b)
    add(boot, "GET", "/api/bootstrap", None, b)
    add(boot, "GET", "/api/bootstrap")
    add(open_, "POST", "/api/projects/open", {"id": home}, b)
    add(boot, "GET", "/api/bootstrap", None, b)
    add(boot, "GET", "/api/bootstrap", None, {**m, "X-Replay-As": "fresh0"})
    add(boot, "GET", "/api/bootstrap", None, {**m, "X-Replay-As": "remember1"})
    add("exact:/api/agent/v1/units", "GET", "/api/agent/v1/units?limit=2", None,
        {**m, "X-Replay-NoCookie": "1", "Authorization": "Bearer " + fixture["tokens"]["personal_write"]})
    add("exact:/api/project", "DELETE", "/api/project", {"name": "Tab Two Lab"},
        {**m, "X-ARK-Project": "@proj:Tab Two Lab@"})
    add(boot, "GET", "/api/bootstrap")
    add(boot, "GET", "/api/bootstrap", None, b)
    add(open_, "POST", "/api/projects/open", {"id": home})
    add(open_, "POST", "/api/projects/open", {"id": home}, b)
    return steps


def write_steps(fixture: dict) -> list[tuple]:
    refs = fixture_refs(Path(fixture["data_dir"]))
    return (export_steps(refs) + same_second_steps(refs) + unit_write_steps(refs) + link_media_steps(refs) + topic_write_steps(refs) + settings_steps()
            + history_steps(refs, fixture["tokens"]) + search_steps() + workspace_steps(fixture) + lifecycle_steps(fixture) + tab_project_steps(fixture) + blackbox_steps()
            + org_admin_steps(fixture) + mobile_steps(fixture, refs)
            + replay_agents.provider_worktree_steps(fixture))
