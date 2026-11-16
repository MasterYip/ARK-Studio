// ARK Studio desktop: connection chooser and settings (bundled page).
import { t, applyI18n } from "./i18n.js";

const invoke = (cmd, args = {}) => window.__TAURI_INTERNALS__.invoke(cmd, args);
const $ = (id) => document.getElementById(id);

let state = null;
let mode = "local";

function setMode(next) {
  mode = next === "remote" ? "remote" : "local";
  for (const tab of document.querySelectorAll(".tabs button")) {
    const on = tab.dataset.mode === mode;
    tab.setAttribute("aria-selected", String(on));
    tab.tabIndex = on ? 0 : -1;
  }
  $("panel-local").classList.toggle("hidden", mode !== "local");
  $("panel-remote").classList.toggle("hidden", mode !== "remote");
  hideStatus();
}

function showStatus(text, { busy = false, error = false, log = "", openAnyway = false, showLog = false } = {}) {
  const box = $("status");
  box.classList.remove("hidden");
  box.classList.toggle("is-busy", busy);
  box.classList.toggle("is-error", error);
  $("status-text").textContent = text;
  $("status-log").textContent = log;
  $("status-log").classList.toggle("hidden", !log);
  $("open-anyway").classList.toggle("hidden", !openAnyway);
  $("show-log").classList.toggle("hidden", !showLog);
}

function hideStatus() {
  $("status").classList.add("hidden");
}

function setBusy(busy) {
  for (const id of ["start-local", "connect-remote", "browse"]) $(id).disabled = busy;
}

function splitError(err) {
  const text = String(err?.message || err || "");
  const at = text.indexOf("\n\n");
  return at < 0 ? { message: text, log: "" } : { message: text.slice(0, at), log: text.slice(at + 2) };
}

function renderRecent() {
  const list = $("recent");
  const data = $("remote-history");
  list.replaceChildren();
  data.replaceChildren();
  for (const url of state.settings.remoteHistory || []) {
    const opt = document.createElement("option");
    opt.value = url;
    data.append(opt);
    const li = document.createElement("li");
    const pick = document.createElement("button");
    pick.type = "button";
    pick.className = "pick";
    pick.textContent = url;
    pick.title = url;
    pick.addEventListener("click", () => { $("remote-url").value = url; connectRemote(); });
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "remove";
    remove.textContent = "×";
    remove.title = t("remote.forget");
    remove.setAttribute("aria-label", `${t("remote.forget")}: ${url}`);
    remove.addEventListener("click", async () => {
      await invoke("forget_remote", { url });
      state.settings.remoteHistory = state.settings.remoteHistory.filter((u) => u !== url);
      renderRecent();
    });
    li.append(pick, remove);
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
  const saved = await invoke("save_settings", { patch: { localOpenRoots: roots } });
  state.settings = saved;
  renderRoots();
}

async function startLocal(event) {
  event?.preventDefault();
  const root = $("local-root").value.trim();
  const port = Number($("local-port").value) || 0;
  if (port && (port < 1024 || port > 65535)) {
    showStatus(t("local.port_invalid"), { error: true });
    return;
  }
  setBusy(true);
  showStatus(t("local.starting"), { busy: true });
  try {
    await invoke("connect_local", { root, port, autoConnect: $("auto-connect").checked });
    // The window now navigates to the local server.
  } catch (err) {
    const { message, log } = splitError(err);
    showStatus(message, { error: true, log, showLog: true });
    setBusy(false);
  }
}

async function reachable(url) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 8000);
  try {
    await fetch(new URL("api/health", url), { mode: "no-cors", cache: "no-store", signal: ctrl.signal });
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

async function connectRemote(event, { force = false } = {}) {
  event?.preventDefault();
  const raw = $("remote-url").value.trim();
  if (!raw) {
    showStatus(t("remote.empty"), { error: true });
    return;
  }
  let url = raw.includes("://") ? raw : `https://${raw}`;
  try {
    url = new URL(url).href;
  } catch {
    showStatus(t("remote.invalid"), { error: true });
    return;
  }
  setBusy(true);
  if (!force) {
    showStatus(t("remote.checking"), { busy: true });
    if (!(await reachable(url))) {
      showStatus(t("remote.unreachable", { url }), { error: true, openAnyway: true });
      setBusy(false);
      return;
    }
  }
  try {
    await invoke("connect_remote", { url: raw, autoConnect: $("auto-connect").checked });
  } catch (err) {
    showStatus(splitError(err).message, { error: true });
    setBusy(false);
  }
}

function toggleSettings(show) {
  const on = show ?? $("view-settings").classList.contains("hidden");
  $("view-settings").classList.toggle("hidden", !on);
  $("view-connect").classList.toggle("hidden", on);
  $("toggle-settings").textContent = on ? t("settings.back") : t("settings.title");
}

async function checkUpdate(install = false) {
  $("update-text").textContent = install ? t("settings.installing") : t("settings.checking");
  try {
    const res = await invoke("check_update", { install });
    if (!res.enabled) $("update-text").textContent = t("settings.updates_off");
    else if (res.available) {
      $("update-text").textContent = t("settings.update_available", { version: res.version });
      $("install-update").classList.remove("hidden");
    } else $("update-text").textContent = t("settings.up_to_date");
  } catch (err) {
    $("update-text").textContent = t("settings.update_failed", { error: splitError(err).message });
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
  $("remote-url").value = s.remoteUrl || "";
  $("auto-connect").checked = !!s.autoConnect;
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
  $("update-text").textContent = state.updater ? t("settings.updates_on") : t("settings.updates_off");
  $("check-update").classList.toggle("hidden", !state.updater);
  renderRecent();
  renderRoots();
  setMode(s.mode === "remote" ? "remote" : "local");

  const params = new URLSearchParams(location.search);
  if (params.get("notice")) {
    $("notice").textContent = params.get("notice");
    $("notice").classList.remove("hidden");
  }
  if (params.get("view") === "settings") toggleSettings(true);
  if (state.local) $("start-local").textContent = t("local.open_running", { port: state.local.port });

  for (const tab of document.querySelectorAll(".tabs button")) {
    tab.addEventListener("click", () => setMode(tab.dataset.mode));
    tab.addEventListener("keydown", (e) => {
      if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        setMode(mode === "local" ? "remote" : "local");
        document.querySelector(`.tabs button[data-mode="${mode}"]`).focus();
      }
    });
  }
  $("panel-local").addEventListener("submit", startLocal);
  $("panel-remote").addEventListener("submit", connectRemote);
  $("open-anyway").addEventListener("click", () => connectRemote(null, { force: true }));
  $("browse").addEventListener("click", async () => {
    const picked = await invoke("pick_folder", { current: $("local-root").value.trim() || null });
    if (picked) $("local-root").value = picked;
  });
  $("use-default").addEventListener("click", () => { $("local-root").value = state.defaultRoot; });
  $("auto-connect").addEventListener("change", (e) => invoke("save_settings", { patch: { autoConnect: e.target.checked } }));
  $("close-to-tray").addEventListener("change", (e) => invoke("save_settings", { patch: { closeToTray: e.target.checked } }));
  for (const id of ["show-log", "open-log"]) {
    $(id).addEventListener("click", () => invoke("open_log").catch((err) => showStatus(splitError(err).message, { error: true })));
  }
  $("open-data").addEventListener("click", () => invoke("open_data_dir"));
  $("toggle-settings").addEventListener("click", () => toggleSettings());
  $("add-root").addEventListener("click", async () => {
    const picked = await invoke("pick_folder", { current: null });
    if (picked && !(state.settings.localOpenRoots || []).includes(picked)) {
      await saveRoots([...(state.settings.localOpenRoots || []), picked]);
    }
  });
  $("check-update").addEventListener("click", () => checkUpdate(false));
  $("install-update").addEventListener("click", () => checkUpdate(true));
  (mode === "local" ? $("start-local") : $("remote-url")).focus();
}

init().catch((err) => showStatus(String(err), { error: true }));
