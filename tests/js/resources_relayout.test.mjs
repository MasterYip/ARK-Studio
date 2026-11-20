// Run: node --test tests/js/resources_relayout.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { separateCards } from "../../ark_studio/web/js/resources-model.js";

const box = (id, x, top, h, w = 300) => ({ id, x, y: top + h / 2, w, h });
const overlaps = (a, b, gap = 24) => Math.abs(a.x - b.x) < (a.w + b.w) / 2 + gap / 2
  && a.y - a.h / 2 < b.y + b.h / 2 + gap && b.y - b.h / 2 < a.y + a.h / 2 + gap;

test("an expanded card pushes the cards below it down; nothing overlaps", () => {
  const cards = [box("a", 0, 0, 400), box("b", 0, 120, 100), box("c", 0, 240, 100), box("far", 2000, 0, 100)];
  const out = separateCards(cards);
  const placed = cards.map((c) => ({ ...c, ...out.get(c.id) }));
  for (let i = 0; i < placed.length; i += 1)
    for (let j = i + 1; j < placed.length; j += 1)
      assert.ok(!overlaps(placed[i], placed[j]) || placed[i].id === placed[j].id, `${placed[i].id}/${placed[j].id} overlap`);
  assert.deepEqual(out.get("far"), { x: 2000, y: 50 }, "cards in another column do not move");
  assert.equal(out.get("a").y, 200, "the top card stays");
});

test("a hand-placed card stays; the other card moves", () => {
  const cards = [box("auto", 0, 0, 200), box("manual", 0, 100, 100)];
  const out = separateCards(cards, new Set(["manual"]));
  assert.deepEqual(out.get("manual"), { x: 0, y: 150 });
  assert.ok(out.get("auto").y + 100 + 24 <= 100 + 0.001, "the auto card moved above the hand-placed one");
});

test("folding or expanding re-lays out the cards", async () => {
  const src = await readFile(new URL("../../ark_studio/web/js/resources-view.js", import.meta.url), "utf8");
  assert.match(src, /function applyFold\(next\) \{[^}]*rebuild\(\{ relayout: true \}\)/s);
  assert.match(src, /separateCards\(\[\.\.\.state\.layout\.values\(\)\], pinned\)/);
});
