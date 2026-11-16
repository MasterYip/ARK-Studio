"""CPython vectors for the Go port of session_transcript.build_transcript.

    python -B tests/python/contract/gen_transcript_vectors.py [--check]

Writes go/internal/sessions/testdata/transcript_vectors.json: the seeded
replay sessions' logs (replay_sessions.seed_sessions) plus a few edge cases,
each with the transcript CPython builds for every status.
"""
from __future__ import annotations

import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(REPO))
sys.path.insert(0, str(HERE))

from arkstudio import session_transcript  # noqa: E402
import replay_sessions  # noqa: E402

OUT = REPO / "go" / "internal" / "sessions" / "testdata" / "transcript_vectors.json"


def entries(path: Path) -> list:
    out = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        try:
            item = json.loads(raw)
        except ValueError:
            continue
        if isinstance(item, dict):
            out.append(item)
    return out


def build() -> list:
    cases = []
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        seeded = replay_sessions.seed_sessions(root, {"id": "p", "name": "P", "path": "/p"}, "a", "b")
        for name, sid in seeded.items():
            meta = json.loads((root / sid / "meta.json").read_text(encoding="utf-8"))
            for status in ("running", "stopped", "errored"):
                kwargs = dict(session_id=sid, status=status, prompt=meta.get("prompt") or "",
                              prompt_at=meta.get("started_at"), stdout=entries(root / sid / "stdout.jsonl"),
                              stderr=entries(root / sid / "stderr.jsonl"), error=meta.get("error") or "",
                              notice="restarted" if status == "stopped" else "", platform=meta.get("platform"))
                data = session_transcript.build_transcript(generated_at="<GEN>", **kwargs)
                cases.append({"input": kwargs, "output": data})
    edge = [{"t": "2026-01-01T00:00:00+00:00", "line": line} for line in (
        "[[ark-event]] {\"type\": \"tool_call\", \"name\": {\"x\": 1}, \"input\": [1, 2.5, null, true]}",
        "[[ark-event]] {\"type\": \"tool_result\", \"name\": 7, \"output\": {\"k\": \"v\"}}",
        "{\"type\": \"result\", \"usage\": {\"input_tokens\": \"9\", \"cached_tokens\": 3}}",
        "{\"usage\": {\"prompt_tokens\": -1, \"completion_tokens\": 2}}",
        "{\"type\": \"assistant\", \"message\": {\"text\": \"only text\"}}",
        "{\"type\": \"item.updated\", \"item\": {\"type\": \"reasoning\", \"text\": \"r\"}}",
        "{\"type\": \"item.completed\", \"item\": {\"type\": \"unknown\"}}",
        "{\"choices\": [5]}",
        "❯ not input",
        "x" * 10)]
    for status in ("running", "idle"):
        kwargs = dict(session_id="s_edge0000", status=status, prompt="", prompt_at=None, stdout=edge,
                      stderr=[{"t": "2026-01-01T00:00:00+00:00", "line": "plain error"}], error="", notice="",
                      platform="")
        cases.append({"input": kwargs, "output": session_transcript.build_transcript(generated_at="<GEN>", **kwargs)})
    return cases


def main(argv: list[str]) -> int:
    text = json.dumps(build(), ensure_ascii=False, indent=1) + "\n"
    if "--check" in argv:
        current = OUT.read_text(encoding="utf-8") if OUT.is_file() else ""
        if current != text:
            print(f"{OUT} is out of date; run gen_transcript_vectors.py")
            return 1
        return 0
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(text, encoding="utf-8", newline="\n")
    print(f"wrote {len(json.loads(text))} cases to {OUT}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
