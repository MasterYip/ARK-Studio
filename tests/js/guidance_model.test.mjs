import assert from "node:assert/strict";
import { test } from "node:test";
import { GUIDANCE_STEPS, guidanceKey, guidanceState, readGuidance, saveGuidance } from "../../web/js/guidance-model.js";

test("resume survives a reload without leaking progress between accounts", () => {
  const data = new Map();
  const storage = { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => data.set(k, v) };
  const first = guidanceKey("Alice"), second = guidanceKey("Bob");
  saveGuidance(storage, first, { status: "paused", step: 6 });
  assert.deepEqual(readGuidance(storage, first), { status: "paused", step: 6 });
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

test("under 20 steps cover all workspaces, graph modes and filtering with just two exercises", () => {
  assert.ok(GUIDANCE_STEPS.length < 20);
  assert.deepEqual([...new Set(GUIDANCE_STEPS.filter((step) => step.workspace).map((step) => step.workspace))],
    ["home", "research", "map", "execute", "output", "editor", "assistant", "settings"]);
  assert.equal(new Set(GUIDANCE_STEPS.map((step) => step.id)).size, GUIDANCE_STEPS.length);
  assert.deepEqual(GUIDANCE_STEPS.filter((step) => step.practice).map((step) => step.id),
    ["edit", "filter"]);
  assert.deepEqual(GUIDANCE_STEPS.filter((step) => step.id === step.layout).map((step) => step.id),
    ["force", "tree", "mind", "node"]);
  assert.ok(GUIDANCE_STEPS.some((step) => step.id === "relations"));
  assert.equal(GUIDANCE_STEPS.find((step) => step.id === "prompts").target, ".start-panel");
  assert.equal(GUIDANCE_STEPS.find((step) => step.id === "mobile").settingsTab, "access");
});
