"""Replay steps for POST /api/skills/<skill>/<action> (Handler._skills_post).

Actions: render (every template of agent-team, rrsi and design, context
merges, a bad gate, a TypeError, a non-object context, unknown skill and
template, a missing template), validate (each skill's kinds, the default
kind per skill, ``payload`` wrapping or the body minus ``kind``, setup
warnings on agent-team tasks), tasks (preview and ``create`` through the
store: a free id, a taken serial, an explicit id that differs from the new
unit's UID, validation failures; also with personal agent tokens of write
and read scope), brief, round, history, leakage (default and caller
patterns, a bad pattern), verify and asset (files written into both
project folders by a CALL step: SVG, HTML, CSS, PNG and broken ones) and
the setup lifecycle reached through /api/skills/setup/<action>.
"""
from __future__ import annotations

import json
from pathlib import Path

import replay_services

MODULE = "skills-post"
CALL = replay_services.CALL
ROUTE = "prefix:/api/skills/"

ARTIFACTS = {
    "assets/fig.svg": '<?xml version="1.0"?>\n<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>\n',
    "assets/ext.svg": '<svg xmlns="http://www.w3.org/2000/svg"><image href="https://cdn/x.png"/></svg>',
    "assets/broken.svg": "<svg>\n<g>\n</svg>",
    "assets/page.html": '<meta name="viewport" content="w"><link href="http://a/b.css">',
    "assets/theme.css": ":root { --ink: #111; }",
    "assets/img.png": b"\x89PNG\r\n\x1a\n....",
    "assets/fake.png": b"GIF89a",
    "assets/empty.pdf": b"",
}


def _write_artifacts(py_dir, go_dir) -> None:
    for data in (py_dir, go_dir):
        projects = Path(data) / "projects"
        for folder in projects.iterdir():
            if not (folder / "project.json").is_file():
                continue
            for rel, body in ARTIFACTS.items():
                target = folder / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(body if isinstance(body, bytes) else body.encode("utf-8"))


TASK = {
    "title": "Replay task ✓", "objective": "Check the skills API", "owner": "agent:codex",
    "reviewer": "human:ana", "repository": "ARK-Studio", "base_revision": "main@abc1234",
    "deliverables": ["report"], "owned_paths": ["docs/**"], "acceptance": [{"command": "pytest -q"}],
    "gates": ["unit", "scope"], "non_goals": ["UI"], "stop_conditions": ["red"],
}
ROUND = {"round": 0, "incumbent": {"score": 0.62, "cost": 120000}, "s_star": 0.662,
         "candidates": [{"variant": "a", "score": 0.668, "cost": 122000,
                         "edits": [{"component": "prompt", "hypothesis": "h"}]},
                        {"variant": "b", "score": 0.64, "cost": 110000,
                         "edits": [{"component": "skill", "hypothesis": "h"}]}]}


def skills_steps(fixture: dict) -> list[tuple]:
    h = {"X-Replay-Module": MODULE}
    tokens = fixture["tokens"]
    users = fixture.get("users") or []
    project = {"X-ARK-Project": fixture["project"]} if users else {}
    nocookie = {**h, "X-Replay-NoCookie": "1"}
    pw = {**nocookie, "Authorization": "Bearer " + tokens["personal_write"], **project}
    pr = {**nocookie, "Authorization": "Bearer " + tokens["personal_read"], **project}
    steps: list[tuple] = [(CALL, "", "", _write_artifacts, dict(h))]

    def post(path, body, headers=h):
        steps.append((ROUTE, "POST", path, body, dict(headers)))

    # ---- render
    for skill, templates in (("agent-team", ("goal_manifest.md", "inspection_report.md", "agent_brief.md")),
                             ("rrsi", ("proposer.md", "analyst.md", "critic.md", "domain.json")),
                             ("design", ("theme.json", "design_brief.md", "style_review.md")),
                             ("setup", ("environment.json", "setup_summary.md"))):
        for template in templates:
            post(f"/api/skills/{skill}/render", {"template": template})
    post("/api/skills/Agent-Team/render", {"template": " goal_manifest.md ",
                                           "context": {"task_id": "WEB-0042", "gates": ["UNIT"], "budget": None}})
    post("/api/skills/agent-team/render", {"template": "goal_manifest.md", "context": {"gates": ["deploy"]}})
    post("/api/skills/agent-team/render", {"template": "goal_manifest.md", "context": {"deliverables": 5}})
    post("/api/skills/rrsi/render", {"template": "domain.json", "context": {"name": "mine"}})
    post("/api/skills/rrsi/render", {"template": "critic.md", "context": {"diff": None, "round_index": 3}})
    post("/api/skills/design/render", {"template": "theme.json", "context": {"meta": {"name": "x"}}})
    post("/api/skills/design/render", {"template": "design_brief.md", "context": [1]})
    post("/api/skills/design/render", {"template": "design_brief.md", "context": []})
    post("/api/skills/design/render", {})
    post("/api/skills/design/render", {"template": "   "})
    post("/api/skills/nope/render", {"template": "x.md"})
    post("/api/skills/design/render", {"template": "x.md"})
    # ---- validate (default kinds, payload wrapping, setup warnings)
    post("/api/skills/agent-team/validate", dict(TASK))
    post("/api/skills/agent-team/validate", {"kind": "Task", "payload": dict(TASK, task_id="T-0001")})
    post("/api/skills/agent-team/validate", {"kind": "request", "payload": dict(TASK, repository="unknown-repo")})
    post("/api/skills/agent-team/validate", {"kind": "manifest", "payload": dict(TASK, task_id="T-0500")})
    post("/api/skills/agent-team/validate", {"kind": "brief", "payload": {"objective": "x"}})
    post("/api/skills/agent-team/validate", {"kind": "task", "payload": dict(TASK, existing_ids=5)})
    post("/api/skills/agent-team/validate", {"kind": "nope"})
    post("/api/skills/rrsi/validate", dict(ROUND))
    post("/api/skills/rrsi/validate", {"kind": "history", "records": [
        {"component": "prompt", "hypothesis": "a", "verdict": "accepted", "delta_score": 0.03}]})
    post("/api/skills/rrsi/validate", {"kind": "edit", "component": "skill", "diff": "+ skills/x"})
    post("/api/skills/rrsi/validate", {"kind": "leak", "text": "tests/x.py"})
    post("/api/skills/rrsi/validate", {"kind": "domain", "payload": {"name": "d", "harness_path": "h", "evolve": ["a"]}})
    post("/api/skills/rrsi/validate", {"incumbent": [1]})
    post("/api/skills/design/validate", {})
    post("/api/skills/design/validate", {"kind": "asset", "path": "assets/fig.svg"})
    post("/api/skills/design/validate", {"kind": "asset", "path": "../../escape.png"})
    post("/api/skills/design/validate", {"kind": "artifact", "path": "assets/fig.svg"})
    post("/api/skills/design/validate", {"kind": "artifact", "path": 5})
    post("/api/skills/setup/validate", {"repositories": [{"id": "x", "path": ""}]})
    post("/api/skills/setup/validate", {"kind": "other"})
    post("/api/skills/nope/validate", {})
    # ---- tasks: preview, create, conflicts, tokens
    post("/api/skills/agent-team/tasks", dict(TASK))
    post("/api/skills/agent-team/tasks", dict(TASK, create=True))
    post("/api/skills/agent-team/tasks", dict(TASK, create=True, assignee=users[0] if users else "local",
                                              title="Second é", gates=["static"]))
    post("/api/skills/agent-team/tasks", dict(TASK, create=True, task_id="WEB-9000"))
    post("/api/skills/agent-team/tasks", dict(TASK, create=True, task_id="T-0001"))
    post("/api/skills/agent-team/tasks", dict(TASK, owner=""))
    post("/api/skills/agent-team/tasks", dict(TASK, create=True, phase="review"), pw)
    post("/api/skills/agent-team/tasks", dict(TASK, create=True), pr)
    post("/api/skills/agent-team/tasks", dict(TASK, existing_ids=["T-9999"]))
    post("/api/skills/agent-team/tasks", {"create": True, "team": {"size": 1e400}})
    # ---- direct actions
    post("/api/skills/agent-team/brief", {"objective": "Fix", "owned_paths": ["a"], "acceptance": "x",
                                          "stop_conditions": "y"})
    post("/api/skills/rrsi/round", dict(ROUND))
    post("/api/skills/rrsi/round", {"incumbent": {"score": "x"}})
    post("/api/skills/rrsi/history", {"records": [{"component": "memory", "hypothesis": "h", "verdict": "neutral"}],
                                      "config": {"T": 3}})
    post("/api/skills/rrsi/history", {"records": 5})
    post("/api/skills/rrsi/leakage", {"diff": "+ assertEqual(x)\nT-0123"})
    post("/api/skills/rrsi/leakage", {"text": "aXb", "patterns": ["X", "b$"]})
    post("/api/skills/rrsi/leakage", {"text": "a", "patterns": ["("]})
    post("/api/skills/rrsi/leakage", {"text": "a", "patterns": "x"})
    for path in ("assets/fig.svg", "assets/ext.svg", "assets/broken.svg", "assets/page.html", "assets/theme.css",
                 "assets/img.png", "assets/fake.png", "assets/empty.pdf", "assets/none.svg", "../outside.svg", ""):
        post("/api/skills/design/verify", {"path": path})
    post("/api/skills/design/verify", {"path": "assets/img.png", "kind": "pdf"})
    post("/api/skills/design/verify", {"path": "assets/img.png", "artifact_kind": "png", "kind": "svg"})
    post("/api/skills/design/asset", {"path": "assets\\fig.svg"})
    post("/api/skills/design/asset", {"path": "https://x/a.exe"})
    post("/api/skills/design/asset", {})
    post("/api/skills/design/nope", {})
    post("/api/skills/design", {})
    post("/api/skills/design/render/extra", {})
    post("/api/skills/design/Render", {})
    # ---- setup lifecycle through /api/skills/setup/<action>
    post("/api/skills/setup/discover", {})
    post("/api/skills/setup/draft", {"profile": {"objective": "from the skills route"}})
    post("/api/skills/setup/propose", {})
    post("/api/skills/setup/bogus", {})
    post("/api/skills/agent-team/validate", dict(TASK, gates=["unit", "component"]))
    confirmed_setup_steps(steps, post, h, pw)
    return steps


def confirmed_setup_steps(steps: list, post, h, pw) -> None:
    """Setup warnings of agent-team tasks while the environment is confirmed
    or verified (setup_layers.check_task past the state gate): registered,
    unknown and read-only repositories (by id, folder name and full path),
    gates with and without a recorded command, dict gates, and the
    ``skipped`` code/commands sections.  The 1.0 record is written by hand
    (a confirmed state needs a human), migrated on read like in replay_setup."""
    import replay_setup as rs
    out = steps

    def write(record):
        def run(*dirs):
            for d in dirs:
                rs.clear(Path(d))
                target = rs._setup_dir(Path(d)) / "environment.json"
                target.parent.mkdir(parents=True, exist_ok=True)
                rs._write(target, record(Path(d)))
        out.append((CALL, "", "", run, dict(h)))

    def clear(*dirs):
        for d in dirs:
            rs.clear(Path(d))

    tasks = [dict(TASK, repository="alpha", gates=["unit", "static", {"id": "component"}, {"id": 5}, "scope"]),
             dict(TASK, repository="beta", gates=["unit"]),
             dict(TASK, repository="alpha-fork", gates=[]),
             dict(TASK, repository="team"),
             dict(TASK, repository=" ", gates="unit")]
    for state, profile in (("confirmed", None),
                           ("verified", dict(rs.legacy_profile(), skipped=["code", "commands"])),
                           ("confirmed", dict(rs.legacy_profile(), commands=[{"id": "u", "run": "x", "gate": "static"},
                                                                             "bad", {"gate": None}],
                                              repositories=[{"id": "alpha", "path": rs.SHARED + "/alpha",
                                                             "access": "read-only"}, "bad"]))):
        write(rs.legacy_record(state, by="human:nobody", profile=profile))
        for task in tasks:
            post("/api/skills/agent-team/validate", task)
        post("/api/skills/agent-team/validate", {"kind": "request", "payload": tasks[1]})
        post("/api/skills/agent-team/tasks", dict(tasks[0]))
        post("/api/skills/agent-team/tasks", dict(tasks[1]), pw)
    out.append((CALL, "", "", clear, dict(h)))
