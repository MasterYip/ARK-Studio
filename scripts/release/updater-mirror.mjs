#!/usr/bin/env node
// The updater's second endpoint: a fixed GitHub release with the tag
// `updater` whose only asset is a copy of the newest published latest.json.
// Installed apps ask `releases/latest/download/latest.json` first and this
// copy second (tauri.conf.json plugins.updater.endpoints), so a newer release
// published without update bundles (a patch release, a hand-made release)
// does not leave every update check at 404.
//
// The `updater` release is a pre-release: GitHub's `releases/latest` never
// resolves to a pre-release, so it cannot shadow the real latest release.
//
// .github/workflows/updater-manifest.yml runs this when a release is
// published (draft assets are not downloadable yet, so the copy waits for
// publishing):
//
//   node scripts/release/updater-mirror.mjs decide <tag> <prerelease> <release latest.json|-> <updater latest.json|->
//     prints "copy" or "skip: <reason>"
import { existsSync, readFileSync } from "node:fs";

export const UPDATER_TAG = "updater";

const SEMVER = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/;

/** Semver order of two plain versions (pre-releases sort before their release); NaN when unparsable. */
export function compareVersions(a, b) {
  const pa = SEMVER.exec(String(a || "").replace(/^v/, ""));
  const pb = SEMVER.exec(String(b || "").replace(/^v/, ""));
  if (!pa || !pb) return NaN;
  for (let i = 1; i <= 3; i++) {
    const d = Number(pa[i]) - Number(pb[i]);
    if (d) return Math.sign(d);
  }
  if (pa[4] === pb[4]) return 0;
  if (!pa[4]) return 1;
  if (!pb[4]) return -1;
  return pa[4] < pb[4] ? -1 : 1;
}

/**
 * Whether the published release's latest.json should replace the copy on
 * the `updater` release.
 * @param {{tag: string, prerelease: boolean, manifest: object|null, current: object|null}} input
 *   manifest: latest.json of the published release (null: it has none);
 *   current: latest.json now on the `updater` release (null: none yet).
 * @returns {{copy: boolean, reason: string}}
 */
export function decideMirror({ tag, prerelease, manifest, current }) {
  if (!/^v\d+\.\d+\.\d+$/.test(String(tag || ""))) return { copy: false, reason: `${tag || "(no tag)"} is not a vX.Y.Z release` };
  if (prerelease) return { copy: false, reason: `${tag} is a pre-release` };
  if (!manifest) return { copy: false, reason: `${tag} has no latest.json` };
  const version = String(manifest.version || "");
  if (version.replace(/^v/, "") !== tag.slice(1)) return { copy: false, reason: `latest.json says ${version || "(no version)"}, not ${tag.slice(1)}` };
  if (!manifest.platforms || !Object.keys(manifest.platforms).length) return { copy: false, reason: `latest.json of ${tag} lists no platforms` };
  const order = current?.version ? compareVersions(version, current.version) : 1;
  if (!(order > 0)) return { copy: false, reason: `the updater release already has ${current.version}` };
  return { copy: true, reason: `${current?.version || "nothing"} -> ${version}` };
}

function readManifest(path) {
  if (!path || path === "-" || !existsSync(path)) return null;
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
}

if (process.argv[1] && process.argv[1].endsWith("updater-mirror.mjs")) {
  const [cmd, tag, prerelease, manifestPath, currentPath] = process.argv.slice(2);
  if (cmd !== "decide" || !tag) {
    console.error("usage: updater-mirror.mjs decide <tag> <prerelease true|false> <release latest.json|-> <updater latest.json|->");
    process.exit(2);
  }
  const result = decideMirror({
    tag,
    prerelease: prerelease === "true",
    manifest: readManifest(manifestPath),
    current: readManifest(currentPath),
  });
  console.log(result.copy ? "copy" : `skip: ${result.reason}`);
  if (result.copy) console.error(`updater release: ${result.reason}`);
}
