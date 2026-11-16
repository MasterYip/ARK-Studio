"""Persistent server settings and read-only discovery of existing account auth."""
from __future__ import annotations

from contextlib import closing
import json
import os
from pathlib import Path
import sqlite3

from . import auth

PATH_FIELDS = {"root", "sessions_root", "auth_password_file"}
ENV_PATHS = {"org_db": "ARK_ORG_DB", "private_dir": "ARK_PRIVATE_DIR",
             "gpu_servers_config": "ARK_GPU_SERVERS_CONFIG", "gpu_queue_db": "ARK_GPU_QUEUE_DB",
             "blackbox_dir": "ARK_BLACKBOX_DIR"}
STRING_FIELDS = PATH_FIELDS | {"host", "public_url", "auth_user", "auth_password_hash", "public_ip"}
BOOL_FIELDS = {"no_demo", "expose", "detect_public_ip", "trust_proxy", "require_auth"}
INT_FIELDS = {"port", "auth_session_ttl"}


def initialize(path: str | Path, data_dir: str | Path, username: str, password: str,
               *, port: int = 8890, public_url: str = "") -> dict:
    from .org import OrgManager
    target = Path(path).expanduser().resolve()
    data = Path(data_dir).expanduser().resolve()
    if target.exists() or (data.exists() and any(data.iterdir())):
        raise auth.AuthConfigError("Configuration or nonempty data directory already exists; initialization never overwrites an installation")
    username = auth.validate_username(username)
    if len(password) < 12:
        raise auth.AuthConfigError("Initial owner password must contain at least 12 characters")
    if not 1 <= port <= 65535:
        raise auth.AuthConfigError("Server port must be between 1 and 65535")
    if public_url:
        from .mobile import normalize_public_url
        public_url = normalize_public_url(public_url)
    values = {"root": str(data / "projects"), "org_db": str(data / "org.sqlite3"),
              "private_dir": str(data / "private"), "sessions_root": str(data / "agent-sessions"),
              "gpu_servers_config": str(data / "private/gpu_servers.yaml"),
              "gpu_queue_db": str(data / "gpu-queue.sqlite3"), "blackbox_dir": str(data / "blackbox"),
              "auth_user": username, "require_auth": True, "host": "127.0.0.1", "port": port,
              "public_url": public_url, "no_demo": True}
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    data.mkdir(mode=0o700, parents=True, exist_ok=True)
    data.chmod(0o700)
    manager = OrgManager(data / "org.sqlite3")
    try:
        manager.bootstrap(owner_username=username, owner_password_hash=auth.hash_password(password))
    finally:
        manager.close()
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(values, stream, indent=2)
        stream.write("\n")
    return {"config": str(target), "data": str(data), "owner": username, "port": port}


def load(path: str | Path) -> tuple[dict, dict]:
    source = Path(path).expanduser().resolve()
    try:
        values = json.loads(source.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError) as exc:
        raise auth.AuthConfigError("Cannot read server JSON configuration") from exc
    if not isinstance(values, dict):
        raise auth.AuthConfigError("Server configuration must be a JSON object")
    unknown = values.keys() - (STRING_FIELDS | BOOL_FIELDS | INT_FIELDS | ENV_PATHS.keys() | {"media_root"})
    if unknown:
        raise auth.AuthConfigError("Unknown server setting: " + sorted(unknown)[0])
    cli, env = {"require_auth": True}, {}

    def resolved(value):
        candidate = Path(value).expanduser()
        return str((source.parent / candidate).resolve() if not candidate.is_absolute() else candidate.resolve())

    for key, value in values.items():
        if key in STRING_FIELDS | ENV_PATHS.keys():
            if not isinstance(value, str) or "\x00" in value:
                raise auth.AuthConfigError("Server setting must be a string: " + key)
            if key in PATH_FIELDS | ENV_PATHS.keys():
                if not value.strip():
                    raise auth.AuthConfigError("Server path must not be empty: " + key)
                value = resolved(value)
        elif key in BOOL_FIELDS and not isinstance(value, bool):
            raise auth.AuthConfigError("Server setting must be a boolean: " + key)
        elif key in INT_FIELDS and (not isinstance(value, int) or isinstance(value, bool) or value < 0):
            raise auth.AuthConfigError("Server setting must be a nonnegative integer: " + key)
        elif key == "media_root":
            if not isinstance(value, list) or any(not isinstance(item, str) or not item.strip() for item in value):
                raise auth.AuthConfigError("media_root must be a list of paths")
            value = [resolved(item) for item in value]
        if key == "port" and value > 65535:
            raise auth.AuthConfigError("Server port must be at most 65535")
        if key in ENV_PATHS:
            env[ENV_PATHS[key]] = value
        else:
            cli[key] = value
    return cli, env


def persisted_auth(database: str | Path, username: str = "") -> auth.AuthManager | None:
    """A protected organization cannot silently become an anonymous local server."""
    database = Path(database)
    if not database.exists():
        return None
    try:
        with closing(sqlite3.connect(database.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            tables = {row[0] for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            if "users" not in tables:
                return None
            protected = conn.execute("SELECT 1 FROM users WHERE password_hash IS NOT NULL AND password_hash<>'' LIMIT 1").fetchone()
            if not protected:
                return None
            rows = conn.execute(
                "SELECT u.username,u.password_hash FROM users u JOIN memberships m ON m.user_id=u.id "
                "WHERE m.role='owner' AND u.disabled=0 AND u.password_hash IS NOT NULL "
                "AND u.password_hash<>'' ORDER BY u.created_at,u.username").fetchall()
    except sqlite3.Error as exc:
        raise auth.AuthConfigError("Cannot read existing organization authentication; refusing anonymous startup") from exc
    chosen = next((row for row in rows if not username or row[0].lower() == username.lower()), None)
    if chosen is None:
        raise auth.AuthConfigError("Organization has accounts but no matching active owner; configure authentication explicitly")
    return auth.AuthManager(chosen[0], password_hash=chosen[1])
