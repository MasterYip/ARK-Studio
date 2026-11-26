// Optional browser regression (Playwright):
//   node --test tests/browser/graph_force_settle.test.cjs
// The Force simulation stops once the nodes have settled (motion under the
// FORCE_SETTLE thresholds for 20 ticks), and switching Mind -> Force or
// Node -> Force starts with the centroid on the gravity centre without
// moving the picture; Mind and node layouts are centred there too.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('Force settles before stopping; layout switches share one centre', { skip: !chromium, timeout: 240000 }, async () => {
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
    const page = await browser.newPage({ viewport: { width: 1100, height: 760 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const result = await page.evaluate(async () => {
      localStorage.clear();
      const { GraphView } = await import('/js/graph.js');
      const { FORCE_SETTLE } = await import('/js/graph-core.js');
      const nodes = Array.from({ length: 120 }, (_, i) => ({ id: i + 1, uid: `T-${i + 1}`, type: ['task', 'idea', 'claim'][i % 3], status: 'open', title: `Node ${i + 1}` }));
      const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + Math.floor(i / 3), relation: 'part_of' }));
      const g = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout: 'force' });
      const wait = async () => { const t0 = performance.now(); while (g.running && performance.now() - t0 < 40000) await new Promise((r) => setTimeout(r, 25)); };
      g.setData(nodes, edges, { layout: 'force' });
      await wait();
      const fresh = { ...g._lastSettle };
      const centroid = () => {
        const cx = g.nodes.reduce((a, n) => a + n.x, 0) / g.nodes.length, cy = g.nodes.reduce((a, n) => a + n.y, 0) / g.nodes.length;
        return Math.hypot(cx, cy);
      };
      const box = () => {
        const xs = g.nodes.flatMap((n) => [n.x - (n.w || 0) / 2, n.x + (n.w || 0) / 2]), ys = g.nodes.flatMap((n) => [n.y - (n.h || 0) / 2, n.y + (n.h || 0) / 2]);
        return Math.hypot((Math.min(...xs) + Math.max(...xs)) / 2, (Math.min(...ys) + Math.max(...ys)) / 2);
      };
      const switches = {};
      for (const from of ['mind', 'node']) {
        g.setLayout(from);
        await new Promise((r) => setTimeout(r, 700));
        const layoutCentre = box();
        // Re-centring itself never moves the picture: a node keeps its screen
        // position (the camera moves along); the fit animation follows.
        const probe = g.nodes[5];
        const before = g._screen(probe);
        const copy = g.nodes.map((n) => ({ n, x: n.x, y: n.y }));
        const pan = { ...g.pan };
        g._centreOnGravity();
        const after = g._screen(probe);
        const jump = Math.hypot(after.x - before.x, after.y - before.y);
        copy.forEach(({ n, x, y }) => { n.x = x; n.y = y; });
        g.pan = pan;
        g.setLayout('force');
        switches[from] = { layoutCentre, atSwitch: centroid(), jump };
        await wait();
        switches[from].settle = { ...g._lastSettle };
      }
      return { fresh, switches, rule: FORCE_SETTLE };
    });
    const { rule } = result;
    for (const settle of [result.fresh, result.switches.mind.settle, result.switches.node.settle]) {
      assert.equal(settle.verdict, 'settled', 'the simulation stopped because the nodes settled');
      assert.ok(settle.quiet >= rule.ticks, `quiet for ${settle.quiet} ticks`);
      assert.ok(settle.mean * settle.zoom < rule.mean && settle.max * settle.zoom < rule.max, `final motion ${settle.mean}/${settle.max}`);
      assert.ok(settle.ticks < rule.tickCap, 'well before the safety cap');
    }
    for (const [from, s] of Object.entries(result.switches)) {
      assert.ok(s.layoutCentre < 1, `${from} layout centred on the gravity centre (${s.layoutCentre})`);
      assert.ok(s.atSwitch < 1e-6, `${from} -> Force starts with the centroid on the gravity centre (${s.atSwitch})`);
      assert.ok(s.jump < 1e-6, `${from} -> Force: the picture does not jump (${s.jump})`);
    }
    assert.deepEqual(errors, []);
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
