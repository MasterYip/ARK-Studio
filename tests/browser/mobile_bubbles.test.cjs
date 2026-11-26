// Optional browser regression (Playwright) against a real ark-server:
//   ARK_TEST_SERVER=ark_studio/server/ark-server.exe node --test tests/browser/mobile_bubbles.test.cjs
//   (ARK_TEST_BROWSER=<path to Chrome/Edge>; ARK_TEST_SHOTS=<folder>; headless only)
// A conversation that is hard to fit on a phone (390×844 and 360×740, zh and
// en): long paths, URLs, hashes and tokens, a code block with a long line, a
// wide table, an image, inline math, a long ARK UPDATE turn, tool commands
// with long output, a notice and an error.  In Execute › Runs and in the
// docked Sessions sidebar every bubble, notice and separator stays inside the
// screen, centred items stay centred, code, tables and tool output scroll
// inside their own box, and the page never scrolls sideways.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { mkdtempSync, rmSync } = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
let chromium;
let devices;
try { ({ chromium, devices } = require('playwright')); } catch { /* Optional browser dependency. */ }

const SERVER = process.env.ARK_TEST_SERVER ? path.resolve(process.env.ARK_TEST_SERVER) : '';
const FAKE = path.join(__dirname, 'fixtures', 'fake-claude-wide.cjs');
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const LONG_PATH = '/data/team/projects/prj_0a1b2c3d4e/runtime/sessions/sess_0123456789abcdef0123456789abcdef/artifacts/output/final_report_with_a_very_long_name_v2_FINAL_really_final.md';
const LONG_URL = 'https://ark.example.com/api/agent/v1/media/med_9f8e7d6c5b4a39281706f5e4d3c2b1a0/raw?download=1&name=very_long_attachment_name_without_any_spaces_at_all.tar.gz';
const HASH = `sha256:${'9f86d081884c7d659a2feaa0c55ad015'.repeat(4)}`;
const WIDE_MD = [
  `Wrote \`${LONG_PATH}\`. Download: ${LONG_URL}`, '', `Checksum ${HASH}`, '',
  '```bash', `python3 tools/train.py --config configs/very/long/path/experiment_config.yaml --output ${LONG_PATH}`, '```', '',
  '| run | dataset | checkpoint | reward | notes |', '|---|---|---|---|---|',
  `| r1 | hoi_grasp_benchmark_v3 | ${HASH.slice(0, 48)} | 0.8123 | baseline_without_any_augmentation |`, '',
  '![ARK mark](/brand/ark-mark.png)', '',
  String.raw`$$\sum_{i=1}^{100} x_i^2 + \frac{a_1+a_2+a_3+a_4+a_5+a_6+a_7+a_8+a_9+a_{10}+a_{11}+a_{12}}{b_1 b_2 b_3 b_4 b_5 b_6 b_7 b_8 b_9 b_{10} b_{11} b_{12}}$$`,
].join('\n');
const CHAT = [
  { role: 'user', content: `ARK UPDATE for ARK session sess_0123456789abcdef0123456789abcdef: see ${LONG_URL} token_${'A1b2C3d4E5f6G7h8'.repeat(10)}`, at: '2026-11-26T10:00:00Z' },
  { role: 'assistant', content: WIDE_MD, at: '2026-11-26T10:00:05Z', meta: { harness: 'Claude Code' },
    tools: [{ name: 'get_unit', ok: true, args: { uid: `U-${'0123456789'.repeat(8)}` }, target: LONG_PATH }] },
];

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

async function until(fn, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timed out');
    await sleep(150);
  }
}

// Every message piece in `root` that leaves the screen, plus centring and
// page scroll.  Boxes that scroll themselves (pre, tables, tool output) may
// hold wider content, but the box itself must fit.
function measure(root) {
  const host = document.querySelector(root);
  const sel = [
    '.sw-turn-user', '.sw-bubble', '.sw-turn-images', '.sw-turn-assistant', '.sw-prose', '.sw-prose .rich > *',
    '.sw-prose pre', '.sw-prose table', '.sw-prose img', '.sw-prose .katex-display', '.sw-prose :not(pre) > code',
    '.math-inline', '.math-block', '.sw-prose a', '.sw-prose li', '.sw-prose blockquote',
    '.chat-msg', '.chat-bubble', '.chat-user-text', '.chat-error', '.assistant-response', '.assistant-answer > *',
    '.assistant-response pre', '.assistant-response table', '.assistant-response img', '.assistant-tool-row',
    '.sw-note', '.ssb-note', '.sw-turn-sep', '.sw-turn-sep > span', '.ssb-sep', '.sw-err-card', '.sw-act', '.sw-act-head',
    '.sw-act-head > *', '.sw-act-task',
    '.sw-act-item', '.sw-act-cmd', '.sw-act-out', '.sw-msg-actions', '.ssb-msg',
  ].join(',');
  const vw = document.documentElement.clientWidth;
  const bad = [];
  const seen = {};
  for (const el of host ? host.querySelectorAll(sel) : []) {
    const r = el.getBoundingClientRect();
    if (!r.width && !r.height) continue;
    const name = `${el.tagName.toLowerCase()}.${[...el.classList].join('.')}`;
    seen[name] = (seen[name] || 0) + 1;
    if (r.right > vw + 1 || r.left < -1) bad.push(`${name} [${Math.round(r.left)}, ${Math.round(r.right)}] > ${vw}: ${(el.textContent || el.getAttribute('src') || '').slice(0, 60)}`);
  }
  // Centred items: equal room on both sides inside the thread.
  const off = [];
  for (const el of host ? host.querySelectorAll('.sw-turn-sep > span, .sw-note, .ssb-note') : []) {
    const r = el.getBoundingClientRect();
    const p = el.parentElement.getBoundingClientRect();
    if (!r.width) continue;
    const left = r.left - p.left;
    const right = p.right - r.right;
    if (Math.abs(left - right) > 2) off.push(`${el.className} off centre: ${Math.round(left)} vs ${Math.round(right)}`);
  }
  const thread = host?.querySelector('.sw-thread, .ssb-thread');
  return {
    bad, off, seen,
    page: document.scrollingElement.scrollWidth - innerWidth,
    thread: thread ? thread.scrollWidth - thread.clientWidth : 0,
  };
}

async function openTools(page, root) {
  await page.evaluate((r) => {
    for (const d of document.querySelectorAll(`${r} details.sw-act:not([open])`)) d.open = true;
  }, root);
  await sleep(400);
}

test('phone chat: bubbles stay on screen', { skip: !chromium || !SERVER, timeout: 420000 }, async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'ark-mbub-'));
  const shots = process.env.ARK_TEST_SHOTS || dir;
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const server = spawn(SERVER, ['serve', '--root', path.join(dir, 'projects'), '--host', '127.0.0.1', '--port', String(port),
    '--no-demo', '--sessions-root', path.join(dir, 'sessions'), '--web-root', path.resolve(__dirname, '../../ark_studio/web')],
  { stdio: 'ignore', env: { ...process.env, ARK_TASK_CUSTOM_COMMAND: `"${process.execPath}" "${FAKE}"`, ARK_TASK_CUSTOM_LABEL: 'Wide stream', ARK_TASK_CUSTOM_FOLLOWUP: '0' } });
  let browser;
  let session;
  const failures = [];
  try {
    for (let i = 0; ; i += 1) {
      try { await api(base, 'GET', '/api/health'); break; } catch (err) { if (i > 100) throw err; await sleep(200); }
    }
    await api(base, 'POST', '/api/projects', { name: 'Phone bubbles' });
    session = (await api(base, 'POST', '/api/agent-sessions', {
      platform: 'custom', title: 'Wide stream', prompt: `Prompt_${'0123456789abcdef'.repeat(10)}`,
    })).session;
    await until(async () => (await api(base, 'GET', `/api/agent-sessions/${session.id}/transcript`)).items.some((i) => i.type === 'message' && /Done\. See/.test(i.text || '')), 30000);
    browser = await chromium.launch({ headless: true,
      ...(process.env.ARK_TEST_BROWSER ? { executablePath: process.env.ARK_TEST_BROWSER } : {}) });
    for (const [w, hgt, scale] of [[390, 844, 1], [360, 740, 1], [360, 740, 1.25]]) {
      for (const lang of ['zh', 'en']) {
        const tag = `${lang}-${w}${scale > 1 ? '-large' : ''}`;
        const context = await browser.newContext({ ...devices['Pixel 7'], viewport: { width: w, height: hgt } });
        await context.addInitScript(([l, fs]) => {
          if (fs > 1) localStorage.setItem('ark.display', JSON.stringify({ font_scale: fs }));
          localStorage.setItem('ark.prefsV2', '1');
          localStorage.setItem('ark.lang', l);
          localStorage.setItem('ark.welcome.v1:local', '1');
        }, [lang, scale]);
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', (error) => errors.push(error.message));
        await page.goto(`${base}/?lang=${lang}&session=${session.id}#/execute/sessions`);
        await page.locator('.guidance-invite .btn.ghost').tap({ timeout: 4000 }).catch(() => {});
        await page.locator('.sw-thread .sw-turn-assistant').first().waitFor({ timeout: 30000 });
        await page.locator('.sw-thread .sw-turn-user', { hasText: 'ARK UPDATE' }).waitFor({ timeout: 15000 });
        // Closed tool rows with long summaries (four kinds of tools; a sub-agent).
        await page.locator('.sw-thread .sw-act-open-tab').first().waitFor({ timeout: 15000 });
        await page.evaluate(() => {
          const t = document.querySelector('.sw-thread');
          t.style.scrollBehavior = 'auto';
          const row = document.querySelector('.sw-thread .sw-act-open-tab').closest('.sw-act');
          t.scrollTop += row.getBoundingClientRect().top - t.getBoundingClientRect().top - t.clientHeight / 2;
        });
        await sleep(300);
        await page.screenshot({ path: path.join(shots, `mobile-bubbles-rows-${tag}.png`) });
        await openTools(page, '.sw-thread');
        const runs = await page.evaluate(measure, '.mobile-session-page');
        console.log('RUNS', tag, JSON.stringify(runs));
        for (const step of [0, 1, 2, 3]) {
          await page.evaluate((s) => { const t = document.querySelector('.sw-thread'); t.scrollTop = (t.scrollHeight - t.clientHeight) * s / 3; }, step);
          await sleep(150);
          await page.screenshot({ path: path.join(shots, `mobile-bubbles-runs-${tag}-${step}.png`) });
        }
        if (runs.bad.length) failures.push(`Runs ${tag}: ${runs.bad.join(' | ')}`);
        if (runs.off.length) failures.push(`Runs ${tag}: ${runs.off.join(' | ')}`);
        if (runs.page > 1) failures.push(`Runs ${tag}: page scrolls sideways by ${runs.page}px`);
        if (runs.thread > 1) failures.push(`Runs ${tag}: thread scrolls sideways by ${runs.thread}px`);
        // The Assistant chat with the same content (its history is mocked).
        await page.route('**/api/chat?*', (route) => route.fulfill({ json: { messages: CHAT } }));
        await page.goto(`${base}/?lang=${lang}#/chat`);
        await page.locator('.chat-thread .chat-msg.assistant').first().waitFor({ timeout: 20000 });
        await sleep(400);
        const chat = await page.evaluate(measure, '.chat-thread');
        console.log('CHAT', tag, JSON.stringify(chat));
        for (const step of [0, 1, 2]) {
          await page.evaluate((s) => { const t = document.querySelector('.chat-thread'); t.scrollTop = (t.scrollHeight - t.clientHeight) * s / 2; }, step);
          await sleep(150);
          await page.screenshot({ path: path.join(shots, `mobile-bubbles-chat-${tag}-${step}.png`) });
        }
        if (chat.bad.length) failures.push(`Chat ${tag}: ${chat.bad.join(' | ')}`);
        if (chat.page > 1) failures.push(`Chat ${tag}: page scrolls sideways by ${chat.page}px`);
        if (chat.thread > 1) failures.push(`Chat ${tag}: thread scrolls sideways by ${chat.thread}px`);
        assert.deepEqual(errors, [], `no page errors (${tag})`);
        await context.close();
      }
    }
  } finally {
    if (session) await api(base, 'POST', `/api/agent-sessions/${session.id}/stop`, {}).catch(() => {});
    await browser?.close();
    server.kill();
    await sleep(300);
    if (!process.env.ARK_TEST_SHOTS) rmSync(dir, { recursive: true, force: true });
  }
  assert.deepEqual(failures, []);
});
