"""Mobile pairing, device sessions and ARK connection discovery.

The phone client is the regular ARK web app served over HTTP(S).  Pairing is a
two-step exchange:

1. The desktop Settings view asks the server to create a short-lived *pairing
   code*.  Only the QR/pairing URL carries that code; it is never written to a
   log and it is claimed at most once.
2. The phone opens the pairing URL (``<base>/#pair=<code>``), calls
   ``POST /api/mobile/pair/claim`` and receives a separate, scoped *mobile
   session token*.  That token is kept in the phone's localStorage and sent on
   API calls as ``X-ARK-Mobile-Token: <token>`` (or ``?mobile_token=`` for
   browser resources that cannot set a header).

This module deliberately contains no HTTP code so the security rules can be
tested directly: short expiries, project binding, one-time claims, revocation,
read-only enforcement and rate limiting live here.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import ipaddress
import os
import re
import secrets
import shlex
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.request
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import quote, urlparse

LOOPBACK_BIND_HOSTS = {"127.0.0.1", "localhost", "::1", "[::1]"}
PERMISSIONS = ("read_only", "full")
DEFAULT_PERMISSION = "full"
PAIRING_TTL_DEFAULT = 600
PAIRING_TTL_MAX = 3600
SESSION_TTL_DEFAULT = 12 * 3600
SESSION_TTL_MAX = 365 * 24 * 3600
# A permanent phone session lasts until it is revoked (or the phone signs out).
SESSION_PERMANENT = 0
PERMANENT_HORIZON = 100 * 365 * 24 * 3600
PERMANENT_WORDS = {"permanent", "never", "forever", "until_revoked", "0"}
CLAIM_LIMIT = 8
CLAIM_WINDOW = 60.0
CREATE_LIMIT = 30
CREATE_WINDOW = 3600.0


class MobileError(Exception):
    """A clean mobile API error with an HTTP status and machine-readable code."""

    def __init__(self, status: int, message: str, code: str = "MOBILE_ERROR"):
        super().__init__(message)
        self.status = status
        self.message = message
        self.code = code


def _sha256(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _iso(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, tz=timezone.utc).isoformat(timespec="seconds")


def _utcnow() -> float:
    return time.time()


def normalize_public_url(value: str) -> str:
    """Validate and normalise an advertised ARK base URL.

    A base URL may contain a path prefix (``https://ark.example.net/ark/``) but
    never credentials, a query or a fragment.  The returned value always ends
    with ``/`` so appending ``#pair=...`` cannot land on the wrong path.
    """
    text = str(value or "").strip()
    if not text:
        raise MobileError(400, "public URL is empty", "INVALID_PUBLIC_URL")
    if len(text) > 500 or any(char in text for char in ("\r", "\n", "\t", " ")):
        raise MobileError(400, "public URL contains whitespace", "INVALID_PUBLIC_URL")
    parsed = urlparse(text)
    if parsed.scheme not in {"http", "https"}:
        raise MobileError(400, "public URL must start with http:// or https://",
                          "INVALID_PUBLIC_URL")
    if not parsed.hostname:
        raise MobileError(400, "public URL has no host", "INVALID_PUBLIC_URL")
    if parsed.username or parsed.password:
        raise MobileError(400, "public URL must not contain credentials",
                          "INVALID_PUBLIC_URL")
    if parsed.query or parsed.fragment:
        raise MobileError(400, "public URL must not contain a query or fragment",
                          "INVALID_PUBLIC_URL")
    try:
        port = parsed.port
    except ValueError as exc:
        raise MobileError(400, "public URL has an invalid port", "INVALID_PUBLIC_URL") from exc
    if port is not None and not 1 <= port <= 65535:
        raise MobileError(400, "public URL port is out of range", "INVALID_PUBLIC_URL")
    path = parsed.path or "/"
    if not path.endswith("/"):
        path += "/"
    host = parsed.hostname
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    netloc = host + (f":{port}" if port else "")
    return f"{parsed.scheme}://{netloc}{path}"


def validate_tunnel_host(value: str) -> str:
    """Validate a bare hostname/IP for the desktop tunnel field."""
    host = str(value or "").strip()
    if not host:
        raise MobileError(400, "tunnel host is required", "INVALID_TUNNEL_HOST")
    if len(host) > 253 or any(char in host for char in ("/", "\\", " ", "\r", "\n", "\t", "@")):
        raise MobileError(400, "tunnel host must be a bare hostname or IP address",
                          "INVALID_TUNNEL_HOST")
    if host.startswith("[") and host.endswith("]"):
        inner = host[1:-1]
        try:
            ipaddress.IPv6Address(inner)
        except ValueError as exc:
            raise MobileError(400, "tunnel host is not a valid IPv6 address",
                              "INVALID_TUNNEL_HOST") from exc
        return f"[{inner}]"
    if ":" in host:
        raise MobileError(400, "tunnel host must not include a port",
                          "INVALID_TUNNEL_HOST")
    try:
        ipaddress.IPv4Address(host)
        return host
    except ValueError:
        pass
    if not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?", host):
        raise MobileError(400, "tunnel host is not a valid hostname",
                          "INVALID_TUNNEL_HOST")
    return host


def validate_port(value, *, name: str = "port", default: int | None = None) -> int:
    try:
        port = int(value)
    except (TypeError, ValueError) as exc:
        if default is not None and value in (None, ""):
            return default
        raise MobileError(400, f"{name} must be a number", "INVALID_PORT") from exc
    if not 1 <= port <= 65535:
        raise MobileError(400, f"{name} must be between 1 and 65535", "INVALID_PORT")
    return port


def is_loopback_host(host: str) -> bool:
    text = str(host or "").strip().strip("[]").lower()
    return text in {"localhost", "127.0.0.1", "::1", "0:0:0:0:0:0:0:1"}


_IP_CACHE: tuple[float, list[str]] = (0.0, [])
_PUBLIC_IP_CACHE: dict = {"at": 0.0, "ip": "", "source": "", "error": ""}
PUBLIC_IP_CACHE_TTL = 600.0
PUBLIC_IP_RETRY_TTL = 60.0


def _env_bool(name: str) -> bool:
    return str(os.environ.get(name) or "").strip().lower() in {"1", "true", "yes", "on"}


def _outbound_ipv4() -> str:
    """Return the address the OS would use for an outbound route.

    Connecting a UDP socket sends no packet; it only asks the routing table
    which local address a default route would use.  This finds the usual LAN
    address on Windows hosts whose hostname resolves to a loopback-only entry.
    """
    sock = None
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        sock.settimeout(0.2)
        sock.connect(("192.0.2.1", 9))
        ip = str(sock.getsockname()[0] or "")
        parsed = ipaddress.IPv4Address(ip)
        if not (parsed.is_loopback or parsed.is_link_local or parsed.is_unspecified):
            return ip
    except (OSError, ValueError):
        pass
    finally:
        if sock is not None:
            try:
                sock.close()
            except OSError:
                pass
    return ""


def _private_ipv4() -> list[str]:
    global _IP_CACHE
    cached_at, cached = _IP_CACHE
    if cached and time.monotonic() - cached_at < 60:
        return list(cached)
    addresses: list[str] = []
    outbound = _outbound_ipv4()
    if outbound:
        addresses.append(outbound)
    try:
        for info in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
            ip = info[4][0]
            if not ip or ip in addresses:
                continue
            try:
                parsed = ipaddress.IPv4Address(ip)
            except ValueError:
                continue
            if parsed.is_loopback or parsed.is_link_local or parsed.is_unspecified:
                continue
            if parsed.is_private:
                addresses.append(ip)
    except OSError:
        pass
    # Some Windows hosts expose the LAN address only through the routing table.
    if not addresses and os.name == "nt":
        try:
            output = subprocess.run(
                ["powershell", "-NoProfile", "-Command",
                 "(Get-NetIPAddress -AddressFamily IPv4 | Where-Object "
                 "{ $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } "
                 "| Select-Object -ExpandProperty IPAddress) -join ','"],
                capture_output=True, text=True, timeout=4,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
            ).stdout
            for ip in output.strip().split(","):
                ip = ip.strip()
                if ip and ip not in addresses:
                    addresses.append(ip)
        except (OSError, subprocess.SubprocessError):
            pass
    _IP_CACHE = (time.monotonic(), list(addresses))
    return addresses


def _fetch_public_ip(timeout: float = 1.5) -> tuple[str, str, str]:
    """One short, explicit outbound lookup.  Returns ``(ip, source, error)``."""
    providers = ("https://api.ipify.org", "https://ifconfig.me/ip")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    last_error = ""
    for url in providers:
        try:
            request = urllib.request.Request(
                url, headers={"User-Agent": "ARK-Studio/2.0 public-ip opt-in"})
            with opener.open(request, timeout=timeout) as response:
                text = response.read(128).decode("utf-8", "replace").strip()
            parsed = ipaddress.ip_address(text)
            if parsed.is_global:
                return str(parsed), url, ""
            last_error = "provider returned a non-public address"
        except (OSError, ValueError, urllib.request.URLError) as exc:
            last_error = str(exc)[:200]
    return "", "", last_error or "public IP lookup failed"


def detect_public_ip(enabled: bool | None = None, *, timeout: float = 1.5,
                     force: bool = False) -> dict:
    """Best-effort public IP detection, explicitly opt-in.

    Never called during server startup.  A failure is cached briefly so an
    offline host does not pay the timeout on every Settings refresh, and the
    caller must still describe NAT/port-forwarding as unknown.
    """
    global _PUBLIC_IP_CACHE
    if enabled is None:
        enabled = _env_bool("ARK_DETECT_PUBLIC_IP")
    if not enabled:
        return {"ip": "", "source": "", "error": "", "checked": False,
                "enabled": False}
    now = time.monotonic()
    cached = _PUBLIC_IP_CACHE
    ttl = PUBLIC_IP_CACHE_TTL if cached.get("ip") else PUBLIC_IP_RETRY_TTL
    if not force and cached.get("at") and now - float(cached["at"]) < ttl:
        return {"ip": cached.get("ip", ""), "source": cached.get("source", ""),
                "error": cached.get("error", ""), "checked": True, "enabled": True}
    ip, source, error = _fetch_public_ip(timeout=timeout)
    _PUBLIC_IP_CACHE = {"at": now, "ip": ip, "source": source, "error": error}
    return {"ip": ip, "source": source, "error": error, "checked": True,
            "enabled": True}


def _host_header_port(host_header: str, fallback: int) -> tuple[str, int]:
    text = str(host_header or "").strip()
    if not text:
        return "", fallback
    parsed = urlparse("//" + text)
    try:
        port = parsed.port or fallback
    except ValueError:
        port = fallback
    return parsed.hostname or "", port


def _base_url(host: str, scheme: str, port: int | None) -> str:
    """Build a validated ``scheme://host[:port]/`` base for a QR payload."""
    clean_host = validate_tunnel_host(host)
    clean_scheme = "https" if str(scheme or "").lower() == "https" else "http"
    default_port = 443 if clean_scheme == "https" else 80
    try:
        parsed_port = int(port) if port is not None else default_port
    except (TypeError, ValueError):
        parsed_port = default_port
    if parsed_port == default_port:
        return f"{clean_scheme}://{clean_host}/"
    return f"{clean_scheme}://{clean_host}:{parsed_port}/"


def _public_ip_url(app, ip: str) -> str:
    port = int(getattr(app, "port", 0) or 0)
    try:
        return _base_url(ip, "http", port or 80)
    except MobileError:
        return ""


def _pairing_candidates(app, *, request_host: str = "",
                        request_scheme: str = "",
                        public_detection: dict | None = None) -> list[tuple[str, str]]:
    """Ordered ``(base_url, source)`` candidates for automatic pairing.

    Config beats a request-derived host; request host beats interface
    enumeration, which beats an opt-in public-IP lookup.  The returned list is
    also what Settings displays, so it must not claim the URL is reachable.
    """
    public_url = str(getattr(app, "public_url", "") or "").strip()
    if public_url:
        try:
            return [(normalize_public_url(public_url), "public_url")]
        except MobileError:
            pass
    port = int(getattr(app, "port", 0) or 0)
    candidates: list[tuple[str, str]] = []
    explicit_public = str(getattr(app, "public_ip", "") or "").strip().strip("[]")
    if explicit_public:
        try:
            ipaddress.ip_address(explicit_public)
            url = _public_ip_url(app, explicit_public)
            if url:
                candidates.append((url, "public_ip"))
        except ValueError:
            pass
    host = str(request_host or "").strip()
    if host and not is_loopback_host(host) and not _is_unspecified(host):
        try:
            url = _base_url(host, request_scheme or "http", port or None)
            candidates.append((url, "request_host"))
        except MobileError:
            pass
    for ip in _private_ipv4():
        try:
            candidates.append((_base_url(ip, "http", port or None), "lan"))
        except MobileError:
            continue
    detection = public_detection
    if detection is None and not explicit_public:
        detection = detect_public_ip(
            enabled=bool(getattr(app, "detect_public_ip", False)))
    if detection and detection.get("ip") and not explicit_public:
        url = _public_ip_url(app, detection["ip"])
        if url:
            candidates.append((url, "detected_public_ip"))
    # De-duplicate while preserving order.
    seen = set()
    unique = []
    for url, source in candidates:
        if url in seen:
            continue
        seen.add(url)
        unique.append((url, source))
    return unique


def _is_unspecified(host: str) -> bool:
    try:
        return ipaddress.ip_address(str(host or "").strip("[]")).is_unspecified
    except ValueError:
        return False


def _auth_info(app) -> dict:
    manager = getattr(app, "auth", None)
    required = bool(getattr(app, "auth_enabled", False) or manager)
    return {
        "required": required,
        "user": str(getattr(manager, "username", "") or "") if required else "",
        "note": (
            "Sign in with the ARK account before the app or API responds. "
            "The phone opens the login page first, then claims the pairing code."
            if required else
            "No account is configured. ARK is loopback-only by default; it "
            "refuses a public bind without authentication."
        ),
    }



def build_pairing_url(base: str, token: str) -> str:
    normalized = normalize_public_url(base)
    return f"{normalized.rstrip('/')}/#pair={quote(token, safe='')}"


def url_host_port(base: str) -> tuple[str, int]:
    parsed = urlparse(base)
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    return parsed.hostname or "", port


def tunnel_command(*, ssh_target: str = "user@server", bind: str = "0.0.0.0",
                   local_port: int = 8891, ark_port: int = 8890,
                   ssh_port: int = 22, ssh_binary: str = "ssh") -> list[str]:
    """Build the local-forward command a phone-reachable computer must run.

    The phone cannot run SSH itself.  This command runs on a laptop/desktop on
    the same network as the phone and publishes ARK on that machine's LAN
    interface; the phone then scans a QR code pointing at the laptop.
    """
    local_port = validate_port(local_port, name="local port")
    ark_port = validate_port(ark_port, name="ARK port")
    ssh_port = validate_port(ssh_port, name="SSH port")
    bind = str(bind or "0.0.0.0").strip()
    try:
        ipaddress.ip_address(bind.strip("[]"))
    except ValueError as exc:
        raise MobileError(400, "tunnel bind address must be an IP address",
                          "INVALID_BIND") from exc
    target = str(ssh_target or "").strip() or "user@server"
    if any(char in target for char in ("\r", "\n", "\t", " ")):
        raise MobileError(400, "SSH target must not contain whitespace", "INVALID_SSH_TARGET")
    argv = [ssh_binary, "-N"]
    if ssh_port != 22:
        argv += ["-p", str(ssh_port)]
    argv += ["-L", f"{bind}:{local_port}:127.0.0.1:{ark_port}"]
    argv.append(target)
    return argv


def run_tunnel(*, ssh_target: str, local_port: int = 8891, ark_port: int = 8890,
               ssh_port: int = 22, bind: str = "0.0.0.0",
               identity: str = "", ssh_binary: str = "ssh") -> int:
    """Run the documented SSH local forward until interrupted."""
    argv = tunnel_command(ssh_target=ssh_target, bind=bind, local_port=local_port,
                          ark_port=ark_port, ssh_port=ssh_port, ssh_binary=ssh_binary)
    if identity:
        argv[1:1] = ["-i", str(Path(identity).expanduser())]
    if not (shutil.which(ssh_binary) or Path(ssh_binary).is_file()):
        print(f"error: OpenSSH client not found ({ssh_binary})", file=sys.stderr)
        return 2
    print("ARK mobile tunnel")
    print(f"  command: {shlex.join(argv)}")
    print(f"  LAN URL: http://<this-computer-LAN-IP>:{local_port}/")
    print("  The phone must join the same network; the phone cannot run SSH itself.")
    print("  WARNING: this listens on every interface. Use a trusted LAN, keep the")
    print("  pairing token read-only unless you need writes, and stop the tunnel when done.")
    print("  WARNING: plain HTTP on a LAN is not a secure context, so install/offline")
    print("  caching may be unavailable in the phone browser. See docs/guide/mobile.md.")
    print("Press Ctrl+C to close the tunnel.")
    try:
        process = subprocess.Popen(argv)
    except OSError as exc:
        print(f"error: could not start ssh: {exc}", file=sys.stderr)
        return 2
    try:
        while process.poll() is None:
            time.sleep(0.5)
    except KeyboardInterrupt:
        pass
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
    return 0


@dataclass
class PairingRecord:
    id: str
    token_hash: str
    project_id: str
    project_name: str
    permission: str
    created_at: float
    expires_at: float
    claimed_at: float | None = None
    revoked_at: float | None = None
    # Seconds a claimed phone stays signed in; SESSION_PERMANENT = until revoked.
    session_ttl: int = SESSION_PERMANENT
    # The account that generated the code.  A phone may only ever become this
    # user: a QR from user A can never sign in as user B.
    user: str = ""
    # "qr" for a desktop-issued pairing code, "password" for a native client
    # that signed in with the account password (POST /api/mobile/login).
    source: str = "qr"

    def public(self) -> dict:
        state = "revoked" if self.revoked_at else "claimed" if self.claimed_at else "pending"
        return {
            "id": self.id,
            "permission": self.permission,
            "project": self.project_name,
            "user": self.user,
            "created_at": _iso(self.created_at),
            "expires_at": _iso(self.expires_at),
            "claimed_at": _iso(self.claimed_at) if self.claimed_at else None,
            "revoked_at": _iso(self.revoked_at) if self.revoked_at else None,
            "state": state,
            "session_permanent": self.session_ttl == SESSION_PERMANENT,
            "session_ttl": self.session_ttl or None,
            "source": self.source,
        }


@dataclass
class MobileSession:
    token_hash: str
    pairing_id: str
    project_id: str
    project_name: str
    permission: str
    created_at: float
    expires_at: float
    last_seen_at: float
    user_agent: str = field(default="", repr=False)
    user: str = field(default="", repr=False)

    def public(self) -> dict:
        return {
            "permission": self.permission,
            "project": self.project_name,
            "expires_at": _iso(self.expires_at),
            "created_at": _iso(self.created_at),
            "read_only": self.permission == "read_only",
            "permanent": self.expires_at - self.created_at >= PERMANENT_HORIZON - 1,
        }


class PairingManager:
    """Pairing/session store.

    Pairing codes are short-lived and single-use and only exist in memory.
    Claimed phone sessions default to *until revoked*; when ``state_path`` is
    set they are persisted (token hashes only, never raw tokens) so a server
    restart or redeploy does not sign every phone out.  Revoke a phone from
    Settings -> Mobile.
    """

    def __init__(self, *, clock=_utcnow, pairing_ttl: int = PAIRING_TTL_DEFAULT,
                 session_ttl: int = SESSION_TTL_DEFAULT,
                 claim_limit: int = CLAIM_LIMIT, claim_window: float = CLAIM_WINDOW,
                 create_limit: int = CREATE_LIMIT, create_window: float = CREATE_WINDOW,
                 state_path: str | Path | None = None):
        self.clock = clock
        self.state_path = Path(state_path) if state_path else None
        self.pairing_ttl = max(30, min(PAIRING_TTL_MAX, int(pairing_ttl)))
        self.session_ttl = max(300, min(SESSION_TTL_MAX, int(session_ttl)))
        self.claim_limit = max(1, int(claim_limit))
        self.claim_window = max(1.0, float(claim_window))
        self.create_limit = max(1, int(create_limit))
        self.create_window = max(1.0, float(create_window))
        self._pairings: dict[str, PairingRecord] = {}
        self._by_token: dict[str, str] = {}
        self._sessions: dict[str, MobileSession] = {}
        self._attempts: dict[str, deque[float]] = {}
        self._lock = threading.RLock()
        self._load()

    # ------------------------------------------------------------ persistence
    def _load(self) -> None:
        if not self.state_path or not self.state_path.is_file():
            return
        try:
            data = json.loads(self.state_path.read_text(encoding="utf-8"))
            now = self.clock()
            for item in data.get("pairings") or []:
                record = PairingRecord(**item)
                self._pairings[record.id] = record
                self._by_token[record.token_hash] = record.id
            for item in data.get("sessions") or []:
                session = MobileSession(**item)
                record = self._pairings.get(session.pairing_id)
                if session.expires_at > now and not (record and record.revoked_at):
                    self._sessions[session.token_hash] = session
        except (OSError, ValueError, TypeError):
            # A corrupt store signs phones out; it never blocks server start.
            self._pairings.clear(); self._by_token.clear(); self._sessions.clear()

    def _save(self) -> None:
        if not self.state_path:
            return
        from dataclasses import asdict
        live = {session.pairing_id for session in self._sessions.values()}
        data = {
            "format": "ark-mobile-sessions", "version": 1,
            "pairings": [asdict(record) for record in self._pairings.values()
                         if record.claimed_at and (record.id in live or record.revoked_at)],
            "sessions": [asdict(session) for session in self._sessions.values()],
        }
        try:
            self.state_path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.state_path.with_suffix(".tmp")
            tmp.write_text(json.dumps(data), encoding="utf-8")
            try:
                os.chmod(tmp, 0o600)
            except OSError:
                pass
            os.replace(tmp, self.state_path)
        except OSError:
            pass

    @staticmethod
    def _session_ttl(value) -> int:
        if value is None or value == "":
            return SESSION_PERMANENT
        if str(value).strip().lower().replace("-", "_") in PERMANENT_WORDS:
            return SESSION_PERMANENT
        try:
            ttl = int(value)
        except (TypeError, ValueError) as exc:
            raise MobileError(400, "session_ttl must be seconds or 'permanent'",
                              "INVALID_SESSION_TTL") from exc
        return max(300, min(SESSION_TTL_MAX, ttl))

    # ------------------------------------------------------------- rate limit
    def _rate(self, key: str, limit: int, window: float) -> None:
        now = self.clock()
        bucket = self._attempts.setdefault(key, deque())
        while bucket and now - bucket[0] >= window:
            bucket.popleft()
        if len(bucket) >= limit:
            raise MobileError(
                429,
                "Too many pairing attempts. Wait a minute and try again.",
                "RATE_LIMITED",
            )
        bucket.append(now)

    def _cleanup(self) -> None:
        now = self.clock()
        stale = [key for key, record in self._pairings.items()
                 if record.expires_at < now - 3600 and record.claimed_at is None]
        for key in stale:
            record = self._pairings.pop(key, None)
            if record:
                self._by_token.pop(record.token_hash, None)
        expired = [key for key, session in self._sessions.items()
                   if session.expires_at < now - 3600]
        for key in expired:
            self._sessions.pop(key, None)

    @staticmethod
    def _permission(value: str) -> str:
        permission = str(value or DEFAULT_PERMISSION).strip().lower().replace("-", "_")
        if permission not in PERMISSIONS:
            raise MobileError(400, "permission must be read_only or full",
                              "INVALID_PERMISSION")
        return permission

    # ------------------------------------------------------------------ create
    def create(self, project: dict, *, permission: str = DEFAULT_PERMISSION,
               ttl_seconds: int | None = None, client: str = "",
               user: str = "", session_ttl=None, source: str = "qr") -> dict:
        with self._lock:
            self._cleanup()
            self._rate(f"create:{client or 'local'}", self.create_limit, self.create_window)
            permission = self._permission(permission)
            session_seconds = self._session_ttl(session_ttl)
            try:
                ttl = int(ttl_seconds) if ttl_seconds is not None else self.pairing_ttl
            except (TypeError, ValueError) as exc:
                raise MobileError(400, "expires_in must be a number", "INVALID_TTL") from exc
            ttl = max(30, min(PAIRING_TTL_MAX, ttl))
            now = self.clock()
            token = "arkp_" + secrets.token_urlsafe(32)
            token_hash = _sha256(token)
            pair_id = "pair_" + secrets.token_hex(6)
            record = PairingRecord(
                id=pair_id,
                token_hash=token_hash,
                project_id=str(project.get("id") or ""),
                project_name=str(project.get("name") or "Project"),
                permission=permission,
                created_at=now,
                expires_at=now + ttl,
                user=str(user or ""),
                session_ttl=session_seconds,
                source="password" if source == "password" else "qr",
            )
            self._pairings[pair_id] = record
            self._by_token[token_hash] = pair_id
            return {
                "id": pair_id,
                "token": token,
                "permission": permission,
                "project_id": record.project_id,
                "project_name": record.project_name,
                "user": record.user,
                "created_at": _iso(record.created_at),
                "expires_at": _iso(record.expires_at),
                "expires_in": ttl,
                "session_permanent": session_seconds == SESSION_PERMANENT,
                "session_ttl": session_seconds or None,
            }

    # ------------------------------------------------------------------- peek
    def find(self, token: str) -> PairingRecord | None:
        """Resolve a pairing code without claiming it.

        Used by the server to learn which project/user the code is bound to
        before it builds request context.  Returns ``None`` for unknown codes,
        never a distinguishable error, so the claim endpoint stays uniform.
        """
        raw = str(token or "").strip()
        if not raw or len(raw) > 200:
            return None
        with self._lock:
            pair_id = self._by_token.get(_sha256(raw))
            record = self._pairings.get(pair_id) if pair_id else None
            if not record:
                return None
            if not hmac.compare_digest(record.token_hash, _sha256(raw)):
                return None
            return record

    # ------------------------------------------------------------------- claim
    def claim(self, token: str, project: dict, *, client: str = "",
              user_agent: str = "") -> dict:
        with self._lock:
            self._cleanup()
            self._rate(f"claim:{client or 'local'}", self.claim_limit, self.claim_window)
            raw = str(token or "").strip()
            if not raw or len(raw) > 200:
                raise MobileError(401, "The pairing code is invalid or has expired.",
                                  "PAIRING_INVALID")
            token_hash = _sha256(raw)
            pair_id = self._by_token.get(token_hash)
            record = self._pairings.get(pair_id) if pair_id else None
            # Constant-time compare even though the lookup is a dict.
            if not record or not hmac.compare_digest(record.token_hash, token_hash):
                raise MobileError(401, "The pairing code is invalid or has expired.",
                                  "PAIRING_INVALID")
            now = self.clock()
            if record.revoked_at:
                raise MobileError(401, "This pairing session was revoked.",
                                  "PAIRING_REVOKED")
            if record.claimed_at:
                raise MobileError(401, "This pairing code has already been used.",
                                  "PAIRING_USED")
            if record.expires_at <= now:
                raise MobileError(401, "The pairing code has expired.",
                                  "PAIRING_EXPIRED")
            if str(project.get("id") or "") != record.project_id:
                raise MobileError(
                    403,
                    "This pairing code belongs to a different project. "
                    "A code is only valid for the project that created it.",
                    "MOBILE_WRONG_PROJECT",
                )
            record.claimed_at = now
            session_seconds = record.session_ttl
            lifetime = PERMANENT_HORIZON if session_seconds == SESSION_PERMANENT else session_seconds
            session_token = "arkm_" + secrets.token_urlsafe(32)
            session_hash = _sha256(session_token)
            session = MobileSession(
                token_hash=session_hash,
                pairing_id=record.id,
                project_id=record.project_id,
                project_name=record.project_name,
                permission=record.permission,
                created_at=now,
                expires_at=now + lifetime,
                last_seen_at=now,
                user_agent=str(user_agent or "")[:200],
                user=record.user,
            )
            self._sessions[session_hash] = session
            self._save()
            return {
                "mobile_token": session_token,
                "token_type": "X-ARK-Mobile-Token",
                "permission": session.permission,
                "read_only": session.permission == "read_only",
                "project": record.project_name,
                "user": record.user,
                "expires_at": None if session_seconds == SESSION_PERMANENT else _iso(session.expires_at),
                "expires_in": None if session_seconds == SESSION_PERMANENT else session_seconds,
                "permanent": session_seconds == SESSION_PERMANENT,
                "pairing_id": record.id,
            }

    # ------------------------------------------------------------- authenticate
    def authenticate(self, token: str, project: dict, *, method: str = "GET",
                     path: str = "") -> MobileSession:
        raw = str(token or "").strip()
        if not raw:
            raise MobileError(401, "Pairing is required for this device.",
                              "MOBILE_TOKEN_REQUIRED")
        session = self._sessions.get(_sha256(raw))
        if not session:
            raise MobileError(401, "The mobile session is invalid or has expired.",
                              "MOBILE_TOKEN_INVALID")
        now = self.clock()
        if session.expires_at <= now:
            self._sessions.pop(session.token_hash, None)
            raise MobileError(401, "The mobile session has expired. Pair again.",
                              "MOBILE_TOKEN_EXPIRED")
        record = self._pairings.get(session.pairing_id)
        if record and record.revoked_at:
            self._sessions.pop(session.token_hash, None)
            raise MobileError(401, "This mobile session was revoked.", "MOBILE_TOKEN_REVOKED")
        if str(project.get("id") or "") != session.project_id:
            raise MobileError(
                403,
                "This phone is paired with a different project. "
                "Switch the desktop back or pair again.",
                "MOBILE_WRONG_PROJECT",
            )
        verb = str(method or "GET").upper()
        # Switching projects is session management, not a project write: a
        # read-only password session may still follow its account.
        if session.permission == "read_only" and verb not in {"GET", "HEAD"}                 and path != "/api/mobile/session/project":
            raise MobileError(
                403,
                "This phone has read-only access. Generate a full-access pairing "
                "code on the desktop to allow edits.",
                "MOBILE_READ_ONLY",
            )
        session.last_seen_at = now
        return session

    def session_for_token(self, token: str) -> MobileSession | None:
        """Return a live session without raising (used by the status endpoint)."""
        raw = str(token or "").strip()
        if not raw:
            return None
        session = self._sessions.get(_sha256(raw))
        if not session:
            return None
        if session.expires_at <= self.clock():
            self._sessions.pop(session.token_hash, None)
            return None
        record = self._pairings.get(session.pairing_id)
        if record and record.revoked_at:
            self._sessions.pop(session.token_hash, None)
            return None
        return session

    def pairing_for(self, session: MobileSession | None) -> PairingRecord | None:
        if session is None:
            return None
        return self._pairings.get(session.pairing_id)

    def rebind(self, token: str, project: dict, *, permission: str) -> MobileSession:
        """Move a password-signed-in device session to another project.

        Only sessions minted by ``POST /api/mobile/login`` may switch: the
        caller proved the account password, so the device may follow the
        account across projects it can read.  QR-paired sessions stay pinned to
        the project their code was issued for.  The caller checks the account's
        permission on ``project`` and passes the effective device permission.
        """
        with self._lock:
            session = self.session_for_token(token)
            if session is None:
                raise MobileError(401, "The mobile session is invalid or has expired.",
                                  "MOBILE_TOKEN_INVALID")
            record = self._pairings.get(session.pairing_id)
            if record is None or record.source != "password":
                raise MobileError(
                    403,
                    "This phone was paired with a QR code for one project. "
                    "Scan a new code from the other project instead.",
                    "MOBILE_PROJECT_PINNED",
                )
            permission = self._permission(permission)
            project_id = str(project.get("id") or "")
            project_name = str(project.get("name") or "Project")
            record.project_id = session.project_id = project_id
            record.project_name = session.project_name = project_name
            record.permission = session.permission = permission
            self._save()
            return session

    # ------------------------------------------------------------------ revoke
    def revoke(self, pair_id: str, project: dict) -> dict:
        with self._lock:
            record = self._pairings.get(str(pair_id or ""))
            if not record or record.project_id != str(project.get("id") or ""):
                raise MobileError(404, "Pairing session not found", "PAIRING_NOT_FOUND")
            if not record.revoked_at:
                record.revoked_at = self.clock()
            for key, session in list(self._sessions.items()):
                if session.pairing_id == record.id:
                    self._sessions.pop(key, None)
            self._save()
            return record.public()

    def revoke_session(self, token: str) -> bool:
        raw = str(token or "").strip()
        if not raw:
            return False
        with self._lock:
            removed = self._sessions.pop(_sha256(raw), None) is not None
            if removed:
                self._save()
            return removed

    def revoke_user(self, username: str, *, except_token: str = "") -> int:
        """Drop every paired-device session bound to one account.

        Used after a password change: a phone paired (or password-signed-in)
        by that account must sign in again.  ``except_token`` keeps the raw
        token of the device that made the change, if it was a device.
        """
        # Exact match: usernames are case-sensitive (like AuthManager.revoke_user).
        target = str(username or "").strip()
        if not target:
            return 0
        keep = _sha256(str(except_token).strip()) if str(except_token or "").strip() else ""
        removed = 0
        with self._lock:
            for key, session in list(self._sessions.items()):
                if key != keep and str(session.user or "").strip() == target:
                    self._sessions.pop(key, None)
                    removed += 1
            if removed:
                self._save()
        return removed

    def list(self, project: dict) -> list[dict]:
        project_id = str(project.get("id") or "")
        records = [record.public() for record in self._pairings.values()
                   if record.project_id == project_id]
        records.sort(key=lambda item: item["created_at"], reverse=True)
        return records

    def project_devices(self, project_id: str) -> list[dict]:
        """Claimed, unrevoked phones of a project with their last request.

        Read-only; used by the resources graph (personal devices)."""
        with self._lock:
            last_seen: dict[str, float] = {}
            agents: dict[str, str] = {}
            for session in self._sessions.values():
                if session.last_seen_at >= last_seen.get(session.pairing_id, 0):
                    last_seen[session.pairing_id] = session.last_seen_at
                    agents[session.pairing_id] = session.user_agent or ""
            out = []
            for record in self._pairings.values():
                if record.project_id != str(project_id or "") or record.revoked_at or not record.claimed_at:
                    continue
                agent = agents.get(record.id, "").lower()
                label = ("iPhone" if "iphone" in agent else "iPad" if "ipad" in agent
                         else "Android phone" if "android" in agent else "Phone")
                out.append({"id": record.id, "user": record.user, "label": label,
                            "permission": record.permission, "source": record.source,
                            "claimed_at": record.claimed_at,
                            "last_seen_at": last_seen.get(record.id)})
            return out

    def pending_count(self, project: dict) -> int:
        project_id = str(project.get("id") or "")
        now = self.clock()
        return sum(1 for record in self._pairings.values()
                   if record.project_id == project_id and not record.revoked_at
                   and not record.claimed_at and record.expires_at > now)


# ----------------------------------------------------------------- connection
def connection_info(app, host_header: str = "", *, request_scheme: str = "") -> dict:
    """Describe honest, reachable URLs for this ARK server.

    Selection order is: explicit public URL (``ARK_PUBLIC_URL``/``--public-url``)
    -> explicit public IP -> the request's non-loopback Host -> detected LAN
    address -> an opt-in public-IP lookup -> loopback-only with the exact SSH
    command to run elsewhere.

    A detected address is not a connectivity promise: NAT, firewall and port
    forwarding are outside ARK's knowledge and the UI says so.
    """
    bind_host = str(getattr(app, "bind_host", "127.0.0.1") or "127.0.0.1")
    port = int(getattr(app, "port", 0) or 0)
    public_url = str(getattr(app, "public_url", "") or "").strip()
    expose = bool(getattr(app, "expose", False))
    loopback_bind = is_loopback_host(bind_host)
    lan_addresses = [] if loopback_bind else _private_ipv4()
    lan_urls = [f"http://{ip}:{port}/" for ip in lan_addresses] if port else []
    host_from_request, request_port = _host_header_port(host_header, port)
    request_url = ""
    if host_header and not is_loopback_host(host_from_request) and not _is_unspecified(host_from_request):
        try:
            request_url = _base_url(host_from_request, request_scheme or "http",
                                    request_port)
        except MobileError:
            request_url = ""

    explicit_public = str(getattr(app, "public_ip", "") or "").strip().strip("[]")
    try:
        ipaddress.ip_address(explicit_public)
    except ValueError:
        explicit_public = ""
    detection = detect_public_ip(
        enabled=bool(getattr(app, "detect_public_ip", False))
        and not loopback_bind and not public_url and not explicit_public)
    candidates = []
    if not loopback_bind or public_url:
        candidates = _pairing_candidates(
            app, request_host=host_from_request if request_url else "",
            request_scheme=request_scheme, public_detection=detection)
    candidate_urls = [url for url, _source in candidates]

    if public_url:
        mode = "public"
        advertised = [public_url]
    elif expose and not loopback_bind:
        mode = "public"
        advertised = candidate_urls
    elif not loopback_bind:
        # Even when interface enumeration fails, do not misreport a public bind
        # as loopback-only: the desktop can still enter the LAN address itself.
        mode = "lan"
        advertised = lan_urls
    else:
        mode = "loopback"
        advertised = []

    tunnel_port = int(os.environ.get("ARK_TUNNEL_PORT") or (port + 1 if port else 8891))
    ssh_target = (os.environ.get("ARK_SSH_TARGET") or "").strip() or "user@server"
    command = tunnel_command(local_port=tunnel_port, ark_port=port or 8890,
                             ssh_target=ssh_target)
    auto_base = candidates[0][0] if candidates else ""
    auto_source = candidates[0][1] if candidates else ""
    if mode == "loopback":
        auto_base, auto_source = "", "loopback"
    auth = _auth_info(app)
    detected_ip = lan_addresses[0] if lan_addresses else ""
    return {
        "mode": mode,
        "expose": expose,
        "loopback_only": mode == "loopback",
        "bind_host": bind_host,
        "port": port,
        "public_url": public_url or None,
        "advertised_urls": advertised,
        "candidate_urls": candidate_urls,
        "lan_urls": lan_urls,
        "request_url": request_url,
        "detected_ip": detected_ip,
        "detected_port": port,
        "detected_public_ip": detection.get("ip", "") if detection else "",
        "detected_public_ip_source": detection.get("source", "") if detection else "",
        "detected_public_ip_error": detection.get("error", "") if detection else "",
        "public_ip": explicit_public or (detection.get("ip", "") if detection else ""),
        "public_ip_source": (
            "configured" if explicit_public
            else (detection.get("source", "") if detection and detection.get("ip") else "")
        ),
        "detect_public_ip_enabled": bool(getattr(app, "detect_public_ip", False)),
        "pairing_auto": bool(mode != "loopback" and auto_base),
        "pairing_base": auto_base or None,
        "pairing_source": auto_source,
        "auth_required": auth["required"],
        "auth_user": auth["user"],
        "auth_note": auth["note"],
        "tunnel": {
            "required": mode == "loopback",
            "command": shlex.join(command),
            "argv": command,
            "ssh_target": ssh_target,
            "local_port": tunnel_port,
            "ark_port": port or 8890,
            "bind": "0.0.0.0",
            "helper": ("python -m arkstudio mobile-tunnel "
                       f"--ssh {ssh_target} --local-port {tunnel_port} "
                       f"--ark-port {port or 8890}"),
            "warning": (
                "Run this on a phone-reachable computer, not on the phone. "
                "It listens on every interface; use a trusted LAN, keep the "
                "pairing read-only unless you need writes, and stop it afterwards."
            ),
        },
        "https": bool(public_url.startswith("https://")) if public_url else False,
        "secure_context": bool(
            public_url.startswith("https://") if public_url
            else (mode == "loopback")
        ),
        "secure_context_note": (
            "The desktop loopback URL is a secure context; a phone-facing LAN "
            "HTTP URL is not, so install/offline caching may be unavailable."
        ),
        "pwa_note": (
            "Install and offline caching require a secure context (HTTPS or "
            "localhost). Plain HTTP on a LAN may load the app but the service "
            "worker and install prompt can be unavailable."
        ),
        "reachability_note": (
            "Addresses are detected from configuration, the request or this "
            "machine's interfaces. NAT, port forwarding and firewall rules are "
            "unknown to ARK; test the URL from the phone before relying on it."
        ),
    }


def resolve_pairing_base(app, *, override_host: str = "", override_port=None,
                         override_base: str = "",
                         request_host: str = "", request_port=None,
                         request_scheme: str = "") -> tuple[str, str]:
    """Return ``(base_url, source)`` for the QR code.

    An explicit, validated public URL always wins.  Otherwise the desktop may
    enter the tunnel laptop's host/port (loopback mode); in public/LAN mode the
    request Host and detected addresses are used automatically so Settings
    never needs manual host/port fields.
    """
    public_url = str(getattr(app, "public_url", "") or "").strip()
    if public_url:
        return normalize_public_url(public_url), "public_url"
    if str(override_base or "").strip():
        return normalize_public_url(override_base), "override_base"
    host = str(override_host or "").strip()
    if host:
        host = validate_tunnel_host(host)
        info = connection_info(app)
        default_port = int(info["tunnel"]["local_port"] or getattr(app, "port", 0) or 0)
        port = validate_port(override_port, name="tunnel port", default=default_port)
        return _base_url(host, "http", port), "tunnel_target"
    info = connection_info(app, _host_header(request_host, request_port),
                           request_scheme=request_scheme)
    if info["pairing_base"]:
        return info["pairing_base"], info["pairing_source"]
    if info["mode"] in {"public", "lan"}:
        return "", "lan_unresolved"
    return "", "loopback"


def _host_header(host: str, port=None) -> str:
    text = str(host or "").strip()
    if not text:
        return ""
    if text.startswith("[") and text.endswith("]"):
        inner = text[1:-1]
        return f"[{inner}]:{port}" if port else text
    if ":" in text:
        # Bare IPv6 from urlparse.hostname.
        return f"[{text}]:{port}" if port else f"[{text}]"
    return f"{text}:{port}" if port else text
