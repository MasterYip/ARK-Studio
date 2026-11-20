// Size of a resource location (code repository, workspace, storage,
// database): the `stats` / `size` object of GET /api/skills/setup/state and
// GET /api/resources/graph (internal/setup/locstats.go).  Each device
// measures its own paths, so sizes are shown per device and never summed.
// Pure, no DOM; `t` is the i18n lookup.
import { agoKey, ageSeconds } from "./resources-model.js";

/** A size older than this is marked as possibly out of date. */
export const SIZE_STALE_SECONDS = 24 * 3600;

/** Decimal byte size: "512 B", "2.3 GB", "118 MB" (one decimal below 100). */
export function formatSize(bytes) {
  let value = Number(bytes);
  if (!Number.isFinite(value) || value < 0) return "?";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) { value /= 1000; unit += 1; }
  const text = unit === 0 || value >= 100 ? String(Math.round(value)) : value.toFixed(1).replace(/\.0$/, "");
  return `${text} ${units[unit]}`;
}

/** 12340 -> "12,340" (both languages group thousands with commas). */
export function groupDigits(n) {
  const value = Math.round(Number(n) || 0);
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

/**
 * What to show for one location's size.
 * Returns { state, text, split, stale, capped, title } where state is
 * "none" (not measured), "ok", "missing", "network" or "error"; `split` is
 * the working tree / .git line of a git checkout (or null).
 */
export function sizeView(stats, { t, now = Date.now() } = {}) {
  if (!stats || typeof stats !== "object") {
    return { state: "none", text: t("size.unmeasured"), split: null, stale: false, capped: false, title: t("size.unmeasured_hint") };
  }
  const age = ageSeconds(stats.measured_at, now);
  const [agoK, agoVars] = agoKey(age);
  const when = t(agoK, agoVars);
  const stale = age === null || age > SIZE_STALE_SECONDS;
  const source = ["server", "desktop", "agent"].includes(stats.source) ? stats.source : "agent";
  const titleParts = [t(`size.source.${source}`, { when })];
  const kind = stats.kind || "dir";
  if (kind === "missing" || kind === "network" || kind === "error") {
    const text = kind === "error" && stats.error ? `${t("size.error")}: ${stats.error}` : t(`size.${kind}`);
    return { state: kind, text: `${text} · ${when}`, split: null, stale, capped: false, title: titleParts.join("\n") };
  }
  const capped = Boolean(stats.capped);
  const parts = [`${capped ? "≥ " : ""}${formatSize(stats.bytes)}`];
  if (kind !== "file") parts.push(t("size.files", { n: `${capped ? "≥ " : ""}${groupDigits(stats.files)}` }));
  parts.push(when);
  let split = null;
  const git = Number(stats.git_bytes) || 0;
  if (stats.git && git > 0) {
    split = t("size.git_split", { work: formatSize(Math.max(0, (Number(stats.bytes) || 0) - git)), git: formatSize(git) });
  }
  if (capped) titleParts.push(t(stats.capped === "entries" ? "size.capped_entries" : "size.capped_time"));
  if (Number(stats.skipped) > 0) titleParts.push(t("size.skipped", { n: groupDigits(stats.skipped) }));
  if (stale) titleParts.push(t("size.stale_hint"));
  return { state: "ok", text: parts.join(" · "), split, stale, capped, title: titleParts.join("\n") };
}

/** The raw host id of a graph device id ("host:<id>"), "" for the ARK server, null otherwise. */
export function graphHostId(deviceId) {
  if (deviceId === "ark") return "";
  return typeof deviceId === "string" && deviceId.startsWith("host:") ? deviceId.slice(5) : null;
}

const LOCAL_PATH = /^([A-Za-z]:[\\/]|\/|~)/;

/** A path on a device's file system (not a URL or other reference). */
export function isLocalPath(path) { return typeof path === "string" && LOCAL_PATH.test(path); }

/** Desktop measurement results (measure_paths) as location-stats reports. */
export function desktopReports(results, targets) {
  const byPath = new Map((Array.isArray(results) ? results : []).map((r) => [r.path, r]));
  return targets.map((target) => {
    const r = byPath.get(target.path);
    if (!r) return null;
    const item = { resource: target.resource, path_or_ref: target.path, kind: r.kind || "dir",
      bytes: Number(r.bytes) || 0, files: Number(r.files) || 0 };
    if (r.git) Object.assign(item, { git: true, git_bytes: Number(r.git_bytes) || 0, git_files: Number(r.git_files) || 0 });
    if (r.capped) item.capped = r.capped;
    if (r.skipped) item.skipped = Number(r.skipped) || 0;
    if (r.error) item.error = String(r.error).slice(0, 200);
    return item;
  }).filter(Boolean);
}

/** Paths of a device row whose size is missing or stale (the desktop app measures them). */
export function duePaths(paths, now = Date.now()) {
  return (Array.isArray(paths) ? paths : []).filter((p) => isLocalPath(p.path_or_ref))
    .filter((p) => {
      const age = ageSeconds(p.stats?.measured_at, now);
      return age === null || age > SIZE_STALE_SECONDS;
    });
}
