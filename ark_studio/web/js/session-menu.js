// Close and Delete for a session, shared by the session menus (the list, the
// header and main tab on Execute › Runs, the Sessions sidebar view).

import { h } from "./dom.js";
import { t } from "./i18n.js";
import { openModal } from "./components.js";
import { closeAgentSession, deleteAgentSession, sendSessionMessage, stopAgentSession } from "./session-stream.js";

/** Ask before deleting; resolves to null (cancelled) or { withFiles }. */
export function confirmDeleteSession(title) {
  return new Promise((resolve) => {
    const files = h("input", { type: "checkbox", "data-delete-files": "" });
    openModal({
      title: t("session.delete_title"),
      body: h("div", { class: "session-delete-body" },
        h("p", null, t("session.delete_body", { title })),
        h("label", { class: "row tight session-delete-files" }, files, h("span", null, t("session.delete_files")))),
      actions: [
        { label: t("action.cancel"), ghost: true, onClick: () => resolve(null) },
        { label: t("tasks.delete"), danger: true, onClick: () => resolve({ withFiles: files.checked }) },
      ],
      onClose: () => resolve(null),
    });
  });
}

/** Menu items: 关闭 / End and 删除 / Delete (owner or project admin; the server checks). */
export function closeDeleteItems(ctx, session, title, { onDeleted = null } = {}) {
  return [
    !session.closed ? {
      label: t("session.menu.close"), icon: "x", onClick: async () => {
        try {
          await closeAgentSession(session.id);
          ctx.toast?.(t("session.toast.closed"), "ok");
        } catch (err) { ctx.toast?.(err.message, "err"); }
      },
    } : null,
    {
      label: t("tasks.delete"), icon: "trash", danger: true, onClick: async () => {
        const choice = await confirmDeleteSession(title);
        if (!choice) return;
        try {
          await deleteAgentSession(session.id, choice);
          ctx.toast?.(t(choice.withFiles ? "session.toast.deleted_files" : "session.toast.deleted"), "ok");
          onDeleted?.(session);
        } catch (err) { ctx.toast?.(err.message, "err"); }
      },
    },
  ].filter(Boolean);
}

/**
 * "Stop background tasks…" (a session between turns whose sub-agents or
 * background tasks still run): stop them all (the agent is asked to, the
 * session stays open), or end the session.
 */
export function stopBackgroundItems(session, { onStopped = () => {}, onError = () => {} } = {}) {
  return [
    {
      label: t("session.stop_background_all"), icon: "stop", title: t("session.stop_background_all_hint"),
      onClick: async () => {
        try { onStopped(await sendSessionMessage(session, t("session.stop_background_message"), { mode: "steer" })); } catch (err) { onError(err); }
      },
    },
    {
      label: t("session.stop_end"), icon: "x", danger: true, title: t("session.stop_end_hint"),
      onClick: async () => {
        try { await stopAgentSession(session.id); onStopped(null); } catch (err) { onError(err); }
      },
    },
  ];
}
