"""Regenerate vectors.json from the Python implementation: python -B gen_vectors.py"""
import json
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / "python"))  # ark_studio/python (arkstudio)
from arkstudio.store import _NOTE_LINE, excerpt, markdown_excerpt  # noqa: E402

pieces = ["word", "中文", "😀", " ", "  ", " ", "　", "\x1c", "\x85", "\t", "\n", "\n\n",
          "\r\n", "# ", "## ", "> ", "- ", "* ", "1. ", "１. ", "12) ", "| ", "**b**", "__u__",
          "`c`", "~~s~~", "![alt](img.png)", "[link](http://x)", "```", "~~~", "$$", "---",
          "***", "   ", "    # deep", " ", "x" * 50, "很" * 90, "**User comment (2024-01-02):** hi",
          "**Agent reply (２０２４-01-02):** z"]
rng = random.Random(11)
bodies = ["", "a", "   ", "\n\n\n", "```\ncode\n```\nafter", "x " * 300, "中文" * 200]
for _ in range(400):
    bodies.append("".join(rng.choice(pieces) for _ in range(rng.randint(1, 40))))
out = []
for body in bodies:
    for limit in (240, 120, 320, 10):
        md, cut = markdown_excerpt(body, limit + 160)
        out.append({"body": body, "limit": limit, "excerpt": excerpt(body, limit),
                    "md_limit": limit + 160, "md": md, "md_cut": cut,
                    "note_stripped": _NOTE_LINE.sub("", body)})
ints = ["1", " 12 ", "+3", "-4", "１２", "1_000", "_1", "1__0", "²", "", "-", "1a", "٣", "0x10", "12 3"]
int_out = []
for text in ints:
    try:
        int_out.append({"in": text, "ok": True, "value": int(text), "isdigit": text.isdigit()})
    except ValueError as exc:
        int_out.append({"in": text, "ok": False, "error": str(exc), "isdigit": text.isdigit()})
Path(__file__).with_name("vectors.json").write_text(
    json.dumps({"excerpts": out, "ints": int_out}, ensure_ascii=True), encoding="utf-8")
