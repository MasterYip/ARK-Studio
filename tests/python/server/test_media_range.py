"""Range requests and preview headers for /api/media/{id}/raw and /api/workspace/raw."""
import os
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio.server import App, serve


class VizFrameChecks:
    """web/viz-frame.html is the only page allowed to run inline scripts, and
    only inside a sandboxed opaque origin with no network access."""

    def test_viz_frame_is_sandboxed_without_network(self):
        status, headers, body = self.fetch("/viz-frame.html")
        self.assertEqual(status, 200)
        csp = headers["Content-Security-Policy"]
        self.assertTrue(csp.startswith("sandbox allow-scripts;"), csp)
        self.assertNotIn("allow-same-origin", csp)
        self.assertIn("connect-src 'none'", csp)
        self.assertIn("frame-ancestors 'self'", csp)
        self.assertEqual(headers["X-Frame-Options"], "SAMEORIGIN")
        self.assertIn(b"ark-viz-render", body)

    def test_app_shell_keeps_strict_script_policy(self):
        status, headers, _ = self.fetch("/index.html")
        self.assertEqual(status, 200)
        self.assertIn("script-src 'self';", headers["Content-Security-Policy"])
        self.assertNotIn("unsafe-inline' 'unsafe-eval", headers["Content-Security-Policy"])
        self.assertEqual(headers["X-Frame-Options"], "DENY")


class MediaRangeTests(VizFrameChecks, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=True)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"
        project = cls.app.require()
        folder = project.path / "attachments"
        folder.mkdir(exist_ok=True)
        cls.payload = bytes(range(256)) * 40  # 10240 bytes
        (folder / "clip.mp4").write_bytes(cls.payload)
        (folder / "paper.pdf").write_bytes(b"%PDF-1.4\n%fake\n")
        (folder / "page.html").write_text("<script>alert(1)</script>", encoding="utf-8")
        (folder / "notes.md").write_text("# Notes\n", encoding="utf-8")
        unit = project.store.list_units({"limit": 1})["items"][0]
        cls.video_id = project.store.add_media(unit["id"], "attachments/clip.mp4", "video")["id"]
        cls.pdf_id = project.store.add_media(unit["id"], "attachments/paper.pdf", "file")["id"]
        cls.html_id = project.store.add_media(unit["id"], "attachments/page.html", "file")["id"]

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

    def fetch(self, path, headers=None, method="GET"):
        req = urllib.request.Request(self.base + path, headers=headers or {}, method=method)
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, dict(response.headers), response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, dict(exc.headers), exc.read()

    def test_full_body_advertises_ranges(self):
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw")
        self.assertEqual(status, 200)
        self.assertEqual(body, self.payload)
        self.assertEqual(headers["Accept-Ranges"], "bytes")
        self.assertEqual(headers["Content-Type"], "video/mp4")
        self.assertIn("sandbox", headers["Content-Security-Policy"])
        self.assertNotIn("Content-Encoding", headers)

    def test_byte_ranges(self):
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=100-199"})
        self.assertEqual(status, 206)
        self.assertEqual(headers["Content-Range"], "bytes 100-199/10240")
        self.assertEqual(headers["Content-Length"], "100")
        self.assertEqual(body, self.payload[100:200])
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=10000-"})
        self.assertEqual((status, headers["Content-Range"]), (206, "bytes 10000-10239/10240"))
        self.assertEqual(body, self.payload[10000:])
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=-16"})
        self.assertEqual((status, body), (206, self.payload[-16:]))
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=9000-99999"})
        self.assertEqual((status, headers["Content-Range"]), (206, "bytes 9000-10239/10240"))

    def test_unsatisfiable_and_malformed_ranges(self):
        status, headers, _ = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=20000-"})
        self.assertEqual(status, 416)
        self.assertEqual(headers["Content-Range"], "bytes */10240")
        status, _, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "bytes=0-1,5-9"})
        self.assertEqual((status, len(body)), (200, 10240))
        status, _, body = self.fetch(f"/api/media/{self.video_id}/raw", {"Range": "items=1-2"})
        self.assertEqual(status, 200)

    def test_pdf_may_be_framed_same_origin_only(self):
        status, headers, _ = self.fetch(f"/api/media/{self.pdf_id}/raw")
        self.assertEqual(status, 200)
        self.assertEqual(headers["Content-Type"], "application/pdf")
        csp = headers["Content-Security-Policy"]
        self.assertIn("frame-ancestors 'self'", csp)
        self.assertNotIn("script-src", csp)
        self.assertIn("default-src 'none'", csp)
        self.assertNotIn("sandbox", csp)
        self.assertEqual(headers["X-Frame-Options"], "SAMEORIGIN")
        # The rest of the app still denies framing.
        _, api_headers, _ = self.fetch("/api/health")
        self.assertEqual(api_headers["X-Frame-Options"], "DENY")
        self.assertIn("frame-ancestors 'none'", api_headers["Content-Security-Policy"])

    def test_active_content_is_downloaded_not_rendered(self):
        status, headers, _ = self.fetch(f"/api/media/{self.html_id}/raw")
        self.assertEqual(status, 200)
        self.assertEqual(headers["Content-Type"], "application/octet-stream")
        self.assertTrue(headers["Content-Disposition"].startswith("attachment"))

    def test_download_flag(self):
        _, headers, _ = self.fetch(f"/api/media/{self.video_id}/raw?download=1")
        self.assertTrue(headers["Content-Disposition"].startswith("attachment"))

    def test_workspace_raw(self):
        status, headers, body = self.fetch("/api/workspace/raw?path=attachments/clip.mp4", {"Range": "bytes=0-9"})
        self.assertEqual((status, body), (206, self.payload[:10]))
        status, headers, body = self.fetch("/api/workspace/raw?path=attachments/notes.md")
        self.assertEqual(status, 200)
        self.assertTrue(headers["Content-Type"].startswith("text/markdown"))
        status, _, _ = self.fetch("/api/workspace/raw?path=../outside.txt")
        self.assertIn(status, (400, 403, 404))
        status, _, _ = self.fetch("/api/workspace/raw?path=attachments")
        self.assertEqual(status, 400)
        status, _, _ = self.fetch("/api/workspace/raw")
        self.assertEqual(status, 400)

    def test_head_request_has_no_body(self):
        status, headers, body = self.fetch(f"/api/media/{self.video_id}/raw", method="HEAD")
        self.assertEqual((status, headers["Content-Length"], body), (200, "10240", b""))


if __name__ == "__main__":
    unittest.main()



class MediaCaptionTests(unittest.TestCase):
    def test_update_media_caption_keeps_url_and_logs(self):
        with tempfile.TemporaryDirectory() as tmp:
            app = App(Path(tmp) / "projects", seed_demo=True)
            try:
                store = app.require().store
                unit = store.list_units({"limit": 1})["items"][0]
                media = store.add_media(unit["id"], "attachments/a.png", "image", "old")
                updated = store.update_media(media["id"], {"caption": "  New caption ", "url": "x"})
                self.assertEqual(updated["caption"], "New caption")
                self.assertEqual(updated["url"], "attachments/a.png")
                fresh = store.get_unit(unit["id"])
                self.assertEqual([m["caption"] for m in fresh["media"] if m["id"] == media["id"]], ["New caption"])
                with self.assertRaises(Exception):
                    store.update_media(999999, {"caption": "x"})
            finally:
                app.project.close()
