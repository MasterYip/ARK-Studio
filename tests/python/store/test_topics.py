"""Topic units: a node that holds a curated subgraph of other nodes."""
import json
import sqlite3
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

from arkstudio import db
from arkstudio.server import App, serve
from arkstudio.store import MAX_TOPIC_MEMBERS, StoreError

from tests.python.store.test_store import make_store


class TopicStoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))
        self.a = self.make("Motivation", "story")
        self.b = self.make("Main claim", "claim")
        self.c = self.make("Ablation", "experiment")
        self.outside = self.make("Unrelated idea", "idea")
        self.store.create_link(self.a["id"], self.b["id"], "supports")
        self.store.create_link(self.c["id"], self.b["id"], "validates")
        self.store.create_link(self.c["id"], self.outside["id"], "relates_to")

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def make(self, title, type_name="idea", **extra):
        return self.store.create_unit({"title": title, "type": type_name, **extra})

    def test_topic_is_a_unit_with_members_and_inner_links(self):
        topic = self.make("Paper narrative", "topic",
                          members=[self.a["uid"], {"unit": self.b["id"], "caption": "the bet"}, self.c["id"]])
        self.assertTrue(topic["uid"].startswith("TP-"))
        self.assertEqual(topic["member_count"], 3)
        data = self.store.topic(topic["uid"])
        self.assertEqual([m["uid"] for m in data["members"]], [self.a["uid"], self.b["uid"], self.c["uid"]])
        self.assertEqual([m["member"]["step"] for m in data["members"]], [1, 2, 3])
        self.assertEqual(data["members"][1]["member"]["caption"], "the bet")
        # Only links between members; the link to the outside idea is not part of the topic.
        self.assertEqual({(e["source"], e["target"]) for e in data["edges"]},
                         {(self.a["id"], self.b["id"]), (self.c["id"], self.b["id"])})
        self.assertEqual(data["archived"], 0)
        # Members know which topics hold them.
        self.assertEqual([t["uid"] for t in self.store.get_unit(self.b["id"])["topics"]], [topic["uid"]])
        self.assertEqual(self.store.get_unit(self.outside["id"])["topics"], [])

    def test_add_is_idempotent_and_validated(self):
        topic = self.make("Story", "topic")
        result = self.store.add_topic_members(topic["id"], [self.a["id"], self.a["id"], self.b["uid"]])
        self.assertEqual(result["added"], [self.a["uid"], self.b["uid"]])
        self.assertEqual(result["skipped"], [self.a["uid"]])
        with self.assertRaises(StoreError):
            self.store.add_topic_members(topic["id"], [topic["id"]])
        with self.assertRaises(StoreError):
            self.store.add_topic_members(self.a["id"], [self.b["id"]])  # not a topic
        with self.assertRaises(StoreError):
            self.store.add_topic_members(topic["id"], [{"unit": self.c["id"], "x": "left"}])
        with self.assertRaises(StoreError):
            self.store.add_topic_members(topic["id"], ["X-9999"])
        self.assertEqual(self.store.topic(topic["id"])["topic"]["member_count"], 2)

    def test_member_cap(self):
        topic = self.make("Big", "topic")
        units = [self.make(f"n{i}", "note")["id"] for i in range(MAX_TOPIC_MEMBERS)]
        self.store.add_topic_members(topic["id"], units)
        with self.assertRaises(StoreError):
            self.store.add_topic_members(topic["id"], [self.a["id"]])

    def test_layout_order_and_caption(self):
        topic = self.make("Process", "topic", members=[self.a["id"], self.b["id"], self.c["id"]])
        activity_before = len(self.store.activity(limit=200, unit_id=topic["id"]))
        rev = self.store.revision()
        result = self.store.design_topic(topic["id"], {"items": [
            {"unit": self.a["id"], "x": 10.123, "y": -20},
            {"unit": self.b["uid"], "x": 300, "y": 0},
        ]})
        self.assertEqual((result["updated"], result["reordered"]), (2, False))
        # Moving nodes is a layout change: no activity entry, but other clients see a new revision.
        self.assertEqual(len(self.store.activity(limit=200, unit_id=topic["id"])), activity_before)
        self.assertNotEqual(self.store.revision(), rev)
        data = self.store.topic(topic["id"])
        self.assertEqual((data["members"][0]["member"]["x"], data["members"][0]["member"]["y"]), (10.12, -20.0))
        self.assertIsNone(data["members"][2]["member"]["x"])

        result = self.store.design_topic(topic["id"], {
            "order": [self.c["uid"], self.a["id"]],
            "items": [{"unit": self.c["id"], "caption": "starts here"}]})
        self.assertTrue(result["reordered"])
        data = self.store.topic(topic["id"])
        self.assertEqual([m["id"] for m in data["members"]], [self.c["id"], self.a["id"], self.b["id"]])
        self.assertEqual(data["members"][0]["member"]["caption"], "starts here")
        self.assertEqual(data["members"][1]["member"]["x"], 10.12)  # order changes keep positions
        last = self.store.activity(limit=1, unit_id=topic["id"])[0]
        self.assertEqual(last["action"], "topic.update")

        with self.assertRaises(StoreError):
            self.store.design_topic(topic["id"], {"items": [{"unit": self.outside["id"], "x": 1}]})

    def test_member_card_width(self):
        """Node-graph card width per member (``w``): set, keep, clear, validate."""
        from arkstudio.store import MAX_CARD_W, MIN_CARD_W
        topic = self.make("Widths", "topic", members=[self.a["id"], self.b["id"]])
        member = lambda i: self.store.topic(topic["id"])["members"][i]["member"]  # noqa: E731
        self.assertIsNone(member(0)["w"])
        self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "x": 5, "y": 6, "w": 321.27}]})
        self.assertEqual((member(0)["x"], member(0)["w"]), (5.0, 321.3))
        # A position-only write keeps the width; a width-only write keeps the position.
        self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "x": 50, "y": 6}]})
        self.assertEqual(member(0)["w"], 321.3)
        self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "w": MAX_CARD_W}]})
        self.assertEqual((member(0)["x"], member(0)["w"]), (50.0, float(MAX_CARD_W)))
        self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "w": None}]})
        self.assertIsNone(member(0)["w"])
        for bad in (MIN_CARD_W - 1, MAX_CARD_W + 1, "wide", True, float("nan")):
            with self.subTest(w=bad), self.assertRaises(StoreError):
                self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "w": bad}]})

    def test_old_database_gains_member_width_column(self):
        path = Path(self.tmp.name) / "old" / "ark.sqlite3"
        path.parent.mkdir()
        con = db.connect(path)
        try:
            # A v11 database: topic_members without the width column.
            con.execute("ALTER TABLE topic_members DROP COLUMN w")
            self.assertNotIn("w", {row[1] for row in con.execute("PRAGMA table_info(topic_members)")})
            db.migrate(con)
            self.assertIn("w", {row[1] for row in con.execute("PRAGMA table_info(topic_members)")})
            db.migrate(con)  # idempotent
        finally:
            con.close()

    def test_revision_changes_for_writes_in_the_same_clock_tick(self):
        # updated_at has one-second resolution; freeze it so every write below
        # lands in the same tick.  Only the change counter can tell them apart.
        topic = self.make("Frozen", "topic", members=[self.a["id"], self.b["id"]])
        with mock.patch.object(db, "utc_now", return_value="2026-01-01T00:00:00+00:00"):
            seen = [self.store.revision()]
            for x in (1, 2):
                self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "x": x, "y": 0}]})
                seen.append(self.store.revision())
            self.store.design_topic(topic["id"], {"items": [{"unit": self.b["id"], "caption": "same tick"}]})
            seen.append(self.store.revision())
            self.assertEqual(len(set(seen)), len(seen), seen)
            # Reads and rejected writes leave it alone.
            self.store.topic(topic["id"])
            with self.assertRaises(StoreError):
                self.store.design_topic(topic["id"], {"items": [{"unit": self.outside["id"], "x": 1}]})
            self.assertEqual(self.store.revision(), seen[-1])
            # A write committed by another connection (another process) counts too.
            other = sqlite3.connect(self.store.path / "ark.sqlite3")
            try:
                with other:
                    other.execute("UPDATE topic_members SET y=5 WHERE topic_id=?", (topic["id"],))
            finally:
                other.close()
            self.assertNotEqual(self.store.revision(), seen[-1])

    def test_archive_hides_member_and_restore_brings_it_back(self):
        topic = self.make("Process", "topic", members=[self.a["id"], self.b["id"]])
        self.store.archive_unit(self.a["id"])
        data = self.store.topic(topic["id"])
        self.assertEqual([m["id"] for m in data["members"]], [self.b["id"]])
        self.assertEqual(data["archived"], 1)
        self.assertEqual(data["topic"]["member_count"], 1)
        with self.assertRaises(StoreError):
            self.store.add_topic_members(topic["id"], [self.a["id"]])
        self.store.restore_unit(self.a["id"])
        self.assertEqual(len(self.store.topic(topic["id"])["members"]), 2)

    def test_remove_member_and_topic_deletion_cascade(self):
        topic = self.make("Process", "topic", members=[self.a["id"], self.b["id"]])
        self.store.remove_topic_member(topic["uid"], self.a["uid"])
        self.assertEqual([m["id"] for m in self.store.topic(topic["id"])["members"]], [self.b["id"]])
        with self.assertRaises(StoreError):
            self.store.remove_topic_member(topic["uid"], self.a["uid"])
        with self.store.lock, self.store.con:
            self.store.con.execute("DELETE FROM units WHERE id=?", (topic["id"],))
        count = self.store.con.execute("SELECT COUNT(*) FROM topic_members").fetchone()[0]
        self.assertEqual(count, 0)

    def test_nested_topics_and_previews(self):
        inner = self.make("Ablations", "topic", members=[self.c["id"]])
        outer = self.make("Paper", "topic", members=[self.a["id"], self.b["id"], inner["id"]])
        listing = self.store.topics()
        self.assertEqual({item["uid"] for item in listing["items"]}, {inner["uid"], outer["uid"]})
        preview = next(item for item in listing["items"] if item["uid"] == outer["uid"])["preview"]
        self.assertEqual([m["uid"] for m in preview["members"]], [self.a["uid"], self.b["uid"], inner["uid"]])
        self.assertEqual(preview["edges"], [[self.a["id"], self.b["id"]]])
        # The nested topic is a member like any other node.
        members = self.store.topic(outer["id"])["members"]
        self.assertEqual(members[2]["type"], "topic")
        self.assertEqual(members[2]["member_count"], 1)

    def test_rule_members_exclude_and_restore(self):
        claim2 = self.make("Second claim", "claim", tags=["topic:control"])
        topic = self.make("Claims", "topic", members=[self.a["id"]],
                          design={"rule": {"types": ["claim"], "page": 3}})
        data = self.store.topic(topic["id"])
        self.assertEqual(data["subgraph"]["rule"], {"types": ["claim"]})  # unknown keys dropped
        self.assertEqual([m["uid"] for m in data["members"]][0], self.a["uid"])  # manual members first
        self.assertEqual({m["uid"] for m in data["members"]}, {self.a["uid"], self.b["uid"], claim2["uid"]})
        self.assertEqual({m["uid"]: m["member"]["source"] for m in data["members"]}[self.b["uid"]], "rule")
        # A unit created later that matches the rule joins automatically.
        claim3 = self.make("Third claim", "claim")
        self.assertIn(claim3["uid"], [m["uid"] for m in self.store.topic(topic["id"])["members"]])
        self.assertEqual([t["uid"] for t in self.store.get_unit(claim3["id"])["topics"]], [topic["uid"]])

        # Removing a rule match excludes it; restore lets the rule show it again.
        report = self.store.design_topic(topic["id"], {"remove": [claim2["id"]]})
        self.assertEqual(report["removed"], [claim2["uid"]])
        data = self.store.topic(topic["id"])
        self.assertNotIn(claim2["uid"], [m["uid"] for m in data["members"]])
        self.assertEqual([u["uid"] for u in data["excluded"]], [claim2["uid"]])
        self.store.design_topic(topic["id"], {"restore": [claim2["uid"]]})
        self.assertIn(claim2["uid"], [m["uid"] for m in self.store.topic(topic["id"])["members"]])

        # Moving a rule member pins its layout without making it a manual member.
        self.store.design_topic(topic["id"], {"items": [{"unit": self.b["id"], "x": 5, "y": 6}],
                                              "order": [self.b["id"]]})
        data = self.store.topic(topic["id"])
        self.assertEqual(data["members"][0]["uid"], self.b["uid"])
        self.assertEqual((data["members"][0]["member"]["x"], data["members"][0]["member"]["source"]), (5, "rule"))
        # Clearing the rule drops rule-only members, keeps manual ones.
        self.store.design_topic(topic["id"], {"rule": None})
        self.assertEqual([m["uid"] for m in self.store.topic(topic["id"])["members"]], [self.a["uid"]])
        with self.assertRaises(StoreError):
            self.store.design_topic(topic["id"], {"rule": {"types": ["nope"]}})

    def test_link_visibility(self):
        topic = self.make("Evidence chain", "topic", members=[self.a["id"], self.b["id"], self.c["id"]])
        edges = self.store.topic(topic["id"])["edges"]
        supports = next(e for e in edges if e["relation"] == "supports")
        validates = next(e for e in edges if e["relation"] == "validates")
        self.assertFalse(supports["hidden"] or validates["hidden"])
        self.store.design_topic(topic["id"], {"relations": ["supports"]})
        edges = {e["id"]: e for e in self.store.topic(topic["id"])["edges"]}
        self.assertTrue(edges[validates["id"]]["hidden"])
        self.assertFalse(edges[supports["id"]]["hidden"])
        self.store.design_topic(topic["id"], {"relations": None, "hide_links": [supports["id"]]})
        data = self.store.topic(topic["id"])
        edges = {e["id"]: e for e in data["edges"]}
        self.assertTrue(edges[supports["id"]]["hidden"])
        self.assertFalse(edges[validates["id"]]["hidden"])
        self.assertEqual(data["subgraph"]["hidden_links"], [supports["id"]])
        preview = next(i for i in self.store.topics()["items"] if i["id"] == topic["id"])["preview"]
        self.assertEqual(preview["edges"], [[self.c["id"], self.b["id"]]])
        self.store.design_topic(topic["id"], {"show_links": [supports["id"]]})
        self.assertFalse(any(e["hidden"] for e in self.store.topic(topic["id"])["edges"]))
        with self.assertRaises(StoreError):
            self.store.design_topic(topic["id"], {"relations": ["likes"]})

    def test_outline_follows_part_of_until_the_topic_sets_parents(self):
        chapter = self.make("Chapter", "story")
        section = self.make("Section", "story")
        self.store.create_link(section["id"], chapter["id"], "part_of")
        topic = self.make("Paper", "topic", members=[chapter["id"], self.a["id"], section["id"], self.b["id"]])
        data = self.store.topic(topic["id"])
        outline = [(m["uid"], m["member"]["path"], m["member"]["depth"], m["member"]["parent_source"])
                   for m in data["members"]]
        self.assertEqual(outline, [
            (chapter["uid"], "1", 0, None), (section["uid"], "1.1", 1, "link"),
            (self.a["uid"], "2", 0, None), (self.b["uid"], "3", 0, None)])
        self.assertEqual(data["members"][0]["member"]["children"], 1)
        # The topic's own outline wins: indent B under A, lift the section out.
        report = self.store.design_topic(topic["id"], {"items": [
            {"unit": self.b["id"], "parent": self.a["uid"]},
            {"unit": section["id"], "parent": None}]})
        self.assertTrue(report["outline"])
        paths = {m["uid"]: m["member"]["path"] for m in self.store.topic(topic["id"])["members"]}
        self.assertEqual(paths, {chapter["uid"]: "1", self.a["uid"]: "2", self.b["uid"]: "2.1", section["uid"]: "3"})
        # "auto" returns to the part_of link; a cycle is cut, never looped.
        self.store.design_topic(topic["id"], {"items": [
            {"unit": section["id"], "parent": "auto"},
            {"unit": self.a["id"], "parent": self.b["id"]}]})
        members = self.store.topic(topic["id"])["members"]
        self.assertEqual(len(members), 4)
        self.assertEqual({m["uid"]: m["member"]["path"] for m in members}[section["uid"]], "1.1")
        with self.assertRaises(StoreError):
            self.store.design_topic(topic["id"], {"items": [{"unit": self.a["id"], "parent": self.outside["id"]}]})
        self.assertNotIn("body", members[0])
        self.assertIn("body", self.store.topic(topic["id"], full=True)["members"][0])

    def test_rule_members_follow_seq_then_creation(self):
        late = self.make("Late but first", "story", tags=["seq:01"])
        topic = self.make("Stories", "topic", design={"rule": {"types": ["story"]}})
        uids = [m["uid"] for m in self.store.topic(topic["id"])["members"]]
        self.assertEqual(uids, [late["uid"], self.a["uid"]])

    def test_bad_design_on_create_leaves_no_topic(self):
        node = self.store.create_unit({"title": "Kept", "type": "claim"})
        for design in ({"add": ["Q-9999"]}, {"add": [node["uid"]], "items": [{"unit": "C-9999", "parent": None}]},
                       {"order": "C-0001"}, {"rule": {"types": ["no-such-type"]}}):
            before = self.store.list_units({"types": ["topic"], "limit": 50}, include_facets=False)["total"]
            with self.assertRaises(StoreError):
                self.store.create_unit({"type": "topic", "title": "Bad", "design": design})
            after = self.store.list_units({"types": ["topic"], "limit": 50}, include_facets=False)["total"]
            self.assertEqual(before, after, design)
        archived = self.store.create_unit({"title": "Gone", "type": "claim"})
        self.store.archive_unit(archived["uid"])
        with self.assertRaises(StoreError):
            self.store.create_unit({"type": "topic", "title": "Bad", "members": [archived["uid"]]})

    def test_default_topics_are_created_once(self):
        created = self.store.ensure_default_topics()
        self.assertEqual([t["meta"]["default"] for t in created], ["story", "related", "method", "experiment"])
        self.assertEqual(self.store.ensure_default_topics(), [])
        listing = self.store.topics()["items"]
        self.assertEqual([t["default"] for t in listing[:4]], ["story", "related", "method", "experiment"])
        story = self.store.topic(listing[0]["id"])
        self.assertEqual([m["uid"] for m in story["members"]], [self.a["uid"]])
        self.assertIsNone(story["topic"]["assignee"])
        self.store.archive_unit(listing[0]["id"])
        self.assertEqual(self.store.ensure_default_topics(), [])

    def test_old_database_gains_the_table(self):
        path = Path(self.tmp.name) / "old.sqlite3"
        con = sqlite3.connect(path)
        con.executescript(db.SCHEMA.split("-- v6:")[0] + "CREATE TABLE counters(prefix TEXT PRIMARY KEY, n INTEGER);")
        con.close()
        upgraded = db.connect(path)
        try:
            tables = {r[0] for r in upgraded.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            self.assertIn("topic_members", tables)
            self.assertEqual(db.meta_get(upgraded, "schema_version"), str(db.SCHEMA_VERSION))
        finally:
            upgraded.close()


class TopicApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=False)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        if cls.app.project:
            cls.app.project.close()
        cls.tmp.cleanup()

    def request(self, path, *, method="GET", body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def unit(self, title, type_name, **extra):
        status, data = self.request("/api/units", method="POST",
                                    body={"title": title, "type": type_name, **extra})
        self.assertEqual(status, 200, data)
        return data["unit"]

    def test_topic_routes(self):
        a = self.unit("Gap", "story")
        b = self.unit("Insight", "claim")
        status, link = self.request("/api/links", method="POST",
                                    body={"source": a["id"], "target": b["id"], "relation": "inspires"})
        self.assertEqual(status, 200, link)
        topic = self.unit("Narrative", "topic", members=[a["id"]])
        self.assertEqual(topic["member_count"], 1)

        status, data = self.request(f"/api/units/{topic['uid']}/members", method="POST",
                                    body={"members": [b["uid"], a["uid"]]})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["report"]["added"], [b["uid"]])
        self.assertEqual(data["report"]["skipped"], [a["uid"]])
        self.assertEqual(len(data["edges"]), 1)

        status, data = self.request(f"/api/units/{topic['id']}/members", method="PATCH",
                                    body={"items": [{"unit": a["id"], "x": 40, "y": 80}], "order": [b["id"]]})
        self.assertEqual(status, 200, data)
        status, data = self.request(f"/api/units/{topic['id']}/members")
        self.assertEqual(status, 200)
        self.assertEqual([m["uid"] for m in data["members"]], [b["uid"], a["uid"]])
        self.assertEqual(data["members"][1]["member"]["x"], 40)

        status, listing = self.request("/api/topics")
        self.assertEqual(status, 200)
        self.assertIn(topic["uid"], [item["uid"] for item in listing["items"]])

        status, data = self.request(f"/api/units/{topic['id']}/members/{a['uid']}", method="DELETE")
        self.assertEqual(status, 200, data)
        status, detail = self.request(f"/api/units/{a['uid']}")
        self.assertNotIn(topic["uid"], [t["uid"] for t in detail["unit"]["topics"]])  # the default Story topic still shows it

        status, data = self.request(f"/api/units/{a['id']}/members")
        self.assertEqual(status, 400)
        self.assertIn("not a topic", json.dumps(data))


    def test_agent_workflow_from_the_docs(self):
        """The steps in docs/guide/topics.md "Agent workflow", through /api/agent/v1 only."""
        token = self.app.manager.agent_token

        def agent(path, method="GET", body=None):
            data = json.dumps(body).encode() if body is not None else None
            req = urllib.request.Request(self.base + path, data=data, method=method, headers={
                "Content-Type": "application/json", "Authorization": f"Bearer {token}", "X-Agent-Name": "doc-test"})
            try:
                with urllib.request.urlopen(req, timeout=10) as response:
                    return response.status, json.loads(response.read())
            except urllib.error.HTTPError as exc:
                return exc.code, json.loads(exc.read().decode() or "{}")

        def unit(title, type_name):
            status, data = agent("/api/agent/v1/units", "POST", {"title": title, "type": type_name})
            self.assertEqual(status, 200, data)
            return data["unit"]

        question = unit("Does warm-up remove early spikes?", "question")
        experiment = unit("Warm-up vs none, 3 seeds", "experiment")
        claim = unit("Warm-up removes the early spikes", "claim")
        decision = unit("Adopt 500-step warm-up?", "decision")
        side = unit("Unrelated note", "experiment")
        status, link = agent("/api/agent/v1/links", "POST",
                             {"source": experiment["uid"], "target": claim["uid"], "relation": "supports"})
        self.assertEqual(status, 200, link)
        status, other = agent("/api/agent/v1/links", "POST",
                              {"source": side["uid"], "target": claim["uid"], "relation": "relates_to"})
        self.assertEqual(status, 200, other)

        # 1. Create the topic with its first design in one call.
        status, data = agent("/api/agent/v1/units", "POST", {
            "type": "topic", "title": "Warm-up study, step by step", "status": "active",
            "body": "Question, experiment, claim and the open decision.",
            "design": {
                "add": [question["uid"], experiment["uid"], claim["uid"], side["uid"]],
                "items": [{"unit": experiment["uid"], "parent": question["uid"]},
                          {"unit": claim["uid"], "parent": question["uid"]}],
                "order": [question["uid"], experiment["uid"], claim["uid"], side["uid"]],
                "relations": ["supports"],
            },
        })
        self.assertEqual(status, 200, data)
        topic = data["unit"]
        self.assertEqual((topic["type"], topic["uid"][:2], topic["member_count"]), ("topic", "TP", 4))

        # 2. Read it back: outline paths, links and settings.
        status, view = agent(f"/api/agent/v1/units/{topic['uid']}/members")
        self.assertEqual(status, 200, view)
        self.assertEqual([(m["uid"], m["member"]["path"]) for m in view["members"]],
                         [(question["uid"], "1"), (experiment["uid"], "1.1"), (claim["uid"], "1.2"), (side["uid"], "2")])
        shown = {e["relation"]: e["hidden"] for e in view["edges"]}
        self.assertEqual(shown, {"supports": False, "relates_to": True})
        self.assertEqual(view["subgraph"]["relations"], ["supports"])

        # 3. Revise: remove a node, add one, place it, set a rule, hide a link.
        status, data = agent(f"/api/agent/v1/units/{topic['uid']}/members", "POST", {
            "remove": [side["uid"]],
            "add": [{"unit": decision["uid"], "x": 650, "y": 80}],
            "items": [{"unit": question["uid"], "x": 90, "y": 80},
                      {"unit": experiment["uid"], "x": 370, "y": 40},
                      {"unit": claim["uid"], "x": 370, "y": 140}],
            "hide_links": [link["id"]],
        })
        self.assertEqual(status, 200, data)
        report = data["report"]
        self.assertEqual((report["added"], report["removed"]), ([decision["uid"]], [side["uid"]]))
        self.assertEqual([m["uid"] for m in data["members"]][-1], decision["uid"])
        positions = {m["uid"]: (m["member"]["x"], m["member"]["y"]) for m in data["members"]}
        self.assertEqual(positions[experiment["uid"]], (370, 40))
        self.assertIn(link["id"], data["subgraph"]["hidden_links"])
        self.assertTrue(next(e for e in data["edges"] if e["id"] == link["id"])["hidden"])

        status, data = agent(f"/api/agent/v1/units/{topic['uid']}/members", "POST",
                             {"show_links": [link["id"]], "rule": {"types": ["decision"]}})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["subgraph"]["rule"], {"types": ["decision"]})
        later = unit("Adopt cosine decay too?", "decision")
        status, view = agent(f"/api/agent/v1/units/{topic['uid']}/members")
        rule_member = next(m for m in view["members"] if m["uid"] == later["uid"])
        self.assertEqual(rule_member["member"]["source"], "rule")

        # 4. Find it again: the topic list and the unit's own topics.
        status, listing = agent(f"/api/agent/v1/units?types=topic")
        self.assertEqual(status, 200, listing)
        self.assertIn(topic["uid"], [item["uid"] for item in listing["items"]])
        status, detail = agent(f"/api/agent/v1/units/{claim['uid']}")
        self.assertIn(topic["uid"], [item["uid"] for item in detail["unit"]["topics"]])

        # Writes need a token.
        req = urllib.request.Request(self.base + f"/api/agent/v1/units/{topic['uid']}/members", method="POST",
                                     data=b"{}", headers={"Content-Type": "application/json"})
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=10)
        self.assertIn(ctx.exception.code, (401, 403))


if __name__ == "__main__":
    unittest.main()
