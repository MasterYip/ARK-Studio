// Optional browser regression (Playwright):
//   node --test tests/browser/graph_force_labels.test.cjs
// Force labels after visiting the node graph and Mind on the same view (their
// text state must not leak): every line is centred under its node at zoom
// 0.3-3, dpr 1 and 2, with and without scene tiles, with topics (member
// counts), markers, CJK and wrapped titles; a title wraps the same in every
// state; label boxes never overlap; priority is hovered > selected >
// highlighted neighbours > others, larger nodes first.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('Force labels are centred, wrap the same in every state and never overlap', { skip: !chromium, timeout: 240000 }, async () => {
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
    for (const dpr of [1, 2]) {
      for (const tiles of [false, 1]) {
        const page = await browser.newPage({ viewport: { width: 1100, height: 760 }, deviceScaleFactor: dpr });
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        const result = await page.evaluate(async ({ tiles }) => {
          localStorage.clear();
          const { GraphView } = await import('/js/graph.js');
          await document.fonts.ready;
          const titles = ['Behaviour cloning baseline', '基于行为的控制减少长时间范围内的累积误差实验结果分析',
            'A rather long title that certainly wraps onto a second line of the label', 'Short', '数据集', 'Ablation: horizon length vs compounding error'];
          const nodes = Array.from({ length: 60 }, (_, i) => ({
            id: i + 1, uid: `T-${i + 1}`, type: i % 9 === 0 ? 'topic' : ['task', 'idea', 'claim'][i % 3], status: ['open', 'done', 'blocked'][i % 3],
            title: `${titles[i % titles.length]} ${i + 1}`, tags: i % 2 ? ['marker:star-yellow'] : [],
            member_count: i % 9 === 0 ? 4 : undefined, degree: i % 7,
          }));
          const edges = nodes.slice(1).map((n, i) => ({ id: `e${i}`, source: n.id, target: 1 + (i % 6), relation: i % 2 ? 'supports' : 'part_of' }));
          const g = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, layout: 'node', sceneTiles: tiles });
          g._resizeObserver?.disconnect();
          g.setData(nodes, edges, { layout: 'node', warm: false });
          // Visit the node graph (port labels: right-aligned text) and Mind first.
          g.zoom = 1.5; g._draw();
          g.setLayout('mind'); g.zoom = 1.5; g._draw();
          g.setLayout('force');
          g.running = false; cancelAnimationFrame(g.frame); g._cancelCameraAnimation?.();
          await new Promise((r) => setTimeout(r, 300));
          g.running = false; cancelAnimationFrame(g.frame);
          // A loose grid, plus a dense cluster around node 1 for collisions.
          g.nodes.forEach((n, i) => { n.x = (i % 10) * 150 - 700; n.y = Math.floor(i / 10) * 110 - 300; n.vx = n.vy = 0; n._appear = 1; });
          const hub = g.byId.get(1);
          for (const id of [2, 3, 4, 5, 6]) { const n = g.byId.get(id); n.x = hub.x + (id - 4) * 18; n.y = hub.y + 40; }
          g.showLabels = true;
          const ctx = () => g.ctx;
          let calls = null;
          const record = (c) => {
            if (c.__recording) return;
            c.__recording = true;
            const real = c.fillText.bind(c);
            c.fillText = (text, x, y, ...rest) => {
              if (calls && /^11px /.test(c.font)) calls.push({ text, x, y, align: c.textAlign, base: c.textBaseline, w: c.measureText(text).width });
              return real(text, x, y, ...rest);
            };
          };
          const frame = () => {
            record(ctx());
            for (let i = 0; i < 60; i++) { g._draw(); if (g._sceneTilesDone !== false) break; }
            calls = [];
            g._draw();
            const out = calls; calls = null;
            return out;
          };
          const failures = [];
          const wraps = new Map();
          let checked = 0;
          for (const zoom of [0.3, 0.6, 1, 2, 3]) {
            for (const state of ['plain', 'neighbour', 'hover', 'select']) {
              g.hover = state === 'hover' || state === 'neighbour' ? hub : null;
              g.selected = state === 'select' ? hub.id : null;
              g.selectedNodes = new Set(state === 'select' ? [hub.id] : []);
              g.zoom = zoom;
              g.pan = { x: Math.round(-hub.x * zoom * 2) / 2, y: Math.round(-hub.y * zoom * 2) / 2 };
              const drawn = frame();
              const screens = g.nodes.map((n) => ({ n, s: g._screen(n) }));
              // Group lines by node: the node whose label layout holds the line
              // and sits right above it.
              const boxes = [];
              for (const call of drawn) {
                checked += 1;
                if (call.align !== 'center') failures.push(`z${zoom} ${state}: align ${call.align}`);
                if (call.base !== 'top') failures.push(`z${zoom} ${state}: baseline ${call.base}`);
                const owner = screens.find(({ n, s }) => n._forceLabelLayout?.lines.includes(call.text) && Math.abs(s.x - call.x) < 0.01 && call.y > s.y && call.y - s.y < 80);
                if (!owner) { failures.push(`z${zoom} ${state}: "${call.text}" not centred under a node`); continue; }
                boxes.push({ id: owner.n.id, x0: call.x - call.w / 2, x1: call.x + call.w / 2, y0: call.y, y1: call.y + 14 });
                const key = owner.n.id;
                const lines = owner.n._forceLabelLayout.lines.join('\n');
                if (wraps.has(key) && wraps.get(key) !== lines) failures.push(`node ${key} wraps differently in ${state}`);
                wraps.set(key, lines);
              }
              for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
                const a = boxes[i], b = boxes[j];
                if (a.id === b.id) continue;
                if (a.x0 < b.x1 && b.x0 < a.x1 && a.y0 < b.y1 && b.y0 < a.y1) failures.push(`z${zoom} ${state}: labels of ${a.id} and ${b.id} overlap`);
              }
              if (state === 'hover' && zoom === 1) {
                // Priority: the hovered hub's label is drawn; in the cluster the
                // largest neighbour wins over smaller ones it would overlap.
                const ids = new Set(boxes.map((b) => b.id));
                if (!ids.has(1)) failures.push('the hovered label is missing');
                window.__cluster = [2, 3, 4, 5, 6].filter((id) => ids.has(id));
              }
            }
          }
          // Expected winner in the cluster: highlighted neighbours of the hub
          // first, then the larger node (radius, then degree).
          const neighbours = new Set(g.edges.filter((e) => e.source === 1 || e.target === 1).map((e) => (e.source === 1 ? e.target : e.source)));
          const size = (n) => n.radius || 7 + Math.sqrt(n.degree || 0) * 2.1;
          const expected = [2, 3, 4, 5, 6].map((id) => g.byId.get(id))
            .sort((a, b) => Number(neighbours.has(b.id)) - Number(neighbours.has(a.id)) || size(b) - size(a) || (b.degree || 0) - (a.degree || 0))[0].id;
          return { failures: failures.slice(0, 12), count: failures.length, checked, cluster: window.__cluster, expected };
        }, { tiles });
        assert.ok(result.checked > 200, `labels were drawn (${result.checked})`);
        assert.deepEqual(result.failures, [], `dpr ${dpr} tiles ${tiles}: ${result.count} failures`);
        // The cluster labels overlap each other: the highest-priority one is
        // drawn first and therefore kept.
        assert.ok(result.cluster.includes(result.expected), `the top-priority cluster node ${result.expected} keeps its label (${result.cluster})`);
        assert.deepEqual(errors, []);
        await page.close();
      }
    }
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
