#!/usr/bin/env node
// Release check: no real server address in what we ship.
//
//   node scripts/release/check-hosts.mjs [path ...]
//
// Scans files (text or binary; directories recursively) for host names and
// IPv4 addresses and fails if any is one of ours. Default paths: everything
// that ends up in a bundle (desktop UI and shell, tauri config, ark_studio/web/ which is
// embedded in ark-server, the Go sources). CI also runs it on the built
// ark-studio and ark-server binaries.
//
// Forbidden hosts are kept as SHA-256 hashes so the repository never spells
// them out; a subdomain of a forbidden domain counts too. Extra hosts:
//   * ARK_FORBIDDEN_HOSTS="a.example,b.example" (comma separated);
//   * the server addresses in the developer's ~/.ark/desktop.json (remote URL,
//     server list, local-open origins), when that file exists.
// Loopback, private and documentation addresses (example.com, 127.x, ...) are
// never reported. Defaults must use examples such as https://ark.example.com.
import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

export const FORBIDDEN_SHA256 = new Set([
  "2d2d9e74c170017557f9f380c4db559be90cfff25163cc7171769d24db5bf71e", // production domain
  "dbc8e4bc9b8c2f075dfc46a29749e3c85e8a78480bbb0d14e545dde51583d9f4", // production server IPv4
  "1dd7374c30bc17c46c3c552333ea37248ec00c9847de2acef426a80dd90de265", // quick-tunnel domain
]);

export const DEFAULT_PATHS = [
  "ark_studio/desktop/ui",
  "ark_studio/desktop/src-tauri/src",
  "ark_studio/desktop/src-tauri/capabilities",
  "ark_studio/desktop/src-tauri/tauri.conf.json",
  "ark_studio/desktop/src-tauri/windows",
  "ark_studio/web",
  "ark_studio/server",
];

const SKIP_DIRS = new Set(["node_modules", "target", ".git", "gen", "dist", "binaries"]);
const sha = (s) => createHash("sha256").update(s).digest("hex");
const HOST_RE = /(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}|\b(?:\d{1,3}\.){3}\d{1,3}\b/gi;

function harmless(host) {
  return host === "localhost" || /(^|\.)(example\.(com|org|net)|localhost|test|invalid)$/.test(host)
    || /^(127|10|0)\./.test(host) || /^192\.168\./.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host);
}

/** Hosts named in ~/.ark/desktop.json (the developer's own servers). */
export function localConfigHosts(home = homedir()) {
  const file = join(home, ".ark", "desktop.json");
  if (!existsSync(file)) return [];
  let data;
  try {
    data = JSON.parse(readFileSync(file, "utf8"));
  } catch {
    return [];
  }
  const out = new Set();
  const visit = (v) => {
    if (typeof v === "string") {
      try {
        const host = new URL(v.includes("://") ? v : `https://${v}`).hostname.toLowerCase();
        if (host.includes(".") && !harmless(host)) out.add(host);
      } catch { /* not an address */ }
    } else if (Array.isArray(v)) v.forEach(visit);
    else if (v && typeof v === "object") Object.values(v).forEach(visit);
  };
  for (const key of ["remote_url", "launcher_servers", "local_open_origins", "remoteUrl", "remoteHistory"]) visit(data[key]);
  return [...out];
}

export function forbiddenSet(extra = []) {
  const set = new Set(FORBIDDEN_SHA256);
  for (const h of extra) if (h) set.add(sha(h.trim().toLowerCase()));
  return set;
}

/** Forbidden hosts found in one text (a host or any parent domain matches). */
export function findHosts(text, forbidden) {
  const hits = new Set();
  for (const m of text.matchAll(HOST_RE)) {
    const host = m[0].toLowerCase();
    if (harmless(host)) continue;
    const labels = host.split(".");
    for (let i = 0; i < labels.length - 1; i++) {
      const candidate = labels.slice(i).join(".");
      if (forbidden.has(sha(candidate))) {
        hits.add(host);
        break;
      }
    }
  }
  return [...hits];
}

function* files(path) {
  const st = statSync(path);
  if (st.isFile()) {
    yield path;
    return;
  }
  for (const name of readdirSync(path)) {
    if (SKIP_DIRS.has(name)) continue;
    yield* files(join(path, name));
  }
}

export function scan(paths, forbidden) {
  const out = [];
  for (const p of paths) {
    if (!existsSync(p)) continue;
    for (const f of files(p)) {
      const hits = findHosts(readFileSync(f).toString("latin1"), forbidden);
      for (const h of hits) out.push({ file: f, host: h });
    }
  }
  return out;
}

if (process.argv[1] && process.argv[1].endsWith("check-hosts.mjs")) {
  const args = process.argv.slice(2);
  const paths = args.length ? args : DEFAULT_PATHS.map((p) => join(root, p));
  const extra = [...(process.env.ARK_FORBIDDEN_HOSTS || "").split(","), ...localConfigHosts()];
  const hits = scan(paths, forbiddenSet(extra));
  for (const { file, host } of hits) {
    // Print where, not what: the host itself may be private.
    console.error(`${relative(root, file) || file}: forbidden server address (${host.length} chars, sha256 ${sha(host).slice(0, 12)})`);
  }
  if (hits.length) {
    console.error(`${hits.length} forbidden server address(es); use example addresses (https://ark.example.com) instead.`);
    process.exit(1);
  }
  console.log(`check-hosts: no forbidden server addresses in ${paths.length} path(s)`);
}
