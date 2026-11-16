"""Fast Workbench Search returns exactly what the plain scan returned.

``reference_search_files`` / ``reference_search_nodes`` are the original
line-by-line implementations (before the in-memory node index, the parallel
cached file scan, the prefilters and the ripgrep candidate finder), kept here
as the oracle.  They share the unchanged ``build_matcher`` / ``line_matches``
/ ``path_filter`` and read the limits from ``arkstudio.search`` so tests can
lower them for both at once.
"""
import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio import search
from arkstudio import workspace as ws
from arkstudio.server import App, serve
from arkstudio.store import Store


def reference_search_files(root, query, *, case=False, word=False, regex=False, include="", exclude="",
                           time_limit=60.0):
    matcher = search.build_matcher(query, case=case, word=word, regex=regex)
    accept = search.path_filter(include, exclude)
    root_path = Path(root).resolve()
    started = time.monotonic()
    files = []
    total = visited = skipped = 0
    truncated = timed_out = False
    for current, dirs, names in os.walk(root_path, followlinks=False):
        dirs[:] = sorted(
            (name for name in dirs
             if not ws._is_ignored(name)
             and not (Path(current) == root_path and ws._is_protected(root_path, name))
             and not ws._is_link_like(Path(current) / name)),
            key=str.lower)
        for name in sorted(names, key=str.lower):
            if time.monotonic() - started >= time_limit:
                timed_out = truncated = True
                break
            if visited >= search.MAX_FILES_VISITED or len(files) >= search.MAX_ITEMS or total >= search.MAX_RESULTS:
                truncated = True
                break
            child = Path(current) / name
            if ws._is_link_like(child) or ws._is_ignored(name):
                continue
            rel = child.relative_to(root_path).as_posix()
            if not accept(rel):
                continue
            visited += 1
            try:
                if child.stat().st_size > search.MAX_FILE_BYTES:
                    skipped += 1
                    continue
                raw = child.read_bytes()
            except OSError:
                skipped += 1
                continue
            if ws._looks_binary(raw):
                continue
            text = raw.decode("utf-8", "replace")
            hits, capped = search.line_matches(text, matcher, min(search.MAX_PER_ITEM, search.MAX_RESULTS - total))
            if not hits:
                continue
            total += len(hits)
            files.append({"path": rel, "name": name, "matches": hits, "capped": capped})
        if truncated:
            break
    return {"query": str(query), "files": files, "total": total, "visited": visited,
            "skipped": skipped, "truncated": truncated, "timed_out": timed_out}


def reference_search_nodes(store, query, *, case=False, word=False, regex=False, time_limit=60.0):
    matcher = search.build_matcher(query, case=case, word=word, regex=regex)
    started = time.monotonic()
    with store.lock:
        units = store.con.execute(
            "SELECT id, uid, type, title, body, status FROM units WHERE archived=0 ORDER BY updated_at DESC").fetchall()
        tag_rows = store.con.execute("SELECT unit_id, tag FROM unit_tags").fetchall()
        comment_rows = store.con.execute(
            "SELECT id, unit_id, author, body FROM unit_comments ORDER BY created_at").fetchall()
    tags, comments = {}, {}
    for row in tag_rows:
        tags.setdefault(row["unit_id"], []).append(row["tag"])
    for row in comment_rows:
        comments.setdefault(row["unit_id"], []).append(row)
    nodes = []
    total = 0
    truncated = timed_out = False
    for unit in units:
        if time.monotonic() - started >= time_limit:
            timed_out = truncated = True
            break
        if len(nodes) >= search.MAX_ITEMS or total >= search.MAX_RESULTS:
            truncated = True
            break
        found = []
        budget = min(search.MAX_PER_ITEM, search.MAX_RESULTS - total)

        def add(field, text, extra=None):
            nonlocal budget
            if budget <= 0 or not text:
                return
            hits, _ = search.line_matches(text, matcher, budget)
            for hit in hits:
                found.append({"field": field, **hit, **(extra or {})})
            budget -= len(hits)

        add("title", unit["title"])
        add("body", unit["body"])
        unit_tags = [tag for tag in tags.get(unit["id"], []) if not tag.startswith(("type:", "status:", "importance:"))]
        if unit_tags:
            add("tags", " · ".join(unit_tags))
        for comment in comments.get(unit["id"], []):
            add("comment", comment["body"], {"comment_id": comment["id"], "author": comment["author"]})
        if found:
            total += len(found)
            nodes.append({"id": unit["id"], "uid": unit["uid"], "type": unit["type"], "title": unit["title"],
                          "status": unit["status"], "matches": found})
    return {"query": str(query), "nodes": nodes, "total": total, "truncated": truncated, "timed_out": timed_out}


def comparable(result):
    return {key: value for key, value in result.items() if key not in ("elapsed_ms", "backend")}


# Lines exercising case folding corner cases of ``re`` (dotted/dotless i,
# long s, Kelvin sign, Greek final sigma), CJK, emoji, separators other than
# "\n" (``str.splitlines`` splits on them) and long lines.
TRICKY = (
    "İstanbul and ıssue and ſtar and Kelvin\n"
    "ΣΊΣΥΦΟΣ σίσυφος ς\n"
    "研究 observation 🚀 launch 🚀🚀\n"
    "form\x0cfeed line para\x85next\r\nwindows\rmac\n"
    + "x" * 600 + " needle " + "y" * 600 + "\n"
)

FILE_QUERIES = [
    ("needle", {}), ("NEEDLE", {"case": True}), ("Needle", {"case": True}), ("needle", {"word": True}),
    ("need", {"word": True}), ("ne+dle", {"regex": True}), (r"^needle", {"regex": True}),
    (r"needle$", {"regex": True}), (r"(?<=the )needle", {"regex": True}), (r"\bneedle\b", {"regex": True}),
    (r"needle|研究", {"regex": True}), (r"(?i)NEEDLE", {"regex": True, "case": True}),
    ("研究", {}), ("🚀", {}), ("🚀🚀", {}), ("launch 🚀", {"word": True}),
    ("s", {}), ("is", {}), ("ss", {}), ("k", {}), ("ı", {}), ("İ", {}), ("σ", {}), ("Σ", {"case": True}),
    ("ΣΊΣΥΦΟΣ", {}), ("feed", {}), ("line", {"word": True}), ("windows", {}), ("mac", {"word": True}),
    ("a.b", {}), ("[x]", {}), ("x" * 50, {}), ("zzz-not-there", {}),
    ("needle", {"include": "*.md"}), ("needle", {"include": "docs, src/**"}), ("needle", {"exclude": "docs"}),
    ("needle", {"include": "*.txt", "exclude": "deep"}), ("needle", {"include": "src/a"}),
]


class FixtureMixin:
    def make_tree(self, root: Path) -> None:
        def put(rel, data):
            target = root / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            if isinstance(data, bytes):
                target.write_bytes(data)
            else:
                target.write_text(data, encoding="utf-8", newline="")

        put("README.md", "Title\nthe needle is here\nNeedle again, NEEDLE.\n" + TRICKY)
        put("Zeta.txt", "needle in zeta\n")
        put("alpha.TXT", "needle_word not a word match\nneedle.\n")
        put("docs/a.md", TRICKY + "needle\n")
        put("docs/B.md", "nothing\n")
        put("docs/deep/x/y/z.txt", "deep needle\n" * 5)
        put("Docs2/c.md", "研究 and needle\n")
        put("src/a/main.py", "def needle():\n    return 'needle'  # needle\n")
        put("src/b.js", "const needle = 1;\n")
        put("blob.bin", b"needle\x00\x01\x02")
        put("latin1.txt", "caf\xe9 needle\n".encode("latin-1"))
        put("bom.txt", "﻿needle with bom\n")
        put("empty.txt", "")
        put("big.txt", b"needle\n" * (search.MAX_FILE_BYTES // 7 + 10))
        put("node_modules/dep.js", "needle\n")
        put("__pycache__/x.txt", "needle\n")
        put("cache.pyc", b"needle")
        put("setup/private.txt", "needle secret\n")      # protected once project.json exists
        put("project.json", "{}\n")
        put("many/" + "f%03d.txt" % 0, "needle\n")
        for n in range(1, 40):
            put("many/f%03d.txt" % n, ("needle %d\n" % n) * (n % 4 + 1))


class FileEquivalenceTests(FixtureMixin, unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / "ws"
        self.root.mkdir()
        self.make_tree(self.root)
        search.clear_text_cache()

    def tearDown(self):
        self.tmp.cleanup()

    def assertSame(self, query, opts, **kwargs):
        expected = reference_search_files(self.root, query, **opts)
        for use_rg in (False, None):
            for attempt in ("cold", "warm"):
                got = search.search_files(self.root, query, use_rg=use_rg, time_limit=60, **opts, **kwargs)
                self.assertEqual(comparable(got), expected, f"{query!r} {opts} use_rg={use_rg} {attempt}")
        return expected

    def test_queries_match_the_plain_scan(self):
        nonempty = 0
        for query, opts in FILE_QUERIES:
            with self.subTest(query=query, **opts):
                nonempty += bool(self.assertSame(query, opts)["files"])
        self.assertGreater(nonempty, 25, "the fixture exercises real matches")

    def test_truncation_flags_and_limits(self):
        limits = [dict(MAX_ITEMS=3), dict(MAX_RESULTS=7), dict(MAX_PER_ITEM=2), dict(MAX_FILES_VISITED=5),
                  dict(MAX_ITEMS=1, MAX_PER_ITEM=1)]
        for patch in limits:
            with self.subTest(**patch), mock.patch.multiple(search, **patch):
                result = self.assertSame("needle", {})
                if list(patch) == ["MAX_PER_ITEM"]:
                    self.assertTrue(any(f["capped"] for f in result["files"]))
                else:
                    self.assertTrue(result["truncated"])
                self.assertSame("needle", {"include": "many"})
                self.assertSame("研究", {})

    def test_file_changes_are_seen_through_the_cache(self):
        self.assertSame("needle", {})
        target = self.root / "docs" / "B.md"
        target.write_text("now a needle\n", encoding="utf-8")
        os.utime(target, ns=(time.time_ns(), time.time_ns() + 5_000_000))
        result = self.assertSame("needle", {})
        self.assertIn("docs/B.md", [f["path"] for f in result["files"]])
        (self.root / "docs" / "a.md").unlink()
        self.assertSame("needle", {})

    def test_links_are_not_followed(self):
        outside = Path(self.tmp.name) / "outside"
        outside.mkdir()
        (outside / "secret.txt").write_text("needle outside\n", encoding="utf-8")
        try:
            os.symlink(outside, self.root / "link-dir", target_is_directory=True)
            os.symlink(outside / "secret.txt", self.root / "link.txt")
        except (OSError, NotImplementedError):
            self.skipTest("symlinks need extra privileges here")
        result = self.assertSame("needle", {})
        self.assertFalse([f for f in result["files"] if "link" in f["path"]])

    def test_ripgrep_candidates_only_narrow_the_files(self):
        """A candidate set (always a superset of the files with a matching
        line) never changes the result; None falls back to the scan."""
        expected = reference_search_files(self.root, "needle")
        everything = {p.relative_to(self.root).as_posix() for p in self.root.rglob("*") if p.is_file()}
        holders = {p for p in everything if b"needle" in (self.root / p).read_bytes().lower()}
        for candidates, backend in ((holders, "rg"), (everything, "rg"), (None, "python")):
            with mock.patch.object(search, "_rg_candidates", return_value=candidates):
                got = search.search_files(self.root, "needle", use_rg=True, time_limit=60)
            self.assertEqual(comparable(got), expected)
            self.assertEqual(got["backend"], backend)
        with mock.patch.object(search, "_rg_candidates", side_effect=AssertionError("regex never uses rg")):
            search.search_files(self.root, "ne+dle", regex=True, use_rg=True)

    @unittest.skipUnless(search.ripgrep_path(), "ripgrep is not installed (set ARK_RG to its path)")
    def test_real_ripgrep_backend(self):
        for query, opts in FILE_QUERIES:
            if opts.get("regex"):
                continue
            with self.subTest(query=query, **opts):
                search.clear_text_cache()
                expected = reference_search_files(self.root, query, **opts)
                got = search.search_files(self.root, query, use_rg=True, time_limit=60, **opts)
                self.assertEqual(comparable(got), expected)

    def test_streaming_generator_and_cancel(self):
        items = list(search.iter_search_files(self.root, "needle", time_limit=60))
        summary = items.pop()
        self.assertTrue(summary.pop("done"))
        expected = reference_search_files(self.root, "needle")
        self.assertEqual(items, expected["files"])
        calls = []

        def cancelled():
            calls.append(1)
            return len(calls) > 3
        items = list(search.iter_search_files(self.root, "needle", cancelled=cancelled, time_limit=60))
        self.assertTrue(items[-1]["truncated"])
        self.assertLess(len(items) - 1, len(expected["files"]))
        stream = search.iter_search_files(self.root, "needle", time_limit=60)
        next(stream)
        stream.close()   # the client went away: no error, the pool is released

    def test_prefilter_never_drops_a_matching_text(self):
        texts = [TRICKY, "needle", "NEEDLE\nx", "a needle", "ſ s S", "ı i I İ", "K k K", "ς σ Σ", "研究🚀"]
        queries = [("needle", {}), ("s", {}), ("i", {}), ("k", {}), ("ı", {}), ("İ", {}), ("σ", {}), ("Σ", {}),
                   ("Needle", {"case": True}), ("needle", {"word": True}), ("🚀", {}), ("研究", {}),
                   (r"^needle$", {"regex": True}), (r"(?<!x)needle", {"regex": True}), (r"\bk\b", {"regex": True})]
        for text in texts:
            item = search.Text(text)
            for query, opts in queries:
                matcher = search.build_matcher(query, **opts)
                has_line = bool(search.line_matches(text, matcher)[0])
                prefilter = search.Prefilter(matcher, query, case=opts.get("case", False), regex=opts.get("regex", False))
                if has_line:
                    self.assertTrue(prefilter(item), f"{query!r} {opts} in {text!r}")

    def test_ascii_fold_extra_covers_every_non_ascii_ignorecase_partner(self):
        import re
        chars = "".join(chr(c) for c in range(0x80, 0x110000) if not 0xD800 <= c <= 0xDFFF)
        partners = set()
        for c in "abcdefghijklmnopqrstuvwxyz":
            partners.update(re.findall(re.escape(c), chars, re.IGNORECASE))
        self.assertTrue(partners)
        self.assertLessEqual(partners, set(search.ASCII_FOLD_EXTRA))


@unittest.skipUnless(shutil.which("git"), "git is not installed")
class GitignoreTests(FixtureMixin, unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / "repo"
        self.root.mkdir()
        self.make_tree(self.root)
        (self.root / ".gitignore").write_text("Zeta.txt\ngen/\n*.log\n", encoding="utf-8")
        (self.root / "gen").mkdir()
        (self.root / "gen" / "out.txt").write_text("needle generated\n", encoding="utf-8")
        (self.root / "run.log").write_text("needle log\n", encoding="utf-8")
        (self.root / "kept.log").write_text("needle tracked log\n", encoding="utf-8")
        git = ["git", "-c", "user.email=t@example.invalid", "-c", "user.name=t"]
        subprocess.run(git + ["init", "-q"], cwd=self.root, check=True)
        subprocess.run(git + ["add", "-f", "kept.log"], cwd=self.root, check=True)
        search._git_roots.clear()
        search.clear_text_cache()

    def tearDown(self):
        search._git_roots.clear()
        self.tmp.cleanup()

    def test_gitignored_files_are_skipped_but_tracked_ones_stay(self):
        result = search.search_files(self.root, "needle", time_limit=60)
        paths = [f["path"] for f in result["files"]]
        expected = [f["path"] for f in reference_search_files(self.root, "needle")["files"]
                    if f["path"] not in ("Zeta.txt", "gen/out.txt", "run.log")]
        self.assertEqual(paths, expected)
        self.assertIn("kept.log", paths)


class NodeEquivalenceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = Store(Path(self.tmp.name) / "p")
        s = self.store
        self.units = []
        for n in range(30):
            unit = s.create_unit({
                "type": "idea" if n % 2 else "evidence",
                "title": f"Needle {n} 研究" if n % 3 == 0 else f"title {n}",
                "body": (TRICKY if n % 5 == 0 else "") + f"line one\nthe needle number {n}\n🚀 rocket" * (n % 3),
                "tags": ["habitat:needle-plains", "status:open"] if n % 4 == 0 else ["x"],
            })
            self.units.append(unit)
            if n % 6 == 0:
                s.add_comment(unit["id"], f"a needle comment {n}\nsecond ΣΊΣΥΦΟΣ line")
        s.archive_unit(self.units[3]["id"])

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def assertSame(self, query, opts):
        expected = reference_search_nodes(self.store, query, **opts)
        for attempt in ("cold", "cached"):
            got = search.search_nodes(self.store, query, time_limit=60, **opts)
            self.assertEqual(comparable(got), expected, f"{query!r} {opts} {attempt}")
        return expected

    def test_queries_match_the_plain_scan(self):
        nonempty = 0
        for query, opts in FILE_QUERIES:
            if "include" in opts or "exclude" in opts:
                continue
            with self.subTest(query=query, **opts):
                nonempty += bool(self.assertSame(query, opts)["nodes"])
        self.assertGreater(nonempty, 15)
        self.assertSame("plains", {})
        self.assertSame("status:open", {})   # hidden tag prefixes stay unsearched

    def test_truncation_flags(self):
        for patch in (dict(MAX_ITEMS=2), dict(MAX_RESULTS=5), dict(MAX_PER_ITEM=1)):
            with self.subTest(**patch), mock.patch.multiple(search, **patch):
                self.assertSame("needle", {})
                self.assertSame("line", {"word": True})

    def test_cache_follows_every_kind_of_write(self):
        self.assertSame("needle", {})
        unit = self.units[1]
        self.store.update_unit(unit["id"], {"body": "fresh needle body"})
        self.assertSame("fresh", {})
        self.store.update_unit(unit["id"], {"tags": ["needle-tag-new"]})
        self.assertSame("needle-tag-new", {})
        self.store.add_comment(self.units[2]["id"], "comment with kiwi")
        self.assertSame("kiwi", {})
        self.store.archive_unit(self.units[0]["id"])
        self.assertSame("needle", {})
        self.store.create_unit({"type": "idea", "title": "brand new needle"})
        self.assertSame("needle", {})

    def test_streaming_generator(self):
        items = list(search.iter_search_nodes(self.store, "needle", time_limit=60))
        summary = items.pop()
        self.assertTrue(summary["done"])
        self.assertEqual(items, reference_search_nodes(self.store, "needle")["nodes"])
        items = list(search.iter_search_nodes(self.store, "needle", cancelled=lambda: True))
        self.assertEqual(len(items), 1)
        self.assertTrue(items[0]["truncated"])


class StreamingRouteTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=True)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"
        root = Path(cls.app.project.path)
        (root / "stream-notes").mkdir(exist_ok=True)
        for n in range(5):
            (root / "stream-notes" / f"n{n}.md").write_text(f"x\nstream-token {n}\n", encoding="utf-8")

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

    def fetch(self, path, params, gzip_ok=False):
        url = self.base + path + "?" + urllib.parse.urlencode(params)
        request = urllib.request.Request(url, headers={"Accept-Encoding": "gzip"} if gzip_ok else {})
        with urllib.request.urlopen(request, timeout=15) as response:
            raw = response.read()
            if response.headers.get("Content-Encoding") == "gzip":
                import gzip
                raw = gzip.decompress(raw)
            return response.headers, raw

    def test_stream_lines_equal_the_plain_response(self):
        for gzip_ok in (False, True):
            headers, raw = self.fetch("/api/workspace/grep", {"q": "stream-token", "stream": 1}, gzip_ok)
            self.assertIn("application/x-ndjson", headers["Content-Type"])
            events = [json.loads(line) for line in raw.decode("utf-8").splitlines() if line]
            self.assertEqual(events[-1]["type"], "done")
            self.assertFalse(events[-1]["cancelled"])
            _, plain = self.fetch("/api/workspace/grep", {"q": "stream-token"})
            plain = json.loads(plain)
            self.assertEqual([e["file"] for e in events[:-1]], plain["files"])
            self.assertEqual(events[-1]["total"], plain["total"])
            self.assertEqual(len(plain["files"]), 5)
        headers, raw = self.fetch("/api/search/nodes", {"q": "the", "stream": 1})
        events = [json.loads(line) for line in raw.decode("utf-8").splitlines() if line]
        _, plain = self.fetch("/api/search/nodes", {"q": "the"})
        self.assertEqual([e["node"] for e in events[:-1]], json.loads(plain)["nodes"])
        self.assertTrue(all(e["type"] == "node" for e in events[:-1]))

    def test_stream_validation_errors_are_plain_json(self):
        for path in ("/api/workspace/grep", "/api/search/nodes"):
            with self.assertRaises(urllib.error.HTTPError) as caught:
                self.fetch(path, {"q": "(", "regex": 1, "stream": 1})
            self.assertEqual(caught.exception.code, 400)
            self.assertIn("invalid regular expression", json.loads(caught.exception.read())["error"]["message"])

    def test_newer_request_with_the_same_sid_cancels_the_older(self):
        from arkstudio import server as server_mod
        started = []
        original = search.iter_search_files

        def slow(*args, cancelled=None, **kwargs):
            started.append(1)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and not cancelled():
                time.sleep(0.01)
            yield {"done": True, "query": "", "total": 0, "visited": 0, "skipped": 0, "truncated": True,
                   "timed_out": False, "backend": "python", "elapsed_ms": 0}

        import threading
        results = {}
        with mock.patch.object(search, "iter_search_files", slow):
            first = threading.Thread(target=lambda: results.setdefault(
                "first", self.fetch("/api/workspace/grep", {"q": "x", "stream": 1, "sid": "panel:files"})))
            first.start()
            for _ in range(200):
                if started:
                    break
                time.sleep(0.01)
            began = time.monotonic()
            with mock.patch.object(search, "iter_search_files", original):
                self.fetch("/api/workspace/grep", {"q": "stream-token", "stream": 1, "sid": "panel:files"})
            first.join(10)
        self.assertLess(time.monotonic() - began, 5)
        done = json.loads(results["first"][1].decode().splitlines()[-1])
        self.assertTrue(done["cancelled"])
        self.assertNotIn("panel:files", server_mod._SEARCH_RUNS)


    def test_client_disconnect_stops_the_scan(self):
        import socket
        seen = {}

        def slow(*args, cancelled=None, **kwargs):
            seen["started"] = time.monotonic()
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if cancelled():
                    seen["cancelled"] = time.monotonic()
                    break
                time.sleep(0.01)
            yield {"done": True, "query": "", "total": 0, "visited": 0, "skipped": 0, "truncated": True,
                   "timed_out": False, "backend": "python", "elapsed_ms": 0}

        with mock.patch.object(search, "iter_search_files", slow):
            port = self.httpd.server_address[1]
            with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
                sock.sendall(b"GET /api/workspace/grep?q=x&stream=1 HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n" % port)
                self.assertIn(b"200", sock.recv(4096))
            for _ in range(300):
                if "cancelled" in seen:
                    break
                time.sleep(0.01)
        self.assertIn("cancelled", seen, "the server noticed the closed connection")
        self.assertLess(seen["cancelled"] - seen["started"], 3)

if __name__ == "__main__":
    unittest.main()
