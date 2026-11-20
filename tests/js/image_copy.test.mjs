// Run: node --experimental-vm-modules --test tests/js/image_copy.test.mjs
// Image copy menu: the pure model (image-copy-model.js) and the copy path of
// image-copy.js with fetch, canvas, clipboard and the desktop bridge mocked.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const WEB = new URL("../../ark_studio/web/js/", import.meta.url);

async function loadModel(context = vm.createContext({ URL })) {
  const source = await readFile(new URL("image-copy-model.js", WEB), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "image-copy-model.js", context });
  await module.link(() => { throw new Error("image-copy-model must stay dependency-free"); });
  await module.evaluate();
  return module;
}

// image-copy.js with the real model and stubbed DOM-side imports.
async function loadCopy(globals = {}) {
  const context = vm.createContext({ URL, Blob, Promise, Uint8Array, Error, Object, ...globals });
  const model = await loadModel(context);
  const source = await readFile(new URL("image-copy.js", WEB), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "image-copy.js", context });
  await module.link(async (specifier) => {
    if (specifier === "./image-copy-model.js") return model;
    const stubs = {
      "./dom.js": { copyText: async () => true },
      "./i18n.js": { t: (key) => key },
      "./context-menu.js": { openContextMenu: () => {} },
    };
    const values = stubs[specifier];
    if (!values) throw new Error(`unexpected import ${specifier}`);
    return new vm.SyntheticModule(Object.keys(values), function () {
      for (const [name, value] of Object.entries(values)) this.setExport(name, value);
    }, { context });
  });
  await module.evaluate();
  return module.namespace;
}

// The pure model is also imported natively (same realm, so deepEqual works).
const m = await import(new URL("image-copy-model.js", WEB).href);

test("menu: copy image, copy link, open in new tab for an image", () => {
  const items = m.imageMenuItems({ src: "/api/media/3/raw", kind: "image" }, (key) => `T:${key}`);
  assert.deepEqual(items.map((item) => item.action), ["copy_image", "copy_link", "open_tab"]);
  assert.deepEqual(items.map((item) => item.label), ["T:media.copy_image", "T:media.copy_link", "T:media.open_tab"]);
  assert.equal(items[0].shortcut, "Ctrl+C");
  assert.equal(m.imageMenuItems("/a.png", undefined, { mac: true })[0].shortcut, "⌘C");
});

test("menu: videos and files get no image menu; data: images no link item", () => {
  assert.deepEqual(m.imageMenuItems({ src: "/v.mp4", kind: "video" }), []);
  assert.deepEqual(m.imageMenuItems({ url: "/report.pdf", kind: "pdf" }), []);
  assert.deepEqual(m.imageMenuItems(null), []);
  assert.deepEqual(m.imageMenuItems({ src: "" }), []);
  assert.deepEqual(m.imageMenuItems({ src: "data:image/png;base64,AAAA" }).map((i) => i.action), ["copy_image", "open_tab"]);
  // kind given but an image extension or is_image wins
  assert.equal(m.imageMediaInfo({ url: "/x/pic.webp?v=2", kind: "file" }).src, "/x/pic.webp?v=2");
  assert.equal(m.imageMediaInfo({ url: "/api/media/4/raw", kind: "upload", is_image: true }).src, "/api/media/4/raw");
  assert.equal(m.imageMediaInfo({ url: "/api/media/4/raw", is_image: false }), null);
});

test("absoluteMediaUrl resolves against the page and drops sign-in tokens when asked", () => {
  const base = "https://ark.test/app/index.html#/home";
  assert.equal(m.absoluteMediaUrl("/api/media/1/raw?ark_project=p", base), "https://ark.test/api/media/1/raw?ark_project=p");
  assert.equal(m.absoluteMediaUrl("api/media/1/raw", base), "https://ark.test/app/api/media/1/raw");
  assert.equal(
    m.absoluteMediaUrl("https://ark.test/api/media/1/raw?mobile_token=s3cret&ark_project=p", base, { stripSecrets: true }),
    "https://ark.test/api/media/1/raw?ark_project=p");
  assert.equal(m.absoluteMediaUrl("data:image/png;base64,AA", base), "data:image/png;base64,AA");
  assert.equal(m.absoluteMediaUrl("", base), "");
});

test("PNG conversion decision", () => {
  assert.equal(m.needsPngConversion("image/png"), false);
  assert.equal(m.needsPngConversion("IMAGE/PNG; charset=binary"), false);
  for (const type of ["image/jpeg", "image/webp", "image/gif", "image/svg+xml", "", "application/octet-stream"]) {
    assert.equal(m.needsPngConversion(type), true, type);
  }
  assert.equal(m.isSvgType("image/svg+xml;charset=utf-8"), true);
});

test("canvas size: natural size, default for size-less SVG, capped", () => {
  assert.deepEqual(m.pngCanvasSize(640, 480), { width: 640, height: 480 });
  assert.deepEqual(m.pngCanvasSize(0, 0), { width: 1024, height: 1024 });
  assert.deepEqual(m.pngCanvasSize(300, 0), { width: 300, height: 300 });
  assert.deepEqual(m.pngCanvasSize(20000, 10000), { width: 8192, height: 4096 });
});

test("copy shortcut: Ctrl+C / Cmd+C only when nothing else claims it", () => {
  const key = (over = {}) => ({ key: "c", ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, ...over });
  assert.equal(m.isCopyShortcut(key({ ctrlKey: true })), true);
  assert.equal(m.isCopyShortcut(key({ key: "C", ctrlKey: true })), true);
  assert.equal(m.isCopyShortcut(key({ metaKey: true })), false);
  assert.equal(m.isCopyShortcut(key({ metaKey: true }), { mac: true }), true);
  assert.equal(m.isCopyShortcut(key({ ctrlKey: true, shiftKey: true })), false);
  assert.equal(m.isCopyShortcut(key({ ctrlKey: true }), { hasSelection: true }), false);
  assert.equal(m.isCopyShortcut(key({ ctrlKey: true }), { inField: true }), false);
  assert.equal(m.isCopyShortcut(key({ key: "v", ctrlKey: true })), false);
});

test("copy error kinds", () => {
  assert.equal(m.copyErrorKind(Object.assign(new Error("x"), { name: "NotAllowedError" })), "denied");
  assert.equal(m.copyErrorKind({ code: "no_clipboard" }), "denied");
  assert.equal(m.copyErrorKind({ stage: "load" }), "load");
  assert.equal(m.copyErrorKind(new Error("boom")), "failed");
});

// ------------------------------------------------------------ copy path
const okFetch = (type, calls = []) => async (url, init) => {
  calls.push({ url, init });
  return { ok: true, status: 200, blob: async () => new Blob([new Uint8Array([1, 2, 3])], { type }) };
};

class FakeItem {
  constructor(items) { this.items = items; }
}

function fakeClipboard({ fail = null } = {}) {
  const written = [];
  return {
    written,
    async write(items) {
      for (const item of items) {
        for (const [type, value] of Object.entries(item.items)) written.push({ type, blob: await value });
      }
      if (fail) throw Object.assign(new Error(fail), { name: fail });
    },
  };
}

test("copy: PNG is written as is, fetched with credentials from the raw URL", async () => {
  const copy = await loadCopy();
  const calls = [];
  const clipboard = fakeClipboard();
  let converted = 0;
  const how = await copy.copyImageToClipboard("/api/media/9/raw", {
    fetchImpl: okFetch("image/png", calls), clipboard, ClipboardItemImpl: FakeItem,
    toPng: async () => { converted += 1; return new Blob([], { type: "image/png" }); }, desktop: null,
  });
  assert.equal(how, "clipboard");
  assert.equal(converted, 0);
  assert.equal(calls[0].url, "/api/media/9/raw");
  assert.equal(calls[0].init.credentials, "include");
  assert.equal(clipboard.written.length, 1);
  assert.equal(clipboard.written[0].type, "image/png");
  assert.equal(clipboard.written[0].blob.type, "image/png");
});

test("copy: JPEG / WebP / SVG go through the PNG conversion", async () => {
  const copy = await loadCopy();
  for (const type of ["image/jpeg", "image/webp", "image/svg+xml", "image/gif"]) {
    const clipboard = fakeClipboard();
    const seen = [];
    await copy.copyImageToClipboard("/x", {
      fetchImpl: okFetch(type), clipboard, ClipboardItemImpl: FakeItem, desktop: null,
      toPng: async (blob) => { seen.push(blob.type); return new Blob([new Uint8Array([9])], { type: "image/png" }); },
    });
    assert.deepEqual(seen, [type]);
    assert.equal(clipboard.written[0].blob.type, "image/png");
  }
});

test("copy: ClipboardItem gets a Promise<Blob> (user activation survives the fetch)", async () => {
  const copy = await loadCopy();
  let value = null;
  class SpyItem { constructor(items) { value = items["image/png"]; this.items = items; } }
  await copy.copyImageToClipboard("/x", {
    fetchImpl: okFetch("image/png"), clipboard: fakeClipboard(), ClipboardItemImpl: SpyItem, desktop: null,
  });
  assert.equal(typeof value?.then, "function");
});

test("copy: permission denied falls back to the desktop bridge, else rejects as denied", async () => {
  const copy = await loadCopy();
  const sent = [];
  const desktop = { copyImage: async (bytes) => { sent.push(bytes); } };
  const how = await copy.copyImageToClipboard("/x", {
    fetchImpl: okFetch("image/png"), clipboard: fakeClipboard({ fail: "NotAllowedError" }), ClipboardItemImpl: FakeItem, desktop,
  });
  assert.equal(how, "desktop");
  assert.equal(sent.length, 1);
  assert.deepEqual([...sent[0]], [1, 2, 3]);

  await assert.rejects(copy.copyImageToClipboard("/x", {
    fetchImpl: okFetch("image/png"), clipboard: fakeClipboard({ fail: "NotAllowedError" }), ClipboardItemImpl: FakeItem, desktop: null,
  }), (err) => m.copyErrorKind(err) === "denied");
});

test("copy: no async clipboard (http page in the desktop app) uses the bridge directly", async () => {
  const copy = await loadCopy();
  const sent = [];
  const how = await copy.copyImageToClipboard("/x", {
    fetchImpl: okFetch("image/png"), clipboard: undefined, ClipboardItemImpl: undefined,
    desktop: { copyImage: async (bytes) => { sent.push(bytes.length); } },
  });
  assert.equal(how, "desktop");
  assert.deepEqual(sent, [3]);
  await assert.rejects(copy.copyImageToClipboard("/x", {
    fetchImpl: okFetch("image/png"), clipboard: undefined, ClipboardItemImpl: undefined, desktop: null,
  }), (err) => m.copyErrorKind(err) === "denied");
});

test("copy: a failed fetch is reported as a load error, not a permission problem", async () => {
  const copy = await loadCopy();
  const desktopCalls = [];
  await assert.rejects(copy.copyImageToClipboard("/missing", {
    fetchImpl: async () => ({ ok: false, status: 404, blob: async () => new Blob([]) }),
    clipboard: fakeClipboard(), ClipboardItemImpl: FakeItem,
    desktop: { copyImage: async () => desktopCalls.push(1) },
  }), (err) => m.copyErrorKind(err) === "load");
  assert.deepEqual(desktopCalls, []);
});
