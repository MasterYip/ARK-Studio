import { GRAPH_DEFAULTS, stepForce, typeColor, forceNodeRadius, drawForceNode } from "./graph-core.js";

export function createEntryForce(host, initialProgress) {
  const canvas = host.querySelector("canvas"), ctx = canvas.getContext("2d");
  if (!ctx) return null;
  const reduced = matchMedia("(prefers-reduced-motion: reduce)");
  const types = ["claim", "evidence", "task", "method", "question", "artifact", "idea", "experiment"];
  const statuses = ["open", "active", "review", "done", "open", "blocked"];
  const sample = Array.from({ length: 73 }, (_, i) => ({
    id: String(i), parent: i <= 6 ? 0 : i <= 24 ? 1 + (i - 7) % 6 : 7 + (i - 25) % 18,
    type: i ? types[(i - 1) % types.length] : "story", status: statuses[i % statuses.length],
    x: Math.cos(i * 2.399963) * 25 * Math.sqrt(i),
    y: Math.sin(i * 2.399963) * 25 * Math.sqrt(i), vx: 0, vy: 0, degree: 0,
  }));
  const edges = sample.slice(1).map((n) => {
    sample[n.parent].degree++; n.degree++;
    return { source: n.id, target: String(n.parent), relation: "part_of" };
  });
  let nodes = [], byId = new Map(), activeEdges = [], progress = initialProgress, shown = initialProgress;
  let width = 0, height = 0, frame = 0, last = 0, alpha = 1, destroyed = false;

  function populate() {
    const count = 1 + shown * (sample.length - 1), target = Math.ceil(count);
    if (target < nodes.length) { nodes = []; byId.clear(); }
    while (nodes.length < target) {
      const index = nodes.length, template = sample[index], parent = nodes[template.parent];
      const node = { ...template };
      if (shown < 1 && parent) {
        node.x = parent.x + Math.cos(index * 2.399963) * 18;
        node.y = parent.y + Math.sin(index * 2.399963) * 18;
      }
      nodes.push(node); byId.set(node.id, node); alpha = 1;
    }
    activeEdges = edges.filter((edge) => byId.has(edge.source) && byId.has(edge.target));
    nodes.forEach((n, i) => { n._appear = Math.max(0, Math.min(1, count - i)); });
    canvas.dataset.visibleNodes = String(target);
  }

  function draw() {
    ctx.clearRect(0, 0, width, height);
    const css = getComputedStyle(document.documentElement);
    const bg = css.getPropertyValue("--entry-bg").trim(), sub = css.getPropertyValue("--entry-sub").trim();
    // Camera only: viewport changes never alter physics parameters.
    const zoom = Math.max(width / 760, height / 620) * (host.dataset.entry === "login" ? 1 : .8);
    const screen = (node) => ({ x: width / 2 + node.x * zoom, y: height / 2 + node.y * zoom });
    ctx.strokeStyle = sub; ctx.lineWidth = 1;
    for (const edge of activeEdges) {
      const source = byId.get(edge.source), target = byId.get(edge.target);
      const a = screen(source), b = screen(target);
      ctx.globalAlpha = .28 * Math.min(source._appear, target._appear);
      ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
    }
    for (const node of nodes) {
      const p = screen(node);
      ctx.globalAlpha = node._appear;
      drawForceNode(ctx, node, p.x, p.y, forceNodeRadius(node, zoom), typeColor(node.type), bg);
    }
    ctx.globalAlpha = 1;
  }

  function resize() {
    width = host.clientWidth; height = host.clientHeight;
    const dpr = Math.min(devicePixelRatio || 1, 2);
    canvas.width = Math.round(width * dpr); canvas.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0); draw();
  }
  function tick(time) {
    frame = 0;
    if (destroyed) return;
    if (!document.hidden) {
      shown = reduced.matches ? progress : shown + (progress - shown) * .16;
      if (Math.abs(progress - shown) < .001) shown = progress;
      populate();
      if (!reduced.matches && alpha > .02) {
        stepForce(nodes, activeEdges, byId, GRAPH_DEFAULTS); alpha *= .985;
      }
      if (time - last > 32 || reduced.matches) { draw(); last = time; }
    }
    if ((!reduced.matches && alpha > .02) || shown !== progress) wake();
  }
  function wake() { if (!frame && !destroyed) frame = requestAnimationFrame(tick); }
  function setProgress(value, reset) {
    progress = value;
    if (reset || reduced.matches) { shown = progress; populate(); }
    alpha = 1; draw(); wake();
  }
  function motionChanged() { if (reduced.matches) { shown = progress; populate(); draw(); } wake(); }
  populate();
  for (let i = 0; i < 72; i++) stepForce(nodes, activeEdges, byId, GRAPH_DEFAULTS);
  resize(); wake();
  window.addEventListener("resize", resize);
  reduced.addEventListener("change", motionChanged);
  return { draw, progress: setProgress, destroy() {
    destroyed = true; cancelAnimationFrame(frame);
    window.removeEventListener("resize", resize); reduced.removeEventListener("change", motionChanged);
  } };
}
