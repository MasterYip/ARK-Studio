"""Store.changes: applying a delta to a cached snapshot equals a full reload.

The client (web/js/client-cache.js mergeSnapshotDelta) applies the same
rules as ``apply_delta`` below; tests/js/client_cache.test.mjs checks the JS
merge against the same contract.
"""
from __future__ import annotations

import sqlite3
import tempfile
import unittest
from pathlib import Path

from arkstudio import db
from arkstudio.store import Store


def apply_delta(snapshot: dict, delta: dict) -> dict:
    """Reference merge: the documented client rule (docs/development/client-cache.md)."""
    touched = set(delta["touched"])
    drop = set(delta["deleted"]) | {node["id"] for node in delta["units"]}
    nodes = [node for node in snapshot["nodes"] if node["id"] not in drop] + delta["units"]
    nodes.sort(key=lambda node: (node["updated_at"], node["id"]), reverse=True)
    edges = [edge for edge in snapshot["edges"]
             if edge["source"] not in touched and edge["target"] not in touched]
    edges += delta["edges"]
    edges.sort(key=lambda edge: edge["id"])
    return {"nodes": nodes, "edges": edges, "total": len(nodes)}


def comparable(snapshot: dict) -> dict:
    return {"nodes": snapshot["nodes"], "edges": snapshot["edges"], "total": snapshot["total"]}


class ChangesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.tmp.name) / "p", {"name": "Delta"})
        s = self.store
        self.root = s.create_unit({"title": "Root", "type": "story", "assignee": "alice"})
        self.mid = s.create_unit({"title": "Middle", "type": "claim"})
        self.leaf = s.create_unit({"title": "Leaf", "type": "experiment"})
        self.other = s.create_unit({"title": "Other", "type": "note", "tags": ["topic:x"]})
        s.create_link(self.mid["id"], self.root["id"], "part_of")
        s.create_link(self.leaf["id"], self.mid["id"], "part_of")
        self.support = s.create_link(self.other["id"], self.leaf["id"], "supports")

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def check_step(self, write, *, bodies=True):
        """Snapshot, write, then delta-merge must equal the new snapshot."""
        before = self.store.graph_snapshot(bodies=bodies)
        write()
        delta = self.store.changes(before["cursor"], bodies=bodies)
        self.assertFalse(delta["reset"], delta)
        after = self.store.graph_snapshot(bodies=bodies)
        self.assertEqual(delta["cursor"], after["cursor"])
        self.assertEqual(comparable(apply_delta(before, delta)), comparable(after))
        return delta

    def test_mixed_write_sequence_matches_full_reload(self):
        s = self.store
        steps = [
            lambda: s.update_unit(self.root["id"], {"title": "Root renamed"}),
            lambda: s.update_unit(self.other["id"], {"tags": ["topic:y"], "status": "review"}),
            # Inherited ownership: every descendant's assignee_effective moves.
            lambda: s.update_unit(self.root["id"], {"assignee": "bob"}),
            lambda: s.add_comment(self.leaf["id"], "Looks good", actor="human:ui"),
            lambda: s.add_media(self.leaf["id"], "assets/fig.png", "image", "Figure"),
            lambda: s.update_link(self.support["id"], "validates"),
            lambda: s.archive_unit(self.mid["id"]),
            lambda: s.restore_unit(self.mid["id"]),
            lambda: s.create_unit({"title": "Topic", "type": "topic",
                                   "meta": {}, "rule": {"tags": ["topic:y"]}}),
            lambda: s.delete_link(self.support["id"]),
        ]
        for step in steps:
            with self.subTest(step=step):
                self.check_step(step)

    def test_several_writes_in_one_delta_and_lite_bodies(self):
        s = self.store

        def burst():
            created = s.create_unit({"title": "New child", "type": "note"})
            s.create_link(created["id"], self.leaf["id"], "part_of")
            s.update_unit(self.mid["id"], {"assignee": "carol"})
            s.update_unit(self.leaf["id"], {"body": "x" * 600})
            s.archive_unit(self.other["id"])
        delta = self.check_step(burst, bodies=False)
        self.assertTrue(all("search_body" not in node for node in delta["units"]))
        self.assertIn(self.other["id"], delta["deleted"])

    def test_unit_rows_deleted_outright_are_reported(self):
        before = self.store.graph_snapshot()
        with self.store.lock, self.store.con:
            self.store.con.execute("DELETE FROM units WHERE id=?", (self.leaf["id"],))
        delta = self.store.changes(before["cursor"])
        self.assertIn(self.leaf["id"], delta["deleted"])
        self.assertEqual(comparable(apply_delta(before, delta)),
                         comparable(self.store.graph_snapshot()))

    def test_unchanged_cursor_is_empty(self):
        cursor = self.store.change_cursor()
        delta = self.store.changes(cursor)
        self.assertEqual((delta["units"], delta["deleted"], delta["edges"], delta["reset"]),
                         ([], [], [], False))
        self.assertEqual(delta["cursor"], cursor)

    def test_unknown_future_foreign_or_pruned_cursors_reset(self):
        cursor = self.store.change_cursor()
        log_id, seq = cursor.split(".")
        for bad in ("", "garbage", f"{log_id}.{int(seq) + 5}", f"other.{seq}", f"{log_id}.x"):
            with self.subTest(cursor=bad):
                self.assertTrue(self.store.changes(bad)["reset"])
        for i in range(5):
            self.store.update_unit(self.other["id"], {"title": f"Other {i}"})
        self.store.prune_change_log(keep=1)
        self.assertTrue(self.store.changes(cursor)["reset"])
        self.assertFalse(self.store.changes(self.store.change_cursor())["reset"])

    def test_large_changes_reset(self):
        cursor = self.store.change_cursor()
        self.store.update_unit(self.root["id"], {"title": "Big"})
        self.assertTrue(self.store.changes(cursor, unit_limit=1)["reset"])

    def test_replacing_the_database_changes_the_log_identity(self):
        cursor = self.store.change_cursor()
        self.store.reset_change_log_id()
        self.assertTrue(self.store.changes(cursor)["reset"])

    def test_triggers_log_writes_from_other_connections(self):
        before = self.store.graph_snapshot()
        other = sqlite3.connect(str(self.store.path / "ark.sqlite3"))
        try:
            other.execute("UPDATE units SET title='Outside' WHERE id=?", (self.other["id"],))
            other.commit()
        finally:
            other.close()
        delta = self.store.changes(before["cursor"])
        self.assertEqual(comparable(apply_delta(before, delta)),
                         comparable(self.store.graph_snapshot()))

    def test_bodies_endpoint_matches_snapshot_bodies(self):
        full = self.store.graph_snapshot()
        bodies = dict(map(tuple, self.store.graph_bodies()["bodies"]))
        self.assertEqual(bodies, {node["id"]: node["search_body"] for node in full["nodes"]})
        lite = self.store.graph_snapshot(bodies=False)
        self.assertFalse(lite["bodies"])
        self.assertTrue(all("search_body" not in node for node in lite["nodes"]))

    def test_schema_creates_change_log_on_old_databases(self):
        path = Path(self.tmp.name) / "old.sqlite3"
        con = sqlite3.connect(str(path))
        con.executescript(db.SCHEMA)
        con.close()
        upgraded = db.connect(path)
        try:
            self.assertTrue(db.meta_get(upgraded, db.CHANGE_LOG_ID))
            upgraded.execute("INSERT INTO units(uid,type,title,created_at,updated_at) "
                             "VALUES('N-1','note','n','x','x')")
            self.assertEqual(upgraded.execute("SELECT kind FROM change_log").fetchone()[0], "unit")
        finally:
            upgraded.close()


if __name__ == "__main__":
    unittest.main()
