// Run: node --test tests/js/node_card.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import {
  cardMetrics, applyCardMetrics, cleanCardWidth, titleLines, textUnits, CARD_KINDS, CARD_WIDTH_RANGE, CARD_LINE_H, THUMB,
} from "../../ark_studio/web/js/node-card.js";
import { existsSync } from "node:fs";
// These cases compare against the frozen Python server's sources; trees
// without ark_studio/python (the public export) skip them.
const noPython = !existsSync(new URL("../../ark_studio/python/arkstudio/", import.meta.url))
  && "the Python reference sources are not in this tree";

const image = (id) => ({ id, url: `media/${id}.png` });

test("width range matches the server's stored limits", { skip: noPython }, async () => {
  const py = await readFile(new URL("../../ark_studio/python/arkstudio/store.py", import.meta.url), "utf8");
  const min = Number(py.match(/^MIN_CARD_W = (\d+)/m)[1]);
  const max = Number(py.match(/^MAX_CARD_W = (\d+)/m)[1]);
  assert.deepEqual(CARD_WIDTH_RANGE, [min, max]);
});

test("short titles get the layout's minimum width and one line", () => {
  for (const kind of ["mind", "node"]) {
    const m = cardMetrics({ title: "A" }, { kind });
    assert.equal(m.w, CARD_KINDS[kind].minW);
    assert.equal(m.lines, 1);
    assert.equal(m.h, CARD_KINDS[kind].baseH);
  }
  assert.ok(CARD_KINDS.node.baseH > CARD_KINDS.mind.baseH, "node cards keep room for the footer");
});

test("long titles wrap; height grows by one line height per line, up to the clamp", () => {
  const long = "word ".repeat(40).trim();
  const m = cardMetrics({ title: long }, { kind: "node" });
  assert.equal(m.w, CARD_KINDS.node.autoMaxW, "automatic width stops growing");
  assert.ok(m.lines > 1);
  assert.equal(m.h, CARD_KINDS.node.baseH + (m.lines - 1) * CARD_LINE_H);
  const huge = cardMetrics({ title: "x".repeat(2000) }, { kind: "node" });
  assert.equal(huge.lines, CARD_KINDS.node.maxLines, "line clamp");
  // Explicit newlines count as lines; CJK is wider than Latin.
  assert.equal(titleLines("a\nb\nc", 200, 6), 3);
  assert.ok(textUnits("研究") > textUnits("ab"));
});

test("a wider card wraps into fewer lines; widths clamp to the layout range", () => {
  const title = "A fairly long research claim that needs a few lines to read";
  const narrow = cardMetrics({ title }, { kind: "node", customW: 150 });
  const wide = cardMetrics({ title }, { kind: "node", customW: 500 });
  assert.equal(narrow.w, 150);
  assert.equal(wide.w, 500);
  assert.ok(wide.lines < narrow.lines && wide.h < narrow.h);
  assert.equal(cardMetrics({ title }, { kind: "node", customW: 20 }).w, CARD_KINDS.node.minW);
  assert.equal(cardMetrics({ title }, { kind: "node", customW: 9000 }).w, CARD_KINDS.node.maxW);
});

test("image strip and pinned main image size the card like Mind", () => {
  const media = [image(1), image(2), { id: 3, url: "notes.pdf" }];
  const strip = cardMetrics({ title: "x", media }, { kind: "node" });
  assert.equal(strip.images.length, 2, "only images are shown");
  assert.equal(strip.imageW, 2 * (THUMB + 4) + 8);
  assert.equal(strip.w, strip.titleW + strip.imageW);
  assert.ok(strip.h >= THUMB + 12);
  assert.equal(cardMetrics({ title: "x", media }, { kind: "node", showImages: false }).images.length, 0);
  // A custom width includes the images: the title column shrinks.
  assert.equal(cardMetrics({ title: "x", media }, { kind: "node", customW: 400 }).titleW, 400 - strip.imageW);
  const pinned = cardMetrics({ title: "x", media, pinnedImage: { mediaId: 2, side: "top", w: 300, aspect: 1.5 } }, { kind: "node" });
  assert.equal(pinned.pinned.item.id, 2);
  assert.equal(pinned.w, 300);
  assert.equal(pinned.h, pinned.contentH + 200 + 8);
  // A pin to media that is no longer attached falls back to the strip.
  assert.equal(cardMetrics({ title: "x", media, pinnedImage: { mediaId: 99 } }, { kind: "node" }).pinned, null);
});

test("stored widths are cleaned; metrics copy onto the node", () => {
  assert.equal(cleanCardWidth(null), null);
  assert.equal(cleanCardWidth(""), null);
  assert.equal(cleanCardWidth(true), null);
  assert.equal(cleanCardWidth("abc"), null);
  assert.equal(cleanCardWidth(10), CARD_WIDTH_RANGE[0]);
  assert.equal(cleanCardWidth(9999), CARD_WIDTH_RANGE[1]);
  assert.equal(cleanCardWidth(321.27), 321.3);
  const node = applyCardMetrics({ title: "x" }, cardMetrics({ title: "x" }, { kind: "node", customW: 300 }));
  assert.equal(node.w, 300);
  assert.equal(node.h, CARD_KINDS.node.baseH);
  assert.equal(node.pinnedMeta, null);
});

test("Mind sizing is unchanged by the extraction", () => {
  // Golden values from the previous inline mindLayout metrics.
  assert.deepEqual(
    (({ w, h, titleW, imageW }) => ({ w, h, titleW, imageW }))(cardMetrics({ title: "Predictable policies" }, { kind: "mind" })),
    { w: 34 + 20 * 7, h: 38, titleW: 34 + 20 * 7, imageW: 0 });
  const withImages = cardMetrics({ title: "x", media: [image(1)] }, { kind: "mind", customW: 300 });
  assert.equal(withImages.titleW, 300 - (THUMB + 4 + 8));
});
