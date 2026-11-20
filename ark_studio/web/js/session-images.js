// Images in a session composer (Sessions page and sidebar): paste, drop or
// pick image files; thumbnails with a remove button before sending.  The
// image is sent as context ({kind: "image_data"}); the server saves it in the
// session workspace and the agent gets its path.  Thumbnails use object URLs,
// never base64 text in the DOM.

import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { acceptImage, MAX_IMAGE_BYTES, MAX_IMAGES } from "./session-sidebar-model.js";
import { attachImageContextMenu } from "./image-copy.js";

let nextId = 1;

function readDataUrl(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("read failed"));
    reader.readAsDataURL(file);
  });
}

/** Read image files; returns { images, errors } (errors are messages for a toast). */
export async function readImages(files, current = 0) {
  const images = [];
  const errors = [];
  for (const file of [...(files || [])]) {
    const check = acceptImage(file, current + images.length);
    if (!check.ok) {
      if (check.reason === "size") errors.push(t("session.images.too_large", { name: file.name || "", mb: MAX_IMAGE_BYTES >> 20 }));
      else if (check.reason === "count") errors.push(t("session.images.too_many", { n: MAX_IMAGES }));
      else if (file.type) errors.push(t("session.images.not_image", { name: file.name || file.type }));
      continue;
    }
    try {
      images.push({ id: `img${nextId++}`, name: check.name, size: file.size, data: await readDataUrl(file), url: URL.createObjectURL(file) });
    } catch (err) {
      errors.push(err.message);
    }
  }
  return { images, errors };
}

export function releaseImages(images = []) {
  for (const image of images) if (image.url) URL.revokeObjectURL(image.url);
}

/** Thumbnails of the images waiting in a composer. */
export function imageTray(images, onRemove) {
  if (!images?.length) return null;
  return h("div", { class: "sw-img-tray" }, images.map((image) => h("span", { class: "sw-img-thumb", title: image.name },
    h("img", { src: image.url, alt: image.name, loading: "lazy" }),
    h("button", {
      class: "sw-img-x", type: "button", title: t("action.remove"), "aria-label": t("session.images.remove", { name: image.name }),
      onclick: () => onRemove(image),
    }, icon("x", "sm")))));
}

/** Image files from a paste or drop event (null when it carries none). */
export function eventImageFiles(event) {
  const items = event.clipboardData?.items || event.dataTransfer?.items;
  const files = [];
  if (items) {
    for (const item of items) if (item.kind === "file" && /^image\//.test(item.type)) files.push(item.getAsFile());
  } else for (const file of event.dataTransfer?.files || []) if (/^image\//.test(file.type)) files.push(file);
  return files.filter(Boolean).length ? files.filter(Boolean) : null;
}

/** A hidden file picker for images; returns { input, open }. */
export function imagePicker(onFiles) {
  const input = h("input", { type: "file", accept: "image/*", multiple: true, hidden: true });
  input.addEventListener("change", () => { onFiles([...input.files]); input.value = ""; });
  return { input, open: () => input.click() };
}

/** Paste and drop of images on `target` call onFiles(files). */
export function bindImageInput(target, onFiles) {
  target.addEventListener("paste", (event) => {
    const files = eventImageFiles(event);
    if (!files) return;
    event.preventDefault();
    onFiles(files);
  });
  target.addEventListener("dragover", (event) => {
    if ([...(event.dataTransfer?.items || [])].some((item) => item.kind === "file")) {
      event.preventDefault();
      target.classList.add("sw-drop");
    }
  });
  target.addEventListener("dragleave", () => target.classList.remove("sw-drop"));
  target.addEventListener("drop", (event) => {
    target.classList.remove("sw-drop");
    const files = eventImageFiles(event);
    if (!files) return;
    event.preventDefault();
    onFiles(files);
  });
}

/** Thumbnails of the images sent with a user turn (workspace paths). */
export function turnImages(paths, urlFor, onOpen) {
  if (!paths?.length) return null;
  const wrap = h("div", { class: "sw-turn-images" });
  for (const path of paths) {
    const src = urlFor(path);
    const name = path.split("/").pop();
    const button = h("button", {
      class: "sw-turn-image", type: "button", title: path, onclick: () => onOpen(src, path),
    }, h("img", { src, alt: name, loading: "lazy", decoding: "async" }));
    attachImageContextMenu(button, () => ({ src, kind: "image", caption: name }));
    wrap.appendChild(button);
  }
  return wrap;
}

export function clearTray(host) {
  clear(host);
}
