// Account ownership of a unit: `assignee` (the ARK username that owns or
// maintains the node) and the server-resolved `assignee_effective` /
// `assignee_from` (inherited from the nearest part_of ancestor).
//
// Not to be confused with `unit.assignees`, the agent/person *units* attached
// through assigned_to links; those keep their own UI.

import { api } from "./api.js";
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { railChip } from "./filter-chip-menu.js";
import { displayName, paintAvatar, registerUserPainter, userCard, userLabel, userName } from "./users.js";

const cache = { project: "", data: null, promise: null };

/** Picker users for the current project: { users: [{username, display_name}], me }. */
export function loadAssignees(projectId = "", { refresh = false } = {}) {
  if (cache.project === projectId && cache.promise) return cache.promise;
  if (cache.project === projectId && cache.data && !refresh) return Promise.resolve(cache.data);
  // refresh: fetch again but keep serving the old list until the reply lands.
  if (cache.project !== projectId) cache.data = null;
  cache.project = projectId;
  cache.promise = api.assignees()
    .catch(() => (cache.project === projectId && cache.data) || { users: [], me: "" })
    .then((data) => { if (cache.project === projectId) cache.data = data; return data; })
    .finally(() => { if (cache.project === projectId) cache.promise = null; });
  return cache.promise;
}

export function cachedAssignees(projectId = "") {
  return cache.project === projectId ? cache.data : null;
}

function hue(name = "") {
  let n = 0;
  for (const ch of String(name).toLowerCase()) n = (n * 31 + ch.codePointAt(0)) % 360;
  return n;
}

/** Plain-text assignee for read-only views: own, inherited or unassigned. */
export function assigneeLabel(unit) {
  const name = unit?.assignee_effective || unit?.assignee || "";
  if (!name) return t("assignee.none");
  const shown = displayName(name);
  if (!unit.assignee && unit.assignee_from) {
    return `${shown} · ${t("assignee.inherited_from", { title: unit.assignee_from.title || unit.assignee_from.uid })}`;
  }
  return shown;
}

// Title + avatar for a chip; re-run in place when the user directory changes
// (a profile save renames every visible chip without a reload).
function paintAssigneeChip(el) {
  const name = el.dataset.assignee || "";
  const label = t("assignee.chip_title", { name: userLabel(name) });
  const from = el.dataset.assigneeFrom || "";
  const title = from ? `${label} · ${t("assignee.inherited_from", { title: from })}` : label;
  el.title = title;
  el.setAttribute("aria-label", title);
  paintAvatar(el, name);
}
registerUserPainter(".assignee-chip[data-assignee]", paintAssigneeChip);

/** Small initials chip for the effective assignee, or null when unassigned. */
export function assigneeChip(unit) {
  const name = unit?.assignee_effective || unit?.assignee || "";
  if (!name) return null;
  const inherited = !unit.assignee && Boolean(unit.assignee_from);
  const el = h("span", {
    class: `assignee-chip ${inherited ? "inherited" : ""}`.trim(),
    style: { "--assignee-hue": String(hue(name)) },
    dataset: {
      assignee: name,
      assigneeFrom: inherited ? String(unit.assignee_from.title || unit.assignee_from.uid || "") : "",
    },
  });
  paintAssigneeChip(el);
  return el;
}

/**
 * Assignee picker: the project's users plus "Unassigned".  Saves on change
 * with a single-field PATCH (not through the text-draft autosave), then calls
 * onSaved(unit); by default it syncs caches and refreshes the data in place
 * (ctx.unitsChanged: lists re-render, a graph is patched, never rebuilt),
 * because reassigning a branch root changes what its descendants inherit.
 */
export function assigneePicker(ctx, unit, { readOnly = false, onSaved = null } = {}) {
  const projectId = ctx?.S?.project?.id || "";
  const wrap = h("div", { class: "assignee-picker" });
  const select = h("select", { class: "input", "aria-label": t("assignee.label"), disabled: readOnly ? true : undefined });
  const hint = h("div", { class: "assignee-hint" });
  wrap.append(select, hint);

  const paint = (data) => {
    const own = unit.assignee || "";
    const effective = unit.assignee_effective || "";
    const inherited = !own && effective;
    // Project members only; a current owner who left stays visible (marked) so it can be cleared.
    const users = [...(data?.users || [])];
    for (const name of [own, effective]) {
      if (name && !users.some((u) => u.username.toLowerCase() === name.toLowerCase())) {
        users.push({ username: name, display_name: name, former: true });
      }
    }
    select.replaceChildren(
      h("option", { value: "" }, inherited
        ? `${userLabel(effective)} · ${t("assignee.inherited_short")}`
        : t("assignee.none")),
      // Who is listed comes from /api/project/assignees; how they are named
      // comes from the user directory (display name, username secondary).
      ...users.map((u) => h("option", {
        value: u.username, dataset: { userOption: u.former ? undefined : u.username },
        disabled: u.former && u.username !== own ? true : undefined,
      }, u.former ? `${userLabel(u.username)} · ${t("assignee.not_member")}`
        : userCard(u.username) ? userLabel(u.username)
          : (u.display_name && u.display_name !== u.username ? `${u.display_name} (${u.username})` : u.username))));
    select.value = own;
    hint.textContent = inherited && unit.assignee_from
      ? t("assignee.inherited_from", { title: unit.assignee_from.title || unit.assignee_from.uid })
      : "";
    hint.hidden = !hint.textContent;
  };
  paint(cachedAssignees(projectId));
  loadAssignees(projectId).then(paint);

  select.addEventListener("change", async () => {
    const next = select.value || null;
    select.disabled = true;
    try {
      const data = await api.updateUnit(unit.id, { assignee: next });
      Object.assign(unit, {
        assignee: data.unit.assignee, assignee_effective: data.unit.assignee_effective,
        assignee_from: data.unit.assignee_from, updated_at: data.unit.updated_at,
      });
      paint(cachedAssignees(projectId));
      if (onSaved) onSaved(data.unit);
      else if (ctx?.unitsChanged) {
        // In place: a graph is patched, not rebuilt (app.js refreshData).
        ctx.unitsChanged([{ ...(ctx.S?.detail?.id === unit.id ? ctx.S.detail : {}), ...data.unit }]);
      } else {
        const local = (ctx?.S?.units || []).find((item) => item.id === unit.id);
        if (local) Object.assign(local, data.unit);
        if (ctx?.S?.detail?.id === unit.id) ctx.S.detail = { ...ctx.S.detail, ...data.unit };
        ctx?.refresh?.();
      }
    } catch (err) {
      select.value = unit.assignee || "";
      ctx?.toast?.(err.message || String(err), "err");
    } finally {
      select.disabled = readOnly;
    }
  });
  return wrap;
}

/** Filter rail group: Me / Unassigned / each user (OR within the group). */
export function assigneeFilterSection(ctx, facets = {}) {
  const S = ctx.S;
  const selected = new Set(S.filters.assignees || []);
  const me = S.session?.user || "";
  const counts = facets.assignees || {};
  const names = new Set(Object.keys(counts));
  const loaded = cachedAssignees(S.project?.id || "");
  for (const user of loaded?.users || []) names.add(user.username);
  // Assignees who left the project still filter, marked as no longer members.
  const member = (name) => !loaded || (loaded.users || []).some((u) => u.username.toLowerCase() === name.toLowerCase());
  const base = [];
  const chip = (value, label, count, extra = null) => {
    const on = selected.has(value);
    base.push(value);
    // The group menu (filter-chip-menu.js) reads `base` once every chip is drawn.
    return railChip(ctx, h("button", {
      type: "button", class: `type-chip ${on ? "on" : ""}`.trim(), "aria-pressed": String(on),
      dataset: { assigneeFilter: value, filterGroup: "assignees", value },
      onclick: () => ctx.toggleFilterValue("assignees", value),
    }, extra, label, h("span", { class: "n" }, count ?? 0)), "assignees", base, value);
  };
  const wrap = h("div", { class: "type-filter assignee-filter" });
  if (me) wrap.appendChild(chip("me", t("assignee.me"), counts[me] ?? 0, assigneeChip({ assignee: me })));
  wrap.appendChild(chip("none", t("assignee.none"), facets.unassigned ?? 0));
  for (const name of [...names].sort((a, b) => displayName(a).localeCompare(displayName(b)))) {
    if (name === me) continue;
    const left = !member(name);
    const button = chip(name, left ? h("span", null, userName(name), ` · ${t("assignee.not_member")}`) : userName(name),
      counts[name] ?? 0, assigneeChip({ assignee: name }));
    if (left) { button.classList.add("former-member"); button.title = t("assignee.not_member"); }
    wrap.appendChild(button);
  }
  const section = h("div", { class: "rail-section assignee-section" },
    h("div", { class: "rail-label", title: t("assignee.filter_hint") }, t("assignee.label")), wrap);
  if (!cachedAssignees(S.project?.id || "")) {
    loadAssignees(S.project?.id || "").then(() => {
      if (section.isConnected) section.replaceWith(assigneeFilterSection(ctx, facets));
    });
  }
  return section;
}
