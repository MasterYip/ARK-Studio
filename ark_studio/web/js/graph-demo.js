// Graph walkthrough: a dialog with its own GraphView on a small sample graph
// (graph-demo-script.js).  A script drives it with synthetic pointer, wheel
// and key events while a drawn cursor moves and the corner shows each input.
// The sandbox never touches the person's graph or the server: its callbacks
// only change the sample data in memory, and it does not prefetch on hover.
import { h, clear } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { GraphView } from "./graph.js";
import {
  DEMO_LAYOUTS, DEMO_SCRIPTS, DEMO_START_MODE, DEMO_TIMING, demoDataset, demoGraphOptions, demoLayoutFor, formatKeys, opKeys,
} from "./graph-demo-script.js";

const LAYOUT_LABELS = { force: "graph.force", mind: "graph.mind", node: "graph.layout_node" };
const KEY_CHIP_MS = 1600;

class Cancelled extends Error {}

let OPEN = null;

function reducedMotion() {
  try { return Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches); } catch { return false; }
}

const CURSOR_SVG = '<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M4 2.5 L4 19 L8.6 14.9 L11.6 21.5 L14.4 20.3 L11.5 13.8 L17.6 13.6 Z" fill="#fff" stroke="#111" stroke-width="1.4" stroke-linejoin="round"/></svg>';

export function openGraphDemo({ layout = "force" } = {}) {
  OPEN?.close();
  const demo = new GraphDemo(demoLayoutFor(layout));
  OPEN = demo;
  return demo;
}

class GraphDemo {
  constructor(layout) {
    this.layout = layout;
    this.reduced = reducedMotion();
    this.stepMode = this.reduced;
    this.paused = false;
    this.run = null;
    this.graph = null;
    this.cursor = { x: 0, y: 0, down: false };
    this.returnFocus = document.activeElement;
    this._build();
    this.start();
  }

  _build() {
    this.layoutBtns = new Map();
    const layoutBar = h("div", { class: "graph-demo-layouts", role: "group", "aria-label": t("graph.layout") },
      DEMO_LAYOUTS.map((id) => {
        const btn = h("button", {
          type: "button", class: "graph-demo-layout", "aria-pressed": String(id === this.layout),
          onclick: () => this.setLayout(id),
        }, t(LAYOUT_LABELS[id]));
        this.layoutBtns.set(id, btn);
        return btn;
      }));
    this.graphHost = h("div", { class: "graph-demo-graph" });
    // Pointer input from the person stops here: only the script moves the
    // sample graph.
    this.shield = h("div", { class: "graph-demo-shield" });
    this.cursorEl = h("div", { class: "graph-demo-cursor", html: CURSOR_SVG, "aria-hidden": "true" });
    this.keysEl = h("div", { class: "graph-demo-keys", "aria-hidden": "true" });
    this.stage = h("div", { class: "graph-demo-stage" }, this.graphHost, this.shield, this.cursorEl, this.keysEl);
    this.stepEl = h("span", { class: "graph-demo-step" });
    this.captionText = h("span", { class: "graph-demo-caption-text" });
    this.caption = h("div", { class: "graph-demo-caption", role: "status", "aria-live": "polite" }, this.stepEl, this.captionText);
    this.progress = h("div", { class: "graph-demo-progress", "aria-hidden": "true" });
    this.playBtn = h("button", { type: "button", class: "btn primary graph-demo-play", onclick: () => this.togglePlay() });
    this.nextBtn = h("button", { type: "button", class: "btn ghost graph-demo-next", onclick: () => this.next() },
      icon("chevronRight", "sm"), t("graph.demo.next"));
    this.restartBtn = h("button", { type: "button", class: "btn ghost graph-demo-restart", onclick: () => this.start() },
      icon("refresh", "sm"), t("graph.demo.restart"));
    const closeBtn = h("button", { type: "button", class: "icon-btn", title: t("action.close"), "aria-label": t("action.close"), onclick: () => this.close() }, icon("x"));
    this.dialog = h("div", { class: "graph-demo", role: "dialog", "aria-modal": "true", "aria-label": t("graph.demo.title") },
      h("div", { class: "graph-demo-head" }, h("h3", null, t("graph.demo.title")), layoutBar, closeBtn),
      this.stage,
      this.caption,
      h("div", { class: "graph-demo-foot" }, this.progress, h("div", { class: "spacer" }), this.restartBtn, this.nextBtn, this.playBtn));
    this.backdrop = h("div", { class: "modal-backdrop graph-demo-backdrop", onclick: (event) => { if (event.target === this.backdrop) this.close(); } }, this.dialog);
    this._onKey = (event) => {
      if (!event.isTrusted) return;
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        this.close();
        return;
      }
      // The sample graph only follows the script; keys aimed at it (or at its
      // title editor) stop here.  The dialog's buttons keep Tab, Enter, Space.
      if (this.stage.contains(event.target)) {
        event.preventDefault();
        event.stopPropagation();
      }
    };
    document.addEventListener("keydown", this._onKey, true);
    (document.fullscreenElement || document.body).appendChild(this.backdrop);
    setTimeout(() => this.playBtn.focus(), 30);
  }

  // ------------------------------------------------------------------ sandbox
  _makeGraph() {
    this.graph?.destroy();
    clear(this.graphHost);
    let graph = null;
    const options = demoGraphOptions({ newTitle: t("mind.new_node"), getGraph: () => graph });
    graph = new GraphView(this.graphHost, {
      ...options,
      layout: this.layout,
      mode: DEMO_START_MODE[this.layout],
      sandbox: true,
      allRelations: true,
      persistLayout: false,
      persistMode: false,
      edgeFilterKey: "ark.graphDemo",
      nodeGraphKey: "ark.graphDemo.node",
    });
    // Synthetic pointers have no capture to take.
    graph.canvas.setPointerCapture = () => {};
    graph.canvas.releasePointerCapture = () => {};
    const data = demoDataset(this.layout, t);
    graph.setData(data.nodes, data.edges, { layout: this.layout });
    this.graph = graph;
    const rect = this.stage.getBoundingClientRect();
    this._placeCursor(rect.width * 0.82, rect.height * 0.8);
  }

  // ------------------------------------------------------------------ controls
  setLayout(layout) {
    if (!DEMO_LAYOUTS.includes(layout)) return;
    this.layout = layout;
    for (const [id, btn] of this.layoutBtns) btn.setAttribute("aria-pressed", String(id === layout));
    this.start();
  }

  start() {
    if (this.run) this.run.cancelled = true;
    this._wake();
    clear(this.keysEl);
    this._makeGraph();
    const steps = DEMO_SCRIPTS[this.layout];
    clear(this.progress);
    this.dots = steps.map(() => this.progress.appendChild(h("span", { class: "graph-demo-dot" })));
    this.stepIndex = -1;
    this.finished = false;
    this.paused = this.stepMode;
    this._showCaption(0, steps.length, t(steps[0].caption));
    this._syncControls();
    const run = { cancelled: false };
    this.run = run;
    this._play(run).catch((err) => { if (!(err instanceof Cancelled)) console.error(err); });
  }

  togglePlay() {
    if (this.finished) { this.stepMode = false; this.start(); return; }
    this.paused = !this.paused;
    if (!this.paused) this.stepMode = false;
    this._wake();
    this._syncControls();
  }

  // Run one step, then wait (step-by-step mode).
  next() {
    if (this.finished) return;
    this.stepMode = true;
    this.paused = false;
    this._wake();
    this._syncControls();
  }

  close() {
    if (this.closed) return;
    this.closed = true;
    if (this.run) this.run.cancelled = true;
    this._wake();
    document.removeEventListener("keydown", this._onKey, true);
    this.graph?.destroy();
    this.graph = null;
    this.backdrop.remove();
    if (OPEN === this) OPEN = null;
    try { this.returnFocus?.focus?.({ preventScroll: true }); } catch { /* element gone */ }
  }

  _syncControls() {
    clear(this.playBtn);
    if (this.finished) this.playBtn.append(icon("refresh", "sm"), t("graph.demo.replay"));
    else if (this.paused) this.playBtn.append(icon("play", "sm"), t("graph.demo.play"));
    else this.playBtn.append(icon("pause", "sm"), t("graph.demo.pause"));
    this.nextBtn.disabled = this.finished || !this.paused;
    this.nextBtn.classList.toggle("primary", this.paused && !this.finished);
    this.nextBtn.classList.toggle("ghost", !(this.paused && !this.finished));
    this.playBtn.classList.toggle("primary", !this.nextBtn.classList.contains("primary"));
    this.playBtn.classList.toggle("ghost", this.nextBtn.classList.contains("primary"));
    this.dialog.dataset.state = this.finished ? "done" : this.paused ? "paused" : "playing";
  }

  _showCaption(index, total, text) {
    this.stepEl.textContent = `${index + 1} / ${total}`;
    this.captionText.textContent = text;
    this.dots?.forEach((dot, i) => {
      dot.classList.toggle("done", i < index);
      dot.classList.toggle("on", i === index);
    });
  }

  // ------------------------------------------------------------------ timing
  _wake() {
    const waiters = this._waiters || [];
    this._waiters = [];
    waiters.forEach((resolve) => resolve());
  }

  async _gate(run) {
    while (this.paused && !run.cancelled) await new Promise((resolve) => { (this._waiters ||= []).push(resolve); });
    if (run.cancelled) throw new Cancelled();
  }

  _scale(ms) { return this.reduced ? Math.min(ms, 160) : ms; }

  // Wait `ms` of playing time (a pause stops the clock).
  async _sleep(run, ms) {
    let left = this._scale(ms);
    while (left > 0) {
      await this._gate(run);
      const slice = Math.min(left, 40);
      await new Promise((resolve) => setTimeout(resolve, slice));
      left -= slice;
    }
    await this._gate(run);
  }

  // Call frame(progress 0..1) on animation frames for `ms` of playing time.
  async _animate(run, ms, frame) {
    const total = Math.max(1, this._scale(ms));
    let elapsed = 0;
    let last = performance.now();
    while (elapsed < total) {
      await this._gate(run);
      const now = await new Promise((resolve) => requestAnimationFrame(resolve));
      if (run.cancelled) throw new Cancelled();
      elapsed += Math.max(0, now - last);
      last = now;
      frame(Math.min(1, elapsed / total));
      if (this.paused) last = performance.now();
    }
  }

  // ------------------------------------------------------------------ player
  async _play(run) {
    const steps = DEMO_SCRIPTS[this.layout];
    await this._sleep(run, 900);
    // Force spreads out while it settles: frame it before the first step.
    if (this.layout === "force") {
      this.graph.fit({ animate: true });
      await this._sleep(run, 400);
    }
    for (let i = 0; i < steps.length; i++) {
      await this._gate(run);
      this.stepIndex = i;
      this._showCaption(i, steps.length, t(steps[i].caption));
      await this._sleep(run, 450);
      for (const op of steps[i].ops) await this._op(run, op);
      await this._sleep(run, DEMO_TIMING.stepPause);
      if (this.stepMode && i < steps.length - 1) {
        this.paused = true;
        this._syncControls();
        this._showCaption(i + 1, steps.length, t(steps[i + 1].caption));
      }
    }
    // Leave the graph: no hover card stays open.
    await this._glide(run, { at: [0.92, 0.88] });
    this.finished = true;
    this.paused = false;
    this.dots.forEach((dot) => { dot.classList.add("done"); dot.classList.remove("on"); });
    this.stepEl.textContent = "";
    this.captionText.textContent = t("graph.demo.done");
    this._syncControls();
  }

  async _op(run, op) {
    const keys = opKeys(op);
    switch (op.op) {
      case "move":
        await this._glide(run, op.to);
        break;
      case "wait":
        await this._sleep(run, op.ms || 0);
        break;
      case "click":
      case "dblclick": {
        await this._glide(run, op.to);
        this._showKeys(keys);
        await this._press(run, 0, {});
        if (op.op === "dblclick") {
          await this._sleep(run, 60);
          await this._press(run, 0, {}, { detail: 2 });
          this._mouse("dblclick", { button: 0, detail: 2 });
        }
        break;
      }
      case "drag": {
        const button = op.button === 2 ? 2 : 0;
        const mods = { altKey: Boolean(op.alt) };
        await this._glide(run, op.from);
        this._showKeys(keys);
        const start = { ...this.cursor };
        this._pointer("pointerdown", { button, buttons: button === 2 ? 2 : 1, ...mods });
        this._cursorState(true, button === 2);
        await this._sleep(run, DEMO_TIMING.press);
        const target = op.to.rel ? { x: start.x + op.to.rel[0], y: start.y + op.to.rel[1] } : null;
        await this._glide(run, target ? { point: target } : op.to, { duration: DEMO_TIMING.drag, buttons: button === 2 ? 2 : 1, mods });
        await this._sleep(run, 120);
        this._release({ button, buttons: 0, ...mods });
        this._cursorState(false);
        break;
      }
      case "wheel": {
        await this._glide(run, op.at);
        for (let i = 0; i < (op.times || 1); i++) {
          this._showKeys(keys);
          this._dispatch(this.graph.canvas, new WheelEvent("wheel", {
            ...this._client(), deltaY: op.deltaY || 120, deltaMode: 0, bubbles: false, cancelable: true,
          }));
          await this._sleep(run, DEMO_TIMING.wheel);
        }
        await this._sleep(run, DEMO_TIMING.press);
        break;
      }
      case "key": {
        this._showKeys(keys);
        const target = this.graph.editor || this.graph.host;
        this._dispatch(target, new KeyboardEvent("keydown", {
          key: op.key, ctrlKey: Boolean(op.ctrl), shiftKey: Boolean(op.shift), bubbles: false, cancelable: true,
        }));
        await this._sleep(run, DEMO_TIMING.key);
        break;
      }
      case "type": {
        const editor = await this._editor(run);
        if (!editor) break;
        this._showKeys(keys);
        editor.value = "";
        for (const ch of t(op.text)) {
          editor.value += ch;
          editor.dispatchEvent(new Event("input"));
          await this._sleep(run, DEMO_TIMING.char);
        }
        await this._sleep(run, DEMO_TIMING.press);
        break;
      }
      case "button": {
        const el = this.graph.host.querySelector(op.selector);
        if (!el) break;
        await this._glide(run, { selector: op.selector });
        this._showKeys(keys);
        this._cursorState(true);
        await this._sleep(run, 140);
        this._cursorState(false);
        el.click();
        await this._sleep(run, DEMO_TIMING.button);
        break;
      }
      default:
        break;
    }
  }

  async _editor(run) {
    for (let i = 0; i < 40 && !this.graph.editor; i++) await this._sleep(run, 40);
    // The editor focuses itself 20 ms after it opens.
    await this._sleep(run, 60);
    return this.graph.editor;
  }

  // ------------------------------------------------------------------ input
  // A target as a point in stage coordinates (the canvas fills the stage).
  _resolve(target) {
    const graph = this.graph;
    if (!target || !graph) return null;
    if (target.point) return target.point;
    if (target.node) {
      const node = graph.byId.get(target.node);
      if (!node) return null;
      const p = graph._screen(node);
      return { x: p.x + (target.dx || 0), y: p.y + (target.dy || 0) };
    }
    if (target.at) return { x: graph.width * target.at[0], y: graph.height * target.at[1] };
    if (target.edge) {
      const edge = graph.edges.find((item) => item.id === target.edge);
      return edge ? graph._nodeEdgeHandle(edge) : null;
    }
    if (target.graph) {
      const points = graph.nodes.map((node) => graph._screen(node));
      if (!points.length) return null;
      const pad = target.pad || 0;
      const x0 = Math.min(...points.map((p) => p.x)) - pad, x1 = Math.max(...points.map((p) => p.x)) + pad;
      const y0 = Math.min(...points.map((p) => p.y)) - pad, y1 = Math.max(...points.map((p) => p.y)) + pad;
      const where = target.graph;
      return {
        x: where.includes("l") ? x0 : where.includes("r") ? x1 : (x0 + x1) / 2,
        y: where.includes("t") ? y0 : where.includes("b") ? y1 : (y0 + y1) / 2,
      };
    }
    if (target.selector) {
      const el = graph.host.querySelector(target.selector);
      if (!el) return null;
      const r = el.getBoundingClientRect(), c = graph.canvas.getBoundingClientRect();
      return { x: r.left + r.width / 2 - c.left, y: r.top + r.height / 2 - c.top };
    }
    return null;
  }

  // Move the cursor to a target (re-resolved every frame: Force nodes keep
  // moving), sending pointermove on the way.
  async _glide(run, target, { duration = DEMO_TIMING.glide, buttons = 0, mods = {} } = {}) {
    const from = { x: this.cursor.x, y: this.cursor.y };
    const ease = (p) => (p < 0.5 ? 2 * p * p : 1 - ((-2 * p + 2) ** 2) / 2);
    await this._animate(run, duration, (p) => {
      const to = this._resolve(target);
      if (!to) return;
      const k = ease(p);
      this._placeCursor(from.x + (to.x - from.x) * k, from.y + (to.y - from.y) * k);
      this._pointer("pointermove", { button: -1, buttons, ...mods });
    });
  }

  async _press(run, button, mods, extra = {}) {
    this._pointer("pointerdown", { button, buttons: 1, ...mods, ...extra });
    this._cursorState(true);
    await this._sleep(run, DEMO_TIMING.press);
    this._release({ button, buttons: 0, ...mods, ...extra });
    this._cursorState(false);
    await this._sleep(run, 80);
  }

  // Release on the canvas.  The graph checks what lies under the release
  // point (a link drop must land on the canvas), so the shield steps aside.
  _release(init) {
    this.shield.style.pointerEvents = "none";
    try { this._pointer("pointerup", init); } finally { this.shield.style.pointerEvents = ""; }
  }

  _client() {
    const rect = this.graph.canvas.getBoundingClientRect();
    return { clientX: rect.left + this.cursor.x, clientY: rect.top + this.cursor.y };
  }

  _pointer(type, init) {
    if (!this.graph) return;
    this._dispatch(this.graph.canvas, new PointerEvent(type, {
      pointerId: 1, pointerType: "mouse", isPrimary: true, bubbles: false, cancelable: true, ...this._client(), ...init,
    }));
  }

  _mouse(type, init) {
    if (!this.graph) return;
    this._dispatch(this.graph.canvas, new MouseEvent(type, { bubbles: false, cancelable: true, ...this._client(), ...init }));
  }

  _dispatch(target, event) {
    try { target.dispatchEvent(event); } catch (err) { console.error(err); }
  }

  _placeCursor(x, y) {
    this.cursor.x = x;
    this.cursor.y = y;
    this.cursorEl.style.transform = `translate(${x}px, ${y}px)`;
  }

  _cursorState(down, right = false) {
    this.cursorEl.classList.toggle("down", down);
    this.cursorEl.classList.toggle("right", down && right);
  }

  // One chip per input; the same input again (wheel notches, arrow
  // presses) counts up on the last chip.
  _showKeys(tokens) {
    if (!tokens.length) return;
    const text = formatKeys(tokens, t);
    let chip = this.keysEl.lastElementChild;
    if (chip && chip.dataset.text === text && !chip.classList.contains("fade")) {
      chip.dataset.count = String(Number(chip.dataset.count) + 1);
      chip.textContent = `${text} ×${chip.dataset.count}`;
    } else {
      chip = h("kbd", { class: "graph-demo-key", dataset: { text, count: "1" } }, text);
      this.keysEl.appendChild(chip);
      while (this.keysEl.children.length > 3) this.keysEl.firstElementChild.remove();
    }
    clearTimeout(chip._fade);
    chip._fade = setTimeout(() => {
      chip.classList.add("fade");
      setTimeout(() => chip.remove(), 400);
    }, KEY_CHIP_MS);
  }
}
