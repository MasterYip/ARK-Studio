#!/usr/bin/env python3
"""Audit ARK Studio's web UI for user-visible English that bypasses i18n.

Stdlib only. Scans ark_studio/web/js/**/*.js and ark_studio/web/*.html and reports:

* literals in UI positions (h() children, label/title/placeholder/... props,
  toast/confirmDialog/openModal arguments, textContent/title assignments,
  setAttribute("title"|"aria-label"|...)) that contain English words and are
  not wrapped in t();
* sentence-like English literals anywhere else (``loose`` findings);
* EN/ZH key parity in ark_studio/web/js/i18n.js;
* t("key") calls whose key is missing from EN or ZH.

Suppress a deliberate literal with a ``// i18n-ignore`` comment on the same
line (or on its own line directly above), or wrap a region in
``/* i18n-ignore-start */ ... /* i18n-ignore-end */``.

Usage:
  python scripts/dev/i18n_audit.py            # summary
  python scripts/dev/i18n_audit.py --list     # every finding
  python scripts/dev/i18n_audit.py --json     # machine-readable result
"""
from __future__ import annotations

import argparse
import bisect
import json
import re
import sys
from html.parser import HTMLParser
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
WEB = ROOT / "ark_studio" / "web"
I18N = WEB / "js" / "i18n.js"
# The UI dictionaries, one module per language (i18n.js loads the active one).
I18N_UI = [WEB / "js" / "i18n-en.js", WEB / "js" / "i18n-zh.js"]
# Translations of server-originated text (codes from ark_studio/python/arkstudio/messages.py):
# English templates, and the Chinese module loaded only for Chinese.
I18N_SERVER = WEB / "js" / "i18n_server.js"
I18N_SERVER_ZH = WEB / "js" / "i18n_server_zh.js"
I18N_FILES = (I18N, *I18N_UI, I18N_SERVER, I18N_SERVER_ZH)
DICT_NAMES = {"EN": "EN", "ZH": "ZH", "SERVER_EN": "EN", "SERVER_ZH": "ZH"}

# Words that stay as written in every language: products, brands, keys, units.
ALLOW_WORDS = {
    "ARK", "Studio", "DSH", "Codex", "Claude", "Code", "GPU", "GPUs", "CPU", "Mermaid", "LaTeX",
    "KaTeX", "Markdown", "Git", "GitHub", "Python", "JSON", "YAML", "CSV", "PDF", "PNG", "JPG",
    "SVG", "HTML", "CSS", "API", "URL", "URLs", "UID", "UIDs", "ID", "IDs", "QR", "PWA", "APK",
    "Android", "iOS", "Windows", "macOS", "Linux", "WSL", "SSH", "HTTP", "HTTPS", "LAN", "VRAM",
    "CUDA", "NVIDIA", "OpenAI", "Anthropic", "Gemini", "DeepSeek", "Qwen", "Ollama", "MCP",
    "Ctrl", "Cmd", "Alt", "Shift", "Esc", "Enter", "Tab", "Del", "Backspace", "Space",
    "PgUp", "PgDn", "Home", "End", "Meta", "Delete", "Win", "ms", "px", "KB", "MB", "GB", "TB", "MiB", "GiB",
    "fps", "dpi", "Termux", "WebView", "Edge", "Chrome", "VS", "Tailscale", "UTC",
    "HttpOnly", "SameSite", "Lax", "cookie",
    "TODO", "README", "AGENTS", "CLAUDE", "SKILL", "md", "py", "js", "ts", "png", "jpg", "svg",
    "pdf", "csv", "json", "yaml", "yml", "txt", "tex", "ipynb", "zip", "http", "https", "www",
}
ALLOW_STRINGS = {"·", "…", "—", "×", "OK"}

UI_PROPS = {
    "label", "title", "placeholder", "aria-label", "ariaLabel", "hint", "alt", "text",
    "tooltip", "confirmLabel", "cancelLabel", "subtitle", "desc", "description", "summary",
    "emptyText", "html", "heading", "caption", "help", "message",
}
UI_ASSIGN = {"textContent", "innerText", "title", "placeholder", "ariaLabel", "innerHTML", "alt"}
UI_ATTRS = {"title", "aria-label", "placeholder", "alt"}
# callee -> argument indexes that are user-visible text ("*" = index >= 1).
UI_CALLS = {
    "toast": {0}, "confirmDialog": {0}, "alert": {0}, "confirm": {0}, "prompt": {0, 1},
    "iconBtn": {1}, "emptyState": {0, 1}, "iconText": {1}, "h": "*",
    "openModal": set(), "showToast": {0},
}
SKIP_CALLEES = {
    "Error", "TypeError", "RangeError", "querySelector", "querySelectorAll", "closest",
    "matches", "getElementById", "addEventListener", "removeEventListener", "fetch",
    "getItem", "setItem", "removeItem", "require", "import", "createElement", "getPropertyValue",
    "setProperty", "postMessage", "dispatchEvent", "CustomEvent", "Event", "RegExp",
}
SKIP_OBJECTS = {"console", "classList", "localStorage", "sessionStorage", "dataset", "JSON"}
CODE_TAGS = {"code", "kbd", "pre", "samp"}
COMPARE_OPS = {"===", "!==", "==", "!=", "case", "in"}
CONTROL_KW = {"if", "for", "while", "switch", "catch", "function", "return", "typeof", "await", "with"}
REGEX_PREV_KW = {"return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw",
                 "case", "do", "else", "yield", "await"}
PUNCTS = sorted(
    [">>>=", "...", "===", "!==", "**=", "<<=", ">>=", ">>>", "&&=", "||=", "??=",
     "=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--", "+=", "-=", "*=", "/=",
     "%=", "&=", "|=", "^=", "**", "<<", ">>"], key=len, reverse=True)


class Tok:
    __slots__ = ("kind", "val", "pos", "quote", "static", "sub")

    def __init__(self, kind, val, pos, quote="", static="", sub=None):
        self.kind, self.val, self.pos, self.quote = kind, val, pos, quote
        self.static, self.sub = static, sub or []


def _unescape(raw: str) -> str:
    try:
        return json.loads('"' + raw.replace('"', '\\"').replace("\\'", "'").replace("\\`", "`")
                          .replace("\\$", "$").replace("\n", "\\n") + '"')
    except Exception:
        return raw


def tokenize(src: str, i: int = 0, stop_brace: bool = False):
    toks: list[Tok] = []
    n = len(src)
    depth = 0
    while i < n:
        c = src[i]
        if c in " \t\r\n﻿":
            i += 1
            continue
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        if c in "\"'":
            j = i + 1
            while j < n and src[j] != c:
                j += 2 if src[j] == "\\" else 1
            toks.append(Tok("str", _unescape(src[i + 1:j]), i, c))
            i = j + 1
            continue
        if c == "`":
            j = i + 1
            static, subs, buf = [], [], []
            while j < n and src[j] != "`":
                if src[j] == "\\":
                    buf.append(src[j:j + 2])
                    j += 2
                elif src.startswith("${", j):
                    sub, j = tokenize(src, j + 2, stop_brace=True)
                    subs.append(sub)
                    buf.append("{}")
                else:
                    buf.append(src[j])
                    j += 1
            static = "".join(buf)
            toks.append(Tok("tpl", static, i, "`", static, subs))
            i = j + 1
            continue
        if c.isalpha() or c in "_$":
            j = i + 1
            while j < n and (src[j].isalnum() or src[j] in "_$"):
                j += 1
            toks.append(Tok("ident", src[i:j], i))
            i = j
            continue
        if c.isdigit():
            j = i + 1
            while j < n and (src[j].isalnum() or src[j] in "._"):
                j += 1
            toks.append(Tok("num", src[i:j], i))
            i = j
            continue
        if c == "/":
            prev = toks[-1] if toks else None
            is_regex = prev is None or (prev.kind == "punct" and prev.val not in (")", "]", "}")) \
                or (prev.kind == "ident" and prev.val in REGEX_PREV_KW)
            if is_regex:
                j, in_class = i + 1, False
                while j < n:
                    ch = src[j]
                    if ch == "\\":
                        j += 2
                        continue
                    if ch == "\n":
                        break
                    if in_class:
                        in_class = ch != "]"
                    elif ch == "[":
                        in_class = True
                    elif ch == "/":
                        break
                    j += 1
                j += 1
                while j < n and src[j].isalpha():
                    j += 1
                toks.append(Tok("regex", src[i:j], i))
                i = j
                continue
        if stop_brace:
            if c == "{":
                depth += 1
            elif c == "}":
                if depth == 0:
                    return toks, i + 1
                depth -= 1
        for p in PUNCTS:
            if src.startswith(p, i):
                toks.append(Tok("punct", p, i))
                i += len(p)
                break
        else:
            toks.append(Tok("punct", c, i))
            i += 1
    return toks, i


# --------------------------------------------------------------------------- text tests
WORD_RE = re.compile(r"[A-Za-z][A-Za-z'’]+")
PLACEHOLDER_RE = re.compile(r"\{\w*\}")
CODE_ONLY_RE = re.compile(r"^[\w.:/#@?&=%+\-\[\]()*,|^$<>!~]+$")
TITLE_RE = re.compile(r"^[A-Z][a-z]+(?:[ /-][A-Za-z]+)*[.…:]?$")
SENTENCE_RE = re.compile(r"^[\W\d]*[A-Z][a-z’']*(?:[\s,]+[\w’'()…\-./?!:;]+)+")
HTML_TAG_RE = re.compile(r"<[^>]*>")
ENTITY_RE = re.compile(r"&[a-z]+;|&#\d+;")


def english_words(text: str) -> list[str]:
    text = PLACEHOLDER_RE.sub(" ", text)
    words = WORD_RE.findall(text)
    return [w for w in words if w not in ALLOW_WORDS and w.strip("'’") not in ALLOW_WORDS]


def is_code_like(text: str) -> bool:
    s = PLACEHOLDER_RE.sub("", text).strip()
    if not s:
        return True
    if "://" in s or s.startswith(("/", "./", "../", "#", ".", "--", "data:", "mailto:")):
        return True
    if not re.search(r"\s", s):
        if CODE_ONLY_RE.match(s) and re.search(r"[./:#_=@\[\]<>]|[a-z]-[a-z]|\d|^\w+\(\)$", s):
            return True
        if re.fullmatch(r"[a-z][a-z0-9]*[A-Z]\w*", s):  # camelCase identifier
            return True
        if re.fullmatch(r"[A-Z][a-z]+(?:-[A-Z][a-z]+)+", s):  # HTTP header name
            return True
    if re.search(r"[;{}]|=>|\bvar\(|\bcalc\(|\brgba?\(", s):
        return True
    return False


def ui_text_is_english(text: str, html: bool = False) -> bool:
    if text.strip() in ALLOW_STRINGS:
        return False
    if html:
        text = ENTITY_RE.sub(" ", HTML_TAG_RE.sub(" ", text))
    if is_code_like(text):
        return False
    return bool(english_words(text))


def looks_like_sentence(text: str) -> bool:
    s = PLACEHOLDER_RE.sub("x", text).strip()
    if is_code_like(s) or re.search(r"[<>`\\]", s):
        return False
    words = english_words(text)
    if TITLE_RE.match(s):  # "Board", "Show/hide filters": labels handed to local helpers
        return bool(words)
    if not SENTENCE_RE.match(s):
        return False
    return len(words) >= 2


# --------------------------------------------------------------------------- JS scan
class Frame:
    __slots__ = ("kind", "callee", "obj", "argi", "skip", "ui", "prop", "elem_start", "first_arg",
                 "parent", "assign_ui", "assign_line")

    def __init__(self, kind, parent=None, callee=None, obj=None, skip=False, ui=None):
        self.kind, self.parent, self.callee, self.obj = kind, parent, callee, obj
        self.argi, self.skip, self.ui = 0, skip, ui
        self.prop, self.elem_start, self.first_arg = None, True, None
        self.assign_ui, self.assign_line = None, -1


class FileScan:
    def __init__(self, path: Path, src: str):
        self.path, self.src = path, src
        self.nl = [m.start() for m in re.finditer("\n", src)]
        self.lines = src.split("\n")
        self.ignored = self._ignored_lines()
        self.findings: list[dict] = []
        self.keys: dict[str, int] = {}
        self.dyn_keys: list[str] = []

    def line_of(self, pos: int) -> int:
        return bisect.bisect_left(self.nl, pos) + 1

    def _ignored_lines(self) -> set[int]:
        out, on = set(), False
        for no, line in enumerate(self.lines, 1):
            if "i18n-ignore-file" in line:
                return set(range(1, len(self.lines) + 2))
            if "i18n-ignore-start" in line:
                on = True
            if on:
                out.add(no)
            if "i18n-ignore-end" in line:
                on = False
            elif "i18n-ignore" in line:
                out.add(no)
                if line.strip().startswith(("//", "<!--")):
                    out.add(no + 1)
        return out

    def add(self, tok: Tok, text: str, kind: str, ctx: str):
        line = self.line_of(tok.pos)
        if line in self.ignored:
            return
        self.findings.append({"file": rel(self.path), "line": line, "kind": kind, "ctx": ctx,
                              "text": text[:120]})


def ui_context(stack: list) -> str | None:
    """Describe the UI slot a literal in the innermost frame fills, or None."""
    top = stack[-1]
    if top.skip:
        return None
    if top.assign_ui:
        return top.assign_ui
    if top.kind == "{" and top.prop in UI_PROPS:
        return f"{top.prop}:"
    if top.kind == "(" and top.callee in UI_CALLS:
        spec = UI_CALLS[top.callee]
        if (spec == "*" and top.argi >= 1) or (spec != "*" and top.argi in spec):
            return f"{top.callee}()"
    if top.kind == "(" and top.callee == "setAttribute" and top.argi == 1 and top.first_arg in UI_ATTRS:
        return f"setAttribute({top.first_arg})"
    if top.kind == "[" and top.parent is not None:
        p = top.parent
        if p.kind == "(" and p.callee == "h" and p.argi >= 1 and not p.skip:
            return "h() child"
    if top.ui:
        return top.ui
    return None


def scan_tokens(fs: FileScan, toks: list, inherited_ui=None):
    stack = [Frame("root", ui=inherited_ui)]
    for idx, tok in enumerate(toks):
        top = stack[-1]
        prev = toks[idx - 1] if idx else None
        nxt = toks[idx + 1] if idx + 1 < len(toks) else None
        if top.assign_ui and prev is not None and fs.line_of(tok.pos) > top.assign_line and \
                not (prev.kind == "punct" and prev.val in ("?", ":", "+", "||", "??", "(", ",", "=")) and \
                not (tok.kind == "punct" and tok.val in ("?", ":", "+", "||", "??")):
            top.assign_ui = None
        if tok.kind == "punct":
            v = tok.val
            if v in "([{":
                callee = obj = None
                if v == "(" and prev is not None and prev.kind == "ident" and prev.val not in CONTROL_KW:
                    callee = prev.val
                    if idx >= 3 and toks[idx - 2].val in (".", "?.") and toks[idx - 3].kind == "ident":
                        obj = toks[idx - 3].val
                skip = top.skip or obj in SKIP_OBJECTS or callee in SKIP_CALLEES
                if top.kind == "(" and top.callee == "t":
                    skip = True  # t() arguments are keys / interpolation values
                top.elem_start = False
                stack.append(Frame(v, top, callee, obj, skip))
            elif v in ")]}":
                if len(stack) > 1:
                    stack.pop()
                stack[-1].elem_start = False
            elif v == ",":
                top.argi += 1
                top.elem_start, top.prop, top.assign_ui = True, None, None
            elif v == ";":
                top.assign_ui = None
            elif v == "=" and prev is not None and prev.kind == "ident" and prev.val in UI_ASSIGN \
                    and idx >= 2 and toks[idx - 2].val in (".", "?."):
                top.assign_ui = f".{prev.val} ="
                top.assign_line = fs.line_of(tok.pos)
            elif v != ":":
                top.elem_start = False
            continue
        if top.kind == "{" and top.elem_start and tok.kind in ("ident", "str", "num") and \
                nxt is not None and nxt.kind == "punct" and nxt.val == ":":
            top.prop = tok.val
            top.elem_start = False
            continue
        top.elem_start = False
        if tok.kind not in ("str", "tpl"):
            continue
        if top.kind == "(" and top.argi == 0 and top.first_arg is None and tok.kind == "str":
            top.first_arg = tok.val
        compared = (prev is not None and prev.val in COMPARE_OPS) or             (nxt is not None and nxt.kind in ("punct", "ident") and nxt.val in COMPARE_OPS)
        if top.kind == "(" and top.callee == "t" and top.argi == 0:
            if compared:
                continue
            if tok.kind == "str" and nxt is not None and nxt.val == "+":
                fs.dyn_keys.append(tok.val)
            elif tok.kind == "str":
                fs.keys.setdefault(tok.val, fs.line_of(tok.pos))
            else:
                fs.dyn_keys.append(tok.static.split("{}")[0])
                for sub in tok.sub:
                    scan_tokens(fs, sub)
            continue
        if top.kind == "(" and top.callee == "h" and top.argi == 0:
            continue  # tag name
        ctx = ui_context(stack)
        if ctx and top.kind == "(" and top.callee == "h" and top.first_arg in CODE_TAGS:
            ctx = None  # literal code shown as code
        text = tok.val if tok.kind == "str" else tok.static
        if compared:
            continue  # an operand of ===, a case label or an `in` test is data, not text
        if tok.kind == "tpl":
            for sub in tok.sub:
                scan_tokens(fs, sub, None if top.skip else ctx)
        if top.skip:
            continue
        is_html = ctx in ("html:", ".innerHTML =")
        if ctx and ui_text_is_english(text, html=is_html):
            fs.add(tok, text, "ui", ctx)
        elif not ctx and looks_like_sentence(text):
            fs.add(tok, text, "loose", "literal")


def rel(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


# --------------------------------------------------------------------------- HTML scan
VOID_TAGS = {"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source",
             "track", "wbr"}


class _HtmlScan(HTMLParser):
    """Static HTML text.  Elements marked data-i18n / data-i18n-aria are translated by script."""

    def __init__(self, fs: FileScan):
        super().__init__(convert_charrefs=True)
        self.fs, self.skip_depth, self.translated = fs, 0, [False]

    def _pos(self) -> int:
        line, col = self.getpos()
        return (self.fs.nl[line - 2] + 1 if line > 1 else 0) + col

    def handle_starttag(self, tag, attrs):
        if tag in ("script", "style"):
            self.skip_depth += 1
        names = dict(attrs)
        if tag not in VOID_TAGS:
            self.translated.append("data-i18n" in names)
        for name, value in attrs:
            if name == "aria-label" and "data-i18n-aria" in names:
                continue
            meta_text = tag == "meta" and name == "content" and                 dict(attrs).get("name") in ("description", "application-name", "apple-mobile-web-app-title")
            if value and (name in UI_ATTRS or meta_text):
                if ui_text_is_english(value):
                    self.fs.add(Tok("str", value, self._pos()), value, "ui", f"<{tag} {name}>")

    def handle_endtag(self, tag):
        if tag in ("script", "style") and self.skip_depth:
            self.skip_depth -= 1
        if tag not in VOID_TAGS and len(self.translated) > 1:
            self.translated.pop()

    def handle_data(self, data):
        if self.skip_depth or self.translated[-1] or not data.strip():
            return
        if ui_text_is_english(data.strip()):
            self.fs.add(Tok("str", data, self._pos()), " ".join(data.split()), "ui", "html text")


# --------------------------------------------------------------------------- dictionaries
def load_dicts() -> dict[str, dict[str, str]]:
    """EN/ZH UI dictionaries merged with the server-text dictionaries."""
    out: dict[str, dict[str, str]] = {}
    for path in (*I18N_UI, I18N_SERVER, I18N_SERVER_ZH):
        if path.exists():
            for lang, entries in parse_dicts(path.read_text(encoding="utf-8")).items():
                out.setdefault(lang, {}).update(entries)
    return out


def parse_dicts(src: str) -> dict[str, dict[str, str]]:
    toks, _ = tokenize(src)
    out: dict[str, dict[str, str]] = {}
    i = 0
    while i < len(toks) - 3:
        a, b, c, d = toks[i:i + 4]
        if a.kind == "ident" and a.val == "const" and b.kind == "ident" and c.val == "=" and d.val == "{" \
                and b.val in DICT_NAMES:
            entries: dict[str, str] = {}
            depth, j = 1, i + 4
            while j < len(toks) and depth:
                tk = toks[j]
                if tk.kind == "punct" and tk.val in "([{":
                    depth += 1
                elif tk.kind == "punct" and tk.val in ")]}":
                    depth -= 1
                elif depth == 1 and tk.kind == "str" and j + 2 < len(toks) and toks[j + 1].val == ":" \
                        and toks[j - 1].val in ("{", ","):
                    val = toks[j + 2]
                    entries[tk.val] = val.val if val.kind == "str" else val.static
                j += 1
            out.setdefault(DICT_NAMES[b.val], {}).update(entries)
            i = j
        else:
            i += 1
    return out


# --------------------------------------------------------------------------- driver
def source_files() -> list[Path]:
    files = sorted(p for p in (WEB / "js").rglob("*.js") if p not in I18N_FILES)
    files += sorted(WEB.glob("*.html"))
    return files


def audit(extra_keys: dict | None = None) -> dict:
    findings: list[dict] = []
    used: dict[str, str] = {}
    dyn: set[str] = set()
    for path in source_files():
        src = path.read_text(encoding="utf-8")
        fs = FileScan(path, src)
        if path.suffix == ".html":
            _HtmlScan(fs).feed(src)
        else:
            scan_tokens(fs, tokenize(src)[0])
        findings += fs.findings
        for key, line in fs.keys.items():
            used.setdefault(key, f"{rel(path)}:{line}")
        dyn.update(k for k in fs.dyn_keys if k)
    dicts = load_dicts()
    en, zh = dict(dicts.get("EN", {})), dict(dicts.get("ZH", {}))
    for key, pair in (extra_keys or {}).items():
        en.setdefault(key, pair.get("en", ""))
        zh.setdefault(key, pair.get("zh", ""))
    per_file: dict[str, int] = {}
    for f in findings:
        per_file[f["file"]] = per_file.get(f["file"], 0) + 1
    server = server_audit(en, zh)
    return {
        **server,
        "total": len(findings),
        "ui": sum(1 for f in findings if f["kind"] == "ui"),
        "loose": sum(1 for f in findings if f["kind"] == "loose"),
        "per_file": dict(sorted(per_file.items(), key=lambda kv: (-kv[1], kv[0]))),
        "findings": findings,
        "en_keys": len(en),
        "zh_keys": len(zh),
        "missing_in_zh": sorted(set(en) - set(zh)),
        "missing_in_en": sorted(set(zh) - set(en)),
        "used_missing_en": sorted(k for k in used if k not in en),
        "used_missing_zh": sorted(k for k in used if k not in zh),
        "used_keys": len(used),
        "dynamic_prefixes": sorted(dyn),
        "where": {k: used[k] for k in used if k not in en or k not in zh},
    }


# --------------------------------------------------------------------------- server text
# Server-originated English shown in the UI is translated client-side from the
# server.* templates in ark_studio/web/js/i18n_server.js (see i18n.js localizeServer /
# serverErrorText).  This section mirrors that matcher in Python and checks the
# server sources, so a new human-facing server message without an EN/ZH entry
# fails the audit.
ARK = ROOT / "ark_studio" / "python" / "arkstudio"
# Error raises in these files are agent-facing (skill internals, agent docs) or
# CLI-only (startup flags); they intentionally stay English.  launcher.py is
# the native Tk connection chooser: its errors go to a tkinter messagebox
# before any web page exists, so the web i18n dictionaries never see them.
SERVER_SKIP_FILES = {
    "__main__.py", "server_config.py", "desktop.py", "launcher.py", "qr.py", "messages.py",
    "skills/rrsi.py", "skills/design.py", "skills/protocol.py", "skills/agent_team.py",
    "skills/catalog.py", "skills/interface.py",
}
# Agent-only / CLI-only error messages (regex on the English template).
SERVER_AGENT_ONLY = [
    r"^Unknown (task-)?agent endpoint",
    r"^A valid (read-only task-)?agent token is required",
    r"^A valid read-only task-agent token is required",
    r"^Task-agent (read tokens|sessions)",
    r"^refusing to bind ARK",
    r"^auth (username|password)",
    r"^PBKDF2 ",
    r"^configure only one of auth",
    r"^cannot read auth password file",
    r"^public IP is not a valid address",
]
SERVER_ERROR_CLASSES = None  # any call named *Error / OrgConflict with a message
_BUILTIN_SKIP = {"EOFError", "AssertionError", "TypeError", "KeyError"}
_PLACEHOLDER = re.compile(r"\{([A-Za-z_][A-Za-z0-9_]*)\}")


def _template_regex(template: str) -> re.Pattern:
    parts, names, pos = [], [], 0
    for m in _PLACEHOLDER.finditer(template):
        parts.append(re.escape(template[pos:m.start()]))
        name = m.group(1)
        parts.append(f"(?P={name})" if name in names else f"(?P<{name}>.+?)")
        names.append(name)
        pos = m.end()
    parts.append(re.escape(template[pos:]))
    return re.compile("".join(parts), re.S)


class ServerMatcher:
    """Python twin of i18n.js matchServerText (most literal text first)."""

    def __init__(self, en: dict[str, str]):
        rows = []
        for key, template in en.items():
            if not key.startswith("server."):
                continue
            template = template.replace('\\"', '"')  # parse_dicts keeps \" escapes
            literal = len(_PLACEHOLDER.sub("", template))
            if literal >= 3:
                rows.append((key, literal, _template_regex(template)))
        rows.sort(key=lambda row: -row[1])
        self.rows = rows

    def match(self, text: str, prefix: str = "server.") -> str | None:
        for key, _, pattern in self.rows:
            if key.startswith(prefix) and pattern.fullmatch(text):
                return key
        return None


def _py_template(node):
    """English template of a str / f-string / concatenation; values become {vN}."""
    import ast
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.JoinedStr):
        out = []
        for value in node.values:
            out.append(value.value if isinstance(value, ast.Constant) else "{v}")
        return "".join(out)
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        left, right = _py_template(node.left), _py_template(node.right)
        if left is not None and right is not None:
            return left + right
    return None


def _sample(template: str) -> str:
    count = iter(range(1000))
    return re.sub(r"\{v\}", lambda _m: f"x{next(count)}", template)


def server_error_messages(root: Path = ARK) -> list[dict]:
    """Every literal error message raised by a human-facing server module."""
    import ast
    found = []
    for path in sorted(root.rglob("*.py")):
        name = path.relative_to(root).as_posix()
        if name in SERVER_SKIP_FILES:
            continue
        tree = ast.parse(path.read_text(encoding="utf-8"))
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            func = node.func
            callee = func.id if isinstance(func, ast.Name) else getattr(func, "attr", "")
            if not (callee.endswith("Error") or callee == "OrgConflict") or callee in _BUILTIN_SKIP:
                continue
            message = None
            for arg in node.args:
                if isinstance(arg, ast.Constant) and isinstance(arg.value, str) \
                        and re.fullmatch(r"[A-Za-z_]+", arg.value):
                    continue  # an error code
                message = _py_template(arg)
                if message is not None:
                    break
            if message is None:
                continue
            words = re.findall(r"[A-Za-z]{2,}", message.replace("{v}", " "))
            if len(words) < 2:
                continue
            if any(re.search(rx, message) for rx in SERVER_AGENT_ONLY):
                continue
            found.append({"file": f"ark_studio/python/arkstudio/{name}", "line": node.lineno, "kind": callee,
                          "template": message, "sample": _sample(message)})
    return found


def server_audit(en: dict[str, str], zh: dict[str, str]) -> dict:
    matcher = ServerMatcher(en)
    untranslated = []
    for item in server_error_messages():
        key = matcher.match(item["sample"], "server.error.")
        if not key or key not in zh:
            untranslated.append({**item, "key": key})
    placeholder_mismatch = sorted(
        key for key in en if key.startswith("server.") and key in zh
        and not set(_PLACEHOLDER.findall(zh[key])) <= set(_PLACEHOLDER.findall(en[key])))
    return {
        "server_keys": sum(1 for key in en if key.startswith("server.")),
        "server_untranslated": untranslated,
        "server_placeholder_mismatch": placeholder_mismatch,
    }


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--list", action="store_true", help="print every finding")
    ap.add_argument("--file", help="only list findings whose path contains this text")
    ap.add_argument("--json", action="store_true", help="print the full result as JSON")
    ap.add_argument("--lookup", metavar="TEXT", help="find existing keys whose EN or ZH value contains TEXT")
    ap.add_argument("--extra", action="append", default=[], metavar="JSON",
                    help='also treat keys in {"key": {"en": ..., "zh": ...}} files as defined')
    args = ap.parse_args(argv)
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    if args.lookup:
        dicts = load_dicts()
        needle = args.lookup.lower()
        for key, en in dicts.get("EN", {}).items():
            zh = dicts.get("ZH", {}).get(key, "")
            if needle in en.lower() or needle in zh.lower():
                print(f"{key}	{en}	{zh}")
        return 0
    extra: dict = {}
    for path in args.extra:
        extra.update(json.loads(Path(path).read_text(encoding="utf-8")))
    res = audit(extra)
    if args.json:
        print(json.dumps(res, ensure_ascii=False, indent=1))
        return 0
    if args.list or args.file:
        for f in res["findings"]:
            if args.file and args.file not in f["file"]:
                continue
            print(f'{f["file"]}:{f["line"]}: [{f["kind"]} {f["ctx"]}] {f["text"]!r}')
    print(f'unwrapped literals: {res["total"]} (ui {res["ui"]}, loose {res["loose"]})')
    for name, count in res["per_file"].items():
        print(f"  {count:4d}  {name}")
    print(f'keys: EN {res["en_keys"]}, ZH {res["zh_keys"]}, used in code {res["used_keys"]}')
    for label in ("missing_in_zh", "missing_in_en", "used_missing_en", "used_missing_zh"):
        if res[label]:
            items = [f'{k} ({res["where"].get(k, "")})'.replace(" ()", "") for k in res[label]]
            print(f"  {label}: " + ", ".join(items))
    print(f'server text: {res["server_keys"]} server.* keys, '
          f'untranslated server messages: {len(res["server_untranslated"])}')
    for item in res["server_untranslated"]:
        print(f'  {item["file"]}:{item["line"]}: [{item["kind"]}] {item["template"]!r}')
    if res["server_placeholder_mismatch"]:
        print("  server_placeholder_mismatch: " + ", ".join(res["server_placeholder_mismatch"]))
    failed = (res["total"] or res["server_untranslated"] or res["server_placeholder_mismatch"]
              or any(res[label] for label in ("missing_in_zh", "missing_in_en",
                                              "used_missing_en", "used_missing_zh")))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
