"""Read-only provider catalog for local agent CLIs.

ARK never installs, logs in to, or starts a provider here.  This module only:

* resolves the configured preset command on ``PATH`` (``shutil.which`` or an
  explicit absolute path);
* runs the provider's own ``--version`` with a short timeout, as a read-only
  probe;
* checks whether a credential *source* exists (environment variable name or a
  well-known credential file).  Only booleans, variable names and a sanitized
  version line are returned.

Credential values, credential file contents, tokens and API keys never leave
this module and are never logged.  A provider that ARK cannot verify is
reported as ``unknown`` instead of being guessed as authenticated.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

DEFAULT_TTL_SECONDS = 60
VERSION_TIMEOUT_SECONDS = 4
MAX_VERSION_LINE = 120

PROVIDER_STATUS_ORDER = {
    "ready": 0,
    "unknown": 1,
    "unauthenticated": 2,
    "missing": 3,
}

# Provider ids that have a stable, read-only ``--version`` flag.  The demo echo
# preset also qualifies because it is a local Python command.  A local shell or
# an operator-defined custom command is never probed.
VERSION_PROBED = {"claude", "codex", "dsh", "opencode", "cursor", "gemini", "echo"}

AUTH_ENV = {
    "claude": (
        "ANTHROPIC_API_KEY",
        "ANTHROPIC_AUTH_TOKEN",
        "CLAUDE_CODE_OAUTH_TOKEN",
    ),
    "codex": ("OPENAI_API_KEY", "CODEX_API_KEY"),
    "dsh": ("TENCENTHUB_API_KEY", "DEEPSEEK_API_KEY", "DSH_API_KEY"),
    "opencode": (
        "ANTHROPIC_API_KEY",
        "OPENAI_API_KEY",
        "GEMINI_API_KEY",
        "GOOGLE_API_KEY",
        "OPENROUTER_API_KEY",
        "DEEPSEEK_API_KEY",
        "TENCENTHUB_API_KEY",
        "GROQ_API_KEY",
    ),
    "cursor": ("CURSOR_API_KEY",),
    "gemini": ("GEMINI_API_KEY", "GOOGLE_API_KEY"),
}

AUTH_DETAIL = {
    "claude": "Run `claude /login` or set ANTHROPIC_API_KEY.",
    "codex": "Run `codex login` or set OPENAI_API_KEY.",
    "dsh": "Configure model credentials in DSH or Settings → Assistant.",
    "opencode": "Run `opencode auth login` or export a provider API key.",
    "cursor": "ARK cannot inspect the Cursor keychain; sign in with the Cursor CLI.",
    "gemini": "Run `gemini` and sign in, or set GEMINI_API_KEY.",
}

_AUTH_REDACTION = re.compile(r"[A-Za-z0-9_\-]{24,}")
_CONTROL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
_ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")

# A version probe never needs an API key.  Passing the server's full
# environment to a CLI would expose unrelated credentials to a child process.
_PROBE_ENV_KEYS = {
    "PATH", "Path", "PATHEXT", "SYSTEMROOT", "SystemRoot", "COMSPEC", "ComSpec",
    "WINDIR", "HOME", "USERPROFILE", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL",
    "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
    "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
}


def _probe_environment() -> dict:
    return {key: value for key, value in os.environ.items() if key in _PROBE_ENV_KEYS}


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _sanitize_version(text: str) -> str:
    """Return one bounded, printable line; never pass secret-looking blobs on."""
    value = _ANSI.sub("", str(text or ""))
    value = _CONTROL.sub("", value).replace("\r", " ").replace("\n", " ")
    value = " ".join(value.split())
    value = _AUTH_REDACTION.sub("[redacted]", value)
    return value[:MAX_VERSION_LINE]


def _resolve_program(program: str, *, which: Callable[[str], str | None]) -> str | None:
    program = str(program or "").strip()
    if not program:
        return None
    if os.path.isabs(program) or os.sep in program or (os.altsep and os.altsep in program):
        target = Path(program)
        return str(target) if target.is_file() else None
    return which(program)


def _version_argv(program: str) -> list[str]:
    if os.name == "nt" and Path(program).suffix.lower() in {".cmd", ".bat"}:
        # CreateProcess cannot execute a batch shim directly.  Quote each token
        # explicitly; never use shell=True with interpolated output.
        command = subprocess.list2cmdline([program, "--version"])
        return [os.environ.get("COMSPEC") or "cmd.exe", "/d", "/s", "/c", command]
    return [program, "--version"]


def _default_run(program: str, timeout: int = VERSION_TIMEOUT_SECONDS) -> dict:
    argv = _version_argv(program)
    try:
        completed = subprocess.run(
            argv,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            shell=False,
            check=False,
            text=True,
            encoding="utf-8",
            errors="replace",
            env=_probe_environment(),
        )
    except subprocess.TimeoutExpired:
        return {"returncode": None, "stdout": "", "stderr": "", "error": "timed out"}
    except (OSError, ValueError) as exc:
        return {"returncode": None, "stdout": "", "stderr": "", "error": str(exc)}
    return {
        "returncode": completed.returncode,
        "stdout": completed.stdout or "",
        "stderr": completed.stderr or "",
        "error": "",
    }


def _read_json_structure(path: Path) -> tuple[str, Any]:
    """Classify a credential file without returning any of its values."""
    try:
        if not path.is_file():
            return "missing", None
        if path.stat().st_size > 2 * 1024 * 1024:
            return "unreadable", None
        raw = path.read_text(encoding="utf-8", errors="replace")
        if not raw.strip():
            return "empty", None
        return "present", json.loads(raw)
    except (OSError, ValueError):
        return "unreadable", None


def _env_sources(env: dict, names: tuple[str, ...]) -> list[str]:
    return [f"env:{name}" for name in names if str(env.get(name) or "").strip()]


def _file_label(path: Path, home: Path) -> str:
    try:
        relative = path.resolve().relative_to(home.resolve())
        return "file:~/" + relative.as_posix()
    except (OSError, ValueError):
        return "file:" + path.name


def _auth(
    provider_id: str,
    *,
    env: dict,
    home: Path,
    assistant_key: Callable[[], tuple[bool, str]],
) -> dict:
    """Return a credential-availability record.  No values are included."""
    names = AUTH_ENV.get(provider_id, ())
    sources = _env_sources(env, names)
    base = {
        "state": "unknown",
        "authenticated": None,
        "method": None,
        "sources": sources,
        "detail": AUTH_DETAIL.get(provider_id, "ARK cannot verify this provider's credentials."),
    }
    if provider_id in {"echo", "shell"}:
        return {
            "state": "not_required",
            "authenticated": True,
            "method": "none",
            "sources": [],
            "detail": "Local process; no provider account is involved.",
        }
    if provider_id == "custom":
        return {
            "state": "unknown",
            "authenticated": None,
            "method": "operator",
            "sources": [],
            "detail": "Authentication is managed by the operator-defined command.",
        }
    if provider_id == "dsh":
        has_key, source = assistant_key()
        if has_key:
            return {
                "state": "authenticated",
                "authenticated": True,
                "method": "ark_assistant",
                "sources": sources + [source],
                "detail": "ARK found a configured assistant model credential for DSH.",
            }
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "detail": "Provider API key found in the environment."}
        return {**base, "state": "unknown", "authenticated": None,
                "detail": "DSH may manage its own profile; ARK cannot verify it. Configure a key in Settings → Assistant to pass one explicitly."}
    if provider_id == "claude":
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "detail": "Claude credential found in the environment."}
        path = home / ".claude" / ".credentials.json"
        state, data = _read_json_structure(path)
        sources = sources + [_file_label(path, home)] if state != "missing" else sources
        if state == "present" and isinstance(data, dict):
            oauth = data.get("claudeAiOauth") if isinstance(data.get("claudeAiOauth"), dict) else {}
            access = str(oauth.get("accessToken") or "").strip()
            refresh = str(oauth.get("refreshToken") or "").strip()
            expires = oauth.get("expiresAt")
            if refresh or access:
                expired = False
                try:
                    expired = bool(access and not refresh and expires and float(expires) <= time.time() * 1000)
                except (TypeError, ValueError):
                    expired = False
                if not expired:
                    return {**base, "state": "authenticated", "authenticated": True,
                            "method": "credentials_file", "sources": sources,
                            "detail": "Claude CLI login found in ~/.claude/.credentials.json."}
        if state == "unreadable":
            return {**base, "state": "unknown", "sources": sources,
                    "detail": "Claude's credential file exists but could not be read; run `claude /login` to repair it."}
        return {**base, "state": "unauthenticated", "authenticated": False,
                "sources": sources, "detail": AUTH_DETAIL["claude"]}
    if provider_id == "codex":
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "detail": "Provider API key found in the environment."}
        path = home / ".codex" / "auth.json"
        state, data = _read_json_structure(path)
        if state != "missing":
            sources = sources + [_file_label(path, home)]
        if state == "present" and isinstance(data, dict):
            tokens = data.get("tokens")
            key = data.get("OPENAI_API_KEY")
            if (isinstance(tokens, dict) and tokens) or str(key or "").strip():
                return {**base, "state": "authenticated", "authenticated": True,
                        "method": "credentials_file", "sources": sources,
                        "detail": "Codex login found in ~/.codex/auth.json."}
        if state == "unreadable":
            return {**base, "state": "unknown", "sources": sources,
                    "detail": "Codex's auth file exists but could not be read; run `codex login` to repair it."}
        return {**base, "state": "unauthenticated", "authenticated": False,
                "sources": sources, "detail": AUTH_DETAIL["codex"]}
    if provider_id == "opencode":
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "sources": sources,
                    "detail": "Provider API key found in the environment."}
        candidates = [
            home / ".local" / "share" / "opencode" / "auth.json",
            home / ".config" / "opencode" / "auth.json",
        ]
        appdata = str(env.get("APPDATA") or "").strip()
        if appdata:
            candidates.append(Path(appdata) / "opencode" / "auth.json")
        for path in candidates:
            state, data = _read_json_structure(path)
            if state == "present":
                sources = sources + [_file_label(path, home)]
                if isinstance(data, dict) and data:
                    return {**base, "state": "authenticated", "authenticated": True,
                            "method": "credentials_file", "sources": sources,
                            "detail": "OpenCode auth file found."}
            elif state == "unreadable":
                return {**base, "state": "unknown", "sources": sources + [_file_label(path, home)],
                        "detail": "OpenCode's auth file exists but could not be read."}
        return {**base, "state": "unauthenticated", "authenticated": False,
                "sources": sources, "detail": AUTH_DETAIL["opencode"]}
    if provider_id == "gemini":
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "detail": "Provider API key found in the environment."}
        path = home / ".gemini" / "oauth_creds.json"
        state, data = _read_json_structure(path)
        if state != "missing":
            sources = sources + [_file_label(path, home)]
        if state == "present" and isinstance(data, dict) and data:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "credentials_file", "sources": sources,
                    "detail": "Gemini login found in ~/.gemini/oauth_creds.json."}
        if state == "unreadable":
            return {**base, "state": "unknown", "sources": sources,
                    "detail": "Gemini's credential file exists but could not be read."}
        return {**base, "state": "unauthenticated", "authenticated": False,
                "sources": sources, "detail": AUTH_DETAIL["gemini"]}
    if provider_id == "cursor":
        if sources:
            return {**base, "state": "authenticated", "authenticated": True,
                    "method": "environment", "sources": sources,
                    "detail": "CURSOR_API_KEY found in the environment."}
        return {**base, "state": "unknown", "sources": [],
                "detail": AUTH_DETAIL["cursor"]}
    return base


# --------------------------------------------------------------------------
# Model discovery
#
# Every method below is read-only and bounded.  Nothing here starts a model
# run or spends tokens:
#
# * ``cli``     a provider's own list command.  Verified locally:
#               ``codex debug models [--bundled]`` (codex-cli 0.166).
#               ``opencode models`` is OpenCode's documented list command.
#               Claude Code, Gemini CLI, Cursor Agent and DSH have no list
#               command, so nothing is run for them.
# * ``config``  named model keys in the provider's local config files.  Only
#               those keys are read; credentials in the same files are never
#               touched, and secret-looking values are dropped.
# * ``api``     ``GET {base_url}/models`` on the OpenAI-compatible endpoint
#               ARK already knows (Settings -> Assistant, used by DSH).  This
#               runs only on an explicit refresh, never on page load.
# * ``builtin`` a curated, clearly labelled fallback so the picker is never
#               empty.
# --------------------------------------------------------------------------

MODEL_LIST_TIMEOUT_SECONDS = 8
MODEL_HTTP_TIMEOUT_SECONDS = 5
MAX_MODEL_OUTPUT_BYTES = 8 * 1024 * 1024
MAX_CONFIG_BYTES = 512 * 1024
MAX_MODELS = 150
MAX_MODEL_ERROR = 160

_MODEL_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@\[\]+-]{0,119}$")
_SECRET_PREFIXES = ("sk-", "sk_", "pk-", "rk-", "ghp_", "gho_", "xox", "aiza", "bearer ")
_SECRET_RUN = re.compile(r"[A-Za-z0-9_]{28,}")
_EFFORT_ID = re.compile(r"^[a-z][a-z0-9_-]{0,23}$")

# Effort levels printed by ``claude --help`` (Claude Code 2.1.x).
CLAUDE_EFFORTS = ["low", "medium", "high", "xhigh", "max"]

# Curated fallback lists.  They are labelled ``builtin`` in the API and in the
# UI so nobody mistakes them for what the provider offers today.
BUILTIN_MODELS: dict[str, list[dict]] = {
    "claude": [
        {"id": "opus", "label": "opus (latest Opus alias)"},
        {"id": "sonnet", "label": "sonnet (latest Sonnet alias)"},
        {"id": "haiku", "label": "haiku (latest Haiku alias)"},
        {"id": "fable", "label": "fable (latest Fable alias)"},
    ],
    "codex": [
        {"id": "gpt-6-astra"}, {"id": "gpt-6"}, {"id": "gpt-5.6"}, {"id": "gpt-5.5"},
        {"id": "gpt-5.4"}, {"id": "gpt-5.3-codex-spark"}, {"id": "gpt-5.2"},
    ],
    "gemini": [
        {"id": "gemini-2.5-pro"}, {"id": "gemini-2.5-flash"}, {"id": "gemini-2.5-flash-lite"},
    ],
    "opencode": [
        {"id": "anthropic/claude-sonnet-4-5"}, {"id": "openai/gpt-5"},
    ],
    "cursor": [
        {"id": "auto", "label": "auto"}, {"id": "sonnet-4.5"}, {"id": "gpt-5"},
    ],
    "dsh": [
        {"id": "deepseek-v4.1-flash-1017"},
    ],
}
BUILTIN_EFFORTS: dict[str, list[str]] = {"claude": CLAUDE_EFFORTS}

# Why a provider uses the builtin list (shown in the UI).
NO_LIST_COMMAND = {
    "claude": "Claude Code has no model list command",
    "gemini": "Gemini CLI has no model list command",
    "cursor": "Cursor Agent has no verified model list command",
    "dsh": "DSH has no model list command",
}

_LIST_ENV_KEYS = _PROBE_ENV_KEYS | {"CODEX_HOME", "OPENCODE_CONFIG", "USERNAME", "USER",
                                    "HOMEDRIVE", "HOMEPATH"}


def _list_environment() -> dict:
    return {key: value for key, value in os.environ.items() if key in _LIST_ENV_KEYS}


def _looks_secret(value: str) -> bool:
    lowered = value.strip().lower()
    return lowered.startswith(_SECRET_PREFIXES) or bool(_SECRET_RUN.search(value))


def _clean_model_id(value: Any) -> str:
    """Return a safe model id, or "" for anything that is not one."""
    if not isinstance(value, str):
        return ""
    value = value.strip()
    if not value or not _MODEL_ID.match(value) or _looks_secret(value):
        return ""
    return value


def _clean_label(value: Any, fallback: str) -> str:
    if not isinstance(value, str):
        return fallback
    text = _sanitize_version(value)[:80]
    return text or fallback


def _clean_efforts(values: Any) -> list[str]:
    out: list[str] = []
    for value in values or []:
        if isinstance(value, dict):
            value = value.get("effort") or value.get("id")
        if isinstance(value, str):
            value = value.strip().lower()
            if _EFFORT_ID.match(value) and value not in out:
                out.append(value)
    return out[:8]


def _short_error(text: Any) -> str:
    return _sanitize_version(str(text or ""))[:MAX_MODEL_ERROR]


def _command_argv(program: str, args: list[str]) -> list[str]:
    if os.name == "nt" and Path(program).suffix.lower() in {".cmd", ".bat"}:
        command = subprocess.list2cmdline([program, *args])
        return [os.environ.get("COMSPEC") or "cmd.exe", "/d", "/s", "/c", command]
    return [program, *args]


def _default_list_run(argv: list[str], timeout: float = MODEL_LIST_TIMEOUT_SECONDS) -> dict:
    """Run a read-only list command with a hard timeout and bounded output."""
    try:
        process = subprocess.Popen(
            _command_argv(argv[0], list(argv[1:])),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            shell=False,
            env=_list_environment(),
        )
    except (OSError, ValueError) as exc:
        return {"returncode": None, "stdout": "", "stderr": "", "error": str(exc)}
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        process.kill()
        try:
            process.communicate(timeout=2)
        except (subprocess.TimeoutExpired, OSError, ValueError):
            pass
        return {"returncode": None, "stdout": "", "stderr": "", "error": "timed out"}
    if len(stdout or b"") > MAX_MODEL_OUTPUT_BYTES:
        return {"returncode": process.returncode, "stdout": "", "stderr": "",
                "error": "output too large"}
    return {
        "returncode": process.returncode,
        "stdout": (stdout or b"").decode("utf-8", errors="replace"),
        "stderr": (stderr or b"")[:4096].decode("utf-8", errors="replace"),
        "error": "",
    }


def _no_list_run(argv: list[str], timeout: float = MODEL_LIST_TIMEOUT_SECONDS) -> dict:
    return {"returncode": None, "stdout": "", "stderr": "", "error": "list commands disabled"}


def _default_http_get(url: str, headers: dict,
                      timeout: float = MODEL_HTTP_TIMEOUT_SECONDS) -> dict:
    import urllib.error
    import urllib.request
    from urllib.parse import urlparse

    if urlparse(url).scheme not in {"http", "https"}:
        return {"status": None, "body": "", "error": "unsupported URL scheme"}
    request = urllib.request.Request(url, headers=headers, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:  # noqa: S310 - http(s) only
            body = response.read(MAX_MODEL_OUTPUT_BYTES + 1)
            if len(body) > MAX_MODEL_OUTPUT_BYTES:
                return {"status": response.status, "body": "", "error": "response too large"}
            return {"status": response.status,
                    "body": body.decode("utf-8", errors="replace"), "error": ""}
    except urllib.error.HTTPError as exc:
        return {"status": exc.code, "body": "", "error": f"HTTP {exc.code}"}
    except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
        reason = str(getattr(exc, "reason", exc)).lower()
        text = "timed out" if "timed out" in reason else "unreachable"
        return {"status": None, "body": "", "error": text}


def _entry(model_id: str, *, source: str, via: str, label: str = "",
           efforts: list[str] | None = None, default_effort: str = "") -> dict:
    item = {"id": model_id, "label": label or model_id, "source": source, "via": via}
    if efforts:
        item["efforts"] = list(efforts)
        if default_effort in efforts:
            item["default_effort"] = default_effort
    return item


def _result(method: str, *, models: list[dict] | None = None, default: str = "",
            default_effort: str = "", error: str = "", full_list: bool = False) -> dict:
    return {
        "method": method,
        "models": list(models or [])[:MAX_MODELS],
        "default": default,
        "default_effort": default_effort,
        "error": error,
        "full_list": full_list,
    }


def _run_failure(label: str, result: dict) -> str:
    if result.get("error"):
        return _short_error(f"{label} {result['error']}")
    if result.get("returncode") not in (0, None):
        return f"{label} exited with {result.get('returncode')}"
    return ""


def codex_cli_models(program: str, run: Callable[..., dict], *, bundled: bool) -> dict:
    """``codex debug models`` prints the model catalog Codex itself uses.

    ``--bundled`` dumps the catalog shipped with the binary without any
    refresh; the plain form lets Codex refresh its own cache and is used only
    on an explicit refresh.
    """
    args = ["debug", "models"] + (["--bundled"] if bundled else [])
    label = "codex " + " ".join(args)
    method = "cli:" + label
    result = run([program, *args], MODEL_LIST_TIMEOUT_SECONDS)
    failure = _run_failure(label, result)
    if failure:
        return _result(method, error=failure)
    try:
        data = json.loads(str(result.get("stdout") or ""))
    except ValueError:
        return _result(method, error=f"{label} returned malformed JSON")
    rows = data.get("models") if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return _result(method, error=f"{label} returned no model list")
    ranked = []
    for index, row in enumerate(rows):
        if not isinstance(row, dict) or row.get("visibility") == "hide":
            continue
        model_id = _clean_model_id(row.get("slug") or row.get("id"))
        if not model_id:
            continue
        try:
            priority = float(row.get("priority"))
        except (TypeError, ValueError):
            priority = float(index + 1000)
        efforts = _clean_efforts(row.get("supported_reasoning_levels"))
        ranked.append((priority, index, _entry(
            model_id, source="detected", via="cli",
            label=_clean_label(row.get("display_name"), model_id),
            efforts=efforts,
            default_effort=str(row.get("default_reasoning_level") or "").strip().lower())))
    ranked.sort(key=lambda item: (item[0], item[1]))
    models = [item[2] for item in ranked]
    if not models:
        return _result(method, error=f"{label} listed no models")
    return _result(method, models=models, full_list=True)


def opencode_cli_models(program: str, run: Callable[..., dict]) -> dict:
    """``opencode models`` prints one ``provider/model`` id per line."""
    method = "cli:opencode models"
    result = run([program, "models"], MODEL_LIST_TIMEOUT_SECONDS)
    failure = _run_failure("opencode models", result)
    if failure:
        return _result(method, error=failure)
    models = []
    seen = set()
    for line in _ANSI.sub("", str(result.get("stdout") or "")).splitlines():
        token = line.strip()
        if "/" not in token or " " in token:
            continue
        model_id = _clean_model_id(token)
        if model_id and model_id not in seen:
            seen.add(model_id)
            models.append(_entry(model_id, source="detected", via="cli"))
    if not models:
        return _result(method, error="opencode models returned no model ids")
    return _result(method, models=models, full_list=True)


def _read_text_bounded(path: Path) -> tuple[str, str]:
    """Return (state, text).  ``utf-8-sig`` tolerates a BOM written by editors."""
    try:
        if not path.is_file():
            return "missing", ""
        if path.stat().st_size > MAX_CONFIG_BYTES:
            return "unreadable", ""
        return "present", path.read_text(encoding="utf-8-sig", errors="replace")
    except OSError:
        return "unreadable", ""


def _read_json_config(path: Path) -> tuple[str, Any]:
    state, text = _read_text_bounded(path)
    if state != "present":
        return state, None
    if not text.strip():
        return "empty", None
    try:
        return "present", json.loads(text)
    except ValueError:
        # JSONC: drop whole-line ``//`` comments and retry once.
        stripped = "\n".join(line for line in text.splitlines()
                             if not line.lstrip().startswith("//"))
        try:
            return "present", json.loads(stripped)
        except ValueError:
            return "malformed", None


_TOML_TABLE = re.compile(r"^\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$")
_TOML_STRING = re.compile(
    r"^([A-Za-z0-9_-]+)\s*=\s*(?:\"([^\"\\]*)\"|'([^']*)')\s*(#.*)?$")


def _toml_model_keys(text: str) -> dict:
    """Extract only the model keys from a Codex ``config.toml``.

    Python 3.10 has no ``tomllib``.  Codex's model keys are plain strings, so a
    line reader that ignores every other key (including any secret) suffices.
    """
    top: dict[str, str] = {}
    profiles: dict[str, dict[str, str]] = {}
    table = ""
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        header = _TOML_TABLE.match(line)
        if header:
            table = header.group(1).strip()
            continue
        match = _TOML_STRING.match(line)
        if not match:
            continue
        key = match.group(1)
        value = match.group(2) if match.group(2) is not None else match.group(3)
        if key not in {"model", "model_reasoning_effort"}:
            continue
        if not table:
            top[key] = value
        elif table.startswith("profiles."):
            name = table.split(".", 1)[1].strip().strip("\"'")
            profiles.setdefault(name, {})[key] = value
    return {"top": top, "profiles": profiles}


def codex_config_models(home: Path, env: dict) -> dict:
    codex_home = str(env.get("CODEX_HOME") or "").strip()
    path = (Path(codex_home) if codex_home else home / ".codex") / "config.toml"
    method = "config:" + _file_label(path, home).split(":", 1)[1]
    state, text = _read_text_bounded(path)
    if state == "missing":
        return _result(method)
    if state != "present":
        return _result(method, error=f"{path.name} could not be read")
    parsed = _toml_model_keys(text)
    default = _clean_model_id(parsed["top"].get("model"))
    effort = _clean_efforts([parsed["top"].get("model_reasoning_effort")])
    models = []
    if default:
        models.append(_entry(default, source="detected", via="config"))
    for name, keys in parsed["profiles"].items():
        model_id = _clean_model_id(keys.get("model"))
        if model_id and all(item["id"] != model_id for item in models):
            models.append(_entry(model_id, source="detected", via="config",
                                 label=f"{model_id} (profile {_clean_label(name, 'profile')[:24]})"))
    return _result(method, models=models, default=default,
                   default_effort=effort[0] if effort else "")


_CLAUDE_MODEL_ENV = ("ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
                     "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL")


def claude_config_models(home: Path, env: dict) -> dict:
    """Read ``model``/``effortLevel`` and model env names from Claude settings.

    ``settings.json`` may also hold ``env.ANTHROPIC_AUTH_TOKEN``; only the
    model variable names above are looked up, never any other key.
    """
    path = home / ".claude" / "settings.json"
    method = "config:~/.claude/settings.json"
    state, data = _read_json_config(path)
    error = f"settings.json is {state}" if state in {"unreadable", "malformed"} else ""
    data = data if isinstance(data, dict) else {}
    settings_env = data.get("env") if isinstance(data.get("env"), dict) else {}
    default = _clean_model_id(data.get("model"))
    found: list[tuple[str, str]] = []
    for name in _CLAUDE_MODEL_ENV:
        value = _clean_model_id(settings_env.get(name)) or _clean_model_id(env.get(name))
        if value:
            found.append((value, name))
            if name == "ANTHROPIC_MODEL" and not default:
                default = value
    models = []
    if default:
        models.append(_entry(default, source="detected", via="config"))
    for value, name in found:
        if all(item["id"] != value for item in models):
            models.append(_entry(value, source="detected", via="config", label=f"{value} ({name})"))
    effort = _clean_efforts([data.get("effortLevel")])
    return _result(method, models=models, default=default,
                   default_effort=effort[0] if effort else "", error=error)


def opencode_config_models(home: Path, env: dict) -> dict:
    """Read ``model``, ``small_model`` and ``provider.*.models`` ids only."""
    base = str(env.get("XDG_CONFIG_HOME") or "").strip()
    folder = (Path(base) if base else home / ".config") / "opencode"
    for path in (folder / "opencode.json", folder / "opencode.jsonc", folder / "config.json"):
        state, data = _read_json_config(path)
        if state == "missing":
            continue
        method = "config:" + _file_label(path, home).split(":", 1)[1]
        if not isinstance(data, dict):
            return _result(method, error=f"{path.name} is {state}")
        default = _clean_model_id(data.get("model"))
        models = []
        for value in (default, _clean_model_id(data.get("small_model"))):
            if value and all(item["id"] != value for item in models):
                models.append(_entry(value, source="detected", via="config"))
        provider_map = data.get("provider") if isinstance(data.get("provider"), dict) else {}
        for provider_name, spec in provider_map.items():
            if not isinstance(spec, dict) or not isinstance(spec.get("models"), dict):
                continue
            for model_name, model_spec in spec["models"].items():
                model_id = _clean_model_id(f"{provider_name}/{model_name}")
                if model_id and all(item["id"] != model_id for item in models):
                    label = (model_spec.get("name") if isinstance(model_spec, dict) else "") or model_id
                    models.append(_entry(model_id, source="detected", via="config",
                                         label=_clean_label(label, model_id)))
        return _result(method, models=models, default=default)
    return _result("config:~/.config/opencode/opencode.json")


def gemini_config_models(home: Path, env: dict) -> dict:
    path = home / ".gemini" / "settings.json"
    method = "config:~/.gemini/settings.json"
    state, data = _read_json_config(path)
    if state == "missing":
        return _result(method)
    if not isinstance(data, dict):
        return _result(method, error=f"settings.json is {state}")
    raw = data.get("model")
    default = _clean_model_id(raw.get("name") if isinstance(raw, dict) else raw)
    models = [_entry(default, source="detected", via="config")] if default else []
    return _result(method, models=models, default=default)


CONFIG_READERS: dict[str, Callable[[Path, dict], dict]] = {
    "codex": codex_config_models,
    "claude": claude_config_models,
    "opencode": opencode_config_models,
    "gemini": gemini_config_models,
}


def openai_compatible_models(base_url: str, api_key: str,
                             http_get: Callable[..., dict]) -> dict:
    """``GET {base_url}/models``.  Errors are returned as a status, never raised."""
    method = "api:GET /models"
    base_url = str(base_url or "").strip().rstrip("/")
    if not base_url:
        return _result(method, error="no endpoint configured")
    headers = {"Accept": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    try:
        response = http_get(base_url + "/models", headers, MODEL_HTTP_TIMEOUT_SECONDS)
    except Exception as exc:  # noqa: BLE001 - discovery must never raise
        return _result(method, error=f"models endpoint failed ({type(exc).__name__})")
    if response.get("error"):
        return _result(method, error=_short_error(f"models endpoint {response['error']}"))
    try:
        data = json.loads(str(response.get("body") or ""))
    except ValueError:
        return _result(method, error="models endpoint returned malformed JSON")
    rows = data.get("data") if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return _result(method, error="models endpoint returned no model list")
    models = []
    for row in rows:
        model_id = _clean_model_id(row.get("id") if isinstance(row, dict) else row)
        if model_id and all(item["id"] != model_id for item in models):
            models.append(_entry(model_id, source="detected", via="api"))
    if not models:
        return _result(method, error="models endpoint listed no models")
    return _result(method, models=models, full_list=True)


def merge_models(preset: dict, results: list[dict], *, checked_at: str | None,
                 assistant_model: str = "", installed: bool = True) -> dict:
    """Combine configured, detected and builtin models into one catalog."""
    provider_id = str(preset.get("id") or "")
    options: list[dict] = []
    seen: dict[str, dict] = {}

    def add(item: dict) -> bool:
        existing = seen.get(item["id"])
        if existing is None:
            seen[item["id"]] = dict(item)
            options.append(seen[item["id"]])
            return True
        if item.get("efforts") and not existing.get("efforts"):
            existing["efforts"] = list(item["efforts"])
            if item.get("default_effort"):
                existing["default_effort"] = item["default_effort"]
        return False

    for raw in preset.get("models") or []:
        model_id = str(raw or "").strip()[:120]
        if model_id:
            add(_entry(model_id, source="configured", via="preset"))
    methods: list[str] = []
    errors: list[str] = []
    default = ""
    default_effort = ""
    full_list = False
    for result in results:
        if result.get("error"):
            errors.append(result["error"])
        if result.get("models") or result.get("default"):
            methods.append(result["method"])
        for item in result.get("models") or []:
            add(item)
        default = default or result.get("default") or ""
        default_effort = default_effort or result.get("default_effort") or ""
        full_list = full_list or bool(result.get("full_list") and result.get("models"))
    if provider_id == "dsh" and installed and assistant_model:
        model_id = _clean_model_id(assistant_model)
        if model_id:
            add(_entry(model_id, source="detected", via="config", label=f"{model_id} (ARK assistant)"))
            default = default or model_id
            methods.append("config:ARK assistant")
    builtin_used = False
    if not full_list and not preset.get("models"):
        for item in BUILTIN_MODELS.get(provider_id, []):
            builtin_used = add(_entry(item["id"], source="builtin", via="builtin",
                       label=item.get("label") or item["id"])) or builtin_used
    provider_efforts = BUILTIN_EFFORTS.get(provider_id) or []
    if provider_efforts:
        for item in options:
            item.setdefault("efforts", list(provider_efforts))
    if default and default in seen:
        seen[default]["default"] = True
        if default_effort and default_effort in (seen[default].get("efforts") or []):
            seen[default]["default_effort"] = default_effort
    if preset.get("models"):
        source = "configured"
    elif any(item["source"] == "detected" for item in options):
        source = "detected"
    elif builtin_used:
        source = "builtin"
    else:
        source = "provider_default"
    list_failed = any(str(result.get("method") or "").startswith(("cli:", "api:"))
                      and result.get("error") for result in results)
    reason_code = ""
    if full_list:
        detail = "Detected from " + ", ".join(methods) + "."
    elif builtin_used:
        if not installed:
            reason_code, reason = "not_installed", "the provider is not installed"
        elif list_failed:
            reason_code, reason = "list_failed", "the provider list command failed"
        elif provider_id in NO_LIST_COMMAND:
            reason_code, reason = "no_list_command", NO_LIST_COMMAND[provider_id]
        else:
            reason_code, reason = "not_run", "no list command was run"
        detail = "Built-in list; " + reason + "."
        if methods:
            detail += " Also read " + ", ".join(methods) + "."
    elif methods:
        detail = "Read from " + ", ".join(methods) + "."
    elif preset.get("models"):
        detail = "Configured in the preset."
    else:
        detail = "No model list is available; the provider default is used."
    return {
        "options": options[:MAX_MODELS],
        "source": source,
        "methods": methods,
        "default": default,
        "default_effort": default_effort,
        "error": "; ".join(dict.fromkeys(errors))[:MAX_MODEL_ERROR * 2],
        "checked_at": checked_at,
        "detail": detail,
        "builtin": builtin_used,
        "full_list": full_list,
        "reason": reason_code,
    }


def _kind_for(preset: dict) -> str:
    if preset.get("demo"):
        return "demo"
    if preset["id"] == "shell":
        return "local"
    if preset["id"] == "custom":
        return "custom"
    return "cli"


def _capabilities(preset: dict, assistant_model: str = "",
                  discovery: dict | None = None) -> dict:
    if discovery is None:
        discovery = merge_models(preset, [], checked_at=None,
                                 assistant_model=assistant_model)
    options = list(discovery.get("options") or [])
    # ``models`` stays a plain list of ids for older readers; the rich rows are
    # in ``model_options``.
    models = [item["id"] for item in options]
    configured_efforts = list(preset.get("efforts") or [])
    default_option = next((item for item in options if item.get("default")), None)
    if configured_efforts:
        efforts, efforts_source = configured_efforts, "configured"
    elif default_option and default_option.get("efforts"):
        efforts = list(default_option["efforts"])
        efforts_source = "builtin" if default_option.get("source") == "builtin" else "detected"
    elif BUILTIN_EFFORTS.get(str(preset.get("id") or "")):
        efforts, efforts_source = list(BUILTIN_EFFORTS[preset["id"]]), "builtin"
    else:
        efforts, efforts_source = [], "unsupported"
    permission_modes = list(
        preset.get("permission_modes") or preset.get("access_modes") or [])
    supports_approval = bool(preset.get("supports_approval"))
    if not permission_modes:
        permission_modes = (["read_only", "plan", "ask", "accept_edits", "full_access"]
                            if supports_approval
                            else ["read_only", "plan", "full_access"])
    approval_modes = [mode for mode in permission_modes
                      if mode in {"ask", "accept_edits"}]
    return {
        "models": models,
        "model_options": options,
        "models_source": discovery.get("source") or "provider_default",
        "models_methods": list(discovery.get("methods") or []),
        "models_checked_at": discovery.get("checked_at"),
        "models_error": discovery.get("error") or "",
        "models_detail": discovery.get("detail") or "",
        "models_reason": discovery.get("reason") or "",
        "default_model": discovery.get("default") or "",
        "default_effort": discovery.get("default_effort") or "",
        "custom_models": True,
        "efforts": efforts,
        "efforts_source": efforts_source,
        "permission_modes": permission_modes,
        "permission_enforcement": ("ark_access_token+stdin_approvals"
                                   if supports_approval else "ark_access_token"),
        "native_approval": supports_approval,
        "approval": {
            "supported": supports_approval,
            "mode": "adapter_mediated" if supports_approval else "unsupported",
            "protocol": "ark-permission-v1" if supports_approval else "",
            "modes": approval_modes,
            "detail": (
                "The adapter requests approval on stdout and ARK answers on stdin "
                "(ark-permission-v1)."
                if supports_approval else
                "This preset has no interactive stdin, so approval modes are unavailable."
            ),
        },
        "streaming": "line_buffered",
        "followup": bool(preset.get("followup")),
        "prompt_mode": preset.get("prompt_mode") or "stdin",
        "stdin": bool(preset.get("followup")),
        "demo": bool(preset.get("demo")),
    }


def _provider_path() -> str:
    """PATH used for provider discovery, including common user-install dirs.

    A daemon/systemd-launched ARK process often has a shorter PATH than the
    interactive shell, so npm/pip tools installed in ~/.local/bin or ~/bin
    appear missing even though the user can run them.
    """
    current = os.environ.get("PATH") or ""
    if os.name == "nt":
        return current
    home = Path.home()
    extras = [home / ".local" / "bin", home / "bin", Path("/snap/bin"), Path("/usr/local/bin")]
    parts = [str(path) for path in extras if path.is_dir()]
    parts.extend(part for part in current.split(os.pathsep) if part)
    seen = set()
    ordered = []
    for part in parts:
        if part not in seen:
            seen.add(part)
            ordered.append(part)
    return os.pathsep.join(ordered)


def _which_provider(program: str) -> str | None:
    return shutil.which(program, path=_provider_path())


class ProviderCatalog:
    """Cached, read-only probe of every configured task-agent preset."""

    def __init__(
        self,
        presets: list[dict],
        *,
        env: dict | None = None,
        home: str | Path | None = None,
        ttl: int = DEFAULT_TTL_SECONDS,
        which: Callable[[str], str | None] | None = None,
        run: Callable[[str], dict] | None = None,
        assistant_key: Callable[[], tuple[bool, str]] | None = None,
        assistant_model: Callable[[], str] | None = None,
        clock: Callable[[], float] | None = None,
        list_run: Callable[..., dict] | None = None,
        http_get: Callable[..., dict] | None = None,
        assistant_endpoint: Callable[[], dict] | None = None,
    ):
        self.presets = [dict(preset) for preset in presets]
        # ``None`` means "read the live process environment on every probe", so
        # a manual refresh can notice an operator's newly set key variable.
        self._env_override = dict(env) if env is not None else None
        self.env = dict(os.environ if env is None else env)
        self.home = Path(home).expanduser() if home else Path.home()
        self.ttl = max(0, int(ttl))
        self._which = which or _which_provider
        self._run = run or _default_run
        self._assistant_key = assistant_key or self._default_assistant_key
        self._assistant_model = assistant_model or self._default_assistant_model
        self._clock = clock or time.time
        # A test that fakes the version runner but not the list runner must
        # stay hermetic, so CLI model listing is then disabled.
        if list_run is not None:
            self._list_run = list_run
        else:
            self._list_run = _default_list_run if run is None else _no_list_run
        self._http_get = http_get or _default_http_get
        self._assistant_endpoint = assistant_endpoint or self._default_assistant_endpoint
        # Results of refresh-only methods (HTTP, Codex catalog refresh), kept
        # until the next explicit refresh so TTL re-probes never hit the network.
        self._remote_models: dict[str, dict] = {}
        self._explicit = False
        self._lock = threading.RLock()
        self._cached: dict | None = None
        self._cached_at = 0.0
        self._last_refresh: str | None = None
        self._probe_count = 0

    # ------------------------------------------------------------ assistant
    @staticmethod
    def _default_assistant_key() -> tuple[bool, str]:
        try:
            from . import assistant_config
            config = assistant_config.load()
        except (OSError, ValueError):
            return False, "ark_assistant"
        return bool(str(config.get("api_key") or "").strip()), "ark_assistant"

    @staticmethod
    def _default_assistant_model() -> str:
        try:
            from . import assistant_config
            return str(assistant_config.load().get("model") or "")
        except (OSError, ValueError):
            return ""

    @staticmethod
    def _default_assistant_endpoint() -> dict:
        try:
            from . import assistant_config
            config = assistant_config.load()
        except (OSError, ValueError):
            return {}
        return {"base_url": config.get("base_url") or "", "api_key": config.get("api_key") or ""}

    # --------------------------------------------------------------- models
    def _discover_models(self, preset: dict, resolved: str | None, env: dict) -> dict:
        provider_id = str(preset.get("id") or "")
        checked_at = utc_now()
        results: list[dict] = []
        installed = bool(resolved)
        if installed and provider_id in CONFIG_READERS:
            try:
                results.append(CONFIG_READERS[provider_id](self.home, env))
            except Exception as exc:  # noqa: BLE001 - discovery must not break the catalog
                results.append(_result("config", error=f"config read failed ({type(exc).__name__})"))
        if installed and provider_id == "codex":
            remote = self._remote_models.get(provider_id)
            if self._explicit:
                remote = codex_cli_models(str(resolved), self._list_run, bundled=False)
                self._remote_models[provider_id] = remote
            if remote and remote.get("models"):
                results.append(remote)
            else:
                bundled = codex_cli_models(str(resolved), self._list_run, bundled=True)
                if remote and remote.get("error") and not bundled.get("models"):
                    results.append(remote)
                results.append(bundled)
        elif installed and provider_id == "opencode":
            results.append(opencode_cli_models(str(resolved), self._list_run))
        elif installed and provider_id == "dsh":
            if self._explicit:
                endpoint = self._assistant_endpoint() or {}
                self._remote_models[provider_id] = openai_compatible_models(
                    str(endpoint.get("base_url") or ""), str(endpoint.get("api_key") or ""),
                    self._http_get)
            remote = self._remote_models.get(provider_id)
            if remote:
                results.append(remote)
        return merge_models(preset, results, checked_at=checked_at,
                            assistant_model=self._assistant_model(), installed=installed)

    # ---------------------------------------------------------------- probe
    def _probe_one(self, preset: dict) -> dict:
        provider_id = str(preset.get("id") or "")
        command = list(preset.get("command") or [])
        program = command[0] if command else ""
        resolved = _resolve_program(program, which=self._which)
        installed = bool(resolved)
        version = None
        version_error = ""
        env = dict(self._env_override) if self._env_override is not None else dict(os.environ)
        if installed and provider_id in VERSION_PROBED:
            result = self._run(str(resolved))
            output = _sanitize_version(
                (str(result.get("stdout") or "") + " " + str(result.get("stderr") or "")).strip())
            if result.get("error"):
                version_error = f"version probe {result.get('error')}"
            elif result.get("returncode") not in (0, None):
                version_error = f"version probe exited with {result.get('returncode')}"
            elif output:
                version = output
            else:
                version_error = "version probe returned no version"
        auth = _auth(
            provider_id,
            env=env,
            home=self.home,
            assistant_key=self._assistant_key,
        )
        if not installed:
            status = "missing"
            auth = {
                **auth,
                "state": "not_installed",
                "authenticated": None,
                "detail": f"{program or 'command'} was not found on PATH.",
            }
        elif auth.get("authenticated") is False:
            status = "unauthenticated"
        elif auth.get("authenticated") is True:
            status = "ready"
        else:
            status = "unknown"
        detail = {
            "ready": f"{preset.get('label') or provider_id} is installed and credentials were detected.",
            "missing": f"{program or 'command'} was not found on PATH.",
            "unauthenticated": auth.get("detail") or "Credentials were not detected.",
            "unknown": auth.get("detail") or "The provider is installed but ARK cannot verify its sign-in state.",
        }[status]
        if status in {"ready", "unknown"} and version_error:
            detail = f"{detail} {version_error.capitalize()}."
        return {
            "id": provider_id,
            "label": str(preset.get("label") or provider_id),
            "description": str(preset.get("description") or ""),
            "kind": _kind_for(preset),
            "preset_id": provider_id,
            "command": program,
            "installed": installed,
            "launchable": installed,
            "version": version,
            "version_error": version_error,
            "status": status,
            "authenticated": auth.get("authenticated"),
            "auth": auth,
            "health": {"state": status, "detail": detail, "checked_at": utc_now()},
            "capabilities": _capabilities(
                preset, discovery=self._discover_models(preset, resolved, env)),
        }

    def _snapshot_uncached(self) -> dict:
        started = time.time()
        providers = [self._probe_one(preset) for preset in self.presets]
        providers.sort(key=lambda item: (
            PROVIDER_STATUS_ORDER.get(item.get("status"), 9),
            str(item.get("label") or "").lower(),
        ))
        counts: dict[str, int] = {}
        for item in providers:
            counts[item["status"]] = counts.get(item["status"], 0) + 1
        payload = {
            "providers": providers,
            "counts": counts,
            "total": len(providers),
            "generated_at": utc_now(),
            "last_refresh": self._last_refresh,
            "probe_ms": int((time.time() - started) * 1000),
            "ttl_seconds": self.ttl,
            "cached": False,
            "environment": {
                "platform": os.name,
                # Paths are user-relative labels; ARK never returns HOME itself.
                "home": "~",
            },
        }
        self._probe_count += 1
        return payload

    def snapshot(self, *, force: bool = False) -> dict:
        now = self._clock()
        with self._lock:
            fresh = (self._cached is not None
                     and not force
                     and now - self._cached_at < self.ttl)
            if fresh:
                payload = dict(self._cached)
                payload["cached"] = True
                payload["cache_age_seconds"] = int(max(0, now - self._cached_at))
                return payload
            self._explicit = bool(force)
            try:
                payload = self._snapshot_uncached()
            finally:
                self._explicit = False
            self._cached = payload
            self._cached_at = now
            if force:
                self._last_refresh = payload["generated_at"]
                payload["last_refresh"] = self._last_refresh
                self._cached = payload
            return payload

    def refresh(self) -> dict:
        with self._lock:
            self._cached = None
            self._cached_at = 0.0
            payload = self.snapshot(force=True)
            payload["cache_age_seconds"] = 0
            return payload

    def summary(self) -> dict:
        """Lightweight status map used by the session preset API."""
        payload = self.snapshot()
        return {
            "generated_at": payload["generated_at"],
            "cached": payload.get("cached", False),
            "providers": {
                item["id"]: {
                    "status": item["status"],
                    "installed": item["installed"],
                    "version": item["version"],
                    "authenticated": item.get("authenticated"),
                    "auth_state": (item.get("auth") or {}).get("state"),
                    "auth_detail": (item.get("auth") or {}).get("detail"),
                    "health": item.get("health") or {},
                    "capabilities": item.get("capabilities") or {},
                }
                for item in payload["providers"]
            },
        }

    def metadata_for(self, provider_id: str) -> dict:
        provider = self.summary()["providers"].get(str(provider_id or ""))
        return dict(provider) if provider else {}


def presets_with_provider_status(presets: list[dict], catalog: ProviderCatalog | None) -> list[dict]:
    """Merge catalog state into public preset rows without changing launch rules."""
    if catalog is None:
        return presets
    try:
        status = catalog.summary()["providers"]
    except (OSError, ValueError):
        return presets
    out = []
    for preset in presets:
        item = dict(preset)
        provider = status.get(str(item.get("id") or ""))
        if provider:
            item["provider"] = provider
            item["provider_status"] = provider.get("status")
            item["provider_version"] = provider.get("version")
            item["provider_auth"] = {
                "state": provider.get("auth_state"),
                "authenticated": provider.get("authenticated"),
                "detail": provider.get("auth_detail"),
            }
            item["provider_capabilities"] = provider.get("capabilities") or {}
        out.append(item)
    return out
