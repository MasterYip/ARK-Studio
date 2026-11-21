// The main window's status page: the local server is starting, failed or
// was stopped ("local"), or a saved server did not answer ("remote").
import { t, applyI18n } from "./i18n.js";
import { unreachableMessage } from "./connect-model.js";

const invoke = (cmd, args = {}) => window.__TAURI_INTERNALS__.invoke(cmd, args);
const $ = (id) => document.getElementById(id);
const params = new URLSearchParams(location.search);
const kind = ["starting", "remote"].includes(params.get("kind")) ? params.get("kind") : "local";
const target = params.get("url") || "";

function show(text) {
  const at = text.indexOf("\n\n");
  $("message").textContent = at < 0 ? text : text.slice(0, at);
  const log = at < 0 ? "" : text.slice(at + 2);
  $("log").textContent = log;
  $("log").classList.toggle("hidden", !log);
}

applyI18n();
$("title").textContent = t(`offline.${kind}_title`);
if (kind === "starting") {
  const spin = document.createElement("span");
  spin.className = "spinner";
  spin.setAttribute("aria-hidden", "true");
  $("title").prepend(spin);
  show(t("local.starting"));
  for (const id of ["retry", "show-log", "home", "open-anyway"]) $(id).classList.add("hidden");
} else {
  show(params.get("message") || target);
}
$("show-log").classList.toggle("hidden", kind !== "local");
$("home").classList.toggle("hidden", kind !== "remote");
$("open-anyway").classList.toggle("hidden", kind !== "remote");
$("show-log").addEventListener("click", () => invoke("open_log").catch(() => {}));
$("settings").addEventListener("click", () => invoke("open_settings", { view: kind === "remote" ? "connect" : "local" }));
$("home").addEventListener("click", () => invoke("go_local_home"));

async function openRemote(force) {
  const res = await invoke("connect_remote", { url: target, force });
  if (!res.opened) show(unreachableMessage(res, target, t));
}

$("open-anyway").addEventListener("click", () => openRemote(true).catch((err) => show(String(err?.message || err))));
$("retry").addEventListener("click", async () => {
  $("retry").disabled = true;
  try {
    if (kind === "remote") {
      await openRemote(false);
    } else {
      show(t("local.starting"));
      const state = await invoke("get_state");
      await invoke("connect_local", { root: state.settings.localRoot || "", port: state.settings.localPort || 0 });
    }
  } catch (err) {
    show(String(err?.message || err));
  } finally {
    $("retry").disabled = false;
  }
});
