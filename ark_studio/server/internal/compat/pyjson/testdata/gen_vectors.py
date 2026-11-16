"""Regenerate vectors.json from CPython (the reference): python -B gen_vectors.py"""
import json
import math
import random
from pathlib import Path

inputs = [
    '{"b": 1, "a": 2, "b": 3}', '[]', '{}', '[[], {}, [{}]]', 'null', 'true', '"x"',
    '{"f": 1.0, "g": 1e16, "h": 1e15, "i": 1e-05, "j": 0.0001, "k": -0.0, "l": 123456789012345678}',
    '[1.5, 2.25e-7, 1E400, -1e400, 3.14159265358979, 100000000000000000000, -0, 0.1, 1e22, 5e-324]',
    '[NaN, Infinity, -Infinity]', '"\\u00e9\\u2028\\ud83d\\ude00 raw é 😀"', '"\\u0001\\u001f\\u007f\\b\\f\\n\\r\\t\\/"',
    '"<&>\'\\""', '{"nested": {"z": [1, {"y": null}], "a": "中文"}}', '  [1 , 2 ]  ',
    '{"k": 1e2, "m": 12.0, "n": 0.30000000000000004, "o": 1.7976931348623157e308}',
    '[123e-2, 9007199254740993, 18446744073709551616, -9223372036854775809]',
]
rng = random.Random(7)
for _ in range(300):
    x = rng.choice([rng.random() * 10 ** rng.randint(-30, 30), rng.uniform(-1e6, 1e6),
                    float(rng.randint(-10 ** 18, 10 ** 18)), rng.random()])
    inputs.append(json.dumps([x, -x, x / 3]))
bad = ['{"a" 1}', '[1,]', '{"a": 1,}', '"\x01"', "'a'", '01', '1 2', '{"a": nan}', '', '[', '"\\x"']
vectors = []
for text in inputs:
    value = json.loads(text)
    vectors.append({"in": text, "dumps": json.dumps(value, ensure_ascii=False),
                    "indent": json.dumps(value, ensure_ascii=False, indent=2),
                    "str": str(value)})
out = {"ok": vectors, "bad": bad}
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=True, indent=0), encoding="utf-8")
