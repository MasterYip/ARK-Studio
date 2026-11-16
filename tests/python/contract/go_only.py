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
  rest of the payload is still checked byte for byte.

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
}


def is_go_only(route_id: str) -> bool:
    return route_id.startswith(PREFIX)


def _strip_interface_docs(body: bytes) -> bytes:
    """Remove the Go-only docs.* capabilities and the "docs" member."""
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
                                   if not (isinstance(c, dict) and str(c.get("id", "")).startswith("docs."))]
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
    return AUGMENTED.get(path.split("?", 1)[0])


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

FIXES = [
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


def fix_for(method: str, path: str, py, go):
    """The FIXES entry that explains this Python/Go reply pair, or None."""
    method = "GET" if method == "HEAD" else method
    path = path.split("?", 1)[0]
    for fix in FIXES:
        if fix.method == method and fix.path == path and fix.match(py, go):
            return fix
    return None
