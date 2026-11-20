// Long lists built as they are scrolled to: the first rows at once, then a
// chunk more whenever the end of the list comes near the viewport.  Research
// › Review rebuilt every waiting item (each with a strip of image and video
// previews) on every checkbox click; a project with hundreds of reviews
// stalled the page for seconds.

/** How many rows to show next: `shown` grows by `chunk`, never past `total`. */
export function nextRowCount(shown, total, chunk) {
  return Math.min(Math.max(0, total), Math.max(0, shown) + Math.max(1, chunk));
}

/** Rows to build first: what was shown before (a redraw), at least `initial`. */
export function initialRowCount(total, initial, previous = 0) {
  return Math.min(Math.max(0, total), Math.max(initial, previous || 0));
}

/**
 * Fill `list` with `render(item, index)` rows, `initial` (or `previous`)
 * at once and `chunk` more each time a sentinel after the last row comes
 * within `margin` of the viewport.  `onCount(n)` hears how many rows are
 * built (to keep them across a redraw).  Returns a function that stops it.
 */
export function fillProgressively(list, items, render, { initial = 30, chunk = 30, previous = 0, margin = "800px", onCount = null } = {}) {
  let shown = 0;
  const add = (count) => {
    const fragment = document.createDocumentFragment();
    for (; shown < count; shown += 1) fragment.appendChild(render(items[shown], shown));
    list.appendChild(fragment);
    onCount?.(shown);
  };
  add(initialRowCount(items.length, initial, previous));
  if (shown >= items.length || typeof IntersectionObserver === "undefined") {
    add(items.length);
    return () => {};
  }
  const sentinel = document.createElement("div");
  sentinel.className = "progressive-sentinel";
  sentinel.setAttribute("aria-hidden", "true");
  list.after(sentinel);
  const observer = new IntersectionObserver((entries) => {
    if (!entries.some((entry) => entry.isIntersecting)) return;
    add(nextRowCount(shown, items.length, chunk));
    if (shown >= items.length) stop();
    else { observer.unobserve(sentinel); observer.observe(sentinel); } // re-check: still in view after growing
  }, { rootMargin: `${margin} 0px` });
  const stop = () => { observer.disconnect(); sentinel.remove(); };
  observer.observe(sentinel);
  return stop;
}
