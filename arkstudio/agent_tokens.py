"""Personal agent tokens: per-account bearer tokens for the agent API.

Each ARK account (and the local user in loopback mode) may mint its own
tokens in Settings -> Personal -> Agent access.  A token acts *as that user*:
the agent API resolves the account, applies the account's project grants and
caps them by the token scope (``read`` or ``write``).  Writes are attributed
``agent:<username>``.

Storage rules
-------------
* The value ``arka_<random>`` is returned exactly once, at creation.
* ``runtime/org.sqlite3`` keeps only its SHA-256 hash plus metadata (label,
  display prefix, scope, created / last used / expiry / revoked times).  The
  token has 240 bits of entropy, so an unsalted digest is not guessable.
* At most ``MAX_ACTIVE_TOKENS`` live tokens per user.
* Deleting or disabling the user, or an explicit revoke, invalidates them;
  :func:`AgentTokenStore.revoke_user_tokens` is the helper other flows call
  (for example "also revoke my agent tokens" on a password change).

The shared server token (``<root>/.agent-token``) is unrelated and keeps
working as before; it is the legacy, admin-only credential.
"""
from __future__ import annotations

import hashlib
import secrets
import threading
import time
from collections import deque
from datetime import datetime, timedelta, timezone

TOKEN_PREFIX = "arka_"
MAX_ACTIVE_TOKENS = 10
SCOPES = ("read", "write")
EXPIRY_DAYS = (0, 30, 90)
MAX_LABEL = 80
DISPLAY_PREFIX_LEN = len(TOKEN_PREFIX) + 4
LAST_USED_RESOLUTION = 60.0
FAIL_LIMIT_DEFAULT = 20
FAIL_WINDOW_DEFAULT = 300.0

SCHEMA = """
CREATE TABLE IF NOT EXISTS agent_tokens(
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT 'write',
  created_at TEXT NOT NULL,
  expires_at TEXT,
  last_used_at TEXT,
  revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS agent_tokens_user ON agent_tokens(user_id);
"""


class TokenError(Exception):
    def __init__(self, status: int, message: str, code: str):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code


def is_personal_token(value: str) -> bool:
    return str(value or "").startswith(TOKEN_PREFIX)


def token_hash(value: str) -> str:
    return hashlib.sha256(str(value or "").encode("utf-8")).hexdigest()


def _iso(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, timezone.utc).isoformat(timespec="seconds")


def _parse(text: str | None) -> float | None:
    if not text:
        return None
    try:
        return datetime.fromisoformat(str(text)).timestamp()
    except ValueError:
        return None


def clean_scope(value) -> str:
    scope = str(value or "write").strip().lower()
    if scope not in SCOPES:
        raise TokenError(400, "scope must be read or write", "AGENT_TOKEN_SCOPE_INVALID")
    return scope


def clean_expiry(value) -> int:
    try:
        days = int(value or 0)
    except (TypeError, ValueError):
        days = -1
    if days not in EXPIRY_DAYS:
        raise TokenError(400, "expires_days must be 0 (never), 30 or 90",
                         "AGENT_TOKEN_EXPIRY_INVALID")
    return days


class AgentTokenStore:
    """Personal agent tokens in the organization database (``agent_tokens``)."""

    def __init__(self, store, *, clock=time.time):
        self.store = store
        self.clock = clock
        self._last_used: dict[str, float] = {}
        self._lock = threading.Lock()
        # Idempotent migration: older org databases simply gain the table.
        with store.lock, store._connect() as con:
            con.executescript(SCHEMA)
            con.commit()

    def _now(self) -> float:
        return float(self.clock())

    def public(self, row: dict) -> dict:
        expires = _parse(row.get("expires_at"))
        return {
            "id": row.get("id"),
            "label": row.get("label") or "",
            "prefix": row.get("prefix") or "",
            "scope": row.get("scope") or "write",
            "created_at": row.get("created_at"),
            "expires_at": row.get("expires_at"),
            "last_used_at": row.get("last_used_at"),
            "expired": bool(expires is not None and expires <= self._now()),
        }

    def _live_rows(self, user_id: str) -> list[dict]:
        rows = self.store._rows(
            "SELECT * FROM agent_tokens WHERE user_id=? AND revoked_at IS NULL "
            "ORDER BY created_at DESC, id", (str(user_id),))
        return rows

    def list_for(self, user_id: str) -> list[dict]:
        return [self.public(row) for row in self._live_rows(user_id)]

    def active_count(self, user_id: str) -> int:
        return sum(1 for item in self.list_for(user_id) if not item["expired"])

    def create(self, user_id: str, *, label: str = "", scope="write",
               expires_days=0) -> tuple[str, dict]:
        scope = clean_scope(scope)
        days = clean_expiry(expires_days)
        label = " ".join(str(label or "").split())[:MAX_LABEL] or "Agent token"
        with self._lock:
            if self.active_count(user_id) >= MAX_ACTIVE_TOKENS:
                raise TokenError(
                    409,
                    f"You already have {MAX_ACTIVE_TOKENS} active agent tokens. "
                    "Revoke one before creating another.",
                    "AGENT_TOKEN_LIMIT")
            secret = TOKEN_PREFIX + secrets.token_urlsafe(30)
            now = self._now()
            row = {
                "id": "tok_" + secrets.token_hex(8),
                "user_id": str(user_id),
                "label": label,
                "token_hash": token_hash(secret),
                "prefix": secret[:DISPLAY_PREFIX_LEN],
                "scope": scope,
                "created_at": _iso(now),
                "expires_at": _iso(now + days * 86400) if days else None,
                "last_used_at": None,
                "revoked_at": None,
            }
            self.store._write(
                "INSERT INTO agent_tokens(id,user_id,label,token_hash,prefix,scope,"
                "created_at,expires_at,last_used_at,revoked_at) "
                "VALUES(?,?,?,?,?,?,?,?,NULL,NULL)",
                (row["id"], row["user_id"], row["label"], row["token_hash"],
                 row["prefix"], row["scope"], row["created_at"], row["expires_at"]))
        return secret, self.public(row)

    def revoke(self, user_id: str, token_id: str) -> bool:
        """Revoke one token *owned by* ``user_id`` (another user's id is a miss)."""
        count = self.store._write_many([(
            "UPDATE agent_tokens SET revoked_at=? WHERE id=? AND user_id=? "
            "AND revoked_at IS NULL",
            (_iso(self._now()), str(token_id or ""), str(user_id)))])
        return count > 0

    def revoke_user_tokens(self, user_id: str) -> int:
        return self.store._write_many([(
            "UPDATE agent_tokens SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL",
            (_iso(self._now()), str(user_id)))])

    def delete_user_tokens(self, user_id: str) -> int:
        return self.store._write_many([(
            "DELETE FROM agent_tokens WHERE user_id=?", (str(user_id),))])

    def authenticate(self, secret: str) -> dict | None:
        """``{token, user}`` rows for a live token of an enabled user, else ``None``."""
        if not is_personal_token(secret) or len(secret) > 200:
            return None
        row = self.store._row("SELECT * FROM agent_tokens WHERE token_hash=?",
                              (token_hash(secret),))
        if not row or row.get("revoked_at"):
            return None
        now = self._now()
        expires = _parse(row.get("expires_at"))
        if expires is not None and expires <= now:
            return None
        user = self.store.user_by_id(row["user_id"])
        if not user or user.get("disabled"):
            return None
        with self._lock:
            last = self._last_used.get(row["id"], 0.0)
            touch = now - last >= LAST_USED_RESOLUTION
            if touch:
                self._last_used[row["id"]] = now
        if touch:
            row["last_used_at"] = _iso(now)
            self.store._write("UPDATE agent_tokens SET last_used_at=? WHERE id=?",
                              (row["last_used_at"], row["id"]))
        return {"token": row, "user": user}


class FailureLimiter:
    """Sliding-window limit on failed bearer-token attempts per client."""

    def __init__(self, limit: int = FAIL_LIMIT_DEFAULT,
                 window: float = FAIL_WINDOW_DEFAULT, *, clock=time.time):
        self.limit = int(limit)
        self.window = float(window)
        self.clock = clock
        self._buckets: dict[str, deque] = {}
        self._lock = threading.Lock()

    def _bucket(self, key: str, now: float) -> deque:
        bucket = self._buckets.setdefault(key, deque())
        while bucket and now - bucket[0] > self.window:
            bucket.popleft()
        return bucket

    def check(self, key: str) -> None:
        now = float(self.clock())
        with self._lock:
            if len(self._bucket(key, now)) >= self.limit:
                raise TokenError(
                    429, "Too many failed agent token attempts. Wait a few minutes "
                    "and try again.", "AGENT_TOKEN_RATE_LIMITED")

    def record(self, key: str) -> None:
        now = float(self.clock())
        with self._lock:
            self._bucket(key, now).append(now)
            if len(self._buckets) > 4096:
                for stale in [k for k, b in self._buckets.items() if not b]:
                    self._buckets.pop(stale, None)
