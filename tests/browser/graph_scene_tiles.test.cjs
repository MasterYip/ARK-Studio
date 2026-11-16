// Optional browser regression (Playwright):
//   node --test tests/browser/graph_scene_tiles.test.cjs
// A settled Force or node graph composited from scene tiles must be the same picture
// as drawing the scene directly (labels included, across tile seams), tiles
// must be reused when only the camera pans, and any scene change must drop
// them.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('Force and node-graph scene tiles equal direct drawing and are reused on pan', { skip: !chromium, timeout: 180000 }, async () => {
  const web = path.resolve(__dirname, '../../web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.end('<!doctype html><link rel="stylesheet" href="/css/app.css"><div id="g" style="width:1100px;height:700px"></div>');
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
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [layout, dpr] of [['force', 1], ['force', 2], ['node', 1], ['node', 2]]) {
      const page = await browser.newPage({ viewport: { width: 1200, height: 800 }, deviceScaleFactor: dpr });
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${server.address().port}/`);
      const result = await page.evaluate(async (layout) => {
        const { GraphView } = await import('/js/graph.js');
        await document.fonts.ready;
        localStorage.clear();
        const graph = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout, sceneTiles: 1 });
        graph._resizeObserver.disconnect();
        const nodes = Array.from({ length: 400 }, (_, i) => ({
          id: i + 1, uid: `T-${i + 1}`, type: ['task', 'idea', 'claim'][i % 3], status: ['open', 'done', 'blocked'][i % 3],
          title: `Research item ${i + 1} with a label`, tags: i % 2 ? ['marker:star-yellow'] : [], degree: i % 5,
        }));
        const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + (i * 7) % n.id, relation: i % 2 ? 'supports' : 'part_of' }));
        graph.setData(nodes, edges, { layout, warm: false });
        graph.running = false;
        cancelAnimationFrame(graph.frame);
        graph._cancelCameraAnimation?.();
        await new Promise((r) => setTimeout(r, 500));
        if (layout === 'force') {
          const cols = 20;
          graph.nodes.forEach((n, i) => { n.x = (i % cols) * 90 - 900; n.y = Math.floor(i / cols) * 70 - 700; n.vx = n.vy = 0; });
        }
        // Look at the middle of the graph, camera on whole device pixels (as
        // the tile path rounds it) plus an odd offset.
        const mid = graph.nodes[200];
        const centre = (zoom) => ({ x: (Math.round(-mid.x * zoom * dpr) + 37) / dpr, y: (Math.round(-mid.y * zoom * dpr) - 21) / dpr });
        graph.showLabels = true;
        const dpr = Math.min(2.5, window.devicePixelRatio || 1);
        const capture = () => {
          const c = graph.canvas;
          // Premultiplied: the canvas is transparent, and the colour of an
          // almost transparent pixel is meaningless once unpremultiplied.
          const data = graph.ctx.getImageData(0, 0, c.width, c.height).data;
          const out = new Float32Array(data.length);
          for (let i = 0; i < data.length; i += 4) {
            const a = data[i + 3] / 255;
            out[i] = data[i] * a; out[i + 1] = data[i + 1] * a; out[i + 2] = data[i + 2] * a; out[i + 3] = data[i + 3];
          }
          return out;
        };
        const settle = () => {
          for (let i = 0; i < 400; i++) {
            graph._draw();
            graph._spriteCache?.pump(1000);
            if (!graph._spriteCache?.queue.size && graph._sceneTilesDone !== false) break;
          }
          graph._draw();
        };
        const out = [];
        for (const zoom of [0.25, 0.6, 1.3]) {
          graph.zoom = zoom;
          graph.pan = centre(zoom);
          graph.opts.sceneTiles = false;
          settle();
          const direct = capture();
          graph.opts.sceneTiles = 1;
          graph._sceneTiles = null; graph._sceneTilesPrev = null; graph._tileLastZoom = undefined;
          settle();
          const tiles = capture();
          // Same pixel, premultiplied, within 24 levels on every channel
          // (antialiased steps on different surfaces round a few levels apart).
          // Control: the same tiles moved by one device pixel must fail.
          const w = graph.canvas.width, h = graph.canvas.height;
          const same = (p, q) => Math.abs(direct[p] - tiles[q]) <= 24 && Math.abs(direct[p + 1] - tiles[q + 1]) <= 24
            && Math.abs(direct[p + 2] - tiles[q + 2]) <= 24 && Math.abs(direct[p + 3] - tiles[q + 3]) <= 24;
          let differing = 0;
          let shifted = 0;
          for (let y = 0; y < h; y++) for (let x = 1; x < w; x++) {
            const p = (y * w + x) * 4;
            if (!same(p, p)) differing += 1;
            if (!same(p, p - 4)) shifted += 1;
          }
          out.push({ zoom, differing, shifted, total: w * h, tiles: graph._sceneTiles?.tiles.size || 0 });
        }
        // Pan by whole tiles' worth of pixels: existing tiles are reused.
        graph.zoom = 0.6; graph.pan = centre(0.6);
        graph._sceneTiles = null; graph._sceneTilesPrev = null; graph._tileLastZoom = undefined;
        settle();
        const before = new Set(graph._sceneTiles.tiles.values());
        graph.pan.x += 100;
        graph._draw();
        const reused = [...graph._sceneTiles.tiles.values()].filter((t) => before.has(t)).length;
        // A scene change (hover) drops the tiles: none of the old ones survive.
        const panned = new Set(graph._sceneTiles.tiles.values());
        graph.hover = graph.nodes[5];
        graph._draw();
        const afterHover = [...(graph._sceneTiles?.tiles.values() || [])].filter((t) => panned.has(t)).length;
        graph.hover = null;
        // Dragging the background pans (tiles stay); dragging a node edits.
        graph.drag = { pan: true, moved: true };
        const panDrag = graph._sceneTilesUsable();
        graph.drag = { node: graph.nodes[0], moved: true };
        const nodeDrag = graph._sceneTilesUsable();
        graph.drag = null;
        return { out, reused, before: before.size, afterHover, panDrag, nodeDrag };
      }, layout);
      console.log(JSON.stringify(result.out));
      for (const row of result.out) {
        assert.ok(row.tiles > 0, `${layout} dpr ${dpr} zoom ${row.zoom}: tiles were used`);
        assert.ok(row.differing / row.total < 0.0001, `${layout} dpr ${dpr} zoom ${row.zoom}: ${row.differing} of ${row.total} pixels differ`);
        assert.ok(row.shifted > row.differing * 50 + 1000, `${layout} dpr ${dpr} zoom ${row.zoom}: a 1 px shift is detected (${row.shifted} vs ${row.differing})`);
      }
      assert.ok(result.reused >= result.before - 3, `pan reuses tiles (${result.reused}/${result.before})`);
      assert.equal(result.afterHover, 0, 'hover changes the scene: old tiles are dropped');
      assert.equal(result.panDrag, true, 'a pan drag keeps the tiles');
      assert.equal(result.nodeDrag, false, 'a node drag does not use tiles');
      assert.deepEqual(errors, []);
      console.log(JSON.stringify({ layout, dpr, ...result }));
      await page.close();
    }
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
