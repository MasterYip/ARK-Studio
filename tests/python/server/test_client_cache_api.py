"""Server contract for the browser's offline project cache.

Conditional GETs (ETag / If-None-Match -> 304), the snapshot delta endpoint,
batched unit details, the revision event stream, the per-account cache id
and access changes.  Real HTTP, two accounts (docs/development/client-cache.md).
"""
from __future__ import annotations

import http.client
import json
import time
import unittest
from unittest import mock

from arkstudio import server as server_mod
from tests.python.auth.test_org import AuthHttpCase
from tests.python.store.test_changes import apply_delta, comparable


class ClientCacheApiTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        self.bob = self.login("bob", "bob-pass")
        self.project = self.project_id()
        self.assertEqual(self.grant(self.project, user="bob", permission="write").status, 200)
        self.tab = {"X-ARK-Project": self.project}

    def get(self, path, cookie, etag=None, headers=None):
        merged = dict(self.tab)
        merged.update(headers or {})
        if etag:
            merged["If-None-Match"] = etag
        return self.api(path, cookie=cookie, headers=merged)

    def first_unit(self, cookie):
        return self.get("/api/units?limit=1&facets=0", cookie).json()["items"][0]

    def test_every_json_get_has_a_validator_and_answers_304(self):
        for path in ("/api/bootstrap", "/api/project", "/api/project/overview",
                     "/api/graph/snapshot", "/api/units?limit=5", "/api/me",
                     "/api/settings/effective", "/api/topics"):
            with self.subTest(path=path):
                first = self.get(path, self.bob)
                self.assertEqual(first.status, 200, first.text)
                etag = first.headers.get("etag")
                self.assertTrue(etag and etag.startswith('W/"'), first.headers)
                again = self.get(path, self.bob, etag)
                self.assertEqual(again.status, 304, again.text)
                self.assertEqual(again.text, "")
                self.assertEqual(again.headers.get("etag"), etag)
                # Strong form and lists are accepted too (proxies rewrite).
                strong = etag[2:]
                self.assertEqual(self.get(path, self.bob, f'"zzz", {strong}').status, 304)

    def test_writes_invalidate_validators(self):
        snapshot = self.get("/api/graph/snapshot", self.bob)
        boot = self.get("/api/bootstrap", self.bob)
        unit = self.first_unit(self.bob)
        detail = self.get(f"/api/units/{unit['id']}", self.bob)
        patched = self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH",
                           headers=self.tab, body={"title": "Renamed by owner"})
        self.assertEqual(patched.status, 200, patched.text)
        for response, path in ((snapshot, "/api/graph/snapshot"), (boot, "/api/bootstrap"),
                               (detail, f"/api/units/{unit['id']}")):
            with self.subTest(path=path):
                fresh = self.get(path, self.bob, response.headers["etag"])
                self.assertEqual(fresh.status, 200)
                self.assertIn("Renamed by owner", fresh.text)

    def test_validators_are_per_account_and_project(self):
        mine = self.get("/api/graph/snapshot", self.owner)
        theirs = self.get("/api/graph/snapshot", self.bob, mine.headers["etag"])
        # Revision-keyed validators include the account: never a cross-user 304.
        self.assertEqual(theirs.status, 200)
        self.assertNotEqual(theirs.headers["etag"], mine.headers["etag"])
        self.assertEqual(theirs.headers.get("x-ark-project-id"), self.project)

    def test_delta_after_mixed_writes_matches_full_reload(self):
        before = self.get("/api/graph/snapshot", self.bob).json()
        units = self.get("/api/units?limit=4&facets=0", self.bob).json()["items"]
        a, b, c = units[0], units[1], units[2]
        writes = [
            ("PATCH", f"/api/units/{a['id']}", {"title": "Delta A", "status": "review"}),
            ("POST", "/api/units", {"title": "Delta new", "type": "note"}),
            ("POST", "/api/links", {"source": b["id"], "target": c["id"], "relation": "supports"}),
            ("POST", f"/api/units/{c['id']}/comments", {"body": "delta comment"}),
            ("POST", f"/api/units/{b['id']}/archive", {}),
        ]
        for method, path, body in writes:
            response = self.api(path, cookie=self.owner, method=method, headers=self.tab, body=body)
            self.assertEqual(response.status, 200, (path, response.text))
        delta = self.get(f"/api/changes?since={before['cursor']}", self.bob).json()
        self.assertFalse(delta["reset"], delta)
        after = self.get("/api/graph/snapshot", self.bob).json()
        self.assertEqual(delta["cursor"], after["cursor"])
        self.assertEqual(comparable(apply_delta(before, delta)), comparable(after))
        self.assertIn(b["id"], delta["deleted"])
        # Nothing new: an empty delta with the same cursor.
        idle = self.get(f"/api/changes?since={after['cursor']}", self.bob).json()
        self.assertEqual((idle["units"], idle["deleted"], idle["cursor"]), ([], [], after["cursor"]))
        self.assertTrue(self.get("/api/changes?since=bogus.1", self.bob).json()["reset"])

    def test_lite_snapshot_and_bodies(self):
        lite = self.get("/api/graph/snapshot?bodies=0", self.bob)
        full = self.get("/api/graph/snapshot", self.bob)
        self.assertLess(len(lite.text), len(full.text))
        self.assertNotIn("search_body", lite.text)
        bodies = self.get("/api/graph/bodies", self.bob)
        self.assertEqual(bodies.status, 200)
        self.assertEqual(self.get("/api/graph/bodies", self.bob, bodies.headers["etag"]).status, 304)
        merged = {node["id"]: node["search_body"] for node in full.json()["nodes"]}
        self.assertEqual(dict(map(tuple, bodies.json()["bodies"])), merged)

    def test_unit_details_batch(self):
        units = self.get("/api/units?limit=3&facets=0", self.bob).json()["items"]
        ids = ",".join(str(unit["id"]) for unit in units) + ",999999"
        batch = self.get(f"/api/unit-details?ids={ids}", self.bob).json()
        self.assertEqual([unit["id"] for unit in batch["units"]], [unit["id"] for unit in units])
        self.assertEqual(batch["missing"], ["999999"])
        single = self.get(f"/api/units/{units[0]['id']}", self.bob).json()["unit"]
        self.assertEqual(batch["units"][0], single)
        self.assertTrue(batch["cursor"])
        # The batch hands out the same validator the single GET sends.
        etag = batch["etags"][str(units[0]["id"])]
        self.assertEqual(self.get(f"/api/units/{units[0]['id']}", self.bob, etag).status, 304)

    def test_revoked_grant_never_serves_the_old_project(self):
        snapshot = self.get("/api/graph/snapshot", self.bob)
        cursor = snapshot.json()["cursor"]
        revoke = self.api(f"/api/org/projects/{self.project}/grants/revoke",
                          cookie=self.owner, method="POST", body={"user": "bob"})
        self.assertEqual(revoke.status, 200, revoke.text)
        for path, etag in (("/api/graph/snapshot", snapshot.headers["etag"]),
                           (f"/api/changes?since={cursor}", None),
                           ("/api/bootstrap", None)):
            with self.subTest(path=path):
                response = self.get(path, self.bob, etag)
                self.assertNotEqual(response.status, 304)
                served = response.headers.get("x-ark-project-id")
                # Bob falls back to another readable project (the demo) or none.
                self.assertNotEqual(served, self.project)
                if response.status == 200 and path == "/api/bootstrap":
                    summary = response.json().get("summary") or {}
                    self.assertNotEqual(summary.get("id"), self.project)

    def test_cache_identity_is_opaque_stable_and_per_account(self):
        owner = self.api("/api/bootstrap", cookie=self.owner).json()["session"]["cache_id"]
        bob = self.api("/api/bootstrap", cookie=self.bob).json()["session"]["cache_id"]
        self.assertTrue(owner and bob)
        self.assertNotEqual(owner, bob)
        self.assertNotIn("bob", bob)
        again = self.login("bob", "bob-pass")
        self.assertEqual(self.api("/api/bootstrap", cookie=again).json()["session"]["cache_id"], bob)
        mobile = self.api("/api/mobile/session", cookie=self.bob).json()
        self.assertEqual(mobile["cache_id"], bob)
        anonymous = self.api("/api/mobile/session").json()
        self.assertIsNone(anonymous["cache_id"])
        boot = self.api("/api/bootstrap", cookie=self.bob).json()
        self.assertEqual(boot["server"]["cache_schema"], server_mod.CLIENT_CACHE_SCHEMA)

    def test_media_raw_revalidates(self):
        project = self.app.project_by_id(self.project)
        (project.path / "assets").mkdir(exist_ok=True)
        (project.path / "assets" / "dot.png").write_bytes(b"\x89PNG\r\n\x1a\nfake")
        unit = self.first_unit(self.owner)
        media = self.api("/api/media", cookie=self.owner, method="POST", headers=self.tab,
                         body={"unit": unit["id"], "url": "assets/dot.png", "kind": "image"})
        self.assertEqual(media.status, 200, media.text)
        media_id = media.json()["id"]
        first = self.get(f"/api/media/{media_id}/raw", self.bob)
        self.assertEqual(first.status, 200)
        self.assertIn("no-cache", first.headers.get("cache-control"))
        self.assertEqual(self.get(f"/api/media/{media_id}/raw", self.bob,
                                  first.headers["etag"]).status, 304)
        self.api(f"/api/org/projects/{self.project}/grants/revoke",
                 cookie=self.owner, method="POST", body={"user": "bob"})
        denied = self.get(f"/api/media/{media_id}/raw", self.bob, first.headers["etag"])
        self.assertNotEqual(denied.status, 304)

    # ------------------------------------------------------------ event stream
    def open_events(self, cookie):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        conn.request("GET", f"/api/events?ark_project={self.project}", headers={
            "Host": self.host, "Cookie": cookie, "Accept": "text/event-stream"})
        response = conn.getresponse()
        return conn, response

    @staticmethod
    def next_event(response, deadline=8.0, skip=("sessions",)):
        """Next server-sent event, ignoring kinds in ``skip``."""
        stop = time.monotonic() + deadline
        event, data, lines = "message", "", []
        while time.monotonic() < stop:
            line = response.fp.readline().decode("utf-8")
            if not line:
                return None
            line = line.rstrip("\r\n")
            if not line:
                if data and event not in skip:
                    return event, json.loads(data)
                event, data = "message", ""
                continue
            if line.startswith("event:"):
                event = line[6:].strip()
            elif line.startswith("data:"):
                data += line[5:].strip()
        return None

    def test_event_stream_pushes_revisions_and_ends_on_revoke(self):
        with mock.patch.object(server_mod, "EVENTS_TICK", 0.05), \
                mock.patch.object(server_mod, "EVENTS_RECHECK", 0.2):
            conn, response = self.open_events(self.bob)
            try:
                self.assertEqual(response.status, 200)
                self.assertTrue(response.getheader("Content-Type").startswith("text/event-stream"))
                self.assertEqual(response.getheader("X-Accel-Buffering"), "no")
                self.assertIn("no-cache", response.getheader("Cache-Control"))
                self.assertIsNone(response.getheader("Content-Encoding"))
                kind, first = self.next_event(response)
                self.assertEqual(kind, "revision")
                self.assertEqual(first["project"], self.project)
                unit = self.first_unit(self.owner)
                self.api(f"/api/units/{unit['id']}", cookie=self.owner, method="PATCH",
                         headers=self.tab, body={"title": "Pushed"})
                kind, second = self.next_event(response)
                self.assertEqual(kind, "revision")
                self.assertNotEqual(second["cursor"], first["cursor"])
                self.api(f"/api/org/projects/{self.project}/grants/revoke",
                         cookie=self.owner, method="POST", body={"user": "bob"})
                while True:
                    item = self.next_event(response)
                    self.assertIsNotNone(item, "stream ended without a denied event")
                    if item[0] != "revision":
                        break
                self.assertEqual(item[0], "denied")
            finally:
                conn.close()

    def test_event_stream_ends_when_signed_out(self):
        with mock.patch.object(server_mod, "EVENTS_TICK", 0.05), \
                mock.patch.object(server_mod, "EVENTS_RECHECK", 0.2):
            conn, response = self.open_events(self.bob)
            try:
                self.assertEqual(self.next_event(response)[0], "revision")
                self.api("/api/auth/logout", cookie=self.bob, method="POST", body={})
                item = self.next_event(response)
                self.assertEqual(item[0], "auth")
            finally:
                conn.close()

    def test_stream_reports_this_accounts_session_list(self):
        with mock.patch.object(server_mod, "EVENTS_TICK", 0.05), \
                mock.patch.object(server_mod, "EVENTS_SESSIONS_EVERY", 0.1):
            conn, response = self.open_events(self.bob)
            try:
                seen = {}
                for _ in range(4):
                    item = self.next_event(response, skip=())
                    self.assertIsNotNone(item)
                    seen.setdefault(item[0], item[1])
                    if "sessions" in seen and "revision" in seen:
                        break
                self.assertIn("sessions", seen)
                self.assertEqual(seen["sessions"]["project"], self.project)
                self.assertTrue(seen["sessions"]["token"])
            finally:
                conn.close()
        # The token depends only on what this account may see.
        handler_cls = type("H", (server_mod.Handler,), {"app": self.app})
        self.assertTrue(callable(getattr(handler_cls, "_sessions_token")))


if __name__ == "__main__":
    unittest.main()
