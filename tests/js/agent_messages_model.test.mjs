// Experimental "Message agent" helpers (web/js/agent-messages-model.js).
import test from "node:test";
import assert from "node:assert/strict";

import * as M from "../../ark_studio/web/js/agent-messages-model.js";

const store = (value) => ({ getItem: (key) => (key === M.AGENT_MESSAGES_KEY ? value : null) });

test("the switch is off unless set", () => {
  assert.equal(M.messagesEnabledIn(store(null)), false);
  assert.equal(M.messagesEnabledIn(store("0")), false);
  assert.equal(M.messagesEnabledIn(store("1")), true);
  assert.equal(M.messagesEnabledIn({ getItem: () => { throw new Error("blocked"); } }), false);
});

test("targets: mine, running sessions of this project (linked first), live tokens", () => {
  const targets = M.messageTargets({
    projectId: "prj_1",
    unitUid: "T-0002",
    tokens: [{ id: "tk1", label: "laptop" }, { id: "tk2", label: "old", expired: true }, { id: "tk3", prefix: "arka_ab" }],
    sessions: [
      { id: "s1", title: "Sweep", status: "running", project: { id: "prj_1" }, capabilities: { steer_channel: "stdin" } },
      { id: "s2", title: "Done", status: "exited", project: { id: "prj_1" } },
      { id: "s3", title: "Other project", status: "running", project: { id: "prj_9" } },
      { id: "s4", title: "Ablation", status: "starting", project: { id: "prj_1" }, linked_units: [{ uid: "T-0002" }] },
    ],
  });
  assert.deepEqual(targets.map((x) => x.value), ["mine", "session:s4", "session:s1", "token:tk1", "token:tk3"]);
  assert.equal(targets[2].channel, "stdin");
  assert.equal(targets[1].channel, "next_turn");
  assert.equal(targets[4].label, "arka_ab");
  assert.equal(M.defaultTarget(targets, "T-0002"), "session:s4");
  assert.equal(M.defaultTarget(targets, ""), "mine");
  assert.equal(M.defaultTarget(M.messageTargets({}), "T-0002"), "mine");
});

test("helpers", () => {
  assert.equal(M.sessionOf("session:ark-ses-1"), "ark-ses-1");
  assert.equal(M.sessionOf("token:x"), "");
  assert.equal(M.messageStatus("answered"), "answered");
  assert.equal(M.messageStatus("weird"), "queued");
  assert.equal(M.messageProblem("  "), "agent_msg.empty");
  assert.equal(M.messageProblem("x".repeat(M.MESSAGE_MAX + 1)), "agent_msg.too_long");
  assert.equal(M.messageProblem("Pause the sweep"), "");
});

test("right-click targets: a session, an agent through a token, a host", () => {
  // Running sessions take a steer; idle or ended ones are resumed with the message.
  assert.deepEqual(M.rowTarget("agent", { kind: "session", id: "s1", title: "Sweep", state: "running" }),
    { value: "session:s1", kind: "session", label: "Sweep", channel: "next_turn", resume: false });
  assert.equal(M.rowTarget("agent", { kind: "session", id: "s2", title: "Done", state: "idle" }).resume, true);
  assert.equal(M.rowTarget("agent", { kind: "session", id: "s3", state: "ended" }).label, "s3");
  assert.deepEqual(M.rowTarget("agent", { kind: "external", id: "agent:alice/claude", title: "alice/claude" }),
    { value: "actor:agent:alice/claude", kind: "actor", label: "alice/claude" });
  assert.deepEqual(M.rowTarget("host", { kind: "host", label: "Laptop", deviceId: "c59c4a65af13" }),
    { value: "host:c59c4a65af13", kind: "host", label: "Laptop" });
  // Rows without an address get no menu entry.
  assert.equal(M.rowTarget("agent", { kind: "node", id: "A-0001" }), null);
  assert.equal(M.rowTarget("agent", { kind: "external", id: "human:bob" }), null);
  assert.equal(M.rowTarget("host", { kind: "gpu-server", label: "4090", deviceId: "" }), null);
  assert.equal(M.rowTarget("host", { kind: "host", deviceId: "bad id!" }), null);
  assert.equal(M.rowTarget("agent", null), null);
});

test("a preset target is selectable and keeps the listed session's channel", () => {
  const base = M.messageTargets({ sessions: [
    { id: "s1", title: "Sweep", status: "running", capabilities: { steer_channel: "stdin" } }] });
  const running = M.withPreset(base, M.rowTarget("agent", { kind: "session", id: "s1", title: "Sweep", state: "running" }));
  assert.equal(running.length, base.length);
  assert.equal(running.find((x) => x.value === "session:s1").channel, "stdin");
  const host = M.withPreset(base, { value: "host:abc123", kind: "host", label: "Laptop" });
  assert.deepEqual(host.map((x) => x.value), ["mine", "host:abc123", "session:s1"]);
  assert.equal(M.withPreset(base, null), base);
});
