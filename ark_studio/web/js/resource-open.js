// "Open in VS Code / Cursor", "Open SSH terminal", "Copy SSH command" and
// "Copy path" for resource locations and devices (Resources graph context
// menu, Settings > Resources ⋯ menus).  Decisions and strings come from
// resource-open-model.js; this file loads the connections, runs actions and
// shows the "SSH connection" dialog (the viewer's own entry per device).
//
// In the desktop app the terminal opens through window.__ARK_DESKTOP__.openSsh
// and editor links through openEditor (the app checks both); in a browser a
// vscode:// link is followed directly and the SSH command is copied.
import { h, copyText } from "./dom.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { openModal, toast } from "./components.js";
import {
  openActions, effectiveConnection, configNames, sshCommand, validAlias, validHost, validUser, SSH_KINDS,
  cleanConnection, managedAlias, sshConfigSnippet, identityForConfig, configMatchFits, remoteEditorUrl, keySuggestion,
  authMode, keySetupCommands, sshFormPrefill, sshFormBody, gpuDeviceId, machineName, resolveAlias, staleAliases,
} from "./resource-open-model.js";
import { graphHostId } from "./location-size-model.js";
import { desktopDeviceId } from "./location-size.js";

const desk = () => (typeof window !== "undefined" ? window.__ARK_DESKTOP__ : null);
const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);

/** The shell the copied command is for (PowerShell on Windows). */
function shellKind() {
  const platform = desk()?.platform || (typeof navigator !== "undefined" ? `${navigator.userAgentData?.platform || navigator.platform || ""}` : "");
  return /win/i.test(platform) ? "powershell" : "posix";
}

/**
 * Connections of the project's devices (GET /api/resources/connect) plus
 * the desktop app's ~/.ssh/config matches.  refresh(devices) after each
 * graph load; everything else is synchronous for context menus.
 */
export function connectionStore() {
  const state = { devices: {}, hosts: {}, machines: {}, links: {}, mine: {}, suggest: {}, canSave: false,
    canSavePersonal: false, canEditShared: false, matches: new Map(), myDevice: "", localArk: false, loaded: false };
  let pending = null;
  let lastDevices = [];
  async function refresh(devices = null) {
    if (devices) lastDevices = devices;
    devices = lastDevices;
    if (pending) return pending;
    pending = (async () => {
      try {
        const data = await api.resourcesConnect();
        state.devices = data?.devices || {};
        state.hosts = data?.hosts || {};
        state.machines = data?.machines || {};
        state.links = data?.links || {};
        state.mine = data?.mine || {};
        state.suggest = data?.suggest || {};
        state.canSave = data?.can_save === true;
        state.canSavePersonal = data?.can_save_personal ?? state.canSave;
        state.canEditShared = data?.can_edit_shared === true;
      } catch { /* older servers: no connections */ }
      state.myDevice = await desktopDeviceId().catch(() => "");
      try {
        const role = (await desk()?.hub?.context?.())?.role;
        state.localArk = role === "local" || (typeof location !== "undefined" && LOOPBACK.has(location.hostname));
      } catch { state.localArk = typeof location !== "undefined" && LOOPBACK.has(location.hostname); }
      const match = desk()?.sshConfigMatch;
      if (match) {
        const names = new Set();
        for (const device of devices) {
          if (!SSH_KINDS.has(device.kind)) continue;
          for (const n of configNames(device, state.devices[device.id])) names.add(n);
        }
        // The managed ark-<name> alias and the address of every machine.
        for (const m of Object.values(state.machines)) {
          const managed = managedAlias(m?.name);
          if (managed) names.add(managed);
          if (m?.host && validHost(m.host)) names.add(m.host);
        }
        try {
          const found = names.size ? await match([...names].slice(0, 64)) : [];
          state.matches = new Map((found || []).filter((m) => validAlias(m.alias)).map((m) => [String(m.name).toLowerCase(), m.alias]));
        } catch { state.matches = new Map(); }
      }
      repairLocalAliases(devices, state);
      state.loaded = true;
    })().finally(() => { pending = null; });
    return pending;
  }
  return {
    refresh,
    state,
    /** The graph device id of a setup host id (Settings > Resources). */
    deviceOfHost: (hostId) => state.hosts[hostId] || (hostId ? `host:${hostId}` : ""),
    connection: (device) => effectiveConnection(state.devices[device?.id], device, state.matches),
    /** The name of the machine a device is linked to ("" when none): its ark-<name> alias. */
    machineName: (device) => machineName(device, state),
    /** The connection of a GPU server by name (GPU tab), or null. */
    gpuConnection(name) {
      const id = gpuDeviceId(name);
      return id ? effectiveConnection(state.devices[id], { id, kind: "gpu-server", label: name }, state.matches) : null;
    },
    /** The location is on this computer. */
    isLocal(device) {
      if (!device) return false;
      if (device.id === "ark") return state.localArk;
      const id = graphHostId(device.id);
      return Boolean(id && state.myDevice && id === state.myDevice);
    },
  };
}

async function openEditor(url) {
  const d = desk();
  if (d?.openEditor) {
    await d.openEditor(url);
    return;
  }
  // Browsers hand custom schemes to the installed app (they may ask first).
  const a = h("a", { href: url, rel: "noopener", style: { display: "none" } });
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// ------------------------------------------------ SSH config entry for a server
// VS Code Remote-SSH and ssh only use the person's key (and a port and user)
// through an ~/.ssh/config Host alias.  Before opening a server that has no
// alias yet: an existing matching Host is used; else the desktop app adds
// `Host ark-<name>` to its marked block (it asks before writing), and a
// browser shows that entry to paste and remembers that it was added.

const ADDED_KEY = "ark.ssh.managed";   // alias -> "user@host:port" (browser: entry pasted)
const KEYFILE_KEY = "ark.ssh.keyfile"; // alias -> key file path (a path, never a key)
const AUTH_KEY = "ark.ssh.auth";       // alias -> "key" | "password" (the person's choice)

function readMap(name) {
  try { const v = JSON.parse(localStorage.getItem(name) || "{}"); return v && typeof v === "object" ? v : {}; } catch { return {}; }
}
function writeMap(name, key, value) {
  try {
    const map = readMap(name);
    if (value) map[key] = value; else delete map[key];
    localStorage.setItem(name, JSON.stringify(map));
  } catch { /* storage off: ask again next time */ }
}
const connKey = (c) => `${c.user || ""}@${c.host}:${c.port || 22}`;

/**
 * Forget aliases older versions made from a device's hostname (staleAliases):
 * "pasted" marks are dropped, a remembered key file and login mode move to
 * the machine's alias when it has none yet.
 */
function repairLocalAliases(devices, data) {
  const stale = staleAliases(devices, data);
  if (!stale.size) return;
  const added = readMap(ADDED_KEY);
  const keys = readMap(KEYFILE_KEY);
  const auth = readMap(AUTH_KEY);
  for (const [alias, own] of stale) {
    if (added[alias]) writeMap(ADDED_KEY, alias, "");
    if (own && keys[alias] && !keys[own]) writeMap(KEYFILE_KEY, own, keys[alias]);
    if (own && auth[alias] && !auth[own]) writeMap(AUTH_KEY, own, auth[alias]);
    if (keys[alias]) writeMap(KEYFILE_KEY, alias, "");
    if (auth[alias]) writeMap(AUTH_KEY, alias, "");
  }
}

/**
 * The connection to open with: `conn` with an ~/.ssh/config alias that
 * carries the port, user and (key login) the key.  Shows the SSH config
 * dialog when there is none yet; null when the person cancels.
 */
export async function prepareConnection(conn, { name = "" } = {}) {
  const c = cleanConnection(conn);
  if (!c) throw new Error(t("resources.sshcfg.no_address"));
  const d = desk();
  // Only an alias known to exist is used (resolveAlias); else the desktop
  // app writes ark-<machine name> first (it asks), a browser shows the entry
  // to paste.  Without a machine name: user@host:port, no alias.
  const managed = name ? managedAlias(name) : null;
  let found = null;
  if (d?.sshConfigMatch) {
    try { found = (await d.sshConfigMatch([c.alias, managed, c.host].filter(Boolean))) || []; } catch { found = []; }
  }
  const r = resolveAlias({ name, conn: c, found, added: d?.sshConfigMatch ? null : readMap(ADDED_KEY) });
  if (r.known) return { ...c, alias: r.alias };
  // No alias: key login uses the key file remembered on this computer.
  const keyFile = readMap(KEYFILE_KEY)[r.alias || c.host] || "";
  const direct = keyFile && authMode({ auth: c.auth, identityFile: keyFile }) === "key" ? { ...c, identityFile: keyFile } : c;
  if (!r.alias) return direct;
  // A browser cannot write ~/.ssh/config: open with user@host:port at once
  // and offer the entry (with the person's key) for next time.
  if (!d?.sshConfigEnsure) {
    const snippet = sshConfigSnippet([{ alias: r.alias, host: c.host, user: c.user || "", port: c.port || null,
      identityFile: keyFile, auth: authMode({ auth: c.auth, identityFile: keyFile }) }]);
    if (snippet) {
      toast(t("resources.sshcfg.direct_opened", { alias: r.alias }), "", { duration: 15000, action: {
        label: t("resources.sshcfg.copy"),
        onClick: async () => {
          const ok = await copyText(snippet);
          if (ok) writeMap(ADDED_KEY, r.alias, `${connKey(c)}#${authMode({ auth: c.auth, identityFile: keyFile })}`);
          toast(t(ok ? "resources.sshcfg.copied" : "resources.open.copy_failed"), ok ? "ok" : "err");
        },
      } });
    }
    return direct;
  }
  // Desktop app: write Host ark-<machine> once (it asks first), then open.
  return sshConfigDialog(c, r.alias, { name });
}

/**
 * The "SSH config entry" dialog; resolves to the connection to use or null.
 * Key login writes IdentityFile + IdentitiesOnly; password login writes
 * neither, so VS Code and ssh show their own password prompt (ARK never
 * asks for, stores or passes a password).
 */
export function sshConfigDialog(conn, alias, { name = "" } = {}) {
  const d = desk();
  const canWrite = Boolean(d?.sshConfigEnsure);
  return new Promise((resolve) => {
    let done = false;
    const finish = (value) => { if (!done) { done = true; resolve(value); } };
    const savedKey = readMap(KEYFILE_KEY)[alias] || "";
    const savedMode = readMap(AUTH_KEY)[alias] || "";
    // The mode: the person's earlier choice, else the server entry's, else
    // detected from a remembered key path.
    let mode = authMode({ auth: savedMode || conn.auth, identityFile: savedKey });
    let detected = !savedMode && !conn.auth;
    const key = h("input", {
      class: "input mono", value: savedKey, spellcheck: "false", maxlength: 1024,
      placeholder: t("resources.sshcfg.key_ph"), list: `ark-ssh-keys-${alias}`,
    });
    key.value = savedKey;
    const keyList = h("datalist", { id: `ark-ssh-keys-${alias}` });
    const preview = h("pre", { class: "code-box", style: { whiteSpace: "pre-wrap", wordBreak: "break-all", margin: "0" } });
    const entry = () => ({ alias, host: conn.host, user: conn.user || "", port: conn.port || null,
      identityFile: mode === "key" ? key.value.trim() : "", auth: mode });
    const radios = {};
    const radio = (value) => {
      const input = h("input", { type: "radio", name: `ark-ssh-auth-${alias}`, value });
      input.checked = mode === value;
      input.addEventListener("change", () => { if (input.checked) { mode = value; detected = false; update(); } });
      radios[value] = input;
      return h("label", { class: "row tight", style: { gap: "6px", cursor: "pointer" } }, input, t(`resources.sshcfg.auth_${value}`));
    };
    const setMode = (value) => { mode = value; radios[value].checked = true; };
    const authRow = h("div", { class: "row tight", style: { gap: "16px" } }, radio("key"), radio("password"));
    const browse = canWrite && d.chooseSshKey ? h("button", {
      class: "btn ghost", type: "button",
      onclick: async () => {
        try {
          const picked = await d.chooseSshKey();
          if (picked) { key.value = picked; setMode("key"); update(); }
        } catch (err) { toast(err?.message || String(err), "err"); }
      },
    }, t("resources.sshcfg.browse")) : null;
    const keyBox = h("div", { class: "stack" },
      h("label", { class: "field" }, h("span", null, t("resources.sshcfg.key")),
        h("div", { class: "row tight" }, key, browse), keyList),
      h("p", { class: "settings-lead", style: { margin: "0" } }, t("resources.sshcfg.key_hint")));
    // Password login: the prompt note and, optionally, how to switch to a key.
    const setup = keySetupCommands(conn, alias, { shell: shellKind() }) || [];
    const setupText = setup.join("\n");
    const passwordBox = h("div", { class: "stack" },
      h("p", { class: "settings-lead", style: { margin: "0" } }, t("resources.sshcfg.password_note")),
      setup.length ? h("details", null,
        h("summary", null, t("resources.sshcfg.key_setup")),
        h("p", { class: "settings-lead", style: { margin: "0" } }, t("resources.sshcfg.key_setup_lead")),
        h("pre", { class: "code-box", style: { whiteSpace: "pre-wrap", wordBreak: "break-all", margin: "0" } }, setupText),
        h("div", { class: "row tight" }, h("button", {
          class: "btn ghost", type: "button",
          onclick: async () => {
            const ok = await copyText(setupText);
            toast(t(ok ? "resources.sshcfg.copied_setup" : "resources.open.copy_failed"), ok ? "ok" : "err");
          },
        }, t("resources.sshcfg.copy_setup")))) : null);
    function update() {
      keyBox.style.display = mode === "key" ? "" : "none";
      passwordBox.style.display = mode === "password" ? "" : "none";
      preview.textContent = sshConfigSnippet([entry()]) || t("resources.sshcfg.key_invalid");
    }
    key.addEventListener("input", () => {
      // Typing a key path while the mode was only detected means key login.
      if (detected && key.value.trim()) setMode("key");
      update();
    });
    update();
    if (canWrite && d.sshKeyCandidates) {
      d.sshKeyCandidates().then((paths) => {
        for (const p of paths || []) keyList.appendChild(h("option", { value: p }));
        const suggested = key.value ? "" : keySuggestion(paths, name || alias);
        if (suggested) {
          key.value = suggested;
          // A key named after this server: key login, unless chosen otherwise.
          if (detected) setMode("key");
          update();
        }
      }).catch(() => {});
    }
    const copyBtn = canWrite ? null : h("button", {
      class: "btn ghost", type: "button",
      onclick: async () => {
        const snippet = sshConfigSnippet([entry()]);
        if (!snippet) { toast(t("resources.sshcfg.key_invalid"), "err"); return; }
        const ok = await copyText(snippet);
        toast(t(ok ? "resources.sshcfg.copied" : "resources.open.copy_failed"), ok ? "ok" : "err");
      },
    }, t("resources.sshcfg.copy"));
    const remember = () => {
      const k = key.value.trim();
      if (mode === "key" && k && !identityForConfig(k)) { toast(t("resources.sshcfg.key_invalid"), "err"); return false; }
      if (mode === "key") writeMap(KEYFILE_KEY, alias, k);
      writeMap(AUTH_KEY, alias, mode);
      return true;
    };
    // The connection as typed, for "Open without it" (the terminal then uses -i).
    const direct = () => ({ ...conn, auth: mode, identityFile: mode === "key" ? key.value.trim() : "" });
    openModal({
      title: t("resources.sshcfg.title", { name: name || conn.host }),
      body: h("div", { class: "stack" },
        h("p", { class: "settings-lead" }, t(canWrite ? "resources.sshcfg.lead_desktop" : "resources.sshcfg.lead_browser")),
        h("div", { class: "field" }, h("span", null, t("resources.sshcfg.auth")), authRow),
        keyBox,
        passwordBox,
        preview,
        canWrite ? null : h("p", { class: "settings-lead", style: { margin: "0" } }, t("resources.sshcfg.where")),
        copyBtn ? h("div", { class: "row tight" }, copyBtn) : null),
      onClose: () => finish(null),
      actions: [
        { label: t("resources.sshcfg.without"), ghost: true,
          onClick: () => { if (!remember()) return false; finish(direct()); return true; } },
        { spacer: true },
        { label: t("action.cancel"), ghost: true, onClick: () => { finish(null); } },
        {
          label: t(canWrite ? "resources.sshcfg.write_and" : "resources.sshcfg.added_and"), primary: true,
          onClick: async () => {
            if (!remember()) return false;
            if (canWrite) {
              try {
                await d.sshConfigEnsure(entry());
              } catch (err) { toast(err?.message || String(err), "err"); return false; }
            } else {
              writeMap(ADDED_KEY, alias, `${connKey(conn)}#${mode}`);
            }
            finish({ ...conn, alias, auth: mode });
            return true;
          },
        },
      ],
    });
  });
}

/**
 * Open a server: "editor" (VS Code / Cursor at `folder`) or "ssh" (the
 * desktop app's terminal; a browser copies the command).  Errors are shown.
 */
export async function openServer(action, conn, { name = "", folder = "~", editor = "vscode", path = null } = {}) {
  try {
    if (action === "editor") {
      const c = await prepareConnection(conn, { name });
      if (!c) return;
      const url = remoteEditorUrl(c, folder, editor) || remoteEditorUrl(c, "/", editor);
      if (!url) throw new Error(t("resources.sshcfg.no_address"));
      await openEditor(url);
      return;
    }
    const d = desk();
    if (d?.openSsh) {
      const c = await prepareConnection(conn, { name });
      if (!c) return;
      await d.openSsh(c, path);
      toast(t("resources.sshcfg.terminal_opened"), "ok");
      return;
    }
    // A browser copies the command (no alias needed: it runs in the person's own shell).
    const c = cleanConnection(conn);
    if (!c) throw new Error(t("resources.sshcfg.no_address"));
    const ok = await copyText(sshCommand(c, path, { shell: shellKind() }));
    toast(t(ok ? "resources.sshcfg.copied_ssh" : "resources.open.copy_failed"), ok ? "ok" : "err");
  } catch (err) {
    toast(err?.message || String(err), "err");
  }
}

/**
 * Context-menu items for a device or one of its locations (no separators).
 * device: a graph device ({id, kind, label}); path: the location's path,
 * or null for the device itself; onChange: after the SSH entry changed.
 */
export function openMenuItems(store, { device, path = null, onChange = null }) {
  if (!device) return [];
  const conn = store.connection(device);
  const actions = openActions({ device, path, local: store.isLocal(device), conn,
    terminal: Boolean(desk()?.openSsh), localTerminal: Boolean(desk()?.openTerminal), canSave: store.state.canSave });
  // The managed alias is named after the linked machine, never the device's hostname.
  const name = store.machineName?.(device) || "";
  const run = (fn) => async () => {
    try { await fn(); } catch (err) { toast(err?.message || String(err), "err"); }
  };
  return actions.map((a) => {
    switch (a.id) {
      case "vscode":
      case "cursor":
        // Remote folders go through the SSH config entry (the key), local ones open directly.
        return { label: t(`resources.open.${a.id}`), icon: "external",
          onClick: a.conn
            ? () => openServer("editor", a.conn, { name, folder: a.folder, editor: a.id })
            : run(() => openEditor(a.url)) };
      case "terminal":
        return { label: t("resources.open.terminal"), icon: "terminal",
          onClick: a.local
            ? run(async () => { await desk().openTerminal(a.path); })
            : () => openServer("ssh", a.command.conn, { name, path: a.command.path }) };
      case "copy-ssh":
        return { label: t("resources.open.copy_ssh"), icon: "copy",
          onClick: run(async () => {
            const ok = await copyText(sshCommand(a.command.conn, a.command.path, { shell: shellKind() }));
            toast(t(ok ? "resources.open.copied_ssh" : "resources.open.copy_failed"), ok ? "ok" : "err");
          }) };
      case "copy-path":
        return { label: t("resources.open.copy_path"), icon: "copy",
          onClick: run(async () => {
            const ok = await copyText(a.path);
            toast(t(ok ? "resources.open.copied_path" : "resources.open.copy_failed"), ok ? "ok" : "err");
          }) };
      case "vscode-setup":
        // The SSH dialog (prefilled), then VS Code once the connection is saved.
        return {
          label: t("resources.open.vscode_setup"), icon: "external",
          disabled: !a.canSave,
          title: a.canSave ? undefined : t("resources.open.ssh_sign_in"),
          onClick: () => openSshForm(store, device, onChange, {
            afterSave: () => {
              const conn = store.connection(device);
              if (conn) return openServer("editor", conn, { name: store.machineName?.(device) || "", folder: a.folder });
              return null;
            },
          }),
        };
      case "ssh-setup":
        return {
          label: t("resources.open.ssh_edit"), icon: "settings",
          disabled: !a.canSave,
          title: a.canSave ? undefined : t("resources.open.ssh_sign_in"),
          onClick: () => openSshForm(store, device, onChange),
        };
      default:
        return null;
    }
  }).filter(Boolean);
}

/**
 * The SSH dialog of a device: the machine it is linked to, the machine's
 * shared host, port and user (admins edit them, everyone else overrides
 * them for themselves), and the viewer's alias, login and key file.
 * Opens prefilled from GET /api/resources/connect (sshFormPrefill).
 */
export function openSshForm(store, device, onChange = null, { afterSave = null } = {}) {
  const st = store.state;
  const data = { machines: st.machines, links: st.links, mine: st.mine, suggest: st.suggest };
  const admin = st.canEditShared;
  const local = { matches: st.matches, added: readMap(ADDED_KEY), localAuth: readMap(AUTH_KEY), localKeys: readMap(KEYFILE_KEY) };
  let prefill = sshFormPrefill({ device, data, ...local });
  const savedMachine = prefill.machine;
  const input = (attrs = {}) => h("input", { class: "input", spellcheck: "false", ...attrs });
  const name = input({ maxlength: 60 });
  const host = input({ maxlength: 253, placeholder: t("resources.ssh.host_ph") });
  const port = input({ maxlength: 5, inputmode: "numeric", placeholder: "22" });
  const user = input({ maxlength: 64, placeholder: t("resources.ssh.user_ph") });
  const alias = input({ maxlength: 100 });
  const key = input({ class: "input mono", maxlength: 1024, placeholder: t("resources.sshcfg.key_ph") });
  // How you log in: detected (a key path set on this computer means key) or chosen.
  const auth = h("select", { class: "input" },
    ["", "key", "password"].map((v) => h("option", { value: v }, t(`resources.ssh.auth_${v || "auto"}`)))); // i18n-ignore: option values; labels go through t()
  const linkNote = h("p", { class: "muted", style: { margin: "0" } });
  const picker = h("select", { class: "input", disabled: !admin },
    h("option", { value: "" }, t("resources.ssh.machine_own", { device: device.label || device.id })),
    prefill.options.map((o) => h("option", { value: o.id }, o.name)));
  const keyField = h("label", { class: "field" }, h("span", null, t("resources.ssh.key")), key);
  const fill = () => {
    const v = prefill.values;
    name.value = v.name;
    host.value = v.host;
    port.value = v.port;
    user.value = v.user;
    alias.value = v.alias;
    alias.placeholder = prefill.aliasPlaceholder;
    auth.value = v.auth;
    key.value = v.keyFile;
    picker.value = prefill.machine && prefill.machine !== device.id ? prefill.machine : "";
    const how = { server: "linked_server", hostname: "linked_hostname", address: "linked_address" }[prefill.how];
    linkNote.textContent = how ? t(`resources.ssh.${how}`) : "";
    linkNote.style.display = how ? "" : "none";
    keyField.style.display = auth.value === "password" ? "none" : "";
  };
  picker.addEventListener("change", () => {
    prefill = sshFormPrefill({ device, data, link: picker.value, ...local });
    fill();
  });
  auth.addEventListener("change", () => { keyField.style.display = auth.value === "password" ? "none" : ""; });
  fill();
  const field = (label, el) => h("label", { class: "field" }, h("span", null, label), el);
  const group = (title, ...children) => h("div", { class: "stack" }, h("b", null, title), ...children);
  const save = async (body) => {
    try {
      await api.resourceConnectSave(device.id, body);
      await store.refresh();
      toast(t("resources.ssh.saved"), "ok");
      onChange?.();
    } catch (err) { toast(err.message, "err"); return false; }
    if (afterSave) setTimeout(() => afterSave(), 0);
  };
  // Login mode and key file stay in this computer's storage, per alias.
  const remember = (form) => {
    const k = form.alias || prefill.managedAlias || form.host;
    if (!validAlias(k)) return true;
    const path = key.value.trim();
    if (form.auth !== "password" && path && !identityForConfig(path)) { toast(t("resources.sshcfg.key_invalid"), "err"); return false; }
    writeMap(KEYFILE_KEY, k, form.auth === "password" ? "" : path);
    writeMap(AUTH_KEY, k, form.auth);
    // A browser cannot read ~/.ssh/config: an alias typed here counts as confirmed.
    if (!desk()?.sshConfigMatch && form.alias && form.host) {
      writeMap(ADDED_KEY, form.alias, `${connKey({ user: form.user, host: form.host, port: form.port ? Number(form.port) : 22 })}#${form.auth || "key"}`);
    }
    return true;
  };
  const hasMine = Object.keys(prefill.personal).length > 0;
  openModal({
    title: t("resources.ssh.title", { device: device.label || device.id }),
    body: h("div", { class: "stack" },
      h("p", { class: "settings-lead" }, t(admin ? "resources.ssh.lead" : "resources.ssh.lead_member")),
      field(t("resources.ssh.machine"), picker),
      linkNote,
      group(t("resources.ssh.shared"),
        admin ? field(t("resources.ssh.name"), name) : null,
        field(t("resources.ssh.host"), host),
        h("div", { class: "row tight" }, field(t("resources.ssh.user"), user), field(t("resources.ssh.port"), port))),
      group(t("resources.ssh.personal"),
        field(t("resources.ssh.alias"), alias),
        field(t("resources.ssh.auth"), auth),
        keyField)),
    actions: [
      hasMine && st.canSavePersonal ? { label: t("resources.ssh.clear"), ghost: true, danger: true, onClick: () => save({}) } : null,
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("resources.ssh.save"), primary: true,
        onClick: () => {
          const form = { name: name.value.trim(), host: host.value.trim(), port: port.value.trim(), user: user.value.trim(),
            alias: alias.value.trim(), auth: auth.value };
          if ((form.alias && !validAlias(form.alias)) || (form.host && !validHost(form.host)) || (form.user && !validUser(form.user))
            || (form.port && !/^\d{1,5}$/.test(form.port))) {
            toast(t("resources.ssh.invalid"), "err");
            return false;
          }
          if (!form.alias && !form.host) { toast(t("resources.ssh.need_host"), "err"); return false; }
          if (!remember(form)) return false;
          const body = sshFormBody(prefill, form, { admin, personal: st.canSavePersonal,
            linkChanged: admin && prefill.machine !== savedMachine });
          return save(body);
        },
      },
    ].filter(Boolean),
  });
}
