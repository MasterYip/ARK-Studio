import assert from "node:assert/strict";
import { test } from "node:test";
import { deviceRows, since } from "../../ark_studio/web/js/setup-devices-model.js";

test("devices: current first, then most recently seen; path counts", () => {
  const rows = deviceRows({ devices: [
    { id: "a", label: "old pc", last_seen: "2026-11-01T00:00:00Z", paths: [{ resource: "project/code", path_or_ref: "/x" }], missing: [] },
    { id: "b", label: "laptop", last_seen: "2026-11-19T00:00:00Z", paths: [{ resource: "project/code" }, { resource: "project/tasks" }], missing: ["project/code", "project/tasks"] },
    { id: "c", current: true, paths: [] },
  ] });
  assert.deepEqual(rows.map((r) => r.id), ["c", "b", "a"]);
  assert.equal(rows[0].label, "c");
  assert.equal(rows[1].missing, 2);
  assert.equal(rows[1].paths[0].missing, true);
  assert.equal(rows[2].set, 1);
  assert.deepEqual(deviceRows({}), []);
});

test("since", () => {
  const now = Date.parse("2026-11-19T12:00:00Z");
  assert.deepEqual(since("2026-11-19T11:59:50Z", now), { n: 1, unit: "min" });
  assert.deepEqual(since("2026-11-19T07:00:00Z", now), { n: 5, unit: "h" });
  assert.deepEqual(since("2026-11-16T12:00:00Z", now), { n: 3, unit: "d" });
  assert.equal(since("", now), null);
});
