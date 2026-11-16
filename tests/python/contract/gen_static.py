"""Snapshot Python's static payloads into the Go server.

    python -B tests/python/contract/gen_static.py          # rewrite go/internal/server/static_gen.go
    python -B tests/python/contract/gen_static.py --check  # exit 1 when stale

Some responses are pure functions of Python constants or of files shipped with
the code (skill catalog, agent interface descriptor, tag vocabulary, the type
table, profile options).  The Go server serves these snapshots verbatim; the
inventory test fails as soon as the Python side changes.

The setup payload of a project *without* setup files depends only on the
viewer.  It is captured once per viewer class with sentinel strings for the
user, token kind, host and host source.  The generator checks that
substituting real values into a template reproduces Python's payload exactly
for several viewers before it writes anything.
"""
from __future__ import annotations

import json
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
TARGET = ROOT / "go" / "internal" / "server" / "static_gen.go"

S_USER, S_KIND, S_HOST, S_SRC = "Zq9user", "Zq9kind", "Zq9host00000", "Zq9src"
S_LABEL = "host-" + S_HOST[:6]


def go_string(text: str) -> str:
    """A JSON string literal is a valid Go interpreted string literal."""
    return json.dumps(text, ensure_ascii=False)


def dumps(value) -> str:
    return json.dumps(value, ensure_ascii=False)


def setup_templates(layers, base):
    classes = {
        "admin": dict(admin=True, can_write=True),
        "write": dict(admin=False, can_write=True),
        "read": dict(admin=False, can_write=False),
    }
    out = {}
    real_fp = base.machine_fingerprint
    with tempfile.TemporaryDirectory() as empty:
        for server in (False, True):
            base.machine_fingerprint = (lambda: S_HOST) if server else (lambda: "000000000000")
            try:
                for name, flags in classes.items():
                    viewer = layers.Viewer(user=S_USER, token_kind=S_KIND, host_id=S_HOST,
                                           host_source=S_SRC, **flags)
                    out[f"{name}:{int(server)}"] = dumps(layers.payload(
                        empty, viewer, default_owner=S_USER, known_user=lambda n: n))
                viewer = layers.legacy_viewer(S_HOST)
                viewer.host_source = S_SRC
                out[f"legacy:{int(server)}"] = dumps(layers.payload(
                    empty, viewer, default_owner=S_USER, known_user=lambda n: n))
            finally:
                base.machine_fingerprint = real_fp
        # Self-check: substitution reproduces real payloads.
        for user, kind, host, src in (("alice", "session", "lab-host-1", "query"),
                                      ("local", "local", "abcdef123456", "server"),
                                      ("u.ser@x", "personal", "h", "header")):
            viewer = layers.Viewer(user=user, admin=False, can_write=True, token_kind=kind,
                                   host_id=host, host_source=src)
            real = dumps(layers.payload(empty, viewer, default_owner=user, known_user=lambda n: n))
            server = int(host == base.machine_fingerprint())
            got = substitute(out[f"write:{server}"], user, kind, host, src)
            if got != real:
                raise SystemExit(f"setup template substitution differs for {user}/{host}")
            if Path(empty).exists() and any(Path(empty).iterdir()):
                raise SystemExit("setup payload wrote files; the template approach is invalid")
    return out


def substitute(template: str, user: str, kind: str, host: str, src: str) -> str:
    def j(text):  # JSON-escaped body of a string
        return json.dumps(text, ensure_ascii=False)[1:-1]
    return (template.replace(j(S_LABEL), j("host-" + host[:6])).replace(S_HOST, j(host))
            .replace(S_USER, j(user)).replace(S_KIND, j(kind)).replace(S_SRC, j(src)))


def render() -> str:
    sys.path.insert(0, str(ROOT))
    import mimetypes

    from arkstudio import demo, linux_account, org, skills, tags
    from arkstudio.skills import setup as base
    from arkstudio.skills import setup_layers as layers

    skill_ids = [s["id"] for s in skills.SKILLS]
    static = {
        "vocabulary": dumps(tags.public_vocabulary()),
        "skills_catalog": dumps(skills.catalog_payload()),
        "skills_interface": dumps(skills.interface.describe(skill_ids=skill_ids)),
        "profile_options": dumps({
            "palette": list(org.AVATAR_COLORS), "langs": list(org.PROFILE_LANGS),
            "themes": list(org.PROFILE_THEMES),
            "limits": {"display_name": org.MAX_DISPLAY_NAME, "bio": org.MAX_BIO,
                       "email": org.MAX_EMAIL, "avatar_bytes": org.MAX_AVATAR_BYTES}}),
        "known_types": dumps([{"id": k, "label": v.get("label", k), "color": v.get("color", "#94a3b8")}
                              for k, v in tags.KNOWN_TYPES.items()]),
        "sudo_hint": dumps(linux_account.SUDO_HINT),
        "avatar_colors": dumps(list(org.AVATAR_COLORS)),
        # CPython's built-in mimetypes tables; the registry / mime.types overlay
        # is applied at runtime like mimetypes.init() does.
        "mimetypes": dumps({"types": mimetypes._types_map_default,
                            "suffix": mimetypes._suffix_map_default,
                            "encodings": mimetypes._encodings_map_default}),
        "demo": dumps({"id": demo.DEMO_ID, "name": demo.DEMO_NAME, "description": demo.DEMO_DESCRIPTION}),
    }
    for sid in skill_ids:
        static[f"skill:{sid}"] = dumps(skills.get_skill(sid))
        static[f"skill_nodocs:{sid}"] = dumps(skills.get_skill(sid, include_docs=False))
        static[f"prompt:{sid}"] = dumps(skills.agent_prompt(sid))
    templates = setup_templates(layers, base)
    lines = ["// Code generated by tests/python/contract/gen_static.py; DO NOT EDIT.", "",
             "package server", "",
             "// staticJSON holds Python's static payloads, encoded exactly as json.dumps does.",
             "var staticJSON = map[string]string{"]
    # A blank line between entries keeps gofmt from re-aligning them.
    for n, key in enumerate(sorted(static)):
        lines += ([""] if n else []) + [f"\t{go_string(key)}: {go_string(static[key])},"]
    lines += ["}", "", "// skillIDs is the order of skills.SKILLS.",
              "var skillIDs = []string{" + ", ".join(go_string(s) for s in skill_ids) + "}", "",
              "// setupTemplates are setup payloads of a project without setup files, per",
              "// viewer class and \"is this the server host\", with sentinel strings.",
              "var setupTemplates = map[string]string{"]
    for n, key in enumerate(sorted(templates)):
        lines += ([""] if n else []) + [f"\t{go_string(key)}: {go_string(templates[key])},"]
    from arkstudio import server as srv
    lines += ["}", "", "// Paths from server.py (LOGIN_ASSETS, PUBLIC_AUTH_PATHS, GLOBAL_API_*)."]
    for go_name, py_name in (("loginAssets", "LOGIN_ASSETS"), ("publicAuthPaths", "PUBLIC_AUTH_PATHS"),
                             ("globalAPIPaths", "GLOBAL_API_PATHS")):
        lines.append(f"var {go_name} = set(" + ", ".join(
            go_string(x) for x in sorted(getattr(srv, py_name))) + ")")
    lines.append("")
    lines.append("var globalAPIPrefixes = []string{" + ", ".join(
        go_string(x) for x in srv.GLOBAL_API_PREFIXES) + "}")
    consts = [("setupUser", S_USER), ("setupKind", S_KIND), ("setupHost", S_HOST),
              ("setupSource", S_SRC), ("setupLabel", S_LABEL)]
    width = max(len(name) for name, _ in consts)  # gofmt alignment
    lines += ["", "// Setup template sentinels.", "const ("]
    lines += [f"\t{name.ljust(width)} = {go_string(value)}" for name, value in consts]
    lines.append(")")
    return "\n".join(lines) + "\n"


def main(argv: list[str]) -> int:
    text = render()
    if "--check" in argv:
        current = TARGET.read_text(encoding="utf-8") if TARGET.is_file() else ""
        if current != text:
            print(f"stale: {TARGET.relative_to(ROOT)} (run tests/python/contract/gen_static.py)")
            return 1
        return 0
    TARGET.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
