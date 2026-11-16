"""Replay steps for personal and effective settings (org.py personal
settings, App.effective_settings_payload, Handler._assistant_config_payload,
assistant_config.test_connection).

Reads: GET /api/personal/settings for every scope spelling, GET
/api/settings/effective, GET /api/assistant/config.  Writes: personal
assistant settings (every validation branch, key set/replace/clear, hint),
personal agent settings (commands, enabled, sealed env credentials, clear),
the Linux-account selector, POST /api/assistant/config and the connection
test through the chat replay's fake model endpoint (scenario forced by the
base URL path, never a real provider).

Credentials a save leaves alone keep their stored sealed token byte for
byte (main 6988ce9; checked per server by a CALL step against a snapshot
taken before the save).  Plaintext left in user_secrets by the old save bug
is re-sealed on read (audit personal.secrets.reseal): CALL steps seed
plaintext, non-string and empty values into both servers' rows, then each
read path (personal settings, effective settings, agent presets, assistant
config) repairs them; a value shaped like a token that does not open stays
SECRET_CORRUPT.  Sealed values are compared opened (S12), so a row one
server repaired is also opened by the Python reader with that server's key.
"""
from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import replay_services

MODULE = "personal-settings"
CALL = replay_services.CALL


def _reset_agent_secrets(*dirs) -> None:
    for d in dirs:
        for db in Path(d).rglob("org.sqlite3"):
            con = sqlite3.connect(db)
            try:
                con.execute("UPDATE user_secrets SET data_json='{}' WHERE scope='agents'")
                con.commit()
            finally:
                con.close()


_SNAPSHOT: dict = {}


def _rows(d, scope):
    for db in Path(d).rglob("org.sqlite3"):
        con = sqlite3.connect(db)
        try:
            rows = con.execute(
                "SELECT user_id, data_json FROM user_secrets WHERE scope=?", (scope,)).fetchall()
        finally:
            con.close()
        for uid, blob in rows:
            yield db, uid, blob


def _load(blob):
    return json.loads(blob) if (blob or "").strip() else {}


def _snapshot_agents(*dirs) -> None:
    """Remember each server's stored agents tokens (per user)."""
    for d in dirs:
        _SNAPSHOT[str(d)] = {(str(db), uid): _load(blob) for db, uid, blob in _rows(d, "agents")}


def _check_untouched(keys):
    def check(*dirs) -> None:
        for d in dirs:
            before = _SNAPSHOT[str(d)]
            after = {(str(db), uid): _load(blob) for db, uid, blob in _rows(d, "agents")}
            for row, tokens in before.items():
                for key in keys:
                    if key in tokens and after.get(row, {}).get(key) != tokens[key]:
                        raise AssertionError(f"{d}: sealed {key} changed by a save that did not touch it")
    return check


def _seed(scope, extra):
    """Merge raw values (plaintext, non-strings, fake tokens) into every
    stored row of ``scope`` on both servers, as the old bug left them."""
    def seed(*dirs) -> None:
        for d in dirs:
            for db, uid, blob in list(_rows(d, scope)):
                data = _load(blob)
                data.update(extra)
                con = sqlite3.connect(db)
                try:
                    con.execute("UPDATE user_secrets SET data_json=? WHERE user_id=? AND scope=?",
                                (json.dumps(data), uid, scope))
                    con.commit()
                finally:
                    con.close()
    return seed


def _reset_secrets(scope):
    def reset(*dirs) -> None:
        for d in dirs:
            for db in Path(d).rglob("org.sqlite3"):
                con = sqlite3.connect(db)
                try:
                    con.execute("UPDATE user_secrets SET data_json='{}' WHERE scope=?", (scope,))
                    con.commit()
                finally:
                    con.close()
    return reset


def settings_steps(fixture: dict) -> list[tuple]:
    h = {"X-Replay-Module": MODULE}
    other = {**h, "X-Replay-As": "1"}
    ps, eff = "exact:/api/personal/settings", "exact:/api/settings/effective"
    cfg, test = "exact:/api/assistant/config", "exact:/api/assistant/config/test"
    fake = replay_services.fake_llm().url  # http://127.0.0.1:<port>/v1
    root = fake[: -len("/v1")]
    steps: list[tuple] = []

    def get(route, path, headers=h):
        steps.append((route, "GET", path, None, dict(headers)))

    def post(route, path, body, headers=h):
        steps.append((route, "POST", path, body, dict(headers)))

    # ---- reads before any personal setting exists
    for q in ("", "?scope=assistant", "?scope=agents", "?scope=environment", "?scope=bogus",
              "?scope=%20Agents%20", "?scope=", "?scope=ENVIRONMENT", "?scope=assistant&scope=agents"):
        get(ps, "/api/personal/settings" + q)
    get(eff, "/api/settings/effective")
    steps.append((eff, "HEAD", "/api/settings/effective", None, dict(h)))
    get(cfg, "/api/assistant/config")
    post(test, "/api/assistant/config/test", {})  # no credential anywhere: 409

    # ---- assistant scope: validation
    for body in ({"base_url": "ftp://x.example"}, {"base_url": "http://"}, {"base_url": "http://a b.example"},
                 {"base_url": "http://x.example/" + "a" * 400}, {"base_url": 5}, {"model": "a b"},
                 {"model": "x" * 121}, {"reasoning_effort": "huge"}, {"timeout": "abc"}, {"timeout": 4},
                 {"timeout": 301}, {"timeout": [1]}, {"timeout": True}, {"timeout": {"a": 1}},
                 {"timeout": "1_0"}, {"timeout": 1e400}, {"bogus": 1}, {"zeta": 1, "alpha": 2},
                 {"api_key": "k" * 4097}, {"scope": "bogus"}, {"scope": 7}, "[1]"):
        post(ps, "/api/personal/settings", body)
    post(ps, "/api/personal/settings?scope=bogus", {})
    post(ps, "/api/personal/settings?scope=agents", {"scope": "", "base_url": "http://a.example"})
    # ---- assistant scope: saves
    post(ps, "/api/personal/settings", {"base_url": fake + "/", "model": "fake-model", "reasoning_effort": " HIGH ",
                                         "timeout": " 20 ", "api_key": "  sk-personal-1234  "})
    get(ps, "/api/personal/settings")
    get(cfg, "/api/assistant/config")
    get(eff, "/api/settings/effective")
    get(ps, "/api/personal/settings", other)  # another user sees none of it
    post(test, "/api/assistant/config/test", {})  # fake endpoint, scenario "none"
    post(ps, "/api/personal/settings", {"scope": "Assistant", "model": "", "timeout": None, "reasoning_effort": ""})
    post(ps, "/api/personal/settings", {"timeout": 12.9, "api_key": "ab"})  # short key hint
    post(ps, "/api/personal/settings", {"api_key": "sk-second-key-9999", "clear_key": False})
    # POST /api/assistant/config: the same personal save, then the config payload
    post(cfg, "/api/assistant/config", {"model": "fake-model-2"})
    post(cfg, "/api/assistant/config", {"bogus": True})
    post(cfg, "/api/assistant/config", {"base_url": "mailto:x"})
    post(cfg, "/api/assistant/config", {"model": "m"}, {**h, "Origin": "http://evil.example"})
    for scenario in ("http500", "http404", "unexpected", "strmsg", "badjson", "long", "attr", "plain"):
        post(cfg, "/api/assistant/config", {"base_url": f"{root}/t/{scenario}/v1"})
        post(test, "/api/assistant/config/test", {})
    post(cfg, "/api/assistant/config", {"base_url": "http://127.0.0.1:9/v1"})  # refused
    post(test, "/api/assistant/config/test", {})
    post(ps, "/api/personal/settings", {"clear_key": True, "base_url": ""})
    post(test, "/api/assistant/config/test", {})  # key cleared: 409 again
    get(cfg, "/api/assistant/config")

    # ---- agents scope
    for body in ({"scope": "agents"}, {"scope": "agents", "providers": "x"}, {"scope": "agents", "providers": []},
                 {"scope": "agents", "providers": {"nope": {}}},
                 {"scope": "agents", "providers": {"codex": "x"}},
                 {"scope": "agents", "providers": {"codex": {"command": []}}},
                 {"scope": "agents", "providers": {"codex": {"command": ["a"] * 25}}},
                 {"scope": "agents", "providers": {"codex": {"command": "a\nb"}}},
                 {"scope": "agents", "providers": {"codex": {"command": ["ok", ""]}}},
                 {"scope": "agents", "providers": {"codex": {"command": "x" * 401}}},
                 {"scope": "agents", "providers": {"codex": {"env": {"1BAD": "x"}}}},
                 {"scope": "agents", "providers": {"codex": {"env": {"OK": "v" * 4097}}}}):
        post(ps, "/api/personal/settings", body)
    post(ps, "/api/personal/settings?scope=agents", {"providers": {
        "Claude": {"command": "claude --replay", "enabled": False, "env": {" ANTHROPIC_API_KEY ": "sk-ant-secret-9876"}},
        "gemini": {"command": "  ", "enabled": 0}, "opencode": None, "shell": {}}})
    get(ps, "/api/personal/settings?scope=agents")
    get(eff, "/api/settings/effective")
    get("exact:/api/agent-sessions/presets", "/api/agent-sessions/presets")
    get(ps, "/api/personal/settings?scope=agents", other)
    # A second save that leaves claude's credential alone keeps its sealed
    # token byte for byte (6988ce9; before, Python wrote it back opened).
    presets = "exact:/api/agent-sessions/presets"
    steps.append((CALL, "", "", _snapshot_agents, dict(h)))
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {
        "codex": {"command": ["codex", "exec", "{prompt}"], "env": {"OPENAI_API_KEY": "sk-open-5555", "EMPTY": ""}}}})
    steps.append((CALL, "", "", _check_untouched(["env:claude:ANTHROPIC_API_KEY"]), dict(h)))
    get(ps, "/api/personal/settings?scope=agents")
    get(eff, "/api/settings/effective")
    get(cfg, "/api/assistant/config")
    get(presets, "/api/agent-sessions/presets")
    # Replace one key, keep the other.
    steps.append((CALL, "", "", _snapshot_agents, dict(h)))
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {
        "codex": {"env": {"OPENAI_API_KEY": "sk-open-5556"}}}})
    steps.append((CALL, "", "", _check_untouched(["env:claude:ANTHROPIC_API_KEY"]), dict(h)))
    get(ps, "/api/personal/settings?scope=agents")
    # Clearing: provider null, provider {}, clear_env, empty value.
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"claude": None}})
    get(ps, "/api/personal/settings?scope=agents")
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {
        "claude": {"env": {"ANTHROPIC_API_KEY": "sk-ant-again-1111"}}}})
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"claude": {}}})
    get(ps, "/api/personal/settings?scope=agents")
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {
        "codex": {"env": {"OPENAI_API_KEY": "sk-open-6666", "SECOND": "abc"}, "clear_env": ["MISSING"]}}})
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"codex": {"clear_env": [" OPENAI_API_KEY ", None]}}})
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"codex": {"env": {"SECOND": ""}}}})
    get(ps, "/api/personal/settings?scope=agents")
    # Repair on read: plaintext (incl. a 4-part value without the ark1 prefix
    # and one with an empty part), a non-string and an empty value, read
    # through each path that opens user_secrets; then read again.
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {
        "codex": {"env": {"OPENAI_API_KEY": "sk-open-7777"}}}})
    plain = {"env:codex:PLAIN": "plain-text-value", "env:codex:NUM": 5, "env:codex:EMPTY": "",
             "env:codex:NOTARK": "ark2$a$b$c", "env:codex:HOLE": "ark1$a$$c"}
    for route, path in ((ps, "/api/personal/settings?scope=agents"), (eff, "/api/settings/effective"),
                        (presets, "/api/agent-sessions/presets")):
        steps.append((CALL, "", "", _seed("agents", plain), dict(h)))
        get(route, path)
        get(route, path)
        get(ps, "/api/personal/settings?scope=agents")
    # A token-shaped value that does not open stays SECRET_CORRUPT.
    steps.append((CALL, "", "", _seed("agents", {"env:codex:BAD": "ark1$AAAA$BBBB$CCCC"}), dict(h)))
    get(ps, "/api/personal/settings?scope=agents")
    get(eff, "/api/settings/effective")
    get(presets, "/api/agent-sessions/presets")
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"codex": None}})
    steps.append((CALL, "", "", _reset_agent_secrets, dict(h)))
    # Assistant scope: a plaintext api_key repaired by each reader.
    post(ps, "/api/personal/settings", {"api_key": "sk-assist-4321", "base_url": fake + "/"})
    for route, path in ((cfg, "/api/assistant/config"), (ps, "/api/personal/settings"),
                        (eff, "/api/settings/effective")):
        steps.append((CALL, "", "", _seed("assistant", {"api_key": "sk-plain-assist-1234"}), dict(h)))
        get(route, path)
        get(cfg, "/api/assistant/config")
    post(test, "/api/assistant/config/test", {})  # the repaired key is used
    post(ps, "/api/personal/settings", {"model": "fake-model-3"})  # untouched key kept
    get(ps, "/api/personal/settings")
    steps.append((CALL, "", "", _seed("assistant", {"api_key": "ark1$x$y$z"}), dict(h)))
    get(cfg, "/api/assistant/config")
    post(ps, "/api/personal/settings", {"clear_key": True, "base_url": ""})  # the read fails first
    steps.append((CALL, "", "", _reset_secrets("assistant"), dict(h)))
    post(ps, "/api/personal/settings", {"clear_key": True, "base_url": ""})
    get(cfg, "/api/assistant/config")
    post(ps, "/api/personal/settings", {"scope": "agents", "providers": {"codex": None, "claude": {}, "gemini": None}})
    get(ps, "/api/personal/settings?scope=agents")
    steps.append((CALL, "", "", _reset_agent_secrets, dict(h)))

    # ---- environment scope (no Linux account is assigned in the fixture)
    for body in ({"scope": "environment"}, {"scope": "environment", "linux_account": "nobody"},
                 {"scope": " ENVIRONMENT ", "test": True}, {"scope": "environment", "test": 1, "linux_account": "x"},
                 {"scope": "environment", "linux_account": ""}, {"scope": "environment", "linux_account": None}):
        post(ps, "/api/personal/settings", body)
    get(ps, "/api/personal/settings?scope=environment")
    get(ps, "/api/personal/settings?scope=environment", other)
    get(eff, "/api/settings/effective", other)
    get(cfg, "/api/assistant/config", other)
    return steps
