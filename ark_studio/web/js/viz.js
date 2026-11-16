// Embedded HTML visualisations (```html blocks and .html attachments).
//
// Security model (see docs/development/design.md, "Embedded HTML visualisations"):
//  * The HTML never touches ARK's DOM.  It is posted to web/viz-frame.html,
//    loaded in <iframe sandbox="allow-scripts"> WITHOUT allow-same-origin, so
//    it runs in an opaque origin: no access to ARK's DOM, cookies, storage,
//    mobile token or same-origin API.  The frame's own response CSP repeats
//    the sandbox and sets connect-src 'none' (no fetch/XHR/WebSocket), and
//    images/media/fonts only from data:/blob:.
//  * Running is opt-in per block ("Run visualisation"): agent-written or
//    imported nodes must not execute code just because someone looked at them,
//    and an endless loop in a same-process frame would freeze the tab.
//  * The only message ARK accepts back is {type:"ark-viz-size"} from the
//    exact frame window it created, with a clamped numeric height.

import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import {
  acceptVizMessage, vizRenderMessage, hashText, VIZ_DEFAULT_HEIGHT, VIZ_FRAME_PATH,
} from "./rich-model.js";

const frames = new Map();      // contentWindow -> id
const frameEls = new Map();    // id -> iframe
const approved = new Set();    // approval keys for this browser session
let seq = 0;
let listening = false;

function onMessage(event) {
  const accepted = acceptVizMessage(event, frames);
  if (!accepted) return;
  const frame = frameEls.get(accepted.id);
  if (!frame?.isConnected) return;
  const next = `${accepted.height}px`;
  if (frame.style.height !== next) frame.style.height = next;
}

function prune() {
  for (const [id, frame] of frameEls) {
    if (frame.isConnected) continue;
    frameEls.delete(id);
    for (const [win, value] of frames) if (value === id) frames.delete(win);
  }
}

function currentTheme() {
  return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/** Create a sandboxed frame showing `html`.  Returns the iframe element. */
export function vizFrame(html, { title = t("viz.frame_title") } = {}) {
  if (!listening) {
    window.addEventListener("message", onMessage);
    listening = true;
  }
  prune();
  seq += 1;
  const id = `viz-${Date.now().toString(36)}-${seq}`;
  const frame = h("iframe", {
    class: "viz-frame", title,
    sandbox: "allow-scripts",
    referrerpolicy: "no-referrer",
    src: VIZ_FRAME_PATH,
    dataset: { vizId: id },
    style: { height: `${VIZ_DEFAULT_HEIGHT}px` },
  });
  frameEls.set(id, frame);
  frame.addEventListener("load", () => {
    const win = frame.contentWindow;
    if (!win) return;
    for (const [other, value] of frames) if (value === id) frames.delete(other);
    frames.set(win, id);
    // Opaque-origin target: "*" is the only possible targetOrigin; the
    // payload is the block's own source, nothing secret.
    win.postMessage(vizRenderMessage(id, html, currentTheme()), "*");
  }, { once: true });
  return frame;
}

/**
 * A ```html block in rendered Markdown.  `key` scopes the opt-in: once the
 * user ran a block, re-renders of the same block (live preview) keep running.
 */
export function vizBlock(source, { key = "" } = {}) {
  const approval = key || hashText(source);
  const stage = h("div", { class: "viz-stage" });
  const sourceView = h("pre", { class: "viz-source hidden" }, h("code", null, source));
  const block = h("div", { class: "viz-block", dataset: { vizState: "idle" } });
  const runBtn = h("button", { class: "btn sm primary viz-run", type: "button" }, icon("play", "sm"), t("viz.run"));
  const srcBtn = h("button", {
    class: "btn sm ghost viz-toggle-source", type: "button", "aria-pressed": "false",
    onclick: () => {
      const showing = sourceView.classList.toggle("hidden") === false;
      srcBtn.setAttribute("aria-pressed", showing ? "true" : "false");
    },
  }, icon("file", "sm"), t("viz.source"));
  const run = () => {
    approved.add(approval);
    block.dataset.vizState = "running";
    stage.replaceChildren(vizFrame(source, { title: t("viz.title") }));
    runBtn.replaceChildren(icon("refresh", "sm"), t("viz.rerun"));
  };
  runBtn.addEventListener("click", run);
  stage.appendChild(h("div", { class: "viz-placeholder" },
    icon("shield", "sm"), h("span", null, t("viz.opt_in_hint"))));
  block.append(
    h("div", { class: "viz-head" },
      h("span", { class: "viz-badge", title: t("viz.sandbox_hint") }, icon("shield", "sm"), t("viz.sandboxed")),
      h("span", { class: "viz-label" }, t("viz.title")),
      h("span", { class: "spacer" }),
      srcBtn, runBtn),
    stage, sourceView);
  if (approved.has(approval)) run();
  return block;
}
