// Settings repaints only for changes to the data it shows, never over
// unsaved edits (settings-sync-model.js; the 2 s repaint loop of 2026-11-22).
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { settingsNeedsRepaint, settingsViewKey } from "../../ark_studio/web/js/settings-sync-model.js";

const base = () => ({
  project: { id: "prj_a", name: "Alpha", path: "/p/a", settings: { agents: { providers: { codex: { enabled: true } } } },
    updated: "2026-11-22T10:00:00Z", stats: { units: 3 } },
  session: { user: "ana", role: "member", project_permission: "admin", local: false },
  stats: { units: 3 }, activity: [{ id: 1 }], needsYou: [], units: [],
  infoHubPath: "", theme: "light", palettePresets: {},
});

test("unit, activity and counter changes do not repaint Settings", () => {
  const shown = settingsViewKey(base());
  const after = base();
  after.project.updated = "2026-11-22T10:00:02Z";
  after.project.stats = { units: 4 };
  after.stats = { units: 4 };
  after.activity = [{ id: 2, action: "mobile.pair.create" }, { id: 1 }];
  after.needsYou = [{ id: 9 }];
  after.units = [{ id: 4 }];
  const key = settingsViewKey(after);
  assert.equal(key, shown);
  assert.equal(settingsNeedsRepaint({ key, shownKey: shown }), false);
});

test("a change to what Settings shows repaints it", () => {
  const shown = settingsViewKey(base());
  const changes = [
    (S) => { S.project.settings.agents.providers.codex.enabled = false; },
    (S) => { S.project.name = "Alpha 2"; },
    (S) => { S.session.project_permission = "write"; },
    (S) => { S.palettePresets = { sea: {} }; },
    (S) => { S.project = { ...S.project, id: "prj_b" }; },
  ];
  for (const change of changes) {
    const S = base();
    change(S);
    const key = settingsViewKey(S);
    assert.notEqual(key, shown, String(change));
    assert.equal(settingsNeedsRepaint({ key, shownKey: shown }), true, String(change));
  }
});

test("unsaved edits are never repainted, not even by Sync now", () => {
  const S = base();
  const shown = settingsViewKey(S);
  S.project.settings = { agents: {} };
  const key = settingsViewKey(S);
  assert.equal(settingsNeedsRepaint({ key, shownKey: shown, dirty: true }), false);
  assert.equal(settingsNeedsRepaint({ key: shown, shownKey: shown, dirty: true, manual: true }), false);
  assert.equal(settingsNeedsRepaint({ key: shown, shownKey: shown, manual: true }), true);
});

test("app.js asks the Settings page before rebuilding it for a remote change", () => {
  const app = readFileSync(new URL("../../ark_studio/web/js/app.js", import.meta.url), "utf8");
  const start = app.indexOf("async function refreshFromRemote(");
  const body = app.slice(start, app.indexOf("\n}\n", start));
  const gate = body.indexOf('S.tab === "settings" && ctx.settingsPage?.mounted()');
  assert.ok(gate > 0, "refreshFromRemote has a Settings branch");
  assert.ok(gate < body.indexOf("await renderRoute("), "the Settings branch comes before the generic rebuild");
  assert.match(body.slice(gate, gate + 400), /settingsNeedsRepaint\(/);
});

test("Settings creates a pairing code only while the Mobile section is shown", () => {
  const src = readFileSync(new URL("../../ark_studio/web/js/views/settings.js", import.meta.url), "utf8");
  const start = src.indexOf("async function maybeAutoPair(");
  const body = src.slice(start, src.indexOf("\n  }\n", start));
  const guard = body.indexOf('activeTab !== tabOfSection("mobile")');
  assert.ok(guard > 0, "auto pairing checks the visible tab");
  assert.ok(guard < body.indexOf("performPairing("), "before it creates a code");
  assert.match(body, /autoPairings\.get\(/, "a repaint reuses the code made before");
});
