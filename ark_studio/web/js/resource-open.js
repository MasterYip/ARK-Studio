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
  authMode, keySetupCommands,
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
  const state = { devices: {}, hosts: {}, canSave: false, matches: new Map(), myDevice: "", localArk: false, loaded: false };
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
        state.canSave = data?.can_save === true;
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
        try {
          const found = names.size ? await match([...names].slice(0, 64)) : [];
          state.matches = new Map((found || []).filter((m) => validAlias(m.alias)).map((m) => [String(m.name).toLowerCase(), m.alias]));
        } catch { state.matches = new Map(); }
      }
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
 * The connection to open with: `conn` with an ~/.ssh/config alias that
 * carries the port, user and (key login) the key.  Shows the SSH config
 * dialog when there is none yet; null when the person cancels.
 */
export async function prepareConnection(conn, { name = "" } = {}) {
  const c = cleanConnection(conn);
  if (!c) throw new Error(t("resources.sshcfg.no_address"));
  if (c.alias) return c;
  const alias = managedAlias(name) || managedAlias(c.host);
  if (!alias) return c;
  const d = desk();
  if (d?.sshConfigMatch) {
    let found = [];
    try { found = (await d.sshConfigMatch([alias, c.host])) || []; } catch { found = []; }
    const own = found.find((m) => m.alias === alias && String(m.hostname || "").toLowerCase() === c.host.toLowerCase()
      && configMatchFits(m, c));
    if (own) return { ...c, alias };
    const other = found.find((m) => String(m.name).toLowerCase() === c.host.toLowerCase() && configMatchFits(m, c));
    if (other) return { ...c, alias: other.alias };
  } else if (String(readMap(ADDED_KEY)[alias] || "").split("#")[0] === connKey(c)) {
    return { ...c, alias };
  }
  return sshConfigDialog(c, alias, { name });
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
    const c = await prepareConnection(conn, { name });
    if (!c) return;
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
    terminal: Boolean(desk()?.openSsh), canSave: store.state.canSave });
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
            ? () => openServer("editor", a.conn, { name: device.label || device.id, folder: a.folder, editor: a.id })
            : run(() => openEditor(a.url)) };
      case "ssh-terminal":
        return { label: t("resources.open.ssh_terminal"), icon: "terminal",
          onClick: () => openServer("ssh", a.command.conn, { name: device.label || device.id, path: a.command.path }) };
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
      case "ssh-setup":
        return {
          label: t(a.known ? "resources.open.ssh_edit" : "resources.open.ssh_setup"), icon: "settings",
          hint: a.known ? null : t("resources.open.ssh_setup_hint"),
          disabled: !a.canSave,
          title: a.canSave ? undefined : t("resources.open.ssh_sign_in"),
          onClick: () => openSshForm(store, device, onChange),
        };
      default:
        return null;
    }
  }).filter(Boolean);
}

/** The viewer's own SSH entry for a device: alias and/or host, user, port. */
export function openSshForm(store, device, onChange = null) {
  const saved = store.state.devices[device.id];
  const mine = saved?.source === "mine" ? saved : null;
  const input = (value, attrs = {}) => {
    const el = h("input", { class: "input", value: value ?? "", ...attrs });
    el.value = value ?? "";
    return el;
  };
  const alias = input(mine?.alias, { maxlength: 100, placeholder: t("resources.ssh.alias_ph"), spellcheck: "false" });
  const host = input(mine?.host, { maxlength: 253, placeholder: t("resources.ssh.host_ph"), spellcheck: "false" });
  const user = input(mine?.user, { maxlength: 64, placeholder: t("resources.ssh.user_ph"), spellcheck: "false" });
  const port = input(mine?.port ? String(mine.port) : "", { maxlength: 5, inputmode: "numeric", placeholder: "22" });
  // How you log in: detected (a key path set on this computer means key) or chosen.
  const auth = h("select", { class: "input" },
    ["", "key", "password"].map((v) => h("option", { value: v }, t(`resources.ssh.auth_${v || "auto"}`)))); // i18n-ignore: option values; labels go through t()
  auth.value = mine?.auth || "";
  const field = (label, el) => h("label", { class: "field" }, h("span", null, label), el);
  const save = async (body) => {
    try {
      await api.resourceConnectSave(device.id, body);
      await store.refresh();
      toast(t("resources.ssh.saved"), "ok");
      onChange?.();
    } catch (err) { toast(err.message, "err"); return false; }
  };
  openModal({
    title: t("resources.ssh.title", { device: device.label || device.id }),
    body: h("div", { class: "stack" },
      h("p", { class: "settings-lead" }, t("resources.ssh.lead")),
      saved && saved.source !== "mine" ? h("p", { class: "muted" }, t("resources.ssh.from_config")) : null,
      field(t("resources.ssh.alias"), alias),
      field(t("resources.ssh.host"), host),
      h("div", { class: "row tight" }, field(t("resources.ssh.user"), user), field(t("resources.ssh.port"), port)),
      field(t("resources.ssh.auth"), auth)),
    actions: [
      mine ? { label: t("resources.ssh.clear"), ghost: true, danger: true, onClick: () => save({}) } : null,
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("resources.ssh.save"), primary: true,
        onClick: () => {
          const body = { alias: alias.value.trim(), host: host.value.trim(), user: user.value.trim(), port: port.value.trim(),
            auth: auth.value };
          if ((body.alias && !validAlias(body.alias)) || (body.host && !validHost(body.host)) || (body.user && !validUser(body.user))
            || (body.port && !/^\d{1,5}$/.test(body.port))) {
            toast(t("resources.ssh.invalid"), "err");
            return false;
          }
          if (!body.alias && !body.host) { toast(t("resources.ssh.need_host"), "err"); return false; }
          return save(body);
        },
      },
    ].filter(Boolean),
  });
}
