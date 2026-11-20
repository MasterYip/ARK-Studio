import { h } from "./dom.js";
import { icon } from "./icons.js";
import { t } from "./i18n.js";
import { MOBILE_BREAKPOINT } from "./mobile-core.js";

const THRESHOLD = 72;
const SLOP = 6;
const EXCLUDED = "canvas, input, textarea, select, [contenteditable], [role='slider'], .modal-backdrop, .drawer";

export function installPullToSync(root, sync, enabled) {
  const doc = root.ownerDocument;
  const win = doc.defaultView;
  const phone = win.matchMedia(`(max-width: ${MOBILE_BREAKPOINT}px)`);
  const label = h("span");
  const indicator = h("div", { class: "pull-to-sync", role: "status" }, icon("refresh", "sm"), label);
  indicator.hidden = true;
  doc.body.append(indicator);
  let gesture = null;
  let syncing = false;

  function reset() {
    gesture = null;
    if (!syncing) indicator.hidden = true;
  }

  function available() {
    return phone.matches && enabled() && !doc.documentElement.classList.contains("keyboard-open")
      && !doc.querySelector(".modal-backdrop");
  }

  function atTop(target) {
    if (doc.scrollingElement?.scrollTop > 0) return false;
    for (let element = target; element; element = element.parentElement) {
      if (element.scrollTop > 0) return false;
      if (element === root) break;
    }
    return true;
  }

  root.addEventListener("touchstart", (event) => {
    reset();
    const target = event.target;
    if (syncing || !available() || event.touches.length !== 1 || event.defaultPrevented
      || target.closest?.(EXCLUDED) || !atTop(target)) return;
    const touch = event.touches[0];
    gesture = { id: touch.identifier, x: touch.clientX, y: touch.clientY, hash: win.location.hash, target, owned: false, armed: false };
  }, { passive: true });

  root.addEventListener("touchmove", (event) => {
    if (!gesture) return;
    const touch = event.touches[0];
    if (!available() || win.location.hash !== gesture.hash || event.touches.length !== 1
      || touch.identifier !== gesture.id || event.defaultPrevented || !event.cancelable
      || !atTop(gesture.target)) return reset();
    const dx = Math.abs(touch.clientX - gesture.x);
    const dy = touch.clientY - gesture.y;
    if (!gesture.owned) {
      if (Math.max(dx, Math.abs(dy)) < SLOP) return;
      if (dy < SLOP || dy < dx * 1.25) return reset();
      gesture.owned = true;
    }
    event.preventDefault();
    gesture.armed = dy >= THRESHOLD;
    indicator.hidden = dy < SLOP;
    indicator.classList.toggle("armed", gesture.armed);
    indicator.style.setProperty("--pull-offset", `${Math.min(16, dy / 5)}px`);
  }, { passive: false });

  root.addEventListener("touchend", (event) => {
    const release = gesture?.armed && event.touches.length === 0
      && Array.from(event.changedTouches).some((touch) => touch.identifier === gesture.id
        && touch.clientY - gesture.y >= THRESHOLD)
      && win.location.hash === gesture.hash && available() && atTop(gesture.target);
    reset();
    if (!release) return;
    const run = sync();
    if (!run) return;
    syncing = true;
    label.textContent = t("cache.syncing");
    indicator.classList.add("syncing");
    indicator.hidden = false;
    const finish = () => {
      syncing = false;
      indicator.classList.remove("syncing");
      label.textContent = "";
      reset();
    };
    Promise.resolve(run).then(finish, finish);
  }, { passive: true });

  root.addEventListener("touchcancel", reset, { passive: true });
  win.addEventListener("hashchange", reset);
}
