import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { contrast, normalizeDisplay, paletteMode, paletteTokens, readable, resolvePalette } from "../../ark_studio/web/js/appearance.js";

const palettes = [
  { background: "#101222", surface: "#1c1f36", text: "#ecebff", muted: "#a5a3c4", line: "#303456", accent: "#6366f1" },
  { background: "#f6f5fb", surface: "#ffffff", text: "#24213b", muted: "#625c7b", line: "#dcd8eb", accent: "#4f46e5" },
];

test("old dark palettes only override dark mode; paired palettes switch independently", () => {
  const legacy = { enabled: true, ...palettes[0] };
  assert.equal(paletteMode(legacy), "dark");
  assert.equal(resolvePalette(legacy, "light"), null);
  assert.equal(resolvePalette(legacy, "dark"), legacy);
  const paired = { enabled: true, dark: palettes[0], light: palettes[1] };
  assert.equal(resolvePalette(paired, "light"), palettes[1]);
  assert.equal(resolvePalette(paired, "dark"), palettes[0]);
  assert.equal(resolvePalette({ ...paired, enabled: false }, "dark"), null);
});

test("each palette derives form, menu, chip and text colors from matching surfaces", () => {
  for (const palette of palettes) {
    const tokens = paletteTokens(palette);
    assert.equal(tokens["--sub"], palette.muted);
    assert.equal(tokens["--mute"], palette.muted);
    assert.equal(paletteMode({ background: tokens["--input-bg"] }), paletteMode(palette));
    assert.ok(contrast(tokens["--btn-ink"], tokens["--btn"]) >= 4.5);
    assert.ok(contrast(tokens["--accent-ink"], tokens["--accent"]) >= 4.5);
  }
});

test("high contrast reaches 10:1 for text and 3:1 for boundaries on both surfaces", () => {
  for (const palette of palettes) {
    const tokens = paletteTokens({ ...palette, muted: "#777777" }, true);
    for (const bg of [palette.background, palette.surface]) {
      for (const key of ["--ink", "--sub", "--mute"]) assert.ok(contrast(tokens[key], bg) >= 10, key);
      assert.ok(contrast(tokens["--line"], bg) >= 3);
    }
  }
  assert.equal(readable("#111111", ["#ffffff"]), "#111111");
});

test("display normalization bounds scale and does not coerce string booleans", () => {
  assert.deepEqual(normalizeDisplay(), { font_scale: 1, high_contrast: false });
  assert.equal(normalizeDisplay({ font_scale: 999 }).font_scale, 1.5);
  assert.equal(normalizeDisplay({ font_scale: -1 }).font_scale, 1);
  assert.equal(normalizeDisplay({ font_scale: "invalid" }).font_scale, 1);
  assert.equal(normalizeDisplay({ high_contrast: "false" }).high_contrast, false);
});

test("text scaling uses rem without scaling graph geometry or fixed SVG lockups", () => {
  const css = readFileSync(new URL("../../ark_studio/web/css/app.css", import.meta.url), "utf8");
  assert.match(css, /font-size:calc\(16px \* var\(--font-scale,1\)\)/);
  assert.doesNotMatch(css, /font-size:\s*\d+(?:\.\d+)?px/);
  assert.doesNotMatch(css, /\bzoom:\s*var\(--font/);
});
