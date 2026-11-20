// Experimental: send a message to your agent from ARK (T-0044).  Behind the
// Settings > Advanced switch "Experimental: message agents" (this browser,
// default off).  A message goes either to an agent that ARK started and that
// is running now (the session's steer channel: stdin between turns, or the
// next turn) or into ARK's message queue, which agents read through
// GET /api/agent/v1/updates (`ark-server agent watch`) and answer there.
// Design and limits: docs/development/agent-messaging.md.

import { h, relativeTime } from "./dom.js";
import { t } from "./i18n.js";
import { api } from "./api.js";
import { openModal, toast } from "./components.js";
import {
  AGENT_MESSAGES_KEY, MESSAGE_MAX, messagesEnabledIn, messageTargets, defaultTarget, sessionOf,
  messageStatus, messageProblem,
} from "./agent-messages-model.js";

export const AGENT_MESSAGES_CHANGED = "ark:agent-messages-changed";

export function agentMessagesEnabled() {
  try { return messagesEnabledIn(localStorage); } catch { return false; }
}

export function setAgentMessagesEnabled(on) {
  try { localStorage.setItem(AGENT_MESSAGES_KEY, on ? "1" : "0"); } catch { /* storage unavailable */ }
  window.dispatchEvent(new CustomEvent(AGENT_MESSAGES_CHANGED, { detail: { on: Boolean(on) } }));
}

/** The node menu entry, or null while the switch is off. */
export function agentMessageMenuItem(ctx, unit) {
  if (!agentMessagesEnabled()) return null;
  const uid = String(unit?.uid || unit?.id || "");
  return { label: t("agent_msg.menu"), icon: "message",
    onClick: () => openAgentMessage({ uid, title: unit?.title || "", projectId: ctx?.S?.project?.id || "" }) };
}

/** The Home button, or null while the switch is off. */
export function agentMessageButton(ctx) {
  if (!agentMessagesEnabled()) return null;
  return h("button", { class: "btn sm ghost", type: "button", "data-agent-message": "",
    onclick: () => openAgentMessage({ projectId: ctx?.S?.project?.id || "" }) },
    t("agent_msg.button"));
}

function threadView(messages) {
  if (!messages.length) return h("p", { class: "muted" }, t("agent_msg.none_yet"));
  return h("div", { class: "agent-msg-thread" }, ...messages.slice(0, 8).map((m) => h("div", { class: "agent-msg-item" },
    h("div", { class: "row" },
      h("span", { class: "chip" }, t(`agent_msg.status.${messageStatus(m.status)}`)),
      h("span", { class: "muted" }, relativeTime(m.created_at))),
    h("div", { class: "agent-msg-body" }, m.body),
    ...(Array.isArray(m.thread) ? m.thread : []).map((r) => h("div", { class: "agent-msg-reply" },
      h("b", null, t("agent_msg.reply_from", { who: String(r.by || "").replace(/^agent:/, "") })), " ", r.body)))));
}

/** The dialog; `uid` ties the message to a node ("Message the agent working on this"). */
export async function openAgentMessage({ uid = "", title = "", projectId = "" } = {}) {
  const [tokens, sessions, history] = await Promise.all([
    api.agentTokens().then((r) => r.tokens || []).catch(() => []),
    api.agentSessions().then((r) => r.sessions || []).catch(() => []),
    api.agentMessages(uid).then((r) => r.messages || []).catch(() => []),
  ]);
  const targets = messageTargets({ tokens, sessions, projectId, unitUid: uid });
  const label = (x) => {
    if (x.kind === "mine") return t("agent_msg.target_mine");
    if (x.kind === "session") return t(x.channel === "stdin" ? "agent_msg.target_session_now" : "agent_msg.target_session_next", { name: x.label });
    return t("agent_msg.target_token", { name: x.label });
  };
  const select = h("select", { class: "select", "aria-label": t("agent_msg.to") },
    ...targets.map((x) => h("option", { value: x.value }, label(x))));
  select.value = defaultTarget(targets, uid);
  const input = h("textarea", { class: "textarea", style: { minHeight: "96px" }, maxlength: String(MESSAGE_MAX),
    placeholder: t("agent_msg.placeholder"), "aria-label": t("agent_msg.text") });
  const how = h("p", { class: "muted small" });
  const paintHow = () => { how.textContent = t(sessionOf(select.value) ? "agent_msg.how_session" : "agent_msg.how_queue"); };
  select.addEventListener("change", paintHow);
  paintHow();
  const threadHost = h("div", null, threadView(history));
  const body = h("div", { class: "agent-msg" },
    uid ? h("p", null, t("agent_msg.about", { uid, title })) : null,
    h("label", { class: "field" }, h("span", null, t("agent_msg.to")), select),
    how,
    input,
    h("p", { class: "muted small" }, t("agent_msg.no_secrets")),
    h("h4", null, t("agent_msg.recent")),
    threadHost);
  openModal({
    title: t("agent_msg.title"),
    body,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("agent_msg.send"), primary: true,
        onClick: async () => {
          const text = input.value.trim();
          const problem = messageProblem(text);
          if (problem) { toast(t(problem), "err"); return false; }
          try {
            const session = sessionOf(select.value);
            if (session) {
              await api.sessionSteer(session, uid ? `${text}\n\n${t("agent_msg.session_node", { uid })}` : text);
              toast(t("agent_msg.sent_session"));
            } else {
              await api.sendAgentMessage({ body: text, unit: uid || undefined, target: select.value });
              toast(t("agent_msg.sent_queue"));
            }
          } catch (err) {
            toast(err.message, "err");
            return false;
          }
          return true;
        },
      },
    ],
  });
}
