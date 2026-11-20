// Project picker cards (T-0034), shared by the project menu (app.js) and the
// "Open another project…" dialog (components.js openProjectModal).
//
// Projects are cards in a responsive grid, ordered by project-picker-model.js
// (pins, then the person's own recent activity).  Right-click a card, or
// press the context-menu key / Shift+F10 on it, for Open, Pin to top /
// Unpin and Hide / Unhide; hidden projects sit in a collapsed "Hidden (N)"
// section under the grid.  Arrow keys move across the grid, Enter opens.
// Pins, hidden projects and the "Created by me" filter follow the account
// (PATCH /api/me prefs.projects); browser storage is the cache and the
// fallback.  Each card names its creator (left of the footer) and who made
// the latest change (right), from GET /api/projects creator / last_actor.

import { h, clear, relativeTime } from "./dom.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { openContextMenu } from "./context-menu.js";
import { currentUsername, displayName, userAvatar, userColor } from "./users.js";
import { presenceInitials } from "./presence-model.js";
import {
  arrangeProjects, createProjectPrefsStore, togglePinned, toggleHidden, setOnlyMine, gridMove, columnsFromTops,
  refreshedProjectRows, cardPeopleText,
} from "./project-picker-model.js";

export {
  arrangeProjects, withKnownActivity, normalizeProjectPrefs, refreshedProjectRows, PROJECT_PREFS_KEY,
} from "./project-picker-model.js";

function browserStorage() {
  try { return window.localStorage; } catch { return null; }
}

let reportError = null;

export const projectPrefs = createProjectPrefsStore({
  storage: typeof window === "undefined" ? null : browserStorage(),
  save: async (value) => {
    const data = await api.updateMe({ prefs: { projects: value } });
    return data?.profile?.prefs?.projects || {};
  },
  onError: (err) => reportError?.(err),
});

/** Called once at boot with the bootstrap profile (like initLayoutPresets). */
export function initProjectPrefs(profile, { toast = null } = {}) {
  reportError = toast ? (err) => toast(err?.message || String(err), "err") : null;
  projectPrefs.init(profile);
}

// The "Hidden (N)" section stays open while the page lives once opened.
let hiddenOpen = false;

function openProject(ctx, project, beforeOpen) {
  if (ctx.switchProject) { ctx.switchProject(project, { beforeOpen }); return; }
  api.openProject({ id: project.id }).then((data) => {
    beforeOpen?.(project);
    ctx.applyBootstrap(data);
    ctx.toast?.(t("project.toast.opened", { name: project.name }), "ok");
  }).catch((err) => ctx.toast?.(err.message, "err"));
}

function peopleText(project) {
  return cardPeopleText(project, {
    t, when: relativeTime, me: currentUsername(),
    name: (user, fallback) => { const shown = displayName(user); return shown && shown !== user ? shown : fallback || user; },
  });
}

/** Small avatar: the account's (initials or picture), or the agent's initials. */
function personAvatar(person) {
  if (!person.agent) return userAvatar(person.user, { size: "xs", title: false });
  const el = h("span", { class: "user-avatar xs project-card-agent", "aria-hidden": "true" }, presenceInitials(person.agent.split("/").pop()));
  el.style.setProperty("--user-color", userColor(person.agent));
  return el;
}

/**
 * The picker: `el` (cards + hidden section), `fill(projects)` to show a list
 * in picker order and `refresh(projects)` for a background reload (cards keep
 * their places).  `beforeOpen` runs when a card opens its project.
 */
export function projectPicker(ctx, { beforeOpen = null, label = "" } = {}) {
  const grid = h("div", { class: "project-grid", role: "group", "aria-label": label || t("app.menu.projects") });
  const hiddenGrid = h("div", { class: "project-grid project-grid-hidden", role: "group" });
  const toggle = h("button", {
    class: "project-hidden-toggle", type: "button", "aria-expanded": "false",
    onclick: () => { hiddenOpen = !hiddenOpen; syncHidden(); },
  });
  const hiddenSection = h("div", { class: "project-hidden", hidden: true }, toggle, hiddenGrid);
  const onlyMine = h("input", {
    type: "checkbox",
    onchange: (event) => { change(null, (prefs) => setOnlyMine(prefs, event.target.checked)); },
  });
  const toolbar = h("div", { class: "project-picker-toolbar", hidden: true },
    h("label", { class: "filter-check project-only-mine" }, onlyMine, h("span", null, t("project.picker.only_mine"))));
  const el = h("div", { class: "project-picker-body" }, toolbar, grid, hiddenSection,
    h("div", { class: "project-picker-hint" }, t("project.picker.hint")));
  let projects = [];
  let shown = [], hidden = [];

  function syncHidden() {
    hiddenSection.hidden = !hidden.length;
    toggle.setAttribute("aria-expanded", String(hiddenOpen));
    clear(toggle);
    toggle.append(icon(hiddenOpen ? "chevron" : "chevronRight", "sm"),
      h("span", null, t("project.picker.hidden", { n: hidden.length })));
    hiddenGrid.hidden = !hiddenOpen;
  }

  function card(project, { isHidden = false } = {}) {
    const current = project.id === ctx.S.project?.id;
    const pinned = projectPrefs.get().pinned.includes(project.id);
    const detail = project.description || project.path || "";
    const { creator, time: when, people } = peopleText(project);
    const button = h("button", {
      class: ["project-card", current ? "current" : "", pinned ? "pinned" : "", isHidden ? "is-hidden" : ""].filter(Boolean).join(" "),
      type: "button", "data-project-id": project.id, title: project.path || project.name || "",
      "aria-current": current ? "true" : undefined,
      "aria-haspopup": "menu",
      onclick: () => openProject(ctx, project, beforeOpen),
      oncontextmenu: (event) => {
        event.preventDefault();
        event.stopPropagation();
        openMenu(project, event.clientX, event.clientY, event.currentTarget);
      },
    },
      h("span", { class: "project-card-head" },
        h("span", { class: `project-dot ${project.demo ? "demo" : ""}`.trim() }),
        h("span", { class: "project-card-name" }, project.name || project.id),
        pinned ? h("span", { class: "project-card-pin", title: t("project.picker.pinned"), "aria-label": t("project.picker.pinned") },
          icon("pin", "sm")) : null),
      h("span", { class: "project-card-desc" }, detail),
      h("span", { class: "project-card-foot" },
        project.demo ? h("span", { class: "demo-badge" }, t("common.demo")) : null,
        current ? h("span", { class: "pill" }, t("project.current")) : null,
        creator ? h("span", { class: "project-card-creator", title: creator }, creator) : null,
        h("span", { class: "project-card-time", title: when },
          people.updater ? personAvatar(people.updater) : null,
          h("span", { class: "project-card-time-text" }, when))));
    return button;
  }

  function render() {
    const focused = el.contains(document.activeElement) ? document.activeElement?.dataset?.projectId || "" : "";
    clear(grid);
    clear(hiddenGrid);
    const filtered = projectPrefs.get().only_mine === true;
    // The filter is offered once the server names creators (created_by_me).
    toolbar.hidden = !projects.some((p) => p && "created_by_me" in p);
    onlyMine.checked = filtered && !toolbar.hidden;
    if (!projects.length) grid.appendChild(h("div", { class: "empty sm" }, t("project.empty")));
    else if (filtered && !shown.length && !hidden.length) grid.appendChild(h("div", { class: "empty sm" }, t("project.picker.none_mine")));
    else if (!shown.length) grid.appendChild(h("div", { class: "empty sm" }, t("project.picker.all_hidden")));
    grid.append(...shown.map((p) => card(p)));
    hiddenGrid.append(...hidden.map((p) => card(p, { isHidden: true })));
    syncHidden();
    if (focused) focusCard(focused);
  }

  function focusCard(id) {
    const target = [...el.querySelectorAll(".project-card")].find((c) => c.dataset.projectId === id);
    if (target && !target.closest("[hidden]")) target.focus();
  }

  function fill(list) {
    projects = Array.isArray(list) ? list : [];
    ({ shown, hidden } = arrangeProjects(projects, projectPrefs.get()));
    render();
  }

  function refresh(list) {
    if (!Array.isArray(list)) return;
    const next = arrangeProjects(list, projectPrefs.get());
    const a = refreshedProjectRows(shown, next.shown);
    const b = refreshedProjectRows(hidden, next.hidden);
    projects = list;
    if (!a && !b) return;
    shown = a || shown;
    hidden = b || hidden;
    render();
  }

  async function change(project, fn) {
    // The user moved the card on purpose: re-sort at once.
    await projectPrefs.update((prefs) => fn(prefs, project?.id)).catch(() => {});
    if (!el.isConnected) return;
    if (!project) { fill(projects); onlyMine.focus(); return; }
    if (fn === toggleHidden && projectPrefs.get().hidden.includes(project.id)) {
      // Keep keyboard focus in the grid: on the next card, or the toggle.
      const index = shown.findIndex((p) => p.id === project.id);
      fill(projects);
      const cards = [...grid.querySelectorAll(".project-card")];
      (cards[Math.min(index, cards.length - 1)] || toggle).focus();
      return;
    }
    fill(projects);
    focusCard(project.id);
  }

  function openMenu(project, x, y, from = null) {
    const prefs = projectPrefs.get();
    const pinned = prefs.pinned.includes(project.id);
    const isHidden = prefs.hidden.includes(project.id);
    openContextMenu({
      x, y, className: "project-card-menu", returnFocus: from,
      items: [
        { heading: project.name || project.id },
        { label: t("action.open"), icon: "folder", onClick: () => openProject(ctx, project, beforeOpen) },
        { separator: true },
        { label: t(pinned ? "project.picker.unpin" : "project.picker.pin"), icon: "pin",
          onClick: () => change(project, togglePinned) },
        { label: t(isHidden ? "project.picker.unhide" : "project.picker.hide"), icon: isHidden ? "eye" : "eyeOff",
          onClick: () => change(project, toggleHidden) },
      ],
    });
  }

  el.addEventListener("keydown", (event) => {
    const target = event.target.closest?.(".project-card");
    if (!target || !el.contains(target)) return;
    const project = [...shown, ...hidden].find((p) => p.id === target.dataset.projectId);
    if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
      if (!project) return;
      event.preventDefault();
      event.stopPropagation();
      const rect = target.getBoundingClientRect();
      openMenu(project, rect.left + 16, rect.top + Math.min(rect.height, 40), target);
      return;
    }
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const cards = [...target.parentElement.querySelectorAll(":scope > .project-card")];
    const next = gridMove(cards.indexOf(target), event.key, cards.length, columnsFromTops(cards.map((c) => c.offsetTop)));
    if (next < 0) return;
    event.preventDefault();
    event.stopPropagation();
    cards[next].focus();
    cards[next].scrollIntoView?.({ block: "nearest" });
  });

  return { el, grid, fill, refresh, focusCurrent: () => (grid.querySelector(".project-card.current") || grid.querySelector(".project-card")) };
}

/**
 * Picker filled from the known list at once and refreshed from GET
 * /api/projects in the background (which also brings the person's activity).
 */
export function liveProjectPicker(ctx, options = {}) {
  const picker = projectPicker(ctx, options);
  picker.fill(ctx.S.projects || []);
  api.projects().then((data) => {
    if (!picker.el.isConnected || !Array.isArray(data?.projects)) return;
    ctx.S.projects = data.projects;
    picker.refresh(data.projects);
  }).catch(() => { /* keep the cached list */ });
  return picker;
}
