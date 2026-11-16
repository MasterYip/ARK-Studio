// Optional browser regression (Playwright):
//   node --test tests/browser/graph_zoom_consistency.test.cjs
// A card (Mind, node graph) must look like one picture uniformly scaled at
// every zoom: captures at several zooms, rescaled to the same size, may
// differ only by antialiasing.  A thumbnail that loads later only changes
// pixels inside its own slot, and never the card's size.  Sprites drawn at
// their native scale match direct drawing.  Force keeps its original
// screen-space labels: 11 px text at every zoom, hidden below zoom 0.3
// unless the node is focused.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

// Mean luminance difference (0-255) allowed between zooms at 3 world px per
// pixel: glyphs rasterised at 7 px and at 24 px differ slightly in weight.
const AA_TOLERANCE = Number(process.env.ARK_AA_TOLERANCE || 14);
const IMAGE = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="#e11d48"/><circle cx="40" cy="40" r="22" fill="#fde047"/></svg>');

test('cards and thumbnails scale as one picture; Force labels keep their screen size', { skip: !chromium, timeout: 180000 }, async () => {
  const web = path.resolve(__dirname, '../../web');
  let releaseImage;
  const imageGate = new Promise((resolve) => { releaseImage = resolve; });
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.end('<!doctype html><link rel="stylesheet" href="/css/app.css"><div id="g" style="width:900px;height:600px"></div>');
      return;
    }
    if (pathname === '/thumb.svg') {
      await imageGate;
      res.setHeader('Content-Type', 'image/svg+xml');
      res.end(IMAGE);
      return;
    }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/plain');
      res.end(await readFile(file));
    } catch { res.writeHead(404).end(); }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const page = await browser.newPage({ viewport: { width: 1000, height: 700 }, deviceScaleFactor: 1 });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(base + '/');
    await page.evaluate(async (base) => {
      const { GraphView } = await import('/js/graph.js');
      await document.fonts.ready;
      const node = {
        id: 1, uid: 'C-0042', type: 'claim', status: 'review',
        title: 'Behaviour-level control reduces compounding error at long horizons',
        tags: ['marker:star-yellow', 'marker:flag-red'],
        media: [{ id: 7, url: `${base}/thumb.svg`, is_image: true }],
      };
      const graphs = {};
      for (const layout of ['mind', 'node', 'force']) {
        const host = document.getElementById('g');
        host.innerHTML = '';
        const div = document.createElement('div');
        div.style.cssText = 'width:900px;height:600px';
        host.appendChild(div);
        const graph = new GraphView(div, { persistLayout: false, persistMode: false, layout });
        graph._resizeObserver.disconnect();
        graph.setData([{ ...node }], [], { layout, warm: false });
        graph.running = false;
        cancelAnimationFrame(graph.frame);
        graph.showLabels = true;
        graphs[layout] = graph;
        div.id = `host-${layout}`;
        div.style.display = 'none';
        host.removeChild(div);
        document.body.appendChild(div);
      }
      window.__graphs = graphs;
    }, base);

    // Render one zoom and return the item's capture (luminance) and box.
    const capture = (layout, zoom, { direct = false } = {}) => page.evaluate(({ layout, zoom, direct }) => {
      for (const [name, graph] of Object.entries(window.__graphs)) graph.host.style.display = name === layout ? 'block' : 'none';
      const g = window.__graphs[layout];
      const n = g.nodes[0];
      n.x = 0; n.y = 0;
      g._resize?.();
      g.selectedNodes = new Set(); g.selected = null; g.hover = null;
      // Whole-pixel alignment of the card's corner at zoom 1, so the sprite
      // (a bitmap) and direct drawing can be compared pixel for pixel.
      g.zoom = zoom;
      g.pan = zoom === 1 ? { x: (n.w / 2) % 1, y: (n.h / 2) % 1 } : { x: 0, y: 0 };
      g._spritesEnabled = !direct;
      g._draw();
      for (let i = 0; i < 50 && g._spriteCache?.queue.size; i++) g._spriteCache.pump(1000);
      g._draw();
      // World box around the card (plus its ports).
      const box = { x: -(n.w / 2 + 6) * zoom, y: -(n.h / 2 + 6) * zoom, w: (n.w + 12) * zoom, h: (n.h + 12) * zoom };
      const t = g._transform();
      // Exact (fractional) crop, composited on white, resampled to 3 world
      // px per pixel: layout shifts of a few world px stay visible while
      // glyph antialiasing at different raster sizes averages out.
      const worldW = box.w / zoom;
      const worldH = box.h / zoom;
      const tw = Math.round(worldW / 3), th = Math.round(worldH / 3);
      const dst = new OffscreenCanvas(tw, th);
      const c = dst.getContext('2d');
      c.fillStyle = '#ffffff';
      c.fillRect(0, 0, tw, th);
      c.imageSmoothingQuality = 'high';
      c.drawImage(g.canvas, t.x + box.x, t.y + box.y, box.w, box.h, 0, 0, tw, th);
      const data = c.getImageData(0, 0, tw, th).data;
      const lum = [];
      for (let i = 0; i < data.length; i += 4) lum.push(Math.round(0.299 * data[i] + 0.587 * data[i + 1] + 0.114 * data[i + 2]));
      // Full-resolution pixels for exact comparisons at one zoom.
      const sx = Math.round(t.x + box.x), sy = Math.round(t.y + box.y);
      const sw = Math.round(box.w), sh = Math.round(box.h);
      const image = g.ctx.getImageData(sx, sy, sw, sh);
      const raw = [];
      for (let i = 0; i < image.data.length; i += 4) raw.push(image.data[i], image.data[i + 1], image.data[i + 2]);
      return { lum, tw, th, w: n.w, h: n.h, raw, sw, sh, thumbs: g._cardGeometry ? g._cardGeometry(n).thumbs.map((t) => t.box) : [] };
    }, { layout, zoom, direct });

    // Mean luminance difference over pixels with ink in either picture, so a
    // small node and its label are not diluted by the empty space around them.
    // Both pictures are box-blurred (3x3) first: per-pixel glyph
    // antialiasing averages out, displacement of words or markers does not.
    const blur = (shot) => {
      if (shot.blurred) return shot.blurred;
      const { lum, tw } = shot, th = lum.length / tw, out = new Array(lum.length);
      for (let y = 0; y < th; y++) for (let x = 0; x < tw; x++) {
        let sum = 0, n = 0;
        for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) {
          const yy = y + dy, xx = x + dx;
          if (yy < 0 || xx < 0 || yy >= th || xx >= tw) continue;
          sum += lum[yy * tw + xx]; n += 1;
        }
        out[y * tw + x] = sum / n;
      }
      shot.blurred = out;
      return out;
    };
    const meanDiff = (a, b) => {
      assert.equal(a.lum.length, b.lum.length);
      const pa = blur(a), pb = blur(b);
      let sum = 0, count = 0;
      for (let i = 0; i < pa.length; i++) {
        if (pa[i] > 248 && pb[i] > 248) continue;
        sum += Math.abs(pa[i] - pb[i]);
        count += 1;
      }
      return count ? sum / count : 0;
    };
    const report = {};
    for (const layout of ['mind', 'node']) {
      // Reference: the largest zoom, downscaled.
      const zooms = [0.6, 1, 2];
      const shots = {};
      for (const zoom of zooms) shots[zoom] = await capture(layout, zoom);
      for (const zoom of zooms) {
        assert.equal(shots[zoom].w, shots[2].w, `${layout}: card width does not depend on zoom`);
        assert.equal(shots[zoom].h, shots[2].h, `${layout}: card height does not depend on zoom`);
      }
      report[layout] = { 'z0.6-vs-z2': +meanDiff(shots[0.6], shots[2]).toFixed(2), 'z1-vs-z2': +meanDiff(shots[1], shots[2]).toFixed(2) };
      // Uniform scaling: only antialiasing differences.
      // Control: the same picture shifted by 6 world px (two comparison
      // pixels; a re-wrap moves words much further).  Uniform scaling must
      // stay well below it.
      const shifted = { tw: shots[2].tw, lum: shots[2].lum.map((_, i) => (i % shots[2].tw > 1 ? shots[2].lum[i - 2] : 255)) };
      const control = meanDiff(shifted, shots[0.6]);
      report[layout].shift_control = +control.toFixed(2);
      if (process.env.ARK_CALIBRATE) continue;
      for (const zoom of [0.6, 1]) {
        const diff = meanDiff(shots[zoom], shots[2]);
        assert.ok(diff < AA_TOLERANCE && diff < 0.75 * control, `${layout} at ${zoom}: ${diff.toFixed(2)} (control ${control.toFixed(2)})`);
      }
      // Sprites at their native scale equal direct drawing (zoom 1, dpr 1).
      const direct = await capture(layout, 1, { direct: true });
      let differing = 0;
      for (let i = 0; i < direct.raw.length; i++) if (Math.abs(direct.raw[i] - shots[1].raw[i]) > 2) differing += 1;
      report[layout].sprite_vs_direct_differing_channels = differing;
      assert.ok(differing / direct.raw.length < 0.002, `${layout}: sprite vs direct ${differing}`);
    }

    // Force (the original rules): the label is screen-space 11 px text below
    // the node at every zoom; below zoom 0.3 it is hidden unless the node is
    // hovered, selected or focused.
    const forceLabel = (zoom, hover = false) => page.evaluate(({ zoom, hover }) => {
      for (const [name, graph] of Object.entries(window.__graphs)) graph.host.style.display = name === 'force' ? 'block' : 'none';
      const g = window.__graphs.force;
      const n = g.nodes[0];
      n.x = 0; n.y = 0;
      g._resize?.();
      g.selectedNodes = new Set(); g.selected = null; g.hover = hover ? n : null;
      g.zoom = zoom;
      g.pan = { x: 0, y: 0 };
      g._draw();
      const t = g._transform();
      const r = g._nodeRadius(n, zoom);
      // Rows with ink in a 240 px wide band below the node.
      const top = Math.round(t.y + r + 3), height = 40, left = Math.round(t.x - 120);
      const data = g.ctx.getImageData(left, top, 240, height).data;
      const rows = [];
      for (let y = 0; y < height; y++) {
        let ink = 0;
        for (let x = 0; x < 240; x++) if (data[(y * 240 + x) * 4 + 3] > 60) ink += 1;
        if (ink) rows.push(y);
      }
      return rows.length ? { first: rows[0], last: rows[rows.length - 1], rows: rows.length } : null;
    }, { zoom, hover });
    const label1 = await forceLabel(1), label2 = await forceLabel(2), label04 = await forceLabel(0.4);
    assert.ok(label1 && label2 && label04, 'Force labels show at zoom 0.4, 1 and 2');
    assert.equal(label2.last - label2.first, label1.last - label1.first, 'Force label text keeps its screen size');
    assert.equal(label04.last - label04.first, label1.last - label1.first);
    assert.equal(await forceLabel(0.25), null, 'Force labels hide below zoom 0.3');
    assert.ok(await forceLabel(0.25, true), 'a hovered node keeps its label below zoom 0.3');
    report.force_label_rows = { z1: label1, z2: label2 };

    // Thumbnails: before and after loading, same card size; pixels change
    // only inside the thumbnail slot.
    const beforeLoad = {};
    for (const layout of ['mind', 'node']) beforeLoad[layout] = await capture(layout, 1);
    releaseImage();
    await page.waitForTimeout(800);
    for (const layout of ['mind', 'node']) {
      const before = beforeLoad[layout];
      const after = await capture(layout, 1);
      assert.equal(after.w, before.w);
      assert.equal(after.h, before.h);
      const slot = before.thumbs[0];
      assert.ok(slot, `${layout}: the thumbnail has a slot before it loads`);
      let outside = 0, inside = 0;
      for (let y = 0; y < before.sh; y++) {
        for (let x = 0; x < before.sw; x++) {
          const i = (y * before.sw + x) * 3;
          const changed = Math.abs(before.raw[i] - after.raw[i]) + Math.abs(before.raw[i + 1] - after.raw[i + 1]) > 6;
          if (!changed) continue;
          // Capture origin is the card's top-left minus 6 world px (zoom 1).
          const wx = x - 6, wy = y - 6;
          const inSlot = wx >= slot.x - 1 && wx <= slot.x + slot.w + 1 && wy >= slot.y - 1 && wy <= slot.y + slot.h + 1;
          if (inSlot) inside += 1; else outside += 1;
        }
      }
      report[`${layout}_thumbnail`] = { inside, outside };
      assert.ok(inside > 100, `${layout}: the loaded thumbnail is drawn in its slot`);
      assert.equal(outside, 0, `${layout}: nothing outside the slot moves when the thumbnail loads`);
      // After loading, the card still scales as one picture.
      const z2 = await capture(layout, 2);
      const z06 = await capture(layout, 0.6);
      assert.ok(meanDiff(z06, z2) < AA_TOLERANCE, `${layout} with media: ${meanDiff(z06, z2)}`);
    }
    console.log(JSON.stringify(report));
    assert.deepEqual(errors, []);
  } finally {
    releaseImage?.();
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
