import { quadtree } from "../vendor/d3-quadtree/d3-quadtree.js";
import { applyExactRepulsion } from "./graph-core.js";

export function applyRepulsion(nodes, strength, { theta = 0.55, exactThreshold = 400 } = {}) {
  const n = nodes.length;
  if (!strength || n < 2) return;
  const pair = (a, b, i, j, mass = 1) => {
    let dx = a.x - b.x, dy = a.y - b.y;
    let d2 = dx * dx + dy * dy;
    if (d2 < 0.000001) {
      const angle = (Math.min(i, j) * 31 + Math.max(i, j)) * 2.399963;
      const sign = i < j ? 1 : -1;
      dx = Math.cos(angle) * sign; dy = Math.sin(angle) * sign; d2 = 1;
    }
    const factor = strength * mass / (Math.max(64, d2) * Math.sqrt(d2));
    return [dx * factor, dy * factor];
  };
  if (n <= exactThreshold) {
    applyExactRepulsion(nodes, strength);
    return;
  }
  const index = new Map(nodes.map((node, i) => [node, i]));
  const tree = quadtree(nodes, (node) => node.x, (node) => node.y);
  tree.visitAfter((quad) => {
    let mass = 0, x = 0, y = 0;
    if (quad.length) {
      for (const child of quad) if (child) {
        mass += child.mass; x += child.cx * child.mass; y += child.cy * child.mass;
      }
    } else {
      for (let leaf = quad; leaf; leaf = leaf.next) {
        mass++; x += leaf.data.x; y += leaf.data.y;
      }
    }
    quad.mass = mass; quad.cx = x / mass; quad.cy = y / mass;
  });
  const ax = new Float64Array(n), ay = new Float64Array(n);
  let sumX = 0, sumY = 0;
  for (let i = 0; i < n; i++) {
    const node = nodes[i];
    tree.visit((quad, x0, y0, x1, y1) => {
      if (!quad.mass) return true;
      if (quad.length) {
        const dx = node.x - quad.cx, dy = node.y - quad.cy;
        const d2 = dx * dx + dy * dy, width = x1 - x0;
        const contains = node.x >= x0 && node.x < x1 && node.y >= y0 && node.y < y1;
        if (!contains && width * width < theta * theta * d2) {
          const factor = strength * quad.mass / (Math.max(64, d2) * Math.sqrt(d2));
          ax[i] += dx * factor; ay[i] += dy * factor;
          return true;
        }
      } else {
        for (let leaf = quad; leaf; leaf = leaf.next) if (leaf.data !== node) {
          const [x, y] = pair(node, leaf.data, i, index.get(leaf.data));
          ax[i] += x; ay[i] += y;
        }
      }
      return false;
    });
    sumX += ax[i]; sumY += ay[i];
  }
  // Approximation must not introduce a net translation of the graph.
  const meanX = sumX / n, meanY = sumY / n;
  for (let i = 0; i < n; i++) {
    nodes[i].vx += ax[i] - meanX; nodes[i].vy += ay[i] - meanY;
  }
}
