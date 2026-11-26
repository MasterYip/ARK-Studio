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
    "pair.confirm": "Approve code {code} in ARK on the computer (Settings › Mobile).",
    "invite.loading": "Checking the invite link…",
    "invite.title": "Join a project",
    "invite.invited": "{name} invited you to {project}",
    "invite.access": "Access: {perm}",
    "invite.lead_signup": "Sign in to join, or create an account first.",
    "invite.lead_signin": "Sign in with your account to join.",
    "invite.tab_signin": "I have an account",
    "invite.tab_signup": "Create account",
    "perm.read": "Read", "perm.write": "Edit", "perm.admin": "Admin",
    "state.invalid": "This invite link is not valid. Check that you copied all of it.",
    "state.expired": "This invite link has expired. Ask the person who sent it for a new one.",
    "state.revoked": "This invite link was revoked. Ask the person who sent it for a new one.",
    "state.used_up": "This invite link has already been used. Ask the person who sent it for a new one.",
    "signup.username": "Username",
    "signup.username_hint": "Letters, digits and . _ @ + - (up to 64)",
    "signup.display": "Display name (optional)",
    "signup.password": "Password",
    "signup.password_hint": "At least {n} characters",
    "signup.confirm": "Confirm password",
    "signup.mismatch": "The passwords do not match.",
    "signup.short": "The password must be at least {n} characters.",
    "signup.submit": "Create account and join",
    "signup.busy": "Creating account…",
    "signup.done": "Account created. Opening the project…",
    "signup.failed": "Could not create the account.",
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
    "pair.confirm": "请在电脑上的 ARK（设置 › 移动端）批准代码 {code}。",
    "invite.loading": "正在检查邀请链接…",
    "invite.title": "加入项目",
    "invite.invited": "{name} 邀请你加入「{project}」",
    "invite.access": "权限：{perm}",
    "invite.lead_signup": "登录后加入；没有账号可以先创建一个。",
    "invite.lead_signin": "使用你的账号登录后加入。",
    "invite.tab_signin": "我有账号",
    "invite.tab_signup": "创建账号",
    "perm.read": "只读", "perm.write": "可编辑", "perm.admin": "管理员",
    "state.invalid": "邀请链接无效，请检查是否完整复制。",
    "state.expired": "邀请链接已过期，请向发送者索取新链接。",
    "state.revoked": "邀请链接已被撤销，请向发送者索取新链接。",
    "state.used_up": "邀请链接已被使用，请向发送者索取新链接。",
    "signup.username": "用户名",
    "signup.username_hint": "字母、数字和 . _ @ + -，最多 64 个字符",
    "signup.display": "显示名称（可选）",
    "signup.password": "密码",
    "signup.password_hint": "至少 {n} 个字符",
    "signup.confirm": "确认密码",
    "signup.mismatch": "两次输入的密码不一致。",
    "signup.short": "密码至少需要 {n} 个字符。",
    "signup.submit": "创建账号并加入",
    "signup.busy": "正在创建账号…",
    "signup.done": "账号已创建，正在打开项目…",
    "signup.failed": "无法创建账号。",
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
    // Invite links (api/auth/invite/*).
    "This username is taken.": "该用户名已被占用。",
    "auth username must be 1-64 characters: letters, digits, . _ @ + -": "用户名需为 1–64 个字符：字母、数字和 . _ @ + -。",
    "This invite link is not valid. Check that you copied all of it.": "邀请链接无效，请检查是否完整复制。",
    "This invite link has expired. Ask for a new one.": "邀请链接已过期，请索取新链接。",
    "This invite link was revoked. Ask for a new one.": "邀请链接已被撤销，请索取新链接。",
    "This invite link has already been used. Ask for a new one.": "邀请链接已被使用，请索取新链接。",
    "This invite link does not allow creating an account. Sign in with an existing account.": "此邀请链接不允许创建账号，请使用已有账号登录。",
    "Too many invite attempts. Wait a few minutes and try again.": "邀请操作过于频繁，请等几分钟再试。",
    "The form expired or was not opened from this server. Reload and try again.": "表单已过期，或不是从此服务器打开的。请刷新后重试。",
    "Cross-site invite requests are rejected.": "已拒绝跨站邀请请求。",
    "This page is served over plain HTTP. Use an HTTPS reverse proxy before exposing ARK to the public internet; credentials and the session cookie are otherwise readable on the network.": "此页面通过明文 HTTP 提供。将 ARK 暴露到公网前，请先配置 HTTPS 反向代理；否则凭据和会话 cookie 在网络上可被读取。",
  },
};
/* i18n-ignore-end */
var LOGIN_LANG = (function () {
  var query = new URLSearchParams(window.location.search).get("lang");
  if (query === "zh" || query === "en") {
    try { localStorage.setItem("ark.lang", query); localStorage.removeItem("ark.langAuto"); } catch (_) {}
    return query;
  }
  try { return localStorage.getItem("ark.lang") === "zh" ? "zh" : "en"; } catch (err) { return "en"; }
})();

function loginText(key, vars) {
  var text = (LOGIN_LANG === "zh" && LOGIN_TEXT.zh[key]) || LOGIN_TEXT.en[key] || key;
  Object.keys(vars || {}).forEach(function (name) { text = text.split("{" + name + "}").join(String(vars[name])); });
  return text;
}

// This browser's device id (same rules as ensureDeviceId in mobile-core.js):
// the Android app's ark_device cookie wins, else a random id kept in
// localStorage and mirrored into the cookie.
function loginDeviceId() {
  var valid = /^[A-Za-z0-9_-]{16,128}$/;
  var cookie = "";
  document.cookie.split(";").forEach(function (part) {
    var kv = part.trim().split("=");
    if (kv[0] === "ark_device") cookie = decodeURIComponent(kv.slice(1).join("="));
  });
  var id = valid.test(cookie) ? cookie : "";
  var stored = "";
  try { stored = localStorage.getItem("ark.device.id") || ""; } catch (err) { /* storage disabled */ }
  if (!id && valid.test(stored)) id = stored;
  if (!id && window.crypto && window.crypto.getRandomValues) {
    var bytes = new Uint8Array(24);
    window.crypto.getRandomValues(bytes);
    id = btoa(String.fromCharCode.apply(null, bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }
  if (!id) return "";
  if (id !== stored) {
    try { localStorage.setItem("ark.device.id", id); } catch (err) { /* storage disabled */ }
  }
  if (id !== cookie) {
    document.cookie = "ark_device=" + id + "; Path=/; Max-Age=315360000; SameSite=Lax"
      + (location.protocol === "https:" ? "; Secure" : "");
  }
  return id;
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

  function post(path, body) {
    return fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (response) {
      return response.json().then(function (data) {
        if (!response.ok) {
          throw new Error((data && data.error && data.error.message)
            || loginText("pair.invalid"));
        }
        return data;
      });
    });
  }
  // Through a Cloudflare Tunnel the computer confirms the 6-digit code first.
  function confirmed(first) {
    if (!first || !first.pending) return first;
    var poll = function (data) {
      if (!data || !data.pending) return data;
      status.textContent = loginText("pair.confirm",
        { code: String(first.confirm_code || "").replace(/^(\d{3})(\d{3})$/, "$1 $2") });
      return new Promise(function (resolve) { setTimeout(resolve, 2000); }).then(function () {
        return post("api/mobile/pair/claim/status",
          { code: code, claim_id: first.claim_id, claim_secret: first.claim_secret });
      }).then(poll);
    };
    return poll(first);
  }

  post("api/mobile/pair/claim", { token: code, device_id: loginDeviceId() || undefined }).then(confirmed).then(function (data) {
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


// Invite links: <server>/#/join/<token>.  Signed out, the server sends the
// browser here and the hash comes along (the first block above keeps it in
// "next", so signing in lands on the Join dialog).  Show who invited whom,
// and, when the link allows it, a form that creates the account, joins the
// project and signs in.  The token is sent in POST bodies only.
(function () {
  var match = String(window.location.hash || "").match(/^#\/?join\/([A-Za-z0-9_-]{16,128})/);
  if (!match) return;
  var token = match[1];
  var form = document.querySelector(".login-form");
  var nextInput = document.getElementById("login-next");
  if (!form) return;
  var csrfInput = form.querySelector('input[name="csrf"]');
  // A failed sign-in (wrong password) came back from the server: keep sign-in open.
  var signinFailed = Boolean(document.querySelector(".login-error"));
  var serverText = function (text) {
    return (LOGIN_LANG === "zh" && LOGIN_TEXT.server_zh[text]) || text;
  };
  var el = function (tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  };
  var banner = el("div", "login-invite", loginText("invite.loading"));
  banner.setAttribute("role", "status");
  banner.setAttribute("data-invite-banner", "loading");
  form.parentNode.insertBefore(banner, form);

  function unusable(message) {
    banner.className = "login-invite is-error";
    banner.setAttribute("data-invite-banner", "unusable");
    banner.textContent = message;
    // Signing in still works; it just does not open the Join dialog.
    if (nextInput) nextInput.value = String(nextInput.value || "/").split("#")[0] || "/";
  }

  function field(label, input, hint) {
    var wrap = el("label", "login-field");
    wrap.appendChild(el("span", null, label));
    wrap.appendChild(input);
    if (hint) wrap.appendChild(el("span", "login-hint", hint));
    return wrap;
  }

  function input(name, type, autocomplete, max) {
    var node = el("input", "login-input");
    node.name = name;
    node.type = type;
    node.autocomplete = autocomplete;
    node.maxLength = max;
    if (type === "text") { node.autocapitalize = "off"; node.spellcheck = false; }
    return node;
  }

  function signupForm(data) {
    var min = Number(data.password_min) || 8;
    var box = el("form", "login-form login-signup");
    box.setAttribute("data-invite-signup", "");
    box.noValidate = true;
    var username = input("username", "text", "username", 64);
    username.required = true;
    var display = input("display_name", "text", "name", 80);
    var password = input("password", "password", "new-password", 1024);
    var confirm = input("confirm", "password", "new-password", 1024);
    var error = el("div", "login-error");
    error.hidden = true;
    error.setAttribute("role", "alert");
    var button = el("button", "login-submit");
    button.type = "submit";
    button.appendChild(el("span", "login-submit-label", loginText("signup.submit")));
    button.appendChild(el("span", "login-submit-busy", loginText("signup.busy")));
    box.appendChild(error);
    box.appendChild(field(loginText("signup.username"), username, loginText("signup.username_hint")));
    box.appendChild(field(loginText("signup.display"), display));
    box.appendChild(field(loginText("signup.password"), password, loginText("signup.password_hint", { n: min })));
    box.appendChild(field(loginText("signup.confirm"), confirm));
    box.appendChild(button);
    var fail = function (message) {
      error.hidden = false;
      error.textContent = message;
      button.disabled = false;
      button.classList.remove("is-busy");
    };
    box.addEventListener("submit", function (event) {
      event.preventDefault();
      if (!username.value.trim()) { username.focus(); return; }
      if (password.value.length < min) { fail(loginText("signup.short", { n: min })); password.focus(); return; }
      if (password.value !== confirm.value) { fail(loginText("signup.mismatch")); confirm.focus(); return; }
      error.hidden = true;
      button.disabled = true;
      button.classList.add("is-busy");
      fetch("api/auth/invite/signup", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          token: token, csrf: csrfInput ? csrfInput.value : "", username: username.value.trim(),
          display_name: display.value.trim(), password: password.value,
        }),
      }).then(function (response) {
        return response.json().catch(function () { return {}; }).then(function (body) {
          if (!response.ok) throw new Error((body && body.error && body.error.message) || loginText("signup.failed"));
          return body;
        });
      }).then(function (body) {
        banner.className = "login-invite";
        banner.textContent = loginText("signup.done");
        try { sessionStorage.setItem("ark.tabProject", String((body.project && body.project.id) || "")); } catch (_) {}
        // They came to work in this project: no first-run Welcome offering a new one.
        try { localStorage.setItem("ark.welcome.v1:" + encodeURIComponent(String(body.user || "")), "1"); } catch (_) {}
        window.location.replace("./#/home");
      }).catch(function (err) {
        fail(serverText(err.message));
      });
    });
    return box;
  }

  fetch("api/auth/invite/preview", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: token }),
  }).then(function (response) {
    return response.json().catch(function () { return {}; }).then(function (body) {
      if (!response.ok) throw new Error((body && body.error && body.error.message) || loginText("state.invalid"));
      return body;
    });
  }).then(function (data) {
    if (data.state !== "valid") {
      var known = ["invalid", "expired", "revoked", "used_up"].indexOf(data.state) >= 0 ? data.state : "invalid";
      unusable(loginText("state." + known));
      return;
    }
    var project = (data.project && (data.project.name || data.project.id)) || "";
    var inviter = (data.inviter && (data.inviter.display_name || data.inviter.username)) || "";
    banner.textContent = "";
    banner.setAttribute("data-invite-banner", "valid");
    banner.appendChild(el("div", "login-invite-title", loginText("invite.invited", { name: inviter, project: project })));
    banner.appendChild(el("div", "login-invite-meta", loginText("invite.access", { perm: loginText("perm." + data.permission) })));
    var title = document.getElementById("login-title");
    if (title) title.textContent = loginText("invite.title");
    var lead = document.querySelector(".login-lead");
    if (lead) lead.textContent = loginText(data.signup ? "invite.lead_signup" : "invite.lead_signin");
    if (!data.signup) return;
    var signup = signupForm(data);
    var switcher = el("div", "login-switch");
    switcher.setAttribute("role", "group");
    var tabSignup = el("button", null, loginText("invite.tab_signup"));
    var tabSignin = el("button", null, loginText("invite.tab_signin"));
    tabSignup.type = "button";
    tabSignin.type = "button";
    tabSignup.setAttribute("data-invite-tab", "signup");
    tabSignin.setAttribute("data-invite-tab", "signin");
    var show = function (mode) {
      var up = mode === "signup";
      signup.hidden = !up;
      form.hidden = up;
      tabSignup.setAttribute("aria-pressed", String(up));
      tabSignin.setAttribute("aria-pressed", String(!up));
      var focus = (up ? signup : form).querySelector('input:not([type="hidden"])');
      if (focus) focus.focus();
    };
    tabSignup.addEventListener("click", function () { show("signup"); });
    tabSignin.addEventListener("click", function () { show("signin"); });
    switcher.appendChild(tabSignup);
    switcher.appendChild(tabSignin);
    form.parentNode.insertBefore(switcher, form);
    form.parentNode.insertBefore(signup, form.nextSibling);
    show(signinFailed ? "signin" : "signup");
  }).catch(function (err) {
    unusable(serverText(err.message));
  });
})();
