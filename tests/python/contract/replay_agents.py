"""Replay steps for the provider catalog and agent worktrees (providers.py, worktrees.py).

Both servers get the same hermetic agent-CLI environment (``agent_env``):

* PATH = a fake-CLI folder + Git + the system folders.  The fakes answer
  ``--version`` and the read-only model-list commands from fixture files, so
  no real provider CLI is ever started.  ``cursor-agent`` is absent (missing),
  ``gemini`` exits 3 (version probe failure).
* HOME / USERPROFILE / APPDATA = a fake home with provider credential and
  config files (only their structure is read by the servers).
* Every provider credential variable is removed; DEEPSEEK_BASE_URL points at a
  local fake OpenAI-compatible ``/models`` endpoint served by this process
  (the explicit refresh lists DSH models from it), DEEPSEEK_API_KEY is fake.

HOOK steps run a setup function on both data dirs between requests (they
are not compared): ``worktree_clean`` makes the fixture's Git working tree
clean (tracked files restored, untracked files excluded) so a worktree can be
created; ``worktree_dirty`` adds an untracked file inside a worktree.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HOOK = "__hook__"

CREDENTIAL_ENV = (
    "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY",
    "TENCENTHUB_API_KEY", "DEEPSEEK_API_KEY", "DSH_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
    "OPENROUTER_API_KEY", "GROQ_API_KEY", "CURSOR_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
    "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CODEX_HOME", "OPENCODE_CONFIG",
    "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "ARK_DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL",
    "DEEPSEEK_MODEL", "DEEPSEEK_REASONING_EFFORT", "DEEPSEEK_TIMEOUT", "ARK_AGENT_WORKTREE_ROOT",
    "ARK_AGENT_WORKTREE_BRANCH_PREFIX",
)

CODEX_BUNDLED = {"models": [
    {"slug": "gpt-5.5", "display_name": "GPT 5.5 \u6a21\u578b", "priority": 2,
     "supported_reasoning_levels": [{"effort": "low"}, {"effort": "High"}, {"effort": "bad level!"}],
     "default_reasoning_level": "high"},
    {"slug": "hidden-model", "visibility": "hide"},
    {"id": "gpt-6", "priority": "1"},
    {"slug": "sk-abcdefghijklmnopqrstuvwxyz012345"},
    {"slug": "gpt-x", "priority": None, "supported_reasoning_levels": "low"},
    {"slug": " spaced "}, "not a row",
]}
CODEX_LIVE = {"models": [{"slug": "gpt-7-preview", "priority": 0.5}, {"slug": "gpt-5.5", "priority": 1}]}
OPENCODE_MODELS = ("anthropic/claude-sonnet-4-5\nopenai/gpt-5\n\x1b[1mbold header\x1b[0m\n"
                   "local/llama-3\nnot-a-model\nopenai/gpt-5\n")
HOME_FILES = {
    ".claude/.credentials.json": json.dumps({"claudeAiOauth": {"accessToken": "x", "refreshToken": "y"}}),
    ".claude/settings.json": '// comment line\n{"model": "opus", "effortLevel": "High",\n'
                             ' "env": {"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-x", "ANTHROPIC_AUTH_TOKEN": "t"}}\n',
    ".codex/config.toml": 'model = "gpt-5.5"\nmodel_reasoning_effort = "medium" # note\napi_key = "sk-secret"\n'
                          '[profiles.fast]\nmodel = "gpt-5.4"\n[profiles."deep"]\nmodel = \'gpt-6\'\n'
                          '[other]\nmodel = "ignored"\n',
    ".codex/auth.json": json.dumps({"tokens": {}}),
    ".config/opencode/opencode.json": json.dumps({
        "model": "anthropic/claude-sonnet-4-5", "small_model": "openai/gpt-5-mini",
        "provider": {"local": {"models": {"llama-3": {"name": "Llama 3 \u672c\u5730"}, "bad id!": {}}}}},
        ensure_ascii=False),
    ".local/share/opencode/auth.json": "",
    ".config/opencode/auth.json": json.dumps({"anthropic": {"type": "api"}}),
    ".gemini/settings.json": json.dumps({"model": {"name": "gemini-2.5-pro"}}),
}
DSH_MODELS = {"data": [{"id": "deepseek-v4.1-flash-1017"}, {"id": "deepseek-r2"}, "plain-id", {"id": "x y"}]}


def _fake_cli(folder: Path, name: str, version: str | None, extra: dict[str, str] | None = None,
              exit_code: int = 0) -> None:
    """A fake CLI: --version prints ``version`` (or exits ``exit_code``);
    ``extra`` maps the first two arguments to a fixture file to print."""
    extra = extra or {}
    if os.name == "nt":
        lines = ["@echo off"]
        if version is None:
            lines.append(f'if "%~1"=="--version" exit /b {exit_code}')
        else:
            lines.append(f'if "%~1"=="--version" (echo {version}& exit /b 0)')
        for args, file in extra.items():
            first, _, second = args.partition(" ")
            third = ""
            if " " in second:
                second, _, third = second.partition(" ")
            cond = f'"%~1"=="{first}"' + (f' if "%~2"=="{second}"' if second else "") \
                + (f' if "%~3"=="{third}"' if third else ' if "%~3"==""' if first == "debug" else "")
            lines.append(f'if {cond} (type "%~dp0{file}"& exit /b 0)')
        lines.append("exit /b 2")
        (folder / f"{name}.cmd").write_text("\r\n".join(lines) + "\r\n", encoding="ascii")
        return
    lines = ["#!/bin/sh"]
    if version is None:
        lines.append(f'[ "$1" = "--version" ] && exit {exit_code}')
    else:
        lines.append(f'[ "$1" = "--version" ] && {{ echo "{version}"; exit 0; }}')
    here = '$(dirname "$0")'
    for args, file in extra.items():
        lines.append(f'[ "$*" = "{args}" ] && {{ cat "{here}/{file}"; exit 0; }}')
    lines.append("exit 2")
    path = folder / name
    path.write_text("\n".join(lines) + "\n", encoding="ascii")
    path.chmod(0o755)


class _ModelsHandler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        if self.path.rstrip("/") == "/v1/models" and self.headers.get("Authorization") == "Bearer replay-fake-key-0001":
            body = json.dumps(DSH_MODELS).encode()
            self.send_response(200)
        else:
            body = b'{"error": "nope"}'
            self.send_response(404)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def agent_env(work: Path) -> dict:
    """Environment overrides shared by both servers (see the module docstring)."""
    home = work / "agent-home"
    bin_dir = work / "agent-bin"
    for rel, text in HOME_FILES.items():
        (home / rel).parent.mkdir(parents=True, exist_ok=True)
        (home / rel).write_text(text, encoding="utf-8")
    (home / "AppData" / "Roaming").mkdir(parents=True, exist_ok=True)
    bin_dir.mkdir(parents=True, exist_ok=True)
    (bin_dir / "codex-bundled.json").write_text(json.dumps(CODEX_BUNDLED, ensure_ascii=False), encoding="utf-8")
    (bin_dir / "codex-live.json").write_text(json.dumps(CODEX_LIVE), encoding="utf-8")
    (bin_dir / "opencode-models.txt").write_bytes(OPENCODE_MODELS.encode())
    _fake_cli(bin_dir, "claude", "2.1.0 (Claude Code)")
    _fake_cli(bin_dir, "codex", "codex-cli 0.166.0",
              {"debug models --bundled": "codex-bundled.json", "debug models": "codex-live.json"})
    _fake_cli(bin_dir, "opencode", "opencode 0.9.1", {"models": "opencode-models.txt"})
    _fake_cli(bin_dir, "dsh", "dsh 1.2.3 build abcdefghijklmnopqrstuvwxyz0123")
    _fake_cli(bin_dir, "gemini", None, exit_code=3)
    server = ThreadingHTTPServer(("127.0.0.1", 0), _ModelsHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    git = shutil.which("git")
    paths = [str(bin_dir)] + ([str(Path(git).parent)] if git else [])
    if os.name == "nt":
        root = os.environ.get("SYSTEMROOT", r"C:\Windows")
        paths += [rf"{root}\System32", root, rf"{root}\System32\WindowsPowerShell\v1.0"]
    else:
        paths += ["/usr/local/bin", "/usr/bin", "/bin"]
    env = {name: "" for name in CREDENTIAL_ENV}
    env.update({"PATH": os.pathsep.join(paths), "HOME": str(home), "USERPROFILE": str(home),
                "APPDATA": str(home / "AppData" / "Roaming"),
                "DEEPSEEK_BASE_URL": f"http://127.0.0.1:{server.server_address[1]}/v1",
                "DEEPSEEK_API_KEY": "replay-fake-key-0001", "DEEPSEEK_MODEL": "deepseek-v4.1-flash-1017"})
    return env


def _git(folder: Path, *args: str) -> None:
    null = "nul" if os.name == "nt" else "/dev/null"
    env = {k: v for k, v in os.environ.items() if not k.upper().startswith("GIT_")}
    env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": null})
    subprocess.run(["git", *args], cwd=folder, env=env, check=True, capture_output=True)


def run_hook(name: str, data_dirs: list[Path], fixture: dict) -> None:
    for data in data_dirs:
        project = data / "projects" / fixture["folder"]
        if name == "worktree_clean":
            _git(project, "checkout", "--", ".")
            exclude = project / ".git" / "info" / "exclude"
            exclude.parent.mkdir(parents=True, exist_ok=True)
            with exclude.open("a", encoding="utf-8") as handle:
                handle.write("*\n")
        elif name == "worktree_dirty":
            target = data / "projects" / f"{fixture['folder']}-worktrees" / "agent-feature-x"
            if target.is_dir():  # a project member (--auth) cannot create worktrees
                (target / "scratch.txt").write_text("dirty\n", encoding="utf-8")
        else:
            raise ValueError(f"unknown hook {name}")


def provider_worktree_steps(fixture: dict) -> list[tuple]:
    providers = {"X-Replay-Module": "providers"}
    wt = {"X-Replay-Module": "worktrees"}
    read = {"X-ARK-Read-Token": fixture["tokens"]["read_token"]}
    steps: list[tuple] = []

    def add(route, method, path, body=None, headers=None):
        steps.append((route, method, path, body, dict(headers or {})))

    p, r = "exact:/api/providers", "exact:/api/providers/refresh"
    add(p, "GET", "/api/providers", headers=providers)
    add(p, "GET", "/api/providers", headers=providers)          # cached
    add(r, "POST", "/api/providers/refresh", {}, providers)      # explicit: live codex list, DSH /models
    add(p, "GET", "/api/providers", headers=providers)          # cached, with last_refresh
    add(r, "POST", "/api/providers/refresh", {}, {**providers, **read})
    add(r, "POST", "/api/providers/refresh", {}, {**providers, "Origin": "http://evil.example"})
    add(p, "HEAD", "/api/providers", headers=providers)

    w, c, d = "exact:/api/worktrees", "exact:/api/worktrees/create", "exact:/api/worktrees/remove"
    add(w, "GET", "/api/worktrees", headers=wt)
    for body in ({"name": "Feature X"}, {}, {"name": "!!!"}, {"branch": "-x"}, {"branch": "HEAD"},
                 {"branch": "a..b"}, {"branch": "a/.b"}, {"branch": "x.lock"}, {"branch": "a b"},
                 {"branch": "main"}, {"branch": "x" * 101}, {"base": "-bad", "name": "y"},
                 {"branch": "\u5206\u652f"}, {"name": "x", "branch": "ok/branch."}):
        add(c, "POST", "/api/worktrees/create", body, wt)
    add(c, "POST", "/api/worktrees/create", {"name": "ro"}, {**wt, **read})
    add(d, "POST", "/api/worktrees/remove", {"path": "x", "confirm": True}, {**wt, **read})
    steps.append((HOOK, "worktree_clean", "", None, {}))
    add(w, "GET", "/api/worktrees", headers=wt)
    add(c, "POST", "/api/worktrees/create", {"name": "x", "base": "nope"}, wt)
    add(c, "POST", "/api/worktrees/create", {"name": "Feature X"}, wt)
    add(c, "POST", "/api/worktrees/create", {"name": "Feature X"}, wt)       # already checked out
    add(c, "POST", "/api/worktrees/create", {"branch": "agent/two", "base": "main"}, wt)
    add(c, "POST", "/api/worktrees/create", {"branch": "main"}, wt)
    add(w, "GET", "/api/worktrees", headers=wt)
    for body in ({"path": "agent-two"}, {"confirm": "yes", "name": "agent-two"}, {"confirm": True},
                 {"confirm": True, "name": "nope"}, {"confirm": True, "branch": "main"},
                 {"confirm": True, "path": "C:/nope/agent-two"}):
        add(d, "POST", "/api/worktrees/remove", body, wt)
    add(d, "POST", "/api/worktrees/remove", {"confirm": True, "name": "agent-two", "delete_branch": True}, wt)
    add(c, "POST", "/api/worktrees/create", {"branch": "agent/two"}, wt)      # branch deleted: new again
    add(d, "POST", "/api/worktrees/remove", {"confirm": True, "branch": "agent/two"}, wt)
    add(c, "POST", "/api/worktrees/create", {"branch": "agent/two"}, wt)      # reuses the kept branch
    steps.append((HOOK, "worktree_dirty", "", None, {}))
    add(w, "GET", "/api/worktrees", headers=wt)
    add(d, "POST", "/api/worktrees/remove", {"confirm": True, "name": "agent-feature-x"}, wt)
    add(d, "POST", "/api/worktrees/remove", {"confirm": True, "name": "agent-feature-x", "force": True,
                                             "delete_branch": True}, wt)
    add(w, "GET", "/api/worktrees", headers=wt)
    add(w, "GET", "/api/worktrees", headers={**wt, **read})
    return steps


if __name__ == "__main__":  # pragma: no cover - manual inspection
    json.dump(provider_worktree_steps({"tokens": {"read_token": "rt"}, "folder": "f"}), sys.stdout, indent=1)
