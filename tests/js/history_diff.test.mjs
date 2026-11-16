import assert from "node:assert/strict";
import { diffFiles, inlineChanges } from "../../ark_studio/web/js/history-diff.js";

const source = [
  "diff --git a/body.md b/body.md", "index 123..456 100644", "--- a/body.md", "+++ b/body.md",
  "@@ -2,4 +2,5 @@", " unchanged", "-old one", "-old two", "+new one", "+new two", "+new three", " tail",
  "@@ -20 +21 @@", "-old last", "\\ No newline at end of file", "+new last", "\\ No newline at end of file",
  "diff --git a/title.txt b/title.txt", "--- a/title.txt", "+++ b/title.txt", "@@ -1 +1 @@", "-Old", "+New", "",
].join("\n");
const files = diffFiles(source);
assert.deepEqual(files.map((file) => file.name), ["body.md", "title.txt"]);
assert.deepEqual(files[0].rows[1], {
  before: { number: 2, text: "unchanged", kind: "context" }, after: { number: 2, text: "unchanged", kind: "context" },
});
assert.deepEqual(files[0].rows[2], {
  before: { number: 3, text: "old one", kind: "removed", sourceIndex: 6 },
  after: { number: 3, text: "new one", kind: "added", sourceIndex: 8 },
});
assert.equal(files[0].rows[4].before, null);
assert.equal(files[0].rows[4].after.number, 5);
assert.equal(files[0].rows[5].before.number, 5);
assert.equal(files[0].rows[5].after.number, 6);
assert.equal(files[0].rows[7].before.number, 20);
assert.equal(files[0].rows[7].after.number, 21);
assert.ok(files[0].rows[7].before.noNewline && files[0].rows[7].after.noNewline);
assert.equal(files[1].rows[1].before.text, "Old");
assert.deepEqual(files[0].blocks, [
  { index: 0, rowIndex: 2, sourceIndex: 6 }, { index: 1, rowIndex: 7, sourceIndex: 13 },
]);
assert.deepEqual(diffFiles(""), []);
assert.equal(diffFiles("diff --git a/body.md b/body.md\n@@ -0,0 +1 @@\n+insert")[0].rows[1].before, null);
assert.equal(diffFiles("diff --git a/body.md b/body.md\n@@ -1 +0,0 @@\n-delete")[0].rows[1].after, null);

const changedText = (parts) => parts.filter((part) => part.changed).map((part) => part.text).join("");
const numeric = inlineChanges("Measured **1.9 cm**", "Measured **1.7 cm**");
assert.equal(changedText(numeric.before), "9");
assert.equal(changedText(numeric.after), "7");
for (const [before, after] of [
  ["研究🙂状态：草稿", "研究🚀状态：完成"],
  ["a <script>alert(1)</script> old", "a <script>alert(2)</script> new"],
  ["   spaced\tline", "  spaced line"],
  ["abc", "abc"], [null, "New line"], ["Deleted line", null], ["", "x"],
]) {
  const result = inlineChanges(before, after);
  assert.equal(result.before.map((part) => part.text).join(""), before || "");
  assert.equal(result.after.map((part) => part.text).join(""), after || "");
  for (const part of [...result.before, ...result.after]) {
    assert.ok(!/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(part.text));
  }
}
assert.equal(changedText(inlineChanges("研究🙂状态", "研究🚀状态").before), "🙂");
assert.equal(changedText(inlineChanges("研究🙂状态", "研究🚀状态").after), "🚀");
assert.equal(changedText(inlineChanges("abc", "abc").before), "");
assert.deepEqual(inlineChanges("old", "new", 0), {
  before: [{ text: "old", changed: true }], after: [{ text: "new", changed: true }],
});
const large = "a".repeat(50000);
assert.equal(changedText(inlineChanges(large, "new").before), large);

const originalParagraphs = [
  '- **Disturbance robustness.** Table R1 and Figure R1 compare each tracker with its student.',
  '- **Responsiveness.** We measure response delay and recovery time under a sustained pull.',
];
const inserted = '- **Physical consistency.** The predicted and executed states agree within the measured error bounds.';
const changedParagraphs = [inserted, originalParagraphs[0].replace('Figure R1', 'Figure R3'),
  originalParagraphs[1].replace('recovery time', 'recovery duration')];
const paragraphDiff = (old, next) => ['diff --git a/body.md b/body.md', '--- a/body.md', '+++ b/body.md',
  `@@ -1,${old.length} +1,${next.length} @@`, ...old.map(line => '-' + line), ...next.map(line => '+' + line)].join('\n');
const aligned = diffFiles(paragraphDiff(originalParagraphs, changedParagraphs))[0];
assert.equal(aligned.rows[1].before, null);
assert.equal(aligned.rows[1].after.text, inserted);
assert.equal(aligned.rows[2].before.text, originalParagraphs[0]);
assert.equal(aligned.rows[2].after.text, changedParagraphs[1]);
assert.equal(changedText(inlineChanges(aligned.rows[2].before.text, aligned.rows[2].after.text).before), '1');
assert.equal(changedText(inlineChanges(aligned.rows[2].before.text, aligned.rows[2].after.text).after), '3');
assert.equal(aligned.blocks.length, 1); // display alignment does not change server apply-block identities
const reversed = diffFiles(paragraphDiff(changedParagraphs, originalParagraphs))[0];
assert.equal(reversed.rows[1].before.text, inserted);
assert.equal(reversed.rows[1].after, null);
for (const [old, next] of [
  [['研究数据已经完成第一轮分析', '实验结果支持当前假设'], ['新增说明：这里介绍数据来源', '研究数据已经完成第二轮分析', '实验结果支持当前结论']],
  [['Keep first line', 'Delete obsolete details', 'Keep final line'], ['Keep first changed line', 'Keep final changed line']],
  [Array.from({ length: 120 }, (_, i) => `old ${i}`), Array.from({ length: 121 }, (_, i) => `new ${i}`)],
]) {
  const result = diffFiles(paragraphDiff(old, next))[0].rows.filter(row => !row.hunk);
  assert.deepEqual(result.filter(row => row.before).map(row => row.before.text), old);
  assert.deepEqual(result.filter(row => row.after).map(row => row.after.text), next);
}
