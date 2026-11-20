// Run: node --test --experimental-vm-modules tests/js/topbar_order.test.mjs
// Top-bar right group order, one shared control style, and Settings plus the
// account avatar at the bottom of the workbench activity bar.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const web = new URL("../../ark_studio/web/", import.meta.url);

async function actionsSource() {
  const app = await readFile(new URL("js/app.js", web), "utf8");
  const start = app.indexOf('const actions = h("div", { class: "top-actions" }');
  return { app, actions: app.slice(start, app.indexOf("topbar.appendChild(actions);", start)) };
}

test("right group: search, sync, Assistant, layout, theme, language, then Settings and account", async () => {
  const { actions } = await actionsSource();
  const marks = [
    'class: "search-btn top-ctl"',
    "    sync,",
    'workspaceButton("assistant"',
    "    layoutToggles,",
    "theme-top-btn",
    'class: "lang-toggle top-ctl"',
    'workspaceButton("settings"',
    "    account,",
    "mobile-menu-btn",
  ];
  const at = marks.map((mark) => actions.indexOf(mark));
  at.forEach((index, k) => assert.ok(index > 0, marks[k]));
  assert.deepEqual([...at].sort((a, b) => a - b), at);
});

test("Assistant and Settings are separate buttons, not a shared pill", async () => {
  const { app } = await actionsSource();
  assert.doesNotMatch(app, /aux-tabs|tab-aux|AUX_WORKSPACES/);
  const css = await readFile(new URL("css/app.css", web), "utf8");
  assert.doesNotMatch(css, /\.aux-tabs|\.tab-aux/);
});

test("Settings and the account sit at the bottom of the activity bar on the workbench", async () => {
  const { app } = await actionsSource();
  const foot = app.slice(app.indexOf("function renderActivityFoot()"));
  assert.ok(foot.indexOf('workspaceButton("settings"') < foot.indexOf("accountMenuButton(ctx, { side: true })"));
  assert.match(foot, /setActivityFooter\(activityFoot\)/);
  const css = await readFile(new URL("css/app.css", web), "utf8");
  assert.match(css, /\.wb-activity-foot\{margin-top:auto/);
  assert.match(css, /body\.wb-on \.topbar \.settings-top-btn,body\.wb-on \.topbar \.account-btn\{display:none\}/);
  const mobile = await readFile(new URL("css/mobile.css", web), "utf8");
  assert.match(mobile, /\.top-actions \.settings-top-btn/);
  const profile = await readFile(new URL("js/profile.js", web), "utf8");
  assert.match(profile, /side \? \{ x: rect\.right \+ 6, y: rect\.bottom \}/);
});

test("every top-bar control uses the shared style", async () => {
  const { actions } = await actionsSource();
  const css = await readFile(new URL("css/app.css", web), "utf8");
  for (const cls of ["search-btn top-ctl", "top-ctl theme-top-btn", "top-ctl mobile-filter-btn", "assistant-top-btn top-ctl", "settings-top-btn top-ctl"]) {
    assert.ok(actions.includes(cls), cls);
  }
  // The language switch is one toggle in the shared style, not two buttons.
  assert.ok(actions.includes('class: "lang-toggle top-ctl"'));
  assert.equal((actions.match(/lang-toggle/g) || []).length, 1);
  assert.match(actions, /changeLanguage\(getLang\(\) === "zh" \? "en" : "zh"\)/);
  assert.match(actions, /"aria-label": t\("topbar\.lang_switch"\)/);
  assert.match(css, /\.topbar \.top-ctl\{[^}]*height:var\(--top-control-height\)[^}]*border-radius:8px/);
  assert.doesNotMatch(css, /\.lang-toggle button\.on|\.layout-toggle\.on\{/);
});
