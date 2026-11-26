// Optional browser regression (Playwright):
//   node --test tests/browser/graph_remote_update.test.cjs
// A change from another device/user (setData remote mode) keeps the camera
// and every existing node where it is: changed nodes update in place,
// removed ones disappear, new ones start next to a neighbour (or in the
// middle of the view) and only they settle.  Force and the node graph.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('a remote delta keeps the camera and existing positions', { skip: !chromium, timeout: 180000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.end('<!doctype html><link rel="stylesheet" href="/css/app.css"><div id="g" class="graph-host" style="width:1000px;height:680px"></div>');
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
    for (const layout of ['force', 'node']) {
      const page = await browser.newPage({ viewport: { width: 1100, height: 760 } });
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${server.address().port}/`);
      const r = await page.evaluate(async (layout) => {
        localStorage.clear();
        const { GraphView } = await import('/js/graph.js');
        const nodes = Array.from({ length: 80 }, (_, i) => ({ id: i + 1, uid: `T-${i + 1}`, type: ['task', 'idea', 'claim'][i % 3], status: 'open', title: `Node ${i + 1}` }));
        const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + Math.floor(i / 3), relation: 'part_of' }));
        const g = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout });
        const settle = async () => { const t0 = performance.now(); while ((g.running || g._nodeAnimation) && performance.now() - t0 < 40000) await new Promise((res) => setTimeout(res, 25)); };
        g.setData(nodes, edges, { layout });
        await settle();
        // The user looks somewhere specific.
        g._cancelCameraAnimation?.();
        g.zoom = 1.7;
        g.pan = { x: 123, y: -45 };
        g._draw();
        const camera = { zoom: g.zoom, pan: { ...g.pan } };
        const before = new Map(g.nodes.map((n) => [n.id, [n.x, n.y]]));
        // The delta: node 7 renamed, node 80 removed, node 81 linked to 3,
        // node 82 unrelated.
        const next = nodes.filter((n) => n.id !== 80).map((n) => (n.id === 7 ? { ...n, title: 'Renamed remotely' } : { ...n }));
        next.push({ id: 81, uid: 'T-81', type: 'idea', status: 'open', title: 'New linked' }, { id: 82, uid: 'T-82', type: 'idea', status: 'open', title: 'New unrelated' });
        const nextEdges = edges.filter((e) => e.source !== 80 && e.target !== 80).map((e) => ({ ...e }));
        nextEdges.push({ id: 'new', source: 81, target: 3, relation: 'part_of' });
        g.setData(next, nextEdges, { layout, preserveCamera: true, animate: true, remote: true });
        const afterInsert = new Map(g.nodes.map((n) => [n.id, [n.x, n.y]]));
        await settle();
        const moved = [];
        for (const n of g.nodes) {
          if (n.id > 80) continue;
          const [x, y] = before.get(n.id);
          if (Math.hypot(n.x - x, n.y - y) > 1e-9) moved.push(n.id);
        }
        const centre = g._world(g.width / 2, g.height / 2);
        const n3 = g.byId.get(3);
        const linked = afterInsert.get(81), unrelated = afterInsert.get(82);
        // A second delta without new nodes: nothing moves at all.
        const quiet = new Map(g.nodes.map((n) => [n.id, [n.x, n.y]]));
        g.setData(g.nodes.map((n) => ({ ...n, title: n.id === 9 ? 'Again' : n.title })), nextEdges, { layout, preserveCamera: true, animate: true, remote: true });
        await settle();
        const movedQuiet = g.nodes.filter((n) => { const [x, y] = quiet.get(n.id); return Math.hypot(n.x - x, n.y - y) > 1e-9; }).map((n) => n.id);
        return {
          camera, cameraAfter: { zoom: g.zoom, pan: { ...g.pan } }, moved, movedQuiet,
          renamed: g.byId.get(7).title, removed: g.byId.has(80), count: g.nodes.length,
          linkedNear: Math.hypot(linked[0] - before.get(3)[0], linked[1] - before.get(3)[1]),
          unrelatedNear: Math.hypot(unrelated[0] - centre.x, unrelated[1] - centre.y), n3: [n3.x, n3.y],
        };
      }, layout);
      assert.deepEqual(r.cameraAfter, r.camera, `${layout}: the camera stays`);
      assert.deepEqual(r.moved, [], `${layout}: existing nodes stay where they were`);
      assert.deepEqual(r.movedQuiet, [], `${layout}: a delta without new nodes moves nothing`);
      assert.equal(r.renamed, 'Renamed remotely');
      assert.equal(r.removed, false);
      assert.equal(r.count, 81);
      if (layout === 'force') {
        assert.ok(r.linkedNear < 70, `a new linked node starts next to its neighbour (${r.linkedNear})`);
        assert.ok(r.unrelatedNear < 70, `an unrelated new node starts in the middle of the view (${r.unrelatedNear})`);
      }
      assert.deepEqual(errors, []);
      await page.close();
    }
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
