// web/js/download-link.js: every download goes through saveDownload.
// Browsers keep their own download; ARK Studio 2.1.7+ saves through the
// app ("Save as" dialog, streamed, progress, Show in folder); older apps
// save through the page (showSaveFilePicker, else a blob link). The last
// tests scan the web app so no download button bypasses the helper.
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import test from "node:test";
import {
  desktopShell, dispositionName, downloadRoute, exportFileName, installDownloadLinks, nameFromUrl, saveDownload,
} from "../../ark_studio/web/js/download-link.js";

// ------------------------------------------------------------ a tiny DOM
class El {
  constructor(tag) {
    this.tagName = tag.toUpperCase(); this.children = []; this.attrs = {}; this.listeners = {};
    this.className = ""; this.textContent = ""; this.parent = null; this.clicked = 0;
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  append(...kids) { for (const k of kids) { k.parent = this; this.children.push(k); } }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this); this.parent = null; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  click() { this.clicked += 1; for (const fn of this.listeners.click || []) fn({}); }
  get buttons() { return this.children.filter((c) => c.tagName === "BUTTON"); }
}
function fakeDoc() {
  const root = new El("div");
  const body = new El("body");
  const listeners = {};
  return {
    root, body, listeners,
    getElementById: (id) => (id === "toast-root" ? root : null),
    createElement: (tag) => new El(tag),
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
  };
}
function fakeWin(desktop, extra = {}) {
  const listeners = {};
  return {
    __ARK_DESKTOP__: desktop, listeners, ...extra,
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) { listeners[type] = (listeners[type] || []).filter((f) => f !== fn); },
    emit(type, detail) { for (const fn of listeners[type] || []) fn({ detail }); },
  };
}
const card = (doc) => doc.root.children[0];
const tick = () => new Promise((r) => setTimeout(r, 0));

function streamResponse(chunks, { status = 200, headers = {} } = {}) {
  let i = 0;
  const h = new Map(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]));
  return {
    ok: status >= 200 && status < 300, status,
    headers: { get: (k) => h.get(k.toLowerCase()) ?? null },
    body: { getReader: () => ({ read: async () => (i < chunks.length ? { done: false, value: chunks[i++] } : { done: true }) }) },
    text: async () => new TextDecoder().decode(Buffer.concat(chunks.map((c) => Buffer.from(c)))),
  };
}

// ---------------------------------------------------------------- routes
test("the route follows the shell", () => {
  assert.equal(downloadRoute(fakeWin(undefined)), "browser");
  assert.equal(downloadRoute(fakeWin({ downloads: true })), "browser", "not the ARK shell");
  assert.equal(downloadRoute(fakeWin({ shell: "tauri", saveDownload() {} })), "native", "2.1.7+");
  assert.equal(downloadRoute(fakeWin({ shell: "tauri", downloads: true }, { showSaveFilePicker() {} })), "picker", "2.1.2-2.1.6");
  assert.equal(downloadRoute(fakeWin({ shell: "tauri" }, { showSaveFilePicker() {} })), "picker", "2.1.1");
  assert.equal(downloadRoute(fakeWin({ shell: "tauri" })), "blob");
  assert.equal(desktopShell(fakeWin(null)), null);
});

test("file names", () => {
  assert.equal(nameFromUrl("/api/agent-sessions/s1/artifacts/download?path=out%2Fplot.png"), "plot.png");
  assert.equal(nameFromUrl("https://h/files/Paper%20draft.pdf"), "Paper draft.pdf");
  assert.equal(nameFromUrl("/api/export?format=project"), "");
  assert.equal(nameFromUrl("blob:http://x/1"), "");
  assert.equal(dispositionName(`attachment; filename="ARK_Develop.ark.zip"; filename*=UTF-8''ARK_Develop.ark.zip`), "ARK_Develop.ark.zip");
  assert.equal(dispositionName("attachment; filename*=UTF-8''%E9%A1%B9%E7%9B%AE.zip"), "项目.zip");
  assert.equal(dispositionName('attachment; filename="a.json"'), "a.json");
  assert.equal(exportFileName("ARK_Develop", "project"), "ARK_Develop.ark.zip");
  assert.equal(exportFileName("项目 一", "project"), "ark-project.ark.zip");
  assert.equal(exportFileName("My Notes", "svg"), "My-Notes-map.svg");
});

test("browsers keep the normal download", async () => {
  const doc = fakeDoc();
  const got = await saveDownload("/api/export?format=project&sessions=1", { filename: "A.ark.zip", win: fakeWin(undefined), doc });
  assert.equal(got.state, "browser");
  assert.equal(doc.body.children.length, 0, "the link is removed again");
  assert.equal(doc.root.children.length, 0, "no app card in browsers");
});

test("2.1.7+: the app saves it; progress, Saved to ..., Show in folder", async () => {
  const doc = fakeDoc();
  const calls = [];
  let finish;
  const desktop = {
    shell: "tauri",
    saveDownload: (url, opts) => { calls.push([url, opts]); return new Promise((r) => { finish = r; }); },
    revealDownload: async (id) => { calls.push(["reveal", id]); },
    cancelDownload: async (t) => { calls.push(["cancel", t]); },
  };
  const win = fakeWin(desktop);
  for (const url of ["/api/export?format=project&project=p1", "/api/export?format=project&project=p1&sessions=1"]) {
    calls.length = 0;
    const p = saveDownload(url, { filename: "ARK_Develop.ark.zip", win, doc });
    await tick();
    assert.equal(calls[0][0], url, "the export URL as given (sessions on and off)");
    assert.equal(calls[0][1].name, "ARK_Develop.ark.zip");
    const ticket = calls[0][1].ticket;
    assert.ok(ticket);
    win.emit("ark-download-progress", { ticket: "other", state: "progress", received: 1, total: 2 });
    win.emit("ark-download-progress", { ticket, state: "progress", name: "ARK_Develop.ark.zip", received: 5 * 1048576, total: 10 * 1048576 });
    assert.match(card(doc).children[0].textContent, /Downloading ARK_Develop\.ark\.zip · 5\.0 MB of 10\.0 MB/);
    finish({ state: "saved", id: "3", name: "ARK_Develop.ark.zip", path: "D:\\dl\\ARK_Develop.ark.zip" });
    assert.deepEqual(await p, { state: "saved", name: "ARK_Develop.ark.zip", path: "D:\\dl\\ARK_Develop.ark.zip" });
    const c = card(doc);
    assert.equal(c.getAttribute("data-download-state"), "ok");
    assert.equal(c.children[0].textContent, "Saved to D:\\dl\\ARK_Develop.ark.zip");
    const reveal = c.buttons.find((b) => b.textContent === "Show in folder");
    reveal.click();
    assert.deepEqual(calls.at(-1), ["reveal", "3"]);
    c.remove();
  }
});

test("2.1.7+: dialog cancelled, page Cancel, server error", async () => {
  const doc = fakeDoc();
  let reply;
  const calls = [];
  const desktop = {
    shell: "tauri",
    saveDownload: (url, opts) => { calls.push(opts.ticket); return reply(); },
    cancelDownload: (t) => { calls.push(["cancel", t]); },
  };
  const win = fakeWin(desktop);
  reply = async () => ({ state: "cancelled" });
  assert.equal((await saveDownload("/x.zip", { win, doc })).state, "cancelled");
  assert.equal(doc.root.children.length, 0, "the dialog's Cancel leaves no card");

  let release;
  reply = () => new Promise((r) => { release = r; });
  const p = saveDownload("/api/export?format=project", { filename: "A.ark.zip", win, doc });
  await tick();
  card(doc).buttons.find((b) => b.textContent === "Cancel").click();
  assert.deepEqual(calls.at(-1), ["cancel", calls.at(-2)]);
  release({ state: "aborted", name: "A.ark.zip" });
  assert.equal((await p).state, "cancelled");
  assert.match(card(doc).children[0].textContent, /Download cancelled/);
  card(doc).remove();

  reply = async () => { throw "Project not found or not accessible for this account."; };
  const failed = await saveDownload("/api/export?format=project", { filename: "A.ark.zip", win, doc });
  assert.equal(failed.state, "failed");
  assert.equal(card(doc).getAttribute("data-download-state"), "err");
  assert.equal(card(doc).children[0].textContent, "Download failed: A.ark.zip (Project not found or not accessible for this account.)");
});

test("2.1.7+: page-made files (logs, prompt exports) go to the app as bytes", async () => {
  const doc = fakeDoc();
  const got = [];
  const desktop = { shell: "tauri", saveDownload() {}, saveBytes: async (bytes, name) => { got.push([new TextDecoder().decode(bytes), name]); return { state: "saved", id: "1", name, path: `C:\\x\\${name}` }; } };
  const r = await saveDownload(new Blob(["hello"]), { filename: "run.log", win: fakeWin(desktop), doc });
  assert.equal(r.state, "saved");
  assert.deepEqual(got, [["hello", "run.log"]]);
});

test("older apps: the system dialog opens from the click, the page streams the file", async () => {
  const doc = fakeDoc();
  const written = [];
  let pickerCalls = 0;
  const writable = { write: async (c) => { written.push(c); }, close: async () => { written.push("closed"); }, abort: async () => {} };
  const win = fakeWin({ shell: "tauri", downloads: true }, {
    showSaveFilePicker: (opts) => { pickerCalls += 1; assert.equal(opts.suggestedName, "ARK_Develop.ark.zip"); return Promise.resolve({ name: "ARK_Develop.ark.zip", createWritable: async () => writable }); },
  });
  const fetched = [];
  const fetchImpl = async (url, init) => { fetched.push([url, init.credentials]); return streamResponse([new Uint8Array(3), new Uint8Array(4)], { headers: { "Content-Length": "7" } }); };
  const p = saveDownload("/api/export?format=project&sessions=1", { filename: "ARK_Develop.ark.zip", win, doc, fetch: fetchImpl });
  assert.equal(pickerCalls, 1, "the picker opens before the first await (user activation)");
  const r = await p;
  assert.equal(r.state, "saved");
  assert.deepEqual(fetched, [["/api/export?format=project&sessions=1", "same-origin"]]);
  assert.equal(written.length, 3);
  assert.equal(written.at(-1), "closed");
  assert.equal(card(doc).children[0].textContent, "Saved to ARK_Develop.ark.zip");
});

test("older apps: a server error is shown, the half file is dropped", async () => {
  const doc = fakeDoc();
  let aborted = false;
  const win = fakeWin({ shell: "tauri" }, {
    showSaveFilePicker: async () => ({ name: "a.zip", createWritable: async () => ({ write: async () => {}, close: async () => {}, abort: async () => { aborted = true; } }) }),
  });
  const fetchImpl = async () => streamResponse([Buffer.from('{"error":"Sign in first"}')], { status: 401 });
  const r = await saveDownload("/api/export?format=project", { filename: "a.zip", win, doc, fetch: fetchImpl });
  assert.equal(r.state, "failed");
  assert.equal(r.error, "Sign in first");
  assert.ok(aborted);
  assert.equal(card(doc).getAttribute("data-download-state"), "err");
});

test("older apps: picker cancelled is silent; no picker means a blob link", async () => {
  const doc = fakeDoc();
  const cancelled = fakeWin({ shell: "tauri" }, { showSaveFilePicker: async () => { throw Object.assign(new Error("x"), { name: "AbortError" }); } });
  let fetched = 0;
  assert.equal((await saveDownload("/a.zip", { win: cancelled, doc, fetch: async () => { fetched += 1; } })).state, "cancelled");
  assert.equal(fetched, 0);
  assert.equal(doc.root.children.length, 0);

  const blobWin = fakeWin({ shell: "tauri" });
  globalThis.URL.createObjectURL ||= () => "blob:x";
  const r = await saveDownload("/api/export?format=project", { filename: "a.zip", win: blobWin, doc,
    fetch: async () => streamResponse([new Uint8Array(2)], { headers: { "Content-Disposition": 'attachment; filename="Real.ark.zip"' } }) });
  assert.equal(r.state, "saved");
  assert.equal(r.name, "Real.ark.zip", "the server's name");
  assert.equal(card(doc).children[0].textContent, "Saved to Downloads: Real.ark.zip");
});

test("in the app every <a download> click goes through saveDownload", async () => {
  const doc = fakeDoc();
  const seen = [];
  const win = fakeWin({ shell: "tauri", saveDownload: async (url, o) => { seen.push([url, o.name]); return { state: "cancelled" }; } });
  assert.equal(installDownloadLinks({ win, doc }), true);
  assert.equal(installDownloadLinks({ win, doc }), false, "once");
  const [onClick] = doc.listeners.click;
  const link = { href: "http://127.0.0.1:1/api/export?format=project", getAttribute: () => "Demo.ark.zip" };
  let prevented = false;
  onClick({ button: 0, target: { closest: (sel) => (sel === "a[download]" ? link : null) }, preventDefault() { prevented = true; } });
  await tick();
  assert.ok(prevented);
  assert.deepEqual(seen, [["http://127.0.0.1:1/api/export?format=project", "Demo.ark.zip"]]);
  onClick({ button: 0, ctrlKey: true, target: { closest: () => link }, preventDefault() { throw new Error("modifier clicks stay"); } });
  onClick({ button: 0, target: { closest: () => null }, preventDefault() { throw new Error("other clicks stay"); } });

  const browserDoc = fakeDoc();
  assert.equal(installDownloadLinks({ win: fakeWin(undefined), doc: browserDoc }), false);
  assert.equal(browserDoc.listeners.click, undefined, "browsers: untouched");
});

// ------------------------------------------------ every button, by source
const WEB = new URL("../../ark_studio/web/js/", import.meta.url);
function sources(dir = WEB, out = []) {
  for (const name of readdirSync(dir)) {
    const url = new URL(name, dir);
    if (statSync(url).isDirectory()) sources(new URL(`${name}/`, dir), out);
    else if (name.endsWith(".js")) out.push([url.pathname.split("/web/js/")[1], readFileSync(url, "utf8")]);
  }
  return out;
}
const ALL = sources();
const read = (rel) => ALL.find(([f]) => f === rel)[1];

test("no script starts a download behind the helper's back", () => {
  for (const [file, text] of ALL) {
    if (file === "download-link.js") continue;
    assert.doesNotMatch(text, /\.download\s*=/, `${file}: a script-made <a download>; use saveDownload`);
    assert.doesNotMatch(text, /location\.href\s*=\s*(?:exportUrl|bundleExportUrl|api\.\w*Url)/, `${file}: navigating to a download; use saveDownload`);
    assert.doesNotMatch(text, /window\.open\([^)]*(?:exportUrl|rawUrl|download)/i, `${file}: a download in a new window; use saveDownload`);
  }
});

test("the known download buttons use the helper or a plain <a download> the app catches", () => {
  const viaHelper = {
    "views/settings.js": /download: exportFileName\(project\.name, "project"\)/, // Settings > Data > Export project (<a download>)
    "app.js": /saveDownload\(exportUrl\(format/, // Export JSON / Markdown / CSV / SVG
    "export-menu.js": /saveDownload\(bundleExportUrl\(/, // Export as Markdown bundle
    "session-library.js": /saveDownload\(sessionContextApi\.rawUrl/, // transcript Download JSONL (menu)
    "dom.js": /return saveDownload\(blob/, // task log, prompt library export
  };
  for (const [file, re] of Object.entries(viaHelper)) assert.match(read(file), re, file);
  const anchors = {
    "session-library.js": 1, // Download JSONL (card)
    "session-workspace.js": 3, // artifact / image / file Download
    "views/editor.js": 2, // editor Download (toolbar, binary view)
  };
  for (const [file, n] of Object.entries(anchors)) {
    const count = (read(file).match(/h\("a", \{[^}]*\bdownload: /g) || []).length;
    assert.equal(count, n, `${file}: <a download> links (caught by installDownloadLinks)`);
  }
  assert.match(read("app.js"), /installDownloadLinks\(\);/, "the app installs the link handler at start");
  assert.match(read("app.js"), /setDownloadText\(t\);/);
});

test("the download strings exist in both languages", () => {
  const keys = ["preparing", "progress", "progress_total", "saved", "saved_downloads", "show_folder", "failed", "cancel", "cancelled"];
  for (const lang of ["en", "zh"]) {
    const text = read(`i18n-${lang}.js`);
    for (const k of keys) assert.match(text, new RegExp(`"download\\.${k}":`), `${lang}: download.${k}`);
  }
  assert.match(read("i18n-zh.js"), /"download\.show_folder": "打开文件夹"/);
  assert.match(read("i18n-zh.js"), /"download\.saved": "已保存到 \{path\}"/);
});
