// Run: node --experimental-vm-modules --test tests/js/setup_review_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/setup-review-model.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "setup-review-model.js" });
await module.link(() => { throw new Error("setup-review-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;

const st = (project, user) => ({ layers: { project, ...(user ? { user } : {}) } });

test("pages: project first, then my layer, with their modes", () => {
  const pages = m.reviewPages(st({ state: "proposed", digest: "p1" }, { state: "proposed", digest: "u1" }));
  assert.deepEqual(pages.map((p) => [p.layer, p.mode, p.digest, p.canConfirm]),
    [["project", "confirm", "p1", true], ["user", "confirm", "u1", true]]);
});

test("non-admins see the project layer read-only", () => {
  const pages = m.reviewPages(st({ state: "proposed", can_confirm: false }, { state: "proposed" }));
  assert.equal(pages[0].mode, "readonly");
  assert.equal(pages[0].canConfirm, false);
  assert.equal(m.firstPage(pages), 1);
});

test("modes for confirmed, draft, missing layers", () => {
  assert.equal(m.pageMode(st({ state: "verified" }), "project"), "done");
  assert.equal(m.pageMode(st({ state: "confirmed" }), "project"), "done");
  assert.equal(m.pageMode(st({ state: "draft" }), "project"), "pending");
  assert.equal(m.pageMode(st({ state: "stale" }), "project"), "pending");
  assert.equal(m.pageMode(st({ state: "unset" }), "project"), "pending");
  assert.equal(m.pageMode(st({ state: "confirmed" }), "user"), "none");
  assert.equal(m.pageMode({ state: "proposed" }, "project"), "confirm"); // older payload without layers
  assert.equal(m.pageMode({}, "project"), "none");
});

test("first page: requested layer, else first confirmable, else 1/2", () => {
  const pages = m.reviewPages(st({ state: "confirmed" }, { state: "proposed" }));
  assert.equal(m.firstPage(pages), 1);
  assert.equal(m.firstPage(pages, "project"), 0);
  assert.equal(m.firstPage(pages, "user"), 1);
  assert.equal(m.firstPage(m.reviewPages(st({ state: "draft" }, { state: "draft" }))), 0);
});

test("step labels and paging", () => {
  assert.equal(m.stepLabel(0, 2), "1/2");
  assert.equal(m.stepLabel(1, 2), "2/2");
  assert.equal(m.nextIndex(0, 2), 1);
  assert.equal(m.nextIndex(1, 2), 2); // the result page
  assert.equal(m.nextIndex(2, 2), 2);
});

test("Home offers review when a layer awaits this user or an admin's draft", () => {
  assert.equal(m.needsReview(st({ state: "proposed" }, { state: "confirmed" })), true);
  assert.equal(m.needsReview(st({ state: "proposed", can_confirm: false }, { state: "draft" })), false);
  assert.equal(m.needsReview(st({ state: "proposed", can_confirm: false }, { state: "proposed" })), true);
  assert.equal(m.needsReview(st({ state: "draft" }, { state: "draft" })), true);
  assert.equal(m.needsReview(st({ state: "draft", can_confirm: false }, { state: "draft" })), false);
  assert.equal(m.needsReview(st({ state: "verified" }, { state: "confirmed" })), false);
});

test("combined result", () => {
  const pages = m.reviewPages(st({ state: "proposed", can_confirm: false }, { state: "proposed" }));
  assert.deepEqual(m.reviewOutcome(pages, new Set(["user"])),
    [{ layer: "project", result: "admin" }, { layer: "user", result: "confirmed" }]);
  assert.deepEqual(m.reviewOutcome(m.reviewPages(st({ state: "verified" }, { state: "proposed" }))),
    [{ layer: "project", result: "already" }, { layer: "user", result: "skipped" }]);
  assert.deepEqual(m.reviewOutcome(m.reviewPages(st({ state: "draft" }))),
    [{ layer: "project", result: "pending" }, { layer: "user", result: "none" }]);
});
