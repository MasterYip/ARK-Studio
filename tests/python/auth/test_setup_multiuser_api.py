"""Real account tokens see the shared register and retain project permissions."""
import json

from tests.python.auth.test_org import AuthHttpCase, request


class MultiUserSetupHttpTests(AuthHttpCase):
    def setUp(self):
        super().setUp()
        self.main = self.project_id()
        self.cookies = {"owner": self.owner}
        self.tokens = {}
        for name in ("alice", "bob", "outsider"):
            self.assertEqual(self.create_user(name, f"{name}-pass").status, 200)
            if name != "outsider":
                self.assertEqual(self.grant(self.main, user=name, permission="write").status, 200)
            self.cookies[name] = self.login(name, f"{name}-pass")
            token = self.api("/api/personal/agent-tokens", cookie=self.cookies[name],
                             method="POST", body={"label": "setup", "scope": "write"})
            self.tokens[name] = token.json()["token"]

    def agent(self, name, action="", body=None, host=""):
        headers = {"Host": self.host, "Authorization": f"Bearer {self.tokens[name]}",
                   "Content-Type": "application/json", "X-ARK-Project": self.main}
        if host:
            headers["X-ARK-Host"] = host
        return request(self.base, "/api/agent/v1/setup" + (f"/{action}" if action else ""),
                       method="POST" if body is not None else "GET", headers=headers,
                       body=json.dumps(body) if body is not None else None)

    def draft(self, name, rid, path, host):
        response = self.agent(name, "draft", {"resources": [{"id": rid, "kind": "repository",
            "path_or_ref": path, "details": {"role": "code"}}], "propose": True}, host)
        self.assertEqual(response.status, 200, response.text)
        self.assertTrue(response.json().get("ok", True), response.text)

    def test_second_user_setup_preserves_first_and_updates_both_token_views(self):
        self.draft("alice", "code", "/alice/code", "alice-pc")
        self.draft("bob", "data", "/bob/data", "bob-pc")
        self.draft("alice", "paper", "/alice/paper", "alice-pc")
        for name, host in (("alice", "alice-pc"), ("bob", "bob-pc")):
            response = self.agent(name, host=host)
            self.assertEqual(response.status, 200, response.text)
            items = {i["key"]: i for i in response.json()["resources"]}
            self.assertEqual(set(items), {"u:alice/code", "u:alice/paper", "u:bob/data"})
            self.assertIn("/bob/data", response.text)
            self.assertIn("/alice/code", response.text)
            if name == "bob":
                self.assertFalse(items["u:alice/code"]["usable_here"])

    def test_member_can_record_own_location_for_another_members_resource(self):
        self.draft("alice", "code", "/alice/code", "alice-pc")
        response = self.agent("bob", "location", {"resource": "u:alice/code",
            "path_or_ref": "/bob/checkout", "host_label": "Bob PC"}, host="bob-pc")
        self.assertEqual(response.status, 200, response.text)
        view = self.agent("alice", host="alice-pc").json()
        paths = view["resources"][0]["locations"]
        self.assertEqual({i["path_or_ref"] for i in paths}, {"/alice/code", "/bob/checkout"})

    def test_cannot_replace_another_members_contribution_or_self_confirm_as_agent(self):
        self.draft("alice", "code", "/alice/code", "alice-pc")
        response = self.agent("bob", "remove", {"owner": "alice", "id": "code"})
        self.assertEqual(response.status, 403, response.text)
        digest = self.agent("alice").json()["layers"]["user"]["digest"]
        self.assertEqual(self.agent("alice", "confirm", {"digest": digest}).status, 403)

    def test_nonmember_has_no_access_to_register(self):
        self.draft("alice", "code", "/alice/code", "alice-pc")
        response = self.agent("outsider")
        self.assertIn(response.status, (403, 404), response.text)
        self.assertNotIn("/alice/code", response.text)

    def test_home_setup_status_matches_the_signed_in_member_and_machine(self):
        self.draft("alice", "code", "/alice/code", "alice-pc")
        self.draft("bob", "data", "/bob/data", "bob-pc")
        for name in ("alice", "bob"):
            headers = {"X-ARK-Project": self.main, "X-ARK-Host": f"{name}-pc"}
            setup = self.api("/api/skills/setup/state", cookie=self.cookies[name], headers=headers)
            home = self.api("/api/project/overview", cookie=self.cookies[name], headers=headers)
            self.assertEqual(setup.status, 200, setup.text)
            self.assertEqual(home.status, 200, home.text)
            for key in ("state", "layers", "readiness", "host"):
                self.assertEqual(home.json()["setup"][key], setup.json()[key])
            self.assertEqual(home.json()["setup"]["layers"]["user"]["owner"], name)
