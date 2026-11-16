"""Start an ARK server (Python or Go) on a free port and talk to it over HTTP.

Stdlib only.  Shared by harness.py (black-box contract cases), diffreplay.py
(Python vs Go differential replay) and the load-test driver.
"""
from __future__ import annotations

import gzip
import http.client
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
ASSET_VERSION = "contract1"
PYTHON = os.environ.get("ARK_CONTRACT_PYTHON") or sys.executable


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def server_spec() -> str:
    """``python`` or a path to an ark-server binary (ARK_CONTRACT_SERVER)."""
    return (os.environ.get("ARK_CONTRACT_SERVER") or "python").strip()


def make_fixture(target: Path, *, auth: int = 0, nodes: int = 0) -> dict:
    """Build a data dir with the Python reference store (see fixture.py)."""
    out = subprocess.run([PYTHON, "-B", str(REPO / "tests" / "python" / "contract" / "fixture.py"),
                          str(target), "--auth", str(auth), "--nodes", str(nodes)],
                         cwd=str(REPO), capture_output=True, text=True, check=True)
    return json.loads(out.stdout.strip().splitlines()[-1])


def copy_fixture(source: Path, target: Path) -> Path:
    """Copy a data dir; the org registry stores absolute project paths, so the
    copy's ``projects.path`` rows are rewritten to point at the copy."""
    import sqlite3
    shutil.copytree(source, target)
    org = target / "org.sqlite3"
    if org.is_file():
        old, new = str(Path(source).resolve()), str(Path(target).resolve())
        con = sqlite3.connect(str(org))
        try:
            con.execute("UPDATE projects SET path=? || substr(path, ?) WHERE substr(path, 1, ?)=?",
                        (new, len(old) + 1, len(old), old))
            con.commit()
        finally:
            con.close()
    return target


class Server:
    """One server process bound to 127.0.0.1:<port> on ``data_dir``."""

    def __init__(self, spec: str, data_dir: Path, *, env: dict | None = None,
                 port: int | None = None, args: list[str] | None = None, demo: bool = False):
        self.spec = spec
        self.data_dir = Path(data_dir).resolve()
        self.root = self.data_dir / "projects"
        self.port = port or free_port()
        self.extra_env = dict(env or {})
        self.extra_args = list(args or [])
        self.demo = demo
        self.proc: subprocess.Popen | None = None
        self.log_path = self.data_dir.parent / f"{self.data_dir.name}-{self.kind}.log"

    @property
    def kind(self) -> str:
        return "python" if self.spec == "python" else "go"

    def command(self) -> list[str]:
        common = ["serve", "--root", str(self.root), "--host", "127.0.0.1",
                  "--port", str(self.port), *([] if self.demo else ["--no-demo"]),
                  "--sessions-root", str(self.data_dir / "agent-sessions"), *self.extra_args]
        if self.kind == "python":
            if self.extra_env.get("ARK_REPLAY_CLOCK") == "1":
                shim = REPO / "tests" / "python" / "contract" / "replayclock.py"
                return [PYTHON, "-B", str(shim), *common]
            return [PYTHON, "-B", "-m", "arkstudio", *common]
        return [self.spec, *common]

    def env(self) -> dict:
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(("ARK_AUTH", "ARK_PUBLIC", "ARK_HOST", "ARK_PORT",
                                    "ARK_ALLOWED_HOSTS", "ARK_TRUST_PROXY"))}
        env.update({
            "ARK_ASSET_VERSION": ASSET_VERSION,
            "ARK_BLACKBOX": "0",
            "ARK_ORG_DB": str(self.data_dir / "org.sqlite3"),
            "ARK_PRIVATE_DIR": str(self.data_dir / "private"),
            "PYTHONIOENCODING": "utf-8",
            "PYTHONDONTWRITEBYTECODE": "1",
        })
        env.update(self.extra_env)
        return env

    def start(self, timeout: float = 60.0) -> "Server":
        log = open(self.log_path, "ab")
        flags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
        self.proc = subprocess.Popen(self.command(), cwd=str(REPO), env=self.env(),
                                     stdout=log, stderr=subprocess.STDOUT,
                                     creationflags=flags)
        log.close()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(f"{self.kind} server exited early (code {self.proc.returncode}); "
                                   f"log: {self.log_path}\n" + self.log_text()[-3000:])
            try:
                if Client(self).get("/api/health").status == 200:
                    return self
            except OSError:
                time.sleep(0.1)
        self.stop()
        raise RuntimeError(f"{self.kind} server did not become ready; log: {self.log_path}")

    def log_text(self) -> str:
        try:
            return self.log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            return ""

    def stop(self) -> None:
        proc, self.proc = self.proc, None
        if proc is None or proc.poll() is not None:
            return
        if os.name == "nt":
            subprocess.run(["taskkill", "/T", "/F", "/PID", str(proc.pid)],
                           capture_output=True, check=False)
        else:
            proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)

    def __enter__(self):
        return self.start()

    def __exit__(self, *exc):
        self.stop()


class Response:
    def __init__(self, status: int, headers: list[tuple[str, str]], raw: bytes):
        self.status = status
        self.header_list = headers
        self.headers = {}
        for key, value in headers:
            self.headers.setdefault(key.lower(), value)
        self.raw = raw
        self.body = gzip.decompress(raw) if self.headers.get("content-encoding") == "gzip" and raw else raw

    def header(self, name: str, default: str = "") -> str:
        return self.headers.get(name.lower(), default)

    def all(self, name: str) -> list[str]:
        return [v for k, v in self.header_list if k.lower() == name.lower()]

    @property
    def text(self) -> str:
        return self.body.decode("utf-8", errors="replace")

    def json(self):
        return json.loads(self.body.decode("utf-8"))

    def __repr__(self):
        return f"<Response {self.status} {self.header('content-type')} {len(self.body)}B>"


class Client:
    """Minimal HTTP/1.1 client with a cookie jar; one connection per request."""

    def __init__(self, server: Server, *, host_header: str | None = None):
        self.server = server
        self.host_header = host_header
        self.cookies: dict[str, str] = {}

    def origin(self) -> str:
        return f"http://127.0.0.1:{self.server.port}"

    def request(self, method: str, path: str, *, headers: dict | None = None,
                body: bytes | str | dict | None = None, timeout: float = 60.0) -> Response:
        conn = http.client.HTTPConnection("127.0.0.1", self.server.port, timeout=timeout)
        hdrs = {"Host": (self.host_header if self.host_header is not None
                         else f"127.0.0.1:{self.server.port}")}
        if self.cookies:
            hdrs["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        if isinstance(body, dict):
            body = json.dumps(body, ensure_ascii=False).encode("utf-8")
            hdrs["Content-Type"] = "application/json"
        elif isinstance(body, str):
            body = body.encode("utf-8")
        hdrs.update(headers or {})
        try:
            conn.putrequest(method, path, skip_host=True, skip_accept_encoding=True)
            for key, value in hdrs.items():
                # Non-Latin-1 text goes out as raw UTF-8 bytes, like a browser.
                conn.putheader(key, str(value).encode("utf-8").decode("latin-1"))
            if body is not None:
                conn.putheader("Content-Length", str(len(body)))
            conn.endheaders(body)
            resp = conn.getresponse()
            raw = resp.read()
            out = Response(resp.status, resp.getheaders(), raw)
        finally:
            conn.close()
        for cookie in out.all("set-cookie"):
            name, _, rest = cookie.partition("=")
            value = rest.split(";", 1)[0]
            if "Max-Age=0" in cookie or not value:
                self.cookies.pop(name.strip(), None)
            else:
                self.cookies[name.strip()] = value
        return out

    def get(self, path: str, **kw) -> Response:
        return self.request("GET", path, **kw)

    def post(self, path: str, body=None, *, same_origin: bool = True, **kw) -> Response:
        headers = dict(kw.pop("headers", {}) or {})
        if same_origin:
            headers.setdefault("Origin", self.origin())
        return self.request("POST", path, body=body if body is not None else {}, headers=headers, **kw)

    def login(self, username: str, password: str, *, remember: bool = False) -> Response:
        page = self.get("/login")
        csrf = page.text.split('name="csrf" value="', 1)[1].split('"', 1)[0]
        from urllib.parse import urlencode
        form = urlencode({"username": username, "password": password, "csrf": csrf,
                          "next": "/", **({"remember": "1"} if remember else {})})
        return self.request("POST", "/login", body=form, headers={
            "Content-Type": "application/x-www-form-urlencoded", "Origin": self.origin()})


def temp_dir(prefix: str) -> Path:
    return Path(tempfile.mkdtemp(prefix=prefix))
