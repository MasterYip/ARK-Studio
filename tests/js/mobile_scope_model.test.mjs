// What a paired phone may call (mobile-scope-model.js) matches the server's
// guard (routes_mobile.go).  Run with: node --test tests/js/mobile_scope_model.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { PHONE_GETS, PHONE_PREFIXES, phoneMayCall } from "../../ark_studio/web/js/mobile-scope-model.js";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const GO = fs.readFileSync(path.join(ROOT, "ark_studio/server/internal/server/routes_mobile.go"), "utf8");
const strings = (text) => [...text.matchAll(/"(\/api\/[^"]*)"/g)].map((m) => m[1]);

test("the phone lists equal the server's", () => {
  const prefixes = GO.match(/mobileDevicePrefixes = \[\]string\{([\s\S]*?)\}/)[1];
  assert.deepEqual([...PHONE_PREFIXES].sort(), strings(prefixes).sort());
  const gets = GO.match(/mobileScopedGet = map\[string\]bool\{([\s\S]*?)\n\)/)[1];
  const goGets = strings(gets);
  if (/notificationsPath:\s*true/.test(gets)) goGets.push("/api/notifications");
  assert.deepEqual([...PHONE_GETS].sort(), goGets.sort());
});

test("phoneMayCall follows the server's rules", () => {
  for (const [route, method] of [["/api/bootstrap", "GET"], ["/api/units/T-0001", "PATCH"], ["/api/project/overview?x=1", "GET"],
    ["/api/projects", "GET"], ["/api/projects/open", "POST"], ["/api/me", "POST"], ["/api/users/bob/avatar", "GET"],
    ["/api/mobile/session", "GET"], ["/api/health", "GET"], ["/index.html", "GET"]]) {
    assert.equal(phoneMayCall(route, method), true, `${method} ${route}`);
  }
  for (const [route, method] of [["/api/settings/effective", "GET"], ["/api/providers", "GET"], ["/api/personal/agent-tokens", "GET"],
    ["/api/org/users", "GET"], ["/api/projects", "POST"], ["/api/me/password", "POST"], ["/api/agent/token", "GET"],
    ["/api/open", "POST"], ["/api/users/bob/avatar", "POST"], ["/api/projectsx", "GET"], ["/api/remote/info", "GET"]]) {
    assert.equal(phoneMayCall(route, method), false, `${method} ${route}`);
  }
});
