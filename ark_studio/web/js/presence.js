// The project's live agent presence in this tab (T-0043).  live-sync.js
// receives "event: presence" frames (GET /api/events?presence=1) and hands
// them here; GET /api/presence loads the state when a project opens.  The
// graphs draw it as an overlay (graph-presence.js); the node details and the
// Map's "Agents active" chip read it too.  Nothing here touches graph data.
import { api, tabProjectId } from "./api.js";
import { getMindSettings } from "./mind-settings.js";
import {
  activeAgents, clearPresence, createPresenceModel, expirePresence, mergePresence, presenceOnUnit,
} from "./presence-model.js";

const model = createPresenceModel();
const listeners = new Set();
let enabled = getMindSettings().agentPresence !== false;
let sweepTimer = 0;
let loadSeq = 0;

function notify() {
  for (const fn of [...listeners]) {
    try { fn(model); } catch { /* a listener's error never stops the others */ }
  }
}

function scheduleSweep() {
  if (sweepTimer || !model.entries.size) return;
  sweepTimer = setInterval(() => {
    if (expirePresence(model)) notify();
    if (!model.entries.size) { clearInterval(sweepTimer); sweepTimer = 0; }
  }, 500);
}

/** Merge a server payload (a stream frame or the snapshot reply). */
export function applyPresence(payload) {
  if (!enabled || !payload) return;
  const project = tabProjectId();
  if (project && payload.project && payload.project !== project) return;
  const result = mergePresence(model, payload);
  scheduleSweep();
  if (result.changed) notify();
}

/** Load the current presence (project opened or the stream reconnected). */
export async function loadPresence() {
  if (!enabled) return;
  const seq = ++loadSeq;
  try {
    const data = await api.presence();
    if (seq === loadSeq) applyPresence(data);
  } catch { /* older server or offline: the stream fills it in */ }
}

/** Project switched: the old project's badges go at once. */
export function resetPresence() {
  loadSeq += 1;
  clearPresence(model);
  notify();
}

export function presenceEnabled() { return enabled; }

/** Settings › Appearance › Graphs › General › Show agent activity on the graph. */
export function setPresenceEnabled(value) {
  const next = value !== false;
  if (next === enabled) return;
  enabled = next;
  if (!enabled) clearPresence(model);
  notify();
  if (enabled) loadPresence();
}

/** Call fn(model) after every change; returns the unsubscribe function. */
export function subscribePresence(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function presenceModel() { return model; }
export function presenceAgents() { return enabled ? activeAgents(model) : []; }
export function presenceForUnit(unit) { return enabled ? presenceOnUnit(model, unit) : []; }

if (typeof window !== "undefined") {
  window.addEventListener("ark:presence", (event) => applyPresence(event.detail));
  window.addEventListener("ark:presence-reset", () => { resetPresence(); loadPresence(); });
  // Diagnostics and the headless checks.
  window.__ARK_PRESENCE__ = { model, applyPresence, presenceAgents };
}
