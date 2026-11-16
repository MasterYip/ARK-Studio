import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { mismatches, readVersion, TARGETS } from "../../scripts/release/version.mjs";
import { overlay } from "../../scripts/release/ci-tauri-config.mjs";

test("VERSION is the single source: every copy agrees", () => {
  assert.match(readVersion(), /^\d+\.\d+\.\d+/);
  assert.deepEqual(mismatches(), []);
  assert.ok(TARGETS.some(([file]) => file === "ark_studio/desktop/src-tauri/tauri.conf.json"));
});

test("tauri.conf.json bundles the ark-server sidecar and keeps the updater off by default", () => {
  const conf = JSON.parse(readFileSync(new URL("../../ark_studio/desktop/src-tauri/tauri.conf.json", import.meta.url), "utf8"));
  assert.deepEqual(conf.bundle.externalBin, ["binaries/ark-server"]);
  assert.equal(conf.bundle.createUpdaterArtifacts, false);
  assert.equal(conf.plugins.updater.pubkey, "");
  for (const t of ["nsis", "msi", "deb", "appimage", "app", "dmg"]) assert.ok(conf.bundle.targets.includes(t), t);
});

test("CI overlay: unsigned builds stay unsigned, macOS gets an ad-hoc signature", () => {
  assert.deepEqual(overlay({}, "x86_64-pc-windows-msvc"), { bundle: {} });
  assert.deepEqual(overlay({}, "x86_64-unknown-linux-gnu"), { bundle: {} });
  assert.deepEqual(overlay({}, "aarch64-apple-darwin"), { bundle: { macOS: { signingIdentity: "-" } } });
  // empty strings (absent GitHub secrets) count as absent
  assert.deepEqual(overlay({ ARK_UPDATER_PUBKEY: "", TAURI_SIGNING_PRIVATE_KEY: "", WINDOWS_CERT_THUMBPRINT: " " }, "x86_64-pc-windows-msvc"), { bundle: {} });
});

test("CI overlay: updater needs both keys; Windows prefers Trusted Signing over a PFX", () => {
  assert.deepEqual(overlay({ ARK_UPDATER_PUBKEY: "pub" }, "x86_64-unknown-linux-gnu"), { bundle: {} });
  const up = overlay({ ARK_UPDATER_PUBKEY: " pub ", TAURI_SIGNING_PRIVATE_KEY: "priv" }, "x86_64-unknown-linux-gnu");
  assert.equal(up.bundle.createUpdaterArtifacts, true);
  assert.equal(up.plugins.updater.pubkey, "pub");
  const pfx = overlay({ WINDOWS_CERT_THUMBPRINT: "ABC" }, "x86_64-pc-windows-msvc");
  assert.equal(pfx.bundle.windows.certificateThumbprint, "ABC");
  const azure = overlay({ WINDOWS_CERT_THUMBPRINT: "ABC", AZURE_TS_ENDPOINT: "https://e", AZURE_TS_ACCOUNT: "a", AZURE_TS_PROFILE: "p" }, "x86_64-pc-windows-msvc");
  assert.match(azure.bundle.windows.signCommand, /^trusted-signing-cli -e https:\/\/e -a a -c p /);
  assert.equal(overlay({ APPLE_SIGNING_IDENTITY: "Developer ID Application: X (T)" }, "x86_64-apple-darwin").bundle.macOS.signingIdentity,
    "Developer ID Application: X (T)");
});

import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DEFAULT_PATHS, findHosts, forbiddenSet, localConfigHosts, scan } from "../../scripts/release/check-hosts.mjs";

test("check-hosts: shipped sources contain no forbidden server address", () => {
  const root = new URL("../../", import.meta.url);
  const paths = DEFAULT_PATHS.map((p) => new URL(p, root).pathname.replace(/^\/([A-Za-z]:)/, "$1"));
  assert.deepEqual(scan(paths, forbiddenSet(localConfigHosts())), []);
});

test("check-hosts: finds hosts and subdomains, ignores examples and loopback", () => {
  const forbidden = forbiddenSet(["ark.private-host.net", "203.0.113.77"]);
  assert.deepEqual(findHosts('url: "https://ark.private-host.net/x"', forbidden), ["ark.private-host.net"]);
  assert.deepEqual(findHosts("http://203.0.113.77:8890", forbidden), ["203.0.113.77"]);
  assert.deepEqual(findHosts("placeholder https://ark.example.com 127.0.0.1 localhost", forbiddenSet(["ark.example.com"])), []);
  // a parent domain on the list also catches its subdomains
  assert.deepEqual(findHosts("a.b.private-host.net", forbiddenSet(["private-host.net"])), ["a.b.private-host.net"]);
});

test("check-hosts: the developer's ~/.ark/desktop.json servers are forbidden too", () => {
  const home = mkdtempSync(join(tmpdir(), "ark-home-"));
  mkdirSync(join(home, ".ark"));
  writeFileSync(join(home, ".ark", "desktop.json"), JSON.stringify({
    remote_url: "https://ark.dev-box.org/", launcher_servers: ["https://lab.dev-box.org:8443"],
    local_open_origins: ["http://127.0.0.1:8890"], launcher_root: "E:/x",
  }));
  assert.deepEqual(localConfigHosts(home).sort(), ["ark.dev-box.org", "lab.dev-box.org"]);
  assert.deepEqual(localConfigHosts(join(home, "missing")), []);
});
