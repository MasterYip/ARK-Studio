// The Assistant's reach: a unit from another project is labelled with the
// project's name and links to [[prj/uid]]; tool steps name the project they
// ran in; the scope strings exist in both languages.
import assert from "node:assert/strict";
import test from "node:test";

import { crossRefLabel, toolUnitRef, toolProjectName } from "../../ark_studio/web/js/assistant-scope-model.js";
import { EN } from "../../ark_studio/web/js/i18n-en.js";
import { ZH } from "../../ark_studio/web/js/i18n-zh.js";

const projects = [{ id: "prj_cur1", name: "Mine" }, { id: "prj_team9", name: "Team" }];

test("a unit in another project reads with the project's name", () => {
  assert.equal(crossRefLabel("prj_team9/T-0001", projects, "prj_cur1"), "Team · T-0001");
  assert.equal(crossRefLabel("prj_cur1/T-0001", projects, "prj_cur1"), "T-0001");
  // Not in the person's list: the reference as written.
  assert.equal(crossRefLabel("prj_gone/T-0001", projects, "prj_cur1"), "prj_gone/T-0001");
  assert.equal(crossRefLabel("T-0001", projects, "prj_cur1"), "T-0001");
});

test("tool steps link to the project's unit", () => {
  assert.equal(toolUnitRef({ uid: "I-0002" }), "I-0002");
  assert.equal(toolUnitRef({ uid: "I-0002", cite: "[[prj_team9/I-0002]]", project: { id: "prj_team9" } }), "prj_team9/I-0002");
  assert.equal(toolUnitRef({ error: "nope" }), "");
  assert.equal(toolUnitRef(null), "");
});

test("tool steps name the project they ran in, not the current one", () => {
  assert.equal(toolProjectName({ args: { project: "prj_team9" } }, projects, "prj_cur1"), "Team");
  assert.equal(toolProjectName({ args: {}, result: { project: { id: "prj_x", name: "X" } } }, projects, "prj_cur1"), "X");
  assert.equal(toolProjectName({ args: { project: "prj_cur1" } }, projects, "prj_cur1"), "");
  assert.equal(toolProjectName({ args: { query: "a" } }, projects, "prj_cur1"), "");
});

test("scope strings exist in English and Chinese", () => {
  for (const key of ["chat.scope", "chat.scope_hint", "chat.tool.projects", "chat.tool.neighbors", "chat.in_project", "chat.tool.other_projects"]) {
    assert.ok(EN[key], `missing en: ${key}`);
    assert.ok(ZH[key], `missing zh: ${key}`);
  }
  assert.equal(ZH["chat.scope"], "当前项目 · 可读取你有权限的其他项目");
});
