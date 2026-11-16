// Run: node --experimental-vm-modules --test tests/js/node_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load() {
  const source = await readFile(new URL("../../web/js/node-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "node-model.js" });
  await module.link(() => { throw new Error("node-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await load();
const unit = {
  id: 3, uid: "IDEA-0003", title: "Idea", body: "x", type: "idea", status: "active", importance: "p1",
  tags: ["type:idea", "status:active", "importance:p1", "ml", "ml", "vision"], start_at: "2026-01-01", end_at: null,
  links_out: [{ id: 9, relation: "supports", target: { uid: "CLAIM-0001", title: "C" } }],
  links_in: [{ id: 10, relation: "assigned_to", source: { uid: "TASK-0002", title: "T" } }, { id: 11, source: {} }],
};

test("draft from unit strips reserved and duplicate tags", () => {
  const draft = m.draftFromUnit(unit);
  assert.deepEqual(draft.tags, ["ml", "vision"]);
  assert.equal(draft.end_at, "");
  assert.equal(m.draftChanged(draft, m.draftFromUnit(unit)), false);
  assert.equal(m.draftChanged(draft, { ...draft, body: "y" }), true);
  assert.equal(m.draftChanged(draft, { ...draft, tags: ["vision", "ml"] }), true);
  assert.deepEqual(m.changedFields(draft, { ...draft, title: "New", tags: ["ml"] }), ["title", "tags"]);
});

test("validation mirrors the server rules", () => {
  const draft = m.draftFromUnit(unit);
  assert.deepEqual(m.validateDraft(draft), []);
  assert.equal(m.validateDraft({ ...draft, title: "   " })[0].key, "node.title_required");
  assert.equal(m.validateDraft({ ...draft, title: "x".repeat(241) })[0].key, "node.title_too_long");
  assert.equal(m.validateDraft({ ...draft, end_at: "2025-12-31" })[0].field, "end_at");
  assert.equal(m.validateDraft({ ...draft, body: "x".repeat(m.MAX_BODY + 1) })[0].field, "body");
});

test("patch body matches the inspector's PATCH shape", () => {
  const patch = m.patchFromDraft({ ...m.draftFromUnit(unit), title: "  Spaced  " });
  assert.deepEqual(Object.keys(patch).sort(), ["body", "end_at", "importance", "start_at", "status", "tags", "title", "type"]);
  assert.equal(patch.title, "Spaced");
  assert.equal(patch.end_at, null);
  assert.equal(patch.start_at, "2026-01-01");
});

test("tags, modes, links and media helpers", () => {
  assert.deepEqual(m.addTag(["a"], " b, "), ["a", "b"]);
  assert.deepEqual(m.addTag(["a"], "a"), ["a"]);
  assert.deepEqual(m.addTag(["a"], "status:done"), ["a"]);
  assert.equal(m.normalizeMode("bogus"), "split");
  assert.equal(m.normalizeMode("split", { phone: true }), "preview");
  assert.equal(m.normalizeMode("source", { phone: true }), "source");
  const rows = m.linkRows(unit);
  assert.deepEqual(rows.map((row) => [row.direction, row.uid, row.relation]), [["out", "CLAIM-0001", "supports"], ["in", "TASK-0002", "assigned_to"]]);
  assert.equal(m.mediaMarkdown({ id: 5, kind: "video", caption: "Demo [v1]" }), "![Demo  v1](media:5)");
  assert.equal(m.mediaMarkdown({ id: 6, kind: "file", caption: "Paper" }), "[Paper](media:6)");
  assert.equal(m.mediaKinds([{ id: 1, is_image: true }, { id: 2, kind: "Video" }]).get(2), "video");
  assert.equal(m.galleryKind({ url: "a/clip.MP4" }), "video");
  assert.equal(m.galleryKind({ url: "a/x.png", kind: "file" }), "image");
  assert.equal(m.galleryKind({ url: "a/chart.html" }), "html");
  assert.equal(m.galleryKind({ url: "a/p.pdf" }), "pdf");
  assert.equal(m.galleryKind({ url: "zotero://select/1" }), "link");
  assert.equal(m.galleryKind({ url: "a/data.bin" }), "file");
});

test("gantt span: open units with only start_at run to today", () => {
  const today = "2026-11-06";
  assert.equal(m.ganttSpan({ status: "open" }, today), null);
  assert.deepEqual(m.ganttSpan({ status: "active", start_at: "2026-11-01" }, today),
    { start: "2026-11-01", end: today, point: false, ongoing: true });
  assert.deepEqual(m.ganttSpan({ status: "open", start_at: today }, today),
    { start: today, end: today, point: false, ongoing: true });
  // Planned start in the future, or done without an end: a point.
  assert.equal(m.ganttSpan({ status: "open", start_at: "2026-12-01" }, today).point, true);
  assert.equal(m.ganttSpan({ status: "done", start_at: "2026-11-01" }, today).point, true);
  assert.deepEqual(m.ganttSpan({ status: "done", start_at: "2026-11-01", end_at: "2026-11-03" }, today),
    { start: "2026-11-01", end: "2026-11-03", point: false, ongoing: false });
  assert.deepEqual(m.ganttSpan({ status: "open", end_at: "2026-11-09" }, today),
    { start: "2026-11-09", end: "2026-11-09", point: true, ongoing: false });
});
