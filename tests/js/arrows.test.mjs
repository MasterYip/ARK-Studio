// Run: node --experimental-vm-modules --test tests/js/arrows.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";
import {
  ARROW_HEADS, ARROW_PLACEMENTS, defaultArrowSettings, normalizeArrowSettings,
  normalizePersonalArrows, resolveArrowStyle, arrowResolver,
  relationReversed, displayReversed, REVERSED_BY_DEFAULT,
} from "../../web/js/arrows.js";
import { resolveAppearance } from "../../web/js/appearance-overrides.js";
import { loadGraphCore } from "./load_graph_core.mjs";

test("the model matches arkstudio/settings.py", async () => {
  const py = await readFile(new URL("../../arkstudio/settings.py", import.meta.url), "utf8");
  const tuple = (name) => [...py.match(new RegExp(`${name} = \\(([^)]*)\\)`))[1].matchAll(/"([a-z_]+)"/g)].map((m) => m[1]);
  assert.deepEqual(ARROW_HEADS, tuple("ARROW_HEADS"));
  assert.deepEqual(ARROW_PLACEMENTS, tuple("ARROW_PLACEMENTS"));
});

test("defaults: filled triangle at the target, symmetric relations without arrows", () => {
  const settings = defaultArrowSettings();
  assert.deepEqual(resolveArrowStyle(settings, "part_of"), { head: "triangle", start: false, end: true, size: 1 });
  assert.equal(resolveArrowStyle(settings, "relates_to"), null);
  assert.equal(resolveArrowStyle(settings, "unset"), null);
  assert.deepEqual(normalizeArrowSettings(null), settings);
  assert.deepEqual(resolveArrowStyle(null, "supports"), resolveArrowStyle(settings, "supports"));
});

test("per-relation overrides layer over the project default", () => {
  const settings = {
    style: { head: "open", placement: "both", size: 1.5 },
    relations: { depends_on: { head: "diamond" }, part_of: { placement: "none" }, cites: { size: 0.75, placement: "source" } },
  };
  assert.deepEqual(resolveArrowStyle(settings, "supports"), { head: "open", start: true, end: true, size: 1.5 });
  assert.deepEqual(resolveArrowStyle(settings, "depends_on"), { head: "diamond", start: true, end: true, size: 1.5 });
  assert.equal(resolveArrowStyle(settings, "part_of"), null);
  assert.deepEqual(resolveArrowStyle(settings, "cites"), { head: "open", start: true, end: false, size: 0.75 });
  // An explicit relation table replaces the built-in one: relates_to now has arrows.
  assert.ok(resolveArrowStyle(settings, "relates_to"));
  assert.equal(resolveArrowStyle({ style: { head: "none", placement: "target", size: 1 } }, "supports"), null);
});

test("invalid values fall back instead of breaking the renderer", () => {
  const value = normalizeArrowSettings({
    style: { head: "spiral", placement: "middle", size: 9 },
    relations: { supports: { head: "bar", size: "x" }, blocks: "nope", uses: {} },
    mind_tree: "yes",
  });
  assert.deepEqual(value, { style: { head: "triangle", placement: "target", size: 1 }, relations: { supports: { head: "bar" } }, mind_tree: false });
});

test("personal overrides need a complete style and then win", () => {
  assert.equal(normalizePersonalArrows({ style: { head: "bar" } }), null);
  assert.equal(normalizePersonalArrows("x"), null);
  const mine = { style: { head: "bar", placement: "source", size: 2 }, mind_tree: true };
  const project = { arrows: { style: { head: "diamond", placement: "target", size: 1 }, relations: {} } };
  const effective = resolveAppearance(project, { arrows: mine });
  assert.equal(effective.source.arrows, "personal");
  const resolve = arrowResolver(effective.arrows);
  assert.deepEqual(resolve("supports"), { head: "bar", start: true, end: false, size: 2 });
  assert.equal(resolve.mindTree, true);
  // Personal "no arrow" beats a project arrow.
  const none = resolveAppearance(project, { arrows: { style: { head: "triangle", placement: "none", size: 1 } } });
  assert.equal(arrowResolver(none.arrows)("supports"), null);
  assert.equal(arrowResolver(resolveAppearance(project, {}).arrows)("supports").head, "diamond");
});

test("the resolver memoizes per settings object", () => {
  const settings = normalizeArrowSettings({ style: { head: "stealth", placement: "target", size: 1 } });
  const resolve = arrowResolver(settings);
  assert.equal(arrowResolver(settings), resolve);
  assert.equal(resolve("supports"), resolve("supports"));
  assert.notEqual(arrowResolver(normalizeArrowSettings(settings)), resolve);
});

function recorder() {
  const ops = [];
  return {
    ops,
    moveTo: (x, y) => ops.push(["M", x, y]),
    lineTo: (x, y) => ops.push(["L", x, y]),
    arc: (x, y, r) => ops.push(["A", x, y, r]),
    closePath: () => ops.push(["Z"]),
  };
}

test("arrowhead geometry per type: tip at the node, body behind it", async () => {
  const core = (await loadGraphCore(vm.createContext({}))).namespace;
  const { addArrowhead, arrowInset, ARROW_PAINT } = core;
  const size = 10;
  const near = (a, b) => Math.abs(a - b) < 1e-9;
  for (const head of ARROW_HEADS.filter((id) => id !== "none")) {
    const path = recorder();
    // Travelling right (+x) into a tip at (100, 50).
    addArrowhead(path, 100, 50, 1, 0, size, head);
    const points = path.ops.filter((op) => op[0] !== "Z");
    assert.ok(points.length >= 2, head);
    for (const [kind, x, y, r] of points) {
      const reach = kind === "A" ? x + r : x;
      assert.ok(reach <= 100 + 1e-9 && x >= 100 - size * 1.2 - 1e-9, `${head} stays behind the tip`);
      assert.ok(Math.abs(y - 50) <= size * 0.6, `${head} stays near the line`);
    }
    // Every head touches the tip point (the node rim).
    const touches = points.some(([kind, x, y, r]) => (kind === "A" ? near(x + r, 100) : near(x, 100)) && (kind === "A" || head === "bar" || near(y, 50)));
    assert.ok(touches, `${head} reaches the tip`);
    assert.ok(arrowInset(head, size) >= 0 && arrowInset(head, size) <= size * 1.2);
    assert.ok(["fill", "stroke", "hollow"].includes(ARROW_PAINT[head]), head);
  }
  // Shapes differ: stealth has a notch, diamonds reach 1.2 × size, bar is a tee at the tip.
  const shape = (head) => { const p = recorder(); addArrowhead(p, 0, 0, 1, 0, size, head); return p.ops; };
  assert.deepEqual(shape("stealth")[2], ["L", -6, 0]);
  assert.deepEqual(shape("diamond")[2], ["L", -12, 0]);
  assert.deepEqual(shape("bar").map((op) => op[1]), [0, 0]);
  assert.equal(shape("open").filter((op) => op[0] === "Z").length, 0, "open arrows are not closed");
  // Direction is respected: travelling up puts the body below the tip.
  const up = recorder();
  addArrowhead(up, 0, 0, 0, -1, size, "triangle");
  assert.ok(up.ops.slice(1, 3).every(([, , y]) => near(y, size)));
});

test("size scale multiplies the zoom-clamped default", async () => {
  const { arrowSize } = (await loadGraphCore(vm.createContext({}))).namespace;
  assert.equal(arrowSize(1, 1, 1), arrowSize(1, 1));
  assert.equal(arrowSize(1, 1, 2), arrowSize(1, 1) * 2);
  assert.equal(arrowSize(1, 0.05, 0.5), 3);
});

test("paint modes: hollow fills with the background then outlines", async () => {
  const { paintArrowheads } = (await loadGraphCore(vm.createContext({}))).namespace;
  const calls = [];
  const ctx = new Proxy({}, {
    get: (target, key) => (key in target ? target[key] : (...args) => calls.push([key, ...args])),
    set: (target, key, value) => { calls.push([`=${String(key)}`, value]); return true; },
  });
  paintArrowheads(ctx, "P", "diamond", "#123456", "#ffffff");
  const names = calls.map(([name]) => name);
  assert.ok(names.indexOf("=fillStyle") < names.indexOf("fill") && names.indexOf("fill") < names.indexOf("stroke"));
  assert.deepEqual(calls.find(([name]) => name === "=fillStyle"), ["=fillStyle", "#ffffff"]);
  calls.length = 0;
  paintArrowheads(ctx, "P", "triangle", "#123456", "#ffffff");
  assert.deepEqual(calls.map(([name]) => name), ["=fillStyle", "fill"]);
});

test("display-only reversal: built-in defaults, project/personal settings and quick toggles", async () => {
  const py = await readFile(new URL("../../arkstudio/settings.py", import.meta.url), "utf8");
  const tuple = [...py.match(/ARROW_REVERSED_BY_DEFAULT = \(([^)]*)\)/)[1].matchAll(/"([a-z_]+)"/g)].map((m) => m[1]);
  assert.deepEqual([...REVERSED_BY_DEFAULT].sort(), tuple.sort());
  assert.equal(relationReversed(null, "derived_from"), true);
  assert.equal(relationReversed(null, "depends_on"), true);
  assert.equal(relationReversed(null, "part_of"), false);
  const project = normalizeArrowSettings({ relations: { derived_from: { reverse: false }, cites: { reverse: true } } });
  assert.equal(relationReversed(project, "derived_from"), false);
  assert.equal(relationReversed(project, "cites"), true);
  assert.equal(relationReversed(project, "depends_on"), true, "no entry keeps the default");
  // The top-level style never carries reverse; garbage is ignored.
  assert.equal(normalizeArrowSettings({ style: { reverse: true }, relations: { uses: { reverse: "yes" } } }).style.reverse, undefined);
  assert.equal(relationReversed(normalizeArrowSettings({ relations: { uses: { reverse: "yes" } } }), "uses"), false);
  const personal = normalizePersonalArrows({ style: { head: "open", placement: "target", size: 1 }, relations: { supports: { reverse: true } } });
  assert.equal(relationReversed(personal, "supports"), true);
  assert.equal(arrowResolver(personal).reversed("supports"), true);
  // Quick toggle wins over the settings, both ways.
  assert.equal(displayReversed(project, "cites", { cites: false }), false);
  assert.equal(displayReversed(project, "derived_from", { derived_from: true }), true);
  assert.equal(displayReversed(project, "derived_from", { derived_from: "x" }), false);
});
