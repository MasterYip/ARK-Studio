# Node graph link curves

How other node editors shape links, and what the node graph
(`ark_studio/web/js/node-graph-layout.js`) uses. Every link leaves the source's output
port (right edge) and enters the target's input port (left edge) horizontally.
"Forward" means the target port is to the right of the source port; a
"backward" link points left.

## Other editors

| Editor | Forward link: tangent (control-point offset) | Backward link | Source |
| --- | --- | --- | --- |
| Blender node editor | `curving × 0.1 × |dx|`, scaled by `min(1, slope × (4.5 − 0.25 × curving))` (preference *Noodle Curving*, default 5; 0 = straight line) | Same formula with `|dx|`: handles point outwards, so the link leaves right, swings back and enters from the left; the loop grows with the gap | `source/blender/editors/space_node/drawnode.cc`, `calculate_inner_link_bezier_points` |
| ComfyUI / LiteGraph.js | `0.25 × distance` (Euclidean) along each slot direction (*Spline*). *Linear*: 15 px stubs and a straight segment. *Straight*: 10 px stubs, orthogonal through the horizontal midpoint | Same: tangents grow with the straight-line distance, a wide S | `litegraph.js`, `LGraphCanvas.prototype.renderLink` (`LINK_RENDER_MODES`) |
| React Flow / xyflow | `getBezierPath`: `0.5 × gap` when the target is ahead, `curvature × 25 × √(−gap)` (curvature 0.25) when it is behind | A tight S whose tangents grow only with √gap; it crosses back over the cards | `packages/system/src/utils/edges/bezier-edge.ts`, `calculateControlOffset` |
| React Flow / xyflow (smooth step) | Orthogonal path with a 20 px offset and 5 px rounded corners, one vertical jog at `centerX` | Out 20 px, vertical to the midline between source and target, across, down into the target | `packages/system/src/utils/edges/smoothstep-edge.ts`, `getSmoothStepPath` |
| Rete.js | `curvature × max(|dy| / 2, |dx|)` with curvature 0.3 | Same formula; tangents follow the larger of the two gaps | `rete-render-utils`, `src/connection.ts`, `classicConnectionPath` |

Unreal Blueprints, Houdini and Node-RED are not open in a form we could read
for this comparison; they are not included.

None of these route around other nodes: they draw one curve per link
regardless of what lies under it. The node graph keeps its obstacle-aware
routing (corridors, port slots, long-link channels from the layered layout)
and only takes the curve shape from them.

## Comparison on the node layout

`node scripts/bench/node_graph_layout_metrics.mjs` lays out the graphs with
the layered layout, then `node scripts/bench/node_link_styles.mjs` routes them
with each model. "Through cards" counts links passing over a card that is not
their endpoint; link box area sums each link's bounding box (thousands of
world px²); routing time is the median of three runs.

| Graph (links / backward) | Forward / backward | Crossings | Through cards | Length | Link box area | Backward box area | Backward length | Route ms |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Demo (133 / 0) | previous / previous loop | 801 | 0 | 156,075 | 90,801 | — | — | 7.0 |
| Demo | xyflow / compact | **491** | 0 | 155,381 | 90,801 | — | — | 1.1 |
| Demo | Blender / compact | 491 | 0 | 155,380 | 90,801 | — | — | 2.2 |
| Demo | Rete / compact | 1,387 | 0 | 156,301 | 90,801 | — | — | 0.8 |
| Demo | LiteGraph / compact | 1,603 | 5 | 157,994 | 91,176 | — | — | 2.5 |
| 300 synthetic (391 / 6) | previous / previous loop | 3,489 | 13 | 957,385 | 2,237,695 | 179,025 | 65,101 | 5.9 |
| 300 synthetic | xyflow / compact | **2,107** | **3** | 943,714 | 2,191,146 | 132,476 | 54,271 | 2.6 |
| 300 synthetic | xyflow / xyflow tight S | 2,263 | 45 | 928,464 | 2,190,184 | 131,515 | 39,022 | 2.1 |
| 300 hand-arranged (391 / 68) | previous / previous loop | 6,246 | 124 | 1,461,959 | 2,511,286 | 628,907 | 467,981 | 9.2 |
| 300 hand-arranged | xyflow / compact | **4,655** | 84 | 1,413,419 | 2,331,184 | **448,805** | 421,471 | 4.8 |
| 300 hand-arranged | Blender / compact | 4,658 | 89 | 1,414,609 | 2,331,184 | 448,805 | 421,471 | 5.1 |
| 300 hand-arranged | xyflow / xyflow tight S | 4,601 | 176 | 1,348,422 | 2,326,226 | 443,847 | 356,512 | 2.1 |

The demo's shown directions include the display reversal of `derived_from`
and `depends_on`. In the hand-arranged variant 20% of the cards were moved
to another layout column, which is where backward links come from in
practice (manual positions, topics).

## Choice

- **Forward: xyflow tangents, `0.5 × dx`.** The previous rule added a minimum
  of 36 px and `0.25 × dy` for steep links; the extra bulge made steep curves
  overlap their neighbours (crossings −39% on the demo and −40% on the
  300-node graph). Blender's rule is the same except that nearly horizontal
  links get shorter handles; the results are equal within noise, so the
  simpler rule is used. Rete and LiteGraph scale the tangent with the
  vertical gap or the full distance and roughly double the crossings.
- **Backward: compact smooth-step loop** (xyflow's step path with our lane
  choice): a 22 px stub out of the output, a vertical run beside the source,
  a horizontal lane, a vertical run beside the target and a 22 px stub into
  the input, with 14 px rounded corners. The lane is the gap between the two
  cards when there is one, otherwise just above or below both, nudged off
  cards and other lanes. Previously the loop's tangents grew with the
  vertical distance (55%), so a backward link bulged far to the side.
  Backward link area −27% (300) and −29% (hand-arranged), links through
  cards 13 → 3 and 124 → 84. xyflow's tight S is shorter still but runs over
  the cards themselves (45 and 176 links through cards), so it is not used.
- Obstacle-aware corridors, port slots and long-link channels are unchanged
  and still take over when a direct curve would cross a card.

No link-style setting was added: the defaults above won on every metric
except raw length, and a Straight/Linear choice can be added later through
the arrow settings if wanted.
