"""go_only.FIXES predicates stay narrow (no arkstudio import)."""
from __future__ import annotations

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


if __name__ == "__main__":
    unittest.main()
