"""Personal appearance overrides (palette, graph physics) on the account profile.

Overrides live in ``prefs.appearance`` of ``PATCH /api/me``.  They never touch
the project manifest: project settings and the agent palette endpoint keep
returning project values, and a read-only viewer can still set their own.
"""
from __future__ import annotations

import json
import unittest

from arkstudio import settings as settings_mod
from arkstudio.settings import GRAPH_DEFAULTS, SettingsError

from tests.python.auth.test_org import AuthHttpCase, request

PALETTE = {
    "enabled": True,
    "light": {"preset": "sunset-light"},
    "dark": {"preset": "indigo", "accent": "#ff00aa"},
}
GRAPH = {"repulsion": 9000, "distance": 120, "gravity": 0.002, "damping": 0.6}


class OverrideValidationTests(unittest.TestCase):
    def test_palette_override_is_normalized(self):
        value = settings_mod.personal_palette(PALETTE)
        self.assertEqual(value["enabled"], True)
        self.assertEqual(value["light"]["preset"], "sunset-light")
        self.assertEqual(value["light"]["background"],
                         settings_mod.PALETTE_PRESETS["sunset-light"]["background"])
        self.assertEqual(value["dark"]["accent"], "#ff00aa")
        self.assertEqual(set(value), {"enabled", "light", "dark"})

    def test_invalid_overrides_are_rejected(self):
        bad_palettes = [
            [], {"light": {}}, {"enabled": "yes", "light": {}, "dark": {}},
            {"light": {}, "dark": {}, "saved": {}},
            {"light": {"preset": "nope"}, "dark": {}},
            {"light": {"background": "#000000"}, "dark": {}},
            {"light": {"accent": "red"}, "dark": {}},
            {"light": {"script": "#000000"}, "dark": {}},
        ]
        for value in bad_palettes:
            with self.subTest(palette=value), self.assertRaises(SettingsError):
                settings_mod.personal_palette(value)
        for value in [{}, {"repulsion": 1}, {**GRAPH, "repulsion": -1},
                      {**GRAPH, "gravity": True}, {**GRAPH, "extra": 1}, "x"]:
            with self.subTest(graph=value), self.assertRaises(SettingsError):
                settings_mod.personal_graph(value)
        with self.assertRaises(SettingsError):
            settings_mod.update_personal_appearance({}, {"mind": {}})
        with self.assertRaises(SettingsError):
            settings_mod.update_personal_appearance({}, [])

    def test_merge_and_clear(self):
        merged = settings_mod.update_personal_appearance({}, {"palette": PALETTE})
        self.assertEqual(set(merged), {"palette"})
        merged = settings_mod.update_personal_appearance(merged, {"graph": GRAPH})
        self.assertEqual(set(merged), {"palette", "graph"})
        self.assertEqual(merged["graph"], {k: float(v) for k, v in GRAPH.items()})
        cleared = settings_mod.update_personal_appearance(merged, {"palette": None})
        self.assertEqual(set(cleared), {"graph"})
        # Stored garbage is dropped on read instead of breaking the profile.
        self.assertEqual(settings_mod.personal_appearance({"palette": "x", "graph": GRAPH}),
                         {"graph": {k: float(v) for k, v in GRAPH.items()}})


class ProfileOverrideApiTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("vera", "vera-pass", role="viewer").status, 200)
        self.vera = self.login("vera", "vera-pass")
        self.project = self.project_id()
        self.assertEqual(self.grant(self.project, user="vera", permission="read").status, 200)

    def patch(self, cookie, body):
        return self.api("/api/me", cookie=cookie, method="PATCH", body=body)

    def project_settings(self, cookie=None):
        response = self.api("/api/project/settings", cookie=cookie or self.owner)
        self.assertEqual(response.status, 200, response.text)
        return response.json()["settings"]

    def test_overrides_persist_per_user_and_never_touch_the_project(self):
        before = self.project_settings()
        response = self.patch(self.owner, {"prefs": {"appearance": {"palette": PALETTE, "graph": GRAPH}}})
        self.assertEqual(response.status, 200, response.text)
        mine = response.json()["profile"]["prefs"]["appearance"]
        self.assertEqual(mine["palette"]["light"]["preset"], "sunset-light")
        self.assertEqual(mine["graph"]["repulsion"], 9000.0)
        # Stored with the profile: a fresh GET and the bootstrap both return it.
        self.assertEqual(self.api("/api/me", cookie=self.owner).json()["profile"]["prefs"]["appearance"], mine)
        self.assertEqual(self.api("/api/bootstrap", cookie=self.owner).json()["profile"]["prefs"]["appearance"], mine)
        # Project values are unchanged, for this user and everyone else.
        self.assertEqual(self.project_settings(), before)
        self.assertEqual(self.project_settings(self.vera)["palette"], before["palette"])
        # Another account has no override.
        self.assertNotIn("appearance", self.api("/api/me", cookie=self.vera).json()["profile"]["prefs"])
        # Other prefs survive an appearance patch and vice versa.
        self.assertEqual(self.patch(self.owner, {"prefs": {"theme": "dark"}}).status, 200)
        prefs = self.api("/api/me", cookie=self.owner).json()["profile"]["prefs"]
        self.assertEqual(prefs["theme"], "dark")
        self.assertEqual(prefs["appearance"], mine)
        # Clearing one group keeps the other; clearing both removes the key.
        partial = self.patch(self.owner, {"prefs": {"appearance": {"palette": None}}}).json()
        self.assertEqual(set(partial["profile"]["prefs"]["appearance"]), {"graph"})
        empty = self.patch(self.owner, {"prefs": {"appearance": {"graph": None}}}).json()
        self.assertNotIn("appearance", empty["profile"]["prefs"])

    def test_invalid_override_is_rejected_and_keeps_the_previous_value(self):
        self.assertEqual(self.patch(self.owner, {"prefs": {"appearance": {"graph": GRAPH}}}).status, 200)
        for body in ({"palette": {"light": {}}}, {"graph": {"repulsion": 1}}, {"mind": {}}, [],
                     {"palette": {**PALETTE, "light": {"background": "#000000"}}}):
            with self.subTest(body=body):
                response = self.patch(self.owner, {"prefs": {"appearance": body}})
                self.assertEqual(response.status, 400, response.text)
                self.assertEqual(response.json()["error"]["code"], "PROFILE_INVALID")
        kept = self.api("/api/me", cookie=self.owner).json()["profile"]["prefs"]["appearance"]
        self.assertEqual(set(kept), {"graph"})

    def test_read_only_viewer_can_set_a_personal_override(self):
        # The viewer cannot edit project settings ...
        denied = self.api("/api/project/settings", cookie=self.vera, method="PATCH",
                          body={"palette": {"enabled": True}})
        self.assertIn(denied.status, (401, 403), denied.text)
        # ... but can override the look for themselves.
        response = self.patch(self.vera, {"prefs": {"appearance": {"palette": PALETTE}}})
        self.assertEqual(response.status, 200, response.text)
        self.assertEqual(response.json()["profile"]["prefs"]["appearance"]["palette"]["dark"]["accent"],
                         "#ff00aa")

    def test_agent_palette_endpoint_keeps_returning_project_values(self):
        minted = self.api("/api/personal/agent-tokens", cookie=self.owner, method="POST",
                          body={"label": "palette"})
        self.assertEqual(minted.status, 200, minted.text)
        token = minted.json()["token"]
        headers = {"Host": self.host, "Authorization": f"Bearer {token}",
                   "Content-Type": "application/json"}
        before = request(self.base, "/api/agent/v1/project/palette", headers=headers)
        self.assertEqual(before.status, 200, before.text)
        self.assertEqual(self.patch(self.owner, {"prefs": {"appearance": {"palette": PALETTE}}}).status, 200)
        after = request(self.base, "/api/agent/v1/project/palette", headers=headers)
        self.assertEqual(after.json()["palette"], before.json()["palette"])
        self.assertEqual(after.json()["palette"], self.project_settings()["palette"])
        # Writing the project palette through the agent API leaves the override alone.
        patched = request(self.base, "/api/agent/v1/project/palette", method="POST", headers=headers,
                          body=json.dumps({"enabled": True, "light": {"preset": "forest-light"}}))
        self.assertEqual(patched.status, 200, patched.text)
        self.assertEqual(patched.json()["palette"]["light"]["preset"], "forest-light")
        mine = self.api("/api/me", cookie=self.owner).json()["profile"]["prefs"]["appearance"]
        self.assertEqual(mine["palette"]["light"]["preset"], "sunset-light")


if __name__ == "__main__":
    unittest.main()
