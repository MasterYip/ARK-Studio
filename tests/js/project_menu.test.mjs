// Run: node --test tests/js/project_menu.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

const web = new URL("../../ark_studio/web/js/", import.meta.url);

test("project menu offers exactly New project and Open another project", async () => {
  const app = await readFile(new URL("app.js", web), "utf8");
  const start = app.indexOf('"mobile-menu-actions app-menu-actions"');
  const block = app.slice(start, app.indexOf("function mobileMenuSheet", start));
  assert.match(block, /"data-menu-action": "new-project"[\s\S]*openProjectModal\(ctx, "new"\)/);
  assert.match(block, /"data-menu-action": "open-project"[\s\S]*openProjectModal\(ctx, "open"\)/);
  assert.doesNotMatch(block, /new-unit|openCreateUnit|nav\.home|navigateWorkspace\("home"\)/);
});

test("the project dialog has new-only and open-only modes", async () => {
  const src = await readFile(new URL("components.js", web), "utf8");
  assert.match(src, /export async function openProjectModal\(ctx, mode = "all"\)/);
  assert.match(src, /const showOpen = mode !== "new", showNew = mode !== "open";/);
  for (const file of ["i18n-en.js", "i18n-zh.js"]) {
    assert.match(await readFile(new URL(file, web), "utf8"), /"project\.open_other":/);
  }
});

test("Due & blocked: empty groups are skipped, never appended as null", async () => {
  const src = await readFile(new URL("views/overview.js", web), "utf8");
  const body = src.slice(src.indexOf("function duePanel"), src.indexOf("function blockerText"));
  assert.match(body, /panel\.append\(\.\.\.\[[\s\S]*\]\.filter\(Boolean\)\)/);
});
