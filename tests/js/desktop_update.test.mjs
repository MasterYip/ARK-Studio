import assert from "node:assert/strict";
import test from "node:test";
import { DISMISS_KEY, loadDismissed, saveDismissed, shouldRecheck, updateBanner } from "../../ark_studio/web/js/desktop-update-model.js";

const info = (extra = {}) => ({ current: "2.1.0", enabled: true, selfInstall: true, available: "2.2.0", ...extra });

test("a newer version shows the banner; installer builds install, portable builds download", () => {
  assert.deepEqual(updateBanner(info()), { version: "2.2.0", current: "2.1.0", action: "install" });
  assert.equal(updateBanner(info({ selfInstall: false })).action, "download");
  assert.equal(updateBanner(info({ available: "v2.2.0" })).version, "2.2.0");
});

test("no banner without an update, with the updater off, or for the running version", () => {
  assert.equal(updateBanner(null), null);
  assert.equal(updateBanner(info({ available: null })), null);
  assert.equal(updateBanner(info({ available: "" })), null);
  assert.equal(updateBanner(info({ enabled: false })), null);
  assert.equal(updateBanner(info({ available: "2.1.0" })), null);
});

test("Later hides that version only", () => {
  assert.equal(updateBanner(info(), "2.2.0"), null);
  assert.equal(updateBanner(info(), "v2.2.0"), null);
  assert.equal(updateBanner(info({ available: "2.3.0" }), "2.2.0").version, "2.3.0");
});

test("the dismissed version is stored per browser profile", () => {
  const store = new Map();
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  assert.equal(loadDismissed(storage), null);
  saveDismissed("2.2.0", storage);
  assert.equal(store.get(DISMISS_KEY), "2.2.0");
  assert.equal(loadDismissed(storage), "2.2.0");
  const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); } };
  assert.equal(loadDismissed(broken), null);
  saveDismissed("2.2.0", broken);
});

test("one later look while the start-up check may still be running", () => {
  assert.equal(shouldRecheck(info({ available: null }), 0), true);
  assert.equal(shouldRecheck(info({ available: null }), 1), false);
  assert.equal(shouldRecheck(info({ enabled: false, available: null }), 0), false);
  assert.equal(shouldRecheck(info(), 0), false);
  assert.equal(shouldRecheck(null, 0), false);
});
