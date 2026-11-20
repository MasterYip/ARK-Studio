// Scripts for the graph walkthrough (graph-demo.js): a small sample graph per
// layout, the steps that drive it, and the sandbox callbacks that stand in for
// the server.  Pure data and functions, no imports: the sandbox never reaches
// the API, and tests load this module on its own.
//
// A step is { caption, ops }.  `caption` is an i18n key.  Ops:
//   { op: "move", to }                     the cursor glides to a target
//   { op: "click", to } / { op: "dblclick", to }
//   { op: "drag", from, to, button, alt }  button 0 (left) or 2 (right)
//   { op: "wheel", at, deltaY, times }
//   { op: "key", key, ctrl, shift }        sent to the graph (or its title editor)
//   { op: "type", text }                   i18n key typed into the title editor
//   { op: "button", selector }             a toolbar button of the sandbox graph
//   { op: "wait", ms }
// Targets: { node, dx, dy } (screen px offset from the node centre),
// { at: [fx, fy] } (fraction of the canvas), { edge } (a node-graph route
// handle), { graph: "tl" | "c" | ..., pad } (box around every node),
// { rel: [dx, dy] } (drag end relative to the drag start), { selector }.

export const DEMO_LAYOUTS = ["force", "mind", "node"];

// The demo for the graph's current layout (Tree reads like Force).
export function demoLayoutFor(layout) {
  return DEMO_LAYOUTS.includes(layout) ? layout : "force";
}

const NODE = (id, title, type, extra = {}) => ({
  id, uid: `DEMO-${id.slice(1)}`, titleKey: `graph.demo.node.${title}`,
  type, status: "open", importance: "p2", tags: [], ...extra,
});
const LINK = (id, source, target, relation = "part_of") => ({ id, source, target, relation });

const DATASETS = {
  force: {
    nodes: [
      NODE("d1", "launch", "topic"), NODE("d2", "budget", "decision"), NODE("d3", "timeline", "event"),
      NODE("d4", "venue", "todo"), NODE("d5", "speakers", "question"), NODE("d6", "catering", "todo"),
      NODE("d7", "invites", "note"), NODE("d8", "feedback", "note"),
    ],
    edges: [
      LINK("e1", "d2", "d1"), LINK("e2", "d3", "d1"), LINK("e3", "d4", "d1"), LINK("e4", "d5", "d1"),
      LINK("e5", "d6", "d4"), LINK("e6", "d7", "d3"), LINK("e7", "d8", "d5"), LINK("e8", "d2", "d4", "supports"),
    ],
  },
  mind: {
    nodes: [
      NODE("d1", "launch", "topic"), NODE("d2", "budget", "decision"), NODE("d3", "venue", "todo"),
      NODE("d4", "catering", "todo"), NODE("d5", "timeline", "event"), NODE("d6", "invites", "note"),
      NODE("d7", "speakers", "question"),
    ],
    edges: [
      LINK("e1", "d2", "d1"), LINK("e2", "d3", "d2"), LINK("e3", "d4", "d2"), LINK("e4", "d5", "d1"),
      LINK("e5", "d6", "d5"), LINK("e6", "d7", "d1"),
    ],
  },
  node: {
    nodes: [
      NODE("d1", "idea", "question"), NODE("d2", "draft", "note"), NODE("d3", "review", "todo"),
      NODE("d4", "publish", "event"), NODE("d5", "assets", "resource"), NODE("d6", "feedback", "note"),
    ],
    edges: [
      LINK("e1", "d1", "d2", "produces"), LINK("e2", "d2", "d3", "depends_on"), LINK("e3", "d3", "d4", "blocks"),
      LINK("e4", "d6", "d3", "supports"),
    ],
  },
};

// A fresh copy of the sample graph; `translate(key)` gives node titles.
export function demoDataset(layout, translate = (key) => key) {
  const data = DATASETS[demoLayoutFor(layout)];
  return {
    nodes: data.nodes.map(({ titleKey, ...node }) => ({ ...node, title: translate(titleKey), tags: [...node.tags] })),
    edges: data.edges.map((edge) => ({ ...edge })),
  };
}

// The mode each demo starts in (the graph's own default for that layout).
export const DEMO_START_MODE = { force: "read", mind: "edit", node: "read" };

const C = (name) => `graph.demo.caption.${name}`;

export const DEMO_SCRIPTS = {
  force: [
    { caption: C("force_select"), ops: [{ op: "click", to: { node: "d2" } }] },
    { caption: C("force_drag"), ops: [{ op: "drag", from: { node: "d3" }, to: { rel: [120, -70] } }] },
    { caption: C("force_pan"), ops: [{ op: "drag", button: 2, from: { at: [0.22, 0.78] }, to: { rel: [150, -90] } }] },
    { caption: C("force_zoom"), ops: [
      { op: "wheel", at: { node: "d1" }, deltaY: -160, times: 3 },
      { op: "wheel", at: { node: "d1" }, deltaY: 160, times: 2 },
    ] },
    { caption: C("force_fit"), ops: [{ op: "key", key: "f" }] },
    { caption: C("force_box"), ops: [{ op: "drag", from: { graph: "tl", pad: 26 }, to: { graph: "c", pad: 0 } }] },
    { caption: C("force_clear"), ops: [{ op: "key", key: "Escape" }] }, // i18n-ignore: KeyboardEvent.key
    { caption: C("force_edit"), ops: [{ op: "key", key: "e" }] },
    { caption: C("force_link"), ops: [{ op: "drag", alt: true, from: { node: "d6" }, to: { node: "d8" } }] },
    { caption: C("force_rename"), ops: [
      { op: "dblclick", to: { node: "d7" } },
      { op: "type", text: "graph.demo.text.guest_list" },
      { op: "key", key: "Enter" },
    ] },
    { caption: C("force_read"), ops: [{ op: "key", key: "r" }] },
  ],
  mind: [
    { caption: C("mind_select"), ops: [{ op: "click", to: { node: "d1" } }] },
    { caption: C("mind_child"), ops: [
      { op: "key", key: "Tab" },
      { op: "type", text: "graph.demo.text.agenda" },
      { op: "key", key: "Enter" },
    ] },
    { caption: C("mind_sibling"), ops: [
      { op: "key", key: "Enter" },
      { op: "type", text: "graph.demo.text.qa" },
      { op: "key", key: "Enter" },
    ] },
    { caption: C("mind_arrows"), ops: [
      { op: "key", key: "ArrowUp" }, { op: "key", key: "ArrowUp" }, { op: "key", key: "ArrowLeft" },
    ] },
    { caption: C("mind_rename"), ops: [
      { op: "dblclick", to: { node: "d2" } },
      { op: "type", text: "graph.demo.text.costs" },
      { op: "key", key: "Enter" },
    ] },
    { caption: C("mind_undo"), ops: [{ op: "key", key: "z", ctrl: true }] },
    { caption: C("mind_fold"), ops: [
      { op: "click", to: { node: "d2" } }, { op: "key", key: "-" }, { op: "wait", ms: 700 }, { op: "key", key: "+" },
    ] },
    { caption: C("mind_fold_all"), ops: [
      { op: "click", to: { node: "d1" } }, { op: "key", key: "/" }, { op: "wait", ms: 800 }, { op: "key", key: "*" },
    ] },
    { caption: C("mind_fit"), ops: [{ op: "key", key: "f" }] },
  ],
  node: [
    { caption: C("node_cards"), ops: [{ op: "move", to: { node: "d2" } }, { op: "wait", ms: 900 }] },
    { caption: C("node_move"), ops: [{ op: "drag", from: { node: "d3" }, to: { rel: [0, 110] } }] },
    { caption: C("node_edit"), ops: [{ op: "key", key: "e" }] },
    { caption: C("node_bend"), ops: [{ op: "drag", from: { edge: "e2" }, to: { rel: [0, -70] } }] },
    { caption: C("node_link"), ops: [{ op: "drag", alt: true, from: { node: "d5" }, to: { node: "d4" } }] },
    { caption: C("node_auto"), ops: [{ op: "button", selector: ".graph-node-auto" }, { op: "wait", ms: 900 }] },
    { caption: C("node_undo"), ops: [{ op: "key", key: "z", ctrl: true }, { op: "wait", ms: 500 }] },
    { caption: C("node_zoom"), ops: [
      { op: "wheel", at: { node: "d3" }, deltaY: -160, times: 3 },
      { op: "key", key: "f" },
    ] },
  ],
};

// Display names of keys (KeyboardEvent.key -> label).
const KEY_NAMES = {
  " ": "Space", Escape: "Esc", ArrowUp: "↑", ArrowDown: "↓", ArrowLeft: "←", ArrowRight: "→",
  "-": "−", Delete: "Del", Control: "Ctrl", Meta: "Ctrl",
};

export function keyName(key) {
  if (KEY_NAMES[key]) return KEY_NAMES[key];
  return key.length === 1 ? key.toUpperCase() : key;
}

// What the corner overlay shows for an op: a list of tokens.  Tokens starting
// with "input." are i18n keys (graph.demo.input.*), the rest are key names.
export function opKeys(op) {
  switch (op.op) {
    case "click": case "button": return ["input.click"];
    case "dblclick": return ["input.dblclick"];
    case "drag":
      if (op.button === 2) return ["input.right_drag"];
      return op.alt ? ["Alt", "input.drag"] : ["input.drag"];
    case "wheel": return ["input.wheel"];
    case "type": return ["input.type"];
    case "key": {
      const tokens = [];
      if (op.ctrl) tokens.push("Ctrl");
      if (op.shift) tokens.push("Shift");
      tokens.push(keyName(op.key));
      return tokens;
    }
    default: return [];
  }
}

// Every key the demo presses, as { key, ctrl, shift } (tests check each one
// against graph.js).
export function demoKeys(layout) {
  return (DEMO_SCRIPTS[layout] || []).flatMap((step) => step.ops)
    .filter((op) => op.op === "key")
    .map((op) => ({ key: op.key, ctrl: Boolean(op.ctrl), shift: Boolean(op.shift) }));
}

// The shortcut-help row that documents each overlay token: a literal <kbd>
// text from GraphView._renderHelp or an i18n key used there.
export const HELP_ROWS = {
  "input.click": "graph.help.left_click",
  "input.dblclick": "graph.help.space_dblclick",
  "input.drag": "graph.help.left_drag",
  "input.right_drag": "graph.help.right_drag",
  "input.wheel": "graph.help.wheel",
  "input.type": "graph.help.edit_title",
  Alt: "graph.help.alt_drag",
  F: "F", R: "R / E", E: "R / E", "Esc": "Esc", Tab: "Tab", Enter: "Enter",
  "↑": "← → ↑ ↓", "↓": "← → ↑ ↓", "←": "← → ↑ ↓", "→": "← → ↑ ↓",
  "+": "+ / -", "−": "+ / -", "*": "* / /", "/": "* / /", Ctrl: "Ctrl+Z", Z: "Ctrl+Z",
};

// Overlay text for a token list, e.g. ["Ctrl", "Z"] -> "Ctrl + Z".
export function formatKeys(tokens, translate = (key) => key) {
  return tokens.map((token) => (token.startsWith("input.") ? translate(`graph.demo.${token}`) : token)).join(" + ");
}

// Tokens for a real key press ("Show pressed keys"); null for a lone modifier.
export function eventKeyTokens(event) {
  const key = event?.key;
  if (!key || ["Control", "Shift", "Alt", "Meta", "AltGraph", "CapsLock"].includes(key)) return null; // i18n-ignore: KeyboardEvent.key names
  const tokens = [];
  if (event.ctrlKey || event.metaKey) tokens.push("Ctrl");
  if (event.altKey) tokens.push("Alt");
  // Shift is part of a printed symbol (*, +, ?) and is not listed for it.
  if (event.shiftKey && (key.length > 1 || /[a-z]/i.test(key))) tokens.push("Shift");
  tokens.push(keyName(key));
  return tokens;
}

// Tokens for a real pointer press.
export function eventPointerTokens(event) {
  const tokens = [];
  if (event.ctrlKey || event.metaKey) tokens.push("Ctrl");
  if (event.shiftKey) tokens.push("Shift");
  if (event.altKey) tokens.push("Alt");
  tokens.push(event.button === 2 ? "input.right_button" : "input.click");
  return tokens;
}

// Rough length of a script in ms at normal speed (the player's timings).
export const DEMO_TIMING = { glide: 650, press: 260, drag: 900, wheel: 140, key: 520, char: 85, button: 420, stepPause: 950 };
export function estimateDuration(layout, translate = (key) => key) {
  let ms = 0;
  for (const step of DEMO_SCRIPTS[layout] || []) {
    ms += DEMO_TIMING.stepPause;
    for (const op of step.ops) {
      if (op.op === "move") ms += DEMO_TIMING.glide;
      else if (op.op === "click" || op.op === "dblclick") ms += DEMO_TIMING.glide + DEMO_TIMING.press * 2;
      else if (op.op === "drag") ms += DEMO_TIMING.glide + DEMO_TIMING.drag + DEMO_TIMING.press * 2;
      else if (op.op === "wheel") ms += DEMO_TIMING.glide + (op.times || 1) * DEMO_TIMING.wheel + DEMO_TIMING.press;
      else if (op.op === "key") ms += DEMO_TIMING.key;
      else if (op.op === "type") ms += [...translate(op.text)].length * DEMO_TIMING.char + DEMO_TIMING.press;
      else if (op.op === "button") ms += DEMO_TIMING.glide + DEMO_TIMING.button;
      else if (op.op === "wait") ms += op.ms || 0;
    }
  }
  return ms;
}

// GraphView callbacks for the sandbox.  They change only the sample graph in
// memory (ids "demo-new-<n>"); none of them reaches the server.
export function demoGraphOptions({ newTitle = "", getGraph = () => null } = {}) {
  let seq = 0;
  const nextId = () => `demo-new-${++seq}`;
  const layoutState = { positions: {}, bends: {}, widths: {} };
  const resolved = (value) => Promise.resolve(value);
  return {
    onSelect: () => {},
    onEdgeClick: () => {},
    onEdgeSelect: () => {},
    onModeChange: () => {},
    onNotice: () => {},
    onError: () => {},
    onRename: () => resolved(true),
    onAddNode: (node, where) => {
      const graph = getGraph();
      const parentEdge = graph?.edges?.find((edge) => edge.source === node.id && edge.relation === "part_of");
      const parentId = where === "child" ? node.id : (parentEdge ? parentEdge.target : null);
      const id = nextId();
      return {
        unit: { id, uid: id.toUpperCase(), title: newTitle, type: node.type || "note", status: "open", importance: "p2", tags: [] },
        link: parentId ? { id: `${id}-link` } : null,
        parentId,
        orphan: !parentId,
      };
    },
    onAddOrphan: () => {
      const id = nextId();
      return resolved({ unit: { id, uid: id.toUpperCase(), title: newTitle, type: "note", status: "open", importance: "p2", tags: [] } });
    },
    onConnect: (source, target) => resolved({ link: { id: `demo-link-${source.id}-${target.id}` }, relation: "unset" }),
    onDelete: () => resolved(false),
    onRestoreNodes: () => resolved(true),
    onRestoreLinks: () => resolved(true),
    nodeLayoutStore: {
      load: () => layoutState,
      save: (state) => { Object.assign(layoutState, state); },
    },
  };
}
