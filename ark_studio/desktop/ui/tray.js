// ARK Studio desktop: the tray window (Windows; src/tray_popup.rs). Renders
// the entries from get_state, reports the card's size so the app can place
// the window at the tray, and runs the chosen entry. Escape or clicking
// elsewhere closes it; arrow keys, Home/End, Enter and Space work like a menu.
import { t, applyI18n } from "./i18n.js";
import { trayModel } from "./tray-model.js";

const invoke = (cmd, args = {}) => window.__TAURI_INTERNALS__.invoke(cmd, args);
const card = document.getElementById("tray");
const MARGIN = 8; // tray.css: the card's offset inside the transparent window

const ICONS = {
  window: '<rect x="2.5" y="3.5" width="11" height="9" rx="1.5"/><path d="M2.5 6h11"/>',
  download: '<path d="M8 2.5v7.5M4.8 7 8 10.2 11.2 7M3 13h10"/>',
  server: '<rect x="2.5" y="2.5" width="11" height="4.5" rx="1.2"/><rect x="2.5" y="9" width="11" height="4.5" rx="1.2"/><path d="M5 4.75h.01M5 11.25h.01"/>',
  plus: '<path d="M8 3.5v9M3.5 8h9"/>',
  external: '<path d="M9.5 2.5h4v4M13.5 2.5 7.5 8.5M12 9.5v3a1 1 0 0 1-1 1H3.5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h3"/>',
  play: '<path d="M5 3.2v9.6L12.5 8z"/>',
  stop: '<rect x="4" y="4" width="8" height="8" rx="1.2"/>',
  settings: '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.8v1.7M8 12.5v1.7M1.8 8h1.7M12.5 8h1.7M3.6 3.6l1.2 1.2M11.2 11.2l1.2 1.2M3.6 12.4l1.2-1.2M11.2 4.8l1.2-1.2"/>',
  folder: '<path d="M2 4.5a1 1 0 0 1 1-1h3.2l1.5 1.5H13a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1z"/>',
  log: '<path d="M4 2.5h5.5L12 5v8.5H4z"/><path d="M6 7.5h4M6 10h4"/>',
  power: '<path d="M8 2v5.5"/><path d="M4.6 4.4a5 5 0 1 0 6.8 0"/>',
};

function icon(name) {
  return `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[name] || ""}</svg>`;
}

function el(tag, cls, text) {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text != null) node.textContent = text;
  return node;
}

function entries() {
  return [...card.querySelectorAll(".tray-item")];
}

function choose(id) {
  invoke("tray_menu_action", { id }).catch(() => {});
}

function render(state) {
  const { head, items } = trayModel(state, t);
  card.replaceChildren();
  const top = el("div", "tray-head");
  const mark = el("img");
  mark.src = "brand/ark-mark.svg";
  mark.alt = "";
  const text = el("div", "tray-head-text");
  const name = el("div", "tray-name", "ARK Studio");
  if (head.version) name.append(el("small", null, head.version));
  const status = el("div", `tray-status${head.running ? " is-running" : ""}`);
  status.append(el("i"), el("span", null, `${t("local.title")} · ${head.status}`));
  text.append(name, status);
  top.append(mark, text);
  card.append(top, el("div", "tray-sep"));
  for (const entry of items) {
    if (entry.kind === "sep") {
      const line = el("div", "tray-sep");
      line.setAttribute("role", "separator");
      card.append(line);
    } else if (entry.kind === "group") {
      card.append(el("div", "tray-group", entry.label));
    } else {
      const button = el("button", `tray-item${entry.tone ? ` is-${entry.tone}` : ""}`);
      button.type = "button";
      button.setAttribute("role", "menuitem");
      button.tabIndex = -1;
      button.innerHTML = icon(entry.icon);
      button.append(el("span", null, entry.label));
      button.title = entry.label;
      button.addEventListener("click", () => choose(entry.id));
      button.addEventListener("pointermove", () => {
        if (document.activeElement !== button) button.focus({ preventScroll: true });
      });
      card.append(button);
    }
  }
}

async function refresh() {
  let state = null;
  try {
    state = await invoke("get_state");
  } catch {
    state = null;
  }
  render(state);
  card.focus({ preventScroll: true });
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  const box = card.getBoundingClientRect();
  await invoke("tray_menu_ready", { width: Math.ceil(box.width) + 2 * MARGIN, height: Math.ceil(box.height) + 2 * MARGIN });
}

document.addEventListener("keydown", (e) => {
  const list = entries();
  const at = list.indexOf(document.activeElement);
  const go = (i) => list[(i + list.length) % list.length]?.focus({ preventScroll: true });
  if (e.key === "Escape") choose("dismiss");
  else if (e.key === "ArrowDown") go(at + 1);
  else if (e.key === "ArrowUp") go(at < 0 ? list.length - 1 : at - 1);
  else if (e.key === "Home") go(0);
  else if (e.key === "End") go(list.length - 1);
  else return;
  e.preventDefault();
});
card.addEventListener("pointerleave", () => card.focus({ preventScroll: true }));
document.addEventListener("contextmenu", (e) => e.preventDefault());

window.__arkTrayRefresh = refresh;
applyI18n();
refresh();
