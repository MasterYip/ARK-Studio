import { h } from "../dom.js";
import { GraphView } from "../graph.js";
import { mindGraphOptions, preferredGraphLayout } from "../mind.js";
import { renderTopics } from "./topics.js";

// Research > Story / Related / Method / Experiment are the project's default
// topics; every plan route renders the topic workspace (views/topics.js).
export function renderPlan(host, ctx) {
  const sub = ctx.S.sub || "story";
  return renderTopics(host, ctx, { defaultKey: sub === "topic" ? null : sub });
}

export function graphPanel(ctx, S) {
  const panel = h("div", { class: "graph-panel" });
  const host = h("div", { class: "graph-host" });
  panel.appendChild(host);
  const graph = new GraphView(host, {
    cameraKey: `${S.tab}:${S.sub || ""}:${S.view}`,
    nodeGraphKey: `ark.nodeGraph.v1:${ctx.S.project?.path || ""}:${S.tab}:${S.sub || ""}:${S.view}`,
    isInspectorOpen: () => Boolean(ctx.S.inspectorPane !== false || ctx.S.drawerOpen),
    onSelect: (node, dbl, event) => { if (node) ctx.openUnit(node.uid || node.id, event ? { x: event.clientX, y: event.clientY } : null); },
    ...mindGraphOptions(ctx),
  });
  ctx.trackGraph(graph);
  graph.setData(S.graph?.nodes || [], S.graph?.edges || [], { selected: null, layout: preferredGraphLayout(S.view === "split" ? "tree" : "force") });
  if (S.tab === "map" && ctx.S.mapFocus) focusWhenPlaced(ctx, graph, ctx.S.mapFocus);
  return panel;
}

// "Show on map" from Node details: select the node and centre it once the
// layout has placed it (a force layout keeps moving for a moment).
function focusWhenPlaced(ctx, graph, uid) {
  ctx.S.mapFocus = null;
  const find = () => (graph.nodes || []).find((node) => node.uid === uid || String(node.id) === String(uid));
  if (!find()) return;
  for (const delay of [80, 700]) {
    setTimeout(() => {
      const node = find();
      if (node && Number.isFinite(node.x)) graph.focus(node);
    }, delay);
  }
  ctx.openUnit(uid);
}
