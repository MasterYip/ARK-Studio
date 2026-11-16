// Run: node --experimental-vm-modules --test tests/js/files_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function loadModel() {
  const source = await readFile(new URL("../../web/js/files-model.js", import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: "files-model.js" });
  await module.link(() => { throw new Error("files-model must stay dependency-free"); });
  await module.evaluate();
  return module.namespace;
}

const model = await loadModel();

test("scope keys and base refs are normalized", () => {
  assert.equal(model.scopeKey("s_abc"), "s_abc");
  assert.equal(model.scopeKey("  s_abc  "), "s_abc");
  assert.equal(model.scopeKey(""), "project");
  assert.equal(model.scopeKey(null), "project");
  assert.equal(model.normalizeBaseRef(""), "HEAD");
  assert.equal(model.normalizeBaseRef(" feature/x "), "feature/x");
  assert.equal(model.sessionScopeLabel(null), "Project scope");
  assert.equal(model.sessionScopeLabel({ title: "Fix parser" }), "Fix parser");
});

test("file tabs open, activate, dedupe and cap", () => {
  let state = model.normalizeFileTabs({});
  assert.deepEqual(state, { tabs: [], active: "" });
  state = model.openFileTab(state, "README.md");
  state = model.openFileTab(state, "src/app.js");
  assert.deepEqual(state.tabs, ["README.md", "src/app.js"]);
  assert.equal(state.active, "src/app.js");
  state = model.openFileTab(state, "README.md");
  assert.deepEqual(state.tabs, ["src/app.js", "README.md"]);
  assert.equal(state.active, "README.md");
  state = model.activateFileTab(state, "src/app.js");
  assert.equal(state.active, "src/app.js");
  state = model.closeFileTab(state, "src/app.js");
  assert.deepEqual(state.tabs, ["README.md"]);
  assert.equal(state.active, "README.md");
  let many = { tabs: [], active: "" };
  for (let index = 0; index < model.FILE_TAB_LIMIT + 4; index += 1) {
    many = model.openFileTab(many, `file-${index}.txt`);
  }
  assert.equal(many.tabs.length, model.FILE_TAB_LIMIT);
  assert.equal(many.tabs[0], "file-4.txt");
  assert.equal(many.active, `file-${model.FILE_TAB_LIMIT + 3}.txt`);
  assert.deepEqual(model.closeAllFileTabs(many), { tabs: [], active: "" });
});

test("tab normalization repairs an invalid active tab", () => {
  assert.deepEqual(model.normalizeFileTabs({ tabs: ["a", "a", "b"], active: "missing" }),
    { tabs: ["a", "b"], active: "b" });
  assert.deepEqual(model.normalizeFileTabs({ tabs: "nope", active: 5 }), { tabs: [], active: "" });
});

test("tree ancestors expand from the project root", () => {
  assert.deepEqual(model.treeAncestors("src/deep/app.js"), ["src", "src/deep"]);
  assert.deepEqual(model.treeAncestors("README.md"), []);
  assert.deepEqual(model.treeAncestors("src\\win\\x.py"), ["src", "src/win"]);
});

test("diff stats and totals are honest about binary files", () => {
  assert.deepEqual(model.diffStat(3, 1), { additions: 3, deletions: 1, label: "+3 −1" });
  assert.deepEqual(model.diffStat(null, null), { additions: null, deletions: null, label: "±" });
  assert.equal(model.diffTotalLabel({ files: 1, additions: 3, deletions: 1 }), "1 file · +3 −1");
  assert.equal(model.diffTotalLabel({ files: 2, additions: 0, deletions: 0 }), "2 files · +0 −0");
});

test("unified diff rows are classified for coloring", () => {
  const rows = model.parseDiffRows([
    "diff --git a/a.py b/a.py",
    "--- a/a.py",
    "+++ b/a.py",
    "@@ -1 +1 @@",
    "-old",
    "+new",
    " context",
    "\\ No newline at end of file",
    "",
  ].join("\n"));
  assert.deepEqual(rows.map((row) => row.kind),
    ["meta", "meta", "meta", "hunk", "del", "add", "ctx", "meta"]);
  assert.equal(rows[4].text, "-old");
  assert.equal(model.parseDiffRows("").length, 0);
});

test("change and terminal labels", () => {
  assert.equal(model.fileChangeLabel("M"), "modified");
  assert.equal(model.fileChangeLabel("a"), "added");
  assert.equal(model.fileChangeClass("D"), "deleted");
  assert.equal(model.fileChangeClass("R100"), "moved");
  assert.equal(model.terminalRunning("running"), true);
  assert.equal(model.terminalRunning("stopping"), true);
  assert.equal(model.terminalRunning("starting"), false);
  assert.equal(model.terminalRunning("exited"), false);
  assert.equal(model.terminalStatusLabel("starting"), "starting…");
  assert.equal(model.terminalStatusLabel("exited", 0), "exit 0");
  assert.equal(model.terminalStatusLabel("cancelled"), "cancelled");
  assert.equal(model.terminalStatusLabel("output_limit"), "output limit");
  assert.equal(model.terminalText([{ text: "a" }, { text: "b" }]), "ab");
  assert.equal(model.nextTerminalOffset(2, [{ seq: 3 }, { seq: 5 }]), 5);
  assert.equal(model.nextTerminalOffset(7, [{ seq: 3 }]), 7);
  assert.equal(model.clampTerminalHeight(9999), 520);
  assert.equal(model.clampTerminalHeight("nope"), 220);
  assert.equal(model.ptyBackendLabel("pywinpty"), "pywinpty");
  assert.equal(model.ptyBackendLabel("posix"), "POSIX pty");
  assert.equal(model.ptyBackendLabel(""), "");
});

test("ANSI screen renders SGR colour, bold, 256-colour and truecolour runs", () => {
  const screen = model.createAnsiScreen();
  screen.write("\x1b[1;31mred\x1b[0m plain");
  const runs = screen.runs()[0];
  assert.equal(runs.map((run) => run.text).join(""), "red plain");
  assert.equal(runs[0].style.bold, true);
  assert.equal(runs[0].style.fg, model.ANSI_PALETTE[1]);
  assert.equal(runs[1].style.bold, false);
  assert.equal(runs[1].style.fg, "");
  assert.equal(screen.text(), "red plain");

  const extended = model.createAnsiScreen();
  extended.write("\x1b[38;5;196mX\x1b[48;2;1;2;3mY\x1b[39;49mZ");
  const extendedRuns = extended.runs()[0];
  assert.equal(extendedRuns[0].style.fg, "rgb(255, 0, 0)");
  assert.equal(extendedRuns[1].style.bg, "rgb(1, 2, 3)");
  assert.equal(extendedRuns[2].style.fg, "");
  assert.equal(extendedRuns[2].style.bg, "");

  const styled = model.createAnsiScreen();
  styled.write("note", { fg: "#888888", italic: true });
  assert.equal(styled.runs()[0][0].style.italic, true);
  assert.equal(styled.runs()[0][0].style.fg, "#888888");
});

test("ANSI screen handles CR redraw, backspace, erase and clear", () => {
  const screen = model.createAnsiScreen();
  screen.write("progress 10%\rprogress 90%");
  assert.equal(screen.text(), "progress 90%");
  screen.write("\b!");
  assert.equal(screen.text(), "progress 90!");
  screen.write("\x1b[2D");
  screen.write("X");
  assert.equal(screen.text(), "progress 9X!");
  screen.write("\x1b[K");
  assert.equal(screen.text(), "progress 9X");
  screen.write("\x1b[2J\x1b[Hcleared");
  assert.equal(screen.text(), "cleared");
});

test("ANSI screen supports cursor addressing, erase and alternate screen", () => {
  const screen = model.createAnsiScreen();
  screen.write("abcdef");
  screen.write("\x1b[2D\x1b[K");
  assert.equal(screen.text(), "abcd");
  screen.write("\x1b[2;3Hxy");
  assert.equal(screen.text(), "abcd\n  xy");
  screen.write("\x1b[1;1H");
  screen.write("Z");
  assert.equal(screen.text(), "Zbcd\n  xy");
  screen.write("\x1b[?1049h");
  screen.write("alt-screen");
  assert.equal(screen.text(), "alt-screen");
  assert.equal(screen.alternate, true);
  screen.write("\x1b[?1049l");
  assert.equal(screen.text(), "Zbcd\n  xy");
  assert.equal(screen.alternate, false);
  screen.write("\x1b]0;window title\x07visible");
  assert.ok(screen.text().includes("visible"));
});

test("ANSI viewport origin keeps PTY cursor addressing aligned", () => {
  const screen = model.createAnsiScreen({ rows: 5 });
  screen.write("$ python -i\n");
  screen.setOrigin();
  screen.write("banner\ntype help\n>>> ");
  screen.write("\x1b[3;5H1+1");
  assert.equal(screen.text(), "$ python -i\nbanner\ntype help\n>>> 1+1");
  screen.write("\r\n2");
  assert.equal(screen.text(), "$ python -i\nbanner\ntype help\n>>> 1+1\n2");
  screen.setRows(3);
  assert.equal(screen.rows, 3);
  screen.write("\nline\nline\nline\nline");
  assert.ok(screen.origin > 0);
});

test("ANSI text is stripped safely and the screen is line-bounded", () => {
  assert.equal(model.stripAnsi("\x1b[1;31mred\x1b[0m plain"), "red plain");
  assert.equal(model.stripAnsi("\x1b]0;title\x07ok"), "ok");
  const screen = model.createAnsiScreen({ maxLines: 60 });
  for (let index = 0; index < 200; index += 1) screen.write(`line ${index}\n`);
  assert.ok(screen.lineCount <= 60);
  assert.ok(screen.text().endsWith("line 199\n"));
});

test("terminal geometry fits an xterm-like grid and clamps", () => {
  assert.deepEqual(model.fitTerminalSize({
    width: 800, height: 400, cellWidth: 7, lineHeight: 16, paddingX: 20, paddingY: 14,
  }), { cols: 111, rows: 24 });
  assert.deepEqual(model.fitTerminalSize({}), { cols: 80, rows: 24 });
  assert.deepEqual(model.fitTerminalSize({
    width: 50, height: 50, cellWidth: 10, lineHeight: 10,
  }), { cols: 20, rows: 5 });
  assert.deepEqual(model.fitTerminalSize({
    width: 99999, height: 99999, cellWidth: 1, lineHeight: 1,
  }), { cols: 400, rows: 120 });
});
