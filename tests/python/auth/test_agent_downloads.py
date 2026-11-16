"""Bearer-only downloads enforce account, project and filesystem boundaries."""
import io
import json
import urllib.error
import urllib.request
import zipfile
from pathlib import Path
from unittest import mock

from tests.python.auth.test_org import AuthHttpCase, NoRedirect


class AgentDownloadTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("reader", "reader-pass").status, 200)
        self.reader = self.login("reader", "reader-pass")
        self.project = self.app.project
        self.main_id = self.project.id
        self.assertEqual(self.grant(self.main_id, user="reader", permission="read").status, 200)
        made = self.api("/api/personal/agent-tokens", cookie=self.reader, method="POST",
                        body={"label": "downloads", "scope": "read", "expires_days": 30}).json()
        self.token, self.token_id = made["token"], made["item"]["id"]
        self.content = b"\x89PNG\r\n\x1a\n\x00\xff\x80project media"
        self.asset = self.project.path / "assets" / "download.png"
        self.asset.write_bytes(self.content)
        self.unit = self.project.store.create_unit({"title": "Download reply", "type": "artifact"})
        self.media = self.project.store.add_media(self.unit["uid"], "assets/download.png", "image", "Figure")
        self.project.store.update_unit(self.unit["uid"], {"body": f"![Figure](media:{self.media['id']})"})

    def download(self, path, *, method="GET", token=None, project=None, headers=None):
        auth_headers = {"Authorization": f"Bearer {self.token if token is None else token}",
                        "X-ARK-Project": project or self.main_id, **(headers or {})}
        req = urllib.request.Request(self.base + path, method=method, headers=auth_headers)
        opener = urllib.request.build_opener(NoRedirect())
        try:
            response = opener.open(req, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.code, response.headers, response.read()

    def media_paths(self):
        return [f"/api/agent/v1/media/{self.media['id']}/raw",
                f"/api/media/{self.media['id']}/raw"]

    def export_paths(self):
        return [f"{prefix}/export?format=bundle&units={self.unit['uid']}&depth=0"
                for prefix in ("/api/agent/v1", "/api")]

    def test_media_binary_head_range_and_download_without_cookie(self):
        # Personal tokens work through the remote/mobile gate as well as loopback.
        with mock.patch("arkstudio.server.Handler._trusted_local", return_value=False):
            for path in self.media_paths():
                with self.subTest(path=path):
                    status, headers, raw = self.download(path + "?download=1")
                    self.assertEqual(status, 200, raw)
                    self.assertEqual(raw, self.content)
                    self.assertEqual(headers["Content-Type"], "application/octet-stream")
                    self.assertIn("attachment", headers["Content-Disposition"])
                    status, headers, raw = self.download(path, method="HEAD")
                    self.assertEqual(status, 200)
                    self.assertEqual(headers["Content-Type"], "image/png")
                    self.assertEqual(raw, b"")
                    self.assertEqual(int(headers["Content-Length"]), len(self.content))
                    status, headers, raw = self.download(path, headers={"Range": "bytes=2-7"})
                    self.assertEqual(status, 206)
                    self.assertEqual(raw, self.content[2:8])
                    self.assertEqual(headers["Content-Range"], f"bytes 2-7/{len(self.content)}")

    def test_bundle_preflight_zip_and_head_without_cookie(self):
        with mock.patch("arkstudio.server.Handler._trusted_local", return_value=False):
            for path in self.export_paths():
                with self.subTest(path=path):
                    status, _, raw = self.download(path + "&check=1")
                    self.assertEqual(status, 200, raw)
                    info = json.loads(raw)
                    self.assertEqual((info["selected"], info["files"], info["missing"]), (1, 1, 0))
                    status, headers, raw = self.download(path)
                    self.assertEqual(status, 200, raw)
                    self.assertEqual(headers["Content-Type"], "application/zip")
                    self.assertIn("attachment", headers["Content-Disposition"])
                    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                        self.assertIsNone(archive.testzip())
                        root = archive.namelist()[0].split("/")[0]
                        asset = f"media/{self.media['id']}-download.png"
                        self.assertEqual(archive.read(f"{root}/{asset}"), self.content)
                        note = archive.read(f"{root}/nodes/{self.unit['uid']}.md").decode()
                        self.assertIn(f"![Figure](../{asset})", note)
                    status, headers, raw = self.download(path, method="HEAD")
                    self.assertEqual(status, 200)
                    self.assertEqual(raw, b"")
                    self.assertGreater(int(headers["Content-Length"]), 0)

    def test_other_projects_and_query_overrides_remain_private(self):
        created = self.api("/api/projects", cookie=self.owner, method="POST",
                           body={"name": "Private", "template": "blank"})
        hidden = created.json()["summary"]["id"]
        for path in self.media_paths() + self.export_paths():
            self.assertEqual(self.download(path, project=hidden)[0], 404)
        for path in self.export_paths():
            self.assertEqual(self.download(path + f"&project={hidden}")[0], 404)
        # Losing the original grant takes effect immediately, including aliases.
        revoked = self.api(f"/api/org/projects/{self.main_id}/grants/revoke", cookie=self.owner,
                           method="POST", body={"user": "reader"})
        self.assertEqual(revoked.status, 200, revoked.text)
        for path in self.media_paths() + self.export_paths():
            self.assertEqual(self.download(path)[0], 404)

    def test_invalid_expired_and_revoked_tokens_cannot_download(self):
        paths = self.media_paths() + self.export_paths()
        for path in paths:
            self.assertEqual(self.download(path, token="")[0], 401)
            self.assertEqual(self.download(path, token="arka_invalid")[0], 401)
            # Tokens in query strings never authorize a download.
            sep = "&" if "?" in path else "?"
            self.assertEqual(self.download(path + sep + "token=" + self.token, token="")[0], 401)
        with mock.patch.object(self.app.org.agent_tokens, "clock", return_value=10**12):
            for path in paths:
                self.assertEqual(self.download(path)[0], 401)
        self.assertEqual(self.api(f"/api/personal/agent-tokens/{self.token_id}", cookie=self.reader,
                                  method="DELETE").status, 200)
        for path in paths:
            self.assertEqual(self.download(path)[0], 401)

    def test_disabled_account_and_header_token_auth(self):
        for path in self.media_paths() + self.export_paths():
            self.assertEqual(self.download(path, token="", headers={"X-ARK-Token": self.token,
                                                                    "Authorization": ""})[0], 200)
        reader_id = self.app.org.user_id_for("reader")
        self.app.org.update_user("owner", reader_id, {"disabled": True})
        for path in self.media_paths() + self.export_paths():
            self.assertEqual(self.download(path)[0], 401)

    def test_filesystem_boundary_and_missing_media(self):
        outside = Path(self.tmp.name) / "private.png"
        outside.write_bytes(b"private")
        denied = self.project.store.add_media(self.unit["uid"], str(outside), "image", "Private")
        missing = self.project.store.add_media(self.unit["uid"], "assets/missing.png", "image", "Missing")
        for prefix in ("/api/agent/v1", "/api"):
            self.assertEqual(self.download(f"{prefix}/media/{denied['id']}/raw")[0], 403)
            self.assertEqual(self.download(f"{prefix}/media/{missing['id']}/raw")[0], 404)
            self.assertEqual(self.download(f"{prefix}/media/999999/raw")[0], 404)

    def test_read_token_does_not_unlock_other_browser_routes_or_writes(self):
        for path in ("/api/project", "/api/personal/agent-tokens", "/api/workspace/raw?path=project.json"):
            self.assertEqual(self.download(path)[0], 401)
        for path in self.media_paths()[1:] + self.export_paths()[1:]:
            for method in ("POST", "PATCH", "DELETE"):
                self.assertEqual(self.download(path, method=method)[0], 401)
        self.assertEqual(self.download("/api/agent/v1/units", method="POST")[0], 403)
