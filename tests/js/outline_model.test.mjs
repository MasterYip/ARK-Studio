// Run: node --experimental-vm-modules --test tests/js/outline_model.test.mjs
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

async function load(name) {
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  await module.link(() => { throw new Error(`${name} must stay dependency-free`); });
  await module.evaluate();
  return module.namespace;
}

const m = await load("outline-model.js");
const brief = (items) => items.map((item) => `${item.level}:${item.text}@${item.line}`);

const DOC = [
  "---",                 // 1 front matter
  "title: Notes",        // 2
  "---",                 // 3
  "# Guide",             // 4
  "",                    // 5
  "Intro text.",         // 6
  "",                    // 7
  "## Install **now**",  // 8
  "```bash",             // 9
  "# not a heading",     // 10
  "```",                 // 11
  "### From [source](https://x.test) ###", // 12
  "Setext title",        // 13
  "------------",        // 14
  "- list item",         // 15
  "---",                 // 16 (an hr after a list, not a heading)
  "## Usage",            // 17
  "    # indented code", // 18
  "#### Deep",           // 19
  "# Appendix",          // 20
].join("\n");

test("markdown headings: ATX and setext, not in code, front matter or lists", () => {
  assert.deepEqual(brief(m.markdownHeadings(DOC)), [
    "1:Guide@4", "2:Install now@8", "3:From source@12", "2:Setext title@13", "2:Usage@17", "4:Deep@19", "1:Appendix@20",
  ]);
  assert.deepEqual(brief(m.markdownHeadings("Multi\nline title\n===\nText\n~~~~\n# no\n~~~~\n#\n")), ["1:Multi line title@1", "1:@8"]);
  assert.deepEqual(m.markdownHeadings("#no space\n####### seven\n"), []);
  assert.equal(m.plainHeading("`code` and *em* and __strong__ <b>x</b>"), "code and em and strong x");
});

test("code symbols for JavaScript, Go and Python", () => {
  const js = [
    "import x from 'y';",
    "export function load(a) {",
    "  function inner() {}",
    "}",
    "const run = async (b) => {",
    "};",
    "export default class Panel extends Base {",
    "  constructor(opts) {",
    "  }",
    "  async paint() {",
    "    if (x) {",
    "    }",
    "  }",
    "  static get size() {",
    "  }",
    "}",
    "export interface Shape {}",
    "let plain = 3;",
  ].join("\n");
  assert.deepEqual(brief(m.outlineFor(js, { language: "javascript" })),
    ["1:load@2", "1:run@5", "1:Panel@7", "2:constructor@8", "2:paint@10", "2:size@14", "1:Shape@17"]);
  const go = "package x\n\ntype Store struct {\n}\n\nfunc New() *Store {}\nfunc (s *Store) Get(id string) {}\ntype Reader interface{}\n";
  assert.deepEqual(brief(m.outlineFor(go, { language: "go" })), ["1:Store@3", "1:New@6", "1:Store.Get@7", "1:Reader@8"]);
  const py = "import os\n\nclass A:\n    def one(self):\n        def helper():\n            pass\n    async def two(self):\n        pass\n\ndef main():\n    pass\n";
  assert.deepEqual(brief(m.outlineFor(py, { language: "python" })), ["1:A@3", "2:one@4", "3:helper@5", "2:two@7", "1:main@10"]);
  assert.deepEqual(m.outlineFor("a = 1", { language: "ruby" }), []);
  assert.equal(m.supportsOutline({ viewer: "markdown" }), true);
  assert.equal(m.supportsOutline({ viewer: "code", language: "go" }), true);
  assert.equal(m.supportsOutline({ viewer: "code", language: "css" }), false);
});

test("ids, tree, current item and section ends", () => {
  const items = m.markdownHeadings("# A\n## B\ntext\n## B\n### C\n# D\n");
  assert.deepEqual(m.itemIds(items), ["1:A#1", "2:B#1", "2:B#2", "3:C#1", "1:D#1"]);
  const tree = m.buildTree(items);
  assert.deepEqual(tree.map((node) => [node.item.text, node.children.map((child) => child.item.text)]), [["A", ["B", "B"]], ["D", []]]);
  assert.deepEqual(tree[0].children[1].children.map((node) => node.id), ["3:C#1"]);
  assert.equal(m.currentIndex(items, 1), 0);
  assert.equal(m.currentIndex(items, 3), 1);
  assert.equal(m.currentIndex(items, 5), 3);
  assert.equal(m.currentIndex([{ level: 1, text: "x", line: 4 }], 2), -1);
  assert.equal(m.sectionEnd(items, 0, 7), 5);
  assert.equal(m.sectionEnd(items, 2, 7), 5);
  assert.equal(m.sectionEnd(items, 4, 7), 7);
  assert.deepEqual(m.ancestorsOf(items, 3), [0, 2]);
});

test("folding levels: fold all, unfold all and fold to level n", () => {
  const items = m.markdownHeadings("# A\n## B\n### C\n#### D\n## E\n# F\n");
  assert.deepEqual([...m.foldAllIds(items)], ["1:A#1", "2:B#1", "3:C#1", "4:D#1", "2:E#1", "1:F#1"]);
  // Level 2: headings down to level 2 stay visible, level 2 and deeper are closed.
  assert.deepEqual([...m.foldLevelIds(items, 2)], ["2:B#1", "3:C#1", "4:D#1", "2:E#1"]);
  assert.deepEqual([...m.foldLevelIds(items, 1)], [...m.foldAllIds(items)]);
  assert.deepEqual([...m.foldLevelIds(items, 7)], []);
  // What that leaves visible in a document: headings and the paragraphs after them.
  const entries = [
    { level: 1, id: "1:A#1" }, { level: 0 }, { level: 2, id: "2:B#1" }, { level: 0 }, { level: 3, id: "3:C#1" }, { level: 0 },
    { level: 4, id: "4:D#1" }, { level: 0 }, { level: 2, id: "2:E#1" }, { level: 0 }, { level: 1, id: "1:F#1" }, { level: 0 },
  ];
  const shown = (collapsed) => entries.map((entry, i) => (m.foldVisibility(entries, collapsed)[i] ? "." : entry.level || "p")).join("");
  assert.equal(shown(new Set()), "1p2p3p4p2p1p");
  assert.equal(shown(m.foldLevelIds(items, 2)), "1p2.....2.1p");
  assert.equal(shown(m.foldLevelIds(items, 3)), "1p2p3...2p1p");
  assert.equal(shown(m.foldAllIds(items)), "1.........1.");
  // Headings without an id (not in the outline) still end a folded section.
  assert.deepEqual(m.foldVisibility([{ level: 2, id: "x" }, { level: 0 }, { level: 1 }, { level: 0 }], new Set(["x"])), [false, true, false, false]);
});

test("fold and unfold the current section", () => {
  const items = m.markdownHeadings("# A\n## B\ntext\n### C\nmore\n# D\n");
  let state = m.foldAt(items, 5, new Set(), true);           // in C
  assert.deepEqual([...state], ["3:C#1"]);
  state = m.foldAt(items, 5, state, true);                   // C already folded: its parent B
  assert.deepEqual([...state].sort(), ["2:B#1", "3:C#1"]);
  state = m.foldAt(items, 5, state, false);                  // unfold opens the chain down to C
  assert.deepEqual([...state], []);
  assert.deepEqual([...m.foldAt(items, 0, new Set(["1:A#1"]), false)], ["1:A#1"]);   // before any heading
  assert.deepEqual(m.revealIds(items, 2, new Set(["1:A#1", "2:B#1", "1:D#1"])), ["1:A#1", "2:B#1"]);
});
