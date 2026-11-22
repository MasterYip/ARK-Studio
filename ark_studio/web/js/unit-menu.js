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
import { installMovePlanLinks, projectSplitMenuItem } from "./project-move.js";
import { isLocateShortcut, locateInMapItem, locateUnit } from "./locate-map.js";

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
      id: "open-details", label: t("unit_menu.open_details"), icon: "search",
      onClick: () => ctx.openUnit?.(uid, event ? { x: event.clientX, y: event.clientY } : null),
    },
    openInEditorItem(ctx, unit),
    locateInMapItem(unit, ctx),
    historyMenuItem(unit, ctx),
    { separator: true },
    assignMenuItem([unit], ctx),
    agentMessageMenuItem(ctx, unit), // experimental, null while off
    exportBundleMenuItem([{ uid }]),
    projectSplitMenuItem(ctx, unit),
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
  installMovePlanLinks(ctx);
  document.addEventListener("contextmenu", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target || target.closest("input, textarea, select, [contenteditable='true']")) return;
    const row = target.closest("[data-unit-ref]");
    const uid = row?.dataset.unitRef;
    if (!uid) return;
    event.preventDefault();
    openContextMenu({ x: event.clientX, y: event.clientY, items: unitMenuItems(ctx, rowUnit(row), event) });
  });
  // Phones: a long press on a row or card raises the same menu.
  installLongPress();
  // Double-click a card/row opens the node as a pinned Editor tab; Alt+Enter
  // does the same for a focused row.
  document.addEventListener("dblclick", (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target || target.closest("button, a, input, textarea, select")) return;
    const row = target.closest("[data-unit-ref]");
    if (!row?.dataset.unitRef) return;
    openUnitInEditor(ctx, { uid: row.dataset.unitRef });
  });
  // Alt+M (Settings › Shortcuts): locate the focused row's node, else the
  // node open in the inspector, in the research map.  Canvas graphs and the
  // command palette handle the key themselves.
  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || !isLocateShortcut(event)) return;
    const target = event.target instanceof Element ? event.target : null;
    if (target?.closest("input, textarea, select, [contenteditable='true'], .graph-focusable")) return;
    const row = document.activeElement?.closest?.("[data-unit-ref]");
    const unit = row ? rowUnit(row) : ctx.S?.drawerOpen && ctx.S.detail?.uid ? ctx.S.detail : null;
    if (unit && locateUnit(unit, ctx)) event.preventDefault();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" || !event.altKey || event.ctrlKey || event.metaKey) return;
    const row = document.activeElement?.closest?.("[data-unit-ref]");
    if (!row?.dataset.unitRef) return;
    event.preventDefault();
    openUnitInEditor(ctx, { uid: row.dataset.unitRef });
  });
}

// What a [data-unit-ref] element says about its node: a review group card
// (data-review-group) is not a unit.
function rowUnit(row) {
  const uid = row.dataset.unitRef;
  const title = row.querySelector(".card-title, .t, h3")?.textContent?.trim() || "";
  return { uid, title, ...(row.dataset.reviewGroup ? { reviewGroup: row.dataset.reviewGroup } : {}) };
}

// A touch long press (500 ms without moving) on a unit row raises its menu;
// the browser's own long-press menu and the tap that ends the press are not
// doubled (as session-touch.js does for one element).
function installLongPress() {
  const LONG_PRESS_MS = 500;
  let timer = 0, start = null, fired = 0;
  const cancel = () => { clearTimeout(timer); timer = 0; start = null; };
  document.addEventListener("pointerdown", (event) => {
    cancel();
    if (event.pointerType === "mouse") return;
    const target = event.target instanceof Element ? event.target : null;
    const row = target?.closest("[data-unit-ref]");
    if (!row || target.closest("input, textarea, select, [contenteditable='true'], canvas")) return;
    start = { x: event.clientX, y: event.clientY };
    timer = setTimeout(() => {
      timer = 0;
      fired = performance.now();
      row.dispatchEvent(new MouseEvent("contextmenu", { clientX: start.x, clientY: start.y, bubbles: true, cancelable: true }));
    }, LONG_PRESS_MS);
  }, true);
  document.addEventListener("pointermove", (event) => {
    if (start && Math.hypot(event.clientX - start.x, event.clientY - start.y) > 10) cancel();
  }, true);
  for (const type of ["pointerup", "pointercancel"]) document.addEventListener(type, () => { clearTimeout(timer); timer = 0; }, true);
  document.addEventListener("contextmenu", (event) => {
    if (event.isTrusted && performance.now() - fired < 800) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  document.addEventListener("click", (event) => {
    // Only the tap that ends the press: the menu it opened stays usable.
    if (event.target instanceof Element && event.target.closest(".graph-menu")) return;
    if (performance.now() - fired < 800) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
}
