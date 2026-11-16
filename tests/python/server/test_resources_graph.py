"""Resources graph: aggregation, status, occupancy and personal-device privacy.

GET /api/resources/graph is read-only: it must never probe GPU servers or
spawn a subprocess.  Personal devices of other members only reveal that they
exist and the project's shared resources on them.
"""
from __future__ import annotations

import json
import subprocess
import unittest
from datetime import datetime, timezone
from unittest import mock

from arkstudio import resources_graph as rg
from tests.python.auth.test_org import AuthHttpCase, request

NOW = datetime(2026, 11, 14, 12, 0, tzinfo=timezone.utc).timestamp()


def iso(seconds_ago: float) -> str:
    return datetime.fromtimestamp(NOW - seconds_ago, timezone.utc).isoformat()


def layer(owner="", *, resources=(), locations=(), hosts=(), verification=None):
    return {"layer": "user" if owner else "project", "owner": owner,
            "profile": {"resources": list(resources)}, "locations": list(locations),
            "hosts": list(hosts), "verification": verification}


def sources(**overrides):
    base = {
        "now": NOW,
        "project": {"id": "p1", "name": "Demo", "path": "/srv/ark/projects/demo", "db": "/srv/ark/projects/demo/ark.sqlite3"},
        "server": {"hostname": "ark-01", "platform": "linux", "fingerprint": "fp-ark",
                   "disk": {"total": 1000e9, "used": 250e9, "free": 750e9}, "load1": 0.5},
        "layers": {"project": layer(), "users": []},
        "history": [],
        "gpu": {"enabled": False, "servers": [], "last_probe": None, "jobs": []},
        "sessions": [],
        "phones": [],
        "users": {"alice": {"username": "alice", "display_name": "Alice"},
                  "bob": {"username": "bob", "display_name": "Bob"}},
        "prefs": {},
    }
    base.update(overrides)
    return base


def by_id(items):
    return {item["id"]: item for item in items}


GPU = {
    "enabled": True,
    "servers": [{"name": "lab-01", "host": "lab-01.example", "user": "ark"},
                {"name": "lab-02", "host": "lab-02.example"}, {"name": "lab-03", "host": "x"}],
    "last_probe": {"probed_at": iso(120), "servers": [
        {"server": "lab-01", "ok": True, "gpus": [
            {"index": 0, "name": "A100", "memory_total_mb": 40000, "memory_used_mb": 30000, "utilization_pct": 90},
            {"index": 1, "name": "A100", "memory_total_mb": 40000, "memory_used_mb": 20000, "utilization_pct": 70},
            {"index": 2, "name": "A100", "memory_total_mb": 40000, "memory_used_mb": 0, "utilization_pct": 0}],
         "system": {"cpu_percent": 35, "memory_used_mb": 64000, "memory_total_mb": 256000,
                    "processes": [{"pid": 1, "gpu_index": 0, "memory_mb": 12000, "job_id": "job-1"},
                                  {"pid": 2, "gpu_index": 0, "memory_mb": 18000, "job_id": ""}]}},
        {"server": "lab-02", "ok": False, "error": {"kind": "ssh", "message": "connection timed out"}, "gpus": []},
    ]},
    "jobs": [{"id": "job-1", "server": "lab-01", "gpu_indices": [0], "job": "train-small", "status": "running"}],
}


class BuildGraphTests(unittest.TestCase):
    def test_ark_server_is_online_with_storage_and_sessions(self):
        graph = rg.build_graph(sources(sessions=[{"owner": "alice", "status": "running", "worktree": "/w/feat-a"},
                                                 {"owner": "bob", "status": "done"}]), "alice")
        ark = by_id(graph["devices"])["ark"]
        self.assertEqual(ark["status"]["state"], "online")
        self.assertEqual(ark["metrics"]["disk_pct"], 25.0)
        kinds = {r["kind"]: r for r in graph["resources"] if r["device"] == "ark"}
        self.assertEqual(kinds["database"]["path"], "/srv/ark/projects/demo/ark.sqlite3")
        self.assertEqual(kinds["sessions"]["metrics"], {"running": 1, "mine": 1, "total": 2})
        self.assertEqual(kinds["worktree"]["path"], "/w/feat-a")

    def test_gpu_status_metrics_and_our_share(self):
        graph = rg.build_graph(sources(gpu=GPU), "alice")
        devices = by_id(graph["devices"])
        lab1, lab2, lab3 = devices["gpu:lab-01"], devices["gpu:lab-02"], devices["gpu:lab-03"]
        self.assertEqual(lab1["status"]["state"], "online")
        self.assertEqual(lab2["status"]["state"], "offline")
        self.assertIn("connection timed out", lab2["status"]["reason"])
        self.assertEqual(lab3["status"]["state"], "unknown")
        self.assertEqual(lab1["actions"], ["check"])
        self.assertEqual(lab1["occupancy"], {
            "gpus_total": 3, "gpus_ours": 1, "gpus_busy_others": 1, "gpus_free": 1,
            "mem_ours_mb": 12000, "mem_others_mb": 38000, "mem_total_mb": 120000, "util_avg_pct": 53.3})
        gpu0 = next(r for r in graph["resources"] if r["device"] == "gpu:lab-01" and r["kind"] == "gpu")
        self.assertEqual(gpu0["metrics"]["mem_ours_mb"], 12000)
        self.assertEqual(gpu0["metrics"]["mem_others_mb"], 18000)
        self.assertTrue(gpu0["metrics"]["ours"])
        links = {(c["target"], c["state"]) for c in graph["connections"] if c["kind"] == "ssh"}
        self.assertEqual(links, {("gpu:lab-01", "ok"), ("gpu:lab-02", "failed"), ("gpu:lab-03", "unknown")})

    def test_an_old_probe_is_stale_and_resources_are_last_known(self):
        old = dict(GPU, last_probe={**GPU["last_probe"], "probed_at": iso(3 * 3600)})
        graph = rg.build_graph(sources(gpu=old), "alice")
        lab1 = by_id(graph["devices"])["gpu:lab-01"]
        self.assertEqual(lab1["status"]["state"], "stale")
        self.assertIn("3 h ago", lab1["status"]["reason"])
        self.assertTrue(all(r["last_known"] for r in graph["resources"] if r["device"] == "gpu:lab-01"))

    def test_setup_resources_hosts_remotes_and_unplaced(self):
        project = layer(resources=[
            {"id": "code", "kind": "repository", "name": "code", "details": {"remote": "git@github.com:lab/code.git"}},
            {"id": "data", "kind": "storage", "name": "datasets", "details": {"kind": "nfs"}},
            {"id": "db", "kind": "storage", "name": "results db", "details": {"kind": "database"}},
            {"id": "gpu", "kind": "compute", "name": "lab compute", "details": {"gpu_server": "lab-01"}},
            {"id": "deploy", "kind": "command", "name": "deploy", "details": {}},
        ], locations=[
            {"resource": "project/code", "host_id": "lab-01", "path_or_ref": "/srv/code", "verified_at": iso(60)},
            {"resource": "project/data", "host_id": "nas", "host_label": "NAS", "path_or_ref": "/mnt/data"},
        ])
        graph = rg.build_graph(sources(gpu=GPU, layers={"project": project, "users": []}), "alice")
        devices = by_id(graph["devices"])
        self.assertIn("git:github.com", devices)
        self.assertEqual(devices["host:nas"]["scope"], "shared")
        located = {(r["device"], r["kind"], r["label"]) for r in graph["resources"]}
        self.assertIn(("gpu:lab-01", "repository", "code"), located, "a host named like a GPU server is that server")
        self.assertIn(("gpu:lab-01", "compute", "lab compute"), located)
        self.assertIn(("host:nas", "storage", "datasets"), located)
        self.assertIn(("unplaced", "database", "results db"), located)
        self.assertNotIn("deploy", {r["label"] for r in graph["resources"]})
        self.assertIn({"source": "gpu:lab-01", "target": "git:github.com", "kind": "git-remote"},
                      [{k: c[k] for k in ("source", "target", "kind")} for c in graph["connections"]])

    def test_host_status_comes_from_passive_signals(self):
        alice = layer("alice", resources=[{"id": "nb", "kind": "repository", "name": "notebooks", "details": {}}],
                      locations=[{"resource": "u:alice/nb", "host_id": "alice-pc", "path_or_ref": "C:/nb"}],
                      hosts=[{"id": "alice-pc", "label": "Alice laptop"}, {"id": "alice-old", "label": "Old"},
                             {"id": "fp-ark", "label": "server"}])
        history = [{"at": iso(300), "event": "location", "host": "alice-pc"},
                   {"at": iso(9 * 86400), "event": "host", "host": "alice-old"}]
        graph = rg.build_graph(sources(layers={"project": layer(), "users": [alice]}, history=history), "alice")
        devices = by_id(graph["devices"])
        self.assertEqual(devices["host:alice-pc"]["status"]["state"], "online")
        self.assertEqual(devices["host:alice-old"]["status"]["state"], "stale")
        self.assertIn("9 d ago", devices["host:alice-old"]["status"]["reason"])
        self.assertNotIn("host:fp-ark", devices, "the ARK server's own fingerprint is the ark device")
        self.assertEqual(devices["host:alice-pc"]["scope"], "personal")
        self.assertTrue(devices["host:alice-pc"]["mine"])

    def test_personal_devices_are_private_to_other_members(self):
        project = layer(resources=[{"id": "code", "kind": "repository", "name": "code", "details": {}}],
                        locations=[{"resource": "project/code", "host_id": "lab", "path_or_ref": "/srv/code"}])
        alice = layer("alice", resources=[{"id": "nb", "kind": "repository", "name": "notebooks", "details": {}}],
                      locations=[{"resource": "u:alice/nb", "host_id": "alice-pc", "path_or_ref": "C:/Users/alice/nb"},
                                 {"resource": "project/code", "host_id": "alice-pc", "path_or_ref": "C:/Users/alice/code"}],
                      hosts=[{"id": "alice-pc", "label": "Alice laptop"}])
        phones = [{"id": "ph1", "user": "alice", "label": "Android phone", "last_seen_at": NOW - 30}]
        data = sources(layers={"project": project, "users": [alice]}, phones=phones,
                       history=[{"at": iso(60), "host": "alice-pc", "event": "location"}])
        bob = rg.build_graph(data, "bob")
        device = by_id(bob["devices"])["host:alice-pc"]
        self.assertEqual(device["label"], "Alice's device")
        self.assertEqual(device["status"]["state"], "unknown")
        self.assertIsNone(device["status"]["last_seen"])
        on_device = [r for r in bob["resources"] if r["device"] == "host:alice-pc"]
        self.assertEqual([(r["label"], r["path"], r["redacted"]) for r in on_device], [("code", None, True)])
        text = json.dumps(bob)
        self.assertNotIn("C:/Users/alice", text)
        self.assertNotIn("Alice laptop", text)
        self.assertNotIn("notebooks", text)
        phone = by_id(bob["devices"])["phone:ph1"]
        self.assertEqual(phone["status"]["state"], "unknown")
        # The owner sees everything plus privacy actions.
        mine = rg.build_graph(data, "alice")
        own = by_id(mine["devices"])["host:alice-pc"]
        self.assertEqual(own["label"], "Alice laptop")
        self.assertEqual(own["status"]["state"], "online")
        self.assertIn("privacy", own["actions"])
        self.assertIn("C:/Users/alice/nb", json.dumps(mine))
        self.assertEqual(by_id(mine["devices"])["phone:ph1"]["status"]["state"], "online")
        # Shared details: Bob sees the name, status and paths.
        shared = rg.build_graph({**data, "prefs": {"alice": {"host:alice-pc": {"share_details": True}}}}, "bob")
        device = by_id(shared["devices"])["host:alice-pc"]
        self.assertEqual(device["label"], "Alice laptop")
        self.assertIn("C:/Users/alice/nb", json.dumps(shared))
        # Hidden: gone for Bob (shared resources fall back to "not located"), kept for Alice.
        prefs = {"alice": {"host:alice-pc": {"hidden": True}}}
        hidden = rg.build_graph({**data, "prefs": prefs}, "bob")
        self.assertNotIn("host:alice-pc", by_id(hidden["devices"]))
        self.assertFalse(any(c["source"] == "host:alice-pc" for c in hidden["connections"]))
        self.assertNotIn("C:/Users/alice", json.dumps(hidden))
        self.assertTrue(by_id(rg.build_graph({**data, "prefs": prefs}, "alice")["devices"])["host:alice-pc"]["prefs"]["hidden"])

    def test_git_host_parsing(self):
        self.assertEqual(rg.git_host("git@github.com:lab/x.git"), "github.com")
        self.assertEqual(rg.git_host("https://token@gitlab.example.org/a/b"), "gitlab.example.org")
        self.assertEqual(rg.git_host("/local/path"), "")


class FakeGpu:
    def __init__(self):
        self.probes = 0
        self._queue = None
        self.last_probe = GPU["last_probe"]

    def servers(self):
        return {"enabled": True, "servers": GPU["servers"]}

    def probe(self, body=None):
        self.probes += 1
        return {"servers": []}

    def __getattr__(self, name):  # anything else is unexpected here
        raise AssertionError(f"resources graph called gpu.{name}")


class ResourcesGraphApiTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        created = self.api("/api/projects", cookie=self.owner, method="POST",
                           body={"name": "Lab resources", "template": "blank"})
        self.assertEqual(created.status, 200, created.text)
        self.main = created.json()["summary"]["id"]
        self.gpu = FakeGpu()
        self.app.gpu = self.gpu
        self.cookies = {}
        self.tokens = {}
        for name in ("alice", "bob", "outsider"):
            self.assertEqual(self.create_user(name, f"{name}-pass").status, 200)
            if name != "outsider":
                self.assertEqual(self.grant(self.main, user=name, permission="write").status, 200)
            self.cookies[name] = self.login(name, f"{name}-pass")
        token = self.api("/api/personal/agent-tokens", cookie=self.cookies["alice"],
                         method="POST", body={"label": "setup", "scope": "write"})
        headers = {"Host": self.host, "Authorization": f"Bearer {token.json()['token']}",
                   "Content-Type": "application/json", "X-ARK-Project": self.main, "X-ARK-Host": "alice-pc"}
        body = {"resources": [{"id": "nb", "kind": "repository", "path_or_ref": "C:/Users/alice/nb",
                               "details": {"role": "code"}}], "propose": True}
        response = request(self.base, "/api/agent/v1/setup/draft", method="POST", headers=headers,
                           body=json.dumps(body))
        self.assertEqual(response.status, 200, response.text)

    def graph(self, name):
        response = self.api("/api/resources/graph", cookie=self.cookies[name], headers={"X-ARK-Project": self.main})
        self.assertEqual(response.status, 200, response.text)
        return response.json()

    def test_read_only_and_no_probe_or_subprocess(self):
        with mock.patch.object(subprocess, "Popen", side_effect=AssertionError("subprocess")), \
                mock.patch.object(subprocess, "run", side_effect=AssertionError("subprocess")):
            graph = self.graph("bob")
        self.assertEqual(self.gpu.probes, 0)
        devices = by_id(graph["devices"])
        self.assertEqual(devices["gpu:lab-01"]["status"]["state"], "stale")  # fixture probe is fixed in the past
        self.assertIn("ark", devices)

    def test_outsider_never_sees_the_project(self):
        response = self.api("/api/resources/graph", cookie=self.cookies["outsider"],
                            headers={"X-ARK-Project": self.main})
        # Without a grant the request falls back to a project the outsider may
        # read (or fails); the Lab project's devices never appear.
        if response.status == 200:
            self.assertNotEqual(response.json()["project"]["id"], self.main)
        else:
            self.assertIn(response.status, (403, 404, 409), response.text)
        self.assertNotIn("alice-pc", response.text)
        self.assertNotIn("C:/Users/alice", response.text)

    def test_two_users_privacy_and_owner_controls(self):
        mine = by_id(self.graph("alice")["devices"])["host:alice-pc"]
        self.assertTrue(mine["mine"])
        self.assertEqual(mine["scope"], "personal")
        bob = self.graph("bob")
        self.assertEqual(by_id(bob["devices"])["host:alice-pc"]["label"], "alice's device")
        self.assertNotIn("C:/Users/alice", json.dumps(bob))
        # Bob cannot change Alice's device.
        denied = self.api("/api/resources/devices/host:alice-pc", cookie=self.cookies["bob"], method="POST",
                          body={"hidden": True}, headers={"X-ARK-Project": self.main})
        self.assertEqual(denied.status, 404, denied.text)
        # Alice shares details, then hides it.
        for body, check in (({"share_details": True}, lambda g: "C:/Users/alice/nb" in json.dumps(g)),
                            ({"hidden": True}, lambda g: "host:alice-pc" not in by_id(g["devices"]))):
            response = self.api("/api/resources/devices/host:alice-pc", cookie=self.cookies["alice"],
                                method="POST", body=body, headers={"X-ARK-Project": self.main})
            self.assertEqual(response.status, 200, response.text)
            self.assertTrue(check(self.graph("bob")))
        self.assertTrue(by_id(self.graph("alice")["devices"])["host:alice-pc"]["prefs"]["hidden"])
        # Still the owner's to unhide.
        response = self.api("/api/resources/devices/host:alice-pc", cookie=self.cookies["alice"],
                            method="POST", body={"hidden": False}, headers={"X-ARK-Project": self.main})
        self.assertEqual(response.status, 200, response.text)
        self.assertIn("host:alice-pc", by_id(self.graph("bob")["devices"]))
        bad = self.api("/api/resources/devices/host:alice-pc", cookie=self.cookies["alice"],
                       method="POST", body={"hidden": "yes"}, headers={"X-ARK-Project": self.main})
        self.assertEqual(bad.status, 400, bad.text)


if __name__ == "__main__":
    unittest.main()
