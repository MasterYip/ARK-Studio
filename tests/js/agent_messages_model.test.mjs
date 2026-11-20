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
