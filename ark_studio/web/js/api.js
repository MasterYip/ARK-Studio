import {
  MOBILE_TOKEN_STORAGE_KEY, MOBILE_PERMISSION_STORAGE_KEY,
  MOBILE_EXPIRES_STORAGE_KEY,
} from "./mobile-core.js";
import { t, serverErrorText } from "./i18n.js";
import { clientCache } from "./client-cache.js";
import { clearStarterTokens } from "./starter-token-store.js";
import { createConnectivity, isProxyFailure } from "./connectivity.js";
import {
  cacheRoute, mergeBodiesDelta, mergeSnapshotDelta, servePolicy, staleAfterDelta,
} from "./client-cache-model.js";

// Relative app base so a path-prefixed tunnel (https://host/ark/) works without
// hard-coded localhost or root-absolute URLs.  The HTML entry point is the base.
const APP_BASE = new URL("./", document.baseURI);

export function getMobileToken() {
  try {
    return localStorage.getItem(MOBILE_TOKEN_STORAGE_KEY) || "";
  } catch {
    return "";
  }
}

export function setMobileToken(token, extra = {}) {
  try {
    localStorage.setItem(MOBILE_TOKEN_STORAGE_KEY, String(token || ""));
    if (extra.permission) localStorage.setItem(MOBILE_PERMISSION_STORAGE_KEY, extra.permission);
    if (extra.expires_at) localStorage.setItem(MOBILE_EXPIRES_STORAGE_KEY, extra.expires_at);
  } catch { /* storage may be disabled */ }
}

export function clearMobileToken() {
  try {
    localStorage.removeItem(MOBILE_TOKEN_STORAGE_KEY);
    localStorage.removeItem(MOBILE_PERMISSION_STORAGE_KEY);
    localStorage.removeItem(MOBILE_EXPIRES_STORAGE_KEY);
  } catch { /* storage may be disabled */ }
}

// The project this browser tab shows.  Every request carries it, so opening or
// creating a project in one tab never moves another tab, device or person.
// sessionStorage is per tab and survives a reload of that tab.
const TAB_PROJECT_KEY = "ark.tabProject";
let tabProject = "";
try { tabProject = sessionStorage.getItem(TAB_PROJECT_KEY) || ""; } catch { /* storage disabled */ }

export function tabProjectId() { return tabProject; }

export function setTabProject(id) {
  tabProject = String(id || "");
  clientCache.project = tabProject;
  try {
    if (tabProject) sessionStorage.setItem(TAB_PROJECT_KEY, tabProject);
    else sessionStorage.removeItem(TAB_PROJECT_KEY);
  } catch { /* storage disabled */ }
}

export function appUrl(path) {
  const clean = String(path || "").replace(/^\.?\//, "");
  return new URL(clean, APP_BASE).toString();
}

function authHeaders(headers = {}) {
  const token = getMobileToken();
  const out = token ? { ...headers, "X-ARK-Mobile-Token": token } : { ...headers };
  // A caller may name a project explicitly (GPU views); otherwise the tab's.
  if (tabProject && !token && !out["X-ARK-Project"]) out["X-ARK-Project"] = tabProject;
  return out;
}

// Append the mobile token for browser-owned resources (img/video/download) that
// cannot attach a header.  Document that query tokens should stay on a trusted
// tunnel because URLs can appear in proxy logs.
export function authenticatedUrl(value) {
  if (!value) return value;
  const text = String(value);
  if (/^(?:data:|blob:)/i.test(text)) return text;
  let url;
  if (/^https?:/i.test(text)) {
    try { url = new URL(text); } catch { return text; }
    if (url.origin !== location.origin) return text;
  } else {
    url = new URL(text.replace(/^\/+/, ""), APP_BASE);
  }
  const token = getMobileToken();
  if (token) url.searchParams.set("mobile_token", token);
  else if (tabProject && !url.searchParams.has("ark_project")) url.searchParams.set("ark_project", tabProject);
  return url.toString();
}

// ------------------------------------------------------------ request log
// Every API call is recorded (route, bytes, time, how it was served, the view
// that asked) for the network benchmark and for diagnosing slow links:
// window.__ARK_NET__ (bounded), summarized by window.ARKNet.summary().
const NET_LOG_LIMIT = 3000;
const netLog = [];
if (typeof window !== "undefined") {
  window.__ARK_NET__ = netLog;
  window.__ARK_PERF__ ||= {};
}

function logNet(entry) {
  netLog.push({ at: Math.round(performance.now()), view: currentViewName(), ...entry });
  if (netLog.length > NET_LOG_LIMIT) netLog.splice(0, netLog.length - NET_LOG_LIMIT);
}

function currentViewName() {
  try { return document.body?.dataset?.tab || ""; } catch { return ""; }
}

/** Mark a performance milestone once (first render from cache, fresh data). */
export function perfMark(name) {
  const perf = typeof window !== "undefined" ? window.__ARK_PERF__ : null;
  if (perf && perf[name] === undefined) perf[name] = Math.round(performance.now());
}

// ------------------------------------------------------------- sync state
// live: the revision stream is connected, so cached entries validated since
// liveSince are current unless a delta marked them stale.  ownWriteAt: this
// tab's last write.  syncRequestedAt: the last "Sync now" (entries checked
// before it are read from the server).  offline: the server is unreachable;
// cached data is shown read-only.
export const syncState = { live: false, liveSince: 0, ownWriteAt: 0, syncRequestedAt: 0, offline: false, syncing: 0, connectivity: "online" };
if (typeof window !== "undefined") window.__ARK_SYNC__ = syncState; // diagnostics and smoke tests

// Server reachability (connectivity.js).  Every response clears it; a
// failed request enters it and starts the health probe, so the offline state
// always ends once the server answers again, even if nothing else is sent.
async function probeHealth() {
  const controller = typeof AbortController === "function" ? new AbortController() : null;
  const timer = setTimeout(() => controller?.abort(), 8000);
  try {
    const response = await fetch(appUrl("/api/health"), {
      cache: "no-store", headers: authHeaders(), signal: controller?.signal,
    });
    let data = null;
    try { data = await response.json(); } catch { /* proxy page */ }
    return { status: response.status, data };
  } finally {
    clearTimeout(timer);
  }
}

export const connectivity = createConnectivity({
  probe: probeHealth,
  onChange: ({ state, offline }) => {
    syncState.offline = offline;
    syncState.connectivity = state;
    emit("ark:connectivity", { offline, state });
    // Back online: the push channel reconnects and cached views revalidate.
    if (!offline) emit("ark:reconnected", { state });
  },
});

/** Re-check reachability now (writes, focus, visibility, browser online). */
export function checkConnectivity() {
  return connectivity.check();
}

function syncing(delta) {
  syncState.syncing = Math.max(0, syncState.syncing + delta);
  try { window.dispatchEvent(new CustomEvent("ark:syncing", { detail: { count: syncState.syncing } })); } catch { /* non-browser */ }
}

function emit(name, detail) {
  try { window.dispatchEvent(new CustomEvent(name, { detail })); } catch { /* non-browser */ }
}

function apiError(response, data) {
  const code = data?.error?.code || "";
  const english = data?.error?.message || (typeof data?.error === "string" ? data.error : "") || `HTTP ${response.status}`;
  // Show the server's message in the UI language when i18n_server.js knows
  // it; keep the English original on serverMessage.
  const error = new Error(data?.error && typeof data.error === "object"
    ? serverErrorText(data.error, english) : english);
  error.serverMessage = english;
  error.status = response.status;
  error.code = code;
  error.data = data;
  return error;
}

function reportAuthErrors(error) {
  if (error.status === 401 && String(error.code).startsWith("MOBILE_")) {
    emit("ark:mobile-auth-required", { code: error.code });
  }
  if (error.status === 401 && error.code === "AUTH_REQUIRED") {
    // The server says this session is not valid: nothing cached may stay.
    clearClientData().catch(() => {});
    emit("ark:auth-required", { code: error.code });
  }
}

// Codes meaning this account cannot read the project at all (grant revoked).
// Anything else that is 403/404 (a private session of someone else, an
// admin-only panel, a deleted session) only drops that one cached entry.
const PROJECT_LOST = new Set(["PROJECT_NOT_ACCESSIBLE", "NO_PROJECT_ACCESS"]);

/** One HTTP exchange; returns { response, text, data } or throws. */
async function exchange(path, opts) {
  const started = performance.now();
  let response;
  try {
    response = await fetch(appUrl(path), opts);
  } catch (error) {
    if (error.name === "AbortError") throw error;
    logNet({ path, method: opts.method || "GET", status: 0, ms: Math.round(performance.now() - started), served: "error" });
    connectivity.failure("offline");
    const failure = new Error(t("api.network_error"));
    failure.network = true;
    throw failure;
  }
  const text = response.status === 304 ? "" : await response.text();
  logNet({
    path, method: opts.method || "GET", status: response.status, bytes: text.length,
    ms: Math.round(performance.now() - started), served: response.status === 304 ? "304" : "network",
  });
  let data = null;
  try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
  if (isProxyFailure(response.status, data)) {
    connectivity.failure("unreachable");
    const failure = apiError(response, data);
    failure.network = true;
    failure.unreachable = true;
    throw failure;
  }
  connectivity.success();
  return { response, text, data };
}

async function request(path, options = {}) {
  const method = String(options.method || "GET").toUpperCase();
  if (method === "GET" && !options.noCache && clientCache.cacheable(path)) {
    return cachedGet(path, options);
  }
  const opts = { headers: {}, ...options };
  delete opts.fresh;
  delete opts.noCache;
  delete opts.write;
  // A POST that changes no project data (opening a project) must not make
  // the next reads skip the cache as if this tab had just written.
  const writes = method !== "GET" && options.write !== false;
  opts.headers = authHeaders(opts.headers);
  if (opts.body && typeof opts.body !== "string") {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.body);
  }
  if (method !== "GET" && connectivity.offline && !(await connectivity.check())) {
    // Cached data is read-only while the server is unreachable; a write
    // re-checks first and is never queued.
    const error = new Error(t("cache.offline_write"));
    error.offline = true;
    throw error;
  }
  let result;
  try {
    const pre = method === "GET" && !options.signal ? takeEarly(path, tabProject) : null;
    const early304 = pre && await pre;
    if (early304?.result && early304.result.response.status !== 304) result = early304.result;
    result ||= await exchange(path, opts);
  } catch (error) {
    throw error;
  } finally {
    if (writes) {
      window.__ARK_LAST_WRITE_AT__ = Date.now();
      syncState.ownWriteAt = Date.now();
    }
  }
  const { response, data, text } = result;
  if (!response.ok || data?.ok === false) {
    const error = apiError(response, data);
    reportAuthErrors(error);
    throw error;
  }
  if (method === "GET" && !options.noCache && !tabProject && clientCache.identity && cacheRoute(path)) {
    // No tab project yet (first request of a new tab): file the reply under
    // the project the server chose, so the next start renders from it.
    const served = response.headers.get("X-ARK-Project-Id") || "";
    if (served) storeResponse(path, data, { etag: response.headers.get("ETag") || "", text, project: served });
  }
  return data;
}

// --------------------------------------------------------- cached reads
// Cacheable GETs (client-cache-model.js ROUTES) render from the cache: fresh
// entries cost no request, others are shown at once and revalidated in the
// background with If-None-Match (a 304 is a tiny round trip).  A changed
// response updates the cache and fires "ark:cache-updated" {route, kind, data}
// so the view refreshes in place.  The graph snapshot revalidates through
// GET /api/changes (merged locally) instead of downloading it again.
const revalidations = new Map();

function share(data, bytes) {
  // Small payloads are copied so a view mutating its data cannot change the
  // cache; the snapshot is shared (views treat it as read-only).
  if (bytes > 512 * 1024 || typeof structuredClone !== "function") return data;
  try { return structuredClone(data); } catch { return data; }
}

async function cachedGet(path, options) {
  const project = tabProject;
  const entry = await clientCache.get(path, project);
  const rule = cacheRoute(path);
  const policy = servePolicy(entry, rule, { ...syncState, now: Date.now() }, { fresh: Boolean(options.fresh) });
  // Graph data is large and treated as read-only by its users: never copied.
  const size = ["snapshot", "bodies"].includes(rule.kind) ? Infinity : (entry?.bytes || 0);
  if (policy === "fresh") {
    logNet({ path, method: "GET", status: 200, bytes: 0, ms: 0, served: "cache" });
    return share(entry.data, size);
  }
  if (policy === "swr" && !options.fresh) {
    logNet({ path, method: "GET", status: 200, bytes: 0, ms: 0, served: "cache-stale" });
    revalidate(path, entry, project).catch(() => { /* shown from cache; next sync retries */ });
    return share(entry.data, size);
  }
  const data = await revalidate(path, entry, project, options.signal);
  return share(data, size);
}

// ------------------------------------------------------- early requests
// Startup asks the server who is signed in before any cached data may be
// shown.  Requests every start needs anyway (bootstrap, Home overview) are
// sent at the same time and adopted by the cache afterwards, so a cold start
// does not wait for one more round trip.
const early = new Map();

/**
 * Adopt the requests boot.js sent before any module loaded (first visit):
 * they serve as this tab's early requests for the same paths and project.
 */
export function adoptBootRequests() {
  const boot = typeof window !== "undefined" ? window.ARKEarly : null;
  if (!boot?.requests) return;
  window.ARKEarly = null;
  for (const [path, promise] of Object.entries(boot.requests)) {
    const forProject = boot.projects?.[path] ?? boot.project ?? "";
    const pending = promise.then((reply) => {
      let data = null;
      try { data = reply.text ? JSON.parse(reply.text) : {}; } catch { data = { raw: reply.text }; }
      logNet({ path, method: "GET", status: reply.status, bytes: reply.text.length, ms: reply.ms, served: "boot" });
      if (isProxyFailure(reply.status, data)) {
        connectivity.failure("unreachable");
        return null;
      }
      connectivity.success();
      return { etag: "", result: { response: reply, text: reply.text, data } };
    }, () => null);
    const item = { project: typeof forProject === "string" ? forProject : null, pending };
    // A project learned from the bootstrap reply is known once it arrives.
    if (item.project === null) Promise.resolve(forProject).then((id) => { item.project = id || ""; }, () => { item.project = ""; });
    early.set(path, item);
  }
}

export function startEarlyGet(path) {
  if (early.has(path)) return early.get(path).pending;
  const project = tabProject;
  const pending = (async () => {
    // Revalidate whatever this browser holds for the route.  The validator is
    // opaque, and a 304 is only used for the matching account's entry.
    const etag = await clientCache.anyEtag(path, project).catch(() => "");
    const extra = {};
    if (project) extra["X-ARK-Project"] = project;
    if (etag) extra["If-None-Match"] = etag;
    const result = await exchange(path, { headers: authHeaders(extra) }).catch(() => null);
    return { etag, result };
  })();
  early.set(path, { project, pending });
  return pending;
}

function takeEarly(path, project) {
  const item = early.get(path);
  if (!item) return null;
  early.delete(path);
  if (item.expires && Date.now() > item.expires) return null;
  return item.project === project ? item.pending : null;
}

// How long a project-switch prefetch may stand in for the view's own read.
const SWITCH_PREFETCH_MS = 20_000;

/**
 * Project switch: read ``paths`` for ``project`` while POST /api/projects/open
 * is still on its way, so the new project's first screen does not wait for
 * another round trip.  A reply is used only if the server answered for that
 * project (X-ARK-Project-Id; a server with one shared current project may
 * still answer for the old one) and only by the next read of the same path
 * and project within SWITCH_PREFETCH_MS.  ``dropProjectPrefetch`` forgets
 * them when the switch fails or a later click wins.
 */
export function prefetchProject(project, paths) {
  if (!project) return;
  for (const path of paths) {
    if (!clientCache.cacheable(path)) continue;
    const pending = (async () => {
      const etag = await clientCache.anyEtag(path, project).catch(() => "");
      const extra = { "X-ARK-Project": project };
      if (etag) extra["If-None-Match"] = etag;
      const result = await exchange(path, { headers: authHeaders(extra) }).catch(() => null);
      if (!result || result.response.headers.get("X-ARK-Project-Id") !== project) return null;
      return { etag, result };
    })();
    early.set(path, { project, pending, expires: Date.now() + SWITCH_PREFETCH_MS, prefetch: true });
  }
}

export function dropProjectPrefetch(project = null) {
  for (const [path, item] of early) {
    if (item.prefetch && (project === null || item.project === project)) early.delete(path);
  }
}

/**
 * Prefetch a cacheable GET only when nothing is cached for it yet (idle
 * prefetch must not re-download what the view just loaded).
 */
export async function prefetchRoute(path) {
  if (!clientCache.cacheable(path)) return false;
  if (await clientCache.get(path, tabProject)) return false;
  await revalidate(path, null, tabProject);
  return true;
}

function revalidate(path, entry, project, signal = null) {
  const key = `${project}|${path}`;
  if (revalidations.has(key)) return revalidations.get(key);
  if (signal) {
    // An abortable read (filter facets) is not shared with other callers.
    return revalidateOnce(path, entry, project, signal);
  }
  const run = revalidateOnce(path, entry, project).finally(() => revalidations.delete(key));
  revalidations.set(key, run);
  return run;
}

function revalidateOnce(path, entry, project, signal = null) {
  syncing(1);
  const run = (async () => {
    const rule = cacheRoute(path);
    if ((rule.kind === "snapshot" || rule.kind === "bodies") && entry?.cursor) {
      const merged = await revalidateByDelta(path, entry, project);
      if (merged) return merged;
    }
    const headers = authHeaders(entry?.etag ? { "If-None-Match": entry.etag } : {});
    if (project) headers["X-ARK-Project"] = headers["X-ARK-Project"] || project;
    let result;
    try {
      // A request started before the account was known (startEarlyGet)
      // serves as this fetch when it was made for the same project.
      const pre = takeEarly(path, project);
      if (pre) {
        const early304 = await pre;
        // A 304 only counts when it confirms the entry this account holds.
        if (early304?.result && !(early304.result.response.status === 304
            && (!entry || entry.etag !== early304.etag))) result = early304.result;
      }
      result ||= await exchange(path, { headers, signal: signal || undefined });
    } catch (error) {
      // No answer from ARK (network or proxy): keep showing the cache.
      if (error.network && entry) return entry.data;
      throw error;
    }
    const { response, text, data } = result;
    if (response.status === 304 && entry) {
      clientCache.validated(path, { project });
      return entry.data;
    }
    if (!response.ok || data?.ok === false) {
      const error = apiError(response, data);
      if (rule.scope === "project" && PROJECT_LOST.has(error.code)) {
        await clientCache.dropProject(project);
        emit("ark:project-access-lost", { project, code: error.code });
      } else if ([403, 404, 410].includes(response.status)) {
        await clientCache.remove(path, project);
      }
      reportAuthErrors(error);
      throw error;
    }
    const served = response.headers.get("X-ARK-Project-Id") || "";
    if (rule.scope === "project" && project && served && served !== project) {
      if (!entry) {
        // Nothing cached for the requested project (a guessed or unreadable
        // tab project): file the reply under the project actually served.
        storeResponse(path, data, { etag: response.headers.get("ETag") || "", text, project: served });
        return data;
      }
      // The server answered for another project: this account can no longer
      // read the requested one.  Forget it; never file the reply under it.
      await clientCache.dropProject(project);
      emit("ark:project-access-lost", { project, served, route: rule.route, data });
      return data;
    }
    const etag = response.headers.get("ETag") || "";
    storeResponse(path, data, { etag, text, project });
    if (entry && entry.etag !== etag) {
      emit("ark:cache-updated", { route: rule.route, kind: rule.kind, data, project });
    }
    return data;
  })().finally(() => syncing(-1));
  return run;
}

function storeResponse(path, data, { etag, text, project }) {
  const rule = cacheRoute(path);
  if (!rule) return;
  if (rule.kind === "unit" && data?.unit) {
    // Cache a unit under both its id and uid (links use either).
    for (const ref of new Set([String(data.unit.id), String(data.unit.uid || "")])) {
      if (ref) clientCache.put(`/api/units/${encodeURIComponent(ref)}`, data, { etag, text, project });
    }
    return;
  }
  clientCache.put(path, data, { etag, text, project });
}

async function revalidateByDelta(path, entry, project) {
  const lite = entry.data?.bodies === false;
  const headers = authHeaders(project ? { "X-ARK-Project": project } : {});
  let result;
  try {
    result = await exchange(`/api/changes${qs({ since: entry.cursor, bodies: lite ? 0 : 1 })}`, { headers });
  } catch (error) {
    if (error.network) return entry.data;
    throw error;
  }
  const { response, data: delta } = result;
  const served = response.headers.get("X-ARK-Project-Id") || "";
  if (!response.ok || delta?.ok === false || (served && project && served !== project)) return null;
  if (delta.reset) return null;
  applyDelta(delta, project, { from: "revalidate" });
  const fresh = await clientCache.get(path, project);
  return fresh?.data || null;
}

/**
 * Apply GET /api/changes to every cached snapshot of the project, mark the
 * affected details/aggregates stale and announce the change.  Returns true
 * when anything changed.
 */
export function applyDelta(delta, project = tabProject, { from = "sync" } = {}) {
  let changed = false;
  for (const path of ["/api/graph/snapshot", "/api/graph/snapshot?bodies=0", "/api/graph/bodies"]) {
    const key = clientCache.keyFor(path, project);
    if (!key) continue;
    const held = clientCache.peek(path, project);
    if (!held) {
      // Not decoded in this tab: revalidate it (through a delta) when used.
      clientCache.markStale((route) => route === cacheRoute(path).route, project);
      continue;
    }
    if (held.cursor === delta.cursor) {
      clientCache.validated(path, { project });
      continue;
    }
    const merged = path === "/api/graph/bodies"
      ? mergeBodiesDelta(held.data, delta) : mergeSnapshotDelta(held.data, delta);
    if (!merged) continue;
    clientCache.put(path, merged, { cursor: delta.cursor, project });
    changed = true;
  }
  const touched = (delta.touched || []).length || (delta.deleted || []).length;
  if (touched) {
    clientCache.markStale((route) => staleAfterDelta(route, delta), project);
    changed = true;
  }
  if (changed) emit("ark:remote-delta", { delta, project, from });
  return changed;
}

/**
 * Prefetch unit details in one round trip (GET /api/unit-details) into the
 * cache.  Returns how many were stored.
 */
export async function prefetchUnits(ids, { project = tabProject } = {}) {
  const list = [...new Set((ids || []).map(String).filter(Boolean))].slice(0, 60);
  if (!list.length || !clientCache.identity) return 0;
  const headers = authHeaders(project ? { "X-ARK-Project": project } : {});
  const { response, data } = await exchange(`/api/unit-details${qs({ ids: list.join(",") })}`, { headers });
  const served = response.headers.get("X-ARK-Project-Id") || "";
  if (!response.ok || data?.ok === false || (served && project && served !== project)) return 0;
  for (const unit of data.units || []) {
    const etag = data.etags?.[String(unit.id)] || "";
    storeResponse(`/api/units/${unit.id}`, { ok: true, unit }, { etag, text: null, project });
  }
  return (data.units || []).length;
}

/**
 * Show cached data at once, then the server's: ``apply(data, {cached})`` runs
 * with the cached copy (if any) and again with the fresh one when it
 * differs.  For views that render live data (sessions, GPU, resources).
 */
export async function cachedThenFresh(path, load, apply) {
  const cached = await peekCached(path).catch(() => null);
  let shown = "";
  if (cached) {
    shown = JSON.stringify(cached);
    apply(cached, { cached: true });
  }
  const fresh = await load({ fresh: true });
  if (!cached || JSON.stringify(fresh) !== shown) apply(fresh, { cached: false });
  return fresh;
}

/** Cached data for a GET path without any request (or null). */
export async function peekCached(path) {
  const entry = await clientCache.get(path, tabProject);
  return entry ? entry.data : null;
}

export function qs(params = {}) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    if (Array.isArray(value)) {
      for (const item of value) if (item !== "") search.append(key, item);
    } else search.set(key, value);
  }
  const text = search.toString();
  return text ? `?${text}` : "";
}

// A request a user view plugin made through its bridge (plugin-host.js): the
// signed-in user's own session and permissions, labelled with the plugin id
// so the server logs the write as "human:plugin:<id>" (activity, node
// history).  Never cached.
export function pluginRequest(pluginId, path, { method = "GET", body } = {}) {
  return request(path, { method, ...(body === undefined ? {} : { body }), headers: { "X-ARK-Plugin": String(pluginId) }, noCache: true });
}

export const api = {
  bootstrap: (options = {}) => request("/api/bootstrap", options),
  authSession: () => request("/api/auth/session"),
  // Signing out forgets every cached project of this browser profile first.
  logout: async () => {
    await clearClientData();
    return request("/api/auth/logout", { method: "POST", body: {} });
  },
  revision: () => request("/api/revision"),
  // Live agent presence (T-0043, presence.js); never cached.
  presence: () => request("/api/presence", { noCache: true }),
  changes: (since, { lite = true } = {}) => request(`/api/changes${qs({ since, bodies: lite ? 0 : 1 })}`),
  remoteInfo: () => request("/api/remote/info"),
  mobileSession: () => request("/api/mobile/session"),
  connectionInfo: () => request("/api/mobile/connection"),
  mobileApps: () => request("/api/mobile/apps"),
  createPairing: (body) => request("/api/mobile/pair", { method: "POST", body }),
  claimPairing: (token) => request("/api/mobile/pair/claim", { method: "POST", body: { token } }),
  pairings: () => request("/api/mobile/pairs"),
  revokePairing: (id) => request(`/api/mobile/pairs/${encodeURIComponent(id)}/revoke`, { method: "POST", body: {} }),
  // ARK Studio desktop link code (Go-only route; desktop-hub.js).
  linkDesktop: ({ signal, ...body } = {}) => request("/api/desktop/link", { method: "POST", body, signal }),
  logoutMobile: () => request("/api/mobile/session", { method: "DELETE" }),
  project: (options = {}) => request("/api/project", options),
  assignees: () => request("/api/project/assignees"),
  // `query`: graph_limit/graph_types/graph_statuses/graph_days when the
  // Home filter is not on the account (home-filter-model.js).
  overview: (options = {}, query = {}) => request(`/api/project/overview${qs(query)}`, options),
  projects: () => request("/api/projects"),
  createProject: (body) => request("/api/projects", { method: "POST", body }),
  openProject: (body) => request("/api/projects/open", { method: "POST", body, write: false }),
  updateProject: (body) => request("/api/project", { method: "PATCH", body }),
  deleteProject: (body) => request("/api/project", { method: "DELETE", body }),
  updateSettings: (body) => request("/api/project/settings", { method: "PATCH", body }),
  effectiveSettings: () => request("/api/settings/effective"),
  personalSettings: (scope = "assistant") => request(`/api/personal/settings${qs({ scope })}`),
  savePersonalSettings: (scope, body) => request(
    `/api/personal/settings${qs({ scope })}`, { method: "POST", body }),
  changePassword: (body) => request("/api/org/password", { method: "POST", body }),
  me: (options = {}) => request("/api/me", options),
  updateMe: (body) => request("/api/me", { method: "PATCH", body }),
  uploadAvatar: (body) => request("/api/me/avatar", { method: "POST", body }),
  removeAvatar: () => request("/api/me/avatar", { method: "DELETE" }),
  changeOwnPassword: (body) => request("/api/me/password", { method: "POST", body }),
  orgOverview: () => request("/api/org"),
  orgUsers: () => request("/api/org/users"),
  createOrgUser: (body) => request("/api/org/users", { method: "POST", body }),
  updateOrgUser: (id, body) => request(`/api/org/users/${encodeURIComponent(id)}`,
    { method: "PATCH", body }),
  deleteOrgUser: (id) => request(`/api/org/users/${encodeURIComponent(id)}`,
    { method: "DELETE" }),
  resetOrgPassword: (id, body) => request(
    `/api/org/users/${encodeURIComponent(id)}/password`, { method: "POST", body }),
  orgTeams: () => request("/api/org/teams"),
  orgProject: (id) => request(`/api/org/projects/${encodeURIComponent(id)}`),
  linuxAccounts: (userId) => request(
    `/api/org/users/${encodeURIComponent(userId)}/linux-accounts`),
  assignLinuxAccount: (userId, account) => request(
    `/api/org/users/${encodeURIComponent(userId)}/linux-accounts`,
    { method: "POST", body: { account } }),
  revokeLinuxAccount: (userId, account) => request(
    `/api/org/users/${encodeURIComponent(userId)}/linux-accounts/${encodeURIComponent(account)}`,
    { method: "DELETE" }),
  personalEnvironment: () => request("/api/personal/settings?scope=environment"),
  savePersonalEnvironment: (body) => request(
    "/api/personal/settings?scope=environment", { method: "POST", body }),
  createOrgTeam: (body) => request("/api/org/teams", { method: "POST", body }),
  updateOrgTeam: (id, body) => request(`/api/org/teams/${encodeURIComponent(id)}`,
    { method: "PATCH", body }),
  deleteOrgTeam: (id) => request(`/api/org/teams/${encodeURIComponent(id)}`,
    { method: "DELETE" }),
  addTeamMember: (id, user) => request(
    `/api/org/teams/${encodeURIComponent(id)}/members`, {
      method: "POST", body: { user },
    }),
  removeTeamMember: (id, userId) => request(
    `/api/org/teams/${encodeURIComponent(id)}/members/${encodeURIComponent(userId)}`,
    { method: "DELETE" }),
  grantProject: (projectId, body) => request(
    `/api/org/projects/${encodeURIComponent(projectId)}/grants`,
    { method: "POST", body }),
  revokeProjectGrant: (projectId, body) => request(
    `/api/org/projects/${encodeURIComponent(projectId)}/grants/revoke`,
    { method: "POST", body }),
  // Project invite links (Go only, routes_invites.go).
  projectInvites: (projectId) => request(`/api/org/projects/${encodeURIComponent(projectId)}/invites`),
  createProjectInvite: (projectId, body) => request(
    `/api/org/projects/${encodeURIComponent(projectId)}/invites`, { method: "POST", body }),
  revokeProjectInvite: (projectId, inviteId) => request(
    `/api/org/projects/${encodeURIComponent(projectId)}/invites/${encodeURIComponent(inviteId)}/revoke`,
    { method: "POST", body: {} }),
  inviteSettings: () => request("/api/org/invite-settings"),
  saveInviteSettings: (body) => request("/api/org/invite-settings", { method: "POST", body }),
  invitePreview: (token) => request("/api/auth/invite/preview", { method: "POST", body: { token }, write: false }),
  acceptInvite: (token) => request("/api/auth/invite/accept", { method: "POST", body: { token } }),

  skills: () => request("/api/skills"),
  setupState: () => request("/api/skills/setup/state"),
  setupConfirm: (body) => request("/api/skills/setup/confirm", { method: "POST", body }),
  // Multi-user setup (docs/operations/multi-user-setup.md): resource, remove, location,
  // host, propose, verify, request-access, draft.
  setupAction: (action, body = {}) => request(`/api/skills/setup/${encodeURIComponent(action)}`,
    { method: "POST", body }),
  skill: (id) => request(`/api/skills/${encodeURIComponent(id)}`),
  renderSkill: (id, body) => request(`/api/skills/${encodeURIComponent(id)}/render`, { method: "POST", body }),
  validateSkill: (id, body) => request(`/api/skills/${encodeURIComponent(id)}/validate`, { method: "POST", body }),
  skillTask: (id, body) => request(`/api/skills/${encodeURIComponent(id)}/tasks`, { method: "POST", body }),

  units: (params = {}, options = {}) => request(`/api/units${qs(params)}`, options),
  unit: (id, options = {}) => request(`/api/units/${encodeURIComponent(id)}`, options),
  createUnit: (body) => request("/api/units", { method: "POST", body }),
  updateUnit: (id, body) => request(`/api/units/${encodeURIComponent(id)}`, { method: "PATCH", body }),
  nodeHistory: (id, params = {}, project) => request(`/api/units/${encodeURIComponent(id)}/history${qs(params)}`,
    { headers: project ? { "X-ARK-Project": project } : {} }),
  setNodeHistory: (id, enabled, project) => request(`/api/units/${encodeURIComponent(id)}/history`,
    { method: "POST", body: { enabled }, headers: project ? { "X-ARK-Project": project } : {} }),
  nodeRevision: (id, revision, project) => request(`/api/units/${encodeURIComponent(id)}/history/${encodeURIComponent(revision)}`,
    { headers: project ? { "X-ARK-Project": project } : {} }),
  nodeDiff: (id, params = {}, project) => request(`/api/units/${encodeURIComponent(id)}/history/diff${qs(params)}`,
    { headers: project ? { "X-ARK-Project": project } : {} }),
  restoreNodeRevision: (id, body, project) => request(`/api/units/${encodeURIComponent(id)}/history/restore`,
    { method: "POST", body, headers: project ? { "X-ARK-Project": project } : {} }),
  applyNodeHistoryChange: (id, body, project) => request(`/api/units/${encodeURIComponent(id)}/history/apply`,
    { method: "POST", body, headers: project ? { "X-ARK-Project": project } : {} }),
  saveNodeVersion: (id, expected_hash, project) => request(`/api/units/${encodeURIComponent(id)}/history/checkpoint`,
    { method: "POST", body: { expected_hash }, headers: project ? { "X-ARK-Project": project } : {} }),
  // Review requests and review groups (server routes_reviews.go).
  unitReview: (id) => request(`/api/units/${encodeURIComponent(id)}/review`),
  reviewGroups: (params = {}) => request(`/api/reviews${qs(params)}`),
  reviewGroup: (id) => request(`/api/reviews/${encodeURIComponent(id)}`),
  createReviewGroup: (body) => request("/api/reviews", { method: "POST", body }),
  updateReviewGroup: (id, body) => request(`/api/reviews/${encodeURIComponent(id)}`, { method: "POST", body }),
  decideReviewGroup: (id, body) => request(`/api/reviews/${encodeURIComponent(id)}/decide`, { method: "POST", body }),
  reviewGroupComment: (id, body) => request(`/api/reviews/${encodeURIComponent(id)}/comments`, { method: "POST", body }),
  archiveUnit: (id) => request(`/api/units/${encodeURIComponent(id)}/archive`, { method: "POST", body: {} }),
  restoreUnit: (id) => request(`/api/units/${encodeURIComponent(id)}/restore`, { method: "POST", body: {} }),
  // Recycle bin: deleted nodes (archiveUnit puts them there).
  trash: () => request("/api/trash"),
  purgeUnit: (id) => request(`/api/trash/${encodeURIComponent(id)}`, { method: "DELETE" }),
  emptyTrash: () => request("/api/trash", { method: "DELETE" }),
  insertBetween: (id, body = {}) => request(`/api/units/${encodeURIComponent(id)}/insert-between`, { method: "POST", body }),
  // Multi-selection (Go only): shared-attribute read, one edit for many
  // units in one transaction, and grouping siblings under a new parent.
  unitsBatch: (ids = []) => request(`/api/units/batch${qs({ units: ids.join(",") })}`, { noCache: true }),
  batchUpdate: (body) => request("/api/units/batch", { method: "POST", body }),
  groupUnits: (body) => request("/api/units/group", { method: "POST", body }),
  ungroupUnit: (id, body = {}) => request(`/api/units/${encodeURIComponent(id)}/ungroup`, { method: "POST", body }),
  comments: (id) => request(`/api/units/${encodeURIComponent(id)}/comments`),
  addComment: (id, body) => request(`/api/units/${encodeURIComponent(id)}/comments`, { method: "POST", body }),
  resolveComment: (id, commentId, resolved = true) => request(
    `/api/units/${encodeURIComponent(id)}/comments/${encodeURIComponent(commentId)}/resolve`, { method: "POST", body: { resolved } }),
  graph: (params = {}) => request(`/api/graph${qs(params)}`),
  // Lite snapshot: no node bodies (most of the bytes); graphBodies() adds them
  // for text filtering.  Both revalidate through GET /api/changes.
  graphSnapshot: (options = {}) => request("/api/graph/snapshot?bodies=0", options),
  graphBodies: (options = {}) => request("/api/graph/bodies", options),
  unitDetails: (ids) => request(`/api/unit-details${qs({ ids: (ids || []).join(",") })}`),
  timeline: (params = {}) => request(`/api/timeline${qs(params)}`),
  activity: (limit = 40) => request(`/api/activity${qs({ limit })}`),

  createLink: (body) => request("/api/links", { method: "POST", body }),
  topics: () => request("/api/topics"),
  topic: (id, { full = false } = {}) => request(`/api/units/${encodeURIComponent(id)}/members${full ? "?full=1" : ""}`),
  addTopicMembers: (id, members) => request(`/api/units/${encodeURIComponent(id)}/members`, { method: "POST", body: { add: members } }),
  designTopic: (id, body) => request(`/api/units/${encodeURIComponent(id)}/members`, { method: "PATCH", body }),
  updateLink: (id, body) => request(`/api/links/${encodeURIComponent(id)}`, { method: "PATCH", body }),
  deleteLink: (id) => request(`/api/links/${id}`, { method: "DELETE" }),
  addMedia: (body) => request("/api/media", { method: "POST", body }),
  uploadMedia: (body) => request("/api/media/upload", { method: "POST", body }),
  deleteMedia: (id) => request(`/api/media/${id}`, { method: "DELETE" }),
  updateMedia: (id, body) => request(`/api/media/${id}`, { method: "PATCH", body }),
  openExternal: (body) => request("/api/open", { method: "POST", body }),

  insights: () => request("/api/assistant/insights"),
  applyInsight: (insight) => request("/api/assistant/apply", { method: "POST", body: { insight } }),
  extract: (body) => request("/api/assistant/extract", { method: "POST", body }),
  chat: (limit = 100) => request(`/api/chat?limit=${limit}`),
  assistantConfig: () => request("/api/assistant/config"),
  saveAssistantConfig: (body) => request("/api/assistant/config", { method: "POST", body }),
  testAssistantConfig: () => request("/api/assistant/config/test", { method: "POST", body: {} }),
  chatSend: (message, extra = {}) => request("/api/chat", { method: "POST", body: { message, ...extra } }),
  chatStream: async (message, onEvent, extra = {}) => {
    const response = await fetch(appUrl("/api/chat/stream"), {
      method: "POST",
      headers: authHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify({ message, ...extra }),
    });
    if (!response.ok) {
      const text = await response.text();
      let detail = text;
      try {
        const parsed = JSON.parse(text)?.error;
        detail = parsed?.message ? serverErrorText(parsed, parsed.message) : text;
      } catch { /* plain text */ }
      throw new Error(detail || t("api.chat_stream_failed", { status: response.status }));
    }
    if (!response.body) throw new Error(t("api.streaming_unsupported"));
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "", finished = false;
    const dispatch = (line) => {
      const event = JSON.parse(line);
      if (event.type === "done" || event.type === "error") finished = true;
      onEvent?.(event);
    };
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let index;
        while ((index = buffer.indexOf("\n")) >= 0) {
          const line = buffer.slice(0, index).trim();
          buffer = buffer.slice(index + 1);
          if (line) dispatch(line);
        }
      }
      buffer += decoder.decode();
      if (buffer.trim()) dispatch(buffer.trim());
      if (!finished) throw new Error(t("chat.interrupted"));
    } finally {
      await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  },
  clearChat: () => request("/api/chat", { method: "DELETE" }),
  dispatch: (task) => request(`/api/assistant/dispatch${qs({ task })}`),
  importSources: () => request("/api/import/infohub/sources"),
  previewImport: (path) => request(`/api/import/infohub/preview${qs({ path })}`),
  importInfoHub: (body) => request("/api/import/infohub", { method: "POST", body }),
  infoHubBrowse: (params = {}) => request(`/api/infohub/browse${qs(params)}`),
  infoHubGraph: (params = {}) => request(`/api/infohub/graph${qs(params)}`),
  infoHubUnit: (params = {}) => request(`/api/infohub/unit${qs(params)}`),
  infoHubImport: (body) => request("/api/infohub/import", { method: "POST", body }),
  // Go only (routes_importark.go): import from another ARK project.  Not
  // cached: the reply is about another project.
  importArkGraph: (params = {}) => request(`/api/import-ark/graph${qs(params)}`, { noCache: true }),
  importArkUnit: (project, uid) => request(`/api/import-ark/unit${qs({ project, uid })}`, { noCache: true }),
  importArkPreview: (body) => request("/api/import-ark/preview", { method: "POST", body, write: false }),
  importArkApply: (body) => request("/api/import-ark/apply", { method: "POST", body }),
  agentToken: () => request("/api/agent/token"),
  agentTokens: () => request("/api/personal/agent-tokens"),
  // Agent rules (Settings > Agents and Personal AI; the Home starter prompts).
  agentRules: () => request("/api/agent-rules"),
  saveAgentRules: (scope, settings) => request("/api/agent-rules", { method: "POST", body: { scope, settings } }),
  previewAgentRules: (scope, settings) => request("/api/agent-rules", { method: "POST", body: { scope, settings, dry_run: true }, write: false }),
  // Experimental agent messages (agent-messages.js; docs/development/agent-messaging.md).
  agentMessages: (unit = "") => request(`/api/agent-messages${unit ? `?unit=${encodeURIComponent(unit)}` : ""}`),
  sendAgentMessage: (body) => request("/api/agent-messages", { method: "POST", body }),
  createAgentToken: (body) => request("/api/personal/agent-tokens", { method: "POST", body }),
  revokeAgentToken: (id) => request(`/api/personal/agent-tokens/${encodeURIComponent(id)}`, { method: "DELETE" }),

  gpuStatus: () => request("/api/gpu/status"),
  gpuServers: () => request("/api/gpu/servers"),
  gpuProbe: (body = {}) => request("/api/gpu/probe", { method: "POST", body }),
  gpuPrepareDispatch: (body) => request("/api/gpu/dispatch/prepare", { method: "POST", body }),
  gpuQueueStatus: () => request("/api/gpu/queue"),
  gpuDashboard: (options = {}) => request("/api/gpu/dashboard", options),
  resourcesGraph: (options = {}) => request("/api/resources/graph", options),
  resourceDevicePrivacy: (id, body) => request(`/api/resources/devices/${encodeURIComponent(id)}`, { method: "POST", body }),
  blackbox: () => request("/api/blackbox"),
  blackboxSnapshot: (reason = "manual") => request("/api/blackbox", {
    method: "POST", body: { reason },
  }),
  blackboxRestore: (id) => request(`/api/blackbox/${encodeURIComponent(id)}/restore`, {
    method: "POST", body: {},
  }),
  gpuJobs: () => request("/api/gpu/jobs"),
  gpuSubmit: (body) => request("/api/gpu/jobs", { method: "POST", body }),
  gpuCancel: (id) => request(`/api/gpu/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST", body: {} }),
  gpuJobLogs: (id) => request(`/api/gpu/jobs/${encodeURIComponent(id)}/logs`),

  // Session context (docs/agents/session-context.md): internal + uploaded sessions.
  sessionContexts: (options = {}) => request("/api/session-contexts", options),
  sessionContextTranscript: (id) => request(`/api/session-contexts/${encodeURIComponent(id)}/transcript`),
  sessionContextSummary: (id) => request(`/api/session-contexts/${encodeURIComponent(id)}/context`),
  updateSessionContext: (id, body) => request(`/api/session-contexts/${encodeURIComponent(id)}`, { method: "PATCH", body }),
  deleteSessionContext: (id) => request(`/api/session-contexts/${encodeURIComponent(id)}`, { method: "DELETE" }),
  continueSessionContext: (id, body) => request(`/api/session-contexts/${encodeURIComponent(id)}/continue`, { method: "POST", body }),
  sessionContextRawUrl: (id) => authenticatedUrl(`/api/session-contexts/${encodeURIComponent(id)}/raw`),
  sessionPresets: (options = {}) => request("/api/agent-sessions/presets", options),
  sessionGroups: (options = {}) => request("/api/agent-sessions/groups", options),
  agentSessions: (options = {}) => request("/api/agent-sessions", options),
  providers: () => request("/api/providers"),
  refreshProviders: () => request("/api/providers/refresh", { method: "POST", body: {} }),
  worktrees: (options = {}) => request("/api/worktrees", options),
  createWorktree: (body) => request("/api/worktrees/create", { method: "POST", body }),
  removeWorktree: (body) => request("/api/worktrees/remove", { method: "POST", body }),
  agentSession: (id, options = {}) => request(`/api/agent-sessions/${encodeURIComponent(id)}`, options),
  createSession: (body) => request("/api/agent-sessions", { method: "POST", body }),
  sessionInput: (id, text, context = null) => request(`/api/agent-sessions/${encodeURIComponent(id)}/input`,
    { method: "POST", body: context ? { text, context } : { text } }),
  sessionSteer: (id, text, context = null, now = false) => request(`/api/agent-sessions/${encodeURIComponent(id)}/steer`,
    { method: "POST", body: { text, ...(context ? { context } : {}), ...(now ? { now: true } : {}) } }),
  sessionRemoveSteer: (id, steerId) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/steer/${encodeURIComponent(steerId)}`,
    { method: "DELETE" }),
  sessionUpdateSteer: (id, steerId, text) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/steer/${encodeURIComponent(steerId)}`,
    { method: "PATCH", body: { text } }),
  sessionReorderSteer: (id, order) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/steer/reorder`,
    { method: "POST", body: { order } }),
  sessionApprovals: (id) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/approvals`),
  sessionApproval: (id, requestId, decision, message = "") => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/approvals/${encodeURIComponent(requestId)}`,
    { method: "POST", body: { decision, message } }),
  forkSession: (id, body) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/fork`, { method: "POST", body }),
  sessionValidateContext: (id, context) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/context/validate`,
    { method: "POST", body: { context } }),
  continueSession: (id, text, extra = {}) => request(`/api/agent-sessions/${encodeURIComponent(id)}/continue`,
    { method: "POST", body: { text, ...extra } }),
  stopSession: (id) => request(`/api/agent-sessions/${encodeURIComponent(id)}/stop`,
    { method: "POST", body: {} }),
  archiveSession: (id) => request(`/api/agent-sessions/${encodeURIComponent(id)}/archive`,
    { method: "POST", body: {} }),
  relaunchSession: (id) => request(`/api/agent-sessions/${encodeURIComponent(id)}/relaunch`,
    { method: "POST", body: {} }),
  deleteSession: (id) => request(`/api/agent-sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  renameSession: (id, title) => request(`/api/agent-sessions/${encodeURIComponent(id)}`, { method: "PATCH", body: { title } }),
  sessionLogs: (id, stream = "stdout", offset = 0, limit = 300) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/logs${qs({ stream, offset, limit })}`),
  // query: { tail, known, agent, item } (a page of items, an unchanged marker,
  // one sub-agent's transcript, one tool item).
  sessionTranscript: (id, options = {}) => {
    const { query = null, ...rest } = options;
    return request(`/api/agent-sessions/${encodeURIComponent(id)}/transcript${qs(query || {})}`, rest);
  },
  sessionSteerNow: (id, steerId) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/steer/${encodeURIComponent(steerId)}`,
    { method: "POST", body: { action: "send_now" } }),
  sessionMedia: (id) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/media`),
  sessionArtifacts: (id, path = "") => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/artifacts${qs({ path })}`),
  sessionArtifactRead: (id, path) => request(
    `/api/agent-sessions/${encodeURIComponent(id)}/artifacts/read${qs({ path })}`),
  sessionArtifactUrl: (id, path, download = false) => authenticatedUrl(
    `/api/agent-sessions/${encodeURIComponent(id)}/artifacts/${download ? "download" : "read"}${qs({ path })}`),

  // Project-scoped Files workspace (tree, editor, git review, terminals).
  workspaceInfo: () => request("/api/workspace/info"),
  workspaceTree: (path = "", depth = 1, options = {}) => request(`/api/workspace/tree${qs({ path, depth })}`, options),
  workspaceSearch: (q, limit = 100) => request(`/api/workspace/search${qs({ q, limit })}`),
  // Workbench Search: file contents and node text (title, body, tags, comments).
  workspaceGrep: (params = {}, options = {}) => request(`/api/workspace/grep${qs(params)}`, options),
  searchNodes: (params = {}, options = {}) => request(`/api/search/nodes${qs(params)}`, options),
  // Streaming variant (stream=1, NDJSON): onEvent gets {type: "node"|"file",
  // node|file}, then {type: "done", ...summary} or {type: "error", message}.
  // Abort with options.signal; the server then stops scanning.
  searchStream: async (kind, params = {}, onEvent = null, options = {}) => {
    const path = kind === "files" ? "/api/workspace/grep" : "/api/search/nodes";
    let response;
    try {
      response = await fetch(appUrl(`${path}${qs({ ...params, stream: 1 })}`), { signal: options.signal, headers: authHeaders() });
    } catch (error) {
      if (error.name === "AbortError") throw error;
      throw new Error(t("api.network_error"));
    }
    if (!response.ok) {
      let data = null;
      try { data = await response.json(); } catch { /* not JSON */ }
      const english = data?.error?.message || `HTTP ${response.status}`;
      const error = new Error(data?.error && typeof data.error === "object" ? serverErrorText(data.error, english) : english);
      error.status = response.status;
      throw error;
    }
    if (!response.body) throw new Error(t("api.streaming_unsupported"));
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const dispatch = (line) => {
      if (!line.trim()) return;
      let event;
      try { event = JSON.parse(line); } catch { return; }
      onEvent?.(event);
    };
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let index;
      while ((index = buffer.indexOf("\n")) >= 0) {
        dispatch(buffer.slice(0, index));
        buffer = buffer.slice(index + 1);
      }
    }
    dispatch(buffer + decoder.decode());
  },
  workspaceFile: (path) => request(`/api/workspace/file${qs({ path })}`),
  workspaceSave: (body) => request("/api/workspace/file", { method: "POST", body }),
  workspaceCreate: (body) => request("/api/workspace/create", { method: "POST", body }),
  workspaceRename: (body) => request("/api/workspace/rename", { method: "POST", body }),
  workspaceDelete: (body) => request("/api/workspace/delete", { method: "POST", body }),
  workspaceChanges: (base = "HEAD", options = {}) => request(`/api/workspace/changes${qs({ base })}`, options),
  workspaceDiff: (path, base = "HEAD") => request(`/api/workspace/diff${qs({ path, base })}`),
  workspaceTerminals: () => request("/api/workspace/terminals"),
  workspaceTerminal: (id, offset = 0, limit = 300) => request(
    `/api/workspace/terminals/${encodeURIComponent(id)}${qs({ offset, limit })}`),
  workspaceTerminalStart: (body) => request("/api/workspace/terminals", { method: "POST", body }),
  workspaceTerminalStdin: (id, text) => request(
    `/api/workspace/terminals/${encodeURIComponent(id)}/stdin`, { method: "POST", body: { text } }),
  workspaceTerminalCancel: (id) => request(
    `/api/workspace/terminals/${encodeURIComponent(id)}/cancel`, { method: "POST", body: {} }),
  workspaceTerminalResize: (id, cols, rows) => request(
    `/api/workspace/terminals/${encodeURIComponent(id)}/resize`,
    { method: "POST", body: { cols, rows } }),
  workspaceTerminalClose: (id) => request(
    `/api/workspace/terminals/${encodeURIComponent(id)}`, { method: "DELETE" }),
  workspaceTerminalStream: async (id, offset = 0, onEvent = null, signal = null) => {
    const response = await fetch(
      appUrl(`/api/workspace/terminals/${encodeURIComponent(id)}/stream${qs({ offset })}`),
      { signal, headers: authHeaders() });
    if (!response.ok) {
      let detail = t("api.terminal_stream_failed", { status: response.status });
      try {
        const data = await response.json();
        detail = data?.error?.message ? serverErrorText(data.error, data.error.message) : detail;
      } catch { /* plain error */ }
      throw new Error(detail);
    }
    if (!response.body) throw new Error(t("api.streaming_unsupported"));
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let finished = false;
    while (!finished) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let index;
      while ((index = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, index);
        buffer = buffer.slice(index + 2);
        let event = "message";
        let data = "";
        for (const line of frame.split("\n")) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data += line.slice(5).trim();
        }
        if (!data) continue;
        let payload;
        try { payload = JSON.parse(data); } catch { continue; }
        onEvent?.({ event, data: payload });
        if (event === "done") { finished = true; break; }
      }
    }
    try { await reader.cancel(); } catch { /* already closed */ }
  },
};

export function mediaUrl(media) {
  if (!media) return "";
  if (/^file:\/\//i.test(media.url) && media.raw_url) return authenticatedUrl(media.raw_url);
  if (/^[a-z][a-z0-9+.-]*:/i.test(media.url) || /^data:/i.test(media.url)) {
    return authenticatedUrl(media.url);
  }
  return authenticatedUrl(media.raw_url || "");
}

export function exportUrl(format, project = "") {
  const extra = project ? `&project=${encodeURIComponent(project)}` : "";
  return authenticatedUrl(`/api/export?format=${encodeURIComponent(format)}${extra}`);
}

/** ZIP of Markdown notes (with media) for the given node UIDs; depth = referenced-node hops. */
function bundleQuery(uids, { depth = 1, project = "" } = {}) {
  const units = [...new Set((uids || []).map((uid) => String(uid || "").trim()).filter(Boolean))];
  const params = new URLSearchParams({ format: "bundle", depth: String(Number(depth) || 0), units: units.join(",") });
  // Name the tab's project: the server's per-session project may differ.
  if (project) params.set("project", String(project));
  return `/api/export?${params}`;
}

export function bundleExportUrl(uids, options = {}) {
  return authenticatedUrl(bundleQuery(uids, options));
}

/** Validate a bundle export before downloading; resolves to {nodes, files, missing, bytes}. */
export function checkBundleExport(uids, options = {}) {
  return request(`${bundleQuery(uids, options)}&check=1`);
}

/**
 * Forget cached project data: IndexedDB entries of every account and the
 * service worker's media cache.  ``signOut`` also stops caching until the
 * next sign-in identifies the account again.
 */
export async function clearClientData({ signOut = true } = {}) {
  try { await clientCache.clearAll({ signOut }); } catch { /* storage unavailable */ }
  if (signOut) {
    try {
      localStorage.removeItem("ark.cache.identity");
      localStorage.removeItem("ark.cache.lastProject");
      // Home starter tokens kept for Copy prompt (starter-token-store.js).
      clearStarterTokens(localStorage);
    } catch { /* storage disabled */ }
  }
  try {
    navigator.serviceWorker?.controller?.postMessage({ type: "ark-media-clear" });
  } catch { /* no worker */ }
}

/** Bind the cache (and the worker's media cache) to the signed-in account. */
export async function configureClientCache({ cacheId, schema, project }) {
  await clientCache.configure({ cacheId, schema, project });
  try {
    navigator.serviceWorker?.controller?.postMessage({ type: "ark-media-identity", id: cacheId || "" });
  } catch { /* no worker */ }
}

export { clientCache };
