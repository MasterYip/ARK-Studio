// Settings > Personal > Plugins: the built-in views (first-party plugins)
// and the user's own view plugins: opt in, import (zip / html / js / URL),
// confirm permissions, enable/disable, remove.  User plugins are stored in
// this browser for the signed-in user (plugin-store.js) and run sandboxed
// (plugin-host.js, docs/guide/plugins.md).

import { h, clear } from "./dom.js";
import { t, getLang } from "./i18n.js";
import { icon } from "./icons.js";
import { toast, openModal, confirmDialog } from "./components.js";
import { BUILTIN_MANIFESTS, PLUGIN_PERMISSIONS, pluginTitle, pluginViewId, confirmMode } from "./view-plugins.js";
import { BundleError, parseTextBundle, parseZipBundle, browserInflate } from "./plugin-bundle.js";
import { listPlugins, savePlugin, removePlugin, userPluginsEnabled, setUserPluginsEnabled, persistent } from "./plugin-store.js";
import { loadUserPlugins, unregisterViewPlugin, forgetPluginData, writeGrants, PLUGINS_CHANGED } from "./plugin-host.js";

export const EXAMPLE_PLUGIN_URL = "plugins-examples/node-word-count.html";
// The examples the Plugins page can add in one click.
export const EXAMPLE_PLUGINS = [
  { url: EXAMPLE_PLUGIN_URL, label: "plugins.add_example", icon: "puzzle" },
  { url: "plugins-examples/assistant-plugin.html", label: "plugins.add_assistant_example", icon: "sparkles" },
];

const locationLabel = (name) => t(`workbench.container.${name}`);

function bundleError(err) {
  if (err instanceof BundleError) return t(err.key, { field: err.params?.field || "", entry: err.params?.entry || "" });
  return err?.message || String(err);
}

// Each permission in plain words; the ones that still ask (every time, or
// before the first write) are marked.
function permissionList(permissions) {
  if (!permissions.length) return h("p", { class: "muted" }, t("plugins.no_permissions"));
  const ordered = PLUGIN_PERMISSIONS.filter((permission) => permissions.includes(permission));
  return h("ul", { class: "plugin-permissions" },
    ...ordered.map((permission) => {
      const mode = confirmMode(permission);
      return h("li", { dataset: { permission } },
        h("b", null, t(`plugins.perm.${permission}`)),
        mode === "none" ? null : h("span", { class: `pill plugin-confirm ${mode}` }, icon("shield", "sm"), t(`plugins.confirm.${mode}`)),
        h("div", { class: "muted" }, t(`plugins.perm.${permission}.hint`)));
    }));
}

// Ask the user to install `bundle` with the permissions it declares.
function confirmInstall(bundle, existing) {
  const { manifest } = bundle;
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => { if (!done) { done = true; resolve(value); } };
    openModal({
      title: t(existing ? "plugins.update_title" : "plugins.install_title", { name: manifest.name }),
      body: h("div", { class: "plugin-install" },
        h("div", { class: "plugin-install-head" }, icon(manifest.icon, "sm"),
          h("b", null, pluginTitle(manifest, getLang())),
          h("span", { class: "pill" }, `v${manifest.version}`),
          h("code", null, manifest.id)),
        manifest.description ? h("p", null, manifest.description) : null,
        existing ? h("p", { class: "muted" }, t("plugins.update_from", { version: existing.manifest.version })) : null,
        h("p", null, t("plugins.install_location", { place: locationLabel(manifest.location) })),
        h("h4", null, t("plugins.install_permissions")),
        permissionList(manifest.permissions),
        h("div", { class: "settings-note" }, icon("shield", "sm"), h("div", null, t("plugins.sandbox_note")))),
      onClose: () => finish(false),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => finish(false) },
        { label: t(existing ? "plugins.update" : "plugins.install"), primary: true, onClick: () => finish(true) },
      ],
    });
  });
}

async function readSource(source) {
  if (source.file) {
    const file = source.file;
    if (/\.zip$/i.test(file.name)) return { kind: "zip", bytes: new Uint8Array(await file.arrayBuffer()), name: file.name };
    return { kind: "text", text: await file.text(), name: file.name };
  }
  // URLs: only what this page may fetch (the ARK server itself; the page's
  // CSP blocks other origins).  The response is data, never run here.
  let url;
  try { url = new URL(source.url, location.href); } catch { throw new BundleError("plugins.err.url"); }
  if (url.origin !== location.origin) throw new BundleError("plugins.err.url_origin");
  const response = await fetch(url.href, { credentials: "same-origin" });
  if (!response.ok) throw new BundleError("plugins.err.url_fetch", { field: String(response.status) });
  if (/\.zip$/i.test(url.pathname)) return { kind: "zip", bytes: new Uint8Array(await response.arrayBuffer()), name: url.pathname };
  return { kind: "text", text: await response.text(), name: url.pathname };
}

async function importPlugin(ctx, source, onDone) {
  const user = String(ctx.S.session?.user || "");
  try {
    const installed = await listPlugins(user);
    const raw = await readSource(source);
    const bundle = raw.kind === "zip" ? await parseZipBundle(raw.bytes, browserInflate) : parseTextBundle(raw.text, raw.name);
    const existing = installed.find((item) => item.id === bundle.manifest.id) || null;
    if (!(await confirmInstall(bundle, existing))) return;
    await savePlugin(user, {
      id: bundle.manifest.id, manifest: bundle.manifest, html: bundle.html, enabled: true,
      granted: bundle.manifest.permissions.slice(), source: source.file ? source.file.name : source.url,
      installedAt: existing?.installedAt || Date.now(), updatedAt: Date.now(),
    });
    await loadUserPlugins();
    toast(t("plugins.installed", { name: bundle.manifest.name }), "ok");
    onDone?.();
  } catch (err) {
    toast(t("plugins.import_failed", { error: bundleError(err) }), "err");
  }
}

function askUrl() {
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => { if (!done) { done = true; resolve(value); } };
    const input = h("input", { class: "input", type: "url", placeholder: EXAMPLE_PLUGIN_URL, "aria-label": t("plugins.url") });
    openModal({
      title: t("plugins.import_url"),
      body: h("div", { class: "stack" },
        h("label", { class: "field" }, h("label", null, t("plugins.url")), input),
        h("p", { class: "muted" }, t("plugins.url_hint"))),
      enterConfirm: true,
      onClose: () => finish(null),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => finish(null) },
        { label: t("plugins.import"), primary: true, onClick: () => {
          const value = input.value.trim();
          if (!value) { input.focus(); return false; }
          finish(value);
          return true;
        } },
      ],
    });
  });
}

export function pluginsPanel(ctx) {
  const user = String(ctx.S.session?.user || "");
  const builtins = h("div", { class: "plugin-list" },
    ...BUILTIN_MANIFESTS.map((manifest) => h("div", { class: "plugin-item builtin" },
      icon(manifest.icon, "sm"),
      h("div", { class: "plugin-main" }, h("b", null, pluginTitle(manifest, getLang(), t)),
        h("span", { class: "muted" }, ` · ${locationLabel(manifest.location)}`)),
      h("span", { class: "pill" }, t("plugins.builtin")))));
  const mine = h("div", { class: "plugin-user" });
  const optIn = h("input", { type: "checkbox", checked: userPluginsEnabled() });
  const file = h("input", { type: "file", accept: ".zip,.html,.htm,.js", hidden: true });
  file.addEventListener("change", () => {
    const picked = file.files?.[0];
    file.value = "";
    if (picked) importPlugin(ctx, { file: picked }, render);
  });

  optIn.addEventListener("change", async () => {
    if (optIn.checked) {
      const ok = await confirmDialog(t("plugins.opt_in_confirm"), { title: t("plugins.opt_in"), confirmLabel: t("plugins.opt_in_yes") });
      if (!ok) { optIn.checked = false; return; }
    }
    setUserPluginsEnabled(optIn.checked);
    await loadUserPlugins();
    render();
  });

  async function setEnabled(item, enabled) {
    await savePlugin(user, { ...item, enabled });
    await loadUserPlugins();
    render();
  }

  async function remove(item) {
    const ok = await confirmDialog(t("plugins.remove_confirm", { name: item.manifest.name }), { danger: true, confirmLabel: t("plugins.remove") });
    if (!ok) return;
    await removePlugin(user, item.id);
    writeGrants.revoke(item.id);
    try { await forgetPluginData(user, item.id); } catch { /* its data stays until the browser clears it */ }
    unregisterViewPlugin(pluginViewId(item.manifest), { forget: true });
    await loadUserPlugins();
    render();
  }

  // Renders can overlap (a change event during a load): only the latest
  // one fills the list.
  let renderSeq = 0;
  async function render() {
    const seq = ++renderSeq;
    const out = [];
    const show = () => { if (seq === renderSeq) { clear(mine); mine.append(...out); } };
    if (!userPluginsEnabled()) {
      out.push(h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("plugins.off_note"))));
      show();
      return;
    }
    out.push(h("div", { class: "plugin-actions" },
      h("button", { class: "primary-btn", type: "button", onclick: () => file.click() }, icon("upload", "sm"), t("plugins.import_file")),
      h("button", { class: "ghost-btn", type: "button", onclick: async () => {
        const url = await askUrl();
        if (url) importPlugin(ctx, { url }, render);
      } }, icon("link", "sm"), t("plugins.import_url")),
      ...EXAMPLE_PLUGINS.map((example) => h("button", { class: "ghost-btn", type: "button", dataset: { example: example.url },
        onclick: () => importPlugin(ctx, { url: example.url }, render) }, icon(example.icon, "sm"), t(example.label))),
      file));
    let items = [];
    try { items = await listPlugins(user); } catch (err) { toast(err.message, "err"); }
    if (!items.length) out.push(h("div", { class: "empty sm" }, t("plugins.empty")));
    const list = h("div", { class: "plugin-list" });
    for (const item of items) {
      const toggle = h("input", { type: "checkbox", checked: item.enabled, "aria-label": t("plugins.enabled") });
      toggle.addEventListener("change", () => setEnabled(item, toggle.checked));
      list.appendChild(h("div", { class: `plugin-item ${item.enabled ? "" : "off"}`.trim(), dataset: { pluginId: item.id } },
        icon(item.manifest.icon, "sm"),
        h("div", { class: "plugin-main" },
          h("div", null, h("b", null, pluginTitle(item.manifest, getLang())),
            h("span", { class: "muted" }, ` · ${item.manifest.name} v${item.manifest.version} · ${locationLabel(item.manifest.location)}`)),
          h("div", { class: "plugin-perms" },
            ...(item.granted.length ? item.granted : ["none"]).map((permission) => h("span", {
              class: `pill ${confirmMode(permission) === "none" ? "" : "plugin-confirm"}`.trim(),
              title: PLUGIN_PERMISSIONS.includes(permission) ? t(`plugins.perm.${permission}.hint`) : "",
            }, PLUGIN_PERMISSIONS.includes(permission) ? t(`plugins.perm.${permission}`) : t("plugins.no_permissions"))))),
        h("label", { class: "plugin-toggle" }, toggle, h("span", null, t("plugins.enabled"))),
        h("button", { class: "ghost-btn danger sm", type: "button", onclick: () => remove(item) }, icon("trash", "sm"), t("plugins.remove"))));
    }
    if (items.length) out.push(list);
    if (!(await persistent())) out.push(h("div", { class: "notice" }, icon("alert"), h("div", null, t("plugins.memory_only"))));
    show();
  }

  const onPluginsChanged = () => {
    if (!mine.isConnected) { window.removeEventListener(PLUGINS_CHANGED, onPluginsChanged); return; }
    render();
  };
  window.addEventListener(PLUGINS_CHANGED, onPluginsChanged);
  render();
  return h("section", { class: "panel settings-panel plugins-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("plugins.title"))),
    h("p", { class: "settings-lead" }, t("plugins.lead")),
    h("h4", null, t("plugins.builtin_title")),
    builtins,
    h("h4", null, t("plugins.mine_title")),
    h("label", { class: "plugin-opt-in" }, optIn, h("span", null, t("plugins.opt_in"))),
    mine,
    h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("plugins.storage_note"))));
}
