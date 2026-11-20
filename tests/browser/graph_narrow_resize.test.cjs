// Optional browser regression (Playwright, real scrollbars):
//   node --test tests/browser/graph_narrow_resize.test.cjs
// The Local graph in a narrow, scrolling sidebar section: resizing through
// narrow widths keeps a single canvas, never shows scrollbars in its
// container (which used to make the width oscillate: scrollbar on -> narrower
// -> resize -> scrollbar off -> wider -> ...), resizes only on real size
// changes, leaves no stale frame behind, and a view rebuilt in the same host
// still has one canvas.  Force and Mind, dpr 1 and 2.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('a narrow Local graph never oscillates, scrolls, doubles or ghosts', { skip: !chromium, timeout: 240000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') {
      // A sidebar section like the workbench's: scrolls when its content is taller.
      res.end(`<!doctype html><link rel="stylesheet" href="/css/app.css">
        <div id="view" class="wb-view" style="overflow:auto;width:430px;height:500px">
          <div class="lg-panel"><div class="lg-center"><b>Centre node</b></div><div class="lg-stage"><div id="g" class="lg-graph"></div></div></div>
        </div>`);
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
    // Real (classic) scrollbars: Playwright hides them by default.
    browser = await chromium.launch({ headless: true, ignoreDefaultArgs: ['--hide-scrollbars'],
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const dpr of [1, 2]) {
      for (const layout of ['force', 'mind']) {
        const page = await browser.newPage({ viewport: { width: 900, height: 700 }, deviceScaleFactor: dpr });
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        const r = await page.evaluate(async (layout) => {
          localStorage.clear();
          const { GraphView } = await import('/js/graph.js');
          const { applyPresence } = await import('/js/presence.js');
          const host = document.getElementById('g');
          const view = document.getElementById('view');
          const make = () => new GraphView(host, {
            compact: true, localGraph: true, compactLabels: true, readOnly: true, persistLayout: false, persistMode: false, cameraKey: null, layout,
            sidebarToolbar: { layouts: ['force', 'mind', 'node'], extra: [], onLayout() {} },
          });
          let g = make();
          const nodes = Array.from({ length: 30 }, (_, i) => ({ id: i + 1, uid: `T-${i + 1}`, type: ['task', 'idea', 'claim'][i % 3], status: 'open', title: `Node ${i + 1}` }));
          const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + Math.floor(i / 3), relation: 'part_of' }));
          g.setData(nodes, edges, { layout, selected: 1 });
          const frame = () => new Promise((res) => requestAnimationFrame(() => requestAnimationFrame(res)));
          const sleep = (ms) => new Promise((res) => setTimeout(res, ms));
          const failures = [];
          let changes = 0;
          let last = '';
          const sizes = [[430, 500], [400, 480], [360, 460], [330, 420], [300, 380], [260, 320], [220, 300], [180, 260], [150, 220], [120, 200], [333.5, 401.25], [260, 500], [430, 500]];
          for (const [w, h] of sizes) {
            view.style.width = `${w}px`;
            view.style.height = `${h}px`;
            await frame();
            await sleep(120);
            const key = `${host.getBoundingClientRect().width}x${host.getBoundingClientRect().height}`;
            if (key !== last) changes += 1;
            last = key;
            const label = `${w}x${h}`;
            if (host.querySelectorAll('canvas').length !== 2) failures.push(`${label}: missing or duplicated canvas layers`);
            for (const el of [view, ...view.querySelectorAll('.lg-panel, .lg-stage, .lg-graph')]) {
              if (el.scrollWidth > el.clientWidth || el.scrollHeight > el.clientHeight) failures.push(`${label}: ${el.className} scrolls (${el.scrollWidth}x${el.scrollHeight} > ${el.clientWidth}x${el.clientHeight})`);
            }
            const c = g.canvas, rect = host.getBoundingClientRect();
            if (parseFloat(c.style.width) > rect.width + 1e-6 || parseFloat(c.style.height) > rect.height + 1e-6) failures.push(`${label}: canvas ${c.style.width}x${c.style.height} larger than its host ${rect.width}x${rect.height}`);
            // No resize while nothing changes (an oscillation keeps resizing).
            const idle = g.resizeCount;
            await sleep(300);
            if (g.resizeCount !== idle) failures.push(`${label}: ${g.resizeCount - idle} resizes while idle`);
            // No stale frame: what is on the canvas equals a fresh redraw.
            g.running = false; cancelAnimationFrame(g.frame); g._cancelCameraAnimation?.();
            await frame();
            g._draw();
            const shown = g.ctx.getImageData(0, 0, c.width, c.height).data;
            g.ctx.save(); g.ctx.setTransform(1, 0, 0, 1, 0, 0); g.ctx.clearRect(0, 0, c.width, c.height); g.ctx.restore();
            g._draw();
            const fresh = g.ctx.getImageData(0, 0, c.width, c.height).data;
            let diff = 0;
            for (let i = 0; i < shown.length; i++) if (shown[i] !== fresh[i]) diff += 1;
            if (diff) failures.push(`${label}: ${diff} stale channels`);
            if (c.width !== Math.floor(parseFloat(c.style.width) * devicePixelRatio + 1e-6)) failures.push(`${label}: backing store ${c.width} for ${c.style.width} at dpr ${devicePixelRatio}`);
          }
          const resizes = g.resizeCount;
          const originalCamera = { pan: { ...g.pan }, zoom: g.zoom };
          const originalLayout = g.nodes.map(({ id, x, y }) => [id, x, y]);
          const overlay = host.querySelector('.graph-presence-layer');
          const entry = (actor, label, unit) => ({ actor, label, unit, state: 'writing' });
          applyPresence({ entries: [entry('local', 'Local agent', 1), entry('remote', 'Outside agent', 999)] });
          const chip = host.querySelector('.local-graph-presence button.presence-chip');
          const label = chip.getAttribute('aria-label');
          const corner = [chip.offsetParent.getBoundingClientRect().left - host.getBoundingClientRect().left,
            chip.offsetParent.getBoundingClientRect().top - host.getBoundingClientRect().top];
          chip.click();
          const rows = [...host.querySelectorAll('.presence-row b')].map((element) => element.textContent);
          const menu = host.querySelector('.presence-list').getBoundingClientRect();
          const chipRect = chip.getBoundingClientRect();
          if (chip.classList.contains('hidden') || chip.title !== 'Local agent' || !label || rows.join() !== 'Local agent') failures.push('invalid local presence');
          if (corner.some((offset) => offset < 0 || offset > 12) || menu.top < chipRect.bottom) failures.push('presence is not in the upper left or menu overlaps chip');
          const clearCanvas = () => {
            g.ctx.save();
            g.ctx.setTransform(1, 0, 0, 1, 0, 0);
            g.ctx.clearRect(0, 0, g.canvas.width, g.canvas.height);
            g.ctx.restore();
          };
          clearCanvas();
          Object.defineProperty(document, 'hidden', { configurable: true, value: true });
          document.dispatchEvent(new Event('visibilitychange'));
          await frame();
          const painted = () => g.ctx.getImageData(0, 0, g.canvas.width, g.canvas.height).data.some((value) => value !== 0);
          if (painted()) failures.push('a hidden graph should not repaint');
          delete document.hidden;
          document.dispatchEvent(new Event('visibilitychange'));
          await frame();
          if (!painted()) failures.push('the same-size canvas did not repaint after idle');
          g.canvas.style.marginLeft = '7px';
          clearCanvas();
          g.canvas.dispatchEvent(new Event('contextrestored'));
          await frame();
          if (!painted() || overlay.getBoundingClientRect().left !== g.canvas.getBoundingClientRect().left) failures.push('canvas layers did not recover together');
          if (overlay.width !== g.canvas.width || overlay.height !== g.canvas.height) failures.push('presence backing store differs');
          if (resizes !== g.resizeCount || originalCamera.zoom !== g.zoom || JSON.stringify(originalCamera.pan) !== JSON.stringify(g.pan)) failures.push('canvas recovery moved the camera');
          if (JSON.stringify(originalLayout) !== JSON.stringify(g.nodes.map(({ id, x, y }) => [id, x, y]))) failures.push('canvas recovery moved nodes');
          // Rebuilt in the same host (the section re-rendered): one canvas, one toolbar.
          g.destroy();
          g = make();
          g.setData(nodes, edges, { layout, selected: 1 });
          await frame();
          return { failures, resizes, changes, canvases: host.querySelectorAll('canvas').length, toolbars: host.querySelectorAll('.graph-tools').length };
        }, layout);
        assert.deepEqual(r.failures, [], `dpr ${dpr} ${layout}: ${r.failures.join('; ')}`);
        assert.ok(r.resizes <= r.changes + 2, `dpr ${dpr} ${layout}: ${r.resizes} resizes for ${r.changes} size changes`);
        assert.equal(r.canvases, 2, 'a rebuilt view has one graph canvas and its overlay');
        assert.equal(r.toolbars, 1, 'and one toolbar');
        assert.deepEqual(errors, []);
        await page.close();
      }
    }
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
