import tempfile
import unittest
from pathlib import Path

from arkstudio.blackbox import BlackBox
from arkstudio.projects import ProjectManager


class BlackBoxTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / "projects"
        self.manager = ProjectManager(self.root, seed_demo=False)
        self.project = self.manager.create("Snapshot Test", template="blank")
        self.box = BlackBox(Path(self.tmp.name) / "blackbox",
                            project_provider=lambda: self.project,
                            enabled=True)

    def tearDown(self):
        self.box.stop()
        try:
            self.project.close()
        except Exception:
            pass
        self.tmp.cleanup()

    def test_snapshot_and_restore_project_database(self):
        first = self.box.snapshot(self.project, "manual", force=True)
        self.assertTrue(first["integrity"] == "ok")
        baseline = first["unit_count"]
        self.assertGreaterEqual(baseline, 0)
        self.assertTrue((Path(self.tmp.name) / "blackbox").is_dir())
        self.assertFalse((self.project.path / "blackbox").exists())

        self.project.store.create_unit({"type": "idea", "title": "contaminant",
                                        "status": "open", "importance": "p2"})
        self.assertGreater(self.project.store.stats()["total"], 0)
        second = self.box.snapshot(self.project, "manual", force=True)
        self.assertGreater(second["unit_count"], first["unit_count"])

        result = self.box.restore(self.project, first["id"])
        self.assertEqual(result["snapshot"]["id"], first["id"])
        self.assertEqual(self.project.store.stats()["total"], baseline)
        events = self.box.list(self.project)
        self.assertTrue(any(item["reason"] == "pre-restore" for item in events))

    def test_snapshots_are_outside_the_project(self):
        snapshot = self.box.snapshot(self.project, "manual", force=True)
        self.assertFalse(str(self.project.path).startswith(str(self.box.root / self.project.id)))
        self.assertTrue((self.box.root / self.project.id / snapshot["id"] / "manifest.json").is_file())
