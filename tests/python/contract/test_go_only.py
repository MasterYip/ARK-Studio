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


if __name__ == "__main__":
    unittest.main()
