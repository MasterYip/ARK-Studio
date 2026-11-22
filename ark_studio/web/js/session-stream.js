// The app's shared session stream (session-stream-model.js wired to api.js)
// and the session actions both the Sessions page and the Sessions sidebar
// view use: attach context, send a message, stop, start a new session.

import { api, tabProjectId } from "./api.js";
import { t } from "./i18n.js";
import { createSessionStream, splitTranscriptKey } from "./session-stream-model.js";

export const sessionStream = createSessionStream({
  // The list names the project it was read for (a switch may be on its way).
  listSessions: () => {
    const project = tabProjectId();
    return api.agentSessions({ fresh: true }).then((data) => (data && project ? { ...data, project } : data));
  },
  fetchTranscript: (key, { tail, known } = {}) => {
    const { id, agent } = splitTranscriptKey(key);
    return api.sessionTranscript(id, { fresh: true, query: { tail, known, agent } });
  },
});

export function contextItems(sessionId) {
  return sessionStream.contexts.get(sessionId) || [];
}

export function contextPayload(sessionId) {
  return contextItems(sessionId).map((item) => ({ ...item.attachment }));
}

// Validate `attachment` against the session and add it to the context of the
// next message (replacing an entry with the same kind and label).
export async function attachSessionContext(sessionId, attachment) {
  const data = await api.sessionValidateContext(sessionId, [attachment]);
  const item = data.items?.[0];
  if (!item) throw new Error(t("session.context.rejected"));
  const list = contextItems(sessionId).filter((entry) => entry.label !== item.label || entry.kind !== item.kind);
  list.push({ ...item, attachment });
  sessionStream.contexts.set(sessionId, list);
  return item;
}

export function removeSessionContext(sessionId, itemId) {
  const list = contextItems(sessionId).filter((entry) => entry.id !== itemId);
  if (list.length) sessionStream.contexts.set(sessionId, list);
  else sessionStream.contexts.delete(sessionId);
}

export function clearSessionContext(sessionId) {
  sessionStream.contexts.delete(sessionId);
}

// Send `text` (and the attached context) to a session: a steer while it runs,
// otherwise the next turn with `choices` ({ platform, model, effort,
// access_mode }).  Returns { session, steer } from the server.
export async function sendSessionMessage(session, text, { mode, context = [], choices = {}, now = false } = {}) {
  if (mode === "steer") {
    const data = await api.sessionSteer(session.id, text, context, now);
    if (data.session) sessionStream.patchSession(data.session);
    if (data.mode) sessionStream.refreshSessions().catch(() => {});
    return { session: data.session || null, steer: data.steer || null, mode: data.mode || "" };
  }
  const data = await api.continueSession(session.id, text, {
    context,
    platform: choices.platform ?? session.platform,
    model: choices.model ?? session.model ?? "",
    effort: choices.effort ?? session.effort ?? "",
    access_mode: choices.access_mode ?? session.access_mode ?? "read_only",
  });
  if (data.session) sessionStream.patchSession(data.session);
  return { session: data.session || null, steer: null };
}

export async function stopAgentSession(sessionId) {
  const data = await api.stopSession(sessionId);
  sessionStream.patchSession(data?.session?.id ? data.session : { id: sessionId, status: "stopping" });
  setTimeout(() => sessionStream.refreshSessions().catch(() => {}), 350);
  return data;
}

export async function startAgentSession(body) {
  const data = await api.createSession(body);
  if (data.session) sessionStream.patchSession(data.session);
  sessionStream.refreshSessions().catch(() => {});
  return data.session;
}

// Send a queued message now (inject it, or stop the turn and start the next
// with it); the other queued messages stay queued.
export async function sendQueuedNow(sessionId, steerId) {
  const data = await api.sessionSteerNow(sessionId, steerId);
  if (data.session) sessionStream.patchSession(data.session);
  sessionStream.refreshSessions().catch(() => {});
  return data;
}

// A tool item with its whole kept output (a page holds a preview).
export async function fullToolItem(sessionId, itemId) {
  const data = await api.sessionTranscript(sessionId, { fresh: true, query: { item: itemId } });
  return data.item || null;
}

// One image of a tool result, as a data URL (fetched when its row opens).
export async function toolImage(sessionId, itemId, n) {
  const data = await api.sessionTranscript(sessionId, { fresh: true, query: { item: itemId, image: n } });
  return data.image || "";
}

// Rename a session (an empty title returns to the default); both views see it.
export async function renameAgentSession(sessionId, title) {
  const data = await api.renameSession(sessionId, title);
  if (data.session) sessionStream.patchSession(data.session);
  return data.session;
}

// 关闭 / End: stop the process; ARK leaves the session alone afterwards.
export async function closeAgentSession(sessionId) {
  const data = await api.closeSession(sessionId);
  if (data.session) sessionStream.patchSession(data.session);
  sessionStream.refreshSessions().catch(() => {});
  return data.session;
}

// 删除 / Delete: off the list; the transcript stays in the sessions folder
// unless `withFiles`.
export async function deleteAgentSession(sessionId, { withFiles = false } = {}) {
  const data = withFiles ? await api.deleteSessionEnded(sessionId) : await api.archiveSessionEnded(sessionId);
  await sessionStream.refreshSessions().catch(() => {});
  return data;
}

export const sessionImages = new Map();   // session id ("" = new session) -> [{ id, name, size, data, url }]
