"""Project-level settings that travel with the `.ark` folder.

Graph physics travels with the canvas simulation, so the API and the UI share
one vocabulary.  Teams may also configure the ARK assistant and the coding-agent
presets at project level; a person's own settings override them.  Project
settings never hold credential **values**: an agent/assistant credential is a
name of an environment variable (``api_key_env`` / ``env_names``) that the
server operator provides, so a project folder can be committed or shared
without leaking a token.
"""
from __future__ import annotations

import math
import os
import re
from urllib.parse import urlparse

from .tags import RELATIONS

GRAPH_RANGES = {
    "repulsion": (0.0, 16000.0),
    "distance": (0.0, 320.0),
    "gravity": (0.0, 0.02),
    "damping": (0.0, 1.0),
}

# Info Hub's current defaults (slider 59 / 0 / 95 / 30).
GRAPH_DEFAULTS = {
    "repulsion": 3286.0639999999985,
    "distance": 0.0,
    "gravity": 0.011506123058542407,
    "damping": 0.3,
}

PALETTE_KEYS = (
    "accent", "accent2", "accent_soft", "background", "surface", "text",
    "muted", "line", "success", "warning", "danger",
)
PALETTE_PRESETS = {
    "ark-light": {
        "accent": "#2563eb", "accent2": "#1d4ed8", "accent_soft": "#eaf1ff",
        "background": "#f5f6f8", "surface": "#ffffff", "text": "#1a2231",
        "muted": "#5b6474", "line": "#d6dce5", "success": "#047857",
        "warning": "#b45309", "danger": "#dc2626",
    },
    "indigo-light": {
        "accent": "#4f46e5", "accent2": "#6d28d9", "accent_soft": "#eeecff",
        "background": "#f6f5fb", "surface": "#ffffff", "text": "#24213b",
        "muted": "#625c7b", "line": "#dcd8eb", "success": "#047857",
        "warning": "#b45309", "danger": "#be123c",
    },
    "forest-light": {
        "accent": "#047857", "accent2": "#0f766e", "accent_soft": "#e1f3ec",
        "background": "#f3f8f5", "surface": "#ffffff", "text": "#172c24",
        "muted": "#536c60", "line": "#d1e0d7", "success": "#047857",
        "warning": "#b45309", "danger": "#b91c1c",
    },
    "sunset-light": {
        "accent": "#c2410c", "accent2": "#9a3412", "accent_soft": "#fff0e5",
        "background": "#fbf6f3", "surface": "#ffffff", "text": "#35251f",
        "muted": "#765e52", "line": "#e6d8cf", "success": "#047857",
        "warning": "#b45309", "danger": "#b91c1c",
    },
    "mono-light": {
        "accent": "#475569", "accent2": "#334155", "accent_soft": "#e8ecf0",
        "background": "#f6f7f8", "surface": "#ffffff", "text": "#20252c",
        "muted": "#59616c", "line": "#d5d9de", "success": "#047857",
        "warning": "#a16207", "danger": "#b91c1c",
    },
    "ark": {
        "accent": "#3b82f6", "accent2": "#60a5fa", "accent_soft": "#172554",
        "background": "#0f141c", "surface": "#1d2532", "text": "#e8eef6",
        "muted": "#93a2b6", "line": "#273243", "success": "#34d399",
        "warning": "#f59e0b", "danger": "#ef4444",
    },
    "indigo": {
        "accent": "#6366f1", "accent2": "#a78bfa", "accent_soft": "#211a4d",
        "background": "#101222", "surface": "#1c1f36", "text": "#ecebff",
        "muted": "#a5a3c4", "line": "#303456", "success": "#34d399",
        "warning": "#fbbf24", "danger": "#fb7185",
    },
    "forest": {
        "accent": "#10b981", "accent2": "#6ee7b7", "accent_soft": "#062e25",
        "background": "#0d1714", "surface": "#172722", "text": "#e7f7ef",
        "muted": "#94b8a8", "line": "#28443a", "success": "#34d399",
        "warning": "#f59e0b", "danger": "#f87171",
    },
    "sunset": {
        "accent": "#f97316", "accent2": "#fbbf24", "accent_soft": "#451a03",
        "background": "#1a1110", "surface": "#2a1b18", "text": "#fff1e8",
        "muted": "#c4a294", "line": "#4a2d24", "success": "#34d399",
        "warning": "#fbbf24", "danger": "#ef4444",
    },
    "mono": {
        "accent": "#94a3b8", "accent2": "#cbd5e1", "accent_soft": "#1e293b",
        "background": "#0f1216", "surface": "#1b2028", "text": "#f1f5f9",
        "muted": "#94a3b8", "line": "#303845", "success": "#a7f3d0",
        "warning": "#fde68a", "danger": "#fecaca",
    },
}
DEFAULT_PALETTE = {"preset": "ark", **PALETTE_PRESETS["ark"]}
_HEX_RE = re.compile(r"^#[0-9a-fA-F]{6}$")


class SettingsError(ValueError):
    pass


def normalize_graph(value: dict | None) -> dict:
    source = value if isinstance(value, dict) else {}
    out = {}
    for key, (low, high) in GRAPH_RANGES.items():
        raw = source.get(key)
        try:
            number = float(raw)
            if not math.isfinite(number):
                raise ValueError
        except (TypeError, ValueError):
            number = GRAPH_DEFAULTS[key]
        out[key] = max(low, min(high, number))
    return out


def update_graph(base: dict | None, patch: dict | None) -> dict:
    """Validate an explicit patch. Missing keys keep their current values."""
    merged = normalize_graph(base)
    if not isinstance(patch, dict):
        raise SettingsError("graph settings must be an object")
    for key, value in patch.items():
        if key not in GRAPH_RANGES:
            raise SettingsError(f"unknown graph setting: {key}")
        if isinstance(value, bool) or not isinstance(value, (int, float, str)):
            raise SettingsError(f"{key} must be a number")
        try:
            number = float(value)
        except (TypeError, ValueError) as exc:
            raise SettingsError(f"{key} must be a number") from exc
        if not math.isfinite(number):
            raise SettingsError(f"{key} must be finite")
        low, high = GRAPH_RANGES[key]
        if number < low or number > high:
            raise SettingsError(f"{key} must be between {low:g} and {high:g}")
        merged[key] = number
    return merged


# ------------------------------------------------------------ line ends
# Visio-style arrowheads for graph edges.  ``style`` is the project-wide
# default; ``relations`` holds partial per-relation overrides (any of head,
# placement, size); ``mind_tree`` also decorates Mind-map tree branches.
# The two symmetric relations (reverse label == label) default to no arrow.
ARROW_HEADS = ("none", "triangle", "open", "stealth", "diamond", "diamond_filled", "circle", "bar")
ARROW_PLACEMENTS = ("target", "source", "both", "none")
ARROW_SIZE_RANGE = (0.5, 2.5)
ARROW_STYLE_KEYS = ("head", "placement", "size")
# Per-relation entries may also set ``reverse`` (display only: draw and lay
# out the relation as target -> source).  Without it, these relations are
# reversed by default because their stored direction reads against the flow
# ("A derived from B" / "A depends on B" flow from B to A).
ARROW_RELATION_KEYS = (*ARROW_STYLE_KEYS, "reverse")
ARROW_REVERSED_BY_DEFAULT = ("derived_from", "depends_on")
ARROW_DEFAULT_STYLE = {"head": "triangle", "placement": "target", "size": 1.0}
ARROW_DEFAULT_RELATIONS = {
    rel: {"placement": "none"}
    for rel, meta in RELATIONS.items() if meta["reverse"] == meta["label"]
}
ARROW_DEFAULTS = {
    "style": dict(ARROW_DEFAULT_STYLE),
    "relations": {rel: dict(value) for rel, value in ARROW_DEFAULT_RELATIONS.items()},
    "mind_tree": False,
}


def _arrow_style(value, *, partial: bool, strict: bool, where: str, allow_reverse: bool = False) -> dict:
    """Validate one style object.  ``partial`` styles may omit keys."""
    if not isinstance(value, dict):
        if strict:
            raise SettingsError(f"{where} must be an object")
        return {} if partial else dict(ARROW_DEFAULT_STYLE)
    unknown = sorted(set(value) - set(ARROW_RELATION_KEYS if allow_reverse else ARROW_STYLE_KEYS))
    if unknown and strict:
        raise SettingsError(f"unknown {where} field: {unknown[0]}")
    out = {} if partial else dict(ARROW_DEFAULT_STYLE)
    for key, choices in (("head", ARROW_HEADS), ("placement", ARROW_PLACEMENTS)):
        if key not in value:
            continue
        raw = value[key]
        if isinstance(raw, str) and raw in choices:
            out[key] = raw
        elif strict:
            raise SettingsError(f"{where} {key} must be one of: {', '.join(choices)}")
    if allow_reverse and "reverse" in value:
        if isinstance(value["reverse"], bool):
            out["reverse"] = value["reverse"]
        elif strict:
            raise SettingsError(f"{where} reverse must be true or false")
    if "size" in value:
        raw = value["size"]
        number = None
        if not isinstance(raw, bool) and isinstance(raw, (int, float, str)):
            try:
                number = float(raw)
            except ValueError:
                number = None
        low, high = ARROW_SIZE_RANGE
        if number is not None and math.isfinite(number) and low <= number <= high:
            out["size"] = round(number, 3)
        elif strict:
            raise SettingsError(f"{where} size must be between {low:g} and {high:g}")
    return out


def _arrow_relations(value, *, strict: bool) -> dict:
    if not isinstance(value, dict):
        if strict:
            raise SettingsError("arrow relations must be an object")
        return {rel: dict(item) for rel, item in ARROW_DEFAULT_RELATIONS.items()}
    out = {}
    for rel, item in value.items():
        if rel not in RELATIONS:
            if strict:
                raise SettingsError(f"unknown relation: {rel}")
            continue
        style = _arrow_style(item, partial=True, strict=strict, where=f"arrow style for {rel}",
                             allow_reverse=True)
        if style:
            out[rel] = style
    return out


def normalize_arrows(value: dict | None) -> dict:
    """Lenient read of stored arrow settings; invalid parts fall back."""
    source = value if isinstance(value, dict) else {}
    return {
        "style": _arrow_style(source.get("style"), partial=False, strict=False, where="arrow style"),
        "relations": _arrow_relations(source.get("relations", ARROW_DEFAULT_RELATIONS), strict=False),
        "mind_tree": source.get("mind_tree") is True,
    }


def update_arrows(base: dict | None, patch) -> dict:
    """Validate an explicit patch.  ``style`` merges; ``relations`` replaces."""
    merged = normalize_arrows(base)
    if not isinstance(patch, dict):
        raise SettingsError("arrow settings must be an object")
    unknown = sorted(set(patch) - {"style", "relations", "mind_tree"})
    if unknown:
        raise SettingsError(f"unknown arrow setting: {unknown[0]}")
    if "style" in patch:
        style = _arrow_style(patch["style"], partial=True, strict=True, where="arrow style")
        merged["style"] = {**merged["style"], **style}
    if "relations" in patch:
        merged["relations"] = _arrow_relations(patch["relations"], strict=True)
    if "mind_tree" in patch:
        if not isinstance(patch["mind_tree"], bool):
            raise SettingsError("mind_tree must be true or false")
        merged["mind_tree"] = patch["mind_tree"]
    return merged


def palette_mode(colors: dict) -> str:
    """Match native controls to the palette, including legacy single palettes."""
    color = colors.get("background", "#0f141c")
    rgb = [int(color[i:i + 2], 16) / 255 for i in (1, 3, 5)]
    linear = [v / 12.92 if v <= .04045 else ((v + .055) / 1.055) ** 2.4 for v in rgb]
    return "light" if sum(v * w for v, w in zip(linear, (.2126, .7152, .0722))) > .179 else "dark"


def _palette_colors(value: dict | None, default: str = "ark") -> dict:
    source = value if isinstance(value, dict) else {}
    preset = str(source.get("preset") or "").strip().lower()
    if preset not in PALETTE_PRESETS and preset != "custom":
        preset = default
    colors = dict(PALETTE_PRESETS.get(preset, PALETTE_PRESETS[default]))
    for key in PALETTE_KEYS:
        raw = source.get(key)
        if isinstance(raw, str) and _HEX_RE.match(raw.strip()):
            colors[key] = raw.strip().lower()
    return {"preset": preset, **colors}


def normalize_palette(value: dict | None) -> dict:
    source = value if isinstance(value, dict) else {}
    legacy = _palette_colors(source)
    modes = {mode: _palette_colors(source.get(mode), default)
             for mode, default in (("light", "ark-light"), ("dark", "ark"))}
    if not any(isinstance(source.get(mode), dict) for mode in modes):
        modes[palette_mode(legacy)] = legacy
    return {"enabled": bool(source.get("enabled", False)), **legacy, **modes,
            "saved": _saved_palettes(source.get("saved"))}


def _saved_palettes(value, *, strict: bool = False) -> dict:
    if value is None:
        return {}
    if not isinstance(value, dict) or len(value) > 24:
        if strict:
            raise SettingsError("saved palettes must be an object with at most 24 themes")
        return {}
    result = {}
    for name, pair in value.items():
        try:
            if (not isinstance(name, str) or not 1 <= len(name) <= 64
                    or name != name.strip() or any(ord(c) < 32 for c in name)
                    or name in {"__proto__", "constructor", "prototype"}):
                raise SettingsError("saved theme name must contain 1 to 64 visible characters")
            if not isinstance(pair, dict) or set(pair) != {"light", "dark"}:
                raise SettingsError("saved theme must contain light and dark palettes")
            colors = {}
            for mode, default in (("light", "ark-light"), ("dark", "ark")):
                patch = pair[mode]
                if not isinstance(patch, dict) or set(patch) - {*PALETTE_KEYS, "preset"}:
                    raise SettingsError("saved theme accepts only palette colors and preset")
                colors[mode] = _update_palette_colors({"preset": default}, patch)
                if palette_mode(colors[mode]) != mode:
                    raise SettingsError(f"{mode} palette background must match its mode")
            result[name] = colors
        except SettingsError:
            if strict:
                raise
    return result


def update_palette(base: dict | None, patch: dict | None) -> dict:
    if not isinstance(patch, dict):
        raise SettingsError("palette settings must be an object")
    current = normalize_palette(base)
    modes = {mode: current[mode] for mode in ("light", "dark")}
    for mode in modes:
        if mode not in patch:
            continue
        if not isinstance(patch[mode], dict):
            raise SettingsError(f"{mode} palette must be an object")
        modes[mode] = _update_palette_colors(current[mode], patch[mode])
        if palette_mode(modes[mode]) != mode:
            raise SettingsError(f"{mode} palette background must match its mode")
    legacy_patch = {key: value for key, value in patch.items() if key in (*PALETTE_KEYS, "preset")}
    legacy = _update_palette_colors(current, legacy_patch) if legacy_patch else _palette_colors(current)
    if legacy_patch and not any(mode in patch for mode in modes):
        modes[palette_mode(legacy)] = legacy
    elif any(mode in patch for mode in modes):
        legacy = modes[palette_mode(legacy)]
    saved = dict(current["saved"])
    if "saved" in patch:
        if not isinstance(patch["saved"], dict):
            raise SettingsError("saved palettes must be an object")
        for name, pair in patch["saved"].items():
            if pair is None:
                saved.pop(name, None)
            else:
                saved.update(_saved_palettes({name: pair}, strict=True))
        saved = _saved_palettes(saved, strict=True)
    return {"enabled": bool(patch.get("enabled", current["enabled"])), **legacy, **modes, "saved": saved}


def _update_palette_colors(base: dict, patch: dict) -> dict:
    current = _palette_colors(base)
    merged = dict(current)
    if "preset" in patch:
        preset = str(patch.get("preset") or "").strip().lower()
        if preset not in PALETTE_PRESETS and preset != "custom":
            raise SettingsError(f"unknown palette preset: {preset}")
        if preset != current["preset"]:
            if preset == "custom":
                merged["preset"] = "custom"
            else:
                merged = _palette_colors({"preset": preset})
    for key in PALETTE_KEYS:
        if key not in patch:
            continue
        raw = patch.get(key)
        if not isinstance(raw, str) or not _HEX_RE.match(raw.strip()):
            raise SettingsError(f"{key} must be a #RRGGBB colour")
        merged[key] = raw.strip().lower()
    return _palette_colors(merged)


def project_settings(manifest: dict | None) -> dict:
    manifest = manifest if isinstance(manifest, dict) else {}
    settings = manifest.get("settings")
    if not isinstance(settings, dict):
        settings = {}
    return {
        "graph": normalize_graph(settings.get("graph")),
        "arrows": normalize_arrows(settings.get("arrows")),
        "assistant": normalize_assistant(settings.get("assistant"), strict=False),
        "agents": normalize_agents(settings.get("agents"), strict=False),
        "palette": normalize_palette(settings.get("palette")),
        "sessions": normalize_sessions(settings.get("sessions")),
    }


# ------------------------------------------------------ session context
SESSION_DEFAULTS = {"accept_uploads": False}


def normalize_sessions(value) -> dict:
    """Session context consent (docs/agents/session-context.md); default off."""
    source = value if isinstance(value, dict) else {}
    return {"accept_uploads": source.get("accept_uploads") is True}


def update_sessions(base: dict | None, patch) -> dict:
    if not isinstance(patch, dict):
        raise SettingsError("sessions settings must be an object")
    unknown = set(patch) - set(SESSION_DEFAULTS)
    if unknown:
        raise SettingsError(f"unknown sessions setting: {sorted(unknown)[0]}")
    current = normalize_sessions(base)
    if "accept_uploads" in patch:
        if not isinstance(patch["accept_uploads"], bool):
            raise SettingsError("sessions.accept_uploads must be true or false")
        current["accept_uploads"] = patch["accept_uploads"]
    return current


# ------------------------------------------------- assistant / task agents
ASSISTANT_FIELDS = ("base_url", "model", "reasoning_effort", "timeout")
REASONING_EFFORTS = {"low", "medium", "high", "max"}
ENV_NAME_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]{0,63}$")
PROVIDER_ID_RE = re.compile(r"^[a-z][a-z0-9_-]{0,40}$")
# A project admin controls command lines and endpoint URLs, so project-scope
# environment names are an exfiltration surface: never let a project name an
# ARK credential or a generic token/secret/password/credential variable.
FORBIDDEN_PROJECT_ENV_RE = re.compile(
    r"^ARK_|TOKEN|SECRET|PASSWORD|CREDENTIAL", re.IGNORECASE)
# Project assistant credentials must point at a known model-provider key (or
# the operator's explicit allow-list), never at an arbitrary server secret.
PROJECT_ASSISTANT_KEY_ENVS = {
    "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
    "TENCENTHUB_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
    "OPENROUTER_API_KEY", "GROQ_API_KEY", "CURSOR_API_KEY", "DSH_API_KEY",
}


def _project_assistant_key_allowlist() -> set[str]:
    override = str(os.environ.get("ARK_PROJECT_ASSISTANT_KEY_ENVS") or "").strip()
    if override:
        return {item.strip().upper() for item in override.split(",") if item.strip()}
    return set(PROJECT_ASSISTANT_KEY_ENVS)


def _validate_project_env_name(name: str, *, field: str) -> str:
    text = str(name or "").strip()
    if FORBIDDEN_PROJECT_ENV_RE.search(text):
        raise SettingsError(
            f"{field} must not reference ARK_/TOKEN/SECRET/PASSWORD/CREDENTIAL "
            "environment variables; use a personal setting for credentials")
    return text


def _validate_project_assistant_env(name: str) -> str:
    text = _validate_project_env_name(name, field="assistant api_key_env")
    if text.upper() not in _project_assistant_key_allowlist():
        raise SettingsError(
            "assistant api_key_env is not an allowed provider key; use a known "
            "model API key name or ask the operator to extend "
            "ARK_PROJECT_ASSISTANT_KEY_ENVS")
    return text


def _clean_url(value: str) -> str:
    url = str(value or "").strip().rstrip("/")
    parsed = urlparse(url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise SettingsError("assistant base_url must be an http(s) URL")
    if any(ch.isspace() for ch in url) or len(url) > 400:
        raise SettingsError("assistant base_url is invalid")
    return url


def normalize_assistant(value, *, strict: bool = True) -> dict:
    """Validate the project-level assistant block (no credential values).

    ``strict=False`` is the read path: it drops stored values that a previous
    version allowed but current policy rejects, instead of failing project
    loading.
    """
    source = value if isinstance(value, dict) else {}
    out: dict = {}
    if "base_url" in source and source.get("base_url") not in (None, ""):
        out["base_url"] = _clean_url(source["base_url"])
    if "model" in source and source.get("model") not in (None, ""):
        model = str(source["model"]).strip()
        if not model or len(model) > 120 or any(ch.isspace() for ch in model):
            raise SettingsError("assistant model must be a short name without spaces")
        out["model"] = model
    if "reasoning_effort" in source and source.get("reasoning_effort") not in (None, ""):
        effort = str(source["reasoning_effort"]).strip().lower()
        if effort not in REASONING_EFFORTS:
            raise SettingsError(
                "assistant reasoning_effort must be one of "
                + ", ".join(sorted(REASONING_EFFORTS)))
        out["reasoning_effort"] = effort
    if "timeout" in source and source.get("timeout") not in (None, ""):
        try:
            timeout = int(source["timeout"])
        except (TypeError, ValueError) as exc:
            raise SettingsError("assistant timeout must be a number") from exc
        if not 5 <= timeout <= 300:
            raise SettingsError("assistant timeout must be between 5 and 300 seconds")
        out["timeout"] = timeout
    if "api_key_env" in source and source.get("api_key_env") not in (None, ""):
        name = str(source["api_key_env"]).strip()
        if not ENV_NAME_RE.fullmatch(name):
            if strict:
                raise SettingsError("assistant api_key_env must be an environment variable name")
        else:
            try:
                out["api_key_env"] = _validate_project_assistant_env(name)
            except SettingsError:
                if strict:
                    raise
    if "enabled" in source and source.get("enabled") is not None:
        out["enabled"] = bool(source["enabled"])
    return out


def update_assistant(base: dict | None, patch) -> dict:
    merged = normalize_assistant(base)
    if patch is None:
        return {}
    if not isinstance(patch, dict):
        raise SettingsError("assistant settings must be an object")
    unknown = set(patch) - set(ASSISTANT_FIELDS) - {"api_key_env", "enabled"}
    if unknown:
        raise SettingsError(f"unknown assistant setting: {sorted(unknown)[0]}")
    for key, value in patch.items():
        if value in (None, ""):
            merged.pop(key, None)
    # Validate by normalising the explicit values on top of the current ones.
    merged = normalize_assistant({**merged, **{k: v for k, v in patch.items()
                                               if v not in (None, "")}})
    return merged


def normalize_agents(value, *, allowed_providers: list[str] | None = None,
                     strict: bool = True) -> dict:
    """Validate project-scope agent configuration.

    Project settings may declare **which** provider is enabled and which
    environment-variable names carry its credential, but never the command line
    itself and never the credential value.  A project admin therefore cannot
    turn PATCH /api/project/settings into arbitrary code execution on the host.
    Commands live only in a person's own sealed settings.
    """
    source = value if isinstance(value, dict) else {}
    providers = source.get("providers")
    out: dict = {"providers": {}}
    if not isinstance(providers, dict):
        return out
    allowed = {str(pid).lower() for pid in (allowed_providers or [])}
    for provider_id, raw in providers.items():
        pid = str(provider_id or "").strip().lower()
        if not PROVIDER_ID_RE.fullmatch(pid):
            if strict:
                raise SettingsError(f"invalid task-agent provider id: {provider_id}")
            continue
        if allowed and pid not in allowed:
            if strict:
                raise SettingsError(f"unknown task-agent provider: {pid}")
            continue
        if not isinstance(raw, dict):
            if strict:
                raise SettingsError(f"provider {pid} settings must be an object")
            continue
        item: dict = {}
        env_names = raw.get("env_names")
        if env_names not in (None, []):
            if not isinstance(env_names, list):
                if strict:
                    raise SettingsError(f"provider {pid} env_names must be a list")
                continue
            names = []
            for name in env_names:
                text = str(name or "").strip()
                if not ENV_NAME_RE.fullmatch(text):
                    if strict:
                        raise SettingsError(f"provider {pid} env_names must be variable names")
                    continue
                try:
                    text = _validate_project_env_name(
                        text, field=f"provider {pid} env_names")
                except SettingsError:
                    if strict:
                        raise
                    continue
                if text not in names:
                    names.append(text)
            item["env_names"] = names
        if "enabled" in raw and raw.get("enabled") is not None:
            item["enabled"] = bool(raw.get("enabled"))
        out["providers"][pid] = item
    return out


def update_agents(base: dict | None, patch,
                  *, allowed_providers: list[str] | None = None) -> dict:
    merged = normalize_agents(base)
    if patch is None:
        return {"providers": {}}
    if not isinstance(patch, dict):
        raise SettingsError("agents settings must be an object")
    unknown = set(patch) - {"providers"}
    if unknown:
        raise SettingsError(f"unknown agents setting: {sorted(unknown)[0]}")
    providers = patch.get("providers")
    if providers is None:
        return merged
    if not isinstance(providers, dict):
        raise SettingsError("agents.providers must be an object")
    allowed = {str(pid).lower() for pid in (allowed_providers or [])}
    for provider_id, raw in providers.items():
        pid = str(provider_id or "").strip().lower()
        if allowed and pid not in allowed:
            raise SettingsError(f"unknown task-agent provider: {pid}")
        if raw in (None, {}):
            merged["providers"].pop(pid, None)
            continue
        if not isinstance(raw, dict):
            raise SettingsError(f"provider {pid} settings must be an object")
        if "command" in raw or "label" in raw:
            raise SettingsError(
                "project settings must not configure task-agent commands or labels; "
                "commands are personal settings (personal override > project > "
                "server/env)")
        current = dict(merged["providers"].get(pid) or {})
        current.update(raw)
        merged["providers"][pid] = normalize_agents(
            {"providers": {pid: current}},
            allowed_providers=allowed_providers)["providers"][pid]
    return merged


def settings_patch(manifest: dict | None, body: dict | None,
                   *, allowed_providers: list[str] | None = None) -> dict:
    current = project_settings(manifest)
    body = body if isinstance(body, dict) else {}
    known = {"graph", "arrows", "assistant", "agents", "palette", "sessions"}
    has_section = any(key in body for key in known)
    if not has_section:
        # Backwards compatibility: a bare physics patch is still accepted.
        current["graph"] = update_graph(current["graph"], body)
        return current
    unknown = set(body) - known
    if unknown:
        raise SettingsError(f"unknown settings section: {sorted(unknown)[0]}")
    if "graph" in body:
        current["graph"] = update_graph(current["graph"], body["graph"])
    if "arrows" in body:
        current["arrows"] = update_arrows(current["arrows"], body["arrows"])
    if "assistant" in body:
        current["assistant"] = update_assistant(current["assistant"], body["assistant"])
    if "agents" in body:
        current["agents"] = update_agents(current["agents"], body["agents"],
                                          allowed_providers=allowed_providers)
    if "palette" in body:
        current["palette"] = update_palette(current["palette"], body["palette"])
    if "sessions" in body:
        current["sessions"] = update_sessions(current["sessions"], body["sessions"])
    return current


# ------------------------------------------------ personal appearance overrides
# A person may override the project's presentation settings for themselves
# (stored with their account profile, see org.py).  Each group is all-or-
# nothing: an override replaces the project value for that person; a missing
# group inherits the project value.  The project manifest is never touched.
APPEARANCE_OVERRIDE_GROUPS = ("palette", "graph", "arrows")


def personal_palette(value) -> dict:
    """Validate a personal palette override: ``{enabled, light, dark}``."""
    if not isinstance(value, dict) or set(value) - {"enabled", "light", "dark"}:
        raise SettingsError("personal palette accepts only enabled, light and dark")
    if not {"light", "dark"} <= set(value):
        raise SettingsError("personal palette must contain light and dark palettes")
    enabled = value.get("enabled", True)
    if not isinstance(enabled, bool):
        raise SettingsError("personal palette enabled must be true or false")
    out = {"enabled": enabled}
    for mode, default in (("light", "ark-light"), ("dark", "ark")):
        patch = value[mode]
        if not isinstance(patch, dict) or set(patch) - {*PALETTE_KEYS, "preset"}:
            raise SettingsError("personal palette accepts only palette colors and preset")
        colors = _update_palette_colors({"preset": default}, patch)
        if palette_mode(colors) != mode:
            raise SettingsError(f"{mode} palette background must match its mode")
        out[mode] = colors
    return out


def personal_graph(value) -> dict:
    """Validate a personal graph-physics override; every field is required."""
    if not isinstance(value, dict):
        raise SettingsError("graph settings must be an object")
    missing = sorted(set(GRAPH_RANGES) - set(value))
    if missing:
        raise SettingsError(f"personal graph settings need: {', '.join(missing)}")
    return update_graph(GRAPH_DEFAULTS, value)


def personal_arrows(value) -> dict:
    """Validate a personal arrow override; ``style`` is required, the rest
    falls back to the defaults (not to the project value)."""
    if not isinstance(value, dict):
        raise SettingsError("arrow settings must be an object")
    if "style" not in value:
        raise SettingsError("personal arrow settings need: style")
    style = _arrow_style(value["style"], partial=True, strict=True, where="arrow style")
    missing = sorted(set(ARROW_STYLE_KEYS) - set(style))
    if missing:
        raise SettingsError(f"personal arrow style needs: {', '.join(missing)}")
    return update_arrows(ARROW_DEFAULTS, value)


_PERSONAL_GROUPS = {"palette": personal_palette, "graph": personal_graph, "arrows": personal_arrows}


def personal_appearance(value, *, strict: bool = False) -> dict:
    """Normalize stored overrides; invalid groups are dropped unless ``strict``."""
    if value is None:
        return {}
    if not isinstance(value, dict):
        if strict:
            raise SettingsError("appearance overrides must be an object")
        return {}
    unknown = sorted(set(value) - set(APPEARANCE_OVERRIDE_GROUPS))
    if unknown and strict:
        raise SettingsError(f"unknown appearance group: {', '.join(unknown)}")
    out = {}
    for group, check in _PERSONAL_GROUPS.items():
        if value.get(group) is None:
            continue
        try:
            out[group] = check(value[group])
        except SettingsError:
            if strict:
                raise
    return out


def update_personal_appearance(base, patch) -> dict:
    """Merge a ``{group: value | null}`` patch; ``null`` clears the override."""
    if not isinstance(patch, dict):
        raise SettingsError("appearance overrides must be an object")
    unknown = sorted(set(patch) - set(APPEARANCE_OVERRIDE_GROUPS))
    if unknown:
        raise SettingsError(f"unknown appearance group: {', '.join(unknown)}")
    merged = personal_appearance(base)
    for group, value in patch.items():
        if value is None:
            merged.pop(group, None)
        else:
            merged[group] = _PERSONAL_GROUPS[group](value)
    return merged
