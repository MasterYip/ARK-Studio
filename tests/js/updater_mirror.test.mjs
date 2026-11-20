import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { UPDATER_TAG, compareVersions, decideMirror } from "../../scripts/release/updater-mirror.mjs";

const read = (path) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8");
const manifest = (version, platforms = { "windows-x86_64": { url: "u", signature: "s" } }) => ({ version, platforms });

test("the updater asks the latest release first, then the fixed updater release", () => {
  const conf = JSON.parse(read("ark_studio/desktop/src-tauri/tauri.conf.json"));
  assert.deepEqual(conf.plugins.updater.endpoints, [
    "https://github.com/MasterYip/ARK-Studio/releases/latest/download/latest.json",
    `https://github.com/MasterYip/ARK-Studio/releases/download/${UPDATER_TAG}/latest.json`,
  ]);
});

test("versions compare as semver", () => {
  assert.equal(compareVersions("2.1.0", "2.0.9"), 1);
  assert.equal(compareVersions("v2.10.0", "2.9.0"), 1);
  assert.equal(compareVersions("2.1.0", "2.1.0"), 0);
  assert.equal(compareVersions("2.1.0-rc.1", "2.1.0"), -1);
  assert.ok(Number.isNaN(compareVersions("2.1", "2.1.0")));
});

test("a published release with a newer latest.json replaces the copy", () => {
  assert.deepEqual(decideMirror({ tag: "v2.1.0", prerelease: false, manifest: manifest("2.1.0"), current: manifest("2.0.0") }).copy, true);
  assert.equal(decideMirror({ tag: "v2.1.0", prerelease: false, manifest: manifest("2.1.0"), current: null }).copy, true);
});

test("pre-releases, other tags, missing or mismatched manifests and older versions are skipped", () => {
  const skip = (input) => {
    const r = decideMirror(input);
    assert.equal(r.copy, false, JSON.stringify(input));
    return r.reason;
  };
  assert.match(skip({ tag: "v2.1.0-rc.1", prerelease: true, manifest: manifest("2.1.0-rc.1"), current: null }), /not a vX\.Y\.Z/);
  assert.match(skip({ tag: "v2.1.0", prerelease: true, manifest: manifest("2.1.0"), current: null }), /pre-release/);
  assert.match(skip({ tag: "updater", prerelease: true, manifest: manifest("2.1.0"), current: null }), /not a vX\.Y\.Z/);
  assert.match(skip({ tag: "v2.0.1", prerelease: false, manifest: null, current: manifest("2.0.0") }), /no latest\.json/);
  assert.match(skip({ tag: "v2.1.0", prerelease: false, manifest: manifest("2.0.9"), current: null }), /says 2\.0\.9/);
  assert.match(skip({ tag: "v2.1.0", prerelease: false, manifest: manifest("2.1.0", {}), current: null }), /no platforms/);
  assert.match(skip({ tag: "v2.0.5", prerelease: false, manifest: manifest("2.0.5"), current: manifest("2.1.0") }), /already has 2\.1\.0/);
  assert.match(skip({ tag: "v2.1.0", prerelease: false, manifest: manifest("2.1.0"), current: manifest("2.1.0") }), /already has/);
});

test("the workflow copies on publish into a pre-release that is never the latest release", () => {
  const wf = read(".github/workflows/updater-manifest.yml");
  assert.match(wf, /release:\s*\n\s*types: \[published\]/);
  assert.match(wf, /--prerelease --latest=false/);
  assert.match(wf, /updater-mirror\.mjs decide/);
  assert.match(wf, /gh release upload updater .* --clobber/);
  // release.yml itself must not touch the updater release (its release is still a draft)
  assert.doesNotMatch(read(".github/workflows/release.yml"), /gh release (create|upload) updater/);
});
