"""Session context in account mode: token scope, consent, and per-session privacy.

Sessions are private to their creator; "shared" opens one to project members;
project admins see and manage every session (docs/agents/session-context.md).
"""
import json
import time
from unittest import mock

from tests.python.auth.test_org import AuthHttpCase

LINE = json.dumps({"type": "user", "message": {"role": "user", "content": "private plan"}}) + "\n"


class SessionUploadAuthTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        made = self.api("/api/projects", cookie=self.owner, method="POST", body={"name": "Lab"})
        self.assertEqual(made.status, 200, made.text)
        self.project = made.json()["summary"]["id"]
        self.cookies = {}
        for name in ("writer", "bob"):
            self.assertEqual(self.create_user(name, f"{name}-pass").status, 200)
            self.cookies[name] = self.login(name, f"{name}-pass")
            self.assertEqual(self.grant(self.project, user=name, permission="write").status, 200)
            self.open_lab(self.cookies[name])
        self.open_lab(self.owner)

    def open_lab(self, cookie):
        opened = self.api("/api/projects/open", cookie=cookie, method="POST", body={"id": self.project})
        self.assertEqual(opened.status, 200, opened.text)

    def token(self, cookie, scope="write"):
        made = self.api("/api/personal/agent-tokens", cookie=cookie, method="POST",
                        body={"label": "session", "scope": scope, "expires_days": 30})
        self.assertEqual(made.status, 200, made.text)
        return made.json()["token"]

    def bearer(self, path, token, *, method="GET", body=None, project=None):
        return self.api(path, method=method, body=body,
                        headers={"Authorization": f"Bearer {token}", "X-ARK-Project": project or self.project})

    def upload(self, token, project=None, sid="ark-ses-auth001"):
        return self.bearer(f"/api/agent/v1/sessions/{sid}/upload", token, method="POST",
                           body={"offset": 0, "content": LINE}, project=project)

    def set_consent(self, cookie, value):
        return self.api("/api/project/settings", cookie=cookie, method="PATCH",
                        body={"sessions": {"accept_uploads": value}})

    def listed(self, cookie):
        response = self.api("/api/session-contexts", cookie=cookie)
        self.assertEqual(response.status, 200, response.text)
        return {item["id"]: item for item in response.json()["sessions"]}

    def stop(self, session_id):
        try:
            self.app.sessions.request_stop(session_id)
        except Exception:  # noqa: BLE001 - already finished
            return
        for _ in range(200):
            if not self.app.sessions.get(session_id).is_active:
                return
            time.sleep(0.05)

    def test_consent_is_admin_only_and_scope_is_enforced(self):
        self.assertEqual(self.set_consent(self.cookies["writer"], True).status, 403)
        self.assertEqual(self.set_consent(self.owner, True).status, 200)
        with mock.patch("arkstudio.server.Handler._trusted_local", return_value=False):
            read_token = self.token(self.cookies["writer"], "read")
            response = self.upload(read_token)
            self.assertEqual(response.status, 403, response.text)
            write_token = self.token(self.cookies["writer"])
            response = self.upload(write_token)
            self.assertEqual(response.status, 200, response.text)
            self.assertEqual(response.json()["session"]["uploaded_by"], "agent:writer")
            self.assertEqual(response.json()["session"]["owner"], "writer")
            status = self.bearer("/api/agent/v1/sessions/ark-ses-auth001", read_token)
            self.assertEqual(status.status, 200, status.text)
            self.assertTrue(status.json()["exists"])
            other = self.api("/api/projects", cookie=self.owner, method="POST", body={"name": "Private"}).json()
            other_id = (other.get("summary") or {}).get("id") or ""
            self.assertTrue(other_id)
            self.assertNotEqual(other_id, self.project)
            response = self.upload(write_token, project=other_id, sid="ark-ses-auth002")
            self.assertIn(response.status, {403, 404}, response.text)

    def test_external_sessions_are_private_until_shared(self):
        self.assertEqual(self.set_consent(self.owner, True).status, 200)
        sid = "ark-ses-auth001"
        writer_token = self.token(self.cookies["writer"])
        bob_token = self.token(self.cookies["bob"])
        self.assertEqual(self.upload(writer_token).status, 200)
        bob = self.cookies["bob"]
        # Another member can neither take over the id nor see the session.
        hijack = self.upload(bob_token)
        self.assertEqual((hijack.status, hijack.json()["error"]["code"]), (403, "SESSION_UPLOAD_NOT_OWNER"))
        self.assertEqual(self.bearer(f"/api/agent/v1/sessions/{sid}", bob_token).status, 404)
        self.assertNotIn(sid, self.listed(bob))
        for suffix in ("", "/transcript", "/raw", "/context"):
            with self.subTest(path=suffix):
                response = self.api(f"/api/session-contexts/{sid}{suffix}", cookie=bob)
                self.assertEqual(response.status, 404, response.text)
        response = self.api(f"/api/session-contexts/{sid}/continue", cookie=bob, method="POST",
                            body={"mode": "seed", "platform": "echo", "text": "go"})
        self.assertEqual(response.status, 404, response.text)
        self.assertEqual(self.api(f"/api/session-contexts/{sid}", cookie=bob, method="PATCH",
                                  body={"shared": True}).status, 404)
        # The creator sees it, may manage it, and shares it.
        mine = self.listed(self.cookies["writer"])[sid]
        self.assertEqual((mine["owner"], mine["shared"], mine["can_manage"]), ("writer", False, True))
        bad = self.api(f"/api/session-contexts/{sid}", cookie=self.cookies["writer"], method="PATCH",
                       body={"shared": "yes"})
        self.assertEqual(bad.status, 400)
        shared = self.api(f"/api/session-contexts/{sid}", cookie=self.cookies["writer"], method="PATCH",
                          body={"shared": True})
        self.assertEqual((shared.status, shared.json()["session"]["shared"]), (200, True))
        # Shared: readable by members, still only managed by its creator or an admin.
        seen = self.listed(bob)[sid]
        self.assertFalse(seen["can_manage"])
        for suffix in ("/transcript", "/raw", "/context"):
            self.assertEqual(self.api(f"/api/session-contexts/{sid}{suffix}", cookie=bob).status, 200)
        rename = self.api(f"/api/session-contexts/{sid}", cookie=bob, method="PATCH", body={"title": "mine now"})
        self.assertEqual((rename.status, rename.json()["error"]["code"]), (403, "SESSION_NOT_OWNER"))
        self.assertEqual(self.api(f"/api/session-contexts/{sid}", cookie=bob, method="DELETE").status, 403)
        # A project admin sees and deletes any session, shared or not.
        self.api(f"/api/session-contexts/{sid}", cookie=self.cookies["writer"], method="PATCH",
                 body={"shared": False})
        self.assertNotIn(sid, self.listed(bob))
        admin_view = self.listed(self.owner)[sid]
        self.assertTrue(admin_view["can_manage"])
        self.assertEqual(self.api(f"/api/session-contexts/{sid}/transcript", cookie=self.owner).status, 200)
        deleted = self.api(f"/api/session-contexts/{sid}", cookie=self.owner, method="DELETE")
        self.assertEqual(deleted.status, 200, deleted.text)

    def test_internal_sessions_are_private_in_runs_too(self):
        writer = self.cookies["writer"]
        started = self.api("/api/agent-sessions", cookie=writer, method="POST",
                           body={"platform": "echo", "prompt": "private run"})
        self.assertEqual(started.status, 200, started.text)
        sid = started.json()["session"]["id"]
        try:
            bob = self.cookies["bob"]
            listing = self.api("/api/agent-sessions", cookie=bob).json()
            self.assertNotIn(sid, [item["id"] for item in listing["sessions"]])
            for path in (f"/api/agent-sessions/{sid}", f"/api/agent-sessions/{sid}/transcript",
                         f"/api/session-contexts/{sid}", f"/api/session-contexts/{sid}/raw"):
                with self.subTest(path=path):
                    self.assertEqual(self.api(path, cookie=bob).status, 404)
            stop = self.api(f"/api/agent-sessions/{sid}/stop", cookie=bob, method="POST", body={})
            self.assertEqual(stop.status, 404)
            owner_list = self.api("/api/agent-sessions", cookie=self.owner).json()
            self.assertIn(sid, [item["id"] for item in owner_list["sessions"]])
            self.stop(sid)
            shared = self.api(f"/api/session-contexts/{sid}", cookie=writer, method="PATCH", body={"shared": True})
            self.assertEqual(shared.status, 200, shared.text)
            self.assertEqual(self.api(f"/api/agent-sessions/{sid}/transcript", cookie=bob).status, 200)
            self.assertEqual(self.api(f"/api/agent-sessions/{sid}", cookie=bob, method="DELETE").status, 403)
            resume = self.api(f"/api/session-contexts/{sid}/continue", cookie=bob, method="POST",
                              body={"mode": "resume", "text": "x"})
            self.assertIn(resume.status, {403, 409})
        finally:
            self.stop(sid)
