// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/session_sidebar.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder> keeps the screenshots)
// Headless only.  The Sessions sidebar view and the Sessions page:
//   - a session started from the sidebar, open across workspaces, context
//     attached in one click, an image pasted into the composer;
//   - a recorded Claude Code stream: sub-agent and background-task chips (two
//     kinds), the details rail tabs, a sub-agent opened in its own tab, no
//     raw JSON and no task notices in the conversation;
//   - Enter queues while a turn runs, "Send now" interrupts it, Ctrl+Enter
//     sends at once; the session menu; no horizontal scrolling;
//   - performance with a 6k-event stream: no long task over the budget when
//     the page opens it and while it streams.
// English and Chinese; a narrow window keeps the classic layout.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, existsSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const FAKE = path.join(__dirname, 'fixtures', 'fake-claude-stream.cjs');
const LONG_TASK_BUDGET = Number(process.env.ARK_LONG_TASK_BUDGET || 150);   // ms; headless CI machines are slower
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP8z8DwnwEKmBgYGBgAJAABBCBlDwAAAABJRU5ErkJggg==';

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

const until = async (fn, ms = 20000) => {
  const end = Date.now() + ms;
  for (;;) {
    const value = await fn();
    if (value) return value;
    if (Date.now() > end) throw new Error('timed out');
    await new Promise((r) => setTimeout(r, 200));
  }
};

async function startServer(dir, env) {
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ARK_TASK_CUSTOM_COMMAND: `"${process.execPath}" "${FAKE}"`, ...env } });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  await until(() => api(base, 'GET', '/api/health').then(() => true, () => false), 30000).catch(() => {
    throw new Error(`server did not start\n${log}`);
  });
  return { base, server };
}

async function stopServer(base, server, ids) {
  for (const id of ids) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
  await new Promise((r) => setTimeout(r, 1500));
  server.kill();
  await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
}

async function newPage(browser, lang, viewport = { width: 1440, height: 900 }) {
  const context = await browser.newContext({ viewport });
  await context.addInitScript((lang) => {
    localStorage.setItem('ark.prefsV2', '1');
    localStorage.setItem('ark.lang', lang);
    localStorage.setItem('ark.welcome.v1:local', '1');
    window.__long = [];
    new PerformanceObserver((list) => { for (const e of list.getEntries()) window.__long.push(Math.round(e.duration)); })
      .observe({ type: 'longtask', buffered: true });
  }, lang);
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', (error) => {
    errors.push(error.message);
    console.log('PAGEERROR', error.message, String(error.stack || '').split(/\r?\n/).slice(0, 4).join(' | '));
  });
  return { context, page, errors };
}

async function dismissTour(page) {
  await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
}

const noSideScroll = (page, selector) => page.evaluate((sel) => [...document.querySelectorAll(sel)]
  .filter((el) => el.offsetParent).map((el) => el.scrollWidth - el.clientWidth), selector);

test('sessions sidebar and page: chat, chips, tabs, queue, images, layout', { skip: !chromium || !SERVER, timeout: 420000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-ssb-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  // followup 0: the recorded agent takes a message at the next turn, like `claude -p`.
  const { base, server } = await startServer(dir, { ARK_TASK_CUSTOM_LABEL: 'Recorded Claude stream', ARK_TASK_CUSTOM_FOLLOWUP: '0' });
  const ids = [];
  let browser;
  let emptyIdForMenu = '';
  try {
    await api(base, 'POST', '/api/projects', { name: 'Sidebar test' });
    const unit = (await api(base, 'POST', '/api/units', { type: 'task', title: 'Sidebar target' })).unit;
    const streamed = (await api(base, 'POST', '/api/agent-sessions', { platform: 'custom', title: 'Recorded stream', prompt: 'go' })).session;
    ids.push(streamed.id);
    await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}/transcript`)).items.some((i) => i.id === 'toolu_H'));

    // The conversation holds no raw stream JSON and no centred task notices.
    const full = await api(base, 'GET', `/api/agent-sessions/${streamed.id}/transcript?tail=200`);
    for (const item of full.items) {
      assert.doesNotMatch(String(item.text || ''), /^\s*\{"type"/, `raw JSON in ${item.id}`);
      assert.ok(!(item.type === 'notice' && item.level !== 'reasoning'), `notice ${item.id}: ${item.text}`);
    }
    assert.ok(full.activity.length >= 4, 'activity list for the chips');

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of (process.env.ARK_TEST_LANGS || 'en,zh').split(',')) {
      const zh = lang === 'zh';
      const { context, page, errors } = await newPage(browser, lang);
      await page.goto(`${base}/?lang=${lang}#/map`);
      await dismissTour(page);
      // The Sessions view sits in the left bar (an activity icon).
      const tab = page.locator('[data-view-icon="sessions"]');
      await tab.waitFor({ timeout: 30000 });
      assert.equal(await tab.getAttribute('aria-label'), zh ? '会话' : 'Sessions');
      if ((await tab.getAttribute('aria-pressed')) !== 'true') await tab.click();
      const sidebar = page.locator('[data-session-sidebar]');
      await sidebar.waitFor({ state: 'visible' });

      if (!zh) {
        // A new session from the sidebar (Echo demo agent), Enter sends.
        await sidebar.locator('.ssb-provider').selectOption('echo');
        const input = sidebar.locator('.ssb-input');
        await input.fill('Summarise the plan');
        await input.press('Shift+Enter');
        assert.equal(await input.inputValue(), 'Summarise the plan\n', 'Shift+Enter adds a new line');
        await input.press('Enter');
        await sidebar.locator('.ssb-assistant').first().waitFor({ timeout: 30000 });
        const echoId = await sidebar.locator('.ssb-switch').inputValue();
        ids.push(echoId);
        await page.screenshot({ path: path.join(shots, 'sidebar-map-en.png') });

        // It stays open while the main area changes workspace.
        await page.goto(`${base}/?lang=${lang}#/execute/board`);
        await sidebar.waitFor({ state: 'visible' });
        assert.equal(await sidebar.locator('.ssb-switch').inputValue(), echoId, 'the same session after navigation');

        // One click: attach the open node; paste an image; send both.
        await page.goto(`${base}/?lang=${lang}&unit=${unit.uid}#/map`);
        const suggest = sidebar.locator(`[data-ssb-suggest="${unit.uid}"]`);
        await suggest.waitFor({ timeout: 20000 });
        await suggest.click();
        await sidebar.locator('.ssb-ctx').filter({ hasText: unit.uid }).waitFor();
        await sidebar.locator('.ssb-input').evaluate((el, png) => {
          const bytes = Uint8Array.from(atob(png), (c) => c.charCodeAt(0));
          const data = new DataTransfer();
          data.items.add(new File([bytes], 'shot.png', { type: 'image/png' }));
          el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }));
        }, PNG);
        await sidebar.locator('.sw-img-thumb').waitFor();
        await page.screenshot({ path: path.join(shots, 'sidebar-context-image-en.png') });
        await sidebar.locator('.ssb-input').fill('Use this node');
        await sidebar.locator('.ssb-input').press('Enter');
        await sidebar.locator('.sw-img-thumb').waitFor({ state: 'detached', timeout: 20000 });
        const turn = await until(async () => (await api(base, 'GET', `/api/agent-sessions/${echoId}/transcript`)).items
          .find((item) => item.role === 'user' && /Use this node/.test(item.text) && item.images?.length), 30000);
        assert.match(turn.text, /context attached: 2 item/);
        await sidebar.locator('.sw-turn-image img').first().waitFor({ timeout: 20000 });
        // A new session can start without a first message.
        await sidebar.locator('.ssb-switch').selectOption('');
        await sidebar.locator('.ssb-send').click();
        const emptyId = await until(async () => {
          const value = await sidebar.locator('.ssb-switch').inputValue();
          return value && value !== echoId ? value : null;
        });
        ids.push(emptyId);
        emptyIdForMenu = emptyId;
        assert.equal((await api(base, 'GET', `/api/agent-sessions/${emptyId}`)).session.title, '');
        assert.match(await sidebar.locator('.ssb-switch option:checked').textContent(), /New session · Echo/);
        await sidebar.locator('.ssb-switch').selectOption(echoId);
        await sidebar.locator('.sw-turn-image').first().scrollIntoViewIfNeeded();
        await until(() => sidebar.locator('.sw-turn-image img').first().evaluate((img) => img.complete && img.naturalWidth > 0), 15000);
      }

      // Chips: sub-agents and background tasks are two kinds.
      await sidebar.locator('.ssb-switch').selectOption(streamed.id);
      const agents = sidebar.locator('[data-activity-chip="subagent"]');
      const bg = sidebar.locator('[data-activity-chip="background"]');
      await bg.waitFor({ state: 'visible', timeout: 20000 });
      assert.equal((await agents.textContent()).trim(), zh ? '1 个子智能体运行中' : '1 sub-agent running');
      assert.equal((await bg.textContent()).trim(), zh ? '2 个后台任务' : '2 background tasks');
      await bg.click();
      assert.equal(await sidebar.locator('.sa-pop [data-activity-section="background"] .sa-row').count(), 2);
      assert.equal(await sidebar.locator('.sa-pop [data-activity-section="subagent"] .sa-row').count(), 2, 'A finished, B running');
      await page.screenshot({ path: path.join(shots, `sidebar-chips-${lang}.png`) });
      await page.keyboard.press('Escape');
      await sidebar.locator('.sa-pop [data-activity-section="background"] .sa-row').first().click();

      // No horizontal scrolling at the sidebar widths.
      for (const width of [320, 420, 600]) {
        await sidebar.evaluate((el, w) => { el.style.width = `${w}px`; }, width);
        await page.waitForTimeout(150);
        for (const delta of await noSideScroll(page, '.ssb-thread')) assert.ok(delta <= 1, `sidebar ${width}px scrolls sideways by ${delta}`);
      }
      await sidebar.evaluate((el) => { el.style.width = ''; });

      // The Sessions page: chips, rail tabs, a sub-agent tab, the menu.
      await page.goto(`${base}/?lang=${lang}&session=${streamed.id}#/execute/sessions`);
      const pageChip = page.locator('.sw-main [data-activity-chip="background"]');
      await pageChip.waitFor({ timeout: 20000 });
      assert.equal((await pageChip.textContent()).trim(), zh ? '2 个后台任务' : '2 background tasks');
      // Wide window: the list is in the Sessions view, the details in Session
      // properties; the conversation takes the page.
      await sidebar.locator('[data-session-list] .sw-session').first().waitFor();
      assert.equal(await page.locator('.sw-workspace.sw-docked').count(), 1);
      assert.equal(await sidebar.locator('.ssb-composer').isVisible(), false, 'the Sessions view shows the list here');
      await page.locator('[data-view="sessionprops"] .sw-side-tab').first().waitFor();
      if (!zh) {
        for (const width of [1280, 1600, 1920]) {
          await page.setViewportSize({ width, height: 960 });
          await page.waitForTimeout(400);
          await page.screenshot({ path: path.join(shots, `runs-layout-${width}.png`) });
          for (const delta of await noSideScroll(page, '.sw-thread')) assert.ok(delta <= 1, `page ${width}px scrolls sideways by ${delta}`);
        }
        await page.setViewportSize({ width: 1440, height: 900 });
      }
      // Rename from the list: right-click, or F2 on the focused row.
      const newTitle = zh ? '回放会话' : 'Replayed stream';
      await page.locator(`[data-session-row="${streamed.id}"]`).click({ button: 'right' });
      await page.locator('.graph-menu-item', { hasText: zh ? '重命名' : 'Rename' }).click({ timeout: 8000 })
        .catch(async (err) => { await page.screenshot({ path: path.join(shots, `fail-rename-${lang}.png`) }); throw err; });
      await page.locator('.sw-rename').fill(newTitle);
      await page.locator('.sw-rename').press('Enter');
      await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}`)).session.title === newTitle);
      await page.locator('.sw-main h2', { hasText: newTitle }).waitFor();
      await page.locator(`[data-session-row="${streamed.id}"]`).focus();
      await page.keyboard.press('F2');
      await page.locator('.sw-rename').press('Escape');
      assert.equal((await api(base, 'GET', `/api/agent-sessions/${streamed.id}`)).session.title, newTitle, 'Esc cancels');
      // The stored-contexts block: its count is the rows it shows.
      const ctxCount = Number((await sidebar.locator('[data-session-library] .sw-count').textContent()).trim());
      const ctxRows = await sidebar.locator('[data-session-library] .ctx-list > :not(.ctx-empty-line)').count();
      assert.equal(ctxCount, ctxRows, 'the context count matches its rows');
      assert.match(await sidebar.locator('[data-session-library] .ctx-usage').textContent(), zh ? /已存/ : /stored/);
      // Collapsible blocks remember their state.
      await sidebar.locator('[data-block="groups"]').click();
      assert.equal(await sidebar.locator('[data-block="groups"]').getAttribute('aria-expanded'), 'true');
      if (zh) {
        for (const width of [320, 420]) {
          await sidebar.evaluate((el, w) => { el.closest('.wb-side, .wb-container, aside')?.style.setProperty('width', `${w}px`); el.style.width = `${w}px`; }, width);
          await page.waitForTimeout(300);
          await page.screenshot({ path: path.join(shots, `runs-list-zh-${width}.png`) });
        }
        await sidebar.evaluate((el) => { el.style.width = ''; el.closest('.wb-side, .wb-container, aside')?.style.removeProperty('width'); });
        await page.waitForTimeout(300);
      }
      await sidebar.locator('[data-block="groups"]').click();
      if (!zh && emptyIdForMenu) {
        // 关闭 / End, then 删除 / Delete (the transcript is kept) from the list menu.
        const row = page.locator(`[data-session-row="${emptyIdForMenu}"]`);
        await row.click({ button: 'right' });
        await page.locator('.graph-menu-item', { hasText: 'Close session' }).click();
        await until(async () => (await api(base, 'GET', `/api/agent-sessions/${emptyIdForMenu}`)).session.closed === true);
        await row.click({ button: 'right' });
        await page.locator('.graph-menu-item', { hasText: 'Delete' }).click();
        await page.locator('.modal [data-delete-files]').waitFor();
        await page.locator('.modal').getByRole('button', { name: 'Delete' }).click();
        await row.waitFor({ state: 'detached', timeout: 20000 });
        assert.ok(existsSync(path.join(dir, 'sessions', '.archived', emptyIdForMenu)), 'the transcript stays in the sessions folder');
      }
      await page.locator('.sw-side-tab', { hasText: zh ? '子智能体' : 'Subagents' }).click();
      await page.locator('.sa-side-list .sa-row').first().waitFor();
      assert.equal(await page.locator('.sa-side-list .sa-row[data-activity-kind="background"]').count(), 0, 'no commands under Sub-agents');
      assert.equal(await page.locator('.sa-side-list .sa-row[data-activity-kind="subagent"]').count(), 2);
      // Open sub-agent A in a tab: its own messages and tools.
      await page.locator('.sa-side-list .sa-row', { hasText: 'Survey session code' }).click();
      await page.locator('[data-session-tab="toolu_A"].on').waitFor();
      await page.locator('.sw-turn-assistant', { hasText: 'session-model.js holds the helpers' }).first().waitFor({ timeout: 20000 })
        .catch(async (err) => { await page.screenshot({ path: path.join(shots, `fail-agent-tab-${lang}.png`) }); throw err; });
      assert.equal(await page.locator('.sw-composer').isVisible(), false, 'a sub-agent tab is read-only');
      await page.screenshot({ path: path.join(shots, `page-agent-tab-${lang}.png`) });
      await page.locator('[data-session-tab="main"]').click();
      await page.locator('.sw-composer').waitFor({ state: 'visible' });
      await page.locator('.sw-side-tab', { hasText: zh ? '后台任务' : 'Background tasks' }).click();
      await until(async () => (await page.locator('.sa-side-list .sa-row[data-activity-kind="background"]').count()) === 2);
      await page.screenshot({ path: path.join(shots, `page-background-${lang}.png`) });
      // The session menu replaces the header buttons.
      await page.locator('.sw-main [data-session-menu]').click();
      await page.locator('.graph-menu-item', { hasText: zh ? '复制回复' : 'Copy replies' }).waitFor();
      await page.keyboard.press('Escape');
      for (const width of [980, 1280]) {
        await page.setViewportSize({ width, height: 900 });
        await page.waitForTimeout(250);
        for (const delta of await noSideScroll(page, '.sw-thread')) assert.ok(delta <= 1, `page ${width}px scrolls sideways by ${delta}`);
      }
      await page.setViewportSize({ width: 1440, height: 900 });

      if (!zh) {
        // Enter queues while the turn runs; "Send now" stops the turn and starts the next with it.
        const composer = page.locator('.sw-composer-input');
        assert.equal(await page.locator('.sw-main [data-composer-hint]').count(), 0, 'no hint row');
        assert.match(await page.locator('.sw-main .sw-send').getAttribute('title'), /Ctrl\+Enter to send now/);
        await composer.fill('Queued question');
        await composer.press('Enter');
        const now = page.locator('.sw-main [data-steer-now]').first();
        await now.waitFor({ timeout: 20000 });
        assert.match(await now.textContent(), /interrupts/);
        await composer.fill('Second, stays queued');
        await composer.press('Enter');
        await until(async () => (await page.locator('.sw-main [data-steer-now]').count()) === 2);
        await now.click();
        const after = await until(async () => {
          const s = (await api(base, 'GET', `/api/agent-sessions/${streamed.id}`)).session;
          return s.turn >= 2 && ['starting', 'running', 'idle'].includes(s.status) ? s : null;
        }, 40000);
        assert.deepEqual(after.steer_queue.filter((item) => item.status === 'queued').map((item) => item.text), ['Second, stays queued']);
        // The delivered message left the card; the waiting one is still there.
        await until(async () => (await page.locator('.sw-main [data-steer-now]').count()) === 1);
        // Ctrl+Enter sends at once (interrupting the new turn, once it runs).
        await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}`)).session.status === 'running', 20000);
        await page.locator('.sw-main .sw-status.running').first().waitFor({ timeout: 20000 });
        await page.waitForTimeout(1000);
        await page.locator('.sw-composer-input').waitFor();
        await page.locator('.sw-composer-input').fill('Right now');
        await page.locator('.sw-composer-input').press('Control+Enter');
        await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}`)).session.turn >= 3, 40000);
        const transcript = await api(base, 'GET', `/api/agent-sessions/${streamed.id}/transcript`);
        assert.ok(transcript.items.some((item) => item.role === 'user' && item.text === 'Right now'));
      }

      // A narrow window keeps the classic layout: the page's own list, no sidebar view.
      await page.setViewportSize({ width: 980, height: 900 });
      await page.goto(`${base}/?lang=${lang}&session=${streamed.id}#/execute/sessions`);
      await page.locator('.sw-workspace:not(.sw-docked) .sw-rail .sw-session').first().waitFor({ timeout: 20000 });
      if (!zh) await page.screenshot({ path: path.join(shots, 'runs-layout-980.png') });
      await page.setViewportSize({ width: 800, height: 900 });
      await page.goto(`${base}/?lang=${lang}#/map`);
      await page.waitForTimeout(800);
      assert.equal(await page.locator('[data-session-sidebar]').isVisible(), false);
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    await stopServer(base, server, ids);
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* a stopped agent may hold its folder briefly */ }
  }
});

test('sessions page and sidebar stay responsive with a 6k-event stream', { skip: !chromium || !SERVER, timeout: 420000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-ssb-perf-'));
  const EVENTS = 6000;
  const { base, server } = await startServer(dir, { FAKE_STREAM_EVENTS: String(EVENTS), FAKE_STREAM_TAIL: '200' });
  const ids = [];
  let browser;
  try {
    await api(base, 'POST', '/api/projects', { name: 'Perf' });
    const big = (await api(base, 'POST', '/api/agent-sessions', { platform: 'custom', title: 'Big stream', prompt: 'go' })).session;
    const small = (await api(base, 'POST', '/api/agent-sessions', { platform: 'echo', title: 'Small', prompt: 'hello' })).session;
    ids.push(big.id, small.id);
    await until(async () => ((await api(base, 'GET', `/api/agent-sessions/${big.id}`)).session.counts?.stdout || 0) >= EVENTS, 120000);
    const page200 = await fetch(`${base}/api/agent-sessions/${big.id}/transcript?tail=200`, { headers: { Origin: base } }).then((r) => r.text());
    assert.ok(page200.length < 2e6, `a transcript page is small (${page200.length} bytes)`);
    const sig = JSON.parse(page200).signature;
    const unchanged = await api(base, 'GET', `/api/agent-sessions/${big.id}/transcript?tail=200&known=${encodeURIComponent(sig)}`);
    assert.ok(unchanged.unchanged || unchanged.signature !== sig, 'a known signature gets a short reply');
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const { context, page, errors } = await newPage(browser, 'en');
    await page.goto(`${base}/?lang=en&session=${small.id}#/execute/sessions`);
    await page.locator('.sw-session').first().waitFor({ timeout: 60000 });
    await page.waitForTimeout(2500);
    await page.evaluate(() => { window.__long = []; });
    const t0 = Date.now();
    await page.locator('.sw-session', { hasText: 'Big stream' }).first().click();
    await page.locator('.sw-turn-assistant').first().waitFor({ timeout: 60000 });
    const first = Date.now() - t0;
    await page.waitForTimeout(1500);
    const opening = await page.evaluate(() => window.__long.slice());
    await page.evaluate(() => { window.__long = []; });
    await page.waitForTimeout(10000);
    const streaming = await page.evaluate(() => window.__long.slice());
    console.log(`page: first render ${first} ms, long tasks opening ${JSON.stringify(opening)}, streaming ${JSON.stringify(streaming)}`);
    assert.ok(first < 1500, `first render ${first} ms`);
    assert.ok(Math.max(0, ...opening, ...streaming) <= LONG_TASK_BUDGET, `long tasks: ${opening} / ${streaming}`);
    assert.ok(await page.locator('.sw-column > *').count() < 200, 'a window of blocks is drawn, not the whole history');
    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    if (browser) await browser.close();
    await stopServer(base, server, ids);
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* see above */ }
  }
});

test('runs page: chat only, tabs for sub-agents and forks, standard properties', { skip: !chromium || !SERVER, timeout: 420000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-ssb-r6-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const { base, server } = await startServer(dir, { ARK_TASK_CUSTOM_LABEL: 'Recorded Claude stream', ARK_TASK_CUSTOM_FOLLOWUP: '0' });
  const ids = [];
  let browser;
  try {
    await api(base, 'POST', '/api/projects', { name: 'Runs test' });
    const unit = (await api(base, 'POST', '/api/units', { type: 'task', title: 'Inspector compare' })).unit;
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    // Empty state: one line and a New session button in the chat area, no page header.
    {
      const { context, page, errors } = await newPage(browser, 'en', { width: 1280, height: 900 });
      await page.goto(`${base}/?lang=en#/execute/sessions`);
      await dismissTour(page);
      await page.locator('.sw-empty-line [data-new-session]').waitFor({ timeout: 30000 });
      assert.equal(await page.locator('.session-chat-page .page-head').count(), 0, 'no page header');
      await page.screenshot({ path: path.join(shots, 'r6-runs-empty-en-1280.png') });
      assert.deepEqual(errors, []);
      await context.close();
    }
    const streamed = (await api(base, 'POST', '/api/agent-sessions', { platform: 'custom', title: 'Recorded stream', prompt: 'go' })).session;
    ids.push(streamed.id);
    await until(async () => (await api(base, 'GET', `/api/agent-sessions/${streamed.id}/transcript`)).items.some((i) => i.id === 'toolu_H'));
    const echo = (await api(base, 'POST', '/api/agent-sessions', { platform: 'echo', title: 'Echo parent', prompt: 'hello' })).session;
    ids.push(echo.id);
    for (const lang of ['en', 'zh']) {
      const zh = lang === 'zh';
      const { context, page, errors } = await newPage(browser, lang, { width: 1600, height: 960 });
      await page.goto(`${base}/?lang=${lang}&session=${streamed.id}#/execute/sessions`);
      await dismissTour(page);
      await page.locator('.sw-main h2').waitFor({ timeout: 30000 });
      assert.equal(await page.locator('[data-composer-hint]').count(), 0, 'no shortcut row in the composer');
      // A sub-agent from Session properties > Sub-agents opens as a tab.
      const props = page.locator('[data-view="sessionprops"]');
      await props.locator('.sw-side-tab', { hasText: zh ? '子智能体' : 'Subagents' }).click();
      await props.locator('.sa-row', { hasText: 'Survey session code' }).click();
      await page.locator('[data-session-tab="toolu_A"].on').waitFor();
      for (const width of [1280, 1600]) {
        await page.setViewportSize({ width, height: 960 });
        await page.waitForTimeout(300);
        await page.screenshot({ path: path.join(shots, `r6-runs-tabs-${lang}-${width}.png`) });
      }
      await page.locator('[data-session-tab="main"]').click();
      await props.locator('.sw-side-tab').first().click();
      // Sidebar widths 320 / 420.
      for (const width of [320, 420]) {
        await page.evaluate((w) => document.documentElement.style.setProperty('--wb-left-w', `${w}px`), width);
        await page.waitForTimeout(300);
        await page.screenshot({ path: path.join(shots, `r6-sidebar-${lang}-${width}.png`) });
      }
      await page.evaluate(() => window.dispatchEvent(new Event('resize')));
      // The section headers have the same height and font.
      const heads = await page.evaluate(() => [...document.querySelectorAll('.ssb-list .sw-block-head, .ssb-list .ctx-panel.compact > .panel-head')]
        .filter((el) => el.offsetParent).map((el) => { const cs = getComputedStyle(el); return [Math.round(el.getBoundingClientRect().height), cs.fontSize, cs.fontWeight]; }));
      assert.ok(heads.length >= 3, `three section headers (${heads.length})`);
      assert.ok(heads.every((x) => JSON.stringify(x) === JSON.stringify(heads[0])), `headers differ: ${JSON.stringify(heads)}`);
      // Search, segments and selects share one height.
      const heights = await page.evaluate(() => [...document.querySelectorAll('.ssb-list .sw-rail-tools input[type=search], .ssb-list .sw-rail-tools select, .ssb-list .sw-filterbar, .ssb-list .ctx-tools .sw-search, .ssb-list .ctx-tools .sw-filterbar')]
        .filter((el) => el.offsetParent).map((el) => Math.round(el.getBoundingClientRect().height)));
      assert.ok(heights.length >= 3 && heights.every((x) => x === heights[0]), `control heights ${heights}`);
      // Session properties: tabs and values at the inspector's body size.
      await page.setViewportSize({ width: 1600, height: 960 });
      const propSizes = await props.evaluate((el) => ({
        tab: getComputedStyle(el.querySelector('.sw-side-tab')).fontSize,
        value: getComputedStyle(el.querySelector('.sw-fact .v') || el).fontSize,
      }));
      await page.screenshot({ path: path.join(shots, `r6-props-${lang}.png`) });
      if (!zh) {
        // Fork in new tab (Echo cannot copy its history: a seeded fork).
        await page.goto(`${base}/?lang=${lang}&session=${echo.id}#/execute/sessions`);
        await page.locator('.sw-main h2', { hasText: 'Echo parent' }).waitFor({ timeout: 20000 });
        await page.locator('.sw-main [data-session-menu]').click();
        await page.locator('.graph-menu-item', { hasText: 'Fork in new tab' }).click();
        const forkTab = page.locator('[data-tab-kind="session"].on');
        await forkTab.waitFor({ timeout: 30000 });
        const forkId = await forkTab.getAttribute('data-session-tab');
        ids.push(forkId);
        const fork = (await api(base, 'GET', `/api/agent-sessions/${forkId}`)).session;
        assert.equal(fork.forked_from, echo.id);
        assert.equal(fork.fork_method, 'seeded');
        assert.equal(await page.locator('.sw-composer').isVisible(), true, 'a fork tab has its own composer');
        await page.screenshot({ path: path.join(shots, 'r6-fork-tab-en.png') });
        await page.locator('[data-session-tab="main"]').click();
        await page.locator('.sw-main h2', { hasText: 'Echo parent' }).waitFor();
        assert.equal(await page.locator(`[data-session-tab="${forkId}"]`).count(), 1, 'the fork tab stays in the strip');
        // Node inspector, same viewport, for comparison.
        await page.goto(`${base}/?lang=${lang}&unit=${unit.uid}#/map`);
        await page.locator('.drawer-body').first().waitFor({ timeout: 20000 });
        await page.screenshot({ path: path.join(shots, 'r6-node-inspector-en.png') });
        const nodeBody = await page.locator('.drawer-body').first().evaluate((el) => getComputedStyle(el).fontSize);
        assert.equal(propSizes.value, nodeBody, `values ${propSizes.value} vs inspector ${nodeBody}`);
        assert.equal(propSizes.tab, nodeBody, 'tab labels at body size');
      }
      assert.deepEqual(errors, [], lang);
      await context.close();
    }
  } finally {
    if (browser) await browser.close();
    await stopServer(base, server, ids);
    try { rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 500 }); } catch { /* see above */ }
  }
});
