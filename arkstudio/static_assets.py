"""Bounded representations of repository assets, never API or login responses."""
from __future__ import annotations

import gzip
import posixpath
import re
import threading
from collections import OrderedDict
from dataclasses import dataclass
from pathlib import Path
from typing import Callable


def accepts_gzip(header: str) -> bool:
    qualities = {}
    for item in header.lower().split(","):
        encoding, *parameters = item.strip().split(";")
        encoding = encoding.strip()
        quality = 1.0
        for parameter in parameters:
            key, _, value = parameter.strip().partition("=")
            if key == "q":
                try:
                    quality = float(value)
                except ValueError:
                    quality = 0.0
        qualities[encoding] = quality
    return 0 < qualities.get("gzip", qualities.get("*", 0)) <= 1


@dataclass
class _Entry:
    fingerprint: tuple
    data: bytes
    compressed: bytes | None = None
    compression_checked: bool = False

    @property
    def size(self) -> int:
        return len(self.data) + len(self.compressed or b"")


class StaticAssets:
    def __init__(self, *, max_bytes: int = 16 * 1024 * 1024, max_entries: int = 128,
                 max_source_bytes: int = 2 * 1024 * 1024):
        self.max_bytes = max_bytes
        self.max_entries = max_entries
        self.max_source_bytes = max_source_bytes
        self._entries = OrderedDict()
        self._bytes = 0
        self._lock = threading.Lock()

    def get(self, path: Path, version: str, transform: Callable[[str, str], str] | None = None,
            *, use_gzip: bool = False) -> tuple[bytes, str | None]:
        stat = path.stat()
        fingerprint = (stat.st_mtime_ns, stat.st_ctime_ns, stat.st_size, version)
        # Serialize first preparation too: simultaneous browser requests should
        # not repeat regex rewriting/compression for the same release asset.
        with self._lock:
            entry = self._entries.pop(path, None)
            if entry:
                self._bytes -= entry.size
            if entry is None or entry.fingerprint != fingerprint:
                data = (transform(path.read_text(encoding="utf-8", errors="replace"), version).encode("utf-8")
                        if transform else path.read_bytes())
                entry = _Entry(fingerprint, data)
            if use_gzip and not entry.compression_checked:
                if len(entry.data) >= 1024:
                    compressed = gzip.compress(entry.data, compresslevel=9, mtime=0)
                    if len(compressed) < len(entry.data):
                        entry.compressed = compressed
                entry.compression_checked = True
            if stat.st_size <= self.max_source_bytes and entry.size <= self.max_bytes:
                self._entries[path] = entry
                self._bytes += entry.size
                while self._bytes > self.max_bytes or len(self._entries) > self.max_entries:
                    _, expired = self._entries.popitem(last=False)
                    self._bytes -= expired.size
            return (entry.compressed, "gzip") if use_gzip and entry.compressed else (entry.data, None)


# Static ES-module imports (``import x from "./a.js"``, ``import "./a.js"``,
# ``export ... from "./a.js"``).  Dynamic ``import()`` is deliberately not
# followed: those modules load on demand.
_STATIC_IMPORT = re.compile(
    r"""(?:^|[;\n}])\s*(?:import|export)\s+(?:[^"'();]*?\s+from\s+)?["'](\.{1,2}/[^"'?]+?\.js)["']""",
    re.M)
_BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.S)


def static_imports(source: str) -> list[str]:
    """Relative module specifiers a module imports statically, in order."""
    return _STATIC_IMPORT.findall(_BLOCK_COMMENT.sub("", source))


def module_graph(web_root: Path, entry: str) -> list[str]:
    """Every module the entry module loads before it runs, breadth first.

    Paths are relative to ``web_root`` with forward slashes (``js/dom.js``).
    The entry itself is excluded; files outside the root or missing are
    skipped.  Used to emit ``<link rel="modulepreload">`` for the first screen
    so the browser fetches the whole graph in parallel instead of discovering
    it one import level (one round trip) at a time.
    """
    root = web_root.resolve()
    order: list[str] = []
    seen = {entry}
    queue = [entry]
    while queue:
        current = queue.pop(0)
        try:
            source = (root / current).read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        base = posixpath.dirname(current)
        for spec in static_imports(source):
            rel = posixpath.normpath(posixpath.join(base, spec))
            if rel.startswith("..") or rel in seen or not (root / rel).is_file():
                continue
            seen.add(rel)
            order.append(rel)
            queue.append(rel)
    return order
