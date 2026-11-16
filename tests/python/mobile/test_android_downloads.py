"""Settings -> Mobile serves both Android builds: native app and web-app shell."""
import json
import os
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio import server as server_mod
from arkstudio.server import App, serve


class AndroidDownloadTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        # A fake checkout: <tmp>/web is WEB_ROOT, <tmp>/dist holds the APKs.
        cls.root = Path(cls.tmp.name)
        cls.dist = cls.root / "dist"
        cls.dist.mkdir()
        cls.app = App(cls.root / "projects", seed_demo=False)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

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

    def get(self, path):
        try:
            with urllib.request.urlopen(self.base + path, timeout=10) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, dict(exc.headers), exc.read()

    def with_dist(self, files):
        fake_web = self.root / "web"
        fake_web.mkdir(exist_ok=True)
        for name in ("ARK-Studio-android.apk", "ARK-Studio-webapp.apk"):
            (self.dist / name).unlink(missing_ok=True)
        for name, data in files.items():
            (self.dist / name).write_bytes(data)
        return mock.patch.object(server_mod, "WEB_ROOT", fake_web)

    def test_both_apks_download_with_their_own_names(self):
        with self.with_dist({"ARK-Studio-android.apk": b"NATIVE", "ARK-Studio-webapp.apk": b"WEBAPP"}):
            for path, body, name in (
                    ("/download/ARK-Studio-android.apk", b"NATIVE", "ARK-Studio-android.apk"),
                    ("/download/ark.apk", b"NATIVE", "ARK-Studio-android.apk"),
                    ("/download/ARK-Studio-webapp.apk", b"WEBAPP", "ARK-Studio-webapp.apk")):
                status, headers, data = self.get(path)
                self.assertEqual(200, status, path)
                self.assertEqual(body, data)
                self.assertIn(name, headers.get("Content-Disposition", ""))
                self.assertEqual("application/vnd.android.package-archive", headers.get("Content-Type"))

    def test_missing_build_is_404_and_reported_unavailable(self):
        with self.with_dist({"ARK-Studio-android.apk": b"NATIVE"}):
            status, _, _ = self.get("/download/ARK-Studio-webapp.apk")
            self.assertEqual(404, status)
            status, _, raw = self.get("/api/mobile/apps")
            self.assertEqual(200, status)
            apps = json.loads(raw)["apps"]
            self.assertTrue(apps["native"]["available"])
            self.assertEqual(6, apps["native"]["size"])
            self.assertFalse(apps["webapp"]["available"])
            self.assertNotEqual(apps["native"]["app_id"], apps["webapp"]["app_id"])
            self.assertEqual("./download/ARK-Studio-webapp.apk", apps["webapp"]["url"])


if __name__ == "__main__":
    unittest.main()
