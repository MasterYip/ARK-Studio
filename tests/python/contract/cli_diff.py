"""Differential test of the command line: ``python -m arkstudio`` vs ``ark-server``.

    python -B tests/python/contract/cli_diff.py --go go/ark-server.exe [--only export] [--keep]
    python -B tests/python/contract/cli_diff.py --go python      # Python against itself

Every case runs the same argv once per side, each in its own copy of one
fixture (``fixture.py``: the demo project plus trap units, media, topics, a
Git workspace, an org database).  Compared per case: exit code, stdout,
stderr and the files left behind (``statediff.dump``: SQLite table by table,
every other file by content).

Normalizations (applied to both sides):

C1  The side's work directory -> <WORK> (raw, JSON-escaped and with "/").
C2  ISO timestamps at or after the run start -> <NOW> (statediff S2), and the
    local "%Y%m%d-%H%M" stamp in Markdown bundle names -> <STAMP>.
C3  Project ids (``prj_`` + 10 hex) and user ids (``usr_`` + 16 hex) that are
    not in the fixture -> <PRJn>, in order of first appearance (``new``,
    ``demo`` and ``server-init`` draw them at random).
C4  ZIP files are compared as entry lists (name, size, normalized text);
    member timestamps and compression framing are ignored.
C5  Exit code 1 (an uncaught Python exception, "Traceback ..." on stderr) is
    compared by exit code and the last stderr line ("module.Class: message");
    the Go binary prints only that line.
C6  Python runs with PYTHONIOENCODING=utf-8 (the Go binary always writes
    UTF-8; Python would otherwise use the console/locale code page) and
    COLUMNS=80 (argparse's width when stdout is not a terminal; the Go help
    texts are the 80-column snapshot of gen_cli.py).  Both sides write "\\n"
    as os.linesep, so CRLF is compared on Windows.
C7  Secret files (.agent-token, .auth-secret) by presence and length, password
    hashes in org tables by presence (statediff S6).
C8  meta.change_log_id of a project database (schema v11, a random
    token_hex(8) drawn when the database is created or its log is reset)
    -> <LOGID>.  The differential replay compares it exactly (its replay
    clock makes it deterministic); here ``new`` and ``demo`` draw it freely.

Cases marked ``known`` document intended differences; they are reported, not
counted as failures.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import time
import zipfile
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE))
import statediff  # noqa: E402

PY = sys.executable
PRJ = re.compile(r"prj_[0-9a-f]{10}|usr_[0-9a-f]{16}")
STAMP = re.compile(r"-\d{8}-\d{4}(?=[./\\\"]|\.zip)")
PASSWORD = "correct horse battery"
# A setup profile with a repository inside the fixture (the Git workspace), so
# verify passes some checks and fails others.
SETUP_PROFILE = json.dumps({
    "objective": "Check the fixture workspace é",
    "repositories": [{"id": "code", "path": "{data}", "role": "primary"},
                     {"id": "gone", "path": "{work}/missing-repo", "role": "paper"}],
    "compute": [{"id": "local", "kind": "local", "use": "any"}],
    "commands": [{"id": "unit", "run": "python -m pytest -q", "repository": "code", "gate": "unit"}],
    "secrets": [{"id": "tok", "ref": "env:ARK_CLI_DIFF_TOKEN", "purpose": "test"}],
    "policies": {"commit": "allow", "push": "ask"},
})

# skills render / validate for agent-team, rrsi and design (go/internal/skillkit).
def _skill_case(name, argv, payload=None, *, raw=None):
    case = {"name": name, "argv": argv, "files": {}}
    if raw is not None:
        case["files"]["p.json"] = raw
    elif payload is not None:
        case["files"]["p.json"] = json.dumps(payload, ensure_ascii=False)
    return case


def _render(name, skill, template, payload=None, raw=None, out=False):
    argv = ["skills", "render", skill, template]
    if payload is not None or raw is not None:
        argv += ["--context", "{work}/p.json"]
    if out:
        argv += ["--out", "{work}/out.txt"]
    return _skill_case("skills-render-" + name, argv, payload, raw=raw)


def _validate(name, skill, kind, payload=None, raw=None, root=False):
    argv = ["skills", "validate", skill, kind]
    if payload is not None or raw is not None:
        argv += ["--payload", "{work}/p.json"]
    if root:
        argv += ["--project-root", "{work}"]
    return _skill_case("skills-validate-" + name, argv, payload, raw=raw)


_TASK = {
    "title": "Ship the parser \u2713", "objective": "Make parsing total", "owner": "agent:codex",
    "reviewer": "human:ana", "repository": "ARK-Studio", "base_revision": "main@abc1234",
    "deliverables": ["parser.py", "tests"], "owned_paths": ["src/**"],
    "acceptance": [{"command": "pytest -q", "expected": "green", "evidence": "log"}, "ruff check"],
    "gates": ["Unit", "scope"], "non_goals": ["UI"], "stop_conditions": ["two red runs"],
    "dependencies": ["t-0003", " T-0004 "], "budget": {"tokens": 5000, "hours": 1.5},
    "evidence": {"retain": ["log"], "outside_git": ["traces"]}, "role": "worker", "phase": "Review",
}
_THEME_RAW = (REPO / "arkstudio" / "skills" / "assets" / "design" / "theme.json").read_text(encoding="utf-8")
_DROP = object()


def _theme(**changes):
    theme = json.loads(_THEME_RAW)
    for path, value in changes.items():
        node = theme
        keys = path.split("__")
        for key in keys[:-1]:
            node = node[key]
        if value is _DROP:
            node.pop(keys[-1], None)
        else:
            node[keys[-1]] = value
    return theme


SKILLKIT_RENDER_CASES = [
    _render(f"{skill}-{template.split('.')[0]}", skill, template)
    for skill, templates in (("agent-team", ("goal_manifest.md", "inspection_report.md", "agent_brief.md")),
                             ("rrsi", ("proposer.md", "analyst.md", "critic.md", "domain.json")),
                             ("design", ("theme.json", "design_brief.md", "style_review.md")))
    for template in templates
] + [
    _render("agent-team-context", "Agent-Team", " goal_manifest.md ", {
        "task_id": "WEB-0042", "title": "S\u00e4ubern \u2713", "gates": ["UNIT", "scope"], "dependencies": "T-0001",
        "commands": ["a", "", "b"], "team": {"mode": "solo", "flag": True, "roles": [], "none": None},
        "acceptance": "just one", "evidence": ["kept"], "budget": None, "extra": {"nested": [1, 2.5]}}),
    _render("agent-team-report-commands", "agent-team", "inspection_report.md",
            {"commands": ["make", " ", "make test"], "changed_files": "ab", "results": 3.0}),
    _render("agent-team-bad-gate", "agent-team", "goal_manifest.md", {"gates": ["unit", "deploy"]}),
    _render("agent-team-typeerror", "agent-team", "goal_manifest.md", {"deliverables": 5}),
    _render("agent-team-out", "agent-team", "agent_brief.md", {"objective": "Zeile 1\nZeile 2"}, out=True),
    _render("rrsi-context", "rrsi", "proposer.md", {"task_title": "Harness \u2713", "budget": 3, "diff": None,
                                                    "prune_set": ["skill"], "unused": {"a": 1}}),
    _render("rrsi-domain-context", "rrsi", "domain.json", {"name": "mine", "evolve": [], "extra": "x"}),
    _render("design-theme-merge", "design", "theme.json", {"meta": {"name": "dark \u2713"}, "color": 5}),
    _render("design-brief-context", "design", "design_brief.md", {"mediums": "ab", "venue": None}),
    _render("design-review-out", "design", "style_review.md", {"mode": "web", "mode_checks": []}, out=True),
    _render("design-bad-context", "design", "design_brief.md", {"qualities": 7}),
    _render("design-context-list", "design", "design_brief.md", raw="[1]"),
    _render("rrsi-invalid", "rrsi", "nope.md"),
]

SKILLKIT_VALIDATE_CASES = [
    _validate("design", "design", "theme"),
    _validate("task-empty", "agent-team", "task"),
    _validate("task-ok", "agent-team", " Task ", dict(_TASK, existing_ids=["T-0001", "N-0002"])),
    _validate("task-request", "agent-team", "request", dict(_TASK, task_id="t-0002", existing_ids=["T-0002"])),
    _validate("task-serial", "agent-team", "task", dict(_TASK, task_id="FIG-0007", existing_ids=["WEB-0007"])),
    _validate("task-noserial", "agent-team", "task", dict(_TASK, task_id="ABC")),
    _validate("task-prefix", "agent-team", "task", dict(_TASK, prefix="fig-web", existing_ids=["T-12345", "x", None])),
    _validate("task-bad-prefix", "agent-team", "task", dict(_TASK, task_prefix="9x")),
    _validate("task-existing-string", "agent-team", "task", dict(_TASK, existing_ids="T-0001")),
    _validate("task-existing-int", "agent-team", "task", dict(_TASK, existing_ids=5)),
    _validate("task-many-errors", "agent-team", "task", {
        "title": "x", "owner": "Same", "reviewer": "same", "phase": "done", "status": "  ",
        "importance": "p9", "dependencies": ["bad id", "T-0000"], "worktree": "../wt", "gates": ["unit", "moon"],
        "task_structure": "wide", "team": {"mode": "peer-team", "size": "9"}, "context": {"budget_tokens": 10},
        "model_policy": {"routing": "random"}, "verification": {"independent": False, "method": "vibes"},
        "failure_taxonomy": ["luck"], "handoff": {"contract": ""}, "acceptance": [{"threshold": "t"}]}),
    _validate("task-exceptions", "agent-team", "task", dict(_TASK, owner="x", reviewer="X", acceptance_exception=True,
                                                            worktree="/abs", worktree_exception=1,
                                                            verification={"independent": False})),
    _validate("task-worktree", "agent-team", "task", dict(_TASK, task_id="T-0009", worktree=".worktrees\\T-0009\\")),
    _validate("task-worktree-wrong", "agent-team", "task", dict(_TASK, task_id="T-0009", worktree="C:wt")),
    _validate("task-team", "agent-team", "task", dict(_TASK, task_structure="Breadth", assignee="ana",
                                                      team={"mode": "router-workers", "size": 4, "rationale": "fan out"},
                                                      model_policy={"routing": "TIERED"})),
    _validate("task-unicode", "agent-team", "task", dict(_TASK, task_id="\u0167-0001", title="\u00df" * 300)),
    _validate("task-unicode-prefix", "agent-team", "task", dict(_TASK, prefix="stra\u00dfe")),
    _validate("task-overflow", "agent-team", "task", dict(_TASK, team={"size": 1e400})),
    _validate("manifest", "agent-team", "manifest"),
    _validate("manifest-ok", "agent-team", "Protocol", dict(_TASK, task_id="T-0005", format="x", ark_status="done",
                                                            project_id="proj", existing_ids=["T-0005", "t-0005", "T-0006"])),
    _validate("manifest-dup", "agent-team", "manifest", dict(_TASK, task_id="T-0006", existing_ids=["T-0006 ", "X-0006"])),
    _validate("manifest-existing-int", "agent-team", "manifest", dict(_TASK, existing_ids=3)),
    _validate("agent-team-kind", "agent-team", "other"),
    _validate("rrsi-kind", "rrsi", "nope"),
    _validate("rrsi-round-default", "rrsi", "round"),
    _validate("rrsi-round-example", "rrsi", "selection", {
        "round": 0, "incumbent": {"score": 0.62, "cost": 120000}, "s_star": 0.662, "accepted_counts": {"skill": 1},
        "config": {"delta": 0.017, "beta0": 0.0, "beta1": 0.5, "w_s": 1.0, "w_c": 0.25, "w_n": 0.1,
                   "b_min": 1, "b_max": 3, "T": 10, "stall_window": 3, "recent_window": 3},
        "candidates": [
            {"variant": "candidate-a", "commit": "abc1234", "score": 0.668, "cost": 122000,
             "edits": [{"component": "prompt", "hypothesis": "Inject the unit."}]},
            {"variant": "candidate-b", "score": "0.640", "cost": 110000,
             "edits": [{"component": " Skill ", "hypothesis": "Retrieve on demand."}]},
            {"variant": "candidate-c", "score": 0.7, "cost": 0,
             "edits": [{"component": "memory", "hypothesis": "h"}]},
            {"variant": "gated", "gate_failure": {"why": "lint"}, "edits": [{"component": "config", "hypothesis": "h"}]},
            {"variant": "pending", "edits": [{"component": "config", "hypothesis": "h"}]}],
        "guards": {"candidate-c": ["leaks tests", "slow"], "candidate-a": []}}),
    _validate("rrsi-round-ints", "rrsi", "round", {
        "incumbent": {"score": 1, "cost": 0}, "config": {"delta": 0, "beta0": 1, "w_n": True, "T": 0},
        "candidates": [{"variant": "v", "score": 2, "cost": 5, "edits": [{"component": "skill", "hypothesis": "h"}]}],
        "guards": {"v": "ab"}}),
    _validate("rrsi-round-shaped", "rrsi", "round", {
        "incumbent": {"score": 0.5, "cost": 100}, "s_star": 0.51, "round": "3",
        "config": {"T": 4, "b_max": 2, "w_s": 2, "w_c": 0.5},
        "candidates": [{"variant": "v", "score": 0.505, "cost": 140, "edits": [{"component": "prompt", "hypothesis": "h"}]},
                       {"variant": "w", "score": 0.3, "edits": [{"component": "prompt", "hypothesis": "h"}]}]}),
    _validate("rrsi-round-tie", "rrsi", "round", {
        "incumbent": {"score": 0.5, "cost": 100},
        "candidates": [{"variant": "a", "score": 0.6, "cost": 90, "edits": [{"component": "prompt", "hypothesis": "h"}]},
                       {"variant": "b", "score": 0.6, "cost": 80, "edits": [{"component": "prompt", "hypothesis": "h"}]}]}),
    _validate("rrsi-round-budget", "rrsi", "round", {
        "incumbent": {"score": 0.5}, "config": {"T": 2},
        "candidates": [{"variant": "v", "score": 0.6, "edits": [{"component": "prompt", "hypothesis": "h"}] * 4}],
        "round": 2}),
    _validate("rrsi-round-config-unknown", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"zeta": 1, "alpha": 2}}),
    _validate("rrsi-round-config-neg", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"w_c": -1}}),
    _validate("rrsi-round-config-budget", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"b_min": "4"}}),
    _validate("rrsi-round-config-type", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"delta": "x"}}),
    _validate("rrsi-round-config-int", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"T": "ten"}}),
    _validate("rrsi-round-config-window", "rrsi", "round", {"incumbent": {"score": 1}, "config": {"stall_window": 0}}),
    _validate("rrsi-round-config-string", "rrsi", "round", {"incumbent": {"score": 1}, "config": "ab"}),
    _validate("rrsi-round-incumbent-list", "rrsi", "round", {"incumbent": [1]}),
    _validate("rrsi-round-incumbent-bool", "rrsi", "round", {"incumbent": {"score": True}}),
    _validate("rrsi-round-incumbent-inf", "rrsi", "round", {"incumbent": {"score": "inf"}}),
    _validate("rrsi-round-counts", "rrsi", "round", {"incumbent": {"score": 1}, "accepted_counts": [1]}),
    _validate("rrsi-round-candidate-type", "rrsi", "round", {"incumbent": {"score": 1}, "candidates": ["x"]}),
    _validate("rrsi-round-edit-string", "rrsi", "round", {"incumbent": {"score": 1}, "candidates": [{"variant": "v", "edits": "ab"}]}),
    _validate("rrsi-round-noedits", "rrsi", "round", {"incumbent": {"score": 1}, "candidates": [{"variant": "v"}]}),
    _validate("rrsi-round-novariant", "rrsi", "round", {"incumbent": {"score": 1}, "candidates": [{"edits": [{}]}]}),
    _validate("rrsi-round-badcomponent", "rrsi", "round", {"incumbent": {"score": 1},
                                                           "candidates": [{"variant": "v", "edits": [{"component": "Tool-X", "hypothesis": "h"}]}]}),
    _validate("rrsi-round-nohypothesis", "rrsi", "round", {"incumbent": {"score": 1},
                                                           "candidates": [{"variant": "v", "edits": [{"component": "prompt"}]}]}),
    _validate("rrsi-round-badscore", "rrsi", "round", {"incumbent": {"score": 1},
                                                       "candidates": [{"variant": "v", "score": "high", "edits": [{"component": "prompt", "hypothesis": "h"}]}]}),
    _validate("rrsi-round-counts-value", "rrsi", "round", {"incumbent": {"score": 0.5}, "accepted_counts": {"memory": "x"},
                                                           "candidates": [{"variant": "v", "score": 0.9, "edits": [{"component": "memory", "hypothesis": "h"}]}]}),
    _validate("rrsi-round-guard-type", "rrsi", "round", {"incumbent": {"score": 0.5}, "guards": {"v": [1]},
                                                         "candidates": [{"variant": "v", "score": 0.9, "edits": [{"component": "memory", "hypothesis": "h"}]}]}),
    _validate("rrsi-history", "rrsi", "history", {
        "records": [{"round": 0, "component": "prompt", "hypothesis": "a", "verdict": "accepted", "delta_score": 0.02},
                    {"round": "1", "component": "skill", "hypothesis": "b", "verdict": "rejected", "delta_cost": "0.5"},
                    {"round": 2, "component": "memory", "hypothesis": "c", "verdict": "invalid", "variant": 7},
                    {"round": 3, "component": "skill", "hypothesis": "d", "verdict": "neutral", "commit": "abc"}],
        "config": {"T": 3, "stall_window": 2}}),
    _validate("rrsi-history-empty", "rrsi", "edits"),
    _validate("rrsi-history-stalled", "rrsi", "history", {
        "records": [{"component": "prompt", "hypothesis": "a", "verdict": "neutral"}] * 3, "config": {"T": "2"}}),
    _validate("rrsi-history-bad", "rrsi", "history", {"records": [{"component": "prompt", "hypothesis": "a", "verdict": "maybe"}]}),
    _validate("rrsi-history-badround", "rrsi", "history", {"records": [{"round": -1, "component": "prompt"}]}),
    _validate("rrsi-history-component", "rrsi", "history", {"records": [{"component": "Prompt", "hypothesis": "a"}]}),
    _validate("rrsi-history-string", "rrsi", "history", {"records": "ab"}),
    _validate("rrsi-history-int", "rrsi", "history", {"records": 5}),
    _validate("rrsi-history-delta", "rrsi", "history", {"records": [{"component": "prompt", "hypothesis": "a",
                                                                     "verdict": "accepted", "delta_score": True}]}),
    _validate("rrsi-edit", "rrsi", "edit", {"component": "Skill", "diff": "+ from arkstudio.skills import x\n- y",
                                            "hypothesis": "  h  "}),
    _validate("rrsi-edit-recovered", "rrsi", "edit", {"component": "memory", "diff": "+ prompt = 'x'\n# c\n+  # note"}),
    _validate("rrsi-edit-tool", "rrsi", "edit", {"component": "skill", "diff": "+ registry = ToolRegistry()\n+++ b/x"}),
    _validate("rrsi-edit-empty", "rrsi", "edit", {}),
    _validate("rrsi-leakage", "rrsi", "leakage", {"text": "see tests/test_parser.py\nassertEqual(x)\nT-0123 and hidden tests\nfine"}),
    _validate("rrsi-leak-clean", "rrsi", "leak", {"diff": "+ prompt = render(unit)"}),
    _validate("rrsi-domain", "rrsi", "domain", {
        "name": " mine ", "harness_path": "src\\harness", "evolve": ["a", " b "], "heldout": "c", "smoke": ["a"],
        "metric": 5, "guards": [1, "x"], "component_signals": {"skill": "skills/", "memory": ["Memory("]}}),
    _validate("rrsi-domain-errors", "rrsi", "domain", {"name": "x", "harness_path": "a", "evolve": ["a"], "heldout": ["a", "b"], "smoke": ["b"]}),
    _validate("rrsi-domain-dup", "rrsi", "domain", {"name": "x", "harness_path": "a", "evolve": ["a", "a "]}),
    _validate("rrsi-domain-path", "rrsi", "domain", {"name": "x", "harness_path": "C:\\x", "evolve": ["a"]}),
    _validate("rrsi-domain-dotdot", "rrsi", "domain", {"name": "x", "harness_path": "a/../b", "evolve": ["a"]}),
    _validate("rrsi-domain-signal", "rrsi", "domain", {"name": "x", "harness_path": "a", "evolve": ["a"], "component_signals": {"gpu": []}}),
    _validate("rrsi-domain-signals-type", "rrsi", "domain", {"component_signals": ["skill"]}),
    _validate("rrsi-domain-empty", "rrsi", "domain"),
    _validate("design-kind", "design", "palette"),
    _validate("design-theme-ok", "design", "theme.json", raw=_THEME_RAW),
    _validate("design-theme-wrapped", "design", "theme", {"theme": _theme(meta__name="x \u2713"), "variant": "paper"}),
    _validate("design-theme-variant-unknown", "design", "theme", {"theme": _theme(), "variant": "print"}),
    _validate("design-theme-variant-int", "design", "theme", {"theme": _theme(), "variant": 5}),
    _validate("design-theme-variant-list", "design", "theme", {"theme": _theme(), "variant": ["paper"]}),
    _validate("design-theme-variants-bad", "design", "theme", {"theme": _theme(variants=[1]), "variant": "web"}),
    _validate("design-theme-variants-list", "design", "theme", {"theme": _theme(variants=[1])}),
    _validate("design-theme-variant-names", "design", "theme", {
        "theme": _theme(variants={"Print": {}, "web": 3, "video": {"meta": {"version": 0}}}), "variant": "video"}),
    _validate("design-theme-missing", "design", "theme", {"meta": {}, "colour": {}}),
    _validate("design-theme-meta-version", "design", "theme", {"theme": _theme(meta__version=1.0)}),
    _validate("design-theme-meta-intent", "design", "theme", {"theme": _theme(meta__intent=["", " "])}),
    _validate("design-theme-roles", "design", "theme", {"theme": _theme(
        color__roles__focus=_DROP, color__roles__Bad={"value": "#fff"},
        color__roles__surface={"value": "#FFF", "opacity": "2", "usage": "u"})}),
    _validate("design-theme-hex", "design", "theme", {"theme": _theme(color__roles__surface={"value": "fff", "usage": "u"})}),
    _validate("design-theme-palette", "design", "theme", {"theme": _theme(color__categorical="ab")}),
    _validate("design-theme-palette-int", "design", "theme", {"theme": _theme(color__sequential=5)}),
    _validate("design-theme-opacity", "design", "theme", {"theme": _theme(color__opacity={"muted": "nan"})}),
    _validate("design-theme-weight", "design", "theme", {"theme": _theme(typography__sans__weight=950)}),
    _validate("design-theme-weight-type", "design", "theme", {"theme": _theme(typography__mono__weight=True)}),
    _validate("design-theme-style", "design", "theme", {"theme": _theme(typography__serif__style="Oblique")}),
    _validate("design-theme-roles-pt", "design", "theme", {"theme": _theme(typography__roles_pt={"body": 0})}),
    _validate("design-theme-casing", "design", "theme", {"theme": _theme(typography__casing={"h1": "SMALLCAPS"})}),
    _validate("design-theme-strokes", "design", "theme", {"theme": _theme(shape__strokes={"a": {"color": "ink", "width_pt": 1}})}),
    _validate("design-theme-shadow", "design", "theme", {"theme": _theme(shape__shadows={"s": {"color": "none", "opacity": 2, "blur": 1}})}),
    _validate("design-theme-vocab", "design", "theme", {"theme": _theme(shape__vocabulary={"box": {
        "geometry": "rectangle", "fill": "ink", "stroke": {"color": "paper"}, "padding": [1]}})}),
    _validate("design-theme-spacing", "design", "theme", {"theme": _theme(spacing__scale="1a")}),
    _validate("design-theme-layout", "design", "theme", {"theme": _theme(layout__grid_columns=0)}),
    _validate("design-theme-chart", "design", "theme", {"theme": _theme(chart__axes=None)}),
    _validate("design-theme-web", "design", "theme", {"theme": _theme(web__breakpoints_px={"sm": 1.5})}),
    _validate("design-theme-motion", "design", "theme", {"theme": _theme(motion__reduced_motion="none")}),
    _validate("design-theme-video", "design", "theme", {"theme": _theme(video__safe_area_percent=30)}),
    _validate("design-theme-contrast", "design", "theme", {"theme": _theme(
        color__roles__text_primary={"value": "#EEEEEE", "opacity": 0.5, "usage": "u"},
        color__roles__text_secondary={"value": "#FAFAFA", "usage": "u"},
        color__categorical=["#777777", "#777", "#777777FF"])}),
    _validate("design-asset", "design", "asset", {"path": "assets\\fig.SVG"}, root=True),
    _validate("design-asset-errors", "design", "asset", {"path": "https://x/../a.exe"}),
    _validate("design-asset-data", "design", "asset", {"path": "/data:x"}, root=True),
    _validate("design-asset-empty", "design", "asset"),
    _validate("design-asset-dots", "design", "asset", {"path": "./assets//x/./y.png/"}),
    _validate("design-asset-escape", "design", "asset", {"path": "assets/../../out.png"}, root=True),
    _validate("design-artifact-missing", "design", "artifact", {"path": "assets/none.svg"}, root=True),
    _validate("design-artifact-noroot", "design", "artifact", {"path": "assets//none.svg"}),
    _validate("design-artifact-type", "design", "artifact", {"path": 5}),
    _validate("design-artifact-outside", "design", "artifact", {"path": "../outside.css"}, root=True),
]
_SVG_OK = '<?xml version="1.0"?>\n<svg xmlns="http://www.w3.org/2000/svg" width="4"><rect/></svg>\n'
for _name, _body, _suffix, _kind in (
        ("svg-ok", _SVG_OK, ".svg", None),
        ("svg-external", '<svg xmlns="http://www.w3.org/2000/svg"><image HREF = "https://cdn/x.png"/>'
                         "<a xlink:href='http://a/b' xmlns:xlink=\"http://www.w3.org/1999/xlink\"/></svg>", ".svg", None),
        ("svg-root", "<html/>", ".svg", None),
        ("svg-prefixed", "<s:svg xmlns:s='urn:s'/>", ".svg", None),
        ("svg-empty", "", ".svg", None),
        ("svg-space", "   \n ", ".svg", None),
        ("svg-text", "not xml", ".svg", None),
        ("svg-open", "<svg>", ".svg", None),
        ("svg-mismatch", "<svg>\n<g>\n</svg>", ".svg", None),
        ("svg-junk", "<svg/>x", ".svg", None),
        ("svg-two", "<a/><b/>", ".svg", None),
        ("svg-badtoken", "< svg/>", ".svg", None),
        ("svg-unquoted", "<svg a=1/>", ".svg", None),
        ("svg-entity", "<svg>&nbsp;</svg>", ".svg", None),
        ("svg-charref", "<svg>&#0;&#x41;</svg>", ".svg", None),
        ("svg-unbound", "<x:svg/>", ".svg", None),
        ("svg-dupattr", "<svg a='1' a='2'/>", ".svg", None),
        ("svg-control", "<svg>\x01</svg>", ".svg", None),
        ("svg-unclosed", "<svg", ".svg", None),
        ("svg-unclosed-end", "<svg></svg", ".svg", None),
        ("svg-comment-only", "<!-- c -->", ".svg", None),
        ("svg-doctype", "<!DOCTYPE svg [<!ENTITY a 'b'>]>\n<svg><![CDATA[<x>]]><?pi x?><!-- ok --></svg>\n<!-- after -->", ".svg", None),
        ("svg-crlf", "<svg>\r\n<g>\r\n</h></svg>", ".svg", None),
        ("png", b"\x89PNG\r\n\x1a\nrest", ".png", None),
        ("png-bad", b"GIF89a", ".PNG", None),
        ("jpeg-as-pdf", b"\xff\xd8\xff\xe0", ".jpg", "pdf"),
        ("html", '<meta name="viewport" content="x"><script src="https://cdn/a.js"></script>', ".html", None),
        ("html-bare", "<p>hi</p>", ".html", None),
        ("css", ":root { --ink: #111; }", ".css", None),
        ("css-plain", "p { color: red }", ".css", None),
        ("webp", "RIFF", ".webp", None),
        ("unknown", "x", ".txt", None),
        ("empty-png", "", ".png", None)):
    _payload = {"path": f"assets/{_name}{_suffix}"}
    if _kind:
        _payload["artifact_kind"] = _kind
    _case = _validate(f"design-artifact-{_name}", "design", "artifact", _payload, root=True)
    _case["files"][f"assets/{_name}{_suffix}"] = _body
    SKILLKIT_VALIDATE_CASES.append(_case)

# argv placeholders: {data} fixture data dir, {root} its projects folder,
# {project} the fixture project folder, {work} the side's work dir.
CASES: list[dict] = [
    # help and parse errors
    {"name": "help", "argv": ["-h"]},
    {"name": "help-serve", "argv": ["serve", "--help"]},
    {"name": "help-skills-setup", "argv": ["skills", "setup", "-h"]},
    {"name": "help-session-watch", "argv": ["session-watch", "-h"]},
    {"name": "bad-command", "argv": ["frobnicate"]},
    {"name": "agent-no-sub", "argv": ["agent"]},
    {"name": "no-command-flag", "argv": ["--root", "{root}"]},
    # new / demo
    {"name": "new-blank", "argv": ["new", "My paper", "--root", "{work}/fresh"]},
    {"name": "new-paper", "argv": ["new", "论文 Paper", "--template", "paper", "--description", "désc", "--root", "{root}"]},
    {"name": "new-demo-template", "argv": ["new", "Demo copy", "--template", "demo", "--root", "{work}/fresh"]},
    {"name": "new-existing-name", "argv": ["new", "My paper", "--root", "{work}/twice"],
     "before": [["new", "My paper", "--root", "{work}/twice"]]},
    {"name": "new-empty-name", "argv": ["new", "  ", "--root", "{work}/fresh"]},
    {"name": "new-long-name", "argv": ["new", "x" * 81, "--root", "{work}/fresh"]},
    {"name": "new-default-root", "argv": ["new", "Env root"]},
    {"name": "demo", "argv": ["demo", "--root", "{work}/fresh"]},
    # export
    {"name": "export-json", "argv": ["export", "--project", "{project}"]},
    {"name": "export-markdown", "argv": ["export", "--project", "{project}", "--format", "markdown"]},
    {"name": "export-csv", "argv": ["export", "--project", "{project}", "--format", "csv"]},
    {"name": "export-svg-out", "argv": ["export", "--project", "{project}", "--format", "svg", "--out", "{work}/map.svg"]},
    {"name": "export-json-out-rel", "argv": ["export", "--project", "{project}", "--out", "out.json"]},
    {"name": "export-bundle-dir", "argv": ["export", "--project", "{project}", "--format", "bundle",
                                           "--units", "{first_uid}", "--depth", "2", "--out", "{work}"]},
    {"name": "export-bundle-file", "argv": ["export", "--project", "{project}", "--format", "bundle",
                                            "--units", "{first_uid}", "--depth", "0", "--out", "b.zip"]},
    {"name": "export-bundle-cwd", "argv": ["export", "--project", "{project}", "--format", "bundle",
                                           "--units", "{first_uid},nope"]},
    {"name": "export-bundle-no-units", "argv": ["export", "--project", "{project}", "--format", "bundle"]},
    {"name": "export-bundle-bad-depth", "argv": ["export", "--project", "{project}", "--format", "bundle",
                                                 "--units", "{first_uid}", "--depth", "9"]},
    {"name": "export-not-project", "argv": ["export", "--project", "{data}"]},
    {"name": "export-missing", "argv": ["export", "--project", "{work}/nope.ark"]},
    # agent context
    {"name": "agent-context-project", "argv": ["agent", "context", "--project", "{project}"]},
    {"name": "agent-context-root", "argv": ["agent", "context", "--root", "{root}"]},
    {"name": "agent-context-empty", "argv": ["agent", "context", "--root", "{work}/empty"]},
    # skills
    {"name": "skills-default", "argv": ["skills"]},
    {"name": "skills-list", "argv": ["skills", "list"]},
    {"name": "skills-show", "argv": ["skills", "show", " RRSI "]},
    {"name": "skills-show-unknown", "argv": ["skills", "show", "nope"]},
    {"name": "skills-interface", "argv": ["skills", "interface"]},
    {"name": "skills-render-unknown-template", "argv": ["skills", "render", " RRSI ", "x"]},
    {"name": "skills-render-unknown-skill", "argv": ["skills", "render", "Nope", "x"]},
    {"name": "skills-render-badjson-first", "argv": ["skills", "render", "nope", "x", "--context", "{{"]},
    {"name": "skills-render-setup-json", "argv": ["skills", "render", "setup", " environment.json "]},
    {"name": "skills-render-setup-summary", "argv": ["skills", "render", "Setup", "setup_summary.md"]},
    {"name": "skills-render-setup-context", "argv": ["skills", "render", "setup", "environment.json",
                                                     "--context", "{work}/c.json"],
     "files": {"c.json": SETUP_PROFILE}},
    {"name": "skills-render-setup-out", "argv": ["skills", "render", "setup", "setup_summary.md",
                                                 "--out", "{work}/summary.md", "--context", "{work}/c.json"],
     "files": {"c.json": '{"objective": "", "secrets": [{"id": "s", "ref": "ghp_abcdefghijklmnopqrstuvwxyz0123456789"}]}'}},
    *SKILLKIT_RENDER_CASES,
    {"name": "skills-validate-unknown-skill", "argv": ["skills", "validate", "nope", "x"]},
    {"name": "skills-validate-setup", "argv": ["skills", "validate", "setup", "Environment", "--payload", "{work}/p.json"],
     # fixed paths: the digest hashes them, so side-specific work dirs would differ
     "files": {"p.json": SETUP_PROFILE.replace("{data}", "/srv/data").replace("{work}", "/srv/work")}},
    {"name": "skills-validate-setup-wrapped", "argv": ["skills", "validate", "setup", "profile", "--payload",
                                                       "{work}/p.json"],
     "files": {"p.json": '{"profile": {"repositories": [{"id": "x", "path": "", "access": "rw"}]}}'}},
    {"name": "skills-validate-setup-kind", "argv": ["skills", "validate", "setup", "other"]},
    {"name": "skills-validate-brief", "argv": ["skills", "validate", "agent-team", "delegation", "--payload",
                                               "{work}/b.json"], "files": {"b.json": '{"objective": "x", "team_size": 3}'}},
    *SKILLKIT_VALIDATE_CASES,
    # skills brief (interface.validate_brief; go/internal/skillbrief has the CPython vectors)
    {"name": "skills-brief-empty", "argv": ["skills", "brief"]},
    {"name": "skills-brief-ok", "argv": ["skills", "brief", "--payload", "{work}/b.json"],
     "files": {"b.json": json.dumps({"objective": "Fix the parser", "owned_paths": ["src/parser.py"],
                                     "acceptance": [{"command": "pytest -q", "evidence": "green run"}],
                                     "stop_conditions": ["tests fail twice"], "deliverables": ["patch"]})}},
    {"name": "skills-brief-team", "argv": ["skills", "brief", "--payload", "{work}/b.json"],
     "files": {"b.json": json.dumps({"goal": "Refute claim C", "task_structure": "Adversarial",
                                     "team": {"mode": "peer-team", "size": 9, "roles": ["author", "Critic"]},
                                     "context": {"budget_tokens": "600_000", "artifacts": ["../x", "notes/a.md"]},
                                     "owned_paths": "docs/", "acceptance": "review passes",
                                     "stop_conditions": "budget spent", "gates": "unit",
                                     "verification": {"independent": None, "method": "Human"},
                                     "failure_taxonomy": ["tool", "spec", "luck"], "dependencies": "T1"},
                                    ensure_ascii=False)}},
    {"name": "skills-brief-typeerror", "argv": ["skills", "brief", "--payload", "{work}/b.json"],
     "files": {"b.json": '{"objective": "x", "gates": 5}'}},
    {"name": "skills-brief-overflow", "argv": ["skills", "brief", "--payload", "{work}/b.json"],
     "files": {"b.json": '{"team": {"size": 1e400}}'}},
    {"name": "skills-brief-notobject", "argv": ["skills", "brief", "--payload", '"text"']},
    # skills setup (the 1.0 single-profile lifecycle on the project layer)
    {"name": "skills-setup-noproject", "argv": ["skills", "setup"]},
    {"name": "skills-setup-not-project", "argv": ["skills", "setup", "state", "--project", "{data}"]},
    {"name": "skills-setup-state", "argv": ["skills", "setup", "--project", "{project}"]},
    {"name": "skills-setup-summary", "argv": ["skills", "setup", "summary", "--project", "{project}"]},
    {"name": "skills-setup-questions-project", "argv": ["skills", "setup", "questions", "--project", "{project}"]},
    {"name": "skills-setup-questions-inline", "argv": ["skills", "setup", "questions", "--payload",
                                                       '{{"objective": "x", "repositories": [{{"id": "c", "path": "~/c"}}]}}']},
    {"name": "skills-setup-questions-file", "argv": ["skills", "setup", "questions", "--payload", "{work}/p.json"],
     "files": {"p.json": '﻿{"objective": "from a file",\r\n "compute": [{"id": "g", "kind": "ssh"}]}'}},
    {"name": "skills-setup-questions-badjson", "argv": ["skills", "setup", "questions", "--payload", '{{"a": [1,']},
    {"name": "skills-setup-questions-badjson-line", "argv": ["skills", "setup", "questions", "--payload",
                                                             "{work}/p.json"], "files": {"p.json": '{\n "a": 1\n "b": 2}'}},
    {"name": "skills-setup-questions-notobject", "argv": ["skills", "setup", "questions", "--payload", "[1]"]},
    {"name": "skills-setup-discover", "argv": ["skills", "setup", "discover", "--project", "{project}",
                                               "--paths", "{data}", "{work}/none"]},
    {"name": "skills-setup-discover-default", "argv": ["skills", "setup", "discover", "--project", "{project}"]},
    {"name": "skills-setup-draft", "argv": ["skills", "setup", "draft", "--project", "{project}", "--payload",
                                            "{work}/p.json"], "files": {"p.json": SETUP_PROFILE}},
    {"name": "skills-setup-propose", "argv": ["skills", "setup", "draft", "--project", "{project}", "--payload",
                                              "{work}/p.json", "--propose"], "files": {"p.json": SETUP_PROFILE}},
    {"name": "skills-setup-draft-secret", "argv": ["skills", "setup", "draft", "--project", "{project}",
                                                   "--payload", '{{"secrets": [{{"id": "k", "ref": "sk-abcdefghijklmnopqrstuvwxyz123456"}}]}}']},
    {"name": "skills-setup-draft-invalid", "argv": ["skills", "setup", "draft", "--project", "{project}",
                                                    "--payload", '{{"repositories": [{{"path": ""}}], "policies": {{"push": "never"}}}}']},
    {"name": "skills-setup-confirm-unset", "argv": ["skills", "setup", "confirm", "--project", "{project}"]},
    {"name": "skills-setup-confirm-mismatch", "argv": ["skills", "setup", "confirm", "--project", "{project}",
                                                       "--digest", "abc"],
     "files": {"p.json": SETUP_PROFILE},
     "before": [["skills", "setup", "draft", "--project", "{project}", "--payload", "{work}/p.json", "--propose"]]},
    {"name": "skills-setup-confirm", "argv": ["skills", "setup", "confirm", "--project", "{project}",
                                              "--digest", "{digest}", "--note", "reviewed ✓"],
     "files": {"p.json": SETUP_PROFILE}, "env": {"USERNAME": "Reviewer", "LOGNAME": "", "USER": "", "LNAME": ""},
     "before": [["skills", "setup", "draft", "--project", "{project}", "--payload", "{work}/p.json", "--propose"]]},
    {"name": "skills-setup-verify-unconfirmed", "argv": ["skills", "setup", "verify", "--project", "{project}"],
     "files": {"p.json": SETUP_PROFILE},
     "before": [["skills", "setup", "draft", "--project", "{project}", "--payload", "{work}/p.json"]]},
    {"name": "skills-setup-verify", "argv": ["skills", "setup", "verify", "--project", "{project}"],
     "files": {"p.json": SETUP_PROFILE}, "env": {"USERNAME": "Reviewer"},
     "before": [["skills", "setup", "draft", "--project", "{project}", "--payload", "{work}/p.json", "--propose"],
                ["skills", "setup", "confirm", "--project", "{project}", "--digest", "{digest}"]]},
    {"name": "skills-setup-summary-after", "argv": ["skills", "setup", "summary", "--project", "{project}"],
     "files": {"p.json": SETUP_PROFILE},
     "before": [["skills", "setup", "draft", "--project", "{project}", "--payload", "{work}/p.json"]]},
    # server-init
    {"name": "server-init", "argv": ["server-init", "--config", "{work}/etc/ark.json", "--data-dir", "{work}/srv",
                                     "--owner", "Admin.1", "--password-file", "{work}/pw.txt", "--port", "9000",
                                     "--public-url", "https://ark.example.org/"],
     "files": {"pw.txt": PASSWORD + "\n"}},
    {"name": "server-init-short", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir", "{work}/srv",
                                           "--password-file", "{work}/pw.txt"], "files": {"pw.txt": "short\n"}},
    {"name": "server-init-nonempty", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir", "{data}",
                                              "--password-file", "{work}/pw.txt"], "files": {"pw.txt": PASSWORD}},
    {"name": "server-init-bad-user", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir", "{work}/srv",
                                              "--owner", "a b", "--password-file", "{work}/pw.txt"],
     "files": {"pw.txt": PASSWORD}},
    {"name": "server-init-bad-port", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir", "{work}/srv",
                                              "--port", "0", "--password-file", "{work}/pw.txt"],
     "files": {"pw.txt": PASSWORD}},
    {"name": "server-init-bad-url", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir", "{work}/srv",
                                             "--public-url", "ftp://x", "--password-file", "{work}/pw.txt"],
     "files": {"pw.txt": PASSWORD}},
    {"name": "server-init-empty-pwfile", "argv": ["server-init", "--config", "{work}/c.json", "--data-dir",
                                                  "{work}/srv", "--password-file", "{work}/pw.txt"],
     "files": {"pw.txt": "\r\n"}},
    # serve startup checks (all refuse to start)
    {"name": "serve-expose-noauth", "argv": ["serve", "--root", "{root}", "--host", "0.0.0.0", "--port", "1"]},
    {"name": "serve-require-auth", "argv": ["serve", "--root", "{root}", "--require-auth", "--port", "1"]},
    {"name": "serve-config-missing", "argv": ["serve", "--config", "{work}/none.json"]},
    {"name": "serve-config-not-object", "argv": ["serve", "--config", "{work}/c.json"], "files": {"c.json": "[1]"}},
    {"name": "serve-config-unknown", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"zeta": 1, "alpha": 2, "root": "x"}'}},
    {"name": "serve-config-bad-bool", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"expose": "yes"}'}},
    {"name": "serve-config-bad-port", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"port": 70000}'}},
    {"name": "serve-config-neg-int", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"auth_session_ttl": -1}'}},
    {"name": "serve-config-empty-path", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"org_db": " "}'}},
    {"name": "serve-config-bad-media", "argv": ["serve", "--config", "{work}/c.json"],
     "files": {"c.json": '{"media_root": ["a", ""]}'}},
    {"name": "serve-config-no-owner", "argv": ["serve", "--config", "{work}/c.json", "--port", "1"],
     "files": {"c.json": '\ufeff{"root": "fresh/projects", "org_db": "fresh/org.sqlite3"}'}},
    {"name": "serve-auth-secret-no-org", "argv": ["serve", "--root", "{work}/lost/projects", "--port", "1"],
     "files": {"lost/projects/.auth-secret": "x" * 32}},
    {"name": "serve-two-password-sources", "argv": ["serve", "--root", "{work}/fresh", "--auth-user", "a",
                                                    "--auth-password", "p", "--auth-password-hash", "h", "--port", "1"]},
    {"name": "serve-user-no-password", "argv": ["serve", "--root", "{work}/fresh", "--auth-user", "a", "--port", "1"]},
    {"name": "serve-password-file-missing", "argv": ["serve", "--root", "{work}/fresh", "--auth-user", "a",
                                                     "--auth-password-file", "{work}/none.txt", "--port", "1"]},
    # SSH helpers that fail before starting ssh
    {"name": "tunnel-bad-bind", "argv": ["mobile-tunnel", "--bind", "lan"]},
    {"name": "tunnel-bad-port", "argv": ["mobile-tunnel", "--local-port", "0"]},
    {"name": "tunnel-space-target", "argv": ["mobile-tunnel", "--ssh", "a b"]},
    {"name": "remote-empty-host", "argv": ["remote", "--ssh", ""]},
    # session-watch without a token
    {"name": "session-watch-no-token", "argv": ["session-watch", "--id", "ark-ses-1", "--once"]},
    {"name": "session-watch-refused", "argv": ["session-watch", "--id", "ARK-ses-1", "--file", "{work}/s.jsonl",
                                               "--once", "--server", "http://127.0.0.1:9"],
     "files": {"s.jsonl": '{"type": "user", "text": "ark-ses-1"}\n'}, "env": {"ARK_TOKEN": "arka_x"}},
    {"name": "session-watch-not-found", "argv": ["session-watch", "--id", "ark-ses-2", "--agent", "claude", "--once"],
     "files": {"claude/projects/p/a.jsonl": '{"text": "other"}\n'},
     "env": {"ARK_TOKEN": "arka_x", "CLAUDE_CONFIG_DIR": "{work}/claude", "CODEX_HOME": "{work}/codex"}},
    {"name": "session-watch-missing-file", "argv": ["session-watch", "--id", "ark-ses-1", "--file", "{work}/no.jsonl",
                                                    "--once"], "env": {"ARK_TOKEN": "arka_x"}},
    {"name": "desktop", "argv": ["desktop", "--no-server"], "known": "desktop is a pywebview GUI (Python only)",
     "skip_python": True},
]


def _rmtree(path: Path) -> None:
    def onerror(func, p, _exc):
        os.chmod(p, stat.S_IWRITE)
        func(p)
    if path.exists():
        shutil.rmtree(path, onerror=onerror)


def build_fixture(base: Path) -> dict:
    data = base / "data"
    out = subprocess.run([PY, "-B", str(HERE / "fixture.py"), str(data)], capture_output=True, text=True,
                         encoding="utf-8", env=_env(base), check=True)
    info = json.loads(out.stdout)
    sys.path.insert(0, str(REPO))
    import sqlite3
    folder = data / "projects" / info["folder"]
    with sqlite3.connect(folder / "ark.sqlite3") as conn:
        info["first_uid"] = conn.execute("SELECT uid FROM units ORDER BY id LIMIT 1").fetchone()[0]
    return info


def _env(work: Path) -> dict:
    env = {k: v for k, v in os.environ.items() if not k.startswith("ARK_")}
    env.update({"PYTHONPATH": str(REPO), "PYTHONIOENCODING": "utf-8", "COLUMNS": "80",
                "PYTHONDONTWRITEBYTECODE": "1", "ARK_ROOT": str(work / "data" / "projects")})
    return env


def _command(side: str, go: str) -> list[str]:
    return [PY, "-B", "-m", "arkstudio"] if side.startswith("python") else [go]


class Normalizer:
    def __init__(self, work: Path, start: str, known_ids: set[str]):
        raw = str(work.resolve())
        self.paths = sorted({raw, json.dumps(raw)[1:-1], raw.replace("\\", "/")}, key=len, reverse=True)
        self.start = start
        self.known = known_ids
        self.ids: dict[str, str] = {}

    def text(self, value: str) -> str:
        for p in self.paths:
            value = value.replace(p, "<WORK>")
        value = statediff.ISO.sub(lambda m: "<NOW>" if m.group(0) >= self.start[:19] else m.group(0), value)
        value = STAMP.sub("-<STAMP>", value)

        def prj(m):
            if m.group(0) in self.known:
                return m.group(0)
            return self.ids.setdefault(m.group(0), f"<PRJ{len(self.ids) + 1}>")
        return PRJ.sub(prj, value)


def _zip_listing(path: Path, norm: Normalizer) -> list:
    items = []
    with zipfile.ZipFile(path) as zf:
        for info in zf.infolist():
            data = zf.read(info)
            try:
                body = norm.text(data.decode("utf-8"))
            except UnicodeDecodeError:
                body = f"<{len(data)} bytes {hash(data)}>"
            items.append([norm.text(info.filename), info.file_size, body])
    return items


def _state(work: Path, norm: Normalizer, start: str) -> dict:
    state = statediff.dump(work, start)
    out = {}
    for rel, value in state.items():
        if rel.endswith(".zip"):
            value = {"zip": _zip_listing(work / rel, norm)}
        elif isinstance(value, dict) and isinstance(value.get("sqlite"), dict):
            for row in value["sqlite"].get("meta") or []:
                if row.get("key") == "change_log_id":
                    row["value"] = "<LOGID>"
        out[norm.text(rel)] = json.loads(norm.text(json.dumps(value, ensure_ascii=False)))
    return out


def _stderr(text: str, code: int) -> str:
    if code == 1:
        lines = [line for line in text.splitlines() if line.strip()]
        return lines[-1] if lines else ""
    return text


def run_side(side: str, case: dict, base: Path, root: Path, info: dict, go: str, start: str) -> dict:
    work = root / case["name"] / side
    _rmtree(work)
    shutil.copytree(base, work)
    for rel, content in (case.get("files") or {}).items():
        target = work / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(content, bytes):  # binary fixtures are written as they are
            target.write_bytes(content)
            continue
        target.write_bytes(content.replace("{data}", str(work / "data").replace("\\", "/"))
                           .replace("{work}", str(work).replace("\\", "/")).encode("utf-8"))
    fill = {"data": str(work / "data"), "root": str(work / "data" / "projects"),
            "project": str(work / "data" / "projects" / info["folder"]), "work": str(work),
            "first_uid": info["first_uid"]}

    def expand(argv):
        if any("{digest}" in a for a in argv):
            layer = Path(fill["project"]) / "setup" / "project.json"
            fill["digest"] = json.loads(layer.read_text(encoding="utf-8")).get("digest", "") if layer.is_file() else ""
        return [a.format(**fill) for a in argv]

    env = _env(work)
    env.update({k: v.format(**fill) for k, v in (case.get("env") or {}).items()})
    for pre in case.get("before") or []:
        subprocess.run(_command(side, go) + expand(pre), cwd=work, env=env, capture_output=True, timeout=120)
    proc = subprocess.run(_command(side, go) + expand(case["argv"]), cwd=work, env=env, capture_output=True,
                          timeout=120, stdin=subprocess.DEVNULL)
    norm = Normalizer(work, start, {info["project"]})
    stdout = norm.text(proc.stdout.decode("utf-8", "replace"))
    stderr = norm.text(_stderr(proc.stderr.decode("utf-8", "replace"), proc.returncode))
    return {"exit": proc.returncode, "stdout": stdout, "stderr": stderr, "state": _state(work, norm, start)}


def compare(a: dict, b: dict) -> list[str]:
    problems = []
    if a["exit"] != b["exit"]:
        problems.append(f"exit {a['exit']} (python) != {b['exit']} (go)")
    for stream in ("stdout", "stderr"):
        if a[stream] != b[stream]:
            x, y = a[stream], b[stream]
            at = next((i for i in range(min(len(x), len(y))) if x[i] != y[i]), min(len(x), len(y)))
            problems.append(f"{stream} differs at {at}: python {x[max(0, at - 60):at + 100]!r} "
                            f"go {y[max(0, at - 60):at + 100]!r}")
    problems += statediff.diff(a["state"], b["state"])
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--go", default=str(REPO / "go" / "ark-server.exe"),
                        help="ark-server binary, or 'python' to compare Python with itself")
    parser.add_argument("--work", default="", help="work dir (default: a new temp dir)")
    parser.add_argument("--only", default="", help="run cases whose name contains this text")
    parser.add_argument("--keep", action="store_true", help="keep the work dir")
    parser.add_argument("--json", default="", help="write the per-case result here")
    args = parser.parse_args(argv)
    if args.work:
        root = Path(args.work)
    else:
        root = Path(os.environ.get("TEMP", "/tmp")) / f"cli-diff-{os.getpid()}"
    root.mkdir(parents=True, exist_ok=True)
    go = args.go if args.go == "python" else str(Path(args.go).resolve())
    sides = ("python", "go")
    try:
        base = root / "_base"
        _rmtree(base)
        base.mkdir()
        info = build_fixture(base)
        # Fixture timestamps stay as they are; anything stamped later is <NOW>.
        time.sleep(1.1)
        start = datetime.now(timezone.utc).isoformat(timespec="seconds")
        failures, known, results = 0, 0, []
        for case in CASES:
            if args.only and args.only not in case["name"]:
                continue
            t0 = time.monotonic()
            if go == "python":
                if case.get("skip_python"):
                    continue
                out = {s: run_side(s, case, base, root, info, go, start) for s in ("python", "python2")}
                a, b = out["python"], out["python2"]
            else:
                if case.get("skip_python"):
                    b = run_side("go", case, base, root, info, go, start)
                    print(f"[known] {case['name']}: {case.get('known')} (go exit {b['exit']}: {b['stderr'][:120]!r})")
                    known += 1
                    continue
                a, b = (run_side(s, case, base, root, info, go, start) for s in sides)
            problems = compare(a, b)
            status = "ok" if not problems else ("known" if case.get("known") else "FAIL")
            if status == "FAIL":
                failures += 1
            elif status == "known":
                known += 1
            results.append({"name": case["name"], "status": status, "problems": problems})
            print(f"[{status}] {case['name']} ({time.monotonic() - t0:.1f}s)"
                  + (f": {case.get('known')}" if status == "known" else ""))
            if status == "FAIL":
                for p in problems[:8]:
                    print("    " + p)
        print(f"{len(results)} cases, {failures} failing, {known} known differences")
        if args.json:
            Path(args.json).write_text(json.dumps(results, indent=1, ensure_ascii=False), encoding="utf-8")
        return 1 if failures else 0
    finally:
        if not args.keep:
            _rmtree(root)


if __name__ == "__main__":
    raise SystemExit(main())
