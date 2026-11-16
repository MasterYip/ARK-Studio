// Bitmap cache for graph cards and labels.
//
// Every card (Mind, node graph) and every Force node with its label is drawn
// by one function in world units: the same drawing at every zoom, only
// scaled.  A sprite is that drawing rasterised at a power-of-two resolution
// bucket (screen px per world unit, devicePixelRatio included) and drawn
// with drawImage at scale/bucket (between 0.5 and 1), so text and markers
// keep their exact layout while zooming and never reflow.
//
// Entries are keyed by an item id plus a content hash (title, status,
// markers, loaded thumbnails, palette, ...).  A new hash drops the item's
// old bitmaps.  Rasterising happens in `pump(budgetMs)` (idle time); until a
// bitmap exists the caller draws directly with the same function or reuses a
// bitmap from another bucket.  Memory is bounded by an LRU byte cap; a hit
// only bumps a counter, the ordering cost is paid when evicting.

export const MIN_BUCKET = 1 / 16;
export const MAX_BUCKET = 4;
// Bitmaps above this many pixels are not cached (very large cards when
// zoomed far in); the caller draws them directly.
export const MAX_SPRITE_PIXELS = 2048 * 1024;
export const DEFAULT_MAX_BYTES = 96 * 1024 * 1024;
// Small bitmaps (labels, nodes, zoomed-out cards) share atlas pages: drawing
// thousands of distinct small canvases costs a texture each per frame, one
// page costs one.  Slots are shelf-packed with a 2 px gutter; a page is
// evicted as a whole.
export const ATLAS_SIZE = 2048;
export const ATLAS_MAX_SLOT = [256, 128];
const GUTTER = 2;

/** The power-of-two bucket at or above `scale` (screen px per world unit). */
export function spriteBucket(scale) {
  const value = Math.max(MIN_BUCKET, Math.min(MAX_BUCKET, Number(scale) || 1));
  return 2 ** Math.ceil(Math.log2(value) - 1e-9);
}

function defaultSurface(w, h) {
  if (typeof OffscreenCanvas === "function") return new OffscreenCanvas(w, h);
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  return canvas;
}

export class SpriteCache {
  constructor({ maxBytes = DEFAULT_MAX_BYTES, createSurface = defaultSurface, now = () => performance.now() } = {}) {
    this.maxBytes = maxBytes;
    this.createSurface = createSurface;
    this.now = now;
    this.items = new Map();   // id -> { hash, buckets: Map(bucket -> entry), queued: Set(bucket) }
    this.lru = new Set();     // every entry; entry.used orders eviction
    this.queue = new Map();   // job key -> job
    this.pages = [];
    this.tick = 0;
    this.bytes = 0;
    this.stats = { hits: 0, misses: 0, rasterised: 0, evicted: 0 };
  }

  _item(id, hash) {
    let item = this.items.get(id);
    if (item && item.hash !== hash) {
      for (const entry of [...item.buckets.values()]) this._drop(entry);
      for (const bucket of item.queued) this.queue.delete(jobKey(id, bucket));
      this.items.delete(id);
      item = null;
    }
    if (!item) {
      item = { hash, buckets: new Map(), queued: new Set() };
      this.items.set(id, item);
    }
    return item;
  }

  _drop(entry) {
    const item = this.items.get(entry.id);
    if (item?.buckets.get(entry.bucket) === entry) item.buckets.delete(entry.bucket);
    if (item && !item.buckets.size && !item.queued.size) this.items.delete(entry.id);
    if (this.lru.delete(entry)) this.bytes -= entry.bytes;
    entry.page?.entries.delete(entry);
  }

  // A slot of width x height px on an atlas page (shelf packing).
  _slot(width, height) {
    const h = Math.ceil((height + GUTTER) / 8) * 8;
    const w = width + GUTTER;
    for (const page of this.pages) {
      for (const shelf of page.shelves) {
        if (shelf.h === h && shelf.x + w <= ATLAS_SIZE) {
          const slot = { page, x: shelf.x, y: shelf.y };
          shelf.x += w;
          return slot;
        }
      }
      if (page.top + h <= ATLAS_SIZE) {
        page.shelves.push({ y: page.top, h, x: w });
        const slot = { page, x: 0, y: page.top };
        page.top += h;
        return slot;
      }
    }
    const surface = this.createSurface(ATLAS_SIZE, ATLAS_SIZE);
    const page = { surface, ctx: surface.getContext("2d"), shelves: [{ y: 0, h, x: w }], top: h, entries: new Set(), used: 0 };
    this.pages.push(page);
    this.bytes += ATLAS_SIZE * ATLAS_SIZE * 4;
    return { page, x: 0, y: 0 };
  }

  /**
   * The best bitmap for `id`/`hash` at `bucket`: the exact bucket, else the
   * nearest other bucket (preferring a sharper one).  Queues the exact
   * bucket when it is missing.  Returns null when nothing is ready.
   */
  get(id, hash, bucket, job) {
    const item = this._item(id, hash);
    const exact = item.buckets.get(bucket);
    if (exact) {
      exact.used = ++this.tick;
      this.stats.hits += 1;
      return exact;
    }
    this.stats.misses += 1;
    if (job) this.request(id, hash, bucket, job);
    let best = null, bestScore = Infinity;
    for (const [b, entry] of item.buckets) {
      // Larger buckets downscale cleanly; smaller ones only as a stopgap.
      const score = b >= bucket ? b / bucket : 4 * bucket / b;
      if (score < bestScore) { best = entry; bestScore = score; }
    }
    if (best) best.used = ++this.tick;
    return best;
  }

  /** The bitmap for `id`/`hash` at exactly `bucket`, or null; queues nothing. */
  peek(id, hash, bucket) {
    const item = this.items.get(id);
    const entry = item && item.hash === hash ? item.buckets.get(bucket) : null;
    if (entry) entry.used = ++this.tick;
    return entry || null;
  }

  /** Queue a rasterisation.  `job = { w, h, paint(ctx) }` in world units. */
  request(id, hash, bucket, job) {
    const item = this._item(id, hash);
    if (item.queued.has(bucket)) return;
    item.queued.add(bucket);
    this.queue.set(jobKey(id, bucket), { id, hash, bucket, w: job.w, h: job.h, paint: job.paint });
  }

  /** Whether a bitmap of this size would be cached at all. */
  cacheable(w, h, bucket) {
    return w * h * bucket * bucket <= MAX_SPRITE_PIXELS;
  }

  /** Rasterise queued sprites for up to `budgetMs`.  Returns jobs left. */
  pump(budgetMs = 6) {
    const start = this.now();
    for (const [key, job] of this.queue) {
      this.queue.delete(key);
      const item = this.items.get(job.id);
      item?.queued.delete(job.bucket);
      if (!item || item.hash !== job.hash || item.buckets.has(job.bucket)) continue;
      this.rasterise(job);
      if (this.now() - start >= budgetMs) break;
    }
    return this.queue.size;
  }

  /** Rasterise one job now (also used for direct-vs-sprite comparisons). */
  rasterise({ id, hash, bucket, w, h, paint }) {
    const width = Math.max(1, Math.ceil(w * bucket));
    const height = Math.max(1, Math.ceil(h * bucket));
    let entry;
    if (width <= ATLAS_MAX_SLOT[0] && height <= ATLAS_MAX_SLOT[1]) {
      const { page, x, y } = this._slot(width, height);
      const ctx = page.ctx;
      ctx.save();
      ctx.clearRect(x, y, width, height);
      ctx.beginPath();
      ctx.rect(x, y, width, height);
      ctx.clip();
      ctx.translate(x, y);
      ctx.scale(bucket, bucket);
      paint(ctx);
      ctx.restore();
      entry = { id, surface: page.surface, sx: x, sy: y, page, w, h, bucket, bytes: 0, used: ++this.tick };
      page.entries.add(entry);
    } else {
      const surface = this.createSurface(width, height);
      const ctx = surface.getContext("2d");
      ctx.scale(bucket, bucket);
      paint(ctx);
      entry = { id, surface, sx: 0, sy: 0, w, h, bucket, bytes: width * height * 4, used: ++this.tick };
    }
    const item = this._item(id, hash);
    const old = item.buckets.get(bucket);
    if (old) this._drop(old);
    item.buckets.set(bucket, entry);
    this.lru.add(entry);
    this.bytes += entry.bytes;
    this.stats.rasterised += 1;
    this._evict();
    return entry;
  }

  _evict() {
    if (this.bytes <= this.maxBytes) return;
    // Least recently drawn first, down to 80% of the cap, so eviction (and
    // its sort) runs rarely.
    const target = this.maxBytes * 0.8;
    // Atlas pages and standalone bitmaps compete by their last use.
    for (const page of this.pages) {
      page.used = 0;
      for (const entry of page.entries) page.used = Math.max(page.used, entry.used);
    }
    const units = [...this.pages.map((page) => ({ page, used: page.used })),
      ...[...this.lru].filter((entry) => !entry.page).map((entry) => ({ entry, used: entry.used }))]
      .sort((a, b) => a.used - b.used);
    for (const unit of units) {
      if (this.bytes <= target) break;
      if (unit.entry) {
        this._drop(unit.entry);
        this.stats.evicted += 1;
        continue;
      }
      for (const entry of [...unit.page.entries]) { this._drop(entry); this.stats.evicted += 1; }
      this.pages.splice(this.pages.indexOf(unit.page), 1);
      this.bytes -= ATLAS_SIZE * ATLAS_SIZE * 4;
    }
  }

  clear() {
    this.items.clear();
    this.lru.clear();
    this.queue.clear();
    this.pages = [];
    this.bytes = 0;
  }
}

function jobKey(id, bucket) {
  return `${id}#${bucket}`;
}
