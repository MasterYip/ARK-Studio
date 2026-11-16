"""Block ``import arkstudio`` in Go-only test runs.

The Python server is frozen (deprecated reference and rollback path); the
Go-only contract suite (harness.py against ark-server, golden.py) must not
depend on it.  ``install()`` makes any later ``import arkstudio`` raise
ImportError, so an accidental dependency fails loudly instead of silently
using the Python code.
"""
from __future__ import annotations

import importlib.abc
import sys


class _Blocker(importlib.abc.MetaPathFinder):
    def find_spec(self, name, path=None, target=None):
        if name == "arkstudio" or name.startswith("arkstudio."):
            raise ImportError(f"{name}: the Go-only contract suite must not import the Python server")
        return None


def install() -> None:
    if not any(isinstance(f, _Blocker) for f in sys.meta_path):
        sys.meta_path.insert(0, _Blocker())
    for name in [n for n in sys.modules if n == "arkstudio" or n.startswith("arkstudio.")]:
        raise ImportError(f"{name} was already imported before noarkstudio.install()")
