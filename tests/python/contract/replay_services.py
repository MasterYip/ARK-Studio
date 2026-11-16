"""Services modules of the differential replay (assistant, chat, GPU, InfoHub, open).

    python -B tests/python/contract/replay_services.py --go go/ark-server.exe [--auth|--owner] [--only assistant]

Quick mode (this script): only the services steps run, after the fixture is
built, against both servers; responses and the resulting data dirs are
compared exactly like diffreplay.py (same normalizations).  diffreplay.py
runs the same steps as part of the full replay via ``steps(fixture)``.

Steps are (route id, method, path, body, headers) like replay_writes.py;
``X-Replay-Module`` names the report module.
"""
from __future__ import annotations

import sqlite3
import sys
from pathlib import Path
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent))
import replay_gpu  # noqa: E402
import replay_setup  # noqa: E402
from fixture import project_dir  # noqa: E402

CALL = replay_gpu.CALL  # (CALL, "", "", fn, headers): diffreplay.py calls fn(py_dir, go_dir)
GPU = replay_gpu.GPU
SETUP = replay_setup.SETUP

ASSISTANT = "assistant"


def _refs(data_dir: Path) -> dict:
    project = project_dir(data_dir) / "ark.sqlite3"
    con = sqlite3.connect(str(project))
    try:
        by_type: dict[str, list] = {}
        for uid, id_, type_, archived in con.execute("SELECT uid, id, type, archived FROM units ORDER BY id"):
            if not archived:
                by_type.setdefault(type_, []).append((uid, id_))
        return by_type
    finally:
        con.close()


def assistant_steps(refs: dict) -> list[tuple]:
    h = {"X-Replay-Module": ASSISTANT}
    dispatch, apply, extract = "exact:/api/assistant/dispatch", "exact:/api/assistant/apply", "exact:/api/assistant/extract"
    tasks = refs.get("task", [])[:8]
    others = [items[0] for kind, items in sorted(refs.items()) if kind != "task" and items][:4]
    steps = [(dispatch, "GET", "/api/assistant/dispatch", None, h),
             (dispatch, "GET", "/api/assistant/dispatch?task=", None, h),
             (dispatch, "HEAD", "/api/assistant/dispatch?task=NOPE-1", None, h)]
    for uid, id_ in tasks:
        steps.append((dispatch, "GET", f"/api/assistant/dispatch?task={quote(uid)}", None, h))
        steps.append((dispatch, "GET", f"/api/assistant/dispatch?task={id_}", None, {**h, "Accept-Encoding": "gzip"}))
    for uid, _id in others:
        steps.append((dispatch, "GET", f"/api/assistant/dispatch?task={quote(uid)}", None, h))
    for bad in ("NOPE-1", "%EF%BC%91", "99999", quote("中文"), "1&task=2"):
        steps.append((dispatch, "GET", f"/api/assistant/dispatch?task={bad}", None, h))
    live = [uid for items in refs.values() for uid, _ in items]
    a, b, c = (live + ["X-1", "X-2", "X-3"])[:3]
    for body in ({}, {"insight": None}, {"insight": [1]}, {"insight": "x"}, {"insight": 3},
                 {"insight": True}, {"insight": {"ops": "ab"}}, {"insight": {"ops": 5}},
                 {"insight": {"ops": {"op": "link"}}}, {"insight": {"ops": [1]}},
                 {"insight": {"id": "i-1", "kind": "k", "ops": [{"op": "note"}, {"op": "link", "source": a, "target": b}]}},
                 {"insight": {"id": "i-2", "ops": [{"op": "link", "source": a, "target": c, "relation": "supports"}]}},
                 {"insight": {"id": "i-2", "ops": [{"op": "link", "source": a, "target": c, "relation": "supports"}]}},
                 {"insight": {"ops": [{"op": "link", "source": a, "target": "NOPE-1"}]}},
                 {"insight": {"ops": [{"op": "link", "source": b, "target": c, "relation": "bogus"}]}},
                 {"insight": {"ops": [{"op": "link", "source": b, "target": c}, {"op": "link", "source": c, "target": c}]}},
                 "[1]", "{"):
        steps.append((apply, "POST", "/api/assistant/apply", body, h))
    steps.append((apply, "POST", "/api/assistant/apply", {"insight": {}}, {**h, "Origin": "http://evil.example"}))
    texts = [
        "", "   \n\t", 5, [1, 2], {"a": 1}, True,
        "- first idea\n- second idea\n\n  continued para\nmore\n# Heading\n1. numbered\n2) also\n* star\n• bullet",
        "one line only", "x", "a\n\nb\n\ncc",
        "中文想法 一\r\n- 第二个 😀 idea\r\n\r\n３. fullwidth digit bullet",
        "line sep para\x1cfs\x85nel\vvt\fff",
        "- " + "长" * 130, "- " + "word " * 40, "-no space\n-\tTab bullet\n#### h4\n##### h5 is text",
        "\n".join(f"- item {i}" for i in range(70)),
    ]
    for text in texts:
        steps.append((extract, "POST", "/api/assistant/extract", {"text": text}, h))
    for limit in ("3", "abc", 2.7, [1], True, 0, -5, 100, None, "", " 2 ", "２", 1e300, "9" * 30):
        steps.append((extract, "POST", "/api/assistant/extract",
                      {"text": "- l1\n- l2\n- l3\n- l4", "limit": limit}, h))
    for source in (a, 1, "1", "NOPE-1", 0, "", 99999, True, 1.5, [1]):
        steps.append((extract, "POST", "/api/assistant/extract",
                      {"text": "- sourced one\n- sourced two", "source": source}, h))
    steps.append((extract, "POST", "/api/assistant/extract", {"text": "- x1"}, {**h, "Origin": "http://evil.example"}))
    steps.append(("exact:/api/activity", "GET", "/api/activity?limit=80", None, h))
    steps.append(("exact:/api/units", "GET", "/api/units?limit=1000&sort=created&facets=0", None, h))
    return steps


INFOHUB = "infohub"
INFOHUB_SCHEMA = """
CREATE TABLE messages(id INTEGER PRIMARY KEY, source TEXT NOT NULL, account TEXT NOT NULL DEFAULT '',
  item_key TEXT NOT NULL, channel TEXT, channel_name TEXT, is_group INTEGER DEFAULT 0, sender TEXT,
  sender_name TEXT, sent_at INTEGER, kind TEXT, text TEXT, link TEXT, mentions_me INTEGER DEFAULT 0,
  from_me INTEGER DEFAULT 0, flags TEXT, policy TEXT, priority TEXT, labels TEXT, rule TEXT,
  segment_id INTEGER, done INTEGER DEFAULT 0, collected_at INTEGER, UNIQUE(source, account, item_key));
CREATE TABLE units(id INTEGER PRIMARY KEY, key TEXT UNIQUE, kind TEXT, title TEXT, summary TEXT,
  status TEXT DEFAULT 'open', priority TEXT, due_at INTEGER, start_at INTEGER, end_at INTEGER, origin TEXT,
  confidence REAL, created_at INTEGER, updated_at INTEGER, last_activity_at INTEGER, reminded_at INTEGER,
  for_me INTEGER DEFAULT 0, polished INTEGER DEFAULT 0);
CREATE TABLE unit_tags(unit_id INTEGER, tag TEXT, PRIMARY KEY(unit_id, tag));
CREATE TABLE unit_links(parent_id INTEGER, child_id INTEGER, relation TEXT DEFAULT 'part_of',
  PRIMARY KEY(parent_id, child_id, relation));
CREATE TABLE unit_messages(unit_id INTEGER, message_id INTEGER, PRIMARY KEY(unit_id, message_id));
"""


def make_infohub(target: Path) -> dict:
    """InfoHub databases for the import replay (real schema, a minimal one, junk)."""
    import time
    target.mkdir(parents=True, exist_ok=True)
    full = target / "hub.db"
    if full.exists():
        return {"full": str(full), "minimal": str(target / "minimal.db"), "junk": str(target / "junk.db"),
                "empty": str(target / "empty.db")}
    old, recent = 978307200, int(time.time()) - 5 * 86400  # 2001-01-01 and five days ago
    con = sqlite3.connect(str(full))
    con.executescript(INFOHUB_SCHEMA)
    units = [
        # id, key, kind, title, summary, status, priority, due_at, start_at, end_at, conf, created, updated, last, for_me, polished
        (1, "topic-alpha", "topic", "Alpha topic", "  Alpha   summary\twith  spaces ", "open", "high", None, old, None, 0.5, old, old, old, 1, 0),
        (2, "todo-1", "todo", "Fix the 中文 parser", "x" * 300, "open", "LOW ", 1767225600000, None, None, None, old, recent, recent, 0, 1),
        (3, None, "event", "Workshop", "", "done", "p1", 1767225600, 1767139200, 1767312000, None, old, old, None, 0, 0),
        (4, "decision-9", "Decision", "Pick 😀 model", "Decided.", "open", "normal", None, None, None, 1.0, old, recent, recent, 1, 0),
        (5, "note-dropped", "note", "Dropped note", "gone", "dropped", None, None, None, None, None, old, old, old, 0, 0),
        (6, "q-1", "question", None, None, "open", "urgent", None, "abc", None, None, None, None, None, 0, 0),
        (7, "topic-beta", "topic", "Beta topic", "Beta summary", "open", "", None, None, None, None, old, old + 50, old + 60, 0, 0),
        (8, "misc-1", None, "Kindless item", "s", "open", "critical", None, -5, None, None, old, old, old, 0, 0),
        (9, "res-1", "resource", "Zeta resource", "r", "open", "p3", None, None, None, None, old, old, old, 0, 0),
        (10, "Kind Ünique/1", "Paper Review!", "Paper", "p", "open", "medium", None, None, None, None, old, old, old, 0, 0),
    ]
    con.executemany("INSERT INTO units(id, key, kind, title, summary, status, priority, due_at, start_at, end_at, "
                    "confidence, created_at, updated_at, last_activity_at, for_me, polished) "
                    "VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", units)
    tags = [(1, "area:ml"), (1, "source:slack"), (1, "type:topic"), (1, "plain tag"), (1, "status:x"),
            (2, "area:ml"), (2, "area:infra"), (2, "  "), (2, "t" * 120), (3, "source:mail"), (4, "area:infra"),
            (4, "importance:p0"), (7, "area:ml"), (7, "Mixed Case"), (9, "lang:中文")]
    con.executemany("INSERT INTO unit_tags VALUES (?,?)", tags)
    links = [(1, 2, "part_of"), (1, 3, "part_of"), (2, 4, "relates"), (4, 2, "part_of"), (7, 9, None),
             (7, 7, "part_of"), (99, 1, "part_of"), (2, 5, "part_of"), (9, 10, "blocks")]
    con.executemany("INSERT INTO unit_links VALUES (?,?,?)", links)
    msgs = [(1, "slack", "a1", "C1", "general", "u1", "Ann", old + 10, "text", "hello   world", None, 1, 0, "p1", "l"),
            (2, "slack", "a2", "C1", "general", "u2", None, old + 20, "text", "hello world", None, 0, 1, None, None),
            (3, "mail", "a3", None, None, None, None, None, None, "  second\nline ", "http://x", 0, 0, None, None),
            (4, "slack", "a4", "C2", "dev", "u3", "Bo", old + 5, "text", "", None, 0, 0, None, None),
            (5, "slack", "a5", "C2", "dev", "u3", "Bo", old + 30, "text", "y" * 200, None, 0, 0, None, None)]
    con.executemany("INSERT INTO messages(id, source, item_key, channel, channel_name, sender, sender_name, sent_at, "
                    "kind, text, link, mentions_me, from_me, priority, labels) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", msgs)
    con.executemany("INSERT INTO unit_messages VALUES (?,?)", [(1, 1), (1, 2), (1, 3), (2, 4), (2, 5), (4, 1), (9, 2)])
    con.commit()
    con.close()
    con = sqlite3.connect(str(target / "minimal.db"))
    con.executescript("CREATE TABLE units(id INTEGER PRIMARY KEY, key TEXT, kind TEXT, title TEXT);"
                      "CREATE TABLE unit_tags(unit_id INTEGER, tag TEXT);"
                      "CREATE TABLE unit_links(parent_id INTEGER, child_id INTEGER, relation TEXT);"
                      "INSERT INTO units VALUES (1, 'm-1', 'topic', 'Min one'), (2, NULL, NULL, NULL);"
                      "INSERT INTO unit_links VALUES (1, 2, 'part_of');")
    con.commit()
    con.close()
    (target / "junk.db").write_bytes(b"this is not a database\n" * 40)
    con = sqlite3.connect(str(target / "empty.db"))
    con.execute("CREATE TABLE other(x)")
    con.commit()
    con.close()
    return make_infohub(target)


def infohub_steps(fixture: dict) -> list[tuple]:
    import os
    h = {"X-Replay-Module": INFOHUB}
    env = os.environ.get("INFOHUB_DB")
    dbs = make_infohub(Path(env).parent if env else Path(fixture["data_dir"]).parent / "infohub")
    full, minimal, junk, empty = (quote(dbs[k]) for k in ("full", "minimal", "junk", "empty"))
    missing = quote(str(Path(dbs["full"]).parent / "missing.db"))
    steps = [("exact:/api/infohub/candidates", "GET", "/api/infohub/candidates", None, h),
             ("exact:/api/import/infohub/sources", "GET", "/api/import/infohub/sources", None, h),
             ("exact:/api/import/infohub/preview", "GET", "/api/import/infohub/preview", None, h)]
    for path in (full, minimal, junk, empty, missing, "", "~"):
        steps.append(("exact:/api/import/infohub/preview", "GET", f"/api/import/infohub/preview?path={path}", None, h))
    browse = "exact:/api/infohub/browse"
    queries = ["", "q=topic", "q=" + quote("中文 parser"), "q=%25", "kind=topic", "kind=topic,todo", "kind=Decision",
               "tags=area:ml", "tags=area:ml,area:infra", "tags=area:ml,source:slack", "tag=area:ml",
               "status=all", "status=dropped", "status=done", "days=30", "days=1e9", "days=abc", "days=-3",
               "days=nan", "mine=1", "mine=TRUE", "roots=1", "roots=on", "order=oldest", "order=due",
               "order=title", "order=created", "order=bogus", "limit=2&offset=1", "limit=0", "limit=x",
               "offset=99999999", "limit=3&order=title"]
    for query in queries:
        steps.append((browse, "GET", f"/api/infohub/browse?path={full}&{query}", None, h))
    steps.append((browse, "GET", "/api/infohub/browse", None, h))
    for path in (minimal, junk, empty, missing):
        steps.append((browse, "GET", f"/api/infohub/browse?path={path}", None, h))
    graph = "exact:/api/infohub/graph"
    for query in ("", "connected=0", "context=1", "context=1&connected=0", "kind=todo&context=1",
                  "limit=2&context=1", "q=Beta", "q=nothingmatches", "roots=1&connected=0", "status=all&connected=0"):
        steps.append((graph, "GET", f"/api/infohub/graph?path={full}&{query}", None, {**h, "Accept-Encoding": "gzip"}))
    steps.append((graph, "GET", f"/api/infohub/graph?path={minimal}", None, h))
    unit = "exact:/api/infohub/unit"
    for query in ("", "key=topic-alpha", "key=id-3", "id=3", "key=3", "id=topic-beta", "key=todo-1&id=1",
                  "key=nope", "key=" + quote("Kind Ünique/1"), "key=q-1", "key=decision-9", "key=topic-beta"):
        steps.append((unit, "GET", f"/api/infohub/unit?path={full}&{query}", None, h))
    steps.append((unit, "GET", f"/api/infohub/unit?path={minimal}&key=m-1", None, h))
    steps.append((unit, "GET", "/api/infohub/unit?key=x", None, h))
    imp, bulk = "exact:/api/infohub/import", "exact:/api/import/infohub"
    fp, mp = dbs["full"], dbs["minimal"]
    for body in ({"path": fp, "key": "todo-1"}, {"path": fp, "key": "todo-1"}, {"path": fp, "key": "id-3"},
                 {"path": fp, "key": 4, "as_idea": True}, {"path": fp, "key": "decision-9", "as_idea": 1},
                 {"path": fp, "key": "topic-alpha", "copy": True}, {"path": fp, "key": "topic-alpha", "copy": True},
                 {"path": fp, "key": "q-1"}, {"path": fp, "key": "misc-1"}, {"path": fp, "key": "Kind Ünique/1"},
                 {"path": fp, "key": "nope"}, {"path": fp}, {"key": "todo-1"}, {"path": 5, "key": "x"},
                 {"path": mp, "key": "m-1"}, {"path": fp, "key": "note-dropped"}):
        steps.append((imp, "POST", "/api/infohub/import", body, h))
    for body in ({"path": fp}, {"path": fp}, {"path": fp, "mode": "all"}, {"path": fp, "mode": "all", "update": True},
                 {"path": fp, "mode": "bogus"}, {"path": fp, "mode": ["x"]}, {"path": fp, "mode": 3}, {},
                 {"path": dbs["junk"]}, {"path": mp, "mode": "all"}, {"path": [1]}):
        steps.append((bulk, "POST", "/api/import/infohub", body, h))
    steps.append((imp, "POST", "/api/infohub/import", {"path": fp, "key": "todo-1"}, {**h, "Origin": "http://evil.example"}))
    for query in ("", "context=1&connected=0"):
        steps.append((graph, "GET", f"/api/infohub/graph?path={full}&{query}", None, h))
    steps.append((browse, "GET", f"/api/infohub/browse?path={full}&status=all", None, h))
    steps.append((unit, "GET", f"/api/infohub/unit?path={full}&key=todo-1", None, h))
    steps.append(("exact:/api/units", "GET", "/api/units?limit=1000&sort=created&facets=0", None, h))
    steps.append(("exact:/api/activity", "GET", "/api/activity?limit=60", None, h))
    return steps


OPEN = "host-open"


def open_steps(fixture: dict) -> list[tuple]:
    """POST /api/open and the local-folder bridge.  Nothing is launched: the
    replay shim fakes os.startfile / webbrowser / the opener subprocess for
    Python, and the Go server fakes them under ARK_REPLAY_CLOCK=1."""
    import os
    h = {"X-Replay-Module": OPEN}
    project = project_dir(fixture["data_dir"]) / "ark.sqlite3"
    con = sqlite3.connect(str(project))
    try:
        with_media = [r[0] for r in con.execute(
            "SELECT DISTINCT u.uid FROM units u JOIN unit_media m ON m.unit_id=u.id ORDER BY u.id")]
        without = [r[0] for r in con.execute(
            "SELECT uid FROM units WHERE id NOT IN (SELECT unit_id FROM unit_media) ORDER BY id LIMIT 2")]
    finally:
        con.close()
    route = "exact:/api/open"
    bodies = [{}, {"unit": ""}, {"url": "https://example.com/x?y=1"}, {"url": " HTTP://example.com "},
              {"url": "http://"}, {"url": "zotero://select/items/1"}, {"url": "javascript:alert(1)"},
              {"url": "data:text/plain,x"}, {"url": "noscheme"}, {"url": 5},
              {"path": "project.json"}, {"path": " assets/fig.png "}, {"path": "assets"}, {"path": "../x"},
              {"path": "/etc/passwd"}, {"path": "C:/Windows/win.ini"}, {"path": "a\\..\\project.json"},
              {"path": "nope.txt"}, {"path": "./"}, {"path": "a\x00b"}, {"path": "file://host/share/x"},
              {"path": "file:///C:/Windows/win.ini"}, {"path": "FILE:"}, {"path": "file:///"},
              {"session": "s-1"}, {"unit": "NOPE-1"}, {"unit": 1}, {"unit": True},
              {"path": "project.json", "url": "https://example.com"}]
    bodies += [{"unit": uid} for uid in with_media + without]
    # session + workspace-relative path (seeded finished sessions, replay_sessions.py)
    seeded = fixture.get("seeded_sessions") or {}
    sid = seeded.get("claude", "s_20260102030405claude1")
    bodies += [{"session": sid, "path": "artifacts/notes.md"}, {"session": sid, "path": " sub/a.csv "},
               {"session": sid, "path": "artifacts"}, {"session": sid, "path": "artifacts/nope.md"},
               {"session": sid, "path": "../meta.json"}, {"session": sid, "path": "/etc/passwd"},
               {"session": sid, "path": "artifacts/semi;name 空格.txt"},
               {"session": "s_doesnotexist1", "path": "x.txt"}, {"session": "bad id", "path": "x.txt"},
               {"session": seeded.get("codex", "s_20260102030405codex01"), "path": "x.txt"},
               {"session": seeded.get("hidden", "s_20260102030405hidden1"), "path": "x.txt"},
               {"session": "s_20260101000001def456", "path": "x.txt"}]
    steps = [(route, "POST", "/api/open", body, h) for body in bodies]
    steps.append((route, "POST", "/api/open", {"url": "https://example.com"}, {**h, "Origin": "http://evil.example"}))
    local = "exact:/api/local/open"
    origin = (os.environ.get("ARK_LOCAL_OPEN_ORIGINS") or "http://allowed.example").split(",")[0]
    roots = [r for r in (os.environ.get("ARK_LOCAL_OPEN_ROOTS") or "").split(os.pathsep) if r]
    root = roots[0] if roots else str(Path(fixture["data_dir"]).parent)
    for headers in ({}, {"Origin": "http://evil.example"}, {"Origin": origin}, {"Origin": origin + "/"}):
        steps.append((local, "OPTIONS", "/api/local/open", None, {**h, **headers}))
    for body in ({}, {"path": ""}, {"path": root}, {"path": str(Path(root) / "inside.txt")},
                 {"path": str(Path(root) / "sub")}, {"path": str(Path(root) / "missing.txt")},
                 {"path": str(Path(root).parent)}, {"path": root.upper()}, {"path": "relative.txt"}, {"path": 7}):
        steps.append((local, "POST", "/api/local/open", body, {**h, "Origin": origin}))
    steps.append((local, "POST", "/api/local/open", {"path": root}, {**h, "Origin": "http://evil.example"}))
    steps.append((local, "POST", "/api/local/open", {"path": root}, h))
    steps.append((local, "POST", "/api/local/open", "{", {**h, "Origin": origin}))
    return steps



CHAT = "chat"
CHAT_KEY_ENV = "REPLAY_CHAT_API_KEY"


class FakeLLM:
    """An OpenAI-compatible /chat/completions endpoint for the chat replay.

    Scripted by a "[[s:<name>]]" tag in the last user message; the round is
    the number of assistant messages after it.  Every request body (project
    name and path in the system prompt masked) is recorded per (scenario,
    round, stream); both servers must send identical requests
    (outbound_problems).
    """

    def __init__(self):
        import threading
        from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
        fake = self
        self.requests: dict = {}
        self.lock = threading.Lock()

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                import json
                raw = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                try:
                    body = json.loads(raw)
                except ValueError:
                    body = {}
                status, ctype, payload = fake.answer(body, dict(self.headers), raw, self.path)
                self.send_response(status)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(payload)))
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(payload)
                self.close_connection = True

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}/v1"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    @staticmethod
    def _scenario(messages: list) -> tuple:
        import re
        last = max((i for i, m in enumerate(messages) if isinstance(m, dict) and m.get("role") == "user"), default=-1)
        text = str(messages[last].get("content") or "") if last >= 0 else ""
        m = re.search(r"\[\[s:([a-z0-9_]+)\]\]", text)
        rounds = sum(1 for m2 in messages[last + 1:] if isinstance(m2, dict) and m2.get("role") == "assistant")
        return (m.group(1) if m else "none"), rounds, last

    def answer(self, body: dict, headers: dict, raw: bytes, path: str = ""):
        import json
        import re
        messages = body.get("messages") or []
        name, rnd, last = self._scenario(messages)
        forced = re.match(r"/t/([a-z0-9_]+)/", path)  # base_url .../t/<scenario>/v1 (assistant config test)
        if forced:
            name, rnd = forced.group(1), 0
        masked = re.sub(rb"Project path: (?:[^\\\"]|\\[^n])*", rb"Project path: <PATH>", raw)
        record = {"body": masked.decode("utf-8", "replace"),
                  "headers": {k: headers.get(k) for k in ("Authorization", "Content-Type", "Accept")}}
        with self.lock:
            self.requests.setdefault((name, rnd, bool(body.get("stream"))), []).append(record)
        tools = [m for m in messages[last + 1:] if isinstance(m, dict) and m.get("role") == "tool"]
        reply = self.script(name, rnd, tools)
        if isinstance(reply, tuple):  # (status, content type, raw body)
            return reply
        if body.get("stream"):
            return 200, "text/event-stream", self.sse(reply)
        reply = reply.get("__message__", reply)
        return 200, "application/json", json.dumps({"choices": [{"index": 0, "message": reply}]},
                                                   ensure_ascii=False).encode("utf-8")

    @staticmethod
    def sse(reply) -> bytes:
        import json
        if "__chunks__" in reply:
            return "".join(reply["__chunks__"]).encode("utf-8")
        if "__message__" in reply:
            reply = reply["__message__"]
        chunks = []
        text = reply.get("reasoning_content") or ""
        for i in range(0, len(text), 7):
            chunks.append({"choices": [{"index": 0, "delta": {"reasoning_content": text[i:i + 7]}}]})
        content = reply.get("content") or ""
        for i in range(0, len(content), 5):
            chunks.append({"choices": [{"index": 0, "delta": {"content": content[i:i + 5]}}]})
        for index, call in enumerate(reply.get("tool_calls") or []):
            fn = call.get("function") or {}
            args = fn.get("arguments") or ""
            half = len(args) // 2
            first = {"index": index, "function": {"name": fn.get("name"), "arguments": args[:half]}}
            if call.get("id"):
                first["id"] = call["id"]
            chunks.append({"choices": [{"index": 0, "delta": {"tool_calls": [first]}}]})
            chunks.append({"choices": [{"index": 0, "delta": {"tool_calls": [
                {"index": index, "function": {"arguments": args[half:]}}]}}]})
        chunks.append({"choices": [{"index": 1, "delta": {"content": "ignored second choice"}}]})
        chunks.append({"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
        return ("".join("data: " + json.dumps(c, ensure_ascii=False) + "\r\n\r\n" for c in chunks)
                + ": keep-alive comment\n\ndata: [DONE]\n\n").encode("utf-8")

    @staticmethod
    def call(i, name, args) -> dict:
        import json
        return {"id": f"call-{i}", "type": "function",
                "function": {"name": name, "arguments": args if isinstance(args, str) else json.dumps(args, ensure_ascii=False)}}

    def script(self, name: str, rnd: int, tools: list):
        import json
        c = self.call
        if name == "plain":
            return {"role": "assistant", "content": "Hello **world** 中文 😀 — see T-0001.",
                    "reasoning_content": "Thinking about the 项目 first."}
        if name == "tools":
            if rnd == 0:
                return {"role": "assistant", "content": "", "reasoning_content": "Inspect first.",
                        "tool_calls": [c(0, "get_context", {}), c(1, "search_units", {"query": "claim", "limit": 3}),
                                       c(2, "list_units", {"type": "task", "status": "open", "limit": "2"})]}
            if rnd == 1:
                return {"role": "assistant", "content": "Creating.",
                        "tool_calls": [c(3, "create_unit", {"type": "idea", "title": "LLM idea 中文", "body": "b",
                                                              "tags": ["area:llm"], "importance": "p1"})]}
            if rnd == 2:
                created = {}
                for t in tools:
                    try:
                        created = json.loads(t.get("content") or "{}")
                    except ValueError:
                        pass
                uid = created.get("uid") or "NOPE-1"
                return {"role": "assistant", "content": "",
                        "tool_calls": [c(4, "update_unit", {"ref": uid, "status": "active", "tags": ["x", "y"]}),
                                       c(5, "link_units", {"source": uid, "target": uid, "relation": "supports"}),
                                       c(6, "get_unit", {"ref": uid})]}
            return {"role": "assistant", "content": "Done: created and linked.", "reasoning_content": "All set."}
        if name == "badtools":
            if rnd == 0:
                return {"role": "assistant", "content": "x",
                        "tool_calls": [c(0, "nope_tool", {}), c(1, "get_unit", {"ref": "NOPE-1"}),
                                       c(2, "search_units", {"query": "a", "limit": "abc"}), c(3, "list_units", "{not json"),
                                       c(4, "create_unit", {"type": "bogus", "title": ""}),
                                       c(5, "link_units", {"source": "X-1"}), c(6, "get_infohub_unit", {"key": "nope"}),
                                       c(7, "list_units", "[1, 2]"), {"type": "function", "function": {"name": "get_context"}}]}
            return {"role": "assistant", "content": "Handled the errors."}
        if name == "infohub":
            if rnd == 0:
                return {"role": "assistant", "content": "",
                        "tool_calls": [c(0, "get_infohub_context", {}),
                                       c(1, "search_infohub", {"query": "topic", "limit": 5, "days": 0}),
                                       c(2, "get_infohub_unit", {"key": "topic-alpha"}),
                                       c(3, "search_infohub", {"kind": "todo"})]}
            return {"role": "assistant", "content": "InfoHub summarised."}
        if name == "dsml":
            if rnd == 0:
                return {"role": "assistant", "content": (
                    "Let me look.\n<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"search_units\">\n"
                    "<｜DSML｜parameter name=\"query\" string=\"true\">claim &amp; idea</｜DSML｜parameter>\n"
                    "<｜DSML｜parameter name=\"limit\" string=\"false\">2</｜DSML｜parameter>\n"
                    "</｜DSML｜invoke>\n<｜DSML｜invoke name='get_context'></｜DSML｜invoke>\n</｜DSML｜function_calls>")}
            return {"role": "assistant", "content": "Answer <b>bold</b>\n\n\n\nafter < | | DSML stray\n||DSML line"}
        if name == "empty":
            return {"role": "assistant", "content": "   "}
        if name == "limit":
            return {"role": "assistant", "content": f"round {rnd}", "tool_calls": [c(rnd, "get_context", {})]}
        if name == "http500":
            return 500, "text/plain", "upstream exploded 中文".encode("utf-8")
        if name == "http404":
            return 404, "text/plain", b""
        if name == "unexpected":
            return 200, "application/json", b'{"choices": []}'
        if name == "strmsg":
            return 200, "application/json", b'{"choices": [{"message": "plain"}]}'
        if name == "badjson":
            return 200, "application/json", b"not json at all"
        if name == "long":
            return {"role": "assistant", "content": "y" * 250 + "中文"}
        if name == "partial":
            if rnd == 0:
                text = ["Partial <", "b> and <", " |", "｜DSML｜invoke name=\"get_context\"></｜DSML｜invoke>"]
                chunks = [{"choices": [{"index": 0, "delta": {"content": t}}]} for t in text]
                chunks.append({"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]})
                return {"__chunks__": ["data: " + json.dumps(x, ensure_ascii=False) + "\n\n" for x in chunks]
                        + ["data: [DONE]\n\n"], "__message__": {"role": "assistant", "content": "".join(text)}}
            return {"role": "assistant", "content": "Final <"}
        if name == "streamerr":
            return {"__chunks__": ['data: {"choices": [{"index": 0, "delta": {"content": "par"}}]}\n\n',
                                   'data: {"error": {"message": "rate limited 限流"}}\n\n'],
                    "__message__": {"role": "assistant", "content": "non-stream ok"}}
        if name == "nodone":
            return {"__chunks__": ['data: {"choices": [{"index": 0, "delta": {"content": "cut"}}]}\n\n'],
                    "__message__": {"role": "assistant", "content": None}}
        if name == "attr":
            return {"__chunks__": ['data: {"choices": [{"index": 0, "delta": ["x"]}]}\n\n'],
                    "__message__": {"role": "assistant", "content": 5}}
        return {"role": "assistant", "content": f"default reply for {name}"}

    def problems(self) -> list:
        out = []
        for key, items in sorted(self.requests.items(), key=lambda kv: str(kv[0])):
            if len(items) % 2:
                out.append(f"outbound {key}: {len(items)} requests (odd: one server sent more)")
                continue
            for i in range(0, len(items), 2):
                if items[i] != items[i + 1]:
                    a, b = items[i]["body"], items[i + 1]["body"]
                    at = next((j for j in range(min(len(a), len(b))) if a[j] != b[j]), min(len(a), len(b)))
                    out.append(f"outbound {key} #{i // 2}: python {a[max(0, at - 80):at + 120]!r} != "
                               f"go {b[max(0, at - 80):at + 120]!r} headers equal: "
                               f"{items[i]['headers'] == items[i + 1]['headers']}")
        return out


_FAKE: list = []


def fake_llm() -> FakeLLM:
    if not _FAKE:
        _FAKE.append(FakeLLM())
    return _FAKE[0]


def outbound_problems() -> list:
    return (_FAKE[0].problems() if _FAKE else []) + replay_gpu.fake_ssh_problems()


def prepare_env() -> None:
    """Environment both servers inherit for the chat replay: the project
    assistant key variable, and no real provider key or endpoint."""
    import os
    for name in ("DEEPSEEK_API_KEY", "ARK_DEEPSEEK_API_KEY", "TENCENTHUB_API_KEY", "OPENAI_API_KEY",
                 "ARK_CHAT_MODE", "ARK_DSH_COMMAND"):
        os.environ.pop(name, None)
    os.environ["DEEPSEEK_BASE_URL"] = "http://127.0.0.1:9/v1"  # never reached: no global key
    os.environ[CHAT_KEY_ENV] = "sk-replay-chat-0123"
    os.environ["ARK_PROJECT_ASSISTANT_KEY_ENVS"] = CHAT_KEY_ENV
    if _DSH:  # --only chat-dsh: every chat goes through the fake harness
        os.environ["ARK_DSH_COMMAND"] = _DSH[0]
    replay_gpu.prepare_env()  # GPU: fake ssh, default server list location
    replay_setup.prepare_env()  # setup: a set secret reference, no provider CLI installed


def chat_steps(fixture: dict) -> list:
    import os
    h = {"X-Replay-Module": CHAT}
    refs = _refs(Path(fixture["data_dir"]))
    tasks = [uid for uid, _ in refs.get("task", [])]
    others = [items[0][0] for kind, items in sorted(refs.items()) if kind != "task" and items]
    t1 = tasks[0] if tasks else "T-1"
    o1, o2 = (others + ["X-1", "X-2"])[:2]
    get, status, post, stream = "exact:/api/chat", "exact:/api/chat/status", "exact:/api/chat", "exact:/api/chat/stream"
    sett = "exact:/api/project/settings"
    steps = [(status, "GET", "/api/chat/status", None, h)]
    for q in ("", "?limit=1", "?limit=abc", "?limit=999", "?limit=0"):
        steps.append((get, "GET", "/api/chat" + q, None, h))
    local = ["help", "帮助!", "What can you do?", "summary", "What should I do next?", "下一步",
             "create idea: Learned recovery | Test it due 2026-03-01", "create task Fix parser due 2026-02-01",
             "Create a claim about scaling", "新建任务 写论文", "记下 一个想法", "add a note", "idea: quick one",
             "create question", f"link {o1} supports {o2}", f"{o1} supports {o2}.", "link X-1 to Y-2 as bogus",
             f"connect {o1} with {o2} via cites", f"mark {t1} done", f"mark {t1} nonsense", f"set {t1} to review",
             f"把 {t1} 标记为 active", "mark NOPE-9 done", f"dispatch {t1}", f"dispatch {o1}", "dispatch NOPE-9",
             "search claim", "find: 中文", "search zzzzqqq", "scaling experiments please", "qwerty asdfgh",
             "the and", "x" * 8000, "  padded message。", "Ünïcode ÉLAN test",
             "[[s:x]] <｜DSML｜invoke name=\"x\">hidden</｜DSML｜invoke> visible"]
    for text in local:
        steps.append((post, "POST", "/api/chat", {"message": text}, h))
    for body in ({}, {"message": ""}, {"message": "   "}, {"message": 5}, {"message": ["help"]},
                 {"message": "x" * 8001}, {"message": "help", "infohub": "not a dict"}, "[1]", "{"):
        steps.append((post, "POST", "/api/chat", body, h))
    steps.append((post, "POST", "/api/chat", {"message": "help"}, {**h, "Origin": "http://evil.example"}))
    for body in ({"message": "help"}, {"message": "summary"}, {"message": ""}, {"message": "x" * 8001},
                 {"message": f"mark {t1} open"}, "[1]", "{"):
        steps.append((stream, "POST", "/api/chat/stream", body, h))
    steps.append((stream, "POST", "/api/chat/stream", {"message": "help"}, {**h, "Origin": "http://evil.example"}))
    steps.append((get, "GET", "/api/chat?limit=400", None, h))
    # Model-backed chat through the fake endpoint (project assistant settings).
    url = fake_llm().url
    steps.append((sett, "PATCH", "/api/project/settings", {"assistant": {
        "base_url": url, "model": "fake-model", "api_key_env": CHAT_KEY_ENV, "reasoning_effort": "low",
        "timeout": 20}}, h))
    steps.append((get, "GET", "/api/chat?limit=3", None, h))
    steps.append((status, "GET", "/api/chat/status", None, h))
    hub = os.environ.get("INFOHUB_DB")
    dbs = make_infohub(Path(hub).parent if hub else Path(fixture["data_dir"]).parent / "infohub")
    hub_ctx = {"path": dbs["full"], "filters": {"q": "", "kinds": ["topic", "todo"], "tags": [], "days": 0,
                                                "connected": False, "context": True}, "visible": False}
    scenarios = ["plain", "tools", "badtools", "dsml", "empty", "limit", "http500", "http404", "unexpected",
                 "partial", "streamerr", "nodone", "attr", "none"]
    for kind, route, path in (("send", post, "/api/chat"), ("stream", stream, "/api/chat/stream")):
        for name in scenarios:
            steps.append((route, "POST", path, {"message": f"[[s:{name}]] please {kind} 中文"}, h))
        steps.append((route, "POST", path, {"message": f"[[s:infohub]] what is in InfoHub? {kind}", "infohub": hub_ctx}, h))
        steps.append((route, "POST", path, {"message": f"[[s:plain]] with an empty infohub {kind}", "infohub": {}}, h))
        steps.append((route, "POST", path, {"message": f"[[s:plain]] bad infohub path {kind}",
                                            "infohub": {"path": dbs["junk"], "filters": "x"}}, h))
    steps.append((get, "GET", "/api/chat?limit=60", None, h))
    # An endpoint that refuses connections, then the local harness again.
    steps.append((sett, "PATCH", "/api/project/settings", {"assistant": {"base_url": "http://127.0.0.1:9/v1"}}, h))
    steps.append((post, "POST", "/api/chat", {"message": "[[s:plain]] refused"}, h))
    steps.append((stream, "POST", "/api/chat/stream", {"message": "[[s:plain]] refused stream"}, h))
    steps.append((sett, "PATCH", "/api/project/settings", {"assistant": None}, h))
    steps.append((get, "GET", "/api/chat?limit=5", None, h))
    steps.append(("exact:/api/chat", "DELETE", "/api/chat", None, h))
    steps.append((get, "GET", "/api/chat", None, h))
    steps.append((post, "POST", "/api/chat", {"message": "help"}, h))
    steps.append(("exact:/api/units", "GET", "/api/units?limit=1000&sort=created&facets=0", None, h))
    steps.append(("exact:/api/activity", "GET", "/api/activity?limit=120", None, h))
    return steps



CHAT_DSH = "chat-dsh"
FAKE_DSH = r'''
import hashlib, re, sys
raw = sys.stdin.buffer.read()
norm = re.sub(rb"API base: http://[^\r\n]*", b"API base: <BASE>", raw)
norm = re.sub(rb"Project: [^\r\n]*", b"Project: <PROJECT>", norm)
text = raw.decode("utf-8", "replace")
request = text.split("User request:", 1)[-1].strip()
if "fail" in request:
    sys.exit(3)
if "stderr" in request:
    sys.stderr.write("only stderr \u4e2d\u6587\r\n")
    sys.exit(0)
if "silent" in request:
    sys.exit(0)
sys.stdout.buffer.write(("DSH ok " + hashlib.sha256(norm).hexdigest()[:16] + " crlf=" + str(raw.count(b"\r\n"))
                         + "\r\nrequest: " + request + "\rtail \u00e9\n\n").encode("utf-8") + b"\xff end")
'''
_DSH: list = []


def dsh_command() -> str:
    """ARK_DSH_COMMAND for the chat-dsh module: the fake harness script."""
    import tempfile
    if not _DSH:
        script = Path(tempfile.mkdtemp(prefix="ark-fake-dsh-")) / "fake_dsh.py"
        script.write_text(FAKE_DSH, encoding="utf-8")
        _DSH.append(f"{sys.executable} -I {script}")
    return _DSH[0]


def chat_dsh_steps(fixture: dict) -> list:
    """The DeepSeek Harness subprocess (ARK_DSH_COMMAND, a fake script)."""
    h = {"X-Replay-Module": CHAT_DSH}
    post, stream = "exact:/api/chat", "exact:/api/chat/stream"
    steps = [("exact:/api/chat/status", "GET", "/api/chat/status", None, h),
             ("exact:/api/chat", "GET", "/api/chat?limit=2", None, h)]
    for text in ("summarise the project 中文", "please fail now", "stderr please", "silent please", "help"):
        steps.append((post, "POST", "/api/chat", {"message": text}, h))
    steps.append((post, "POST", "/api/chat", {"message": "with infohub", "infohub": {"path": "E:/x/hub.db", "filters": {"q": "a"}}}, h))
    steps.append((post, "POST", "/api/chat", {"message": "host header"}, {**h, "Host": "localhost:{port}"}))
    for text in ("stream it", "stream fail now"):
        steps.append((stream, "POST", "/api/chat/stream", {"message": text}, h))
    steps.append(("exact:/api/chat", "GET", "/api/chat?limit=40", None, h))
    return steps


AVATAR = "avatar-writes"


def avatar_steps(fixture: dict) -> list:
    """Writes to /api/users/<name>/avatar: always 405 after the guards."""
    h = {"X-Replay-Module": AVATAR}
    route = r"regex:^/api/users/([^/]+)/avatar$"
    names = [fixture.get("owner") or "local", "local", (fixture.get("users") or ["user0001"])[0], "nobody",
             quote("中文"), "a%2Fb", "%C3%28"]
    steps = []
    for name in names:
        for method, body in (("POST", {"data": "aGk=", "content_type": "image/png"}), ("PATCH", {}),
                             ("DELETE", None), ("POST", "not json"), ("PATCH", "x" * 100_000)):
            steps.append((route, method, f"/api/users/{name}/avatar", body, h))
    for method in ("POST", "PATCH", "DELETE"):
        steps.append((route, method, "/api/users/local/avatar", {}, {**h, "Origin": "http://evil.example"}))
        steps.append((route, method, "/api/users/local/avatar", {}, {**h, "X-Replay-NoCookie": "1"}))
        steps.append((route, method, "/api/users/local/avatar?v=1", {}, h))
        steps.append((route, method, "/api/users/local/avatar", {}, {**h, "Host": "evil.example"}))
    steps.append((route, "GET", "/api/users/local/avatar", None, h))
    return steps


def steps(fixture: dict) -> list[tuple]:
    refs = _refs(Path(fixture["data_dir"]))
    return (assistant_steps(refs) + infohub_steps(fixture) + open_steps(fixture) + chat_steps(fixture)
            + avatar_steps(fixture) + replay_gpu.gpu_steps(fixture) + replay_setup.setup_steps(fixture))


MODULES = {ASSISTANT: lambda fixture: assistant_steps(_refs(Path(fixture["data_dir"]))),
           INFOHUB: infohub_steps, OPEN: open_steps, CHAT: chat_steps,
           CHAT_DSH: chat_dsh_steps, AVATAR: avatar_steps, GPU: replay_gpu.gpu_steps,
           SETUP: replay_setup.setup_steps}


def main(argv=None) -> int:
    import argparse
    import diffreplay
    import replay_writes
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--go", required=True)
    parser.add_argument("--auth", action="store_true")
    parser.add_argument("--owner", action="store_true")
    parser.add_argument("--only", default="", help="one module: " + ", ".join(MODULES))
    parser.add_argument("--trace", default="")
    parser.add_argument("--show", type=int, default=15)
    args = parser.parse_args(argv)

    if args.only == CHAT_DSH:
        dsh_command()

    def only(fixture):
        if not args.only:
            return steps(fixture)
        return MODULES[args.only](fixture)

    import os
    import tempfile
    # INFOHUB_DB for both servers (default_candidates); the database itself is
    # created by infohub_steps before the first request.
    os.environ.setdefault("INFOHUB_DB", str(Path(tempfile.mkdtemp(prefix="ark-infohub-")) / "hub.db"))
    # The local-folder bridge: an allowed origin and one root with a file and a folder.
    if not os.environ.get("ARK_LOCAL_OPEN_ROOTS"):
        root = Path(tempfile.mkdtemp(prefix="ark-local-open-"))
        (root / "inside.txt").write_text("x", encoding="utf-8")
        (root / "sub").mkdir()
        os.environ["ARK_LOCAL_OPEN_ROOTS"] = str(root)
        os.environ.setdefault("ARK_LOCAL_OPEN_ORIGINS", "http://allowed.example, http://other.example/")
    # Quick mode: every other step list of the full replay is empty.
    diffreplay.read_steps = lambda units: []
    diffreplay.view_steps = lambda *a, **k: []
    diffreplay.agent_steps = lambda *a, **k: []
    diffreplay.write_steps = lambda units: []
    replay_writes.agent_write_steps = lambda fixture: []
    replay_writes.write_steps = only
    diffreplay.replay_services_steps = lambda fixture: []
    result = diffreplay.replay(args.go, auth_users=3 if args.auth or args.owner else 0,
                               as_owner=args.owner, trace=args.trace)
    for item in result["mismatches"][:args.show]:
        print(f"#{item['n']} {item['method']} {item['path']}")
        for problem in item["problems"]:
            print("    " + problem[:600])
    for name, (compared, bad) in sorted(result.get("modules", {}).items()):
        print(f"  module {name:20} compared={compared:5} mismatches={bad}")
    for problem in result.get("state", [])[:args.show]:
        print("  state: " + problem[:600])
    if _FAKE:
        print(f"  fake model endpoint: {sum(len(v) for v in _FAKE[0].requests.values())} requests "
              f"({len(_FAKE[0].requests)} scenario rounds) compared between the servers")
    if replay_gpu.fake_ssh_calls():
        print(f"  fake ssh: {replay_gpu.fake_ssh_calls()} invocations compared between the servers")
    print(f"steps={result['steps']} compared={result['compared']} skipped={result['skipped']} "
          f"mismatches={len(result['mismatches'])} state_diffs={len(result.get('state', []))}")
    return 1 if result["mismatches"] or result.get("state") else 0


if __name__ == "__main__":
    # Run as the imported module so diffreplay.py and this script share one
    # fake model endpoint and one fake harness command.
    import replay_services
    raise SystemExit(replay_services.main())
