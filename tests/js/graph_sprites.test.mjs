// Run: node --test tests/js/graph_sprites.test.mjs
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  SpriteCache, spriteBucket, MIN_BUCKET, MAX_BUCKET, MAX_SPRITE_PIXELS, ATLAS_SIZE,
} from "../../ark_studio/web/js/graph-sprites.js";

function fakeSurface(w, h) {
  const calls = [];
  const ctx = new Proxy({}, { get: (_, name) => (...args) => calls.push([name, ...args]) });
  return { width: w, height: h, calls, getContext: () => ctx };
}

// Large jobs get their own bitmap; small ones go to atlas pages.
const big = (paint = (ctx) => ctx.fillText("x", 0, 0)) => ({ w: 300, h: 100, paint });
const small = (paint = (ctx) => ctx.fillText("x", 0, 0)) => ({ w: 60, h: 20, paint });
const PAGE = ATLAS_SIZE * ATLAS_SIZE * 4;

test("buckets are powers of two at or above the scale, clamped", () => {
  assert.equal(spriteBucket(1), 1);
  assert.equal(spriteBucket(1.01), 2);
  assert.equal(spriteBucket(0.6), 1);
  assert.equal(spriteBucket(0.5), 0.5);
  assert.equal(spriteBucket(0.3), 0.5);
  assert.equal(spriteBucket(0.0001), MIN_BUCKET);
  assert.equal(spriteBucket(100), MAX_BUCKET);
  // Drawing scale stays within (0.5, 1]: sprites are only ever downscaled.
  for (const scale of [0.07, 0.15, 0.3, 0.6, 1, 1.4, 2.5, 3.9]) {
    const ratio = scale / spriteBucket(scale);
    assert.ok(ratio > 0.5 && ratio <= 1, `${scale}`);
  }
});

test("a miss queues the job; pump rasterises it at the bucket", () => {
  const cache = new SpriteCache({ createSurface: fakeSurface });
  assert.equal(cache.get("a", "h1", 2, big()), null);
  assert.equal(cache.queue.size, 1);
  cache.get("a", "h1", 2, big()); // queued once
  assert.equal(cache.queue.size, 1);
  assert.equal(cache.pump(100), 0);
  const entry = cache.get("a", "h1", 2, big());
  assert.ok(entry);
  assert.equal(entry.surface.width, 600);
  assert.equal(entry.surface.height, 200);
  assert.deepEqual(entry.surface.calls[0], ["scale", 2, 2], "painted in world units at the bucket");
  assert.equal(cache.bytes, 600 * 200 * 4);
  assert.equal(cache.stats.hits, 1);
});

test("small sprites share an atlas page, each clipped to its own slot", () => {
  const cache = new SpriteCache({ createSurface: fakeSurface });
  const a = cache.rasterise({ id: "a", hash: "h", bucket: 1, ...small() });
  const b = cache.rasterise({ id: "b", hash: "h", bucket: 1, ...small() });
  assert.equal(a.surface, b.surface, "one page");
  assert.equal(a.surface.width, ATLAS_SIZE);
  assert.equal(cache.pages.length, 1);
  assert.equal(cache.bytes, PAGE);
  assert.ok(b.sx >= a.sx + 60 + 2 || b.sy >= a.sy + 20 + 2, "slots do not overlap (2 px gutter)");
  const calls = a.surface.calls.map(([name]) => name);
  assert.deepEqual(calls.slice(0, 7), ["save", "clearRect", "beginPath", "rect", "clip", "translate", "scale"]);
});

test("a nearby bucket stands in while the exact one rasterises", () => {
  const cache = new SpriteCache({ createSurface: fakeSurface });
  cache.rasterise({ id: "a", hash: "h", bucket: 1, ...big() });
  cache.rasterise({ id: "a", hash: "h", bucket: 4, ...big() });
  // Prefer a sharper (larger) bucket over a blurrier one.
  assert.equal(cache.get("a", "h", 2, big()).bucket, 4);
  assert.equal(cache.queue.size, 1);
});

test("new content drops the item's bitmaps and queued jobs", () => {
  const cache = new SpriteCache({ createSurface: fakeSurface });
  cache.rasterise({ id: "a", hash: "old", bucket: 1, ...big() });
  cache.get("a", "old", 2, big());
  assert.equal(cache.get("a", "new", 1, big()), null, "old picture never shown for new content");
  assert.equal(cache.bytes, 0);
  assert.equal([...cache.queue.values()].every((item) => item.hash === "new"), true);
});

test("LRU eviction keeps memory under the cap; atlas pages go as a whole", () => {
  const one = 300 * 100 * 4;
  const cache = new SpriteCache({ createSurface: fakeSurface, maxBytes: 3 * one });
  for (const id of ["a", "b", "c"]) cache.rasterise({ id, hash: "h", bucket: 1, ...big() });
  cache.get("a", "h", 1); // a is now the most recently used
  cache.rasterise({ id: "d", hash: "h", bucket: 1, ...big() });
  // Evicts least recently drawn first, down to 80% of the cap.
  assert.ok(cache.bytes <= cache.maxBytes * 0.8);
  assert.equal(cache.stats.evicted, 2);
  assert.equal(cache.get("b", "h", 1), null, "least recently used goes first");
  assert.equal(cache.get("c", "h", 1), null);
  assert.ok(cache.get("a", "h", 1), "recently drawn survives");
  assert.ok(cache.get("d", "h", 1));
  const atlas = new SpriteCache({ createSurface: fakeSurface, maxBytes: PAGE + one });
  atlas.rasterise({ id: "s1", hash: "h", bucket: 1, ...small() });
  atlas.rasterise({ id: "s2", hash: "h", bucket: 1, ...small() });
  atlas.rasterise({ id: "big", hash: "h", bucket: 1, ...big() });
  atlas.rasterise({ id: "big2", hash: "h", bucket: 1, ...big() });
  assert.equal(atlas.pages.length, 0, "the oldest unit, the whole page, was evicted");
  assert.equal(atlas.get("s1", "h", 1), null);
  assert.ok(atlas.get("big2", "h", 1));
});

test("pump respects its time budget; huge bitmaps are not cached", () => {
  let now = 0;
  const cache = new SpriteCache({ createSurface: fakeSurface, now: () => now });
  for (let i = 0; i < 10; i++) cache.get(`n${i}`, "h", 1, small(() => { now += 3; }));
  assert.equal(cache.pump(5), 8, "stops once the budget is spent");
  assert.equal(cache.stats.rasterised, 2);
  assert.equal(cache.cacheable(100, 100, 1), true);
  assert.equal(cache.cacheable(4000, 1000, 1), 4000 * 1000 <= MAX_SPRITE_PIXELS);
});

test("peek returns only an exact bitmap and never queues", () => {
  const cache = new SpriteCache({ createSurface: fakeSurface });
  assert.equal(cache.peek("a", "h1", 2), null);
  assert.equal(cache.queue.size, 0, "a miss is not queued");
  cache.rasterise({ id: "a", hash: "h1", bucket: 4, ...big() });
  assert.equal(cache.peek("a", "h1", 2), null, "no stand-in from another bucket");
  assert.equal(cache.peek("a", "h2", 4), null, "a stale hash is no match");
  assert.equal(cache.queue.size, 0);
  const entry = cache.peek("a", "h1", 4);
  assert.ok(entry);
  assert.equal(entry, cache.get("a", "h1", 4));
});
