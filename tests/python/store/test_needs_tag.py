"""The ``needs:`` tag: ARK records that a unit waited on the human and how it ended."""
from __future__ import annotations

import os
import sqlite3
import tempfile
import unittest
from pathlib import Path

from arkstudio import db
from arkstudio.server import App, serve
from arkstudio.store import Store
from arkstudio.tags import needs_transition

from tests.python.store.test_assignee import http
from tests.python.auth.test_org import AuthHttpCase
from tests.python.store.test_store import make_store


def needs_tags(unit: dict) -> list[str]:
    return [t for t in unit["tags"] if t.startswith("needs:")]


class TransitionTests(unittest.TestCase):
    def test_pure_lifecycle(self):
        self.assertIsNone(needs_transition(None, "task", "active"))
        self.assertEqual(needs_transition(None, "task", "review"), "review")
        self.assertEqual(needs_transition("review", "task", "done"), "accepted")
        self.assertEqual(needs_transition("review", "task", "active"), "sent-back")
        self.assertEqual(needs_transition("review", "task", "open"), "sent-back")
        self.assertEqual(needs_transition("sent-back", "task", "review"), "review")
        self.assertEqual(needs_transition(None, "decision", "open"), "decision")
        self.assertEqual(needs_transition("decision", "decision", "done"), "decided")
        self.assertEqual(needs_transition("decision", "decision", "archived"), "dismissed")
        self.assertEqual(needs_transition("review", "task", "review", archived=True), "dismissed")
        self.assertEqual(needs_transition("accepted", "task", "active"), "accepted")


class StoreNeedsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_review_then_accepted(self):
        unit = self.store.create_unit({"title": "Draft", "type": "task", "status": "active"})
        self.assertEqual(needs_tags(unit), [])
        unit = self.store.update_unit(unit["id"], {"status": "review"})
        self.assertEqual(needs_tags(unit), ["needs:review"])
        unit = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(needs_tags(unit), ["needs:accepted"])
        # Later edits keep the outcome.
        unit = self.store.update_unit(unit["id"], {"status": "active", "title": "Draft 2"})
        self.assertEqual(needs_tags(unit), ["needs:accepted"])
        activity = self.store.activity(limit=20, unit_id=unit["id"])
        moves = [(a["detail"]["from"], a["detail"]["to"]) for a in activity if a["action"] == "unit.needs"]
        self.assertEqual(moves[::-1], [(None, "review"), ("review", "accepted")])

    def test_sent_back_then_review_again(self):
        unit = self.store.create_unit({"title": "Paper", "status": "review"})
        self.assertEqual(needs_tags(unit), ["needs:review"])
        unit = self.store.update_unit(unit["id"], {"status": "active"})
        self.assertEqual(needs_tags(unit), ["needs:sent-back"])
        unit = self.store.update_unit(unit["id"], {"status": "review"})
        self.assertEqual(needs_tags(unit), ["needs:review"])
        unit = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(needs_tags(unit), ["needs:accepted"])

    def test_decision_decided(self):
        unit = self.store.create_unit({"title": "Pick venue", "type": "decision"})
        self.assertEqual(needs_tags(unit), ["needs:decision"])
        unit = self.store.update_unit(unit["id"], {"status": "done"})
        self.assertEqual(needs_tags(unit), ["needs:decided"])

    def test_archived_while_pending_is_dismissed(self):
        a = self.store.create_unit({"title": "Choice", "type": "decision"})
        a = self.store.update_unit(a["id"], {"status": "archived"})
        self.assertEqual(needs_tags(a), ["needs:dismissed"])
        b = self.store.create_unit({"title": "Waiting", "status": "review"})
        self.store.archive_unit(b["id"])
        self.assertEqual(needs_tags(self.store.get_unit(b["id"])), ["needs:dismissed"])
        self.store.restore_unit(b["id"])
        self.assertEqual(needs_tags(self.store.get_unit(b["id"])), ["needs:review"])

    def test_client_cannot_set_or_remove_the_tag(self):
        unit = self.store.create_unit({"title": "X", "tags": ["needs:accepted", "topic:a"]})
        self.assertEqual(needs_tags(unit), [])
        unit = self.store.update_unit(unit["id"], {"status": "review"})
        # A full tag list without needs: (as the tag editor sends) keeps it.
        unit = self.store.update_unit(unit["id"], {"tags": ["topic:a", "topic:b"]})
        self.assertEqual(needs_tags(unit), ["needs:review"])
        unit = self.store.update_unit(unit["id"], {"tags": ["topic:a", "needs:decided", "needs:x"]})
        self.assertEqual(needs_tags(unit), ["needs:review"])

    def test_facets_and_filter(self):
        done = self.store.create_unit({"title": "Accepted one", "status": "review"})
        self.store.update_unit(done["id"], {"status": "done"})
        self.store.create_unit({"title": "Still waiting", "status": "review"})
        self.store.create_unit({"title": "Plain"})
        facets = self.store.facets()
        values = {v["value"]: v["count"] for v in facets["namespaces"]["needs"]}
        self.assertEqual(values, {"accepted": 1, "review": 1})
        listed = self.store.list_units({"tags": ["needs:accepted"]}, include_facets=False)
        self.assertEqual([u["title"] for u in listed["items"]], ["Accepted one"])
        both = self.store.list_units({"tags": ["needs:accepted", "needs:review"]}, include_facets=False)
        self.assertEqual(both["total"], 2)

    def test_needs_you_output_unchanged(self):
        self.store.create_unit({"title": "R", "status": "review"})
        self.store.create_unit({"title": "D", "type": "decision"})
        items = self.store.needs_you()
        self.assertEqual([i["kind"] for i in items], ["decision", "review"])


class BackfillTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_backfill_is_idempotent_and_does_not_guess_history(self):
        review = self.store.create_unit({"title": "R"})
        decision = self.store.create_unit({"title": "D", "type": "note"})
        done = self.store.create_unit({"title": "Done"})
        con = self.store.con
        # Simulate a database written before needs: existed.
        con.execute("UPDATE units SET status='review' WHERE id=?", (review["id"],))
        con.execute("UPDATE units SET type='decision' WHERE id=?", (decision["id"],))
        con.execute("UPDATE units SET status='done', type='decision' WHERE id=?", (done["id"],))
        con.execute("DELETE FROM unit_tags WHERE tag LIKE 'needs:%'")
        con.execute("DELETE FROM meta WHERE key=?", (db.NEEDS_TAGS_MIGRATED,))
        con.commit()
        path = self.store.path
        manifest = self.store.manifest
        self.store.close()
        self.store = Store(path, manifest)
        tags = {u: needs_tags(self.store.get_unit(u)) for u in (review["id"], decision["id"], done["id"])}
        self.assertEqual(tags, {review["id"]: ["needs:review"], decision["id"]: ["needs:decision"],
                                done["id"]: []})
        self.assertEqual(db.backfill_needs_tags(self.store.con), 0)
        self.store.con.execute("DELETE FROM meta WHERE key=?", (db.NEEDS_TAGS_MIGRATED,))
        self.store.con.commit()
        self.store.close()
        self.store = Store(path, manifest)
        n = sqlite3.connect(str(path / "ark.sqlite3")).execute(
            "SELECT COUNT(*) FROM unit_tags WHERE tag LIKE 'needs:%'").fetchone()[0]
        self.assertEqual(n, 2)


class HttpNeedsTests(unittest.TestCase):
    """UI (PATCH /api/units), API and agent API writes tag the same way."""

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

    def _ui(self, unit_id, patch):
        status, data = http(self.base, f"/api/units/{unit_id}", method="PATCH", body=patch)
        self.assertEqual(status, 200, data)
        return data["unit"]

    def _agent(self, uid, patch):
        status, data = http(self.base, f"/api/agent/v1/units/{uid}", method="POST",
                            headers=self.agent, body=patch)
        self.assertEqual(status, 200, data)
        return data["unit"]

    def test_all_write_paths_agree(self):
        status, data = http(self.base, "/api/units", method="POST",
                            body={"title": "Via UI", "status": "review", "tags": ["needs:decided"]})
        self.assertEqual(status, 200, data)
        ui = data["unit"]
        status, data = http(self.base, "/api/agent/v1/units", method="POST", headers=self.agent,
                            body={"title": "Via agent", "status": "review"})
        self.assertEqual(status, 200, data)
        ag = data["unit"]
        self.assertEqual(needs_tags(ui), ["needs:review"])
        self.assertEqual(needs_tags(ag), ["needs:review"])
        # Accept one in the UI, the other through the agent API.
        self.assertEqual(needs_tags(self._ui(ui["id"], {"status": "done"})), ["needs:accepted"])
        self.assertEqual(needs_tags(self._agent(ag["uid"], {"status": "done"})), ["needs:accepted"])
        # An agent rewriting the tag list cannot drop or change it.
        again = self._agent(ag["uid"], {"tags": ["topic:x", "needs:review"]})
        self.assertEqual(needs_tags(again), ["needs:accepted"])
        # Sent back from the UI, back to review from the agent.
        self.assertEqual(needs_tags(self._ui(ui["id"], {"status": "review"})), ["needs:review"])
        self.assertEqual(needs_tags(self._ui(ui["id"], {"status": "active"})), ["needs:sent-back"])
        self.assertEqual(needs_tags(self._agent(ui["uid"], {"status": "review"})), ["needs:review"])
        listed = http(self.base, "/api/units?tag=needs:accepted&facets=1")[1]
        self.assertEqual([u["uid"] for u in listed["items"]], [ag["uid"]])
        self.assertIn("needs", listed["facets"]["namespaces"])
        activity = http(self.base, "/api/activity?limit=40")[1]["activity"]
        self.assertTrue(any(a["action"] == "unit.needs" and a["detail"]["to"] == "accepted"
                            for a in activity))


class ReadOnlyAssignTests(AuthHttpCase):
    """The Assign-to menu uses PATCH /api/units/<id> {assignee}; read-only is refused."""

    def test_menu_assign_write_and_read_only_refusal(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice", permission="write").status, 200)
        self.assertEqual(self.grant(project_id, user="bob", permission="read").status, 200)
        alice = self.login("alice", "alice-pass")
        bob = self.login("bob", "bob-pass")
        made = self.api("/api/units", cookie=alice, method="POST", body={"title": "Node"})
        self.assertEqual(made.status, 200, made.text)
        unit = made.json()["unit"]
        done = self.api(f"/api/units/{unit['id']}", cookie=alice, method="PATCH",
                        body={"assignee": "bob"})
        self.assertEqual(done.status, 200, done.text)
        self.assertEqual(done.json()["unit"]["assignee"], "bob")
        refused = self.api(f"/api/units/{unit['id']}", cookie=bob, method="PATCH",
                           body={"assignee": "bob"})
        self.assertEqual(refused.status, 403, refused.text)
        cleared = self.api(f"/api/units/{unit['id']}", cookie=alice, method="PATCH",
                           body={"assignee": None})
        self.assertIsNone(cleared.json()["unit"]["assignee"])
