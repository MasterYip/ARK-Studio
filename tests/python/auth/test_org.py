"""Organization, per-project authorization and per-user credential tests.

These run the real stdlib HTTP server on 127.0.0.1 with a seeded project and a
multi-user organization database in a temporary directory.  No external model,
network or Git remote is contacted.
"""
from __future__ import annotations

import json
import io
import os
import re
import subprocess
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from contextlib import contextmanager
from types import SimpleNamespace
from unittest import mock

from arkstudio import auth, linux_account, org
from arkstudio.server import App, serve
from arkstudio.sessions import SessionError, SessionManager


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Response:
    def __init__(self, status: int, headers: dict, text: str):
        self.status = status
        self.headers = {str(key).lower(): value for key, value in (headers or {}).items()}
        self.text = text

    def json(self):
        return json.loads(self.text or "{}")


def request(base: str, path: str, *, method: str = "GET", body=None,
            headers=None) -> Response:
    data = None
    if body is not None:
        data = body.encode("utf-8") if isinstance(body, str) else body
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers=headers or {})
    opener = urllib.request.build_opener(NoRedirect())
    try:
        with opener.open(req, timeout=20) as response:
            return Response(response.status, dict(response.headers),
                            response.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as exc:
        return Response(exc.code, dict(exc.headers or {}),
                        exc.read().decode("utf-8", "replace"))


def csrf_from(page: str) -> str:
    match = re.search(r'name="csrf"\s+value="([^"]+)"', page)
    return match.group(1) if match else ""


def cookie_from(response: Response, name: str = "ark_session") -> str:
    for value in (response.headers.get("set-cookie") or "").split(","):
        part = value.strip().split(";", 1)[0]
        if part.startswith(name + "="):
            return part
    return ""


class AuthHttpCase(unittest.TestCase):
    """Owner account + one seeded project, served over real HTTP."""

    def setUp(self):
        self._old_chat = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        self.tmp = tempfile.TemporaryDirectory()
        self.manager = auth.AuthManager("owner", password="owner-pass",
                                        iterations=1000, login_limit=50,
                                        login_window=60)
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=True,
                       task_sessions_root=Path(self.tmp.name) / "sessions",
                       auth=self.manager)
        self.httpd = serve(self.app, port=0, block=False)
        self.port = self.httpd.server_address[1]
        self.base = f"http://127.0.0.1:{self.port}"
        self.host = f"127.0.0.1:{self.port}"
        self.owner = self.login("owner", "owner-pass")

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.workspace.shutdown()
        self.app.shutdown()
        self.tmp.cleanup()
        if self._old_chat is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = self._old_chat

    # ------------------------------------------------------------- helpers
    def login(self, username: str, password: str) -> str:
        page = request(self.base, "/login?next=%2F", headers={"Host": self.host})
        payload = urllib.parse.urlencode({
            "username": username, "password": password,
            "csrf": csrf_from(page.text), "next": "/",
        })
        response = request(self.base, "/login", method="POST", body=payload, headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Host": self.host, "Origin": f"http://{self.host}",
        })
        self.assertEqual(response.status, 303, response.text)
        return cookie_from(response)

    def api(self, path: str, *, cookie: str = "", method: str = "GET", body=None,
            host: str | None = None, headers=None):
        merged = {"Host": host or self.host,
                  "Content-Type": "application/json"}
        if cookie:
            merged["Cookie"] = cookie
        merged.update(headers or {})
        merged["Origin"] = f"http://{merged['Host']}"
        data = json.dumps(body) if body is not None else None
        return request(self.base, path, method=method, body=data, headers=merged)

    def create_user(self, username: str, password: str, role: str = "member",
                    cookie: str | None = None) -> Response:
        return self.api("/api/org/users", cookie=cookie or self.owner,
                        method="POST", body={"username": username,
                                             "password": password, "role": role})

    def projects(self, cookie: str) -> list[dict]:
        response = self.api("/api/projects", cookie=cookie)
        self.assertEqual(response.status, 200, response.text)
        return response.json()["projects"]

    def project_id(self) -> str:
        response = self.api("/api/project", cookie=self.owner)
        self.assertEqual(response.status, 200, response.text)
        return response.json()["summary"]["id"]

    def grant(self, project_id: str, *, user: str | None = None,
              team: str | None = None, permission: str = "read",
              cookie: str | None = None) -> Response:
        body = {"permission": permission}
        if user:
            body["user"] = user
        if team:
            body["team"] = team
        return self.api(f"/api/org/projects/{project_id}/grants",
                        cookie=cookie or self.owner, method="POST", body=body)


class IdentityAndRoleTests(AuthHttpCase):
    def test_identity_is_per_user_and_never_shared(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        owner_session = self.api("/api/auth/session", cookie=self.owner).json()
        alice_session = self.api("/api/auth/session", cookie=alice).json()
        self.assertEqual(owner_session["user"], "owner")
        self.assertEqual(owner_session["role"], "owner")
        self.assertEqual(alice_session["user"], "alice")
        self.assertEqual(alice_session["role"], "member")
        self.assertFalse(alice_session["is_admin"])
        # Passwords are independent.
        page = request(self.base, "/login?next=%2F", headers={"Host": self.host})
        wrong = request(self.base, "/login", method="POST", body=urllib.parse.urlencode({
            "username": "alice", "password": "owner-pass",
            "csrf": csrf_from(page.text), "next": "/"}), headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Host": self.host, "Origin": f"http://{self.host}"})
        self.assertEqual(wrong.status, 401)
        # A member has no implicit access to real projects, but the built-in
        # tutorial is available to everyone.
        self.assertEqual([item["id"] for item in self.projects(alice)], ["ark-demo"])
        denied = self.api("/api/org/users", cookie=alice)
        self.assertEqual(denied.status, 403)
        self.assertEqual(denied.json()["error"]["code"], "ORG_FORBIDDEN")
        demo = self.api("/api/project", cookie=alice)
        self.assertEqual(demo.status, 200)
        self.assertEqual(demo.json()["summary"]["id"], "ark-demo")

    def test_member_creates_project_and_is_project_admin(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        created = self.api("/api/projects", cookie=alice, method="POST",
                           body={"name": "Alice Project", "template": "blank"})
        self.assertEqual(created.status, 200, created.text)
        data = created.json()
        project_id = data["summary"]["id"]
        self.assertEqual(data["session"]["project_permission"], "admin")
        providers = data["summary"]["settings"]["agents"]["providers"]
        self.assertTrue(providers)
        self.assertTrue(all(item.get("enabled") is False
                            for item in providers.values()))
        effective = self.api("/api/settings/effective", cookie=alice).json()
        self.assertTrue(effective["agents"]["providers"])
        self.assertTrue(all(item.get("enabled") is False
                            for item in effective["agents"]["providers"]
                            if item.get("id") not in {"echo", "shell"}))
        self.assertEqual(sorted(item["id"] for item in self.projects(alice)),
                         sorted(["ark-demo", project_id]))
        # Project admin may edit project settings, but is not an org admin.
        allowed = self.api("/api/project/settings", cookie=alice, method="PATCH",
                           body={"graph": {"repulsion": 4200}})
        self.assertEqual(allowed.status, 200, allowed.text)
        denied = self.api("/api/org/users", cookie=alice)
        self.assertEqual(denied.status, 403)
        # An organization admin can change the creator's project relationship.
        downgraded = self.grant(project_id, user="alice", permission="read")
        self.assertEqual(downgraded.status, 200, downgraded.text)
        blocked = self.api("/api/project/settings", cookie=alice, method="PATCH",
                           body={"graph": {"repulsion": 4300}})
        self.assertEqual(blocked.status, 403)
        restored = self.grant(project_id, user="alice", permission="admin")
        self.assertEqual(restored.status, 200, restored.text)
        deleted = self.api("/api/project", cookie=alice, method="DELETE",
                           body={"name": "Alice Project"})
        self.assertEqual(deleted.status, 200, deleted.text)
        self.assertNotIn(project_id,
                         [item["id"] for item in self.projects(alice)])

    def test_duplicate_username_and_bad_password_rejected(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        duplicate = self.create_user("ALICE", "other-pass")
        self.assertEqual(duplicate.status, 409)
        self.assertEqual(duplicate.json()["error"]["code"], "USER_EXISTS")
        weak = self.create_user("bob", "")
        self.assertEqual(weak.status, 400)
        self.assertEqual(weak.json()["error"]["code"], "PASSWORD_INVALID")
        member = self.create_user("carol", "carol-pass", cookie=self.login(
            "alice", "alice-pass"))
        self.assertEqual(member.status, 403)

    def test_only_owner_can_create_owner_and_last_owner_is_protected(self):
        promoted = self.create_user("admin1", "admin-pass", role="admin")
        self.assertEqual(promoted.status, 200)
        admin_cookie = self.login("admin1", "admin-pass")
        owner_attempt = self.create_user("owner2", "owner2-pass", role="owner",
                                         cookie=admin_cookie)
        self.assertEqual(owner_attempt.status, 403)
        self.assertEqual(owner_attempt.json()["error"]["code"], "ORG_OWNER_REQUIRED")
        owner_id = self.api("/api/org", cookie=self.owner).json()["user"]["id"]
        self_demote = self.api(f"/api/org/users/{owner_id}", cookie=self.owner,
                               method="PATCH", body={"role": "admin"})
        self.assertEqual(self_demote.status, 409)
        self.assertEqual(self_demote.json()["error"]["code"], "LAST_OWNER")
        # Admin can manage members but only an owner may touch an owner.
        owner_patch = self.api(f"/api/org/users/{owner_id}", cookie=admin_cookie,
                               method="PATCH", body={"display_name": "X"})
        self.assertEqual(owner_patch.status, 403)

    def test_password_change_invalidates_other_sessions(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        first = self.login("alice", "alice-pass")
        second = self.login("alice", "alice-pass")
        changed = self.api("/api/org/password", cookie=second, method="POST", body={
            "current_password": "alice-pass", "new_password": "new-alice-pass"})
        self.assertEqual(changed.status, 200, changed.text)
        self.assertGreaterEqual(changed.json()["sessions_revoked"], 1)
        self.assertEqual(self.api("/api/project", cookie=first).status, 401)
        self.assertEqual(self.api("/api/auth/session", cookie=second).status, 200)
        self.login("alice", "new-alice-pass")
        with self.assertRaises(AssertionError):
            self.login("alice", "alice-pass")


class GrantTests(AuthHttpCase):
    def test_direct_project_grant_levels(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        grant = self.grant(project_id, user="alice", permission="read")
        self.assertEqual(grant.status, 200, grant.text)
        self.assertEqual(grant.json()["grant"]["permission"], "read")
        self.assertEqual(self.api("/api/project", cookie=alice).status, 200)
        self.assertEqual([item["id"] for item in self.projects(alice)],
                         [project_id, "ark-demo"])
        denied = self.api("/api/units", cookie=alice, method="POST",
                          body={"title": "nope", "type": "note"})
        self.assertEqual(denied.status, 403)
        self.assertEqual(denied.json()["error"]["code"], "PROJECT_FORBIDDEN")
        self.assertEqual(self.grant(project_id, user="alice", permission="write").status, 200)
        created = self.api("/api/units", cookie=alice, method="POST",
                           body={"title": "alice wrote this", "type": "note"})
        self.assertEqual(created.status, 200, created.text)
        # Settings and grants stay admin-only.
        write_settings = self.api("/api/project/settings", cookie=alice, method="PATCH",
                                  body={"graph": {"repulsion": 4000}})
        self.assertEqual(write_settings.status, 403)
        self.assertEqual(write_settings.json()["error"]["code"], "PROJECT_FORBIDDEN")
        self.assertEqual(self.grant(project_id, user="alice", permission="read").status, 200)
        self.assertEqual(self.grant(project_id, user="alice", permission="admin",
                                    cookie=alice).status, 403)
        admin_grant = self.grant(project_id, user="alice", permission="admin")
        self.assertEqual(admin_grant.status, 200)
        self.assertEqual(self.api("/api/project/settings", cookie=alice, method="PATCH",
                                  body={"graph": {"repulsion": 4000}}).status, 200)
        revoke = self.api(f"/api/org/projects/{project_id}/grants/revoke",
                          cookie=self.owner, method="POST", body={"user": "alice"})
        self.assertEqual(revoke.status, 200)
        denied = self.api("/api/projects/open", cookie=alice, method="POST",
                          body={"id": project_id})
        self.assertEqual(denied.status, 404)

    def test_team_grant_gives_members_access(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        team = self.api("/api/org/teams", cookie=self.owner, method="POST",
                        body={"name": "research", "description": "core team"})
        self.assertEqual(team.status, 200, team.text)
        team_id = team.json()["team"]["id"]
        self.assertEqual(self.api(f"/api/org/teams/{team_id}/members", cookie=self.owner,
                                  method="POST", body={"user": "alice"}).status, 200)
        project_id = self.project_id()
        granted = self.grant(project_id, team=team_id, permission="write")
        self.assertEqual(granted.status, 200, granted.text)
        alice = self.login("alice", "alice-pass")
        bob = self.login("bob", "bob-pass")
        self.assertEqual(self.api("/api/project", cookie=alice).status, 200)
        self.assertEqual(self.api("/api/units", cookie=alice, method="POST",
                                  body={"title": "team write", "type": "note"}).status, 200)
        # Bob is not on the team and has no direct grant; he only sees the demo.
        bob_project = self.api("/api/project", cookie=bob)
        self.assertEqual(bob_project.status, 200)
        self.assertEqual(bob_project.json()["summary"]["id"], "ark-demo")
        self.assertEqual([item["id"] for item in self.projects(bob)], ["ark-demo"])
        alice_id = self.api("/api/org", cookie=alice).json()["user"]["id"]
        removed = self.api(f"/api/org/teams/{team_id}/members/{alice_id}",
                           cookie=self.owner, method="DELETE")
        self.assertEqual(removed.status, 200, removed.text)
        alice_project = self.api("/api/project", cookie=alice)
        self.assertEqual(alice_project.status, 200)
        self.assertEqual(alice_project.json()["summary"]["id"], "ark-demo")

    def test_viewer_role_reads_all_projects_but_cannot_write(self):
        self.assertEqual(self.create_user("viewer1", "viewer-pass",
                                          role="viewer").status, 200)
        viewer = self.login("viewer1", "viewer-pass")
        self.assertEqual(self.api("/api/project", cookie=viewer).status, 200)
        self.assertEqual(self.api("/api/units", cookie=viewer, method="POST",
                                  body={"title": "no", "type": "note"}).status, 403)
        self.assertEqual(self.api("/api/org/users", cookie=viewer).status, 403)

    def test_unauthorized_projects_and_path_traversal_are_rejected(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        first_id = self.project_id()
        self.assertEqual(self.grant(first_id, user="alice", permission="read").status, 200)
        # The owner creates a second project, which alice may not see or open.
        created = self.api("/api/projects", cookie=self.owner, method="POST",
                           body={"name": "Secret Project", "template": "blank"})
        self.assertEqual(created.status, 200, created.text)
        second_id = created.json()["summary"]["id"]
        self.assertNotEqual(first_id, second_id)
        listed = self.projects(alice)
        self.assertEqual([item["id"] for item in listed], [first_id, "ark-demo"])
        by_id = self.api("/api/projects/open", cookie=alice, method="POST",
                         body={"id": second_id})
        self.assertEqual(by_id.status, 404)
        self.assertEqual(by_id.json()["error"]["code"], "PROJECT_NOT_ACCESSIBLE")
        unknown = self.api("/api/projects/open", cookie=alice, method="POST",
                           body={"id": "prj_doesnotexist"})
        self.assertEqual(unknown.status, 404)
        second_path = created.json()["summary"]["path"]
        by_path = self.api("/api/projects/open", cookie=alice, method="POST",
                           body={"path": second_path})
        self.assertEqual(by_path.status, 404)
        outside = Path(self.tmp.name) / "Outside.ark"
        outside.mkdir()
        traversal = self.api("/api/projects/open", cookie=alice, method="POST",
                             body={"path": str(outside)})
        self.assertEqual(traversal.status, 403)
        self.assertEqual(traversal.json()["error"]["code"], "PROJECT_PATH_DENIED")
        rel_traversal = self.api("/api/projects/open", cookie=alice, method="POST",
                                 body={"path": "../Outside.ark"})
        self.assertEqual(rel_traversal.status, 403)


class PersonalSettingsTests(AuthHttpCase):
    CLEAN_GLOBAL = {
        "api_key": "", "base_url": "https://global.example/v1",
        "model": "global-model", "reasoning_effort": "high", "timeout": 90,
    }

    def test_personal_overrides_project_and_is_isolated(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice", permission="read").status, 200)
        project_save = self.api("/api/project/settings", cookie=self.owner,
                                method="PATCH",
                                body={"assistant": {"base_url": "https://project.example/v1",
                                                    "model": "project-model"}})
        self.assertEqual(project_save.status, 200, project_save.text)
        with mock.patch("arkstudio.assistant_config.load",
                        return_value=dict(self.CLEAN_GLOBAL)):
            effective = self.api("/api/settings/effective", cookie=alice).json()
            self.assertEqual(effective["assistant"]["model"], "project-model")
            self.assertEqual(effective["assistant"]["sources"]["model"], "project")
            self.assertTrue(effective["assistant"]["personal_config_required"])
            saved = self.api("/api/personal/settings?scope=assistant", cookie=alice,
                             method="POST", body={"model": "alice-model",
                                                  "api_key": "sk-alice-secret-1234"})
            self.assertEqual(saved.status, 200, saved.text)
            self.assertTrue(saved.json()["settings"]["has_key"])
            alice_effective = self.api("/api/settings/effective", cookie=alice).json()
            self.assertEqual(alice_effective["assistant"]["model"], "alice-model")
            self.assertEqual(alice_effective["assistant"]["sources"]["model"], "personal")
            self.assertTrue(alice_effective["assistant"]["configured"])
            self.assertFalse(alice_effective["assistant"]["personal_config_required"])
            owner_effective = self.api("/api/settings/effective",
                                       cookie=self.owner).json()
            self.assertEqual(owner_effective["assistant"]["model"], "project-model")
            self.assertNotEqual(owner_effective["assistant"].get("model"), "alice-model")
            config = self.api("/api/assistant/config", cookie=alice).json()
            self.assertEqual(config["config"]["model"], "alice-model")
            self.assertEqual(config["config"]["source"], "personal")
            self.assertNotIn("sk-alice-secret-1234", json.dumps(config))
            # Clearing the personal override falls back to the project value.
            cleared = self.api("/api/personal/settings?scope=assistant", cookie=alice,
                               method="POST", body={"clear_key": True, "model": ""})
            self.assertEqual(cleared.status, 200)
            after = self.api("/api/settings/effective", cookie=alice).json()
            self.assertEqual(after["assistant"]["model"], "project-model")
            self.assertFalse(after["assistant"]["configured"])
            self.assertTrue(after["assistant"]["personal_config_required"])

    def test_personal_agent_credentials_are_sealed_and_never_returned(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        self.assertEqual(self.grant(self.project_id(), user="alice",
                                    permission="read").status, 200)
        secret = "sk-ant-alice-SECRET-9876"
        saved = self.api("/api/personal/settings?scope=agents", cookie=alice,
                         method="POST", body={"providers": {"claude": {
                             "command": "claude -p {prompt}",
                             "env": {"ANTHROPIC_API_KEY": secret}}}})
        self.assertEqual(saved.status, 200, saved.text)
        public = json.dumps(saved.json(), ensure_ascii=False)
        self.assertNotIn(secret, public)
        self.assertIn("…9876", public)
        self.assertEqual(saved.json()["settings"]["providers"]["claude"]["env_names"],
                         ["ANTHROPIC_API_KEY"])
        with mock.patch("arkstudio.assistant_config.load",
                        return_value=dict(self.CLEAN_GLOBAL)):
            for path in ("/api/settings/effective", "/api/assistant/config",
                         "/api/agent-sessions/presets", "/api/bootstrap", "/api/org"):
                response = self.api(path, cookie=alice)
                self.assertEqual(response.status, 200, (path, response.text))
                self.assertNotIn(secret, response.text, path)
            effective = self.api("/api/settings/effective", cookie=alice).json()
            claude = [item for item in effective["agents"]["providers"]
                      if item["id"] == "claude"][0]
            self.assertEqual(claude["command_source"], "personal")
            self.assertIn("ANTHROPIC_API_KEY", claude["env_names"])
            self.assertEqual(claude["credential_source"], "personal")
        # The secret is sealed in the organization database, not stored raw.
        database = Path(self.app.org.path)
        data = database.read_bytes()
        self.assertNotIn(secret.encode("utf-8"), data)
        self.assertIn(b"ark1$", data)
        # Another user sees neither the value nor the override.
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        bob = self.login("bob", "bob-pass")
        bob_settings = self.api("/api/personal/settings?scope=agents", cookie=bob).json()
        self.assertEqual(bob_settings["settings"]["providers"], {})
        self.assertNotIn(secret, json.dumps(bob_settings))

    # -- regression: partial saves must not write untouched secrets as plaintext
    def _secrets_blob(self, username: str, scope: str) -> dict:
        user = self.app.org.store.user_by_name(username)
        row = self.app.org.store.secrets_row(user["id"], scope) or {}
        return json.loads(row.get("data_json") or "{}")

    def test_partial_agents_save_keeps_untouched_secrets_sealed(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        claude_key = "sk-ant-alice-CLAUDE-1111"
        codex_key = "sk-alice-CODEX-2222"
        first = self.api("/api/personal/settings?scope=agents", cookie=alice,
                         method="POST", body={"providers": {
                             "claude": {"env": {"ANTHROPIC_API_KEY": claude_key}},
                             "codex": {"env": {"OPENAI_API_KEY": codex_key}}}})
        self.assertEqual(first.status, 200, first.text)
        # Touch only the codex command; claude and the codex key are untouched.
        second = self.api("/api/personal/settings?scope=agents", cookie=alice,
                          method="POST", body={"providers": {
                              "codex": {"command": "codex exec {prompt}"}}})
        self.assertEqual(second.status, 200, second.text)
        blob = self._secrets_blob("alice", "agents")
        self.assertEqual(sorted(blob), ["env:claude:ANTHROPIC_API_KEY",
                                        "env:codex:OPENAI_API_KEY"])
        for token in blob.values():
            self.assertTrue(token.startswith("ark1$"), "secret stored unsealed")
        data = Path(self.app.org.path).read_bytes()
        self.assertNotIn(claude_key.encode(), data)
        self.assertNotIn(codex_key.encode(), data)
        # A third save (the one that used to hit SECRET_CORRUPT) and reads work.
        third = self.api("/api/personal/settings?scope=agents", cookie=alice,
                         method="POST", body={"providers": {
                             "claude": {"enabled": True}}})
        self.assertEqual(third.status, 200, third.text)
        read = self.api("/api/personal/settings?scope=agents", cookie=alice)
        self.assertEqual(read.status, 200, read.text)
        providers = read.json()["settings"]["providers"]
        self.assertEqual(providers["claude"]["env_hints"],
                         {"ANTHROPIC_API_KEY": "…1111"})
        self.assertEqual(providers["codex"]["env_hints"], {"OPENAI_API_KEY": "…2222"})
        self.assertNotIn(claude_key, read.text)
        self.assertNotIn(codex_key, read.text)
        internal = self.app.org.personal_settings("alice", "agents")["secrets"]
        self.assertEqual(internal["env:claude:ANTHROPIC_API_KEY"], claude_key)
        self.assertEqual(internal["env:codex:OPENAI_API_KEY"], codex_key)
        # Explicit clears still remove exactly the named credential.
        cleared = self.api("/api/personal/settings?scope=agents", cookie=alice,
                           method="POST", body={"providers": {
                               "codex": {"clear_env": ["OPENAI_API_KEY"]}}})
        self.assertEqual(cleared.status, 200, cleared.text)
        self.assertEqual(sorted(self._secrets_blob("alice", "agents")),
                         ["env:claude:ANTHROPIC_API_KEY"])

    def test_partial_assistant_save_keeps_key_sealed_and_unchanged(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        key = "sk-alice-assistant-3333"
        saved = self.api("/api/personal/settings?scope=assistant", cookie=alice,
                         method="POST", body={"api_key": key})
        self.assertEqual(saved.status, 200, saved.text)
        token = self._secrets_blob("alice", "assistant")["api_key"]
        changed = self.api("/api/personal/settings?scope=assistant", cookie=alice,
                           method="POST", body={"model": "alice-model"})
        self.assertEqual(changed.status, 200, changed.text)
        # The untouched key keeps its exact sealed token (not re-encrypted).
        self.assertEqual(self._secrets_blob("alice", "assistant")["api_key"], token)
        self.assertNotIn(key.encode(), Path(self.app.org.path).read_bytes())
        self.assertTrue(changed.json()["settings"]["has_key"])

    def test_plaintext_secret_is_resealed_on_read_instead_of_500(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        claude_key = "sk-ant-alice-CLAUDE-4444"
        good = self.api("/api/personal/settings?scope=agents", cookie=alice,
                        method="POST", body={"providers": {
                            "codex": {"env": {"OPENAI_API_KEY": "sk-codex-5555"}}}})
        self.assertEqual(good.status, 200, good.text)
        # Simulate the damage left by the old bug: one plaintext value next to
        # a correctly sealed one.
        user = self.app.org.store.user_by_name("alice")
        blob = self._secrets_blob("alice", "agents")
        blob["env:claude:ANTHROPIC_API_KEY"] = claude_key
        self.app.org.store.save_settings(
            user["id"], "agents",
            {"providers": {"claude": {}, "codex": {}}},
            json.dumps(blob))
        stderr = io.StringIO()
        with mock.patch("sys.stderr", stderr):
            read = self.api("/api/personal/settings?scope=agents", cookie=alice)
        self.assertEqual(read.status, 200, read.text)
        self.assertNotIn(claude_key, read.text)
        self.assertEqual(read.json()["settings"]["providers"]["claude"]["env_hints"],
                         {"ANTHROPIC_API_KEY": "…4444"})
        repaired = self._secrets_blob("alice", "agents")
        self.assertTrue(repaired["env:claude:ANTHROPIC_API_KEY"].startswith("ark1$"))
        self.assertEqual(repaired["env:codex:OPENAI_API_KEY"],
                         blob["env:codex:OPENAI_API_KEY"])
        self.assertNotIn(claude_key.encode(), Path(self.app.org.path).read_bytes())
        self.assertIn("re-sealed", stderr.getvalue())
        self.assertNotIn(claude_key, stderr.getvalue())
        audit = json.dumps(self.app.org.store.list_audit())
        self.assertIn("personal.secrets.reseal", audit)
        self.assertNotIn(claude_key, audit)
        self.assertEqual(self.app.org.personal_settings("alice", "agents")
                         ["secrets"]["env:claude:ANTHROPIC_API_KEY"], claude_key)
        # A real SecretBox token that fails authentication still fails closed.
        blob = self._secrets_blob("alice", "agents")
        blob["env:codex:OPENAI_API_KEY"] = blob["env:claude:ANTHROPIC_API_KEY"]
        self.app.org.store.save_settings(user["id"], "agents",
                                         {"providers": {}}, json.dumps(blob))
        broken = self.api("/api/personal/settings?scope=agents", cookie=alice)
        self.assertEqual(broken.status, 500)
        self.assertNotIn(claude_key, broken.text)

    def test_agent_config_requires_personal_or_project_configuration(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        self.assertEqual(self.grant(self.project_id(), user="alice",
                                    permission="read").status, 200)
        with mock.patch("arkstudio.assistant_config.load",
                        return_value=dict(self.CLEAN_GLOBAL)):
            presets = self.api("/api/agent-sessions/presets", cookie=alice).json()
            self.assertTrue(presets["personal_config_required"])
            command_attempt = self.api("/api/project/settings", cookie=self.owner,
                                       method="PATCH",
                                       body={"agents": {"providers": {"claude": {
                                           "command": "sh -c 'cat ~/.ark/org/secret.key'",
                                       }}}})
            self.assertEqual(command_attempt.status, 400)
            self.assertIn("personal settings", command_attempt.text)
            project_agents = self.api("/api/project/settings", cookie=self.owner,
                                      method="PATCH",
                                      body={"agents": {"providers": {"claude": {
                                          "env_names": ["DEEPSEEK_API_KEY"]}}}})
            self.assertEqual(project_agents.status, 200, project_agents.text)
            self.assertNotIn("command", project_agents.json()["settings"]["agents"]
                             ["providers"]["claude"])
            after = self.api("/api/settings/effective", cookie=alice).json()
            claude = [item for item in after["agents"]["providers"]
                      if item["id"] == "claude"][0]
            self.assertEqual(claude["command_source"], "global")
            self.assertTrue(claude["project_configured"])
            self.assertFalse(after["agents"]["personal_config_required"])


class MobileBindingTests(AuthHttpCase):
    def _pair(self, cookie: str, *, permission: str = "read_only") -> Response:
        return self.api("/api/mobile/pair", cookie=cookie, method="POST",
                        body={"permission": permission,
                              "tunnel_host": "192.168.1.20", "tunnel_port": 8891})

    def test_pairing_is_bound_to_creating_user(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        self.assertEqual(self.grant(self.project_id(), user="alice",
                                    permission="write").status, 200)
        pair = self._pair(self.owner)
        self.assertEqual(pair.status, 200, pair.text)
        data = pair.json()
        self.assertEqual(data["pair"]["user"], "owner")
        self.assertNotIn("owner-pass", json.dumps(data))
        self.assertNotIn("password", json.dumps(data).lower())
        # A different signed-in account cannot redeem it.
        foreign = self.api("/api/mobile/pair/claim", cookie=alice, method="POST",
                           body={"token": data["code"]},
                           host="192.168.1.20:8891")
        self.assertEqual(foreign.status, 403, foreign.text)
        self.assertEqual(foreign.json()["error"]["code"], "PAIRING_WRONG_USER")
        # An unauthenticated phone redeems it and becomes the creator only.
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": data["code"]}, host="192.168.1.20:8891")
        self.assertEqual(claim.status, 200, claim.text)
        self.assertEqual(claim.json()["user"], "owner")
        set_cookie = claim.headers.get("set-cookie", "")
        self.assertIn("ark_mobile=", set_cookie)
        self.assertNotIn("ark_session=", set_cookie)
        mobile_cookie = cookie_from(claim, "ark_mobile")
        self.assertTrue(mobile_cookie)
        status = self.api("/api/mobile/session", cookie=mobile_cookie,
                          host="192.168.1.20:8891")
        self.assertEqual(status.status, 200, status.text)
        self.assertEqual(status.json()["auth_user"], "owner")
        self.assertTrue(status.json()["authenticated"])
        # The paired-device cookie is not a desktop account session.
        account = self.api("/api/auth/session", cookie=mobile_cookie,
                           host="192.168.1.20:8891").json()
        self.assertFalse(account["authenticated"])
        # Codes are one-time; replay is rejected.
        replay = self.api("/api/mobile/pair/claim", method="POST",
                          body={"token": data["code"]}, host="192.168.1.20:8891")
        self.assertEqual(replay.status, 401)
        self.assertEqual(replay.json()["error"]["code"], "PAIRING_USED")

    def test_phone_never_exceeds_the_users_project_permission(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice",
                                    permission="read").status, 200)
        # read-only member cannot mint a full-access pairing
        denied = self._pair(alice, permission="full")
        self.assertEqual(denied.status, 403)
        read_pair = self._pair(alice)
        self.assertEqual(read_pair.status, 200, read_pair.text)
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": read_pair.json()["code"]},
                         host="192.168.1.20:8891")
        token = claim.json()["mobile_token"]
        session_cookie = cookie_from(claim, "ark_mobile")
        headers = {"X-ARK-Mobile-Token": token, "Host": "192.168.1.20:8891",
                   "Cookie": session_cookie}
        self.assertEqual(self.api("/api/project", headers=headers).status, 200)
        denied_write = self.api("/api/units", method="POST",
                                body={"title": "phone", "type": "note"},
                                headers=headers)
        self.assertEqual(denied_write.status, 403)
        self.assertEqual(denied_write.json()["error"]["code"], "MOBILE_READ_ONLY")
        # After a write grant a full pairing still behaves as that user.
        self.assertEqual(self.grant(project_id, user="alice",
                                    permission="write").status, 200)
        full = self._pair(alice, permission="full")
        self.assertEqual(full.status, 200, full.text)
        claim2 = self.api("/api/mobile/pair/claim", method="POST",
                          body={"token": full.json()["code"]},
                          host="192.168.1.20:8891")
        token2 = claim2.json()["mobile_token"]
        headers2 = {"X-ARK-Mobile-Token": token2, "Host": "192.168.1.20:8891",
                    "Cookie": cookie_from(claim2, "ark_mobile")}
        self.assertEqual(self.api("/api/units", method="POST",
                                  body={"title": "phone write", "type": "note"},
                                  headers=headers2).status, 200)
        # The phone is pinned to its paired project.
        created = self.api("/api/projects", cookie=self.owner, method="POST",
                           body={"name": "Other", "template": "blank"})
        other_id = created.json()["summary"]["id"]
        switch = self.api("/api/projects/open", method="POST",
                          body={"id": other_id}, headers=headers2)
        self.assertEqual(switch.status, 403)
        self.assertEqual(switch.json()["error"]["code"], "MOBILE_WRONG_PROJECT")
        # Pairing management stays desktop-only.
        manage = self.api("/api/mobile/pairs", headers=headers2)
        self.assertEqual(manage.status, 403)

    def test_pairing_code_contains_no_credentials(self):
        pair = self._pair(self.owner).json()
        blob = json.dumps(pair)
        self.assertNotIn("owner-pass", blob)
        self.assertNotIn("api_key", blob.lower())
        self.assertTrue(pair["qr_svg"].startswith("<svg"))
        self.assertNotIn(pair["code"], pair["qr_svg"])
        pairs = self.api("/api/mobile/pairs", cookie=self.owner).json()["pairs"]
        self.assertEqual(pairs[0]["user"], "owner")
        self.assertNotIn("token", json.dumps(pairs))


class LoopbackCompatibilityTests(unittest.TestCase):
    """No account configured: the original single-user loopback loop still works."""

    def setUp(self):
        self._old_chat = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        self.tmp = tempfile.TemporaryDirectory()
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=True,
                       task_sessions_root=Path(self.tmp.name) / "sessions")
        self.httpd = serve(self.app, port=0, block=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.workspace.shutdown()
        self.app.shutdown()
        self.tmp.cleanup()
        if self._old_chat is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = self._old_chat

    def api(self, path, *, method="GET", body=None):
        return request(self.base, path, method=method,
                       body=json.dumps(body) if body is not None else None,
                       headers={"Content-Type": "application/json"})

    def test_single_user_loopback_experience_is_unchanged(self):
        boot = self.api("/api/bootstrap")
        self.assertEqual(boot.status, 200)
        payload = boot.json()
        self.assertTrue(payload["summary"]["name"])
        self.assertTrue(payload["session"]["local"])
        self.assertEqual(payload["session"]["role"], "owner")
        self.assertEqual(len(payload["projects"]), 2)
        self.assertEqual(self.api("/api/project").status, 200)
        created = self.api("/api/projects", method="POST",
                           body={"name": "Local Project", "template": "blank"})
        self.assertEqual(created.status, 200, created.text)
        self.assertEqual(len(created.json()["projects"]), 3)
        effective = self.api("/api/settings/effective")
        self.assertEqual(effective.status, 200)
        self.assertEqual(effective.json()["user"], "local")
        saved = self.api("/api/personal/settings?scope=assistant", method="POST",
                         body={"model": "local-model"})
        self.assertEqual(saved.status, 200, saved.text)
        self.assertEqual(saved.json()["settings"]["values"]["model"], "local-model")
        pair = self.api("/api/mobile/pair", method="POST",
                        body={"permission": "read_only",
                              "tunnel_host": "192.168.1.20", "tunnel_port": 8891})
        self.assertEqual(pair.status, 200, pair.text)
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": pair.json()["code"]})
        # The loopback desktop Host is trusted; the claim still works.
        self.assertEqual(claim.status, 200, claim.text)


class OrgStoreUnitTests(unittest.TestCase):
    def test_roles_and_permission_order(self):
        self.assertEqual(org.ROLES, ("owner", "admin", "member", "viewer"))
        self.assertGreater(org.PERMISSION_RANK["admin"],
                           org.PERMISSION_RANK["write"])
        with self.assertRaises(org.OrgError):
            org.validate_role("superuser")
        with self.assertRaises(org.OrgError):
            org.validate_permission("owner")

    def test_secret_box_round_trip_and_aad_binding(self):
        with tempfile.TemporaryDirectory() as tmp:
            box = org.SecretBox(Path(tmp) / "secret.key")
            sealed = box.seal("hello-credential", aad="user1:assistant:api_key")
            self.assertNotIn("hello-credential", sealed)
            self.assertEqual(box.open(sealed, aad="user1:assistant:api_key"),
                             "hello-credential")
            with self.assertRaises(org.OrgError):
                box.open(sealed, aad="user2:assistant:api_key")
            tampered = sealed[:-2] + ("AA" if not sealed.endswith("AA") else "BB")
            with self.assertRaises(org.OrgError):
                box.open(tampered, aad="user1:assistant:api_key")
            # Shape check used by repair-on-read: sealed vs plaintext.
            self.assertTrue(org.SecretBox.is_token(sealed))
            for plain in ("sk-ant-123", "", "ark1$", "ark1$a$b", "ark2$a$b$c",
                          "ark1$$b$c", None, 42):
                self.assertFalse(org.SecretBox.is_token(plain), plain)

    def test_password_hashes_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            manager = org.OrgManager(Path(tmp) / "org.sqlite3",
                                     private_dir=Path(tmp) / "private")
            password_hash = auth.hash_password("correct horse", iterations=1000)
            manager.bootstrap(owner_username="owner",
                              owner_password_hash=password_hash)
            row = manager.store.user_by_name("owner")
            self.assertTrue(row["password_hash"].startswith("pbkdf2_sha256$"))
            blob = Path(manager.path).read_bytes()
            self.assertNotIn(b"correct horse", blob)
            self.assertEqual(manager.password_hash_for("owner"), password_hash)
            manager.close()

    def test_secret_box_fails_closed_on_unreadable_or_corrupt_key(self):
        with tempfile.TemporaryDirectory() as tmp:
            # A corrupt (too short) key must never be silently replaced.
            key = Path(tmp) / "secret.key"
            key.write_bytes(b"short")
            with self.assertRaises(org.OrgError) as caught:
                org.SecretBox(key).seal("value", aad="user:scope:key")
            self.assertEqual(caught.exception.code, "SECRET_KEY_INVALID")
            self.assertEqual(key.read_bytes(), b"short")
            # An unreadable key path (a directory) fails closed too.
            unreadable = Path(tmp) / "dir-key"
            unreadable.mkdir()
            with self.assertRaises(org.OrgError) as caught:
                org.SecretBox(unreadable).seal("value", aad="user:scope:key")
            self.assertEqual(caught.exception.code, "SECRET_KEY_UNAVAILABLE")


class SecurityRegressionTests(AuthHttpCase):
    """Regression coverage for the security review findings C1/H1/H2/H3/M2/L1-L4."""

    TUNNEL = "192.168.1.20:8891"

    def _pair(self, cookie: str, *, permission: str = "read_only") -> dict:
        response = self.api("/api/mobile/pair", cookie=cookie, method="POST",
                            body={"permission": permission,
                                  "tunnel_host": "192.168.1.20", "tunnel_port": 8891})
        self.assertEqual(response.status, 200, response.text)
        return response.json()

    def _device_cookie(self, cookie: str, *, permission: str = "read_only") -> str:
        pair = self._pair(cookie, permission=permission)
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": pair["code"]}, host=self.TUNNEL)
        self.assertEqual(claim.status, 200, claim.text)
        device = cookie_from(claim, "ark_mobile")
        self.assertTrue(device)
        return device

    def _project(self, name: str) -> dict:
        response = self.api("/api/projects", cookie=self.owner, method="POST",
                            body={"name": name, "template": "blank"})
        self.assertEqual(response.status, 200, response.text)
        return response.json()["summary"]

    def _stop_session(self, session_id: str) -> None:
        """Stop a spawned test agent so Windows can remove its temp workspace."""
        try:
            self.app.sessions.request_stop(session_id)
        except Exception:  # noqa: BLE001 - already finished
            return
        for _ in range(100):
            try:
                if not self.app.sessions.get(session_id).is_active:
                    return
            except Exception:  # noqa: BLE001 - removed
                return
            time.sleep(0.05)

    def test_project_admin_cannot_configure_task_agent_command(self):
        for payload in (
            {"agents": {"providers": {"claude": {"command": "touch PWNED"}}}},
            {"agents": {"providers": {"claude": {"label": "claude"}}}},
            {"agents": {"providers": {"evil": {"env_names": ["DEEPSEEK_API_KEY"]}}}},
            {"agents": {"providers": {"claude": {"env_names": ["ARK_AUTH_PASSWORD"]}}}},
            {"agents": {"providers": {"claude": {"env_names": ["GITHUB_TOKEN"]}}}},
            {"agents": {"providers": {"claude": {"env_names": ["AWS_SECRET_ACCESS_KEY"]}}}},
            {"assistant": {"api_key_env": "ARK_AUTH_PASSWORD"}},
            {"assistant": {"api_key_env": "GITHUB_TOKEN"}},
            {"assistant": {"api_key_env": "MY_CUSTOM_MODEL_KEY"}},
        ):
            with self.subTest(payload=payload):
                response = self.api("/api/project/settings", cookie=self.owner,
                                    method="PATCH", body=payload)
                self.assertEqual(response.status, 400, response.text)
        # Provider env names and a known provider key remain allowed at project
        # scope; commands remain allowed in personal settings only.
        allowed = self.api("/api/project/settings", cookie=self.owner,
                           method="PATCH", body={
                               "assistant": {"api_key_env": "DEEPSEEK_API_KEY"},
                               "agents": {"providers": {"claude": {
                                   "env_names": ["DEEPSEEK_API_KEY"]}}},
                           })
        self.assertEqual(allowed.status, 200, allowed.text)
        self.assertNotIn("command",
                         allowed.json()["settings"]["agents"]["providers"]["claude"])
        personal = self.api("/api/personal/settings?scope=agents",
                            cookie=self.owner, method="POST",
                            body={"providers": {"claude": {
                                "command": "claude -p {prompt}"}}})
        self.assertEqual(personal.status, 200, personal.text)

    def test_terminals_are_scoped_to_the_current_project(self):
        first_id = self.project_id()
        second = self._project("Second Project")
        second_id = second["id"]
        started = self.api("/api/workspace/terminals", cookie=self.owner,
                           method="POST", body={"command": "echo hello"})
        self.assertEqual(started.status, 200, started.text)
        terminal = started.json()["terminal"]
        self.assertEqual(terminal["project_id"], second_id)
        self.assertEqual(terminal["creator"], "owner")
        self.assertEqual(self.api("/api/projects/open", cookie=self.owner,
                                  method="POST", body={"id": first_id}).status, 200)
        # The owner is project admin here but the terminal belongs to another
        # project: list and every by-id action must 404.
        listed = self.api("/api/workspace/terminals", cookie=self.owner).json()
        self.assertEqual([item["id"] for item in listed["terminals"]], [])
        terminal_id = terminal["id"]
        for path, body in (
            (f"/api/workspace/terminals/{terminal_id}", None),
            (f"/api/workspace/terminals/{terminal_id}/stdin", {"text": "x"}),
            (f"/api/workspace/terminals/{terminal_id}/resize", {"cols": 80, "rows": 20}),
            (f"/api/workspace/terminals/{terminal_id}/cancel", {}),
        ):
            method = "POST" if body is not None else "GET"
            with self.subTest(path=path):
                response = self.api(path, cookie=self.owner, method=method, body=body)
                self.assertEqual(response.status, 404, response.text)
        self.assertEqual(self.api(f"/api/workspace/terminals/{terminal_id}/stream",
                                  cookie=self.owner).status, 404)
        self.assertEqual(self.api(f"/api/workspace/terminals/{terminal_id}",
                                  cookie=self.owner, method="DELETE").status, 404)
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        self.assertEqual(self.grant(first_id, user="alice",
                                    permission="read").status, 200)
        self.assertEqual(self.api("/api/workspace/terminals",
                                  cookie=alice).json()["terminals"], [])
        self.assertEqual(self.api(f"/api/workspace/terminals/{terminal_id}",
                                  cookie=alice).status, 404)

    def test_paired_device_has_explicit_allowlist_and_scoped_org(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice",
                                    permission="write").status, 200)
        device = self._device_cookie(alice, permission="full")
        overview = self.api("/api/org", cookie=device, host=self.TUNNEL)
        self.assertEqual(overview.status, 200, overview.text)
        data = overview.json()
        self.assertTrue(data["device"])
        self.assertEqual([item["id"] for item in data["projects"]], [project_id])
        self.assertNotIn("members", data)
        self.assertNotIn("audit", data)
        self.assertEqual(data["teams"], [])
        for path, method in (
            ("/api/personal/settings?scope=assistant", "GET"),
            ("/api/providers", "GET"),
            ("/api/org/users", "GET"),
            ("/api/settings/effective", "GET"),
            ("/api/assistant/config", "GET"),
        ):
            with self.subTest(path=path):
                response = self.api(path, cookie=device, host=self.TUNNEL,
                                    method=method)
                self.assertEqual(response.status, 403, response.text)
                self.assertEqual(response.json()["error"]["code"], "MOBILE_SCOPE")
        creation = self.api("/api/projects", cookie=device, host=self.TUNNEL,
                            method="POST", body={"name": "Device Project"})
        self.assertEqual(creation.status, 403)
        listed = self.api("/api/projects", cookie=device, host=self.TUNNEL).json()
        self.assertEqual([item["id"] for item in listed["projects"]], [project_id])

    def test_query_mobile_token_is_get_only_for_read_resources(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice",
                                    permission="read").status, 200)
        pair = self._pair(alice, permission="read_only")
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": pair["code"]}, host=self.TUNNEL)
        token = claim.json()["mobile_token"]
        get = request(self.base, "/api/project?mobile_token=" + urllib.parse.quote(token),
                      headers={"Host": self.TUNNEL})
        self.assertEqual(get.status, 200, get.text)
        post = request(self.base, "/api/units?mobile_token=" + urllib.parse.quote(token),
                       method="POST", body=json.dumps({"title": "query write", "type": "note"}),
                       headers={"Host": self.TUNNEL, "Content-Type": "application/json",
                                "Origin": f"http://{self.TUNNEL}"})
        self.assertEqual(post.status, 401, post.text)
        units = self.api("/api/units?q=query", cookie=self.owner,
                         host=self.TUNNEL).json()
        self.assertNotIn("query write", json.dumps(units))

    def test_forwarded_proto_requires_trust_proxy(self):
        page = request(self.base, "/login?next=%2F", headers={
            "Host": self.host, "X-Forwarded-Proto": "https"})
        self.assertEqual(page.status, 200)
        self.assertIn("plain HTTP", page.text)
        self.app.trust_proxy = True
        try:
            page = request(self.base, "/login?next=%2F", headers={
                "Host": self.host, "X-Forwarded-Proto": "https"})
            self.assertIn("This page is served over HTTPS", page.text)
        finally:
            self.app.trust_proxy = False

    def test_project_admin_cannot_open_another_projects_session_artifact(self):
        first_id = self.project_id()
        self._project("Other Session Project")
        started = self.api("/api/agent-sessions", cookie=self.owner, method="POST",
                           body={"platform": "echo", "prompt": "hello"})
        self.assertEqual(started.status, 200, started.text)
        session_id = started.json()["session"]["id"]
        self.assertEqual(self.api("/api/projects/open", cookie=self.owner,
                                  method="POST", body={"id": first_id}).status, 200)
        opened = self.api("/api/open", cookie=self.owner, method="POST",
                          body={"session": session_id, "path": "ark-context.json"})
        self.assertEqual(opened.status, 404, opened.text)
        self.assertEqual(opened.json()["error"]["code"], "SESSION_NOT_ACCESSIBLE")
        self._stop_session(session_id)

    def test_task_agent_read_token_works_in_account_mode(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        project_id = self.project_id()
        self.assertEqual(self.grant(project_id, user="alice",
                                    permission="write").status, 200)
        started = self.api("/api/agent-sessions", cookie=alice, method="POST",
                           body={"platform": "echo", "prompt": "hello"})
        self.assertEqual(started.status, 200, started.text)
        session_id = started.json()["session"]["id"]
        token = self.app.sessions.get(session_id).meta.get("read_token")
        self.assertTrue(token)
        read = request(self.base, "/api/task-agent/v1/context", headers={
            "Host": self.host, "X-ARK-Read-Token": token})
        self.assertEqual(read.status, 200, read.text)
        self.assertEqual(read.json()["project"]["id"], project_id)
        self._stop_session(session_id)

    def test_task_subprocess_env_excludes_global_provider_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            manager = SessionManager(
                Path(tmp) / "sessions",
                project_provider=lambda: {},
                context_provider=lambda: {},
                agent_config_provider=lambda actor: {
                    "presets": {"claude": {"env": {
                        "ANTHROPIC_API_KEY": "personal-only-value"}}}},
            )
            session = SimpleNamespace(id="s_security_test", meta={
                "access_mode": "read_only", "platform": "claude", "worktree": None})
            with mock.patch.dict(os.environ, {
                "OPENAI_API_KEY": "global-openai",
                "TENCENTHUB_API_KEY": "global-tencent",
                "ANTHROPIC_API_KEY": "global-anthropic",
                "SAFE_VAR": "kept",
                "SSH_AUTH_SOCK": "/tmp/sock",
            }, clear=False):
                env = manager._build_env(
                    session, {"id": "p", "name": "P", "path": tmp}, "",
                    "read-token", Path(tmp), Path(tmp), actor="alice")
        self.assertNotIn("OPENAI_API_KEY", env)
        self.assertNotIn("TENCENTHUB_API_KEY", env)
        self.assertEqual(env.get("ANTHROPIC_API_KEY"), "personal-only-value")
        self.assertEqual(env.get("SAFE_VAR"), "kept")
        self.assertEqual(env.get("SSH_AUTH_SOCK"), "/tmp/sock")

    def test_auth_mode_task_assistant_does_not_inject_global_model_key(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        self.assertEqual(self.grant(self.project_id(), user="alice",
                                    permission="read").status, 200)
        self.app.set_request_context(user="alice")
        with mock.patch("arkstudio.assistant_config.load", return_value={
                "api_key": "global-model-key", "base_url": "https://x.example/v1",
                "model": "m", "reasoning_effort": "high", "timeout": 90}):
            resolved = self.app.assistant_config_for("alice")
        self.assertEqual(resolved["api_key"], "")
        self.assertFalse(resolved["configured"])


@contextmanager
def popen_in_this_thread(fake=None):
    """Replace subprocess.Popen for the calling thread only; yields its argv list.

    mock.patch on subprocess.Popen is process-wide: a server, session or git
    thread still running from this or an earlier test that spawns a process
    meanwhile (subprocess.run uses Popen) would be recorded as the launch under
    test and receive the fake.  Other threads keep the real Popen here.
    """
    calls = []
    real, owner = subprocess.Popen, threading.get_ident()

    def popen(*args, **kwargs):
        if threading.get_ident() != owner:
            return real(*args, **kwargs)
        calls.append(list(args[0] if args else kwargs["args"]))
        return fake

    with mock.patch("arkstudio.sessions.subprocess.Popen", popen):
        yield calls


class LinuxAccountTests(AuthHttpCase):
    """Admin-assigned Linux accounts, selector validation and fail-closed launch."""

    REQUIREMENT_ARGS = {"resolver": None}

    def _resolver(self, account):
        accounts = {
            "alice": {"uid": 1000, "home": "/home/alice"},
            "bob": {"uid": 1001, "home": "/home/bob"},
            "root": {"uid": 0, "home": "/root"},
            "system": {"uid": 500, "home": "/home/system"},
        }
        return accounts.get(account)

    def _fake_validate(self, account, **kwargs):
        entry = self._resolver(account)
        if not entry:
            raise linux_account.LinuxAccountError(
                "not found", 400, "LINUX_ACCOUNT_NOT_FOUND")
        return {"account": account, "uid": entry["uid"], "home": entry["home"]}

    def _assign(self, user_id: str, account: str, *, cookie: str = "") -> Response:
        return self.api(f"/api/org/users/{user_id}/linux-accounts",
                        cookie=cookie or self.owner, method="POST",
                        body={"account": account})

    def test_admin_assignment_and_user_selection(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        alice_id = self.api("/api/org", cookie=alice).json()["user"]["id"]
        with mock.patch("arkstudio.org.linux_account.validate_linux_account",
                        side_effect=self._fake_validate):
            denied = self._assign(alice_id, "alice", cookie=alice)
            self.assertEqual(denied.status, 403)
            self.assertEqual(denied.json()["error"]["code"], "ORG_FORBIDDEN")
            assigned = self._assign(alice_id, "alice")
            self.assertEqual(assigned.status, 200, assigned.text)
            second = self._assign(alice_id, "bob")
            self.assertEqual(second.status, 200, second.text)
        environment = self.api("/api/personal/settings?scope=environment",
                               cookie=alice)
        self.assertEqual(environment.status, 200, environment.text)
        state = environment.json()["settings"]
        self.assertEqual(state["assigned"], ["alice", "bob"])
        self.assertEqual(state["selected"], "")
        unassigned = self.api("/api/personal/settings?scope=environment",
                              cookie=alice, method="POST",
                              body={"linux_account": "root"})
        self.assertEqual(unassigned.status, 403)
        self.assertEqual(unassigned.json()["error"]["code"],
                         "LINUX_ACCOUNT_NOT_ASSIGNED")
        selected = self.api("/api/personal/settings?scope=environment",
                            cookie=alice, method="POST",
                            body={"linux_account": "alice"})
        self.assertEqual(selected.status, 200, selected.text)
        self.assertEqual(selected.json()["settings"]["selected"], "alice")
        with mock.patch("arkstudio.org.linux_account.preflight",
                        return_value={"account": "alice", "uid": 1000,
                                      "home": "/home/alice",
                                      "sudo": "/usr/bin/sudo"}):
            tested = self.api("/api/personal/settings?scope=environment",
                              cookie=alice, method="POST",
                              body={"test": True})
        self.assertEqual(tested.status, 200, tested.text)
        self.assertTrue(tested.json()["ok"])
        revoked = self.api(f"/api/org/users/{alice_id}/linux-accounts/alice",
                           cookie=self.owner, method="DELETE")
        self.assertEqual(revoked.status, 200, revoked.text)
        after = self.api("/api/personal/settings?scope=environment",
                         cookie=alice).json()["settings"]
        self.assertNotIn("alice", after["assigned"])
        self.assertEqual(after["selected"], "")
        self.assertEqual(self.app.linux_account_for("alice"), "")

    def test_validation_rejects_root_system_and_bad_names(self):
        with self.assertRaises(linux_account.LinuxAccountError) as caught:
            linux_account.validate_linux_account("root", resolver=self._resolver)
        self.assertEqual(caught.exception.code, "LINUX_ACCOUNT_SYSTEM")
        with self.assertRaises(linux_account.LinuxAccountError) as caught:
            linux_account.validate_linux_account("system", resolver=self._resolver)
        self.assertEqual(caught.exception.code, "LINUX_ACCOUNT_SYSTEM")
        with self.assertRaises(linux_account.LinuxAccountError) as caught:
            linux_account.validate_linux_account("alice;rm -rf /",
                                                 resolver=self._resolver)
        self.assertEqual(caught.exception.code, "LINUX_ACCOUNT_INVALID")
        with self.assertRaises(linux_account.LinuxAccountError) as caught:
            linux_account.validate_linux_account("alice", resolver=self._resolver,
                                                 server_user="alice")
        self.assertEqual(caught.exception.code, "LINUX_ACCOUNT_SERVER_ACCOUNT")
        valid = linux_account.validate_linux_account("alice",
                                                     resolver=self._resolver)
        self.assertEqual(valid["home"], "/home/alice")

    def test_command_wrapper_has_no_password_and_sets_home(self):
        with mock.patch("arkstudio.linux_account.sudo_binary",
                        return_value="/usr/bin/sudo"):
            argv = linux_account.wrap_command(
                ["claude", "-p", "hello"], "alice", home="/home/alice")
        self.assertEqual(argv[:7], ["/usr/bin/sudo", "-n", "-E", "-u", "alice",
                                    "--", "/usr/bin/env"])
        self.assertIn("HOME=/home/alice", argv)
        self.assertIn("XDG_CONFIG_HOME=/home/alice/.config", argv)
        self.assertEqual(argv[-3:], ["claude", "-p", "hello"])
        self.assertFalse(any(part.lower().startswith("--password") for part in argv))

    def test_preflight_denied_and_workspace_checks(self):
        denied = SimpleNamespace(returncode=1, stderr="sudo: a password is required")
        ok = SimpleNamespace(returncode=0, stderr="")
        fail = SimpleNamespace(returncode=1, stderr="")
        with mock.patch("arkstudio.linux_account.sudo_binary",
                        return_value="/usr/bin/sudo"):
            with self.assertRaises(linux_account.LinuxAccountError) as caught:
                linux_account.preflight("alice", resolver=self._resolver,
                                        runner=lambda *a, **k: denied)
            self.assertEqual(caught.exception.code, "LINUX_SUDO_DENIED")
            self.assertIn("NOPASSWD", caught.exception.message)
            calls = iter([ok, fail])

            def runner(*args, **kwargs):
                return next(calls)

            with self.assertRaises(linux_account.LinuxAccountError) as caught:
                linux_account.preflight("alice", resolver=self._resolver,
                                        workspace="/tmp/workspace",
                                        runner=runner)
            self.assertEqual(caught.exception.code, "LINUX_WORKSPACE_NOT_WRITABLE")

    def test_no_silent_fallback_when_sudo_is_unavailable(self):
        manager = SessionManager(
            Path(self.tmp.name) / "isolation-sessions",
            project_provider=lambda: {
                "id": "p", "name": "P",
                "path": str(Path(self.tmp.name) / "projects")},
            context_provider=lambda: {},
            linux_account_provider=lambda actor: "alice",
        )
        with mock.patch("arkstudio.sessions.linux_account.preflight",
                        side_effect=linux_account.LinuxAccountError(
                            "passwordless sudo denied", 409, "LINUX_SUDO_DENIED")), \
                popen_in_this_thread() as launched:
            with self.assertRaises(SessionError) as caught:
                manager.start({"platform": "echo", "prompt": "hello"},
                              actor="alice")
        self.assertEqual(caught.exception.code, "LINUX_SUDO_DENIED")
        self.assertEqual(launched, [])  # no silent fallback to the server account

    def test_launch_wraps_command_when_assigned(self):
        manager = SessionManager(
            Path(self.tmp.name) / "wrapped-sessions",
            project_provider=lambda: {
                "id": "p", "name": "P",
                "path": str(Path(self.tmp.name) / "projects")},
            context_provider=lambda: {},
            linux_account_provider=lambda actor: "alice",
        )
        fake_proc = mock.MagicMock()
        fake_proc.pid = 4242
        fake_proc.returncode = 0
        fake_proc.stdin = None
        fake_proc.stdout = io.BytesIO(b"")
        fake_proc.stderr = io.BytesIO(b"")
        fake_proc.poll.return_value = 0
        fake_proc.wait.return_value = 0
        with mock.patch("arkstudio.sessions.linux_account.preflight",
                        return_value={"account": "alice", "uid": 1000,
                                      "home": "/home/alice",
                                      "sudo": "/usr/bin/sudo"}), \
                mock.patch("arkstudio.sessions.linux_account.wrap_command",
                           return_value=["sudo", "-n", "-u", "alice", "--",
                                         "echo", "hello"]) as wrap, \
                popen_in_this_thread(fake_proc) as launched:
            manager.start({"platform": "echo", "prompt": "hello"}, actor="alice")
        wrap.assert_called_once()
        self.assertEqual(len(launched), 1, launched)
        self.assertEqual(launched[0][:4], ["sudo", "-n", "-u", "alice"])


if __name__ == "__main__":
    unittest.main()
