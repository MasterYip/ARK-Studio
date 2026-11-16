import { h, clear } from "./dom.js";
import { icon } from "./icons.js";

let active = null;

export function closeContextMenu() {
  if (!active) return;
  active.remove();
  active = null;
  document.removeEventListener("pointerdown", onOutside, true);
  document.removeEventListener("keydown", onKey, true);
  window.removeEventListener("resize", closeContextMenu);
  window.removeEventListener("scroll", onScroll, true);
}

function onOutside(event) {
  if (active && !active.contains(event.target)) closeContextMenu();
}

// Scrolling the page closes the menu; scrolling inside a long menu does not.
function onScroll(event) {
  if (active && event.target instanceof Node && active.contains(event.target)) return;
  closeContextMenu();
}

function onKey(event) {
  if (event.key === "Escape") {
    event.preventDefault();
    closeContextMenu();
  }
}

function itemButton(item, close) {
  const hasChildren = Array.isArray(item.children) && item.children.length;
  const button = h("button", {
    class: `graph-menu-item ${item.danger ? "danger" : ""}`.trim(),
    type: "button", role: item.checked !== undefined ? "menuitemradio" : "menuitem", disabled: item.disabled,
    title: item.title || undefined,
    "aria-checked": item.checked !== undefined ? String(Boolean(item.checked)) : undefined,
    onclick: (event) => {
      event.stopPropagation();
      if (hasChildren) return;
      close();
      item.onClick?.();
    },
    onmouseenter: (event) => {
      const menu = event.currentTarget.closest(".graph-menu");
      menu?.querySelectorAll(".graph-menu-sub").forEach((sub) => sub.remove());
      if (!hasChildren) return;
      const sub = buildMenu(item.children, closeContextMenu);
      sub.classList.add("graph-menu-sub");
      event.currentTarget.appendChild(sub);
      const rect = event.currentTarget.getBoundingClientRect();
      const subRect = sub.getBoundingClientRect();
      if (rect.right + subRect.width > window.innerWidth - 4) sub.style.right = "100%";
      if (rect.top + subRect.height > window.innerHeight - 4) sub.style.top = "auto";
    },
  },
    // `checked` (radio-style submenus) shows a check mark in the icon slot.
    item.checked ? icon("check", "sm") : item.checked === false ? h("span", { class: "graph-menu-dot" })
      : item.icon ? icon(item.icon, "sm") : h("span", { class: "graph-menu-dot" }),
    h("span", { class: "graph-menu-label" }, item.label,
      item.hint ? h("span", { class: "graph-menu-hint" }, ` · ${item.hint}`) : null),
    item.shortcut ? h("kbd", null, item.shortcut) : null,
    hasChildren ? h("span", { class: "graph-menu-arrow" }, "›") : null);
  return button;
}

function buildMenu(items, onDone) {
  const menu = h("div", { class: "graph-menu", role: "menu" });
  for (const item of items || []) {
    if (!item) continue;
    if (item.heading) {
      menu.appendChild(h("div", { class: "graph-menu-heading" }, item.heading));
      continue;
    }
    if (item.separator) {
      menu.appendChild(h("div", { class: "graph-menu-sep", role: "separator" }));
      continue;
    }
    menu.appendChild(itemButton(item, onDone || closeContextMenu));
  }
  return menu;
}

export function openContextMenu({ x, y, items, className = "" }) {
  closeContextMenu();
  const menu = buildMenu(items, closeContextMenu);
  if (className) menu.classList.add(...className.split(/\s+/).filter(Boolean));
  menu.style.left = `${Math.max(4, Math.min(x, window.innerWidth - 250))}px`;
  menu.style.top = `${Math.max(4, y)}px`;
  document.body.appendChild(menu);
  const rect = menu.getBoundingClientRect();
  if (rect.bottom > window.innerHeight - 4) menu.style.top = `${Math.max(4, y - rect.height)}px`;
  if (rect.right > window.innerWidth - 4) menu.style.left = `${Math.max(4, x - rect.width)}px`;
  active = menu;
  document.addEventListener("pointerdown", onOutside, true);
  document.addEventListener("keydown", onKey, true);
  window.addEventListener("resize", closeContextMenu);
  window.addEventListener("scroll", onScroll, true);
  menu.querySelector("button:not([disabled])")?.focus();
  return closeContextMenu;
}
