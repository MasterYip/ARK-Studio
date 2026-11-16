// Collapsible block for long read-only content inside Settings (status
// lists, queues, logs, history, reference tables).  Real setting controls
// stay outside it.  Built on <details>/<summary>, so it is keyboard
// accessible (Tab to the summary, Enter/Space toggles) with no extra code.
// Collapsed by default; the open/closed state is remembered per section.

// No dom.js import: it pulls in the Markdown bundle, and the state helpers
// below are unit-tested without a DOM.
import { icon } from "./icons.js";

function el(tag, className, ...children) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.append(...children.flat().filter((child) => child != null && child !== false));
  return node;
}

export const FOLD_PREFIX = "ark.settingsFold.";

function storageOrNull(storage) {
  if (storage !== undefined) return storage;
  try { return globalThis.localStorage || null; } catch { return null; }
}

/** Remembered state for one section; `fallback` when nothing is stored. */
export function foldIsOpen(id, { storage, fallback = false } = {}) {
  try {
    const value = storageOrNull(storage)?.getItem(FOLD_PREFIX + id);
    return value === "1" ? true : value === "0" ? false : fallback;
  } catch { return fallback; }
}

export function rememberFold(id, open, { storage } = {}) {
  try { storageOrNull(storage)?.setItem(FOLD_PREFIX + id, open ? "1" : "0"); } catch { /* private browsing */ }
}

/**
 * `foldSection({ id, title, summary, body })` returns `{ el, body, setSummary }`.
 * `summary` is the one-line digest (counts, state) shown while collapsed;
 * update it with `setSummary(text | node)` as data loads.
 */
export function foldSection({ id, title, summary = "", body = null, open = false, className = "" }) {
  const summaryEl = el("span", "settings-fold-summary");
  const content = el("div", "settings-fold-body", [].concat(body ?? []));
  const chevron = el("span", "settings-fold-chevron", icon("chevronRight", "sm"));
  chevron.setAttribute("aria-hidden", "true");
  const details = el("details", `settings-fold ${className}`.trim(),
    el("summary", "settings-fold-head", chevron, el("span", "settings-fold-title", title), summaryEl),
    content);
  details.dataset.fold = id;
  details.open = foldIsOpen(id, { fallback: open });
  details.addEventListener("toggle", () => rememberFold(id, details.open));
  const setSummary = (value) => {
    summaryEl.replaceChildren(...[].concat(value ?? []).filter((part) => part != null && part !== ""));
  };
  setSummary(summary);
  return { el: details, body: content, setSummary };
}
