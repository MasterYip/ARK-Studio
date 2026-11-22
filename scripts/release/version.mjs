#!/usr/bin/env node
// Single source of the release version: the repository's VERSION file.
//
//   node scripts/release/version.mjs            print the version
//   node scripts/release/version.mjs --check    exit 1 if a copy disagrees
//   node scripts/release/version.mjs --write    copy VERSION into every target
//   node scripts/release/version.mjs --set 2.1.0  update VERSION, then --write
//   node scripts/release/version.mjs --tag v2.1.0 exit 1 unless the tag matches
//
// Copies: ark_studio/desktop/package.json, ark_studio/desktop/src-tauri/Cargo.toml,
// ark_studio/desktop/src-tauri/tauri.conf.json, ark_studio/server/cmd/ark-server/release.go
// (Settings > About on server pages), and the frozen Python server's
// ark_studio/python/pyproject.toml and arkstudio/__init__.py when that folder is
// present (it is not in the public export). ark-server gets it at build time:
// go build -ldflags "-X main.version=<VERSION>".
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const SEMVER = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;

// Each target: file, regex whose group 1 is the version, replacement builder.
export const TARGETS = [
  ["ark_studio/python/pyproject.toml", /^version = "([^"]*)"/m, (v) => `version = "${v}"`],
  ["ark_studio/python/arkstudio/__init__.py", /^__version__ = "([^"]*)"/m, (v) => `__version__ = "${v}"`],
  ["ark_studio/desktop/package.json", /^ {2}"version": "([^"]*)"/m, (v) => `  "version": "${v}"`],
  ["ark_studio/desktop/src-tauri/Cargo.toml", /^version = "([^"]*)"/m, (v) => `version = "${v}"`],
  ["ark_studio/desktop/src-tauri/tauri.conf.json", /^ {2}"version": "([^"]*)"/m, (v) => `  "version": "${v}"`],
  ["ark_studio/server/cmd/ark-server/release.go", /^const release = "([^"]*)"/m, (v) => `const release = "${v}"`],
];

export function readVersion(base = root) {
  const v = readFileSync(join(base, "VERSION"), "utf8").trim();
  if (!SEMVER.test(v)) throw new Error(`VERSION is not semver: ${JSON.stringify(v)}`);
  return v;
}

// Legacy targets: checked and written only when the file exists.
const OPTIONAL = new Set(["ark_studio/python/pyproject.toml", "ark_studio/python/arkstudio/__init__.py"]);
const present = (base) => TARGETS.filter(([file]) => !OPTIONAL.has(file) || existsSync(join(base, file)));

export function mismatches(base = root) {
  const want = readVersion(base);
  const out = [];
  for (const [file, re] of present(base)) {
    const m = readFileSync(join(base, file), "utf8").match(re);
    if (!m) out.push(`${file}: no version field found`);
    else if (m[1] !== want) out.push(`${file}: ${m[1]} != VERSION ${want}`);
  }
  return out;
}

function write(base, v) {
  for (const [file, re, repl] of present(base)) {
    const path = join(base, file);
    const text = readFileSync(path, "utf8");
    if (!re.test(text)) throw new Error(`${file}: no version field found`);
    writeFileSync(path, text.replace(re, repl(v)));
  }
}

function main(argv) {
  const [cmd, arg] = argv;
  if (!cmd) { console.log(readVersion()); return 0; }
  if (cmd === "--check") {
    const bad = mismatches();
    for (const line of bad) console.error(line);
    return bad.length ? 1 : 0;
  }
  if (cmd === "--write") { write(root, readVersion()); return 0; }
  if (cmd === "--set") {
    if (!SEMVER.test(arg || "")) { console.error("usage: --set X.Y.Z"); return 2; }
    writeFileSync(join(root, "VERSION"), `${arg}\n`);
    write(root, arg);
    return 0;
  }
  if (cmd === "--tag") {
    const v = readVersion();
    if ((arg || "").replace(/^refs\/tags\//, "") !== `v${v}`) {
      console.error(`tag ${arg} does not match VERSION ${v} (expected v${v})`);
      return 1;
    }
    return 0;
  }
  console.error(`unknown option ${cmd}`);
  return 2;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  process.exitCode = main(process.argv.slice(2));
}
