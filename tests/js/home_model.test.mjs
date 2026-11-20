import assert from "node:assert/strict";
import { test } from "node:test";
import {
  agentConnection, checklistCard, checklistItems, checklistRestorable, CHECKLIST_DONE_MS, contentNodes, dismissChecklist, restoreChecklist, LIVE_MINUTES, NEW_PROJECT_NODES,
  runningAgents, shouldShowWelcome, STARTER_TOKEN_LABEL, tutorialChapters, tutorialTopic,
} from "../../ark_studio/web/js/home-model.js";

const NOW = Date.parse("2026-11-16T12:00:00Z");
const ago = (minutes) => new Date(NOW - minutes * 60000).toISOString();
const starter = (extra = {}) => ({ id: "t1", label: `${STARTER_TOKEN_LABEL} – 2026-11-16`, created_at: ago(5),
  last_used_at: null, expired: false, ...extra });

test("agent connection: none, waiting, signed in, connected, quiet", () => {
  assert.equal(agentConnection({ now: NOW }).state, "none");
  assert.equal(agentConnection({ agents: { first_at: null, recent: [] }, tokens: [], now: NOW }).state, "none");
  // Only Home starter tokens that are still valid count.
  assert.equal(agentConnection({ tokens: [{ label: "CI bot", created_at: ago(1) }], now: NOW }).state, "none");
  assert.equal(agentConnection({ tokens: [starter({ expired: true })], now: NOW }).state, "none");
  const waiting = agentConnection({ tokens: [starter()], now: NOW });
  assert.equal(waiting.state, "waiting");
  assert.equal(waiting.at, ago(5));
  const signed = agentConnection({ tokens: [starter(), starter({ id: "t2", last_used_at: ago(2) })], now: NOW });
  assert.deepEqual([signed.state, signed.at], ["signed_in", ago(2)]);
  // A write in this project wins over any token state.
  const agents = { first_at: ago(600), recent: [
    { actor: "agent:local/claude-code", writes: 7, last_at: ago(3) },
    { actor: "agent:local/codex", writes: 2, last_at: ago(90) },
  ] };
  const live = agentConnection({ agents, tokens: [starter()], now: NOW });
  assert.deepEqual([live.state, live.actor, live.writes, live.live, live.agents],
    ["connected", "agent:local/claude-code", 9, true, 2]);
  const old = agentConnection({ agents: { first_at: ago(600), recent: [{ actor: "agent:a", writes: 1, last_at: ago(LIVE_MINUTES + 1) }] }, now: NOW });
  assert.equal(old.live, false);
  assert.equal(agentConnection({ agents: { first_at: ago(99999), recent: [] }, now: NOW }).state, "quiet");
});

test("agent connection counts only this project's starter tokens (UX-18 labels)", () => {
  const mine = starter({ label: `${STARTER_TOKEN_LABEL} (prj_a) – 2026-11-16` });
  const other = starter({ id: "t2", label: `${STARTER_TOKEN_LABEL} (prj_b) – 2026-11-16`, last_used_at: ago(1) });
  assert.equal(agentConnection({ tokens: [other], projectId: "prj_a", now: NOW }).state, "none");
  assert.equal(agentConnection({ tokens: [mine, other], projectId: "prj_a", now: NOW }).state, "waiting");
  assert.equal(agentConnection({ tokens: [mine, other], projectId: "prj_b", now: NOW }).state, "signed_in");
  // Older unscoped starter tokens still count everywhere; no project id means all.
  assert.equal(agentConnection({ tokens: [starter()], projectId: "prj_a", now: NOW }).state, "waiting");
  assert.equal(agentConnection({ tokens: [other], now: NOW }).state, "signed_in");
});

test("running agents are the ones that wrote in the last 30 minutes", () => {
  const agents = { recent: [
    { actor: "agent:a", writes: 3, last_at: ago(1) },
    { actor: "agent:b", writes: 3, last_at: ago(LIVE_MINUTES + 5) },
    { actor: "agent:c", writes: 1, last_at: "not a date" },
  ] };
  assert.deepEqual(runningAgents(agents, NOW).map((a) => a.actor), ["agent:a"]);
  assert.deepEqual(runningAgents(null, NOW), []);
});

test("getting-started card: new non-demo projects only, topics not counted", () => {
  const project = { id: "p", demo: false };
  const open = [{ id: "question", done: false }, { id: "agent", done: false }];
  assert.equal(contentNodes({ total: 9, by_type: { topic: 4 } }), 5);
  const fresh = checklistCard({ project, stats: { total: 1 }, items: open, now: NOW });
  assert.equal(fresh.show, true);
  assert.equal(fresh.done, false);
  assert.equal(fresh.record.since, new Date(NOW).toISOString(), "the start is stored");
  assert.equal(checklistCard({ project, stats: { total: NEW_PROJECT_NODES + 3, by_type: { topic: 4 } }, items: open, now: NOW }).show, true);
  // An established project, a demo, no project or no stats yet: nothing shown or stored.
  for (const args of [
    { project, stats: { total: NEW_PROJECT_NODES } },
    { project: { id: "d", demo: true }, stats: { total: 1 } },
    { project: null, stats: { total: 1 } },
    { project, stats: null },
  ]) {
    assert.deepEqual(checklistCard({ ...args, items: open, now: NOW }), { show: false, done: false, record: null });
  }
  // Once started it stays while the agent fills the project.
  const grown = checklistCard({ project, stats: { total: 200 }, items: open, record: fresh.record, now: NOW + 1000 });
  assert.equal(grown.show, true);
  assert.equal(grown.record, null, "nothing new to store");
});

test("getting-started card: closing it hides it for good", () => {
  const project = { id: "p" };
  const items = [{ id: "question", done: false }];
  const record = dismissChecklist({ since: new Date(NOW).toISOString() }, NOW);
  assert.ok(record.dismissed);
  assert.equal(checklistCard({ project, stats: { total: 1 }, items, record, now: NOW }).show, false);
  assert.equal(checklistCard({ project, stats: { total: 0 }, items, record: dismissChecklist(null, NOW), now: NOW }).show, false);
});

test("getting-started card: a closed card can be brought back (Undo, Show getting started)", () => {
  const project = { id: "p" };
  const items = [{ id: "question", done: false }];
  const started = checklistCard({ project, stats: { total: 1 }, items, now: NOW }).record;
  assert.equal(checklistRestorable({ project, record: started }), false, "nothing to restore while shown");
  const closed = dismissChecklist(started, NOW);
  assert.equal(checklistRestorable({ project, record: closed }), true);
  assert.equal(checklistRestorable({ project: { id: "d", demo: true }, record: closed }), false);
  assert.equal(checklistRestorable({ project, record: { ...closed, expired: new Date(NOW).toISOString() } }), false);
  const back = restoreChecklist(closed);
  assert.equal("dismissed" in back, false);
  assert.equal(back.since, started.since, "the start is kept");
  // Restored on a project that has grown since: still shown.
  assert.equal(checklistCard({ project, stats: { total: 300 }, items, record: back, now: NOW }).show, true);
  assert.deepEqual(restoreChecklist(null), {});
});

test("getting-started card: done state, then gone 3 days after the last step", () => {
  const project = { id: "p" };
  const done = [{ id: "question", done: true }, { id: "agent", done: true }, { id: "invite", done: false, optional: true }];
  const started = { since: new Date(NOW - 86400000).toISOString() };
  const first = checklistCard({ project, stats: { total: 4 }, items: done, record: started, now: NOW });
  assert.equal(first.show, true);
  assert.equal(first.done, true, "optional steps do not count");
  assert.equal(first.record.completedAt, new Date(NOW).toISOString());
  const later = checklistCard({ project, stats: { total: 30 }, items: done, record: first.record, now: NOW + CHECKLIST_DONE_MS - 1 });
  assert.equal(later.show, true);
  assert.equal(later.record, null, "completion time kept");
  const gone = checklistCard({ project, stats: { total: 30 }, items: done, record: first.record, now: NOW + CHECKLIST_DONE_MS });
  assert.equal(gone.show, false);
  assert.ok(gone.record.expired, "expiry is stored");
  // Expired stays expired, even if a step is undone or the project is emptied.
  const open = [{ id: "question", done: false }];
  assert.equal(checklistCard({ project, stats: { total: 0 }, items: open, record: gone.record, now: NOW + 2 * CHECKLIST_DONE_MS }).show, false);
  // A step undone before expiry clears the completion time.
  const undone = checklistCard({ project, stats: { total: 4 }, items: open, record: first.record, now: NOW + 1000 });
  assert.equal(undone.show, true);
  assert.equal(undone.done, false);
  assert.equal("completedAt" in undone.record, false);
});

test("checklist items tick from existing data", () => {
  const empty = checklistItems({ stats: { total: 1, by_type: { note: 1 } } });
  assert.deepEqual(empty.map((i) => [i.id, i.done]), [["question", false], ["agent", false], ["plan", false], ["accept", false]]);
  const busy = checklistItems({
    stats: { by_type: { question: 1, task: 2 }, by_status: { done: 1 } },
    connection: { state: "connected" }, team: true,
    members: [{ username: "a", member: true }, { username: "b", member: true }],
  });
  assert.deepEqual(busy.map((i) => [i.id, i.done]), [["question", true], ["agent", true], ["plan", true], ["accept", true], ["invite", true]]);
  assert.equal(busy.at(-1).optional, true);
  // A token that was only used ("signed in") is not a connected agent yet.
  assert.equal(checklistItems({ connection: { state: "signed_in" } })[1].done, false);
  // An idea counts as the written-down question too.
  assert.equal(checklistItems({ stats: { by_type: { idea: 1 } } })[0].done, true);
});

test("the Welcome shows once, only on an example or an empty project", () => {
  assert.equal(shouldShowWelcome({ project: { demo: true }, stats: { total: 88 } }), true);
  assert.equal(shouldShowWelcome({ project: { demo: false }, stats: { total: 1 } }), true);
  assert.equal(shouldShowWelcome({ project: { demo: false }, stats: { total: 5, by_type: { topic: 4 } } }), true);
  assert.equal(shouldShowWelcome({ project: { demo: false }, stats: { total: 6 } }), false);
  assert.equal(shouldShowWelcome({ stored: "2026-11-16", project: { demo: true }, stats: {} }), false);
  assert.equal(shouldShowWelcome({ stored: "unavailable", project: { demo: true } }), false);
  assert.equal(shouldShowWelcome({ project: null }), false);
});

test("tutorial chapters in seq order", () => {
  const units = [
    { uid: "S-0003", title: "Research: x", tags: ["tutorial:chapter", "seq:02"] },
    { uid: "N-0001", title: "Note", tags: [] },
    { uid: "S-0001", title: "Start here: y", tags: ["seq:00", "tutorial:chapter"] },
    { uid: "S-0002", title: "Home: z", tags: ["tutorial:chapter", "seq:01"] },
  ];
  assert.deepEqual(tutorialChapters(units).map((u) => u.uid), ["S-0001", "S-0002", "S-0003"]);
  assert.deepEqual(tutorialChapters(null), []);
});

test("tutorial chapters follow the UI language", () => {
  const units = [
    { uid: "S-0002", title: "Home: z", tags: ["tutorial:chapter", "seq:01", "lang:en"] },
    { uid: "S-0001", title: "Start here: y", tags: ["tutorial:chapter", "seq:00", "lang:en"] },
    { uid: "S-0011", title: "首页：z", tags: ["tutorial:chapter", "seq:01", "lang:zh"] },
    { uid: "S-0010", title: "从这里开始：y", tags: ["tutorial:chapter", "seq:00", "lang:zh"] },
  ];
  assert.deepEqual(tutorialChapters(units, "zh").map((u) => u.uid), ["S-0010", "S-0011"]);
  assert.deepEqual(tutorialChapters(units, "en").map((u) => u.uid), ["S-0001", "S-0002"]);
  assert.deepEqual(tutorialChapters(units).map((u) => u.uid), ["S-0001", "S-0002"]);
  // A language without chapters, or a tutorial built before lang: tags, shows English.
  assert.deepEqual(tutorialChapters(units, "fr").map((u) => u.uid), ["S-0001", "S-0002"]);
  const old = [{ uid: "S-0001", title: "Start here", tags: ["tutorial:chapter", "seq:00"] }];
  assert.deepEqual(tutorialChapters(old, "zh").map((u) => u.uid), ["S-0001"]);
});

test("tutorial topic in the UI language", () => {
  const units = [
    { uid: "TP-0001", title: "Topic: warm-up", tags: ["tutorial:example"] },
    { uid: "TP-0002", title: "Tutorial", tags: ["tutorial:topic", "lang:en"] },
    { uid: "TP-0003", title: "教程", tags: ["tutorial:topic", "lang:zh"] },
  ];
  assert.equal(tutorialTopic(units, "zh").uid, "TP-0003");
  assert.equal(tutorialTopic(units, "en").uid, "TP-0002");
  assert.equal(tutorialTopic(units, "fr").uid, "TP-0002");
  assert.equal(tutorialTopic(units.slice(0, 1), "zh"), null);
  assert.equal(tutorialTopic(null), null);
});
