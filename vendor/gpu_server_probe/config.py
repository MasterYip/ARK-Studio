"""Safe, dependency-free parsing for ``servers_rp.yaml``.

The upstream GPU tool keeps a private ``servers_rp.yaml`` with cluster hosts.
ARK must never need passwords or private keys, so this module accepts a small
YAML subset and a strict schema:

    enabled: false              # opt-in; probe/dispatch stay off until true
    defaults:
      port: 22
      user: ubuntu
      timeout_seconds: 8
    servers:
      - name: gpu-server-1
        host: gpu-server-1.example.com
        port: 22
        user: ubuntu
        tags: [4090, primary]
        note: "H20 node behind ~/.ssh/config"

Supported YAML: block mappings/lists, quoted or plain scalars, booleans,
integers and inline lists. Anchors, aliases, tags, block scalars, flow maps and
tabs are rejected instead of guessed at. Credential-looking fields are rejected
with an actionable error: authentication comes from the local SSH agent or
``~/.ssh/config``.
"""
from __future__ import annotations

import re
from pathlib import Path

MAX_CONFIG_BYTES = 256 * 1024
MAX_SERVERS = 64

TOP_FIELDS = {"enabled", "defaults", "servers", "queue"}
DEFAULT_FIELDS = {"port", "user", "timeout_seconds"}
QUEUE_FIELDS = {
    "enabled", "state_dir", "poll_seconds", "default_timeout_minutes",
    "max_jobs_per_gpu", "max_utilization_pct", "min_free_memory_mb",
    "allow_colocation", "setup", "path_map", "env",
}
PATH_MAP_FIELDS = {"from", "to", "servers"}
SERVER_FIELDS = {"name", "host", "port", "user", "tags", "note", "enabled", "timeout_seconds"}

NAME_RE = re.compile(r"^[A-Za-z0-9._-]{1,60}$")
HOST_RE = re.compile(r"^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$")
HOST_V6_RE = re.compile(r"^[0-9A-Fa-f:]{2,45}$")
USER_RE = re.compile(r"^[A-Za-z0-9._-]{1,32}$")
TAG_RE = re.compile(r"^[A-Za-z0-9._:-]{1,40}$")
KEY_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_-]*$")
INLINE_MAP_RE = re.compile(r"^([A-Za-z_][A-Za-z0-9_-]*):(?:\s+(.*))?$")

# Authentication material: never accepted, never echoed back.
_SECRET_NAMES = {
    "password", "passphrase", "passwd", "pwd", "secret", "token",
    "key", "keyfile", "keydata", "privatekey", "sshkey",
    "apikey", "identity", "identityfile", "credential", "credentials",
}
_SECRET_SUFFIXES = ("password", "passphrase", "passwd", "secret", "token", "apikey", "privatekey")


class ConfigError(ValueError):
    """The server list is unreadable or violates the safe schema."""


class ConfigNotFound(ConfigError):
    """No server list exists yet; this is a normal, non-fatal state."""


def _secret_field(name: str) -> bool:
    normalized = re.sub(r"[^A-Za-z0-9]", "", name).lower()
    return normalized in _SECRET_NAMES or normalized.endswith(_SECRET_SUFFIXES)


class _Line:
    __slots__ = ("indent", "text", "number")

    def __init__(self, indent: int, text: str, number: int):
        self.indent = indent
        self.text = text
        self.number = number


def _tokenize(source: str) -> list[_Line]:
    lines: list[_Line] = []
    for number, raw in enumerate(source.splitlines(), 1):
        if "\t" in raw:
            raise ConfigError(f"line {number}: tabs are not supported; indent with spaces")
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped in {"---", "..."}:
            raise ConfigError(f"line {number}: document markers are not supported")
        indent = len(raw) - len(raw.lstrip(" "))
        if indent == 0 and stripped.startswith("- "):
            raise ConfigError(f"line {number}: the top level must be a mapping")
        lines.append(_Line(indent, stripped, number))
    if not lines:
        raise ConfigError("the server list is empty")
    return lines


def _unquote(text: str, number: int) -> str:
    quote = text[0]
    body = text[1:]
    out: list[str] = []
    index = 0
    while index < len(body):
        char = body[index]
        if char == quote:
            if quote == "'" and index + 1 < len(body) and body[index + 1] == "'":
                out.append("'")
                index += 2
                continue
            if body[index + 1:].strip():
                raise ConfigError(f"line {number}: unexpected text after a quoted value")
            return "".join(out)
        if quote == '"' and char == "\\" and index + 1 < len(body):
            escaped = body[index + 1]
            out.append({"n": "\n", "t": "\t", '"': '"', "\\": "\\"}.get(escaped, escaped))
            index += 2
            continue
        out.append(char)
        index += 1
    raise ConfigError(f"line {number}: unterminated quoted value")


def _split_inline(text: str, number: int) -> list[str]:
    parts: list[str] = []
    current: list[str] = []
    quote = ""
    for char in text:
        if quote:
            current.append(char)
            if char == quote:
                quote = ""
            continue
        if char in "\"'":
            quote = char
            current.append(char)
        elif char == ",":
            parts.append("".join(current).strip())
            current = []
        else:
            current.append(char)
    if quote:
        raise ConfigError(f"line {number}: unterminated quoted inline list")
    parts.append("".join(current).strip())
    if any(not part for part in parts):
        raise ConfigError(f"line {number}: empty item in an inline list")
    return parts


def _scalar(text: str, number: int):
    text = text.strip()
    if not text:
        return None
    if text[0] in "\"'":
        return _unquote(text, number)
    lowered = text.lower()
    # YAML 1.2 core booleans only: ``off``/``yes`` stay strings so server names
    # such as "off" or "no" are not silently coerced. Use true/false.
    if lowered == "true":
        return True
    if lowered == "false":
        return False
    if lowered in {"null", "~"}:
        return None
    if text.startswith("[") and text.endswith("]"):
        inner = text[1:-1].strip()
        if not inner:
            return []
        return [_scalar(part, number) for part in _split_inline(inner, number)]
    if text.startswith(("&", "*", "|", ">", "{", "@", "`")):
        raise ConfigError(f"line {number}: unsupported YAML construct {text[:24]!r}")
    if re.match(r"^[+-]?\d+$", text):
        return int(text)
    if re.match(r"^[+-]?\d+\.\d+$", text):
        return float(text)
    if any(ord(char) < 32 for char in text):
        raise ConfigError(f"line {number}: control characters are not allowed")
    return text


def _parse_block(lines: list[_Line], index: int, indent: int):
    if lines[index].text.startswith("- "):
        return _parse_list(lines, index, indent)
    return _parse_mapping(lines, index, indent)


def _parse_mapping(lines: list[_Line], index: int, indent: int):
    result: dict = {}
    while index < len(lines):
        line = lines[index]
        if line.indent < indent:
            break
        if line.indent > indent:
            raise ConfigError(f"line {line.number}: unexpected indentation")
        if line.text.startswith("- "):
            break
        key, separator, rest = line.text.partition(":")
        key = key.strip()
        if not separator or not KEY_RE.match(key):
            raise ConfigError(f"line {line.number}: expected 'key: value'")
        if _secret_field(key):
            raise ConfigError(
                f"line {line.number}: credential field {key!r} is not supported; "
                "use your SSH agent or ~/.ssh/config instead"
            )
        if key in result:
            raise ConfigError(f"line {line.number}: duplicate key {key!r}")
        index += 1
        rest = rest.strip()
        if rest:
            result[key] = _scalar(rest, line.number)
        elif index < len(lines) and lines[index].indent > indent:
            result[key], index = _parse_block(lines, index, lines[index].indent)
        else:
            result[key] = None
    return result, index


def _parse_list(lines: list[_Line], index: int, indent: int):
    result: list = []
    while index < len(lines):
        line = lines[index]
        if line.indent < indent:
            break
        if line.indent > indent:
            raise ConfigError(f"line {line.number}: unexpected indentation")
        if not line.text.startswith("- "):
            break
        item = line.text[2:].strip()
        index += 1
        if not item:
            if index < len(lines) and lines[index].indent > indent:
                child, index = _parse_block(lines, index, lines[index].indent)
                result.append(child)
            else:
                result.append(None)
            continue
        match = INLINE_MAP_RE.match(item)
        if match:
            entry: dict = {}
            key = match.group(1)
            if _secret_field(key):
                raise ConfigError(
                    f"line {line.number}: credential field {key!r} is not supported; "
                    "use your SSH agent or ~/.ssh/config instead"
                )
            value = (match.group(2) or "").strip()
            entry[key] = _scalar(value, line.number) if value else None
            while index < len(lines) and lines[index].indent > indent:
                extra, index = _parse_mapping(lines, index, lines[index].indent)
                for extra_key, extra_value in extra.items():
                    if extra_key in entry:
                        raise ConfigError(f"line {line.number}: duplicate key {extra_key!r}")
                    entry[extra_key] = extra_value
            result.append(entry)
            continue
        result.append(_scalar(item, line.number))
        if index < len(lines) and lines[index].indent > indent:
            raise ConfigError(f"line {lines[index].number}: unexpected indentation")
    return result, index


def parse_yaml(source: str) -> dict:
    """Parse the documented YAML subset into plain Python values."""
    lines = _tokenize(source)
    value, index = _parse_block(lines, 0, lines[0].indent)
    if index != len(lines):
        raise ConfigError(f"line {lines[index].number}: unexpected content")
    if not isinstance(value, dict):
        raise ConfigError("the top level must be a mapping")
    return value


def _require_mapping(value, label: str) -> dict:
    if not isinstance(value, dict):
        raise ConfigError(f"{label} must be a mapping")
    return value


def _check_fields(value: dict, allowed: set[str], label: str) -> None:
    for key in value:
        if key not in allowed:
            raise ConfigError(f"unknown field {label}.{key}" if label != "config" else f"unknown field {key}")


def _boolean(value, label: str) -> bool:
    if isinstance(value, bool):
        return value
    raise ConfigError(f"{label} must be true or false")


def _integer(value, label: str, low: int, high: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ConfigError(f"{label} must be an integer")
    if not low <= value <= high:
        raise ConfigError(f"{label} must be between {low} and {high}")
    return value


def _text(value, label: str, limit: int, *, allow_empty: bool = True) -> str:
    if value is None and allow_empty:
        return ""
    if not isinstance(value, str):
        raise ConfigError(f"{label} must be a string")
    value = value.strip()
    if not value and not allow_empty:
        raise ConfigError(f"{label} is required")
    if len(value) > limit:
        raise ConfigError(f"{label} must be at most {limit} characters")
    if any(ord(char) < 32 for char in value):
        raise ConfigError(f"{label} must not contain control characters")
    return value


def _host(value, label: str) -> str:
    host = _text(value, label, 253, allow_empty=False)
    if not (HOST_RE.match(host) or HOST_V6_RE.match(host)):
        raise ConfigError(
            f"{label} must be a plain hostname or IP address "
            "(no spaces, user@, URLs or shell characters)"
        )
    return host


def _user(value, label: str) -> str:
    user = _text(value, label, 32)
    if user and not USER_RE.match(user):
        raise ConfigError(f"{label} must contain only letters, digits, dot, dash or underscore")
    return user


def _name(value, label: str) -> str:
    name = _text(value, label, 60, allow_empty=False)
    if not NAME_RE.match(name):
        raise ConfigError(f"{label} must use letters, digits, dot, dash or underscore")
    return name


def _tags(value, label: str) -> list[str]:
    if value is None:
        return []
    if not isinstance(value, list):
        raise ConfigError(f"{label} must be a list of short strings")
    if len(value) > 12:
        raise ConfigError(f"{label} can have at most 12 tags")
    tags: list[str] = []
    for item in value:
        if isinstance(item, (int, float)) and not isinstance(item, bool):
            item = str(item)
        tag = _text(item, label, 40, allow_empty=False)
        if not TAG_RE.match(tag):
            raise ConfigError(f"{label} tag {tag!r} may only use letters, digits, '.', ':', '-' or '_'")
        if tag not in tags:
            tags.append(tag)
    return tags


def _queue(value) -> dict:
    """Validate the optional bare-metal queue policy."""
    raw = _require_mapping(value or {}, "queue")
    _check_fields(raw, QUEUE_FIELDS, "queue")
    state_dir = _text(raw.get("state_dir", "/tmp/ark-gpu-queue"),
                      "queue.state_dir", 240, allow_empty=False)
    if not state_dir.startswith("/"):
        raise ConfigError("queue.state_dir must be an absolute POSIX path")
    if ".." in state_dir.split("/"):
        raise ConfigError("queue.state_dir must not contain '..'")
    setup = _text(raw.get("setup", ""), "queue.setup", 2000)
    env_raw = raw.get("env") or {}
    env_map = _require_mapping(env_raw, "queue.env")
    env: dict[str, str] = {}
    for key, value in env_map.items():
        key = str(key)
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,63}", key):
            raise ConfigError("queue.env keys must be environment variable names")
        if isinstance(value, bool):
            value = "1" if value else "0"
        elif isinstance(value, (int, float)):
            value = str(value)
        elif not isinstance(value, str):
            raise ConfigError(f"queue.env.{key} must be a string")
        if any(ord(char) < 32 for char in value) or len(value) > 500:
            raise ConfigError(f"queue.env.{key} must be a short single-line value")
        env[key] = value
    maps_raw = raw.get("path_map") or []
    if not isinstance(maps_raw, list):
        raise ConfigError("queue.path_map must be a list")
    path_map: list[dict] = []
    for index, item in enumerate(maps_raw, 1):
        item = _require_mapping(item, f"queue.path_map[{index}]")
        _check_fields(item, PATH_MAP_FIELDS, f"queue.path_map[{index}]")
        source = _text(item.get("from"), f"queue.path_map[{index}].from", 240,
                       allow_empty=False)
        target = _text(item.get("to"), f"queue.path_map[{index}].to", 240,
                       allow_empty=False)
        for label, path in (("from", source), ("to", target)):
            if not path.startswith("/") or ".." in path.split("/"):
                raise ConfigError(f"queue.path_map[{index}].{label} must be an absolute path without '..'")
        servers = item.get("servers") or []
        if not isinstance(servers, list):
            raise ConfigError(f"queue.path_map[{index}].servers must be a list")
        servers = [str(name).strip() for name in servers if str(name).strip()]
        path_map.append({"from": source.rstrip("/"), "to": target.rstrip("/"),
                         "servers": servers})
    return {
        "enabled": _boolean(raw.get("enabled", False), "queue.enabled"),
        "state_dir": state_dir,
        "poll_seconds": _integer(raw.get("poll_seconds", 5), "queue.poll_seconds", 1, 300),
        "default_timeout_minutes": _integer(
            raw.get("default_timeout_minutes", 240),
            "queue.default_timeout_minutes", 1, 1440),
        "max_jobs_per_gpu": _integer(raw.get("max_jobs_per_gpu", 0),
                                     "queue.max_jobs_per_gpu", 0, 16),
        "max_utilization_pct": _integer(raw.get("max_utilization_pct", 10),
                                        "queue.max_utilization_pct", 0, 100),
        "min_free_memory_mb": _integer(raw.get("min_free_memory_mb", 1024),
                                       "queue.min_free_memory_mb", 0, 1_000_000),
        "allow_colocation": _boolean(raw.get("allow_colocation", True),
                                     "queue.allow_colocation"),
        "setup": setup,
        "path_map": path_map,
        "env": env,
    }


def validate(data: dict) -> dict:
    """Validate a parsed server list and return the normalized structure."""
    data = _require_mapping(data, "config")
    _check_fields(data, TOP_FIELDS, "config")

    enabled = _boolean(data.get("enabled", False), "enabled")

    defaults_raw = data.get("defaults") or {}
    defaults = _require_mapping(defaults_raw, "defaults")
    _check_fields(defaults, DEFAULT_FIELDS, "defaults")
    default_port = _integer(defaults.get("port", 22), "defaults.port", 1, 65535)
    default_user = _user(defaults.get("user", ""), "defaults.user")
    default_timeout = _integer(defaults.get("timeout_seconds", 8), "defaults.timeout_seconds", 1, 60)

    servers_raw = data.get("servers")
    if servers_raw is None:
        servers_raw = []
    if not isinstance(servers_raw, list):
        raise ConfigError("servers must be a list")
    if len(servers_raw) > MAX_SERVERS:
        raise ConfigError(f"at most {MAX_SERVERS} servers can be configured")

    servers: list[dict] = []
    seen: set[str] = set()
    for position, raw in enumerate(servers_raw, 1):
        item = _require_mapping(raw, f"servers[{position}]")
        _check_fields(item, SERVER_FIELDS, f"servers[{position}]")
        name = _name(item.get("name"), f"servers[{position}].name")
        if name in seen:
            raise ConfigError(f"duplicate server name {name!r}")
        seen.add(name)
        host = _host(item.get("host"), f"servers[{position}].host")
        port = _integer(item.get("port", default_port), f"servers[{position}].port", 1, 65535)
        user = _user(item.get("user", default_user), f"servers[{position}].user")
        timeout = _integer(item.get("timeout_seconds", default_timeout),
                           f"servers[{position}].timeout_seconds", 1, 60)
        servers.append({
            "name": name,
            "host": host,
            "port": port,
            "user": user,
            "tags": _tags(item.get("tags"), f"servers[{position}].tags"),
            "note": _text(item.get("note", ""), f"servers[{position}].note", 200),
            "enabled": _boolean(item.get("enabled", True), f"servers[{position}].enabled"),
            "timeout_seconds": timeout,
        })

    return {
        "enabled": enabled,
        "defaults": {"port": default_port, "user": default_user, "timeout_seconds": default_timeout},
        "servers": servers,
        "queue": _queue(data.get("queue")),
    }


def load_config(path: str | Path) -> dict:
    """Read and validate ``path``; raises ConfigNotFound when it is absent."""
    target = Path(path).expanduser()
    try:
        raw = target.read_bytes()
    except FileNotFoundError as exc:
        raise ConfigNotFound(str(target)) from exc
    except OSError as exc:
        raise ConfigError(f"cannot read {target}: {exc}") from exc
    if len(raw) > MAX_CONFIG_BYTES:
        raise ConfigError(f"{target} is larger than {MAX_CONFIG_BYTES // 1024} KiB")
    try:
        text = raw.decode("utf-8-sig")
    except UnicodeDecodeError as exc:
        raise ConfigError(f"{target} is not valid UTF-8") from exc
    return validate(parse_yaml(text))


PUBLIC_SERVER_FIELDS = ("name", "host", "port", "user", "tags", "note", "enabled", "timeout_seconds")


def public_server(server: dict) -> dict:
    """Return only the documented, non-secret server fields."""
    return {key: server[key] for key in PUBLIC_SERVER_FIELDS if key in server}
