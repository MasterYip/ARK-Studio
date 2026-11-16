"""Copy the SQLite schemas from the Python modules into the Go store package.

    python -B tests/python/contract/gen_schema.py          # rewrite go/internal/store/schema_gen.go
    python -B tests/python/contract/gen_schema.py --check  # exit 1 when it is stale

The Go server must create and migrate project and organization databases
exactly like Python, so the DDL is generated, never retyped.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
TARGET = ROOT / "go" / "internal" / "store" / "schema_gen.go"


def render() -> str:
    sys.path.insert(0, str(ROOT))
    from arkstudio import agent_tokens, db, org
    for text in (db.SCHEMA, db.REVISION_SCHEMA, db.CHANGE_LOG_SCHEMA, org.SCHEMA, agent_tokens.SCHEMA):
        assert "`" not in text
    out = ["// Code generated from arkstudio/db.py, org.py and agent_tokens.py by",
           "// tests/python/contract/gen_schema.py; DO NOT EDIT. The schema must stay identical to Python.",
           "", "package store", "",
           f"// SchemaVersion is db.SCHEMA_VERSION.\nconst SchemaVersion = {db.SCHEMA_VERSION}", "",
           "// ProjectSchema is db.SCHEMA verbatim.", "const ProjectSchema = `" + db.SCHEMA + "`", "",
           "// RevisionSchema is db.REVISION_SCHEMA verbatim (applied after migrate).",
           "const RevisionSchema = `" + db.REVISION_SCHEMA + "`", "",
           "// ChangeLogSchema is db.CHANGE_LOG_SCHEMA verbatim (applied after RevisionSchema).",
           "const ChangeLogSchema = `" + db.CHANGE_LOG_SCHEMA + "`", "",
           f"// ChangeLogKeep is db.CHANGE_LOG_KEEP.\nconst ChangeLogKeep = {db.CHANGE_LOG_KEEP}", "",
           f"// ChangeLogID is db.CHANGE_LOG_ID (meta key).\nconst ChangeLogID = {json.dumps(db.CHANGE_LOG_ID)}", "",
           f"// OrgSchemaVersion is org.ORG_SCHEMA_VERSION.\nconst OrgSchemaVersion = {org.ORG_SCHEMA_VERSION}", "",
           "// OrgSchema is org.SCHEMA verbatim.", "const OrgSchema = `" + org.SCHEMA + "`", "",
           "// AgentTokenSchema is agent_tokens.SCHEMA verbatim.",
           "const AgentTokenSchema = `" + agent_tokens.SCHEMA + "`", "",
           "// UserProfileColumns is org.USER_PROFILE_COLUMNS.", "var UserProfileColumns = [][2]string{"]
    out += [f'\t{{"{name}", "{decl}"}},' for name, decl in org.USER_PROFILE_COLUMNS]
    out.append("}")
    from arkstudio import tags
    from arkstudio.store import Store
    q = lambda s: json.dumps(s, ensure_ascii=False)  # noqa: E731 - JSON string == Go string
    out += ["", "// Relations is the key order of tags.RELATIONS.",
            "var Relations = []string{" + ", ".join(q(r) for r in tags.RELATIONS) + "}", "",
            "// SymmetricRelations are the relations whose reverse label is their label.",
            "var SymmetricRelations = []string{" + ", ".join(
                q(r) for r, m in tags.RELATIONS.items() if m["reverse"] == m["label"]) + "}", "",
            "// KnownTypes is tags.KNOWN_TYPES in order: id, label, color.",
            "var KnownTypes = [][3]string{"]
    out += ["\t{%s, %s, %s}," % (q(k), q(v.get("label", k)), q(v.get("color", "#94a3b8")))
            for k, v in tags.KNOWN_TYPES.items()]
    out += ["}", "", "// TypePrefixes is the uid prefix of every tags.KNOWN_TYPES entry.",
            "var TypePrefixes = map[string]string{" + ", ".join(
                f"{q(k)}: {q(v['prefix'])}" for k, v in tags.KNOWN_TYPES.items()) + "}", "",
            "// KnownStatuses is tags.KNOWN_STATUSES in order.",
            "var KnownStatuses = []string{" + ", ".join(q(k) for k in tags.KNOWN_STATUSES) + "}", "",
            "// KnownImportance is tags.KNOWN_IMPORTANCE in order.",
            "var KnownImportance = []string{" + ", ".join(q(k) for k in tags.KNOWN_IMPORTANCE) + "}"]
    out += ["", "// DefaultTopics is Store.DEFAULT_TOPICS: key, title, type.",
            "var DefaultTopics = [][3]string{"]
    out += ["\t{%s, %s, %s}," % tuple(q(x) for x in t) for t in Store.DEFAULT_TOPICS]
    out += ["}", "", f"// DefaultTopicsFlag is Store.DEFAULT_TOPICS_FLAG.",
            f"const DefaultTopicsFlag = {q(Store.DEFAULT_TOPICS_FLAG)}"]
    return "\n".join(out) + "\n"


def main(argv: list[str]) -> int:
    text = render()
    if "--check" in argv:
        current = TARGET.read_text(encoding="utf-8") if TARGET.is_file() else ""
        if current != text:
            print(f"stale: {TARGET.relative_to(ROOT)} (run tests/python/contract/gen_schema.py)")
            return 1
        return 0
    TARGET.write_text(text, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
