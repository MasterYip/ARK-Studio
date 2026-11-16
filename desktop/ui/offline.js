// Shown when the local server fails or stops, or a server cannot be reached.
import { t, applyI18n } from "./i18n.js";

const invoke = (cmd, args = {}) => window.__TAURI_INTERNALS__.invoke(cmd, args);
const $ = (id) => document.getElementById(id);
const params = new URLSearchParams(location.search);
const kind = params.get("kind") === "remote" ? "remote" : "local";
const target = params.get("url") || "";

function show(text) {
  const at = text.indexOf("\n\n");
  $("message").textContent = at < 0 ? text : text.slice(0, at);
  const log = at < 0 ? "" : text.slice(at + 2);
  $("log").textContent = log;
  $("log").classList.toggle("hidden", !log);
}

applyI18n();
$("title").textContent = kind === "local" ? t("offline.local_title") : t("offline.remote_title");
show(params.get("message") || target);
$("show-log").classList.toggle("hidden", kind !== "local");
$("show-log").addEventListener("click", () => invoke("open_log").catch(() => {}));
$("switch").addEventListener("click", () => invoke("open_chooser"));
$("retry").addEventListener("click", async () => {
  $("retry").disabled = true;
  try {
    const state = await invoke("get_state");
    if (kind === "remote") {
      await invoke("connect_remote", { url: target, autoConnect: !!state.settings.autoConnect });
    } else {
      show(t("local.starting"));
      await invoke("connect_local", {
        root: state.settings.localRoot || "",
        port: state.settings.localPort || 0,
        autoConnect: !!state.settings.autoConnect,
      });
    }
  } catch (err) {
    show(String(err?.message || err));
    $("retry").disabled = false;
  }
});
