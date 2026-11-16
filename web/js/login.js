// Login page helper: carry the pre-login hash route/pairing fragment through
// the form.  The fragment never reaches the server, so JavaScript is the only
// place that can preserve `#pair=…` (and `#/overview`) across sign-in.  If this
// script is unavailable the form still posts; the user lands on the app and
// can paste the one-time pairing code in the gate.

// Login-page language.  Before sign-in only login.css and login.js are public,
// so this page cannot import the app's i18n module; it carries the few strings
// it needs.  The language follows the app's saved choice (or ?lang=).
/* i18n-ignore-start */
var LOGIN_TEXT = {
  en: {
    "pair.busy": "Pairing this device…",
    "pair.ok": "Paired. Opening ARK…",
    "pair.failed": "Pairing failed: {error} Sign in below.",
    "pair.invalid": "The pairing code is invalid or has expired.",
  },
  zh: {
    "theme": "主题", "light": "浅色", "dark": "深色",
    "doc_title": "登录 · ARK Studio",
    "eyebrow": "受保护的服务器",
    "title": "登录",
    "lead": "进入你的研究工作台。",
    "username": "用户名",
    "password": "密码",
    "submit": "登录",
    "remember": "7 天内自动登录",
    "busy": "正在登录…",
    "facts": "会话安全",
    "fact_rate": "登录限速",
    "foot": "让研究彼此连接。",
    "pair.busy": "正在配对此设备…",
    "pair.ok": "已配对，正在打开 ARK…",
    "pair.failed": "配对失败：{error} 请在下方登录。",
    "pair.invalid": "配对码无效或已过期。",
  },
  // Messages rendered by the server (arkstudio/server.py, arkstudio/auth.py).
  server_zh: {
    "Sign-in failed.": "登录失败。",
    "Invalid username or password.": "用户名或密码错误。",
    "Too many sign-in attempts. Wait a few minutes and try again.": "登录尝试次数过多，请等几分钟再试。",
    "Cross-site sign-in attempts are rejected.": "已拒绝跨站登录请求。",
    "The sign-in form expired or was not opened from this server. Reload and try again.": "登录表单已过期，或不是从此服务器打开的。请刷新后重试。",
    "This page is served over HTTPS.": "此页面通过 HTTPS 提供。",
    // Pairing-claim errors (api/mobile/pair/claim).
    "The pairing code is invalid or has expired.": "配对码无效或已过期。",
    "This pairing code has already been used.": "此配对码已被使用。",
    "The pairing code has expired.": "配对码已过期。",
    "This pairing session was revoked.": "此配对会话已被撤销。",
    "This pairing code belongs to a different ARK account.": "此配对码属于另一个 ARK 账户。",
    "This pairing code has no bound account.": "此配对码未绑定账户。",
    "The paired project is no longer available.": "配对的项目已不可用。",
    "Too many pairing attempts. Wait a minute and try again.": "配对尝试次数过多。请等待一分钟后重试。",
    "This page is served over plain HTTP. Use an HTTPS reverse proxy before exposing ARK to the public internet; credentials and the session cookie are otherwise readable on the network.": "此页面通过明文 HTTP 提供。将 ARK 暴露到公网前，请先配置 HTTPS 反向代理；否则凭据和会话 cookie 在网络上可被读取。",
  },
};
/* i18n-ignore-end */
var LOGIN_LANG = (function () {
  var query = new URLSearchParams(window.location.search).get("lang");
  if (query === "zh" || query === "en") {
    try { localStorage.setItem("ark.lang", query); } catch (_) {}
    return query;
  }
  try { return localStorage.getItem("ark.lang") === "zh" ? "zh" : "en"; } catch (err) { return "en"; }
})();

function loginText(key, vars) {
  var text = (LOGIN_LANG === "zh" && LOGIN_TEXT.zh[key]) || LOGIN_TEXT.en[key] || key;
  Object.keys(vars || {}).forEach(function (name) { text = text.split("{" + name + "}").join(String(vars[name])); });
  return text;
}

(function () {
  if (LOGIN_LANG !== "zh") return;
  var zh = LOGIN_TEXT.zh;
  var server = LOGIN_TEXT.server_zh;
  document.documentElement.lang = "zh-CN";
  document.title = zh.doc_title;
  document.querySelectorAll("[data-i18n]").forEach(function (el) {
    var text = zh[el.getAttribute("data-i18n")];
    if (text) el.textContent = text;
  });
  document.querySelectorAll("[data-i18n-aria]").forEach(function (el) {
    var text = zh[el.getAttribute("data-i18n-aria")];
    if (text) el.setAttribute("aria-label", text);
  });
  var transport = document.querySelector(".login-transport");
  if (transport && server[transport.textContent.trim()]) transport.textContent = server[transport.textContent.trim()];
  var error = document.querySelector(".login-error");
  if (error) {
    error.childNodes.forEach(function (node) {
      var text = node.textContent.trim();
      if (server[text]) node.textContent = (node.nodeType === 3 ? " " : "") + server[text];
    });
  }
})();

(function () {
  var nextInput = document.getElementById("login-next");
  if (nextInput && window.location.hash) {
    var base = String(nextInput.value || "").split("#")[0];
    if (!base) base = window.location.pathname + window.location.search;
    if (base.indexOf("#") === -1) nextInput.value = base + window.location.hash;
  }

  var form = document.querySelector(".login-form");
  if (!form) return;
  form.addEventListener("submit", function () {
    window.ARKEntryGraph?.progress(0, true);
    requestAnimationFrame(function () { window.ARKEntryGraph?.progress(.8); });
    var button = form.querySelector(".login-submit");
    if (!button) return;
    button.disabled = true;
    button.classList.add("is-busy");
  });
  var username = form.querySelector('input[name="username"]');
  var password = form.querySelector('input[name="password"]');
  if (username && password && username.value && !password.value) {
    password.focus();
  }
})();


// A QR generated by a signed-in desktop already carries a one-time pairing
// proof.  Claim it before showing the password form: the server returns a
// project-scoped paired-device session and sets its HttpOnly cookie, so a
// scanned phone never asks for the account password again.
(function () {
  var match = String(window.location.hash || "").match(/(?:^#|[?&])pair=([^&]+)/);
  if (!match) return;
  var code;
  try { code = decodeURIComponent(match[1]); } catch (err) { return; }
  if (!code) return;
  var form = document.querySelector(".login-form");
  var nextInput = document.getElementById("login-next");
  if (!form || !nextInput) return;

  var status = document.createElement("div");
  status.className = "login-pairing";
  status.setAttribute("role", "status");
  status.textContent = loginText("pair.busy");
  window.ARKEntryGraph?.progress(0, true);
  requestAnimationFrame(function () { window.ARKEntryGraph?.progress(.65); });
  form.parentNode.insertBefore(status, form);
  form.querySelectorAll("input,button").forEach(function (el) { el.disabled = true; });

  var target = String(nextInput.value || "/");
  target = target.replace(/([#?&])pair=[^&]*/g, "").replace(/[?&]$/, "");
  if (!target) target = "/";

  fetch("api/mobile/pair/claim", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: code }),
  }).then(function (response) {
    return response.json().then(function (data) {
      if (!response.ok) {
        throw new Error((data && data.error && data.error.message)
          || loginText("pair.invalid"));
      }
      return data;
    });
  }).then(function (data) {
    window.ARKEntryGraph?.progress(1);
    try {
      localStorage.setItem("ark.mobile.token", String(data.mobile_token || ""));
      if (data.permission) localStorage.setItem("ark.mobile.permission", data.permission);
      if (data.expires_at) localStorage.setItem("ark.mobile.expires", data.expires_at);
    } catch (err) { /* cookie still carries the session */ }
    status.className = "login-pairing is-ok";
    status.textContent = loginText("pair.ok");
    window.location.replace(target);
  }).catch(function (err) {
    window.ARKEntryGraph?.progress(1);
    status.className = "login-pairing is-error";
    var error = (LOGIN_LANG === "zh" && LOGIN_TEXT.server_zh[err.message]) || err.message;
    status.textContent = loginText("pair.failed", { error: error });
    nextInput.value = target;
    form.querySelectorAll("input,button").forEach(function (el) { el.disabled = false; });
    var password = form.querySelector('input[name="password"]');
    if (password) password.focus();
  });
})();
