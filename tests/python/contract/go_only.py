"""Intentional Go-only divergences from the frozen Python reference server.

Since the 2026-11-15 switch-over, backend features land in ark-server (ark_studio/server/)
only; ark_studio/python/arkstudio/server.py is frozen.  The contract (gen_contract.py) is
still generated from Python, so a new Go route is not a contract record and
Python cannot answer it.  This module is the allow-list the parity tools
consult:

* ``ROUTES``: Go-only route ids.  ``ark-server routes`` lists them (from
  ``GoOnlyRoutes`` in ark_studio/server/internal/server/routes_agentdocs.go) with the
  ``goonly:`` prefix.  harness.py runs cases that name them only against
  Go and reports SKIP against Python; diffreplay/replay_probe never
  generate steps for them (they are not in contract.json).
* ``AUGMENTED``: Python routes whose Go reply carries extra, Go-only
  members.  diffreplay strips them from the Go body (and ignores the
  body-derived ETag / Content-Length) before the byte comparison, so the
  rest of the payload is still checked byte for byte.  The agent token
  routes go the other way: Go dropped "limit" and the canonicaliser adds
  Python's value back.

* ``FIXES``: intentional behaviour changes on Python routes (bug fixes made
  in Go only).  Each entry names the route, the Go file, a note and a
  ``match(py, go)`` predicate over the two replies (status, headers, body);
  diffreplay counts a mismatching step whose pair matches as an
  *intentional divergence* (listed with the fix id in the summary and the
  report, not a mismatch).  The predicate must be narrow: it recognises
  the Python failure and the Go replacement, so any other difference on the
  same route still fails the replay.

Add an entry here, with the Go file that implements it, whenever a Go-only
route, payload member or behaviour fix is added, together with a Go unit
test for the new behaviour (the replay no longer checks it against Python).
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Callable

PREFIX = "goonly:"

ROUTES = {
    # ark_studio/server/internal/server/routes_agentdocs.go, ark_studio/server/internal/agentdocs
    "goonly:exact:/api/agent/v1/docs": "agent documentation index",
    "goonly:prefix:/api/agent/v1/docs/": "one agent doc (text/markdown, ?section=, ?format=json)",
    # ark_studio/server/internal/server/routes_desktop.go, ark_studio/server/internal/mobile/desktop.go
    "goonly:exact:/api/desktop/link": "ARK Studio desktop link code (signed-in session; claimed with /api/mobile/pair/claim)",
    "goonly:exact:/api/desktop/projects": "desktop-link token: list (GET) and create (POST) projects",
    # ark_studio/server/internal/server/project_reviews.go, ark_studio/server/internal/store/needscards.go,
    # ark_studio/server/internal/org/reviewseen.go (test internal/server/project_reviews_test.go).
    # Table review_seen (statediff GO_ONLY_TABLES).
    "goonly:exact:/api/needs/seen": "project picker badges: the person opened the Needs you list or some of its items",
    # ark_studio/server/internal/server/notifications.go, ark_studio/server/internal/sessions/alerts.go
    # (test internal/server/notifications_test.go)
    "goonly:exact:/api/notifications": "Android notifications: what needs the person across their projects since a cursor",
    # ark_studio/server/internal/server/routes_prompts.go, ark_studio/server/internal/promptlib (tests
    # promptlib_test.go, internal/server/routes_prompts_test.go): the Home prompt library.
    "goonly:exact:/api/prompts": "Home prompt library: custom prompts and Home pins (GET), save/delete/import/pins (POST)",
    "goonly:exact:/api/agent/v1/prompts": "agents list (GET) and save (POST) custom prompts for the token user",
    # ark_studio/server/internal/server/app_manifest.go, ark_studio/server/internal/apkinfo (tests app_manifest_test.go,
    # apkinfo_test.go)
    "goonly:exact:/download/android-apps.json": "Android update check: version, size and SHA-256 of each APK (public)",
    # ark_studio/server/internal/server/routes_agentmarkers.go, ark_studio/server/internal/store/markers (tests markers_test.go,
    # internal/store/markers_test.go, internal/server/markers_test.go)
    "goonly:exact:/api/agent/v1/markers": "node marker groups of the project (tag marker:<id>, exclusive groups)",
    # ark_studio/server/internal/api/chat/plugin.go, ark_studio/server/internal/chat/plugin.go (tests plugin_test.go,
    # internal/server/plugin_chat_test.go)
    # ark_studio/server/internal/server/routes_agentwatch.go, ark_studio/server/internal/agentwatch,
    # ark_studio/server/internal/store/updates.go, agentmsg.go (T-0044; tests agentwatch_test.go,
    # internal/server/agentwatch_test.go)
    "goonly:exact:/api/agent/v1/updates": "updates for the agent since a cursor (activity log), optional long poll",
    "goonly:exact:/api/agent/v1/messages": "experimental: messages a person sent to their agents",
    r"goonly:regex:^/api/agent/v1/messages/(\d+)$": "experimental: one message",
    r"goonly:regex:^/api/agent/v1/messages/(\d+)/(ack|reply)$": "experimental: acknowledge or reply to a message",
    "goonly:exact:/api/agent-messages": "experimental: a signed-in person lists (GET) and sends (POST) messages to agents",
    "goonly:exact:/api/plugins/ai/chat": "D-0003: the ai:chat permission of user view plugins: one conversation with "
                                         "the user's/project's Assistant model; read tools run, write tools come back "
                                         "as node:write proposals; rate-limited, logged in activity, no history",
    # ark_studio/server/internal/server/routes_presence.go, ark_studio/server/internal/presence (tests presence_test.go,
    # internal/server/routes_presence_test.go)
    "goonly:exact:/api/presence": "T-0043: live agent presence of the project (members, read access; memory only)",
    "goonly:exact:/api/agent/v1/presence": "T-0043: an agent announces the unit it works on (state working|done)",
    # ark_studio/server/internal/server/routes_trash.go, ark_studio/server/internal/store/trash.go (tests
    # store/trash_test.go, internal/server/routes_trash_test.go).  The archive activity row also keeps the
    # removed links ("links") and a restore from the bin recreates them (state only; replies unchanged).
    "goonly:exact:/api/trash": "recycle bin: list deleted units (GET), empty the bin for good (DELETE)",
    r"goonly:regex:^/api/trash/([^/]+)$": "recycle bin: delete one unit in the bin for good (DELETE)",
    # ark_studio/server/internal/server/routes_invites.go, ark_studio/server/internal/org/invites.go (test
    # internal/server/routes_invites_test.go).  Tables project_invites and org_options (statediff GO_ONLY_TABLES).
    r"goonly:regex:^/api/org/projects/([^/]+)/invites$": "project invite links: list (GET) and create (POST); project admin",
    r"goonly:regex:^/api/org/projects/([^/]+)/invites/([A-Za-z0-9_]+)/revoke$": "project invite links: revoke one",
    "goonly:exact:/api/org/invite-settings": "organization switch for sign-up through invite links (GET; POST owner)",
    "goonly:exact:/api/auth/invite/preview": "public: what an invite link grants (no sign-in needed)",
    "goonly:exact:/api/auth/invite/accept": "join a project through an invite link with the signed-in account",
    "goonly:exact:/api/auth/invite/signup": "public: create an account through an invite link that allows it, join, sign in",
    # ark_studio/server/internal/server/routes_reviews.go, ark_studio/server/internal/store/reviewrounds.go,
    # reviewgroups.go, anchors.go (tests store/review_test.go, internal/server/routes_reviews_test.go).
    # Tables review_rounds, review_groups, review_group_members, review_group_comments (statediff GO_ONLY_TABLES).
    r"goonly:regex:^/api/units/([^/]+)/review$": "review rounds, note, anchors and changes since the last review",
    r"goonly:regex:^/api/reviews$": "review groups: list (GET) and create (POST)",
    r"goonly:regex:^/api/reviews/([^/]+)$": "one review group (GET), update / resubmit / close (POST)",
    r"goonly:regex:^/api/reviews/([^/]+)/decide$": "a person accepts or sends back a review group",
    r"goonly:regex:^/api/reviews/([^/]+)/comments$": "comment on a review group",
    r"goonly:regex:^/api/reviews/([^/]+)/comments/(\d+)/resolve$": "resolve a review group comment",
    r"goonly:regex:^/api/agent/v1/units/([^/]+)/review$": "agent: review rounds and changes of a unit",
    r"goonly:regex:^/api/agent/v1/reviews$": "agent: list (GET) and create (POST) review groups",
    r"goonly:regex:^/api/agent/v1/reviews/([^/]+)$": "agent: one review group (GET), update / resubmit / close (POST)",
    r"goonly:regex:^/api/agent/v1/reviews/([^/]+)/comments$": "agent: reply in a review group",
    r"goonly:regex:^/api/agent/v1/reviews/([^/]+)/comments/(\d+)/resolve$": "agent: resolve a review group comment",
    # ark_studio/server/internal/server/routes_importark.go, ark_studio/server/internal/store/importark.go,
    # xproject.go (T-0070; tests store/importark_test.go, internal/server/routes_importark_test.go).
    "goonly:prefix:/api/import-ark/": "import (copy) units from another ARK project: source graph, preview, apply",
    # ark_studio/server/internal/server/routes_agentlinks.go, ark_studio/server/internal/store/linkquery.go (test
    # internal/server/routes_agentlinks_test.go); writes go through store DeleteLink / UpdateLink.
    "goonly:exact:/api/agent/v1/links": "agent: delete a link by {source, target, relation?} (DELETE; POST is Python's)",
    r"goonly:regex:^/api/agent/v1/links/(\d+)$": "agent: delete (DELETE) or change the relation of (PATCH) one link",
    r"goonly:regex:^/api/agent/v1/units/([^/]+)/links$": "agent: list one unit's links, in and out",
    # ark_studio/server/internal/server/routes_storage.go, ark_studio/server/internal/project/storage.go (tests
    # project/storage_test.go, internal/server/routes_storage_test.go).  Option "project_storage" in org_options.
    # With a folder chosen, GET /api/projects lists its projects too, POST /api/projects creates there (and adds
    # "storage_notice" when that folder is missing), and in local mode POST /api/projects/open {path} lists the
    # folder of an ARK project outside the listed folders; without one (every replay) nothing changes.
    "goonly:exact:/api/org/project-storage": "where new projects are stored: show (GET), check / change / unlist a folder (POST); local mode or org admin",
    "goonly:exact:/api/projects/reveal": "local mode: show a project or listed folder in the file manager",
    # ark_studio/server/internal/server/routes_projectmove.go, ark_studio/server/internal/store/move.go,
    # moveplans.go, clusters.go (T-0068; tests store/move_test.go, internal/server/routes_projectmove_test.go).
    # Table unit_aliases (statediff GO_ONLY_TABLES); plans and journals in <project>/moves/.
    "goonly:prefix:/api/project-move/": "split / merge projects: clusters, move plans, approve / reject (a person), apply, undo",
    "goonly:prefix:/api/agent/v1/project-move/": "agent: clusters, propose move plans, apply an approved plan, undo",
}

# Go-only query parameter (no reply difference on any replayed request: the
# replays never send it): ``GET /api/events?presence=1`` adds "event: presence"
# frames (T-0043, routes_presence.go; test routes_presence_test.go
# TestPresenceEventStream).  Agent reads and writes only update the in-memory
# presence; their replies are unchanged.
QUERY_PARAMS = {
    "/api/events?presence=1": "T-0043: agent presence frames on the event stream",
}

# Go-only request header (no reply difference on any replayed request: the
# replays never send it): ``X-ARK-Plugin: <plugin id>`` on a signed-in
# browser write without an agent token or X-Agent-Name makes the write actor
# "human:plugin:<id>" instead of "human:ui" (D-0003, view plugins' node:write;
# ark_studio/server/internal/server/handler.go writeActor, test
# plugin_chat_test.go TestPluginWriteActor).
HEADERS = {
    "X-ARK-Plugin": "write actor human:plugin:<id> for user view plugin writes (D-0003)",
}


def is_go_only(route_id: str) -> bool:
    return route_id.startswith(PREFIX)


# links.list / links.delete / links.update: routes_agentlinks.go (Python's links.write stays).
# markers.list: routes_agentmarkers.go (node marker groups).
GO_ONLY_CAPABILITIES = ("docs.", "watch.", "messages.", "links.list", "links.delete", "links.update", "project_move.",
                        "markers.list")


def _strip_interface_docs(body: bytes) -> bytes:
    """Remove the Go-only docs.*, watch.*, messages.* link-edit and project_move.* capabilities and the "docs" member."""
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    if not isinstance(payload, dict):
        return body
    payload.pop("docs", None)
    caps = payload.get("capabilities")
    if isinstance(caps, list):
        payload["capabilities"] = [c for c in caps
                                   if not (isinstance(c, dict) and str(c.get("id", "")).startswith(GO_ONLY_CAPABILITIES))]
    return json.dumps(payload, ensure_ascii=False).encode()


def _strip_storage(*path: str):
    """A canonicaliser removing the Go-only "storage" member of every object
    in the list at ``path`` (the GPU servers' df figures, gpu/storage.go)."""
    def strip(body: bytes) -> bytes:
        try:
            payload = json.loads(body)
        except ValueError:
            return body
        items = payload
        for key in path:
            items = items.get(key) if isinstance(items, dict) else None
        if not isinstance(items, list):
            return body
        for item in items:
            if isinstance(item, dict):
                item.pop("storage", None)
        return json.dumps(payload, ensure_ascii=False).encode()
    return strip


def _strip_registration(body: bytes) -> bytes:
    """Remove the Go-only top-level "registration" member (T-0018,
    ark_studio/server/internal/gpu/scope.go Service.registration)."""
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    if not isinstance(payload, dict) or "registration" not in payload:
        return body
    payload.pop("registration")
    return json.dumps(payload, ensure_ascii=False).encode()


def _strip_member(body: bytes, name: str) -> bytes:
    """Remove one Go-only top-level member."""
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    if not isinstance(payload, dict) or name not in payload:
        return body
    payload.pop(name)
    return json.dumps(payload, ensure_ascii=False).encode()


NEEDS_REVIEW_MEMBERS = ("parents", "links", "delivery", "verdict", "review_round", "review_since", "review_by",
                        "review_note", "review_anchors", "review_base", "review_changes", "review_focus", "review_prev",
                        "auto_group")


def _strip_needs_review(body: bytes) -> bytes:
    """Remove the Go-only review members of needs_you items, the "new" flag of
    their media and the review group items."""
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    items = payload.get("needs_you") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        return body
    kept = []
    for item in items:
        if not isinstance(item, dict) or item.get("kind") == "review_group":
            continue
        for name in NEEDS_REVIEW_MEMBERS:
            item.pop(name, None)
        for media in item.get("media") or []:
            if isinstance(media, dict):
                media.pop("new", None)
        kept.append(item)
    payload["needs_you"] = kept
    return json.dumps(payload, ensure_ascii=False).encode()


# Python's token list reply carries "limit": 10 (agent_tokens.MAX_ACTIVE_TOKENS);
# ark-server has no cap and sends no "limit" since 2026-11-23
# (ark_studio/server/internal/org/admin.go ListAgentTokens; test
# starter_token_test.go TestAgentTokensNoLimit).  The canonicaliser puts
# Python's member back in its place so the rest of the reply is still compared.
PY_TOKEN_LIMIT = 10
TOKENS_PATH = "/api/personal/agent-tokens"


def _restore_token_limit(body: bytes) -> bytes:
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    if not isinstance(payload, dict) or "tokens" not in payload or "limit" in payload:
        return body
    out = {}
    for key, value in payload.items():
        out[key] = value
        if key == "tokens":
            out["limit"] = PY_TOKEN_LIMIT
    return json.dumps(out, ensure_ascii=False).encode()


def _chain(*strips):
    def strip(body: bytes) -> bytes:
        for f in strips:
            body = f(body)
        return body
    return strip


AUGMENTED = {
    "/api/agent/v1/interface": _strip_interface_docs,  # routes_agentdocs.go agentInterfaceEncoded
    # ark_studio/server/internal/gpu/storage.go (ProbeServer "storage"; test storage_test.go),
    # ark_studio/server/internal/gpu/scope.go ("registration"; test scope_test.go)
    "/api/gpu/probe": _chain(_strip_storage("servers"), _strip_registration),
    "/api/gpu/dashboard": _chain(_strip_storage("probe", "servers"), _strip_registration),
    "/api/gpu/servers": _strip_registration,
    # ark_studio/server/internal/resources/graph.go gpuResources (device "storage"; test storage_test.go)
    "/api/resources/graph": _strip_storage("devices"),
    # ark_studio/server/internal/store/overview.go agentWriters (T-0030: Home's agent
    # connection state; test internal/server/home_onboarding_test.go)
    "/api/project/overview": lambda body: _strip_member(body, "agents"),
    # ark_studio/server/internal/store/reviewrounds.go enrichReviews and reviewgroups.go: the review
    # members of needs_you items and the review group items (tests store/review_test.go).
    "/api/project": _strip_needs_review,
    TOKENS_PATH: _restore_token_limit,
}

# The Go system query ends with a read-only df section (ark_studio/server/internal/gpu/
# storage.go StorageCommand); the GPU replay's fake ssh compares the argv
# both servers sent after removing it (replay_gpu.fake_ssh_problems).
GO_ONLY_SSH_SUFFIX = ("\necho __ARK_DF__; df -PB1 -x tmpfs -x devtmpfs -x overlay -x squashfs 2>/dev/null "
                      "|| df -PB1 2>/dev/null || true")


def ssh_call_canonical(argv: list) -> list:
    """An ssh argv as the Python server sends it (Go-only remote additions removed)."""
    return [a.replace(GO_ONLY_SSH_SUFFIX, "") if isinstance(a, str) else a for a in argv]


def augmented(path: str):
    """The Go-body canonicaliser for a request path (query ignored), or None."""
    path = path.split("?", 1)[0]
    if path.startswith(TOKENS_PATH + "/"):  # DELETE one token: the list follows
        return _restore_token_limit
    return AUGMENTED.get(path)


@dataclass(frozen=True)
class Fix:
    id: str
    method: str          # "GET" also covers HEAD
    path: str            # request path without the query
    go_file: str
    note: str
    match: Callable      # match(py_response, go_response) -> bool


def _resources_nonfinite(py, go) -> bool:
    return (py.status == 500 and go.status == 200
            and b"Internal error: cannot convert float " in py.body
            and (b"infinity to integer" in py.body or b"NaN to integer" in py.body))


# ------------------------------------------------- T-0018: GPU project scope
# ark-server scopes the server-wide GPU list to the projects whose setup
# registers a server (compute resource with gpu_server), and shows connection
# details only to the org owner/admin or in local mode
# (ark_studio/server/internal/gpu/scope.go; tests scope_test.go,
# internal/server/routes_gpu_test.go; docs/operations/gpu.md "Project scope").
# The GPU replay registers every server it configures (replay_gpu.register_servers),
# so the remaining differences are: a named server that is not registered
# (Python 400 unknown -> Go 404), the member view in --auth runs, and setup
# discovery offering names only.

GPU_SCOPE_FILE = "ark_studio/server/internal/gpu/scope.go (Scope; test scope_test.go)"
# Members that the member view leaves out (Go) or nulls (the graph's detail).
GPU_REDACTED = frozenset({"config_path", "defaults", "ssh_preview", "host", "port", "user", "note",
                          "timeout_seconds"})
# Members whose content the member view replaces (generic how-to, empty path map)
# or that only Go sends.
GPU_REPLACED = frozenset({"how_to_enable", "path_map", "registration"})
# scope.go redactErr: the member text of the config errors (no config path).
GPU_MEMBER_ERRORS = {
    "config_missing": "No GPU server list is configured on this ARK server; ask a server admin.",
    "config_invalid": "The GPU server list on this ARK server is invalid; ask a server admin to fix it.",
    "disabled": "GPU probing is disabled on this ARK server; ask a server admin to enable it.",
}


def _json(body):
    try:
        return json.loads(body)
    except (ValueError, TypeError):
        return None


def _shape_equal(py, go, may_drop=frozenset(), drop=frozenset()) -> bool:
    """The two JSON values have the same structure: the same keys (minus
    ``drop``, and minus keys in ``may_drop`` that Go left out), list lengths
    and booleans.  Strings, numbers and null are not compared: the raw bodies
    of the two servers differ in paths and clocks (the normalised comparison
    already failed), and the member view nulls or scrubs some strings."""
    if isinstance(py, dict):
        if not isinstance(go, dict):
            return False
        keys = {k for k in py if k not in drop and not (k in may_drop and k not in go)}
        if keys != {k for k in go if k not in drop}:
            return False
        return all(_shape_equal(py[k], go[k], may_drop, drop) for k in keys)
    if isinstance(py, list):
        return (isinstance(go, list) and len(py) == len(go)
                and all(_shape_equal(a, b, may_drop, drop) for a, b in zip(py, go)))
    if isinstance(py, bool) or isinstance(go, bool):
        return py is go
    return not isinstance(go, (dict, list))


def _walk(value, keys: frozenset):
    """(dict, key) for every member named in ``keys``, recursively."""
    if isinstance(value, dict):
        for k, v in value.items():
            if k in keys:
                yield value, k
            yield from _walk(v, keys)
    elif isinstance(value, list):
        for v in value:
            yield from _walk(v, keys)


def _redaction_evidence(py, go) -> bool:
    """Python sent a connection detail somewhere Go left it out or null."""
    if isinstance(py, dict) and isinstance(go, dict):
        for k in GPU_REDACTED:
            if py.get(k) not in (None, "", []) and (k not in go or go[k] is None):
                return True
        return any(_redaction_evidence(py[k], go[k]) for k in py if k in go)
    if isinstance(py, list) and isinstance(go, list):
        return any(_redaction_evidence(a, b) for a, b in zip(py, go))
    return False


def _gpu_member_view(py, go) -> bool:
    """The member view of a GPU reply: same status (200), Go has no config
    path or SSH preview anywhere, Python's connection details are missing or
    null in Go, and otherwise the same structure."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict):
        return False
    if any(True for _ in _walk(b, frozenset({"config_path", "ssh_preview"}))):
        return False
    return _redaction_evidence(a, b) and _shape_equal(a, b, GPU_REDACTED, GPU_REPLACED)


def _gpu_member_config_error(py, go) -> bool:
    """A config error (missing, invalid, disabled) whose member text names no path."""
    a, b = _json(py.body), _json(go.body)
    ea = a.get("error") if isinstance(a, dict) else None
    eb = b.get("error") if isinstance(b, dict) else None
    if not isinstance(ea, dict) or not isinstance(eb, dict) or py.status != go.status:
        return False
    code = ea.get("code")
    return (code in GPU_MEMBER_ERRORS and eb.get("code") == code and eb.get("message") == GPU_MEMBER_ERRORS[code]
            and ea.get("message") != eb.get("message"))


def _gpu_not_registered(py, go) -> bool:
    """A named server the project does not register: Python 400 'unknown
    server', Go 404 GPU_SERVER_NOT_REGISTERED (unknown names look the same)."""
    if py.status != 400 or go.status != 404:
        return False
    a, b = _json(py.body), _json(go.body)
    ea = a.get("error") if isinstance(a, dict) else None
    eb = b.get("error") if isinstance(b, dict) else None
    return (isinstance(ea, dict) and isinstance(eb, dict) and eb.get("code") == "GPU_SERVER_NOT_REGISTERED"
            and "nknown server" in str(ea.get("message", "")))


def _gpu_compute(value):
    return [d for d in (x for x, _ in _walk(value, frozenset({"gpu_server"})))]


def _discover_names_only(py, go) -> bool:
    """Setup discovery reads only the server names (no tags, so the proposed
    GPU compute entries have no note); everything else has the same structure."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict):
        return False
    pa, pb = _gpu_compute(a), _gpu_compute(b)
    if not pa or len(pa) != len(pb) or not any(d.get("note") for d in pa) or any(d.get("note") for d in pb):
        return False
    return _shape_equal(a, b, frozenset({"note"}))


def _gpu_overview_unregistered(py, go) -> bool:
    """The project overview of a project that registers none of the configured
    servers: Go counts 0 GPU servers (and hides the last probe summary of
    servers outside the project) where Python counts the whole list."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    ga = a.get("gpu") if isinstance(a, dict) else None
    gb = b.get("gpu") if isinstance(b, dict) else None
    if not isinstance(ga, dict) or not isinstance(gb, dict):
        return False
    if not (isinstance(ga.get("servers"), int) and ga["servers"] > 0 and gb.get("servers") == 0):
        return False
    return _shape_equal(a, b)


GPU_MEMBER_ROUTES = [("GET", "/api/gpu/status"), ("GET", "/api/gpu/servers"), ("GET", "/api/gpu/queue"),
                     ("GET", "/api/gpu/dashboard"), ("POST", "/api/gpu/probe"),
                     ("POST", "/api/gpu/dispatch/prepare"), ("GET", "/api/resources/graph")]
GPU_CONFIG_ERROR_ROUTES = [("GET", "/api/gpu/dashboard"), ("POST", "/api/gpu/probe"),
                           ("POST", "/api/gpu/dispatch/prepare")]
GPU_NAMED_ROUTES = [("POST", "/api/gpu/probe"), ("POST", "/api/gpu/dispatch/prepare"), ("POST", "/api/gpu/jobs"),
                    ("POST", "/api/agent/v1/gpu/jobs"), ("POST", "/api/task-agent/v1/gpu/jobs")]


def _workbench_current_layout(py, go) -> bool:
    """T-0024: prefs.workbench.layout (the current arrangement) is refused by
    Python (400 invalid workbench prefs) and stored by Go."""
    return (py.status == 400 and b"invalid workbench layout preferences" in py.body
            and go.status == 200 and b'"workbench"' in go.body and b'"layout"' in go.body)


# ------------------------------------------- T-0029: APKs from GitHub releases
# ark-server links the Android apps to the latest GitHub release, resolved by
# the server (app_release.go): /api/mobile/apps reports GitHub URLs (every app
# available) with Go-only members, and /download/<apk> without a dist/ build
# redirects there (302) instead of Python's 404 APK_NOT_FOUND.  Replays run with
# ARK_REPLAY_CLOCK=1, so the URL is the release page (no network).

APK_RELEASE_FILE = ("ark_studio/server/internal/server/app_release.go (releaseResolver; "
                    "test routes_mobile_apps_test.go)")
APK_PATHS = ("/download/ARK-Studio-android.apk", "/download/ark.apk", "/download/ARK-Studio-webapp.apk")
_APP_KEYS = frozenset({"available", "url", "file", "app_id", "title", "size", "updated"})
_APP_GO_KEYS = _APP_KEYS | {"direct", "version", "qr_svg", "local", "local_url"}


def _header(r, name: str) -> str:
    if hasattr(r, "header"):
        return r.header(name)
    return (r.headers or {}).get(name, "")


def _apk_redirect(py, go) -> bool:
    return (py.status == 404 and (not py.body or b'"APK_NOT_FOUND"' in py.body)
            and go.status == 302 and not go.body
            and _header(go, "location").startswith("https://"))


def _mobile_apps_release(py, go) -> bool:
    a, b = _json(py.body), _json(go.body)
    if py.status != 200 or go.status != 200 or not isinstance(a, dict) or not isinstance(b, dict):
        return False
    if set(a) != {"ok", "apps"} or set(b) != {"ok", "apps", "version", "release_url"}:
        return False
    # Go lists only the native app; the WebView app (Python's "webapp") is no longer built.
    if not str(b["release_url"]).startswith("https://") or set(a["apps"]) != {"native", "webapp"}             or set(b["apps"]) != {"native"}:
        return False
    for key in ("native",):
        pa, ga = a["apps"][key], b["apps"][key]
        if set(pa) != _APP_KEYS or set(ga) != _APP_GO_KEYS:
            return False
        if (ga["app_id"], ga["title"]) != (pa["app_id"], pa["title"]) or ga["available"] is not True                 or not str(ga["url"]).startswith("https://") or ga["local_url"] != pa["url"]:
            return False
    return True


def _plugin_frame_csp(py, go) -> bool:
    """T-0025: web/plugin-frame.html is served with the strict plugin CSP by Go."""
    return (py.status == 200 and go.status == 200
            and "connect-src 'none'" in str(go.headers.get("Content-Security-Policy", "") if hasattr(go.headers, "get") else go.headers)
            and py.body == go.body)


FIXES = [
    Fix("plugin-frame-csp", "GET", "/plugin-frame.html",
        "ark_studio/server/internal/server/static.go (respond.go PluginCSP; test plugin_frame_test.go)",
        "T-0025: the user view plugin host page gets a sandbox CSP without network access "
        "(Python serves it with the app's default CSP)",
        _plugin_frame_csp),
    Fix("workbench-current-layout", "PATCH", "/api/me",
        "ark_studio/server/internal/settings/workbench.go (workbenchPrefs; test workbench_test.go)",
        "T-0024: the profile's prefs.workbench also keeps the user's current layout (\"layout\", "
        "validated like a preset layout, with its save time \"layout_at\") so dragged views follow "
        "the account; Python refuses the members",
        _workbench_current_layout),
    Fix("mobile-apps-github-release", "GET", "/api/mobile/apps", APK_RELEASE_FILE,
        "T-0029: the Android apps link to the latest GitHub release (always available, with "
        "version, release_url, direct, qr_svg, local, local_url) instead of this server's dist/",
        _mobile_apps_release),
] + [
    Fix("apk-download-redirect", "GET", path, APK_RELEASE_FILE,
        "T-0029: without a dist/ build the APK download redirects (302) to the latest GitHub "
        "release instead of answering 404 APK_NOT_FOUND",
        _apk_redirect)
    for path in APK_PATHS
] + [
    Fix("resources-graph-nonfinite-gpu-memory", "GET", "/api/resources/graph",
        "ark_studio/server/internal/resources/graph.go (roundMB, finiteMB; test round_test.go)",
        "a GPU probe reporting an infinite/NaN memory figure: Python's round() raises and the "
        "whole graph answers 500; Go reports those figures as null",
        _resources_nonfinite),
] + [
    Fix("gpu-member-view", method, path, GPU_SCOPE_FILE,
        "T-0018: a non-admin member gets no GPU host, port, user, note, config path or SSH preview "
        "(Python shows them to everyone with access to any project)",
        _gpu_member_view)
    for method, path in GPU_MEMBER_ROUTES
] + [
    Fix("gpu-member-config-error", method, path, GPU_SCOPE_FILE,
        "T-0018: a member's GPU config error names no private config path",
        _gpu_member_config_error)
    for method, path in GPU_CONFIG_ERROR_ROUTES
] + [
    Fix("gpu-server-not-registered", method, path, GPU_SCOPE_FILE,
        "T-0018: a server the project does not register (or the list does not know) is refused "
        "with 404 GPU_SERVER_NOT_REGISTERED instead of Python's 400 unknown server",
        _gpu_not_registered)
    for method, path in GPU_NAMED_ROUTES
] + [
    Fix("gpu-overview-unregistered", "GET", "/api/project/overview", GPU_SCOPE_FILE,
        "T-0018: the overview of a project that registers no GPU server counts none",
        _gpu_overview_unregistered),
    Fix("setup-discover-gpu-names-only", "POST", "/api/agent/v1/setup/discover",
        "ark_studio/server/internal/server/routes_setup.go (setupGPUServers; test routes_gpu_test.go)",
        "T-0018: setup discovery offers the configured GPU server names only (no tags in the "
        "proposed compute note)",
        _discover_names_only),
]


# ------------------------------------- T-0030: Home and onboarding (line A)
# User decision (2026-11-16): a project created from the blank template has
# no built-in topics; GET /api/topics no longer creates Story / Related work /
# Method / Experiment there (routes_views.go blankTemplate).  Paper, demo and
# tutorial projects keep them.  The project overview leaves topics out of the
# KPIs and Team load (overview.go).  Both: test
# internal/server/home_onboarding_test.go.  The example seeds' relative dates
# (project/create.go SeedShiftDays) are off under ARK_REPLAY_CLOCK=1, so the
# replay sees the recorded dates.

HOME_TOPICS_FILE = ("ark_studio/server/internal/server/routes_views.go (blankTemplate; "
                    "test internal/server/home_onboarding_test.go)")
HOME_KPI_FILE = ("ark_studio/server/internal/store/overview.go (ProjectOverview; "
                 "test internal/server/home_onboarding_test.go)")
# store.DefaultTopics: the built-in topic keys and titles.
DEFAULT_TOPIC_KEYS = frozenset({"story", "related", "method", "experiment"})
DEFAULT_TOPIC_TITLES = frozenset({"Story", "Related work", "Method", "Experiment"})


def _is_default_topic(item) -> bool:
    """A built-in topic as a unit (type topic with a built-in default key, or
    a built-in title made by the system) or as an activity row about one."""
    if not isinstance(item, dict):
        return False
    if item.get("type") == "topic":
        meta = item.get("meta")
        key = item.get("default") or (meta.get("default") if isinstance(meta, dict) else None)
        return key in DEFAULT_TOPIC_KEYS or (
            key is None and item.get("title") in DEFAULT_TOPIC_TITLES and item.get("origin") == "system")
    return (item.get("unit_type") == "topic" and item.get("unit_title") in DEFAULT_TOPIC_TITLES
            and str(item.get("actor", "")).startswith("system"))


def _without_default_topics(value, dropped: list):
    """value with every built-in topic (and the "topic" key of type counts)
    removed; what was removed goes to dropped."""
    if isinstance(value, list):
        out = []
        for item in value:
            if _is_default_topic(item):
                dropped.append(item)
            else:
                out.append(_without_default_topics(item, dropped))
        return out
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if k == "by_type" and isinstance(v, dict) and "topic" in v:
                v = {t: n for t, n in v.items() if t != "topic"}
            out[k] = _without_default_topics(v, dropped)
        return out
    return value


def _blank_no_default_topics(py, go) -> bool:
    """A blank project: Python created the built-in topics (GET /api/topics),
    Go did not.  With them removed from Python's reply (list items, the
    "topic" type count) the two replies have the same structure, and Go's
    reply has no built-in topic at all."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict):
        return False
    dropped: list = []
    stripped = _without_default_topics(a, dropped)
    left: list = []
    _without_default_topics(b, left)
    return bool(dropped) and not left and _shape_equal(stripped, b)


OPEN_STATUSES = ("inbox", "open", "active", "review", "blocked")


def _overview_kpis_without_topics(py, go) -> bool:
    """GET /api/project/overview: Go's KPIs and Team load leave topics out.
    Every other member has the same structure; each KPI "now" (and the open
    work of the Team load) is lower by at most the topics in that status, at
    least one is lower, the 14-day figures are equal and Go's deltas match its
    own numbers."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict):
        return False
    b = {k: v for k, v in b.items() if k != "agents"}
    graph = a.get("graph")
    nodes = graph.get("nodes") if isinstance(graph, dict) else None
    if not isinstance(nodes, list):
        return False
    by_status: dict = {}
    for n in nodes:
        if isinstance(n, dict) and n.get("type") == "topic":
            by_status[n.get("status")] = by_status.get(n.get("status"), 0) + 1
    if not by_status:
        return False
    open_topics = sum(by_status.get(s, 0) for s in OPEN_STATUSES)
    ceiling = {"active": by_status.get("active", 0), "review": by_status.get("review", 0),
               "blocked": by_status.get("blocked", 0), "open": open_topics, "overdue": open_topics}
    rest = ("kpis", "members", "unassigned", "store_ms")
    if set(a) != set(b) or not _shape_equal({k: v for k, v in a.items() if k not in rest},
                                            {k: v for k, v in b.items() if k not in rest}):
        return False
    ka, kb = a.get("kpis"), b.get("kpis")
    if not isinstance(ka, dict) or not isinstance(kb, dict) or set(ka) != set(kb):
        return False
    changed = False
    for key, limit in ceiling.items():
        x, y = ka.get(key), kb.get(key)
        if not isinstance(x, dict) or not isinstance(y, dict) or set(x) != set(y):
            return False
        diff = x.get("now", 0) - y.get("now", 0)
        if not 0 <= diff <= limit:
            return False
        changed = changed or diff > 0
        if "delta" in y and y["delta"] != y["now"] - y["prev"]:
            return False
    if any(ka.get(key) != kb.get(key) for key in ("created", "completed")):
        return False

    def open_work(payload):
        rows = [payload.get("unassigned") or {}] + list(payload.get("members") or [])
        return sum(r.get("open", 0) for r in rows if isinstance(r, dict))
    lost = open_work(a) - open_work(b)
    return changed and 0 <= lost <= open_topics


HOME_FIXES = [
    Fix("blank-project-no-default-topics", method, path, HOME_TOPICS_FILE,
        "T-0030 (user decision 2026-11-16): a blank project has no built-in topics; GET /api/topics "
        "does not create Story / Related work / Method / Experiment there (Python creates them on "
        "a writer's first GET), so later replies about that project lack them too",
        _blank_no_default_topics)
    for method, path in (("GET", "/api/topics"), ("POST", "/api/projects/open"), ("GET", "/api/project"),
                         ("GET", "/api/bootstrap"), ("GET", "/api/units"), ("GET", "/api/graph/snapshot"))
] + [
    Fix("overview-kpis-without-topics", "GET", "/api/project/overview", HOME_KPI_FILE,
        "T-0030: the Home KPIs (active, open, review, blocked, overdue and their last-week figures) "
        "and Team load leave topics out; an empty project no longer reads \"Active 4\"",
        _overview_kpis_without_topics),
]

FIXES += HOME_FIXES


# ------------------------------------- T-0028: Assistant chat per user
# The Assistant thread is strictly the viewer's own rows.  Python also shows
# every member the pre-v10 rows (empty user); ark-server shows them only in
# local mode, and on an account server gives them to the project owner once
# (store.AdoptLegacyChat, meta migration.chat_legacy_owner).  A project ZIP
# keeps only the requester's own chat unless a project admin downloads it in
# the browser (routes_exports.go archiveChatUser, project.WriteArchiveChat).
# Local-mode replies are unchanged; the --auth replay (as user0001, a member)
# differs on the steps below.

CHAT_FILE = ("ark_studio/server/internal/store/chat.go (ChatHistory, ClearChat, AdoptLegacyChat; "
             "tests chat_test.go, internal/server/chat_scope_test.go)")
EXPORT_CHAT_FILE = ("ark_studio/server/internal/project/archive.go (WriteArchiveChat, keepOwnChat; "
                    "test internal/server/chat_scope_test.go)")
ARCHIVE_CHAT_EXCLUDED = "other users' Assistant chat"  # project.ArchiveChatExcluded
LEGACY_CHAT_OWNER = "migration.chat_legacy_owner"      # store.LegacyChatOwner


def _chat_messages(value):
    msgs = value.get("messages") if isinstance(value, dict) else None
    if not isinstance(msgs, list) or not all(isinstance(m, dict) and isinstance(m.get("id"), int) for m in msgs):
        return None
    return msgs


def _chat_own_thread(py, go) -> bool:
    """GET /api/chat: Go leaves out the oldest rows of Python's list (the
    pre-v10 rows); what both lists share is the end of Go's list (Go's window
    may reach further back into the viewer's own rows), and the rest of the
    reply has the same structure."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    ma, mb = _chat_messages(a), _chat_messages(b)
    if ma is None or mb is None:
        return False
    go_ids = [m["id"] for m in mb]
    in_go = set(go_ids)
    dropped = [m for m in ma if m["id"] not in in_go]
    kept = [m for m in ma if m["id"] in in_go]
    if not dropped or (mb and any(m["id"] >= min(go_ids) for m in dropped)):
        return False
    tail = mb[len(mb) - len(kept):]
    if [m["id"] for m in kept] != [m["id"] for m in tail]:
        return False
    if any((x.get("role"), x.get("content")) != (y.get("role"), y.get("content")) for x, y in zip(kept, tail)):
        return False
    rest_a = {k: v for k, v in a.items() if k != "messages"}
    rest_b = {k: v for k, v in b.items() if k != "messages"}
    return _shape_equal(rest_a, rest_b)


def _chat_clear_count(py, go) -> bool:
    """DELETE /api/chat: Go clears fewer rows (not the pre-v10 ones)."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict) or set(a) != set(b) or set(a) != {"ok", "cleared"}:
        return False
    ca, cb = a.get("cleared"), b.get("cleared")
    return (type(ca) is int and type(cb) is int and 0 <= cb < ca
            and a.get("ok") is True and b.get("ok") is True)


def _chat_clear_activity(py, go) -> bool:
    """GET /api/activity: the same entries; only chat.clear details count
    fewer messages in Go."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    la = a.get("activity") if isinstance(a, dict) else None
    lb = b.get("activity") if isinstance(b, dict) else None
    if not isinstance(la, list) or not isinstance(lb, list) or len(la) != len(lb) or set(a) != set(b):
        return False
    changed = False
    for x, y in zip(la, lb):
        if not isinstance(x, dict) or not isinstance(y, dict) or x.get("action") != y.get("action"):
            return False
        if x.get("action") != "chat.clear":
            if not _shape_equal(x, y):
                return False
            continue
        dx, dy = x.get("detail"), y.get("detail")
        if not (isinstance(dx, dict) and isinstance(dy, dict) and set(dx) == set(dy) == {"messages"}):
            return False
        if set(x) != set(y):
            return False
        if dx["messages"] != dy["messages"]:
            if not (type(dx["messages"]) is int and type(dy["messages"]) is int
                    and 0 <= dy["messages"] < dx["messages"]):
                return False
            changed = True
    return changed


def _zip_db_state(body: bytes):
    """(entry names, manifest, table row counts, chat rows, meta) of a
    project ZIP, or None."""
    import io
    import sqlite3
    import tempfile
    import zipfile
    from pathlib import Path
    try:
        archive = zipfile.ZipFile(io.BytesIO(body))
    except zipfile.BadZipFile:
        return None
    with archive, tempfile.TemporaryDirectory() as scratch:
        names = sorted(archive.namelist())
        manifest = _json(archive.read("ARK-EXPORT.json")) if "ARK-EXPORT.json" in names else None
        dbs = [n for n in names if n.endswith("/ark.sqlite3")]
        if len(dbs) != 1 or not isinstance(manifest, dict):
            return None
        path = Path(scratch) / "ark.sqlite3"
        path.write_bytes(archive.read(dbs[0]))
        con = sqlite3.connect(str(path))
        try:
            tables = [r[0] for r in con.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")]
            counts = {t: con.execute(f'SELECT COUNT(*) FROM "{t}"').fetchone()[0] for t in tables}
            chat = (con.execute("SELECT id, user, role, content FROM chat_messages ORDER BY id").fetchall()
                    if "chat_messages" in tables else None)
            meta = dict(con.execute("SELECT key, value FROM meta").fetchall()) if "meta" in tables else None
        finally:
            con.close()
    return names, manifest, counts, chat, meta


def _export_chat_scope(py, go) -> bool:
    """A project ZIP whose database differs only in the Assistant chat:
    Go's pre-v10 rows belong to the owner recorded in meta
    migration.chat_legacy_owner, and (a member's or agent's download; the
    manifest then also excludes "other users' Assistant chat") only one
    user's rows remain.  Every other table has the same row count, the
    entries are the same and the manifests differ only in that exclusion."""
    if py.status != 200 or go.status != 200:
        return False
    sa, sb = _zip_db_state(py.body), _zip_db_state(go.body)
    if sa is None or sb is None:
        return False
    (na, fa, ca, chat_a, meta_a), (nb, fb, cb, chat_b, meta_b) = sa, sb
    if na != nb or chat_a is None or chat_b is None or meta_a is None or meta_b is None:
        return False
    if set(ca) != set(cb) or any(ca[t] != cb[t] for t in ca if t not in ("chat_messages", "meta")):
        return False
    ex_a, ex_b = fa.get("excluded"), fb.get("excluded")
    if not isinstance(ex_a, list) or not isinstance(ex_b, list) or set(fa) != set(fb):
        return False
    scrubbed = ex_b == ex_a + [ARCHIVE_CHAT_EXCLUDED]
    if not scrubbed and ex_b != ex_a:
        return False
    owner = meta_b.get(LEGACY_CHAT_OWNER)
    if LEGACY_CHAT_OWNER in meta_a or (owner is None and not scrubbed):
        return False
    extra, missing = set(meta_b) - set(meta_a), set(meta_a) - set(meta_b)
    if extra - {LEGACY_CHAT_OWNER} or any(not (scrubbed and k.startswith("chat_cleared:")) for k in missing):
        return False
    rows_a = {r[0]: r for r in chat_a}
    users_b = set()
    for rid, user, role, content in chat_b:
        ra = rows_a.get(rid)
        if ra is None or (ra[2], ra[3]) != (role, content):
            return False
        if ra[1] != user and not (ra[1] == "" and owner is not None and user == owner):
            return False
        users_b.add(user)
    if scrubbed:
        return len(users_b) <= 1 and "" not in users_b and len(chat_b) < len(chat_a)
    return len(chat_b) == len(chat_a) and any(r[1] == "" for r in chat_a)


CHAT_FIXES = [
    Fix("chat-own-thread", "GET", "/api/chat", CHAT_FILE,
        "T-0028: the Assistant history is the viewer's own rows only; Python also lists the pre-v10 "
        "rows (empty user) to every member",
        _chat_own_thread),
    Fix("chat-clear-own-thread", "DELETE", "/api/chat", CHAT_FILE,
        "T-0028: Clear history counts (and deletes) only the viewer's own rows, not the pre-v10 ones",
        _chat_clear_count),
    Fix("chat-clear-activity-count", "GET", "/api/activity", CHAT_FILE,
        "T-0028: the chat.clear activity detail counts the viewer's own rows only",
        _chat_clear_activity),
] + [
    Fix("export-chat-scope", "GET", path, EXPORT_CHAT_FILE,
        "T-0028: a project ZIP gives the pre-v10 chat rows to the project owner and, unless a project "
        "admin downloads it in the browser, keeps only the requester's own Assistant chat",
        _export_chat_scope)
    for path in ("/api/export", "/api/agent/v1/export")
]

FIXES += CHAT_FIXES


# ------------------------------------- UX-18: Home starter-token rotation
# POST /api/personal/agent-tokens with {"purpose": "starter", "project": <id>}
# (the Home "Copy prompt" flow) returns the user's one starter token for that
# project: Go labels it "Home starter prompt (<id>) – <date>", keeps its value
# sealed and reuses it ("reused"), mints a new one only when there is none or
# for mode "new" (revoking the previous one), and lists revoked ids in a
# Go-only "rotated" member.  Python ignores both request members and creates
# a plain token with the client's label.  Creates without "purpose" (every
# recorded replay step) are unchanged.

STARTER_TOKEN_FILE = ("ark_studio/server/internal/org/starter_tokens.go (starterRequest, starterToken; "
                      "test starter_token_test.go)")
STARTER_LABEL = "Home starter prompt ("


def _starter_rotation(py, go) -> bool:
    """Both minted a token; Go's reply adds "rotated" (a list of token ids),
    its item carries the starter label, and the rest has the same structure
    (the token lists may differ by the tokens Go revoked)."""
    if py.status != 200 or go.status != 200:
        return False
    a, b = _json(py.body), _json(go.body)
    if not isinstance(a, dict) or not isinstance(b, dict) or "rotated" in a:
        return False
    rotated = b.get("rotated")
    if not isinstance(rotated, list) or not all(isinstance(x, str) and x.startswith("tok_") for x in rotated):
        return False
    ia, ib = a.get("item"), b.get("item")
    if not isinstance(ia, dict) or not isinstance(ib, dict):
        return False
    if not str(ib.get("label", "")).startswith(STARTER_LABEL) or str(ia.get("label", "")).startswith(STARTER_LABEL):
        return False
    if not (isinstance(a.get("token"), str) and isinstance(b.get("token"), str)):
        return False
    rest_a = {k: v for k, v in a.items() if k not in ("tokens", "limit")}
    if "reused" in b and not isinstance(b["reused"], bool):
        return False
    rest_b = {k: v for k, v in b.items() if k not in ("tokens", "rotated", "reused")}
    return _shape_equal(rest_a, rest_b) and isinstance(b.get("tokens"), list)


FIXES.append(Fix("agent-token-starter-rotation", "POST", "/api/personal/agent-tokens", STARTER_TOKEN_FILE,
                 "UX-18: a Home starter-prompt token (purpose: starter, project) is one per user and project, "
                 "stored sealed and reused by later copies; Python ignores the members and mints a plain token "
                 "(and Go sends no \"limit\")",
                 _starter_rotation))


# ------------------------------------- T-0034: project picker (personal ranking)
# GET /api/projects gives every listed project the viewer's own recent
# activity, "my_activity" (sum of 0.5^(age/7 days) over their activity
# entries of the last 30 days) and "my_last_active_at" (newest own entry or
# null), from a bounded, cached per-project scan
# (ark_studio/server/internal/server/project_activity.go; test
# project_activity_test.go).  The order and every other member are unchanged.
# The picker's pins and hidden projects are kept on the profile as
# prefs.projects = {"pinned": [id...], "hidden": [id...]}
# (ark_studio/server/internal/settings/projectprefs.go); Python refuses the
# unknown preference.

PROJECT_ACTIVITY_MEMBERS = ("my_activity", "my_last_active_at")
# Who created each project and who changed it last
# (ark_studio/server/internal/server/project_people.go; test project_people_test.go).
PROJECT_PEOPLE_MEMBERS = ("creator", "created_by_me", "last_actor")
# New Needs you cards for the project picker badges
# (ark_studio/server/internal/server/project_reviews.go; test project_reviews_test.go).
PROJECT_REVIEW_MEMBERS = ("review_new", "review_total")


def _strip_my_activity(body: bytes) -> bytes:
    """Remove the Go-only per-project activity, people and review badge members of /api/projects."""
    try:
        payload = json.loads(body)
    except ValueError:
        return body
    items = payload.get("projects") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        return body
    for item in items:
        if isinstance(item, dict):
            for key in PROJECT_ACTIVITY_MEMBERS + PROJECT_PEOPLE_MEMBERS + PROJECT_REVIEW_MEMBERS:
                item.pop(key, None)
    return json.dumps(payload, ensure_ascii=False).encode()


# ark_studio/server/internal/server/project_activity.go (withMyActivity; test project_activity_test.go)
AUGMENTED["/api/projects"] = _strip_my_activity


def _profile_project_prefs(py, go) -> bool:
    """PATCH /api/me with prefs.projects: Python 400 unknown preference,
    Go 200 (a stored value is echoed under profile.prefs.projects, a cleared
    one is absent)."""
    if py.status != 400 or go.status != 200 or b"unknown preference: projects" not in py.body:
        return False
    data = _json(go.body)
    return isinstance(data, dict) and isinstance(data.get("profile"), dict)


FIXES += [
    Fix("profile-project-prefs", "PATCH", "/api/me",
        "ark_studio/server/internal/settings/projectprefs.go (ProjectPrefs; tests projectprefs_test.go, "
        "internal/server/project_activity_test.go)",
        "T-0034: the profile keeps the project picker's pinned and hidden projects (prefs.projects); "
        "Python refuses the unknown preference",
        _profile_project_prefs),
]


# ------------------------------- T-0036: graph display settings by graph type
# Mind/marker/Local-graph display preferences moved from browser storage to
# Settings › Appearance › Graphs: a project default (settings.graph_display,
# PATCH /api/project/settings, admins) and a sparse personal override
# (profile prefs.appearance.graph_display, PATCH /api/me), both
# {tab: {setting: value}} (ark_studio/server/internal/settings/graphdisplay.go;
# tests graphdisplay_test.go, internal/server/graph_display_test.go).  Per-node
# Mind state (card widths, pinned images) follows the account per project as
# prefs.mind_layout = {project_id: {"widths", "pinned_images"}}.  The project
# member is present only once set, so projects without one answer like
# Python; Python refuses the new section, group and preference.

GRAPH_DISPLAY = "graph_display"
MIND_LAYOUT = "mind_layout"


def _strip_project_graph_display(body: bytes) -> bytes:
    """Remove settings.graph_display from GET/PATCH /api/project/settings."""
    payload = _json(body)
    settings = payload.get("settings") if isinstance(payload, dict) else None
    if not isinstance(settings, dict) or GRAPH_DISPLAY not in settings:
        return body
    settings.pop(GRAPH_DISPLAY)
    return json.dumps(payload, ensure_ascii=False).encode()


def _strip_profile_display(body: bytes) -> bytes:
    """Remove profile.prefs.mind_layout and prefs.appearance.graph_display
    (and an appearance left empty) from GET/PATCH /api/me."""
    payload = _json(body)
    profile = payload.get("profile") if isinstance(payload, dict) else None
    prefs = profile.get("prefs") if isinstance(profile, dict) else None
    if not isinstance(prefs, dict):
        return body
    appearance = prefs.get("appearance")
    changed = prefs.pop(MIND_LAYOUT, None) is not None
    if isinstance(appearance, dict) and GRAPH_DISPLAY in appearance:
        appearance.pop(GRAPH_DISPLAY)
        changed = True
        if not appearance:
            prefs.pop("appearance")
    return json.dumps(payload, ensure_ascii=False).encode() if changed else body


AUGMENTED["/api/project/settings"] = _strip_project_graph_display
AUGMENTED["/api/me"] = _strip_profile_display


def _go_profile_ok(go) -> bool:
    data = _json(go.body)
    return go.status == 200 and isinstance(data, dict) and isinstance(data.get("profile"), dict)


def _project_graph_display(py, go) -> bool:
    """PATCH /api/project/settings with graph_display: Python 400 unknown
    section, Go 200 with the settings."""
    if py.status != 400 or go.status != 200 or b"unknown settings section: graph_display" not in py.body:
        return False
    data = _json(go.body)
    return isinstance(data, dict) and isinstance(data.get("settings"), dict)


def _profile_graph_display(py, go) -> bool:
    """PATCH /api/me with prefs.appearance.graph_display: Python 400 unknown
    appearance group, Go 200."""
    return (py.status == 400 and b"unknown appearance group: graph_display" in py.body
            and _go_profile_ok(go))


def _profile_mind_layout(py, go) -> bool:
    """PATCH /api/me with prefs.mind_layout: Python 400 unknown preference, Go 200."""
    return py.status == 400 and b"unknown preference: mind_layout" in py.body and _go_profile_ok(go)


GRAPH_DISPLAY_FILE = ("ark_studio/server/internal/settings/graphdisplay.go (GraphDisplay, UpdateMindLayout; "
                      "tests graphdisplay_test.go, internal/server/graph_display_test.go)")

FIXES += [
    Fix("project-graph-display", "PATCH", "/api/project/settings", GRAPH_DISPLAY_FILE,
        "T-0036: project graph display defaults (settings.graph_display); Python refuses the unknown section",
        _project_graph_display),
    Fix("profile-graph-display", "PATCH", "/api/me", GRAPH_DISPLAY_FILE,
        "T-0036: personal graph display overrides (prefs.appearance.graph_display); Python refuses the "
        "unknown appearance group",
        _profile_graph_display),
    Fix("profile-mind-layout", "PATCH", "/api/me", GRAPH_DISPLAY_FILE,
        "T-0036: Mind card widths and pinned images per project (prefs.mind_layout); Python refuses the "
        "unknown preference",
        _profile_mind_layout),
]


# ------------------------------------------------------ node marker groups
# settings.markers = {"groups": [...]} (ark_studio/server/internal/store/markers,
# settings.go/writes.go; tests markers_test.go, internal/server/markers_test.go):
# present only once a project admin has set it, so projects without one
# answer like Python; Python refuses the section.  Unit writes apply the
# exclusive groups to marker:* tags (store/markers.go); the replay's units
# carry no marker tags, so their replies are unchanged.

MARKERS = "markers"


def _strip_project_markers(body: bytes) -> bytes:
    """Remove settings.markers from GET/PATCH /api/project/settings."""
    payload = _json(body)
    settings = payload.get("settings") if isinstance(payload, dict) else None
    if not isinstance(settings, dict) or MARKERS not in settings:
        return body
    settings.pop(MARKERS)
    return json.dumps(payload, ensure_ascii=False).encode()


AUGMENTED["/api/project/settings"] = _chain(_strip_project_graph_display, _strip_project_markers)


def _project_markers(py, go) -> bool:
    """PATCH /api/project/settings with markers: Python 400 unknown section, Go 200."""
    if py.status != 400 or go.status != 200 or b"unknown settings section: markers" not in py.body:
        return False
    data = _json(go.body)
    return isinstance(data, dict) and isinstance(data.get("settings"), dict)


FIXES += [
    Fix("project-markers", "PATCH", "/api/project/settings",
        "ark_studio/server/internal/store/markers/markers.go (Parse; tests markers_test.go)",
        "node marker groups (settings.markers); Python refuses the unknown section", _project_markers),
]


def fix_for(method: str, path: str, py, go):
    """The FIXES entry that explains this Python/Go reply pair, or None."""
    method = "GET" if method == "HEAD" else method
    path = path.split("?", 1)[0]
    for fix in FIXES:
        if fix.method == method and fix.path == path and fix.match(py, go):
            return fix
    return None


# ------------------------------------------------- T-0091: units without a parent
# Graph checks list tasks, results and decisions without a part_of parent
# (task-no-parent, result-no-parent) and accept an experiment part_of its task
# (core rule C4) for experiment-no-task.  ark_studio/server/internal/store/assistant.go,
# test store/assistant_parent_test.go.
PARENT_CHECKS_FILE = "ark_studio/server/internal/store/assistant.go (test store/assistant_parent_test.go)"
_PARENT_KINDS = {"task-no-parent", "result-no-parent", "experiment-no-task"}


def _insights_parent_checks(py, go) -> bool:
    if py.status != 200 or go.status != 200:
        return False
    try:
        a, b = json.loads(py.body), json.loads(go.body)
    except ValueError:
        return False
    ka = {i.get("id") for i in a.get("insights", []) if i.get("kind") not in _PARENT_KINDS}
    kb = {i.get("id") for i in b.get("insights", []) if i.get("kind") not in _PARENT_KINDS}
    new = any(i.get("kind") in ("task-no-parent", "result-no-parent") for i in b.get("insights", []))
    old_exp = any(i.get("kind") == "experiment-no-task" for i in a.get("insights", []))
    return (new or old_exp) and kb <= ka


FIXES += [
    Fix("insights-parent-checks", "GET", path, PARENT_CHECKS_FILE,
        "T-0091: Graph checks list units without a part_of parent; an experiment part_of its task has a task",
        _insights_parent_checks)
    for path in ("/api/assistant/insights", "/api/agent/v1/insights")
]
