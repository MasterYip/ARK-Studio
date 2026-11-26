// Finished sessions the person has not opened yet (server: GET/POST
// /api/agent-sessions/notices, session_notices.go): the bubbles on Execute
// and Runs, the unseen marks of the session list and what to notify.  Pure
// so tests/js/session_notices.test.mjs covers it; the runtime is
// session-notices.js.
//
// A notice is {id, title, outcome: "done" | "error", at, reason}: the main
// conversation's turn ended cleanly (the agent waits for the person) or
// failed.  It stays until the person opens the session.

/** The notices of a reply, cleaned: known outcomes and ids only. */
export function noticeItems(data) {
  const items = Array.isArray(data?.items) ? data.items : [];
  return items.filter((item) => item && typeof item.id === "string" && item.id
    && (item.outcome === "done" || item.outcome === "error"))
    .map((item) => ({ id: item.id, title: String(item.title || ""), outcome: item.outcome, at: String(item.at || ""),
      reason: String(item.reason || "") }));
}

/** The notification switches (both on unless turned off). */
export function noticePrefs(data) {
  const prefs = data?.prefs || {};
  return { done: prefs.done !== false, error: prefs.error !== false };
}

/**
 * The count bubble of Execute and Runs: hidden without notices, red while a
 * failed session waits, orange otherwise.
 */
export function noticeBubble(items = []) {
  const errors = items.filter((item) => item.outcome === "error").length;
  const n = items.length;
  return { n, errors, done: n - errors, state: !n ? "none" : errors ? "error" : "new", text: n > 99 ? "99+" : String(n) };
}

/** id -> outcome of the unseen sessions (the list's marks). */
export function unseenById(items = []) {
  return new Map(items.map((item) => [item.id, item.outcome]));
}

const noticeKey = (item) => `${item.id}@${item.at}`;

/**
 * The notices that were not there before (`known`: keys of the previous
 * reply, null on the first reply of a project, which only records them).
 */
export function freshNotices(known, items = []) {
  if (!known) return [];
  return items.filter((item) => !known.has(noticeKey(item)));
}

export function noticeKeys(items = []) {
  return new Set(items.map(noticeKey));
}

/**
 * What to do with a fresh notice: "seen" when the person is looking at that
 * session (mark it, no notification), "" when their switch is off, else
 * "toast" while the page has focus and "system" when it does not.
 */
export function noticeAction(item, { prefs = { done: true, error: true }, viewing = "", visible = true, focused = true } = {}) {
  if (viewing && viewing === item.id && visible && focused) return "seen";
  if (!(item.outcome === "error" ? prefs.error : prefs.done)) return "";
  return visible && focused ? "toast" : "system";
}

/**
 * The notification text: {title, body, line} ("WM-HOI 已完成一轮，等待你回复",
 * "WM-HOI 出错停止：API Error: 529"); line is the one-line toast.
 */
export function noticeText(item, t) {
  const name = item.title || item.id;
  if (item.outcome === "error") {
    const reason = item.reason || t("session.notice.error_unknown");
    return { title: t("session.notice.error_title", { name }), body: reason, line: t("session.notice.error", { name, reason }) };
  }
  const line = t("session.notice.done", { name });
  return { title: line, body: "", line };
}
