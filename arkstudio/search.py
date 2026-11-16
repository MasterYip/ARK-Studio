"""Workbench Search: full-text search over nodes and project files.

Both searches share one matcher (plain text, match case, whole word or a
regular expression) and return every matching line with the match ranges,
so the client can highlight them.  They are bounded: result, per-item and
time limits, files over ``MAX_FILE_BYTES`` and binary files are skipped,
ignored/protected folders follow the Explorer's rules and, inside a git
work tree, files ignored by ``.gitignore`` are skipped.

Speed (results are identical to a plain line-by-line scan):

* Nodes are read once per project revision into memory (title, body, tags,
  comments) and each node is tested with one whole-text test (``Prefilter``)
  before its lines are matched.
* Files are listed with ``os.scandir`` (cached stats, folders in parallel),
  read on a small thread pool, kept in a bounded in-memory text cache
  validated by size and mtime, and tested whole before line matching.
  While most texts are not cached yet and ripgrep is available (``ARK_RG``
  or ``rg`` on PATH), it narrows plain-text queries to candidate files.
* Plain-text queries find their matching lines with one search over the
  whole text instead of one per line.
* ``iter_search_nodes`` / ``iter_search_files`` yield results as they are
  found (the routes stream them with ``stream=1``) and stop when the caller
  closes them or ``cancelled()`` becomes true.

Routes (see server.py): ``GET /api/search/nodes`` and
``GET /api/workspace/grep``.
"""
from __future__ import annotations

import bisect
import fnmatch
import itertools
import os
import re
import shutil
import subprocess
import threading
import time
import weakref
from collections import OrderedDict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from . import workspace as ws

MAX_QUERY = 500
MAX_LINE_CHARS = 240
MAX_RESULTS = 2000          # matching lines over all items
MAX_PER_ITEM = 100          # matching lines per node / file
MAX_ITEMS = 500             # nodes or files with matches
MAX_FILES_VISITED = 8000
MAX_FILE_BYTES = ws.MAX_FILE_BYTES
TIME_LIMIT = 4.0            # seconds
READ_THREADS = 8


def _cache_mb() -> int:
    try:
        return max(0, int(os.environ.get("ARK_SEARCH_CACHE_MB", "192")))
    except ValueError:
        return 192


TEXT_CACHE_BYTES = _cache_mb() * 1024 * 1024
RG_TIMEOUT = 20.0


class SearchError(ValueError):
    pass


def build_matcher(query: str, *, case: bool = False, word: bool = False, regex: bool = False) -> re.Pattern:
    text = str(query or "")
    if not text.strip():
        raise SearchError("query is required")
    if len(text) > MAX_QUERY:
        raise SearchError("query is too long")
    pattern = text if regex else re.escape(text)
    if word:
        pattern = rf"(?<!\w)(?:{pattern})(?!\w)"
    try:
        compiled = re.compile(pattern, 0 if case else re.IGNORECASE)
    except re.error as exc:
        raise SearchError(f"invalid regular expression: {exc}") from exc
    if compiled.search(""):
        raise SearchError("the pattern matches empty text")
    return compiled


def _clip(line: str, ranges: list[list[int]]) -> tuple[str, list[list[int]], int]:
    """Keep a long line readable around its first match; shift the ranges."""
    if len(line) <= MAX_LINE_CHARS:
        return line, ranges, 0
    start = max(0, ranges[0][0] - 60) if ranges else 0
    end = min(len(line), start + MAX_LINE_CHARS)
    out = [[max(0, a - start), min(end, b) - start] for a, b in ranges if a < end and b > start]
    return line[start:end], out, start


def _literal_lines(text: str, lines: list[str], matcher: re.Pattern):
    """``(number, line)`` of the lines holding a match of a plain-text
    matcher, found with one search over the whole text.  Exact for plain
    text (with or without whole word): such a match never contains a line
    separator, and the word lookarounds see a separator as a non-word
    character just like a line edge, so the matches inside each line are
    the ones a per-line search finds."""
    ends = list(itertools.accumulate(map(len, text.splitlines(True))))
    last = -1
    for match in matcher.finditer(text):
        index = bisect.bisect_right(ends, match.start())
        if index != last and index < len(lines):
            last = index
            yield index + 1, lines[index]


def line_matches(text: str, matcher: re.Pattern, limit: int = MAX_PER_ITEM, *,
                 literal: bool = False) -> tuple[list[dict], bool]:
    """Every matching line (1-based) with match ranges; True when capped.
    ``literal``: the matcher is plain text (not a regex), which allows
    finding the matching lines with one whole-text search."""
    hits: list[dict] = []
    text = str(text)
    lines = text.splitlines() or [""]
    numbered = _literal_lines(text, lines, matcher) if literal else enumerate(lines, start=1)
    for number, line in numbered:
        ranges = [[m.start(), m.end()] for m in matcher.finditer(line) if m.end() > m.start()]
        if not ranges:
            continue
        shown, shifted, offset = _clip(line, ranges[:20])
        hits.append({"line": number, "text": shown, "ranges": shifted, "offset": offset})
        if len(hits) >= limit:
            return hits, True
    return hits, False


def _patterns(value) -> list[str]:
    if isinstance(value, (list, tuple)):
        items = value
    else:
        items = str(value or "").split(",")
    return [item.strip().replace("\\", "/") for item in items if str(item).strip()][:40]


def _glob_match(path: str, pattern: str) -> bool:
    """VS Code-ish globs: a bare name/pattern matches any path segment or the
    file name; a pattern with a slash matches the whole relative path (or a
    folder prefix); ``**`` spans folders."""
    pattern = pattern.strip("/")
    if not pattern:
        return False
    if "/" not in pattern:
        parts = path.split("/")
        return any(fnmatch.fnmatch(part, pattern) for part in parts)
    if fnmatch.fnmatch(path, pattern) or fnmatch.fnmatch(path, pattern.replace("**/", "")):
        return True
    return path.startswith(pattern.rstrip("*").rstrip("/") + "/")


def path_filter(include, exclude):
    inc, exc = _patterns(include), _patterns(exclude)

    def accept(path: str) -> bool:
        if inc and not any(_glob_match(path, p) for p in inc):
            return False
        return not any(_glob_match(path, p) for p in exc)
    return accept


# --------------------------------------------------------------- prefilter
# With ``re.IGNORECASE`` an ASCII letter also matches these non-ASCII
# characters (dotted and dotless i, long s, Kelvin sign); every other match
# of an ASCII query is ASCII.  Texts holding one of them skip the byte-level
# ignore-case test below.  The tests recompute this set from ``re``.
ASCII_FOLD_EXTRA = "\u0130\u0131\u017f\u212a"
_ASCII_FOLD_EXTRA_UTF8 = tuple(ch.encode("utf-8") for ch in ASCII_FOLD_EXTRA)
_UNSAFE = b""


class Text:
    """A searched text and, built on first use, its UTF-8 bytes with ASCII
    letters lower-cased (``folded``; None when the text holds one of
    ``ASCII_FOLD_EXTRA``)."""

    __slots__ = ("text", "_folded")

    def __init__(self, text: str):
        self.text = text
        self._folded = None

    def folded(self) -> bytes | None:
        if self._folded is None:
            if self.text.isascii():
                self._folded = self.text.encode("ascii").lower()
            else:
                raw = self.text.encode("utf-8", "surrogatepass")
                unsafe = any(seq in raw for seq in _ASCII_FOLD_EXTRA_UTF8)
                self._folded = _UNSAFE if unsafe else raw.lower()
        return self._folded or None


class Prefilter:
    """A fast test that is true for every text holding at least one matching
    line (it may also be true for some texts without one, never the reverse);
    the exact per-line pass then decides, so results do not change.

    * plain text, match case: substring test;
    * plain ASCII text, ignore case: substring test on ``Text.folded``
      (falls back to one ``re`` search for texts with ``ASCII_FOLD_EXTRA``);
    * other plain text: one ``re`` search over the whole text;
    * whole word: the same tests on the bare query;
    * regex without anchors, word boundaries or lookarounds: one ``re``
      search over the whole text (a line separator is never part of such a
      match's context).  Other regexes are not prefiltered.
    """

    def __init__(self, matcher: re.Pattern, query: str, *, case: bool, regex: bool):
        self.matcher = matcher
        self.literal = None if regex else str(query)
        self.case = case
        self.fold = None
        if self.literal is not None and not case and self.literal.isascii():
            self.fold = self.literal.lower().encode("ascii")
        self.whole = not (regex and re.search(r"\^|\$|\\[AZbB]|\(\?<?[=!]", matcher.pattern))

    def __call__(self, item: Text) -> bool:
        if self.literal is not None:
            if self.case:
                return self.literal in item.text
            if self.fold is not None:
                folded = item.folded()
                if folded is not None:
                    return self.fold in folded
            return self.matcher.search(item.text) is not None
        if self.whole:
            return self.matcher.search(item.text) is not None
        return True


# ----------------------------------------------------------------- files
# File texts by absolute path, validated by mtime and size: path ->
# [mtime_ns, size, Text | None, scan].  LRU, but an entry used by the running
# scan is never evicted for another one of the same scan, so a workspace
# larger than the budget keeps a stable cached part instead of thrashing.
_text_cache: OrderedDict[str, list] = OrderedDict()
_text_cache_bytes = 0
_text_lock = threading.Lock()
_scan_ids = itertools.count(1)


def _cached_text(path: str, mtime_ns: int, size: int, scan: int = 0):
    """The cached ``Text`` (None for binary), or ``...`` on a miss."""
    global _text_cache_bytes
    with _text_lock:
        hit = _text_cache.get(path)
        if hit is None:
            return ...
        if hit[0] != mtime_ns or hit[1] != size:
            del _text_cache[path]
            _text_cache_bytes -= _cost(hit[1])
            return ...
        hit[3] = scan
        _text_cache.move_to_end(path)
        return hit[2]


def _cost(size: int) -> int:
    return 2 * size + 200    # text plus its lower-cased copy


def _remember_text(path: str, mtime_ns: int, size: int, text: Text | None, scan: int = 0) -> None:
    global _text_cache_bytes
    cost = _cost(size)
    if cost > TEXT_CACHE_BYTES // 8:
        return
    with _text_lock:
        old = _text_cache.pop(path, None)
        if old:
            _text_cache_bytes -= _cost(old[1])
        while _text_cache_bytes + cost > TEXT_CACHE_BYTES and _text_cache:
            oldest = next(iter(_text_cache.values()))
            if scan and oldest[3] == scan:
                return
            _, dropped = _text_cache.popitem(last=False)
            _text_cache_bytes -= _cost(dropped[1])
        _text_cache[path] = [mtime_ns, size, text, scan]
        _text_cache_bytes += cost


def clear_text_cache() -> None:
    global _text_cache_bytes
    with _text_lock:
        _text_cache.clear()
        _text_cache_bytes = 0


def _read(path: str, mtime_ns: int, size: int, scan: int = 0):
    """``Text`` for a text file, None for a binary one, ``...`` on an error."""
    cached = _cached_text(path, mtime_ns, size, scan)
    if cached is not ...:
        return cached
    try:
        with open(path, "rb") as handle:
            raw = handle.read()
    except OSError:
        return ...
    text = None if ws._looks_binary(raw) else Text(raw.decode("utf-8", "replace"))
    _remember_text(path, mtime_ns, size, text, scan)
    return text


def _entry_is_link(entry: os.DirEntry) -> bool:
    """``workspace._is_link_like`` on a scandir entry (symlink or reparse
    point), using the entry's cached stat."""
    try:
        if entry.is_symlink():
            return True
        stat = entry.stat(follow_symlinks=False)
    except OSError:
        return False
    flags = getattr(stat, "st_file_attributes", 0)
    return bool(flags & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400))


def _list_dir(path: str):
    try:
        with os.scandir(path) as listing:
            entries = list(listing)
    except OSError:
        return [], []
    dirs, files = [], []
    for entry in entries:
        try:
            is_dir = entry.is_dir()
        except OSError:
            is_dir = False
        (dirs if is_dir else files).append(entry)
    return dirs, files


def _walk(root_path: Path, pool: ThreadPoolExecutor | None = None,
          deadline: float | None = None) -> tuple[list[tuple], bool]:
    """Every file entry ``(rel, name, path, entry)`` in the order of the plain
    scan (``os.walk`` top-down, folders and files sorted case-insensitively,
    a folder's files before its subfolders), ignored names included so the
    scan caps count exactly as before, and whether the listing is complete
    (False when ``deadline`` passed first).  Folders are listed level by
    level, in parallel when a pool is given."""
    listed: dict[str, tuple[list, list]] = {}
    children: dict[str, list[tuple[str, str]]] = {}
    level = [(str(root_path), "")]
    complete = True
    while level:
        if deadline is not None and time.monotonic() >= deadline:
            complete = False
            for _, rel_dir in level:
                listed[rel_dir], children[rel_dir] = ([], []), []
            break
        results = pool.map(_list_dir, [path for path, _ in level]) if pool else map(_list_dir, [p for p, _ in level])
        next_level = []
        for (path, rel_dir), (dirs, files) in zip(level, results):
            files.sort(key=lambda e: e.name.lower())
            keep = sorted(
                (e for e in dirs if not ws._is_ignored(e.name)
                 and not (rel_dir == "" and ws._is_protected(root_path, e.name))
                 and not _entry_is_link(e)),
                key=lambda e: e.name.lower())
            listed[rel_dir] = (files, keep)
            subs = [(e.path, f"{rel_dir}/{e.name}" if rel_dir else e.name) for e in keep]
            children[rel_dir] = subs
            next_level.extend(subs)
        level = next_level
    out: list[tuple] = []
    stack = [""]
    while stack:
        rel_dir = stack.pop()
        files, _ = listed[rel_dir]
        for entry in files:
            out.append((f"{rel_dir}/{entry.name}" if rel_dir else entry.name, entry.name, entry.path, entry))
        stack.extend(rel for _, rel in reversed(children[rel_dir]))
    return out, complete


_git_roots: dict[str, tuple[float, bool]] = {}


def _in_git_work_tree(root_path: Path) -> bool:
    key = str(root_path)
    now = time.monotonic()
    hit = _git_roots.get(key)
    if hit and now - hit[0] < 30:
        return hit[1]
    inside = False
    if shutil.which("git"):
        try:
            result = subprocess.run(["git", "rev-parse", "--is-inside-work-tree"], cwd=root_path,
                                    capture_output=True, timeout=5)
            inside = result.returncode == 0 and result.stdout.strip() == b"true"
        except (OSError, subprocess.SubprocessError):
            inside = False
    _git_roots[key] = (now, inside)
    return inside


def _git_ignored(root_path: Path, rels: list[str]) -> set[str]:
    """Paths ignored by ``.gitignore`` (and the other git exclude sources)
    when the root is inside a git work tree; empty otherwise."""
    if not rels or not _in_git_work_tree(root_path):
        return set()
    try:
        result = subprocess.run(["git", "-c", "core.quotepath=off", "check-ignore", "--stdin", "-z"],
                                cwd=root_path, input="\0".join(rels).encode("utf-8"),
                                capture_output=True, timeout=15)
    except (OSError, subprocess.SubprocessError):
        return set()
    if result.returncode not in (0, 1):
        return set()
    return {item for item in result.stdout.decode("utf-8", "replace").split("\0") if item}


_rg_lookup: dict[str, tuple[float, str | None]] = {}


def ripgrep_path() -> str | None:
    """ripgrep binary: ``ARK_RG`` (a path, or ``none`` to disable) or ``rg``
    on PATH (looked up at most once a minute)."""
    configured = os.environ.get("ARK_RG", "").strip()
    if configured.lower() in ("none", "off", "0", "false"):
        return None
    hit = _rg_lookup.get(configured)
    if hit and time.monotonic() - hit[0] < 60:
        return hit[1]
    if configured:
        found = configured if Path(configured).is_file() else None
    else:
        found = shutil.which("rg")
    _rg_lookup[configured] = (time.monotonic(), found)
    return found


def _rg_candidates(root_path: Path, query: str, *, case: bool, cancelled=None) -> set[str] | None:
    """Relative paths of the files that contain the literal ``query`` (a
    superset of the files with a matching line), or None when ripgrep is
    unavailable or cannot answer exactly enough."""
    rg = ripgrep_path()
    if not rg or "\n" in query or "\r" in query:
        return None
    if not case and not query.isascii():
        return None   # non-ASCII case folding is left to Python's re
    args = [rg, "--files-with-matches", "--no-messages", "--no-config", "--no-ignore", "--hidden",
            "--text", "--fixed-strings", "--case-sensitive" if case else "--ignore-case",
            "--max-filesize", str(MAX_FILE_BYTES)]
    for name in sorted(ws.IGNORED_DIRS):
        args += ["--glob", f"!{name}/"]
    args += ["--regexp", query]
    if not case and "i" in query.lower():
        # re also matches i with dotted/dotless i, which ripgrep's case
        # folding does not: keep every file holding one as a candidate.
        args += ["--regexp", "\u0130", "--regexp", "\u0131"]
    args += ["--", "."]
    try:
        proc = subprocess.Popen(args, cwd=root_path, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    except OSError:
        return None
    try:
        deadline = time.monotonic() + RG_TIMEOUT
        while True:
            try:
                out, _ = proc.communicate(timeout=0.05)
                break
            except subprocess.TimeoutExpired:
                if (cancelled and cancelled()) or time.monotonic() > deadline:
                    return None
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
    if proc.returncode not in (0, 1):
        return None
    found = set()
    for raw in out.split(b"\n"):
        rel = os.fsdecode(raw.strip()).replace("\\", "/")
        if rel.startswith("./"):
            rel = rel[2:]
        if rel:
            found.add(rel)
    return found


_warming = threading.Lock()


def _warm_later(items: list[tuple]) -> None:
    """Read files skipped thanks to ripgrep into the text cache in the
    background (one warm-up at a time), so the next searches can use the
    faster in-memory scan."""
    if not items or not _warming.acquire(blocking=False):
        return

    def run():
        try:
            for path, mtime_ns, size in items:
                if _cached_text(path, mtime_ns, size) is ...:
                    _read(path, mtime_ns, size)
        finally:
            _warming.release()
    threading.Thread(target=run, name="ark-search-warm", daemon=True).start()


def iter_search_files(root: str | Path, query: str, *, case=False, word=False, regex=False,
                      include="", exclude="", time_limit: float = TIME_LIMIT, cancelled=None,
                      use_rg: bool | None = None):
    """Yield the matching files in Explorer order, then a summary dict with
    ``"done": True``.  Closing the generator or ``cancelled()`` returning
    true stops the scan (the summary then reports ``truncated``)."""
    matcher = build_matcher(query, case=case, word=word, regex=regex)
    prefilter = Prefilter(matcher, query, case=case, regex=regex)
    accept = path_filter(include, exclude)
    root_path = Path(root).resolve()
    started = time.monotonic()
    scan = next(_scan_ids)
    total = visited = skipped = found = 0
    truncated = timed_out = False
    backend = "python"
    pool = ThreadPoolExecutor(max_workers=READ_THREADS, thread_name_prefix="ark-search")
    pending: dict[int, object] = {}
    try:
        plan, complete = _walk(root_path, pool, started + time_limit)
        ignored = _git_ignored(root_path, [item[0] for item in plan])
        if ignored:
            plan = [item for item in plan if item[0] not in ignored]
        # What the plain scan would do per listed file: None (not opened),
        # ("big"|"error", None) (skipped), ("miss", None) (opened, no match)
        # or ("read", args of _read).
        work: list = []
        for rel, name, path, entry in plan:
            if _entry_is_link(entry) or ws._is_ignored(name) or not accept(rel):
                work.append(None)
                continue
            try:
                stat = entry.stat(follow_symlinks=False)
            except OSError:
                work.append(("error", None))
                continue
            if stat.st_size > MAX_FILE_BYTES:
                work.append(("big", None))
            else:
                work.append(("read", (path, stat.st_mtime_ns, stat.st_size, scan)))
        # ripgrep pays off when most texts are not cached yet: it narrows the
        # files to read to those containing the literal query.
        reads = [i for i, step in enumerate(work) if step and step[0] == "read"]
        if not regex and use_rg is not False and reads:
            cold = sum(1 for i in reads if _cached_text(*work[i][1]) is ...)
            if use_rg or cold * 2 > len(reads):
                candidates = _rg_candidates(root_path, str(query), case=case, cancelled=cancelled)
                if candidates is not None:
                    backend = "rg"
                    skipped_reads = []
                    for i in reads:
                        if plan[i][0] not in candidates:
                            skipped_reads.append(work[i][1][:3])
                            work[i] = ("miss", None)
                    _warm_later(skipped_reads)
        ahead = READ_THREADS * 8
        submitted = 0

        def submit(upto: int) -> None:
            nonlocal submitted
            while submitted < len(work) and submitted <= upto:
                step = work[submitted]
                if step is not None and step[0] == "read":
                    cached = _cached_text(*step[1])
                    pending[submitted] = cached if cached is not ... else pool.submit(_read, *step[1])
                submitted += 1

        for index, step in enumerate(work):
            if cancelled and cancelled():
                truncated = True
                break
            if time.monotonic() - started >= time_limit:
                timed_out = truncated = True
                break
            if visited >= MAX_FILES_VISITED or found >= MAX_ITEMS or total >= MAX_RESULTS:
                truncated = True
                break
            if step is None:
                continue
            visited += 1
            kind = step[0]
            if kind in ("big", "error"):
                skipped += 1
                continue
            if kind == "miss":
                continue
            submit(index + ahead)
            text = pending.pop(index)
            if hasattr(text, "result"):
                text = text.result()
            if text is ...:
                skipped += 1
                continue
            if text is None or not prefilter(text):
                continue
            hits, capped = line_matches(text.text, matcher, min(MAX_PER_ITEM, MAX_RESULTS - total), literal=not regex)
            if not hits:
                continue
            total += len(hits)
            found += 1
            rel, name = plan[index][0], plan[index][1]
            yield {"path": rel, "name": name, "matches": hits, "capped": capped}
        else:
            if not complete:
                timed_out = truncated = True
    finally:
        for future in pending.values():
            if hasattr(future, "cancel"):
                future.cancel()
        pool.shutdown(wait=False, cancel_futures=True)
    yield {"done": True, "query": str(query), "total": total, "visited": visited, "skipped": skipped,
           "truncated": truncated, "timed_out": timed_out, "backend": backend,
           "elapsed_ms": round((time.monotonic() - started) * 1000)}


def search_files(root: str | Path, query: str, *, case=False, word=False, regex=False,
                 include="", exclude="", time_limit: float = TIME_LIMIT, use_rg: bool | None = None) -> dict:
    """Matching lines of the project files (see ``iter_search_files``)."""
    files: list[dict] = []
    summary: dict = {}
    for item in iter_search_files(root, query, case=case, word=word, regex=regex, include=include,
                                  exclude=exclude, time_limit=time_limit, use_rg=use_rg):
        if item.get("done"):
            summary = item
        else:
            files.append(item)
    return {"query": summary["query"], "files": files, "total": summary["total"], "visited": summary["visited"],
            "skipped": summary["skipped"], "truncated": summary["truncated"], "timed_out": summary["timed_out"],
            "backend": summary["backend"], "elapsed_ms": summary["elapsed_ms"]}


# ----------------------------------------------------------------- nodes
_node_cache: "weakref.WeakKeyDictionary" = weakref.WeakKeyDictionary()
_node_lock = threading.Lock()
TAG_SKIP = ("type:", "status:", "importance:")


def _node_rows(store) -> list[tuple]:
    """``(head, fields, Text(all fields))`` per non-archived node, most
    recently updated first; cached per store until ``store.revision()``
    changes (every write to units, tags and comments bumps it)."""
    revision = store.revision()
    with _node_lock:
        cached = _node_cache.get(store)
        if cached and cached[0] == revision:
            return cached[1]
    with store.lock:
        units = store.con.execute(
            "SELECT id, uid, type, title, body, status FROM units WHERE archived=0 ORDER BY updated_at DESC").fetchall()
        tag_rows = store.con.execute("SELECT unit_id, tag FROM unit_tags").fetchall()
        comment_rows = store.con.execute(
            "SELECT id, unit_id, author, body FROM unit_comments ORDER BY created_at").fetchall()
    tags: dict[int, list[str]] = {}
    for row in tag_rows:
        tags.setdefault(row["unit_id"], []).append(row["tag"])
    comments: dict[int, list] = {}
    for row in comment_rows:
        comments.setdefault(row["unit_id"], []).append(row)
    rows = []
    for unit in units:
        fields = [("title", unit["title"] or "", None), ("body", unit["body"] or "", None)]
        unit_tags = [tag for tag in tags.get(unit["id"], []) if not tag.startswith(TAG_SKIP)]
        if unit_tags:
            fields.append(("tags", " · ".join(unit_tags), None))
        for comment in comments.get(unit["id"], []):
            fields.append(("comment", comment["body"] or "",
                           {"comment_id": comment["id"], "author": comment["author"]}))
        head = {"id": unit["id"], "uid": unit["uid"], "type": unit["type"], "title": unit["title"],
                "status": unit["status"]}
        rows.append((head, fields, Text("\n".join(text for _, text, _ in fields))))
    with _node_lock:
        _node_cache[store] = (revision, rows)
    return rows


def iter_search_nodes(store, query: str, *, case=False, word=False, regex=False,
                      time_limit: float = TIME_LIMIT, cancelled=None):
    """Yield the matching nodes, then a summary dict with ``"done": True``."""
    matcher = build_matcher(query, case=case, word=word, regex=regex)
    prefilter = Prefilter(matcher, query, case=case, regex=regex)
    started = time.monotonic()
    rows = _node_rows(store)
    total = found = 0
    truncated = timed_out = False
    for head, fields, haystack in rows:
        if cancelled and cancelled():
            truncated = True
            break
        if time.monotonic() - started >= time_limit:
            timed_out = truncated = True
            break
        if found >= MAX_ITEMS or total >= MAX_RESULTS:
            truncated = True
            break
        if not prefilter(haystack):
            continue
        lines: list[dict] = []
        budget = min(MAX_PER_ITEM, MAX_RESULTS - total)
        for field, text, extra in fields:
            if budget <= 0 or not text:
                continue
            hits, _ = line_matches(text, matcher, budget, literal=not regex)
            for hit in hits:
                lines.append({"field": field, **hit, **(extra or {})})
            budget -= len(hits)
        if lines:
            total += len(lines)
            found += 1
            yield {**head, "matches": lines}
    yield {"done": True, "query": str(query), "total": total, "truncated": truncated,
           "timed_out": timed_out, "elapsed_ms": round((time.monotonic() - started) * 1000)}


def search_nodes(store, query: str, *, case=False, word=False, regex=False,
                 time_limit: float = TIME_LIMIT) -> dict:
    """Title, body, tags and comments of non-archived nodes."""
    nodes: list[dict] = []
    summary: dict = {}
    for item in iter_search_nodes(store, query, case=case, word=word, regex=regex, time_limit=time_limit):
        if item.get("done"):
            summary = item
        else:
            nodes.append(item)
    return {"query": summary["query"], "nodes": nodes, "total": summary["total"],
            "truncated": summary["truncated"], "timed_out": summary["timed_out"],
            "elapsed_ms": summary["elapsed_ms"]}
