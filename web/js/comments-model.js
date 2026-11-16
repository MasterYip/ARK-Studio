// Pure helpers for unit comments (dependency-free; see
// tests/js/comments_model.test.mjs).  Comments are rows on a unit (server:
// Store.comments): user kinds come from people, `agent_reply` from agents.

export const USER_KINDS = ["comment", "sent_back", "decided", "accepted"];

/** "user" | "agent" for one comment. */
export function authorKind(comment = {}) {
  if (comment.author_kind === "agent" || comment.author_kind === "user") return comment.author_kind;
  return comment.kind === "agent_reply" ? "agent" : "user";
}

/** Unresolved comments from people: the ones an agent still has to act on. */
export function openUserCount(comments) {
  return (Array.isArray(comments) ? comments : [])
    .filter((c) => authorKind(c) === "user" && !c.resolved && !c.resolved_at).length;
}

function byTime(a, b) {
  const at = String(a.created_at || ""), bt = String(b.created_at || "");
  if (at !== bt) return at < bt ? -1 : 1;
  return (Number(a.id) || 0) - (Number(b.id) || 0);
}

/**
 * Threads in time order: `[{ comment, replies: [...] }]`.  A comment whose
 * `reply_to` is on the list is shown under its root (one level deep: a reply
 * to a reply joins the same thread); an unknown `reply_to` starts a thread.
 */
export function threadComments(comments) {
  const list = (Array.isArray(comments) ? comments : []).slice().sort(byTime);
  const byId = new Map(list.map((c) => [c.id, c]));
  const rootOf = (c) => {
    let cur = c;
    const seen = new Set();
    while (cur.reply_to != null && byId.has(cur.reply_to) && !seen.has(cur.id)) {
      seen.add(cur.id);
      cur = byId.get(cur.reply_to);
    }
    return cur;
  };
  const threads = [];
  const index = new Map();
  for (const c of list) {
    const root = rootOf(c);
    if (root === c) {
      const thread = { comment: c, replies: [] };
      threads.push(thread);
      index.set(c.id, thread);
    } else {
      index.get(root.id)?.replies.push(c);
    }
  }
  return threads;
}

/** Author label: "agent:codex" -> "codex"; people keep their username. */
export function authorName(comment = {}) {
  const author = String(comment.author || "");
  if (author.startsWith("agent:")) return author.slice(6);
  return author;
}
