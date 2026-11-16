"""Build a contract-test data directory with the Python reference implementation.

    python -B tests/python/contract/fixture.py <data-dir> [--auth N] [--nodes N]

Layout (the same one ``server-init`` produces)::

    <data-dir>/projects/<slug>.ark   one project: the demo seed plus trap data
    <data-dir>/org.sqlite3           organization (only with --auth)
    <data-dir>/private/              ARK_PRIVATE_DIR for both servers

Creating fixtures through the Python store is deliberate: the Python server is
the reference, and both servers are then started on byte-identical copies.
The trap units exercise the translation hazards listed in docs/development/go-port/README.md
(code points vs bytes, Unicode whitespace/digits, JSON floats, key order,
control characters, assignee inheritance, topic rules, borrowed media).

``--auth N`` creates an owner ``owner`` and N members ``user0001..`` with a
cheap PBKDF2 hash (1000 iterations, password ``contract-password``) and a
write grant on the project, so load tests can sign in hundreds of users.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT))

PASSWORD = "contract-password"
PROJECT_NAME = "Contract Fixture"


def _trap_units(store) -> None:
    long_cn = "这是一个很长的中文段落，用来检查按码点截断而不是按字节截断。" * 12
    a = store.create_unit({
        "type": "claim", "title": "中文标题 🚀 emoji 节点",
        "body": f"# 标题\n\n{long_cn}\n\n```python\nprint('fenced')\n```\n\n"
                "１. 全角数字列表\n 　 indented with NBSP and ideographic space\n"
                "![图](assets/x.png) and [链接](https://example.com) **粗体** `code` ~~del~~\n"
                "| a | b |\n\x1c\x1d separators \x01 control \x7f del   line sep\n",
        "tags": ["status:review", "area:模型", "seq:2"],
        "meta": {"score": 1.0, "big": 1e16, "small": 1e-05, "neg": -0.0, "int": 10,
                 "nested": {"z": 1, "a": [1.5, None, True, "é "]}, "text": "<&>'\""},
        "start_at": "2026-01-02", "end_at": "2026-03-04",
    }, actor="human:ui")
    b = store.create_unit({
        "type": "evidence", "title": "Evidence with media",
        "body": "Short body.",
        "tags": ["status:done", "area:模型"],
        "media": [{"url": "assets/fig.png", "kind": "image", "caption": "Figure"},
                  {"url": "https://example.com/v.mp4?x=1", "kind": "video", "caption": "Video"},
                  {"url": "assets/fig.png", "kind": "image", "caption": "Duplicate"}],
    }, actor="human:ui")
    store.create_link(b["id"], a["id"], "supports", "evidence for the claim", "human:ui")
    parent = store.create_unit({"type": "milestone", "title": "Parent owned by local",
                                "assignee": "local", "tags": ["status:active"]}, actor="human:ui")
    child = store.create_unit({"type": "task", "title": "Child inherits assignee",
                               "assignee": None, "tags": ["status:open", "importance:p0"],
                               "end_at": "2020-01-01", "start_at": "2019-12-01"}, actor="human:ui")
    store.create_link(child["id"], parent["id"], "part_of", "", "human:ui")
    grandchild = store.create_unit({"type": "decision", "title": "Decide: grandchild",
                                    "assignee": None, "body": "Should we? " + "很" * 500},
                                   actor="human:ui")
    store.create_link(grandchild["id"], child["id"], "part_of", "", "human:ui")
    c1 = store.add_comment(a["id"], "First comment 第一条", "comment", author="local")
    store.add_comment(a["id"], "Agent answer", "agent_reply", reply_to=c1["id"],
                      actor="agent:contract")
    c3 = store.add_comment(grandchild["id"], "Please decide", "comment", author="local")
    store.resolve_comment(grandchild["id"], c3["id"], True, author="local")
    store.create_unit({"type": "topic", "title": "Topic with a rule",
                       "design": {"rule": {"tags": ["area:模型"]}}}, actor="human:ui")
    gone = store.create_unit({"type": "idea", "title": "Archived idea"}, actor="human:ui")
    store.archive_unit(gone["id"], actor="human:ui")
    store.create_unit({"type": "note", "title": "Title with \"quotes\" and \\ backslash\ttab",
                       "body": "line1\r\nline2\n\n---\n\n$$\n\ttabbed"}, actor="human:ui")


# A 1x1 PNG (valid header and IHDR, so org.set_avatar_image accepts it).
TINY_PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d4948445200000001000000010806000000"
    "1f15c4890000000d49444154789c6360000002000154a24f2e0000000049454e44ae426082")


def _media_files(store, folder: Path, outside: Path) -> None:
    """Files and media rows for /api/media/{id}/raw and Range edge cases."""
    media = folder / "assets" / "media-range"
    media.mkdir(parents=True, exist_ok=True)
    clip = bytes((i * 7 + 3) % 256 for i in range(100_000))
    files = {"clip.mp4": clip, "pic.png": TINY_PNG, "doc.pdf": b"%PDF-1.4\n" + clip[:3000],
             "notes.txt": "plain 文本 😀\n".encode("utf-8") * 50, "data.csv": b"a,b\n1,2\n",
             "page.html": b"<script>alert(1)</script>", "图表 😀.png": TINY_PNG,
             "empty.bin": b"", "vector.svg": b"<svg xmlns='http://www.w3.org/2000/svg'/>"}
    for name, data in files.items():
        (media / name).write_bytes(data)
    outside.mkdir(parents=True, exist_ok=True)
    (outside / "secret.txt").write_bytes(b"outside the project")
    host = store.create_unit({"type": "artifact", "title": "Media host 媒体 🎬",
                              "body": "Range fixtures"}, actor="human:ui")
    rows = [(f"assets/media-range/{name}", "image" if name.endswith(".png") else "link", name)
            for name in files]
    rows += [((media / "clip.mp4").resolve().as_uri(), "video", "file uri inside"),
             ((outside / "secret.txt").resolve().as_uri(), "link", "file uri outside"),
             ("../outside-escape.txt", "link", "dot-dot"),
             ("https://example.com/remote.png", "image", "remote"),
             ("assets/media-range/missing.png", "image", "missing")]
    ids = {}
    for url, kind, caption in rows:
        ids[caption] = store.add_media(host["id"], url, kind, caption, "human:ui")["id"]
    claim = store.list_units({"limit": 1, "types": ["claim"], "sort": "created"})["items"][0]
    fence = "`" * 3
    body = "\n".join([
        f"Mentions [[{host['uid']}]], \\[[ESCAPED-1]], [[{claim['uid']}|Label 中]] and [[NOPE-99]].",
        f"![shot](media:{ids['pic.png']}) and [doc](assets/media-range/doc.pdf#page=2)",
        f"![clip](media:{ids['clip.mp4']})",
        f"> ![quoted clip](/api/media/{ids['clip.mp4']}/raw)",
        f'<img src="media:{ids["pic.png"]}"> <a href=\'assets/media-range/notes.txt\'>notes</a> <x src="unclosed',
        f"[space](assets/media-range/图表 😀.png) [ws](/api/workspace/raw?path=assets%2Fmedia-range%2Fdata.csv)",
        "[abs](/some/abs) [ext](https://example.com/x.mp3) [up](../../escape.txt) `[[NOT-1]] media:1`",
        f"see media:{ids['notes.txt']} here; also /api/media/{ids['data.csv']}/rawish",
        fence + "md", "[[INSIDE-1]] ![x](media:1)", fence, "~~~~", "~~~ not closing", "~~~~",
        f"[remote](media:{ids['remote']}) [outside](media:{ids['file uri outside']}) [miss](media:{ids['missing']})",
    ])
    trap = store.create_unit({"type": "note", "title": "Bundle trap 打包", "body": body,
                              "tags": ["area:模型/x", "seq:9"], "source_ref": "ref:1"}, actor="human:ui")
    store.create_link(trap["id"], host["id"], "uses", "trap uses host", "human:ui")
    store.create_link(host["id"], claim["id"], "supersedes", "", "human:ui")


def _topic_design(store) -> None:
    """A topic with a rule plus include and exclude members; default topics."""
    units = store.list_units({"limit": 12, "sort": "created"})["items"]
    topic = store.create_unit({"type": "topic", "title": "Curated 话题 ✨",
                               "design": {"rule": {"types": ["claim", "evidence"]}}},
                              actor="human:ui")
    store.design_topic(topic["id"], {"add": [{"id": u["id"]} for u in units[:4]]}, "human:ui")
    claims = [u for u in units if u["type"] in ("claim", "evidence")]
    if claims:
        store.design_topic(topic["id"], {"exclude": [{"id": claims[0]["id"]}]}, "human:ui")
    store.ensure_default_topics()


def _session(sessions_root: Path, summary: dict, actor: str, *, access_mode: str = "read_only",
             token: str = "rt_contract_read_token_0123456789abcdef",
             folder_name: str = "s_20260101000000abc123") -> str:
    """A finished task-agent session with a read token and context snapshot."""
    folder = sessions_root / folder_name
    (folder / "workspace").mkdir(parents=True, exist_ok=True)
    meta = {"id": folder.name, "title": "Contract session 会话", "status": "stopped",
            "platform": "codex", "agent": "codex", "task_ref": "T-0001",
            "created_at": "2026-01-01T00:00:00+00:00",
            "last_activity_at": "2026-01-01T00:05:00+00:00", "read_token": token,
            "actor": actor, "access_mode": access_mode,
            "project": {"id": summary["id"], "name": summary["name"], "path": summary["path"]}}
    (folder / "meta.json").write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n",
                                      encoding="utf-8")
    (folder / "workspace" / "ark-context.json").write_text(
        json.dumps({"format": "ark-task-agent-context", "note": "snapshot 快照"}, ensure_ascii=False),
        encoding="utf-8")
    return token


def _org_extras(org, summary: dict, users: list[str]) -> dict:
    """Teams, grants, a linux account, an avatar and personal agent tokens."""
    tokens = {}
    owner = "owner" if org.store.user_by_name("owner") else "local"
    if len(users) >= 2:
        team = org.create_team(owner, {"name": "Team 团队", "description": "contract"})
        org.add_team_member(owner, team["id"], {"user_id": org.store.user_by_name(users[1])["id"]})
        org.store.set_team_grant(summary["id"], team["id"], "read", granted_by="")
        org.store.add_linux_account(org.store.user_by_name(users[0])["id"], "alice", "")
        org.set_avatar_image(users[0], TINY_PNG, "image/png")
    first = users[0] if users else "local"
    second = users[1] if len(users) >= 2 else first
    tokens["personal_write"] = org.create_agent_token(first, {"label": "write 令牌", "scope": "write"})["token"]
    tokens["personal_read"] = org.create_agent_token(second, {"label": "read", "scope": "read"})["token"]
    revoked = org.create_agent_token(first, {"label": "revoked", "scope": "write"})
    org.revoke_agent_token(first, revoked["item"]["id"])
    tokens["revoked"] = revoked["token"]
    expired = org.create_agent_token(first, {"label": "expired", "scope": "write", "expires_days": 30})
    org.store._write("UPDATE agent_tokens SET expires_at=? WHERE id=?",
                     ("2020-01-01T00:00:00+00:00", expired["item"]["id"]))
    tokens["expired"] = expired["token"]
    return tokens


def _arrow_settings(project) -> None:
    """Stored arrow settings with invalid parts: the lenient read path drops them."""
    from arkstudio import db
    manifest = dict(project.manifest)
    manifest.setdefault("settings", {})["arrows"] = {
        "style": {"head": "diamond", "placement": "bogus", "size": "1.23456"},
        "relations": {"supports": {"head": "open", "size": 9}, "nope": {"head": "bar"},
                      "cites": {}, "relates_to": {"placement": "both"}},
        "mind_tree": 1,
    }
    db.write_manifest(project.path, manifest)
    project.manifest = manifest


def _appearance_prefs(org, username: str) -> None:
    """A stored personal override: valid arrows, an invalid graph group."""
    prefs = {"lang": "zh", "appearance": {
        "arrows": {"style": {"head": "stealth", "placement": "both", "size": 1.75},
                   "relations": {"blocks": {"head": "bar"}}, "mind_tree": True},
        "graph": {"repulsion": 1}}}
    org.store._write("UPDATE users SET prefs_json=? WHERE username=?",
                     (json.dumps(prefs, ensure_ascii=False), username))


def _chat_history(store, users: list[str]) -> None:
    """Assistant chat rows: shared (written before per-user history) and per user."""
    store.add_chat("user", "shared question 共享", meta={"source": "ui"})
    store.add_chat("assistant", "shared answer", meta={"source": "ui"})
    for name in ["local"] + list(users[:2]):
        store.add_chat("user", f"question from {name}", meta={"source": "ui"}, user=name)
        store.add_chat("assistant", f"answer for {name}", tools=[{"name": "x"}], user=name)


def _git_workspace(folder: Path) -> None:
    """The project folder as a Git working tree for the Editor's Changes view:
    one commit (fixed identity and dates), then modified, deleted, untracked,
    binary, BOM and non-UTF-8 files."""
    import os
    import subprocess
    files = {
        ".gitignore": b"history.git/\nlogs/\nexports/\n",
        "src/tracked.log": b"line tracked but ignored later\n",
        "src/main.py": b"def main():\n    print('hi')\n\n\nif __name__ == '__main__':\n    main()\n",
        "src/util.py": "".join(f"line {i} 行\n" for i in range(40)).encode(),
        "docs/guide.md": "# Guide 指南\n\nSome text.\n".encode(),
        "docs/old.md": b"old\ncontent\n",
        "data/blob.bin": b"\x00\x01\x02binary",
        "crlf.txt": b"one\r\ntwo\r\n",
    }
    for rel, data in files.items():
        (folder / rel).parent.mkdir(parents=True, exist_ok=True)
        (folder / rel).write_bytes(data)
    null = "nul" if os.name == "nt" else "/dev/null"
    env = {k: v for k, v in os.environ.items() if not k.upper().startswith("GIT_")}
    env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": null,
                "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@localhost",
                "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@localhost",
                "GIT_AUTHOR_DATE": "1767225600 +0000", "GIT_COMMITTER_DATE": "1767225600 +0000"})

    def git(*args):
        subprocess.run(["git", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false", *args],
                       cwd=folder, env=env, check=True, capture_output=True)

    git("init", "-q")
    git("add", *files)
    git("commit", "-q", "-m", "fixture")
    (folder / "src/util.py").write_bytes(
        "".join(f"line {i} 行\n" for i in range(40) if i not in (20, 21))
        .replace("line 5 行", "line five").encode() + b"appended\n")
    (folder / "docs/old.md").unlink()
    # Ignored after the commit: Workbench Search skips ignored files but keeps
    # tracked ones (src/tracked.log).
    with (folder / ".gitignore").open("ab") as handle:
        handle.write(b"*.log\nignored/\n")
    (folder / "ignored").mkdir()
    (folder / "ignored" / "x.txt").write_bytes(b"line ignored folder\n")
    (folder / "notes").mkdir(exist_ok=True)
    (folder / "notes" / "skip.log").write_bytes(b"line ignored file\n")
    (folder / "data/blob.bin").write_bytes(b"\x00\x01changed")
    extra = {"notes/new.md": "new 新\nfile\n".encode(), "bin2.dat": b"ab\x00cd", "empty.txt": b"",
             "bom.txt": b"\xef\xbb\xbfbom text\n", "latin1.txt": b"caf\xe9\n"}
    for rel, data in extra.items():
        (folder / rel).parent.mkdir(parents=True, exist_ok=True)
        (folder / rel).write_bytes(data)
    # Windows may apply a deleted entry's folder timestamp late; pin the
    # folders so every copy of the fixture lists the same mtimes.
    for rel in ("src", "docs", "data", "notes", "ignored", "."):
        os.utime(folder / rel, ns=(1767225600 * 10 ** 9, 1767225600 * 10 ** 9))


MARKER = ".contract-fixture-project"


def project_dir(data_dir) -> Path:
    """The fixture's own project folder (replays create more projects, e.g.
    "5.ark", which sort before it)."""
    projects = Path(data_dir) / "projects"
    marker = Path(data_dir) / MARKER
    if marker.is_file():
        return projects / marker.read_text(encoding="utf-8").strip()
    return next(projects.glob("*.ark"))


def build(data_dir: Path, *, auth: int = 0, nodes: int = 0) -> dict:
    from arkstudio import auth as auth_mod
    from arkstudio.org import OrgManager
    from arkstudio.projects import ProjectManager

    data_dir.mkdir(parents=True, exist_ok=True)
    (data_dir / "private").mkdir(exist_ok=True)
    manager = ProjectManager(data_dir / "projects", seed_demo=False)
    project = manager.create(PROJECT_NAME, template="demo", demo=True,
                             description="Fixture for Python/Go contract tests")
    store = project.store
    store.creator_resolver = lambda: "local"
    _trap_units(store)
    _media_files(store, project.path, data_dir / "outside")
    _topic_design(store)
    if nodes:
        for i in range(nodes):
            store.create_unit({"type": ("task", "idea", "claim", "evidence")[i % 4],
                               "title": f"Load node {i:05d} 负载",
                               "body": f"Body {i} " + "text " * (i % 40),
                               "tags": [f"area:a{i % 7}", ("status:open", "status:review",
                                                           "status:done")[i % 3]]},
                              actor="system:seed")
    _arrow_settings(project)
    summary = project.summary()
    (Path(data_dir) / MARKER).write_text(Path(summary["path"]).name, encoding="utf-8")
    # One black-box snapshot, so neither server takes a "startup" snapshot at
    # a wall-clock second of its own (the replay enables the black box).
    from arkstudio.blackbox import BlackBox
    BlackBox(data_dir / "blackbox").snapshot(project, "fixture", force=True)
    _git_workspace(project.path)
    _chat_history(project.store, [f"user{i:04d}" for i in range(1, auth + 1)])
    project.close()
    users = []
    org = OrgManager(data_dir / "org.sqlite3", private_dir=data_dir / "private" / "org")
    if auth:
        cheap = auth_mod.hash_password(PASSWORD, iterations=1000)
        org.bootstrap(owner_username="owner", owner_password_hash=cheap, projects=[summary])
        for i in range(1, auth + 1):
            name = f"user{i:04d}"
            row = org.store.insert_user(name, cheap, display_name=f"User {i}")
            org.store.set_membership(row["id"], "member", org.org_id)
            org.store.set_project_grant(summary["id"], row["id"], "write", granted_by=row["id"])
            users.append(name)
    else:
        org.bootstrap(projects=[summary])
    tokens = _org_extras(org, summary, users)
    _appearance_prefs(org, users[0] if users else "local")
    org.close()
    tokens["read_token"] = _session(data_dir / "agent-sessions", summary,
                                    users[0] if users else "local")
    # A full-access session: the task-agent GPU queue routes (replay_services.py).
    tokens["full_token"] = _session(data_dir / "agent-sessions", summary, users[0] if users else "local",
                                    access_mode="full_access", token="rt_contract_full_token_0123456789abcdef",
                                    folder_name="s_20260101000001def456")
    import replay_sessions  # crafted finished sessions for the agent-sessions replay
    seeded = replay_sessions.seed_sessions(data_dir / "agent-sessions", summary, users[0] if users else "local",
                                           users[1] if len(users) > 1 else "someone-else")
    import replay_sessionctx  # stored external transcripts and an internal mirror
    contexts = replay_sessionctx.seed_contexts(Path(summary["path"]), users[0] if users else "local",
                                               users[1] if len(users) > 1 else "someone-else")
    tokens["server"] = (data_dir / "projects" / ".agent-token").read_text(encoding="utf-8").strip()
    return {"data_dir": str(data_dir), "project": summary["id"], "folder": summary["folder"],
            "users": users, "owner": "owner" if auth else None, "password": PASSWORD,
            "tokens": tokens, "seeded_sessions": seeded,
            "seeded_contexts": contexts}


def main(argv=None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("data_dir")
    parser.add_argument("--auth", type=int, default=0)
    parser.add_argument("--nodes", type=int, default=0)
    args = parser.parse_args(argv)
    print(json.dumps(build(Path(args.data_dir), auth=args.auth, nodes=args.nodes)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
