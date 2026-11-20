import assert from "node:assert/strict";
import { test } from "node:test";
import {
  pickText, requiredRules, scopeRules, draftFrom, ruleEnabled, ruleParam, setEnabled, setParam,
  addCustom, updateCustom, removeCustom, cleanDraft, estimateTokens,
} from "../../ark_studio/web/js/agent-rules-model.js";

const CATALOG = {
  rules: [
    { id: "progress-body", kind: "required", scope: "ark", title: { en: "Progress", zh: "进展" } },
    { id: "plan-approval", kind: "preference", scope: "personal", default: true, title: { en: "Plan", zh: "计划" } },
    { id: "language", kind: "preference", scope: "personal", default: true,
      params: [{ id: "records_language", default: "auto", choices: ["auto", "zh", "en"] }] },
    { id: "no-kill-by-name", kind: "preference", scope: "project", default: false },
  ],
};

test("rules split by kind and scope", () => {
  assert.deepEqual(requiredRules(CATALOG).map((r) => r.id), ["progress-body"]);
  assert.deepEqual(scopeRules(CATALOG, "personal").map((r) => r.id), ["plan-approval", "language"]);
  assert.deepEqual(scopeRules(CATALOG, "project").map((r) => r.id), ["no-kill-by-name"]);
  assert.equal(pickText({ en: "a", zh: "甲" }, "zh"), "甲");
  assert.equal(pickText({ en: "a", zh: "" }, "zh"), "a");
});

test("a draft stores only what differs from the defaults", () => {
  const [plan, lang] = scopeRules(CATALOG, "personal");
  let d = draftFrom({ personal: {} }, "personal");
  assert.equal(ruleEnabled(d, plan), true);
  d = setEnabled(d, plan, false);
  assert.deepEqual(d.rules, { "plan-approval": { enabled: false } });
  d = setEnabled(d, plan, true);
  assert.deepEqual(cleanDraft(d).rules, {});
  d = setParam(d, lang, lang.params[0], " zh \n");
  assert.equal(ruleParam(d, lang, lang.params[0]), "zh");
  d = setParam(d, lang, lang.params[0], "auto");
  assert.deepEqual(cleanDraft(d).rules, {});
});

test("custom rules get free ids; empty ones are not sent", () => {
  let d = draftFrom({ project: { custom: [{ id: "custom-1", enabled: true, text: { en: "x" } }] } }, "project");
  d = addCustom(d);
  assert.equal(d.custom[1].id, "custom-2");
  assert.equal(cleanDraft(d).custom.length, 1);
  d = updateCustom(d, 1, { text: { en: "", zh: "规则" } });
  assert.equal(cleanDraft(d).custom.length, 2);
  d = removeCustom(d, 0);
  assert.deepEqual(d.custom.map((c) => c.id), ["custom-2"]);
});

test("token estimate counts CJK characters one each", () => {
  assert.equal(estimateTokens("abcd"), 1);
  assert.equal(estimateTokens("中文"), 2);
  assert.equal(estimateTokens(""), 0);
});

test("multi params: list items, all stands alone, repo options from setup", async () => {
  const m = await import("../../ark_studio/web/js/agent-rules-model.js");
  assert.deepEqual(m.listItems(" a, b,a,, "), ["a", "b"]);
  assert.equal(m.toggleListItem("all", "repo", true), "repo");
  assert.equal(m.toggleListItem("repo", "tasks", true), "repo, tasks");
  assert.equal(m.toggleListItem("repo, tasks", "repo", false), "tasks");
  assert.equal(m.toggleListItem("repo", "all", true), "all");
  const state = {
    resources: [{ key: "ark-studio", kind: "repository", name: "ARK" }, { id: "local", kind: "compute" }],
    project_layer: { profile: { resources: [{ id: "agent-team-tasks", kind: "task_space" }, { id: "ark-studio", kind: "repository" }] } },
    mine: { profile: { resources: [{ id: "notes", kind: "dataset", details: { remote: "git@x:notes" } }] } },
  };
  assert.deepEqual(m.gitRepoOptions(state).map((o) => o.id), ["ark-studio", "agent-team-tasks", "notes"]);
  assert.deepEqual(m.gitRepoOptions(null), []);
  // A rule both scopes may set shows in each.
  const catalog = { rules: [{ id: "git-push", kind: "preference", scope: "both" }] };
  assert.equal(m.scopeRules(catalog, "project").length, 1);
  assert.equal(m.scopeRules(catalog, "personal").length, 1);
});
