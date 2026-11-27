// Which gate the web app shows, and the gate's styling on wide screens.
//   node --test tests/js/pair_gate.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { pairGateKind, pairingReachable } from "../../ark_studio/web/js/mobile-core.js";

const here = path.dirname(fileURLToPath(import.meta.url));
const web = path.join(here, "..", "..", "ark_studio", "web");

test("the desktop window refused through a proxy is not asked for a phone code", () => {
  // 2026-11-27, Linux desktop with a proxy on: /api/mobile/session said
  // required and the window showed the phone pairing page.
  const proxied = { required: true, authenticated: false, token_present: false, reason: "not_local",
    connection: { mode: "loopback" } };
  assert.equal(pairGateKind(proxied), "not_local");
  assert.equal(pairingReachable(proxied), false, "no code helps on a loopback-only server");
  assert.equal(pairGateKind({ required: false, authenticated: false }), "");
  assert.equal(pairGateKind(null), "");
});

test("phones still get the pairing page", () => {
  assert.equal(pairGateKind({ required: true, authenticated: false, reason: "" }), "pair");
  assert.equal(pairGateKind({ required: true, authenticated: false, reason: "invalid_or_expired" }), "pair");
  assert.equal(pairGateKind({ required: true, authenticated: true, reason: "" }), "");
  assert.equal(pairingReachable({ connection: { mode: "lan" } }), true);
  assert.equal(pairingReachable({ connection: { mode: "public" } }), true);
});

// Splits CSS into top-level chunks, keeping each @media block whole.
function topLevelBlocks(css) {
  const out = [];
  let depth = 0, start = 0;
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) { out.push(css.slice(start, i + 1).trim()); start = i + 1; }
  }
  return out;
}

test("the pairing card is styled on wide screens, not only on phones", () => {
  // The card's brand, eyebrow and facts were styled only inside the phone
  // media query: in the desktop window the logo and the icons stood on
  // lines of their own.
  const css = fs.readFileSync(path.join(web, "css", "mobile.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const wide = topLevelBlocks(css).filter((b) => /^@media\s*\(min-width:\s*761px\)/.test(b)).join("\n");
  for (const sel of [".pair-gate-wrap", ".pair-gate-card", ".pair-gate-brand", ".pair-gate-eyebrow",
    ".pair-gate-lead", ".pair-gate-status", ".pair-gate-hint", ".pair-gate-facts", ".pair-gate-facts span", ".pair-gate-foot"]) {
    const esc = sel.split(".").join(String.raw`\.`);
    assert.ok(new RegExp(String.raw`(^|[\s,}])` + esc + String.raw`\s*\{`).test(wide), `${sel} on wide screens`);
  }
  assert.match(wide, /\.pair-gate-brand\s*\{[^}]*display:\s*flex/);
  assert.match(wide, /\.pair-gate-eyebrow\s*\{[^}]*display:\s*flex/);
  // No unscoped pair-gate rule after the phone block overrides it again.
  const unscoped = topLevelBlocks(css).filter((b) => !b.startsWith("@") && /\.pair-gate-(wrap|card|form|input|submit)\b/.test(b.split("{")[0]));
  assert.deepEqual(unscoped, []);
});

test("both languages name the out-of-range reason", async () => {
  for (const file of ["i18n-en.js", "i18n-zh.js"]) {
    const text = fs.readFileSync(path.join(web, "js", file), "utf8");
    for (const key of ["out_eyebrow", "out_title", "out_lead", "out_hint", "out_retry", "out_pair"]) {
      assert.match(text, new RegExp(String.raw`"mobile\.gate\.` + key + `":`), `${file} ${key}`);
    }
    assert.match(text, /127\.0\.0\.1/);
  }
});
