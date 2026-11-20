// Pure mobile/PWA helpers shared by the app, the service worker tests and
// Node's built-in test runner.  Deliberately DOM-free so it can be imported in
// isolation.

export const MOBILE_BREAKPOINT = 760;
export const PAIR_HASH_KEY = "pair";
export const MOBILE_TOKEN_STORAGE_KEY = "ark.mobile.token";
export const MOBILE_PERMISSION_STORAGE_KEY = "ark.mobile.permission";
export const MOBILE_EXPIRES_STORAGE_KEY = "ark.mobile.expires";

// Six destinations fit a 360 px phone at 60 px each and cover the mobile-first
// screens.  Topics/Assets/Distribution stay in the "More" sheet so the
// desktop information architecture is preserved.
export const MOBILE_PRIMARY_TABS = [
  { id: "home", workspace: "home", labelKey: "nav.home", icon: "home" },
  { id: "research", workspace: "research", labelKey: "nav.research", icon: "bulb" },
  { id: "map", workspace: "map", labelKey: "nav.map", icon: "map" },
  { id: "execute", workspace: "execute", labelKey: "nav.execute", icon: "play" },
  { id: "assistant", workspace: "assistant", labelKey: "nav.assistant", icon: "message" },
  { id: "settings", workspace: "settings", labelKey: "nav.settings", icon: "settings" },
];

// Editor has no bottom-bar slot of its own (six already fill a 360 px phone):
// it is reachable from the "More" sheet and as the last chip of Execute's
// sub-tab row, and the Execute slot stays highlighted while it is open.
export const MOBILE_SECONDARY_TABS = [
  { id: "editor", workspace: "editor", labelKey: "nav.editor", icon: "edit" },
  { id: "plan", labelKey: "nav.plan", icon: "route" },
  { id: "production", labelKey: "nav.production", icon: "palette" },
  { id: "distribution", labelKey: "nav.distribution", icon: "send" },
];

export function isMobileViewport(width) {
  return Number(width) > 0 && Number(width) <= MOBILE_BREAKPOINT;
}

export function mobileTabActive(activeTab, itemId) {
  if (activeTab === itemId) return true;
  if (activeTab === "plan" && itemId === "ideas") return false;
  return false;
}

export function tabHash(tab, sub = "") {
  if (!tab) return "#/overview";
  if (tab === "plan" && sub) return `#/plan/${sub}`;
  return `#/${tab}`;
}

// Read the one-time pairing code from either `#pair=...` or a hash-route
// spelling such as `#/overview?pair=...` (kept lenient for manual entry).
export function parsePairHash(hash) {
  const text = String(hash || "").replace(/^#/, "");
  if (!text) return "";
  const cleaned = text.replace(/^\//, "");
  const parts = cleaned.split(/[&?]/);
  for (const part of parts) {
    const index = part.indexOf("=");
    if (index <= 0) continue;
    const key = part.slice(0, index);
    // The first segment may itself be "pair=..." (no leading slash).
    if (key === PAIR_HASH_KEY || key.endsWith(`/${PAIR_HASH_KEY}`)) {
      try {
        return decodeURIComponent(part.slice(index + 1));
      } catch {
        return part.slice(index + 1);
      }
    }
  }
  return "";
}

export function pairingHash(token) {
  return `#${PAIR_HASH_KEY}=${encodeURIComponent(String(token || ""))}`;
}

// Build the QR payload.  A path-prefixed public URL keeps its path and gains a
// single trailing slash; localhost is never injected by the client.
export function pairingUrl(base, token) {
  const raw = String(base || "").trim();
  if (!raw) throw new Error("A pairing base URL is required");
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("The pairing base URL is not a valid URL");
  }
  if (!["http:", "https:"].includes(parsed.protocol)) {
    throw new Error("The pairing base URL must use http or https");
  }
  if (parsed.username || parsed.password) {
    throw new Error("The pairing base URL must not contain credentials");
  }
  if (parsed.search || parsed.hash) {
    throw new Error("The pairing base URL must not contain a query or fragment");
  }
  let path = parsed.pathname || "/";
  if (!path.endsWith("/")) path += "/";
  return `${parsed.origin}${path}${pairingHash(token)}`;
}

export function formatRemaining(expiresAt, now = Date.now()) {
  const target = typeof expiresAt === "number" ? expiresAt : Date.parse(expiresAt || "");
  if (!Number.isFinite(target)) return "—";
  const seconds = Math.max(0, Math.round((target - now) / 1000));
  if (seconds <= 0) return "expired";
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

export function isStandalone(displayMode = "") {
  return displayMode === "standalone" || displayMode === "fullscreen"
    || displayMode === "minimal-ui";
}

// Honest PWA capability state.  A service worker only exists in a secure
// context, and a LAN HTTP URL is not one even though the app itself loads.
export function pwaState({ secureContext = false, hostname = "", standalone = false,
                           serviceWorker = false, manifest = true } = {}) {
  const localhost = ["localhost", "127.0.0.1", "::1", "[::1]"].includes(String(hostname).toLowerCase());
  const secure = Boolean(secureContext || localhost);
  const installed = Boolean(standalone);
  if (!manifest) {
    return { canInstall: false, canCache: secure, installed, reason: "no-manifest" };
  }
  if (!secure) {
    return {
      canInstall: false,
      canCache: false,
      installed,
      reason: "insecure-context",
    };
  }
  return {
    canInstall: !installed,
    canCache: true,
    installed,
    reason: installed ? "installed" : serviceWorker ? "ready" : "browser-install",
  };
}

// The service worker must never cache API traffic, regardless of method.
export function shouldCacheAsset(url, method = "GET") {
  if (String(method).toUpperCase() !== "GET") return false;
  let parsed;
  try {
    parsed = new URL(String(url), "http://ark.local/");
  } catch {
    return false;
  }
  if (parsed.pathname.includes("/api/") || parsed.pathname.startsWith("/api/")) return false;
  if (/(^|\/)manifest\.webmanifest$/.test(parsed.pathname)) return true;
  return /\.(?:css|js|mjs|png|svg|ico|webp|woff2?|ttf)$/i.test(parsed.pathname)
    || parsed.pathname.endsWith("/");
}

export function shouldCacheResponse(response, method = "GET") {
  if (!response || !response.ok) return false;
  if (String(method).toUpperCase() !== "GET") return false;
  const type = String(response.headers?.get?.("Content-Type") || "");
  return !type.includes("application/json") && !type.includes("application/x-ndjson");
}

// On-screen keyboard detection for the phone shell.  Browsers either shrink
// the visual viewport (Chrome 108+, Safari) or the whole layout viewport
// (older Chrome / WebView) when the keyboard opens; both show up as the
// visible height dropping well below the tallest height seen at this width.
// Only an editable element with focus counts, so the address bar collapsing
// or a split-screen resize does not hide the tab bar.
export const KEYBOARD_MIN_PX = 120;

export function keyboardOpen({ fullHeight, visibleHeight, editableFocused } = {}) {
  const full = Number(fullHeight) || 0;
  const visible = Number(visibleHeight) || 0;
  if (!editableFocused || full <= 0 || visible <= 0) return false;
  return full - visible >= Math.max(KEYBOARD_MIN_PX, full * 0.18);
}

// Tracks the tallest visible height per viewport width (a rotation or a new
// window width starts a new baseline) and answers keyboardOpen() for a sample.
export function keyboardTracker() {
  const tallest = new Map();
  return (sample = {}) => {
    const width = Math.round(Number(sample.width) || 0);
    const visible = Number(sample.visibleHeight) || 0;
    const layout = Number(sample.layoutHeight) || 0;
    const seen = Math.max(tallest.get(width) || 0, visible, layout);
    // While the keyboard is up the layout height may shrink too; only grow the
    // baseline, never shrink it, so closing the keyboard restores the bar.
    tallest.set(width, seen);
    return keyboardOpen({ fullHeight: seen, visibleHeight: visible, editableFocused: sample.editableFocused });
  };
}

export function isEditableTarget(el) {
  if (!el || typeof el !== "object") return false;
  if (el.isContentEditable) return true;
  const tag = String(el.tagName || "").toLowerCase();
  if (tag === "textarea") return !el.readOnly && !el.disabled;
  if (tag === "select") return false;
  if (tag !== "input") return false;
  const type = String(el.type || "text").toLowerCase();
  const NON_TEXT = ["button", "checkbox", "radio", "range", "color", "file", "submit", "reset", "image", "hidden"];
  return !NON_TEXT.includes(type) && !el.readOnly && !el.disabled;
}
