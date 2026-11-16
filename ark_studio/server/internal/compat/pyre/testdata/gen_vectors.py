"""Regenerate vectors.json from CPython re: python -B gen_vectors.py

Each case: pattern, flags (0 or re.IGNORECASE), subject -> finditer spans,
or the str() of re.error / the exception type for compile failures.
"""
import json
import random
import re
import warnings
from pathlib import Path

warnings.simplefilter("ignore")
patterns = [
    r"abc", r"a|b|c", r"ab|abc", r"(ab|abc)d", r"a*", r"a+?", r"a{2,3}", r"a{,2}", r"a{2}", r"a{3,2}", r"a{",
    r"a{1", r"a{x}", r"{}", r"x{}", r"[abc]", r"[^abc]", r"[a-c]", r"[c-a]", r"[]a]", r"[^]a]", r"[a-]", r"[-a]",
    r"[\w-]", r"[\d]", r"[", r"[a", r"a]", r"(", r")", r"(a", r"a)", r"(?:a|b)+", r"(?P<n>a)(?P=n)", r"(?P<n>a",
    r"(?P<1>a)", r"(?P=x)", r"(?Px)", r"(a)\1", r"(a)\2", r"\1", r"(?=a)", r"(?!a)b", r"(?<=a)b", r"(?<!a)b",
    r"(?<=a+)b", r"(?<=ab|c)d", r"(?<=ab|cd)e", r"(?i)abc", r"(?i:a)b", r"(?-i:a)b", r"(?x) a b # c",
    r"(?x)a\ b", r"(?s).", r".", r"(?m)^a$", r"^a$", r"a$", r"\Aa\Z", r"\bword\b", r"\Bor\B", r"\w+", r"\W+",
    r"\d+", r"\D", r"\s+", r"\S+", r"\x41", r"\x4", r"é", r"\u00", r"\U0001F600", r"\U00110000",
    r"\N{EM DASH}", r"\0", r"\07", r"\101", r"\400", r"\8", r"\e", r"\q", "\\", "a\\", r"*a", r"a**", r"a*+",
    r"+", r"?", r"a??", r"(?#comment)a", r"(?#unterminated", r"(?", r"(?<", r"(?<x)", r"(?z)", r"(?i",
    r"(?L)a", r"(?au)a", r"(?a)\w+", r"(?-x:a)", r"(?i-i:a)", r"(?t)a*", r"(?(1)a|b)", r"(a)?(?(1)b|c)",
    r"(?(x)a)", r"(?(0)a)", r"(a)(?(1)b|c|d)", r"x*?y", r"(a|ab)(c|bcd)(d*)", r"(a*)*", r"(a*)+b", r"(?:)+",
    r"()", r"a|", r"|a", r"[\b]", r"\b", r"[a-\w]", r"[\w-a]", "é", "(?i)É", "(?i)ſ", r"(?i)s", r"(?i)k",
    r"(?i)[a-z]+", r"(?i)[^a-z]", "(?i)[à-ÿ]", "中文", "😀+", "(?i)σ", "(?i)Σ+", r"\d{2,}", r"[\s\S]", r"a.c",
    r"(?<!\w)(?:ab)(?!\w)", r"(?<!\w)(?:a)|(b)(?!\w)", r"(?<!\w)(?:[)(?!\w)", r"(?<!\w)(?:a(b)(?!\w)",
    "a\nb(", "x(\ny", r"(?<=\b)a", r"(?<=(a))b", r"(?<=(?P<x>a))(?P=x)", r"(a)(?<=\1)", r"(?i)(a)\1",
    r"[\x00-\x7f]+", r"[^\x00-\x7f]+", r"a{99999999999}", r"(?P<a>x)(?P<a>y)", r"(?P<a b>x)", r"[a-",
    r"(?i)[k]", r"(?i)K", r"(?a)\b\w+\b", r"(?<=a|bc)x", r"(?:a|b|c)", r"(?:ab|ac)", r"[a]",
]
subjects = ["", "abc", "abcd abcd", "aaa", "AbC aBc ABC", "word words sword word", "a b\nc", "café CAFÉ",
            "中文 中文字", "😀😀 x", "ſs S", "KkK", "123 ٣٤ x9", "aab abab", "ab abcd", "Σσς", "x_y z-w",
            "a\n", "\n", "tab\there", "ba", "aa", "bcx ax", "éÉàÀÿŸ"]
rng = random.Random(3)
cases = []
for p in patterns:
    for flags in (0, re.IGNORECASE):
        try:
            c = re.compile(p, flags)
        except re.error as e:
            cases.append({"p": p, "f": flags, "error": str(e)})
            continue
        except Exception as e:  # noqa: BLE001
            cases.append({"p": p, "f": flags, "error": type(e).__name__ + ": " + str(e)})
            continue
        cases.append({"p": p, "f": flags, "empty": bool(c.search("")),
                      "runs": [[s, [list(m.span()) for m in c.finditer(s)]] for s in subjects]})
Path(__file__).with_name("vectors.json").write_text(json.dumps(cases, ensure_ascii=True), encoding="utf-8")
print(len(cases))
