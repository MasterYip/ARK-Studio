import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { mismatches, readVersion, TARGETS } from "../../scripts/release/version.mjs";
import { overlay } from "../../scripts/release/ci-tauri-config.mjs";
import { buildManifest, ghName, REQUIRED } from "../../scripts/release/updater-json.mjs";

test("VERSION is the single source: every copy agrees", () => {
  assert.match(readVersion(), /^\d+\.\d+\.\d+/);
  assert.deepEqual(mismatches(), []);
  assert.ok(TARGETS.some(([file]) => file === "ark_studio/desktop/src-tauri/tauri.conf.json"));
});

test("tauri.conf.json bundles the ark-server sidecar and carries the updater public key", () => {
  const conf = JSON.parse(readFileSync(new URL("../../ark_studio/desktop/src-tauri/tauri.conf.json", import.meta.url), "utf8"));
  assert.deepEqual(conf.bundle.externalBin, ["binaries/ark-server"]);
  // update bundles are signed only in CI, with the private key (ci-tauri-config.mjs)
  assert.equal(conf.bundle.createUpdaterArtifacts, false);
  // a minisign public key, base64 of its two-line text
  assert.match(Buffer.from(conf.plugins.updater.pubkey, "base64").toString("utf8"), /^untrusted comment: minisign public key: [0-9A-F]+\n/);
  assert.deepEqual(conf.plugins.updater.endpoints, [
    "https://github.com/MasterYip/ARK-Studio/releases/latest/download/latest.json",
    "https://github.com/MasterYip/ARK-Studio/releases/download/updater/latest.json",
  ]);
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

test("latest.json: one manifest for every platform from the jobs' signatures", () => {
  const v = "2.1.0";
  const files = [
    { name: `ARK Studio_${v}_x64-setup.exe`, size: 11, sig: "sig-nsis\n" },
    { name: `ARK Studio_${v}_x64_en-US.msi`, size: 12, sig: "sig-msi" },
    { name: `ARK Studio_${v}_macos-arm64.app.tar.gz`, size: 13, sig: "sig-arm" },
    { name: `ARK Studio_${v}_macos-x64.app.tar.gz`, size: 14, sig: "sig-x64" },
    { name: `ARK Studio_${v}_amd64.AppImage`, size: 15, sig: "sig-appimage" },
    { name: `ARK Studio_${v}_amd64.deb`, size: 16, sig: "sig-deb" },
    // not update bundles: no signature, or no updater platform
    { name: `ark-server_${v}_amd64.deb`, size: 17 },
    { name: `ARK-Studio_${v}_x64-portable.zip`, size: 18 },
    { name: `ARK Studio_${v}_aarch64.dmg`, size: 19, sig: "stray" },
  ];
  // what tauri-action attached: GitHub names, macOS tarballs named by architecture
  const assets = [
    ...files.filter((f) => !f.name.includes(".app.tar.gz")).map((f) => ({ name: ghName(f.name), size: f.size })),
    { name: "ARK.Studio_aarch64.app.tar.gz", size: 13 },
    { name: "ARK.Studio_x64.app.tar.gz", size: 14 },
  ];
  const { manifest, problems } = buildManifest({ files, assets, version: v, tag: `v${v}`, repo: "o/r", notes: "n", date: "d" });
  assert.deepEqual(problems, []);
  assert.deepEqual(Object.keys(manifest.platforms).sort(), [...REQUIRED, "darwin-aarch64-app", "darwin-x86_64-app"].sort());
  const base = `https://github.com/o/r/releases/download/v${v}`;
  assert.deepEqual(manifest.platforms["windows-x86_64"], { signature: "sig-nsis", url: `${base}/ARK.Studio_${v}_x64-setup.exe` });
  assert.equal(manifest.platforms["windows-x86_64-msi"].url, `${base}/ARK.Studio_${v}_x64_en-US.msi`);
  assert.equal(manifest.platforms["darwin-aarch64"].url, `${base}/ARK.Studio_aarch64.app.tar.gz`);
  assert.equal(manifest.platforms["darwin-x86_64"].signature, "sig-x64");
  assert.equal(manifest.platforms["linux-x86_64"].url, `${base}/ARK.Studio_${v}_amd64.AppImage`);
  assert.equal(manifest.platforms["linux-x86_64-deb"].signature, "sig-deb");
  assert.equal(manifest.version, v);
});

test("latest.json: a missing platform fails, no key writes nothing", () => {
  const files = [{ name: "ARK Studio_2.1.0_x64-setup.exe", size: 1, sig: "s" }];
  const { problems } = buildManifest({ files, assets: [{ name: "ARK.Studio_2.1.0_x64-setup.exe", size: 1 }], version: "2.1.0", tag: "v2.1.0", repo: "o/r" });
  assert.ok(problems.some((p) => p.startsWith("darwin-aarch64:")));
  assert.ok(problems.some((p) => p.startsWith("linux-x86_64:")));
  const unmatched = buildManifest({ files, assets: [], version: "2.1.0", tag: "v2.1.0", repo: "o/r" });
  assert.ok(unmatched.problems.some((p) => p.includes("no matching release asset")));
  const unsigned = buildManifest({ files: [{ name: "ARK Studio_2.1.0_x64-setup.exe", size: 1 }], assets: [], version: "2.1.0", tag: "v2.1.0", repo: "o/r" });
  assert.deepEqual([unsigned.signed, unsigned.problems], [0, []]);
});
