import json
import tempfile
import unittest
from pathlib import Path

from arkstudio import db
from arkstudio.store import Store, StoreError
from arkstudio.tags import TagError


def make_store(tmp: Path) -> Store:
    path = tmp / "test.ark"
    path.mkdir(parents=True)
    (path / "logs").mkdir()
    manifest = {
        "format": "ark-studio-project", "version": 1, "id": "prj_test", "name": "Test",
        "description": "", "created": db.utc_now(), "updated": db.utc_now(),
        "template": "blank", "demo": False,
    }
    db.write_manifest(path, manifest)
    return Store(path, manifest)


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def make(self, title="A unit", type="idea", **kwargs):
        data = {"title": title, "type": type, "body": kwargs.pop("body", "body text")}
        data.update(kwargs)
        return self.store.create_unit(data)

    def test_defaults_and_uid(self):
        unit = self.make(tags=["topic:control", "importance:p1"])
        self.assertEqual(unit["type"], "idea")
        self.assertEqual(unit["status"], "open")
        self.assertEqual(unit["importance"], "p1")
        self.assertTrue(unit["uid"].startswith("I-"))
        self.assertIn("type:idea", unit["tags"])
        self.assertIn("status:open", unit["tags"])
        self.assertIn("topic:control", unit["tags"])

    def test_unknown_type_is_rejected(self):
        with self.assertRaises(StoreError):
            self.make(type="alien")
        with self.assertRaises(StoreError):
            self.make(tags=["type:idea", "type:claim"])
        with self.assertRaises(StoreError):
            self.make(tags=["status:nowhere"])
        with self.assertRaises(StoreError):
            self.make(tags=["importance:urgent"])

    def test_unicode_namespaces_and_colons_in_values(self):
        unit = self.make(tags=["大话题:测试", "source:lark:org2", "话题:具身智能"])
        self.assertIn("大话题:测试", unit["tags"])
        self.assertIn("source:lark:org2", unit["tags"])
        filtered = self.store.list_units({"tags": ["大话题:测试"]})
        self.assertEqual(filtered["total"], 1)

    def test_hierarchy_uses_child_to_parent_direction(self):
        parent = self.make("Paper spine", "story")
        child = self.make("Motivation", "story")
        self.store.create_link(child["id"], parent["id"], "part_of")
        loaded = self.store.get_unit(child["uid"])
        self.assertEqual(loaded["parents"][0]["uid"], parent["uid"])
        parent_loaded = self.store.get_unit(parent["uid"])
        self.assertEqual(parent_loaded["parents"], [])

    def test_update_tags_type_and_dates(self):
        unit = self.make("Old", "idea", tags=["topic:a"], start_at="2026-10-01")
        updated = self.store.update_unit(unit["uid"], {
            "title": "New", "type": "claim", "tags": ["topic:b"], "end_at": "2026-10-10",
            "status": "active", "importance": "p0",
        })
        self.assertEqual(updated["title"], "New")
        self.assertEqual(updated["type"], "claim")
        self.assertEqual(updated["status"], "active")
        self.assertEqual(updated["importance"], "p0")
        self.assertIn("topic:b", updated["tags"])
        self.assertNotIn("topic:a", updated["tags"])
        self.assertEqual(updated["start_at"], "2026-10-01")
        self.assertEqual(updated["end_at"], "2026-10-10")

    def test_bad_dates(self):
        with self.assertRaises(StoreError):
            self.make(start_at="10/01/2026")
        unit = self.make()
        with self.assertRaises(StoreError):
            self.store.update_unit(unit["uid"], {"start_at": "2026-10-10", "end_at": "2026-10-01"})

    def test_timeline_filter_selects_endpoint_membership(self):
        spanning = self.make("spans window", "task", start_at="2026-10-01", end_at="2026-10-30")
        ending = self.make("ends inside", "task", start_at="2026-09-01", end_at="2026-10-10")
        starting = self.make("starts inside", "task", start_at="2026-10-10", end_at="2026-11-20")
        got = self.store.list_units({"start_from": "2026-10-05", "start_to": "2026-10-15"})
        uids = {item["uid"] for item in got["items"]}
        self.assertNotIn(spanning["uid"], uids)
        self.assertEqual(uids, {ending["uid"], starting["uid"]})

    def test_facets_ignore_their_own_filter(self):
        self.make("A", "task", status="active", importance="p0")
        self.make("B", "task", status="open", importance="p1")
        data = self.store.list_units({"statuses": ["active"]}, include_facets=True)
        self.assertEqual(data["total"], 1)
        facets = data["facets"]
        self.assertEqual(sum(facets["types"].values()), 1)
        self.assertEqual(sum(facets["statuses"].values()), 2)
        self.assertEqual(sum(facets["importance"].values()), 1)

    def test_links_are_directed_and_unique(self):
        a = self.make("A", "idea")
        b = self.make("B", "claim")
        link = self.store.create_link(a["id"], b["id"], "inspires")
        self.assertTrue(link["id"])
        again = self.store.create_link(a["id"], b["id"], "inspires")
        self.assertTrue(again.get("already"))
        with self.assertRaises(StoreError):
            self.store.create_link(a["id"], b["id"], "not_a_relation")
        with self.assertRaises(StoreError):
            self.store.create_link(a["id"], a["id"], "relates_to")
        detail = self.store.get_unit(a["uid"])
        self.assertEqual(detail["links_out"][0]["target"]["uid"], b["uid"])
        self.store.delete_link(link["id"])
        self.assertEqual(self.store.get_unit(a["uid"])["links_out"], [])

    def test_filters_types_tags_and_search(self):
        self.make("Alpha", "idea", tags=["topic:control", "stage:capture"])
        self.make("Beta", "idea", tags=["topic:control"])
        self.make("Gamma", "claim", tags=["topic:control", "stage:capture"])
        only = self.store.list_units({"tags": ["stage:capture"]}, include_facets=True)
        self.assertEqual(only["total"], 2)
        both = self.store.list_units({"tags": ["stage:capture", "topic:control"]})
        self.assertEqual(both["total"], 2)
        type_filtered = self.store.list_units({"types": ["idea"]})
        self.assertEqual(type_filtered["total"], 2)
        search = self.store.list_units({"q": "Gam"})
        self.assertEqual(search["total"], 1)
        self.assertIn("topic", only["facets"]["namespaces"])

    def test_tags_or_inside_namespace_and_and_across(self):
        self.make("A", "idea", tags=["topic:a", "stage:capture"])
        self.make("B", "idea", tags=["topic:b"])
        self.make("C", "idea", tags=["topic:a", "stage:other"])
        two_topics = self.store.list_units({"tags": ["topic:a", "topic:b"]})
        self.assertEqual(two_topics["total"], 3)
        cross_namespace = self.store.list_units({"tags": ["topic:a", "stage:capture"]})
        self.assertEqual(cross_namespace["total"], 1)
        self.assertEqual(cross_namespace["items"][0]["title"], "A")
        impossible = self.store.list_units({"tags": ["topic:b", "stage:capture"]})
        self.assertEqual(impossible["total"], 0)

    def test_tags_unset_is_a_virtual_or_branch(self):
        a = self.make("A", "idea", tags=["phase:alignment", "scope:historical"])
        b = self.make("B", "idea", tags=["phase:collection"])
        c = self.make("C", "idea")
        d = self.make("D", "idea", tags=["scope:historical"])
        self.assertEqual(self.store.list_units({"tags_unset": ["phase"]})["total"], 2)
        self.assertEqual(self.store.list_units({"tags": ["phase:alignment", "phase:collection"]})["total"], 2)
        both = self.store.list_units({"tags": ["phase:alignment", "phase:collection"],
                                      "tags_unset": ["phase"]})
        self.assertEqual(both["total"], 4)
        cross = self.store.list_units({"tags": ["phase:alignment"], "tags_unset": ["scope"]})
        self.assertEqual(cross["total"], 0)
        facets = self.store.list_units({}, include_facets=True)["facets"]
        self.assertEqual(facets["unset"]["phase"], 2)
        self.assertEqual({u["uid"] for u in self.store.list_units({"tags_unset": ["phase"]})["items"]},
                         {c["uid"], d["uid"]})

    def test_updated_since_filter(self):
        self.make("Recent", "task")
        self.assertGreater(self.store.list_units({"updated_since": "2000-01-01T00:00:00Z"})["total"], 0)
        self.assertEqual(self.store.list_units({"updated_since": "2999-01-01T00:00:00Z"})["total"], 0)

    def test_status_and_importance_are_or_groups(self):
        self.make("A", "task", status="active", importance="p0")
        self.make("B", "task", status="open", importance="p1")
        self.make("C", "task", status="blocked", importance="p2")
        statuses = self.store.list_units({"statuses": ["active", "open"]})
        self.assertEqual(statuses["total"], 2)
        importance = self.store.list_units({"importance": ["p0", "p1"]})
        self.assertEqual(importance["total"], 2)
        cross = self.store.list_units({"statuses": ["active"], "importance": ["p1"]})
        self.assertEqual(cross["total"], 0)

    def test_slim_graph_omits_full_bodies(self):
        unit = self.make("Long", "idea", body="x" * 5000)
        graph = self.store.graph({}, slim=True)
        node = next(item for item in graph["nodes"] if item["uid"] == unit["uid"])
        self.assertNotIn("body", node)
        self.assertTrue(node["body_truncated"])
        self.assertLessEqual(len(node["excerpt"]), 241)

    def test_archive_hides_from_list(self):
        unit = self.make("Archive me", "note")
        self.store.archive_unit(unit["uid"])
        self.assertEqual(self.store.list_units({})["total"], 0)
        self.assertTrue(self.store.get_unit(unit["uid"])["archived"])

    def test_stats_and_task_counts(self):
        self.make("task one", "task", status="active")
        self.make("task two", "task", status="done")
        self.make("idea", "idea")
        stats = self.store.stats()
        self.assertEqual(stats["tasks"]["total"], 2)
        self.assertEqual(stats["tasks"]["open"], 1)
        self.assertEqual(stats["by_type"]["task"], 2)

    def test_assistant_insights_and_dispatch(self):
        claim = self.make("A claim", "claim")
        task = self.make("A task", "task", status="active")
        insights = __import__("arkstudio.assistant", fromlist=["insights"]).insights(self.store)
        self.assertTrue(any(i["kind"] == "claim-no-evidence" for i in insights))
        self.assertTrue(any(i["kind"] == "task-unassigned" for i in insights))
        prompt = __import__("arkstudio.assistant", fromlist=["dispatch_prompt"]).dispatch_prompt(self.store, task["uid"])
        self.assertIn("A task", prompt["prompt"])
        self.assertIn("UNASSIGNED", prompt["prompt"])

    def test_extract_outline_to_ideas(self):
        from arkstudio import assistant
        result = assistant.extract_ideas(self.store, "- First idea\n- Second idea has a longer body\n\nThird paragraph")
        self.assertEqual(result["created_count"], 3)
        titles = [u["title"] for u in result["created"]]
        self.assertIn("First idea", titles)
        self.assertIn("Third paragraph", titles)
        source = self.make("Source topic", "source")
        linked = assistant.extract_ideas(self.store, "Linked idea", source=source["uid"])
        self.assertEqual(linked["created_count"], 1)
        self.assertEqual(linked["created"][0]["links_out"][0]["target"]["uid"], source["uid"])

    def test_link_relation_default_and_update(self):
        a = self.make("Link source", "idea")
        b = self.make("Link target", "claim")
        link = self.store.create_link(a["id"], b["id"], "unset")
        self.assertEqual(link["relation"], "unset")
        updated = self.store.update_link(link["id"], "supports")
        self.assertTrue(updated["updated"])
        detail = self.store.get_unit(a["uid"])
        self.assertEqual(detail["links_out"][0]["relation"], "supports")

    def test_media_and_export(self):
        unit = self.make("With media", "production")
        media = self.store.add_media(unit["uid"], "assets/example.png", "figure", "A figure")
        self.assertTrue(media["id"])
        self.assertEqual(len(self.store.get_unit(unit["uid"])["media"]), 1)
        from arkstudio import export
        data = export.project_json(self.store)
        self.assertEqual(len(data["units"]), 1)
        md = export.project_markdown(self.store)
        self.assertIn("With media", md)
        self.assertIn("With media", export.project_csv(self.store))


if __name__ == "__main__":
    unittest.main()
