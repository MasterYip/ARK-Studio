"""Regenerate vectors.json from CPython: python -B gen_vectors.py (ark_studio/python on sys.path)."""
import json
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / "python"))  # ark_studio/python (arkstudio)
from arkstudio.secret_redact import redact_text, redact_value  # noqa: E402

PIECES = [
    "Authorization: Bearer abcdefghijklmnop123", "bearer short", "BEARER ABC.def-ghi_jkl~mno",
    "x bearer zzzzzzzzzzzzzzzz", "中bearer abcdefghijklmnop", "arka_0123456789abcdefXYZ", "中arka_0123456789abcdefgh",
    "sk-ant-api03-abcdefghijk_LMN", "sk-proj-abcdefghijklmnopqrstuvwxyz", "sk-1234567890abcdefghij",
    "ghp_abcdefghijklmnopqrstuvwxyz0123", "github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
    "AKIAABCDEFGHIJKLMNOP", "AKIAABCDEFGHIJKLMNOPQ", "ASIA0123456789ABCDEF", "xoxb-1234567890-abc",
    "AIzaSyA0123456789abcdefghijklmnopqrstu", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk",
    "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----",
    "-----BEGIN RSA PRIVATE KEY-----\nabc\n", "-----BEGIN OPENSSH PRIVATE KEY-----", 
    'password = "hunter2abc9"', "PASSWORD=abcdefgh1", "api_key: 'xyzxyz123456'", '"apiKey": "abc123def456"',
    "secret=${SECRET_VALUE1}", "token = process.env.TOKEN1", "auth_token=getenv(X12345678)",
    "access-token: ********", "refresh_token=xxxxxxxxxx", "my.db_password: <redacted1>", "passwd=onlyletters",
    "passwd=12345678", "client_secret: [REDACTED:x]1", "Ｐassword=abc12345678", "pässword=abc12345678",
    "ſecret=abcdefg12345", "SECRET\u212a=abc12345678", "key=val", "\n", " ", "，", "日本語テキスト", "😀",
    "x", "1", "_", "-", ".", "\t", "'", '"', "`", ";", "{", "}", "=", ":",
]

def rand_text(rng):
    return "".join(rng.choice(PIECES) + rng.choice(["", " ", "\n", "", "_", "中"]) for _ in range(rng.randint(1, 6)))

def main():
    rng = random.Random(20261114)
    cases = []
    texts = list(PIECES) + [rand_text(rng) for _ in range(600)]
    extras = [[], ["supersecrettoken1"], ["short", "abcdefgh1234"]]
    for i, text in enumerate(texts):
        extra = extras[i % 3]
        if extra and i % 2:
            text = text + extra[-1] + " " + extra[-1]
        out, n = redact_text(text, extra)
        cases.append({"text": text, "extra": extra, "out": out, "n": n})
    value = {"a": ["password=abc123456", {"k": "sk-ant-api03-abcdefghijk_LMN", "n": 1}], "password=abc123456": True}
    out, n = redact_value(value, ["supersecrettoken1"])
    doc = {"text": cases, "value": {"in": value, "out": out, "n": n}}
    Path(__file__).with_name("vectors.json").write_text(json.dumps(doc, ensure_ascii=False, indent=0), encoding="utf-8")

main()
