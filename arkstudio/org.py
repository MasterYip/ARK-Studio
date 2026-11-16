"""GitHub-organization-like multi-user support for ARK Studio.

ARK used to run as one server account.  This module adds the persistent
organization model that makes several people share one server without mixing
their credentials:

``runtime/org.sqlite3`` (a database *outside* every project folder) holds
organizations, users (salted PBKDF2 hashes only), memberships/roles, teams,
registered projects, per-user and per-team project grants, personal settings and
an audit trail.

Design rules
------------

* Roles are ``owner`` > ``admin`` > ``member`` > ``viewer``.  Project access is
  an explicit ``read`` / ``write`` / ``admin`` grant.  Effective project access
  is the maximum of the role's implicit access, a direct user grant and a team
  grant.  Owners and admins can administer every project in the organization;
  a viewer may read every project; a member starts with no project access.
* Permission checks live here and are called by the HTTP server on every
  project-scoped API, never only by the UI.
* Personal settings are per user and never shared.  Non-secret overrides live in
  ``user_settings``; credential **values** are sealed (HMAC-SHA256 keystream +
  HMAC tag, stdlib only) in ``user_secrets`` with a machine-local key file that
  is created ``0600`` under the private ARK directory.  A copy of the database
  alone cannot decrypt them and the API only ever returns ``has_key``/hints.
* Project settings may configure the assistant endpoint/model and task-agent
  command lines, but credentials there are environment-variable *names* only,
  so a shared project folder never contains a token.
* Effective settings resolve ``personal override > project > global/env``.
"""
from __future__ import annotations

import base64
import contextlib
import hashlib
import hmac
import json
import os
import re
import secrets
import sqlite3
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlparse

from . import agent_tokens, assistant_config, auth, linux_account
from .workbench_prefs import WorkbenchPrefsError, workbench_prefs
from .settings import (
    REASONING_EFFORTS, SettingsError, personal_appearance, update_personal_appearance,
)

ORG_SCHEMA_VERSION = 3
ROLES = ("owner", "admin", "member", "viewer")
ROLE_RANK = {"owner": 3, "admin": 2, "member": 1, "viewer": 0}
PROJECT_PERMISSIONS = ("read", "write", "admin")
PERMISSION_RANK = {"read": 1, "write": 2, "admin": 3}
DEFAULT_ORG_ID = "org_default"
DEFAULT_ORG_NAME = "ARK Studio"
LOCAL_USERNAME = "local"
PERSONAL_SCOPES = ("assistant", "agents", "environment")
SECRET_PREFIX = "ark1"
USERNAME_RE = auth.USERNAME_RE
MAX_DISPLAY_NAME = 80
MAX_TEAM_NAME = 60
# Self-service profile (``/api/me``).  The username is the stable login ID and
# is never editable here: grants, assignees, activity actors and sessions all
# reference it.
MAX_BIO = 160
MAX_EMAIL = 254
MAX_AVATAR_BYTES = 200 * 1024
MAX_AVATAR_PIXELS = 1024
AVATAR_COLORS = (
    "#4f46e5", "#0ea5e9", "#0d9488", "#16a34a", "#ca8a04",
    "#ea580c", "#dc2626", "#db2777", "#9333ea", "#475569",
)
# Raster formats only.  SVG (script-capable) and anything not matching its
# magic bytes are refused.
AVATAR_TYPES = {"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}
PROFILE_LANGS = ("en", "zh")
PROFILE_THEMES = ("system", "light", "dark")
PROFILE_EDITABLE = ("display_name", "bio", "email", "avatar", "prefs")
EMAIL_RE = re.compile(r"^[^@\s<>\"'(),;:\\]+@[^@\s<>\"'(),;:\\]+\.[^@\s<>\"'(),;:\\]+$")
# Columns added after schema v2.  Applied by an idempotent migration so an
# existing org.sqlite3 keeps every row.
USER_PROFILE_COLUMNS = (
    ("bio", "TEXT NOT NULL DEFAULT ''"),
    ("email", "TEXT NOT NULL DEFAULT ''"),
    ("avatar_kind", "TEXT NOT NULL DEFAULT 'initials'"),
    ("avatar_color", "TEXT NOT NULL DEFAULT ''"),
    ("avatar_path", "TEXT NOT NULL DEFAULT ''"),
    ("prefs_json", "TEXT NOT NULL DEFAULT '{}'"),
    # The project this person last opened; the fallback for a new browser tab
    # or session.  Never a server-wide current project.
    ("last_project_id", "TEXT NOT NULL DEFAULT ''"),
)


class OrgError(Exception):
    """A clean organization/permission error with an HTTP status and code."""

    def __init__(self, status: int, message: str, code: str = "ORG_ERROR"):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code


class OrgConflict(OrgError):
    def __init__(self, message: str, code: str = "ORG_CONFLICT"):
        super().__init__(409, message, code)


def _utcnow() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


def _unb64(text: str) -> bytes:
    return base64.urlsafe_b64decode((text + "=" * (-len(text) % 4)).encode("ascii"))


def validate_role(value: str) -> str:
    role = str(value or "").strip().lower()
    if role not in ROLES:
        raise OrgError(400, f"role must be one of {', '.join(ROLES)}", "INVALID_ROLE")
    return role


def validate_permission(value: str) -> str:
    permission = str(value or "").strip().lower()
    if permission not in PROJECT_PERMISSIONS:
        raise OrgError(400, "permission must be read, write or admin",
                       "INVALID_PERMISSION")
    return permission


def validate_username(value: str) -> str:
    try:
        return auth.validate_username(value)
    except auth.AuthConfigError as exc:
        raise OrgError(400, str(exc), "INVALID_USERNAME") from exc


def _clean_name(value: str, field: str, *, limit: int) -> str:
    text = str(value or "").replace("\x00", "").strip()
    if not text:
        raise OrgError(400, f"{field} is required", "INVALID_NAME")
    if len(text) > limit:
        raise OrgError(400, f"{field} is longer than {limit} characters", "INVALID_NAME")
    return text


# --------------------------------------------------------------------- secrets
class SecretBox:
    """Authenticated at-rest sealing for per-user credential values.

    Stdlib-only: a SHA-256 based keystream (encrypt) plus HMAC-SHA256 over the
    ciphertext (authenticate), keyed by a random machine-local key file created
    ``0600``.  The same associated data (user id + scope) is required to open a
    value, so ciphertext cannot be moved between users or scopes.
    """

    def __init__(self, key_path: str | Path, *, env_key: str = "ARK_ORG_SECRET_KEY"):
        self.key_path = Path(key_path)
        self.env_key = env_key
        self._key: bytes | None = None
        self._lock = threading.RLock()

    def _load_key(self) -> bytes:
        with self._lock:
            if self._key is not None:
                return self._key
            override = str(os.environ.get(self.env_key) or "").strip()
            if override:
                try:
                    raw = bytes.fromhex(override)
                except ValueError:
                    try:
                        raw = _unb64(override)
                    except (ValueError, TypeError) as exc:
                        raise OrgError(
                            500, f"{self.env_key} must be a hex or base64 32-byte key",
                            "SECRET_KEY_INVALID") from exc
                if len(raw) not in (16, 24, 32):
                    raise OrgError(500, f"{self.env_key} must decode to 16-32 bytes",
                                   "SECRET_KEY_INVALID")
                self._key = hashlib.sha256(raw).digest()
                return self._key
            try:
                existing = self.key_path.read_bytes()
            except FileNotFoundError:
                existing = None
            except OSError as exc:
                # Fail closed: an existing-but-unreadable key file means we must
                # not silently generate a replacement (that would make every
                # stored credential unreadable without telling anyone).
                raise OrgError(
                    500,
                    "the local credential key exists but cannot be read; fix its "
                    "permissions or set ARK_ORG_SECRET_KEY explicitly",
                    "SECRET_KEY_UNAVAILABLE") from exc
            if existing is not None:
                if len(existing) >= 16:
                    self._key = hashlib.sha256(existing).digest()
                    return self._key
                raise OrgError(
                    500,
                    "the local credential key is corrupt (too short); restore a "
                    "valid key or set ARK_ORG_SECRET_KEY explicitly",
                    "SECRET_KEY_INVALID")
            try:
                self.key_path.parent.mkdir(parents=True, exist_ok=True)
                raw = secrets.token_bytes(32)
                self.key_path.write_bytes(raw)
                try:
                    os.chmod(self.key_path, 0o600)
                except OSError:
                    pass
                self._key = hashlib.sha256(raw).digest()
                return self._key
            except OSError as exc:
                raise OrgError(
                    500,
                    "ARK cannot create the local secret key; personal credentials "
                    "cannot be stored safely on this host",
                    "SECRET_KEY_UNAVAILABLE") from exc

    @staticmethod
    def _stream(key: bytes, aad: bytes, nonce: bytes, length: int) -> bytes:
        out = bytearray()
        counter = 0
        while len(out) < length:
            out.extend(hashlib.sha256(
                b"ark-org-keystream\x01" + key + aad + nonce
                + counter.to_bytes(8, "big")).digest())
            counter += 1
        return bytes(out[:length])

    def seal(self, plaintext: str, *, aad: str) -> str:
        data = str(plaintext).encode("utf-8")
        nonce = secrets.token_bytes(16)
        key = self._load_key()
        context = aad.encode("utf-8")
        stream = self._stream(key, context, nonce, len(data))
        cipher = bytes(left ^ right for left, right in zip(data, stream))
        tag = hmac.new(key, b"ark-org-tag\x02" + context + nonce + cipher,
                       hashlib.sha256).digest()
        return "$".join((SECRET_PREFIX, _b64(nonce), _b64(cipher), _b64(tag)))

    @staticmethod
    def is_token(value: object) -> bool:
        """True when ``value`` has the shape of a sealed token.

        Shape only (no key, no authentication): used to tell a sealed value
        apart from a credential that was mistakenly stored as plaintext.  A
        value that looks like a token but fails :meth:`open` is still treated
        as corrupt (fail closed), never as plaintext.
        """
        if not isinstance(value, str):
            return False
        parts = value.split("$")
        return len(parts) == 4 and parts[0] == SECRET_PREFIX and all(parts[1:])

    def open(self, token: str, *, aad: str) -> str:
        text = str(token or "")
        try:
            version, nonce_b64, cipher_b64, tag_b64 = text.split("$", 3)
        except ValueError as exc:
            raise OrgError(500, "stored secret is unreadable", "SECRET_CORRUPT") from exc
        if version != SECRET_PREFIX:
            raise OrgError(500, "stored secret has an unknown version", "SECRET_CORRUPT")
        try:
            nonce = _unb64(nonce_b64)
            cipher = _unb64(cipher_b64)
            tag = _unb64(tag_b64)
        except (ValueError, TypeError) as exc:
            raise OrgError(500, "stored secret is unreadable", "SECRET_CORRUPT") from exc
        key = self._load_key()
        context = aad.encode("utf-8")
        expected = hmac.new(key, b"ark-org-tag\x02" + context + nonce + cipher,
                            hashlib.sha256).digest()
        if not hmac.compare_digest(expected, tag):
            raise OrgError(
                500,
                "stored credentials cannot be decrypted with this host's key "
                "(restore ~/.ark/org/secret.key or re-enter them)",
                "SECRET_KEY_MISMATCH")
        stream = self._stream(key, context, nonce, len(cipher))
        return bytes(left ^ right for left, right in zip(cipher, stream)).decode(
            "utf-8", "replace")


# ------------------------------------------------------------------ SQLite store
SCHEMA = """
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS orgs(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users(
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships(
  org_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(org_id, user_id)
);
CREATE TABLE IF NOT EXISTS teams(
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(org_id, name)
);
CREATE TABLE IF NOT EXISTS team_members(
  team_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY(team_id, user_id)
);
CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL,
  name TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  created_by TEXT
);
CREATE TABLE IF NOT EXISTS project_grants(
  project_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  permission TEXT NOT NULL,
  granted_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, user_id)
);
CREATE TABLE IF NOT EXISTS team_grants(
  project_id TEXT NOT NULL,
  team_id TEXT NOT NULL,
  permission TEXT NOT NULL,
  granted_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, team_id)
);
CREATE TABLE IF NOT EXISTS user_settings(
  user_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(user_id, scope)
);
CREATE TABLE IF NOT EXISTS user_secrets(
  user_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  data_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(user_id, scope)
);
CREATE TABLE IF NOT EXISTS linux_accounts(
  user_id TEXT NOT NULL,
  account TEXT NOT NULL,
  assigned_by TEXT,
  created_at TEXT NOT NULL,
  PRIMARY KEY(user_id, account)
);
CREATE TABLE IF NOT EXISTS audit(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  user_id TEXT,
  action TEXT NOT NULL,
  project_id TEXT,
  detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS project_grants_user ON project_grants(user_id);
CREATE INDEX IF NOT EXISTS team_grants_team ON team_grants(team_id);
CREATE INDEX IF NOT EXISTS audit_at ON audit(at);
"""


class OrgStore:
    """Thin SQLite access layer.

    Connections are opened per operation and closed immediately.  That keeps a
    loopback workbench from holding a Windows file lock on ``org.sqlite3``
    (temporary folders, backup tools and tests then behave), and the write lock
    still serialises every mutation.
    """

    def __init__(self, path: str | Path, *, clock=time.time):
        self.path = Path(path).resolve()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.clock = clock
        self.lock = threading.RLock()
        with self._connect() as con:
            con.executescript(SCHEMA)
            self._migrate(con)
            con.execute(
                "INSERT INTO meta(key,value) VALUES('schema_version',?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (str(ORG_SCHEMA_VERSION),))
            con.commit()
        try:
            os.chmod(self.path, 0o600)
        except OSError:
            pass

    @staticmethod
    def _migrate(con) -> None:
        """Add profile columns to ``users`` when missing (safe to rerun)."""
        existing = {str(row[1]) for row in con.execute("PRAGMA table_info(users)")}
        for name, decl in USER_PROFILE_COLUMNS:
            if name not in existing:
                con.execute(f"ALTER TABLE users ADD COLUMN {name} {decl}")
        con.commit()

    @contextlib.contextmanager
    def _connect(self):
        con = sqlite3.connect(str(self.path), timeout=30)
        con.row_factory = sqlite3.Row
        try:
            con.execute("PRAGMA journal_mode=DELETE")
            con.execute("PRAGMA synchronous=NORMAL")
            con.execute("PRAGMA foreign_keys=ON")
            yield con
        finally:
            con.close()

    def close(self) -> None:
        """Compatibility no-op: no connection is held between operations."""

    # ----------------------------------------------------------------- helpers
    def _rows(self, sql: str, args: tuple = ()) -> list[dict]:
        with self.lock, self._connect() as con:
            rows = con.execute(sql, args).fetchall()
        return [dict(row) for row in rows]

    def _row(self, sql: str, args: tuple = ()) -> dict | None:
        rows = self._rows(sql, args)
        return rows[0] if rows else None

    def _write(self, sql: str, args: tuple = ()) -> None:
        with self.lock, self._connect() as con:
            con.execute(sql, args)
            con.commit()

    def _write_many(self, statements: list[tuple[str, tuple]]) -> int:
        """Run several statements in one transaction; return the last rowcount."""
        rowcount = 0
        with self.lock, self._connect() as con:
            for sql, args in statements:
                cursor = con.execute(sql, args)
                rowcount = cursor.rowcount
            con.commit()
        return rowcount

    # --------------------------------------------------------------------- org
    def ensure_org(self, name: str = DEFAULT_ORG_NAME) -> dict:
        row = self._row("SELECT * FROM orgs ORDER BY created_at LIMIT 1")
        if row:
            return row
        created = _utcnow()
        self._write("INSERT INTO orgs(id,name,created_at) VALUES(?,?,?)",
                    (DEFAULT_ORG_ID, str(name or DEFAULT_ORG_NAME), created))
        return {"id": DEFAULT_ORG_ID, "name": str(name or DEFAULT_ORG_NAME),
                "created_at": created}

    def org(self) -> dict | None:
        return self._row("SELECT * FROM orgs ORDER BY created_at LIMIT 1")

    # ------------------------------------------------------------------- users
    def user_by_name(self, username: str) -> dict | None:
        text = str(username or "").strip()
        if not text:
            return None
        return self._row("SELECT * FROM users WHERE username=? COLLATE NOCASE", (text,))

    def set_last_project(self, user_id: str, project_id: str) -> None:
        self._write("UPDATE users SET last_project_id=? WHERE id=?",
                    (str(project_id or ""), str(user_id or "")))

    def last_project(self, user_id: str) -> str:
        row = self._row("SELECT last_project_id FROM users WHERE id=?", (str(user_id or ""),))
        return str((row or {}).get("last_project_id") or "")

    def user_by_id(self, user_id: str) -> dict | None:
        return self._row("SELECT * FROM users WHERE id=?", (str(user_id or ""),))

    def list_users(self) -> list[dict]:
        return self._rows("SELECT * FROM users ORDER BY username COLLATE NOCASE")

    def project_member_rows(self, org_id: str, project_id: str) -> list[dict]:
        """Enabled org members with a user or team grant on the project, in one
        query (``OrgService.project_member`` for every user, without N+1)."""
        return self._rows(
            """SELECT u.username, u.display_name FROM users u
               JOIN memberships m ON m.user_id=u.id AND m.org_id=?
              WHERE u.disabled=0 AND (
                    EXISTS(SELECT 1 FROM project_grants g WHERE g.project_id=? AND g.user_id=u.id)
                 OR EXISTS(SELECT 1 FROM team_grants tg JOIN team_members tm ON tm.team_id=tg.team_id
                           WHERE tg.project_id=? AND tm.user_id=u.id))
              ORDER BY u.username COLLATE NOCASE""",
            (str(org_id), str(project_id), str(project_id)))

    def insert_user(self, username: str, password_hash: str | None,
                    display_name: str = "") -> dict:
        username = validate_username(username)
        if not str(password_hash or "").strip():
            raise OrgError(400, "a password is required", "PASSWORD_REQUIRED")
        now = _utcnow()
        user_id = "usr_" + secrets.token_hex(8)
        try:
            self._write(
                "INSERT INTO users(id,username,display_name,password_hash,disabled,"
                "created_at,updated_at) VALUES(?,?,?,?,0,?,?)",
                (user_id, username, str(display_name or "").strip()[:MAX_DISPLAY_NAME],
                 str(password_hash), now, now))
        except sqlite3.IntegrityError as exc:
            raise OrgConflict(f"user already exists: {username}", "USER_EXISTS") from exc
        return self.user_by_id(user_id) or {}

    def update_user(self, user_id: str, **fields) -> dict:
        allowed = {"display_name", "password_hash", "disabled"}
        updates = {key: value for key, value in fields.items() if key in allowed}
        if not updates:
            return self.user_by_id(user_id) or {}
        sets = ", ".join(f"{key}=?" for key in updates)
        values = list(updates.values()) + [_utcnow(), str(user_id)]
        self._write(f"UPDATE users SET {sets}, updated_at=? WHERE id=?", tuple(values))
        return self.user_by_id(user_id) or {}

    def update_profile(self, user_id: str, **fields) -> dict:
        """Self-service profile columns only (never username/role/disabled)."""
        allowed = {"display_name", "bio", "email", "avatar_kind", "avatar_color",
                   "avatar_path", "prefs_json"}
        updates = {key: value for key, value in fields.items() if key in allowed}
        if not updates:
            return self.user_by_id(user_id) or {}
        sets = ", ".join(f"{key}=?" for key in updates)
        values = list(updates.values()) + [_utcnow(), str(user_id)]
        self._write(f"UPDATE users SET {sets}, updated_at=? WHERE id=?", tuple(values))
        return self.user_by_id(user_id) or {}

    def delete_user(self, user_id: str) -> bool:
        user_id = str(user_id or "")
        rowcount = self._write_many([
            ("DELETE FROM memberships WHERE user_id=?", (user_id,)),
            ("DELETE FROM team_members WHERE user_id=?", (user_id,)),
            ("DELETE FROM project_grants WHERE user_id=?", (user_id,)),
            ("DELETE FROM user_settings WHERE user_id=?", (user_id,)),
            ("DELETE FROM user_secrets WHERE user_id=?", (user_id,)),
            ("DELETE FROM users WHERE id=?", (user_id,)),
        ])
        return rowcount > 0

    # -------------------------------------------------------------- membership
    def membership(self, user_id: str, org_id: str = DEFAULT_ORG_ID) -> dict | None:
        return self._row("SELECT * FROM memberships WHERE org_id=? AND user_id=?",
                         (str(org_id), str(user_id)))

    def set_membership(self, user_id: str, role: str, org_id: str = DEFAULT_ORG_ID) -> dict:
        role = validate_role(role)
        row = self.membership(user_id, org_id)
        if row:
            self._write("UPDATE memberships SET role=? WHERE org_id=? AND user_id=?",
                        (role, str(org_id), str(user_id)))
        else:
            self._write(
                "INSERT INTO memberships(org_id,user_id,role,created_at) VALUES(?,?,?,?)",
                (str(org_id), str(user_id), role, _utcnow()))
        return self.membership(user_id, org_id) or {}

    def members(self, org_id: str = DEFAULT_ORG_ID) -> list[dict]:
        return self._rows(
            "SELECT u.*, m.role AS role FROM memberships m "
            "JOIN users u ON u.id=m.user_id WHERE m.org_id=? "
            "ORDER BY u.username COLLATE NOCASE", (str(org_id),))

    def owners(self, org_id: str = DEFAULT_ORG_ID) -> list[dict]:
        return [row for row in self.members(org_id) if row.get("role") == "owner"]

    # ------------------------------------------------------------------- teams
    def team_by_id(self, team_id: str) -> dict | None:
        return self._row("SELECT * FROM teams WHERE id=?", (str(team_id or ""),))

    def team_by_name(self, name: str, org_id: str = DEFAULT_ORG_ID) -> dict | None:
        return self._row("SELECT * FROM teams WHERE org_id=? AND name=? COLLATE NOCASE",
                         (str(org_id), str(name or "").strip()))

    def list_teams(self, org_id: str = DEFAULT_ORG_ID) -> list[dict]:
        return self._rows("SELECT * FROM teams WHERE org_id=? ORDER BY name COLLATE NOCASE",
                          (str(org_id),))

    def insert_team(self, name: str, description: str = "",
                    org_id: str = DEFAULT_ORG_ID) -> dict:
        team_id = "team_" + secrets.token_hex(6)
        try:
            self._write(
                "INSERT INTO teams(id,org_id,name,description,created_at) VALUES(?,?,?,?,?)",
                (team_id, str(org_id), _clean_name(name, "team name", limit=MAX_TEAM_NAME),
                 str(description or "").strip()[:400], _utcnow()))
        except sqlite3.IntegrityError as exc:
            raise OrgConflict(f"team already exists: {name}", "TEAM_EXISTS") from exc
        return self.team_by_id(team_id) or {}

    def delete_team(self, team_id: str) -> bool:
        return self._write_many([
            ("DELETE FROM team_members WHERE team_id=?", (str(team_id),)),
            ("DELETE FROM team_grants WHERE team_id=?", (str(team_id),)),
            ("DELETE FROM teams WHERE id=?", (str(team_id),)),
        ]) > 0

    def team_members(self, team_id: str) -> list[dict]:
        return self._rows(
            "SELECT u.id, u.username, u.display_name, tm.added_at FROM team_members tm "
            "JOIN users u ON u.id=tm.user_id WHERE tm.team_id=? "
            "ORDER BY u.username COLLATE NOCASE", (str(team_id),))

    def add_team_member(self, team_id: str, user_id: str) -> bool:
        try:
            self._write(
                "INSERT INTO team_members(team_id,user_id,added_at) VALUES(?,?,?)",
                (str(team_id), str(user_id), _utcnow()))
            return True
        except sqlite3.IntegrityError:
            return False

    def remove_team_member(self, team_id: str, user_id: str) -> bool:
        return self._write_many([
            ("DELETE FROM team_members WHERE team_id=? AND user_id=?",
             (str(team_id), str(user_id))),
        ]) > 0

    # ---------------------------------------------------------------- projects
    def project_row(self, project_id: str) -> dict | None:
        return self._row("SELECT * FROM projects WHERE id=?", (str(project_id or ""),))

    def project_by_path(self, path: str) -> dict | None:
        return self._row("SELECT * FROM projects WHERE path=?", (str(path or ""),))

    def list_projects(self, org_id: str = DEFAULT_ORG_ID) -> list[dict]:
        return self._rows("SELECT * FROM projects WHERE org_id=? ORDER BY created_at",
                          (str(org_id),))

    def register_project(self, project_id: str, name: str, path: str,
                         created_by: str = "", updated_at: str = "",
                         org_id: str = DEFAULT_ORG_ID) -> dict:
        project_id = str(project_id or "").strip()
        path = str(path or "").strip()
        if not project_id or not path:
            raise OrgError(400, "project id and path are required", "INVALID_PROJECT")
        existing = self.project_by_path(path)
        now = _utcnow()
        if existing:
            if existing["id"] != project_id:
                # Same folder registered under another id: trust the registry.
                return existing
            if str(name or "") != existing["name"] or (updated_at and
                                                       updated_at != existing["updated_at"]):
                self._write("UPDATE projects SET name=?, updated_at=? WHERE id=?",
                            (str(name or existing["name"]), updated_at or existing["updated_at"],
                             project_id))
            return self.project_row(project_id) or existing
        try:
            self._write(
                "INSERT INTO projects(id,org_id,name,path,created_at,updated_at,created_by)"
                " VALUES(?,?,?,?,?,?,?)",
                (project_id, str(org_id), str(name or project_id), path, now,
                 updated_at or now, str(created_by or "")))
        except sqlite3.IntegrityError as exc:
            raise OrgConflict("project is already registered", "PROJECT_EXISTS") from exc
        return self.project_row(project_id) or {}

    def touch_project(self, project_id: str, name: str = "", updated_at: str = "") -> None:
        row = self.project_row(project_id)
        if not row:
            return
        self._write("UPDATE projects SET name=?, updated_at=? WHERE id=?",
                    (str(name or row["name"]), updated_at or _utcnow(), str(project_id)))

    def unregister_project(self, project_id: str) -> bool:
        return self._write_many([
            ("DELETE FROM project_grants WHERE project_id=?", (str(project_id),)),
            ("DELETE FROM team_grants WHERE project_id=?", (str(project_id),)),
            ("DELETE FROM projects WHERE id=?", (str(project_id),)),
        ]) > 0

    # ------------------------------------------------------------------ grants
    def set_project_grant(self, project_id: str, user_id: str, permission: str,
                          granted_by: str = "") -> dict:
        permission = validate_permission(permission)
        self._write(
            "INSERT INTO project_grants(project_id,user_id,permission,granted_by,created_at)"
            " VALUES(?,?,?,?,?) ON CONFLICT(project_id,user_id) DO UPDATE SET "
            "permission=excluded.permission, granted_by=excluded.granted_by",
            (str(project_id), str(user_id), permission, str(granted_by or ""), _utcnow()))
        return {"project_id": str(project_id), "user_id": str(user_id),
                "permission": permission}

    def remove_project_grant(self, project_id: str, user_id: str) -> bool:
        return self._write_many([
            ("DELETE FROM project_grants WHERE project_id=? AND user_id=?",
             (str(project_id), str(user_id))),
        ]) > 0

    def set_team_grant(self, project_id: str, team_id: str, permission: str,
                       granted_by: str = "") -> dict:
        permission = validate_permission(permission)
        self._write(
            "INSERT INTO team_grants(project_id,team_id,permission,granted_by,created_at)"
            " VALUES(?,?,?,?,?) ON CONFLICT(project_id,team_id) DO UPDATE SET "
            "permission=excluded.permission, granted_by=excluded.granted_by",
            (str(project_id), str(team_id), permission, str(granted_by or ""), _utcnow()))
        return {"project_id": str(project_id), "team_id": str(team_id),
                "permission": permission}

    def remove_team_grant(self, project_id: str, team_id: str) -> bool:
        return self._write_many([
            ("DELETE FROM team_grants WHERE project_id=? AND team_id=?",
             (str(project_id), str(team_id))),
        ]) > 0

    def project_grants(self, project_id: str) -> list[dict]:
        return self._rows(
            "SELECT g.user_id, u.username, u.display_name, g.permission, g.created_at "
            "FROM project_grants g JOIN users u ON u.id=g.user_id "
            "WHERE g.project_id=? ORDER BY u.username COLLATE NOCASE", (str(project_id),))

    def team_grants(self, project_id: str) -> list[dict]:
        return self._rows(
            "SELECT g.team_id, t.name, g.permission, g.created_at FROM team_grants g "
            "JOIN teams t ON t.id=g.team_id WHERE g.project_id=? ORDER BY t.name",
            (str(project_id),))

    def grants_for_user(self, project_id: str, user_id: str) -> list[str]:
        rows = self._rows(
            "SELECT permission FROM project_grants WHERE project_id=? AND user_id=?",
            (str(project_id), str(user_id)))
        rows += self._rows(
            "SELECT g.permission FROM team_grants g JOIN team_members tm "
            "ON tm.team_id=g.team_id WHERE g.project_id=? AND tm.user_id=?",
            (str(project_id), str(user_id)))
        return [row["permission"] for row in rows]

    # ------------------------------------------------------------------- audit
    def log(self, user_id: str, action: str, project_id: str = "",
            detail: dict | None = None) -> None:
        try:
            payload = json.dumps(detail or {}, ensure_ascii=False, default=str)[:4000]
        except (TypeError, ValueError):
            payload = "{}"
        self._write(
            "INSERT INTO audit(at,user_id,action,project_id,detail_json) VALUES(?,?,?,?,?)",
            (_utcnow(), str(user_id or ""), str(action)[:120], str(project_id or ""),
             payload))

    def list_audit(self, limit: int = 100) -> list[dict]:
        rows = self._rows("SELECT * FROM audit ORDER BY id DESC LIMIT ?",
                          (max(1, min(500, int(limit))),))
        out = []
        for row in rows:
            try:
                detail = json.loads(row.get("detail_json") or "{}")
            except ValueError:
                detail = {}
            item = {key: value for key, value in row.items() if key != "detail_json"}
            item["detail"] = detail if isinstance(detail, dict) else {}
            out.append(item)
        return out

    # -------------------------------------------------- personal settings rows
    def settings_row(self, user_id: str, scope: str) -> dict | None:
        return self._row("SELECT * FROM user_settings WHERE user_id=? AND scope=?",
                         (str(user_id), str(scope)))

    def secrets_row(self, user_id: str, scope: str) -> dict | None:
        return self._row("SELECT * FROM user_secrets WHERE user_id=? AND scope=?",
                         (str(user_id), str(scope)))

    def save_settings(self, user_id: str, scope: str, data: dict,
                      secrets_blob: str | None = None) -> None:
        now = _utcnow()
        payload = json.dumps(data or {}, ensure_ascii=False)
        self._write(
            "INSERT INTO user_settings(user_id,scope,data_json,updated_at) VALUES(?,?,?,?) "
            "ON CONFLICT(user_id,scope) DO UPDATE SET data_json=excluded.data_json, "
            "updated_at=excluded.updated_at",
            (str(user_id), str(scope), payload, now))
        if secrets_blob is not None:
            self._write(
                "INSERT INTO user_secrets(user_id,scope,data_json,updated_at) "
                "VALUES(?,?,?,?) ON CONFLICT(user_id,scope) DO UPDATE SET "
                "data_json=excluded.data_json, updated_at=excluded.updated_at",
                (str(user_id), str(scope), str(secrets_blob), now))

    def save_secrets(self, user_id: str, scope: str, secrets_blob: str) -> None:
        """Replace only the sealed-secrets row (used by repair-on-read)."""
        self._write(
            "INSERT INTO user_secrets(user_id,scope,data_json,updated_at) "
            "VALUES(?,?,?,?) ON CONFLICT(user_id,scope) DO UPDATE SET "
            "data_json=excluded.data_json, updated_at=excluded.updated_at",
            (str(user_id), str(scope), str(secrets_blob), _utcnow()))

    # -------------------------------------------------- Linux account mapping
    def linux_accounts(self, user_id: str) -> list[dict]:
        return self._rows(
            "SELECT * FROM linux_accounts WHERE user_id=? ORDER BY account",
            (str(user_id),))

    def linux_accounts_map(self) -> dict[str, list[str]]:
        rows = self._rows(
            "SELECT u.username AS username, l.account AS account "
            "FROM linux_accounts l JOIN users u ON u.id=l.user_id "
            "ORDER BY u.username, l.account")
        out: dict[str, list[str]] = {}
        for row in rows:
            out.setdefault(str(row["username"]), []).append(str(row["account"]))
        return out

    def add_linux_account(self, user_id: str, account: str,
                          assigned_by: str = "") -> dict:
        self._write(
            "INSERT INTO linux_accounts(user_id,account,assigned_by,created_at) "
            "VALUES(?,?,?,?) ON CONFLICT(user_id,account) DO UPDATE SET "
            "assigned_by=excluded.assigned_by",
            (str(user_id), str(account), str(assigned_by or ""), _utcnow()))
        return {"user_id": str(user_id), "account": str(account)}

    def remove_linux_account(self, user_id: str, account: str) -> bool:
        return self._write_many([
            ("DELETE FROM linux_accounts WHERE user_id=? AND account=?",
             (str(user_id), str(account))),
        ]) > 0


# ---------------------------------------------------------------- manager API
class OrgManager:
    """High-level organization operations used by the HTTP server and auth."""

    def __init__(self, path: str | Path, *, clock=time.time,
                 private_dir: str | Path | None = None,
                 secret_key_env: str = "ARK_ORG_SECRET_KEY",
                 allowed_providers: list[str] | None = None):
        self.clock = clock
        self.store = OrgStore(path, clock=clock)
        self.path = self.store.path
        self._private_dir = Path(private_dir) if private_dir is not None else None
        self._secret_key_env = secret_key_env
        self._secret_box: SecretBox | None = None
        self.allowed_providers = list(allowed_providers or [])
        self._lock = threading.RLock()
        self.agent_tokens = agent_tokens.AgentTokenStore(self.store, clock=clock)

    @property
    def org_id(self) -> str:
        org = self.store.org()
        return str(org["id"]) if org else DEFAULT_ORG_ID

    def close(self) -> None:
        self.store.close()

    def _private_path(self) -> Path:
        if self._private_dir is not None:
            return self._private_dir
        return assistant_config.private_dir() / "org"

    def secret_box(self) -> SecretBox:
        with self._lock:
            if self._secret_box is None:
                self._secret_box = SecretBox(self._private_path() / "secret.key",
                                             env_key=self._secret_key_env)
            return self._secret_box

    # ------------------------------------------------------------- bootstrap
    def bootstrap(self, *, owner_username: str = "", owner_password_hash: str = "",
                  owner_display_name: str = "", projects: list[dict] | None = None,
                  sync_password: bool = False) -> dict:
        """Ensure the default org/owner exist and register existing projects.

        Idempotent and safe on an existing installation: the organization
        database is authoritative after the first run, so restarting ARK does
        not silently reset a password that was changed in the UI.  Pass
        ``sync_password=True`` (ARK_AUTH_SYNC=1) to force the CLI/env bootstrap
        password back over the stored hash.
        """
        org = self.store.ensure_org()
        created = False
        owner = None
        username = str(owner_username or "").strip()
        if username:
            username = validate_username(username)
            owner = self.store.user_by_name(username)
            if owner is None:
                if not str(owner_password_hash or "").strip():
                    raise OrgError(
                        500,
                        "the bootstrap owner account has no password hash; ARK "
                        "cannot create a sign-in account",
                        "OWNER_PASSWORD_MISSING")
                owner = self.store.insert_user(
                    username, owner_password_hash,
                    display_name=owner_display_name or username)
                created = True
            elif sync_password and str(owner_password_hash or "").strip():
                owner = self.store.update_user(
                    owner["id"], password_hash=str(owner_password_hash))
            if not self.store.membership(owner["id"], org["id"]):
                self.store.set_membership(owner["id"], "owner", org["id"])
            elif self.store.membership(owner["id"], org["id"])["role"] != "owner":
                self.store.set_membership(owner["id"], "owner", org["id"])
        else:
            # Loopback/no-auth mode: one reserved local owner keeps the
            # single-user experience and still enables personal settings.
            username = LOCAL_USERNAME
            owner = self.store.user_by_name(username)
            if owner is None:
                now = _utcnow()
                owner_id = "usr_local"
                try:
                    self.store._write(
                        "INSERT INTO users(id,username,display_name,password_hash,disabled,"
                        "created_at,updated_at) VALUES(?,?,?,NULL,0,?,?)",
                        (owner_id, username, "Local user", now, now))
                except sqlite3.IntegrityError:
                    pass
                owner = self.store.user_by_name(username)
                created = True
            if owner and not self.store.membership(owner["id"], org["id"]):
                self.store.set_membership(owner["id"], "owner", org["id"])
        registered = []
        for summary in projects or []:
            record = self.register_project(summary, owner_username=username,
                                           grant_owner=True)
            if record:
                registered.append(record["id"])
        return {"org": org, "owner": self.user_public(owner) if owner else None,
                "created": created, "projects_registered": registered}

    def register_project(self, summary: dict, *, owner_username: str = "",
                         grant_owner: bool = False) -> dict | None:
        project_id = str((summary or {}).get("id") or "").strip()
        path = str((summary or {}).get("path") or "").strip()
        if not project_id or not path:
            return None
        created_by = ""
        if owner_username:
            user = self.store.user_by_name(owner_username)
            created_by = str(user["id"]) if user else ""
        record = self.store.register_project(
            project_id, str(summary.get("name") or project_id), path,
            created_by=created_by, updated_at=str(summary.get("updated") or ""),
            org_id=self.org_id)
        if grant_owner and created_by:
            existing = {grant["user_id"] for grant in
                        self.store.project_grants(record["id"])}
            if created_by not in existing:
                self.store.set_project_grant(record["id"], created_by, "admin",
                                             granted_by=created_by)
        return record

    def sync_projects(self, summaries: list[dict], *, owner_username: str = "") -> list[dict]:
        """Register on-disk projects that the org database does not know yet.

        Existing installations have project folders but no registry.  Migration
        grants every organization owner admin on the discovered project; other
        members receive access only through an explicit grant.
        """
        for summary in summaries:
            if not self.store.project_by_path(str(summary.get("path") or "")):
                self.register_project(summary, owner_username=owner_username,
                                      grant_owner=False)
        for summary in summaries:
            record = self.store.project_by_path(str(summary.get("path") or ""))
            if not record:
                continue
            self.store.touch_project(record["id"], str(summary.get("name") or ""),
                                     str(summary.get("updated") or ""))
            for owner in self.store.owners(self.org_id):
                if not any(grant["user_id"] == owner["id"] for grant in
                           self.store.project_grants(record["id"])):
                    self.store.set_project_grant(record["id"], owner["id"], "admin",
                                                 granted_by=owner["id"])
        return self.store.list_projects(self.org_id)

    # ----------------------------------------------------------------- users
    @staticmethod
    def user_public(row: dict | None, role: str | None = None) -> dict:
        row = row or {}
        return {
            "id": row.get("id"),
            "username": row.get("username"),
            "display_name": row.get("display_name") or row.get("username") or "",
            "role": role if role is not None else row.get("role"),
            "disabled": bool(row.get("disabled")),
            "created_at": row.get("created_at"),
            "updated_at": row.get("updated_at"),
            "avatar": OrgManager.avatar_public(row),
            "bio": str(row.get("bio") or ""),
        }

    def account(self, username: str) -> dict | None:
        """Enabled account for auth: ``{id, username, password_hash, disabled}``."""
        row = self.store.user_by_name(username)
        if not row or row.get("disabled"):
            return None
        if not str(row.get("password_hash") or "").strip():
            return None
        return row

    def password_hash_for(self, username: str) -> str | None:
        row = self.account(username)
        return str(row["password_hash"]) if row else None

    def user_id_for(self, username: str) -> str | None:
        row = self.store.user_by_name(username)
        return str(row["id"]) if row else None

    def role_for(self, username: str) -> str | None:
        row = self.store.user_by_name(username)
        if not row:
            return None
        membership = self.store.membership(row["id"], self.org_id)
        return str(membership["role"]) if membership else None

    def is_owner(self, username: str) -> bool:
        return self.role_for(username) == "owner"

    def require_org_admin(self, username: str) -> str:
        role = self.role_for(username)
        if not role:
            raise OrgError(403, "Your account is not a member of this organization.",
                           "ORG_FORBIDDEN")
        if ROLE_RANK.get(role, -1) < ROLE_RANK["admin"]:
            raise OrgError(403, "This action needs an organization admin or owner.",
                           "ORG_FORBIDDEN")
        return role

    def list_members(self) -> list[dict]:
        return [self.user_public(row, role=row.get("role"))
                for row in self.store.members(self.org_id)]

    def create_user(self, actor: str, body: dict) -> dict:
        actor_role = self.require_org_admin(actor)
        username = validate_username(str(body.get("username") or ""))
        try:
            password_hash = auth.hash_password(str(body.get("password") or ""))
        except auth.AuthConfigError as exc:
            raise OrgError(400, str(exc), "PASSWORD_INVALID") from exc
        role = validate_role(body.get("role") or "member")
        if role == "owner" and actor_role != "owner":
            raise OrgError(403, "Only an owner can add another owner",
                           "ORG_OWNER_REQUIRED")
        display = str(body.get("display_name") or "").strip()[:MAX_DISPLAY_NAME]
        user = self.store.insert_user(username, password_hash, display_name=display)
        self.store.set_membership(user["id"], role, self.org_id)
        self.store.log(self.user_id_for(actor), "org.user.create", "",
                       {"user": username, "role": role})
        return self.user_public(self.store.user_by_id(user["id"]), role=role)

    def update_user(self, actor: str, user_id: str, body: dict) -> dict:
        actor_role = self.require_org_admin(actor)
        target = self.store.user_by_id(user_id)
        if not target:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        target_membership = self.store.membership(target["id"], self.org_id)
        target_role = str(target_membership["role"]) if target_membership else None
        if target_role == "owner" and actor_role != "owner":
            raise OrgError(403, "Only an owner can change an owner account",
                           "ORG_OWNER_REQUIRED")
        fields: dict = {}
        if "display_name" in body:
            fields["display_name"] = str(body.get("display_name") or "").strip()[:MAX_DISPLAY_NAME]
        if "disabled" in body:
            if str(target["username"]) == str(actor):
                raise OrgError(409, "you cannot disable your own account", "ORG_SELF")
            fields["disabled"] = 1 if body.get("disabled") else 0
        new_role = None
        if body.get("role") not in (None, ""):
            new_role = validate_role(body["role"])
        if new_role and new_role != target_role:
            if new_role == "owner" and actor_role != "owner":
                raise OrgError(403, "Only an owner can promote another owner",
                               "ORG_OWNER_REQUIRED")
            if target_role == "owner":
                if actor_role != "owner":
                    raise OrgError(403, "Only an owner can change an owner",
                                   "ORG_OWNER_REQUIRED")
                if len(self.store.owners(self.org_id)) <= 1:
                    raise OrgError(409, "the organization needs at least one owner",
                                   "LAST_OWNER")
        if new_role == "owner" and target_role != "owner":
            if len([o for o in self.store.owners(self.org_id)]) >= 50:
                raise OrgError(409, "too many owners", "TOO_MANY_OWNERS")
        if fields:
            self.store.update_user(target["id"], **fields)
            if fields.get("disabled"):
                self.agent_tokens.revoke_user_tokens(target["id"])
        if new_role:
            self.store.set_membership(target["id"], new_role, self.org_id)
        self.store.log(self.user_id_for(actor), "org.user.update", "",
                       {"user": target["username"],
                        "fields": sorted(set(fields) | ({"role"} if new_role else set()))})
        row = self.store.user_by_id(target["id"])
        role = new_role or target_role
        return self.user_public(row, role=role)

    def delete_user(self, actor: str, user_id: str) -> dict:
        actor_role = self.require_org_admin(actor)
        target = self.store.user_by_id(user_id)
        if not target:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        if str(target["username"]) == str(actor):
            raise OrgError(409, "you cannot delete your own account", "ORG_SELF")
        if self.store.membership(target["id"], self.org_id) and \
                str(self.store.membership(target["id"], self.org_id)["role"]) == "owner":
            if actor_role != "owner":
                raise OrgError(403, "Only an owner can remove an owner",
                               "ORG_OWNER_REQUIRED")
            if len(self.store.owners(self.org_id)) <= 1:
                raise OrgError(409, "the organization needs at least one owner",
                               "LAST_OWNER")
        self.store.delete_user(target["id"])
        self.agent_tokens.delete_user_tokens(target["id"])
        self.store.log(self.user_id_for(actor), "org.user.delete", "",
                       {"user": target["username"]})
        return {"deleted": True, "user": self.user_public(target)}

    def change_password(self, actor: str, body: dict) -> dict:
        """Change one's own password (current password required)."""
        user = self.store.user_by_name(actor)
        if not user:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        current = str(body.get("current_password") or "")
        if not auth.verify_password(str(user.get("password_hash") or ""), current):
            raise OrgError(401, "current password is incorrect", "PASSWORD_INVALID")
        try:
            new_hash = auth.hash_password(str(body.get("new_password") or ""))
        except auth.AuthConfigError as exc:
            raise OrgError(400, str(exc), "PASSWORD_INVALID") from exc
        self.store.update_user(user["id"], password_hash=new_hash)
        self.store.log(user["id"], "org.password.change")
        result = {"changed": True}
        if body.get("revoke_agent_tokens"):
            result["agent_tokens_revoked"] = self.revoke_user_tokens(user["username"])
        return result

    def reset_password(self, actor: str, user_id: str, body: dict) -> dict:
        self.require_org_admin(actor)
        target = self.store.user_by_id(user_id)
        if not target:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        if self.role_for(target["username"]) == "owner" and not self.is_owner(actor):
            raise OrgError(403, "Only an owner can reset an owner's password",
                           "ORG_OWNER_REQUIRED")
        try:
            new_hash = auth.hash_password(str(body.get("new_password") or ""))
        except auth.AuthConfigError as exc:
            raise OrgError(400, str(exc), "PASSWORD_INVALID") from exc
        self.store.update_user(target["id"], password_hash=new_hash)
        self.store.log(self.user_id_for(actor), "org.password.reset", "",
                       {"user": target["username"]})
        return {"reset": True}

    # ------------------------------------------------- personal agent tokens
    def revoke_user_tokens(self, username: str) -> int:
        """Revoke every personal agent token of ``username``; return the count.

        Password changes keep tokens by default; a "also revoke my agent
        tokens" choice (or an admin action) calls this.
        """
        user = self.store.user_by_name(username)
        if not user:
            return 0
        count = self.agent_tokens.revoke_user_tokens(user["id"])
        if count:
            self.store.log(user["id"], "personal.agent_token.revoke_all", "",
                           {"count": count})
        return count

    def list_agent_tokens(self, username: str) -> dict:
        user = self._user_or_error(username)
        return {"user": user["username"], "tokens": self.agent_tokens.list_for(user["id"]),
                "limit": agent_tokens.MAX_ACTIVE_TOKENS,
                "scopes": list(agent_tokens.SCOPES),
                "expiry_days": list(agent_tokens.EXPIRY_DAYS)}

    def create_agent_token(self, username: str, body: dict) -> dict:
        user = self._user_or_error(username)
        try:
            secret, item = self.agent_tokens.create(
                user["id"], label=str(body.get("label") or ""),
                scope=body.get("scope") or "write",
                expires_days=body.get("expires_days") or 0)
        except agent_tokens.TokenError as exc:
            raise OrgError(exc.status, exc.message, exc.code) from exc
        self.store.log(user["id"], "personal.agent_token.create", "",
                       {"token": item["id"], "scope": item["scope"]})
        return {"token": secret, "item": item, **self.list_agent_tokens(username)}

    def revoke_agent_token(self, username: str, token_id: str) -> dict:
        user = self._user_or_error(username)
        if not self.agent_tokens.revoke(user["id"], token_id):
            raise OrgError(404, "agent token not found", "AGENT_TOKEN_NOT_FOUND")
        self.store.log(user["id"], "personal.agent_token.revoke", "", {"token": token_id})
        return {"revoked": True, **self.list_agent_tokens(username)}

    # ------------------------------------------------- self-service profile
    @property
    def avatar_dir(self) -> Path:
        """Uploaded avatars live next to org.sqlite3, outside every project."""
        return self.path.parent / ".org" / "avatars"

    @staticmethod
    def default_avatar_color(username: str) -> str:
        digest = hashlib.sha256(str(username or "").lower().encode("utf-8")).digest()
        return AVATAR_COLORS[digest[0] % len(AVATAR_COLORS)]

    @staticmethod
    def _prefs(row: dict | None) -> dict:
        try:
            data = json.loads(str((row or {}).get("prefs_json") or "{}"))
        except ValueError:
            data = {}
        data = data if isinstance(data, dict) else {}
        out = {}
        if data.get("lang") in PROFILE_LANGS:
            out["lang"] = data["lang"]
        if data.get("theme") in PROFILE_THEMES:
            out["theme"] = data["theme"]
        display = data.get("display")
        if isinstance(display, dict):
            scale = display.get("font_scale", 1)
            if isinstance(scale, (int, float)) and not isinstance(scale, bool) and 1 <= scale <= 1.5:
                out["display"] = {"font_scale": scale,
                                  "high_contrast": display.get("high_contrast") is True}
        # Personal overrides of project presentation settings (palette, graph
        # physics); they follow the person across projects.
        appearance = personal_appearance(data.get("appearance"))
        if appearance:
            out["appearance"] = appearance
        # Saved workbench layout presets (see workbench_prefs.py).
        workbench = workbench_prefs(data.get("workbench"))
        if workbench:
            out["workbench"] = workbench
        return out

    @classmethod
    def avatar_public(cls, row: dict | None) -> dict:
        row = row or {}
        username = str(row.get("username") or "")
        color = str(row.get("avatar_color") or "")
        if color not in AVATAR_COLORS:
            color = cls.default_avatar_color(username)
        if str(row.get("avatar_kind") or "") == "image" and row.get("avatar_path"):
            # Versioned URL: a new upload changes it, so caches never go stale.
            version = hashlib.sha256(
                f"{row.get('avatar_path')}|{row.get('updated_at')}".encode("utf-8")
            ).hexdigest()[:10]
            from urllib.parse import quote
            return {"kind": "image", "color": color,
                    "url": f"api/users/{quote(username, safe='')}/avatar?v={version}"}
        return {"kind": "initials", "color": color}

    @classmethod
    def user_card(cls, row: dict | None) -> dict:
        """What other signed-in members of the org may see about an account."""
        row = row or {}
        username = str(row.get("username") or "")
        return {
            "username": username,
            "display_name": str(row.get("display_name") or "") or username,
            "avatar": cls.avatar_public(row),
            "bio": str(row.get("bio") or ""),
            "email": str(row.get("email") or ""),
            "disabled": bool(row.get("disabled")),
        }

    def user_directory(self) -> list[dict]:
        return [self.user_card(row) for row in self.store.list_users()]

    def _profile_user(self, username: str) -> dict:
        user = self.store.user_by_name(username) if username else None
        if not user or user.get("disabled"):
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        return user

    def profile(self, username: str) -> dict:
        user = self._profile_user(username)
        card = self.user_card(user)
        card.pop("disabled", None)
        card["id"] = user.get("id")
        card["prefs"] = self._prefs(user)
        card["updated_at"] = user.get("updated_at")
        return card

    @staticmethod
    def _plain_line(value, field: str, limit: int) -> str:
        text = str(value if value is not None else "")
        # One plain-text line: control characters (newlines included) become
        # spaces; HTML is escaped by every renderer, never interpreted.
        text = re.sub(r"[\x00-\x1f\x7f]+", " ", text).strip()
        text = re.sub(r"\s{2,}", " ", text)
        if len(text) > limit:
            raise OrgError(400, f"{field} is longer than {limit} characters",
                           "PROFILE_INVALID")
        return text

    def update_profile(self, username: str, body: dict) -> dict:
        """Self-service edit of the signed-in account's own profile.

        Only ``display_name``, ``bio``, ``email``, ``avatar`` (initials colour)
        and ``prefs`` are accepted.  Anything else -- username, role, disabled,
        password -- is refused so a crafted request cannot escalate.
        """
        user = self._profile_user(username)
        if not isinstance(body, dict):
            raise OrgError(400, "profile update must be a JSON object", "PROFILE_INVALID")
        unknown = sorted(set(body) - set(PROFILE_EDITABLE))
        if unknown:
            raise OrgError(400, f"these fields cannot be changed here: {', '.join(unknown)}",
                           "PROFILE_FIELD_FORBIDDEN")
        fields: dict = {}
        if "display_name" in body:
            name = self._plain_line(body.get("display_name"), "display name",
                                    MAX_DISPLAY_NAME)
            if not name:
                raise OrgError(400, "display name is required", "PROFILE_INVALID")
            fields["display_name"] = name
        if "bio" in body:
            fields["bio"] = self._plain_line(body.get("bio"), "bio", MAX_BIO)
        if "email" in body:
            email = self._plain_line(body.get("email"), "email", MAX_EMAIL)
            if email and not EMAIL_RE.match(email):
                raise OrgError(400, "email address is not valid", "PROFILE_INVALID")
            fields["email"] = email
        remove_image = False
        if "avatar" in body:
            avatar = body.get("avatar")
            if not isinstance(avatar, dict) or avatar.get("kind") != "initials":
                raise OrgError(400, "choose an initials colour or upload an avatar image",
                               "PROFILE_INVALID")
            color = str(avatar.get("color") or "").lower()
            if color not in AVATAR_COLORS:
                raise OrgError(400, "avatar colour must be one of the palette colours",
                               "PROFILE_INVALID")
            fields.update(avatar_kind="initials", avatar_color=color, avatar_path="")
            remove_image = bool(user.get("avatar_path"))
        if "prefs" in body:
            prefs = body.get("prefs")
            if not isinstance(prefs, dict):
                raise OrgError(400, "prefs must be an object", "PROFILE_INVALID")
            merged = self._prefs(user)
            if "lang" in prefs:
                if prefs["lang"] not in PROFILE_LANGS:
                    raise OrgError(400, "language must be en or zh", "PROFILE_INVALID")
                merged["lang"] = prefs["lang"]
            if "theme" in prefs:
                if prefs["theme"] not in PROFILE_THEMES:
                    raise OrgError(400, "theme must be system, light or dark",
                                   "PROFILE_INVALID")
                merged["theme"] = prefs["theme"]
            if "display" in prefs:
                display = prefs["display"]
                if not isinstance(display, dict) or set(display) - {"font_scale", "high_contrast"}:
                    raise OrgError(400, "invalid display preferences", "PROFILE_INVALID")
                current = merged.get("display", {"font_scale": 1, "high_contrast": False})
                scale = display.get("font_scale", current["font_scale"])
                high = display.get("high_contrast", current["high_contrast"])
                if (isinstance(scale, bool) or not isinstance(scale, (int, float))
                        or not 1 <= scale <= 1.5 or not isinstance(high, bool)):
                    raise OrgError(400, "invalid display preferences", "PROFILE_INVALID")
                merged["display"] = {"font_scale": scale, "high_contrast": high}
            if "appearance" in prefs:
                # {group: value | null}; null clears that override.
                try:
                    appearance = update_personal_appearance(merged.get("appearance"),
                                                            prefs["appearance"])
                except SettingsError as exc:
                    raise OrgError(400, str(exc), "PROFILE_INVALID") from exc
                if appearance:
                    merged["appearance"] = appearance
                else:
                    merged.pop("appearance", None)
            if "workbench" in prefs:
                # Replaces the saved layout presets; null clears them.
                if prefs["workbench"] is None:
                    merged.pop("workbench", None)
                else:
                    try:
                        merged["workbench"] = workbench_prefs(prefs["workbench"], strict=True)
                    except WorkbenchPrefsError as exc:
                        raise OrgError(400, str(exc), "PROFILE_INVALID") from exc
            extra = sorted(set(prefs) - {"lang", "theme", "display", "appearance", "workbench"})
            if extra:
                raise OrgError(400, f"unknown preference: {', '.join(extra)}",
                               "PROFILE_INVALID")
            fields["prefs_json"] = json.dumps(merged, sort_keys=True)
        if fields:
            self.store.update_profile(user["id"], **fields)
            if remove_image:
                self._remove_avatar_files(str(user["id"]))
            self.store.log(user["id"], "org.profile.update", "",
                           {"fields": sorted(k for k in body if k in PROFILE_EDITABLE)})
        return self.profile(str(user["username"]))

    @staticmethod
    def sniff_avatar(data: bytes) -> str:
        """Content type from magic bytes (PNG/JPEG/WebP only), else ``""``."""
        if data.startswith(b"\x89PNG\r\n\x1a\n") and data[12:16] == b"IHDR":
            return "image/png"
        if data.startswith(b"\xff\xd8\xff"):
            return "image/jpeg"
        if len(data) >= 12 and data[:4] == b"RIFF" and data[8:12] == b"WEBP":
            return "image/webp"
        return ""

    def _remove_avatar_files(self, user_id: str) -> None:
        for ext in set(AVATAR_TYPES.values()):
            with contextlib.suppress(OSError):
                (self.avatar_dir / f"{user_id}.{ext}").unlink()

    def set_avatar_image(self, username: str, data: bytes, content_type: str) -> dict:
        user = self._profile_user(username)
        declared = str(content_type or "").split(";")[0].strip().lower()
        if declared not in AVATAR_TYPES:
            raise OrgError(415, "avatar must be a PNG, JPEG or WebP image",
                           "AVATAR_TYPE")
        if not data:
            raise OrgError(400, "avatar image is empty", "AVATAR_INVALID")
        if len(data) > MAX_AVATAR_BYTES:
            raise OrgError(413, f"avatar image is larger than {MAX_AVATAR_BYTES // 1024} KB",
                           "AVATAR_TOO_LARGE")
        sniffed = self.sniff_avatar(data)
        if sniffed != declared:
            raise OrgError(415, "avatar content does not match its image type",
                           "AVATAR_TYPE")
        if sniffed == "image/png":
            width = int.from_bytes(data[16:20], "big")
            height = int.from_bytes(data[20:24], "big")
            if not (0 < width <= MAX_AVATAR_PIXELS and 0 < height <= MAX_AVATAR_PIXELS):
                raise OrgError(400, "avatar image dimensions are too large",
                               "AVATAR_INVALID")
        user_id = str(user["id"])
        if not re.fullmatch(r"[A-Za-z0-9_]+", user_id):
            raise OrgError(500, "unexpected user id", "AVATAR_INVALID")
        name = f"{user_id}.{AVATAR_TYPES[sniffed]}"
        folder = self.avatar_dir
        folder.mkdir(parents=True, exist_ok=True)
        tmp = folder / f".{name}.{secrets.token_hex(4)}.tmp"
        tmp.write_bytes(data)
        with contextlib.suppress(OSError):
            os.chmod(tmp, 0o600)
        self._remove_avatar_files(user_id)
        os.replace(tmp, folder / name)
        self.store.update_profile(user_id, avatar_kind="image", avatar_path=name)
        self.store.log(user_id, "org.profile.avatar", "", {"type": sniffed,
                                                          "bytes": len(data)})
        return self.profile(str(user["username"]))

    def remove_avatar(self, username: str) -> dict:
        user = self._profile_user(username)
        self._remove_avatar_files(str(user["id"]))
        self.store.update_profile(str(user["id"]), avatar_kind="initials", avatar_path="")
        self.store.log(user["id"], "org.profile.avatar_remove")
        return self.profile(str(user["username"]))

    def avatar_file(self, username: str) -> tuple[Path, str] | None:
        """Stored avatar image for an account, or ``None`` (initials)."""
        row = self.store.user_by_name(username)
        if not row or str(row.get("avatar_kind") or "") != "image":
            return None
        name = str(row.get("avatar_path") or "")
        match = re.fullmatch(r"([A-Za-z0-9_]+)\.(png|jpg|webp)", name)
        if not match or match.group(1) != str(row["id"]):
            return None
        path = self.avatar_dir / name
        if not path.is_file():
            return None
        ctype = {"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp"}[match.group(2)]
        return path, ctype

    def set_password_hash(self, username: str, password_hash: str) -> None:
        user = self._profile_user(username)
        self.store.update_user(user["id"], password_hash=str(password_hash))
        self.store.log(user["id"], "org.password.change")

    # ------------------------------------------------- Linux account mapping
    def assign_linux_account(self, actor: str, user_id: str, body: dict) -> dict:
        """Admin-only mapping of an ARK user to an existing Linux account."""
        self.require_org_admin(actor)
        target = self.store.user_by_id(user_id)
        if not target:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        if not self.store.membership(target["id"], self.org_id):
            raise OrgError(400, "the user is not an organization member",
                           "USER_NOT_MEMBER")
        account = str(body.get("account") or "").strip()
        try:
            info = linux_account.validate_linux_account(account)
        except linux_account.LinuxAccountError as exc:
            raise OrgError(exc.status, exc.message, exc.code) from exc
        self.store.add_linux_account(target["id"], info["account"],
                                     assigned_by=self.user_id_for(actor) or "")
        self.store.log(self.user_id_for(actor), "org.linux_account.assign", "",
                       {"user": target["username"], "account": info["account"]})
        return {"user_id": target["id"], "username": target["username"],
                "account": info["account"], "home": info["home"],
                "uid": info["uid"], "capability": linux_account.available()}

    def revoke_linux_account(self, actor: str, user_id: str,
                             account: str) -> dict:
        self.require_org_admin(actor)
        target = self.store.user_by_id(user_id)
        if not target:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        account = str(account or "").strip()
        selected = self.selected_linux_account(target["username"])
        removed = self.store.remove_linux_account(target["id"], account)
        if removed and selected == account:
            # Clear the selection instead of silently running as the server
            # account; the next launch fails closed until a new account is set.
            self.store.save_settings(target["id"], "environment", {})
        self.store.log(self.user_id_for(actor), "org.linux_account.revoke", "",
                       {"user": target["username"], "account": account,
                        "removed": removed})
        return {"removed": removed, "user_id": target["id"],
                "username": target["username"], "account": account}

    def linux_accounts_for(self, username: str) -> list[str]:
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            return []
        return [row["account"] for row in self.store.linux_accounts(user["id"])]

    def selected_linux_account(self, username: str) -> str:
        """The account this user selected, iff it is still assigned."""
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            return ""
        row = self.store.settings_row(user["id"], "environment") or {}
        try:
            data = json.loads(row.get("data_json") or "{}")
        except ValueError:
            data = {}
        selected = str((data or {}).get("linux_account") or "").strip()
        if not selected:
            return ""
        assigned = {item["account"] for item in self.store.linux_accounts(user["id"])}
        return selected if selected in assigned else ""

    def personal_environment_public(self, username: str) -> dict:
        """Personal Linux-account selector state (no passwords, no secrets)."""
        user = self._user_or_error(username)
        assigned = self.linux_accounts_for(username)
        selected = self.selected_linux_account(username)
        return {
            "scope": "environment",
            "user": username,
            "assigned": assigned,
            "selected": selected,
            "capability": linux_account.available(),
            "note": (
                "Only an organization admin can assign or remove a Linux "
                "account. ARK launches task agents through `sudo -n` as the "
                "selected account; there is no fallback to the server account."
            ),
        }

    def save_personal_environment(self, username: str, body: dict) -> dict:
        if not isinstance(body, dict):
            raise OrgError(400, "settings must be an object", "INVALID_SETTINGS")
        user = self._user_or_error(username)
        assigned = self.linux_accounts_for(username)
        if body.get("test"):
            account = str(body.get("linux_account") or
                          self.selected_linux_account(username) or "").strip()
            if account not in assigned:
                raise OrgError(
                    403,
                    "You can only test a Linux account assigned to you by an "
                    "organization admin.",
                    "LINUX_ACCOUNT_NOT_ASSIGNED")
            try:
                info = linux_account.preflight(account)
            except linux_account.LinuxAccountError as exc:
                raise OrgError(exc.status, exc.message, exc.code) from exc
            self.store.log(user["id"], "personal.linux_account.test", "",
                           {"account": account})
            return {"ok": True, "account": account, "home": info["home"],
                    "uid": info["uid"], "sudo": info.get("sudo", "")}
        if "linux_account" in body:
            account = str(body.get("linux_account") or "").strip()
            if account and account not in assigned:
                raise OrgError(
                    403,
                    "This Linux account is not assigned to you. Ask an "
                    "organization admin to assign it first.",
                    "LINUX_ACCOUNT_NOT_ASSIGNED")
            self.store.save_settings(user["id"], "environment",
                                     {"linux_account": account} if account else {})
            self.store.log(user["id"], "personal.linux_account.select", "",
                           {"account": account})
        return self.personal_environment_public(username)

    # -------------------------------------------------------------- project ACL
    def project_permission(self, username: str, project_id: str) -> str | None:
        """Effective project permission for a username, or ``None``."""
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            return None
        if not self.store.membership(user["id"], self.org_id):
            return None
        project = self.store.project_row(project_id)
        if not project:
            return None
        role = self.role_for(username) or "viewer"
        if role in ("owner", "admin"):
            return "admin"
        best = "read" if role == "viewer" else None
        best_rank = PERMISSION_RANK[best] if best else 0
        for permission in self.store.grants_for_user(project_id, user["id"]):
            rank = PERMISSION_RANK.get(permission, 0)
            if rank > best_rank:
                best, best_rank = permission, rank
            if best == "admin":
                break
        return best

    def project_member_names(self, project_id: str) -> list[dict]:
        """``[{username, display_name}]`` of every project member (see below)."""
        return [{"username": row["username"], "display_name": row["display_name"] or row["username"]}
                for row in self.store.project_member_rows(self.org_id, project_id)]

    def project_member(self, username: str, project_id: str) -> bool:
        """True when an enabled org member has joined the project: a direct
        user grant or a grant to one of their teams.  Org-wide roles (owner,
        admin, viewer) alone do not make someone a member; this is the set
        the Assignee picker and "Assign to" offer and the server accepts."""
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            return False
        if not self.store.membership(user["id"], self.org_id):
            return False
        return bool(self.store.grants_for_user(project_id, user["id"]))

    def require_project_permission(self, username: str, project_id: str,
                                   level: str = "read") -> str:
        needed = validate_permission(level)
        permission = self.project_permission(username, project_id)
        if not permission:
            raise OrgError(
                404,
                "project not found or your account is not authorized for it",
                "PROJECT_NOT_ACCESSIBLE")
        if PERMISSION_RANK[permission] < PERMISSION_RANK[needed]:
            raise OrgError(
                403,
                f"this action needs {needed} access to the project "
                f"(your access is {permission})",
                "PROJECT_FORBIDDEN")
        return permission

    def projects_for(self, username: str) -> list[dict]:
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            return []
        if not self.store.membership(user["id"], self.org_id):
            return []
        out = []
        for project in self.store.list_projects(self.org_id):
            permission = self.project_permission(username, project["id"])
            if not permission:
                continue
            item = dict(project)
            item["permission"] = permission
            item["grants"] = self.store.project_grants(project["id"])
            item["team_grants"] = self.store.team_grants(project["id"])
            out.append(item)
        return out

    def project_detail(self, username: str, project_id: str) -> dict:
        permission = self.require_project_permission(username, project_id, "read")
        project = dict(self.store.project_row(project_id) or {})
        project["permission"] = permission
        project["grants"] = self.store.project_grants(project_id)
        project["team_grants"] = self.store.team_grants(project_id)
        project["teams"] = [dict(team, members=self.store.team_members(team["id"]))
                            for team in self.store.list_teams(self.org_id)]
        project["members"] = self.list_members()
        return project

    def grant_project(self, actor: str, project_id: str, body: dict) -> dict:
        self.require_project_permission(actor, project_id, "admin")
        permission = validate_permission(body.get("permission") or "read")
        actor_id = self.user_id_for(actor) or ""
        if body.get("team") or body.get("team_id"):
            team = (self.store.team_by_id(body.get("team") or body.get("team_id"))
                    or self.store.team_by_name(str(body.get("team") or "")))
            if not team:
                raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
            if not self.store.membership(
                    self.user_id_for(actor) or "", self.org_id):
                raise OrgError(403, "not an organization member", "ORG_FORBIDDEN")
            grant = self.store.set_team_grant(project_id, team["id"], permission,
                                              granted_by=actor_id)
            self.store.log(actor_id, "project.grant.team", project_id,
                           {"team": team["name"], "permission": permission})
            grant["team"] = team["name"]
            return grant
        target = body.get("user") or body.get("username") or body.get("user_id") or ""
        user = (self.store.user_by_id(str(target)) if str(target).startswith("usr_")
                else self.store.user_by_name(str(target)))
        if not user:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        if not self.store.membership(user["id"], self.org_id):
            raise OrgError(400, "the user is not an organization member",
                           "USER_NOT_MEMBER")
        grant = self.store.set_project_grant(project_id, user["id"], permission,
                                             granted_by=actor_id)
        self.store.log(actor_id, "project.grant.user", project_id,
                       {"user": user["username"], "permission": permission})
        grant["user"] = user["username"]
        return grant

    def revoke_project(self, actor: str, project_id: str, body: dict) -> dict:
        self.require_project_permission(actor, project_id, "admin")
        actor_id = self.user_id_for(actor) or ""
        if body.get("team") or body.get("team_id"):
            team = (self.store.team_by_id(body.get("team") or body.get("team_id"))
                    or self.store.team_by_name(str(body.get("team") or "")))
            if not team:
                raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
            removed = self.store.remove_team_grant(project_id, team["id"])
            self.store.log(actor_id, "project.revoke.team", project_id,
                           {"team": team["name"]})
            return {"revoked": removed, "team": team["name"]}
        target = body.get("user") or body.get("username") or body.get("user_id") or ""
        user = (self.store.user_by_id(str(target)) if str(target).startswith("usr_")
                else self.store.user_by_name(str(target)))
        if not user:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        removed = self.store.remove_project_grant(project_id, user["id"])
        self.store.log(actor_id, "project.revoke.user", project_id,
                       {"user": user["username"]})
        return {"revoked": removed, "user": user["username"]}

    # ------------------------------------------------------------------ teams
    def list_teams(self) -> list[dict]:
        teams = []
        for team in self.store.list_teams(self.org_id):
            item = dict(team)
            item["members"] = self.store.team_members(team["id"])
            item["grants"] = [
                grant for grant in
                self.store._rows("SELECT * FROM team_grants WHERE team_id=?",
                                 (team["id"],))]
            teams.append(item)
        return teams

    def create_team(self, actor: str, body: dict) -> dict:
        self.require_org_admin(actor)
        team = self.store.insert_team(str(body.get("name") or ""),
                                      str(body.get("description") or ""),
                                      self.org_id)
        self.store.log(self.user_id_for(actor), "org.team.create", "",
                       {"team": team["name"]})
        team["members"] = []
        team["grants"] = []
        return team

    def update_team(self, actor: str, team_id: str, body: dict) -> dict:
        self.require_org_admin(actor)
        team = self.store.team_by_id(team_id)
        if not team:
            raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
        if body.get("description") is not None:
            self.store._write("UPDATE teams SET description=? WHERE id=?",
                              (str(body["description"]).strip()[:400], team["id"]))
        if body.get("name") not in (None, ""):
            name = _clean_name(body["name"], "team name", limit=MAX_TEAM_NAME)
            try:
                self.store._write("UPDATE teams SET name=? WHERE id=?", (name, team["id"]))
            except sqlite3.IntegrityError as exc:
                raise OrgConflict(f"team already exists: {name}", "TEAM_EXISTS") from exc
        self.store.log(self.user_id_for(actor), "org.team.update", "",
                       {"team": team["name"]})
        return self.store.team_by_id(team["id"]) or team

    def delete_team(self, actor: str, team_id: str) -> dict:
        self.require_org_admin(actor)
        team = self.store.team_by_id(team_id)
        if not team:
            raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
        self.store.delete_team(team_id)
        self.store.log(self.user_id_for(actor), "org.team.delete", "",
                       {"team": team["name"]})
        return {"deleted": True, "team": team["name"]}

    def add_team_member(self, actor: str, team_id: str, body: dict) -> dict:
        self.require_org_admin(actor)
        team = self.store.team_by_id(team_id)
        if not team:
            raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
        user = (self.store.user_by_id(str(body.get("user_id") or ""))
                or self.store.user_by_name(str(body.get("user") or body.get("username") or "")))
        if not user:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        if not self.store.membership(user["id"], self.org_id):
            raise OrgError(400, "the user is not an organization member",
                           "USER_NOT_MEMBER")
        added = self.store.add_team_member(team["id"], user["id"])
        self.store.log(self.user_id_for(actor), "org.team.member.add", "",
                       {"team": team["name"], "user": user["username"]})
        return {"added": added, "team": team["name"], "user": user["username"]}

    def remove_team_member(self, actor: str, team_id: str, user_id: str) -> dict:
        self.require_org_admin(actor)
        team = self.store.team_by_id(team_id)
        if not team:
            raise OrgError(404, "team not found", "TEAM_NOT_FOUND")
        user = self.store.user_by_id(user_id) or self.store.user_by_name(user_id)
        if not user:
            raise OrgError(404, "user not found", "USER_NOT_FOUND")
        removed = self.store.remove_team_member(team["id"], user["id"])
        self.store.log(self.user_id_for(actor), "org.team.member.remove", "",
                       {"team": team["name"], "user": user["username"]})
        return {"removed": removed, "team": team["name"], "user": user["username"]}

    # ------------------------------------------------------ personal settings
    def _user_or_error(self, username: str) -> dict:
        user = self.store.user_by_name(username)
        if not user or user.get("disabled"):
            raise OrgError(403, "the account is not available", "ACCOUNT_UNAVAILABLE")
        return user

    @staticmethod
    def _secret_aad(user: dict, scope: str, key: str) -> str:
        return f"{user['id']}:{scope}:{key}"

    def _sealed_tokens(self, user: dict, scope: str) -> dict[str, str]:
        """Stored sealed tokens for one user/scope (never opened).

        Repair-on-read: an earlier bug could write untouched credentials back
        as plaintext.  Any non-empty string that is not shaped like a SecretBox
        token is sealed in place (logged by key name only, never the value)
        so the read succeeds instead of failing with ``SECRET_CORRUPT``.
        """
        row = self.store.secrets_row(user["id"], scope)
        if not row or not str(row.get("data_json") or "").strip():
            return {}
        try:
            stored = json.loads(row["data_json"])
        except ValueError:
            return {}
        if not isinstance(stored, dict):
            return {}
        tokens: dict[str, str] = {}
        repaired: list[str] = []
        for key, value in stored.items():
            key = str(key)
            if not isinstance(value, str) or not value:
                continue
            if SecretBox.is_token(value):
                tokens[key] = value
                continue
            tokens[key] = self.secret_box().seal(
                value, aad=self._secret_aad(user, scope, key))
            repaired.append(key)
        if repaired:
            self.store.save_secrets(user["id"], scope, self._sealed_blob(tokens))
            self.store.log(user["id"], "personal.secrets.reseal",
                           detail={"scope": scope, "keys": sorted(repaired)})
            print(f"ark: re-sealed {len(repaired)} plaintext credential field(s) "
                  f"for user {user['id']} scope {scope}: {', '.join(sorted(repaired))}",
                  file=sys.stderr, flush=True)
        return tokens

    @staticmethod
    def _sealed_blob(tokens: dict[str, str]) -> str:
        """Serialize sealed tokens; refuse to persist anything unsealed."""
        for key, value in tokens.items():
            if not SecretBox.is_token(value):
                raise OrgError(500, "refusing to store an unsealed credential",
                               "SECRET_UNSEALED")
        return json.dumps(tokens, ensure_ascii=False) if tokens else "{}"

    def _secrets(self, user: dict, scope: str) -> dict:
        return {key: self.secret_box().open(token, aad=self._secret_aad(user, scope, key))
                for key, token in self._sealed_tokens(user, scope).items()}

    def personal_settings(self, username: str, scope: str) -> dict:
        """Private settings with secret values (internal use only)."""
        scope = self._validate_scope(scope)
        user = self._user_or_error(username)
        row = self.store.settings_row(user["id"], scope) or {}
        try:
            data = json.loads(row.get("data_json") or "{}")
        except ValueError:
            data = {}
        data = data if isinstance(data, dict) else {}
        data["secrets"] = self._secrets(user, scope)
        data["updated_at"] = row.get("updated_at")
        return data

    def personal_settings_public(self, username: str, scope: str) -> dict:
        """API view: never returns a credential value, only hints."""
        internal = self.personal_settings(username, scope)
        secrets_map = internal.pop("secrets", {}) or {}
        if scope == "assistant":
            values = {key: internal.get(key) for key in
                      ("base_url", "model", "reasoning_effort", "timeout")
                      if internal.get(key) not in (None, "")}
            key = str(secrets_map.get("api_key") or "")
            return {
                "scope": "assistant",
                "user": username,
                "values": values,
                "overrides": sorted(values),
                "has_key": bool(key),
                "key_hint": (f"…{key[-4:]}" if len(key) >= 4 else ("…" if key else "")),
                "updated_at": internal.get("updated_at"),
                "inherits": True,
            }
        providers = internal.get("providers")
        providers = providers if isinstance(providers, dict) else {}
        secrets_map = secrets_map if isinstance(secrets_map, dict) else {}
        # Credential values were sealed under ``env:<provider>:<name>`` keys.
        env_names_by_provider: dict[str, list[str]] = {}
        env_hints: dict[str, dict[str, str]] = {}
        for key, value in secrets_map.items():
            parts = str(key).split(":", 2)
            if len(parts) != 3 or parts[0] != "env":
                continue
            provider_id, name = parts[1], parts[2]
            env_names_by_provider.setdefault(provider_id, []).append(name)
            env_hints.setdefault(provider_id, {})[name] = (
                "…" + str(value)[-4:] if len(str(value)) >= 4 else "…")
        out = {}
        for provider_id in sorted(set(providers) | set(env_names_by_provider)):
            raw = providers.get(provider_id)
            raw = raw if isinstance(raw, dict) else {}
            names = env_names_by_provider.get(provider_id, [])
            out[provider_id] = {
                "command": raw.get("command") or "",
                "enabled": bool(raw.get("enabled", True)),
                "env_names": names,
                "env_hints": env_hints.get(provider_id, {}),
                "overrides": sorted(
                    [key for key in ("command", "enabled") if key in raw]
                    + (["env"] if names else [])),
            }
        return {"scope": "agents", "user": username, "providers": out,
                "updated_at": internal.get("updated_at"), "inherits": True}

    @staticmethod
    def _validate_scope(scope: str) -> str:
        text = str(scope or "").strip().lower()
        if text not in PERSONAL_SCOPES:
            raise OrgError(400, "scope must be assistant or agents", "INVALID_SCOPE")
        return text

    # -- validation / save (assistant)
    @staticmethod
    def _clean_assistant_patch(patch: dict) -> dict:
        if not isinstance(patch, dict):
            raise OrgError(400, "settings must be an object", "INVALID_SETTINGS")
        unknown = set(patch) - {"base_url", "model", "reasoning_effort", "timeout",
                                "api_key", "clear_key"}
        if unknown:
            raise OrgError(400, f"unknown assistant setting: {sorted(unknown)[0]}",
                           "INVALID_SETTINGS")
        clean: dict = {}
        if "base_url" in patch:
            url = str(patch.get("base_url") or "").strip().rstrip("/")
            if url:
                parsed = urlparse(url)
                if parsed.scheme not in {"http", "https"} or not parsed.netloc \
                        or any(ch.isspace() for ch in url) or len(url) > 400:
                    raise OrgError(400, "base_url must be an http(s) URL",
                                   "INVALID_SETTINGS")
            clean["base_url"] = url
        if "model" in patch:
            model = str(patch.get("model") or "").strip()
            if model and (len(model) > 120 or any(ch.isspace() for ch in model)):
                raise OrgError(400, "model must be a short name without spaces",
                               "INVALID_SETTINGS")
            clean["model"] = model
        if "reasoning_effort" in patch:
            effort = str(patch.get("reasoning_effort") or "").strip().lower()
            if effort and effort not in REASONING_EFFORTS:
                raise OrgError(400, "reasoning_effort must be one of "
                               + ", ".join(sorted(REASONING_EFFORTS)),
                               "INVALID_SETTINGS")
            clean["reasoning_effort"] = effort
        if "timeout" in patch:
            if patch.get("timeout") in (None, ""):
                clean["timeout"] = None
            else:
                try:
                    timeout = int(patch["timeout"])
                except (TypeError, ValueError) as exc:
                    raise OrgError(400, "timeout must be a number",
                                   "INVALID_SETTINGS") from exc
                if not 5 <= timeout <= 300:
                    raise OrgError(400, "timeout must be between 5 and 300 seconds",
                                   "INVALID_SETTINGS")
                clean["timeout"] = timeout
        return clean

    def save_personal_assistant(self, username: str, body: dict) -> dict:
        user = self._user_or_error(username)
        current = self.personal_settings(username, "assistant")
        current.pop("secrets", None)
        current.pop("updated_at", None)
        # Untouched credentials keep their existing sealed token verbatim.
        sealed = self._sealed_tokens(user, "assistant")
        clean = self._clean_assistant_patch(body)
        clear_key = bool(body.get("clear_key"))
        api_key = str(body.get("api_key") or "").strip()
        if api_key and len(api_key) > 4096:
            raise OrgError(400, "api_key is too long", "INVALID_SETTINGS")
        data = {key: current.get(key) for key in
                ("base_url", "model", "reasoning_effort", "timeout")
                if current.get(key) not in (None, "")}
        for key, value in clean.items():
            if value in (None, ""):
                data.pop(key, None)
            else:
                data[key] = value
        if clear_key:
            sealed.pop("api_key", None)
        elif api_key:
            sealed["api_key"] = self.secret_box().seal(
                api_key, aad=self._secret_aad(user, "assistant", "api_key"))
        self.store.save_settings(user["id"], "assistant", data, self._sealed_blob(sealed))
        self.store.log(user["id"], "personal.assistant.save",
                       detail={"fields": sorted(clean), "key": "api_key" in sealed})
        return self.personal_settings_public(username, "assistant")

    # -- validation / save (agents)
    def save_personal_agents(self, username: str, body: dict) -> dict:
        user = self._user_or_error(username)
        current = self.personal_settings(username, "agents")
        current.pop("secrets", None)
        current.pop("updated_at", None)
        providers = current.get("providers")
        commands = {"providers": dict(providers) if isinstance(providers, dict) else {}}
        patch = body.get("providers")
        if not isinstance(patch, dict):
            raise OrgError(400, "providers must be an object", "INVALID_SETTINGS")
        if self.allowed_providers:
            unknown = [pid for pid in patch
                       if str(pid).lower() not in self.allowed_providers]
            if unknown:
                raise OrgError(400, f"unknown task-agent provider: {unknown[0]}",
                               "INVALID_SETTINGS")
        # Keep previously sealed credentials (their stored tokens, never the
        # opened plaintext) unless the patch replaces/clears them.
        sealed: dict[str, str] = self._sealed_tokens(user, "agents")
        for raw_id, raw in patch.items():
            pid = str(raw_id or "").strip().lower()
            if not re.fullmatch(r"[a-z][a-z0-9_-]{0,40}", pid):
                raise OrgError(400, f"invalid provider id: {raw_id}", "INVALID_SETTINGS")
            prefix = f"env:{pid}:"
            if raw in (None, {}):
                commands["providers"].pop(pid, None)
                for key in [key for key in sealed if key.startswith(prefix)]:
                    sealed.pop(key, None)
                continue
            if not isinstance(raw, dict):
                raise OrgError(400, f"provider {pid} settings must be an object",
                               "INVALID_SETTINGS")
            item = dict(commands["providers"].get(pid) or {})
            if "command" in raw:
                command = raw.get("command")
                if isinstance(command, list):
                    parts = [str(part) for part in command]
                    if not parts or len(parts) > 24:
                        raise OrgError(400, f"provider {pid} command is invalid",
                                       "INVALID_SETTINGS")
                else:
                    parts = [str(command or "")] if str(command or "").strip() else []
                for part in parts:
                    if not part or len(part) > 400 or any(ch in part for ch in "\r\n\x00"):
                        raise OrgError(400, f"provider {pid} command is invalid",
                                       "INVALID_SETTINGS")
                if not parts:
                    item.pop("command", None)
                else:
                    item["command"] = parts if isinstance(command, list) else parts[0]
            if "enabled" in raw:
                item["enabled"] = bool(raw.get("enabled"))
            env_patch = raw.get("env")
            if isinstance(env_patch, dict):
                for name, value in env_patch.items():
                    env_name = str(name or "").strip()
                    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,63}", env_name):
                        raise OrgError(400, f"invalid environment variable name: {name}",
                                       "INVALID_SETTINGS")
                    text = str(value or "")
                    if len(text) > 4096:
                        raise OrgError(400, "credential value is too long",
                                       "INVALID_SETTINGS")
                    key = f"env:{pid}:{env_name}"
                    if text:
                        sealed[key] = self.secret_box().seal(
                            text, aad=self._secret_aad(user, "agents", key))
                    else:
                        sealed.pop(key, None)
            clear_env = raw.get("clear_env")
            if isinstance(clear_env, list):
                for name in clear_env:
                    sealed.pop(f"env:{pid}:{str(name or '').strip()}", None)
            has_env = any(key.startswith(prefix) for key in sealed)
            if item.get("command") or "enabled" in item or has_env:
                commands["providers"][pid] = item
            else:
                commands["providers"].pop(pid, None)
        self.store.save_settings(user["id"], "agents", commands, self._sealed_blob(sealed))
        self.store.log(user["id"], "personal.agents.save",
                       detail={"providers": sorted(commands["providers"]),
                               "secret_fields": len(sealed)})
        return self.personal_settings_public(username, "agents")

    # --------------------------------------------------- effective resolution
    def resolve_assistant(self, project_summary: dict | None, username: str,
                          *, global_config: dict | None = None) -> dict:
        """Return effective assistant config plus per-field source labels.

        Values are personal override > project > global/env > built-in default.
        """
        project_summary = project_summary or {}
        project_settings = project_summary.get("settings") or {}
        project_assistant = (project_settings.get("assistant")
                             if isinstance(project_settings.get("assistant"), dict) else {})
        global_config = global_config if global_config is not None else assistant_config.load()
        personal = self.personal_settings(username, "assistant")
        personal_secrets = personal.pop("secrets", {}) or {}
        personal.pop("updated_at", None)
        values = {"base_url": assistant_config.DEFAULT_BASE_URL,
                  "model": assistant_config.DEFAULT_MODEL,
                  "reasoning_effort": assistant_config.DEFAULT_REASONING_EFFORT,
                  "timeout": assistant_config.DEFAULT_TIMEOUT}
        sources = {key: "default" for key in values}
        for key in ("base_url", "model", "reasoning_effort", "timeout"):
            if personal.get(key) not in (None, ""):
                values[key] = personal[key]
                sources[key] = "personal"
            elif project_assistant.get(key) not in (None, ""):
                values[key] = project_assistant[key]
                sources[key] = "project"
            elif global_config.get(key) not in (None, ""):
                values[key] = global_config[key]
                sources[key] = "global"
        api_key = ""
        key_source = "none"
        key_name = ""
        if personal_secrets.get("api_key"):
            api_key = str(personal_secrets["api_key"])
            key_source = "personal"
        elif project_assistant.get("api_key_env"):
            key_name = str(project_assistant["api_key_env"])
            candidate = str(os.environ.get(key_name) or "").strip()
            if candidate:
                api_key = candidate
                key_source = "project"
        elif global_config.get("api_key"):
            api_key = str(global_config["api_key"])
            key_source = "global"
        out = {
            "base_url": values["base_url"],
            "model": values["model"],
            "reasoning_effort": values["reasoning_effort"],
            "timeout": values["timeout"],
            "api_key": api_key,
            "sources": dict(sources, api_key=key_source),
            "api_key_env": key_name,
            "project_configured": bool(project_assistant),
            "personal_configured": bool(personal or personal_secrets),
        }
        out["configured"] = bool(api_key)
        out["has_key"] = bool(api_key)
        out["key_hint"] = (f"…{api_key[-4:]}" if len(api_key) >= 4 else ("…" if api_key else ""))
        out["personal_config_required"] = not out["configured"] and not out["personal_configured"]
        return out

    def resolve_agents(self, project_summary: dict | None, username: str,
                       base_presets: list[dict]) -> dict:
        """Effective task-agent presets/env for one user.

        ``presets`` maps provider id to ``{command: [...], env: {...}}`` with the
        credential values resolved for this user only.  ``providers`` is the
        secret-free view used by the API.
        """
        project_settings = (project_summary or {}).get("settings") or {}
        project_agents = (project_settings.get("agents")
                          if isinstance(project_settings.get("agents"), dict) else {})
        project_providers = (project_agents.get("providers")
                             if isinstance(project_agents.get("providers"), dict) else {})
        personal = self.personal_settings(username, "agents")
        personal_secrets = personal.pop("secrets", None) or {}
        personal.pop("updated_at", None)
        personal_providers = (personal.get("providers")
                              if isinstance(personal.get("providers"), dict) else {})
        # Sealed credential values are flat ``env:<provider>:<name>`` entries.
        secret_env: dict[str, dict[str, str]] = {}
        for key, value in (personal_secrets or {}).items():
            parts = str(key).split(":", 2)
            if len(parts) != 3 or parts[0] != "env":
                continue
            secret_env.setdefault(parts[1].lower(), {})[parts[2]] = str(value)
        presets_out: dict[str, dict] = {}
        providers_public: list[dict] = []
        for base in base_presets:
            preset_id = str(base.get("id") or "")
            if not preset_id:
                continue
            base_command = base.get("command") or []
            base_command = base_command if isinstance(base_command, list) else [base_command]
            personal_item = personal_providers.get(preset_id)
            personal_item = personal_item if isinstance(personal_item, dict) else {}
            project_item = project_providers.get(preset_id)
            project_item = project_item if isinstance(project_item, dict) else {}
            command = list(base_command)
            command_source = "global"
            if personal_item.get("command"):
                raw = personal_item["command"]
                command = raw if isinstance(raw, list) else [raw]
                command_source = "personal"
            elif project_item.get("command"):
                raw = project_item["command"]
                command = raw if isinstance(raw, list) else [raw]
                command_source = "project"
            env: dict[str, str] = {}
            env_names: list[str] = []
            for name in project_item.get("env_names") or []:
                text = str(name or "").strip()
                if not text or text in env_names:
                    continue
                env_names.append(text)
                value = str(os.environ.get(text) or "").strip()
                if value:
                    env[text] = value
            for name, value in (secret_env.get(preset_id) or {}).items():
                if name not in env_names:
                    env_names.append(name)
                env[name] = value
            enabled = True
            # An explicit project-level disable is a boundary: new projects
            # start with every provider off and a personal default must not
            # silently turn one back on.  The project admin must enable it.
            if project_item.get("enabled") is False:
                enabled = False
            elif personal_item.get("enabled") is not None:
                enabled = bool(personal_item.get("enabled"))
            elif project_item.get("enabled") is not None:
                enabled = bool(project_item.get("enabled"))
            presets_out[preset_id] = {"command": command, "env": env,
                                      "enabled": enabled,
                                      "command_source": command_source}
            credential_source = "none"
            if secret_env.get(preset_id):
                credential_source = "personal"
            elif env:
                credential_source = "project"
            providers_public.append({
                "id": preset_id,
                "label": base.get("label") or preset_id,
                "command": command,
                "command_source": command_source,
                "env_names": env_names,
                "credential_source": credential_source,
                "overridden": bool(personal_item),
                "project_configured": bool(project_item),
                "enabled": enabled,
            })
        personal_count = len(personal_providers)
        project_count = len([pid for pid, item in project_providers.items() if item])
        return {
            "presets": presets_out,
            "providers": providers_public,
            "personal_configured": personal_count > 0,
            "project_configured": project_count > 0,
        }

    def org_overview(self, username: str) -> dict:
        org = self.store.org() or {}
        role = self.role_for(username)
        user = self.store.user_by_name(username)
        projects = self.projects_for(username)
        admin = bool(role and ROLE_RANK.get(role, -1) >= ROLE_RANK["admin"])
        payload = {
            "org": {"id": org.get("id"), "name": org.get("name"),
                    "created_at": org.get("created_at")},
            "user": self.user_public(user, role=role),
            "role": role,
            "is_admin": admin,
            "is_owner": role == "owner",
            "projects": projects,
            "teams": self.list_teams(),
        }
        if admin:
            payload["members"] = self.list_members()
            payload["all_projects"] = self.store.list_projects(self.org_id)
            payload["audit"] = self.store.list_audit(40)
        return payload

    def list_members(self) -> list[dict]:
        return [self.user_public(row, role=row.get("role"))
                for row in self.store.members(self.org_id)]


def default_org_path(project_root: str | Path) -> Path:
    """``runtime/org.sqlite3`` sits next to ``runtime/projects`` by default."""
    override = str(os.environ.get("ARK_ORG_DB") or "").strip()
    if override:
        return Path(override).expanduser().resolve()
    return (Path(project_root).resolve().parent / "org.sqlite3")
