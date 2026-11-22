// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/project_state.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder> keeps the screenshots;
//   ARK_TEST_WEB=<web folder> runs it against another web build, e.g. to time the old switch)
// Headless only.  Switching projects keeps each project's page:
//   - project A on the Map with a zoomed camera and a unit open in the
//     inspector; switch to B (first visit: Home); back to A: the same route,
//     camera, inspector unit and selected node;
//   - A on Execute › Runs with its second session selected; B; back: the
//     same session is selected;
//   - the project picker shows "1 new" on A for a unit that went to review
//     while the person was in B, and the selector button a dot; the badge
//     stays while A shows the Map and clears once A's Needs you is opened.
// Prints the switch timings (click to the target page painted).
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync, mkdirSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
try { ({ chromium } = require('playwright')); } catch {
  try { ({ chromium } = require('playwright-core')); } catch { /* Optional browser dependency. */ }
}

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const WEB = process.env.ARK_TEST_WEB ? path.resolve(process.env.ARK_TEST_WEB) : path.resolve(__dirname, '../../ark_studio/web');
const FAKE = path.join(__dirname, 'fixtures', 'fake-claude-stream.cjs');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => resolve(port)); });
  });
}

async function api(base, method, route, body, project = '') {
  const headers = { Origin: base };
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

// Click a project in the selector's menu; resolves with ms from the click
// until the target page painted (no switch in progress, no route spinner,
// the page's view in place).
async function switchTo(page, id, painted) {
  await page.locator('.project-picker').click();
  const card = page.locator(`.project-card[data-project-id="${id}"]`).first();
  await card.waitFor({ timeout: 8000 });
  return page.evaluate(async ({ id, painted }) => {
    const card = document.querySelector(`.project-card[data-project-id="${id}"]`);
    const t0 = performance.now();
    card.click();
    for (;;) {
      await new Promise((r) => requestAnimationFrame(r));
      const S = window.__ARK__?.S;
      if (S?.project?.id === id && !document.body.classList.contains('project-switching')
          && !document.querySelector('#app .loading') && document.querySelector(painted)) return Math.round(performance.now() - t0);
      if (performance.now() - t0 > 20000) return -1;
    }
  }, { id, painted });
}

test('switching projects keeps each page; picker badges show new reviews', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-pstate-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  mkdirSync(shots, { recursive: true });
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', WEB],
  { stdio: 'ignore', env: { ...process.env, ARK_TASK_CUSTOM_COMMAND: `"${process.execPath}" "${FAKE}"`, ARK_TASK_CUSTOM_LABEL: 'Recorded' } });
  let browser;
  const sessions = [];
  try {
    await until(() => fetch(base + '/api/health').then((r) => r.ok), 30000, 'server');
    // A local server writes to its current project: fill A before creating B.
    const A = (await api(base, 'POST', '/api/projects', { name: 'Alpha' })).summary.id;
    const units = [];
    for (const title of ['Root idea', 'Claim one', 'Claim two', 'Method', 'Experiment']) {
      units.push((await api(base, 'POST', '/api/units', { type: 'claim', title }, A)).unit);
    }
    for (const u of units.slice(1)) await api(base, 'POST', '/api/links', { source: u.uid, target: units[0].uid, relation: 'part_of' }, A);
    for (const title of ['First run', 'Second run']) {
      const s = (await api(base, 'POST', '/api/agent-sessions', { platform: 'custom', title, prompt: 'go' }, A)).session;
      sessions.push(s.id);
    }
    const B = (await api(base, 'POST', '/api/projects', { name: 'Beta' })).summary.id;
    await api(base, 'POST', '/api/units', { type: 'note', title: 'Beta note' }, B);
    await api(base, 'POST', '/api/projects/open', { id: A });

    browser = await chromium.launch({ headless: true, executablePath: process.env.ARK_TEST_BROWSER || undefined });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.addInitScript(() => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.lang', 'en');
      localStorage.setItem('ark.welcome.v1:local', '1');
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(e.message));
    await page.goto(base + '/#/home');
    await until(() => page.evaluate((id) => window.__ARK__?.S?.project?.id === id && !document.getElementById('ark-startup'), A), 30000, 'boot');
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 3000 }).catch(() => {});
    const timings = {};

    // ---- 1. Map: zoomed camera and a unit open in the inspector
    await page.evaluate(() => { location.hash = '#/map'; });
    await until(() => page.evaluate(() => window.__ARK__.graphs.some((g) => g._fitted && g.nodes.length >= 5)), 20000, 'map graph');
    await sleep(800);
    const before = await page.evaluate(async (uid) => {
      const g = window.__ARK__.graphs.find((x) => x._fitted);
      g.zoomBy(1.8);
      g.pan = { x: g.pan.x + 60, y: g.pan.y - 40 };
      g._draw();
      await window.__ARK__.ctx.openUnit(uid);
      await new Promise((r) => setTimeout(r, 300));
      return { zoom: g.zoom, pan: { ...g.pan }, layout: g.layout, hash: location.hash, unit: window.__ARK__.S.detail?.uid };
    }, units[2].uid);
    assert.equal(before.unit, units[2].uid);
    await page.screenshot({ path: path.join(shots, 'state-1-alpha-map.png') });

    timings.firstVisitB = await switchTo(page, B, '.workspace-view-host > *');
    assert.equal(await page.evaluate(() => location.hash), '#/overview', 'a first visit lands on Home');
    assert.equal(await page.evaluate(() => window.__ARK__.S.drawerOpen), false);

    timings.backToMapA = await switchTo(page, A, '.graph-host canvas');
    await until(() => page.evaluate((uid) => window.__ARK__.S.detail?.uid === uid, units[2].uid), 10000, 'inspector restored');
    const after = await page.evaluate(() => {
      const g = window.__ARK__.graphs.find((x) => x._fitted);
      return { zoom: g.zoom, pan: { ...g.pan }, layout: g.layout, hash: location.hash, selected: g.selected, detail: window.__ARK__.S.detail?.id };
    });
    assert.equal(after.hash, before.hash);
    assert.equal(after.layout, before.layout);
    assert.ok(Math.abs(after.zoom - before.zoom) < 1e-6, `zoom ${after.zoom} vs ${before.zoom}`);
    assert.ok(Math.abs(after.pan.x - before.pan.x) < 0.5 && Math.abs(after.pan.y - before.pan.y) < 0.5, 'pan restored');
    assert.equal(after.selected, after.detail, 'the open unit is the selected node');
    await page.screenshot({ path: path.join(shots, 'state-2-alpha-map-restored.png') });

    // ---- 2. Runs: the second session selected
    await page.evaluate(() => window.__ARK__.ctx.closeUnit());
    await page.evaluate(() => { location.hash = '#/execute/sessions'; });
    await until(() => page.evaluate(() => document.querySelectorAll('[data-session-row]').length >= 2), 20000, 'runs list');
    await page.locator(`[data-session-row="${sessions[0]}"]`).first().click();
    await until(() => page.evaluate((id) => window.__ARK__.S.selectedSessionId === id, sessions[0]), 8000, 'session selected');
    await sleep(1800);   // the debounced save
    timings.secondVisitB = await switchTo(page, B, '.workspace-view-host > *');
    timings.backToRunsA = await switchTo(page, A, '[data-session-row]');
    assert.equal(await page.evaluate(() => location.hash), '#/execute/sessions');
    await until(() => page.evaluate((id) => window.__ARK__.S.selectedSessionId === id, sessions[0]), 8000, 'session restored');
    await page.screenshot({ path: path.join(shots, 'state-3-alpha-runs-restored.png') });

    // ---- 3. Badges: A's Needs you gets a new item while the person is in B
    await page.evaluate(() => { location.hash = '#/map'; });
    await until(() => page.evaluate(() => window.__ARK__.graphs.some((g) => g._fitted)), 20000, 'map again');
    await sleep(1800);
    await switchTo(page, B, '.workspace-view-host > *');
    await sleep(1500);   // B's Home marks B's (empty) list seen
    // (A local server has one current project: open A for the write, then B again.)
    await api(base, 'POST', '/api/projects/open', { id: A });
    await api(base, 'PATCH', `/api/units/${units[1].uid}`, { status: 'review' }, A);
    await api(base, 'POST', '/api/projects/open', { id: B });
    await page.locator('.project-picker').click();
    const pill = page.locator(`.project-card[data-project-id="${A}"] .project-card-new`);
    await pill.waitFor({ timeout: 8000 });
    assert.equal((await pill.textContent()).trim(), '1 new');
    await page.screenshot({ path: path.join(shots, 'state-4-picker-badge.png') });
    await page.keyboard.press('Escape');
    await until(() => page.evaluate(() => {
      const dot = document.querySelector('.project-picker .project-new-dot');
      return dot && !dot.hidden;
    }), 5000, 'selector dot');
    await page.locator('#topbar').screenshot({ path: path.join(shots, 'state-5-selector-dot.png') }).catch(() => {});
    // Into A: it opens on the Map (not Needs you), so the item stays new.
    await switchTo(page, A, '.graph-host canvas');
    await sleep(1800);
    const stillNew = await api(base, 'GET', '/api/projects');
    assert.equal(stillNew.projects.find((p) => p.id === A).review_new, 1, 'not seen while on the Map');
    // Opening Needs you (Research › Review) marks it seen.
    await page.evaluate(() => { location.hash = '#/research/review'; });
    await page.locator('.review-list .needs-row').first().waitFor({ timeout: 10000 });
    await until(async () => (await api(base, 'GET', '/api/projects')).projects.find((p) => p.id === A).review_new === 0, 10000, 'seen');
    await page.locator('.project-picker').click();
    await until(() => page.evaluate((id) => !document.querySelector(`.project-card[data-project-id="${id}"] .project-card-new`)
      && document.querySelector(`.project-card[data-project-id="${id}"] .project-card-total`), A), 8000, 'badge cleared');
    await page.screenshot({ path: path.join(shots, 'state-6-picker-cleared.png') });
    await page.keyboard.press('Escape');

    console.log(`TIMINGS ${JSON.stringify(timings)}`);
    for (const value of Object.values(timings)) assert.ok(value > 0, 'every switch painted');
    assert.deepEqual(errors, []);
  } finally {
    for (const id of sessions) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
    await browser?.close();
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
