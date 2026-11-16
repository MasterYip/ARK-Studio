"""Generate the Go-port contract inventory from the Python sources.

    python -B tests/python/contract/gen_contract.py          # rewrite docs/development/go-port/contract.{md,json}
    python -B tests/python/contract/gen_contract.py --check  # exit 1 when the files are stale

Everything here is derived from the code (``ast`` over arkstudio/server.py,
arkstudio/__main__.py and arkstudio/server_config.py, plus the live constants
of arkstudio.server), never from memory.  The handful of routes the AST walk
cannot see (string-splitting dispatch in ``_skills_get``, the APK loop and the
static fallback) are listed in ``MANUAL_ROUTES`` with the source line they
come from.  Stdlib only.
"""
from __future__ import annotations

import ast
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
SERVER = ROOT / "arkstudio" / "server.py"
MAIN = ROOT / "arkstudio" / "__main__.py"
CONFIG = ROOT / "arkstudio" / "server_config.py"
OUT_MD = ROOT / "docs" / "development" / "go-port" / "contract.md"
OUT_JSON = ROOT / "docs" / "development" / "go-port" / "contract.json"
OUT_GO = ROOT / "go" / "internal" / "server" / "contract_gen.go"

# Entry points per HTTP verb.  Helper methods inherit the verbs of every entry
# point that (transitively) calls them.
ENTRY = {"do_GET": ["GET", "HEAD"], "do_POST": ["POST"], "do_PATCH": ["PATCH"],
         "do_DELETE": ["DELETE"], "do_OPTIONS": ["OPTIONS"]}
PATH_NAMES = {"path"}
GUARD_CALLS = {
    "_require_org_admin": "org-admin",
    "_require_agent": "agent-token (X-ARK-Token / Bearer, or personal arka_ token)",
    "_require_task_agent": "task-agent read token",
    "_personal_agent_tokens_guard": "own account (personal tokens)",
    "_same_origin": "same-origin",
    "_reject_task_read_token": "no task read token",
    "_reject_task_agent_read_token": "no task read token",
    "_reject_workspace_read_token": "no task read token",
    "_guard_session_access": "agent-session access",
    "require_admin": "project:admin",
    "_require_owner": "org-owner",
}
# Functions whose path tests are gates, not endpoints (documented in the pipeline section).
PIPELINE_FUNCS = {"_guard_public_auth", "_guard_mobile", "_guard_project_scope",
                  "_personal_agent_path_allowed", "_bind_personal_agent_token"}
MANUAL_ROUTES = [
    (["POST"], "exact", "/login", "_handle_auth_post",
     "`if path != '/login': return False`; form or JSON body username/password/csrf/next/remember"),
    # (methods, kind, pattern, function, note)
    (["GET", "HEAD"], "exact", "/api/skills/interface", "_skills_get",
     "rest == 'interface' (string split dispatch)"),
    (["GET", "HEAD"], "exact", "/api/skills/setup/state", "_skills_get",
     "parts == ['setup', 'state']"),
    (["GET", "HEAD"], "regex", r"^/api/skills/([^/]+)$", "_skills_get",
     "len(parts) == 1; ?docs=0 omits documents"),
    (["GET", "HEAD"], "regex", r"^/api/skills/([^/]+)/(prompt|agent)$", "_skills_get",
     "len(parts) == 2"),
    (["GET", "HEAD"], "exact", "/download/ARK-Studio-android.apk", "do_GET", "ANDROID_APKS loop"),
    (["GET", "HEAD"], "exact", "/download/ark.apk", "do_GET", "ANDROID_APKS loop"),
    (["GET", "HEAD"], "exact", "/download/ARK-Studio-webapp.apk", "do_GET", "ANDROID_APKS loop"),
    (["GET", "HEAD"], "static", "/*", "_static",
     "web/ file, SPA fallback to index.html for extension-less paths; login.html is 404"),
]


def _const(node):
    return node.value if isinstance(node, ast.Constant) else None


def _dotted(node) -> str:
    parts = []
    while isinstance(node, ast.Attribute):
        parts.append(node.attr)
        node = node.value
    if isinstance(node, ast.Name):
        parts.append(node.id)
    elif isinstance(node, ast.Call):
        parts.append(_dotted(node.func) + "()")
    return ".".join(reversed(parts))


class Module:
    def __init__(self, path: Path):
        self.source = path.read_text(encoding="utf-8")
        self.tree = ast.parse(self.source)
        self.handler = next(n for n in self.tree.body
                            if isinstance(n, ast.ClassDef) and n.name == "Handler")
        self.methods = {n.name: n for n in self.handler.body if isinstance(n, ast.FunctionDef)}
        self.class_regex = {}
        for node in self.handler.body:
            if isinstance(node, ast.Assign) and isinstance(node.value, ast.Call) \
                    and _dotted(node.value.func) == "re.compile":
                self.class_regex[node.targets[0].id] = _const(node.value.args[0])
        self.module_regex = {}
        for node in self.tree.body:
            if isinstance(node, ast.Assign) and isinstance(node.value, ast.Call) \
                    and _dotted(node.value.func) == "re.compile":
                self.module_regex[node.targets[0].id] = _const(node.value.args[0])

    def verbs(self) -> dict[str, set[str]]:
        """Method name -> HTTP verbs that can reach it."""
        out: dict[str, set[str]] = {}
        for entry, verbs in ENTRY.items():
            stack = [entry]
            seen = set()
            while stack:
                name = stack.pop()
                if name in seen or name not in self.methods:
                    continue
                seen.add(name)
                out.setdefault(name, set()).update(verbs)
                for node in ast.walk(self.methods[name]):
                    if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                            and isinstance(node.func.value, ast.Name) and node.func.value.id == "self":
                        stack.append(node.func.attr)
        return out


# ----------------------------------------------------------------- routes
class RouteWalker:
    def __init__(self, module: Module):
        self.m = module
        self.verbs = module.verbs()
        self.routes: list[dict] = []

    # A path test is a condition on the request path; anything else is logic.
    def path_conds(self, test, regex_vars):
        out, methods = [], None
        if isinstance(test, ast.BoolOp):
            for value in test.values:
                sub, sub_methods = self.path_conds(value, regex_vars)
                out.extend(sub)
                if sub_methods is not None:
                    methods = sub_methods if methods is None else methods | sub_methods
            if isinstance(test.op, ast.And) and not out:
                return [], None
            return out, methods
        if isinstance(test, ast.Compare) and len(test.ops) == 1:
            left, op, right = test.left, test.ops[0], test.comparators[0]
            is_path = isinstance(left, ast.Name) and left.id in PATH_NAMES
            is_method = (isinstance(left, ast.Name) and left.id in {"method", "verb"}) or \
                _dotted(left) in {"self.command", "verb"}
            if is_path and isinstance(op, ast.Eq) and isinstance(_const(right), str):
                return [("exact", right.value)], None
            if is_path and isinstance(op, ast.In) and isinstance(right, (ast.Set, ast.Tuple, ast.List)):
                return [("exact", _const(e)) for e in right.elts if isinstance(_const(e), str)], None
            if is_method:
                if isinstance(op, ast.Eq) and isinstance(_const(right), str):
                    return [], {right.value}
                if isinstance(op, ast.In) and isinstance(right, (ast.Set, ast.Tuple, ast.List)):
                    return [], {_const(e) for e in right.elts}
            return [], None
        if isinstance(test, ast.Call):
            func = test.func
            if isinstance(func, ast.Attribute) and func.attr == "startswith" \
                    and isinstance(func.value, ast.Name) and func.value.id in PATH_NAMES:
                arg = test.args[0]
                values = arg.elts if isinstance(arg, ast.Tuple) else [arg]
                return [("prefix", _const(v)) for v in values if isinstance(_const(v), str)], None
            found = self.regex_call(test)
            if found:
                return [found], None
        if isinstance(test, ast.Name) and test.id in regex_vars:
            return [regex_vars[test.id]], None
        return [], None

    def regex_call(self, call):
        if not isinstance(call, ast.Call):
            return None
        name = _dotted(call.func)
        if name in {"re.match", "re.fullmatch"} and len(call.args) >= 2 \
                and isinstance(call.args[1], ast.Name) and call.args[1].id in PATH_NAMES:
            pattern = _const(call.args[0])
            if isinstance(pattern, str):
                return ("fullmatch" if name == "re.fullmatch" else "regex", pattern)
        if isinstance(call.func, ast.Attribute) and call.func.attr in {"match", "fullmatch"} \
                and call.args and isinstance(call.args[0], ast.Name) and call.args[0].id in PATH_NAMES:
            owner = call.func.value
            if isinstance(owner, ast.Attribute) and owner.attr in self.m.class_regex:
                return ("regex", self.m.class_regex[owner.attr])
            if isinstance(owner, ast.Name) and owner.id in self.m.module_regex:
                return ("regex", self.m.module_regex[owner.id])
        return None

    def walk_function(self, name: str):
        fn = self.m.methods[name]
        self.walk(fn.body, name, [], {}, None)

    def walk(self, stmts, fn, flags, regex_vars, parent):
        flags = list(flags)
        for stmt in stmts:
            if isinstance(stmt, ast.Assign) and len(stmt.targets) == 1 \
                    and isinstance(stmt.targets[0], ast.Name):
                found = self.regex_call(stmt.value)
                if found:
                    regex_vars = dict(regex_vars, **{stmt.targets[0].id: found})
                    continue
            if isinstance(stmt, ast.If):
                conds, methods = self.path_conds(stmt.test, regex_vars)
                if conds:
                    shape = self.shape(stmt.body, regex_vars)
                    for kind, pattern in conds:
                        self.add(fn, kind, pattern, methods, stmt.lineno, flags, shape, parent)
                    first = f"{conds[0][0]}:{conds[0][1]}"
                    self.walk(stmt.body, fn, flags, regex_vars, first)
                    self.walk(stmt.orelse, fn, flags, regex_vars, parent)
                    continue
                self.walk(stmt.body, fn, flags, regex_vars, parent)
                self.walk(stmt.orelse, fn, flags, regex_vars, parent)
                flags += self.guards(stmt.test)
                continue
            if isinstance(stmt, (ast.Try, ast.With)):
                self.walk(stmt.body, fn, flags, regex_vars, parent)
                continue
            flags += self.guards(stmt)
            if any(isinstance(n, ast.Call) and _dotted(n.func) == "self._read_json"
                   for n in ast.walk(stmt)):
                flags.append("json-body")
        return flags

    def guards(self, node) -> list[str]:
        out = []
        for sub in ast.walk(node):
            if isinstance(sub, ast.Call) and isinstance(sub.func, ast.Attribute):
                if sub.func.attr in GUARD_CALLS:
                    out.append(GUARD_CALLS[sub.func.attr])
                if _dotted(sub.func) in {"self.app.require", "self.app.check_project_access",
                                         "self._node_history_store"}:
                    level = next((_const(a) for a in sub.args if isinstance(_const(a), str)), None)
                    if level:
                        out.append(f"project:{level}")
        return out

    def shape(self, body, regex_vars) -> dict:
        query, fields, responses, errors, notes = [], [], [], [], []
        guards = []

        def nested_route(node):
            return isinstance(node, ast.If) and self.path_conds(node.test, regex_vars)[0]

        def visit(node):
            if nested_route(node):
                return
            for child in ast.iter_child_nodes(node):
                visit(child)
            if not isinstance(node, ast.Call):
                return
            name = _dotted(node.func)
            args = node.args
            if name == "query.get" and args and isinstance(_const(args[0]), str):
                query.append(args[0].value)
            elif name == "_qlist" and len(args) >= 2:
                query.extend(_const(a) for a in args[1:] if isinstance(_const(a), str))
            elif name in {"filters_from_query", "_gpu_list_query", "_gpu_tail_query"}:
                query.append({"filters_from_query": "<unit filters>",
                              "_gpu_list_query": "<gpu list filters>",
                              "_gpu_tail_query": "tail"}[name])
            elif name == "_gpu_query_int" and len(args) >= 2 and isinstance(_const(args[1]), str):
                query.append(args[1].value)
            elif name == "body.get" and args and isinstance(_const(args[0]), str):
                fields.append(args[0].value)
            elif name == "self._ok":
                responses.append(self.describe_ok(args[0] if args else None))
            elif name == "self._send":
                ctype = _const(args[2]) if len(args) > 2 else None
                responses.append(f"raw {ctype}" if ctype else "raw body")
            elif name in {"self._send_file"}:
                responses.append("file stream (Range)")
            elif name in {"self._stream_zip", "self._send_project_archive", "self._send_markdown_bundle"}:
                responses.append("zip download")
            elif name == "self._stream_chat":
                responses.append("ndjson stream")
            elif name == "self._stream_terminal":
                responses.append("SSE stream")
            elif name in {"self._redirect", "self._secure_redirect"}:
                responses.append("redirect")
            elif name == "self._render_login":
                responses.append("login HTML")
            elif name == "self._send_auth_session":
                responses.append("{ok, auth_required, authenticated, user, expires_at, role, is_admin, local, org}")
            elif name == "ApiError" and len(args) >= 1:
                status = _const(args[0])
                code = _const(args[2]) if len(args) > 2 else None
                errors.append(f"{status}{' ' + code if code else ''}")
            elif name.startswith("self._") and name.split(".")[-1] in self.m.methods \
                    and name.split(".")[-1].endswith(("_get", "_post", "_write", "_action", "_state")):
                notes.append("delegates to " + name.split(".")[-1])
            if isinstance(node.func, ast.Attribute) and node.func.attr in GUARD_CALLS:
                guards.append(GUARD_CALLS[node.func.attr])
            if name in {"self.app.require", "self.app.check_project_access"}:
                level = next((_const(a) for a in args if isinstance(_const(a), str)), None)
                if level and level != "read":
                    guards.append(f"project:{level}")

        for stmt in body:
            visit(stmt)
        for node in body:
            for sub in ast.walk(node):
                if isinstance(sub, ast.Subscript) and isinstance(sub.value, ast.Name) \
                        and sub.value.id == "body" and isinstance(_const(sub.slice), str):
                    fields.append(sub.slice.value)
        uniq = lambda xs: list(dict.fromkeys(x for x in xs if x))
        return {"query": uniq(query), "body": uniq(fields), "response": uniq(responses),
                "errors": uniq(errors), "guards": uniq(guards), "notes": uniq(notes)}

    def describe_ok(self, node) -> str:
        if node is None:
            return "{ok}"
        if isinstance(node, ast.Dict):
            keys = []
            for key, value in zip(node.keys, node.values):
                keys.append(_const(key) if key is not None else "**" + _dotted(value.func if isinstance(value, ast.Call) else value))
            return "{ok, " + ", ".join(str(k) for k in keys) + "}"
        if isinstance(node, ast.Call):
            return "{ok, **" + _dotted(node.func) + "()}"
        return "{ok, **" + (_dotted(node) or type(node).__name__) + "}"

    def add(self, fn, kind, pattern, methods, line, flags, shape, parent):
        verbs = set(self.verbs.get(fn, set()))
        if methods:
            verbs &= {m.upper() for m in methods if isinstance(m, str)}
            if "GET" in verbs:
                verbs.add("HEAD")
        self.routes.append({
            "id": f"{kind}:{pattern}", "kind": kind, "pattern": pattern,
            "methods": sorted(verbs), "function": fn, "line": line,
            "flags": list(dict.fromkeys(flags)), "parent": parent, **shape,
        })


def sample_path(kind: str, pattern: str) -> str:
    if kind in {"exact", "static"}:
        return pattern if kind == "exact" else "/index.html"
    if kind == "prefix":
        return pattern if pattern.endswith("/") is False else pattern + "x"
    text = pattern.lstrip("^").rstrip("$")
    text = text.replace(r"(?:/([^/]+))?", "").replace(r"(/logs)?", "")
    text = text.replace(r"(\d+)", "1").replace("([^/]+)", "x").replace(r"([a-z][a-z-]*)", "x")
    text = text.replace("(prompt|agent)", "prompt")
    return text


def classify(routes):
    sys.path.insert(0, str(ROOT))
    from arkstudio import server as srv  # noqa: E402 - live constants, not copies
    for route in routes:
        path = sample_path(route["kind"], route["pattern"])
        route["sample"] = path
        api = path.startswith("/api/")
        if path in srv.PUBLIC_AUTH_PATHS or path in srv.LOGIN_ASSETS:
            gate = "public"
        elif path.startswith("/api/task-agent/"):
            gate = "task-agent read token"
        elif path.startswith("/api/agent/v1/"):
            gate = "session, agent token or personal token"
        else:
            gate = "session" + (" or paired device" if api else "")
        route["auth_gate"] = gate
        if not api or path.startswith("/api/task-agent/") or path in srv.GLOBAL_API_PATHS \
                or any(path.startswith(p) for p in srv.GLOBAL_API_PREFIXES):
            scope = "global"
        else:
            scope = "project read (GET/HEAD) / write (others)"
        route["project_scope"] = scope
        route["mobile_device"] = sorted(m for m in route["methods"]
                                        if api and srv.mobile_device_path_allowed(path, m))
    return routes


# -------------------------------------------------------------------- CLI
def cli_flags():
    source = MAIN.read_text(encoding="utf-8")
    tree = ast.parse(source)
    main = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "main")
    parsers = {"parser": "(top level)"}
    flags = []
    for node in ast.walk(main):
        if isinstance(node, ast.Assign) and isinstance(node.value, ast.Call) \
                and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_parser":
            owner = _dotted(node.value.func.value)
            name = _const(node.value.args[0])
            prefix = {"agent_sub": "agent ", "skills_sub": "skills "}.get(owner, "")
            parsers[node.targets[0].id] = prefix + name
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Call) \
                and isinstance(node.value.func, ast.Attribute) and node.value.func.attr == "add_parser":
            owner = _dotted(node.value.func.value)
            prefix = {"agent_sub": "agent ", "skills_sub": "skills "}.get(owner, "")
            parsers.setdefault("_bare_" + _const(node.value.args[0]), prefix + _const(node.value.args[0]))
    for node in ast.walk(main):
        if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                and node.func.attr == "add_argument" and isinstance(node.func.value, ast.Name):
            command = parsers.get(node.func.value.id, node.func.value.id)
            names = [_const(a) for a in node.args]
            kw = {k.arg: ast.get_source_segment(source, k.value) for k in node.keywords}
            flags.append({"command": command, "flags": names, "default": kw.get("default"),
                          "type": kw.get("type"), "action": kw.get("action"),
                          "choices": kw.get("choices"), "required": kw.get("required"),
                          "nargs": kw.get("nargs"), "line": node.lineno})
    commands = sorted(set(parsers.values()) - {"(top level)"})
    return commands, sorted(flags, key=lambda f: (f["command"], f["line"]))


# --------------------------------------------------------------- env vars
ENV_CALLS = {"os.environ.get", "os.getenv", "environ.get", "env.get", "_env_bool", "_env_int",
             "os.environ.pop", "os.environ.setdefault", "environ.setdefault", "self.environ.get"}


def env_vars():
    found: dict[str, set[str]] = {}
    files = sorted((ROOT / "arkstudio").rglob("*.py")) + sorted((ROOT / "vendor").rglob("*.py"))
    for path in files:
        rel = path.relative_to(ROOT).as_posix()
        try:
            tree = ast.parse(path.read_text(encoding="utf-8"))
        except (SyntaxError, UnicodeDecodeError):
            continue
        for node in ast.walk(tree):
            name = None
            if isinstance(node, ast.Call) and _dotted(node.func) in ENV_CALLS and node.args:
                name = _const(node.args[0])
            elif isinstance(node, ast.Subscript) and _dotted(node.value) in {"os.environ", "environ", "env"}:
                name = _const(node.slice)
            elif isinstance(node, ast.Compare) and len(node.comparators) == 1 \
                    and _dotted(node.comparators[0]) in {"os.environ"}:
                name = _const(node.left)
            if isinstance(name, str) and re.fullmatch(r"[A-Z][A-Z0-9_]{2,}", name):
                found.setdefault(name, set()).add(rel)
    sys.path.insert(0, str(ROOT))
    from arkstudio import server_config
    for key, name in server_config.ENV_PATHS.items():
        found.setdefault(name, set()).add("arkstudio/server_config.py (config key " + key + ")")
    return {k: sorted(v) for k, v in sorted(found.items())}


def config_keys():
    sys.path.insert(0, str(ROOT))
    from arkstudio import server_config as sc
    keys = []
    for key in sorted(sc.STRING_FIELDS | sc.BOOL_FIELDS | sc.INT_FIELDS | sc.ENV_PATHS.keys() | {"media_root"}):
        kind = ("path" if key in sc.PATH_FIELDS else "bool" if key in sc.BOOL_FIELDS
                else "int" if key in sc.INT_FIELDS else "path (env)" if key in sc.ENV_PATHS
                else "list[path]" if key == "media_root" else "string")
        keys.append({"key": key, "type": kind, "env": sc.ENV_PATHS.get(key)})
    return keys


# ------------------------------------------------------------- root files
FILE_RE = re.compile(r"""\b(self\.root|root|self\.path|path|folder|project\.path|self\.root\.parent|"""
                     r"""self\.app\.root|data|target)\s*/\s*["']([^"']+)["']""")


def root_files():
    found: dict[str, set[str]] = {}
    for path in sorted((ROOT / "arkstudio").glob("*.py")):
        rel = path.relative_to(ROOT).as_posix()
        for match in FILE_RE.finditer(path.read_text(encoding="utf-8")):
            found.setdefault(f"{match.group(1)} / {match.group(2)}", set()).add(rel)
    return {k: sorted(v) for k, v in sorted(found.items())}


# ----------------------------------------------------------------- render
def build() -> dict:
    module = Module(SERVER)
    walker = RouteWalker(module)
    reach = module.verbs()
    for name in module.methods:
        if name in reach and name not in PIPELINE_FUNCS:
            walker.walk_function(name)
    for methods, kind, pattern, fn, note in MANUAL_ROUTES:
        walker.routes.append({"id": f"{kind}:{pattern}", "kind": kind, "pattern": pattern,
                              "methods": methods, "function": fn, "line": None, "flags": [],
                              "parent": None, "query": [], "body": [], "response": [],
                              "errors": [], "guards": [], "notes": ["manual: " + note]})
    # One record per (id, function); merge verbs found in different branches.
    merged: dict[tuple, dict] = {}
    for route in walker.routes:
        key = (route["id"], route["function"])
        if key in merged:
            old = merged[key]
            old["methods"] = sorted(set(old["methods"]) | set(route["methods"]))
            for field in ("query", "body", "response", "errors", "guards", "notes", "flags"):
                old[field] = list(dict.fromkeys(old[field] + route[field]))
        else:
            merged[key] = route
    routes = [r for r in merged.values() if r["pattern"]]
    for route in routes:
        route["role"] = "dispatch" if route["kind"] == "prefix" and not route["response"] else "endpoint"
    routes = classify(routes)
    routes.sort(key=lambda r: (r["sample"], r["function"]))
    commands, flags = cli_flags()
    return {"generated_from": ["arkstudio/server.py", "arkstudio/__main__.py",
                               "arkstudio/server_config.py"],
            "routes": routes, "cli_commands": commands, "cli_flags": flags,
            "config_keys": config_keys(), "env_vars": env_vars(), "root_files": root_files()}


def esc(text) -> str:
    return str(text if text is not None else "").replace("|", "\\|").replace("\n", " ")


PIPELINE = """\
## Request pipeline (every route)

Order in `Handler.do_GET/do_POST/do_PATCH/do_DELETE` (server.py):

1. `_check_host`: the Host header must be in the allow-list (`127.0.0.1:<port>`, `localhost:<port>`,
   the bind host, the `--public-url` host, `ARK_ALLOWED_HOSTS`), or the bind must be non-loopback
   without a public URL. Otherwise `403 HOST_REJECTED`.
2. Auth GET/POST routes (`/login`, `/logout`, `/api/auth/session`, `/api/auth/logout`).
3. `_guard_public_auth`: a personal `arka_` agent token on allowed paths; else (auth enabled) the
   `ark_session` cookie, else a paired-device token. Public paths pass. A non-API GET without a
   session is `302` to `login?next=...`; an API call is `401 AUTH_REQUIRED`.
4. `_guard_mobile`: API calls from a non-local browser without a session need a mobile token
   (`401 MOBILE_TOKEN_REQUIRED`); device tokens are limited to `MOBILE_DEVICE_API_PREFIXES`.
5. `_guard_project_scope`: project-scoped APIs call `App.require('read'|'write')`.
6. POST/PATCH/DELETE (after the task-agent GPU routes): `_same_origin()` or `403`.
7. Errors: `{"ok": false, "error": {"status", "code", "message"}}` with `Connection: close`;
   StoreError/ProjectError are 400, unknown endpoints 404, anything else 500.

Columns: **gate** = authentication gate when auth is enabled (loopback no-auth mode lets
everything through as user `local`); **scope** = project authorization from
`_guard_project_scope`; **device** = verbs a paired phone may call; **guards** = extra checks
inside the handler; **[flags]** = checks applied earlier in the same function.
"""


def render(data: dict) -> str:
    routes = data["routes"]
    lines = [
        "# ARK HTTP/CLI contract (generated)",
        "",
        "Generated by `python -B tests/python/contract/gen_contract.py` from "
        + ", ".join(f"`{p}`" for p in data["generated_from"]) + ". Do not edit by hand;",
        "`tests/python/contract/test_contract_inventory.py` fails when this file is stale.",
        "",
        f"* Routes: **{len(routes)}** route records, "
        f"**{sum(r['role'] == 'endpoint' for r in routes)}** endpoints + "
        f"{sum(r['role'] == 'dispatch' for r in routes)} prefix dispatchers; "
        f"{len({r['id'] for r in routes if r['role'] == 'endpoint'})} unique endpoint patterns "
        f"({sum(len(r['methods']) for r in routes)} method+route pairs, HEAD counted separately)",
        f"* CLI: **{len(data['cli_commands'])}** subcommands, **{len(data['cli_flags'])}** flags/arguments",
        f"* Server JSON config keys: **{len(data['config_keys'])}**",
        f"* Environment variables read by arkstudio/ and vendor/: **{len(data['env_vars'])}**",
        "",
        PIPELINE,
        "## Routes",
        "",
        "| id | methods | function:line | gate | scope | device | guards/flags | query | body | response | errors |",
        "|---|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in routes:
        lines.append("| " + " | ".join(esc(x) for x in [
            f"`{r['id']}`", ",".join(r["methods"]), f"{r['function']}:{r['line'] or '-'}",
            r["auth_gate"], r["project_scope"], ",".join(r["mobile_device"]) or "-",
            ", ".join(r["guards"] + [f"[{f}]" for f in r["flags"]] + r["notes"]),
            ", ".join(r["query"]), ", ".join(r["body"]), "; ".join(r["response"]),
            ", ".join(r["errors"])]) + " |")
    lines += ["", "## CLI", "", "Subcommands: " + ", ".join(f"`{c}`" for c in data["cli_commands"]), "",
              "| command | flags | default | type/action | choices | required/nargs | line |",
              "|---|---|---|---|---|---|---|"]
    for f in data["cli_flags"]:
        lines.append("| " + " | ".join(esc(x) for x in [
            f["command"], " ".join(f"`{n}`" for n in f["flags"]), f["default"],
            f["type"] or f["action"], f["choices"], f["required"] or f["nargs"], f["line"]]) + " |")
    lines += ["", "## Server JSON config (`serve --config`)", "",
              "Unknown keys are rejected. Relative paths resolve against the config file folder.",
              "`--config` implies `require_auth: true`. Env-path keys are exported as env vars.", "",
              "| key | type | exported env |", "|---|---|---|"]
    for k in data["config_keys"]:
        lines.append(f"| `{k['key']}` | {k['type']} | {k['env'] or '-'} |")
    lines += ["", "## Environment variables", "", "| name | read in |", "|---|---|"]
    for name, files in data["env_vars"].items():
        lines.append(f"| `{name}` | {esc(', '.join(files))} |")
    lines += ["", "## Files under a project root (static scan)", "",
              "Path joins with a string literal found in `arkstudio/*.py`. The harness",
              "(`tests/python/contract/harness.py --list-files`) records the files a running server",
              "actually creates.", "", "| join | modules |", "|---|---|"]
    for join, files in data["root_files"].items():
        lines.append(f"| `{esc(join)}` | {esc(', '.join(files))} |")
    return "\n".join(lines) + "\n"


def render_go(data: dict) -> str:
    """The route table the Go server uses to tell 'not ported yet' from 404."""
    lines = ["// Code generated by tests/python/contract/gen_contract.py; DO NOT EDIT.", "",
             "package server", "",
             "// contractRoutes lists every route of the Python server (docs/development/go-port/contract.md).",
             "var contractRoutes = []contractRoute{"]
    for r in data["routes"]:
        lines.append("\t{ID: %s, Kind: %s, Pattern: %s, Methods: []string{%s}, Role: %s}," % (
            json.dumps(r["id"]), json.dumps(r["kind"]), json.dumps(r["pattern"]),
            ", ".join(json.dumps(m) for m in r["methods"]), json.dumps(r["role"])))
    lines.append("}")
    return "\n".join(lines) + "\n"


def main(argv: list[str]) -> int:
    data = build()
    text_json = json.dumps(data, ensure_ascii=False, indent=1) + "\n"
    text_md = render(data)
    text_go = render_go(data)
    if "--check" in argv:
        stale = [p for p, t in ((OUT_JSON, text_json), (OUT_MD, text_md), (OUT_GO, text_go))
                 if not p.is_file() or p.read_text(encoding="utf-8") != t]
        for path in stale:
            print(f"stale: {path.relative_to(ROOT)} (run tests/python/contract/gen_contract.py)")
        return 1 if stale else 0
    OUT_MD.parent.mkdir(parents=True, exist_ok=True)
    OUT_JSON.write_text(text_json, encoding="utf-8", newline="\n")
    OUT_MD.write_text(text_md, encoding="utf-8", newline="\n")
    OUT_GO.parent.mkdir(parents=True, exist_ok=True)
    OUT_GO.write_text(text_go, encoding="utf-8", newline="\n")
    print(f"routes={len(data['routes'])} cli_flags={len(data['cli_flags'])} "
          f"config_keys={len(data['config_keys'])} env_vars={len(data['env_vars'])}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
