// Settings › About: version and update data, the shared update state (Home
// banner, About page, activity bar dot), the overview card and the activity
// bar's About button.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { aboutState, aboutStatus, hasUpdate } from "../../ark_studio/web/js/desktop-update-model.js";
import {
  latestUpdate, onUpdate, publishUpdate, refreshUpdate, resetUpdateState, watchUpdates,
} from "../../ark_studio/web/js/update-state.js";

const web = new URL("../../ark_studio/web/", import.meta.url);
const info = (extra = {}) => ({
  current: "2.1.1", enabled: true, install: "nsis", selfInstall: true, available: null,
  autoCheck: true, lastCheck: 1795000000, ...extra,
});

test("the About dot shows only for a newer version the app found", () => {
  assert.equal(hasUpdate(info({ available: "2.1.2" })), true);
  assert.equal(hasUpdate(info()), false);
  assert.equal(hasUpdate(info({ available: "2.1.1" })), false);
  assert.equal(hasUpdate(info({ available: "2.1.2", enabled: false })), false);
  assert.equal(hasUpdate(null), false);
});

test("desktop app: version, install type, last check, start-up check, update action", () => {
  const state = aboutState(info({ available: "v2.1.2" }), { serverVersion: "2.1.0" });
  assert.deepEqual(state, {
    kind: "desktop", version: "2.1.1", enabled: true, available: "2.1.2", action: "install",
    portable: false, autoCheck: true, lastCheck: 1795000000 * 1000,
  });
  const portable = aboutState(info({ selfInstall: false, install: "portable", available: "2.1.2", lastCheck: 0, autoCheck: false }));
  assert.equal(portable.action, "download");
  assert.equal(portable.portable, true);
  assert.equal(portable.lastCheck, 0);
  assert.equal(portable.autoCheck, false);
  // A copy without the updater (development build) still shows its version.
  assert.equal(aboutState(info({ enabled: false, available: "2.1.2" })).available, "");
});

test("browser and server pages show the server's version and no update controls", () => {
  assert.deepEqual(aboutState(null, { serverVersion: " v2.1.1 " }), {
    kind: "server", version: "2.1.1", enabled: false, available: "", action: "",
    portable: false, autoCheck: false, lastCheck: 0,
  });
  assert.equal(aboutState(null).version, "");
  assert.equal(aboutStatus(aboutState(null)), null);
});

test("status line: new version, up to date after a check, no updater", () => {
  assert.deepEqual(aboutStatus(aboutState(info({ available: "2.1.2" }))), ["about.update.available", { version: "2.1.2" }]);
  assert.deepEqual(aboutStatus(aboutState(info({ available: "2.1.2", selfInstall: false }))),
    ["about.update.available_portable", { version: "2.1.2" }]);
  // Nothing found: only claim "up to date" right after a check on this page.
  assert.equal(aboutStatus(aboutState(info())), null);
  assert.deepEqual(aboutStatus(aboutState(info()), { checked: true }), ["about.update.up_to_date", {}]);
  assert.deepEqual(aboutStatus(aboutState(info({ enabled: false }))), ["about.update.no_updater", {}]);
});

test("one update state for the banner, About and the dot", async () => {
  resetUpdateState();
  const seen = [];
  const off = onUpdate((value) => seen.push(value?.available ?? null));
  publishUpdate(info({ available: "2.1.2" }));
  assert.equal(latestUpdate().available, "2.1.2");
  publishUpdate("nonsense");
  assert.equal(latestUpdate(), null);
  off();
  publishUpdate(info());
  assert.deepEqual(seen, ["2.1.2", null]);
  // A listener that throws does not stop the others.
  const later = [];
  onUpdate(() => { throw new Error("boom"); });
  onUpdate((value) => later.push(value.current));
  publishUpdate(info());
  assert.deepEqual(later, ["2.1.1"]);
  resetUpdateState();
});

test("the app is asked once now and once more while its start-up check may still run", async () => {
  resetUpdateState();
  const replies = [info(), info({ available: "2.1.2" })];
  let calls = 0;
  const hub = { update: async () => replies[Math.min(calls++, replies.length - 1)] };
  const waits = [];
  const result = await watchUpdates({ hub, wait: async (ms) => { waits.push(ms); }, every: 0 });
  assert.equal(calls, 2);
  assert.deepEqual(waits, [15000]);
  assert.equal(result.available, "2.1.2");
  assert.equal(latestUpdate().available, "2.1.2");
  // Once per page.
  await watchUpdates({ hub, wait: async () => {} });
  assert.equal(calls, 2);
  // A failing hub keeps the last state.
  assert.equal((await refreshUpdate({ update: async () => { throw new Error("gone"); } })).available, "2.1.2");
  // Browser: no hub, no state.
  resetUpdateState();
  assert.equal(await refreshUpdate(), null);
  assert.equal(await watchUpdates(), null);
  resetUpdateState();
});

test("activity bar: an About button (info icon, update dot) above Settings and the avatar", async () => {
  const app = await readFile(new URL("js/app.js", web), "utf8");
  const foot = app.slice(app.indexOf("function renderActivityFoot()"));
  const about = foot.indexOf('aboutActivityButton(() => openSettingsAt("about"))');
  assert.ok(about > 0);
  assert.ok(about < foot.indexOf('workspaceButton("settings"'));
  assert.match(app, /\{ id: "about", label: t\("about\.button"\)[^}]*openSettingsAt\("about"\)/);
  assert.match(app, /watchUpdates\(\)/);
  const js = await readFile(new URL("js/about.js", web), "utf8");
  const button = js.slice(js.indexOf("export function aboutActivityButton"));
  assert.match(button, /h\("button", \{\s*class: "wb-act wb-foot-btn about-act-btn", type: "button", "data-about-button": ""/);
  assert.match(button, /icon\("info", "sm"\), h\("span", \{ class: "update-dot", "aria-hidden": "true", hidden: true \}\)/);
  // Tooltip and accessible name follow the update state.
  assert.match(js, /button\.title = label;\s*button\.setAttribute\("aria-label", label\);/);
  assert.match(js, /t\("about\.button_update", \{ version: state\.available \}\)/);
  const css = await readFile(new URL("css/app.css", web), "utf8");
  assert.match(css, /\.wb-act\.about-act-btn \.update-dot\{position:absolute/);
  assert.match(css, /\.update-dot\[hidden\]\{display:none\}/);
});

test("Settings: the overview carries the About card; About holds the update block", async () => {
  const settings = await readFile(new URL("js/views/settings.js", web), "utf8");
  const overview = settings.slice(settings.indexOf("const overviewPanel = h("));
  assert.match(overview.slice(0, 600), /aboutOverviewCard\(\(\) => jump\("about"\)\)/);
  const about = settings.slice(settings.indexOf("const aboutPanel = h("), settings.indexOf("// Stored agent session transcripts"));
  assert.match(about, /aboutUpdateBlock\(\)/);
  assert.match(about, /AGPL-3\.0/);
  // An explicit ?settings= tab wins over a ?scope= from an older link.
  assert.match(settings, /requestedScope && scopes\.includes\(requestedScope\) && !requestedTab/);
});

test("About strings exist in English and Chinese", async () => {
  const [en, zh, js] = await Promise.all([
    readFile(new URL("js/i18n-en.js", web), "utf8"),
    readFile(new URL("js/i18n-zh.js", web), "utf8"),
    readFile(new URL("js/about.js", web), "utf8"),
  ]);
  const keys = new Set([...js.matchAll(/"(about\.[a-z_.]+)"/g)].map((m) => m[1]));
  for (const status of ["available", "available_portable", "up_to_date", "no_updater"]) keys.add(`about.update.${status}`);
  assert.ok(keys.size > 20);
  for (const key of keys) {
    assert.match(en, new RegExp(`"${key.replace(/\./g, "\\.")}":`), `en ${key}`);
    assert.match(zh, new RegExp(`"${key.replace(/\./g, "\\.")}":`), `zh ${key}`);
  }
  assert.match(zh, /"about\.check": "检查更新"/);
  assert.match(zh, /"about\.auto_check": "自动检查更新（启动时和运行期间每 12 小时一次）"/);
});
