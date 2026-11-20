const assert = require('node:assert/strict');
const { readFile } = require('node:fs/promises');
const { createServer } = require('node:http');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

// The desktop app's window controls (desktop/src-tauri/src/shell.js) sit at the
// top right of the borderless window; the top bar and the sign-in/startup
// theme toggle must keep an 8 px gap to them on local and remote pages, at
// 100/125/150 % scaling and narrow widths. Remote pages get no extra button:
// local and remote projects are reached through the project switcher.
const root = path.resolve(__dirname, '../..');
const web = path.join(root, 'ark_studio/web');
const shellSource = path.join(root, 'ark_studio/desktop/src-tauri/src/shell.js');

const topbarPage = `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="/css/app.css"><link rel="stylesheet" href="/css/mobile.css">
  <header id="topbar" class="topbar"><div class="brand">ARK</div><nav class="tabs"></nav>
    <div class="top-actions"><button class="icon-btn top-ctl theme-top-btn">T</button>
    <button class="icon-btn account-btn top-ctl">A</button></div></header>
  <script>document.documentElement.setAttribute('data-desktop-shell', 'custom');</script>`;
const signInPage = `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" href="/css/startup.css">
  <div class="entry-themes"><button type="button">T</button></div>`;

function stub(role) {
  return `window.__TAURI_INTERNALS__ = { invoke: (cmd) => Promise.resolve(
    cmd === 'desktop_context' ? { role: ${JSON.stringify(role)} } : cmd === 'plugin:window|is_maximized' ? false : null) };`;
}

test('page buttons keep clear of the desktop window controls', { skip: !chromium, timeout: 60000 }, async () => {
  const cfg = { version: 'test', platform: 'windows', chrome: 'custom', label: 'main', controls: ['minimize', 'maximize', 'close'], insetLeft: 0 };
  const shell = (await readFile(shellSource, 'utf8')).replace('__ARK_SHELL_CONFIG__', JSON.stringify(cfg));
  const server = createServer(async (request, response) => {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    if (pathname === '/topbar' || pathname === '/signin') {
      response.setHeader('Content-Type', 'text/html');
      response.end(pathname === '/topbar' ? topbarPage : signInPage);
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
  const base = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.ARK_TEST_BROWSER });
    for (const scale of [1, 1.25, 1.5]) {
      for (const width of [1280, 700, 420]) {
        for (const role of ['local', 'remote']) {
          for (const page of ['/topbar', '/signin']) {
            const context = await browser.newContext({ viewport: { width, height: 400 }, deviceScaleFactor: scale });
            await context.addInitScript(stub(role) + '\n' + shell);
            const tab = await context.newPage();
            await tab.goto(base + page);
            await tab.waitForFunction(() => document.querySelectorAll('#ark-desktop-controls button').length === 3);
            await tab.waitForTimeout(100);
            assert.equal(await tab.locator('#ark-desktop-controls button').count(), 3, `${page} ${role}: only the window buttons`);
            const gap = await tab.evaluate(() => {
              const controls = document.getElementById('ark-desktop-controls').getBoundingClientRect();
              const right = Math.max(...[...document.querySelectorAll('.top-actions button, .entry-themes button')]
                .map(el => el.getBoundingClientRect().right));
              return controls.left - right;
            });
            assert.ok(gap >= 7.5, `${page} ${role} ${width}px @${scale}x: gap ${gap}px`);
            await context.close();
          }
        }
      }
    }
  } finally {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
  }
});
