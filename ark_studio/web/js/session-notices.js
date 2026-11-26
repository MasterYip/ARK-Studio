// Finished sessions the person has not opened yet (GET/POST
// /api/agent-sessions/notices): keeps ctx.S.sessionNotices fresh, paints the
// bubbles on Execute and Runs (app.js syncSessionBubbles), marks the session
// on screen seen and notifies when another one finishes: a toast while the
// page has focus, else a system notification (the desktop app's own, or the
// browser's when the person allowed them).  The pure parts are
// session-notices-model.js; the seen marks live on the server per person.
import { api } from "./api.js";
import { toast } from "./components.js";
import { t } from "./i18n.js";
import {
  freshNotices, noticeAction, noticeItems, noticeKeys, noticePrefs, noticeText, unseenById,
} from "./session-notices-model.js";

/** Fired on `window` after the notices changed ({detail: ctx.S.sessionNotices}). */
export const SESSION_NOTICES = "ark:session-notices";

const POLL_MS = 12000;
const HIDDEN_POLL_MS = 30000;

let ctxRef = null;
let timer = 0;
let inflight = null;
let known = null;          // notice keys of the last reply (null: first reply of the project)
let knownProject = "";
let viewing = "";          // the session on screen (session-workspace.js)
const seenSent = new Set(); // "<id>@<at>" marks already posted

const projectKey = (ctx) => String(ctx.S.project?.id || "");

function empty() {
  return { project: "", items: [], unseen: new Map(), prefs: { done: true, error: true }, loaded: false };
}

/** The current notices ({items, unseen: Map id -> outcome, prefs}). */
export function sessionNotices(ctx = ctxRef) {
  const state = ctx?.S.sessionNotices;
  return state && state.project === projectKey(ctx) ? state : empty();
}

function apply(ctx, data) {
  const project = projectKey(ctx);
  const items = noticeItems(data);
  const prev = sessionNotices(ctx);
  ctx.S.sessionNotices = { project, items, unseen: unseenById(items), prefs: noticePrefs(data), loaded: true };
  const changed = JSON.stringify(prev.items) !== JSON.stringify(items) || !prev.loaded
    || JSON.stringify(prev.prefs) !== JSON.stringify(ctx.S.sessionNotices.prefs);
  if (changed) {
    ctx.syncSessionBubbles?.();
    window.dispatchEvent(new CustomEvent(SESSION_NOTICES, { detail: ctx.S.sessionNotices }));
  }
  return items;
}

function pageState() {
  return { visible: document.visibilityState === "visible", focused: typeof document.hasFocus === "function" ? document.hasFocus() : true };
}

/** Reloads the notices and notifies about new ones. */
export function refreshSessionNotices(ctx = ctxRef) {
  if (!ctx || inflight) return inflight;
  const project = projectKey(ctx);
  if (!project || ctx.S.project?.demo) return null;
  inflight = api.sessionNotices().then((data) => {
    if (project !== projectKey(ctx)) return;
    const items = apply(ctx, data);
    const before = knownProject === project ? known : null;
    known = noticeKeys(items);
    knownProject = project;
    const prefs = sessionNotices(ctx).prefs;
    for (const item of freshNotices(before, items)) {
      const action = noticeAction(item, { prefs, viewing, ...pageState() });
      if (action === "seen") markSessionSeen(ctx, item.id);
      else if (action) notify(ctx, item, action);
    }
    // The session on screen finished while it was shown: seen.
    if (viewing && sessionNotices(ctx).unseen.has(viewing)) viewSession(ctx, viewing);
  }).catch(() => { /* older server or offline: no bubbles */ }).finally(() => { inflight = null; });
  return inflight;
}

function schedule() {
  clearTimeout(timer);
  timer = setTimeout(() => {
    refreshSessionNotices();
    schedule();
  }, document.visibilityState === "visible" ? POLL_MS : HIDDEN_POLL_MS);
}

/** Starts the polling (app.js, once). */
export function startSessionNotices(ctx) {
  ctxRef = ctx;
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
      refreshSessionNotices(ctx);
      schedule();
    }
  });
  window.addEventListener("focus", () => { if (viewing) viewSession(ctx, viewing); });
  refreshSessionNotices(ctx);
  schedule();
}

/** Marks a session seen (it was opened): the bubble and its mark go away. */
export function markSessionSeen(ctx, id) {
  const state = sessionNotices(ctx);
  const item = state.items.find((entry) => entry.id === id);
  if (!item) return;
  const key = `${item.id}@${item.at}`;
  if (seenSent.has(key)) return;
  seenSent.add(key);
  // Clear it at once; the reply has the server's view.
  apply(ctx, { items: state.items.filter((entry) => entry.id !== id), prefs: state.prefs });
  known?.delete(key);
  api.markSessionsSeen([id]).then((data) => { if (projectKey(ctx) === state.project) apply(ctx, data); })
    .catch(() => { seenSent.delete(key); });
}

/**
 * The session view tells which session is on screen (null when it closes);
 * one that waits unseen is marked seen while the page has focus.
 */
export function viewSession(ctx, id) {
  viewing = id || "";
  if (!viewing) return;
  const page = pageState();
  if (page.visible && page.focused && sessionNotices(ctx).unseen.has(viewing)) markSessionSeen(ctx, viewing);
}

/** A session whose turn just ended on screen: reload so the mark can clear. */
export function sessionTurnEnded(ctx) {
  refreshSessionNotices(ctx);
}

/** Saves the notification switches (Settings › Personal). */
export async function setSessionNoticePrefs(ctx, prefs) {
  const data = await api.setSessionNoticePrefs(prefs);
  apply(ctx, data);
  return sessionNotices(ctx).prefs;
}

function openSession(ctx, id) {
  ctx.S.selectedSessionId = id;
  ctx.S.sessionThreadMode = "thread";
  if (ctx.navigateWorkspace) ctx.navigateWorkspace("execute", "sessions");
  else ctx.setTaskView?.("sessions");
}

function notify(ctx, item, action) {
  const text = noticeText(item, t);
  if (action === "toast") {
    toast(text.line, item.outcome === "error" ? "err" : "ok", {
      duration: 6000, action: { label: t("session.notice.open"), onClick: () => openSession(ctx, item.id) },
    });
    return;
  }
  const desktop = window.__ARK_DESKTOP__;
  if (typeof desktop?.notify === "function") {
    Promise.resolve(desktop.notify(text.title, text.body)).catch(() => {});
    return;
  }
  if (typeof Notification === "undefined" || Notification.permission !== "granted") return;
  try {
    const n = new Notification(text.title, { body: text.body, tag: `ark-session-${item.id}`, icon: "/favicon.svg" });
    n.onclick = () => {
      window.focus();
      openSession(ctx, item.id);
      n.close();
    };
  } catch { /* some browsers only allow notifications from a service worker */ }
}

/** Browser notifications: "granted", "denied", "default" or "unsupported" (desktop app: "granted"). */
export function systemNotificationState() {
  if (typeof window.__ARK_DESKTOP__?.notify === "function") return "granted";
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

/** Asks the browser for notifications (from a click). */
export async function requestSystemNotifications() {
  if (systemNotificationState() !== "default") return systemNotificationState();
  try { return await Notification.requestPermission(); } catch { return systemNotificationState(); }
}
