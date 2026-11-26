// Optional browser regression (Playwright, WebGL; SwiftShader is enough):
//   node --test tests/browser/graph_3d.test.cjs
// The graph's 3D view (graph3d.js) follows its GraphView: data and relation
// filters, selection both ways (graph -> 3D states, a click in 3D -> graph
// selection and onSelect), "Locate", hover neighbourhood, node drag as in
// 2D (no pin), mouse bindings (right drag rotates, Ctrl+right drag pans,
// right click opens the node menu), read-only graphs do not drag nodes,
// the ? help, the 2D/3D choice is remembered per page, and
// without WebGL the graph falls back to 2D with a notice.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('3D view syncs with the 2D graph, drags, menus, remembers and falls back', { skip: !chromium, timeout: 240000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      res.setHeader('Content-Type', 'text/html');
      res.end('<!doctype html><meta charset="utf-8"><link rel="stylesheet" href="/css/app.css"><div id="toasts"></div><div id="g" class="graph-host" style="position:relative;width:1000px;height:680px"></div><div id="h" class="graph-host" style="position:relative;width:600px;height:400px"></div>');
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
    const page = await browser.newPage({ viewport: { width: 1100, height: 1200 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    const result = await page.evaluate(async () => {
      localStorage.clear();
      const { GraphView } = await import('/js/graph.js');
      const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      const settle = async (v) => { const t0 = performance.now(); while (v.running && performance.now() - t0 < 30000) await frame(); await frame(); };
      const nodes = Array.from({ length: 90 }, (_, i) => ({ id: i + 1, uid: `T-${i + 1}`, type: ['task', 'idea', 'claim'][i % 3], status: 'open', title: `Node ${i + 1}` }));
      const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + Math.floor(i / 3), relation: i % 4 ? 'part_of' : 'supports' }));
      const selected = [];
      const g = new GraphView(document.getElementById('g'), {
        persistLayout: false, layout: 'force', cameraKey: 'p:map', onSelect: (node, dbl) => selected.push([node?.id ?? null, dbl]),
      });
      g.setData(nodes, edges, { layout: 'force' });
      await g.set3D(true);
      const v = g._view3d;
      await settle(v);
      const out = {};
      out.ready = v.ready;
      out.canvasHidden = getComputedStyle(g.canvas).display === 'none';
      out.twoDRunning = g.running;
      out.remembered = localStorage.getItem(Object.keys(localStorage).find((k) => k.startsWith('ark.graph3d.view.v1')));
      // Graph selection -> 3D state.
      g.setSelected(5);
      await frame();
      const i5 = v.sim.index.get(5);
      out.selState = v.states[i5];
      out.neighbour = v.states[v.sim.index.get(2)];
      // Click a node in 3D -> graph selection + onSelect (Read mode).
      const target = v.sim.index.get(10);
      const rect = v.canvas.getBoundingClientRect();
      const at = { x: rect.left + v.proj.x[target], y: rect.top + v.proj.y[target] };
      out.clickAt = at;
      out.pickedHere = v._pick({ clientX: at.x, clientY: at.y, pointerType: 'mouse' });
      // Relation filter -> links.
      g.visibleRelations = new Set(['supports']);
      g._draw();
      out.supportsLinks = v.sim.links.length / 2;
      g.visibleRelations = new Set(['part_of', 'supports']);
      g._draw();
      out.allLinks = v.sim.links.length / 2;
      // Data filter -> subset keeps positions.
      const before = [v.sim.x[v.sim.index.get(30)], v.sim.y[v.sim.index.get(30)]];
      g.setData(nodes.filter((n) => n.id % 2 === 0), edges, { preserveCamera: true });
      out.subsetN = v.sim.n;
      out.kept = Math.hypot(v.sim.x[v.sim.index.get(30)] - before[0], v.sim.y[v.sim.index.get(30)] - before[1]) < 1e-9;
      g.setData(nodes, edges, { preserveCamera: true });
      await settle(v);
      // Locate flies and selects.
      out.located = g.locate('T-40');
      out.flight = Boolean(v.flight);
      out.locSel = g.selected;
      await new Promise((r) => setTimeout(r, 900));
      await settle(v);
      window.__g = g;
      window.__selected = selected;
      // A second graph on the same page key opens in 3D by itself.
      const g2 = new GraphView(document.getElementById('h'), { persistLayout: false, layout: 'force', cameraKey: 'p:map', readOnly: true });
      g2.setData(nodes.slice(0, 20), edges.slice(0, 19), { layout: 'force' });
      const t0 = performance.now();
      while (!g2._view3d?.ready && performance.now() - t0 < 10000) await new Promise((r) => setTimeout(r, 50));
      out.secondIn3d = Boolean(g2._view3d?.ready);
      window.__g2 = g2;
      return out;
    });
    assert.deepEqual(errors, []);
    assert.equal(result.ready, true);
    assert.equal(result.canvasHidden, true, '2D canvas hidden under the 3D layer');
    assert.equal(result.twoDRunning, false, '2D simulation paused');
    assert.equal(result.remembered, '3d');
    assert.equal(result.selState, 4, 'selected in the graph -> selected in 3D');
    assert.equal(result.neighbour, 2, 'a neighbour of the selection is highlighted');
    assert.equal(result.supportsLinks, 23);
    assert.equal(result.allLinks, 89);
    assert.equal(result.subsetN, 45);
    assert.equal(result.kept, true, 'a filter keeps the 3D positions of the remaining nodes');
    assert.equal(result.located, true);
    assert.equal(result.flight, true);
    assert.equal(result.locSel, 40);
    assert.equal(result.secondIn3d, true, 'the 3D choice is remembered for the page');

    // Hover, click and double-click with the real mouse.
    const p = await page.evaluate(async () => {
      window.__g.fit({ animate: false });
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
      const v = window.__g._view3d, i = v.sim.index.get(1), r = v.canvas.getBoundingClientRect();
      return { x: r.left + v.proj.x[i], y: r.top + v.proj.y[i] };
    });
    await page.mouse.move(p.x, p.y);
    await page.waitForTimeout(150);
    const hover = await page.evaluate(() => {
      const v = window.__g._view3d;
      return { hover: v.sim.ids[v.hover], neighbour: v.states[v.sim.index.get(2)], label: v.labels.some((s) => s.visible) };
    });
    await page.mouse.click(p.x, p.y);
    await page.waitForTimeout(150);
    const clicked = await page.evaluate(() => ({ selected: window.__g.selected, calls: window.__selected.slice() }));
    assert.equal(hover.hover, 1);
    assert.equal(hover.label, true);
    assert.equal(clicked.selected, 1, 'a click in 3D selects in the graph');
    assert.deepEqual(clicked.calls.at(-1), [1, false], 'and opens it like a 2D click (Read mode)');
    await page.mouse.dblclick(p.x, p.y);
    await page.waitForTimeout(100);
    const dbl = await page.evaluate(() => ({ calls: window.__selected.slice(), flight: Boolean(window.__g._view3d.flight) }));
    assert.deepEqual(dbl.calls.at(-1), [1, true]);
    assert.equal(dbl.flight, true, 'double-click flies to the node');
    await page.waitForTimeout(900);

    // Drag a node (left): it follows the pointer while held, like the 2D
    // drag; on release it is not pinned and the layout reheats around it.
    const q = await page.evaluate(() => {
      const v = window.__g._view3d, i = v.sim.index.get(1), r = v.canvas.getBoundingClientRect();
      return { x: r.left + v.proj.x[i], y: r.top + v.proj.y[i], theta: v.view.theta };
    });
    await page.mouse.move(q.x, q.y);
    await page.mouse.down();
    await page.mouse.move(q.x + 40, q.y + 25, { steps: 6 });
    const held = await page.evaluate(() => {
      const v = window.__g._view3d, i = v.sim.index.get(1), r = v.canvas.getBoundingClientRect();
      return { dragging: Boolean(v.drag), x: r.left + v.proj.x[i], y: r.top + v.proj.y[i], running: v.running };
    });
    await page.mouse.up();
    await page.waitForTimeout(200);
    const released = await page.evaluate(() => {
      const v = window.__g._view3d;
      return { dragging: Boolean(v.drag), fixed: v.sim.fixed.reduce((a, b) => a + b, 0), theta: v.view.theta,
        pinButton: Boolean(document.querySelector('.graph3d-pins')) };
    });
    assert.equal(held.dragging, true);
    assert.equal(held.running, true, 'the layout reheats while a node is dragged');
    assert.ok(Math.hypot(held.x - (q.x + 40), held.y - (q.y + 25)) < 6, `node under the pointer: ${JSON.stringify(held)}`);
    assert.deepEqual({ ...released, theta: released.theta === q.theta }, { dragging: false, fixed: 0, theta: true, pinButton: false },
      'released, nothing pinned, a node drag does not rotate');

    // Left drag on empty space does nothing; right drag rotates; Ctrl+right drag pans.
    const cam = () => page.evaluate(() => { const v = window.__g._view3d; return { theta: v.view.theta, tx: v.view.target.x, ty: v.view.target.y, tz: v.view.target.z }; });
    const box = await page.locator('#g .graph3d-canvas').boundingBox();
    const empty = { x: box.x + 30, y: box.y + box.height - 30 };
    await page.evaluate(() => window.__g._view3d.setSettings({ autoRotate: false }));
    let c0 = await cam();
    await page.mouse.move(empty.x, empty.y); await page.mouse.down(); await page.mouse.move(empty.x + 80, empty.y - 10, { steps: 5 }); await page.mouse.up();
    let c1 = await cam();
    assert.deepEqual(c1, c0, 'left drag on empty space leaves the camera');
    await page.mouse.move(empty.x, empty.y); await page.mouse.down({ button: 'right' }); await page.mouse.move(empty.x + 80, empty.y, { steps: 5 }); await page.mouse.up({ button: 'right' });
    await page.waitForTimeout(100);
    c1 = await cam();
    assert.notEqual(c1.theta, c0.theta, 'right drag rotates');
    assert.equal(await page.locator('.graph-menu').count(), 0, 'a right drag opens no menu');
    // Let the orbit inertia of the right drag above run out first.
    await page.evaluate(() => { const v = window.__g._view3d; v.spin.theta = v.spin.phi = 0; });
    await page.waitForTimeout(100);
    c0 = await cam();
    await page.keyboard.down('Control');
    await page.mouse.move(empty.x, empty.y); await page.mouse.down({ button: 'right' }); await page.mouse.move(empty.x + 60, empty.y + 20, { steps: 5 }); await page.mouse.up({ button: 'right' });
    await page.keyboard.up('Control');
    await page.waitForTimeout(600);
    c1 = await cam();
    assert.ok(Math.abs(c1.theta - c0.theta) < 1e-9, 'Ctrl+right drag does not rotate');
    assert.ok(Math.hypot(c1.tx - c0.tx, c1.ty - c0.ty, c1.tz - c0.tz) > 1, 'Ctrl+right drag pans');
    assert.equal(await page.locator('.graph-menu').count(), 0);

    // Right click (no drag) on a node: the node menu, as in 2D; on empty space: nothing.
    await page.mouse.click(empty.x, empty.y, { button: 'right' });
    await page.waitForTimeout(150);
    assert.equal(await page.locator('.graph-menu').count(), 0, 'no menu on empty space');
    const r = await page.evaluate(() => {
      window.__g.fit({ animate: false });
      const v = window.__g._view3d; v._update();
      const i = v.sim.index.get(7), b = v.canvas.getBoundingClientRect();
      return { x: b.left + v.proj.x[i], y: b.top + v.proj.y[i] };
    });
    await page.mouse.click(r.x, r.y, { button: 'right' });
    await page.waitForTimeout(200);
    const menu = await page.evaluate(() => ({ items: document.querySelectorAll('.graph-menu .graph-menu-item').length, selected: window.__g.selected }));
    assert.ok(menu.items >= 2, `node menu opened: ${JSON.stringify(menu)}`);
    assert.equal(menu.selected, 7, 'the right-clicked node is selected');
    await page.keyboard.press('Escape');
    await page.mouse.click(empty.x, empty.y);

    // The ? help lists the 3D bindings in 3D and points to them in 2D.
    const help = await page.evaluate(() => {
      const g = window.__g;
      g._toggleHelp(true);
      const in3d = Boolean(g.helpEl.querySelector('[data-help-3d]'));
      g._toggleHelp(false);
      return in3d;
    });
    assert.equal(help, true);

    // A read-only graph: a left drag on a node does not move it; right drag rotates.
    const ro = await page.evaluate(() => {
      const v = window.__g2._view3d, i = 0, r = v.canvas.getBoundingClientRect();
      return { x: r.left + v.proj.x[i], y: r.top + v.proj.y[i], theta: v.view.theta, pos: [v.sim.x[0], v.sim.y[0], v.sim.z[0]] };
    });
    await page.mouse.move(ro.x, ro.y);
    await page.mouse.down();
    await page.mouse.move(ro.x + 60, ro.y, { steps: 5 });
    const roHeld = await page.evaluate(() => Boolean(window.__g2._view3d.drag));
    await page.mouse.up();
    await page.mouse.move(ro.x, ro.y);
    await page.mouse.down({ button: 'right' });
    await page.mouse.move(ro.x + 60, ro.y, { steps: 5 });
    await page.mouse.up({ button: 'right' });
    const roAfter = await page.evaluate(() => ({ theta: window.__g2._view3d.view.theta }));
    assert.equal(roHeld, false, 'read-only: no node drag');
    assert.notEqual(roAfter.theta, ro.theta);

    // Back to 2D: the canvas returns, the choice is stored.
    const back = await page.evaluate(async () => {
      await window.__g.set3D(false);
      return { layer: Boolean(window.__g.host.querySelector('.graph3d-layer')), canvas: getComputedStyle(window.__g.canvas).display,
        stored: localStorage.getItem(Object.keys(localStorage).find((k) => k.startsWith('ark.graph3d.view.v1'))) };
    });
    assert.deepEqual(back, { layer: false, canvas: 'block', stored: '2d' });

    // No WebGL: 2D with a notice.
    const fallback = await page.evaluate(async () => {
      const original = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (kind, ...rest) { return /webgl/.test(kind) ? null : original.call(this, kind, ...rest); };
      try {
        await window.__g.set3D(true);
        return { view: Boolean(window.__g._view3d), toast: document.body.textContent.includes('WebGL') };
      } finally { HTMLCanvasElement.prototype.getContext = original; }
    });
    assert.equal(fallback.view, false);
    assert.equal(errors.length, 0, errors.join('\n'));
  } finally {
    await browser?.close();
    server.close();
  }
});
