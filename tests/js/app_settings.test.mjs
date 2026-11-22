// The app's settings and the server's settings told apart (Settings › App
// only inside the desktop app), "ARK Studio was updated to ..." and "This
// server was updated to ..." once per version, and the app window's own
// "available" notice (desktop shell) on every ARK page.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

import {
  hasUpdate, inDesktopApp, releaseUrl, serverNotice, shellShowsUpdates, updateBanner, updatedNotice,
} from "../../ark_studio/web/js/desktop-update-model.js";
import { appUpdateHub, localUpdateHub } from "../../ark_studio/web/js/update-state.js";

const root = new URL("../../", import.meta.url);
const read = (path) => readFile(new URL(path, root), "utf8");
const info = (extra = {}) => ({ current: "2.1.1", enabled: true, selfInstall: true, available: "2.1.2", dismissed: "", ...extra });

test("Settings › App and the app pointers only inside the desktop app", async () => {
  assert.equal(inDesktopApp(undefined), false);
  assert.equal(inDesktopApp({ shell: "tauri" }), false); // an older shell without the hub
  assert.equal(inDesktopApp({ hub: { context() {} } }), true);
  const settings = await read("ark_studio/web/js/views/settings.js");
  assert.match(settings, /app: desktopApp \? \[appSettingsPanel\(\)\] : \[\]/);
  assert.match(settings, /desktopApp \? appOverviewCard\(\(\) => jump\("app"\)\) : null/);
  assert.match(settings, /settingsScopeLine\(\(\) => jump\("app"\)\)/);
  assert.match(settings, /t\(desktopApp \? "settings\.overview\.where_app" : "settings\.overview\.where_browser"\)/);
  // Tabs without panels are dropped, so a browser never lists App.
  assert.match(settings, /\.filter\(\(tab\) => tab\.sections\.length\)/);
  const about = await read("ark_studio/web/js/about.js");
  assert.match(about, /inDesktopApp\(\) \? h\("button", \{ class: "settings-where-app"/);
});

test("app pages: local and remote pages read the update state; only the local page controls it", async () => {
  const hub = (role) => ({ context: async () => ({ role }), goLocal() {}, update: async () => info() });
  assert.deepEqual((await appUpdateHub(hub("remote"))).role, "remote");
  assert.equal((await appUpdateHub(hub("local"))).role, "local");
  assert.equal(await appUpdateHub(hub("bundled")), null);
  assert.equal(await appUpdateHub(null), null);
  assert.equal(await localUpdateHub(hub("remote")), null);
  assert.ok(await localUpdateHub(hub("local")));
  const about = await read("ark_studio/web/js/about.js");
  assert.match(about, /state\.kind === "desktop" && role === "local" && state\.enabled && hub/);
  assert.match(about, /role === "remote" && openApp/);
});

test("the app's commands: what server pages may call", async () => {
  const lib = await read("ark_studio/desktop/src-tauri/src/lib.rs");
  const list = (name) => lib.slice(lib.indexOf(`const ${name}: &[&str] = &[`), lib.indexOf("];", lib.indexOf(`const ${name}`)));
  const server = list("SERVER_PERMISSIONS");
  const local = list("LOCAL_PERMISSIONS");
  for (const p of ["allow-update-state", "allow-dismiss-update", "allow-open-app-settings"]) assert.ok(server.includes(`"${p}"`), p);
  for (const p of ["allow-check-update", "allow-set-update-auto-check", "allow-dismiss-update-notice", "allow-open-releases"]) {
    assert.ok(local.includes(`"${p}"`) && !server.includes(`"${p}"`), p);
  }
  const build = await read("ark_studio/desktop/src-tauri/build.rs");
  for (const cmd of ["dismiss_update", "dismiss_update_notice", "open_app_settings", "open_local_settings"]) {
    assert.ok(build.includes(`"${cmd}"`), cmd);
    assert.ok(lib.includes(`            ${cmd},`), `${cmd} is registered`);
  }
});

test("updated to: the app's notice with the key changes", () => {
  assert.equal(updatedNotice(null), null);
  assert.equal(updatedNotice(info()), null);
  assert.equal(updatedNotice(info({ notice: { version: "" } })), null);
  assert.deepEqual(updatedNotice(info({ notice: {
    version: "v2.1.2", previous: "2.1.1", changes: ["One", " ", 3, "Two"], url: "https://github.com/MasterYip/ARK-Studio/releases/tag/v2.1.2",
  } })), { version: "2.1.2", previous: "2.1.1", changes: ["One", "Two"], url: "https://github.com/MasterYip/ARK-Studio/releases/tag/v2.1.2" });
  // An odd URL falls back to the release page; seven changes are cut to six.
  const n = updatedNotice(info({ notice: { version: "2.1.2", url: "javascript:x", changes: "abcdefg".split("") } }));
  assert.equal(n.url, releaseUrl("2.1.2"));
  assert.equal(n.changes.length, 6);
  assert.equal(releaseUrl(""), "https://github.com/MasterYip/ARK-Studio/releases/latest");
});

test("this server was updated: once per person and version", () => {
  assert.equal(serverNotice("", "2.1.1"), "");
  assert.equal(serverNotice("2.1.1", ""), "store"); // first visit: nothing to compare with
  assert.equal(serverNotice("2.1.1", "2.1.1"), "");
  assert.equal(serverNotice("2.1.2", "2.1.1"), "show");
  assert.equal(serverNotice("v2.1.2", "2.1.2"), "");
});

test("Later in the app window hides every page's notice for that version, not the dot or About", () => {
  assert.equal(updateBanner(info({ dismissed: "2.1.2" })), null);
  assert.equal(updateBanner(info({ dismissed: "2.1.1" })).version, "2.1.2");
  assert.equal(hasUpdate(info({ dismissed: "2.1.2" })), true);
  assert.equal(shellShowsUpdates({ updateNotice: true }), true);
  assert.equal(shellShowsUpdates({}), false);
});

test("the desktop shell's notice: which version to offer", async () => {
  const shell = await read("ark_studio/desktop/src-tauri/src/shell.js");
  const code = shell.slice(shell.indexOf("// update-notice:begin"), shell.indexOf("// update-notice:end"));
  const ctx = {};
  vm.runInNewContext(`${code}; this.f = updateNoticeVersion;`, ctx);
  const f = ctx.f;
  assert.equal(f(info()), "2.1.2");
  assert.equal(f(info({ available: "v2.1.2" })), "2.1.2");
  assert.equal(f(info({ dismissed: "2.1.2" })), "");
  assert.equal(f(info({ available: "2.1.1" })), "");
  assert.equal(f(info({ enabled: false })), "");
  assert.equal(f(info({ available: null })), "");
  assert.equal(f(null), "");
  // Shown on local and remote pages of the main window; Update installs on
  // the local page and opens the app's settings at About elsewhere.
  assert.match(shell, /if \(noticeRole !== "local" && noticeRole !== "remote"\) return;/);
  assert.match(shell, /if \(noticeRole !== "local"\) \{ invoke\("open_app_settings", \{ view: "about" \}\)/);
  assert.match(shell, /invoke\("dismiss_update", \{ version: version \}\)/);
  assert.match(shell, /updateNotice: true,/);
});

test("Home: the available banner only where the app window does not show its own notice", async () => {
  const home = await read("ark_studio/web/js/desktop-update.js");
  assert.match(home, /role === "local" && !parts\.available && !shellShowsUpdates\(\)/);
  assert.match(home, /if \(found\?\.role !== "local"\) showServerNotice\(parts, draw\)/);
  assert.match(home, /if \(step === "store"\) \{ saveSeenServerVersion\(version\); return; \}/);
});

test("strings for the app/server split in English and Chinese", async () => {
  const [en, zh, about, home, settings] = await Promise.all(["i18n-en.js", "i18n-zh.js", "about.js", "desktop-update.js", "views/settings.js"]
    .map((f) => read(`ark_studio/web/js/${f}`)));
  const keys = new Set([...`${about}${home}${settings}`.matchAll(/"(app\.[a-z_.]+|server\.updated\.[a-z_]+|settings\.overview\.where_[a-z]+)"/g)].map((m) => m[1]));
  assert.ok(keys.size >= 20, String(keys.size));
  for (const key of keys) {
    const re = new RegExp(`"${key.replace(/\./g, "\\.")}":`);
    assert.match(en, re, `en ${key}`);
    assert.match(zh, re, `zh ${key}`);
  }
  assert.match(zh, /"app\.server_settings": "此服务器的设置（\{server\}）"/);
  assert.match(zh, /"app\.app_settings_link": "此应用的设置"/);
  assert.match(zh, /"app\.updated\.title": "ARK Studio 已更新到 \{version\}"/);
  assert.match(zh, /"app\.updated\.all": "查看全部更新内容"/);
});
