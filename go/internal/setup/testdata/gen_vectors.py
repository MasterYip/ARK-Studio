"""Regenerate vectors.json from CPython: python -B gen_vectors.py

Random setup profiles and layers built from trap pieces; every vector stores
the input JSON text and json.dumps(..., ensure_ascii=False) of what
skills/setup.py and setup_layers.py return for it.
"""
import json
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4]))
from arkstudio.skills import setup as base  # noqa: E402
from arkstudio.skills import setup_layers as layers  # noqa: E402

TEXTS = ["", " ", "x", "Alpha", " pad ", "中文", "İstanbul", "ß", "a" * 60, "A-B", "bad id!", "repo.1_x",
         "env:TOKEN", "alias:x", "keychain:a/b", "ssh-config:host", "plain", "sk-" + "a" * 20, "ghp_" + "b" * 22,
         "AKIA" + "C" * 16, "xoxb-1234567890", "https://u:p@example.invalid/x", "https://example.invalid/x",
         "git@example.invalid:x.git", "-----BEGIN RSA PRIVATE KEY-----", "~/x", "C:/x", "/abs", "rel/x",
         "primary", "code", "paper", "read-only", "READ-WRITE", "local", "ssh", "cloud", "any", "ask", "deny",
         "allow", "Allow ", "conda", "artifacts", "logs", "unit", "static", "component", "experiment", "bogus",
         "scope", "code", "team", "compute", "commands", "authority", "unlimited", "Unlimited", "3", " 7 ",
         "１２", "1e3", "-5", "x\ny", "a|b", "line\n"]
SCALARS = [None, True, False, 0, 1, 5, -3, 2.5, 1e9, 10 ** 20, -(10 ** 20), [], {}, ["x"], {"a": 1}]
KEYS = ["id", "path", "role", "access", "default_branch", "worktree_root", "remote", "instructions", "note",
        "kind", "location", "relation", "gpu_server", "gpus", "use", "scratch", "repository", "compute",
        "activate", "python", "retention", "run", "environment", "cwd", "gate", "mandatory", "expensive",
        "proposed", "ref", "purpose", "name", "password", "api_key", "token", "owner", "path_or_ref",
        "details", "type", "subkind", "visibility"]
FIELDS = ["repositories", "task_spaces", "compute", "environments", "storage", "commands", "secrets"]


def value(rng, depth=0):
    r = rng.random()
    if r < 0.6:
        return rng.choice(TEXTS)
    if r < 0.8 or depth > 1:
        return rng.choice(SCALARS)
    if r < 0.9:
        return [value(rng, depth + 1) for _ in range(rng.randint(0, 3))]
    return {rng.choice(KEYS): value(rng, depth + 1) for _ in range(rng.randint(0, 3))}


def entry(rng):
    if rng.random() < 0.05:
        return rng.choice(["str", 5, None, []])
    return {rng.choice(KEYS): value(rng) for _ in range(rng.randint(0, 7))}


def profile(rng):
    out = {}
    for f in FIELDS:
        if rng.random() < 0.5:
            out[f] = [entry(rng) for _ in range(rng.randint(0, 4))] if rng.random() < 0.9 else value(rng)
    for key in ("objective", "non_goals", "notes", "unknowns", "skipped"):
        if rng.random() < 0.5:
            out[key] = value(rng) if rng.random() < 0.5 else [value(rng) for _ in range(rng.randint(0, 3))]
    if rng.random() < 0.6:
        out["agents"] = {k: value(rng) for k in rng.sample(
            ["coordinator", "providers", "concurrency", "default_reviewer", "default_context_tokens"], rng.randint(0, 5))}
    if rng.random() < 0.6:
        out["policies"] = {k: rng.choice(["allow", "ask", "deny", "Deny ", "maybe", None, 1])
                           for k in rng.sample(list(base.POLICY_DEFAULTS) + ["bogus"], rng.randint(0, 5))}
    if rng.random() < 0.4:
        out["cadence"] = {"status_minutes": value(rng), "task_trigger": value(rng)}
    if rng.random() < 0.4:
        out["task_ids"] = {"prefixes": [rng.choice(TEXTS) for _ in range(rng.randint(0, 3))]}
    if rng.random() < 0.3:
        out["resources"] = [entry(rng) for _ in range(rng.randint(0, 4))]
    if rng.random() < 0.2:
        out["policies_defaulted"] = rng.choice([["push"], ["commit", "x"], "push", []])
    return out


def dumps(v):
    return json.dumps(v, ensure_ascii=False)


def main():
    rng = random.Random(11)
    out = []
    for _ in range(120):
        raw = profile(rng)
        text = dumps(raw)
        row = {"in": text}
        normalized, errors, warnings = base.normalize_profile(json.loads(text))
        row["profile"] = dumps([normalized, errors, warnings])
        row["digest"] = base.digest(normalized)
        row["questions"] = dumps(base.questions(normalized))
        state = rng.choice(base.STATES)
        row["state"] = state
        row["readiness"] = dumps(base.readiness(normalized, state))
        record = {"state": state, "profile": normalized, "digest": rng.choice(["", "abc"]),
                  "updated_at": rng.choice(["", "2026-01-01T00:00:00+00:00"]), "updated_by": "human:x",
                  "confirmation": rng.choice([None, {"at": "t", "by": "b"}])}
        row["record"] = dumps(record)
        row["summary"] = base.render_summary(record)
        for layer in ("project", "user"):
            try:
                prof, locs, errs, warns = layers.normalize_layer(json.loads(text), layer=layer, owner="Bob")
                row[layer] = dumps([prof, [list(x) for x in locs], errs, warns])
            except base.SetupError as exc:
                row[layer] = "error: " + str(exc)
        out.append(row)
    Path(__file__).with_name("vectors.json").write_text(dumps(out), encoding="utf-8")


main()
