"""Home overview aggregates (arkstudio/overview.py, GET /api/project/overview)."""
import json
import os
import tempfile
import time
import unittest
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

from arkstudio import overview
from arkstudio.server import App, serve
from tests.python.store.test_store import make_store


def _utc_today():
    # project_overview counts days in UTC, so tests must too (local dates differ near midnight).
    return datetime.now(timezone.utc).date()


def day(offset: int) -> str:
    return (_utc_today() + timedelta(days=offset)).isoformat()


class OverviewTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.store = make_store(Path(self.tmp.name))

    def tearDown(self):
        self.store.close()
        self.tmp.cleanup()

    def make(self, title, type="task", status="open", **extra):
        return self.store.create_unit({"title": title, "type": type, "status": status, **extra})

    def age(self, days: int):
        """Move every unit and activity row ``days`` into the past."""
        stamp = (_utc_today() - timedelta(days=days)).isoformat() + "T09:00:00+00:00"
        with self.store.lock:
            self.store.con.execute("UPDATE units SET created_at=?, updated_at=?", (stamp, stamp))
            self.store.con.execute("UPDATE activity SET at=?", (stamp,))
            self.store.con.commit()

    def ov(self, **kw):
        return overview.project_overview(self.store, **kw)

    def test_empty_project(self):
        data = self.ov(members=["ada"])
        self.assertEqual(14, len(data["days"]))
        self.assertEqual(day(0), data["days"][-1]["date"])
        for key in ("active", "blocked", "review", "overdue"):
            self.assertEqual({"now": 0, "prev": 0, "delta": 0}, data["kpis"][key])
        self.assertEqual({"week": 0, "prev": 0}, data["kpis"]["created"])
        self.assertEqual(["capture", "plan", "evidence", "execute", "make", "share"],
                         [s["id"] for s in data["stages"]])
        self.assertTrue(all(s["count"] == 0 for s in data["stages"]))
        self.assertEqual([{"username": "ada", "member": True, "open": 0, "active": 0, "review": 0,
                           "blocked": 0, "overdue": 0}], data["members"])
        self.assertEqual(0, data["research"]["claims"]["total"])
        self.assertEqual([], data["due"]["overdue"])
        self.assertEqual(0, data["blocked"]["total"])
        self.assertEqual([], data["activity"])

    def test_overdue_and_due_soon(self):
        late = self.make("Late", end_at=day(-2), start_at=day(-5))
        self.make("Late but done", status="done", end_at=day(-2), start_at=day(-5))
        idea = self.make("Late idea", type="idea", end_at=day(-1), start_at=day(-3))
        today = self.make("Due today", end_at=day(0))
        self.make("Due later", end_at=day(20))
        archived = self.make("Archived late", end_at=day(-3), start_at=day(-4))
        self.store.archive_unit(archived["uid"])
        data = self.ov()
        self.assertEqual(2, data["kpis"]["overdue"]["now"])
        self.assertEqual([late["uid"], idea["uid"]], [u["uid"] for u in data["due"]["overdue"]])
        self.assertEqual(2, data["due"]["overdue_total"])
        self.assertEqual([today["uid"]], [u["uid"] for u in data["due"]["soon"]])
        # The Map's Overdue time filter returns the same units.
        found = {u["uid"] for u in self.store.list_units({"overdue": True, "limit": 50})["items"]}
        self.assertEqual({late["uid"], idea["uid"]}, found)

    def test_claims_lacking_evidence(self):
        supported = self.make("Supported", type="claim")
        contested = self.make("Contested", type="claim")
        lacking = self.make("Lacking", type="claim")
        closed = self.make("Closed lacking", type="claim", status="done")
        ev = self.make("Evidence", type="evidence")
        refute = self.make("Counter", type="evidence")
        self.store.create_link(ev["uid"], supported["uid"], "supports")
        self.store.create_link(refute["uid"], contested["uid"], "contradicts")
        # The wrong direction does not count as support.
        self.store.create_link(lacking["uid"], ev["uid"], "supports")
        claims = self.ov()["research"]["claims"]
        self.assertEqual(4, claims["total"])
        self.assertEqual(1, claims["supported"])
        self.assertEqual(1, claims["contested"])
        self.assertEqual(2, claims["lacking"])
        self.assertEqual(1, claims["lacking_open"])
        self.assertEqual([lacking["uid"]], [c["uid"] for c in claims["lacking_items"]])
        self.assertNotIn(closed["uid"], [c["uid"] for c in claims["lacking_items"]])

    def test_member_load_uses_effective_assignee(self):
        story = self.make("Branch", type="story", status="active", assignee="ada")
        child = self.make("Child", status="review")
        self.store.create_link(child["uid"], story["uid"], "part_of")
        self.make("Bo task", status="blocked", assignee="bo", end_at=day(-1), start_at=day(-2))
        self.make("Bo done", status="done", assignee="bo")
        self.make("Nobody", status="open")
        data = self.ov(members=["ada", "bo", "cy"])
        by = {m["username"]: m for m in data["members"]}
        self.assertEqual((2, 1, 1), (by["ada"]["open"], by["ada"]["active"], by["ada"]["review"]))
        self.assertEqual((1, 1, 1), (by["bo"]["open"], by["bo"]["blocked"], by["bo"]["overdue"]))
        self.assertEqual(0, by["cy"]["open"])
        self.assertEqual(["ada", "bo", "cy"], [m["username"] for m in data["members"]])
        self.assertEqual(1, data["unassigned"]["open"])

    def test_deltas_against_last_week(self):
        moving = self.make("Moving", status="open")
        stuck = self.make("Stuck", status="active")
        finished = self.make("Finished", status="active")
        self.age(10)
        self.store.update_unit(moving["uid"], {"status": "active"})
        self.store.update_unit(finished["uid"], {"status": "done"})
        self.store.update_unit(stuck["uid"], {"status": "blocked"})
        self.make("New blocker", status="blocked")
        kpis = self.ov()["kpis"]
        # A week ago: stuck + finished active; now only moving.
        self.assertEqual({"now": 1, "prev": 2, "delta": -1}, kpis["active"])
        self.assertEqual({"now": 2, "prev": 0, "delta": 2}, kpis["blocked"])
        # Three were created 10 days ago (the week before), one today.
        self.assertEqual({"week": 1, "prev": 3}, kpis["created"])
        self.assertEqual({"week": 1, "prev": 0}, kpis["completed"])
        days = self.ov()["days"]
        self.assertEqual(1, days[-1]["completed"])
        self.assertEqual(1, days[-1]["created"])

    def test_overdue_delta_and_archive_window(self):
        old = self.make("Old late", end_at=day(-20), start_at=day(-30))
        recent = self.make("Recently late", end_at=day(-2), start_at=day(-9))
        gone = self.make("Archived this week", status="active")
        self.age(14)
        self.store.archive_unit(gone["uid"])
        kpis = self.ov()["kpis"]
        # 7 days ago only the old one was past its end date.
        self.assertEqual({"now": 2, "prev": 1, "delta": 1}, kpis["overdue"])
        self.assertEqual({"now": 0, "prev": 1, "delta": -1}, kpis["active"])
        self.assertTrue(old and recent)

    def test_blocked_lists_blockers(self):
        task = self.make("Waiting on data", status="blocked")
        dep = self.make("Get data", status="active")
        blocker = self.make("Cluster outage", type="note")
        self.store.create_link(task["uid"], dep["uid"], "depends_on")
        self.store.create_link(blocker["uid"], task["uid"], "blocks")
        blocked = self.ov()["blocked"]
        self.assertEqual(1, blocked["total"])
        rels = {(b["uid"], b["relation"]) for b in blocked["items"][0]["blockers"]}
        self.assertEqual({(dep["uid"], "depends_on"), (blocker["uid"], "blocks")}, rels)

    def test_stage_progress_and_activity_feed(self):
        self.make("Idea", type="idea")
        self.make("Done idea", type="idea", status="done")
        exp = self.make("Exp", type="experiment", status="active")
        self.store.create_link(exp["uid"], self.make("Tmp", type="note")["uid"], "relates_to")
        data = self.ov()
        capture = data["stages"][0]
        self.assertEqual((2, 1), (capture["count"], capture["done"]))
        self.assertEqual(1, data["stages"][1]["active"])
        self.assertEqual({"active": 1}, data["research"]["experiments"]["by_status"])
        self.assertNotIn("link.create", {a["action"] for a in data["activity"]})

    def test_pipeline_review_and_exclusions_match_project_stats(self):
        for stage_id, types in overview.STAGES:
            with self.subTest(stage=stage_id):
                for status in ("inbox", "open", "active", "review", "blocked", "done",
                               "abandoned", "archived"):
                    self.make(f"{stage_id} {status}", type=types[0], status=status)
                archived = self.make(f"{stage_id} archived flag", type=types[0], status="review")
                self.store.archive_unit(archived["uid"])
                tagged = self.make(f"{stage_id} tagged blocker", type=types[0], status="review")
                done = self.make(f"{stage_id} done blocker", type=types[0], status="done")
                # Legacy databases can contain a blocked tag alongside another status.
                with self.store.lock:
                    self.store.con.executemany("INSERT INTO unit_tags(unit_id,tag) VALUES(?,'status:blocked')",
                                               [(tagged["id"],), (done["id"],)])
                    self.store.con.commit()
        stages = self.ov()["stages"]
        self.assertEqual(stages, self.store.stats()["stages"])
        for stage in stages:
            with self.subTest(stage=stage["id"]):
                self.assertEqual(8, stage["count"])
                self.assertEqual(2, stage["done"])
                self.assertEqual(1, stage["active"])
                self.assertEqual(1, stage["review"])
                self.assertEqual(2, stage["blocked"])
                self.assertEqual(6, stage["open"])
                # The stage's click-through uses the same eligible statuses.
                found = self.store.list_units({"types": stage["types"],
                    "statuses": [*overview.OPEN_STATUSES, "done"], "limit": 100})
                self.assertEqual(stage["count"], found["total"])

    def test_backdrop_graph_contains_live_units_and_their_links(self):
        source = self.make("Source", type="idea")
        target = self.make("Target", type="claim")
        archived = self.make("Archived")
        self.store.create_link(source["uid"], target["uid"], "supports")
        self.store.create_link(target["uid"], archived["uid"], "relates_to")
        with self.store.lock:
            self.store.con.execute("UPDATE units SET archived=1 WHERE id=?", (archived["id"],))
            self.store.con.commit()
        graph = self.ov()["graph"]
        self.assertEqual({source["id"], target["id"]}, {n["id"] for n in graph["nodes"]})
        self.assertTrue(all(node["degree"] == 1 for node in graph["nodes"]))
        self.assertEqual([{"source": source["id"], "target": target["id"], "relation": "supports"}],
                         graph["edges"])

    def test_fast_on_a_thousand_units(self):
        types = ["idea", "question", "claim", "evidence", "task", "experiment", "story"]
        statuses = ["open", "active", "review", "blocked", "done"]
        made = []
        for i in range(1000):
            made.append(self.make(f"Unit {i}", type=types[i % len(types)], status=statuses[i % 5],
                                  assignee=["ada", "bo", None][i % 3],
                                  end_at=day(-(i % 9) + 4), start_at=day(-12)))
        for i in range(0, 600, 3):
            self.store.create_link(made[i]["uid"], made[i + 1]["uid"], "supports")
        self.ov()  # warm the assignee index
        start = time.perf_counter()
        data = self.ov(members=["ada", "bo"])
        elapsed = (time.perf_counter() - start) * 1000
        self.assertEqual(1000, sum(s["count"] for s in data["stages"]) + 0)
        self.assertLess(elapsed, 250, f"overview took {elapsed:.1f} ms")


class OverviewApiTests(unittest.TestCase):
    def test_endpoint_round_trip(self):
        old = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        tmp = tempfile.TemporaryDirectory()
        app = App(Path(tmp.name) / "projects", seed_demo=True)
        httpd = serve(app, port=0, block=False)
        try:
            base = f"http://127.0.0.1:{httpd.server_address[1]}"
            with urllib.request.urlopen(base + "/api/project/overview", timeout=15) as response:
                data = json.loads(response.read())
            for key in ("kpis", "days", "stages", "members", "due", "blocked", "research",
                        "activity", "runs", "gpu", "setup"):
                self.assertIn(key, data)
            with urllib.request.urlopen(base + "/api/project", timeout=15) as response:
                project = json.loads(response.read())
            stages = {s["id"]: s for s in project["stats"]["stages"]}
            for stage in data["stages"]:
                self.assertEqual(stages[stage["id"]]["count"], stage["count"])
                self.assertEqual(stages[stage["id"]]["done"], stage["done"])
            self.assertEqual(0, data["runs"]["total"])
            self.assertIn("state", data["setup"])
            with urllib.request.urlopen(base + "/api/units?overdue=1&facets=0", timeout=15) as response:
                units = json.loads(response.read())
            self.assertEqual(data["due"]["overdue_total"], units["total"])
        finally:
            httpd.shutdown()
            httpd.server_close()
            app.project.close()
            tmp.cleanup()
            if old is None:
                os.environ.pop("ARK_CHAT_MODE", None)
            else:
                os.environ["ARK_CHAT_MODE"] = old


if __name__ == "__main__":
    unittest.main()
