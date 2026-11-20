// Run: node --experimental-vm-modules --test tests/js/setup_review_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../ark_studio/web/js/setup-review-model.js", import.meta.url), "utf8");
const module = new vm.SourceTextModule(source, { identifier: "setup-review-model.js" });
await module.link(() => { throw new Error("setup-review-model must stay dependency-free"); });
await module.evaluate();
const m = module.namespace;

const st = (project, user) => ({ layers: { project, ...(user ? { user } : {}) } });

test("pages: project first, then my layer, with their modes", () => {
  const pages = m.reviewPages(st({ state: "proposed", digest: "p1" }, { state: "proposed", digest: "u1" }));
  assert.deepEqual(pages.map((p) => [p.layer, p.mode, p.digest, p.canConfirm]),
    [["project", "confirm", "p1", true], ["user", "confirm", "u1", true]]);
});

test("non-admins see the project layer read-only", () => {
  const pages = m.reviewPages(st({ state: "proposed", can_confirm: false }, { state: "proposed" }));
  assert.equal(pages[0].mode, "readonly");
  assert.equal(pages[0].canConfirm, false);
  assert.equal(pages[1].canConfirm, true);
});

test("modes for confirmed, draft, missing layers", () => {
  assert.equal(m.pageMode(st({ state: "verified" }), "project"), "done");
  assert.equal(m.pageMode(st({ state: "confirmed" }), "project"), "done");
  assert.equal(m.pageMode(st({ state: "draft" }), "project"), "draft");
  assert.equal(m.pageMode(st({ state: "stale" }), "project"), "draft");
  assert.equal(m.pageMode(st({ state: "unset" }), "project"), "pending");
  assert.equal(m.pageMode(st({ state: "confirmed" }), "user"), "none");
  assert.equal(m.pageMode({ state: "proposed" }, "project"), "confirm"); // older payload without layers
  assert.equal(m.pageMode({}, "project"), "none");
});

test("step labels", () => {
  assert.equal(m.stepLabel(0, 2), "1/2");
  assert.equal(m.stepLabel(1, 2), "2/2");
});

test("Home offers review when a layer awaits this user or an admin's draft", () => {
  assert.equal(m.needsReview(st({ state: "proposed" }, { state: "confirmed" })), true);
  assert.equal(m.needsReview(st({ state: "proposed", can_confirm: false }, { state: "draft" })), false);
  assert.equal(m.needsReview(st({ state: "proposed", can_confirm: false }, { state: "proposed" })), true);
  assert.equal(m.needsReview(st({ state: "draft" }, { state: "draft" })), true);
  assert.equal(m.needsReview(st({ state: "draft", can_confirm: false }, { state: "draft" })), false);
  assert.equal(m.needsReview(st({ state: "verified" }, { state: "confirmed" })), false);
});

test("combined result", () => {
  const pages = m.reviewPages(st({ state: "proposed", can_confirm: false }, { state: "proposed" }));
  assert.deepEqual(m.reviewOutcome(pages, new Set(["user"])),
    [{ layer: "project", result: "admin" }, { layer: "user", result: "confirmed" }]);
  assert.deepEqual(m.reviewOutcome(m.reviewPages(st({ state: "verified" }, { state: "proposed" }))),
    [{ layer: "project", result: "already" }, { layer: "user", result: "skipped" }]);
  assert.deepEqual(m.reviewOutcome(m.reviewPages(st({ state: "draft" }))),
    [{ layer: "project", result: "draft" }, { layer: "user", result: "none" }]);
  assert.deepEqual(m.reviewOutcome(m.reviewPages(st({ state: "unset" }))),
    [{ layer: "project", result: "pending" }, { layer: "user", result: "none" }]);
});

test("needsConfirm: only a proposed layer this user may confirm", () => {
  assert.equal(m.needsConfirm(st({ state: "proposed" }, { state: "unset" })), true);
  assert.equal(m.needsConfirm(st({ state: "confirmed" }, { state: "proposed" })), true);
  assert.equal(m.needsConfirm(st({ state: "draft" }, { state: "draft" })), false);
  assert.equal(m.needsConfirm(st({ state: "proposed", can_confirm: false }, { state: "confirmed" })), false);
  assert.equal(m.needsConfirm({}), false);
});

test("visible pages leave out empty layers, never all of them", () => {
  const layers = (pages) => m.visiblePages(pages).map((p) => p.layer);
  assert.deepEqual(layers(m.reviewPages(st({ state: "proposed" }, { state: "unset" }))), ["project"]);
  assert.deepEqual(layers(m.reviewPages(st({ state: "proposed" }))), ["project"]);
  assert.deepEqual(layers(m.reviewPages(st({ state: "proposed" }, { state: "draft" }))), ["project", "user"]);
  assert.deepEqual(layers(m.reviewPages(st({ state: "unset" }, { state: "proposed" }))), ["user"]);
  assert.deepEqual(layers(m.reviewPages(st({ state: "unset" }, { state: "unset" }))), ["project"]);
});

test("plain summary: goal, allow / ask / deny in a fixed order, resources per layer", () => {
  const state = {
    viewer: { user: "ana" },
    profile: { objective: " Find grokking " },
    questions: [{}, {}],
    policies: {
      project: { purchase: "deny", push: "ask", commit: "allow", gpu_dispatch: "ask", external_message: "deny", custom_x: "ask" },
      user: { push: "deny" },
    },
    resources: [
      { layer: "project", kind: "repository", name: "grokking" },
      { layer: "project", kind: "command", id: "unit" },
      { layer: "project", kind: "repository", name: "paper" },
      { layer: "user", kind: "environment", name: "venv", owner: "ana" },
      { layer: "user", kind: "environment", name: "bob-env", owner: "bob" },
    ],
  };
  assert.deepEqual(m.plainSummary(state, "project"), {
    objective: "Find grokking",
    allow: ["commit"], ask: ["push", "gpu_dispatch", "custom_x"], deny: ["external_message", "purchase"],
    resources: [{ kind: "repository", names: ["grokking", "paper"] }, { kind: "command", names: ["unit"] }],
    questions: 2,
  });
  assert.deepEqual(m.plainSummary(state, "user"), {
    objective: "", allow: [], ask: [], deny: ["push"],
    resources: [{ kind: "environment", names: ["venv"] }], questions: 0,
  });
  const empty = m.plainSummary({}, "project");
  assert.deepEqual([empty.allow, empty.resources, empty.questions], [[], [], 0]);
});

test("one Confirm covers every proposed layer this user may confirm", () => {
  // Only the project layer was sent; my layer has nothing pending.
  let plan = m.confirmPlan(st({ state: "proposed", digest: "p2" }, { state: "confirmed", digest: "u1" }));
  assert.deepEqual(plan, { confirm: [{ layer: "project", digest: "p2" }], admin: [], drafts: [] });
  assert.equal(m.confirmLabelKey(plan), "setupreview.confirm.project");
  // Only my layer was sent.
  plan = m.confirmPlan(st({ state: "verified", digest: "p1" }, { state: "proposed", digest: "u2" }));
  assert.deepEqual(plan.confirm, [{ layer: "user", digest: "u2" }]);
  assert.equal(m.confirmLabelKey(plan), "setupreview.confirm.user");
  // Both, each with its own digest.
  plan = m.confirmPlan(st({ state: "proposed", digest: "p2" }, { state: "proposed", digest: "u2" }));
  assert.deepEqual(plan.confirm, [{ layer: "project", digest: "p2" }, { layer: "user", digest: "u2" }]);
  assert.equal(m.confirmLabelKey(plan), "setupreview.confirm.both");
  // A member facing a project change confirms only their own layer.
  plan = m.confirmPlan(st({ state: "proposed", digest: "p2", can_confirm: false }, { state: "proposed", digest: "u2" }));
  assert.deepEqual(plan, { confirm: [{ layer: "user", digest: "u2" }], admin: ["project"], drafts: [] });
  plan = m.confirmPlan(st({ state: "proposed", can_confirm: false }, { state: "confirmed" }));
  assert.deepEqual(plan, { confirm: [], admin: ["project"], drafts: [] });
  assert.equal(m.confirmLabelKey(plan), "");
  // A draft is not confirmed with the rest.
  plan = m.confirmPlan(st({ state: "proposed", digest: "p2" }, { state: "draft", digest: "u3" }));
  assert.deepEqual(plan, { confirm: [{ layer: "project", digest: "p2" }], admin: [], drafts: ["user"] });
});

test("change rows: added, changed with old and new values, removed; paths join their resource", () => {
  const rows = m.changeRows({
    baseline: "confirmed",
    resources: [
      { key: "project/tasks", id: "tasks", kind: "task_space", name: "agent-team-tasks", change: "added" },
      { key: "project/repo", id: "repo", kind: "repository", name: "Repo", change: "changed",
        fields: [{ field: "access", old: "read-only", new: "read-write" }] },
      { key: "project/old", id: "old", kind: "storage", name: "Old", change: "removed" },
    ],
    fields: [{ field: "objective", old: "", new: "Ship it" }, { field: "notes", old: "a", new: "b" }],
    policies: [{ field: "push", old: "ask", new: "deny" }],
    locations: [
      { resource: "project/tasks", host_id: "h1", host_label: "lab", path_or_ref: "/data/tasks", change: "added" },
      { resource: "project/old", host_id: "h1", host_label: "lab", path_or_ref: "/data/old", change: "removed" },
      { resource: "u:ana/ark-studio-public", host_id: "h2", host_label: "laptop", path_or_ref: "/src/ark", change: "added" },
    ],
  }, new Map([["u:ana/ark-studio-public", "ARK public"]]));
  assert.deepEqual(rows.map((r) => [r.type, r.change, r.name || r.field]), [
    ["resource", "added", "agent-team-tasks"], ["resource", "changed", "Repo"], ["resource", "removed", "Old"],
    ["field", "added", "objective"], ["field", "changed", "notes"], ["policy", "changed", "push"],
    ["location", "added", "ARK public"],
  ]);
  assert.deepEqual(rows[0].locations, [{ change: "added", resource: "project/tasks", host: "lab", path: "/data/tasks", name: "tasks" }]);
  assert.deepEqual(rows[1].fields, [{ field: "access", old: "read-only", new: "read-write" }]);
  assert.deepEqual(rows[2].locations, []); // a removed resource's paths go with it
  assert.equal(rows[6].path, "/src/ark");
  assert.deepEqual(m.changeRows(null), []);
});

test("change groups: per layer, unmarked when never confirmed", () => {
  const state = {
    resources: [{ key: "project/tasks", name: "Tasks" }],
    layers: {
      project: { state: "proposed", digest: "p2", changes: { baseline: "confirmed",
        resources: [{ key: "project/tasks", id: "tasks", kind: "task_space", name: "Tasks", change: "added" }] } },
      user: { state: "proposed", digest: "u1", changes: { baseline: "none",
        resources: [{ key: "u:ana/x", id: "x", kind: "storage", name: "X", change: "added" }] } },
    },
  };
  const groups = m.changeGroups(state);
  assert.deepEqual(groups.map((g) => [g.layer, g.mode, g.baseline, g.rows.length]),
    [["project", "confirm", "confirmed", 1], ["user", "confirm", "none", 0]]);
  // A layer with nothing recorded is not shown.
  assert.deepEqual(m.changeGroups(st({ state: "proposed" }, { state: "unset" })).map((g) => g.layer), ["project"]);
});
