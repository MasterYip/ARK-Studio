// Downloads in the desktop app (ark_studio/desktop/src-tauri/src/shell.js and
// downloads.rs): the app saves every page download to the Downloads folder
// and the shell shows each step. Browsers have no shell and keep their own
// download handling; the web app's download links stay plain <a download>.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const shell = readFileSync(new URL("../../ark_studio/desktop/src-tauri/src/shell.js", import.meta.url), "utf8");
const lib = readFileSync(new URL("../../ark_studio/desktop/src-tauri/src/lib.rs", import.meta.url), "utf8");
const build = readFileSync(new URL("../../ark_studio/desktop/src-tauri/build.rs", import.meta.url), "utf8");
const block = shell.match(/\/\/ download-notice:begin\r?\n([\s\S]*?)\/\/ download-notice:end/);
assert.ok(block, "shell.js keeps downloadNotice between download-notice markers");
const downloadNotice = new Function(`${block[1]}; return downloadNotice;`)();

test("a started download shows its name without actions", () => {
  assert.deepEqual(downloadNotice({ state: "started", name: "Demo-2026-11-22.zip" }, false),
    { text: "Downloading Demo-2026-11-22.zip", reveal: false, error: false, close: false });
  assert.equal(downloadNotice({ state: "started", name: "a.zip" }, true).text, "正在下载 a.zip");
});

test("a saved download offers Show in folder", () => {
  const view = downloadNotice({ state: "done", name: "a.zip" }, false);
  assert.equal(view.text, "Saved to Downloads: a.zip");
  assert.equal(view.reveal, true);
  assert.equal(view.error, false);
  assert.equal(downloadNotice({ state: "done", name: "a.zip" }, true).text, "已保存到“下载”文件夹：a.zip");
});

test("a failed download (401, 500, network) is an error, never silence", () => {
  for (const ev of [{ state: "failed", name: "a.zip" }, { state: "unknown" }, {}, null]) {
    const view = downloadNotice(ev, false);
    assert.equal(view.error, true);
    assert.equal(view.reveal, false);
    assert.match(view.text, /^Download failed: /);
  }
  assert.equal(downloadNotice({ state: "failed" }, true).text, "下载失败：文件");
});

test("the app routes every window's downloads through its handler", () => {
  assert.match(lib, /fn base_builder[\s\S]*?\.on_download\(/, "base_builder (main and settings windows) sets on_download");
  assert.match(lib, /window\.__ARK_DOWNLOAD_EVENT__/, "the handler reports to the shell");
  assert.match(shell, /defineProperty\(window, "__ARK_DOWNLOAD_EVENT__"/);
  // Pages reveal a finished download by id; the command is registered and granted.
  assert.match(build, /"show_download"/);
  assert.match(lib, /"allow-show-download"/);
  assert.match(lib, /generate_handler!\[[\s\S]*show_download,/);
});

test("the shell's link rule still leaves download links to the webview", () => {
  assert.match(shell, /a\.hasAttribute\("download"\)\) return;/);
});

test("2.1.7: pages save through the app's Save as dialog", () => {
  for (const cmd of ["save_download", "save_bytes", "cancel_download"]) {
    assert.match(build, new RegExp(`"${cmd}"`), `build.rs declares ${cmd}`);
    assert.match(lib, new RegExp(`"allow-${cmd.replace(/_/g, "-")}"`), `server pages may call ${cmd}`);
    assert.match(lib, new RegExp(`generate_handler!\\[[\\s\\S]*${cmd},`), `${cmd} is registered`);
  }
  assert.match(shell, /saveDownload: function \(url, opts\)/);
  assert.match(shell, /invoke\("save_bytes", bytes, \{ headers:/);
  // Progress of a page's own download goes back to the page, not a shell card.
  assert.match(shell, /if \(ev\.ticket\) \{\s*window\.dispatchEvent\(new CustomEvent\("ark-download-progress"/);
  // Only files of the page's own server, with its cookies.
  assert.match(lib, /fn save_download[\s\S]*?same_origin\(&page, &target\)[\s\S]*?cookies_for_url/);
});
