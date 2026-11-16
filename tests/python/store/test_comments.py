"""Unit comments (db v4): storage, body-note migration, API and Needs-you."""
import json
import os
import sqlite3
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio import db
from arkstudio.export import project_json
from arkstudio.server import App, serve
from arkstudio.store import StoreError
from tests.python.store.test_store import make_store

NL = chr(10)

# A project database as ARK wrote it before v4: no unit_comments table, no
# migration flag, notes appended to bodies.
V3_SCHEMA = db.SCHEMA.split("-- v4:")[0] + db.SCHEMA[db.SCHEMA.index("CREATE TABLE IF NOT EXISTS activity("):]


def make_v3_db(path: Path, bodies: dict[str, str]) -> None:
    con = sqlite3.connect(str(path))
    con.executescript(V3_SCHEMA)
    now = db.utc_now()
    for uid, body in bodies.items():
        con.execute(
            "INSERT INTO units(uid,type,title,body,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)",
            (uid, "task", f"Unit {uid}", body, "review", now, "2026-01-01T00:00:00+00:00"))
    con.execute("INSERT INTO meta(key,value) VALUES('schema_version','3')")
    con.commit()
    con.close()


LEGACY = NL.join([
    "## Result", "AUC 0.82 on 3 seeds.", "",
    "**User comment (2026-11-04):** check seed 2", "",
    "**Agent reply (2026-11-05):** seed 2 rerun, same result", "",
    "**Sent back by user (2026-11-06):** add the variance", ""])


class CommentStoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def unit(self, status="review", type="task", body="Result"):
        return self.store.create_unit({"title": "Node", "type": type, "status": status, "body": body})

    def test_comments_survive_a_body_rewrite_and_thread(self):
        unit = self.unit()
        first = self.store.add_comment(unit["uid"], "  check the seeds  ", author="alice")
        self.assertEqual(first["body"], "check the seeds")
        self.assertEqual((first["author"], first["author_kind"], first["kind"]), ("alice", "user", "comment"))
        reply = self.store.add_comment(unit["uid"], "rerunning", "agent_reply", reply_to=first["id"],
                                       actor="agent:claude")
        self.assertEqual((reply["author"], reply["reply_to"]), ("agent:claude", first["id"]))
        self.store.update_unit(unit["uid"], {"body": "rewritten by an agent"})
        got = self.store.get_unit(unit["uid"])
        self.assertEqual([c["id"] for c in got["comments"]], [first["id"], reply["id"]])
        self.assertEqual((got["comments_count"], got["comments_open"]), (1, 1))
        actions = [a["action"] for a in got["activity"]]
        self.assertEqual(actions.count("comment.create"), 2)

    def test_resolve_is_reversible_and_comments_are_not_deleted(self):
        unit = self.unit()
        c = self.store.add_comment(unit["uid"], "fix the axis label")
        done = self.store.resolve_comment(unit["uid"], c["id"], True, actor="agent:codex")
        self.assertTrue(done["resolved"])
        self.assertEqual(done["resolved_by"], "agent:codex")
        self.assertEqual(self.store.get_unit(unit["uid"])["comments_open"], 0)
        again = self.store.resolve_comment(unit["uid"], c["id"], False, author="bob")
        self.assertFalse(again["resolved"])
        self.assertIsNone(again["resolved_at"])
        self.assertEqual(self.store.get_unit(unit["uid"])["comments_open"], 1)
        actions = [a["action"] for a in self.store.activity(unit_id=unit["id"])]
        self.assertIn("comment.resolve", actions)
        self.assertIn("comment.reopen", actions)
        self.assertFalse(hasattr(self.store, "delete_comment"))

    def test_kind_rules(self):
        unit = self.unit()
        other = self.unit()
        with self.assertRaises(StoreError):
            self.store.add_comment(unit["uid"], "   ")
        with self.assertRaises(StoreError):
            self.store.add_comment(unit["uid"], "x", "shout")
        with self.assertRaises(StoreError):  # agents only reply
            self.store.add_comment(unit["uid"], "x", "comment", actor="agent:a")
        with self.assertRaises(StoreError):  # people do not write agent replies
            self.store.add_comment(unit["uid"], "x", "agent_reply")
        c = self.store.add_comment(other["uid"], "on another unit")
        with self.assertRaises(StoreError):  # reply_to must be on the same unit
            self.store.add_comment(unit["uid"], "x", "agent_reply", reply_to=c["id"], actor="agent:a")
        with self.assertRaises(StoreError):
            self.store.resolve_comment(unit["uid"], c["id"])

    def test_needs_you_counts_open_user_comments(self):
        unit = self.unit()
        a = self.store.add_comment(unit["uid"], "one")
        self.store.add_comment(unit["uid"], "two", "sent_back")
        self.store.add_comment(unit["uid"], "reply", "agent_reply", reply_to=a["id"], actor="agent:x")
        self.store.resolve_comment(unit["uid"], a["id"])
        item = next(i for i in self.store.needs_you() if i["uid"] == unit["uid"])
        self.assertEqual((item["comments"], item["comments_open"]), (2, 1))
        listed = self.store.list_units({"ids": [unit["id"]]})["items"][0]
        self.assertEqual((listed["comments_count"], listed["comments_open"]), (2, 1))

    def test_borrowed_media_is_labelled(self):
        story = self.unit(type="story")
        exp = self.store.create_unit({"title": "Sweep", "type": "experiment", "status": "done"})
        self.store.add_media(exp["uid"], "figs/sweep.png", "image", "sweep")
        self.store.create_link(exp["uid"], story["uid"], "supports")
        item = next(i for i in self.store.needs_you() if i["uid"] == story["uid"])
        self.assertEqual(item["own_media"], 0)
        self.assertEqual(item["media"][0]["from"], {"id": exp["id"], "uid": exp["uid"], "title": "Sweep",
                                                    "type": "experiment"})
        # A non-previewable own attachment does not count as own media.
        self.store.add_media(story["uid"], "notes.pdf", "pdf")
        item = next(i for i in self.store.needs_you() if i["uid"] == story["uid"])
        self.assertEqual(item["own_media"], 0)
        self.store.add_media(story["uid"], "figs/own.png", "image", "own")
        item = next(i for i in self.store.needs_you() if i["uid"] == story["uid"])
        self.assertEqual(item["own_media"], 1)
        self.assertEqual([m.get("from") for m in item["media"]], [None])

    def test_export_includes_comments(self):
        unit = self.unit()
        self.store.add_comment(unit["uid"], "keep me in the export")
        data = project_json(self.store)
        self.assertEqual([c["body"] for c in data["comments"]], ["keep me in the export"])
        self.assertEqual(data["comments"][0]["unit_uid"], unit["uid"])


class BodyNoteMigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "ark.sqlite3"

    def tearDown(self):
        self.tmp.cleanup()

    def rows(self, con, uid):
        return [dict(r) for r in con.execute(
            "SELECT c.* FROM unit_comments c JOIN units u ON u.id=c.unit_id WHERE u.uid=? ORDER BY c.id", (uid,))]

    def test_moves_notes_out_of_the_body(self):
        make_v3_db(self.path, {"T-0001": LEGACY, "T-0002": "no notes here"})
        con = db.connect(self.path)
        try:
            rows = self.rows(con, "T-0001")
            self.assertEqual([(r["kind"], r["author"], r["body"], r["created_at"][:10]) for r in rows], [
                ("comment", "user", "check seed 2", "2026-11-04"),
                ("agent_reply", "agent", "seed 2 rerun, same result", "2026-11-05"),
                ("sent_back", "user", "add the variance", "2026-11-06"),
            ])
            self.assertEqual(rows[1]["reply_to"], rows[0]["id"])  # reply threads under the comment before it
            self.assertTrue(all(r["resolved_at"] is None for r in rows))
            unit = con.execute("SELECT body, updated_at FROM units WHERE uid='T-0001'").fetchone()
            self.assertEqual(unit["body"], "## Result" + NL + "AUC 0.82 on 3 seeds." + NL)
            self.assertEqual(unit["updated_at"], "2026-01-01T00:00:00+00:00")  # not an edit
            self.assertEqual(con.execute("SELECT body FROM units WHERE uid='T-0002'").fetchone()[0], "no notes here")
            self.assertEqual(db.meta_get(con, db.BODY_NOTES_MIGRATED), "1")
            self.assertEqual(db.meta_get(con, "schema_version"), str(db.SCHEMA_VERSION))
            log = con.execute("SELECT actor, action, detail_json FROM activity WHERE action LIKE 'comment.%'").fetchall()
            self.assertEqual([(a["actor"], a["action"]) for a in log], [("system:migration", "comment.migrate")])
            self.assertEqual(json.loads(log[0]["detail_json"])["count"], 3)
        finally:
            con.close()

    def test_idempotent_on_reconnect_and_without_the_flag(self):
        make_v3_db(self.path, {"T-0001": LEGACY})
        db.connect(self.path).close()
        con = db.connect(self.path)
        self.assertEqual(len(self.rows(con, "T-0001")), 3)
        # Even if the flag is lost and the old body comes back (a partial
        # restore), nothing is added twice: identical rows are skipped.
        con.execute("DELETE FROM meta WHERE key=?", (db.BODY_NOTES_MIGRATED,))
        con.execute("UPDATE units SET body=? WHERE uid='T-0001'", (LEGACY,))
        con.commit()
        con.close()
        con = db.connect(self.path)
        try:
            self.assertEqual(len(self.rows(con, "T-0001")), 3)
            self.assertNotIn("User comment", con.execute("SELECT body FROM units").fetchone()[0])
        finally:
            con.close()

    def test_only_exact_lines_leave_the_body(self):
        body = NL.join([
            "Intro", "",
            "**Agent reply (2026-11-05):** first line", "continued on a second line", "",
            "  **User comment (2026-11-04):** indented, not a note line", "",
            "**User comment (2026-11-04):** exact", ""])
        make_v3_db(self.path, {"T-0001": body})
        con = db.connect(self.path)
        try:
            rows = self.rows(con, "T-0001")
            self.assertEqual([(r["kind"], r["body"]) for r in rows], [
                ("agent_reply", "first line continued on a second line"), ("comment", "exact")])
            left = con.execute("SELECT body FROM units").fetchone()[0]
            self.assertIn("**Agent reply (2026-11-05):** first line" + NL + "continued", left)
            self.assertIn("indented, not a note line", left)
            self.assertNotIn("** exact", left)
        finally:
            con.close()

    def test_fresh_database(self):
        con = db.connect(self.path)
        try:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM unit_comments").fetchone()[0], 0)
            self.assertEqual(db.meta_get(con, db.BODY_NOTES_MIGRATED), "1")
        finally:
            con.close()


class CommentApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=True)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"
        cls.token = cls.app.manager.agent_token

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()
        if cls._old_chat_mode is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = cls._old_chat_mode

    def request(self, path, *, method="GET", body=None, agent=False):
        headers = {"Content-Type": "application/json"}
        if agent:
            headers.update({"Authorization": f"Bearer {self.token}", "X-Agent-Name": "tester"})
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def test_ui_and_agent_round_trip(self):
        status, data = self.request("/api/units", method="POST",
                                    body={"title": "Awaiting acceptance", "type": "task", "status": "review"})
        self.assertEqual(status, 200, data)
        uid = data["unit"]["uid"]
        status, data = self.request(f"/api/units/{uid}/comments", method="POST",
                                    body={"body": "add the seed variance", "kind": "sent_back"})
        self.assertEqual(status, 200, data)
        cid = data["comment"]["id"]
        self.assertEqual(data["comment"]["author_kind"], "user")
        status, data = self.request(f"/api/units/{uid}/comments", method="POST",
                                    body={"body": "pretending to be an agent", "kind": "agent_reply"})
        self.assertEqual(status, 400)

        # Agent API: the unit and the comment list carry the open user comments.
        status, data = self.request(f"/api/agent/v1/units/{uid}", agent=True)
        self.assertEqual(status, 200, data)
        self.assertEqual(data["unit"]["comments_open"], 1)
        self.assertEqual([c["body"] for c in data["unit"]["comments"]], ["add the seed variance"])
        status, data = self.request(f"/api/agent/v1/units?ids={data['unit']['id']}", agent=True)
        self.assertEqual(data["items"][0]["comments_open"], 1)
        status, data = self.request(f"/api/agent/v1/units/{uid}/comments", agent=True)
        self.assertEqual((data["total"], data["open_user_comments"]), (1, 1))

        status, data = self.request(f"/api/agent/v1/units/{uid}/comments", method="POST", agent=True,
                                    body={"body": "variance added to the table", "reply_to": cid})
        self.assertEqual(status, 200, data)
        self.assertEqual((data["comment"]["kind"], data["comment"]["author"]), ("agent_reply", "agent:tester"))
        self.assertEqual(data["comment"]["reply_to"], cid)
        status, data = self.request(f"/api/agent/v1/units/{uid}/comments", method="POST", agent=True,
                                    body={"body": "an agent cannot comment as the user", "kind": "comment"})
        self.assertEqual(status, 400)
        status, data = self.request(f"/api/agent/v1/units/{uid}/comments/{cid}/resolve", method="POST",
                                    agent=True, body={})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["open_user_comments"], 0)
        self.assertEqual(data["comment"]["resolved_by"], "agent:tester")
        status, _ = self.request(f"/api/agent/v1/units/{uid}/comments", method="POST",
                                 body={"body": "no token"})
        self.assertEqual(status, 401)

        # The user can reopen it; the needs-you row counts it again.
        status, data = self.request(f"/api/units/{uid}/comments/{cid}/resolve", method="POST",
                                    body={"resolved": False})
        self.assertEqual(status, 200, data)
        self.assertFalse(data["comment"]["resolved"])
        status, project = self.request("/api/project")
        item = next(i for i in project["needs_you"] if i["uid"] == uid)
        self.assertEqual((item["comments"], item["comments_open"]), (1, 1))
        status, unit = self.request(f"/api/units/{uid}")
        actions = [a["action"] for a in unit["unit"]["activity"]]
        for action in ("comment.create", "comment.resolve", "comment.reopen"):
            self.assertIn(action, actions)
        status, data = self.request(f"/api/units/{uid}/comments")
        self.assertEqual(len(data["comments"]), 2)


if __name__ == "__main__":
    unittest.main()
