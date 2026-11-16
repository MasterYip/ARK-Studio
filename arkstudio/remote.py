"""Remote ARK project over SSH: local tunnel plus optional sshfs mirror."""
from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
import time
import urllib.request
import webbrowser
from pathlib import Path


def _healthy(url: str) -> bool:
    try:
        with urllib.request.urlopen(url.rstrip("/") + "/api/health", timeout=1.5) as response:
            return response.status == 200
    except OSError:
        return False


def run(*, ssh_host: str, user: str = "", ssh_port: int = 22,
        remote_port: int = 8890, local_port: int = 8890,
        identity: str = "", remote_path: str = "", mount: str = "",
        open_browser: bool = True) -> int:
    if not ssh_host:
        print("--ssh host is required", file=sys.stderr)
        return 2
    target = f"{user}@{ssh_host}" if user else ssh_host
    ssh = shutil.which("ssh")
    if not ssh:
        print("OpenSSH client (ssh) is not available", file=sys.stderr)
        return 2
    command = [ssh, "-N", "-p", str(ssh_port),
               "-L", f"127.0.0.1:{local_port}:127.0.0.1:{remote_port}"]
    if identity:
        command += ["-i", str(Path(identity).expanduser())]
    command.append(target)
    tunnel = subprocess.Popen(command)
    mirror = None
    url = f"http://127.0.0.1:{local_port}/"
    try:
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            if tunnel.poll() is not None:
                print("SSH tunnel exited before the ARK server became reachable", file=sys.stderr)
                return 1
            if _healthy(url):
                break
            time.sleep(0.3)
        else:
            print(f"ARK server did not answer at {url}", file=sys.stderr)
            return 1
        if mount and remote_path:
            sshfs = shutil.which("sshfs")
            if sshfs:
                Path(mount).expanduser().mkdir(parents=True, exist_ok=True)
                mirror = subprocess.Popen([sshfs, f"{target}:{remote_path}", str(Path(mount).expanduser())])
                print(f"Mirrored {remote_path} at {mount} (sshfs pid {mirror.pid})")
            else:
                print("sshfs is not installed; skipping the optional folder mirror", file=sys.stderr)
        print(f"ARK remote project: {url}")
        print("Press Ctrl+C to close the tunnel.")
        if open_browser:
            webbrowser.open(url)
        while True:
            if tunnel.poll() is not None:
                return 0
            time.sleep(0.5)
    except KeyboardInterrupt:
        return 0
    finally:
        if mirror and mirror.poll() is None:
            mirror.terminate()
        if tunnel.poll() is None:
            tunnel.terminate()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="arkstudio remote")
    parser.add_argument("--ssh", required=True, help="SSH host")
    parser.add_argument("--user", default="")
    parser.add_argument("--ssh-port", type=int, default=22)
    parser.add_argument("--remote-port", type=int, default=8890)
    parser.add_argument("--local-port", type=int, default=8890)
    parser.add_argument("--identity", default="")
    parser.add_argument("--remote-path", default="", help="remote ARK project folder for sshfs")
    parser.add_argument("--mount", default="", help="local temp folder for the optional sshfs mirror")
    parser.add_argument("--no-open", action="store_true")
    args = parser.parse_args(argv)
    return run(ssh_host=args.ssh, user=args.user, ssh_port=args.ssh_port,
               remote_port=args.remote_port, local_port=args.local_port,
               identity=args.identity, remote_path=args.remote_path,
               mount=args.mount, open_browser=not args.no_open)
