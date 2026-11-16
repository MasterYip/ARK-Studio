"""Small shared secret redactor for text that ARK stores on behalf of agents.

Used for uploaded session transcripts and exported internal session logs.  It
recognises well-known credential *shapes* (the same families a public-release
secret scanner blocks) plus caller-supplied
literal values such as the server's own agent token.  Every match becomes
``[REDACTED:<kind>]``.  The redactor never raises on odd input.
"""
from __future__ import annotations

import re
from typing import Any, Iterable

MARK = "[REDACTED:{kind}]"

_PATTERNS: tuple[tuple[str, re.Pattern], ...] = (
    ("private_key", re.compile(
        r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----"
        r"[\s\S]*?(?:-----END (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----|$)")),
    ("bearer", re.compile(r"(?i)(?<=\bbearer )[A-Za-z0-9._~+/=-]{12,}")),
    ("ark_token", re.compile(r"\barka_[A-Za-z0-9_-]{16,}")),
    ("anthropic_key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{12,}")),
    ("openai_key", re.compile(r"\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}")),
    ("github_token", re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{30,})")),
    ("aws_key", re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b")),
    ("slack_token", re.compile(r"\bxox[abposr]-[A-Za-z0-9-]{10,}")),
    ("google_key", re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b")),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}")),
)

# ``password = "..."`` / ``"api_key": "..."`` / ``TOKEN=...``: only the value is replaced.
_ASSIGNMENT = re.compile(
    r"""(?ix)
    (?P<key>\b[\w.-]*(?:password|passwd|api[_-]?key|secret|access[_-]?token|refresh[_-]?token|auth[_-]?token)[\w-]*["']?)
    (?P<sep>\s*[:=]\s*)
    (?P<quote>["']?)
    (?P<value>[^\s"'`,;}{]{8,})
    """)
_PLACEHOLDER = re.compile(r"^(?:\[REDACTED|<|\$\{?|env:|\*{3,}|x{6,}|\.\.\.)", re.I)


def redact_text(text: str, extra: Iterable[str] = ()) -> tuple[str, int]:
    """Return ``(redacted, count)`` for one string."""
    if not isinstance(text, str) or not text:
        return text, 0
    count = 0
    for literal in extra:
        literal = str(literal or "")
        if len(literal) >= 8 and literal in text:
            count += text.count(literal)
            text = text.replace(literal, MARK.format(kind="server_token"))
    for kind, pattern in _PATTERNS:
        text, n = pattern.subn(MARK.format(kind=kind), text)
        count += n

    def _assign(match: re.Match) -> str:
        nonlocal count
        value = match.group("value")
        # Code and references (``getenv("X")``, ``process.env.KEY``, ``$VAR``)
        # are not values; real secrets mix letters and digits.
        if (_PLACEHOLDER.match(value) or "(" in value or value.startswith(("process.", "os."))
                or not (re.search(r"\d", value) and re.search(r"[A-Za-z]", value))):
            return match.group(0)
        count += 1
        return (match.group("key") + match.group("sep") + match.group("quote")
                + MARK.format(kind="credential"))

    text = _ASSIGNMENT.sub(_assign, text)
    return text, count


def redact_value(value: Any, extra: Iterable[str] = ()) -> tuple[Any, int]:
    """Redact every string inside a JSON-like value (keys stay untouched)."""
    extra = tuple(extra)
    if isinstance(value, str):
        return redact_text(value, extra)
    if isinstance(value, list):
        total = 0
        out = []
        for item in value:
            item, n = redact_value(item, extra)
            out.append(item)
            total += n
        return out, total
    if isinstance(value, dict):
        total = 0
        out_dict = {}
        for key, item in value.items():
            item, n = redact_value(item, extra)
            out_dict[key] = item
            total += n
        return out_dict, total
    return value, 0
