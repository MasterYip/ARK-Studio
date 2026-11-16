"""Optional system-tray desktop shell for ARK Studio.

The ARK server remains the single source of truth. This module only wraps the
local web UI in a frameless native window and adds a tray icon, similar to the
InfoHub desktop shell. It is optional: headless/server installs need nothing
from here.

Run:
    python -m arkstudio desktop
"""
from __future__ import annotations

import ctypes
import json
import os
import socket
import subprocess
import sys
import threading
import time
import urllib.request
import webbrowser
from pathlib import Path

try:
    import webview
    import pystray
    from PIL import Image, ImageDraw
except ImportError as exc:  # pragma: no cover - optional dependency
    webview = None
    pystray = None
    Image = ImageDraw = None
    _IMPORT_ERROR = exc
else:
    _IMPORT_ERROR = None


def configure_taskbar_identity() -> None:
    """Set the AppUserModelID before any window exists, like InfoHub."""
    if os.name != "nt":
        return
    try:
        set_id = ctypes.windll.shell32.SetCurrentProcessExplicitAppUserModelID
        set_id.argtypes = [ctypes.c_wchar_p]
        set_id.restype = ctypes.c_long
        set_id("ARKStudio.Desktop.20261031")
    except Exception:
        pass


configure_taskbar_identity()


def _url(host: str, port: int) -> str:
    return f"http://{host}:{port}/"


def _healthy(url: str) -> bool:
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(url.rstrip("/") + "/api/health", timeout=1.5) as response:
            data = json.loads(response.read())
        return bool(data.get("ok"))
    except (OSError, ValueError):
        return False


class ManagedServer:
    def __init__(self, *, root: str, host: str, port: int):
        self.root = Path(root).resolve()
        self.host = host
        self.port = port
        self.url = _url(host, port)
        self.process = None
        self.owned = False

    def ensure(self) -> None:
        if _healthy(self.url):
            return
        try:
            sock = socket.create_connection((self.host, self.port), timeout=1)
            sock.close()
            raise RuntimeError(f"{self.url} is occupied but is not a healthy ARK server")
        except OSError:
            pass
        self.process = subprocess.Popen(
            [sys.executable, "-m", "arkstudio", "serve", "--root", str(self.root),
             "--host", self.host, "--port", str(self.port)],
            cwd=str(Path(__file__).resolve().parent.parent),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
        )
        self.owned = True
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError("ARK server exited during startup")
            if _healthy(self.url):
                return
            time.sleep(0.2)
        raise RuntimeError("ARK server did not start in time")

    def stop(self) -> None:
        if self.owned and self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)


def _icon_image():
    icon = Path(__file__).resolve().parent.parent / "web" / "brand" / "ark-mark.png"
    try:
        image = Image.open(icon).convert("RGBA")
        return image.resize((64, 64), Image.LANCZOS)
    except (OSError, ValueError):
        image = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
        draw = ImageDraw.Draw(image)
        draw.polygon([(32, 4), (62, 56), (37, 56), (32, 40)], fill="#2563eb")
        draw.polygon([(32, 4), (2, 56), (27, 56), (32, 40)], fill="#1d4ed8")
        return image


def _apply_window_icon(window):
    """Set the Windows taskbar/alt-tab icon from web/brand/ark.ico."""
    if os.name != "nt":
        return
    try:
        from ctypes import wintypes
        native = getattr(window, "native", None)
        handle = getattr(native, "Handle", None)
        hwnd = int(handle.ToInt64()) if hasattr(handle, "ToInt64") else int(handle or 0)
        if not hwnd:
            return
        icon_path = Path(__file__).resolve().parent.parent / "web" / "brand" / "ark.ico"
        user32 = ctypes.windll.user32
        user32.LoadImageW.argtypes = [wintypes.HINSTANCE, wintypes.LPCWSTR, wintypes.UINT,
                                      ctypes.c_int, ctypes.c_int, wintypes.UINT]
        user32.LoadImageW.restype = wintypes.HANDLE
        user32.SendMessageW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM]
        for size, kind in ((16, 0), (32, 1)):
            hicon = user32.LoadImageW(None, str(icon_path), 1, size, size, 0x10)
            if hicon:
                user32.SendMessageW(hwnd, 0x80, kind, hicon)
        try:
            from System import Action
            from System.Drawing import Icon
            form = window.native
            def apply():
                form.Icon = Icon(str(icon_path))
            form.Invoke(Action(apply))
        except Exception:
            pass
    except Exception:
        pass


def _set_taskbar_visible(visible: bool) -> None:
    if os.name != "nt":
        return
    try:
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.FindWindowW.restype = wintypes.HWND
        user32.ShowWindow.argtypes = [wintypes.HWND, ctypes.c_int]
        for name in ("Shell_TrayWnd", "Shell_SecondaryTrayWnd"):
            handle = user32.FindWindowW(name, None)
            if handle:
                user32.ShowWindow(handle, 5 if visible else 0)
    except Exception:
        pass


class DesktopApi:
    def __init__(self, window, tray, on_quit):
        self.window = window
        self.tray = tray
        self.on_quit = on_quit
        self._drag_state = None
        self._resize_state = None
        self._maximized = False
        self._restore_rect = None

    def _hwnd(self):
        if os.name != "nt":
            return 0
        from ctypes import wintypes
        try:
            native = getattr(self.window, "native", None)
            handle = getattr(native, "Handle", None)
            if handle:
                return int(handle.ToInt64()) if hasattr(handle, "ToInt64") else int(handle)
        except Exception:
            pass
        target_title = str(self.window.title or "ARK Studio")
        try:
            user32 = ctypes.windll.user32
            user32.EnumWindows.argtypes = [ctypes.WINFUNCTYPE(ctypes.c_bool, wintypes.HWND, wintypes.LPARAM), wintypes.LPARAM]
            user32.GetWindowTextW.argtypes = [wintypes.HWND, wintypes.LPWSTR, ctypes.c_int]
            user32.IsWindowVisible.argtypes = [wintypes.HWND]
            user32.GetWindowThreadProcessId.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.DWORD)]
            found = [0]
            minimized = [0]
            def visit(hwnd, _):
                if not user32.IsWindowVisible(hwnd):
                    return True
                buffer = ctypes.create_unicode_buffer(256)
                user32.GetWindowTextW(hwnd, buffer, 256)
                title = buffer.value
                if title == target_title or (target_title == "ARK Studio" and "ARK Studio" in title):
                    if user32.IsIconic(hwnd):
                        minimized[0] = int(hwnd)
                        return True
                    found[0] = int(hwnd)
                    return False
                return True
            user32.EnumWindows(ctypes.WINFUNCTYPE(ctypes.c_bool, wintypes.HWND, wintypes.LPARAM)(visit), 0)
            if found[0]:
                return found[0]
            if minimized[0]:
                return minimized[0]
        except Exception:
            pass
        try:
            user32 = ctypes.windll.user32
            user32.FindWindowW.restype = ctypes.c_void_p
            found = user32.FindWindowW(None, target_title)
            if found:
                return int(found)
        except Exception:
            pass
        return 0

    def drag(self):
        hwnd = self._hwnd()
        if not hwnd:
            return
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.SendMessageW.argtypes = [wintypes.HWND, wintypes.UINT, wintypes.WPARAM, wintypes.LPARAM]
        user32.SendMessageW.restype = wintypes.LPARAM
        user32.ReleaseCapture()
        user32.SendMessageW(hwnd, 0xA1, 2, 0)

    def drag_start(self, screen_x=0, screen_y=0):
        hwnd = self._hwnd()
        if not hwnd:
            return False
        if self._maximized:
            self._restore_maximized()
            hwnd = self._hwnd()
            if not hwnd:
                return False
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        rect = wintypes.RECT()
        user32.GetWindowRect.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.RECT)]
        if not user32.GetWindowRect(hwnd, ctypes.byref(rect)):
            return False
        self._drag_state = {
            "hwnd": hwnd, "sx": float(screen_x), "sy": float(screen_y),
            "left": int(rect.left), "top": int(rect.top),
        }
        return True

    def drag_move(self, screen_x=0, screen_y=0):
        state = self._drag_state
        if not state:
            return False
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.SetWindowPos.argtypes = [wintypes.HWND, wintypes.HWND, ctypes.c_int,
                                        ctypes.c_int, ctypes.c_int, ctypes.c_int, wintypes.UINT]
        left = state["left"] + int(float(screen_x) - state["sx"])
        top = state["top"] + int(float(screen_y) - state["sy"])
        user32.SetWindowPos(state["hwnd"], 0, left, top, 0, 0, 0x0001 | 0x0004 | 0x0010)
        return True

    def drag_end(self):
        self._drag_state = None

    def minimize(self):
        self._set_taskbar_visible(True)
        hwnd = self._hwnd()
        if hwnd:
            from ctypes import wintypes
            user32 = ctypes.windll.user32
            user32.ShowWindow.argtypes = [wintypes.HWND, ctypes.c_int]
            user32.ShowWindow(hwnd, 6)
        else:
            try:
                self.window.minimize()
            except Exception:
                pass

    def _set_taskbar_visible(self, visible):
        if os.name != "nt":
            return
        try:
            from ctypes import wintypes
            user32 = ctypes.windll.user32
            user32.FindWindowW.restype = wintypes.HWND
            user32.ShowWindow.argtypes = [wintypes.HWND, ctypes.c_int]
            for name in ("Shell_TrayWnd", "Shell_SecondaryTrayWnd"):
                handle = user32.FindWindowW(name, None)
                if handle:
                    user32.ShowWindow(handle, 5 if visible else 0)
        except Exception:
            pass

    def _window_rect(self, hwnd):
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        rect = wintypes.RECT()
        user32.GetWindowRect.argtypes = [wintypes.HWND, ctypes.POINTER(wintypes.RECT)]
        user32.GetWindowRect(hwnd, ctypes.byref(rect))
        return int(rect.left), int(rect.top), int(rect.right - rect.left), int(rect.bottom - rect.top)

    def _restore_maximized(self):
        if not self._maximized:
            return False
        try:
            from System import Action
            from System.Windows.Forms import FormWindowState
            form = self.window.native
            def perform():
                form.WindowState = FormWindowState.Normal
            form.Invoke(Action(perform))
            self._set_taskbar_visible(True)
            self._maximized = False
            self._restore_rect = None
            return True
        except Exception:
            pass
        hwnd = self._hwnd()
        if not hwnd:
            self._maximized = False
            return False
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.SetWindowPos.argtypes = [wintypes.HWND, wintypes.HWND, ctypes.c_int,
                                        ctypes.c_int, ctypes.c_int, ctypes.c_int, wintypes.UINT]
        left, top, width, height = self._restore_rect or (80, 60, 1280, 820)
        user32.SetWindowPos(hwnd, -2, left, top, width, height, 0x0040)
        self._set_taskbar_visible(True)
        self._maximized = False
        self._restore_rect = None
        return True

    def resize_start(self, edge="", screen_x=0, screen_y=0):
        hwnd = self._hwnd()
        if not hwnd:
            return False
        if self._maximized:
            self._restore_maximized()
        left, top, width, height = self._window_rect(hwnd)
        self._resize_state = {
            "hwnd": hwnd, "edge": str(edge), "sx": float(screen_x), "sy": float(screen_y),
            "left": left, "top": top, "width": width, "height": height,
        }
        return True

    def resize_move(self, screen_x=0, screen_y=0):
        state = self._resize_state
        if not state:
            return False
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.SetWindowPos.argtypes = [wintypes.HWND, wintypes.HWND, ctypes.c_int,
                                        ctypes.c_int, ctypes.c_int, ctypes.c_int, wintypes.UINT]
        edge = state["edge"]
        dx = int(float(screen_x) - state["sx"])
        dy = int(float(screen_y) - state["sy"])
        left, top = state["left"], state["top"]
        width, height = state["width"], state["height"]
        if "w" in edge:
            left += dx
            width -= dx
        if "e" in edge:
            width += dx
        if "n" in edge:
            top += dy
            height -= dy
        if "s" in edge:
            height += dy
        if width < 920:
            if "w" in edge:
                left = state["left"] + state["width"] - 920
            width = 920
        if height < 600:
            if "n" in edge:
                top = state["top"] + state["height"] - 600
            height = 600
        user32.SetWindowPos(state["hwnd"], 0, left, top, width, height, 0x0004 | 0x0010)
        return True

    def resize_end(self):
        self._resize_state = None

    def maximize(self):
        try:
            from System import Action
            from System.Windows.Forms import FormWindowState, Screen
            form = self.window.native
            def perform():
                form.MaximizedBounds = Screen.FromHandle(form.Handle).WorkingArea
                form.WindowState = (FormWindowState.Normal
                                    if form.WindowState == FormWindowState.Maximized
                                    else FormWindowState.Maximized)
                self._maximized = form.WindowState == FormWindowState.Maximized
                self._set_taskbar_visible(True)
            form.Invoke(Action(perform))
            return
        except Exception:
            pass
        hwnd = self._hwnd()
        if not hwnd:
            try:
                self.window.toggle_fullscreen()
            except Exception:
                pass
            return
        from ctypes import wintypes
        user32 = ctypes.windll.user32
        user32.SetWindowPos.argtypes = [wintypes.HWND, wintypes.HWND, ctypes.c_int,
                                        ctypes.c_int, ctypes.c_int, ctypes.c_int, wintypes.UINT]
        if self._maximized:
            self._restore_maximized()
            return
        self._restore_rect = self._window_rect(hwnd)
        class Monitor(ctypes.Structure):
            _fields_ = [("cbSize", wintypes.DWORD), ("rcMonitor", wintypes.RECT),
                        ("rcWork", wintypes.RECT), ("dwFlags", wintypes.DWORD)]
        user32.MonitorFromWindow.argtypes = [wintypes.HWND, wintypes.DWORD]
        user32.MonitorFromWindow.restype = wintypes.HANDLE
        user32.GetMonitorInfoW.argtypes = [wintypes.HANDLE, ctypes.POINTER(Monitor)]
        monitor = Monitor()
        monitor.cbSize = ctypes.sizeof(monitor)
        handle = user32.MonitorFromWindow(hwnd, 2)
        if not user32.GetMonitorInfoW(handle, ctypes.byref(monitor)):
            left, top, width, height = -8, -8, 1936, 1056
        else:
            rect = monitor.rcWork
            left, top = int(rect.left), int(rect.top)
            width, height = int(rect.right - rect.left), int(rect.bottom - rect.top)
        user32.SetWindowPos(hwnd, 0, left, top, width, height, 0x0040)
        self._set_taskbar_visible(True)
        self._maximized = True

    def hide(self):
        self._set_taskbar_visible(True)
        self.window.hide()

    def close(self):
        self._set_taskbar_visible(True)
        self.window.hide()

    def quit(self):
        self._set_taskbar_visible(True)
        self.on_quit()


def _chrome_script() -> str:
    return r"""
(() => {
  if (document.getElementById('ark-desktop-controls')) return;
  const style = document.createElement('style');
  style.textContent = `
    #topbar .top-actions{margin-right:96px !important}
    .entry-brand{-webkit-user-select:none;user-select:none}
    #ark-desktop-controls{position:fixed;right:8px;top:9px;display:flex;align-items:center;gap:2px;height:32px;z-index:2147483647}
    #ark-desktop-controls button{width:28px;height:28px;border:1px solid transparent;border-radius:6px;background:transparent;color:var(--sub);font:13px/1 system-ui;cursor:pointer;padding:0}
    #ark-desktop-controls button:hover{background:var(--card);border-color:var(--line);color:var(--ink)}
    #ark-desktop-controls .ark-close:hover{background:#dc2626;border-color:#dc2626;color:#fff}
    .ark-resize{position:fixed;z-index:2147483646;background:transparent}
    .ark-resize.n{left:9px;right:9px;top:0;height:5px;cursor:ns-resize}
    .ark-resize.s{left:9px;right:9px;bottom:0;height:5px;cursor:ns-resize}
    .ark-resize.w{top:9px;bottom:9px;left:0;width:5px;cursor:ew-resize}
    .ark-resize.e{top:9px;bottom:9px;right:0;width:5px;cursor:ew-resize}
    .ark-resize.nw{left:0;top:0;width:10px;height:10px;cursor:nwse-resize}
    .ark-resize.ne{right:0;top:0;width:10px;height:10px;cursor:nesw-resize}
    .ark-resize.sw{left:0;bottom:0;width:10px;height:10px;cursor:nesw-resize}
    .ark-resize.se{right:0;bottom:0;width:10px;height:10px;cursor:nwse-resize}
  `;
  document.head.appendChild(style);
  let windowDrag = null;
  document.addEventListener('pointerdown', (event) => {
    const target = event.target;
    if (!target.closest?.('#topbar,.entry-brand,#ark-startup,.login-body')) return;
    if (target.closest('button,a,input,select,textarea,[role="button"],[contenteditable]:not([contenteditable="false"]),.login-shell,#ark-desktop-controls,.ark-resize')) return;
    if (event.button !== 0) return;
    event.preventDefault();
    windowDrag = { id: event.pointerId, startX: event.screenX, startY: event.screenY, moved: false };
    try { event.target.setPointerCapture?.(event.pointerId); } catch (e) {}
  }, true);
  document.addEventListener('pointermove', (event) => {
    if (!windowDrag || windowDrag.id !== event.pointerId) return;
    if (!windowDrag.moved) {
      if (Math.abs(event.screenX - windowDrag.startX) + Math.abs(event.screenY - windowDrag.startY) < 4) return;
      windowDrag.moved = true;
      window.pywebview?.api?.drag_start(windowDrag.startX, windowDrag.startY);
    }
    window.pywebview?.api?.drag_move(event.screenX, event.screenY);
  }, true);
  const endWindowDrag = (event) => {
    if (!windowDrag || (event.pointerId != null && windowDrag.id !== event.pointerId)) return;
    const moved = windowDrag.moved;
    windowDrag = null;
    if (moved) window.pywebview?.api?.drag_end();
  };
  document.addEventListener('pointerup', endWindowDrag, true);
  document.addEventListener('pointercancel', endWindowDrag, true);
  document.addEventListener('lostpointercapture', endWindowDrag, true);
  window.addEventListener('blur', endWindowDrag);
  let windowResize = null;
  for (const edge of ["n", "s", "w", "e", "nw", "ne", "sw", "se"]) {
    const zone = document.createElement("div");
    zone.className = `ark-resize ${edge}`;
    zone.addEventListener("pointerdown", (event) => {
      if (event.button !== 0) return;
      event.preventDefault();
      event.stopPropagation();
      windowResize = { id: event.pointerId, edge };
      try { zone.setPointerCapture?.(event.pointerId); } catch (e) {}
      window.pywebview?.api?.resize_start(edge, event.screenX, event.screenY);
    });
    zone.addEventListener("pointermove", (event) => {
      if (!windowResize || windowResize.id !== event.pointerId) return;
      window.pywebview?.api?.resize_move(event.screenX, event.screenY);
    });
    const finish = (event) => {
      if (!windowResize || windowResize.id !== event.pointerId) return;
      windowResize = null;
      window.pywebview?.api?.resize_end();
    };
    zone.addEventListener("pointerup", finish);
    zone.addEventListener("pointercancel", finish);
    document.body.appendChild(zone);
  }
  const bar = document.createElement('div');
  bar.id = 'ark-desktop-controls';
  const make = (label, title, fn) => {
    const button = document.createElement('button');
    button.textContent = label; button.title = title;
    button.addEventListener('click', (event) => { event.stopPropagation(); fn(); });
    bar.appendChild(button);
    return button;
  };
  make('\u2212', 'Minimize', () => window.pywebview?.api?.minimize());
  make('\u25a1', 'Maximize / restore', () => window.pywebview?.api?.maximize());
  const close = make('\u00d7', 'Hide to tray', () => window.pywebview?.api?.close());
  if (close) close.classList.add('ark-close');
  document.body.appendChild(bar);
})();
"""


def _inject_chrome(window):
    script = _chrome_script()

    def apply():
        try:
            window.evaluate_js(script)
            _apply_window_icon(window)
            _set_taskbar_visible(True)
        except Exception:
            pass

    try:
        window.events.loaded += apply
    except Exception:
        pass
    apply()


def _inject_after_start(window):
    configure_taskbar_identity()
    script = _chrome_script()
    for delay in (0.4, 1.2, 2.5, 4.0):
        time.sleep(delay)
        try:
            window.evaluate_js(script)
        except Exception:
            pass


def _desktop_config_path() -> Path:
    return Path.home() / ".ark" / "desktop.json"


def _load_desktop_config() -> dict:
    try:
        data = json.loads(_desktop_config_path().read_text(encoding="utf-8"))
        return data if isinstance(data, dict) else {}
    except (OSError, ValueError):
        return {}


def _save_desktop_config(config: dict) -> None:
    try:
        path = _desktop_config_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(config, indent=2, ensure_ascii=False) + "\n",
                        encoding="utf-8")
    except OSError:
        pass


def run_desktop(*, root: str, host: str, port: int, server: bool = True,
                remote_url: str = "", force_local: bool = False) -> int:
    if _IMPORT_ERROR:
        print("ARK desktop shell needs optional dependencies:", file=sys.stderr)
        print("  pip install -r requirements-desktop.txt", file=sys.stderr)
        print(f"Import error: {_IMPORT_ERROR}", file=sys.stderr)
        return 2
    configure_taskbar_identity()
    config = _load_desktop_config()
    remote_url = (remote_url or os.environ.get("ARK_REMOTE_URL") or "").strip()
    if not remote_url and not force_local:
        remote_url = str(config.get("remote_url") or "").strip()
    if remote_url:
        # Persist the mode so a plain double-click of the desktop app uses the
        # remote server next time.  The same file carries the local-open bridge
        # allowlists used by the native remote window.
        config["remote_url"] = remote_url
        config.setdefault("local_open_origins",
                          [x for x in (os.environ.get("ARK_LOCAL_OPEN_ORIGINS") or "").split(",") if x])
        config.setdefault("local_open_roots",
                          [x for x in (os.environ.get("ARK_LOCAL_OPEN_ROOTS") or "").split(os.pathsep) if x])
        _save_desktop_config(config)
    if remote_url:
        origins = config.get("local_open_origins") or []
        roots = config.get("local_open_roots") or []
        if origins and not os.environ.get("ARK_LOCAL_OPEN_ORIGINS"):
            os.environ["ARK_LOCAL_OPEN_ORIGINS"] = ",".join(str(x) for x in origins)
        if roots and not os.environ.get("ARK_LOCAL_OPEN_ROOTS"):
            os.environ["ARK_LOCAL_OPEN_ROOTS"] = os.pathsep.join(str(x) for x in roots)
    service = ManagedServer(root=root, host=host, port=port)
    if server:
        service.ensure()
    elif not remote_url and not _healthy(service.url):
        print(f"ARK server is not reachable at {service.url}", file=sys.stderr)
        return 2
    app_url = remote_url or service.url
    app_title = "ARK Studio · Remote" if remote_url else "ARK Studio"

    icon_path = Path(__file__).resolve().parent.parent / "web" / "brand" / "ark.ico"
    work_w, work_h = 1440, 900
    if os.name == "nt":
        try:
            from ctypes import wintypes
            rect = wintypes.RECT()
            if ctypes.windll.user32.SystemParametersInfoW(0x0030, 0, ctypes.byref(rect), 0):
                work_w = max(920, int(rect.right - rect.left))
                work_h = max(600, int(rect.bottom - rect.top))
        except Exception:
            pass
    # pywebview cancels every download by default (the WebView shows "Couldn't
    # download"), which breaks project exports and Markdown bundles.  Allow
    # them: the native "Save as" dialog then asks where to put the file.
    if hasattr(webview, "settings"):
        webview.settings["ALLOW_DOWNLOADS"] = True
    window = webview.create_window(
        app_title, app_url,
        width=min(1440, max(920, work_w - 80)), height=min(940, max(600, work_h - 80)),
        min_size=(920, 600),
        frameless=True, easy_drag=False,
        background_color="#0f141c",
    )
    tray = {"icon": None}

    def quit_all():
        try:
            if tray["icon"]:
                tray["icon"].stop()
        except Exception:
            pass
        try:
            window.destroy()
        except Exception:
            pass
        service.stop()

    api = DesktopApi(window, tray, quit_all)

    def show():
        try:
            window.show()
            window.restore()
        except Exception:
            pass

    def hide():
        try:
            window.hide()
        except Exception:
            pass

    def start_tray():
        remote = (remote_url or os.environ.get("ARK_REMOTE_URL") or "").strip()
        items = [
            pystray.MenuItem("Open native window", lambda icon, item: show(), default=True),
            pystray.MenuItem("Open in web browser", lambda icon, item: webbrowser.open(app_url)),
        ]
        if remote:
            items.append(pystray.MenuItem(
                "Open local ARK",
                lambda icon, item: webbrowser.open(service.url)))
        items.extend([
            pystray.MenuItem("Hide native window", lambda icon, item: hide()),
            pystray.Menu.SEPARATOR,
            pystray.MenuItem("Quit ARK", lambda icon, item: quit_all()),
        ])
        menu = pystray.Menu(*items)
        icon = pystray.Icon("ark-studio", _icon_image(), "ARK Studio", menu)
        tray["icon"] = icon
        icon.run()

    window.expose(api.drag, api.drag_start, api.drag_move, api.drag_end,
                  api.resize_start, api.resize_move, api.resize_end,
                  api.minimize, api.maximize, api.hide, api.close, api.quit)
    window.events.loaded += lambda: _inject_chrome(window)
    window.events.closing += lambda: (hide(), False)[1]
    threading.Thread(target=start_tray, daemon=True).start()
    try:
        if os.name == "nt":
            webview.start(gui="edgechromium", debug=False, icon=str(icon_path),
                          func=lambda: _inject_after_start(window))
        else:
            webview.start(debug=False, func=lambda: _inject_after_start(window))
    finally:
        service.stop()
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="arkstudio desktop")
    parser.add_argument("--root", default="")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8890)
    parser.add_argument("--no-server", action="store_true",
                        help="use an already running ARK server instead of starting one")
    parser.add_argument("--remote-url", default=os.environ.get("ARK_REMOTE_URL", ""),
                        help="remote ARK URL to load in the desktop window")
    parser.add_argument("--local", action="store_true",
                        help="ignore the saved remote URL and open local ARK")
    args = parser.parse_args(argv)
    root = args.root or str(Path.cwd() / "runtime" / "projects")
    return run_desktop(root=root, host=args.host, port=args.port,
                       server=not args.no_server, remote_url=args.remote_url,
                       force_local=args.local)
