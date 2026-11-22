// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/locate_in_map.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// "Locate in research map" from a Needs you card and from a session's unit
// link: the Map tab opens with the node selected, on screen and open in the
// inspector; a node the filters hide is shown with a notice (the filters stay
// until "Clear filters"); a review group's entry is disabled.  Filter chips:
// Alt+click is "Only this", the chip menu has Only this / Select all / Invert.
// Phones: a long press on a card raises the same menu.
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
    await new Promise((r) => setTimeout(r, 150));
  }
};

// The Map graph's state: selected node, whether `uid` is drawn inside the canvas.
const mapState = (page, uid) => page.evaluate(async (uid) => {
  const { liveGraphs } = await import(performance.getEntriesByType('resource').map((e) => e.name).find((n) => /\/js\/graph\.js(\?|$)/.test(n)) || '/js/graph.js'); // the app's instance (?v=)
  const graph = liveGraphs().find((g) => g.opts?.mapGraph && g.host?.isConnected);
  if (!graph) return { graph: false };
  const node = graph.nodes.find((n) => n.uid === uid);
  const p = node && Number.isFinite(node.x) ? graph._screen(node) : null;
  const selected = graph.byId.get(graph.selected);
  return {
    graph: true, layout: graph.layout, selected: selected?.uid || null, revealed: node?.revealed || null,
    onScreen: Boolean(p && p.x > 0 && p.x < graph.width && p.y > 0 && p.y < graph.height),
    centred: Boolean(p && Math.abs(p.x - graph.width / 2) < 40 && Math.abs(p.y - graph.height / 2) < 40),
    tab: document.body.dataset.tab || '', hash: location.hash,
  };
}, uid);

async function expectLocated(page, uid, label) {
  const state = await until(async () => {
    const s = await mapState(page, uid);
    return s.graph && s.selected === uid && s.centred ? s : null;
  }, 20000).catch(async () => { throw new Error(`${label}: not located ${JSON.stringify(await mapState(page, uid))}`); });
  assert.equal(state.hash, '#/map', `${label}: Map tab`);
  assert.ok(state.onScreen, `${label}: on screen`);
  await page.locator('.drawer-head .uid', { hasText: uid }).first().waitFor({ timeout: 10000 });
  return state;
}

async function menuOn(page, locator, { touch = false } = {}) {
  if (touch) {
    // A long press: pointerdown (touch), held for 600 ms, no move.
    await locator.evaluate((el) => {
      const r = el.getBoundingClientRect();
      el.dispatchEvent(new PointerEvent('pointerdown', { pointerType: 'touch', bubbles: true, clientX: r.left + 20, clientY: r.top + 12 }));
    });
    await page.waitForTimeout(650);
    await locator.evaluate((el) => el.dispatchEvent(new PointerEvent('pointerup', { pointerType: 'touch', bubbles: true })));
  } else {
    await locator.click({ button: 'right', position: { x: 24, y: 12 } });
  }
  const menu = page.locator('.graph-menu').last();
  await menu.waitFor({ timeout: 5000 });
  return menu;
}

test('locate in research map: Needs you, session link, hidden by filter, review group, chips, phone', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-locate-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  let browser;
  const sessions = [];
  try {
    await until(() => api(base, 'GET', '/api/health').then(() => true, () => false), 30000)
      .catch(() => { throw new Error(`server did not start\n${log}`); });
    await api(base, 'POST', '/api/projects', { name: 'Locate test' });
    const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
    const story = await mk({ type: 'story', title: 'Locate story' });
    const others = [];
    for (let i = 0; i < 14; i += 1) others.push(await mk({ type: 'claim', title: `Background claim ${i}`, links: [{ target: story, relation: 'part_of' }] }));
    const claim = await mk({ type: 'claim', title: 'Claim to review', status: 'review', links: [{ target: others[3], relation: 'supports' }] });
    await api(base, 'PATCH', `/api/units/${claim}`, { review_note: 'Check the numbers' });
    const task = await mk({ type: 'task', title: 'Task hidden by the filter', links: [{ target: claim, relation: 'part_of' }] });
    const f1 = await mk({ type: 'artifact', title: 'Figure A' });
    const f2 = await mk({ type: 'artifact', title: 'Figure B' });
    const group = (await api(base, 'POST', '/api/reviews', { title: 'Figures', summary: 'Two figures', members: [f1, f2] })).review;
    const session = (await api(base, 'POST', '/api/agent-sessions', { platform: 'echo', title: 'Locate session', prompt: `Please check ${task} next`, task })).session;
    sessions.push(session.id);
    await until(async () => (await api(base, 'GET', `/api/agent-sessions/${session.id}/transcript`)).items.some((i) => JSON.stringify(i).includes(task)), 30000)
      .catch(async () => { throw new Error(`echo transcript: ${JSON.stringify(await api(base, 'GET', `/api/agent-sessions/${session.id}/transcript`)).slice(0, 1500)}`); });

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const zh = lang === 'zh';
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.welcome.v1:local', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}#/map`);
      await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
      await until(async () => (await mapState(page, story)).graph, 30000);

      // Filter chips: the menu, then Alt+click = Only this (Claim).
      const claimChip = page.locator('.rail [data-filter-group="types"][data-value="claim"]');
      await claimChip.waitFor({ state: 'attached', timeout: 20000 });
      // The Filters view sits in the left bar; open it when it is closed.
      if (!(await claimChip.isVisible())) await page.locator('[data-view-icon="filters"]').click();
      await claimChip.waitFor({ timeout: 10000 });
      const chipMenu = await menuOn(page, claimChip);
      const chipItems = await chipMenu.locator('[data-menu-id^="chip-"]').evaluateAll((els) => els.map((el) => el.dataset.menuId));
      assert.deepEqual(chipItems, ['chip-only', 'chip-all', 'chip-invert']);
      assert.match(await chipMenu.textContent(), zh ? /只选择此项.*选择全部.*反选/ : /Only this.*Select all.*Invert/);
      await page.screenshot({ path: path.join(shots, `chip-menu-${lang}.png`) });
      await page.keyboard.press('Escape');
      await claimChip.click({ modifiers: ['Alt'] });
      await until(async () => (await page.locator('.rail [data-filter-group="types"].on').evaluateAll((els) => els.map((el) => el.dataset.value))).join() === 'claim');
      await until(async () => !(await mapState(page, task)).onScreen && (await page.evaluate(async (uid) => {
        const { liveGraphs } = await import(performance.getEntriesByType('resource').map((e) => e.name).find((n) => /\/js\/graph\.js(\?|$)/.test(n)) || '/js/graph.js'); // the app's instance (?v=)
        return !liveGraphs().find((g) => g.opts?.mapGraph && g.host?.isConnected)?.nodes.some((n) => n.uid === uid);
      }, task)));

      // 1. From a Needs you card (Review page).
      await page.goto(`${base}/?lang=${lang}#/research/review`);
      const row = page.locator(`.review-list .needs-row[data-unit-ref="${claim}"]`).first();
      await row.waitFor({ timeout: 30000 });
      let menu = await menuOn(page, row);
      const item = menu.locator('[data-menu-id="locate-map"]');
      assert.equal(await item.isDisabled(), false);
      assert.match(await item.textContent(), zh ? /在研究地图中定位.*Alt\+M/ : /Locate in research map.*Alt\+M/);
      await page.screenshot({ path: path.join(shots, `needs-menu-${lang}.png`) });
      await item.click();
      await expectLocated(page, claim, `${lang} needs you`);
      assert.equal(await page.locator('.map-reveal-notice').count(), 0, 'a claim matches the filter: no notice');
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(shots, `located-needs-${lang}.png`) });

      // A review group is not a unit: the entry is disabled, with the reason.
      await page.goto(`${base}/?lang=${lang}#/research/review`);
      const groupCard = page.locator(`.review-group[data-review-group="${group.id}"]`).first();
      await groupCard.waitFor({ timeout: 30000 });
      menu = await menuOn(page, groupCard.locator('.needs-row-head, .needs-title, h3').first().or(groupCard).first());
      const off = menu.locator('[data-menu-id="locate-map"]');
      assert.equal(await off.isDisabled(), true, 'review group: disabled');
      assert.equal(await off.getAttribute('title'), zh ? '评审组不是节点' : 'A review group is not a node');
      await page.screenshot({ path: path.join(shots, `group-disabled-${lang}.png`) });
      await page.keyboard.press('Escape');

      // 2. From a session's unit link (a task the Claim-only filter hides).
      await page.goto(`${base}/?lang=${lang}&session=${session.id}#/execute/sessions`);
      // The session's task chip (T-0042) in its header.
      const link = page.locator(`.sw-task-ref[data-unit-ref="${task}"]`).first();
      await link.waitFor({ timeout: 30000 });
      menu = await menuOn(page, link);
      await menu.locator('[data-menu-id="locate-map"]').click();
      const state = await expectLocated(page, task, `${lang} session link`);
      assert.equal(state.revealed, 'filter');
      const notice = page.locator('.graph-panel[data-map-graph] .map-reveal-notice');
      await notice.waitFor({ timeout: 10000 });
      assert.match(await notice.textContent(), zh ? /已临时显示：被当前筛选隐藏/ : /Shown for now: hidden by the current filters/);
      assert.equal(await page.locator('.rail [data-filter-group="types"].on').count(), 1, 'the filters stay as they were');
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(shots, `located-hidden-${lang}.png`) });
      await notice.locator('[data-reveal-clear]').click();
      await notice.waitFor({ state: 'detached', timeout: 10000 });
      assert.ok((await page.locator('.rail [data-filter-group="types"].on').count()) > 1, 'Clear filters: every type again');
      assert.equal((await mapState(page, task)).revealed, null, 'now shown by the filters');

      // Alt+M with the node open in the inspector, from another tab.
      await page.goto(`${base}/?lang=${lang}&unit=${others[5]}#/research/review`);
      await page.locator('.drawer-head .uid', { hasText: others[5] }).first().waitFor({ timeout: 20000 });
      await page.locator('body').click({ position: { x: 5, y: 300 } }).catch(() => {});
      await page.keyboard.press('Alt+M');
      await expectLocated(page, others[5], `${lang} Alt+M`);
      assert.deepEqual(errors, [], `${lang}: page errors`);
      await context.close();
    }

    // Phone: long press on a Needs you card, locate, the Map shows the node.
    const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
    await phone.addInitScript(() => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.lang', 'zh');
      localStorage.setItem('ark.welcome.v1:local', '1');
    });
    const page = await phone.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${base}/?lang=zh#/research/review`);
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
    const row = page.locator(`.needs-row[data-unit-ref="${claim}"]`).first();
    await row.waitFor({ timeout: 30000 });
    const menu = await menuOn(page, row, { touch: true });
    await page.screenshot({ path: path.join(shots, 'phone-menu-zh.png') });
    await menu.locator('[data-menu-id="locate-map"]').click();
    const state = await until(async () => {
      const s = await mapState(page, claim);
      return s.graph && s.selected === claim && s.onScreen ? s : null;
    }, 20000).catch(async () => { throw new Error(`phone: not located ${JSON.stringify(await mapState(page, claim))}`); });
    assert.equal(state.hash, '#/map');
    assert.equal(await page.locator('.drawer-head .uid', { hasText: claim }).count(), 0, 'phone: the map stays in view (a tap opens the node)');
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 2000 }).catch(() => {});
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(shots, 'phone-located-zh.png') });
    assert.deepEqual(errors, [], 'phone: page errors');
    await phone.close();
  } finally {
    await browser?.close();
    for (const id of sessions) await api(base, 'POST', `/api/agent-sessions/${id}/stop`, {}).catch(() => {});
    server.kill();
    await new Promise((r) => (server.exitCode !== null ? r() : server.once('exit', r)));
    if (!process.env.ARK_TEST_SHOTS) {
      try { rmSync(dir, { recursive: true, force: true }); } catch { /* still in use */ }
    }
  }
});
