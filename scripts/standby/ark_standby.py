#!/usr/bin/env python3
"""ARK standby replication helpers (docs/operations/standby.md).

Subcommands:
  snapshot  OUT_DIR             on the primary: consistent copies of every ARK
                                SQLite database (online backup API) plus
                                manifest.json and an rsync exclude list
  apply                         on the standby: verify the received snapshot
                                and install changed databases (online; the
                                standby unit restarts only for a new configuration)
  daily                         on the standby: keep a verified full snapshot
                                per day (DAILY_KEEP, default 7)
  restore   SNAP_DIR            install a verified snapshot into the live paths
                                (used by ark-failover.sh demote on the primary)
  backup    OUT_DIR             online backup of the live databases (no files)

Settings come from the environment (ark-standby.env, sourced by the wrappers).
Databases and files keep the same absolute paths on both machines: the
organization database stores absolute project paths.
"""
from __future__ import annotations

import datetime as dt
import hashlib
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from pathlib import Path

SQLITE_MAGIC = b"SQLite format 3\x00"
DB_SUFFIXES = (".sqlite3", ".sqlite", ".db")
SIDE_FILES = ("-wal", "-shm", "-journal")
SKIP_DIRS = {".cache", "__pycache__", "node_modules", ".git", "history.git"}


def env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def now_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def log(msg: str) -> None:
    print(f"{now_iso()} {msg}", flush=True)


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def is_sqlite(path: Path) -> bool:
    try:
        with path.open("rb") as f:
            return f.read(16) == SQLITE_MAGIC
    except OSError:
        return False


def tree_roots() -> list[Path]:
    """Folders replicated as file trees (projects, private dir, extras)."""
    roots = [env("ARK_PROJECTS"), env("ARK_PRIVATE")] + env("ARK_EXTRA_DIRS").split()
    return [Path(r) for r in roots if r]


def skipped_dirs() -> set[str]:
    """Folder names never scanned: caches plus the folder patterns of
    SYNC_EXCLUDES ("name/"), which the replication leaves behind anyway."""
    extra = {p.rstrip("/") for p in env("SYNC_EXCLUDES").split() if p.endswith("/") and "*" not in p}
    return SKIP_DIRS | extra


def find_databases() -> list[Path]:
    found: set[Path] = set()
    skip = skipped_dirs()
    for root in tree_roots():
        if not root.is_dir():
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = [d for d in dirnames if d not in skip and not d.startswith(".tmp-")]
            for name in filenames:
                if name.endswith(DB_SUFFIXES):
                    p = Path(dirpath) / name
                    if is_sqlite(p):
                        found.add(p)
    for extra in env("ARK_EXTRA_DBS").split():
        p = Path(extra)
        if p.is_file() and is_sqlite(p):
            found.add(p)
    return sorted(found)


def stat_sig(db: Path) -> list:
    """Size and mtime of the database and its WAL; an empty WAL (a reader
    creates one) counts as none."""
    sig = []
    for suffix in ("", "-wal"):
        try:
            st = os.stat(str(db) + suffix)
        except FileNotFoundError:
            sig.append(None)
            continue
        sig.append(None if suffix and st.st_size == 0 else [st.st_size, st.st_mtime_ns])
    return sig


def backup_db(src: Path, dest: Path) -> None:
    """Consistent copy through the SQLite online backup API (never a file copy)."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=dest.name + ".", suffix=".part", dir=dest.parent)
    os.close(fd)
    try:
        s = sqlite3.connect(f"file:{src}?mode=ro", uri=True, timeout=60)
        d = sqlite3.connect(tmp)
        try:
            s.backup(d)
            d.execute("PRAGMA journal_mode=DELETE")
        finally:
            d.close()
            s.close()
        os.chmod(tmp, 0o600)
        os.replace(tmp, dest)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def integrity(db: Path, full: bool = False) -> str:
    c = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=60)
    try:
        rows = c.execute("PRAGMA integrity_check" if full else "PRAGMA quick_check").fetchall()
    finally:
        c.close()
    return "; ".join(str(r[0]) for r in rows[:5])


def mirror(base: Path, live: Path) -> Path:
    return base / str(live).lstrip("/")


# ---------------------------------------------------------------- primary


def cmd_snapshot(out: Path) -> int:
    db_dir = out / "db"
    db_dir.mkdir(parents=True, exist_ok=True)
    state_path = out / "state.json"
    state = json.loads(state_path.read_text()) if state_path.exists() else {}
    dbs = find_databases()
    if not any(p.name == "ark.sqlite3" for p in dbs):
        log("error: no project databases found; check ARK_PROJECTS")
        return 2
    manifest: dict[str, dict] = {}
    copied = 0
    for db in dbs:
        key = str(db)
        dest = mirror(db_dir, db)
        sig = stat_sig(db)
        prev = state.get(key)
        if prev and prev.get("sig") == sig and dest.exists():
            manifest[key] = prev["entry"]
            continue
        backup_db(db, dest)
        entry = {"sha256": sha256(dest), "size": dest.stat().st_size}
        state[key] = {"sig": sig, "entry": entry}
        manifest[key] = entry
        copied += 1
    # Databases that disappeared on the primary leave the snapshot too.
    for key in [k for k in state if k not in manifest]:
        state.pop(key)
        stale = mirror(db_dir, Path(key))
        if stale.exists():
            stale.unlink()
    version = ""
    try:
        version = subprocess.run([env("ARK_SERVER_BIN", os.path.expanduser("~/.local/bin/ark-server")), "version"],
                                 capture_output=True, text=True, timeout=10).stdout.strip()
    except Exception:
        pass
    doc = {"created": now_iso(), "primary": env("PRIMARY_NAME", os.uname().nodename),
           "primary_version": version, "databases": manifest}
    tmp = db_dir / "manifest.json.part"
    tmp.write_text(json.dumps(doc, indent=1, sort_keys=True))
    os.replace(tmp, db_dir / "manifest.json")
    state_path.write_text(json.dumps(state))
    # Exact rsync excludes: the live databases and their side files.
    lines = []
    for root in tree_roots():
        for db in dbs:
            try:
                rel = db.relative_to(root)
            except ValueError:
                continue
            for suffix in ("",) + SIDE_FILES:
                lines.append(f"{root}\t/{rel}{suffix}")
    (out / "db-excludes.tsv").write_text("\n".join(lines) + "\n")
    log(f"snapshot: {len(manifest)} databases, {copied} copied")
    return 0


# ---------------------------------------------------------------- standby


def base() -> Path:
    return Path(env("STANDBY_BASE"))


def role() -> str:
    p = base() / "state" / "role"
    return p.read_text().strip() if p.exists() else "standby"


def systemctl(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(["systemctl", "--user", *args], capture_output=True, text=True)


def verify_snapshot(snap: Path, known: dict | None = None) -> dict | None:
    """The manifest if every copy matches it.  Copies whose checksum is in
    `known` (already verified and installed) only get a size check."""
    mf = snap / "manifest.json"
    if not mf.exists():
        log(f"error: no manifest in {snap}")
        return None
    doc = json.loads(mf.read_text())
    known = known or {}
    for key, entry in doc["databases"].items():
        f = mirror(snap, Path(key))
        if not f.exists() or f.stat().st_size != entry["size"]:
            ok = False
        else:
            ok = known.get(key) == entry["sha256"] or sha256(f) == entry["sha256"]
        if not ok:
            log(f"error: snapshot copy of {key} does not match its manifest (incomplete transfer?)")
            return None
    return doc


def install_db(src: Path, live: Path) -> None:
    live.parent.mkdir(parents=True, exist_ok=True)
    tmp = live.with_name(live.name + ".standby-new")
    shutil.copyfile(src, tmp)
    os.chmod(tmp, 0o600)
    for suffix in SIDE_FILES:
        side = Path(str(live) + suffix)
        if side.exists():
            side.unlink()
    os.replace(tmp, live)


def install_db_online(src: Path, live: Path) -> None:
    """Copy a snapshot into a database the running server has open, through
    the SQLite backup API: the server sees an ordinary commit by another
    process (data_version and file stamps change), so its caches rebuild and
    nobody is signed out.  A new database is a plain file install."""
    if not live.exists():
        install_db(src, live)
        return
    s = sqlite3.connect(f"file:{src}?mode=ro", uri=True, timeout=60)
    d = sqlite3.connect(str(live), timeout=120)
    try:
        s.backup(d)
    finally:
        d.close()
        s.close()


def write_config() -> bool:
    """bkp.json = the primary's configuration with the standby's listener."""
    src = base() / "incoming" / "config" / "primary.json"
    dest = Path(os.path.expanduser(env("STANDBY_CONFIG")))
    if not src.exists():
        return False
    cfg = json.loads(src.read_text(encoding="utf-8-sig"))
    cfg.update({"host": "127.0.0.1", "port": int(env("STANDBY_PORT", "8891"))})
    if env("STANDBY_PUBLIC_URL"):
        cfg["public_url"] = env("STANDBY_PUBLIC_URL")
    text = json.dumps(cfg, indent=2) + "\n"
    if dest.exists() and dest.read_text() == text:
        return False
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_suffix(".json.part")
    tmp.write_text(text)
    os.chmod(tmp, 0o600)
    os.replace(tmp, dest)
    return True


def write_status(doc: dict, changed: int) -> None:
    st = base() / "state"
    status = {
        "last_sync": now_iso(),
        "primary": doc.get("primary", ""),
        "databases": len(doc["databases"]),
        "snapshot_created": doc.get("created", ""),
        "primary_version": doc.get("primary_version", ""),
        "changed_databases": changed,
        "checksums": {k: v["sha256"] for k, v in sorted(doc["databases"].items())},
    }
    tmp = st / "status.json.part"
    tmp.write_text(json.dumps(status, indent=1))
    os.chmod(tmp, 0o644)
    os.replace(tmp, st / "status.json")


def cmd_apply() -> int:
    if role() != "standby":
        log(f"error: this server's role is '{role()}'; refusing to apply a primary snapshot")
        return 3
    snap = base() / "incoming" / "db"
    applied_path = base() / "state" / "applied.json"
    applied = json.loads(applied_path.read_text()) if applied_path.exists() else {}
    doc = verify_snapshot(snap, applied)
    if doc is None:
        return 4
    changed = [k for k, e in doc["databases"].items() if applied.get(k) != e["sha256"] or not Path(k).exists()]
    removed = [k for k in applied if k not in doc["databases"]]
    config_changed = write_config()
    unit = env("STANDBY_UNIT", "ark-server@bkp.service")
    if changed or removed or config_changed:
        active = systemctl("is-active", unit).stdout.strip() == "active"
        # A running server takes database changes online; only a new
        # configuration needs a restart.
        restart = active and config_changed
        if restart:
            systemctl("stop", unit)
        for k in changed:
            if active and not restart:
                install_db_online(mirror(snap, Path(k)), Path(k))
            else:
                install_db(mirror(snap, Path(k)), Path(k))
            applied[k] = doc["databases"][k]["sha256"]
        for k in removed:
            for suffix in ("",) + SIDE_FILES:
                p = Path(k + suffix)
                if p.exists():
                    p.unlink()
            applied.pop(k)
        applied_path.write_text(json.dumps(applied, indent=1, sort_keys=True))
        write_status(doc, len(changed))
        if restart:
            r = systemctl("start", unit)
            if r.returncode != 0:
                log(f"error: {unit} did not start: {r.stderr.strip()}")
                return 5
    else:
        write_status(doc, 0)
    log(f"apply: {len(doc['databases'])} databases, {len(changed)} changed, {len(removed)} removed"
        + (", config updated" if config_changed else ""))
    return 0


def cmd_daily() -> int:
    day = dt.datetime.now().strftime("%Y-%m-%d")
    root = base() / "daily"
    root.mkdir(parents=True, exist_ok=True)
    final = root / day
    if final.exists():
        log(f"daily: {final} already exists")
        return 0
    work = root / (day + ".partial")
    if work.exists():
        shutil.rmtree(work)
    (work / "db").mkdir(parents=True)
    if role() == "standby":
        snap = base() / "incoming" / "db"
        doc = verify_snapshot(snap)
        if doc is None:
            return 4
        for k in doc["databases"]:
            dest = mirror(work / "db", Path(k))
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(mirror(snap, Path(k)), dest)
        source = f"replica of {doc.get('primary', '?')} ({doc.get('created', '?')})"
        dbs = list(doc["databases"])
    else:  # promoted: back up the live databases of this server
        dbs = [str(p) for p in find_databases()]
        for k in dbs:
            backup_db(Path(k), mirror(work / "db", Path(k)))
        source = "live databases (promoted standby)"
    report = {}
    bad = []
    for k in dbs:
        f = mirror(work / "db", Path(k))
        result = integrity(f, full=True)
        report[k] = {"sha256": sha256(f), "size": f.stat().st_size, "integrity": result}
        if result != "ok":
            bad.append(k)
    # Files: hard links to the replicated trees (unchanged files cost no space;
    # the replication replaces files by rename, so old links keep old content).
    excludes = work / "db-excludes.txt"
    with excludes.open("w") as ex:
        for k in dbs:
            for suffix in ("",) + SIDE_FILES:
                ex.write(os.path.basename(k) + suffix + "\n")
    for tree in tree_roots():
        if tree.is_dir():
            dest = mirror(work / "files", tree)
            dest.mkdir(parents=True, exist_ok=True)
            subprocess.run(["rsync", "-a", f"--link-dest={tree}/", f"--exclude-from={excludes}",
                            "--exclude=deployment-backups/", f"{tree}/", f"{dest}/"], check=True)
    cfg = base() / "incoming" / "config" / "primary.json"
    if cfg.exists():
        shutil.copy2(cfg, work / "primary.json")
    (work / "manifest.json").write_text(json.dumps(
        {"created": now_iso(), "source": source, "databases": report, "integrity_failures": bad}, indent=1))
    if bad:
        log(f"error: daily integrity check failed for {len(bad)} databases; kept as {work}")
        return 6
    os.replace(work, final)
    keep = int(env("DAILY_KEEP", "7"))
    days = sorted(p for p in root.iterdir() if p.is_dir() and len(p.name) == 10)
    for old in days[:-keep]:
        shutil.rmtree(old)
    log(f"daily: {final} ({len(dbs)} databases verified; kept {min(len(days), keep)})")
    return 0


def cmd_restore(snap: Path) -> int:
    doc = verify_snapshot(snap)
    if doc is None:
        return 4
    for k in doc["databases"]:
        install_db(mirror(snap, Path(k)), Path(k))
    log(f"restore: installed {len(doc['databases'])} databases from {snap}")
    return 0


def cmd_backup(out: Path) -> int:
    dbs = find_databases()
    for db in dbs:
        backup_db(db, mirror(out, db))
    log(f"backup: {len(dbs)} databases to {out}")
    return 0


def main(argv: list[str]) -> int:
    os.umask(0o077)
    if not argv:
        print(__doc__)
        return 2
    cmd, args = argv[0], argv[1:]
    if cmd == "snapshot" and len(args) == 1:
        return cmd_snapshot(Path(args[0]))
    if cmd == "apply" and not args:
        return cmd_apply()
    if cmd == "daily" and not args:
        return cmd_daily()
    if cmd == "restore" and len(args) == 1:
        return cmd_restore(Path(args[0]))
    if cmd == "backup" and len(args) == 1:
        return cmd_backup(Path(args[0]))
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
