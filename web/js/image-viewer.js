// Shared zoom/pan image viewer, used embedded (Editor image tab, inspector
// preview) and as a floating lightbox (node gallery, sessions, reports).
//
//   wheel           zoom centred on the cursor. Lightbox: always. Embedded:
//                   only while the viewer has focus (click it) or Ctrl/Cmd is
//                   held, so a plain wheel over an inline image still scrolls.
//   left-drag       pan (grab / grabbing cursor); touch: one-finger pan,
//                   two-finger pinch (pointer events)
//   double-click    toggle fit <-> 100% at the cursor
//   keys            + / - zoom, 0 fit, 1 100%, arrows pan, Esc closes a lightbox
//
// The <img> itself is transformed (translate + scale, origin 0 0) instead of
// being drawn to a canvas, so SVGs are re-rasterised by the browser and stay
// sharp at any zoom. Pure maths lives in image-viewer-model.js.
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import {
  clampPan, distance, fitScale, fitView, formatZoom, midpoint, panBy, pinchView, stepScale, wheelFactor, zoomAt,
} from "./image-viewer-model.js";

const PAN_STEP = 48;
const FIT_PADDING = 12;

/**
 * @param {object} opts
 * @param {string} opts.src
 * @param {string} [opts.alt]
 * @param {"embedded"|"lightbox"} [opts.mode]
 * @param {string} [opts.className]  extra class on the root
 * @param {string} [opts.imgClass]   extra class on the <img>
 * @param {boolean} [opts.toolbar]   show the zoom toolbar (default true)
 * @param {(img: HTMLImageElement) => void} [opts.onLoad]
 * @param {() => void} [opts.onError]
 * @param {() => void} [opts.onClose] Esc handler (lightbox)
 */
export function createImageViewer({
  src, alt = "", mode = "embedded", className = "", imgClass = "", toolbar = true, onLoad, onError, onClose,
} = {}) {
  const lightbox = mode === "lightbox";
  const img = h("img", { class: `iv-img ${imgClass}`.trim(), src, alt, draggable: "false" });
  const readout = h("span", { class: "iv-readout", "aria-live": "polite" }, "100%");
  const button = (label, title, action, cls = "") => h("button", {
    class: `iv-btn ${cls}`.trim(), type: "button", title, "aria-label": title,
    onclick: (event) => { event.stopPropagation(); action(); root.focus({ preventScroll: true }); },
    ondblclick: (event) => event.stopPropagation(),
  }, label);
  const bar = toolbar ? h("div", { class: "iv-toolbar", onpointerdown: (event) => event.stopPropagation() },
    button("−", t("viewer.zoom_out"), () => step(-1), "iv-zoom-out"),
    readout,
    button("+", t("viewer.zoom_in"), () => step(1), "iv-zoom-in"),
    button(icon("fit", "sm"), t("viewer.fit"), () => fit(true), "iv-fit"),
    button("1:1", t("viewer.actual"), () => actual(null, true), "iv-actual")) : null;
  const root = h("div", {
    class: `iv iv-${lightbox ? "lightbox" : "embedded"} ${className}`.trim(),
    tabindex: "0", role: "img", "aria-label": alt || t("viewer.image"), title: t(lightbox ? "viewer.hint_lightbox" : "viewer.hint_embedded"),
  }, img, bar);

  let nat = { w: 0, h: 0 };
  let view = { scale: 1, x: 0, y: 0 };
  let fitMode = true;
  let loaded = false;

  const stageSize = () => ({ w: root.clientWidth, h: root.clientHeight });
  const point = (event) => {
    const rect = root.getBoundingClientRect();
    return { x: event.clientX - rect.left, y: event.clientY - rect.top };
  };
  const fitScaleNow = () => {
    const stage = stageSize();
    return fitScale(nat.w, nat.h, stage.w, stage.h, { padding: FIT_PADDING });
  };

  function apply(next, { animate = false, fitted = false } = {}) {
    const stage = stageSize();
    view = loaded ? clampPan(next, nat.w, nat.h, stage.w, stage.h) : next;
    fitMode = fitted;
    root.classList.toggle("iv-animate", animate);
    img.style.transform = `translate(${view.x}px, ${view.y}px) scale(${view.scale})`;
    readout.textContent = formatZoom(view.scale);
    root.classList.toggle("iv-fitted", fitMode);
    root.dataset.scale = String(view.scale);
    root.dataset.x = String(view.x);
    root.dataset.y = String(view.y);
    root.dataset.fit = fitMode ? "1" : "0";
  }
  function fit(animate = false) {
    if (!loaded) return;
    const stage = stageSize();
    apply(fitView(nat.w, nat.h, stage.w, stage.h, { padding: FIT_PADDING }), { animate, fitted: true });
  }
  function centre() {
    const stage = stageSize();
    return { x: stage.w / 2, y: stage.h / 2 };
  }
  function zoomTo(scale, at = null, animate = false) {
    if (!loaded) return;
    const p = at || centre();
    apply(zoomAt(view, scale, p.x, p.y), { animate });
  }
  function actual(at = null, animate = false) {
    if (!loaded) return;
    if (at) { zoomTo(1, at, animate); return; }
    const stage = stageSize();
    apply({ scale: 1, x: (stage.w - nat.w) / 2, y: (stage.h - nat.h) / 2 }, { animate });
  }
  function step(direction) { zoomTo(stepScale(view.scale, direction), null, true); }

  img.addEventListener("load", () => {
    nat = { w: img.naturalWidth || img.width || 300, h: img.naturalHeight || img.height || 150 };
    img.style.width = `${nat.w}px`;
    img.style.height = `${nat.h}px`;
    loaded = true;
    root.classList.add("iv-ready");
    fit();
    onLoad?.(img);
  });
  img.addEventListener("error", () => onError?.());

  // Wheel ------------------------------------------------------------------
  root.addEventListener("wheel", (event) => {
    if (!loaded) return;
    const focused = root.contains(document.activeElement);
    if (!lightbox && !focused && !event.ctrlKey && !event.metaKey) return; // let the page scroll
    event.preventDefault();
    const factor = wheelFactor(event.deltaY, event.deltaMode, root.clientHeight);
    if (factor === 1) return;
    zoomTo(view.scale * factor, point(event));
  }, { passive: false });

  // Pointer: pan with one pointer, pinch with two ----------------------------
  const pointers = new Map();
  let gesture = null;
  const startGesture = () => {
    const pts = [...pointers.values()];
    if (pts.length >= 2) {
      gesture = { kind: "pinch", start: { ...view }, dist: distance(pts[0], pts[1]), mid: midpoint(pts[0], pts[1]) };
    } else if (pts.length === 1) {
      gesture = { kind: "pan", start: { ...view }, from: pts[0] };
    } else gesture = null;
  };
  root.addEventListener("pointerdown", (event) => {
    if (!loaded || (event.pointerType === "mouse" && event.button !== 0)) return;
    if (event.target.closest?.(".iv-toolbar")) return;
    event.preventDefault();
    root.focus({ preventScroll: true });
    root.setPointerCapture?.(event.pointerId);
    pointers.set(event.pointerId, point(event));
    root.classList.add("iv-grabbing");
    root.classList.remove("iv-animate");
    startGesture();
  });
  root.addEventListener("pointermove", (event) => {
    if (!pointers.has(event.pointerId) || !gesture) return;
    pointers.set(event.pointerId, point(event));
    const pts = [...pointers.values()];
    if (gesture.kind === "pinch" && pts.length >= 2) {
      apply(pinchView(gesture.start, gesture.dist, distance(pts[0], pts[1]), gesture.mid, midpoint(pts[0], pts[1])));
    } else if (gesture.kind === "pan") {
      const p = pts[0];
      if (Math.abs(p.x - gesture.from.x) + Math.abs(p.y - gesture.from.y) < 1) return;
      apply(panBy(gesture.start, p.x - gesture.from.x, p.y - gesture.from.y));
    }
  });
  const endPointer = (event) => {
    if (!pointers.delete(event.pointerId)) return;
    if (!pointers.size) root.classList.remove("iv-grabbing");
    startGesture();
  };
  root.addEventListener("pointerup", endPointer);
  root.addEventListener("pointercancel", endPointer);
  root.addEventListener("lostpointercapture", endPointer);

  root.addEventListener("dblclick", (event) => {
    if (!loaded || event.target.closest?.(".iv-toolbar")) return;
    event.preventDefault();
    if (fitMode) actual(point(event), true);
    else fit(true);
  });

  // Keyboard -----------------------------------------------------------------
  root.addEventListener("keydown", (event) => {
    if (event.ctrlKey || event.metaKey || event.altKey) return;
    const pan = event.shiftKey ? PAN_STEP * 4 : PAN_STEP;
    const moves = { ArrowLeft: [pan, 0], ArrowRight: [-pan, 0], ArrowUp: [0, pan], ArrowDown: [0, -pan] };
    let handled = true;
    if (event.key === "+" || event.key === "=") step(1);
    else if (event.key === "-" || event.key === "_") step(-1);
    else if (event.key === "0") fit(true);
    else if (event.key === "1") actual(null, true);
    else if (moves[event.key]) apply(panBy(view, ...moves[event.key]), { animate: true });
    else if (event.key === "Escape" && onClose) onClose();
    else handled = false;
    if (handled) { event.preventDefault(); event.stopPropagation(); }
  });

  // Re-fit when the stage changes size while in fit mode.
  let observer = null;
  const onResize = () => { if (loaded && fitMode) fit(); };
  if (typeof ResizeObserver === "function") {
    observer = new ResizeObserver(onResize);
    observer.observe(root);
  } else window.addEventListener("resize", onResize);

  return {
    el: root,
    img,
    view: () => ({ ...view, fit: fitMode, naturalWidth: nat.w, naturalHeight: nat.h }),
    fit: () => fit(true),
    actual: () => actual(null, true),
    zoomIn: () => step(1),
    zoomOut: () => step(-1),
    zoomTo,
    focus: () => root.focus({ preventScroll: true }),
    destroy: () => { observer?.disconnect(); window.removeEventListener("resize", onResize); },
  };
}

/**
 * Floating lightbox around a viewer. `actions` are extra header nodes
 * (download / open externally); `meta` is a small caption (e.g. file size).
 */
export function openImageLightbox({ src, alt = "", title = "", meta = "", actions = [], className = "" } = {}) {
  document.querySelector(".iv-overlay")?.__ivClose?.();
  let closed = false;
  const previousFocus = document.activeElement;
  const close = () => {
    if (closed) return;
    closed = true;
    viewer.destroy();
    overlay.remove();
    document.removeEventListener("keydown", onKey, true);
    previousFocus?.focus?.({ preventScroll: true });
  };
  const viewer = createImageViewer({ src, alt, mode: "lightbox", onClose: close });
  // Esc anywhere closes, even if focus moved to a header button.
  const onKey = (event) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    close();
  };
  const dialog = h("div", { class: "iv-dialog", role: "dialog", "aria-modal": "true", "aria-label": title || alt || t("viewer.image") },
    h("div", { class: "iv-head" },
      h("span", { class: "iv-title", title: title || alt }, title || alt),
      meta ? h("span", { class: "iv-meta" }, meta) : null,
      h("span", { class: "iv-spacer" }),
      ...actions,
      h("button", { class: "icon-btn iv-close", type: "button", title: t("action.close"), "aria-label": t("action.close"), onclick: close }, icon("x", "sm"))),
    viewer.el);
  const overlay = h("div", { class: `iv-overlay ${className}`.trim(), onclick: (event) => {
    if (event.target === overlay) close();
  } }, dialog);
  overlay.__ivClose = close;
  (document.getElementById("overlay-root") || document.body).appendChild(overlay);
  document.addEventListener("keydown", onKey, true);
  viewer.focus();
  return { close, viewer, el: overlay };
}

/** What the media lightbox shows for an attachment. */
export function mediaLightboxKind(item = {}) {
  const url = String(item.url || item.raw_url || "").split(/[?#]/)[0].toLowerCase();
  const ext = (/\.([a-z0-9]+)$/.exec(url) || [])[1] || "";
  const kind = String(item.kind || "").toLowerCase();
  if (item.is_image || kind === "image" || /^data:image\//i.test(item.url || "")
    || ["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "avif"].includes(ext)) return "image";
  if (kind === "video" || ["mp4", "webm", "mov", "m4v", "ogv"].includes(ext)) return "video";
  if (kind === "audio" || ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac", "opus"].includes(ext)) return "audio";
  if (kind === "pdf" || ext === "pdf") return "pdf";
  return "file";
}

/**
 * A lightbox over a list of attachments (a node's media): the zoom/pan
 * image viewer, a video or audio player, or the browser's PDF viewer, with
 * previous/next (buttons, ← / →, PageUp / PageDown) and Esc to close.
 * Other files show their name and an Open button (`onOpen(item)`).
 * @param {object} opts { items, index, title, srcOf(item) -> url, onOpen(item) }
 */
export function openMediaLightbox({ items = [], index = 0, title = "", srcOf = (item) => item.url, onOpen = null } = {}) {
  if (!items.length) return null;
  document.querySelector(".iv-overlay")?.__ivClose?.();
  let closed = false;
  let current = Math.max(0, Math.min(items.length - 1, index));
  let viewer = null;
  const previousFocus = document.activeElement;
  const titleEl = h("span", { class: "iv-title" });
  const metaEl = h("span", { class: "iv-meta" });
  const stage = h("div", { class: "iv-media-stage" });
  const step = (delta) => { if (items.length > 1) show((current + delta + items.length) % items.length); };
  const prev = h("button", { class: "icon-btn iv-prev", type: "button", title: t("viewer.prev"), "aria-label": t("viewer.prev"), onclick: () => step(-1) }, icon("chevronLeft", "sm"));
  const next = h("button", { class: "icon-btn iv-next", type: "button", title: t("viewer.next"), "aria-label": t("viewer.next"), onclick: () => step(1) }, icon("chevronRight", "sm"));
  const openBtn = onOpen ? h("button", { class: "btn sm ghost iv-open", type: "button", onclick: () => onOpen(items[current]) }, icon("external", "sm"), t("action.open")) : null;
  const close = () => {
    if (closed) return;
    closed = true;
    viewer?.destroy();
    overlay.remove();
    document.removeEventListener("keydown", onKey, true);
    previousFocus?.focus?.({ preventScroll: true });
  };
  function show(i) {
    current = i;
    viewer?.destroy();
    viewer = null;
    stage.replaceChildren();
    const item = items[current];
    const label = item.caption || item.title || String(item.url || "").split(/[\/]/).pop() || t("viewer.image");
    titleEl.textContent = title ? `${title} · ${label}` : label;
    titleEl.title = titleEl.textContent;
    metaEl.textContent = items.length > 1 ? `${current + 1} / ${items.length}` : "";
    const src = srcOf(item);
    const kind = mediaLightboxKind(item);
    stage.dataset.kind = kind;
    if (kind === "image") {
      viewer = createImageViewer({ src, alt: label, mode: "lightbox", onClose: close });
      stage.appendChild(viewer.el);
      viewer.focus();
    } else if (kind === "video") {
      stage.appendChild(h("video", { class: "iv-video", src, controls: true, autoplay: true, playsinline: true, preload: "metadata" }));
    } else if (kind === "audio") {
      stage.appendChild(h("audio", { class: "iv-audio", src, controls: true, autoplay: true, preload: "metadata" }));
    } else if (kind === "pdf") {
      stage.appendChild(h("iframe", { class: "iv-pdf", src, title: label }));
    } else {
      stage.appendChild(h("div", { class: "iv-file" }, icon("file", "lg"), h("div", null, label),
        openBtn ? h("button", { class: "btn primary sm", type: "button", onclick: () => onOpen(item) }, icon("external", "sm"), t("action.open")) : null));
    }
    prev.disabled = next.disabled = items.length < 2;
  }
  const onKey = (event) => {
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(); return; }
    if (event.target?.closest?.("video, audio, iframe")) return;
    const delta = { ArrowLeft: -1, PageUp: -1, ArrowRight: 1, PageDown: 1 }[event.key];
    if (!delta || items.length < 2) return;
    event.preventDefault();
    event.stopPropagation();
    step(delta);
  };
  const dialog = h("div", { class: "iv-dialog iv-media", role: "dialog", "aria-modal": "true", "aria-label": title || t("viewer.image") },
    h("div", { class: "iv-head" }, prev, next, titleEl, metaEl, h("span", { class: "iv-spacer" }), openBtn,
      h("button", { class: "icon-btn iv-close", type: "button", title: t("action.close"), "aria-label": t("action.close"), onclick: close }, icon("x", "sm"))),
    stage);
  const overlay = h("div", { class: "iv-overlay iv-media-overlay", onclick: (event) => { if (event.target === overlay) close(); } }, dialog);
  overlay.__ivClose = close;
  (document.getElementById("overlay-root") || document.body).appendChild(overlay);
  document.addEventListener("keydown", onKey, true);
  show(current);
  return { close, show, get index() { return current; }, el: overlay };
}
