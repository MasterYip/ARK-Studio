"""Focused tests for the ARK skills/protocol layer.

Covers catalog discovery, template rendering, protocol validation, the adapted
RRSI decision core, the design theme/artifact checks, ARK task creation and the
HTTP endpoints used by the Settings panel and coding agents.
"""
import json
import os
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from arkstudio import skills
from arkstudio.skills import agent_team, design, protocol, rrsi
from arkstudio.server import App, serve


class ProtocolTests(unittest.TestCase):
    def test_gate_order_and_unknown_gate(self):
        self.assertEqual(protocol.ordered_gates(["unit", "scope", "static"]),
                         ["scope", "static", "unit"])
        with self.assertRaises(protocol.ProtocolError):
            protocol.ordered_gates(["scope", "nonsense"])

    def test_ark_status_projection(self):
        self.assertEqual(protocol.ark_status("review", "waiting"), "open")
        self.assertEqual(protocol.ark_status("active", "running"), "active")
        self.assertEqual(protocol.ark_status("accepted", "completed"), "done")
        # Agent review stays active; ARK `review` is reserved for the human user.
        self.assertEqual(protocol.ark_status("review"), "active")
        self.assertEqual(protocol.ark_status("evaluation"), "active")

    def test_agent_review_phase_is_tagged_not_review_status(self):
        task = agent_team.example_task()
        task["phase"], task["status"] = "review", "running"
        unit = agent_team.prepare_task(task)["unit"]
        self.assertEqual(unit["status"], "active")
        self.assertIn("phase:review", unit["tags"])
        task["phase"] = "active"
        self.assertNotIn("phase:review", agent_team.prepare_task(task)["unit"]["tags"])
        with self.assertRaises(protocol.ProtocolError):
            protocol.ark_status("bogus")

    def test_project_global_task_serials(self):
        existing = ["T-0001", "FIG-0002", "WEB-0015"]
        self.assertEqual(protocol.next_task_id(existing, prefix="DOC"), "DOC-0016")
        self.assertEqual(protocol.next_task_id([], prefix="T"), "T-0001")
        self.assertEqual(protocol.next_task_id(["FIG-001"], prefix="WEB"), "WEB-0002")
        with self.assertRaises(protocol.ProtocolError):
            protocol.check_unique_task_id("DOC-0015", existing)
        with self.assertRaises(protocol.ProtocolError):
            protocol.check_unique_task_id("T-0001", existing)
        with self.assertRaises(protocol.ProtocolError):
            protocol.parse_task_id("task-1")

    def test_protocol_summary_shape(self):
        summary = protocol.protocol_summary()
        self.assertEqual(summary["phases"][0], "proposed")
        self.assertIn("scope", [gate["id"] for gate in summary["gates"]])
        self.assertTrue(summary["evidence_policy"]["retain_in_git"])


class CatalogTests(unittest.TestCase):
    def test_catalog_lists_adapted_skills(self):
        ids = [skill["id"] for skill in skills.list_skills()]
        self.assertEqual(ids, ["setup", "agent-team", "rrsi", "design"])
        for skill in skills.list_skills():
            self.assertTrue(skill["source"]["license"])
            self.assertTrue(skill["source"]["adapted"])
            self.assertTrue(skill["agent_prompt"])

    def test_skill_detail_includes_docs_and_templates(self):
        skill = skills.get_skill("agent-team")
        self.assertIn("SKILL.md", skill["docs"])
        self.assertIn("goal_manifest", skill["docs"]["SKILL.md"].lower())
        self.assertIn("agent_team.prepare_task", skill["python_api"])
        self.assertEqual([tpl["id"] for tpl in skill["templates"]],
                         ["goal_manifest.md", "inspection_report.md", "agent_brief.md"])

    def test_unknown_skill_raises(self):
        with self.assertRaises(skills.SkillError):
            skills.get_skill("not-a-skill")
        with self.assertRaises(skills.SkillError):
            skills.render_template("agent-team", "nope.md")

    def test_catalog_payload_has_protocol(self):
        payload = skills.catalog_payload()
        self.assertEqual(payload["protocol_version"], protocol.PROTOCOL_VERSION)
        self.assertEqual(payload["attribution"], "docs/agents/skills-attribution.md")


class TemplateRenderingTests(unittest.TestCase):
    def test_goal_manifest_renders_lists_and_fields(self):
        data = skills.render_template("agent-team", "goal_manifest.md", {
            "task_id": "T-0042",
            "title": "Rendered task",
            "deliverables": ["one", "two"],
            "gates": ["scope", "unit"],
            "acceptance": [{"command": "python -V", "threshold": "runs"}],
        })
        text = data["text"]
        self.assertIn("T-0042", text)
        self.assertIn("- one", text)
        self.assertIn("- two", text)
        self.assertIn("scope", text)
        self.assertIn("python -V", text)
        self.assertIn("Reviewer independence", text)

    def test_inspection_report_requires_review_decision(self):
        data = skills.render_template("agent-team", "inspection_report.md", {
            "task_id": "T-0042", "results": "all green",
            "commands": ["python -m unittest"],
        })
        self.assertIn("Independent reviewer decision", data["text"])
        self.assertIn("python -m unittest", data["text"])

    def test_rrsi_briefs_render(self):
        for name in ("proposer.md", "analyst.md", "critic.md"):
            data = skills.render_template("rrsi", name, {"task_title": "Tighten retrieval"})
            self.assertIn("Tighten retrieval", data["text"])
            self.assertIn("component", data["text"].lower())

    def test_design_theme_template_is_valid_json(self):
        data = skills.render_template("design", "theme.json")
        theme = json.loads(data["text"])
        result = design.validate_theme(theme)
        self.assertTrue(result["ok"], result["errors"])
        self.assertEqual(theme["meta"]["name"], "academic-clean")

    def test_design_brief_and_review_render(self):
        brief = skills.render_template("design", "design_brief.md", {
            "project_name": "Project X", "artifact": "assets/figure.svg"})
        self.assertIn("Project X", brief["text"])
        self.assertIn("assets/figure.svg", brief["text"])
        review = skills.render_template("design", "style_review.md", {
            "artifact": "assets/figure.svg"})
        self.assertIn("Shared gates", review["text"])

    def test_rrsi_domain_template_and_validation(self):
        data = skills.render_template("rrsi", "domain.json")
        domain = json.loads(data["text"])
        self.assertEqual(domain["name"], "ark-coding")
        result = rrsi.validate_domain(domain)
        self.assertTrue(result["ok"], result.get("errors"))
        bad = dict(domain)
        bad["evolve"] = ["T-0001"]
        bad["heldout"] = ["T-0001"]
        with self.assertRaises(rrsi.RrsiError):
            rrsi.validate_domain(bad)


class AgentTeamTests(unittest.TestCase):
    def test_example_task_prepares_manifest_and_unit(self):
        existing = ["T-0001", "T-0002"]
        preview = agent_team.prepare_task(agent_team.example_task(), existing_ids=existing)
        self.assertTrue(preview["ok"], preview["errors"])
        self.assertEqual(preview["manifest"]["task_id"], "T-0100")
        self.assertEqual(preview["manifest"]["worktree"], ".worktrees/T-0100")
        self.assertIn("skills:agent-team", preview["unit"]["tags"])
        self.assertIn("## Acceptance", preview["markdown"])
        self.assertEqual(preview["unit"]["type"], "task")

    def test_auto_allocates_next_global_serial(self):
        task = agent_team.example_task()
        task.pop("task_id")
        task.pop("worktree")
        preview = agent_team.prepare_task(task, existing_ids=["T-0004", "FIG-0007"])
        self.assertTrue(preview["ok"], preview["errors"])
        self.assertEqual(preview["manifest"]["task_id"], "T-0008")
        self.assertEqual(preview["manifest"]["worktree"], ".worktrees/T-0008")

    def test_reviewer_must_be_independent(self):
        task = agent_team.example_task()
        task["reviewer"] = task["owner"]
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertTrue(any("reviewer" in error for error in preview["errors"]))

        task["acceptance_exception"] = "solo maintainer pilot"
        preview = agent_team.prepare_task(task)
        self.assertTrue(preview["ok"], preview["errors"])

    def test_rejects_absolute_or_sibling_worktree(self):
        task = agent_team.example_task()
        task["worktree"] = "../T-0100-worktrees"
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertTrue(any("worktree" in error for error in preview["errors"]))

    def test_requires_acceptance_and_owned_paths(self):
        task = agent_team.example_task()
        task["acceptance"] = []
        task["owned_paths"] = []
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        joined = " ".join(preview["errors"])
        self.assertIn("acceptance", joined)
        self.assertIn("owned_paths", joined)

    def test_missing_non_goals_is_a_warning(self):
        task = agent_team.example_task()
        task["non_goals"] = []
        preview = agent_team.prepare_task(task)
        self.assertTrue(preview["ok"], preview["errors"])
        self.assertTrue(any("non_goals" in warning for warning in preview["warnings"]))

    def test_manifest_can_be_revalidated_by_a_reviewer(self):
        preview = agent_team.prepare_task(agent_team.example_task())
        self.assertTrue(preview["ok"], preview["errors"])
        again = agent_team.validate_manifest(preview["manifest"])
        self.assertTrue(again["ok"], again["errors"])
        self.assertEqual(again["manifest"]["task_id"], preview["manifest"]["task_id"])

    def test_create_task_writes_manifest_into_ark_unit(self):
        tmp = tempfile.TemporaryDirectory()
        try:
            from arkstudio.store import Store
            store = Store(Path(tmp.name))
            task = agent_team.example_task({"id": "prj_test", "name": "Test project"})
            task.pop("task_id")
            task.pop("worktree")
            task["create"] = True
            preview = agent_team.create_task(store, task, actor="agent:test")
            self.assertTrue(preview["created"])
            unit = preview["ark_unit"]
            self.assertEqual(unit["uid"], preview["manifest"]["task_id"])
            self.assertEqual(unit["uid"], "T-0001")
            self.assertEqual(unit["type"], "task")
            self.assertIn("Goal Manifest", unit["body"])
            self.assertIn("skills:agent-team", unit["tags"])
            stored = store.list_units({"types": ["task"]})["items"][0]
            self.assertEqual(stored["uid"], "T-0001")
            store.close()
        finally:
            tmp.cleanup()

    def test_create_false_is_read_only(self):
        tmp = tempfile.TemporaryDirectory()
        try:
            from arkstudio.store import Store
            store = Store(Path(tmp.name))
            task = agent_team.example_task({"id": "prj_test", "name": "Test"})
            preview = agent_team.create_task(store, task)
            self.assertFalse(preview["created"])
            self.assertEqual(store.list_units({"types": ["task"]})["items"], [])
            store.close()
        finally:
            tmp.cleanup()


class InterfaceTests(unittest.TestCase):
    def test_describe_exposes_v2_contract(self):
        spec = skills.interface.describe(skill_ids=["agent-team"])
        self.assertEqual(spec["interface_version"], "2.0")
        self.assertIn("router-workers", spec["topologies"])
        self.assertIn("peer-team", spec["topologies"])
        ids = {item["id"] for item in spec["capabilities"]}
        self.assertIn("brief.validate", ids)
        self.assertIn("media.read", ids)
        self.assertIn("export.read", ids)
        self.assertIn("/api/agent/v1/media/{id}/raw", spec["auth"]["scopes"]["read"])
        self.assertIn("/api/agent/v1/export", spec["auth"]["scopes"]["read"])
        self.assertEqual(spec["skills"], ["agent-team"])
        for key in ("task_structure", "team", "context", "model_policy",
                    "verification", "failure_taxonomy", "handoff"):
            self.assertIn(key, spec["manifest_v2"]["new_in_v2"])

    def test_manifest_v2_defaults(self):
        preview = agent_team.prepare_task(agent_team.example_task())
        self.assertTrue(preview["ok"], preview["errors"])
        manifest = preview["manifest"]
        self.assertEqual(manifest["interface_version"], "2.0")
        self.assertEqual(manifest["team"]["mode"], "router-workers")
        self.assertEqual(manifest["context"]["budget_tokens"], 120000)
        self.assertTrue(manifest["verification"]["independent"])
        self.assertIn("topology:router-workers", preview["unit"]["tags"])
        self.assertIn("Execution shape", preview["markdown"])
        self.assertIn("Context plan", preview["markdown"])
        self.assertIn("Failure taxonomy", preview["markdown"])

    def test_team_rules(self):
        task = agent_team.example_task()
        task["team"] = {"mode": "peer-team", "size": 3}
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertIn("heterogeneous", " ".join(preview["errors"]))

        task["team"] = {"mode": "solo", "size": 3}
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertIn("solo", " ".join(preview["errors"]))

        task["team"] = {"mode": "router-workers", "size": 6, "rationale": "too wide"}
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertIn("heterogeneous", " ".join(preview["errors"]))

    def test_context_and_verification_rules(self):
        task = agent_team.example_task()
        task["context"] = {"budget_tokens": 100, "artifacts": ["../escape"]}
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        joined = " ".join(preview["errors"])
        self.assertIn("budget_tokens", joined)
        self.assertIn("project-relative", joined)

        task = agent_team.example_task()
        task["verification"] = {"independent": False}
        preview = agent_team.prepare_task(task)
        self.assertFalse(preview["ok"])
        self.assertIn("independent", " ".join(preview["errors"]))

    def test_brief_validation(self):
        good = {"objective": "extract counterexamples", "owned_paths": ["a/**"],
                "acceptance": ["python -V"], "stop_conditions": ["stop"]}
        result = skills.interface.validate_brief(good)
        self.assertTrue(result["ok"], result["errors"])
        self.assertEqual(result["brief"]["team"]["mode"], "solo")

        bad = dict(good)
        bad["team"] = {"mode": "peer-team", "size": 3}
        result = skills.interface.validate_brief(bad)
        self.assertFalse(result["ok"])
        self.assertTrue(any("heterogeneous" in error for error in result["errors"]))

    def test_brief_template_renders(self):
        data = skills.render_template("agent-team", "agent_brief.md",
                                      {"objective": "extract counterexamples"})
        self.assertIn("Agent Brief", data["text"])
        self.assertIn("extract counterexamples", data["text"])
        self.assertIn("Handoff", data["text"])


class RrsiTests(unittest.TestCase):
    def test_edit_budget_anneals(self):
        table = rrsi.budget_table(10, b_min=1, b_max=3)
        self.assertEqual(table[0], 3)
        self.assertLessEqual(table[-1], 2)
        self.assertEqual(table, sorted(table, reverse=True))
        self.assertEqual(rrsi.edit_budget(10, 10, b_min=1, b_max=3), 1)

    def test_component_recovery_from_diff(self):
        prompt_diff = '--- a/p.py\n+++ b/p.py\n-OLD = "be brief"\n+NEW = "cite the unit"'
        self.assertEqual(rrsi.classify_diff(prompt_diff), "prompt")
        skill_diff = "+++ b/arkstudio/skills/design/SKILL.md\n+new instructions"
        self.assertEqual(rrsi.classify_diff(skill_diff), "skill")
        declared = rrsi.normalize_component("memory", prompt_diff)
        self.assertEqual(declared, "prompt")
        evidenced = rrsi.normalize_component("skill", skill_diff)
        self.assertEqual(evidenced, "skill")

    def test_novelty_counts_only_structural(self):
        self.assertEqual(rrsi.novelty(["skill", "prompt"], {"skill": 0, "prompt": 3}), 1)
        self.assertEqual(rrsi.novelty(["skill", "memory"], {"skill": 1, "memory": 1}), 0)
        self.assertEqual(rrsi.novelty(["prompt", "config"], {}), 0)

    def test_floor_and_cost_rule(self):
        config = rrsi.SearchConfig()
        incumbent = rrsi.Candidate("inc", [{"component": "prompt", "hypothesis": "h"}],
                                   score=0.62, cost=120000)
        passed = rrsi.judge(rrsi.Candidate("a", [{"component": "prompt", "hypothesis": "h"}],
                                          score=0.668, cost=122000),
                            incumbent_score=0.62, incumbent_cost=120000, s_star=0.662,
                            accepted_counts={}, config=config)
        self.assertTrue(passed.admissible, passed.reason)
        rejected_floor = rrsi.judge(
            rrsi.Candidate("b", [{"component": "prompt", "hypothesis": "h"}],
                           score=0.640, cost=110000),
            incumbent_score=0.62, incumbent_cost=120000, s_star=0.662,
            accepted_counts={}, config=config)
        self.assertFalse(rejected_floor.admissible)
        self.assertIn("floor", rejected_floor.reason)
        rejected_cost = rrsi.judge(
            rrsi.Candidate("c", [{"component": "prompt", "hypothesis": "h"}],
                           score=0.700, cost=200000),
            incumbent_score=0.62, incumbent_cost=120000, s_star=0.662,
            accepted_counts={}, config=config)
        self.assertFalse(rejected_cost.admissible)
        self.assertIn("cost rule", rejected_cost.reason)

    def test_round_winner_and_gate_failure(self):
        result = rrsi.validate_round(rrsi.example_round())
        self.assertEqual(result["winner"], "candidate-a")
        self.assertEqual(len(result["decisions"]), 2)

    def test_edit_budget_enforced_per_candidate(self):
        payload = rrsi.example_round()
        payload["config"]["b_min"] = 1
        payload["config"]["b_max"] = 1  # every round allows exactly one edit
        payload["candidates"][0]["edits"].append(
            {"component": "config", "hypothesis": "second independent edit"})
        with self.assertRaises(rrsi.RrsiError):
            rrsi.validate_round(payload)

    def test_history_stall_exploration_prune(self):
        history = rrsi.History()
        for index in range(3):
            history.add({
                "round": index, "component": "prompt", "hypothesis": "no effect",
                "verdict": "rejected", "delta_score": 0.0, "delta_cost": 0.0,
            })
        self.assertTrue(history.stall_flag(0.017, 3))
        self.assertIn("skill", history.untried_components())
        exploration = history.exploration(rrsi.SearchConfig())
        self.assertTrue(exploration["stalled"])
        self.assertIn("prompt", history.prune_set(3))
        self.assertEqual(history.accepted_counts()["prompt"], 0)

    def test_history_jsonl_roundtrip(self):
        tmp = tempfile.TemporaryDirectory()
        try:
            path = Path(tmp.name) / "history.jsonl"
            history = rrsi.History()
            record = rrsi.EditRecord(round=0, component="skill", hypothesis="load on demand",
                                     verdict="accepted", delta_score=0.03, delta_cost=0.01,
                                     variant="a", commit="abc123")
            history.append_jsonl(path, record)
            loaded = rrsi.History.load_jsonl(path)
            self.assertEqual(len(loaded.records), 1)
            self.assertEqual(loaded.records[0]["component"], "skill")
            self.assertTrue(path.read_text(encoding="utf-8").endswith("\n"))
        finally:
            tmp.cleanup()

    def test_leakage_scan(self):
        findings = rrsi.scan_leakage(
            "read tests/test_secret.py and expected_output to answer T-0019")
        patterns = {item["pattern"] for item in findings}
        self.assertGreaterEqual(len(findings), 2)
        self.assertIn(r"\bT-\d{3,}\b", patterns)
        self.assertEqual(rrsi.scan_leakage("inject the current unit into the prompt"), [])


class DesignTests(unittest.TestCase):
    def test_example_theme_validates(self):
        result = design.validate_theme(design.example_theme())
        self.assertTrue(result["ok"], result["errors"])
        self.assertEqual(result["quality_gates"], design.QUALITY_GATES)

    def test_missing_semantic_role_is_rejected(self):
        theme = design.example_theme()
        del theme["color"]["roles"]["data_primary"]
        result = design.validate_theme(theme)
        self.assertFalse(result["ok"])
        self.assertTrue(any("data_primary" in error for error in result["errors"]))

    def test_low_contrast_text_is_rejected(self):
        theme = design.example_theme()
        theme["color"]["roles"]["text_primary"]["value"] = "#EDEDED"
        result = design.validate_theme(theme)
        self.assertFalse(result["ok"])
        self.assertTrue(any("contrast" in error for error in result["errors"]))

    def test_variant_resolution_changes_surface(self):
        base = design.validate_theme(design.example_theme())
        variant = design.validate_theme(design.example_theme(), variant="web")
        self.assertTrue(variant["ok"], variant["errors"])
        self.assertNotEqual(
            base["theme"]["color"]["roles"]["surface"]["value"],
            variant["theme"]["color"]["roles"]["surface"]["value"])
        self.assertTrue(variant["theme"]["variants"])

    def test_unknown_shape_color_role_is_rejected(self):
        theme = design.example_theme()
        theme["shape"]["vocabulary"]["panel"]["fill"] = "blue_box"
        result = design.validate_theme(theme)
        self.assertFalse(result["ok"])
        self.assertTrue(any("blue_box" in error for error in result["errors"]))

    def test_verify_svg_artifact(self):
        tmp = tempfile.TemporaryDirectory()
        try:
            root = Path(tmp.name)
            (root / "assets").mkdir()
            good = root / "assets" / "figure.svg"
            good.write_text(
                '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">'
                '<rect width="10" height="10" fill="#1F5A7A"/></svg>', encoding="utf-8")
            result = design.verify_artifact("assets/figure.svg", project_root=root)
            self.assertTrue(result["ok"], result["errors"])
            self.assertTrue(any(check["id"] == "svg_xml" for check in result["checks"]))

            bad = root / "assets" / "external.svg"
            bad.write_text('<svg xmlns="http://www.w3.org/2000/svg">'
                           '<image href="https://example.com/x.png"/></svg>', encoding="utf-8")
            result = design.verify_artifact("assets/external.svg", project_root=root)
            self.assertFalse(result["ok"])
            self.assertTrue(any("external" in error for error in result["errors"]))

            outside = design.verify_artifact("../outside.svg", project_root=root)
            self.assertFalse(outside["ok"])
        finally:
            tmp.cleanup()

    def test_asset_rules(self):
        self.assertTrue(design.validate_asset_path("assets/figure.svg")["ok"])
        for bad in ("https://example.com/a.png", "assets/../secret.svg",
                    "/tmp/a.svg", "assets/a.exe", "docs/a.svg"):
            result = design.validate_asset_path(bad)
            self.assertFalse(result["ok"], bad)


class SkillsApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._old_chat_mode = os.environ.get("ARK_CHAT_MODE")
        os.environ["ARK_CHAT_MODE"] = "local"
        cls.tmp = tempfile.TemporaryDirectory()
        cls.app = App(Path(cls.tmp.name) / "projects", seed_demo=False)
        cls.httpd = serve(cls.app, port=0, block=False)
        cls.base = f"http://127.0.0.1:{cls.httpd.server_address[1]}"
        cls.token = cls.app.manager.agent_token

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
            with urllib.request.urlopen(req, timeout=10) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode() or "{}")

    def test_catalog_and_detail(self):
        status, data = self.request("/api/skills")
        self.assertEqual(status, 200)
        self.assertEqual(len(data["skills"]), 4)
        self.assertIn("protocol", data)
        status, detail = self.request("/api/skills/rrsi")
        self.assertEqual(status, 200)
        self.assertIn("SKILL.md", detail["skill"]["docs"])
        status, prompt = self.request("/api/skills/design/prompt")
        self.assertEqual(status, 200)
        self.assertIn("theme", prompt["prompt"])

    def test_render_and_validate(self):
        status, rendered = self.request("/api/skills/agent-team/render", method="POST",
                                        body={"template": "goal_manifest.md"})
        self.assertEqual(status, 200)
        self.assertIn("Goal Manifest", rendered["text"])
        status, rendered = self.request("/api/skills/design/render", method="POST",
                                        body={"template": "theme.json"})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(rendered["text"])["meta"]["name"], "academic-clean")

        status, validated = self.request("/api/skills/design/validate", method="POST", body={
            "kind": "theme", "payload": design.example_theme()})
        self.assertEqual(status, 200)
        self.assertTrue(validated["ok"], validated.get("errors"))
        status, broken = self.request("/api/skills/design/validate", method="POST", body={
            "kind": "theme", "payload": {"meta": {}}})
        self.assertEqual(status, 200)
        self.assertFalse(broken["ok"])

    def test_interface_endpoint(self):
        status, data = self.request("/api/skills/interface")
        self.assertEqual(status, 200)
        self.assertEqual(data["interface_version"], "2.0")
        self.assertIn("topologies", data)

        status, _ = self.request("/api/agent/v1/interface")
        self.assertEqual(status, 401)
        status, data = self.request(
            "/api/agent/v1/interface",
            headers={"Authorization": f"Bearer {self.token}"})
        self.assertEqual(status, 200)
        self.assertEqual(data["interface_version"], "2.0")

    def test_brief_endpoint(self):
        status, data = self.request(
            "/api/skills/agent-team/brief", method="POST",
            body={"objective": "extract counterexamples",
                  "owned_paths": ["a/**"], "acceptance": ["python -V"],
                  "stop_conditions": ["stop"]})
        self.assertEqual(status, 200)
        self.assertTrue(data["ok"], data.get("errors"))

    def test_rrsi_round_endpoint(self):
        status, data = self.request("/api/skills/rrsi/round", method="POST",
                                    body=rrsi.example_round())
        self.assertEqual(status, 200)
        self.assertEqual(data["result"]["winner"], "candidate-a")

    def test_create_task_from_agent_team(self):
        task = agent_team.example_task({"id": "prj_api", "name": "API project"})
        task.pop("task_id")
        task.pop("worktree")
        task.update({"create": True, "title": "API task"})
        status, data = self.request("/api/skills/agent-team/tasks", method="POST", body=task)
        self.assertEqual(status, 200, data)
        self.assertTrue(data["created"])
        unit = data["ark_unit"]
        self.assertEqual(unit["type"], "task")
        self.assertEqual(unit["uid"], data["manifest"]["task_id"])
        self.assertIn("Goal Manifest", unit["body"])
        status, fetched = self.request(f"/api/units/{unit['uid']}")
        self.assertEqual(status, 200)
        self.assertIn("skills:agent-team", fetched["unit"]["tags"])

    def test_agent_skills_require_token(self):
        status, _ = self.request("/api/agent/v1/skills")
        self.assertEqual(status, 401)
        status, data = self.request("/api/agent/v1/skills",
                                    headers={"Authorization": f"Bearer {self.token}"})
        self.assertEqual(status, 200)
        self.assertEqual(len(data["skills"]), 4)

    def test_design_verify_endpoint(self):
        project = self.app.require()
        assets = project.path / "assets"
        assets.mkdir(exist_ok=True)
        (assets / "figure.svg").write_text(
            '<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>',
            encoding="utf-8")
        status, data = self.request("/api/skills/design/verify", method="POST", body={
            "path": "assets/figure.svg", "kind": "svg"})
        self.assertEqual(status, 200)
        self.assertTrue(data["ok"], data["errors"])


if __name__ == "__main__":
    unittest.main()
