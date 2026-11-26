// Run: node --experimental-vm-modules --test tests/js/explorer_sections_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/explorer-sections-model.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "explorer-sections-model.js" });
await module.link(() => { throw new Error("explorer-sections-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;
const open = (state) => m.sectionLayout(state).filter((item) => item.open).map((item) => item.id);

test("defaults and stored state: Files, Outline, Timeline open, Search folded", () => {
  const s = m.parseSections("");
  assert.deepEqual(m.SECTION_IDS, ["files", "outline", "timeline", "search"]);
  assert.deepEqual(open(s), ["files", "outline", "timeline"]);
  const stored = m.parseSections(JSON.stringify({ collapsed: { files: true, search: false, junk: 1 }, sizes: { files: 1, outline: 400 }, max: "nope" }));
  assert.deepEqual(open(stored), ["outline", "timeline", "search"]);
  assert.equal(stored.sizes.files, 320, "out of range sizes fall back");
  assert.equal(stored.sizes.outline, 400);
  assert.equal(stored.max, "");
  assert.deepEqual(m.parseSections("{bad json"), m.parseSections(""));
});

test("collapse, maximize, restore and reveal", () => {
  let s = m.parseSections("");
  s = m.toggleSection(s, "outline");
  assert.deepEqual(open(s), ["files", "timeline"]);
  s = m.toggleMaximized(s, "timeline");
  assert.deepEqual(open(s), ["timeline"]);
  assert.equal(m.sectionLayout(s).find((item) => item.id === "timeline").weight, 1);
  // Opening another section ends the maximized state; stored collapses come back.
  s = m.toggleSection(s, "search");
  assert.equal(s.max, "");
  assert.deepEqual(open(s), ["files", "timeline", "search"]);
  s = m.toggleMaximized(m.toggleMaximized(s, "files"), "files");
  assert.deepEqual(open(s), ["files", "timeline", "search"], "maximize twice restores");
  // Collapsing the maximized section restores the others.
  s = m.toggleSection(m.toggleMaximized(s, "search"), "search");
  assert.equal(s.max, "");
  assert.deepEqual(open(s), ["files", "timeline"]);
  // Reveal (Ctrl+Shift+F, Outline from the palette).
  s = m.revealSection(m.toggleMaximized(s, "files"), "outline");
  assert.equal(s.max, "");
  assert.ok(open(s).includes("outline"));
});

test("resizing moves the line between the open neighbours only", () => {
  let s = m.toggleSection(m.parseSections(""), "outline");     // files, timeline open
  const heights = { files: 300, outline: 26, timeline: 200, search: 26 };
  // The sash above Timeline resizes Files and Timeline (Outline is folded).
  assert.deepEqual(m.resizeAt(s, "timeline", heights, 50), { ...s.sizes, files: 350, timeline: 150 });
  // Clamped to the minimum.
  const clamped = m.resizeAt(s, "timeline", heights, 1000);
  assert.equal(clamped.timeline, m.SECTION_MIN);
  assert.equal(clamped.files, 500 - m.SECTION_MIN);
  // Nothing to share: above the first section, nothing open below, or maximized.
  assert.equal(m.resizeAt(s, "files", heights, 10), null);
  assert.equal(m.resizeAt(s, "search", heights, 10), null);
  s = m.toggleMaximized(s, "files");
  assert.equal(m.resizeAt(s, "timeline", heights, 10), null);
});
