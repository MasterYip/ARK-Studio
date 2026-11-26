// The graph's 3D view: the nodes and links of a GraphView (graph.js) laid
// out by a 3D force simulation (graph3d-force.js) and drawn with three.js
// in perspective.  Nodes are camera-facing billboards (one instanced draw:
// the 2D Force node look from an atlas), links one line batch, labels
// camera-facing sprites for near, hovered and selected nodes; depth fades
// everything toward the background.  three.js (vendor/three, MIT) is loaded
// on first use only.
//
// The GraphView stays the owner of the data, the relation filter and the
// selection: it calls sync() wherever it would redraw, and a click here
// selects through it, so the inspector, filters and search behave as in 2D.
import { h } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { typeColor, drawForceNode, forceSettleStep } from "./graph-core.js";
import { createSim3D, setSimLinks, stepForce3D, simBounds, nodeRadius3D } from "./graph3d-force.js";
import {
  nodeStates, pickLabels, pickNode, fitDistance, pixelRatioCap, normalizeGraph3DSettings,
} from "./graph3d-model.js";

let THREE_LOAD = null;
/** three.js, imported once on first use. */
export function loadThree() {
  THREE_LOAD ||= import("../vendor/three/three.module.min.js").catch((error) => { THREE_LOAD = null; throw error; });
  return THREE_LOAD;
}

/** True when the browser can create a WebGL context at all. */
export function webglSupported(doc = globalThis.document) {
  try {
    const canvas = doc.createElement("canvas");
    const gl = canvas.getContext("webgl2") || canvas.getContext("webgl");
    gl?.getExtension?.("WEBGL_lose_context")?.loseContext?.();
    return Boolean(gl);
  } catch { return false; }
}

const SEED = 7;
const FOV = 50;
const CELL = 64;           // atlas cell, px
const DISC = 21;           // node disc radius inside a cell, px
const QUAD = CELL / 2 / DISC; // quad half-size per unit of node radius
const LABEL_BUDGET = 32;
const LABEL_CACHE = 320;
const TICK_BUDGET_MS = 7;
const CLICK_SLOP = 4; // px between press and release that still count as a click

const NODE_VERT = `
attribute vec3 aPos; attribute float aSize; attribute float aCell; attribute float aState;
uniform float uCols; uniform float uFogNear; uniform float uFogFar;
varying vec2 vUv; varying vec2 vLocal; varying float vFog; varying float vState;
void main() {
  vec4 mv = modelViewMatrix * vec4(aPos, 1.0);
  float grow = aState > 3.5 ? 1.3 : aState > 2.5 ? 1.2 : 1.0;
  mv.xy += position.xy * 2.0 * aSize * grow;
  gl_Position = projectionMatrix * mv;
  float col = mod(aCell, uCols), row = floor(aCell / uCols);
  vUv = vec2((col + uv.x) / uCols, 1.0 - (row + 1.0 - uv.y) / uCols);
  vLocal = uv * 2.0 - 1.0;
  vFog = smoothstep(uFogNear, uFogFar, -mv.z);
  vState = aState;
}`;

const NODE_FRAG = `
uniform sampler2D uAtlas; uniform vec3 uFogColor; uniform vec3 uAccent; uniform vec3 uInk;
varying vec2 vUv; varying vec2 vLocal; varying float vFog; varying float vState;
void main() {
  vec4 c = texture2D(uAtlas, vUv);
  float r = length(vLocal);
  if (vState > 2.5) {
    float ring = smoothstep(0.80, 0.84, r) * (1.0 - smoothstep(0.93, 0.97, r));
    c.rgb = mix(c.rgb, vState > 3.5 ? uAccent : uInk, ring);
    c.a = max(c.a, ring);
  }
  if (c.a < 0.03) discard;
  vec3 col = c.rgb;
  if (vState > 0.5 && vState < 1.5) col = mix(col, uFogColor, 0.7);
  col = mix(col, uFogColor, vFog * 0.88);
  gl_FragColor = vec4(col, c.a);
}`;

const EDGE_VERT = `
attribute vec4 aColor;
uniform float uFogNear; uniform float uFogFar;
varying vec4 vColor; varying float vFog;
void main() {
  vec4 mv = modelViewMatrix * vec4(position, 1.0);
  gl_Position = projectionMatrix * mv;
  vColor = aColor;
  vFog = smoothstep(uFogNear, uFogFar, -mv.z);
}`;

const EDGE_FRAG = `
uniform vec3 uFogColor;
varying vec4 vColor; varying float vFog;
void main() {
  gl_FragColor = vec4(mix(vColor.rgb, uFogColor, vFog * 0.9), vColor.a * (1.0 - vFog * 0.75));
}`;

function rgb(hex) {
  const s = String(hex || "").trim();
  const m = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(s);
  if (m) {
    const v = m[1].length === 3 ? m[1].split("").map((c) => c + c).join("") : m[1];
    return [parseInt(v.slice(0, 2), 16) / 255, parseInt(v.slice(2, 4), 16) / 255, parseInt(v.slice(4, 6), 16) / 255];
  }
  const p = /rgba?\(([^)]+)\)/i.exec(s);
  if (p) { const [r, g, b] = p[1].split(/[ ,/]+/).map(Number); return [r / 255, g / 255, b / 255]; }
  return [0.58, 0.64, 0.71];
}

export class Graph3DView {
  /**
   * @param {object} graph   the GraphView (graph.js) that owns data and selection
   * @param {object} options { settings, onSettings(settings), onFail(error) }
   */
  constructor(graph, { settings = null, onSettings = null, onFail = null } = {}) {
    this.graph = graph;
    this.settings = normalizeGraph3DSettings(settings);
    this.onSettings = onSettings;
    this.onFail = onFail;
    this.destroyed = false;
    this.ready = false;
    this.hover = -1;
    this.frame = 0;
    this.pointers = new Map();
    this.labelCache = new Map();
    this.labels = [];
    this.stats = { frames: 0, frameMs: [] };
    this._buildDom();
  }

  _buildDom() {
    const g = this.graph;
    this.canvas = h("canvas", { class: "graph3d-canvas", "aria-label": t("graph3d.canvas") });
    this.rotateBtn = h("button", {
      type: "button", class: "graph3d-btn", "aria-pressed": "false", title: t("graph3d.rotate"), "aria-label": t("graph3d.rotate"),
      onclick: () => this.setSettings({ autoRotate: !this.settings.autoRotate }),
    }, icon("play", "sm"));
    this.settingsEl = h("div", { class: "graph3d-settings hidden", role: "dialog", "aria-label": t("graph3d.settings") });
    // The 3D buttons join the graph's toolbar (after its 3D toggle), so they
    // wrap with it on narrow screens; the settings open under the toolbar.
    this.hud = h("span", { class: "graph3d-tools" },
      h("span", { class: "sep" }),
      h("button", {
        type: "button", class: "graph3d-btn", title: t("graph3d.reset"), "aria-label": t("graph3d.reset"),
        onclick: () => this.resetView(true),
      }, icon("refresh", "sm")),
      this.rotateBtn,
      h("button", {
        type: "button", class: "graph3d-btn", title: t("graph3d.settings"), "aria-label": t("graph3d.settings"),
        "aria-expanded": "false", onclick: (event) => this._toggleSettings(event.currentTarget),
      }, icon("settings", "sm")));
    this.statusEl = h("div", { class: "graph3d-status" }, t("graph3d.loading"));
    this.hintEl = h("div", { class: "graph3d-hint" }, globalThis.matchMedia?.("(pointer: coarse)")?.matches ? t("graph3d.hint_view") : t("graph3d.hint"));
    this.layer = h("div", { class: "graph3d-layer" }, this.canvas, this.statusEl, this.hintEl);
    g.host.append(this.layer, this.settingsEl);
    if (g.view3dBtn) g.view3dBtn.after(this.hud);
    else this.layer.appendChild(this.hud);
    this._syncHud();
  }

  async start() {
    let THREE;
    try {
      THREE = await loadThree();
      if (this.destroyed) return false;
      this.THREE = THREE;
      THREE.ColorManagement.enabled = false;
      this.renderer = new THREE.WebGLRenderer({
        canvas: this.canvas, antialias: !this.settings.lowPower, alpha: false,
        powerPreference: this.settings.lowPower ? "low-power" : "high-performance",
      });
    } catch (error) {
      this.onFail?.(error);
      return false;
    }
    this.renderer.outputColorSpace = THREE.LinearSRGBColorSpace;
    this.canvas.addEventListener("webglcontextlost", (event) => { event.preventDefault(); this.onFail?.(new Error("context lost")); });
    this.scene = new THREE.Scene();
    this.camera = new THREE.PerspectiveCamera(FOV, 1, 1, 100000);
    this.view = { target: new THREE.Vector3(), radius: 800, theta: 0.6, phi: 1.15 };
    this.spin = { theta: 0, phi: 0 };
    this._initMaterials();
    this._bind();
    this.ready = true;
    this.statusEl.remove();
    this._resizeObserver = new ResizeObserver(() => this._resize());
    this._resizeObserver.observe(this.layer);
    this._resize();
    this.sync(true);
    return true;
  }

  _initMaterials() {
    const THREE = this.THREE;
    const fog = { uFogNear: { value: 400 }, uFogFar: { value: 2000 }, uFogColor: { value: new THREE.Color() } };
    this.fog = fog;
    this.atlasCanvas = document.createElement("canvas");
    this.atlas = this._atlasTexture();
    this.nodeMaterial = new THREE.ShaderMaterial({
      vertexShader: NODE_VERT, fragmentShader: NODE_FRAG,
      uniforms: { ...fog, uAtlas: { value: this.atlas }, uCols: { value: 16 }, uAccent: { value: new THREE.Color() }, uInk: { value: new THREE.Color() } },
      transparent: false, alphaToCoverage: !this.settings.lowPower,
    });
    this.edgeMaterial = new THREE.ShaderMaterial({
      vertexShader: EDGE_VERT, fragmentShader: EDGE_FRAG,
      uniforms: { ...fog }, transparent: true, depthWrite: false,
    });
  }

  // ---- data -----------------------------------------------------------------

  _atlasTexture() {
    const texture = new this.THREE.CanvasTexture(this.atlasCanvas);
    texture.minFilter = this.THREE.LinearMipmapLinearFilter;
    texture.anisotropy = 4;
    return texture;
  }

  _newAtlasTexture() {
    this.atlas.dispose();
    this.atlas = this._atlasTexture();
    this.nodeMaterial.uniforms.uAtlas.value = this.atlas;
  }

  /** Called by the GraphView wherever it would redraw. */
  sync(force = false) {
    if (!this.ready || this.destroyed) return;
    const g = this.graph;
    const relations = g.visibleRelations;
    const relSig = [...relations].sort().join(",");
    const physicsSig = JSON.stringify(g.physics);
    const dataChanged = force || g.nodes !== this._nodesRef || g.edges !== this._edgesRef
      || g.nodes.length !== this._nodeCount || g.edges.length !== this._edgeCount;
    if (dataChanged) this._setData();
    else if (relSig !== this._relSig) { setSimLinks(this.sim, g.edges, relations); this._buildEdges(); this._reheat(0.3); }
    else if (physicsSig !== this._physicsSig) this._reheat(0.5);
    this._relSig = relSig;
    this._physicsSig = physicsSig;
    const palette = g._palette();
    if (palette.key !== this._paletteKey) { this._paletteKey = palette.key; this._applyPalette(palette); }
    else if (this._atlasKeys && this.nodes.some((node) => !this._atlasKeys.has(this._cellKey(node)))) this._buildAtlas();
    this._statesDirty = true;
    this._kick();
  }

  _cellKey(node) { return `${node.type || ""}|${node.status || ""}|${node.for_me ? 1 : 0}`; }

  _setData() {
    const g = this.graph;
    this._nodesRef = g.nodes; this._edgesRef = g.edges;
    this._nodeCount = g.nodes.length; this._edgeCount = g.edges.length;
    const previous = g._pos3d || new Map();
    if (this.sim) for (let i = 0; i < this.sim.n; i++) {
      previous.set(this.sim.ids[i], { x: this.sim.x[i], y: this.sim.y[i], z: this.sim.z[i] });
    }
    g._pos3d = previous;
    this.nodes = g.nodes;
    const fresh = this.nodes.filter((node) => !previous.has(node.id)).length;
    this.sim = createSim3D(this.nodes, { seed: SEED, previous });
    for (let i = 0; i < this.sim.n; i++) this.sim.r[i] = nodeRadius3D(this.nodes[i]);
    setSimLinks(this.sim, g.edges, g.visibleRelations);
    this._buildNodes();
    this._buildEdges();
    if (this._atlasKeys) this._buildAtlas();
    const allNew = fresh === this.nodes.length;
    this._reheat(allNew ? 1 : fresh ? 0.4 : 0.12);
    if (allNew && this.nodes.length) {
      // A fresh layout: settle the first part synchronously (bounded), so
      // the first frame is not an exploding ball.
      const until = performance.now() + Math.min(450, 60 + this.nodes.length * 0.2);
      while (performance.now() < until && this.sim.ticks < 220) stepForce3D(this.sim, this.nodes, this._physics());
      this.resetView(false);
    } else if (!this._viewSet) this.resetView(false);
  }

  _physics() { return { ...this.graph.physics }; }

  _buildNodes() {
    const THREE = this.THREE, n = this.sim.n;
    if (this.nodeMesh) { this.scene.remove(this.nodeMesh); this.nodeMesh.geometry.dispose(); }
    const geometry = new THREE.InstancedBufferGeometry();
    const quad = new THREE.PlaneGeometry(1, 1);
    geometry.index = quad.index;
    geometry.setAttribute("position", quad.getAttribute("position"));
    geometry.setAttribute("uv", quad.getAttribute("uv"));
    const attr = (name, size) => {
      const a = new THREE.InstancedBufferAttribute(new Float32Array(Math.max(1, n) * size), size);
      a.setUsage(THREE.DynamicDrawUsage);
      geometry.setAttribute(name, a);
      return a;
    };
    this.aPos = attr("aPos", 3); this.aSize = attr("aSize", 1); this.aCell = attr("aCell", 1);
    this.aState = attr("aState", 1);
    geometry.instanceCount = n;
    this.nodeMesh = new THREE.Mesh(geometry, this.nodeMaterial);
    this.nodeMesh.frustumCulled = false;
    this.nodeMesh.renderOrder = 1;
    this.scene.add(this.nodeMesh);
    this._sizesDirty = true;
    this.proj = { x: new Float32Array(n), y: new Float32Array(n), z: new Float32Array(n), r: new Float32Array(n), w: new Float32Array(n) };
  }

  _buildEdges() {
    const THREE = this.THREE;
    const count = this.sim.links.length / 2;
    if (this.edgeMesh) { this.scene.remove(this.edgeMesh); this.edgeMesh.geometry.dispose(); }
    const geometry = new THREE.BufferGeometry();
    this.ePos = new THREE.BufferAttribute(new Float32Array(Math.max(1, count) * 6), 3).setUsage(THREE.DynamicDrawUsage);
    this.eColor = new THREE.BufferAttribute(new Float32Array(Math.max(1, count) * 8), 4).setUsage(THREE.DynamicDrawUsage);
    geometry.setAttribute("position", this.ePos);
    geometry.setAttribute("aColor", this.eColor);
    geometry.setDrawRange(0, count * 2);
    this.edgeMesh = new THREE.LineSegments(geometry, this.edgeMaterial);
    this.edgeMesh.frustumCulled = false;
    this.scene.add(this.edgeMesh);
    this._statesDirty = true;
  }

  _applyPalette(pal) {
    this.pal = pal;
    const bg = rgb(pal.bg);
    this.scene.background = new this.THREE.Color(...bg);
    this.fog.uFogColor.value.setRGB(...bg);
    this.nodeMaterial.uniforms.uAccent.value.setRGB(...rgb(pal.accent));
    this.nodeMaterial.uniforms.uInk.value.setRGB(...rgb(pal.ink));
    this._buildAtlas();
    for (const entry of this.labelCache.values()) entry.texture.dispose();
    this.labelCache.clear();
    for (const sprite of this.labels) sprite.visible = false;
  }

  // One cell per (type, status, for_me) drawn by the 2D Force node painter.
  _buildAtlas() {
    const keys = new Map();
    for (const node of this.nodes) {
      const key = this._cellKey(node);
      if (!keys.has(key)) keys.set(key, { type: node.type, status: node.status, for_me: node.for_me });
    }
    const cols = Math.max(4, 2 ** Math.ceil(Math.log2(Math.ceil(Math.sqrt(keys.size + 1)))));
    const canvas = this.atlasCanvas;
    const size = cols * CELL;
    if (canvas.width !== size || canvas.height !== size) {
      canvas.width = canvas.height = size;
      // three.js allocates a WebGL2 texture once (immutable storage): an
      // atlas of another size uploaded into it lands in the wrong place and
      // nodes render as quarter or half discs.  A new size, a new texture.
      if (this._atlasUploaded !== undefined && this._atlasUploaded !== size) this._newAtlasTexture();
    }
    const ctx = canvas.getContext("2d");
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    this._atlasKeys = new Map();
    let cell = 0;
    for (const [key, node] of keys) {
      const cx = (cell % cols) * CELL + CELL / 2, cy = Math.floor(cell / cols) * CELL + CELL / 2;
      drawForceNode(ctx, node, cx, cy, DISC, typeColor(node.type), this.pal?.bg || "#0f141c");
      this._atlasKeys.set(key, cell++);
    }
    this.nodeMaterial.uniforms.uCols.value = cols;
    this.atlas.needsUpdate = true;
    this._atlasUploaded = size;
    if (this.aCell) {
      for (let i = 0; i < this.nodes.length; i++) this.aCell.array[i] = this._atlasKeys.get(this._cellKey(this.nodes[i])) ?? 0;
      this.aCell.needsUpdate = true;
    }
  }

  // ---- simulation -----------------------------------------------------------

  _reheat(alpha) {
    if (!this.sim) return;
    this.sim.alpha = Math.max(this.sim.alpha, alpha);
    this.settle = { ticks: 0, quiet: 0 };
    this.running = this.sim.n > 0;
    this._kick();
  }

  _focal() { return (this.height || 600) / 2 / Math.tan((FOV * Math.PI) / 360); }

  _pxPerUnit() {
    const height = this.height || 600;
    return height / 2 / (Math.max(1, this.view.radius) * Math.tan((FOV * Math.PI) / 360));
  }

  _tick() {
    if (!this.running) return;
    const cfg = this._physics();
    // Small graphs take two ticks a frame (within the budget), large ones one.
    const start = performance.now();
    const most = this.sim.n < 600 ? 2 : 1;
    for (let k = 0; k < most; k++) {
      const motion = stepForce3D(this.sim, this.nodes, cfg, { dragged: this.drag?.i ?? -1 });
      const verdict = forceSettleStep(this.settle, motion, this._pxPerUnit(), this.sim.alpha, Boolean(this.drag));
      if (verdict !== "run") { this.running = false; break; }
      if (performance.now() - start > TICK_BUDGET_MS) break;
    }
    this._posDirty = true;
  }

  // ---- frame ----------------------------------------------------------------

  _kick() {
    if (this.frame || this.destroyed || !this.ready) return;
    this.frame = requestAnimationFrame((now) => this._frame(now));
  }

  _frame(now) {
    this.frame = 0;
    if (this.destroyed) return;
    const t0 = performance.now();
    let more = false;
    if (this.running) { this._tick(); more = true; }
    if (this._animateView(now)) more = true;
    if (this.settings.autoRotate && !this.drag && !this.pointers.size) { this.view.theta += 0.0025; more = true; }
    this._placeCamera();
    if (this._posDirty || this._statesDirty || this._sizesDirty || this._camDirty) this._update();
    this.renderer.render(this.scene, this.camera);
    this.stats.frames++;
    const spent = performance.now() - t0;
    this.stats.frameMs.push(spent);
    if (this.stats.frameMs.length > 240) this.stats.frameMs.shift();
    if (more) this._kick();
  }

  _animateView(now) {
    let moving = false;
    const a = this.flight;
    if (a) {
      const k = Math.min(1, (now - a.start) / a.ms);
      const e = k < 0.5 ? 4 * k * k * k : 1 - (-2 * k + 2) ** 3 / 2;
      this.view.target.lerpVectors(a.fromTarget, a.toTarget, e);
      this.view.radius = a.fromRadius + (a.toRadius - a.fromRadius) * e;
      if (a.toTheta !== undefined) this.view.theta = a.fromTheta + (a.toTheta - a.fromTheta) * e;
      if (a.toPhi !== undefined) this.view.phi = a.fromPhi + (a.toPhi - a.fromPhi) * e;
      if (k >= 1) this.flight = null;
      moving = true;
    }
    // Orbit inertia after a drag.
    if (!this.pointers.size && (Math.abs(this.spin.theta) > 1e-4 || Math.abs(this.spin.phi) > 1e-4)) {
      this.view.theta += this.spin.theta; this._setPhi(this.view.phi + this.spin.phi);
      this.spin.theta *= 0.9; this.spin.phi *= 0.9;
      moving = true;
    }
    return moving;
  }

  _setPhi(phi) { this.view.phi = Math.max(0.05, Math.min(Math.PI - 0.05, phi)); }

  _placeCamera() {
    const { target, radius, theta, phi } = this.view;
    const s = Math.sin(phi);
    const key = `${target.x},${target.y},${target.z},${radius},${theta},${phi}`;
    if (key === this._camKey) return;
    this._camKey = key;
    this.camera.position.set(target.x + radius * s * Math.sin(theta), target.y + radius * Math.cos(phi), target.z + radius * s * Math.cos(theta));
    this.camera.lookAt(target);
    this.camera.near = Math.max(0.5, radius / 200);
    this.camera.far = radius * 4 + (this.bounds?.radius || 1000) * 4;
    this.camera.updateProjectionMatrix();
    this.camera.updateMatrixWorld();
    const reach = this.bounds?.radius || 500;
    this.fog.uFogNear.value = Math.max(1, radius - reach * 0.2);
    this.fog.uFogFar.value = radius + reach * 1.6;
    this._camDirty = true;
  }

  _update() {
    const sim = this.sim, n = sim.n;
    const scale = this.settings.nodeSize;
    if (this._posDirty || this._sizesDirty) {
      const p = this.aPos.array;
      for (let i = 0; i < n; i++) { p[i * 3] = sim.x[i]; p[i * 3 + 1] = sim.y[i]; p[i * 3 + 2] = sim.z[i]; }
      this.aPos.needsUpdate = true;
      const e = this.ePos.array, links = sim.links;
      for (let l = 0, k = 0; l < links.length; l += 2, k += 6) {
        const a = links[l], b = links[l + 1];
        e[k] = sim.x[a]; e[k + 1] = sim.y[a]; e[k + 2] = sim.z[a];
        e[k + 3] = sim.x[b]; e[k + 4] = sim.y[b]; e[k + 5] = sim.z[b];
      }
      this.ePos.needsUpdate = true;
      if (this._posDirty || !this.bounds) this.bounds = simBounds(sim);
    }
    if (this._sizesDirty) {
      for (let i = 0; i < n; i++) this.aSize.array[i] = sim.r[i] * scale * QUAD;
      this.aSize.needsUpdate = true;
      if (this._atlasKeys) for (let i = 0; i < n; i++) this.aCell.array[i] = this._atlasKeys.get(this._cellKey(this.nodes[i])) ?? 0;
      this.aCell.needsUpdate = true;
    }
    if (this._statesDirty) this._updateStates();
    this._project();
    this._updateLabels();
    this._posDirty = this._statesDirty = this._sizesDirty = this._camDirty = false;
  }

  _updateStates() {
    const g = this.graph;
    const hover = this.hover >= 0 ? this.sim.ids[this.hover] : null;
    const selected = new Set(g.selectedNodes || []);
    if (g.selected) selected.add(g.selected);
    this.states = nodeStates(this.sim.ids, g.edges, { selected, hover, relations: g.visibleRelations });
    for (let i = 0; i < this.sim.n; i++) this.aState.array[i] = this.states[i];
    this.aState.needsUpdate = true;
    // Links: like the 2D Force map, neutral, the focused ones in the accent.
    const sub = rgb(this.pal?.sub), accent = rgb(this.pal?.accent);
    const focused = this.states.some((s) => s > 0);
    const c = this.eColor.array, links = this.sim.links;
    for (let l = 0, k = 0; l < links.length; l += 2, k += 8) {
      const sa = this.states[links[l]], sb = this.states[links[l + 1]];
      const active = focused && (sa >= 3 || sb >= 3);
      const col = active ? accent : sub;
      const alpha = active ? 0.9 : focused ? 0.1 : 0.34;
      c[k] = c[k + 4] = col[0]; c[k + 1] = c[k + 5] = col[1]; c[k + 2] = c[k + 6] = col[2]; c[k + 3] = c[k + 7] = alpha;
    }
    this.eColor.needsUpdate = true;
  }

  // Screen positions, depth and radius of every node (picking and labels).
  _project() {
    const THREE = this.THREE, sim = this.sim, n = sim.n, p = this.proj;
    const m = (this._vp ||= new THREE.Matrix4()).multiplyMatrices(this.camera.projectionMatrix, this.camera.matrixWorldInverse).elements;
    const w2 = this.width / 2, h2 = this.height / 2;
    const focal = h2 / Math.tan((FOV * Math.PI) / 360);
    const scale = this.settings.nodeSize;
    for (let i = 0; i < n; i++) {
      const x = sim.x[i], y = sim.y[i], z = sim.z[i];
      const cw = m[3] * x + m[7] * y + m[11] * z + m[15];
      if (cw <= 0.01) { p.z[i] = 2; p.w[i] = -1; continue; }
      p.x[i] = ((m[0] * x + m[4] * y + m[8] * z + m[12]) / cw + 1) * w2;
      p.y[i] = (1 - (m[1] * x + m[5] * y + m[9] * z + m[13]) / cw) * h2;
      p.z[i] = (m[2] * x + m[6] * y + m[10] * z + m[14]) / cw;
      p.w[i] = cw;
      p.r[i] = sim.r[i] * scale * focal / cw;
    }
  }

  // Labels reach from the nearest visible node `labelDistance` × 1.2 layout
  // radii further back (0: only hovered and selected nodes).
  _labelReach(nearest) {
    const radius = this.bounds?.radius || 500;
    if (!(this.settings.labelDistance > 0)) return 0;
    return nearest + this.settings.labelDistance * radius * 1.2;
  }

  _labelTexture(node) {
    const key = node.id;
    const title = String(node.title || node.uid || node.id || "").replace(/\s+/g, " ").trim();
    const text = title.length > 40 ? `${title.slice(0, 39)}…` : title;
    const hit = this.labelCache.get(key);
    if (hit && hit.text === text) { this.labelCache.delete(key); this.labelCache.set(key, hit); return hit; }
    hit?.texture.dispose();
    const px = 26, pad = 10;
    const font = `600 ${px}px system-ui, "Segoe UI", "Microsoft YaHei", sans-serif`;
    const canvas = document.createElement("canvas");
    let ctx = canvas.getContext("2d");
    ctx.font = font;
    const width = Math.ceil(Math.min(720, ctx.measureText(text).width) + pad * 2);
    canvas.width = width; canvas.height = px + pad * 1.4;
    ctx = canvas.getContext("2d");
    ctx.font = font;
    ctx.fillStyle = this.pal?.card || "#171f2b";
    ctx.globalAlpha = 0.82;
    const r = 9, w = canvas.width, hh = canvas.height;
    ctx.beginPath(); ctx.moveTo(r, 0); ctx.arcTo(w, 0, w, hh, r); ctx.arcTo(w, hh, 0, hh, r); ctx.arcTo(0, hh, 0, 0, r); ctx.arcTo(0, 0, w, 0, r); ctx.fill();
    ctx.globalAlpha = 1;
    ctx.fillStyle = this.pal?.ink || "#e8eef6";
    ctx.textBaseline = "middle";
    ctx.fillText(text, pad, hh / 2 + 1, width - pad * 2);
    const texture = new this.THREE.CanvasTexture(canvas);
    texture.minFilter = this.THREE.LinearFilter;
    texture.generateMipmaps = false;
    const entry = { text, texture, aspect: canvas.width / canvas.height };
    this.labelCache.set(key, entry);
    while (this.labelCache.size > LABEL_CACHE) {
      const [oldest, value] = this.labelCache.entries().next().value;
      value.texture.dispose();
      this.labelCache.delete(oldest);
    }
    return entry;
  }

  _updateLabels() {
    const sim = this.sim, p = this.proj;
    const forced = [];
    if (this.hover >= 0) forced.push(this.hover);
    for (let i = 0; i < sim.n; i++) if (this.states?.[i] >= 4) forced.push(i);
    if (this.hover >= 0) for (let i = 0; i < sim.n; i++) if (this.states?.[i] === 2) forced.push(i);
    const margin = 40;
    const onScreen = (i) => p.w[i] > 0 && p.z[i] < 1 && p.x[i] > -margin && p.x[i] < this.width + margin && p.y[i] > -margin && p.y[i] < this.height + margin;
    let nearest = Infinity;
    for (let i = 0; i < sim.n; i++) if (p.w[i] < nearest && onScreen(i)) nearest = p.w[i];
    const picks = pickLabels({ n: sim.n, dist: p.w, reach: this._labelReach(nearest), budget: LABEL_BUDGET, forced: forced.slice(0, LABEL_BUDGET), onScreen });
    const THREE = this.THREE;
    while (this.labels.length < picks.length) {
      const sprite = new THREE.Sprite(new THREE.SpriteMaterial({ transparent: true, depthWrite: false, depthTest: true, fog: false }));
      sprite.renderOrder = 3;
      this.scene.add(sprite);
      this.labels.push(sprite);
    }
    // On-screen height: 13 px at the orbit target's distance, larger nearer
    // and smaller further back, within 10–18 px.
    const focal = this._focal();
    // Screen-space declutter: picks come forced first, then nearest first;
    // a label that would overlap one already placed is skipped.
    const placed = [], shown = [];
    for (const pick of picks) {
      const i = pick.i;
      const entry = this._labelTexture(this.nodes[i]);
      const px = Math.max(pick.forced ? 13 : 10, Math.min(18, 13 * Math.sqrt(this.view.radius / Math.max(1, p.w[i]))));
      const w = px * entry.aspect, top = p.y[i] - p.r[i] * 1.05 - px;
      const box = { x0: p.x[i] - w / 2 - 2, x1: p.x[i] + w / 2 + 2, y0: top - 1, y1: top + px + 1 };
      if (!pick.forced && placed.some((b) => b.x0 < box.x1 && box.x0 < b.x1 && b.y0 < box.y1 && box.y0 < b.y1)) continue;
      placed.push(box);
      shown.push({ ...pick, entry, px, box });
    }
    this.labelStats = { candidates: picks.length, shown: shown.length, boxes: shown.map((pick) => pick.box) };
    for (let k = 0; k < this.labels.length; k++) {
      const sprite = this.labels[k];
      const pick = shown[k];
      if (!pick) { sprite.visible = false; continue; }
      const i = pick.i;
      const entry = pick.entry;
      if (sprite.material.map !== entry.texture) { sprite.material.map = entry.texture; sprite.material.needsUpdate = true; }
      const hgt = pick.px * p.w[i] / focal;
      sprite.scale.set(hgt * entry.aspect, hgt, 1);
      sprite.center.set(0.5, -(sim.r[i] * this.settings.nodeSize * 1.05) / hgt);
      sprite.position.set(sim.x[i], sim.y[i], sim.z[i]);
      const dim = this.states?.[i] === 1 ? 0.35 : 1;
      sprite.material.opacity = pick.alpha * dim;
      // Hovered and selected labels (and the hovered node's neighbours) stay
      // readable in front of other nodes.
      const front = Boolean(pick.forced);
      if (sprite.material.depthTest === front) { sprite.material.depthTest = !front; sprite.material.needsUpdate = true; }
      sprite.renderOrder = front ? 4 : 3;
      sprite.visible = true;
    }
  }

  // ---- camera ---------------------------------------------------------------

  resetView(animate = true) {
    if (!this.sim) return;
    const b = simBounds(this.sim);
    this.bounds = b;
    const THREE = this.THREE;
    // Like the 2D fit, a small graph is not blown up: at most 1.3 px per unit
    // at the centre.
    const fit = Math.max(fitDistance(b.radius, FOV, (this.width || 1) / (this.height || 1)), this._focal() / 1.3);
    const to = { target: new THREE.Vector3(b.cx, b.cy, b.cz), radius: fit, theta: 0.6, phi: 1.15 };
    this._viewSet = true;
    if (!animate) {
      this.flight = null;
      this.view.target.copy(to.target); this.view.radius = to.radius; this.view.theta = to.theta; this.view.phi = to.phi;
    } else this._fly(to.target, to.radius, 700, { theta: to.theta, phi: to.phi });
    this._kick();
  }

  _fly(target, radius, ms = 650, { theta, phi } = {}) {
    // Turn the shorter way round.
    const fromTheta = this.view.theta;
    const toTheta = theta === undefined ? undefined : fromTheta + Math.atan2(Math.sin(theta - fromTheta), Math.cos(theta - fromTheta));
    this.flight = {
      start: performance.now(), ms, fromTarget: this.view.target.clone(), toTarget: target.clone(),
      fromRadius: this.view.radius, toRadius: radius, fromTheta, toTheta, fromPhi: this.view.phi, toPhi: phi,
    };
    this.spin.theta = this.spin.phi = 0;
    this._kick();
  }

  /** Fly to a node (double-click, "Locate"). */
  flyTo(index) {
    if (index < 0 || !this.sim) return;
    const r = this.sim.r[index] * this.settings.nodeSize;
    const target = new this.THREE.Vector3(this.sim.x[index], this.sim.y[index], this.sim.z[index]);
    // Close enough that the node is ~24 px in radius.
    this._fly(target, Math.max(40, r * this._focal() / 24), 750);
  }

  locate(node) {
    const i = this.sim?.index.get(node.id);
    if (i === undefined) return false;
    this._statesDirty = true;
    this.flyTo(i);
    return true;
  }

  zoomBy(factor) {
    this.flight = null;
    this.view.radius = Math.max(5, Math.min(1e5, this.view.radius / factor));
    this._kick();
  }

  // ---- input ----------------------------------------------------------------

  _bind() {
    const c = this.canvas;
    c.addEventListener("pointerdown", (e) => this._down(e));
    c.addEventListener("pointermove", (e) => this._move(e));
    c.addEventListener("pointerup", (e) => this._up(e));
    c.addEventListener("pointercancel", (e) => this._up(e, true));
    c.addEventListener("pointerleave", () => { if (!this.pointers.size) this._setHover(-1); });
    c.addEventListener("wheel", (e) => {
      e.preventDefault();
      this.flight = null;
      const k = Math.exp(Math.max(-0.5, Math.min(0.5, e.deltaY * (e.deltaMode === 1 ? 0.05 : 0.0015))));
      this.view.radius = Math.max(5, Math.min(1e5, this.view.radius * k));
      this._kick();
    }, { passive: false });
    c.addEventListener("dblclick", (e) => {
      const i = this._pick(e);
      if (i < 0) return;
      e.preventDefault();
      this.flyTo(i);
      const node = this.nodes[i];
      this.graph._setSelection([node], false);
      this.graph._draw();
      this.graph.opts.onSelect?.(node, true, e);
    });
    c.addEventListener("contextmenu", (e) => e.preventDefault());
  }

  _local(e) {
    const rect = this.canvas.getBoundingClientRect();
    return { x: e.clientX - rect.left, y: e.clientY - rect.top };
  }

  _pick(e) {
    if (!this.sim || !this.proj) return -1;
    const { x, y } = this._local(e);
    return pickNode(x, y, this.proj.x, this.proj.y, this.proj.z, this.proj.r, this.sim.n, e.pointerType === "touch" ? 10 : 3);
  }

  _setHover(i) {
    if (i === this.hover) return;
    this.hover = i;
    this.canvas.style.cursor = i >= 0 ? "pointer" : "";
    this._statesDirty = true;
    this._kick();
  }

  // Mouse: left click selects, left drag on a node drags it (as in 2D: it
  // follows the pointer in the plane facing the camera, the layout reheats,
  // and it is free again on release), left drag on empty space does
  // nothing; right drag orbits, Ctrl+right drag (or middle drag) pans, a
  // right click without drag opens the node menu, the wheel zooms,
  // double-click flies to a node.  Touch: one finger orbits, two zoom and
  // pan, a tap selects.
  _down(e) {
    this.graph.host.focus?.({ preventScroll: true });
    this.canvas.setPointerCapture?.(e.pointerId);
    const p = this._local(e);
    this.pointers.set(e.pointerId, { ...p, sx: p.x, sy: p.y, t: performance.now(), type: e.pointerType, button: e.button });
    this.flight = null;
    this.spin.theta = this.spin.phi = 0;
    this._closeSettings();
    if (this.pointers.size === 1) {
      let kind = "none", i = -1;
      if (e.pointerType !== "mouse") kind = "orbit";
      else if (e.button === 0) {
        i = this._pick(e);
        if (i >= 0 && !this.graph.readOnly) kind = "node";
      } else if (e.button === 2) kind = e.ctrlKey || e.metaKey ? "pan" : "orbit";
      else if (e.button === 1) kind = "pan";
      this.gesture = { kind, i, moved: false };
    } else {
      // Two fingers: pinch to zoom, move to pan.
      const [a, b] = [...this.pointers.values()];
      this.gesture = { kind: "pinch", dist: Math.hypot(a.x - b.x, a.y - b.y), mid: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, moved: true };
      this._endDrag();
    }
  }

  _move(e) {
    const ptr = this.pointers.get(e.pointerId);
    if (!ptr) {
      if (e.pointerType === "mouse") this._setHover(this._pick(e));
      return;
    }
    const p = this._local(e);
    const dx = p.x - ptr.x, dy = p.y - ptr.y;
    ptr.x = p.x; ptr.y = p.y;
    const g = this.gesture;
    if (!g) return;
    if (!g.moved && Math.hypot(p.x - ptr.sx, p.y - ptr.sy) > CLICK_SLOP) g.moved = true;
    if (!g.moved || g.kind === "none") return;
    if (g.kind === "node") { this._dragNode(g.i, p); return; }
    if (g.kind === "pinch") {
      const [a, b] = [...this.pointers.values()];
      if (!a || !b) return;
      const dist = Math.hypot(a.x - b.x, a.y - b.y), mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
      if (g.dist > 0 && dist > 0) this.view.radius = Math.max(5, Math.min(1e5, this.view.radius * g.dist / dist));
      this._pan(mid.x - g.mid.x, mid.y - g.mid.y);
      g.dist = dist; g.mid = mid;
    } else if (g.kind === "pan") this._pan(dx, dy);
    else {
      const k = 2 * Math.PI / Math.max(300, this.height || 600);
      this.view.theta -= dx * k; this._setPhi(this.view.phi - dy * k);
      this.spin.theta = -dx * k; this.spin.phi = -dy * k;
    }
    this._kick();
  }

  _pan(dx, dy) {
    const THREE = this.THREE;
    const per = 1 / this._pxPerUnit();
    const right = new THREE.Vector3().setFromMatrixColumn(this.camera.matrixWorld, 0);
    const up = new THREE.Vector3().setFromMatrixColumn(this.camera.matrixWorld, 1);
    this.view.target.addScaledVector(right, -dx * per).addScaledVector(up, dy * per);
  }

  // Node i follows the pointer in the plane facing the camera through its
  // depth when the drag started; the simulation holds it there while the
  // pointer is down (as the 2D drag does) and the rest of the layout reacts.
  _dragNode(i, p) {
    const THREE = this.THREE, sim = this.sim;
    if (!this.drag) this.drag = { i, ndcZ: this.proj.z[i] };
    const ndcZ = this.drag.ndcZ;
    if (!(ndcZ > -1 && ndcZ < 1)) return;
    const v = new THREE.Vector3((p.x / this.width) * 2 - 1, 1 - (p.y / this.height) * 2, ndcZ).unproject(this.camera);
    sim.x[i] = v.x; sim.y[i] = v.y; sim.z[i] = v.z;
    sim.vx[i] = sim.vy[i] = sim.vz[i] = 0;
    this._posDirty = true;
    this._reheat(0.3);
  }

  // Released: the node is free again and the layout settles around it.
  _endDrag() {
    if (!this.drag) return;
    this.drag = null;
    this._reheat(0.3);
  }

  _up(e, cancelled = false) {
    const ptr = this.pointers.get(e.pointerId);
    this.pointers.delete(e.pointerId);
    try { this.canvas.releasePointerCapture?.(e.pointerId); } catch { /* released */ }
    const g = this.gesture;
    if (this.pointers.size) {
      // One finger of a pinch lifted: continue as an orbit from here.
      const [rest] = [...this.pointers.values()];
      rest.sx = rest.x; rest.sy = rest.y;
      this.gesture = { kind: "orbit", i: -1, moved: true };
      this.spin.theta = this.spin.phi = 0;
      return;
    }
    this.gesture = null;
    if (g?.kind === "node" && this.drag) { this._endDrag(); return; }
    if (cancelled || !ptr || g?.moved) { this._kick(); return; }
    if (ptr.button === 2) { this._contextMenu(e); return; }
    if (ptr.button !== 0) return;
    const i = this._pick(e);
    const graph = this.graph;
    if (i >= 0) {
      const node = this.nodes[i];
      graph._setSelection([node], false);
      graph._draw();
      if (graph.mode === "read" || graph.opts.isInspectorOpen?.()) graph.opts.onSelect?.(node, false, e);
    } else {
      graph._setSelection([], false);
      graph._draw();
      if (graph.mode === "read") graph.opts.onSelect?.(null, false, e);
    }
  }

  // A right click (no drag) on a node opens the graph's node menu, the same
  // as in 2D.  On empty space nothing: the 2D background menu places new
  // nodes on the 2D canvas, which 3D does not show.
  _contextMenu(e) {
    const i = this._pick(e);
    if (i < 0) return;
    const graph = this.graph, node = this.nodes[i];
    if (!graph.selectedNodes.has(node.id)) graph._setSelection([node], false);
    graph._draw();
    graph._openGraphContextMenu(node, e);
  }

  // ---- settings -------------------------------------------------------------

  setSettings(patch) {
    const before = this.settings;
    this.settings = normalizeGraph3DSettings({ ...this.settings, ...patch });
    this.onSettings?.(this.settings);
    if (before.nodeSize !== this.settings.nodeSize) this._sizesDirty = true;
    if (before.lowPower !== this.settings.lowPower) this._resize(true);
    this._camDirty = true;
    this._syncHud();
    this._kick();
  }

  _syncHud() {
    const on = this.settings.autoRotate;
    this.rotateBtn.setAttribute("aria-pressed", String(on));
    this.rotateBtn.classList.toggle("on", on);
    this.rotateBtn.replaceChildren(icon(on ? "pause" : "play", "sm"));
  }

  _toggleSettings(button) {
    if (!this.settingsEl.classList.contains("hidden")) { this._closeSettings(); return; }
    const s = this.settings;
    const slider = (key, label, min, max, step) => {
      const value = h("span", { class: "graph3d-value" }, String(s[key]));
      const input = h("input", {
        type: "range", min, max, step, value: s[key], "aria-label": label,
        oninput: (e) => { value.textContent = e.target.value; this.setSettings({ [key]: Number(e.target.value) }); },
      });
      return h("label", { class: "graph3d-row" }, h("span", null, label), input, value);
    };
    this.settingsEl.replaceChildren(
      slider("labelDistance", t("graph3d.label_distance"), 0, 1, 0.05),
      slider("nodeSize", t("graph3d.node_size"), 0.5, 2, 0.1),
      h("label", { class: "graph3d-row graph3d-check", title: t("graph3d.low_power_hint") },
        h("input", { type: "checkbox", checked: s.lowPower || undefined, onchange: (e) => this.setSettings({ lowPower: e.target.checked }) }),
        h("span", null, t("graph3d.low_power"))),
      h("p", { class: "graph3d-note" }, t("graph3d.physics_note")));
    const tools = this.graph.tools;
    this.settingsEl.style.top = tools ? `${tools.offsetTop + tools.offsetHeight + 6}px` : "";
    this.settingsEl.classList.remove("hidden");
    this._settingsBtn = button;
    button.setAttribute("aria-expanded", "true");
  }

  _closeSettings() {
    this.settingsEl.classList.add("hidden");
    this._settingsBtn?.setAttribute("aria-expanded", "false");
  }

  _resize(force = false) {
    if (!this.renderer) return;
    const rect = this.layer.getBoundingClientRect();
    const w = Math.max(1, Math.floor(rect.width)), hgt = Math.max(1, Math.floor(rect.height));
    const coarse = Boolean(globalThis.matchMedia?.("(pointer: coarse)")?.matches);
    const ratio = pixelRatioCap(globalThis.devicePixelRatio, this.settings.lowPower, coarse);
    if (!force && w === this.width && hgt === this.height && ratio === this._ratio) return;
    this.width = w; this.height = hgt; this._ratio = ratio;
    this.renderer.setPixelRatio(ratio);
    this.renderer.setSize(w, hgt, false);
    this.canvas.style.width = `${w}px`;
    this.canvas.style.height = `${hgt}px`;
    this.camera.aspect = w / hgt;
    this.camera.updateProjectionMatrix();
    this._camKey = null;
    this._camDirty = true;
    this._kick();
  }

  /**
   * The per-instance data as uploaded (tests): instance count, node ids,
   * atlas cells and sizes, with the values the visible nodes call for, and
   * the atlas canvas size against the size of the texture on the GPU.
   */
  instanceSnapshot() {
    const n = this.sim?.n || 0;
    const geometry = this.nodeMesh?.geometry;
    const scale = this.settings.nodeSize;
    const gpu = this._atlasUploaded;
    return {
      count: geometry?.instanceCount ?? 0,
      ids: this.sim?.ids.slice() || [],
      cells: Array.from(this.aCell?.array.slice(0, n) || []),
      expectedCells: (this.nodes || []).map((node) => this._atlasKeys?.get(this._cellKey(node)) ?? -1),
      sizes: Array.from(this.aSize?.array.slice(0, n) || []),
      expectedSizes: Array.from({ length: n }, (_, i) => this.sim.r[i] * scale * QUAD),
      cellCount: this._atlasKeys?.size || 0,
      atlasSize: this.atlasCanvas?.width || 0,
      textureSize: gpu ?? this.atlasCanvas?.width ?? 0,
    };
  }

  /** Mean and 95th percentile of the recent frame times (ms of work). */
  frameStats() {
    const list = [...this.stats.frameMs].sort((a, b) => a - b);
    if (!list.length) return { frames: 0, mean: 0, p95: 0 };
    return { frames: list.length, mean: list.reduce((s, v) => s + v, 0) / list.length, p95: list[Math.floor(list.length * 0.95)] };
  }

  destroy() {
    this.destroyed = true;
    if (this.frame) cancelAnimationFrame(this.frame);
    this._resizeObserver?.disconnect();
    if (this.sim && this.graph) {
      const map = this.graph._pos3d || (this.graph._pos3d = new Map());
      for (let i = 0; i < this.sim.n; i++) map.set(this.sim.ids[i], { x: this.sim.x[i], y: this.sim.y[i], z: this.sim.z[i] });
    }
    for (const entry of this.labelCache.values()) entry.texture.dispose();
    for (const sprite of this.labels) sprite.material.dispose();
    this.nodeMesh?.geometry.dispose(); this.edgeMesh?.geometry.dispose();
    this.nodeMaterial?.dispose(); this.edgeMaterial?.dispose(); this.atlas?.dispose();
    this.renderer?.dispose();
    this.renderer?.forceContextLoss?.();
    this.layer.remove();
    this.hud.remove();
    this.settingsEl.remove();
  }
}
