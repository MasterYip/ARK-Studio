import { h } from "../dom.js";
import { GraphView } from "../graph.js";
import { mindGraphOptions, preferredGraphLayout } from "../mind.js";
import { renderTopics } from "./topics.js";
import { focusLocated, syncRevealNotice } from "../locate-map.js";

// Research > Story / Related / Method / Experiment are the project's default
// topics; every plan route renders the topic workspace (views/topics.js).
export function renderPlan(host, ctx) {
  const sub = ctx.S.sub || "story";
  return renderTopics(host, ctx, { defaultKey: sub === "topic" ? null : sub });
}

export function graphPanel(ctx, S) {
  const map = S.tab === "map";
  const panel = h("div", { class: "graph-panel", dataset: map ? { mapGraph: "1" } : {} });
  const host = h("div", { class: "graph-host" });
  panel.appendChild(host);
  const graph = new GraphView(host, {
    cameraKey: `${ctx.S.project?.id || ""}:${S.tab}:${S.sub || ""}:${S.view}`,
    nodeGraphKey: `ark.nodeGraph.v1:${ctx.S.project?.path || ""}:${S.tab}:${S.sub || ""}:${S.view}`,
    isInspectorOpen: () => Boolean(ctx.S.inspectorPane !== false || ctx.S.drawerOpen),
    onSelect: (node, dbl, event) => { if (node) ctx.openUnit(node.uid || node.id, event ? { x: event.clientX, y: event.clientY } : null); },
    ...mindGraphOptions(ctx),
    mapGraph: map,
  });
  ctx.trackGraph(graph);
  graph.setData(S.graph?.nodes || [], S.graph?.edges || [], { selected: null, layout: preferredGraphLayout(S.view === "split" ? "tree" : "force") });
  if (map) {
    syncRevealNotice(ctx, panel);
    // "Locate in research map" / "Show on map": select and centre the node
    // once the layout has placed it (locate-map.js).
    if (ctx.S.mapFocus) focusLocated(ctx, graph, ctx.S.mapFocus);
  }
  return panel;
}
