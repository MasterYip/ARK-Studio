"""CPython vectors for skillbrief.ValidateBrief (interface.validate_brief).

    python -B go/internal/skillbrief/testdata/gen_vectors.py
"""
import json
import random
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(REPO))
from arkstudio.skills import interface  # noqa: E402

rng = random.Random(11)
SCALARS = [None, True, False, 0, 1, 2, 3, 5, 6, 9, -1, 1.5, 2.9, float("nan"), "", " ", "3", " 4 ", "x",
           "１２", "1_000", "solo", "pipeline", "router-workers", "Peer-Team", "breadth", "ADVERSARIAL",
           "long-horizon", "nope", "unit", "scope", "tiered", "human", "rubric", "spec", "tool", "zz",
           "../x", "/abs", "http://x/y", "a/b.md", "C:\\data", "  reviewer ", "é" * 50, 10 ** 20,
           8000, 7999, 2000001, 600000, "\u2003pad\u2003"]


def scalar():
    return rng.choice(SCALARS)


def value(depth=0):
    r = rng.random()
    if depth < 2 and r < 0.2:
        return [value(depth + 1) for _ in range(rng.randint(0, 4))]
    if depth < 2 and r < 0.3:
        return {rng.choice(["command", "evidence", "x"]): value(depth + 1) for _ in range(rng.randint(0, 2))}
    return scalar()


KEYS = {
    "objective": lambda: rng.choice(["Ship X", "", None, ["a", 1], {"k": 1}, 0, "  goal  ", "x" * 4100]),
    "goal": lambda: rng.choice(["fallback goal", None]),
    "task_structure": lambda: rng.choice(["sequential", "breadth", "Adversarial", "long-horizon", "bad", None, 3]),
    "team": lambda: rng.choice([None, {}, "x", {"mode": scalar(), "size": scalar(), "heterogeneous": scalar(),
                                                  "rationale": scalar(), "roles": value()}]),
    "topology": scalar, "team_size": scalar, "heterogeneous": scalar, "team_rationale": scalar,
    "reviewer": scalar,
    "context": lambda: rng.choice([None, "x", {"budget_tokens": scalar(), "inline": value(), "spill": value(),
                                               "artifacts": value()}]),
    "owned_paths": value, "acceptance": value, "stop_conditions": value,
    "gates": lambda: rng.choice([None, ["scope", "unit"], ["scope"], [], "unit", {"unit": 1}, [1, "unit"], ["x"]]),
    "verification": lambda: rng.choice([None, {}, {"independent": scalar(), "method": scalar(),
                                                   "evidence": value(), "reviewer_reproduces": value()},
                                        {"method": "human"}]),
    "failure_taxonomy": value,
    "handoff": lambda: rng.choice([None, {"contract": scalar(), "inputs": value(), "outputs": value()}]),
    "deliverables": lambda: rng.choice([None, ["d1"], "ab", {"k": 1}, [], 5]),
    "dependencies": lambda: rng.choice([None, ["T1"], "xy", True, []]),
}

out = []
for i in range(1500):
    payload = {k: f() for k, f in KEYS.items() if rng.random() < 0.6}
    text = json.dumps(payload, ensure_ascii=False)
    payload = json.loads(text)
    try:
        result = json.dumps(interface.validate_brief(payload), ensure_ascii=False)
    except Exception as exc:  # noqa: BLE001
        result = f"!{type(exc).__name__}: {exc}"
    out.append([text, result])
out.append(["{}", json.dumps(interface.validate_brief({}), ensure_ascii=False)])
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=False), encoding="utf-8")
print(len(out), sum(1 for _, r in out if r.startswith("!")))
