// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/topic_lines_vs_topics.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// Lines and topics stay apart in the Topics view.  A hand-curated topic that
// carries a line's tag is associated with the line, but clicking the line
// opens the line view (every node with the line tag, sub-lines included) and
// highlights only the line row.  The line row's "专题" chip and the line
// head's "打开关联专题" open the topic; the topic row's "研究线" badge opens
// the line.
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
    await new Promise((r) => setTimeout(r, 100));
  }
};

test('topics view: a line associated with a curated topic opens as a line', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-lines-topics-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const shot = process.env.ARK_TEST_SHOT_PREFIX || 'after';
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: ['ignore', 'pipe', 'pipe'] });
  let log = '';
  server.stdout.on('data', (chunk) => { log += chunk; });
  server.stderr.on('data', (chunk) => { log += chunk; });
  let browser;
  try {
    await until(() => api(base, 'GET', '/api/health').then(() => true, () => false), 30000)
      .catch(() => { throw new Error(`server did not start\n${log}`); });
    await api(base, 'POST', '/api/projects', { name: 'Lines vs topics' });
    const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
    const picked = await mk({ type: 'idea', title: 'Picked idea', tags: ['branch:wm/literature'] });
    const plan = await mk({ type: 'task', title: 'Plan task', tags: ['branch:wm/plan'] });
    // On the line but in no topic (the report's branch:wm-hoi/gcrl nodes).
    const gcrl = await mk({ type: 'task', title: 'GCRL task', tags: ['branch:wm/gcrl'] });
    const gcrl2 = await mk({ type: 'experiment', title: 'GCRL run', tags: ['branch:wm/gcrl'] });
    const other = await mk({ type: 'idea', title: 'Other idea' });
    // A hand-curated topic (no rule) that carries the line tag itself.
    const curated = await mk({ type: 'topic', title: 'Curated world model', tags: ['branch:wm'], members: [picked, plan, other] });
    const plain = await mk({ type: 'topic', title: 'Plain topic', members: [other] });

    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.addInitScript(() => {
      localStorage.setItem('ark.prefsV2', '1');
      localStorage.setItem('ark.lang', 'zh');
      localStorage.setItem('ark.welcome.v1:local', '1');
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${base}/?lang=zh#/research/topic/${plain}`);
    await page.locator('.guidance-invite .btn.ghost').click({ timeout: 4000 }).catch(() => {});
    const explorer = page.locator('.wb-view[data-view="topics"] [data-topic-explorer]');
    await explorer.locator(`[data-outline-row="${other}"]`).waitFor({ timeout: 30000 });
    const selected = () => explorer.locator('[data-topic-row][aria-selected="true"], [data-line-row][aria-selected="true"]')
      .evaluateAll((rows) => rows.map((row) => row.dataset.topicRow || `line:${row.dataset.lineRow}`));
    const outlineHas = (uid) => explorer.locator(`[data-outline-row="${uid}"]`).count().then((n) => n > 0);

    // Clicking the line opens the line view: every node with the tag.
    await explorer.locator('[data-line-row="wm"]').click();
    await page.waitForTimeout(1500);
    await page.screenshot({ path: path.join(shots, `${shot}-line-click.png`) });
    await until(async () => decodeURIComponent(await page.evaluate(() => location.hash)).includes('branch:wm'), 10000);
    await explorer.locator(`[data-outline-row="${gcrl}"]`).waitFor({ timeout: 10000 });
    for (const uid of [picked, plan, gcrl, gcrl2]) assert.ok(await outlineHas(uid), `${uid} is on the line`);
    assert.equal(await outlineHas(other), false, 'a topic member without the tag is not on the line');
    assert.deepEqual(await selected(), ['line:wm'], 'only the line row is highlighted');

    // The topic row says which line it is associated with.
    const badge = explorer.locator(`[data-topic-row="${curated}"] [data-topic-line]`);
    assert.match(await badge.getAttribute('title'), /关联研究线：wm/);
    assert.equal(await explorer.locator(`[data-topic-row="${plain}"] [data-topic-line]`).count(), 0, 'no badge without a line');

    // The line row's chip opens the associated topic with its own members.
    const chip = explorer.locator('[data-line-row="wm"] [data-line-topic]');
    assert.match(await chip.getAttribute('title'), /关联专题：Curated world model/);
    await chip.click();
    await until(async () => (await page.evaluate(() => location.hash)).includes(curated), 10000);
    await explorer.locator(`[data-outline-row="${other}"]`).waitFor({ timeout: 10000 });
    assert.equal(await outlineHas(gcrl), false, 'the curated topic keeps its own members');
    assert.deepEqual(await selected(), [curated], 'only the topic row is highlighted');
    await page.screenshot({ path: path.join(shots, `${shot}-topic-chip.png`) });

    // The topic row's badge opens the line again; the line head links the topic.
    await badge.click();
    await explorer.locator(`[data-outline-row="${gcrl}"]`).waitFor({ timeout: 10000 });
    assert.deepEqual(await selected(), ['line:wm']);
    const headLink = page.locator('[data-line-open-topic]');
    assert.equal(await headLink.getAttribute('data-line-open-topic'), curated);
    await headLink.click();
    await until(async () => (await page.evaluate(() => location.hash)).includes(curated), 10000);
    assert.deepEqual(await selected(), [curated]);

    // A sub-line opens its own line view.
    await explorer.locator('[data-line-row="wm/gcrl"]').click();
    await until(async () => decodeURIComponent(await page.evaluate(() => location.hash)).includes('branch:wm/gcrl'), 10000);
    await until(async () => !(await outlineHas(picked)), 10000);
    assert.ok(await outlineHas(gcrl2));
    assert.deepEqual(await selected(), ['line:wm/gcrl']);
    assert.deepEqual(errors, []);
    await context.close();
  } finally {
    await browser?.close();
    server.kill();
    await new Promise((r) => setTimeout(r, 300));
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
});
