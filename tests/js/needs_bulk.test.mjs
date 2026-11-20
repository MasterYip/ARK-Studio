// Home "Needs you": Accept several (bulk accept of reviews).
// Run: node tests/js/needs_bulk.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { bulkAcceptable, runBatch } from "../../ark_studio/web/js/needs-model.js";

test("only reviews are offered; decisions need their own answer", () => {
  const items = [
    { uid: "T-1", type: "task", kind: "review" },
    { uid: "D-1", type: "decision", kind: "decision" },
    { uid: "E-1", type: "experiment" },
    { title: "no uid" },
  ];
  assert.deepEqual(bulkAcceptable(items).map((i) => i.uid), ["T-1", "E-1"]);
  assert.deepEqual(bulkAcceptable(), []);
});

test("runBatch: bounded concurrency, order kept, failures do not stop the rest", async () => {
  let running = 0, peak = 0;
  const items = Array.from({ length: 9 }, (_, i) => ({ uid: `T-${i}` }));
  const { done, failed } = await runBatch(items, async (item) => {
    running += 1; peak = Math.max(peak, running);
    await new Promise((r) => setTimeout(r, 2));
    running -= 1;
    if (item.uid === "T-4") throw new Error("nope");
  }, 3);
  assert.ok(peak <= 3, `peak ${peak}`);
  assert.deepEqual(done.map((i) => i.uid), ["T-0", "T-1", "T-2", "T-3", "T-5", "T-6", "T-7", "T-8"]);
  assert.equal(failed.length, 1);
  assert.equal(failed[0].item.uid, "T-4");
  assert.deepEqual(await runBatch([], async () => {}), { done: [], failed: [] });
});

test("strings exist in both languages", async () => {
  for (const file of ["i18n-en.js", "i18n-zh.js"]) {
    const src = await readFile(new URL(`../../ark_studio/web/js/${file}`, import.meta.url), "utf8");
    for (const key of ["bulk_accept", "bulk_title", "bulk_hint", "bulk_all", "bulk_selected", "bulk_note", "bulk_action", "bulk_none", "bulk_done", "bulk_failed"]) {
      assert.ok(src.includes(`"needs.${key}"`), `${file} needs.${key}`);
    }
  }
});
