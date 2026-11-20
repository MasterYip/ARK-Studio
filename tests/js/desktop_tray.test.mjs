import assert from "node:assert/strict";
import test from "node:test";
import { MAX_SERVERS, trayModel } from "../../ark_studio/desktop/ui/tray-model.js";

const t = (key, vars = {}) => `${key}${Object.keys(vars).length ? JSON.stringify(vars) : ""}`;
const ids = (model) => model.items.filter((i) => i.kind === "item").map((i) => i.id);

test("stopped local server: Start, no Stop, no update entry", () => {
  const model = trayModel({ version: "2.0.1", local: null, servers: [], update: { available: null } }, t);
  assert.deepEqual(model.head, { version: "2.0.1", running: false, status: "local.stopped" });
  assert.deepEqual(ids(model), ["open", "connect", "browser", "start", "settings", "data", "log", "quit"]);
});

test("running server, saved servers and an update", () => {
  const model = trayModel({
    version: "2.0.1",
    local: { port: 18890 },
    servers: [{ url: "https://a.example/", label: "a.example" }, { url: "https://b.example/x/", label: "b.example/x" }],
    update: { available: "2.1.0" },
  }, t);
  assert.equal(model.head.status, 'local.running{"port":18890}');
  assert.deepEqual(ids(model), [
    "open", "update", "server:https://a.example/", "server:https://b.example/x/", "connect",
    "browser", "stop", "settings", "data", "log", "quit",
  ]);
  const server = model.items.find((i) => i.id === "server:https://a.example/");
  assert.equal(server.label, "a.example");
  assert.equal(model.items.find((i) => i.id === "quit").tone, "danger");
});

test("no state (get_state failed) still offers the basic entries", () => {
  const model = trayModel(null, t);
  assert.equal(model.head.running, false);
  assert.ok(ids(model).includes("open") && ids(model).includes("quit"));
});

test("long server lists are capped", () => {
  const servers = Array.from({ length: MAX_SERVERS + 3 }, (_, i) => ({ url: `https://s${i}.example/`, label: `s${i}` }));
  const model = trayModel({ servers }, t);
  assert.equal(ids(model).filter((id) => id.startsWith("server:")).length, MAX_SERVERS);
});

test("separators never sit next to each other or at the ends", () => {
  for (const state of [null, { local: { port: 1 }, servers: [], update: { available: "9.0.0" } }]) {
    const kinds = trayModel(state, t).items.map((i) => i.kind);
    assert.notEqual(kinds[0], "sep");
    assert.notEqual(kinds.at(-1), "sep");
    kinds.forEach((k, i) => assert.ok(!(k === "sep" && kinds[i + 1] === "sep")));
  }
});
