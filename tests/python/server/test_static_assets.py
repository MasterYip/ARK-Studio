import concurrent.futures
import gzip
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from arkstudio.static_assets import StaticAssets, accepts_gzip


class StaticAssetsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "app.js"
        self.path.write_text("import './state.js';\n" * 100, encoding="utf-8")

    def test_representations_preserve_bytes_and_prepare_once(self):
        cache = StaticAssets()
        transform = mock.Mock(side_effect=lambda text, version: text + version)
        identity, encoding = cache.get(self.path, "release-1", transform)
        self.assertIsNone(encoding)
        with mock.patch("arkstudio.static_assets.gzip.compress", wraps=gzip.compress) as compress:
            compressed, encoding = cache.get(self.path, "release-1", transform, use_gzip=True)
            again, _ = cache.get(self.path, "release-1", transform, use_gzip=True)
            self.assertEqual(encoding, "gzip")
            self.assertEqual(gzip.decompress(compressed), identity)
            self.assertEqual(again, compressed)
            compress.assert_called_once()
        transform.assert_called_once()
        self.assertEqual(cache.get(self.path, "release-1", transform)[0], identity)

    def test_version_and_file_edits_invalidate_even_with_same_size(self):
        cache = StaticAssets()
        transform = mock.Mock(side_effect=lambda text, version: text + version)
        first, _ = cache.get(self.path, "one", transform)
        second, _ = cache.get(self.path, "two", transform)
        self.assertNotEqual(first, second)
        stamp = self.path.stat().st_mtime_ns
        self.path.write_text("export './state.js';\n" * 100, encoding="utf-8")
        os.utime(self.path, ns=(stamp + 1_000_000, stamp + 1_000_000))
        edited, _ = cache.get(self.path, "two", transform)
        self.assertIn(b"export", edited)
        self.assertEqual(transform.call_count, 3)

    def test_bounds_and_lru_eviction(self):
        cache = StaticAssets(max_bytes=250, max_entries=2)
        paths = [Path(self.tmp.name) / f"{index}.css" for index in range(3)]
        for path in paths:
            path.write_bytes(b"x" * 100)
        cache.get(paths[0], "v")
        cache.get(paths[1], "v")
        cache.get(paths[0], "v")  # retain the recently reused entry
        cache.get(paths[2], "v")
        self.assertEqual(list(cache._entries), [paths[0], paths[2]])
        self.assertEqual(cache._bytes, 200)
        paths[0].write_bytes(b"y" * 200)
        cache.get(paths[0], "v")
        self.assertLessEqual(cache._bytes, 250)
        self.assertEqual(cache._bytes, sum(entry.size for entry in cache._entries.values()))

    def test_oversized_and_incompressible_entries(self):
        cache = StaticAssets(max_source_bytes=50)
        cache.get(self.path, "v", use_gzip=True)
        self.assertEqual(cache._bytes, 0)
        self.assertEqual(len(cache._entries), 0)
        self.path.write_bytes(os.urandom(2048))
        cache = StaticAssets()
        data, encoding = cache.get(self.path, "v", use_gzip=True)
        self.assertEqual(data, self.path.read_bytes())
        self.assertIsNone(encoding)

    def test_simultaneous_requests_share_preparation(self):
        cache = StaticAssets()
        transform = mock.Mock(side_effect=lambda text, version: text + version)
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            results = list(pool.map(lambda _: cache.get(self.path, "v", transform, use_gzip=True), range(20)))
        self.assertTrue(all(result == results[0] for result in results))
        transform.assert_called_once()

    def test_gzip_negotiation_respects_explicit_refusal(self):
        for value in ("gzip", "br, gzip", "GZIP; q=0.5", "*;q=0.8"):
            self.assertTrue(accepts_gzip(value), value)
        for value in ("", "identity", "xgzip", "gzip;q=0", "gzip;q=0, *;q=1",
                      "gzip;q=invalid", "gzip;q=-1", "gzip;q=2"):
            self.assertFalse(accepts_gzip(value), value)


if __name__ == "__main__":
    unittest.main()
