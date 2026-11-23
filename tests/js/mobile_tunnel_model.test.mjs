// Run: node --experimental-vm-modules --test tests/js/mobile_tunnel_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/mobile-tunnel-model.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "mobile-tunnel-model.js" });
await module.link(() => { throw new Error("mobile-tunnel-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;

test("tunnel state labels and polling", () => {
  assert.deepEqual({ ...m.tunnelStateView({ state: "running" }) }, { key: "settings.tunnel.state_running", tone: "ok" });
  assert.equal(m.tunnelStateView({ state: "error" }).tone, "bad");
  assert.equal(m.tunnelStateView({ state: "weird" }).key, "settings.tunnel.state_off");
  assert.equal(m.tunnelStateView(null).key, "settings.tunnel.state_off");
  assert.equal(m.tunnelBusy({ enabled: true, state: "downloading" }), true);
  assert.equal(m.tunnelBusy({ enabled: true, state: "running" }), false);
  assert.equal(m.tunnelBusy({ enabled: false, state: "starting" }), false);
});

test("re-link notice only after the address changed", () => {
  const base = { state: "running", url: "https://b.trycloudflare.com/", relink_code: "b-c-d-e", relink_qr_svg: "<svg/>" };
  assert.equal(m.relinkNotice(base), null);
  assert.equal(m.relinkNotice({ ...base, previous_url: base.url }), null);
  assert.equal(m.relinkNotice({ ...base, previous_url: "https://a.trycloudflare.com/", state: "starting" }), null);
  const n = m.relinkNotice({ ...base, previous_url: "https://a.trycloudflare.com/" });
  assert.equal(n.code, "b-c-d-e");
  assert.equal(n.previous, "https://a.trycloudflare.com/");
});

test("re-link input: short code or address", () => {
  assert.equal(m.relinkBaseFromInput(" Able-Baker-Charlie-Delta "), "https://able-baker-charlie-delta.trycloudflare.com/");
  assert.equal(m.relinkBaseFromInput("https://x-y.trycloudflare.com/#relink=abc"), "https://x-y.trycloudflare.com/");
  assert.equal(m.relinkBaseFromInput("ark.example.com"), "https://ark.example.com/");
  assert.equal(m.relinkBaseFromInput("http://insecure.example.com"), "");
  assert.equal(m.relinkBaseFromInput("nodots"), "");
  assert.equal(m.relinkBaseFromInput(""), "");
});

test("awaitConfirmedClaim polls until the computer confirms", async () => {
  const first = { pending: true, claim_id: "c1", claim_secret: "s1", confirm_code: "123456", expires_in: 180, poll_after: 2 };
  const replies = [{ pending: true, confirm_code: "123456", expires_in: 170 }, { mobile_token: "arkm_x" }];
  const posts = [];
  const shown = [];
  const out = await m.awaitConfirmedClaim(first, "arkp_code", {
    post: async (path, body) => { posts.push([path, body]); return replies.shift(); },
    onPending: (code, left) => shown.push([code, left]),
    sleep: async () => {},
  });
  assert.equal(out.mobile_token, "arkm_x");
  assert.equal(posts.length, 2);
  assert.deepEqual(posts[0], ["api/mobile/pair/claim/status", { code: "arkp_code", claim_id: "c1", claim_secret: "s1" }]);
  assert.deepEqual(shown[0], ["123456", 180]);
});

test("awaitConfirmedClaim stops when cancelled and passes errors on", async () => {
  const first = { pending: true, claim_id: "c1", claim_secret: "s1", confirm_code: "1", expires_in: 180 };
  const sent = [];
  await assert.rejects(m.awaitConfirmedClaim(first, "c", {
    post: async (path, body) => { sent.push(body); return { pending: true }; }, sleep: async () => {}, isCancelled: () => true,
  }), (err) => err.cancelled === true);
  assert.deepEqual(sent, [{ claim_id: "c1", claim_secret: "s1", cancel: true }]);
  await assert.rejects(m.awaitConfirmedClaim(first, "c", {
    post: async () => { throw Object.assign(new Error("declined"), { code: "PAIRING_REJECTED" }); }, sleep: async () => {},
  }), (err) => err.code === "PAIRING_REJECTED");
  // A server without confirmation answers with the token at once.
  assert.deepEqual(await m.awaitConfirmedClaim({ mobile_token: "t" }, "c", { post: async () => null }), { mobile_token: "t" });
});
