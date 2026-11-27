// Settings › Proxy (no DOM): the three modes, checking a custom proxy and
// the direct-connection list as typed, and whether a saved setting still
// waits for a restart. The app checks the same again (src-tauri/src/proxy.rs).

export const MODES = ["system", "direct", "custom"];
export const LOOPBACK = ["localhost", "127.0.0.1", "::1"];
const SCHEMES = ["http", "https", "socks5"];
const DEFAULT_PORTS = { http: 80, https: 443 };
const BYPASS_MAX = 64;

/** The stored setting with defaults filled in. */
export function proxyConfig(value) {
  const v = value && typeof value === "object" ? value : {};
  return {
    mode: MODES.includes(v.mode) ? v.mode : "system",
    url: typeof v.url === "string" ? v.url : "",
    bypass: Array.isArray(v.bypass) ? v.bypass.filter((e) => typeof e === "string") : [],
  };
}

/**
 * A typed proxy address as `scheme://host:port` (http:// when no scheme is
 * given): {url} or {error: i18n key}.
 */
export function normalizeProxyUrl(raw) {
  const text = String(raw ?? "").trim();
  if (!text) return { error: "proxy.err.empty" };
  const withScheme = text.includes("://") ? text : `http://${text}`;
  const scheme = withScheme.slice(0, withScheme.indexOf("://")).toLowerCase();
  if (!SCHEMES.includes(scheme)) return { error: "proxy.err.scheme" };
  let rest = withScheme.slice(withScheme.indexOf("://") + 3);
  if (rest.endsWith("/")) rest = rest.slice(0, -1);
  if (rest.includes("@")) return { error: "proxy.err.credentials" };
  if (/[/?#]/.test(rest)) return { error: "proxy.err.path" };
  const m = /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9._-]+)(?::(\d{1,5}))?$/.exec(rest);
  if (!m) return { error: "proxy.err.invalid" };
  const host = m[1].toLowerCase();
  let port = m[2] === undefined ? DEFAULT_PORTS[scheme] : Number(m[2]);
  if (port === undefined) return { error: "proxy.err.port" };
  if (!(port >= 0 && port <= 65535)) return { error: "proxy.err.invalid" };
  return { url: `${scheme}://${host}:${port}` };
}

/**
 * The direct-connection list as typed (commas, semicolons, spaces or new
 * lines between entries): {list} or {error, entry}. `*.example.com` is
 * stored as `.example.com`.
 */
export function parseBypass(raw) {
  const list = [];
  for (const part of String(raw ?? "").split(/[,;\s]+/)) {
    const item = part.trim().toLowerCase();
    if (!item) continue;
    const entry = item.startsWith("*.") ? `.${item.slice(2)}` : item;
    const body = entry.startsWith(".") ? entry.slice(1) : entry;
    if (!body || body.length > 253 || body.startsWith(".") || !/^[a-z0-9\-._:/[\]]+$/.test(body)) {
      return { error: "proxy.err.bypass", entry: item };
    }
    if (!list.includes(entry)) list.push(entry);
  }
  if (list.length > BYPASS_MAX) return { error: "proxy.err.bypass_many", max: BYPASS_MAX };
  return { list };
}

/** The bypass list as shown in the input. */
export function bypassText(list) {
  return (Array.isArray(list) ? list : []).join(", ");
}

/**
 * The patch for save_settings from the form: {patch} or {error, field}.
 * Only custom mode requires a valid address; the other modes keep what is
 * valid for later.
 */
export function proxyPatch({ mode, url, bypass }) {
  const m = MODES.includes(mode) ? mode : "system";
  const u = normalizeProxyUrl(url);
  const b = parseBypass(bypass);
  if (m === "custom") {
    if (u.error) return { error: u.error, field: "url" };
    if (b.error) return { error: b.error, field: "bypass", entry: b.entry, max: b.max };
  }
  return { patch: { proxy: { mode: m, url: u.url || "", bypass: b.list || [] } } };
}

/** Whether two settings route connections the same way. */
export function sameRoute(a, b) {
  const x = proxyConfig(a);
  const y = proxyConfig(b);
  if (x.mode !== y.mode) return false;
  if (x.mode !== "custom") return true;
  return x.url === y.url && x.bypass.join(",") === y.bypass.join(",");
}

/** A saved setting that differs from the one this run started with waits for a restart. */
export function restartNeeded(saved, applied) {
  if (!applied) return false;
  return !sameRoute(saved, applied);
}

/**
 * The platform note under the modes (i18n key), or "" when there is none:
 * WKWebView keeps the system proxy settings.
 */
export function platformNote(platform) {
  return platform === "macos" ? "proxy.note.macos" : "";
}
