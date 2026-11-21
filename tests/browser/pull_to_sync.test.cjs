const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const { createServer } = require('node:http');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

test('phone pull syncs on release only from the top and preserves scrolling', { skip: !chromium, timeout: 30000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const server = createServer(async (request, response) => {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    if (pathname === '/') {
      response.setHeader('Content-Type', 'text/html');
      response.end(`<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
        <link rel="stylesheet" href="/css/app.css"><link rel="stylesheet" href="/css/mobile.css">
        <header id="topbar" class="topbar">ARK Studio</header>
        <aside class="side-panel" style="position:fixed;top:var(--topbar);right:0;bottom:0;width:120px"></aside>
        <main id="app"><div style="height:300px">Project</div>
          <canvas style="display:block;width:100%;height:180px;touch-action:none"></canvas>
          <div style="height:1000px"></div></main>
        <script type="module">
          import { installPullToSync } from '/js/pull-to-sync.js';
          window.syncCalls = 0;
          installPullToSync(document.querySelector('#app'), () => {
            window.syncCalls++;
            return new Promise(resolve => { window.finishSync = resolve; });
          }, () => true);
        </script>`);
      return;
    }
    const file = path.resolve(web, '.' + pathname);
    if (!file.startsWith(web + path.sep)) return response.writeHead(404).end();
    try {
      response.setHeader('Content-Type', file.endsWith('.css') ? 'text/css' : 'text/javascript');
      response.end(await readFile(file));
    } catch { response.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.ARK_TEST_BROWSER });
    const page = await browser.newPage({ viewport: { width: 390, height: 760 }, isMobile: true, hasTouch: true });
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.waitForFunction(() => window.syncCalls === 0);
    const cdp = await page.context().newCDPSession(page);
    const touch = (type, x, y) => cdp.send('Input.dispatchTouchEvent', {
      type, touchPoints: x === undefined ? [] : [{ x, y, id: 1 }],
    });
    const drag = async (x, y, dx, dy) => {
      await touch('touchStart', x, y);
      for (const step of [0.15, 0.5, 1]) await touch('touchMove', x + dx * step, y + dy * step);
      await touch('touchEnd');
    };
    const calls = () => page.evaluate(() => window.syncCalls);
    const indicator = page.locator('.pull-to-sync');
    const topbarRect = () => page.evaluate(() => {
      const { x, y, width, height } = document.querySelector('#topbar').getBoundingClientRect();
      return { x, y, width, height };
    });
    const layout = () => page.evaluate(() => ({
      html: getComputedStyle(document.documentElement).overscrollBehaviorY,
      body: getComputedStyle(document.body).overscrollBehaviorY,
      panel: document.querySelector('.side-panel').getBoundingClientRect().top,
      topbarBottom: document.querySelector('#topbar').getBoundingClientRect().bottom,
    }));
    const restingTopbar = await topbarRect();
    assert.equal(restingTopbar.y, 0);
    assert.deepEqual(await layout(), { html: 'none', body: 'none', panel: restingTopbar.height, topbarBottom: restingTopbar.height });
    await drag(120, 120, 0, 40);
    await drag(120, 120, 100, 30);
    await drag(120, 360, 0, 100);
    await touch('touchStart', 120, 120);
    await touch('touchMove', 120, 220);
    await touch('touchCancel');
    assert.equal(await calls(), 0);
    assert.equal(await indicator.isVisible(), false);

    await touch('touchStart', 120, 120);
    for (const y of [130, 160, 225]) await touch('touchMove', 120, y);
    assert.equal(await calls(), 0);
    assert.equal(await indicator.isVisible(), true);
    assert.deepEqual(await topbarRect(), restingTopbar);
    assert.ok((await indicator.boundingBox()).y >= restingTopbar.height, 'indicator sits under the fixed top bar');
    await touch('touchEnd');
    assert.equal(await calls(), 1);
    assert.equal(await indicator.isVisible(), true);
    assert.deepEqual(await topbarRect(), restingTopbar);
    await drag(120, 120, 0, 100);
    assert.equal(await calls(), 1);
    await page.evaluate(() => window.finishSync());
    await indicator.waitFor({ state: 'hidden' });

    await page.evaluate(() => window.scrollTo(0, 220));
    await drag(120, 55, 0, 100);
    await page.waitForFunction(() => window.scrollY < 220, {}, { timeout: 2000 });
    assert.equal(await calls(), 1);
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.setViewportSize({ width: 1050, height: 760 });
    await drag(120, 120, 0, 100);
    assert.equal(await calls(), 1);

    // Desktop: wheel overscroll at the top must not drag the sticky top bar
    // away from the fixed side panels (root elastic overscroll is disabled).
    await page.close();
    const desktop = await browser.newPage({ viewport: { width: 1280, height: 800 } });
    await desktop.goto(`http://127.0.0.1:${server.address().port}/`);
    await desktop.waitForFunction(() => window.syncCalls === 0);
    const desktopRect = () => desktop.evaluate(() => {
      const { x, y, width, height } = document.querySelector('#topbar').getBoundingClientRect();
      return { x, y, width, height };
    });
    const desktopState = () => desktop.evaluate(() => ({
      html: getComputedStyle(document.documentElement).overscrollBehaviorY,
      body: getComputedStyle(document.body).overscrollBehaviorY,
      panel: document.querySelector('.side-panel').getBoundingClientRect().top,
      topbarBottom: document.querySelector('#topbar').getBoundingClientRect().bottom,
    }));
    const desktopTopbar = await desktopRect();
    const desktopLayout = await desktopState();
    assert.equal(desktopTopbar.y, 0);
    assert.deepEqual(desktopLayout, { html: 'none', body: 'none', panel: desktopTopbar.height, topbarBottom: desktopTopbar.height });
    await desktop.mouse.move(400, 300);
    for (let i = 0; i < 6; i++) {
      await desktop.mouse.wheel(0, -240);
      assert.deepEqual(await desktopRect(), desktopTopbar);
      assert.deepEqual(await desktopState(), desktopLayout);
    }
    await desktop.waitForTimeout(400);
    assert.deepEqual(await desktopRect(), desktopTopbar);
    assert.deepEqual(await desktopState(), desktopLayout);
    assert.equal(await desktop.evaluate(() => window.scrollY), 0);
    await desktop.mouse.wheel(0, 300);
    await desktop.waitForFunction(() => window.scrollY > 0);
    assert.deepEqual(await desktopRect(), desktopTopbar, 'top bar stays pinned while content scrolls');
    assert.equal(await desktop.evaluate(() => window.syncCalls), 0);
  } finally {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
  }
});
