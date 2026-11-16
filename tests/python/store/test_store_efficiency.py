"""Regression checks for bulk reads against the previous per-node semantics."""
import random
import re
import tempfile
import unittest
from pathlib import Path

from arkstudio import store as store_module
from arkstudio.store import excerpt
from tests.python.store.test_store import make_store


def reference_assignees(units, links):
    own = {uid: name for uid, name in units if name}
    parents = {}
    for child, parent in links:
        parents.setdefault(child, []).append(parent)
    index = {}
    for uid, _name in units:
        if uid in own:
            index[uid] = (own[uid], None)
            continue
        seen, frontier, found = {uid}, [uid], None
        while frontier and found is None:
            following = []
            for node in frontier:
                for parent in parents.get(node, ()):
                    if parent in seen:
                        continue
                    seen.add(parent)
                    if parent in own:
                        found = parent
                        break
                    following.append(parent)
                if found is not None:
                    break
            frontier = following
        index[uid] = (own[found], found) if found is not None else (None, None)
    return index


def reference_excerpt(body, limit):
    lines, fenced = [], False
    for line in str(body or "").replace("\r\n", "\n").split("\n"):
        if store_module._MD_FENCE.match(line):
            fenced = not fenced
            continue
        if fenced or line.strip() in {"$$", "---", "***"}:
            continue
        line = store_module._MD_IMAGE.sub(lambda match: match.group(1), line)
        line = store_module._MD_LINK.sub(lambda match: match.group(1), line)
        line = store_module._MD_PREFIX.sub("", line)
        line = store_module._MD_INLINE.sub("", line).replace("|", " ")
        if line.strip():
            lines.append(line.strip())
    text = re.sub(r"\s+", " ", " ".join(lines)).strip()
    return text[:limit].rstrip() + "…" if len(text) > limit else text


class StoreEfficiencyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def test_linear_assignees_match_breadth_first_reference_with_cycles_and_ties(self):
        rng = random.Random(3801)
        for trial in range(12):
            with self.subTest(trial=trial), self.store.con:
                self.store.con.execute("DELETE FROM units")
                units = [(uid, rng.choice([None, None, None, "alice", "bob"])) for uid in range(1, 61)]
                self.store.con.executemany(
                    "INSERT INTO units(id,uid,type,title,assignee,created_at,updated_at) VALUES(?,?, 'task',?,?,?,?)",
                    [(uid, f"T-{uid}", f"Node {uid}", name, "2025-01-01", "2025-01-01") for uid, name in units],
                )
                links = list(dict.fromkeys((rng.randrange(1, 61), rng.randrange(1, 61)) for _ in range(180)))
                self.store.con.executemany(
                    "INSERT INTO unit_links(source_id,target_id,relation,created_at) VALUES(?,?,'part_of','2025-01-01')",
                    links,
                )
            self.assertEqual(self.store._assignee_index(), reference_assignees(units, links))

    def test_prefix_excerpt_matches_full_scan_with_markdown_and_whitespace(self):
        rng = random.Random(752)
        vocabulary = ["# A heading", "A  **bold**\t*value*.", "  - [paper](https://example.test)",
                      "| A | B |", "", " ", "```python", "print(1)", "~~~", "---", "***",
                      "> Quoted  中文🙂", "![a caption](plot.png)", "$$", "space\u2028separator"]
        bodies = ["\n".join(rng.choices(vocabulary, k=70)) for _ in range(80)]
        bodies.extend(["\n", "only one line", "# Empty\n---\n", "same prefix\n" * 1000])
        for body in bodies:
            for limit in (-1, 0, 1, 12, 240, 320):
                with self.subTest(limit=limit, body=body[:20]):
                    self.assertEqual(excerpt(body, limit), reference_excerpt(body, limit))

    def test_batched_review_media_matches_per_node_path(self):
        reviews = [self.store.create_unit({"title": f"Review {i}", "status": "review"}) for i in range(9)]
        sources = [self.store.create_unit({"title": f"Evidence {i}"}) for i in range(12)]
        for i, source in enumerate(sources):
            for number in range(i % 4 + 1):
                self.store.add_media(source["id"], f"assets/shared-{number % 3}.png", "image", f"From {i}-{number}")
            self.store.add_media(source["id"], "paper.pdf", "pdf")
        self.store.archive_unit(sources[-1]["id"])
        for i, review in enumerate(reviews):
            for j, source in enumerate(sources):
                self.store.create_link(source["id"], review["id"], "supports" if j % 3 else "part_of")
                self.store.create_link(review["id"], source["id"], "produces" if j % 4 else "assigned_to")
            if i % 3 == 0:
                self.store.add_media(review["id"], "own.mp4", "video", f"Own {i}")
                self.store.add_media(review["id"], "own.mp4", "video", f"Own {i} duplicate")
            elif i % 3 == 1:
                self.store.add_media(review["id"], "not-previewable.pdf", "pdf")
        ids = [row["id"] for row in reviews]
        for limit in (0, 1, 2, 40):
            with self.subTest(limit=limit):
                expected = {uid: self.store._preview_media(uid, limit, with_own_count=True) for uid in ids}
                self.assertEqual(self.store._preview_media_batch(ids, limit), expected)

    def test_review_media_query_count_scales_by_batches_not_nodes(self):
        with self.store.con:
            self.store.con.executemany(
                "INSERT INTO units(uid,type,title,status,created_at,updated_at) VALUES(?,'task',?,'review',?,?)",
                [(f"T-{i}", f"Review {i}", "2025-01-01", "2025-01-01") for i in range(120)],
            )
        self.store._assignee_index()
        statements = []
        self.store.con.set_trace_callback(statements.append)
        try:
            items = self.store.needs_you()
        finally:
            self.store.con.set_trace_callback(None)
        self.assertEqual(len(items), 120)
        self.assertLessEqual(len(statements), 7)


if __name__ == "__main__":
    unittest.main()
