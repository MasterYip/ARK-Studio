import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

from arkstudio import markdown_bundle as mb
from tests.python.store.test_store import make_store


class MarkdownBundleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))
        self.root = self.store.path
        (self.root / "assets" / "media").mkdir(parents=True)
        (self.root / "assets" / "media" / "fig.png").write_bytes(b"\x89PNG fake")
        (self.root / "assets" / "media" / "clip.mp4").write_bytes(b"fake video")
        (self.root / "docs").mkdir()
        (self.root / "docs" / "table.csv").write_text("a,b\n", encoding="utf-8")
        self.evidence = self.store.create_unit({"title": "Evidence [CE1]", "type": "evidence", "body": "E body"})
        self.fig = self.store.add_media(self.evidence["uid"], "assets/media/fig.png", "image", "Figure")
        self.old = self.store.create_unit({"title": "Old draft", "type": "artifact", "body": "old"})
        self.other = self.store.create_unit({"title": "Mentioned", "type": "idea", "body": "m"})
        self.deep = self.store.create_unit({"title": "Deep", "type": "idea", "body": "d"})
        self.store.create_link(self.other["uid"], self.deep["uid"], "supports")
        body = "\n".join([
            f"See [[{self.other['uid']}]] and \\[[{self.other['uid']}]] and `[[{self.other['uid']}]]`.",
            f"![Figure 1](media:{self.fig['id']})",
            "> ![Video V1](media:{clip})",
            '<span style="color:#dc2626">red</span> <img src="/api/media/{fig}/raw" alt="x">',
            "[table](docs/table.csv) [web](https://example.org/a.png) [[MISSING-1]]",
            "```",
            f"[[{self.other['uid']}]] media:{self.fig['id']}",
            "```",
        ])
        self.reply = self.store.create_unit({"title": "Final reply", "type": "artifact", "body": body})
        self.clip = self.store.add_media(self.reply["uid"], "assets/media/clip.mp4", "video", "Clip")
        self.store.update_unit(self.reply["uid"], {"body": body.replace("{clip}", str(self.clip["id"]))
                                                   .replace("{fig}", str(self.fig["id"]))})
        self.extra = self.store.add_media(self.reply["uid"], "assets/media/missing.png", "image", "Gone")
        self.store.create_link(self.reply["uid"], self.evidence["uid"], "supports")
        self.store.create_link(self.reply["uid"], self.old["uid"], "supersedes")

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def build(self, refs, depth=1):
        bundle = mb.MarkdownBundle(self.store, project_path=self.root, project_name="Test",
                                   resolve_media=mb.local_media_resolver(self.store, self.root))
        bundle.collect(refs, depth=depth)
        buf = io.BytesIO()
        manifest = bundle.write_zip(buf, root="b")
        return manifest, zipfile.ZipFile(io.BytesIO(buf.getvalue()))

    def test_bundle_rewrites_links_and_copies_media(self):
        manifest, archive = self.build([self.reply["uid"]])
        names = set(archive.namelist())
        uid = self.reply["uid"]
        self.assertIn(f"b/nodes/{uid}.md", names)
        self.assertIn(f"b/nodes/{self.evidence['uid']}.md", names)
        self.assertIn(f"b/nodes/{self.other['uid']}.md", names)
        self.assertNotIn(f"b/nodes/{self.old['uid']}.md", names, "superseded drafts are not referenced material")
        self.assertNotIn(f"b/nodes/{self.deep['uid']}.md", names, "depth 1 stops after one hop")
        self.assertIn(f"b/media/{self.fig['id']}-fig.png", names)
        self.assertIn(f"b/media/{self.clip['id']}-clip.mp4", names)
        self.assertIn("b/files/docs/table.csv", names)
        note = archive.read(f"b/nodes/{uid}.md").decode("utf-8")
        self.assertTrue(note.startswith("---\nuid: "))
        self.assertIn(f"[{self.other['uid']} · Mentioned]({self.other['uid']}.md)", note)
        self.assertIn(f"\\[[{self.other['uid']}]]", note)
        self.assertIn(f"`[[{self.other['uid']}]]`", note)
        self.assertIn(f"![Figure 1](../media/{self.fig['id']}-fig.png)", note)
        self.assertIn(f'> <video controls preload="metadata" src="../media/{self.clip["id"]}-clip.mp4"', note)
        self.assertIn(f"> *Video V1* ([open](../media/{self.clip['id']}-clip.mp4))", note)
        self.assertIn(f'<img src="../media/{self.fig["id"]}-fig.png"', note)
        self.assertIn('<span style="color:#dc2626">red</span>', note)
        self.assertIn("[table](../files/docs/table.csv)", note)
        self.assertIn("[web](https://example.org/a.png)", note)
        self.assertIn("MISSING-1", note)
        self.assertIn(f"[[{self.other['uid']}]] media:{self.fig['id']}\n```", note, "code fences stay verbatim")
        self.assertIn(f"supports → [{self.evidence['uid']} · Evidence \\[CE1\\]]({self.evidence['uid']}.md)", note)
        self.assertIn(f"supersedes → {self.old['uid']} · Old draft", note)
        self.assertIn("## Attachments", note)
        self.assertIn("Gone (missing)", note)
        self.assertNotIn("Clip (", note.split("## Attachments")[1], "inline media is not repeated")
        evidence = archive.read(f"b/nodes/{self.evidence['uid']}.md").decode("utf-8")
        self.assertIn("bundle_role: referenced", evidence)
        self.assertIn(f"![Figure](../media/{self.fig['id']}-fig.png)", evidence)
        self.assertIn(f"## Linked from\n\n- [{uid} · Final reply]({uid}.md) supports this", evidence)
        readme = archive.read("b/README.md").decode("utf-8")
        self.assertIn(f"(nodes/{uid}.md)", readme)
        self.assertIn("## Missing media", readme)
        data = json.loads(archive.read("b/manifest.json"))
        self.assertEqual(data["nodes"][0]["role"], "selected")
        self.assertEqual(manifest["nodes"][0]["uid"], uid)
        self.assertTrue(any(f.get("error") for f in data["files"]))
        self.assertEqual(archive.getinfo(f"b/media/{self.clip['id']}-clip.mp4").compress_type, zipfile.ZIP_STORED)

    def test_same_file_attached_twice_is_stored_once(self):
        again = self.store.add_media(self.evidence["uid"], "assets/media/clip.mp4", "video", "Same clip")
        _manifest, archive = self.build([self.reply["uid"]])
        clips = [n for n in archive.namelist() if n.endswith("clip.mp4")]
        self.assertEqual(clips, [f"b/media/{self.clip['id']}-clip.mp4"])
        evidence = archive.read(f"b/nodes/{self.evidence['uid']}.md").decode("utf-8")
        self.assertIn(f'src="../media/{self.clip["id"]}-clip.mp4"', evidence)
        self.assertNotIn(f"{again['id']}-clip.mp4", evidence)

    def test_depth_zero_and_two(self):
        _manifest, archive = self.build([self.reply["uid"]], depth=0)
        notes = [n for n in archive.namelist() if n.startswith("b/nodes/")]
        self.assertEqual(notes, [f"b/nodes/{self.reply['uid']}.md"])
        _manifest, archive = self.build([self.reply["uid"]], depth=2)
        self.assertIn(f"b/nodes/{self.deep['uid']}.md", archive.namelist())

    def test_rejects_bad_refs(self):
        with self.assertRaises(mb.BundleError):
            mb.parse_refs("F-1,../etc")
        with self.assertRaises(mb.BundleError):
            self.build(["NOPE-9"])
        with self.assertRaises(mb.BundleError):
            self.build([])

    def test_media_outside_project_is_not_copied(self):
        outside = Path(self.tmp.name) / "secret.png"
        outside.write_bytes(b"secret")
        media = self.store.add_media(self.other["uid"], str(outside), "image", "Secret")
        _manifest, archive = self.build([self.other["uid"]], depth=0)
        self.assertFalse(any(n.endswith("secret.png") for n in archive.namelist()))
        self.assertIn(f"media:{media['id']}", archive.read("b/README.md").decode("utf-8"))


if __name__ == "__main__":
    unittest.main()
