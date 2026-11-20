// Optional browser regression (Playwright):
//   node --test tests/browser/image_copy_menu.test.cjs
// Right-click on an image preview (attachment thumbnail, lightbox) opens the
// image menu: Copy image, Copy image link, Open in new tab.  Copy image
// fetches the full-size image with the page's cookies, converts it to PNG and
// puts image/png on the clipboard; Ctrl+C on the focused thumbnail does the
// same.  Videos get no image menu.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFile } = require('node:fs/promises');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="40"><rect width="64" height="40" fill="#2a7"/></svg>';

test('image previews offer copy image / copy link / open in new tab', { skip: !chromium, timeout: 120000 }, async () => {
  const web = path.resolve(__dirname, '../../ark_studio/web');
  const mediaRequests = [];
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (url.pathname === '/') {
      res.setHeader('Content-Type', 'text/html');
      res.end('<!doctype html><html><head><link rel="stylesheet" href="/css/app.css"></head><body><div id="overlay-root"></div><div id="toast-root"></div></body></html>');
      return;
    }
    if (url.pathname.startsWith('/api/media/')) {
      // Raw media needs the session cookie, like the ARK server.
      mediaRequests.push({ path: url.pathname, cookie: req.headers.cookie || '' });
      if (!/ark_session=ok/.test(req.headers.cookie || '')) { res.writeHead(401).end(); return; }
      res.setHeader('Content-Type', 'image/svg+xml');
      res.end(SVG);
      return;
    }
    const file = path.resolve(web, '.' + url.pathname);
    if (!file.startsWith(web + path.sep)) { res.writeHead(403).end(); return; }
    try {
      const body = await readFile(file);
      res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/plain');
      res.end(body);
    } catch { res.writeHead(404).end(); }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const context = await browser.newContext({ viewport: { width: 1000, height: 700 }, locale: 'en-US' });
    await context.addCookies([{ name: 'ark_session', value: 'ok', url: origin }]);
    let clipboardGranted = true;
    try { await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin }); } catch { clipboardGranted = false; }
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/?lang=en`);
    await page.evaluate(async () => {
      const { h } = await import('/js/dom.js');
      const { attachImageContextMenu } = await import('/js/image-copy.js');
      const thumb = h('button', { id: 'thumb', type: 'button', style: 'width:120px;height:80px;margin:40px' },
        h('img', { src: '/api/media/7/raw', alt: 'Chart', style: 'width:100%;height:100%' }));
      attachImageContextMenu(thumb, () => ({ src: '/api/media/7/raw', kind: 'image', caption: 'Chart' }));
      const video = h('button', { id: 'video', type: 'button', style: 'width:120px;height:80px;margin:40px' }, 'video');
      attachImageContextMenu(video, () => ({ src: '/api/media/8/raw', kind: 'video' }));
      document.body.append(thumb, video);
      window.__opened = [];
      window.open = (...args) => { window.__opened.push(args); return null; };
    });

    // Right-click: the three items.
    await page.click('#thumb', { button: 'right' });
    const menu = page.locator('.graph-menu.image-copy-menu');
    await menu.waitFor();
    assert.deepEqual((await menu.locator('.graph-menu-label').allTextContents()).map((s) => s.trim()),
      ['Copy image', 'Copy image link', 'Open in new tab']);
    assert.match(await menu.locator('kbd').first().textContent(), /C$/);

    // Open in new tab: absolute URL.
    await menu.locator('button', { hasText: 'Open in new tab' }).click();
    const opened = await page.evaluate(() => window.__opened);
    assert.equal(opened[0][0], `${origin}/api/media/7/raw`);
    assert.equal(opened[0][1], '_blank');
    assert.equal(await page.locator('.image-copy-menu').count(), 0, 'the menu closes after an action');

    // A video gets the browser's own menu, not the image menu.
    await page.click('#video', { button: 'right' });
    assert.equal(await page.locator('.image-copy-menu').count(), 0, 'no image menu on videos');

    if (clipboardGranted) {
      // Copy image link: absolute URL as text.
      await page.click('#thumb', { button: 'right' });
      await page.locator('.image-copy-menu button', { hasText: 'Copy image link' }).click();
      await page.waitForFunction(async () => (await navigator.clipboard.readText()).includes('/api/media/7/raw'));
      assert.equal(await page.evaluate(() => navigator.clipboard.readText()), `${origin}/api/media/7/raw`);

      // Copy image: SVG fetched with the cookie, converted, written as image/png.
      const readPng = () => page.evaluate(async () => {
        const items = await navigator.clipboard.read().catch(() => []);
        const item = items.find((entry) => entry.types.includes('image/png'));
        if (!item) return null;
        const blob = await item.getType('image/png');
        const bitmap = await createImageBitmap(blob);
        return { type: blob.type, width: bitmap.width, height: bitmap.height };
      });
      await page.evaluate(() => navigator.clipboard.writeText('reset'));
      await page.click('#thumb', { button: 'right' });
      await page.locator('.image-copy-menu button', { hasText: 'Copy image' }).first().click();
      await page.waitForSelector('#toast-root .toast.ok');
      assert.deepEqual(await readPng(), { type: 'image/png', width: 64, height: 40 });
      assert.ok(mediaRequests.some((r) => r.cookie.includes('ark_session=ok')), 'the raw image is fetched with the session cookie');

      // Ctrl+C on the focused thumbnail copies the image too.
      await page.evaluate(() => {
        document.getElementById('toast-root').replaceChildren();
        return navigator.clipboard.writeText('reset');
      });
      await page.focus('#thumb');
      await page.keyboard.press('Control+C');
      await page.waitForSelector('#toast-root .toast.ok');
      assert.deepEqual(await readPng(), { type: 'image/png', width: 64, height: 40 });
    } else {
      test.diagnostic('clipboard permissions unavailable in this browser: copy checks skipped');
    }

    // The lightbox image has the same menu.
    await page.evaluate(async () => {
      const { openImageLightbox } = await import('/js/image-viewer.js');
      openImageLightbox({ src: '/api/media/7/raw', alt: 'Chart', title: 'Chart' });
    });
    await page.waitForSelector('.iv-overlay .iv.iv-ready');
    const box = await page.locator('.iv-overlay .iv').boundingBox();
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.locator('.image-copy-menu').waitFor();
    assert.equal(await page.locator('.image-copy-menu .graph-menu-label').count(), 3);
    // Esc closes the menu first, then the lightbox.
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('.image-copy-menu').count(), 0);
    assert.equal(await page.locator('.iv-overlay').count(), 1, 'Esc on the menu keeps the lightbox open');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('.iv-overlay').count(), 0);

    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
});
