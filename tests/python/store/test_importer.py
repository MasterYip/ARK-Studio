import sqlite3
import tempfile
import unittest
from pathlib import Path

from arkstudio.importer import InfoHubSource, preview
from arkstudio.store import Store, StoreError
from tests.python.store.test_store import make_store


def make_infohub(path: Path) -> Path:
    con = sqlite3.connect(path)
    con.executescript(
        """
        CREATE TABLE units(
          id INTEGER PRIMARY KEY, key TEXT, kind TEXT, title TEXT, summary TEXT,
          status TEXT, priority TEXT, start_at INTEGER, due_at INTEGER, end_at INTEGER,
          channel_name TEXT
        );
        CREATE TABLE unit_tags(unit_id INTEGER, tag TEXT);
        CREATE TABLE unit_links(parent_id INTEGER, child_id INTEGER, relation TEXT);
        """
    )
    con.executemany(
        "INSERT INTO units(id,key,kind,title,summary,status,priority,channel_name) VALUES(?,?,?,?,?,?,?,?)",
        [
            (1, "kp", "topic", "Control gap", "A topic summary.", "open", "p1", "RL group"),
            (2, "kt", "task", "Read the paper", "Read it.", "done", "p2", "RL group"),
            (3, "ke", "event", "Reading group", "Thursday.", "open", "high", "RL group"),
            (4, "kn", "note", "A note", "Private note.", "open", "low", ""),
        ],
    )
    con.executemany("INSERT INTO unit_tags VALUES(?,?)", [
        (1, "topic:control"), (1, "type:idea"), (1, "importance:p0"),
        (2, "topic:control"), (3, "type:deadline"), (4, "topic:control"),
    ])
    con.execute("INSERT INTO unit_links VALUES(?,?,?)", (1, 2, "part_of"))
    con.commit()
    con.close()
    return path


class ImporterTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.store = make_store(self.root)
        self.hub = make_infohub(self.root / "hub.db")

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_preview(self):
        data = preview(str(self.hub))
        self.assertEqual(data["units"], 4)
        self.assertEqual(data["topics"], 1)
        self.assertEqual(data["links"], 1)
        self.assertTrue(data["sample"])
        with self.assertRaises(StoreError):
            preview(str(self.root / "missing.db"))

    def test_browse_and_single_node_import(self):
        source = InfoHubSource(self.hub)
        try:
            result = source.browse(self.store, q="Control", kind="topic")
            self.assertEqual(result["total"], 1)
            self.assertEqual(result["items"][0]["key"], "kp")
            self.assertFalse(result["items"][0]["imported"])
            first = source.import_key(self.store, "kp")
            self.assertTrue(first["imported"])
            self.assertEqual(first["unit"]["source_ref"], "infohub:kp")
            again = source.import_key(self.store, "kp")
            self.assertTrue(again["skipped"])
            self.assertEqual(again["unit"]["uid"], first["unit"]["uid"])
            after = source.browse(self.store, kind="topic")
            self.assertTrue(after["items"][0]["imported"])
            note = source.import_key(self.store, "kn", as_idea=True)
            self.assertEqual(note["unit"]["type"], "idea")
            self.assertIn("ih-kind:note", note["unit"]["tags"])
            visual = source.graph(self.store, kind="topic", connected=False)
            self.assertEqual(len(visual["nodes"]), 1)
            self.assertEqual(visual["nodes"][0]["uid"], "kp")
            self.assertEqual(visual["nodes"][0]["color"], "#3b82f6")
        finally:
            source.close()

    def test_topics_only_import_and_provenance(self):
        source = InfoHubSource(self.hub)
        result = source.import_into(self.store, mode="topics")
        source.close()
        self.assertEqual(result["imported"], 1)
        unit = self.store.list_units({"types": ["idea"]})["items"][0]
        self.assertEqual(unit["title"], "Control gap")
        self.assertEqual(unit["source_ref"], "infohub:kp")
        self.assertIn("origin:infohub", unit["tags"])
        self.assertNotIn("type:idea", [t for t in unit["tags"] if t == "importance:p0"])
        self.assertIn("importance:p1", unit["tags"])  # priority wins, not the InfoHub importance tag

    def test_all_import_links_and_idempotency(self):
        source = InfoHubSource(self.hub)
        result = source.import_into(self.store, mode="all")
        self.assertEqual(result["imported"], 4)
        self.assertEqual(result["links"], 1)
        task = [u for u in self.store.units_with_tags() if u["source_ref"] == "infohub:kt"][0]
        topic = [u for u in self.store.units_with_tags() if u["source_ref"] == "infohub:kp"][0]
        self.assertEqual(task["parents"][0]["uid"], topic["uid"])
        again = source.import_into(self.store, mode="all")
        self.assertEqual(again["imported"], 0)
        self.assertEqual(again["skipped"], 4)
        source.close()
        # Updating refreshes metadata without duplicating.
        source = InfoHubSource(self.hub)
        updated = source.import_into(self.store, mode="all", update=True)
        self.assertEqual(updated["updated"], 4)
        source.close()
        self.assertEqual(self.store.unit_count(), 4)


if __name__ == "__main__":
    unittest.main()
