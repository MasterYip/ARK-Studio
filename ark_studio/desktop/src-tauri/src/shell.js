// ARK Studio desktop shell: injected into every page of the app's windows
// (bundled pages and ARK server pages, local or remote, any ARK version).
//
// * window.__ARK_DESKTOP__: shell info, window controls, local-open bridge.
// * Window controls (ARK style, top right) for the borderless windows on
//   Windows/Linux; on macOS the native traffic lights sit over the page
//   (Overlay title bar) and no controls are drawn.
// * Drag regions: pages that opt in (ARK web app desktop mode, bundled
//   pages) set html[data-desktop-shell] and mark their own regions; for other
//   pages (sign-in page, older servers) known ARK containers are marked here.
//   Dragging and double-click maximize are Tauri's data-tauri-drag-region.
(function () {
  "use strict";
  if (window.__ARK_DESKTOP__) return;
  var CFG = __ARK_SHELL_CONFIG__;
  function invoke(cmd, args) {
    return window.__TAURI_INTERNALS__.invoke(cmd, args || {});
  }
  var win = Object.freeze({
    minimize: function () { return invoke("plugin:window|minimize"); },
    toggleMaximize: function () { return invoke("plugin:window|toggle_maximize"); },
    isMaximized: function () { return invoke("plugin:window|is_maximized"); },
    startDragging: function () { return invoke("plugin:window|start_dragging"); },
    close: function () { return invoke("close_window"); }
  });
  Object.defineProperty(window, "__ARK_DESKTOP__", {
    value: Object.freeze({
      shell: "tauri",
      version: CFG.version,
      platform: CFG.platform,
      // "custom": borderless window, ARK draws the controls (top right);
      // "overlay": macOS native traffic lights over the page (top left).
      chrome: CFG.chrome,
      windowLabel: CFG.label,
      controls: Object.freeze(CFG.controls.slice()),
      insetLeft: CFG.insetLeft,
      window: win,
      openLocal: function (path) { return invoke("open_local_path", { path: String(path || "") }); }
    })
  });

  function mark() {
    var root = document.documentElement;
    if (!root) return;
    root.setAttribute("data-desktop-chrome", CFG.chrome);
    root.setAttribute("data-desktop-platform", CFG.platform);
    root.setAttribute("data-desktop-window", CFG.label);
    if (CFG.insetLeft) root.style.setProperty("--ark-desktop-inset-left", CFG.insetLeft + "px");
  }
  mark();

  var DRAG = "data-tauri-drag-region";
  function fallbackRegions() {
    var marked = false;
    ["#topbar", "#ark-startup", ".login-body", ".entry-brand"].forEach(function (sel) {
      var el = document.querySelector(sel);
      if (el && !el.hasAttribute(DRAG)) el.setAttribute(DRAG, "deep");
      if (el) marked = true;
    });
    var shell = document.querySelector(".login-shell");
    if (shell) shell.setAttribute(DRAG, "false");
    var style = document.createElement("style");
    style.id = "ark-desktop-fallback-style";
    style.textContent =
      "html:not([data-desktop-shell])[data-desktop-chrome=custom] #topbar .top-actions{margin-right:104px !important}" +
      "html:not([data-desktop-shell])[data-desktop-chrome=overlay] #topbar{padding-left:var(--ark-desktop-inset-left,80px) !important}" +
      "#ark-desktop-dragstrip{position:fixed;left:0;right:110px;top:0;height:30px;z-index:2147483646}";
    document.head.appendChild(style);
    if (!marked) {
      // Unknown page (error text, foreign page): a thin strip to move the window by.
      var strip = document.createElement("div");
      strip.id = "ark-desktop-dragstrip";
      strip.setAttribute(DRAG, "");
      document.body.appendChild(strip);
    }
  }

  var ICONS = {
    minimize: '<path d="M2.5 6h7"/>',
    maximize: '<rect x="2.5" y="2.5" width="7" height="7" rx="1"/>',
    restore: '<rect x="2.5" y="4" width="5.5" height="5.5" rx="1"/><path d="M4.5 4V3.5a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H8"/>',
    close: '<path d="M3 3l6 6M9 3L3 9"/>'
  };
  function svg(name) {
    return '<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" aria-hidden="true">' + ICONS[name] + "</svg>";
  }
  var TITLES = /^zh\b/i.test(navigator.language || "")
    ? { minimize: "最小化", maximize: "最大化", restore: "还原", close: "关闭" }
    : { minimize: "Minimize", maximize: "Maximize", restore: "Restore", close: "Close" };

  function controls() {
    if (!CFG.controls.length || document.getElementById("ark-desktop-controls")) return;
    var style = document.createElement("style");
    style.id = "ark-desktop-controls-style";
    style.textContent =
      "#ark-desktop-controls{position:fixed;top:var(--ark-desktop-controls-top,9px);right:var(--ark-desktop-controls-right,8px);" +
      "display:flex;align-items:center;gap:2px;height:32px;z-index:2147483647;-webkit-user-select:none;user-select:none}" +
      "#ark-desktop-controls button{width:30px;height:28px;display:grid;place-items:center;padding:0;margin:0;border:1px solid transparent;" +
      "border-radius:6px;background:transparent;color:var(--sub,var(--mute,#657083));cursor:default;transition:background .12s,color .12s,border-color .12s}" +
      "#ark-desktop-controls button:hover{background:var(--card,rgba(127,127,127,.14));border-color:var(--line,transparent);color:var(--ink,inherit)}" +
      "#ark-desktop-controls button.ark-close:hover{background:#dc2626;border-color:#dc2626;color:#fff}" +
      "#ark-desktop-controls button:focus-visible{outline:2px solid var(--accent,#2563eb);outline-offset:1px}" +
      "@media print{#ark-desktop-controls{display:none}}";
    document.head.appendChild(style);
    var bar = document.createElement("div");
    bar.id = "ark-desktop-controls";
    bar.setAttribute(DRAG, "false");
    bar.setAttribute("role", "group");
    bar.setAttribute("aria-label", "Window");
    var maxBtn = null;
    CFG.controls.forEach(function (name) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "ark-" + name;
      b.innerHTML = svg(name);
      b.title = TITLES[name];
      b.setAttribute("aria-label", TITLES[name]);
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        if (name === "minimize") win.minimize();
        else if (name === "maximize") win.toggleMaximize();
        else if (name === "close") win.close();
      });
      if (name === "maximize") maxBtn = b;
      bar.appendChild(b);
    });
    document.body.appendChild(bar);
    if (maxBtn) {
      var sync = function () {
        win.isMaximized().then(function (max) {
          var name = max ? "restore" : "maximize";
          maxBtn.innerHTML = svg(name);
          maxBtn.title = TITLES[name];
          maxBtn.setAttribute("aria-label", TITLES[name]);
          document.documentElement.toggleAttribute("data-desktop-maximized", !!max);
        }, function () {});
      };
      var timer = 0;
      window.addEventListener("resize", function () { clearTimeout(timer); timer = setTimeout(sync, 80); });
      sync();
    }
  }

  function ready() {
    mark();
    if (!document.documentElement.hasAttribute("data-desktop-shell")) fallbackRegions();
    controls();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", ready);
  else ready();
})();
