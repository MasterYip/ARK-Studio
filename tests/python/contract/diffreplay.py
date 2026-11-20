"""Differential replay: identical request sequences against Python and Go.

    python -B tests/python/contract/diffreplay.py --go ark_studio/server/ark-server.exe [--report out.json]
    python -B tests/python/contract/diffreplay.py --go ... --handoff   # cookie survives a server swap
    python -B tests/python/contract/diffreplay.py --go ... --prod-copy <copy of a projects root>

Both servers start on byte-identical copies of one fixture (fixture.py).  Every
step goes to both; the responses are compared on status, the headers listed in
COMPARED_HEADERS and the body bytes.  Only these normalizations are applied,
each to both sides (they are printed with every report):

N1  Headers not compared: Date, Server, Keep-Alive, Transfer-Encoding, and
    Content-Length when the body is gzip-encoded (compressors differ; the
    decompressed bodies and their lengths are compared instead).
N2  gzip bodies are decompressed; the Content-Encoding header itself is compared.
N3  The absolute data directory of each server (raw and JSON-escaped) -> <DATA>.
N4  ISO timestamps (YYYY-MM-DDTHH:MM:SS+00:00) at or after the replay start
    -> <NOW>.  Fixture timestamps are older and are compared exactly.
N5  The project.json mtime_ns inside /api/revision values -> <MTIME>.
N6  Session cookie and CSRF token values -> <TOKEN> / <CSRF> (random by design).
N7  Values of the timing fields "store_ms", "generated_ms", "elapsed_ms",
    "duration_ms", "latency_ms" and "timing" (elapsed milliseconds in
    /api/project/overview, Workbench Search, the GPU probe's SSH run time,
    the assistant connection test) -> <MS>; likewise the ARK host's live
    disk usage "disk_used_gb" / "disk_pct" in /api/resources/graph (other
    processes write to the same disk between the two requests;
    "disk_total_gb" is compared).
N9  Black-box snapshot db_sha256 / db_size / snapshot_sha256 / snapshot_size
    -> <SNAPSHOT> (statediff.py compares the snapshot database rows).
N10 ZIP bodies are compared as their entry names and contents (each
    .sqlite3 entry row by row like statediff.py); compression, entry
    timestamps and the Content-Length of the ZIP are not compared.
N11 Each server's own port number (and port + 1, the suggested tunnel port)
    -> <PORT> / <PORT+1>; a qr_svg equal to arkstudio.qr.to_svg(pairing_url)
    of the same response -> <QR:ok> (the URL inside carries the port).
N14 Provider catalog (/api/providers...): "probe_ms" and "cache_age_seconds"
    -> <MS> (elapsed time; the cache age is whole seconds since the previous
    probe), and the "command" and "version" of the "echo" (local demo)
    provider -> <ECHO>: Python runs its demo agent with its own interpreter
    (version "Python 3.x"), the Go server with its own binary.  The
    Content-Length of HEAD /api/providers is therefore not compared.
N15 The data directory with forward slashes (Git prints paths that way on
    Windows) -> <DATA>, like N3.
N16 Agent sessions (/api/agent-sessions..., /api/task-agent/v1/session,
    /api/session-contexts...) and the settings replies that embed the
    effective task-agent providers (/api/personal/settings,
    /api/settings/effective, /api/assistant/config...):
    integer "pid" values -> <PID>, and in any object whose "id" or
    "platform" is "echo" the "program", "command", "version", "provider_version" and
    "command_preview" -> <ECHO> (as N14: the demo agent is Python's
    interpreter + demo_agent.py vs ark-server demo-agent).  The bodies are
    compared re-encoded after this masking; the Content-Length of HEAD
    replies is therefore not compared.
N17 Replies to POST .../input, .../steer, .../continue and .../approvals/<id>
    on a running session (and POST /api/session-contexts/<id>/continue, which
    starts one): the embedded session's "counts", "approvals" and
    "native_session_id" -> <LIVE> (they show how much of the agent's answer
    the output readers had processed when the reply was built, a race in
    both servers).  WAIT_SESSION steps then wait for the answer and the
    following GETs compare those fields exactly.
N8  Writes are stamped by the request header X-Replay-Clock (start + step
    number seconds; replayclock.py patches Python's db.utc_now/today, the Go
    server honours it under ARK_REPLAY_CLOCK=1), so equal-second ordering
    cannot differ between the two servers.  The stamps are >= the replay
    start and are masked by N4 like real times.
N12 Editor workspace: "mtime_ns" values at or after the replay start ->
    <MTIME_NS> (files written during the replay; their ISO mtime is N4), and
    non-empty "pty_reason" / "pty_hint" / "pty_fallback" texts -> <PTY> (the
    PTY backend is platform specific: pywinpty for Python on Windows, none
    for Go on Windows, posix for both on Linux; "pty_available" is compared).
N13 Terminal output (/api/workspace/terminals...): how a child's output is
    split into entries depends on pipe timing, so consecutive entries of one
    stream are merged and "seq" / "offset" -> <SEQ>; an SSE body is compared
    as its merged output, the sequence of distinct "state" statuses and the
    final "done" event.  The texts, streams, statuses, exit codes, byte and
    line counts are compared exactly.  WAIT_TERMINAL steps poll both servers
    until the terminal has finished (they are not compared).
N18 A 204 response: Python sends "Content-Length: 0", net/http never sends a
    Content-Length with 204 (RFC 9110 section 8.6); the length is not
    compared for 204 (both bodies are empty and compared).
N19 Validators (client cache): a body ETag equal to W/"blake2b-96 of that
    reply's own decompressed body" -> W/"<BODY>" (bodies are compared, and
    N3-N7 change bytes legitimately); the project overview's ETag equal to
    the hash of its own sorted body without the timing fields -> W/"<OVERVIEW>";
    revision-keyed ETags W/"r..." -> W/"r<REV>" (they hash the project
    revision, which carries the project.json mtime, N5; their behaviour is
    compared through the conditional steps' 304/200 answers); an ETag of a
    304 or HEAD reply equal to the one this server sent last for the same
    path -> <ETAG:same>; media ETags "f<mtime_ns>-<size>" and Last-Modified
    of files written during the replay -> <MTIME_NS> / <NOW>.  In a
    /api/unit-details body each etags[id] equal to the hash of
    {"ok": true, "unit": units[i]} -> W/"<BODY>".  If-None-Match values
    {etag}, {etag-strong} and {detail-etag:ID} are filled per server from
    its own earlier replies.
N20 GET /api/events (SSE): read as frames (initial frames, then the steps
    sent during the stream, then the frames they cause); the stream is closed
    by the replay.  Frames are compared after N3-N5; keep-alive comments
    (": keep-alive", every 20 s) are dropped.

Local mode downgrades the fixture's project database to schema v10 (drops
change_log, its triggers and meta.change_log_id, and the v12 column
topic_members.w), so both servers migrate it to v12 at startup; statediff
compares the result.

N21 Route probe (replay_probe.py) HEAD steps: each server's HEAD must
    match its own GET of the same URL (status, ETag, Content-Type,
    Content-Encoding, Content-Length = GET body length unless the body has
    N7 timing fields, empty body; a pair
    whose ETags or lengths differ is resent up to 3 times, for bodies with a wall-clock
    field such as worktrees generated_at or the overview timing); the two
    GET replies are compared with N1-N20.  P1: a Go reply 501 NOT_PORTED is
    a mismatch in every module; probe steps are never skipped.

N22 Intentional Go-only additions (go_only.py): on a route in
    go_only.AUGMENTED (/api/agent/v1/interface: the docs.* capabilities and
    the "docs" member, Go only since the switch-over) the extra members are
    removed from the Go body before the byte comparison; the body-derived
    ETag and Content-Length are not compared for that route.  Go-only
    routes (go_only.ROUTES) have no Python counterpart and are not replayed.
    Intentional Go-only behaviour fixes (go_only.FIXES, e.g.
    resources-graph-nonfinite-gpu-memory): a step that mismatches and whose
    reply pair matches a fix's narrow predicate is counted and listed as an
    *intentional divergence* (``intentional=`` in the summary), not as a
    mismatch.  Python is frozen; the Go behaviour of a fix is covered by Go
    unit tests instead.

Steps whose route is not in ``ark-server routes`` are skipped and counted.
After the replay both servers are stopped and their data dirs are compared
(databases and files, see statediff.py for the S1-S6 normalizations).
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import quote

sys.path.insert(0, str(Path(__file__).resolve().parent))
from replay_views import agent_steps, fixture_ids, module_of, view_steps  # noqa: E402
import replay_agents  # noqa: E402
import replay_sessions  # noqa: E402
import replay_sessionctx  # noqa: E402
import replay_settings  # noqa: E402
import replay_skills  # noqa: E402
import replay_probe  # noqa: E402
import go_only  # noqa: E402
import replay_writes  # noqa: E402
import replay_services  # noqa: E402
import replay_main_sync  # noqa: E402
from statediff import diff as state_diff, dump as state_dump  # noqa: E402
from serverctl import ASSET_VERSION, Client, Server, copy_fixture, make_fixture, temp_dir  # noqa: E402

# Services modules (replay_services.py); its quick mode empties this hook.
replay_services_steps = replay_services.steps
RELOGIN = "__relogin__"
COMPARED_HEADERS = ("content-type", "cache-control", "content-security-policy",
                    "x-frame-options", "x-content-type-options", "referrer-policy",
                    "content-disposition", "location", "vary", "content-encoding",
                    "set-cookie", "retry-after", "accept-ranges", "connection",
                    "x-robots-tag", "content-range", "etag", "x-ark-project-id", "last-modified",
                    "x-accel-buffering")
ISO = re.compile(rb"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+00:00")
REVISION_MTIME = re.compile(rb'("revision": "[^"]*?):(\d{15,20}):')
COOKIE = re.compile(r"(ark_session=)[^;]+")
CSRF = re.compile(rb"csrf\.[A-Za-z0-9_-]+\.\d+\.[A-Za-z0-9_-]+")
# N9: black-box snapshot hashes and sizes (byte layout of the copied database
# and gzip framing differ; statediff.py compares the snapshot rows instead).
SNAPSHOT_BYTES = re.compile(rb'("(?:db_sha256|snapshot_sha256|db_size|snapshot_size)": )("[0-9a-f]+"|\d+)')
MTIME_NS = re.compile(rb'("mtime_ns": )(\d+)')
PTY_TEXT = re.compile(rb'("pty_(?:reason|hint|fallback)": )"(?:[^"\\]|\\.)+"')
TIMING = re.compile(rb'("(?:store_ms|generated_ms|elapsed_ms|duration_ms|latency_ms|probe_ms|cache_age_seconds|disk_used_gb|disk_pct)": )-?[0-9.e+-]+'
                    rb'|("timing": )\{[^{}]*\}')
BODY_ETAG = re.compile(r'W/"[0-9a-f]{24}"')
FILE_ETAG = re.compile(r'"f([0-9a-f]+)-([0-9a-f]+)"')


def blake_etag(data: bytes) -> str:
    import hashlib
    return 'W/"' + hashlib.blake2b(data, digest_size=12).hexdigest() + '"'


def etag_canonical(resp, value: str, norm: "Normalizer", sent: str | None) -> str:
    """N19 for one ETag header value."""
    if value.startswith('W/"r'):
        return 'W/"r<REV>"'
    m = FILE_ETAG.fullmatch(value)
    if m:
        mtime = "<MTIME_NS>" if int(m.group(1), 16) >= norm.start_ns else m.group(1)
        return f'"f{mtime}-{m.group(2)}"'
    if sent is not None and value == sent and (resp.status == 304 or not resp.body):
        return "<ETAG:same>"
    if BODY_ETAG.fullmatch(value) and resp.body:
        if value == blake_etag(resp.body):
            return 'W/"<BODY>"'
        try:
            data = json.loads(resp.body)
        except ValueError:
            return value
        if isinstance(data, dict):
            data = {k: v for k, v in data.items() if k not in ("ok", "timing", "generated_ms", "store_ms")}
            if value == blake_etag(json.dumps(data, sort_keys=True, ensure_ascii=False).encode()):
                return 'W/"<OVERVIEW>"'
    return value


def details_canonical(data: bytes) -> bytes:
    """N19: etags of a /api/unit-details body that match their unit."""
    try:
        payload = json.loads(data)
    except ValueError:
        return data
    if not isinstance(payload, dict) or not isinstance(payload.get("etags"), dict):
        return data
    units = {str(u.get("id")): u for u in payload.get("units") or [] if isinstance(u, dict)}
    for key, value in payload["etags"].items():
        unit = units.get(key)
        if unit is not None and value == blake_etag(json.dumps({"ok": True, "unit": unit},
                                                               ensure_ascii=False).encode()):
            payload["etags"][key] = 'W/"<BODY>"'
    return json.dumps(payload, ensure_ascii=False).encode()


def last_modified_canonical(value: str, norm: "Normalizer") -> str:
    from email.utils import parsedate_to_datetime
    try:
        when = parsedate_to_datetime(value).timestamp()
    except (TypeError, ValueError):
        return value
    return "<NOW>" if when >= norm.start_ns / 10 ** 9 else value


class Normalizer:
    def __init__(self, data_dir: Path, start: str, port: int = 0):
        raw = str(data_dir)
        self.port = int(port or 0)
        self.data_dir, self.start_text = Path(data_dir), start
        self.paths = sorted({raw.encode(), json.dumps(raw)[1:-1].encode(), raw.replace("\\", "/").encode(),
                             json.dumps(json.dumps(raw)[1:-1])[1:-1].encode()},
                            key=len, reverse=True)
        self.start = start.encode()
        self.start_ns = int(datetime.fromisoformat(start).timestamp()) * 10 ** 9

    def body(self, data: bytes) -> bytes:
        for path in self.paths:
            data = data.replace(path, b"<DATA>")
        if b'"qr_svg"' in data:
            data = qr_checked(data)
        if self.port:
            # N11: each server's own listening port (and the tunnel port, port + 1).
            for value, label in ((self.port, b"<PORT>"), (self.port + 1, b"<PORT+1>")):
                # bounded by any alphanumeric: a hex hash may contain the port's digits
                data = re.sub(rb"(?<![0-9A-Za-z])" + str(value).encode() + rb"(?![0-9A-Za-z])", label, data)
        data = ISO.sub(lambda m: b"<NOW>" if m.group(0) >= self.start else m.group(0), data)
        data = REVISION_MTIME.sub(rb"\1:<MTIME>:", data)
        data = TIMING.sub(lambda m: (m.group(1) or m.group(2)) + b"<MS>", data)
        data = SNAPSHOT_BYTES.sub(rb"\1<SNAPSHOT>", data)
        data = MTIME_NS.sub(lambda m: m.group(1) + b"<MTIME_NS>" if int(m.group(2)) >= self.start_ns
                            else m.group(0), data)
        data = PTY_TEXT.sub(rb'\1"<PTY>"', data)
        return CSRF.sub(b"<CSRF>", data)

    def header(self, name: str, value: str) -> str:
        if name == "set-cookie":
            value = COOKIE.sub(r"\1<TOKEN>", value)
        if name == "location":
            value = self.body(value.encode()).decode()
        return value


def qr_checked(data: bytes) -> bytes:
    """N11: a qr_svg equal to arkstudio.qr.to_svg(pairing_url) becomes <QR:ok>."""
    try:
        payload = json.loads(data)
    except ValueError:
        return data
    if not isinstance(payload, dict) or not isinstance(payload.get("qr_svg"), str):
        return data
    from serverctl import REPO
    if str(REPO) not in sys.path:
        sys.path.insert(0, str(REPO))
        sys.path.insert(0, str(REPO / "ark_studio" / "python"))
    try:
        from arkstudio import qr
    except ImportError:  # Go-only runs (golden.py): internal/qr has its own CPython vectors
        label = b'"<QR:svg>"' if payload["qr_svg"].startswith("<svg") else b'"<QR:bad>"'
    else:
        ok = payload["qr_svg"] == qr.to_svg(str(payload.get("pairing_url") or ""))
        label = b'"<QR:ok>"' if ok else b'"<QR:bad>"'
    return data.replace(json.dumps(payload["qr_svg"], ensure_ascii=False).encode(), label)


def compare(step, a, b, na: Normalizer, nb: Normalizer, sent: tuple = (None, None)) -> list[str]:
    # N22: intentional Go-only payload members (go_only.AUGMENTED) are removed
    # from the Go body; its body-derived ETag and Content-Length then differ.
    go_extra = go_only.augmented(step[2]) if b.status == 200 else None
    return combine(step, canonical(step, a, na, sent[0]), canonical(step, b, nb, sent[1], go_extra),
                   bool(go_extra))


def canonical(step, r, n: Normalizer, sent=None, go_extra=None) -> dict:
    """One reply in the form compare() checks: status, the COMPARED_HEADERS
    after N1-N21, the normalized body, and what the Content-Length rule needs.
    golden.py stores this form (JSON-safe via canonical_json)."""
    headers = {}
    for name in COMPARED_HEADERS:
        values = [n.header(name, v) for v in r.all(name)]
        if name == "etag":
            values = [etag_canonical(r, v, n, sent) for v in values]
        elif name == "last-modified":
            values = [last_modified_canonical(v, n) for v in values]
        headers[name] = values
    if r.header("content-type") == "application/zip":
        body = zip_canonical(r.body, n)
    elif step[2].startswith("/api/providers"):
        body = n.body(provider_canonical(r.body))
    elif step[2].startswith(SESSION_LIKE):
        body = n.body(session_canonical(r.body, step))
    elif "/api/workspace/terminals" in step[2]:
        body = n.body(terminal_canonical(r.body))
    elif step[2].startswith("/api/unit-details"):
        body = n.body(details_canonical(r.body))
    elif go_extra:
        body = n.body(go_extra(r.body) if r.body else r.body)
    else:
        body = n.body(r.body)
    return {"status": r.status, "headers": headers, "body": body,
            "untouched": body == r.body and not go_extra,
            "content_encoding": r.header("content-encoding"), "content_length": r.header("content-length"),
            "not_ported": replay_probe.is_not_ported(r.status, r.body)}


def combine(step, a: dict, b: dict, go_extra: bool = False) -> list[str]:
    """The problems between two canonical replies (a: reference, b: Go)."""
    problems = []
    if b["not_ported"]:  # P1: never acceptable, in any module
        problems.append("Go answered 501 NOT_PORTED")
    if a["status"] != b["status"]:
        problems.append(f"status {a['status']} != {b['status']}")
    for name in COMPARED_HEADERS:
        va, vb = a["headers"].get(name, []), b["headers"].get(name, [])
        if name == "etag" and go_extra:
            va, vb = ["<GO_ONLY>"] * len(va), ["<GO_ONLY>"] * len(vb)
        if va != vb:
            problems.append(f"header {name}: {va!r} != {vb!r}")
    ba, bb = a["body"], b["body"]
    untouched = a["untouched"] and b["untouched"]  # N3-N7 change lengths legitimately
    if step[1] == "HEAD" and step[2].startswith(("/api/providers",) + SESSION_LIKE):
        untouched = False  # N14: the HEAD reply's length counts the echo path and probe_ms
    if step[1] == "HEAD" and step[2].startswith(HEAD_TIMING_PATHS):
        untouched = False  # N7: the GET body has timing fields whose digit count varies
    if a["content_encoding"] != "gzip" and a["content_length"] != b["content_length"]             and not problems and untouched and not a["status"] == b["status"] == 204:
        problems.append(f"content-length {a['content_length']} != {b['content_length']}")
    if ba != bb:
        at = next((i for i in range(min(len(ba), len(bb))) if ba[i] != bb[i]), min(len(ba), len(bb)))
        problems.append(f"body differs at byte {at} (len {len(ba)} vs {len(bb)}): "
                        f"py={ba[max(0, at - 60):at + 80]!r} go={bb[max(0, at - 60):at + 80]!r}")
    return problems


def canonical_json(form: dict) -> dict:
    """canonical() with the body as text (UTF-8, surrogate-escaped bytes)."""
    return dict(form, body=form["body"].decode("utf-8", "surrogateescape"))


def canonical_load(form: dict) -> dict:
    return dict(form, body=form["body"].encode("utf-8", "surrogateescape"))


def provider_canonical(data: bytes) -> bytes:
    """N14: the echo provider's interpreter path and version line -> <ECHO>."""
    try:
        payload = json.loads(data)
    except ValueError:
        return data
    for item in payload.get("providers") or [] if isinstance(payload, dict) else []:
        if isinstance(item, dict) and item.get("id") == "echo":
            item["command"] = item["version"] = "<ECHO>"
    return json.dumps(payload, ensure_ascii=False).encode()


# N16 paths: agent sessions, and the settings replies that embed the
# effective task-agent providers (whose echo entry carries the program).
SESSION_LIKE = ("/api/agent-sessions", "/api/task-agent/v1/session", "/api/session-contexts",
                "/api/personal/settings", "/api/settings/effective", "/api/assistant/config")


def _session_mask(value):
    if isinstance(value, list):
        return [_session_mask(item) for item in value]
    if not isinstance(value, dict):
        return value
    out = {key: _session_mask(item) for key, item in value.items()}
    if isinstance(out.get("pid"), int):
        out["pid"] = "<PID>"
    if out.get("id") == "echo" or out.get("platform") == "echo":
        for key in ("program", "command", "version", "command_preview", "provider_version"):
            if key in out:
                out[key] = "<ECHO>"
        if isinstance(out.get("provider"), dict):
            for key in ("command", "version"):
                if key in out["provider"]:
                    out["provider"][key] = "<ECHO>"
    return out


LIVE_SESSION_POST = re.compile(r"^/api/agent-sessions/[^/]+/(?:input|steer|continue|approvals/[^/]+)$"
                               r"|^/api/session-contexts/[^/]+/continue$")


def session_canonical(data: bytes, step=None) -> bytes:
    """N16: agent-session process ids -> <PID>; the echo preset's program
    (Python's interpreter + demo_agent.py vs ark-server demo-agent) -> <ECHO>."""
    try:
        payload = json.loads(data)
    except ValueError:
        return data
    payload = _session_mask(payload)
    if step is not None and step[1] == "POST" and LIVE_SESSION_POST.match(step[2]) \
            and isinstance(payload, dict) and isinstance(payload.get("session"), dict):
        # N17: a reply about a running session embeds what its readers have
        # seen so far; the next GET after a WAIT step compares it exactly.
        for key in ("counts", "approvals", "native_session_id"):
            if key in payload["session"]:
                payload["session"][key] = "<LIVE>"
    return json.dumps(payload, ensure_ascii=False).encode()


def _merge_terminal(value):
    """N13: merge consecutive entries of one stream; seq/offset -> <SEQ>."""
    if isinstance(value, list):
        return [_merge_terminal(item) for item in value]
    if not isinstance(value, dict):
        return value
    out = {key: _merge_terminal(item) for key, item in value.items()}
    entries = out.get("entries")
    if isinstance(entries, list) and all(isinstance(e, dict) and "stream" in e for e in entries):
        merged: list = []
        for e in entries:
            if merged and merged[-1]["stream"] == e["stream"]:
                merged[-1]["text"] += e.get("text", "")
            else:
                merged.append({"seq": "<SEQ>", "stream": e["stream"], "text": e.get("text", ""),
                               "t": e.get("t")})
        out["entries"] = merged
    if "offset" in out and "entries" in out:
        out["offset"] = "<SEQ>"
    return out


def terminal_canonical(data: bytes) -> bytes:
    """N13 for terminal JSON replies and SSE streams."""
    text = data.decode("utf-8", "replace")
    if text.startswith("event:") or text.startswith(":"):
        output: list = []
        statuses: list = []
        done = None
        for frame in text.split("\n\n"):
            event, payload = "", None
            for line in frame.split("\n"):
                if line.startswith("event: "):
                    event = line[7:]
                elif line.startswith("data: "):
                    payload = json.loads(line[6:])
            if event == "output" and isinstance(payload, dict):
                if output and output[-1]["stream"] == payload.get("stream"):
                    output[-1]["text"] += payload.get("text", "")
                else:
                    output.append({"stream": payload.get("stream"), "text": payload.get("text", "")})
            elif event == "state" and isinstance(payload, dict):
                if not statuses or statuses[-1] != payload.get("status"):
                    statuses.append(payload.get("status"))
            elif event == "done":
                done = _merge_terminal(payload)
        return json.dumps({"sse_output": output, "sse_statuses": statuses, "sse_done": done},
                          ensure_ascii=False).encode()
    try:
        payload = json.loads(data)
    except ValueError:
        return data
    return json.dumps(_merge_terminal(payload), ensure_ascii=False).encode()


def wait_terminal(clients, path: str) -> None:
    """WAIT_TERMINAL: poll each server until the terminal has finished."""
    for client in clients:
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            reply = client.get(f"/api/workspace/terminals/{path}")
            try:
                status = reply.json().get("terminal", {}).get("status")
            except ValueError:
                status = None
            if status not in ("running", "stopping"):
                break
            time.sleep(0.1)


def zip_canonical(data: bytes, norm: "Normalizer") -> bytes:
    """N10: a ZIP as its entry names and contents (databases row by row)."""
    import io
    import tempfile
    import zipfile
    from statediff import StateNormalizer, _dump_sqlite
    try:
        archive = zipfile.ZipFile(io.BytesIO(data))
    except zipfile.BadZipFile:
        return b"<not a zip> " + data
    out = []
    with archive, tempfile.TemporaryDirectory() as scratch:
        for info in archive.infolist():
            content = archive.read(info)
            if info.filename.endswith(".sqlite3"):
                raw = Path(scratch) / "entry.sqlite3"
                raw.write_bytes(content)
                state = StateNormalizer(norm.data_dir, norm.start_text)
                content = json.dumps(_dump_sqlite(raw, state), ensure_ascii=False, sort_keys=True).encode()
                raw.unlink()
            out.append(info.filename.encode() + b"\n" + norm.body(content))
    return b"\n--\n".join(out)


def unit_refs(client: Client) -> list[dict]:
    return client.get("/api/units?limit=1000&sort=created&facets=0").json()["items"]


def read_steps(units: list[dict]) -> list[tuple]:
    """(route id, method, path, body, headers)."""
    g = {"Accept-Encoding": "gzip"}
    steps = [
        ("exact:/api/health", "GET", "/api/health", None, {}),
        ("exact:/api/health", "HEAD", "/api/health", None, {}),
        ("exact:/api/nope", "GET", "/api/nope", None, {}),
        ("static:/*", "GET", "/", None, {}),
        ("static:/*", "GET", "/", None, g),
        ("static:/*", "GET", "/index.html", None, g),
        ("static:/*", "GET", "/deep/client/route", None, {}),
        ("static:/*", "GET", f"/js/graph.js?v={ASSET_VERSION}", None, g),
        ("static:/*", "GET", "/js/graph.js", None, {}),
        ("static:/*", "GET", f"/css/app.css?v={ASSET_VERSION}", None, g),
        ("static:/*", "GET", "/sw.js", None, {}),
        ("static:/*", "GET", "/viz-frame.html", None, {}),
        ("static:/*", "GET", "/favicon.svg", None, {}),
        ("static:/*", "GET", "/manifest.webmanifest", None, {}),
        ("static:/*", "GET", "/brand/icon-180.png", None, {}),
        ("static:/*", "HEAD", "/brand/icon-180.png", None, {}),
        ("static:/*", "GET", "/login.html", None, {}),
        ("static:/*", "GET", "/missing.png", None, {}),
        ("static:/*", "GET", "/%2e%2e/README.md", None, {}),
        ("exact:/login", "GET", "/login", None, {}),
        ("exact:/api/auth/session", "GET", "/api/auth/session", None, {}),
        ("exact:/api/graph/snapshot", "GET", "/api/graph/snapshot", None, {}),
        ("exact:/api/graph/snapshot", "GET", "/api/graph/snapshot", None, g),
        ("exact:/api/revision", "GET", "/api/revision", None, {}),
        ("exact:/api/project", "GET", "/api/project", None, {}),
        ("exact:/api/timeline", "GET", "/api/timeline", None, {}),
        ("exact:/api/timeline", "GET", "/api/timeline?from=2026-01-01&to=2026-12-31", None, {}),
    ]
    for limit in ("1", "3", "40", "400", "abc", "0", "999"):
        steps.append(("exact:/api/activity", "GET", f"/api/activity?limit={limit}", None, {}))
    queries = [
        "", "slim=1", "facets=0", "limit=5&offset=3", "limit=0", "limit=x&offset=-4",
        "sort=title", "sort=created", "sort=start", "sort=end", "sort=importance", "sort=type",
        "sort=bogus", "q=" + quote("中文"), "q=claim", "q=%25", "q=_", "types=task,idea",
        "type=claim&origin=manual", "origins=seed", "statuses=review", "status=done&status=open",
        "importance=p0,p1", "tags=" + quote("area:模型"), "tags_unset=area", "tag_unset=stage",
        "tags=status:blocked&tags_unset=area", "roots=1", "roots=true&slim=yes", "from=2026-01-01",
        "to=2026-02-01", "from=2026-01-01&to=2026-03-31", "overdue=1", "updated_since=2000-01-01",
        "created_since=2999-01-01", "assignee=me", "assignee=none", "assignee=local",
        "assignees=LOCAL,unassigned", "ids=1,2,3", "ids=x,4", "types_none=1", "slim=1&sort=title&limit=7",
    ]
    for query in queries:
        steps.append(("exact:/api/units", "GET", "/api/units" + ("?" + query if query else ""), None, g))
    for query in ("", "context=1", "context=1&limit=10", "limit=5", "q=task&context=true",
                  "tags=" + quote("area:模型") + "&context=1", "limit=99999"):
        steps.append(("exact:/api/graph", "GET", "/api/graph" + ("?" + query if query else ""), None, g))
    for unit in units:
        steps.append((r"regex:^/api/units/([^/]+)$", "GET", f"/api/units/{unit['uid']}", None, g))
        steps.append((r"regex:^/api/units/([^/]+)/comments$", "GET",
                      f"/api/units/{unit['id']}/comments", None, {}))
    if units:
        steps.append((r"regex:^/api/units/([^/]+)$", "GET", f"/api/units/{units[0]['id']}", None, {}))
        steps.append((r"regex:^/api/units/([^/]+)$", "HEAD", f"/api/units/{units[0]['uid']}", None, g))
    for bad in ("NOPE-1", quote("中文"), "abc%0A", "%EF%BC%91", "99999", "1%2F2"):
        steps.append((r"regex:^/api/units/([^/]+)$", "GET", f"/api/units/{bad}", None, {}))
        steps.append((r"regex:^/api/units/([^/]+)/comments$", "GET", f"/api/units/{bad}/comments", None, {}))
    steps += edge_steps(units)
    return steps


def edge_steps(units: list[dict]) -> list[tuple]:
    """Request-parsing traps: params, double slashes, odd queries, headers."""
    uid = units[0]["uid"] if units else "X-1"
    unit, comments = r"regex:^/api/units/([^/]+)$", r"regex:^/api/units/([^/]+)/comments$"
    return [
        ("exact:/api/units", "GET", "/api/units;params?limit=2", None, {}),
        ("exact:/api/units", "GET", "/api/units?limit=2&&sort=title&=x&lonely", None, {}),
        ("exact:/api/units", "GET", "/api/units?q=a+b&limit=3", None, {}),
        ("exact:/api/units", "GET", "/api/units?q=%E4%B8%AD%E6%96%87&limit=3&slim=1", None, {}),
        ("exact:/api/units", "GET", "/api/units?q=%ZZ&limit=1", None, {}),
        ("exact:/api/units", "GET", "/api/units?limit=%EF%BC%93", None, {}),
        ("exact:/api/units", "GET", "/api/units?ids=%C2%B2", None, {}),
        ("exact:/api/units", "GET", "/api/units?limit=1#fragment", None, {}),
        ("exact:/api/units", "GET", "/api/units?limit=2", None, {"Accept-Encoding": "gzip;q=0"}),
        ("exact:/api/units", "GET", "/api/units?limit=2", None, {"Accept-Encoding": "br, *;q=0.5"}),
        (unit, "GET", f"/api/units/{uid}%0A", None, {}),
        (unit, "GET", f"/api/units/{uid};x", None, {}),
        (unit, "GET", "/api/units/%EF%BC%91", None, {}),
        (unit, "GET", "/api/units/%C2%B2", None, {}),
        (comments, "GET", f"/api/units/{uid}/comments/", None, {}),
        ("exact:/api/health", "GET", "/api/health?x=1", None, {"Host": "localhost:{port}"}),
        ("exact:/api/health", "GET", "/api/health", None, {"Host": "evil.example"}),
        ("exact:/api/health", "GET", "/api/health", None, {"Host": "contract.example"}),
        ("exact:/api/revision", "GET", "/api/revision", None, {"Host": "contract.example"}),
        ("static:/*", "GET", "/js/../index.html", None, {}),
        ("static:/*", "GET", "/./css/app.css", None, {}),
        ("static:/*", "GET", "/brand/", None, {}),
        ("static:/*", "GET", "/brand", None, {}),
        ("static:/*", "GET", "/?v=contract1", None, {}),
        ("exact:/login", "GET", "/login?next=/x", None, {}),
        ("exact:/logout", "GET", "/logout", None, {}),
        ("exact:/api/auth/session", "GET", "/api/auth/session", None, {"Cookie": "ark_session=v1.bad.1.sig"}),
        ("exact:/api/nope", "PUT", "/api/units", b"{}", {}),
        ("exact:/api/nope", "OPTIONS", "/api/units", None, {}),
        ("exact:/api/nope", "DELETE", "/api/nope", None, {"Origin": "http://evil.example"}),
        ("exact:/api/nope", "PATCH", "/api/nope", "not json", {}),
        ("exact:/api/nope", "POST", "/api/nope", "{}", {}),
        ("exact:/api/nope", "POST", "/api/nope", "{", {}),
    ]


def write_steps(units: list[dict]) -> list[tuple]:
    comment = r"regex:^/api/units/([^/]+)/comments$"
    resolve = r"regex:^/api/units/([^/]+)/comments/(\d+)/resolve$"
    steps = []
    targets = [u for u in units if not u.get("archived")][:6]
    for i, unit in enumerate(targets):
        path = f"/api/units/{unit['uid']}/comments"
        steps += [
            (comment, "POST", path, {"body": f"replay comment {i} 中文 😀 <b>&"}, {}),
            (comment, "POST", path, {"body": "reply", "reply_to": 1}, {}),
            (comment, "POST", path, {"body": ""}, {}),
            (comment, "POST", path, {"body": "x", "kind": "decided"}, {}),
            (comment, "POST", path, {"body": "x", "kind": "nonsense"}, {}),
            (comment, "POST", path, {"body": "x" * 8001}, {}),
            (comment, "POST", path, {"body": "agent", "kind": "agent_reply"}, {}),
            (comment, "POST", path, {"body": "x"}, {"Origin": "http://evil.example"}),
            (comment, "POST", path, "[1,2]", {}),
            (resolve, "POST", f"{path}/{i + 1}/resolve", {"resolved": True}, {}),
            (resolve, "POST", f"{path}/{i + 1}/resolve", {"resolved": False}, {}),
            (resolve, "POST", f"{path}/99999/resolve", {}, {}),
            ("exact:/api/revision", "GET", "/api/revision", None, {}),
            (r"regex:^/api/units/([^/]+)$", "GET", f"/api/units/{unit['uid']}", None, {}),
        ]
    steps.append((comment, "POST", "/api/units/NOPE-1/comments", {"body": "x"}, {}))
    steps.append(("exact:/api/activity", "GET", "/api/activity?limit=60", None, {}))
    steps.append(("exact:/api/project", "GET", "/api/project", None, {}))
    return steps


def _rmtree(path: Path) -> None:
    """Remove a replay work dir, including read-only Git objects on Windows."""
    import os
    import stat

    def retry(func, target, _exc):
        try:
            os.chmod(target, stat.S_IWRITE)
            func(target)
        except OSError:
            pass
    shutil.rmtree(path, onerror=retry)


def go_ported(go: str) -> set[str]:
    if go == "python":  # self-check: Python vs Python must show zero mismatches
        contract = Path(__file__).resolve().parents[3] / "docs" / "development" / "go-port" / "contract.json"
        return {r["id"] for r in json.loads(contract.read_text(encoding="utf-8"))["routes"]} | {"exact:/api/nope"}
    out = subprocess.run([go, "routes", "--json"], capture_output=True, text=True, check=True)
    return {item["id"] for item in json.loads(out.stdout)}


def resolve(step, vars_: dict) -> tuple:
    """Fill @unit@-style placeholders (replay_writes.py) and drop replay-only headers."""
    route, method, path, body, headers = step
    path = replay_writes.placeholders(vars_, path)
    if isinstance(body, (dict, list)):
        body = json.loads(replay_writes.placeholders(vars_, json.dumps(body, ensure_ascii=False)))
    elif isinstance(body, str):
        body = replay_writes.placeholders(vars_, body)
    headers = {k: replay_writes.placeholders(vars_, v) if isinstance(v, str) else v
               for k, v in headers.items() if k != "X-Replay-Module"}
    return route, method, path, body, headers


DATA_TOKEN = "@DATA_DIR@"


def per_server(step, data_dir: Path) -> tuple:
    """@DATA_DIR@ in a step's path, body or headers -> this server's own data
    dir (e.g. setup discovery paths inside each copy); N3 masks it again."""
    route, method, path, body, headers = step
    raw = str(data_dir)
    if isinstance(body, (dict, list)):
        text = json.dumps(body, ensure_ascii=False)
        if DATA_TOKEN in text:
            body = json.loads(text.replace(DATA_TOKEN, json.dumps(raw)[1:-1]))
    elif isinstance(body, str):
        body = body.replace(DATA_TOKEN, raw)
    headers = {k: v.replace(DATA_TOKEN, raw) if isinstance(v, str) else v for k, v in headers.items()}
    return route, method, path.replace(DATA_TOKEN, raw), body, headers


# Signed-in replays: credentials for X-Replay-As (set by replay()).
REPLAY_AS: dict = {}


def _as_client(client: Client, who: str) -> Client:
    """X-Replay-As: "N" = fixture user N (one session per server, reused);
    "freshN" / "rememberN" = a new sign-in (remember-me) for this step."""
    if not REPLAY_AS.get("users"):
        return client
    fresh = who.startswith(("fresh", "remember"))
    index = int(who.lstrip("freshmb") or 0)
    user = REPLAY_AS["users"][index]
    cache = client.__dict__.setdefault("_as", {})
    if fresh or user not in cache:
        other = Client(client.server)
        other.login(user, REPLAY_AS["password"], remember=who.startswith("remember"))
        if fresh:
            return other
        cache[user] = other
    return cache[user]


def pick_client(client: Client, headers: dict) -> Client:
    """The client a step runs as (X-Replay-As, X-Replay-NoCookie, Host); pops those headers."""
    who = headers.pop("X-Replay-As", None)
    if who:
        client = _as_client(client, who)
    if headers.pop("X-Replay-NoCookie", None):
        client = Client(client.server)
    host = headers.pop("Host", None)
    if host is not None:
        other = Client(client.server, host_header=host.replace("{port}", str(client.server.port)))
        other.cookies = client.cookies
        client = other
    return client


def fill_etags(headers: dict, path: str, memory: dict) -> dict:
    """N19: {etag}, {etag-strong} and {detail-etag:ID} from this server's own replies."""
    out = {}
    for key, value in headers.items():
        if isinstance(value, str) and "{" in value:
            own = memory.get(path, "")
            value = value.replace("{etag}", own).replace("{etag-strong}", own[2:] if own.startswith("W/") else own)
            value = re.sub(r"\{detail-etag:([^}]+)\}", lambda m: memory.get("detail:" + m.group(1), ""), value)
        out[key] = value
    return out


def remember(memory: dict, path: str, resp) -> None:
    etag = resp.header("etag")
    if resp.status == 200 and etag:
        memory[path] = etag
    if resp.status == 200 and path.startswith("/api/unit-details"):
        try:
            for key, value in (json.loads(resp.body).get("etags") or {}).items():
                memory["detail:" + key] = value
        except (ValueError, AttributeError):
            pass


N7_FIELDS = ("store_ms", "generated_ms", "elapsed_ms", "timing", "latency_ms")
# Routes whose GET body carries N7 timing fields: the length of their HEAD
# reply (no body to mask) changes with the number of digits in those values.
HEAD_TIMING_PATHS = ("/api/project/overview",)


def head_via_get(step, a, b, na, nb, clients, dirs) -> list[str]:
    """N21: a probe HEAD reply has no body, so N3-N7 and N19 cannot mask its
    ETag and Content-Length.  Each server's HEAD must equal its own GET of the
    same URL (status, ETag, Content-Length, Content-Type), sent right after
    with the same replay clock; the two GET replies are then compared like
    any step."""
    problems = []
    if replay_probe.is_not_ported(b.status, b.body):
        problems.append("Go answered 501 NOT_PORTED")
    get = (step[0], "GET", step[2], None, dict(step[4]))
    twins = [send(c, per_server(get, d)) for c, d in zip(clients, dirs)]
    heads = [a, b]
    for i in (0, 1):  # a wall-clock field (e.g. worktrees generated_at) may tick between
        for _ in range(3):  # HEAD and GET: resend the pair back to back, up to 3 times
            h, t = heads[i], twins[i]
            if h.status != t.status or (h.header("etag") == t.header("etag") and h.header("content-length")
                                        in ("", str(len(t.raw)))):
                break
            head_step = per_server(step, dirs[i])
            heads[i] = send(clients[i], head_step)
            twins[i] = send(clients[i], per_server(get, dirs[i]))
    for side, head, twin in (("py", heads[0], twins[0]), ("go", heads[1], twins[1])):
        if head.body:
            problems.append(f"{side}: HEAD reply has a body")
        for name in ("etag", "content-type", "content-encoding"):
            if head.header(name) != twin.header(name) and head.status == twin.status:
                problems.append(f"{side}: HEAD {name} {head.header(name)!r} != GET {twin.header(name)!r}")
        if head.status != twin.status:
            problems.append(f"{side}: HEAD status {head.status} != GET {twin.status}")
        elif head.header("content-length") and head.header("content-length") != str(len(twin.raw))                 and not any(f'"{k}"'.encode() in twin.body for k in N7_FIELDS):
            problems.append(f"{side}: HEAD content-length {head.header('content-length')} != GET body {len(twin.raw)}")
    if a.status != b.status:
        problems.append(f"status {a.status} != {b.status}")
    problems += compare(get, twins[0], twins[1], na, nb)
    return problems


def send(client: Client, step) -> object:
    _route, method, path, body, headers = step
    headers = dict(headers)
    client = pick_client(client, headers)
    if method == "POST":
        return client.post(path, body, headers=headers)
    return client.request(method, path, body=body, headers=headers)


def events_exchange(client: Client, step, during: list, before: int, after: int, wait: float):
    """N20: open an SSE stream, read `before` frames, send `during` steps as
    the same client, read `after` more frames, close.  Returns the stream as
    a Response (frames joined by blank lines) and the during replies."""
    import http.client
    import socket
    from serverctl import Response
    _route, method, path, _body, headers = step
    headers = dict(headers)
    client = pick_client(client, headers)
    conn = http.client.HTTPConnection("127.0.0.1", client.server.port, timeout=wait)
    hdrs = {"Host": client.host_header if client.host_header is not None else f"127.0.0.1:{client.server.port}",
            "Accept": "text/event-stream"}
    if client.cookies:
        hdrs["Cookie"] = "; ".join(f"{k}={v}" for k, v in client.cookies.items())
    hdrs.update(headers)
    frames: list[str] = []
    replies = []
    try:
        conn.putrequest(method, path, skip_host=True, skip_accept_encoding=True)
        for key, value in hdrs.items():
            conn.putheader(key, str(value).encode("utf-8").decode("latin-1"))
        conn.endheaders()
        resp = conn.getresponse()
        if resp.status != 200 or not resp.getheader("Content-Type", "").startswith("text/event-stream"):
            return Response(resp.status, resp.getheaders(), resp.read()), [send(client, s) for s in during]

        def frame():
            lines: list[str] = []
            while True:
                try:
                    line = resp.readline()
                except (socket.timeout, TimeoutError, OSError):
                    return None
                if not line:
                    return "".join(lines) or None
                text = line.decode("utf-8", "replace").replace("\r\n", "\n")
                if text == "\n":
                    if lines and not all(item.startswith(":") for item in lines):
                        return "".join(lines)
                    lines = []
                    continue
                lines.append(text)

        for count, then in ((before, during), (after, [])):
            for _ in range(count):
                item = frame()
                if item is None:
                    frames.append("<no frame>")
                    break
                frames.append(item)
            replies += [send(client, s) for s in then]
        return Response(resp.status, resp.getheaders(), "\n".join(frames).encode("utf-8")), replies
    finally:
        conn.close()


def downgrade_v10(data_dir: Path) -> None:
    """Turn the fixture's project databases back into schema v10 (no change log, no topic_members.w)."""
    import sqlite3
    for path in sorted((Path(data_dir) / "projects").glob("*.ark/ark.sqlite3")):
        con = sqlite3.connect(str(path))
        try:
            for (name,) in con.execute("SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE '%\\_log\\_%' "
                                       "ESCAPE '\\'").fetchall():
                con.execute(f'DROP TRIGGER "{name}"')
            con.execute("DROP TABLE IF EXISTS change_log")
            con.execute("DELETE FROM sqlite_sequence WHERE name='change_log'")
            con.execute("DELETE FROM meta WHERE key='change_log_id'")
            # v12 added topic_members.w; a v10/v11 database has no such column.
            con.execute("ALTER TABLE topic_members DROP COLUMN w")
            con.execute("UPDATE meta SET value='10' WHERE key='schema_version'")
            con.commit()
        finally:
            con.close()


def replay(go: str, *, auth_users: int = 0, report_path: str = "", nodes: int = 0,
           as_owner: bool = False, trace: str = "", only: str = "", py_debug: bool = False,
           keep_logs: str = "") -> dict:
    ported = go_ported(go)
    replay_services.prepare_env()  # chat: fake model endpoint key, no real provider key
    work = temp_dir("ark-diff-")
    start = datetime.now(timezone.utc).isoformat(timespec="seconds")
    result = {"normalizations": [line.strip() for line in __doc__.splitlines()
                                 if re.match(r"N\d", line.strip())],
              "steps": 0, "compared": 0, "skipped": 0, "mismatches": [], "intentional": []}
    try:
        fixture = make_fixture(work / "fixture", auth=auth_users, nodes=nodes)
        if not auth_users:
            downgrade_v10(Path(fixture["data_dir"]))
        time.sleep(1.1)  # every fixture timestamp is now strictly older than the replay
        start = datetime.now(timezone.utc).isoformat(timespec="seconds")
        py_dir = copy_fixture(Path(fixture["data_dir"]), work / "py")
        go_dir = copy_fixture(Path(fixture["data_dir"]), work / "go")
        env = {"ARK_ALLOWED_HOSTS": "contract.example", "ARK_REPLAY_CLOCK": "1",
               "ARK_BLACKBOX": "1", "ARK_BLACKBOX_INTERVAL": str(10 ** 12),
               # Workbench Search: no ripgrep, so "backend" does not depend on
               # which file texts each server happens to have cached.
               "ARK_RG": "none", **replay_agents.agent_env(work),
               **replay_sessions.session_env(work), **replay_sessionctx.ctx_env()}
        clock0 = datetime.fromisoformat(start).timestamp() + 5
        # --py-debug: ARK_DEBUG on the Python server only (tracebacks of 500s in
        # its log; it changes no reply).  --keep-logs copies both logs out.
        py_env = dict(env, ARK_DEBUG="1") if py_debug else env
        with Server("python", py_dir, env=py_env) as py, Server(go, go_dir, env=env) as gs:
            cp, cg = Client(py), Client(gs)
            REPLAY_AS.clear()
            if auth_users:
                REPLAY_AS.update(users=fixture["users"], password=fixture["password"])
                user = fixture["owner"] if as_owner else fixture["users"][0]
                for client in (cp, cg):
                    client.login(user, fixture["password"])
            np_, ng = Normalizer(py_dir, start, py.port), Normalizer(go_dir, start, gs.port)
            units = unit_refs(cp)
            unit_refs(cg)  # the same read on both: in account mode it pins the session project
            ids = fixture_ids(Path(fixture["data_dir"]))
            views = view_steps(fixture, ids, bool(auth_users))
            agents = agent_steps(fixture, ids, units, bool(auth_users))
            relogin = [(RELOGIN, "", "", None, {})]
            steps = (replay_writes.agent_write_steps(fixture) + read_steps(units) + views + agents + relogin
                     + write_steps(units)
                     + replay_writes.write_steps(fixture) + replay_main_sync.steps(fixture)
                     + replay_services_steps(fixture)
                     + replay_sessions.session_steps(fixture)
                     + replay_sessionctx.ctx_steps(fixture)
                     + replay_settings.settings_steps(fixture)
                     + replay_skills.skills_steps(fixture)
                     + relogin + read_steps(units) + views
                     + replay_probe.probe_steps(fixture, units[0]["uid"] if units else "x"))
            if only:  # development aid: one module's steps (and its WAIT/HOOK steps) only
                steps = [s for s in steps if s[4].get("X-Replay-Module") in only.split(",")
                         or ((callable(s[0]) or s[0] in (replay_sessions.WAIT_SESSION, replay_writes.WAIT_TERMINAL,
                                                     replay_agents.HOOK, replay_services.CALL))
                             and "X-Replay-Module" not in s[4])]
            vars_: dict = {}
            clock_now = str(int(clock0))
            memory = ({}, {})  # N19: each server's own ETags
            for n, step in enumerate(steps):
                if step[0] == replay_writes.WAIT_TERMINAL:
                    if "exact:/api/workspace/terminals" in ported:
                        wait_terminal((cp, cg), replay_writes.placeholders(vars_, step[2]))
                    continue
                if step[0] == replay_sessions.WAIT_SESSION:
                    if r"regex:^/api/agent-sessions/([^/]+)/logs$" in ported:
                        replay_sessions.wait_session((cp, cg), str(vars_.get(step[1], step[1])), step[2])
                    continue
                if step[0] == replay_services.CALL:
                    step[3](py_dir, go_dir)  # e.g. write the GPU server list into both data dirs
                    continue
                if step[0] == replay_agents.HOOK:
                    replay_agents.run_hook(step[1], [py_dir, go_dir], fixture)
                    continue
                if step[0] == RELOGIN:
                    # edge_steps signs out with /logout; write sequences run signed in.
                    if auth_users:
                        for client in (cp, cg):
                            client.cookies.pop("ark_session", None)
                            client.login(user, fixture["password"])
                    continue
                if callable(step[0]):  # a file action of replay_main_sync, applied to both data dirs
                    if step[1] in ported:
                        step[0](py_dir, go_dir)
                    continue
                result["steps"] += 1
                if step[0] not in ported and not step[0].startswith(replay_probe.ROUTE_PREFIX):
                    result["skipped"] += 1
                    continue
                module = step[4].get("X-Replay-Module") or module_of(step[0])
                same_second = bool(step[4].get("X-Replay-SameSecond"))
                step = resolve(step, vars_)
                step[4].pop("X-Replay-SameSecond", None)
                offset = int(step[4].pop("X-Replay-ClockOffset", 0) or 0)
                # Both servers stamp this request with the same second (replayclock.py);
                # X-Replay-SameSecond reuses the previous step's second.
                if not same_second:
                    clock_now = str(int(clock0 + n + offset))
                step[4]["X-Replay-Clock"] = clock_now
                save = step[4].pop("X-Replay-Save", None)
                learn_as = step[4].pop("X-Replay-Learn", None)
                events = step[4].pop("X-Replay-Events", None)
                pairs = []
                if events:
                    during = [resolve(s, vars_) for s in events.get("during", [])]
                    for s in during:
                        s[4]["X-Replay-Clock"] = clock_now
                    t0 = time.monotonic()
                    outs = [events_exchange(c, per_server(step, d), [per_server(s, d) for s in during],
                                            events.get("before", 2), events.get("after", 1), events.get("wait", 6.0))
                            for c, d in ((cp, py_dir), (cg, go_dir))]
                    t1 = t2 = time.monotonic()
                    pairs.append((step, outs[0][0], outs[1][0], (None, None)))
                    pairs += [(s, ra, rb, (None, None)) for s, ra, rb in zip(during, outs[0][1], outs[1][1])]
                else:
                    sent = tuple(memory[i].get(step[2]) for i in (0, 1))
                    steps_ = [per_server((step[0], step[1], step[2], step[3], fill_etags(step[4], step[2], memory[i])), d)
                              for i, d in ((0, py_dir), (1, go_dir))]
                    t0 = time.monotonic()
                    a = send(cp, steps_[0])
                    t1 = time.monotonic()
                    b = send(cg, steps_[1])
                    t2 = time.monotonic()
                    for i, resp in ((0, a), (1, b)):
                        remember(memory[i], step[2], resp)
                    pairs.append((step, a, b, sent))
                for step_, a, b, sent in pairs:
                    replay_writes.learn(vars_, step_[1], step_[2], a.status, a.body)
                    if learn_as and step_ is step:
                        replay_main_sync.learn_cursor(vars_, learn_as, a.status, a.body)
                    if save and step_ is step:
                        replay_sessions.learn(vars_, save, a.body)
                    if trace and module == trace:
                        print(f"  trace #{n} {step_[1]} {step_[2][:70]} -> {a.status}/{b.status} "
                              f"({t1 - t0:.2f}s/{t2 - t1:.2f}s) {a.body[:110]!r}", flush=True)
                    result["compared"] += 1
                    if step_[1] == "HEAD" and step_[0].startswith(replay_probe.ROUTE_PREFIX):
                        problems = head_via_get(step_, a, b, np_, ng, (cp, cg), (py_dir, go_dir))
                    else:
                        problems = compare(step_, a, b, np_, ng, sent)
                    counts = result.setdefault("modules", {}).setdefault(module, [0, 0])
                    counts[0] += 1
                    fix = go_only.fix_for(step_[1], step_[2], a, b) if problems else None
                    if fix is not None:
                        result["intentional"].append({"n": n, "method": step_[1], "path": step_[2],
                                                      "module": module, "fix": fix.id})
                    elif problems:
                        counts[1] += 1
                        result["mismatches"].append({"n": n, "method": step_[1], "path": step_[2],
                                                     "route": step_[0], "module": module, "problems": problems})
        # Both servers are stopped: compare what they left on disk (statediff.py).
        result["state"] = state_diff(state_dump(py_dir, start), state_dump(go_dir, start))
        # Requests both servers sent to the fake model endpoint (chat).
        result["state"] += replay_services.outbound_problems()
    finally:
        if keep_logs:
            Path(keep_logs).mkdir(parents=True, exist_ok=True)
            for log in work.glob("*.log"):
                shutil.copy2(log, Path(keep_logs) / log.name)
        _rmtree(work)
    if report_path:
        Path(report_path).write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    return result


def fresh(go: str) -> bool:
    """Both servers on an empty root: the first-run project and org they create."""
    ok = True
    for demo in (False, True):
        work = temp_dir("ark-fresh-")
        try:
            start = datetime.now(timezone.utc).isoformat(timespec="seconds")
            dirs = {}
            for kind, spec in (("py", "python"), ("go", go)):
                data = work / kind
                data.mkdir()
                with Server(spec, data, env={"ARK_REPLAY_CLOCK": "1"}, demo=demo) as server:
                    Client(server).get("/api/bootstrap")
                    # Go runs the creator-assignee backfill at startup, Python on
                    # the first project request: make one so both have run it.
                    Client(server).get("/api/project")
                dirs[kind] = data
            problems = state_diff(state_dump(dirs["py"], start), state_dump(dirs["go"], start))
            print(f"fresh root, demo={demo}: {len(problems)} state differences")
            for problem in problems[:10]:
                print("  state: " + problem[:400])
            ok = ok and not problems
        finally:
            _rmtree(work)
    return ok


def handoff(go: str) -> bool:
    """A remembered Python session cookie keeps working after switching to Go, and back."""
    work = temp_dir("ark-handoff-")
    try:
        fixture = make_fixture(work / "fixture", auth=1)
        data = Path(fixture["data_dir"])
        with Server("python", data) as py:
            c = Client(py)
            check = c.login(fixture["users"][0], fixture["password"], remember=True)
            token = c.cookies.get("ark_session")
            ok_py = check.status == 303 and token
        port = py.port
        with Server(go, data, port=port) as gs:
            c = Client(gs)
            c.cookies["ark_session"] = token
            ok_go = c.get("/api/auth/session").json().get("authenticated") is True
            c2 = Client(gs)
            c2.login(fixture["users"][0], fixture["password"], remember=True)
            go_token = c2.cookies.get("ark_session")
        with Server("python", data, port=port) as py:
            c = Client(py)
            c.cookies["ark_session"] = go_token
            ok_back = c.get("/api/auth/session").json().get("authenticated") is True
        print(f"python->go remembered cookie: {ok_go}; go->python remembered cookie: {ok_back}")
        return bool(ok_py and ok_go and ok_back)
    finally:
        _rmtree(work)


def prod_copy(go: str, source: Path, limit: int = 2000, show: int = 15) -> bool:
    """Go/no-go gate before a switch-over: both servers on copies of real data.

    ``source`` is a projects root (a copy of production's, never the live
    folder).  Each ``*.ark`` project is staged alone into two fresh copies;
    both servers open it (startup migrations included), the generic read
    steps run against both (unit list, up to ``limit`` unit details and
    comment lists, graph, snapshot, timeline, activity, request-parsing
    edges) plus the project views, and the data dirs are compared after
    (statediff).  Local mode, no sign-in; same normalizations as the replay.
    """
    ok = True
    projects = sorted(p for p in Path(source).glob("*.ark") if (p / "ark.sqlite3").is_file())
    if not projects:
        print(f"no *.ark projects with ark.sqlite3 under {source}")
        return False
    for project in projects:
        work = temp_dir("ark-prodcopy-")
        try:
            stage = work / "stage"
            shutil.copytree(project, stage / "projects" / project.name)
            start = datetime.now(timezone.utc).isoformat(timespec="seconds")
            py_dir = copy_fixture(stage, work / "py")
            go_dir = copy_fixture(stage, work / "go")
            # ARK_REPLAY_CLOCK=1: random ids drawn at startup (client-cache salt,
            # change_log_id of a migrated database) come from the same named
            # streams on both sides; the steps send no X-Replay-Clock, so times
            # are real (N4 masks them).
            env = {"ARK_ALLOWED_HOSTS": "contract.example", "ARK_RG": "none", "ARK_REPLAY_CLOCK": "1"}
            bad = compared = 0
            with Server("python", py_dir, env=env) as py, Server(go, go_dir, env=env) as gs:
                cp, cg = Client(py), Client(gs)
                np_, ng = Normalizer(py_dir, start, py.port), Normalizer(go_dir, start, gs.port)
                units = unit_refs(cp)[:limit]
                unit_refs(cg)
                steps = read_steps(units) + [
                    ("exact:/api/bootstrap", "GET", "/api/bootstrap", None, {}),
                    ("exact:/api/project/overview", "GET", "/api/project/overview", None, {}),
                    ("exact:/api/project/settings", "GET", "/api/project/settings", None, {}),
                    ("exact:/api/topics", "GET", "/api/topics", None, {}),
                    ("exact:/api/export", "GET", "/api/export?format=json", None, {}),
                    ("exact:/api/export", "GET", "/api/export?format=markdown", None, {}),
                ]
                for step in steps:
                    a, b = send(cp, step), send(cg, step)
                    if step[1] == "HEAD":  # N21: no body to normalize; check each HEAD against its GET
                        problems = head_via_get(step, a, b, np_, ng, (cp, cg), (py_dir, go_dir))
                    else:
                        problems = compare(step, a, b, np_, ng)
                    compared += 1
                    if problems:
                        bad += 1
                        if bad <= show:
                            print(f"  {project.name}: {step[1]} {step[2]}")
                            for problem in problems[:3]:
                                print("      " + problem[:400])
            state = state_diff(state_dump(py_dir, start), state_dump(go_dir, start))
            for problem in state[:show]:
                print(f"  {project.name} state: " + problem[:400])
            print(f"{project.name}: units={len(units)} compared={compared} mismatches={bad} state_diffs={len(state)}")
            ok = ok and not bad and not state
        finally:
            _rmtree(work)
    return ok


def main(argv=None) -> int:
    for stream in (sys.stdout, sys.stderr):  # reports quote non-ASCII bodies; never fail on a narrow console
        try:
            stream.reconfigure(errors="backslashreplace")
        except (AttributeError, ValueError):
            pass
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--go", required=True,
                        help="path to the ark-server binary, or 'python' for a Python-vs-Python self-check")
    parser.add_argument("--report", default="")
    parser.add_argument("--auth", action="store_true", help="replay as a signed-in member")
    parser.add_argument("--owner", action="store_true", help="with --auth: sign in as the org owner")
    parser.add_argument("--trace", default="", help="print status and body start of every step of a module")
    parser.add_argument("--handoff", action="store_true")
    parser.add_argument("--only", default="", help="replay only the steps of these modules (comma-separated; development)")
    parser.add_argument("--fresh", action="store_true", help="compare what both servers create on an empty root")
    parser.add_argument("--py-debug", action="store_true",
                        help="ARK_DEBUG=1 for the Python server: tracebacks of its 500 replies in its log")
    parser.add_argument("--keep-logs", default="", help="copy both server logs into this folder")
    parser.add_argument("--show", type=int, default=15)
    parser.add_argument("--nodes", type=int, default=0, help="extra generated units in the fixture")
    parser.add_argument("--prod-copy", default="",
                        help="projects root holding COPIES of real .ark folders: read-only go/no-go gate")
    parser.add_argument("--limit", type=int, default=2000, help="with --prod-copy: unit details per project")
    args = parser.parse_args(argv)
    if args.prod_copy:
        return 0 if prod_copy(args.go, Path(args.prod_copy), args.limit, args.show) else 1
    if args.handoff:
        return 0 if handoff(args.go) else 1
    if args.fresh:
        return 0 if fresh(args.go) else 1
    result = replay(args.go, auth_users=3 if args.auth or args.owner else 0, report_path=args.report,
                    nodes=args.nodes, as_owner=args.owner, trace=args.trace, only=args.only,
                    py_debug=args.py_debug, keep_logs=args.keep_logs)
    for item in result["mismatches"][:args.show]:
        print(f"#{item['n']} {item['method']} {item['path']}")
        for problem in item["problems"]:
            print("    " + problem[:600])
    for item in result.get("intentional", []):
        print(f"  intentional #{item['n']} {item['method']} {item['path']} ({item['fix']}, go_only.FIXES)")
    for name, (compared, bad) in sorted(result.get("modules", {}).items()):
        print(f"  module {name:20} compared={compared:5} mismatches={bad}")
    for problem in result.get("state", [])[:args.show]:
        print("  state: " + problem[:600])
    print(f"steps={result['steps']} compared={result['compared']} skipped={result['skipped']} "
          f"mismatches={len(result['mismatches'])} state_diffs={len(result.get('state', []))} "
          f"intentional={len(result.get('intentional', []))}")
    return 1 if result["mismatches"] or result.get("state") else 0


if __name__ == "__main__":
    raise SystemExit(main())
