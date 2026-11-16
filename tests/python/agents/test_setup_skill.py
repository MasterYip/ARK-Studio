"""Tests for the environment setup skill (first-run human-in-the-loop binding)."""
import json
import os
import subprocess
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio import skills
from arkstudio.skills import setup
from arkstudio.server import App, serve


def _git_repo(root: Path) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    run = lambda *args: subprocess.run(["git", *args], cwd=root, check=True,
                                       capture_output=True)
    run("init", "-q", "-b", "main")
    run("config", "user.email", "t@example.com")
    run("config", "user.name", "t")
    (root / "pyproject.toml").write_text("[tool.pytest.ini_options]\n", encoding="utf-8")
    (root / "tests").mkdir()
    (root / "AGENTS.md").write_text("rules\n", encoding="utf-8")
    run("add", "-A")
    run("commit", "-q", "-m", "init")
    run("remote", "add", "origin", "https://user:pw@example.com/org/repo.git")
    return root


class ProfileTests(unittest.TestCase):
    def test_example_profile_is_valid_and_complete(self):
        result = setup.validate_profile(setup.example_profile())
        self.assertTrue(result["ok"], result["errors"])
        self.assertEqual(result["questions"], [])

    def test_empty_profile_asks_required_questions(self):
        result = setup.validate_profile({})
        fields = [q["field"] for q in result["questions"] if q["required"]]
        for field in ("objective", "repositories", "commands[gate=unit]", "policies"):
            self.assertIn(field, fields)
        self.assertFalse(result["readiness"]["ready"])

    def test_policy_defaults_are_conservative_and_listed(self):
        profile, _, _ = setup.normalize_profile({"policies": {"push": "allow"}})
        self.assertEqual(profile["policies"]["push"], "allow")
        self.assertEqual(profile["policies"]["purchase"], "deny")
        self.assertEqual(profile["policies"]["deployment"], "ask")
        self.assertNotIn("push", profile["policies_defaulted"])
        self.assertIn("deployment", profile["policies_defaulted"])

    def test_secret_values_are_rejected(self):
        result = setup.validate_profile({
            "secrets": [{"id": "k", "ref": "sk-abcdefghijklmnopqrstuvwx"}],
            "notes": "token ghp_abcdefghijklmnopqrstuvwxyz0123",
            "agents": {"api_key": "plain-value"},
        })
        self.assertFalse(result["ok"])
        joined = " ".join(result["errors"])
        self.assertIn("secrets[0].ref", joined)
        self.assertIn("notes", joined)
        self.assertIn("agents.api_key", joined)
        ok = setup.validate_profile({"secrets": [{"id": "k", "ref": "env:WANDB_API_KEY"}]})
        self.assertTrue(ok["ok"], ok["errors"])

    def test_remote_user_info_is_stripped(self):
        profile, errors, _ = setup.normalize_profile(
            {"repositories": [{"id": "a", "path": "/x", "remote": "https://github.com/o/r"}]})
        self.assertFalse(errors)
        self.assertEqual(setup.sanitize_remote("https://u:p@h/o/r.git"), "https://h/o/r.git")

    def test_cross_references_and_multiple_primaries(self):
        result = setup.validate_profile({
            "repositories": [{"id": "a", "path": "/a", "role": "primary"},
                             {"id": "b", "path": "/b", "role": "primary"}],
            "commands": [{"id": "u", "run": "x", "repository": "zzz", "gate": "unit"}],
            "environments": [{"id": "e", "repository": "nope"}],
        })
        joined = " ".join(result["errors"])
        self.assertEqual([item["id"] for item in result["profile"]["repositories"]], ["a", "b"])
        self.assertEqual(result["readiness"]["primary_repositories"], ["a", "b"])
        self.assertNotIn("only one repository", joined)
        self.assertIn("unknown repository 'zzz'", joined)
        self.assertIn("unknown repository 'nope'", joined)

    def test_unknown_gate_rejected(self):
        result = setup.validate_profile({"commands": [{"id": "x", "run": "y", "gate": "vibes"}]})
        self.assertFalse(result["ok"])

    def test_digest_is_order_independent(self):
        a = setup.normalize_profile({"objective": "o", "non_goals": ["n"]})[0]
        b = json.loads(json.dumps(a))
        self.assertEqual(setup.digest(a), setup.digest(dict(reversed(list(b.items())))))


class SkipTests(unittest.TestCase):
    def test_skipped_stages_drop_questions_and_report_skip(self):
        profile = setup.example_profile()
        profile.update(skipped=["compute", "commands"], compute=[], commands=[])
        result = setup.validate_profile(profile)
        self.assertTrue(result["ok"], result["errors"])
        self.assertFalse([q for q in result["questions"] if q["stage"] in {"compute", "commands"}])
        ready = setup.readiness(result["profile"], "confirmed")
        self.assertTrue(ready["ready"])
        reasons = {item["capability"]: item for item in ready["blocked"]}
        self.assertTrue(reasons["gpu"]["skipped"])
        self.assertIn("skipped", reasons["gates"]["reason"])
        self.assertIn("Skipped by the user", setup.render_summary(
            {"state": "draft", "profile": result["profile"]}))

    def test_skip_scope_allows_ready_without_objective(self):
        everything = list(setup.STAGE_IDS)
        result = setup.validate_profile({"skipped": everything})
        self.assertEqual(result["questions"], [])
        self.assertTrue(setup.readiness(result["profile"], "confirmed")["ready"])
        partial = setup.validate_profile({"skipped": everything[1:]})
        self.assertFalse(setup.readiness(partial["profile"], "confirmed")["ready"])

    def test_unknown_skip_rejected(self):
        self.assertFalse(setup.validate_profile({"skipped": ["vibes"]})["ok"])

    def test_skipped_commands_silence_gate_warnings(self):
        profile = setup.normalize_profile({"skipped": ["commands", "code"]})[0]
        self.assertEqual(setup.check_task(profile, "confirmed",
                                          {"repository": "x", "gates": ["unit"]}), [])

    def test_agents_and_server_questions_point_to_configs(self):
        prompts = {q["field"]: q["prompt"] for q in setup.questions(setup.normalize_profile({})[0])}
        self.assertIn("agents.providers", prompts)
        self.assertIn("gpu_servers.yaml", prompts["compute"])


class DiscoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_discover_repository_task_space_and_gpu_names(self):
        repo = _git_repo(self.root / "code")
        team = repo / "docs" / "agent_team"
        team.mkdir(parents=True)
        (team / "agent_team_init.md").write_text("# init\n", encoding="utf-8")
        (repo / "dirty.txt").write_text("x", encoding="utf-8")
        result = setup.discover(
            [str(repo)], gpu_servers={"servers": [{"name": "gpu-a", "tags": ["h200"]}]},
            providers={"providers": {"dsh": {"installed": True}, "x": {"installed": False}}})
        draft = result["draft"]
        self.assertEqual(draft["repositories"][0]["role"], "primary")
        self.assertEqual(draft["repositories"][0]["default_branch"], "main")
        self.assertEqual(draft["repositories"][0]["remote"], "https://example.com/org/repo.git")
        self.assertEqual(draft["repositories"][0]["instructions"], ["AGENTS.md"])
        self.assertEqual(draft["task_spaces"][0]["kind"], "agent-team-folder")
        self.assertEqual(draft["task_spaces"][0]["access"], "read-only")
        self.assertIn("gpu-a", [c["gpu_server"] for c in draft["compute"]])
        self.assertEqual(draft["agents"]["providers"], ["dsh"])
        unit = [c for c in draft["commands"] if c["gate"] == "unit"]
        self.assertEqual(unit[0]["run"], "python -m pytest -q")
        self.assertTrue(unit[0]["proposed"])
        self.assertTrue(any("uncommitted" in str(o.get("detail")) for o in result["observations"]))
        self.assertIn("objective", [q["field"] for q in result["questions"]])

    def test_discover_missing_path_is_reported_not_raised(self):
        result = setup.discover([str(self.root / "missing")])
        self.assertFalse(result["observations"][0]["exists"])
        self.assertEqual(result["draft"]["repositories"], [])


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.project = self.root / "p.ark"
        self.project.mkdir()
        repo = _git_repo(self.root / "code")
        self.profile = setup.example_profile()
        self.profile["repositories"] = [{"id": "code", "path": str(repo), "role": "primary",
                                         "default_branch": "main"}]
        self.profile["environments"] = []
        self.profile["commands"] = [{"id": "unit", "run": "python -m pytest -q",
                                     "repository": "code", "gate": "unit"}]
        self.profile["compute"] = [{"id": "gpu-a", "kind": "ssh", "gpu_server": "gpu-a"}]
        self.profile["storage"] = []
        self.profile["task_spaces"] = []

    def tearDown(self):
        self.tmp.cleanup()

    def test_unset_state(self):
        state = setup.state_payload(self.project)
        self.assertEqual(state["state"], "unset")
        self.assertFalse(state["readiness"]["ready"])

    def test_full_lifecycle_with_human_gate(self):
        draft = setup.save_draft(self.project, self.profile, actor="agent:dsh", propose=True)
        self.assertEqual(draft["state"], "proposed")
        self.assertIn(draft["digest"], draft["summary_markdown"])
        with self.assertRaises(setup.SetupError) as ctx:
            setup.confirm(self.project, actor="agent:dsh", digest_value=draft["digest"])
        self.assertEqual(ctx.exception.status, 403)
        with self.assertRaises(setup.SetupError) as ctx:
            setup.confirm(self.project, actor="human:ui", digest_value="0000")
        self.assertEqual(ctx.exception.code, "SETUP_DIGEST")
        confirmed = setup.confirm(self.project, actor="human:ui", digest_value=draft["digest"])
        self.assertEqual(confirmed["state"], "confirmed")
        self.assertTrue(confirmed["readiness"]["ready"])

        result = setup.verify(confirmed["profile"], gpu_servers={"servers": [{"name": "gpu-a"}]})
        self.assertTrue(result["ok"], result["checks"])
        verified = setup.record_verification(self.project, result, actor="agent:dsh")
        self.assertEqual(verified["state"], "verified")

        # Any edit drops back to draft and needs a new confirmation.
        edited = dict(self.profile, objective="changed")
        again = setup.save_draft(self.project, edited, actor="agent:dsh")
        self.assertEqual(again["state"], "draft")
        self.assertIsNone(again["confirmation"])
        history = (self.project / "setup" / "history.jsonl").read_text(encoding="utf-8")
        events = [json.loads(line)["event"] for line in history.splitlines()]
        self.assertEqual(events, ["proposed", "confirmed", "verify", "draft"])

    def test_hand_edit_after_confirmation_is_stale(self):
        draft = setup.save_draft(self.project, self.profile, actor="agent:a", propose=True)
        setup.confirm(self.project, actor="human:ui", digest_value=draft["digest"])
        path = self.project / "setup" / "project.json"  # format 2.0 project layer
        record = json.loads(path.read_text(encoding="utf-8"))
        record["profile"]["objective"] = "sneaky"
        path.write_text(json.dumps(record), encoding="utf-8")
        self.assertEqual(setup.load(self.project)["state"], "stale")

    def test_verify_failures_keep_confirmed(self):
        self.profile["compute"] = [{"id": "gpu-z", "kind": "ssh", "gpu_server": "gpu-z"}]
        draft = setup.save_draft(self.project, self.profile, actor="agent:a", propose=True)
        setup.confirm(self.project, actor="human:ui", digest_value=draft["digest"])
        result = setup.verify(setup.load(self.project)["profile"],
                              gpu_servers={"servers": [{"name": "gpu-a"}]})
        self.assertFalse(result["ok"])
        state = setup.record_verification(self.project, result, actor="agent:a")
        self.assertEqual(state["state"], "confirmed")

    def test_secret_draft_is_refused(self):
        self.profile["notes"] = "-----BEGIN OPENSSH PRIVATE KEY-----"
        with self.assertRaises(setup.SetupError) as ctx:
            setup.save_draft(self.project, self.profile, actor="agent:a")
        self.assertEqual(ctx.exception.code, "SETUP_SECRET")
        self.assertFalse((self.project / "setup" / "project.json").exists())

    def test_check_task_against_profile(self):
        profile = setup.normalize_profile(self.profile)[0]
        self.assertTrue(setup.check_task(profile, "draft", {})[0].startswith("environment setup"))
        warnings = setup.check_task(profile, "confirmed",
                                    {"repository": "elsewhere", "gates": ["static", "unit"]})
        self.assertTrue(any("not registered" in w for w in warnings))
        self.assertTrue(any("gate 'static'" in w for w in warnings))
        self.assertEqual(setup.check_task(profile, "verified",
                                          {"repository": "code", "gates": ["unit"]}), [])


class SetupCatalogTests(unittest.TestCase):
    def test_catalog_and_templates(self):
        skill = skills.get_skill("setup")
        self.assertIn("SKILL.md", skill["docs"])
        rendered = skills.render_template("setup", "setup_summary.md")
        self.assertIn("ARK Environment Setup", rendered["text"])
        profile = json.loads(skills.render_template("setup", "environment.json")["text"])
        self.assertEqual(profile["format"], "ark-environment")
        result = skills.validate_payload("setup", "environment", setup.example_profile())
        self.assertTrue(result["ok"], result["errors"])

    def test_interface_declares_bootstrap(self):
        descriptor = skills.interface.describe(skill_ids=["setup"])
        self.assertEqual(descriptor["bootstrap"]["first_call"], "GET /api/agent/v1/setup")
        ids = [cap["id"] for cap in descriptor["capabilities"]]
        self.assertIn("setup.discover", ids)
        self.assertIn("setup.read", descriptor["conformance"]["L0"]["requires"])


class SetupApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        cls.repo = _git_repo(Path(cls.tmp.name) / "code")
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=False)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"
        cls.auth = {"Authorization": f"Bearer {cls.app.manager.agent_token}"}

    @classmethod
    def tearDownClass(cls):
        cls.httpd.shutdown()
        cls.httpd.server_close()
        cls.app.project.close()
        cls.tmp.cleanup()
        if cls._old_chat_mode is None:
            os.environ.pop("ARK_CHAT_MODE", None)
        else:
            os.environ["ARK_CHAT_MODE"] = cls._old_chat_mode

    def request(self, path, *, method="GET", body=None, headers=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def test_agent_flow_and_human_confirmation(self):
        status, _ = self.request("/api/agent/v1/setup")
        self.assertEqual(status, 401)
        status, state = self.request("/api/agent/v1/setup", headers=self.auth)
        self.assertEqual(status, 200)
        self.assertIn(state["state"], {"unset", "draft", "proposed"})

        status, found = self.request("/api/agent/v1/setup/discover", method="POST",
                                     body={"paths": [str(self.repo)]}, headers=self.auth)
        self.assertEqual(status, 200, found)
        profile = found["draft"]
        profile.update({"objective": "test objective",
                        "policies": {key: "ask" for key in setup.POLICY_DEFAULTS}})
        status, draft = self.request("/api/agent/v1/setup/draft", method="POST",
                                     body={"profile": profile, "propose": True},
                                     headers=self.auth)
        self.assertEqual(status, 200, draft)
        self.assertEqual(draft["state"], "proposed")

        # An agent cannot confirm, with a token or by naming itself.
        status, denied = self.request("/api/agent/v1/setup/confirm", method="POST",
                                      body={"digest": draft["digest"]}, headers=self.auth)
        self.assertEqual(status, 403, denied)
        status, denied = self.request("/api/skills/setup/confirm", method="POST",
                                      body={"digest": draft["digest"]},
                                      headers={"X-Agent-Name": "dsh"})
        self.assertEqual(status, 403, denied)

        status, confirmed = self.request("/api/skills/setup/confirm", method="POST",
                                         body={"digest": draft["digest"]})
        self.assertEqual(status, 200, confirmed)
        self.assertEqual(confirmed["state"], "confirmed")

        status, verified = self.request("/api/agent/v1/setup/verify", method="POST",
                                        body={}, headers=self.auth)
        self.assertEqual(status, 200, verified)
        self.assertEqual(verified["state"], "verified", verified["verify"]["checks"])

        status, ui = self.request("/api/skills/setup/state")
        self.assertEqual(status, 200)
        self.assertEqual(ui["state"], "verified")

    def test_task_creation_carries_setup_warnings(self):
        from arkstudio.skills import agent_team
        task = agent_team.example_task({"id": "p", "name": "P"})
        task.pop("task_id")
        task.pop("worktree")
        task.update({"repository": "not-registered"})
        status, data = self.request("/api/skills/agent-team/validate", method="POST",
                                    body={"kind": "task", "payload": task})
        self.assertEqual(status, 200)
        self.assertTrue(data.get("setup_warnings"))


if __name__ == "__main__":
    unittest.main()
