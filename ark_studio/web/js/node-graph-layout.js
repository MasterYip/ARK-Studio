// Layered (Sugiyama-style) auto layout and smooth Bézier edge routing for
// node-editor style graphs (GraphView layout "node", the Resources graph).
//
// Pure functions without DOM access so they can run in tests, scripts and a
// Web Worker.  Coordinates are world units; nodes are centred rectangles
// ({ id, x, y, w, h }); edges run from the source's output port (right edge)
// to the target's input port (left edge).
//
// Layout pipeline (Gansner/Sugiyama):
//   1. split into connected components; isolated nodes are packed as a grid
//   2. break cycles (DFS back edges are reversed)
//   3. layer assignment: longest path, then tightened towards successors
//   4. very wide layers wrap into adjacent sub-layers
//   5. long edges get dummy nodes, one per spanned layer
//   6. crossing minimisation: barycenter sweeps + adjacent transposition,
//      keeping the best ordering seen
//   7. coordinate assignment: priority method (dummies first, so long edges
//      run straight), alternating down/up passes
//   8. components are shelf-packed into rows
//
// Routing: every edge leaves/enters its card horizontally through a port
// slot (several edges on one side get distinct, ordered slots).  A direct
// cubic S-curve is used when it clears all other cards; otherwise the edge
// takes a horizontal corridor above/below the obstacles (forward edges) or a
// loop around both cards (backward edges).  Corridors already used by other
// edges are offset so parallel routes do not overlap.

export const NODE_W = 200;
export const NODE_H = 64;
export const LAYER_GAP = 120;
export const ROW_GAP = 28;
export const COMPONENT_GAP = 110;
const DUMMY_H = 12;
const DEFAULT_MAX_LAYER = 80;

// ------------------------------------------------------------------ layout

/**
 * Compute centre positions for every node.
 * @param {Array<{id, w?, h?}>} nodes
 * @param {Array<{source, target}>} edges
 * @returns {{ positions: Map<id, {x, y}>, stats: object }}
 */
export function layeredLayout(nodes, edges, options = {}) {
  const started = now();
  const maxLayer = options.maxLayer || DEFAULT_MAX_LAYER;
  const sweeps = options.sweeps ?? (nodes.length > 1000 ? 6 : 12);
  const n = nodes.length;
  const index = new Map(nodes.map((node, i) => [node.id, i]));
  const width = nodes.map((node) => node.w || NODE_W);
  const height = nodes.map((node) => node.h || NODE_H);
  const pairs = new Map();
  for (const edge of edges) {
    const u = index.get(edge.source), v = index.get(edge.target);
    if (u === undefined || v === undefined || u === v) continue;
    const key = u < v ? `${u}:${v}` : `${v}:${u}`;
    if (!pairs.has(key)) pairs.set(key, [u, v]);
  }
  const parent = Array.from({ length: n }, (_, i) => i);
  const find = (i) => { while (parent[i] !== i) { parent[i] = parent[parent[i]]; i = parent[i]; } return i; };
  for (const [u, v] of pairs.values()) parent[find(u)] = find(v);
  const groups = new Map();
  for (let i = 0; i < n; i++) {
    const root = find(i);
    if (!groups.has(root)) groups.set(root, []);
    groups.get(root).push(i);
  }
  const edgeList = [...pairs.values()];
  const byComponent = new Map();
  for (const pair of edgeList) {
    const root = find(pair[0]);
    if (!byComponent.has(root)) byComponent.set(root, []);
    byComponent.get(root).push(pair);
  }
  const blocks = [];
  const isolated = [];
  const stats = { components: 0, layers: 0, dummies: 0, crossings: 0, reversed: 0 };
  for (const [root, members] of groups) {
    if (members.length === 1) { isolated.push(members[0]); continue; }
    const block = layoutComponent(members, byComponent.get(root) || [], width, height,
      { maxLayer, sweeps, layerGap: options.layerGap ?? LAYER_GAP });
    stats.components += 1;
    stats.layers = Math.max(stats.layers, block.layers);
    stats.dummies += block.dummies;
    stats.crossings += block.crossings;
    stats.reversed += block.reversed;
    blocks.push(block);
  }
  if (isolated.length) blocks.push(gridBlock(isolated, width, height));
  const waypoints = new Map();
  const positions = packBlocks(blocks, nodes, waypoints, options.aspect ?? 1.5);
  stats.ms = now() - started;
  return { positions, waypoints, stats };
}

function now() {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

function layoutComponent(members, pairs, width, height, { maxLayer, sweeps, layerGap }) {
  // Local indices.
  const local = new Map(members.map((g, i) => [g, i]));
  const m = members.length;
  const out = Array.from({ length: m }, () => []);
  const inn = Array.from({ length: m }, () => []);
  for (const [gu, gv] of pairs) {
    const u = local.get(gu), v = local.get(gv);
    out[u].push(v);
    inn[v].push(u);
  }
  // 2. Cycle breaking: iterative DFS, sources first; back edges reverse.
  const dag = Array.from({ length: m }, () => []);
  const state = new Uint8Array(m); // 0 new, 1 on stack, 2 done
  let reversed = 0;
  const order = [...Array(m).keys()].sort((a, b) => (inn[a].length - inn[b].length) || (out[b].length - out[a].length) || a - b);
  for (const start of order) {
    if (state[start]) continue;
    const stack = [[start, 0]];
    state[start] = 1;
    while (stack.length) {
      const top = stack[stack.length - 1];
      const [u, i] = top;
      if (i < out[u].length) {
        top[1] += 1;
        const v = out[u][i];
        if (state[v] === 1) { dag[v].push(u); reversed += 1; } // back edge: v -> u
        else {
          dag[u].push(v);
          if (!state[v]) { state[v] = 1; stack.push([v, 0]); }
        }
      } else {
        state[u] = 2;
        stack.pop();
      }
    }
  }
  const preds = Array.from({ length: m }, () => []);
  for (let u = 0; u < m; u++) for (const v of dag[u]) preds[v].push(u);
  // 3. Layering: longest path from sources, then pull nodes towards their
  // successors so edges get as short as possible.
  const indeg = preds.map((p) => p.length);
  const topo = [];
  const queue = [];
  for (let i = 0; i < m; i++) if (!indeg[i]) queue.push(i);
  while (queue.length) {
    const u = queue.shift();
    topo.push(u);
    for (const v of dag[u]) if (--indeg[v] === 0) queue.push(v);
  }
  const layer = new Int32Array(m);
  for (const u of topo) for (const v of dag[u]) layer[v] = Math.max(layer[v], layer[u] + 1);
  for (let k = topo.length - 1; k >= 0; k--) {
    const u = topo[k];
    if (!dag[u].length) continue;
    let low = Infinity;
    for (const v of dag[u]) low = Math.min(low, layer[v]);
    if (low - 1 > layer[u]) layer[u] = low - 1;
  }
  // 4. Wrap very wide layers into adjacent sub-layers (no edges run inside
  // a layer, so splitting keeps every edge pointing forward).
  let layerCount = 0;
  for (let i = 0; i < m; i++) layerCount = Math.max(layerCount, layer[i] + 1);
  const buckets = Array.from({ length: layerCount }, () => []);
  for (const u of topo) buckets[layer[u]].push(u);
  let shift = 0;
  for (let l = 0; l < layerCount; l++) {
    const bucket = buckets[l];
    const chunks = Math.ceil(bucket.length / maxLayer);
    // Successor-free nodes go to the outer sub-layers first.
    bucket.sort((a, b) => (dag[a].length ? 0 : 1) - (dag[b].length ? 0 : 1));
    bucket.forEach((u, i) => { layer[u] = l + shift + (chunks > 1 ? Math.floor(i / maxLayer) : 0); });
    shift += Math.max(0, chunks - 1);
  }
  layerCount += shift;
  // 5. Dummies for long edges.
  const isDummy = [];
  const vh = [];
  const vw = [];
  const vLayer = [];
  for (let i = 0; i < m; i++) { isDummy.push(false); vh.push(height[members[i]]); vw.push(width[members[i]]); vLayer.push(layer[i]); }
  const down = Array.from({ length: m }, () => []);
  const up = Array.from({ length: m }, () => []);
  const link = (a, b) => { down[a].push(b); up[b].push(a); };
  let dummies = 0;
  const chains = [];
  for (let u = 0; u < m; u++) {
    for (const v of dag[u]) {
      let prev = u;
      const chain = [];
      for (let l = layer[u] + 1; l < layer[v]; l++) {
        const d = isDummy.length;
        isDummy.push(true); vh.push(DUMMY_H); vw.push(0); vLayer.push(l);
        down.push([]); up.push([]);
        link(prev, d);
        prev = d;
        chain.push(d);
        dummies += 1;
      }
      link(prev, v);
      if (chain.length) chains.push({ u, v, chain });
    }
  }
  const total = isDummy.length;
  const layers = Array.from({ length: layerCount }, () => []);
  // Initial order: DFS from layer-0 nodes so related nodes start close.
  const seen = new Uint8Array(total);
  const visit = (start) => {
    const stack = [start];
    while (stack.length) {
      const u = stack.pop();
      if (seen[u]) continue;
      seen[u] = 1;
      layers[vLayer[u]].push(u);
      for (let k = down[u].length - 1; k >= 0; k--) if (!seen[down[u][k]]) stack.push(down[u][k]);
    }
  };
  for (const u of topo) if (vLayer[u] === 0 || !up[u].length) visit(u);
  for (let u = 0; u < total; u++) if (!seen[u]) visit(u);
  // 6. Crossing minimisation.
  const pos = new Float64Array(total);
  const syncPos = () => { for (const row of layers) row.forEach((u, i) => { pos[u] = i; }); };
  syncPos();
  let best = layers.map((row) => row.slice());
  let bestCrossings = totalCrossings(layers, down, pos);
  for (let iter = 0; iter < sweeps && bestCrossings > 0; iter++) {
    const downward = iter % 2 === 0;
    if (downward) for (let l = 1; l < layerCount; l++) sortByBarycenter(layers[l], up, pos);
    else for (let l = layerCount - 2; l >= 0; l--) sortByBarycenter(layers[l], down, pos);
    syncPos();
    transpose(layers, up, down, pos);
    const crossings = totalCrossings(layers, down, pos);
    if (crossings < bestCrossings) {
      bestCrossings = crossings;
      best = layers.map((row) => row.slice());
    }
  }
  for (let l = 0; l < layerCount; l++) layers[l] = best[l];
  syncPos();
  // 7. Coordinates: x by layer, y by the priority method.
  const y = new Float64Array(total);
  for (const row of layers) {
    let cursor = 0;
    row.forEach((u, i) => {
      if (i) cursor += gapBetween(row[i - 1], u, vh, isDummy);
      y[u] = cursor;
    });
    const mid = cursor / 2;
    for (const u of row) y[u] -= mid;
  }
  const passes = [[1, layerCount, 1, up], [layerCount - 2, -1, -1, down], [1, layerCount, 1, up], [layerCount - 2, -1, -1, down]];
  for (const [from, to, step, ref] of passes) {
    for (let l = from; l !== to; l += step) placeLayer(layers[l], ref, y, vh, isDummy);
  }
  // Column x: widest card in each layer decides the step.
  const columnWidth = layers.map((row) => Math.max(NODE_W * 0.5, ...row.map((u) => vw[u] || 0)));
  const columnX = [];
  let cx = 0;
  for (let l = 0; l < layerCount; l++) {
    columnX.push(cx + columnWidth[l] / 2);
    cx += columnWidth[l] + layerGap;
  }
  const items = [];
  let minY = Infinity, maxY = -Infinity;
  for (let u = 0; u < m; u++) {
    const x = columnX[vLayer[u]];
    items.push({ index: members[u], x, y: y[u] });
    minY = Math.min(minY, y[u] - vh[u] / 2);
    maxY = Math.max(maxY, y[u] + vh[u] / 2);
  }
  for (let u = m; u < total; u++) { minY = Math.min(minY, y[u]); maxY = Math.max(maxY, y[u]); }
  // Dummy positions become waypoints for the long edges they stand for.
  const waypoints = chains.map(({ u, v, chain }) => ({
    from: members[u], to: members[v],
    points: chain.map((d) => ({ x: columnX[vLayer[d]], y: y[d], half: columnWidth[vLayer[d]] / 2 })),
  }));
  return {
    items, waypoints, width: Math.max(1, cx - layerGap), height: Math.max(1, maxY - minY), top: minY, left: 0,
    layers: layerCount, dummies, crossings: bestCrossings, reversed,
  };
}

function gapBetween(a, b, vh, isDummy) {
  const pad = isDummy[a] || isDummy[b] ? ROW_GAP / 2 : ROW_GAP;
  return vh[a] / 2 + pad + vh[b] / 2;
}

function sortByBarycenter(row, ref, pos) {
  const value = new Map();
  for (const u of row) {
    const nbrs = ref[u];
    if (!nbrs.length) { value.set(u, pos[u]); continue; }
    let sum = 0;
    for (const v of nbrs) sum += pos[v];
    value.set(u, sum / nbrs.length);
  }
  row.sort((a, b) => value.get(a) - value.get(b) || pos[a] - pos[b]);
}

// Crossings between consecutive layers by counting inversions (Barth et al.).
function totalCrossings(layers, down, pos) {
  let total = 0;
  for (let l = 0; l + 1 < layers.length; l++) total += layerCrossings(layers[l], down, pos, layers[l + 1].length);
  return total;
}

function layerCrossings(row, down, pos, size) {
  const targets = [];
  for (const u of row) {
    const list = down[u].map((v) => pos[v]).sort((a, b) => a - b);
    for (const p of list) targets.push(p);
  }
  if (targets.length < 2) return 0;
  // Fenwick tree over target positions.
  const tree = new Int32Array(size + 1);
  let crossings = 0;
  for (let i = targets.length - 1; i >= 0; i--) {
    let k = targets[i];
    // count already inserted with position < targets[i]
    let s = 0;
    for (let j = k; j > 0; j -= j & -j) s += tree[j];
    crossings += s;
    for (let j = k + 1; j <= size; j += j & -j) tree[j] += 1;
  }
  return crossings;
}

function pairCrossings(a, b, ref, pos) {
  // Crossings among edges of a and b (a placed before b) towards one layer.
  let c = 0;
  for (const x of ref[a]) for (const y of ref[b]) if (pos[x] > pos[y]) c += 1;
  return c;
}

function transpose(layers, up, down, pos) {
  for (let pass = 0; pass < 4; pass++) {
    let improved = false;
    for (const row of layers) {
      if (row.length < 2 || row.length > 400) continue;
      for (let i = 0; i + 1 < row.length; i++) {
        const a = row[i], b = row[i + 1];
        const before = pairCrossings(a, b, up, pos) + pairCrossings(a, b, down, pos);
        const after = pairCrossings(b, a, up, pos) + pairCrossings(b, a, down, pos);
        if (after < before) {
          row[i] = b; row[i + 1] = a;
          pos[a] = i + 1; pos[b] = i;
          improved = true;
        }
      }
    }
    if (!improved) break;
  }
}

// Priority method: nodes move towards the mean of their neighbours in the
// reference layer; higher priority nodes (dummies, then high degree) win and
// push lower priority nodes aside, keeping order and minimum gaps.
function placeLayer(row, ref, y, vh, isDummy) {
  const k = row.length;
  if (!k) return;
  const desired = new Float64Array(k);
  const priority = new Float64Array(k);
  for (let i = 0; i < k; i++) {
    const u = row[i];
    const nbrs = ref[u];
    if (nbrs.length) {
      let sum = 0;
      for (const v of nbrs) sum += y[v];
      desired[i] = sum / nbrs.length;
    } else desired[i] = y[u];
    priority[i] = isDummy[u] ? 1e6 : nbrs.length;
  }
  const orderIdx = [...Array(k).keys()].sort((a, b) => priority[b] - priority[a] || a - b);
  const placed = new Uint8Array(k);
  const gap = (i) => gapBetween(row[i], row[i + 1], vh, isDummy); // between i and i+1
  for (const i of orderIdx) {
    let lo = -Infinity, hi = Infinity;
    let acc = 0;
    for (let j = i - 1; j >= 0; j--) {
      acc += gap(j);
      if (placed[j]) { lo = y[row[j]] + acc; break; }
    }
    acc = 0;
    for (let j = i + 1; j < k; j++) {
      acc += gap(j - 1);
      if (placed[j]) { hi = y[row[j]] - acc; break; }
    }
    let target = desired[i];
    if (target < lo) target = lo;
    if (target > hi) target = hi;
    y[row[i]] = target;
    placed[i] = 1;
    for (let j = i - 1; j >= 0 && !placed[j]; j--) {
      const limit = y[row[j + 1]] - gap(j);
      if (y[row[j]] > limit) y[row[j]] = limit; else break;
    }
    for (let j = i + 1; j < k && !placed[j]; j++) {
      const limit = y[row[j - 1]] + gap(j - 1);
      if (y[row[j]] < limit) y[row[j]] = limit; else break;
    }
  }
}

function gridBlock(indices, width, height) {
  const count = indices.length;
  const cols = Math.max(1, Math.round(Math.sqrt(count * 0.6)));
  const cellW = Math.max(...indices.map((i) => width[i])) + 40;
  const cellH = Math.max(...indices.map((i) => height[i])) + ROW_GAP;
  const items = indices.map((index, k) => ({
    index, x: (k % cols) * cellW + cellW / 2 - 20, y: Math.floor(k / cols) * cellH + cellH / 2,
  }));
  return { items, width: cols * cellW - 40, height: Math.ceil(count / cols) * cellH, top: 0, left: 0 };
}

function packBlocks(blocks, nodes, waypoints, aspect) {
  const positions = new Map();
  if (!blocks.length) return positions;
  blocks.sort((a, b) => b.items.length - a.items.length);
  const area = blocks.reduce((sum, b) => sum + (b.width + COMPONENT_GAP) * (b.height + COMPONENT_GAP), 0);
  const rowWidth = Math.max(blocks[0].width, Math.sqrt(area) * aspect);
  let x = 0, y = 0, rowHeight = 0;
  for (const block of blocks) {
    if (x > 0 && x + block.width > rowWidth) { x = 0; y += rowHeight + COMPONENT_GAP; rowHeight = 0; }
    for (const item of block.items) {
      positions.set(nodes[item.index].id, { x: x + item.x - block.left, y: y + item.y - block.top });
    }
    for (const way of block.waypoints || []) {
      waypoints.set(`${nodes[way.from].id}->${nodes[way.to].id}`,
        way.points.map((p) => ({ x: x + p.x - block.left, y: y + p.y - block.top, half: p.half })));
    }
    x += block.width + COMPONENT_GAP;
    rowHeight = Math.max(rowHeight, block.height);
  }
  return positions;
}

/**
 * Place nodes that have no position yet next to their placed neighbours,
 * without moving anything that is already placed.
 * @param {Array<{id, x?, y?, w?, h?}>} nodes  nodes with finite x/y are fixed
 */
export function placeNewNodes(nodes, edges) {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const fixed = nodes.filter((node) => Number.isFinite(node.x) && Number.isFinite(node.y));
  const fresh = nodes.filter((node) => !(Number.isFinite(node.x) && Number.isFinite(node.y)));
  const positions = new Map();
  if (!fresh.length) return positions;
  const occupied = fixed.map((node) => rectOf(node, 12));
  const overlaps = (r) => occupied.some((o) => r.x0 < o.x1 && r.x1 > o.x0 && r.y0 < o.y1 && r.y1 > o.y0);
  let maxX = fixed.length ? Math.max(...fixed.map((node) => node.x + (node.w || NODE_W) / 2)) : 0;
  let minY = fixed.length ? Math.min(...fixed.map((node) => node.y - (node.h || NODE_H) / 2)) : 0;
  let freeY = minY;
  for (const node of fresh) {
    const w = node.w || NODE_W, h = node.h || NODE_H;
    let anchor = null, side = 1;
    for (const edge of edges) {
      const other = edge.source === node.id ? byId.get(edge.target) : edge.target === node.id ? byId.get(edge.source) : null;
      if (!other || !(Number.isFinite(other.x) && Number.isFinite(other.y))) continue;
      anchor = other;
      side = edge.source === node.id ? -1 : 1; // sources sit left of their targets
      break;
    }
    let x, y;
    if (anchor) {
      x = anchor.x + side * ((anchor.w || NODE_W) / 2 + LAYER_GAP + w / 2);
      y = anchor.y;
      for (let step = 0; step < 200; step++) {
        const offset = (step % 2 ? 1 : -1) * Math.ceil(step / 2) * (h + ROW_GAP);
        const r = { x0: x - w / 2, x1: x + w / 2, y0: y + offset - h / 2, y1: y + offset + h / 2 };
        if (!overlaps(r)) { y += offset; break; }
      }
    } else {
      x = maxX + LAYER_GAP + w / 2;
      y = freeY + h / 2;
      freeY += h + ROW_GAP;
    }
    positions.set(node.id, { x, y });
    node.x = x; node.y = y;
    occupied.push(rectOf(node, 12));
  }
  for (const node of fresh) { delete node.x; delete node.y; }
  return positions;
}

function rectOf(node, margin = 0) {
  const w = (node.w || NODE_W) / 2 + margin, h = (node.h || NODE_H) / 2 + margin;
  return { x0: node.x - w, x1: node.x + w, y0: node.y - h, y1: node.y + h };
}

// ----------------------------------------------------------------- routing

const CELL = 256;

class ObstacleGrid {
  constructor(nodes, margin) {
    this.rects = nodes.map((node) => ({ id: node.id, ...rectOf(node, margin) }));
    this.cells = new Map();
    this.rects.forEach((r, i) => {
      for (let cx = Math.floor(r.x0 / CELL); cx <= Math.floor(r.x1 / CELL); cx++) {
        for (let cy = Math.floor(r.y0 / CELL); cy <= Math.floor(r.y1 / CELL); cy++) {
          const key = cx * 73856093 ^ cy * 19349663;
          let list = this.cells.get(key);
          if (!list) this.cells.set(key, list = []);
          list.push(i);
        }
      }
    });
  }

  hit(x, y, skipA, skipB) {
    const list = this.cells.get(Math.floor(x / CELL) * 73856093 ^ Math.floor(y / CELL) * 19349663);
    if (!list) return -1;
    for (const i of list) {
      const r = this.rects[i];
      if (r.id === skipA || r.id === skipB) continue;
      if (x > r.x0 && x < r.x1 && y > r.y0 && y < r.y1) return i;
    }
    return -1;
  }

  query(x0, y0, x1, y1) {
    const found = new Set();
    for (let cx = Math.floor(x0 / CELL); cx <= Math.floor(x1 / CELL); cx++) {
      for (let cy = Math.floor(y0 / CELL); cy <= Math.floor(y1 / CELL); cy++) {
        for (const i of this.cells.get(cx * 73856093 ^ cy * 19349663) || []) {
          const r = this.rects[i];
          if (r.x1 > x0 && r.x0 < x1 && r.y1 > y0 && r.y0 < y1) found.add(i);
        }
      }
    }
    return [...found].map((i) => this.rects[i]);
  }
}

/** Evaluate a cubic Bézier segment [x0,y0,c1x,c1y,c2x,c2y,x3,y3] at t. */
export function cubicAt(s, t) {
  const u = 1 - t;
  const a = u * u * u, b = 3 * u * u * t, c = 3 * u * t * t, d = t * t * t;
  return { x: a * s[0] + b * s[2] + c * s[4] + d * s[6], y: a * s[1] + b * s[3] + c * s[5] + d * s[7] };
}

const line = (x0, y0, x1, y1) => [x0, y0, x0 + (x1 - x0) / 3, y0 + (y1 - y0) / 3, x0 + 2 * (x1 - x0) / 3, y0 + 2 * (y1 - y0) / 3, x1, y1];

/** Sample a route's segments into a polyline (`steps` points per segment). */
export function sampleRoute(segments, steps = 12) {
  const points = [];
  segments.forEach((s, k) => {
    for (let i = k ? 1 : 0; i <= steps; i++) points.push(cubicAt(s, i / steps));
  });
  return points;
}

function blocked(segments, grid, skipA, skipB, steps = 10) {
  let hits = 0;
  for (const s of segments) {
    for (let i = 1; i < steps; i++) {
      const p = cubicAt(s, i / steps);
      if (grid.hit(p.x, p.y, skipA, skipB) >= 0) hits += 1;
    }
  }
  return hits;
}

// Tangent length (control-point offset) of a forward S-curve, per model.
// See docs/development/node-graph-links.md for the sources.
export const CURVE_MODELS = {
  current: (dx, dy) => Math.max(36, dx * 0.5, Math.min(120, dy * 0.25)),
  xyflow: (dx) => 0.5 * dx,                                       // getBezierPath, target ahead
  blender: (dx, dy) => 5 * 0.1 * dx * Math.min(1, (dy / Math.max(1e-6, dx)) * (4.5 - 0.25 * 5)), // noodle_curving 5
  rete: (dx, dy) => Math.max(dy / 2, dx) * 0.3,                   // classicConnectionPath, curvature 0.3
  litegraph: (dx, dy) => 0.25 * Math.hypot(dx, dy),               // renderLink SPLINE_LINK
};

export const DEFAULT_FORWARD = "xyflow";
export const DEFAULT_BACKWARD = "compact";

function directSegments(p1, p2, model = DEFAULT_FORWARD) {
  const dx = Math.abs(p2.x - p1.x), dy = Math.abs(p2.y - p1.y);
  const k = (CURVE_MODELS[model] || CURVE_MODELS.current)(dx, dy);
  return [[p1.x, p1.y, p1.x + k, p1.y, p2.x - k, p2.y, p2.x, p2.y]];
}

// Polyline with rounded corners (quarter-circle cubics), as cubic segments.
function roundedSegments(points, radius) {
  const segs = [];
  let from = points[0];
  for (let i = 1; i < points.length - 1; i++) {
    const a = points[i - 1], b = points[i], c = points[i + 1];
    const l1 = Math.hypot(b.x - a.x, b.y - a.y), l2 = Math.hypot(c.x - b.x, c.y - b.y);
    const r = Math.min(radius, l1 / 2, l2 / 2);
    if (r < 0.5) { segs.push(line(from.x, from.y, b.x, b.y)); from = b; continue; }
    const u1 = { x: (b.x - a.x) / l1, y: (b.y - a.y) / l1 }, u2 = { x: (c.x - b.x) / l2, y: (c.y - b.y) / l2 };
    const s1 = { x: b.x - u1.x * r, y: b.y - u1.y * r }, s2 = { x: b.x + u2.x * r, y: b.y + u2.y * r };
    if (Math.hypot(s1.x - from.x, s1.y - from.y) > 0.5) segs.push(line(from.x, from.y, s1.x, s1.y));
    const k = 0.5523 * r;
    segs.push([s1.x, s1.y, s1.x + u1.x * k, s1.y + u1.y * k, s2.x - u2.x * k, s2.y - u2.y * k, s2.x, s2.y]);
    from = s2;
  }
  const last = points[points.length - 1];
  segs.push(line(from.x, from.y, last.x, last.y));
  return segs;
}

// Compact backward link: a short stub out of the output, a lane at y, a
// short stub into the input; rounded corners (xyflow smooth-step shape).
function compactLoopSegments(p1, p2, y, stub = BACK_STUB, radius = BACK_RADIUS) {
  return roundedSegments([p1, { x: p1.x + stub, y: p1.y }, { x: p1.x + stub, y }, { x: p2.x - stub, y },
    { x: p2.x - stub, y: p2.y }, p2], radius);
}

export const BACK_STUB = 22;
export const BACK_RADIUS = 14;

function bendSegments(p1, p2, bend) {
  const k1 = Math.max(30, Math.abs(bend.x - p1.x) * 0.5);
  const k2 = Math.max(30, Math.abs(p2.x - bend.x) * 0.5);
  const dir = bend.x >= p1.x ? 1 : -1;
  const dir2 = p2.x >= bend.x ? 1 : -1;
  return [
    [p1.x, p1.y, p1.x + k1, p1.y, bend.x - dir * k1 * 0.6, bend.y, bend.x, bend.y],
    [bend.x, bend.y, bend.x + dir2 * k2 * 0.6, bend.y, p2.x - k2, p2.y, p2.x, p2.y],
  ];
}

// Through layout waypoints (one per spanned layer): a straight run through
// the slot the layout reserved in each column, curves only in the gaps.
function waypointSegments(p1, p2, points) {
  const all = [p1];
  for (const p of points) all.push({ x: p.x - (p.half || 0), y: p.y }, { x: p.x + (p.half || 0), y: p.y });
  all.push(p2);
  if (all.some((p, i) => i && p.x <= all[i - 1].x)) return null;
  const segments = [];
  for (let i = 1; i < all.length; i++) {
    const a = all[i - 1], b = all[i];
    if (Math.abs(a.y - b.y) < 0.5) { segments.push(line(a.x, a.y, b.x, b.y)); continue; }
    const k = (b.x - a.x) * 0.5;
    segments.push([a.x, a.y, a.x + k, a.y, b.x - k, b.y, b.x, b.y]);
  }
  return segments;
}

// Forward corridor: S-curve to y, straight run, S-curve into the port.
function corridorSegments(p1, p2, y, run) {
  const ax = p1.x + run, bx = p2.x - run;
  return [
    [p1.x, p1.y, p1.x + run * 0.55, p1.y, ax - run * 0.45, y, ax, y],
    line(ax, y, bx, y),
    [bx, y, bx + run * 0.45, y, p2.x - run * 0.55, p2.y, p2.x, p2.y],
  ];
}

// Backward edge: loop out of the source to the right, run left along y and
// loop back into the target's input from the left.
function loopSegments(p1, p2, y, reach) {
  const k1 = Math.max(reach, Math.abs(y - p1.y) * 0.55);
  const k2 = Math.max(reach, Math.abs(y - p2.y) * 0.55);
  return [
    [p1.x, p1.y, p1.x + k1, p1.y, p1.x + k1, y, p1.x, y],
    line(p1.x, y, p2.x, y),
    [p2.x, y, p2.x - k2, y, p2.x - k2, p2.y, p2.x, p2.y],
  ];
}

/**
 * Route every edge.  Returns Map<edgeId, route> with
 * `{ segments, start, end, mid, sourceSlot, targetSlot }` in world units.
 * `edge.bend` ({x, y}) forces the route through a manual handle.
 */
export function routeNodeEdges(nodes, edges, options = {}) {
  const margin = options.margin ?? 10;
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const grid = options.fast ? null : new ObstacleGrid(nodes, margin);
  const routes = new Map();
  const visible = edges.filter((edge) => byId.has(edge.source) && byId.has(edge.target) && edge.source !== edge.target);
  // Port slots: order each side's edges by the other end's y, then id.
  const outs = new Map(), ins = new Map();
  for (const edge of visible) {
    if (!outs.has(edge.source)) outs.set(edge.source, []);
    if (!ins.has(edge.target)) ins.set(edge.target, []);
    outs.get(edge.source).push(edge);
    ins.get(edge.target).push(edge);
  }
  const slotY = new Map();
  const assign = (map, end, other) => {
    for (const [id, list] of map) {
      const node = byId.get(id);
      const h = node.h || NODE_H;
      list.sort((a, b) => (byId.get(a[other]).y - byId.get(b[other]).y) || String(a.id).localeCompare(String(b.id)));
      const spacing = list.length > 1 ? Math.min(12, (h - 18) / (list.length - 1)) : 0;
      list.forEach((edge, i) => {
        slotY.set(`${end}:${edge.id}`, node.y + (i - (list.length - 1) / 2) * spacing);
      });
    }
  };
  assign(outs, "out", "target");
  assign(ins, "in", "source");
  const corridors = [];
  const free = (y, x0, x1) => {
    for (const c of corridors) if (c.x1 > x0 && c.x0 < x1 && Math.abs(c.y - y) < 5) return false;
    return true;
  };
  const nudge = (y, x0, x1, dir) => {
    let value = y;
    for (let i = 0; i < 12 && !free(value, x0, x1); i++) value += dir * 6;
    return value;
  };
  // Short edges first: they take the best corridors.
  const ordered = [...visible].sort((a, b) => {
    const la = Math.abs(byId.get(a.target).x - byId.get(a.source).x);
    const lb = Math.abs(byId.get(b.target).x - byId.get(b.source).x);
    return la - lb;
  });
  // Cards that share an x (a layered-layout column) can differ in width.  A
  // link leaves a narrower card straight to the column's edge before it
  // curves, so it never clips a wider neighbour above or below.
  const columnEdge = new Map();
  for (const node of nodes) {
    const key = Math.round(node.x);
    const half = (node.w || NODE_W) / 2;
    const edgeOf = columnEdge.get(key) || { left: Infinity, right: -Infinity };
    edgeOf.left = Math.min(edgeOf.left, node.x - half);
    edgeOf.right = Math.max(edgeOf.right, node.x + half);
    columnEdge.set(key, edgeOf);
  }
  for (const edge of ordered) {
    const s = byId.get(edge.source), t = byId.get(edge.target);
    const p1 = { x: s.x + (s.w || NODE_W) / 2, y: slotY.get(`out:${edge.id}`) };
    const p2 = { x: t.x - (t.w || NODE_W) / 2, y: slotY.get(`in:${edge.id}`) };
    const bent = edge.bend && Number.isFinite(edge.bend.x) && Number.isFinite(edge.bend.y);
    const q1 = bent ? p1 : { x: Math.max(p1.x, columnEdge.get(Math.round(s.x)).right), y: p1.y };
    const q2 = bent ? p2 : { x: Math.min(p2.x, columnEdge.get(Math.round(t.x)).left), y: p2.y };
    let segments, chosen = false;
    const way = options.waypoints?.get(`${edge.source}->${edge.target}`);
    if (bent) {
      segments = bendSegments(p1, p2, edge.bend);
    } else if (options.fast) {
      segments = q2.x - q1.x >= 48 ? directSegments(q1, q2, options.forward)
        : compactLoopSegments(q1, q2, Math.max(rectOf(s, 18).y1, rectOf(t, 18).y1));
    } else if (way?.length && (segments = waypointSegments(q1, q2, way)) && !blocked(segments, grid, s.id, t.id)) {
      // Long edge from the layered layout: follow its reserved channel.
    } else {
      segments = chooseRoute(q1, q2, s, t, grid, nudge, options);
      chosen = true;
    }
    const mid = edge.bend ? { ...edge.bend } : midpoint(segments);
    const [x0, x1] = [Math.min(q1.x, q2.x), Math.max(q1.x, q2.x)];
    if (chosen && segments.length === 3) corridors.push({ x0, x1, y: segments[1][1] });
    // Straight stubs from the ports to the column edges.
    if (q1.x - p1.x > 0.5) segments = [line(p1.x, p1.y, q1.x, q1.y), ...segments];
    if (p2.x - q2.x > 0.5) segments = [...segments, line(q2.x, q2.y, p2.x, p2.y)];
    routes.set(edge.id, { segments, start: p1, end: p2, mid });
  }
  return routes;
}

function chooseRoute(p1, p2, s, t, grid, nudge, options) {
  const dx = p2.x - p1.x;
  // Defaults chosen by scripts/bench/node_link_styles.mjs (see
  // docs/development/node-graph-links.md): xyflow tangents forward, compact
  // smooth-step loops backward.
  const forward = options.forward || DEFAULT_FORWARD;
  const backward = options.backward || DEFAULT_BACKWARD;
  if (dx >= 48) {
    const direct = directSegments(p1, p2, forward);
    if (!blocked(direct, grid, s.id, t.id)) return direct;
  } else if (backward === "xyflow") {
    // getBezierPath for a target behind the source: offset 0.25 * 25 * sqrt(gap).
    const k = 0.25 * 25 * Math.sqrt(Math.max(0, -dx));
    return [[p1.x, p1.y, p1.x + k, p1.y, p2.x - k, p2.y, p2.x, p2.y]];
  }
  const run = dx >= 48 ? Math.max(28, Math.min(110, dx * 0.25)) : 0;
  const reach = 46;
  const x0 = Math.min(p1.x, p2.x) - reach, x1 = Math.max(p1.x, p2.x) + reach;
  const yMin = Math.min(p1.y, p2.y), yMax = Math.max(p1.y, p2.y);
  const near = grid.query(x0, yMin - 900, x1, yMax + 900);
  const candidates = new Set([p1.y, p2.y, (p1.y + p2.y) / 2]);
  for (const r of near) { candidates.add(r.y0 - 8); candidates.add(r.y1 + 8); }
  if (dx < 48) {
    // Backward loops must clear both cards; the gap between them (if any)
    // is the shortest lane.
    const rs = rectOf(s, 0), rt = rectOf(t, 0);
    for (const node of [s, t]) {
      const r = rectOf(node, 18);
      candidates.add(r.y0); candidates.add(r.y1);
    }
    if (rs.y1 + 20 < rt.y0) candidates.add((rs.y1 + rt.y0) / 2);
    if (rt.y1 + 20 < rs.y0) candidates.add((rt.y1 + rs.y0) / 2);
  }
  const middle = (p1.y + p2.y) / 2;
  const sorted = [...candidates].sort((a, b) => (Math.abs(a - p1.y) + Math.abs(a - p2.y) + Math.abs(a - middle) * 0.01)
    - (Math.abs(b - p1.y) + Math.abs(b - p2.y) + Math.abs(b - middle) * 0.01));
  let best = null, bestHits = Infinity;
  for (const raw of sorted.slice(0, 16)) {
    const y = nudge(raw, x0, x1, raw <= middle ? -1 : 1);
    if (dx < 48 && insideSpan(y, s, t)) continue;
    const segments = dx >= 48 ? corridorSegments(p1, p2, y, run)
      : backward === "compact" ? compactLoopSegments(p1, p2, y) : loopSegments(p1, p2, y, reach);
    const hits = blocked(segments, grid, s.id, t.id) + (dx < 48 ? selfHits(segments, s, t) : 0);
    if (!hits) return segments;
    if (hits < bestHits) { best = segments; bestHits = hits; }
  }
  return best || directSegments(p1, p2, forward);
}

function insideSpan(y, s, t) {
  for (const node of [s, t]) {
    const r = rectOf(node, 10);
    if (y > r.y0 && y < r.y1) return true;
  }
  return false;
}

// A loop may not cut through its own source/target cards.
function selfHits(segments, s, t) {
  let hits = 0;
  const rects = [rectOf(s, 4), rectOf(t, 4)];
  for (const seg of segments.slice(1, 2)) {
    for (let i = 1; i < 10; i++) {
      const p = cubicAt(seg, i / 10);
      for (const r of rects) if (p.x > r.x0 && p.x < r.x1 && p.y > r.y0 && p.y < r.y1) hits += 1;
    }
  }
  return hits;
}

function midpoint(segments) {
  if (segments.length === 3) {
    const s = segments[1];
    return { x: (s[0] + s[6]) / 2, y: (s[1] + s[7]) / 2 };
  }
  return cubicAt(segments[Math.floor(segments.length / 2)], segments.length === 1 ? 0.5 : 0);
}

// ----------------------------------------------------------------- metrics

/**
 * Quality metrics for a routed layout: edge-edge crossings (intersection
 * points away from shared ports), edge-node overlaps (an edge passing through
 * a card that is not its endpoint) and total edge length (world units).
 * `routes` maps edge id -> { points } or { segments }.
 */
export function layoutMetrics(nodes, edges, routes) {
  const polylines = [];
  for (const edge of edges) {
    const route = routes.get(edge.id);
    if (!route) continue;
    const points = route.points || sampleRoute(route.segments, 12);
    polylines.push({ edge, points });
  }
  let length = 0;
  for (const { points } of polylines) for (let i = 1; i < points.length; i++) length += Math.hypot(points[i].x - points[i - 1].x, points[i].y - points[i - 1].y);
  // Overlaps.
  const grid = new ObstacleGrid(nodes, -2);
  let overlaps = 0;
  for (const { edge, points } of polylines) {
    const hit = new Set();
    for (let i = 0; i + 1 < points.length; i++) {
      const a = points[i], b = points[i + 1];
      const steps = Math.max(1, Math.ceil(Math.hypot(b.x - a.x, b.y - a.y) / 12));
      for (let k = 0; k <= steps; k++) {
        const x = a.x + (b.x - a.x) * k / steps, y = a.y + (b.y - a.y) * k / steps;
        const idx = grid.hit(x, y, edge.source, edge.target);
        if (idx >= 0) hit.add(idx);
      }
    }
    overlaps += hit.size;
  }
  // Crossings via a segment grid.
  const cells = new Map();
  const segs = [];
  polylines.forEach(({ points }, e) => {
    for (let i = 0; i + 1 < points.length; i++) {
      const a = points[i], b = points[i + 1];
      const id = segs.length;
      segs.push({ e, a, b });
      const cx0 = Math.floor(Math.min(a.x, b.x) / 64), cx1 = Math.floor(Math.max(a.x, b.x) / 64);
      const cy0 = Math.floor(Math.min(a.y, b.y) / 64), cy1 = Math.floor(Math.max(a.y, b.y) / 64);
      for (let cx = cx0; cx <= cx1; cx++) for (let cy = cy0; cy <= cy1; cy++) {
        const key = `${cx},${cy}`;
        let list = cells.get(key);
        if (!list) cells.set(key, list = []);
        list.push(id);
      }
    }
  });
  const found = new Set();
  for (const list of cells.values()) {
    for (let i = 0; i < list.length; i++) for (let j = i + 1; j < list.length; j++) {
      const s1 = segs[list[i]], s2 = segs[list[j]];
      if (s1.e === s2.e) continue;
      const key = list[i] < list[j] ? `${list[i]}:${list[j]}` : `${list[j]}:${list[i]}`;
      if (found.has(key)) continue;
      const p = intersect(s1.a, s1.b, s2.a, s2.b);
      if (!p) continue;
      // Ignore touching at shared ports.
      const e1 = polylines[s1.e], e2 = polylines[s2.e];
      if (nearEnd(p, e1.points) || nearEnd(p, e2.points)) continue;
      found.add(key);
    }
  }
  // Shared length: stretches where an edge runs on top of another edge
  // (collinear elbows hide crossings, so they are reported separately).
  let shared = 0;
  const step = 6;
  polylines.forEach(({ points }, e) => {
    for (let i = 0; i + 1 < points.length; i++) {
      const a = points[i], b = points[i + 1];
      const len = Math.hypot(b.x - a.x, b.y - a.y);
      const n = Math.max(1, Math.round(len / step));
      for (let k = 0; k < n; k++) {
        const p = { x: a.x + (b.x - a.x) * (k + 0.5) / n, y: a.y + (b.y - a.y) * (k + 0.5) / n };
        if (nearEnd(p, points)) continue;
        const list = cells.get(`${Math.floor(p.x / 64)},${Math.floor(p.y / 64)}`) || [];
        if (list.some((id) => segs[id].e !== e && distanceToSegment(p, segs[id].a, segs[id].b) < 2)) shared += len / n;
      }
    }
  });
  return { crossings: found.size, overlaps, shared: Math.round(shared), length: Math.round(length) };
}

function distanceToSegment(p, a, b) {
  const dx = b.x - a.x, dy = b.y - a.y, len = dx * dx + dy * dy;
  const t = len ? Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len)) : 0;
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

function nearEnd(p, points) {
  const a = points[0], b = points[points.length - 1];
  return Math.hypot(p.x - a.x, p.y - a.y) < 14 || Math.hypot(p.x - b.x, p.y - b.y) < 14;
}

function intersect(a, b, c, d) {
  const r = { x: b.x - a.x, y: b.y - a.y }, s = { x: d.x - c.x, y: d.y - c.y };
  const den = r.x * s.y - r.y * s.x;
  if (Math.abs(den) < 1e-9) return null;
  const t = ((c.x - a.x) * s.y - (c.y - a.y) * s.x) / den;
  const u = ((c.x - a.x) * r.y - (c.y - a.y) * r.x) / den;
  if (t <= 0 || t >= 1 || u <= 0 || u >= 1) return null;
  return { x: a.x + t * r.x, y: a.y + t * r.y };
}
