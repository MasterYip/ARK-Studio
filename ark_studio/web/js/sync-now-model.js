// "Sync now" in the top bar (no DOM; sync-now.js renders it): the live-sync
// connection as one state, what the button shows for it, and the gate that
// keeps a double click or a held shortcut from starting a second sync.

/** How long the button shows its check after a sync. */
export const SYNC_DONE_MS = 1500;
/** Clicks this soon after a sync ended are ignored (a double click). */
export const SYNC_MIN_GAP_MS = 600;

/**
 * The push channel as one word: "live" (event stream open), "polling" (the
 * stream is blocked; ARK asks every few seconds), "connecting" (the stream
 * is reconnecting), "offline" or "unreachable" (cached data, read-only).
 */
export function connectionState({ live = false, polling = false, offline = false, connectivity = "" } = {}) {
  if (offline) return connectivity === "unreachable" ? "unreachable" : "offline";
  if (live) return "live";
  if (polling) return "polling";
  return "connecting";
}

/**
 * What the button shows.  `phase`: "idle" | "syncing" | "done".
 * Returns { icon, spinning, disabled, busy, dot, lines } where `dot` is the
 * connection state when it is worth a dot (anything but "live") and `lines`
 * are the tooltip lines as [i18n key, params].
 */
export function syncButtonView({ phase = "idle", connection = "live", shortcut = "", lastSyncedAt = 0 } = {}) {
  const syncing = phase === "syncing";
  const lines = [
    shortcut ? ["sync.now_with_key", { key: shortcut }] : ["sync.now", {}],
    [`sync.state.${connection}`, {}],
  ];
  if (lastSyncedAt) lines.push(["cache.last_sync", { time: lastSyncedAt }]);
  return {
    icon: phase === "done" ? "check" : "refresh",
    spinning: syncing,
    disabled: syncing,
    busy: syncing,
    dot: connection === "live" ? "" : connection,
    lines,
  };
}

/**
 * Single-flight with a short cool-down: while `run` is in progress every
 * trigger() returns the same promise; within `minGap` ms after it ended
 * trigger() returns null and runs nothing.
 */
export function createSyncGate(run, { minGap = SYNC_MIN_GAP_MS, now = () => Date.now() } = {}) {
  let inFlight = null;
  let endedAt = -Infinity;
  return {
    trigger(...args) {
      if (inFlight) return inFlight;
      if (now() - endedAt < minGap) return null;
      inFlight = Promise.resolve().then(() => run(...args)).finally(() => {
        inFlight = null;
        endedAt = now();
      });
      return inFlight;
    },
    get busy() { return Boolean(inFlight); },
  };
}
