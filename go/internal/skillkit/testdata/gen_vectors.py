"""CPython vectors for go/internal/skillkit.

    python -B go/internal/skillkit/testdata/gen_vectors.py   # writes vectors.json.gz

Each vector is [function, input JSON, output]: the output is the
json.dumps of the result, "!V:" + message for a ValueError (ProtocolError,
RrsiError, DesignError), or "!" + class + ": " + message for any other
exception.  ``created_at`` values are replaced by "<NOW>".
"""
import gzip
import json
import random
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

REPO = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(REPO))
from arkstudio.skills import agent_team, design, protocol, rrsi  # noqa: E402

protocol.utc_now = lambda: "2000-01-01T00:00:00+00:00"  # the Go test pins the clock the same way

rng = random.Random(20261115)
SCALARS = [None, True, False, 0, 1, 2, 3, -1, 1.5, 0.017, 1e400, float("nan"), "", " ", "x", "3", " 4 ", "0.5",
           "inf", "nan", "１２", "1_0", "ß", "İ", "ﬀ", "T-0001", "t-0002", "FIG-007", "A-00", "WEB-12345",
           "unit", "Scope", "moon", "solo", "peer-team", "router-workers", "breadth", "../x", "/abs", "C:x",
           ".worktrees/T-0001", "p1", "P0", "p7", "review", "Active", "running", "blocked", "done", "#fff",
           "#12345G", "prompt", "skill", " Memory ", "subagent", "accepted", "neutral", "invalid", 10 ** 20]


def scalar():
    return rng.choice(SCALARS)


def value(depth=0):
    r = rng.random()
    if depth < 2 and r < 0.15:
        return [value(depth + 1) for _ in range(rng.randint(0, 3))]
    if depth < 2 and r < 0.25:
        return {rng.choice(["a", "command", "mode", "size", "score", "x"]): value(depth + 1)
                for _ in range(rng.randint(0, 2))}
    return scalar()


def out(fn):
    try:
        result = fn()
    except ValueError as exc:
        return "!V:" + str(exc)
    except Exception as exc:  # noqa: BLE001
        return "!" + type(exc).__name__ + ": " + str(exc)
    text = json.dumps(result, ensure_ascii=False)
    return text


def scrub(obj):
    if isinstance(obj, dict):
        return {k: ("<NOW>" if k == "created_at" else scrub(v)) for k, v in obj.items()}
    if isinstance(obj, list):
        return [scrub(v) for v in obj]
    return obj


vectors = []


def add(name, payload, fn):
    text = json.dumps(payload, ensure_ascii=False)
    result = out(lambda: scrub(fn(json.loads(text))))
    vectors.append([name, text, result])


TASK_KEYS = ["task_id", "id", "prefix", "task_prefix", "project_id", "project_name", "phase", "status", "title",
             "objective", "goal", "owner", "reviewer", "assignee", "repository", "base_revision", "base",
             "acceptance_exception", "dependencies", "worktree", "worktree_exception", "deliverables", "non_goals",
             "owned_paths", "acceptance", "gates", "stop_conditions", "risks", "background", "evidence",
             "importance", "budget", "task_structure", "team", "context", "model_policy", "verification",
             "failure_taxonomy", "handoff", "role", "ark_unit_uid", "existing_ids"]
GOOD = agent_team.example_task()
for n in range(400):
    task = dict(GOOD) if rng.random() < 0.6 else {}
    for _ in range(rng.randint(0, 6)):
        task[rng.choice(TASK_KEYS)] = value()
    if rng.random() < 0.3:
        task["existing_ids"] = [rng.choice(["T-0001", "T-0100", "N-0100", "X-12345", 5, None, "abc"])
                                for _ in range(rng.randint(0, 3))]
    kind = "task" if n % 2 == 0 else "manifest"
    if kind == "task":
        add("task", task, lambda t: agent_team.prepare_task(
            t, existing_ids=t.get("existing_ids") or [], project={"id": t.get("project_id"), "name": t.get("project_name")}))
    else:
        add("manifest", task, lambda t: agent_team.validate_manifest(
            t, existing_ids=[v for v in (t.get("existing_ids") or []) if str(v) != str(t.get("task_id"))]))

ROUND = rrsi.example_round()
CFG_KEYS = ["delta", "beta0", "beta1", "w_s", "w_c", "w_n", "b_min", "b_max", "T", "stall_window", "recent_window"]
for n in range(700):
    payload = json.loads(json.dumps(ROUND)) if rng.random() < 0.7 else {}
    for _ in range(rng.randint(0, 4)):
        target = rng.random()
        if target < 0.3 and isinstance(payload.get("config"), dict):
            payload["config"][rng.choice(CFG_KEYS)] = rng.choice([0, 1, 2, 3, 5, 10, 0.5, -1, True, "2", "x", None, 0.017])
        elif target < 0.6 and isinstance(payload.get("candidates"), list) and payload["candidates"] and all(isinstance(c, dict) for c in payload["candidates"]):
            cand = rng.choice(payload["candidates"])
            cand[rng.choice(["score", "cost", "variant", "gate_failure", "commit"])] = rng.choice(
                [0.7, 0.66, 0.5, 0.645, 0.9, 200000, 0, None, "0.7", "x", True, ""])
            if rng.random() < 0.3:
                cand["edits"] = [{"component": rng.choice(["prompt", "skill", "memory", "Tool", " Config "]),
                                  "hypothesis": rng.choice(["h", "", " "])} for _ in range(rng.randint(0, 4))]
        else:
            payload[rng.choice(["round", "incumbent", "s_star", "accepted_counts", "guards", "candidates", "config"])] = value()
    if rng.random() < 0.2:
        payload["guards"] = {"candidate-a": rng.choice([["g"], [], "ab", 3]), "candidate-b": ["x"]}
    add("round", payload, rrsi.validate_round)

for n in range(400):
    records = []
    for _ in range(rng.randint(0, 6)):
        rec = {"round": rng.choice([0, 1, 2, "3", -1, 1.5]),
               "component": rng.choice(["prompt", "skill", "memory", "subagent", "config", "bad"]),
               "hypothesis": rng.choice(["h", "x", ""]),
               "verdict": rng.choice(["accepted", "accepted", "rejected", "neutral", "invalid", "maybe"]),
               "delta_score": rng.choice([0.0, 0.01, 0.02, 0.5, -0.1, "0.03", None, True]),
               "delta_cost": rng.choice([0.0, 0.1, "x", None])}
        if rng.random() < 0.2:
            rec.pop(rng.choice(list(rec)))
        records.append(rec)
    payload = {"records": records}
    if rng.random() < 0.6:
        payload["config"] = {rng.choice(CFG_KEYS): rng.choice([1, 2, 3, 4, 0, "2", 0.5]) for _ in range(rng.randint(0, 3))}
    add("history", payload, rrsi.history_snapshot)

DIFFS = ["", "+ x = 1", "+ 'text'", "- \"old\"", "+ # comment", "+++ a\n--- b", "+ from arkstudio.skills import x",
         "+ mem = Memory()", "+ ToolRegistry()", "+ subagent()", "+ f'x'\n+ b'y'", "+    \n+ '''doc'''",
         "+ self.remember(x)", "+ SKILL.md", "+ sub_agent", "\u2028+ x", "+ r\"x\"\r\n- # y"]
for n in range(300):
    add("edit", {"component": rng.choice(["", "skill", "Skill ", "memory", "prompt", "client_tool", "nope"]),
                 "diff": "\n".join(rng.choice(DIFFS) for _ in range(rng.randint(0, 3))),
                 "hypothesis": rng.choice(["", " h ", None])}, rrsi.validate_edit)

LEAK = ["tests/x", "test_a.py", "expected_output", "assertEqual(", "hidden tests", "benchmark task", "T-123",
        "T-12", "contest/", "ok", "assertTrue", "my_test_file.py", "\u00e9tests/"]
for n in range(200):
    text = "\n".join(" ".join(rng.choice(LEAK) for _ in range(rng.randint(0, 3))) for _ in range(rng.randint(0, 4)))
    add("leakage", {"text": text}, lambda p: rrsi.scan_leakage(p["text"]))
for pattern in ["(", "a{2,1}", "[z-a]", "(?P<x>a)(?P<x>b)", "\\", "x*", "(?<=a+)b", "\\bT-\\d+"]:
    add("leakage-patterns", {"text": "aT-12b", "patterns": [pattern]}, lambda p: rrsi.scan_leakage(p["text"], p["patterns"]))

DOMAIN = rrsi.example_domain()
for n in range(300):
    payload = json.loads(json.dumps(DOMAIN)) if rng.random() < 0.7 else {}
    for _ in range(rng.randint(0, 3)):
        payload[rng.choice(["name", "harness_path", "evolve", "heldout", "smoke", "metric", "guards",
                            "component_signals"])] = rng.choice(
            [None, "", " x ", ["a", "a"], ["T-0001"], ["T-0004"], "C:\\x", "a\\..\\b", "/x", {"skill": "x"},
             {"bogus": []}, [1, 2], 5, {"memory": ["a", 1]}])
    add("domain", payload, rrsi.validate_domain)

THEME = design.example_theme()
PATHS = []


def walk(node, prefix):
    if isinstance(node, dict):
        for k, v in node.items():
            PATHS.append(prefix + [k])
            walk(v, prefix + [k])


walk(THEME, [])
for n in range(500):
    theme = json.loads(json.dumps(THEME))
    for _ in range(rng.randint(1, 3)):
        path = rng.choice(PATHS)
        node = theme
        try:
            for key in path[:-1]:
                node = node[key]
            if rng.random() < 0.2:
                node.pop(path[-1], None)
            else:
                node[path[-1]] = rng.choice(
                    [None, "", "x", "#FFF", "#00000080", "abc", 0, 1, -1, 0.05, 2, 100, 950, 1.0, True, "nan",
                     "0.5", [], ["a"], [1, 2], {}, {"value": "#fff", "usage": "u"}, "none", "ink", "Solid", "round"])
        except (TypeError, KeyError, AttributeError):
            pass
    variant = rng.choice([None, None, "paper", "web", "video", "print", 5, ""])
    add("theme", {"theme": theme, "variant": variant}, lambda p: design.validate_theme(p["theme"], variant=p["variant"]))

TEMPLATES = ["$a", "${a}", "$$", "$", "x $ y", "a\n$1", "${a", "${1}", "$a$b", "l1\nl2 $!", "é$\u00e9", "\r\n$-",
             "a\u2028$!", "$missing", "${missing}", "$_a", "${ a}"]
for t in TEMPLATES:
    for ctx in [{"a": "A", "b": None, "_a": 1}, {"a": ["x"], "gates": ["unit"]}]:
        add("substitute", {"template": t, "context": ctx}, lambda p: protocol.substitute(p["template"], p["context"]))
for key in ["acceptance", "gates", "evidence", "dependencies", "team", "failure_taxonomy", "commands", "risks", "x"]:
    for _ in range(12):
        add("render-value", {"key": key, "value": value()},
            lambda p: protocol.substitute("[$" + p["key"] + "]", {p["key"]: p["value"]}))


def xml_root(text):
    import io
    return ET.parse(io.BytesIO(text.encode("utf-8"))).getroot().tag


XML = ["", "   ", "not xml", "<svg>", "<svg></g>", "<a/><b/>", "<svg/>x", "< svg/>", "<svg a=1/>", "<svg>&foo;</svg>",
       "<!-- c -->", "<svg>\n<g>\n</svg>", "<?xml version='1.0'?>\n<svg xmlns='http://www.w3.org/2000/svg'/>",
       "<x:svg/>", "<svg a='1' a='2'/>", "<svg>\x01</svg>", "\ufeff<svg/>", "<svg><![CDATA[x]]></svg>",
       "<!DOCTYPE svg><svg/>", "<svg", "<svg a", "<svg a=", "<svg a='x", "<svg></svg", "<svg><g></svg>", "<html/>",
       "<svg>&#0;</svg>", "<svg>&amp;&lt;&#x41;&#65;</svg>", "<s:svg xmlns:s='u'/>", "<svg xmlns:a='u' a:b='1'/>",
       "<svg c:b='1'/>", "<svg>a<b>c</b>d</svg>  \n<!-- x -->\n", "<svg/><?pi?>", "<svg><!-- a -- b --></svg>",
       "<svg>]]></svg>", "<svg><![CDATA[x</svg>", "<svg a='<'/>", "<svg a='&'/>", "<svg a=\"x\"b='y'/>",
       "<svg/ >", "<svg>&#xZZ;</svg>", "<svg>&#;</svg>", "<svg>&</svg>", "x<svg/>", "<svg>\r\n</g>",
       "<svg>\r</g>", "\n\n  <svg></svv>", "<?xml version='1.0'?><?xml version='1.0'?><svg/>", "<svg xmlns=''/>",
       "<svg xmlns='a'><g xmlns='b'/></svg>", "<svg></svg >", "<svg></ svg>", "<!DOCTYPE svg [ <!ENTITY x 'y'> ]><svg>&x;</svg>",
       "<svg>&#x110000;</svg>", "<svg>&#55296;</svg>", "<1svg/>", "<svg\ta\n=\r'1'/>"]
for x in XML:
    vectors.append(["xml", json.dumps(x, ensure_ascii=False), out(lambda: xml_root(x))])

UP = ["ß", "ﬀ", "ŉ", "ǰ", "İ", "ı", "ǅ", "ﬃx", "straße", "Σσς", "ᾳ", "ΐ", "ⓐ", "𐐨", "ǈ", "abc", "Ⅷ ⅷ"]
for s in UP:
    vectors.append(["upper", json.dumps(s, ensure_ascii=False), json.dumps(s.upper(), ensure_ascii=False)])

target = Path(__file__).with_name("vectors.json.gz")
target.write_bytes(gzip.compress(json.dumps(vectors, ensure_ascii=False).encode("utf-8"), 9, mtime=0))
print(f"wrote {len(vectors)} vectors to {target}")
