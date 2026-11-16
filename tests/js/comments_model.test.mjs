// Run: node --experimental-vm-modules --test tests/js/comments_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/comments-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "comments-model.js" });
  await module.link(() => { throw new Error("comments-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();

const c = (id, kind, created_at, extra = {}) => ({ id, kind, created_at, author: "user", ...extra });

test("threadComments orders by time and nests replies under their root", () => {
  const list = [
    c(3, "agent_reply", "2026-11-05T10:00:00+00:00", { reply_to: 1, author: "agent:codex" }),
    c(1, "comment", "2026-11-04T09:00:00+00:00"),
    c(2, "sent_back", "2026-11-04T12:00:00+00:00"),
    c(4, "agent_reply", "2026-11-06T08:00:00+00:00", { reply_to: 3 }),
    c(5, "agent_reply", "2026-11-06T09:00:00+00:00", { reply_to: 99 }),
  ];
  const threads = m.threadComments(list);
  assert.deepEqual(threads.map((t) => t.comment.id), [1, 2, 5]);
  assert.deepEqual(threads[0].replies.map((r) => r.id), [3, 4]);
  assert.deepEqual(threads[1].replies, []);
  assert.deepEqual(m.threadComments(undefined), []);
});

test("same timestamp falls back to id order", () => {
  const list = [c(2, "agent_reply", "2026-11-04T00:00:00+00:00", { reply_to: 1 }), c(1, "comment", "2026-11-04T00:00:00+00:00")];
  const threads = m.threadComments(list);
  assert.equal(threads.length, 1);
  assert.equal(threads[0].replies[0].id, 2);
});

test("openUserCount counts unresolved comments from people only", () => {
  const list = [
    c(1, "comment", "a"), c(2, "decided", "b", { resolved: true, resolved_at: "x" }),
    c(3, "agent_reply", "c"), c(4, "accepted", "d", { author_kind: "user" }),
  ];
  assert.equal(m.openUserCount(list), 2);
  assert.equal(m.openUserCount(null), 0);
});

test("authorKind and authorName", () => {
  assert.equal(m.authorKind({ kind: "agent_reply" }), "agent");
  assert.equal(m.authorKind({ kind: "comment" }), "user");
  assert.equal(m.authorKind({ kind: "comment", author_kind: "agent" }), "agent");
  assert.equal(m.authorName({ author: "agent:claude" }), "claude");
  assert.equal(m.authorName({ author: "alice" }), "alice");
  assert.ok(m.USER_KINDS.includes("sent_back"));
});
