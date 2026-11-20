// Pure parts of the image copy menu (web/js/image-copy.js): which items the
// right-click menu of an image preview shows, the link it copies and whether
// a fetched image has to be re-encoded as PNG before it goes to the clipboard
// (browsers only accept image/png in a ClipboardItem).  No DOM, no imports.

/** Lower-case MIME type without parameters ("image/svg+xml; charset=x" -> "image/svg+xml"). */
export function blobMimeType(type) {
  return String(type || "").split(";")[0].trim().toLowerCase();
}

/** True unless the bytes are already PNG (anything else is drawn to a canvas first). */
export function needsPngConversion(type) {
  return blobMimeType(type) !== "image/png";
}

export function isSvgType(type) {
  return blobMimeType(type) === "image/svg+xml";
}

export const DEFAULT_RASTER_SIZE = 1024;
export const MAX_RASTER_SIDE = 8192;

/**
 * Canvas size for drawing an image as PNG: its natural size, a default for
 * images without one (an SVG with only a viewBox), scaled down so neither
 * side exceeds `max`.
 */
export function pngCanvasSize(width, height, { fallback = DEFAULT_RASTER_SIZE, max = MAX_RASTER_SIDE } = {}) {
  let w = Number(width) || 0;
  let h = Number(height) || 0;
  if (w <= 0 || h <= 0) {
    if (w > 0) h = w;
    else if (h > 0) w = h;
    else { w = fallback; h = fallback; }
  }
  const scale = Math.min(1, max / Math.max(w, h));
  return { width: Math.max(1, Math.round(w * scale)), height: Math.max(1, Math.round(h * scale)) };
}

/** Query parameters that carry a credential and must not leave in a copied link. */
const SECRET_PARAMS = ["mobile_token", "token", "access_token"];

/**
 * Absolute form of a media URL for "Copy image link" / "Open in new tab".
 * `stripSecrets` drops sign-in tokens from the query (a copied link is meant
 * to be shared; the recipient signs in themselves).  data:/blob: URLs are
 * returned as they are.
 */
export function absoluteMediaUrl(url, base = globalThis.location?.href, { stripSecrets = false } = {}) {
  const text = String(url || "").trim();
  if (!text) return "";
  if (/^(?:data|blob):/i.test(text)) return text;
  let parsed;
  try { parsed = base ? new URL(text, base) : new URL(text); } catch { return text; }
  if (stripSecrets) for (const name of SECRET_PARAMS) parsed.searchParams.delete(name);
  return parsed.toString();
}

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif|ico)(?:[?#]|$)/i;

/**
 * Normalise what a preview hands to the menu: a URL string or an object
 * {src | url, kind?, is_image?, caption?}.  Returns null for non-images
 * (videos, files): those get no image menu.
 */
export function imageMediaInfo(media) {
  if (!media) return null;
  const item = typeof media === "string" ? { src: media } : media;
  const src = String(item.src || item.url || "").trim();
  if (!src) return null;
  const kind = String(item.kind || "").toLowerCase();
  const image = item.is_image === true || kind === "image"
    || (!kind && item.is_image !== false)
    || /^data:image\//i.test(src) || IMAGE_EXT.test(src);
  if (!image) return null;
  return { src, caption: String(item.caption || item.alt || "") };
}

/**
 * The menu's item descriptors (for context-menu.js openContextMenu, minus the
 * handlers): `action` is "copy_image" | "copy_link" | "open_tab".
 */
export function imageMenuItems(media, t = (key) => key, { mac = false } = {}) {
  const info = imageMediaInfo(media);
  if (!info) return [];
  const isDataUrl = /^data:/i.test(info.src);
  return [
    { action: "copy_image", label: t("media.copy_image"), icon: "copy", shortcut: mac ? "⌘C" : "Ctrl+C" },
    // A data: URL is the image itself, not a link worth sharing.
    isDataUrl ? null : { action: "copy_link", label: t("media.copy_link"), icon: "link" },
    { action: "open_tab", label: t("media.open_tab"), icon: "external" },
  ].filter(Boolean);
}

/**
 * Whether a keydown is the copy shortcut for the focused image: Ctrl+C
 * (Cmd+C on macOS) without Shift/Alt, and nothing else claims it (selected
 * text, an input or editable field).
 */
export function isCopyShortcut(event, { mac = false, hasSelection = false, inField = false } = {}) {
  if (!event || hasSelection || inField) return false;
  if (event.shiftKey || event.altKey) return false;
  const mod = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
  return mod && String(event.key || "").toLowerCase() === "c";
}

/**
 * Classify a clipboard failure for the toast: "denied" (permission / no
 * clipboard API / not a secure page), "load" (the image could not be
 * fetched or decoded) or "failed".
 */
export function copyErrorKind(error) {
  if (!error) return "failed";
  if (error.stage === "load") return "load";
  const name = String(error.name || "");
  if (name === "NotAllowedError" || name === "SecurityError" || error.code === "no_clipboard") return "denied";
  return "failed";
}
