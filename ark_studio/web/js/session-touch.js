// Touch helpers for the session views on phones: long-press raises the
// element's context menu, a sheet closes when its handle is swiped down,
// and the composer stays above the on-screen keyboard.

const LONG_PRESS_MS = 500;
const SWIPE_CLOSE_PX = 80;

/** A long press (touch or pen) fires the element's own contextmenu handler. */
export function longPressMenu(el) {
  let timer = null;
  let start = null;
  let fired = 0;   // 0: no long press yet (not "at page load": clicks then must work)
  const cancel = () => { clearTimeout(timer); timer = null; };
  el.addEventListener("pointerdown", (event) => {
    if (event.pointerType === "mouse") return;
    start = [event.clientX, event.clientY];
    cancel();
    timer = setTimeout(() => {
      fired = performance.now();
      el.dispatchEvent(new MouseEvent("contextmenu", { clientX: start[0], clientY: start[1], bubbles: true, cancelable: true }));
    }, LONG_PRESS_MS);
  });
  el.addEventListener("pointermove", (event) => {
    if (start && Math.hypot(event.clientX - start[0], event.clientY - start[1]) > 10) cancel();
  });
  for (const type of ["pointerup", "pointercancel", "pointerleave"]) el.addEventListener(type, cancel);
  // The browser's own long-press menu, and the tap that ends the press, are not doubled.
  el.addEventListener("contextmenu", (event) => {
    if (event.isTrusted && fired && performance.now() - fired < 800) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  el.addEventListener("click", (event) => {
    if (fired && performance.now() - fired < 800) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
}

/** Swiping a sheet's handle down closes it (it follows the finger meanwhile). */
export function swipeDownToClose(sheet, handles, close) {
  for (const handle of handles) {
    let startY = null;
    handle.addEventListener("pointerdown", (event) => {
      if (event.pointerType === "mouse") return;
      startY = event.clientY;
      sheet.classList.add("sw-dragging");
      try { handle.setPointerCapture?.(event.pointerId); } catch { /* the pointer is already gone */ }
    });
    handle.addEventListener("pointermove", (event) => {
      if (startY == null) return;
      const dy = Math.max(0, event.clientY - startY);
      sheet.style.transform = `translateY(${dy}px)`;
    });
    const end = (event) => {
      if (startY == null) return;
      const dy = Math.max(0, (event.clientY ?? startY) - startY);
      startY = null;
      sheet.classList.remove("sw-dragging");
      sheet.style.transform = "";
      if (dy > SWIPE_CLOSE_PX) close();
    };
    handle.addEventListener("pointerup", end);
    handle.addEventListener("pointercancel", end);
  }
}

/** --sw-vh is the visible height (the on-screen keyboard taken off). */
let watching = false;
export function watchKeyboard() {
  if (watching || !globalThis.visualViewport) return;
  watching = true;
  const vv = globalThis.visualViewport;
  const sync = () => {
    const keyboard = Math.max(0, window.innerHeight - vv.height - vv.offsetTop);
    document.documentElement.style.setProperty("--sw-vh", `${Math.round(vv.height)}px`);
    // The conversation ends where the phone tab bar starts (its drawn height).
    const bar = document.querySelector(".mobile-tabbar");
    const barH = bar && getComputedStyle(bar).display !== "none" ? Math.ceil(window.innerHeight - bar.getBoundingClientRect().top) : 0;
    document.documentElement.style.setProperty("--sw-tabbar", `${Math.max(0, barH)}px`);
    document.body.classList.toggle("sw-kb-open", keyboard > 80);
  };
  vv.addEventListener("resize", sync);
  window.addEventListener("resize", sync);
  requestAnimationFrame(sync);
  vv.addEventListener("scroll", sync);
  sync();
}
