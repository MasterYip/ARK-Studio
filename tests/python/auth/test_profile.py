"""Self-service account profile: ``/api/me``, avatar and password change.

Real stdlib HTTP server on 127.0.0.1 with a temporary org database.  The
username stays the stable login ID; only display name, avatar, bio, email and
preferences are editable, and only by the account itself.
"""
from __future__ import annotations

import base64
import json
import os
import sqlite3
import struct
import tempfile
import unittest
import urllib.parse
import zlib
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from arkstudio import org
from arkstudio.server import App, serve

from tests.python.auth.test_org import AuthHttpCase, cookie_from, csrf_from, request


def tiny_png(width: int = 4, height: int = 4) -> bytes:
    raw = b"".join(b"\x00" + b"\xff\x00\x00" * width for _ in range(height))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + kind + data
                + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF))
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))


def b64(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


class ProfileHttpCase(AuthHttpCase):
    def me(self, cookie: str, **kwargs):
        return self.api("/api/me", cookie=cookie, **kwargs)

    def patch(self, cookie: str, body):
        return self.api("/api/me", cookie=cookie, method="PATCH", body=body)

    def login_remember(self, username: str, password: str) -> str:
        page = request(self.base, "/login?next=%2F", headers={"Host": self.host})
        payload = urllib.parse.urlencode({
            "username": username, "password": password, "remember": "on",
            "csrf": csrf_from(page.text), "next": "/",
        })
        response = request(self.base, "/login", method="POST", body=payload, headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Host": self.host, "Origin": f"http://{self.host}",
        })
        self.assertEqual(response.status, 303, response.text)
        return cookie_from(response)

    def pair_phone(self, cookie: str, permission: str) -> dict:
        pair = self.api("/api/mobile/pair", cookie=cookie, method="POST",
                        body={"permission": permission,
                              "tunnel_host": "192.168.1.20", "tunnel_port": 8891})
        self.assertEqual(pair.status, 200, pair.text)
        claim = self.api("/api/mobile/pair/claim", method="POST",
                         body={"token": pair.json()["code"]}, host="192.168.1.20:8891")
        self.assertEqual(claim.status, 200, claim.text)
        return {"X-ARK-Mobile-Token": claim.json()["mobile_token"],
                "Host": "192.168.1.20:8891"}

    def password(self, cookie: str, current: str, new: str, confirm: str | None = None):
        return self.api("/api/me/password", cookie=cookie, method="POST", body={
            "current_password": current, "new_password": new,
            "confirm_password": new if confirm is None else confirm})


class ProfileApiTests(ProfileHttpCase):
    def test_display_preferences_persist_and_validate(self):
        display = {"font_scale": 1.35, "high_contrast": True}
        response = self.patch(self.owner, {"prefs": {"display": display}})
        self.assertEqual(response.status, 200, response.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["prefs"]["display"], display)
        changed = self.patch(self.owner, {"prefs": {"display": {"font_scale": 1.5}}})
        self.assertEqual(changed.json()["profile"]["prefs"]["display"],
                         {"font_scale": 1.5, "high_contrast": True})
        for invalid in ({"font_scale": True}, {"font_scale": 2}, {"font_scale": .9},
                        {"font_scale": "1.2"}, {"high_contrast": "false"}, {"other": 1}, []):
            with self.subTest(invalid=invalid):
                self.assertEqual(self.patch(self.owner, {"prefs": {"display": invalid}}).status, 400)

    def test_get_profile_shape(self):
        response = self.me(self.owner)
        self.assertEqual(response.status, 200, response.text)
        data = response.json()
        profile = data["profile"]
        self.assertEqual(profile["username"], "owner")
        self.assertEqual(profile["display_name"], "owner")
        self.assertEqual(profile["avatar"]["kind"], "initials")
        self.assertIn(profile["avatar"]["color"], org.AVATAR_COLORS)
        self.assertEqual(profile["bio"], "")
        self.assertEqual(profile["email"], "")
        self.assertEqual(profile["prefs"], {})
        self.assertEqual(profile["permissions"]["role"], "owner")
        self.assertTrue(profile["permissions"]["can_change_password"])
        self.assertNotIn("password_hash", response.text)
        self.assertEqual(data["options"]["palette"], list(org.AVATAR_COLORS))
        unauthenticated = self.api("/api/me")
        self.assertEqual(unauthenticated.status, 401)

    def test_patch_validates_and_persists(self):
        color = org.AVATAR_COLORS[3]
        ok = self.patch(self.owner, {
            "display_name": "  Ada Lovelace  ", "bio": "PhD student\n· RL",
            "email": "ada@example.org", "avatar": {"kind": "initials", "color": color},
            "prefs": {"lang": "zh", "theme": "dark"}})
        self.assertEqual(ok.status, 200, ok.text)
        profile = ok.json()["profile"]
        self.assertEqual(profile["display_name"], "Ada Lovelace")
        self.assertEqual(profile["bio"], "PhD student · RL")
        self.assertEqual(profile["email"], "ada@example.org")
        self.assertEqual(profile["avatar"], {"kind": "initials", "color": color})
        self.assertEqual(profile["prefs"], {"lang": "zh", "theme": "dark"})
        # Partial prefs merge; the bootstrap directory carries the new name.
        self.assertEqual(self.patch(self.owner, {"prefs": {"theme": "system"}})
                         .json()["profile"]["prefs"], {"lang": "zh", "theme": "system"})
        boot = self.api("/api/bootstrap", cookie=self.owner).json()
        card = next(u for u in boot["users"] if u["username"] == "owner")
        self.assertEqual(card["display_name"], "Ada Lovelace")
        self.assertEqual(boot["profile"]["prefs"]["theme"], "system")
        bad = [
            {"display_name": "   "},
            {"display_name": "x" * (org.MAX_DISPLAY_NAME + 1)},
            {"bio": "b" * (org.MAX_BIO + 1)},
            {"email": "not-an-email"},
            {"email": "a@b.c<script>"},
            {"avatar": {"kind": "initials", "color": "#123456"}},
            {"avatar": {"kind": "image", "url": "https://evil.example/x.png"}},
            {"prefs": {"lang": "fr"}},
            {"prefs": {"theme": "neon"}},
            {"prefs": {"evil": 1}},
            ["not", "an", "object"],
        ]
        for body in bad:
            with self.subTest(body=body):
                response = self.patch(self.owner, body)
                self.assertEqual(response.status, 400, response.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"],
                         "Ada Lovelace")
        cleared = self.patch(self.owner, {"email": "", "bio": ""}).json()["profile"]
        self.assertEqual((cleared["email"], cleared["bio"]), ("", ""))

    def test_cannot_escalate_or_edit_another_account(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        alice = self.login("alice", "alice-pass")
        for body in ({"username": "owner"}, {"role": "owner"}, {"disabled": True},
                     {"password_hash": "x"}, {"display_name": "A", "role": "admin"},
                     {"id": "usr_local"}):
            with self.subTest(body=body):
                response = self.patch(alice, body)
                self.assertEqual(response.status, 400, response.text)
                self.assertEqual(response.json()["error"]["code"],
                                 "PROFILE_FIELD_FORBIDDEN")
        self.assertEqual(self.app.org.role_for("alice"), "member")
        self.assertEqual(self.me(alice).json()["profile"]["display_name"], "alice")
        # /api/me only ever edits the caller.
        self.assertEqual(self.patch(alice, {"display_name": "Alice A."}).status, 200)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"], "owner")
        owner_id = self.app.org.user_id_for("owner")
        denied = self.api(f"/api/org/users/{owner_id}", cookie=alice, method="PATCH",
                          body={"display_name": "pwned"})
        self.assertEqual(denied.status, 403, denied.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"], "owner")

    def test_cross_origin_and_task_agent_tokens_are_refused(self):
        cross = self.api("/api/me", cookie=self.owner, method="PATCH",
                         body={"display_name": "x"},
                         headers={"Host": self.host})
        self.assertEqual(cross.status, 200)  # same origin baseline
        evil = request(self.base, "/api/me", method="PATCH",
                       body=json.dumps({"display_name": "evil"}),
                       headers={"Host": self.host, "Cookie": self.owner,
                                "Content-Type": "application/json",
                                "Origin": "https://evil.example"})
        self.assertEqual(evil.status, 403, evil.text)
        fake = SimpleNamespace(id="sess", meta={"actor": "owner"})
        with mock.patch.object(self.app.sessions, "session_for_read_token",
                               side_effect=lambda token: fake if token == "tok" else None):
            for method, body in (("GET", None), ("PATCH", {"display_name": "agent"})):
                response = self.api("/api/me", cookie=self.owner, method=method, body=body,
                                    headers={"Authorization": "Bearer tok"})
                self.assertEqual(response.status, 403, response.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"], "x")


class PasswordChangeTests(ProfileHttpCase):
    def test_wrong_current_password_is_rejected_and_rate_limited(self):
        self.manager.login_limit = 3
        for _ in range(3):
            response = self.password(self.owner, "wrong-pass", "brand-new-pass")
            self.assertEqual(response.status, 403, response.text)
            self.assertEqual(response.json()["error"]["code"], "CURRENT_PASSWORD_INVALID")
        limited = self.password(self.owner, "owner-pass", "brand-new-pass")
        self.assertEqual(limited.status, 429, limited.text)
        self.assertTrue(limited.headers.get("retry-after"))
        # The session survived every failure.
        self.assertEqual(self.me(self.owner).status, 200)
        self.assertTrue(self.app.auth.account("owner"))

    def test_validation(self):
        mismatch = self.password(self.owner, "owner-pass", "brand-new-pass", "other-pass")
        self.assertEqual(mismatch.json()["error"]["code"], "PASSWORD_MISMATCH")
        same = self.password(self.owner, "owner-pass", "owner-pass")
        self.assertEqual(same.json()["error"]["code"], "PASSWORD_UNCHANGED")
        short = self.password(self.owner, "owner-pass", "x")
        self.assertEqual(short.json()["error"]["code"], "PASSWORD_TOO_SHORT")
        missing = self.password(self.owner, "", "brand-new-pass")
        self.assertEqual(missing.json()["error"]["code"], "PASSWORD_REQUIRED")

    def test_success_revokes_other_sessions_but_keeps_current(self):
        self.assertEqual(self.create_user("alice", "alice-pass").status, 200)
        self.assertEqual(self.grant(self.project_id(), user="alice",
                                    permission="write").status, 200)
        current = self.login("alice", "alice-pass")
        other = self.login("alice", "alice-pass")
        remembered = self.login_remember("alice", "alice-pass")
        phone = self.pair_phone(current, "full")
        owner_phone = self.pair_phone(self.owner, "read_only")
        sessions_file = self.app.root / ".auth-sessions.json"
        self.assertTrue(sessions_file.is_file())
        for cookie in (current, other, remembered):
            self.assertEqual(self.me(cookie).status, 200)
        self.assertEqual(self.api("/api/me", headers=phone).status, 200)
        # A paired phone cannot change the password itself.
        denied = self.api("/api/me/password", method="POST", headers=phone, body={
            "current_password": "alice-pass", "new_password": "alice-new-pass",
            "confirm_password": "alice-new-pass"})
        self.assertEqual(denied.status, 403, denied.text)
        changed = self.password(current, "alice-pass", "alice-new-pass")
        self.assertEqual(changed.status, 200, changed.text)
        self.assertEqual(changed.json()["sessions_revoked"], 2)
        self.assertEqual(changed.json()["devices_revoked"], 1)
        self.assertEqual(self.me(current).status, 200)
        self.assertEqual(self.me(other).status, 401)
        self.assertEqual(self.me(remembered).status, 401)
        self.assertEqual(self.api("/api/me", headers=phone).status, 401)
        # Other accounts are untouched.
        self.assertEqual(self.me(self.owner).status, 200)
        self.assertEqual(self.api("/api/me", headers=owner_phone).status, 200)
        # The persisted remember-me store no longer holds the revoked session.
        stored = json.loads(sessions_file.read_text(encoding="utf-8"))
        remembered_users = [item for item in stored.get("sessions") or []
                            if str(item.get("username")).lower() == "alice"]
        self.assertEqual(remembered_users, [])
        mobile_store = json.loads((self.app.root / ".mobile-sessions.json")
                                  .read_text(encoding="utf-8"))
        self.assertFalse([s for s in mobile_store["sessions"] if s["user"] == "alice"])
        # Old password fails, new one works.
        page = request(self.base, "/login?next=%2F", headers={"Host": self.host})
        old = request(self.base, "/login", method="POST", body=urllib.parse.urlencode({
            "username": "alice", "password": "alice-pass",
            "csrf": csrf_from(page.text), "next": "/"}), headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Host": self.host, "Origin": f"http://{self.host}"})
        self.assertNotEqual(old.status, 303)
        self.assertTrue(self.login("alice", "alice-new-pass"))


class AvatarTests(ProfileHttpCase):
    def upload(self, cookie: str, data: bytes, content_type: str = "image/png", **kw):
        return self.api("/api/me/avatar", cookie=cookie, method="POST",
                        body={"content_type": content_type, "data": b64(data)}, **kw)

    def test_upload_fetch_and_remove(self):
        png = tiny_png()
        response = self.upload(self.owner, png)
        self.assertEqual(response.status, 200, response.text)
        avatar = response.json()["profile"]["avatar"]
        self.assertEqual(avatar["kind"], "image")
        self.assertTrue(avatar["url"].startswith("api/users/owner/avatar?v="))
        stored = list(self.app.org.avatar_dir.glob("*.png"))
        self.assertEqual(len(stored), 1)
        self.assertNotIn(str(self.app.root), str(stored[0].parent))
        fetched = self.api("/" + avatar["url"], cookie=self.owner)
        self.assertEqual(fetched.status, 200)
        self.assertEqual(fetched.headers["content-type"], "image/png")
        self.assertEqual(fetched.headers["x-content-type-options"], "nosniff")
        self.assertIn("sandbox", fetched.headers["content-security-policy"])
        # Another member of the org can see it; anonymous callers cannot.
        self.assertEqual(self.create_user("bob", "bob-pass1").status, 200)
        bob = self.login("bob", "bob-pass1")
        self.assertEqual(self.api("/" + avatar["url"], cookie=bob).status, 200)
        self.assertEqual(self.api("/" + avatar["url"]).status, 401)
        self.assertEqual(self.api("/api/users/bob/avatar", cookie=bob).status, 404)
        boot = self.api("/api/bootstrap", cookie=bob).json()
        card = next(u for u in boot["users"] if u["username"] == "owner")
        self.assertEqual(card["avatar"]["kind"], "image")
        removed = self.api("/api/me/avatar", cookie=self.owner, method="DELETE")
        self.assertEqual(removed.status, 200, removed.text)
        self.assertEqual(removed.json()["profile"]["avatar"]["kind"], "initials")
        self.assertEqual(list(self.app.org.avatar_dir.glob("*.png")), [])
        self.assertEqual(self.api("/" + avatar["url"], cookie=self.owner).status, 404)

    def test_type_magic_size_and_svg_checks(self):
        svg = b'<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>'
        cases = [
            (svg, "image/svg+xml", 415),
            (svg, "image/png", 415),
            (b"\xff\xd8\xff\xe0" + b"0" * 64, "image/png", 415),
            (tiny_png(), "text/html", 415),
            (b"GIF89a" + b"0" * 32, "image/gif", 415),
            (tiny_png(4000, 4), "image/png", 400),
            (b"\x89PNG\r\n\x1a\n" + b"0" * (org.MAX_AVATAR_BYTES + 1), "image/png", 413),
        ]
        for data, ctype, status in cases:
            with self.subTest(ctype=ctype, size=len(data)):
                self.assertEqual(self.upload(self.owner, data, ctype).status, status)
        not_base64 = self.api("/api/me/avatar", cookie=self.owner, method="POST",
                              body={"content_type": "image/png", "data": "@@@"})
        self.assertEqual(not_base64.status, 400)
        self.assertEqual(self.me(self.owner).json()["profile"]["avatar"]["kind"], "initials")
        jpeg = b"\xff\xd8\xff\xe0" + b"0" * 64
        self.assertEqual(self.upload(self.owner, jpeg, "image/jpeg").status, 200)
        webp = b"RIFF\x10\x00\x00\x00WEBPVP8 " + b"0" * 32
        self.assertEqual(self.upload(self.owner, webp, "image/webp").status, 200)
        self.assertEqual([p.suffix for p in self.app.org.avatar_dir.iterdir()], [".webp"])
        # Choosing an initials colour drops the uploaded image.
        color = org.AVATAR_COLORS[0]
        self.assertEqual(self.patch(self.owner, {"avatar": {"kind": "initials",
                                                            "color": color}}).status, 200)
        self.assertEqual(list(self.app.org.avatar_dir.iterdir()), [])


class MobileProfileTests(ProfileHttpCase):
    def test_read_only_device_reads_but_cannot_write(self):
        phone = self.pair_phone(self.owner, "read_only")
        read = self.api("/api/me", headers=phone)
        self.assertEqual(read.status, 200, read.text)
        self.assertTrue(read.json()["profile"]["permissions"]["read_only"])
        self.assertFalse(read.json()["profile"]["permissions"]["can_change_password"])
        for path, method, body in (
                ("/api/me", "PATCH", {"display_name": "phone"}),
                ("/api/me/avatar", "POST", {"content_type": "image/png",
                                            "data": b64(tiny_png())}),
                ("/api/me/avatar", "DELETE", None),
                ("/api/me/password", "POST", {"current_password": "owner-pass",
                                              "new_password": "owner-new-pass",
                                              "confirm_password": "owner-new-pass"})):
            with self.subTest(path=path, method=method):
                response = self.api(path, method=method, body=body, headers=phone)
                self.assertEqual(response.status, 403, response.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"], "owner")

    def test_full_access_device_may_edit_profile(self):
        phone = self.pair_phone(self.owner, "full")
        response = self.api("/api/me", method="PATCH", headers=phone,
                            body={"display_name": "From phone"})
        self.assertEqual(response.status, 200, response.text)
        self.assertEqual(self.me(self.owner).json()["profile"]["display_name"], "From phone")


class LocalModeProfileTests(unittest.TestCase):
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

    def api(self, path, *, method="GET", body=None, headers=None):
        merged = {"Content-Type": "application/json"}
        merged.update(headers or {})
        return request(self.base, path, method=method,
                       body=json.dumps(body) if body is not None else None,
                       headers=merged)

    def test_local_profile_works_without_password(self):
        me = self.api("/api/me").json()["profile"]
        self.assertEqual(me["username"], "local")
        self.assertEqual(me["display_name"], "Local user")
        self.assertTrue(me["permissions"]["local"])
        self.assertFalse(me["permissions"]["can_change_password"])
        patched = self.api("/api/me", method="PATCH", body={
            "display_name": "Me", "bio": "Solo", "prefs": {"lang": "zh"}})
        self.assertEqual(patched.status, 200, patched.text)
        self.assertEqual(patched.json()["profile"]["display_name"], "Me")
        upload = self.api("/api/me/avatar", method="POST",
                          body={"content_type": "image/png", "data": b64(tiny_png())})
        self.assertEqual(upload.status, 200, upload.text)
        url = upload.json()["profile"]["avatar"]["url"]
        self.assertEqual(self.api("/" + url).status, 200)
        password = self.api("/api/me/password", method="POST", body={
            "current_password": "a", "new_password": "bbbbbbbbb",
            "confirm_password": "bbbbbbbbb"})
        self.assertEqual(password.status, 409, password.text)
        self.assertEqual(password.json()["error"]["code"], "LOCAL_MODE")
        fake = SimpleNamespace(id="sess", meta={"actor": "local"})
        with mock.patch.object(self.app.sessions, "session_for_read_token",
                               side_effect=lambda token: fake if token == "tok" else None):
            agent = self.api("/api/me", headers={"Authorization": "Bearer tok"})
            self.assertEqual(agent.status, 403, agent.text)


class MigrationTests(unittest.TestCase):
    def test_existing_v2_database_gains_profile_columns(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "org.sqlite3"
            con = sqlite3.connect(path)
            con.executescript("""
                CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
                INSERT INTO meta VALUES('schema_version','2');
                CREATE TABLE users(
                  id TEXT PRIMARY KEY,
                  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
                  display_name TEXT NOT NULL DEFAULT '',
                  password_hash TEXT,
                  disabled INTEGER NOT NULL DEFAULT 0,
                  created_at TEXT NOT NULL,
                  updated_at TEXT NOT NULL);
                INSERT INTO users VALUES('usr_old','olduser','Old User','hash',0,'t','t');
            """)
            con.commit()
            con.close()
            for _ in range(2):  # idempotent
                manager = org.OrgManager(path, private_dir=Path(tmp) / "private")
                columns = {row[1] for row in sqlite3.connect(path).execute(
                    "PRAGMA table_info(users)")}
                for name, _decl in org.USER_PROFILE_COLUMNS:
                    self.assertIn(name, columns)
                row = manager.store.user_by_name("olduser")
                self.assertEqual(row["display_name"], "Old User")
                self.assertEqual(row["password_hash"], "hash")
                self.assertEqual(row["bio"], "")
                self.assertEqual(row["prefs_json"], "{}")
                profile = manager.profile("olduser")
                self.assertEqual(profile["avatar"]["kind"], "initials")
                manager.close()
            version = sqlite3.connect(path).execute(
                "SELECT value FROM meta WHERE key='schema_version'").fetchone()[0]
            self.assertEqual(version, str(org.ORG_SCHEMA_VERSION))


if __name__ == "__main__":
    unittest.main()
