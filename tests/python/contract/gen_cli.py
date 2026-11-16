"""Snapshot the argparse tree of ``python -m arkstudio`` for the Go CLI.

    python -B tests/python/contract/gen_cli.py          # rewrite go/internal/pyargparse/cli_spec.json
    python -B tests/python/contract/gen_cli.py --check  # exit 1 when stale

The Go binary parses its command line with ``internal/pyargparse``, a port of
Python 3.10's ``argparse`` parse loop (abbreviations, ``--opt=value``, nargs
patterns, error texts, exit code 2).  The parser *structure* (every parser,
option string, dest, nargs, choices, required flag, value type) and the
rendered ``usage``/``--help`` texts come from this snapshot, so a flag added on
the Python side makes the inventory test fail until the snapshot is refreshed.
Defaults are runtime values (env, cwd) and are supplied by the Go commands.

Help texts are rendered with ``COLUMNS=80`` (argparse's width when stdout is
not a terminal); the CLI differential harness runs Python with the same width.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
TARGET = ROOT / "go" / "internal" / "pyargparse" / "cli_spec.json"


class _Grab(Exception):
    def __init__(self, parser):
        super().__init__("grabbed")
        self.parser = parser


def _kind(action) -> str:
    return {
        "_HelpAction": "help",
        "_StoreAction": "store",
        "_StoreTrueAction": "store_true",
        "_StoreFalseAction": "store_false",
        "_AppendAction": "append",
        "_SubParsersAction": "parsers",
    }[type(action).__name__]


def _record(parser: argparse.ArgumentParser) -> dict:
    actions = []
    for action in parser._actions:
        kind = _kind(action)
        item = {
            "kind": kind,
            "dest": action.dest,
            "option_strings": list(action.option_strings),
            "nargs": action.nargs if action.nargs is None or isinstance(action.nargs, (int, str)) else str(action.nargs),
            "required": bool(action.required),
            "type": getattr(action.type, "__name__", None) if action.type else None,
            "metavar": action.metavar,
        }
        if kind == "parsers":
            item["choices"] = list(action.choices)
            item["parsers"] = [_record(sub) for sub in action.choices.values()]
        else:
            item["choices"] = list(action.choices) if action.choices is not None else None
        actions.append(item)
    return {
        "prog": parser.prog,
        "usage": parser.format_usage(),
        "help": parser.format_help(),
        "actions": actions,
    }


def build() -> dict:
    sys.path.insert(0, str(ROOT))
    os.environ["COLUMNS"] = "80"
    from arkstudio import __main__ as cli

    original = argparse.ArgumentParser.parse_args

    def grab(self, args=None, namespace=None):
        raise _Grab(self)

    argparse.ArgumentParser.parse_args = grab
    try:
        cli.main([])
    except _Grab as grabbed:
        top = grabbed.parser
    finally:
        argparse.ArgumentParser.parse_args = original
    return _record(top)


def render(spec: dict) -> str:
    return json.dumps(spec, ensure_ascii=False, indent=1) + "\n"


VECTORS = ROOT / "go" / "cmd" / "ark-server" / "testdata" / "cli_vectors.json"
VECTOR_ENV = {"ARK_ROOT": "/ark-root", "COLUMNS": "80"}

# argv cases for the parse-only vectors (no command runs): abbreviations,
# --opt=value, nargs, choices, required options, extras, '--', numbers.
CASES = [
    [], ["-h"], ["--help"], ["--he"], ["foo"], ["--root", "x"], ["serve"], ["serve", "-h"],
    ["serve", "--port", "9"], ["serve", "--po", "9"], ["serve", "--port=9"], ["serve", "--p", "9"],
    ["serve", "--port", "x"], ["serve", "--port", " 12 "], ["serve", "--port", "1_0"], ["serve", "--port", "１２"],
    ["serve", "--port", "-5"], ["serve", "--port"], ["serve", "--port", "--root"], ["serve", "--root", "-x"],
    ["serve", "--expose=1"], ["serve", "--expose", "--no-demo"], ["serve", "--media-root", "a", "--media-root=b"],
    ["serve", "--media", "a"], ["serve", "--m", "a"], ["serve", "--auth-p", "x"], ["serve", "--auth-password-f", "f"],
    ["serve", "--zz"], ["serve", "extra"], ["serve", "--", "--port"], ["serve", "-x"], ["serve", "-hx"],
    ["serve", "--host", "a b"], ["serve", "--root", "a b"], ["serve", "--root", "-1"], ["serve", "--root", "-1.5"],
    ["serve", "--require-auth", "--tr", "--det"], ["serve", "--config", "c.json", "--host", "0.0.0.0"],
    ["serve", "--auth-session-ttl", "3600"], ["serve", "--port", "9", "--port", "10"], ["serve", "--port", "²"],
    ["serve", "--port", "'q\"z"], ["serve", "--port", "a\tb\x01é​"], ["serve", "--web-root", "w"],
    ["server-init"], ["server-init", "--config", "c"], ["server-init", "--config", "c", "--data-dir", "d", "--port", "99"],
    ["server-init", "--conf", "c", "--data", "d", "--own", "o", "--pass", "p", "--pub", "u"],
    ["desktop", "--no-s", "--loc"], ["remote"], ["remote", "--ssh", "h", "--no-open", "--ssh-port", "2"],
    ["remote", "--s", "h"], ["remote", "--ssh", "h", "--remote-p", "x"],
    ["mobile-tunnel"], ["mobile-tunnel", "--ssh", "u@h", "--bind", "::1", "--local-port", "0"],
    ["new"], ["new", "My paper"], ["new", "a", "b"], ["new", "My", "--template", "paper", "--desc", "d"],
    ["new", "--template", "x", "n"], ["new", "-n"], ["new", "--", "-n"], ["new", "-5"], ["demo", "--root", "r"],
    ["export"], ["export", "--project", "p"], ["export", "--project", "p", "--format", "bundle", "--units", "a,b",
                                                 "--depth", "2", "--media-root", "m", "--out", "o"],
    ["export", "--project", "p", "--format", "html"], ["export", "--pro", "p", "--dep", "x"],
    ["agent"], ["agent", "context"], ["agent", "context", "--project", "p"], ["agent", "ctx"],
    ["session-watch"], ["session-watch", "--id", "ark-ses-1", "--interval", "12.5", "--once"],
    ["session-watch", "--id", "x", "--interval", "abc"], ["session-watch", "--id", "x", "--interval", "1e1"],
    ["session-watch", "--id", "x", "--interval", "inf"], ["session-watch", "--id", "x", "--agent", "gpt"],
    ["session-watch", "--id", "x", "--t", "y"], ["session-watch", "--id", "x", "--token", "T"],
    ["skills"], ["skills", "list"], ["skills", "show"], ["skills", "show", "rrsi"], ["skills", "render", "a", "b",
                                                                                     "--context", "{}"],
    ["skills", "validate", "a"], ["skills", "validate", "a", "b", "--payload", "p", "--project-r", "r"],
    ["skills", "interface"], ["skills", "brief", "--pay", "x"], ["skills", "setup"], ["skills", "setup", "draft"],
    ["skills", "setup", "--project", "p", "discover", "--paths", "a", "b"], ["skills", "setup", "--paths"],
    ["skills", "setup", "--propose", "confirm", "--digest", "d", "--note", "n"], ["skills", "setup", "x"],
    ["skills", "setup", "state", "summary"], ["skills", "nope"], ["skills", "list", "--x"],
]


def portable(value):
    """The ARK_ROOT default as recorded in the vectors, independent of the OS.

    Both servers render the default root like ``str(Path(ARK_ROOT))``: on
    Windows that is ``\ark-root``, elsewhere ``/ark-root``. The vectors store
    the environment spelling; the Go test maps its native rendering back the
    same way, so only exactly that value is normalized.
    """
    native = str(Path(VECTOR_ENV["ARK_ROOT"]))
    if isinstance(value, str) and value == native:
        return VECTOR_ENV["ARK_ROOT"]
    if isinstance(value, list):
        return [portable(v) for v in value]
    return value


def vectors() -> list:
    import contextlib
    import io

    sys.path.insert(0, str(ROOT))
    saved = dict(os.environ)
    for key in list(os.environ):
        if key.startswith("ARK_"):
            del os.environ[key]
    os.environ.update(VECTOR_ENV)
    try:
        from arkstudio import __main__ as cli
        original = argparse.ArgumentParser.parse_args

        def grab(self, args=None, namespace=None):
            raise _Grab(self)

        out = []
        for case in CASES:
            argparse.ArgumentParser.parse_args = grab
            try:
                cli.main(list(case))
            except _Grab as grabbed:
                parser = grabbed.parser
            finally:
                argparse.ArgumentParser.parse_args = original
            stdout, stderr = io.StringIO(), io.StringIO()
            record = {"argv": case, "exit": None, "ns": None}
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                try:
                    ns = parser.parse_args(list(case))
                    if not ns.command:
                        ns = parser.parse_args(["serve"] + list(case))
                    # floats as "f:<repr>" (JSON has no inf/nan)
                    record["ns"] = {k: f"f:{v!r}" if isinstance(v, float) else portable(v)
                                    for k, v in sorted(vars(ns).items())}
                except SystemExit as exc:
                    record["exit"] = exc.code
            record["stdout"], record["stderr"] = stdout.getvalue(), stderr.getvalue()
            out.append(record)
        return out
    finally:
        os.environ.clear()
        os.environ.update(saved)


def render_vectors(items: list) -> str:
    return json.dumps({"env": VECTOR_ENV, "cases": items}, ensure_ascii=False, indent=1) + "\n"


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    outputs = {TARGET: render(build()), VECTORS: render_vectors(vectors())}
    if "--check" in argv:
        stale = False
        for target, text in outputs.items():
            # read_text uses universal newlines, so a CRLF checkout compares equal.
            current = target.read_text(encoding="utf-8") if target.exists() else ""
            if current != text:
                print(f"{target} is stale; run gen_cli.py", file=sys.stderr)
                stale = True
        return 1 if stale else 0
    for target, text in outputs.items():
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8", newline="\n")
        print(f"wrote {target}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
