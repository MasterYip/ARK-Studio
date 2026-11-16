#!/usr/bin/env python3
"""Capture the README/doc screenshots and the demo images from a fresh demo.

    cd go && go generate ./... && go build -o ark-server ./cmd/ark-server && cd ..
    python scripts/dev/capture_screenshots.py --server go/ark-server --out ./output/shots
    python scripts/dev/capture_screenshots.py --server go/ark-server --write

Starts ``ark-server serve`` on a new temporary root (local mode, starter demo
projects, user "Local user"), drives headless Edge/Chrome over the DevTools
protocol at 1440x900, light theme, English, guided tour dismissed, and saves:

    docs/screenshots/readme-home.png    Home of the PredActor demo
    docs/screenshots/readme-map.png     Map, force graph
    docs/screenshots/readme-editor.png  Editor, split view of a demo node
    demo-assets/workbench-overview.png  Home, dark theme (attached by the demo seed)
    demo-assets/mind-graph.png          Map, mind layout, dark theme (attached by the seed)

``--write`` saves into the repository (run it twice: the demo seed attaches the
two demo-assets images, so the second run shows the new ones in previews).
Otherwise files go to ``--out``. Set ARK_EDGE_PATH for a browser elsewhere.
No external network is used.
"""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request
from urllib.parse import quote, urlparse

ROOT = Path(__file__).resolve().parents[2]
WIDTH, HEIGHT = 1440, 900


class CdpError(RuntimeError):
    pass


class WebSocket:
    """Minimal RFC 6455 text-frame client (no dependencies)."""

    def __init__(self, url: str, timeout: float = 30.0):
        parsed = urlparse(url)
        self.sock = socket.create_connection((parsed.hostname, parsed.port or 80), timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        path = parsed.path + ("?" + parsed.query if parsed.query else "")
        self.sock.sendall((f"GET {path} HTTP/1.1\r\nHost: {parsed.hostname}:{parsed.port}\r\n"
                           "Upgrade: websocket\r\nConnection: Upgrade\r\n"
                           f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode("ascii"))
        response = b""
        while b"\r\n\r\n" not in response:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise CdpError("WebSocket handshake closed early")
            response += chunk
        header, _, self.buffer = response.partition(b"\r\n\r\n")
        if b" 101 " not in header.split(b"\r\n", 1)[0]:
            raise CdpError("WebSocket handshake failed")

    def send(self, text: str) -> None:
        payload = text.encode("utf-8")
        header = bytearray([0x81])
        if len(payload) < 126:
            header.append(0x80 | len(payload))
        elif len(payload) < 65536:
            header += bytes([0x80 | 126]) + struct.pack(">H", len(payload))
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", len(payload))
        mask = os.urandom(4)
        self.sock.sendall(bytes(header) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))

    def exact(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(max(65536, count - len(self.buffer)))
            if not chunk:
                raise CdpError("WebSocket closed")
            self.buffer += chunk
        data, self.buffer = self.buffer[:count], self.buffer[count:]
        return data

    def recv(self) -> str:
        fragments = bytearray()
        while True:
            first, second = self.exact(2)
            length = second & 0x7F
            if length == 126:
                length = struct.unpack(">H", self.exact(2))[0]
            elif length == 127:
                length = struct.unpack(">Q", self.exact(8))[0]
            payload = self.exact(length)
            opcode = first & 0x0F
            if opcode == 0x8:
                raise CdpError("WebSocket closed")
            if opcode in (0x1, 0x2):
                fragments = bytearray(payload)
            elif opcode == 0x0:
                fragments += payload
            else:
                continue
            if first & 0x80:
                return fragments.decode("utf-8", "replace")


class Page:
    def __init__(self, ws_url: str):
        self.ws = WebSocket(ws_url)
        self.next_id = 0

    def call(self, method: str, params: dict | None = None) -> dict:
        self.next_id += 1
        self.ws.send(json.dumps({"id": self.next_id, "method": method, "params": params or {}}))
        while True:
            message = json.loads(self.ws.recv())
            if message.get("id") == self.next_id:
                if "error" in message:
                    raise CdpError(f"{method}: {message['error']}")
                return message.get("result") or {}

    def js(self, expression: str):
        result = self.call("Runtime.evaluate", {"expression": expression, "returnByValue": True, "awaitPromise": True})
        if result.get("exceptionDetails"):
            raise CdpError(f"JS failed: {result['exceptionDetails'].get('text')}: {expression[:200]}")
        return result.get("result", {}).get("value")

    def wait(self, expression: str, timeout: float = 30.0):
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                value = self.js(expression)
            except CdpError:
                value = None
            if value:
                return value
            time.sleep(0.2)
        raise CdpError("timed out waiting for " + expression[:200])

    def goto(self, url: str) -> None:
        self.call("Page.navigate", {"url": url})
        time.sleep(0.5)
        self.wait("document.readyState === 'complete'")

    def shot(self, path: Path) -> None:
        data = self.call("Page.captureScreenshot", {"format": "png", "captureBeyondViewport": False})
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(base64.b64decode(data["data"]))
        print("wrote", path)


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def find_browser() -> str:
    for candidate in (os.environ.get("ARK_EDGE_PATH", ""),
                      r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
                      r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
                      shutil.which("msedge") or "", shutil.which("microsoft-edge") or "",
                      shutil.which("google-chrome") or "", shutil.which("chromium") or ""):
        if candidate and Path(candidate).is_file():
            return candidate
    raise SystemExit("No Edge/Chrome found; set ARK_EDGE_PATH")


def get_json(url: str):
    with urllib.request.urlopen(url, timeout=15) as response:
        return json.loads(response.read())


def wait_http(url: str, timeout: float = 30.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            get_json(url)
            return
        except OSError:
            time.sleep(0.2)
    raise SystemExit("server did not start: " + url)


SETTLE = 2.5


def prepare(page: Page, base: str, project: str, theme: str) -> None:
    """Theme, language and dismissed tour before the app boots; one project per tab."""
    page.goto(base + "/api/health")
    page.js(f"""(() => {{
      localStorage.clear();
      for (const [k, v] of Object.entries({{"ark.theme": {json.dumps(theme)}, "ark.themePref": {json.dumps(theme)},
          "ark.loginTheme": {json.dumps(theme)}, "ark.lang": "en"}})) localStorage.setItem(k, v);
      for (const user of ["local", "Local user", "local-user"])
        localStorage.setItem("ark.guidance.v3:" + encodeURIComponent(user), JSON.stringify({{status: "dismissed", step: 0}}));
      sessionStorage.setItem("ark.tabProject", {json.dumps(project)});
      return true; }})()""")
    # Opening materializes the per-visit tutorial copy and makes it current.
    request = urllib.request.Request(base + "/api/projects/open", method="POST", data=json.dumps({"id": project}).encode(),
                                     headers={"Content-Type": "application/json", "Origin": base})
    with urllib.request.urlopen(request, timeout=30) as response:
        response.read()


def open_view(page: Page, base: str, hash_: str) -> None:
    page.goto(base + "/" + hash_)
    page.wait("document.querySelector('.topbar, header, nav') && !document.querySelector('.boot, .boot-screen')", 40)
    time.sleep(SETTLE)
    # A first-visit invitation or starter popover must not cover the view.
    page.js("""(() => { document.querySelectorAll('.guidance-layer, .guidance-invite, .toast').forEach((e) => e.remove()); return true; })()""")
    time.sleep(0.3)


def capture(server: Path, out: dict[str, Path], keep: bool) -> None:
    temp = Path(tempfile.mkdtemp(prefix="ark-shots-"))
    port, debug = free_port(), free_port()
    base = f"http://127.0.0.1:{port}"
    env = dict(os.environ, ARK_WEB_ROOT=str(ROOT / "web"))
    env.pop("ARK_PUBLIC", None)
    serve = subprocess.Popen([str(server), "serve", "--root", str(temp / "data" / "projects"), "--port", str(port)],
                             cwd=temp, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    browser = None
    try:
        wait_http(base + "/api/health")
        projects = get_json(base + "/api/projects")
        items = projects.get("projects", projects) if isinstance(projects, dict) else projects
        ids = {p.get("name") or p.get("title"): p.get("id") or p.get("slug") for p in items}
        pred = next(v for k, v in ids.items() if k and "PredActor" in k)
        browser = subprocess.Popen([find_browser(), "--headless=new", "--disable-gpu", "--no-first-run",
                                    "--no-default-browser-check", "--disable-extensions", "--hide-scrollbars",
                                    f"--window-size={WIDTH},{HEIGHT}", f"--user-data-dir={temp / 'profile'}",
                                    f"--remote-debugging-port={debug}", "about:blank"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + 30
        while True:
            try:
                request = urllib.request.Request(f"http://127.0.0.1:{debug}/json/new?{quote('about:blank', safe='')}", method="PUT")
                with urllib.request.urlopen(request, timeout=10) as response:
                    target = json.loads(response.read())
                break
            except OSError:
                if time.time() > deadline:
                    raise
                time.sleep(0.3)
        page = Page(target["webSocketDebuggerUrl"])
        page.call("Page.enable")
        page.call("Emulation.setDeviceMetricsOverride", {"width": WIDTH, "height": HEIGHT, "deviceScaleFactor": 1, "mobile": False})
        for name, (project, theme, hash_, action) in SHOTS.items():
            if name not in out:
                continue
            prepare(page, base, pred if project == "predactor" else project, theme)
            open_view(page, base, hash_)
            if action:
                try:
                    action(page)
                except CdpError:
                    page.shot(out[name].with_name(out[name].stem + "-failed.png"))
                    raise
            page.shot(out[name])
    finally:
        if browser:
            browser.terminate()
        serve.terminate()
        try:
            serve.wait(10)
            if browser:
                browser.wait(10)
        except subprocess.TimeoutExpired:
            pass
        if not keep:
            shutil.rmtree(temp, ignore_errors=True)


def click_text(selector: str, text: str) -> str:
    return f"""(() => {{ const el = [...document.querySelectorAll({json.dumps(selector)})]
      .find((e) => e.offsetParent && e.textContent.trim() === {json.dumps(text)});
      if (el) el.click(); return !!el; }})()"""


def hide_side_panels(page: Page) -> None:
    """Close the workbench's left and right containers (Home reads best full width)."""
    page.js("""(() => { for (const name of ["panel-left", "panel-right"]) {
        const b = document.querySelector(`button.layout-toggle[data-layout="${name}"][aria-pressed="true"]`);
        if (b) b.click(); } return true; })()""")
    time.sleep(1)


def home(page: Page) -> None:
    hide_side_panels(page)


def map_force(page: Page) -> None:
    time.sleep(4)  # let the force layout settle


def map_mind(page: Page) -> None:
    hide_side_panels(page)
    page.js("""(() => { const s = [...document.querySelectorAll('select')].find((e) => [...e.options].some((o) => o.value === 'mind'));
      if (s) { s.value = 'mind'; s.dispatchEvent(new Event('change', {bubbles: true})); } return !!s; })()""")
    time.sleep(4)


def editor_split(page: Page) -> None:
    """The tutorial note: Markdown source on the left, its preview on the right."""
    page.wait("!!document.querySelector('.ed-split-btn:not([disabled])')", 30)
    page.js("document.querySelector('.ed-split-btn').click()")
    time.sleep(1.5)
    page.js("""(() => { const b = document.querySelector('[data-editor-mode="source"]'); if (b) b.click(); return !!b; })()""")
    time.sleep(2)


NOTE = "#/editor/" + quote("ws:assets/tutorial/research/warmup-note.md", safe="")
# name: (project, theme, route, action). "predactor" is the starter demo
# project; "ark-demo" the built-in tutorial project.
SHOTS = {
    "readme-home": ("predactor", "light", "#/home", home),
    "readme-map": ("predactor", "light", "?unit=S-0001#/map", map_force),
    "readme-editor": ("ark-demo", "light", NOTE, editor_split),
    "workbench-overview": ("predactor", "dark", "#/home", home),
    "mind-graph": ("predactor", "dark", "#/map", map_mind),
}
TARGETS = {
    "readme-home": "docs/screenshots/readme-home.png",
    "readme-map": "docs/screenshots/readme-map.png",
    "readme-editor": "docs/screenshots/readme-editor.png",
    "workbench-overview": "demo-assets/workbench-overview.png",
    "mind-graph": "demo-assets/mind-graph.png",
}


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--server", type=Path, required=True, help="ark-server binary built from this checkout")
    parser.add_argument("--out", type=Path, help="folder for the PNGs (default with --write: the repository)")
    parser.add_argument("--write", action="store_true", help="overwrite the repository's screenshots")
    parser.add_argument("--only", nargs="*", choices=sorted(SHOTS), help="capture only these")
    parser.add_argument("--keep", action="store_true", help="keep the temporary server root")
    args = parser.parse_args(argv)
    if not args.write and not args.out:
        parser.error("give --out or --write")
    names = args.only or list(SHOTS)
    out = {n: (ROOT / TARGETS[n] if args.write else args.out / (n + ".png")) for n in names}
    capture(args.server.resolve(), out, args.keep)
    return 0


if __name__ == "__main__":
    sys.exit(main())
