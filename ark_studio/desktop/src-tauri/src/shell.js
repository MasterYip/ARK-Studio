// ARK Studio desktop shell: injected into every page of the app's windows
// (bundled pages and ARK server pages, local or remote, any ARK version).
//
// * window.__ARK_DESKTOP__: shell info, window controls, local-open bridge
//   and the desktop hub (`hub`). The app's capabilities decide what a page
//   may actually call: the hub's server list, "open server" and the linked
//   servers' projects work on the local ARK page only; remote pages get
//   context(), goLocal() (the project switcher's "This computer") and linkServer(code) for
//   their own server; everything else is refused there.
// * Window controls (ARK style, top right) for the borderless windows on
//   Windows/Linux; on macOS the native traffic lights sit over the page
//   (Overlay title bar) and no controls are drawn.
// * Drag regions: pages that opt in (ARK web app desktop mode, bundled
//   pages) set html[data-desktop-shell] and mark their own regions; for other
//   pages (sign-in page, older servers) known ARK containers are marked here.
//   Dragging and double-click maximize are Tauri's data-tauri-drag-region.
// * "ARK Studio 2.x.y is available" (bottom right of every ARK page in the
//   main window, local or remote): Update / Later, hidden per version
//   (`updateNotice` tells the page not to show its own banner).
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
      // This shell shows the update notice itself (pages skip their banner).
      updateNotice: true,
      // The app saves page downloads (<a download>, attachments, blob: URLs)
      // to the Downloads folder and shows them (src/downloads.rs).
      downloads: true,
      window: win,
      openLocal: function (path) { return invoke("open_local_path", { path: String(path || "") }); },
      // PNG bytes (Uint8Array) to the system clipboard, for pages whose
      // WebView refuses navigator.clipboard.write (web/js/image-copy.js).
      copyImage: function (bytes) { return invoke("copy_image_png", bytes); },
      // Resource location sizes (web/js/location-size.js): this computer's
      // device id and the sizes of its registered paths.
      deviceId: function () { return invoke("device_id"); },
      measurePaths: function (paths) {
        return invoke("measure_paths", { paths: (paths || []).map(function (p) { return String(p || ""); }) });
      },
      // Resources: open a location over SSH (web/js/resource-open.js). The
      // app checks every part and builds the command itself (remote_open.rs).
      openSsh: function (target, path) {
        target = target || {};
        var text = function (v) { return v === undefined || v === null || v === "" ? null : String(v); };
        return invoke("open_ssh_terminal", {
          target: { alias: text(target.alias), host: text(target.host), user: text(target.user),
            port: target.port ? Number(target.port) : null, identity_file: text(target.identityFile),
            auth: text(target.auth) },
          path: text(path)
        });
      },
      // A terminal window in a folder on this computer (remote_open.rs local_terminal_launch).
      openTerminal: function (path) { return invoke("open_local_terminal", { path: String(path || "") }); },
      sshConfigMatch: function (names) {
        return invoke("ssh_config_match", { names: (names || []).map(function (n) { return String(n || ""); }) });
      },
      openEditor: function (url) { return invoke("open_editor_url", { url: String(url || "") }); },
      // Host ark-<name> in the ARK block of ~/.ssh/config (the app shows the
      // entry and asks before writing): {alias, host, user, port, identityFile, auth}.
      sshConfigEnsure: function (entry) {
        entry = entry || {};
        var text = function (v) { return v === undefined || v === null || v === "" ? null : String(v); };
        return invoke("ssh_config_ensure", {
          entry: { alias: String(entry.alias || ""), host: String(entry.host || ""), user: text(entry.user),
            port: entry.port ? Number(entry.port) : null, identity_file: text(entry.identityFile),
            auth: text(entry.auth) }
        });
      },
      sshKeyCandidates: function () { return invoke("ssh_key_candidates"); },
      chooseSshKey: function () { return invoke("choose_ssh_key"); },
      hub: Object.freeze({
        // {role: "local" | "remote" | "bundled", version, server?}
        context: function () { return invoke("desktop_context"); },
        goLocal: function () { return invoke("go_local_home"); },
        servers: function () { return invoke("desktop_servers"); },
        // Updates: update() {current, available, selfInstall, enabled,
        // autoCheck, lastCheck, dismissed, notice} on every ARK page;
        // dismissUpdate(version) is "Later"; openAppSettings(view) opens the
        // app's settings window ("about", "servers", "local", "window",
        // "data"). The local page also: checkUpdate asks the release
        // endpoint; installUpdate downloads, installs and restarts like
        // Settings > About; setAutoCheck(on) turns the automatic check on or
        // off; dismissUpdateNotice() closes "updated to"; openReleases(version)
        // opens the release page (portable builds).
        update: function () { return invoke("update_state"); },
        dismissUpdate: function (version) { return invoke("dismiss_update", { version: String(version || "") }); },
        dismissUpdateNotice: function () { return invoke("dismiss_update_notice"); },
        openAppSettings: function (view) { return invoke("open_app_settings", { view: view ? String(view) : null }); },
        checkUpdate: function () { return invoke("check_update", { install: false }); },
        installUpdate: function () { return invoke("check_update", { install: true }); },
        setAutoCheck: function (on) { return invoke("set_update_auto_check", { enabled: !!on }); },
        openReleases: function (version) { return invoke("open_releases", { version: version ? String(version) : null }); },
        status: function (url) { return invoke("server_status", { url: String(url || "") }); },
        openServer: function (url, opts) {
          opts = opts || {};
          return invoke("open_server", {
            url: String(url || ""),
            project: opts.project ? String(opts.project) : null,
            newProject: !!opts.newProject,
            link: !!opts.link,
            force: !!opts.force
          });
        },
        openSettings: function (view) { return invoke("open_settings", { view: view ? String(view) : null }); },
        // Settings > Project storage location: the native folder picker
        // (the chosen path, or null when cancelled).
        pickFolder: function (current) { return invoke("choose_project_folder", { current: current ? String(current) : null }); },
        // Desktop links: a server page hands over its one-time code
        // (remote pages); the local page reads and creates projects through
        // the app, which keeps the device token to itself.
        linkServer: function (code) { return invoke("link_server", { code: String(code || "") }); },
        serverProjects: function (url) { return invoke("server_projects", { url: String(url || "") }); },
        createServerProject: function (url, data) {
          data = data || {};
          return invoke("create_server_project", {
            url: String(url || ""),
            name: String(data.name || ""),
            description: data.description ? String(data.description) : null,
            template: data.template ? String(data.template) : null
          });
        },
        unlinkServer: function (url) { return invoke("unlink_server", { url: String(url || "") }); }
      })
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
      "html:not([data-desktop-shell])[data-desktop-chrome=custom] #topbar .top-actions{margin-right:max(var(--ark-desktop-controls-space,104px),calc(var(--ark-desktop-controls-reserve,0px) - 9px)) !important}" +
      "html:not([data-desktop-shell])[data-desktop-chrome=overlay] #topbar{padding-left:var(--ark-desktop-inset-left,80px) !important}" +
      "#ark-desktop-dragstrip{position:fixed;left:0;right:var(--ark-desktop-controls-reserve,110px);top:0;height:30px;z-index:2147483646}";
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

  function controlsStyle() {
    if (document.getElementById("ark-desktop-controls-style")) return;
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
  }

  // --ark-desktop-controls-reserve: the width from the controls' left edge to
  // the window's right edge plus an 8 px gap, measured from the drawn bar, so
  // pages (top bar, sign-in and startup screens) keep their buttons clear of
  // it however many buttons it holds and at every zoom or display scaling.
  var GAP = 8;
  function reserve() {
    var bar = document.getElementById("ark-desktop-controls");
    var root = document.documentElement;
    if (!bar || !root) return;
    var left = bar.getBoundingClientRect().left;
    var space = Math.ceil(root.clientWidth - left) + GAP;
    if (space > GAP) root.style.setProperty("--ark-desktop-controls-reserve", space + "px");
  }
  function watchReserve(bar) {
    reserve();
    if (typeof ResizeObserver === "function") new ResizeObserver(reserve).observe(bar);
    window.addEventListener("resize", reserve);
  }

  function controls() {
    if (!CFG.controls.length || document.getElementById("ark-desktop-controls")) return;
    controlsStyle();
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
    watchReserve(bar);
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

  // Links. target=_blank links and window.open() reach the main window's
  // on_new_window (src/lib.rs), which hands http(s) and mailto: to the
  // system. A plain link to another site would instead replace the ARK page
  // inside the app (no address bar, no way back to a browser): it takes the
  // same new-window path. Links within the ARK server (#/ routes, same
  // origin) and every other scheme are left to the page.
  // link-target:begin
  function externalTarget(href, base, target) {
    var t = String(target || "").toLowerCase();
    if (t && t !== "_self" && t !== "_top" && t !== "_parent") return "";
    var url;
    try { url = new URL(String(href || ""), base); } catch (e) { return ""; }
    if (url.protocol === "mailto:") return url.href;
    if (url.protocol !== "http:" && url.protocol !== "https:") return "";
    var page;
    try { page = new URL(base).origin; } catch (e) { page = ""; }
    return url.origin === page ? "" : url.href;
  }
  // link-target:end
  function attr(el, name) {
    var v = el.getAttribute(name);
    return v == null ? "" : v;
  }
  if (CFG.label === "main") {
    // Bubble phase on window: the page's own handlers (file links, unit
    // references, routers) run first and may claim the click.
    window.addEventListener("click", function (e) {
      if (e.defaultPrevented || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
      var a = e.target && e.target.closest ? e.target.closest("a[href], area[href]") : null;
      if (!a || a.hasAttribute("download")) return;
      var href = externalTarget(attr(a, "href") || attr(a, "xlink:href"), document.baseURI, attr(a, "target"));
      if (!href) return;
      e.preventDefault();
      window.open(href, "_blank", "noopener");
    });
  }

  // Downloads. The app saves what a page downloads (<a download>,
  // Content-Disposition: attachment, blob: URLs) to the Downloads folder
  // (src/downloads.rs) and reports each step here through
  // __ARK_DOWNLOAD_EVENT__({id, state: "started" | "done" | "failed", name}):
  // one card per download, bottom right; "Show in folder" once it is saved.
  // The text for an event (pure: tests/js).
  // download-notice:begin
  function downloadNotice(ev, zh) {
    var name = String((ev && ev.name) || (zh ? "文件" : "file"));
    var state = ev && ev.state;
    if (state === "started") return { text: (zh ? "正在下载 " : "Downloading ") + name, reveal: false, error: false, close: false };
    if (state === "done") return { text: (zh ? "已保存到“下载”文件夹：" : "Saved to Downloads: ") + name, reveal: true, error: false, close: true };
    return { text: (zh ? "下载失败：" : "Download failed: ") + name, reveal: false, error: true, close: true };
  }
  // download-notice:end
  var DL_TEXT = { zh: { reveal: "在文件夹中显示", close: "关闭" }, en: { reveal: "Show in folder", close: "Close" } };
  function downloadStyle() {
    if (document.getElementById("ark-download-style")) return;
    var style = document.createElement("style");
    style.id = "ark-download-style";
    style.textContent =
      "#ark-downloads{position:fixed;right:16px;bottom:96px;z-index:2147483601;display:flex;flex-direction:column;gap:8px;align-items:flex-end;max-width:min(420px,calc(100vw - 32px))}" +
      "#ark-downloads .ark-dl{display:flex;align-items:center;gap:8px 10px;padding:10px 12px;border:1px solid var(--line,#d7dce4);border-radius:10px;" +
      "background:var(--card,#fff);color:var(--ink,#1f2937);box-shadow:0 8px 24px rgba(15,23,42,.16);font:13px/1.45 var(--font,system-ui,sans-serif)}" +
      "#ark-downloads .ark-dl span{flex:1 1 auto;min-width:0;overflow-wrap:anywhere}" +
      "#ark-downloads .ark-dl.err{border-color:var(--danger,#dc2626)}" +
      "#ark-downloads .ark-dl.err span{color:var(--danger,#b91c1c)}" +
      "#ark-downloads button{flex:none;height:28px;padding:0 10px;border-radius:7px;border:1px solid var(--line,#d7dce4);background:transparent;color:inherit;font:inherit;cursor:pointer}" +
      "#ark-downloads button.ark-dl-x{width:28px;padding:0;color:var(--sub,#5b6472)}" +
      "#ark-downloads button:focus-visible{outline:2px solid var(--accent,#2563eb);outline-offset:1px}" +
      "@media print{#ark-downloads{display:none}}";
    document.head.appendChild(style);
  }
  function downloadEvent(ev) {
    if (!ev || typeof ev !== "object" || !document.body) return;
    var zh = NOTICE_ZH();
    var view = downloadNotice(ev, zh);
    var labels = DL_TEXT[zh ? "zh" : "en"];
    downloadStyle();
    var list = document.getElementById("ark-downloads");
    if (!list) {
      list = document.createElement("div");
      list.id = "ark-downloads";
      list.setAttribute(DRAG, "false");
      document.body.appendChild(list);
    }
    var key = String(ev.id || "");
    var card = null;
    for (var i = 0; i < list.children.length; i++) {
      if (list.children[i].getAttribute("data-id") === key) card = list.children[i];
    }
    if (!card) {
      card = document.createElement("div");
      card.setAttribute("data-id", key);
      list.appendChild(card);
    }
    card.className = "ark-dl" + (view.error ? " err" : "");
    card.setAttribute("role", view.error ? "alert" : "status");
    card.textContent = "";
    var text = document.createElement("span");
    text.textContent = view.text;
    card.appendChild(text);
    if (view.reveal) {
      var reveal = document.createElement("button");
      reveal.type = "button";
      reveal.textContent = labels.reveal;
      reveal.addEventListener("click", function () {
        invoke("show_download", { id: key }).catch(function () {});
      });
      card.appendChild(reveal);
    }
    if (view.close) {
      var x = document.createElement("button");
      x.type = "button";
      x.className = "ark-dl-x";
      x.title = labels.close;
      x.setAttribute("aria-label", labels.close);
      x.textContent = "×";
      x.addEventListener("click", function () { card.remove(); });
      card.appendChild(x);
      clearTimeout(card._arkTimer);
      card._arkTimer = setTimeout(function () { card.remove(); }, view.error ? 20000 : 12000);
    }
  }
  Object.defineProperty(window, "__ARK_DOWNLOAD_EVENT__", { value: downloadEvent });

  // Update notice. The version to offer, or "" (pure: tests/js).
  // update-notice:begin
  function updateNoticeVersion(info) {
    if (!info || typeof info !== "object" || info.enabled === false) return "";
    var clean = function (v) { return typeof v === "string" ? v.trim().replace(/^v/, "") : ""; };
    var version = clean(info.available);
    if (!version || version === clean(info.current) || version === clean(info.dismissed)) return "";
    return version;
  }
  // update-notice:end
  var NOTICE_ZH = function () {
    var lang = "";
    try { lang = localStorage.getItem("ark.lang") || ""; } catch (e) { lang = ""; }
    return lang ? lang === "zh" : /^zh\b/i.test(navigator.language || "");
  };
  var NOTICE_TEXT = {
    zh: { title: "ARK Studio {v} 可用", update: "更新", later: "稍后", installing: "正在下载更新，安装完成后 ARK Studio 会自动重启。", failed: "更新未能安装：" },
    en: { title: "ARK Studio {v} is available", update: "Update", later: "Later", installing: "Downloading the update. ARK Studio restarts when it is installed.", failed: "The update was not installed: " }
  };
  function noticeStyle() {
    if (document.getElementById("ark-update-notice-style")) return;
    var style = document.createElement("style");
    style.id = "ark-update-notice-style";
    style.textContent =
      "#ark-update-notice{position:fixed;right:16px;bottom:40px;z-index:2147483600;display:flex;flex-wrap:wrap;align-items:center;gap:8px 10px;" +
      "max-width:min(420px,calc(100vw - 32px));padding:10px 12px;border:1px solid var(--line,#d7dce4);border-radius:10px;" +
      "background:var(--card,#fff);color:var(--ink,#1f2937);box-shadow:0 8px 24px rgba(15,23,42,.16);font:13px/1.45 var(--font,system-ui,sans-serif)}" +
      "#ark-update-notice b{font-weight:650;flex:1 1 auto}" +
      "#ark-update-notice .ark-un-dot{width:8px;height:8px;border-radius:50%;background:var(--accent,#2563eb);flex:none}" +
      "#ark-update-notice .ark-un-actions{display:flex;gap:6px;margin-left:auto}" +
      "#ark-update-notice button{height:28px;padding:0 12px;border-radius:7px;border:1px solid var(--line,#d7dce4);background:transparent;color:inherit;font:inherit;cursor:pointer}" +
      "#ark-update-notice button.ark-un-primary{background:var(--accent,#2563eb);border-color:var(--accent,#2563eb);color:var(--accent-ink,#fff);font-weight:600}" +
      "#ark-update-notice button:focus-visible{outline:2px solid var(--accent,#2563eb);outline-offset:1px}" +
      "#ark-update-notice .ark-un-status{flex-basis:100%;font-size:12px;color:var(--sub,#5b6472)}" +
      "#ark-update-notice .ark-un-status:empty{display:none}" +
      "@media print{#ark-update-notice{display:none}}";
    document.head.appendChild(style);
  }
  var noticeRole = "";
  var noticeShown = "";
  function showNotice(info, version) {
    if (noticeShown === version && document.getElementById("ark-update-notice")) return;
    var old = document.getElementById("ark-update-notice");
    if (old) old.remove();
    noticeShown = version;
    noticeStyle();
    var text = NOTICE_TEXT[NOTICE_ZH() ? "zh" : "en"];
    var box = document.createElement("div");
    box.id = "ark-update-notice";
    box.setAttribute("role", "status");
    box.setAttribute(DRAG, "false");
    var dot = document.createElement("span");
    dot.className = "ark-un-dot";
    var title = document.createElement("b");
    title.textContent = text.title.replace("{v}", version);
    var actions = document.createElement("span");
    actions.className = "ark-un-actions";
    var update = document.createElement("button");
    update.type = "button";
    update.className = "ark-un-primary";
    update.textContent = text.update;
    var later = document.createElement("button");
    later.type = "button";
    later.textContent = text.later;
    var status = document.createElement("span");
    status.className = "ark-un-status";
    actions.appendChild(update);
    actions.appendChild(later);
    box.appendChild(dot);
    box.appendChild(title);
    box.appendChild(actions);
    box.appendChild(status);
    later.addEventListener("click", function () {
      box.remove();
      invoke("dismiss_update", { version: version }).catch(function () {});
    });
    update.addEventListener("click", function () {
      // The local page installs (the portable build downloads); a remote
      // page opens the app's settings at About.
      if (noticeRole !== "local") { invoke("open_app_settings", { view: "about" }).catch(function () {}); return; }
      if (info.selfInstall === false) { invoke("open_releases", { version: version }).catch(function () {}); return; }
      update.disabled = true;
      later.disabled = true;
      status.textContent = text.installing;
      invoke("check_update", { install: true }).then(function () {
        status.textContent = "";
        update.disabled = false;
        later.disabled = false;
      }, function (err) {
        status.textContent = text.failed + String((err && err.message) || err);
        update.disabled = false;
        later.disabled = false;
      });
    });
    document.body.appendChild(box);
  }
  var lastLook = 0;
  function lookForUpdate() {
    lastLook = Date.now();
    invoke("update_state").then(function (info) {
      var version = updateNoticeVersion(info);
      if (version) showNotice(info, version);
      else {
        var old = document.getElementById("ark-update-notice");
        if (old) old.remove();
        noticeShown = "";
      }
    }, function () {});
  }
  function watchUpdates() {
    if (CFG.label !== "main" || !document.body) return;
    invoke("desktop_context").then(function (context) {
      noticeRole = (context && context.role) || "";
      if (noticeRole !== "local" && noticeRole !== "remote") return;
      setTimeout(lookForUpdate, 12000);
      setInterval(lookForUpdate, 30 * 60 * 1000);
      window.addEventListener("focus", function () { if (Date.now() - lastLook > 5 * 60 * 1000) lookForUpdate(); });
      // The page found a version itself (Settings > About > Check for updates).
      window.addEventListener("ark:desktop-update", lookForUpdate);
    }, function () {});
  }

  function ready() {
    mark();
    if (!document.documentElement.hasAttribute("data-desktop-shell")) fallbackRegions();
    controls();
    watchUpdates();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", ready);
  else ready();
})();
