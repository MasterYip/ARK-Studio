// Desktop hub: ARK inside the ARK Studio desktop app, which always runs a
// local ARK and keeps a list of saved remote servers.  The app's shell
// injects window.__ARK_DESKTOP__.hub; what a page may call is decided by the
// app (Tauri capabilities), by the page's origin:
//
// * the local ARK page: context(), servers(), status(url), openServer(url,
//   {project, newProject, link}), openSettings(view), goLocal(), and for
//   linked servers serverProjects(url), createServerProject(url, data),
//   unlinkServer(url);
// * a remote ARK page: context(), goLocal() ("back to this computer") and
//   linkServer(code) for its own server only; it never sees the saved
//   servers.
//
// This module adds the desktop groups to the project switcher and the
// Projects dialog ("This computer" / "Servers" with the linked servers'
// projects), the "Location" choice of the new-project form, "Link this
// desktop" on a server page, and handles ?ark_open_project= /
// ?ark_new_project=1 / ?ark_link_desktop=1 (how the app opens a project, the
// new-project dialog or the link prompt on a server).  Browsers have no hub:
// nothing here renders and the query is still honored.

import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import {
  desktopHub, hubContext, normalizeServers, serverState, serverProjectsState, locationOptions, canLinkHere,
  linkThisDesktop,
} from "./desktop-hub-model.js";

export {
  OPEN_PROJECT_PARAM, NEW_PROJECT_PARAM, LINK_DESKTOP_PARAM, desktopHub, hubContext, normalizeServers, serverState,
  serverProjectsState, locationOptions, canLinkHere, desktopQuery, applyDesktopQuery,
} from "./desktop-hub-model.js";

const message = (err) => {
  const text = String(err?.message || err || "");
  return text.startsWith("desktop.") ? t(text) : text;
};

function groupHead(text) {
  return h("div", { class: "desktop-group-head", role: "presentation" }, text);
}

function rowButton({ cls = "", iconName, title, detail, onclick, data = {} }) {
  return h("button", { class: `project-row ${cls}`.trim(), type: "button", onclick, ...data },
    icon(iconName, "sm"),
    h("span", { class: "main" },
      h("span", { class: "t" }, h("span", { class: "n" }, title)),
      detail ? h("span", { class: "d" }, detail) : null),
    icon("chevronRight", "sm"));
}

async function openServer(hub, server, { toast, close, newProject = false, project = null, link = false } = {}) {
  try {
    const res = await hub.openServer(server.url, { newProject, project, link });
    if (res && res.opened === false) {
      toast?.(t("desktop.server.unreachable", { name: server.label }), "err");
      return false;
    }
    close?.();
    return true;
  } catch (err) {
    toast?.(message(err), "err");
    return false;
  }
}

/** One saved server: its row, and under it the linked server's projects or a "Link…" offer. */
function serverBlock(hub, server, { toast, close }) {
  const pill = h("span", { class: "desktop-server-state", "data-state": "checking" }, t("desktop.server.checking"));
  const setState = (status) => {
    const st = serverState(status);
    pill.dataset.state = st;
    pill.textContent = t(`desktop.server.${st}`);
  };
  const row = h("button", {
    class: "project-row desktop-server-row", type: "button", title: server.url, "data-server-url": server.url,
    onclick: () => openServer(hub, server, { toast, close }),
  },
    icon("external", "sm"),
    h("span", { class: "main" },
      h("span", { class: "t" }, h("span", { class: "n" }, server.label), pill),
      h("span", { class: "d" }, server.linked && server.user
        ? `${server.url} · ${t("desktop.link.as", { user: server.user })}` : server.url)),
    icon("chevronRight", "sm"));
  const nested = h("div", { class: "desktop-server-projects" });
  const linkOffer = (text) => h("div", { class: "desktop-link-offer" },
    h("span", null, text),
    h("button", {
      class: "btn ghost sm", type: "button", "data-desktop-action": "link",
      onclick: () => openServer(hub, server, { toast, close, link: true }),
    }, icon("link", "sm"), t("desktop.link.action")));
  const fill = (reply) => {
    const st = serverProjectsState(reply);
    clear(nested);
    if (st.kind === "projects") {
      if (!st.projects.length) nested.appendChild(h("div", { class: "empty sm" }, t("desktop.link.no_projects")));
      for (const p of st.projects) {
        nested.appendChild(rowButton({
          cls: "desktop-remote-project", iconName: "folder", title: p.name, detail: p.description || null,
          data: { "data-project-id": p.id, title: `${server.label} · ${p.name}` },
          onclick: () => openServer(hub, server, { toast, close, project: p.id }),
        }));
      }
    } else if (st.kind === "not_linked") {
      nested.appendChild(linkOffer(t("desktop.link.offer")));
    } else if (st.kind === "expired") {
      nested.appendChild(linkOffer(t("desktop.link.expired")));
    } else if (st.kind === "unsupported") {
      nested.appendChild(h("div", { class: "empty sm" }, t("desktop.link.unsupported")));
    } else if (st.kind === "error") {
      nested.appendChild(h("div", { class: "empty sm" }, st.error || t("desktop.link.list_failed")));
    }
  };
  if (typeof hub.serverProjects === "function" && server.linked) {
    Promise.resolve(hub.serverProjects(server.url)).then((reply) => {
      fill(reply);
      if (reply && typeof reply.reachable === "boolean") setState({ reachable: reply.reachable });
      else Promise.resolve(hub.status(server.url)).then(setState, () => setState({ reachable: false }));
    }, () => setState({ reachable: false }));
  } else {
    Promise.resolve(hub.status(server.url)).then((st) => {
      setState(st);
      if (typeof hub.serverProjects === "function" && st?.reachable) fill({ linked: false });
    }, () => setState({ reachable: false }));
  }
  return h("div", { class: "desktop-server", "data-server-url": server.url }, row, nested);
}

/**
 * "Link this desktop" on a server page.  The dialog stays open while it
 * links: the button shows progress, a failure or the timeout is shown in the
 * dialog with the button enabled again, and Cancel always closes it (and
 * stops the code request).  `modal` is components.js openModal.  Resolves
 * to true once linked.
 */
export async function confirmLinkDesktop({ hub = desktopHub(), modal, toast, requestCode = (body) => api.linkDesktop(body),
  timeoutMs } = {}) {
  const context = await hubContext(hub);
  if (!canLinkHere(context)) {
    if (context?.role === "remote" && context.linked) toast?.(t("desktop.link.already"), "ok");
    return false;
  }
  return new Promise((resolve) => {
    let running = null; // AbortController of the attempt in progress
    let settled = false;
    const settle = (value) => { if (!settled) { settled = true; resolve(value); } };
    const error = h("div", { class: "desktop-link-error", role: "alert", hidden: true });
    const body = h("div", { class: "desktop-link-body" },
      h("div", { style: { fontSize: "13px", color: "var(--sub)", lineHeight: "1.6" } }, t("desktop.link.confirm")), error);
    const link = {
      label: t("desktop.link.action"), primary: true, close: false,
      onClick: async (_event, close) => {
        if (running) return false;
        const btn = link._btn;
        running = typeof AbortController === "function" ? new AbortController() : { signal: null, abort() {} };
        const attempt = running;
        error.hidden = true;
        error.textContent = "";
        if (btn) { btn.disabled = true; btn.setAttribute("aria-busy", "true"); btn.textContent = t("desktop.link.working"); }
        try {
          const res = await linkThisDesktop(hub, requestCode, { device: "ARK Studio", signal: attempt.signal, timeoutMs });
          if (attempt.signal?.aborted || settled) return false;
          settle(true);
          close(true);
          toast?.(t("desktop.link.done", { user: res?.user || "" }), "ok");
        } catch (err) {
          if (attempt.signal?.aborted || settled) return false;
          error.textContent = t("desktop.link.failed", { error: message(err) });
          error.hidden = false;
        } finally {
          if (running === attempt) running = null;
          if (btn?.isConnected) { btn.disabled = false; btn.removeAttribute("aria-busy"); btn.textContent = t("desktop.link.action"); }
        }
        return false;
      },
    };
    modal({
      title: t("desktop.link.title"), body, raised: true,
      actions: [{ label: t("action.cancel"), ghost: true, onClick: () => { running?.abort(); } }, link],
      onClose: () => { running?.abort(); settle(false); },
      enterConfirm: true,
    });
  });
}

/**
 * Wraps the project list of the switcher / Projects dialog.  Outside the
 * desktop app it returns `list` unchanged.  In the app the wrapper gains,
 * once the page's role is known:
 * - local page: "This computer" over the list, then "Servers" (saved
 *   servers with their online state and, when linked, their projects;
 *   "Connect to a server…");
 * - remote page: "This computer" with "Back to this computer" (and "Link
 *   this desktop" when it is not linked yet), then "This server" over the
 *   list.
 */
export function withDesktopGroups(list, { close = null, toast = null, modal = null, hub = desktopHub() } = {}) {
  if (!hub) return list;
  const wrap = h("div", { class: "desktop-groups" }, list);
  hubContext(hub).then((context) => {
    if (!context) return;
    if (context.role === "remote") {
      const local = h("div", { class: "project-list desktop-list" },
        rowButton({
          cls: "desktop-local-row", iconName: "home", title: t("desktop.back_local"), detail: t("desktop.back_local_hint"),
          data: { "data-desktop-action": "local" },
          onclick: () => { close?.(); Promise.resolve(hub.goLocal()).catch((err) => toast?.(message(err), "err")); },
        }));
      if (canLinkHere(context) && modal) {
        local.appendChild(rowButton({
          cls: "desktop-link-row", iconName: "link", title: t("desktop.link.title"), detail: t("desktop.link.row_hint"),
          data: { "data-desktop-action": "link" },
          onclick: () => { close?.(); confirmLinkDesktop({ hub, modal, toast }); },
        }));
      }
      wrap.prepend(groupHead(t("desktop.group.local")), local, groupHead(t("desktop.group.this_server")));
      return;
    }
    if (context.role !== "local") return;
    wrap.prepend(groupHead(t("desktop.group.local")));
    const servers = h("div", { class: "project-list desktop-list", role: "group", "data-menu-group": "servers",
      "aria-label": t("desktop.group.servers") });
    const connect = h("button", {
      class: "btn ghost desktop-connect", type: "button", "data-desktop-action": "connect",
      onclick: () => { close?.(); hub.openSettings("connect"); },
    }, icon("plus", "sm"), t("desktop.connect"));
    wrap.append(groupHead(t("desktop.group.servers")), servers, connect);
    Promise.resolve(hub.servers()).then((payload) => {
      const saved = normalizeServers(payload);
      clear(servers);
      if (!saved.length) { servers.appendChild(h("div", { class: "empty sm" }, t("desktop.no_servers"))); return; }
      for (const server of saved) servers.appendChild(serverBlock(hub, server, { toast, close }));
    }).catch(() => servers.appendChild(h("div", { class: "empty sm" }, t("desktop.no_servers"))));
  });
  return wrap;
}

/**
 * The "Location" field of the new-project form.  Outside the app, or on a
 * page that is not the local ARK, or without saved servers: hidden.  Fills
 * in asynchronously; `value()` is "local" or a server URL.
 */
export function newProjectLocation({ hub = desktopHub() } = {}) {
  if (!hub) return null;
  const select = h("select", { class: "select", "data-desktop-location": "" },
    h("option", { value: "local" }, t("desktop.location.local")));
  const hint = h("div", { class: "muted desktop-location-hint", style: { fontSize: "10px", marginTop: "4px" } });
  const field = h("div", { class: "field desktop-location", hidden: true },
    h("label", null, t("desktop.location")), select, hint);
  const servers = new Map();
  const sync = () => {
    const remote = servers.get(select.value);
    hint.textContent = !remote ? ""
      : remote.linked ? t("desktop.location.linked_hint", { name: remote.label, user: remote.user || "?" })
        : t("desktop.location.server_hint", { name: remote.label });
  };
  select.addEventListener("change", sync);
  hubContext(hub).then(async (context) => {
    if (context?.role !== "local") return;
    const saved = normalizeServers(await hub.servers());
    if (!saved.length) return;
    clear(select);
    for (const option of locationOptions(saved, t("desktop.location.local"))) select.appendChild(h("option", { value: option.value }, option.label));
    for (const s of saved) servers.set(s.url, s);
    field.hidden = false;
    sync();
  }).catch(() => {});
  return {
    el: field,
    value: () => (field.hidden ? "local" : select.value || "local"),
    server: () => servers.get(select.value) || null,
    /**
     * New project on the chosen server.  Linked: created there by the app
     * and opened; else the server's own new-project dialog opens.  Returns
     * true when the dialog may close.
     */
    createOnServer: async ({ name = "", description = "", template = "blank", toast, close } = {}) => {
      const server = servers.get(select.value);
      if (!server) return false;
      if (!server.linked || typeof hub.createServerProject !== "function") {
        return openServer(hub, server, { toast, close, newProject: true });
      }
      if (!name.trim()) { toast?.(t("error.required"), "err"); return false; }
      try {
        const res = await hub.createServerProject(server.url, { name: name.trim(), description, template });
        toast?.(t("desktop.location.created", { name: res?.project?.name || name, server: server.label }), "ok");
        if (res?.project?.id) await openServer(hub, server, { toast, close, project: res.project.id });
        else close?.();
        return true;
      } catch (err) {
        toast?.(message(err), "err");
        return false;
      }
    },
  };
}
