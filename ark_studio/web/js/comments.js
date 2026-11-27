// "Comments" section for one unit: the inspector (read and edit views) and
// Editor node tabs.  Comments are their own rows on the unit (server:
// Store.comments), so an agent rewriting the body cannot lose them.  People
// add comments here or from Home "Needs you"; agents answer with
// `agent_reply` comments that show threaded under the comment they answer.
// Nothing is deleted: a comment is resolved or reopened.

import { h, clear, fmtDateTime, relativeTime } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { toast } from "./components.js";
import { authorKind, authorName, openUserCount, threadComments } from "./comments-model.js";
import { userAvatar, userName } from "./users.js";

/**
 * @param {object} unit  unit payload with `comments` (GET /api/units/<id>)
 * @param {object} opts  `heading` "h3" | "h4", `className` for the section
 *   frame, `onChange(comments)` after a write, `ctx` to refresh Home counts.
 */
export function commentsSection(unit, {
  heading = "h4", className = "link-section", onChange = null, ctx = null, review = null,
} = {}) {
  let comments = Array.isArray(unit.comments) ? unit.comments : [];
  const count = h("span", { class: "count comments-count" });
  const openBadge = h("span", { class: "comments-open-badge" });
  const list = h("div", { class: "comments-list" });
  const input = h("textarea", {
    class: "textarea comments-input", rows: "2",
    placeholder: t("comments.placeholder"), "aria-label": t("comments.add"),
  });
  const addBtn = h("button", { class: "btn sm primary", type: "button", dataset: { commentAction: "add" } }, icon("message", "sm"), t("comments.add"));

  const changed = (fresh) => {
    comments = fresh;
    unit.comments = fresh;
    paint();
    onChange?.(fresh);
    // Home "Needs you" shows unresolved counts; keep it in step.
    if (ctx?.applyProject && (ctx.S?.needsYou || []).some((item) => item.id === unit.id)) {
      api.project().then((data) => ctx.applyProject(data)).catch(() => {});
    }
  };

  const submit = async () => {
    const text = input.value.trim();
    if (!text) { input.focus(); return; }
    addBtn.disabled = true;
    try {
      const data = await api.addComment(unit.id, { body: text, kind: "comment" });
      input.value = "";
      changed(data.comments || []);
      toast(t("comments.added"), "ok");
    } catch (err) {
      toast(err.message, "err");
    } finally {
      addBtn.disabled = false;
    }
  };
  addBtn.addEventListener("click", submit);
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); submit(); }
  });

  const toggle = async (comment, button) => {
    button.disabled = true;
    try {
      const data = await api.resolveComment(unit.id, comment.id, !comment.resolved);
      changed(data.comments || []);
    } catch (err) {
      toast(err.message, "err");
      button.disabled = false;
    }
  };

  const item = (comment, reply = false) => {
    const who = authorKind(comment);
    const open = who === "user" && !comment.resolved;
    const name = authorName(comment);
    const resolveBtn = who === "user" ? h("button", {
      class: "btn sm ghost comment-resolve", type: "button",
      title: comment.resolved ? t("comments.reopen_hint") : t("comments.resolve_hint"),
      onclick: (event) => toggle(comment, event.currentTarget),
    }, icon(comment.resolved ? "refresh" : "check", "sm"), comment.resolved ? t("comments.reopen") : t("comments.resolve")) : null;
    return h("article", {
      class: `comment author-${who} kind-${comment.kind}${open ? " open" : ""}${comment.resolved ? " resolved" : ""}${reply ? " reply" : ""}`,
      dataset: { commentId: comment.id, kind: comment.kind },
    },
      h("div", { class: "comment-head" },
        h("span", { class: `comment-chip author-${who}`, title: name || undefined },
          who === "agent" ? icon("cpu", "sm")
            : (name && name !== "user" ? userAvatar(name, { size: "xs", title: false }) : icon("user", "sm")),
          name && name !== "user" && name !== "agent"
            ? (who === "user" ? userName(name) : name)
            : t(`comments.author.${who}`)),
        h("span", { class: `comment-kind kind-${comment.kind}` }, t(`comments.kind.${comment.kind}`)),
        comment.resolved ? h("span", { class: "comment-state", title: fmtDateTime(comment.resolved_at) }, t("comments.resolved")) : null,
        h("time", { class: "comment-time", title: fmtDateTime(comment.created_at) }, relativeTime(comment.created_at)),
        resolveBtn),
      h("div", { class: "comment-body" }, comment.body));
  };

  const paint = () => {
    clear(list);
    const threads = threadComments(comments);
    count.textContent = String(comments.length);
    const n = openUserCount(comments);
    openBadge.textContent = n ? t("comments.open_count", { n }) : "";
    openBadge.hidden = !n;
    if (!threads.length) {
      list.appendChild(h("p", { class: "muted comments-empty" }, t("comments.empty")));
      return;
    }
    for (const thread of threads) {
      list.appendChild(h("div", { class: "comment-thread" },
        item(thread.comment),
        thread.replies.length ? h("div", { class: "comment-replies" }, thread.replies.map((r) => item(r, true))) : null));
    }
  };
  paint();

  return h("section", { class: `${className} comments-section`.trim(), dataset: { unitRef: unit.uid } },
    h(heading, null, t("comments.title"), count, openBadge),
    list,
    h("div", { class: "comments-add" },
      input,
      h("span", { class: "muted comments-hint" }, t("comments.hint")),
      // One row: Accept / Send back (a unit in review) and Add comment.  The
      // text typed above is the accept note, the send-back reason or the
      // comment (`review`: { accept(text, clear), sendBack(text, clear) }).
      h("div", { class: "comments-actions", dataset: review ? { reviewActions: unit.uid } : {} },
        decideButton(unit, review, input, addBtn, changed),
        ...commentActionButtons(review, addBtn, () => input.value.trim(), () => { input.value = ""; }))));
}

/**
 * An open decision gets "Mark decided" before Add comment: the text typed
 * above (optional) is recorded as the decision and the node is closed.
 */
function decideButton(unit, review, input, addBtn, changed) {
  if (review || unit.type !== "decision" || unit.status === "done") return null;
  const button = h("button", { class: "btn sm primary", type: "button", dataset: { commentAction: "decide" } },
    icon("check", "sm"), t("needs.decide"));
  addBtn.classList.remove("primary");
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const text = input.value.trim();
      let comments = unit.comments || [];
      if (text) comments = (await api.addComment(unit.id, { body: text, kind: "decided" })).comments || comments;
      await api.updateUnit(unit.id, { status: "done" });
      unit.status = "done";
      input.value = "";
      button.remove();
      addBtn.classList.add("primary");
      changed(comments);
      toast(t("needs.decided", { uid: unit.uid }), "ok");
    } catch (err) {
      toast(err.message, "err");
      button.disabled = false;
    }
  });
  return button;
}

/** The buttons of the comment row, in order (Accept, Send back, Add comment). */
export function commentActionButtons(review, addBtn, text = () => "", clearInput = () => {}) {
  addBtn.className = review ? "btn sm" : "btn sm primary";
  if (!review) return [addBtn];
  return [
    h("button", { class: "btn sm primary", type: "button", dataset: { reviewAction: "accept" },
      onclick: () => review.accept(text(), clearInput) }, icon("check", "sm"), t("needs.accept")),
    h("button", { class: "btn sm", type: "button", dataset: { reviewAction: "send-back" },
      onclick: () => review.sendBack(text(), clearInput) }, icon("arrowLeft", "sm"), t("needs.send_back")),
    addBtn,
  ];
}
