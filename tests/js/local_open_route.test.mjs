import assert from "node:assert/strict";
import test from "node:test";
import { LOCAL_OPEN_BRIDGE, isLikelyLocalPath, localOpenRoute } from "../../web/js/rich-model.js";

test("desktop app opens local paths itself, whatever its server port", () => {
  assert.deepEqual(localOpenRoute({ shell: "tauri", openLocal: () => {} }), { kind: "desktop" });
});

test("browsers use the loopback bridge, default port or injected URL", () => {
  assert.deepEqual(localOpenRoute(undefined), { kind: "bridge", url: `${LOCAL_OPEN_BRIDGE}/api/local/open` });
  assert.equal(LOCAL_OPEN_BRIDGE, "http://127.0.0.1:8890");
  assert.deepEqual(localOpenRoute({ localOpenUrl: "http://127.0.0.1:18890/" }),
    { kind: "bridge", url: "http://127.0.0.1:18890/api/local/open" });
});

test("local path detection follows the viewer's platform", () => {
  const win = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)";
  const mac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)";
  assert.equal(isLikelyLocalPath("E:/data/a.md", { userAgent: win }), true);
  assert.equal(isLikelyLocalPath("E:\\data\\a.md", { userAgent: win }), true);
  assert.equal(isLikelyLocalPath("/home/me/a.md", { userAgent: win }), false);
  assert.equal(isLikelyLocalPath("E:/data/a.md", { userAgent: mac }), false);
  // A plain browser on macOS/Linux keeps showing the copy dialog (no bridge there).
  assert.equal(isLikelyLocalPath("/Users/me/a.md", { userAgent: mac }), false);
  // Inside the desktop app the platform comes from the shell.
  assert.equal(isLikelyLocalPath("/Users/me/a.md", { userAgent: mac, desktop: { platform: "macos" } }), true);
  assert.equal(isLikelyLocalPath("//server/share", { desktop: { platform: "linux" } }), false);
  assert.equal(isLikelyLocalPath("C:/x", { userAgent: mac, desktop: { platform: "windows" } }), true);
});
