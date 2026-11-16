"""Route probe: one request per Python dispatch branch, so no branch can
silently answer 501 NOT_PORTED on the Go server.

The contract (gen_contract.py) records every branch of Handler whose test is
a condition on the request *path* (``path == ...``, ``startswith``,
``re.match``).  Dispatchers also branch on values derived from the path
(``action``, ``rest``, ``parts``, ``skill_id``) or on a query value that
selects a sub-endpoint (``fmt``, ``scope``); those inner branches are not
contract records, which is how POST /api/skills/<skill>/<action> stayed 501
while the route table said "ported".

This module closes that gap:

* ``scan_branches()`` walks arkstudio/server.py and lists every
  (function, variable) pair compared with string constants.  Each pair must
  be classified in DERIVED (a path-derived dispatch key: probed) or in
  NOT_DISPATCH (a query flag, header, body value or computed value: not a
  sub-endpoint).  ``unclassified()`` returns new pairs; test_contract_inventory
  fails on them, so a new dispatcher cannot slip past the probe.
* ``probe_steps()`` builds the probe set: every contract record (all roles:
  endpoint and dispatch) with each of its methods, every DERIVED key on the
  paths that reach it, and near-miss paths under each dispatch prefix
  (unknown sub-paths, which must fall through to Python's 404, not 501).
* diffreplay.py runs the probe steps last in every mode, compared byte for
  byte like any module and never skipped; independently, any Go response
  carrying ``NOT_PORTED`` is a mismatch in every module.
* ``python -B replay_probe.py --go go/ark-server.exe`` runs the probe against
  the Go server alone (quick check, no Python reference) and fails on any
  NOT_PORTED reply; ``--write`` records the result in probe.json, which
  route_table.py reads to count dispatcher sub-routes.
"""
from __future__ import annotations

import ast
import json
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
SERVER = REPO / "arkstudio" / "server.py"
CONTRACT = REPO / "docs" / "development" / "go-port" / "contract.json"
PROBE_JSON = REPO / "docs" / "development" / "go-port" / "probe.json"

MODULE = "route-probe"
ROUTE_PREFIX = "probe:"  # diffreplay never skips these steps
PROBE_TIMEOUT = 8.0
PATH_NAMES = {"path", "raw_path", "clean_path", "route"}

# Path-derived dispatch keys -> path templates that reach them ({k} = key,
# {u} = a live unit uid).  Methods per template.
DERIVED: dict[tuple[str, str], list[tuple[str, str]]] = {
    ("_node_history_get", "action"): [("GET", "/api/units/{u}/history/{k}"),
                                      ("GET", "/api/agent/v1/units/{u}/history/{k}")],
    ("_node_history_write", "action"): [("POST", "/api/units/{u}/history/{k}"),
                                        ("POST", "/api/agent/v1/units/{u}/history/{k}")],
    ("_session_contexts_get", "action"): [("GET", "/api/session-contexts/s_probe0000/{k}")],
    ("_setup_action", "action"): [("POST", "/api/skills/setup/{k}"), ("POST", "/api/agent/v1/setup/{k}")],
    ("_skills_get", "parts"): [("GET", "/api/skills/{k}"), ("GET", "/api/skills/setup/{k}")],
    ("_skills_get", "parts[1]"): [("GET", "/api/skills/agent-team/{k}"), ("GET", "/api/skills/x/{k}")],
    ("_skills_get", "rest"): [("GET", "/api/skills/{k}")],
    ("_skills_post", "action"): [("POST", "/api/skills/agent-team/{k}"), ("POST", "/api/skills/rrsi/{k}"),
                                 ("POST", "/api/skills/design/{k}"), ("POST", "/api/skills/x/{k}")],
    ("_skills_post", "skill_id"): [("POST", "/api/skills/{k}/validate")],
    ("_static", "rel"): [("GET", "/{k}")],
    ("_api_get", "fmt"): [("GET", "/api/export?format={k}")],
    ("_api_get", "scope"): [("GET", "/api/personal/settings?scope={k}")],
    ("do_POST", "scope"): [("POST", "/api/personal/settings?scope={k}")],
}

# Compared with string constants, but not a sub-endpoint selector.
NOT_DISPATCH: set[tuple[str, str]] = {
    ("_api_get", "(query.get('bodies') or ['1'])[0]"),
    ("_api_get", "(query.get('context') or ['0'])[0]"),
    ("_api_get", "(query.get('download') or [''])[0]"),
    ("_api_get", "(query.get('full') or ['0'])[0]"),
    ("_api_get", "(query.get('sessions') or [''])[0]"),
    ("_api_get", "query.get('facets', ['1'])[0]"),
    ("_api_get", "self.app.project_permission()"),
    ("_device_permission", "wanted"),
    ("_external_path", "prefix"),
    ("_file_headers", "ctype"),
    ("_guard_session_access", "level"),
    ("_handle_auth_post", "str(body.get('remember') or '').lower()"),
    ("_handle_local_open", "root"),
    ("_mobile_login", "ctype"),
    ("_mobile_projects", "source"),
    ("_read_login_form", "ctype"),
    ("_request_is_secure", "self._request_scheme()"),
    ("_request_scheme", "str(self.headers.get('X-Forwarded-Proto'"),
    ("_request_scheme", "str(self.headers.get('X-Forwarded-Ssl') "),
    ("_same_origin", "site"),
    ("_same_origin", "str(origin).strip().lower()"),
    ("_send_file", "headers.get('Cache-Control', 'no-store')"),
    ("_send_markdown_bundle", "(query.get('check') or [''])[0]"),
    ("_send_not_modified", "key"),
    ("_session_viewer", "self.app.project_permission()"),
    ("_setup_action", "layer"),
    ("_skills_get", "(query.get('docs') or ['1'])[0]"),
    ("_skills_post", "key"),
    ("_skills_post", "kind"),
    ("_stream_search", "key"),
    ("_stream_terminal", "key"),
    ("_workspace_get", "(query.get('download') or [''])[0]"),
    ("_worktrees_payload", "item.get('status')"),
    ("do_POST", "str(body.get('access_mode') or '')"),
    ("do_POST", "str(session.meta.get('access_mode') or '"),
}


def _module():
    return ast.parse(SERVER.read_text(encoding="utf-8"))


def _class_sets(cls: ast.ClassDef) -> dict[str, list[str]]:
    out = {}
    for node in cls.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name) \
                and isinstance(node.value, (ast.Set, ast.Tuple, ast.List)):
            vals = [e.value for e in node.value.elts if isinstance(e, ast.Constant) and isinstance(e.value, str)]
            if vals:
                out[node.targets[0].id] = vals
    return out


def scan_branches() -> dict[tuple[str, str], list[str]]:
    """(function, variable) -> string constants it is compared with, in Handler."""
    tree = _module()
    cls = next(n for n in tree.body if isinstance(n, ast.ClassDef) and n.name == "Handler")
    sets = _class_sets(cls)
    out: dict[tuple[str, str], set[str]] = {}
    for fn in cls.body:
        if not isinstance(fn, ast.FunctionDef):
            continue
        for node in ast.walk(fn):
            if not (isinstance(node, ast.Compare) and len(node.ops) == 1):
                continue
            left, right = node.left, node.comparators[0]
            if isinstance(left, ast.Name):
                name = left.id
            elif isinstance(left, ast.Subscript):
                name = ast.unparse(left)
            elif isinstance(left, ast.Call):
                name = ast.unparse(left)[:40]
            else:
                continue
            if name in PATH_NAMES or name in {"method", "verb", "self.command"}:
                continue
            vals: list[str] = []
            if isinstance(right, ast.Constant) and isinstance(right.value, str):
                vals = [right.value]
            elif isinstance(right, (ast.Set, ast.Tuple, ast.List)):
                vals = [e.value for e in right.elts if isinstance(e, ast.Constant) and isinstance(e.value, str)]
            elif isinstance(right, ast.Attribute) and isinstance(node.ops[0], ast.In) \
                    and isinstance(right.value, ast.Name) and right.value.id == "self":
                vals = sets.get(right.attr, ["<" + ast.unparse(right) + ">"])
            if vals:
                out.setdefault((fn.name, name), set()).update(vals)
    return {k: sorted(v) for k, v in sorted(out.items())}


def unclassified() -> list[tuple[str, str]]:
    return [k for k in scan_branches() if k not in DERIVED and k not in NOT_DISPATCH]


def _contract() -> list[dict]:
    return json.loads(CONTRACT.read_text(encoding="utf-8"))["routes"]


METHOD_ORDER = {"GET": 0, "HEAD": 1, "OPTIONS": 2, "POST": 3, "PUT": 4, "PATCH": 5, "DELETE": 6}
# Last, in this order: they sign the client out or remove the project.
DESTRUCTIVE = [("DELETE", "/api/mobile/session"), ("DELETE", "/api/project"),
               ("POST", "/api/auth/logout"), ("POST", "/logout"), ("GET", "/logout"), ("HEAD", "/logout")]
# Event streams stay open (compared as frame sequences by N20 in main-sync).
STREAMS = {("GET", "/api/events"), ("HEAD", "/api/events")}
# Probed elsewhere with real bodies; an empty body here would start agent
# processes whose timing-dependent replies are not about routing.
BODY = {("POST", "/api/agent-sessions"): {"preset": "probe-unknown"},
        ("POST", "/api/chat/stream"): {"message": ""},
        ("POST", "/api/chat"): {"message": ""}}


def probe_list(unit: str = "x") -> list[tuple[str, str, str]]:
    """[(contract id or "derived:fn:var" / "nearmiss:prefix", method, path)], ordered."""
    seen: dict[tuple[str, str], int] = {}
    out: list[tuple[str, str, str]] = []

    def add(rid, method, path):  # one request per URL; "a ; b" when several records share it
        if (method, path) not in seen:
            seen[(method, path)] = len(out)
            out.append((rid, method, path))
        else:
            i = seen[(method, path)]
            if rid not in out[i][0].split(" ; "):
                out[i] = (out[i][0] + " ; " + rid, method, path)

    routes = _contract()
    for r in routes:
        for m in r["methods"]:
            add(r["id"], m, r["sample"])
    branches = scan_branches()
    for key, templates in DERIVED.items():
        for k in branches.get(key, []):
            if k.startswith("<"):
                continue
            for m, t in templates:
                add(f"derived:{key[0]}:{key[1]}", m, t.replace("{k}", k).replace("{u}", unit))
    for r in routes:
        if r["role"] != "dispatch":
            continue
        base = r["pattern"] if r["pattern"].endswith("/") else r["pattern"] + "/"
        for m in r["methods"]:
            for tail in ("zz-unknown", "x/zz-unknown", "x/y/zz-unknown"):
                add("nearmiss:" + r["pattern"], m, base + tail)
    for m, p in (("PUT", "/api/units"), ("OPTIONS", "/api/local/open"), ("OPTIONS", "/api/units"),
                 ("GET", "/api/zz-unknown"), ("POST", "/api/zz-unknown"), ("PATCH", "/api/zz-unknown"),
                 ("DELETE", "/api/zz-unknown")):
        add("extra", m, p)
    last = {d: i for i, d in enumerate(DESTRUCTIVE)}
    out.sort(key=lambda s: (last.get((s[1], s[2]), -1), METHOD_ORDER.get(s[1], 9)))
    return out


def probe_steps(fixture: dict, unit: str = "x", streams: bool = False) -> list[tuple]:
    h = {"X-Replay-Module": MODULE}
    users = fixture.get("users") or []
    if users:
        h["X-ARK-Project"] = fixture["project"]
    steps = []
    tokens = fixture.get("tokens") or {}
    for rid, method, path in probe_list(unit):
        if (method, path) in STREAMS and not streams:
            continue
        body = BODY.get((method, path), {} if method in {"POST", "PATCH", "PUT"} else None)
        steps.append((ROUTE_PREFIX + rid, method, path, body, dict(h)))
        # The agent and task-agent dispatchers sit behind their tokens: probe
        # them once more with the server agent token / a task read token.
        token = tokens.get("server") if path.startswith("/api/agent/v1/") else             tokens.get("read_token") if path.startswith("/api/task-agent/") else None
        if token:
            steps.append((ROUTE_PREFIX + rid, method, path, body,
                          dict(h, Authorization="Bearer " + token, **{"X-Replay-NoCookie": "1"})))
    return steps


def is_not_ported(status: int, body: bytes) -> bool:
    return status == 501 and b"NOT_PORTED" in body


def go_only(go: str, write: bool = False) -> int:
    """Probe the Go server alone (signed out on a local root, then signed in
    as the org owner); fail on any NOT_PORTED reply.  --write records the
    statuses in probe.json (read by route_table.py)."""
    sys.path.insert(0, str(HERE))
    from serverctl import Client, Server, copy_fixture, make_fixture, temp_dir  # noqa: E402
    import shutil
    statuses: dict[str, list] = {}
    bad = []
    for auth in (0, 3):
        work = temp_dir("ark-probe-")
        try:
            fixture = make_fixture(work / "fixture", auth=auth)
            data = copy_fixture(Path(fixture["data_dir"]), work / "go")
            with Server(go, data, env={"ARK_ALLOWED_HOSTS": "contract.example", "ARK_RG": "none"}) as gs:
                c = Client(gs)
                if auth:
                    c.login(fixture["owner"], fixture["password"])
                for rid, method, path, body, headers in probe_steps(fixture, streams=True):
                    headers = {k: v for k, v in headers.items() if k != "X-Replay-Module"}
                    key = f"{method} {path}" + (" [token]" if "Authorization" in headers else "")
                    try:
                        if method == "POST":
                            resp = c.post(path, body, headers=headers, timeout=PROBE_TIMEOUT)
                        else:
                            resp = c.request(method, path, body=body, headers=headers, timeout=PROBE_TIMEOUT)
                        status = resp.status
                    except OSError:
                        status = "open"
                    if status == 501 and is_not_ported(resp.status, resp.body):
                        status = "NOT_PORTED"
                        bad.append(f"{'owner' if auth else 'local'}: {key}")
                    statuses.setdefault(key, [rid[len(ROUTE_PREFIX):]]).append(status)
        finally:
            shutil.rmtree(work, ignore_errors=True)
    print(f"probe: {len(statuses)} requests x 2 modes (local, owner), {len(bad)} NOT_PORTED")
    for line in bad[:50]:
        print("  NOT_PORTED " + line)
    missing = unclassified()
    for key in missing:
        print(f"  unclassified dispatch variable: {key}")
    if write:
        PROBE_JSON.write_text(json.dumps({"probes": {k: v for k, v in sorted(statuses.items())}},
                                         indent=0, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n")
    return 1 if bad or missing else 0


def recorded() -> dict:
    """probe.json: "METHOD path" -> [probe id, local status, owner status]."""
    if not PROBE_JSON.is_file():
        return {}
    return json.loads(PROBE_JSON.read_text(encoding="utf-8"))["probes"]


def stale_probes() -> list[str]:
    """Probe requests missing from probe.json (Python gained a branch: re-record)."""
    rec = recorded()
    return [f"{m} {p}" for rid, m, p in probe_list() if f"{m} {p}" not in rec]
    # (token variants are extra rows: "METHOD path [token]")


def main(argv: list[str]) -> int:
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", default="")
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--scan", action="store_true", help="print the dispatch-variable scan")
    args = parser.parse_args(argv)
    if args.scan or not args.go:
        for key, vals in scan_branches().items():
            tag = "DERIVED" if key in DERIVED else "plain" if key in NOT_DISPATCH else "UNCLASSIFIED"
            print(f"{tag:12} {key} {vals[:12]}")
        print(f"{len(probe_list())} probe requests")
        return 1 if unclassified() else 0
    return go_only(str(Path(args.go).resolve()), write=args.write)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
