"""Repeated setup preserves contributions and exposes every machine path."""
import json
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from arkstudio.skills import setup
from arkstudio.skills import setup_layers as L


class SharedRegisterTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.project = Path(self.tmp.name) / "p.ark"
        self.alice = L.Viewer(user="alice", host_id="alice-laptop")
        self.bob = L.Viewer(user="bob", host_id="bob-workstation")

    def save(self, viewer, rid="code", **kwargs):
        raw = {"resources": [{"id": rid, "kind": "repository", "name": rid,
                "path_or_ref": f"/work/{viewer.user}/{rid}",
                "details": {"role": "code", "default_branch": "main"}}]}
        raw.update(kwargs)
        result = L.save_layer(self.project, raw, viewer=viewer, layer="user",
                              actor=f"agent:{viewer.user}", propose=True)
        self.assertTrue(result["ok"], result)
        return result

    def items(self, viewer):
        return {r["key"]: r for r in L.payload(self.project, viewer)["resources"]}

    def test_every_member_sees_all_resources_and_remote_paths(self):
        self.save(self.alice)
        self.save(self.bob, "assets")
        for viewer in (self.alice, self.bob, L.legacy_viewer()):
            items = self.items(viewer)
            self.assertEqual(set(items), {"u:alice/code", "u:bob/assets"})
            self.assertEqual(items["u:alice/code"]["locations"][0]["path_or_ref"], "/work/alice/code")
            self.assertEqual(items["u:bob/assets"]["owner"], "bob")
        remote = self.items(self.bob)["u:alice/code"]
        self.assertFalse(remote["usable_here"])
        self.assertFalse(remote["access_verified"])
        self.assertIsNone(remote["location"])

    def test_repeated_setup_merges_resources_and_keeps_confirmation_of_other_user(self):
        result = self.save(self.alice)
        L.confirm_layer(self.project, viewer=self.alice, layer="user",
                        actor="human:alice", digest_value=result["digest"])
        before = L.load_user(self.project, "alice")
        before_missing = L.payload(self.project, self.alice)["readiness"]["needs_location"]
        self.save(self.bob)
        self.save(self.bob, "second")
        self.assertEqual(L.load_user(self.project, "alice"), before)
        self.assertEqual(L.payload(self.project, self.alice)["readiness"]["needs_location"], before_missing)
        self.assertEqual(set(self.items(self.alice)), {"u:alice/code", "u:bob/code", "u:bob/second"})

    def test_repository_without_location_here_names_it_in_readiness(self):
        # The flat profile on bob's host has no repository, so readiness says
        # "no primary repository registered"; the layered view must replace it
        # with the specific missing location.
        self.save(self.alice)
        blocked = L.payload(self.project, self.bob)["readiness"]["blocked"]
        implement = [b["reason"] for b in blocked if b["capability"] == "implement"]
        self.assertEqual(implement, ["repository 'code' has no location on this host"])

    def test_paths_from_all_users_and_hosts_are_preserved(self):
        self.save(self.alice)
        for viewer, path in ((self.bob, "/mnt/alice/code"),
                             (self.bob, "/work/other-checkout"),
                             (L.Viewer(user="carol", host_id="third"), "/carol/code")):
            L.add_location(self.project, viewer=viewer, key="u:alice/code",
                           path_or_ref=path, actor=f"agent:{viewer.user}")
        paths = self.items(self.alice)["u:alice/code"]["locations"]
        self.assertEqual(len(paths), 4)
        self.assertEqual({p["added_by"] for p in paths}, {"alice", "bob", "carol"})
        self.assertEqual(len(self.items(self.bob)["u:alice/code"]["locations"]), 4)

    def test_flat_legacy_drafts_also_merge(self):
        self.save(self.alice)
        L.save_layer(self.project, {"storage": [{"id": "data", "location": "/data"}]},
                     viewer=self.alice, layer="user", actor="agent:alice")
        self.assertEqual(set(self.items(self.bob)), {"u:alice/code", "u:alice/data"})

    def test_concurrent_setup_does_not_lose_resources(self):
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda i: self.save(self.alice, f"repo-{i}"), range(12)))
        self.assertEqual(len(self.items(self.bob)), 12)

    def test_secrets_are_references_visible_to_members_and_values_are_rejected(self):
        self.save(self.alice, resources=[{"id": "key", "kind": "secret",
                  "details": {"ref": "env:MODEL_KEY"}}])
        self.assertEqual(self.items(self.bob)["u:alice/key"]["details"]["ref"], "env:MODEL_KEY")
        with self.assertRaises(setup.SetupError):
            self.save(self.alice, resources=[{"id": "bad", "kind": "secret",
                      "details": {"ref": "sk-" + "a" * 40}}])

    def test_read_only_viewer_cannot_write_locations(self):
        self.save(self.alice)
        with self.assertRaises(setup.SetupError):
            L.add_location(self.project, viewer=L.Viewer(user="reader", can_write=False),
                           key="u:alice/code", path_or_ref="/read", actor="human:reader")

    def test_legacy_migration_retains_resources_locations_and_backup(self):
        profile = setup.example_profile()
        normalized = setup.normalize_profile(profile)[0]
        digest = setup.digest(normalized)
        folder = self.project / "setup"
        folder.mkdir(parents=True)
        old = {"profile": normalized, "state": "confirmed", "machine": "old-host",
               "confirmation": {"by": "human:alice", "digest": digest}}
        (folder / "environment.json").write_text(json.dumps(old), encoding="utf-8")
        L.migrate(self.project, default_owner="alice")
        self.assertTrue((folder / L.LEGACY_BACKUP).exists())
        items = self.items(self.bob)
        self.assertEqual(len(items), sum(len(normalized[f]) for f in setup.KIND_FIELDS.values()))
        self.assertTrue(any(i["kind"] == "secret" for i in items.values()))
        self.assertEqual(L.load_project(self.project)["state"], "confirmed")


if __name__ == "__main__":
    unittest.main()
