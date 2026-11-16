"""Mobile pairing, URL selection, permission and rate-limit tests (no network).

The HTTP tests use the real stdlib server on 127.0.0.1 but never contact an
external service.  The Host header is used deliberately to simulate a phone
arriving through an SSH local forward: ARK sees 127.0.0.1 on the socket, but the
tunnel computer's address in Host marks the request as non-local.
"""
from __future__ import annotations

import json
import os
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from arkstudio import mobile
from arkstudio.server import App, serve


class FakeClock:
    def __init__(self, value: float = 1000.0):
        self.value = value

    def __call__(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


PROJECT_A = {"id": "prj_a", "name": "Alpha"}
PROJECT_B = {"id": "prj_b", "name": "Beta"}


class PairingManagerTests(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.manager = mobile.PairingManager(
            clock=self.clock, pairing_ttl=600, session_ttl=1200,
            claim_limit=3, claim_window=60, create_limit=5, create_window=3600)

    def test_create_claim_and_one_time_use(self):
        pair = self.manager.create(PROJECT_A, permission="read_only", client="desk")
        self.assertTrue(pair["token"].startswith("arkp_"))
        self.assertEqual(pair["permission"], "read_only")
        result = self.manager.claim(pair["token"], PROJECT_A, client="phone")
        token = result["mobile_token"]
        self.assertTrue(token.startswith("arkm_"))
        self.assertTrue(result["read_only"])
        self.assertNotIn(pair["token"], json.dumps(self.manager.list(PROJECT_A)))
        session = self.manager.authenticate(token, PROJECT_A, method="GET")
        self.assertEqual(session.project_id, "prj_a")
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.assertEqual(caught.exception.code, "PAIRING_USED")
        self.assertEqual(caught.exception.status, 401)

    def test_claim_rejects_expired_and_wrong_project(self):
        pair = self.manager.create(PROJECT_A, client="desk")
        self.clock.advance(601)
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.assertEqual(caught.exception.code, "PAIRING_EXPIRED")

        other = self.manager.create(PROJECT_A, client="desk")
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(other["token"], PROJECT_B, client="phone")
        self.assertEqual(caught.exception.code, "MOBILE_WRONG_PROJECT")
        self.assertEqual(caught.exception.status, 403)
        # A rejected wrong-project claim does not consume the code.
        self.assertTrue(self.manager.claim(other["token"], PROJECT_A, client="phone")["mobile_token"])

    def test_revoke_invalidates_pairing_and_sessions(self):
        pair = self.manager.create(PROJECT_A, client="desk")
        claim = self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.manager.revoke(pair["id"], PROJECT_A)
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.authenticate(claim["mobile_token"], PROJECT_A)
        self.assertEqual(caught.exception.code, "MOBILE_TOKEN_INVALID")
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.assertIn(caught.exception.code, {"PAIRING_INVALID", "PAIRING_REVOKED"})
        self.assertEqual(self.manager.list(PROJECT_A)[0]["state"], "revoked")

    def test_read_only_blocks_writes_and_full_allows_them(self):
        read_pair = self.manager.create(PROJECT_A, permission="read_only", client="desk")
        read_token = self.manager.claim(read_pair["token"], PROJECT_A, client="phone")["mobile_token"]
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.authenticate(read_token, PROJECT_A, method="POST")
        self.assertEqual(caught.exception.code, "MOBILE_READ_ONLY")
        self.manager.authenticate(read_token, PROJECT_A, method="GET")

        full_pair = self.manager.create(PROJECT_A, permission="full", client="desk")
        full_token = self.manager.claim(full_pair["token"], PROJECT_A, client="phone")["mobile_token"]
        session = self.manager.authenticate(full_token, PROJECT_A, method="POST")
        self.assertEqual(session.permission, "full")

    def test_session_expiry(self):
        pair = self.manager.create(PROJECT_A, client="desk", session_ttl=1200)
        token = self.manager.claim(pair["token"], PROJECT_A, client="phone")["mobile_token"]
        self.clock.advance(1201)
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.authenticate(token, PROJECT_A)
        self.assertEqual(caught.exception.code, "MOBILE_TOKEN_EXPIRED")

    def test_default_pairing_is_full_access_and_permanent(self):
        pair = self.manager.create(PROJECT_A, client="desk")
        self.assertEqual(pair["permission"], "full")
        self.assertTrue(pair["session_permanent"])
        claim = self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.assertTrue(claim["permanent"])
        self.assertIsNone(claim["expires_at"])
        self.clock.advance(400 * 24 * 3600)
        session = self.manager.authenticate(claim["mobile_token"], PROJECT_A, method="POST")
        self.assertEqual(session.permission, "full")
        # Still a one-time *code*: the QR cannot be claimed twice.
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(pair["token"], PROJECT_A, client="phone2")
        self.assertEqual(caught.exception.code, "PAIRING_USED")

    def test_session_ttl_choices(self):
        self.assertEqual(mobile.PairingManager._session_ttl("permanent"), mobile.SESSION_PERMANENT)
        self.assertEqual(mobile.PairingManager._session_ttl(None), mobile.SESSION_PERMANENT)
        self.assertEqual(mobile.PairingManager._session_ttl(str(7 * 24 * 3600)), 7 * 24 * 3600)
        self.assertEqual(mobile.PairingManager._session_ttl(10), 300)
        with self.assertRaises(mobile.MobileError):
            mobile.PairingManager._session_ttl("soon")

    def test_permanent_sessions_survive_restart_and_revoke_persists(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp) / ".mobile-sessions.json"
            first = mobile.PairingManager(clock=self.clock, state_path=state)
            pair = first.create(PROJECT_A, client="desk")
            token = first.claim(pair["token"], PROJECT_A, client="phone")["mobile_token"]
            stored = state.read_text(encoding="utf-8")
            self.assertNotIn(token, stored)
            self.assertNotIn(pair["token"], stored)
            second = mobile.PairingManager(clock=self.clock, state_path=state)
            self.assertEqual(second.authenticate(token, PROJECT_A).project_id, "prj_a")
            second.revoke(pair["id"], PROJECT_A)
            third = mobile.PairingManager(clock=self.clock, state_path=state)
            self.assertIsNone(third.session_for_token(token))
            state.write_text("{broken", encoding="utf-8")
            self.assertIsNone(mobile.PairingManager(state_path=state).session_for_token(token))

    def test_claim_rate_limit_and_window_reset(self):
        pair = self.manager.create(PROJECT_A, client="desk")
        for _ in range(3):
            with self.assertRaises(mobile.MobileError) as caught:
                self.manager.claim("not-the-code", PROJECT_A, client="phone")
            self.assertEqual(caught.exception.code, "PAIRING_INVALID")
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.claim(pair["token"], PROJECT_A, client="phone")
        self.assertEqual(caught.exception.code, "RATE_LIMITED")
        self.assertEqual(caught.exception.status, 429)
        self.clock.advance(61)
        self.assertIn("mobile_token", self.manager.claim(pair["token"], PROJECT_A, client="phone"))

    def test_permission_validation_and_max_ttl(self):
        with self.assertRaises(mobile.MobileError) as caught:
            self.manager.create(PROJECT_A, permission="admin")
        self.assertEqual(caught.exception.code, "INVALID_PERMISSION")
        pair = self.manager.create(PROJECT_A, ttl_seconds=99999)
        self.assertEqual(pair["expires_in"], mobile.PAIRING_TTL_MAX)

    def test_public_url_and_pairing_url_validation(self):
        self.assertEqual(mobile.normalize_public_url("https://ark.example.net/ark"),
                         "https://ark.example.net/ark/")
        self.assertEqual(mobile.normalize_public_url("http://192.168.1.2:8891/"),
                         "http://192.168.1.2:8891/")
        for bad in ("ftp://ark.example", "https://user:pw@ark.example/",
                    "https://ark.example/?token=1", "https://ark.example/#x",
                    "https://ark.example", "javascript:alert(1)"):
            with self.subTest(bad=bad):
                if bad == "https://ark.example":
                    # Missing a trailing slash is normalised, not rejected.
                    self.assertEqual(mobile.normalize_public_url(bad), "https://ark.example/")
                    continue
                with self.assertRaises(mobile.MobileError):
                    mobile.normalize_public_url(bad)
        url = mobile.build_pairing_url("https://ark.example.net/ark/", "arkp_abc")
        self.assertEqual(url, "https://ark.example.net/ark/#pair=arkp_abc")

    def test_tunnel_host_and_port_validation(self):
        self.assertEqual(mobile.validate_tunnel_host("192.168.1.20"), "192.168.1.20")
        self.assertEqual(mobile.validate_tunnel_host("ark-laptop.local"), "ark-laptop.local")
        self.assertEqual(mobile.validate_tunnel_host("[::1]"), "[::1]")
        for bad in ("http://192.168.1.20", "192.168.1.20:8891", "a b", "a/b", ""):
            with self.subTest(bad=bad):
                with self.assertRaises(mobile.MobileError):
                    mobile.validate_tunnel_host(bad)
        with self.assertRaises(mobile.MobileError):
            mobile.validate_port(0)
        with self.assertRaises(mobile.MobileError):
            mobile.validate_port(70000)


class ConnectionInfoTests(unittest.TestCase):
    def _app(self, bind_host="127.0.0.1", port=8890, public_url=""):
        return SimpleNamespace(bind_host=bind_host, port=port, public_url=public_url)

    def test_loopback_state_has_exact_ssh_command(self):
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=["192.168.1.20"]):
            info = mobile.connection_info(self._app())
        self.assertEqual(info["mode"], "loopback")
        self.assertTrue(info["loopback_only"])
        self.assertEqual(info["advertised_urls"], [])
        command = info["tunnel"]["command"]
        self.assertTrue(command.startswith("ssh -N -L 0.0.0.0:8891:127.0.0.1:8890 "))
        self.assertIn("user@server", command)
        self.assertIn("phone-reachable", info["tunnel"]["warning"])
        self.assertEqual(info["tunnel"]["local_port"], 8891)

    def test_lan_mode_when_bound_non_loopback(self):
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=["192.168.1.20", "10.0.0.5"]):
            info = mobile.connection_info(self._app(bind_host="0.0.0.0", port=8890))
        self.assertEqual(info["mode"], "lan")
        self.assertFalse(info["loopback_only"])
        self.assertEqual(info["advertised_urls"],
                         ["http://192.168.1.20:8890/", "http://10.0.0.5:8890/"])

    def test_lan_mode_without_detected_address_is_not_called_loopback(self):
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=[]):
            info = mobile.connection_info(self._app(bind_host="0.0.0.0", port=8890))
        self.assertEqual(info["mode"], "lan")
        self.assertFalse(info["loopback_only"])
        self.assertEqual(info["advertised_urls"], [])
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=[]):
            base, source = mobile.resolve_pairing_base(self._app(bind_host="0.0.0.0", port=8890))
        self.assertEqual(base, "")
        self.assertEqual(source, "lan_unresolved")

    def test_public_url_wins_over_lan(self):
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=["192.168.1.20"]):
            info = mobile.connection_info(self._app(
                bind_host="0.0.0.0", port=8890,
                public_url="https://ark.example.net/ark/"))
        self.assertEqual(info["mode"], "public")
        self.assertEqual(info["advertised_urls"], ["https://ark.example.net/ark/"])
        self.assertTrue(info["https"])

    def test_pairing_base_selection_order(self):
        app = self._app()
        with mock.patch("arkstudio.mobile._private_ipv4", return_value=["192.168.1.20"]):
            base, source = mobile.resolve_pairing_base(app, override_host="192.168.1.50",
                                                       override_port=8891)
        self.assertEqual(base, "http://192.168.1.50:8891/")
        self.assertEqual(source, "tunnel_target")

        public = self._app(public_url="https://ark.example.net/ark/")
        base, source = mobile.resolve_pairing_base(public, override_host="192.168.1.50",
                                                   override_port=8891)
        self.assertEqual(base, "https://ark.example.net/ark/")
        self.assertEqual(source, "public_url")

        with mock.patch("arkstudio.mobile._private_ipv4", return_value=["192.168.1.20"]):
            base, source = mobile.resolve_pairing_base(self._app(bind_host="0.0.0.0", port=8890))
        self.assertEqual(base, "http://192.168.1.20:8890/")
        self.assertEqual(source, "lan")

        base, source = mobile.resolve_pairing_base(self._app())
        self.assertEqual(base, "")
        self.assertEqual(source, "loopback")


class MobileApiTests(unittest.TestCase):
    def setUp(self):
        self._old_chat = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        self.tmp = tempfile.TemporaryDirectory()
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=True,
                       task_sessions_root=Path(self.tmp.name) / "agent-sessions")
        self.httpd = serve(self.app, port=0, block=False)
        self.base = f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def tearDown(self):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.app.workspace.shutdown()
        self.app.project.close()
        self.tmp.cleanup()
        if self._old_chat is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = self._old_chat

    def request(self, path, *, method="GET", body=None, headers=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                raw = response.read()
                ctype = response.headers.get("Content-Type", "")
                return response.status, (json.loads(raw) if "json" in ctype
                                         else raw.decode("utf-8", "replace"))
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def _pair(self, **body):
        status, data = self.request("/api/mobile/pair", method="POST", body=body)
        self.assertEqual(status, 200, data)
        return data

    def _claim(self, code, host="192.168.1.20:8891"):
        return self.request("/api/mobile/pair/claim", method="POST",
                            body={"token": code}, headers={"Host": host})

    def test_loopback_pairing_state_and_qr(self):
        status, info = self.request("/api/mobile/connection")
        self.assertEqual(status, 200)
        self.assertEqual(info["mode"], "loopback")
        self.assertTrue(info["loopback_only"])
        self.assertIn("ssh -N -L 0.0.0.0:", info["tunnel"]["command"])

        status, error = self.request("/api/mobile/pair", method="POST",
                                     body={"permission": "read_only"})
        self.assertEqual(status, 409)
        self.assertEqual(error["error"]["code"], "MOBILE_LOOPBACK_ONLY")

        pair = self._pair(permission="read_only", tunnel_host="192.168.1.20",
                          tunnel_port=8891, expires_in=600)
        self.assertTrue(pair["code"].startswith("arkp_"))
        self.assertEqual(pair["pairing_url"],
                         f"http://192.168.1.20:8891/#pair={pair['code']}")
        self.assertTrue(pair["qr_svg"].startswith("<svg"))
        self.assertIn("<title>", pair["qr_svg"])
        self.assertNotIn(pair["code"], pair["qr_svg"])  # URL is encoded as QR, not text

        # The generated Host is now accepted for the tunnel-facing app shell.
        status, html = self.request("/", headers={"Host": "192.168.1.20:8891"})
        self.assertEqual(status, 200)
        self.assertIn("manifest.webmanifest", html)

        status, pairs = self.request("/api/mobile/pairs")
        self.assertEqual(status, 200)
        self.assertEqual(len(pairs["pairs"]), 1)
        self.assertNotIn("token", json.dumps(pairs))

        # Activity never contains the raw pairing or mobile tokens.
        status, activity = self.request("/api/activity")
        self.assertEqual(status, 200)
        text = json.dumps(activity)
        self.assertNotIn(pair["code"], text)
        self.assertIn(pair["pair"]["id"], text)

    def test_claim_replay_read_only_and_logout(self):
        pair = self._pair(permission="read_only", tunnel_host="192.168.1.20",
                          tunnel_port=8891)
        status, claim = self._claim(pair["code"])
        self.assertEqual(status, 200, claim)
        token = claim["mobile_token"]
        self.assertTrue(token.startswith("arkm_"))
        self.assertTrue(claim["read_only"])
        self.assertNotEqual(token, pair["code"])

        status, replay = self._claim(pair["code"])
        self.assertEqual(status, 401)
        self.assertEqual(replay["error"]["code"], "PAIRING_USED")

        headers = {"Host": "192.168.1.20:8891", "X-ARK-Mobile-Token": token}
        status, project = self.request("/api/project", headers=headers)
        self.assertEqual(status, 200, project)
        status, snapshot = self.request('/api/graph/snapshot', headers=headers)
        self.assertEqual(status, 200, snapshot)
        self.assertGreater(snapshot['total'], 0)
        status, denied = self.request("/api/units", method="POST",
                                      body={"title": "phone write", "type": "note"},
                                      headers=headers)
        self.assertEqual(status, 403)
        self.assertEqual(denied["error"]["code"], "MOBILE_READ_ONLY")
        status, secret = self.request("/api/agent/token", headers=headers)
        self.assertEqual(status, 403)
        self.assertEqual(secret["error"]["code"], "MOBILE_SECRET_BLOCKED")

        status, logout = self.request("/api/mobile/session", method="DELETE", headers=headers)
        self.assertEqual(status, 200)
        self.assertTrue(logout["logged_out"])
        status, expired = self.request("/api/project", headers=headers)
        self.assertEqual(status, 401)
        self.assertEqual(expired["error"]["code"], "MOBILE_TOKEN_INVALID")

    def test_full_access_write_then_revoke(self):
        pair = self._pair(permission="full", tunnel_host="192.168.1.20",
                          tunnel_port=8891)
        status, claim = self._claim(pair["code"])
        self.assertEqual(status, 200, claim)
        token = claim["mobile_token"]
        headers = {"Host": "192.168.1.20:8891", "X-ARK-Mobile-Token": token}
        status, created = self.request("/api/units", method="POST",
                                       body={"title": "phone write", "type": "note"},
                                       headers=headers)
        self.assertEqual(status, 200, created)
        status, revoked = self.request(
            f"/api/mobile/pairs/{pair['pair']['id']}/revoke", method="POST", body={})
        self.assertEqual(status, 200, revoked)
        self.assertEqual(revoked["revoked"]["state"], "revoked")
        status, denied = self.request("/api/project", headers=headers)
        self.assertEqual(status, 401)
        self.assertEqual(denied["error"]["code"], "MOBILE_TOKEN_INVALID")

    def test_tunnel_host_without_token_is_rejected(self):
        pair = self._pair(permission="read_only", tunnel_host="192.168.1.20",
                          tunnel_port=8891)
        tunnel_host = {"Host": "192.168.1.20:8891"}
        status, data = self.request("/api/project", headers=tunnel_host)
        self.assertEqual(status, 401)
        self.assertEqual(data["error"]["code"], "MOBILE_TOKEN_REQUIRED")
        status, session = self.request("/api/mobile/session", headers=tunnel_host)
        self.assertEqual(status, 200)
        self.assertTrue(session["required"])
        self.assertFalse(session["authenticated"])
        status, project = self.request("/api/project")
        self.assertEqual(status, 200)  # the loopback desktop stays trusted
        self.assertEqual(pair["pair"]["permission"], "read_only")

    def test_paired_device_cannot_manage_pairings(self):
        pair = self._pair(permission="read_only", tunnel_host="192.168.1.20",
                          tunnel_port=8891)
        status, claim = self._claim(pair["code"])
        token = claim["mobile_token"]
        headers = {"Host": "192.168.1.20:8891", "X-ARK-Mobile-Token": token}
        for path, method, body in (
            ("/api/mobile/connection", "GET", None),
            ("/api/mobile/pairs", "GET", None),
            ("/api/mobile/pair", "POST", {"permission": "full"}),
            (f"/api/mobile/pairs/{pair['pair']['id']}/revoke", "POST", {}),
        ):
            with self.subTest(path=path):
                status, data = self.request(path, method=method, body=body, headers=headers)
                self.assertEqual(status, 403, data)
                self.assertEqual(data["error"]["code"], "MOBILE_SCOPE")

    def test_unregistered_host_is_rejected(self):
        status, data = self.request("/api/project", headers={"Host": "evil.example:8891"})
        self.assertEqual(status, 403)
        self.assertEqual(data["error"]["code"], "HOST_REJECTED")

    def test_claim_rate_limit_over_http(self):
        self.app.mobile.claim_limit = 2
        pair = self._pair(permission="read_only", tunnel_host="192.168.1.20",
                          tunnel_port=8891)
        for _ in range(2):
            status, data = self._claim("wrong-code")
            self.assertEqual(status, 401)
        status, data = self._claim(pair["code"])
        self.assertEqual(status, 429)
        self.assertEqual(data["error"]["code"], "RATE_LIMITED")


class PublicUrlAppTests(unittest.TestCase):
    def test_public_url_registers_host_and_builds_qr(self):
        with tempfile.TemporaryDirectory() as tmp:
            app = App(Path(tmp) / "projects", seed_demo=True,
                      task_sessions_root=Path(tmp) / "agent-sessions",
                      public_url="https://ark.example.net/ark/")
            httpd = serve(app, port=0, block=False)
            base = f"http://127.0.0.1:{httpd.server_address[1]}"
            try:
                self.assertTrue(app.host_allowed("ark.example.net"))
                status, info = self._request(base, "/api/mobile/connection")
                self.assertEqual(status, 200)
                self.assertEqual(info["mode"], "public")
                status, pair = self._request(base, "/api/mobile/pair", method="POST",
                                             body={"permission": "read_only"})
                self.assertEqual(status, 200, pair)
                self.assertEqual(pair["base_source"], "public_url")
                self.assertTrue(pair["pairing_url"].startswith(
                    "https://ark.example.net/ark/#pair="))
            finally:
                httpd.shutdown()
                httpd.server_close()
                app.workspace.shutdown()
                app.project.close()

    @staticmethod
    def _request(base, path, *, method="GET", body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            base + path, data=data, method=method,
            headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read() or b"{}")


if __name__ == "__main__":
    unittest.main()
