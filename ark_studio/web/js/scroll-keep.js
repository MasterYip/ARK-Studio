// Keep a panel's scroll position and keyboard focus across a full re-render.
//
// The filter rail is rebuilt on every chip click.  Which element scrolls
// depends on the layout: the rail itself (classic layout, phone sheet) or the
// workbench view around it (`.wb-view > .rail` is overflow: visible).  Saving
// `rail.scrollTop` alone therefore restored 0 in the workbench and the list
// jumped to the top.  keepView() finds the real scroller first.

/** Nearest element (the node itself included) that scrolls vertically. */
export function scrollParent(el) {
  for (let node = el; node && node.nodeType === 1; node = node.parentElement) {
    if (node === document.body || node === document.documentElement) break;
    const overflow = getComputedStyle(node).overflowY;
    if (overflow === "auto" || overflow === "scroll" || overflow === "overlay") return node;
  }
  return document.scrollingElement || document.documentElement;
}

const FOCUSABLE = "button, input, select, textarea, summary, a[href], [tabindex]";

/** A stable name for a control inside `root`, so its replacement can be found. */
export function focusKey(el, root) {
  if (!el || !root?.contains(el) || el === root) return null;
  if (el.dataset?.focusKey) return `k:${el.dataset.focusKey}`;
  if (el.dataset?.filterGroup) return `g:${el.dataset.filterGroup}|${el.dataset.value ?? ""}`;
  const all = [...root.querySelectorAll(FOCUSABLE)];
  const index = all.indexOf(el);
  return index >= 0 ? `i:${index}` : null;
}

export function findByFocusKey(root, key) {
  if (!key) return null;
  const all = [...root.querySelectorAll(FOCUSABLE)];
  if (key.startsWith("k:")) return all.find((el) => el.dataset?.focusKey === key.slice(2)) || null;
  if (key.startsWith("g:")) {
    const [group, value] = key.slice(2).split("|");
    return all.find((el) => el.dataset?.filterGroup === group && (el.dataset.value ?? "") === value) || null;
  }
  return all[Number(key.slice(2))] || null;
}

/**
 * Run `render` (which rebuilds `root`'s children) and put the scroll offset
 * and the focused control back.  The offset is restored at once and again on
 * the next frame, after late layout (fonts, counts that arrive later).
 */
export function keepView(root, render) {
  if (!root || typeof document === "undefined") return render();
  const scroller = scrollParent(root);
  const top = scroller.scrollTop;
  const active = document.activeElement;
  const key = focusKey(active, root);
  const result = render();
  if (scroller.scrollTop !== top) scroller.scrollTop = top;
  if (key && !root.contains(document.activeElement)) {
    findByFocusKey(root, key)?.focus({ preventScroll: true });
  }
  if (top > 0) {
    requestAnimationFrame(() => {
      if (scroller.isConnected && Math.abs(scroller.scrollTop - top) > 1 && scroller.scrollTop < top) scroller.scrollTop = top;
    });
  }
  return result;
}
