"""Default node dates and creator ownership, plus the v5 backfill.

* ``start_at`` defaults to the creation date, ``end_at`` to the completion
  date when a unit becomes ``done`` without one.  Explicit values win.
* A new unit without an ``assignee`` key belongs to the creating account.
* The v5 migration fills the same defaults on existing units, once.
"""
from __future__ import annotations

import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path

from arkstudio import db, org
from arkstudio.importer import InfoHubSource
from arkstudio.server import App, serve
from arkstudio.skills import agent_team
from arkstudio.store import StoreError

from tests.python.store.test_assignee import http
from tests.python.store.test_importer import make_infohub
from tests.python.auth.test_org import AuthHttpCase
from tests.python.store.test_store import make_store

TODAY = db.today()
PAST = "2024-01-01"
FUTURE = "2999-01-01"


class StoreDefaultDateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def create(self, **data):
        return self.store.create_unit({"title": "N", **data})

    def test_create_sets_start_to_creation_date(self):
        self.assertEqual(self.create()["start_at"], TODAY)
        self.assertEqual(self.create(start_at=None)["start_at"], TODAY)
        self.assertEqual(self.create(start_at="")["start_at"], TODAY)
        self.assertIsNone(self.create()["end_at"])

    def test_explicit_dates_win(self):
        unit = self.create(start_at=PAST, end_at="2024-02-01", status="done")
        self.assertEqual((unit["start_at"], unit["end_at"]), (PAST, "2024-02-01"))
        planned = self.create(start_at=FUTURE)
        self.assertEqual(planned["start_at"], FUTURE)

    def test_default_never_contradicts_an_explicit_date(self):
        # An end before today: start stays empty rather than after the end.
        unit = self.create(end_at=PAST)
        self.assertIsNone(unit["start_at"])
        self.assertEqual(unit["end_at"], PAST)
        # Done with a future planned start: no completion date before it.
        done = self.create(status="done", start_at=FUTURE)
        self.assertIsNone(done["end_at"])

    def test_create_done_sets_end(self):
        unit = self.create(status="done")
        self.assertEqual((unit["start_at"], unit["end_at"]), (TODAY, TODAY))

    def test_completion_sets_end_once(self):
        unit = self.create(start_at=PAST)
        self.assertEqual(self.store.update_unit(unit["id"], {"status": "active"})["end_at"], None)
        done = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(done["end_at"], TODAY)
        activity = self.store.activity(5, unit_id=unit["id"])
        self.assertIn("end_at", activity[0]["detail"]["fields"])

    def test_inspector_patch_with_null_end_still_completes(self):
        unit = self.create(start_at=PAST)
        done = self.store.update_unit(unit["id"], {"status": "done", "start_at": PAST, "end_at": None})
        self.assertEqual(done["end_at"], TODAY)

    def test_planned_deadline_is_kept_on_completion(self):
        unit = self.create(start_at=PAST, end_at="2024-06-01")
        done = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(done["end_at"], "2024-06-01")
        explicit = self.create(start_at=PAST)
        done = self.store.update_unit(explicit["id"], {"status": "done", "end_at": "2024-03-01"})
        self.assertEqual(done["end_at"], "2024-03-01")

    def test_reopen_keeps_end_and_recompletion_fills_only_when_empty(self):
        unit = self.create(start_at=PAST, end_at="2024-02-01", status="done")
        reopened = self.store.update_unit(unit["id"], {"status": "active"})
        self.assertEqual(reopened["end_at"], "2024-02-01")  # last completion kept
        again = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(again["end_at"], "2024-02-01")  # not empty: unchanged
        self.store.update_unit(unit["id"], {"status": "active", "end_at": None})
        cleared = self.store.get_unit(unit["id"])
        self.assertIsNone(cleared["end_at"])
        self.assertEqual(self.store.update_unit(unit["id"], {"status": "done"})["end_at"], TODAY)

    def test_clearing_end_on_a_done_unit_is_allowed(self):
        unit = self.create(status="done")
        cleared = self.store.update_unit(unit["id"], {"end_at": None})
        self.assertIsNone(cleared["end_at"])  # no status transition, no default

    def test_insert_between_gets_defaults(self):
        self.store.creator_resolver = lambda: "alice"
        parent = self.create(title="P")
        child = self.create(title="C")
        self.store.create_link(child["id"], parent["id"], "part_of")
        made = self.store.insert_between(child["id"], title="Mid")["unit"]
        self.assertEqual(made["start_at"], TODAY)
        self.assertEqual(made["assignee"], "alice")

    def test_agent_team_create_task_gets_defaults(self):
        self.store.creator_resolver = lambda: "alice"
        task = agent_team.example_task({"id": "prj_test", "name": "Test"})
        task.pop("task_id")
        task.pop("worktree")
        task.update({"create": True, "title": "Default task"})
        unit = agent_team.create_task(self.store, task)["ark_unit"]
        self.assertEqual(unit["start_at"], TODAY)
        self.assertEqual(unit["assignee"], "alice")
        task.update({"title": "Owned task", "assignee": "bob"})
        unit = agent_team.create_task(self.store, task)["ark_unit"]
        self.assertEqual(unit["assignee"], "bob")

    def test_importer_gets_defaults(self):
        hub = make_infohub(Path(self.tmp.name) / "hub.db")
        source = InfoHubSource(hub)
        try:
            source.import_into(self.store, mode="all")
        finally:
            source.close()
        done = self.store.list_units({"q": "Read the paper"})["items"][0]
        self.assertEqual((done["start_at"], done["end_at"]), (TODAY, TODAY))
        open_unit = self.store.list_units({"q": "Control gap"})["items"][0]
        self.assertEqual(open_unit["start_at"], TODAY)
        self.assertIsNone(open_unit["end_at"])


class StoreCreatorAssigneeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_no_resolver_or_no_account_leaves_unassigned(self):
        self.assertIsNone(self.store.create_unit({"title": "N"})["assignee"])
        self.store.creator_resolver = lambda: ""
        self.assertIsNone(self.store.create_unit({"title": "N"})["assignee"])

    def test_creator_is_default_and_explicit_values_win(self):
        self.store.creator_resolver = lambda: "alice"
        self.assertEqual(self.store.create_unit({"title": "N"})["assignee"], "alice")
        self.assertEqual(self.store.create_unit({"title": "N", "assignee": "bob"})["assignee"], "bob")
        self.assertIsNone(self.store.create_unit({"title": "N", "assignee": None})["assignee"])
        self.assertIsNone(self.store.create_unit({"title": "N", "assignee": ""})["assignee"])

    def test_system_writes_and_refused_creators_stay_unassigned(self):
        self.store.creator_resolver = lambda: "alice"
        self.assertIsNone(self.store.create_unit({"title": "N"}, actor="system")["assignee"])
        self.assertIsNone(self.store.create_unit({"title": "N"}, actor="system:migration")["assignee"])

        def refuse(name):
            raise StoreError("not a member")
        self.store.assignee_resolver = refuse
        unit = self.store.create_unit({"title": "N"})  # the create itself succeeds
        self.assertIsNone(unit["assignee"])

    def test_update_never_assigns(self):
        unit = self.store.create_unit({"title": "N"})
        self.store.creator_resolver = lambda: "alice"
        self.assertIsNone(self.store.update_unit(unit["id"], {"title": "M"})["assignee"])


class LocalApiDefaultsTests(unittest.TestCase):
    """Auth disabled: the reserved local owner is the creator."""

    def setUp(self):
        self._old_chat = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        self.tmp = tempfile.TemporaryDirectory()
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=False)
        self.httpd = serve(self.app, port=0, block=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        status, _ = http(self.base, "/api/projects", method="POST", body={"name": "Local"})
        self.assertEqual(status, 200)
        token = http(self.base, "/api/agent/token")[1]["token"]
        self.agent = {"Authorization": f"Bearer {token}", "X-Agent-Name": "coordinator"}

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.shutdown()
        self.tmp.cleanup()
        if self._old_chat is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = self._old_chat

    def test_ui_and_agent_creates_get_dates_and_local_owner(self):
        status, data = http(self.base, "/api/units", method="POST",
                            body={"title": "UI", "start_at": None, "end_at": None})
        self.assertEqual(status, 200, data)
        self.assertEqual((data["unit"]["start_at"], data["unit"]["assignee"]), (TODAY, org.LOCAL_USERNAME))
        status, data = http(self.base, "/api/agent/v1/units", method="POST", headers=self.agent,
                            body={"title": "Agent", "status": "done"})
        self.assertEqual(status, 200, data)
        unit = data["unit"]
        self.assertEqual((unit["start_at"], unit["end_at"], unit["assignee"]),
                         (TODAY, TODAY, org.LOCAL_USERNAME))

    def test_ui_and_agent_updates_set_completion_date(self):
        unit = http(self.base, "/api/units", method="POST", body={"title": "N", "start_at": PAST})[1]["unit"]
        # Needs-you "Accept" / "Mark decided" send {status: "done"} like this.
        status, data = http(self.base, f"/api/units/{unit['id']}", method="PATCH", body={"status": "done"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["unit"]["end_at"], TODAY)
        other = http(self.base, "/api/units", method="POST", body={"title": "M", "start_at": PAST})[1]["unit"]
        status, data = http(self.base, f"/api/agent/v1/units/{other['uid']}", method="POST",
                            headers=self.agent, body={"status": "done"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["unit"]["end_at"], TODAY)

    def test_insert_between_endpoint(self):
        parent = http(self.base, "/api/units", method="POST", body={"title": "P"})[1]["unit"]
        child = http(self.base, "/api/units", method="POST", body={
            "title": "C", "links": [{"target": parent["id"], "relation": "part_of"}]})[1]["unit"]
        status, data = http(self.base, f"/api/units/{child['id']}/insert-between", method="POST",
                            body={"title": "Mid"})
        self.assertEqual(status, 200, data)
        self.assertEqual((data["unit"]["start_at"], data["unit"]["assignee"]), (TODAY, org.LOCAL_USERNAME))

    def test_agent_team_task_endpoint(self):
        task = agent_team.example_task({"id": "prj_api", "name": "API project"})
        task.pop("task_id")
        task.pop("worktree")
        task.update({"create": True, "title": "API task"})
        status, data = http(self.base, "/api/skills/agent-team/tasks", method="POST", body=task)
        self.assertEqual(status, 200, data)
        self.assertEqual(data["ark_unit"]["assignee"], org.LOCAL_USERNAME)
        self.assertEqual(data["ark_unit"]["start_at"], TODAY)

    def test_interface_documents_defaults(self):
        data = http(self.base, "/api/agent/v1/interface", headers=self.agent)[1]
        self.assertIn("creating account", data["ownership"]["default"])
        self.assertIn("start_at", data["dates"])


class AuthApiDefaultsTests(AuthHttpCase):
    """Signed-in accounts: UI and agent-API creates belong to the session's user."""

    def test_creator_for_ui_and_agent_paths(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        self.assertEqual(self.grant(self.project_id(), user="alice", permission="write").status, 200)
        alice = self.login("alice", "alice-pass")
        made = self.api("/api/units", cookie=alice, method="POST", body={"title": "Alice node"})
        self.assertEqual(made.status, 200, made.text)
        self.assertEqual(made.json()["unit"]["assignee"], "alice")
        self.assertEqual(made.json()["unit"]["start_at"], TODAY)

        token = self.app.manager.agent_token
        agent = {"Authorization": f"Bearer {token}", "X-Agent-Name": "worker"}
        made = self.api("/api/agent/v1/units", cookie=alice, method="POST", headers=agent,
                        body={"title": "Agent node"})
        self.assertEqual(made.status, 200, made.text)
        self.assertEqual(made.json()["unit"]["assignee"], "alice")
        owner = self.api("/api/agent/v1/units", cookie=self.owner, method="POST", headers=agent,
                         body={"title": "Owner agent node"})
        self.assertEqual(owner.json()["unit"]["assignee"], "owner")
        explicit = self.api("/api/units", cookie=alice, method="POST",
                            body={"title": "For owner", "assignee": "owner"})
        self.assertEqual(explicit.json()["unit"]["assignee"], "owner")


def build_v4_database(path: Path) -> None:
    """A v4 project database whose units predate the v5 defaults."""
    con = sqlite3.connect(str(path))
    con.row_factory = sqlite3.Row
    con.executescript(db.SCHEMA)
    for key, value in (("schema_version", "4"), (db.BODY_NOTES_MIGRATED, "1"),
                       (db.NEEDS_TAGS_MIGRATED, "1")):
        con.execute("INSERT INTO meta(key,value) VALUES(?,?)", (key, value))
    units = [
        # uid, status, start, end, created, updated, archived
        ("N-0001", "open", None, None, "2024-03-05T10:00:00+00:00", "2024-04-01T00:00:00+00:00", 0),
        ("N-0002", "done", None, None, "2024-03-05T10:00:00+00:00", "2024-03-20T00:00:00+00:00", 0),
        ("N-0003", "done", None, None, "2024-03-05T10:00:00+00:00", "2024-05-02T09:00:00+00:00", 0),
        ("N-0004", "done", None, None, "2024-03-05T10:00:00+00:00", "2024-05-03T09:00:00+00:00", 1),
        ("N-0005", "done", "2023-01-01", "2023-02-01", "2024-03-05T10:00:00+00:00",
         "2024-05-03T09:00:00+00:00", 1),
        ("N-0006", "open", None, "2024-01-01", "2024-03-05T10:00:00+00:00", "2024-03-05T10:00:00+00:00", 0),
        ("N-0007", "done", "2024-06-01", None, "2024-03-05T10:00:00+00:00", "2024-05-01T00:00:00+00:00", 0),
        ("N-0008", "active", "2024-02-02", None, "2024-03-05T10:00:00+00:00", "2024-03-05T10:00:00+00:00", 0),
    ]
    for uid, status, start, end, created, updated, archived in units:
        con.execute(
            "INSERT INTO units(uid,type,title,status,start_at,end_at,created_at,updated_at,archived) "
            "VALUES(?,?,?,?,?,?,?,?,?)", (uid, "note", uid, status, start, end, created, updated, archived))

    def log(unit_id, at, action, detail, actor="human:ui"):
        con.execute("INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
                    (at, actor, action, unit_id, detail if isinstance(detail, str) else json.dumps(detail)))
    log(2, "2024-03-07T08:00:00+00:00", "unit.update", {"fields": ["status"]})
    log(2, "2024-03-10T23:30:00+00:00", "unit.update", {"fields": ["status", "tags"]})
    log(2, "2024-03-20T00:00:00+00:00", "unit.update", {"fields": ["body"]})
    log(3, "2024-04-01T00:00:00+00:00", "unit.update", "{not json")
    con.commit()
    con.close()


class DateMigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "ark.sqlite3"
        build_v4_database(self.path)

    def tearDown(self):
        self.tmp.cleanup()

    def rows(self, con):
        return {row["uid"]: dict(row) for row in con.execute(
            "SELECT uid, start_at, end_at, updated_at FROM units ORDER BY id")}

    def test_backfill_rules(self):
        raw = sqlite3.connect(str(self.path))
        raw.row_factory = sqlite3.Row
        before = self.rows(raw)
        raw.close()
        con = db.connect(self.path)
        after = self.rows(con)
        self.assertEqual(db.meta_get(con, "schema_version"), str(db.SCHEMA_VERSION))
        expect = {
            "N-0001": ("2024-03-05", None),          # open: start only
            "N-0002": ("2024-03-05", "2024-03-10"),  # last status change in the log
            "N-0003": ("2024-03-05", "2024-05-02"),  # no usable entry: updated_at
            "N-0004": ("2024-03-05", "2024-05-03"),  # archived, null: same rules
            "N-0005": ("2023-01-01", "2023-02-01"),  # archived, dated: untouched
            "N-0006": (None, "2024-01-01"),          # creation after the deadline: skip
            "N-0007": ("2024-06-01", None),          # completion before the start: skip
            "N-0008": ("2024-02-02", None),          # already started, open
        }
        self.assertEqual({uid: (r["start_at"], r["end_at"]) for uid, r in after.items()}, expect)
        self.assertEqual({uid: r["updated_at"] for uid, r in after.items()},
                         {uid: r["updated_at"] for uid, r in before.items()})
        logged = con.execute(
            "SELECT u.uid, a.actor, a.detail_json FROM activity a JOIN units u ON u.id=a.unit_id "
            "WHERE a.action=? ORDER BY a.id", (db.BACKFILL_ACTION,)).fetchall()
        self.assertEqual([row["uid"] for row in logged], ["N-0001", "N-0002", "N-0003", "N-0004"])
        self.assertEqual({row["actor"] for row in logged}, {db.MIGRATION_ACTOR})
        detail = json.loads(logged[1]["detail_json"])
        self.assertEqual(detail["fields"], ["start_at", "end_at"])
        self.assertEqual(detail["end_at"]["source"], "activity")
        self.assertEqual(json.loads(logged[2]["detail_json"])["end_at"]["source"], "updated_at")
        con.close()

    def test_idempotent(self):
        db.connect(self.path).close()
        con = db.connect(self.path)
        count = con.execute("SELECT COUNT(*) FROM activity WHERE action=?", (db.BACKFILL_ACTION,)).fetchone()[0]
        self.assertEqual(count, 4)
        # Even without the meta flag a rerun finds nothing left to fill.
        self.assertEqual(db.backfill_default_dates(con), 0)
        con.close()


class AssigneeMigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))
        con = self.store.con
        for uid, creator, assignee in (("N-0001", "alice", None), ("N-0002", "human:ui", None),
                                       ("N-0003", "system", None), ("N-0004", "ghost", None),
                                       ("N-0005", "alice", "bob"), ("N-0006", None, None)):
            unit_id = con.execute(
                "INSERT INTO units(uid,type,title,created_at,updated_at,assignee) VALUES(?,?,?,?,?,?)",
                (uid, "note", uid, "2024-01-01T00:00:00+00:00", "2024-01-02T00:00:00+00:00", assignee)).lastrowid
            if creator:
                con.execute("INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,?,?,?)",
                            ("2024-01-01T00:00:00+00:00", creator, "unit.create", unit_id, "{}"))
        con.commit()
        self.accounts = {"alice": "Alice", "system": "system"}

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def is_account(self, name):
        return self.accounts.get(name.lower())

    def test_creator_backfill_only_real_accounts_and_once(self):
        self.assertEqual(self.store.backfill_creator_assignees(self.is_account), 1)
        owners = {row["uid"]: row["assignee"] for row in self.store.con.execute(
            "SELECT uid, assignee FROM units")}
        self.assertEqual(owners, {"N-0001": "Alice", "N-0002": None, "N-0003": None,
                                  "N-0004": None, "N-0005": "bob", "N-0006": None})
        row = self.store.con.execute(
            "SELECT actor, detail_json FROM activity WHERE action=?", (db.BACKFILL_ACTION,)).fetchone()
        self.assertEqual(row["actor"], db.MIGRATION_ACTOR)
        self.assertEqual(json.loads(row["detail_json"])["assignee"]["to"], "Alice")
        self.assertEqual(self.store.con.execute(
            "SELECT updated_at FROM units WHERE uid='N-0001'").fetchone()[0], "2024-01-02T00:00:00+00:00")
        # Once per database: later runs (and later units) are left alone.
        self.store.con.execute("UPDATE units SET assignee=NULL WHERE uid='N-0001'")
        self.store.con.commit()
        self.assertEqual(self.store.backfill_creator_assignees(self.is_account), 0)
        self.assertEqual(db.meta_get(self.store.con, db.CREATOR_ASSIGNEE_MIGRATED), "1")

    def test_server_runs_backfill_with_real_accounts(self):
        app = App(Path(self.tmp.name) / "projects", seed_demo=False)
        try:
            self.assertEqual(app.account_name("LOCAL"), org.LOCAL_USERNAME)
            self.assertIsNone(app.account_name("alice"))
            self.assertIsNone(app.account_name(""))
        finally:
            app.shutdown()


if __name__ == "__main__":
    unittest.main()
