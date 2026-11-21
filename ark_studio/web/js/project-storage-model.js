// Settings > Project storage location: the model behind
// project-storage-panel.js (pure, so tests run without a DOM).
//
// GET /api/org/project-storage answers {storage: {default, configured,
// builtin, fallback, free_bytes, folders: [{path, builtin, default, exists,
// projects}], host_local, platform}}.  `default` is where the next project is
// created: the chosen folder, or the server's own folder (`builtin`) when
// none is chosen or the chosen one is missing (`fallback`).

/** Below this much free space the panel warns (bytes). */
export const LOW_SPACE_BYTES = 1024 ** 3;

/** "12.3 GB" style sizes (1024 based, like file managers on Windows). */
export function formatBytes(n) {
  if (typeof n !== "number" || !Number.isFinite(n) || n < 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let v = n, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
  const digits = i === 0 || v >= 100 ? 0 : 1;
  return `${v.toFixed(digits)} ${units[i]}`;
}

/** An absolute path for the server's platform ("windows", "linux", "darwin"). */
export function looksAbsolute(text, platform = "") {
  const p = String(text || "").trim();
  if (!p) return false;
  if (platform === "windows") return /^[a-zA-Z]:[\\/]/.test(p) || /^\\\\[^\\]+\\[^\\]+/.test(p);
  return p.startsWith("/");
}

/**
 * Quick check of a typed path before asking the server: an i18n key for the
 * problem, or "" (the server decides the rest: system folders, projects,
 * write access).
 */
export function pathInputError(text, platform = "") {
  const p = String(text || "").trim();
  if (!p) return "storage.error.required";
  if (!looksAbsolute(p, platform)) return "storage.error.relative";
  return "";
}

/** An example path for the input's placeholder. */
export function examplePath(platform = "") {
  return platform === "windows" ? "C:\\ARK Projects" : "/srv/ark/projects"; // i18n-ignore: example paths
}

/** The desktop app can show its own folder picker (local ARK page only). */
export function canPickFolder(hub) {
  return Boolean(hub && typeof hub.pickFolder === "function");
}

/** What the panel shows, from the storage payload. */
export function storageView(storage) {
  const s = storage && typeof storage === "object" ? storage : {};
  const folders = Array.isArray(s.folders) ? s.folders : [];
  const free = typeof s.free_bytes === "number" ? s.free_bytes : null;
  const configured = String(s.configured || "");
  return {
    current: String(s.default || s.builtin || ""),
    builtin: String(s.builtin || ""),
    configured,
    usesBuiltin: !configured || Boolean(s.fallback),
    fallback: Boolean(s.fallback),
    freeText: free === null ? "" : formatBytes(free),
    lowSpace: free !== null && free < LOW_SPACE_BYTES,
    canReveal: Boolean(s.host_local),
    platform: String(s.platform || ""),
    folders: folders.map((f) => ({
      path: String(f.path || ""),
      builtin: Boolean(f.builtin),
      isDefault: Boolean(f.default),
      exists: f.exists !== false,
      projects: Number.isFinite(f.projects) ? f.projects : 0,
      // The server's own folder and the folder for new projects stay listed.
      removable: !f.builtin && !f.default,
    })),
  };
}

/** The confirmation text parts for a checked folder: [key, vars][]. */
export function checkSummary(check) {
  if (!check || !check.ok) return [];
  const parts = [[check.will_create ? "storage.check.will_create" : "storage.check.exists", { path: check.path }]];
  if (typeof check.free_bytes === "number") {
    parts.push([check.free_bytes < LOW_SPACE_BYTES ? "storage.check.low_space" : "storage.check.free",
      { size: formatBytes(check.free_bytes) }]);
  }
  return parts;
}

/** The toast after creating a project when the chosen folder was missing. */
export function fallbackNotice(data) {
  const n = data && data.storage_notice;
  if (!n || n.code !== "STORAGE_FALLBACK") return null;
  return ["storage.fallback_created", { configured: String(n.configured || ""), used: String(n.used || "") }];
}
