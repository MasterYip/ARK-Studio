// ARK Studio desktop: the Settings page (bundled; tray "Settings…", the
// local ARK page's "Connect to a server…" and the status page open it).
import { t, applyI18n } from "./i18n.js";
import { unreachableMessage } from "./connect-model.js";

const invoke = (cmd, args = {}) => window.__TAURI_INTERNALS__.invoke(cmd, args);
const $ = (id) => document.getElementById(id);

let state = null;

function showStatus(prefix, text, { busy = false, error = false, log = "", openAnyway = false, showLog = false } = {}) {
  const box = $(`${prefix}-status`);
  box.classList.remove("hidden");
  box.classList.toggle("is-busy", busy);
  box.classList.toggle("is-error", error);
  $(`${prefix}-status-text`).textContent = text;
  const logEl = $(`${prefix}-status-log`);
  if (logEl) {
    logEl.textContent = log;
    logEl.classList.toggle("hidden", !log);
  }
  $("open-anyway").classList.toggle("hidden", !(prefix === "remote" && openAnyway));
  if (prefix === "local") $("show-log").classList.toggle("hidden", !showLog);
}

function hideStatus(prefix) {
  $(`${prefix}-status`).classList.add("hidden");
}

function splitError(err) {
  const text = String(err?.message || err || "");
  const at = text.indexOf("\n\n");
  return at < 0 ? { message: text, log: "" } : { message: text.slice(0, at), log: text.slice(at + 2) };
}

function renderLocalState() {
  $("local-state").textContent = state.local ? t("local.running", { port: state.local.port }) : t("local.stopped");
  $("local-state").classList.toggle("on", !!state.local);
}

function renderServers() {
  const list = $("recent");
  const data = $("remote-history");
  list.replaceChildren();
  data.replaceChildren();
  for (const server of state.servers || []) {
    const opt = document.createElement("option");
    opt.value = server.url;
    data.append(opt);
    const li = document.createElement("li");
    const label = document.createElement("span");
    label.className = "server-label";
    const name = document.createElement("b");
    name.textContent = server.label;
    const url = document.createElement("small");
    url.textContent = server.linked
      ? `${server.url} · ${t("servers.linked", { user: server.user || "?" })}`
      : server.url;
    label.append(name, url);
    label.title = server.url;
    // Desktop link: the app lists and creates projects there (a revocable
    // device token; no password). Link on the server's own page.
    const link = document.createElement("button");
    link.type = "button";
    link.className = "btn small";
    link.textContent = server.linked ? t("servers.unlink") : t("servers.link");
    link.title = server.linked ? t("servers.unlink_hint") : t("servers.link_hint");
    link.addEventListener("click", async () => {
      try {
        if (server.linked) {
          await invoke("unlink_server", { url: server.url });
          state = await invoke("get_state");
          renderServers();
        } else {
          await invoke("open_server", { url: server.url, link: true, force: false });
        }
      } catch (err) {
        showStatus("remote", splitError(err).message, { error: true });
      }
    });
    const open = document.createElement("button");
    open.type = "button";
    open.className = "btn small";
    open.textContent = t("servers.open");
    open.addEventListener("click", () => { $("remote-url").value = server.url; connectRemote(); });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove";
    remove.textContent = "×";
    remove.title = t("remote.forget");
    remove.setAttribute("aria-label", `${t("remote.forget")}: ${server.url}`);
    remove.addEventListener("click", async () => {
      await invoke("forget_remote", { url: server.url });
      state.servers = state.servers.filter((s) => s.url !== server.url);
      renderServers();
    });
    li.append(label, link, open, remove);
    list.append(li);
  }
  if (!list.children.length) {
    const li = document.createElement("li");
    const span = document.createElement("span");
    span.className = "path";
    span.textContent = t("servers.none");
    li.append(span);
    list.append(li);
  }
}

function renderRoots() {
  const list = $("roots");
  list.replaceChildren();
  for (const root of state.settings.localOpenRoots || []) {
    const li = document.createElement("li");
    const span = document.createElement("span");
    span.className = "path";
    span.textContent = root;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove";
    remove.textContent = "×";
    remove.setAttribute("aria-label", `${t("settings.remove")}: ${root}`);
    remove.addEventListener("click", () => saveRoots(state.settings.localOpenRoots.filter((r) => r !== root)));
    li.append(span, remove);
    list.append(li);
  }
  if (!list.children.length) {
    const li = document.createElement("li");
    const span = document.createElement("span");
    span.className = "path";
    span.textContent = t("settings.no_folders");
    li.append(span);
    list.append(li);
  }
}

async function saveRoots(roots) {
  state.settings = await invoke("save_settings", { patch: { localOpenRoots: roots } });
  renderRoots();
}

async function applyLocal(event) {
  event?.preventDefault();
  const root = $("local-root").value.trim();
  const port = Number($("local-port").value) || 0;
  if (port && (port < 1024 || port > 65535)) {
    showStatus("local", t("local.port_invalid"), { error: true });
    return;
  }
  $("start-local").disabled = true;
  showStatus("local", t("local.starting"), { busy: true });
  try {
    const res = await invoke("connect_local", { root, port });
    // The main window now shows the local Home; this window hides.
    state.local = { port: res.port };
    renderLocalState();
    showStatus("local", res.fallback ? t("local.fallback", { port: res.port, requested: res.requestedPort }) : t("local.applied", { port: res.port }));
  } catch (err) {
    const { message, log } = splitError(err);
    showStatus("local", message, { error: true, log, showLog: true });
  } finally {
    $("start-local").disabled = !state.serverFound;
  }
}

async function connectRemote(event, { force = false } = {}) {
  event?.preventDefault();
  const raw = $("remote-url").value.trim();
  if (!raw) {
    showStatus("remote", t("remote.empty"), { error: true });
    return;
  }
  $("connect-remote").disabled = true;
  showStatus("remote", t("remote.checking"), { busy: true });
  try {
    const res = await invoke("connect_remote", { url: raw, force });
    if (res.opened) {
      hideStatus("remote");
      state = await invoke("get_state");
      renderServers();
    } else {
      showStatus("remote", unreachableMessage(res, raw, t), { error: true, openAnyway: true });
    }
  } catch (err) {
    showStatus("remote", splitError(err).message || t("remote.invalid"), { error: true });
  } finally {
    $("connect-remote").disabled = false;
  }
}

// About: the version, and with the updater built in the update controls.
// Installer builds update themselves; the portable build downloads.
function renderAbout(message = "", { busy = false, error = false } = {}) {
  const u = state.update || {};
  const on = !!u.enabled;
  const available = on ? u.available : null;
  $("about-version").textContent = t("about.version", { version: state.version });
  $("check-update").classList.toggle("hidden", !on);
  $("auto-check-row").classList.toggle("hidden", !on);
  $("auto-check").checked = !!u.autoCheck;
  $("install-update").classList.toggle("hidden", !(available && u.selfInstall));
  $("download-update").classList.toggle("hidden", !(available && !u.selfInstall));
  for (const id of ["check-update", "install-update", "download-update"]) $(id).disabled = busy;
  let text = message;
  if (!text && available) text = t(u.selfInstall ? "about.available" : "about.available_portable", { version: available });
  const p = $("update-text");
  p.textContent = text;
  p.classList.toggle("hidden", !text);
  p.classList.toggle("is-error", error);
}

async function checkUpdate() {
  renderAbout(t("about.checking"), { busy: true });
  try {
    state.update = await invoke("check_update", { install: false });
    renderAbout(state.update.available ? "" : t("about.up_to_date"));
  } catch (err) {
    renderAbout(t("about.check_failed", { error: splitError(err).message }), { error: true });
  }
}

async function installUpdate() {
  renderAbout(t("about.installing"), { busy: true });
  try {
    // On success ARK Studio restarts into the new version.
    state.update = await invoke("check_update", { install: true });
    renderAbout(state.update.available ? "" : t("about.up_to_date"));
  } catch (err) {
    renderAbout(t("about.install_failed", { error: splitError(err).message }), { error: true });
  }
}

async function init() {
  applyI18n();
  state = await invoke("get_state");
  const s = state.settings;
  $("version").textContent = `ARK Studio ${state.version}`;
  $("local-root").value = s.localRoot || state.defaultRoot;
  $("local-root").placeholder = state.defaultRoot;
  $("local-port").value = s.localPort || state.defaultPort;
  $("close-to-tray").checked = !!s.closeToTray;
  $("git-missing").classList.toggle("hidden", state.gitFound);
  $("server-missing").classList.toggle("hidden", state.serverFound);
  $("start-local").disabled = !state.serverFound;
  $("data-dir").textContent = state.dataDir;
  $("projects-dir").textContent = state.defaultRoot;
  const kind = state.layout?.kind;
  $("layout-kind").textContent = kind === "install" ? t("settings.layout_install") : kind === "user" ? t("settings.layout_user") : "";
  if (state.layout?.note) {
    $("layout-note").textContent = state.layout.note;
    $("layout-note").classList.remove("hidden");
  }
  renderAbout();
  renderLocalState();
  renderServers();
  renderRoots();

  const params = new URLSearchParams(location.search);
  if (params.get("notice")) {
    $("notice").textContent = params.get("notice");
    $("notice").classList.remove("hidden");
  }

  $("panel-local").addEventListener("submit", applyLocal);
  $("panel-remote").addEventListener("submit", connectRemote);
  $("open-anyway").addEventListener("click", () => connectRemote(null, { force: true }));
  $("browse").addEventListener("click", async () => {
    const picked = await invoke("pick_folder", { current: $("local-root").value.trim() || null });
    if (picked) $("local-root").value = picked;
  });
  $("use-default").addEventListener("click", () => { $("local-root").value = state.defaultRoot; });
  $("close-to-tray").addEventListener("change", (e) => invoke("save_settings", { patch: { closeToTray: e.target.checked } }));
  for (const id of ["show-log", "open-log"]) {
    $(id).addEventListener("click", () => invoke("open_log").catch((err) => showStatus("local", splitError(err).message, { error: true })));
  }
  $("open-data").addEventListener("click", () => invoke("open_data_dir"));
  $("go-home").addEventListener("click", () => invoke("go_local_home"));
  $("add-root").addEventListener("click", async () => {
    const picked = await invoke("pick_folder", { current: null });
    if (picked && !(state.settings.localOpenRoots || []).includes(picked)) {
      await saveRoots([...(state.settings.localOpenRoots || []), picked]);
    }
  });
  $("open-web-settings").addEventListener("click", () => invoke("open_local_settings").catch((err) => showStatus("local", splitError(err).message, { error: true })));
  $("check-update").addEventListener("click", checkUpdate);
  $("install-update").addEventListener("click", installUpdate);
  $("download-update").addEventListener("click", () => invoke("open_releases", { version: state.update?.available || null }));
  $("open-releases").addEventListener("click", () => invoke("open_releases", { version: null }));
  $("auto-check").addEventListener("change", async (e) => {
    state.settings = await invoke("save_settings", { patch: { autoUpdateCheck: e.target.checked } });
    state.update.autoCheck = !!state.settings.autoUpdateCheck;
  });

  const view = params.get("view");
  if (view === "connect" || view === "servers") {
    $("sec-servers").scrollIntoView({ block: "start" });
    $("remote-url").focus({ preventScroll: true });
  } else if (view === "local") {
    $("sec-local").scrollIntoView({ block: "start" });
    $("local-root").focus({ preventScroll: true });
  } else if (view === "about") {
    $("sec-about").scrollIntoView({ block: "start" });
  } else if (view === "window") {
    $("sec-window").scrollIntoView({ block: "start" });
  } else if (view === "data") {
    $("sec-data").scrollIntoView({ block: "start" });
  }
}

init().catch((err) => showStatus("local", String(err), { error: true }));
