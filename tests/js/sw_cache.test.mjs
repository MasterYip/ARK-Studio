import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";

function worker() {
  const handlers = {}, stores = new Map();
  const key = (r) => typeof r === "string" ? r : r.url;
  const caches = {
    async open(name) {
      if (!stores.has(name)) stores.set(name, new Map());
      const entries = stores.get(name);
      return {
        async match(r) { return entries.get(key(r))?.clone(); },
        async put(r, response) { entries.set(key(r), response.clone()); },
        async keys() { return [...entries.keys()]; },
        async delete(r) { return entries.delete(key(r)); },
      };
    },
    async match(r) { for (const entries of stores.values()) if (entries.has(key(r))) return entries.get(key(r)).clone(); },
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
  };
  let fetcher, calls = 0;
  const self = { location: new URL("https://ark.test/prefix/sw.js"),
    addEventListener(type, fn) { handlers[type] = fn; }, skipWaiting: async () => {}, clients: { claim: async () => {} } };
  vm.runInNewContext(fs.readFileSync(new URL("../../ark_studio/web/sw.js", import.meta.url), "utf8"), {
    self, URL, caches, Response, Headers, setTimeout, clearTimeout,
    fetch: (...args) => { calls++; return fetcher(...args); },
  });
  return {
    self, caches, stores, handlers,
    set fetch(fn) { fetcher = fn; }, get calls() { return calls; },
    async request(path, destination = "script", mode = "cors") {
      let response;
      const pending = [];
      handlers.fetch({ request: { url: new URL(path, self.location).href, method: "GET", destination, mode },
        respondWith(value) { response = value; }, waitUntil(value) { pending.push(value); } });
      const result = await response;
      await Promise.all(pending);
      return result;
    },
  };
}
const javascript = (text, immutable = true) => new Response(text, { headers: {
  "Content-Type": "text/javascript", "Cache-Control": immutable ? "private, max-age=31536000, immutable" : "no-store",
} });

test("versioned modules reuse exact cached releases and never mix offline versions", async () => {
  const w = worker();
  w.fetch = async () => javascript("release-a");
  assert.equal(await (await w.request("./js/app.js?v=a")).text(), "release-a");
  w.fetch = async () => { throw new Error("offline"); };
  assert.equal(await (await w.request("./js/app.js?v=a")).text(), "release-a");
  assert.equal(w.calls, 1);
  assert.equal((await w.request("./js/app.js?v=b")).type, "error");
  assert.equal(w.calls, 2);
});

test("a mismatched release URL is never retained under the old version", async () => {
  const w = worker();
  w.fetch = async () => javascript("current", false);
  await w.request("./js/app.js?v=old");
  await w.request("./js/app.js?v=old");
  assert.equal(w.calls, 2);
  assert.equal(await w.caches.match("https://ark.test/prefix/js/app.js?v=old"), undefined);
});

test("API/auth routes bypass shell caches; redirected login HTML is never stored", async () => {
  const w = worker();
  w.fetch = async () => { throw new Error("Must bypass worker"); };
  assert.equal(await w.request("./api/bootstrap"), undefined);
  assert.equal(await w.request("./login", "document", "navigate"), undefined);
  assert.equal(await w.request("./logout", "document", "navigate"), undefined);
  assert.equal(w.calls, 0);
  const login = new Response('<input name="csrf" value="secret">', { headers: { "Content-Type": "text/html" } });
  Object.defineProperty(login, "redirected", { value: true });
  w.fetch = async () => login;
  await w.request("./", "document", "navigate");
  assert.equal([...w.stores.values()].reduce((n, entries) => n + entries.size, 0), 0);
});

test("offline preparation uses the HTML build for every asset URL", async () => {
  const w = worker();
  const paths = [];
  w.fetch = async (url) => {
    paths.push(String(url));
    if (String(url).endsWith("index.html")) return new Response('<meta name="ark-build" content="release42">', { headers: { "Content-Type": "text/html" } });
    assert.equal(new URL(url).searchParams.get("v"), "release42");
    return javascript("asset");
  };
  let installed;
  w.handlers.install({ waitUntil(value) { installed = value; } });
  await installed;
  assert.equal(paths.length, w.self.ARK_SHELL_ASSETS.length - 1);
  assert.ok(await w.caches.match("https://ark.test/prefix/index.html"));
});

test("project images are cached per account, served without a request and dropped on sign-out", async () => {
  const w = worker();
  const send = (data) => {
    const pending = [];
    w.handlers.message({ data, waitUntil: (value) => pending.push(value) });
    return Promise.all(pending);
  };
  const image = () => new Response("png-bytes", { headers: {
    "Content-Type": "image/png", "Content-Length": "9", ETag: '"f1-9"' } });
  // No confirmed account: straight to the network, nothing stored.
  w.fetch = async () => image();
  await w.request("./api/media/7/raw?ark_project=p1", "image");
  assert.equal([...w.stores.keys()].some((name) => name.startsWith("ark-media-a")), false);
  await send({ type: "ark-media-identity", id: "acct1" });
  await w.request("./api/media/7/raw?ark_project=p1", "image");
  const calls = w.calls;
  w.fetch = async () => { throw new Error("must be served from the cache"); };
  assert.equal(await (await w.request("./api/media/7/raw?ark_project=p1", "image")).text(), "png-bytes");
  assert.equal(w.calls, calls, "a fresh cached image costs no request");
  // Downloads and other API calls are never cached.
  w.fetch = async () => image();
  await w.request("./api/media/7/raw?download=1", "image");
  assert.equal(w.stores.get("ark-media-acct1").size, 1);
  // Another account (or sign-out) removes the previous account's images.
  await send({ type: "ark-media-identity", id: "acct2" });
  assert.equal(w.stores.has("ark-media-acct1"), false);
  await send({ type: "ark-media-clear" });
  assert.equal([...w.stores.keys()].filter((name) => /^ark-media-acct/.test(name)).length, 0);
});

