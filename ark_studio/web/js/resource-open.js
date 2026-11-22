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
        return { label: t(`resources.open.${a.id}`), icon: "external", onClick: run(() => openEditor(a.url)) };
      case "ssh-terminal":
        return { label: t("resources.open.ssh_terminal"), icon: "terminal",
          onClick: run(async () => { await desk().openSsh(a.command.conn, a.command.path); }) };
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
      h("div", { class: "row tight" }, field(t("resources.ssh.user"), user), field(t("resources.ssh.port"), port))),
    actions: [
      mine ? { label: t("resources.ssh.clear"), ghost: true, danger: true, onClick: () => save({}) } : null,
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("resources.ssh.save"), primary: true,
        onClick: () => {
          const body = { alias: alias.value.trim(), host: host.value.trim(), user: user.value.trim(), port: port.value.trim() };
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
