import unittest

from arkstudio.settings import (PALETTE_PRESETS, SettingsError, normalize_palette,
                                palette_mode, update_palette)


class PaletteTests(unittest.TestCase):
    def test_legacy_palette_migrates_to_matching_mode(self):
        for preset in PALETTE_PRESETS:
            value = normalize_palette({"enabled": True, "preset": preset})
            mode = palette_mode(PALETTE_PRESETS[preset])
            self.assertEqual(value[mode]["preset"], preset)
            self.assertEqual(value[mode]["background"], PALETTE_PRESETS[preset]["background"])
            self.assertEqual(palette_mode(value["light"]), "light")
            self.assertEqual(palette_mode(value["dark"]), "dark")

    def test_modes_merge_independently_and_survive_normalization(self):
        initial = normalize_palette(None)
        value = update_palette(initial, {"enabled": True, "dark": {"preset": "indigo"},
                                         "light": {"preset": "forest-light"}})
        self.assertEqual(value["dark"]["preset"], "indigo")
        self.assertEqual(value["light"]["preset"], "forest-light")
        changed = update_palette(value, {"light": {"text": "#102030", "preset": "custom"}})
        self.assertEqual(changed["dark"], value["dark"])
        self.assertEqual(changed["light"]["text"], "#102030")
        self.assertEqual(normalize_palette(changed), changed)

    def test_old_agent_patch_keeps_other_mode(self):
        initial = update_palette(None, {"light": {"preset": "forest-light"}})
        changed = update_palette(initial, {"preset": "indigo"})
        self.assertEqual(changed["dark"]["preset"], "indigo")
        self.assertEqual(changed["light"], initial["light"])

    def test_reject_invalid_color_and_mismatched_mode(self):
        for patch in ({"dark": "bad"}, {"light": {"preset": "indigo"}},
                      {"dark": {"text": "red"}}, {"preset": "missing"}):
            with self.subTest(patch=patch), self.assertRaises(SettingsError):
                update_palette(None, patch)

    def test_named_themes_round_trip_merge_apply_and_delete(self):
        active = update_palette(None, {"light": {"preset": "forest-light", "accent": "#123456"},
                                       "dark": {"preset": "indigo"}})
        pair = {mode: active[mode] for mode in ("light", "dark")}
        saved = update_palette(active, {"saved": {"Research": pair}})
        saved = update_palette(saved, {"saved": {"Second": pair}})
        self.assertEqual(normalize_palette(saved), saved)
        changed = update_palette(saved, {"preset": "sunset"})
        self.assertEqual(changed["saved"], saved["saved"])
        restored = update_palette(changed, changed["saved"]["Research"])
        self.assertEqual(restored["light"]["accent"], "#123456")
        self.assertEqual(restored["dark"]["preset"], "indigo")
        deleted = update_palette(restored, {"saved": {"Research": None}})
        self.assertEqual(set(deleted["saved"]), {"Second"})
        self.assertEqual(deleted["light"], restored["light"])

    def test_named_themes_validate_names_colors_and_only_theme_data(self):
        pair = {"light": {"preset": "ark-light"}, "dark": {"preset": "ark"}}
        invalid = [None, [], {"": pair}, {"__proto__": pair}, {"x" * 65: pair},
                   {"a\nb": pair}, {" bad ": pair}, {"bad": {}},
                   {"bad": {**pair, "token": "private"}},
                   {"bad": {**pair, "light": {"password": "private"}}},
                   {"bad": {**pair, "dark": {"text": "red"}}},
                   {"bad": {**pair, "light": {"preset": "indigo"}}},
                   {str(i): pair for i in range(25)}]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(SettingsError):
                update_palette(None, {"saved": value})
        self.assertEqual(normalize_palette({"saved": {"bad": {}}})["saved"], {})
