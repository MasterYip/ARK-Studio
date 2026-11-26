// Optional browser regression (Playwright, WebGL; SwiftShader is enough):
//   node --test tests/browser/graph_3d_filters.test.cjs
// Filters moved back and forth quickly (min links, type, status) change
// which nodes the 3D view shows and how many atlas cells it needs.  After
// every change the per-instance data must match the visible nodes (count,
// ids, atlas cells, sizes) and the nearest nodes must render as whole
// discs (no quarter or half discs from a stale atlas texture), and no two
// labels overlap on screen.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('3D instances and node discs stay whole while filters change', { skip: !chromium, timeout: 300000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.setHeader('Content-Type', 'text/html');
      res.end('<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="/css/app.css"><div id="g" class="graph-host" style="position:relative;width:1000px;height:700px"></div>');
      return;
    }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream');
      res.end(await readFile(file));
    } catch { res.writeHead(404).end(); }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true, args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'],
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const page = await browser.newPage({ viewport: { width: 1040, height: 760 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const result = await page.evaluate(async () => {
      localStorage.clear();
      const { GraphView } = await import('/js/graph.js');
      const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      const TYPES = ['evidence', 'related_work', 'experiment', 'decision', 'task', 'claim', 'idea', 'question', 'method', 'story', 'run', 'artifact', 'resource', 'milestone', 'source', 'note'];
      const STATUSES = ['open', 'active', 'done', 'blocked', 'review'];
      let s = 3; const rand = () => ((s = (Math.imul(s, 1664525) + 1013904223) >>> 0) / 4294967296);
      const all = Array.from({ length: 1200 }, (_, i) => ({ id: i + 1, uid: `N-${i + 1}`, type: TYPES[i % TYPES.length], status: STATUSES[Math.floor(i / TYPES.length) % STATUSES.length], title: `Node ${i + 1}` }));
      const edges = [];
      for (let i = 1; i < all.length; i++) edges.push({ id: `p${i}`, source: i + 1, target: 1 + Math.floor(rand() * i), relation: 'part_of' });
      for (let k = 0; k < 500; k++) { const a = 1 + Math.floor(rand() * 1200), b = 1 + Math.floor(rand() * 1200); if (a !== b) edges.push({ id: `x${k}`, source: a, target: b, relation: 'supports' }); }
      const degree = new Map();
      for (const e of edges) { degree.set(e.source, (degree.get(e.source) || 0) + 1); degree.set(e.target, (degree.get(e.target) || 0) + 1); }
      const g = new GraphView(document.getElementById('g'), { persistLayout: false, layout: 'force', cameraKey: 'filters' });
      g.setData(all, edges, { layout: 'force' });
      await g.set3D(true);
      const v = g._view3d;
      // Filters as the Map applies them: a new node list from the server.
      const filters = [
        (n) => (degree.get(n.id) || 0) >= 3,            // min links 3
        (n) => n.type === 'evidence',                      // one type: few atlas cells
        () => true,                                         // everything: many cells
        (n) => (degree.get(n.id) || 0) >= 6,
        (n) => n.status === 'done' && n.id <= 200,         // ~100 nodes
        (n) => (degree.get(n.id) || 0) >= 1,
        (n) => ['task', 'claim'].includes(n.type),
        (n) => (degree.get(n.id) || 0) >= 2,
      ];
      const report = [];
      const gl = v.renderer.getContext();
      const bg = v.scene.background;
      for (let round = 0; round < 2; round++) for (let f = 0; f < filters.length; f++) {
        const shown = all.filter(filters[f]);
        g.setData(shown, edges, { preserveCamera: true });
        await frame();
        if (f % 3 === 2) { g.fit({ animate: false }); await frame(); }
        const snap = v.instanceSnapshot();
        const ids = shown.map((n) => n.id);
        const problems = [];
        if (snap.count !== shown.length) problems.push(`count ${snap.count} != ${shown.length}`);
        if (JSON.stringify(snap.ids) !== JSON.stringify(ids)) problems.push('ids differ');
        if (snap.cells.some((c, i) => c !== snap.expectedCells[i])) problems.push('atlas cells differ');
        if (snap.sizes.some((x, i) => Math.abs(x - snap.expectedSizes[i]) > 1e-3)) problems.push('sizes differ');
        if (snap.textureSize !== snap.atlasSize) problems.push(`texture ${snap.textureSize} != atlas ${snap.atlasSize}`);
        // Pixels: the nearest on-screen nodes are whole discs.
        v._update(); v.renderer.render(v.scene, v.camera);
        const p = v.proj, W = gl.drawingBufferWidth, H = gl.drawingBufferHeight, ratio = W / v.width;
        const order = [...Array(v.sim.n).keys()].filter((i) => p.w[i] > 0 && p.r[i] >= 5 && p.x[i] > 20 && p.x[i] < v.width - 20 && p.y[i] > 60 && p.y[i] < v.height - 20)
          .sort((a, b) => p.w[a] - p.w[b]).slice(0, 12);
        const px = new Uint8Array(4);
        let clipped = 0;
        for (const i of order) {
          let holes = 0;
          for (let k = 0; k < 8; k++) {
            const ang = k * Math.PI / 4;
            const x = Math.round((p.x[i] + Math.cos(ang) * p.r[i] * 0.8) * ratio), y = Math.round((p.y[i] + Math.sin(ang) * p.r[i] * 0.8) * ratio);
            gl.readPixels(x, H - 1 - y, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px);
            const d = Math.abs(px[0] - bg.r * 255) + Math.abs(px[1] - bg.g * 255) + Math.abs(px[2] - bg.b * 255);
            if (d < 12) holes++;
          }
          if (holes) clipped++;
        }
        if (clipped) problems.push(`${clipped}/${order.length} near discs clipped`);
        // Labels: no two shown labels overlap on screen (none is hovered or selected here).
        const boxes = v.labelStats?.boxes || [];
        let overlaps = 0;
        for (let a = 0; a < boxes.length; a++) for (let b = a + 1; b < boxes.length; b++) {
          const p1 = boxes[a], p2 = boxes[b];
          if (p1.x0 < p2.x1 && p2.x0 < p1.x1 && p1.y0 < p2.y1 && p2.y0 < p1.y1) overlaps++;
        }
        if (overlaps) problems.push(`${overlaps} overlapping labels`);
        report.push({ round, f, n: shown.length, cells: snap.cellCount, checked: order.length, labels: boxes.length, problems });
      }
      return report;
    });
    assert.deepEqual(errors, []);
    const bad = result.filter((r) => r.problems.length);
    assert.deepEqual(bad, [], JSON.stringify(bad, null, 1));
    assert.ok(result.some((r) => r.labels >= 5), 'labels shown: ' + JSON.stringify(result.map((r) => r.labels)));
    assert.ok(result.every((r) => r.checked >= 1) && result.reduce((s, r) => s + r.checked, 0) >= 100, 'enough discs checked: ' + JSON.stringify(result.map((r) => r.checked)));
  } finally {
    await browser?.close();
    server.close();
  }
});
