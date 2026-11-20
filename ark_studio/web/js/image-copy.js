// Right-click menu for image previews (attachment thumbnails, the lightbox,
// session images, graph images): Copy image, Copy image link, Open in new
// tab; Ctrl+C (Cmd+C) on a focused preview copies the image.
//
// Copying fetches the full-size image (with the page's credentials), turns
// anything but PNG into PNG through a canvas and writes it with the async
// Clipboard API.  Where the page may not write images (the desktop app on an
// http:// server, a WebView that refuses), the ARK Studio desktop shell's
// copyImage bridge writes the PNG instead.  Pure parts: image-copy-model.js.
import { copyText } from "./dom.js";
import { t } from "./i18n.js";
import { openContextMenu } from "./context-menu.js";
import {
  absoluteMediaUrl, copyErrorKind, imageMediaInfo, imageMenuItems, isCopyShortcut, isSvgType, needsPngConversion, pngCanvasSize,
} from "./image-copy-model.js";

const isMac = () => /Mac|iPhone|iPad/.test(globalThis.navigator?.platform || globalThis.navigator?.userAgent || "");

// components.js (toast) pulls in much of the app; load it when needed so this
// module stays cheap to import from the image viewer.
function notify(message, kind) {
  import("./components.js").then(({ toast }) => toast(message, kind)).catch(() => {});
}

/** The image's bytes, as fetched (same-origin cookies, or the URL's own token). */
export async function fetchImageBlob(url, { fetchImpl = globalThis.fetch } = {}) {
  let response;
  try {
    response = await fetchImpl(url, { credentials: "include" });
  } catch (err) {
    throw Object.assign(new Error(err?.message || "fetch failed"), { stage: "load" });
  }
  if (!response.ok) throw Object.assign(new Error(`HTTP ${response.status}`), { stage: "load" });
  return response.blob();
}

function loadImage(objectUrl) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.decoding = "async";
    img.onload = () => resolve(img);
    img.onerror = () => reject(Object.assign(new Error("image decode failed"), { stage: "load" }));
    img.src = objectUrl;
  });
}

/** Draw any browser-decodable image (JPEG, WebP, GIF first frame, SVG, ...) to a PNG blob. */
export async function rasterToPng(blob, { doc = globalThis.document } = {}) {
  const objectUrl = URL.createObjectURL(isSvgType(blob.type) ? new Blob([blob], { type: "image/svg+xml" }) : blob);
  try {
    const img = await loadImage(objectUrl);
    const size = pngCanvasSize(img.naturalWidth || img.width, img.naturalHeight || img.height);
    const canvas = doc.createElement("canvas");
    canvas.width = size.width;
    canvas.height = size.height;
    canvas.getContext("2d").drawImage(img, 0, 0, size.width, size.height);
    return await new Promise((resolve, reject) => canvas.toBlob(
      (png) => (png ? resolve(png) : reject(Object.assign(new Error("PNG encode failed"), { stage: "load" }))), "image/png"));
  } finally {
    URL.revokeObjectURL(objectUrl);
  }
}

/** Fetch `url` and return it as an image/png Blob (converted unless already PNG). */
export async function imagePngBlob(url, { fetchImpl = globalThis.fetch, toPng = rasterToPng } = {}) {
  const blob = await fetchImageBlob(url, { fetchImpl });
  if (!needsPngConversion(blob.type)) return blob;
  return toPng(blob);
}

function desktopBridge() {
  const desktop = globalThis.window?.__ARK_DESKTOP__;
  return typeof desktop?.copyImage === "function" ? desktop : null;
}

async function copyViaDesktop(desktop, pngPromise) {
  const png = await pngPromise;
  const bytes = new Uint8Array(await png.arrayBuffer());
  await desktop.copyImage(bytes);
}

/**
 * Copy the image at `url` to the clipboard as PNG.  Call it straight from the
 * click / keydown handler: the ClipboardItem gets a Promise<Blob>, so the
 * user gesture is still valid when the bytes arrive (Safari needs that).
 * Resolves to "clipboard" or "desktop"; rejects with an Error whose
 * copyErrorKind() is "denied", "load" or "failed".
 */
export function copyImageToClipboard(url, deps = {}) {
  const {
    fetchImpl = globalThis.fetch,
    toPng = rasterToPng,
    clipboard = globalThis.navigator?.clipboard,
    ClipboardItemImpl = globalThis.ClipboardItem,
    desktop = desktopBridge(),
  } = deps;
  const pngPromise = imagePngBlob(url, { fetchImpl, toPng });
  pngPromise.catch(() => {}); // handled below; avoid an unhandled-rejection report
  const loadFailure = (err) => (err?.stage === "load" ? err : null);
  if (!clipboard?.write || typeof ClipboardItemImpl !== "function") {
    if (desktop) return copyViaDesktop(desktop, pngPromise).then(() => "desktop");
    return pngPromise.then(() => {
      throw Object.assign(new Error("clipboard unavailable"), { code: "no_clipboard" });
    });
  }
  let item;
  try {
    item = new ClipboardItemImpl({ "image/png": pngPromise });
  } catch {
    item = null; // older engines want a Blob, not a promise
  }
  const write = item
    ? clipboard.write([item])
    : pngPromise.then((png) => clipboard.write([new ClipboardItemImpl({ "image/png": png })]));
  return write.then(() => "clipboard", async (err) => {
    // A failed fetch surfaces as the write's error: report that, not "denied".
    const loadErr = await pngPromise.then(() => null, loadFailure);
    if (loadErr) throw loadErr;
    if (desktop) return copyViaDesktop(desktop, pngPromise).then(() => "desktop");
    throw err;
  });
}

function reportCopyError(err) {
  const kind = copyErrorKind(err);
  notify(t(kind === "denied" ? "media.copy_denied" : kind === "load" ? "media.copy_load_failed" : "media.copy_failed"), "err");
}

/** Copy with toasts (the menu / shortcut action). */
export function copyImageWithFeedback(src) {
  return copyImageToClipboard(src).then(
    () => { notify(t("media.image_copied"), "ok"); return true; },
    (err) => { reportCopyError(err); return false; });
}

export async function copyImageLink(src) {
  const ok = await copyText(absoluteMediaUrl(src, globalThis.location?.href, { stripSecrets: true }));
  notify(t(ok ? "media.link_copied" : "media.copy_failed"), ok ? "ok" : "err");
  return ok;
}

export function openImageInNewTab(src) {
  globalThis.open?.(absoluteMediaUrl(src), "_blank", "noopener");
}

/** Image menu items with handlers, for a caller's own menu (graph image menu). */
export function imageCopyMenuItems(media) {
  const info = imageMediaInfo(media);
  if (!info) return [];
  const actions = {
    copy_image: () => copyImageWithFeedback(info.src),
    copy_link: () => copyImageLink(info.src),
    open_tab: () => openImageInNewTab(info.src),
  };
  return imageMenuItems(info, t, { mac: isMac() }).map(({ action, ...item }) => ({ ...item, onClick: actions[action] }));
}

/** Open the image menu at (x, y) for `media` ({src|url, kind?, caption?} or a URL). */
export function openImageMenu(media, { x, y, returnFocus = null, extraItems = [] } = {}) {
  const items = imageCopyMenuItems(media);
  if (!items.length) return false;
  if (extraItems.length) items.push({ separator: true }, ...extraItems);
  openContextMenu({ x, y, items, className: "image-copy-menu", returnFocus });
  return true;
}

function inEditableField(target) {
  const el = target?.closest ? target : null;
  return Boolean(el?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable='true']"));
}

function hasTextSelection() {
  const sel = globalThis.getSelection?.();
  return Boolean(sel && !sel.isCollapsed && String(sel).trim());
}

const NATIVE_FOCUSABLE = /^(BUTTON|A|INPUT|SELECT|TEXTAREA)$/;

/**
 * Right-click menu and Ctrl+C on an image preview.  `getMedia()` returns the
 * full-size image as {src, kind?, caption?} (or a URL); null / a non-image
 * leaves the browser's own menu.  The element becomes focusable (tabindex 0)
 * so the shortcut can reach it.  Returns a detach function.
 */
export function attachImageContextMenu(element, getMedia, { focusable = true } = {}) {
  if (!element || typeof getMedia !== "function") return () => {};
  const resolve = () => imageMediaInfo(getMedia());
  if (focusable && !NATIVE_FOCUSABLE.test(element.tagName || "") && !element.hasAttribute?.("tabindex")) {
    element.setAttribute("tabindex", "0");
  }
  element.dataset.imageMenu = "1";
  const onContextMenu = (event) => {
    const info = resolve();
    if (!info) return;
    event.preventDefault();
    event.stopPropagation();
    openImageMenu(info, { x: event.clientX, y: event.clientY, returnFocus: element });
  };
  const onKeyDown = (event) => {
    if (!isCopyShortcut(event, { mac: isMac(), hasSelection: hasTextSelection(), inField: inEditableField(event.target) })) {
      // Shift+F10 / the menu key open the menu from the keyboard.
      if (!((event.key === "F10" && event.shiftKey) || event.key === "ContextMenu")) return;
      const info = resolve();
      if (!info) return;
      event.preventDefault();
      const rect = element.getBoundingClientRect();
      openImageMenu(info, { x: rect.left + 8, y: rect.top + Math.min(rect.height, 24), returnFocus: element });
      return;
    }
    const info = resolve();
    if (!info) return;
    event.preventDefault();
    event.stopPropagation();
    copyImageWithFeedback(info.src);
  };
  element.addEventListener("contextmenu", onContextMenu);
  element.addEventListener("keydown", onKeyDown);
  return () => {
    element.removeEventListener("contextmenu", onContextMenu);
    element.removeEventListener("keydown", onKeyDown);
    delete element.dataset.imageMenu;
  };
}

/**
 * The same menu for images inside `container` that match `selector` (rich
 * text images in a session thread).  `getMedia(img)` defaults to the
 * image's own source.
 */
export function delegateImageContextMenu(container, selector, getMedia = (img) => ({ src: img.currentSrc || img.src, caption: img.alt })) {
  if (!container) return () => {};
  const onContextMenu = (event) => {
    const img = event.target?.closest?.(selector);
    if (!img || !container.contains(img) || img.closest("[data-image-menu]")) return;
    const info = imageMediaInfo(getMedia(img));
    if (!info) return;
    event.preventDefault();
    openImageMenu(info, { x: event.clientX, y: event.clientY });
  };
  container.addEventListener("contextmenu", onContextMenu);
  return () => container.removeEventListener("contextmenu", onContextMenu);
}
