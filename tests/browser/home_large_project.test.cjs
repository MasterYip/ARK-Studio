// Optional browser regression (Playwright):
//   node --test tests/browser/home_large_project.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser)
// Home of a large project (perf/home-large-projects) against a stub API:
// 10,000 nodes with the default Home filter (the overview sends the newest
// 500), 700 items waiting in Needs you with 20 image/video previews each.
// After the first render the main thread never blocks for more than 200 ms,
// Needs you builds 4 rows (20 more per scroll step after "Show all"), no
// <video> element exists before a preview is played, and live-sync
// re-renders reuse the rows instead of rebuilding them.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const NODES = 10000, LIMIT = 500, NEEDS = 700, MEDIA = 20;

function fixture() {
  const types = ['task', 'claim', 'evidence', 'experiment', 'artifact', 'idea'];
  const statuses = ['open', 'active', 'review', 'done', 'blocked'];
  // The server's answer for the default filter: the newest LIMIT nodes.
  const nodes = Array.from({ length: LIMIT }, (_, i) => ({ id: NODES - i, type: types[i % 6], status: statuses[i % 5], degree: i % 7 }));
  const edges = [];
  for (let i = 1; i < LIMIT; i++) edges.push({ source: NODES - i, target: NODES - Math.floor(i / 2), relation: 'part_of' });
  const needsYou = Array.from({ length: NEEDS }, (_, i) => ({
    id: i + 1, uid: `T-${i + 1}`, type: 'task', title: `Result ${i + 1}: ablation of the gait controller`, status: 'review',
    kind: 'review', updated_at: new Date(Date.now() - i * 60000).toISOString(), excerpt: 'Ablation **result**.',
    excerpt_plain: 'Ablation result with a table and two figures.', comments: 1, comments_open: 0, own_media: MEDIA,
    assignee_effective: 'alice',
    media: Array.from({ length: MEDIA }, (_, j) => (j % 2
      ? { id: i * 100 + j, kind: 'video', url: `assets/files/v${j}.mp4`, raw_url: `/api/media/${i * 100 + j}/raw`, preview_kind: 'video',
        thumb_url: null, poster_url: `/api/media/${i * 100 + j}/thumb`, caption: `clip ${j}` }
      : { id: i * 100 + j, kind: 'image', url: `assets/media/f${j}.png`, raw_url: `/api/media/${i * 100 + j}/raw`, preview_kind: 'image',
        thumb_url: `/api/media/${i * 100 + j}/thumb`, caption: `figure ${j}` })),
  }));
  const overview = {
    today: new Date().toISOString().slice(0, 10), me: 'alice',
    graph: { nodes, edges, total: NODES, matched: NODES, limit: LIMIT, filter: { limit: LIMIT, types: [], statuses: [], days: 0 } },
    kpis: {}, days: [], stages: [], members: [], unassigned: {}, due: {}, blocked: {}, research: {}, activity: [], agents: { recent: [] },
    runs: { total: 0, recent: [] }, gpu: {},
  };
  const stats = { total: NODES, links: NODES * 2, by_type: { task: 5000, claim: 5000 }, by_status: { review: NEEDS } };
  return { overview, needsYou, stats };
}

const PAGE = `<!doctype html><html><head><link rel="stylesheet" href="/css/app.css"></head>
<body><div id="app" style="max-width:1300px;margin:0 auto"><div class="content"><div id="host"></div></div></div>
<div id="overlay-root"></div><div id="toast-root" class="toast-wrap"></div>
<script type="module">
  window.__long = [];
  new PerformanceObserver((list) => { for (const e of list.getEntries()) window.__long.push({ start: e.startTime, dur: e.duration }); })
    .observe({ type: 'longtask', buffered: true });
  const { renderOverview } = await import('/js/views/overview.js');
  const data = await (await fetch('/fixture.json')).json();
  const noop = () => {};
  const ctx = {
    S: { project: { id: 'prj_big', name: 'Big project', settings: {}, updated: new Date().toISOString() }, session: { user: 'alice' },
      stats: data.stats, overview: data.overview, needsYou: data.needsYou, activity: [] },
    navigate: noop, navigateWorkspace: noop, openUnit: noop, openCreate: noop, openImport: noop, toast: noop, refresh: noop, openTopic: noop,
  };
  window.__render = () => { const t = performance.now(); renderOverview(document.getElementById('host'), ctx); return performance.now() - t; };
  window.__first = window.__render();
  window.__ready = performance.now();
</script></body></html>`;

test('Home of a 10k-node project: no long task, lazy needs rows, no video elements', { skip: !chromium, timeout: 180000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const body = JSON.stringify(fixture());
  const thumbs = { n: 0 };
  // 1x1 PNG
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end(PAGE); return; }
    if (url.pathname === '/fixture.json') { res.setHeader('Content-Type', 'application/json'); res.end(body); return; }
    if (/^\/api\/media\/\d+\/thumb$/.test(url.pathname)) { thumbs.n += 1; res.setHeader('Content-Type', 'image/png'); res.end(png); return; }
    if (url.pathname.startsWith('/api/media/')) { res.writeHead(404).end(); return; }
    if (url.pathname.startsWith('/api/')) { res.setHeader('Content-Type', 'application/json'); res.end('{"ok": true}'); return; }
    const file = path.resolve(web, '.' + url.pathname);
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
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.waitForFunction(() => window.__ready, null, { timeout: 60000 });
    await page.waitForTimeout(3000);
    const first = await page.evaluate(() => ({
      renderMs: window.__first,
      rows: document.querySelectorAll('.needs-row').length,
      videos: document.querySelectorAll('video').length,
      line: document.querySelector('[data-home-filter]')?.dataset.homeFilter,
      shown: document.querySelector('[data-home-filter]')?.dataset.shown,
      long: window.__long.filter((l) => l.start >= window.__ready).map((l) => Math.round(l.dur)),
      dom: document.getElementsByTagName('*').length,
    }));
    assert.deepEqual(errors, []);
    assert.equal(first.rows, 4, 'Needs you builds the first four rows');
    assert.equal(first.videos, 0, 'previews are poster images');
    assert.equal(first.line, 'limited');
    assert.equal(first.shown, String(LIMIT));
    assert.ok(Math.max(0, ...first.long) <= 200, `long tasks after the first render: ${first.long}`);
    assert.ok(first.renderMs < 500, `first render ${first.renderMs} ms`);
    assert.ok(first.dom < 4000, `DOM nodes ${first.dom}`);
    assert.ok(thumbs.n <= 4 * MEDIA, `thumbnails requested: ${thumbs.n}`);

    // Show all: 20 more rows, more as the list is scrolled; still no long task.
    const mark = await page.evaluate(() => performance.now());
    await page.click('.needs-toggle');
    await page.waitForTimeout(500);
    const rowsAfter = await page.evaluate(() => document.querySelectorAll('.needs-row').length);
    assert.ok(rowsAfter > 4 && rowsAfter <= 4 + 40, `rows after Show all: ${rowsAfter}`);
    for (let i = 0; i < 5; i++) { await page.mouse.wheel(0, 2500); await page.waitForTimeout(300); }
    const scrolled = await page.evaluate((mark) => ({
      rows: document.querySelectorAll('.needs-row').length,
      long: window.__long.filter((l) => l.start >= mark).map((l) => Math.round(l.dur)),
    }), mark);
    assert.ok(scrolled.rows > rowsAfter && scrolled.rows < NEEDS, `rows after scrolling: ${scrolled.rows}`);
    assert.ok(Math.max(0, ...scrolled.long) <= 200, `long tasks while scrolling: ${scrolled.long}`);

    // A live-sync re-render reuses the rows (no rebuild, no reload).
    const again = await page.evaluate(() => {
      const before = document.querySelector('.needs-row');
      const ms = window.__render();
      return { ms, same: document.querySelector('.needs-row') === before, rows: document.querySelectorAll('.needs-row').length };
    });
    assert.ok(again.same, 'the row element is reused');
    assert.ok(again.ms < 300, `re-render ${again.ms} ms`);
  } finally {
    await browser?.close();
    server.close();
  }
});
