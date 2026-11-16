import { diffChars } from "../vendor/jsdiff/character.js";
import { diffArrays } from "../vendor/jsdiff/array.js";

const wordSegmenter = typeof Intl.Segmenter === "function" ? new Intl.Segmenter(undefined, { granularity: "word" }) : null;

function alignChangedLines(removed, added, deadline) {
  const zip = (old, next) => Array.from({ length: Math.max(old.length, next.length) }, (_, i) => ({ before: old[i] || null, after: next[i] || null }));
  if (!removed.length || !added.length || removed.length * added.length > 10000
      || [...removed, ...added].reduce((size, line) => size + line.text.length, 0) > 50000
      || performance.now() >= deadline) return zip(removed, added);
  const tokens = new Map();
  for (const line of [...removed, ...added]) {
    const words = wordSegmenter ? [...wordSegmenter.segment(line.text)].filter((part) => part.isWordLike).map((part) => part.segment.toLowerCase())
      : line.text.toLowerCase().match(/[\p{L}\p{N}_]+/gu) || [];
    const counts = new Map();
    for (const word of words) counts.set(word, (counts.get(word) || 0) + 1);
    tokens.set(line, { counts, length: words.length });
  }
  // Git groups adjacent insertions and replacements together. Use a second,
  // bounded sequence diff to anchor similar lines before computing inline edits.
  const changes = diffArrays(removed, added, { timeout: Math.max(1, deadline - performance.now()), maxEditLength: 500,
    comparator: (old, next) => {
      if (old.text === next.text) return true;
      const a = tokens.get(old), b = tokens.get(next);
      let common = 0;
      for (const [word, count] of a.counts) common += Math.min(count, b.counts.get(word) || 0);
      return common > 0 && 2 * common / (a.length + b.length) >= .6;
    },
  });
  if (!changes) return zip(removed, added);
  const rows = [];
  let oldIndex = 0, nextIndex = 0, old = [], next = [];
  const flush = () => { rows.push(...zip(old, next)); old = []; next = []; };
  for (const change of changes) {
    if (change.removed) { old.push(...removed.slice(oldIndex, oldIndex + change.count)); oldIndex += change.count; }
    else if (change.added) { next.push(...added.slice(nextIndex, nextIndex + change.count)); nextIndex += change.count; }
    else {
      flush();
      for (let i = 0; i < change.count; i++) rows.push({ before: removed[oldIndex++], after: added[nextIndex++] });
    }
  }
  flush();
  return rows;
}

// Git produces the diff; this only aligns its hunks for a two-column view.
export function diffFiles(source) {
  const files = [];
  const deadline = performance.now() + 60;
  let file = null, before = 0, after = 0, inHunk = false, lastLine = null;
  let removed = [], added = [];
  const flush = () => {
    if (file && (removed.length || added.length)) file.blocks.push({
      index: file.blocks.length, rowIndex: file.rows.length,
      sourceIndex: Math.min(removed[0]?.sourceIndex ?? Infinity, added[0]?.sourceIndex ?? Infinity),
    });
    if (file) for (const row of alignChangedLines(removed, added, deadline)) file.rows.push(row);
    removed = []; added = [];
  };
  for (const [sourceIndex, line] of String(source).split("\n").entries()) {
    if (line.startsWith("diff --git ")) {
      flush();
      file = { name: line.split(" b/").slice(1).join(" b/"), rows: [], blocks: [], sourceIndex };
      files.push(file); inHunk = false; lastLine = null;
      continue;
    }
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (hunk && file) {
      flush(); before = Number(hunk[1]); after = Number(hunk[2]); inHunk = true;
      file.rows.push({ hunk: line }); lastLine = null;
    } else if (inHunk && line.startsWith("-")) {
      lastLine = { number: before++, text: line.slice(1), kind: "removed", sourceIndex };
      removed.push(lastLine);
    } else if (inHunk && line.startsWith("+")) {
      lastLine = { number: after++, text: line.slice(1), kind: "added", sourceIndex };
      added.push(lastLine);
    } else if (inHunk && line.startsWith(" ")) {
      flush();
      lastLine = { text: line.slice(1), kind: "context" };
      file.rows.push({ before: { ...lastLine, number: before++ }, after: { ...lastLine, number: after++ } });
    } else if (inHunk && line.startsWith("\\ No newline") && lastLine) {
      lastLine.noNewline = true;
      const row = file.rows.at(-1);
      if (lastLine.kind === "context" && row?.before && row?.after) {
        row.before.noNewline = row.after.noNewline = true;
      }
    }
  }
  flush();
  return files;
}

export function inlineChanges(before, after, timeout = 15) {
  const whole = (text) => text === null ? [] : [{ text, changed: true }];
  if (before === null || after === null) return { before: whole(before), after: whole(after) };
  if (before === after) return { before: [{ text: before, changed: false }], after: [{ text: after, changed: false }] };
  // Large unrelated lines must not stall the history dialog. A bounded diff
  // falls back to a whole-line highlight while preserving the original text.
  const changes = timeout > 0 && before.length + after.length <= 40000
    ? diffChars(before, after, { timeout, maxEditLength: 2000 }) : null;
  if (!changes) return { before: whole(before), after: whole(after) };
  return {
    before: changes.filter((part) => !part.added).map((part) => ({ text: part.value, changed: Boolean(part.removed) })),
    after: changes.filter((part) => !part.removed).map((part) => ({ text: part.value, changed: Boolean(part.added) })),
  };
}
