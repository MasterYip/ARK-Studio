// T-0043: agent presence model (merge, expire, stack, colours) and the
// overlay: a presence update draws the overlay only and never touches the
// graph (no relayout, refit, reheat, redraw; positions and camera unchanged).
// Run: node --test tests/js/presence_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  FADE_MS, GLIDE_MS, PRESENCE_COLORS, activeAgents, createPresenceModel, expirePresence, mergePresence,
  presenceAlpha, presenceByUnit, presenceColor, presenceGlide, presenceInitials, presenceOnUnit,
} from "../../ark_studio/web/js/presence-model.js";
import { centreOnUnit, createPresenceOverlay } from "../../ark_studio/web/js/graph-presence.js";

const T0 = Date.parse("2026-11-17T10:00:00Z");
const iso = (ms) => new Date(ms).toISOString().replace(/\.\d+Z$/, "+00:00");

function entry(actor, unit, extra = {}) {
  return { actor, label: actor.replace(/^agent:/, ""), kind: "cursor", state: "reading", unit, uid: `T-${unit}`,
    title: `Node ${unit}`, action: "read", note: "", at: iso(T0), expires_at: iso(T0 + 30000), ...extra };
}
const payload = (entries, now = T0) => ({ project: "prj_a", now: iso(now), entries });

test("initials", () => {
  assert.equal(presenceInitials("claude"), "CL");
  assert.equal(presenceInitials("gemini-cli"), "GC");
  assert.equal(presenceInitials("ResearchBot"), "RB");
  assert.equal(presenceInitials("研究助手"), "研");
  assert.equal(presenceInitials(""), "?");
});

test("merge: add, move (glide), update, remove (fade)", () => {
  const m = createPresenceModel();
  let r = mergePresence(m, payload([entry("agent:a", 1)]), T0);
  assert.deepEqual(r.added, ["agent:a|cursor"]);
  assert.ok(r.changed);
  const a = m.entries.get("agent:a|cursor");
  assert.equal(a.prevUnit, null);
  // Same state again: nothing changed.
  r = mergePresence(m, payload([entry("agent:a", 1)]), T0 + 100);
  assert.equal(r.changed, false);
  // Moved to unit 2: glides from 1.
  r = mergePresence(m, payload([entry("agent:a", 2, { state: "writing", action: "update" })]), T0 + 1000);
  assert.deepEqual(r.moved, ["agent:a|cursor"]);
  assert.equal(a.prevUnit, 1);
  assert.equal(a.state, "writing");
  assert.equal(presenceGlide(a, T0 + 1000), 0);
  assert.equal(presenceGlide(a, T0 + 1000 + GLIDE_MS), 1);
  assert.ok(presenceGlide(a, T0 + 1000 + GLIDE_MS / 2) > 0.5, "eased");
  // Gone from the payload: fades out, then is forgotten.
  r = mergePresence(m, payload([]), T0 + 2000);
  assert.deepEqual(r.removed, ["agent:a|cursor"]);
  assert.ok(a.leaving);
  assert.equal(presenceAlpha(a, T0 + 2000), 1);
  assert.equal(presenceAlpha(a, T0 + 2000 + FADE_MS), 0);
  assert.equal(activeAgents(m).length, 0);
  expirePresence(m, T0 + 2000 + FADE_MS);
  assert.equal(m.entries.size, 0);
});

test("a new entry of the same agent glides from where the agent was", () => {
  const m = createPresenceModel();
  mergePresence(m, payload([entry("agent:a", 5, { kind: "working", state: "working", action: "working" })]), T0);
  // "done": the working entry ends, a cursor stays (elsewhere here).
  mergePresence(m, payload([entry("agent:a", 6, { action: "done", state: "writing" })]), T0 + 500);
  const cursor = m.entries.get("agent:a|cursor");
  assert.equal(cursor.prevUnit, 5);
  assert.ok(m.entries.get("agent:a|working").leaving);
});

test("done on the same node: one badge, no fade in or out", () => {
  const m = createPresenceModel();
  mergePresence(m, payload([entry("agent:a", 5, { kind: "working", state: "working", action: "working" })]), T0);
  mergePresence(m, payload([entry("agent:a", 5, { action: "done", state: "writing" })]), T0 + 500);
  const cursor = m.entries.get("agent:a|cursor");
  assert.equal(cursor.prevUnit, null);
  assert.equal(presenceAlpha(cursor, T0 + 500), 1);
  assert.deepEqual(presenceByUnit(m).get("5").map((e) => e.kind), ["cursor"]);
});

test("expire on the client when the stream stalls (server clock skew applied)", () => {
  const m = createPresenceModel();
  // The server clock is 5 s ahead of this one.
  mergePresence(m, payload([entry("agent:a", 1, { expires_at: iso(T0 + 30000) })], T0 + 5000), T0);
  assert.equal(m.skew, 5000);
  assert.equal(expirePresence(m, T0 + 24000), false);
  assert.equal(expirePresence(m, T0 + 25000), true);
  assert.ok(m.entries.get("agent:a|cursor").leaving);
  expirePresence(m, T0 + 25000 + FADE_MS);
  assert.equal(m.entries.size, 0);
});

test("stacking: several agents on one node, working first, then writing, then reading", () => {
  const m = createPresenceModel();
  mergePresence(m, payload([
    entry("agent:c", 7),
    entry("agent:b", 7, { state: "writing", action: "comment" }),
    entry("agent:a", 7, { kind: "working", state: "working", action: "working" }),
    entry("agent:d", 8),
  ]), T0);
  const byUnit = presenceByUnit(m);
  assert.deepEqual(byUnit.get("7").map((e) => e.actor), ["agent:a", "agent:b", "agent:c"]);
  assert.deepEqual(byUnit.get("8").map((e) => e.actor), ["agent:d"]);
  assert.deepEqual(presenceOnUnit(m, { id: 7 }).map((e) => e.actor), ["agent:a", "agent:b", "agent:c"]);
  assert.deepEqual(presenceOnUnit(m, { uid: "T-8" }).map((e) => e.actor), ["agent:d"]);
  assert.deepEqual(activeAgents(m).map((a) => a.label), ["a", "b", "c", "d"]);
});

test("colours: stable per agent and distinct while shown", () => {
  const m = createPresenceModel();
  const actors = Array.from({ length: PRESENCE_COLORS.length }, (_, i) => `agent:bot${i}`);
  mergePresence(m, payload(actors.map((actor, i) => entry(actor, i + 1))), T0);
  const colors = actors.map((actor) => m.entries.get(`${actor}|cursor`).color);
  assert.equal(new Set(colors).size, PRESENCE_COLORS.length, "no two agents share a colour");
  // Same colour after leaving and coming back.
  mergePresence(m, payload([]), T0 + 1000);
  expirePresence(m, T0 + 1000 + FADE_MS);
  mergePresence(m, payload([entry("agent:bot3", 9)]), T0 + 5000);
  assert.equal(m.entries.get("agent:bot3|cursor").color, colors[3]);
  assert.equal(presenceColor(m, "agent:bot3"), colors[3]);
  // The same agent gets the same colour in another tab (a fresh model).
  const other = createPresenceModel();
  mergePresence(other, payload([entry("agent:bot0", 1)]), T0);
  assert.equal(other.entries.get("agent:bot0|cursor").color, presenceColor(createPresenceModel(), "agent:bot0"));
});

test("another project's payload replaces everything", () => {
  const m = createPresenceModel();
  mergePresence(m, payload([entry("agent:a", 1)]), T0);
  mergePresence(m, { project: "prj_b", now: iso(T0), entries: [entry("agent:b", 2)] }, T0 + 10);
  assert.deepEqual([...m.entries.keys()], ["agent:b|cursor"]);
  assert.equal(m.entries.get("agent:b|cursor").prevUnit, null);
});

// ------------------------------------------------------------ overlay

function recordingContext() {
  const calls = [];
  const ctx = new Proxy({ calls }, {
    get(target, key) {
      if (key in target) return target[key];
      if (key === "measureText") return (text) => ({ width: String(text).length * 6 });
      return (...args) => { calls.push(key); return undefined; };
    },
    set(target, key, value) { target[key] = value; return true; },
  });
  return ctx;
}

function fakeDocument() {
  return {
    createElement() {
      const ctx = recordingContext();
      return { style: {}, width: 0, height: 0, className: "", setAttribute() {}, getContext: () => ctx, remove() { this.removed = true; }, ctx };
    },
  };
}

function fakeView(layout = "force", count = 2000) {
  const nodes = Array.from({ length: count }, (_, i) => ({ id: i + 1, uid: `T-${i + 1}`, x: (i % 50) * 30 - 700, y: Math.floor(i / 50) * 30 - 600, w: 160, h: 40 }));
  const touched = [];
  const spy = (name) => () => { touched.push(name); };
  const view = {
    layout, width: 1200, height: 800, zoom: 1, pan: { x: 10, y: -20 }, nodes, byId: new Map(nodes.map((n) => [n.id, n])),
    hiddenIds: new Set(), canvas: { offsetLeft: 0, offsetTop: 0, after(el) { view.overlayEl = el; } }, host: {},
    _transform() { return { x: this.width / 2 + this.pan.x, y: this.height / 2 + this.pan.y, z: this.zoom }; },
    _nodeRadius: () => 9, _dpr: () => 1,
    setData: spy("setData"), patchData: spy("patchData"), patchNode: spy("patchNode"), _draw: spy("_draw"),
    _applyLayout: spy("_applyLayout"), fit: spy("fit"), reheat: spy("reheat"), _start: spy("_start"),
    _layoutNodeGraph: spy("_layoutNodeGraph"), autoLayout: spy("autoLayout"),
  };
  return { view, touched };
}

function fakeStore() {
  const model = createPresenceModel();
  const subs = new Set();
  return {
    model: () => model, enabled: () => true,
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
    push(p, now) { mergePresence(model, p, now); for (const fn of subs) fn(model); },
  };
}

for (const layout of ["force", "mind", "node"]) {
  test(`overlay (${layout}): a presence update draws only the overlay`, () => {
    const { view, touched } = fakeView(layout);
    const before = JSON.stringify({ nodes: view.nodes.map((n) => [n.x, n.y, n.w, n.h]), zoom: view.zoom, pan: view.pan });
    const store = fakeStore();
    const frames = [];
    let wall = T0;
    const overlay = createPresenceOverlay(view, store, {
      document: fakeDocument(), raf: (fn) => { frames.push(fn); return frames.length; }, caf() {},
      now: () => wall - T0, wall: () => wall, reducedMotion: false,
    });
    const run = (n = 1) => { for (let i = 0; i < n && frames.length; i += 1) { wall += 40; frames.shift()(); } };
    run();
    // Ten agents walk over the graph.
    for (let step = 0; step < 20; step += 1) {
      store.push(payload(Array.from({ length: 10 }, (_, i) => entry(`agent:bot${i}`, 1 + ((i * 37 + step * 11) % 2000),
        { state: step % 2 ? "writing" : "reading" })), wall), wall);
      run(3);
    }
    assert.ok(overlay.stats.frames > 10, "the overlay was drawn");
    assert.ok(overlay.stats.badges > 0, "badges were drawn");
    assert.ok(overlay.canvas.ctx.calls.includes("arc"));
    assert.deepEqual(touched, [], "no graph method was called");
    assert.equal(JSON.stringify({ nodes: view.nodes.map((n) => [n.x, n.y, n.w, n.h]), zoom: view.zoom, pan: view.pan }), before,
      "positions and camera unchanged");
    // Everyone leaves: the overlay fades out and stops.
    store.push(payload([], wall), wall);
    run(40);
    expirePresence(store.model(), wall + FADE_MS);
    store.push(payload([], wall), wall);
    run(5);
    assert.equal(frames.length, 0, "no frame loop without agents");
    overlay.destroy();
    assert.ok(overlay.canvas.removed);
  });
}

test("overlay: a frame with 10 agents over 2,000 nodes is cheap", () => {
  const { view } = fakeView("force", 2000);
  const store = fakeStore();
  const overlay = createPresenceOverlay(view, store, { document: fakeDocument(), raf: () => 1, caf() {}, now: () => 0, wall: () => T0 });
  store.push(payload(Array.from({ length: 10 }, (_, i) => entry(`agent:bot${i}`, 100 + i * 150, { state: "writing" }))), T0);
  const started = performance.now();
  for (let i = 0; i < 200; i += 1) overlay.drawNow(i * 40, T0 + 400 + i);
  const perFrame = (performance.now() - started) / 200;
  assert.ok(perFrame < 2, `${perFrame.toFixed(3)} ms per overlay frame`);
});

test("centreOnUnit moves the camera only", () => {
  const { view, touched } = fakeView("force", 10);
  const animations = [];
  view._animateCamera = (zoom, pan) => animations.push({ zoom, pan });
  const node = centreOnUnit(view, 3);
  assert.equal(node.id, 3);
  assert.deepEqual(animations, [{ zoom: 1, pan: { x: -node.x, y: -node.y } }]);
  assert.deepEqual(touched, []);
  assert.equal(centreOnUnit(view, 999), null);
});

test("graph-presence.js never calls graph data, layout or fit methods", async () => {
  const source = await readFile(new URL("../../ark_studio/web/js/graph-presence.js", import.meta.url), "utf8");
  const code = source.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, "");
  for (const name of ["setData", "patchData", "patchNode", "_applyLayout", "reheat", "_start(", ".fit(", "_layoutNodeGraph", "autoLayout"]) {
    assert.ok(!code.includes(name), `graph-presence.js mentions ${name}`);
  }
});

test("overlay: badges are redrawn in the same frame as the graph (drag, pan)", () => {
  const { view, touched } = fakeView("force", 50);
  const original = view._draw;
  const store = fakeStore();
  let reads = 0;
  const canvas = view.canvas;
  view.canvas = { after: canvas.after, get offsetLeft() { reads += 1; return 0; }, get offsetTop() { return 0; } };
  const overlay = createPresenceOverlay(view, store, { document: fakeDocument(), raf: () => 1, caf() {}, now: () => 0, wall: () => T0 + 1000 });
  store.push(payload([entry("agent:a", 5, { state: "writing" })]), T0);
  const before = overlay.stats.frames;
  // A drag: the graph redraws three times; the overlay follows each time.
  view.pan.x += 30; view._draw(); view._draw(); view._draw();
  assert.equal(overlay.stats.frames, before + 3);
  assert.equal(touched.filter((n) => n === "_draw").length, 3, "the graph's own draw still runs");
  assert.ok(reads <= 1, `canvas offset read ${reads} times`);
  // Offscreen scene tiles also go through _draw: no overlay frame for them.
  view._tileRender = true; view._draw(); view._tileRender = false;
  assert.equal(overlay.stats.frames, before + 3);
  overlay.destroy();
  assert.equal(view._draw, original);
});

test("overlay (mind): an agent inside a collapsed branch shows on the nearest shown ancestor", () => {
  // Topic 1 holds 2, 2 holds 3; topic 1 is collapsed, so 2 and 3 are hidden.
  const { view } = fakeView("mind", 5);
  view.nodes.forEach((n, i) => { n.x = i * 200 - 400; n.y = 0; });
  const node1 = view.nodes[0]; node1.id = 1; view.byId.set(1, node1);
  const node2 = view.nodes[1]; node2.id = 2; view.byId.set(2, node2);
  const node3 = view.nodes[2]; node3.id = 3; view.byId.set(3, node3);
  const node5 = view.nodes[4]; node5.id = 5; view.byId.set(5, node5);
  view.hiddenIds = new Set([2, 3]);
  view.mindParentOf = new Map([[2, 1], [3, 2]]);
  const store = fakeStore();
  const overlay = createPresenceOverlay(view, store, { document: fakeDocument(), raf: () => 1, caf() {}, now: () => 0, wall: () => T0 + 1000 });
  store.push(payload([entry("agent:a", 3, { state: "writing" }), entry("agent:b", 2), entry("agent:c", 5)]), T0);
  overlay.drawNow(0, T0 + 1000);
  assert.equal(overlay.stats.badges, 3, "a and b on the collapsed topic (node 1), c on node 5");
  // Expanded: the hidden set empties and the badges go back to their nodes.
  view.hiddenIds = new Set();
  overlay.drawNow(0, T0 + 1000);
  assert.equal(overlay.stats.badges, 3);
  // Hidden with no shown ancestor (a filter in Force): no badge.
  view.layout = "force";
  view.hiddenIds = new Set([3]);
  overlay.drawNow(0, T0 + 1000);
  assert.equal(overlay.stats.badges, 2);
  overlay.destroy();
});

test("presence keeps sub-agents separate while sharing the parent colour", () => {
  const model = createPresenceModel();
  mergePresence(model, payload([
    entry("agent:alice/codex", 1, { label: "Codex", color_key: "agent:alice" }),
    entry("agent:alice/claude", 1, { label: "Claude", color_key: "agent:alice" }),
    entry("agent:bob/codex", 1, { label: "Codex", color_key: "agent:bob" }),
  ]), T0);
  const agents = activeAgents(model);
  assert.deepEqual(agents.map(({ actor }) => actor).sort(), ["agent:alice/claude", "agent:alice/codex", "agent:bob/codex"]);
  assert.equal(model.entries.get("agent:alice/codex|cursor").color, model.entries.get("agent:alice/claude|cursor").color);
  assert.notEqual(model.entries.get("agent:alice/codex|cursor").color, model.entries.get("agent:bob/codex|cursor").color);
});
