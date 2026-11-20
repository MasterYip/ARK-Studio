import assert from "node:assert/strict";
import { test } from "node:test";
import { GUIDANCE_STEPS, guidanceFinish, guidanceKey, guidanceState, readGuidance, saveGuidance } from "../../ark_studio/web/js/guidance-model.js";

test("resume survives a reload without leaking progress between accounts", () => {
  const data = new Map();
  const storage = { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
  const first = guidanceKey("Alice"), second = guidanceKey("Bob");
  saveGuidance(storage, first, { status: "paused", step: 3 });
  assert.deepEqual(readGuidance(storage, first), { status: "paused", step: 3 });
  assert.deepEqual(readGuidance(storage, second), { status: "new", step: 0 });
  saveGuidance(storage, first, { status: "completed", step: GUIDANCE_STEPS.length - 1 });
  assert.equal(readGuidance(storage, first).status, "completed");
});

test("corrupt or unavailable storage leaves a usable guide", () => {
  assert.deepEqual(readGuidance({ getItem: () => "invalid JSON" }, "k"), { status: "new", step: 0 });
  assert.deepEqual(readGuidance(null, "k"), { status: "new", step: 0 });
  assert.deepEqual(saveGuidance(null, "k", { status: "paused", step: 4 }), { status: "paused", step: 4 });
  for (const input of [null, [], { status: "broken" }, "completed"]) {
    assert.deepEqual(guidanceState(input), { status: "new", step: 0 });
  }
  assert.equal(guidanceState({ status: "paused", step: -100 }).step, 0);
  assert.equal(guidanceState({ status: "paused", step: 999 }).step, GUIDANCE_STEPS.length - 1);
  assert.equal(guidanceState({ status: "paused", step: 1.5 }).step, 0);
});

test("five steps walk the core loop: capture, plan, evidence, execute, review", () => {
  assert.deepEqual(GUIDANCE_STEPS.map((step) => step.id), ["capture", "plan", "evidence", "execute", "review"]);
  assert.deepEqual(GUIDANCE_STEPS.map((step) => step.workspace), ["home", "research", "map", "execute", "home"]);
  // The agent card and Needs you are framed on Home.
  assert.equal(GUIDANCE_STEPS[0].target, ".start-panel");
  assert.equal(GUIDANCE_STEPS[4].target, ".needs-panel");
  // No sandbox exercises or settings detours any more.
  assert.ok(GUIDANCE_STEPS.every((step) => !step.practice && !step.surface && !step.settingsTab));
});

test("the last step opens the tutorial graph in the tutorial project only", () => {
  assert.equal(guidanceFinish({ id: "ark-demo", demo: true }), "tutorial-topic");
  assert.equal(guidanceFinish({ id: "prj_123", demo: true }), "done");
  assert.equal(guidanceFinish({ id: "prj_456" }), "done");
  assert.equal(guidanceFinish(null), "done");
});
