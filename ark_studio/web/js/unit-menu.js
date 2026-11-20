// Shared right-click menu for ARK units shown as DOM rows/cards (lists,
// boards, outlines, Gantt rows, dashboards).  Any element carrying
// data-unit-ref="<uid>" gets the menu through one delegated listener; the
// canvas graph builds its own richer menu and adds the same "Open in Editor"
// entry (see graph.js _openGraphContextMenu).

import { t } from "./i18n.js";
import { copyText } from "./dom.js";
import { openContextMenu } from "./context-menu.js";
import { assignMenuItem, setAssignContext } from "./assign-menu.js";
import { installUnitReferences } from "./unit-references.js";
import { exportBundleMenuItem } from "./export-menu.js";
import { historyMenuItem, setHistoryContext } from "./node-history.js";
import { setNodeReveal } from "./node-reveal.js";
import { agentMessageMenuItem } from "./agent-messages.js";

export const OPEN_IN_EDITOR_SHORTCUT = "Alt+Enter";

export function unitEditorSource(unit) {
  const uid = String(unit?.uid || unit?.id || "");
  // Without a known title the Editor keeps the tab's current one (or the uid).
  return unit?.title ? { kind: "unit", uid, title: unit.title } : { kind: "unit", uid };
}

// `reveal` ({ field, line, commentId }) scrolls the node tab to a match.
export function openUnitInEditor(ctx, unit, { preview = false, reveal = null } = {}) {
  const source = unitEditorSource(unit);
  if (!source.uid || !ctx?.openInEditor) return false;
  setNodeReveal(source.uid, reveal);
  ctx.openInEditor(source, { preview });
  return true;
}

// The "Open in Editor" entry, shared by every unit menu.
export function openInEditorItem(ctx, unit) {
  return {
    label: t("editor.open_in_editor"), icon: "edit", shortcut: OPEN_IN_EDITOR_SHORTCUT,
    onClick: () => openUnitInEditor(ctx, unit),
  };
}

export function unitMenuItems(ctx, unit, event = null) {
  const uid = String(unit?.uid || unit?.id || "");
  return [
    {
      label: t("unit_menu.open_details"), icon: "search",
      onClick: () => ctx.openUnit?.(uid, event ? { x: event.clientX, y: event.clientY } : null),
    },
    openInEditorItem(ctx, unit),
    historyMenuItem(unit, ctx),
    { separator: true },
    assignMenuItem([unit], ctx),
    agentMessageMenuItem(ctx, unit), // experimental, null while off
    exportBundleMenuItem([{ uid }]),
    { label: t("unit_menu.copy_uid"), icon: "copy", onClick: () => copyText(uid) },
  ];
}

let installed = false;

export function installUnitContextMenu(ctx) {
  if (installed) return;
  installed = true;
  setAssignContext(ctx);
  setHistoryContext(ctx);
  installUnitReferences(ctx);
  document.addEventListener("contextmenu", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target || target.closest("input, textarea, select, [contenteditable='true']")) return;
    const row = target.closest("[data-unit-ref]");
    const uid = row?.dataset.unitRef;
    if (!uid) return;
    event.preventDefault();
    const title = row.querySelector(".card-title, .t, h3")?.textContent?.trim() || "";
    openContextMenu({ x: event.clientX, y: event.clientY, items: unitMenuItems(ctx, { uid, title }, event) });
  });
  // Double-click a card/row opens the node as a pinned Editor tab; Alt+Enter
  // does the same for a focused row.
  document.addEventListener("dblclick", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target || target.closest("button, a, input, textarea, select")) return;
    const row = target.closest("[data-unit-ref]");
    if (!row?.dataset.unitRef) return;
    openUnitInEditor(ctx, { uid: row.dataset.unitRef });
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || !event.altKey || event.ctrlKey || event.metaKey) return;
    const row = document.activeElement?.closest?.("[data-unit-ref]");
    if (!row?.dataset.unitRef) return;
    event.preventDefault();
    openUnitInEditor(ctx, { uid: row.dataset.unitRef });
  });
}
