import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import {
  LOOPBACK, MODES, bypassText, normalizeProxyUrl, parseBypass, platformNote, proxyConfig, proxyPatch, restartNeeded, sameRoute,
} from "../../ark_studio/desktop/ui/proxy-model.js";

test("three modes, system by default", () => {
  assert.deepEqual(MODES, ["system", "direct", "custom"]);
  assert.deepEqual(proxyConfig(undefined), { mode: "system", url: "", bypass: [] });
  assert.deepEqual(proxyConfig({ mode: "vpn", url: 3, bypass: "x" }), { mode: "system", url: "", bypass: [] });
  assert.equal(proxyConfig({ mode: "custom", url: "http://127.0.0.1:7890", bypass: ["a"] }).mode, "custom");
  assert.deepEqual(LOOPBACK, ["localhost", "127.0.0.1", "::1"]);
});

test("custom proxy addresses are checked and normalized", () => {
  assert.deepEqual(normalizeProxyUrl("http://127.0.0.1:7890"), { url: "http://127.0.0.1:7890" });
  assert.deepEqual(normalizeProxyUrl(" 127.0.0.1:7890 "), { url: "http://127.0.0.1:7890" });
  assert.deepEqual(normalizeProxyUrl("SOCKS5://127.0.0.1:1080/"), { url: "socks5://127.0.0.1:1080" });
  assert.deepEqual(normalizeProxyUrl("https://Proxy.Example.com"), { url: "https://proxy.example.com:443" });
  assert.deepEqual(normalizeProxyUrl("http://[::1]:8080"), { url: "http://[::1]:8080" });
  assert.equal(normalizeProxyUrl("").error, "proxy.err.empty");
  assert.equal(normalizeProxyUrl("ftp://127.0.0.1:21").error, "proxy.err.scheme");
  assert.equal(normalizeProxyUrl("socks4://127.0.0.1:1080").error, "proxy.err.scheme");
  assert.equal(normalizeProxyUrl("socks5://127.0.0.1").error, "proxy.err.port");
  assert.equal(normalizeProxyUrl("http://user:pw@127.0.0.1:7890").error, "proxy.err.credentials");
  assert.equal(normalizeProxyUrl("http://127.0.0.1:7890/path").error, "proxy.err.path");
  assert.equal(normalizeProxyUrl("http://127.0.0.1:7890?x=1").error, "proxy.err.path");
  assert.equal(normalizeProxyUrl("http://").error, "proxy.err.invalid");
  assert.equal(normalizeProxyUrl("http://127.0.0.1:99999").error, "proxy.err.invalid");
  assert.equal(normalizeProxyUrl("http://bad host:1").error, "proxy.err.invalid");
});

test("the direct-connection list", () => {
  assert.deepEqual(parseBypass(" 192.168.0.0/16, *.Corp.example.com;intranet\n.lan  10.0.0.1 "),
    { list: ["192.168.0.0/16", ".corp.example.com", "intranet", ".lan", "10.0.0.1"] });
  assert.deepEqual(parseBypass("a.example, a.example"), { list: ["a.example"] });
  assert.deepEqual(parseBypass(""), { list: [] });
  assert.deepEqual(parseBypass("ok.example, bad!host"), { error: "proxy.err.bypass", entry: "bad!host" });
  assert.equal(parseBypass("*").error, "proxy.err.bypass");
  assert.equal(parseBypass("..x").error, "proxy.err.bypass");
  assert.equal(parseBypass(Array.from({ length: 65 }, (_, i) => `h${i}`).join(",")).error, "proxy.err.bypass_many");
  assert.equal(bypassText(["a.example", "10.0.0.0/8"]), "a.example, 10.0.0.0/8");
  assert.equal(bypassText(null), "");
});

test("the save patch: only custom mode needs a valid address", () => {
  assert.deepEqual(proxyPatch({ mode: "custom", url: "127.0.0.1:7890", bypass: "a.example" }),
    { patch: { proxy: { mode: "custom", url: "http://127.0.0.1:7890", bypass: ["a.example"] } } });
  assert.deepEqual(proxyPatch({ mode: "custom", url: "", bypass: "" }), { error: "proxy.err.empty", field: "url" });
  assert.equal(proxyPatch({ mode: "custom", url: "h:1", bypass: "bad!" }).field, "bypass");
  // other modes never fail on the custom fields and keep what is valid
  assert.deepEqual(proxyPatch({ mode: "direct", url: "nonsense://", bypass: "ok.example" }),
    { patch: { proxy: { mode: "direct", url: "", bypass: ["ok.example"] } } });
  assert.deepEqual(proxyPatch({ mode: "system", url: "", bypass: "" }), { patch: { proxy: { mode: "system", url: "", bypass: [] } } });
  assert.equal(proxyPatch({ mode: "other", url: "", bypass: "" }).patch.proxy.mode, "system");
});

test("a changed route waits for a restart", () => {
  const sys = { mode: "system", url: "http://127.0.0.1:7890", bypass: [] };
  const custom = { mode: "custom", url: "http://127.0.0.1:7890", bypass: ["a.example"] };
  assert.equal(restartNeeded(sys, { mode: "system" }), false, "unused custom fields do not count");
  assert.equal(restartNeeded(custom, { mode: "system" }), true);
  assert.equal(restartNeeded(custom, { ...custom }), false);
  assert.equal(restartNeeded(custom, { ...custom, bypass: [] }), true);
  assert.equal(restartNeeded({ mode: "direct" }, { mode: "direct", url: "x" }), false);
  assert.equal(restartNeeded(custom, undefined), false);
  assert.equal(sameRoute(undefined, { mode: "system" }), true);
});

test("macOS explains that the window keeps the system settings", () => {
  assert.equal(platformNote("macos"), "proxy.note.macos");
  assert.equal(platformNote("windows"), "");
  assert.equal(platformNote("linux"), "");
});

test("every proxy string exists in both languages and the page has the section", () => {
  const i18n = readFileSync(new URL("../../ark_studio/desktop/ui/i18n.js", import.meta.url), "utf8");
  const zhAt = i18n.indexOf("const ZH");
  const keys = [...new Set([...readFileSync(new URL("../../ark_studio/desktop/ui/proxy-model.js", import.meta.url), "utf8")
    .matchAll(/"(proxy\.[a-z_.]+)"/g)].map((m) => m[1]))];
  const page = readFileSync(new URL("../../ark_studio/desktop/ui/index.html", import.meta.url), "utf8");
  const settings = readFileSync(new URL("../../ark_studio/desktop/ui/settings.js", import.meta.url), "utf8");
  keys.push(...[...page.matchAll(/data-i18n="(proxy\.[a-z_.]+)"/g)].map((m) => m[1]));
  keys.push(...[...settings.matchAll(/t\("(proxy\.[a-z_.]+)"/g)].map((m) => m[1]));
  assert.ok(keys.length > 20);
  for (const key of keys) {
    const en = i18n.indexOf(`"${key}":`);
    assert.ok(en > 0 && en < zhAt, `EN ${key}`);
    assert.ok(i18n.indexOf(`"${key}":`, zhAt) > zhAt, `ZH ${key}`);
  }
  assert.match(i18n, /"proxy\.system": "跟随系统"/);
  assert.match(i18n, /"proxy\.direct": "不使用代理"/);
  assert.match(i18n, /"proxy\.restart_needed": "重启 ARK Studio 后生效。"/);
  for (const value of ["system", "direct", "custom"]) assert.match(page, new RegExp(`name="proxy-mode" value="${value}"`));
  assert.match(page, /id="proxy-restart"/);
});

test("the web Settings › App links to Proxy only for apps that have it", async () => {
  const { appHasProxySetting, PROXY_SETTING_SINCE } = await import("../../ark_studio/web/js/desktop-update-model.js");
  assert.equal(PROXY_SETTING_SINCE, "2.1.8");
  assert.equal(appHasProxySetting("2.1.8"), true);
  assert.equal(appHasProxySetting("v2.2.0"), true);
  assert.equal(appHasProxySetting("2.1.7"), false);
  assert.equal(appHasProxySetting(""), false);
  assert.equal(appHasProxySetting(undefined), false);
});
