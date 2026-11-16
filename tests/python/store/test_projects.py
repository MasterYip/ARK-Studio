import tempfile
import unittest
from pathlib import Path

from arkstudio.projects import ProjectManager


class ProjectTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / "projects"

    def tearDown(self):
        self.tmp.cleanup()

    def test_create_list_open_and_blank_template(self):
        manager = ProjectManager(self.root, seed_demo=False)
        project = manager.create("My Paper", description="A test", template="blank")
        self.assertTrue(project.path.name.endswith(".ark"))
        self.assertTrue((project.path / "project.json").is_file())
        self.assertTrue((project.path / "ark.sqlite3").is_file())
        self.assertTrue((project.path / "logs").is_dir())
        self.assertEqual(project.store.unit_count(), 1)  # welcome note
        listed = manager.list_projects()
        self.assertEqual(len(listed), 1)
        self.assertEqual(listed[0]["name"], "My Paper")
        project.close()

        reopened = manager.open(listed[0]["path"])
        self.assertEqual(reopened.name, "My Paper")
        self.assertEqual(reopened.store.unit_count(), 1)
        reopened.close()

    def test_demo_template_has_cross_cutting_content(self):
        manager = ProjectManager(self.root, seed_demo=False)
        project = manager.create("PredActor", template="demo", demo=True)
        stats = project.store.stats()
        self.assertGreater(stats["total"], 70)
        self.assertGreater(stats["links"], 80)
        self.assertGreater(stats["by_type"].get("task", 0), 5)
        self.assertGreater(stats["by_type"].get("experiment", 0), 3)
        self.assertTrue((project.path / "assets").is_dir())
        project.close()

    def test_initial_project_is_not_opened_twice(self):
        manager = ProjectManager(self.root, seed_demo=True)
        initial = manager.take_initial()
        self.assertIsNotNone(initial)
        self.assertEqual(len(manager.list_projects()), 1)
        initial.close()
        self.assertIsNone(manager.take_initial())

    def test_name_collision(self):
        manager = ProjectManager(self.root, seed_demo=False)
        first = manager.create("Same Name", template="blank")
        second = manager.create("Same Name", template="blank")
        self.assertNotEqual(first.path, second.path)
        self.assertTrue(second.path.name.startswith("same-name-2"))
        first.close()
        second.close()


if __name__ == "__main__":
    unittest.main()
