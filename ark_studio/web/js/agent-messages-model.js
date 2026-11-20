// Pure helpers for the experimental "Message agent" dialog (agent-messages.js;
// tests/js/agent_messages_model.test.mjs).  The switch lives in this browser
// (Settings > Advanced), default off.  Design: docs/development/agent-messaging.md.

export const AGENT_MESSAGES_KEY = "ark.exp.agentMessages";
export const MESSAGE_STATUSES = ["queued", "delivered", "read", "answered"];
export const MESSAGE_MAX = 4000;
// Sessions ARK launched that take a steer message now (sessions.go activeStatuses,
// minus "stopping").
export const STEERABLE_STATUSES = new Set(["starting", "running"]);

/** The switch's state in `storage` ("1" = on; anything else, or nothing, = off). */
export function messagesEnabledIn(storage) {
  try { return storage?.getItem(AGENT_MESSAGES_KEY) === "1"; } catch { return false; }
}

function linkedTo(session, uid) {
  if (!uid) return false;
  return (Array.isArray(session?.linked_units) ? session.linked_units : [])
    .some((u) => (typeof u === "string" ? u : u?.uid) === uid);
}

/**
 * Where a message can go, in menu order: any of my agents in this project;
 * running ARK sessions of this project (those linked to `unitUid` first),
 * which get it at once through their steer channel; each of my agent tokens
 * by label.  Expired tokens and stopped sessions are left out.
 */
export function messageTargets({ tokens = [], sessions = [], projectId = "", unitUid = "" } = {}) {
  const out = [{ value: "mine", kind: "mine", label: "" }];
  const running = (Array.isArray(sessions) ? sessions : [])
    .filter((s) => s?.id && STEERABLE_STATUSES.has(s.status))
    .filter((s) => !projectId || !s.project?.id || s.project.id === projectId)
    .map((s) => ({ value: `session:${s.id}`, kind: "session", label: String(s.title || s.id),
      linked: linkedTo(s, unitUid), channel: s.capabilities?.steer_channel || "next_turn" }));
  running.sort((a, b) => Number(b.linked) - Number(a.linked));
  out.push(...running);
  for (const tok of Array.isArray(tokens) ? tokens : []) {
    if (!tok?.id || tok.expired) continue;
    out.push({ value: `token:${tok.id}`, kind: "token", label: String(tok.label || tok.prefix || tok.id) });
  }
  return out;
}

/** The target a dialog opened on `unitUid` preselects: a running session linked to it, else "mine". */
export function defaultTarget(targets, unitUid = "") {
  return targets.find((x) => x.kind === "session" && x.linked && unitUid)?.value || "mine";
}

/** "session:<id>" -> "<id>" (else ""). */
export function sessionOf(target) {
  return String(target || "").startsWith("session:") ? String(target).slice(8) : "";
}

/** A known status, for the i18n key agent_msg.status.<status>. */
export function messageStatus(status) {
  return MESSAGE_STATUSES.includes(status) ? status : "queued";
}

/** Client-side check before sending; returns an i18n key or "". */
export function messageProblem(text) {
  const body = String(text || "").trim();
  if (!body) return "agent_msg.empty";
  if (body.length > MESSAGE_MAX) return "agent_msg.too_long";
  return "";
}
