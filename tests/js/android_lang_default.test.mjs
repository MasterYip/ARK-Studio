// Run: node --test tests/js/android_lang_default.test.mjs
// The Android app's WebView follows the phone's language until the user picks one.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const SRC = await readFile(new URL("../../ark_studio/web/js/startup-graph.js", import.meta.url), "utf8");
const ANDROID_UA = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36 ARKAndroid/3.0";
const DESKTOP_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120 Safari/537.36";

function storage(initial = {}) {
  const map = new Map(Object.entries(initial));
  return {
    getItem: (key) => (map.has(key) ? map.get(key) : null),
    setItem: (key, value) => map.set(key, String(value)),
    removeItem: (key) => map.delete(key),
    map,
  };
}

function boot({ ua, languages, stored = {} }) {
  const localStorage = storage(stored);
  const root = { dataset: {}, style: { setProperty() {} } };
  const context = {
    navigator: { userAgent: ua, languages, language: languages[0] },
    localStorage,
    location: { search: "" },
    URLSearchParams,
    matchMedia: () => ({ matches: false }),
    document: { documentElement: root, querySelector: () => null },
  };
  vm.runInNewContext(SRC, context);
  return localStorage.map;
}

test("first run in the Android app follows a Chinese system", () => {
  const map = boot({ ua: ANDROID_UA, languages: ["zh-CN", "en-US"] });
  assert.equal(map.get("ark.lang"), "zh");
  assert.equal(map.get("ark.langAuto"), "1");
});

test("first run in the Android app picks English for any other system language", () => {
  assert.equal(boot({ ua: ANDROID_UA, languages: ["en-GB"] }).get("ark.lang"), "en");
  assert.equal(boot({ ua: ANDROID_UA, languages: ["ja-JP", "zh-CN"] }).get("ark.lang"), "en");
  assert.equal(boot({ ua: ANDROID_UA, languages: ["zh-Hans-CN"] }).get("ark.lang"), "zh");
});

test("an automatic choice follows a later system language change", () => {
  const map = boot({ ua: ANDROID_UA, languages: ["en-US"], stored: { "ark.lang": "zh", "ark.langAuto": "1" } });
  assert.equal(map.get("ark.lang"), "en");
});

test("an explicit choice in ARK is kept", () => {
  const map = boot({ ua: ANDROID_UA, languages: ["zh-CN"], stored: { "ark.lang": "en" } });
  assert.equal(map.get("ark.lang"), "en");
  assert.equal(map.has("ark.langAuto"), false);
});

test("desktop browsers keep the existing default", () => {
  const map = boot({ ua: DESKTOP_UA, languages: ["zh-CN"] });
  assert.equal(map.has("ark.lang"), false);
});

test("choosing a language in ARK ends system following", async () => {
  const state = await readFile(new URL("../../ark_studio/web/js/state.js", import.meta.url), "utf8");
  const body = state.slice(state.indexOf("export function setLanguage("), state.indexOf("export function selectedStatuses"));
  assert.match(body, /localStorage\.removeItem\("ark\.langAuto"\)/);
  const login = await readFile(new URL("../../ark_studio/web/js/login.js", import.meta.url), "utf8");
  assert.match(login, /setItem\("ark\.lang", query\); localStorage\.removeItem\("ark\.langAuto"\)/);
});
