// Settings panels for local provider discovery and project worktrees.
//
// This module only renders what the server reports: it never installs a CLI,
// starts a login flow, or reads a credential value.  Refreshing re-runs the
// read-only probe; removing a worktree always goes through a danger dialog.

import { h, clear, relativeTime } from "./dom.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { t, localizeServer } from "./i18n.js";
import { confirmDialog } from "./components.js";
import { providerStatusLabel } from "./session-model.js";
import { foldSection } from "./fold-section.js";

function statusPill(status) {
  return h("span", { class: `provider-status ${status || "unknown"}`.trim() },
    h("i"), providerStatusLabel(status || "unknown", { t }));
}

// Known API values (auth state, model source) get a label; unknown ones show as sent.
function providerValue(kind, value) {
  const key = `providers.${kind}.${value}`;
  const text = t(key);
  return text === key ? value : text;
}

function capabilityChips(capabilities = {}) {
  const chips = [];
  const models = capabilities.models || [];
  const source = capabilities.models_source && capabilities.models_source !== "provider_default"
    ? ` · ${providerValue("source", capabilities.models_source)}` : "";
  chips.push(h("span", { class: "provider-cap", title: localizeServer(capabilities.models_detail || "") },
    icon("sparkles", "sm"),
    models.length
      ? t(models.length === 1 ? "providers.cap.models_one" : "providers.cap.models", { n: models.length, source })
      : t("providers.cap.default_model")));
  const efforts = capabilities.efforts || [];
  chips.push(h("span", { class: "provider-cap" },
    icon("clock", "sm"), efforts.length ? efforts.join(" / ") : t("providers.cap.no_efforts")));
  const modes = capabilities.permission_modes || [];
  chips.push(h("span", { class: "provider-cap" },
    icon("shield", "sm"), modes.length ? modes.join(" / ") : t("providers.cap.no_modes")));
  chips.push(h("span", { class: "provider-cap" },
    icon("message", "sm"), capabilities.streaming === "line_buffered" ? t("providers.cap.line_buffered") // i18n-ignore: API value
      : (capabilities.streaming || t("common.unknown"))));
  chips.push(h("span", { class: "provider-cap" },
    icon("alert", "sm"), capabilities.approval?.supported ? t("providers.cap.stdin_approvals") : t("providers.cap.no_approvals")));
  return h("div", { class: "provider-caps" }, chips);
}

function providerCard(provider) {
  const capabilities = provider.capabilities || {};
  const auth = provider.auth || {};
  const models = capabilities.models || [];
  return h("article", { class: `provider-card ${provider.status}`.trim() },
    h("div", { class: "provider-card-head" },
      h("span", { class: `provider-dot ${provider.status}`.trim() }),
      h("div", { class: "provider-title" },
        h("b", null, provider.label || provider.id),
        h("span", { class: "provider-id" }, provider.id)),
      provider.version ? h("span", { class: "provider-version" }, provider.version) : null,
      statusPill(provider.status)),
    h("div", { class: "provider-auth" },
      icon("shield", "sm"),
      h("span", null, localizeServer(auth.detail || provider.health?.detail || ""))),
    h("div", { class: "provider-meta" },
      h("span", { title: provider.command || "" }, h("code", null, provider.command || "—")),
      h("span", null, t(provider.installed ? "providers.meta.install_found" : "providers.meta.install_missing")),
      h("span", null, t("providers.meta.auth", { state: auth.state ? providerValue("auth", auth.state) : t("common.unknown") })),
      auth.sources?.length ? h("span", { class: "provider-sources" }, t("providers.meta.sources", { sources: auth.sources.join(", ") })) : null),
    capabilityChips(capabilities),
    models.length
      ? h("div", { class: "provider-models" }, models.map((model) => h("code", null, model)))
      : h("div", { class: "provider-models muted" }, t("providers.no_model_list")),
    provider.version_error ? h("div", { class: "provider-note" }, localizeServer(provider.version_error)) : null);
}

export function renderProvidersPanel() {
  const status = h("span", { class: "settings-status" });
  const list = h("div", { class: "provider-list" }, h("div", { class: "settings-lead" }, t("providers.probing")));
  const note = h("div", { class: "settings-note" }, icon("shield", "sm"),
    h("div", null, h("b", null, t("providers.note_title")), t("providers.note_body")));
  let loading = false;

  const refreshBtn = h("button", { class: "ghost-btn" }, icon("refresh", "sm"), t("providers.refresh_catalog"));
  // The catalog is a read-only probe result: folded behind its counts.
  const catalog = foldSection({ id: "provider-catalog", title: t("settings.fold.provider_catalog"),
    summary: t("providers.probing"), body: list });

  const paint = (data) => {
    const providers = data.providers || [];
    const counts = data.counts || {};
    clear(list);
    clear(status);
    const summary = Object.entries(counts).map(([key, value]) =>
      h("span", { class: `provider-count ${key}`.trim() }, `${value} ${providerStatusLabel(key, { t })}`));
    status.append(...summary);
    catalog.setSummary(t("settings.fold.count", { n: providers.length }));
    if (!providers.length) {
      list.appendChild(h("div", { class: "settings-lead" }, t("providers.no_presets")));
      return;
    }
    for (const provider of providers) list.appendChild(providerCard(provider));
  };

  const load = async (force = false) => {
    if (loading) return;
    loading = true;
    refreshBtn.disabled = true;
    try {
      const data = force ? await api.refreshProviders() : await api.providers();
      paint(data);
      note.title = t(data.cached ? "providers.last_checked_cached" : "providers.last_checked",
        { time: relativeTime(data.generated_at) || data.generated_at || "" });
    } catch (err) {
      clear(list);
      list.appendChild(h("div", { class: "notice" }, icon("alert", "sm"), h("div", null, err.message)));
      clear(status);
      status.appendChild(h("span", { class: "pill err" }, t("providers.discovery_unavailable")));
    } finally {
      loading = false;
      refreshBtn.disabled = false;
    }
  };
  refreshBtn.addEventListener("click", () => load(true));

  const panel = h("section", { class: "panel settings-panel provider-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("providers.title")),
      status),
    h("p", { class: "settings-lead" }, t("providers.lead")),
    h("div", { class: "assistant-api-actions" }, refreshBtn),
    catalog.el,
    note);
  load(false);
  return panel;
}

function worktreeStatusLabel(status) {
  const key = `worktrees.status.${status}`;
  const label = t(key);
  return label === key ? status : label;
}

function worktreeRow(item, reload, notify) {
  const active = item.active_count || 0;
  const dirty = Number(item.dirty) || 0;
  const badges = [];
  badges.push(h("span", { class: `worktree-state ${item.status || ""}`.trim() }, worktreeStatusLabel(item.status || "clean")));
  if (dirty) badges.push(h("span", { class: "worktree-state dirty" }, t("worktrees.changed", { n: dirty })));
  if (active) badges.push(h("span", { class: "worktree-state busy" },
    t(active === 1 ? "worktrees.active_sessions_one" : "worktrees.active_sessions", { n: active })));
  if (item.conflict) badges.push(h("span", { class: "worktree-state conflict" }, t("worktrees.status.conflict")));
  const remove = h("button", {
    class: "btn sm ghost danger",
    title: active ? t("worktrees.remove_blocked") : t("worktrees.remove_title"),
    disabled: active > 0,
    onclick: async () => {
      const confirmed = await confirmDialog(
        t("worktrees.remove_confirm", { name: item.branch || item.name }),
        { title: t("worktrees.remove"), confirmLabel: t("worktrees.remove"), danger: true });
      if (!confirmed) return;
      try {
        await api.removeWorktree({ path: item.path, confirm: true });
        reload();
      } catch (err) {
        // A dirty worktree is a deliberate refusal; report it without forcing.
        notify?.(err.message);
      }
    },
  }, icon("trash", "sm"), t("action.remove"));
  return h("div", { class: `worktree-row ${item.status || ""}`.trim() },
    h("div", { class: "worktree-main" },
      h("div", { class: "worktree-title" },
        icon("git", "sm"),
        h("b", null, item.branch || item.name),
        ...badges),
      h("div", { class: "worktree-path", title: item.path }, item.path),
      h("div", { class: "worktree-meta" },
        h("span", null, `head ${item.head || "—"}`), // i18n-ignore: Git term
        h("span", null, item.active_sessions?.length
          ? t("worktrees.sessions", { ids: item.active_sessions.join(", ") }) : t("worktrees.no_active_session")),
        item.clean ? h("span", null, t("worktrees.status.clean"))
          : h("span", { class: "warn" }, t("worktrees.uncommitted")))),
    h("div", { class: "worktree-actions" }, remove));
}

export function renderWorktreesPanel() {
  const status = h("span", { class: "settings-status" });
  const facts = h("div", { class: "settings-lead" }, t("worktrees.loading"));
  const list = h("div", { class: "worktree-list" });
  const listFold = foldSection({ id: "worktree-list", title: t("settings.fold.worktrees"),
    summary: t("worktrees.loading"), body: list });
  const nameInput = h("input", { class: "input", placeholder: "feature-login", "aria-label": t("worktrees.name_aria") });
  const baseInput = h("input", { class: "input", placeholder: t("worktrees.base_placeholder"), "aria-label": t("worktrees.base") });
  const createBtn = h("button", { class: "primary-btn" }, icon("plus", "sm"), t("worktrees.create"));
  const message = h("span", { class: "settings-status" });
  let loading = false;
  let available = false;

  const paint = (data) => {
    clear(facts);
    clear(list);
    clear(status);
    available = Boolean(data.available);
    if (!data.available) {
      listFold.setSummary(t("worktrees.unavailable"));
      facts.appendChild(h("div", { class: "notice" }, icon("alert", "sm"),
        h("div", null, localizeServer(data.reason) || t("worktrees.unavailable"))));
      createBtn.disabled = true;
      return;
    }
    createBtn.disabled = false;
    const repo = data.repository || {};
    facts.append(
      h("div", { class: "sw-facts" },
        h("div", { class: "sw-fact" }, h("span", { class: "k" }, t("worktrees.fact.repository")),
          h("span", { class: "v mono" }, repo.root || "—")),
        h("div", { class: "sw-fact" }, h("span", { class: "k" }, t("worktrees.fact.main_branch")),
          h("span", { class: "v" }, repo.main_branch || repo.branch || "—")),
        h("div", { class: "sw-fact" }, h("span", { class: "k" }, t("worktrees.fact.safe_root")),
          h("span", { class: "v mono" }, data.root || "—")),
        h("div", { class: "sw-fact" }, h("span", { class: "k" }, t("worktrees.fact.managed")),
          h("span", { class: "v" }, t("worktrees.fact.managed_count", { managed: data.managed_total ?? 0, total: data.total ?? 0 })))));
    status.appendChild(h("span", { class: `pill ${data.dirty_total ? "warn" : "ok"}` },
      data.dirty_total ? t("worktrees.dirty_total", { n: data.dirty_total }) : t("worktrees.all_clean")));
    const managed = (data.worktrees || []).filter((item) => item.managed && !item.main);
    listFold.setSummary(t("settings.fold.count", { n: managed.length }));
    if (!managed.length) {
      list.appendChild(h("div", { class: "settings-lead" }, t("worktrees.empty")));
      return;
    }
    for (const item of managed) list.appendChild(worktreeRow(item, () => load(), (text) => {
      message.textContent = text || t("worktrees.remove_failed");
      if (text) message.classList.add("warn");
    }));
  };

  const load = async () => {
    if (loading) return;
    loading = true;
    createBtn.disabled = true;
    try {
      const data = await api.worktrees();
      paint(data);
    } catch (err) {
      clear(facts);
      clear(list);
      listFold.setSummary(t("worktrees.unavailable"));
      facts.appendChild(h("div", { class: "notice" }, icon("alert", "sm"), h("div", null, err.message)));
    } finally {
      loading = false;
      createBtn.disabled = !available;
    }
  };

  createBtn.addEventListener("click", async () => {
    const name = nameInput.value.trim();
    if (!name) { message.textContent = t("worktrees.name_required"); return; }
    createBtn.disabled = true;
    message.textContent = t("worktrees.creating");
    try {
      await api.createWorktree({ name, base: baseInput.value.trim() });
      nameInput.value = "";
      baseInput.value = "";
      message.textContent = t("worktrees.created");
      await load();
    } catch (err) {
      message.textContent = err.message;
    } finally {
      createBtn.disabled = !available;
    }
  });

  const refreshBtn = h("button", { class: "ghost-btn" }, icon("refresh", "sm"), t("action.refresh"));
  refreshBtn.addEventListener("click", () => load());

  const panel = h("section", { class: "panel settings-panel worktree-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("worktrees.title")), status),
    h("p", { class: "settings-lead" }, t("worktrees.lead")),
    facts,
    h("div", { class: "assistant-api-actions" }, refreshBtn),
    h("div", { class: "worktree-create" },
      h("div", { class: "field" }, h("label", null, t("worktrees.name")), nameInput),
      h("div", { class: "field" }, h("label", null, t("worktrees.base")), baseInput),
      createBtn,
      message),
    listFold.el,
    h("div", { class: "settings-note" }, icon("shield", "sm"),
      h("div", null, h("b", null, t("worktrees.safe_root_title")), t("worktrees.safe_root_body"))));
  load();
  return panel;
}
