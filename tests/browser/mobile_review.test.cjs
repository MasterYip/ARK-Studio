// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=<ark-server> node tests/browser/mobile_review.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<dir> keeps the screenshots)
// Review requests on a phone (390 x 844, touch), English and Chinese: Home's
// Needs you shows the same cards as a desktop (round, "Review this" note with
// anchor chips, previous round, changes toggle, one card per delivery with
// the member table from the server's auto_group, the review group); a tap on
// a member row opens that unit, a tap on an anchor opens the unit at the
// heading, and the unit sheet has Accept / Send back / Add comment on one row.
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
  const head = `D1（agent/mobile-review-${lang} @ abc1234）。独立评审：PASS。\n\n# Results\n`;
  const members = [];
  for (const [title, value] of [['Latency 4.7 ms per step', 'a'], ['Memory 0.2 MB per world', 'b'], ['Throughput ~41% idle', 'c']]) {
    members.push(await mk({ type: 'evidence', title: `${title} ${lang}`, status: 'review', body: head + value,
      links: [{ target: task, relation: 'part_of' }] }));
  }
  const unit = await mk({ type: 'experiment', title: `Ablation ${lang}`, body: '# Setup\nseed 1\n\n# Results\nacc 0.5' });
  await api(base, 'PATCH', `/api/units/${unit}`, { status: 'review', review_note: 'Check [Results](#results)' });
  await api(base, 'POST', `/api/units/${unit}/comments`, { body: 'Add the baseline', kind: 'sent_back' });
  await api(base, 'PATCH', `/api/units/${unit}`, { status: 'active' });
  const filler = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n\n');
  await api(base, 'PATCH', `/api/units/${unit}`, { body: `# Setup\nseed 1\n\n${filler}\n\n# Results\nacc 0.5\n\n## Baseline\nacc 0.4 with the old model` });
  await api(base, 'PATCH', `/api/units/${unit}`, { status: 'review', review_note: 'See [the baseline](#baseline) and [Results](#results)' });
  const g1 = await mk({ type: 'artifact', title: `Figure A ${lang}`, body: '# Plot\nA' });
  const g2 = await mk({ type: 'artifact', title: `Figure B ${lang}`, body: '# Plot\nB' });
  const group = (await api(base, 'POST', '/api/reviews', {
    title: `Figures ${lang}`, summary: 'Two figures', review_note: `Start with [plot A](${g1}#plot)`, members: [g1, g2],
  })).review;
  return { members, unit, group };
}

test('mobile review: Home cards, member rows, anchors, unit sheet actions', { skip: !chromium || !SERVER, timeout: 300000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-mobile-review-'));
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
    await api(base, 'POST', '/api/projects', { name: 'Mobile review' });
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const lang of ['zh', 'en']) {
      const { members, unit, group } = await seed(base, lang);
      // The server groups the delivery: every member carries the same card.
      const project = await api(base, 'GET', '/api/project');
      const cards = project.needs_you.filter((i) => members.includes(i.uid)).map((i) => i.auto_group?.key);
      assert.equal(new Set(cards).size, 1, `${lang}: one auto_group for the delivery`);
      assert.equal(cards[0], `group:${[...members].sort().join(',')}`);
      const context = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
      await context.addInitScript((lang) => {
        localStorage.setItem('ark.prefsV2', '1');
        localStorage.setItem('ark.lang', lang);
      }, lang);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`${base}/?lang=${lang}#/overview`);
      const panel = page.locator('.needs-panel');
      await panel.waitFor({ timeout: 30000 });
      // The first-run tour invitation covers the bottom of the screen.
      const later = page.locator('.guidance-invite .btn.ghost').first();
      await later.waitFor({ timeout: 5000 }).then(() => later.click()).catch(() => {});
      // The unit in its second round.
      const row = panel.locator(`.needs-row[data-unit-ref="${unit}"]`);
      await row.waitFor();
      assert.equal((await row.locator('.needs-round').textContent()).trim(), lang === 'zh' ? '第 2 轮' : 'Round 2');
      assert.equal((await row.locator('.needs-note-label').textContent()).trim(), lang === 'zh' ? '本次请看' : 'Review this');
      assert.match(await row.locator('.needs-prev').textContent(), /Add the baseline/);
      const chip = row.locator('.review-anchor[data-anchor="#baseline"]');
      const chipBox = await chip.boundingBox();
      assert.ok(chipBox.height >= 28, `${lang}: anchor chip is touch-sized (${chipBox.height})`);
      // The delivery card: the member table with numbers and the verdict.
      const delivery = panel.locator(`.needs-group[data-group="group:${[...members].sort().join(',')}"]`);
      await delivery.waitFor();
      assert.equal(await delivery.locator('.needs-member').count(), 3);
      const verdict = delivery.locator('.needs-member-verdict').first();
      assert.ok(await verdict.isVisible(), `${lang}: verdict shown on a phone`);
      assert.match(await verdict.textContent(), /PASS/);
      for (const m of await delivery.locator('.needs-member').all()) {
        const box = await m.boundingBox();
        assert.ok(box.height >= 44 && box.x + box.width <= 390, `${lang}: member row ${JSON.stringify(box)}`);
      }
      const width = await page.evaluate(() => document.documentElement.scrollWidth);
      assert.ok(width <= 390, `${lang}: no sideways scroll (${width})`);
      // Cards further down render as they scroll into view (Home perf).
      const groupCard = panel.locator(`.review-group[data-review-group="${group.id}"]`);
      const more = panel.locator('.needs-toggle[aria-expanded="false"]');
      if (await more.count()) await more.first().tap();
      for (let i = 0; i < 20 && !(await groupCard.count()); i += 1) {
        await page.mouse.wheel(0, 600);
        await page.waitForTimeout(150);
      }
      await groupCard.scrollIntoViewIfNeeded();
      assert.equal(await groupCard.locator('.needs-member').count(), 2);
      // The changes since the last review, opened.
      await row.locator('.needs-changes').tap();
      await row.locator('.review-diff-add').first().waitFor();
      await row.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `mobile-needs-${lang}.png`) });
      await delivery.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `mobile-delivery-${lang}.png`) });
      await page.screenshot({ path: path.join(shots, `mobile-home-full-${lang}.png`), fullPage: true });
      // Accept all: a checklist of the members.
      await delivery.locator('.needs-actions .btn.primary').tap();
      const dialog = page.locator('.modal');
      await dialog.waitFor();
      assert.equal(await dialog.locator('.needs-bulk-row input[type=checkbox]:checked').count(), 3);
      await page.screenshot({ path: path.join(shots, `mobile-accept-all-${lang}.png`) });
      await dialog.locator('.modal-foot .btn.ghost').first().tap();
      await dialog.waitFor({ state: 'detached' });
      // A tap on the row (not the title) opens that member.
      await delivery.locator('.needs-member').nth(1).locator('.needs-member-number').tap();
      const sheet = page.locator('.drawer.mobile-sheet');
      await sheet.waitFor({ timeout: 10000 });
      const second = await delivery.locator('.needs-member').nth(1).getAttribute('data-unit-ref');
      await page.waitForFunction((uid) => document.querySelector('.drawer.mobile-sheet')?.textContent.includes(uid), second);
      await page.keyboard.press('Escape');
      await sheet.waitFor({ state: 'detached', timeout: 10000 }).catch(() => {});
      // The anchor opens the unit at the heading; the sheet has the review note
      // and Accept / Send back / Add comment on one row.
      await chip.tap();
      await page.locator('.drawer [data-anchor="baseline"].anchor-flash').waitFor({ timeout: 10000 });
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(shots, `mobile-anchor-${lang}.png`) });
      const drawer = page.locator('.drawer.mobile-sheet');
      await drawer.locator('.review-panel .needs-note').waitFor();
      await drawer.locator('.review-panel').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(shots, `mobile-unit-note-${lang}.png`) });
      const actions = drawer.locator('.comments-actions').first();
      await actions.scrollIntoViewIfNeeded();
      const order = await actions.locator('button').evaluateAll((els) => els.map((el) => el.dataset.reviewAction || el.dataset.commentAction));
      assert.deepEqual(order, ['accept', 'send-back', 'add']);
      const boxes = await actions.locator('button').evaluateAll((els) => els.map((el) => el.getBoundingClientRect()).map((r) => Math.round(r.y)));
      assert.ok(boxes.every((y) => y === boxes[0]), `${lang}: one row ${boxes}`);
      await drawer.locator('.comments-input').first().fill('Needs one more seed');
      await page.screenshot({ path: path.join(shots, `mobile-unit-actions-${lang}.png`) });
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
