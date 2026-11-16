"""Regenerate vectors.json from arkstudio.qr: python -B gen_vectors.py"""
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / "python"))  # ark_studio/python (arkstudio)
from arkstudio import qr  # noqa: E402

payloads = ["a", "https://ark.example.net/#pair=arkp_abc", "中文 😀 pairing", "x" * 100, "y" * 180,
            "http://192.168.1.20:8890/#pair=" + "Z" * 43, "z" * 213]
out = [{"data": p, "svg": qr.to_svg(p)} for p in payloads]
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, ensure_ascii=True), encoding="utf-8")
