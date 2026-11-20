// "Assign to ▸" submenu for unit right-click menus (graph/map/mind map and
// the shared row/card menu).  Writes the same single-field PATCH as the
// inspector picker (assignee.js), then refreshes the route and announces the
// save so the inspector and Editor tabs pick it up.
import { api, getMobileToken } from "./api.js";
import { t } from "./i18n.js";
import { cachedAssignees, loadAssignees } from "./assignee.js";
import { announceUnitSaved } from "./unit-edit.js";
import { MOBILE_PERMISSION_STORAGE_KEY } from "./mobile-core.js";
import { assignChoice, assignNames, sameName } from "./assign-model.js";
import { displayName, userCard, userLabel } from "./users.js";

// The app ctx, installed once by app.js; the canvas graph has no ctx of its own.
let appCtx = null;
export function setAssignContext(ctx) { appCtx = ctx; }

/** False for read-only project members and read-only phone sessions. */
export function canAssign(S) {
  if (S?.session?.project_permission === "read") return false;
  try {
    if (getMobileToken() && localStorage.getItem(MOBILE_PERMISSION_STORAGE_KEY) === "read_only") return false;
  } catch { /* storage unavailable */ }
  return true;
}

/** PATCH {assignee} on every unit, then sync caches, toast and refresh. */
export async function assignUnits(ctx, units, name) {
  const saved = [];
  try {
    for (const unit of units) {
      const data = await api.updateUnit(unit.uid || unit.id, { assignee: name || null });
      saved.push(data.unit);
    }
  } catch (err) {
    ctx?.toast?.(err.message || String(err), "err");
  }
  for (const unit of saved) {
    const local = (ctx?.S?.units || []).find((item) => item.id === unit.id);
    if (local) Object.assign(local, unit);
    if (ctx?.S?.detail?.id === unit.id) ctx.S.detail = { ...ctx.S.detail, ...unit };
    announceUnitSaved(unit, "assign");
  }
  if (saved.length) {
    ctx?.toast?.(name ? t("assign.done", { n: saved.length, name: displayName(name) }) : t("assign.cleared", { n: saved.length }), "ok");
    // In place on a graph (no rebuild, relayout or refit); lists re-render.
    if (ctx?.unitsChanged) await ctx.unitsChanged(saved);
    else await ctx?.refresh?.();
  }
  return saved;
}

/**
 * Context-menu item for `units` (one node, or the graph selection).
 * Returns null without a ctx or units; a disabled item for read-only users.
 */
export function assignMenuItem(units, ctx = appCtx) {
  const list = (units || []).filter((unit) => unit && (unit.uid || unit.id));
  if (!ctx || !list.length) return null;
  const label = list.length > 1 ? t("assign.selection", { n: list.length }) : t("assign.menu");
  if (!canAssign(ctx.S)) {
    return { label, icon: "user", disabled: true, title: t("assign.read_only"), hint: t("assign.read_only") };
  }
  const projectId = ctx.S?.project?.id || "";
  const data = cachedAssignees(projectId);
  // Membership can change while the page is open: revalidate for next time.
  loadAssignees(projectId, { refresh: true });
  // Rows in lists only carry uid/title: use the loaded unit when there is one.
  const full = list.map((unit) => ("assignee" in unit ? unit
    : (ctx.S?.units || []).find((item) => item.uid === unit.uid || item.id === unit.id) || unit));
  const choice = assignChoice(full);
  // Only accounts that joined this project (server: assignable_users).
  const me = data?.me || "";
  const users = data?.users || [];
  const inheritedHint = (name) => (name && choice.inheritedFrom && sameName(choice.effective, name)
    ? t("assign.inherited_hint", { title: choice.inheritedFrom }) : "");
  const entry = (name, text) => ({
    label: text, icon: "user",
    checked: choice.known && (name ? sameName(choice.own, name) : !choice.own),
    hint: inheritedHint(name),
    onClick: () => assignUnits(ctx, list, name),
  });
  const { members, former } = assignNames(users, me, Boolean(data?.me_member), choice);
  const children = members.map((name) => {
    if (me && sameName(name, me)) return entry(name, t("assign.me", { name: displayName(name) }));
    const user = users.find((item) => sameName(item.username, name));
    // Listed members come from /api/project/assignees; names from users.js.
    const text = userCard(name) ? userLabel(name)
      : user?.display_name && user.display_name !== user.username
        ? `${user.display_name} (${user.username})` : name;
    return entry(name, text);
  });
  // A current owner who left the project: visible (checked / inherited) but not offered.
  for (const name of former) {
    children.push({ ...entry(name, userLabel(name)), disabled: true, onClick: null,
      hint: [inheritedHint(name), t("assignee.not_member")].filter(Boolean).join(" · ") });
  }
  if (!data) children.push({ label: t("assign.loading"), disabled: true });
  children.push({ separator: true });
  children.push(entry(null, t("assign.none")));
  return { label, icon: "user", children };
}
