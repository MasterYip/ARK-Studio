// About ARK Studio and the app's own settings inside the web Settings:
// Settings › About (version, updates), Settings › App (the desktop app's
// settings: updates, servers, start-up, data; only inside the app), their
// cards on the overview, the "settings of this server / of this app" line in
// the Settings header, the "ARK Studio was updated to ..." card and the
// activity bar's About button (with a dot while the app has an update).
// Data: desktop-update-model.js; the update state is shared (update-state.js).
import { h, relativeTime } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { aboutState, aboutStatus, hasUpdate, inDesktopApp, updatedNotice } from "./desktop-update-model.js";
import { announceUpdate, appUpdateHub, latestUpdate, onUpdate, publishUpdate, refreshUpdate } from "./update-state.js";

/** The release version the ARK server put in this page (empty for development builds). */
export function serverVersion(doc = globalThis.document) {
  return doc?.querySelector?.('meta[name="ark-version"]')?.content?.trim() || "";
}

const currentState = () => aboutState(latestUpdate(), { serverVersion: serverVersion() });

const versionLabel = (state) => (state.version ? `ARK Studio ${state.version}` : "ARK Studio");

/** Re-render `el` on every new update state while it is on the page. */
function follow(el, render) {
  let seen = false;
  const off = onUpdate(() => {
    if (el.isConnected) { seen = true; render(); } else if (seen) off();
  });
}

/** Opens the app's settings window at a section; null when this app cannot. */
function appSettingsOpener(hub) {
  if (typeof hub?.openAppSettings === "function") return (view) => hub.openAppSettings(view);
  if (typeof hub?.openSettings === "function") return (view) => hub.openSettings(view); // older app, local page
  return null;
}

// ------------------------------------------ "ARK Studio was updated to ..."

/** The card after the app's first start of a new version (local Home, About); null when there is none. */
export function updatedNoticeCard(info, hub, { onClose = null } = {}) {
  const notice = updatedNotice(info);
  if (!notice) return null;
  const card = h("section", { class: "panel app-updated", "data-app-updated": notice.version,
    "aria-label": t("app.updated.title", { version: notice.version }) },
  h("div", { class: "app-updated-head" },
    icon("sparkles", "sm"),
    h("b", null, t("app.updated.title", { version: notice.version })),
    notice.previous ? h("span", { class: "muted small" }, t("app.updated.from", { version: notice.previous })) : null,
    h("button", { class: "btn sm ghost app-updated-close", type: "button", "data-app-updated-close": "",
      onclick: async () => {
        card.remove();
        onClose?.();
        try { publishUpdate(await hub.dismissUpdateNotice()); } catch { /* older app */ }
      } }, t("app.updated.close"))),
  notice.changes.length ? h("ul", { class: "app-updated-list" }, notice.changes.map((item) => h("li", null, item))) : null,
  h("a", { class: "app-updated-link", href: notice.url, target: "_blank", rel: "noopener noreferrer" },
    icon("external", "sm"), t("app.updated.all")));
  return card;
}

// --------------------------------------------------------- Settings › About

/**
 * Settings › About › Updates.  Local page of the app: version, install type,
 * last check, Check for updates, Update and restart (Download for the
 * portable build), the automatic check.  Remote page in the app: the app's
 * and the server's version and the way to the app's settings.  Browser: the
 * server's version.
 */
export function aboutUpdateBlock() {
  const box = h("div", { class: "about-update", "data-about-update": "" });
  let hub = null;
  let role = "";
  let busy = false;
  let checked = false;
  let message = null; // [key, params, isError] after an action
  const render = () => {
    const state = currentState();
    const server = serverVersion();
    const rows = [
      h("dt", null, t(state.kind === "desktop" ? "about.app_version" : "about.server_version")),
      h("dd", { "data-about-version": "" }, state.version || t("about.version_dev")),
    ];
    if (state.kind === "desktop" && role === "remote") {
      rows.push(h("dt", null, t("about.server_version")), h("dd", { "data-about-server-version": "" }, server || t("about.version_dev")));
    }
    if (state.kind === "desktop") {
      rows.push(h("dt", null, t("about.install_type")),
        h("dd", null, t(state.portable ? "about.install_portable" : "about.install_installer")));
      if (state.enabled) {
        rows.push(h("dt", null, t("about.last_check")),
          h("dd", { "data-about-last-check": "" },
            state.lastCheck ? relativeTime(new Date(state.lastCheck).toISOString()) : t("about.never_checked")));
      }
    }
    const status = message || aboutStatus(state, { checked });
    const statusLine = h("p", {
      class: `about-update-status${message?.[2] ? " is-error" : ""}${state.available ? " is-new" : ""}`,
      role: "status", "aria-live": "polite", hidden: status ? undefined : true,
    }, status ? t(status[0], status[1]) : "");
    const actions = [];
    const toggles = [];
    const openApp = appSettingsOpener(hub);
    if (state.kind === "desktop" && role === "local" && state.enabled && hub) {
      if (typeof hub.checkUpdate === "function") {
        actions.push(h("button", {
          class: "btn sm", type: "button", "data-about-check": "", disabled: busy || undefined,
          onclick: () => check(),
        }, icon("refresh", "sm"), t("about.check")));
      }
      if (state.action === "install") {
        actions.push(h("button", {
          class: "btn sm primary", type: "button", "data-about-install": "", disabled: busy || undefined,
          onclick: () => install(),
        }, icon("download", "sm"), t("about.install")));
      } else if (state.action === "download") {
        actions.push(h("button", {
          class: "btn sm primary", type: "button", "data-about-download": "",
          onclick: () => Promise.resolve(hub.openReleases?.(state.available)).catch(fail("about.update.open_failed")),
        }, icon("external", "sm"), t("about.download")));
      }
      if (typeof hub.setAutoCheck === "function") {
        const input = h("input", {
          type: "checkbox", "data-about-auto-check": "", checked: state.autoCheck || undefined,
          onchange: async (event) => {
            const on = event.target.checked;
            try { publishUpdate(await hub.setAutoCheck(on)); } catch (err) {
              event.target.checked = !on;
              fail("about.update.save_failed")(err);
            }
          },
        });
        toggles.push(h("label", { class: "row tight about-update-toggle" }, input, h("span", null, t("about.auto_check"))));
      }
    } else if (state.kind === "desktop" && role === "remote" && openApp) {
      // A server page cannot install: the app's settings window can.
      actions.push(h("button", {
        class: `btn sm${state.available ? " primary" : ""}`, type: "button", "data-about-open-app": "",
        onclick: () => Promise.resolve(openApp("about")).catch(fail("about.update.open_failed")),
      }, icon("settings", "sm"), t(state.available ? "about.update_in_app" : "about.open_app_settings")));
    } else if (state.kind === "desktop" && !state.enabled) {
      actions.push(h("a", {
        class: "btn sm", href: "https://github.com/MasterYip/ARK-Studio/releases/latest",
        target: "_blank", rel: "noopener noreferrer",
      }, icon("external", "sm"), t("about.latest_release")));
    }
    const updated = role === "local" ? updatedNoticeCard(latestUpdate(), hub) : null;
    box.replaceChildren(...[
      updated,
      h("h3", { class: "about-update-title" }, t("about.updates")),
      h("dl", { class: "settings-about-details about-update-details" }, rows),
      statusLine,
      actions.length ? h("div", { class: "settings-actions about-update-actions" }, actions) : null,
      ...toggles,
      state.kind === "server" ? h("p", { class: "muted small about-update-note" }, t("about.update.server")) : null,
    ].filter(Boolean));
  };
  const fail = (key) => (err) => {
    message = [key, { error: String(err?.message || err) }, true];
    render();
  };
  async function check() {
    busy = true;
    message = ["about.update.checking", {}, false];
    render();
    try {
      publishUpdate(await hub.checkUpdate());
      announceUpdate();
      checked = true;
      message = null;
    } catch (err) {
      message = ["about.update.check_failed", { error: String(err?.message || err) }, true];
    }
    busy = false;
    render();
  }
  async function install() {
    busy = true;
    message = ["about.update.installing", {}, false];
    render();
    try {
      // Installs and restarts the app; returning means nothing was installed.
      publishUpdate(await hub.installUpdate());
      message = null;
    } catch (err) {
      message = ["about.update.install_failed", { error: String(err?.message || err) }, true];
    }
    busy = false;
    render();
  }
  render();
  appUpdateHub().then((found) => {
    if (!found) return;
    ({ hub, role } = found);
    follow(box, render);
    render();
    refreshUpdate(hub);
  }).catch(() => { /* browser */ });
  return box;
}

// ----------------------------------------------------------- Settings › App

/** Settings › App: the desktop app's own settings, here or one click away in its settings window. */
export function appSettingsPanel() {
  const panel = h("section", { class: "settings-about settings-app", "data-app-settings": "" });
  const servers = h("div", { class: "app-settings-servers", "data-app-servers": "" });
  const links = h("div", { class: "settings-about-links app-settings-links" });
  const linkButton = (view, iconName, label, open) => h("button", {
    class: "ghost-btn", type: "button", "data-app-open": view, onclick: () => Promise.resolve(open(view)).catch(() => {}),
  }, icon(iconName, "sm"), label);
  panel.append(
    h("h3", { class: "settings-app-title" }, t("app.title_long")),
    h("p", { class: "settings-lead" }, t("app.lead")),
    aboutUpdateBlock(),
    h("h3", { class: "about-update-title" }, t("app.servers")),
    servers,
    h("h3", { class: "about-update-title" }, t("app.more")),
    links);
  servers.append(h("p", { class: "muted small" }, t("app.servers_hint")));
  appUpdateHub().then(async (found) => {
    if (!found) return;
    const open = appSettingsOpener(found.hub);
    if (open) {
      links.append(
        linkButton("local", "monitor", t("app.open_local"), open),
        linkButton("window", "layers", t("app.open_window"), open),
        linkButton("data", "folder", t("app.open_data"), open));
    }
    if (found.role === "local" && typeof found.hub.servers === "function") {
      let list = [];
      try { list = (await found.hub.servers())?.servers || []; } catch { list = []; }
      servers.replaceChildren(list.length
        ? h("ul", { class: "app-settings-server-list" }, list.slice(0, 8).map((s) => h("li", null,
          icon(s.linked ? "link" : "globe", "sm"), h("span", null, String(s.label || s.url)))))
        : h("p", { class: "muted small" }, t("app.no_servers")));
    } else {
      servers.replaceChildren(h("p", { class: "muted small" }, t("app.servers_remote", { server: location.host })));
    }
    if (open) servers.append(h("div", { class: "settings-actions about-update-actions" },
      h("button", { class: "btn sm", type: "button", "data-app-open": "servers", onclick: () => Promise.resolve(open("servers")).catch(() => {}) },
        icon("settings", "sm"), t("app.manage_servers"))));
  }).catch(() => { /* browser */ });
  return panel;
}

// ------------------------------------------------- Settings overview cards

function overviewCard({ jumpId, data, mark, title, detail, onOpen }) {
  const card = h("button", {
    class: "settings-overview-link settings-about-card", type: "button",
    "data-settings-jump": jumpId, [data]: "", onclick: () => onOpen?.(),
  });
  const render = () => {
    const { text, dot } = detail();
    card.classList.toggle("has-update", dot);
    card.replaceChildren(...[
      mark,
      h("span", null, h("b", null, title), h("small", null, text)),
      dot ? h("span", { class: "update-dot", "aria-hidden": "true" }) : null,
      h("span", { class: "settings-about-card-open" }, t("about.open"), icon("chevronRight", "sm")),
    ].filter(Boolean));
  };
  render();
  appUpdateHub().then((found) => {
    if (!found) return;
    follow(card, render);
    render();
  }).catch(() => { /* browser */ });
  return card;
}

/** The overview's "About ARK Studio" card: version and update state at a glance. */
export function aboutOverviewCard(onOpen) {
  return overviewCard({
    jumpId: "about", data: "data-about-card", title: t("about.card_title"), onOpen,
    mark: h("img", { class: "settings-about-card-mark", src: "./brand/ark-mark.svg", alt: "", width: 28, height: 28 }),
    detail: () => {
      const state = currentState();
      let detail = "";
      if (state.available) detail = t("about.update.new_short", { version: state.available });
      else if (state.kind === "desktop" && state.enabled && state.lastCheck) {
        detail = t("about.last_check_short", { time: relativeTime(new Date(state.lastCheck).toISOString()) });
      } else if (state.kind === "server") detail = t("about.card_server");
      return { text: [versionLabel(state), detail].filter(Boolean).join(" · "), dot: !!state.available };
    },
  });
}

/** The overview's "Settings of this app" card (only inside the desktop app). */
export function appOverviewCard(onOpen) {
  return overviewCard({
    jumpId: "app", data: "data-app-card", title: t("app.card_title"), onOpen,
    mark: h("span", { class: "settings-app-card-mark" }, icon("monitor")),
    detail: () => {
      const state = currentState();
      const update = state.available ? t("about.update.new_short", { version: state.available }) : t("app.card_detail");
      return { text: update, dot: !!state.available };
    },
  });
}

/**
 * The line under the Settings title: whose settings these are.  In the app
 * it also leads to the app's own settings (Settings › App).
 */
export function settingsScopeLine(onOpenApp) {
  const line = h("div", { class: "settings-where", "data-settings-where": "" });
  const render = (local) => {
    const where = local ? t("app.this_computer") : (globalThis.location?.host || "");
    line.replaceChildren(
      h("span", { class: "settings-where-label" }, icon("database", "sm"), t("app.server_settings", { server: where })),
      inDesktopApp() ? h("button", { class: "settings-where-app", type: "button", "data-settings-where-app": "", onclick: () => onOpenApp?.() },
        icon("monitor", "sm"), t("app.app_settings_link"), icon("chevronRight", "sm"))
        : h("span", { class: "settings-where-note" }, t("app.browser_note")));
  };
  render(false);
  appUpdateHub().then((found) => { if (found?.role === "local") render(true); }).catch(() => {});
  return line;
}

// ------------------------------------------------------- activity bar

function syncAboutButton(button, info = latestUpdate()) {
  const update = hasUpdate(info);
  const state = aboutState(info);
  const label = update ? t("about.button_update", { version: state.available }) : t("about.button");
  button.title = label;
  button.setAttribute("aria-label", label);
  button.classList.toggle("has-update", update);
  const dot = button.querySelector(".update-dot");
  if (dot) dot.hidden = !update;
}

let wired = false;
function wireAboutButtons() {
  if (wired) return;
  wired = true;
  onUpdate((info) => {
    for (const button of globalThis.document?.querySelectorAll?.("[data-about-button]") || []) syncAboutButton(button, info);
  });
}

/** The activity bar's About button (bottom, next to Settings): opens Settings › About. */
export function aboutActivityButton(onOpen) {
  wireAboutButtons();
  const button = h("button", {
    class: "wb-act wb-foot-btn about-act-btn", type: "button", "data-about-button": "",
    onclick: () => onOpen?.(),
  }, icon("info", "sm"), h("span", { class: "update-dot", "aria-hidden": "true", hidden: true }));
  syncAboutButton(button);
  return button;
}
