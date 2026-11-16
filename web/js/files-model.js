// Pure helpers for the Tasks → Files workspace.
// Kept DOM-free (like session-model.js) so tabs, diff rows and small labels can
// be tested with Node.

export const FILE_TAB_LIMIT = 10;
export const FILE_STORE_KEY = "ark.files.workspace.v1";

// English defaults for the labels below. UI callers inject the app translator
// (`{ t }` from i18n.js); Node tests use these strings unchanged.
/* i18n-ignore-start */
const EN_TEXT = {
  "files.diff_total_one": "{n} file · +{add} −{del}",
  "files.diff_total": "{n} files · +{add} −{del}",
  "files.change.modified": "modified",
  "files.change.added": "added",
  "files.change.deleted": "deleted",
  "files.change.renamed": "renamed",
  "files.change.copied": "copied",
  "files.change.type_changed": "type changed",
  "files.change.unmerged": "unmerged",
  "files.change.changed": "changed",
  "files.term.starting": "starting…",
  "files.term.running": "running",
  "files.term.stopping": "stopping…",
  "files.term.finished": "finished",
  "files.term.exit": "exit {code}",
  "files.term.cancelled": "cancelled",
  "files.term.timed_out": "timed out",
  "files.term.output_limit": "output limit",
  "files.term.failed": "failed",
  "files.term.idle": "idle",
  "files.project_scope": "Project scope",
};
/* i18n-ignore-end */

/** English translator used when no `t` is injected. Same `{var}` syntax as i18n.js. */
export function enText(key, vars = {}) {
  let text = EN_TEXT[key] ?? key;
  for (const [name, value] of Object.entries(vars || {})) text = text.replaceAll(`{${name}}`, String(value));
  return text;
}

export function scopeKey(sessionId) {
  const text = String(sessionId || "").trim();
  return text || "project";
}

export function normalizeFileTabs(value) {
  const source = value && typeof value === "object" ? value : {};
  const raw = Array.isArray(source.tabs) ? source.tabs : [];
  const tabs = [];
  for (const item of raw) {
    const path = String(item || "").trim();
    if (path && !tabs.includes(path)) tabs.push(path);
  }
  const trimmed = tabs.slice(0, FILE_TAB_LIMIT);
  let active = String(source.active || "").trim();
  if (!trimmed.includes(active)) active = trimmed[trimmed.length - 1] || "";
  return { tabs: trimmed, active };
}

export function openFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeFileTabs(state);
  if (!target) return next;
  next.tabs = next.tabs.filter((item) => item !== target);
  next.tabs.push(target);
  while (next.tabs.length > FILE_TAB_LIMIT) next.tabs.shift();
  next.active = target;
  return next;
}

export function closeFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeFileTabs(state);
  const index = next.tabs.indexOf(target);
  if (index < 0) return next;
  next.tabs.splice(index, 1);
  if (next.active === target) {
    next.active = next.tabs[index] || next.tabs[index - 1] || "";
  }
  return normalizeFileTabs(next);
}

export function closeAllFileTabs() {
  return { tabs: [], active: "" };
}

export function activateFileTab(state, path) {
  const target = String(path || "").trim();
  const next = normalizeFileTabs(state);
  if (!next.tabs.includes(target)) return next;
  next.active = target;
  return next;
}

export function treeAncestors(path) {
  const parts = String(path || "").replace(/\\/g, "/").split("/").filter(Boolean);
  const out = [];
  for (let index = 0; index < parts.length - 1; index += 1) {
    out.push(parts.slice(0, index + 1).join("/"));
  }
  return out;
}

export function diffStat(additions, deletions) {
  const usable = (value) => value !== null && value !== undefined && value !== ""
    && Number.isFinite(Number(value));
  const both = usable(additions) && usable(deletions);
  const add = both ? Math.max(0, Number(additions)) : null;
  const del = both ? Math.max(0, Number(deletions)) : null;
  return {
    additions: add,
    deletions: del,
    label: both ? `+${add} −${del}` : "±",
  };
}

export function diffTotalLabel(totals = {}, { t = enText } = {}) {
  const files = Number(totals.files) || 0;
  const additions = Number(totals.additions) || 0;
  const deletions = Number(totals.deletions) || 0;
  return t(files === 1 ? "files.diff_total_one" : "files.diff_total", { n: files, add: additions, del: deletions });
}

export function parseDiffRows(text = "") {
  const lines = String(text || "").split("\n");
  if (lines.length && lines[lines.length - 1] === "") lines.pop();
  return lines.map((line) => {
    let kind = "ctx";
    if (line.startsWith("diff --git") || line.startsWith("index ")
      || line.startsWith("new file mode") || line.startsWith("deleted file mode")
      || line.startsWith("similarity index") || line.startsWith("rename from")
      || line.startsWith("rename to") || line.startsWith("Binary files") // i18n-ignore: git diff syntax
      || line.startsWith("GIT binary patch") || line.startsWith("\\ No newline")
      || line.startsWith("--- ") || line.startsWith("+++ ")) {
      kind = "meta";
    } else if (line.startsWith("@@")) {
      kind = "hunk";
    } else if (line.startsWith("+")) {
      kind = "add";
    } else if (line.startsWith("-")) {
      kind = "del";
    }
    return { kind, text: line };
  });
}

const STATUS_LABELS = {
  M: "modified", A: "added", D: "deleted", R: "renamed", C: "copied",
  T: "type_changed", U: "unmerged",
};

export function fileChangeLabel(status, { t = enText } = {}) {
  const code = String(status || "M").slice(0, 1).toUpperCase();
  return t(`files.change.${STATUS_LABELS[code] || "changed"}`);
}

export function fileChangeClass(status) {
  const code = String(status || "M").slice(0, 1).toUpperCase();
  if (code === "A") return "added";
  if (code === "D") return "deleted";
  if (code === "R" || code === "C") return "moved";
  return "modified";
}

export function terminalRunning(status) {
  return status === "running" || status === "stopping";
}

export function terminalStatusLabel(status, exitCode = null, { t = enText } = {}) {
  switch (status) {
    case "starting": return t("files.term.starting");
    case "running": return t("files.term.running");
    case "stopping": return t("files.term.stopping");
    case "exited": return exitCode === null || exitCode === undefined ? t("files.term.finished") : t("files.term.exit", { code: exitCode });
    case "cancelled": return t("files.term.cancelled");
    case "timeout": return t("files.term.timed_out");
    case "output_limit": return t("files.term.output_limit");
    case "failed": return t("files.term.failed");
    default: return t("files.term.idle");
  }
}

export function terminalText(entries = []) {
  return entries.map((entry) => String(entry?.text || "")).join("");
}

export function nextTerminalOffset(offset, entries = []) {
  let cursor = Number(offset) || 0;
  for (const entry of entries) {
    const seq = Number(entry?.seq) || 0;
    if (seq > cursor) cursor = seq;
  }
  return cursor;
}

export function clampTerminalHeight(value, min = 120, max = 520) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 220;
  return Math.max(min, Math.min(max, Math.round(number)));
}

// --------------------------------------------------------------- ANSI screen
// A small, DOM-free terminal screen used by the Files terminal drawer.  It is
// intentionally not a complete terminal emulator: it supports SGR colour/bold/
// underline, carriage-return redraws, erase/clear, cursor addressing and the
// alternate screen, and safely ignores everything else.  The browser turns the
// resulting styled runs into spans; raw PTY text is never interpreted as HTML.

export const ANSI_PALETTE = [
  "#1c1f26", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#dcdfe4",
  "#5c6370", "#ff7b86", "#b5e890", "#f5d093", "#7cc4ff", "#d99bf0", "#7fd4df", "#ffffff",
];

const ANSI_DEFAULT_STYLE = Object.freeze({
  fg: "", bg: "", bold: false, dim: false, italic: false, underline: false, inverse: false,
});

function ansiBaseStyle() {
  return { ...ANSI_DEFAULT_STYLE };
}

function xtermColor(index) {
  const value = Number(index);
  if (!Number.isFinite(value) || value < 0) return "";
  if (value < 16) return ANSI_PALETTE[Math.floor(value)];
  if (value < 232) {
    const cube = [0, 95, 135, 175, 215, 255];
    const n = Math.floor(value) - 16;
    const r = cube[Math.floor(n / 36) % 6];
    const g = cube[Math.floor(n / 6) % 6];
    const b = cube[n % 6];
    return rgbColor(r, g, b);
  }
  if (value < 256) {
    const level = 8 + (Math.floor(value) - 232) * 10;
    return rgbColor(level, level, level);
  }
  return "";
}

function rgbColor(r, g, b) {
  const channel = (value) => Math.max(0, Math.min(255, Math.round(Number(value) || 0)));
  return `rgb(${channel(r)}, ${channel(g)}, ${channel(b)})`;
}

function sameAnsiStyle(left, right) {
  return left === right || (left.fg === right.fg && left.bg === right.bg
    && left.bold === right.bold && left.dim === right.dim
    && left.italic === right.italic && left.underline === right.underline
    && left.inverse === right.inverse);
}

function applySgr(style, params) {
  const next = { ...style };
  const raw = String(params || "").replace(/^[?>]/, "");
  const codes = raw === "" ? [0] : raw.split(";").map((part) => (part === "" ? 0 : Number(part)));
  for (let index = 0; index < codes.length; index += 1) {
    const code = codes[index];
    if (!Number.isFinite(code)) continue;
    if (code === 0) {
      Object.assign(next, ANSI_DEFAULT_STYLE);
    } else if (code === 1) {
      next.bold = true;
    } else if (code === 2) {
      next.dim = true;
    } else if (code === 3) {
      next.italic = true;
    } else if (code === 4) {
      next.underline = true;
    } else if (code === 7) {
      next.inverse = true;
    } else if (code === 21 || code === 22) {
      next.bold = false;
      next.dim = false;
    } else if (code === 23) {
      next.italic = false;
    } else if (code === 24) {
      next.underline = false;
    } else if (code === 27) {
      next.inverse = false;
    } else if (code === 39) {
      next.fg = "";
    } else if (code === 49) {
      next.bg = "";
    } else if (code >= 30 && code <= 37) {
      next.fg = ANSI_PALETTE[code - 30];
    } else if (code >= 90 && code <= 97) {
      next.fg = ANSI_PALETTE[code - 90 + 8];
    } else if (code >= 40 && code <= 47) {
      next.bg = ANSI_PALETTE[code - 40];
    } else if (code >= 100 && code <= 107) {
      next.bg = ANSI_PALETTE[code - 100 + 8];
    } else if (code === 38 || code === 48) {
      const key = code === 38 ? "fg" : "bg";
      const mode = codes[index + 1];
      if (mode === 5 && index + 2 < codes.length) {
        next[key] = xtermColor(codes[index + 2]);
        index += 2;
      } else if (mode === 2 && index + 4 < codes.length) {
        next[key] = rgbColor(codes[index + 2], codes[index + 3], codes[index + 4]);
        index += 4;
      } else if (mode !== undefined) {
        index += 1;
      }
    }
  }
  return next;
}

function findStringEnd(text, start) {
  for (let index = start; index < text.length; index += 1) {
    if (text[index] === "\x07") return index + 1;
    if (text[index] === "\x1b" && text[index + 1] === "\\") return index + 2;
  }
  return text.length;
}

function clampNumber(value, low, high) {
  return Math.max(low, Math.min(high, value));
}

export function createAnsiScreen(options = {}) {
  const maxLines = clampNumber(Number(options.maxLines) || 3000, 50, 20000);
  let viewportRows = clampNumber(Number(options.rows) || 24, 1, 500);
  const blankLine = () => ({ chars: [], styles: [] });
  let lines = [blankLine()];
  let row = 0;
  let col = 0;
  let originRow = 0;
  let style = ansiBaseStyle();
  let savedCursor = { row: 0, col: 0 };
  let altScreen = null;

  function ensureRow(index) {
    while (lines.length <= index) lines.push(blankLine());
  }

  function trimScrollback() {
    if (lines.length <= maxLines) return;
    const drop = lines.length - maxLines;
    lines = lines.slice(drop);
    row = Math.max(0, row - drop);
    originRow = Math.max(0, originRow - drop);
  }

  function followViewport() {
    if (row - originRow >= viewportRows) {
      originRow = Math.max(0, row - viewportRows + 1);
    }
  }

  function currentLine() {
    ensureRow(row);
    return lines[row];
  }

  function writeChar(char) {
    const line = currentLine();
    while (line.chars.length < col) {
      line.chars.push(" ");
      line.styles.push(style);
    }
    if (col < line.chars.length) {
      line.chars[col] = char;
      line.styles[col] = style;
    } else {
      line.chars.push(char);
      line.styles.push(style);
    }
    col += 1;
  }

  function newline() {
    row += 1;
    col = 0;
    ensureRow(row);
    followViewport();
    trimScrollback();
  }

  function reset() {
    lines = [blankLine()];
    row = 0;
    col = 0;
    originRow = 0;
    style = ansiBaseStyle();
    savedCursor = { row: 0, col: 0 };
    altScreen = null;
  }

  function eraseLine(mode) {
    const line = currentLine();
    if (mode === 1) {
      const end = Math.min(col, line.chars.length - 1);
      for (let index = 0; index <= end; index += 1) {
        line.chars[index] = " ";
        line.styles[index] = ANSI_DEFAULT_STYLE;
      }
    } else if (mode === 2) {
      line.chars = [];
      line.styles = [];
    } else {
      line.chars = line.chars.slice(0, col);
      line.styles = line.styles.slice(0, col);
    }
  }

  function eraseDisplay(mode) {
    if (mode === 2 || mode === 3) {
      lines = [blankLine()];
      row = 0;
      col = 0;
      originRow = 0;
      return;
    }
    if (mode === 1) {
      for (let index = 0; index < row; index += 1) lines[index] = blankLine();
      eraseLine(1);
      return;
    }
    eraseLine(0);
    if (row + 1 < lines.length) lines = lines.slice(0, row + 1);
  }

  function scrollUp(count) {
    const amount = clampNumber(count, 0, 10000);
    if (!amount) return;
    if (amount >= lines.length) {
      lines = [blankLine()];
    } else {
      lines = lines.concat(Array.from({ length: amount }, blankLine)).slice(amount);
    }
    row = Math.max(0, row - amount);
    trimScrollback();
  }

  function scrollDown(count) {
    const amount = clampNumber(count, 0, 10000);
    if (!amount) return;
    lines = Array.from({ length: amount }, blankLine)
      .concat(lines)
      .slice(0, Math.max(maxLines, lines.length));
    row = Math.min(row + amount, lines.length - 1);
  }

  function insertLines(count) {
    const amount = clampNumber(count, 0, 10000);
    ensureRow(row);
    const filler = Array.from({ length: amount }, blankLine);
    lines = lines.slice(0, row).concat(filler, lines.slice(row)).slice(0, maxLines);
    col = 0;
  }

  function deleteLines(count) {
    const amount = clampNumber(count, 0, 10000);
    ensureRow(row);
    lines = lines.slice(0, row).concat(lines.slice(row + amount));
    while (lines.length < 1) lines.push(blankLine());
    trimScrollback();
  }

  function insertChars(count) {
    const amount = clampNumber(count, 0, 10000);
    const line = currentLine();
    while (line.chars.length < col) {
      line.chars.push(" ");
      line.styles.push(ANSI_DEFAULT_STYLE);
    }
    line.chars.splice(col, 0, ...Array(amount).fill(" "));
    line.styles.splice(col, 0, ...Array(amount).fill(style));
  }

  function deleteChars(count) {
    const amount = clampNumber(count, 0, 10000);
    const line = currentLine();
    line.chars.splice(col, amount);
    line.styles.splice(col, amount);
  }

  function eraseChars(count) {
    const amount = clampNumber(count, 0, 10000);
    const line = currentLine();
    for (let index = col; index < Math.min(col + amount, line.chars.length); index += 1) {
      line.chars[index] = " ";
      line.styles[index] = ANSI_DEFAULT_STYLE;
    }
  }

  function cursorTo(targetRow, targetCol) {
    row = clampNumber(targetRow, 0, Math.max(0, lines.length + 200));
    col = clampNumber(targetCol, 0, 10000);
    ensureRow(row);
  }

  function alternateScreen(active) {
    if (active) {
      if (altScreen) return;
      altScreen = { lines, row, col, originRow, savedCursor };
      lines = [blankLine()];
      row = 0;
      col = 0;
      originRow = 0;
      return;
    }
    if (!altScreen) return;
    ({ lines, row, col, originRow, savedCursor } = altScreen);
    altScreen = null;
    trimScrollback();
  }

  function csiParams(params) {
    const text = String(params || "");
    const parts = text.split(";").map((part) => (part === "" ? null : Number(part)));
    return parts;
  }

  function handleCsi(params, final) {
    const raw = String(params || "");
    const isPrivate = raw.startsWith("?");
    const clean = isPrivate ? raw.slice(1) : raw;
    const values = csiParams(clean);
    const first = (fallback = 1) => {
      const value = values[0];
      return Number.isFinite(value) ? value : fallback;
    };
    const mode = first(0);
    if (final === "m" && !isPrivate) {
      style = applySgr(style, clean);
      return;
    }
    if (final === "J") {
      eraseDisplay(mode);
      return;
    }
    if (final === "K") {
      eraseLine(mode);
      return;
    }
    if (final === "H" || final === "f") {
      const targetRow = Number.isFinite(values[0]) ? values[0] - 1 : 0;
      const targetCol = Number.isFinite(values[1]) ? values[1] - 1 : 0;
      // Absolute row addressing is relative to the PTY viewport, not to the
      // whole scrollback buffer.  originRow tracks the viewport's top line.
      cursorTo(originRow + clampNumber(targetRow, 0, viewportRows - 1), targetCol);
      return;
    }
    if (final === "A") { row = Math.max(0, row - first(1)); ensureRow(row); return; }
    if (final === "B") { row += first(1); ensureRow(row); trimScrollback(); return; }
    if (final === "C") { col += first(1); return; }
    if (final === "D") { col = Math.max(0, col - first(1)); return; }
    if (final === "E") { row += first(1); col = 0; ensureRow(row); trimScrollback(); return; }
    if (final === "F") { row = Math.max(0, row - first(1)); col = 0; ensureRow(row); return; }
    if (final === "G" || final === "`") { col = Math.max(0, first(1) - 1); return; }
    if (final === "d") { row = Math.max(0, first(1) - 1); ensureRow(row); return; }
    if (final === "S") { scrollUp(first(1)); return; }
    if (final === "T") { scrollDown(first(1)); return; }
    if (final === "L") { insertLines(first(1)); return; }
    if (final === "M") { deleteLines(first(1)); return; }
    if (final === "@") { insertChars(first(1)); return; }
    if (final === "P") { deleteChars(first(1)); return; }
    if (final === "X") { eraseChars(first(1)); return; }
    if (final === "s") { savedCursor = { row, col }; return; }
    if (final === "u") { row = savedCursor.row; col = savedCursor.col; ensureRow(row); return; }
    if (isPrivate && (final === "h" || final === "l")) {
      const modes = clean.split(";").map((part) => Number(part));
      for (const item of modes) {
        if (item === 1049 || item === 1047 || item === 47) {
          alternateScreen(final === "h");
        }
      }
    }
  }

  function consumeCsi(text, start) {
    for (let index = start + 1; index < text.length; index += 1) {
      const code = text.charCodeAt(index);
      if (code >= 0x40 && code <= 0x7e) {
        handleCsi(text.slice(start + 1, index), text[index]);
        return index + 1;
      }
    }
    return text.length;
  }

  function consumeEscape(text, start) {
    const next = text[start + 1];
    if (next === undefined) return text.length;
    if (next === "[") return consumeCsi(text, start + 1);
    if (next === "]" || next === "P" || next === "X" || next === "^" || next === "_") {
      return findStringEnd(text, start + 2);
    }
    if (next === "(" || next === ")" || next === "*" || next === "+" || next === "#" || next === "%") {
      return Math.min(text.length, start + 3);
    }
    if (next === "7") { savedCursor = { row, col }; return start + 2; }
    if (next === "8") {
      row = savedCursor.row;
      col = savedCursor.col;
      ensureRow(row);
      return start + 2;
    }
    if (next === "c") { reset(); return start + 2; }
    if (next === "D") { newline(); return start + 2; }
    if (next === "M") { row = row > 0 ? row - 1 : (scrollDown(1), row); return start + 2; }
    if (next === "E") { newline(); col = 0; return start + 2; }
    return start + 2;
  }

  function processText(text) {
    let index = 0;
    while (index < text.length) {
      const char = text[index];
      if (char === "\x1b") {
        index = consumeEscape(text, index);
        continue;
      }
      if (char === "\r") { col = 0; index += 1; continue; }
      if (char === "\n") { newline(); index += 1; continue; }
      if (char === "\b") { col = Math.max(0, col - 1); index += 1; continue; }
      if (char === "\t") {
        const spaces = 8 - (col % 8);
        for (let step = 0; step < spaces; step += 1) writeChar(" ");
        index += 1;
        continue;
      }
      const code = char.charCodeAt(0);
      if (code < 0x20 || code === 0x7f) { index += 1; continue; }
      writeChar(char);
      index += 1;
    }
    trimScrollback();
  }

  return {
    write(text, baseStyle = null) {
      const value = String(text || "");
      if (!value) return;
      if (baseStyle) {
        const previous = style;
        style = { ...ANSI_DEFAULT_STYLE, ...baseStyle };
        processText(value);
        style = previous;
      } else {
        processText(value);
      }
    },
    clear() { reset(); },
    setOrigin(rowIndex = row) {
      originRow = clampNumber(Number(rowIndex) || 0, 0, Math.max(0, lines.length - 1));
    },
    setRows(value) {
      viewportRows = clampNumber(Number(value) || 24, 1, 500);
      followViewport();
    },
    text() { return lines.map((line) => line.chars.join("")).join("\n"); },
    runs() {
      return lines.map((line) => {
        const out = [];
        let start = 0;
        for (let index = 1; index <= line.chars.length; index += 1) {
          const previous = line.styles[index - 1] || ANSI_DEFAULT_STYLE;
          const current = line.styles[index] || ANSI_DEFAULT_STYLE;
          if (index === line.chars.length || !sameAnsiStyle(previous, current)) {
            out.push({ text: line.chars.slice(start, index).join(""), style: previous });
            start = index;
          }
        }
        return out;
      });
    },
    get cursor() { return { row, col }; },
    get lineCount() { return lines.length; },
    get origin() { return originRow; },
    get rows() { return viewportRows; },
    get alternate() { return Boolean(altScreen); },
  };
}

export function stripAnsi(text = "") {
  return String(text || "")
    .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b[P^_X][\s\S]*?(?:\x07|\x1b\\)/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/\x1b[@-_]/g, "");
}

export function fitTerminalSize(options = {}) {
  const width = Math.max(0, Number(options.width) || 0);
  const height = Math.max(0, Number(options.height) || 0);
  const cellWidth = Number(options.cellWidth) || 0;
  const lineHeight = Number(options.lineHeight) || 0;
  const paddingX = Math.max(0, Number(options.paddingX) || 0);
  const paddingY = Math.max(0, Number(options.paddingY) || 0);
  const minCols = Number(options.minCols) || 20;
  const maxCols = Number(options.maxCols) || 400;
  const minRows = Number(options.minRows) || 5;
  const maxRows = Number(options.maxRows) || 120;
  if (!(cellWidth > 0) || !(lineHeight > 0) || !width || !height) {
    return { cols: 80, rows: 24 };
  }
  const cols = Math.floor((width - paddingX) / cellWidth);
  const rows = Math.floor((height - paddingY) / lineHeight);
  return {
    cols: Math.max(minCols, Math.min(maxCols, cols || minCols)),
    rows: Math.max(minRows, Math.min(maxRows, rows || minRows)),
  };
}

export function ptyBackendLabel(backend) {
  if (backend === "pywinpty") return "pywinpty";
  if (backend === "posix") return "POSIX pty";
  return "";
}

export function normalizeBaseRef(value) {
  const text = String(value || "").trim();
  return text || "HEAD";
}

export function sessionScopeLabel(session, { t = enText } = {}) {
  if (!session) return t("files.project_scope");
  return session.title || session.id || t("files.project_scope");
}
