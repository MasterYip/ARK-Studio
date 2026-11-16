import { h, clear, fmtDateTime, richText } from "./dom.js";
import { api } from "./api.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { openModal, toast } from "./components.js";
import { S } from "./state.js";
import { canAssign } from "./assign-menu.js";
import { announceUnitSaved, syncUnitLocally, UNIT_RESTORE_CHECK_EVENT } from "./unit-edit.js";
// The diff view (and its diff library) loads when a diff is first shown.
const loadDiffView = () => import("./history-diff-view.js");

const HISTORY_VIEWS = ["diff", "preview"];
let appContext = null;
export function setHistoryContext(ctx) { appContext = ctx; }

export function historyMenuItem(unit, ctx = null) {
  return { label: t("history.title"), icon: "clock", onClick: () => openNodeHistory(unit, ctx) };
}

export function openNodeHistory(unit, ctx = null) {
  ctx ||= appContext;
  const uid = unit.uid || unit.id, project = S.project?.id;
  const writable = canAssign(S);
  let state = null, offset = 0, from = "", to = "", view = "diff", busy = false, sequence = 0;
  const enabled = h("input", { type: "checkbox", disabled: true, onchange: async () => {
    enabled.disabled = true;
    try { await api.setNodeHistory(uid, enabled.checked, project); await load(); }
    catch (error) { enabled.checked = Boolean(state?.enabled); toast(error.message, "err"); }
    finally { enabled.disabled = !writable || !state?.available; }
  } });
  const status = h("span", { class: "muted" });
  const errorBox = h("div", { class: "history-error", role: "alert", hidden: true });
  const content = h("div", { class: "history-content", tabindex: "0", "aria-label": t("history.title") });
  const selectors = h("div", { class: "history-selectors" });
  const pages = h("span", { class: "muted" });
  const previous = h("button", { class: "icon-btn", title: t("history.newer"), "aria-label": t("history.newer"),
    onclick: () => { offset = Math.max(0, offset - 50); from = to = ""; load(); } }, icon("chevronLeft", "sm"));
  const next = h("button", { class: "icon-btn", title: t("history.older"), "aria-label": t("history.older"),
    onclick: () => { offset += 50; from = to = ""; load(); } }, icon("chevronRight", "sm"));
  const refresh = h("button", { class: "icon-btn", title: t("action.refresh"), "aria-label": t("action.refresh"),
    onclick: () => load() }, icon("refresh", "sm"));
  const saveVersion = h("button", { class: "btn sm", disabled: true, onclick: performCheckpoint },
    icon("save", "sm"), t("history.save_version"));
  const confirmation = h("div", { class: "history-confirm", hidden: true });
  const restore = h("button", { class: "btn sm", disabled: true, onclick: () => {
    if (!canRestore()) return;
    confirmation.hidden = false;
    clear(confirmation).append(h("span", null, t("history.confirm")),
      h("button", { class: "btn sm primary", onclick: performRestore }, icon("refresh", "sm"), t("history.restore")),
      h("button", { class: "btn sm ghost", onclick: () => { confirmation.hidden = true; } }, t("action.cancel")));
  } }, icon("refresh", "sm"), t("history.restore"));
  const modes = h("div", { class: "history-modes", role: "tablist" }, HISTORY_VIEWS.map((id) => h("button", {
    class: "btn sm ghost", role: "tab", "aria-selected": id === view ? "true" : "false",
    onclick: (event) => {
      view = id;
      for (const button of modes.children) button.setAttribute("aria-selected", button === event.currentTarget ? "true" : "false");
      showRevision();
    },
  }, icon(id === "diff" ? "git" : "eye", "sm"), t(`history.${id}`))));
  const body = h("div", { class: "node-history" },
    h("div", { class: "history-settings" }, h("label", null, enabled, t("history.enabled")), status),
    errorBox, selectors,
    h("div", { class: "history-toolbar" }, modes, h("span", { class: "spacer" }), pages, previous, next, refresh, saveVersion, restore),
    confirmation, content);
  const modal = openModal({ title: `${t("history.title")} · ${uid}`, body, wide: true });
  modal.backdrop.classList.add("history-backdrop");
  modal.modal.classList.add("history-modal");
  load();

  function fail(error) {
    errorBox.textContent = error.message || String(error);
    errorBox.hidden = false;
  }
  function canRestore() {
    const event = new CustomEvent(UNIT_RESTORE_CHECK_EVENT, { cancelable: true, detail: { id: state?.id, uid, project } });
    if (!window.dispatchEvent(event)) { toast(t("history.unsaved"), "err"); return false; }
    return true;
  }
  async function load() {
    if (busy) return;
    busy = true;
    const previousHead = state?.head;
    enabled.disabled = true;
    refresh.disabled = previous.disabled = next.disabled = restore.disabled = saveVersion.disabled = true;
    errorBox.hidden = true;
    try {
      state = await api.nodeHistory(uid, { limit: 50, offset }, project);
      if (!modal.backdrop.isConnected) return;
      enabled.checked = state.enabled;
      status.textContent = !state.available ? t("history.git_required")
        : state.enabled ? t(state.pending ? "history.pending" : "history.text_only") : t("history.paused");
      previous.disabled = offset === 0;
      next.disabled = !state.has_more;
      const versions = state.versions;
      pages.textContent = versions.length ? `${offset + 1}–${offset + versions.length}` : "";
      clear(selectors);
      if (!versions.length) { clear(content).append(h("div", { class: "empty" }, t("history.empty"))); return; }
      if (!versions.some((v) => v.id === to) || (offset === 0 && to === previousHead)) to = versions[0].id;
      if (from && !versions.some((v) => v.id === from)) from = "";
      const options = () => versions.map((v) => h("option", { value: v.id },
        `${fmtDateTime(v.created_at)} · ${v.author} · ${v.id.slice(0, 8)}`));
      const fromSelect = h("select", { "aria-label": t("history.from"), onchange: () => {
        from = fromSelect.value; showRevision();
      } }, h("option", { value: "" }, t("history.previous")), options());
      const toSelect = h("select", { "aria-label": t("history.to"), onchange: () => {
        to = toSelect.value; showRevision();
      } }, options());
      fromSelect.value = from; toSelect.value = to;
      selectors.append(h("label", null, t("history.from"), fromSelect), h("label", null, t("history.to"), toSelect));
      await showRevision();
    } catch (error) { fail(error); }
    finally {
      busy = false; refresh.disabled = false; enabled.disabled = !writable || !state?.available;
      saveVersion.disabled = !writable || !state?.enabled || !state?.available;
    }
  }
  async function showRevision() {
    const request = ++sequence;
    for (const button of content.querySelectorAll(".history-apply")) button.disabled = true;
    confirmation.hidden = true;
    restore.disabled = true;
    errorBox.hidden = true;
    try {
      const [data, diffView] = await Promise.all([
        view === "diff" ? api.nodeDiff(uid, { from, to }, project) : api.nodeRevision(uid, to, project),
        view === "diff" ? loadDiffView() : null,
      ]);
      if (request !== sequence || !modal.backdrop.isConnected) return;
      clear(content);
      if (view === "preview") {
        content.append(h("h4", null, data.revision.title), richText(data.revision.body, {
          vizScope: `history:${uid}:${to}`, mediaKinds: state.media_kinds,
        }));
      } else {
        if (!data.diff) content.append(h("div", { class: "empty" }, t("history.no_diff")));
        else content.append(diffView.renderHistoryDiff(data.diff, writable && state.enabled ? {
          canApply: data.can_apply,
          onApply: (selection) => performApply({ ...selection, from: data.from, to: data.to, expected_hash: data.current_hash }),
        } : {}));
      }
      restore.disabled = !writable || !state.enabled || !state.available;
    } catch (error) { if (request === sequence) fail(error); }
  }
  function disableMutationControls() {
    for (const element of body.querySelectorAll("button,select,input")) element.disabled = true;
    ++sequence;
  }
  async function performCheckpoint() {
    if (busy || !canRestore()) return;
    busy = true;
    disableMutationControls();
    let error = null;
    try {
      await api.saveNodeVersion(uid, state.current_hash, project);
      toast(t("history.version_saved"), "ok");
      offset = 0; to = "";
    } catch (failure) { error = failure; }
    finally {
      busy = false; await load();
      for (const button of modes.children) button.disabled = false;
      if (error) fail(error);
    }
  }
  async function performApply(selection) {
    if (busy || !canRestore()) return;
    busy = true;
    disableMutationControls();
    let error = null;
    try {
      const data = await api.applyNodeHistoryChange(uid, selection, project);
      if (S.project?.id === project) {
        syncUnitLocally(ctx || { S }, data.unit);
        announceUnitSaved(data.unit, "history");
      }
      toast(t("history.applied"), "ok");
      offset = 0; from = selection.from; to = "";
    } catch (failure) { error = failure; }
    finally {
      busy = false; await load();
      for (const button of modes.children) button.disabled = false;
      if (error) fail(error);
    }
  }
  async function performRestore() {
    if (busy || !canRestore()) return;
    busy = true;
    for (const button of confirmation.querySelectorAll("button")) button.disabled = true;
    try {
      const data = await api.restoreNodeRevision(uid, { revision: to, expected_hash: state.current_hash }, project);
      if (S.project?.id === project) {
        syncUnitLocally(ctx || { S }, data.unit);
        announceUnitSaved(data.unit, "history");
      }
      toast(t("history.restored"), "ok");
      offset = 0; from = to = "";
      busy = false;
      await load();
    } catch (error) { fail(error); }
    finally { busy = false; confirmation.hidden = true; }
  }
}
