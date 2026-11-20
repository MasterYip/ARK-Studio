// Run: node --test tests/js/boot_retry.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

test("boot reloads once by itself on a failed file before showing the error", async () => {
  const src = await readFile(new URL("../../ark_studio/web/js/boot.js", import.meta.url), "utf8");
  const fail = src.slice(src.indexOf("function fail(message)"), src.indexOf("function onError"));
  assert.match(fail, /if \(!retriedRecently\(\)\)/);
  assert.match(fail, /location\.reload\(\)/);
  assert.match(src, /Date\.now\(\) - Number\(sessionStorage\.getItem\(RETRY_KEY\) \|\| 0\) < 30000/);
  assert.match(src.slice(src.indexOf("function finish()")), /sessionStorage\.removeItem\("ark\.boot\.retriedAt"\)/);
});
