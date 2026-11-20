// Pure helpers for the Sessions sidebar view (session-sidebar.js): the
// remembered session and provider/model choice, the session switcher list,
// the "add context" candidates, transcript windowing and paint throttling.
// No DOM and no imports (tests load it in a bare VM).

export const SIDEBAR_STORE_KEY = "ark.sessionSidebar.v1";
export const NEW_SESSION = "";
export const RECENT_LIMIT = 30;
export const BLOCK_WINDOW = 80;
export const PAINT_INTERVAL_MS = 200;
export const MAX_CANDIDATES = 8;

const ACTIVE = ["starting", "running", "idle", "stopping"];
const isActive = (session) => Boolean(session) && ACTIVE.includes(session.status);
const text = (value, max) => String(value ?? "").trim().slice(0, max);

/** Stored sidebar state: the open session and the provider/model for a new one. */
export function normalizeSidebarPrefs(raw) {
  const src = raw && typeof raw === "object" ? raw : {};
  return {
    sessionId: text(src.sessionId, 80),
    platform: text(src.platform, 60),
    model: text(src.model, 80),
  };
}

function storage() {
  try { return globalThis.localStorage || null; } catch { return null; }
}

export function loadSidebarPrefs(store = storage()) {
  try { return normalizeSidebarPrefs(JSON.parse(store?.getItem(SIDEBAR_STORE_KEY) || "{}")); } catch {
    return normalizeSidebarPrefs({});
  }
}

export function saveSidebarPrefs(patch, store = storage()) {
  const next = normalizeSidebarPrefs({ ...loadSidebarPrefs(store), ...patch });
  try { store?.setItem(SIDEBAR_STORE_KEY, JSON.stringify(next)); } catch { /* storage disabled */ }
  return next;
}

// The run panel (Execute > Launch) and the sidebar share the last provider
// and model, so a new session from either starts like the previous one.
export function rememberSessionChoice({ platform = "", model = "" } = {}, store = storage()) {
  if (!platform) return loadSidebarPrefs(store);
  return saveSidebarPrefs({ platform, model }, store);
}

/** Provider for a new session: the remembered one if available, else the first available. */
export function pickPreset(presets = [], platform = "") {
  const available = presets.filter((preset) => preset.available);
  return available.find((preset) => preset.id === platform) || available[0] || null;
}

/** Sessions for the switcher: running first, then by last activity; archived ones left out. */
export function recentSessions(sessions = [], limit = RECENT_LIMIT, keep = "") {
  const rank = (session) => (isActive(session) ? 0 : 1);
  const list = sessions.filter((session) => session && session.status !== "archived")
    .sort((a, b) => rank(a) - rank(b)
      || String(b.last_activity_at || b.created_at || "").localeCompare(String(a.last_activity_at || a.created_at || "")));
  const out = list.slice(0, limit);
  if (keep && !out.some((session) => session.id === keep)) {
    const extra = sessions.find((session) => session?.id === keep);
    if (extra) out.push(extra);
  }
  return out;
}

/** The session to show: the remembered one while it exists, else none (a new session). */
export function sidebarSelection(sessions = [], remembered = "") {
  return remembered && sessions.some((session) => session.id === remembered) ? remembered : NEW_SESSION;
}

/**
 * Nodes the user is looking at, for one-click "add context": the node open in
 * Details or the Editor, then the map selection.  `units` resolves ids to
 * { uid, title, type }.
 */
export function contextCandidates({ detail = null, drawerOpen = false, selectedId = null, units = [],
  editorKey = "", tab = "", graphSelections = [] } = {}) {
  const byId = new Map();
  const byUid = new Map();
  for (const unit of units || []) {
    if (!unit) continue;
    if (unit.id != null) byId.set(String(unit.id), unit);
    if (unit.uid) byUid.set(String(unit.uid), unit);
  }
  const out = [];
  const add = (unit, source) => {
    if (!unit?.uid || unit.archived || out.some((item) => item.uid === unit.uid)) return;
    out.push({ uid: String(unit.uid), title: String(unit.title || ""), type: String(unit.type || ""), source });
  };
  if (detail?.uid && (drawerOpen || String(detail.id) === String(selectedId))) add(detail, "open");
  if (tab === "editor" && String(editorKey || "").startsWith("unit:")) {
    const uid = String(editorKey).slice(5);
    add(byUid.get(uid) || { uid, title: "" }, "open");
  }
  if (selectedId != null) add(byId.get(String(selectedId)) || byUid.get(String(selectedId)), "selected");
  for (const ids of graphSelections || []) {
    for (const id of ids || []) add(byId.get(String(id)) || byUid.get(String(id)), "selected");
  }
  return out.slice(0, MAX_CANDIDATES);
}

/** A context entry for a unit that is not validated yet (a new session). */
export function pendingUnitContext(candidate) {
  return {
    id: `unit:${candidate.uid}`, kind: "unit", uid: candidate.uid,
    label: candidate.title ? `${candidate.uid} · ${candidate.title}` : candidate.uid,
    attachment: { kind: "unit", uid: candidate.uid },
  };
}

/** Long transcripts: only the last `limit` blocks are drawn; `hidden` older ones wait behind "Show earlier". */
export function windowBlocks(blocks = [], limit = BLOCK_WINDOW) {
  const size = Math.max(1, Number(limit) || BLOCK_WINDOW);
  if (blocks.length <= size) return { blocks, hidden: 0 };
  let start = blocks.length - size;
  // Do not start on a bare time separator.
  while (start < blocks.length - 1 && blocks[start]?.kind === "separator") start += 1;
  return { blocks: blocks.slice(start), hidden: start };
}

// Drawing many blocks at once blocks the page: the newest DRAW_CHUNK blocks
// are drawn first, older ones a chunk per frame.
export const DRAW_CHUNK = 30;

/** Blocks to draw now: `drawn` so far grows by DRAW_CHUNK up to `limit`. */
export function drawLimit(drawn, limit, chunk = DRAW_CHUNK) {
  return Math.min(limit, Math.max(chunk, drawn + chunk));
}

/** What the composer's main button does. */
export function composerAction(session, { hasContent = false } = {}) {
  if (!session) return "create";            // a new session may start empty
  if (turnRunning(session)) return hasContent ? "steer" : "stop";
  if (isActive(session)) return hasContent ? "steer" : "none";   // a waiting process takes the message
  return hasContent ? "continue" : "none";
}

/** A new session's title: the first line of the prompt, shortened. */
export function newSessionTitle(prompt = "", max = 60) {
  const line = String(prompt || "").split(/\r?\n/).map((part) => part.trim()).find(Boolean) || "";
  return line.length > max ? `${line.slice(0, max - 1).trimEnd()}…` : line;
}

/**
 * Coalesce repaint requests: at most one paint per frame and per
 * `interval` ms, and none while `visible()` is false (the paint waits until
 * `flush()` after the view is shown again).
 */
export function createPaintScheduler({ paint, visible = () => true, interval = PAINT_INTERVAL_MS,
  now = () => Date.now(), frame = (fn) => setTimeout(fn, 16), delay = (fn, ms) => setTimeout(fn, ms) } = {}) {
  let pending = false;
  let scheduled = false;
  let last = -Infinity;
  const stats = { paints: 0, requests: 0 };

  function run() {
    scheduled = false;
    if (!pending) return;
    if (!visible()) return;          // stays pending until flush()
    const wait = interval - (now() - last);
    if (wait > 0) {
      scheduled = true;
      delay(() => frame(run), wait);
      return;
    }
    pending = false;
    last = now();
    stats.paints += 1;
    paint();
  }

  return {
    request() {
      stats.requests += 1;
      pending = true;
      if (scheduled) return;
      scheduled = true;
      frame(run);
    },
    flush() {
      if (!pending) return;
      if (!scheduled) {
        scheduled = true;
        frame(run);
      }
    },
    pending: () => pending,
    stats,
  };
}

// ------------------------------------------------------------ composer keys
/**
 * What a key press in a session composer does.  While a turn runs, Enter
 * queues the message and Ctrl+Enter (Cmd+Enter) sends it now; otherwise both
 * send.  Shift+Enter is a new line; IME composition is left alone.
 * Returns "newline" | "queue" | "now" | "send" | null.
 */
export function composerKeyAction(event = {}, { running = false } = {}) {
  if (event.key !== "Enter" || event.isComposing || event.keyCode === 229) return null;
  if (event.shiftKey) return "newline";
  const mod = Boolean(event.ctrlKey || event.metaKey);
  if (!running) return "send";
  return mod ? "now" : "queue";
}

/** Composer hint key: "running" (Enter queues, Ctrl+Enter sends now) or "idle". */
export function composerHintKey(running) {
  return running ? "session.composer.keys_running" : "session.composer.keys_idle";
}

/** How "send now" reaches a running agent: "inject" into its input, or "interrupt" the turn. */
export function sendNowMode(session) {
  return session?.capabilities?.stream_input || session?.capabilities?.followup || session?.followup ? "inject" : "interrupt";
}

const LIVE = ["starting", "running", "idle", "stopping"];

/**
 * Is a turn running (Enter queues, Ctrl+Enter sends now)?  A long-lived
 * Claude Code session that waits for its next message ("idle") is not.
 */
export function turnRunning(session) {
  if (!session || !LIVE.includes(session.status)) return false;
  return !(session.capabilities?.stream_input && session.status === "idle");
}

/** Label key of the Stop button: stop the turn, end a waiting session, or stop. */
export function stopLabelKey(session) {
  if (!session?.capabilities?.stream_input) return "tasks.stop";
  return session.status === "idle" ? "session.stop_end" : "session.stop_turn";
}

// ------------------------------------------------------------------ images
export const MAX_IMAGE_BYTES = 8 * 1024 * 1024;
export const MAX_IMAGES = 6;
const IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/svg+xml"]);
const IMAGE_EXT = { "image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp", "image/bmp": "bmp", "image/svg+xml": "svg" };

/** Can this file be attached?  { ok, reason: "" | "type" | "size" | "count", name } */
export function acceptImage(file = {}, current = 0) {
  const type = String(file.type || "").toLowerCase();
  if (!IMAGE_TYPES.has(type)) return { ok: false, reason: "type" };
  if (current >= MAX_IMAGES) return { ok: false, reason: "count" };
  if (!(Number(file.size) > 0) || Number(file.size) > MAX_IMAGE_BYTES) return { ok: false, reason: "size" };
  let name = String(file.name || "").trim().replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^[-.]+|[-.]+$/g, "");
  if (!name || !/\.[a-z0-9]+$/i.test(name)) name = `${name || "pasted"}.${IMAGE_EXT[type]}`;
  return { ok: true, reason: "", name: name.slice(0, 80) };
}

/** Context attachments for images read as data URLs. */
export function imageContext(images = []) {
  return images.map((image) => ({ kind: "image_data", name: image.name, data: image.data }));
}

// ---------------------------------------------------------- sub-agent tabs
export const TABS_STORE_KEY = "ark.session.agentTabs.v1";
export const MAX_AGENT_TABS = 12;

/** Open sub-agent tabs per session: { [sessionId]: { tabs: [{ id, label }], active } } */
export function normalizeAgentTabs(raw) {
  const out = {};
  const src = raw && typeof raw === "object" ? raw : {};
  for (const [sessionId, value] of Object.entries(src).slice(-40)) {
    const tabs = (Array.isArray(value?.tabs) ? value.tabs : [])
      .filter((tab) => tab && typeof tab.id === "string" && /^[A-Za-z0-9_.:-]{1,120}$/.test(tab.id))
      .slice(-MAX_AGENT_TABS)
      .map((tab) => ({ id: tab.id, label: String(tab.label || "").slice(0, 80) }));
    const active = tabs.some((tab) => tab.id === value?.active) ? value.active : "";
    if (tabs.length) out[sessionId] = { tabs, active };
  }
  return out;
}

export function openAgentTab(store, sessionId, tab) {
  const next = normalizeAgentTabs(store);
  const entry = next[sessionId] || { tabs: [], active: "" };
  if (!entry.tabs.some((item) => item.id === tab.id)) entry.tabs = [...entry.tabs, { id: tab.id, label: String(tab.label || "").slice(0, 80) }].slice(-MAX_AGENT_TABS);
  entry.active = tab.id;
  next[sessionId] = entry;
  return next;
}

export function closeAgentTab(store, sessionId, tabId) {
  const next = normalizeAgentTabs(store);
  const entry = next[sessionId];
  if (!entry) return next;
  const index = entry.tabs.findIndex((tab) => tab.id === tabId);
  entry.tabs = entry.tabs.filter((tab) => tab.id !== tabId);
  if (entry.active === tabId) entry.active = entry.tabs[Math.min(index, entry.tabs.length - 1)]?.id || "";
  if (!entry.tabs.length) delete next[sessionId];
  return next;
}

export function activateAgentTab(store, sessionId, tabId) {
  const next = normalizeAgentTabs(store);
  const entry = next[sessionId];
  if (entry) entry.active = entry.tabs.some((tab) => tab.id === tabId) ? tabId : "";
  return next;
}

export function loadAgentTabs(store = storage()) {
  try { return normalizeAgentTabs(JSON.parse(store?.getItem(TABS_STORE_KEY) || "{}")); } catch { return {}; }
}

export function saveAgentTabs(value, store = storage()) {
  try { store?.setItem(TABS_STORE_KEY, JSON.stringify(normalizeAgentTabs(value))); } catch { /* storage disabled */ }
}

// ------------------------------------------------------------------ layout
/** On Execute › Runs (wide window) the Sessions view shows the session list. */
export function onRunsPage(state = {}) {
  return state.tab === "tasks" && state.taskView === "sessions";
}

/** What the Sessions view shows: "list" on Execute › Runs (wide window), else "conversation". */
export function sessionViewMode(state = {}, { shell = true } = {}) {
  return shell && onRunsPage(state) ? "list" : "conversation";
}

/** Session properties apply on Execute › Runs only (the node inspector elsewhere). */
export function sessionPropsApplies(state = {}) {
  return onRunsPage(state);
}
