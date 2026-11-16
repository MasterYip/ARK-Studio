// Execute > Runs > Session context: internal and uploaded external agent
// sessions of this project, with transcript view, Continue and housekeeping.
// See docs/agents/session-context.md and docs/guide/session-context.md.
import { h, clear, richText, relativeTime, fmtDateTime, copyText, emptyState } from "./dom.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { api, cachedThenFresh } from "./api.js";
import { openModal, confirmDialog, toast } from "./components.js";
import { contextLinks, continueOptions, filterContexts, formatBytes, parseLinks, visibilityKey } from "./session-context-model.js";

const OPEN_KEY = "ark.sessionLibrary.open";

export const sessionContextApi = {
  list: () => api.sessionContexts(),
  transcript: (id) => api.sessionContextTranscript(id),
  context: (id) => api.sessionContextSummary(id),
  update: (id, body) => api.updateSessionContext(id, body),
  remove: (id) => api.deleteSessionContext(id),
  continue: (id, body) => api.continueSessionContext(id, body),
  rawUrl: (id) => api.sessionContextRawUrl(id),
};

function agentText(item) {
  const key = `tasks.platform.${item.agent}`;
  const label = t(key);
  const name = label === key ? (item.agent_label || item.agent || t("ctx.agent_unknown")) : label;
  return item.model ? `${name} · ${item.model}` : name;
}

function sourceChip(item) {
  const key = item.source === "external" ? "ctx.source.external" : (item.copy ? "ctx.source.copy" : "ctx.source.internal");
  return h("span", { class: `pill ${item.source === "external" ? "warn" : "ok"}`, "data-ctx-source": item.source,
    title: item.copy ? t("ctx.copy_hint") : undefined }, t(key));
}

function visibilityChip(item) {
  return h("span", { class: `pill ${item.shared ? "ok" : ""}`.trim(), "data-ctx-visibility": item.shared ? "shared" : "private",
    title: item.owner ? t("ctx.owner", { name: item.owner }) : undefined }, t(visibilityKey(item)));
}

async function setShared(item, shared, reload) {
  try {
    await sessionContextApi.update(item.id, { shared });
    toast(t(shared ? "ctx.shared_on" : "ctx.shared_off"), "ok");
    reload();
  } catch (err) { toast(err.message, "err"); }
}

function commandBox(command) {
  return h("div", { class: "ctx-command" },
    h("code", null, command),
    h("button", {
      class: "btn sm ghost", type: "button", "data-ctx-copy-command": "",
      onclick: async () => toast(t(await copyText(command) ? "ctx.copied" : "ctx.copy_failed"), "ok"),
    }, icon("copy", "sm"), t("ctx.copy")));
}

async function copyContext(item) {
  try {
    const data = await sessionContextApi.context(item.id);
    const ok = await copyText(data.summary || "");
    toast(t(ok ? "ctx.context_copied" : "ctx.copy_failed"), ok ? "ok" : "err");
  } catch (err) { toast(err.message, "err"); }
}

function transcriptItem(item) {
  if (item.type === "message") {
    return h("div", { class: `ctx-msg ${item.role === "user" ? "user" : "assistant"}` },
      h("div", { class: "ctx-msg-role" }, t(item.role === "user" ? "ctx.role.user" : "ctx.role.assistant"),
        item.at ? h("span", { class: "muted" }, fmtDateTime(item.at)) : null),
      item.markdown === false
        ? h("div", { class: "ctx-msg-body ctx-plain" }, item.text || "")
        : h("div", { class: "ctx-msg-body" }, richText(item.text || "")));
  }
  if (item.type === "tool") {
    const text = [item.input, item.output].filter((v) => v !== undefined && v !== null && v !== "")
      .map((v) => (typeof v === "string" ? v : JSON.stringify(v, null, 2))).join("\n\n");
    return h("details", { class: "ctx-tool" },
      h("summary", null, icon("terminal", "sm"), h("b", null, item.name || t("ctx.tool")),
        item.status ? h("span", { class: "muted" }, item.status) : null),
      h("pre", null, text.slice(0, 8000)));
  }
  if (item.type === "error") return h("div", { class: "ctx-note bad" }, icon("alert", "sm"), item.text || "");
  if (item.type === "notice" || item.type === "reasoning") {
    return h("div", { class: "ctx-note" }, item.text || "");
  }
  return null;
}

export async function openTranscript(item) {
  const body = h("div", { class: "ctx-transcript", "data-ctx-transcript": item.id }, h("p", { class: "muted" }, t("ctx.loading")));
  const meta = h("div", { class: "ctx-meta" },
    sourceChip(item),
    h("span", null, agentText(item)),
    h("code", null, item.id),
    item.native_session_id ? h("span", { class: "muted" }, t("ctx.native_id", { id: item.native_session_id })) : null,
    item.cwd ? h("span", { class: "muted" }, item.cwd) : null);
  openModal({
    title: item.title || item.id,
    wide: true,
    body: h("div", { class: "stack" }, meta, body),
    actions: [
      { label: t("ctx.copy_context"), ghost: true, close: false, onClick: () => copyContext(item) },
      { label: t("ctx.download"), ghost: true, close: false, onClick: () => { window.open(sessionContextApi.rawUrl(item.id), "_blank"); } },
      { label: t("action.close"), primary: true },
    ],
  });
  try {
    const data = await sessionContextApi.transcript(item.id);
    clear(body);
    const nodes = (data.items || []).map(transcriptItem).filter(Boolean);
    if (!nodes.length) body.appendChild(h("p", { class: "muted" }, t("ctx.transcript_empty")));
    const shown = nodes.slice(-400);
    if (nodes.length > shown.length) body.appendChild(h("p", { class: "muted" }, t("ctx.transcript_trimmed", { count: nodes.length - shown.length })));
    body.append(...shown);
  } catch (err) {
    clear(body);
    body.appendChild(h("p", { class: "ctx-note bad" }, err.message));
  }
}

async function openContinue(ctx, item, reload) {
  const options = continueOptions(item);
  let presets = [];
  try { presets = ((await api.sessionPresets()).presets || []).filter((p) => p.available); } catch { /* keep empty */ }
  const text = h("textarea", { class: "input", rows: 4, "data-ctx-continue-text": "", placeholder: t("ctx.continue_placeholder") });
  const platform = h("select", { class: "select", "data-ctx-platform": "" },
    presets.map((p) => h("option", { value: p.id, selected: p.id === item.agent }, p.label || p.id)));
  const start = async (mode) => {
    try {
      const body = { mode, text: text.value.trim() };
      if (mode === "seed") body.platform = platform.value;
      const data = await sessionContextApi.continue(item.id, body);
      toast(t(data.mode === "resume" ? "ctx.resumed" : "ctx.seeded"), "ok");
      ctx.S.selectedSessionId = data.session?.id || ctx.S.selectedSessionId;
      ctx.S.sessionThreadMode = "thread";
      reload();
      ctx.refresh?.();
      return true;
    } catch (err) { toast(err.message, "err"); return false; }
  };
  const sections = [];
  if (options.command) {
    sections.push(h("div", { class: "stack tight" },
      h("b", null, t(item.source === "external" ? "ctx.resume_terminal" : "ctx.resume_terminal_internal")),
      commandBox(options.command)));
  } else if (item.source === "external") {
    sections.push(h("p", { class: "muted" }, t("ctx.no_native_command")));
  }
  sections.push(h("div", { class: "stack tight" },
    h("b", null, t("ctx.continue_in_ark")),
    h("p", { class: "muted" }, t(options.nativeResume ? "ctx.continue_native_hint" : "ctx.continue_seed_hint")),
    text,
    options.nativeResume ? null : h("label", { class: "row tight" }, h("span", null, t("ctx.platform")), platform)));
  const actions = [
    { label: t("ctx.copy_context"), ghost: true, close: false, onClick: () => copyContext(item) },
    { spacer: true },
    { label: t("action.cancel"), ghost: true },
  ];
  if (options.nativeResume) {
    actions.push({ label: t("ctx.new_seeded"), ghost: true, onClick: () => openSeedOnly() });
    actions.push({ label: t("ctx.resume_native"), primary: true, onClick: () => (text.value.trim() ? start("resume") : (toast(t("ctx.text_required"), "err"), false)) });
  } else {
    actions.push({ label: t("ctx.start_seeded"), primary: true, disabled: !presets.length, onClick: () => start("seed") });
  }
  function openSeedOnly() {
    setTimeout(() => openContinue(ctx, { ...item, resume: { ...(item.resume || {}), native: false } }, reload), 0);
  }
  openModal({ title: t("ctx.continue_title", { title: item.title || item.id }), wide: true,
    body: h("div", { class: "stack", "data-ctx-continue": item.id }, ...sections), actions });
}

function openEdit(item, field, reload) {
  const input = h("input", {
    class: "input", "data-ctx-edit": field,
    value: field === "title" ? (item.title || "") : (item.linked_units || []).join(", "),
    placeholder: field === "title" ? t("ctx.rename_placeholder") : t("ctx.link_placeholder"),
  });
  openModal({
    title: t(field === "title" ? "ctx.rename" : "ctx.link"),
    body: h("div", { class: "stack" }, field === "title" ? null : h("p", { class: "muted" }, t("ctx.link_hint")), input),
    enterConfirm: true,
    actions: [
      { label: t("action.cancel"), ghost: true },
      {
        label: t("action.save"), primary: true,
        onClick: async () => {
          const body = field === "title" ? { title: input.value } : { linked_units: parseLinks(input.value) };
          try { await sessionContextApi.update(item.id, body); reload(); return true; } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

async function remove(item, reload) {
  const ok = await confirmDialog(t(item.source === "external" ? "ctx.delete_confirm_external" : "ctx.delete_confirm_internal",
    { title: item.title || item.id }), { danger: true, confirmLabel: t("action.delete") });
  if (!ok) return;
  try { await sessionContextApi.remove(item.id); toast(t("ctx.deleted"), "ok"); reload(); } catch (err) { toast(err.message, "err"); }
}

function row(ctx, item, reload) {
  const links = contextLinks(item);
  const more = h("details", { class: "ctx-more" },
    h("summary", { class: "icon-btn", title: t("ctx.more"), "aria-label": t("ctx.more") }, icon("more", "sm")),
    h("div", { class: "ctx-more-menu" },
      item.can_manage ? h("button", { class: "btn sm ghost", type: "button", "data-ctx-share": item.shared ? "off" : "on",
        onclick: () => setShared(item, !item.shared, reload) },
        icon(item.shared ? "user" : "external", "sm"), t(item.shared ? "ctx.unshare" : "ctx.share")) : null,
      item.can_manage ? h("button", { class: "btn sm ghost", type: "button", onclick: () => openEdit(item, "title", reload) }, icon("edit", "sm"), t("ctx.rename")) : null,
      item.can_manage ? h("button", { class: "btn sm ghost", type: "button", onclick: () => openEdit(item, "links", reload) }, icon("link", "sm"), t("ctx.link")) : null,
      h("a", { class: "btn sm ghost", href: sessionContextApi.rawUrl(item.id), download: `${item.id}.jsonl` }, icon("download", "sm"), t("ctx.download")),
      item.can_manage ? h("button", { class: "btn sm ghost danger", type: "button", disabled: item.active, onclick: () => remove(item, reload) }, icon("trash", "sm"), t("action.delete")) : null));
  // Open upward when the menu would run under the status bar / viewport edge.
  more.addEventListener("toggle", () => {
    if (!more.open) return;
    more.classList.remove("up");
    const menu = more.querySelector(".ctx-more-menu");
    const rect = menu.getBoundingClientRect();
    if (rect.bottom > window.innerHeight - 48) more.classList.add("up");
  });
  return h("div", { class: "ctx-row", "data-ctx-row": item.id },
    h("div", { class: "ctx-row-main" },
      h("div", { class: "ctx-row-title" }, sourceChip(item), visibilityChip(item), h("b", null, item.title || item.id),
        item.title ? h("code", { class: "muted" }, item.id) : null),
      h("div", { class: "ctx-row-sub muted" },
        h("span", null, agentText(item)),
        item.owner && !item.can_manage ? h("span", null, t("ctx.owner", { name: item.owner })) : null,
        h("span", { title: fmtDateTime(item.updated_at) }, relativeTime(item.updated_at)),
        h("span", null, formatBytes(item.bytes)),
        item.redactions ? h("span", null, t("ctx.redactions", { count: item.redactions })) : null,
        ...links.map((uid) => h("button", {
          class: "chip", type: "button", "data-ctx-link": uid,
          onclick: () => ctx.openUnit?.(uid),
        }, uid)))),
    h("div", { class: "ctx-row-actions" },
      h("button", { class: "btn sm ghost", type: "button", "data-ctx-view": item.id, onclick: () => openTranscript(item) }, icon("eye", "sm"), t("ctx.view")),
      h("button", { class: "btn sm primary", type: "button", "data-ctx-continue-btn": item.id, disabled: item.active,
        title: item.active ? t("ctx.active_hint") : "", onclick: () => openContinue(ctx, item, reload) }, icon("play", "sm"), t("ctx.continue")),
      more));
}

export function sessionLibraryPanel(ctx) {
  let data = null;
  let source = "all";
  let query = "";
  let open = localStorage.getItem(OPEN_KEY) !== "0";
  const list = h("div", { class: "ctx-list" });
  const count = h("span", { class: "sw-count" });
  const usage = h("span", { class: "muted ctx-usage" });
  const consent = h("input", { type: "checkbox", "data-ctx-consent": "" });
  const consentLabel = h("label", { class: "row tight ctx-consent", title: t("ctx.consent_hint") }, consent, h("span", null, t("ctx.consent")));
  consent.addEventListener("change", async () => {
    const value = consent.checked;
    try {
      const result = await api.updateSettings({ sessions: { accept_uploads: value } });
      if (ctx.S.project) ctx.S.project.settings = result.settings || ctx.S.project.settings;
      toast(t(value ? "ctx.consent_on" : "ctx.consent_off"), "ok");
    } catch (err) {
      consent.checked = !value;
      toast(err.message, "err");
    }
  });
  const filterBar = h("div", { class: "sw-filterbar" });
  const filterButtons = {};
  for (const id of ["all", "internal", "external"]) {
    filterButtons[id] = h("button", {
      class: `sw-filter ${id === source ? "on" : ""}`.trim(), type: "button", "data-ctx-filter": id,
      onclick: () => {
        source = id;
        for (const [key, btn] of Object.entries(filterButtons)) btn.classList.toggle("on", key === id);
        paint();
      },
    }, t(`ctx.filter.${id}`));
    filterBar.appendChild(filterButtons[id]);
  }
  const search = h("input", { class: "input sw-search", type: "search", placeholder: t("ctx.search"),
    "aria-label": t("ctx.search"), oninput: (e) => { query = e.target.value; paint(); } });
  const bodyEl = h("div", { class: "ctx-body", hidden: !open },
    h("div", { class: "ctx-tools" }, search, filterBar, h("span", { class: "spacer" }), consentLabel),
    list,
    h("p", { class: "muted ctx-foot" }, t("ctx.foot")));
  const toggle = h("button", { class: "link", type: "button", "data-ctx-toggle": "",
    onclick: () => {
      open = !open;
      bodyEl.hidden = !open;
      localStorage.setItem(OPEN_KEY, open ? "1" : "0");
      toggle.textContent = open ? t("start.hide") : t("start.show");
      if (open && !data) load();
    } }, open ? t("start.hide") : t("start.show"));
  const panel = h("section", { class: "panel ctx-panel", "data-session-library": "" },
    h("div", { class: "panel-head" },
      h("div", { class: "ctx-title" }, icon("layers", "sm"), h("h3", null, t("ctx.title")), count, usage),
      h("div", { class: "row tight" },
        h("button", { class: "icon-btn", type: "button", title: t("action.refresh"), "aria-label": t("action.refresh"), onclick: () => load() }, icon("refresh", "sm")),
        toggle)),
    bodyEl);

  function paint() {
    clear(list);
    const items = filterContexts(data?.sessions || [], { source, query });
    count.textContent = String((data?.sessions || []).length);
    if (!data) { list.appendChild(h("p", { class: "muted" }, t("ctx.loading"))); return; }
    if (!items.length) {
      list.appendChild(emptyState(t("ctx.empty"), t(data.settings?.accept_uploads ? "ctx.empty_hint_on" : "ctx.empty_hint_off")));
      return;
    }
    for (const item of items) list.appendChild(row(ctx, item, load));
  }

  async function load() {
    try {
      // Only what the server returned to this account is cached; shown at
      // once, then replaced by the current list.
      await cachedThenFresh("/api/session-contexts", (options) => api.sessionContexts(options), (value, { cached }) => {
        data = value;
        consent.checked = Boolean(data.settings?.accept_uploads);
        usage.textContent = t("ctx.usage", { used: formatBytes(data.usage?.bytes), limit: formatBytes(data.usage?.limit) });
        if (cached) paint();
      });
    } catch (err) {
      data = { sessions: [], settings: {} };
      toast(err.message, "err");
    }
    paint();
    // While an ARK session runs, refresh so Continue/Delete unlock when it ends.
    clearTimeout(activeTimer);
    if ((data.sessions || []).some((item) => item.active)) {
      activeTimer = setTimeout(() => { if (panel.isConnected && open) load(); }, 3000);
    }
  }
  let activeTimer = null;

  paint();
  if (open) load();
  return panel;
}
