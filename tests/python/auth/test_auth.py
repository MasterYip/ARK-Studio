"""Public-mode authentication tests (stdlib only, never an external service).

The HTTP tests run the real stdlib server on 127.0.0.1.  A public deployment is
simulated with an explicit public URL and the public-mobile tests use the same
one-time pairing API; no NAT, DNS or public-IP provider is contacted.
"""
from __future__ import annotations

import contextlib
import io
import json
import os
import re
import tempfile
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from arkstudio import auth, mobile
from arkstudio.__main__ import _build_auth, main
from arkstudio.server import App, Handler, serve


class FakeClock:
    def __init__(self, value: float = 1_000_000.0):
        self.value = value

    def __call__(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


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
    data = body.encode("utf-8") if isinstance(body, str) else body
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers=headers or {})
    opener = urllib.request.build_opener(NoRedirect())
    try:
        with opener.open(req, timeout=10) as response:
            return Response(response.status, dict(response.headers),
                            response.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as exc:
        return Response(exc.code, dict(exc.headers or {}),
                        exc.read().decode("utf-8", "replace"))


def cookie_from(response: Response, name: str = "ark_session") -> str:
    for value in (response.headers.get("set-cookie") or "").split(","):
        part = value.strip().split(";", 1)[0]
        if part.startswith(name + "="):
            return part
    return ""


def csrf_from(page: str) -> str:
    match = re.search(r'name="csrf"\s+value="([^"]+)"', page)
    return match.group(1) if match else ""


class PasswordHashTests(unittest.TestCase):
    def test_salted_hash_round_trip_and_validation(self):
        first = auth.hash_password("correct horse battery", iterations=1000)
        second = auth.hash_password("correct horse battery", iterations=1000)
        self.assertNotEqual(first, second)  # random salt
        self.assertTrue(first.startswith("pbkdf2_sha256$1000$"))
        self.assertTrue(auth.verify_password(first, "correct horse battery"))
        self.assertFalse(auth.verify_password(first, "wrong"))
        self.assertFalse(auth.verify_password("not-a-hash", "correct horse battery"))
        self.assertTrue(auth.is_password_hash(first))
        self.assertFalse(auth.is_password_hash("pbkdf2_sha256$0$AA$BB"))

    def test_manager_holds_only_a_hash(self):
        manager = auth.AuthManager("ark", password="top-secret", iterations=1000)
        self.assertFalse(hasattr(manager, "password"))
        self.assertNotIn("top-secret", repr(manager.__dict__))
        token, session = manager.login("ark", "top-secret", client_ip="127.0.0.1")
        self.assertEqual(manager.authenticate(token).username, "ark")
        self.assertEqual(session.username, "ark")

    def test_password_file_and_env_build(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "password"
            path.write_text("from-file\n", encoding="utf-8")
            manager = auth.build_manager(username="ark", password_file=str(path),
                                         iterations=1000)
            self.assertTrue(auth.verify_password(manager.password_hash, "from-file"))
        args = SimpleNamespace(auth_user="", auth_password="", auth_password_file="",
                               auth_password_hash="", auth_session_ttl=0)
        env = {"ARK_AUTH_USER": "env-user", "ARK_AUTH_PASSWORD": "env-password"}
        manager = _build_auth(args, environ=env)
        self.assertEqual(manager.username, "env-user")
        self.assertTrue(auth.verify_password(manager.password_hash, "env-password"))
        self.assertIsNone(_build_auth(args, environ={}))
        with self.assertRaises(auth.AuthConfigError):
            _build_auth(SimpleNamespace(auth_user="ark", auth_password="a",
                                        auth_password_file="", auth_password_hash="b",
                                        auth_session_ttl=0), environ={})

    def test_safe_next_paths(self):
        self.assertEqual(auth.safe_next_path("/#pair=abc"), "/#pair=abc")
        self.assertEqual(auth.safe_next_path("/settings?x=1#/tasks"),
                         "/settings?x=1#/tasks")
        for bad in ("https://evil.example/", "//evil.example/", "/\\evil", "/../etc",
                    "\r\nLocation: https://evil.example"):
            with self.subTest(bad=bad):
                self.assertEqual(auth.safe_next_path(bad, default="/"), "/")


class AuthManagerTests(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.manager = auth.AuthManager(
            "ark", password="secret-pass", iterations=1000,
            session_ttl=900, login_limit=3, login_window=60, clock=self.clock)

    def test_session_expiry_and_revoke(self):
        token, _session = self.manager.login("ark", "secret-pass", client_ip="phone")
        self.assertIsNotNone(self.manager.authenticate(token))
        self.clock.advance(901)
        self.assertIsNone(self.manager.authenticate(token))
        token2, _ = self.manager.login("ark", "secret-pass", client_ip="phone")
        self.assertTrue(self.manager.revoke(token2))
        self.assertIsNone(self.manager.authenticate(token2))
        self.assertFalse(self.manager.revoke(token2))

    def test_tampered_cookie_is_rejected(self):
        token, _ = self.manager.login("ark", "secret-pass", client_ip="phone")
        payload, signature = token.rsplit(".", 1)
        tampered = payload[:-1] + ("A" if payload[-1] != "A" else "B") + "." + signature
        self.assertIsNone(self.manager.authenticate(tampered))
        self.assertIsNone(self.manager.authenticate(token + "x"))

    def test_wrong_password_rate_limit_and_window(self):
        for _ in range(3):
            with self.assertRaises(auth.AuthError) as caught:
                self.manager.login("ark", "wrong", client_ip="1.2.3.4")
            self.assertEqual(caught.exception.status, 401)
            self.assertEqual(caught.exception.code, "AUTH_INVALID")
        with self.assertRaises(auth.AuthError) as caught:
            self.manager.login("ark", "secret-pass", client_ip="1.2.3.4")
        self.assertEqual(caught.exception.status, 429)
        self.assertEqual(caught.exception.code, "AUTH_RATE_LIMITED")
        self.assertGreaterEqual(caught.exception.retry_after, 1)
        self.clock.advance(61)
        token, _ = self.manager.login("ark", "secret-pass", client_ip="1.2.3.4")
        self.assertTrue(token)

    def test_unknown_username_does_not_bypass_rate_limit(self):
        with self.assertRaises(auth.AuthError):
            self.manager.login("nobody", "secret-pass", client_ip="5.6.7.8")
        with self.assertRaises(auth.AuthError):
            self.manager.login("ark", "wrong", client_ip="5.6.7.8")

    def test_csrf_tokens(self):
        token = self.manager.login_csrf()
        self.assertTrue(self.manager.verify_login_csrf(token))
        self.assertFalse(self.manager.verify_login_csrf(token + "x"))
        self.assertFalse(self.manager.verify_login_csrf("garbage"))
        self.clock.advance(auth.LOGIN_CSRF_TTL + 1)
        self.assertFalse(self.manager.verify_login_csrf(token))

    def test_cookie_flags(self):
        token, _ = self.manager.login("ark", "secret-pass")
        header = self.manager.cookie_header(token)
        self.assertIn("HttpOnly", header)
        self.assertIn("SameSite=Lax", header)
        self.assertIn("Path=/", header)
        self.assertNotIn("Secure", header)
        secure = self.manager.cookie_header(token, secure=True)
        self.assertIn("Secure", secure)
        cleared = self.manager.clear_cookie_header(secure=True)
        self.assertIn("Max-Age=0", cleared)
        self.assertIn("Secure", cleared)

    def test_cookie_path_is_scoped_below_proxy_prefix(self):
        self.assertEqual(auth.cookie_path_from_url("https://ark.example.net/ark/"),
                         "/ark/")
        self.assertEqual(auth.cookie_path_from_url(""), "/")
        manager = auth.AuthManager("ark", password="x", iterations=1000,
                                   cookie_path="/ark/")
        token, _ = manager.login("ark", "x")
        self.assertIn("Path=/ark/", manager.cookie_header(token))


class CaseSensitivityTests(unittest.TestCase):
    def setUp(self):
        self.m = auth.AuthManager("Ark", password="Secret-Pass1", iterations=1000)

    def test_username_must_match_case(self):
        self.assertEqual("Ark", self.m.verify_credentials("Ark", "Secret-Pass1"))
        for wrong in ("ark", "ARK", "aRk"):
            with self.assertRaises(auth.AuthError):
                self.m.verify_credentials(wrong, "Secret-Pass1")

    def test_password_must_match_case(self):
        for wrong in ("secret-pass1", "SECRET-PASS1", "Secret-pass1"):
            with self.assertRaises(auth.AuthError):
                self.m.verify_credentials("Ark", wrong)

    def test_accounts_differing_only_by_case_are_refused(self):
        with self.assertRaises(auth.AuthConfigError):
            self.m.add_account("ark", auth.hash_password("x", iterations=1000))

    def test_org_accounts_are_case_sensitive(self):
        from arkstudio import org as org_mod
        with tempfile.TemporaryDirectory() as tmp:
            accounts = org_mod.OrgManager(Path(tmp) / "org.sqlite3")
            accounts.store.insert_user("Alice", auth.hash_password("Pw-12345", iterations=1000))
            self.m.set_accounts(accounts)
            self.assertEqual("Alice", self.m.verify_credentials("Alice", "Pw-12345"))
            with self.assertRaises(auth.AuthError):
                self.m.verify_credentials("alice", "Pw-12345")
            token, session = self.m.issue_session("Alice")
            self.assertIsNotNone(self.m.authenticate(token))
            self.assertEqual(0, self.m.revoke_user("alice"))
            self.assertEqual(1, self.m.revoke_user("Alice"))


class RememberMeTests(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.tmp = tempfile.TemporaryDirectory()
        self.folder = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def manager(self):
        m = auth.AuthManager("ark", password="pw-123456", iterations=1000, clock=self.clock)
        m.enable_persistence(self.folder)
        return m

    def test_only_remembered_sessions_survive_a_restart(self):
        first = self.manager()
        short, plain = first.login("ark", "pw-123456")
        long, kept = first.login("ark", "pw-123456", remember=True)
        self.assertEqual(first.session_ttl, plain.expires_at - plain.created_at)
        self.assertEqual(auth.REMEMBER_TTL, kept.expires_at - kept.created_at)
        stored = (self.folder / ".auth-sessions.json").read_text(encoding="utf-8")
        self.assertNotIn(long.split(".")[1], stored)   # sid hashes only
        again = self.manager()
        self.assertIsNotNone(again.authenticate(long))
        self.assertIsNone(again.authenticate(short))

    def test_remembered_session_expires_after_7_days(self):
        token, _ = self.manager().login("ark", "pw-123456", remember=True)
        self.clock.advance(auth.REMEMBER_TTL - 60)
        self.assertIsNotNone(self.manager().authenticate(token))
        self.clock.advance(120)
        self.assertIsNone(self.manager().authenticate(token))

    def test_sign_out_and_user_revocation_persist(self):
        first = self.manager()
        token, _ = first.login("ark", "pw-123456", remember=True)
        first.revoke(token)
        self.assertIsNone(self.manager().authenticate(token))
        other, _ = self.manager().login("ark", "pw-123456", remember=True)
        third = self.manager()
        third.revoke_user("ark")
        self.assertIsNone(self.manager().authenticate(other))

    def test_signing_secret_is_stable_and_damaged_store_is_ignored(self):
        token, _ = self.manager().login("ark", "pw-123456", remember=True)
        secret = (self.folder / ".auth-secret").read_bytes()
        self.manager()
        self.assertEqual(secret, (self.folder / ".auth-secret").read_bytes())
        (self.folder / ".auth-sessions.json").write_text("{broken", encoding="utf-8")
        self.assertIsNone(self.manager().authenticate(token))


class AuthHttpMixin:
    SESSION_TTL = 900

    def setUp(self):
        self._old_chat = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        self.clock = FakeClock()
        self.manager = auth.AuthManager(
            "ark", password="secret-pass", iterations=1000, clock=self.clock,
            session_ttl=self.SESSION_TTL, login_limit=4, login_window=60)
        self.tmp = tempfile.TemporaryDirectory()
        self.app = App(Path(self.tmp.name) / "projects", seed_demo=True,
                       task_sessions_root=Path(self.tmp.name) / "sessions",
                       auth=self.manager)
        self.httpd = serve(self.app, port=0, block=False)
        self.port = self.httpd.server_address[1]
        self.base = f"http://127.0.0.1:{self.port}"
        self.host = f"127.0.0.1:{self.port}"

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

    # ------------------------------------------------------------- helpers
    def login_page(self, next_path="/", *, host=None):
        target = "/login?next=" + urllib.parse.quote(next_path, safe="")
        return request(self.base, target, headers={"Host": host or self.host})

    def login(self, *, password="secret-pass", username="ark", csrf=None,
              next_path="/", host=None, origin=None, extra=None, form=None):
        if csrf is None:
            csrf = csrf_from(self.login_page(next_path, host=host).text)
        fields = {"username": username, "password": password, "csrf": csrf, "next": next_path}
        fields.update(form or {})
        body = urllib.parse.urlencode(fields)
        headers = {
            "Content-Type": "application/x-www-form-urlencoded",
            "Host": host or self.host,
            "Origin": (origin if origin is not None
                       else f"http://{host or self.host}"),
        }
        headers.update(extra or {})
        return request(self.base, "/login", method="POST", body=body, headers=headers)

    # ------------------------------------------------------- remember me
    def max_age(self, response):
        cookie = response.headers.get("set-cookie") or ""
        match = re.search(r"Max-Age=(\d+)", cookie)
        return int(match.group(1)) if match else None

    def test_login_page_offers_remember_checkbox(self):
        page = self.login_page().text
        self.assertIn('name="remember"', page)
        self.assertIn("Keep me signed in for 7 days", page)

    def test_remember_me_sets_a_7_day_cookie(self):
        plain = self.login()
        self.assertEqual(303, plain.status)
        self.assertEqual(self.SESSION_TTL, self.max_age(plain))
        remembered = self.login(form={"remember": "1"})
        self.assertEqual(303, remembered.status)
        self.assertEqual(auth.REMEMBER_TTL, self.max_age(remembered))

    # --------------------------------------------------------------- gate
    def test_gate_redirects_static_and_rejects_api(self):
        page = request(self.base, "/", headers={"Host": self.host})
        self.assertEqual(page.status, 302)
        self.assertTrue(page.headers["location"].startswith("/login?next="))
        api = request(self.base, "/api/project", headers={"Host": self.host})
        self.assertEqual(api.status, 401)
        self.assertEqual(api.json()["error"]["code"], "AUTH_REQUIRED")
        css = request(self.base, "/css/app.css", headers={"Host": self.host})
        self.assertEqual(css.status, 302)
        login_css = request(self.base, "/css/login.css", headers={"Host": self.host})
        self.assertEqual(login_css.status, 200)
        login_js = request(self.base, "/js/login.js", headers={"Host": self.host})
        self.assertEqual(login_js.status, 200)

    def test_login_page_matches_ark_and_sets_security_headers(self):
        page = self.login_page()
        self.assertEqual(page.status, 200)
        self.assertIn("ARK", page.text)
        self.assertIn("login.css", page.text)
        self.assertIn("name=\"csrf\"", page.text)
        self.assertIn("Sign in", page.text)
        csp = page.headers.get("content-security-policy", "")
        self.assertIn("form-action 'self'", csp)
        self.assertIn("frame-ancestors 'none'", csp)
        self.assertIn("no-store", page.headers.get("cache-control", ""))

    def test_health_is_public_but_does_not_leak_root(self):
        health = request(self.base, "/api/health", headers={"Host": self.host})
        self.assertEqual(health.status, 200)
        self.assertTrue(health.json()["auth_required"])
        self.assertNotIn("root", health.json())

    def test_wrong_password_then_rate_limit(self):
        csrf = csrf_from(self.login_page().text)
        first = self.login(password="wrong", csrf=csrf)
        self.assertEqual(first.status, 401)
        self.assertIn("Invalid username or password", first.text)
        for _ in range(3):
            self.login(password="wrong")
        limited = self.login(password="secret-pass")
        self.assertEqual(limited.status, 429)
        self.assertIn("retry-after", limited.headers)
        self.assertIn("Too many sign-in attempts", limited.text)

    def test_csrf_and_cross_origin_login_are_rejected(self):
        no_csrf = self.login(csrf="not-a-token")
        self.assertEqual(no_csrf.status, 403)
        cross = self.login(origin="https://evil.example")
        self.assertEqual(cross.status, 403)
        fetch_cross = self.login(extra={"Sec-Fetch-Site": "cross-site"})
        self.assertEqual(fetch_cross.status, 403)

    def test_login_allows_same_host_through_a_different_tunnel_port(self):
        # A local SSH/tunnel proxy may preserve the browser Origin port while
        # rewriting Host to the upstream ARK port.  The host must still match.
        same_host = self.login(origin=f"http://127.0.0.1:{self.port + 1}")
        self.assertEqual(same_host.status, 303)
        other_host = self.login(origin=f"http://127.0.0.2:{self.port}")
        self.assertEqual(other_host.status, 403)

    def test_null_origin_form_is_accepted_only_for_same_origin_fetch_metadata(self):
        # Chromium sometimes serialises a same-origin form POST as
        # ``Origin: null``.  The CSRF token and same-origin Sec-Fetch-Site are
        # still required.
        same = self.login(extra={"Origin": "null", "Sec-Fetch-Site": "same-origin"})
        self.assertEqual(same.status, 303)
        cross = self.login(extra={"Origin": "null", "Sec-Fetch-Site": "cross-site"})
        self.assertEqual(cross.status, 403)

    def test_signed_in_computer_does_not_need_mobile_pairing(self):
        response = self.login()
        self.assertEqual(response.status, 303)
        cookie = response.headers["set-cookie"].split(";", 1)[0]
        status = request(self.base, "/api/mobile/session",
                         headers={"Host": self.host, "Cookie": cookie})
        self.assertEqual(status.status, 200)
        self.assertFalse(status.json()["required"])
        self.assertEqual(status.json()["auth_user"], "ark")

    def test_successful_login_sets_cookie_and_opens_app(self):
        response = self.login(next_path="/#pair=arkp_example")
        self.assertEqual(response.status, 303)
        self.assertEqual(response.headers["location"], "/#pair=arkp_example")
        set_cookie = response.headers["set-cookie"]
        self.assertIn("HttpOnly", set_cookie)
        self.assertIn("SameSite=Lax", set_cookie)
        self.assertIn("Path=/", set_cookie)
        if self.app.public_url.startswith("https://"):
            self.assertIn("Secure", set_cookie)
        else:
            self.assertNotIn("Secure", set_cookie)  # this test server is plain HTTP
        cookie = cookie_from(response)
        self.assertTrue(cookie)
        project = request(self.base, "/api/project", headers={
            "Host": self.host, "Cookie": cookie})
        self.assertEqual(project.status, 200)
        app_html = request(self.base, "/", headers={"Host": self.host, "Cookie": cookie})
        self.assertEqual(app_html.status, 200)
        self.assertIn("manifest.webmanifest", app_html.text)
        session = request(self.base, "/api/auth/session", headers={
            "Host": self.host, "Cookie": cookie})
        self.assertEqual(session.status, 200)
        self.assertTrue(session.json()["authenticated"])
        self.assertEqual(session.json()["user"], "ark")

    def test_tampered_cookie_and_logout(self):
        cookie = cookie_from(self.login())
        tampered = cookie[:-2] + ("AA" if not cookie.endswith("AA") else "BB")
        denied = request(self.base, "/api/project", headers={
            "Host": self.host, "Cookie": tampered})
        self.assertEqual(denied.status, 401)
        logout = request(self.base, "/api/auth/logout", method="POST", body=b"",
                         headers={"Host": self.host, "Cookie": cookie,
                                  "Origin": self.base})
        self.assertEqual(logout.status, 200)
        self.assertTrue(logout.json()["logged_out"])
        self.assertIn("Max-Age=0", logout.headers["set-cookie"])
        after = request(self.base, "/api/project", headers={
            "Host": self.host, "Cookie": cookie})
        self.assertEqual(after.status, 401)

    def test_session_expiry_invalidates_cookie(self):
        cookie = cookie_from(self.login())
        self.clock.advance(self.SESSION_TTL + 1)
        expired = request(self.base, "/api/project", headers={
            "Host": self.host, "Cookie": cookie})
        self.assertEqual(expired.status, 401)

    def test_logout_form_redirects_to_login(self):
        cookie = cookie_from(self.login())
        logout = request(self.base, "/logout", method="POST", body=b"",
                         headers={"Host": self.host, "Cookie": cookie,
                                  "Origin": self.base})
        self.assertEqual(logout.status, 303)
        self.assertTrue(logout.headers["location"].endswith("/login"))
        self.assertIn("Max-Age=0", logout.headers["set-cookie"])

    def test_login_next_is_sanitised(self):
        csrf = csrf_from(self.login_page().text)
        response = self.login(csrf=csrf, next_path="https://evil.example/")
        self.assertEqual(response.status, 303)
        self.assertEqual(response.headers["location"], "/")

    def test_login_next_never_carries_a_mobile_token(self):
        csrf = csrf_from(self.login_page().text)
        response = self.login(csrf=csrf,
                              next_path="/?mobile_token=arkm_do_not_leak#/tasks")
        self.assertEqual(response.status, 303)
        self.assertNotIn("mobile_token", response.headers["location"])
        self.assertNotIn("arkm_", response.headers["location"])
        self.assertEqual(response.headers["location"], "/#/tasks")

    def test_public_url_strict_host_allow_list_blocks_dns_rebinding(self):
        self.app.public_url = "https://ark.example.net/"
        self.app.allow_advertised_url(self.app.public_url)
        self.app.bind_host = "0.0.0.0"
        self.assertTrue(self.app.host_allowed("ark.example.net"))
        self.assertTrue(self.app.host_allowed(self.host))
        self.assertFalse(self.app.host_allowed("evil.example:443"))
        denied = request(self.base, "/api/project", headers={"Host": "evil.example:443"})
        self.assertEqual(denied.status, 403)
        self.assertEqual(denied.json()["error"]["code"], "HOST_REJECTED")


class AuthHttpTests(AuthHttpMixin, unittest.TestCase):
    pass


class PublicBindTests(unittest.TestCase):
    def test_serve_refuses_non_loopback_without_auth(self):
        with tempfile.TemporaryDirectory() as tmp:
            app = App(Path(tmp) / "projects", seed_demo=True,
                      task_sessions_root=Path(tmp) / "sessions")
            try:
                with self.assertRaises(ValueError) as caught:
                    serve(app, host="0.0.0.0", port=0)
                self.assertIn("requires account authentication", str(caught.exception))
            finally:
                app.workspace.shutdown()
                app.project.close()

    def test_cli_expose_refusal_is_clear(self):
        with tempfile.TemporaryDirectory() as tmp:
            stderr = io.StringIO()
            with mock.patch.dict(os.environ, {}, clear=False):
                for key in ("ARK_AUTH_USER", "ARK_AUTH_PASSWORD",
                            "ARK_AUTH_PASSWORD_FILE", "ARK_AUTH_PASSWORD_HASH",
                            "ARK_PUBLIC"):
                    os.environ.pop(key, None)
                with contextlib.redirect_stderr(stderr):
                    # Nest the root: ARK reads <root>/../org.sqlite3, which must stay
                    # inside this test's own temporary directory.
                    code = main(["serve", "--expose", "--root", str(Path(tmp) / "projects"),
                                 "--no-demo"])
            self.assertEqual(code, 2)
            self.assertIn("refusing to bind", stderr.getvalue())
            self.assertIn("authentication", stderr.getvalue())


class AutoPairingTests(AuthHttpMixin, unittest.TestCase):
    """Public URL + account mode: QR creation needs no manual host/port."""

    def setUp(self):
        super().setUp()
        self.app.public_url = "https://ark.example.net/"
        self.app.allow_advertised_url(self.app.public_url)
        self.app.configure_auth_transport()
        self.public_host = "ark.example.net"

    def _login_public(self):
        csrf = csrf_from(self.login_page(host=self.public_host).text)
        return self.login(csrf=csrf, host=self.public_host, next_path="/")

    def test_pair_api_auto_selects_public_url_without_manual_fields(self):
        cookie = cookie_from(self._login_public())
        response = request(self.base, "/api/mobile/pair", method="POST", body=b"{}",
                           headers={"Host": self.host, "Cookie": cookie,
                                    "Content-Type": "application/json"})
        self.assertEqual(response.status, 200, response.text)
        data = response.json()
        self.assertEqual(data["base_source"], "public_url")
        self.assertTrue(data["pairing_url"].startswith("https://ark.example.net/#pair="))
        self.assertTrue(data["qr_svg"].startswith("<svg"))
        self.assertTrue(data["requires_login"])
        self.assertTrue(data["auto"])

    def test_phone_scans_qr_without_password(self):
        cookie = cookie_from(self._login_public())
        pair = request(self.base, "/api/mobile/pair", method="POST", body=b"{}",
                       headers={"Host": self.host, "Cookie": cookie,
                                "Content-Type": "application/json"}).json()
        # The QR is a one-time, desktop-issued sign-in proof.  Claiming it must
        # create the account session before the phone asks for a password.
        claim = request(self.base, "/api/mobile/pair/claim", method="POST",
                        body=json.dumps({"token": pair["code"]}),
                        headers={"Host": self.public_host,
                                 "Content-Type": "application/json"})
        self.assertEqual(claim.status, 200, claim.text)
        set_cookie = claim.headers.get("set-cookie", "")
        self.assertIn("ark_mobile=", set_cookie)
        self.assertIn("HttpOnly", set_cookie)
        self.assertNotIn("ark_session=", set_cookie)
        mobile_cookie = cookie_from(claim, "ark_mobile")
        # The paired-device cookie alone can load the shell and call
        # project-scoped APIs, but it must not become a desktop account session.
        shell = request(self.base, "/", headers={"Host": self.public_host,
                                                "Cookie": mobile_cookie})
        self.assertEqual(shell.status, 200)
        token = claim.json()["mobile_token"]
        project = request(self.base, "/api/project", headers={
            "Host": self.public_host, "Cookie": mobile_cookie})
        self.assertEqual(project.status, 200)
        account = request(self.base, "/api/auth/session", headers={
            "Host": self.public_host, "Cookie": mobile_cookie})
        self.assertFalse(account.json()["authenticated"])
        self.assertTrue(token.startswith("arkm_"))

    def test_signed_in_public_window_can_create_pairing(self):
        cookie = cookie_from(self._login_public())
        response = request(self.base, "/api/mobile/pair", method="POST", body=b"{}",
                           headers={"Host": self.public_host, "Cookie": cookie,
                                    "Content-Type": "application/json"})
        self.assertEqual(response.status, 200, response.text)
        data = response.json()
        self.assertTrue(data["pairing_url"].startswith("https://"))
        self.assertTrue(data["qr_svg"].startswith("<svg"))

    def test_login_redirects_desktop_browser_to_login(self):
        page = request(self.base, "/", headers={"Host": self.public_host})
        self.assertEqual(page.status, 302)
        self.assertIn("/login?next=", page.headers["location"])
        page = request(self.base, "/?settings=mobile#/settings",
                       headers={"Host": self.public_host})
        self.assertEqual(page.status, 302)

    def test_pair_api_uses_request_host_when_exposed(self):
        self.app.public_url = ""
        self.app.expose = True
        self.app.bind_host = "0.0.0.0"
        public_host = f"203.0.113.7:{self.port}"
        csrf = csrf_from(self.login_page(host=public_host).text)
        response = self.login(csrf=csrf, host=public_host, next_path="/")
        self.assertEqual(response.status, 303, response.text)
        cookie = cookie_from(response)
        with mock.patch.object(Handler, "_trusted_local", return_value=True):
            pair = request(self.base, "/api/mobile/pair", method="POST", body=b"{}",
                           headers={"Host": public_host, "Cookie": cookie,
                                    "Content-Type": "application/json"})
        self.assertEqual(pair.status, 200, pair.text)
        data = pair.json()
        self.assertEqual(data["base_source"], "request_host")
        self.assertTrue(data["pairing_url"].startswith(
            f"http://203.0.113.7:{self.port}/#pair="))

    def test_path_prefix_scopes_redirect_and_cookie(self):
        self.app.public_url = "https://ark.example.net/ark/"
        self.app.allow_advertised_url(self.app.public_url)
        self.app.configure_auth_transport()
        page = request(self.base, "/", headers={"Host": self.host})
        self.assertEqual(page.status, 302)
        self.assertTrue(page.headers["location"].startswith("/ark/login?next="))
        csrf = csrf_from(self.login_page(next_path="/ark/", host=self.host).text)
        login = self.login(csrf=csrf, next_path="/ark/", host=self.host)
        self.assertEqual(login.status, 303)
        self.assertEqual(login.headers["location"], "/ark/")
        self.assertIn("Path=/ark/", login.headers["set-cookie"])

    def test_unauthenticated_mobile_status_is_redacted(self):
        session = request(self.base, "/api/mobile/session", headers={
            "Host": self.public_host})
        self.assertEqual(session.status, 200)
        data = session.json()
        self.assertTrue(data["login_required"])
        self.assertIsNone(data["project"])


class PublicSelectionTests(unittest.TestCase):
    def test_request_host_beats_lan_and_explicit_public_ip_beats_request(self):
        app = SimpleNamespace(bind_host="0.0.0.0", port=8890, public_url="",
                              expose=True, public_ip="", detect_public_ip=False)
        with mock.patch("arkstudio.mobile._private_ipv4",
                        return_value=["192.168.1.20"]):
            base, source = mobile.resolve_pairing_base(
                app, request_host="203.0.113.7", request_port=8890,
                request_scheme="http")
        self.assertEqual((base, source), ("http://203.0.113.7:8890/", "request_host"))
        app.public_ip = "198.51.100.9"
        with mock.patch("arkstudio.mobile._private_ipv4",
                        return_value=["192.168.1.20"]):
            base, source = mobile.resolve_pairing_base(
                app, request_host="203.0.113.7", request_port=8890)
        self.assertEqual(base, "http://198.51.100.9:8890/")
        self.assertEqual(source, "public_ip")

    def test_opt_in_public_ip_detection_is_used_but_not_required(self):
        app = SimpleNamespace(bind_host="0.0.0.0", port=8890, public_url="",
                              expose=True, public_ip="", detect_public_ip=True)
        with mock.patch("arkstudio.mobile.detect_public_ip",
                        return_value={"ip": "198.51.100.9",
                                      "source": "https://api.ipify.org",
                                      "error": "", "checked": True, "enabled": True}), \
             mock.patch("arkstudio.mobile._private_ipv4", return_value=[]):
            info = mobile.connection_info(app)
            self.assertEqual(info["detected_public_ip"], "198.51.100.9")
            base, source = mobile.resolve_pairing_base(app)
        self.assertEqual(base, "http://198.51.100.9:8890/")
        self.assertEqual(source, "detected_public_ip")

    def test_loopback_is_not_auto_pairing_and_keeps_tunnel_command(self):
        app = SimpleNamespace(bind_host="127.0.0.1", port=8890, public_url="",
                              expose=False, public_ip="", detect_public_ip=False)
        info = mobile.connection_info(app)
        self.assertEqual(info["mode"], "loopback")
        self.assertFalse(info["pairing_auto"])
        self.assertIsNone(info["pairing_base"])
        self.assertIn("ssh -N -L", info["tunnel"]["command"])
        self.assertTrue(info["tunnel"]["required"])


if __name__ == "__main__":
    unittest.main()
