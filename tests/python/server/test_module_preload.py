"""index.html lists the entry module's static import graph (boot.js preloads it).

The list is computed from web/js at serve time (no build step), follows only
static imports, and uses the same versioned URLs the modules import.
"""
from __future__ import annotations

import json
import re
import tempfile
import unittest
from pathlib import Path

from arkstudio import server as server_mod
from arkstudio.static_assets import module_graph, static_imports


class ModuleGraphTests(unittest.TestCase):
    def test_static_imports_only(self):
        source = (
            'import { a } from "./a.js";\n'
            'import "./side.js";\n'
            'export { b } from "../lib/b.js";\n'
            "export * from './c.js';\n"
            'const later = () => import("./lazy.js");\n'
            '/* import { x } from "./commented.js"; */\n'
            'const text = "from ./not-a-module.js";\n'
        )
        self.assertEqual(static_imports(source), ["./a.js", "./side.js", "../lib/b.js", "./c.js"])

    def test_graph_is_breadth_first_unique_and_skips_dynamic(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "js" / "views").mkdir(parents=True)
            (root / "vendor").mkdir()
            (root / "js" / "app.js").write_text(
                'import "./a.js";\nimport { v } from "./views/v.js";\nimport("./lazy.js");\n')
            (root / "js" / "a.js").write_text('import "../vendor/lib.js";\nimport "./missing.js";\n')
            (root / "js" / "views" / "v.js").write_text('import { a } from "../a.js";\n')
            (root / "js" / "lazy.js").write_text('import "./a.js";\n')
            (root / "vendor" / "lib.js").write_text("export const x = 1;\n")
            self.assertEqual(module_graph(root, "js/app.js"),
                             ["js/a.js", "js/views/v.js", "vendor/lib.js"])

    def test_index_lists_the_real_app_graph_with_versioned_urls(self):
        version = "test1"
        html = server_mod._index_html((server_mod.WEB_ROOT / "index.html").read_text(encoding="utf-8"), version)
        listed = re.search(r'<script type="application/json" id="ark-modules">(.*?)</script>', html)
        self.assertIsNotNone(listed)
        hrefs = json.loads(listed.group(1))
        self.assertEqual(hrefs[0], "js/app.js")
        self.assertEqual(hrefs[1:], module_graph(server_mod.WEB_ROOT, "js/app.js"))
        self.assertNotIn("ark:modulepreload", html)
        self.assertEqual(len(hrefs), len(set(hrefs)))
        # Dynamically imported views stay out of the first-screen list.
        self.assertNotIn("js/views/settings.js", hrefs)


if __name__ == "__main__":
    unittest.main()
