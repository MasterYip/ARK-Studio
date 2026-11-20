#!/usr/bin/env node
// Writes the updater's latest.json for a tagged release, once, after every
// desktop job finished (release.yml, job "checksums"). Each job signs its
// update bundles (`<bundle>.sig` next to the bundle in the merged artifacts);
// tauri-action uploaded the bundles to the draft release. One manifest from
// all of them avoids the jobs racing to edit a shared latest.json.
//
//   node scripts/release/updater-json.mjs <artifacts dir> <version> <tag> <owner/repo> <release assets.json> <out.json>
//
// <release assets.json>: `gh release view <tag> --json assets`. Bundles are
// matched to release assets by GitHub's file name (spaces become dots) and
// else by size (tauri-action names the macOS tarballs by architecture).
// Without any .sig (no updater key) nothing is written. With signatures, a
// missing platform is an error: an incomplete latest.json would leave those
// installs on the old version without a word.
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// Bundle file name (as the desktop jobs collect them) -> updater platform keys.
// The first key of each group is the plain `{os}-{arch}` entry the updater
// falls back to; Windows prefers NSIS (the installer the app ships as).
export const BUNDLES = [
  { re: /_x64-setup\.exe$/, keys: ["windows-x86_64-nsis", "windows-x86_64"] },
  { re: /_x64_[A-Za-z-]+\.msi$/, keys: ["windows-x86_64-msi"] },
  { re: /_macos-arm64\.app\.tar\.gz$/, keys: ["darwin-aarch64-app", "darwin-aarch64"] },
  { re: /_macos-x64\.app\.tar\.gz$/, keys: ["darwin-x86_64-app", "darwin-x86_64"] },
  { re: /_amd64\.AppImage$/, keys: ["linux-x86_64-appimage", "linux-x86_64"] },
  { re: /_amd64\.deb$/, keys: ["linux-x86_64-deb"] },
];

/** Every platform a release must cover once updates are signed. */
export const REQUIRED = [
  "windows-x86_64",
  "windows-x86_64-nsis",
  "windows-x86_64-msi",
  "darwin-aarch64",
  "darwin-x86_64",
  "linux-x86_64",
  "linux-x86_64-appimage",
  "linux-x86_64-deb",
];

/** GitHub's name for an uploaded file (as tauri-action computes it). */
export function ghName(name) {
  return name.trim().replace(/[^a-zA-Z0-9_-]/g, ".").replace(/\.\./g, ".");
}

/**
 * files: [{name, size, sig?}] (sig: the signature text of a bundle);
 * assets: [{name, size}] of the release. Returns {manifest, problems}.
 */
export function buildManifest({ files, assets, version, tag, repo, notes, date }) {
  const platforms = {};
  const problems = [];
  const signed = files.filter((f) => typeof f.sig === "string" && f.sig.trim());
  for (const f of signed) {
    const kind = BUNDLES.find((b) => b.re.test(f.name));
    if (!kind) continue;
    const byName = assets.find((a) => a.name === ghName(f.name));
    const bySize = assets.filter((a) => a.size === f.size);
    const asset = byName || (bySize.length === 1 ? bySize[0] : null);
    if (!asset) {
      problems.push(`${f.name}: no matching release asset`);
      continue;
    }
    const entry = {
      signature: f.sig.trim(),
      url: `https://github.com/${repo}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(asset.name)}`,
    };
    for (const key of kind.keys) {
      if (platforms[key]) problems.push(`${key}: two bundles (${f.name})`);
      platforms[key] = entry;
    }
  }
  if (signed.length) {
    for (const key of REQUIRED) if (!platforms[key]) problems.push(`${key}: no signed bundle`);
  }
  const manifest = { version, notes, pub_date: date, platforms };
  return { manifest, problems, signed: signed.length };
}

function readFiles(dir) {
  const names = readdirSync(dir);
  return names
    .filter((n) => !n.endsWith(".sig") && statSync(join(dir, n)).isFile())
    .map((name) => {
      const sig = names.includes(`${name}.sig`) ? readFileSync(join(dir, `${name}.sig`), "utf8") : undefined;
      return { name, size: statSync(join(dir, name)).size, sig };
    });
}

if (process.argv[1] && process.argv[1].endsWith("updater-json.mjs")) {
  const [dir, version, tag, repo, assetsFile, out] = process.argv.slice(2);
  if (!out) {
    console.error("usage: updater-json.mjs <artifacts dir> <version> <tag> <owner/repo> <assets.json> <out.json>");
    process.exit(2);
  }
  const assets = JSON.parse(readFileSync(assetsFile, "utf8")).assets || [];
  const { manifest, problems, signed } = buildManifest({
    files: readFiles(dir),
    assets,
    version,
    tag,
    repo,
    notes: `ARK Studio ${version}`,
    date: new Date().toISOString(),
  });
  if (!signed) {
    console.log("updater: no signed update bundles, latest.json not written");
    process.exit(0);
  }
  if (problems.length) {
    console.error(`latest.json is incomplete:\n  ${problems.join("\n  ")}`);
    process.exit(1);
  }
  writeFileSync(out, `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`${out}: ${Object.keys(manifest.platforms).sort().join(", ")}`);
}
