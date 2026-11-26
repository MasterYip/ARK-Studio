// Pure JS tests for the mobile/PWA layer.  Run with:
//   node --test tests/js/mobile.test.mjs
//
// These tests never start a browser, touch the network or call the ARK server.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  ensureDeviceId,
  validDeviceId,
  MOBILE_BREAKPOINT,
  MOBILE_PRIMARY_TABS,
  MOBILE_SECONDARY_TABS,
  formatRemaining,
  isMobileViewport,
  isStandalone,
  mobileTabActive,
  pairingHash,
  pairingUrl,
  parsePairHash,
  pwaState,
  shouldCacheAsset,
  shouldCacheResponse,
  tabHash,
} from "../../ark_studio/web/js/mobile-core.js";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const WEB = path.join(ROOT, "ark_studio", "web");

test("mobile nav keeps six primary destinations and secondary desktop routes", () => {
  assert.equal(MOBILE_PRIMARY_TABS.length, 6);
  assert.deepEqual(MOBILE_PRIMARY_TABS.map((item) => item.id),
    ["home", "research", "map", "execute", "assistant", "settings"]);
  assert.deepEqual(MOBILE_SECONDARY_TABS.map((item) => item.id),
    ["editor", "plan", "production", "distribution"]);
  assert.equal(new Set([...MOBILE_PRIMARY_TABS, ...MOBILE_SECONDARY_TABS].map((item) => item.id)).size, 10);
  assert.equal(mobileTabActive("home", "home"), true);
  assert.equal(mobileTabActive("execute", "home"), false);
  assert.equal(tabHash("overview"), "#/overview");
  assert.equal(tabHash("plan", "method"), "#/plan/method");
});

test("mobile breakpoint covers 360-430 px phones and stays desktop above it", () => {
  assert.equal(MOBILE_BREAKPOINT, 760);
  for (const width of [360, 390, 430]) assert.equal(isMobileViewport(width), true);
  assert.equal(isMobileViewport(761), false);
  assert.equal(isMobileViewport(1440), false);
});

test("pairing URL keeps a path prefix and never injects localhost", () => {
  assert.equal(
    pairingUrl("https://ark.example.net/ark", "arkp_abc"),
    "https://ark.example.net/ark/#pair=arkp_abc");
  assert.equal(
    pairingUrl("http://192.168.1.20:8891/", "arkp_a b"),
    "http://192.168.1.20:8891/#pair=arkp_a%20b");
  assert.equal(pairingHash("arkp_abc"), "#pair=arkp_abc");
  assert.throws(() => pairingUrl("ftp://ark.example/", "x"));
  assert.throws(() => pairingUrl("https://user:pw@ark.example/", "x"));
  assert.throws(() => pairingUrl("https://ark.example/?x=1", "x"));
});

test("pairing hash parsing survives QR links and manual entry", () => {
  assert.equal(parsePairHash("#pair=arkp_abc"), "arkp_abc");
  assert.equal(parsePairHash("#/pair=arkp_abc"), "arkp_abc");
  assert.equal(parsePairHash("#/overview?pair=arkp_abc"), "arkp_abc");
  assert.equal(parsePairHash("#pair=arkp_a%20b"), "arkp_a b");
  assert.equal(parsePairHash("#/overview"), "");
  assert.equal(parsePairHash(""), "");
});

test("PWA state is honest about insecure LAN HTTP", () => {
  const insecure = pwaState({ secureContext: false, hostname: "192.168.1.20" });
  assert.equal(insecure.canInstall, false);
  assert.equal(insecure.canCache, false);
  assert.equal(insecure.reason, "insecure-context");

  const localhost = pwaState({ secureContext: false, hostname: "127.0.0.1" });
  assert.equal(localhost.canCache, true);

  const https = pwaState({ secureContext: true, hostname: "ark.example.net", serviceWorker: true });
  assert.equal(https.canInstall, true);
  assert.equal(https.canCache, true);
  assert.equal(https.reason, "ready");

  const installed = pwaState({ secureContext: true, hostname: "ark.example.net", standalone: true });
  assert.equal(installed.installed, true);
  assert.equal(installed.canInstall, false);
  assert.equal(isStandalone("standalone"), true);
  assert.equal(isStandalone("browser"), false);
});

test("service worker policy keeps API responses out of the cache", () => {
  for (const url of [
    "/api/bootstrap",
    "https://ark.example.net/ark/api/units?limit=2",
    "http://127.0.0.1:8890/api/media/1/raw",
    "api/agent-sessions/1/transcript",
  ]) {
    assert.equal(shouldCacheAsset(url), false, url);
  }
  for (const url of [
    "/css/app.css",
    "./js/app.js",
    "https://ark.example.net/ark/manifest.webmanifest",
    "/brand/icon-192.png",
    "/",
    "/vendor/katex/fonts/KaTeX_Main-Regular.woff2",
  ]) {
    assert.equal(shouldCacheAsset(url), true, url);
  }
  assert.equal(shouldCacheAsset("/api/bootstrap", "POST"), false);
  assert.equal(shouldCacheAsset("/css/app.css", "POST"), false);
  const jsonResponse = { ok: true, headers: { get: () => "application/json" } };
  const cssResponse = { ok: true, headers: { get: () => "text/css" } };
  assert.equal(shouldCacheResponse(jsonResponse), false);
  assert.equal(shouldCacheResponse(cssResponse), true);
  assert.equal(shouldCacheResponse({ ok: false, headers: { get: () => "text/css" } }), false);
});

test("expiry formatting matches the QR countdown", () => {
  assert.equal(formatRemaining(1000 + 599000, 1000), "9:59");
  assert.equal(formatRemaining(1000 + 61000, 1000), "1:01");
  assert.equal(formatRemaining(1000, 1000), "expired");
  assert.equal(formatRemaining("2026-01-01T00:10:00Z", Date.parse("2026-01-01T00:00:00Z")), "10:00");
  assert.equal(formatRemaining("not-a-date"), "—");
});

function shellAssetsFromServiceWorker() {
  const source = fs.readFileSync(path.join(WEB, "sw.js"), "utf8");
  const match = source.match(/const SHELL_ASSETS = \[([\s\S]*?)\];/);
  assert.ok(match, "SHELL_ASSETS must exist in sw.js");
  return [...match[1].matchAll(/"([^"]+)"/g)].map((entry) => entry[1]);
}

function walkJs(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) return walkJs(full);
    return entry.name.endsWith(".js") ? [full] : [];
  });
}

test("service worker precaches every app module and no API route", () => {
  const assets = shellAssetsFromServiceWorker();
  assert.ok(assets.includes("./index.html"));
  assert.ok(assets.includes("./css/mobile.css"));
  assert.ok(assets.includes("./manifest.webmanifest"));
  assert.ok(assets.includes("./js/mobile-core.js"));
  assert.ok(assets.includes("./js/mobile-ui.js"));
  assert.ok(assets.some((asset) => asset.includes("/api/")) === false);
  const missing = walkJs(path.join(WEB, "js"))
    .filter((file) => !file.endsWith(`${path.sep}package.json`))
    .map((file) => "./" + path.relative(WEB, file).split(path.sep).join("/"))
    .filter((relative) => !assets.includes(relative));
  assert.deepEqual(missing, [], `JS modules missing from sw.js: ${missing.join(", ")}`);
  for (const asset of assets) {
    const onDisk = path.join(WEB, asset.replace(/^\.\//, ""));
    assert.ok(fs.existsSync(onDisk), `missing shell asset: ${asset}`);
  }
});

test("manifest is relative, standalone and has install icons", () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(WEB, "manifest.webmanifest"), "utf8"));
  assert.equal(manifest.display, "standalone");
  assert.equal(manifest.scope, "./");
  assert.equal(manifest.start_url, "./#/overview");
  assert.equal(manifest.theme_color, "#0f141c");
  assert.ok(!JSON.stringify(manifest).includes("localhost"));
  const icons = new Map(manifest.icons.map((icon) => [icon.sizes + ":" + icon.purpose, icon.src]));
  assert.equal(icons.get("192x192:any"), "brand/icon-192.png");
  assert.equal(icons.get("512x512:any"), "brand/icon-512.png");
  assert.equal(icons.get("512x512:maskable"), "brand/icon-maskable-512.png");
  for (const icon of manifest.icons) {
    assert.ok(fs.existsSync(path.join(WEB, icon.src)), icon.src);
  }
});

test("HTML entry has PWA metadata, safe-area viewport and relative assets", () => {
  const html = fs.readFileSync(path.join(WEB, "index.html"), "utf8");
  assert.match(html, /name="viewport"[^>]*viewport-fit=cover/);
  assert.match(html, /name="theme-color"/);
  assert.match(html, /rel="manifest" href="\.\/manifest\.webmanifest"/);
  assert.match(html, /rel="apple-touch-icon"/);
  assert.match(html, /src="\.\/js\/app\.js"/);
  assert.match(html, /href="\.\/css\/mobile\.css"/);
  assert.doesNotMatch(html, /https?:\/\//);
  assert.doesNotMatch(html, /(?:src|href)="\/(?!\/)/);
});

test("service worker never contains an API cache branch", () => {
  const source = fs.readFileSync(path.join(WEB, "sw.js"), "utf8");
  assert.match(source, /isApiRequest\(url\)\) return;/);
  assert.doesNotMatch(source, /cache\.put\([^)]*api/i);
  assert.match(source, /CACHE_VERSION/);
});

test("a browser keeps one device id; the Android app's cookie wins", () => {
  const store = new Map();
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
  const doc = { cookie: "" };
  const cryptoImpl = { getRandomValues: (bytes) => { bytes.fill(7); return bytes; } };
  const first = ensureDeviceId({ storage, doc, cryptoImpl, secure: true });
  assert.ok(validDeviceId(first));
  assert.match(doc.cookie, /^ark_device=[A-Za-z0-9_-]+; Path=\/; Max-Age=\d+; SameSite=Lax; Secure$/);
  doc.cookie = "";
  assert.equal(ensureDeviceId({ storage, doc, cryptoImpl }), first, "the stored id survives a cleared cookie");
  const app = "a".repeat(64);
  doc.cookie = `other=1; ark_device=${app}`;
  assert.equal(ensureDeviceId({ storage, doc, cryptoImpl }), app);
  assert.equal(store.get("ark.device.id"), app);
  assert.ok(!validDeviceId("short") && !validDeviceId("has space in it 123456"));
});
