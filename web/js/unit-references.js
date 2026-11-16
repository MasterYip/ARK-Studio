import { api } from "./api.js";
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { createInspectorPreview, placeFloating } from "./inspector.js";
import { activationPoint } from "./floating-position.js";

const SELECTOR = "a.rich-unit-link[data-unit-ref]";

export function installUnitReferences(ctx) {
  let anchor = null, card = null, showTimer = null, hideTimer = null, request = null;
  let project = null, version = 0, point = null;
  const projectKey = () => ctx.S.project?.id;
  const linkAt = (target) => target instanceof Element ? target.closest(SELECTOR) : null;
  const inside = (target) => target instanceof Node && (anchor?.contains(target) || card?.contains(target));

  function close() {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    request?.abort(); request = null;
    version++;
    anchor?.removeAttribute("aria-describedby");
    card?.remove(); anchor = card = null;
  }

  function leave() {
    clearTimeout(showTimer); showTimer = null;
    clearTimeout(hideTimer);
    hideTimer = setTimeout(() => {
      if (!anchor?.matches(":hover") && !card?.matches(":hover")
          && !card?.contains(document.activeElement) && !card?.classList.contains("dragging")) close();
    }, 200);
  }

  function openDetails(ref, point = null) {
    close();
    ctx.openUnit?.(ref, point, { revealPane: true });
  }

  function bindCard(link) {
    card.id = "unit-reference-preview";
    card.addEventListener("pointerenter", () => clearTimeout(hideTimer));
    card.addEventListener("pointerleave", (event) => { if (!inside(event.relatedTarget)) leave(); });
    link.setAttribute("aria-describedby", card.id);
  }

  async function show(link) {
    if (link !== anchor || !link.isConnected || project !== projectKey()) return close();
    const ref = link.dataset.unitRef, current = ++version, controller = new AbortController();
    request = controller;
    card = h("aside", { class: "drawer floating unit-ref-preview", role: "complementary", "aria-label": t("refs.preview") },
      h("div", { class: "drawer-body" }, h("p", { class: "unit-ref-message", role: "status" }, t("refs.loading"))));
    document.getElementById("overlay-root").appendChild(card);
    bindCard(link); placeFloating(card, point);
    try {
      const { unit } = await api.unit(ref, { signal: controller.signal });
      if (current !== version || project !== projectKey() || !link.isConnected) return;
      card.remove();
      card = createInspectorPreview(ctx, unit, point, () => { link.focus({ preventScroll: true }); close(); });
      bindCard(link);
    } catch (error) {
      if (current !== version || error.name === "AbortError") return;
      card.querySelector(".unit-ref-message").textContent = error.status === 404 ? t("refs.missing", { ref }) : error.message;
    } finally {
      if (current === version) request = null;
    }
  }

  function enter(link, event) {
    clearTimeout(hideTimer);
    if (card?.contains(link) || (anchor === link && (card || showTimer))) return;
    close(); anchor = link; point = activationPoint(event); project = projectKey();
    showTimer = setTimeout(() => { showTimer = null; show(link); }, 450);
  }
  document.addEventListener("pointerover", (event) => {
    if (event.pointerType === "touch") return;
    const link = linkAt(event.target);
    if (link && !link.contains(event.relatedTarget)) enter(link, event);
  });
  document.addEventListener("pointermove", (event) => {
    if (anchor?.contains(event.target) && !card) point = { x: event.clientX, y: event.clientY };
  });
  document.addEventListener("pointerout", (event) => {
    if (anchor && linkAt(event.target) === anchor && !inside(event.relatedTarget)) leave();
  });
  document.addEventListener("focusin", (event) => {
    const link = linkAt(event.target);
    if (link) enter(link, event);
    else if (card?.contains(event.target)) clearTimeout(hideTimer);
  });
  document.addEventListener("focusout", (event) => { if (inside(event.target) && !inside(event.relatedTarget)) leave(); });
  document.addEventListener("click", (event) => {
    const link = linkAt(event.target);
    if (!link || event.defaultPrevented || event.button !== 0) return;
    event.stopPropagation(); close();
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault(); openDetails(link.dataset.unitRef, activationPoint(event));
  }, true);
  document.addEventListener("pointerdown", (event) => { if (!inside(event.target)) close(); }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !anchor) return;
    event.preventDefault(); event.stopImmediatePropagation();
    if (card?.contains(document.activeElement)) anchor?.focus({ preventScroll: true });
    close();
  }, true);
  document.addEventListener("scroll", (event) => {
    if (card?.contains(event.target)) return;
    const rect = anchor?.getBoundingClientRect();
    if (rect && point && point.x >= rect.left && point.x <= rect.right
        && point.y >= rect.top && point.y <= rect.bottom) return;
    close();
  }, true);
  document.addEventListener("visibilitychange", () => { if (document.hidden) close(); });
  window.addEventListener("resize", close);
  window.addEventListener("hashchange", close);
  window.addEventListener("popstate", close);
  new MutationObserver(() => {
    if (anchor && (!anchor.isConnected || project !== projectKey() || (card && !card.isConnected))) close();
  }).observe(document.body, { childList: true, subtree: true });
}
