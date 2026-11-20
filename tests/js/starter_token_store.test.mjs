// Run: node --experimental-vm-modules --test tests/js/starter_token_store.test.mjs
// Copy prompt reuses the starter token this browser minted (no request, no
// dialog, no rotation); sign-out, a revoke in Settings and a token the
// server no longer lists forget it.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  clearStarterTokens, forgetStarterTokenId, loadStarterToken, saveStarterToken, starterTokenKey, starterTokenListed,
} from "../../ark_studio/web/js/starter-token-store.js";

function memoryStorage() {
  const data = new Map();
  return {
    get length() { return data.size; },
    key: (i) => [...data.keys()][i] ?? null,
    getItem: (k) => (data.has(k) ? data.get(k) : null),
    setItem: (k, v) => data.set(k, String(v)),
    removeItem: (k) => data.delete(k),
    data,
  };
}

const NOW = Date.parse("2026-11-18T12:00:00Z");

test("a stored token is kept per account and project and expires locally", () => {
  const store = memoryStorage();
  saveStarterToken(store, "ann", "prj_a", { token: "arka_1", id: "tok_1", expires_at: "2027-02-16T12:00:00+00:00" });
  saveStarterToken(store, "bob", "prj_a", { token: "arka_2", id: "tok_2", expires_at: null });
  assert.equal(loadStarterToken(store, "ann", "prj_a", NOW).token, "arka_1");
  assert.equal(loadStarterToken(store, "ann", "prj_b", NOW), null);
  assert.equal(loadStarterToken(store, "bob", "prj_a", NOW).token, "arka_2"); // never expires
  assert.equal(loadStarterToken(store, "ann", "prj_a", Date.parse("2027-02-16T12:00:01Z")), null);
  // Nothing is stored without a value and an id; unreadable entries count as none.
  saveStarterToken(store, "ann", "prj_c", { token: "arka_3" });
  assert.equal(loadStarterToken(store, "ann", "prj_c", NOW), null);
  store.setItem(starterTokenKey("ann", "prj_d"), "{not json");
  assert.equal(loadStarterToken(store, "ann", "prj_d", NOW), null);
});

test("sign-out, a revoke in Settings and an unlisted token forget it", () => {
  const store = memoryStorage();
  store.setItem("ark.other", "keep");
  saveStarterToken(store, "ann", "prj_a", { token: "arka_1", id: "tok_1" });
  saveStarterToken(store, "ann", "prj_b", { token: "arka_2", id: "tok_2" });
  saveStarterToken(store, "bob", "prj_a", { token: "arka_3", id: "tok_3" });
  forgetStarterTokenId(store, "tok_2");
  assert.equal(loadStarterToken(store, "ann", "prj_b", NOW), null);
  assert.ok(loadStarterToken(store, "ann", "prj_a", NOW));
  clearStarterTokens(store, "ann");
  assert.equal(loadStarterToken(store, "ann", "prj_a", NOW), null);
  assert.ok(loadStarterToken(store, "bob", "prj_a", NOW));
  clearStarterTokens(store);
  assert.equal(store.data.size, 1);
  assert.equal(store.getItem("ark.other"), "keep");

  assert.equal(starterTokenListed([{ id: "tok_1", expired: false }], "tok_1"), true);
  assert.equal(starterTokenListed([{ id: "tok_1", expired: true }], "tok_1"), false);
  assert.equal(starterTokenListed([{ id: "tok_9" }], "tok_1"), false); // revoked or rotated away
  assert.equal(starterTokenListed(null, "tok_1"), true); // list unknown: keep it
});

test("the Copy flow copies a stored token inside the click and mints only without one", () => {
  const src = readFileSync(new URL("../../ark_studio/web/js/views/agent-tokens.js", import.meta.url), "utf8");
  const flow = src.slice(src.indexOf("export function copyPromptWithToken"));
  const reuse = flow.slice(0, flow.indexOf("tokenFlow = new Promise"));
  // Reuse path: text built and copyText called before any await or request.
  assert.match(reuse, /loadStarterToken\(store, user, projectId\)/);
  assert.ok(reuse.indexOf("copyText(text)") > 0);
  assert.doesNotMatch(reuse.slice(0, reuse.indexOf("copyText(text)")), /await|api\.|openModal/);
  // The list check runs after the copy; rotation and legacy revokes stay in the mint path.
  assert.ok(reuse.indexOf("checkKeptToken") > reuse.indexOf("copyText(text)"));
  assert.doesNotMatch(reuse, /createAgentToken|revokeAgentToken/);
  const mint = flow.slice(flow.indexOf("async function openTokenFlow"));
  assert.match(mint, /purpose: "starter"/);
  assert.match(mint, /saveStarterToken\(store, user, projectId/);
  // Settings revoke and sign-out forget stored tokens.
  assert.match(src, /revokeAgentToken\(item\.id\);\s*const store = storage\(\);\s*if \(store\) forgetStarterTokenId\(store, item\.id\)/);
  const api = readFileSync(new URL("../../ark_studio/web/js/api.js", import.meta.url), "utf8");
  assert.match(api.slice(api.indexOf("export async function clearClientData")), /clearStarterTokens\(localStorage\)/);
  // Home passes the signed-in account and refreshes the connection line only after a copy.
  const onboarding = readFileSync(new URL("../../ark_studio/web/js/views/onboarding.js", import.meta.url), "utf8");
  assert.match(onboarding, /user: ctx\.S\.session\?\.user/);
  assert.match(onboarding, /result\?\.copied\) connection\.refresh\(\)/);
});
