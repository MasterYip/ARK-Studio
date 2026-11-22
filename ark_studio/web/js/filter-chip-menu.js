// A filter chip's menu (right click, long press on touch): Only this,
// Select all, Invert; Alt+click on the chip is Only this.  The group rules
// are in filter-group-model.js; `can(action)` and `run(action)` come from
// the filter rail (components.js, assignee.js) or Needs you's bar.

import { t } from "./i18n.js";
import { openContextMenu } from "./context-menu.js";
import { longPressMenu } from "./session-touch.js";
import { isOnlyThisClick, railGroupCan } from "./filter-group-model.js";

export function chipGroupMenuItems({ can = () => true, run }) {
  return [
    { id: "chip-only", label: t("filters.chip.only"), icon: "filter", shortcut: t("filters.chip.only_click"),
      disabled: !can("only"), onClick: () => run("only") },
    { id: "chip-all", label: t("filters.chip.all"), icon: "check", disabled: !can("all"), onClick: () => run("all") },
    { id: "chip-invert", label: t("filters.chip.invert"), icon: "refresh", disabled: !can("invert"), onClick: () => run("invert") },
  ];
}

/** Give `chip` the group menu and Alt+click; returns the chip. */
export function attachChipGroupMenu(chip, options) {
  if (!options?.run) return chip;
  // Before the chip's own toggle (a capture listener on the target runs first).
  chip.addEventListener("click", (event) => {
    if (!isOnlyThisClick(event)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    if (options.can?.("only") !== false) options.run("only");
  }, true);
  chip.addEventListener("contextmenu", (event) => {
    event.preventDefault();
    event.stopPropagation();
    openContextMenu({ x: event.clientX, y: event.clientY, items: chipGroupMenuItems(options), returnFocus: chip });
  });
  chip.addEventListener("keydown", (event) => {
    if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10")) return;
    event.preventDefault();
    const rect = chip.getBoundingClientRect();
    openContextMenu({ x: rect.left, y: rect.bottom + 4, items: chipGroupMenuItems(options), returnFocus: chip });
  });
  longPressMenu(chip);
  return chip;
}

/**
 * A filter rail chip (components.js, assignee.js) with the group menu, when
 * the rail's context can apply it (ctx.applyFilterGroup).
 */
export function railChip(ctx, chip, group, base, value) {
  if (!ctx?.applyFilterGroup) return chip;
  return attachChipGroupMenu(chip, {
    can: (action) => railGroupCan(ctx.S.filters, group, action, base, value),
    run: (action) => ctx.applyFilterGroup(group, action, base, value),
  });
}
