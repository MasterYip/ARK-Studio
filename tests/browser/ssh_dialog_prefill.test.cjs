// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/ssh_dialog_prefill.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>, ARK_TEST_SHOTS=<folder> for the screenshots)
// The device SSH dialog opens prefilled from the project's machine records:
// the ARK server runs on a GPU server of its own list (like gpu-lab), its
// public URL names that server's address, so the ARK server device is
// matched to the GPU server and the dialog shows its host, port, user and
// the ark-<name> alias suggestion.  English and Chinese.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, writeFileSync, mkdirSync, readdirSync, statSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch {
  try { ({ chromium } = require('playwright-core')); } catch { /* Optional browser dependency. */ }
}

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const WEB = path.resolve(__dirname, '../../ark_studio/web');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function api(base, method, route, body, project = '', extra = {}) {
  const headers = { Origin: base, ...extra };
  if (body) headers['Content-Type'] = 'application/json';
  if (project) headers['X-ARK-Project'] = project;
  const res = await fetch(base + route, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

const until = async (fn, ms = 20000, what = 'condition') => {
  const end = Date.now() + ms;
  for (;;) {
    const value = await fn().catch(() => null);
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out: ${what}`);
    await sleep(150);
  }
};

const TEXT = {
  en: { machine: 'Machine', matched: 'Matched by the server address.' },
  zh: { machine: '机器', matched: '已按服务器地址自动关联。' },
};

test('device SSH dialog opens prefilled from the machine record', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-ssh-prefill-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const gpuConfig = path.join(dir, 'gpu_servers.yaml');
  writeFileSync(gpuConfig, 'enabled: true\nservers:\n  - name: gpu-lab\n    host: 198.51.100.23\n    port: 2022\n    user: user\n');
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', WEB,
    '--public-url', 'https://198.51.100.23:8443'],
  { stdio: 'ignore', env: { ...process.env, ARK_GPU_SERVERS_CONFIG: gpuConfig, ARK_PRIVATE_DIR: path.join(dir, 'private') } });
  let browser;
  try {
    await until(() => fetch(base + '/api/health').then((r) => r.ok), 30000, 'server');
    const id = (await api(base, 'POST', '/api/projects', { name: 'Lab' })).summary.id;
    // Register the GPU server in the project (a compute resource, T-0018).
    const projects = path.join(dir, 'projects');
    const folder = readdirSync(projects).map((n) => path.join(projects, n)).find((p) => statSync(p).isDirectory());
    mkdirSync(path.join(folder, 'setup'), { recursive: true });
    writeFileSync(path.join(folder, 'setup', 'project.json'), JSON.stringify({
      profile: { compute: [{ id: 'gpu-lab', kind: 'ssh', gpu_server: 'gpu-lab', use: 'ask' }] } }));
    const connect = await api(base, 'GET', '/api/resources/connect', null, id);
    assert.deepEqual(connect.links.ark, { machine: 'gpu:gpu-lab', how: 'server' }, JSON.stringify(connect) + ' ' + folder);

    browser = await chromium.launch({ headless: true, executablePath: process.env.ARK_TEST_BROWSER || undefined });
    for (const lang of ['en', 'zh']) {
      const L = TEXT[lang];
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.addInitScript((l) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', l);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      await page.goto(`${base}/?lang=${lang}#/home`);
      await until(() => page.evaluate((pid) => window.__ARK__?.S?.project?.id === pid && !document.getElementById('ark-startup'), id), 30000, 'boot');
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 3000 }).catch(() => {});
      // The ARK server device, as the Resources graph's context menu opens it.
      await page.evaluate(async () => {
        const mod = await import('/js/resource-open.js');
        const graph = await (await import('/js/api.js')).api.resourcesGraph();
        const ark = graph.devices.find((d) => d.id === 'ark');
        const store = mod.connectionStore();
        await store.refresh(graph.devices);
        mod.openSshForm(store, ark);
      });
      const dialog = page.locator('.modal');
      await dialog.waitFor({ timeout: 10000 });
      const values = await dialog.evaluate((el) => [...el.querySelectorAll('input, select')].map((i) => i.value));
      // name, host, user, port, alias, key; selects: machine, login.
      const fields = await dialog.evaluate((el) => Object.fromEntries([...el.querySelectorAll('label.field')]
        .map((f) => [f.querySelector('span')?.textContent, f.querySelector('input, select')?.value])));
      assert.equal(fields[L.machine], 'gpu:gpu-lab', JSON.stringify(fields));
      assert.ok(values.includes('198.51.100.23'), JSON.stringify(values));
      assert.ok(values.includes('2022'), JSON.stringify(values));
      assert.ok(values.includes('user'), JSON.stringify(values));
      assert.ok(values.includes('gpu-lab'), JSON.stringify(values));
      assert.ok((await dialog.textContent()).includes(L.matched));
      assert.equal(await dialog.locator('input[placeholder="ark-gpu-lab"]').count(), 1, 'alias suggestion');
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `ssh-dialog-prefill-${lang}.png`) });
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
    await sleep(300);
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* Windows may hold files briefly. */ }
  }
});

const MENU = {
  en: { vscode: 'Open in VS Code', cursor: 'Open in Cursor', terminal: 'Open in terminal', path: 'Copy path', edit: 'SSH connection…',
    setup: 'Open in VS Code…' },
  zh: { vscode: '在 VS Code 中打开', cursor: '在 Cursor 中打开', terminal: '在终端中打开', path: '复制路径', edit: 'SSH 连接…',
    setup: '在 VS Code 中打开…' },
};

// After setup ties a device to its GPU server (a compute resource with
// gpu_server and device_id), right-clicking a repository on it offers VS Code,
// Cursor and the terminal at once, and VS Code opens without any dialog (a
// browser offers the ~/.ssh/config entry to copy).  Unlinked, "Open in VS
// Code…" asks first (the prefilled dialog), never a dead end.
test('graph menu of a remote repository opens it in VS Code directly', { skip: !chromium || !SERVER, timeout: 240000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-ssh-menu-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const gpuConfig = path.join(dir, 'gpu_servers.yaml');
  writeFileSync(gpuConfig, 'enabled: true\nservers:\n  - name: gpu-lab\n    host: 198.51.100.23\n    port: 2022\n    user: user\n');
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', WEB,
    '--public-url', 'https://198.51.100.23:8443'],
  { stdio: 'ignore', env: { ...process.env, ARK_GPU_SERVERS_CONFIG: gpuConfig, ARK_PRIVATE_DIR: path.join(dir, 'private') } });
  let browser;
  try {
    await until(() => fetch(base + '/api/health').then((r) => r.ok), 30000, 'server');
    const id = (await api(base, 'POST', '/api/projects', { name: 'Lab' })).summary.id;
    const projects = path.join(dir, 'projects');
    const folder = readdirSync(projects).map((n) => path.join(projects, n)).find((p) => statSync(p).isDirectory());
    mkdirSync(path.join(folder, 'setup'), { recursive: true });
    writeFileSync(path.join(folder, 'setup', 'project.json'), JSON.stringify({ profile: {
      repositories: [{ id: 'robokit', path: '/data/RoboKit', role: 'code' }],
      compute: [{ id: 'gpu-lab', kind: 'ssh', gpu_server: 'gpu-lab', use: 'ask', device_id: '709270ec09fc' }] } }));
    // The repository's folder on the GPU machine's own setup host.
    const device = 'host:709270ec09fc';
    await api(base, 'POST', '/api/skills/setup/location', { resource: 'project/robokit', path_or_ref: '/data/RoboKit',
      host_label: 'deskhost7k2pq' }, id, { 'X-ARK-Host': '709270ec09fc' });
    const connect = await api(base, 'GET', '/api/resources/connect', null, id);
    assert.deepEqual(connect.links[device], { machine: 'gpu:gpu-lab', how: 'setup' }, JSON.stringify(connect.links));

    browser = await chromium.launch({ headless: true, executablePath: process.env.ARK_TEST_BROWSER || undefined });
    for (const [lang, linked] of [['zh', true], ['en', true], ['zh', false]]) {
      const M = MENU[lang];
      if (!linked) await api(base, 'POST', `/api/resources/connect/${device}`, { link: '' }, id);
      const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
      await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base });
      await context.addInitScript((l) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', l);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      await page.goto(`${base}/?lang=${lang}#/execute/resources`);
      await until(() => page.evaluate((pid) => window.__ARK__?.S?.project?.id === pid && !document.getElementById('ark-startup'), id), 30000, 'boot');
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 3000 }).catch(() => {});
      const canvas = page.locator('canvas.resources-canvas');
      await canvas.waitFor({ timeout: 15000 });
      await sleep(1500);
      // Find the repository row by its tooltip.
      const box = await canvas.boundingBox();
      let spot = null;
      for (let y = box.y + 10; y < box.y + box.height && !spot; y += 14) {
        for (let x = box.x + 10; x < box.x + box.width && !spot; x += 24) {
          await page.mouse.move(x, y);
          const tip = await page.evaluate(() => {
            const el = document.querySelector('.resources-tooltip');
            return el && !el.classList.contains('hidden') ? el.textContent : '';
          });
          if (tip.includes('/data/RoboKit')) spot = { x, y };
        }
      }
      assert.ok(spot, 'repository row found');
      await page.mouse.click(spot.x, spot.y, { button: 'right' });
      const menu = page.locator('.graph-menu');
      await menu.waitFor({ timeout: 5000 });
      const text = await menu.textContent();
      if (linked) {
        for (const want of [M.vscode, M.cursor, M.terminal, M.path, M.edit]) assert.ok(text.includes(want), `${lang}: ${want} in ${text}`);
        assert.ok(!text.includes(M.setup), text);
      } else {
        assert.ok(text.includes(M.setup), text);
        assert.ok(!text.includes(M.terminal), text);
      }
      await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `graph-menu-${linked ? 'linked' : 'unlinked'}-${lang}.png`) });
      if (linked && lang === 'zh') {
        // VS Code opens at once (user@host:port, no dialog); the toast offers the
        // machine's entry (ark-gpu-lab), never one named after the device's hostname.
        await page.evaluate(() => {
          window.__opened = [];
          const click = HTMLAnchorElement.prototype.click;
          HTMLAnchorElement.prototype.click = function () { if (this.href.startsWith('vscode:')) window.__opened.push(this.href); else click.call(this); };
        });
        await menu.getByText(M.vscode, { exact: true }).click();
        const opened = await until(() => page.evaluate(() => window.__opened[0] || null), 5000, 'VS Code link');
        assert.match(opened, /^vscode:\/\/vscode-remote\/ssh-remote\+[0-9a-f]+\/data\/RoboKit$/);
        assert.equal(await page.locator('.modal').count(), 0, 'no dialog');
        const offer = page.locator('#toast-root .toast', { hasText: 'ark-gpu-lab' });
        await offer.waitFor({ timeout: 5000 });
        assert.ok(!(await page.locator('#toast-root').textContent()).includes('ark-deskhost7k2pq'));
        await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, 'graph-menu-vscode-entry-zh.png') });
        await offer.locator('.toast-action').click();
        const entry = await until(() => page.evaluate(() => navigator.clipboard.readText()).then((v) => (v.includes('Host ') ? v : null)), 5000, 'entry');
        assert.ok(entry.includes('Host ark-gpu-lab') && entry.includes('HostName 198.51.100.23') && entry.includes('Port 2022'), entry);
        // Pasted: the next open uses the alias.
        await page.mouse.click(spot.x, spot.y, { button: 'right' });
        await menu.waitFor({ timeout: 5000 });
        await menu.getByText(M.vscode, { exact: true }).click();
        await until(() => page.evaluate(() => window.__opened.length === 2), 5000, 'second open');
        assert.equal(await page.evaluate(() => window.__opened[1]), 'vscode://vscode-remote/ssh-remote+ark-gpu-lab/data/RoboKit');
        // Terminal: a browser copies the ssh command for the clicked folder.
        await page.mouse.click(spot.x, spot.y, { button: 'right' });
        await menu.waitFor({ timeout: 5000 });
        await menu.getByText(M.terminal, { exact: true }).click();
        const copied = await until(() => page.evaluate(() => navigator.clipboard.readText()).then((v) => v || null), 5000, 'clipboard');
        assert.equal(copied, "ssh -t -p 2022 -- user@198.51.100.23 'cd -- ''/data/RoboKit'' && exec ${SHELL:-/bin/sh} -l'");
        await page.waitForFunction(() => document.querySelector('#toast-root')?.textContent.includes('SSH'));
        await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, 'graph-menu-terminal-copied-zh.png') });
      }
      if (!linked) {
        // "Open in VS Code…" opens the prefilled dialog.
        await menu.getByText(M.setup).click();
        await page.locator('.modal').waitFor({ timeout: 5000 });
        await page.screenshot({ path: path.join(process.env.ARK_TEST_SHOTS || dir, `graph-menu-unlinked-dialog-${lang}.png`) });
      }
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
    await sleep(300);
    try { rmSync(dir, { recursive: true, force: true }); } catch { /* Windows may hold files briefly. */ }
  }
});
