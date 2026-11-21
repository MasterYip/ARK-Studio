// Settings > Project storage location: the panel model (project-storage-model.js).
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  LOW_SPACE_BYTES, formatBytes, looksAbsolute, pathInputError, examplePath, canPickFolder, storageView,
  checkSummary, fallbackNotice,
} from "../../ark_studio/web/js/project-storage-model.js";
import { SETTINGS_TABS, resolveSettingsTab, settingsTabScope } from "../../ark_studio/web/js/settings-tabs.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const STORAGE = {
  default: "D:\\ARK Projects", configured: "D:\\ARK Projects", builtin: "C:\\Users\\u\\AppData\\Local\\ARK\\projects",
  fallback: false, free_bytes: 50 * 1024 ** 3, host_local: true, platform: "windows",
  folders: [
    { path: "C:\\Users\\u\\AppData\\Local\\ARK\\projects", builtin: true, default: false, exists: true, projects: 3 },
    { path: "D:\\ARK Projects", builtin: false, default: true, exists: true, projects: 1 },
    { path: "E:\\old", builtin: false, default: false, exists: false, projects: 0 },
  ],
};

test("sizes read like a file manager", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(1536), "1.5 KB");
  assert.equal(formatBytes(50 * 1024 ** 3), "50.0 GB");
  assert.equal(formatBytes(250 * 1024 ** 3), "250 GB");
  for (const bad of [-1, NaN, Infinity, null, "5"]) assert.equal(formatBytes(bad), "");
});

test("typed paths must be absolute for the server's platform", () => {
  assert.ok(looksAbsolute("D:\\ARK", "windows"));
  assert.ok(looksAbsolute("d:/ark", "windows"));
  assert.ok(looksAbsolute("\\\\nas\\share\\ark", "windows"));
  assert.ok(!looksAbsolute("ark\\projects", "windows"));
  assert.ok(!looksAbsolute("/data/ark", "windows"));
  assert.ok(looksAbsolute("/data/ark", "linux"));
  assert.ok(!looksAbsolute("~/ark", "linux"));
  assert.equal(pathInputError("", "linux"), "storage.error.required");
  assert.equal(pathInputError("   ", "linux"), "storage.error.required");
  assert.equal(pathInputError("ark", "linux"), "storage.error.relative");
  assert.equal(pathInputError(" /srv/ark/projects ", "linux"), "");
  assert.match(examplePath("windows"), /^[A-Z]:\\/);
  assert.match(examplePath("linux"), /^\//);
});

test("the desktop folder picker is offered only when the app provides it", () => {
  assert.equal(canPickFolder(null), false);
  assert.equal(canPickFolder({ context() {} }), false);
  assert.equal(canPickFolder({ pickFolder() {} }), true);
});

test("the view shows where new projects go and which folders stay listed", () => {
  const v = storageView(STORAGE);
  assert.equal(v.current, "D:\\ARK Projects");
  assert.equal(v.usesBuiltin, false);
  assert.equal(v.freeText, "50.0 GB");
  assert.equal(v.lowSpace, false);
  assert.equal(v.canReveal, true);
  assert.deepEqual(v.folders.map((f) => [f.projects, f.isDefault, f.exists, f.removable]),
    [[3, false, true, false], [1, true, true, false], [0, false, false, true]]);
  // Nothing chosen: the server's own folder, not removable.
  const plain = storageView({ default: "/data/p", builtin: "/data/p", configured: "", free_bytes: LOW_SPACE_BYTES - 1,
    folders: [{ path: "/data/p", builtin: true, default: true, projects: 2 }] });
  assert.equal(plain.usesBuiltin, true);
  assert.equal(plain.lowSpace, true);
  assert.equal(plain.canReveal, false);
  assert.equal(plain.folders[0].removable, false);
  // The chosen folder is missing: the built-in one is used and said so.
  const fb = storageView({ ...STORAGE, default: STORAGE.builtin, fallback: true });
  assert.equal(fb.fallback, true);
  assert.equal(fb.usesBuiltin, true);
  assert.equal(fb.current, STORAGE.builtin);
  assert.equal(fb.configured, "D:\\ARK Projects");
  // Unknown free space and an empty reply.
  assert.equal(storageView({ ...STORAGE, free_bytes: null }).freeText, "");
  assert.deepEqual(storageView(null).folders, []);
});

test("a checked folder says whether it will be created and how much space is left", () => {
  assert.deepEqual(checkSummary({ ok: true, path: "/a", exists: true, will_create: false, free_bytes: 2 * LOW_SPACE_BYTES }),
    [["storage.check.exists", { path: "/a" }], ["storage.check.free", { size: "2.0 GB" }]]);
  assert.deepEqual(checkSummary({ ok: true, path: "/b", will_create: true, free_bytes: 1024 }),
    [["storage.check.will_create", { path: "/b" }], ["storage.check.low_space", { size: "1.0 KB" }]]);
  assert.deepEqual(checkSummary({ ok: true, path: "/c", will_create: false, free_bytes: null }), [["storage.check.exists", { path: "/c" }]]);
  assert.deepEqual(checkSummary(null), []);
});

test("creating a project in the fallback folder is reported", () => {
  assert.equal(fallbackNotice({ summary: {} }), null);
  assert.deepEqual(fallbackNotice({ storage_notice: { code: "STORAGE_FALLBACK", configured: "E:\\usb", used: "C:\\p" } }),
    ["storage.fallback_created", { configured: "E:\\usb", used: "C:\\p" }]);
});

test("the storage tab sits in the admin scope", () => {
  assert.deepEqual(resolveSettingsTab("projectStorage"), { tab: "storage", section: "" });
  assert.equal(settingsTabScope("storage"), "admin");
  assert.ok(SETTINGS_TABS.findIndex((tab) => tab.id === "storage") < SETTINGS_TABS.findIndex((tab) => tab.id === "admin"));
});

test("every storage text exists in English and Chinese with the same placeholders", async () => {
  const panel = await readFile(new URL("../../ark_studio/web/js/project-storage-panel.js", import.meta.url), "utf8");
  const model = await readFile(new URL("../../ark_studio/web/js/project-storage-model.js", import.meta.url), "utf8");
  const used = new Set([...`${panel}\n${model}`.matchAll(/"((?:storage|settings\.tab\.storage|project\.reveal)[a-zA-Z0-9_.]*)"/g)]
    .map((m) => m[1]).filter((k) => k.includes(".") && !k.endsWith(".")));
  assert.ok(used.size > 20);
  const vars = (s) => [...String(s).matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");
  for (const key of Object.keys(EN).filter((k) => k.startsWith("storage.") || k === "settings.tab.storage")) used.add(key);
  for (const key of used) {
    assert.ok(EN[key], `en: ${key}`);
    assert.ok(ZH[key], `zh: ${key}`);
    assert.equal(vars(EN[key]), vars(ZH[key]), key);
  }
  assert.equal(ZH["storage.title"], "项目存储位置");
  assert.equal(ZH["project.reveal"], "在文件夹中显示");
});
