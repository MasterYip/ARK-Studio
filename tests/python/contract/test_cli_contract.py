"""The Go CLI's parser snapshot and argv vectors are current (gen_cli.py).

The full command differential (cli_diff.py) needs a built ark-server and
runs separately:  python -B tests/python/contract/cli_diff.py --go go/ark-server.exe
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))


class CliSnapshotTest(unittest.TestCase):
    def test_snapshot_current(self):
        import gen_cli
        self.assertEqual(gen_cli.main(["--check"]), 0,
                         "run python -B tests/python/contract/gen_cli.py")


if __name__ == "__main__":
    unittest.main()
