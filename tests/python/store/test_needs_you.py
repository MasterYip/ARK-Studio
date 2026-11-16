"""The Home page "Needs you" list: review units and open decisions."""
import tempfile
import time
import unittest
from pathlib import Path

from arkstudio.store import markdown_excerpt
from tests.python.store.test_store import make_store


NL = chr(10)


class NeedsYouTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def make(self, title, type="task", status="open", body="body text"):
        return self.store.create_unit({"title": title, "type": type, "status": status, "body": body})

    def uids(self):
        return [item["uid"] for item in self.store.needs_you()]

    def test_comment_count_and_plain_excerpt(self):
        body = NL.join(["## Result", "**0.82** AUC, see [fig](assets/a.png)", "",
                        "**User comment (2026-11-04):** check seeds", "",
                        "**Sent back by user (2026-11-03):** rerun", "",
                        "**Agent reply (2026-11-04):** rerunning"])
        # Since db v4 comments are rows (tests/python/store/test_comments.py).  A legacy
        # note line an older agent still writes is not a comment, but it stays
        # out of the excerpt.
        unit = self.make("Waiting", status="review", body=body)
        self.store.add_comment(unit["uid"], "a real comment row")
        item = next(i for i in self.store.needs_you() if i["uid"] == unit["uid"])
        self.assertEqual((1, 1), (item["comments"], item["comments_open"]))
        self.assertNotIn("**", item["excerpt_plain"])
        self.assertNotIn("](", item["excerpt_plain"])
        self.assertTrue(item["excerpt_plain"].startswith("Result 0.82 AUC, see fig"))
        for note in ("check seeds", "rerun", "rerunning", "User comment"):
            self.assertNotIn(note, item["excerpt_plain"])

    def test_selection(self):
        review_task = self.make("Finished task", "task", "review")
        review_story = self.make("Finished story", "story", "review")
        open_decision = self.make("Pick an optimiser", "decision", "open")
        done_decision = self.make("Already decided", "decision", "done")
        archived_review = self.make("Archived review", "task", "review")
        self.store.archive_unit(archived_review["uid"])
        archived_decision = self.make("Archived decision", "decision", "open")
        self.store.archive_unit(archived_decision["uid"])
        active_task = self.make("Active task", "task", "active")
        done_task = self.make("Done task", "task", "done")

        uids = self.uids()
        self.assertIn(review_task["uid"], uids)
        self.assertIn(review_story["uid"], uids)
        self.assertIn(open_decision["uid"], uids)
        for unit in (done_decision, archived_review, archived_decision, active_task, done_task):
            self.assertNotIn(unit["uid"], uids)
        self.assertEqual(len(uids), 3)
        # Decisions come first.
        self.assertEqual(uids[0], open_decision["uid"])
        kinds = {item["uid"]: item["kind"] for item in self.store.needs_you()}
        self.assertEqual(kinds[open_decision["uid"]], "decision")
        self.assertEqual(kinds[review_task["uid"]], "review")

    def test_item_shape_and_payload(self):
        unit = self.make("Pick", "decision", "open",
                         body="## Question\n\nWhich LR?\n\n## Options\n\n- 1e-3\n- 3e-4\n")
        payload = self.store.project_payload()
        self.assertIn("needs_you", payload)
        item = payload["needs_you"][0]
        for key in ("uid", "type", "title", "status", "updated_at", "excerpt", "kind", "media"):
            self.assertIn(key, item)
        self.assertEqual(item["uid"], unit["uid"])
        self.assertIn("## Question", item["excerpt"])  # Markdown is kept
        self.assertEqual(item["media"], [])

    def test_accept_and_decide_leave_the_list(self):
        review = self.make("Finished", "task", "review")
        decision = self.make("Pick", "decision", "open")
        self.store.update_unit(review["uid"], {"status": "done"})
        self.store.update_unit(decision["uid"], {"status": "done"})
        self.assertEqual(self.uids(), [])

    def test_own_media_preferred_and_limited(self):
        unit = self.make("With figures", "task", "review")
        for i in range(4):
            self.store.add_media(unit["uid"], f"figs/plot{i}.png", "image", f"plot {i}")
        self.store.add_media(unit["uid"], "notes.pdf", "pdf", "not previewable")
        exp = self.make("Experiment", "experiment", "done")
        self.store.add_media(exp["uid"], "figs/exp.png", "image")
        self.store.create_link(exp["uid"], unit["uid"], "supports")
        self.assertEqual(len(self.store.needs_you()[0]["media"]), 4)
        media = self.store.needs_you(media_limit=3)[0]["media"]
        self.assertEqual(len(media), 3)
        self.assertTrue(all("from" not in m for m in media))
        self.assertTrue(all(m["preview_kind"] == "image" for m in media))
        self.assertEqual(media[0]["caption"], "plot 3")  # newest first
        self.assertTrue(media[0]["thumb_url"].startswith("/api/media/"))

    def test_media_falls_back_to_linked_units_newest_first(self):
        story = self.make("Story awaiting acceptance", "story", "review")
        old = self.make("Old experiment", "experiment", "done")
        self.store.add_media(old["uid"], "figs/old.png", "image", "old fig")
        time.sleep(0.01)
        new = self.make("New experiment", "experiment", "done")
        self.store.add_media(new["uid"], "videos/rollout.mp4", "video", "rollout")
        self.store.create_link(old["uid"], story["uid"], "supports")
        self.store.create_link(new["uid"], story["uid"], "part_of")
        # An agent assignment never contributes previews.
        agent = self.make("Agent", "agent", "active")
        self.store.add_media(agent["uid"], "avatar.png", "image")
        self.store.create_link(story["uid"], agent["uid"], "assigned_to")
        # Archived neighbours are ignored.
        gone = self.make("Archived experiment", "experiment", "done")
        self.store.add_media(gone["uid"], "figs/gone.png", "image")
        self.store.create_link(gone["uid"], story["uid"], "supports")
        self.store.archive_unit(gone["uid"])

        media = self.store.needs_you()[0]["media"]
        self.assertEqual([m["caption"] for m in media], ["rollout", "old fig"])
        self.assertEqual(media[0]["preview_kind"], "video")
        self.assertIsNone(media[0]["thumb_url"])
        self.assertEqual(media[0]["from"]["uid"], new["uid"])
        self.assertEqual(media[1]["from"]["uid"], old["uid"])

    def test_borrowed_previews_are_newest_first_across_neighbours_and_unique(self):
        story = self.make("Reply awaiting review", "story", "review")
        evidence = self.make("Older evidence", "experiment", "done")
        self.store.add_media(evidence["uid"], "videos/old.mp4", "video", "old clip")
        self.store.add_media(evidence["uid"], "videos/shared.mp4", "video", "shared")
        time.sleep(0.01)
        latest = self.make("Latest run", "experiment", "done")
        self.store.add_media(latest["uid"], "videos/new.mp4", "video", "new clip")
        self.store.add_media(latest["uid"], "videos/shared.mp4", "video", "shared again")
        # "supports" ranks above "relates_to", but newer media still come first.
        self.store.create_link(evidence["uid"], story["uid"], "supports")
        self.store.create_link(story["uid"], latest["uid"], "relates_to")
        media = self.store.needs_you()[0]["media"]
        self.assertEqual([m["caption"] for m in media], ["shared again", "new clip", "old clip"])

    def test_own_previews_skip_repeated_files(self):
        unit = self.make("Re-uploaded videos", "artifact", "review")
        for caption in ("V1", "V2", "V1 again"):
            self.store.add_media(unit["uid"], f"videos/{caption.split()[0]}.mp4", "video", caption)
        item = self.store.needs_you()[0]
        self.assertEqual([m["caption"] for m in item["media"]], ["V1 again", "V2"])
        self.assertEqual(item["own_media"], 2)

    def test_markdown_excerpt(self):
        text, cut = markdown_excerpt("First paragraph.\n\nSecond paragraph.")
        self.assertEqual(text, "First paragraph.\n\nSecond paragraph.")
        self.assertFalse(cut)
        text, cut = markdown_excerpt("![fig](a.png)\n\n```python\nx = 1\n```\n\nReal text **bold**.")
        self.assertEqual(text, "Real text **bold**.")
        text, cut = markdown_excerpt("intro\n\n" + "word " * 200)
        self.assertTrue(cut)
        self.assertEqual(text, "intro")
        text, cut = markdown_excerpt("word " * 200)
        self.assertTrue(cut)
        self.assertLessEqual(len(text), 402)


if __name__ == "__main__":
    unittest.main()
