"""A project resource register, contributed to repeatedly by its members.

Every project member sees every resource and recorded location. Contributions
are stored per user so setup cannot replace another person's records. A path
on someone else's machine is information, not proof of access on this host.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import threading
from dataclasses import dataclass, field
from functools import wraps
from pathlib import Path
from typing import Any, Iterable, Mapping

from . import protocol
from . import setup as base
from .setup import SetupError

LAYER_FORMAT = "ark-setup-layer"
LAYER_VERSION = "2.0"
PROJECT_FILE = "project.json"
USERS_DIR = "users"
LEGACY_FILE = "environment.json"
LEGACY_BACKUP = "environment.json.v1.bak"

KINDS = tuple(base.KIND_FIELDS)
# The 1.0 field that held the machine-specific location of an entry.
LOCATION_FIELD = {"repository": "path", "task_space": "location", "storage": "location"}
PROJECT_FIELDS = ("objective", "non_goals", "agents", "policies", "cadence", "task_ids",
                  "unknowns", "skipped", "notes")
USER_FIELDS = ("resources", "policies", "notes", "unknowns", "hosts", "locations")
POLICY_RANK = {"allow": 0, "ask": 1, "deny": 2}
_HOST_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$")
MAX_RESOURCES = 200
MAX_LOCATIONS = 400
_WRITE_LOCK = threading.RLock()


def _serialized(func):
    @wraps(func)
    def call(*args, **kwargs):
        with _WRITE_LOCK:
            return func(*args, **kwargs)
    return call


# --------------------------------------------------------------------------- #
# Viewer
# --------------------------------------------------------------------------- #

@dataclass
class Viewer:
    """Who is asking.  ``user`` is empty only for the legacy server token."""

    user: str = ""
    teams: frozenset = field(default_factory=frozenset)  # lower-cased team ids and names
    admin: bool = False
    legacy: bool = False
    can_write: bool = True
    token_kind: str = "session"  # session | personal | server | local | cli
    host_id: str = ""
    host_source: str = "server"

    def __post_init__(self) -> None:
        self.user = str(self.user or "")
        self.teams = frozenset(str(t).lower() for t in (self.teams or ()) if str(t).strip())
        self.host_id = clean_host_id(self.host_id) or base.machine_fingerprint()

    def is_user(self, name: str) -> bool:
        return bool(self.user) and self.user.lower() == str(name or "").lower()

    def public(self, label: str = "") -> dict:
        return {"user": self.user or None, "token_kind": self.token_kind, "admin": self.admin,
                "legacy": self.legacy, "teams": sorted(self.teams),
                "host": {"id": self.host_id, "label": label or self.host_id,
                         "source": self.host_source,
                         "server": self.host_id == base.machine_fingerprint()}}


def legacy_viewer(host_id: str = "") -> Viewer:
    """The legacy server token can read the whole project register."""
    return Viewer(user="", legacy=True, admin=False, token_kind="server", host_id=host_id)


def clean_host_id(value: Any) -> str:
    text = str(value or "").strip()[:48]
    return text if _HOST_RE.match(text) else ""


def default_host_label(host_id: str) -> str:
    return f"host-{str(host_id)[:6]}"


# --------------------------------------------------------------------------- #
# Paths
# --------------------------------------------------------------------------- #

def setup_dir(project_path: str | Path) -> Path:
    return Path(str(project_path)) / base.PROFILE_DIR


def _user_slug(username: str) -> str:
    name = str(username or "").strip().lower()
    stem = re.sub(r"[^a-z0-9._-]+", "_", name)[:40].strip("._") or "user"
    return f"{stem}-{hashlib.sha1(name.encode('utf-8')).hexdigest()[:8]}"


def project_file(project_path: str | Path) -> Path:
    return setup_dir(project_path) / PROJECT_FILE


def user_file(project_path: str | Path, username: str) -> Path:
    return setup_dir(project_path) / USERS_DIR / f"{_user_slug(username)}.json"


def resource_key(layer: str, owner: str, rid: str) -> str:
    return f"project/{rid}" if layer == "project" else f"u:{owner}/{rid}"


# --------------------------------------------------------------------------- #
# Normalisation
# --------------------------------------------------------------------------- #

def _text(value: Any, limit: int = 500) -> str:
    return str(value if value is not None else "").strip()[:limit]


def normalize_location(item: Mapping, *, errors: list[str], where: str = "locations",
                       actor: str = "") -> dict | None:
    if not isinstance(item, Mapping):
        errors.append(f"{where} must be an object")
        return None
    host_id = clean_host_id(item.get("host_id"))
    path = _text(item.get("path_or_ref") or item.get("path"), 500)
    resource = _text(item.get("resource"), 200)
    if not host_id:
        errors.append(f"{where}.host_id must be letters, digits, '.', '_' or '-'")
    if not path:
        errors.append(f"{where}.path_or_ref is required")
    if not resource:
        errors.append(f"{where}.resource is required")
    if not (host_id and path and resource):
        return None
    return {
        "resource": resource,
        "host_id": host_id,
        "host_label": _text(item.get("host_label"), 60) or default_host_label(host_id),
        "path_or_ref": path,
        "verified_at": _text(item.get("verified_at"), 40),
        "verified_by": _text(item.get("verified_by"), 120),
        "added_by": _text(item.get("added_by"), 120) or actor,
        "added_at": _text(item.get("added_at"), 40),
    }


_META_KEYS = ("name", "purpose", "owner", "visibility", "audience", "share", "locations",
              "path_or_ref", "host_id", "host_label")


def item_from_entry(kind: str, entry: Mapping) -> dict:
    """Convert a flat entry, preserving its kind-specific details."""
    details = {k: v for k, v in entry.items() if k not in _META_KEYS and k != "id"}
    if isinstance(details.get("access"), (list, tuple)):
        details.pop("access")
    item: dict[str, Any] = {"kind": kind, "id": entry.get("id"), "details": details}
    for key in ("name", "purpose", "owner", "path_or_ref"):
        if entry.get(key) not in (None, ""):
            item[key] = entry[key]
    return item


def normalize_resource(item: Mapping, index: int, *, layer: str, owner: str,
                       errors: list[str], warnings: list[str]) -> tuple[dict, str]:
    """Return ``(resource, location_path)``; ``location_path`` comes from 1.0 fields."""
    kind = _text(item.get("kind") or item.get("type"), 40).lower().replace("-", "_")
    if kind in base.KIND_FIELDS.values():  # e.g. "repositories"
        kind = next(k for k, v in base.KIND_FIELDS.items() if v == kind)
    if kind not in KINDS:
        errors.append(f"resources[{index}].kind: '{kind}' is not one of {', '.join(KINDS)}")
        kind = "storage"
    if not isinstance(item.get("details"), Mapping):
        flat = {k: v for k, v in item.items() if k not in ("kind", "type")}
        if "subkind" in flat:
            flat["kind"] = flat.pop("subkind")
        item = item_from_entry(kind, flat)
    details_in = dict(item["details"])
    rid = base._ident(item.get("id") or details_in.get("id"), f"{kind.replace('_', '-')}-{index + 1}",
                      f"resources[{index}]", errors)
    loc_field = LOCATION_FIELD.get(kind)
    location = _text(item.get("path_or_ref") or details_in.pop("path_or_ref", "")
                     or (details_in.get(loc_field) if loc_field else ""), 500)
    probe = dict(details_in, id=rid)
    if kind == "repository":
        probe["path"] = probe.get("path") or "~"  # the path lives in locations
    entry, e, w = base.normalize_entry(kind, probe, index)
    errors.extend(f"resources.{rid}: {msg}" for msg in e)
    warnings.extend(f"resources.{rid}: {msg}" for msg in w)
    entry.pop("id", None)
    if loc_field:
        entry.pop(loc_field, None)
    resource_owner = owner if layer == "user" else (_text(item.get("owner"), 64) or owner)
    return ({
        "id": rid,
        "kind": kind,
        "name": _text(item.get("name"), 120) or rid,
        "purpose": _text(item.get("purpose") or details_in.get("purpose")
                         or details_in.get("note"), 300),
        "owner": resource_owner,
        "details": entry,
    }, location)


def _legacy_resources(raw: Mapping) -> list[dict]:
    """1.0 flat lists (``repositories``, ``secrets``...) as resource items."""
    out = []
    for kind, field_name in base.KIND_FIELDS.items():
        for entry in base._list(raw.get(field_name)):
            if isinstance(entry, Mapping):
                out.append(item_from_entry(kind, entry))
    return out


def normalize_layer(raw: Mapping[str, Any] | None, *, layer: str, owner: str,
                    project_policies: Mapping[str, str] | None = None
                    ) -> tuple[dict, list[tuple[str, str]], list[str], list[str]]:
    """Return ``(profile, [(resource_id, location_path)], errors, warnings)``."""
    raw = dict(raw or {})
    errors: list[str] = []
    warnings: list[str] = []
    items = [item for item in base._list(raw.get("resources")) if isinstance(item, Mapping)]
    items += _legacy_resources(raw)
    resources, seen, locs = [], set(), []
    if len(items) > MAX_RESOURCES:
        raise SetupError("too many setup resources; remove unused entries first")
    for index, item in enumerate(items):
        resource, location = normalize_resource(item, index, layer=layer, owner=owner,
                                                errors=errors, warnings=warnings)
        if resource["id"] in seen:
            errors.append(f"resources: duplicate id '{resource['id']}'")
            continue
        seen.add(resource["id"])
        resources.append(resource)
        if location:
            locs.append((resource["id"], location))
    if layer == "project":
        flat = {key: raw.get(key) for key in PROJECT_FIELDS if key in raw}
        # Stored profiles list every policy; keys still at their default stay
        # "defaulted" so re-normalising is idempotent (and the digest stable).
        if isinstance(flat.get("policies"), Mapping):
            policies_in = dict(flat["policies"])
            for key in raw.get("policies_defaulted") or []:
                if policies_in.get(key) == base.POLICY_DEFAULTS.get(key):
                    policies_in.pop(key, None)
            flat["policies"] = policies_in
        normalized, e, w = base.normalize_profile(flat)
        errors.extend(e)
        warnings.extend(w)
        profile = {key: normalized[key] for key in PROJECT_FIELDS if key in normalized}
        profile["policies_defaulted"] = normalized["policies_defaulted"]
    else:
        ignored = [key for key in PROJECT_FIELDS if key not in USER_FIELDS and raw.get(key)]
        for key in ignored:
            warnings.append(f"{key}: project-level field ignored in a user layer; "
                            "draft it with layer=project")
        policies = {}
        raw_policies = raw.get("policies") if isinstance(raw.get("policies"), Mapping) else {}
        ceiling = dict(project_policies or base.POLICY_DEFAULTS)
        for key, value in raw_policies.items():
            if key not in base.POLICY_DEFAULTS:
                warnings.append(f"policies.{key} is not a known policy and is ignored")
                continue
            value = _text(value, 10).lower()
            if value not in POLICY_RANK:
                errors.append(f"policies.{key}: '{value}' is not one of allow, ask, deny")
                continue
            if POLICY_RANK[value] < POLICY_RANK.get(ceiling.get(key, "ask"), 1):
                errors.append(f"policies.{key}: a user layer may only tighten the project "
                              f"policy (project: {ceiling.get(key)})")
                continue
            policies[key] = value
        notes = _text(raw.get("notes"), 4000)
        unknowns = base.normalize_profile({"unknowns": raw.get("unknowns")})[0]["unknowns"]
        profile = {"policies": policies, "notes": notes, "unknowns": unknowns}
    # Cross references inside one layer (other layers are checked per viewer).
    ids_by_kind: dict[str, set] = {}
    for item in resources:
        ids_by_kind.setdefault(item["kind"], set()).add(item["id"])
    primaries = [r for r in resources if r["kind"] == "repository"
                 and r["details"].get("role") == "primary"]
    for item in primaries:
        if item["details"].get("access") == "read-only":
            warnings.append(f"resources.{item['id']}: this primary repository is read-only")
    profile["resources"] = resources
    for hit in base.find_secret_values(raw):
        errors.append(f"{hit}: looks like a secret value; store a reference (env:NAME) instead")
    return profile, locs, list(dict.fromkeys(errors)), list(dict.fromkeys(warnings))


def layer_digest(profile: Mapping[str, Any]) -> str:
    return base.digest(profile)


# --------------------------------------------------------------------------- #
# Records
# --------------------------------------------------------------------------- #

def _empty(layer: str, owner: str = "") -> dict:
    profile = normalize_layer({}, layer=layer, owner=owner)[0]
    return {"format": LAYER_FORMAT, "setup_version": LAYER_VERSION, "layer": layer,
            "owner": owner, "state": "unset", "profile": profile, "digest": "",
            "confirmation": None, "verification": None, "locations": [], "hosts": [],
            "updated_at": "", "updated_by": ""}


def _finish(record: dict, layer: str, owner: str,
            project_policies: Mapping[str, str] | None = None) -> dict:
    profile, _, errors, _ = normalize_layer(record.get("profile"), layer=layer, owner=owner,
                                            project_policies=project_policies)
    record["profile"] = profile
    record["layer"], record["owner"] = layer, owner
    state = record.get("state") if record.get("state") in base.STATES else "draft"
    current = layer_digest(profile)
    if state in base.READY_STATES:
        if (record.get("confirmation") or {}).get("digest") != current:
            state, record["stale_reason"] = "stale", "profile changed after confirmation"
    record["state"] = state
    record["digest"] = current
    record["valid"] = not errors
    locs: list[dict] = []
    for item in base._list(record.get("locations"))[:MAX_LOCATIONS]:
        loc = normalize_location(item, errors=[]) if isinstance(item, Mapping) else None
        if loc:
            locs.append(loc)
    record["locations"] = locs
    hosts = []
    for item in base._list(record.get("hosts"))[:50]:
        if isinstance(item, Mapping) and clean_host_id(item.get("id")):
            hosts.append({"id": clean_host_id(item.get("id")),
                          "label": _text(item.get("label"), 60)
                          or default_host_label(item.get("id"))})
    record["hosts"] = hosts
    return record


def _read_json(path: Path) -> dict | None:
    if not path.is_file():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        return {"state": "stale", "stale_reason": f"{path.name} is unreadable"}


def load_project(project_path: str | Path, *, default_owner: str = "local",
                 known_user: Any = None) -> dict:
    migrate(project_path, default_owner=default_owner, known_user=known_user)
    data = _read_json(project_file(project_path))
    if data is None:
        return _empty("project")
    return _finish(data, "project", "")


def load_user(project_path: str | Path, username: str,
              project_policies: Mapping[str, str] | None = None) -> dict:
    if not username:
        return _empty("user", "")
    data = _read_json(user_file(project_path, username))
    if data is None:
        return _empty("user", username)
    owner = str(data.get("owner") or username)
    if owner.lower() != username.lower():  # a file that claims someone else
        return _empty("user", username)
    return _finish(data, "user", owner, project_policies)


def all_user_layers(project_path: str | Path) -> list[dict]:
    folder = setup_dir(project_path) / USERS_DIR
    out = []
    if not folder.is_dir():
        return out
    for path in sorted(folder.glob("*.json")):
        data = _read_json(path) or {}
        owner = str(data.get("owner") or "")
        if owner and path.name == f"{_user_slug(owner)}.json":
            out.append(_finish(data, "user", owner))
    return out


def read_layers(project_path: str | Path) -> dict:
    """Project and user layers as stored, without migrating or writing."""
    data = _read_json(project_file(project_path))
    project = _finish(data, "project", "") if data is not None else _empty("project")
    return {"project": project, "users": all_user_layers(project_path)}


def recent_history(project_path: str | Path, limit: int = 2000) -> list[dict]:
    """The newest ``limit`` setup history events (read-only)."""
    path = setup_dir(project_path) / base.HISTORY_FILE
    if not path.is_file():
        return []
    try:
        lines = path.read_text(encoding="utf-8").splitlines()[-limit:]
    except OSError:
        return []
    out = []
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict):
            out.append(event)
    return out


def _write(project_path: str | Path, target: Path, record: dict, event: dict) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    keep = {k: v for k, v in record.items() if k not in {"valid", "stale_reason"}}
    tmp = target.with_suffix(".tmp")
    tmp.write_text(json.dumps(keep, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    os.replace(tmp, target)
    history = setup_dir(project_path) / base.HISTORY_FILE
    with history.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(event, ensure_ascii=False) + "\n")


def _layer_target(project_path: str | Path, layer: str, owner: str) -> Path:
    return project_file(project_path) if layer == "project" else user_file(project_path, owner)


def _load_layer(project_path: str | Path, layer: str, owner: str) -> dict:
    if layer == "project":
        return load_project(project_path)
    project = load_project(project_path)
    return load_user(project_path, owner, project["profile"].get("policies"))


def _layer_name(layer: str, owner: str) -> str:
    return "project" if layer == "project" else f"user:{owner}"


# --------------------------------------------------------------------------- #
# Migration 1.0 -> 2.0
# --------------------------------------------------------------------------- #

@_serialized
def migrate(project_path: str | Path, *, default_owner: str = "local",
            known_user: Any = None) -> dict | None:
    """Split a 1.0 ``environment.json`` into the project layer and one user layer.

    Idempotent: does nothing once ``project.json`` exists.  ``known_user`` is an
    optional ``callable(name) -> canonical name or None`` used to pick the
    confirming user from ``confirmation.by``.
    """
    folder = setup_dir(project_path)
    legacy = folder / LEGACY_FILE
    if project_file(project_path).exists() or not legacy.is_file():
        return None
    try:
        old = json.loads(legacy.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        old = {}
    if not isinstance(old, dict):
        old = {}
    old_profile, _, _ = base.normalize_profile(old.get("profile"))
    confirmation = old.get("confirmation") or {}
    carried = (old.get("state") in base.READY_STATES
               and confirmation.get("digest") == base.digest(old_profile))
    owner = ""
    for candidate in (confirmation.get("by"), old.get("updated_by")):
        text = str(candidate or "")
        if text.startswith("human:"):
            name = text.split(":", 1)[1]
            resolved = known_user(name) if callable(known_user) else None
            if resolved:
                owner = str(resolved)
                break
    owner = owner or default_owner or "local"
    machine = clean_host_id(old.get("machine")) or base.machine_fingerprint()
    label = default_host_label(machine)
    now = protocol.utc_now()
    project_items, locations = [], []
    for kind, field_name in base.KIND_FIELDS.items():
        for entry in old_profile.get(field_name) or []:
            item = item_from_entry(kind, entry)
            item.update(owner=owner)
            project_items.append(item)
            loc_field = LOCATION_FIELD.get(kind)
            path = entry.get(loc_field) if loc_field else ("local" if kind == "compute"
                                                           and entry.get("kind") == "local"
                                                           else "")
            if path:
                locations.append({"resource": resource_key("project", "", entry["id"]),
                                  "host_id": machine, "host_label": label,
                                  "path_or_ref": path, "added_by": "migration",
                                  "added_at": now})
    project_raw = {key: old_profile.get(key) for key in PROJECT_FIELDS}
    # Keep only explicitly set policies; defaults are re-derived (and listed).
    project_raw["policies"] = {k: v for k, v in (old_profile.get("policies") or {}).items()
                               if k not in (old_profile.get("policies_defaulted") or [])}
    project_raw["resources"] = project_items
    p_profile, _, p_errors, _ = normalize_layer(project_raw, layer="project", owner=owner)
    note = {"from": LEGACY_FILE, "at": now, "old_digest": base.digest(old_profile),
            "old_state": old.get("state") or "", "carried_confirmation": carried}

    def record(layer: str, layer_owner: str, profile: dict, locs: list) -> dict:
        digest_value = layer_digest(profile)
        if carried:
            state = old.get("state") if old.get("state") in base.READY_STATES else "confirmed"
            conf = {"by": confirmation.get("by"), "at": confirmation.get("at"),
                    "digest": digest_value, "note": confirmation.get("note") or "",
                    "migrated_from_digest": confirmation.get("digest")}
        else:
            state, conf = ("proposed" if old.get("state") not in (None, "", "unset") else "draft"), None
        out = {"format": LAYER_FORMAT, "setup_version": LAYER_VERSION, "layer": layer,
               "owner": layer_owner, "state": state, "profile": profile,
               "digest": digest_value, "confirmation": conf,
               "verification": old.get("verification") if carried else None,
               "locations": locs, "hosts": [{"id": machine, "label": label}] if layer == "user" else [],
               "updated_at": now, "updated_by": "migration", "migration": note}
        if not carried and state == "proposed":
            out["needs_reconfirmation"] = True
        return out

    project_record = record("project", "", p_profile, locations)
    if p_errors and project_record["state"] == "proposed":
        project_record["state"] = "draft"
    _write(project_path, project_file(project_path), project_record,
           {"at": now, "actor": "migration", "layer": "project", "event": "migrated",
            "state": project_record["state"], "digest": project_record["digest"]})
    backup = folder / LEGACY_BACKUP
    if backup.exists():
        backup = folder / f"{LEGACY_BACKUP}.{now.replace(':', '').replace('-', '')[:15]}"
    os.replace(legacy, backup)
    return {"owner": owner, "carried_confirmation": carried, "backup": backup.name}


# --------------------------------------------------------------------------- #
# Writes
# --------------------------------------------------------------------------- #

def _check_layer_writer(viewer: Viewer, layer: str, owner: str) -> None:
    if not viewer.can_write:
        raise SetupError("this action needs write access to the project", 403,
                         "SETUP_FORBIDDEN")
    if layer == "project":
        return
    if viewer.legacy or not viewer.user:
        raise SetupError("the server token has no user layer; use a personal agent token "
                         "or layer=project", 403, "SETUP_NO_USER")
    if not viewer.is_user(owner):
        raise SetupError("you can only change your own setup layer", 403, "SETUP_NOT_OWNER")


def _merge_locations(existing: list[dict], new: list[dict], keep_keys: set[str] | None,
                     layer_prefix: str) -> list[dict]:
    """Keep distinct paths and contributors, even on the same machine."""
    replaced = {(loc["resource"], loc["host_id"], loc["path_or_ref"], loc.get("added_by")) for loc in new}
    out = []
    for loc in existing:
        if (loc["resource"], loc["host_id"], loc["path_or_ref"], loc.get("added_by")) in replaced:
            continue
        if keep_keys is not None and loc["resource"].startswith(layer_prefix) \
                and loc["resource"] not in keep_keys:
            continue
        out.append(loc)
    if len(out) + len(new) > MAX_LOCATIONS:
        raise SetupError("too many setup locations; remove unused entries first")
    return out + new


@_serialized
def save_layer(project_path: str | Path, raw: Mapping[str, Any] | None, *, viewer: Viewer,
               layer: str, actor: str, propose: bool = False, host_label: str = "",
               merge_resources: bool = True) -> dict:
    """Draft (or propose) a layer.  Any content edit clears its confirmation."""
    owner = "" if layer == "project" else viewer.user
    _check_layer_writer(viewer, layer, owner)
    raw = dict(raw or {})
    previous = _load_layer(project_path, layer, owner)
    if merge_resources:
        merged = {r["id"]: r for r in previous["profile"].get("resources") or []}
        fields = {k: v for k, v in previous["profile"].items() if k != "resources"}
        for item in base._list(raw.get("resources")) + _legacy_resources(raw):
            if isinstance(item, Mapping):
                merged[_text(item.get("id"), 48).lower() or f"r{len(merged) + 1}"] = item
        for rid in base._list(raw.get("delete")):
            merged.pop(_text(rid, 48).lower(), None)
        fields.update({k: v for k, v in raw.items()
                       if k not in {"resources", "delete", *base.KIND_FIELDS.values()}})
        raw = dict(fields, resources=list(merged.values()))
    project_policies = None
    if layer == "user":
        project_policies = load_project(project_path)["profile"].get("policies")
    profile, locs, errors, warnings = normalize_layer(
        raw, layer=layer, owner=owner or viewer.user, project_policies=project_policies)
    secret_errors = [e for e in errors if "looks like a secret value" in e]
    if secret_errors:
        raise SetupError("refusing to store secret values: " + "; ".join(secret_errors),
                         400, "SETUP_SECRET")
    now = protocol.utc_now()
    label = _text(host_label, 60) or host_label_for(previous, viewer.host_id)
    new_locs = [{"resource": resource_key(layer, owner, rid), "host_id": viewer.host_id,
                 "host_label": label, "path_or_ref": path, "verified_at": "", "verified_by": "",
                 "added_by": viewer.user or actor, "added_at": now} for rid, path in locs]
    for item in base._list(raw.get("locations")):
        loc = normalize_location(item, errors=errors, actor=viewer.user or actor)
        if loc:
            new_locs.append(loc)
    prefix = resource_key(layer, owner, "")
    keep = {resource_key(layer, owner, r["id"]) for r in profile["resources"]}
    locations = _merge_locations(previous.get("locations") or [], new_locs, keep, prefix)
    hosts = previous.get("hosts") or []
    if layer == "user":
        hosts = _upsert_host(hosts, viewer.host_id, label)
        for item in base._list(raw.get("hosts")):
            if isinstance(item, Mapping) and clean_host_id(item.get("id")):
                hosts = _upsert_host(hosts, clean_host_id(item["id"]),
                                     _text(item.get("label"), 60))
    unchanged = layer_digest(profile) == previous.get("digest") and \
        previous["state"] in base.READY_STATES
    state = previous["state"] if unchanged else ("proposed" if propose and not errors else "draft")
    record = {"format": LAYER_FORMAT, "setup_version": LAYER_VERSION, "layer": layer,
              "owner": owner, "state": state, "profile": profile,
              "digest": layer_digest(profile),
              "confirmation": previous.get("confirmation") if unchanged else None,
              "verification": previous.get("verification") if unchanged else None,
              "locations": locations, "hosts": hosts,
              "updated_at": now, "updated_by": actor}
    _write(project_path, _layer_target(project_path, layer, owner), record,
           {"at": now, "actor": actor, "layer": _layer_name(layer, owner), "event": state,
            "digest": record["digest"], "previous_state": previous["state"]})
    return {"ok": not errors, "errors": errors, "warnings": warnings, "layer": layer,
            "state": state, "digest": record["digest"]}


def _upsert_host(hosts: list[dict], host_id: str, label: str) -> list[dict]:
    out = [dict(h) for h in hosts]
    for item in out:
        if item["id"] == host_id:
            if label:
                item["label"] = label
            return out
    out.append({"id": host_id, "label": label or default_host_label(host_id)})
    return out[-50:]


def host_label_for(record: Mapping, host_id: str) -> str:
    for item in record.get("hosts") or []:
        if item.get("id") == host_id:
            return item.get("label") or default_host_label(host_id)
    return default_host_label(host_id)


@_serialized
def set_host(project_path: str | Path, *, viewer: Viewer, label: str, actor: str,
             host_id: str = "") -> dict:
    """Name a host in the caller's layer (no state change)."""
    _check_layer_writer(viewer, "user", viewer.user)
    host = clean_host_id(host_id) or viewer.host_id
    record = _load_layer(project_path, "user", viewer.user)
    if record["state"] == "unset":
        record.update(state="draft", updated_at=protocol.utc_now(), updated_by=actor)
    record["hosts"] = _upsert_host(record.get("hosts") or [], host, _text(label, 60))
    for loc in record.get("locations") or []:
        if loc["host_id"] == host and label:
            loc["host_label"] = _text(label, 60)
    _write(project_path, user_file(project_path, viewer.user), record,
           {"at": protocol.utc_now(), "actor": actor, "layer": f"user:{viewer.user}",
            "event": "host", "host": host})
    return {"ok": True, "host": {"id": host, "label": host_label_for(record, host)}}


@_serialized
def add_location(project_path: str | Path, *, viewer: Viewer, key: str, path_or_ref: str,
                 actor: str, host_id: str = "", host_label: str = "",
                 scope: str = "") -> dict:
    """Register where an accessible resource lives on a host (no state change)."""
    _check_layer_writer(viewer, "project" if viewer.legacy else "user", viewer.user)
    view = resolve(project_path, viewer)
    visible = {item["key"]: item for item in view["_full"]}
    if key not in visible:
        # Same answer for "does not exist" and "not yours to see".
        raise SetupError("resource not found or not accessible to you", 404,
                         "SETUP_RESOURCE_NOT_FOUND")
    errors: list[str] = []
    host = clean_host_id(host_id) or viewer.host_id
    project_scope = scope == "project" or viewer.legacy
    if project_scope and not (viewer.admin or viewer.legacy):
        raise SetupError("only a project admin can add project-wide locations", 403,
                         "SETUP_ADMIN_LOCATION")
    layer = "project" if project_scope else "user"
    record = _load_layer(project_path, layer, "" if project_scope else viewer.user)
    label = _text(host_label, 60) or host_label_for(record, host)
    loc = normalize_location({"resource": key, "host_id": host, "host_label": label,
                              "path_or_ref": path_or_ref, "added_at": protocol.utc_now()},
                             errors=errors, actor=viewer.user or actor)
    if errors or not loc:
        raise SetupError("; ".join(errors) or "invalid location", 400, "SETUP_INVALID")
    if base.find_secret_values({"path_or_ref": loc["path_or_ref"]}):
        raise SetupError("refusing to store secret values: path_or_ref looks like a secret",
                         400, "SETUP_SECRET")
    record["locations"] = _merge_locations(record.get("locations") or [], [loc], None, "")
    if layer == "user":
        record["hosts"] = _upsert_host(record.get("hosts") or [], host, _text(host_label, 60))
        if record["state"] == "unset":
            record["state"] = "draft"
    record["updated_at"], record["updated_by"] = protocol.utc_now(), actor
    target = _layer_target(project_path, layer, viewer.user)
    _write(project_path, target, record,
           {"at": record["updated_at"], "actor": actor,
            "layer": _layer_name(layer, viewer.user), "event": "location",
            "resource": key, "host": host})
    return {"ok": True, "location": loc, "scope": layer}


@_serialized
def propose(project_path: str | Path, *, viewer: Viewer, layer: str, actor: str) -> dict:
    owner = "" if layer == "project" else viewer.user
    _check_layer_writer(viewer, layer, owner)
    record = _load_layer(project_path, layer, owner)
    if record["state"] == "unset":
        raise SetupError("there is no profile to confirm", 409, "SETUP_UNSET")
    if not record.get("valid", True):
        _, _, errors, _ = normalize_layer(record["profile"], layer=layer, owner=owner)
        raise SetupError("the profile has validation errors: " + "; ".join(errors[:5]),
                         409, "SETUP_INVALID")
    if record["state"] in ("draft", "stale"):
        record["state"] = "proposed"
        record["confirmation"] = None
        record["updated_at"], record["updated_by"] = protocol.utc_now(), actor
        _write(project_path, _layer_target(project_path, layer, owner), record,
               {"at": record["updated_at"], "actor": actor, "layer": _layer_name(layer, owner),
                "event": "proposed", "digest": record["digest"]})
    return {"ok": True, "layer": layer, "state": record["state"], "digest": record["digest"]}


@_serialized
def confirm_layer(project_path: str | Path, *, viewer: Viewer, layer: str, actor: str,
                  digest_value: str, note: str = "") -> dict:
    """Human-only.  Project layer: admins.  User layer: that user only."""
    _check_layer_writer(viewer, layer, viewer.user)
    if not base.is_human_actor(actor) or viewer.legacy:
        raise SetupError("only a human can confirm the environment profile; agents may "
                         "draft and propose", 403, "SETUP_HUMAN_REQUIRED")
    if layer == "project":
        if not viewer.admin:
            raise SetupError("only a project admin can confirm the project layer", 403,
                             "SETUP_ADMIN_REQUIRED")
        owner = ""
    else:
        owner = viewer.user
        if not owner:
            raise SetupError("you can only change your own setup layer", 403, "SETUP_NOT_OWNER")
    record = _load_layer(project_path, layer, owner)
    if record["state"] == "unset":
        raise SetupError("there is no profile to confirm", 409, "SETUP_UNSET")
    if not record.get("valid", True):
        _, _, errors, _ = normalize_layer(record["profile"], layer=layer, owner=owner)
        raise SetupError("the profile has validation errors: " + "; ".join(errors[:5]),
                         409, "SETUP_INVALID")
    if str(digest_value or "") != record["digest"]:
        raise SetupError("digest mismatch: the profile changed since it was reviewed",
                         409, "SETUP_DIGEST")
    now = protocol.utc_now()
    record.update({"state": "confirmed",
                   "confirmation": {"by": actor, "at": now, "digest": record["digest"],
                                    "note": _text(note, 1000)},
                   "verification": None})
    record.pop("needs_reconfirmation", None)
    _write(project_path, _layer_target(project_path, layer, owner), record,
           {"at": now, "actor": actor, "layer": _layer_name(layer, owner),
            "event": "confirmed", "digest": record["digest"]})
    return {"ok": True, "layer": layer, "state": "confirmed", "digest": record["digest"]}


# --------------------------------------------------------------------------- #
# Resolution
# --------------------------------------------------------------------------- #

def needs_location(resource: Mapping) -> bool:
    kind = resource.get("kind")
    if kind in LOCATION_FIELD:
        return True
    return kind == "compute" and (resource.get("details") or {}).get("kind") == "local"


def _meta(resource: Mapping, key: str, layer: str, reason: str) -> dict:
    return {"key": key, "id": resource["id"], "kind": resource["kind"], "name": resource["name"],
            "purpose": resource.get("purpose") or "", "owner": resource.get("owner") or "",
            "layer": layer, "reason": reason,
            "ask": resource.get("owner") or ""}


def resolve(project_path: str | Path, viewer: Viewer, *, default_owner: str = "local",
            known_user: Any = None) -> dict:
    """Everything ``viewer`` may know, for its current host.

    Keys starting with ``_`` are internal (never sent as-is by the server).
    """
    project = load_project(project_path, default_owner=default_owner, known_user=known_user)
    policies = project["profile"].get("policies") or dict(base.POLICY_DEFAULTS)
    layers = all_user_layers(project_path)
    mine = next((rec for rec in layers if viewer.is_user(rec["owner"])), None)
    if mine is None:
        mine = _empty("user", viewer.user)
    else:
        mine = load_user(project_path, viewer.user, policies)
    host_label = host_label_for(mine, viewer.host_id)

    full, accessible, unavailable = [], [], []
    sources = [("project", project)] + [("user", rec) for rec in layers]
    pools = [record.get("locations") or [] for _, record in sources]
    for layer, record in sources:
        for resource in record["profile"].get("resources") or []:
            key = resource_key(layer, record["owner"], resource["id"])
            locations = []
            for pool in pools:
                for loc in pool:
                    if loc["resource"] == key:
                        locations.append(dict(loc))
            locations.sort(key=lambda loc: (loc.get("added_at", ""),
                                            viewer.is_user(loc.get("added_by", ""))))
            here = next((loc for loc in reversed(locations)
                         if loc["host_id"] == viewer.host_id), None)
            if here is None:
                here = next((loc for loc in locations
                             if not base.is_local_path(loc["path_or_ref"])
                             and loc["path_or_ref"] != "local"), None)
            usable = here is not None or not needs_location(resource)
            item = dict(_meta(resource, key, layer, "registered"), details=resource["details"],
                        locations=locations,
                        location=here, usable_here=usable,
                        access_verified=bool(here and here.get("verified_at")),
                        updated_at=record.get("updated_at", ""),
                        layer_state=record["state"])
            full.append(item)
            if usable:
                accessible.append(item)
            else:
                unavailable.append(dict(item, reason="no_location_on_host"))
    user_policies = (mine["profile"].get("policies") or {}) if viewer.user else {}
    effective = {key: max((policies.get(key, "ask"), user_policies.get(key, "allow")),
                          key=lambda v: POLICY_RANK.get(v, 1)) for key in base.POLICY_DEFAULTS}
    return {"project": project, "mine": mine if viewer.user else None,
            "host": {"id": viewer.host_id, "label": host_label},
            "accessible": accessible,
            "known_but_inaccessible": unavailable,
            "policies": {"effective": effective, "project": policies, "user": user_policies},
            "_full": full}


def flat_view(view: Mapping[str, Any], items: list[dict] | None = None) -> tuple[dict, dict]:
    """A 1.0-shaped profile from usable resources; plus ``flat id -> item``."""
    items = view["accessible"] if items is None else items
    project = view["project"]["profile"]
    raw: dict[str, Any] = {key: project.get(key) for key in PROJECT_FIELDS}
    raw["policies"] = dict(view["policies"]["effective"])
    counts: dict[str, int] = {}
    for item in items:
        counts[item["id"]] = counts.get(item["id"], 0) + 1
    mapping: dict[str, dict] = {}
    for item in items:
        flat_id = item["id"]
        if counts[flat_id] > 1:
            flat_id = re.sub(r"[^a-z0-9._-]+", "-", f"{item['owner'] or 'project'}-{item['id']}"
                             .lower())[:48]
        entry = dict(item["details"], id=flat_id)
        field_name = LOCATION_FIELD.get(item["kind"])
        if field_name:
            entry[field_name] = (item.get("location") or {}).get("path_or_ref", "")
        raw.setdefault(base.KIND_FIELDS[item["kind"]], [])
        raw[base.KIND_FIELDS[item["kind"]]] = (raw.get(base.KIND_FIELDS[item["kind"]]) or []) + [entry]
        mapping[flat_id] = item
    profile, _, _ = base.normalize_profile(raw)
    profile["policies_defaulted"] = project.get("policies_defaulted") or []
    return profile, mapping


def viewer_readiness(view: Mapping[str, Any], flat: Mapping[str, Any]) -> dict:
    project_state = view["project"]["state"]
    mine = view.get("mine")
    user_state = (mine or {}).get("state") or "unset"
    ready = base.readiness(flat, project_state)
    blocked = list(ready["blocked"])
    if mine is not None and user_state in ("draft", "proposed", "stale"):
        blocked.insert(0, {"capability": "tasks", "reason": f"your setup layer is '{user_state}'; "
                           "confirm it in Settings -> Setup before task work starts"})
        ready["ready"] = False
    missing = [item for item in view["known_but_inaccessible"]
               if item["reason"] == "no_location_on_host"]
    for entry in blocked:
        if entry["capability"] == "implement" and entry["reason"] == "no primary repository registered":
            repo = next((m for m in missing if m["kind"] == "repository"), None)
            if repo:
                entry["reason"] = f"repository '{repo['id']}' has no location on this host"
    seen: set[str] = set()
    blocked = [b for b in blocked if not (b["capability"] in seen or seen.add(b["capability"]))]
    ready["blocked"] = blocked
    blocked_ids = {b["capability"] for b in blocked}
    ready["allowed"] = [cap for cap in ("plan", "tasks", "implement", "gates", "gpu")
                        if cap not in blocked_ids]
    ready.update({
        "project_state": project_state,
        "user_state": user_state,
        "host": dict(view["host"]),
        "needs_location": [item["key"] for item in missing
                           if item["layer"] == "project" or item["owner"] == (mine or {}).get("owner")],
        "inaccessible": len([i for i in view["known_but_inaccessible"]
                             if i["reason"] != "no_location_on_host"]),
        "accessible": len(view["accessible"]),
    })
    return ready


def check_task(view: Mapping[str, Any], task: Mapping[str, Any]) -> list[str]:
    """Per-caller warnings for a task manifest."""
    flat, _ = flat_view(view)
    state = view["project"]["state"]
    warnings = base.check_task(flat, state, task)
    if state not in base.READY_STATES:
        return warnings
    mine = view.get("mine")
    if mine and mine.get("state") in ("draft", "proposed", "stale"):
        warnings.insert(0, f"your setup layer is '{mine['state']}'; confirm it in "
                           "Settings -> Setup before task work starts")
    repository = _text(task.get("repository"), 500)
    if repository and any("is not registered" in w for w in warnings):
        def names(item: Mapping) -> set:
            return {item["id"], item["name"], item["key"]} | {
                Path(loc["path_or_ref"]).name for loc in item.get("locations") or []}
        for item in view["known_but_inaccessible"]:
            if item["kind"] != "repository" or repository not in names(item):
                continue
            warnings = [w for w in warnings if "is not registered" not in w]
            if item["reason"] == "no_location_on_host":
                warnings.append(f"repository '{repository}' has no location on this host; "
                                "register one with POST /api/agent/v1/setup/location")
            break
    return warnings


def public_item(item: Mapping[str, Any]) -> dict:
    return {k: v for k, v in item.items() if not k.startswith("_")}


def own_layer_public(record: Mapping[str, Any] | None) -> dict | None:
    if record is None:
        return None
    return {k: record.get(k) for k in ("layer", "owner", "state", "digest", "confirmation",
                                        "verification", "updated_at", "updated_by",
                                        "needs_reconfirmation", "stale_reason", "valid")} | {
        "profile": record["profile"], "locations": record.get("locations") or [],
        "hosts": record.get("hosts") or []}


def project_layer_public(view: Mapping[str, Any], viewer: Viewer) -> dict:
    """Project facts and resources, visible to every project member."""
    record = view["project"]
    profile = dict(record["profile"])
    out = {k: record.get(k) for k in ("layer", "state", "digest", "confirmation", "verification",
                                      "updated_at", "updated_by", "needs_reconfirmation",
                                      "stale_reason", "valid", "migration")}
    out["profile"] = profile
    out["locations"] = record.get("locations") or []
    return out


# --------------------------------------------------------------------------- #
# Verify
# --------------------------------------------------------------------------- #

@_serialized
def verify_view(project_path: str | Path, viewer: Viewer, *, actor: str,
                gpu_servers: Mapping[str, Any] | None = None,
                environ: Mapping[str, str] | None = None) -> dict:
    """Read-only checks of the caller's usable resources on this host."""
    view = resolve(project_path, viewer)
    flat, mapping = flat_view(view)
    if viewer.host_id != base.machine_fingerprint():
        result = {"ok": False, "checks": [{
            "id": "host", "status": "skip", "mandatory": False,
            "detail": f"locations on host '{view['host']['label']}' cannot be checked from "
                      "the ARK server; verify them on that host"}],
            "machine": viewer.host_id, "verified_at": protocol.utc_now()}
    else:
        result = base.verify(flat, gpu_servers=gpu_servers, environ=environ)
    passed = {c["id"].split(":", 1)[1] for c in result["checks"]
              if c["status"] == "pass" and c["id"].count(":") == 1}
    stamped: dict[str, set] = {"project": set(), "user": set()}
    for flat_id, item in mapping.items():
        loc = item.get("location")
        if flat_id in passed and loc and loc["host_id"] == viewer.host_id:
            stamped["project"].add((loc["resource"], loc["path_or_ref"]))
            stamped["user"].add((loc["resource"], loc["path_or_ref"]))
    now = result.get("verified_at") or protocol.utc_now()
    targets = []
    if viewer.admin and view["project"]["state"] != "unset":
        targets.append(("project", ""))
    if viewer.user and view.get("mine") and view["mine"]["state"] != "unset":
        targets.append(("user", viewer.user))
    for layer, owner in targets:
        record = _load_layer(project_path, layer, owner)
        for loc in record.get("locations") or []:
            if (loc["resource"], loc["path_or_ref"]) in stamped[layer] \
                    and loc["host_id"] == viewer.host_id:
                loc["verified_at"], loc["verified_by"] = now, actor
        if record["state"] in base.READY_STATES and viewer.host_id == base.machine_fingerprint():
            record["state"] = "verified" if result.get("ok") else "confirmed"
            record["verification"] = {"ok": bool(result.get("ok")), "at": now, "by": actor,
                                      "host": viewer.host_id}
        _write(project_path, _layer_target(project_path, layer, owner), record,
               {"at": now, "actor": actor, "layer": _layer_name(layer, owner),
                "event": "verify", "ok": bool(result.get("ok")), "host": viewer.host_id})
    # A non-admin's verify still moves a confirmed project layer to verified
    # (as in 1.0) when every mandatory check passed here.
    if not viewer.admin and result.get("ok") and viewer.host_id == base.machine_fingerprint():
        record = load_project(project_path)
        if record["state"] == "confirmed":
            record["state"] = "verified"
            record["verification"] = {"ok": True, "at": now, "by": actor, "host": viewer.host_id}
            _write(project_path, project_file(project_path), record,
                   {"at": now, "actor": actor, "layer": "project", "event": "verify", "ok": True,
                    "host": viewer.host_id})
    return result


@_serialized
def record_project_verification(project_path: str | Path, result: Mapping[str, Any], *,
                                actor: str) -> None:
    """1.0 API: persist an already-computed verify result on the project layer."""
    record = load_project(project_path)
    if record["state"] not in base.READY_STATES:
        return
    now = protocol.utc_now()
    record["state"] = "verified" if result.get("ok") else "confirmed"
    record["verification"] = {"ok": bool(result.get("ok")), "at": now, "by": actor,
                              "host": str(result.get("machine") or base.machine_fingerprint())}
    _write(project_path, project_file(project_path), record,
           {"at": now, "actor": actor, "layer": "project", "event": "verify",
            "ok": bool(result.get("ok")), "digest": record["digest"]})


# --------------------------------------------------------------------------- #
# Rendering
# --------------------------------------------------------------------------- #

def render_resources(items: Iterable[Mapping]) -> str:
    rows = []
    for item in items:
        locs = item.get("locations")
        rows.append([item.get("key") or item.get("id"), item.get("kind"), item.get("name"),
                     item.get("owner"),
                     ", ".join(f"{loc['host_label']}: {loc['path_or_ref']}" for loc in locs)
                     if locs else "—"])
    return base._table(rows, ["key", "kind", "name", "owner",
                              "locations"])


def render_project_summary(view: Mapping[str, Any], viewer: Viewer) -> str:
    record = view["project"]
    shown = [item for item in view["_full"] if item["layer"] == "project"]
    flat, _ = flat_view(view, shown)
    flat["policies"] = dict(view["policies"]["project"])
    flat_record = dict(record, profile=flat)
    text = base.render_summary(flat_record).rstrip()
    return (text + "\n\n## All registered resources and locations\n\n"
            + render_resources(view["_full"]) + "\n")


def render_user_summary(view: Mapping[str, Any]) -> str:
    mine = view.get("mine") or {}
    owner = mine.get("owner") or ""
    key_prefix = resource_key("user", owner, "")
    items = [item for item in view["_full"] if item["key"].startswith(key_prefix)]
    parts = [f"# My setup layer: {owner}", "",
             f"- State: **{mine.get('state')}**  ·  digest `{mine.get('digest') or ''}`",
             f"- Updated: {mine.get('updated_at') or '—'} by {mine.get('updated_by') or '—'}"]
    conf = mine.get("confirmation") or {}
    if conf:
        parts.append(f"- Confirmed: {conf.get('at')} by {conf.get('by')}")
    parts += ["", "## My resources", "", render_resources(items), ""]
    policies = (mine.get("profile") or {}).get("policies") or {}
    parts += ["## My policy tightenings", "", base._table(
        [[key, value, view["policies"]["project"].get(key, "")] for key, value in policies.items()],
        ["action", "mine", "project"]), ""]
    parts += ["## My hosts", "", base._table(
        [[h["id"], h["label"]] for h in mine.get("hosts") or []], ["id", "label"]), ""]
    return "\n".join(parts).rstrip() + "\n"


# --------------------------------------------------------------------------- #
# Payload
# --------------------------------------------------------------------------- #

def payload(project_path: str | Path, viewer: Viewer, *, default_owner: str = "local",
            known_user: Any = None) -> dict:
    """The per-viewer state document returned by ``GET /api/agent/v1/setup``."""
    view = resolve(project_path, viewer, default_owner=default_owner, known_user=known_user)
    flat, _ = flat_view(view)
    ready = viewer_readiness(view, flat)
    project = view["project"]
    mine = view.get("mine")
    project_public = project_layer_public(view, viewer)
    out = {
        "format": LAYER_FORMAT,
        "setup_version": LAYER_VERSION,
        # 1.0-compatible fields (project layer + flat per-viewer view).
        "state": project["state"],
        "digest": project["digest"],
        "confirmation": project.get("confirmation"),
        "verification": project.get("verification"),
        "profile": flat,
        "readiness": ready,
        "questions": base.questions(flat),
        "summary_markdown": render_project_summary(view, viewer),
        # 2.0
        "viewer": viewer.public(view["host"]["label"]),
        "host": view["host"],
        "resources": [public_item(i) for i in view["_full"]],
        "accessible": [public_item(i) for i in view["accessible"]],
        "known_but_inaccessible": [public_item(i) for i in view["known_but_inaccessible"]],
        "policies": view["policies"],
        "layers": {
            "project": dict(
                {k: project_public.get(k) for k in ("state", "digest", "confirmation",
                                                    "updated_at", "updated_by",
                                                    "needs_reconfirmation", "valid")},
                can_edit=viewer.can_write, can_confirm=viewer.admin and not viewer.legacy),
            "user": None if mine is None else dict(
                {k: mine.get(k) for k in ("owner", "state", "digest", "confirmation",
                                          "updated_at", "updated_by", "needs_reconfirmation",
                                          "valid")},
                can_edit=viewer.can_write and bool(viewer.user) and not viewer.legacy,
                can_confirm=viewer.can_write and bool(viewer.user) and not viewer.legacy),
        },
        "project_layer": project_public,
        "mine": own_layer_public(mine),
        "user_summary_markdown": render_user_summary(view) if mine is not None else "",
    }
    return out
