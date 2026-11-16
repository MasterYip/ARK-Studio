"""Intentional Go-only divergences from the frozen Python reference server.

Since the 2026-11-15 switch-over, backend features land in ark-server (go/)
only; arkstudio/server.py is frozen.  The contract (gen_contract.py) is
still generated from Python, so a new Go route is not a contract record and
Python cannot answer it.  This module is the allow-list the parity tools
consult:

* ``ROUTES``: Go-only route ids.  ``ark-server routes`` lists them (from
  ``GoOnlyRoutes`` in go/internal/server/routes_agentdocs.go) with the
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
    # go/internal/server/routes_agentdocs.go, go/internal/agentdocs
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


AUGMENTED = {
    "/api/agent/v1/interface": _strip_interface_docs,  # routes_agentdocs.go agentInterfaceEncoded
}


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


FIXES = [
    Fix("resources-graph-nonfinite-gpu-memory", "GET", "/api/resources/graph",
        "go/internal/resources/graph.go (roundMB, finiteMB; test round_test.go)",
        "a GPU probe reporting an infinite/NaN memory figure: Python's round() raises and the "
        "whole graph answers 500; Go reports those figures as null",
        _resources_nonfinite),
]


def fix_for(method: str, path: str, py, go):
    """The FIXES entry that explains this Python/Go reply pair, or None."""
    method = "GET" if method == "HEAD" else method
    path = path.split("?", 1)[0]
    for fix in FIXES:
        if fix.method == method and fix.path == path and fix.match(py, go):
            return fix
    return None
