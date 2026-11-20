// Theme and boot controls are ready before the shared Force module loads.
(function () {
  "use strict";
  var lang;
  // In the Android app the UI follows the phone's language until the user picks one
  // in ARK (state.js setLanguage and a ?lang= login link clear ark.langAuto).  This is the
  // first script on index.html and login.html, so every later reader sees the value.
  try {
    if (/\bARKAndroid\//.test(navigator.userAgent || "")
        && (!localStorage.getItem("ark.lang") || localStorage.getItem("ark.langAuto") === "1")) {
      var system = (navigator.languages && navigator.languages[0]) || navigator.language || "";
      localStorage.setItem("ark.lang", /^zh\b/i.test(system) ? "zh" : "en");
      localStorage.setItem("ark.langAuto", "1");
    }
  } catch (_) {}
  try { lang = new URLSearchParams(location.search).get("lang") || localStorage.getItem("ark.lang"); } catch (_) {}
  var zh = lang === "zh", root = document.documentElement;
  var theme = new URLSearchParams(location.search).get("theme");
  try {
    var pref = localStorage.getItem("ark.themePref");
    theme = theme || (pref === "system" ? (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light") : pref) || localStorage.getItem("ark.theme");
  } catch (_) {}
  root.dataset.theme = theme === "dark" ? "dark" : "light";
  try {
    var display = JSON.parse(localStorage.getItem("ark.display") || "{}");
    var scale = Number(display.font_scale);
    root.style.setProperty("--font-scale", String(Number.isFinite(scale) ? Math.max(1, Math.min(1.5, scale)) : 1));
    root.dataset.contrast = display.high_contrast === true ? "high" : "normal";
  } catch (_) {}
  var button = document.querySelector("[data-startup-theme-toggle]");
  var paths = {
    light: ["M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10Z", "M12 1v2", "M12 21v2", "M4.2 4.2l1.4 1.4", "M18.4 18.4l1.4 1.4", "M1 12h2", "M21 12h2", "M4.2 19.8l1.4-1.4", "M18.4 5.6l1.4-1.4"],
    dark: ["M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"],
  };
  /* i18n-ignore-start */
  function syncTheme() {
    var dark = root.dataset.theme === "dark", next = dark ? "light" : "dark";
    if (button) {
      button.title = button.ariaLabel = dark ? (zh ? "切换到浅色" : "Switch to light") : (zh ? "切换到深色" : "Switch to dark");
      button.setAttribute("aria-pressed", String(dark));
      button.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + paths[next].map(function (d) { return '<path d="' + d + '"/>'; }).join("") + '</svg>';
    }
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = dark ? "#0f141c" : "#f5f6f8";
  }
  /* i18n-ignore-end */
  button?.addEventListener("click", function () {
    var value = root.dataset.theme === "dark" ? "light" : "dark";
    root.dataset.theme = value;
    try { localStorage.setItem("ark.theme", value); localStorage.setItem("ark.themePref", value); localStorage.setItem("ark.loginTheme", value); } catch (_) {}
    syncTheme();
  });
  syncTheme();
  var host = document.querySelector(".entry-graph");
  if (!host) return;
  var engine = null, destroyed = false, value = host.dataset.entry === "login" ? 1 : 0;
  var observer = new MutationObserver(function () { syncTheme(); engine?.draw(); });
  observer.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
  window.ARKEntryGraph = {
    progress: function (next, reset) { value = Math.max(0, Math.min(1, next)); engine?.progress(value, reset); },
    destroy: function () { destroyed = true; observer.disconnect(); engine?.destroy(); },
  };
  import("./startup-force.js").then(function (module) {
    if (!destroyed) engine = module.createEntryForce(host, value);
  }).catch(function () { /* Sign-in and boot remain usable if the backdrop is unavailable. */ });
})();
