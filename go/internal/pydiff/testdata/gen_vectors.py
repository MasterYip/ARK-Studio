"""Regenerate vectors.json from CPython difflib: python -B gen_vectors.py"""
import json
import random
from difflib import SequenceMatcher
from pathlib import Path

rng = random.Random(7)
cases = [("", ""), ("abc", ""), ("", "abc"), ("abcd", "acbd"), ("中文😀x", "中x😀文"),
         ("a" * 300 + "b", "a" * 250 + "c" + "a" * 60), ("the quick brown fox", "the quack brown fix")]
alphabet = "ab cdé\n中"
for n in range(60):
    la, lb = rng.randint(0, 400), rng.randint(0, 400)
    a = "".join(rng.choice(alphabet) for _ in range(la))
    b = "".join(rng.choice(alphabet) for _ in range(lb))
    if n % 3 == 0:
        b = a[:la // 2] + b[: lb // 3] + a[la // 2:]
    cases.append((a, b))
out = [{"a": a, "b": b, "ops": SequenceMatcher(None, a, b).get_opcodes()} for a, b in cases]
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=True), encoding="utf-8")

# unified_diff over str.splitlines(keepends=True), as workspace.diff_file uses it.
from difflib import unified_diff
lines = ["x\n", "y\n", "z\n", "same\n", "中\n", "end", "\r\n", "a\rb\n", "\x0c\n"]
uni = [("", ""), ("a\n", ""), ("", "a\nb"), ("a\nb\nc\n", "a\nB\nc\n"),
       ("".join(f"l{i}\n" for i in range(40)), "".join(f"l{i}\n" for i in range(40) if i not in (5, 20, 21)) + "tail"),
       ("same\n" * 300, "same\n" * 150 + "diff\n" + "same\n" * 150)]
for n in range(40):
    a = "".join(rng.choice(lines) for _ in range(rng.randint(0, 60)))
    b = "".join(rng.choice(lines) for _ in range(rng.randint(0, 60)))
    if n % 2 == 0:
        b = a[: len(a) // 2] + b[:10] + a[len(a) // 2:]
    uni.append((a, b))
uout = [{"a": a, "b": b, "diff": "".join(unified_diff(a.splitlines(keepends=True), b.splitlines(keepends=True),
                                                     fromfile="a/f", tofile="/dev/null" if n % 5 == 4 else "b/f", n=3)),
         "to": "/dev/null" if n % 5 == 4 else "b/f", "lines": a.splitlines()}
        for n, (a, b) in enumerate(uni)]
Path(__file__).with_name("unified.json").write_text(json.dumps(uout, ensure_ascii=True), encoding="utf-8")
