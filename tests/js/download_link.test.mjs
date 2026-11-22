// web/js/download-link.js: script-started downloads take the app's save path
// inside ARK Studio (window.__ARK_DESKTOP__.downloads) and the browser path
// (a new tab, unchanged) everywhere else.
import assert from "node:assert/strict";
import test from "node:test";
import { appSavesDownloads, openDownload } from "../../ark_studio/web/js/download-link.js";

function fakePage(desktop) {
  const calls = { opened: [], clicked: [], appended: 0, removed: 0 };
  const doc = {
    createElement(tag) {
      assert.equal(tag, "a");
      const el = {
        href: "", download: null, rel: "",
        click() { calls.clicked.push({ href: el.href, download: el.download, rel: el.rel }); },
        remove() { calls.removed += 1; },
      };
      return el;
    },
    body: { append() { calls.appended += 1; } },
  };
  const win = { __ARK_DESKTOP__: desktop, open(url, target) { calls.opened.push([url, target]); } };
  return { calls, doc, win };
}

test("only an app that saves downloads takes the app path", () => {
  assert.equal(appSavesDownloads({ shell: "tauri", downloads: true }), true);
  assert.equal(appSavesDownloads({ shell: "tauri" }), false, "ARK Studio 2.1.1 and older");
  assert.equal(appSavesDownloads({ downloads: "yes" }), false);
  assert.equal(appSavesDownloads(undefined), false);
  assert.equal(appSavesDownloads(null), false);
});

test("in the app: an <a download> click the app saves", () => {
  const { calls, doc, win } = fakePage({ shell: "tauri", downloads: true });
  const mode = openDownload("http://127.0.0.1:18890/api/session-context/s1/raw", { filename: "s1.jsonl", win, doc });
  assert.equal(mode, "app");
  assert.deepEqual(calls.clicked, [{ href: "http://127.0.0.1:18890/api/session-context/s1/raw", download: "s1.jsonl", rel: "noopener" }]);
  assert.equal(calls.appended, 1);
  assert.equal(calls.removed, 1);
  assert.deepEqual(calls.opened, []);
});

test("in a browser (and older apps): a new tab, as before", () => {
  for (const desktop of [undefined, { shell: "tauri", hub: {} }]) {
    const { calls, doc, win } = fakePage(desktop);
    assert.equal(openDownload("https://ark.example.com/api/x/raw", { filename: "x.jsonl", win, doc }), "tab");
    assert.deepEqual(calls.opened, [["https://ark.example.com/api/x/raw", "_blank"]]);
    assert.deepEqual(calls.clicked, []);
  }
});
