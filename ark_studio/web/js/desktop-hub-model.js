// Desktop hub model (no DOM): the ARK Studio desktop app's hub API as the
// web app uses it.  Rendering is in desktop-hub.js.

export const OPEN_PROJECT_PARAM = "ark_open_project";
export const NEW_PROJECT_PARAM = "ark_new_project";
export const LINK_DESKTOP_PARAM = "ark_link_desktop";
const PARAMS = [OPEN_PROJECT_PARAM, NEW_PROJECT_PARAM, LINK_DESKTOP_PARAM];

/** The hub of the desktop shell, or null (browser, older app). */
export function desktopHub(desktop = globalThis.window?.__ARK_DESKTOP__) {
  const hub = desktop && typeof desktop === "object" ? desktop.hub : null;
  return hub && typeof hub.context === "function" && typeof hub.goLocal === "function" ? hub : null;
}

const contexts = new WeakMap();
/** {role: "local" | "remote" | "bundled", ...} of this page, or null. Cached. */
export function hubContext(hub = desktopHub()) {
  if (!hub) return Promise.resolve(null);
  if (!contexts.has(hub)) contexts.set(hub, Promise.resolve().then(() => hub.context()).catch(() => null));
  return contexts.get(hub);
}

/** Forget the cached context (after linking: `linked` changed). */
export function resetHubContext(hub = desktopHub()) {
  if (hub) contexts.delete(hub);
}

/** Saved servers for display: [{url, label, origin, linked, user}], invalid entries dropped. */
export function normalizeServers(payload) {
  const list = Array.isArray(payload?.servers) ? payload.servers : [];
  const seen = new Set();
  return list.filter((s) => s && typeof s.url === "string" && /^https?:\/\//i.test(s.url) && !seen.has(s.url) && seen.add(s.url))
    .map((s) => ({
      url: s.url, label: String(s.label || s.url), origin: String(s.origin || ""),
      linked: s.linked === true, user: typeof s.user === "string" && s.user ? s.user : null,
    }));
}

/** "checking" | "online" | "offline" from a status() reply (undefined: not known yet). */
export function serverState(status) {
  if (!status) return "checking";
  return status.reachable ? "online" : "offline";
}

/**
 * A linked server's project list (hub.serverProjects reply) as one state:
 * kind "projects" (with projects, canCreate, user), "not_linked", "expired"
 * (the link was revoked or ended), "offline", "unsupported" (an older
 * server) or "error".
 */
export function serverProjectsState(reply) {
  if (!reply || typeof reply !== "object") return { kind: "error", projects: [] };
  if (reply.expired) return { kind: "expired", projects: [] };
  if (!reply.linked) return { kind: "not_linked", projects: [] };
  if (reply.reachable === false) return { kind: "offline", projects: [], error: reply.error || "" };
  if (reply.unsupported) return { kind: "unsupported", projects: [] };
  if (reply.error) return { kind: "error", projects: [], error: String(reply.error) };
  const projects = (Array.isArray(reply.projects) ? reply.projects : [])
    .filter((p) => p && typeof p.id === "string" && p.id)
    .map((p) => ({ id: p.id, name: String(p.name || p.id), description: String(p.description || ""),
      permission: String(p.permission || "") }));
  return { kind: "projects", projects, canCreate: reply.canCreate === true, user: reply.user || null };
}

/** The new-project "Location" options: this computer, then each saved server. */
export function locationOptions(servers, localLabel) {
  return [{ value: "local", label: localLabel },
    ...(servers || []).map((s) => ({ value: s.url, label: s.label }))];
}

/** A remote page may offer "Link this desktop": a saved, unlinked server of an app that links. */
export function canLinkHere(context) {
  return Boolean(context && context.role === "remote" && context.server && context.link && context.linked === false);
}

/**
 * ?ark_open_project=<id> / ?ark_new_project=1 / ?ark_link_desktop=1 in
 * `search`.  Returns {openProject, newProject, linkDesktop, search} with
 * `search` the query without them (for history.replaceState), or null when
 * none is present.
 */
export function desktopQuery(search) {
  const params = new URLSearchParams(search || "");
  if (!PARAMS.some((p) => params.has(p))) return null;
  const flag = (name) => ["1", "true", "yes"].includes((params.get(name) || "").toLowerCase());
  const id = (params.get(OPEN_PROJECT_PARAM) || "").trim().slice(0, 200);
  const out = { openProject: id || null, newProject: flag(NEW_PROJECT_PARAM), linkDesktop: flag(LINK_DESKTOP_PARAM) };
  for (const p of PARAMS) params.delete(p);
  const rest = params.toString();
  return { ...out, search: rest ? `?${rest}` : "" };
}

/**
 * After boot: open the project, the new-project dialog or the "Link this
 * desktop" prompt the URL asks for, and drop the parameters from the address.
 * `ctx`: currentProjectId(), openProjectById(id), openNewProject(),
 * linkDesktop(), toast().
 */
export async function applyDesktopQuery(ctx, { location = globalThis.location, history = globalThis.history } = {}) {
  const q = desktopQuery(location?.search);
  if (!q) return null;
  try { history?.replaceState?.(history.state, "", `${location.pathname}${q.search}${location.hash || ""}`); } catch { /* ignore */ }
  if (q.openProject && q.openProject !== ctx.currentProjectId?.()) {
    try { await ctx.openProjectById(q.openProject); } catch (err) { ctx.toast?.(err.message, "err"); }
  }
  if (q.newProject) ctx.openNewProject?.();
  if (q.linkDesktop) ctx.linkDesktop?.();
  return q;
}

/** How long "Link this desktop" may take in all (code request and claim). */
export const LINK_TIMEOUT_MS = 15000;

/**
 * "Link this desktop" on a server page: a one-time code from this server
 * (`requestCode({device, signal})`, POST /api/desktop/link) handed to the
 * app, which claims it and keeps the device token.  Returns the app's reply
 * ({linked, user}).
 *
 * Never hangs: after `timeoutMs` it rejects with "desktop.link.timeout" and
 * aborts the code request; `signal` (Cancel) rejects with an AbortError.
 * The app's claim cannot be aborted from the page; a late reply is ignored.
 */
export function linkThisDesktop(hub, requestCode, {
  device = "", signal = null, timeoutMs = LINK_TIMEOUT_MS,
  setTimer = (fn, ms) => setTimeout(fn, ms), clearTimer = (id) => clearTimeout(id),
} = {}) {
  if (!hub || typeof hub.linkServer !== "function") return Promise.reject(new Error("desktop.link.unavailable"));
  const abortError = () => Object.assign(new Error("desktop.link.cancelled"), { name: "AbortError" });
  if (signal?.aborted) return Promise.reject(abortError());
  const controller = typeof AbortController === "function" ? new AbortController() : null;
  return new Promise((resolve, reject) => {
    let done = false;
    const finish = (fn, value) => {
      if (done) return;
      done = true;
      clearTimer(timer);
      signal?.removeEventListener?.("abort", onAbort);
      fn(value);
    };
    const onAbort = () => { controller?.abort(); finish(reject, abortError()); };
    const timer = setTimer(() => {
      controller?.abort();
      finish(reject, new Error("desktop.link.timeout"));
    }, timeoutMs);
    signal?.addEventListener?.("abort", onAbort, { once: true });
    (async () => {
      const data = await requestCode({ device, signal: controller?.signal });
      if (done) return undefined;
      if (!data || typeof data.code !== "string" || !data.code) throw new Error("desktop.link.no_code");
      const result = await hub.linkServer(data.code);
      resetHubContext(hub);
      return result;
    })().then((result) => finish(resolve, result), (err) => finish(reject, err));
  });
}
