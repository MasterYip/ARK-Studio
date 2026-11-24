// Optional browser regression (Playwright):
//   node --test tests/browser/wheel_scroll.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a Chromium executable)
// The mouse wheel scrolls every main scroll area: the page itself (Home,
// Settings), the inspector drawer, a Board column, the session sidebar thread
// and the left search panel; only the graph canvas consumes the wheel (it
// zooms).  Runs in
// Chromium and, when installed, WebKit.
// WebKit (Safari, WebKitGTK in the Linux desktop app) stops wheel-scrolling
// the document when the root element sets overscroll-behavior (WebKit bug
// 237093).  boot.js marks those engines when a mouse is present and the root
// overscroll lock is lifted there; Chromium keeps it (fixed top bar during
// elastic overscroll).
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let playwright;
try { playwright = require('playwright'); } catch { /* Optional browser dependency. */ }

const filler = (n, cls = '') => Array.from({ length: n }, (_, i) => `<p class="${cls}" style="margin:0;padding:10px 0">Line ${i + 1}</p>`).join('');

const PAGE = `<!doctype html><html lang="en" data-theme="light"><head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="stylesheet" href="/css/app.css"><link rel="stylesheet" href="/css/mobile.css"></head><body>
<section id="ark-startup" hidden><div class="entry-progress"><h1></h1><span class="entry-stage"></span><span class="entry-slow-note"></span>
  <div class="entry-track" role="progressbar"><i></i></div><button class="entry-retry" type="button"></button></div></section>
<header id="topbar" class="topbar">ARK Studio</header>
<aside id="search" style="position:fixed;left:0;top:var(--topbar);bottom:0;width:220px;display:flex;flex-direction:column">
  <div class="wb-search-results">${filler(80)}</div></aside>
<main id="app" style="margin:0 380px 0 240px"><div class="page home-page" id="page">
  <div id="g" style="width:100%;height:260px;position:relative"></div>
  <div class="settings-content">${filler(150)}</div>
  <div class="board-col" style="max-height:300px;min-height:0"><div class="board-body" id="board">${filler(60, 'task-card')}</div></div>
  ${filler(60)}</div></main>
<aside class="drawer" id="drawer" style="position:fixed;right:0;top:var(--topbar);bottom:0;width:360px;display:flex;flex-direction:column">
  <div class="drawer-body" id="inspector">${filler(200)}</div>
  <div style="height:200px;display:flex;flex-direction:column"><div class="ssb-thread" id="thread">${filler(80)}</div></div></aside>
<script src="/js/boot.js"></script>
<script type="module">
  const { GraphView } = await import('/js/graph.js');
  const nodes = Array.from({ length: 12 }, (_, i) => ({ id: i + 1, uid: 'T-' + (i + 1), type: 'task', status: 'open', title: 'Node ' + (i + 1) }));
  const edges = nodes.slice(1).map((n, i) => ({ id: 'e' + i, source: n.id, target: 1, relation: 'part_of' }));
  window.graph = new GraphView(document.getElementById('g'), { persistLayout: false, persistMode: false, cameraKey: null, layout: 'force' });
  window.graph.setData(nodes, edges, { layout: 'force' });
  window.ready = true;
</script></body></html>`;

function serve() {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    if (pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end(PAGE); return; }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      res.setHeader('Content-Type', file.endsWith('.css') ? 'text/css' : file.endsWith('.js') ? 'text/javascript' : 'application/octet-stream');
      res.end(await readFile(file));
    } catch { res.writeHead(404).end(); }
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)));
}

async function launch(name) {
  const type = playwright?.[name];
  if (!type) return null;
  try {
    return await type.launch(name === 'chromium' && process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {});
  } catch { return null; } // engine not installed
}

for (const engine of ['chromium', 'webkit']) {
  test(`${engine}: the mouse wheel scrolls every main area; only the graph canvas zooms`, { skip: !playwright, timeout: 90000 }, async (t) => {
    const browser = await launch(engine);
    if (!browser) { t.skip(`${engine} is not installed`); return; }
    const server = await serve();
    try {
      const page = await browser.newPage({ viewport: { width: 1400, height: 800 } });
      const errors = [];
      // The stub page has no project: graph.js API calls may fail with "no project".
      page.on('pageerror', (error) => { if (!/no project/i.test(error.message)) errors.push(error.message); });
      await page.goto(`http://127.0.0.1:${server.address().port}/`);
      await page.waitForFunction(() => window.ready === true);

      const root = await page.evaluate(() => ({
        flag: document.documentElement.dataset.rootOverscroll || '',
        html: getComputedStyle(document.documentElement).getPropertyValue('overscroll-behavior-y'),
        body: getComputedStyle(document.body).getPropertyValue('overscroll-behavior-y'),
      }));
      // Playwright's WebKit build may not implement overscroll-behavior ('');
      // what matters is that the root lock does not apply there.
      if (engine === 'webkit') {
        assert.equal(root.flag, 'auto');
        assert.notEqual(root.html, 'none');
        assert.notEqual(root.body, 'none');
      } else assert.deepEqual(root, { flag: '', html: 'none', body: 'none' });

      const center = (selector) => page.evaluate((s) => {
        const r = document.querySelector(s).getBoundingClientRect();
        return { x: r.left + Math.min(r.width / 2, 150), y: r.top + Math.min(r.height / 2, 120) };
      }, selector);
      const wheelAt = async (selector, dx, dy) => {
        const { x, y } = await center(selector);
        await page.mouse.move(x, y);
        await page.mouse.wheel(dx, dy);
        await page.waitForTimeout(350);
      };
      const offsets = (selector) => page.evaluate((s) => {
        const el = s ? document.querySelector(s) : document.scrollingElement;
        return { top: el.scrollTop, left: el.scrollLeft };
      }, selector);

      // Page (Home/Settings content scrolls with the document).
      await wheelAt('.settings-content', 0, 300);
      assert.ok((await offsets(null)).top > 0, 'page scrolls under the wheel');
      await page.evaluate(() => { document.scrollingElement.scrollTop = 0; });

      for (const selector of ['#inspector', '#board', '#thread', '.wb-search-results']) {
        await page.evaluate((s) => document.querySelector(s).scrollIntoView({ block: 'nearest' }), selector);
        await wheelAt(selector, 0, 200);
        assert.ok((await offsets(selector)).top > 0, `${selector} scrolls under the wheel`);
      }

      // Graph canvas: the wheel zooms the graph and the page stays put.
      await page.evaluate(() => { document.scrollingElement.scrollTop = 0; });
      const zoom = await page.evaluate(() => window.graph.zoom);
      await wheelAt('#g canvas', 0, -300);
      await page.waitForTimeout(400);
      assert.notEqual(await page.evaluate(() => window.graph.zoom), zoom, 'graph zooms');
      assert.equal((await offsets(null)).top, 0, 'page does not scroll over the graph');
      assert.deepEqual(errors, []);
    } finally {
      await browser.close();
      server.close();
    }
  });
}
