// Run: node --experimental-vm-modules --test tests/js/session_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../ark_studio/web/js/session-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "session-model.js" });
  await module.link(() => { throw new Error("session-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const model = await loadModel();

const sessions = [
  { id: "s_1", title: "Running root", status: "running", agent: "reviewer", task_ref: "T-1",
    last_activity_at: "2026-10-31T10:00:00+00:00", parent_id: "" },
  { id: "s_2", title: "Child", status: "stopped", agent: "reviewer", task_ref: "T-1",
    last_activity_at: "2026-10-31T09:00:00+00:00", parent_id: "s_1", depth: 1 },
  { id: "s_3", title: "Grandchild", status: "errored", agent: "auditor", task_ref: "T-2",
    last_activity_at: "2026-10-31T08:00:00+00:00", parent_id: "s_2", depth: 2 },
  { id: "s_4", title: "Finished root", status: "stopped", agent: "auditor", task_ref: "",
    last_activity_at: "2026-10-31T07:00:00+00:00", parent_id: "" },
];

test("status buckets and active detection", () => {
  assert.equal(model.isActiveSession(sessions[0]), true);
  assert.equal(model.isActiveSession(sessions[1]), false);
  assert.equal(model.statusBucket(sessions[2]), "errored");
  assert.equal(model.statusBucket(sessions[3]), "completed");
});

test("filter by bucket, subagents and query", () => {
  assert.deepEqual(model.filterSessions(sessions, { filter: "active" }).map((s) => s.id), ["s_1"]);
  assert.deepEqual(model.filterSessions(sessions, { filter: "completed" }).map((s) => s.id).sort(),
    ["s_2", "s_3", "s_4"]);
  assert.deepEqual(model.filterSessions(sessions, { filter: "subagents" }).map((s) => s.id), ["s_2", "s_3"]);
  assert.deepEqual(model.filterSessions(sessions, { query: "AUDITOR" }).map((s) => s.id), ["s_3", "s_4"]);
  assert.deepEqual(model.filterSessions(sessions, { query: "T-1", filter: "subagents" }).map((s) => s.id), ["s_2"]);
});

test("grouping is active-first for status and alphabetical otherwise", () => {
  const groups = model.groupSessions(sessions, "status");
  assert.deepEqual(groups.map((group) => group.id), ["active", "errored", "completed"]);
  assert.deepEqual(groups[0].sessions.map((s) => s.id), ["s_1"]);
  const byAgent = model.groupSessions(sessions, "agent");
  assert.deepEqual(byAgent.map((group) => group.label), ["auditor", "reviewer"]);
  const byTask = model.groupSessions(sessions, "task");
  assert.deepEqual(byTask.map((group) => group.label), ["No task", "T-1", "T-2"]);
});

test("subagent trees are recursive and cycle-safe", () => {
  assert.deepEqual(model.sessionDescendants(sessions, "s_1").map((row) => [row.session.id, row.depth]),
    [["s_2", 1], ["s_3", 2]]);
  assert.deepEqual(model.sessionAncestors(sessions, "s_3").map((s) => s.id), ["s_1", "s_2"]);
  assert.deepEqual(model.sessionRoots(sessions).map((s) => s.id), ["s_1", "s_4"]);
  const cyclic = [
    { id: "a", parent_id: "b", status: "stopped" },
    { id: "b", parent_id: "a", status: "stopped" },
  ];
  assert.equal(model.sessionDescendants(cyclic, "a").length, 1);
  assert.deepEqual(model.sessionAncestors(cyclic, "a").map((s) => s.id), ["b"]);
});

test("media kind and sizing", () => {
  assert.equal(model.mediaKind("x.PNG", ""), "image");
  assert.equal(model.mediaKind("clip.bin", "video/mp4"), "video");
  assert.equal(model.mediaKind("voice.bin", "audio/mpeg"), "audio");
  assert.equal(model.mediaKind("paper.pdf", ""), "pdf");
  assert.equal(model.mediaKind("notes.md", "text/markdown"), "document");
  assert.equal(model.formatBytes(0), "0 B");
  assert.equal(model.formatBytes(2048), "2.0 KB");
});

test("transcript metadata and selection fallback", () => {
  const items = [
    { type: "message", text: "hello" },
    { type: "tool", output: "ok" },
    { type: "error", text: "bad" },
    { type: "notice", level: "reasoning", text: "hmm" },
  ];
  assert.deepEqual(model.transcriptMeta(items), { messages: 1, tools: 1, errors: 1, reasoning: 1 });
  assert.equal(model.nextSelection(sessions, "missing"), "s_1");
  assert.equal(model.nextSelection(sessions, "s_4"), "s_4");
  assert.equal(model.nextSelection([], ""), "");
  const activeChildFirst = [
    { id: "child", parent_id: "root", status: "running", last_activity_at: "2026-10-31T12:00:00+00:00" },
    { id: "root", parent_id: "", status: "running", last_activity_at: "2026-10-31T11:00:00+00:00" },
  ];
  assert.equal(model.nextSelection(activeChildFirst, ""), "root");
  assert.equal(model.nextSelection([{ id: "done", parent_id: "", status: "stopped" }], ""), "done");
});

test("file tab state opens, activates, closes and normalizes", () => {
  let state = model.normalizeTabState(null);
  assert.deepEqual(state, { tabs: [], active: "" });
  state = model.openFileTab(state, "artifacts/a.py");
  state = model.openFileTab(state, "artifacts/b.md");
  assert.deepEqual(state, { tabs: ["artifacts/a.py", "artifacts/b.md"], active: "artifacts/b.md" });
  state = model.activateFileTab(state, "artifacts/a.py");
  assert.equal(state.active, "artifacts/a.py");
  state = model.openFileTab(state, "artifacts/b.md");
  assert.deepEqual(state.tabs, ["artifacts/a.py", "artifacts/b.md"]);
  state = model.closeFileTab(state, "artifacts/b.md");
  assert.deepEqual(state, { tabs: ["artifacts/a.py"], active: "artifacts/a.py" });
  state = model.closeFileTab(state, "artifacts/a.py");
  assert.deepEqual(state, { tabs: [], active: "" });
  assert.deepEqual(model.openFileTab(null, ""), { tabs: [], active: "" });
  assert.deepEqual(model.activateFileTab({ tabs: ["a"], active: "a" }, "missing"),
    { tabs: ["a"], active: "a" });
  let many = { tabs: [], active: "" };
  for (let i = 0; i < 12; i += 1) many = model.openFileTab(many, `f${i}.txt`);
  assert.equal(many.tabs.length, model.FILE_TAB_LIMIT);
  assert.equal(many.tabs.at(-1), "f11.txt");
  assert.equal(many.tabs.includes("f0.txt"), false);
});

test("file language and view kind feed the right-rail code view", () => {
  assert.equal(model.fileLanguage("artifacts/a.py"), "python");
  assert.equal(model.fileLanguage("README.MD"), "markdown");
  assert.equal(model.fileLanguage("data.unknownext"), "");
  assert.equal(model.tabLabel("artifacts/deep/a b.ts"), "a b.ts");
  assert.equal(model.tabLabel(""), "file");
  assert.equal(model.fileViewKind("artifacts/notes.md", "text/markdown"), "markdown");
  assert.equal(model.fileViewKind("artifacts/main.js", "text/javascript"), "code");
  assert.equal(model.fileViewKind("artifacts/data.bin"), "text");
  assert.equal(model.fileViewKind("artifacts/shot.png"), "image");
  assert.equal(model.fileViewKind("artifacts/clip.mp4", "video/mp4"), "video");
  assert.equal(model.fileViewKind("artifacts/voice.wav", "audio/wav"), "audio");
});

test("permission modes and approval state helpers", () => {
  assert.equal(model.permissionModeLabel("accept_edits"), "accept edits");
  assert.equal(model.permissionModeLabel("plan"), "plan");
  assert.match(model.permissionModeDescription("ask"), /approval/);
  assert.equal(model.sessionPermissionModes({}, { permission_modes: ["read_only", "full_access"] }).length, 2);
  assert.deepEqual(model.sessionPermissionModes({ permission_modes: ["read_only", "bogus", "plan"] }),
    ["read_only", "plan"]);
  assert.equal(model.sessionAccessMode({ access_mode: "bogus", permission_modes: ["plan", "read_only"] }), "plan");
  assert.equal(model.isApprovalMode("ask"), true);
  assert.equal(model.isApprovalMode("full_access"), false);
  const session = {
    access_mode: "ask",
    permission_modes: ["read_only", "plan", "ask", "accept_edits", "full_access"],
    capabilities: { supports_approval: true, permission_modes: ["read_only", "plan", "ask", "accept_edits", "full_access"] },
    approvals: { items: [
      { id: "r1", status: "pending", kind: "command", command: "pytest" },
      { id: "r2", status: "allowed", scope: "always" },
      { id: "r3", status: "denied" },
    ] },
  };
  assert.equal(model.sessionSupportsApproval(session), true);
  assert.equal(model.sessionSupportsApproval({ capabilities: { supports_approval: false } }), false);
  assert.deepEqual(model.pendingApprovals(session).map((item) => item.id), ["r1"]);
  assert.equal(model.approvalStatusLabel({ status: "pending" }), "waiting for your decision");
  assert.equal(model.approvalStatusLabel({ status: "allowed", scope: "always" }), "allowed always");
  assert.equal(model.approvalStatusLabel({ status: "denied", auto: true }), "blocked by mode");
  assert.equal(model.approvalStatusLabel({ status: "expired" }), "expired");
  assert.equal(model.canDecideApproval({ status: "pending" }, true), true);
  assert.equal(model.canDecideApproval({ status: "pending" }, false), false);
  assert.equal(model.approvalRequestLabel({ kind: "file_edit", path: "artifacts/a.md" }), "artifacts/a.md");
  assert.equal(model.approvalRequestLabel({ kind: "command", command: "ls" }), "ls");
  assert.equal(model.forkRelationLabel({ relation: "side_chat" }), "side chat");
  assert.equal(model.forkRelationLabel({ relation: "fork" }), "fork");
  const counts = model.approvalCounts(session);
  assert.equal(counts.pending, 1);
  assert.equal(counts.allowed, 1);
  assert.equal(counts.denied, 1);
});

test("steer queue edit and reorder helpers", () => {
  const session = { steer_queue: [
    { id: "a", status: "delivered", text: "already sent" },
    { id: "b", status: "queued", text: "second" },
    { id: "c", status: "queued", text: "third" },
  ] };
  assert.deepEqual(model.queuedSteerItems(session).map((item) => item.id), ["b", "c"]);
  assert.equal(model.editableSteer({ id: "a", status: "delivered" }), false);
  assert.equal(model.editableSteer({ id: "b", status: "queued" }), true);
  assert.deepEqual(model.steerMoveOrder(session, "c", -1), ["c", "b"]);
  assert.deepEqual(model.steerMoveOrder(session, "b", 1), ["c", "b"]);
  assert.equal(model.steerMoveOrder(session, "b", -1), null);
  assert.equal(model.steerMoveOrder(session, "c", 1), null);
  assert.equal(model.steerMoveOrder(session, "missing", -1), null);
  assert.equal(model.steerMoveOrder({ steer_queue: [] }, "a", -1), null);
});

test("composer mode switches between steer and continue", () => {
  const running = { status: "running", capabilities: { followup: true } };
  const queued = { status: "idle", capabilities: { followup: false } };
  const stopped = { status: "stopped", capabilities: { followup: true } };
  assert.equal(model.composerMode(running).mode, "steer");
  assert.equal(model.composerMode(running).channel, "stdin");
  assert.equal(model.composerMode(queued).channel, "next_turn");
  assert.equal(model.composerMode(stopped).mode, "continue");
  assert.equal(model.composerMode(null).canSend, false);
  assert.equal(model.steerLabel({ status: "delivered", channel: "stdin" }), "delivered");
  assert.equal(model.steerLabel({ status: "queued", channel: "next_turn" }), "queued for next turn");
  assert.match(model.steerLabel({ status: "failed", error: "stdin closed" }), /stdin closed/);
  assert.equal(model.formatTokens(999), "999");
  assert.equal(model.formatTokens(1500), "1.5k");
  assert.equal(model.formatTokens(2500000), "2.5m");
});

test("worktree grouping, labels and provider status are explicit", () => {
  const worktreeSessions = [
    { id: "a", title: "Agent A", status: "running", last_activity_at: "2026-10-31T12:00:00+00:00",
      worktree: { path: "/repo-worktrees/agent-x", branch: "agent/x", conflict: false } },
    { id: "b", title: "Agent B", status: "running", last_activity_at: "2026-10-31T11:00:00+00:00",
      worktree: { path: "/repo-worktrees/agent-x", branch: "agent/x", conflict: true } },
    { id: "c", title: "Private", status: "running", last_activity_at: "2026-10-31T10:00:00+00:00" },
  ];
  const groups = model.groupSessions(worktreeSessions, "worktree");
  assert.deepEqual(groups.map((group) => group.label), ["agent/x", "No worktree"]);
  assert.deepEqual(groups[0].sessions.map((session) => session.id), ["a", "b"]);
  assert.equal(model.worktreeLabel(worktreeSessions[0]), "agent/x");
  assert.equal(model.worktreeLabel(worktreeSessions[2]), "");
  assert.equal(model.worktreeConflict(worktreeSessions[0]), false);
  assert.equal(model.worktreeConflict(worktreeSessions[1]), true);
  assert.match(model.sessionSearchText(worktreeSessions[0]), /agent\/x/);
  assert.equal(model.providerStatusLabel("ready"), "ready");
  assert.equal(model.providerStatusLabel("missing"), "not installed");
  assert.equal(model.providerStatusLabel("unauthenticated"), "not signed in");
  assert.equal(model.providerStatusLabel("unknown"), "auth unknown");
  assert.equal(model.providerStatusLabel("whatever"), "unknown");
});

test("thread blocks group consecutive tools and separate user turns", () => {
  const items = [
    { id: "m-1", type: "message", role: "user", text: "fix it", at: "2026-11-03T08:00:00Z" },
    { id: "c1", type: "tool", name: "shell", input: { command: "git status" }, status: "ok", output: "M a" },
    { id: "c2", type: "tool", name: "grep", input: { pattern: "x" }, status: "ok", output: "" },
    { id: "m-2", type: "message", role: "assistant", text: "done", at: "2026-11-03T08:00:05Z" },
    { id: "e1", type: "tool", name: "apply_patch", input: { patch: "*** Update File: a.js\n*** Add File: b.css\n" }, status: "ok" },
    { id: "e2", type: "tool", name: "write_file", input: { path: "a.js" }, status: "running" },
    { id: "m-3", type: "message", role: "user", text: "again", at: "2026-11-03T09:00:00Z" },
    { id: "x-1", type: "error", text: "stream disconnected" },
    { id: "n-1", type: "notice", level: "reasoning", text: "hmm", streaming: true },
  ];
  const blocks = model.threadBlocks(items);
  assert.deepEqual(blocks.map((block) => block.kind),
    ["separator", "user", "tools", "assistant", "tools", "separator", "user", "error", "reasoning"]);
  assert.equal(blocks[2].key, "tools-c1");
  assert.deepEqual(blocks[2].items.map((item) => item.id), ["c1", "c2"]);
  const summary = model.toolGroupSummary(blocks[4].items);
  assert.equal(summary.counts.edit, 2);
  assert.deepEqual(summary.files, ["a.js", "b.css"]);
  assert.equal(summary.edited, 2);
  assert.equal(summary.running, 1);
  assert.equal(summary.kind, "edit");
  const mixed = model.toolGroupSummary(blocks[2].items);
  assert.equal(mixed.counts.command, 1);
  assert.equal(mixed.counts.search, 1);
  assert.equal(model.toolCommand(items[1]), "git status");
  assert.equal(model.toolKind({ name: "tool", input: { command: "ls" } }), "command");
  assert.equal(model.toolKind({ name: "Read" }), "read");
  // Gemini CLI's tool names (stream-json tool_use).
  assert.equal(model.toolKind({ name: "replace", input: { file_path: "a.txt" } }), "edit");
  assert.equal(model.toolKind({ name: "write_file" }), "edit");
  assert.equal(model.toolKind({ name: "run_shell_command", input: { command: "ls" } }), "command");
  assert.equal(model.toolKind({ name: "read_file" }), "read");
  assert.equal(model.toolKind({ name: "grep_search" }), "search");
  assert.equal(model.toolKind({ name: "list_directory" }), "search");
  assert.equal(model.toolKind({ name: "google_web_search" }), "search");
  assert.equal(model.toolKind({ name: "web_fetch" }), "read");
  assert.equal(model.toolKind({ name: "replace_all_users" }), "other");
  // Signatures change only when the block content changes.
  const before = model.blockSignature(blocks[4]);
  blocks[4].items[1].status = "ok";
  assert.notEqual(model.blockSignature(blocks[4]), before);
  assert.equal(model.blockSignature(blocks[3]), model.blockSignature({ kind: "assistant", item: { ...items[3] } }));
});

test("live activity reports the phase and the current turn start", () => {
  const session = { status: "running", started_at: "2026-11-03T08:00:00Z" };
  assert.equal(model.liveActivity([], { status: "stopped" }), null);
  assert.equal(model.liveActivity([], session).phase, "working");
  assert.equal(model.liveActivity([{ type: "notice", level: "reasoning", streaming: true }], session).phase, "thinking");
  assert.equal(model.liveActivity([{ type: "tool", name: "bash", status: "running" }], session).phase, "running");
  const live = model.liveActivity([
    { type: "message", role: "user", at: "2026-11-03T09:00:00Z" },
    { type: "message", role: "assistant", streaming: true },
  ], session);
  assert.equal(live.phase, "writing");
  assert.equal(live.since, "2026-11-03T09:00:00.000Z");
});

test("model picker groups configured, detected and builtin with the default first", () => {
  const preset = { models: ["mine"] };
  const caps = {
    efforts: ["low"],
    model_options: [
      { id: "mine", source: "configured" },
      { id: "b", source: "detected", efforts: ["medium"] },
      { id: "a", source: "detected", default: true, efforts: ["low", "xhigh"] },
      { id: "z", source: "builtin" },
    ],
  };
  const options = model.modelOptions(preset, caps);
  assert.deepEqual(options.map((item) => item.id), ["mine", "b", "a", "z"]);
  assert.equal(model.defaultModelOption(options).id, "a");
  const picker = model.modelPickerGroups(options, "b");
  assert.deepEqual(picker.groups.map((group) => group.source), ["configured", "detected", "builtin"]);
  assert.deepEqual(picker.groups[1].items.map((item) => item.id), ["a", "b"]);
  assert.equal(picker.currentIsCustom, false);
  assert.equal(model.modelPickerGroups(options, "typed-by-hand").currentIsCustom, true);
  assert.equal(model.modelPickerGroups(options, "").currentIsCustom, false);
});

test("model efforts follow the selected model, configured efforts win", () => {
  const caps = { efforts: ["low", "medium"], model_options: [
    { id: "a", source: "detected", default: true, efforts: ["low", "xhigh"] },
    { id: "b", source: "detected", efforts: ["medium"] },
    { id: "c", source: "detected" },
  ] };
  assert.deepEqual(model.modelEfforts({}, caps, "b"), ["medium"]);
  assert.deepEqual(model.modelEfforts({}, caps, ""), ["low", "xhigh"]);
  assert.deepEqual(model.modelEfforts({}, caps, "c"), ["low", "medium"]);
  assert.deepEqual(model.modelEfforts({ efforts: ["x"] }, caps, "b"), ["x"]);
});

test("legacy catalogs with only model ids still populate the picker", () => {
  const options = model.modelOptions({}, { models: ["one", "two"] });
  assert.deepEqual(options.map((item) => [item.id, item.source]), [["one", "detected"], ["two", "detected"]]);
  const info = model.modelSourceSummary({ models_source: "detected",
    models_methods: ["config:~/.codex/config.toml", "cli:codex debug models"], models_checked_at: "2026-11-04T00:00:00Z" });
  assert.equal(info.kind, "cli");
  assert.equal(info.via, "codex debug models");
});

test("model option labels show config origin and effective rank; endpoint models group as available", () => {
  const label = model.modelOptionLabel;
  assert.equal(label({ id: "claude-sonnet-5-5", source: "detected", default: true, rank: "effective",
    origin_key: "ANTHROPIC_MODEL", origin_file: "~/.claude/settings.json" }),
  "claude-sonnet-5-5 (ANTHROPIC_MODEL in ~/.claude/settings.json · in effect) · default");
  assert.equal(label({ id: "x", rank: "overridden", origin_key: "ANTHROPIC_MODEL", origin_file: "" }),
    "x (ANTHROPIC_MODEL (environment) · overridden)");
  assert.equal(label({ id: "h", rank: "alias_target", origin_key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", origin_file: "~/.claude/settings.json" }),
    "h (ANTHROPIC_DEFAULT_HAIKU_MODEL in ~/.claude/settings.json · alias mapping)");
  assert.equal(label({ id: "claude-opus-5-5", label: "claude-opus-5-5", source: "builtin" }), "claude-opus-5-5");
  assert.equal(label({ id: "gpt-5", label: "GPT 5" }), "gpt-5 — GPT 5");
  const zh = (key, vars = {}) => ({ "tasks.models.rank.effective": "生效中", "tasks.models.default_tag": "默认",
    "tasks.models.origin_file": `${vars.file} 中的 ${vars.key}` })[key] ?? key;
  assert.equal(label({ id: "m", default: true, rank: "effective", origin_key: "model", origin_file: "~/.claude/settings.json" }, { t: zh }),
    "m (~/.claude/settings.json 中的 model · 生效中) · 默认");
  const picker = model.modelPickerGroups(model.modelOptions({}, { model_options: [
    { id: "gemini-4-argon", source: "available" }, { id: "claude-opus-5-5", source: "detected", default: true },
  ] }));
  assert.deepEqual(picker.groups.map((group) => group.source), ["detected", "available"]);
});

// ------------------------------------------------- session header / properties
test("the session header keeps only the live state; Stop only while a turn runs", () => {
  const busy = { id: "s_h", status: "idle", capabilities: { stream_input: true }, busy: { subagents: 1, background: 2 } };
  const head = model.sessionHeaderModel(busy, { usage: { total_tokens: 466000, estimated: true } });
  assert.deepEqual(Object.keys(head).sort(), [...model.HEADER_FIELDS].sort(), "no provider, access, cwd, id or times");
  assert.equal(head.state, "running");
  assert.deepEqual(head.busy.map((r) => r.key), ["session.busy.subagents_one", "session.busy.background_n"]);
  assert.equal(head.tokens, 466000);
  assert.equal(head.estimated, true);
  assert.equal(head.stop, false, "between turns: no Stop in the header (the menu has it)");
  const turn = model.sessionHeaderModel({ id: "s_t", status: "running", capabilities: { stream_input: true } });
  assert.equal(turn.stop, true);
  assert.equal(turn.tokens, null);
  const quiet = model.sessionHeaderModel({ id: "s_q", status: "idle", capabilities: { stream_input: true } });
  assert.equal(quiet.state, "done", "the turn ended: done, waiting for the person");
  assert.deepEqual(quiet.busy, []);
});

test("pending approvals and errors are the header state", () => {
  const live = { id: "s_a", status: "running", capabilities: { stream_input: true } };
  const head = model.sessionHeaderModel(live, { pending: 2 });
  assert.equal(head.state, "approval");
  assert.equal(head.pending, 2);
  assert.equal(model.headerStateKey("approval"), "session.state.approval");
  assert.equal(model.headerStateKey("errored"), "tasks.status.errored");
  const ended = model.sessionHeaderModel({ id: "s_e", status: "errored" }, { pending: 1 });
  assert.equal(ended.state, "errored", "an ended session has nothing to approve");
  assert.equal(ended.pending, 0);
  assert.equal(ended.stop, false);
});

test("what left the header is in Session properties", () => {
  const session = {
    id: "s_20261122073534e2d3f4", platform: "claude", model: "claude-opus", effort: "high", access_mode: "full_access",
    permission_label: "full access", turn: 3, agent: "reviewer", project: { name: "Paper revision" }, cwd: ".",
    capabilities: { supports_approval: false, permission_modes: ["read_only", "full_access"] },
  };
  const facts = model.sessionInfoFacts(session, { platform: "Claude Code", cwd: "workspace root" });
  const byId = Object.fromEntries(facts.map((f) => [f.id, f]));
  for (const id of ["session_id", "platform", "model", "effort", "access", "turn", "approvals", "agent", "project", "workspace"]) {
    assert.ok(byId[id], `${id} is listed`);
  }
  assert.equal(byId.session_id.value, session.id);
  assert.equal(byId.session_id.copy, true, "the id has a copy button");
  assert.equal(byId.session_id.mono, true);
  assert.equal(byId.workspace.mono, true, "paths in monospace");
  assert.equal(byId.workspace.value, "workspace root");
  assert.equal(byId.platform.value, "Claude Code");
  assert.equal(byId.platform.mono, undefined, "words in the UI font");
  assert.equal(byId.access.value, "Full access", "localised mode label, sentence case, not the server's raw label");
  assert.equal(byId.access.tone, "full_access");
  assert.equal(byId.effort.value, "High");
  assert.equal(byId.turn.num, true);
  const zh = model.sessionInfoFacts(session, { t: (key) => ({ "session.perm.full_access": "完全访问" }[key] || key) });
  assert.equal(zh.find((f) => f.id === "access").value, "完全访问");
  const bare = model.sessionInfoFacts({ status: "stopped" });
  assert.equal(bare.some((f) => f.id === "session_id" || f.id === "model" || f.id === "parent"), false, "only what the session has");
});
