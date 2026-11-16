"""Native-client account sign-in: ``POST /api/mobile/login``.

The Android app never stores the account password.  It exchanges the password
once for a revocable device token (``arkm_…``) that behaves exactly like a
claimed QR pairing.  These tests run the real stdlib server on 127.0.0.1.
"""
from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

from arkstudio import auth, mobile
from arkstudio.server import App, serve

from tests.python.auth.test_auth import AuthHttpMixin, FakeClock, cookie_from, csrf_from, request


class MobileLoginTests(unittest.TestCase):
    # Reuse the auth server fixture without inheriting its test methods.
    SESSION_TTL = AuthHttpMixin.SESSION_TTL
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

    login_page = AuthHttpMixin.login_page
    login = AuthHttpMixin.login

    def setUp(self):
        AuthHttpMixin.setUp(self)
        self.app.public_url = "https://ark.example.net/"
        self.app.allow_advertised_url(self.app.public_url)
        self.app.configure_auth_transport()
        self.phone = "ark.example.net"

    # ------------------------------------------------------------ helpers
    def mobile_login(self, password="secret-pass", username="ark", **extra):
        body = {"username": username, "password": password, "device": "Pixel test"}
        body.update(extra)
        return request(self.base, "/api/mobile/login", method="POST",
                       body=json.dumps(body),
                       headers={"Host": self.phone, "Content-Type": "application/json",
                                "User-Agent": "ARKStudioAndroid/3.0"})

    def api(self, path, token, *, method="GET", body=None):
        headers = {"Host": self.phone, "X-ARK-Mobile-Token": token}
        if body is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(body)
        return request(self.base, path, method=method, body=body, headers=headers)

    def desktop_cookie(self):
        csrf = csrf_from(self.login_page(host=self.phone).text)
        return cookie_from(self.login(csrf=csrf, host=self.phone, next_path="/"))

    # -------------------------------------------------------------- tests
    def test_password_login_returns_device_token_without_cookie(self):
        response = self.mobile_login()
        self.assertEqual(response.status, 200, response.text)
        data = response.json()
        self.assertTrue(data["mobile_token"].startswith("arkm_"))
        self.assertEqual(data["token_type"], "X-ARK-Mobile-Token")
        self.assertEqual(data["source"], "password")
        self.assertEqual(data["user"], "ark")
        self.assertEqual(data["permission"], "full")
        self.assertTrue(data["permanent"])
        self.assertTrue(data["projects"])
        self.assertNotIn("set-cookie", response.headers)
        self.assertNotIn("secret-pass", response.text)
        token = data["mobile_token"]
        project = self.api("/api/project", token)
        self.assertEqual(project.status, 200, project.text)
        status = self.api("/api/mobile/session", token).json()
        self.assertTrue(status["authenticated"])
        self.assertEqual(status["source"], "password")
        self.assertEqual(status["user"], "ark")
        # A device token is not a desktop account session.
        account = self.api("/api/auth/session", token).json()
        self.assertFalse(account["authenticated"])
        # Device scope still applies: no agent token, no org admin.
        self.assertEqual(self.api("/api/agent/token", token).status, 403)

    def test_wrong_password_is_generic_and_rate_limited(self):
        first = self.mobile_login(password="nope")
        self.assertEqual(first.status, 401)
        self.assertEqual(first.json()["error"]["code"], "AUTH_INVALID")
        unknown = self.mobile_login(username="ghost", password="nope")
        self.assertEqual(unknown.json()["error"]["message"],
                         first.json()["error"]["message"])
        for _ in range(4):
            last = self.mobile_login(password="nope")
        self.assertEqual(last.status, 429)
        self.assertEqual(last.json()["error"]["code"], "AUTH_RATE_LIMITED")
        self.assertTrue(int(last.headers["retry-after"]) >= 1)
        # The shared limiter also blocks the right password until it expires.
        self.assertEqual(self.mobile_login().status, 429)
        self.clock.advance(61)
        self.assertEqual(self.mobile_login().status, 200)

    def test_form_posts_and_cross_origin_are_rejected(self):
        form = request(self.base, "/api/mobile/login", method="POST",
                       body="username=ark&password=secret-pass",
                       headers={"Host": self.phone,
                                "Content-Type": "application/x-www-form-urlencoded"})
        self.assertIn(form.status, {400, 415})
        plain = request(self.base, "/api/mobile/login", method="POST",
                        body=json.dumps({"username": "ark", "password": "secret-pass"}),
                        headers={"Host": self.phone, "Content-Type": "text/plain"})
        self.assertEqual(plain.status, 415)
        cross = request(self.base, "/api/mobile/login", method="POST",
                        body=json.dumps({"username": "ark", "password": "secret-pass"}),
                        headers={"Host": self.phone, "Content-Type": "application/json",
                                 "Origin": "https://evil.example"})
        self.assertEqual(cross.status, 403)

    def test_read_only_request_cannot_write(self):
        data = self.mobile_login(permission="read_only").json()
        self.assertEqual(data["permission"], "read_only")
        write = self.api("/api/units", data["mobile_token"], method="POST",
                         body={"kind": "idea", "title": "x"})
        self.assertEqual(write.status, 403)
        self.assertEqual(write.json()["error"]["code"], "MOBILE_READ_ONLY")

    def test_desktop_lists_and_revokes_password_device(self):
        token = self.mobile_login().json()["mobile_token"]
        cookie = self.desktop_cookie()
        pairs = request(self.base, "/api/mobile/pairs",
                        headers={"Host": self.phone, "Cookie": cookie}).json()["pairs"]
        mine = [item for item in pairs if item["source"] == "password"]
        self.assertEqual(len(mine), 1)
        self.assertEqual(mine[0]["state"], "claimed")
        revoke = request(self.base, f"/api/mobile/pairs/{mine[0]['id']}/revoke",
                         method="POST", body=b"{}",
                         headers={"Host": self.phone, "Cookie": cookie,
                                  "Content-Type": "application/json"})
        self.assertEqual(revoke.status, 200, revoke.text)
        after = self.api("/api/project", token)
        self.assertEqual(after.status, 401)

    def test_sign_out_revokes_token(self):
        token = self.mobile_login().json()["mobile_token"]
        out = self.api("/api/mobile/session", token, method="DELETE")
        self.assertEqual(out.status, 200, out.text)
        self.assertTrue(out.json()["logged_out"])
        self.assertEqual(self.api("/api/project", token).status, 401)

    def test_password_session_can_switch_projects_qr_cannot(self):
        cookie = self.desktop_cookie()
        created = request(self.base, "/api/projects", method="POST",
                           body=json.dumps({"name": "Second project"}),
                           headers={"Host": self.phone, "Cookie": cookie,
                                    "Content-Type": "application/json"})
        self.assertEqual(created.status, 200, created.text)
        data = self.mobile_login(permission="read_only").json()
        token = data["mobile_token"]
        listing = self.api("/api/mobile/session/projects", token).json()
        self.assertTrue(listing["switchable"])
        others = [item for item in listing["projects"] if item["id"] != listing["current"]]
        self.assertTrue(others, listing)
        target = others[0]["id"]
        switched = self.api("/api/mobile/session/project", token, method="POST",
                            body={"project_id": target})
        self.assertEqual(switched.status, 200, switched.text)
        self.assertEqual(switched.json()["project_id"], target)
        self.assertEqual(self.api("/api/project", token).status, 200)
        status = self.api("/api/mobile/session", token).json()
        self.assertEqual(status["project"]["id"], target)
        missing = self.api("/api/mobile/session/project", token, method="POST",
                           body={"project_id": "nope"})
        self.assertEqual(missing.status, 404)
        # A QR-claimed device stays pinned.
        pair = request(self.base, "/api/mobile/pair", method="POST", body=b"{}",
                       headers={"Host": self.phone, "Cookie": cookie,
                                "Content-Type": "application/json"}).json()
        claim = request(self.base, "/api/mobile/pair/claim", method="POST",
                        body=json.dumps({"token": pair["code"]}),
                        headers={"Host": self.phone,
                                 "Content-Type": "application/json"}).json()
        qr_token = claim["mobile_token"]
        listing = self.api("/api/mobile/session/projects", qr_token).json()
        self.assertFalse(listing["switchable"])
        self.assertEqual(len(listing["projects"]), 1)
        pinned = self.api("/api/mobile/session/project", qr_token, method="POST",
                          body={"project_id": target})
        self.assertEqual(pinned.status, 403)
        self.assertEqual(pinned.json()["error"]["code"], "MOBILE_PROJECT_PINNED")

    def test_unknown_project_on_login(self):
        response = self.mobile_login(project_id="does-not-exist")
        self.assertEqual(response.status, 404)


class MobileLoginWithoutAuthTests(unittest.TestCase):
    def test_no_account_configured(self):
        old = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        tmp = tempfile.TemporaryDirectory()
        app = App(Path(tmp.name) / "projects", seed_demo=True,
                  task_sessions_root=Path(tmp.name) / "sessions")
        httpd = serve(app, port=0, block=False)
        try:
            base = f"http://127.0.0.1:{httpd.server_address[1]}"
            response = request(base, "/api/mobile/login", method="POST",
                               body=json.dumps({"username": "a", "password": "b"}),
                               headers={"Content-Type": "application/json"})
            self.assertEqual(response.status, 400)
            self.assertEqual(response.json()["error"]["code"], "AUTH_NOT_CONFIGURED")
        finally:
            httpd.shutdown()
            httpd.server_close()
            app.workspace.shutdown()
            app.project.close()
            tmp.cleanup()
            if old is None:
                os.environ.pop("ARK_CHAT_MODE", None)
            else:
                os.environ["ARK_CHAT_MODE"] = old


class PairingSourceTests(unittest.TestCase):
    def test_source_persists_and_rebind_is_password_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp) / "m.json"
            manager = mobile.PairingManager(state_path=state)
            project = {"id": "p1", "name": "One"}
            pair = manager.create(project, user="ark", source="password")
            token = manager.claim(pair["token"], project)["mobile_token"]
            qr = manager.create(project, user="ark")
            qr_token = manager.claim(qr["token"], project)["mobile_token"]
            reloaded = mobile.PairingManager(state_path=state)
            session = reloaded.session_for_token(token)
            self.assertEqual(reloaded.pairing_for(session).source, "password")
            moved = reloaded.rebind(token, {"id": "p2", "name": "Two"},
                                    permission="read_only")
            self.assertEqual((moved.project_id, moved.permission), ("p2", "read_only"))
            with self.assertRaises(mobile.MobileError) as ctx:
                reloaded.rebind(qr_token, {"id": "p2", "name": "Two"}, permission="full")
            self.assertEqual(ctx.exception.code, "MOBILE_PROJECT_PINNED")
            # Old records without a source load as QR pairings.
            data = json.loads(state.read_text(encoding="utf-8"))
            for item in data["pairings"]:
                item.pop("source", None)
            state.write_text(json.dumps(data), encoding="utf-8")
            legacy = mobile.PairingManager(state_path=state)
            self.assertEqual(legacy.pairing_for(legacy.session_for_token(token)).source, "qr")

    def test_verify_credentials_issues_no_session(self):
        manager = auth.AuthManager("ark", password="pw", iterations=1000, clock=FakeClock())
        self.assertEqual(manager.verify_credentials("ark", "pw"), "ark")
        self.assertEqual(len(manager._sessions), 0)
        with self.assertRaises(auth.AuthError):
            manager.verify_credentials("ark", "bad")


if __name__ == "__main__":
    unittest.main()
