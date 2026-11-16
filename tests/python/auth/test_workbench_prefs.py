"""Workbench layout presets on the account profile (prefs.workbench)."""
from __future__ import annotations

import copy
import json
import unittest

from arkstudio import workbench_prefs as wp

from tests.python.auth.test_org import AuthHttpCase

LAYOUT = {"v": 1, "containers": {
    "left": {"views": ["explorer", "search"], "active": "explorer", "visible": True, "size": 300},
    "right": {"views": ["details", "localgraph"], "active": "localgraph", "visible": False, "size": 430},
    "bottom": {"views": ["terminal"], "active": "terminal", "visible": True, "size": 260, "maximized": True},
}}
VALUE = {"presets": [{"id": "u:my-writing", "name": " My writing ", "layout": LAYOUT}], "default": "u:my-writing"}


class ValidationTests(unittest.TestCase):
    def test_valid_value_is_normalized(self):
        clean = wp.workbench_prefs(VALUE, strict=True)
        self.assertEqual(clean["presets"][0]["name"], "My writing")
        self.assertEqual(clean["presets"][0]["layout"]["containers"]["bottom"]["maximized"], True)
        self.assertNotIn("maximized", clean["presets"][0]["layout"]["containers"]["left"])
        self.assertEqual(wp.workbench_prefs({"presets": [], "default": "focus"}, strict=True),
                         {"presets": [], "default": "focus"})

    def test_invalid_values_are_rejected(self):
        def changed(fn):
            value = copy.deepcopy(VALUE)
            fn(value)
            return value
        bad = [
            [], {"presets": "x"}, {"presets": [], "extra": 1}, {"presets": [], "default": "nope"},
            changed(lambda v: v["presets"][0].update(id="mine")),
            changed(lambda v: v["presets"][0].update(name="")),
            changed(lambda v: v["presets"][0].update(name="x" * 61)),
            changed(lambda v: v["presets"].append(copy.deepcopy(v["presets"][0]))),
            changed(lambda v: v["presets"][0]["layout"].update(v=2)),
            changed(lambda v: v["presets"][0]["layout"]["containers"].update(top={})),
            changed(lambda v: v["presets"][0]["layout"]["containers"]["left"].update(size=5)),
            changed(lambda v: v["presets"][0]["layout"]["containers"]["left"].update(active="terminal")),
            changed(lambda v: v["presets"][0]["layout"]["containers"]["left"]["views"].append("details")),
            changed(lambda v: v["presets"][0]["layout"]["containers"]["left"]["views"].append("Bad View")),
            changed(lambda v: v["presets"][0]["layout"]["containers"]["left"].update(visible="yes")),
            {"presets": [{"id": f"u:p{i}", "name": "p", "layout": LAYOUT} for i in range(wp.MAX_PRESETS + 1)]},
        ]
        for value in bad:
            with self.subTest(value=str(value)[:80]), self.assertRaises(wp.WorkbenchPrefsError):
                wp.workbench_prefs(value, strict=True)
        # Stored garbage reads as "no presets" instead of breaking the profile.
        self.assertEqual(wp.workbench_prefs({"presets": "x"}), {})


STACKED = {"v": 1, "containers": {
    "left": {"views": ["explorer", "search"], "active": "explorer", "visible": True, "size": 300,
             "sections": [{"views": ["explorer"], "active": "explorer", "size": 320.6},
                          {"views": ["search"], "active": "search", "size": 200, "collapsed": True}]},
    "right": {"views": ["details", "localgraph", "assistant"], "active": "details", "visible": True, "size": 430,
              "sections": [{"views": ["details"], "active": "details", "size": 240, "maximized": True},
                           {"views": ["localgraph", "assistant"], "active": "assistant", "size": 240}]},
    "bottom": {"views": [], "active": "", "visible": False, "size": 260, "sections": []},
}}


class SectionValidationTests(unittest.TestCase):
    def test_stacked_sections_are_kept(self):
        clean = wp.workbench_prefs({"presets": [{"id": "u:s", "name": "S", "layout": STACKED}]}, strict=True)
        boxes = clean["presets"][0]["layout"]["containers"]
        self.assertEqual(boxes["left"]["sections"], [
            {"views": ["explorer"], "active": "explorer", "size": 320},
            {"views": ["search"], "active": "search", "size": 200, "collapsed": True}])
        self.assertTrue(boxes["right"]["sections"][0]["maximized"])
        self.assertEqual(boxes["bottom"]["sections"], [])
        # Layouts saved before sections stay valid and gain nothing.
        legacy = wp.workbench_prefs(VALUE, strict=True)["presets"][0]["layout"]["containers"]
        self.assertNotIn("sections", legacy["left"])

    def test_bad_sections_are_rejected(self):
        def changed(fn):
            layout = copy.deepcopy(STACKED)
            fn(layout["containers"])
            return {"presets": [{"id": "u:s", "name": "S", "layout": layout}]}
        bad = [
            changed(lambda c: c["left"].update(sections="x")),
            changed(lambda c: c["left"]["sections"].reverse()),                      # order differs from views
            changed(lambda c: c["left"]["sections"].pop()),                          # a view without a section
            changed(lambda c: c["left"]["sections"].append({"views": [], "active": ""})),
            changed(lambda c: c["left"]["sections"][0].update(active="search")),
            changed(lambda c: c["left"]["sections"][0].update(size=10)),
            changed(lambda c: c["left"]["sections"][0].update(size=True)),
            changed(lambda c: c["left"]["sections"][0].update(collapsed="yes")),
            changed(lambda c: c["left"]["sections"][0].update(extra=1)),
            changed(lambda c: c["right"]["sections"][1]["views"].append("details")),
        ]
        for value in bad:
            with self.subTest(value=str(value)[:120]), self.assertRaises(wp.WorkbenchPrefsError):
                wp.workbench_prefs(value, strict=True)


class ProfileApiTests(AuthHttpCase):
    def call(self, path, **kwargs):
        res = self.api(path, **kwargs)
        return res.status, res.json()

    def test_save_read_clear_and_reject(self):
        cookie = self.owner
        status, data = self.call("/api/me", cookie=cookie, method="PATCH", body={"prefs": {"workbench": VALUE}})
        self.assertEqual(status, 200, data)
        saved = data["profile"]["prefs"]["workbench"]
        self.assertEqual(saved["default"], "u:my-writing")
        self.assertEqual(saved["presets"][0]["name"], "My writing")
        status, data = self.call("/api/me", cookie=cookie)
        self.assertEqual(data["profile"]["prefs"]["workbench"], saved)
        # Other preferences are untouched by a workbench update and vice versa.
        status, data = self.call("/api/me", cookie=cookie, method="PATCH", body={"prefs": {"lang": "zh"}})
        self.assertEqual(data["profile"]["prefs"]["workbench"], saved)
        status, data = self.call("/api/me", cookie=cookie, method="PATCH",
                                body={"prefs": {"workbench": {"presets": [], "default": "bogus"}}})
        self.assertEqual(status, 400)
        self.assertIn("PROFILE_INVALID", json.dumps(data))
        status, data = self.call("/api/me", cookie=cookie, method="PATCH", body={"prefs": {"workbench": None}})
        self.assertEqual(status, 200)
        self.assertNotIn("workbench", data["profile"]["prefs"])


if __name__ == "__main__":
    unittest.main()
