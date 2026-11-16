import assert from "node:assert/strict";
import test from "node:test";
import { DRAG_ATTR, desktopChrome, installDesktopShell } from "../../ark_studio/web/js/desktop-shell.js";

function fakeDoc(selectors) {
  const attrs = (init = {}) => {
    const map = new Map(Object.entries(init));
    return {
      map,
      setAttribute: (k, v) => map.set(k, String(v)),
      hasAttribute: (k) => map.has(k),
      getAttribute: (k) => (map.has(k) ? map.get(k) : null),
    };
  };
  const els = Object.fromEntries(selectors.map((s) => [s, attrs()]));
  return { documentElement: attrs(), querySelector: (s) => els[s] || null, els };
}

const desktop = (extra = {}) => ({ shell: "tauri", platform: "windows", chrome: "custom", window: {}, ...extra });

test("browsers (no desktop shell) are left alone", () => {
  const doc = fakeDoc(["#topbar"]);
  assert.equal(installDesktopShell(undefined, doc), "");
  assert.equal(installDesktopShell({ shell: "tauri" }, doc), ""); // no window API: older shell
  assert.equal(doc.documentElement.hasAttribute("data-desktop-shell"), false);
  assert.equal(doc.els["#topbar"].hasAttribute(DRAG_ATTR), false);
});

test("desktop app: top bar and startup screen become deep drag regions", () => {
  const doc = fakeDoc(["#topbar", "#ark-startup"]);
  assert.equal(installDesktopShell(desktop(), doc), "custom");
  assert.equal(doc.documentElement.getAttribute("data-desktop-shell"), "custom");
  assert.equal(doc.els["#topbar"].getAttribute(DRAG_ATTR), "deep");
  assert.equal(doc.els["#ark-startup"].getAttribute(DRAG_ATTR), "deep");
});

test("an explicit drag-region choice in the page wins", () => {
  const doc = fakeDoc(["#topbar"]);
  doc.els["#topbar"].setAttribute(DRAG_ATTR, "false");
  installDesktopShell(desktop(), doc);
  assert.equal(doc.els["#topbar"].getAttribute(DRAG_ATTR), "false");
});

test("macOS uses the native traffic lights (overlay)", () => {
  assert.equal(desktopChrome(desktop({ platform: "macos", chrome: "overlay" })), "overlay");
  assert.equal(desktopChrome(desktop({ chrome: "anything" })), "custom");
  assert.equal(desktopChrome(null), "");
});
