"""Regenerate vectors.json from CPython: python -B gen_vectors.py"""
import json
import random
import shlex
import subprocess
from pathlib import Path

PIECES = ['a', 'b c', '"q x"', "'s y'", '\\', '\\"', '"', "'", ' ', '\t', '\n', 'C:\\Program Files\\x.exe',
          '"C:\\a b\\c.exe" --flag', '{prompt}', 'x"y', "it's", '""', "''", '\u00e9', '\u4e2d \u6587', '\\\\',
          'a\\ b', '-p']


def main():
    rng = random.Random(7)
    texts = PIECES + ["".join(rng.choice(PIECES) for _ in range(rng.randint(1, 6))) for _ in range(800)]
    out = {"shlex": [], "cmdline": [], "capitalize": []}
    for t in texts:
        row = {"text": t}
        for posix in (True, False):
            try:
                row["posix" if posix else "nt"] = shlex.split(t, posix=posix)
            except ValueError as exc:
                row["posix_err" if posix else "nt_err"] = str(exc)
        out["shlex"].append(row)
    for _ in range(400):
        args = [rng.choice(PIECES + [""]) for _ in range(rng.randint(1, 4))]
        out["cmdline"].append({"args": args, "line": subprocess.list2cmdline(args)})
    for t in ["version probe exited with 1", "\u00c9COLE Abc", "\u01c6x", "\u00dfa", "", "1abc DEF",
              "\ufb01x", "\u0130X"]:
        out["capitalize"].append({"in": t, "out": t.capitalize()})
    Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=False), encoding="utf-8")


main()
