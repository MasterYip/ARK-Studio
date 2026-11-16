// ARK Studio service worker: cache the app shell, plus project images for the
// signed-in account (see MEDIA below).  Other API responses are never cached
// here; the page keeps project data in IndexedDB (web/js/client-cache.js).
//
// The shell list is intentionally explicit.  A Python test compares it with the
// files under web/js so a new module cannot silently be missing from offline
// mode.  Bump CACHE_VERSION whenever the shell changes.
const CACHE_VERSION = "ark-shell-v169";
const SHELL_ASSETS = [
  "./vendor/dompurify/purify.es.js",
  "./vendor/marked/marked.esm.js",
  "./js/rich-html.js",
  "./js/markdown.js",
  "./js/node-history.js",
  "./js/history-diff.js",
  "./js/history-diff-view.js",
  "./vendor/jsdiff/character.js",
  "./vendor/jsdiff/array.js",
  "./vendor/jsdiff/base.js",
  "./",
  "./index.html",
  "./manifest.webmanifest",
  "./favicon.svg",
  "./brand/ark-mark.svg",
  "./brand/ark-lockup-loading.svg",
  "./brand/ark-lockup-loading-dark.svg",
  "./brand/ark-lockup-corner.svg",
  "./brand/ark-lockup-corner-dark.svg",
  "./brand/ark-mark.png",
  "./brand/icon-180.png",
  "./brand/icon-192.png",
  "./brand/icon-512.png",
  "./brand/icon-maskable-512.png",
  "./css/app.css",
  "./css/login.css",
  "./css/startup.css",
  "./css/mobile.css",
  "./vendor/katex/katex.min.css",
  "./vendor/katex/katex.min.js",
  "./js/app.js",
  "./js/map-list-model.js",
  "./js/admin-panel.js",
  "./js/api.js",
  "./js/client-cache.js",
  "./js/client-cache-model.js",
  "./js/live-sync.js",
  "./js/connectivity.js",
  "./js/sync-status.js",
  "./js/cache-panel.js",
  "./js/appearance.js",
  "./js/appearance-panel.js",
  "./js/assistant-composer.js",
  "./js/assistant-response.js",
  "./js/boot.js",
  "./js/startup-graph.js",
  "./js/startup-force.js",
  "./js/graph-core.js",
  "./js/node-graph-layout.js",
  "./js/node-card.js",
  "./js/graph-sprites.js",
  "./js/mind-layout.js",
  "./js/resources-model.js",
  "./js/resources-view.js",
  "./js/arrows.js",
  "./js/floating-position.js",
  "./js/assign-menu.js",
  "./js/assign-model.js",
  "./js/assignee.js",
  "./js/profile.js",
  "./js/users.js",
  "./js/comments.js",
  "./js/comments-model.js",
  "./js/components.js",
  "./js/context-menu.js",
  "./js/desktop-shell.js",
  "./js/dom.js",
  "./js/editor-model.js",
  "./js/editor-layout.js",
  "./js/breadcrumb-model.js",
  "./js/files-model.js",
  "./js/filter-store.js",
  "./js/gantt.js",
  "./js/graph.js",
  "./js/guidance.js",
  "./js/guidance-model.js",
  "./js/settings-tabs.js",
  "./js/fold-section.js",
  "./js/appearance-overrides.js",
  "./js/graph-filter.js",
  "./js/graph-force.js",
  "./vendor/d3-quadtree/d3-quadtree.js",
  "./js/i18n.js",
  "./js/i18n_server.js",
  "./js/i18n-en.js",
  "./js/i18n-zh.js",
  "./js/i18n_server_zh.js",
  "./js/editor-entry.js",
  "./js/editor-media.js",
  "./js/icons.js",
  "./js/image-viewer.js",
  "./js/image-viewer-model.js",
  "./js/infohub-panel.js",
  "./js/inspector.js",
  "./js/inspector-width.js",
  "./js/login.js",
  "./js/markers.js",
  "./js/mind-settings.js",
  "./js/mind.js",
  "./js/mobile-core.js",
  "./js/mobile-ui.js",
  "./js/needs-model.js",
  "./js/node-model.js",
  "./js/physics.js",
  "./js/providers-panel.js",
  "./js/rich-model.js",
  "./js/session-context-model.js",
  "./js/setup-review-model.js",
  "./js/starter-prompts.js",
  "./js/session-dock.js",
  "./js/session-library.js",
  "./js/session-model.js",
  "./js/gpu-model.js",
  "./js/session-workspace.js",
  "./js/shortcuts.js",
  "./js/skills-panel.js",
  "./js/state.js",
  "./js/topic-actions.js",
  "./js/topic-model.js",
  "./js/unit-edit.js",
  "./js/unit-menu.js",
  "./js/export-menu.js",
  "./js/unit-references.js",
  "./js/viz.js",
  "./js/workbench.js",
  "./js/terminal-panel.js",
  "./js/changes-panel.js",
  "./js/search-model.js",
  "./js/search-view.js",
  "./js/node-reveal.js",
  "./js/scroll-sync-model.js",
  "./js/scroll-sync.js",
  "./js/local-graph-model.js",
  "./js/local-graph-view.js",
  "./js/layout-presets.js",
  "./js/workbench-model.js",
  "./js/views/agent-tokens.js",
  "./js/views/chat.js",
  "./js/views/distribution.js",
  "./js/views/editor.js",
  "./js/views/ideas.js",
  "./js/views/map.js",
  "./js/views/node-editor.js",
  "./js/views/onboarding.js",
  "./js/views/overview.js",
  "./js/views/plan.js",
  "./js/views/production.js",
  "./js/views/settings.js",
  "./js/views/setup-resources.js",
  "./js/views/tasks.js",
  "./js/views/topics.js",
];

self.ARK_SHELL_ASSETS = SHELL_ASSETS;
const BASE = new URL("./", self.location.href);
const INDEX = new URL("./index.html", BASE).href;

function isApiRequest(url) {
  return url.pathname.includes("/api/");
}

function isStaticDestination(request) {
  return ["script", "style", "image", "font", "manifest"].includes(request.destination);
}

async function cacheShell() {
  const cache = await caches.open(CACHE_VERSION);
  const response = await fetch(INDEX, { cache: "no-store" });
  if (!response.ok || response.redirected) return;
  const html = await response.clone().text();
  const version = html.match(/name="ark-build" content="([A-Za-z0-9_.-]+)"/)?.[1];
  if (!version) return; // Never store a sign-in form/CSRF token as the shell.
  // Reuse versioned HTTP-cache entries from the completed first visit. Bound
  // concurrency so offline preparation cannot saturate a slow connection.
  const assets = SHELL_ASSETS.filter((path) => path !== "./" && path !== "./index.html");
  let cursor = 0;
  await Promise.all(Array.from({ length: 3 }, async () => {
    while (cursor < assets.length) {
      const url = new URL(assets[cursor++], BASE);
      url.searchParams.set("v", version);
      const asset = await fetch(url.href);
      if (!asset.ok || asset.redirected || !asset.headers.get("Cache-Control")?.includes("immutable")) throw new Error("Shell asset unavailable or release changed");
      await cache.put(url.href, asset);
    }
  }));
  await cache.put(INDEX, response);
}

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    await cacheShell();
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const keys = await caches.keys();
    const previous = keys.filter((key) => key.startsWith("ark-shell-") && key !== CACHE_VERSION).at(-1);
    await Promise.all(keys.filter((key) => key.startsWith("ark-shell-") && key !== CACHE_VERSION && key !== previous).map((key) => caches.delete(key)));
    await self.clients.claim();
    // Never navigate open windows here: a forced reload mid-session threw
    // users back to the boot screen and lost unsaved input.  Requests are
    // versioned, so an open tab keeps working; the page offers a reload
    // (see mobile-ui.js "ark:update-ready") and the new shell applies on the
    // next navigation.
  })());
});

// ------------------------------------------------------------------ media
// Project images (GET /api/media/<id>/raw requested as an image) are served
// cache-first and revalidated in the background.  The cache belongs to one
// account: the page sends the server's opaque cache_id ("ark-media-identity")
// after sign-in and "ark-media-clear" on sign-out; images of any other
// account are deleted then.  Without a known account nothing is cached.  A
// revalidation answered 401/403/404 (signed out, grant revoked) deletes the
// entry.  Bounded to MEDIA_MAX_ENTRIES / MEDIA_MAX_BYTES (oldest first).
const MEDIA_PREFIX = "ark-media-";
const MEDIA_META = "ark-media-meta";
const MEDIA_MAX_ENTRIES = 400;
const MEDIA_MAX_BYTES = 80 * 1024 * 1024;
const MEDIA_MAX_ITEM = 6 * 1024 * 1024;
// A cached image is shown without a request; it is revalidated in the
// background at most this often (a page full of thumbnails must not occupy
// every connection with revalidations on a slow link).
const MEDIA_RECHECK_MS = 30 * 60 * 1000;
let trimTimer = 0;
const MEDIA_RE = /\/api\/media\/\d+\/raw$/;
let mediaIdentity = null;

async function mediaId() {
  if (mediaIdentity !== null) return mediaIdentity;
  try {
    const meta = await caches.open(MEDIA_META);
    const stored = await meta.match(new URL("./__media_identity", BASE).href);
    mediaIdentity = stored ? await stored.text() : "";
  } catch { mediaIdentity = ""; }
  return mediaIdentity;
}

async function setMediaIdentity(id) {
  mediaIdentity = String(id || "");
  const keep = mediaIdentity ? MEDIA_PREFIX + mediaIdentity : "";
  const keys = await caches.keys();
  await Promise.all(keys.filter((key) => key.startsWith(MEDIA_PREFIX) && key !== MEDIA_META && key !== keep)
    .map((key) => caches.delete(key)));
  const meta = await caches.open(MEDIA_META);
  await meta.put(new URL("./__media_identity", BASE).href, new Response(mediaIdentity));
}

function mediaKey(url) {
  const clean = new URL(url);
  clean.searchParams.delete("mobile_token");
  return clean.href;
}

function isMediaRequest(request, url) {
  return MEDIA_RE.test(url.pathname) && request.destination === "image"
    && !url.searchParams.has("download") && !request.headers?.get?.("Range");
}

function scheduleTrim(cache) {
  if (trimTimer) return;
  trimTimer = setTimeout(() => { trimTimer = 0; trimMedia(cache).catch(() => {}); }, 5000);
}

async function trimMedia(cache) {
  const keys = await cache.keys();
  let total = 0;
  const sizes = [];
  for (const key of keys) {
    const response = await cache.match(key);
    const size = Number(response?.headers.get("Content-Length") || 0);
    sizes.push([key, size]);
    total += size;
  }
  let index = 0;
  while ((sizes.length - index > MEDIA_MAX_ENTRIES || total > MEDIA_MAX_BYTES) && index < sizes.length) {
    total -= sizes[index][1];
    await cache.delete(sizes[index][0]);
    index += 1;
  }
}

async function cachedMedia(request, event) {
  const id = await mediaId();
  if (!id) return fetch(request);
  const cache = await caches.open(MEDIA_PREFIX + id);
  const key = mediaKey(request.url);
  const cached = await cache.match(key);
  const refresh = async () => {
    const headers = {};
    const etag = cached?.headers.get("ETag");
    if (etag) headers["If-None-Match"] = etag;
    const response = await fetch(request.url, { credentials: "same-origin", headers, cache: "no-store" });
    if ([401, 403, 404].includes(response.status)) {
      await cache.delete(key);
      return response;
    }
    if (response.status === 304 && cached) {
      const headers = new Headers(cached.headers);
      headers.set("X-ARK-Cached-At", String(Date.now()));
      await cache.put(key, new Response(await cached.clone().arrayBuffer(), { status: 200, headers }));
      return cached;
    }
    const size = Number(response.headers.get("Content-Length") || 0);
    if (response.ok && !response.redirected && (await mediaId()) === id
        && String(response.headers.get("Content-Type") || "").startsWith("image/")
        && size > 0 && size <= MEDIA_MAX_ITEM) {
      const headers = new Headers(response.headers);
      headers.set("X-ARK-Cached-At", String(Date.now()));
      const body = await response.clone().arrayBuffer();
      await cache.put(key, new Response(body, { status: 200, headers }));
      scheduleTrim(cache);
    }
    return response;
  };
  if (cached) {
    const checked = Number(cached.headers.get("X-ARK-Cached-At") || 0);
    if (Date.now() - checked > MEDIA_RECHECK_MS) event.waitUntil(refresh().catch(() => {}));
    return cached;
  }
  return refresh();
}

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting();
  if (event.data && event.data.type === "ark-media-identity") {
    event.waitUntil?.(setMediaIdentity(event.data.id));
  }
  if (event.data && event.data.type === "ark-media-clear") {
    event.waitUntil?.(setMediaIdentity(""));
  }
});

async function networkFirstNavigation(request, event) {
  const cache = await caches.open(CACHE_VERSION);
  const network = (async () => {
    const response = await fetch(request, { cache: "no-store" });
    if (response.ok && !response.redirected && response.headers.get("Content-Type")?.includes("text/html")) {
      const html = await response.clone().text();
      if (html.includes('name="ark-build"')) await cache.put(INDEX, response.clone());
    }
    return response;
  })();
  event.waitUntil(network.catch(() => {}));
  const fallback = await cache.match(INDEX);
  if (!fallback) return network;
  let timer;
  try {
    return await Promise.race([
      network.catch(() => fallback),
      new Promise((resolve) => { timer = setTimeout(() => resolve(fallback), 2500); }),
    ]);
  } finally { clearTimeout(timer); }
}

async function cachedStatic(request) {
  const cache = await caches.open(CACHE_VERSION);
  const versioned = new URL(request.url).searchParams.has("v");
  if (versioned) {
    const cached = await caches.match(request);
    if (cached) return cached;
  }
  try {
    const response = await fetch(request, { cache: versioned ? "default" : "no-cache" });
    if (response?.ok && !response.redirected && (!versioned || response.headers.get("Cache-Control")?.includes("immutable")) && !response.headers.get("Content-Type")?.includes("text/html")) {
      cache.put(request, response.clone()).catch(() => {});
    }
    return response;
  } catch {
    // Matching a different ?v can mix module versions after a deployment.
    const cached = await caches.match(request);
    return cached || Response.error();
  }
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") return;
  let url;
  try {
    url = new URL(request.url);
  } catch {
    return;
  }
  if (url.origin !== self.location.origin) return;
  // Other API requests (including streams and artifacts) always go to the
  // network.  Project data in the shell cache would leak between accounts.
  if (isMediaRequest(request, url)) {
    event.respondWith(cachedMedia(request, event));
    return;
  }
  if (isApiRequest(url)) return;
  if (url.pathname === new URL("./login", BASE).pathname || url.pathname === new URL("./logout", BASE).pathname) return;
  if (request.mode === "navigate") {
    event.respondWith(networkFirstNavigation(request, event));
    return;
  }
  if (isStaticDestination(request)) {
    event.respondWith(cachedStatic(request));
  }
});
