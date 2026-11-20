// The tray window's entries from get_state (ids are the native tray menu's,
// handled by on_tray_menu in src/lib.rs). Pure: tests/js/desktop_tray.test.mjs.

export const MAX_SERVERS = 6;

/**
 * @param {object} state get_state(): { version, local: {port}|null, servers: [{url, label}], update: {available} }
 * @param {(key: string, vars?: object) => string} t
 * @returns {{ head: {version: string, running: boolean, status: string}, items: object[] }}
 * items: { kind: "item", id, label, icon, tone? } | { kind: "sep" } | { kind: "group", label }
 */
export function trayModel(state, t) {
  const running = !!state?.local;
  const head = {
    version: String(state?.version || ""),
    running,
    status: running ? t("local.running", { port: state.local.port }) : t("local.stopped"),
  };
  const items = [];
  const item = (id, label, icon, tone) => items.push({ kind: "item", id, label, icon, ...(tone ? { tone } : {}) });
  const sep = () => items.push({ kind: "sep" });

  item("open", t("tray.open"), "window", "primary");
  const update = state?.update?.available;
  if (update) item("update", t("tray.update", { version: update }), "download", "accent");
  sep();
  items.push({ kind: "group", label: t("servers.title") });
  for (const s of (state?.servers || []).slice(0, MAX_SERVERS)) {
    if (s?.url) item(`server:${s.url}`, String(s.label || s.url), "server", "indent");
  }
  item("connect", t("remote.connect_title"), "plus", "indent");
  item("browser", t("tray.browser"), "external");
  sep();
  if (running) item("stop", t("tray.stop"), "stop");
  else item("start", t("tray.start"), "play");
  sep();
  item("settings", t("tray.settings"), "settings");
  item("data", t("settings.open_data"), "folder");
  item("log", t("log.show"), "log");
  sep();
  item("quit", t("tray.quit"), "power", "danger");
  return { head, items };
}
