"""Disposable ARK Studio tutorial project."""
from __future__ import annotations

import json
import re
import shutil
import tempfile
import unittest
from pathlib import Path

from arkstudio import demo, demo_content, tags
from arkstudio.projects import ProjectManager
from arkstudio.store import Store

FENCE = re.compile(r"^```([A-Za-z0-9_-]*)[ \t]*\n(.*?)^```[ \t]*$", re.MULTILINE | re.DOTALL)
CHAPTER_KEYS = ["start", "home", "research", "map", "execute", "output", "editor",
                "agents", "settings"]


def fences(body: str, lang: str) -> list[str]:
    return [code for found, code in FENCE.findall(body) if found.lower() == lang]


class DemoProjectTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Build one template for the read-only content checks.
        cls.shared = tempfile.TemporaryDirectory()
        cls.template = demo.ensure_template(cls.shared.name)
        manifest = json.loads((cls.template / "project.json").read_text(encoding="utf-8"))
        store = Store(cls.template, manifest)
        try:
            cls.units = [store.get_unit(item["uid"])
                         for item in store.list_units({"limit": 500})["items"]]
            cls.links = [dict(row) for row in store.con.execute(
                "SELECT l.relation, a.uid AS src, b.uid AS dst FROM unit_links l "
                "JOIN units a ON a.id = l.source_id JOIN units b ON b.id = l.target_id")]
        finally:
            store.close()
        cls.by_title = {unit["title"]: unit for unit in cls.units}
        cls.chapters = {item["key"]: cls.by_title[item["title"]]
                        for item in demo_content.CHAPTERS}

    @classmethod
    def tearDownClass(cls):
        cls.shared.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def _store(self, path: Path) -> Store:
        manifest = json.loads((path / "project.json").read_text(encoding="utf-8"))
        return Store(path, manifest)

    def _titles(self, path: Path) -> list[str]:
        store = self._store(path)
        try:
            return [unit["title"] for unit in store.list_units({"limit": 500})["items"]]
        finally:
            store.close()

    # ------------------------------------------------------------- content
    def test_every_chapter_exists_and_is_linked_from_start_here(self):
        self.assertEqual(sorted(self.chapters), sorted(CHAPTER_KEYS))
        start = self.chapters["start"]
        self.assertIn("Start here", start["title"])
        linked = {link["src"] for link in self.links if link["dst"] == start["uid"]}
        linked |= {link["dst"] for link in self.links if link["src"] == start["uid"]}
        for key in CHAPTER_KEYS[1:]:
            chapter = self.chapters[key]
            self.assertIn(chapter["uid"], linked, key)
            # Start here names every chapter by UID, so a reader can jump to it.
            self.assertIn(chapter["uid"], start["body"], key)

    def test_chapters_follow_the_house_style(self):
        for key, chapter in self.chapters.items():
            body = chapter["body"]
            self.assertEqual(chapter["type"], "story", key)
            self.assertIn("tutorial:chapter", chapter["tags"], key)
            # A plain one-line lead (it is the excerpt in lists), then the
            # "what this is for" paragraph.
            lead = body.split("\n\n", 1)[0]
            self.assertNotRegex(lead, r"[#*`\[]", key)
            self.assertRegex(body, r"\n## What .+ (is )?for\n\n\S", key)
            steps = re.search(r"## Try it\n\n((?:\d+\. .*\n(?:   .*\n)*)+)", body)
            self.assertIsNotNone(steps, f"{key}: missing numbered Try it steps")
            count = len(re.findall(r"^\d+\. ", steps.group(1), re.MULTILINE))
            self.assertGreaterEqual(count, 3, key)
            self.assertLessEqual(count, 6, key)
            if key != "start":
                self.assertRegex(body, r"## Tips", key)
            self.assertIn("**", body, f"{key}: UI labels should be bold")

    def test_every_chapter_hub_links_to_its_examples(self):
        uid_of = {item["key"]: self.by_title[item["title"]]["uid"]
                  for item in demo_content.EXAMPLES}
        for item in demo_content.EXAMPLES:
            hub = self.chapters[item["chapter"]]["uid"]
            self.assertTrue(any(link["src"] == uid_of[item["key"]] and link["dst"] == hub
                                for link in self.links), item["key"])
        for key in CHAPTER_KEYS[1:]:
            self.assertTrue(any(item["chapter"] == key for item in demo_content.EXAMPLES), key)

    def test_research_chain_and_execute_board_use_real_ark_data(self):
        by_type: dict[str, list[dict]] = {}
        for unit in self.units:
            by_type.setdefault(unit["type"], []).append(unit)
        for kind in ("idea", "question", "claim", "evidence", "experiment", "method",
                     "related_work", "source", "artifact", "production", "outreach",
                     "decision", "agent"):
            self.assertIn(kind, by_type, kind)
        statuses = {unit["status"] for unit in by_type["task"]}
        self.assertTrue({"inbox", "open", "active", "blocked", "review", "done"} <= statuses)
        relations = {link["relation"] for link in self.links}
        for relation in ("derived_from", "targets", "supports", "produces", "validates",
                         "assigned_to", "blocks", "uses", "cites", "part_of"):
            self.assertIn(relation, relations)
        channels = {tag for unit in by_type["outreach"] for tag in unit["tags"]
                    if tag.startswith("channel:")}
        self.assertGreaterEqual(len(channels), 3)
        style = [unit for unit in by_type["production"] if "role:style" in unit["tags"]][0]
        self.assertGreaterEqual(sum(tag.startswith("color:#") for tag in style["tags"]), 6)

    def test_task_body_is_a_goal_manifest_from_the_agent_team_templates(self):
        task = self.by_title["Run the 3-seed warm-up comparison"]
        self.assertEqual(task["uid"], "T-0001")
        self.assertTrue(task["body"].startswith("# Goal Manifest"))
        for heading in ("## Objective", "## Acceptance", "## Gates (cheapest first)",
                        "## Verification", "## Decision log"):
            self.assertIn(heading, task["body"])
        urls = [media["url"] for media in task["media"]]
        self.assertIn("assets/tutorial/execute/inspection_report.md", urls)

    def test_gpu_example_documents_a_claim_but_creates_no_job(self):
        unit = next(u for u in self.units if u["title"].startswith("GPU queue:"))
        payload = json.loads(fences(unit["body"], "json")[0])
        self.assertEqual(payload["gpu_memory_mb"], 20000)
        self.assertFalse((self.template / "gpu").exists())
        self.assertFalse(any(path.name.startswith("gpu-jobs") for path in self.template.rglob("*")))

    def test_mermaid_blocks_are_non_empty_and_cover_the_key_diagrams(self):
        blocks = [code for unit in self.units for code in fences(unit["body"], "mermaid")]
        self.assertGreaterEqual(len(blocks), 4)
        for code in blocks:
            lines = [line.strip() for line in code.splitlines() if line.strip()]
            self.assertGreaterEqual(len(lines), 3, code)
            self.assertRegex(lines[0], r"^(flowchart|graph|stateDiagram(-v2)?|sequenceDiagram)\b")
            self.assertTrue(any("-->" in line or "->>" in line for line in lines[1:]), code)
        joined = "\n".join(blocks)
        for marker in ("results feed the next idea", "review --> done", "proposed --> confirmed",
                       "Reserve 20 GB"):
            self.assertIn(marker, joined)

    def test_html_block_is_self_contained(self):
        blocks = [code for unit in self.units for code in fences(unit["body"], "html")]
        self.assertGreaterEqual(len(blocks), 1)
        for code in blocks:
            self.assertIn("<svg", code)
            self.assertIn("<script>", code)
            lowered = code.lower()
            for forbidden in ("http", "fetch", "xmlhttprequest", "/api/", "import(",
                              "websocket", "eventsource"):
                self.assertNotIn(forbidden, lowered)

    def test_media_exist_in_the_template_and_stay_inside_assets(self):
        assets = (self.template / "assets").resolve()
        seen = 0
        for unit in self.units:
            for media in unit["media"]:
                seen += 1
                url = media["url"]
                self.assertTrue(url.startswith("assets/"), url)
                path = (self.template / url).resolve()
                self.assertTrue(path.is_relative_to(assets), url)
                self.assertTrue(path.is_file(), url)
                self.assertTrue(media["caption"], url)
            for raw in re.findall(r"\]\((assets/[^)\s]+)\)", unit["body"]):
                path = (self.template / raw).resolve()
                self.assertTrue(path.is_relative_to(assets), raw)
                self.assertTrue(path.is_file(), raw)
        self.assertGreaterEqual(seen, 25)
        embeds = [ref for unit in self.units
                  for ref in re.findall(r"!\[[^\]]*\]\((assets/[^)\s]+)\)", unit["body"])]
        self.assertGreaterEqual(len(embeds), 12)
        self.assertTrue(any(ref.endswith(".mp4") for ref in embeds))
        kinds = {media["kind"] for unit in self.units for media in unit["media"]}
        self.assertTrue({"figure", "data", "document", "pdf", "log"} <= kinds)

    def test_output_has_a_colour_board_theme_and_a_verified_figure(self):
        from arkstudio.skills import design
        output = self.template / "assets" / "tutorial" / "output"
        theme = json.loads((output / "theme.json").read_text(encoding="utf-8"))
        self.assertEqual(theme, design.example_theme())
        board = (output / "colour-board.svg").read_text(encoding="utf-8")
        for role in theme["color"]["roles"].values():
            self.assertIn(role["value"], board)
        for name in ("colour-board.svg", "poster-cover.svg", "fig-main-result.svg"):
            report = design.verify_artifact(output / name)
            self.assertTrue(report["ok"], (name, report["errors"]))
        figure = self.by_title["Figure 1: warm-up removes early spikes (checked)"]
        self.assertIn("**passes**", figure["body"])

    def test_one_status_rule_and_relation_vocabulary_hold(self):
        for unit in self.units:
            reserved = {ns: [t for t in unit["tags"] if t.startswith(ns + ":")]
                        for ns in tags.NAMESPACES}
            for ns, values in reserved.items():
                self.assertEqual(len(values), 1, (unit["uid"], ns, values))
            self.assertIn(unit["type"], tags.KNOWN_TYPES)
            self.assertIn(unit["status"], tags.KNOWN_STATUSES)
            self.assertNotIn("{{", unit["body"], unit["uid"])
        for link in self.links:
            self.assertIn(link["relation"], tags.RELATIONS)
            self.assertNotEqual(link["relation"], "unset")
        for _a, _b, relation, _note in demo_content.LINKS:
            self.assertIn(relation, tags.RELATIONS)

    def test_committed_assets_match_the_build_script_and_stay_small(self):
        import subprocess
        import sys
        root = Path(__file__).resolve().parents[3]
        result = subprocess.run([sys.executable, "-B", str(root / "scripts" / "dev" / "build_demo_assets.py"),
                                 "--check"], capture_output=True, text=True, cwd=str(root))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        total = sum(path.stat().st_size for path in demo.ASSET_SOURCE.rglob("*") if path.is_file())
        self.assertLess(total, 3 * 1024 * 1024)

    def test_unwrap_joins_prose_but_keeps_blocks(self):
        source = ("A short\nparagraph.\n\n1. Step one\n   continues.\n2. Step two\n\n"
                  "```mermaid\nflowchart LR\n  A --> B\n```\n\n| a | b |\n|---|---|\n\n"
                  "$$\nx = 1\n$$\n\n> quoted\n> more\n")
        self.assertEqual(demo.unwrap(source), (
            "A short paragraph.\n\n1. Step one continues.\n2. Step two\n\n"
            "```mermaid\nflowchart LR\n  A --> B\n```\n\n| a | b |\n|---|---|\n\n"
            "$$\nx = 1\n$$\n\n> quoted more\n"))
        # Every stored prose paragraph is one line: no line both follows and
        # precedes plain text outside fences.
        for unit in self.units:
            body = FENCE.sub("", unit["body"])
            for para in body.split("\n\n"):
                lines = [line for line in para.split("\n") if line.strip()]
                if lines and all(not re.match(r"^\s*([-*|>#!$]|\d+[.)])", line) for line in lines):
                    self.assertEqual(len(lines), 1, (unit["uid"], para[:80]))

    def test_note_type_is_not_used(self):
        self.assertFalse(any(unit["type"] == "note" for unit in self.units))

    # ------------------------------------------------------- lifecycle
    def test_edits_are_discarded_on_reset(self):
        path = demo.ensure_demo(self.root, "alice")
        expected = len(self._titles(path))
        store = self._store(path)
        try:
            store.create_unit({"title": "temporary edit", "type": "idea"})
        finally:
            store.close()
        fresh = demo.ensure_demo(self.root, "alice", reset=True)
        titles = self._titles(fresh)
        self.assertNotIn("temporary edit", titles)
        self.assertEqual(len(titles), expected)

    def test_demo_copies_are_isolated_per_user(self):
        alice = demo.ensure_demo(self.root, "alice")
        bob = demo.ensure_demo(self.root, "bob")
        self.assertNotEqual(alice, bob)
        store = self._store(alice)
        try:
            store.create_unit({"title": "alice only", "type": "idea"})
        finally:
            store.close()
        self.assertNotIn("alice only", self._titles(bob))

    def test_demo_folders_are_not_normal_projects(self):
        demo.ensure_demo(self.root, "alice")
        manager = ProjectManager(self.root, seed_demo=False)
        names = [item["folder"] for item in manager.list_projects()]
        self.assertFalse(any(name.startswith(".demo") for name in names))

    # ------------------------------------------------------- versioning
    def _age(self, path: Path, version) -> None:
        manifest_path = path / "project.json"
        data = json.loads(manifest_path.read_text(encoding="utf-8"))
        if version is None:
            data.pop("demo_version", None)
        else:
            data["demo_version"] = version
        manifest_path.write_text(json.dumps(data), encoding="utf-8")

    def test_template_and_copies_carry_the_version(self):
        path = demo.ensure_demo(self.root, "alice")
        self.assertEqual(demo.folder_version(demo.template_path(self.root)), demo.DEMO_VERSION)
        self.assertEqual(demo.folder_version(path), demo.DEMO_VERSION)
        self.assertTrue(demo.is_current(path))
        self.assertIsNone(demo.folder_version(self.root / "missing"))

    def test_current_template_is_cached_not_rebuilt(self):
        template = demo.ensure_template(self.root)
        marker = template / "assets" / "marker.txt"
        marker.write_text("kept", encoding="utf-8")
        self.assertEqual(demo.ensure_template(self.root), template)
        self.assertTrue(marker.is_file())

    def test_stale_template_is_rebuilt(self):
        for old in (demo.DEMO_VERSION - 1, None, "2"):
            template = demo.ensure_template(self.root)
            (template / "assets" / "marker.txt").write_text("old", encoding="utf-8")
            self._age(template, old)
            rebuilt = demo.ensure_template(self.root)
            self.assertEqual(demo.folder_version(rebuilt), demo.DEMO_VERSION, old)
            self.assertFalse((rebuilt / "assets" / "marker.txt").exists(), old)
            self.assertTrue((rebuilt / "assets" / "tutorial" / "output" / "colour-board.svg").is_file())

    def test_legacy_template_without_version_is_replaced(self):
        # A template from before versioning: 13 old units, no demo_version.
        template = demo.template_path(self.root)
        template.mkdir(parents=True)
        manifest = {"format": "ark-studio-project", "version": 1, "id": demo.DEMO_ID,
                    "name": demo.DEMO_NAME, "demo": True}
        (template / "project.json").write_text(json.dumps(manifest), encoding="utf-8")
        store = Store(template, manifest)
        try:
            store.create_unit({"title": "Old tour node", "type": "idea"})
        finally:
            store.close()
        path = demo.ensure_demo(self.root, "alice")
        titles = self._titles(path)
        self.assertNotIn("Old tour node", titles)
        self.assertTrue(any(title.startswith("Start here") for title in titles))

    def test_stale_user_copy_refreshes_on_next_open(self):
        path = demo.ensure_demo(self.root, "alice")
        store = self._store(path)
        try:
            store.create_unit({"title": "edit in an old copy", "type": "idea"})
        finally:
            store.close()
        # Same version: the copy (and the edit) survive a normal open.
        self.assertIn("edit in an old copy", self._titles(demo.ensure_demo(self.root, "alice")))
        self._age(path, demo.DEMO_VERSION - 1)
        fresh = demo.ensure_demo(self.root, "alice")
        self.assertEqual(demo.folder_version(fresh), demo.DEMO_VERSION)
        self.assertNotIn("edit in an old copy", self._titles(fresh))

    def test_bumping_the_version_rebuilds_template_and_copies(self):
        path = demo.ensure_demo(self.root, "alice")
        original = demo.DEMO_VERSION
        try:
            demo.DEMO_VERSION = original + 1
            template = demo.ensure_template(self.root)
            self.assertEqual(demo.folder_version(template), original + 1)
            self.assertFalse(demo.is_current(path))
            fresh = demo.ensure_demo(self.root, "alice")
            self.assertEqual(demo.folder_version(fresh), original + 1)
        finally:
            demo.DEMO_VERSION = original
        # Going back is also a version change: everything follows the code.
        self.assertEqual(demo.folder_version(demo.ensure_demo(self.root, "alice")), original)

    def test_missing_asset_folder_still_builds(self):
        original = demo.ASSET_SOURCE
        try:
            demo.ASSET_SOURCE = self.root / "no-assets"
            template = demo.ensure_template(self.root / "projects")
        finally:
            demo.ASSET_SOURCE = original
        store = self._store(template)
        try:
            items = store.list_units({"limit": 500})["items"]
            self.assertTrue(any(item["title"].startswith("Start here") for item in items))
            self.assertTrue(all(not store.get_unit(item["uid"])["media"] for item in items))
            task = store.get_unit("T-0001")
            self.assertTrue(task["body"].startswith("# Goal Manifest"))
        finally:
            store.close()
        shutil.rmtree(self.root / "projects", ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
