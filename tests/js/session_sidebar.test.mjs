// Run: node --experimental-vm-modules --test tests/js/session_sidebar.test.mjs
// The Sessions sidebar view: its view model (stored state, switcher, context
// candidates, windowing, paint throttling), the shared session stream (one
// subscription per session) and the sub-agent / background-task model.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load(name) {
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  await module.link(() => { throw new Error(`${name} must stay dependency-free`); });
  await module.evaluate();
  return module.namespace;
}

const model = await load("session-sidebar-model.js");
const streamModel = await load("session-stream-model.js");
const activityModel = await load("session-activity-model.js");
const sessionModel = await load("session-model.js");
const workbench = await load("workbench-model.js");
const fixture = JSON.parse(await readFile(new URL("./fixtures/claude_subagents_transcript.json", import.meta.url), "utf8"));

function memoryStore() {
  const data = new Map();
  return { getItem: (key) => data.get(key) ?? null, setItem: (key, value) => data.set(key, String(value)), data };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

// ------------------------------------------------------------ registration
test("Sessions in the left bar, Session properties beside Details on the right", () => {
  const def = workbench.VIEW_DEFAULTS.find((view) => view.id === "sessions");
  assert.deepEqual(def, { id: "sessions", container: "left", icon: "message", titleKey: "workbench.view.sessions" });
  const props = workbench.VIEW_DEFAULTS.find((view) => view.id === "sessionprops");
  assert.deepEqual(props, { id: "sessionprops", container: "right", icon: "cpu", titleKey: "workbench.view.sessionprops" });
  const layout = workbench.builtinPresetLayout("default");
  assert.ok(layout.containers.left.views.includes("sessions"));
  assert.deepEqual(layout.containers.right.views, ["details", "sessionprops"]);
  assert.equal(layout.containers.right.active, "details");
});

test("layout per tab: the Sessions view lists sessions on Execute › Runs; properties apply there", () => {
  const runs = { tab: "tasks", taskView: "sessions" };
  assert.equal(model.sessionViewMode(runs), "list");
  assert.equal(model.sessionViewMode({ tab: "map" }), "conversation");
  assert.equal(model.sessionViewMode({ tab: "tasks", taskView: "board" }), "conversation");
  assert.equal(model.sessionViewMode(runs, { shell: false }), "conversation", "a narrow window keeps the page's own list");
  assert.equal(model.sessionPropsApplies(runs), true);
  assert.equal(model.sessionPropsApplies({ tab: "map" }), false, "the node inspector elsewhere");
});

test("dock side, width and open state persist with the workbench layout", () => {
  let layout = workbench.builtinPresetLayout("default");
  layout = workbench.placeView(layout, "sessions", "left", { anchor: "filters", side: "before" });
  layout = workbench.setContainerSize(layout, "left", 410);
  layout = workbench.activateView(layout, "sessions");
  const restored = workbench.parseWorkbench(workbench.serializeWorkbench(layout));
  assert.equal(workbench.containerOf(restored, "sessions"), "left");
  assert.equal(restored.containers.left.size, 410);
  assert.equal(restored.containers.left.visible, true);
  assert.equal(workbench.isViewShown(restored, "sessions"), true);
  const closed = workbench.parseWorkbench(workbench.serializeWorkbench(workbench.setContainerVisible(restored, "left", false)));
  assert.equal(workbench.isViewShown(closed, "sessions"), false, "a closed sidebar stays closed");
  // Moved to the bottom panel and back to the right: still one place.
  let moved = workbench.moveView(restored, "sessions", "bottom");
  moved = workbench.moveView(moved, "sessions", "right");
  const all = workbench.CONTAINERS.flatMap((name) => moved.containers[name].views);
  assert.equal(all.filter((id) => id === "sessions").length, 1);
});

// -------------------------------------------------------------- stored state
test("sidebar prefs: the open session and the provider/model for a new one", () => {
  const store = memoryStore();
  assert.deepEqual(model.loadSidebarPrefs(store), { sessionId: "", platform: "", model: "", byProject: {} });
  model.saveSidebarPrefs({ sessionId: "s_1" }, store);
  model.rememberSessionChoice({ platform: "claude", model: "opus" }, store);
  assert.deepEqual(model.loadSidebarPrefs(store), { sessionId: "s_1", platform: "claude", model: "opus", byProject: {} });
  model.rememberSessionChoice({ platform: "", model: "x" }, store);
  assert.equal(model.loadSidebarPrefs(store).platform, "claude", "an empty choice keeps the last one");
  store.setItem(model.SIDEBAR_STORE_KEY, "{broken");
  assert.deepEqual(model.loadSidebarPrefs(store), { sessionId: "", platform: "", model: "", byProject: {} });
  assert.equal(model.normalizeSidebarPrefs({ sessionId: "x".repeat(200) }).sessionId.length, 80);
});

test("sidebar session per project: a switch back reopens that project's session", () => {
  const store = memoryStore();
  let prefs = model.loadSidebarPrefs(store);
  prefs = model.saveSidebarPrefs(model.withProjectSession(prefs, "pA", "a1"), store);
  prefs = model.saveSidebarPrefs(model.withProjectSession(prefs, "pB", "b1"), store);
  assert.deepEqual(model.loadSidebarPrefs(store).byProject, { pA: "a1", pB: "b1" });
  const listA = [{ id: "a1" }, { id: "a2" }];
  // Back in A after B: A's session, not B's (gone from this list) or a new one.
  assert.equal(model.sessionForList(listA, { selected: "b1", prefs, project: "pA", switched: true }), "a1");
  // A switch replaces even a session id the other project happens to list.
  assert.equal(model.sessionForList(listA, { selected: "a2", prefs, project: "pA", switched: true }), "a1");
  // No switch: the open session stays while it exists.
  assert.equal(model.sessionForList(listA, { selected: "a2", prefs, project: "pA" }), "a2");
  // The remembered session ended and was deleted: a new session.
  assert.equal(model.sessionForList([{ id: "a2" }], { selected: "b1", prefs, project: "pA", switched: true }), "");
  // First visit of a project: a new session.
  assert.equal(model.sessionForList(listA, { selected: "b1", prefs, project: "pC", switched: true }), "");
  // Capped: the newest projects stay.
  let many = model.normalizeSidebarPrefs({});
  for (let i = 0; i < model.MAX_PROJECT_SESSIONS + 5; i++) many = model.normalizeSidebarPrefs({ ...many, ...model.withProjectSession(many, `p${i}`, `s${i}`) });
  assert.equal(Object.keys(many.byProject).length, model.MAX_PROJECT_SESSIONS);
  assert.equal(many.byProject[`p${model.MAX_PROJECT_SESSIONS + 4}`], `s${model.MAX_PROJECT_SESSIONS + 4}`);
});

test("provider pick: the remembered one when available, else the first available", () => {
  const presets = [{ id: "echo", available: true }, { id: "claude", available: true }, { id: "codex", available: false }];
  assert.equal(model.pickPreset(presets, "claude").id, "claude");
  assert.equal(model.pickPreset(presets, "codex").id, "echo");
  assert.equal(model.pickPreset([], "claude"), null);
});

test("switcher: running sessions first, then by activity; archived left out; the open one kept", () => {
  const sessions = [
    { id: "a", status: "stopped", last_activity_at: "2026-11-19T10:00:00Z" },
    { id: "b", status: "running", last_activity_at: "2026-11-18T10:00:00Z" },
    { id: "c", status: "stopped", last_activity_at: "2026-11-19T12:00:00Z" },
    { id: "d", status: "archived", last_activity_at: "2026-11-19T13:00:00Z" },
  ];
  assert.deepEqual(model.recentSessions(sessions).map((s) => s.id), ["b", "c", "a"]);
  assert.deepEqual(model.recentSessions(sessions, 1, "a").map((s) => s.id), ["b", "a"]);
  assert.equal(model.sidebarSelection(sessions, "c"), "c");
  assert.equal(model.sidebarSelection(sessions, "gone"), model.NEW_SESSION);
});

// ------------------------------------------------------------------ context
test("context candidates: the open node, the editor tab, then the map selection", () => {
  const units = [
    { id: 1, uid: "T-0001", title: "One", type: "task" },
    { id: 2, uid: "I-0002", title: "Two", type: "idea" },
    { id: 3, uid: "C-0003", title: "Three", type: "claim" },
    { id: 4, uid: "N-0004", title: "Gone", type: "note", archived: true },
  ];
  const got = model.contextCandidates({
    detail: units[0], drawerOpen: true, selectedId: 2, units, tab: "editor", editorKey: "unit:C-0003",
    graphSelections: [[2, 4], [1]],
  });
  assert.deepEqual(got.map((c) => [c.uid, c.source]), [["T-0001", "open"], ["C-0003", "open"], ["I-0002", "selected"]]);
  assert.deepEqual(model.contextCandidates({ detail: units[0], drawerOpen: false, selectedId: null, units }), [],
    "a closed inspector is not context");
  assert.deepEqual(model.pendingUnitContext(got[0]), {
    id: "unit:T-0001", kind: "unit", uid: "T-0001", label: "T-0001 · One", attachment: { kind: "unit", uid: "T-0001" },
  });
});

test("shared context: attached in one view, seen in the other; listeners hear each change", () => {
  const stream = streamModel.createSessionStream({ listSessions: async () => ({}), fetchTranscript: async () => ({}), setTimer: () => 1, clearTimer: () => {} });
  const heard = [];
  const off = stream.subscribeContext((id) => heard.push(id));
  stream.contexts.set("s1", [{ id: "ctx-1", kind: "unit", label: "T-0001 · One" }]);
  stream.contexts.delete("s1");
  stream.contexts.delete("s1");
  off();
  stream.contexts.set("s2", []);
  assert.deepEqual(heard, ["s1", "s1"], "set and an actual delete notify; a no-op delete does not");
});

// ------------------------------------------------------------ the stream
test("one subscription per session: two views, one list request per tick, one transcript request", async () => {
  let ticks = [];
  let t = 1000;
  const calls = { list: 0, transcript: 0 };
  const stream = streamModel.createSessionStream({
    listSessions: async () => { calls.list += 1; return { sessions: [{ id: "s1", status: "running", last_activity_at: String(calls.list) }] }; },
    fetchTranscript: async (id) => { calls.transcript += 1; return { session: id, signature: `sig${calls.transcript}`, items: [] }; },
    now: () => t, setTimer: (fn) => { ticks.push(fn); return ticks.length; }, clearTimer: () => { ticks = []; },
  });
  const pageLists = [], sidebarLists = [];
  const offPage = stream.subscribeSessions((data) => pageLists.push(data));
  const offSidebar = stream.subscribeSessions((data) => sidebarLists.push(data));
  await flush();
  assert.equal(calls.list, 1, "both views share the first request");
  assert.equal(ticks.length, 1, "one timer for both");
  const pageTranscripts = [], sidebarTranscripts = [];
  const offA = stream.subscribeTranscript("s1", (data) => pageTranscripts.push(data.signature));
  const offB = stream.subscribeTranscript("s1", (data) => sidebarTranscripts.push(data.signature));
  await flush();
  assert.equal(calls.transcript, 1, "the second view reuses the request in flight");
  assert.equal(stream.transcriptFollowers("s1"), 2);
  t += 2000;
  await ticks[0]();
  await flush();
  assert.equal(calls.list, 2);
  assert.equal(calls.transcript, 2, "a running session's transcript is refreshed once per tick");
  assert.deepEqual(pageTranscripts, sidebarTranscripts);
  assert.equal(pageLists.length, sidebarLists.length);
  offA(); offB();
  assert.equal(stream.transcriptFollowers("s1"), 0);
  offPage(); offSidebar();
  assert.equal(stream.polling(), false, "the timer stops with the last subscriber");
});

test("stream: inactive subscribers do not poll; an unchanged transcript is not re-delivered", async () => {
  const calls = { list: 0, transcript: 0 };
  let visible = false;
  let tick = null;
  let t = 0;
  const stream = streamModel.createSessionStream({
    listSessions: async () => { calls.list += 1; return { sessions: [{ id: "s1", status: "stopped" }] }; },
    fetchTranscript: async () => { calls.transcript += 1; return { signature: "same", items: [] }; },
    now: () => t, setTimer: (fn) => { tick = fn; return 1; }, clearTimer: () => {},
  });
  stream.subscribeSessions(() => {}, { active: () => visible, immediate: false });
  await tick();
  assert.equal(calls.list, 0, "a hidden view does not poll");
  visible = true;
  await tick();
  assert.equal(calls.list, 1);
  const seen = [];
  stream.subscribeTranscript("s1", (data) => seen.push(data.signature));
  await flush();
  t += 5000;
  await stream.transcript("s1");
  assert.deepEqual(seen, ["same"], "same signature, no repaint");
  t += 100;
  await stream.transcript("s1", { maxAge: 700 });
  assert.equal(calls.transcript, 2, "a fresh result is shared within maxAge");
  stream.patchSession({ id: "s1", status: "running" });
  assert.equal(stream.latestSessions().sessions[0].status, "running");
});

// ----------------------------------------------------------- thread window
test("long transcripts render a window; Show earlier widens it", () => {
  const blocks = Array.from({ length: 200 }, (_, i) => ({ kind: i % 10 === 0 ? "separator" : "assistant", key: `b${i}` }));
  const first = model.windowBlocks(blocks, 80);
  assert.equal(first.blocks.length + first.hidden, 200);
  assert.ok(first.blocks.length <= 80);
  assert.notEqual(first.blocks[0].kind, "separator");
  assert.deepEqual(model.windowBlocks(blocks.slice(0, 5), 80), { blocks: blocks.slice(0, 5), hidden: 0 });
});

test("composer action: create, steer, continue or stop", () => {
  assert.equal(model.composerAction(null, { hasContent: true }), "create");
  assert.equal(model.composerAction(null), "create", "a new session may start without a message");
  assert.equal(model.composerAction({ status: "running" }), "stop");
  assert.equal(model.composerAction({ status: "running" }, { hasContent: true }), "steer");
  assert.equal(model.composerAction({ status: "stopped" }, { hasContent: true }), "continue");
  assert.equal(model.newSessionTitle("\n  Fix the login bug in T-0042\nmore"), "Fix the login bug in T-0042");
  assert.equal(model.newSessionTitle("x".repeat(100)).length, 60);
});

test("paint scheduler: many updates, one paint per interval; nothing while hidden", () => {
  let now = 0;
  const frames = [];
  const delays = [];
  let visible = true;
  let paints = 0;
  const scheduler = model.createPaintScheduler({
    paint: () => { paints += 1; }, visible: () => visible, interval: 200, now: () => now,
    frame: (fn) => frames.push(fn), delay: (fn, ms) => delays.push([fn, ms]),
  });
  for (let i = 0; i < 50; i += 1) scheduler.request();
  assert.equal(frames.length, 1, "coalesced into one frame");
  frames.shift()();
  assert.equal(paints, 1);
  now = 50;
  scheduler.request();
  scheduler.request();
  frames.shift()();
  assert.equal(paints, 1, "throttled");
  assert.equal(delays[0][1], 150);
  now = 200;
  delays.shift()[0]();
  frames.shift()();
  assert.equal(paints, 2);
  visible = false;
  now = 1000;
  scheduler.request();
  frames.shift()();
  assert.equal(paints, 2, "hidden: no paint");
  assert.equal(scheduler.pending(), true);
  visible = true;
  scheduler.flush();
  frames.shift()();
  assert.equal(paints, 3, "painted when shown again");
});

// ----------------------------------------------- sub-agents and background
const running = { id: "s1", status: "running" };
const clone = (value) => JSON.parse(JSON.stringify(value));
const byKey = (activity) => Object.fromEntries(activity.entries.map((entry) => [entry.key, entry]));

test("activity from a Claude Code stream: sub-agents and background tasks are two kinds", () => {
  const activity = activityModel.sessionActivity(fixture.items, { session: running, now: Date.parse("2026-11-19T10:02:00Z") });
  const keys = byKey(activity);
  assert.deepEqual(activity.subagents.map((e) => e.itemId), ["toolu_A", "toolu_B", "toolu_F"]);
  assert.deepEqual(activity.background.map((e) => e.itemId), ["toolu_C", "toolu_D"]);
  assert.equal(keys["tool:toolu_G"], undefined, "a foreground command is neither");
  const a = keys["tool:toolu_A"];
  assert.deepEqual([a.label, a.detail, a.status, a.tokens], ["Survey session code", "Explore", "completed", 2570]);
  assert.equal(keys["tool:toolu_B"].status, "running");
  assert.equal(keys["tool:toolu_B"].model, "sonnet");
  assert.deepEqual([keys["tool:toolu_C"].id, keys["tool:toolu_C"].status], ["bash_1", "stopped"], "KillShell stops it");
  assert.deepEqual([keys["tool:toolu_D"].id, keys["tool:toolu_D"].status], ["bash_2", "completed"], "moved to the background, then finished");
  assert.match(keys["tool:toolu_D"].lastOutput, /exit code 0/);
  const f = keys["tool:toolu_F"];
  assert.deepEqual([f.kind, f.background, f.id, f.status], ["subagent", true, "agent_77", "failed"], "a background agent is a sub-agent");
  assert.equal(activity.runningSubagents, 1);
  assert.equal(activity.runningBackground, 0);
  assert.equal(activityModel.activityList(activity, "subagent")[0].itemId, "toolu_B", "running first");
});

test("chips count while work runs; nothing runs once the session ended", () => {
  const items = clone(fixture.items.slice(0, fixture.items.findIndex((item) => item.id === "toolu_E")));
  items.find((item) => item.id === "toolu_D").task.status = "running";
  const activity = activityModel.sessionActivity(items, { session: running });
  assert.equal(activity.runningBackground, 2, "two background commands");
  assert.equal(activity.runningSubagents, 1, "one sub-agent");
  const ended = activityModel.sessionActivity(items, { session: { id: "s1", status: "stopped" } });
  assert.equal(ended.runningBackground + ended.runningSubagents, 0);
  // The reply's activity list (items without their output) gives the same entries.
  const slim = items.map(({ output, ...rest }) => rest);
  assert.deepEqual(activityModel.sessionActivity(slim, { session: running }).entries.map((e) => [e.key, e.status]),
    activity.entries.map((e) => [e.key, e.status]));
});

test("resumed session: tasks whose call is not in the transcript", () => {
  const items = [
    { id: "m-1", type: "message", role: "user", text: "Continue" },
    { id: "k-1", type: "task", task: { id: "bash_9", type: "command", background: true, status: "running", description: "npm run watch", started_at: "2026-11-19T10:00:00Z" } },
    { id: "k-2", type: "task", task: { id: "a_3", type: "agent", status: "completed", summary: "Agent Docs finished\nwrote 3 files" } },
  ];
  const activity = activityModel.sessionActivity(items, { session: running });
  assert.deepEqual(activity.entries.map((e) => [e.id, e.kind, e.status]), [["bash_9", "background", "running"], ["a_3", "subagent", "completed"]]);
  assert.equal(activity.entries[1].lastOutput, "wrote 3 files");
  assert.deepEqual(sessionModel.threadBlocks(items).map((b) => b.kind), ["user"], "task records are not turns");
});

test("other agents: run_in_background, BashOutput status, ARK child sessions", () => {
  const items = [
    { id: "t1", type: "tool", name: "Bash", input: { command: "make", run_in_background: true }, status: "ok", output: "Command running in background with ID: b77", at: "2026-11-19T10:00:00Z" },
    { id: "t2", type: "tool", name: "BashOutput", input: { bash_id: "b77" }, status: "ok", output: "<status>running</status>\n<stdout>step 3/9</stdout>" },
    { id: "t3", type: "tool", name: "TaskOutput", input: { task_id: "b77" }, status: "ok", output: "<status>completed</status><exit_code>0</exit_code>\n<stdout>built</stdout>" },
    { id: "i1", type: "tool", name: "command", input: "ls", status: "ok", output: "x" },
  ];
  const sessions = [{ id: "c1", parent_id: "s1", title: "Child", status: "running", platform: "codex" }, { id: "c2", parent_id: "other", status: "running" }];
  const activity = activityModel.sessionActivity(items, { session: running, sessions });
  assert.deepEqual(activity.entries.map((e) => [e.key, e.kind, e.status]),
    [["tool:t1", "background", "completed"], ["session:c1", "subagent", "running"]]);
  assert.equal(activity.entries[0].lastOutput, "built");
  assert.equal(activity.runningSubagents, 1, "an ARK sub-session is a running sub-agent");
  assert.equal(activityModel.normalizeTaskStatus("killed"), "stopped");
});

test("task badge on a tool row; blockKeyFor finds an item's block", () => {
  const items = fixture.items;
  const b = activityModel.taskBadge(items.find((item) => item.id === "toolu_A"));
  assert.deepEqual([b.kind, b.status, b.tokens], ["subagent", "completed", 2570]);
  assert.equal(activityModel.taskBadge(items.find((item) => item.id === "toolu_C")).background, true);
  const blocks = sessionModel.threadBlocks(items);
  assert.match(activityModel.blockKeyFor(blocks, "toolu_D"), /^tools-/);
  assert.equal(activityModel.blockKeyFor(blocks, "zz"), "");
});

test("block signatures change with task progress, not on every poll", () => {
  const items = clone(fixture.items);
  const block = sessionModel.threadBlocks(items).find((entry) => entry.items?.some((item) => item.id === "toolu_B"));
  const before = sessionModel.blockSignature(block);
  assert.equal(sessionModel.blockSignature(block), before);
  block.items.find((item) => item.id === "toolu_B").task.last = "Reading p9.md";
  assert.notEqual(sessionModel.blockSignature(block), before);
});

// ------------------------------------------------------ composer and queue
test("keys: Enter queues and Ctrl+Enter sends now while a turn runs; both send otherwise", () => {
  const k = (extra) => ({ key: "Enter", ...extra });
  assert.equal(model.composerKeyAction(k({}), { running: true }), "queue");
  assert.equal(model.composerKeyAction(k({ ctrlKey: true }), { running: true }), "now");
  assert.equal(model.composerKeyAction(k({ metaKey: true }), { running: true }), "now");
  assert.equal(model.composerKeyAction(k({}), { running: false }), "send");
  assert.equal(model.composerKeyAction(k({ ctrlKey: true }), { running: false }), "send");
  assert.equal(model.composerKeyAction(k({ shiftKey: true }), { running: true }), "newline");
  assert.equal(model.composerKeyAction(k({ isComposing: true }), { running: true }), null);
  assert.equal(model.composerKeyAction({ key: "a" }), null);
  assert.equal(model.composerHintKey(true), "session.composer.keys_running");
  assert.equal(model.sendNowMode({ capabilities: { followup: true } }), "inject");
  assert.equal(model.sendNowMode({ platform: "claude" }), "interrupt");
});

test("images: type, size and count limits; names; context payload", () => {
  assert.deepEqual(model.acceptImage({ type: "image/png", size: 10, name: "Screen Shot 1.png" }), { ok: true, reason: "", name: "Screen-Shot-1.png" });
  assert.equal(model.acceptImage({ type: "image/jpeg", size: 10, name: "" }).name, "pasted.jpg");
  assert.equal(model.acceptImage({ type: "application/pdf", size: 10 }).reason, "type");
  assert.equal(model.acceptImage({ type: "image/png", size: model.MAX_IMAGE_BYTES + 1 }).reason, "size");
  assert.equal(model.acceptImage({ type: "image/png", size: 0 }).reason, "size");
  assert.equal(model.acceptImage({ type: "image/png", size: 5 }, model.MAX_IMAGES).reason, "count");
  assert.deepEqual(model.imageContext([{ id: "i", name: "a.png", data: "data:image/png;base64,AA", url: "blob:x" }]),
    [{ kind: "image_data", name: "a.png", data: "data:image/png;base64,AA" }]);
});

test("sub-agent tabs per session: open, activate, close, remembered", () => {
  const store = memoryStore();
  let tabs = model.openAgentTab({}, "s1", { id: "toolu_A", label: "Survey" });
  tabs = model.openAgentTab(tabs, "s1", { id: "toolu_B", label: "Tests" });
  tabs = model.openAgentTab(tabs, "s1", { id: "toolu_A", label: "Survey" });
  assert.deepEqual(tabs.s1, { tabs: [{ id: "toolu_A", label: "Survey", kind: "agent" }, { id: "toolu_B", label: "Tests", kind: "agent" }], active: "toolu_A" });
  tabs = model.activateAgentTab(tabs, "s1", "");
  assert.equal(tabs.s1.active, "", "the main conversation");
  tabs = model.activateAgentTab(tabs, "s1", "toolu_B");
  tabs = model.closeAgentTab(tabs, "s1", "toolu_B");
  assert.deepEqual(tabs.s1, { tabs: [{ id: "toolu_A", label: "Survey", kind: "agent" }], active: "toolu_A" });
  model.saveAgentTabs(tabs, store);
  assert.deepEqual(model.loadAgentTabs(store), tabs);
  assert.deepEqual(model.closeAgentTab(tabs, "s1", "toolu_A"), {});
  assert.deepEqual(model.normalizeAgentTabs({ s: { tabs: [{ id: "bad id!" }], active: "x" } }), {});
});

// ---------------------------------------------------------- paged stream
test("stream pages: tail, known signature, unchanged replies, Show earlier, sub-agent keys", async () => {
  const calls = [];
  let signature = "v1";
  const stream = streamModel.createSessionStream({
    listSessions: async () => ({ sessions: [{ id: "s1", status: "running" }] }),
    fetchTranscript: async (key, opts) => {
      calls.push([key, opts.tail, opts.known]);
      if (opts.known === signature) return { unchanged: true, signature };
      return { signature, items: [{ id: "m-1" }], offset: opts.tail > streamModel.TRANSCRIPT_TAIL ? 0 : 50 };
    },
    setTimer: () => 1, clearTimer: () => {},
  });
  const seen = [];
  stream.subscribeTranscript("s1", (data) => seen.push(data.signature));
  await flush();
  await stream.transcript("s1");
  assert.deepEqual(calls, [["s1", streamModel.TRANSCRIPT_TAIL, ""], ["s1", streamModel.TRANSCRIPT_TAIL, "v1"]], "the second request sends the signature it has");
  assert.deepEqual(seen, ["v1"], "an unchanged reply is not delivered again");
  signature = "v2";
  await stream.transcript("s1");
  assert.deepEqual(seen, ["v1", "v2"]);
  await stream.setTail("s1", 700);
  assert.deepEqual(calls.at(-1), ["s1", 700, ""], "a wider page is fetched in full");
  assert.equal(stream.cachedTranscript("s1").offset, 0);
  assert.equal(streamModel.transcriptKey("s1", "toolu_A"), "s1::toolu_A");
  assert.deepEqual(streamModel.splitTranscriptKey("s1::toolu_A"), { id: "s1", agent: "toolu_A" });
  stream.subscribeTranscript("s1::toolu_A", () => {});
  await flush();
  assert.equal(calls.at(-1)[0], "s1::toolu_A");
});

// ---------------------------------------------- long-lived Claude Code sessions
test("composer states of a long-lived Claude Code session", () => {
  const stream = (status) => ({ status, capabilities: { stream_input: true, followup: true } });
  const oneShot = (status) => ({ status, platform: "claude", capabilities: { followup: false } });
  // While a turn runs: Enter queues, Ctrl+Enter injects into the turn.
  assert.equal(model.turnRunning(stream("running")), true);
  assert.equal(model.composerKeyAction({ key: "Enter" }, { running: model.turnRunning(stream("running")) }), "queue");
  assert.equal(model.composerKeyAction({ key: "Enter", ctrlKey: true }, { running: model.turnRunning(stream("running")) }), "now");
  assert.equal(model.sendNowMode(stream("running")), "inject", "no interrupt: the message joins the running turn");
  assert.equal(model.stopLabelKey(stream("running")), "session.stop_turn");
  // Waiting for the next message: Enter sends at once; Stop ends the process.
  assert.equal(model.turnRunning(stream("idle")), false);
  assert.equal(model.composerKeyAction({ key: "Enter" }, { running: model.turnRunning(stream("idle")) }), "send");
  assert.equal(model.composerHintKey(model.turnRunning(stream("idle"))), "session.composer.keys_idle");
  assert.equal(model.stopLabelKey(stream("idle")), "session.stop_end");
  // One-shot turns (older CLI, other agents): idle still means a turn runs; send now interrupts.
  assert.equal(model.turnRunning(oneShot("idle")), true);
  assert.equal(model.sendNowMode(oneShot("running")), "interrupt");
  assert.equal(model.stopLabelKey(oneShot("running")), "tasks.stop");
  assert.equal(model.turnRunning({ status: "stopped" }), false);
  // A new session can start with an image only.
  assert.equal(model.composerAction(null, { hasContent: true }), "create");
});

test("tool rows count images without carrying their data", () => {
  const block = { kind: "tools", key: "tools-t1", items: [{ id: "t1", type: "tool", name: "Read", status: "ok", output: "a screenshot", images: 1 }] };
  const before = sessionModel.blockSignature(block);
  block.items[0].images = 2;
  assert.notEqual(sessionModel.blockSignature(block), before);
});

// ------------------------------------------------- one turn state, titles
test("one turn state drives the badge, the indicator, the composer and Stop", () => {
  const stream = (status) => ({ id: "s", status, capabilities: { stream_input: true } });
  const oneShot = (status) => ({ id: "s", status, capabilities: { followup: false } });
  const items = [{ id: "m-1", type: "message", role: "assistant", text: "Done.", streaming: false }];
  // A long-lived process after its `result`, a background task still running.
  const waiting = stream("idle");
  assert.equal(sessionModel.sessionPhase(waiting), "waiting");
  assert.equal(sessionModel.turnRunning(waiting), false);
  assert.equal(sessionModel.liveActivity(items, waiting), null, "no working indicator while it waits");
  assert.equal(sessionModel.shownStatus(waiting), "done", "the main turn ended: done (green), waiting for the person");
  assert.equal(sessionModel.shownStatus({ ...waiting, turn_end: { outcome: "error", at: "x", reason: "API Error" } }), "errored");
  assert.equal(model.composerAction(waiting), "none", "no Stop button for a turn that is not running");
  assert.equal(model.composerAction(waiting, { hasContent: true }), "steer", "the message goes straight in");
  assert.equal(model.stopLabelKey(waiting), "session.stop_end");
  // A turn (a user message, or the agent woken by a notification).
  const turn = stream("running");
  assert.equal(sessionModel.turnRunning(turn), true);
  assert.equal(sessionModel.liveActivity(items, turn).phase, "working");
  assert.equal(sessionModel.shownStatus(turn), "running");
  assert.equal(model.composerAction(turn), "stop");
  // A one-shot turn quiet for 90 s is still a running turn, not idle.
  const quiet = oneShot("idle");
  assert.equal(sessionModel.turnRunning(quiet), true);
  assert.equal(sessionModel.shownStatus(quiet), "running");
  assert.equal(sessionModel.liveActivity(items, quiet).phase, "waiting");
  assert.equal(sessionModel.sessionPhase({ status: "stopped" }), "stopped");
  assert.equal(sessionModel.turnRunning({ status: "stopped" }), false);
});

// ------------------------------------- background work keeps a session running
test("between turns, running sub-agents or background tasks keep the session running", () => {
  const waiting = (extra = {}) => ({ id: "s", status: "idle", capabilities: { stream_input: true }, ...extra });
  const bash = (id, command, status = "running") => ({ id, type: "tool", name: "Bash", status: "completed",
    input: { command, run_in_background: true }, task: { id: `sh-${id}`, type: "command", status, background: true } });
  const agent = (id, status = "running") => ({ id, type: "tool", name: "Task", status: "completed",
    input: { description: "Survey" }, task: { id: `t-${id}`, type: "agent", status, background: true } });
  // Turn ended, a sub-agent runs: running, with the reason as a sub-label.
  let activity = activityModel.sessionActivity([agent("a"), agent("b", "completed")], { session: waiting() });
  assert.equal(sessionModel.shownStatus(waiting(), activity), "running");
  assert.deepEqual(JSON.parse(JSON.stringify(sessionModel.busyReason(waiting(), activity))),
    [{ key: "session.busy.subagents_one", n: 1 }]);
  assert.equal(sessionModel.turnRunning(waiting()), false, "Enter still sends at once");
  assert.equal(sessionModel.liveActivity([], waiting()), null, "no typing indicator for background work");
  assert.equal(model.stopBackgroundMode(waiting(), sessionModel.busyWork(waiting(), activity)), true, "Stop background tasks…");
  assert.equal(model.stopBackgroundMode(waiting(), sessionModel.busyWork(waiting(), activity), { hasContent: true }), false);
  // Two background tasks, from the server's count (a list row).
  const listed = waiting({ busy: { subagents: 0, background: 2 } });
  assert.equal(sessionModel.shownStatus(listed), "running");
  assert.deepEqual(JSON.parse(JSON.stringify(sessionModel.busyReason(listed))), [{ key: "session.busy.background_n", n: 2 }]);
  // Only the ARK watcher runs: done.  The chip still shows it.
  activity = activityModel.sessionActivity([bash("w", "powershell -NoProfile -File ark-watch.ps1")], { session: waiting() });
  assert.equal(activity.runningBackground, 1);
  assert.equal(activity.busyBackground, 0);
  assert.equal(sessionModel.shownStatus(waiting(), activity), "done");
  assert.deepEqual(sessionModel.busyReason(waiting(), activity).length, 0);
  assert.equal(model.stopBackgroundMode(waiting(), sessionModel.busyWork(waiting(), activity)), false);
  // A background command and the watcher: one background task.
  activity = activityModel.sessionActivity([bash("w", "sh ark-watch.sh"), bash("b", "npm run build")], { session: waiting() });
  assert.equal(activity.busyBackground, 1);
  // Everything stopped by an error, a stop or a close: that state, never idle.
  for (const status of ["errored", "stopped", "closed"]) {
    const ended = { id: "s", status, capabilities: { stream_input: true }, busy: { subagents: 1, background: 0 } };
    activity = activityModel.sessionActivity([agent("a")], { session: ended });
    assert.equal(sessionModel.shownStatus(ended, activity), status);
    assert.equal(sessionModel.busyReason(ended, activity).length, 0);
  }
  // A running turn is just running (no sub-label).
  const turn = { id: "s", status: "running", capabilities: { stream_input: true } };
  activity = activityModel.sessionActivity([agent("a")], { session: turn });
  assert.equal(sessionModel.shownStatus(turn, activity), "running");
  assert.equal(sessionModel.busyReason(turn, activity).length, 0);
  assert.equal(model.stopBackgroundMode(turn, sessionModel.busyWork(turn, activity)), false, "Stop stops the turn");
});

test("a session without a title shows New session · provider", () => {
  assert.equal(sessionModel.sessionTitle({ title: "", platform_label: "Claude Code" }), "New session · Claude Code");
  assert.equal(sessionModel.sessionTitle({ title: "HOI survey", platform_label: "Claude Code" }), "HOI survey");
  assert.equal(sessionModel.sessionTitle({}), "Untitled session");
  const zh = (key, vars) => (key === "session.new_title" ? `新会话 · ${vars.provider}` : key);
  assert.equal(sessionModel.sessionTitle({ platform: "codex" }, { t: zh }), "新会话 · codex");
});

// ------------------------------------------- stored contexts, blocks
test("stored contexts: count matches the rows shown, sizes go to the listed sessions", () => {
  const items = [
    { id: "s_1", source: "internal", bytes: 10240 },
    { id: "s_2", source: "internal", bytes: 2048 },
    { id: "s_gone", source: "internal", bytes: 512 },
    { id: "ark-ses-x", source: "external", bytes: 4096 },
  ];
  const section = model.contextSection(items, ["s_1", "s_2"]);
  assert.deepEqual(section.rows.map((r) => r.id), ["s_gone", "ark-ses-x"], "listed ARK sessions are one row: in the list");
  assert.equal(section.count, section.rows.length, "the count is the rows shown");
  assert.equal(section.total, 10240 + 2048 + 512 + 4096, "the stored total counts ARK transcripts too");
  assert.deepEqual(section.sizes, { s_1: 10240, s_2: 2048 });
  assert.deepEqual(section.listed.map((r) => r.id), ["s_1", "s_2"]);
  // Nothing listed: every stored context is a row (never "4" over an empty list).
  const all = model.contextSection(items, []);
  assert.equal(all.count, 4);
  assert.equal(all.rows.length, 4);
});

test("queued turns: delivered messages leave the card; failed ones stay for retry", () => {
  const session = { steer_queue: [
    { id: "st_1", text: "a", status: "delivered", channel: "stdin" },
    { id: "st_2", text: "b", status: "queued", channel: "next_turn" },
    { id: "st_3", text: "c", status: "failed", error: "stdin closed" },
  ] };
  assert.deepEqual(sessionModel.pendingSteerItems(session).map((item) => item.id), ["st_2", "st_3"]);
  assert.deepEqual(sessionModel.pendingSteerItems({ steer_queue: [{ id: "x", status: "delivered" }] }), [], "nothing pending: no card");
  assert.deepEqual(sessionModel.pendingSteerItems({}), []);
});

test("collapsible blocks of the Sessions view remember their state", () => {
  const store = memoryStore();
  assert.deepEqual(model.loadBlocks(store), { list: true, groups: false, contexts: true });
  assert.equal(model.toggleBlock("groups", store).groups, true);
  assert.equal(model.toggleBlock("list", store).list, false);
  assert.deepEqual(model.loadBlocks(store), { list: false, groups: true, contexts: true });
  store.setItem(model.BLOCKS_STORE_KEY, "{bad");
  assert.deepEqual(model.loadBlocks(store), model.BLOCK_DEFAULTS);
});

test("tab groups: forks open as tabs of the session they came from", () => {
  let tabs = model.openAgentTab({}, "s_main", { id: "toolu_A", label: "Survey" });
  tabs = model.openForkTab(tabs, "s_main", { id: "s_fork1", label: "Fork · main" });
  assert.deepEqual(tabs.s_main.tabs.map((tab) => [tab.id, tab.kind]), [["toolu_A", "agent"], ["s_fork1", "session"]]);
  assert.equal(tabs.s_main.active, "s_fork1", "the new fork is focused");
  assert.equal(model.tabRoot(tabs, "s_fork1"), "s_main", "a fork shows its parent's strip");
  assert.equal(model.tabRoot(tabs, "s_main"), "s_main");
  assert.equal(model.tabRoot(tabs, "s_other"), "s_other");
  assert.deepEqual(model.activeTabOf(tabs, "s_main"), { kind: "session", id: "s_fork1" });
  tabs = model.activateAgentTab(tabs, "s_main", "toolu_A");
  assert.deepEqual(model.activeTabOf(tabs, "s_main"), { kind: "agent", id: "toolu_A" });
  tabs = model.activateAgentTab(tabs, "s_main", "");
  assert.deepEqual(model.activeTabOf(tabs, "s_main"), { kind: "main", id: "" });
  // Remembered, and closing the fork tab drops it from the group.
  assert.deepEqual(model.normalizeAgentTabs(JSON.parse(JSON.stringify(tabs))), tabs);
  tabs = model.closeAgentTab(tabs, "s_main", "s_fork1");
  assert.equal(model.tabRoot(tabs, "s_fork1"), "s_fork1");
});
