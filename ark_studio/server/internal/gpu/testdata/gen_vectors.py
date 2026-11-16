"""Regenerate vectors.json from CPython: python -B gen_vectors.py

Runs the real vendor/gpu_server_probe config.py and dispatch.py over many
inputs (including error cases); config_test.go and dispatch_test.go replay
them against the Go port.
"""
import inspect
import json
import math
import os
import random
import shlex
import shutil
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[3] / "python" / "vendor"))

from gpu_server_probe import config, dispatch  # noqa: E402


class Hang(Exception):
    pass


def guarded(fn, *args):
    """Run fn, turning a runaway loop (CPython hangs on some inputs) into Hang."""
    count = [0]

    def tracer(frame, event, arg):
        count[0] += 1
        if count[0] > 2_000_000:
            raise Hang()
        return tracer

    sys.settrace(tracer)
    try:
        return fn(*args)
    finally:
        sys.settrace(None)


def outcome(fn, *args, trace=True):
    try:
        value = guarded(fn, *args) if trace else fn(*args)
    except Hang:
        return {"type": "Hang"}
    except RecursionError:
        return {"type": "RecursionError"}
    except (config.ConfigError, dispatch.DispatchError) as exc:
        return {"type": type(exc).__name__, "err": str(exc)}
    except Exception as exc:  # leaks such as OverflowError / ValueError
        return {"type": type(exc).__name__, "err": str(exc)}
    return {"ok": json.dumps(value, ensure_ascii=False)}


# ------------------------------------------------------------------ yaml

VALUES = [
    "", "x", "gpu-1", "4090", "-3", "+7", "007", "1_000", "\u0661\u0662", "\uff11\uff12", "1.5", "-0.25",
    "\u0663.\u0665", "1.", ".5", "1e3", "99999999999999999999999999", "1" * 400 + ".5", "true", "False",
    "TRUE", "null", "~", "Null", "yes", "off", "[]", "[ ]", "[a, b]", "[4090, primary]", "[a,,b]",
    "['x, y', \"z\"]", "[\"unterminated]", "[[a], b]", "[[a, b]]", "[1, 2.5, true, null]", "&anchor",
    "*alias", "| block", "> folded", "{a: 1}", "@x", "`x`", "\"quoted\"", "'single'", "'it''s'",
    "\"a\\nb\\tc\\\"d\\\\e\\qf\"", "\"unterminated", "'unterminated", "\"done\" trailing", "\"done\"  ",
    "\"\"", "''", "a\x01b", "a\x7fb", "caf\u00e9", "\u4e2d\u6587", "x: y", "a # comment", "- item",
    "\u3000padded\u3000", "\x1fx", "[" , "]", "[a]b", "'x'y", "\"x\\", "ABCDEFGHIJKLMNOPQRSTUVWXYZ&*",
    "&" + "\u00e9" * 30, "/tmp/q", "/data/../x", "rel/path", "1.0", "4090.0", "0", "65535", "65536",
    "-0", "+0.0", "12345678901234567890", "\u0966\u0967", "0.1", "1" * 30, "2" * 17 + ".0",
]

KEYS = [
    "enabled", "defaults", "servers", "queue", "port", "user", "timeout_seconds", "name", "host", "tags",
    "note", "state_dir", "poll_seconds", "default_timeout_minutes", "max_jobs_per_gpu",
    "max_utilization_pct", "min_free_memory_mb", "allow_colocation", "setup", "path_map", "env", "from",
    "to", "password", "ssh_key", "api-key", "Identity_File", "my_token", "keys", "x", "_k", "k-1",
    "1bad", "bad key", "PATH", "CUDA_HOME", "unknown",
]

SEPARATORS = ["\n", "\n", "\n", "\r\n", "\r", "\x0b", "\x0c", "\x1c", "\x1d", "\x1e", "\x85", "\u2028",
              "\u2029"]


def rand_line(rng):
    indent = " " * rng.choice([0, 0, 0, 2, 2, 4, 4, 6, 8, 1, 3])
    kind = rng.random()
    if kind < 0.45:
        key = rng.choice(KEYS)
        return f"{indent}{key}: {rng.choice(VALUES)}".rstrip(" ") if rng.random() < 0.8 else f"{indent}{key}:"
    if kind < 0.7:
        if rng.random() < 0.5:
            return f"{indent}- {rng.choice(KEYS)}: {rng.choice(VALUES)}"
        return f"{indent}- {rng.choice(VALUES)}"
    if kind < 0.75:
        return indent + rng.choice(["# comment", "", "---", "...", "-", "\t", "key\t: v", "noseparator",
                                    " : v", "a:b", "\u00a0", "- ", "-x"])
    return f"{indent}{rng.choice(KEYS)}:"


GOOD = """enabled: true
defaults:
  port: 22
  user: ubuntu
  timeout_seconds: 8
servers:
  - name: gpu-server-1
    host: gpu-server-1.example.com
    port: 2222
    user: ubuntu
    tags: [4090, primary, 4090, 1.5]
    note: "H20 node behind ~/.ssh/config"
  - name: gpu-2
    host: 10.0.0.2
    enabled: false
  - name: v6
    host: "fe80::1"
    user: ""
queue:
  enabled: true
  state_dir: /tmp/ark-q
  poll_seconds: 10
  path_map:
    - from: /home/me/proj/
      to: /data/proj//
      servers: [gpu-2, " gpu-server-1 ", 5, true]
    - from: /a
      to: /b
  env:
    HF_HOME: /data/hf
    FLAG: true
    OFF: false
    N: 3
    F: 2.50
  setup: source ~/venv/bin/activate
"""

HAND_YAML = [
    "", "\n\n", "# only comment\n", "---\n", "...\n", "a: 1\n\tb: 2\n", "- a\n", "  - a\n", "a: [1, 2]",
    "a:\n  - x\n  - y\n", "a:\n  - x: 1\n    y: 2\n  - z\n", "a:\n  - x: 1\n    x: 2\n",
    "a:\n  - name: a\n      - x\n", "a:\n  -\n    b: 1\n  -\n", "a:\n  - b\n    c: 1\n",
    "a: 1\na: 2\n", "a:\n    b: 1\n  c: 2\n", "a:\n  b: 1\n   c: 2\n", "a:\n  b: 1\nc: 2\n",
    "  a: 1\nb: 2\n", "a: 1\n  - b\n", "password: x\n", "a:\n  - token: x\n", "a: 'x'\n",
    "a: \"x\" y\n", "a: \"x\\\"\n", "a: 'it''s'\n", "a: [\"a, b\", 'c']\n", "a: [a, 'b]\n",
    "a: &x 1\n", "a: |\n  text\n", "a: {b: 1}\n", "a: \x01\n", "a: b\x01\n", "\ufeffa: 1\n",
    "a: 1\r\nb: 2\r\n", "a: 1\rb: 2", "a: 1\x85b: 2", "a: 1\u2028  b: 2", "a:\n\u3000 b: 1\n",
    "a: \u0661\u0662\u0663\n", "a: \uff11.\uff15\n", "a: " + "9" * 100 + "\n", "a: 1.5e3\n",
    "a:\n- x\n", "a:\n  b:\n    c:\n      d: [x]\n", "1a: x\n", "a b: x\n", ": x\n", "a:x\n",
    "a: x # not a comment\n", "a:   \n", "a: ~\n", "a: NULL\n", "a: True\n", "a: on\n",
    GOOD, GOOD.replace("port: 2222", "port: 70000"), GOOD.replace("user: ubuntu\n  timeout", "user: bad user\n  timeout"),
    GOOD.replace("host: 10.0.0.2", "host: user@10.0.0.2"), GOOD.replace("tags: [4090, primary, 4090, 1.5]", "tags: [a b]"),
    GOOD.replace("FLAG: true", "FLAG: [1]"), GOOD.replace("state_dir: /tmp/ark-q", "state_dir: tmp"),
    GOOD.replace("state_dir: /tmp/ark-q", "state_dir: /tmp/../q"), GOOD.replace("from: /a", "from: a"),
    GOOD.replace("to: /b", "to: /b/../c"), GOOD.replace("poll_seconds: 10", "poll_seconds: 1.5"),
    GOOD.replace("poll_seconds: 10", "poll_seconds: true"), GOOD.replace("name: v6", "name: gpu-2"),
    GOOD.replace("enabled: true\ndefaults", "enabled: yes\ndefaults"), GOOD.replace("note: \"H20", "bogus: 1\n    note: \"H20"),
    GOOD.replace("setup: source", "extra: 1\n  setup: source"),
    "servers:\n  - name: a\n    host: h\n    tags: [" + ", ".join(str(i) for i in range(13)) + "]\n",
    "servers:\n" + "".join(f"  - name: s{i}\n    host: h{i}\n" for i in range(65)),
    "servers:\n  - name: " + "n" * 61 + "\n    host: h\n",
    "servers:\n  - name: a\n    host: " + "h" * 254 + "\n",
    "servers:\n  - name: a\n    host: " + "h" * 253 + "\n",
    "servers:\n  - name: a\n    host: -bad\n", "servers:\n  - name: a\n    host: 1:2\n",
    "servers:\n  - name: a\n    host: h\n    note: " + "n" * 201 + "\n",
    "servers:\n  - name: a\n    host: h\n    note: \"x\\ny\"\n",
    "servers:\n  - name: \"caf\u00e9\"\n    host: h\n",
    "servers:\n  - name: \"  \"\n    host: h\n", "servers:\n  - name: a\n", "servers:\n  - name: a\n    host: h\n    port: \"22\"\n",
    "servers: x\n", "servers: [a]\n", "defaults: 1\n", "defaults:\n  port: 0\n", "defaults:\n  bogus: 1\n",
    "queue: 0\n", "queue: [1]\n", "queue:\n  env:\n    1A: x\n", "queue:\n  env: [1]\n",
    "queue:\n  env:\n    A: \"" + "x" * 501 + "\"\n", "queue:\n  env:\n    A: \"a\\tb\"\n",
    "queue:\n  path_map: x\n", "queue:\n  path_map:\n    - x\n", "queue:\n  path_map:\n    - from: /a\n      to: /b\n      servers: x\n",
    "queue:\n  path_map:\n    - from: /a\n      to: /b\n      bogus: 1\n", "queue:\n  state_dir:\n",
    "queue:\n  setup: \"" + "s" * 2001 + "\"\n", "queue:\n  min_free_memory_mb: 1000001\n",
    "queue:\n  max_jobs_per_gpu: " + "9" * 30 + "\n", "queue:\n  allow_colocation: 1\n",
    "enabled: true\n", "enabled: 1\n", "unknown: 1\n", "enabled:\n",
]


def deep_mapping(levels):
    return "".join(" " * i + "k:\n" for i in range(levels)) + " " * levels + "v: 1\n"


def deep_inline(levels):
    return "a: " + "[" * levels + "x" + "]" * levels + "\n"


def yaml_vectors(rng):
    sources = list(HAND_YAML)
    for _ in range(900):
        sep = rng.choice(SEPARATORS) if rng.random() < 0.2 else "\n"
        lines = [rand_line(rng) for _ in range(rng.randint(1, 9))]
        sources.append(sep.join(lines) + (sep if rng.random() < 0.7 else ""))
    # mutate the good document line by line
    good_lines = GOOD.splitlines()
    for _ in range(500):
        lines = list(good_lines)
        for _ in range(rng.randint(1, 3)):
            i = rng.randrange(len(lines))
            line = lines[i]
            indent = len(line) - len(line.lstrip(" "))
            stripped = line.strip()
            choice = rng.random()
            if choice < 0.5 and ":" in stripped:
                head = stripped.split(":", 1)[0]
                lines[i] = " " * indent + head + ": " + rng.choice(VALUES)
            elif choice < 0.7:
                lines[i] = " " * indent + rng.choice(KEYS) + ": " + rng.choice(VALUES)
            elif choice < 0.8:
                del lines[i]
            elif choice < 0.9:
                lines.insert(i, rand_line(rng))
            else:
                lines[i] = " " * rng.choice([0, 2, 4, 6]) + stripped
        sources.append("\n".join(lines) + "\n")
    out = []
    for src in sources:
        row = {"src": src, "parse": outcome(config.parse_yaml, src)}
        if "ok" in row["parse"]:
            row["validate"] = outcome(config.validate, config.parse_yaml(src))
        out.append(row)
    return out


def recursion_vectors():
    """Coarse checks only: the exact limit depends on the caller's stack."""
    rows = []
    for levels in (50, 200, 700):
        for kind, make in (("mapping", deep_mapping), ("inline", deep_inline)):
            src = make(levels)
            rows.append({"kind": kind, "levels": levels, "parse": outcome(config.parse_yaml, src, trace=False)})
    return rows


def _fails(src):
    try:
        config.parse_yaml(src)
    except RecursionError:
        return True
    return False


def recursion_thresholds():
    """First failing nesting level for a known Python stack depth at the call."""
    rows = []
    for kind, make in (("mapping", deep_mapping), ("inline", deep_inline)):
        stack = len(inspect.stack())  # _fails adds one frame below this one
        lo, hi = 1, 2000
        while lo < hi:
            mid = (lo + hi) // 2
            if _fails(make(mid)):
                hi = mid
            else:
                lo = mid + 1
        rows.append({"kind": kind, "stack": stack + 1, "first_failing": lo})
    return rows


# ------------------------------------------------------------- validate

VALIDATE_JSON = [
    "1", "[]", "null", "{}", '{"enabled": 1}', '{"enabled": null}', '{"defaults": []}', '{"defaults": 0}',
    '{"defaults": {"port": 22.0}}', '{"defaults": {"port": true}}', '{"defaults": {"port": 99999999999999999999}}',
    '{"defaults": {"user": 5}}', '{"defaults": {"user": null}}', '{"servers": {}}', '{"servers": false}',
    '{"servers": [null]}', '{"servers": [{"name": 5, "host": "h"}]}',
    '{"servers": [{"name": "a", "host": "h", "tags": [1e16, 4090.0, -0.0, 1e-7, 12345678901234567890]}]}',
    '{"servers": [{"name": "a", "host": "h", "tags": [true]}]}', '{"servers": [{"name": "a", "host": "h", "tags": [null]}]}',
    '{"servers": [{"name": "a", "host": "h", "tags": "x"}]}', '{"servers": [{"name": "a", "host": "h", "tags": [[1]]}]}',
    '{"servers": [{"name": "a", "host": "h", "tags": [" x ", "x"]}]}', '{"servers": [{"name": "a", "host": "h", "tags": ["a\\u0001"]}]}',
    '{"servers": [{"name": "a", "host": "h", "tags": [NaN]}]}', '{"servers": [{"name": "a", "host": "h", "tags": [Infinity]}]}',
    '{"servers": [{"name": "a", "host": "h", "enabled": 1}]}', '{"servers": [{"name": "a", "host": "h", "note": null}]}',
    '{"servers": [{"name": "a", "host": "h\\n"}]}', '{"servers": [{"name": "a", "host": "h", "user": "u\\n"}]}',
    '{"servers": [{"name": "a", "host": "\\u0661"}]}', '{"servers": [{"name": "a", "host": "fe80::1:2:3:4:5:6:7:8:9:a:b:c:d:e:f:0"}]}',
    '{"queue": {"env": {"A": 1e16, "B": 4090.0, "C": NaN, "D": -Infinity, "E": 99999999999999999999}}}',
    '{"queue": {"env": {"A": null}}}', '{"queue": {"env": {"A": {"x": 1}}}}', '{"queue": {"env": {"": "x"}}}',
    '{"queue": {"env": {"A\\n": "x"}}}', '{"queue": {"env": {"' + "A" * 64 + '": "x"}}}',
    '{"queue": {"env": {"' + "A" * 65 + '": "x"}}}', '{"queue": {"env": 0}}', '{"queue": {"env": "x"}}',
    '{"queue": {"path_map": [{"from": "/a/", "to": "/b", "servers": [null, 1.5, [1, "x"], {"k": "v"}, "  ", false, "s"]}]}}',
    '{"queue": {"path_map": [{"from": "/", "to": "///"}]}}', '{"queue": {"path_map": [{"from": 1, "to": "/b"}]}}',
    '{"queue": {"path_map": 0}}', '{"queue": {"path_map": {}}}', '{"queue": {"path_map": {"a": 1}}}',
    '{"queue": {"state_dir": null}}', '{"queue": {"state_dir": "  /x  "}}', '{"queue": {"state_dir": "/a/\\u0085"}}',
    '{"queue": {"setup": 5}}', '{"queue": {"poll_seconds": 300.0}}', '{"queue": {"enabled": "true"}}',
    '{"queue": {"default_timeout_minutes": 1440, "max_jobs_per_gpu": 16, "max_utilization_pct": 100, "min_free_memory_mb": 0}}',
    '{"queue": {"default_timeout_minutes": 0}}', '{"queue": {"max_utilization_pct": -1}}',
    '{"queue": true}', '{"queue": ""}', '{"queue": "x"}', '{"servers": [{"name": "a", "host": "h", "port": -1}]}',
    '{"servers": [{"name": "a", "host": "h", "timeout_seconds": 61}]}', '{"servers": [{"name": "a", "host": "h", "user": ""}], "defaults": {"user": "root"}}',
    '{"servers": [{"name": "a", "host": "h"}], "defaults": {"user": "root", "port": 2200, "timeout_seconds": 3}}',
    '{"servers": [{"name": " a ", "host": " h "}, {"name": "a", "host": "h"}]}',
    '{"servers": [{"name": "' + "\u00e9" * 61 + '", "host": "h"}]}',
]


def validate_vectors():
    return [{"json": text, "out": outcome(config.validate, json.loads(text))} for text in VALIDATE_JSON]


def public_vectors():
    rows = []
    for text in ['{}', '{"name": "a", "secret": 1, "host": "h", "port": 22}',
                 '{"timeout_seconds": 1, "enabled": false, "note": "", "tags": [], "user": "u", "port": 1, "host": "h", "name": "n", "x": 1}']:
        rows.append({"json": text, "out": json.dumps(config.public_server(json.loads(text)), ensure_ascii=False)})
    return rows


# ---------------------------------------------------------------- load

def load_vectors():
    tmp = Path(tempfile.mkdtemp(prefix="gpuvec"))
    try:
        return _load_vectors(tmp)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


LOAD_FILES = {
    "ok.yaml": ("\ufeff" + GOOD).encode("utf-8"),
    "nobom.yaml": GOOD.encode("utf-8"),
    "bad.yaml": b"enabled: true\nnote: \xff\n",
    "surrogate.yaml": b"a: \xed\xa0\x80\n",
    "empty.yaml": b"",
    "bom-only.yaml": b"\xef\xbb\xbf",
    "double-bom.yaml": b"\xef\xbb\xbf\xef\xbb\xbfa: 1\n",
    # big.yaml / exact.yaml are rebuilt by config_test.go, not stored.
    "big.yaml": b"#" * (256 * 1024 + 1),
    "exact.yaml": (b"#" * (256 * 1024 - 20) + b"\nenabled: true\n").ljust(256 * 1024, b" "),
    "invalid.yaml": b"servers: x\n",
    "sub/ok.yaml": GOOD.encode("utf-8"),
    "file.txt": b"x",
}

LOAD_PATHS = [
    "{D}/ok.yaml", "{D}/nobom.yaml", "{D}/bad.yaml", "{D}/surrogate.yaml", "{D}/empty.yaml", "{D}/bom-only.yaml",
    "{D}/double-bom.yaml", "{D}/big.yaml", "{D}/exact.yaml", "{D}/invalid.yaml", "{D}/missing.yaml",
    "{D}/sub", "{D}", "{D}/sub/./ok.yaml", "{D}//sub//ok.yaml/", "{D}/sub/../ok.yaml", "{D}/file.txt/x",
    "{D}/nodir/x.yaml", "{D}/a<b.yaml", "{D}/a\"b.yaml", "~/ok.yaml", "~", "~/missing.yaml",
    "~nobody/x.yaml", "~nobody", "{D}/a\x00b.yaml", "{D}/" + "x" * 300, "{D}/./sub/ok.yaml",
]


def _load_vectors(tmp):
    for name, data in LOAD_FILES.items():
        path = tmp / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    os.environ["USERPROFILE"] = str(tmp)
    os.environ["HOME"] = str(tmp)
    d = str(tmp)
    dr = d.replace("\\", "\\\\")
    rows = []
    for raw in LOAD_PATHS:
        res = outcome(config.load_config, raw.replace("{D}", d), trace=False)
        if "err" in res:
            res["err"] = res["err"].replace(dr, "{DR}").replace(d, "{D}")
        rows.append({"path": raw, "files": None, "out": res})
    files = {k: v.decode("latin-1") for k, v in LOAD_FILES.items() if k not in ("big.yaml", "exact.yaml")}
    return {"files": files, "rows": rows}


# ------------------------------------------------------------- dispatch

SERVER_CONFIGS = {
    "one": {"servers": [{"name": "gpu-a", "host": "gpu-a.example.com", "port": 2222, "user": "ubuntu",
                         "tags": [4090], "note": "n"}]},
    "many": {"defaults": {"user": "root"},
             "servers": [{"name": "gpu-a", "host": "10.0.0.1"}, {"name": "gpu-b", "host": "fe80::1", "user": ""},
                         {"name": "gpu-dis", "host": "h", "enabled": False}, {"name": "off", "host": "h2", "port": 22}]},
    "nouser": {"servers": [{"name": "solo", "host": "solo.local", "user": ""}]},
}

FIELD_POOLS = {
    "server": [None, "", "gpu-a", " gpu-a ", "gpu-b", "off", "nope", 5, True, False, [1], {"a": 1}, "gpu-dis",
               "solo", 1.0, "gpu-a\n"],
    "gpus": [None, "", "0,1", "0 1", " 3 ,, 4 ", "\uff11", "0_1", "1__2", "16", "-1", "+2", "a", [0, 1], [0, 0],
             [True], [1.9], [-0.5], ["2"], [" 3 "], [None], [[1]], [{"a": 1}], [1e300], [float("nan")],
             [float("inf")], "0,1,2,3,4,5,6,7,8", list(range(9)), 5, True, {"a": 1}, [99999999999999999999999],
             "99999999999999999999", "\u0663\u2028\u0664", "1,1", [15, 16], [], "  ", "1\u00a02", [-1e300],
             [1, "1"], ["\u0661_\u0662"], ["1_"], ["_1"], [""], ["0x1"], ["\u00b2"]],
    "gpu_indices": [None, "2", [3, 4], "x"],
    "timeout_minutes": [None, 60, 0, 1440, 1441, "30", " 30 ", "3_0", "30.0", 30.9, -0.5, 1e300,
                        float("nan"), float("inf"), float("-inf"), True, False, [1], "", "\u0663\u0660",
                        99999999999999999999, 1.0, "-5", {"a": 1}, "\uff16\uff10"],
    "job": [None, "", "ark-job", " my.job ", "bad job", "x" * 64, "x" * 65, 5, 1.5, True, "a\n", [1],
            {"k": 1}, "caf\u00e9", 0, "j\u3000"],
    "project": [None, "", "proj_1", "bad/proj", 1e16, False, "p" * 65],
    "work_dir": [None, "", "/data/x", " /data/x ", "rel", "/a/../b", "/a/..b", "/a\u0001", 5, "/" + "x" * 240,
                 "/" + "x" * 239, "/\u00fcn\u00ef c\u00f6d\u00e9", "/it's", "/a/$(rm -rf)", "/..", False, 0, [],
                 "/a\u0085"],
    "command": [None, "", "  ", "python train.py --lr 1e-3", "echo 'hi'", "a\x00b", "a\x01b",
                "line1\nline2\tx", "\x1fpython\x1f", 5, "x" * 16385, "x" * 16384, "\u4e2d\u6587 \u547d\u4ee4",
                "run\r\nx", "safe-cmd", "a\x1fb", ["ls"], "it's \"quoted\""],
    "env": [None, "", {}, {"A": "1"}, {"B": 2, "A": True, "C": 1.5, "D": False}, {"1A": "x"}, {"A\n": "x"},
            {"A": None}, {"A": [1]}, {"A": "x" * 201}, {"A": "x" * 200}, {"A": "a\x00"},
            {f"V{i}": str(i) for i in range(17)}, {f"V{i}": str(i) for i in range(16)}, [1], "x",
            {"Z": "has space", "a_b": "it's", "_": ""}, {"A": 1e16}, {"A": 4090.0}, {"A": {"x": 1}},
            {"\u00e9": "x"}, {"A" * 64: "x"}, {"A" * 65: "x"}, {"A": 99999999999999999999}, {"A": float("nan")},
            {"b": "1", "B": "2", "_a": "3"}, 0, False, {"A": "\u4e2d"}],
    "execute": [None, False, True, 0, 1, "", "yes"],
    "launch": [None, False, True, [], [0]],
}


GOOD_VALUES = {
    "server": ["gpu-a", "gpu-b", "off", "solo", " gpu-a "], "gpus": ["0,1", [2, 3], "", None, "7 5", [1.9]],
    "timeout_minutes": [30, "45", 1440, 1.5], "job": ["train-1", None, 5], "project": ["p.x", None],
    "work_dir": ["/data/run 1", "/srv/it's", None, "/a/b"], "command": ["python train.py --x 'y'", "nvidia-smi"],
    "env": [{"B": "2", "A": "x y"}, {"HF_HOME": "/d", "N": 3, "T": True}, None],
}


def small(value):
    return len(json.dumps(value, ensure_ascii=False)) < 120


def rand_body(rng):
    body = {}
    for key, pool in FIELD_POOLS.items():
        p = {"execute": 0.1, "launch": 0.05, "gpu_indices": 0.15}.get(key, 0.6)
        if rng.random() < p:
            if key in GOOD_VALUES and rng.random() < 0.75:
                body[key] = rng.choice(GOOD_VALUES[key])
            else:
                body[key] = rng.choice([v for v in pool if small(v)])
    if rng.random() < 0.85:
        body.setdefault("command", "python train.py")
        if body["command"] is None and rng.random() < 0.8:
            body["command"] = "nvidia-smi"
    return body


def dispatch_vectors(rng):
    servers = {name: config.validate(cfg)["servers"] for name, cfg in SERVER_CONFIGS.items()}
    servers["none"] = []
    bodies = [{}, {"command": "x"}, {"server": "gpu-a", "command": "x"}, [], "x", None]
    for key, pool in FIELD_POOLS.items():
        for value in pool:
            bodies.append({"server": "gpu-a", key: value, "command": "python run.py"} if key != "command"
                          else {"server": "gpu-a", key: value})
    bodies += [rand_body(rng) for _ in range(1000)]
    rows = []
    for i, body in enumerate(bodies):
        names = list(servers) if i < 6 else [rng.choice(["one", "many", "many", "nouser", "none"])]
        if i >= 6 and isinstance(body, dict) and "server" not in body:
            names = [rng.choice(["one", "many", "nouser"])]
        for name in names:
            res = outcome(_build, body, servers[name])
            rows.append({"body": json.dumps(body, ensure_ascii=False), "servers": name, "out": res})
    return {"servers": {k: json.dumps(v, ensure_ascii=False) for k, v in servers.items()}, "rows": rows}


def _build(body, servers):
    out = dispatch.build_request(body, servers)
    out["generated_at"] = "NOW"
    return out


def helper_vectors(rng):
    values = []
    for pool in FIELD_POOLS.values():
        values.extend(pool)
    rows = []
    for v in values:
        text = json.dumps(v, ensure_ascii=False)
        row = {"json": text}
        for fn, key in ((dispatch._command, "command"), (dispatch._work_dir, "work_dir"),
                        (dispatch._env, "env"), (dispatch._gpus, "gpus")):
            row[key] = outcome(fn, v)
        rows.append(row)
    pieces = ["", "a", "A-z_0.9", "it's", "a b", "@%+=:,./-", "\u00e9", "\u4e2d", "x\ny", "$HOME", "*", "'",
              '"', "\\", "~", "a=b", "!", "#", "&&", "\t", "[x]", "{a}", "^", "a\x00b"]
    quote = [{"s": p, "q": shlex.quote(p)} for p in pieces]
    joins = []
    for _ in range(300):
        args = [rng.choice(pieces) for _ in range(rng.randint(0, 5))]
        joins.append({"args": args, "line": shlex.join(args)})
    jobs = []
    for s in ["", "a", "a" * 64, "a" * 65, "a\n", "a\n\n", "a\r", "\n", "a.b-c_d", "a b", "\u00e9", "a" * 64 + "\n",
              "\uff21", "a/b", "a\u2028"]:
        jobs.append({"s": s, "m": bool(dispatch.JOB_RE.match(s))})
    return {"helpers": rows, "quote": quote, "join": joins, "job": jobs}


def main():
    rng = random.Random(20261114)
    out = {
        "yaml": yaml_vectors(rng),
        "recursion": recursion_vectors(),
        "recursion_limit": recursion_thresholds(),
        "validate": validate_vectors(),
        "public": public_vectors(),
        "load": load_vectors(),
        "dispatch": dispatch_vectors(rng),
    }
    out.update(helper_vectors(rng))
    (HERE / "vectors.json").write_text(json.dumps(out, ensure_ascii=False), encoding="utf-8")
    counts = {k: len(v) if isinstance(v, list) else len(v.get("rows", v)) for k, v in out.items()}
    print(counts)


main()
