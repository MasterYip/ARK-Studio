// Optional browser regression (Playwright) against a real ark-server with sign-in:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/invite_join.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge> to pick a browser; ARK_TEST_SHOTS=<dir> keeps screenshots)
// Project invite links, English and Chinese: a project admin creates a link in
// Settings > Members; someone without an account opens it, creates an account
// and lands in the project; an existing account signs in and joins from the
// Join dialog; a bogus link says so.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const OWNER_PASSWORD = 'owner-password-123';

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function call(context, base, method, route, body) {
  const res = await context.request.fetch(base + route, {
    method, headers: { Origin: base, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    data: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok()) throw new Error(`${method} ${route}: ${res.status()} ${JSON.stringify(data)}`);
  return data;
}

async function signIn(page, base, user, password) {
  await page.locator('.login-form:not(.login-signup) input[name="username"]').fill(user);
  await page.locator('.login-form:not(.login-signup) input[name="password"]').fill(password);
  await page.locator('.login-form:not(.login-signup) .login-submit').click();
}

test('invite links: create, sign up and join, existing account joins, bogus link', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-invite-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--auth-user', 'owner', '--sessions-root', path.join(dir, 'sessions'),
    '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ARK_AUTH_PASSWORD: OWNER_PASSWORD } });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  let browser;
  try {
    for (let i = 0; ; i += 1) {
      try { const r = await fetch(`${base}/api/health`); if (r.ok) break; throw new Error(String(r.status)); } catch (err) {
        if (i > 100) throw new Error(`server did not start: ${err.message}\n${log}`);
        await new Promise((r) => setTimeout(r, 200));
      }
    }
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const newContext = async (lang, init = null) => {
      const context = await browser.newContext({ viewport: { width: 1280, height: 860 } });
      await context.addInitScript(([l, extra]) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', l);
        if (extra) sessionStorage.setItem('ark.tabProject', extra);
        localStorage.setItem('ark.welcome.v1:owner', '1');
      }, [lang, init]);
      return context;
    };
    const watch = (page, errors) => page.on('pageerror', (error) => errors.push(error.message));

    // The owner signs in once and makes one project per language.
    const ownerCtx = await newContext('en');
    const ownerPage = await ownerCtx.newPage();
    await ownerPage.goto(`${base}/login`);
    await signIn(ownerPage, base, 'owner', OWNER_PASSWORD);
    await ownerPage.waitForURL((url) => !url.pathname.endsWith('/login'));
    await ownerPage.close();

    for (const lang of ['en', 'zh']) {
      const zh = lang === 'zh';
      const name = zh ? '邀请测试' : 'Invite demo';
      const project = (await call(ownerCtx, base, 'POST', '/api/projects', { name })).summary;
      const errors = [];

      // 1. Settings > Members: create a link with the defaults.
      const adminCtx = await newContext(lang, project.id);
      await adminCtx.addCookies(await ownerCtx.cookies());
      const admin = await adminCtx.newPage();
      watch(admin, errors);
      await admin.goto(`${base}/?settings=members#/settings`);
      await admin.locator('[data-invite-panel]').waitFor({ timeout: 30000 });
      assert.equal(await admin.locator('[data-invite-panel] h3').textContent(), zh ? '邀请链接' : 'Invite links');
      await admin.locator('[data-invite-create]').click();
      const urlBox = admin.locator('[data-invite-url]');
      await urlBox.waitFor();
      const link = await urlBox.inputValue();
      assert.match(link, new RegExp(`^${base}/#/join/[A-Za-z0-9_-]{32}$`));
      await admin.locator('[data-invite-id]').first().waitFor();
      await admin.locator('[data-invite-panel]').screenshot({ path: path.join(shots, `invite-settings-${lang}.png`) });

      // 2. Someone without an account opens the link, creates one and joins.
      const guestCtx = await newContext(lang);
      const guest = await guestCtx.newPage();
      watch(guest, errors);
      await guest.goto(link);
      await guest.waitForURL(/\/login/);
      const banner = guest.locator('[data-invite-banner="valid"]');
      await banner.waitFor({ timeout: 20000 });
      const bannerText = await banner.textContent();
      assert.ok(bannerText.includes(name), bannerText);
      assert.ok(bannerText.includes(zh ? '可编辑' : 'Edit'), bannerText);
      const form = guest.locator('[data-invite-signup]');
      await form.waitFor();
      await guest.screenshot({ path: path.join(shots, `invite-signup-${lang}.png`) });
      const user = `guest_${lang}`;
      await form.locator('input[name="username"]').fill(user);
      await form.locator('input[name="display_name"]').fill(zh ? '新成员' : 'New Member');
      await form.locator('input[name="password"]').fill('pass-1234');
      await form.locator('input[name="confirm"]').fill('pass-1234-x');
      await form.locator('.login-submit').click();
      assert.equal(await form.locator('.login-error').textContent(), zh ? '两次输入的密码不一致。' : 'The passwords do not match.');
      await form.locator('input[name="confirm"]').fill('pass-1234');
      await form.locator('.login-submit').click();
      await guest.waitForURL((url) => !url.pathname.endsWith('/login'), { timeout: 30000 });
      await guest.locator('.project-picker .p-name').filter({ hasText: name }).waitFor({ timeout: 30000 });
      await guest.waitForTimeout(1500);
      assert.equal(await guest.locator('.welcome-layer').count(), 0, 'no first-run Welcome after joining');
      await guest.screenshot({ path: path.join(shots, `invite-joined-${lang}.png`) });
      const me = await call(guestCtx, base, 'GET', '/api/auth/session');
      assert.equal(me.user, user);
      const invites = await call(ownerCtx, base, 'GET', `/api/org/projects/${project.id}/invites`);
      assert.equal(invites.invites[0].uses, 1);
      assert.equal(invites.invites[0].state, 'used_up');

      // 3. An existing account: the link opens sign-in, then the Join dialog.
      const bob = `bob_${lang}`;
      await call(ownerCtx, base, 'POST', '/api/org/users', { username: bob, password: 'bob-password-1', role: 'member' });
      const second = await call(ownerCtx, base, 'POST', `/api/org/projects/${project.id}/invites`, { permission: 'read' });
      const bobCtx = await newContext(lang);
      const bobPage = await bobCtx.newPage();
      watch(bobPage, errors);
      await bobPage.goto(second.url);
      await bobPage.locator('[data-invite-banner="valid"]').waitFor({ timeout: 20000 });
      await bobPage.locator('[data-invite-tab="signin"]').click();
      await signIn(bobPage, base, bob, 'bob-password-1');
      const dialog = bobPage.locator('[data-join-dialog="join"]');
      await dialog.waitFor({ timeout: 30000 });
      const dialogText = await dialog.textContent();
      assert.ok(dialogText.includes(name) && !dialogText.includes('null'), dialogText);
      assert.ok(!(await bobPage.evaluate(() => location.hash)).includes('join'), 'the token left the address bar');
      await bobPage.screenshot({ path: path.join(shots, `invite-dialog-${lang}.png`) });
      await bobPage.locator('.modal .btn.primary').click();
      await bobPage.locator('.project-picker .p-name').filter({ hasText: name }).waitFor({ timeout: 30000 });
      const detail = await call(ownerCtx, base, 'GET', `/api/org/projects/${project.id}`);
      assert.ok(detail.project.grants.some((g) => g.username === bob && g.permission === 'read'));

      // 4. A bogus link: a clear message, sign-in still works.
      const lostCtx = await newContext(lang);
      const lost = await lostCtx.newPage();
      watch(lost, errors);
      await lost.goto(`${base}/#/join/${'Z'.repeat(32)}`);
      const unusable = lost.locator('[data-invite-banner="unusable"]');
      await unusable.waitFor({ timeout: 20000 });
      assert.equal(await unusable.textContent(), zh ? '邀请链接无效，请检查是否完整复制。'
        : 'This invite link is not valid. Check that you copied all of it.');
      await lost.screenshot({ path: path.join(shots, `invite-invalid-${lang}.png`) });

      assert.deepEqual(errors, [], lang);
      for (const ctx of [adminCtx, guestCtx, bobCtx, lostCtx]) await ctx.close();
    }
    await ownerCtx.close();
  } finally {
    if (browser) await browser.close();
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    rmSync(dir, { recursive: true, force: true });
  }
});
