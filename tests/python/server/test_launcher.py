import os
from pathlib import Path
import unittest
from unittest import mock

from arkstudio import launcher
from arkstudio.launcher import connection_options, remote_address


class LauncherTests(unittest.TestCase):
    def test_explicit_local_connection_cannot_inherit_a_remote_target(self):
        with mock.patch.dict(os.environ, {"ARK_REMOTE_URL": "https://remote.example.test"}):
            options = connection_options("local", "https://saved.example.test", ".", "8891")
        self.assertEqual(options, {"remote_url": "", "server": True, "force_local": True,
                                   "root": str(Path.cwd()), "host": "127.0.0.1", "port": 8891})

    def test_remote_connection_never_starts_a_local_server(self):
        options = connection_options("remote", " https://example.test/ark/ ", ".", "invalid")
        self.assertEqual(options["remote_url"], "https://example.test/ark")
        self.assertFalse(options["server"])

    def test_reject_invalid_urls_and_local_options(self):
        for value in ("example.test", "file:///tmp/test", "https://user:password@example.test",
                      "https://example.test?token=secret", "https://example.test/#route", "https://[bad",
                      "https://example.test:wrong", "https://example.test/a b"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                remote_address(value)
        for port in ("0", "65536", "invalid"):
            with self.subTest(port=port), self.assertRaises(ValueError):
                connection_options("local", "", ".", port)

    def test_native_shadow_is_a_no_op_off_windows(self):
        window = mock.Mock()
        with mock.patch.object(launcher.os, "name", "posix"):
            self.assertEqual(launcher._enable_native_shadow(window), "")
        window.assert_not_called()
        self.assertEqual(window.method_calls, [])

    def test_chooser_has_no_painted_edge(self):
        source = Path(launcher.__file__).read_text(encoding="utf-8")
        self.assertNotIn("#dce2eb", source)
        self.assertIn("_enable_native_shadow(window)", source)

    def test_shadow_mask_is_soft_low_opacity_and_offset_down(self):
        width, height, spread, values = launcher.shadow_alpha(200, 120)
        self.assertEqual((spread, width, height), (30, 260, 180))
        self.assertEqual(len(values), width * height)
        self.assertLessEqual(max(values), round(255 * launcher.SHADOW_PEAK_ALPHA))
        self.assertGreater(max(values), 25)
        row = values[(height // 2) * width:(height // 2 + 1) * width]
        right = row[spread + 200:]
        # Fades smoothly across the whole spread instead of ending in an edge.
        self.assertEqual(right, sorted(right, reverse=True))
        self.assertGreater(right[10], 0)
        self.assertLessEqual(right[-1], 1)
        column = values[width // 2::width]
        self.assertGreater(column[spread + 120 + 6], column[spread - 7])
        self.assertEqual(launcher.shadow_alpha(300, 180, 1.5)[2], 45)

