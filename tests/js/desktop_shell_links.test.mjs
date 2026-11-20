// The desktop shell's link rule (ark_studio/desktop/src-tauri/src/shell.js):
// which plain link clicks leave the ARK window for the system browser.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("../../ark_studio/desktop/src-tauri/src/shell.js", import.meta.url), "utf8");
const block = source.match(/\/\/ link-target:begin\r?\n([\s\S]*?)\/\/ link-target:end/);
assert.ok(block, "shell.js keeps externalTarget between link-target markers");
const externalTarget = new Function(`${block[1]}; return externalTarget;`)();

const PAGE = "https://ark.example.com/#/tasks";
const LOCAL = "http://127.0.0.1:8891/#/home";

test("ARK's own routes stay in the window", () => {
  assert.equal(externalTarget("#/join/abc", PAGE), "");
  assert.equal(externalTarget("./#/units/TASK-12", PAGE), "");
  assert.equal(externalTarget("/api/media/3/raw", LOCAL), "");
  assert.equal(externalTarget("https://ark.example.com/login", PAGE), "");
  assert.equal(externalTarget("https://ARK.example.com:443/x", PAGE), "");
});

test("other sites and other ARK servers go to the browser", () => {
  assert.equal(externalTarget("https://github.com/MasterYip/ARK-Studio", PAGE), "https://github.com/MasterYip/ARK-Studio");
  assert.equal(externalTarget("http://ark.example.com/", PAGE), "http://ark.example.com/");
  assert.equal(externalTarget("https://ark.example.com:8443/#/home", PAGE), "https://ark.example.com:8443/#/home");
  assert.equal(externalTarget("https://ark.example.com/", LOCAL), "https://ark.example.com/");
  assert.equal(externalTarget("//cdn.example.org/a.pdf", PAGE), "https://cdn.example.org/a.pdf");
  assert.equal(externalTarget("mailto:a@example.com", PAGE), "mailto:a@example.com");
});

test("named targets are left to on_new_window, _self/_top follow the rule", () => {
  assert.equal(externalTarget("https://github.com/", PAGE, "_blank"), "");
  assert.equal(externalTarget("https://github.com/", PAGE, "preview"), "");
  assert.equal(externalTarget("https://github.com/", PAGE, "_self"), "https://github.com/");
  assert.equal(externalTarget("https://github.com/", PAGE, "_TOP"), "https://github.com/");
});

test("other schemes are never opened", () => {
  for (const href of ["javascript:alert(1)", "data:text/html,x", "file:///E:/tasks/x", "vscode://file/E:/x",
    "intent://x#Intent;end", "blob:https://ark.example.com/1", "ftp://example.com/", "", "http://"]) {
    assert.equal(externalTarget(href, PAGE), "", href);
  }
});
