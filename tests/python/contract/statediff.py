"""Compare the on-disk state two servers left behind after the same replay.

Every SQLite database under the data dir is dumped table by table (rows in
rowid / primary-key order, every column) and every other file is read.  The
two dumps are compared after these normalizations, applied to both sides:

S1  The absolute data directory -> <DATA> (also JSON-escaped).
S2  ISO timestamps (YYYY-MM-DDTHH:MM:SS+00:00, with or without fraction) at
    or after the replay start -> <NOW>.
S3  REAL columns holding Unix times at or after the replay start -> <TIME>
    (node_text_pending.edited_at, session clocks).
S4  Git object ids (40/64 hex) in node-history columns and refs -> <GIT>;
    commit ids include the commit time.  Git object *contents* are compared
    through the tree/blob listing below, not by hash.
S5  Files ignored: SQLite -wal/-shm/-journal files, server logs in the
    replay work dir, history.git internals (objects/, logs/, index, packed
    refs, ORIG_HEAD) -- their text is compared via `git cat-file` of each ref.
S6  Random secrets in org tables (session ids, token hashes, password hashes,
    CSRF secrets) -> <SECRET>: their *presence* and row count are compared.
    Files holding a random secret generated at startup (SECRET_FILES) are
    compared by presence and length only.
S7  Black-box snapshots: ark.sqlite3.gz is decompressed and compared row by
    row like any database; manifest.json masks db_sha256, db_size,
    snapshot_sha256 and snapshot_size (page layout and gzip framing differ)
    and the project.json mtime inside "revision".
S9  .auth-sessions.json (remembered sign-ins): sid_hash -> <SECRET>,
    created_at / expires_at at or after the replay start -> <TIME>; the
    username, project_id, remember flag and order are compared.
S8  A project's own Git working tree (the Editor's Changes view): .git/index
    is ignored -- `git diff` refreshes its cached stat data (file ctimes of
    each copy) opportunistically; every other .git file is compared.
S10 Agent worktrees of that repository (worktrees.py): .git/worktrees/<name>/
    index is ignored like S8, and in reflogs (.git/logs/**, .git/worktrees/
    <name>/logs/**) the "<unix seconds> <zone>" of each entry -> <TIME> (git
    stamps them with the wall clock).
S11 Agent sessions (agent-sessions/<id>/meta.json): "pid" -> <PID> (process
    ids), and an echo session's "command_preview" -> <ECHO> (Python runs
    demo_agent.py with its interpreter, Go its own binary; see N16); in
    every agent-sessions file "127.0.0.1:<port>" -> <PORT> (the task API
    base comes from each server's Host header, see N11); the data dir is
    also masked when JSON-escaped twice (S1; an [[ark-event]] line inside
    a log entry).
S12 Sealed personal credentials (org user_secrets.data_json): every value is
    opened with that data dir's own private/org/secret.key and compared as
    plaintext ("<opened:...>"); a value that does not open is compared as
    "<raw:...>" (rows seeded by the replay with plaintext the old save bug
    left; the servers re-seal them on read).  The key file itself stays S6.
"""
from __future__ import annotations

import json
import sys
import re
import sqlite3
import subprocess
from datetime import datetime
from pathlib import Path

ISO = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?\+00:00")
GITID = re.compile(r"\b[0-9a-f]{40}\b|\b[0-9a-f]{64}\b")
SQLITE_SIDE = ("-wal", "-shm", "-journal")
SECRET_COLUMNS = {"password_hash", "token_hash", "sid_hash", "secret", "secret_hash", "csrf_secret",
                  "code_hash", "token", "salt"}
REVISION_MTIME = re.compile(r":(\d{15,20}):")
REFLOG_TIME = re.compile(r"> \d{9,11} [+-]\d{4}(?=\t|\r?\n|$)")
SNAPSHOT_BYTES = ("db_sha256", "db_size", "snapshot_sha256", "snapshot_size")
SECRET_FILES = {".auth-secret", ".agent-token", "secret.key"}
# Sealed values (S12): opened with the data dir's own key and compared as plaintext.
SECRET_TABLE_COLUMNS = {("user_secrets", "data_json")}
GIT_COLUMNS = {"head", "commit", "commit_id"}


class StateNormalizer:
    def __init__(self, data_dir: Path, start: str):
        raw = str(Path(data_dir).resolve())
        self.paths = sorted({raw, json.dumps(raw)[1:-1], raw.replace("\\", "/"),
                             json.dumps(json.dumps(raw)[1:-1])[1:-1]}, key=len, reverse=True)
        self.start = start
        self.start_ts = datetime.fromisoformat(start).timestamp()
        self.data_dir = Path(data_dir)

    def sealed(self, row: dict, value):
        """S12: user_secrets.data_json -> {key: "<opened:plaintext>"} using
        <data>/private/org/secret.key (ARK_PRIVATE_DIR of the replay); a value
        that does not open (e.g. one stored in the clear) -> "<raw:value>"."""
        if value in (None, "", "{}"):
            return value
        try:
            sealed = json.loads(value)
        except ValueError:
            return "<unparsable>"
        if not isinstance(sealed, dict):
            return "<not-a-dict>"
        key_path = self.data_dir / "private" / "org" / "secret.key"
        out = {}
        for key, token in sealed.items():
            try:
                out[key] = "<opened:" + open_sealed(key_path, token,
                                                    f"{row.get('user_id')}:{row.get('scope')}:{key}") + ">"
            except (ValueError, TypeError, AttributeError, OSError):
                out[key] = f"<raw:{token}>"
        return json.dumps(out, ensure_ascii=False)

    def text(self, value: str) -> str:
        for path in self.paths:
            value = value.replace(path, "<DATA>")
        return ISO.sub(lambda m: "<NOW>" if m.group(0)[:19] >= self.start[:19] else m.group(0), value)

    def cell(self, column: str, value):
        if value is None:
            return None
        if isinstance(value, float) and value >= self.start_ts - 1 and value < self.start_ts + 86400:
            return "<TIME>"
        if isinstance(value, bytes):
            return "bytes:" + value.hex()
        if column in SECRET_COLUMNS and value not in ("", None):
            return "<SECRET>"
        if isinstance(value, str):
            value = self.text(value)
            if column in GIT_COLUMNS:
                value = GITID.sub("<GIT>", value)
        return value


def _dump_sqlite(path: Path, norm: StateNormalizer) -> dict:
    con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        out = {}
        tables = [r[0] for r in con.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_stat%' ORDER BY name")]
        for table in tables:
            cols = [r[1] for r in con.execute(f'PRAGMA table_info("{table}")')]
            try:
                rows = con.execute(f'SELECT * FROM "{table}" ORDER BY rowid').fetchall()
            except sqlite3.OperationalError:  # WITHOUT ROWID
                rows = sorted(con.execute(f'SELECT * FROM "{table}"').fetchall(), key=repr)
            out[table] = [{c: (norm.sealed(dict(zip(cols, row)), v) if (table, c) in SECRET_TABLE_COLUMNS
                               else norm.cell(c, v)) for c, v in zip(cols, row)} for row in rows]
        out["__schema__"] = sorted(r[0] or "" for r in con.execute("SELECT sql FROM sqlite_master"))
        return out
    finally:
        con.close()


def _git_listing(repo: Path, norm: StateNormalizer) -> list:
    """Text of every revision ref: commit message, author name, tree files."""
    def git(*args):
        return subprocess.run(["git", f"--git-dir={repo}", *args], capture_output=True,
                              check=False).stdout.decode("utf-8", "replace")
    out = []
    for line in git("for-each-ref", "--format=%(objectname)").split():
        commit = line.strip()
        meta = git("log", "-1", "--format=%an%x00%B", commit)
        parents = len(git("rev-list", "--parents", "-n1", commit).split()) - 1
        files = {name: git("cat-file", "-p", f"{commit}:{name}") for name in ("title.txt", "body.md")}
        out.append({"meta": meta, "parents": parents, "files": files})
    return sorted(out, key=lambda x: json.dumps(x, sort_keys=True))


def _unb64(text: str) -> bytes:
    import base64
    return base64.urlsafe_b64decode((text + "=" * (-len(text) % 4)).encode("ascii"))


def open_sealed(key_path: Path, token, aad: str) -> str:
    """SecretBox.open (org.py / ark_studio/server/internal/org) with the key file's key: a
    SHA-256 keystream plus HMAC-SHA256 tag over aad.  Independent of the
    server code, so the Go-only suite can open sealed values too; raises
    ValueError for anything that does not open."""
    import hashlib
    import hmac
    version, nonce_b64, cipher_b64, tag_b64 = str(token or "").split("$", 3)
    if version != "ark1":
        raise ValueError("unknown version")
    nonce, cipher, tag = _unb64(nonce_b64), _unb64(cipher_b64), _unb64(tag_b64)
    raw = Path(key_path).read_bytes()
    if len(raw) < 16:
        raise ValueError("short key")
    key = hashlib.sha256(raw).digest()
    context = aad.encode("utf-8")
    if not hmac.compare_digest(hmac.new(key, b"ark-org-tag" + context + nonce + cipher,
                                        hashlib.sha256).digest(), tag):
        raise ValueError("tag mismatch")
    stream = bytearray()
    counter = 0
    while len(stream) < len(cipher):
        stream.extend(hashlib.sha256(b"ark-org-keystream" + key + context + nonce
                                     + counter.to_bytes(8, "big")).digest())
        counter += 1
    return bytes(a ^ b for a, b in zip(cipher, stream)).decode("utf-8", "replace")


def dump(data_dir: Path, start: str, *, skip: tuple[str, ...] = ()) -> dict:
    data_dir = Path(data_dir)
    norm = StateNormalizer(data_dir, start)
    out = {}
    for path in sorted(data_dir.rglob("*")):
        rel = path.relative_to(data_dir).as_posix()
        if any(rel.startswith(s) for s in skip):
            continue
        parts = rel.split("/")
        if "history.git" in parts:
            if path.name == "history.git" and path.is_dir():
                out[rel] = {"git": _git_listing(path, norm)}
            continue
        if path.is_dir() or rel.endswith(SQLITE_SIDE) or (path.name == "index" and parts[-2:-1] == [".git"]) \
                or (path.name == "index" and parts[-3:-2] == ["worktrees"] and ".git" in parts):
            continue
        if path.suffix in (".sqlite3", ".db", ".sqlite"):
            out[rel] = {"sqlite": _dump_sqlite(path, norm)}
            continue
        if rel.endswith(".sqlite3.gz"):
            import gzip
            import tempfile
            with tempfile.TemporaryDirectory() as scratch:
                raw = Path(scratch) / "snapshot.sqlite3"
                raw.write_bytes(gzip.decompress(path.read_bytes()))
                out[rel] = {"sqlite": _dump_sqlite(raw, norm)}
            continue
        if parts[0] == "blackbox" and path.name == "manifest.json":
            manifest = json.loads(path.read_text(encoding="utf-8"))
            for key in SNAPSHOT_BYTES:
                if key in manifest:
                    manifest[key] = "<SNAPSHOT>"
            if isinstance(manifest.get("revision"), str):  # project.json mtime_ns, see N5
                manifest["revision"] = REVISION_MTIME.sub(":<MTIME>:", manifest["revision"])
            out[rel] = {"text": norm.text(json.dumps(manifest, ensure_ascii=False, sort_keys=True))}
            continue
        if path.name == ".auth-sessions.json":
            state = json.loads(path.read_text(encoding="utf-8"))
            for item in state.get("sessions") or []:
                item["sid_hash"] = "<SECRET>"
                for key in ("created_at", "expires_at"):
                    if isinstance(item.get(key), (int, float)) and item[key] >= norm.start_ts:
                        item[key] = "<TIME>"
            out[rel] = {"text": norm.text(json.dumps(state, ensure_ascii=False, sort_keys=True))}
            continue
        data = path.read_bytes()
        if path.name in SECRET_FILES:
            out[rel] = {"secret_len": len(data.strip())}
            continue
        if parts[0] == "agent-sessions":
            text = re.sub(r"127\.0\.0\.1:\d+", "127.0.0.1:<PORT>", data.decode("utf-8", "replace"))
            data = text.encode()
        if parts[0] == "agent-sessions" and path.name == "meta.json":
            text = data.decode("utf-8", "replace")
            text = re.sub(r'("pid": )\d+', r'\1"<PID>"', text)
            if re.search(r'"platform": "echo"', text):
                text = re.sub(r'("command_preview": )"(?:[^"\\]|\\.)*"', r'\1"<ECHO>"', text)
            data = text.encode()
        if ".git" in parts and "logs" in parts[parts.index(".git"):]:
            data = REFLOG_TIME.sub("> <TIME>", data.decode("utf-8", "replace")).encode()
        try:
            out[rel] = {"text": norm.text(data.decode("utf-8"))}
        except UnicodeDecodeError:
            out[rel] = {"bytes": data.hex()}
    return out


def diff(a: dict, b: dict, *, limit: int = 30) -> list[str]:
    problems = []
    for rel in sorted(set(a) | set(b)):
        if rel not in a or rel not in b:
            problems.append(f"file only in {'python' if rel in a else 'go'}: {rel}")
            continue
        va, vb = a[rel], b[rel]
        if va == vb:
            continue
        if "sqlite" in va and "sqlite" in vb:
            for table in sorted(set(va["sqlite"]) | set(vb["sqlite"])):
                ta, tb = va["sqlite"].get(table), vb["sqlite"].get(table)
                if ta == tb:
                    continue
                if ta is None or tb is None:
                    problems.append(f"{rel}: table {table} only on one side")
                    continue
                if len(ta) != len(tb):
                    problems.append(f"{rel}: {table} has {len(ta)} rows (python) vs {len(tb)} (go)")
                for n, (ra, rb) in enumerate(zip(ta, tb)):
                    if ra != rb:
                        cols = [c for c in ra if ra.get(c) != rb.get(c)]
                        problems.append(f"{rel}: {table} row {n}: " + "; ".join(
                            f"{c}: {ra.get(c)!r:.200} != {rb.get(c)!r:.200}" for c in cols))
                        break
        else:
            ta, tb = json.dumps(va, ensure_ascii=False), json.dumps(vb, ensure_ascii=False)
            at = next((i for i in range(min(len(ta), len(tb))) if ta[i] != tb[i]), min(len(ta), len(tb)))
            problems.append(f"{rel}: differs at {at}: py={ta[max(0, at - 80):at + 120]!r} "
                            f"go={tb[max(0, at - 80):at + 120]!r}")
        if len(problems) >= limit:
            break
    return problems
