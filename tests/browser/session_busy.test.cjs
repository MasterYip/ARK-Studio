// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/session_busy.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder>; headless only)
// A long-lived Claude Code session whose turn has ended while a sub-agent
// and a background command still run shows Running with the reason; the
// ARK watcher alone does not count; no typing indicator; Enter sends at once;
// the empty composer's button is "Stop background tasks…" with a choice.
// When the work finishes the session is idle.  zh and en.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, writeFileSync, chmodSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const FAKE = path.join(__dirname, 'fixtures', 'fake-claude-input.cjs');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function api(base, method, route, body) {
  const res = await fetch(base + route, {
    method, headers: body ? { 'Content-Type': 'application/json', Origin: base } : { Origin: base },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${JSON.stringify(data)}`);
  return data;
}

async function until(fn, ms = 20000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timed out');
    await sleep(150);
  }
}

test('background work keeps a session running between turns', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-busy-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  // ARK puts its flags right after the program, so the fake is the program.
  const claude = path.join(dir, process.platform === 'win32' ? 'claude.cmd' : 'claude');
  writeFileSync(claude, process.platform === 'win32' ? `@"${process.execPath}" "${FAKE}" %*\r\n`
    : `#!/bin/sh\nexec "${process.execPath}" "${FAKE}" "$@"\n`);
  if (process.platform !== 'win32') chmodSync(claude, 0o755);
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore', env: { ...process.env, ARK_TASK_CLAUDE_COMMAND: `"${claude}" -p "{prompt}"` } });
  const ids = [];
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) { if (i > 100) throw err; await sleep(200); }
    }
    await api(base, 'POST', '/api/projects', { name: 'Busy sessions' });
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const zh = lang === 'zh';
      const s = (await api(base, 'POST', '/api/agent-sessions', { platform: 'claude', title: `Busy ${lang}`, prompt: 'start background' })).session;
      ids.push(s.id);
      const busy = await until(async () => {
        const got = (await api(base, 'GET', `/api/agent-sessions/${s.id}`)).session;
        return got.status === 'idle' && got.busy ? got : null;
      });
      assert.ok(busy.capabilities?.stream_input, 'a long-lived session');
      assert.deepEqual(busy.busy, { subagents: 1, background: 1 }, 'the watcher does not count');
      const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
      await context.addInitScript((l) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', l);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}&session=${s.id}#/execute/sessions`);
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
      const head = page.locator('.sw-title-line');
      await head.waitFor({ timeout: 30000 });
      const running = zh ? '运行中' : 'Running';
      const reason = zh ? '1 个子智能体 · 1 个后台任务' : '1 sub-agent · 1 background task';
      await head.locator('.sw-status.running').waitFor({ timeout: 15000 });
      await head.locator('[data-busy-note]', { hasText: reason }).waitFor({ timeout: 15000 });
      // The compact header: name and live state only; the rest is in Session properties.
      assert.equal(await page.locator('.sw-main-head .sw-title-meta, .sw-main-bar').count(), 0, 'one header row');
      assert.equal(await page.locator('.sw-main-head', { hasText: s.id }).count(), 0, 'no session id in the header');
      await page.locator('.sw-side [data-fact="session_id"]', { hasText: s.id }).waitFor();
      await page.locator('.sw-side [data-fact="live"] .sw-live.running').waitFor();
      await page.locator('.sw-side [data-fact="access"]').waitFor();
      // The composer sits at the bottom of the pane (above the status bar) at every width,
      // on the pane's background; a narrow pane shows the access mode as its icon only.
      for (const width of [1400, 1000]) {
        await page.setViewportSize({ width, height: 900 });
        await page.waitForTimeout(300);
        const at = await page.evaluate(() => {
          const status = getComputedStyle(document.body).getPropertyValue('--wb-status-h');
          const comp = document.querySelector('.sw-composer');
          const access = document.querySelector('.sw-access-chip');
          return { composer: comp.getBoundingClientRect().bottom, pane: window.innerHeight - (parseFloat(status) || 0),
            main: document.querySelector('.sw-main').getBoundingClientRect().bottom, bg: getComputedStyle(comp).backgroundColor,
            accessClipped: access ? access.scrollWidth > access.clientWidth + 1 : false };
        });
        assert.ok(Math.abs(at.composer - at.pane) <= 4, `composer bottom ${at.composer} vs pane bottom ${at.pane} at ${width}`);
        assert.ok(Math.abs(at.main - at.composer) <= 4, `composer is the last row (${at.main} / ${at.composer})`);
        assert.equal(at.bg, 'rgba(0, 0, 0, 0)', 'the composer row has the pane background');
        assert.equal(at.accessClipped, false, `access label not clipped at ${width}`);
      }
      await page.setViewportSize({ width: 1400, height: 900 });
      await page.locator('.sw-session [data-busy-note]', { hasText: reason }).first().waitFor();
      await page.locator('.sw-fact', { hasText: `${running} · ${reason}` }).first().waitFor();
      // The chip still lists the watcher with the build.
      await page.locator('[data-activity-chip="background"]', { hasText: '2' }).waitFor();
      assert.equal(await page.locator('.sw-live-row:not(.hidden)').count(), 0, 'no typing indicator');
      // The empty composer's button: Stop background tasks… with a choice.
      const send = page.locator('.sw-send[data-stop-background]');
      await send.waitFor();
      assert.equal(await send.getAttribute('aria-label'), zh ? '停止后台任务…' : 'Stop background tasks…');
      await page.screenshot({ path: path.join(shots, `session-busy-${lang}.png`) });
      await send.click();
      await page.locator('.graph-menu-item', { hasText: zh ? '停止所有后台任务和子智能体' : 'Stop all background tasks and sub-agents' }).waitFor();
      await page.locator('.graph-menu-item', { hasText: zh ? '结束会话' : 'End session' }).waitFor();
      await page.keyboard.press('Escape');
      // Enter sends at once (no turn runs), also while background work runs.
      const input = page.locator('.sw-composer-input');
      await input.fill('finish background');
      await input.press('Enter');
      await until(async () => (await api(base, 'GET', `/api/agent-sessions/${s.id}/transcript`)).items
        .some((i) => i.type === 'message' && /echo: finish background/.test(i.text || '')));
      const steers = (await api(base, 'GET', `/api/agent-sessions/${s.id}`)).session.steer_queue || [];
      assert.ok(!steers.some((x) => x.status === 'queued'), 'not queued');
      // All work done: idle, no reason, the button is Send again.
      await head.locator('.sw-status.idle').waitFor({ timeout: 20000 });
      assert.equal(await head.locator('[data-busy-note]').count(), 0);
      await until(async () => (await page.locator('.sw-send[data-stop-background]').count()) === 0, 10000);
      await page.screenshot({ path: path.join(shots, `session-idle-${lang}.png`) });
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    for (const id of ids) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
    await sleep(1000);
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* best effort */ }
  }
});
