// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/launch_dialog.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder>; headless only)
// The "Run an agent in ARK" dialog (2026-11-23): Claude Code offers its
// reasoning levels, the model list names the default, access is two choices,
// worktree / subfolder / title sit under Advanced, the task comes from the
// open node, the first message may stay empty, and the choice is remembered
// for the next dialog.  zh and en.
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

test('launch dialog: effort, defaults, advanced, remembered choice', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-launch-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const claude = path.join(dir, process.platform === 'win32' ? 'claude.cmd' : 'claude');
  writeFileSync(claude, process.platform === 'win32' ? `@"${process.execPath}" "${FAKE}" %*\r\n`
    : `#!/bin/sh\nexec "${process.execPath}" "${FAKE}" "$@"\n`);
  if (process.platform !== 'win32') chmodSync(claude, 0o755);
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !/^ARK_TASK_/i.test(k)));
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore', env: { ...env, ARK_TASK_CLAUDE_COMMAND: `"${claude}" -p "{prompt}"` } });
  let browser;
  const started = [];
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const zh = lang === 'zh';
      const context = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
      await context.addInitScript((l) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', l);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      for (let i = 0; ; i += 1) { try { await page.goto(`${base}/api/health`); break; } catch (err) { if (i > 100) throw err; await sleep(200); } }
      await page.goto(`${base}/?lang=${lang}#/execute/sessions`);
      if (zh) {
        await page.evaluate(async () => {
          const { api } = await import('/js/api.js');
          await fetch('/api/projects', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'Launch dialog' }) });
          await api.updateSettings({ agents: { providers: { claude: { env_names: [], enabled: true } } } });
        });
        await page.reload();
      }
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
      const open = async () => {
        await page.goto(`${base}/?lang=${lang}#/execute/board`);
        await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
        for (let i = 0; i < 3; i += 1) {
          const label = zh ? '在 ARK 中运行智能体' : 'Run agent in ARK';
          await page.locator(`[data-new-session], [data-launch-agent], button[aria-label="${label}"]`).filter({ visible: true }).first()
            .click({ timeout: 30000 }).catch(() => {});
          try { await page.locator('.modal .launch-form').first().waitFor({ timeout: 10000 }); return; } catch (err) {
            if (i === 2) { await page.screenshot({ path: path.join(shots, `launch-dialog-${lang}-failed.png`) }); throw err; }
          }
          await page.keyboard.press('Escape');
        }
      };
      await open();
      const modal = page.locator('.modal').last();
      const platform = modal.locator('[data-launch-field="platform"] select');
      assert.equal(await platform.inputValue(), zh ? 'claude' : 'claude', 'a real agent comes first');
      // Reasoning levels from the provider catalog, default named.
      const effort = modal.locator('[data-launch-field="effort"] select');
      assert.ok(await effort.isVisible(), 'effort shown for Claude Code');
      const levels = await effort.locator('option').evaluateAll((os) => os.map((o) => o.value));
      assert.deepEqual(levels, ['', 'low', 'medium', 'high', 'xhigh', 'max']);
      assert.ok(await effort.isEnabled());
      // Two access choices, read only first and checked.
      assert.deepEqual(await modal.locator('[data-launch-field="access"] input').evaluateAll((os) => os.map((o) => o.value)), ['read_only', 'full_access']);
      assert.equal(await modal.locator('[data-launch-field="access"] input:checked').getAttribute('value'), 'read_only');
      // Advanced is collapsed; no free-text agent field.
      assert.equal(await modal.locator('[data-launch-advanced]').getAttribute('open'), null);
      assert.equal(await modal.locator('[data-launch-field="worktree"]').isVisible(), false);
      assert.equal(await modal.locator('[data-launch-field="agent"]').isVisible(), false);
      assert.equal(await modal.locator('[data-launch-field="prompt"] textarea').inputValue(), '');
      await page.screenshot({ path: path.join(shots, `launch-dialog-${lang}.png`) });
      if (zh) {
        // Start with an empty first message, high effort, full access.
        await effort.selectOption('high');
        await modal.locator('[data-launch-field="access"] input[value="full_access"]').check();
        await page.locator('.modal-foot .btn.primary').click();
        await page.locator('.modal .launch-form').first().waitFor({ state: 'detached', timeout: 20000 });
        const list = await page.evaluate(async () => (await (await fetch('/api/agent-sessions')).json()).sessions);
        assert.equal(list.length, 1);
        started.push(list[0].id);
        assert.equal(list[0].effort, 'high');
        assert.equal(list[0].access_mode, 'full_access');
        // The next dialog starts with the same choice.
        await open();
        const again = page.locator('.modal').last();
        assert.equal(await again.locator('[data-launch-field="effort"] select').inputValue(), 'high');
        assert.equal(await again.locator('[data-launch-field="access"] input:checked').getAttribute('value'), 'full_access');
        await page.keyboard.press('Escape');
        await page.evaluate(() => localStorage.removeItem('ark.launchDefaults.v1'));
      }
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    for (const id of started) await fetch(`${base}/api/agent-sessions/${id}/stop`, { method: 'POST', headers: { 'Content-Type': 'application/json', Origin: base }, body: '{}' }).catch(() => {});
    await sleep(800);
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* best effort */ }
  }
});
