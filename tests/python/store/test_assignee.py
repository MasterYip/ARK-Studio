"""Node assignee: schema v3 migration, validation, inheritance, filters, API.

``units.assignee`` is the ARK account that owns a node.  It is separate from
``assigned_to`` links (agent/person units), which must keep working unchanged.
"""
from __future__ import annotations

import csv
import gzip
import io
import json
import os
import sqlite3
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio import db, export, org
from arkstudio.blackbox import BlackBox
from arkstudio.projects import ProjectManager
from arkstudio.server import App, filters_from_query, serve
from arkstudio.store import Store, StoreError

from tests.python.auth.test_org import AuthHttpCase
from tests.python.store.test_store import make_store

# The v2 schema as shipped before this change: no units.assignee column.
OLD_SCHEMA = db.SCHEMA.replace(
    "  archived INTEGER NOT NULL DEFAULT 0,\n  assignee TEXT\n);",
    "  archived INTEGER NOT NULL DEFAULT 0\n);",
)


def build_old_database(path: Path) -> None:
    con = sqlite3.connect(str(path))
    con.executescript(OLD_SCHEMA)
    con.execute("INSERT INTO meta(key,value) VALUES('schema_version','2')")
    con.execute(
        "INSERT INTO units(uid,type,title,body,status,importance,created_at,updated_at) "
        "VALUES('N-0001','note','Old root','kept','open','p2','2024-01-01T00:00:00+00:00',"
        "'2024-01-02T00:00:00+00:00')")
    con.execute(
        "INSERT INTO units(uid,type,title,body,status,importance,created_at,updated_at) "
        "VALUES('N-0002','note','Old child','','open','p2','2024-01-01T00:00:00+00:00',"
        "'2024-01-02T00:00:00+00:00')")
    con.execute(
        "INSERT INTO unit_links(source_id,target_id,relation,created_at) "
        "VALUES(2,1,'part_of','2024-01-01T00:00:00+00:00')")
    con.commit()
    con.close()


def link(store: Store, child: dict, parent: dict, relation: str = "part_of") -> None:
    store.create_link(child["id"], parent["id"], relation)


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_old_schema_file_gains_nullable_column_without_rewriting_rows(self):
        self.assertNotIn("assignee", OLD_SCHEMA)
        path = self.dir / "ark.sqlite3"
        build_old_database(path)
        before = sqlite3.connect(str(path))
        rows_before = before.execute("SELECT * FROM units ORDER BY id").fetchall()
        before.close()

        con = db.connect(path)
        columns = {row[1] for row in con.execute("PRAGMA table_info(units)")}
        self.assertIn("assignee", columns)
        indexes = {row[1] for row in con.execute("PRAGMA index_list(units)")}
        self.assertIn("units_assignee", indexes)
        self.assertEqual(db.meta_get(con, "schema_version"), str(db.SCHEMA_VERSION))
        # v5 fills start_at from created_at (index 7); nothing else changes.
        rows_after = [tuple(row)[:-1] for row in con.execute("SELECT * FROM units ORDER BY id")]
        self.assertEqual([row[7] for row in rows_after], ["2024-01-01", "2024-01-01"])
        strip = lambda row: row[:7] + row[8:]  # noqa: E731
        self.assertEqual([strip(row) for row in rows_after], [strip(tuple(row)) for row in rows_before])
        self.assertEqual([row[0] for row in con.execute("SELECT assignee FROM units")], [None, None])
        con.close()

        # Idempotent: a second open neither fails nor duplicates anything.
        con = db.connect(path)
        self.assertEqual(sum(1 for row in con.execute("PRAGMA table_info(units)") if row[1] == "assignee"), 1)
        con.close()

    def test_store_reads_migrated_project_and_inherits(self):
        project = self.dir / "old.ark"
        project.mkdir()
        build_old_database(project / "ark.sqlite3")
        store = Store(project)
        try:
            child = store.get_unit("N-0002")
            self.assertIsNone(child["assignee"])
            self.assertIsNone(child["assignee_effective"])
            self.assertEqual(child["body"], "")
            store.update_unit("N-0001", {"assignee": "alice"})
            child = store.get_unit("N-0002")
            self.assertEqual(child["assignee_effective"], "alice")
            self.assertEqual(child["assignee_from"]["uid"], "N-0001")
        finally:
            store.close()

    def test_new_and_demo_projects_have_the_column(self):
        manager = ProjectManager(self.dir / "projects", seed_demo=False)
        project = manager.create("Fresh", template="blank")
        try:
            columns = {row[1] for row in project.store.con.execute("PRAGMA table_info(units)")}
            self.assertIn("assignee", columns)
        finally:
            project.close()


class StoreAssigneeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))
        users = {"alice": "alice", "bob": "Bob"}

        def resolve(name: str) -> str:
            canonical = users.get(name.lower())
            if not canonical:
                raise StoreError(f"Unknown assignee {name!r}")
            return canonical

        self.store.assignee_resolver = resolve

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_default_null_set_clear_and_reject(self):
        unit = self.store.create_unit({"title": "Plain"})
        self.assertIsNone(unit["assignee"])
        self.assertIsNone(unit["assignee_effective"])
        self.assertIsNone(unit["assignee_from"])
        unit = self.store.update_unit(unit["id"], {"assignee": "ALICE"})
        self.assertEqual(unit["assignee"], "alice")
        unit = self.store.update_unit(unit["id"], {"assignee": "bob"})
        self.assertEqual(unit["assignee"], "Bob")  # canonical account case
        unit = self.store.update_unit(unit["id"], {"assignee": ""})
        self.assertIsNone(unit["assignee"])
        unit = self.store.update_unit(unit["id"], {"assignee": "bob"})
        unit = self.store.update_unit(unit["id"], {"assignee": None})
        self.assertIsNone(unit["assignee"])
        with self.assertRaises(StoreError):
            self.store.update_unit(unit["id"], {"assignee": "mallory"})
        with self.assertRaises(StoreError):
            self.store.create_unit({"title": "Bad", "assignee": "mallory"})
        with self.assertRaises(StoreError):
            self.store.update_unit(unit["id"], {"assignee": 42})
        # Other patches leave the assignee alone.
        unit = self.store.update_unit(unit["id"], {"assignee": "alice"})
        unit = self.store.update_unit(unit["id"], {"title": "Renamed"})
        self.assertEqual(unit["assignee"], "alice")

    def test_activity_log_records_the_change(self):
        unit = self.store.create_unit({"title": "Logged", "assignee": "Alice"})
        created = self.store.activity(limit=1, unit_id=unit["id"])[0]
        self.assertEqual(created["detail"]["assignee"], "alice")
        self.store.update_unit(unit["id"], {"assignee": "BOB"}, actor="agent:test")
        entry = self.store.activity(limit=1, unit_id=unit["id"])[0]
        self.assertEqual(entry["action"], "unit.update")
        self.assertEqual(entry["actor"], "agent:test")
        self.assertEqual(entry["detail"]["fields"], ["assignee"])
        self.assertEqual(entry["detail"]["assignee"], {"from": "alice", "to": "Bob"})
        log = (self.store.log_path).read_text(encoding="utf-8").strip().splitlines()
        self.assertIn('"to": "Bob"', log[-1])
        # Setting the same value again is not a change.
        self.store.update_unit(unit["id"], {"assignee": "bob"})
        self.assertEqual(self.store.activity(limit=1, unit_id=unit["id"])[0]["id"], entry["id"])

    def test_without_resolver_only_the_format_is_checked(self):
        self.store.assignee_resolver = None
        unit = self.store.create_unit({"title": "CLI", "assignee": "someone"})
        self.assertEqual(unit["assignee"], "someone")
        with self.assertRaises(StoreError):
            self.store.update_unit(unit["id"], {"assignee": "has space"})

    def test_inheritance_nearest_ancestor_wins(self):
        root = self.store.create_unit({"title": "Root", "assignee": "alice"})
        mid = self.store.create_unit({"title": "Mid"})
        leaf = self.store.create_unit({"title": "Leaf"})
        link(self.store, mid, root)
        link(self.store, leaf, mid)
        got = self.store.get_unit(leaf["id"])
        self.assertIsNone(got["assignee"])
        self.assertEqual(got["assignee_effective"], "alice")
        self.assertEqual(got["assignee_from"]["id"], root["id"])
        self.store.update_unit(mid["id"], {"assignee": "bob"})
        got = self.store.get_unit(leaf["id"])
        self.assertEqual(got["assignee_effective"], "Bob")
        self.assertEqual(got["assignee_from"]["title"], "Mid")
        own = self.store.get_unit(mid["id"])
        self.assertEqual(own["assignee_effective"], "Bob")
        self.assertIsNone(own["assignee_from"])
        # Non part_of links do not carry ownership.
        other = self.store.create_unit({"title": "Other"})
        link(self.store, other, root, "relates_to")
        self.assertIsNone(self.store.get_unit(other["id"])["assignee_effective"])
        # Graph and list payloads carry the same resolved values.
        nodes = {n["id"]: n for n in self.store.graph({}, slim=True)["nodes"]}
        self.assertEqual(nodes[leaf["id"]]["assignee_effective"], "Bob")
        items = {u["id"]: u for u in self.store.list_units({})["items"]}
        self.assertEqual(items[root["id"]]["assignee_effective"], "alice")

    def test_part_of_cycles_terminate(self):
        a = self.store.create_unit({"title": "A"})
        b = self.store.create_unit({"title": "B"})
        c = self.store.create_unit({"title": "C"})
        link(self.store, a, b)
        link(self.store, b, c)
        link(self.store, c, a)
        for unit in (a, b, c):
            self.assertIsNone(self.store.get_unit(unit["id"])["assignee_effective"])
        self.store.update_unit(c["id"], {"assignee": "alice"})
        self.assertEqual(self.store.get_unit(a["id"])["assignee_effective"], "alice")
        self.assertEqual(self.store.get_unit(b["id"])["assignee_effective"], "alice")
        self.assertEqual(len(self.store.graph({})["nodes"]), 3)
        self.assertEqual(self.store.list_units({"assignees": ["none"]})["total"], 0)

    def test_list_graph_and_facet_filters(self):
        root = self.store.create_unit({"title": "Alice root", "assignee": "alice"})
        kid = self.store.create_unit({"title": "Alice kid"})
        link(self.store, kid, root)
        bob = self.store.create_unit({"title": "Bob task", "assignee": "bob"})
        loose = self.store.create_unit({"title": "Loose"})

        def titles(filters):
            return sorted(u["title"] for u in self.store.list_units(filters)["items"])

        self.assertEqual(titles({"assignees": ["alice"]}), ["Alice kid", "Alice root"])
        self.assertEqual(titles({"assignees": ["ALICE"]}), ["Alice kid", "Alice root"])
        self.assertEqual(titles({"assignees": ["me"], "me": "Bob"}), ["Bob task"])
        self.assertEqual(titles({"assignees": ["me"], "me": ""}), [])
        self.assertEqual(titles({"assignees": ["none"]}), ["Loose"])
        self.assertEqual(titles({"assignees": ["none", "bob"]}), ["Bob task", "Loose"])
        graph = self.store.graph({"assignees": ["alice"]})
        self.assertEqual({n["id"] for n in graph["nodes"]}, {root["id"], kid["id"]})
        self.assertEqual(len(graph["edges"]), 1)
        facets = self.store.list_units({"assignees": ["alice"]}, include_facets=True)["facets"]
        self.assertEqual(facets["assignees"], {"alice": 2, "Bob": 1})
        self.assertEqual(facets["unassigned"], 1)
        # Query parsing: comma lists, repeated keys, and `me` from the caller.
        parsed = filters_from_query({"assignee": ["me,none"], "assignees": ["bob"]}, me="alice")
        self.assertEqual(sorted(parsed["assignees"]), ["bob", "me", "none"])
        self.assertEqual(parsed["me"], "alice")
        self.assertEqual(filters_from_query({})["assignees"], [])
        del loose, bob

    def test_index_cache_sees_writes_from_another_connection(self):
        root = self.store.create_unit({"title": "Root"})
        kid = self.store.create_unit({"title": "Kid"})
        link(self.store, kid, root)
        self.assertIsNone(self.store.get_unit(kid["id"])["assignee_effective"])
        other = sqlite3.connect(str(self.store.path / "ark.sqlite3"))
        other.execute("UPDATE units SET assignee='carol' WHERE id=?", (root["id"],))
        other.commit()
        other.close()
        self.assertEqual(self.store.get_unit(kid["id"])["assignee_effective"], "carol")

    def test_assigned_to_links_are_unchanged(self):
        agent = self.store.create_unit({"title": "Codex", "type": "agent"})
        task = self.store.create_unit({"title": "Run sweep", "type": "task"})
        self.store.create_link(task["id"], agent["id"], "assigned_to")
        got = self.store.get_unit(task["id"])
        self.assertEqual([a["uid"] for a in got["assignees"]], [agent["uid"]])
        self.assertIsNone(got["assignee"])
        got = self.store.update_unit(task["id"], {"assignee": "alice"})
        self.assertEqual(got["assignee"], "alice")
        self.assertEqual([a["uid"] for a in got["assignees"]], [agent["uid"]])
        # assigned_to is not part_of: the agent unit does not inherit.
        self.assertIsNone(self.store.get_unit(agent["id"])["assignee_effective"])
        self.assertEqual([link["relation"] for link in got["links_out"]], ["assigned_to"])

    def test_needs_you_rows_carry_the_effective_assignee(self):
        root = self.store.create_unit({"title": "Branch", "assignee": "alice"})
        review = self.store.create_unit({"title": "Ready", "status": "review"})
        link(self.store, review, root)
        self.store.create_unit({"title": "Pick one", "type": "decision", "assignee": "bob"})
        rows = {row["title"]: row for row in self.store.needs_you()}
        self.assertEqual(rows["Ready"]["assignee_effective"], "alice")
        self.assertIsNone(rows["Ready"]["assignee"])
        self.assertEqual(rows["Ready"]["assignee_from"]["title"], "Branch")
        self.assertEqual(rows["Pick one"]["assignee_effective"], "Bob")
        self.assertIsNone(rows["Pick one"]["assignee_from"])

    def test_export_round_trip(self):
        root = self.store.create_unit({"title": "Exported", "assignee": "alice"})
        kid = self.store.create_unit({"title": "Kid"})
        link(self.store, kid, root)
        data = export.project_json(self.store)
        units = {u["title"]: u for u in data["units"]}
        self.assertEqual(units["Exported"]["assignee"], "alice")
        self.assertIsNone(units["Kid"]["assignee"])
        self.assertEqual(units["Kid"]["assignee_effective"], "alice")
        rows = list(csv.DictReader(io.StringIO(export.project_csv(self.store))))
        self.assertEqual({r["title"]: r["assignee"] for r in rows}, {"Exported": "alice", "Kid": ""})
        self.assertIn("assignee `alice`", export.project_markdown(self.store))


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.manager = ProjectManager(Path(self.tmp.name) / "projects", seed_demo=False)
        self.project = self.manager.create("Snap", template="blank")
        self.box = BlackBox(Path(self.tmp.name) / "blackbox",
                            project_provider=lambda: self.project, enabled=True)

    def tearDown(self):
        self.box.stop()
        try:
            self.project.close()
        except Exception:
            pass
        self.tmp.cleanup()

    def test_snapshot_restore_keeps_assignee(self):
        unit = self.project.store.create_unit({"title": "Owned", "assignee": "alice"})
        snap = self.box.snapshot(self.project, "manual", force=True)
        self.project.store.update_unit(unit["id"], {"assignee": None})
        self.box.restore(self.project, snap["id"])
        self.assertEqual(self.project.store.get_unit(unit["id"])["assignee"], "alice")

    def test_restoring_an_old_snapshot_without_the_column(self):
        snap = self.box.snapshot(self.project, "manual", force=True)
        old = Path(self.tmp.name) / "old.sqlite3"
        build_old_database(old)
        archive = self.box.root / self.project.id / snap["id"] / "ark.sqlite3.gz"
        with old.open("rb") as source, gzip.open(archive, "wb") as sink:
            sink.write(source.read())
        self.box.restore(self.project, snap["id"])
        child = self.project.store.get_unit("N-0002")
        self.assertIsNone(child["assignee"])
        self.project.store.update_unit("N-0001", {"assignee": "bob"})
        self.assertEqual(self.project.store.get_unit("N-0002")["assignee_effective"], "bob")


def http(base: str, path: str, *, method: str = "GET", body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            return response.status, json.loads(response.read() or b"{}")
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read() or b"{}")


class LocalApiTests(unittest.TestCase):
    """Auth disabled: the local username is a valid assignee and is `me`."""

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

    def test_agent_api_round_trip_and_filters(self):
        status, data = http(self.base, "/api/agent/v1/units", method="POST", headers=self.agent,
                            body={"title": "Branch root", "assignee": "LOCAL"})
        self.assertEqual(status, 200, data)
        root = data["unit"]
        self.assertEqual(root["assignee"], "local")
        status, data = http(self.base, "/api/agent/v1/units", method="POST", headers=self.agent,
                            body={"title": "Step", "assignee": None,
                                  "links": [{"target": root["id"], "relation": "part_of"}]})
        step = data["unit"]
        self.assertIsNone(step["assignee"])
        self.assertEqual(step["assignee_effective"], "local")
        self.assertEqual(step["assignee_from"]["uid"], root["uid"])
        status, data = http(self.base, "/api/agent/v1/units", method="POST", headers=self.agent,
                            body={"title": "Nope", "assignee": "nobody"})
        self.assertEqual(status, 400)
        self.assertIn("nobody", data["error"]["message"])

        status, data = http(self.base, f"/api/agent/v1/units/{step['uid']}", method="POST",
                            headers=self.agent, body={"assignee": None})
        self.assertEqual(status, 200)
        self.assertEqual(data["unit"]["assignee_effective"], "local")  # still inherited
        status, data = http(self.base, f"/api/agent/v1/units/{root['uid']}", method="POST",
                            headers=self.agent, body={"assignee": ""})
        self.assertIsNone(data["unit"]["assignee"])
        http(self.base, f"/api/units/{root['id']}", method="PATCH", body={"assignee": "local"})

        mine = http(self.base, "/api/units?assignee=me&facets=0")[1]
        self.assertEqual({u["uid"] for u in mine["items"]}, {root["uid"], step["uid"]})
        unowned = http(self.base, "/api/units?assignee=none&facets=0")[1]
        self.assertNotIn(root["uid"], {u["uid"] for u in unowned["items"]})
        named = http(self.base, "/api/graph?assignee=local")[1]
        self.assertEqual({n["uid"] for n in named["nodes"]}, {root["uid"], step["uid"]})
        agent_list = http(self.base, "/api/agent/v1/units?assignee=local", headers=self.agent)[1]
        self.assertEqual(agent_list["total"], 2)
        picker = http(self.base, "/api/project/assignees")[1]
        self.assertEqual(picker["me"], "local")
        self.assertIn("local", [u["username"] for u in picker["users"]])
        activity = http(self.base, "/api/activity?limit=5")[1]["activity"]
        self.assertTrue(any(item["detail"].get("assignee") for item in activity))

    def test_local_user_is_the_only_member(self):
        status, data = http(self.base, "/api/org/users", method="POST",
                            body={"username": "alice", "password": "alice-pass-123", "role": "member"})
        self.assertEqual(status, 200, data)
        picker = http(self.base, "/api/project/assignees")[1]
        self.assertEqual([u["username"] for u in picker["users"]], ["local"])
        self.assertTrue(picker["me_member"])
        status, data = http(self.base, "/api/units", method="POST", body={"title": "N"})
        status, data = http(self.base, f"/api/units/{data['unit']['id']}", method="PATCH",
                            body={"assignee": "alice"})
        self.assertEqual(status, 400)
        self.assertIn("has not joined this project", data["error"]["message"])

    def test_interface_descriptor_documents_ownership(self):
        data = http(self.base, "/api/agent/v1/interface", headers=self.agent)[1]
        self.assertEqual(data["ownership"]["field"], "assignee")
        self.assertIn("assignee", data["manifest_v2"]["top_level"])


class AuthApiTests(AuthHttpCase):
    """Account auth with two users: canonical case, `me`, unknown names."""

    def test_two_users_assign_and_filter(self):
        self.assertEqual(self.create_user("Alice", "alice-pass").status, 200)
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="Alice", permission="write").status, 200)
        alice = self.login("Alice", "alice-pass")   # usernames are case-sensitive

        created = self.api("/api/units", cookie=self.owner, method="POST",
                           body={"title": "Owner branch", "assignee": "OWNER"})
        self.assertEqual(created.status, 200, created.text)
        owner_root = created.json()["unit"]
        self.assertEqual(owner_root["assignee"], "owner")
        made = self.api("/api/units", cookie=alice, method="POST",
                        body={"title": "Alice branch", "assignee": "alice"})
        self.assertEqual(made.status, 200, made.text)
        alice_root = made.json()["unit"]
        self.assertEqual(alice_root["assignee"], "Alice")  # stored in account case

        bad = self.api(f"/api/units/{alice_root['id']}", cookie=alice, method="PATCH",
                       body={"assignee": "mallory"})
        self.assertEqual(bad.status, 400)
        local = self.api(f"/api/units/{alice_root['id']}", cookie=alice, method="PATCH",
                         body={"assignee": org.LOCAL_USERNAME})
        self.assertEqual(local.status, 400)  # no local user when auth is on

        mine = self.api("/api/units?assignee=me&facets=0", cookie=alice).json()
        self.assertEqual([u["uid"] for u in mine["items"]], [alice_root["uid"]])
        owners = self.api("/api/units?assignee=me&facets=0", cookie=self.owner).json()
        self.assertEqual([u["uid"] for u in owners["items"]], [owner_root["uid"]])
        named = self.api("/api/graph?assignee=alice", cookie=self.owner).json()
        self.assertEqual([n["uid"] for n in named["nodes"]], [alice_root["uid"]])
        picker = self.api("/api/project/assignees", cookie=alice).json()
        self.assertEqual(picker["me"].lower(), "alice")
        names = {u["username"] for u in picker["users"]}
        self.assertTrue({"owner", "Alice"} <= names, names)


class MemberOnlyAssigneeTests(AuthHttpCase):
    """Picker / Assign to list and server validation: project members only."""

    def test_members_only_and_non_member_is_a_400(self):
        for name, role in (("alice", "member"), ("bob", "member"), ("carol", "member"), ("dave", "viewer")):
            self.assertEqual(self.create_user(name, f"{name}-pass", role=role).status, 200)
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice", permission="write").status, 200)
        team = self.api("/api/org/teams", cookie=self.owner, method="POST", body={"name": "lab"})
        team_id = team.json()["team"]["id"]
        self.api(f"/api/org/teams/{team_id}/members", cookie=self.owner, method="POST", body={"user": "carol"})
        self.assertEqual(self.grant(project_id, team=team_id, permission="read").status, 200)

        picker = self.api("/api/project/assignees", cookie=self.owner).json()
        names = {u["username"] for u in picker["users"]}
        self.assertEqual(names, {"owner", "alice", "carol"})  # bob: no grant; dave: org viewer only
        self.assertTrue(picker["me_member"])
        dave = self.login("dave", "dave-pass")
        seen_by_dave = self.api("/api/project/assignees", cookie=dave).json()
        self.assertFalse(seen_by_dave["me_member"])  # can read, but has not joined: no "Me"
        self.assertNotIn("dave", {u["username"] for u in seen_by_dave["users"]})

        unit = self.api("/api/units", cookie=self.owner, method="POST", body={"title": "N"}).json()["unit"]
        bad = self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH", body={"assignee": "bob"})
        self.assertEqual(bad.status, 400, bad.text)
        self.assertEqual(bad.json()["error"]["message"],
                         "'bob' has not joined this project: assign a project member, or null to unassign.")
        for name in ("alice", "carol", "OWNER"):
            ok = self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH", body={"assignee": name})
            self.assertEqual(ok.status, 200, ok.text)

        # alice leaves: no longer offered, still shown as a former assignee and in the rail.
        self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH", body={"assignee": "alice"})
        revoke = self.api(f"/api/org/projects/{project_id}/grants/revoke", cookie=self.owner,
                          method="POST", body={"user": "alice"})
        self.assertEqual(revoke.status, 200, revoke.text)
        picker = self.api("/api/project/assignees", cookie=self.owner).json()
        self.assertNotIn("alice", {u["username"] for u in picker["users"]})
        self.assertEqual(picker["former"], ["alice"])
        facets = self.api("/api/units?facets=1", cookie=self.owner).json()["facets"]
        self.assertEqual(facets["assignees"].get("alice"), 1)
        # Echoing the stored value is a no-op; assigning her again is refused.
        same = self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH",
                        body={"assignee": "alice", "title": "N2"})
        self.assertEqual(same.status, 200, same.text)
        again = self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH", body={"assignee": "bob"})
        self.assertEqual(again.status, 400)


if __name__ == "__main__":
    unittest.main()
