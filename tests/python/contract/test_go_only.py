"""go_only.FIXES predicates stay narrow (no arkstudio import)."""
from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent))
import go_only  # noqa: E402


def resp(status: int, body: bytes):
    return SimpleNamespace(status=status, body=body, headers={})


PY_INF = b'{"error": "Internal error: cannot convert float infinity to integer"}'


class FixesTest(unittest.TestCase):
    def test_resources_nonfinite(self):
        fix = go_only.fix_for("GET", "/api/resources/graph?x=1", resp(500, PY_INF), resp(200, b"{}"))
        self.assertEqual(fix.id, "resources-graph-nonfinite-gpu-memory")
        self.assertIsNotNone(go_only.fix_for("HEAD", "/api/resources/graph", resp(500, PY_INF), resp(200, b"")))

    def test_narrow(self):
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(500, b"other"), resp(200, b"{}")))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(500, PY_INF), resp(500, PY_INF)))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources", resp(500, PY_INF), resp(200, b"{}")))
        self.assertIsNone(go_only.fix_for("POST", "/api/resources/graph", resp(500, PY_INF), resp(200, b"{}")))

    def test_entries_documented(self):
        for fix in go_only.FIXES:
            self.assertTrue(fix.id and fix.go_file and fix.note, fix)
            self.assertTrue((Path(__file__).resolve().parents[3] / fix.go_file.split(" ")[0]).is_file(), fix.go_file)


REPO = Path(__file__).resolve().parents[3]


class StorageTest(unittest.TestCase):
    """GPU server storage (ark_studio/server/internal/gpu/storage.go) is a Go-only addition."""

    ITEM = {"mount": "/data", "used_bytes": 9, "total_bytes": 10, "free_bytes": 1, "percent": 90.0, "data": True}

    def test_probe_and_dashboard_strip_storage(self):
        probe = {"enabled": True, "servers": [{"server": "a", "ok": True, "storage": [self.ITEM]},
                                              {"server": "b", "ok": False}], "ranked": []}
        want = {"enabled": True, "servers": [{"server": "a", "ok": True}, {"server": "b", "ok": False}], "ranked": []}
        strip = go_only.augmented("/api/gpu/probe")
        self.assertEqual(json.loads(strip(json.dumps(probe).encode())), want)
        dash = go_only.augmented("/api/gpu/dashboard?project=x")
        self.assertEqual(json.loads(dash(json.dumps({"probe": probe, "jobs": []}).encode())),
                         {"probe": want, "jobs": []})
        self.assertEqual(dash(b'{"probe": null}'), b'{"probe": null}')
        self.assertEqual(strip(b"not json"), b"not json")

    def test_graph_strips_device_storage(self):
        graph = {"devices": [{"id": "gpu:a", "storage": [self.ITEM]}, {"id": "ark"}], "resources": []}
        out = json.loads(go_only.augmented("/api/resources/graph")(json.dumps(graph).encode()))
        self.assertEqual(out, {"devices": [{"id": "gpu:a"}, {"id": "ark"}], "resources": []})

    def test_ssh_suffix_matches_go(self):
        source = (REPO / "ark_studio/server/internal/gpu/storage.go").read_text(encoding="utf-8")
        decl = source.split("const StorageCommand = ", 1)[1].split("\n\n", 1)[0]
        decl = decl.replace('" + storageMarker + "', "__ARK_DF__")
        command = "".join(re.findall(r'"([^"]*)"', decl))
        self.assertEqual("\n" + command, go_only.GO_ONLY_SSH_SUFFIX)
        self.assertIn("` + StorageCommand + `\ntrue`", (REPO / "ark_studio/server/internal/gpu/ssh.go").read_text(encoding="utf-8"))
        argv = ["-p", "22", "--", "a.fake", "bash", "-lc", "'echo __ARK_LOAD__\ndone" + go_only.GO_ONLY_SSH_SUFFIX + "\ntrue'"]
        self.assertEqual(go_only.ssh_call_canonical(argv)[-1], "'echo __ARK_LOAD__\ndone\ntrue'")


def err(status: int, code: str, message: str) -> bytes:
    return json.dumps({"ok": False, "error": {"status": status, "code": code, "message": message}}).encode()


def body(obj) -> bytes:
    return json.dumps(obj).encode()


class GpuScopeTest(unittest.TestCase):
    """T-0018 (ark_studio/server/internal/gpu/scope.go): project scope and member view."""

    def test_not_registered(self):
        py = resp(400, err(400, "unknown_server", "Unknown server(s): zzz"))
        go = resp(404, err(404, "GPU_SERVER_NOT_REGISTERED", "GPU server 'zzz' is not registered in this project."))
        for method, path in (("POST", "/api/gpu/probe"), ("POST", "/api/gpu/dispatch/prepare"),
                             ("POST", "/api/gpu/jobs"), ("POST", "/api/task-agent/v1/gpu/jobs")):
            self.assertEqual(go_only.fix_for(method, path, py, go).id, "gpu-server-not-registered")
        queue = resp(400, err(400, "queue_invalid", "unknown server 'nope'"))
        self.assertIsNotNone(go_only.fix_for("POST", "/api/gpu/jobs", queue, go))
        # Narrow: another 400, another 404 code, another route, another status.
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", resp(400, err(400, "invalid_request", "bad")), go))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py, resp(404, err(404, "GPU_JOB_NOT_FOUND", "x"))))
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/jobs", py, go))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py,
                                          resp(403, err(403, "GPU_SERVER_NOT_REGISTERED", "x"))))

    PY_SERVERS = {"available": True, "enabled": True, "config_path": "/d1/private/gpu_servers.yaml",
                  "servers": [{"name": "a", "host": "a.fake", "port": 2222, "user": "replay", "tags": ["4090"],
                               "note": "n", "enabled": True, "timeout_seconds": 8}],
                  "count": 1, "defaults": {"port": 2222}, "checked_at": "t1"}
    GO_SERVERS = {"available": True, "enabled": True,
                  "servers": [{"name": "a", "tags": ["4090"], "enabled": True}],
                  "count": 1, "checked_at": "t2", "registration": {"registered": ["a"]}}

    def test_member_view(self):
        py, go = resp(200, body(self.PY_SERVERS)), resp(200, body(self.GO_SERVERS))
        self.assertEqual(go_only.fix_for("GET", "/api/gpu/servers", py, go).id, "gpu-member-view")
        # The admin/local view (address kept) is not a member view.
        local = dict(self.PY_SERVERS, checked_at="t2")
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", py, resp(200, body(local))))
        # Narrow: a config path left anywhere, an extra member, a lost server, a changed flag.
        for bad in (dict(self.GO_SERVERS, config_path="/x"), dict(self.GO_SERVERS, extra=1),
                    dict(self.GO_SERVERS, servers=[]),
                    dict(self.GO_SERVERS, servers=[{"name": "a", "tags": ["4090"], "enabled": False}]),
                    dict(self.GO_SERVERS, enabled=False)):
            self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", py, resp(200, body(bad))), bad)
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/servers", resp(500, b"{}"), go))
        self.assertIsNone(go_only.fix_for("GET", "/api/gpu/jobs", py, go))

    def test_member_dispatch_and_graph(self):
        py = {"server": {"name": "a", "host": "a.fake", "port": 22, "user": "u", "tags": [], "enabled": True},
              "transport": {"host": "a.fake", "port": 22, "user": "u", "auth": "agent"},
              "ssh_preview": "ssh -p 22 u@a.fake", "config_path": "/d1/x", "request": {"server": "a"}}
        go = {"server": {"name": "a", "tags": [], "enabled": True}, "transport": {"auth": "agent"},
              "request": {"server": "a"}}
        self.assertIsNotNone(go_only.fix_for("POST", "/api/gpu/dispatch/prepare", resp(200, body(py)),
                                             resp(200, body(go))))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/dispatch/prepare", resp(200, body(py)),
                                          resp(200, body(dict(go, ssh_preview="ssh a")))))
        gpy = {"devices": [{"id": "gpu:a", "detail": {"host": "a.fake", "user": "u", "tags": []},
                            "status": {"state": "online"}}]}
        ggo = {"devices": [{"id": "gpu:a", "detail": {"host": None, "user": None, "tags": []},
                            "status": {"state": "online"}}]}
        self.assertIsNotNone(go_only.fix_for("GET", "/api/resources/graph", resp(200, body(gpy)), resp(200, body(ggo))))
        self.assertIsNone(go_only.fix_for("GET", "/api/resources/graph", resp(200, body(gpy)),
                                          resp(200, body({"devices": []}))))

    def test_member_config_error(self):
        py = resp(403, err(403, "disabled", "GPU probing is disabled by default. Set 'enabled: true' in /d1/g.yaml"))
        go = resp(403, err(403, "disabled", go_only.GPU_MEMBER_ERRORS["disabled"]))
        self.assertEqual(go_only.fix_for("POST", "/api/gpu/probe", py, go).id, "gpu-member-config-error")
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py, resp(403, err(403, "disabled", "other"))))
        self.assertIsNone(go_only.fix_for("POST", "/api/gpu/probe", py,
                                          resp(409, err(409, "disabled", go_only.GPU_MEMBER_ERRORS["disabled"]))))

    def test_discover_names_only(self):
        def found(note):
            item = {"id": "a", "kind": "ssh", "gpu_server": "a", "use": "ask"}
            if note is not None:
                item["note"] = note
            return {"ok": True, "draft": {"compute": [{"id": "local", "kind": "local", "note": "Windows host"}, item]}}
        path = "/api/agent/v1/setup/discover"
        py, go = resp(200, body(found("4090, x"))), resp(200, body(found("")))
        self.assertEqual(go_only.fix_for("POST", path, py, go).id, "setup-discover-gpu-names-only")
        self.assertIsNotNone(go_only.fix_for("POST", path, py, resp(200, body(found(None)))))
        self.assertIsNone(go_only.fix_for("POST", path, py, resp(200, body(found("4090, x")))))
        extra = found("")
        extra["draft"]["compute"].append({"id": "b", "kind": "ssh", "gpu_server": "b"})
        self.assertIsNone(go_only.fix_for("POST", path, py, resp(200, body(extra))))

    def test_overview_unregistered(self):
        def ov(servers, free):
            return {"project": {"id": "p"}, "gpu": {"available": True, "enabled": True, "servers": servers,
                                                     "free_gpus": free, "jobs": {}}}
        path = "/api/project/overview"
        self.assertEqual(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(ov(0, None)))).id,
                         "gpu-overview-unregistered")
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(ov(3, 2)))))
        other = ov(0, None)
        other["project"]["extra"] = 1
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(4, 2))), resp(200, body(other))))
        self.assertIsNone(go_only.fix_for("GET", path, resp(200, body(ov(0, 2))), resp(200, body(ov(0, None)))))

    def test_registration_is_augmented(self):
        strip = go_only.augmented("/api/gpu/servers")
        self.assertIsNone(json.loads(strip(body(self.GO_SERVERS))).get("registration"))
        dash = go_only.augmented("/api/gpu/dashboard")
        out = json.loads(dash(body({"probe": {"servers": [{"server": "a", "storage": []}]}, "jobs": [],
                                    "registration": {"hint_code": "not_registered"}})))
        self.assertEqual(out, {"probe": {"servers": [{"server": "a"}]}, "jobs": []})

    def test_replay_registers_its_servers(self):
        import replay_gpu
        steps = replay_gpu.gpu_steps({"tokens": {"server": "s", "full_token": "f", "read_token": "r"}})
        calls = [s for s in steps if s[0] == replay_gpu.CALL]
        self.assertTrue(all(hasattr(s[3], "gpu_names") for s in calls))
        self.assertIn("q1", calls[-1][3].gpu_names)
        self.assertIn("a", calls[-1][3].gpu_names)  # cumulative
        self.assertEqual(replay_gpu._NAME.findall(replay_gpu.queue_config()), ["q1", "q2", "q3", "qoff"])


if __name__ == "__main__":
    unittest.main()
