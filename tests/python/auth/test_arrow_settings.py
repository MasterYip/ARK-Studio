"""Graph line-end (arrowhead) settings: project section and personal override.

The project value lives in ``settings.arrows`` of the manifest (PATCH
/api/project/settings); a person may override it in ``prefs.appearance.arrows``
(PATCH /api/me).  The personal value wins for that person only.
"""
from __future__ import annotations

import unittest

from arkstudio import settings as settings_mod
from arkstudio.settings import ARROW_DEFAULTS, SettingsError

from tests.python.auth.test_org import AuthHttpCase

MINE = {"style": {"head": "open", "placement": "both", "size": 1.5},
        "relations": {"part_of": {"placement": "none"}}, "mind_tree": True}


class ArrowValidationTests(unittest.TestCase):
    def test_defaults_keep_current_rendering(self):
        self.assertEqual(ARROW_DEFAULTS["style"], {"head": "triangle", "placement": "target", "size": 1.0})
        # Symmetric relations (reverse label == label) carry no arrow.
        self.assertEqual(ARROW_DEFAULTS["relations"],
                         {"relates_to": {"placement": "none"}, "unset": {"placement": "none"}})
        self.assertFalse(ARROW_DEFAULTS["mind_tree"])
        self.assertEqual(settings_mod.project_settings({})["arrows"], ARROW_DEFAULTS)

    def test_patch_merges_style_and_replaces_relations(self):
        value = settings_mod.update_arrows(None, {"style": {"head": "diamond"}})
        self.assertEqual(value["style"], {"head": "diamond", "placement": "target", "size": 1.0})
        self.assertEqual(value["relations"], ARROW_DEFAULTS["relations"])
        value = settings_mod.update_arrows(value, {
            "style": {"size": "2"}, "relations": {"depends_on": {"head": "diamond_filled", "size": 0.75}},
            "mind_tree": True})
        self.assertEqual(value["style"]["size"], 2.0)
        self.assertEqual(value["style"]["head"], "diamond")
        self.assertEqual(value["relations"], {"depends_on": {"head": "diamond_filled", "size": 0.75}})
        self.assertTrue(value["mind_tree"])
        # Empty per-relation entries are dropped.
        self.assertEqual(settings_mod.update_arrows(None, {"relations": {"uses": {}}})["relations"], {})

    def test_invalid_patches_are_rejected(self):
        bad = [
            [], {"color": "red"}, {"style": []}, {"style": {"head": "spiral"}},
            {"style": {"placement": "middle"}}, {"style": {"size": 0.1}}, {"style": {"size": 3}},
            {"style": {"size": True}}, {"style": {"size": "big"}}, {"style": {"size": float("nan")}},
            {"style": {"width": 2}}, {"relations": []}, {"relations": {"loves": {"head": "bar"}}},
            {"relations": {"supports": {"head": "x"}}}, {"relations": {"supports": "bar"}},
            {"mind_tree": "yes"},
        ]
        for patch in bad:
            with self.subTest(patch=patch), self.assertRaises(SettingsError):
                settings_mod.update_arrows(None, patch)

    def test_relations_may_reverse_the_shown_direction(self):
        self.assertEqual(settings_mod.ARROW_REVERSED_BY_DEFAULT, ("derived_from", "depends_on"))
        value = settings_mod.update_arrows(None, {"relations": {
            "derived_from": {"reverse": False}, "cites": {"reverse": True, "head": "bar"}}})
        self.assertEqual(value["relations"], {"derived_from": {"reverse": False},
                                              "cites": {"head": "bar", "reverse": True}})
        for patch in ({"style": {"reverse": True}}, {"relations": {"cites": {"reverse": "yes"}}},
                      {"relations": {"cites": {"reverse": 1}}}):
            with self.subTest(patch=patch), self.assertRaises(SettingsError):
                settings_mod.update_arrows(None, patch)
        # Stored garbage is dropped; personal overrides accept the same field.
        self.assertEqual(settings_mod.normalize_arrows({"relations": {"cites": {"reverse": "x"}}})["relations"], {})
        mine = settings_mod.personal_arrows({**MINE, "relations": {"uses": {"reverse": True}}})
        self.assertEqual(mine["relations"], {"uses": {"reverse": True}})

    def test_stored_garbage_is_read_leniently(self):
        value = settings_mod.normalize_arrows({
            "style": {"head": "spiral", "size": 9, "placement": "both"},
            "relations": {"loves": {"head": "bar"}, "supports": {"head": "bar", "size": "x"}},
            "mind_tree": "yes"})
        self.assertEqual(value, {"style": {"head": "triangle", "placement": "both", "size": 1.0},
                                 "relations": {"supports": {"head": "bar"}}, "mind_tree": False})

    def test_settings_patch_section(self):
        current = settings_mod.settings_patch({}, {"arrows": {"style": {"head": "bar"}}})
        self.assertEqual(current["arrows"]["style"]["head"], "bar")
        # Other sections are untouched; a bare physics patch still works.
        self.assertEqual(current["graph"], settings_mod.project_settings({})["graph"])
        manifest = {"settings": current}
        physics = settings_mod.settings_patch(manifest, {"damping": 0.5})
        self.assertEqual(physics["arrows"]["style"]["head"], "bar")
        with self.assertRaises(SettingsError):
            settings_mod.settings_patch({}, {"arrows": {"style": {"head": "x"}}})

    def test_personal_override_needs_a_complete_style(self):
        self.assertEqual(settings_mod.personal_arrows(MINE)["style"]["head"], "open")
        # Missing parts fall back to the defaults, not to the project.
        only_style = settings_mod.personal_arrows({"style": MINE["style"]})
        self.assertEqual(only_style["relations"], ARROW_DEFAULTS["relations"])
        for value in [{}, "x", {"style": {"head": "open"}}, {"relations": {}},
                      {**MINE, "style": {**MINE["style"], "head": "x"}}, {**MINE, "extra": 1}]:
            with self.subTest(value=value), self.assertRaises(SettingsError):
                settings_mod.personal_arrows(value)
        merged = settings_mod.update_personal_appearance({}, {"arrows": MINE})
        self.assertEqual(set(merged), {"arrows"})
        self.assertEqual(settings_mod.update_personal_appearance(merged, {"arrows": None}), {})
        self.assertEqual(settings_mod.personal_appearance({"arrows": {"style": "x"}}), {})


class ArrowSettingsApiTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("vera", "vera-pass", role="viewer").status, 200)
        self.vera = self.login("vera", "vera-pass")
        self.assertEqual(self.grant(self.project_id(), user="vera", permission="read").status, 200)

    def settings(self, cookie=None):
        response = self.api("/api/project/settings", cookie=cookie or self.owner)
        self.assertEqual(response.status, 200, response.text)
        return response.json()["settings"]

    def test_project_arrows_are_saved_and_validated(self):
        self.assertEqual(self.settings()["arrows"], ARROW_DEFAULTS)
        body = {"arrows": {"style": {"head": "stealth", "size": 1.25},
                           "relations": {"depends_on": {"head": "diamond"}}}}
        saved = self.api("/api/project/settings", cookie=self.owner, method="PATCH", body=body)
        self.assertEqual(saved.status, 200, saved.text)
        arrows = saved.json()["settings"]["arrows"]
        self.assertEqual(arrows["style"], {"head": "stealth", "placement": "target", "size": 1.25})
        self.assertEqual(self.settings(self.vera)["arrows"], arrows)
        bad = self.api("/api/project/settings", cookie=self.owner, method="PATCH",
                       body={"arrows": {"style": {"head": "spiral"}}})
        self.assertEqual(bad.status, 400, bad.text)
        self.assertEqual(self.settings()["arrows"], arrows)
        denied = self.api("/api/project/settings", cookie=self.vera, method="PATCH", body=body)
        self.assertIn(denied.status, (401, 403), denied.text)

    def test_personal_override_is_per_user(self):
        before = self.settings()
        response = self.api("/api/me", cookie=self.vera, method="PATCH",
                            body={"prefs": {"appearance": {"arrows": MINE}}})
        self.assertEqual(response.status, 200, response.text)
        mine = response.json()["profile"]["prefs"]["appearance"]["arrows"]
        self.assertEqual(mine["style"], {"head": "open", "placement": "both", "size": 1.5})
        self.assertTrue(mine["mind_tree"])
        self.assertEqual(self.settings(), before)
        self.assertNotIn("appearance", self.api("/api/me", cookie=self.owner).json()["profile"]["prefs"])
        bad = self.api("/api/me", cookie=self.vera, method="PATCH",
                       body={"prefs": {"appearance": {"arrows": {"style": {"head": "open"}}}}})
        self.assertEqual(bad.status, 400, bad.text)
        cleared = self.api("/api/me", cookie=self.vera, method="PATCH",
                           body={"prefs": {"appearance": {"arrows": None}}})
        self.assertNotIn("appearance", cleared.json()["profile"]["prefs"])


if __name__ == "__main__":
    unittest.main()
