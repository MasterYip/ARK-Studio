"""Guard against merge-conflict markers left in tracked source files."""
from __future__ import annotations

import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
MARKER = re.compile(r"^(<{7}|>{7})( |$)|^={7}$", re.MULTILINE)
SOURCE_SUFFIXES = {".py", ".js", ".mjs", ".cjs", ".css", ".html", ".json", ".webmanifest",
                   ".go", ".java", ".xml", ".gradle", ".ps1", ".sh", ".cs", ".service", ".yaml", ".yml", ".toml"}


class ConflictMarkerTest(unittest.TestCase):
    def test_no_conflict_markers_in_tracked_sources(self):
        files = subprocess.run(["git", "ls-files"], cwd=ROOT, capture_output=True, text=True,
                               check=True).stdout.splitlines()
        hits = []
        for name in files:
            path = ROOT / name
            if path.suffix not in SOURCE_SUFFIXES or not path.is_file():
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
            for match in MARKER.finditer(text):
                line = text.count("\n", 0, match.start()) + 1
                hits.append(f"{name}:{line}")
        self.assertEqual([], hits, "merge-conflict markers left in: " + ", ".join(hits))


if __name__ == "__main__":
    unittest.main()