"""Connection chooser used by the Windows executable and source installs."""
from __future__ import annotations

import os
from pathlib import Path
import sys
import traceback
from urllib.parse import urlsplit, urlunsplit


def _configure_windows_dpi_awareness() -> None:
    """Keep the borderless Tk window crisp and correctly sized on Windows."""
    if os.name != "nt":
        return
    try:
        # Per-monitor v2 is preferred; the fallback covers older Windows 10.
        ctypes = __import__("ctypes")
        ctypes.windll.shcore.SetProcessDpiAwareness(2)
    except Exception:
        try:
            __import__("ctypes").windll.user32.SetProcessDPIAware()
        except Exception:
            pass


# Soft card shadow behind the chooser, in logical (96 dpi) pixels: about 30 px
# of spread, a wide Gaussian falloff, low opacity, nudged slightly downward.
SHADOW_SPREAD = 30
SHADOW_BLUR = 11.0
SHADOW_OFFSET_Y = 4
SHADOW_PEAK_ALPHA = 0.15


def shadow_alpha(width: int, height: int, scale: float = 1.0) -> tuple[int, int, int, list[int]]:
    """Alpha mask (0-255, row-major) of a Gaussian-blurred rectangle.

    The blur of an axis-aligned rectangle is separable: each pixel is the
    product of two 1-D erf profiles, so no imaging library is needed. Returns
    (mask width, mask height, spread in device pixels, values).
    """
    from math import erf, sqrt

    spread = max(1, round(SHADOW_SPREAD * scale))
    sigma = max(0.5, SHADOW_BLUR * scale) * sqrt(2)
    offset = round(SHADOW_OFFSET_Y * scale)
    full_w, full_h = width + 2 * spread, height + 2 * spread

    def profile(size, start, end):
        return [0.5 * (erf((i + 0.5 - start) / sigma) - erf((i + 0.5 - end) / sigma)) for i in range(size)]

    xs = profile(full_w, spread, spread + width)
    ys = profile(full_h, spread + offset, spread + offset + height)
    peak = 255 * SHADOW_PEAK_ALPHA
    return full_w, full_h, spread, [round(peak * y * x) for y in ys for x in xs]


class _LayeredShadow:
    """Click-through per-pixel-alpha window kept directly below the chooser.

    Win32 only: a WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_NOACTIVATE |
    WS_EX_TOOLWINDOW toplevel painted with UpdateLayeredWindow. It follows the
    chooser while dragging, re-renders on size/DPI changes and is destroyed
    with it (it is a Tk child of the chooser).
    """

    def __init__(self, window):
        import ctypes
        from ctypes import wintypes
        import tkinter as tk

        self.ctypes, self.wintypes = ctypes, wintypes
        self.user32, self.gdi32 = ctypes.windll.user32, ctypes.windll.gdi32
        self.window = window
        self.owner_hwnd = self._hwnd(window)
        self.top = tk.Toplevel(window)
        self.top.overrideredirect(True)
        self.top.withdraw()
        self.hwnd = self._hwnd(self.top)
        get_ex = getattr(self.user32, "GetWindowLongPtrW", self.user32.GetWindowLongW)
        set_ex = getattr(self.user32, "SetWindowLongPtrW", self.user32.SetWindowLongW)
        get_ex.restype = set_ex.restype = ctypes.c_ssize_t
        get_ex.argtypes = [wintypes.HWND, ctypes.c_int]
        set_ex.argtypes = [wintypes.HWND, ctypes.c_int, ctypes.c_ssize_t]
        # LAYERED | TRANSPARENT (click-through) | NOACTIVATE | TOOLWINDOW, no APPWINDOW.
        ex_style = (get_ex(self.hwnd, -20) | 0x00080000 | 0x00000020 | 0x08000000 | 0x00000080) & ~0x00040000
        set_ex(self.hwnd, -20, ex_style)
        self.key = None
        self.spread = 0
        self.top.deiconify()
        self.sync()
        window.bind("<Configure>", self._on_configure, add="+")
        window.bind("<FocusIn>", lambda _event: self._place(), add="+")

    def _hwnd(self, widget):
        widget.update_idletasks()
        return self.user32.GetParent(widget.winfo_id()) or widget.winfo_id()

    def _rect(self):
        rect = self.wintypes.RECT()
        self.user32.GetWindowRect(self.wintypes.HWND(self.owner_hwnd), self.ctypes.byref(rect))
        return rect

    def _scale(self) -> float:
        try:
            dpi = self.user32.GetDpiForWindow(self.wintypes.HWND(self.owner_hwnd))
            if dpi:
                return dpi / 96
        except Exception:
            pass
        return self.window.winfo_fpixels("1i") / 96

    def _on_configure(self, event):
        if event.widget is self.window:
            self.sync()

    def sync(self):
        rect = self._rect()
        key = (rect.right - rect.left, rect.bottom - rect.top, self._scale())
        if key != self.key:
            self.key = key
            self._render(rect, *key)
        else:
            self._place(rect)

    def _render(self, rect, width, height, scale):
        ctypes, wintypes = self.ctypes, self.wintypes
        full_w, full_h, spread, values = shadow_alpha(width, height, scale)
        self.spread = spread
        # Black shadow: premultiplied BGRA is (0, 0, 0, alpha).
        pixels = bytearray(full_w * full_h * 4)
        pixels[3::4] = bytes(values)

        class BITMAPINFOHEADER(ctypes.Structure):
            _fields_ = [("biSize", wintypes.DWORD), ("biWidth", wintypes.LONG), ("biHeight", wintypes.LONG),
                        ("biPlanes", wintypes.WORD), ("biBitCount", wintypes.WORD),
                        ("biCompression", wintypes.DWORD), ("biSizeImage", wintypes.DWORD),
                        ("biXPelsPerMeter", wintypes.LONG), ("biYPelsPerMeter", wintypes.LONG),
                        ("biClrUsed", wintypes.DWORD), ("biClrImportant", wintypes.DWORD)]

        class BLENDFUNCTION(ctypes.Structure):
            _fields_ = [("BlendOp", ctypes.c_ubyte), ("BlendFlags", ctypes.c_ubyte),
                        ("SourceConstantAlpha", ctypes.c_ubyte), ("AlphaFormat", ctypes.c_ubyte)]

        user32, gdi32 = self.user32, self.gdi32
        gdi32.CreateCompatibleDC.restype = wintypes.HDC
        gdi32.CreateCompatibleDC.argtypes = [wintypes.HDC]
        gdi32.CreateDIBSection.restype = wintypes.HBITMAP
        gdi32.CreateDIBSection.argtypes = [wintypes.HDC, ctypes.c_void_p, wintypes.UINT,
                                           ctypes.POINTER(ctypes.c_void_p), wintypes.HANDLE, wintypes.DWORD]
        gdi32.SelectObject.restype = wintypes.HGDIOBJ
        gdi32.SelectObject.argtypes = [wintypes.HDC, wintypes.HGDIOBJ]
        gdi32.DeleteObject.argtypes = [wintypes.HGDIOBJ]
        gdi32.DeleteDC.argtypes = [wintypes.HDC]
        user32.GetDC.restype = wintypes.HDC
        user32.GetDC.argtypes = [wintypes.HWND]
        user32.ReleaseDC.argtypes = [wintypes.HWND, wintypes.HDC]
        user32.UpdateLayeredWindow.argtypes = [
            wintypes.HWND, wintypes.HDC, ctypes.POINTER(wintypes.POINT), ctypes.POINTER(wintypes.SIZE),
            wintypes.HDC, ctypes.POINTER(wintypes.POINT), wintypes.COLORREF,
            ctypes.POINTER(BLENDFUNCTION), wintypes.DWORD]
        header = BITMAPINFOHEADER(ctypes.sizeof(BITMAPINFOHEADER), full_w, -full_h, 1, 32, 0, 0, 0, 0, 0, 0)
        screen = user32.GetDC(None)
        memory = gdi32.CreateCompatibleDC(screen)
        bits = ctypes.c_void_p()
        bitmap = gdi32.CreateDIBSection(memory, ctypes.byref(header), 0, ctypes.byref(bits), None, 0)
        try:
            if not bitmap or not bits.value:
                raise OSError("CreateDIBSection failed")
            ctypes.memmove(bits, bytes(pixels), len(pixels))
            previous = gdi32.SelectObject(memory, bitmap)
            ok = user32.UpdateLayeredWindow(
                self.hwnd, screen,
                ctypes.byref(wintypes.POINT(rect.left - spread, rect.top - spread)),
                ctypes.byref(wintypes.SIZE(full_w, full_h)), memory,
                ctypes.byref(wintypes.POINT(0, 0)), 0,
                ctypes.byref(BLENDFUNCTION(0, 0, 255, 1)), 2)  # AC_SRC_OVER, AC_SRC_ALPHA, ULW_ALPHA
            gdi32.SelectObject(memory, previous)
            if not ok:
                raise OSError("UpdateLayeredWindow failed")
        finally:
            if bitmap:
                gdi32.DeleteObject(bitmap)
            gdi32.DeleteDC(memory)
            user32.ReleaseDC(None, screen)
        self._place(rect)

    def _place(self, rect=None):
        rect = rect or self._rect()
        # Directly below the chooser in z-order; never activate the shadow.
        self.user32.SetWindowPos(self.wintypes.HWND(self.hwnd), self.wintypes.HWND(self.owner_hwnd),
                                 rect.left - self.spread, rect.top - self.spread, 0, 0,
                                 0x0001 | 0x0010 | 0x0040)  # NOSIZE | NOACTIVATE | SHOWWINDOW


def _enable_dwm_shadow(window) -> bool:
    """Fallback: DWM's compositor shadow for a borderless popup (tighter)."""
    import ctypes
    from ctypes import wintypes

    hwnd = ctypes.windll.user32.GetParent(window.winfo_id()) or window.winfo_id()
    dwm = ctypes.windll.dwmapi
    enabled = wintypes.BOOL(False)
    if dwm.DwmIsCompositionEnabled(ctypes.byref(enabled)) != 0 or not enabled.value:
        return False
    policy = ctypes.c_int(2)  # DWMWA_NCRENDERING_POLICY = DWMNCRP_ENABLED
    if dwm.DwmSetWindowAttribute(hwnd, 2, ctypes.byref(policy), ctypes.sizeof(policy)) != 0:
        return False

    class MARGINS(ctypes.Structure):
        _fields_ = [("left", ctypes.c_int), ("right", ctypes.c_int),
                    ("top", ctypes.c_int), ("bottom", ctypes.c_int)]

    return dwm.DwmExtendFrameIntoClientArea(hwnd, ctypes.byref(MARGINS(1, 1, 1, 1))) == 0


def _enable_native_shadow(window) -> str:
    """Give the borderless chooser an OS-composited shadow behind its bounds.

    Windows 10's native shadows (DWM, CS_DROPSHADOW) are small and fixed, so
    the preferred path is a separate layered shadow window with a ~30 px soft
    Gaussian falloff. If that fails, fall back to the DWM shadow. Returns the
    technique used: "layered", "dwm" or "" (non-Windows / nothing applied).
    """
    if os.name != "nt":
        return ""
    try:
        window._ark_shadow = _LayeredShadow(window)
        return "layered"
    except Exception:
        traceback.print_exc()
    try:
        return "dwm" if _enable_dwm_shadow(window) else ""
    except Exception:
        return ""

def remote_address(value: str) -> str:
    value = value.strip()
    try:
        url = urlsplit(value)
        valid = (url.scheme in {"http", "https"} and url.hostname and not url.username
                 and not url.password and not url.query and not url.fragment)
        url.port
    except ValueError:
        valid = False
    if not valid or any(char.isspace() for char in value):
        raise ValueError("Enter an HTTP(S) server URL without credentials, a query or a fragment.")
    return urlunsplit((url.scheme, url.netloc, url.path.rstrip("/"), "", ""))


def connection_options(mode: str, address: str, root: str, port: str) -> dict:
    if mode == "remote":
        return {"remote_url": remote_address(address), "server": False, "force_local": False,
                "root": root, "host": "127.0.0.1", "port": 8890}
    if mode != "local":
        raise ValueError("Choose localhost or a remote server.")
    try:
        number = int(port)
    except ValueError:
        raise ValueError("The local port must be between 1 and 65535.") from None
    if not 1 <= number <= 65535 or not root.strip():
        raise ValueError("Choose a project folder and a local port between 1 and 65535.")
    # Local mode must override both the saved URL and ARK_REMOTE_URL.
    return {"remote_url": "", "server": True, "force_local": True,
            "root": str(Path(root).expanduser().resolve()), "host": "127.0.0.1", "port": number}


def choose_connection(config: dict) -> dict | None:
    import tkinter as tk
    from tkinter import filedialog, messagebox, ttk

    _configure_windows_dpi_awareness()
    colors = {
        "bg": "#f5f6f8", "surface": "#ffffff", "surface_2": "#f7f9fc",
        "line": "#e1e5eb", "ink": "#1a2231", "sub": "#5b6474",
        "mute": "#8a93a0", "accent": "#2563eb", "accent_dark": "#1d4ed8",
        "accent_soft": "#eaf1ff", "input": "#ffffff", "input_disabled": "#eef1f5",
    }
    window = tk.Tk()
    window.title("ARK Studio Launcher")
    # A native border makes the small chooser look unrelated to ARK's shell.
    # The custom title bar keeps the window movable while matching the app.
    window.overrideredirect(True)
    window.resizable(False, False)
    window.configure(background=colors["bg"])
    window.option_add("*Font", "{Segoe UI} 10")
    style = ttk.Style(window)
    if "vista" in style.theme_names():
        style.theme_use("vista")
    style.configure("Ark.TCombobox", fieldbackground=colors["input"],
                    background=colors["input"], foreground=colors["ink"],
                    bordercolor=colors["line"], lightcolor=colors["line"],
                    darkcolor=colors["line"], padding=8)
    style.map("Ark.TCombobox", fieldbackground=[("disabled", colors["input_disabled"])],
              foreground=[("disabled", colors["mute"])])
    icon = Path(__file__).resolve().parents[1] / "web/brand/ark.ico"
    if icon.is_file():
        try:
            window.iconbitmap(str(icon))
        except tk.TclError:
            pass

    # No painted edge: the OS draws the shadow outside the window bounds
    # (see _enable_native_shadow), so the card itself stays borderless.
    shell = tk.Frame(window, bg=colors["surface"], highlightthickness=0, bd=0)
    shell.pack(fill="both", expand=True)
    titlebar = tk.Frame(shell, bg=colors["surface"], height=68)
    titlebar.pack(fill="x")
    titlebar.pack_propagate(False)

    lockup = Path(__file__).resolve().parents[1] / "web/brand/ark-lockup-corner.png"
    lockup_image = None
    if lockup.is_file():
        try:
            lockup_image = tk.PhotoImage(file=str(lockup))
        except tk.TclError:
            lockup_image = None
    if lockup_image is not None:
        # Keep a reference on the root for Tk's image lifetime.
        window._ark_lockup_image = lockup_image
        lockup_label = tk.Label(titlebar, image=lockup_image, bg=colors["surface"],
                                bd=0, highlightthickness=0)
        lockup_label.pack(side="left", padx=(22, 0), pady=19)
        titlebar_children = [lockup_label]
    else:
        # Source checkouts without generated brand assets still get a usable
        # fallback; the normal build path uses the repository SVG-derived PNG.
        mark = tk.Canvas(titlebar, width=34, height=34, bg=colors["surface"],
                         highlightthickness=0, bd=0)
        mark.pack(side="left", padx=(22, 9), pady=17)
        mark.create_polygon(17, 2, 32, 31, 20, 31, 17, 22, fill=colors["accent"], outline="")
        mark.create_polygon(17, 2, 2, 31, 14, 31, 17, 22, fill=colors["accent_dark"], outline="")
        brand = tk.Frame(titlebar, bg=colors["surface"])
        brand.pack(side="left", pady=13)
        tk.Label(brand, text="ARK", bg=colors["surface"], fg=colors["ink"],
                 font=("Segoe UI", 16, "bold")).pack(side="left")
        tk.Label(brand, text="Studio", bg=colors["surface"], fg=colors["sub"],
                 font=("Segoe UI", 16)).pack(side="left", padx=(4, 0))
        titlebar_children = [mark, brand, *brand.winfo_children()]

    def close_window(_event=None):
        window.destroy()

    close = tk.Label(titlebar, text="×", bg=colors["surface"], fg=colors["mute"],
                     activebackground="#fee2e2", activeforeground="#dc2626",
                     font=("Segoe UI", 17), width=3, cursor="hand2")
    close.pack(side="right", padx=(0, 7), pady=15)
    close.bind("<Button-1>", close_window)
    tk.Frame(shell, bg=colors["line"], height=1).pack(fill="x")

    content = tk.Frame(shell, bg=colors["bg"], padx=28, pady=26)
    content.pack(fill="both", expand=True)
    content.columnconfigure(0, weight=1)
    tk.Label(content, text="Choose a workspace", bg=colors["bg"], fg=colors["ink"],
             font=("Segoe UI", 20, "bold")).grid(row=0, column=0, sticky="w")
    tk.Label(content, text="Open ARK locally or connect to a remote server.", bg=colors["bg"],
             fg=colors["sub"], font=("Segoe UI", 10)).grid(row=1, column=0, sticky="w", pady=(5, 20))
    saved = str(config.get("remote_url") or "")
    mode = tk.StringVar(value=config.get("launcher_mode") or ("remote" if saved else "local"))
    address = tk.StringVar(value=saved)
    root = tk.StringVar(value=str(config.get("launcher_root") or os.environ.get("ARK_ROOT")
                                or Path.cwd() / "runtime/projects"))
    port = tk.StringVar(value=str(config.get("launcher_port") or 8890))

    modes = tk.Frame(content, bg=colors["bg"])
    modes.grid(row=2, column=0, sticky="ew", pady=(0, 22))
    modes.columnconfigure(0, weight=1)
    modes.columnconfigure(1, weight=1)
    mode_buttons = {}

    class RoundedButton:
        def __init__(self, parent, text, command, *, width=120, height=38,
                     fill=colors["surface"], foreground=colors["sub"],
                     hover=colors["accent_soft"], radius=9):
            self.command = command
            self.text = text
            self.width, self.height, self.radius = width, height, radius
            self.fill, self.foreground, self.hover = fill, foreground, hover
            self.enabled = True
            self.canvas = tk.Canvas(parent, width=width, height=height, bg=colors["bg"],
                                    bd=0, highlightthickness=0, cursor="hand2")
            self.canvas.bind("<Enter>", self._enter)
            self.canvas.bind("<Leave>", self._leave)
            self.canvas.bind("<Button-1>", self._click)
            self._paint()

        def _rounded(self, fill):
            r = self.radius
            w, h = self.width - 1, self.height - 1
            self.canvas.create_rectangle(r, 0, w - r, h, fill=fill, outline=fill)
            self.canvas.create_rectangle(0, r, w, h - r, fill=fill, outline=fill)
            for x, y in ((0, 0), (w - 2 * r, 0), (0, h - 2 * r), (w - 2 * r, h - 2 * r)):
                self.canvas.create_oval(x, y, x + 2 * r, y + 2 * r, fill=fill, outline=fill)

        def _paint(self):
            self.canvas.delete("all")
            fill = self.fill if self.enabled else colors["input_disabled"]
            foreground = self.foreground if self.enabled else colors["mute"]
            self._rounded(fill)
            self.canvas.create_text(self.width / 2, self.height / 2, text=self.text,
                                    fill=foreground, font=("Segoe UI", 10, "bold"))

        @property
        def text(self):
            return getattr(self, "_text", "")

        @text.setter
        def text(self, value):
            self._text = value

        def _enter(self, _event):
            if self.enabled:
                old = self.fill
                self.fill = self.hover
                self._paint()
                self.fill = old

        def _leave(self, _event):
            self._paint()

        def _click(self, _event):
            if self.enabled:
                self.command()

        def set_style(self, fill, foreground, hover):
            self.fill, self.foreground, self.hover = fill, foreground, hover
            self._paint()

        def set_enabled(self, enabled):
            self.enabled = bool(enabled)
            self.canvas.configure(cursor="hand2" if self.enabled else "arrow")
            self._paint()

    def set_mode(value):
        mode.set(value)
        update_mode()

    for column, (value, label) in enumerate((("local", "Localhost"), ("remote", "Remote server"))):
        button = RoundedButton(modes, label, lambda selected=value: set_mode(selected),
                               width=244, height=42, radius=10)
        button.canvas.grid(row=0, column=column, sticky="ew", padx=(0 if column == 0 else 5, 5 if column == 0 else 0))
        mode_buttons[value] = button

    def field_label(parent, text):
        return tk.Label(parent, text=text.upper(), bg=colors["bg"], fg=colors["sub"],
                        font=("Segoe UI", 8, "bold"))

    history = list(dict.fromkeys([saved, *(config.get("launcher_servers") or [])]))
    field_label(content, "Server URL").grid(row=3, column=0, sticky="w")
    url_input = ttk.Combobox(content, textvariable=address, values=[item for item in history if item],
                             style="Ark.TCombobox", width=54)
    url_input.grid(row=4, column=0, sticky="ew", pady=(6, 17))
    field_label(content, "Local project folder").grid(row=5, column=0, sticky="w")
    folder = tk.Frame(content, bg=colors["bg"])
    folder.grid(row=6, column=0, sticky="ew", pady=(6, 17))
    folder.columnconfigure(0, weight=1)
    root_input = tk.Entry(folder, textvariable=root, width=44, relief="flat", bd=0,
                          highlightthickness=1, highlightbackground=colors["line"],
                          highlightcolor=colors["accent"], bg=colors["input"], fg=colors["ink"],
                          insertbackground=colors["ink"], font=("Segoe UI", 10))
    root_input.grid(row=0, column=0, sticky="ew", ipady=8, padx=(1, 7))

    def browse():
        chosen = filedialog.askdirectory(parent=window, initialdir=root.get(), title="Local ARK projects")
        if chosen:
            root.set(chosen)

    browse_button = RoundedButton(folder, "Browse", browse, width=88, height=36, radius=8)
    browse_button.canvas.grid(row=0, column=1, padx=(0, 1))
    local = tk.Frame(content, bg=colors["bg"])
    local.grid(row=7, column=0, sticky="w", pady=(0, 23))
    field_label(local, "Local port").grid(row=0, column=0, padx=(0, 11))
    port_input = tk.Entry(local, textvariable=port, width=8, relief="flat", bd=0,
                          highlightthickness=1, highlightbackground=colors["line"],
                          highlightcolor=colors["accent"], bg=colors["input"], fg=colors["ink"],
                          insertbackground=colors["ink"], font=("Segoe UI", 10))
    port_input.grid(row=0, column=1, ipady=7)
    result = []

    def accept():
        try:
            options = connection_options(mode.get(), address.get(), root.get(), port.get())
        except ValueError as exc:
            messagebox.showerror("ARK connection", str(exc), parent=window)
            return
        config.update(launcher_mode=mode.get(), launcher_root=root.get())
        if mode.get() == "local":
            config["launcher_port"] = options["port"]
        if mode.get() == "remote":
            config["remote_url"] = options["remote_url"]
            config["launcher_servers"] = list(dict.fromkeys([options["remote_url"], *history]))[:10]
        result.append(options)
        window.destroy()

    def update_mode(*_):
        remote = mode.get() == "remote"
        url_input.configure(state="normal" if remote else "disabled")
        mode_buttons["remote"].set_style(colors["accent"] if remote else colors["surface_2"],
                                          "#ffffff" if remote else colors["sub"], colors["accent_dark"])
        mode_buttons["local"].set_style(colors["surface_2"] if remote else colors["accent"],
                                         colors["sub"] if remote else "#ffffff", colors["accent_dark"])
        disabled = remote
        for widget in (root_input, port_input):
            widget.configure(state="disabled" if disabled else "normal")
        root_input.configure(disabledbackground=colors["input_disabled"], disabledforeground=colors["mute"])
        port_input.configure(disabledbackground=colors["input_disabled"], disabledforeground=colors["mute"])
        browse_button.set_enabled(not disabled)

    mode.trace_add("write", update_mode)
    update_mode()
    actions = tk.Frame(content, bg=colors["bg"])
    actions.grid(row=8, column=0, sticky="e")
    cancel_button = RoundedButton(actions, "Cancel", close_window, width=92, height=42, radius=9)
    cancel_button.canvas.grid(row=0, column=0, padx=(0, 9))
    launch_button = RoundedButton(actions, "Launch ARK", accept, width=132, height=42,
                                  fill=colors["accent"], foreground="#ffffff",
                                  hover=colors["accent_dark"], radius=9)
    launch_button.canvas.grid(row=0, column=1)

    drag = {"x": 0, "y": 0, "left": 0, "top": 0}

    def drag_start(event):
        drag.update(x=event.x_root, y=event.y_root, left=window.winfo_x(), top=window.winfo_y())

    def drag_move(event):
        left = drag["left"] + event.x_root - drag["x"]
        top = drag["top"] + event.y_root - drag["y"]
        window.geometry(f"+{left}+{top}")

    def bind_drag(widget):
        if widget is not close:
            widget.bind("<ButtonPress-1>", drag_start, add="+")
            widget.bind("<B1-Motion>", drag_move, add="+")

    for widget in (titlebar, *titlebar_children):
        bind_drag(widget)
    window.bind("<Return>", lambda _: accept())
    window.bind("<Escape>", close_window)
    window.update_idletasks()
    screen_width, screen_height = window.winfo_screenwidth(), window.winfo_screenheight()
    requested_width, requested_height = window.winfo_reqwidth(), window.winfo_reqheight()
    # Respect small laptop displays without hard-coding one monitor's pixels.
    width = min(requested_width, max(520, screen_width - 64))
    height = min(requested_height, max(520, screen_height - 96))
    x = max(0, (screen_width - width) // 2)
    y = max(0, (screen_height - height) // 2)
    window.geometry(f"{width}x{height}+{x}+{y}")
    _enable_native_shadow(window)
    window.mainloop()
    return result[0] if result else None


def main() -> int:
    log_path = Path.home() / ".ark/desktop-launcher.log"
    log_path.parent.mkdir(parents=True, exist_ok=True)
    with log_path.open("w", encoding="utf-8", buffering=1) as log:
        old_out, old_err = sys.stdout, sys.stderr
        sys.stdout = sys.stderr = log
        try:
            from .desktop import _IMPORT_ERROR, _load_desktop_config, _save_desktop_config, run_desktop
            if _IMPORT_ERROR:
                raise RuntimeError("Install requirements-desktop.txt in this Python environment: " + str(_IMPORT_ERROR))
            config = _load_desktop_config()
            options = choose_connection(config)
            if options is None:
                return 0
            _save_desktop_config(config)
            # The chooser is explicit, so an inherited remote URL cannot
            # silently redirect a local launch or replace the chosen server.
            os.environ.pop("ARK_REMOTE_URL", None)
            return run_desktop(**options)
        except Exception as exc:
            traceback.print_exc()
            from tkinter import Tk, messagebox
            window = Tk()
            window.withdraw()
            messagebox.showerror("ARK could not start", f"{exc}\n\nLog: {log_path}", parent=window)
            window.destroy()
            return 1
        finally:
            sys.stdout, sys.stderr = old_out, old_err


if __name__ == "__main__":
    raise SystemExit(main())
