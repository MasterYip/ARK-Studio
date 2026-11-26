// Run: node --experimental-vm-modules --test tests/js/schedules_model.test.mjs
// Settings › Lines and Settings › Scheduled tasks (schedules-model.js).
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  validPrefix, suggestionRows, applyBatches, scheduleLabel, taskFromForm, formFor, runStatus,
} from "../../ark_studio/web/js/schedules-model.js";
import { resolveSettingsTab, settingsTabScope } from "../../ark_studio/web/js/settings-tabs.js";

test("line prefix", () => {
  for (const ok of ["branch", "line", "track_2", "a-b"]) assert.equal(validPrefix(ok), true, ok);
  for (const bad of ["", "Line", "1line", "needs", "marker", "a b", "x".repeat(25)]) assert.equal(validPrefix(bad), false, bad);
});

const answer = {
  inherit: [
    { uid: "T-0001", title: "Task", status: "done", tags: ["line:frontend"], via: ["TP-0005"] },
    { uid: "D-0011", title: "Both", tags: ["line:graph", "line:ops"], via: ["TP-0015", "TP-0007"] },
    { uid: "X-1", tags: [] },
  ],
  new_lines: [{ root: { uid: "Q-1", title: "Goal" }, name: "goal", tag: "line:goal", units: ["T-2", "T-3"], include_root: ["Q-1"] }],
};

test("suggestions become reviewable rows", () => {
  const rows = suggestionRows(answer);
  assert.deepEqual(rows.map((r) => [r.key, r.units, r.tags, r.count]), [
    ["i:T-0001", ["T-0001"], ["line:frontend"], 1],
    ["i:D-0011", ["D-0011"], ["line:graph", "line:ops"], 1],
    ["n:line:goal", ["Q-1", "T-2", "T-3"], ["line:goal"], 3],
  ]);
  assert.equal(rows[0].reason, "TP-0005");
  assert.deepEqual(suggestionRows({}), []);
});

test("applying rows: one update per unit, batched", () => {
  const rows = suggestionRows(answer);
  const one = applyBatches(rows);
  assert.equal(one.length, 1);
  assert.deepEqual(one[0].updates.find((u) => u.unit === "D-0011").tags_add, ["line:graph", "line:ops"]);
  assert.equal(one[0].updates.length, 5);
  assert.equal(applyBatches(rows, 2).length, 3);
  assert.deepEqual(applyBatches([]), []);
});

test("task form: defaults, validation, templates and saved tasks", () => {
  const tpl = { id: "branch-tags", name: "维护线标签", prompt: "p", mode: "suggest", schedule: { kind: "daily", at: "07:00", tz_offset: 480 } };
  const fresh = formFor(tpl, 60);
  assert.equal(fresh.id, "");
  assert.equal(fresh.template, "branch-tags");
  assert.equal(fresh.tz_offset, 60); // a new task uses this browser's zone
  assert.equal(fresh.mode, "suggest");
  const { task, errors } = taskFromForm(fresh);
  assert.deepEqual(errors, []);
  assert.deepEqual(task.schedule, { kind: "daily", tz_offset: 60, at: "07:00" });
  const saved = formFor({ ...tpl, id: "st_1", owner: "ana", enabled: false });
  assert.equal(saved.id, "st_1");
  assert.equal(saved.tz_offset, 480);
  assert.equal(taskFromForm(saved).task.id, "st_1");
  assert.deepEqual(taskFromForm({ kind: "hours", every: 0 }).errors, ["name", "prompt", "every"]);
  assert.deepEqual(taskFromForm({ name: "n", prompt: "p", kind: "weekly", at: "7:00" }).errors, ["at"]);
  assert.equal(taskFromForm({ name: "n", prompt: "p", kind: "weekly", at: "08:00", weekday: 9 }).task.schedule.weekday, 6);
});

test("schedule labels and statuses", () => {
  const t = (key, vars) => `${key}|${JSON.stringify(vars || {})}`;
  assert.match(scheduleLabel({ kind: "daily", at: "07:00", tz_offset: 480 }, t), /UTC\+8/);
  assert.match(scheduleLabel({ kind: "daily", at: "07:00", tz_offset: -330 }, t), /UTC-5:30/);
  assert.match(scheduleLabel({ kind: "hours", every: 6 }, t), /"n":6/);
  assert.equal(runStatus({}), "never");
  assert.equal(runStatus({ last_status: "failed" }), "failed");
  assert.equal(runStatus({ last_status: "ok", running_since: "x" }), "running");
});

test("Settings tabs for lines and scheduled tasks", () => {
  assert.deepEqual(resolveSettingsTab("lines"), { tab: "lines", section: "" });
  assert.deepEqual(resolveSettingsTab("schedules"), { tab: "schedules", section: "" });
  assert.equal(settingsTabScope("schedules"), "project");
});
