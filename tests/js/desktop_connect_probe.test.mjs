import assert from "node:assert/strict";
import test from "node:test";
import { unreachableMessage } from "../../ark_studio/desktop/ui/connect-model.js";

const t = (key, vars = {}) => `${key}${Object.keys(vars).length ? JSON.stringify(vars) : ""}`;
const url = "https://ark.example.com:8443/";

test("each reason has its own message", () => {
  for (const reason of ["certificate", "dns", "refused", "timeout"]) {
    const text = unreachableMessage({ url, reason, error: "x" }, "", t);
    assert.equal(text, `remote.unreachable.${reason}${JSON.stringify({ url, proxy: "", detail: "x" })}`);
  }
});

test("proxy failure names the proxy", () => {
  const text = unreachableMessage({ url, reason: "proxy", proxy: "127.0.0.1:7890", error: "tls handshake eof" }, "", t);
  assert.match(text, /^remote\.unreachable\.proxy/);
  assert.match(text, /127\.0\.0\.1:7890/);
});

test("other failures keep the general message, with details when known", () => {
  assert.equal(unreachableMessage({ url, reason: "network" }, "", t), `remote.unreachable${JSON.stringify({ url })}`);
  assert.equal(
    unreachableMessage({ url, reason: "network", error: "boom" }, "", t),
    `remote.unreachable${JSON.stringify({ url })} remote.detail${JSON.stringify({ detail: "boom" })}`,
  );
  assert.equal(unreachableMessage({ reason: "proxy" }, url, t), `remote.unreachable${JSON.stringify({ url })}`);
  assert.equal(unreachableMessage(null, url, t), `remote.unreachable${JSON.stringify({ url })}`);
});
