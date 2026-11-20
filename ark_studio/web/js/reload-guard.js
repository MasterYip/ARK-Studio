// Reload after a hang.  While a script blocks the page's main thread, the
// browser cannot commit a reload of the same tab (the new document uses the
// same renderer process); F5 takes effect only once the blocking task ends.
// The reloaded page then reopened the view that blocked it (the hash route,
// ?unit= / ?view=) and blocked again, while a new tab started at Home.
//
// This guard keeps one record per tab in sessionStorage:
//   open     the document has not run pagehide yet (cleared there);
//   stalled  a task of STALL_MS or longer ended shortly before pagehide;
//   url      the address the person was on when the page left;
//   at       when the record was written.
// A load that finds the previous document still "open" (renderer crashed or
// was ended from the "page unresponsive" dialog) or "stalled" starts at Home
// without the view parameters and offers to reopen the previous address.
// `?safe=1` asks for the same start explicitly.

export const RELOAD_GUARD_KEY = "ark.load.v1";
export const STALL_MS = 3000;       // a single task this long reads as a hang
export const RECENT_MS = 20000;     // ... when it ended this shortly before leaving
export const RECORD_MAX_AGE_MS = 30 * 60 * 1000;
export const SAFE_DROP_PARAMS = ["safe", "unit", "view", "filter"];

export function readRecord(storage) {
  try {
    const raw = JSON.parse(storage?.getItem(RELOAD_GUARD_KEY) || "null");
    return raw && typeof raw === "object" ? raw : null;
  } catch { return null; }
}

function writeRecord(storage, record) {
  try { storage?.setItem(RELOAD_GUARD_KEY, JSON.stringify(record)); } catch { /* storage disabled or full */ }
}

// Whether this load starts safely, and why: "param" (?safe=1), "unclean" (the
// previous document never left normally) or "stalled" (it left right after
// a hang).  A discarded tab (memory saver) also skips pagehide: not unclean.
export function decideStart(record, { now, safeParam = false, wasDiscarded = false } = {}) {
  const previousUrl = typeof record?.url === "string" ? record.url : "";
  if (safeParam) return { safe: true, reason: "param", previousUrl };
  if (!record || !(now - Number(record.at || 0) <= RECORD_MAX_AGE_MS)) return { safe: false, reason: "", previousUrl: "" };
  if (record.stalled) return { safe: true, reason: "stalled", previousUrl };
  if (record.open && !wasDiscarded) return { safe: true, reason: "unclean", previousUrl };
  return { safe: false, reason: "", previousUrl: "" };
}

// Long-task entries ({startTime, duration}, performance.now() clock): did one
// of at least STALL_MS end within RECENT_MS of `perfNow`?
export function stalledRecently(entries, perfNow, { stallMs = STALL_MS, recentMs = RECENT_MS } = {}) {
  return (entries || []).some((entry) => Number(entry.duration) >= stallMs
    && Number(entry.startTime) + Number(entry.duration) >= perfNow - recentMs);
}

// The address a safe start shows: same path and other query parameters,
// without the view parameters, at Home.
export function safeUrl(href) {
  const url = new URL(href);
  for (const name of SAFE_DROP_PARAMS) url.searchParams.delete(name);
  return url.pathname + url.search + "#/overview";
}

// The address worth reopening: none for Home or an empty route.
export function reopenTarget(previousUrl, base) {
  if (!previousUrl) return "";
  let url;
  try { url = new URL(previousUrl, base); } catch { return ""; }
  url.searchParams.delete("safe");
  const route = url.hash.replace(/^#\/?/, "");
  const extra = SAFE_DROP_PARAMS.some((name) => url.searchParams.has(name));
  if (!extra && (!route || /^(home|overview)\/?$/.test(route))) return "";
  return url.pathname + url.search + url.hash;
}

// Wire the guard into this document.  Returns the start decision.
export function startReloadGuard({
  win = globalThis.window,
  storage = (() => { try { return globalThis.sessionStorage; } catch { return null; } })(),
  now = () => Date.now(),
} = {}) {
  const location = win.location;
  const safeParam = new URLSearchParams(location.search).get("safe") === "1";
  const decision = decideStart(readRecord(storage), {
    now: now(), safeParam, wasDiscarded: Boolean(win.document?.wasDiscarded),
  });
  const here = () => location.pathname + location.search + location.hash;
  const markOpen = () => writeRecord(storage, { open: true, stalled: false, url: here(), at: now() });
  markOpen();
  // Refresh "at" so a page that hangs after hours open still counts.
  win.setInterval?.(() => { if (!win.document?.hidden) markOpen(); }, 60000);
  const seen = [];
  let observer = null;
  try {
    observer = new win.PerformanceObserver((list) => {
      for (const entry of list.getEntries()) if (entry.duration >= STALL_MS) seen.push({ startTime: entry.startTime, duration: entry.duration });
      if (seen.length > 20) seen.splice(0, seen.length - 20);
    });
    observer.observe({ type: "longtask", buffered: true });
  } catch { observer = null; /* longtask unsupported: only the crash case is detected */ }
  win.addEventListener("pagehide", () => {
    // The hang's entry is usually still queued: takeRecords() reads it now.
    const pending = observer?.takeRecords?.() || [];
    const stalled = stalledRecently([...seen, ...pending], win.performance.now());
    writeRecord(storage, { open: false, stalled, url: here(), at: now() });
  });
  win.addEventListener("pageshow", (event) => { if (event.persisted) markOpen(); });
  return decision;
}
