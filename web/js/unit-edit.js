// Unit editing helpers shared by the inspector (side panel) and the Editor's
// node tabs: saving through PATCH /api/units/{id}, attachment upload with the
// same large-file prompts, opening attachments externally, and keeping every
// open view of a unit consistent after a save.

import { api, mediaUrl } from "./api.js";
import { confirmDialog, toast } from "./components.js";
import { t } from "./i18n.js";
import { isImageUrl } from "./dom.js";
import { patchFromDraft } from "./node-model.js";

// Fired on window after any successful unit save:
//   detail: { unit, origin: "inspector" | "editor" }
export const UNIT_SAVED_EVENT = "ark:unit-saved";
export const UNIT_RESTORE_CHECK_EVENT = "ark:unit-restore-check";

export function announceUnitSaved(unit, origin) {
  try { window.dispatchEvent(new CustomEvent(UNIT_SAVED_EVENT, { detail: { unit, origin } })); }
  catch { /* no window */ }
}

/** Patch cached list rows and every live graph node with the saved unit. */
export function syncUnitLocally(ctx, unit) {
  const local = (ctx?.S?.units || []).find((item) => item.id === unit.id);
  if (local) Object.assign(local, unit);
  if (ctx?.S?.detail?.id === unit.id) ctx.S.detail = unit;
  for (const graph of ctx?.graphs || []) {
    const node = graph.byId?.get(unit.id);
    if (!node) continue;
    Object.assign(node, {
      title: unit.title, type: unit.type, status: unit.status,
      importance: unit.importance, tags: unit.tags, body: unit.body,
    });
    graph._draw?.();
  }
}

/** Save a draft: PATCH, then sync caches and notify other views. */
export async function saveUnitDraft(ctx, unitId, draft, origin) {
  const data = await api.updateUnit(unitId, patchFromDraft(draft));
  syncUnitLocally(ctx, data.unit);
  announceUnitSaved(data.unit, origin);
  return data.unit;
}

export function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || "").split(",", 2)[1] || "");
    reader.onerror = () => reject(reader.error || new Error(t("unit_edit.read_failed")));
    reader.readAsDataURL(file);
  });
}

// Files over 3 MB ask before embedding; JPEG/PNG/WebP can be re-encoded.
export async function compressImage(file) {
  const kind = /^image\/(jpeg|png|webp)$/i.test(file.type);
  const sizeMb = file.size / (1024 * 1024);
  if (file.size <= 3 * 1024 * 1024) return file;
  if (!kind) {
    const go = await confirmDialog(
      t("unit_edit.large_no_jpeg", { name: file.name, size: sizeMb.toFixed(1) }),
      { title: t("unit_edit.large_title"), confirmLabel: t("unit_edit.embed_anyway") });
    return go ? file : null;
  }
  const compress = await confirmDialog(
    t("unit_edit.large_compress", { name: file.name, size: sizeMb.toFixed(1) }),
    { title: t("unit_edit.large_title"), confirmLabel: t("unit_edit.compress_jpeg") });
  if (!compress) {
    const original = await confirmDialog(t("unit_edit.embed_original_q"), {
      title: t("unit_edit.large_title"), confirmLabel: t("unit_edit.embed_original"),
    });
    return original ? file : null;
  }
  const bitmap = await createImageBitmap(file);
  const max = 3000;
  const scale = Math.min(1, max / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bitmap.width * scale));
  canvas.height = Math.max(1, Math.round(bitmap.height * scale));
  const context = canvas.getContext("2d");
  context.fillStyle = "#fff";
  context.fillRect(0, 0, canvas.width, canvas.height);
  context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close?.();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.82));
  if (!blob) throw new Error(t("unit_edit.jpeg_failed"));
  return new File([blob], file.name.replace(/\.[^.]+$/, "") + ".jpg", { type: "image/jpeg" });
}

/** Upload a file as an attachment of `unitId`.  Returns { id, ... } or null if cancelled. */
export async function uploadUnitFile(unitId, source, caption = "") {
  const file = await compressImage(source);
  if (!file) return null;
  const data = await fileToBase64(file);
  const uploaded = await api.uploadMedia({
    unit: unitId, filename: file.name || "upload.bin",
    mime: file.type || "application/octet-stream", data, caption: caption || file.name,
  });
  return { ...uploaded, file };
}

/** Attach a URL or project path (kind guessed like the inspector). */
export function addUnitMediaUrl(unitId, url, { kind = "", caption = "" } = {}) {
  return api.addMedia({ unit: unitId, url, kind: kind || (isImageUrl(url) ? "image" : "link"), caption });
}

export async function openMediaExternal(item) {
  // A file:// path lives on the ARK host. For a remote browser, opening it
  // through the media proxy is the only way to view it on this device.
  if (/^file:\/\//i.test(item.url) && item.raw_url) {
    window.open(mediaUrl(item), "_blank", "noopener");
    return;
  }
  const remote = /^[a-z][a-z0-9+.-]*:\/\//i.test(item.url) && !/^file:\/\//i.test(item.url);
  const body = remote ? { url: item.url } : { path: item.url.replace(/^file:\/\//i, "") };
  try {
    await api.openExternal(body);
    toast(t("toast.opened"), "ok");
  } catch (err) { toast(err.message, "err"); }
}
