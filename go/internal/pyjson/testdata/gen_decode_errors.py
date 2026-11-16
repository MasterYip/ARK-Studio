"""CPython json.loads error texts for DecodeErrorMessage (decodeerr_test.go).

    python -B go/internal/pyjson/testdata/gen_decode_errors.py
"""
import json
import random
from pathlib import Path

BS = "\\"
cases = ['', ' ', '{', '}', '[', ']', '[1', '[1,', '[1,]', '[,]', '{"a"', '{"a":', '{"a":1', '{"a":1,',
         '{"a":1,}', '{"a" 1}', '{1:2}', "{'a':1}", 'nope', 'tru', 'nul', 'NaN', 'Na', '-Infinity', '-Inf',
         '-', '-a', '01', '1.', '1.e5', '1e', '1e+', '1E-3x', '.5', '"abc', '"a' + BS, '"a' + BS + 'x"',
         '"' + BS + 'u12"', '"' + BS + 'u12g4"', '"' + BS + 'ud800' + BS + 'u12x4"',
         '"' + BS + 'ud800' + BS + 'uDC00"', '"' + BS + 'ud800"', '"a\nb"', '"a\tb"', '"\x00"',
         '[1]\n[2]', '\n\n  {"a": [1, 2,\n  3,, 4]}', '{"é": "ü", x}', '["\U0001f600", ]', '[1 2]',
         '{"a":1 "b":2}', '[[[[', '{"a":{"b":', 'true false', '  1  ', '[\r\n1\r\n,\r\n]',
         '"' + BS + 'u"', '"' + BS + 'u0', '"' + BS + 'u00', '"' + BS + 'u000', '"' + BS + 'u0000"',
         '"' + BS + 'ud800' + BS + 'u', '"' + BS + 'ud800' + BS + 'u1', '"' + BS + 'ud800' + BS + 'u123',
         '"' + BS + 'ud800' + BS + 'u1234', '{"a":-}', '{"a":[}', '[{]', '1 2', 'Infinity', 'Infinityx',
         '"x"y', '{}x', '[]]', '-0', '-01', '0.0e', '2e3.5', 'é', '"é', '[\U0001f600]']
rng = random.Random(7)
alphabet = list('{}[]:,"' + BS + ' \n0123-eE.ntrufalsNIu"x\té')
for _ in range(600):
    cases.append(''.join(rng.choice(alphabet) for _ in range(rng.randint(1, 12))))
out = []
for text in dict.fromkeys(cases):
    try:
        json.loads(text)
        msg = ""
    except json.JSONDecodeError as exc:
        msg = str(exc)
    out.append([text, msg])
Path(__file__).with_name("decode_errors.json").write_text(
    json.dumps(out, ensure_ascii=False, indent=0), encoding="utf-8")
print(len(out))
