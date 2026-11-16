"""ARK Studio HTTP server.

Standard library only: one process, one SQLite project at a time, loopback by
default.  The UI and the coding-agent API share the same write path, so there is
no second source of truth.
"""
from __future__ import annotations

import base64
import hashlib
import binascii
import gzip
import html
import ipaddress
import itertools
import json
import mimetypes
import os
import posixpath
import re
import select
import shutil
import socket
import sqlite3
import tempfile
import threading
import time
import uuid
import zlib
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, quote, unquote, urlparse

from . import agent_tokens as agent_tokens_mod
from . import overview as overview_mod
from . import auth as auth_mod
from . import blackbox as blackbox_mod
from . import assistant, assistant_config, chat, demo, export, gpu_probe, importer, linux_account, mobile, platform_support, providers, skills
from . import markdown_bundle
from . import session_context
from . import search as search_mod
from . import resources_graph as resources_graph_mod
from .project_archive import write_project_archive
from . import org as org_mod
from . import qr as qr_codes
from . import workspace as workspace_files
from .projects import Project, ProjectError, ProjectManager
from .sessions import SessionError, SessionManager
from .settings import PALETTE_PRESETS, SettingsError, project_settings, settings_patch
from .store import Store, StoreError
from .static_assets import StaticAssets, accepts_gzip, module_graph
from .tags import public_vocabulary
from .worktrees import WorktreeError, WorktreeManager
from . import db

WEB_ROOT = Path(__file__).resolve().parent.parent / "web"
MAX_BODY = 24 * 1024 * 1024
# An error reply first reads an unread request body up to this size (and for
# at most this long) so closing the connection does not reset it.
ERROR_DRAIN_LIMIT = 1024 * 1024
ERROR_DRAIN_TIMEOUT = 2.0
MIN_SELF_SERVICE_PASSWORD = 8
MAX_LOGIN_BODY = 8 * 1024
# The login page must render before the session gate can pass.  Only these
# exact paths are public; every other static file, the app shell and every API
# call (except health/mobile status design) requires a signed-in session.
LOGIN_ASSETS = frozenset({
    "/login",
    "/css/login.css",
    "/css/startup.css",
    "/js/login.js",
    "/js/startup-graph.js",
    "/js/startup-force.js",
    "/js/graph-core.js",
    "/js/arrows.js",
    "/js/icons.js",
    "/brand/ark-mark.svg",
    "/brand/ark-lockup-loading.svg",
    "/brand/ark-lockup-loading-dark.svg",
    "/favicon.svg",
    "/brand/icon-180.png",
})
PUBLIC_AUTH_PATHS = frozenset({
    "/login",
    "/logout",
    "/api/auth/session",
    "/api/auth/logout",
    "/api/health",
    "/api/mobile/session",
    "/api/mobile/pair/claim",
    "/api/mobile/login",
    "/download/ARK-Studio-android.apk",
    "/download/ark.apk",
    "/download/ARK-Studio-webapp.apk",
})

# Downloadable Android builds, read from <checkout>/dist on every request so
# replacing a file publishes the next build.  Both can be installed side by
# side: they use different application IDs.
ANDROID_APKS = {
    "native": {
        "file": "ARK-Studio-android.apk",
        "paths": ("/download/ARK-Studio-android.apk", "/download/ark.apk"),
        "app_id": "com.masteryip.arkstudio",
        "title": "ARK Studio (native)",
    },
    "webapp": {
        "file": "ARK-Studio-webapp.apk",
        "paths": ("/download/ARK-Studio-webapp.apk",),
        "app_id": "com.masteryip.arkstudio.web",
        "title": "ARK Studio Web",
    },
}


def android_apps() -> dict:
    """Which APKs this server can hand out (no file contents)."""
    out = {}
    for key, item in ANDROID_APKS.items():
        target = WEB_ROOT.parent / "dist" / item["file"]
        present = target.is_file()
        out[key] = {
            "available": present,
            "url": "." + item["paths"][0],
            "file": item["file"],
            "app_id": item["app_id"],
            "title": item["title"],
            "size": target.stat().st_size if present else 0,
            "updated": (datetime.fromtimestamp(target.stat().st_mtime, timezone.utc)
                        .isoformat(timespec="seconds") if present else None),
        }
    return out

# Shape version of the payloads browsers keep in their offline cache
# (web/js/client-cache.js).  Bump it when a cached response shape changes;
# clients drop caches written for another value (bootstrap.server.cache_schema).
CLIENT_CACHE_SCHEMA = 1
# Server-sent revision stream (GET /api/events): poll interval, keep-alive
# comment interval (under Cloudflare's 100 s idle timeout), access re-check
# interval and maximum stream lifetime before the client reconnects.
EVENTS_TICK = 1.0
EVENTS_KEEPALIVE = 20.0
EVENTS_RECHECK = 10.0
EVENTS_LIFETIME = 600.0
# How often the stream compares this account's agent-session list (ids and
# statuses) for the ``sessions`` event.
EVENTS_SESSIONS_EVERY = 3.0
UNIT_DETAILS_LIMIT = 60

# Endpoints that do not operate on the *currently open* project.  They either
# need no project (health/auth/org/personal settings) or resolve their own
# project and authorization (project list/create/open).
GLOBAL_API_PATHS = frozenset({
    "/api/health",
    "/api/bootstrap",
    "/api/projects",
    "/api/projects/open",
    "/api/auth/session",
    "/api/auth/logout",
    "/api/personal/settings",
    "/api/me",
    "/api/me/avatar",
    "/api/me/password",
    "/api/settings/effective",
    "/api/assistant/config",
    "/api/assistant/config/test",
    "/api/providers",
    "/api/providers/refresh",
    "/api/agent/v1/whoami",
})
GLOBAL_API_PREFIXES = (
    "/api/auth/",
    "/api/org/",
    "/api/org",
    "/api/personal/",
    "/api/mobile/",
    "/api/users/",
)

# A paired device is not a desktop account.  Only these project/workbench APIs
# are reachable with a mobile session; everything else (organization admin,
# personal credential settings, provider mutation, host actions) is denied.
MOBILE_DEVICE_API_PREFIXES = (
    "/api/bootstrap", "/api/project", "/api/revision", "/api/units",
    "/api/links", "/api/media", "/api/graph", "/api/timeline", "/api/topics",
    "/api/activity", "/api/chat", "/api/skills", "/api/infohub",
    "/api/import", "/api/export", "/api/assistant/insights",
    "/api/assistant/dispatch", "/api/assistant/apply",
    "/api/assistant/extract", "/api/agent-sessions", "/api/workspace",
    "/api/mobile/session", "/api/health", "/api/changes", "/api/events",
    "/api/unit-details",
)
MOBILE_DEVICE_SCOPED_GET = frozenset({"/api/org", "/api/projects", "/api/gpu/dashboard"})
MOBILE_DEVICE_PROFILE_PATHS = frozenset({"/api/me", "/api/me/avatar", "/api/me/password"})
MOBILE_AVATAR_RE = re.compile(r"^/api/users/[^/]+/avatar$")


def mobile_device_path_allowed(path: str, method: str = "GET") -> bool:
    """Explicit paired-device allowlist (deny by default)."""
    path = str(path or "")
    verb = str(method or "GET").upper()
    # A paired device acts as its account: it may read the profile and the
    # avatars of the org.  Profile edits need a full-access device (read-only
    # devices are refused earlier by PairingManager.authenticate); a password
    # change always needs a browser account session.
    if path in MOBILE_DEVICE_PROFILE_PATHS or MOBILE_AVATAR_RE.match(path):
        if path == "/api/me/password":
            return False
        return verb in {"GET", "HEAD"} or path in {"/api/me", "/api/me/avatar"}
    if path in MOBILE_DEVICE_SCOPED_GET:
        return str(method or "GET").upper() in {"GET", "HEAD"}
    return any(path == prefix or path.startswith(prefix + "/")
               for prefix in MOBILE_DEVICE_API_PREFIXES)


class ApiError(Exception):
    def __init__(self, status: int, message: str, code: str | None = None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code


def _float(value, default=0.0, low=0.0, high=100000.0) -> float:
    try:
        number = float(value)
        return max(low, min(high, number)) if number == number else default
    except (TypeError, ValueError):
        return default


def _bool(value) -> bool:
    return str(value).lower() in {"1", "true", "yes", "on"}


def _search_options(query: dict) -> dict:
    """case / word / regex flags of the Workbench Search routes."""
    return {key: _bool((query.get(key) or ["0"])[0]) for key in ("case", "word", "regex")}


# Streaming Workbench Search runs by client search id (``sid``): a newer
# request with the same id cancels the older one still running.
_SEARCH_RUNS: dict[str, int] = {}
_SEARCH_RUNS_LOCK = threading.Lock()
_SEARCH_SEQ = itertools.count(1)


def _int(value, default=0, low=0, high=10000) -> int:
    try:
        n = int(value)
    except (TypeError, ValueError):
        return default
    return max(low, min(high, n))


def _gpu_query_int(query, name, default, low, high):
    values = query.get(name, [str(default)])
    if len(values) != 1 or not re.fullmatch(r"[0-9]+", str(values[0])):
        raise ApiError(400, f"{name} must be an integer between {low} and {high}")
    value = int(values[0])
    if not low <= value <= high:
        raise ApiError(400, f"{name} must be between {low} and {high}")
    return value


def _gpu_list_query(query):
    filters = {"limit": _gpu_query_int(query, "limit", 100, 1, 500),
               "offset": _gpu_query_int(query, "offset", 0, 0, 1_000_000)}
    for key in ("name", "name_prefix", "since", "server"):
        if key in query:
            if len(query[key]) != 1:
                raise ApiError(400, f"{key} accepts one value")
            filters[key] = query[key][0]
    if "status" in query:
        filters["status"] = ",".join(_qlist(query, "status"))
        if not filters["status"]:
            raise ApiError(400, "status must contain at least one job state")
    if "ids" in query:
        filters["ids"] = _qlist(query, "ids")
    return filters


def _gpu_tail_query(query):
    return _gpu_query_int(query, "tail", 200, 1, 500)


def _comments_payload(comments: list[dict]) -> dict:
    """Comment list for agents, with the unresolved user comments counted."""
    open_user = [c for c in comments if c["author_kind"] == "user" and not c["resolved"]]
    return {"comments": comments, "total": len(comments),
            "open_user_comments": len(open_user)}


def _agent_actor(actor: str) -> str:
    """Writes on /api/agent/v1 always come from an agent."""
    return actor if str(actor).startswith("agent:") else "agent:api"


def _etag_matches(header, etag: str) -> bool:
    """Weak comparison of an If-None-Match header with one entity tag."""
    if not header or not etag:
        return False
    if str(header).strip() == "*":
        return True
    want = etag[2:] if etag.startswith("W/") else etag
    for item in str(header).split(","):
        item = item.strip()
        if item.startswith("W/"):
            item = item[2:]
        if item == want:
            return True
    return False


def _qlist(query: dict, name: str, alias: str | None = None) -> list[str]:
    values = []
    for key in (name, alias):
        if not key:
            continue
        for raw in query.get(key, []):
            for part in str(raw).split(","):
                part = part.strip()
                if part and part not in values:
                    values.append(part)
    return values


def filters_from_query(query: dict, *, me: str = "") -> dict:
    """Unit filters from a query string.  ``me`` is the requesting account,
    used to resolve ``assignee=me``."""
    return {
        "q": (query.get("q") or [""])[0].strip(),
        "types": _qlist(query, "types", "type"),
        "types_none": (query.get("types_none") or [""])[0] == "1",
        "origins": _qlist(query, "origins", "origin"),
        "statuses": _qlist(query, "statuses", "status"),
        "importance": _qlist(query, "importance"),
        "tags": _qlist(query, "tags", "tag"),
        "tags_unset": _qlist(query, "tags_unset", "tag_unset"),
        "roots": (query.get("roots") or [""])[0] in {"1", "true", "yes"},
        "slim": (query.get("slim") or [""])[0] in {"1", "true", "yes"},
        "start_from": (query.get("from") or [""])[0] or None,
        "start_to": (query.get("to") or [""])[0] or None,
        "updated_since": (query.get("updated_since") or [""])[0] or None,
        "overdue": (query.get("overdue") or [""])[0] in {"1", "true", "yes"},
        "created_since": (query.get("created_since") or [""])[0] or None,
        "sort": (query.get("sort") or ["updated"])[0],
        # assignee=<user>|none|me, comma-separated or repeated (OR).
        "assignees": _qlist(query, "assignees", "assignee"),
        "me": me,
    }


class App:
    def __init__(self, root: str | Path, *, seed_demo: bool = True, media_roots: list[str] | None = None,
                 task_sessions_root: str | Path | None = None,
                 task_presets: list[dict] | None = None,
                 gpu_service: gpu_probe.GpuService | None = None,
                 provider_catalog: providers.ProviderCatalog | None = None,
                 worktree_manager: WorktreeManager | None = None,
                 public_url: str | None = None,
                 auth: auth_mod.AuthManager | None = None,
                 org: org_mod.OrgManager | None = None,
                 org_path: str | Path | None = None,
                 expose: bool = False, public_ip: str = "",
                 detect_public_ip: bool = False,
                 trust_proxy: bool = False):
        self.root = Path(root).resolve()
        self.manager = ProjectManager(self.root, seed_demo=seed_demo)
        self.media_roots = [Path(p).resolve() for p in (media_roots or [])]
        self.lock = threading.RLock()
        self.auth = auth
        self.auth_enabled = bool(auth and auth.enabled)
        if self.auth_enabled and hasattr(auth, "enable_persistence"):
            # "Keep me signed in" sessions and the cookie signing key survive
            # restarts and redeploys (hashes only; see AuthManager).
            auth.enable_persistence(self.root)
        # The organization database is deliberately *outside* every project
        # folder: runtime/org.sqlite3 next to runtime/projects.
        self.org = org or org_mod.OrgManager(
            Path(org_path) if org_path else org_mod.default_org_path(self.root))
        if self.auth_enabled:
            self.auth.set_accounts(self.org)
        # Request-scoped identity.  Each HTTP request runs in its own thread, so
        # the active project never leaks from one signed-in person to another.
        self._local = threading.local()
        # Failed bearer-token attempts (personal and server token) per client.
        self.agent_token_failures = agent_tokens_mod.FailureLimiter()
        self._project_cache: dict[str, Project] = {}
        self._session_projects: dict[str, str] = {}
        # Set by shutdown(); open /api/events streams end on it.
        self.stopping = threading.Event()
        self.bootstrap_org()
        self.expose = bool(expose)
        self.public_ip = auth_mod.public_ip(public_ip) if public_ip else ""
        self.detect_public_ip = bool(detect_public_ip)
        self.trust_proxy = bool(trust_proxy)
        # Mobile/PWA pairing state is per-process and intentionally ephemeral:
        # a restart forgets every pairing token and phone session.
        raw_public = (public_url if public_url is not None
                      else os.environ.get("ARK_PUBLIC_URL") or "").strip()
        try:
            self.public_url = mobile.normalize_public_url(raw_public) if raw_public else ""
        except mobile.MobileError as exc:
            raise ValueError(f"invalid public URL: {exc.message}") from exc
        self.configure_auth_transport()
        self.bind_host = "127.0.0.1"
        self.port = 0
        self.allowed_hosts: set[str] = set()
        self.extra_allowed_hosts = {
            item.strip().lower() for item in (os.environ.get("ARK_ALLOWED_HOSTS") or "").split(",")
            if item.strip()
        }
        # Paired phones survive restarts/redeploys (token hashes only).
        self.mobile = mobile.PairingManager(state_path=self.root / ".mobile-sessions.json")
        if self.public_url:
            self.allow_advertised_url(self.public_url)
        self.project: Project | None = None
        self.started_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
        # GPU probing is an opt-in, read-only capability; it never blocks startup.
        self.gpu = gpu_service or gpu_probe.GpuService()
        self.project = self.manager.take_initial()
        if not self.project:
            projects = self.manager.list_projects()
            if projects:
                self.project = self.manager.open(projects[0]["path"])
            elif seed_demo:
                self.project = self.manager.create("PredActor", template="demo", demo=True)
            else:
                self.project = self.manager.create("My Research", template="blank")
        # Project-scoped Files workspace: lazy tree, safe file IO, git diffs and
        # bounded terminal processes.  State is local to this server process.
        self.workspace = workspace_files.Workspace()
        # Agent worktrees are project-scoped: the manager resolves the Git
        # repository from the open ARK project at request time.
        self.worktrees = worktree_manager or WorktreeManager(
            project_provider=lambda: self.require().summary())
        # ``runtime/agent-sessions`` sits next to ``runtime/projects`` by default.
        sessions_root = (Path(task_sessions_root) if task_sessions_root
                         else self.root.parent / "agent-sessions")
        self.sessions = SessionManager(
            sessions_root,
            project_provider=lambda: self.require().summary(),
            context_provider=lambda: self.task_context(),
            presets=task_presets,
            strip_env_values=[self.manager.agent_token],
            write_token=self.manager.agent_token,
            unit_provider=lambda uid: self.require().store.get_unit(uid),
            worktree_provider=self.worktrees.resolve_session_target,
            agent_config_provider=self.agent_config_for,
            assistant_config_provider=self.assistant_config_for,
            linux_account_provider=self.linux_account_for,
        )
        # The catalog probes lazily: constructing it never blocks server start.
        self.providers = provider_catalog or providers.ProviderCatalog(self.sessions.presets)
        self.sessions.set_provider_catalog(self.providers)
        self.sessions.on_change = self._on_session_change
        self.org.allowed_providers = [str(preset.get("id") or "")
                                      for preset in self.sessions.presets]
        self._project_cache[str(self.project.path)] = self.project
        blackbox_enabled = (os.environ.get("ARK_BLACKBOX", "1").strip().lower()
                            not in {"0", "false", "no", "off"})
        try:
            blackbox_interval = int(os.environ.get("ARK_BLACKBOX_INTERVAL") or 900)
        except ValueError:
            blackbox_interval = 900
        requested_blackbox = Path(os.environ.get("ARK_BLACKBOX_DIR")
                                  or (self.root.parent / "blackbox"))
        try:
            requested_blackbox.mkdir(parents=True, exist_ok=True)
        except OSError:
            # Some shared hosts own the parent (for example /data) but allow
            # writes below the projects root. Keep snapshots outside any
            # project while still staying on the writable volume.
            requested_blackbox = self.root / ".blackbox"
        self.blackbox = blackbox_mod.BlackBox(
            requested_blackbox,
            project_provider=lambda: self.project,
            interval_seconds=blackbox_interval,
            enabled=blackbox_enabled,
        )
        if self.blackbox.enabled:
            try:
                if not self.blackbox.list(self.project):
                    self.blackbox.snapshot(self.project, "startup", force=True)
            except Exception:
                # A backup failure must never prevent the server from starting.
                pass
            self.blackbox.start()

    # ----------------------------------------------------------- per-user settings
    def project_summary_for_settings(self) -> dict:
        project = self.current_project()
        return project.summary() if project is not None else {}

    def effective_assistant(self) -> dict:
        """Concrete assistant config for the current user and project."""
        return self.org.resolve_assistant(self.project_summary_for_settings(),
                                          self.actor())

    def effective_assistant_public(self) -> dict:
        resolved = self.effective_assistant()
        public = {key: value for key, value in resolved.items() if key != "api_key"}
        public["configured"] = bool(resolved.get("api_key"))
        public["has_key"] = bool(resolved.get("api_key"))
        public["key_hint"] = resolved.get("key_hint") or ""
        return public

    def assistant_config_for(self, username: str = "") -> dict:
        """Effective assistant config for a task subprocess (per-user key only).

        In multi-user mode a task must not inherit the server operator's global
        model credential; only that person's personal or project settings may
        provide one.
        """
        actor = str(username or "")
        resolved = self.org.resolve_assistant(self.project_summary_for_settings(),
                                              actor)
        if self.auth_enabled and (resolved.get("sources") or {}).get("api_key") == "global":
            resolved["api_key"] = ""
            resolved["has_key"] = False
            resolved["key_hint"] = ""
            resolved["configured"] = False
            resolved["sources"] = dict(resolved.get("sources") or {}, api_key="none")
        return resolved

    def agent_config_for(self, username: str = "") -> dict:
        """Provider commands/credentials inherited by a task session."""
        actor = str(username or self.actor())
        resolved = self.org.resolve_agents(self.project_summary_for_settings(),
                                           actor, self.sessions.presets)
        return resolved

    def linux_account_for(self, username: str = "") -> str:
        """Admin-assigned Linux account selected by a launching user."""
        actor = str(username or self.actor())
        if not actor:
            return ""
        try:
            return self.org.selected_linux_account(actor)
        except Exception:  # noqa: BLE001 - a settings read must not crash launch
            return ""

    def effective_agents_public(self) -> dict:
        resolved = self.agent_config_for()
        providers_out = []
        try:
            catalog = self.providers.snapshot()
            by_id = {item.get("id"): item for item in catalog.get("providers", [])}
        except Exception:  # noqa: BLE001 - discovery must never break settings
            by_id = {}
        for item in resolved.get("providers", []):
            provider = dict(item)
            catalog_item = by_id.get(provider["id"]) or {}
            credential = catalog_item.get("auth") or {}
            provider["credential_state"] = credential.get("state") or "unknown"
            provider["credential_sources"] = credential.get("sources") or []
            provider["installed"] = catalog_item.get("installed")
            provider["hint"] = catalog_item.get("detail") or ""
            needs_credentials = credential.get("state") in {"missing", "unauthenticated"}
            provider["personal_config_required"] = bool(
                needs_credentials and not provider.get("overridden")
                and not provider.get("project_configured")
                and not provider.get("env_names"))
            providers_out.append(provider)
        return {
            "providers": providers_out,
            "personal_configured": resolved.get("personal_configured", False),
            "project_configured": resolved.get("project_configured", False),
            "personal_config_required": bool(
                providers_out and not resolved.get("personal_configured")
                and not resolved.get("project_configured")),
        }

    def effective_settings_payload(self) -> dict:
        project = self.current_project()
        return {
            "user": self.actor(),
            "role": self.org.role_for(self.actor()) if self.auth_enabled else "owner",
            "project": ({"id": project.id, "name": project.name,
                         "permission": self.project_permission(project)}
                        if project is not None else None),
            "assistant": self.effective_assistant_public(),
            "agents": self.effective_agents_public(),
            "personal": {
                "assistant": self.org.personal_settings_public(self.actor(),
                                                               "assistant"),
                "agents": self.org.personal_settings_public(self.actor(), "agents"),
            },
            "project_settings": project_settings(project.manifest) if project else {},
        }

    # ------------------------------------------------------------- org/access
    def bootstrap_org(self) -> dict:
        """Create/verify the org, migrate the legacy account and projects."""
        summaries = self.manager.list_projects()
        if self.auth_enabled:
            sync = str(os.environ.get("ARK_AUTH_SYNC") or "").strip().lower() in {
                "1", "true", "yes", "on"}
            result = self.org.bootstrap(
                owner_username=self.auth.username,
                owner_password_hash=self.auth.password_hash,
                owner_display_name=self.auth.username,
                projects=summaries, sync_password=sync,
            )
        else:
            result = self.org.bootstrap(projects=summaries)
        return result

    def set_request_context(self, *, user: str = "", session=None,
                            project_id: str = "", mobile_session=None,
                            agent_token: dict | None = None,
                            tab_project_id: str = "") -> None:
        self._local.user = str(user or "")
        self._local.session = session
        # ``project_id`` pins a credential to one project (paired phone,
        # agent token, task-agent token).  ``tab_project_id`` is the project a
        # signed-in browser tab is showing (X-ARK-Project); it is a preference
        # that falls back when the account cannot read that project.
        self._local.project_id = str(project_id or "")
        self._local.tab_project_id = str(tab_project_id or "")
        self._local.mobile = mobile_session
        # Personal agent token row (``agent_tokens``) when the request
        # authenticated with one; it caps project access at its scope.
        self._local.agent_token = agent_token
        # Project id the request was actually served from (X-ARK-Project-Id).
        self._local.served_project = ""

    def request_agent_token(self) -> dict | None:
        return getattr(self._local, "agent_token", None)

    def clear_request_context(self) -> None:
        self.set_request_context()

    def request_session(self):
        return getattr(self._local, "session", None)

    def actor(self) -> str:
        """Username making this request; empty only for an unauthenticated call."""
        user = str(getattr(self._local, "user", "") or "")
        if user:
            return user
        if self.auth_enabled:
            session = self.request_session()
            return str(session.username) if session else ""
        return org_mod.LOCAL_USERNAME

    def _owner_bootstrap_name(self) -> str:
        return self.auth.username if self.auth_enabled else org_mod.LOCAL_USERNAME

    def _cached_project(self, path: str | Path) -> Project:
        key = str(Path(path).resolve())
        with self.lock:
            project = self._project_cache.get(key)
            if project is not None:
                return project
            project = self.manager.open(key)
            self._project_cache[key] = project
            return project

    def _demo_project(self, *, reset: bool = False) -> Project:
        username = self.actor() or org_mod.LOCAL_USERNAME
        path = demo.user_path(self.root, username).resolve()
        if reset:
            # Windows keeps a SQLite handle open, so close/drop the cached
            # project before replacing its folder.
            with self.lock:
                cached = self._project_cache.pop(str(path), None)
            if cached is not None:
                try:
                    cached.close()
                except Exception:  # noqa: BLE001 - best effort before replacement
                    pass
        path = demo.ensure_demo(self.root, username, reset=reset)
        return self._cached_project(path)

    def project_by_id(self, project_id: str) -> Project | None:
        project_id = str(project_id or "").strip()
        if not project_id:
            return None
        if project_id == demo.DEMO_ID:
            try:
                return self._demo_project()
            except (ProjectError, OSError):
                return None
        row = (self.org.store.project_row(project_id)
               or self.org.store.project_by_path(project_id))
        if row:
            path = Path(str(row.get("path") or ""))
            if path.is_dir():
                try:
                    return self._cached_project(path)
                except ProjectError:
                    return None
        for summary in self.manager.list_projects():
            if summary["id"] == project_id or summary["folder"] == project_id:
                try:
                    return self._cached_project(summary["path"])
                except ProjectError:
                    return None
        return None

    def _visible_project_ids(self, username: str) -> dict[str, dict]:
        visible = {item["id"]: item for item in self.org.projects_for(username)}
        # The tutorial is available to every signed-in member, including a
        # member with no project grants yet.
        visible[demo.DEMO_ID] = demo.summary(self.root, username)
        return visible

    def _default_project_for(self, username: str) -> Project | None:
        visible = self._visible_project_ids(username)
        for summary in self.manager.list_projects():
            if summary["id"] in visible:
                try:
                    return self._cached_project(summary["path"])
                except ProjectError:
                    continue
        if demo.DEMO_ID in visible:
            try:
                return self._demo_project()
            except (ProjectError, OSError):
                return None
        return None

    def _readable_project(self, project_id: str, user: str) -> Project | None:
        project = self.project_by_id(project_id) if project_id else None
        if project is not None and (project.id == demo.DEMO_ID
                                    or self.org.project_permission(user, project.id)):
            return project
        return None

    def _remembered_project_for(self, user: str, session=None) -> Project | None:
        """The session's last project, else the person's last project."""
        for project_id in (getattr(session, "project_id", "") if session is not None else "",
                           self.org.store.last_project(self.org.user_id_for(user) or "")):
            project = self._readable_project(str(project_id or ""), user)
            if project is not None:
                return project
        return None

    def _project_for_request(self) -> Project | None:
        """Account mode: the active project belongs to the request, never the
        server.  Order: a pinned credential project; the browser tab's
        X-ARK-Project; this session's project; the person's last project; the
        newest project they can read."""
        user = self.actor()
        if not user:
            return None
        forced = str(getattr(self._local, "project_id", "") or "")
        if forced:
            project = self.project_by_id(forced)
            if project is None:
                return None
            return project
        tab = self._readable_project(str(getattr(self._local, "tab_project_id", "") or ""), user)
        if tab is not None:
            return tab
        session = self.request_session()
        if session is not None:
            with self.lock:
                path = self._session_projects.get(session.sid_hash)
            if path:
                try:
                    project = self._cached_project(path)
                except ProjectError:
                    project = None
                if project is not None and (project.id == demo.DEMO_ID
                                              or self.org.project_permission(user, project.id)):
                    return project
                with self.lock:
                    self._session_projects.pop(session.sid_hash, None)
            project = self._remembered_project_for(user, session) or self._default_project_for(user)
            if project is not None:
                with self.lock:
                    self._session_projects[session.sid_hash] = str(project.path)
                # Pin the session (and, the first time, the person) to it, so
                # a restart or a project someone else creates later never
                # changes what this session shows.
                self.auth.set_session_project(session, project.id)
                user_id = self.org.user_id_for(user)
                if user_id and project.id != demo.DEMO_ID and not self.org.store.last_project(user_id):
                    self.org.store.set_last_project(user_id, project.id)
                return project
            return None
        return self._remembered_project_for(user) or self._default_project_for(user)

    def current_project(self) -> Project | None:
        project = self.project if not self.auth_enabled else self._project_for_request()
        if project is not None and hasattr(self._local, "served_project"):
            self._local.served_project = project.id
        return project

    def project_permission(self, project: Project | None = None) -> str | None:
        if not self.auth_enabled:
            return "admin"
        project = project or self.current_project()
        if project is None:
            return None
        user = self.actor()
        if not user:
            return None
        if project.id == demo.DEMO_ID:
            return "write"
        return self.org.project_permission(user, project.id)

    def check_project_access(self, project: Project, level: str = "read") -> str:
        """Enforce read/write/admin on one project for the current request."""
        needed = org_mod.validate_permission(level)
        if not self.auth_enabled:
            return "admin"
        user = self.actor()
        if not user:
            raise ApiError(401, "Sign in with an ARK account to continue.",
                           "AUTH_REQUIRED")
        permission = ("write" if project.id == demo.DEMO_ID
                      else self.org.project_permission(user, project.id))
        if not permission:
            raise ApiError(404, "Project not found or not accessible for this account.",
                           "PROJECT_NOT_ACCESSIBLE")
        mobile_session = getattr(self._local, "mobile", None)
        effective = permission
        if mobile_session is not None and mobile_session.permission == "read_only":
            effective = "read"
        if org_mod.PERMISSION_RANK[effective] < org_mod.PERMISSION_RANK[needed]:
            raise ApiError(
                403,
                f"This action needs {needed} access to the project "
                f"(your access is {effective}).",
                "PROJECT_FORBIDDEN",
            )
        return effective

    def mobile_org_overview(self) -> dict:
        """Minimal organization view for a paired device, pinned to its project."""
        overview = self.org.org_overview(self.actor())
        project = self.current_project()
        projects = [item for item in overview.get("projects") or []
                    if project is not None and item.get("id") == project.id]
        return {
            "org": overview.get("org"),
            "user": overview.get("user"),
            "role": overview.get("role"),
            "is_admin": overview.get("is_admin"),
            "is_owner": overview.get("is_owner"),
            "projects": projects,
            "teams": [],
            "device": True,
        }

    def resolve_assignee(self, name: str, project_id: str | None = None) -> str:
        """Canonical ARK username for a unit ``assignee`` or a 400 StoreError.

        Only accounts that have joined the project are accepted
        (``OrgService.project_member``: a user or team grant), matched
        case-insensitively and stored in the account's own case.  In local
        single-user mode the local user is the only member.
        """
        text = str(name or "").strip()
        user = self.org.store.user_by_name(text) if text else None
        if not self.auth_enabled:
            if text.lower() == org_mod.LOCAL_USERNAME:
                return org_mod.LOCAL_USERNAME
        elif user and (project_id is None
                       or self.org.project_member(str(user["username"]), project_id)):
            return str(user["username"])
        if user:
            raise StoreError(
                f"{text!r} has not joined this project: assign a project member, "
                "or null to unassign.")
        raise StoreError(
            f"Unknown assignee {text!r}: use an existing ARK account username, "
            "or null to unassign.")

    def account_name(self, name: str) -> str | None:
        """Canonical username of a real ARK account (any project), else None."""
        text = str(name or "").strip()
        if not text:
            return None
        if not self.auth_enabled:
            return org_mod.LOCAL_USERNAME if text.lower() == org_mod.LOCAL_USERNAME else None
        user = self.org.store.user_by_name(text)
        return str(user["username"]) if user else None

    def project_member_names(self, project: Project) -> list[dict]:
        """Accounts that have joined ``project`` (see ``resolve_assignee``)."""
        if not self.auth_enabled:
            return [{"username": org_mod.LOCAL_USERNAME, "display_name": org_mod.LOCAL_USERNAME}]
        # One query (it used to be several per user).
        return self.org.project_member_names(project.id)

    def assignable_users(self, project: Project) -> dict:
        """Accounts the Assignee picker and "Assign to" offer for this project.

        ``users`` are project members only (``project_member_names``);
        ``me_member`` says whether the caller is one.  ``former`` lists names
        still used as assignees in the project whose account is no longer a
        member, so the UI can show and clear them but never offers them.
        """
        me = self.actor()
        users = self.project_member_names(project)
        seen = {user["username"].lower() for user in users}
        former = [name for name in project.store.assignees_in_use() if name.lower() not in seen]
        return {"users": users, "me": me, "me_member": bool(me) and me.lower() in seen,
                "former": former}

    def require(self, level: str = "read") -> Project:
        project = self.current_project()
        if project is not None:
            project_id = project.id
            project.store.assignee_resolver = (
                lambda name, _pid=project_id: self.resolve_assignee(name, _pid))
            # actor() reads this thread's request context, so one resolver
            # serves every request (UI, agent API, skills, imports).
            project.store.creator_resolver = self.actor
            project.store.backfill_creator_assignees(self.account_name)
        if project is None:
            if self.auth_enabled:
                raise ApiError(
                    409,
                    "No project is available for your account yet. Ask an "
                    "organization admin for a project grant.",
                    "NO_PROJECT_ACCESS",
                )
            raise ApiError(409, "No project is open. Create or open one first.")
        token = self.request_agent_token()
        if token is not None and org_mod.PERMISSION_RANK[org_mod.validate_permission(level)]                 > org_mod.PERMISSION_RANK.get(str(token.get("scope") or "read"), 1):
            raise ApiError(
                403,
                f"This agent token has {token.get('scope')} scope; the action needs "
                f"{level} access.",
                "AGENT_TOKEN_SCOPE")
        if self.auth_enabled:
            self.check_project_access(project, level)
        self._local.served_project = project.id
        return project

    def served_project_id(self) -> str:
        return str(getattr(self._local, "served_project", "") or "")

    def cache_identity(self) -> str:
        """Opaque per-account key for the browser's offline cache.

        Clients key cached project data by it (never by the username) and
        wipe caches of another identity.  Derived from a random per-server
        salt, so it does not reveal the username and differs per server.
        """
        user = self.actor()
        if not user:
            return ""
        salt_path = self.root / ".client-cache-salt"
        with self.lock:
            salt = getattr(self, "_cache_salt", "")
            if not salt:
                try:
                    salt = salt_path.read_text(encoding="utf-8").strip()
                except OSError:
                    salt = ""
                if len(salt) < 16:
                    salt = uuid.uuid4().hex + uuid.uuid4().hex
                    try:
                        salt_path.write_text(salt, encoding="utf-8")
                    except OSError:
                        pass
                self._cache_salt = salt
        return hashlib.sha256(f"{salt}:{user.lower()}".encode("utf-8")).hexdigest()[:20]

    def require_admin(self) -> Project:
        return self.require("admin")

    def visible_projects(self) -> list[dict]:
        summaries = self.manager.list_projects()
        self.org.sync_projects(summaries, owner_username=self._owner_bootstrap_name())
        if not self.auth_enabled:
            out = [dict(item, permission="admin", role="owner") for item in summaries]
            if demo.DEMO_ID not in {item["id"] for item in out}:
                out.append(dict(demo.summary(self.root, org_mod.LOCAL_USERNAME),
                                role="owner"))
            return out
        user = self.actor()
        if not user:
            return []
        access = self._visible_project_ids(user)
        out = []
        for summary in summaries:
            row = access.get(summary["id"])
            if not row:
                continue
            item = dict(summary)
            item["permission"] = row["permission"]
            item["grants"] = row.get("grants") or []
            item["team_grants"] = row.get("team_grants") or []
            out.append(item)
        if demo.DEMO_ID not in {item["id"] for item in out}:
            item = dict(demo.summary(self.root, user))
            item["grants"] = []
            item["team_grants"] = []
            out.append(item)
        return out

    def session_payload(self) -> dict:
        if not self.auth_enabled:
            user = self.org.store.user_by_name(org_mod.LOCAL_USERNAME)
            return {
                "required": False,
                "authenticated": True,
                "local": True,
                "user": org_mod.LOCAL_USERNAME,
                "role": "owner",
                "is_admin": True,
                "is_owner": True,
                "org": {"id": self.org.org_id,
                        "name": (self.org.store.org() or {}).get("name")},
                "project_permission": "admin",
                "cache_id": self.cache_identity(),
            }
        session = self.request_session()
        if session is None:
            return {"required": True, "authenticated": False, "local": False,
                    "user": None, "role": None, "is_admin": False,
                    "is_owner": False, "org": None, "project_permission": None}
        role = self.org.role_for(session.username)
        org = self.org.store.org() or {}
        return {
            "required": True,
            "authenticated": True,
            "local": False,
            "user": session.username,
            "role": role,
            "is_admin": bool(role and org_mod.ROLE_RANK.get(role, -1)
                             >= org_mod.ROLE_RANK["admin"]),
            "is_owner": role == "owner",
            "org": {"id": org.get("id"), "name": org.get("name")},
            "project_permission": self.project_permission(),
            "cache_id": self.cache_identity(),
        }

    def switch(self, project: Project) -> None:
        with self.lock:
            if self.auth_enabled:
                session = self.request_session()
                if getattr(self._local, "project_id", ""):
                    raise ApiError(403, "A paired phone stays on its paired project.",
                                   "MOBILE_WRONG_PROJECT")
                self._project_cache[str(project.path)] = project
                # The rest of this request (the bootstrap reply) follows the
                # new project; other tabs keep sending their own X-ARK-Project
                # and other users are never touched.
                self._local.tab_project_id = project.id
                if session is not None:
                    old_path = self._session_projects.get(session.sid_hash)
                    self._session_projects[session.sid_hash] = str(project.path)
                    if old_path and old_path != str(project.path):
                        self._release_project(old_path)
                    self.auth.set_session_project(session, project.id)
                user_id = self.org.user_id_for(self.actor())
                if user_id and project.id != demo.DEMO_ID:
                    self.org.store.set_last_project(user_id, project.id)
                return
            old = self.project
            self.project = project
            if old and old is not project and old.path != project.path:
                with self.lock:
                    self._project_cache.pop(str(old.path), None)
                old.close()

    def _release_project(self, path: str) -> None:
        if self.project is not None and str(self.project.path) == path:
            return
        if any(project_path == path
               for project_path in self._session_projects.values()):
            return
        cached = self._project_cache.pop(path, None)
        if cached is not None and cached is not self.project:
            cached.close()

    def shutdown(self) -> None:
        """Close every project connection this app opened (tests/desktop exit)."""
        self.stopping.set()
        try:
            self.blackbox.stop()
        except Exception:
            pass
        with self.lock:
            for path, project in list(self._project_cache.items()):
                if project is not self.project:
                    project.close()
                self._project_cache.pop(path, None)
            if self.project is not None:
                try:
                    self.project.close()
                except StoreError:
                    pass
            self.org.close()
        try:
            self.gpu.close()
        except Exception:  # noqa: BLE001 - shutdown must not mask the exit
            pass

    def configure_auth_transport(self) -> None:
        """Follow the configured public URL for cookie path/Secure.

        The browser sees the proxy's path prefix (``/ark/``), while this stdlib
        server sees stripped paths.  The cookie must be scoped to the browser's
        view, otherwise it is not sent back through the proxy.
        """
        if not self.auth or not self.public_url:
            return
        self.auth.cookie_path = auth_mod.cookie_path_from_url(self.public_url)
        self.auth.secure = auth_mod.cookie_secure_from_url(self.public_url)

    # ------------------------------------------------------------ mobile/PWA
    def allow_advertised_url(self, url: str) -> None:
        """Register the Host a tunneled phone will send to this loopback server.

        A browser reaching ARK through ``ssh -L 0.0.0.0:...`` keeps the tunnel
        computer's address in the Host header.  The normal Host allow-list would
        reject that request, so an explicitly generated pairing URL (and
        ``ARK_PUBLIC_URL``) teaches the server the one extra host first.
        """
        try:
            host, port = mobile.url_host_port(url)
        except (ValueError, TypeError):
            return
        if not host:
            return
        host = host.lower()
        self.allowed_hosts.add(f"{host}:{port}")
        if port in (80, 443):
            self.allowed_hosts.add(host)
        self.allowed_hosts.add(host)

    def host_allowed(self, host_header: str) -> bool:
        """DNS-rebinding guard that still accepts explicitly configured hosts."""
        host = str(host_header or "").strip()
        if not host:
            return False
        normalized = host.lower()
        if normalized in self.allowed_hosts or normalized in self.extra_allowed_hosts:
            return True
        parsed = urlparse("//" + normalized)
        hostname = (parsed.hostname or "").lower()
        if hostname and (hostname in self.extra_allowed_hosts
                         or hostname in self.allowed_hosts):
            return True
        if not mobile.is_loopback_host(self.bind_host):
            if self.public_url:
                # A configured public URL is an allow-list: otherwise a
                # DNS-rebinding page could present the same public origin over
                # an unexpected Host.  Add aliases through ARK_ALLOWED_HOSTS.
                return False
            # A deliberately public bind cannot enumerate every name; the
            # account gate (never optional for a public bind) authenticates
            # the caller.  Prefer --public-url/ARK_ALLOWED_HOSTS in production.
            return True
        return False

    def mobile_connection(self, host_header: str = "", *,
                          request_scheme: str = "http") -> dict:
        info = mobile.connection_info(self, host_header, request_scheme=request_scheme)
        info["pairs_pending"] = self.mobile.pending_count(self.require().summary())
        info["allowed_hosts"] = sorted(self.allowed_hosts)
        return info

    def create_pairing(self, body: dict, *, request_host: str = "",
                       request_port=None, request_scheme: str = "") -> dict:
        # A pairing is always bound to the account that created it.  The phone
        # can never inherit a different user, and the QR is scoped to exactly
        # this project.
        project = self.require("read")
        # Default: full access, signed in until revoked.  An account without
        # write permission gets read-only instead of an error.
        requested = str(body.get("permission") or "").strip().lower()
        permission = requested or mobile.DEFAULT_PERMISSION
        if permission == "full":
            try:
                self.require("write")
            except ApiError:
                if requested:
                    raise
                permission = "read_only"
        actor = self.actor()
        override_host = str(body.get("tunnel_host") or body.get("advertised_host") or "").strip()
        override_port = body.get("tunnel_port") or body.get("advertised_port")
        override_base = str(body.get("public_url") or "").strip()
        try:
            base, source = mobile.resolve_pairing_base(
                self, override_host=override_host, override_port=override_port,
                override_base=override_base,
                request_host=request_host, request_port=request_port,
                request_scheme=request_scheme)
            if not base:
                if source == "lan_unresolved":
                    raise ApiError(
                        409,
                        "ARK is reachable on a non-loopback interface but no usable "
                        "address could be selected. Set --public-url/ARK_PUBLIC_URL, "
                        "or use the loopback SSH tunnel with a tunnel host.",
                        "MOBILE_LAN_UNRESOLVED",
                    )
                raise ApiError(
                    409,
                    "ARK is loopback-only. Start the SSH tunnel on a phone-reachable "
                    "computer and enter that computer's LAN address, or expose ARK "
                    "with --expose/--public-url and account authentication. A phone "
                    "cannot run SSH by itself.",
                    "MOBILE_LOOPBACK_ONLY",
                )
            pair = self.mobile.create(
                project.summary(),
                permission=permission,
                ttl_seconds=body.get("expires_in"),
                session_ttl=body.get("session_ttl"),
                client="desktop",
                user=actor,
            )
            pairing_url = mobile.build_pairing_url(base, pair["token"])
            qr_svg = qr_codes.to_svg(pairing_url)
        except mobile.MobileError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        self.allow_advertised_url(base)
        # Log the non-secret pair id only; the token exists solely in the
        # response body and the QR image.
        project.store.log_activity(
            "human:ui", "mobile.pair.create", None,
            {"pair": pair["id"], "permission": pair["permission"], "source": source},
        )
        self.org.store.log(self.org.user_id_for(actor), "mobile.pair.create",
                           project.id, {"pair": pair["id"],
                                        "permission": pair["permission"]})
        return {
            "pair": {
                "id": pair["id"],
                "permission": pair["permission"],
                "project": pair["project_name"],
                "user": pair.get("user") or actor,
                "created_at": pair["created_at"],
                "expires_at": pair["expires_at"],
                "expires_in": pair["expires_in"],
                "session_permanent": pair.get("session_permanent", False),
                "session_ttl": pair.get("session_ttl"),
            },
            "code": pair["token"],
            "pairing_url": pairing_url,
            "payload": pairing_url,
            "qr_svg": qr_svg,
            "base_url": base,
            "base_source": source,
            "auto": not bool(override_host or override_base),
            "auth_required": self.auth_enabled,
            "requires_login": self.auth_enabled,
            "project": project.summary(),
        }

    def revoke_pairing(self, pair_id: str) -> dict:
        project = self.require("write")
        try:
            revoked = self.mobile.revoke(pair_id, project.summary())
        except mobile.MobileError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        project.store.log_activity("human:ui", "mobile.pair.revoke", None,
                                   {"pair": revoked["id"]})
        return {"revoked": revoked, "pairs": self.mobile.list(project.summary())}

    def mobile_status(self, token: str, required: bool, *, host_header: str = "",
                      request_scheme: str = "http") -> dict:
        project = self.require()
        summary = project.summary()
        session = self.mobile.session_for_token(token)
        authenticated = bool(session and session.project_id == summary.get("id"))
        reason = ""
        if token and not session:
            reason = "invalid_or_expired"
        elif session and not authenticated:
            reason = "wrong_project"
        info = mobile.connection_info(self, host_header, request_scheme=request_scheme)
        return {
            "required": bool(required),
            "authenticated": authenticated,
            "token_present": bool(token),
            "reason": reason,
            "permission": session.permission if authenticated else None,
            "read_only": session.permission == "read_only" if authenticated else None,
            "expires_at": (
                datetime.fromtimestamp(session.expires_at, tz=timezone.utc)
                .isoformat(timespec="seconds")
                if authenticated else None
            ),
            "project": {"id": summary.get("id"), "name": summary.get("name")},
            "source": (getattr(self.mobile.pairing_for(session), "source", "qr")
                       if authenticated else None),
            "user": (session.user or None) if authenticated else None,
            "auth_required": self.auth_enabled,
            "auth_user": (self.actor() if self.auth_enabled else None),
            "cache_id": self.cache_identity() or None,
            "cache_schema": CLIENT_CACHE_SCHEMA,
            "connection": {
                "mode": info["mode"],
                "loopback_only": info["loopback_only"],
                "public_url": info["public_url"],
                "advertised_urls": info["advertised_urls"],
                "auth_required": info["auth_required"],
                "pwa_note": info["pwa_note"],
            },
        }

    def bootstrap(self) -> dict:
        project = self.current_project()
        if project is not None:
            payload = project.store.project_payload()
            payload["summary"] = project.summary()
        else:
            payload = {
                "manifest": None,
                "path": "",
                "stats": {},
                "activity": [],
                "next_actions": [],
                "needs_you": [],
                "updated_at": None,
                "summary": None,
            }
        session = self.session_payload()
        payload["projects"] = self.visible_projects()
        payload["session"] = session
        payload["org"] = self.org.org_overview(self.actor()) if self.actor() else None
        payload["users"] = self.org.user_directory() if self.actor() else []
        try:
            payload["profile"] = self.profile_payload()["profile"] if self.actor() else None
        except (ApiError, org_mod.OrgError):
            payload["profile"] = None
        payload["vocabulary"] = public_vocabulary()
        payload["palette_presets"] = PALETTE_PRESETS
        payload["server"] = {"started_at": self.started_at, "version": "2.0",
                             "cache_schema": CLIENT_CACHE_SCHEMA}
        return payload

    def profile_payload(self) -> dict:
        """``GET /api/me``: the signed-in account's own editable profile."""
        username = self.actor()
        try:
            profile = self.org.profile(username)
        except org_mod.OrgError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        session = self.session_payload()
        mobile_session = getattr(self._local, "mobile", None)
        profile["permissions"] = {
            "role": session.get("role") or self.org.role_for(username),
            "is_admin": bool(session.get("is_admin")),
            "is_owner": bool(session.get("is_owner")),
            "project_permission": session.get("project_permission"),
            "local": not self.auth_enabled,
            "device": mobile_session is not None,
            "read_only": bool(mobile_session is not None
                              and mobile_session.permission == "read_only"),
            # Only a browser account session can change the sign-in password.
            "can_change_password": bool(self.auth_enabled and mobile_session is None),
        }
        return {
            "profile": profile,
            "options": {
                "palette": list(org_mod.AVATAR_COLORS),
                "langs": list(org_mod.PROFILE_LANGS),
                "themes": list(org_mod.PROFILE_THEMES),
                "limits": {"display_name": org_mod.MAX_DISPLAY_NAME,
                           "bio": org_mod.MAX_BIO, "email": org_mod.MAX_EMAIL,
                           "avatar_bytes": org_mod.MAX_AVATAR_BYTES},
            },
            "users": self.org.user_directory(),
        }

    def project_payload(self) -> dict:
        project = self.require()
        payload = project.store.project_payload()
        payload["summary"] = project.summary()
        return payload

    def project_overview(self, *, setup_state: dict) -> dict:
        """Everything Home needs in one read (docs/guide/home-overview.md)."""
        started = time.perf_counter()
        marks: dict[str, float] = {}

        def lap(name: str, since: list) -> None:
            now = time.perf_counter()
            marks[name] = round((now - since[0]) * 1000, 2)
            since[0] = now

        since = [started]
        project = self.require("read")
        members = [user["username"] for user in self.project_member_names(project)]
        lap("members", since)
        data = overview_mod.project_overview(project.store, members=members)
        lap("store", since)
        data["setup"] = {key: setup_state[key] for key in ("state", "layers", "readiness", "host")}
        lap("setup", since)
        try:
            mine = [item for item in self.sessions.brief()
                    if str((item.get("project") or {}).get("id") or "") == str(project.id)]
            counts: dict[str, int] = {}
            for item in mine:
                counts[str(item.get("status") or "")] = counts.get(str(item.get("status") or ""), 0) + 1
            data["runs"] = overview_mod.runs_summary({"sessions": mine, "counts": counts})
        except Exception:  # noqa: BLE001 - sessions are optional on Home
            data["runs"] = overview_mod.runs_summary({})
        lap("runs", since)
        data["gpu"] = overview_mod.gpu_summary(self.gpu, project=project.id)
        lap("gpu", since)
        data["me"] = self.actor()
        data["timing"] = marks
        data["generated_ms"] = round((time.perf_counter() - started) * 1000, 2)
        return data

    def task_context(self, session=None) -> dict:
        """Compact read-only project view for task agents.

        Only plain data leaves the store: no write token, no media bytes, no
        mutable handles.  ``GET /api/task-agent/v1/context`` serves a fresh copy
        and each session also gets a generated ``ark-context.json`` snapshot.
        """
        project = self.require()
        store = project.store
        units = store.list_units({"limit": 1000, "sort": "updated"})["items"]
        edges = store.graph({"limit": 1200})["edges"]
        slim_units = []
        for unit in units:
            slim_units.append({
                "id": unit.get("id"),
                "uid": unit.get("uid"),
                "type": unit.get("type"),
                "title": unit.get("title"),
                "body": (unit.get("body") or "")[:600],
                "status": unit.get("status"),
                "importance": unit.get("importance"),
                "tags": unit.get("tags") or [],
                "start_at": unit.get("start_at"),
                "end_at": unit.get("end_at"),
                "origin": unit.get("origin"),
                "source_ref": unit.get("source_ref"),
                "updated_at": unit.get("updated_at"),
                "comments_open": unit.get("comments_open") or 0,
            })
        open_ids = [u["id"] for u in slim_units if u["comments_open"]]
        if open_ids:
            by_id = {u["id"]: u for u in slim_units}
            for uid in open_ids:
                by_id[uid]["open_comments"] = [
                    {"id": c["id"], "kind": c["kind"], "author": c["author"],
                     "created_at": c["created_at"], "body": c["body"][:600]}
                    for c in store.comments(uid) if c["author_kind"] == "user" and not c["resolved"]]
        payload = {
            "format": "ark-task-agent-context",
            "version": 1,
            "generated_at": db.utc_now(),
            "read_only": True,
            "project": project.summary(),
            "stats": store.stats(),
            "units": slim_units,
            "links": [
                {"source": edge.get("source"), "target": edge.get("target"),
                 "relation": edge.get("relation"), "note": edge.get("note")}
                for edge in edges
            ],
        }
        if session is not None:
            worktree = session.meta.get("worktree") if isinstance(session.meta.get("worktree"), dict) else {}
            payload["session"] = {
                "id": session.id,
                "platform": session.meta.get("platform"),
                "task_ref": session.meta.get("task_ref") or "",
                "worktree": ({"path": worktree.get("path"), "branch": worktree.get("branch") or ""}
                             if worktree.get("path") else None),
                "read_only": True,
            }
        return payload

    def create_project(self, body: dict) -> dict:
        name = str(body.get("name") or "").strip()
        if not name:
            raise ApiError(400, "Project name is required")
        if self.auth_enabled:
            # Any regular organization member may create a project.  They become
            # that project's admin below; this is project permission, not the
            # organization-wide admin role used for user/team management.
            role = self.org.role_for(self.actor())
            if role not in {"owner", "admin", "member"}:
                raise ApiError(
                    403,
                    "Only organization members can create projects.",
                    "ORG_FORBIDDEN",
                )
        template = str(body.get("template") or "blank")
        project = self.manager.create(
            name,
            description=str(body.get("description") or "").strip()[:600],
            template=template,
        )
        # New projects start with every coding agent unchecked.  The creator
        # (project admin) can enable selected providers later; this avoids
        # accidentally launching a provider for a fresh project.
        agent_ids = [str(preset.get("id") or "") for preset in self.sessions.presets]
        agent_ids = [pid for pid in agent_ids
                     if pid and pid not in {"echo", "shell"}]
        project.manifest.setdefault("settings", {})["agents"] = {
            "providers": {pid: {"enabled": False} for pid in agent_ids},
        }
        project.manifest["updated"] = db.utc_now()
        db.write_manifest(project.path, project.manifest)
        record = self.org.register_project(project.summary(),
                                           owner_username=self.actor(),
                                           grant_owner=True)
        actor_id = self.org.user_id_for(self.actor()) or ""
        for owner in self.org.store.owners(self.org.org_id):
            if not any(grant["user_id"] == owner["id"] for grant in
                       self.org.store.project_grants(project.id)):
                self.org.store.set_project_grant(project.id, owner["id"], "admin",
                                                 granted_by=actor_id)
        self.org.store.log(actor_id, "project.create", project.id, {"name": name})
        self._project_cache[str(project.path)] = project
        self.switch(project)
        return self.bootstrap()

    def delete_project(self, body: dict) -> dict:
        """Delete the current project and its folder; project admin only."""
        project = self.require_admin()
        if str(project.id) == demo.DEMO_ID:
            raise ApiError(403, "The tutorial project cannot be deleted.",
                           "DEMO_PROJECT_PROTECTED")
        expected = str(body.get("name") or "").strip()
        if expected != project.name:
            raise ApiError(
                400,
                "Type the current project name exactly to confirm deletion.",
                "PROJECT_CONFIRM_REQUIRED",
            )
        project_id = str(project.id)
        active = [item for item in self.sessions.list().get("sessions") or []
                  if str((item.get("project") or {}).get("id") or "") == project_id
                  and str(item.get("status") or "") in
                  {"starting", "running", "idle", "stopping"}]
        if active:
            raise ApiError(
                409,
                "Stop this project's task sessions before deleting it.",
                "PROJECT_SESSIONS_ACTIVE",
            )
        path = str(project.path)
        with self.lock:
            self._project_cache.pop(path, None)
        project.close()
        try:
            self.manager.delete(project_id)
        except ProjectError as exc:
            raise ApiError(404, str(exc), "PROJECT_NOT_FOUND") from exc
        self.org.store.unregister_project(project_id)
        self.org.store.log(self.org.user_id_for(self.actor()) or "",
                           "project.delete", project_id, {"name": project.name})
        if not self.auth_enabled:
            self.project = None
        else:
            self._local.tab_project_id = ""
        next_project = self._default_project_for(self.actor())
        if next_project is not None:
            self.switch(next_project)
        return self.bootstrap()

    def _resolve_project_ref(self, ref: str) -> Project:
        """Resolve a project id or a confined on-disk path, checked for access."""
        text = str(ref or "").strip()
        if not text:
            raise ApiError(400, "Project id or path is required")
        if text == demo.DEMO_ID:
            try:
                return self._demo_project(reset=True)
            except (ProjectError, OSError) as exc:
                raise ApiError(500, f"Cannot open the demo project: {exc}",
                               "DEMO_UNAVAILABLE") from exc
        candidate = Path(text)
        if not candidate.is_absolute() and not text.endswith(".ark"):
            # A registered id (or folder name) is always resolved inside the root.
            project = self.project_by_id(text)
            if project is None:
                raise ApiError(404, "Project not found or not accessible.",
                               "PROJECT_NOT_ACCESSIBLE")
            return project
        target = (candidate if candidate.is_absolute() else self.root / text).resolve()
        # Project folders stay inside the configured project root.  This blocks
        # ``../`` traversal and absolute paths outside the workbench.
        if target != self.root and self.root not in target.parents:
            raise ApiError(403, "Project folders must stay inside the configured root.",
                           "PROJECT_PATH_DENIED")
        if not target.is_dir():
            raise ApiError(404, "Project folder not found.", "PROJECT_NOT_ACCESSIBLE")
        summary = None
        for item in self.manager.list_projects():
            if Path(item["path"]).resolve() == target:
                summary = item
                break
        if summary is None:
            raise ApiError(404, "Not an ARK project folder.", "PROJECT_NOT_ACCESSIBLE")
        project = self._cached_project(target)
        # New folders dropped into the root are registered for the org, but
        # only owners gain automatic access (migration/stewardship).
        if self.org.store.project_by_path(str(target)) is None:
            self.org.sync_projects(self.manager.list_projects(),
                                   owner_username=self._owner_bootstrap_name())
        return project

    def open_project(self, body: dict) -> dict:
        ref = str(body.get("id") or body.get("path") or "").strip()
        if not ref:
            raise ApiError(400, "Project id or path is required")
        project = self._resolve_project_ref(ref)
        if self.auth_enabled and project.id != demo.DEMO_ID:
            try:
                self.org.require_project_permission(self.actor(), project.id, "read")
            except org_mod.OrgError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
        self.switch(project)
        return self.bootstrap()

    def update_project(self, body: dict) -> dict:
        project = self.require_admin()
        changed = False
        for key in ("name", "description", "accent"):
            if key in body and body[key] is not None:
                value = str(body[key]).strip()
                if key == "name" and not value:
                    raise ApiError(400, "Project name cannot be empty")
                project.manifest[key] = value[:600]
                changed = True
        if changed:
            project.manifest["updated"] = db.utc_now()
            db.write_manifest(project.path, project.manifest)
            project.store.log_activity("human:ui", "project.update", None, {"fields": list(body)})
            self.org.store.touch_project(project.id, project.name,
                                         project.manifest["updated"])
        return self.project_payload()

    # ------------------------------------------------------ resources graph
    def resources_graph(self) -> dict:
        """Devices, resources and connections (read-only; never probes)."""
        project = self.require("read")
        sources = resources_graph_mod.collect_sources(self, project)
        return resources_graph_mod.build_graph(sources, self.actor())

    def update_device_privacy(self, device_id: str, body: dict) -> dict:
        """Hide or share one of the caller's own personal devices."""
        project = self.require("read")
        actor = self.actor()
        graph = resources_graph_mod.build_graph(
            resources_graph_mod.collect_sources(self, project), actor)
        mine = {device["id"] for device in graph["devices"] if device.get("mine")}
        # A hidden device is no longer in the graph; its owner can still unhide it.
        prefs = resources_graph_mod.device_prefs(self, actor)
        if device_id not in mine and device_id not in prefs:
            raise ApiError(404, "personal device not found", "RESOURCES_DEVICE_NOT_FOUND")
        try:
            saved = resources_graph_mod.set_device_pref(self, actor, device_id, body or {})
        except resources_graph_mod.ResourcesGraphError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        return {"device": saved}

    def update_project_settings(self, body: dict) -> dict:
        project = self.require_admin()
        try:
            settings = settings_patch(
                project.manifest, body,
                allowed_providers=[str(preset.get("id") or "")
                                   for preset in self.sessions.presets])
        except SettingsError as exc:
            raise ApiError(400, str(exc)) from exc
        project.manifest["settings"] = settings
        project.manifest["updated"] = db.utc_now()
        db.write_manifest(project.path, project.manifest)
        project.store.log_activity("human:ui", "project.settings", None,
                                   {"sections": sorted(settings)})
        self.org.store.log(self.org.user_id_for(self.actor()),
                           "project.settings", project.id,
                           {"sections": sorted(body)})
        return {
            "settings": project_settings(project.manifest),
            "summary": project.summary(),
            "updated_at": project.manifest["updated"],
        }

    # ---------------------------------------------------- session context
    # docs/agents/session-context.md: internal task sessions (runtime folder is
    # the live source, mirrored into <project>.ark/sessions/internal/) plus
    # external transcripts uploaded into <project>.ark/sessions/.  Every record
    # is private to its creator unless shared; project admins see all.
    def context_store(self, project: Project | None = None) -> session_context.ContextStore:
        project = project or self.require()
        return session_context.ContextStore(project.path, extra_secrets=[self.manager.agent_token])

    def session_uploads_enabled(self, project: Project | None = None) -> bool:
        project = project or self.require()
        return bool(project_settings(project.manifest)["sessions"]["accept_uploads"])

    def _project_path_by_id(self, project_id: str) -> Path | None:
        project = self.project_by_id(project_id) if project_id else None
        return project.path if project is not None else None

    def _on_session_change(self, session, event: str) -> None:
        """SessionManager hook: keep the project's redacted mirror in step."""
        ref = session.meta.get("project") or {}
        path = self._project_path_by_id(str(ref.get("id") or ""))
        if path is None:
            return
        if event == "delete":
            session_context.remove_mirror(path, session.id)
        elif not session.is_active or event == "update":
            session_context.mirror_internal(path, session.folder, [self.manager.agent_token])

    def sync_session_mirrors(self, project: Project) -> None:
        """Refresh stale mirrors of this project's finished task sessions."""
        for payload in self.sessions.list().get("sessions") or []:
            if str((payload.get("project") or {}).get("id") or "") != str(project.id):
                continue
            try:
                session = self.sessions.get(str(payload.get("id")))
            except SessionError:
                continue
            if session.is_active:
                continue
            try:
                if session_context.mirror_is_stale(project.path, session.folder):
                    session_context.mirror_internal(project.path, session.folder, [self.manager.agent_token])
            except (OSError, SessionError):
                continue

    def _live_internal(self, project: Project) -> dict[str, dict]:
        out = {}
        for payload in self.sessions.list().get("sessions") or []:
            if str((payload.get("project") or {}).get("id") or "") != str(project.id):
                continue
            try:
                folder = self.sessions.get(str(payload.get("id"))).folder
            except SessionError:
                folder = None
            out[str(payload.get("id"))] = session_context.internal_public(payload, folder)
        return out

    def session_contexts(self, viewer: tuple[str, bool]) -> dict:
        user, admin = viewer
        project = self.require()
        store = self.context_store(project)
        self.sync_session_mirrors(project)
        live = self._live_internal(project)
        items = store.list() + list(live.values())
        for session_id in session_context.mirror_ids(project.path):
            if session_id not in live:
                try:
                    items.append(session_context.mirror_public(project.path, session_id))
                except SessionError:
                    continue
        items = [session_context.with_access(item, user, admin) for item in items
                 if session_context.can_view(item, user, admin)]
        items.sort(key=lambda item: str(item.get("updated_at") or ""), reverse=True)
        limits = session_context.limits()
        return {"sessions": items,
                "settings": project_settings(project.manifest)["sessions"],
                "usage": {"bytes": store.usage_bytes(), "limit": limits["project_bytes"]},
                "limits": limits, "viewer": {"user": user, "admin": admin}}

    def _context_kind(self, session_id: str) -> str:
        session_id = str(session_id or "")
        if session_id.startswith("ark-ses-"):
            session_context.ContextStore.check_id(session_id)
            return "external"
        try:
            session = self.sessions.get(session_id)
        except SessionError:
            session = None
        if session is not None:
            ref = session.meta.get("project") or {}
            if str(ref.get("id") or "") == str(self.require().id):
                return "live"
            raise ApiError(404, "Session not found or not accessible.", "SESSION_NOT_ACCESSIBLE")
        session_context.read_mirror_meta(self.require().path, session_id)
        return "copy"

    def _context_record(self, session_id: str) -> dict:
        kind = self._context_kind(session_id)
        if kind == "external":
            store = self.context_store()
            return store.public(store.get(session_id))
        if kind == "live":
            session = self.sessions.get(session_id)
            return session_context.internal_public(self.sessions.public(session), session.folder)
        return session_context.mirror_public(self.require().path, session_id)

    def session_context(self, session_id: str, viewer: tuple[str, bool], *, manage: bool = False) -> dict:
        """The record if ``viewer`` may see it (404 otherwise); ``manage`` needs owner/admin (403)."""
        user, admin = viewer
        record = self._context_record(session_id)
        if not session_context.can_view(record, user, admin):
            raise ApiError(404, "Session not found or not accessible.", "SESSION_NOT_ACCESSIBLE")
        if manage and not session_context.can_manage(record, user, admin):
            raise ApiError(403, "Only the session's creator or a project admin can change it.",
                           "SESSION_NOT_OWNER")
        return session_context.with_access(record, user, admin)

    def session_context_transcript(self, session_id: str, viewer: tuple[str, bool]) -> dict:
        record = self.session_context(session_id, viewer)
        if record["source"] == "external":
            return self.context_store().transcript(session_id)
        if record.get("copy"):
            return session_context.mirror_transcript(self.require().path, session_id)
        data = self.sessions.transcript(session_id)
        data["source"] = "internal"
        return data

    def session_context_raw(self, session_id: str, viewer: tuple[str, bool]) -> bytes:
        record = self.session_context(session_id, viewer)
        if record["source"] == "external":
            source = self.context_store().raw_path(session_id)
        elif record.get("copy"):
            source = session_context.mirror_folder(self.require().path, session_id) / "stdout.jsonl"
        else:
            # The download is the redacted mirror, never the raw runtime log.
            session_context.mirror_internal(self.require().path, self.sessions.get(session_id).folder,
                                            [self.manager.agent_token])
            source = session_context.mirror_folder(self.require().path, session_id) / "stdout.jsonl"
        return source.read_bytes() if source.is_file() else b""

    def session_context_summary(self, session_id: str, viewer: tuple[str, bool]) -> str:
        record = self.session_context(session_id, viewer)
        items = self.session_context_transcript(session_id, viewer).get("items") or []
        return session_context.context_summary(record, items)

    def update_session_context(self, session_id: str, body: dict, viewer: tuple[str, bool]) -> dict:
        record = self.session_context(session_id, viewer, manage=True)
        if record["source"] == "external":
            self.context_store().update(session_id, body)
        elif record.get("copy"):
            session_context.update_mirror(self.require().path, session_id, body)
        else:
            self.sessions.update_context(session_id, body)
        return self.session_context(session_id, viewer)

    def delete_session_context(self, session_id: str, viewer: tuple[str, bool]) -> dict:
        record = self.session_context(session_id, viewer, manage=True)
        if record["source"] == "external":
            return self.context_store().delete(session_id)
        if record.get("copy"):
            session_context.remove_mirror(self.require().path, session_id)
            return {"deleted": session_id}
        return self.sessions.delete(session_id)

    def continue_session_context(self, session_id: str, body: dict, viewer: tuple[str, bool], *,
                                 base_url: str = "") -> dict:
        """Resume natively when the provider can (owner/admin only), else seed a new session."""
        record = self.session_context(session_id, viewer)
        mode = str(body.get("mode") or "auto").strip().lower()
        if mode not in {"auto", "resume", "seed"}:
            raise ApiError(400, "mode must be auto, resume or seed", "SESSION_CONTINUE_MODE_INVALID")
        text = str(body.get("text") or body.get("message") or "").strip()
        resumable = (record.get("source") == "internal" and not record.get("copy")
                     and bool((record.get("resume") or {}).get("native")))
        if mode == "resume" and not resumable:
            raise ApiError(409, "this session has no provider session id to resume",
                           "SESSION_NOT_RESUMABLE")
        if mode == "resume" and not record.get("can_manage"):
            raise ApiError(403, "Only the session's creator or a project admin can change it.",
                           "SESSION_NOT_OWNER")
        if (resumable and record.get("can_manage") and mode in {"auto", "resume"}
                and not body.get("platform")):
            if not text:
                raise ApiError(400, "a message is required to continue", "SESSION_TEXT_REQUIRED")
            payload = self.sessions.continue_session(
                session_id, text, model=body.get("model") or None, actor=self.actor(), resume=True)
            return {"mode": "resume", "session": payload}
        project = self.require()
        if str(project.summary().get("id") or "") == demo.DEMO_ID:
            raise ApiError(403, "Coding agents are disabled in the demo project. "
                                "Chat and Assistant are still available.", "DEMO_AGENTS_DISABLED")
        platform = str(body.get("platform") or "").strip()
        if not platform:
            preset_ids = {str(p.get("id")) for p in self.sessions.presets}
            agent = str(record.get("agent") or "")
            platform = agent if agent in preset_ids else ""
        if not platform:
            raise ApiError(400, "choose a platform for the new session", "SESSION_PLATFORM_REQUIRED")
        summary = self.session_context_summary(session_id, viewer)
        prompt = (text or "Continue the work from the previous session.") + "\n\n" + summary
        links = list(record.get("linked_units") or [])
        task = str(record.get("task_ref") or (links[0] if links else ""))
        title = str(record.get("title") or record.get("id") or "")
        new = self.sessions.start({
            "platform": platform,
            "title": (title if title.startswith("Continue · ") else "Continue · " + title)[:120],
            "prompt": prompt,
            "model": body.get("model") or (record.get("model") if platform == record.get("agent") else ""),
            "task": task,
        }, base_url=base_url, actor=self.actor())
        with new.lock:
            new.meta["continued_from"] = record.get("id")
            new.meta["linked_units"] = links
        self.sessions.save(new)
        return {"mode": "seed", "session": new.public(detail=True)}

    def chat_state(self, limit: int = 100) -> dict:
        project = self.require()
        messages = project.store.chat_history(limit, user=self.actor())
        for message in messages:
            if message.get("content"):
                message["content"] = chat.clean_message(message["content"])
        return {
            "messages": messages,
            "harness": chat.harness_status(self.effective_assistant()),
            "assistant": self.effective_assistant_public(),
            "project": project.summary(),
        }

    def chat_send(self, body: dict, base_url: str = "") -> dict:
        project = self.require("write")
        message = str(body.get("message") or "").strip()
        if not message:
            raise ApiError(400, "message is required")
        if len(message) > 8000:
            raise ApiError(400, "message is too long")
        config = self.effective_assistant()
        who = self.actor()
        user_message = project.store.add_chat("user", message, meta={"source": "ui"}, user=who)
        try:
            result = chat.run_chat(project.store, project, message,
                                   agent_token=self.manager.agent_token, base_url=base_url,
                                   infohub=body.get("infohub") if isinstance(body.get("infohub"), dict) else None,
                                   config=config, history_user=who)
        except (ValueError, StoreError) as exc:
            raise ApiError(400, str(exc)) from exc
        assistant_message = project.store.add_chat(
            "assistant", result["message"],
            tools=result.get("tools") or [],
            meta={
                "harness": result.get("harness") or chat.harness_status(config)["label"],
                "suggestions": result.get("suggestions") or [],
                "refresh": bool(result.get("refresh")),
                "reasoning": result.get("reasoning") or "",
                "user": who,
            },
            user=who,
        )
        return {
            "user": user_message,
            "assistant": assistant_message,
            "refresh": bool(result.get("refresh")),
            "harness": chat.harness_status(config),
        }

    def chat_stream(self, body: dict, base_url: str = ""):
        project = self.require("write")
        message = str(body.get("message") or "").strip()
        if not message:
            raise ApiError(400, "message is required")
        if len(message) > 8000:
            raise ApiError(400, "message is too long")
        config = self.effective_assistant()
        who = self.actor()
        user_message = project.store.add_chat("user", message, meta={"source": "ui", "stream": True}, user=who)
        yield {"type": "user", "message": user_message}
        try:
            result = yield from chat.stream_chat(
                project.store, project, message,
                agent_token=self.manager.agent_token, base_url=base_url,
                infohub=body.get("infohub") if isinstance(body.get("infohub"), dict) else None,
                config=config, history_user=who)
        except (ValueError, StoreError) as exc:
            yield {"type": "error", "message": str(exc)}
            return
        reasoning = str(result.get("reasoning") or "")
        answer = str(result.get("message") or "")
        assistant_message = project.store.add_chat(
            "assistant", answer,
            tools=result.get("tools") or [],
            meta={
                "harness": result.get("harness") or chat.harness_status(config)["label"],
                "suggestions": result.get("suggestions") or [],
                "refresh": bool(result.get("refresh")),
                "reasoning": reasoning,
                "stream": True,
            },
            user=who,
        )
        yield {"type": "done", "user": user_message, "assistant": assistant_message,
               "harness": chat.harness_status(config),
               "refresh": bool(result.get("refresh"))}

    def chat_clear(self) -> dict:
        """DELETE /api/chat: clear this user's Assistant history in this
        project only (other users keep theirs)."""
        project = self.require()
        who = self.actor()
        count = project.store.clear_chat(user=who)
        project.store.log_activity(f"human:{who}" if who else "human:ui", "chat.clear", None, {"messages": count})
        return {"cleared": count}

    # ---------------------------------------------------------------- media IO
    def remote_info(self, host_header: str = "") -> dict:
        project = self.require()
        port = 8890
        host = "127.0.0.1"
        if host_header:
            try:
                parsed = urlparse("//" + host_header)
                host = parsed.hostname or host
                port = parsed.port or port
            except ValueError:
                pass
        urls = []
        try:
            for address in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
                ip = address[4][0]
                if ip and not ip.startswith("127.") and ip not in [u.split("//")[1].split(":")[0] for u in urls]:
                    urls.append(f"http://{ip}:{port}/")
        except OSError:
            pass
        return {
            "project": project.name,
            "path": str(project.path),
            "host": host,
            "port": port,
            "urls": urls,
            "media_roots": [str(path) for path in self.media_roots],
            "request_url": f"http://{host_header}/" if host_header else "",
        }

    def resolve_media(self, media_id: int) -> tuple[Path, str]:
        project = self.require()
        row = project.store.media_row(media_id)
        if not row:
            raise ApiError(404, "Media not found")
        url = row["url"]
        if re.match(r"^https?://", url, re.I):
            raise ApiError(302, url)
        if url.lower().startswith("file://"):
            parsed = urlparse(url)
            raw = unquote(parsed.path)
            if re.match(r"^/[A-Za-z]:/", raw):
                raw = raw[1:]
            path = Path(raw)
        else:
            path = Path(url)
            if not path.is_absolute():
                path = project.path / path
        path = path.expanduser().resolve()
        allowed = [project.path] + self.media_roots
        if not any(path == root or root in path.parents for root in allowed):
            raise ApiError(403, "Local media outside the project folder is not served. "
                                "Start the server with --media-root to allow it.")
        if not path.is_file():
            raise ApiError(404, f"Media file not found: {path.name}")
        ctype = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
        return path, ctype

    # ------------------------------------------------------------- open external
    def open_external(self, body: dict) -> dict:
        """Open one scoped resource in the OS default application.

        Accepted shapes (exactly one source is used, in this order):
        ``{unit}``, ``{session, path}``, ``{path}`` or ``{url}``.  Local paths
        are resolved inside the active project or session workspace only.
        """
        project = self.require()
        unit_ref = str(body.get("unit") or "").strip()
        session_id = str(body.get("session") or "").strip()
        path_value = str(body.get("path") or "").strip()
        url_value = str(body.get("url") or "").strip()
        try:
            if unit_ref:
                unit = project.store.get_unit(unit_ref)
                denied: platform_support.PlatformError | None = None
                target: Path | None = None
                for item in unit.get("media") or []:
                    raw = str(item.get("url") or "").strip()
                    if not raw or re.match(r"^https?://", raw, re.I):
                        continue
                    try:
                        target = platform_support.resolve_media_path(project.path, raw)
                    except platform_support.PlatformError as exc:
                        denied = denied or exc
                        continue
                    break
                if target is None:
                    if denied is not None:
                        raise ApiError(denied.status, str(denied))
                    raise ApiError(
                        400,
                        f"{unit.get('uid') or unit_ref} has no local media file to open; "
                        "add a local asset under the project first",
                    )
                result = platform_support.open_external(target)
                result.update({"mode": "unit", "unit": unit.get("uid")})
                return result
            if session_id:
                if not path_value:
                    raise ApiError(400, "session open requests need a workspace-relative path")
                try:
                    target_session = self.sessions.get(session_id)
                except SessionError as exc:
                    raise ApiError(404, "session not found or not accessible",
                                   "SESSION_NOT_ACCESSIBLE") from exc
                session_project = target_session.meta.get("project") or {}
                if str(session_project.get("id") or "") != project.id:
                    # Project admin on project A must not open project B's files.
                    raise ApiError(404, "session not found or not accessible",
                                   "SESSION_NOT_ACCESSIBLE")
                target = self.sessions.artifact_file(session_id, path_value)
                result = platform_support.open_external(target)
                result.update({"mode": "session", "session": session_id})
                return result
            if path_value:
                if path_value.lower().startswith("file://"):
                    target = platform_support.ensure_within(
                        project.path, platform_support.path_from_file_url(path_value))
                else:
                    target = platform_support.safe_path(project.path, path_value)
                if not target.is_file():
                    raise ApiError(400, "only files can be opened")
                result = platform_support.open_external(target)
                result.update({"mode": "path"})
                return result
            if url_value:
                result = (platform_support.default_browser_open(url_value)
                          if re.match(r"^https?://", url_value, re.I)
                          else platform_support.open_url(url_value))
                result.update({"mode": "url"})
                return result
        except platform_support.PlatformError as exc:
            raise ApiError(exc.status, str(exc)) from exc
        raise ApiError(400, "open requires one of: unit, session+path, path, url")


_asset_version_lock = threading.Lock()
_asset_version_cached = (None, 0.0, "")
_static_assets = StaticAssets()


def _asset_version() -> str:
    override = (os.environ.get("ARK_ASSET_VERSION") or "").strip()
    if override:
        return re.sub(r"[^A-Za-z0-9_.-]", "", override)[:40]
    # A module graph requests dozens of files at once. Scan once per burst,
    # retaining hot reload while avoiding a recursive disk scan per request.
    global _asset_version_cached
    with _asset_version_lock:
        root, checked, version = _asset_version_cached
        if root == WEB_ROOT and time.monotonic() - checked < 0.5:
            return version
        try:
            latest = max((item.stat().st_mtime_ns for item in WEB_ROOT.rglob("*") if item.is_file()), default=0)
        except OSError:
            latest = 0
        version = format(latest, "x")
        _asset_version_cached = (WEB_ROOT, time.monotonic(), version)
        return version


def _versioned_html(text: str, version: str) -> str:
    if '<meta charset="utf-8">' in text:
        text = text.replace('<meta charset="utf-8">',
                            f'<meta charset="utf-8">\n  <meta name="ark-build" content="{version}">', 1)
    # Asset URLs are relative so the app also works behind a path-prefixed
    # reverse proxy; accept both "./css/..." and "/css/..." spellings.
    pattern = r'((?:href|src)=")((?:\./|/)(?:css|js|vendor)/[^"?]+)(")'
    return re.sub(pattern, lambda m: f'{m.group(1)}{m.group(2)}?v={version}{m.group(3)}', text)


# The app's entry module.  index.html lists every module it imports
# statically (computed from the files, so editing web/js needs no build
# step); boot.js turns the list into modulepreload links once it has started
# the entry screen and the early data requests, so those are not queued
# behind a hundred module downloads.
APP_ENTRY = "js/app.js"
_PRELOAD_MARK = "<!-- ark:modulepreload -->"


def _index_html(text: str, version: str) -> str:
    text = _versioned_html(text, version)
    if _PRELOAD_MARK not in text:
        return text
    modules = json.dumps([APP_ENTRY] + module_graph(WEB_ROOT, APP_ENTRY))
    return text.replace(_PRELOAD_MARK,
                        f'<script type="application/json" id="ark-modules">{modules}</script>', 1)


def _versioned_js(source: str, version: str) -> str:
    patterns = [
        r'(\bfrom\s*["\'])(\.{1,2}/[^"\'?]+?\.js)(["\'])',
        r'(\bimport\s*\(\s*["\'])(\.{1,2}/[^"\'?]+?\.js)(["\'])',
        r'(\bimport\s*["\'])(\.{1,2}/[^"\'?]+?\.js)(["\'])',
    ]
    for pattern in patterns:
        source = re.sub(pattern, lambda m: f'{m.group(1)}{m.group(2)}?v={version}{m.group(3)}', source)
    return source


class Handler(BaseHTTPRequestHandler):
    app: App
    server_version = "ARKStudio/2.0"
    protocol_version = "HTTP/1.1"
    allowed_hosts: set[str] = set()

    def log_message(self, fmt, *args):
        if not os.environ.get("ARK_DEBUG"):
            return
        # Even debug logging must never contain a pairing code or a mobile
        # session token carried in the query string.
        message = fmt % args
        message = re.sub(r"(mobile_token=)[^&\s]+", r"\1<redacted>", message)
        message = re.sub(r"([#?&]pair=)[^&\s]+", r"\1<redacted>", message)
        message = re.sub(r"((?:password|token|code)=)[^&\s]+", r"\1<redacted>", message)
        super().log_message("%s", message)

    # ------------------------------------------------------------------ helpers
    def _send(self, status: int, body, ctype="application/json; charset=utf-8", headers=None, *, compress=True):
        if isinstance(body, (dict, list)):
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        elif isinstance(body, str):
            data = body.encode("utf-8")
        else:
            data = body
        headers = dict(headers or {})
        if self._api_get_json(status, ctype):
            # Conditional GET (docs/development/client-cache.md): every JSON API
            # reply has a weak validator of its exact bytes, unless the route
            # already set a revision-keyed one; a matching If-None-Match gets
            # an empty 304.  Weak, because a proxy may re-encode the body.
            headers.setdefault("ETag", 'W/"' + hashlib.blake2b(data, digest_size=12).hexdigest() + '"')
            served = self.app.served_project_id()
            if served:
                headers.setdefault("X-ARK-Project-Id", served)
            if _etag_matches(self.headers.get("If-None-Match"), headers["ETag"]):
                return self._send_not_modified(headers)
        # Graph/unit payloads can be hundreds of kilobytes.  Compressing them
        # is the difference between a prompt filter response and a multi-second
        # round trip over a tunnel.  Small replies stay uncompressed.
        accept = self.headers.get("Accept-Encoding") or ""
        if (compress and len(data) >= 1024 and accepts_gzip(accept)
                and "Content-Encoding" not in headers):
            compressed = gzip.compress(data, compresslevel=5)
            if len(compressed) < len(data):
                headers["Content-Encoding"] = "gzip"
                headers["Vary"] = "Accept-Encoding"
                data = compressed
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self._send_security_headers(headers)
        # A browser can close a connection while the server is writing a
        # response (page unload, headless teardown).  That is not an API error,
        # so never turn it into a noisy internal-error traceback.
        try:
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            # The browser closed a tab or aborted a streaming request mid-reply;
            # there is no one left to report the error to.
            self.close_connection = True

    def _api_get_json(self, status: int, ctype: str) -> bool:
        return (status == 200 and self.command in {"GET", "HEAD"}
                and str(ctype).startswith("application/json")
                and urlparse(self.path).path.startswith("/api/"))

    def _send_not_modified(self, headers=None) -> None:
        headers = {key: value for key, value in dict(headers or {}).items()
                   if key not in {"Content-Encoding", "Content-Length"}}
        self.send_response(304)
        self._send_security_headers(headers)
        try:
            self.end_headers()
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            self.close_connection = True

    def _revision_etag(self, project: Project) -> str:
        """Validator that changes with the project revision (cheap to check)."""
        key = "|".join([str(CLIENT_CACHE_SCHEMA), project.id, self.app.actor() or "",
                        str(self.app.project_permission(project) or ""), self.path,
                        project.store.revision()])
        return 'W/"r' + hashlib.blake2b(key.encode("utf-8"), digest_size=12).hexdigest() + '"'

    def _ok_revisioned(self, project: Project, build) -> None:
        """Answer a revision-keyed GET: 304 before building when unchanged.

        The revision is read before the body is built, so a write that lands
        in between only makes the next request miss (never a stale 304).
        """
        etag = self._revision_etag(project)
        headers = {"ETag": etag, "X-ARK-Project-Id": project.id}
        if _etag_matches(self.headers.get("If-None-Match"), etag):
            return self._send_not_modified(headers)
        return self._ok(build(), headers=headers)

    def _event_stream(self, query: dict) -> None:
        """``GET /api/events``: server-sent revision notifications.

        One ``revision`` event on connect and after every committed change
        (``id:`` is the change cursor, data ``{revision, cursor, project}``).
        Comments keep idle proxies open.  Access is re-checked every
        EVENTS_RECHECK seconds: a signed-out session gets ``auth``, a revoked
        project grant ``denied``; both end the stream.  After EVENTS_LIFETIME
        the server sends ``reconnect`` and closes; EventSource reconnects.
        """
        project = self.app.require("read")
        session = self.app.request_session()
        mobile_session = getattr(self.app._local, "mobile", None)
        headers = {"Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no",
                   "Connection": "close", "X-ARK-Project-Id": project.id}
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self._send_security_headers(headers)
        self.close_connection = True

        def write(text: str) -> bool:
            try:
                self.wfile.write(text.encode("utf-8"))
                self.wfile.flush()
                return True
            except (OSError, ValueError):
                return False

        try:
            self.end_headers()
        except OSError:
            return
        if not write("retry: 3000\n\n"):
            return
        started = last_write = last_check = time.monotonic()
        last_revision = None
        last_sessions = None
        sessions_checked = 0.0
        while not self.app.stopping.is_set():
            now = time.monotonic()
            if now - last_check >= EVENTS_RECHECK:
                last_check = now
                event = ""
                if self.app.auth_enabled:
                    if mobile_session is not None:
                        if self._mobile_session_for_project() is None:
                            event = "auth"
                    elif session is not None and self._auth_session() is None:
                        event = "auth"
                if not event:
                    try:
                        self.app.check_project_access(project, "read")
                    except ApiError:
                        event = "denied"
                if event:
                    write(f"event: {event}\ndata: {json.dumps({'project': project.id})}\n\n")
                    return
            try:
                revision = project.store.revision()
                cursor = project.store.change_cursor() if revision != last_revision else ""
            except (sqlite3.Error, StoreError):
                revision = last_revision
            if revision != last_revision:
                last_revision = revision
                data = json.dumps({"revision": revision, "cursor": cursor, "project": project.id})
                if not write(f"id: {cursor}\nevent: revision\ndata: {data}\n\n"):
                    return
                last_write = now
            if now - sessions_checked >= EVENTS_SESSIONS_EVERY:
                sessions_checked = now
                token = self._sessions_token()
                if token != last_sessions:
                    last_sessions = token
                    data = json.dumps({"token": token, "project": project.id})
                    if not write(f"event: sessions\ndata: {data}\n\n"):
                        return
                    last_write = now
            if now - last_write >= EVENTS_KEEPALIVE:
                if not write(": keep-alive\n\n"):
                    return
                last_write = now
            if now - started >= EVENTS_LIFETIME:
                write("event: reconnect\ndata: {}\n\n")
                return
            if self._client_gone():
                return
            self.app.stopping.wait(EVENTS_TICK)

    DEFAULT_CSP = (
        "default-src 'self'; img-src 'self' data: blob: https: http:; "
        "style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; "
        "font-src 'self' data:; worker-src 'self'; manifest-src 'self'; "
        "form-action 'self'; base-uri 'none'; object-src 'none'; "
        "frame-ancestors 'none'"
    )

    def _send_security_headers(self, headers=None):
        """Common response headers.

        ``headers`` may replace ``Content-Security-Policy`` and
        ``X-Frame-Options`` (used only for same-origin PDF framing); every other
        key is appended.  Replacing instead of appending matters: browsers
        enforce *every* CSP header, so a second header could never relax the
        default one.
        """
        headers = dict(headers or {})
        csp = headers.pop("Content-Security-Policy", self.DEFAULT_CSP)
        frame = headers.pop("X-Frame-Options", "DENY")
        self.send_header("Cache-Control", headers.pop("Cache-Control", "no-store"))
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", frame)
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Content-Security-Policy", csp)
        for key, value in headers.items():
            self.send_header(key, value)

    # Types a browser may render inline from /api/media/{id}/raw or
    # /api/workspace/raw.  Anything else is served as an attachment so a
    # project file (HTML, JS, ...) can never run with ARK's origin.
    _PREVIEW_PREFIXES = ("image/", "video/", "audio/")
    _PREVIEW_TYPES = {"application/pdf", "text/plain", "text/markdown", "text/csv",
                      "application/json"}
    _RANGE_RE = re.compile(r"^bytes=(\d*)-(\d*)$")
    # Same-origin framing for the built-in PDF viewer; see _file_headers.
    PDF_CSP = ("default-src 'none'; object-src 'self'; style-src 'unsafe-inline'; "
               "img-src 'self' data:; frame-ancestors 'self'")
    # Opaque origin, no scripts: opening an SVG/text file top-level cannot
    # touch ARK's cookies, storage or API.  <img>/<video>/<audio>
    # subresource loads ignore the header, so previews are unaffected.
    FILE_CSP = ("sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; "
                "style-src 'unsafe-inline'; frame-ancestors 'self'")

    # web/viz-frame.html hosts embedded HTML visualisations (```html blocks and
    # .html attachments).  ARK frames it with sandbox="allow-scripts" (no
    # allow-same-origin); the header repeats the sandbox so the page is an
    # opaque origin even if opened top-level, lets inline scripts run, and
    # forbids every network fetch (connect/img/media/font only from data:/blob:).
    # srcdoc is not usable: it would inherit ARK's own script-src 'self'.
    VIZ_CSP = ("sandbox allow-scripts; default-src 'none'; "
               "script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; "
               "img-src data: blob:; media-src data: blob:; font-src data:; "
               "connect-src 'none'; form-action 'none'; base-uri 'none'; "
               "frame-ancestors 'self'")

    def _file_headers(self, path: Path, ctype: str, *, download: bool = False) -> tuple[str, dict]:
        safe_name = re.sub(r'[^A-Za-z0-9._ -]', "_", path.name) or "file"
        inline = not download and (ctype.startswith(self._PREVIEW_PREFIXES)
                                   or ctype in self._PREVIEW_TYPES)
        if not inline:
            ctype = "application/octet-stream"
        elif ctype in {"text/plain", "text/markdown", "text/csv"}:
            ctype = f"{ctype}; charset=utf-8"
        headers = {
            "Content-Disposition": f'{"inline" if inline else "attachment"}; filename="{safe_name}"',
            "Accept-Ranges": "bytes",
        }
        if ctype == "application/pdf":
            # Chromium/Edge refuse to start their PDF viewer inside a sandboxed
            # document, under object-src 'none' or when framing is denied, and
            # the Editor shows PDFs in an <iframe>.  So PDFs (only) may be
            # framed by ARK itself and use the built-in viewer; scripts, forms,
            # connections and third-party framing stay forbidden.
            headers["Content-Security-Policy"] = self.PDF_CSP
            headers["X-Frame-Options"] = "SAMEORIGIN"
        else:
            headers["Content-Security-Policy"] = self.FILE_CSP
        return ctype, headers

    def _send_file(self, path: Path, ctype: str, *, download: bool = False):
        """Stream a file with single-range HTTP Range support (video seeking)."""
        stat = path.stat()
        size = stat.st_size
        ctype, headers = self._file_headers(path, ctype, download=download)
        # Validators: a browser (or the service worker's media cache) keeps
        # the bytes and revalidates; authorization still runs on every
        # request, so a revoked account gets 403/404 rather than a 304.
        etag = f'"f{stat.st_mtime_ns:x}-{size:x}"'
        headers.setdefault("ETag", etag)
        headers.setdefault("Last-Modified", datetime.fromtimestamp(
            stat.st_mtime, tz=timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT"))
        if headers.get("Cache-Control", "no-store") == "no-store" and not download:
            headers["Cache-Control"] = "private, no-cache"
        if (not self.headers.get("Range")
                and _etag_matches(self.headers.get("If-None-Match"), etag)):
            return self._send_not_modified(headers)
        start, end = 0, size - 1
        status = 200
        range_header = str(self.headers.get("Range") or "").replace(" ", "")
        match = self._RANGE_RE.match(range_header) if range_header else None
        if match and (match.group(1) or match.group(2)):
            if match.group(1):
                first = int(match.group(1))
                last = min(int(match.group(2)) if match.group(2) else size - 1, size - 1)
            else:  # suffix range: the last N bytes
                suffix = int(match.group(2))
                first, last = (max(0, size - suffix), size - 1) if suffix else (size, size - 1)
            if first >= size or first > last:
                self.send_response(416)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", "0")
                self.send_header("Content-Range", f"bytes */{size}")
                self._send_security_headers({"Accept-Ranges": "bytes",
                                             "Content-Security-Policy": self.FILE_CSP})
                self.end_headers()
                return
            start, end, status = first, last, 206
        # Malformed or multi-range requests fall through to a full 200 body,
        # which RFC 9110 allows.
        length = max(0, end - start + 1)
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(length))
        if status == 206:
            self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self._send_security_headers(headers)
        try:
            self.end_headers()
            if self.command == "HEAD" or not length:
                return
            with path.open("rb") as handle:
                handle.seek(start)
                remaining = length
                while remaining > 0:
                    chunk = handle.read(min(256 * 1024, remaining))
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    remaining -= len(chunk)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            # Seeking a <video> aborts the previous range request mid-reply.
            self.close_connection = True

    def _send_markdown_bundle(self, project: Project, query: dict):
        """Build the ZIP in a temporary file (videos can be large) and stream it."""
        try:
            refs = markdown_bundle.parse_refs((query.get("units") or [""])[0])
            depth = int((query.get("depth") or ["1"])[0] or 1)
            bundle = markdown_bundle.MarkdownBundle(
                project.store, project_path=project.path, project_name=project.name,
                resolve_media=markdown_bundle.local_media_resolver(
                    project.store, project.path, self.app.media_roots))
            bundle.collect(refs, depth=depth)
        except (markdown_bundle.BundleError, ValueError) as exc:
            raise ApiError(400, str(exc)) from exc
        if (query.get("check") or [""])[0] in {"1", "true"}:
            # Cheap preflight: the browser cannot show an error from a
            # download, so the UI validates first and reports problems.
            return self._ok(bundle.summary())
        handle = tempfile.NamedTemporaryFile(prefix="ark-bundle-", suffix=".zip", delete=False)
        tmp = Path(handle.name)
        try:
            with handle:
                manifest = bundle.write_zip(handle)
            name = markdown_bundle.bundle_filename(manifest)
            self._stream_zip(tmp, name)
        finally:
            try:
                tmp.unlink()
            except OSError:
                pass

    def _stream_zip(self, path: Path, name: str):
        self.send_response(200)
        self.send_header("Content-Type", "application/zip")
        self.send_header("Content-Length", str(path.stat().st_size))
        self._send_security_headers({
            "Content-Disposition": f'attachment; filename="{name}"; filename*=UTF-8\'\'{quote(name)}',
            "Content-Security-Policy": self.FILE_CSP,
            "Cache-Control": "no-store",
        })
        try:
            self.end_headers()
            if self.command != "HEAD":
                with path.open("rb") as source:
                    shutil.copyfileobj(source, self.wfile, 256 * 1024)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            self.close_connection = True

    def _send_project_archive(self, project: Project, name: str, *, include_sessions: bool = False):
        if include_sessions:
            self.app.sync_session_mirrors(project)
        with tempfile.TemporaryDirectory(prefix="ark-project-export-") as scratch:
            archive = Path(scratch) / "project.zip"
            try:
                write_project_archive(project, archive, include_sessions=include_sessions)
            except (OSError, ValueError, sqlite3.Error) as exc:
                raise ApiError(400, "Could not export project: " + str(exc)) from exc
            self._stream_zip(archive, name + ".ark.zip")

    def _ok(self, data=None, headers=None):
        payload = {"ok": True}
        if isinstance(data, dict):
            payload.update(data)
        elif data is not None:
            payload["data"] = data
        self._send(200, payload, headers=headers)

    def parse_request(self) -> bool:
        # Per request: nothing has read this request's body yet.
        self._body_consumed = False
        return super().parse_request()

    def _error(self, status: int, message: str, code: str | None = None):
        # Early authorization/validation failures may leave a request body
        # unread.  Read it before replying: closing a socket that has unread
        # (or still arriving) data makes the OS send a TCP RST, and the client
        # then loses this response to ConnectionReset/AbortedError.  http.client
        # sends headers and body in separate writes, so the body often arrives
        # after the handler has already decided to refuse the request.
        self._drain_unread_body()
        self.close_connection = True
        self._send(status, {"ok": False, "error": {"status": status,
                                                   "code": code or "error",
                                                   "message": message}}, headers={"Connection": "close"})

    def _stream_chat(self):
        body = self._read_json()
        host = self.headers.get("Host") or ""
        base_url = f"http://{host}" if host else ""
        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Accel-Buffering", "no")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        try:
            for event in self.app.chat_stream(body, base_url=base_url):
                self.wfile.write((json.dumps(event, ensure_ascii=False) + "\n").encode("utf-8"))
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            return
        except Exception as exc:  # headers are already sent
            try:
                self.wfile.write((json.dumps({"type": "error", "message": str(exc)}) + "\n").encode("utf-8"))
                self.wfile.flush()
            except OSError:
                pass

    def _client_gone(self) -> bool:
        """True once the client closed the connection (an aborted fetch):
        the socket turns readable with nothing left to read."""
        try:
            readable, _, _ = select.select([self.connection], [], [], 0)
            if not readable:
                return False
            return self.connection.recv(1, socket.MSG_PEEK) == b""
        except (OSError, ValueError):
            return True

    def _stream_search(self, kind: str, make, query: dict):
        """Stream a Workbench Search as NDJSON: one ``{"type": kind, kind:
        item}`` line per matching node/file as soon as it is found, then
        ``{"type": "done", ...summary, "cancelled": bool}``.  The scan stops
        when the client disconnects or a newer request with the same ``sid``
        arrives.  Gzip-compressed (flushed per line) when accepted."""
        sid = str((query.get("sid") or [""])[0])[:80]
        token = next(_SEARCH_SEQ)
        if sid:
            with _SEARCH_RUNS_LOCK:
                _SEARCH_RUNS[sid] = token
                while len(_SEARCH_RUNS) > 500:
                    _SEARCH_RUNS.pop(next(iter(_SEARCH_RUNS)))
        state = {"checked": 0.0, "gone": False, "cancelled": False}

        def cancelled() -> bool:
            if sid and _SEARCH_RUNS.get(sid) != token:
                state["cancelled"] = True
            else:
                now = time.monotonic()
                if now - state["checked"] >= 0.05:
                    state["checked"] = now
                    state["gone"] = self._client_gone()
                state["cancelled"] = state["gone"]
            return state["cancelled"]

        compressor = None
        headers = {"X-Accel-Buffering": "no", "Connection": "close"}
        if accepts_gzip(self.headers.get("Accept-Encoding") or ""):
            compressor = zlib.compressobj(5, zlib.DEFLATED, 31)
            headers.update({"Content-Encoding": "gzip", "Vary": "Accept-Encoding"})
        self.send_response(200)
        self.send_header("Content-Type", "application/x-ndjson; charset=utf-8")
        self._send_security_headers(headers)
        self.end_headers()
        self.close_connection = True

        def write(event: dict) -> None:
            data = (json.dumps(event, ensure_ascii=False) + "\n").encode("utf-8")
            if compressor:
                data = compressor.compress(data) + compressor.flush(zlib.Z_SYNC_FLUSH)
            self.wfile.write(data)
            self.wfile.flush()

        items = make(cancelled)
        try:
            for item in items:
                if item.get("done"):
                    summary = {key: value for key, value in item.items() if key != "done"}
                    write({"type": "done", **summary, "cancelled": state["cancelled"]})
                    break
                write({"type": kind, kind: item})
            if compressor:
                self.wfile.write(compressor.flush())
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            return
        except Exception as exc:  # noqa: BLE001 - headers are already sent
            try:
                write({"type": "error", "message": str(exc)})
                if compressor:
                    self.wfile.write(compressor.flush())
            except OSError:
                pass
        finally:
            items.close()
            if sid:
                with _SEARCH_RUNS_LOCK:
                    if _SEARCH_RUNS.get(sid) == token:
                        del _SEARCH_RUNS[sid]

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY:
            raise ApiError(413, "Request body is too large")
        self._body_consumed = True
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw.decode("utf-8") or "{}")
        except (ValueError, UnicodeDecodeError) as exc:
            raise ApiError(400, "Request body must be JSON") from exc
        if not isinstance(body, dict):
            raise ApiError(400, "Request body must be a JSON object")
        return body

    def _drain_body(self) -> None:
        """Consume an unused request body so keep-alive framing stays valid."""
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except (TypeError, ValueError):
            return
        if 0 < length <= MAX_BODY and not getattr(self, "_body_consumed", False):
            self._body_consumed = True
            try:
                self.rfile.read(length)
            except OSError:
                pass

    def _drain_unread_body(self) -> None:
        """Before an error reply, read a small unread body (bounded in size and time)."""
        if getattr(self, "_body_consumed", True) or self.command in {"GET", "HEAD"}:
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except (TypeError, ValueError):
            return
        if not 0 < length <= ERROR_DRAIN_LIMIT:
            return  # oversized bodies are refused without being read
        self._body_consumed = True
        previous = self.connection.gettimeout()
        try:
            self.connection.settimeout(ERROR_DRAIN_TIMEOUT)
            self.rfile.read(length)
        except OSError:
            pass
        finally:
            try:
                self.connection.settimeout(previous)
            except OSError:
                pass

    def _read_login_form(self) -> dict:
        """Parse the small login form body only (never the 24 MB API limit)."""
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError as exc:
            raise ApiError(400, "Invalid Content-Length") from exc
        if length > MAX_LOGIN_BODY:
            raise ApiError(413, "Login request body is too large")
        self._body_consumed = True
        raw = self.rfile.read(length) if length else b""
        ctype = (self.headers.get("Content-Type") or "").split(";", 1)[0].strip().lower()
        if ctype == "application/json":
            try:
                body = json.loads(raw.decode("utf-8") or "{}")
            except (ValueError, UnicodeDecodeError) as exc:
                raise ApiError(400, "Login request must be JSON or a form") from exc
            return body if isinstance(body, dict) else {}
        try:
            parsed = parse_qs(raw.decode("utf-8"), keep_blank_values=True)
        except UnicodeDecodeError as exc:
            raise ApiError(400, "Login form must be UTF-8") from exc
        return {key: values[-1] if values else "" for key, values in parsed.items()}

    # ------------------------------------------------------------- auth helpers
    def _auth_token(self) -> str:
        return auth_mod.parse_cookie(self.headers.get("Cookie") or "",
                                     self.app.auth.cookie_name if self.app.auth else auth_mod.COOKIE_NAME)

    def _auth_session(self):
        if not self.app.auth_enabled:
            return None
        token = self._auth_token()
        return self.app.auth.authenticate(token) if token else None

    def _request_scheme(self) -> str:
        # Forwarded headers are attacker-controlled unless the operator opted
        # into a trusted reverse proxy.
        if self.app.trust_proxy:
            if str(self.headers.get("X-Forwarded-Proto") or "").split(",")[0].strip().lower() == "https":
                return "https"
            if str(self.headers.get("X-Forwarded-Ssl") or "").strip().lower() == "on":
                return "https"
        if self.app.public_url.startswith("https://"):
            return "https"
        return "http"

    def _request_host_port(self) -> tuple[str, int]:
        parsed = urlparse("//" + (self.headers.get("Host") or ""))
        try:
            return parsed.hostname or "", parsed.port or int(self.app.port or 0)
        except ValueError:
            return parsed.hostname or "", int(self.app.port or 0)

    def _request_is_secure(self) -> bool:
        return self._request_scheme() == "https"

    def _external_prefix(self) -> str:
        """Browser-facing path prefix (``/`` or ``/ark/``), never host input."""
        if self.app.public_url:
            prefix = auth_mod.cookie_path_from_url(self.app.public_url)
            return prefix if prefix.startswith("/") else "/"
        if self.app.trust_proxy:
            raw = str(self.headers.get("X-Forwarded-Prefix") or "").strip()
            if raw:
                return auth_mod.normalize_cookie_path(raw)
        return "/"

    def _external_path(self) -> str:
        prefix = self._external_prefix()
        path = self.path or "/"
        if not path.startswith("/"):
            path = "/" + path
        if prefix == "/":
            return path
        return prefix.rstrip("/") + path

    def _clean_auth_next(self, raw: str) -> str:
        text = re.sub(r"([?&])mobile_token=[^&#]*", r"\1", str(raw or ""))
        text = re.sub(r"[?&]+(?=#|$)", "", text)
        return auth_mod.safe_next_path(text, default=self._external_prefix())

    def _login_location(self, next_path: str | None = None) -> str:
        prefix = self._external_prefix()
        target = self._external_path() if next_path is None else str(next_path)
        # A mobile token in the query must not be copied into a login URL,
        # browser history or a Referer.  Tokens belong in the header whenever
        # possible; pairing/hash data stays client-side.
        target = self._clean_auth_next(target)
        return f"{prefix}login?next={quote(target, safe='')}"

    def _login_asset_allowed(self, path: str) -> bool:
        return path in LOGIN_ASSETS

    def _task_agent_read_session(self):
        """Resolve a read-only task-agent token to its session (or ``None``)."""
        token = self._read_token()
        if not token:
            return None
        return self.app.sessions.session_for_read_token(token)

    def _tab_project_header(self) -> str:
        """The project id a signed-in browser tab sends with every request
        (``ark_project`` on URLs a browser loads itself: media, downloads)."""
        value = self.headers.get("X-ARK-Project")
        if not value:
            value = (parse_qs(urlparse(self.path).query).get("ark_project") or [""])[0]
        return str(value or "").strip()[:120]

    def _bind_task_agent_context(self, session) -> None:
        ref = session.meta.get("project") or {}
        project_id = str(ref.get("id") or "")
        self.app.set_request_context(
            user=str(session.meta.get("actor") or ""),
            project_id=project_id)

    def _personal_agent_path_allowed(self, path: str) -> bool:
        if path.startswith("/api/agent/v1/") or path in {
                "/api/media/upload", "/api/skills/agent-team/tasks"}:
            return True
        # Existing raw_url values in unit bodies work with a bearer token too.
        return self.command in {"GET", "HEAD"} and (
            path == "/api/export" or re.fullmatch(r"/api/media/\d+/raw", path) is not None)

    def _bind_personal_agent_token(self) -> bool:
        """Authenticate ``/api/agent/v1/*`` with a personal ``arka_`` token.

        The token acts as its owner: the request identity becomes that
        username (never a session), ``X-ARK-Project`` picks the project (the
        owner's default otherwise) and the normal org permission checks apply,
        capped by the token scope.  Personal tokens unlock the agent API plus
        the explicit asset-upload, skill-task and read-only download endpoints.
        Failed attempts count toward the per-client limit.
        """
        path = unquote(urlparse(self.path).path)
        if not self._personal_agent_path_allowed(path):
            return False
        secret = self._agent_token()
        if not agent_tokens_mod.is_personal_token(secret):
            return False
        client = self._client_ip()
        try:
            self.app.agent_token_failures.check(client)
        except agent_tokens_mod.TokenError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        found = self.app.org.agent_tokens.authenticate(secret)
        if found is None or (self.app.auth_enabled and
                             not self.app.org.store.membership(found["user"]["id"],
                                                               self.app.org.org_id)):
            self.app.agent_token_failures.record(client)
            raise ApiError(401, "The agent token is invalid, expired or revoked.",
                           "AGENT_TOKEN_INVALID")
        project_id = ""
        if self.app.auth_enabled:
            project_id = str(self.headers.get("X-ARK-Project") or "").strip()[:120]
        self.app.set_request_context(user=str(found["user"]["username"]),
                                     project_id=project_id,
                                     agent_token=found["token"])
        return True

    def _guard_public_auth(self) -> None:
        """Require a signed-in account for every app/API/static response.

        Exceptions are deliberately tiny: the login page and its own assets,
        health, the auth status endpoint, the mobile status endpoint (which
        redacts project data until signed in), and the one-time pairing claim.
        A QR generated by a signed-in desktop is a single-use sign-in proof;
        claiming it creates the session of the *user who created the code*, never
        of whoever scans it.  Invalid/expired codes are still rejected by the
        pairing manager.
        """
        if self._bind_personal_agent_token():
            return
        if not self.app.auth_enabled:
            return
        path = unquote(urlparse(self.path).path)
        session = self._auth_session()
        if session:
            # Public paths still need the identity when a cookie is present
            # (for example /api/mobile/session reports the signed-in desktop).
            self.app.set_request_context(user=session.username, session=session,
                                         tab_project_id=self._tab_project_header())
        mobile = None if session else self._mobile_session_for_project()
        if mobile is not None:
            # A paired phone has a project-scoped mobile session, not an account
            # session.  It may load the shell and call mobile-gated APIs;
            # _guard_mobile applies read/write/project restrictions next.  The
            # identity is the account that created the pairing code, never the
            # scanner.
            self.app.set_request_context(
                user=mobile.user or "",
                project_id=mobile.project_id,
                mobile_session=mobile)
        if path in PUBLIC_AUTH_PATHS or self._login_asset_allowed(path):
            return
        if session or mobile is not None:
            return
        # A read-only task-agent session token is its own scoped credential; it
        # never becomes an account session, but it may read its own project.
        task_session = self._task_agent_read_session()
        if task_session is not None and path.startswith("/api/task-agent/"):
            self._bind_task_agent_context(task_session)
            return
        if self.command in {"GET", "HEAD"} and not path.startswith("/api/"):
            raise ApiError(302, self._login_location())
        raise ApiError(401, "Sign in with the ARK account to continue.",
                       "AUTH_REQUIRED")

    def _begin_request(self) -> None:
        self.app.clear_request_context()
        self.mobile_session = None

    def _guard_project_scope(self) -> None:
        """Enforce per-project authorization for every project-scoped API.

        ``read`` is required for GET/HEAD, ``write`` for other verbs; specific
        host/settings endpoints additionally demand ``admin``.  Global endpoints
        (health, auth, org admin, personal settings, project list/create/open)
        do their own checks.
        """
        path = unquote(urlparse(self.path).path)
        if not path.startswith("/api/"):
            return
        if path.startswith("/api/task-agent/"):
            return  # read-token gate lives in _guard_public_auth/_require_task_agent
        if path in GLOBAL_API_PATHS or any(path.startswith(prefix)
                                           for prefix in GLOBAL_API_PREFIXES):
            return
        level = "read" if self.command in {"GET", "HEAD"} else "write"
        if re.fullmatch(r"/api/(?:agent/v1/)?units/[^/]+/history(?:/[^/]+)?", path):
            self._node_history_store(level)
            return
        self.app.require(level)

    def _login_error_html(self, message: str) -> str:
        if not message:
            return ""
        return (f'<div class="login-error" role="alert">'
                f'<strong>Sign-in failed.</strong> {html.escape(message)}</div>')

    def _render_login(self, status: int = 200, *, error: str = "",
                      next_path: str = "/", username: str = "") -> None:
        try:
            template = (WEB_ROOT / "login.html").read_text(encoding="utf-8")
        except OSError as exc:
            raise ApiError(500, f"Login page asset is missing: {exc}") from exc
        secure = self._request_is_secure()
        transport = (
            "This page is served over HTTPS."
            if secure else
            "This page is served over plain HTTP. Use an HTTPS reverse proxy "
            "before exposing ARK to the public internet; credentials and the "
            "session cookie are otherwise readable on the network."
        )
        replacements = {
            "{{ERROR}}": self._login_error_html(error),
            "{{NEXT}}": html.escape(auth_mod.safe_next_path(next_path, default="/"), quote=True),
            "{{CSRF}}": html.escape(self.app.auth.login_csrf(), quote=True),
            "{{USERNAME}}": html.escape(str(username or ""), quote=True),
            "{{TRANSPORT}}": html.escape(transport),
        }
        page = template
        for key, value in replacements.items():
            page = page.replace(key, value)
        page = _versioned_html(page, _asset_version())
        headers = {"X-Robots-Tag": "noindex, nofollow"}
        if status == 429:
            retry = getattr(self, "_auth_retry_after", 60)
            headers["Retry-After"] = str(max(1, int(retry)))
        self._send(status, page, "text/html; charset=utf-8", headers)

    def _handle_auth_get(self, path: str) -> bool:
        if path == "/logout":
            prefix = self._external_prefix()
            headers = {}
            if self.app.auth_enabled:
                headers["Set-Cookie"] = self.app.auth.clear_cookie_header(
                    secure=self._request_is_secure())
                self.app.auth.revoke(self._auth_token())
            self._redirect(prefix, headers=headers)
            return True
        if path == "/api/auth/session":
            session = self._auth_session()
            return self._send_auth_session(session)
        if path == "/login":
            if not self.app.auth_enabled:
                self._redirect(self._external_prefix())
                return True
            session = self._auth_session()
            query = parse_qs(urlparse(self.path).query)
            next_path = self._clean_auth_next((query.get("next") or [""])[0])
            if next_path.rstrip("/").endswith("/login"):
                next_path = self._external_prefix()
            if session:
                self._redirect(next_path)
                return True
            self._render_login(200, next_path=next_path)
            return True
        return False

    def _send_auth_session(self, session) -> bool:
        role = None
        org = None
        if self.app.auth_enabled and session:
            role = self.app.org.role_for(session.username)
            row = self.app.org.store.org() or {}
            org = {"id": row.get("id"), "name": row.get("name")}
        payload = {
            "auth_required": bool(self.app.auth_enabled),
            "authenticated": bool(session) or not self.app.auth_enabled,
            "user": (session.username if session else
                     (None if self.app.auth_enabled else org_mod.LOCAL_USERNAME)),
            "expires_at": (
                datetime.fromtimestamp(session.expires_at, tz=timezone.utc)
                .isoformat(timespec="seconds") if session else None
            ),
            "role": role if self.app.auth_enabled else "owner",
            "is_admin": (bool(role and org_mod.ROLE_RANK.get(role, -1)
                              >= org_mod.ROLE_RANK["admin"])
                         if self.app.auth_enabled else True),
            "local": not self.app.auth_enabled,
            "org": org,
        }
        self._ok(payload)
        return True

    def _secure_redirect(self, location: str, *, status: int = 303, headers=None) -> None:
        self.send_response(status)
        self.send_header("Location", location)
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.send_header("Content-Length", "0")
        self.end_headers()

    # Alias used by the login/logout paths; kept separate from ApiError's 302
    # handling because these responses may also set/clear a cookie.
    def _redirect(self, location: str, *, status: int = 303, headers=None) -> None:
        self._secure_redirect(location, status=status, headers=headers)

    def _handle_auth_post(self, path: str) -> bool:
        if path == "/api/auth/logout":
            if not self._same_origin():
                raise ApiError(403, "Cross-origin sign-out is rejected", "AUTH_CSRF")
            self._drain_body()
            revoked = False
            headers = {}
            if self.app.auth_enabled:
                revoked = self.app.auth.revoke(self._auth_token())
                headers["Set-Cookie"] = self.app.auth.clear_cookie_header(
                    secure=self._request_is_secure())
            self._send(200, {"ok": True, "logged_out": revoked}, headers=headers)
            return True
        if path == "/logout":
            if not self._same_origin():
                raise ApiError(403, "Cross-origin sign-out is rejected", "AUTH_CSRF")
            self._drain_body()
            headers = {}
            if self.app.auth_enabled:
                self.app.auth.revoke(self._auth_token())
                headers["Set-Cookie"] = self.app.auth.clear_cookie_header(
                    secure=self._request_is_secure())
            self._redirect(self._external_prefix() + "login", headers=headers)
            return True
        if path != "/login":
            return False
        if not self.app.auth_enabled:
            self._redirect(self._external_prefix())
            return True
        if not self._same_origin():
            self._render_login(403, error="Cross-site sign-in attempts are rejected.",
                               next_path=self._external_prefix())
            return True
        body = self._read_login_form()
        next_path = self._clean_auth_next(str(body.get("next") or ""))
        username = str(body.get("username") or "")
        csrf = str(body.get("csrf") or "")
        if not self.app.auth.verify_login_csrf(csrf):
            self._render_login(
                403,
                error="The sign-in form expired or was not opened from this server. Reload and try again.",
                next_path=next_path, username=username)
            return True
        try:
            remember = str(body.get("remember") or "").lower() in {"1", "on", "true", "yes"}
            token, _session = self.app.auth.login(
                username, str(body.get("password") or ""), client_ip=self._client_ip(),
                remember=remember)
        except auth_mod.AuthError as exc:
            self._auth_retry_after = exc.retry_after or 60
            self._render_login(exc.status, error=exc.message,
                               next_path=next_path, username=username)
            return True
        if next_path.rstrip("/").endswith("/login"):
            next_path = self._external_prefix()
        self._redirect(next_path, status=303, headers={
            "Set-Cookie": self.app.auth.cookie_header(
                token, secure=self._request_is_secure()),
        })
        return True

    def _same_origin(self) -> bool:
        """Reject cross-site state changes using every browser hint available."""
        host = str(self.headers.get("Host") or "").strip().lower()
        origin = self.headers.get("Origin")
        site = str(self.headers.get("Sec-Fetch-Site") or "").strip().lower()
        referer = self.headers.get("Referer")
        if origin:
            if str(origin).strip().lower() == "null":
                # Chromium can serialize a same-origin form navigation as
                # ``Origin: null`` (for example when the response's referrer
                # policy is no-referrer).  Only accept it together with an
                # explicit same-origin Sec-Fetch-Site hint; login additionally
                # requires the signed CSRF token, and cookies are SameSite=Lax.
                if site != "same-origin":
                    return False
            else:
                try:
                    parsed = urlparse(origin)
                except ValueError:
                    return False
                if parsed.scheme not in {"http", "https"}:
                    return False
                if parsed.username or parsed.password:
                    return False
                # Compare hosts, not the full netloc.  A local tunnel may
                # present a different loopback port in Origin, and a reverse
                # proxy may forward a public hostname while the upstream Host
                # is the internal address.  The signed CSRF token still binds
                # the login form to this server.
                origin_host = (parsed.hostname or "").lower()
                request_host = (urlparse("//" + host).hostname or "").lower()
                allowed_hosts = {request_host}
                if self.app.public_url:
                    allowed_hosts.add((urlparse(self.app.public_url).hostname or "").lower())
                if self.app.trust_proxy:
                    forwarded = str(self.headers.get("X-Forwarded-Host") or "").split(",")[0].strip()
                    if forwarded:
                        allowed_hosts.add((urlparse("//" + forwarded).hostname or "").lower())
                if not origin_host or origin_host not in allowed_hosts:
                    return False
        if site and site not in {"same-origin", "none"}:
            return False
        if referer and not origin:
            try:
                parsed = urlparse(referer)
            except ValueError:
                return False
            referer_host = (parsed.hostname or "").lower()
            request_host = (urlparse("//" + host).hostname or "").lower()
            if referer_host != request_host:
                return False
        return True

    def _local_open_cors(self) -> dict:
        """CORS headers for the loopback-only local-folder bridge.

        The bridge is only usable from an explicit origin allowlist, and only
        for paths under an explicit root allowlist.  It never uses project
        scope: the browser is on the user's machine, while the remote ARK
        server is not.
        """
        origin = str(self.headers.get("Origin") or "").strip().rstrip("/")
        allowed = [item.strip().rstrip("/") for item in
                   (os.environ.get("ARK_LOCAL_OPEN_ORIGINS") or "").split(",") if item.strip()]
        if not origin or origin not in allowed:
            return {}
        return {
            "Access-Control-Allow-Origin": origin,
            "Access-Control-Allow-Methods": "POST, OPTIONS",
            "Access-Control-Allow-Headers": "Content-Type",
            "Access-Control-Allow-Private-Network": "true",
            "Vary": "Origin",
        }

    def _local_open_roots(self) -> list[Path]:
        raw = os.environ.get("ARK_LOCAL_OPEN_ROOTS") or ""
        roots = []
        for item in raw.split(os.pathsep):
            item = item.strip().strip('"')
            if item:
                try:
                    roots.append(Path(item).expanduser().resolve())
                except OSError:
                    pass
        return roots

    def _handle_local_open(self) -> None:
        client = str((self.client_address or ("",))[0] or "")
        try:
            if not ipaddress.ip_address(client).is_loopback:
                raise ValueError
        except ValueError:
            self._send(403, {"ok": False, "error": {"status": 403,
                       "message": "Local open is loopback-only"}})
            return
        cors = self._local_open_cors()
        if not cors:
            self._send(403, {"ok": False, "error": {"status": 403,
                       "message": "Local open origin is not allowed"}},
                       headers={"Vary": "Origin"})
            return
        body = self._read_json() or {}
        raw = str(body.get("path") or "").strip()
        if not raw:
            self._send(400, {"ok": False, "error": {"status": 400,
                       "message": "path is required"}}, headers=cors)
            return
        try:
            target = Path(raw).expanduser().resolve()
        except OSError as exc:
            self._send(400, {"ok": False, "error": {"status": 400,
                       "message": str(exc)}}, headers=cors)
            return
        roots = self._local_open_roots()
        if not roots:
            self._send(403, {"ok": False, "error": {"status": 403,
                       "message": "ARK_LOCAL_OPEN_ROOTS is not configured"}}, headers=cors)
            return
        if not any(target == root or root in target.parents for root in roots):
            self._send(403, {"ok": False, "error": {"status": 403,
                       "message": "Path is outside ARK_LOCAL_OPEN_ROOTS"}}, headers=cors)
            return
        if not target.exists():
            self._send(404, {"ok": False, "error": {"status": 404,
                       "message": "Path does not exist on this computer"}}, headers=cors)
            return
        try:
            result = platform_support.open_external(target)
        except platform_support.PlatformError as exc:
            self._send(exc.status, {"ok": False, "error": {"status": exc.status,
                       "message": str(exc)}}, headers=cors)
            return
        self._send(200, {"ok": True, "path": str(target), "mode": "local", **(result or {})},
                   headers=cors)

    def _embed_media(self, body: dict, actor: str) -> dict:
        """Store one embedded image under the active project's assets."""
        unit_ref = body.get("unit")
        filename = str(body.get("filename") or "asset").strip()
        encoded = str(body.get("data") or "")
        try:
            raw = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error):
            raise ApiError(400, "media upload data is not valid base64")
        if not raw:
            raise ApiError(400, "media upload is empty")
        if len(raw) > 12 * 1024 * 1024:
            raise ApiError(413, "embedded media must be 12 MB or smaller")
        suffix = Path(filename).suffix.lower()
        allowed = {".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp"}
        if suffix not in allowed:
            raise ApiError(400, "only jpg, jpeg, png, gif, svg and webp files can be embedded")
        active_project = self.app.require()
        folder = active_project.path / "assets" / "media"
        folder.mkdir(parents=True, exist_ok=True)
        name = f"{uuid.uuid4().hex}{suffix}"
        target = folder / name
        target.write_bytes(raw)
        url = f"assets/media/{name}"
        return active_project.store.add_media(
            unit_ref, url, str(body.get("kind") or "image"),
            body.get("caption") or filename, actor=actor)

    def _upload_asset(self, body: dict, actor: str) -> dict:
        """Store any project asset (Markdown, PDF, data, image, ...)."""
        unit_ref = body.get("unit")
        filename = str(body.get("filename") or "asset").strip()
        original = Path(filename).name[:180] or "asset"
        encoded = str(body.get("data") or "")
        try:
            raw = base64.b64decode(encoded, validate=True)
        except (ValueError, binascii.Error):
            raise ApiError(400, "asset upload data is not valid base64")
        if not raw:
            raise ApiError(400, "asset upload is empty")
        if len(raw) > 64 * 1024 * 1024:
            raise ApiError(413, "asset must be 64 MB or smaller")
        suffix = Path(original).suffix.lower()[:24]
        safe_original = re.sub(r"[^A-Za-z0-9._-]+", "_", original)
        if not safe_original or safe_original.startswith("."):
            safe_original = "asset"
        active_project = self.app.require()
        folder = active_project.path / "assets" / "files"
        folder.mkdir(parents=True, exist_ok=True)
        name = f"{uuid.uuid4().hex}-{safe_original}"
        target = folder / name
        target.write_bytes(raw)
        image_ext = {".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif"}
        video_ext = {".mp4", ".webm", ".mov", ".mkv"}
        audio_ext = {".mp3", ".wav", ".ogg", ".m4a"}
        kind = str(body.get("kind") or "").strip()
        if not kind:
            kind = ("image" if suffix in image_ext else "video" if suffix in video_ext
                    else "audio" if suffix in audio_ext else "document")
        return active_project.store.add_media(
            unit_ref, f"assets/files/{name}", kind,
            body.get("caption") or original, actor=actor)

    def do_OPTIONS(self):
        path = urlparse(self.path).path
        if path != "/api/local/open":
            self._send(404, {"ok": False, "error": {"status": 404, "message": "Unknown endpoint"}})
            return
        cors = self._local_open_cors()
        if not cors:
            self._send(403, {"ok": False, "error": {"status": 403,
                       "message": "Local open origin is not allowed"}}, headers={"Vary": "Origin"})
            return
        self._send(204, b"", headers=cors)

    def _check_host(self) -> None:
        host = self.headers.get("Host") or ""
        if not self.app.host_allowed(host):
            raise ApiError(403, "Unexpected Host header", "HOST_REJECTED")

    def _client_ip(self) -> str:
        if getattr(self.app, "trust_proxy", False):
            forwarded = str(self.headers.get("X-Forwarded-For") or "").split(",")[0].strip()
            if forwarded:
                try:
                    return str(ipaddress.ip_address(forwarded.split("%")[0]))
                except ValueError:
                    pass
        address = self.client_address[0] if self.client_address else ""
        return str(address or "")

    def _host_header_host(self) -> str:
        parsed = urlparse("//" + (self.headers.get("Host") or ""))
        return parsed.hostname or ""

    def _trusted_local(self) -> bool:
        """A real desktop browser on this machine (both socket and Host local).

        A phone reaching ARK through an SSH local forward arrives from
        127.0.0.1 too, but its Host header is the tunnel computer's address.
        Requiring *both* to be loopback keeps the desktop UI open without
        letting tunneled browsers bypass pairing.
        """
        remote = self._client_ip()
        try:
            remote_loopback = ipaddress.ip_address(remote.split("%")[0]).is_loopback
        except ValueError:
            remote_loopback = mobile.is_loopback_host(remote)
        return bool(remote_loopback and mobile.is_loopback_host(self._host_header_host()))

    def _mobile_token(self, *, allow_query: bool = False) -> str:
        token = (self.headers.get("X-ARK-Mobile-Token") or "").strip()
        if token:
            return token
        if allow_query:
            # Browser-owned media/export URLs cannot set a header.  Query tokens
            # are accepted for read-only resources only, never for mutations.
            parsed = urlparse(self.path)
            query = parse_qs(parsed.query)
            token = (query.get("mobile_token") or [""])[0].strip()
            if token:
                return token
        # The paired-device cookie lets the app shell and browser-owned media
        # requests authenticate before localStorage is available.
        return auth_mod.parse_cookie(self.headers.get("Cookie") or "", "ark_mobile")

    def _mobile_session_for_project(self):
        """Live paired-device session for an accessible project (or ``None``).

        A paired device is not an account session.  It only works while its
        bound user still exists and still has read access to the paired
        project.
        """
        token = self._mobile_token(allow_query=self.command in {"GET", "HEAD"})
        if not token:
            return None
        session = self.app.mobile.session_for_token(token)
        if not session:
            return None
        project = self.app.project_by_id(session.project_id)
        if project is None:
            return None
        if self.app.auth_enabled:
            user = str(session.user or "")
            if not user:
                return None
            try:
                self.app.org.require_project_permission(user, project.id, "read")
            except org_mod.OrgError:
                return None
        return session

    def _guard_mobile(self) -> None:
        """Authenticate/scoping gate for API requests, including via a tunnel.

        Static assets stay public so the shell can load before pairing.  Every
        API call from a non-local browser requires a live mobile token; a token
        supplied from localhost is still enforced (read-only, project scope).
        A mobile session is bound to the account that created its pairing code
        and to the one project the code was issued for, so a phone can never
        reach a different user's project.
        """
        path = unquote(urlparse(self.path).path)
        if not path.startswith("/api/"):
            return
        if path in {"/api/health", "/api/mobile/pair/claim", "/api/mobile/login"}:
            return
        if self.app.request_agent_token() is not None and self._personal_agent_path_allowed(path):
            return  # personal bearer token, bound in _bind_personal_agent_token
        if path.startswith("/api/task-agent/") and \
                self._task_agent_read_session() is not None:
            return
        if path == "/api/mobile/session" and self.command != "DELETE":
            return
        token = self._mobile_token(allow_query=self.command in {"GET", "HEAD"})
        mobile_session = None
        project = None
        if token:
            mobile_session = self.app.mobile.session_for_token(token)
            if not mobile_session:
                raise ApiError(401, "The mobile session is invalid or has expired.",
                               "MOBILE_TOKEN_INVALID")
            project = self.app.project_by_id(mobile_session.project_id)
            if project is None:
                raise ApiError(404, "The paired project is no longer available.",
                               "MOBILE_WRONG_PROJECT")
        session = self._auth_session()
        local = self._trusted_local()
        management = (path == "/api/mobile/connection"
                      or path == "/api/mobile/pair"
                      or path == "/api/mobile/pairs"
                      or path.startswith("/api/mobile/pairs/"))
        if management:
            # A signed-in account may create/revoke QR codes from any browser,
            # including the public/server deployment.  A paired device itself
            # may not manage pairings, and anonymous remote callers cannot.
            if mobile_session is not None:
                raise ApiError(
                    403,
                    "A paired phone cannot manage pairing codes.",
                    "MOBILE_SCOPE",
                )
            if session is None and not local:
                raise ApiError(401, "Sign in to manage pairing codes.",
                               "AUTH_REQUIRED")
            return
        if path == "/api/mobile/session" and self.command == "DELETE":
            if mobile_session is None:
                raise ApiError(401, "The mobile session is invalid or has expired.",
                               "MOBILE_TOKEN_INVALID")
            self.mobile_session = mobile_session
            self.app.set_request_context(
                user=mobile_session.user or "", session=session,
                project_id=mobile_session.project_id,
                mobile_session=mobile_session)
            return
        if mobile_session is not None:
            try:
                mobile_session = self.app.mobile.authenticate(
                    token, project.summary(), method=self.command, path=path)
            except mobile.MobileError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
            self.mobile_session = mobile_session
        if mobile_session is not None:
            user = (mobile_session.user
                    or (session.username if session else "")
                    or (self.app.auth.username if self.app.auth_enabled else ""))
            if session is not None and user and \
                    session.username.lower() != str(user).lower():
                raise ApiError(403, "This mobile session belongs to another account.",
                               "PAIRING_WRONG_USER")
            # A paired phone acts as the account that created its pairing code
            # and is pinned to the paired project; a read-only pairing narrows
            # that account's project access to read.
            self.app.set_request_context(
                user=user, session=session,
                project_id=mobile_session.project_id,
                mobile_session=mobile_session)
        elif session is not None:
            self.app.set_request_context(user=session.username, session=session,
                                         tab_project_id=self._tab_project_header())
        if mobile_session is not None:
            if path == "/api/agent/token":
                raise ApiError(
                    403,
                    "The desktop agent token is never exposed to paired phones.",
                    "MOBILE_SECRET_BLOCKED",
                )
            if path == "/api/open":
                raise ApiError(
                    403,
                    "A paired phone cannot open files or applications on the host.",
                    "MOBILE_HOST_ACTION_BLOCKED",
                )
            if path == "/api/projects/open":
                raise ApiError(
                    403,
                    "A paired phone stays on the project it was paired with.",
                    "MOBILE_WRONG_PROJECT",
                )
            if not mobile_device_path_allowed(path, self.command):
                raise ApiError(
                    403,
                    "This API is not available to a paired device. Use the "
                    "desktop ARK window to manage the organization, personal "
                    "credentials or providers.",
                    "MOBILE_SCOPE",
                )
            return
        if session is not None:
            return
        if not local:
            raise ApiError(
                401,
                "Pair this phone from ARK Settings → Mobile on the desktop. "
                "The pairing link was opened without its one-time code.",
                "MOBILE_TOKEN_REQUIRED",
            )

    def _agent_token(self) -> str:
        auth = self.headers.get("Authorization") or ""
        if auth.lower().startswith("bearer "):
            return auth[7:].strip()
        return self.headers.get("X-ARK-Token") or ""

    def _read_token(self) -> str:
        auth = self.headers.get("Authorization") or ""
        if auth.lower().startswith("bearer "):
            return auth[7:].strip()
        return self.headers.get("X-ARK-Read-Token") or ""

    def _require_agent(self) -> None:
        import hmac
        if self.app.request_agent_token() is not None:
            return  # personal token, already verified for this request
        token = self._agent_token()
        expected = self.app.manager.agent_token
        client = self._client_ip()
        try:
            self.app.agent_token_failures.check(client)
        except agent_tokens_mod.TokenError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        if not token or not hmac.compare_digest(token, expected):
            if token:
                self.app.agent_token_failures.record(client)
            raise ApiError(401, "A valid agent token is required")

    def _require_task_agent(self):
        """Resolve a read-only task-agent session token.

        These tokens are distinct from the write token and only ever unlock the
        GET-only ``/api/task-agent/v1`` namespace.
        """
        token = self._read_token()
        session = self.app.sessions.session_for_read_token(token)
        if not session:
            raise ApiError(401, "A valid read-only task-agent token is required")
        return session

    def _reject_task_read_token(self, action: str) -> None:
        """A task agent must never use its read token to approve itself."""
        for candidate in {self._read_token(), self._agent_token()}:
            if candidate and self.app.sessions.session_for_read_token(candidate):
                raise ApiError(403, f"Task-agent read tokens cannot {action}")

    # ------------------------------------------------------ account profile
    _AVATAR_PATH_RE = re.compile(r"^/api/users/([^/]+)/avatar$")

    def _handle_profile(self, method: str, path: str) -> bool:
        """``/api/me`` (profile, avatar, password) and avatar images.

        Same guards as every UI request already ran (account session or
        paired device, Host, project scope); writes are additionally
        same-origin only (checked by the caller).  A task-agent read token is
        never an account and is refused outright.
        """
        avatar_match = self._AVATAR_PATH_RE.match(path)
        if path not in {"/api/me", "/api/me/avatar", "/api/me/password"} and not avatar_match:
            return False
        self._reject_task_read_token("read or change account profiles")
        username = self.app.actor()
        if not username:
            raise ApiError(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
        org = self.app.org
        try:
            if avatar_match:
                if method not in {"GET", "HEAD"}:
                    raise ApiError(405, "Method not allowed")
                found = org.avatar_file(unquote(avatar_match.group(1)))
                if not found:
                    raise ApiError(404, "no avatar image", "AVATAR_NOT_FOUND")
                file_path, ctype = found
                data = file_path.read_bytes()
                cache = ("private, max-age=31536000, immutable"
                         if "v=" in (urlparse(self.path).query or "") else "private, no-cache")
                self._send(200, data, ctype, headers={
                    "Cache-Control": cache,
                    "Content-Disposition": 'inline; filename="avatar"',
                    "Content-Security-Policy": "default-src 'none'; sandbox",
                })
                return True
            if path == "/api/me" and method in {"GET", "HEAD"}:
                self._ok(self.app.profile_payload())
                return True
            if path == "/api/me" and method == "PATCH":
                org.update_profile(username, self._read_json())
                self._ok(self.app.profile_payload())
                return True
            if path == "/api/me/avatar" and method == "POST":
                length = int(self.headers.get("Content-Length") or 0)
                if length > org_mod.MAX_AVATAR_BYTES * 2:
                    raise ApiError(413, "avatar image is larger than "
                                        f"{org_mod.MAX_AVATAR_BYTES // 1024} KB",
                                   "AVATAR_TOO_LARGE")
                body = self._read_json()
                encoded = str(body.get("data") or "")
                if "," in encoded and encoded.startswith("data:"):
                    encoded = encoded.split(",", 1)[1]
                try:
                    raw = base64.b64decode(encoded, validate=True)
                except (binascii.Error, ValueError) as exc:
                    raise ApiError(400, "avatar data must be base64", "AVATAR_INVALID") from exc
                org.set_avatar_image(username, raw, str(body.get("content_type") or ""))
                self._ok(self.app.profile_payload())
                return True
            if path == "/api/me/avatar" and method == "DELETE":
                org.remove_avatar(username)
                self._ok(self.app.profile_payload())
                return True
            if path == "/api/me/password" and method == "POST":
                self._change_own_password()
                return True
        except org_mod.OrgError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        raise ApiError(405, "Method not allowed")

    def _change_own_password(self) -> None:
        if not self.app.auth_enabled:
            self._drain_body()
            raise ApiError(409, "This local ARK has no sign-in password to change.",
                           "LOCAL_MODE")
        if getattr(self, "mobile_session", None) is not None:
            raise ApiError(403, "Change your password from a signed-in browser.",
                           "MOBILE_SCOPE")
        session = self._auth_session()
        if session is None:
            raise ApiError(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
        body = self._read_json()
        current = str(body.get("current_password") or "")
        new = str(body.get("new_password") or "")
        confirm = str(body.get("confirm_password") or "")
        if not current or not new:
            raise ApiError(400, "Enter your current password and a new password.",
                           "PASSWORD_REQUIRED")
        if confirm != new:
            raise ApiError(400, "The new passwords do not match.", "PASSWORD_MISMATCH")
        if new == current:
            raise ApiError(400, "The new password must differ from the current one.",
                           "PASSWORD_UNCHANGED")
        if len(new) < MIN_SELF_SERVICE_PASSWORD:
            raise ApiError(400, f"The new password must be at least "
                                f"{MIN_SELF_SERVICE_PASSWORD} characters.",
                           "PASSWORD_TOO_SHORT")
        try:
            # Same per-IP/per-user failure limits as the sign-in form.
            username = self.app.auth.verify_credentials(
                session.username, current, client_ip=self._client_ip())
        except auth_mod.AuthError as exc:
            if exc.status == 429:
                self._send(429, {"ok": False, "error": {
                    "status": 429, "code": exc.code, "message": exc.message}},
                    headers={"Retry-After": str(max(1, int(exc.retry_after or 60)))})
                return
            # 403, not 401: the browser session itself is still valid.
            raise ApiError(403, "The current password is incorrect.",
                           "CURRENT_PASSWORD_INVALID") from exc
        try:
            new_hash = auth_mod.hash_password(new)
        except auth_mod.AuthConfigError as exc:
            raise ApiError(400, str(exc), "PASSWORD_INVALID") from exc
        self.app.org.set_password_hash(username, new_hash)
        # Every other browser session (including persisted "keep me signed
        # in" sessions) and every paired device of this account sign out;
        # the browser that made the change stays signed in.
        revoked = self.app.auth.revoke_user(username, except_sid=session.sid_hash)
        devices = self.app.mobile.revoke_user(username)
        # Personal agent tokens survive a password change unless the user
        # ticked "also revoke my agent tokens".
        tokens = (self.app.org.revoke_user_tokens(username)
                  if body.get("revoke_agent_tokens") is True else 0)
        self._ok({"changed": True, "sessions_revoked": revoked,
                  "devices_revoked": devices, "agent_tokens_revoked": tokens})

    def _actor(self) -> str:
        name = (self.headers.get("X-Agent-Name") or "").strip()
        personal = self.app.request_agent_token()
        if personal is not None:
            # A personal token acts as its owner: agent:<username>[/<agent name>].
            user = self.app.actor()
            return "agent:" + user + ("/" + name[:40] if name else "")
        if name:
            return "agent:" + name[:60]
        if self._agent_token():
            return "agent:api"
        return "human:ui"

    # -------------------------------------------------------------------- GET
    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        try:
            self._begin_request()
            self._check_host()
            parsed = urlparse(self.path)
            path = unquote(parsed.path)
            query = parse_qs(parsed.query)
            if self._handle_auth_get(path):
                return
            self._guard_public_auth()
            self._guard_mobile()
            self._guard_project_scope()
            if path.startswith("/api/"):
                return self._api_get(path, query)
            apk = next((item for item in ANDROID_APKS.values() if path in item["paths"]), None)
            if apk:
                target = WEB_ROOT.parent / "dist" / apk["file"]
                if not target.is_file():
                    raise ApiError(404, f"{apk['title']} is not available on this server.",
                                   "APK_NOT_FOUND")
                return self._send(
                    200, target.read_bytes(),
                    "application/vnd.android.package-archive",
                    {"Content-Disposition": f'attachment; filename="{apk["file"]}"',
                     "Cache-Control": "no-store"},
                )
            return self._static(path)
        except ApiError as exc:
            if exc.status == 302:
                self._redirect(exc.message, status=302)
            else:
                self._error(exc.status, exc.message, getattr(exc, "code", None) or None)
        except gpu_probe.GpuError as exc:
            self._error(exc.status, exc.message, exc.code)
        except workspace_files.WorkspaceError as exc:
            self._error(exc.status, str(exc))
        except WorktreeError as exc:
            self._error(exc.status, str(exc), exc.code)
        except SessionError as exc:
            self._error(exc.status, str(exc), getattr(exc, "code", "") or None)
        except (StoreError, ProjectError) as exc:
            self._error(400, str(exc))
        except skills.SkillError as exc:
            self._error(404, str(exc))
        except Exception as exc:  # noqa: BLE001 - report a clean API error
            if os.environ.get("ARK_DEBUG"):
                import traceback
                traceback.print_exc()
            self._error(500, f"Internal error: {exc}")

    def _first_infohub_path(self) -> str:
        candidates = importer.default_candidates()
        if not candidates:
            raise ApiError(400, "No InfoHub database found; set INFOHUB_DB or pass path")
        return candidates[0]["path"]

    def _api_get(self, path: str, query: dict):
        if self._handle_profile(self.command, path):
            return
        if path == "/api/health":
            payload = {"app": "arkstudio", "version": "2.0"}
            if self.app.auth_enabled:
                payload["auth_required"] = True
            else:
                payload["root"] = str(self.app.root)
            return self._ok(payload)
        if path == "/api/bootstrap":
            return self._ok(self.app.bootstrap())
        if path == "/api/projects":
            projects = self.app.visible_projects()
            if getattr(self, "mobile_session", None) is not None:
                # A paired device never enumerates other projects.
                current = self.app.current_project()
                projects = [item for item in projects
                            if current is not None and item.get("id") == current.id]
            return self._ok({"projects": projects,
                             "session": self.app.session_payload()})
        if path == "/api/org" or path == "/api/org/":
            if getattr(self, "mobile_session", None) is not None:
                return self._ok(self.app.mobile_org_overview())
            return self._ok(self.app.org.org_overview(self.app.actor()))
        if path == "/api/org/users":
            self._require_org_admin()
            return self._ok({"users": self.app.org.list_members(),
                             "linux_accounts": self.app.org.store.linux_accounts_map()})
        if path == "/api/org/teams":
            return self._ok({"teams": self.app.org.list_teams()})
        if path == "/api/org/audit":
            self._require_org_admin()
            return self._ok({"audit": self.app.org.store.list_audit(
                _int((query.get("limit") or ["100"])[0], 100, 1, 500))})
        m = re.match(r"^/api/org/users/([^/]+)/linux-accounts$", path)
        if m:
            self._require_org_admin()
            target = self.app.org.store.user_by_id(m.group(1))
            if not target:
                raise ApiError(404, "user not found", "USER_NOT_FOUND")
            return self._ok({
                "user": self.app.org.user_public(target),
                "linux_accounts": [row["account"] for row in
                                   self.app.org.store.linux_accounts(target["id"])],
                "capability": linux_account.available(),
            })
        m = re.match(r"^/api/org/projects/([^/]+)$", path)
        if m:
            try:
                project = self.app.org.project_detail(self.app.actor(), m.group(1))
            except org_mod.OrgError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
            return self._ok({"project": project})
        if path == "/api/personal/agent-tokens":
            user = self._personal_agent_tokens_guard()
            try:
                return self._ok(self.app.org.list_agent_tokens(user))
            except org_mod.OrgError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
        if path == "/api/personal/settings":
            scope = (query.get("scope") or ["assistant"])[0]
            try:
                if scope == "environment":
                    settings = self.app.org.personal_environment_public(
                        self.app.actor())
                    return self._ok({"scope": scope, "settings": settings})
                settings = self.app.org.personal_settings_public(
                    self.app.actor(), scope)
            except org_mod.OrgError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
            return self._ok({"scope": scope, "settings": settings,
                             "effective": self.app.effective_settings_payload()})
        if path == "/api/settings/effective":
            return self._ok(self.app.effective_settings_payload())
        if path == "/api/project":
            return self._ok(self.app.project_payload())
        if path == "/api/project/overview":
            project = self.app.require("read")
            setup = self._setup_payload(project, self._setup_viewer(project, query=query))
            data = self.app.project_overview(setup_state=setup)
            # Diagnostic timings change on every call; the validator ignores them.
            volatile = {key: data.pop(key) for key in ("timing", "generated_ms", "store_ms")
                        if key in data}
            etag = 'W/"' + hashlib.blake2b(json.dumps(data, sort_keys=True, ensure_ascii=False)
                                           .encode("utf-8"), digest_size=12).hexdigest() + '"'
            data.update(volatile)
            return self._ok(data, headers={"ETag": etag})
        if path == "/api/project/assignees":
            return self._ok(self.app.assignable_users(self.app.require("read")))
        if path == "/api/blackbox":
            project = self.app.require("read")
            return self._ok({
                "enabled": self.app.blackbox.enabled,
                "snapshots": self.app.blackbox.list(project),
            })
        if path == "/api/project/settings":
            return self._ok({"settings": project_settings(self.app.require().manifest)})
        if path == "/api/chat":
            return self._ok(self.app.chat_state(_int((query.get("limit") or ["100"])[0], 100, 1, 400)))
        if path == "/api/chat/status":
            return self._ok(chat.harness_status())
        if path == "/api/search/nodes":
            # Workbench Search over node titles, bodies, tags and comments
            # (``stream=1``: NDJSON, see _stream_search).
            store, text, options = self.app.require().store, (query.get("q") or [""])[0], _search_options(query)
            try:
                if _bool((query.get("stream") or ["0"])[0]):
                    search_mod.build_matcher(text, **options)
                    return self._stream_search("node", lambda cancelled: search_mod.iter_search_nodes(
                        store, text, cancelled=cancelled, **options), query)
                return self._ok(search_mod.search_nodes(store, text, **options))
            except search_mod.SearchError as exc:
                raise ApiError(400, str(exc)) from exc
        if path == "/api/revision":
            store = self.app.require().store
            return self._ok({"revision": store.revision(), "cursor": store.change_cursor()})
        if path == "/api/events":
            return self._event_stream(query)
        if path == "/api/changes":
            store = self.app.require().store
            since = (query.get("since") or [""])[0]
            bodies = (query.get("bodies") or ["1"])[0] not in {"0", "false", "no"}
            return self._ok(store.changes(since, bodies=bodies))
        if path == "/api/unit-details":
            store = self.app.require().store
            refs = [ref for ref in _qlist(query, "ids") if ref][:UNIT_DETAILS_LIMIT]
            units, missing = [], []
            cursor = store.change_cursor()
            for ref in refs:
                try:
                    units.append(store.get_unit(ref))
                except StoreError:
                    missing.append(ref)
            # Per unit, the validator GET /api/units/{id} would send for the
            # same bytes, so a cached detail revalidates with a 304 later.
            etags = {str(unit["id"]): 'W/"' + hashlib.blake2b(json.dumps(
                {"ok": True, "unit": unit}, ensure_ascii=False).encode("utf-8"),
                digest_size=12).hexdigest() + '"' for unit in units}
            return self._ok({"units": units, "missing": missing, "cursor": cursor,
                             "etags": etags})
        if path == "/api/remote/info":
            return self._ok(self.app.remote_info(self.headers.get("Host") or ""))
        if path == "/api/mobile/apps":
            return self._ok({"apps": android_apps()})
        if path == "/api/mobile/connection":
            return self._ok(self.app.mobile_connection(
                self.headers.get("Host") or "", request_scheme=self._request_scheme()))
        if path == "/api/mobile/pairs":
            return self._ok({"pairs": self.app.mobile.list(self.app.require().summary())})
        if path == "/api/mobile/session/projects":
            return self._ok(self._mobile_projects())
        if path == "/api/mobile/session":
            if (self.app.auth_enabled and not self._auth_session()
                    and self._mobile_session_for_project() is None):
                # Safe pre-login status: no project name, URLs or secrets.  The
                # phone's login page is the only surface before this point.
                return self._ok({
                    "required": True,
                    "authenticated": False,
                    "token_present": bool(self._mobile_token()),
                    "reason": "login_required",
                    "login_required": True,
                    "permission": None,
                    "read_only": None,
                    "expires_at": None,
                    "project": None,
                    "auth_required": True,
                    "auth_user": None,
                    "cache_id": None,
                    "cache_schema": CLIENT_CACHE_SCHEMA,
                    "connection": {"mode": "public", "loopback_only": False,
                                   "public_url": None, "advertised_urls": [],
                                   "auth_required": True},
                })
            # A signed-in account on a public deployment is a desktop
            # session; it does not need a phone pairing token.
            required = not self._trusted_local() and self._auth_session() is None
            return self._ok(self.app.mobile_status(
                self._mobile_token(), required=required,
                host_header=self.headers.get("Host") or "",
                request_scheme=self._request_scheme()))
        if path == "/api/auth/session":
            return self._send_auth_session(self._auth_session())
        if path == "/api/gpu/status":
            return self._ok(self.app.gpu.status())
        if path == "/api/gpu/servers":
            return self._ok(self.app.gpu.servers())
        if path == "/api/gpu/queue":
            return self._ok(self.app.gpu.queue_status())
        if path == "/api/resources/graph":
            return self._ok(self.app.resources_graph())
        if path == "/api/gpu/dashboard":
            project = self.app.require("read")
            return self._ok(self.app.gpu.dashboard(project=project.id))
        if path == "/api/gpu/jobs":
            self.app.require("read")
            query = parse_qs(urlparse(self.path).query)
            return self._ok(self.app.gpu.queue_list(
                project=self.app.require().id,
                **_gpu_list_query(query)))
        m = re.match(r"^/api/gpu/jobs/([^/]+)$", path)
        if m:
            return self._ok({"job": self._project_gpu_job(m.group(1))})
        m = re.match(r"^/api/gpu/jobs/([^/]+)/logs$", path)
        if m:
            self._project_gpu_job(m.group(1))
            return self._ok(self.app.gpu.queue_logs(m.group(1), limit=_gpu_tail_query(query)))
        if path == "/api/assistant/config":
            return self._ok(self._assistant_config_payload())
        if path == "/api/providers":
            return self._ok(self.app.providers.snapshot())
        if path == "/api/worktrees":
            return self._ok(self._worktrees_payload())
        if path == "/api/skills":
            return self._ok(skills.catalog_payload())
        if path.startswith("/api/skills/"):
            return self._skills_get(path, query)
        if path.startswith("/api/agent/v1/"):
            self._require_agent()
            return self._agent_get(path, query)
        if path == "/api/agent/token":
            if self.app.auth_enabled:
                self.app.require("admin")
            return self._ok({"token": self.app.manager.agent_token})
        if path == "/api/units":
            filters = filters_from_query(query, me=self.app.actor())
            filters["limit"] = _int((query.get("limit") or ["200"])[0], 200, 1, 1000)
            filters["offset"] = _int((query.get("offset") or ["0"])[0], 0, 0, 1_000_000)
            filters["ids"] = [int(x) for x in _qlist(query, "ids") if x.isdigit()]
            project = self.app.require()
            facets = bool(query.get("facets", ["1"])[0] != "0")
            return self._ok_revisioned(
                project, lambda: project.store.list_units(filters, include_facets=facets))
        if path == "/api/graph/snapshot":
            project = self.app.require()
            bodies = (query.get("bodies") or ["1"])[0] not in {"0", "false", "no"}
            return self._ok_revisioned(project, lambda: project.store.graph_snapshot(bodies=bodies))
        if path == "/api/graph/bodies":
            project = self.app.require()
            return self._ok_revisioned(project, project.store.graph_bodies)
        if path == "/api/graph":
            filters = filters_from_query(query, me=self.app.actor())
            context = (query.get("context") or ["0"])[0] in {"1", "true", "yes"}
            data = self.app.require().store.graph(
                filters, context=context, slim=True,
                limit=_int((query.get("limit") or ["700"])[0], 700, 10, 1200))
            return self._ok(data)
        if path == "/api/infohub/candidates":
            return self._ok({"candidates": importer.default_candidates()})
        if path == "/api/infohub/browse":
            source_path = (query.get("path") or [""])[0] or self._first_infohub_path()
            src = importer.InfoHubSource(source_path)
            try:
                data = src.browse(
                    self.app.require().store,
                    q=(query.get("q") or [""])[0],
                    kind=(query.get("kind") or [""])[0],
                    tags=(query.get("tags") or [""])[0],
                    status=(query.get("status") or [""])[0],
                    days=_float((query.get("days") or ["0"])[0], 0, 0, 36500),
                    mine=_bool((query.get("mine") or ["0"])[0]),
                    roots=_bool((query.get("roots") or ["0"])[0]),
                    order=(query.get("order") or ["activity"])[0],
                    limit=_int((query.get("limit") or ["60"])[0], 60, 1, 1000),
                    offset=_int((query.get("offset") or ["0"])[0], 0, 0, 100_000),
                )
            finally:
                src.close()
            return self._ok(data)
        if path == "/api/infohub/unit":
            source_path = (query.get("path") or [""])[0] or self._first_infohub_path()
            key = (query.get("key") or query.get("id") or [""])[0]
            if not key:
                raise ApiError(400, "key is required")
            src = importer.InfoHubSource(source_path)
            try:
                data = src.unit(key, self.app.require().store)
            finally:
                src.close()
            return self._ok({"unit": data})
        if path == "/api/infohub/graph":
            source_path = (query.get("path") or [""])[0] or self._first_infohub_path()
            src = importer.InfoHubSource(source_path)
            try:
                data = src.graph(
                    self.app.require().store,
                    q=(query.get("q") or [""])[0],
                    kind=(query.get("kind") or [""])[0],
                    tags=(query.get("tags") or [""])[0],
                    status=(query.get("status") or [""])[0],
                    days=_float((query.get("days") or ["0"])[0], 0, 0, 36500),
                    mine=_bool((query.get("mine") or ["0"])[0]),
                    roots=_bool((query.get("roots") or ["0"])[0]),
                    order=(query.get("order") or ["activity"])[0],
                    limit=_int((query.get("limit") or ["160"])[0], 160, 1, 1000),
                    connected=_bool((query.get("connected") or ["1"])[0]),
                    context=_bool((query.get("context") or ["0"])[0]),
                )
            finally:
                src.close()
            return self._ok(data)
        if path == "/api/timeline":
            filters = filters_from_query(query, me=self.app.actor())
            return self._ok(self.app.require().store.timeline(filters))
        if path == "/api/activity":
            limit = _int((query.get("limit") or ["40"])[0], 40, 1, 400)
            return self._ok({"activity": self.app.require().store.activity(limit=limit)})
        if path == "/api/workspace" or path.startswith("/api/workspace/"):
            return self._workspace_get(path, query)
        if path.startswith("/api/agent-sessions"):
            return self._sessions_get(path, query)
        if path == "/api/session-contexts" or path.startswith("/api/session-contexts/"):
            return self._session_contexts_get(path, query)
        if path.startswith("/api/task-agent/v1/"):
            session = self._require_task_agent()
            return self._task_agent_get(path, query, session)
        m = re.fullmatch(r"/api/units/([^/]+)/history(?:/([^/]+))?", path)
        if m:
            return self._node_history_get(m.group(1), m.group(2), query)
        m = re.match(r"^/api/units/([^/]+)$", path)
        if m:
            return self._ok({"unit": self.app.require().store.get_unit(m.group(1))})
        m = re.match(r"^/api/units/([^/]+)/comments$", path)
        if m:
            return self._ok({"comments": self.app.require().store.comments(m.group(1))})
        if path == "/api/topics":
            store = self.app.require().store
            if self.app.project_permission() in ("write", "admin"):
                store.ensure_default_topics()
            return self._ok(store.topics())
        m = re.match(r"^/api/units/([^/]+)/members$", path)
        if m:
            full = (query.get("full") or ["0"])[0] in {"1", "true", "yes"}
            return self._ok(self.app.require().store.topic(m.group(1), full=full))
        if path == "/api/assistant/insights":
            return self._ok({"insights": assistant.insights(self.app.require().store)})
        if path == "/api/assistant/dispatch":
            task = (query.get("task") or [""])[0]
            if not task:
                raise ApiError(400, "task is required")
            return self._ok(assistant.dispatch_prompt(self.app.require().store, task))
        if path == "/api/import/infohub/sources":
            return self._ok({"sources": importer.default_candidates()})
        if path == "/api/import/infohub/preview":
            src = (query.get("path") or [""])[0]
            if not src:
                raise ApiError(400, "path is required")
            return self._ok(importer.preview(src))
        if path == "/api/export":
            fmt = (query.get("format") or ["json"])[0].lower()
            project = self.app.require()
            # The project is per session, but a tab may show another one (two
            # tabs, or a restart reset the session's choice).  The client
            # therefore names the project it is exporting.
            wanted = (query.get("project") or [""])[0].strip()
            if wanted and wanted != str(project.id):
                other = self.app.project_by_id(wanted)
                if other is None:
                    raise ApiError(404, "Project not found or not accessible for this account.",
                                   "PROJECT_NOT_ACCESSIBLE")
                self.app.check_project_access(other, "read")
                project = other
            name = re.sub(r"[^A-Za-z0-9._-]+", "-", project.name).strip("-") or "ark-project"
            if fmt == "bundle":
                return self._send_markdown_bundle(project, query)
            if fmt == "project":
                include = (query.get("sessions") or [""])[0] in {"1", "true", "yes"}
                return self._send_project_archive(project, name, include_sessions=include)
            if fmt == "json":
                data = json.dumps(export.project_json(project.store), ensure_ascii=False, indent=2)
                return self._send(200, data, "application/json; charset=utf-8",
                                  {"Content-Disposition": f'attachment; filename="{name}.json"'})
            if fmt == "markdown":
                data = export.project_markdown(project.store)
                return self._send(200, data, "text/markdown; charset=utf-8",
                                  {"Content-Disposition": f'attachment; filename="{name}.md"'})
            if fmt == "csv":
                data = export.project_csv(project.store)
                return self._send(200, data, "text/csv; charset=utf-8",
                                  {"Content-Disposition": f'attachment; filename="{name}.csv"'})
            if fmt == "svg":
                data = export.graph_svg(project.store)
                return self._send(200, data, "image/svg+xml; charset=utf-8",
                                  {"Content-Disposition": f'attachment; filename="{name}-map.svg"'})
            raise ApiError(400, "format must be json, markdown, csv, svg, bundle or project")
        m = re.match(r"^/api/media/(\d+)/raw$", path)
        if m:
            media_path, ctype = self.app.resolve_media(int(m.group(1)))
            download = (query.get("download") or [""])[0] in {"1", "true"}
            return self._send_file(media_path, ctype, download=download)
        raise ApiError(404, f"Unknown endpoint: {path}")

    def _agent_whoami(self) -> dict:
        """Who an agent acts for, with the projects its token can reach."""
        token = self.app.request_agent_token()
        current = self.app.current_project()
        if self.app.auth_enabled:
            items = self.app.visible_projects()
        else:
            items = [dict(current.summary(), permission="admin")] if current else []
        cap = str(token.get("scope")) if token else ""
        projects = []
        for item in items:
            permission = str(item.get("permission") or "read")
            if cap and org_mod.PERMISSION_RANK.get(permission, 1) > org_mod.PERMISSION_RANK[cap]:
                permission = cap
            projects.append({"id": item.get("id"), "name": item.get("name") or item.get("id"),
                             "permission": permission,
                             "current": bool(current and item.get("id") == current.id)})
        return {
            "user": self.app.actor() or None,
            "actor": _agent_actor(self._actor()),
            "token_kind": "personal" if token else "server",
            "token_label": (token.get("label") if token else "Server token (legacy)"),
            "token_id": token.get("id") if token else None,
            "scope": cap or None,
            "expires_at": token.get("expires_at") if token else None,
            "project_header": "X-ARK-Project",
            "projects": projects,
        }

    def _personal_agent_tokens_guard(self) -> str:
        """Only a signed-in person (or the local user) manages personal tokens.

        Agent bearer tokens, task-agent read tokens and paired phones cannot
        list or mint tokens.
        """
        if self.app.request_agent_token() is not None or self._agent_token()                 or self.headers.get("X-ARK-Read-Token"):
            raise ApiError(403, "Agent and task tokens cannot manage personal agent tokens.",
                           "AGENT_TOKEN_FORBIDDEN")
        if getattr(self.app._local, "mobile", None) is not None or self.mobile_session is not None                 or self._mobile_token():
            raise ApiError(403, "A paired phone cannot manage personal agent tokens.",
                           "MOBILE_AGENT_TOKENS_BLOCKED")
        user = self.app.actor()
        if not user or (self.app.auth_enabled and self.app.request_session() is None):
            raise ApiError(401, "Sign in with the ARK account to continue.", "AUTH_REQUIRED")
        return user

    def _project_gpu_job(self, job_id: str, permission: str = "read") -> dict:
        project = self.app.require(permission)
        job = self.app.gpu.queue_get(job_id)
        if str(job.get("project") or "") != str(project.id):
            raise ApiError(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
        return job

    def _submit_project_gpu_job(self, body: dict):
        project = self.app.require("write")
        return self._ok({"job": self.app.gpu.queue_submit(
            body, owner=self.app.actor(), project=project.id,
            session=str(body.get("session") or ""),
            task=str(body.get("task") or body.get("task_ref") or ""))})

    def _node_history_store(self, level: str = "read"):
        project_id = str(self.headers.get("X-ARK-Project") or "").strip()
        if not project_id:
            return self.app.require(level).store
        if self.app.request_agent_token() is not None:
            # Token authentication already bound the header's project, and
            # require also enforces the credential's read/write scope.
            return self.app.require(level).store
        if self.mobile_session is not None and self.mobile_session.project_id != project_id:
            raise ApiError(403, "A paired device stays on its paired project")
        project = self.app.project_by_id(project_id)
        if project is None:
            raise ApiError(404, "Project not found")
        if self.app.auth_enabled:
            self.app.check_project_access(project, level)
        return project.store

    def _node_history_get(self, ref: str, action: str | None, query: dict):
        store = self._node_history_store()
        if not action:
            return self._ok(store.node_history(ref,
                limit=_int((query.get("limit") or ["50"])[0], 50, 1, 200),
                offset=_int((query.get("offset") or ["0"])[0], 0, 0, 1_000_000)))
        if action == "diff":
            return self._ok(store.node_diff(ref, (query.get("from") or [None])[0],
                                            (query.get("to") or [None])[0]))
        return self._ok({"revision": store.node_revision(ref, action)})

    def _node_history_write(self, ref: str, action: str | None, body: dict, actor: str):
        store = self._node_history_store("write")
        if not action:
            return self._ok(store.set_node_history(ref, body.get("enabled"), actor))
        if action == "restore":
            return self._ok({"unit": store.restore_node_revision(
                ref, body.get("revision"), body.get("expected_hash"), actor)})
        if action == "apply":
            return self._ok({"unit": store.apply_node_history_change(
                ref, body.get("from"), body.get("to"), body.get("file"),
                body.get("change"), body.get("expected_hash"), actor)})
        if action == "checkpoint":
            return self._ok(store.save_node_version(ref, body.get("expected_hash"), actor))
        raise ApiError(404, "Unknown node history action")

    def _agent_get(self, path: str, query: dict):
        if path == "/api/agent/v1/whoami":
            return self._ok(self._agent_whoami())
        store = self.app.require().store
        if path == "/api/agent/v1/export":
            return self._api_get("/api/export", query)
        media = re.fullmatch(r"/api/agent/v1/media/(\d+)/raw", path)
        if media:
            return self._api_get(f"/api/media/{media.group(1)}/raw", query)
        if path == "/api/agent/v1/gpu/jobs":
            return self._ok(self.app.gpu.queue_list(project=self.app.require().id,
                            **_gpu_list_query(query)))
        gpu_job = re.match(r"^/api/agent/v1/gpu/jobs/([^/]+)(/logs)?$", path)
        if gpu_job:
            job = self._project_gpu_job(gpu_job.group(1))
            return self._ok(self.app.gpu.queue_logs(job["id"], limit=_gpu_tail_query(query)) if gpu_job.group(2) else {"job": job})
        if path == "/api/agent/v1/sessions/watcher.py":
            source = (Path(__file__).resolve().parent / "session_watch.py").read_bytes()
            return self._send(200, source, "text/x-python; charset=utf-8", {
                "Content-Disposition": 'attachment; filename="ark-session-watch.py"',
                "Cache-Control": "no-store"})
        m = re.fullmatch(r"/api/agent/v1/sessions/([^/]+)", path)
        if m:
            project = self.app.require()
            store = self.app.context_store(project)
            state = store.status(m.group(1), uploads_enabled=self.app.session_uploads_enabled(project))
            if state["exists"]:
                user, admin = self._session_viewer()
                if not session_context.can_manage(store.get(m.group(1)), user, admin):
                    raise ApiError(404, "Session not found or not accessible.", "SESSION_NOT_ACCESSIBLE")
            return self._ok(state)
        if path == "/api/agent/v1/interface":
            return self._ok(skills.interface.describe(
                skill_ids=[skill["id"] for skill in skills.SKILLS]))
        if path == "/api/agent/v1/context":
            return self._ok(assistant.context(store))
        if path == "/api/agent/v1/project/palette":
            project = self.app.require()
            return self._ok({
                "palette": project_settings(project.manifest)["palette"],
                "presets": PALETTE_PRESETS,
            })
        if path == "/api/agent/v1/units":
            filters = filters_from_query(query, me=self.app.actor())
            filters["limit"] = _int((query.get("limit") or ["100"])[0], 100, 1, 500)
            return self._ok(store.list_units(filters, include_facets=False))
        m = re.fullmatch(r"/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?", path)
        if m:
            return self._node_history_get(m.group(1), m.group(2), query)
        m = re.match(r"^/api/agent/v1/units/([^/]+)$", path)
        if m:
            return self._ok({"unit": store.get_unit(m.group(1))})
        m = re.match(r"^/api/agent/v1/units/([^/]+)/comments$", path)
        if m:
            return self._ok(_comments_payload(store.comments(m.group(1))))
        m = re.match(r"^/api/agent/v1/units/([^/]+)/members$", path)
        if m:
            return self._ok(store.topic(m.group(1)))
        if path == "/api/agent/v1/insights":
            return self._ok({"insights": assistant.insights(store)})
        if path == "/api/agent/v1/skills":
            return self._ok(skills.catalog_payload())
        if path.startswith("/api/agent/v1/skills/"):
            return self._ok(skills.get_skill(path.rsplit("/", 1)[-1]))
        if path == "/api/agent/v1/setup":
            return self._setup_state(query)
        raise ApiError(404, f"Unknown agent endpoint: {path}")

    # ------------------------------------------------------------------ setup
    def _setup_viewer(self, project, body: dict | None = None,
                      query: dict | None = None):
        """Resolve who is asking for setup data (docs/operations/multi-user-setup.md).

        All project members and their tokens see the resource register.
        Personal tokens retain their user's write permissions. The current
        host is ``X-ARK-Host``, ``?host=`` or ``body.host_id``; by default the
        ARK server's machine.
        """
        import hmac
        layers = skills.setup_layers
        host, source = "", "server"
        for candidate, origin in ((self.headers.get("X-ARK-Host"), "header"),
                                  (((query or {}).get("host") or [""])[0], "query"),
                                  ((body or {}).get("host_id"), "body")):
            host = layers.clean_host_id(candidate)
            if host:
                source = origin
                break
        personal = self.app.request_agent_token()
        bearer = self._agent_token()
        if personal is None and bearer and hmac.compare_digest(
                bearer, self.app.manager.agent_token):
            viewer = layers.legacy_viewer(host)
            viewer.host_source = source
            return viewer
        user = self.app.actor()
        if not self.app.auth_enabled:
            permission = "admin"
        else:
            permission = ("write" if project.id == demo.DEMO_ID
                          else self.app.org.project_permission(user, project.id) or "read")
        rank = org_mod.PERMISSION_RANK.get(permission, 1)
        if personal is not None:
            rank = min(rank, org_mod.PERMISSION_RANK.get(str(personal.get("scope") or "read"), 1))
        mobile_session = getattr(self.app._local, "mobile", None)
        if mobile_session is not None and mobile_session.permission == "read_only":
            rank = min(rank, org_mod.PERMISSION_RANK["read"])
        kind = "personal" if personal is not None else (
            "session" if self.app.auth_enabled else "local")
        return layers.Viewer(user=user,
                             admin=rank >= org_mod.PERMISSION_RANK["admin"],
                             can_write=rank >= org_mod.PERMISSION_RANK["write"],
                             token_kind=kind, host_id=host, host_source=source)

    def _setup_known_user(self, name: str) -> str | None:
        if not self.app.auth_enabled:
            return org_mod.LOCAL_USERNAME if name.lower() == org_mod.LOCAL_USERNAME else None
        user = self.app.org.store.user_by_name(name)
        return str(user["username"]) if user else None

    def _setup_default_owner(self) -> str:
        return self.app._owner_bootstrap_name() or org_mod.LOCAL_USERNAME

    def _setup_payload(self, project, viewer) -> dict:
        return skills.setup_layers.payload(project.path, viewer,
                                           default_owner=self._setup_default_owner(),
                                           known_user=self._setup_known_user)

    def _setup_state(self, query: dict | None = None):
        project = self.app.require()
        return self._ok(self._setup_payload(project, self._setup_viewer(project, query=query)))

    def _add_setup_warnings(self, result: dict, task: dict) -> None:
        """Flag task work that does not match the caller's view of the setup."""
        if not isinstance(result, dict):
            return
        project = self.app.require()
        layers = skills.setup_layers
        view = layers.resolve(project.path, self._setup_viewer(project),
                              default_owner=self._setup_default_owner(),
                              known_user=self._setup_known_user)
        warnings = layers.check_task(view, task or {})
        if warnings:
            result["setup_warnings"] = warnings
            if isinstance(result.get("warnings"), list):
                result["warnings"].extend(warnings)

    def _setup_gpu_servers(self) -> dict | None:
        try:
            return self.app.gpu.servers()
        except Exception:  # noqa: BLE001 - setup must work without the GPU adapter
            return None

    SETUP_ACTIONS = {"discover", "draft", "resource", "remove", "location", "host", "propose",
                     "confirm", "verify"}

    def _setup_action(self, action: str, body: dict, actor: str):
        """Environment setup lifecycle shared by the UI and agent namespaces."""
        layers = skills.setup_layers
        project = self.app.require()
        viewer = self._setup_viewer(project, body)
        default_layer = "project" if viewer.legacy else "user"
        if action == "confirm" and not body.get("layer"):
            # 1.0 clients confirm {digest} only: pick the caller's own layer
            # when the digest is its digest, otherwise the project layer.
            mine = layers.load_user(project.path, viewer.user) if viewer.user else None
            default_layer = ("user" if mine and mine.get("digest")
                             and mine["digest"] == str(body.get("digest") or "") else "project")
        layer = str(body.get("layer") or default_layer).strip().lower()
        if layer not in {"project", "user"}:
            raise ApiError(400, "layer must be project or user", "SETUP_INVALID")
        owner = str(body.get("owner") or "").strip()
        if layer == "user" and owner and not viewer.is_user(owner):
            raise ApiError(403, "you can only change your own setup layer", "SETUP_NOT_OWNER")

        def done(result: dict | None = None):
            data = self._setup_payload(project, viewer)
            if result:
                data.update({key: result[key] for key in ("ok", "errors", "warnings",
                                                          "location", "host")
                             if key in result})
            return self._ok(data)

        try:
            if action == "discover":
                paths = body.get("paths") if isinstance(body.get("paths"), list) else []
                try:
                    provider_summary = self.app.providers.summary()
                except Exception:  # noqa: BLE001 - provider probing is best effort
                    provider_summary = None
                found = skills.setup.discover(
                    paths, gpu_servers=self._setup_gpu_servers(),
                    providers=provider_summary, project=project.summary())
                # The same findings as 2.0 resources for the caller's layer,
                # private by default: the agent asks who should see each one.
                items = layers._legacy_resources(found["draft"])
                for item in items:
                    item["visibility"] = "shared" if layer == "project" else "private"
                found.update({"layer": layer, "resources": items,
                              "host": {"id": viewer.host_id}})
                return self._ok(found)
            if action in {"draft", "resource", "remove"}:
                self.app.require("write")
                if action == "draft":
                    raw = dict(body.get("profile")) if isinstance(body.get("profile"), dict) else {}
                    for key in ("resources", "locations", "hosts"):
                        if isinstance(body.get(key), list):
                            raw[key] = body[key]
                    merge = True
                elif action == "resource":
                    item = body.get("resource") if isinstance(body.get("resource"), dict) else None
                    if not item:
                        raise ApiError(400, "resource is required", "SETUP_INVALID")
                    raw, merge = {"resources": [item]}, True
                else:
                    rid = str(body.get("id") or "").strip()
                    if not rid:
                        raise ApiError(400, "id is required", "SETUP_INVALID")
                    raw, merge = {"resources": [], "delete": [rid]}, True
                result = layers.save_layer(project.path, raw, viewer=viewer, layer=layer,
                                           actor=actor, propose=bool(body.get("propose")),
                                           host_label=str(body.get("host_label") or ""),
                                           merge_resources=merge)
                project.store.log_activity(actor, f"setup.{layer}.{result['state']}", None,
                                           {"digest": result["digest"]})
                return done(result)
            if action == "location":
                self.app.require("write")
                result = layers.add_location(
                    project.path, viewer=viewer, key=str(body.get("resource") or ""),
                    path_or_ref=str(body.get("path_or_ref") or body.get("path") or ""),
                    actor=actor, host_id=str(body.get("host_id") or ""),
                    host_label=str(body.get("host_label") or ""),
                    scope=str(body.get("scope") or ""))
                return done(result)
            if action == "host":
                self.app.require("write")
                result = layers.set_host(project.path, viewer=viewer,
                                         label=str(body.get("label") or ""), actor=actor,
                                         host_id=str(body.get("id") or ""))
                return done(result)
            if action == "propose":
                self.app.require("write")
                result = layers.propose(project.path, viewer=viewer, layer=layer, actor=actor)
                project.store.log_activity(actor, f"setup.{layer}.proposed", None,
                                           {"digest": result["digest"]})
                return done(result)
            if action == "confirm":
                # The human gate: no agent token, no task token, no agent name
                # header.  Project layer: project admin.  User layer: that user.
                if self._agent_token() or not skills.setup.is_human_actor(actor):
                    raise skills.SetupError(
                        "only a human can confirm the environment profile; agents may "
                        "draft and propose", 403, "SETUP_HUMAN_REQUIRED")
                self._reject_task_read_token("confirm the environment setup")
                if layer == "project":
                    self.app.require_admin()
                else:
                    self.app.require("write")
                human = "human:" + (viewer.user or "ui")
                result = layers.confirm_layer(project.path, viewer=viewer, layer=layer,
                                              actor=human,
                                              digest_value=str(body.get("digest") or ""),
                                              note=str(body.get("note") or ""))
                project.store.log_activity(human, f"setup.{layer}.confirmed", None,
                                           {"digest": result["digest"]})
                return done(result)
            if action == "verify":
                checked = layers.verify_view(project.path, viewer, actor=actor,
                                             gpu_servers=self._setup_gpu_servers())
                project.store.log_activity(actor, "setup.verify", None,
                                           {"ok": checked["ok"]})
                data = self._setup_payload(project, viewer)
                data["verify"] = checked
                return self._ok(data)
        except skills.SetupError as exc:
            raise ApiError(exc.status, str(exc), exc.code) from exc
        raise ApiError(404, f"Unknown setup action: {action}")

    def _skills_get(self, path: str, query: dict):
        rest = path[len("/api/skills/"):].strip("/")
        if not rest:
            return self._ok(skills.catalog_payload())
        if rest == "interface":
            return self._ok(skills.interface.describe(
                skill_ids=[skill["id"] for skill in skills.SKILLS]))
        parts = rest.split("/")
        skill_id = parts[0]
        if parts == ["setup", "state"]:
            return self._setup_state(query)
        if len(parts) == 1:
            include_docs = (query.get("docs") or ["1"])[0] not in {"0", "false", "no"}
            return self._ok({"skill": skills.get_skill(skill_id, include_docs=include_docs)})
        if len(parts) == 2 and parts[1] in {"prompt", "agent"}:
            return self._ok({"skill": skill_id, "prompt": skills.agent_prompt(skill_id)})
        raise ApiError(404, f"Unknown skills endpoint: {path}")

    def _skills_post(self, path: str, body: dict, actor: str):
        match = re.match(r"^/api/skills/([^/]+)/([a-z][a-z-]*)$", path)
        if not match:
            raise ApiError(404, f"Unknown skills endpoint: {path}")
        skill_id, action = match.group(1), match.group(2)
        project = self.app.require().summary()
        store = self.app.require().store
        if skill_id == skills.setup.SKILL_ID and action in self.SETUP_ACTIONS:
            return self._setup_action(action, body, actor)
        if action == "render":
            template = str(body.get("template") or "").strip()
            if not template:
                raise ApiError(400, "template is required")
            return self._ok(skills.render_template(
                skill_id, template, body.get("context") or {}, project=project))
        if action == "validate":
            default_kind = {"agent-team": "task", "rrsi": "round", "design": "theme",
                            "setup": "environment"}.get(skill_id, "task")
            kind = str(body.get("kind") or default_kind).strip().lower()
            payload = body.get("payload")
            if not isinstance(payload, dict):
                payload = {key: value for key, value in body.items() if key != "kind"}
            result = skills.validate_payload(
                skill_id, kind, payload, project_root=project.get("path"),
                project=project)
            if skill_id == "agent-team" and kind in {"task", "request"}:
                self._add_setup_warnings(result, payload)
            return self._ok(result)
        if action == "tasks":
            result = skills.create_agent_task(store, body, project=project, actor=actor)
            self._add_setup_warnings(result, body)
            return self._ok(result)
        if action == "brief":
            return self._ok(skills.interface.validate_brief(body))
        if action == "round":
            return self._ok({"result": skills.rrsi.validate_round(body)})
        if action == "history":
            return self._ok({"result": skills.rrsi.history_snapshot(body)})
        if action == "leakage":
            findings = skills.rrsi.scan_leakage(
                str(body.get("text") or body.get("diff") or ""),
                body.get("patterns") if isinstance(body.get("patterns"), list) else None)
            return self._ok({"ok": not findings, "findings": findings})
        if action == "verify":
            return self._ok(skills.design.verify_artifact(
                body.get("path") or "", project_root=project.get("path"),
                kind=str(body.get("artifact_kind") or body.get("kind") or "auto")))
        if action == "asset":
            return self._ok(skills.design.validate_asset_path(
                body.get("path") or "", project_root=project.get("path")))
        raise ApiError(404, f"Unknown skills action: {action}")

    # -------------------------------------------------------------- workspace
    def _workspace_get(self, path: str, query: dict):
        project = self.app.require()
        ws = self.app.workspace
        root = project.path
        if path == "/api/workspace/info":
            data = ws.info(root)
            data["project"] = project.name
            return self._ok(data)
        if path == "/api/workspace/tree":
            return self._ok(ws.tree(root, (query.get("path") or [""])[0],
                                    depth=_int((query.get("depth") or ["1"])[0], 1, 1, 4)))
        if path == "/api/workspace/search":
            return self._ok(ws.search(root, (query.get("q") or [""])[0],
                                      limit=_int((query.get("limit") or ["100"])[0], 100, 1, 200)))
        if path == "/api/workspace/grep":
            # Workbench Search over file contents (bounded; skips binaries;
            # ``stream=1``: NDJSON, see _stream_search).
            text, options = (query.get("q") or [""])[0], _search_options(query)
            options.update(include=(query.get("include") or [""])[0], exclude=(query.get("exclude") or [""])[0])
            try:
                if _bool((query.get("stream") or ["0"])[0]):
                    search_mod.build_matcher(text, case=options["case"], word=options["word"], regex=options["regex"])
                    return self._stream_search("file", lambda cancelled: search_mod.iter_search_files(
                        root, text, cancelled=cancelled, **options), query)
                return self._ok(search_mod.search_files(root, text, **options))
            except search_mod.SearchError as exc:
                raise ApiError(400, str(exc)) from exc
        if path == "/api/workspace/file":
            target = (query.get("path") or [""])[0]
            if not target:
                raise ApiError(400, "path is required")
            return self._ok(ws.read_file(root, target))
        if path == "/api/workspace/raw":
            # Binary preview (image/video/audio/PDF) for the Editor workspace.
            # Same symlink-safe resolution as read_file; no size cap because
            # the body is streamed in ranges instead of loaded into memory.
            target_path = ws.resolve_file(root, (query.get("path") or [""])[0])
            ctype = mimetypes.guess_type(target_path.name)[0] or "application/octet-stream"
            download = (query.get("download") or [""])[0] in {"1", "true"}
            return self._send_file(target_path, ctype, download=download)
        if path == "/api/workspace/changes":
            return self._ok(ws.changes(root, (query.get("base") or ["HEAD"])[0]))
        if path == "/api/workspace/diff":
            target = (query.get("path") or [""])[0]
            if not target:
                raise ApiError(400, "path is required")
            return self._ok(ws.diff_file(root, target, (query.get("base") or ["HEAD"])[0]))
        if path == "/api/workspace/terminals":
            return self._ok(ws.terminals.list(project.id))
        m = re.match(r"^/api/workspace/terminals/([^/]+)/stream$", path)
        if m:
            return self._stream_terminal(m.group(1),
                                         _int((query.get("offset") or ["0"])[0], 0, 0, 10_000_000),
                                         project.id)
        m = re.match(r"^/api/workspace/terminals/([^/]+)$", path)
        if m:
            ws.terminals.get(m.group(1), project.id)  # 404 outside this project
            return self._ok({"terminal": ws.terminals.read(
                m.group(1), _int((query.get("offset") or ["0"])[0], 0, 0, 10_000_000),
                _int((query.get("limit") or ["200"])[0], 200, 1, 1000))})
        raise ApiError(404, f"Unknown workspace endpoint: {path}")

    def _stream_terminal(self, terminal_id: str, offset: int, project_id: str = ""):
        manager = self.app.workspace.terminals
        try:
            manager.get(terminal_id, project_id or None)  # validate before headers
        except workspace_files.WorkspaceError as exc:
            raise ApiError(exc.status, str(exc)) from exc
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Accel-Buffering", "no")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        last_status = None
        last_beat = time.monotonic()
        deadline = last_beat + 600
        terminal_states = {"exited", "cancelled", "timeout", "output_limit", "failed"}

        def emit(event: str, payload: dict) -> None:
            frame = f"event: {event}\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n"
            self.wfile.write(frame.encode("utf-8"))

        try:
            while True:
                try:
                    data = manager.read(terminal_id, offset, 200)
                except workspace_files.WorkspaceError:
                    return
                for entry in data["entries"]:
                    emit("output", entry)
                offset = data["offset"]
                state = {key: value for key, value in data.items() if key != "entries"}
                if data["entries"] or data["status"] != last_status:
                    emit("state", state)
                    last_status = data["status"]
                    self.wfile.flush()
                if data["status"] in terminal_states:
                    emit("done", state)
                    self.wfile.flush()
                    return
                if time.monotonic() - last_beat >= 10:
                    self.wfile.write(b": keep-alive\n\n")
                    self.wfile.flush()
                    last_beat = time.monotonic()
                if time.monotonic() >= deadline:
                    return
                time.sleep(0.2)
        except (BrokenPipeError, ConnectionResetError, OSError):
            return

    def _workspace_post(self, path: str, body: dict):
        project = self.app.require()
        ws = self.app.workspace
        root = project.path
        if path == "/api/workspace/file":
            return self._ok(ws.write_file(root, body))
        if path == "/api/workspace/create":
            return self._ok(ws.create_path(root, body))
        if path == "/api/workspace/rename":
            return self._ok(ws.rename_path(root, body))
        if path == "/api/workspace/delete":
            return self._ok(ws.delete_path(root, body))
        if path == "/api/workspace/terminals":
            terminal = ws.terminals.start(
                root, body.get("command") or "",
                cwd=body.get("cwd") or "",
                timeout=body.get("timeout") or workspace_files.TERMINAL_DEFAULT_TIMEOUT,
                pty=_bool(body.get("pty")),
                cols=body.get("cols", workspace_files.TERMINAL_DEFAULT_COLS),
                rows=body.get("rows", workspace_files.TERMINAL_DEFAULT_ROWS),
                project_id=project.id, project_path=str(project.path),
                creator=self.app.actor())
            return self._ok({"terminal": terminal.public(),
                             "pty_available": workspace_files.pty_availability()["available"],
                             "stream": f"/api/workspace/terminals/{terminal.id}/stream"})
        m = re.match(r"^/api/workspace/terminals/([^/]+)/resize$", path)
        if m:
            ws.terminals.get(m.group(1), project.id)
            return self._ok(ws.terminals.resize(
                m.group(1), body.get("cols", 0), body.get("rows", 0)))
        m = re.match(r"^/api/workspace/terminals/([^/]+)/stdin$", path)
        if m:
            ws.terminals.get(m.group(1), project.id)
            return self._ok({"terminal": ws.terminals.write(
                m.group(1), body.get("text") or "")})
        m = re.match(r"^/api/workspace/terminals/([^/]+)/cancel$", path)
        if m:
            ws.terminals.get(m.group(1), project.id)
            return self._ok({"terminal": ws.terminals.cancel(m.group(1))})
        raise ApiError(404, f"Unknown workspace endpoint: {path}")

    def _reject_task_agent_read_token(self, message: str) -> None:
        token = self._read_token()
        if token and self.app.sessions.session_for_read_token(token):
            raise ApiError(403, message)

    def _reject_workspace_read_token(self) -> None:
        self._reject_task_agent_read_token(
            "task-agent read tokens cannot use the files workspace")

    def _worktrees_payload(self) -> dict:
        """List worktrees with the active sessions that use each one."""
        listing = self.app.worktrees.list()
        active_by_path: dict[str, list[str]] = {}
        for item in listing.get("worktrees", []):
            ids = self.app.sessions.sessions_for_worktree(item.get("path") or "", active_only=True)
            item["active_sessions"] = ids
            item["active_count"] = len(ids)
            item["conflict"] = len(ids) > 1
            if item["conflict"]:
                item["status"] = "conflict"
            elif ids and item.get("status") == "clean":
                item["status"] = "busy"
            active_by_path[item.get("path") or ""] = ids
        listing["active_by_path"] = {key: value for key, value in active_by_path.items() if value}
        listing["groups"] = self._filter_session_groups(
            self.app.sessions.groups())["groups"]
        listing["session_total"] = len(self._accessible_session_ids())
        return listing

    def _session_project(self, session_id: str):
        """Resolve the project a task session belongs to (or ``None``)."""
        try:
            session = self.app.sessions.get(session_id)
        except SessionError:
            return None
        ref = (session.meta.get("project") or {}) if isinstance(session.meta, dict) else {}
        project_id = str(ref.get("id") or "")
        if not project_id:
            return None
        return self.app.project_by_id(project_id)

    def _guard_session_access(self, session_id: str, level: str = "read") -> None:
        """A task session belongs to the project currently open in this view.

        Having read access to another project is not enough: switching projects
        must hide the other project's sessions from the task workspace and block
        direct session/artifact URLs.
        """
        project = self._session_project(session_id)
        current = self.app.current_project()
        if (project is None or current is None
                or str(project.id) != str(current.id)):
            raise ApiError(404, "Session not found or not accessible.",
                           "SESSION_NOT_ACCESSIBLE")
        if self.app.auth_enabled:
            self.app.check_project_access(project, level)
        # Private to the creator unless shared; changing one needs owner/admin.
        meta = self.app.sessions.get(session_id).meta
        record = {"owner": meta.get("actor") or "", "shared": bool(meta.get("shared"))}
        user, admin = self._session_viewer()
        if not session_context.can_view(record, user, admin):
            raise ApiError(404, "Session not found or not accessible.",
                           "SESSION_NOT_ACCESSIBLE")
        if level != "read" and not session_context.can_manage(record, user, admin):
            raise ApiError(403, "Only the session's creator or a project admin can change it.",
                           "SESSION_NOT_OWNER")

    def _accessible_session_ids(self) -> set[str]:
        """Ids of sessions belonging to the currently open project only."""
        current = self.app.current_project()
        if current is None:
            return set()
        out = set()
        try:
            listing = self.app.sessions.list().get("sessions") or []
        except (SessionError, OSError):
            return out
        user, admin = self._session_viewer()
        for item in listing:
            project = item.get("project") or {}
            if (str(project.get("id") or "") == str(current.id)
                    and session_context.can_view(item, user, admin)):
                out.add(str(item.get("id") or ""))
        return out

    def _sessions_token(self) -> str:
        """Fingerprint of the agent sessions this account sees in this project
        (ids and statuses); changes when one starts, ends or is removed."""
        try:
            listing = self._filter_session_listing(self.app.sessions.list())
        except (SessionError, OSError, ApiError):
            return ""
        key = "|".join(f"{item.get('id')}:{item.get('status')}"
                       for item in sorted(listing.get("sessions") or [], key=lambda i: str(i.get("id"))))
        return hashlib.blake2b(key.encode("utf-8"), digest_size=8).hexdigest()

    def _filter_session_listing(self, data: dict) -> dict:
        allowed = self._accessible_session_ids()
        if allowed is None:
            return data
        payload = dict(data)
        payload["sessions"] = [item for item in data.get("sessions") or []
                               if str(item.get("id") or "") in allowed]
        counts: dict[str, int] = {}
        for item in payload["sessions"]:
            key = str(item.get("status") or "")
            counts[key] = counts.get(key, 0) + 1
        payload["counts"] = counts
        payload["total"] = len(payload["sessions"])
        return payload

    def _filter_session_groups(self, data: dict) -> dict:
        allowed = self._accessible_session_ids()
        if allowed is None:
            return data
        groups = []
        counts: dict[str, int] = {}
        for group in data.get("groups") or []:
            sessions = [item for item in group.get("sessions") or []
                        if str(item.get("id") or "") in allowed]
            if not sessions:
                continue
            item = dict(group)
            item["sessions"] = sessions
            item["active"] = len(sessions)
            groups.append(item)
            key = str(item.get("status") or "")
            counts[key] = counts.get(key, 0) + 1
        return {"groups": groups, "counts": counts, "active": len(groups)}

    def _assistant_config_payload(self) -> dict:
        """Effective assistant config (personal > project > global) + personal view."""
        resolved = self.app.effective_assistant()
        personal = self.app.org.personal_settings_public(self.app.actor(), "assistant")
        config = {
            "configured": bool(resolved.get("api_key")),
            "base_url": resolved.get("base_url"),
            "model": resolved.get("model"),
            "reasoning_effort": resolved.get("reasoning_effort"),
            "timeout": resolved.get("timeout"),
            "has_key": bool(resolved.get("api_key")),
            "key_hint": resolved.get("key_hint") or "",
            "source": (resolved.get("sources") or {}).get("api_key") or "none",
            "sources": resolved.get("sources") or {},
            "api_key_env": resolved.get("api_key_env") or "",
            "project_configured": bool(resolved.get("project_configured")),
            "personal_config_required": bool(resolved.get("personal_config_required")),
            "path": str(assistant_config.config_path()),
        }
        return {"config": config, "personal": personal,
                "effective": self.app.effective_settings_payload()}

    def _require_org_admin(self) -> str:
        try:
            return self.app.org.require_org_admin(self.app.actor())
        except org_mod.OrgError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc

    def _session_viewer(self) -> tuple[str, bool]:
        """``(user, admin)`` for session privacy: project admins (and the legacy
        server token, which is admin-only) see every session; others see their
        own and shared ones (docs/agents/session-context.md)."""
        import hmac
        user = self.app.actor()
        bearer = self._agent_token()
        if (self.app.request_agent_token() is None and bearer
                and hmac.compare_digest(bearer, self.app.manager.agent_token)):
            return user, True
        return user, self.app.project_permission() == "admin"

    def _session_contexts_get(self, path: str, query: dict):
        viewer = self._session_viewer()
        if path == "/api/session-contexts":
            return self._ok(self.app.session_contexts(viewer))
        m = re.fullmatch(r"/api/session-contexts/([^/]+)(?:/(transcript|raw|context))?", path)
        if not m:
            raise ApiError(404, f"Unknown session context endpoint: {path}")
        session_id, action = m.group(1), m.group(2)
        if action is None:
            return self._ok({"session": self.app.session_context(session_id, viewer)})
        if action == "transcript":
            return self._ok(self.app.session_context_transcript(session_id, viewer))
        if action == "context":
            return self._ok({"summary": self.app.session_context_summary(session_id, viewer)})
        data = self.app.session_context_raw(session_id, viewer)
        return self._send(200, data, "application/x-ndjson; charset=utf-8", {
            "Content-Disposition": f'attachment; filename="{session_id}.jsonl"',
            "Cache-Control": "no-store"})

    def _sessions_get(self, path: str, query: dict):
        sessions = self.app.sessions
        if path == "/api/agent-sessions/presets":
            return self._ok({"presets": sessions.presets_public(self.app.actor()),
                             "personal_config_required": bool(
                                 self.app.effective_agents_public().get(
                                     "personal_config_required"))})
        if path == "/api/agent-sessions/groups":
            return self._ok(self._filter_session_groups(sessions.groups()))
        if path == "/api/agent-sessions":
            return self._ok(self._filter_session_listing(sessions.list()))
        m = re.match(r"^/api/agent-sessions/([^/]+)$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok({"session": sessions.get(m.group(1)).public(detail=True)})
        m = re.match(r"^/api/agent-sessions/([^/]+)/logs$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            stream = (query.get("stream") or ["stdout"])[0]
            offset = _int((query.get("offset") or ["0"])[0], 0, 0, 10_000_000)
            limit = _int((query.get("limit") or ["200"])[0], 200, 1, 500)
            return self._ok(sessions.logs(m.group(1), stream, offset, limit))
        m = re.match(r"^/api/agent-sessions/([^/]+)/transcript$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok(sessions.transcript(m.group(1)))
        m = re.match(r"^/api/agent-sessions/([^/]+)/approvals$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok(sessions.approvals(m.group(1)))
        m = re.match(r"^/api/agent-sessions/([^/]+)/media$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok(sessions.workspace_index(m.group(1)))
        m = re.match(r"^/api/agent-sessions/([^/]+)/artifacts/read$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok(sessions.read_artifact(m.group(1), (query.get("path") or [""])[0]))
        m = re.match(r"^/api/agent-sessions/([^/]+)/artifacts/download$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            target, ctype, name = sessions.artifact_download(
                m.group(1), (query.get("path") or [""])[0])
            safe_name = re.sub(r'["\r\n]', "", name)
            return self._send(200, target.read_bytes(), ctype,
                              {"Content-Disposition": f'attachment; filename="{safe_name}"'})
        m = re.match(r"^/api/agent-sessions/([^/]+)/artifacts$", path)
        if m:
            self._guard_session_access(m.group(1), "read")
            return self._ok(sessions.list_artifacts(m.group(1), (query.get("path") or [""])[0]))
        raise ApiError(404, f"Unknown endpoint: {path}")

    def _task_agent_get(self, path: str, query: dict, session):
        """Read-only ARK API for a running task session.  Never serves writes."""
        store = self.app.require().store
        if path == "/api/task-agent/v1/context":
            return self._ok(self.app.task_context(session))
        if path == "/api/task-agent/v1/project":
            project = self.app.require()
            return self._ok({"project": project.summary(), "stats": store.stats(), "read_only": True})
        if path == "/api/task-agent/v1/units":
            filters = filters_from_query(query, me=self.app.actor())
            filters["limit"] = _int((query.get("limit") or ["200"])[0], 200, 1, 1000)
            filters["offset"] = _int((query.get("offset") or ["0"])[0], 0, 0, 1_000_000)
            filters["ids"] = [int(x) for x in _qlist(query, "ids") if x.isdigit()]
            return self._ok(store.list_units(filters, include_facets=False))
        m = re.match(r"^/api/task-agent/v1/units/([^/]+)$", path)
        if m:
            return self._ok({"unit": store.get_unit(m.group(1)), "read_only": True})
        m = re.match(r"^/api/task-agent/v1/units/([^/]+)/comments$", path)
        if m:
            return self._ok({**_comments_payload(store.comments(m.group(1))), "read_only": True})
        if path == "/api/task-agent/v1/links":
            edges = store.graph({"limit": 1200})["edges"]
            return self._ok({"links": edges, "total": len(edges), "read_only": True})
        if path == "/api/task-agent/v1/snapshot":
            return self._ok(self.app.sessions.context_snapshot(session))
        if path == "/api/task-agent/v1/session":
            return self._ok({"session": session.public(detail=False)})
        if path == "/api/task-agent/v1/gpu/jobs":
            ref = session.meta.get("project") or {}
            return self._ok(self.app.gpu.queue_list(
                owner=str(session.meta.get("actor") or ""),
                project=str(ref.get("id") or ""), session=session.id, **_gpu_list_query(query)))
        m = re.match(r"^/api/task-agent/v1/gpu/jobs/([^/]+)/logs$", path)
        if m:
            job = self.app.gpu.queue_get(m.group(1))
            ref = session.meta.get("project") or {}
            if (str(job.get("project") or "") != str(ref.get("id") or "")
                    or str(job.get("session") or "") != session.id):
                raise ApiError(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
            return self._ok(self.app.gpu.queue_logs(m.group(1), limit=_gpu_tail_query(query)))
        m = re.match(r"^/api/task-agent/v1/gpu/jobs/([^/]+)$", path)
        if m:
            job = self.app.gpu.queue_get(m.group(1))
            ref = session.meta.get("project") or {}
            if (str(job.get("project") or "") != str(ref.get("id") or "")
                    or str(job.get("session") or "") != session.id):
                raise ApiError(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
            return self._ok({"job": job})
        raise ApiError(404, f"Unknown task-agent endpoint: {path}")

    # ------------------------------------------------------------------- POST
    def do_POST(self):
        try:
            self._begin_request()
            self._check_host()
            parsed = urlparse(self.path)
            path = parsed.path
            if path == "/api/local/open":
                return self._handle_local_open()
            if self._handle_auth_post(unquote(path)):
                return
            self._guard_public_auth()
            self._guard_mobile()
            self._guard_project_scope()
            if path == "/api/task-agent/v1/gpu/jobs":
                session = self._require_task_agent()
                if str(session.meta.get("access_mode") or "") != "full_access":
                    raise ApiError(
                        403,
                        "GPU queue dispatch requires a full-access task session.",
                        "GPU_QUEUE_PERMISSION",
                    )
                self._bind_task_agent_context(session)
                body = self._read_json()
                ref = session.meta.get("project") or {}
                self.app.require("write")
                job = self.app.gpu.queue_submit(
                    body, owner=str(session.meta.get("actor") or ""),
                    project=str(ref.get("id") or self.app.require().id),
                    session=session.id,
                    task=str(session.meta.get("task_ref") or body.get("task") or ""))
                return self._ok({"job": job})
            m = re.match(r"^/api/task-agent/v1/gpu/jobs/([^/]+)/cancel$", path)
            if m:
                session = self._require_task_agent()
                if str(session.meta.get("access_mode") or "") != "full_access":
                    raise ApiError(403, "GPU queue dispatch requires a full-access task session.",
                                   "GPU_QUEUE_PERMISSION")
                self._bind_task_agent_context(session)
                self.app.require("write")
                job = self.app.gpu.queue_get(m.group(1))
                ref = session.meta.get("project") or {}
                if (str(job.get("project") or "") != str(ref.get("id") or "")
                        or str(job.get("session") or "") != session.id):
                    raise ApiError(404, "GPU job not found", "GPU_JOB_NOT_FOUND")
                return self._ok({"job": self.app.gpu.queue_cancel(m.group(1))})
            if not self._same_origin():
                raise ApiError(403, "Cross-origin writes are rejected")
            if path.startswith("/api/task-agent/"):
                raise ApiError(403, "Task-agent sessions have read-only ARK access; "
                                    "route changes through the inner assistant.")
            if self._handle_profile("POST", path):
                return
            if path == "/api/chat/stream":
                return self._stream_chat()
            body = self._read_json()
            actor = self._actor()
            # -------------------------------------------------- org/personal
            if path == "/api/org/users":
                try:
                    user = self.app.org.create_user(self.app.actor(), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"user": user,
                                 "users": self.app.org.list_members()})
            if path == "/api/org/password":
                try:
                    result = self.app.org.change_password(self.app.actor(), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                if self.app.auth_enabled:
                    current = self._auth_session()
                    result["sessions_revoked"] = self.app.auth.revoke_user(
                        self.app.actor(),
                        except_sid=current.sid_hash if current else "")
                    result["devices_revoked"] = self.app.mobile.revoke_user(
                        self.app.actor())
                return self._ok(result)
            m = re.match(r"^/api/org/users/([^/]+)/password$", path)
            if m:
                try:
                    result = self.app.org.reset_password(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                target = self.app.org.store.user_by_id(m.group(1))
                if self.app.auth_enabled and target:
                    result["sessions_revoked"] = self.app.auth.revoke_user(
                        str(target["username"]))
                return self._ok(result)
            m = re.match(r"^/api/org/users/([^/]+)/linux-accounts$", path)
            if m:
                try:
                    result = self.app.org.assign_linux_account(
                        self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"assignment": result,
                                 "linux_accounts": self.app.org.store.linux_accounts_map()})
            if path == "/api/org/teams":
                try:
                    return self._ok({"team": self.app.org.create_team(self.app.actor(), body),
                                     "teams": self.app.org.list_teams()})
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
            m = re.match(r"^/api/org/teams/([^/]+)/members$", path)
            if m:
                try:
                    result = self.app.org.add_team_member(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok(result)
            m = re.match(r"^/api/org/projects/([^/]+)/grants$", path)
            if m:
                try:
                    result = self.app.org.grant_project(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"grant": result,
                                 "project": self.app.org.project_detail(
                                     self.app.actor(), m.group(1))})
            m = re.match(r"^/api/org/projects/([^/]+)/grants/revoke$", path)
            if m:
                try:
                    result = self.app.org.revoke_project(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"result": result,
                                 "project": self.app.org.project_detail(
                                     self.app.actor(), m.group(1))})
            if path == "/api/personal/agent-tokens":
                user = self._personal_agent_tokens_guard()
                try:
                    return self._ok(self.app.org.create_agent_token(user, body))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
            if path == "/api/personal/settings":
                scope = (parse_qs(parsed.query).get("scope") or ["assistant"])[0]
                scope = str(body.get("scope") or scope).strip().lower()
                try:
                    if scope == "assistant":
                        settings = self.app.org.save_personal_assistant(
                            self.app.actor(), body)
                    elif scope == "agents":
                        settings = self.app.org.save_personal_agents(
                            self.app.actor(), body)
                    elif scope == "environment":
                        settings = self.app.org.save_personal_environment(
                            self.app.actor(), body)
                        return self._ok({"scope": scope, "settings": settings})
                    else:
                        raise ApiError(400, "scope must be assistant, agents or environment",
                                       "INVALID_SCOPE")
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"scope": scope, "settings": settings,
                                 "effective": self.app.effective_settings_payload()})
            if path == "/api/mobile/pair":
                request_host, request_port = self._request_host_port()
                return self._ok(self.app.create_pairing(
                    body, request_host=request_host, request_port=request_port,
                    request_scheme=self._request_scheme()))
            if path == "/api/mobile/pair/claim":
                result = self._claim_pairing(body)
                headers = {}
                # The QR is a one-time *paired-device* credential bound to the
                # account that created it.  It must never become a desktop
                # account session: that would bypass the read-only/permission
                # scope and survive revocation.
                token = str(result.get("mobile_token") or "")
                if token:
                    parts = [
                        f"ark_mobile={token}",
                        f"Path={self._external_prefix()}",
                        "HttpOnly", "SameSite=Lax",
                        f"Max-Age={int(result.get('expires_in') or 0)}",
                    ]
                    if self._request_is_secure():
                        parts.append("Secure")
                    headers["Set-Cookie"] = "; ".join(parts)
                    result["mobile_user"] = str(result.get("user") or "")
                return self._ok(result, headers=headers or None)
            if path == "/api/mobile/login":
                return self._mobile_login(body)
            if path == "/api/mobile/session/project":
                return self._ok(self._mobile_switch_project(body))
            m = re.match(r"^/api/mobile/pairs/([^/]+)/revoke$", path)
            if m:
                return self._ok(self.app.revoke_pairing(m.group(1)))
            if path == "/api/gpu/jobs":
                return self._submit_project_gpu_job(body)
            m = re.match(r"^/api/gpu/jobs/([^/]+)/cancel$", path)
            if m:
                self._project_gpu_job(m.group(1), "write")
                return self._ok({"job": self.app.gpu.queue_cancel(m.group(1))})
            m = re.fullmatch(r"/api/resources/devices/([^/]+)", path)
            if m:
                return self._ok(self.app.update_device_privacy(unquote(m.group(1)), body))
            if path == "/api/gpu/probe":
                return self._ok(self.app.gpu.probe(body))
            if path == "/api/gpu/dispatch/prepare":
                return self._ok(self.app.gpu.prepare_dispatch(body))
            if path == "/api/open":
                # A task-agent read token is GET-only by contract and must never
                # trigger desktop actions, even though /api/open is a UI write.
                for candidate in {self._read_token(), self._agent_token()}:
                    if candidate and self.app.sessions.session_for_read_token(candidate):
                        raise ApiError(403, "Task-agent read tokens cannot open resources on the host")
                self.app.require_admin()
                return self._ok(self.app.open_external(body))
            if path == "/api/workspace" or path.startswith("/api/workspace/"):
                self._reject_workspace_read_token()
                if "/terminals" in path:
                    self.app.require_admin()
                return self._workspace_post(path, body)
            if path.startswith("/api/agent/v1/"):
                self._require_agent()
                return self._agent_write(path, body, actor)
            if path.startswith("/api/skills/"):
                return self._skills_post(path, body, actor)
            if path == "/api/providers/refresh":
                self._reject_task_agent_read_token(
                    "task-agent read tokens cannot refresh the host provider catalog")
                return self._ok(self.app.providers.refresh())
            if path == "/api/worktrees/create":
                self._reject_task_agent_read_token(
                    "task-agent read tokens cannot create host Git worktrees")
                self.app.require_admin()
                result = self.app.worktrees.create(body)
                result["worktrees"] = self._worktrees_payload()
                return self._ok(result)
            if path == "/api/worktrees/remove":
                self._reject_task_agent_read_token(
                    "task-agent read tokens cannot remove host Git worktrees")
                self.app.require_admin()
                target = self.app.worktrees.resolve_session_target(
                    body.get("path") or body.get("name") or body.get("branch") or "")
                active = self.app.sessions.sessions_for_worktree(target["path"], active_only=True)
                if active:
                    raise WorktreeError(
                        f"stop the active session(s) before removing this worktree: {', '.join(active)}",
                        409, "WORKTREE_SESSION_ACTIVE")
                result = self.app.worktrees.remove(body)
                result["worktrees"] = self._worktrees_payload()
                return self._ok(result)
            if path == "/api/agent-sessions":
                project = self.app.require()
                if str(project.summary().get("id") or "") == demo.DEMO_ID:
                    raise ApiError(
                        403,
                        "Coding agents are disabled in the demo project. "
                        "Chat and Assistant are still available.",
                        "DEMO_AGENTS_DISABLED",
                    )
                host = self.headers.get("Host") or ""
                base_url = f"http://{host}" if host else ""
                if str(body.get("access_mode") or "") == "full_access":
                    try:
                        self.app.blackbox.snapshot(project, "pre-agent")
                    except Exception:
                        pass
                session = self.app.sessions.start(body, base_url=base_url,
                                                   actor=self.app.actor())
                return self._ok({"session": session.public(detail=True)})
            m = re.fullmatch(r"/api/session-contexts/([^/]+)/continue", path)
            if m:
                self._reject_task_read_token("continue stored sessions")
                host = self.headers.get("Host") or ""
                return self._ok(self.app.continue_session_context(
                    m.group(1), body, self._session_viewer(), base_url=f"http://{host}" if host else ""))
            m = re.match(r"^/api/agent-sessions/([^/]+)/input$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                session = self.app.sessions.send_input(
                    m.group(1), body.get("text") or body.get("message") or "",
                    context=body.get("context"))
                return self._ok({"session": session, "accepted": True})
            m = re.match(r"^/api/agent-sessions/([^/]+)/continue$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.continue_session(
                    m.group(1), body.get("text") or body.get("message") or "",
                    platform=body.get("platform"),
                    model=body.get("model"),
                    effort=body.get("effort"),
                    access_mode=body.get("access_mode"),
                    context=body.get("context"),
                    actor=self.app.actor()), "continued": True})
            m = re.match(r"^/api/agent-sessions/([^/]+)/steer$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok(self.app.sessions.steer(
                    m.group(1), body.get("text") or body.get("message") or "",
                    context=body.get("context")))
            m = re.match(r"^/api/agent-sessions/([^/]+)/steer/reorder$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.reorder_steer(
                    m.group(1), body.get("order"))})
            m = re.match(r"^/api/agent-sessions/([^/]+)/approvals/([^/]+)$", path)
            if m:
                self._reject_task_read_token("decide approvals")
                self._guard_session_access(m.group(1), "write")
                return self._ok(self.app.sessions.decide_approval(
                    m.group(1), m.group(2),
                    body.get("decision") or body.get("action") or "",
                    message=body.get("message") or "",
                    actor=actor))
            m = re.match(r"^/api/agent-sessions/([^/]+)/fork$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                host = self.headers.get("Host") or ""
                base_url = f"http://{host}" if host else ""
                session = self.app.sessions.fork_session(
                    m.group(1), body, base_url=base_url, actor=self.app.actor())
                return self._ok({"session": session.public(detail=True),
                                 "forked_from": m.group(1),
                                 "relation": session.meta.get("relation") or "side_chat"})
            m = re.match(r"^/api/agent-sessions/([^/]+)/context/validate$", path)
            if m:
                self._guard_session_access(m.group(1), "read")
                return self._ok(self.app.sessions.validate_context(
                    m.group(1), body.get("context") or body.get("attachments") or []))
            m = re.match(r"^/api/agent-sessions/([^/]+)/steer/([^/]+)$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.remove_steer(
                    m.group(1), m.group(2)), "removed": True})
            m = re.match(r"^/api/agent-sessions/([^/]+)/stop$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.request_stop(m.group(1))})
            m = re.match(r"^/api/agent-sessions/([^/]+)/archive$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.archive(m.group(1)), "archived": True})
            m = re.match(r"^/api/agent-sessions/([^/]+)/relaunch$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                host = self.headers.get("Host") or ""
                base_url = f"http://{host}" if host else ""
                session = self.app.sessions.relaunch(m.group(1), base_url=base_url,
                                                      actor=self.app.actor())
                return self._ok({"session": session.public(detail=True), "relaunched": True})
            if path == "/api/projects":
                return self._ok(self.app.create_project(body))
            if path == "/api/projects/open":
                return self._ok(self.app.open_project(body))
            if path == "/api/blackbox":
                project = self.app.require_admin()
                reason = str(body.get("reason") or "manual").strip()[:40] or "manual"
                return self._ok({"snapshot": self.app.blackbox.snapshot(project, reason, force=True)})
            m = re.match(r"^/api/blackbox/([^/]+)/restore$", path)
            if m:
                project = self.app.require_admin()
                with self.app.lock:
                    result = self.app.blackbox.restore(project, m.group(1))
                return self._ok({"restored": result, "project": self.app.project_payload()})
            m = re.fullmatch(r"/api/units/([^/]+)/history(?:/([^/]+))?", path)
            if m:
                return self._node_history_write(m.group(1), m.group(2), body, actor)
            store = self.app.require().store
            if path == "/api/units":
                unit = store.create_unit(body, actor=actor)
                return self._ok({"unit": unit})
            if path == "/api/links":
                result = store.create_link(body.get("source"), body.get("target"),
                                           body.get("relation") or "relates_to",
                                           body.get("note") or "", actor=actor)
                return self._ok(result)
            if path == "/api/media/upload":
                return self._ok(self._embed_media(body, actor))
            if path == "/api/media":
                result = store.add_media(body.get("unit"), body.get("url"), body.get("kind") or "link",
                                         body.get("caption") or "", actor=actor)
                return self._ok(result)
            if path == "/api/assistant/config":
                # Per-user, independent assistant settings.  Saving here never
                # touches the shared project folder or ~/.ark/assistant.json.
                try:
                    self.app.org.save_personal_assistant(self.app.actor(), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok(self._assistant_config_payload())
            if path == "/api/assistant/config/test":
                config = self.app.effective_assistant()
                if not config.get("api_key"):
                    raise ApiError(
                        409,
                        "No ARK assistant credential is configured. Set a personal "
                        "API key in Settings → Personal → Assistant, or ask a project "
                        "admin to configure the project assistant.",
                        "PERSONAL_CONFIG_REQUIRED")
                try:
                    return self._ok(assistant_config.test_connection(config))
                except ValueError as exc:
                    raise ApiError(400, str(exc)) from exc
            if path == "/api/assistant/apply":
                return self._ok(assistant.apply(store, body.get("insight") or {}, actor=actor))
            if path == "/api/assistant/extract":
                return self._ok(assistant.extract_ideas(store, body.get("text") or "", actor=actor,
                                                        source=body.get("source"), limit=body.get("limit") or 24))
            if path == "/api/chat":
                host = self.headers.get("Host") or "127.0.0.1"
                return self._ok(self.app.chat_send(body, base_url=f"http://{host}"))
            if path == "/api/import/infohub":
                src = importer.InfoHubSource(body.get("path") or "")
                try:
                    result = src.import_into(store, mode=body.get("mode") or "topics",
                                             update=bool(body.get("update")), actor=actor)
                finally:
                    src.close()
                return self._ok(result)
            if path == "/api/infohub/import":
                source_path = body.get("path") or self._first_infohub_path()
                src = importer.InfoHubSource(source_path)
                try:
                    result = src.import_key(store, body.get("key") or "", actor=actor,
                                             as_idea=bool(body.get("as_idea")),
                                             copy=bool(body.get("copy")))
                finally:
                    src.close()
                return self._ok(result)
            m = re.match(r"^/api/units/([^/]+)/comments$", path)
            if m:
                comment = store.add_comment(m.group(1), body.get("body"), body.get("kind") or "comment",
                                            reply_to=body.get("reply_to"), author=self.app.actor() or "user",
                                            actor=actor)
                return self._ok({"comment": comment, "comments": store.comments(m.group(1))})
            m = re.match(r"^/api/units/([^/]+)/comments/(\d+)/resolve$", path)
            if m:
                comment = store.resolve_comment(m.group(1), m.group(2), body.get("resolved", True) is not False,
                                                author=self.app.actor() or "user", actor=actor)
                return self._ok({"comment": comment, "comments": store.comments(m.group(1))})
            m = re.match(r"^/api/units/([^/]+)/members$", path)
            if m:
                report = store.design_topic(m.group(1), {**body, "add": body.get("add") or body.get("members") or []},
                                            actor=actor)
                return self._ok({"report": report, **store.topic(m.group(1))})
            m = re.match(r"^/api/units/([^/]+)/archive$", path)
            if m:
                return self._ok(store.archive_unit(m.group(1), actor=actor))
            m = re.match(r"^/api/units/([^/]+)/restore$", path)
            if m:
                return self._ok(store.restore_unit(m.group(1), actor=actor))
            m = re.match(r"^/api/units/([^/]+)/insert-between$", path)
            if m:
                return self._ok(store.insert_between(
                    m.group(1), title=body.get("title") or "New node",
                    type_name=body.get("type") or "note", actor=actor))
            raise ApiError(404, f"Unknown endpoint: {path}")
        except ApiError as exc:
            self._error(exc.status, exc.message, getattr(exc, "code", None) or None)
        except gpu_probe.GpuError as exc:
            self._error(exc.status, exc.message, exc.code)
        except workspace_files.WorkspaceError as exc:
            self._error(exc.status, str(exc))
        except WorktreeError as exc:
            self._error(exc.status, str(exc), exc.code)
        except SessionError as exc:
            self._error(exc.status, str(exc), getattr(exc, "code", "") or None)
        except skills.SkillError as exc:
            self._error(404, str(exc))
        except (StoreError, ProjectError, ValueError) as exc:
            self._error(400, str(exc))
        except sqlite3.Error as exc:
            self._error(500, f"Database error: {exc}")
        except Exception as exc:  # noqa: BLE001
            if os.environ.get("ARK_DEBUG"):
                import traceback
                traceback.print_exc()
            self._error(500, f"Internal error: {exc}")

    # ------------------------------------------------- native password login
    def _mobile_account_permission(self, user: str, project) -> str | None:
        # The per-user tutorial project is not an org project; device sessions
        # (QR or password) are only issued for real granted projects.
        if project is None or not user or project.id == demo.DEMO_ID:
            return None
        return self.app.org.project_permission(user, project.id)

    @staticmethod
    def _device_permission(requested, account_permission: str | None) -> str:
        wanted = str(requested or mobile.DEFAULT_PERMISSION).strip().lower().replace("-", "_")
        if wanted not in mobile.PERMISSIONS:
            raise ApiError(400, "permission must be read_only or full", "INVALID_PERMISSION")
        rank = org_mod.PERMISSION_RANK.get(account_permission or "", -1)
        if wanted == "full" and rank >= org_mod.PERMISSION_RANK["write"]:
            return "full"
        return "read_only"

    def _mobile_project_list(self, user: str) -> list[dict]:
        self.app.set_request_context(user=user)
        return [{"id": item.get("id"), "name": item.get("name"),
                 "permission": item.get("permission")}
                for item in self.app.visible_projects()
                if item.get("id") != demo.DEMO_ID]

    def _mobile_login(self, body: dict):
        """Account sign-in for native clients: password in, mobile token out.

        The browser form sets an in-memory ``ark_session`` cookie that dies
        with the process and lasts hours.  A native app must never keep the
        password, so the verified account instead receives a *device session*
        exactly like a claimed QR pairing: persisted as a hash, listed and
        revocable in Settings -> Mobile, limited by the device allow-list and
        capped at the account's project permission.  No cookie is set.
        """
        if not self.app.auth_enabled:
            raise ApiError(
                400,
                "This ARK server has no account sign-in. Pair the phone with the "
                "QR code from Settings → Mobile instead.",
                "AUTH_NOT_CONFIGURED",
            )
        ctype = str(self.headers.get("Content-Type") or "").split(";")[0].strip().lower()
        if ctype != "application/json":
            # Refuse form posts so a cross-site HTML form cannot drive this.
            raise ApiError(415, "Send the sign-in as application/json.",
                           "UNSUPPORTED_MEDIA_TYPE")
        client = self._client_ip()
        try:
            user = self.app.auth.verify_credentials(
                str(body.get("username") or ""), str(body.get("password") or ""),
                client_ip=client)
        except auth_mod.AuthError as exc:
            headers = None
            if exc.status == 429:
                headers = {"Retry-After": str(max(1, int(exc.retry_after or 60)))}
            self._send(exc.status, {"ok": False, "error": {
                "status": exc.status, "code": exc.code, "message": exc.message}},
                headers=headers)
            return None
        self.app.set_request_context(user=user)
        wanted_id = str(body.get("project_id") or "").strip()
        project = (self.app.project_by_id(wanted_id) if wanted_id
                   else self.app._default_project_for(user))
        account_permission = self._mobile_account_permission(user, project)
        if project is None or not account_permission:
            raise ApiError(
                404 if wanted_id else 409,
                "No project is available for this account yet. Ask an "
                "organization admin for a project grant.",
                "PROJECT_NOT_ACCESSIBLE" if wanted_id else "NO_PROJECT_ACCESS",
            )
        permission = self._device_permission(body.get("permission"), account_permission)
        summary = project.summary()
        agent = self.headers.get("User-Agent") or ""
        device = str(body.get("device") or "").strip()[:80]
        try:
            pair = self.app.mobile.create(
                summary, permission=permission, ttl_seconds=60,
                client=f"login:{client}", user=user,
                session_ttl=body.get("session_ttl"), source="password")
            result = self.app.mobile.claim(
                pair["token"], summary, client=f"login:{client}",
                user_agent=(f"{device} · {agent}" if device else agent))
        except mobile.MobileError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        project.store.log_activity("human:mobile", "mobile.login", None,
                                   {"pair": pair["id"], "permission": permission})
        self.app.org.store.log(self.app.org.user_id_for(user), "mobile.login",
                               project.id, {"pair": pair["id"],
                                            "permission": permission})
        result.update({
            "source": "password",
            "project_id": project.id,
            "account_permission": account_permission,
            "projects": self._mobile_project_list(user),
        })
        return self._ok(result)

    def _mobile_projects(self) -> dict:
        session = self.mobile_session
        if session is None:
            raise ApiError(401, "A mobile session is required.", "MOBILE_TOKEN_REQUIRED")
        record = self.app.mobile.pairing_for(session)
        source = getattr(record, "source", "qr")
        switchable = bool(self.app.auth_enabled and source == "password")
        if switchable:
            projects = self._mobile_project_list(session.user)
        else:
            projects = [{"id": session.project_id, "name": session.project_name,
                         "permission": "read" if session.permission == "read_only"
                         else "write"}]
        return {"projects": projects, "current": session.project_id,
                "switchable": switchable, "permission": session.permission,
                "source": source, "user": session.user or None}

    def _mobile_switch_project(self, body: dict) -> dict:
        session = self.mobile_session
        if session is None:
            raise ApiError(401, "A mobile session is required.", "MOBILE_TOKEN_REQUIRED")
        if not self.app.auth_enabled:
            raise ApiError(403, "This phone is pinned to its paired project.",
                           "MOBILE_PROJECT_PINNED")
        user = str(session.user or "")
        project = self.app.project_by_id(str(body.get("project_id") or ""))
        account_permission = self._mobile_account_permission(user, project)
        if project is None or not account_permission:
            raise ApiError(404, "Project not found or not accessible for this account.",
                           "PROJECT_NOT_ACCESSIBLE")
        permission = self._device_permission(body.get("permission"), account_permission)
        try:
            moved = self.app.mobile.rebind(self._mobile_token(), project.summary(),
                                           permission=permission)
        except mobile.MobileError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        project.store.log_activity("human:mobile", "mobile.switch_project", None,
                                   {"pair": moved.pairing_id, "permission": permission})
        return {"project_id": project.id, "project": project.summary().get("name"),
                "permission": moved.permission,
                "read_only": moved.permission == "read_only"}

    def _claim_pairing(self, body: dict) -> dict:
        token = str(body.get("token") or body.get("code") or "")
        record = self.app.mobile.find(token)
        if record is None:
            # Go through the pairing manager so invalid/expired codes keep the
            # same generic error *and* the claim rate limit.
            try:
                self.app.mobile.claim(token or "", {}, client=self._client_ip(),
                                      user_agent=self.headers.get("User-Agent") or "")
            except mobile.MobileError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
            raise ApiError(401, "The pairing code is invalid or has expired.",
                           "PAIRING_INVALID")
        project = self.app.project_by_id(record.project_id)
        if project is None:
            raise ApiError(404, "The paired project is no longer available.",
                           "MOBILE_WRONG_PROJECT")
        # A QR code is bound to the account that created it.  An already
        # signed-in different account may not redeem it (that would hand it a
        # session for someone else), and a removed/unauthorized account fails.
        current = self.app.actor() if self.app.auth_enabled else ""
        if current and record.user and current.lower() != record.user.lower():
            raise ApiError(
                403,
                "This pairing code belongs to a different ARK account.",
                "PAIRING_WRONG_USER",
            )
        if self.app.auth_enabled:
            if not record.user:
                raise ApiError(403, "This pairing code has no bound account.",
                               "PAIRING_UNBOUND")
            try:
                self.app.org.require_project_permission(record.user, project.id, "read")
            except org_mod.OrgError as exc:
                raise ApiError(exc.status, exc.message, exc.code) from exc
        try:
            result = self.app.mobile.claim(
                token,
                project.summary(),
                client=self._client_ip(),
                user_agent=self.headers.get("User-Agent") or "",
            )
        except mobile.MobileError as exc:
            raise ApiError(exc.status, exc.message, exc.code) from exc
        # The pairing id is fine to log; the token and the mobile session token
        # were never echoed to the activity log.
        project.store.log_activity(
            "mobile:pair", "mobile.pair.claim", None,
            {"pair": result["pairing_id"], "permission": result["permission"]},
        )
        self.app.org.store.log(
            self.app.org.user_id_for(record.user), "mobile.pair.claim", project.id,
            {"pair": result["pairing_id"], "permission": result["permission"]})
        result["user"] = record.user
        return result

    def _agent_write(self, path: str, body: dict, actor: str):
        m = re.fullmatch(r"/api/agent/v1/sessions/([^/]+)/upload", path)
        if m:
            project = self.app.require("write")
            if not self.app.session_uploads_enabled(project):
                raise ApiError(
                    403,
                    "This project does not accept session uploads. A project admin can turn on "
                    "session uploads in Execute > Runs > Session context.",
                    "SESSION_UPLOADS_DISABLED")
            user, admin = self._session_viewer()
            return self._ok(self.app.context_store(project).upload(
                m.group(1), body, actor=actor, owner=user, admin=admin))
        store = self.app.require().store
        m = re.fullmatch(r"/api/agent/v1/units/([^/]+)/history(?:/([^/]+))?", path)
        if m:
            return self._node_history_write(m.group(1), m.group(2), body, actor)
        if path == "/api/agent/v1/gpu/jobs":
            return self._submit_project_gpu_job(body)
        gpu_cancel = re.match(r"^/api/agent/v1/gpu/jobs/([^/]+)/cancel$", path)
        if gpu_cancel:
            self._project_gpu_job(gpu_cancel.group(1), "write")
            return self._ok({"job": self.app.gpu.queue_cancel(gpu_cancel.group(1))})
        m = re.match(r"^/api/agent/v1/setup/([a-z][a-z-]*)$", path)
        if m:
            return self._setup_action(m.group(1), body, actor)
        if path == "/api/agent/v1/project/palette":
            project = self.app.require()
            try:
                settings = settings_patch(project.manifest, {"palette": body})
            except SettingsError as exc:
                raise ApiError(400, str(exc)) from exc
            project.manifest["settings"] = settings
            project.manifest["updated"] = db.utc_now()
            db.write_manifest(project.path, project.manifest)
            return self._ok({"palette": settings["palette"]})
        if path == "/api/agent/v1/units":
            return self._ok({"unit": store.create_unit(body, actor=actor)})
        if path == "/api/agent/v1/links":
            return self._ok(store.create_link(body.get("source"), body.get("target"),
                                              body.get("relation") or "relates_to",
                                              body.get("note") or "", actor=actor))
        if path == "/api/agent/v1/assets/upload":
            return self._ok(self._upload_asset(body, actor))
        if path == "/api/agent/v1/media/upload":
            return self._ok(self._embed_media(body, actor))
        if path == "/api/agent/v1/media":
            return self._ok(store.add_media(body.get("unit"), body.get("url"),
                                            body.get("kind") or "link",
                                            body.get("caption") or "", actor=actor))
        m = re.match(r"^/api/agent/v1/units/([^/]+)/comments$", path)
        if m:
            # Agents answer user comments; any other kind is rejected by the store.
            comment = store.add_comment(m.group(1), body.get("body"), body.get("kind") or "agent_reply",
                                        reply_to=body.get("reply_to"), actor=_agent_actor(actor))
            return self._ok({"comment": comment, **_comments_payload(store.comments(m.group(1)))})
        m = re.match(r"^/api/agent/v1/units/([^/]+)/members$", path)
        if m:
            report = store.design_topic(m.group(1), {**body, "add": body.get("add") or body.get("members") or []},
                                        actor=actor)
            return self._ok({"report": report, **store.topic(m.group(1))})
        m = re.match(r"^/api/agent/v1/units/([^/]+)/comments/(\d+)/resolve$", path)
        if m:
            comment = store.resolve_comment(m.group(1), m.group(2), body.get("resolved", True) is not False,
                                            actor=_agent_actor(actor))
            return self._ok({"comment": comment, **_comments_payload(store.comments(m.group(1)))})
        m = re.match(r"^/api/agent/v1/units/([^/]+)$", path)
        if m:
            return self._ok({"unit": store.update_unit(m.group(1), body, actor=actor)})
        raise ApiError(404, f"Unknown agent endpoint: {path}")

    # ------------------------------------------------------------------ PATCH
    def do_PATCH(self):
        try:
            self._begin_request()
            self._check_host()
            self._guard_public_auth()
            self._guard_mobile()
            self._guard_project_scope()
            if not self._same_origin():
                raise ApiError(403, "Cross-origin writes are rejected")
            path = urlparse(self.path).path
            if path.startswith("/api/task-agent/"):
                raise ApiError(403, "Task-agent sessions have read-only ARK access")
            if self._handle_profile("PATCH", path):
                return
            body = self._read_json()
            actor = self._actor()
            m = re.match(r"^/api/org/users/([^/]+)$", path)
            if m:
                try:
                    user = self.app.org.update_user(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                if body.get("disabled") and self.app.auth_enabled:
                    self.app.auth.revoke_user(str(user.get("username") or ""))
                return self._ok({"user": user,
                                 "users": self.app.org.list_members()})
            m = re.match(r"^/api/org/teams/([^/]+)$", path)
            if m:
                try:
                    team = self.app.org.update_team(self.app.actor(), m.group(1), body)
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"team": team, "teams": self.app.org.list_teams()})
            store = self.app.require().store
            if path == "/api/project":
                return self._ok(self.app.update_project(body))
            if path == "/api/project/settings":
                return self._ok(self.app.update_project_settings(body))
            m = re.fullmatch(r"/api/session-contexts/([^/]+)", path)
            if m:
                self._reject_task_read_token("change stored sessions")
                return self._ok({"session": self.app.update_session_context(
                    m.group(1), body, self._session_viewer())})
            m = re.match(r"^/api/agent-sessions/([^/]+)/steer/([^/]+)$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.update_steer(
                    m.group(1), m.group(2), body.get("text") or ""), "updated": True})
            m = re.match(r"^/api/media/(\d+)$", path)
            if m:
                return self._ok({"media": store.update_media(int(m.group(1)), body, actor=actor)})
            m = re.match(r"^/api/links/(\d+)$", path)
            if m:
                return self._ok(store.update_link(int(m.group(1)), body.get("relation") or "unset", actor=actor))
            m = re.match(r"^/api/units/([^/]+)/members$", path)
            if m:
                report = store.design_topic(m.group(1), body, actor=actor)
                return self._ok({"report": report, **store.topic(m.group(1))})
            m = re.match(r"^/api/units/([^/]+)$", path)
            if m:
                return self._ok({"unit": self.app.require().store.update_unit(m.group(1), body, actor=actor)})
            raise ApiError(404, f"Unknown endpoint: {path}")
        except ApiError as exc:
            self._error(exc.status, exc.message, getattr(exc, "code", None) or None)
        except workspace_files.WorkspaceError as exc:
            self._error(exc.status, str(exc))
        except WorktreeError as exc:
            self._error(exc.status, str(exc), exc.code)
        except SessionError as exc:
            self._error(exc.status, str(exc), getattr(exc, "code", "") or None)
        except (StoreError, ProjectError) as exc:
            self._error(400, str(exc))
        except Exception as exc:  # noqa: BLE001
            self._error(500, f"Internal error: {exc}")

    # ------------------------------------------------------------------ DELETE
    def do_DELETE(self):
        try:
            self._begin_request()
            self._check_host()
            self._guard_public_auth()
            self._guard_mobile()
            self._guard_project_scope()
            if not self._same_origin():
                raise ApiError(403, "Cross-origin writes are rejected")
            path = urlparse(self.path).path
            if path.startswith("/api/task-agent/"):
                raise ApiError(403, "Task-agent sessions have read-only ARK access")
            if self._handle_profile("DELETE", path):
                return
            if path == "/api/mobile/session":
                removed = self.app.mobile.revoke_session(self._mobile_token())
                parts = [f"ark_mobile=", f"Path={self._external_prefix()}",
                         "HttpOnly", "SameSite=Lax", "Max-Age=0"]
                if self._request_is_secure():
                    parts.append("Secure")
                return self._ok({"logged_out": removed},
                                headers={"Set-Cookie": "; ".join(parts)})
            actor = self._actor()
            m = re.match(r"^/api/personal/agent-tokens/([A-Za-z0-9_]+)$", path)
            if m:
                user = self._personal_agent_tokens_guard()
                try:
                    return self._ok(self.app.org.revoke_agent_token(user, m.group(1)))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
            m = re.match(r"^/api/org/users/([^/]+)$", path)
            if m:
                try:
                    result = self.app.org.delete_user(self.app.actor(), m.group(1))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                target = self.app.org.store.user_by_id(m.group(1))
                if self.app.auth_enabled and target:
                    self.app.auth.revoke_user(str(target["username"]))
                return self._ok(result)
            m = re.match(r"^/api/org/users/([^/]+)/linux-accounts/([^/]+)$", path)
            if m:
                try:
                    result = self.app.org.revoke_linux_account(
                        self.app.actor(), m.group(1), unquote(m.group(2)))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok({"result": result,
                                 "linux_accounts": self.app.org.store.linux_accounts_map()})
            m = re.match(r"^/api/org/teams/([^/]+)$", path)
            if m:
                try:
                    result = self.app.org.delete_team(self.app.actor(), m.group(1))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok(result)
            m = re.match(r"^/api/org/teams/([^/]+)/members/([^/]+)$", path)
            if m:
                try:
                    result = self.app.org.remove_team_member(
                        self.app.actor(), m.group(1), m.group(2))
                except org_mod.OrgError as exc:
                    raise ApiError(exc.status, exc.message, exc.code) from exc
                return self._ok(result)
            if path == "/api/project":
                return self._ok(self.app.delete_project(self._read_json()))
            store = self.app.require().store
            if path == "/api/chat":
                return self._ok(self.app.chat_clear())
            m = re.match(r"^/api/workspace/terminals/([^/]+)$", path)
            if m:
                self._reject_workspace_read_token()
                project = self.app.require_admin()
                self.app.workspace.terminals.get(m.group(1), project.id)
                return self._ok(self.app.workspace.terminals.close(m.group(1)))
            m = re.match(r"^/api/agent-sessions/([^/]+)$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok(self.app.sessions.delete(m.group(1)))
            m = re.fullmatch(r"/api/session-contexts/([^/]+)", path)
            if m:
                self._reject_task_read_token("delete stored sessions")
                return self._ok(self.app.delete_session_context(m.group(1), self._session_viewer()))
            m = re.match(r"^/api/agent-sessions/([^/]+)/steer/([^/]+)$", path)
            if m:
                self._guard_session_access(m.group(1), "write")
                return self._ok({"session": self.app.sessions.remove_steer(
                    m.group(1), m.group(2)), "removed": True})
            m = re.match(r"^/api/links/(\d+)$", path)
            if m:
                return self._ok(store.delete_link(int(m.group(1)), actor=actor))
            m = re.match(r"^/api/media/(\d+)$", path)
            if m:
                return self._ok(store.delete_media(int(m.group(1)), actor=actor))
            m = re.match(r"^/api/units/([^/]+)/members/([^/]+)$", path)
            if m:
                return self._ok(store.remove_topic_member(m.group(1), unquote(m.group(2)), actor=actor))
            m = re.match(r"^/api/units/([^/]+)$", path)
            if m:
                return self._ok(store.archive_unit(m.group(1), actor=actor))
            raise ApiError(404, f"Unknown endpoint: {path}")
        except ApiError as exc:
            self._error(exc.status, exc.message, getattr(exc, "code", None) or None)
        except workspace_files.WorkspaceError as exc:
            self._error(exc.status, str(exc))
        except WorktreeError as exc:
            self._error(exc.status, str(exc), exc.code)
        except SessionError as exc:
            self._error(exc.status, str(exc), getattr(exc, "code", "") or None)
        except (StoreError, ProjectError) as exc:
            self._error(400, str(exc))
        except Exception as exc:  # noqa: BLE001
            self._error(500, f"Internal error: {exc}")

    # ------------------------------------------------------------------ static
    def _static(self, path: str):
        if path in ("", "/"):
            path = "/index.html"
        rel = posixpath.normpath(path).lstrip("/")
        if rel.startswith(".."):
            raise ApiError(404, "Not found")
        # The login template contains placeholders and is rendered only by the
        # auth handler; never serve the raw file as a static asset.
        if rel == "login.html":
            raise ApiError(404, "Not found")
        target = (WEB_ROOT / rel).resolve()
        if WEB_ROOT not in target.parents and target != WEB_ROOT:
            raise ApiError(404, "Not found")
        if not target.is_file():
            # Hash routing means the server only needs the real entry file.
            if "." not in Path(path).name:
                target = WEB_ROOT / "index.html"
            else:
                raise ApiError(404, "Not found")
        ctype = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        if target.suffix == ".js":
            ctype = "text/javascript; charset=utf-8"
        elif target.suffix in (".css",):
            ctype = "text/css; charset=utf-8"
        elif target.suffix in (".html",):
            ctype = "text/html; charset=utf-8"
        elif target.suffix in (".svg",):
            ctype = "image/svg+xml"
        elif target.suffix == ".webmanifest":
            ctype = "application/manifest+json; charset=utf-8"
        version = _asset_version()
        requested_version = parse_qs(urlparse(self.path).query).get("v", [""])[0]
        # HTML/login/API responses remain no-store. Only a URL for this exact
        # release may be reused indefinitely by this browser, never a proxy.
        cache_headers = {"Cache-Control": "private, max-age=31536000, immutable"} if (
            requested_version == version and target.suffix != ".html" and rel != "sw.js"
        ) else {"Cache-Control": "no-store"}
        transform = (_index_html if rel == "index.html" else _versioned_html if target.suffix == ".html"
                     else _versioned_js if target.suffix == ".js" else None)
        data, encoding = _static_assets.get(target, version, transform,
            use_gzip=accepts_gzip(self.headers.get("Accept-Encoding") or ""))
        cache_headers["Vary"] = "Accept-Encoding"
        if encoding:
            cache_headers["Content-Encoding"] = encoding
        if rel == "viz-frame.html":
            cache_headers.update({"Content-Security-Policy": self.VIZ_CSP, "X-Frame-Options": "SAMEORIGIN"})
        self._send(200, data, ctype, headers=cache_headers, compress=False)


def serve(app: App, host: str = "127.0.0.1", port: int = 8890, *, block: bool = True):
    loopback_bind = mobile.is_loopback_host(host)
    if not loopback_bind and not app.auth_enabled:
        raise ValueError(
            f"refusing to bind ARK to {host}: public/LAN exposure requires account "
            "authentication. Configure --auth-user with ARK_AUTH_PASSWORD "
            "(or --auth-password / ARK_AUTH_PASSWORD_HASH), or keep the default "
            "127.0.0.1 bind."
        )
    handler = type("BoundHandler", (Handler,), {"app": app})
    httpd = ThreadingHTTPServer((host, port), handler)
    actual_port = httpd.server_address[1]
    app.bind_host = host
    app.port = actual_port
    app.configure_auth_transport()
    base_hosts = {f"{h}:{actual_port}" for h in {host, "127.0.0.1", "localhost"}}
    if host in {"::1", "[::1]"}:
        base_hosts.add(f"[::1]:{actual_port}")
    app.allowed_hosts.update(base_hosts)
    if app.public_url:
        app.allow_advertised_url(app.public_url)
    start_queue = getattr(app.gpu, "start_configured_queue", None)
    if callable(start_queue):
        start_queue()
    url = f"http://{host}:{actual_port}/"
    if not block:
        threading.Thread(target=httpd.serve_forever, daemon=True).start()
        return httpd
    print(f"ARK Studio: {url}")
    print(f"Project root: {app.root}")
    if app.auth_enabled:
        print(f"Sign-in: required (user: {app.auth.username}; "
              f"sessions expire after {app.auth.session_ttl}s)")
    else:
        print("Sign-in: not configured (loopback only)")
    if app.auth_enabled or not loopback_bind:
        print("Agent token: redacted (read runtime/projects/.agent-token)")
    else:
        print(f"Agent token: {app.manager.agent_token}")
    if app.public_url:
        print(f"Mobile public URL: {app.public_url}")
        if not app.auth_enabled:
            print("WARNING: a public URL is configured without account authentication.")
            print("Put an authenticated HTTPS reverse proxy in front, or configure")
            print("--auth-user plus ARK_AUTH_PASSWORD/ARK_AUTH_PASSWORD_HASH.")
        if app.public_url.startswith("http://"):
            print("WARNING: the public URL is plain HTTP. Put an HTTPS reverse proxy in")
            print("front before exposing this to the internet; passwords and cookies")
            print("travel in cleartext otherwise.")
    elif not loopback_bind:
        print("Public bind: pairing uses the request Host or a detected LAN address.")
        print("Set --public-url/ARK_PUBLIC_URL for a stable, verifiable phone URL.")
    if loopback_bind:
        print("Mobile pairing: Settings → Mobile. ARK is loopback-only, so pair")
        print("through the SSH tunnel helper (`python -m arkstudio mobile-tunnel`).")
    print("Press Ctrl+C to stop.")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
    return httpd
