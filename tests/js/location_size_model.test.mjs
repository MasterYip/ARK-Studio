// Run: node --experimental-vm-modules --test tests/js/location_size_model.test.mjs
// Resource location sizes (web/js/location-size-model.js): formatting in
// both languages, stale and capped markers, git split, desktop reports and
// the device rows of Settings > Resources.
import assert from "node:assert/strict";
import { test } from "node:test";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";
import { formatSize, groupDigits, sizeView, graphHostId, desktopReports, duePaths, isLocalPath }
  from "../../ark_studio/web/js/location-size-model.js";
import { deviceRows } from "../../ark_studio/web/js/setup-devices-model.js";

const lookup = (dict) => (key, vars = {}) => {
  let text = dict[key] ?? EN[key] ?? key;
  for (const [k, v] of Object.entries(vars)) text = text.replaceAll(`{${k}}`, String(v));
  return text;
};
const now = Date.parse("2026-11-20T12:00:00Z");

test("formatSize and digit grouping", () => {
  assert.equal(formatSize(0), "0 B");
  assert.equal(formatSize(512), "512 B");
  assert.equal(formatSize(2300000000), "2.3 GB");
  assert.equal(formatSize(118400000), "118 MB");
  assert.equal(formatSize(1000), "1 KB");
  assert.equal(formatSize(-1), "?");
  assert.equal(groupDigits(12340), "12,340");
  assert.equal(groupDigits(7), "7");
});

test("sizeView: zh and en lines", () => {
  const stats = { kind: "dir", bytes: 2300000000, files: 12340, measured_at: "2026-11-20T11:55:00+00:00", source: "server" };
  const zh = sizeView(stats, { t: lookup(ZH), now });
  assert.equal(zh.text, "2.3 GB · 12,340 个文件 · 5 分钟前");
  assert.equal(zh.state, "ok");
  assert.equal(zh.stale, false);
  assert.match(zh.title, /ARK 服务器/);
  const en = sizeView(stats, { t: lookup(EN), now });
  assert.equal(en.text, "2.3 GB · 12,340 files · 5 min ago");
  assert.equal(sizeView(null, { t: lookup(ZH), now }).text, "未测量");
  assert.equal(sizeView(undefined, { t: lookup(EN), now }).state, "none");
});

test("sizeView: git split, capped, stale, single file, missing", () => {
  const t = lookup(ZH);
  const git = sizeView({ bytes: 2300000000, files: 10, git: true, git_bytes: 1200000000,
    measured_at: "2026-11-18T12:00:00Z", source: "desktop" }, { t, now });
  assert.equal(git.split, "工作区 1.1 GB / .git 1.2 GB");
  assert.equal(git.stale, true);
  assert.match(git.title, /桌面应用/);
  assert.match(git.title, /超过一天/);
  const capped = sizeView({ bytes: 5000000, files: 2000000, capped: "entries", measured_at: "2026-11-20T11:00:00Z" }, { t, now });
  assert.equal(capped.text, "≥ 5 MB · ≥ 2,000,000 个文件 · 60 分钟前");
  assert.match(capped.title, /条目数上限/);
  const file = sizeView({ kind: "file", bytes: 4096, files: 1, measured_at: "2026-11-20T12:00:00Z" }, { t, now });
  assert.equal(file.text, "4.1 KB · 刚刚");
  const missing = sizeView({ kind: "missing", bytes: 0, measured_at: "2026-11-20T12:00:00Z", source: "server" }, { t, now });
  assert.equal(missing.state, "missing");
  assert.equal(missing.text, "路径不存在 · 刚刚");
  assert.equal(sizeView({ kind: "network", measured_at: "2026-11-20T12:00:00Z" }, { t: lookup(EN), now }).text,
    "network path, not measured · just now");
});

test("graph device ids, local paths", () => {
  assert.equal(graphHostId("ark"), "");
  assert.equal(graphHostId("host:abc123"), "abc123");
  assert.equal(graphHostId("git:github.com"), null);
  assert.ok(isLocalPath("/data/x") && isLocalPath("E:\\code") && isLocalPath("C:/x") && isLocalPath("~/x"));
  assert.ok(!isLocalPath("https://github.com/x") && !isLocalPath("s3://bucket") && !isLocalPath(null));
});

test("desktop reports and due paths per device row", () => {
  const targets = [{ resource: "project/code", path: "E:\\code" }, { resource: "project/db", path: "E:\\db.sqlite3" }];
  const items = desktopReports([
    { path: "E:\\code", kind: "dir", bytes: 10, files: 2, git: true, git_bytes: 4, git_files: 1, capped: "time" },
  ], targets);
  assert.deepEqual(items, [{ resource: "project/code", path_or_ref: "E:\\code", kind: "dir", bytes: 10, files: 2,
    git: true, git_bytes: 4, git_files: 1, capped: "time" }]);
  const rows = deviceRows({ devices: [{ id: "pc", current: true, missing: [], paths: [
    { resource: "project/code", path_or_ref: "/c", stats: { bytes: 1, measured_at: "2026-11-20T11:00:00Z" } },
    { resource: "project/db", path_or_ref: "/d", stats: { bytes: 1, measured_at: "2026-11-18T11:00:00Z" } },
    { resource: "project/web", path_or_ref: "https://example.org" },
    { resource: "project/none", path_or_ref: null },
    { resource: "project/new", path_or_ref: "/n" },
  ] }] });
  assert.equal(rows[0].paths[0].stats.bytes, 1, "device rows keep each path's size");
  assert.deepEqual(duePaths(rows[0].paths, now).map((p) => p.resource), ["project/db", "project/new"]);
});
