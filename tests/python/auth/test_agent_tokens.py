"""Personal agent tokens: per-account bearer tokens that act as the user.

Real stdlib HTTP server on 127.0.0.1 with account auth (owner + members) and a
loopback no-auth case.  Covers storage (hash only, shown once), ownership,
project grants capped by scope, lifecycle (revoke, disable, delete, expiry),
the failure limiter, minting restrictions, attribution and whoami.
"""
from __future__ import annotations

import json
import sqlite3
import time
import unittest
from unittest import mock

from arkstudio import agent_tokens, org
from tests.python.auth import test_org
from tests.python.auth.test_org import AuthHttpCase, request


class PersonalTokenHttpTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.assertEqual(self.create_user("bob", "bob-pass").status, 200)
        self.bob = self.login("bob", "bob-pass")
        self.main_id = self.project_id()
        self.assertEqual(self.grant(self.main_id, user="bob", permission="write").status, 200)

    # ------------------------------------------------------------- helpers
    def mint(self, cookie: str, **body) -> dict:
        response = self.api("/api/personal/agent-tokens", cookie=cookie, method="POST",
                            body={"label": "test", **body})
        self.assertEqual(response.status, 200, response.text)
        return response.json()

    def agent(self, token: str, path: str, *, method: str = "GET", body=None,
              project: str = "", name: str = ""):
        headers = {"Host": self.host, "Authorization": f"Bearer {token}",
                   "Content-Type": "application/json"}
        if project:
            headers["X-ARK-Project"] = project
        if name:
            headers["X-Agent-Name"] = name
        return request(self.base, path, method=method, headers=headers,
                       body=json.dumps(body) if body is not None else None)

    def other_project(self) -> str:
        created = self.api("/api/projects", cookie=self.owner, method="POST",
                           body={"name": "Owner only", "template": "blank"})
        self.assertEqual(created.status, 200, created.text)
        return created.json()["summary"]["id"]

    # --------------------------------------------------------------- tests
    def test_only_the_hash_is_stored_and_the_value_is_shown_once(self):
        data = self.mint(self.bob, scope="write")
        secret = data["token"]
        self.assertTrue(secret.startswith("arka_"))
        self.assertEqual(data["item"]["prefix"], secret[:9])
        listed = self.api("/api/personal/agent-tokens", cookie=self.bob)
        self.assertEqual(listed.status, 200)
        self.assertNotIn(secret, listed.text)
        self.assertNotIn("token_hash", listed.text)
        self.assertEqual(len(listed.json()["tokens"]), 1)
        with sqlite3.connect(str(self.app.org.path)) as con:
            rows = con.execute("SELECT token_hash, prefix FROM agent_tokens").fetchall()
        self.assertEqual(rows[0][0], agent_tokens.token_hash(secret))
        raw = self.app.org.path.read_bytes()
        self.assertNotIn(secret.encode(), raw)

    def test_users_cannot_see_or_revoke_each_others_tokens(self):
        bob_token = self.mint(self.bob)
        mine = self.api("/api/personal/agent-tokens", cookie=self.owner).json()
        self.assertEqual(mine["tokens"], [])
        self.assertEqual(mine["user"], "owner")
        revoke = self.api(f"/api/personal/agent-tokens/{bob_token['item']['id']}",
                          cookie=self.owner, method="DELETE")
        self.assertEqual(revoke.status, 404, revoke.text)
        self.assertEqual(self.agent(bob_token["token"], "/api/agent/v1/whoami").status, 200)
        anonymous = self.api("/api/personal/agent-tokens")
        self.assertEqual(anonymous.status, 401)

    def test_token_acts_as_the_user_and_writes_are_attributed(self):
        secret = self.mint(self.bob)["token"]
        who = self.agent(secret, "/api/agent/v1/whoami")
        self.assertEqual(who.status, 200, who.text)
        data = who.json()
        self.assertEqual(data["user"], "bob")
        self.assertEqual(data["token_kind"], "personal")
        self.assertEqual(data["token_label"], "test")
        self.assertEqual(data["scope"], "write")
        self.assertIn(self.main_id, [p["id"] for p in data["projects"]])
        created = self.agent(secret, "/api/agent/v1/units", method="POST",
                             body={"title": "By bob's agent", "type": "note"},
                             project=self.main_id)
        self.assertEqual(created.status, 200, created.text)
        unit_id = created.json()["unit"]["id"]
        activity = self.api("/api/activity?limit=5", cookie=self.owner).json()["activity"]
        self.assertTrue(any(item.get("actor") == "agent:bob" for item in activity), activity)
        named = self.agent(secret, f"/api/agent/v1/units/{unit_id}", method="POST",
                           body={"title": "Edited by codex"}, project=self.main_id, name="codex")
        self.assertEqual(named.status, 200, named.text)
        activity = self.api("/api/activity?limit=3", cookie=self.owner).json()["activity"]
        self.assertEqual(activity[0].get("actor"), "agent:bob/codex")
        mine = self.agent(secret, "/api/agent/v1/units?assignee=me&limit=5",
                          project=self.main_id)
        self.assertEqual(mine.status, 200, mine.text)

    def test_grants_and_scope_cap_project_access(self):
        hidden = self.other_project()
        secret = self.mint(self.bob)["token"]
        denied = self.agent(secret, "/api/agent/v1/context", project=hidden)
        self.assertEqual(denied.status, 404, denied.text)
        self.assertEqual(denied.json()["error"]["code"], "PROJECT_NOT_ACCESSIBLE")
        self.assertNotIn(hidden, [p["id"] for p in
                                  self.agent(secret, "/api/agent/v1/whoami").json()["projects"]])
        # A read grant caps a write token.
        self.assertEqual(self.grant(self.main_id, user="bob", permission="read").status, 200)
        capped = self.agent(secret, "/api/agent/v1/units", method="POST",
                            body={"title": "nope", "type": "note"}, project=self.main_id)
        self.assertEqual(capped.status, 403, capped.text)
        self.assertEqual(self.grant(self.main_id, user="bob", permission="write").status, 200)
        read_token = self.mint(self.bob, scope="read")["token"]
        self.assertEqual(self.agent(read_token, "/api/agent/v1/context",
                                    project=self.main_id).status, 200)
        blocked = self.agent(read_token, "/api/agent/v1/units", method="POST",
                             body={"title": "nope", "type": "note"}, project=self.main_id)
        self.assertEqual(blocked.status, 403, blocked.text)
        self.assertEqual(blocked.json()["error"]["code"], "AGENT_TOKEN_SCOPE")
        who = self.agent(read_token, "/api/agent/v1/whoami").json()
        self.assertTrue(all(p["permission"] == "read" for p in who["projects"]))
        # A personal token unlocks nothing outside the agent API.
        self.assertEqual(self.agent(secret, "/api/project").status, 401)
        self.assertEqual(self.agent(secret, "/api/personal/agent-tokens").status, 401)

    def test_revoke_disable_delete_and_expiry_stop_tokens(self):
        first = self.mint(self.bob)
        self.assertEqual(self.agent(first["token"], "/api/agent/v1/whoami").status, 200)
        revoked = self.api(f"/api/personal/agent-tokens/{first['item']['id']}",
                           cookie=self.bob, method="DELETE")
        self.assertEqual(revoked.status, 200, revoked.text)
        self.assertEqual(revoked.json()["tokens"], [])
        gone = self.agent(first["token"], "/api/agent/v1/whoami")
        self.assertEqual(gone.status, 401)
        self.assertEqual(gone.json()["error"]["code"], "AGENT_TOKEN_INVALID")
        # Expiry
        expiring = self.mint(self.bob, expires_days=30)
        self.assertTrue(expiring["item"]["expires_at"])
        later = time.time() + 31 * 86400
        with mock.patch.object(self.app.org.agent_tokens, "clock", lambda: later):
            self.assertEqual(self.agent(expiring["token"], "/api/agent/v1/whoami").status, 401)
        self.assertEqual(self.agent(expiring["token"], "/api/agent/v1/whoami").status, 200)
        bad_expiry = self.api("/api/personal/agent-tokens", cookie=self.bob, method="POST",
                              body={"expires_days": 7})
        self.assertEqual(bad_expiry.status, 400)
        # Disabling the user revokes; re-enabling does not bring tokens back.
        bob_id = self.app.org.user_id_for("bob")
        self.assertEqual(self.api(f"/api/org/users/{bob_id}", cookie=self.owner,
                                  method="PATCH", body={"disabled": True}).status, 200)
        self.assertEqual(self.agent(expiring["token"], "/api/agent/v1/whoami").status, 401)
        self.assertEqual(self.api(f"/api/org/users/{bob_id}", cookie=self.owner,
                                  method="PATCH", body={"disabled": False}).status, 200)
        self.assertEqual(self.agent(expiring["token"], "/api/agent/v1/whoami").status, 401)
        # Deleting removes the rows.
        bob = self.login("bob", "bob-pass")
        last = self.mint(bob)["token"]
        self.assertEqual(self.api(f"/api/org/users/{bob_id}", cookie=self.owner,
                                  method="DELETE").status, 200)
        self.assertEqual(self.agent(last, "/api/agent/v1/whoami").status, 401)
        with sqlite3.connect(str(self.app.org.path)) as con:
            left = con.execute("SELECT COUNT(*) FROM agent_tokens WHERE user_id=?",
                               (bob_id,)).fetchone()[0]
        self.assertEqual(left, 0)

    def test_password_change_keeps_tokens_unless_asked(self):
        secret = self.mint(self.bob)["token"]
        changed = self.api("/api/org/password", cookie=self.bob, method="POST",
                           body={"current_password": "bob-pass", "new_password": "bob-pass-2"})
        self.assertEqual(changed.status, 200, changed.text)
        self.assertEqual(self.agent(secret, "/api/agent/v1/whoami").status, 200)
        changed = self.api("/api/org/password", cookie=self.bob, method="POST",
                           body={"current_password": "bob-pass-2", "new_password": "bob-pass-3",
                                 "revoke_agent_tokens": True})
        self.assertEqual(changed.status, 200, changed.text)
        self.assertEqual(changed.json()["agent_tokens_revoked"], 1)
        self.assertEqual(self.agent(secret, "/api/agent/v1/whoami").status, 401)
        self.assertEqual(self.app.org.revoke_user_tokens("nobody"), 0)

    def test_profile_password_change_keeps_tokens_unless_ticked(self):
        secret = self.mint(self.bob)["token"]
        changed = self.api("/api/me/password", cookie=self.bob, method="POST",
                           body={"current_password": "bob-pass", "new_password": "bob-pass-22",
                                 "confirm_password": "bob-pass-22"})
        self.assertEqual(changed.status, 200, changed.text)
        self.assertEqual(changed.json()["agent_tokens_revoked"], 0)
        self.assertEqual(self.agent(secret, "/api/agent/v1/whoami").status, 200)
        changed = self.api("/api/me/password", cookie=self.bob, method="POST",
                           body={"current_password": "bob-pass-22", "new_password": "bob-pass-33",
                                 "confirm_password": "bob-pass-33", "revoke_agent_tokens": True})
        self.assertEqual(changed.status, 200, changed.text)
        self.assertEqual(changed.json()["agent_tokens_revoked"], 1)
        self.assertEqual(self.agent(secret, "/api/agent/v1/whoami").status, 401)

    def test_failed_bearer_attempts_are_rate_limited(self):
        secret = self.mint(self.bob)["token"]
        limiter = self.app.agent_token_failures
        for _ in range(limiter.limit):
            self.assertEqual(self.agent("arka_wrong", "/api/agent/v1/whoami").status, 401)
        limited = self.agent(secret, "/api/agent/v1/whoami")
        self.assertEqual(limited.status, 429, limited.text)
        self.assertEqual(limited.json()["error"]["code"], "AGENT_TOKEN_RATE_LIMITED")

    def test_limit_of_active_tokens(self):
        for index in range(agent_tokens.MAX_ACTIVE_TOKENS):
            self.mint(self.bob, label=f"t{index}")
        over = self.api("/api/personal/agent-tokens", cookie=self.bob, method="POST",
                        body={"label": "one too many"})
        self.assertEqual(over.status, 409)
        self.assertEqual(over.json()["error"]["code"], "AGENT_TOKEN_LIMIT")

    def test_agents_phones_and_task_tokens_cannot_mint(self):
        secret = self.mint(self.bob)["token"]
        with_bearer = self.api("/api/personal/agent-tokens", cookie=self.bob, method="POST",
                               body={}, headers={"Authorization": f"Bearer {secret}"})
        self.assertEqual(with_bearer.status, 403)
        with_read = self.api("/api/personal/agent-tokens", cookie=self.bob, method="POST",
                             body={}, headers={"X-ARK-Read-Token": "anything"})
        self.assertEqual(with_read.status, 403)
        pair = self.api("/api/mobile/pair", cookie=self.bob, method="POST",
                        body={"permission": "read_only", "tunnel_host": "192.168.1.20",
                              "tunnel_port": 8891})
        self.assertEqual(pair.status, 200, pair.text)
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": pair.json()["code"]}, host="192.168.1.20:8891")
        mobile = {"X-ARK-Mobile-Token": claim.json()["mobile_token"],
                  "Host": "192.168.1.20:8891"}
        for method in ("GET", "POST"):
            phone = self.api("/api/personal/agent-tokens", method=method,
                             body={} if method == "POST" else None, headers=mobile)
            self.assertEqual(phone.status, 403, phone.text)
        self.assertEqual(len(self.app.org.list_agent_tokens("bob")["tokens"]), 1)

    def test_tokens_are_only_created_on_explicit_request(self):
        for path in ("/api/bootstrap", "/api/personal/agent-tokens", "/api/agent/setup"):
            self.api(path, cookie=self.bob)
        self.assertEqual(self.app.org.list_agent_tokens("bob")["tokens"], [])

    def test_legacy_server_token_still_works_and_is_attributed_agent_api(self):
        server_token = self.app.manager.agent_token
        self.assertEqual(self.api("/api/agent/token", cookie=self.bob).status, 403)
        self.assertEqual(self.api("/api/agent/token", cookie=self.owner).json()["token"],
                         server_token)
        created = self.api("/api/agent/v1/units", cookie=self.owner, method="POST",
                           body={"title": "legacy", "type": "note"},
                           headers={"Authorization": f"Bearer {server_token}"})
        self.assertEqual(created.status, 200, created.text)
        activity = self.api("/api/activity?limit=3", cookie=self.owner).json()["activity"]
        self.assertEqual(activity[0].get("actor"), "agent:api")
        who = self.api("/api/agent/v1/whoami", cookie=self.owner,
                       headers={"Authorization": f"Bearer {server_token}"}).json()
        self.assertEqual(who["token_kind"], "server")
        self.assertEqual(who["actor"], "agent:api")
        wrong = self.api("/api/agent/v1/context", cookie=self.owner,
                         headers={"Authorization": "Bearer not-the-token"})
        self.assertEqual(wrong.status, 401)


class LocalModePersonalTokenTests(test_org.LoopbackCompatibilityTests):
    def test_single_user_loopback_experience_is_unchanged(self):
        pass  # covered in test_org

    def test_local_user_tokens_act_as_local(self):
        made = self.api("/api/personal/agent-tokens", method="POST",
                        body={"label": "Home starter prompt", "scope": "write"})
        self.assertEqual(made.status, 200, made.text)
        secret = made.json()["token"]
        headers = {"Authorization": f"Bearer {secret}", "Content-Type": "application/json"}
        who = request(self.base, "/api/agent/v1/whoami", headers=headers)
        self.assertEqual(who.status, 200, who.text)
        self.assertEqual(who.json()["user"], "local")
        self.assertEqual(len(who.json()["projects"]), 1)
        created = request(self.base, "/api/agent/v1/units", method="POST", headers=headers,
                          body=json.dumps({"title": "local agent", "type": "note"}))
        self.assertEqual(created.status, 200, created.text)
        activity = self.api("/api/activity?limit=2").json()["activity"]
        self.assertEqual(activity[0].get("actor"), "agent:local")
        legacy = request(self.base, "/api/agent/v1/context", headers={
            "Authorization": f"Bearer {self.app.manager.agent_token}"})
        self.assertEqual(legacy.status, 200)


class TokenStoreUnitTests(unittest.TestCase):
    def test_migration_is_idempotent_and_revoke_is_owner_scoped(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            manager = org.OrgManager(f"{tmp}/org.sqlite3", private_dir=tmp)
            manager.bootstrap()
            again = org.OrgManager(f"{tmp}/org.sqlite3", private_dir=tmp)
            secret, item = again.agent_tokens.create("usr_local", label="x")
            self.assertFalse(manager.agent_tokens.revoke("usr_other", item["id"]))
            self.assertIsNotNone(manager.agent_tokens.authenticate(secret))
            self.assertIsNone(manager.agent_tokens.authenticate(secret + "x"))
            self.assertTrue(manager.agent_tokens.revoke("usr_local", item["id"]))
            self.assertIsNone(manager.agent_tokens.authenticate(secret))


if __name__ == "__main__":
    unittest.main()
