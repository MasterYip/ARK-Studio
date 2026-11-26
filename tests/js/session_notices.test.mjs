// Run: node --experimental-vm-modules --test tests/js/session_notices.test.mjs
// Finished sessions: the list dots (session-model.js shownStatus), the
// bubbles of Execute and Runs and what to notify (session-notices-model.js).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

import {
  freshNotices, noticeAction, noticeBubble, noticeItems, noticeKeys, noticePrefs, noticeText, unseenById,
} from "../../ark_studio/web/js/session-notices-model.js";

async function loadSessionModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/session-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "session-model.js" });
  await module.link(() => { throw new Error("session-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}
const model = await loadSessionModel();
const read = (p) => readFile(new URL(`../../${p}`, import.meta.url), "utf8");

const zh = {
  "session.notice.done": "{name} 已完成一轮，等待你回复",
  "session.notice.error": "{name} 出错停止：{reason}",
  "session.notice.error_title": "{name} 出错停止",
  "session.notice.error_unknown": "未知错误",
};
const t = (key, vars = {}) => (zh[key] || key).replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? "");

test("dots: running (accent, pulsing), done (green), errored (red), ended (grey)", () => {
  const stream = (status, extra = {}) => ({ id: "s", status, capabilities: { stream_input: true }, ...extra });
  const done = { at: "2026-11-26T10:00:00+00:00", outcome: "done", reason: "" };
  const failed = { at: "2026-11-26T10:00:00+00:00", outcome: "error", reason: "API Error: 529" };
  assert.equal(model.shownStatus(stream("running", { turn_end: done })), "running", "a new turn runs");
  assert.equal(model.shownStatus(stream("starting")), "starting");
  assert.equal(model.shownStatus(stream("idle", { turn_end: done })), "done");
  assert.equal(model.shownStatus(stream("idle")), "done", "waiting after a turn from before turn_end");
  assert.equal(model.shownStatus(stream("idle", { turn_end: failed })), "errored");
  assert.equal(model.shownStatus(stream("idle", { turn_end: done, busy: { subagents: 1, background: 0 } })), "running",
    "sub-agents still run: not finished");
  // One-shot turns (Codex) end with the process.
  assert.equal(model.shownStatus({ id: "c", status: "stopped", exit_code: 0, turn_end: done }), "done");
  assert.equal(model.shownStatus({ id: "c", status: "stopped", exit_code: null }), "stopped", "stopped by the person");
  assert.equal(model.shownStatus({ id: "c", status: "stopped", closed: true, turn_end: done }), "stopped", "closed");
  assert.equal(model.shownStatus({ id: "c", status: "errored", turn_end: failed }), "errored");
  assert.equal(model.shownStatus({ id: "c", status: "stopped", turn_end: { ...failed, reason: "ARK restarted" } }), "errored");
  assert.equal(model.finishOutcome({ turn_end: { outcome: "odd" } }), "");
  // The header label follows the dot.
  assert.equal(model.sessionHeaderModel(stream("idle", { turn_end: done })).state, "done");
  assert.equal(model.headerStateKey("done"), "tasks.status.done");
});

test("the CSS gives each state its colour", async () => {
  const css = await read("ark_studio/web/css/app.css");
  assert.match(css, /\.sw-dot\.running\{background:var\(--accent\)/);
  assert.match(css, /\.sw-dot\.done\{background:var\(--green\)/);
  assert.match(css, /\.sw-dot\.errored\{background:var\(--red\)/);
  assert.match(css, /\.sw-dot\.running,\.sw-dot\.starting\{animation:swpulse/);
  assert.match(css, /\.tab-badge\.session-badge\.error\{background:var\(--red\)/);
  for (const lang of ["en", "zh"]) {
    const text = await read(`ark_studio/web/js/i18n-${lang}.js`);
    for (const key of ["tasks.status.done", "session.notice.done", "session.notice.error", "session.notice.bubble",
      "settings.notify.done", "settings.notify.error"]) assert.ok(text.includes(`"${key}"`), `${lang} ${key}`);
  }
});

test("bubbles: hidden without notices, orange for finished, red once one failed", () => {
  assert.deepEqual(noticeBubble([]), { n: 0, errors: 0, done: 0, state: "none", text: "0" });
  const items = noticeItems({ items: [
    { id: "s_a", title: "WM-HOI", outcome: "done", at: "2026-11-26T10:00:00+00:00" },
    { id: "s_b", title: "SandBox", outcome: "done", at: "2026-11-26T10:01:00+00:00" },
    { id: "s_x", outcome: "odd" }, { outcome: "done" }, null,
  ] });
  assert.equal(items.length, 2, "unknown outcomes and rows without an id are dropped");
  assert.deepEqual(noticeBubble(items), { n: 2, errors: 0, done: 2, state: "new", text: "2" });
  const withError = [...items, { id: "s_c", title: "Survey", outcome: "error", at: "2026-11-26T10:02:00+00:00", reason: "API Error" }];
  assert.deepEqual(noticeBubble(withError), { n: 3, errors: 1, done: 2, state: "error", text: "3" });
  assert.equal(noticeBubble(Array.from({ length: 120 }, (_, i) => ({ id: `s_${i}`, outcome: "done" }))).text, "99+");
  const unseen = unseenById(withError);
  assert.equal(unseen.get("s_a"), "done");
  assert.equal(unseen.get("s_c"), "error");
  assert.equal(unseen.has("s_zz"), false);
  assert.deepEqual(noticePrefs({}), { done: true, error: true }, "both switches default on");
  assert.deepEqual(noticePrefs({ prefs: { done: false } }), { done: false, error: true });
});

test("only new finishes notify; the first reply of a project only records", () => {
  const a = { id: "s_a", outcome: "done", at: "1" };
  const b = { id: "s_b", outcome: "error", at: "2", reason: "API Error: 529" };
  assert.deepEqual(freshNotices(null, [a, b]), []);
  const known = noticeKeys([a]);
  assert.deepEqual(freshNotices(known, [a, b]), [b]);
  assert.deepEqual(freshNotices(known, [{ ...a, at: "3" }]), [{ ...a, at: "3" }], "a later finish of the same session is new");
});

test("what to do with a new finish: seen when on screen, toast with focus, system notification without", () => {
  const done = { id: "s_a", outcome: "done", at: "1" };
  const failed = { id: "s_b", outcome: "error", at: "1" };
  assert.equal(noticeAction(done, { viewing: "s_a" }), "seen", "the person is looking at it: no notification");
  assert.equal(noticeAction(done, { viewing: "s_a", visible: false }), "system", "in a hidden tab it notifies");
  assert.equal(noticeAction(done, { viewing: "s_b" }), "toast");
  assert.equal(noticeAction(done, { focused: false }), "system");
  assert.equal(noticeAction(done, { prefs: { done: false, error: true } }), "", "finished alerts off");
  assert.equal(noticeAction(failed, { prefs: { done: false, error: true }, focused: false }), "system", "errors still notify");
  assert.equal(noticeAction(failed, { prefs: { done: true, error: false } }), "", "error alerts off");
});

test("notification text", () => {
  assert.deepEqual(noticeText({ id: "s_a", title: "WM-HOI", outcome: "done" }, t),
    { title: "WM-HOI 已完成一轮，等待你回复", body: "", line: "WM-HOI 已完成一轮，等待你回复" });
  assert.deepEqual(noticeText({ id: "s_b", title: "SandBox", outcome: "error", reason: "API Error: 529 overloaded" }, t),
    { title: "SandBox 出错停止", body: "API Error: 529 overloaded", line: "SandBox 出错停止：API Error: 529 overloaded" });
  assert.equal(noticeText({ id: "s_c", title: "", outcome: "error" }, t).line, "s_c 出错停止：未知错误");
});

test("the desktop app may show them: command, permission and bridge", async () => {
  const lib = await read("ark_studio/desktop/src-tauri/src/lib.rs");
  const server = lib.slice(lib.indexOf("const SERVER_PERMISSIONS: &[&str] = &["), lib.indexOf("];", lib.indexOf("const SERVER_PERMISSIONS")));
  assert.ok(server.includes('"allow-show-notification"'));
  assert.ok(lib.includes("            show_notification,"));
  assert.ok((await read("ark_studio/desktop/src-tauri/build.rs")).includes('"show_notification"'));
  assert.match(await read("ark_studio/desktop/src-tauri/src/shell.js"), /notify: function \(title, body\)/);
  const sw = await read("ark_studio/web/sw.js");
  for (const f of ["session-notices.js", "session-notices-model.js", "session-notices-panel.js"]) assert.ok(sw.includes(`./js/${f}`), f);
});
