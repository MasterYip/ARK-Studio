import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import urllib.request
import zipfile

from arkstudio.project_archive import write_project_archive
from arkstudio.projects import Project, ProjectManager
from tests.python.store.test_store import make_store
from tests.python.auth import test_agent_downloads as downloads
class ProjectArchiveTests(unittest.TestCase):
    def test_snapshot_reopens_with_all_data_and_internal_media(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            store = make_store(root)
            try:
                project = Project(store.path, store.manifest, store)
                unit = store.create_unit({"title": "Archive", "body": "![x](assets/figure.png)"})
                store.add_comment(unit["uid"], "Review comment")
                asset = store.path / "assets/figure.png"
                asset.parent.mkdir()
                asset.write_bytes(b"\x89PNG internal media")
                store.add_media(unit["uid"], "assets/figure.png", "image")
                if shutil.which("git"):
                    store.set_node_history(unit["uid"], True)
                store.archive_unit(unit["uid"])
                with self.assertRaises(ValueError):
                    write_project_archive(project, store.path / "recursive.zip")
                (store.path / "exports").mkdir()
                (store.path / "exports/old.zip").write_bytes(b"not included")
                (root / ".agent-token").write_text("outside project token")
                output = io.BytesIO()
                write_project_archive(project, output)
                with zipfile.ZipFile(output) as archive:
                    self.assertIsNone(archive.testzip())
                    self.assertEqual(archive.read("test.ark/assets/figure.png"), asset.read_bytes())
                    self.assertNotIn("test.ark/ark.sqlite3-wal", archive.namelist())
                    self.assertNotIn("test.ark/exports/old.zip", archive.namelist())
                    self.assertFalse(any(".agent-token" in name for name in archive.namelist()))
                    if shutil.which("git"):
                        self.assertIn("test.ark/history.git/HEAD", archive.namelist())
                    archive.extractall(root / "restore")
                restored = ProjectManager(root / "restore", seed_demo=False).open("test.ark")
                try:
                    self.assertEqual(restored.store.con.execute("PRAGMA integrity_check").fetchone()[0], "ok")
                    self.assertEqual(restored.store.get_unit(unit["uid"])["body"], unit["body"])
                    self.assertTrue(restored.store.get_unit(unit["uid"])["archived"])
                    self.assertEqual(restored.store.con.execute("SELECT count(*) FROM unit_comments").fetchone()[0], 1)
                    self.assertEqual(restored.store.con.execute("SELECT count(*) FROM unit_media").fetchone()[0], 1)
                    if shutil.which("git"):
                        self.assertTrue(restored.store.node_history(unit["uid"])["versions"])
                finally:
                    restored.close()
            finally:
                store.close()

    def test_external_symlinks_and_directory_links_are_not_followed(self):
        with tempfile.TemporaryDirectory() as temp:
            store = make_store(Path(temp))
            try:
                external = Path(temp) / "external.txt"
                external.write_text("outside")
                try:
                    (store.path / "outside.txt").symlink_to(external)
                    (store.path / "outside-dir").symlink_to(Path(temp), target_is_directory=True)
                except OSError:
                    self.skipTest("Symlink creation is unavailable")
                output = io.BytesIO()
                write_project_archive(Project(store.path, store.manifest, store), output)
                with zipfile.ZipFile(output) as archive:
                    self.assertFalse(any("outside" in name for name in archive.namelist()))
            finally:
                store.close()

    @unittest.skipUnless(os.name == "nt", "Windows directory junctions")
    def test_internal_junction_cannot_recurse_back_into_the_project(self):
        with tempfile.TemporaryDirectory() as temp:
            store = make_store(Path(temp))
            try:
                result = subprocess.run(["cmd.exe", "/c", "mklink", "/J", str(store.path / "loop"), str(store.path)],
                                        capture_output=True, creationflags=subprocess.CREATE_NO_WINDOW)
                self.assertEqual(result.returncode, 0, result.stderr)
                output = io.BytesIO()
                write_project_archive(Project(store.path, store.manifest, store), output)
                with zipfile.ZipFile(output) as archive:
                    self.assertFalse(any("/loop/" in name for name in archive.namelist()))
            finally:
                store.close()


class ProjectArchiveHttpTests(downloads.AgentDownloadTests):
    def export_paths(self):
        return [f"{prefix}/export?format=project" for prefix in ("/api/agent/v1", "/api")]

    # Inherit download authorization, revocation and project boundary checks.
    def test_bundle_preflight_zip_and_head_without_cookie(self):
        for path in self.export_paths():
            status, headers, raw = self.download(path)
            self.assertEqual(status, 200, raw)
            self.assertEqual(headers["Content-Type"], "application/zip")
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                metadata = json.loads(archive.read("ARK-EXPORT.json"))
                self.assertEqual(archive.read(metadata["folder"] + "/assets/download.png"), self.content)
            status, headers, raw = self.download(path, method="HEAD")
            self.assertEqual(status, 200)
            self.assertEqual(raw, b"")
            self.assertGreater(int(headers["Content-Length"]), 0)

    def test_browser_session_download(self):
        request = urllib.request.Request(self.base + f"/api/export?format=project&project={self.main_id}",
                                         headers={"Cookie": self.reader})
        with urllib.request.urlopen(request, timeout=20) as response:
            self.assertEqual(response.code, 200)
            self.assertTrue(zipfile.is_zipfile(io.BytesIO(response.read())))
