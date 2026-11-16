import json
import os
import sqlite3
import tempfile
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio.server import App, serve


class ApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"  # never call a real model in tests
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=True)
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

    def request(self, path, *, method="GET", body=None, headers=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                raw = response.read()
                ctype = response.headers.get("Content-Type", "")
                return response.status, (json.loads(raw) if "json" in ctype else raw.decode("utf-8", "replace"))
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def test_infohub_browse_and_single_import(self):
        hub = Path(self.tmp.name) / "api-hub.db"
        con = sqlite3.connect(hub)
        con.executescript("""
            CREATE TABLE units(id INTEGER PRIMARY KEY, key TEXT, kind TEXT, title TEXT,
              summary TEXT, status TEXT, priority TEXT);
            CREATE TABLE unit_tags(unit_id INTEGER, tag TEXT);
            CREATE TABLE unit_links(parent_id INTEGER, child_id INTEGER, relation TEXT);
        """)
        con.execute("INSERT INTO units VALUES(1,'api-topic','topic','Control idea','A summary.','open','p1')")
        con.execute("INSERT INTO unit_tags VALUES(1,'topic:control')")
        con.commit(); con.close()
        import urllib.parse
        q = urllib.parse.quote(str(hub))
        status, data = self.request(f"/api/infohub/browse?path={q}&kind=topic")
        self.assertEqual(status, 200)
        self.assertEqual(data["total"], 1)
        self.assertEqual(data["items"][0]["key"], "api-topic")
        status, visual = self.request(f"/api/infohub/graph?path={q}&kind=topic&connected=0")
        self.assertEqual(status, 200)
        self.assertEqual(len(visual["nodes"]), 1)
        self.assertEqual(visual["nodes"][0]["uid"], "api-topic")
        self.assertEqual(visual["nodes"][0]["color"], "#3b82f6")
        status, detail = self.request(f"/api/infohub/unit?path={q}&key=api-topic")
        self.assertEqual(status, 200)
        self.assertEqual(detail["unit"]["key"], "api-topic")
        self.assertEqual(detail["unit"]["tags"], ["topic:control"])
        status, imported = self.request("/api/infohub/import", method="POST",
                                        body={"path": str(hub), "key": "api-topic", "as_idea": 1})
        self.assertEqual(status, 200)
        self.assertTrue(imported["imported"])
        self.assertEqual(imported["unit"]["source_ref"], "infohub:api-topic")
        status, again = self.request("/api/infohub/import", method="POST",
                                     body={"path": str(hub), "key": "api-topic"})
        self.assertEqual(status, 200)
        self.assertTrue(again["skipped"])

    def test_project_settings_graph_physics(self):
        status, data = self.request("/api/project")
        self.assertEqual(status, 200)
        graph = data["summary"]["settings"]["graph"]
        self.assertAlmostEqual(graph["repulsion"], 3286.064, places=3)
        self.assertAlmostEqual(graph["gravity"], 0.0115061, places=6)
        status, data = self.request("/api/project/settings", method="PATCH", body={
            "graph": {"repulsion": 4000, "distance": 80, "gravity": 0.004, "damping": 0.2},
        })
        self.assertEqual(status, 200)
        self.assertEqual(data["settings"]["graph"]["repulsion"], 4000)
        self.assertEqual(data["summary"]["settings"]["graph"]["distance"], 80)
        status, data = self.request("/api/project/settings", method="PATCH", body={
            "graph": {"repulsion": 999999},
        })
        self.assertEqual(status, 400)
        self.assertIn("between", data["error"]["message"])

    def test_chat_harness_and_persistence(self):
        status, data = self.request("/api/chat")
        self.assertEqual(status, 200)
        self.assertIn("messages", data)
        self.assertIn("harness", data)
        status, data = self.request("/api/chat", method="POST", body={
            "message": "Create idea: Chat API idea | Created through the chat harness.",
        })
        self.assertEqual(status, 200)
        self.assertEqual(data["user"]["role"], "user")
        self.assertEqual(data["assistant"]["role"], "assistant")
        self.assertTrue(data["refresh"])
        uid = data["assistant"]["tools"][0]["result"]["uid"]
        status, unit = self.request(f"/api/units/{uid}")
        self.assertEqual(status, 200)
        self.assertEqual(unit["unit"]["origin"], "assistant")
        status, history = self.request("/api/chat")
        self.assertGreaterEqual(len(history["messages"]), 2)
        status, cleared = self.request("/api/chat", method="DELETE")
        self.assertEqual(status, 200)
        self.assertGreaterEqual(cleared["cleared"], 2)

    def test_chat_stream_local(self):
        body = json.dumps({"message": "summary"}).encode()
        req = urllib.request.Request(self.base + "/api/chat/stream", data=body,
                                     headers={"Content-Type": "application/json"})
        events = []
        with urllib.request.urlopen(req, timeout=30) as response:
            self.assertIn("application/x-ndjson", response.headers.get("Content-Type", ""))
            for raw in response:
                events.append(json.loads(raw.decode()))
        kinds = [event["type"] for event in events]
        self.assertIn("status", kinds)
        self.assertIn("content", kinds)
        self.assertEqual(kinds[-1], "done")
        self.assertEqual(events[-1]["assistant"]["role"], "assistant")

    def test_health_bootstrap_static(self):
        status, data = self.request("/api/health")
        self.assertEqual(status, 200)
        self.assertTrue(data["ok"])
        status, boot = self.request("/api/bootstrap")
        self.assertEqual(status, 200)
        self.assertTrue(boot["summary"]["name"])
        self.assertGreater(boot["stats"]["total"], 50)
        self.assertTrue(any(t["id"] == "idea" for t in boot["vocabulary"]["types"]))
        status, html = self.request("/")
        self.assertEqual(status, 200)
        self.assertIn("ARK Studio", html)
        self.assertIn('name="ark-build"', html)
        self.assertIn("/js/app.js?v=", html)
        status, js = self.request("/js/app.js")
        self.assertEqual(status, 200)
        self.assertIn("ARK Studio", js)
        self.assertIn('from "./api.js?v=', js)

    def test_unit_crud_and_filters(self):
        status, data = self.request("/api/units", method="POST", body={
            "title": "API test idea", "type": "idea", "body": "from test",
            "tags": ["topic:api", "stage:capture"], "importance": "p1",
        })
        self.assertEqual(status, 200)
        unit = data["unit"]
        self.assertTrue(unit["uid"].startswith("I-"))
        status, data = self.request(f"/api/units/{unit['uid']}")
        self.assertEqual(data["unit"]["title"], "API test idea")
        status, data = self.request(f"/api/units/{unit['uid']}", method="PATCH", body={
            "title": "API test claim", "type": "claim", "status": "active", "tags": ["topic:api"],
        })
        self.assertEqual(status, 200)
        self.assertEqual(data["unit"]["type"], "claim")
        self.assertEqual(data["unit"]["status"], "active")
        status, data = self.request("/api/units?types=claim&tags=topic:api")
        self.assertEqual(status, 200)
        self.assertTrue(any(u["uid"] == unit["uid"] for u in data["items"]))
        self.assertIn("topic", data["facets"]["namespaces"])

    def test_graph_snapshot_contains_full_search_text_and_current_metadata(self):
        body = 'x' * 400 + ' full snapshot search'
        _, created = self.request('/api/units', method='POST', body={
            'title':'Snapshot API node', 'type':'idea', 'body':body,
            'tags':['topic:snapshot'],
        })
        status, snapshot = self.request('/api/graph/snapshot')
        self.assertEqual(status, 200)
        self.assertEqual(snapshot['total'], len(snapshot['nodes']))
        node = next(n for n in snapshot['nodes'] if n['id'] == created['unit']['id'])
        self.assertEqual(node['search_body'], body)
        self.assertNotIn('body', node)
        self.assertIn('topic:snapshot', node['tags'])
        self.assertLess(len(node['excerpt']), len(body))

    def test_explicit_no_types_filters_units_graph_and_timeline(self):
        _, baseline = self.request("/api/units")
        self.assertGreater(baseline["total"], 0)
        for endpoint, collection in (("units", "items"), ("graph", "nodes"), ("timeline", "items")):
            with self.subTest(endpoint=endpoint):
                status, data = self.request(
                    f"/api/{endpoint}?types_none=1&types=idea,claim&origins=infohub&context=1"
                )
                self.assertEqual(status, 200)
                self.assertEqual(data[collection], [])
                if endpoint == "graph":
                    self.assertEqual(data["edges"], [])
                    self.assertEqual(data["context_count"], 0)
        _, restored = self.request("/api/units?types_none=0")
        self.assertEqual(restored["total"], baseline["total"])

    def test_link_graph_and_assistant(self):
        status, data = self.request("/api/units", method="POST", body={"title": "Link source", "type": "idea"})
        source = data["unit"]
        status, data = self.request("/api/units", method="POST", body={"title": "Link target", "type": "claim"})
        target = data["unit"]
        status, data = self.request("/api/links", method="POST", body={
            "source": source["uid"], "target": target["uid"], "relation": "inspires",
        })
        self.assertEqual(status, 200)
        link_id = data["id"]
        status, graph = self.request(f"/api/graph?types=idea,claim&q=Link")
        self.assertEqual(status, 200)
        self.assertTrue(any(n["uid"] == source["uid"] for n in graph["nodes"]))
        status, insights = self.request("/api/assistant/insights")
        self.assertEqual(status, 200)
        self.assertIsInstance(insights["insights"], list)
        status, dispatch = self.request("/api/assistant/dispatch?task=T-0005")
        self.assertEqual(status, 200)
        self.assertIn("dispatch", dispatch["prompt"].lower())
        status, updated = self.request(f"/api/links/{link_id}", method="PATCH", body={"relation": "unset"})
        self.assertEqual(status, 200)
        self.assertEqual(updated["relation"], "unset")
        status, _ = self.request(f"/api/links/{link_id}", method="DELETE")
        self.assertEqual(status, 200)

    def test_agent_auth_and_write(self):
        status, _ = self.request("/api/agent/v1/context")
        self.assertEqual(status, 401)
        status, token_data = self.request("/api/agent/token")
        self.assertEqual(status, 200)
        token = token_data["token"]
        status, ctx = self.request("/api/agent/v1/context", headers={"Authorization": f"Bearer {token}"})
        self.assertEqual(status, 200)
        self.assertIn("counts", ctx)
        status, data = self.request("/api/agent/v1/units", method="POST", headers={
            "Authorization": f"Bearer {token}", "X-Agent-Name": "tester",
        }, body={"title": "Agent created", "type": "note", "tags": ["topic:api"]})
        self.assertEqual(status, 200)
        self.assertEqual(data["unit"]["origin"], "manual")
        self.assertEqual(data["unit"]["title"], "Agent created")

    def test_export_and_project_payload(self):
        status, project = self.request("/api/project")
        self.assertEqual(status, 200)
        self.assertIn("summary", project)
        status, markdown = self.request("/api/export?format=markdown")
        self.assertEqual(status, 200)
        self.assertIn("# PredActor", markdown)
        status, svg = self.request("/api/export?format=svg")
        self.assertEqual(status, 200)
        self.assertIn("<svg", svg)

    def test_markdown_bundle_download(self):
        import io
        import zipfile
        status, units = self.request("/api/units?limit=2")
        self.assertEqual(status, 200)
        uids = [unit["uid"] for unit in units["items"][:2]]
        req = urllib.request.Request(f"{self.base}/api/export?format=bundle&depth=0&units={','.join(uids)}")
        with urllib.request.urlopen(req, timeout=20) as response:
            self.assertEqual(response.headers["Content-Type"], "application/zip")
            self.assertIn("attachment", response.headers["Content-Disposition"])
            archive = zipfile.ZipFile(io.BytesIO(response.read()))
        notes = sorted(name.split("/nodes/")[1] for name in archive.namelist() if "/nodes/" in name)
        self.assertEqual(notes, sorted(f"{uid}.md" for uid in uids))
        status, error = self.request("/api/export?format=bundle&units=")
        self.assertEqual(status, 400)
        status, info = self.request(f"/api/export?format=bundle&depth=0&check=1&units={','.join(uids)}")
        self.assertEqual(status, 200)
        self.assertEqual(info["nodes"], 2)
        self.assertIn("bytes", info)

    def test_markdown_bundle_names_its_project(self):
        import io
        import zipfile
        status, first = self.request("/api/project")
        self.assertEqual(status, 200)
        first_id = first["summary"]["id"]
        project = self.app.project
        unit = project.store.create_unit({"title": "Bundle project media", "type": "note"})
        uid = unit["uid"]
        asset = project.path / "assets" / "bundle-project.txt"
        asset.write_bytes(b"media from the requested project")
        media = project.store.add_media(uid, "assets/bundle-project.txt", "file", "Project file")
        status, _ = self.request("/api/projects", method="POST", body={"name": "Bundle other", "template": "blank"})
        self.assertEqual(status, 200)
        try:
            # The session now points at the new project: the UID is unknown there.
            status, error = self.request(f"/api/export?format=bundle&check=1&units={uid}")
            self.assertEqual(status, 400)
            status, info = self.request(f"/api/export?format=bundle&check=1&units={uid}&project={first_id}")
            self.assertEqual(status, 200, info)
            self.assertGreaterEqual(info["nodes"], 1)
            self.assertEqual(info["files"], 1)
            self.assertEqual(info["missing"], 0)
            req = urllib.request.Request(f"{self.base}/api/export?format=bundle&units={uid}&project={first_id}")
            with urllib.request.urlopen(req, timeout=20) as response:
                archive = zipfile.ZipFile(io.BytesIO(response.read()))
            target = next(n for n in archive.namelist() if n.endswith(f"/media/{media['id']}-{asset.name}"))
            self.assertEqual(archive.read(target), asset.read_bytes())
            status, error = self.request(f"/api/export?format=bundle&check=1&units={uid}&project=prj_missing")
            self.assertEqual(status, 404)
        finally:
            self.request("/api/projects/open", method="POST", body={"id": first_id})

    def test_markdown_bundle_download_with_media(self):
        import io
        import zipfile
        project = self.app.project
        unit = project.store.create_unit({"title": "Bundle with media", "type": "note"})
        uid = unit["uid"]
        expected = {}
        for name, kind, content in [("bundle-figure.png", "image", b"test image"),
                                    ("bundle-clip.mp4", "video", b"test video")]:
            target = project.path / "assets" / name
            target.write_bytes(content)
            media = project.store.add_media(uid, f"assets/{name}", kind, name)
            expected[f"media/{media['id']}-{name}"] = content
            if kind == "image":
                project.store.update_unit(uid, {"body": f"![Figure](media:{media['id']})"})
        allowed = Path(self.tmp.name) / "bundle-allowed"
        allowed.mkdir(exist_ok=True)
        external = allowed / "allowed.txt"
        external.write_bytes(b"allowed external media")
        media = project.store.add_media(uid, str(external), "file", "Allowed")
        expected[f"media/{media['id']}-{external.name}"] = external.read_bytes()
        project.store.add_media(uid, "assets/bundle-missing.png", "image", "Missing")
        private = Path(self.tmp.name) / "bundle-private.txt"
        private.write_bytes(b"must stay outside the bundle")
        project.store.add_media(uid, str(private), "file", "Private")
        url = f"/api/export?format=bundle&depth=0&units={uid}"
        with mock.patch.object(self.app, "media_roots", [allowed.resolve()]):
            status, info = self.request(url + "&check=1")
            self.assertEqual(status, 200, info)
            self.assertEqual(info["files"], len(expected))
            self.assertEqual(info["missing"], 2)
            self.assertEqual(info["bytes"], sum(len(value) for value in expected.values()))
            with urllib.request.urlopen(self.base + url, timeout=20) as response:
                self.assertEqual(response.headers["Content-Type"], "application/zip")
                archive = zipfile.ZipFile(io.BytesIO(response.read()))
        self.assertIsNone(archive.testzip())
        root = archive.namelist()[0].split("/")[0]
        for path, content in expected.items():
            self.assertEqual(archive.read(f"{root}/{path}"), content)
        note = archive.read(f"{root}/nodes/{uid}.md").decode()
        self.assertIn("![Figure](../media/", note)
        self.assertIn('<video controls preload="metadata" src="../media/', note)
        manifest = json.loads(archive.read(f"{root}/manifest.json"))
        self.assertEqual(len([f for f in manifest["files"] if f.get("error")]), 2)
        self.assertFalse(any(n.endswith(private.name) for n in archive.namelist()))

    def test_agent_cannot_write_without_token(self):
        status, _ = self.request("/api/agent/v1/units", method="POST", body={"title": "No auth", "type": "note"})
        self.assertEqual(status, 401)

    def test_infohub_import_endpoint(self):
        from tests.python.store.test_importer import make_infohub
        path = make_infohub(Path(self.tmp.name) / "hub-api.db")
        status, preview = self.request(f"/api/import/infohub/preview?path={urllib.parse.quote(str(path))}")
        self.assertEqual(status, 200)
        self.assertEqual(preview["units"], 4)
        status, result = self.request("/api/import/infohub", method="POST", body={"path": str(path), "mode": "all"})
        self.assertEqual(status, 200)
        self.assertEqual(result["imported"], 4)
        self.assertGreaterEqual(result["links"], 1)
        status, again = self.request("/api/import/infohub", method="POST", body={"path": str(path), "mode": "all"})
        self.assertEqual(again["imported"], 0)
        self.assertEqual(again["skipped"], 4)

    # ---------------------------------------------------------- open externally
    def _project_file(self, name="open-me.txt", text="open me"):
        target = self.app.project.path / "assets" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")
        return target

    def test_open_project_relative_file_with_mocked_opener(self):
        target = self._project_file()
        mocked = {"opened": True, "kind": "file", "target": str(target),
                  "platform": "test", "opener": "fake"}
        with mock.patch("arkstudio.server.platform_support.open_external",
                        return_value=mocked) as opener:
            status, data = self.request("/api/open", method="POST",
                                        body={"path": "assets/open-me.txt"})
        self.assertEqual(status, 200, data)
        self.assertTrue(data["opened"])
        self.assertEqual(data["mode"], "path")
        opener.assert_called_once_with(target.resolve())

        # A local file:// URL is accepted only because it resolves inside the
        # active project.
        with mock.patch("arkstudio.server.platform_support.open_external",
                        return_value=mocked) as opener:
            status, data = self.request("/api/open", method="POST",
                                        body={"path": target.as_uri()})
        self.assertEqual(status, 200, data)
        opener.assert_called_once_with(target.resolve())

    def test_open_rejects_outside_absolute_traversal_and_file_urls(self):
        outside = Path(self.tmp.name) / "outside-open.txt"
        outside.write_text("no", encoding="utf-8")
        with mock.patch("arkstudio.server.platform_support.open_external") as opener:
            for body in (
                {"path": "../outside-open.txt"},
                {"path": "/etc/passwd"},
                {"path": str(outside)},
                {"path": outside.as_uri()},
                {"path": "assets/../../outside-open.txt"},
            ):
                with self.subTest(body=body):
                    status, data = self.request("/api/open", method="POST", body=body)
                    self.assertIn(status, (400, 403), data)
            opener.assert_not_called()

    def test_open_unit_with_local_media_and_clear_error_without_one(self):
        target = self._project_file("unit-open.svg", "<svg/>")
        status, data = self.request("/api/units", method="POST",
                                    body={"title": "Open target", "type": "artifact"})
        uid = data["unit"]["uid"]
        status, _ = self.request("/api/media", method="POST",
                                 body={"unit": uid, "url": "assets/unit-open.svg", "kind": "file"})
        self.assertEqual(status, 200)
        with mock.patch("arkstudio.server.platform_support.open_external",
                        return_value={"opened": True, "kind": "file", "target": str(target),
                                      "platform": "test", "opener": "fake"}) as opener:
            status, data = self.request("/api/open", method="POST", body={"unit": uid})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["mode"], "unit")
        opener.assert_called_once_with(target.resolve())

        status, plain = self.request("/api/units", method="POST",
                                     body={"title": "No local media", "type": "note"})
        plain_uid = plain["unit"]["uid"]
        with mock.patch("arkstudio.server.platform_support.open_external") as opener:
            status, data = self.request("/api/open", method="POST", body={"unit": plain_uid})
        self.assertEqual(status, 400, data)
        self.assertIn("no local media file", data["error"]["message"])
        opener.assert_not_called()

    def test_open_url_uses_default_browser_and_same_origin_protection(self):
        with mock.patch("arkstudio.server.platform_support.default_browser_open",
                        return_value={"opened": True, "kind": "url",
                                      "target": "https://example.org/paper",
                                      "platform": "test", "opener": "webbrowser"}) as opener:
            status, data = self.request("/api/open", method="POST",
                                        body={"url": "https://example.org/paper"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["mode"], "url")
        opener.assert_called_once_with("https://example.org/paper")

        status, data = self.request("/api/open", method="POST",
                                    body={"url": "https://example.org/paper"},
                                    headers={"Origin": "https://evil.example"})
        self.assertEqual(status, 403, data)


if __name__ == "__main__":
    unittest.main()
