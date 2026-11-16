"""Account/password sign-in for deliberately public ARK servers.

ARK is loopback-first.  This module only exists for the explicit public mode
(``python -m arkstudio serve --expose ...``) and for reverse-proxy deployments:
without an account the server refuses a non-loopback bind, so a public Host
header alone can never turn the workbench into an open file/terminal endpoint.

Design rules:

* The password is never stored.  It is turned into a salted
  PBKDF2-HMAC-SHA256 hash in memory (``pbkdf2_sha256$iterations$salt$hash``)
  and the plaintext is dropped immediately.  A pre-computed hash can be
  supplied instead, so a deployment never has to put the password in a file.
* The browser receives a signed, HttpOnly, SameSite=Lax cookie.  The cookie
  contains a random session id plus an expiry and an HMAC; the matching session
  record lives in this process only, so logout/restart invalidate it.
* Login attempts are rate-limited per client IP and per username.  Wrong
  credentials return one generic message so the form does not reveal whether a
  username exists.
* The login form carries a signed, expiring CSRF token.  Cross-site form
  submissions are additionally rejected by the server's Origin/Sec-Fetch-Site
  checks and by SameSite=Lax.

This module deliberately has no HTTP code; the gate, login page and cookie
headers live in :mod:`arkstudio.server` and are covered by API tests.
"""
from __future__ import annotations

import base64
import hashlib
import os
import json
import hmac
import ipaddress
import re
import secrets
import time
from collections import deque
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

PASSWORD_ALGORITHM = "pbkdf2_sha256"
PASSWORD_ITERATIONS = 260_000
PASSWORD_MAX_LENGTH = 1024
SESSION_TTL_DEFAULT = 12 * 3600
SESSION_TTL_MIN = 300
SESSION_TTL_MAX = 7 * 24 * 3600
# "Keep me signed in for 7 days" on the login form.
REMEMBER_TTL = 7 * 24 * 3600
LOGIN_LIMIT_DEFAULT = 10
LOGIN_WINDOW_DEFAULT = 300.0
LOGIN_CSRF_TTL = 600
COOKIE_NAME = "ark_session"
USERNAME_RE = re.compile(r"^[A-Za-z0-9_.@+-]{1,64}$")


def _write_private(path: Path, data: bytes) -> None:
    """Atomically write a file readable only by its owner where supported."""
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_bytes(data)
    try:
        os.chmod(tmp, 0o600)
    except OSError:
        pass
    os.replace(tmp, path)


class AuthError(Exception):
    """A clean auth error with an HTTP status and a machine-readable code."""

    def __init__(self, status: int, message: str, code: str = "AUTH_ERROR",
                 retry_after: int | None = None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code
        self.retry_after = retry_after


class AuthConfigError(ValueError):
    """The requested auth configuration is incomplete or inconsistent."""


def _b64encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


def _b64decode(text: str) -> bytes:
    padded = text + "=" * (-len(text) % 4)
    return base64.urlsafe_b64decode(padded.encode("ascii"))


def _sha256(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _iso(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, tz=timezone.utc).isoformat(timespec="seconds")


def validate_username(value: str) -> str:
    username = str(value or "").strip()
    if not username:
        raise AuthConfigError("auth username is required")
    if len(username) > 64 or not USERNAME_RE.fullmatch(username):
        raise AuthConfigError(
            "auth username must be 1-64 characters: letters, digits, . _ @ + -")
    return username


def hash_password(password: str, *, iterations: int = PASSWORD_ITERATIONS,
                  salt: bytes | None = None) -> str:
    """Return a salted PBKDF2 hash string; the plaintext is not retained."""
    text = str(password or "")
    if not text:
        raise AuthConfigError("auth password must not be empty")
    if len(text) > PASSWORD_MAX_LENGTH:
        raise AuthConfigError(f"auth password must be at most {PASSWORD_MAX_LENGTH} characters")
    rounds = int(iterations)
    if not 1_000 <= rounds <= 2_000_000:
        raise AuthConfigError("PBKDF2 iteration count must be between 1000 and 2000000")
    raw_salt = salt if salt is not None else secrets.token_bytes(16)
    digest = hashlib.pbkdf2_hmac("sha256", text.encode("utf-8"), raw_salt, rounds)
    return f"{PASSWORD_ALGORITHM}${rounds}${_b64encode(raw_salt)}${_b64encode(digest)}"


def is_password_hash(value: str) -> bool:
    try:
        algorithm, rounds, salt, digest = str(value or "").split("$", 3)
    except ValueError:
        return False
    if algorithm != PASSWORD_ALGORITHM or not salt or not digest:
        return False
    try:
        iterations = int(rounds)
        if not 1_000 <= iterations <= 2_000_000:
            return False
        return len(_b64decode(salt)) >= 8 and len(_b64decode(digest)) == 32
    except (ValueError, TypeError):
        return False


def verify_password(stored: str, password: str) -> bool:
    """Constant-time check of a plaintext password against a stored hash."""
    text = str(password or "")
    if not text or len(text) > PASSWORD_MAX_LENGTH:
        return False
    try:
        algorithm, rounds, salt, digest = str(stored or "").split("$", 3)
        if algorithm != PASSWORD_ALGORITHM:
            return False
        iterations = int(rounds)
        if not 1_000 <= iterations <= 2_000_000:
            return False
        expected = _b64decode(digest)
        if len(expected) != 32:
            return False
        actual = hashlib.pbkdf2_hmac("sha256", text.encode("utf-8"), _b64decode(salt),
                                     iterations)
    except (ValueError, TypeError):
        return False
    return hmac.compare_digest(actual, expected)


def validate_password_hash(value: str) -> str:
    text = str(value or "").strip()
    if not is_password_hash(text):
        raise AuthConfigError(
            "auth password hash must look like "
            "pbkdf2_sha256$<iterations>$<salt>$<digest>")
    return text


def read_password_file(path: str | Path) -> str:
    target = Path(path).expanduser()
    try:
        text = target.read_text(encoding="utf-8")
    except OSError as exc:
        raise AuthConfigError(f"cannot read auth password file: {exc}") from exc
    # A trailing newline from `echo`/Docker secrets is not part of the password.
    text = text.rstrip("\r\n")
    if not text:
        raise AuthConfigError("auth password file is empty")
    if len(text) > PASSWORD_MAX_LENGTH:
        raise AuthConfigError("auth password file is larger than the password limit")
    return text


@dataclass
class AuthSession:
    sid_hash: str
    username: str
    created_at: float
    expires_at: float
    project_id: str = ""
    remember: bool = False

    def public(self) -> dict:
        return {
            "user": self.username,
            "created_at": _iso(self.created_at),
            "expires_at": _iso(self.expires_at),
            "remember": bool(self.remember),
        }


def normalize_cookie_path(value: str) -> str:
    """Return a safe site path for the session cookie (``/`` or ``/ark/``)."""
    text = str(value or "/").strip() or "/"
    if any(char in text for char in ("\r", "\n", "\t", " ", "\\", ";", ",")):
        return "/"
    if not text.startswith("/") or "//" in text or ".." in text.split("/"):
        return "/"
    if not text.endswith("/"):
        text += "/"
    return text


def parse_cookie(header: str, name: str = COOKIE_NAME) -> str:
    """Extract one cookie value from a Cookie header without a dependency."""
    for part in str(header or "").split(";"):
        key, separator, value = part.strip().partition("=")
        if not separator or key != name:
            continue
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] == '"':
            value = value[1:-1]
        return value
    return ""


def safe_next_path(value: str, default: str = "/") -> str:
    """Allow only a local path, optionally with a query and fragment.

    The login form carries the pre-login location so a QR payload
    (``#pair=...``) and the hash route survive the sign-in redirect.  Anything
    that could become an open redirect (scheme, ``//host``, backslash, control
    characters) falls back to the default.
    """
    text = str(value or "").strip()
    if not text or len(text) > 2000:
        return default
    if any(char in text for char in ("\r", "\n", "\t", "\\", "\x00")):
        return default
    if not text.startswith("/") or text.startswith("//"):
        return default
    lowered = text.lower()
    if lowered.startswith("/%2f") or lowered.startswith("/%5c"):
        return default
    try:
        parsed = urlsplit(text)
    except ValueError:
        return default
    if parsed.scheme or parsed.netloc or not parsed.path.startswith("/"):
        return default
    if ".." in parsed.path.split("/"):
        return default
    return text


class AuthManager:
    """Account, signed sessions and login rate limits.

    ``accounts`` is an optional persistent account backend (an
    ``OrgManager``-shaped object exposing ``account(username)``).  When it is
    attached, the organization database is the source of truth for which users
    exist and which PBKDF2 hash each one has; sessions stay in this process and
    are always bound to exactly one user.  Without a backend the manager keeps
    the original single in-memory account, so existing single-user deployments
    and tests are unchanged.

    ``clock`` is injectable so expiry/rate-limit tests do not sleep.  A
    configured manager is always enabled; ``App(auth=None)`` is how the
    unauthenticated loopback mode is represented.
    """

    def __init__(self, username: str | None = None, *, password: str | None = None,
                 password_hash: str | None = None,
                 session_ttl: int = SESSION_TTL_DEFAULT,
                 login_limit: int = LOGIN_LIMIT_DEFAULT,
                 login_window: float = LOGIN_WINDOW_DEFAULT,
                 iterations: int = PASSWORD_ITERATIONS,
                 cookie_name: str = COOKIE_NAME, cookie_path: str = "/",
                 secure: bool = False, clock=time.time,
                 secret: bytes | None = None,
                 accounts=None):
        self.username = validate_username(username or "")
        if password_hash:
            self.password_hash = validate_password_hash(password_hash)
        elif password:
            self.password_hash = hash_password(password, iterations=iterations)
        else:
            raise AuthConfigError("auth password or password hash is required")
        # Equalise the wrong-username path so the form does not become a
        # timing oracle for account names.
        self._dummy_hash = hash_password(secrets.token_urlsafe(16),
                                         iterations=iterations)
        self._accounts = accounts
        self._local_accounts: dict[str, str] = {self.username: self.password_hash}
        self.session_ttl = max(SESSION_TTL_MIN, min(SESSION_TTL_MAX, int(session_ttl)))
        self.login_limit = max(1, int(login_limit))
        self.login_window = max(1.0, float(login_window))
        self.cookie_name = cookie_name or COOKIE_NAME
        self.cookie_path = normalize_cookie_path(cookie_path)
        self.secure = bool(secure)
        self.clock = clock
        self.secret = secret or secrets.token_bytes(32)
        self._sessions: dict[str, AuthSession] = {}
        self._failures: dict[str, deque[float]] = {}
        # Set by enable_persistence(): remembered sessions survive restarts.
        self._state_path: Path | None = None

    # ------------------------------------------------------------ persistence
    def enable_persistence(self, folder: str | os.PathLike[str]) -> None:
        """Keep "remember me" sessions across server restarts.

        Writes ``<folder>/.auth-secret`` (the cookie signing key, created once,
        owner-only) and ``<folder>/.auth-sessions.json`` (remembered sessions
        as sid hashes; raw session ids are never stored).  Ordinary sessions
        stay in memory and end with the process, as before.
        """
        folder = Path(folder)
        folder.mkdir(parents=True, exist_ok=True)
        secret_path = folder / ".auth-secret"
        try:
            raw = secret_path.read_bytes().strip()
            secret = base64.urlsafe_b64decode(raw + b"=" * (-len(raw) % 4)) if raw else b""
        except (OSError, ValueError):
            secret = b""
        if len(secret) < 32:
            secret = secrets.token_bytes(32)
            _write_private(secret_path, base64.urlsafe_b64encode(secret))
        self.secret = secret
        self._state_path = folder / ".auth-sessions.json"
        self._load_sessions()

    def _load_sessions(self) -> None:
        path = self._state_path
        if not path or not path.is_file():
            return
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
            now = self.clock()
            for item in data.get("sessions") or []:
                session = AuthSession(**{key: item[key] for key in (
                    "sid_hash", "username", "created_at", "expires_at", "project_id", "remember")
                    if key in item})
                if session.remember and session.expires_at > now:
                    self._sessions[session.sid_hash] = session
        except (OSError, ValueError, TypeError, KeyError):
            pass  # a damaged file only signs remembered browsers out

    def _save_sessions(self) -> None:
        path = self._state_path
        if not path:
            return
        now = self.clock()
        items = [asdict(session) for session in self._sessions.values()
                 if session.remember and session.expires_at > now]
        try:
            _write_private(path, json.dumps({"format": "ark-auth-sessions", "version": 1,
                                             "sessions": items}).encode("utf-8"))
        except OSError:
            pass

    @property
    def enabled(self) -> bool:
        return True

    # --------------------------------------------------------------- accounts
    def set_accounts(self, accounts) -> None:
        """Attach the persistent org account backend (idempotent)."""
        self._accounts = accounts

    def account(self, username: str) -> dict | None:
        """Return ``{username, password_hash, disabled}`` for an enabled user.

        Usernames are case-sensitive: ``Ark`` does not sign in as ``ark``.
        The org store keeps them unique case-insensitively, so two accounts
        can never differ only by case (no look-alike impersonation).
        """
        candidate = str(username or "").strip()
        if not candidate:
            return None
        if self._accounts is not None:
            row = self._accounts.account(candidate)
            if not row or str(row.get("username") or "") != candidate:
                return None
            return {"username": str(row.get("username") or candidate),
                    "password_hash": str(row.get("password_hash") or ""),
                    "disabled": bool(row.get("disabled"))}
        stored = self._local_accounts.get(candidate)
        if stored is None:
            return None
        return {"username": candidate, "password_hash": stored, "disabled": False}

    def add_account(self, username: str, password_hash: str) -> None:
        """Register an extra in-memory account (single-user/test compatibility)."""
        user = validate_username(username)
        if any(name.lower() == user.lower() and name != user for name in self._local_accounts):
            raise AuthConfigError(f"an account that differs only by case already exists: {user}")
        self._local_accounts[user] = validate_password_hash(password_hash)

    def usernames(self) -> list[str]:
        if self._accounts is not None:
            rows = self._accounts.store.list_users()
            return [str(row.get("username")) for row in rows if not row.get("disabled")]
        return [self.username]

    # --------------------------------------------------------------- sessions
    def _sign(self, payload: str) -> str:
        digest = hmac.new(self.secret, payload.encode("ascii"), hashlib.sha256).digest()
        return _b64encode(digest)

    def _token_parts(self, token: str) -> tuple[str, int] | None:
        text = str(token or "")
        if not text or len(text) > 500:
            return None
        try:
            payload, signature = text.rsplit(".", 1)
            version, sid, expires = payload.split(".")
            expires_at = int(expires)
        except (ValueError, TypeError):
            return None
        if version != "v1" or not sid or len(sid) > 200:
            return None
        if not hmac.compare_digest(self._sign(payload), signature):
            return None
        return sid, expires_at

    def issue_session(self, username: str | None = None, *,
                      remember: bool = False) -> tuple[str, AuthSession]:
        now = self.clock()
        sid = secrets.token_urlsafe(24)
        ttl = REMEMBER_TTL if remember else self.session_ttl
        expires_at = int(now) + ttl
        payload = f"v1.{sid}.{expires_at}"
        token = f"{payload}.{self._sign(payload)}"
        session = AuthSession(
            sid_hash=_sha256(sid),
            username=str(username or self.username),
            created_at=now,
            expires_at=expires_at,
            remember=bool(remember),
        )
        self._sessions[session.sid_hash] = session
        self._cleanup(now)
        if remember:
            self._save_sessions()
        return token, session

    def authenticate(self, token: str) -> AuthSession | None:
        parts = self._token_parts(token)
        if not parts:
            return None
        sid, expires_at = parts
        session = self._sessions.get(_sha256(sid))
        if not session:
            return None
        now = self.clock()
        if expires_at <= now or session.expires_at <= now:
            self._drop(session.sid_hash)
            return None
        # A session is only usable while its own account still exists and is
        # enabled; deleting/disabling a user invalidates all of their sessions.
        if self.account(session.username) is None:
            self._drop(session.sid_hash)
            return None
        return session

    def set_session_project(self, session: AuthSession, project_id: str) -> None:
        """Remember the project a browser session last opened (survives a
        restart for remembered sessions)."""
        project_id = str(project_id or "")
        if session is None or session.project_id == project_id:
            return
        session.project_id = project_id
        if session.remember and session.sid_hash in self._sessions:
            self._save_sessions()

    def _drop(self, key: str) -> bool:
        session = self._sessions.pop(key, None)
        if session is not None and session.remember:
            self._save_sessions()
        return session is not None

    def revoke(self, token: str) -> bool:
        parts = self._token_parts(token)
        if not parts:
            return False
        return self._drop(_sha256(parts[0]))

    def revoke_user(self, username: str, *, except_sid: str = "") -> int:
        """Invalidate every session belonging to one user (optionally keep one)."""
        target = str(username or "").strip()
        removed = 0
        for key, session in list(self._sessions.items()):
            if key == except_sid:
                continue
            if session.username == target:
                self._sessions.pop(key, None)
                removed += 1
        if removed:
            self._save_sessions()
        return removed

    def _cleanup(self, now: float) -> None:
        stale = [key for key, session in self._sessions.items()
                 if session.expires_at <= now - 3600]
        for key in stale:
            self._sessions.pop(key, None)

    # --------------------------------------------------------------- sign-in
    def _bucket(self, key: str, *, prune: bool) -> deque[float]:
        now = self.clock()
        bucket = self._failures.setdefault(key, deque())
        while bucket and now - bucket[0] >= self.login_window:
            bucket.popleft()
        if prune and not bucket:
            self._failures.pop(key, None)
        return bucket

    def _check_limit(self, key: str) -> None:
        if len(self._failures) > 4096:
            now = self.clock()
            stale = [name for name, bucket in self._failures.items()
                     if not bucket or now - bucket[-1] >= self.login_window]
            for name in stale:
                self._failures.pop(name, None)
        bucket = self._bucket(key, prune=False)
        if len(bucket) >= self.login_limit:
            retry_after = max(1, int(self.login_window - (self.clock() - bucket[0])))
            raise AuthError(
                429,
                "Too many sign-in attempts. Wait a few minutes and try again.",
                "AUTH_RATE_LIMITED",
                retry_after=retry_after,
            )

    def _record_failure(self, key: str) -> None:
        self._bucket(key, prune=False).append(self.clock())

    def login(self, username: str, password: str, *, client_ip: str = "",
              remember: bool = False) -> tuple[str, AuthSession]:
        return self.issue_session(
            self.verify_credentials(username, password, client_ip=client_ip),
            remember=remember)

    def verify_credentials(self, username: str, password: str, *,
                           client_ip: str = "") -> str:
        """Check a username/password and return the canonical username.

        Shares the per-IP/per-user failure limits with the browser login form
        but issues no cookie session.  Native clients use it to mint a
        revocable mobile token instead (``POST /api/mobile/login``).
        """
        ip_key = f"ip:{client_ip or 'unknown'}"
        user_key = f"user:{str(username or '').strip().lower()}"
        self._check_limit(ip_key)
        self._check_limit(user_key)
        candidate = str(username or "").strip()
        account = self.account(candidate)
        username_ok = account is not None
        stored = account["password_hash"] if username_ok else self._dummy_hash
        password_ok = verify_password(stored, password)
        if username_ok and password_ok:
            self._bucket(ip_key, prune=True)
            return str(account["username"])
        self._record_failure(ip_key)
        self._record_failure(user_key)
        raise AuthError(401, "Invalid username or password.", "AUTH_INVALID")

    # ------------------------------------------------------------------ CSRF
    def login_csrf(self, *, ttl: int = LOGIN_CSRF_TTL) -> str:
        payload = f"csrf.{secrets.token_urlsafe(18)}.{int(self.clock()) + int(ttl)}"
        return f"{payload}.{self._sign(payload)}"

    def verify_login_csrf(self, token: str) -> bool:
        text = str(token or "")
        if not text or len(text) > 300:
            return False
        try:
            payload, signature = text.rsplit(".", 1)
            version, _nonce, expires = payload.split(".")
            expires_at = int(expires)
        except (ValueError, TypeError):
            return False
        if version != "csrf" or not hmac.compare_digest(self._sign(payload), signature):
            return False
        return expires_at > self.clock()

    # --------------------------------------------------------------- cookies
    def cookie_header(self, token: str, *, secure: bool | None = None,
                      max_age: int | None = None) -> str:
        # Default: the token's own lifetime (12 h, or 7 days when remembered).
        if max_age is None:
            parts = self._token_parts(token)
            max_age = (parts[1] - int(self.clock())) if parts else self.session_ttl
        ttl = max(0, int(max_age))
        parts = [
            f"{self.cookie_name}={token}",
            f"Path={self.cookie_path}",
            "HttpOnly",
            "SameSite=Lax",
            f"Max-Age={ttl}",
        ]
        if self.secure if secure is None else bool(secure):
            parts.append("Secure")
        return "; ".join(parts)

    def clear_cookie_header(self, *, secure: bool | None = None) -> str:
        parts = [
            f"{self.cookie_name}=",
            f"Path={self.cookie_path}",
            "HttpOnly",
            "SameSite=Lax",
            "Max-Age=0",
            "Expires=Thu, 01 Jan 1970 00:00:00 GMT",
        ]
        if self.secure if secure is None else bool(secure):
            parts.append("Secure")
        return "; ".join(parts)


def build_manager(*, username: str = "", password: str = "",
                  password_hash: str = "", password_file: str = "",
                  session_ttl: int = SESSION_TTL_DEFAULT,
                  login_limit: int = LOGIN_LIMIT_DEFAULT,
                  login_window: float = LOGIN_WINDOW_DEFAULT,
                  cookie_path: str = "/", secure: bool = False,
                  iterations: int = PASSWORD_ITERATIONS,
                  clock=time.time) -> AuthManager | None:
    """Build a manager from CLI/env-style values, or ``None`` when unconfigured.

    Exactly one of ``password_hash``, ``password_file`` or ``password`` may be
    supplied.  A hash is preferred in checked-in/deployment config because the
    plaintext never exists on disk; the env password is preferred over a CLI
    argument because argv is visible in process listings.
    """
    user = str(username or "").strip()
    direct = str(password or "")
    hashed = str(password_hash or "").strip()
    file_path = str(password_file or "").strip()
    sources = [bool(direct), bool(hashed), bool(file_path)]
    if not user and not any(sources):
        return None
    if not user:
        raise AuthConfigError(
            "auth username is required (ARK_AUTH_USER or --auth-user)")
    if sum(1 for item in sources if item) > 1:
        raise AuthConfigError(
            "configure only one of auth password, password file or password hash")
    if hashed:
        return AuthManager(user, password_hash=hashed, session_ttl=session_ttl,
                           login_limit=login_limit, login_window=login_window,
                           cookie_path=cookie_path, secure=secure, clock=clock)
    if file_path:
        direct = read_password_file(file_path)
    if not direct:
        raise AuthConfigError(
            "auth password is required (ARK_AUTH_PASSWORD, ARK_AUTH_PASSWORD_FILE "
            "or ARK_AUTH_PASSWORD_HASH; --auth-password is supported but visible "
            "to other processes)")
    return AuthManager(user, password=direct, session_ttl=session_ttl,
                       login_limit=login_limit, login_window=login_window,
                       iterations=iterations, cookie_path=cookie_path,
                       secure=secure, clock=clock)


def public_ip(value: str) -> str:
    """Validate an explicitly configured public IP (v4 or v6)."""
    text = str(value or "").strip().strip("[]")
    if not text:
        return ""
    try:
        parsed = ipaddress.ip_address(text)
    except ValueError as exc:
        raise AuthConfigError(f"public IP is not a valid address: {text}") from exc
    return str(parsed)


def cookie_secure_from_url(public_url: str) -> bool:
    return str(public_url or "").lower().startswith("https://")


def cookie_path_from_url(public_url: str) -> str:
    text = str(public_url or "").strip()
    if not text:
        return "/"
    try:
        parsed = urlsplit(text)
    except ValueError:
        return "/"
    return normalize_cookie_path(parsed.path or "/")
