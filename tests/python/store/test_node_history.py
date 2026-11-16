import shutil
import sqlite3
import tempfile
import unittest
import time
from pathlib import Path
from unittest import mock

from arkstudio.node_history import HistoryError
from arkstudio.store import Store, StoreError
from tests.python.store.test_store import make_store
from tests.python.auth.test_org import AuthHttpCase


class NodeHistoryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))
        self.unit = self.store.create_unit({"title": "First title", "body": "First **body**"})
        self.uid = self.unit["uid"]

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_opt_in_text_only_dedup_disable_and_reenable(self):
        self.assertFalse(self.store.node_history(self.uid)["enabled"])
        self.store.update_unit(self.uid, {"body": "Before opt in"})
        self.assertFalse(self.store.text_history.path.exists())
        baseline = self.store.set_node_history(self.uid, True, "agent:writer")
        self.assertEqual(len(baseline["versions"]), 1)
        self.store.update_unit(self.uid, {"title": "New title", "body": "New body"}, "agent:writer")
        self.store.update_unit(self.uid, {"body": "New body", "status": "active", "meta": {"secret": "not tracked"}})
        state = self.store.node_history(self.uid)
        self.assertEqual(len(state["versions"]), 2)
        paths = self.store.text_history._git("ls-tree", "--name-only", state["head"]).splitlines()
        self.assertEqual(paths, ["body.md", "title.txt"])
        self.assertEqual(self.store.node_revision(self.uid, baseline["head"])["body"], "Before opt in")
        self.store.set_node_history(self.uid, False)
        self.store.update_unit(self.uid, {"body": "Untracked"})
        self.assertEqual(self.store.node_history(self.uid)["head"], state["head"])
        state = self.store.set_node_history(self.uid, True)
        self.assertEqual(len(state["versions"]), 3)
        self.assertEqual(self.store.node_revision(self.uid, state["head"])["body"], "Untracked")

    def test_diff_restore_conflict_and_preserved_future(self):
        baseline = self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"title": "Second title", "body": "Second body", "status": "active"}, "agent:test")
        state = self.store.node_history(self.uid)
        diff = self.store.node_diff(self.uid)
        self.assertIn("-First **body**", diff["diff"])
        self.assertIn("+Second body", diff["diff"])
        restored = self.store.restore_node_revision(self.uid, baseline["head"], state["current_hash"])
        self.assertEqual(restored["body"], self.unit["body"])
        self.assertEqual(restored["status"], "active")
        newest = self.store.node_history(self.uid)
        self.assertEqual(len(newest["versions"]), 3)
        self.assertEqual(self.store.node_revision(self.uid, state["head"])["body"], "Second body")
        with self.assertRaisesRegex(StoreError, "changed"):
            self.store.restore_node_revision(self.uid, state["head"], state["current_hash"])
        self.assertEqual(self.store.node_history(self.uid)["head"], newest["head"])

    def test_history_survives_restart_and_whole_project_copy(self):
        state = self.store.set_node_history(self.uid, True)
        self.store.close()
        copy = self.store.path.parent / "copy.ark"
        shutil.copytree(self.store.path, copy)
        self.store = Store(copy)
        self.assertEqual(self.store.node_history(self.uid)["head"], state["head"])
        self.assertEqual(self.store.node_revision(self.uid, state["head"])["body"], self.unit["body"])
        self.store.update_unit(self.uid, {"body": "After restart"}, "agent:test")
        self.assertEqual(len(self.store.node_history(self.uid)["versions"]), 2)

    def test_foreign_revisions_invalid_refs_and_paging(self):
        baseline = self.store.set_node_history(self.uid, True)
        other = self.store.create_unit({"title": "Private other node", "body": "Other content"})
        foreign = self.store.set_node_history(other["uid"], True)["head"]
        for revision in (foreign, "HEAD", "--all", "../config", "0" * 40, None):
            with self.subTest(revision=revision), self.assertRaises(StoreError):
                self.store.node_revision(self.uid, revision)
        self.store.update_unit(self.uid, {"body": "Two"}, "agent:test")
        page = self.store.node_history(self.uid, limit=1)
        self.assertTrue(page["has_more"])
        self.assertEqual(self.store.node_history(self.uid, limit=1, offset=1)["versions"][0]["id"], baseline["head"])
        self.assertEqual(self.store.node_history(self.uid, offset=2)["versions"], [])
        self.assertIn("+First **body**", self.store.node_diff(self.uid, after=baseline["head"])["diff"])

    def test_git_failure_rolls_back_text_and_does_not_block_untracked_nodes(self):
        self.store.set_node_history(self.uid, True)
        with mock.patch.object(self.store.text_history, "record", side_effect=HistoryError("disk full")):
            with self.assertRaisesRegex(StoreError, "disk full"):
                self.store.update_unit(self.uid, {"body": "Must roll back"}, "agent:test")
            self.assertEqual(self.store.get_unit(self.uid)["body"], self.unit["body"])
            self.store.update_unit(self.uid, {"status": "active"})
            other = self.store.create_unit({"title": "Untracked"})
            self.store.update_unit(other["uid"], {"body": "Works"})
        with mock.patch("arkstudio.node_history.shutil.which", return_value=None):
            self.assertFalse(self.store.node_history(self.uid)["available"])
            with self.assertRaisesRegex(StoreError, "requires Git"):
                self.store.update_unit(self.uid, {"body": "Cannot track without Git"}, "agent:test")
            self.store.set_node_history(self.uid, False)
            self.store.update_unit(self.uid, {"body": "Tracking off"})

    def test_failed_sql_does_not_publish_git_revision(self):
        baseline = self.store.set_node_history(self.uid, True)
        self.store.con.execute("""CREATE TRIGGER refuse_history BEFORE UPDATE OF head ON node_text_history
            BEGIN SELECT RAISE(ABORT, 'history SQL failure'); END""")
        with self.assertRaises(sqlite3.IntegrityError):
            self.store.update_unit(self.uid, {"body": "Not published"}, "agent:test")
        self.assertEqual(self.store.get_unit(self.uid)["body"], self.unit["body"])
        self.assertEqual(self.store.node_history(self.uid)["head"], baseline["head"])
        self.assertEqual(len(self.store.node_history(self.uid)["versions"]), 1)

    def test_human_versions_wait_for_idle_and_accumulate_small_edits(self):
        self.store.set_node_history(self.uid, True)
        now = time.time()
        with mock.patch.object(self.store, "_start_history_worker"):
            with mock.patch("arkstudio.store.time.time", return_value=now):
                self.store.update_unit(self.uid, {"body": self.unit["body"] + "!"})
            self.assertTrue(self.store.node_history(self.uid)["pending"])
            self.assertEqual(self.store.flush_node_versions(now + 301), 0)  # below 10% threshold
            with mock.patch("arkstudio.store.time.time", return_value=now + 240):
                self.store.update_unit(self.uid, {"body": self.unit["body"] + "! More edits."})
            self.assertEqual(self.store.flush_node_versions(now + 301), 0)  # idle timer reset
            self.assertEqual(self.store.flush_node_versions(now + 541), 1)
        state = self.store.node_history(self.uid)
        self.assertFalse(state["pending"])
        self.assertEqual(len(state["versions"]), 2)
        self.assertEqual(self.store.flush_node_versions(now + 900), 0)

    def test_manual_checkpoint_bypasses_threshold_and_deduplicates(self):
        self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"body": self.unit["body"] + "!"})
        state = self.store.node_history(self.uid)
        saved = self.store.save_node_version(self.uid, state["current_hash"])
        self.assertFalse(saved["pending"])
        self.assertEqual(len(saved["versions"]), 2)
        self.assertEqual(self.store.save_node_version(self.uid, saved["current_hash"])["head"], saved["head"])
        with self.assertRaisesRegex(StoreError, "changed"):
            self.store.save_node_version(self.uid, "stale")

    def test_pending_human_text_survives_restart_and_agent_handoff(self):
        self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"body": "Human draft persisted"})
        path = self.store.path
        self.store.close()
        self.store = Store(path)
        self.assertTrue(self.store.node_history(self.uid)["pending"])
        self.assertTrue(self.store._history_worker.is_alive())
        self.store.update_unit(self.uid, {"body": "Agent output"}, "agent:test")
        state = self.store.node_history(self.uid)
        self.assertFalse(state["pending"])
        self.assertEqual(len(state["versions"]), 3)
        self.assertEqual(self.store.node_revision(self.uid, state["versions"][1]["id"])["body"], "Human draft persisted")

    def test_pending_versions_retry_git_failure_and_respect_disabled_tracking(self):
        self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"body": "A sufficiently large human revision"})
        with mock.patch.object(self.store.text_history, "record", side_effect=HistoryError("disk full")):
            with self.assertRaisesRegex(StoreError, "disk full"):
                self.store.flush_node_versions(time.time() + 301)
        self.assertTrue(self.store.node_history(self.uid)["pending"])
        self.assertEqual(self.store.get_unit(self.uid)["body"], "A sufficiently large human revision")
        self.store.set_node_history(self.uid, False)
        self.assertEqual(self.store.flush_node_versions(time.time() + 301), 0)
        self.store.set_node_history(self.uid, True)
        self.assertFalse(self.store.node_history(self.uid)["pending"])

    def test_apply_single_change_preserves_other_changes_and_file_arrow_preserves_title(self):
        old = "old first\nkeep\nkeep\nold last\n"
        new = "new first\nkeep\nkeep\nnew last\n"
        self.store.update_unit(self.uid, {"body": old})
        first = self.store.set_node_history(self.uid, True)["head"]
        self.store.update_unit(self.uid, {"title": "New title", "body": new}, "agent:test")
        state = self.store.node_history(self.uid)
        diff = self.store.node_diff(self.uid)
        self.assertTrue(diff["can_apply"])
        self.assertEqual([c["index"] for c in diff["changes"] if c["file"] == "body.md"], [0, 1])
        result = self.store.apply_node_history_change(self.uid, first, state["head"], "body.md", 0, state["current_hash"])
        self.assertEqual(result["body"], "old first\nkeep\nkeep\nnew last\n")
        self.assertEqual(result["title"], "New title")
        state = self.store.node_history(self.uid)
        result = self.store.apply_node_history_change(self.uid, first, state["head"], "body.md", None, state["current_hash"])
        self.assertEqual(result["body"], old)
        self.assertEqual(result["title"], "New title")
        self.assertEqual(len(self.store.node_history(self.uid)["versions"]), 4)

    def test_apply_insert_delete_eof_and_unicode_line_boundaries(self):
        for old, new in (("", "new"), ("old", ""), ("a\n", "a"), ("a", "a\n"),
                         ("-- old\r\n研究\u2028🙂", "++ new\r\n研究\u2028🚀")):
            with self.subTest(old=old, new=new):
                unit = self.store.create_unit({"title": "Boundary case", "body": old})
                uid = unit["uid"]
                first = self.store.set_node_history(uid, True)["head"]
                self.store.update_unit(uid, {"body": new}, "agent:test")
                state = self.store.node_history(uid)
                restored = self.store.apply_node_history_change(uid, first, state["head"], "body.md", 0, state["current_hash"])
                self.assertEqual(restored["body"], old)

    def test_apply_rejects_stale_historical_foreign_and_invalid_selections(self):
        first = self.store.set_node_history(self.uid, True)["head"]
        self.store.update_unit(self.uid, {"body": "Second"}, "agent:test")
        second = self.store.node_history(self.uid)
        self.store.update_unit(self.uid, {"body": "Third"}, "agent:test")
        state = self.store.node_history(self.uid)
        with self.assertRaisesRegex(StoreError, "changed"):
            self.store.apply_node_history_change(self.uid, first, second["head"], "body.md", 0, second["current_hash"])
        with self.assertRaisesRegex(StoreError, "current version"):
            self.store.apply_node_history_change(self.uid, first, second["head"], "body.md", 0, state["current_hash"])
        other = self.store.create_unit({"title": "Private"})
        foreign = self.store.set_node_history(other["uid"], True)["head"]
        for before, file, change in ((foreign, "body.md", 0), (first, "../config", 0),
                                     (first, "body.md", -1), (first, "body.md", True),
                                     (first, "body.md", 999), (first, "body.md", "0")):
            with self.subTest(file=file, change=change), self.assertRaises(StoreError):
                self.store.apply_node_history_change(self.uid, before, state["head"], file, change, state["current_hash"])

    def test_large_document_uses_absolute_100_character_threshold(self):
        self.store.update_unit(self.uid, {"body": "a" * 2000})
        self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"body": "a" * 2000 + "b" * 99})
        self.assertEqual(self.store.flush_node_versions(time.time() + 301), 0)
        self.store.update_unit(self.uid, {"body": "a" * 2000 + "b" * 100})
        self.assertEqual(self.store.flush_node_versions(time.time() + 301), 1)

    def test_small_pending_nodes_do_not_starve_later_nodes(self):
        head = self.store.set_node_history(self.uid, True)["head"]
        with mock.patch.object(self.store, "_start_history_worker"):
            for index in range(21):
                unit = self.store.create_unit({"title": self.unit["title"], "body": self.unit["body"]})
                # Seed identical baselines without 21 redundant Git writes.
                with self.store.con:
                    self.store.con.execute("INSERT INTO node_text_history(unit_id,enabled,head) VALUES(?,1,?)", (unit["id"], head))
                self.store.update_unit(unit["uid"], {"body": self.unit["body"] + ("!" if index < 20 else "Large edit")})
            now = time.time() + 301
            self.assertEqual(self.store.flush_node_versions(now), 0)
            self.assertEqual(self.store.flush_node_versions(now + 15), 1)

    def test_background_worker_resumes_overdue_edits_after_restart(self):
        self.store.set_node_history(self.uid, True)
        self.store.update_unit(self.uid, {"body": "Ready for automatic checkpoint"})
        with self.store.lock, self.store.con:
            self.store.con.execute("UPDATE node_text_pending SET edited_at=?", (time.time() - 301,))
        path = self.store.path
        self.store.close()
        self.store = Store(path)
        deadline = time.monotonic() + 22
        while time.monotonic() < deadline:
            with self.store.lock:
                pending = self.store.con.execute("SELECT 1 FROM node_text_pending LIMIT 1").fetchone()
            if not pending:
                break
            time.sleep(.1)
        self.assertFalse(self.store.node_history(self.uid)["pending"])
        self.assertEqual(len(self.store.node_history(self.uid)["versions"]), 2)


class NodeHistoryHttpTests(AuthHttpCase):
    def test_project_header_pins_history_after_another_tab_switches(self):
        first_project = self.app.project
        first = first_project.store.create_unit({"title": "First project"})
        second = self.api("/api/projects", cookie=self.owner, method="POST",
                          body={"name": "Second project", "template": "blank"}).json()["summary"]["id"]
        path = f"/api/units/{first['uid']}/history"
        response = self.api(path, cookie=self.owner, method="POST", body={"enabled": True},
                            headers={"X-ARK-Project": first_project.id})
        self.assertEqual(response.status, 200, response.text)
        self.assertTrue(first_project.store.node_history(first["uid"])["enabled"])
        self.create_user("member", "member-pass")
        member = self.login("member", "member-pass")
        self.grant(second, user="member", permission="read")
        self.grant(first_project.id, user="member", permission="write")
        self.api("/api/projects/open", cookie=member, method="POST", body={"id": second})
        response = self.api(path, cookie=member, method="POST", body={"enabled": False},
                            headers={"X-ARK-Project": first_project.id})
        self.assertEqual(response.status, 200, response.text)
        self.assertFalse(first_project.store.node_history(first["uid"])["enabled"])

    def test_browser_and_personal_token_history_and_permissions(self):
        project_id = self.app.project.id
        unit = self.app.project.store.create_unit({"title": "Versioned reply", "body": "Original"})
        uid = unit["uid"]
        path = f"/api/units/{uid}/history"
        enabled = self.api(path, cookie=self.owner, method="POST", body={"enabled": True})
        self.assertEqual(enabled.status, 200, enabled.text)
        first = enabled.json()["head"]
        self.assertEqual(self.api(f"/api/units/{uid}", cookie=self.owner, method="PATCH", body={"body": "Second"},
                                  headers={"X-Agent-Name": "test"}).status, 200)
        token = self.api("/api/personal/agent-tokens", cookie=self.owner, method="POST",
                         body={"label": "history", "scope": "read", "expires_days": 30}).json()["token"]
        headers = {"Authorization": "Bearer " + token, "X-ARK-Project": self.app.project.id}
        agent_path = f"/api/agent/v1/units/{uid}/history"
        state = self.api(agent_path, headers=headers)
        self.assertEqual(state.status, 200, state.text)
        self.assertEqual(len(state.json()["versions"]), 2)
        self.assertIn("+Second", self.api(agent_path + "/diff", headers=headers).json()["diff"])
        self.assertEqual(self.api(agent_path + "/" + first, headers=headers).json()["revision"]["body"], "Original")
        for endpoint, body in ((agent_path, {"enabled": False}), (agent_path + "/restore", {
                "revision": first, "expected_hash": state.json()["current_hash"]}),
                (agent_path + "/checkpoint", {"expected_hash": state.json()["current_hash"]}),
                (agent_path + "/apply", {"from": first, "to": state.json()["head"], "file": "body.md",
                                         "change": 0, "expected_hash": state.json()["current_hash"]})):
            self.assertEqual(self.api(endpoint, method="POST", body=body, headers=headers).status, 403)
        restored = self.api(path + "/restore", cookie=self.owner, method="POST", body={
            "revision": first, "expected_hash": state.json()["current_hash"]})
        self.assertEqual(restored.status, 200, restored.text)
        self.assertEqual(restored.json()["unit"]["body"], "Original")
        writer = self.api("/api/personal/agent-tokens", cookie=self.owner, method="POST",
                          body={"label": "writer", "scope": "write", "expires_days": 30}).json()["token"]
        write_headers = {**headers, "Authorization": "Bearer " + writer}
        disabled = self.api(agent_path, headers=write_headers, method="POST", body={"enabled": False})
        self.assertEqual(disabled.status, 200, disabled.text)
        self.assertFalse(disabled.json()["enabled"])
        self.assertEqual(self.api(agent_path, headers=write_headers, method="POST",
                                  body={"enabled": True}).status, 200)
        agent_restore = self.api(agent_path + "/restore", headers=write_headers, method="POST", body={
            "revision": state.json()["head"], "expected_hash": disabled.json()["current_hash"]})
        self.assertEqual(agent_restore.status, 200, agent_restore.text)
        self.assertEqual(agent_restore.json()["unit"]["body"], "Second")
        self.api(f"/api/units/{uid}", cookie=self.owner, method="PATCH", body={"body": "Human draft"})
        pending = self.api(agent_path, headers=headers).json()
        self.assertTrue(pending["pending"])
        checkpoint = self.api(agent_path + "/checkpoint", headers=write_headers, method="POST",
                              body={"expected_hash": pending["current_hash"]})
        self.assertEqual(checkpoint.status, 200, checkpoint.text)
        self.assertFalse(checkpoint.json()["pending"])
        applied = self.api(agent_path + "/apply", headers=write_headers, method="POST", body={
            "from": first, "to": checkpoint.json()["head"], "file": "body.md", "change": 0,
            "expected_hash": checkpoint.json()["current_hash"]})
        self.assertEqual(applied.status, 200, applied.text)
        self.assertEqual(applied.json()["unit"]["body"], "Original")
        self.assertEqual(self.api(path).status, 401)
        self.create_user("stranger", "stranger-pass")
        stranger = self.login("stranger", "stranger-pass")
        self.assertEqual(self.api(path, cookie=stranger, headers={"X-ARK-Project": project_id}).status, 404)

