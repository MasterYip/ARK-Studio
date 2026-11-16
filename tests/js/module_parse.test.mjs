// Browser-module syntax check without a bundler.
// Run: node --experimental-vm-modules --test tests/js/module_parse.test.mjs
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const root = fileURLToPath(new URL("../../web/js", import.meta.url));

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const target = join(dir, entry.name);
    return entry.isDirectory() ? walk(target) : [target];
  });
}

const modules = walk(root).filter((file) => file.endsWith(".js"));

test("every web ES module is syntactically valid", () => {
  assert.ok(modules.length > 20, `expected the web module tree, found ${modules.length}`);
  const failures = [];
  for (const file of modules) {
    try {
      new vm.SourceTextModule(readFileSync(file, "utf8"), { identifier: file });
    } catch (error) {
      failures.push(`${file}: ${error.message}`);
    }
  }
  assert.deepEqual(failures, []);
});
