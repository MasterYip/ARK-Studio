"""Private assistant/LLM configuration for ARK Studio.

The API key and endpoint live outside the repository and outside the project
folder, in ``$ARK_PRIVATE_DIR/assistant.json`` (default ``~/.ark/assistant.json``).
The public API only reports whether a key exists and its last four characters.
"""
from __future__ import annotations

import json
import os
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlparse

try:
    import winreg
except ImportError:  # pragma: no cover - non-Windows
    winreg = None

DEFAULT_BASE_URL = "https://tokenhub.tencentmaas.com/v1"
DEFAULT_MODEL = "deepseek-v4.1-flash-1017"
DEFAULT_REASONING_EFFORT = "high"
DEFAULT_TIMEOUT = 90
EFFORTS = {"low", "medium", "high", "max"}


def _env_value(name: str) -> str:
    value = (os.environ.get(name) or "").strip()
    if value or winreg is None:
        return value
    for hive in (winreg.HKEY_CURRENT_USER, winreg.HKEY_LOCAL_MACHINE):
        try:
            with winreg.OpenKey(hive, r"Environment") as key:
                value, _ = winreg.QueryValueEx(key, name)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        except OSError:
            continue
    return ""


def private_dir() -> Path:
    override = _env_value("ARK_PRIVATE_DIR")
    return Path(override).expanduser() if override else Path.home() / ".ark"


def config_path() -> Path:
    return private_dir() / "assistant.json"


def _file_values() -> dict:
    path = config_path()
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def load() -> dict:
    file_values = _file_values()
    env_key = _env_value("DEEPSEEK_API_KEY") or _env_value("ARK_DEEPSEEK_API_KEY")
    file_has_key = "api_key" in file_values
    return {
        "api_key": str(file_values.get("api_key") or "") if file_has_key else env_key,
        "base_url": str(file_values.get("base_url") or _env_value("DEEPSEEK_BASE_URL") or DEFAULT_BASE_URL).rstrip("/"),
        "model": str(file_values.get("model") or _env_value("DEEPSEEK_MODEL") or DEFAULT_MODEL),
        "reasoning_effort": str(file_values.get("reasoning_effort") or _env_value("DEEPSEEK_REASONING_EFFORT") or DEFAULT_REASONING_EFFORT).lower(),
        "timeout": int(file_values.get("timeout") or _env_value("DEEPSEEK_TIMEOUT") or DEFAULT_TIMEOUT),
    }


def public(config: dict | None = None) -> dict:
    config = config or load()
    key = config.get("api_key") or ""
    return {
        "configured": bool(key),
        "base_url": config.get("base_url") or DEFAULT_BASE_URL,
        "model": config.get("model") or DEFAULT_MODEL,
        "reasoning_effort": config.get("reasoning_effort") or DEFAULT_REASONING_EFFORT,
        "timeout": config.get("timeout") or DEFAULT_TIMEOUT,
        "has_key": bool(key),
        "key_hint": f"…{key[-4:]}" if len(key) >= 4 else ("" if not key else "…"),
        "source": "private" if config_path().is_file() else ("environment" if key else "default"),
        "path": str(config_path()),
    }


def _validated_url(value: str) -> str:
    url = str(value or "").strip().rstrip("/")
    parsed = urlparse(url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValueError("base_url must be an http(s) URL")
    if any(ch.isspace() for ch in url):
        raise ValueError("base_url must not contain spaces")
    return url


def save(patch: dict) -> dict:
    if not isinstance(patch, dict):
        raise ValueError("config must be an object")
    current = _file_values()
    merged = {
        "base_url": current.get("base_url") or DEFAULT_BASE_URL,
        "model": current.get("model") or DEFAULT_MODEL,
        "reasoning_effort": current.get("reasoning_effort") or DEFAULT_REASONING_EFFORT,
        "timeout": current.get("timeout") or DEFAULT_TIMEOUT,
        "api_key": current.get("api_key") or "",
    }
    if "base_url" in patch:
        merged["base_url"] = _validated_url(patch["base_url"])
    if "model" in patch:
        model = str(patch["model"] or "").strip()
        if not model or len(model) > 120 or any(ch.isspace() for ch in model):
            raise ValueError("model must be a short name without spaces")
        merged["model"] = model
    if "reasoning_effort" in patch:
        effort = str(patch["reasoning_effort"] or "").strip().lower()
        if effort not in EFFORTS:
            raise ValueError(f"reasoning_effort must be one of {', '.join(sorted(EFFORTS))}")
        merged["reasoning_effort"] = effort
    if "timeout" in patch:
        timeout = int(patch["timeout"])
        if not 5 <= timeout <= 300:
            raise ValueError("timeout must be between 5 and 300 seconds")
        merged["timeout"] = timeout
    if patch.get("clear_key"):
        merged["api_key"] = ""
    elif "api_key" in patch and str(patch.get("api_key") or "").strip():
        merged["api_key"] = str(patch["api_key"]).strip()
    path = config_path()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    handle = tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False)
    try:
        json.dump(merged, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        handle.close()
        os.replace(handle.name, path)
    finally:
        try:
            Path(handle.name).unlink(missing_ok=True)
        except OSError:
            pass
    try:
        os.chmod(path, 0o600)
    except OSError:
        pass
    return public(load())


def test_connection(config: dict | None = None) -> dict:
    config = config or load()
    key = config.get("api_key") or ""
    if not key:
        raise ValueError("Save an API key first")
    body = {
        "model": config["model"],
        "messages": [{"role": "user", "content": "Reply with exactly OK."}],
        "max_tokens": 16,
        "stream": False,
    }
    request = urllib.request.Request(
        config["base_url"] + "/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"},
        method="POST",
    )
    started = time.time()
    try:
        with urllib.request.urlopen(request, timeout=min(30, config["timeout"])) as response:
            data = json.load(response)
    except urllib.error.HTTPError as exc:
        detail = exc.read(500).decode("utf-8", "replace").strip()
        raise ValueError(f"HTTP {exc.code}: {detail or exc.reason}") from exc
    except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
        raise ValueError(str(exc)) from exc
    message = ""
    try:
        message = data["choices"][0]["message"].get("content") or ""
    except (KeyError, IndexError, TypeError):
        pass
    return {"ok": True, "model": config["model"], "latency_ms": round((time.time() - started) * 1000),
            "reply": str(message)[:200]}
