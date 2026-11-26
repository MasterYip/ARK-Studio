// 3D force layout for the graph's 3D view (graph3d.js).  The same forces as
// the 2D Force map (graph-core.js stepForce), in three dimensions:
//   many-body   inverse-square repulsion, 64 px² floor; exact pairs up to
//               EXACT_THRESHOLD nodes, an octree (Barnes–Hut) above
//   gravity     linear pull to the origin
//   links       linear spring, coefficient 0.02, rest length `distance`
//   cluster     the optional "cluster by type/status/tag" terms
//   collision   overlapping nodes are pushed apart (3D only: the 2D map
//               draws flat discs, a 3D view would hide nodes behind others)
//   damping     velocities damped and clamped to ±30 per axis per tick
// State lives in typed arrays (struct of arrays) so a 2,000 node tick stays
// a few milliseconds.  Everything is deterministic: start positions come from
// a seeded hash of each node id, and ties are broken by index.
import { CLUSTER_TUNING, clusterKey } from "./graph-core.js";

export const EXACT_THRESHOLD = 400;
export const THETA = 0.9; // as d3-force-3d; 0.6 costs ~2.5x at 2,000 nodes
const MAX_SPEED = 30;
const GOLDEN = 2.399963;

/** A small seeded PRNG (mulberry32): `rand()` in [0, 1). */
export function seededRandom(seed = 1) {
  let s = (Number(seed) >>> 0) || 1;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** FNV-1a hash of a string, mixed with `seed`. */
export function hashSeed(text, seed = 1) {
  let h = (0x811c9dc5 ^ (Number(seed) >>> 0)) >>> 0;
  const s = String(text);
  for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 0x01000193) >>> 0; }
  return h >>> 0;
}

/**
 * The simulation state for `nodes` (objects with `id`, optional `degree`,
 * `type`, `status`, `tags`).  Start positions: `previous` (Map id ->
 * {x,y,z}) when it has the node, else a point in a ball whose radius grows
 * with √n, from a hash of (seed, id): the same graph always starts the same
 * way whatever the node order.
 */
export function createSim3D(nodes, { seed = 7, previous = null, spread = null } = {}) {
  const n = nodes.length;
  const sim = {
    n, ids: nodes.map((node) => node.id), index: new Map(),
    x: new Float64Array(n), y: new Float64Array(n), z: new Float64Array(n),
    vx: new Float64Array(n), vy: new Float64Array(n), vz: new Float64Array(n),
    r: new Float64Array(n), fixed: new Uint8Array(n), seed,
    links: new Int32Array(0), linkRel: [], alpha: 1, clusterRamp: 0, ticks: 0,
  };
  const radius = spread || Math.max(120, Math.cbrt(n) * 70);
  for (let i = 0; i < n; i++) {
    const node = nodes[i];
    sim.index.set(node.id, i);
    sim.r[i] = nodeRadius3D(node);
    const old = previous?.get(node.id);
    if (old && Number.isFinite(old.x) && Number.isFinite(old.y) && Number.isFinite(old.z)) {
      sim.x[i] = old.x; sim.y[i] = old.y; sim.z[i] = old.z;
      if (old.fixed) sim.fixed[i] = 1;
      continue;
    }
    const rand = seededRandom(hashSeed(node.id, seed));
    // Uniform in the ball: direction from two uniforms, radius ∝ cbrt(u).
    const u = rand() * 2 - 1, phi = rand() * Math.PI * 2, rr = radius * Math.cbrt(rand());
    const s = Math.sqrt(1 - u * u);
    sim.x[i] = rr * s * Math.cos(phi); sim.y[i] = rr * u; sim.z[i] = rr * s * Math.sin(phi);
  }
  return sim;
}

/** World radius of a node (the 2D Force map's size rule, uncapped by zoom). */
export function nodeRadius3D(node, scale = 1) {
  return ((node.radius || 7 + Math.sqrt(node.degree || 0) * 2.1)) * scale;
}

/** Link endpoints as index pairs (only relations in `visible`, if given). */
export function setSimLinks(sim, edges, visible = null) {
  const out = [];
  const rel = [];
  for (const edge of edges) {
    if (visible && !visible.has(edge.relation)) continue;
    const a = sim.index.get(edge.source), b = sim.index.get(edge.target);
    if (a === undefined || b === undefined || a === b) continue;
    out.push(a, b); rel.push(edge.relation);
  }
  sim.links = Int32Array.from(out);
  sim.linkRel = rel;
  return sim;
}

// A fixed direction for two coincident bodies i < j (deterministic).
function tieDirection(i, j) {
  const a = (Math.min(i, j) * 31 + Math.max(i, j)) * GOLDEN;
  const zz = Math.cos(a * 0.618034);
  const s = Math.sqrt(Math.max(0, 1 - zz * zz));
  const sign = i < j ? 1 : -1;
  return [Math.cos(a) * s * sign, Math.sin(a) * s * sign, zz * sign];
}

/** Exact O(n²) repulsion into the acceleration arrays (ax, ay, az). */
export function exactRepulsion3D(sim, strength, ax, ay, az) {
  const { n, x, y, z } = sim;
  for (let i = 0; i < n; i++) for (let j = i + 1; j < n; j++) {
    let dx = x[i] - x[j], dy = y[i] - y[j], dz = z[i] - z[j];
    let d2 = dx * dx + dy * dy + dz * dz;
    if (d2 < 1e-6) { [dx, dy, dz] = tieDirection(i, j); d2 = 1; }
    const f = strength / (Math.max(64, d2) * Math.sqrt(d2));
    ax[i] += dx * f; ay[i] += dy * f; az[i] += dz * f;
    ax[j] -= dx * f; ay[j] -= dy * f; az[j] -= dz * f;
  }
}

/**
 * The octree of the current positions: flat arrays per cell (centre of
 * mass, mass, half size, centre, first child, body range in `order`).
 * Leaves hold up to LEAF_SIZE bodies (more at the depth limit, where the
 * bodies coincide).  The arrays are kept on `sim` and reused every tick.
 */
const LEAF_SIZE = 4;
export function buildOctree(sim, maxDepth = 24, leafSize = LEAF_SIZE) {
  const { n, x, y, z } = sim;
  let minX = Infinity, minY = Infinity, minZ = Infinity, maxX = -Infinity, maxY = -Infinity, maxZ = -Infinity;
  for (let i = 0; i < n; i++) {
    const xi = x[i], yi = y[i], zi = z[i];
    if (xi < minX) minX = xi; if (xi > maxX) maxX = xi;
    if (yi < minY) minY = yi; if (yi > maxY) maxY = yi;
    if (zi < minZ) minZ = zi; if (zi > maxZ) maxZ = zi;
  }
  const half = Math.max(maxX - minX, maxY - minY, maxZ - minZ, 1) / 2 + 1e-3;
  let tree = sim._tree;
  const cap = Math.max(64, n * 3);
  if (!tree || tree.mass.length < cap || tree.order.length !== n) {
    tree = sim._tree = {
      order: new Int32Array(n), scratch: new Int32Array(n), octant: new Uint8Array(n), count: 0,
      mx: new Float64Array(cap), my: new Float64Array(cap), mz: new Float64Array(cap), mass: new Float64Array(cap),
      cx: new Float64Array(cap), cy: new Float64Array(cap), cz: new Float64Array(cap), half: new Float64Array(cap),
      first: new Int32Array(cap), kids: new Int32Array(cap), start: new Int32Array(cap), len: new Int32Array(cap),
      // Children of a cell are stored contiguously: first .. first + kids.
    };
  }
  tree.count = 0;
  const order = tree.order, scratch = tree.scratch, octant = tree.octant;
  for (let i = 0; i < n; i++) order[i] = i;
  const grow = () => {
    const size = tree.mass.length * 2;
    for (const key of ["mx", "my", "mz", "mass", "cx", "cy", "cz", "half"]) {
      const next = new Float64Array(size); next.set(tree[key]); tree[key] = next;
    }
    for (const key of ["first", "kids", "start", "len"]) { const next = new Int32Array(size); next.set(tree[key]); tree[key] = next; }
  };
  // Breadth-first: a cell's children are allocated together.
  const alloc = (start, len, cx, cy, cz, h) => {
    if (tree.count >= tree.mass.length) grow();
    const c = tree.count++;
    tree.cx[c] = cx; tree.cy[c] = cy; tree.cz[c] = cz; tree.half[c] = h;
    tree.start[c] = start; tree.len[c] = len; tree.kids[c] = 0; tree.first[c] = -1;
    return c;
  };
  if (!n) return tree;
  alloc(0, n, (minX + maxX) / 2, (minY + maxY) / 2, (minZ + maxZ) / 2, half);
  const depth = [0];
  for (let c = 0; c < tree.count; c++) {
    const start = tree.start[c], len = tree.len[c], end = start + len;
    let m = 0, sx = 0, sy = 0, sz = 0;
    for (let k = start; k < end; k++) { const i = order[k]; m++; sx += x[i]; sy += y[i]; sz += z[i]; }
    tree.mass[c] = m; tree.mx[c] = sx / m; tree.my[c] = sy / m; tree.mz[c] = sz / m;
    if (len <= leafSize || depth[c] >= maxDepth) continue;
    const cx = tree.cx[c], cy = tree.cy[c], cz = tree.cz[c];
    const counts = [0, 0, 0, 0, 0, 0, 0, 0];
    for (let k = start; k < end; k++) {
      const i = order[k];
      const o = (x[i] >= cx ? 1 : 0) | (y[i] >= cy ? 2 : 0) | (z[i] >= cz ? 4 : 0);
      octant[i] = o; counts[o]++;
    }
    const fill = [0, 0, 0, 0, 0, 0, 0, 0];
    for (let o = 1; o < 8; o++) fill[o] = fill[o - 1] + counts[o - 1];
    const offsets = fill.slice();
    // Stable partition: bodies keep their index order inside an octant.
    for (let k = start; k < end; k++) { const i = order[k]; scratch[start + fill[octant[i]]++] = i; }
    for (let k = start; k < end; k++) order[k] = scratch[k];
    const q = tree.half[c] / 2;
    let kids = 0;
    for (let o = 0; o < 8; o++) {
      if (!counts[o]) continue;
      const child = alloc(start + offsets[o], counts[o], cx + (o & 1 ? q : -q), cy + (o & 2 ? q : -q), cz + (o & 4 ? q : -q), q);
      depth[child] = depth[c] + 1;
      if (!kids) tree.first[c] = child;
      kids++;
    }
    tree.kids[c] = kids;
  }
  return tree;
}

/**
 * Barnes–Hut repulsion: a cell whose size is below `theta` times its
 * distance (and that does not contain the body) acts as one body at its
 * centre of mass.  Leaves are exact, and overlapping bodies there get the
 * collision push (`collide` 0..1).  The mean is removed so the
 * approximation adds no net drift.
 */
export function octreeRepulsion3D(sim, strength, ax, ay, az, { theta = THETA, collide = 0 } = {}) {
  const { n, x, y, z, r } = sim;
  if (n < 2) return;
  const tree = buildOctree(sim);
  const { order, mx, my, mz, mass, cx, cy, cz, half, first, kids, start, len } = tree;
  const t2 = theta * theta;
  const stack = sim._stack || (sim._stack = new Int32Array(1024));
  let sumX = 0, sumY = 0, sumZ = 0;
  for (let i = 0; i < n; i++) {
    const xi = x[i], yi = y[i], zi = z[i], ri = r[i];
    let fx = 0, fy = 0, fz = 0, top = 0;
    stack[top++] = 0;
    while (top) {
      const c = stack[--top];
      if (!kids[c]) {
        for (let k = start[c], end = k + len[c]; k < end; k++) {
          const j = order[k];
          if (j === i) continue;
          let dx = xi - x[j], dy = yi - y[j], dz = zi - z[j];
          let d2 = dx * dx + dy * dy + dz * dz;
          if (d2 < 1e-6) { [dx, dy, dz] = tieDirection(i, j); d2 = 1; }
          const d = Math.sqrt(d2);
          const f = strength / ((d2 > 64 ? d2 : 64) * d);
          fx += dx * f; fy += dy * f; fz += dz * f;
          if (collide) {
            const overlap = ri + r[j] - d;
            if (overlap > 0) { const p = collide * overlap * 0.5 / d; fx += dx * p; fy += dy * p; fz += dz * p; }
          }
        }
        continue;
      }
      const dx = xi - mx[c], dy = yi - my[c], dz = zi - mz[c];
      const d2 = dx * dx + dy * dy + dz * dz, h = half[c], w = 2 * h;
      if (w * w < t2 * d2) {
        const ox = xi - cx[c], oy = yi - cy[c], oz = zi - cz[c];
        if (ox > h || ox < -h || oy > h || oy < -h || oz > h || oz < -h) {
          const f = strength * mass[c] / ((d2 > 64 ? d2 : 64) * Math.sqrt(d2));
          fx += dx * f; fy += dy * f; fz += dz * f;
          continue;
        }
      }
      for (let ch = first[c] + kids[c] - 1; ch >= first[c]; ch--) stack[top++] = ch;
    }
    ax[i] += fx; ay[i] += fy; az[i] += fz;
    sumX += fx; sumY += fy; sumZ += fz;
  }
  const meanX = sumX / n, meanY = sumY / n, meanZ = sumZ / n;
  for (let i = 0; i < n; i++) { ax[i] -= meanX; ay[i] -= meanY; az[i] -= meanZ; }
}

// Collision only (for the exact path, where repulsion has no leaf pass).
function exactCollision(sim, collide, ax, ay, az) {
  const { n, x, y, z, r } = sim;
  for (let i = 0; i < n; i++) for (let j = i + 1; j < n; j++) {
    const dx = x[i] - x[j], dy = y[i] - y[j], dz = z[i] - z[j];
    const d = Math.sqrt(dx * dx + dy * dy + dz * dz) || 1;
    const overlap = r[i] + r[j] - d;
    if (overlap <= 0) continue;
    const p = collide * overlap * 0.5 / d;
    ax[i] += dx * p; ay[i] += dy * p; az[i] += dz * p;
    ax[j] -= dx * p; ay[j] -= dy * p; az[j] -= dz * p;
  }
}

const unit01 = (value, fallback) => {
  const v = Number(value);
  return Number.isFinite(v) ? Math.max(0, Math.min(1, v)) : fallback;
};

// Solve the 3×3 system m · w = t (Cramer); zero when singular.
function solve3(m, t) {
  const [a, b, c, d, e, f, g, h, k] = m;
  const det = a * (e * k - f * h) - b * (d * k - f * g) + c * (d * h - e * g);
  if (Math.abs(det) < 1e-12) return [0, 0, 0];
  return [
    (t[0] * (e * k - f * h) - b * (t[1] * k - f * t[2]) + c * (t[1] * h - e * t[2])) / det,
    (a * (t[1] * k - f * t[2]) - t[0] * (d * k - f * g) + c * (d * t[2] - t[1] * g)) / det,
    (a * (e * t[2] - t[1] * h) - b * (d * t[2] - t[1] * g) + t[0] * (d * h - e * g)) / det,
  ];
}

/**
 * The cluster terms of graph-core.js applyClusterForces in 3D: members pull
 * to their group's centroid, groups push each other apart as units (capped),
 * then the net force and the net torque are removed.  Returns the group of
 * every node (Int32Array, −1: none) or null.
 */
export function applyCluster3D(sim, nodes, cfg, ramp, ax, ay, az, tuning = CLUSTER_TUNING) {
  const { n, x, y, z } = sim;
  if (n < 2 || !(ramp > 0)) return null;
  const by = cfg.cluster_by || "type";
  const gid = new Int32Array(n);
  const keys = new Map();
  const sx = [], sy = [], sz = [], count = [];
  for (let i = 0; i < n; i++) {
    const key = clusterKey(nodes[i], by);
    if (key === null) { gid[i] = -1; continue; }
    let g = keys.get(key);
    if (g === undefined) { g = count.length; keys.set(key, g); sx.push(0); sy.push(0); sz.push(0); count.push(0); }
    gid[i] = g; sx[g] += x[i]; sy[g] += y[i]; sz[g] += z[i]; count[g]++;
  }
  const k = count.length;
  if (!k) return gid;
  const cx = sx.map((v, g) => v / count[g]), cy = sy.map((v, g) => v / count[g]), cz = sz.map((v, g) => v / count[g]);
  const a = unit01(cfg.cluster_attract, 0.5) * tuning.attract * ramp;
  const b = k > 1 ? unit01(cfg.cluster_repel, 0.5) * tuning.repel * (Number(cfg.repulsion) || 0) * ramp : 0;
  const soft2 = count.map((c) => (tuning.soft + tuning.softPerNode * Math.sqrt(c)) ** 2);
  const cap = tuning.cap * ramp;
  const gx = new Float64Array(k), gy = new Float64Array(k), gz = new Float64Array(k);
  if (b) for (let g = 0; g < k; g++) for (let h = g + 1; h < k; h++) {
    let dx = cx[g] - cx[h], dy = cy[g] - cy[h], dz = cz[g] - cz[h];
    let d2 = dx * dx + dy * dy + dz * dz;
    if (d2 < 1e-6) { [dx, dy, dz] = tieDirection(g, h); d2 = 1; }
    const f = b / ((d2 + soft2[g] + soft2[h]) * Math.sqrt(d2));
    gx[g] += dx * f * count[h]; gy[g] += dy * f * count[h]; gz[g] += dz * f * count[h];
    gx[h] -= dx * f * count[g]; gy[h] -= dy * f * count[g]; gz[h] -= dz * f * count[g];
  }
  for (let g = 0; g < k; g++) {
    const m = Math.hypot(gx[g], gy[g], gz[g]);
    if (m > cap) { const c = cap / m; gx[g] *= c; gy[g] *= c; gz[g] *= c; }
  }
  const fx = new Float64Array(n), fy = new Float64Array(n), fz = new Float64Array(n);
  let mX = 0, mY = 0, mZ = 0, ox = 0, oy = 0, oz = 0, grouped = 0;
  for (let i = 0; i < n; i++) {
    const g = gid[i];
    if (g < 0) continue;
    const w = Math.min(1, (count[g] - 1) / 3);
    fx[i] = a * w * (cx[g] - x[i]) + gx[g]; fy[i] = a * w * (cy[g] - y[i]) + gy[g]; fz[i] = a * w * (cz[g] - z[i]) + gz[g];
    mX += fx[i]; mY += fy[i]; mZ += fz[i]; ox += x[i]; oy += y[i]; oz += z[i]; grouped++;
  }
  if (!grouped) return gid;
  mX /= grouped; mY /= grouped; mZ /= grouped; ox /= grouped; oy /= grouped; oz /= grouped;
  // Net torque τ = Σ r × f and the inertia tensor I = Σ (|r|² 1 − r rᵀ);
  // subtracting ω × r with I ω = τ leaves no spin.
  const tq = [0, 0, 0];
  const S = [0, 0, 0, 0, 0, 0]; // xx yy zz xy xz yz
  for (let i = 0; i < n; i++) {
    if (gid[i] < 0) continue;
    fx[i] -= mX; fy[i] -= mY; fz[i] -= mZ;
    const rx = x[i] - ox, ry = y[i] - oy, rz = z[i] - oz;
    tq[0] += ry * fz[i] - rz * fy[i]; tq[1] += rz * fx[i] - rx * fz[i]; tq[2] += rx * fy[i] - ry * fx[i];
    S[0] += rx * rx; S[1] += ry * ry; S[2] += rz * rz; S[3] += rx * ry; S[4] += rx * rz; S[5] += ry * rz;
  }
  const tr = S[0] + S[1] + S[2];
  const [wx, wy, wz] = solve3([tr - S[0], -S[3], -S[4], -S[3], tr - S[1], -S[5], -S[4], -S[5], tr - S[2]], tq);
  for (let i = 0; i < n; i++) {
    if (gid[i] < 0) continue;
    const rx = x[i] - ox, ry = y[i] - oy, rz = z[i] - oz;
    ax[i] += fx[i] - (wy * rz - wz * ry); ay[i] += fy[i] - (wz * rx - wx * rz); az[i] += fz[i] - (wx * ry - wy * rx);
  }
  return k > 1 ? gid : null;
}

/**
 * One tick.  `cfg`: the graph physics (repulsion, distance, gravity, damping,
 * cluster…), plus `collide` (0..1, default 0.6) and `theta`.  `nodes` is only
 * read for the cluster grouping.  `dragged`: index held by the pointer.
 * Returns the motion { mean, max } of this tick.
 */
export function stepForce3D(sim, nodes, cfg, { dragged = -1, exactThreshold = EXACT_THRESHOLD } = {}) {
  const { n, x, y, z, vx, vy, vz, fixed, links } = sim;
  if (!n) return { mean: 0, max: 0 };
  const ax = new Float64Array(n), ay = new Float64Array(n), az = new Float64Array(n);
  const collide = cfg.collide ?? 0.6;
  if (n <= exactThreshold) {
    exactRepulsion3D(sim, cfg.repulsion, ax, ay, az);
    if (collide) exactCollision(sim, collide, ax, ay, az);
  } else {
    octreeRepulsion3D(sim, cfg.repulsion, ax, ay, az, { theta: cfg.theta ?? THETA, collide });
  }
  for (let i = 0; i < n; i++) { ax[i] -= x[i] * cfg.gravity; ay[i] -= y[i] * cfg.gravity; az[i] -= z[i] * cfg.gravity; }
  sim.clusterRamp = cfg.cluster ? Math.min(1, sim.clusterRamp + 1 / 40) : 0;
  let gid = null, same = 1, cross = 1;
  if (cfg.cluster && sim.clusterRamp > 0) {
    gid = applyCluster3D(sim, nodes, cfg, sim.clusterRamp, ax, ay, az);
    if (gid) {
      same = 1 + CLUSTER_TUNING.linkSame * unit01(cfg.cluster_attract, 0.5) * sim.clusterRamp;
      cross = 1 - CLUSTER_TUNING.linkCross * unit01(cfg.cluster_repel, 0.5) * sim.clusterRamp;
    }
  }
  const rest = cfg.distance || 0;
  for (let l = 0; l < links.length; l += 2) {
    const a = links[l], b = links[l + 1];
    const dx = x[b] - x[a], dy = y[b] - y[a], dz = z[b] - z[a];
    const d = Math.sqrt(dx * dx + dy * dy + dz * dz) || 1;
    let f = (d - rest) * 0.02;
    if (gid && gid[a] >= 0 && gid[b] >= 0) f *= gid[a] === gid[b] ? same : cross;
    const ux = dx / d * f, uy = dy / d * f, uz = dz / d * f;
    ax[a] += ux; ay[a] += uy; az[a] += uz;
    ax[b] -= ux; ay[b] -= uy; az[b] -= uz;
  }
  // Past the classic stopping point the motion cools with alpha (as in 2D).
  sim.alpha *= 0.985;
  const cool = sim.alpha < 0.02 ? sim.alpha / 0.02 : 1;
  const keep = 1 - cfg.damping;
  let sum = 0, max = 0;
  for (let i = 0; i < n; i++) {
    if (fixed[i] || i === dragged) { vx[i] = vy[i] = vz[i] = 0; continue; }
    vx[i] = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, (vx[i] + ax[i]) * keep)) * cool;
    vy[i] = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, (vy[i] + ay[i]) * keep)) * cool;
    vz[i] = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, (vz[i] + az[i]) * keep)) * cool;
    x[i] += vx[i]; y[i] += vy[i]; z[i] += vz[i];
    const moved = Math.sqrt(vx[i] * vx[i] + vy[i] * vy[i] + vz[i] * vz[i]);
    sum += moved;
    if (moved > max) max = moved;
  }
  sim.ticks++;
  return { mean: sum / n, max };
}

/** Bounding sphere of the layout: { cx, cy, cz, radius }. */
export function simBounds(sim, filter = null) {
  const { n, x, y, z } = sim;
  let cx = 0, cy = 0, cz = 0, m = 0;
  for (let i = 0; i < n; i++) if (!filter || filter(i)) { cx += x[i]; cy += y[i]; cz += z[i]; m++; }
  if (!m) return { cx: 0, cy: 0, cz: 0, radius: 100 };
  cx /= m; cy /= m; cz /= m;
  // The 95th percentile distance, so a few far outliers do not shrink the view.
  const d = [];
  for (let i = 0; i < n; i++) if (!filter || filter(i)) d.push(Math.hypot(x[i] - cx, y[i] - cy, z[i] - cz) + sim.r[i]);
  d.sort((p, q) => p - q);
  return { cx, cy, cz, radius: Math.max(60, d[Math.min(d.length - 1, Math.floor(d.length * 0.95))]) };
}
