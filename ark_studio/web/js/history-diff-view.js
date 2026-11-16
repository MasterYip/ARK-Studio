import { h } from "./dom.js";
import { t } from "./i18n.js";
import { diffFiles, inlineChanges } from "./history-diff.js";
import { icon } from "./icons.js";

// Row keys of the side-by-side diff (also the data-side attribute), not labels.
const DIFF_SIDES = ["before", "after"];

export function renderHistoryDiff(source, { onApply, canApply = false } = {}) {
  const files = diffFiles(source), changedLines = new Map();
  const deadline = performance.now() + 200;
  for (const file of files) for (const row of file.rows) {
    if (row.hunk || row.before?.kind === "context" || row.after?.kind === "context") continue;
    const parts = inlineChanges(row.before?.text ?? null, row.after?.text ?? null,
      Math.max(0, Math.min(15, deadline - performance.now())));
    for (const side of ["before", "after"]) if (row[side]) {
      row[side].segments = parts[side];
      changedLines.set(row[side].sourceIndex, row[side]);
    }
  }
  const segments = (line) => line?.segments
    ? line.segments.map((part) => part.changed ? h("mark", { class: "history-word-change" }, part.text) : part.text)
    : line?.text || " ";
  const applyButton = (file, change = null, compact = false) => onApply ? h("button", {
    type: "button", class: `icon-btn history-apply ${compact ? "history-apply-change" : ""}`,
    title: t(!canApply ? "history.apply_current" : change === null ? "history.apply_file" : "history.apply_change"),
    "aria-label": t(change === null ? "history.apply_file" : "history.apply_change"),
    disabled: !canApply, "data-file": file, "data-change": change ?? "file",
    onclick: (event) => { event.stopPropagation(); onApply({ file, change }); },
  }, icon("arrowRight", "sm")) : null;
  const split = h("div", { class: "history-diff-split" }, files.map((file) => {
    const labels = [t("history.from"), t("history.to")];
    const blocks = new Map(file.blocks.map((block) => [block.rowIndex, block]));
    return h("section", { class: "history-diff-file" }, h("h4", null, file.name, applyButton(file.name)),
      // Every pair shares its grid row and the outer scrollbar. Wrapped text
      // therefore stays aligned without measuring heights or scroll feedback.
      h("div", { class: "history-diff-columns" },
        h("div", { class: "history-diff-pair history-head-pair" }, labels.map((label) => h("div", { class: "history-pane-head" }, label))),
        file.rows.map((row, index) => h("div", { class: "history-diff-pair" }, DIFF_SIDES.map((side) => {
          const line = row[side];
          if (row.hunk) return h("div", { class: "history-diff-row hunk", "data-side": side }, row.hunk);
          return h("div", { class: `history-diff-row ${line?.kind || "placeholder"}`, "data-side": side },
            side === "after" && blocks.has(index) ? applyButton(file.name, blocks.get(index).index, true) : null,
            h("span", { class: "history-line-number", "aria-hidden": "true" }, line?.number ?? ""),
            h("span", { class: "history-line-text" }, segments(line), line?.noNewline
              ? h("span", { class: "history-line-note" }, ` [${t("history.no_final_newline")}]`) : null));
        })))));
  }));
  const fileStarts = new Map(files.map((file) => [file.sourceIndex, file]));
  const blockStarts = new Map(files.flatMap((file) => file.blocks.map((block) => [block.sourceIndex, { file: file.name, ...block }])));
  const unified = h("pre", { class: "history-diff history-diff-unified" }, source.split("\n").map((line, index) => {
    if (fileStarts.has(index) && onApply) {
      const file = fileStarts.get(index);
      return h("span", { class: "history-unified-file" }, file.name, applyButton(file.name));
    }
    const changed = changedLines.get(index);
    const hasGutter = changed || line.startsWith(" ");
    return h("span", {
      class: [hasGutter ? "history-unified-line" : "", line.startsWith("+") ? "added"
        : line.startsWith("-") ? "removed" : line.startsWith("@@") ? "hunk" : ""].filter(Boolean).join(" "),
    }, hasGutter ? [blockStarts.has(index) ? applyButton(blockStarts.get(index).file, blockStarts.get(index).index, true) : null,
      h("span", { class: "history-unified-sign" }, line[0]),
      h("span", { class: "history-unified-text" }, changed ? segments(changed) : line.slice(1), "\n")] : line + "\n");
  }));
  return h("div", { class: "history-diff-responsive" }, split, unified);
}
