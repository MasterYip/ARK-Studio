// Every download a page starts goes through saveDownload() (tests/js/
// download_link.test.mjs checks the callers).
//
// * Browsers: the browser's own download (<a download>), unchanged.
// * ARK Studio 2.1.7 and newer (__ARK_DESKTOP__.saveDownload): the app asks
//   the server for the file with the page's cookies, shows the system
//   "Save as" dialog and streams the file to the chosen path
//   (desktop/src-tauri/src/downloads.rs); the page shows the progress and
//   "Saved to ..." with "Show in folder".
// * Older ARK Studio (2.1.6 and before): WebView2 there saved <a download>
//   silently or not at all, so the page fetches the file itself (same
//   origin, the page's cookies) into the file picked in the system dialog
//   (showSaveFilePicker), or, without that API, into a blob link.
//
// installDownloadLinks() sends clicks on every <a download> through
// saveDownload in the app, so plain links need nothing from here.

const PROGRESS_EVENT = "ark-download-progress";
const ROUTES = new Set(["native", "picker", "blob"]);

// English until app.js hands over t() (setDownloadText); the app uses i18n-*.js.
const EN = {
  "download.preparing": "Preparing {name}…", // i18n-ignore fallback before setDownloadText
  "download.progress": "Downloading {name} · {done}", // i18n-ignore fallback before setDownloadText
  "download.progress_total": "Downloading {name} · {done} of {total}", // i18n-ignore fallback before setDownloadText
  "download.saved": "Saved to {path}", // i18n-ignore fallback before setDownloadText
  "download.saved_downloads": "Saved to Downloads: {name}", // i18n-ignore fallback before setDownloadText
  "download.show_folder": "Show in folder", // i18n-ignore fallback before setDownloadText
  "download.failed": "Download failed: {name} ({error})", // i18n-ignore fallback before setDownloadText
  "download.cancel": "Cancel", // i18n-ignore fallback before setDownloadText
  "download.cancelled": "Download cancelled: {name}", // i18n-ignore fallback before setDownloadText
  "action.close": "Close", // i18n-ignore fallback before setDownloadText
};
let translate = (key, vars = {}) => (EN[key] || key).replace(/\{(\w+)\}/g, (_, k) => String(vars[k] ?? ""));

/** Uses the app's translations (app.js) instead of the English fallback. */
export function setDownloadText(fn) {
  if (typeof fn === "function") translate = fn;
}

/** The ARK Studio desktop shell of this page, or null in a browser. */
export function desktopShell(win = globalThis.window) {
  const desktop = win?.__ARK_DESKTOP__;
  return desktop && typeof desktop === "object" && desktop.shell === "tauri" ? desktop : null;
}

/** "browser" | "native" (app saves) | "picker" (page saves via the dialog) | "blob". */
export function downloadRoute(win = globalThis.window) {
  const desktop = desktopShell(win);
  if (!desktop) return "browser";
  if (typeof desktop.saveDownload === "function") return "native";
  if (typeof win.showSaveFilePicker === "function") return "picker";
  return "blob";
}

/** A file name for a URL: the `path` query's last part, else the URL's (when it has an extension). */
export function nameFromUrl(url) {
  let parsed;
  try { parsed = new URL(String(url || ""), "http://x/"); } catch { return ""; }
  if (parsed.protocol === "blob:" || parsed.protocol === "data:") return "";
  const fromPath = (parsed.searchParams.get("path") || "").split(/[\\/]/).pop();
  if (fromPath) return fromPath;
  const last = decodeURIComponent(parsed.pathname.split("/").pop() || "");
  return /\.[A-Za-z0-9]{1,10}$/.test(last) ? last : "";
}

/** File name from a Content-Disposition header (filename* wins). */
export function dispositionName(header) {
  const text = String(header || "");
  const star = /filename\*\s*=\s*(?:UTF-8|utf-8)''([^;]+)/.exec(text);
  if (star) {
    try { return decodeURIComponent(star[1].trim()); } catch { /* fall through */ }
  }
  const plain = /filename\s*=\s*"([^"]*)"|filename\s*=\s*([^;]+)/.exec(text);
  return plain ? (plain[1] ?? plain[2] ?? "").trim() : "";
}

export function formatSize(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

/** The server's error text for a failed response. */
async function responseError(response) {
  let text = "";
  try { text = await response.text(); } catch { /* no body */ }
  try {
    const data = JSON.parse(text);
    const msg = data?.error?.message || data?.error || data?.detail || data?.message;
    if (msg && typeof msg === "string") return msg;
  } catch { /* not JSON */ }
  const line = text.replace(/<[^>]*>/g, " ").trim().split("\n")[0]?.trim();
  return line && line.length < 200 ? line : `HTTP ${response.status}`;
}

// ------------------------------------------------------------------ card
// A toast with progress, then the result; lives in #toast-root.
function downloadCard(doc, onCancel) {
  const root = doc.getElementById?.("toast-root") || doc.body;
  const el = doc.createElement("div");
  el.className = "toast toast-download";
  el.setAttribute("data-download-card", "");
  const text = doc.createElement("span");
  const bar = doc.createElement("progress");
  bar.className = "toast-download-bar";
  el.append(text, bar);
  let cancel = null;
  if (onCancel) {
    cancel = doc.createElement("button");
    cancel.type = "button";
    cancel.className = "toast-action";
    cancel.textContent = translate("download.cancel");
    cancel.addEventListener("click", () => onCancel());
    el.append(cancel);
  }
  root.append(el);
  let timer = null;
  const end = (kind, message, action, ms) => {
    bar.remove();
    cancel?.remove();
    el.className = `toast toast-download ${kind}`.trim();
    el.setAttribute("data-download-state", kind || "done");
    text.textContent = message;
    if (action) {
      const btn = doc.createElement("button");
      btn.type = "button";
      btn.className = "toast-action";
      btn.textContent = action.label;
      btn.addEventListener("click", () => { action.onClick(); });
      el.append(btn);
    }
    const close = doc.createElement("button");
    close.type = "button";
    close.className = "toast-action";
    close.textContent = "×";
    close.setAttribute("aria-label", translate("action.close"));
    close.addEventListener("click", () => el.remove());
    el.append(close);
    clearTimeout(timer);
    timer = setTimeout(() => el.remove(), ms);
  };
  return {
    el,
    say(message) { text.textContent = message; },
    progress(name, received, total) {
      if (total > 0) { bar.max = total; bar.value = Math.min(received, total); } else { bar.removeAttribute("value"); }
      text.textContent = translate(total > 0 ? "download.progress_total" : "download.progress",
        { name, done: formatSize(received), total: formatSize(total) });
    },
    saved(message, action) { end("ok", message, action, 12000); },
    failed(message) { end("err", message, null, 15000); },
    cancelled(message) { end("", message, null, 4000); },
  };
}

// ---------------------------------------------------------------- routes

function browserDownload(source, name, doc) {
  const blobUrl = typeof source === "string" ? "" : URL.createObjectURL(source);
  const link = doc.createElement("a");
  link.href = blobUrl || source;
  link.download = name || "";
  link.rel = "noopener";
  doc.body.append(link);
  link.click();
  link.remove();
  if (blobUrl) setTimeout(() => URL.revokeObjectURL(blobUrl), 1500);
}

let tickets = 0;

async function nativeSave(desktop, source, name, card, win) {
  const ticket = `dl${Date.now().toString(36)}${(tickets += 1)}`;
  const onProgress = (event) => {
    const d = event.detail || {};
    if (d.ticket !== ticket) return;
    if (d.state === "progress") card.progress(d.name || name, d.received || 0, d.total || 0);
  };
  win.addEventListener?.(PROGRESS_EVENT, onProgress);
  card.cancelTicket = ticket;
  try {
    if (typeof source === "string") return await desktop.saveDownload(source, { name, ticket });
    const bytes = new Uint8Array(await source.arrayBuffer());
    return await desktop.saveBytes(bytes, name);
  } finally {
    win.removeEventListener?.(PROGRESS_EVENT, onProgress);
  }
}

async function fetchTo(source, name, card, fetchImpl, writer, signal) {
  if (typeof source !== "string") {
    await writer.write(source);
    return { name, bytes: source.size };
  }
  const response = await fetchImpl(source, { credentials: "same-origin", signal });
  if (!response.ok) throw new Error(await responseError(response));
  const served = dispositionName(response.headers.get("Content-Disposition")) || name;
  const total = Number(response.headers.get("Content-Length")) || 0;
  let received = 0;
  card.progress(served, 0, total);
  const reader = response.body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    await writer.write(value);
    received += value.byteLength;
    card.progress(served, received, total);
  }
  return { name: served, bytes: received };
}

/**
 * Saves a URL (same origin) or a Blob under `filename` (a suggestion; the
 * server's name wins where the app reads it). Resolves to
 * {state: "browser" | "saved" | "cancelled" | "failed", name, path?, error?}.
 * Call it straight from the click: the system dialog of older apps needs
 * the click's user activation.
 */
export function saveDownload(source, options = {}) {
  const win = options.win || globalThis.window;
  const doc = options.doc || globalThis.document;
  const fetchImpl = options.fetch || globalThis.fetch?.bind(globalThis);
  const route = options.route || downloadRoute(win);
  const name = String(options.filename || "").trim() || (typeof source === "string" ? nameFromUrl(source) : "") || "download";
  if (!ROUTES.has(route)) {
    browserDownload(source, name, doc);
    return Promise.resolve({ state: "browser", name });
  }
  const desktop = desktopShell(win);
  const aborter = typeof AbortController === "function" ? new AbortController() : null;
  let card = null;
  const onCancel = () => {
    if (route === "native" && card?.cancelTicket) desktop.cancelDownload?.(card.cancelTicket);
    else aborter?.abort();
  };
  card = downloadCard(doc, typeof source === "string" ? onCancel : null);
  card.say(translate("download.preparing", { name }));
  // The picker must open before the first await (user activation).
  let picked = null;
  if (route === "picker") {
    try {
      picked = win.showSaveFilePicker({ suggestedName: name });
    } catch (error) {
      picked = Promise.reject(error);
    }
  }
  const run = async () => {
    if (route === "native") {
      const result = await nativeSave(desktop, source, name, card, win);
      if (!result || result.state === "cancelled") { card.el.remove(); return { state: "cancelled", name }; }
      if (result.state === "aborted") {
        card.cancelled(translate("download.cancelled", { name: result.name || name }));
        return { state: "cancelled", name };
      }
      const reveal = result.id && desktop.revealDownload
        ? { label: translate("download.show_folder"), onClick: () => desktop.revealDownload(result.id).catch(() => {}) }
        : null;
      card.saved(translate("download.saved", { path: result.path || result.name || name, name: result.name || name }), reveal);
      return { state: "saved", name: result.name || name, path: result.path || "" };
    }
    let handle = null;
    if (route === "picker") {
      try {
        handle = await picked;
      } catch (error) {
        if (error?.name === "AbortError") { card.el.remove(); return { state: "cancelled", name }; }
        handle = null; // no user activation left, or the API is blocked: use a blob link
      }
    }
    if (handle) {
      const writable = await handle.createWritable();
      try {
        const got = await fetchTo(source, name, card, fetchImpl, writable, aborter?.signal);
        await writable.close();
        card.saved(translate("download.saved", { path: handle.name || got.name, name: handle.name || got.name }), null);
        return { state: "saved", name: handle.name || got.name };
      } catch (error) {
        try { await writable.abort(); } catch { /* already closed */ }
        throw error;
      }
    }
    // Blob link: the whole file in memory, then the WebView's own save
    // (the Downloads folder in ARK Studio 2.1.x).
    const parts = [];
    const sink = { write: async (chunk) => { parts.push(chunk); } };
    const got = await fetchTo(source, name, card, fetchImpl, sink, aborter?.signal);
    browserDownload(new Blob(parts), got.name, doc);
    card.saved(translate("download.saved_downloads", { name: got.name }), null);
    return { state: "saved", name: got.name };
  };
  return run().catch((error) => {
    if (error?.name === "AbortError") {
      card.cancelled(translate("download.cancelled", { name }));
      return { state: "cancelled", name };
    }
    const message = String(error?.message || error || "error");
    card.failed(translate("download.failed", { name, error: message }));
    return { state: "failed", name, error: message };
  });
}

/** The name the server gives an /api/export file (routes_views.go exportNameRe). */
export function exportFileName(projectName, format = "project") {
  const base = String(projectName || "").replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "") || "ark-project";
  const ext = { project: ".ark.zip", json: ".json", markdown: ".md", csv: ".csv", svg: "-map.svg" }[format] || "";
  return base + ext;
}

/** Kept for older callers: same as saveDownload. */
export function openDownload(url, { filename = "", win, doc } = {}) {
  return saveDownload(url, { filename, win, doc });
}

/**
 * In the app, clicks on <a download> (any page, any view) go through
 * saveDownload instead of the WebView; in browsers nothing changes.
 */
export function installDownloadLinks({ win = globalThis.window, doc = globalThis.document } = {}) {
  if (downloadRoute(win) === "browser" || doc.__arkDownloadLinks) return false;
  doc.__arkDownloadLinks = true;
  doc.addEventListener("click", (event) => {
    if (event.defaultPrevented || event.button > 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    const link = event.target?.closest?.("a[download]");
    if (!link || !link.href) return;
    event.preventDefault();
    const attr = link.getAttribute("download") || "";
    saveDownload(link.href, { filename: attr === "true" ? "" : attr, win, doc });
  }, true);
  return true;
}
