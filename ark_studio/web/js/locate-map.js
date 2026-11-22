// "Locate in research map" for any node: one menu entry (unit-menu.js for
// DOM rows and cards, graph.js for canvas graphs, the inspector's ⋯ menu),
// one shortcut (Alt+M by default) and one action, ctx.locateInMap (app.js):
// open the Map's graph, show the node even when the filters hide it (with a
// notice, the filters themselves stay), select it, glide the camera to it
// with a short pulse (GraphView.locate) and open it in the inspector
// (on a phone the map stays in view instead).

import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { getShortcut, shortcutMatches } from "./shortcuts.js";
import { isMobileViewport } from "./mobile-core.js";
import { LOCATE_SHORTCUT_ID, locateBlockReason, revealNotice } from "./locate-map-model.js";

let context = null;

/** The app context the canvas graphs' menus use (they have no ctx of their own). */
export function setLocateContext(ctx) {
  context = ctx;
}

export function locateShortcut() {
  return getShortcut(LOCATE_SHORTCUT_ID);
}

export function isLocateShortcut(event) {
  return shortcutMatches(event, locateShortcut());
}

/** The menu entry; disabled, with the reason, for a review group or a node in the recycle bin. */
export function locateInMapItem(unit, ctx = context) {
  const reason = locateBlockReason(unit);
  const why = reason ? t(`locate.disabled.${reason}`) : "";
  return {
    id: "locate-map", label: t("locate.menu"), icon: "map", shortcut: locateShortcut(),
    disabled: Boolean(reason) || !ctx?.locateInMap,
    title: why || undefined, hint: why || undefined,
    onClick: () => ctx?.locateInMap?.(String(unit.uid ?? unit.id)),
  };
}

/** Run the action for `unit` if it can be located; false otherwise. */
export function locateUnit(unit, ctx = context) {
  if (locateBlockReason(unit) || !ctx?.locateInMap) return false;
  ctx.locateInMap(String(unit.uid ?? unit.id));
  return true;
}

/**
 * Once the Map graph has the node: locate it, then open it in the
 * inspector (not on a phone, where the inspector sheet would cover the
 * map: the node stays selected and a tap opens it).  A force layout may not
 * have placed a new node yet, so this retries for a moment; a node the Map
 * does not have is reported.
 */
export function focusLocated(ctx, graph, uid) {
  ctx.S.mapFocus = null;
  let tries = 0;
  const attempt = () => {
    if (!graph.host?.isConnected) return;
    if (graph.locate(uid)) {
      if (!isMobileViewport(window.innerWidth)) ctx.openUnit?.(uid);
      return;
    }
    if (++tries < 25) setTimeout(attempt, 60);
    else ctx.toast?.(t("locate.not_found", { uid }), "warn");
  };
  requestAnimationFrame(attempt);
}

/**
 * The notice over the Map graph while a located node is shown only because
 * of the locate: says why, offers "Clear filters" (when filters hide it)
 * and a close button that hides the node again.
 */
export function syncRevealNotice(ctx, panel = document.querySelector(".graph-panel[data-map-graph]")) {
  if (!panel) return;
  const notice = ctx.S.tab === "map" ? revealNotice(ctx.S.graph) : null;
  let el = panel.querySelector(".map-reveal-notice");
  if (!notice) { el?.remove(); return; }
  const signature = `${notice.key}|${notice.nodes.map((node) => node.uid).join(",")}`;
  if (el?.dataset.signature === signature) return;
  el?.remove();
  const names = notice.nodes.map((node) => node.uid).join(", ");
  el = h("div", { class: "map-reveal-notice", role: "status", dataset: { signature } },
    icon("info", "sm"),
    h("span", { class: "map-reveal-text" }, t(notice.key), h("b", null, ` ${names}`)),
    notice.clearFilters ? h("button", {
      class: "btn sm", type: "button", "data-reveal-clear": "",
      onclick: () => ctx.clearFilters?.(),
    }, t("locate.clear_filters")) : null,
    h("button", {
      class: "icon-btn", type: "button", title: t("locate.hide_again"), "aria-label": t("locate.hide_again"),
      "data-reveal-dismiss": "", onclick: () => ctx.clearMapReveal?.(),
    }, icon("x", "sm")));
  panel.appendChild(el);
}
