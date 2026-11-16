// Project terminal (formerly the Execute › Files terminal drawer): run a
// command in the project folder through /api/workspace/terminals with
// streaming output, optional PTY (ANSI screen, live resize), stdin, cancel,
// presets and a timeout.  It is the workbench "Terminal" view; one panel is
// created per page and kept alive while hidden or moved.

import { h } from "./dom.js";
import { t, localizeServer } from "./i18n.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { toast } from "./components.js";
import {
  terminalRunning, terminalStatusLabel, createAnsiScreen, fitTerminalSize, ptyBackendLabel,
} from "./files-model.js";

export function createTerminalPanel() {
  let destroyed = false;
  let info = null;
  let termId = "";
  let termStatus = "idle";
  let termExit = null;
  let termTruncated = false;
  let termAbort = null;
  let termError = "";
  let termCwd = "";
  let termRunSeq = 0;
  let termPty = false;
  let termPtyAvailable = false;
  let termPtyHint = "";
  let termPtyUserSet = false;
  let termCols = 80;
  let termRows = 24;
  const termScreen = createAnsiScreen({ maxLines: 3000 });
  let termRenderQueued = false;
  let termResizeObserver = null;
  let termResizeTimer = null;
  let terminalCellCanvas = null;
  let infoLoaded = false;

  const root = h("section", { class: "fw-terminal wb-terminal", "aria-label": t("files.terminal") });
  const termOut = h("pre", { class: "fw-term-out", "aria-live": "polite", tabindex: "0" });
  const termStatusBadge = h("span", { class: "fw-term-state" });
  const termCwdLabel = h("span", { class: "fw-term-cwd" }, t("files.terminal_cwd"));
  const ptyToggle = h("input", {
    class: "fw-term-pty-check", type: "checkbox", checked: false, disabled: true,
    "aria-label": t("files.terminal_pty_toggle"),
  });
  const ptyLabel = h("label", { class: "fw-term-pty", title: t("files.terminal_pty_toggle") },
    ptyToggle, h("span", null, t("files.terminal_pty_toggle")));
  const termPtyNote = h("span", { class: "fw-term-note" }, t("files.terminal_pty"));
  const cmdInput = h("input", {
    class: "input fw-term-input", type: "text", spellcheck: false,
    placeholder: t("files.terminal_command_placeholder"),
    "aria-label": t("files.terminal_command"),
  });
  const presetSelect = h("select", {
    class: "select fw-term-preset", "aria-label": t("files.terminal_presets"),
  }, [
    h("option", { value: "" }, t("files.terminal_presets")),
    h("option", { value: "codex" }, "Codex"),
    h("option", { value: "claude" }, "Claude"),
    h("option", { value: "dsh" }, "DSH"),
  ]);
  presetSelect.addEventListener("change", () => {
    if (!presetSelect.value) return;
    cmdInput.value = presetSelect.value;
    cmdInput.focus();
    presetSelect.value = "";
  });
  const timeoutSelect = h("select", { class: "select fw-term-timeout", "aria-label": t("files.terminal_timeout") },
    [15, 30, 60, 120].map((value) => h("option", { value: String(value), selected: value === 60 }, `${value}s`)));
  const runBtn = h("button", { class: "btn sm primary fw-term-run", type: "button", onclick: () => runCommand() },
    icon("play", "sm"), t("files.terminal_run"));
  const cancelBtn = h("button", { class: "btn sm ghost fw-term-cancel", type: "button", onclick: () => cancelCommand() },
    icon("x", "sm"), t("action.cancel"));
  const clearBtn = h("button", { class: "btn sm ghost", type: "button", onclick: () => clearTerminal() },
    icon("trash", "sm"), t("action.clear"));
  const stdinInput = h("input", {
    class: "input fw-term-stdin", type: "text", spellcheck: false,
    placeholder: t("files.terminal_stdin"), "aria-label": t("files.terminal_stdin"),
  });
  const sendBtn = h("button", { class: "btn sm ghost fw-term-send", type: "button", onclick: () => sendStdin() },
    icon("arrowRight", "sm"), t("chat.send"));
  cmdInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); runCommand(); }
  });
  stdinInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); sendStdin(); }
  });
  ptyToggle.addEventListener("change", () => {
    termPtyUserSet = true;
    paintTerminalChrome();
  });
  const shellLabel = h("span", { class: "fw-term-cwd fw-term-shell" });
  root.append(
    h("div", { class: "fw-term-head" },
      termStatusBadge,
      termCwdLabel,
      shellLabel,
      h("span", { class: "spacer" }),
      ptyLabel,
      termPtyNote,
      clearBtn, cancelBtn),
    termOut,
    h("div", { class: "fw-term-compose" },
      presetSelect, timeoutSelect, cmdInput, runBtn),
    h("div", { class: "fw-term-compose secondary" },
      stdinInput, sendBtn));

  function terminalPtyNoteText() {
    if (termPty && terminalRunning(termStatus)) {
      return t("files.terminal_pty_size", { cols: termCols, rows: termRows });
    }
    if (!termPtyAvailable) {
      return termPtyHint || t("files.terminal_pty_unavailable");
    }
    const backend = ptyBackendLabel(info?.pty_backend);
    return backend
      ? t("files.terminal_pty_ready", { backend })
      : t("files.terminal_pty_ready_generic");
  }

  function applyTerminalInfo() {
    const available = Boolean(info?.pty_available);
    termPtyAvailable = available;
    termPtyHint = localizeServer(String(info?.pty_hint || info?.pty_reason || ""));
    ptyToggle.disabled = !available;
    if (!termPtyUserSet) ptyToggle.checked = available;
    ptyLabel.title = available
      ? terminalPtyNoteText()
      : (termPtyHint || t("files.terminal_pty_unavailable"));
    shellLabel.textContent = info?.shell ? t("files.shell", { shell: info.shell }) : "";
    paintTerminalChrome();
  }

  function paintTerminalChrome() {
    if (destroyed) return;
    const running = terminalRunning(termStatus);
    const busy = running || termStatus === "starting";
    termStatusBadge.className = `fw-term-state ${termStatus}`;
    termStatusBadge.textContent = terminalStatusLabel(termStatus, termExit, { t });
    runBtn.disabled = busy;
    cmdInput.disabled = busy;
    timeoutSelect.disabled = busy;
    ptyToggle.disabled = !termPtyAvailable || busy;
    cancelBtn.disabled = !running;
    sendBtn.disabled = !running;
    stdinInput.disabled = !running;
    termPtyNote.textContent = terminalPtyNoteText();
    termPtyNote.title = termPtyAvailable
      ? termPtyNote.textContent
      : (termPtyHint || t("files.terminal_pty_unavailable"));
    root.dataset.status = termStatus;
    root.dataset.terminalId = termId;
    root.dataset.pty = termPty ? "1" : "0";
    if (termError) termStatusBadge.title = termError;
    else termStatusBadge.removeAttribute("title");
    termOut.dataset.status = termStatus;
    termOut.dataset.pty = termPty ? "1" : "0";
  }

  function styleAnsiSpan(span, style) {
    if (!style) return;
    let fg = style.fg;
    let bg = style.bg;
    if (style.inverse) {
      fg = style.bg || "var(--bg2)";
      bg = style.fg || "var(--sub)";
    }
    if (fg) span.style.color = fg;
    if (bg) span.style.backgroundColor = bg;
    if (style.bold) span.style.fontWeight = "700";
    if (style.dim) span.style.opacity = "0.72";
    if (style.italic) span.style.fontStyle = "italic";
    if (style.underline) span.style.textDecoration = "underline";
  }

  function renderTerminalScreen() {
    const fragment = document.createDocumentFragment();
    const lines = termScreen.runs();
    lines.forEach((runs, index) => {
      for (const run of runs) {
        if (!run.text) continue;
        const span = document.createElement("span");
        span.textContent = run.text;
        styleAnsiSpan(span, run.style);
        fragment.appendChild(span);
      }
      if (index < lines.length - 1) fragment.appendChild(document.createTextNode("\n"));
    });
    termOut.replaceChildren(fragment);
    termOut.scrollTop = termOut.scrollHeight;
  }

  function scheduleTerminalRender() {
    if (destroyed || termRenderQueued) return;
    termRenderQueued = true;
    requestAnimationFrame(() => {
      termRenderQueued = false;
      if (!destroyed) renderTerminalScreen();
    });
  }

  function terminalStreamStyle(stream) {
    if (stream === "command") return { fg: "var(--accent-2)", bold: true };
    if (stream === "system") return { fg: "var(--mute)", italic: true };
    if (stream === "stdin") return { fg: "var(--amber)" };
    if (stream === "stderr") return { fg: "var(--red)" };
    return null;
  }

  function appendTerminalText(text, stream = "stdout") {
    if (!text) return;
    termScreen.write(text, terminalStreamStyle(stream));
    scheduleTerminalRender();
  }

  function clearTerminal() {
    termScreen.clear();
    termOut.replaceChildren();
  }

  function terminalFontMetrics() {
    if (!terminalCellCanvas) terminalCellCanvas = document.createElement("canvas");
    const computed = getComputedStyle(termOut);
    const fontSize = computed.fontSize || "11px";
    const fontFamily = computed.fontFamily || "monospace";
    const context = terminalCellCanvas.getContext("2d");
    if (context) {
      context.font = `${computed.fontStyle || "normal"} ${computed.fontWeight || "400"} `
        + `${fontSize} ${fontFamily}`;
    }
    const cellWidth = (context && context.measureText("M").width) || 7;
    let lineHeight = Number.parseFloat(computed.lineHeight);
    if (!Number.isFinite(lineHeight)) lineHeight = (Number.parseFloat(fontSize) || 11) * 1.5;
    const paddingX = (Number.parseFloat(computed.paddingLeft) || 0)
      + (Number.parseFloat(computed.paddingRight) || 0);
    const paddingY = (Number.parseFloat(computed.paddingTop) || 0)
      + (Number.parseFloat(computed.paddingBottom) || 0);
    return { cellWidth, lineHeight, paddingX, paddingY };
  }

  function fitCurrentTerminal() {
    return fitTerminalSize({
      width: termOut.clientWidth,
      height: termOut.clientHeight,
      ...terminalFontMetrics(),
    });
  }

  function sendTerminalResize() {
    if (!termPty || !termId || !terminalRunning(termStatus)) return;
    const next = fitCurrentTerminal();
    if (next.cols === termCols && next.rows === termRows) return;
    termCols = next.cols;
    termRows = next.rows;
    termScreen.setRows(termRows);
    api.workspaceTerminalResize(termId, termCols, termRows)
      .then((data) => {
        if (destroyed) return;
        if (data?.terminal) termPty = Boolean(data.terminal.pty);
        paintTerminalChrome();
      })
      .catch((err) => {
        if (!destroyed) termError = err.message;
      });
  }

  async function runCommand() {
    const command = cmdInput.value.trim();
    if (!command) return;
    if (terminalRunning(termStatus)) return;
    if (termAbort) { termAbort.abort(); termAbort = null; }
    if (termId) api.workspaceTerminalClose(termId).catch(() => {});
    const run = ++termRunSeq;
    const initialSize = fitCurrentTerminal();
    termCols = initialSize.cols;
    termRows = initialSize.rows;
    const wantPty = termPtyAvailable && ptyToggle.checked;
    if (termOut.textContent.trim() === t("files.terminal_idle")) clearTerminal();
    if (termOut.textContent && !termOut.textContent.endsWith("\n")) appendTerminalText("\n");
    termStatus = "starting";
    termExit = null;
    termTruncated = false;
    termError = "";
    termId = "";
    termCwd = "";
    termPty = false;
    appendTerminalText(`$ ${command}\n`, "command");
    paintTerminalChrome();
    try {
      const data = await api.workspaceTerminalStart({
        command,
        timeout: Number(timeoutSelect.value) || 60,
        pty: wantPty,
        cols: termCols,
        rows: termRows,
      });
      if (destroyed || run !== termRunSeq) {
        api.workspaceTerminalCancel(data.terminal.id).catch(() => {});
        return;
      }
      termId = data.terminal.id;
      termStatus = data.terminal.status;
      termCwd = data.terminal.cwd || "";
      termPty = Boolean(data.terminal.pty);
      if (Number.isFinite(Number(data.terminal.cols))) termCols = Number(data.terminal.cols);
      if (Number.isFinite(Number(data.terminal.rows))) termRows = Number(data.terminal.rows);
      termScreen.setRows(termRows);
      if (termPty) termScreen.setOrigin();
      termCwdLabel.textContent = termCwd ? `cwd: ${termCwd}` : t("files.terminal_cwd");
      if (wantPty && !termPty) {
        const reason = localizeServer(data.terminal.pty_fallback) || t("files.terminal_pty_unavailable");
        appendTerminalText(
          `[${t("files.terminal_pty_fallback_note", { reason })}]\n`, "system");
      }
      paintTerminalChrome();
      const controller = new AbortController();
      termAbort = controller;
      await api.workspaceTerminalStream(termId, 0, ({ event, data: payload }) => {
        if (destroyed || run !== termRunSeq) return;
        if (event === "output") {
          appendTerminalText(payload.text || "", payload.stream || "stdout");
        } else if (event === "state" || event === "done") {
          termStatus = payload.status || termStatus;
          termExit = payload.exit_code ?? termExit;
          termTruncated = Boolean(payload.truncated);
          termError = payload.error || "";
          if (payload.pty !== undefined) termPty = Boolean(payload.pty);
          if (Number.isFinite(Number(payload.cols))) termCols = Number(payload.cols);
          if (Number.isFinite(Number(payload.rows))) termRows = Number(payload.rows);
          paintTerminalChrome();
          if (event === "done") {
            appendTerminalText(`\n[${terminalStatusLabel(termStatus, termExit, { t })}]\n`, "system");
            if (termTruncated) appendTerminalText(`${t("files.diff_truncated")}\n`, "system");
          }
        }
      }, controller.signal);
    } catch (err) {
      if (err.name !== "AbortError" && !destroyed && run === termRunSeq) {
        termError = err.message;
        termStatus = "failed";
        toast(err.message, "err");
      }
    } finally {
      if (!destroyed && run === termRunSeq) {
        if (terminalRunning(termStatus)) termStatus = "exited";
        else if (termStatus === "starting") termStatus = "failed";
        paintTerminalChrome();
      }
    }
  }

  async function cancelCommand() {
    if (!termId || !terminalRunning(termStatus)) return;
    try {
      const data = await api.workspaceTerminalCancel(termId);
      termStatus = data.terminal?.status || "stopping";
      paintTerminalChrome();
    } catch (err) {
      toast(err.message, "err");
    }
  }

  async function sendStdin() {
    const text = stdinInput.value;
    if (!text || !termId || !terminalRunning(termStatus)) return;
    try {
      await api.workspaceTerminalStdin(termId, text);
      stdinInput.value = "";
    } catch (err) {
      toast(err.message, "err");
    }
  }

  async function loadInfo() {
    try {
      info = await api.workspaceInfo();
    } catch {
      info = null;
    }
    if (!destroyed) applyTerminalInfo();
  }

  if (typeof ResizeObserver === "function") {
    termResizeObserver = new ResizeObserver(() => {
      if (termResizeTimer) clearTimeout(termResizeTimer);
      termResizeTimer = setTimeout(() => {
        termResizeTimer = null;
        if (!destroyed) sendTerminalResize();
      }, 140);
    });
    termResizeObserver.observe(termOut);
  }
  paintTerminalChrome();

  return {
    el: root,
    // Called whenever the view is shown: first time loads PTY/shell info and
    // prints the idle hint; focus goes to the command line.
    shown() {
      if (!infoLoaded) { infoLoaded = true; loadInfo(); }
      if (termStatus === "idle" && !termOut.childNodes.length) appendTerminalText(`${t("files.terminal_idle")}\n`, "system");
      requestAnimationFrame(() => sendTerminalResize());
    },
    focus() { cmdInput.focus(); },
    running: () => terminalRunning(termStatus),
    destroy() {
      destroyed = true;
      termRunSeq += 1;
      if (termResizeTimer) clearTimeout(termResizeTimer);
      termResizeObserver?.disconnect();
      if (termAbort) { try { termAbort.abort(); } catch { /* noop */ } termAbort = null; }
      if (termId && terminalRunning(termStatus)) api.workspaceTerminalCancel(termId).catch(() => {});
    },
  };
}
