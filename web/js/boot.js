// A classic script can report failures before app.js/i18n evaluate.
(function () {
  document.documentElement.dataset.boot = "booted";
  var host = document.getElementById("ark-startup"), stage = host.querySelector(".entry-stage");
  var track = host.querySelector('[role="progressbar"]'), bar = track.querySelector("i");
  var lang;
  try { lang = new URLSearchParams(location.search).get("lang") || localStorage.getItem("ark.lang"); } catch (_) {}
  var zh = lang === "zh", ready = false, current = 0, slowTimer;
  /* i18n-ignore-start: classic boot runs before the app's i18n module */
  var stages = zh ? ["正在加载工作台", "正在检查会话", "正在读取项目", "正在打开视图"]
    : ["Loading workbench", "Checking session", "Reading project", "Opening view"];
  host.querySelector("h1").textContent = zh ? "正在打开 ARK" : "Opening ARK";
  var retry = host.querySelector(".entry-retry");
  retry.textContent = zh ? "重试" : "Retry";
  host.querySelector(".entry-slow-note").textContent = zh ? "加载时间较长，仍在尝试连接。" : "This is taking longer. Still trying to connect.";
  retry.addEventListener("click", function () { location.reload(); });
  /* i18n-ignore-end */
  // First visit (no service worker yet, so the app's modules are still
  // downloading): ask for the session and the project data right away, in
  // parallel with the module graph.  api.js adopts these replies; nothing is
  // shown before the session reply says who is signed in.  Repeat visits
  // skip this: their modules come from the worker and their data revalidates
  // with validators from the cache.  Pairing links and phone tokens keep the
  // app's own order.
  window.ARKEarly = null;
  try {
    var controlled = navigator.serviceWorker && navigator.serviceWorker.controller;
    var pairing = /[#?&]pair=/.test(location.hash) || !!localStorage.getItem("ark.mobile.token");
    if (!controlled && !pairing && window.fetch) {
      var project = sessionStorage.getItem("ark.tabProject") || "";
      if (!project) {
        try { project = JSON.parse(localStorage.getItem("ark.cache.lastProject") || "{}").project || ""; } catch (_) { project = ""; }
      }
      var base = new URL("./", document.baseURI);
      var get = function (path, forProject) {
        var headers = forProject ? { "X-ARK-Project": forProject } : {};
        var started = performance.now();
        return fetch(new URL(path.slice(1), base).href, { headers: headers, credentials: "same-origin" })
          .then(function (response) {
            return response.text().then(function (text) {
              return { status: response.status, ok: response.ok, headers: response.headers, text: text,
                ms: Math.round(performance.now() - started) };
            });
          });
      };
      var bootstrap = get("/api/bootstrap", project);
      var requests = { "/api/mobile/session": get("/api/mobile/session", ""), "/api/bootstrap": bootstrap };
      var projects = { "/api/bootstrap": project };
      var route = location.hash.replace(/^#\/?/, "");
      // Home also needs the overview, the Map (and Research > Idea) the graph
      // snapshot: for the project the bootstrap reply names when this
      // browser does not know one yet.
      var follow = !route || /^(home|overview)(\/|$)/.test(route) ? "/api/project/overview"
        : /^(map|research\/idea|research\/graph)(\/|$)/.test(route) ? "/api/graph/snapshot?bodies=0" : "";
      if (follow) {
        var served = project ? Promise.resolve(project) : bootstrap.then(function (reply) {
          return reply.headers.get("X-ARK-Project-Id") || "";
        });
        projects[follow] = served;
        requests[follow] = served.then(function (id) {
          if (!id) throw new Error("no project");
          return get(follow, id);
        });
      }
      window.ARKEarly = { project: project, projects: projects, requests: requests };
    }
  } catch (_) { window.ARKEarly = null; }
  // The active language's strings are a module that i18n.js imports at
  // run time; preload it now so it arrives with the static module graph.
  try {
    var build = document.querySelector('meta[name="ark-build"]');
    var active = lang === "zh" ? ["i18n-zh.js", "i18n_server_zh.js"] : ["i18n-en.js"];
    active.forEach(function (file) {
      var link = document.createElement("link");
      link.rel = "modulepreload";
      link.href = "./js/" + file + (build ? "?v=" + build.content : "");
      document.head.appendChild(link);
    });
  } catch (_) { /* the import still loads it */ }
  // The first-screen module graph (listed by the server in #ark-modules):
  // fetch all of it in parallel instead of one import level per round trip.
  try {
    var listed = document.getElementById("ark-modules");
    var version = document.querySelector('meta[name="ark-build"]');
    JSON.parse(listed ? listed.textContent : "[]").forEach(function (path) {
      var link = document.createElement("link");
      link.rel = "modulepreload";
      link.href = "./" + path + (version ? "?v=" + version.content : "");
      document.head.appendChild(link);
    });
  } catch (_) { /* modules still load through their imports */ }
  // Start preloads in head, promote them without blocking the first paint.
  var styles = Promise.all(Array.from(document.querySelectorAll("link[data-ark-style]"), function (preload) {
    return new Promise(function (resolve, reject) {
      var link = document.createElement("link");
      link.rel = "stylesheet"; link.href = preload.href;
      link.onload = resolve; link.onerror = function () { reject(new Error("Stylesheet unavailable")); };
      document.head.appendChild(link);
    });
  }));
  function progress(index) {
    if (ready) return;
    clearTimeout(slowTimer);
    host.classList.remove("is-slow");
    slowTimer = setTimeout(function () { if (!ready) host.classList.add("is-slow"); }, 15000);
    current = index;
    stage.textContent = stages[Math.min(index, 3)];
    track.setAttribute("aria-valuenow", String(index));
    track.setAttribute("aria-valuetext", stage.textContent);
    bar.style.width = [8, 52, 70, 90, 100][index] + "%";
    window.ARKEntryGraph?.progress([0, .5, .7, .9, 1][index]);
  }
  function finish() {
    if (ready) return;
    progress(4); ready = true; clearTimeout(slowTimer);
    performance.mark("ark-ready");
    // Cached loads have no artificial minimum animation duration.
    requestAnimationFrame(function () {
      window.ARKEntryGraph?.destroy(); host.remove();
      resources?.disconnect();
      window.removeEventListener("error", onError, true);
      window.removeEventListener("unhandledrejection", onRejection);
    });
  }
  function fail(message) {
    if (ready) return;
    clearTimeout(slowTimer);
    host.classList.add("has-error"); stage.textContent = message;
    track.removeAttribute("aria-valuenow");
  }
  function onError(event) {
    if (event.message) fail(event.message);
    else if (event.target?.matches?.("script,link[rel=stylesheet]")) {
      fail(zh ? "资源加载失败，请重试。" : "A resource could not be loaded. Please retry."); // i18n-ignore: pre-module error
    }
  }
  function onRejection(event) {
    var reason = event.reason;
    fail(reason && reason.message ? reason.message : String(reason));
  }
  window.ARKBoot = { styles: styles, progress: progress, finish: finish, fail: fail };
  styles.catch(function (error) { fail(error.message); });
  var completed = 0, resources;
  if (window.PerformanceObserver) {
    resources = new PerformanceObserver(function (list) {
      if (current || ready) return;
      completed += list.getEntries().filter(function (entry) { return /\.(js|css)(\?|$)/.test(entry.name); }).length;
      // Growth advances on completed downloads, then session/project/view
      // milestones. It is not presented as a byte percentage.
      window.ARKEntryGraph?.progress(Math.min(.48, completed / 140));
    });
    resources.observe({ type: "resource", buffered: true });
  }
  window.addEventListener("error", onError, true);
  window.addEventListener("unhandledrejection", onRejection);
  progress(0);
})();
