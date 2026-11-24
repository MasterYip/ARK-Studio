// Run: node --experimental-vm-modules --test tests/js/support_model.test.mjs
// The Board / Home support panel's merging rules (support-model.js) over
// GET /api/project/support replies.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/support-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "support-model.js" });
  await module.link(() => { throw new Error("support-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const m = await loadModel();
const NOW = Date.parse("2026-11-24T12:00:00Z");
const iso = (ms) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, "+00:00");

const reply = {
  can_write: true,
  agents: {
    sessions: [
      { id: "s_old", title: "Old run", platform: "codex", platform_label: "Codex", model: "gpt-5", status: "stopped", last_activity_at: "2026-11-23T10:00:00+00:00", device: "ark" },
      { id: "s_run", title: "Write results", platform: "claude", platform_label: "Claude Code", model: "opus", status: "running",
        task_ref: "T-0003", last_activity_at: "2026-11-24T11:58:00+00:00", device: "ark", children: 2 },
      { id: "s_idle", title: "Review notes", platform: "claude", platform_label: "Claude Code", status: "idle",
        task_ref: "T-0004", last_activity_at: "2026-11-24T11:00:00+00:00", device: "ark" },
      { id: "s_busy", title: "Busy between turns", platform: "claude", status: "idle", busy: { subagents: 1, background: 0 },
        last_activity_at: "2026-11-24T10:00:00+00:00", device: "ark" },
      { id: "s_err", title: "Broken", platform: "claude", status: "errored", error: "exit 1", last_activity_at: "2026-11-24T09:00:00+00:00", device: "ark" },
      { id: "s_ask", title: "Asks", platform: "codex", status: "running", approvals_pending: 1, last_activity_at: "2026-11-24T08:00:00+00:00", device: "ark" },
    ],
    external: [
      { actor: "agent:local/writer", label: "local/writer", writes: 4, last_at: "2026-11-24T11:30:00+00:00", unit_uid: "T-0003", unit_title: "Results" },
      { actor: "agent:claude", label: "claude", writes: 1, last_at: "2026-11-24T11:50:00+00:00", unit_uid: null,
        live: { kind: "working", state: "working", unit_uid: "T-0009", unit_title: "Figure", at: "2026-11-24T11:59:00+00:00" } },
    ],
  },
  hosts: [
    { id: "gpu:gpu-lab", kind: "gpu-server", label: "gpu-lab", state: "online", last_seen: "2026-11-24T11:50:00+00:00",
      gpu: { total: 8, free: 3 }, jobs: { running: 1, queued: 2 }, probed_at: "2026-11-24T11:50:00+00:00" },
    { id: "host:c59c4a65af13", kind: "host", label: "laptop", state: "stale", last_seen: "2026-11-23T08:00:00+00:00", device_id: "c59c4a65af13", paths_missing: 1 },
    { id: "ark", kind: "ark", label: "ark-server", state: "online", sessions_running: 2 },
  ],
  library: { repository: 2, task_space: 1, storage: 1, database: 1, environment: 0, compute: 1, worktree: 2, total: 6, bytes: 3 * 1024 ** 3 },
  deadlines: [
    { kind: "token", id: "tok_1", label: "laptop agent", at: iso(NOW + 5 * 86_400_000) },
    { kind: "gpu_job", id: "j1", label: "train", server: "gpu-lab", at: iso(NOW + 3_600_000) },
    { kind: "review", uid: "T-0007", label: "Draft", at: iso(NOW - 4 * 86_400_000) },
  ],
};

const units = [
  { uid: "T-0003", type: "task", title: "Results", status: "active" },
  { uid: "T-0004", type: "task", title: "Notes", status: "review" },
  { uid: "T-0010", type: "task", title: "Due tomorrow", status: "open", end_at: "2026-11-25" },
  { uid: "T-0011", type: "task", title: "Late", status: "active", end_at: "2026-11-20" },
  { uid: "T-0012", type: "task", title: "Done late", status: "done", end_at: "2026-11-19" },
  { uid: "T-0013", type: "task", title: "Next month", status: "open", end_at: "2026-12-30" },
  { uid: "A-0001", type: "agent", title: "Claude · writer" },
  { uid: "A-0002", type: "agent", title: "Old planner bot" },
  { uid: "H-0001", type: "host", title: "gpu-lab", tags: ["gpu:4090"] },
  { uid: "H-0002", type: "host", title: "lab-box", tags: ["os:linux"] },
  { uid: "R-0001", type: "artifact", title: "Paper PDF" },
  { uid: "R-0002", type: "resource", title: "Dataset" },
];

test("sessionState maps statuses, approvals, background work and the unit in review", () => {
  assert.equal(m.sessionState({ status: "running" }), "running");
  assert.equal(m.sessionState({ status: "starting" }), "running");
  assert.equal(m.sessionState({ status: "idle", busy: { subagents: 1 } }), "running");
  assert.equal(m.sessionState({ status: "idle" }), "idle");
  assert.equal(m.sessionState({ status: "idle" }, "review"), "review");
  assert.equal(m.sessionState({ status: "stopped" }, "review"), "review");
  assert.equal(m.sessionState({ status: "running", approvals_pending: 2 }), "approval");
  assert.equal(m.sessionState({ status: "errored" }), "error");
  assert.equal(m.sessionState({ status: "conflict" }), "error");
  assert.equal(m.sessionState({ status: "stopped" }), "ended");
  assert.equal(m.sessionState(null), "ended");
});

test("agentRows: sessions by state, then recent agents, then unmatched agent nodes", () => {
  const rows = m.agentRows(reply, { units, legacy: units.filter((u) => u.type === "agent") });
  assert.deepEqual(rows.map((r) => r.key), [
    "session:s_run", "session:s_busy", "session:s_ask", "session:s_idle", "session:s_err", "session:s_old",
    "agent:agent:claude", "agent:agent:local/writer", "node:A-0002",
  ]);
  const run = rows[0];
  assert.equal(run.provider, "Claude Code");
  assert.equal(run.model, "opus");
  assert.equal(run.host, "ark-server");
  assert.equal(run.children, 2);
  assert.deepEqual(run.unit, { uid: "T-0003", title: "Results", status: "active" });
  assert.equal(rows.find((r) => r.id === "s_idle").state, "review", "its task waits for review");
  const live = rows.find((r) => r.id === "agent:claude");
  assert.equal(live.state, "working");
  assert.deepEqual(live.unit, { uid: "T-0009", title: "Figure" }, "the live unit wins over the last write");
  const writer = rows.find((r) => r.id === "agent:local/writer");
  assert.equal(writer.state, "recent");
  assert.equal(writer.writes, 4);
  // "Claude · writer" matches no live name exactly, "claude" does after the dot split.
  assert.ok(!rows.some((r) => r.key === "node:A-0001"), "an agent node named like a live agent is merged");
});

test("a session's task title comes from the reply when the page lacks the unit", () => {
  const rows = m.agentRows({ agents: { sessions: [{ id: "s", title: "x", status: "running", task_ref: "T-0099", task_title: "From server" }] } });
  assert.deepEqual(rows[0].unit, { uid: "T-0099", title: "From server", status: "" });
});

test("hostRows: ARK server first, host nodes merged by name, the rest listed as nodes", () => {
  const rows = m.hostRows(reply, { legacy: units.filter((u) => u.type === "host") });
  assert.deepEqual(rows.map((r) => r.key), ["ark", "host:c59c4a65af13", "gpu:gpu-lab", "node:H-0002"]);
  const gpu = rows.find((r) => r.kind === "gpu-server");
  assert.deepEqual(gpu.gpu, { total: 8, free: 3 });
  assert.deepEqual(gpu.jobs, { running: 1, queued: 2 });
  assert.equal(gpu.unit, "H-0001", "the gpu-lab host node points at the live server");
  assert.equal(rows[0].sessions, 2);
  assert.equal(rows[1].pathsMissing, 1);
  assert.deepEqual(rows[3].tags, ["linux"]);
  assert.equal(m.hostRows({ hosts: [{ id: "gpu:a", kind: "gpu-server", label: "a", gpu: null }] })[0].gpu, null, "never probed: no GPU figures");
});

test("libraryCounts: setup resources by group plus artifacts and resource nodes", () => {
  const lib = m.libraryCounts(reply, units);
  assert.equal(lib.artifacts, 1);
  assert.equal(lib.resourceNodes, 1);
  assert.equal(lib.repositories, 2);
  assert.equal(lib.workspaces, 3);
  assert.equal(lib.storage, 2);
  assert.equal(lib.other, 1);
  assert.equal(lib.total, 1 + 1 + 6);
  assert.equal(lib.bytes, 3 * 1024 ** 3);
  assert.deepEqual(m.sizeParts(lib.bytes), { n: 3, unit: "GB" });
  assert.equal(m.libraryCounts({ library: { bytes: null } }, []).bytes, null);
});

test("deadlineRows: due units, expiring tokens, GPU limits and long reviews, oldest first", () => {
  const rows = m.deadlineRows(reply, units, { now: NOW });
  assert.deepEqual(rows.map((r) => r.key), ["unit:T-0011", "review:T-0007", "gpu:j1", "unit:T-0010", "token:tok_1"]);
  assert.equal(rows[0].overdue, true);
  assert.equal(rows.find((r) => r.kind === "review").waitDays, 4);
  assert.equal(rows.find((r) => r.key === "unit:T-0010").overdue, false);
  assert.ok(!rows.some((r) => r.uid === "T-0012"), "done units are not due");
  assert.ok(!rows.some((r) => r.uid === "T-0013"), "beyond 14 days");
  assert.equal(m.daysUntil(rows.find((r) => r.kind === "token").at, NOW), 5);
  assert.equal(m.deadlineRows(reply, units, { now: NOW, limit: 2 }).length, 2);
});

test("summary counts come from the rows the cards show", () => {
  const agents = m.agentRows(reply, { units, legacy: units.filter((u) => u.type === "agent") });
  const hosts = m.hostRows(reply, { legacy: units.filter((u) => u.type === "host") });
  const lib = m.libraryCounts(reply, units);
  assert.deepEqual(m.supportSummary(agents, hosts, lib), { agents: 8, hosts: 4, library: 8 });
  assert.deepEqual(m.sessionCounts(agents), { running: 2, approval: 1, review: 1, error: 1, idle: 0, external: 2 });
});

test("empty states lead to the real action, and none for read-only members", () => {
  assert.deepEqual(m.cardEmpty("agents", { rows: 0, canWrite: true }), { key: "support.agents.empty", action: "launch" });
  assert.deepEqual(m.cardEmpty("agents", { rows: 0, canWrite: false }), { key: "support.agents.empty", action: null });
  assert.deepEqual(m.cardEmpty("hosts", { rows: 0, canWrite: true }), { key: "support.hosts.empty", action: "settings" });
  assert.deepEqual(m.cardEmpty("library", { rows: 0, canWrite: true }), { key: "support.library.empty", action: "settings" });
  assert.deepEqual(m.cardEmpty("due", { rows: 0, canWrite: true }), { key: "support.due.empty", action: null });
  assert.deepEqual(m.cardEmpty("agents", { rows: 0, loading: true }), { key: "support.loading", action: null });
  assert.equal(m.cardEmpty("agents", { rows: 3, canWrite: true }), null);
});

test("an empty or missing reply gives empty rows, not errors", () => {
  for (const data of [null, {}, { agents: {}, hosts: null }]) {
    assert.deepEqual(m.agentRows(data), []);
    assert.deepEqual(m.hostRows(data), []);
    assert.deepEqual(m.deadlineRows(data, []), []);
    assert.equal(m.libraryCounts(data, []).total, 0);
  }
  const nodesOnly = m.agentRows(null, { legacy: [{ uid: "A-1", type: "agent", title: "Planner" }] });
  assert.deepEqual(nodesOnly.map((r) => [r.kind, r.title]), [["node", "Planner"]]);
});

test("token agents linked to a session merge into its row; unlinked ones stay with a hint", () => {
  const data = {
    agents: {
      sessions: [
        { id: "s_sand", title: "SandBox", platform: "claude", platform_label: "Claude Code", status: "running",
          last_activity_at: "2026-11-24T10:00:00+00:00", device: "ark",
          linked_agents: [
            { actor: "agent:MasterYip/claude-sandbox", label: "MasterYip/claude-sandbox", writes: 136, last_at: "2026-11-24T10:13:00+00:00",
              unit_uid: "T-0038", unit_title: "Sandbox work", session_id: "s_sand", session_via: "header" },
            { actor: "agent:MasterYip/sandbox", writes: 4, last_at: "2026-11-24T09:00:00+00:00", session_id: "s_sand", session_via: "token" },
          ] },
        { id: "s_hoi", title: "WM-HOI", platform: "claude", status: "idle", last_activity_at: "2026-11-24T09:00:00+00:00", device: "ark" },
      ],
      external: [
        { actor: "agent:MasterYip", label: "MasterYip", writes: 8, last_at: "2026-11-24T04:15:00+00:00" },
        { actor: "agent:MasterYip/other", label: "MasterYip/other", writes: 2, last_at: "2026-11-24T11:00:00+00:00",
          unit_uid: "T-0040", likely_session: { id: "s_hoi", title: "WM-HOI" } },
      ],
    },
  };
  const rows = m.agentRows(data, { units: [{ uid: "T-0038", title: "Sandbox work", status: "active" }] });
  assert.deepEqual(rows.map((r) => r.key), ["session:s_sand", "session:s_hoi", "agent:agent:MasterYip/other", "agent:agent:MasterYip"]);
  const sand = rows[0];
  assert.equal(sand.writes, 140);
  assert.equal(sand.viaToken, true);
  assert.equal(sand.unit.uid, "T-0038");
  assert.equal(sand.at, "2026-11-24T10:13:00+00:00");
  assert.equal(rows[1].viaToken, false);
  assert.equal(rows[1].writes, 0);
  assert.deepEqual(rows[2].likely, { id: "s_hoi", title: "WM-HOI" });
  assert.equal(rows[3].likely, null);
  assert.equal(m.sessionCounts(rows).external, 2);
});
