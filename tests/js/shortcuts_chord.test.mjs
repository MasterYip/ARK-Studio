// Run: node --experimental-vm-modules --test tests/js/shortcuts_chord.test.mjs
// Two-key shortcuts (Ctrl+K Ctrl+0 …) and keys matched by their physical
// key (Ctrl+Shift+[ arrives as "{"), used by the Editor's folding commands.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load(name) {
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  await module.link(() => { throw new Error(`${name} must stay dependency-free`); });
  await module.evaluate();
  return module.namespace;
}

const s = await load("shortcuts.js");
const key = (k, { ctrl = false, shift = false, alt = false, meta = false, code = "" } = {}) => ({
  key: k, code, ctrlKey: ctrl, shiftKey: shift, altKey: alt, metaKey: meta,
});

const specs = Object.fromEntries(s.FOLD_ACTIONS.map((action) => [action.id, s.DEFAULT_SHORTCUTS[action.id]]));

test("fold defaults follow VS Code", () => {
  assert.equal(specs.editorFoldAll, "Ctrl+K Ctrl+0");
  assert.equal(specs.editorUnfoldAll, "Ctrl+K Ctrl+J");
  assert.equal(specs.editorFoldLevel3, "Ctrl+K Ctrl+3");
  assert.equal(specs.editorFold, "Ctrl+Shift+[");
  assert.equal(specs.editorUnfold, "Ctrl+Shift+]");
  assert.deepEqual(s.FOLD_ACTIONS.filter((action) => action.level).map((action) => action.level), [1, 2, 3, 4, 5, 6, 7]);
  // Every fold action can be changed in Settings.
  for (const action of s.FOLD_ACTIONS) assert.ok(s.SHORTCUT_ACTIONS.some((item) => item.id === action.id), action.id);
});

test("chords: prefix, then the second key", () => {
  const first = s.matchChord(key("k", { ctrl: true, code: "KeyK" }), specs, "");
  assert.deepEqual(first, { type: "prefix", combo: "Ctrl+K" });
  assert.deepEqual(s.matchChord(key("0", { ctrl: true, code: "Digit0" }), specs, "Ctrl+K"), { type: "match", id: "editorFoldAll" });
  assert.deepEqual(s.matchChord(key("j", { ctrl: true, code: "KeyJ" }), specs, "Ctrl+K"), { type: "match", id: "editorUnfoldAll" });
  assert.deepEqual(s.matchChord(key("2", { ctrl: true, code: "Digit2" }), specs, "Ctrl+K"), { type: "match", id: "editorFoldLevel2" });
  // Without Ctrl, or an unknown second key: nothing.
  assert.deepEqual(s.matchChord(key("0", { code: "Digit0" }), specs, "Ctrl+K"), { type: "none" });
  assert.deepEqual(s.matchChord(key("x", { ctrl: true, code: "KeyX" }), specs, "Ctrl+K"), { type: "none" });
  // The second key alone is not a shortcut.
  assert.deepEqual(s.matchChord(key("0", { ctrl: true, code: "Digit0" }), specs, ""), { type: "none" });
  // A chord never matches as a single shortcut.
  assert.equal(s.shortcutMatches(key("k", { ctrl: true }), "Ctrl+K Ctrl+0"), false);
});

test("bracket keys match by physical key with Shift held", () => {
  const shifted = key("{", { ctrl: true, shift: true, code: "BracketLeft" });
  assert.deepEqual(s.matchChord(shifted, specs, ""), { type: "match", id: "editorFold" });
  assert.deepEqual(s.matchChord(key("}", { ctrl: true, shift: true, code: "BracketRight" }), specs, ""), { type: "match", id: "editorUnfold" });
  assert.equal(s.shortcutMatches(shifted, "Ctrl+Shift+["), true);
  assert.equal(s.shortcutMatches(key("[", { ctrl: true, code: "BracketLeft" }), "Ctrl+Shift+["), false);
  // Recorded the same way in Settings.
  assert.equal(s.shortcutFromEvent(shifted), "Ctrl+Shift+[");
  assert.equal(s.shortcutFromEvent(key("!", { ctrl: true, shift: true, code: "Digit1" })), "Ctrl+Shift+1");
});

test("chord specs: parts, labels and what Settings accepts", () => {
  assert.deepEqual(s.chordParts("Ctrl+K  Ctrl+0"), ["Ctrl+K", "Ctrl+0"]);
  assert.equal(s.isChord("Ctrl+K Ctrl+0"), true);
  assert.equal(s.isChord("Ctrl+S"), false);
  assert.equal(s.shortcutLabel("Ctrl+K Ctrl+0"), "Ctrl K, Ctrl 0");
  assert.equal(s.shortcutLabel("Ctrl+Shift+["), "Ctrl Shift [");
  assert.equal(s.shortcutIsUsable("Ctrl+K Ctrl+J", "editorUnfoldAll"), true);
  assert.equal(s.shortcutIsUsable("Ctrl+K 0", "editorFoldAll"), false);
  assert.equal(s.shortcutIsUsable("Ctrl+K Ctrl+1 Ctrl+2", "editorFoldAll"), false);
  assert.equal(s.shortcutIsUsable("Ctrl+Shift+[", "editorFold"), true);
  // Edit / view and full screen.
  assert.equal(s.DEFAULT_SHORTCUTS.editorEdit, "Ctrl+E");
  assert.equal(s.DEFAULT_SHORTCUTS.editorFullscreen, "F11");
  assert.equal(s.shortcutIsUsable("F11", "editorFullscreen"), true);
  assert.equal(s.shortcutIsUsable("F12", "editorFullscreen"), false, "the browser keeps F12");
  assert.equal(s.shortcutFromEvent(key("F11", { code: "F11" })), "F11");
  assert.equal(s.shortcutMatches(key("F11", { code: "F11" }), "F11"), true);
});

test("palette has its own key; an old saved Ctrl+K gives way to the fold chords", () => {
  assert.equal(s.DEFAULT_SHORTCUTS.palette, "Ctrl+P");
  const store = new Map();
  globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v), removeItem: (k) => store.delete(k) };
  try {
    store.set("ark.shortcuts.v1", JSON.stringify({ ...s.DEFAULT_SHORTCUTS, palette: "Ctrl+K", chat: "Alt+K" }));
    assert.equal(s.getShortcut("palette"), "Ctrl+P");
    assert.equal(s.getShortcut("chat"), "Alt+K");
    store.set("ark.shortcuts.v1", JSON.stringify({ palette: "Ctrl+Shift+Space" }));
    assert.equal(s.getShortcut("palette"), "Ctrl+Shift+Space");
  } finally {
    delete globalThis.localStorage;
  }
});
