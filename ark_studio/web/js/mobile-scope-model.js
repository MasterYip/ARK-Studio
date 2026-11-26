// What a paired phone may call: the same rules as the server's guard
// (server/internal/server/routes_mobile.go, mobileDevicePathAllowed and
// guardMobile).  The page skips other calls on a phone instead of asking the
// server and showing its refusal; tests/js/mobile_scope_model.test.mjs keeps
// the lists equal to the Go ones.

export const PHONE_PREFIXES = ["/api/bootstrap", "/api/project", "/api/revision", "/api/units",
  "/api/links", "/api/media", "/api/graph", "/api/timeline", "/api/topics",
  "/api/activity", "/api/chat", "/api/skills", "/api/infohub",
  "/api/import", "/api/export", "/api/assistant/insights",
  "/api/assistant/dispatch", "/api/assistant/apply",
  "/api/assistant/extract", "/api/agent-sessions", "/api/workspace",
  "/api/mobile/session", "/api/health", "/api/changes", "/api/events",
  "/api/unit-details",
  "/api/trash"];

// Read only (GET/HEAD).
export const PHONE_GETS = ["/api/org", "/api/projects", "/api/gpu/dashboard", "/api/notifications", "/api/mobile/apps"];

// Allowed before the device check (guardMobile).
const ALWAYS = ["/api/health", "/api/mobile/pair/claim", "/api/mobile/login", "/api/projects/open"];

const AVATAR = /^\/api\/users\/[^/]+\/avatar$/;

/** True when a paired phone may call `path` (with or without a query) with `method`. */
export function phoneMayCall(path, method = "GET") {
  let route = String(path || "").split(/[?#]/)[0];
  if (!route.startsWith("/")) route = `/${route.replace(/^\.\//, "")}`;
  if (!route.startsWith("/api/")) return true;
  const verb = String(method || "GET").toUpperCase();
  const read = verb === "GET" || verb === "HEAD";
  if (ALWAYS.includes(route)) return true;
  if (route === "/api/agent/token" || route === "/api/open" || route === "/api/me/password") return false;
  if (route === "/api/me" || route === "/api/me/avatar") return true;
  if (AVATAR.test(route)) return read;
  if (PHONE_GETS.includes(route)) return read;
  return PHONE_PREFIXES.some((prefix) => route === prefix || route.startsWith(`${prefix}/`));
}
