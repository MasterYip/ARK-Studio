"""Workbench Search: matcher options, file/node search limits and routes."""
import json
import os
import tempfile
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from arkstudio import search
from arkstudio.server import App, serve


class MatcherTests(unittest.TestCase):
    def test_case_word_regex(self):
        lines = "Alpha alphabet ALPHA\nbeta"
        hits, _ = search.line_matches(lines, search.build_matcher("alpha"))
        self.assertEqual(hits[0]["line"], 1)
        self.assertEqual(hits[0]["ranges"], [[0, 5], [6, 11], [15, 20]])
        hits, _ = search.line_matches(lines, search.build_matcher("alpha", case=True))
        self.assertEqual(hits[0]["ranges"], [[6, 11]])
        hits, _ = search.line_matches(lines, search.build_matcher("alpha", word=True))
        self.assertEqual(hits[0]["ranges"], [[0, 5], [15, 20]])
        hits, _ = search.line_matches(lines, search.build_matcher(r"b\w+a", regex=True))
        self.assertEqual([h["line"] for h in hits], [2])
        # Regex metacharacters are literal without the regex toggle.
        hits, _ = search.line_matches("a.b axb", search.build_matcher("a.b"))
        self.assertEqual(hits[0]["ranges"], [[0, 3]])
        for bad in ("", "   ", "(", "x*"):
            with self.assertRaises(search.SearchError):
                search.build_matcher(bad, regex=True)

    def test_long_lines_are_clipped_around_the_match(self):
        line = "x" * 1000 + "needle" + "y" * 1000
        hits, _ = search.line_matches(line, search.build_matcher("needle"))
        hit = hits[0]
        self.assertLessEqual(len(hit["text"]), search.MAX_LINE_CHARS)
        a, b = hit["ranges"][0]
        self.assertEqual(hit["text"][a:b], "needle")
        self.assertEqual(hit["offset"] + a, 1000)

    def test_globs(self):
        accept = search.path_filter("*.md, src/**", "drafts, *.lock")
        self.assertTrue(accept("README.md"))
        self.assertTrue(accept("src/app/main.py"))
        self.assertFalse(accept("notes/a.txt"))
        self.assertFalse(accept("drafts/x.md"))
        self.assertTrue(search.path_filter("", "")("anything/at/all.bin"))


class FileSearchTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "docs").mkdir()
        (self.root / "docs" / "a.md").write_text("first\nthe Needle here\nneedle again\n", encoding="utf-8")
        (self.root / "docs" / "b.txt").write_text("nothing\n", encoding="utf-8")
        (self.root / "blob.bin").write_bytes(b"needle\x00\x01\x02")
        (self.root / "big.txt").write_bytes(b"needle\n" * (search.MAX_FILE_BYTES // 7 + 10))
        (self.root / "node_modules").mkdir()
        (self.root / "node_modules" / "dep.js").write_text("needle", encoding="utf-8")

    def tearDown(self):
        self.tmp.cleanup()

    def test_matches_skips_binaries_big_files_and_ignored_dirs(self):
        data = search.search_files(self.root, "needle")
        self.assertEqual([f["path"] for f in data["files"]], ["docs/a.md"])
        self.assertEqual([m["line"] for m in data["files"][0]["matches"]], [2, 3])
        self.assertEqual(data["skipped"], 1, "the oversized file is skipped")
        self.assertEqual(data["total"], 2)
        self.assertFalse(data["truncated"])

    def test_include_exclude_and_time_limit(self):
        self.assertEqual(search.search_files(self.root, "needle", include="*.txt")["files"], [])
        self.assertEqual(search.search_files(self.root, "needle", exclude="docs")["files"], [])
        timed = search.search_files(self.root, "needle", time_limit=0)
        self.assertTrue(timed["timed_out"])
        self.assertTrue(timed["truncated"])


class SearchRouteTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
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

    def get(self, path, params):
        url = self.base + path + "?" + urllib.parse.urlencode(params)
        try:
            with urllib.request.urlopen(url, timeout=15) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def post(self, path, body):
        req = urllib.request.Request(self.base + path, data=json.dumps(body).encode(), method="POST",
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.loads(response.read())

    def test_node_search_covers_title_body_tags_and_comments(self):
        store = self.app.project.store
        unit = store.create_unit({"type": "idea", "title": "Zebra crossing study",
                                  "body": "line one\nwatch the zebra herd", "tags": ["habitat:zebra-plains"]})
        store.add_comment(unit["id"], "a zebra comment")
        status, data = self.get("/api/search/nodes", {"q": "zebra"})
        self.assertEqual(status, 200)
        hit = next(node for node in data["nodes"] if node["uid"] == unit["uid"])
        fields = sorted({m["field"] for m in hit["matches"]})
        self.assertEqual(fields, ["body", "comment", "tags", "title"])
        body = next(m for m in hit["matches"] if m["field"] == "body")
        self.assertEqual(body["line"], 2)
        a, b = body["ranges"][0]
        self.assertEqual(body["text"][a:b], "zebra")
        status, data = self.get("/api/search/nodes", {"q": "Zebra", "case": "1"})
        hit = next(node for node in data["nodes"] if node["uid"] == unit["uid"])
        self.assertEqual({m["field"] for m in hit["matches"]}, {"title"})
        status, data = self.get("/api/search/nodes", {"q": "(", "regex": "1"})
        self.assertEqual(status, 400)

    def test_file_grep_route(self):
        root = Path(self.app.project.path)
        (root / "grep-notes").mkdir(exist_ok=True)
        (root / "grep-notes" / "hit.md").write_text("alpha\nunique-grep-token here\n", encoding="utf-8")
        status, data = self.get("/api/workspace/grep", {"q": "unique-grep-token", "include": "grep-notes"})
        self.assertEqual(status, 200)
        self.assertEqual([f["path"] for f in data["files"]], ["grep-notes/hit.md"])
        self.assertEqual(data["files"][0]["matches"][0]["line"], 2)
        status, data = self.get("/api/workspace/grep", {"q": ""})
        self.assertEqual(status, 400)


if __name__ == "__main__":
    unittest.main()
