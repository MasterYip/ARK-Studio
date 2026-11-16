"""Environment setup module of the services replay (replay_services.py ``--only setup``).

Routes: POST /api/agent/v1/setup/{action} (Handler._setup_action), the full
per-viewer payload of GET /api/skills/setup/state and GET /api/agent/v1/setup
and the ``setup`` block of GET /api/project/overview for a project that has a
``setup/`` folder (arkstudio/skills/setup.py, setup_layers.py).

CALL steps write the same files into both data dirs between the requests:
small Git repositories under ``<data>/setup-work`` built with a fixed
identity and fixed dates (identical commit ids on both sides; a hand-made
extra worktree entry so ``git worktree list`` counts two without a second
index file), a task-space folder, 1.0 ``environment.json`` profiles
(confirmed with the matching digest, so the migration carries the
confirmation; proposed; invalid), and hand-edited 2.0 layer files (edited
profile -> stale, unreadable JSON, a user file that claims another owner,
secret-looking values, too many resources, locations on another host).
Request bodies name paths inside each server's own copy with
``@DATA_DIR@`` (diffreplay.per_server).  Confirming over /api/agent/v1 always
needs a human (403), so a confirmed layer is only reachable through files.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
import replay_gpu  # noqa: E402
from fixture import project_dir  # noqa: E402

SETUP = "setup"
CALL = replay_gpu.CALL
DATA = "@DATA_DIR@"
WORK = DATA + "/setup-work"
# A 1.0 profile digest covers its paths, so the 1.0 records name a copy of the
# repositories next to the two data dirs: the same absolute path on both sides.
SHARED = "@SHARED@"
SECRET_ENV = "REPLAY_SETUP_SECRET"
_GIT_ENV = {"GIT_AUTHOR_NAME": "Replay", "GIT_AUTHOR_EMAIL": "replay@example.invalid",
            "GIT_COMMITTER_NAME": "Replay", "GIT_COMMITTER_EMAIL": "replay@example.invalid",
            "GIT_AUTHOR_DATE": "1700000000 +0000", "GIT_COMMITTER_DATE": "1700000000 +0000",
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}


def prepare_env() -> None:
    """A secret reference variable that is set (its value is never read).
    Provider CLIs come from the replay's sealed agent environment
    (replay_agents.agent_env: fake claude/codex/opencode/dsh/gemini with a fake
    home), so discover lists installed providers whose credentials are ready,
    unauthenticated or unknown, exactly as ProviderCatalog.summary() reports."""
    os.environ[SECRET_ENV] = "1"
    os.environ.pop("REPLAY_SETUP_UNSET", None)
    for name in ("DSH", "CLAUDE", "CODEX", "OPENCODE", "CURSOR", "GEMINI"):
        os.environ.pop(f"ARK_TASK_{name}_COMMAND", None)
    for name in ("ARK_TASK_CUSTOM_COMMAND", "ARK_TASK_SHELL_COMMAND"):
        os.environ.pop(name, None)


def _project(data_dir: Path) -> Path:
    return project_dir(data_dir)


def _setup_dir(data_dir: Path) -> Path:
    return _project(data_dir) / "setup"


def _git(cwd: Path, *args: str) -> None:
    subprocess.run(["git", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false",
                    "-c", "commit.gpgsign=false", *args],
                   cwd=str(cwd), check=True, capture_output=True, env={**os.environ, **_GIT_ENV})


def _write(path: Path, text) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(text if isinstance(text, bytes) else text.encode("utf-8"))


def _shared(data_dir: Path) -> Path:
    return Path(data_dir).parent / "setup-shared"


def _fill(value, data_dir: Path):
    """@DATA_DIR@ -> this data dir, @SHARED@ -> the shared copy, inside a JSON-like value."""
    text = json.dumps(value, ensure_ascii=False).replace(DATA, json.dumps(str(data_dir))[1:-1])
    text = text.replace(SHARED, json.dumps(str(_shared(data_dir)))[1:-1])
    return json.loads(text)


def make_repos(data_dir: Path) -> None:
    _make_repos(Path(data_dir) / "setup-work")
    if not _shared(data_dir).is_dir():
        _make_repos(_shared(data_dir))


def _make_repos(root: Path) -> None:
    alpha, beta = root / "alpha", root / "beta"
    _write(alpha / "pyproject.toml", "[tool.pytest.ini_options]\naddopts = '-q'\n")
    _write(alpha / "AGENTS.md", "# agents\n")
    _write(alpha / "README.md", "alpha\n")
    _write(alpha / "tests" / "test_x.py", "def test_x():\n    pass\n")
    _git(alpha, "init", "-q")
    _git(alpha, "add", "-A")
    _git(alpha, "commit", "-q", "-m", "alpha")
    _git(alpha, "remote", "add", "origin", "https://replay:tok3n@example.invalid/alpha.git")
    _git(alpha, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
    _write(alpha / ".git" / "info" / "exclude", "# local\n.worktrees/\n")
    _write(alpha / "untracked.txt", "dirty\n")
    _write(beta / "package.json", json.dumps({"scripts": {"test": "x", "build": "y", "lint": "z"}}))
    _write(beta / "Makefile", "all:\n\techo\ntest:\n\techo test\n")
    _write(beta / "Cargo.toml", "[package]\nname = 'beta'\n")
    _write(beta / "CLAUDE.md", "claude\n")
    _git(beta, "init", "-q")
    _git(beta, "add", "-A")
    _git(beta, "commit", "-q", "-m", "beta")
    _git(beta, "branch", "feature/x")
    wt = beta / ".git" / "worktrees" / "gone"
    _write(wt / "HEAD", "ref: refs/heads/feature/x\n")
    _write(wt / "commondir", "../..\n")
    _write(wt / "gitdir", str(root / "gone" / ".git").replace("\\", "/") + "\n")
    _write(root / "Weird Name!" / "x.txt", "x\n")
    _git(root / "Weird Name!", "init", "-q")
    _write(root / "team" / "agent_team_init.md", "team\n")
    _write(root / "team" / "sub" / "task_status.md", "nested\n")
    _write(root / "deep" / "a" / "b" / "c" / "d" / "task_status.md", "too deep\n")
    _write(root / "deep" / "a" / "setup_answers.yaml", "x: 1\n")
    _write(root / ".hidden" / "task_status.md", "hidden\n")
    _write(root / "plain" / "setup.py", "print(1)\n")
    _write(root / "plain" / "requirements.txt", "x\n")
    _write(root / "plain" / "tests" / "t.py", "")
    (root / "artifacts").mkdir(parents=True, exist_ok=True)


def call(fn) -> tuple:
    def run(*dirs):
        for d in dirs:
            fn(Path(d))
    return (CALL, "", "", run, {"X-Replay-Module": SETUP})


def clear(data_dir: Path) -> None:
    shutil.rmtree(_setup_dir(data_dir), ignore_errors=True)


def put(rel: str, content) -> tuple:
    """A CALL writing setup/<rel>: dict/list -> indented JSON (with
    @DATA_DIR@ filled in), str/bytes as is, a callable gets the data dir and
    returns the content; None deletes the file."""
    def write(data_dir: Path):
        target = _setup_dir(data_dir) / rel
        value = content(data_dir) if callable(content) else content
        if value is None:
            target.unlink(missing_ok=True)
            return
        if isinstance(value, (dict, list)):
            value = json.dumps(_fill(value, data_dir), indent=2, ensure_ascii=False) + "\n"
        _write(target, value)
    return call(write)


def edit(rel: str, fn) -> tuple:
    def write(data_dir: Path):
        target = _setup_dir(data_dir) / rel
        data = json.loads(target.read_text(encoding="utf-8"))
        fn(data)
        _write(target, json.dumps(data, indent=2, ensure_ascii=False) + "\n")
    return call(write)


def legacy_profile() -> dict:
    return {
        "objective": "Reproduce the 中文 baseline",
        "non_goals": ["no new datasets"],
        "repositories": [{"id": "alpha", "path": SHARED + "/alpha", "role": "primary",
                          "default_branch": "main", "instructions": ["AGENTS.md"]},
                         {"id": "beta", "path": SHARED + "/beta", "role": "code", "access": "read-only",
                          "default_branch": "nope"}],
        "task_spaces": [{"id": "team", "kind": "agent-team-folder", "location": SHARED + "/team"},
                        {"id": "gone", "kind": "other", "location": SHARED + "/missing"}],
        "compute": [{"id": "local", "kind": "local", "use": "any"},
                    {"id": "gpu-a", "kind": "ssh", "gpu_server": "a", "use": "ask"}],
        "environments": [{"id": "py", "kind": "venv", "repository": "alpha", "activate": "source .venv/bin/activate"}],
        "storage": [{"id": "art", "kind": "artifacts", "location": SHARED + "/artifacts"},
                    {"id": "logs", "kind": "logs", "location": SHARED + "/no-logs"}],
        "commands": [{"id": "unit", "run": "python -m pytest -q", "repository": "alpha", "environment": "py",
                      "gate": "unit"}],
        "secrets": [{"id": "s1", "ref": "env:" + SECRET_ENV, "purpose": "set"},
                    {"id": "s2", "ref": "env:REPLAY_SETUP_UNSET"}],
        "agents": {"coordinator": "dsh", "providers": ["dsh"], "concurrency": "unlimited",
                   "default_reviewer": "user"},
        "policies": {"push": "deny", "commit": "allow"},
        "cadence": {"status_minutes": 5, "task_trigger": "on request"},
        "task_ids": {"prefixes": ["EXP", "doc"]},
        "skipped": ["team"],
    }


def legacy_record(state, *, by: str, carried: bool = True, profile=None, **extra):
    """A 1.0 environment.json; carried=True records the digest of its own
    normalized profile (the migration then carries the confirmation)."""
    def build(data_dir: Path):
        from arkstudio.skills import setup as base
        raw = _fill(profile if profile is not None else legacy_profile(), data_dir)
        digest = base.digest(base.normalize_profile(raw)[0]) if carried else "0000"
        out = {"format": "ark-environment", "setup_version": "1.0", "state": state, "profile": raw,
               "updated_at": "2026-01-01T00:00:00+00:00", "updated_by": by, "machine": "",
               "confirmation": {"by": by, "at": "2026-01-01T00:00:00+00:00", "note": "ok", "digest": digest},
               "verification": {"ok": True, "at": "2026-01-01T00:00:00+00:00"}}
        out.update(extra)
        return json.dumps(out, indent=2, ensure_ascii=False) + "\n"
    return build


def user_layer(owner: str, *, resources=None, state: str = "draft", here: bool = False, **more) -> dict:
    res = resources if resources is not None else [
        {"id": "mine", "kind": "repository", "name": "My fork", "purpose": "fork",
         "details": {"role": "code", "access": "read-write", "default_branch": "main"}},
        {"id": "scratch", "kind": "storage", "details": {"kind": "other"}},
        {"id": "laptop-gpu", "kind": "compute", "details": {"kind": "local", "use": "any"}},
        {"id": "alpha", "kind": "secret", "details": {"ref": "env:" + SECRET_ENV}}]
    locs = [{"resource": f"u:{owner}/mine", "host_id": "@HOST@" if here else "elsewhere-1", "host_label": "Laptop",
             "path_or_ref": WORK + "/beta", "added_at": "2026-01-02T00:00:00+00:00", "added_by": owner},
            {"resource": f"u:{owner}/scratch", "host_id": "elsewhere-1", "path_or_ref": "s3://bucket/x",
             "added_at": "2026-01-01T00:00:00+00:00"},
            {"resource": "project/alpha", "host_id": "elsewhere-1", "path_or_ref": "D:/work/alpha",
             "added_at": "2026-01-03T00:00:00+00:00", "added_by": owner},
            {"resource": "x", "host_id": "bad host!"}, "not a dict"]
    return {"format": "ark-setup-layer", "setup_version": "2.0", "layer": "user", "owner": owner,
            "state": state, "profile": {"resources": res, "policies": {"push": "deny", "commit": "allow"},
                                        "notes": "my notes", "unknowns": ["where is data"]},
            "locations": locs, "hosts": [{"id": "elsewhere-1", "label": "Laptop"}, {"id": "bad host"}, 5,
                                         {"id": " spaced  "}],
            "updated_at": "2026-01-02T00:00:00+00:00", "updated_by": owner, **more}


def put_user(name: str, record) -> tuple:
    from arkstudio.skills.setup_layers import _user_slug
    return put(f"users/{_user_slug(name)}.json", record)


def here_host(data_dir: Path, rel: str) -> None:
    """@HOST@ in setup/<rel> -> this machine's fingerprint (both servers run here)."""
    from arkstudio.skills import setup as base
    target = _setup_dir(data_dir) / rel
    target.write_text(target.read_text(encoding="utf-8").replace("@HOST@", base.machine_fingerprint()),
                      encoding="utf-8")


def setup_steps(fixture: dict) -> list:
    h = {"X-Replay-Module": SETUP}
    tokens = fixture["tokens"]
    users = fixture.get("users") or []
    me = users[0] if users else "local"
    other = users[1] if len(users) > 1 else me
    project = {"X-ARK-Project": fixture["project"]} if users else {}
    nocookie = {**h, "X-Replay-NoCookie": "1"}
    srv = {**nocookie, "Authorization": "Bearer " + tokens["server"]}
    named = {**nocookie, "X-ARK-Token": tokens["server"], "X-Agent-Name": "setup 代理"}
    pw = {**nocookie, "Authorization": "Bearer " + tokens["personal_write"], **project}
    pr = {**nocookie, "Authorization": "bearer " + tokens["personal_read"], **project}
    route = r"regex:^/api/agent/v1/setup/([a-z][a-z-]*)$"
    state_r, agent_r = "exact:/api/skills/setup/state", "exact:/api/agent/v1/setup"
    overview_r = "exact:/api/project/overview"

    def post(action, body, headers=srv):
        return (route, "POST", f"/api/agent/v1/setup/{action}", body, headers)

    def reads(full=True):
        out = [(state_r, "GET", "/api/skills/setup/state", None, h),
               (agent_r, "GET", "/api/agent/v1/setup", None, srv),
               (agent_r, "GET", "/api/agent/v1/setup", None, pw),
               (overview_r, "GET", "/api/project/overview", None, h)]
        if full:
            out += [(state_r, "GET", "/api/skills/setup/state?host=laptop-1", None, {**h, "X-ARK-Host": " bad host "}),
                    (state_r, "GET", "/api/skills/setup/state?host=elsewhere-1", None, h),
                    (agent_r, "GET", "/api/agent/v1/setup?host=elsewhere-1", None, srv),
                    (agent_r, "GET", "/api/agent/v1/setup", None, {**pr, "X-ARK-Host": "elsewhere-1"}),
                    (agent_r, "GET", "/api/agent/v1/setup", None, named)]
        return out

    steps = [call(clear), call(make_repos)] + reads()
    # ---- discovery: read-only, nothing written
    paths = [WORK + "/alpha", WORK + "/beta", WORK + "/alpha/tests", WORK + "/alpha", WORK + "/team",
             WORK + "/plain", WORK + "/Weird Name!", WORK + "/deep", WORK + "/missing", WORK + "//beta/./",
             "~/ark-replay-missing-dir", 5, None, ""]
    steps += [post("discover", {}), post("discover", {}, pw), post("discover", {"layer": "project"}, pw),
              post("discover", {"paths": "not a list"}), post("discover", {"paths": [None, "", 0, False, "  "]}),
              post("discover", {"paths": paths}), post("discover", {"paths": paths, "host_id": "laptop-9"}, pw),
              post("discover", {"paths": [WORK] * 25}), post("discover", {"paths": [WORK + "/beta"]}, named),
              post("discover", {"layer": "team"}), post("discover", {"layer": 5}),
              post("discover", {"layer": "user", "owner": "someone-else"}, pw),
              post("discover", {"layer": " USER ", "owner": me.upper()}, pw),
              post("discover", {"paths": [WORK + "/alpha"]}, {**srv, "X-ARK-Host": "box-2"})]
    steps += [replay_gpu.config("enabled: true\nservers:\n  - name: a\n    host: a.fake\n    tags: [4090, x]\n"
                                "  - name: B Server\n    host: b.fake\n"),
              post("discover", {"paths": [WORK + "/alpha"]}),
              replay_gpu.config("enabled: true\nservers: []\n"), post("discover", {}),
              replay_gpu.config("enabled: [\n"), post("discover", {}), replay_gpu.config(None)]
    # ---- before any record: lifecycle errors on an empty register
    steps += [post("propose", {}), post("propose", {}, pw), post("propose", {"layer": "project"}, pw),
              post("confirm", {"digest": ""}), post("confirm", {"digest": "x"}, pw),
              post("verify", {}), post("verify", {}, pw), post("host", {"label": "x"}),
              post("location", {"resource": "project/none", "path_or_ref": "/x"}),
              post("bogus", {}), post("resource", {}), post("resource", {"resource": "x"}),
              post("remove", {}), post("remove", {"id": "  "})] + reads(False)
    # ---- drafts of the project layer (server token = project layer)
    draft = {"profile": {
        "objective": "Ship the 中文 setup", "non_goals": ["x", "", None],
        "repositories": [{"id": "Alpha", "path": WORK + "/alpha", "role": "primary", "default_branch": "main",
                          "remote": "git@example.invalid:me/alpha.git", "instructions": "AGENTS.md"},
                         {"id": "beta", "path": "https://example.invalid/beta.git", "role": "code"}],
        "task_spaces": [{"kind": "agent-team-folder", "path": WORK + "/team"}],
        "compute": [{"id": "local", "kind": "local", "use": "any"}, {"id": "gpu-a", "kind": "ssh", "gpu_server": "a"}],
        "storage": [{"id": "art", "kind": "artifacts", "location": WORK + "/artifacts"},
                    {"id": "logs", "kind": "logs", "location": WORK + "/no-such"}],
        "commands": [{"id": "unit", "run": "python -m pytest -q", "repository": "alpha", "gate": "unit"},
                     {"id": "lint", "run": "ruff check .", "gate": "static", "mandatory": None}],
        "secrets": [{"id": "s1", "ref": "env:" + SECRET_ENV}, {"id": "s2", "ref": "env:REPLAY_SETUP_UNSET"}],
        "agents": {"coordinator": "codex", "providers": "codex", "concurrency": "8", "default_context_tokens": 1e9},
        "policies": {"push": "Deny", "bogus": "allow", "gpu_dispatch": "deny"},
        "cadence": {"status_minutes": "0", "task_trigger": "always"},
        "task_ids": {"prefixes": ["exp", "A-B", "bad prefix", "ß"]},
        "unknowns": [{"field": "data", "blocks": "x"}, "budget", 5],
        "skipped": ["compute", "Compute", "nope"],
        "notes": "n" * 5000}}
    steps += [post("draft", draft), post("draft", {**draft, "propose": True}), post("draft", {"profile": {}})]
    steps += reads()
    steps += [post("propose", {}), post("propose", {"layer": "project"}, pw)]
    fixed = json.loads(json.dumps(draft))
    fixed["profile"]["task_ids"] = {"prefixes": ["EXP"]}
    fixed["profile"]["skipped"] = ["team"]
    fixed["profile"]["agents"]["concurrency"] = "unlimited"
    steps += [post("draft", {**fixed, "propose": 1}), post("propose", {}),
              post("draft", {**fixed, "propose": True, "host_label": "Server box"})] + reads()
    # Secrets are refused before anything is written.
    for bad in ({"profile": {"notes": "token sk-abcdefghijklmnopqrstuvwxyz"}},
                {"profile": {"secrets": [{"id": "k", "ref": "ghp_" + "a" * 30}]}},
                {"resources": [{"kind": "storage", "id": "db", "password": "hunter2"}]},
                {"profile": {"repositories": [{"path": "https://u:p@example.invalid/r.git"}]}},
                {"profile": {"api_key": "env:OK_REF", "credential": "plain"}}):
        steps.append(post("draft", bad))
    # 2.0 resources, merging and deletion; locations and hosts.
    steps += [post("resource", {"resource": {"kind": "repositories", "id": "gamma", "path": WORK + "/beta",
                                             "name": "Gamma", "purpose": "docs", "role": "website"}}),
              post("resource", {"resource": {"kind": "data", "id": "x"}}),
              post("resource", {"resource": {"type": "task-space", "id": "space2", "location": "jira:ARK",
                                             "subkind": "issue-tracker"}}),
              post("resource", {"resource": {"kind": "storage", "id": "bucket", "details": {"kind": "datasets"},
                                             "path_or_ref": "s3://data/x", "owner": "Someone"}}),
              post("resource", {"resource": {"kind": "compute", "id": "Bad ID!", "details": {"kind": "cloud"}}}),
              post("remove", {"id": "gamma"}), post("remove", {"id": "not-there"}),
              post("draft", {"resources": [{"kind": "command", "id": "smoke", "run": "make smoke", "gate": "component"}],
                             "delete": ["lint"], "locations": [
                                 {"resource": "project/art", "host_id": "laptop-1", "path_or_ref": "~/art"},
                                 {"resource": "", "host_id": "", "path_or_ref": ""}, "bad"]}),
              post("location", {"resource": "project/alpha", "path_or_ref": "C:/elsewhere/alpha", "host_id": "laptop-1",
                                "host_label": "Laptop 1"}),
              post("location", {"resource": "project/alpha", "path": WORK + "/alpha"}),
              post("location", {"resource": "project/alpha", "path_or_ref": ""}),
              post("location", {"resource": "project/alpha", "path_or_ref": "https://u:pw@example.invalid/x"}),
              post("location", {"resource": "project/nope", "path_or_ref": "/x"}),
              post("location", {"resource": "project/alpha", "path_or_ref": "D:/a", "scope": "project"}, pw),
              post("location", {"resource": "project/alpha", "path_or_ref": "D:/mine/alpha", "host_id": "laptop-1"}, pw),
              post("location", {"resource": "project/alpha", "path_or_ref": "D:/mine/alpha"}, pr),
              post("host", {"label": "Server"}), post("host", {"label": "My laptop", "id": "laptop-1"}, pw),
              post("host", {"label": "", "id": "bad id!"}, pw)] + reads()
    # The caller's own user layer (personal tokens).
    steps += [post("draft", {"profile": {"objective": "ignored here", "policies": {"push": "allow", "merge": "deny",
                                                                                 "x": "ask", "commit": "maybe"},
                                         "notes": "mine"},
                             "resources": [{"kind": "repository", "id": "fork", "path": WORK + "/beta"}],
                             "hosts": [{"id": "laptop-1", "label": "Laptop"}, {"id": "bad host"}]}, pw),
              post("draft", {"layer": "user"}), post("draft", {"layer": "user", "owner": other}, pw),
              post("draft", {"profile": {"policies": {"merge": "deny"}}, "propose": True}, pw),
              post("propose", {"layer": "user"}, pw), post("propose", {}, pr), post("draft", {}, pr),
              post("confirm", {}, pw), post("confirm", {"layer": "user", "digest": "nope"}, pw)] + reads()
    # The human gate: every agent token is refused, whatever the digest.
    steps += [post("confirm", {"digest": "0" * 16}), post("confirm", {"layer": "project", "digest": "x"}, named),
              post("confirm", {"layer": "user"}, pw), post("confirm", {"layer": "nope"})]
    # Verify on the server host (non-admin legacy token) and from another host.
    steps += [post("verify", {}), post("verify", {}, {**srv, "X-ARK-Host": "laptop-1"}), post("verify", {}, pw),
              post("verify", {"host_id": "laptop-1"}, pw)] + reads(False)
    # ---- 1.0 environment.json: migration with a carried confirmation
    legacy_by = "human:owner" if users else "human:local"
    steps += [call(clear), put("environment.json", legacy_record("confirmed", by=legacy_by))] + reads()
    steps += [post("verify", {}), post("verify", {}, pw)] + reads(False)
    # Edited after confirmation -> stale; propose again.
    steps += [edit("project.json", lambda d: d["profile"].__setitem__("objective", "edited by hand")),
              (agent_r, "GET", "/api/agent/v1/setup", None, srv), post("propose", {}),
              (agent_r, "GET", "/api/agent/v1/setup", None, srv)]
    # Moved: the recorded location is for another host -> known but inaccessible.
    steps += [edit("project.json", lambda d: [loc.__setitem__("host_id", "old-box") for loc in d["locations"]])]
    steps += reads()
    # Unreadable layer files.
    steps += [put("project.json", "{not json"), (agent_r, "GET", "/api/agent/v1/setup", None, srv),
              post("propose", {}), put("project.json", b"\xef\xbb\xbf{}"), (state_r, "GET", "/api/skills/setup/state", None, h),
              put("project.json", "[1, 2]"), (state_r, "GET", "/api/skills/setup/state", None, h),
              put("project.json", b"\xff\xfe"), (state_r, "GET", "/api/skills/setup/state", None, h)]
    # A second migration with a backup already present (timestamped name);
    # a proposed (not carried) 1.0 record and an invalid one.
    steps += [put("project.json", None), put("environment.json", legacy_record("proposed", by="agent:x", carried=False)),
              (agent_r, "GET", "/api/agent/v1/setup", None, srv), put("project.json", None),
              put("environment.json", legacy_record("verified", by="human:nobody",
                                                     profile={"repositories": [{"id": "BAD ID"}], "skipped": ["x"]})),
              (agent_r, "GET", "/api/agent/v1/setup", None, srv), put("project.json", None),
              put("environment.json", "{broken"), (agent_r, "GET", "/api/agent/v1/setup", None, srv),
              put("project.json", None), put("environment.json", "[]"), (state_r, "GET", "/api/skills/setup/state", None, h)]
    # A 1.0 record whose every mandatory check passes here: verify -> verified.
    passing = {"objective": "pass", "repositories": [{"id": "alpha", "path": SHARED + "/alpha", "role": "primary",
                                                      "default_branch": "main"}],
               "commands": [{"id": "unit", "run": "pytest", "gate": "unit"}], "compute": [{"id": "local"}],
               "storage": [{"id": "art", "kind": "artifacts", "location": SHARED + "/artifacts"}]}
    steps += [call(clear), put("environment.json", legacy_record("confirmed", by=legacy_by, profile=passing)),
              post("verify", {}, {**srv, "X-ARK-Host": "laptop-1"}), post("verify", {}),
              post("verify", {}, pw)] + reads(False)
    # ---- user layers written by hand
    steps += [put_user(me, user_layer(me, here=True)), call(lambda d: here_host(d, users_rel(me))),
              put_user(other, user_layer(other, state="confirmed")),
              put_user("mallory", user_layer("someone-else")),
              put("users/stray.json", user_layer("stray")),
              put("users/not-json.json", "{"), put("users/list.json", "[]"),
              put_user("sec", user_layer("sec", resources=[{"kind": "secret", "id": "k", "details": {"ref": "plain-value"}},
                                                         {"kind": "storage", "id": "t", "token": "abc"}]))] + reads()
    steps += [post("verify", {}, pw), post("propose", {"layer": "user"}, pw), post("host", {"label": "Desk"}, pw),
              post("draft", {"delete": ["scratch"]}, pw)] + reads(False)
    # Too many resources: the GET reads fail like Python (500), the POST maps to 400.
    many = {"format": "ark-setup-layer", "state": "draft",
            "profile": {"resources": [{"kind": "storage", "id": f"s{i}"} for i in range(201)]}}
    steps += [put("project.json", many), (state_r, "GET", "/api/skills/setup/state", None, h),
              post("draft", {}), post("resource", {"resource": {"kind": "storage", "id": "one"}}),
              put("project.json", None)] + reads(False)
    # Request-level guards on the action route.
    steps += [post("draft", "[1]"), post("draft", "{"), post("draft", "not json"),
              post("discover", {}, {**srv, "Origin": "http://evil.example"}),
              post("discover", {}, {**nocookie, "Authorization": "Bearer " + tokens["revoked"]}),
              post("discover", {}, {**nocookie, "Authorization": "Bearer " + tokens["expired"]}),
              post("discover", {}, {**nocookie, "Authorization": "Bearer wrong"}),
              post("discover", {}, nocookie), post("discover", {}, h),
              post("discover", {}, {**nocookie, "Authorization": "Bearer " + tokens["read_token"]}),
              post("confirm", {}, {**nocookie, "Authorization": "Bearer " + tokens["full_token"]}),
              post("draft-x", {}), post("x", {}), (route, "POST", "/api/agent/v1/setup/draft?x=1", {}, srv),
              (route, "GET", "/api/agent/v1/setup/draft", None, srv)]
    # The final register stays for the state comparison.
    return steps + reads(False)


def users_rel(name: str) -> str:
    from arkstudio.skills.setup_layers import _user_slug
    return f"users/{_user_slug(name)}.json"
