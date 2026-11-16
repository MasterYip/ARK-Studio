"""Deployments preserve authentication without a launcher's environment."""
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from arkstudio import auth, org, server_config
from arkstudio.__main__ import _build_auth


class ServerConfigTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.config = self.root / "server.json"

    def load(self, values):
        self.config.write_text(json.dumps(values))
        return server_config.load(self.config)

    def test_relative_paths_resolve_against_config_directory(self):
        cli, env = self.load({"root": "data/projects", "private_dir": "data/private",
                              "org_db": "data/org.sqlite3", "media_root": ["media"]})
        self.assertEqual(cli["root"], str(self.root / "data/projects"))
        self.assertEqual(env["ARK_ORG_DB"], str(self.root / "data/org.sqlite3"))
        self.assertEqual(cli["media_root"], [str(self.root / "media")])
        self.assertIs(cli["require_auth"], True)

    def test_unknown_settings_plaintext_passwords_and_wrong_types_fail(self):
        for values in ({"auth_password": "secret"}, {"port": True}, {"port": 70000},
                       {"port": "8890"}, {"trust_proxy": "false"}, {"root": ""}, {"media_root": "x"}):
            with self.subTest(keys=list(values)), self.assertRaises(auth.AuthConfigError):
                self.load(values)

    def protected_org(self):
        database = self.root / "org.sqlite3"
        manager = org.OrgManager(database)
        self.addCleanup(manager.close)
        manager.bootstrap(owner_username="owner", owner_password_hash=auth.hash_password("initial", iterations=1000))
        return manager, database

    def test_password_changed_in_database_is_used_without_environment(self):
        manager, database = self.protected_org()
        manager.set_password_hash("owner", auth.hash_password("changed", iterations=1000))
        args = SimpleNamespace(root=str(self.root / "projects"), auth_user="", auth_password="",
                               auth_password_file="", auth_password_hash="", auth_session_ttl=0)
        with mock.patch.dict("os.environ", {}, clear=True):
            saved = _build_auth(args, environ={})
        self.assertTrue(auth.verify_password(saved.password_hash, "changed"))
        self.assertFalse(auth.verify_password(saved.password_hash, "initial"))

    def test_disabled_owner_never_falls_back_to_anonymous_mode(self):
        manager, database = self.protected_org()
        manager.store.update_user(manager.user_id_for("owner"), disabled=1)
        with self.assertRaises(auth.AuthConfigError):
            server_config.persisted_auth(database)

    def test_corrupt_database_never_falls_back_to_anonymous_mode(self):
        database = self.root / "org.sqlite3"
        database.write_bytes(b"damaged database")
        with self.assertRaises(auth.AuthConfigError):
            server_config.persisted_auth(database)

    def test_empty_installation_keeps_explicit_local_mode(self):
        self.assertIsNone(server_config.persisted_auth(self.root / "missing.sqlite3"))
        manager = org.OrgManager(self.root / "org.sqlite3")
        self.addCleanup(manager.close)
        manager.bootstrap()
        self.assertIsNone(server_config.persisted_auth(manager.path))

    def test_native_initialization_stores_no_plaintext_and_cannot_overwrite(self):
        data = self.root / "data"
        password = "strong-initial-owner-password"
        server_config.initialize(self.config, data, "owner", password, port=18990)
        self.assertNotIn(password, self.config.read_text())
        self.assertNotIn(password.encode(), (data / "org.sqlite3").read_bytes())
        saved = server_config.persisted_auth(data / "org.sqlite3")
        self.assertTrue(auth.verify_password(saved.password_hash, password))
        with self.assertRaises(auth.AuthConfigError):
            server_config.initialize(self.config, data, "owner", "another-strong-password")

    def test_missing_org_database_with_existing_login_state_fails_closed(self):
        projects = self.root / "projects"
        projects.mkdir()
        (projects / ".auth-secret").write_text("existing login state")
        args = SimpleNamespace(root=str(projects), auth_user="", auth_password="",
                               auth_password_file="", auth_password_hash="", auth_session_ttl=0)
        with mock.patch.dict("os.environ", {}, clear=True), self.assertRaises(auth.AuthConfigError):
            _build_auth(args, environ={})
