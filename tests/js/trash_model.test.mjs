import assert from "node:assert/strict";
import test from "node:test";

import { trashActor, withoutUnits, canEditTrash } from "../../ark_studio/web/js/trash-model.js";

test("the recycle bin names who deleted a node, not the local owner", () => {
  assert.equal(trashActor("human:alice"), "alice");
  assert.equal(trashActor("agent:ana/claude"), "claude");
  assert.equal(trashActor("agent:codex"), "codex");
  for (const actor of ["human:ui", "human:local", "agent:api", "task:ark-ses-1", "", null, undefined]) {
    assert.equal(trashActor(actor), "", String(actor));
  }
});

test("restored and permanently deleted nodes leave the list", () => {
  const units = [{ uid: "I-0001" }, { uid: "N-0002" }, { uid: "T-0003" }];
  assert.deepEqual(withoutUnits(units, "N-0002").map((u) => u.uid), ["I-0001", "T-0003"]);
  assert.deepEqual(withoutUnits(units, ["I-0001", "T-0003"]).map((u) => u.uid), ["N-0002"]);
  assert.deepEqual(withoutUnits(null, "I-0001"), []);
});

test("read-only members see the bin without restore or delete", () => {
  assert.equal(canEditTrash({ project_permission: "read" }), false);
  assert.equal(canEditTrash({ project_permission: "write" }), true);
  assert.equal(canEditTrash({ project_permission: "admin" }), true);
  assert.equal(canEditTrash({ local: true }), true);
  assert.equal(canEditTrash(null), true);
});
