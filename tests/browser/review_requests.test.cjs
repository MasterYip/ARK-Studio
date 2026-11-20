// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node tests/browser/review_requests.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// Review requests in Needs you, English and Chinese: one card for the units
// of one delivery (a member table), a unit's second round with its note,
// previous round and "changes since the last review" diff, an anchor in the
// note that opens the unit scrolled to the heading, and a review group that
// is accepted as a whole and disappears.
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

async function seed(base, lang) {
  const mk = async (data) => (await api(base, 'POST', '/api/units', data)).unit.uid;
  const task = await mk({ type: 'task', title: `Sandbox delivery ${lang}`, status: 'active' });
  const head = `D1（agent/review-test-${lang} @ abc1234）。独立评审：PASS。\n\n# Results\n`;
  const members = [];
  for (const [title, value] of [['Latency 4.7 ms per step', 'a'], ['Memory 0.2 MB per world', 'b'], ['Throughput ~41% idle', 'c']]) {
    members.push(await mk({ type: 'evidence', title: `${title} ${lang}`, status: 'review', body: head + value,
      links: [{ target: task, relation: 'part_of' }] }));
  }
  const unit = await mk({ type: 'experiment', title: `Ablation ${lang}`, body: '# Setup\nseed 1\n\n# Results\nacc 0.5' });
  await api(base, 'PATCH', `/api/units/${unit}`, { status: 'review', review_note: 'Check [Results](#results)' });
  await api(base, 'POST', `/api/units/${unit}/comments`, { body: 'Add the baseline', kind: 'sent_back' });
  await api(base, 'PATCH', `/api/units/${unit}`, { status: 'active' });
  await api(base, 'PATCH', `/api/units/${unit}`, { body: '# Setup\nseed 1\n\n# Results\nacc 0.5\n\n## Baseline\nacc 0.4 with the old model' });
  const r = await api(base, 'PATCH', `/api/units/${unit}`, { status: 'review', review_note: 'See [the baseline](#baseline)' });
  assert.deepEqual(r.review_warnings, []);
  const g1 = await mk({ type: 'artifact', title: `Figure A ${lang}`, body: '# Plot\nA' });
  const g2 = await mk({ type: 'artifact', title: `Figure B ${lang}`, body: '# Plot\nB' });
  const group = (await api(base, 'POST', '/api/reviews', {
    title: `Figures ${lang}`, summary: 'Two figures', review_note: `Start with [plot A](${g1}#plot)`, members: [g1, g2],
  })).review;
  return { members, unit, group };
}

test('review requests: delivery card, rounds, diff, anchors, review group', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-review-'));
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
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) {
        if (i > 100) throw new Error(`server did not start: ${err.message}\n${log}`);
        await new Promise((r) => setTimeout(r, 200));
      }
    }
    await api(base, 'POST', '/api/projects', { name: 'Review test' });
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['en', 'zh']) {
      const { members, unit, group } = await seed(base, lang);
      const context = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
        localStorage.setItem('ark.needsExpanded:' + 'x', '1');
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}#/research/review`);
      const list = page.locator('.review-list');
      await list.waitFor({ timeout: 30000 });
      // One card for the three units of the delivery, with a member table.
      const delivery = list.locator(`.needs-group[data-group="group:${[...members].sort().join(',')}"]`);
      await delivery.waitFor();
      assert.equal(await delivery.locator('.needs-member').count(), 3, `${lang}: members`);
      for (const uid of members) assert.equal(await delivery.locator(`.needs-member[data-unit-ref="${uid}"]`).count(), 1);
      assert.match(await delivery.locator('.needs-member').first().locator('.needs-member-verdict').textContent(), /PASS/);
      assert.match(await delivery.locator('.needs-group-why').textContent(), lang === 'zh' ? /同一提交/ : /same commit/);
      // The unit in its second round: note, previous round, diff toggle.
      const row = list.locator(`.needs-row[data-unit-ref="${unit}"]`);
      assert.equal((await row.locator('.needs-round').textContent()).trim(), lang === 'zh' ? '第 2 轮' : 'Round 2');
      assert.equal((await row.locator('.needs-note-label').textContent()).trim(), lang === 'zh' ? '本次请看' : 'Review this');
      assert.match(await row.locator('.needs-prev').textContent(), /Add the baseline/);
      await row.locator('.needs-changes').click();
      await row.locator('.review-diff-add').first().waitFor();
      assert.match(await row.locator('.review-diff-add').first().textContent(), /Baseline/);
      // The review group is one card.
      const groupCard = list.locator(`.review-group[data-review-group="${group.id}"]`);
      assert.equal(await groupCard.locator('.needs-member').count(), 2);
      await page.screenshot({ path: path.join(shots, `review-requests-${lang}.png`) });
      // The anchor chip opens the unit scrolled to the heading, highlighted.
      await row.locator('.review-anchor[data-anchor="#baseline"]').click();
      await page.locator('.drawer [data-anchor="baseline"].anchor-flash').waitFor({ timeout: 10000 });
      await page.locator('.drawer .review-panel .needs-note').waitFor();
      await page.screenshot({ path: path.join(shots, `review-anchor-${lang}.png`) });
      // Accept / Send back under the comment box; the typed text becomes the reason.
      const box = page.locator('.drawer .comments-input').first();
      await box.fill('Needs one more seed');
      await page.locator('.drawer [data-review-action="send-back"]').first().click();
      const reason = page.locator('.modal textarea');
      await reason.waitFor();
      assert.equal(await reason.inputValue(), 'Needs one more seed');
      await page.locator('.modal .modal-foot .btn.ghost').first().click();
      // One row under the box: Accept, Send back, Add comment; wraps left-aligned when narrow.
      const actions = page.locator('.drawer .comments-actions').first();
      const order = await actions.locator('button').evaluateAll((els) => els.map((el) => el.dataset.reviewAction || el.dataset.commentAction));
      assert.deepEqual(order, ['accept', 'send-back', 'add']);
      for (const width of [420, 320]) {
        await page.evaluate((w) => { for (const d of document.querySelectorAll('.drawer')) { d.style.width = `${w}px`; d.style.maxWidth = `${w}px`; } }, width);
        await page.waitForTimeout(150);
        const boxes = await actions.locator('button').evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => ({ x: Math.round(r.x), y: Math.round(r.y) })));
        const left = Math.round(await actions.evaluate((el) => el.getBoundingClientRect().x));
        assert.equal(boxes[0].x, left, `${width}: first button at the left edge`);
        if (width === 420) assert.ok(boxes.every((b) => b.y === boxes[0].y), `420: one row ${JSON.stringify(boxes)}`);
        await actions.locator('xpath=ancestor::section[1]').screenshot({ path: path.join(shots, `comment-row-${width}-${lang}.png`) });
      }
      await page.keyboard.press('Escape');
      // Accept the group: it is gone afterwards.
      await groupCard.locator('.needs-actions .btn.primary').click();
      await page.locator('.modal .modal-foot .btn.primary').click();
      await groupCard.waitFor({ state: 'detached', timeout: 10000 });
      assert.deepEqual(errors, [], `${lang}: page errors`);
      await context.close();
    }
  } finally {
    await browser?.close();
    server.kill();
    if (!process.env.ARK_TEST_SHOTS) {
      try { rmSync(dir, { recursive: true, force: true }); } catch { /* still in use */ }
    }
  }
});
