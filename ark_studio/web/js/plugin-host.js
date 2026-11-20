// The view plugin host: registers plugins with the workbench, runs their
// lifecycle (mount / update / unmount) and, for user plugins, the sandboxed
// iframe and its postMessage bridge.
//
//   registerViewPlugin(manifest, impl, options)  first-party (built-in) views
//   loadUserPlugins(ctx)                         the user's enabled plugins
//
// Built-ins get the trusted host `api` ({ ctx }); user plugins never run in
// this page: their adapter mounts <iframe sandbox="allow-scripts"
// src="plugin-frame.html"> and answers only the calls gateRequest allows
// (view-plugins.js; docs/development/plugin-api.md).

import { h } from "./dom.js";
import { t, getLang } from "./i18n.js";
import { api, pluginRequest, authenticatedUrl } from "./api.js";
import { toast, openModal } from "./components.js";
import { icon } from "./icons.js";
import { registerView, unregisterView, setViewCatalog, isViewDisplayed, WORKBENCH_CHANGED } from "./workbench.js";
import {
  createRegistry, BUILTIN_MANIFESTS, pluginTitle, pluginViewId, gateRequest, pluginState, publicNode, publicGraph,
} from "./view-plugins.js";
import {
  createWriteGrants, authorizeWrite, applyWriteOps, summarizeWriteOps, createEventHub, createPluginStorage,
  createRateLimits, publicNodeDetail, publicTheme, MAX_MEDIA_BYTES,
} from "./plugin-api.js";
import { listPlugins, userPluginsEnabled, pluginDataBackend } from "./plugin-store.js";

export const registry = createRegistry();
export const PLUGINS_CHANGED = "ark:plugins-changed";
let hostCtx = null;

function hostState(record) {
  const S = hostCtx?.S || {};
  const selection = S.detail && (S.drawerOpen || S.detail.id === S.selectedId) ? S.detail
    : (S.units || []).find((unit) => unit.id === S.selectedId) || null;
  return {
    visible: isViewDisplayed(record.viewId),
    theme: document.documentElement.dataset.theme === "dark" ? "dark" : "light",
    lang: getLang(),
    location: record.manifest.location,
    tab: S.tab || "",
    selection,
  };
}

function safely(fn) {
  try { return fn(); } catch (err) { console.error("[ark plugin]", err); return undefined; }
}

function mount(record) {
  if (record.mounted || !record.el) return;
  record.mounted = true;
  safely(() => record.impl.mount?.(record.el, record.api));
}

// `reason`: "show" (the view just became visible), "selection", "theme",
// "layout".  Built-ins keep their former onShow behaviour on "show".
function update(record, reason) {
  if (!record.mounted) return;
  safely(() => record.impl.update?.({ ...hostState(record), reason }));
}

function shown(viewId) {
  const record = registry.get(viewId);
  if (!record) return;
  mount(record);
  update(record, "show");
}

// Register a plugin with the registry and the workbench.  `options`: the
// workbench extras of first-party views (applies, badge, tooltip, actions,
// menu).  Built-ins mount at once; user plugins when first shown.
export function registerViewPlugin(manifest, impl = {}, options = {}) {
  const record = registry.register(manifest, impl);
  record.api = manifest.builtin ? { ctx: hostCtx } : null;
  record.el = registerView({
    ...options,
    id: record.viewId,
    icon: manifest.icon,
    ...(manifest.builtin ? {} : { title: () => pluginTitle(manifest, getLang()), tooltip: () => manifest.name }),
    onShow: () => shown(record.viewId),
  });
  if (manifest.builtin) mount(record);
  setViewCatalog(registry.viewDefaults());
  return record.el;
}

export function unregisterViewPlugin(viewId, { forget = false } = {}) {
  const record = registry.unregister(viewId);
  if (!record) return;
  if (record.mounted) safely(() => record.impl.unmount?.());
  unregisterView(viewId, { forget });
  setViewCatalog(registry.viewDefaults());
}

// First-party view: the built-in manifest for `id` plus its implementation.
export function registerBuiltinView(id, impl = {}, options = {}) {
  const manifest = BUILTIN_MANIFESTS.find((item) => item.id === id);
  if (!manifest) throw new Error(`unknown built-in view ${id}`);
  return registerViewPlugin(manifest, impl, options);
}

// Host events that plugins hear through update().
let wired = false;
function wireEvents() {
  if (wired) return;
  wired = true;
  const all = (reason) => { for (const record of registry.list()) if (!record.manifest.builtin) update(record, reason); };
  window.addEventListener("ark:selection", () => { all("selection"); pluginEvents.emit("selection"); });
  window.addEventListener("ark:theme-applied", () => all("theme"));
  window.addEventListener(WORKBENCH_CHANGED, () => all("layout"));
  // Live sync (app.js): touched unit ids, or null when anything may have changed.
  window.addEventListener("ark:units-touched", (event) => pluginEvents.emit("graph", { uids: touchedUids(event.detail?.ids) }));
  window.addEventListener("ark:project-switched", (event) => {
    writeGrants.projectChanged(event.detail?.id || "");
    pluginEvents.emit("project");
  });
}

function touchedUids(ids) {
  if (!Array.isArray(ids)) return null;
  const byId = new Map((hostCtx?.S?.units || []).map((unit) => [unit.id, unit.uid]));
  const uids = ids.map((id) => byId.get(id));
  return uids.every(Boolean) ? uids : null;
}

export function initPluginHost(ctx) {
  hostCtx = ctx;
  wireEvents();
  for (const record of registry.list()) if (record.api) record.api.ctx = ctx;
}

// ------------------------------------------------------------ user plugins
const userViews = new Set();

function currentUser() {
  return String(hostCtx?.S?.session?.user || "");
}

export async function loadUserPlugins() {
  for (const viewId of [...userViews]) unregisterViewPlugin(viewId);
  userViews.clear();
  if (!userPluginsEnabled()) { window.dispatchEvent(new CustomEvent(PLUGINS_CHANGED)); return []; }
  let records = [];
  try { records = await listPlugins(currentUser()); } catch { records = []; }
  for (const item of records) {
    if (!item.enabled) continue;
    try {
      registerViewPlugin(item.manifest, sandboxAdapter(item));
      userViews.add(pluginViewId(item.manifest));
    } catch (err) {
      console.error("[ark plugin]", item.manifest?.id, err);
    }
  }
  window.dispatchEvent(new CustomEvent(PLUGINS_CHANGED));
  return records;
}

// ----------------------------------------------------- the sandbox adapter
const FRAME_PATH = "plugin-frame.html";

function randomChannel() {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// node:write session grants (until reload or project switch; revocable from
// the bar above the plugin's view), the plugins' key/value data, and event
// subscriptions per plugin frame (keyed by view id).
export const writeGrants = createWriteGrants();
const pluginStorage = createPluginStorage(pluginDataBackend);
const frames = new Map();
const pluginEvents = createEventHub({ deliver: (key, event, data) => frames.get(key)?.event(event, data) });

// Removing a plugin drops what it stored (every project).
export function forgetPluginData(user, pluginId) {
  return pluginStorage.forget(String(user || ""), pluginId);
}

const projectId = () => String(hostCtx?.S?.project?.id || "");
const fail = (code, reason = "") => Object.assign(new Error(code), { code, ...(reason ? { reason } : {}) });

// Errors the frame may see: a fixed code and, for server refusals, the
// server's short message (never headers, tokens or stack traces).
const REPLY_CODES = ["declined", "busy", "quota_exceeded", "not_found", "forbidden", "not_configured", "rate_limited", "too_large", "bad_params", "failed"];
function replyError(err) {
  let code = REPLY_CODES.includes(err?.code) ? err.code : "failed";
  if (err?.status === 404) code = "not_found";
  else if (err?.status === 403 || err?.status === 401) code = "forbidden";
  else if (err?.status === 429) code = "rate_limited";
  else if (err?.code === "AI_NOT_CONFIGURED") code = "not_configured";
  const message = String(err?.reason || (err?.status ? err.serverMessage || err.message : "") || "").slice(0, 300);
  return { code, message };
}

// A confirmation must not be clicked by accident when it appears under the
// pointer: its buttons (other than Cancel) wait a moment.
function armDialog() {
  const foot = document.querySelector("#overlay-root .modal-backdrop:last-child .modal-foot");
  const buttons = [...(foot?.querySelectorAll("button:not(.ghost)") || [])];
  for (const button of buttons) button.disabled = true;
  setTimeout(() => { for (const button of buttons) button.disabled = false; }, 600);
}

// A plugin never replaces a dialog that is already open (the user's own
// work, or the plugin's previous question): the call is refused as "busy".
function askDialog({ title, body, actions }) {
  if (document.querySelector("#overlay-root .modal-backdrop")) return Promise.reject(fail("busy"));
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => { if (!done) { done = true; resolve(value); } };
    openModal({
      title, body, onClose: () => finish(null),
      actions: [{ label: t("action.cancel"), ghost: true, onClick: () => finish(null) },
        ...actions.map((action) => ({ label: action.label, primary: action.primary, onClick: () => finish(action.value) }))],
    });
    armDialog();
  });
}

const askedBy = (plugin) => h("p", { class: "plugin-ask-by" }, icon("puzzle", "sm"),
  h("span", null, t("plugins.ask_by", { name: plugin.name, version: plugin.version })));

function askComment(plugin, params) {
  return askDialog({
    title: t("plugins.comment_confirm_title"),
    body: h("div", { class: "plugin-ask" }, askedBy(plugin),
      h("p", null, t("plugins.comment_confirm_to", { uid: params.uid })),
      h("pre", { class: "plugin-ask-text" }, params.body.slice(0, 1200))),
    actions: [{ label: t("plugins.comment_post"), primary: true, value: true }],
  });
}

function askWrite(plugin, ops) {
  const summary = summarizeWriteOps(ops);
  const lines = summary.lines.slice(0, 30);
  return askDialog({
    title: t("plugins.write.title", { name: plugin.name }),
    body: h("div", { class: "plugin-ask" }, askedBy(plugin),
      h("p", { class: "plugin-ask-headline" }, summary.headline.map((item) => t(item.key, item.vars)).join(" · ")),
      h("ul", { class: "plugin-ask-lines" }, ...lines.map((item) => h("li", null, t(item.key, item.vars))),
        summary.lines.length > lines.length ? h("li", { class: "muted" }, t("plugins.write.more", { n: summary.lines.length - lines.length })) : null),
      h("div", { class: "settings-note" }, icon("shield", "sm"), h("div", null, t("plugins.write.note")))),
    actions: [
      { label: t("plugins.write.allow_session"), value: "session" },
      { label: t("plugins.write.allow_once"), primary: true, value: "once" },
    ],
  });
}

function askLink(plugin, params) {
  return askDialog({
    title: t("plugins.link.title"),
    body: h("div", { class: "plugin-ask" }, askedBy(plugin),
      h("p", null, t("plugins.link.lead")),
      h("p", { class: "plugin-ask-host" }, params.host),
      h("pre", { class: "plugin-ask-text" }, params.url)),
    actions: [{ label: t("plugins.link.open"), primary: true, value: true }],
  });
}

async function copyText(value) {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch { /* the frame has the focus: fall back to a hidden textarea */ }
  const active = document.activeElement;
  const area = h("textarea", { readonly: true, style: { position: "fixed", left: "-9999px", top: "0", opacity: "0" } });
  area.value = value;
  document.body.appendChild(area);
  area.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  area.remove();
  active?.focus?.();
  if (!ok) throw fail("failed", "clipboard");
  return true;
}

// The selected node with body, media and links (the inspector's detail has
// them; a list row is completed from GET /api/units/{uid}).
async function selectedDetail(record) {
  const unit = hostState(record).selection;
  if (!unit?.uid) return null;
  if (unit.body !== undefined && Array.isArray(unit.links_in) && Array.isArray(unit.media)) return unit;
  try { const data = await api.unit(unit.uid); return data.unit || data; } catch { return unit; }
}

// The host side of the bridge: what each allowed method does.
// handler(record, params, plugin): plugin = { id, name, version, granted, viewId, storage() }.
const HANDLERS = {
  "selection.get": async (record) => publicNodeDetail(await selectedDetail(record), publicNode),
  "media.get": async (record, params, plugin) => {
    // Without graph:read only the selected node's media.
    if (!plugin.granted.includes("graph:read")) {
      const unit = await selectedDetail(record);
      if (!(unit?.media || []).some((item) => Number(item.id) === params.id)) throw fail("not_found");
    }
    const response = await fetch(authenticatedUrl(`/api/media/${params.id}/raw`), { credentials: "same-origin" });
    if (!response.ok) throw fail(response.status === 404 ? "not_found" : "failed");
    if (Number(response.headers.get("Content-Length") || 0) > MAX_MEDIA_BYTES) throw fail("too_large");
    const blob = await response.blob();
    if (blob.size > MAX_MEDIA_BYTES) throw fail("too_large");
    // A Blob is copied into the frame; the SDK makes a blob: URL in the
    // frame's own (opaque) origin.  No ARK URL or cookie is handed over.
    return { blob, type: blob.type, size: blob.size };
  },
  "graph.snapshot": () => api.graphSnapshot().then((data) => publicGraph(data)),
  "node.get": async (record, params) => {
    const data = await api.unit(params.uid || params.id);
    return publicNodeDetail(data.unit || data, publicNode);
  },
  "nodes.search": async (record, params) => {
    const data = await api.searchNodes({ q: params.q, limit: params.limit, ...(params.type ? { types: params.type } : {}) });
    return {
      query: params.q,
      nodes: (data?.nodes || []).slice(0, params.limit).map((node) => ({
        ...publicNode(node), match: String(node.matches?.[0]?.text || "").slice(0, 300),
      })),
    };
  },
  "topics.list": async () => {
    const data = await api.topics();
    return { topics: (data?.items || []).map((topic) => publicNode(topic)) };
  },
  "topic.members": async (record, params) => {
    const data = await api.topic(params.uid);
    return { topic: publicNode(data?.topic), members: (data?.members || []).slice(0, 5000).map((unit) => publicNode(unit)) };
  },
  "events.subscribe": (record, params, plugin) => ({ events: pluginEvents.subscribe(plugin.viewId, params.events) }),
  "events.unsubscribe": (record, params, plugin) => ({ events: pluginEvents.unsubscribe(plugin.viewId, params.events) }),
  "project.get": () => {
    const S = hostCtx?.S || {};
    const css = getComputedStyle(document.documentElement);
    const mode = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
    return {
      project: { id: projectId(), name: String(S.project?.name || "") },
      user: { name: String(S.session?.user || ""), role: String(S.session?.role || ""), permission: String(S.session?.project_permission || "") },
      theme: publicTheme(mode, (name) => css.getPropertyValue(name)),
      lang: getLang(),
    };
  },
  "storage.get": (record, params, plugin) => plugin.storage().get(params.key),
  "storage.set": (record, params, plugin) => plugin.storage().set(params.key, params.json),
  "storage.remove": (record, params, plugin) => plugin.storage().remove(params.key),
  "storage.keys": (record, params, plugin) => plugin.storage().keys(),
  "storage.clear": (record, params, plugin) => plugin.storage().clear(),
  "node.open": (record, params) => {
    if (params.target === "map") hostCtx?.showOnMap?.(params.uid);
    else if (params.target === "editor") hostCtx?.openUnitInEditor?.({ uid: params.uid });
    else hostCtx?.openUnit?.(params.uid);
    return true;
  },
  notify: (record, params, plugin) => {
    toast(t("plugins.notify", { name: plugin.name, message: params.message }), params.kind === "info" ? "" : params.kind);
    return true;
  },
  "clipboard.write": (record, params) => copyText(params.text),
  "comment.create": async (record, params, plugin) => {
    if (!(await askComment(plugin, params))) throw fail("declined");
    const body = `${params.body}\n\n${t("plugins.comment_via", { name: plugin.name })}`;
    const data = await pluginRequest(plugin.id, `/api/units/${encodeURIComponent(params.uid)}/comments`, { method: "POST", body: { body, kind: "comment" } });
    return { id: data?.comment?.id ?? data?.id ?? null };
  },
  "nodes.write": async (record, params, plugin) => {
    const grant = await authorizeWrite({ grants: writeGrants, pluginId: plugin.id, project: projectId(), ask: () => askWrite(plugin, params.ops) });
    const result = await applyWriteOps(params.ops, (kind, payload) => {
      if (kind === "create") return pluginRequest(plugin.id, "/api/units", { method: "POST", body: payload });
      if (kind === "update") return pluginRequest(plugin.id, `/api/units/${encodeURIComponent(payload.uid)}`, { method: "PATCH", body: payload.fields });
      if (kind === "link") return pluginRequest(plugin.id, "/api/links", { method: "POST", body: payload });
      return pluginRequest(plugin.id, `/api/links/${encodeURIComponent(payload.id)}`, { method: "DELETE" });
    });
    const applied = result.results.filter((item) => item.ok).length;
    if (applied) {
      toast(t(result.ok ? "plugins.write.done" : "plugins.write.partial", { name: plugin.name, n: applied }), result.ok ? "ok" : "err");
      hostCtx?.refresh?.();
    }
    return { ...result, grant };
  },
  "link.open": async (record, params, plugin) => {
    if (!(await askLink(plugin, params))) throw fail("declined");
    window.open(params.url, "_blank", "noopener,noreferrer");
    return true;
  },
  "ai.chat": async (record, params, plugin) => {
    const data = await pluginRequest(plugin.id, "/api/plugins/ai/chat", { method: "POST", body: { plugin: plugin.id, ...params } });
    return { message: String(data?.message || ""), proposals: data?.proposals || [], reads: data?.reads || [], model: String(data?.model || "") };
  },
};

// The bar above a plugin's view while it may write without asking (drawn by
// ARK, outside the frame, so the plugin cannot hide or fake it).
function grantBar(plugin) {
  const bar = h("div", { class: "plugin-grant-bar", hidden: true, role: "status" },
    icon("pen", "sm"), h("span", null, t("plugins.write.session_on")),
    h("button", { class: "ghost-btn sm", type: "button", onclick: () => {
      writeGrants.revoke(plugin.id);
      toast(t("plugins.write.revoked", { name: plugin.name }), "ok");
    } }, t("plugins.write.revoke")));
  const sync = () => { bar.hidden = !writeGrants.has(plugin.id, projectId()); };
  return { bar, sync };
}

function sandboxAdapter(item) {
  const granted = (item.granted || []).filter((permission) => item.manifest.permissions.includes(permission));
  const viewId = pluginViewId(item.manifest);
  const plugin = {
    id: item.manifest.id, name: item.manifest.name, version: item.manifest.version, granted, viewId,
    storage: () => pluginStorage.scope(currentUser(), item.manifest.id, projectId()),
  };
  let frame = null;
  let bar = null;
  let channel = "";
  let loaded = false;
  let listener = null;
  let stopGrants = null;
  const allow = createRateLimits();
  const record = () => registry.get(viewId);
  const send = (message) => frame?.contentWindow?.postMessage({ ...message, channel }, "*");
  // Event payloads carry only what the plugin's read permissions allow.
  const eventData = (event, data) => {
    if (event === "graph") return granted.includes("graph:read") ? data : {};
    if (event === "selection") {
      const unit = hostState(record()).selection;
      return granted.includes("selection:read") ? { uid: unit?.uid || null } : {};
    }
    return granted.includes("project:read") ? { id: projectId(), name: String(hostCtx?.S?.project?.name || "") } : {};
  };
  return {
    mount(container) {
      const grant = grantBar(plugin);
      bar = grant.bar;
      stopGrants = writeGrants.onChange((id) => { if (id === plugin.id) grant.sync(); });
      grant.sync();
      frame = h("iframe", {
        class: "plugin-frame", title: item.manifest.name, src: FRAME_PATH,
        sandbox: "allow-scripts", referrerpolicy: "no-referrer",
      });
      frames.set(viewId, {
        event(event, data) {
          if (loaded) send({ type: "ark-plugin-event", event, data: eventData(event, data) });
        },
      });
      listener = async (event) => {
        if (!frame || event.source !== frame.contentWindow) return;
        const data = event.data;
        if (data?.type === "ark-plugin-frame") {
          // A fresh frame document (first load, or the view moved to another
          // bar: re-parenting an iframe reloads it).  Each load gets a new
          // channel id; replies to the old document are dropped, and so are
          // its event subscriptions.
          channel = randomChannel();
          loaded = true;
          pluginEvents.drop(viewId);
          frame.contentWindow.postMessage({
            type: "ark-plugin-load", channel, html: item.html, granted,
            manifest: { id: item.manifest.id, name: item.manifest.name, version: item.manifest.version },
            state: pluginState(hostState(record()), granted),
          }, "*");
          return;
        }
        if (data?.channel !== channel) return;
        if (data.type === "ark-plugin-ready") { send({ type: "ark-plugin-update", state: pluginState(hostState(record()), granted) }); return; }
        if (data.type !== "ark-plugin-call") return;
        let size = 0;
        try { size = JSON.stringify(data).length; } catch { size = Infinity; }
        const gate = allow("calls") ? gateRequest(data, { granted, channel, size }) : { ok: false, id: Number.isInteger(data.id) ? data.id : null, code: "rate_limited" };
        // The stricter windows of writes, AI, clipboard, links, … (plugin-api.js RATE_LIMITS).
        if (gate.ok && gate.bucket && !allow(gate.bucket)) Object.assign(gate, { ok: false, code: "rate_limited" });
        if (!gate.ok) { if (gate.id !== null) send({ type: "ark-plugin-reply", id: gate.id, ok: false, error: gate.code }); return; }
        try {
          const result = await HANDLERS[gate.method](record(), gate.params, plugin);
          send({ type: "ark-plugin-reply", id: gate.id, ok: true, result: result ?? null });
        } catch (err) {
          const { code, message } = replyError(err);
          send({ type: "ark-plugin-reply", id: gate.id, ok: false, error: code, message });
        }
      };
      window.addEventListener("message", listener);
      container.classList.add("plugin-view");
      container.append(bar, frame);
    },
    update(state) {
      if (!frame || !loaded) return;
      send({ type: "ark-plugin-update", state: pluginState(state, granted) });
    },
    unmount() {
      send({ type: "ark-plugin-unmount" });
      if (listener) window.removeEventListener("message", listener);
      pluginEvents.drop(viewId);
      frames.delete(viewId);
      stopGrants?.();
      bar?.remove();
      frame?.remove();
      frame = null;
      bar = null;
      listener = null;
    },
  };
}
