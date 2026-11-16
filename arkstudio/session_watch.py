"""Upload an agent's session JSONL to ARK at an interval (opt-in watcher).

Run it only when you want ARK to keep a copy of an external agent session::

    export ARK_TOKEN=arka_...            # personal agent token (never on argv)
    python -m arkstudio session-watch --id ark-ses-abc123 --server http://127.0.0.1:8890
    python ark-session-watch.py --id ark-ses-abc123 --file ~/.claude/projects/x/y.jsonl

The project must accept session uploads (Execute > Runs > Session context).
The watcher re-reads ``GET /api/agent/v1/sessions/<id>`` for the next byte
offset, sends complete new lines in chunks of at most 1 MB, and re-uploads the
whole file with ``mode=replace`` if the file shrank.  Stop it with Ctrl+C.

Without ``--file`` it looks for the newest JSONL that contains the ARK session
id in the agent's default session folder (Claude Code ``~/.claude/projects``,
Codex ``$CODEX_HOME/sessions`` or ``~/.codex/sessions``, DSH ``~/.dsh/sessions``
when a zstd decoder is available).

This file is standard library only and self-contained: ARK serves it at
``GET /api/agent/v1/sessions/watcher.py`` for machines without ARK installed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable, Iterable

DEFAULT_CHUNK = 1024 * 1024
SEARCH_DAYS = 14
SEARCH_LIMIT = 400
EXIT_DISABLED = 3
EXIT_NOT_FOUND = 4


class UploadError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message


# ------------------------------------------------------------------ discovery
def agent_roots(agent: str = "", home: Path | None = None, env: dict | None = None) -> list[tuple[str, Path]]:
    home = Path(home) if home is not None else Path.home()
    env = os.environ if env is None else env
    roots: list[tuple[str, Path]] = []
    if agent in ("", "claude"):
        base = Path(env.get("CLAUDE_CONFIG_DIR") or home / ".claude")
        roots.append(("claude", base / "projects"))
    if agent in ("", "codex"):
        roots.append(("codex", Path(env.get("CODEX_HOME") or home / ".codex") / "sessions"))
    if agent in ("", "dsh"):
        roots.append(("dsh", home / ".dsh" / "sessions"))
    return roots


def _candidates(root: Path, *, days: int = SEARCH_DAYS) -> list[Path]:
    if not root.is_dir():
        return []
    cutoff = time.time() - days * 86400
    found = []
    for pattern in ("**/*.jsonl", "**/*.jsonl.zstd"):
        for path in root.glob(pattern):
            try:
                if path.is_file() and path.stat().st_mtime >= cutoff:
                    found.append(path)
            except OSError:
                continue
    found.sort(key=lambda p: p.stat().st_mtime, reverse=True)
    return found[:SEARCH_LIMIT]


def _contains(path: Path, needle: bytes) -> bool:
    try:
        data = read_source(path)
    except (OSError, RuntimeError):
        return False
    return needle in data


def find_session_file(session_id: str, agent: str = "", *, home: Path | None = None,
                      env: dict | None = None) -> tuple[str, Path] | None:
    """Newest agent session file that mentions ``session_id``."""
    needle = session_id.encode("utf-8")
    best: tuple[float, str, Path] | None = None
    for name, root in agent_roots(agent, home, env):
        for path in _candidates(root):
            if _contains(path, needle):
                mtime = path.stat().st_mtime
                if best is None or mtime > best[0]:
                    best = (mtime, name, path)
                break  # candidates are newest first per root
    return (best[1], best[2]) if best else None


def read_source(path: Path) -> bytes:
    """Raw bytes of a session file; DSH's ``.jsonl.zstd`` needs a zstd decoder."""
    path = Path(path)
    data = path.read_bytes()
    if path.suffix != ".zstd":
        return data
    try:  # Python 3.14+
        from compression import zstd  # type: ignore
        return zstd.decompress(data)
    except ImportError:
        pass
    try:
        import zstandard  # type: ignore
        return zstandard.ZstdDecompressor().decompressobj().decompress(data)
    except ImportError as exc:
        raise RuntimeError("this session file is zstd-compressed; install the zstandard package "
                           "or use Python 3.14+") from exc


# ------------------------------------------------------------------ transport
class Client:
    def __init__(self, server: str, token: str, project: str = "", *, opener: Callable | None = None):
        self.server = server.rstrip("/")
        self.token = token
        self.project = project
        self.opener = opener or urllib.request.urlopen

    def _request(self, method: str, path: str, body: dict | None = None) -> dict:
        headers = {"Authorization": f"Bearer {self.token}", "Accept": "application/json",
                   "X-Agent-Name": "session-watch"}
        if self.project:
            headers["X-ARK-Project"] = self.project
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.server + path, data=data, method=method, headers=headers)
        try:
            with self.opener(req, timeout=60) as response:
                return json.loads(response.read().decode("utf-8") or "{}")
        except urllib.error.HTTPError as exc:
            try:
                payload = json.loads(exc.read().decode("utf-8") or "{}")
            except ValueError:
                payload = {}
            err = payload.get("error") or {}
            raise UploadError(exc.code, str(err.get("code") or ""), str(err.get("message") or exc.reason)) from exc

    def status(self, session_id: str) -> dict:
        return self._request("GET", f"/api/agent/v1/sessions/{session_id}")

    def upload(self, session_id: str, body: dict) -> dict:
        return self._request("POST", f"/api/agent/v1/sessions/{session_id}/upload", body)


def split_chunks(data: bytes, start: int, limit: int = DEFAULT_CHUNK) -> Iterable[tuple[int, int, bytes]]:
    """Yield ``(offset, source_length, content)`` of complete lines from ``data[start:]``.

    A single line longer than ``limit`` (for example an inline screenshot) is
    replaced by a small ``ark_truncated`` marker; ``source_length`` still covers
    the original bytes so offsets stay aligned with the file.
    """
    end = data.rfind(b"\n") + 1
    offset = start
    while offset < end:
        stop = min(end, offset + limit)
        if stop < end:
            cut = data.rfind(b"\n", offset, stop)
            if cut >= offset:
                stop = cut + 1
            else:
                stop = data.find(b"\n", offset) + 1
                marker = json.dumps({"type": "ark_truncated", "bytes": stop - offset - 1}).encode() + b"\n"
                yield offset, stop - offset, marker
                offset = stop
                continue
        yield offset, stop - offset, data[offset:stop]
        offset = stop


def sync_once(client: Client, session_id: str, path: Path, *, agent: str = "",
              chunk_bytes: int = DEFAULT_CHUNK, meta: dict | None = None,
              log: Callable[[str], None] = lambda _msg: None) -> dict:
    """Upload whatever is new in ``path``.  Returns ``{"sent", "received_bytes"}``."""
    state = client.status(session_id)
    if not state.get("uploads_enabled"):
        raise UploadError(403, "SESSION_UPLOADS_DISABLED", "this ARK project does not accept session uploads")
    limit = min(chunk_bytes, int((state.get("limits") or {}).get("chunk_bytes") or chunk_bytes))
    received = int(state.get("received_bytes") or 0)
    data = read_source(path)
    mode = "append"
    if len(data) < received:
        log(f"{path} shrank ({len(data)} < {received} bytes); re-uploading it")
        received = 0
        mode = "replace"
    sent = 0
    extra = {key: value for key, value in (meta or {}).items() if value}
    if agent:
        extra.setdefault("agent", agent)
    for offset, length, chunk in split_chunks(data, received, limit):
        content = chunk.decode("utf-8", "replace")
        body = {"offset": offset, "length": length, "content": content,
                "sha256": hashlib.sha256(content.encode("utf-8")).hexdigest(),
                "mode": mode, **extra}
        try:
            result = client.upload(session_id, body)
        except UploadError as exc:
            if exc.status == 409:
                log("offset changed on the server; resyncing on the next pass")
                break
            raise
        mode = "append"
        sent += length
        received = int(result.get("received_bytes") or offset + length)
    return {"sent": sent, "received_bytes": received}


def watch(client: Client, session_id: str, path: Path, *, interval: float = 60, agent: str = "",
          once: bool = False, meta: dict | None = None, sleep: Callable[[float], None] = time.sleep,
          log: Callable[[str], None] = print, max_passes: int | None = None) -> int:
    passes = 0
    while True:
        passes += 1
        try:
            result = sync_once(client, session_id, path, agent=agent, meta=meta, log=log)
            if result["sent"]:
                log(f"uploaded {result['sent']} bytes (server has {result['received_bytes']})")
        except UploadError as exc:
            if exc.status in (401, 403):
                log(f"ARK refused the upload ({exc.code or exc.status}): {exc.message}")
                if exc.code == "SESSION_UPLOADS_DISABLED":
                    log("Ask a project admin to turn on session uploads in Execute > Runs > Session context.")
                return EXIT_DISABLED
            log(f"upload failed ({exc.status} {exc.code}): {exc.message}; retrying later")
        except (OSError, RuntimeError) as exc:
            log(f"could not read or send {path}: {exc}; retrying later")
        if once or (max_passes is not None and passes >= max_passes):
            return 0
        sleep(interval)


# ------------------------------------------------------------------------ CLI
def build_parser(parser: argparse.ArgumentParser | None = None) -> argparse.ArgumentParser:
    parser = parser or argparse.ArgumentParser(prog="ark-session-watch", description=__doc__.split("\n")[0])
    parser.add_argument("--id", required=True, help="ARK session id from the prompt (ark-ses-...)")
    parser.add_argument("--file", default="", help="session JSONL file (default: auto-detect by id)")
    parser.add_argument("--agent", default="", choices=["claude", "codex", "dsh", "other"],
                        help="agent that wrote the file (narrows auto-detection)")
    parser.add_argument("--server", default=os.environ.get("ARK_URL") or "http://127.0.0.1:8890",
                        help="ARK base URL (default $ARK_URL or http://127.0.0.1:8890)")
    parser.add_argument("--project", default=os.environ.get("ARK_PROJECT") or "",
                        help="ARK project id sent as X-ARK-Project (default $ARK_PROJECT)")
    parser.add_argument("--token-env", default="ARK_TOKEN",
                        help="environment variable holding the agent token (default ARK_TOKEN)")
    parser.add_argument("--interval", type=float, default=60.0, help="seconds between uploads (min 10)")
    parser.add_argument("--title", default="", help="title shown in ARK")
    parser.add_argument("--once", action="store_true", help="upload once and exit")
    return parser


def run(args: argparse.Namespace, *, env: dict | None = None, opener: Callable | None = None,
        home: Path | None = None, log: Callable[[str], None] = print) -> int:
    env = os.environ if env is None else env
    token = str(env.get(args.token_env) or "").strip()
    if not token:
        log(f"set {args.token_env} to your ARK agent token first (it is never read from the command line)")
        return 2
    session_id = str(args.id).strip().lower()
    agent = args.agent
    if args.file:
        path = Path(os.path.expanduser(args.file))
    else:
        found = find_session_file(session_id, agent, home=home, env=env)
        if not found:
            log(f"no recent session file mentions {session_id}; pass --file")
            return EXIT_NOT_FOUND
        agent, path = agent or found[0], found[1]
        log(f"watching {path} ({agent})")
    client = Client(args.server, token, args.project, opener=opener)
    interval = max(10.0, float(args.interval))
    return watch(client, session_id, path, interval=interval, agent=agent, once=args.once,
                 meta={"title": args.title}, log=log)


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return run(args)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    sys.exit(main())
