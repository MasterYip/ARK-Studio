// Settings → Project → Setup / Resources (docs/operations/multi-user-setup.md).
// One project may be maintained by several people: each registers their own
// resources and machine paths. Every project member sees the full register.
import { h, clear, richText } from "../dom.js";
import { t, localizeServer } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { openModal, toast, confirmDialog } from "../components.js";
import { userBadge } from "../users.js";
import { foldSection } from "../fold-section.js";
import { reviewPages, firstPage, stepLabel, nextIndex, reviewOutcome } from "../setup-review-model.js";

const KINDS = ["repository", "task_space", "compute", "environment", "storage", "command", "secret"];
const KIND_ICON = { repository: "git", task_space: "tasks", compute: "cpu", environment: "layers",
  storage: "database", command: "terminal", secret: "shield" };
const NO_LOCATION = new Set(["command", "secret", "environment"]);
const PROPOSED = "proposed";
const REVIEWABLE = new Set(["proposed", "draft", "stale"]);
const PROPOSABLE = new Set(["draft", "stale"]);
const STATE_TONE = { unset: "", draft: "warn", proposed: "warn", stale: "bad", confirmed: "ok", verified: "ok" };
// Kind-specific detail fields: [field, choices | null].  Values are data.
const DETAIL_FIELDS = {
  repository: [["role", ["primary", "code", "paper", "website", "data", "reference"]],
    ["access", ["read-write", "read-only"]], ["default_branch", null], ["remote", null]],
  task_space: [["kind", ["agent-team-folder", "issue-tracker", "ark", "other"]],
    ["access", ["read-only", "read-write"]], ["relation", null]],
  compute: [["kind", ["ssh", "local", "slurm", "cloud"]], ["gpu_server", null], ["gpus", null],
    ["use", ["ask", "any", "reserved"]], ["scratch", null]],
  environment: [["kind", ["conda", "venv", "uv", "poetry", "docker", "system", "other"]],
    ["activate", null], ["python", null]],
  storage: [["kind", ["artifacts", "datasets", "checkpoints", "logs", "media", "other"]],
    ["retention", null]],
  command: [["run", null], ["gate", ["", "static", "unit", "component", "integration", "experiment", "publication"]],
    ["repository", null], ["environment", null]],
  secret: [["ref", null]],
};

function stateLabel(state) {
  return t(`setupres.state.${state || "unset"}`);
}

function statePill(state) {
  return h("span", { class: `pill ${STATE_TONE[state] || ""}`.trim(), "data-layer-state": state || "unset" },
    stateLabel(state));
}

function kindLabel(kind) { return t(`setupres.kind.${kind}`); }

/* ---------------------------------------------------------------- review */

function layerSummary(state, layer) {
  const markdown = layer === "user" ? state.user_summary_markdown : state.summary_markdown;
  return String(markdown || "").split("\n")
    .map((line) => (line.startsWith("|")
      ? line.split(" | ").map((cell) => localizeServer(cell, "server.setup.")).join(" | ")
      : localizeServer(line, "server.setup."))).join("\n");
}

const MODE_NOTE = {
  done: ["check", "setupreview.mode.done"],
  readonly: ["info", "setupres.admin_confirms"],
  pending: ["info", "setupreview.mode.pending"],
  none: ["info", "setupreview.mode.none"],
};

/**
 * "Review & confirm": one paged dialog, page 1 the project layer (admins
 * confirm; others read it), page 2 my own layer, then the combined result.
 * `layer` picks the first page ("project" / "user"; "" = first confirmable).
 */
export function openSetupReview(initialState, layer = "", reload) {
  let state = initialState;
  let pages = reviewPages(state);
  let index = firstPage(pages, layer);
  const confirmed = new Set();
  let busy = false;
  const steps = h("div", { class: "setup-steps", "data-setup-steps": "" });
  const content = h("div", { class: "setup-review" });
  const hint = h("span", { class: "hint" });
  const back = h("button", { class: "btn ghost", "data-review-back": "" }, t("setupreview.back"));
  const confirm = h("button", { class: "btn primary", "data-review-confirm": "" }, t("start.confirm"));
  const next = h("button", { class: "btn", "data-review-next": "" });
  const foot = h("div", { class: "modal-foot" }, hint, h("div", { class: "spacer" }), back, confirm, next);
  const dialog = openModal({
    title: t("start.review_title"),
    wide: true,
    body: h("div", { class: "setup-review-pages" }, steps, content),
    onClose: () => { if (confirmed.size) reload?.(); },
  });
  dialog.modal.classList.add("setup-review-dialog");
  dialog.modal.appendChild(foot);

  const render = () => {
    const total = pages.length;
    const result = index >= total;
    clear(steps);
    pages.forEach((page, i) => {
      if (i) steps.appendChild(h("span", { class: "setup-steps-sep" }, "·"));
      steps.appendChild(h("span", {
        class: `setup-step ${i === index ? "active" : ""} ${confirmed.has(page.layer) || page.mode === "done" ? "done" : ""}`.trim(),
        "data-step": page.layer,
      }, `${stepLabel(i, total)} · ${t(`setupreview.layer.${page.layer}`)}`));
    });
    clear(content);
    if (result) {
      content.append(h("p", { class: "muted" }, t("setupreview.result_lead")),
        h("ul", { class: "setup-review-result" }, ...reviewOutcome(pages, confirmed).map((row) =>
          h("li", { "data-result": `${row.layer}:${row.result}` },
            h("b", null, t(`setupreview.layer.${row.layer}`)), " ",
            h("span", { class: `pill ${row.result === "confirmed" || row.result === "already" ? "ok" : "warn"}` },
              t(`setupreview.result.${row.result}`))))));
      hint.textContent = "";
      back.hidden = false;
      confirm.hidden = true;
      next.textContent = t("action.close");
      next.className = "btn primary";
      return;
    }
    const page = pages[index];
    const info = state.layers?.[page.layer] || {};
    const blocked = (state.readiness?.blocked || []).map((b) => `${b.capability}: ${localizeServer(b.reason)}`);
    const note = confirmed.has(page.layer) ? ["check", "setupreview.mode.confirmed_now"] : MODE_NOTE[page.mode];
    content.append(...[
      h("p", { class: "muted" }, t(page.layer === "user" ? "setupres.review_user_hint" : "start.review_hint")),
      info.needs_reconfirmation ? h("div", { class: "notice" }, icon("alert"), h("div", null, t("setupres.reconfirm"))) : null,
      note ? h("div", { class: "notice", "data-review-mode": page.mode }, icon(note[0]), h("div", null, t(note[1]))) : null,
      page.mode === "none" ? null : h("div", { class: "setup-summary" }, richText(layerSummary(state, page.layer))),
      blocked.length && page.layer === "project" ? h("div", { class: "notice" }, icon("alert"), h("div", null, blocked.join(" · "))) : null,
    ].filter(Boolean));
    hint.textContent = page.digest ? t("start.digest_hint", { digest: page.digest }) : "";
    back.hidden = index === 0;
    confirm.hidden = false;
    confirm.disabled = busy || !page.canConfirm || confirmed.has(page.layer);
    next.textContent = index === total - 1 ? t("setupreview.finish") : t("setupreview.next");
    next.className = "btn";
  };

  back.addEventListener("click", () => { index = Math.max(0, index - 1); render(); });
  next.addEventListener("click", () => {
    if (index >= pages.length) { dialog.close(true); return; }
    index = nextIndex(index, pages.length);
    render();
  });
  confirm.addEventListener("click", async () => {
    const page = pages[index];
    if (!page?.canConfirm || busy) return;
    busy = true;
    render();
    try {
      await api.setupConfirm({ layer: page.layer, digest: page.digest });
      confirmed.add(page.layer);
      toast(t("start.confirmed"), "ok");
      try { state = await api.setupState(); } catch { /* keep the old state */ }
      pages = reviewPages(state);
      index = nextIndex(index, pages.length);
    } catch (err) {
      toast(err.message, "err");
    } finally {
      busy = false;
      render();
    }
  });
  render();
  return dialog;
}

/* ----------------------------------------------------------------- forms */

function field(label, control, full = false) {
  return h("div", { class: `field ${full ? "full" : ""}`.trim() }, h("label", null, label), control);
}

function select(values, current, labeler = (v) => v) {
  const el = h("select", { class: "select" }, ...values.map((v) => h("option", { value: v }, labeler(v))));
  if (current !== undefined && current !== null) el.value = current;
  return el;
}

async function openResourceForm(state, { layer = "user", item = null, reload }) {
  const editing = Boolean(item);
  const details = item?.details || {};
  const kind = select(KINDS, item?.kind || "repository", kindLabel);
  kind.disabled = editing;
  kind.dataset.field = "kind";
  const id = h("input", { class: "input", value: item?.id || "", maxlength: 48, disabled: editing,
    placeholder: t("setupres.ph.id") });
  const name = h("input", { class: "input", value: item?.name || "", maxlength: 120 });
  const purpose = h("input", { class: "input", value: item?.purpose || "", maxlength: 300,
    placeholder: t("setupres.ph.purpose") });
  id.dataset.field = "id";
  name.dataset.field = "name";
  purpose.dataset.field = "purpose";
  const path = h("input", { class: "input", value: item?.location?.path_or_ref || "", maxlength: 500,
    placeholder: t("setupres.ph.path") });
  path.dataset.field = "path";
  const pathField = field(t("setupres.location_here", { host: state.host?.label || "" }), path, true);
  const detailHost = h("div", { class: "assistant-api-grid" });
  let detailInputs = [];
  const renderDetails = () => {
    clear(detailHost);
    detailInputs = (DETAIL_FIELDS[kind.value] || []).map(([key, choices]) => {
      const value = details[key] ?? "";
      const control = choices ? select(choices, choices.includes(value) ? value : choices[0])
        : h("input", { class: "input", value: String(value), maxlength: 500 });
      control.dataset.field = `detail-${key}`;
      detailHost.appendChild(field(t(`setupres.field.${key}`), control, !choices));
      return [key, control];
    });
    pathField.hidden = NO_LOCATION.has(kind.value);
  };
  kind.addEventListener("change", renderDetails);
  renderDetails();
  const body = h("div", { class: "stack setupres-form" },
    h("div", { class: "assistant-api-grid" },
      field(t("setupres.field.kind"), kind), field(t("setupres.field.id"), id),
      field(t("setupres.field.name"), name), field(t("setupres.field.purpose"), purpose, true)),
    detailHost, pathField,
    h("div", { class: "settings-note" }, icon("shield", "sm"), h("div", null, t("setupres.secret_rule"))));
  openModal({
    title: t(editing ? "setupres.edit_title" : layer === "project" ? "setupres.add_project_title" : "setupres.add_title"),
    wide: true,
    body,
    actions: [
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("setupres.save"),
        primary: true,
        onClick: async () => {
          const resource = {
            id: (id.value || "").trim().toLowerCase(), kind: kind.value, name: name.value.trim(),
            purpose: purpose.value.trim(),
            details: Object.fromEntries(detailInputs.map(([key, el]) => [key, el.value.trim()])),
          };
          if (!resource.id) { toast(t("setupres.id_required"), "err"); return false; }
          if (!NO_LOCATION.has(kind.value) && path.value.trim()) resource.path_or_ref = path.value.trim();
          try {
            const data = await api.setupAction("resource", { layer, resource });
            if (data.ok === false && data.errors?.length) toast(data.errors.map((e) => localizeServer(e)).join(" · "), "err");
            else toast(t("setupres.saved"), "ok");
            reload?.(data);
          } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

function openLocationForm(state, item, reload) {
  const path = h("input", { class: "input", maxlength: 500, value: item.location?.path_or_ref || "",
    placeholder: t("setupres.ph.path") });
  const label = h("input", { class: "input", maxlength: 60, value: state.host?.label || "" });
  openModal({
    title: t("setupres.location_title", { name: item.name }),
    body: h("div", { class: "stack" },
      h("p", { class: "settings-lead" }, t("setupres.location_lead", { id: state.host?.id || "" })),
      field(t("setupres.host_label"), label),
      field(t("setupres.path_or_ref"), path, true)),
    actions: [
      { spacer: true },
      { label: t("action.cancel"), ghost: true },
      {
        label: t("setupres.save"), primary: true,
        onClick: async () => {
          if (!path.value.trim()) { toast(t("setupres.path_required"), "err"); return false; }
          try {
            const data = await api.setupAction("location", {
              resource: item.key, path_or_ref: path.value.trim(), host_label: label.value.trim() });
            toast(t("setupres.saved"), "ok");
            reload?.(data);
          } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

/* ------------------------------------------------------------------ rows */

function resourceRow(item, { actions = [], showOwner = true, reason = "" } = {}) {
  const needsLoc = item.details && !NO_LOCATION.has(item.kind) && !item.location
    && !(item.kind === "compute" && item.details?.kind !== "local");
  return h("div", { class: "setupres-row", "data-resource-key": item.key, "data-reason": reason || item.reason || "" },
    h("span", { class: "setupres-ic" }, icon(KIND_ICON[item.kind] || "layers", "sm")),
    h("div", { class: "setupres-main" },
      h("div", { class: "setupres-title" },
        h("b", null, item.name || item.id),
        h("span", { class: "muted" }, kindLabel(item.kind)),
        reason ? h("span", { class: "pill" }, t(`setupres.reason.${reason}`)) : null),
      item.purpose ? h("div", { class: "muted setupres-purpose" }, item.purpose) : null,
      h("div", { class: "setupres-meta" },
        showOwner && item.owner ? h("span", { class: "setupres-owner" }, userBadge(item.owner)) : null,
        needsLoc ? h("span", { class: "pill warn" }, icon("alert", "sm"), t("setupres.no_location_here")) : null),
      h("div", { class: "stack setupres-locations" }, ...(item.locations || []).map((loc) =>
        h("div", { class: "setupres-meta" },
          h("span", { class: "muted" }, loc.host_label),
          h("code", { class: "setupres-loc" }, loc.path_or_ref),
          loc.added_by ? userBadge(loc.added_by) : null)))),
    h("div", { class: "setupres-actions" }, ...actions));
}

function btn(label, iconName, onclick, cls = "ghost-btn sm") {
  return h("button", { class: cls, type: "button", onclick }, icon(iconName, "sm"), label);
}

/* ---------------------------------------------------------------- panels */

export function setupResourcesPanels(ctx) {
  const projectPanel = h("section", { class: "panel settings-panel setupres-panel", "data-setup-panel": "project" });
  const minePanel = h("section", { class: "panel settings-panel setupres-panel", "data-setup-panel": "mine" });
  const othersPanel = h("section", { class: "panel settings-panel setupres-panel", "data-setup-panel": "others" });
  let state = null;

  const reload = async (data) => {
    try { state = data && data.layers ? data : await api.setupState(); }
    catch (err) {
      for (const panel of [projectPanel, minePanel, othersPanel]) clear(panel);
      projectPanel.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message)));
      return;
    }
    renderProject();
    renderMine();
    renderOthers();
  };

  const byKey = () => {
    const out = new Map();
    for (const item of [...(state.accessible || []), ...(state.known_but_inaccessible || [])]) out.set(item.key, item);
    return out;
  };

  function renderProject() {
    clear(projectPanel);
    const info = state.layers?.project || {};
    const profile = state.project_layer?.profile || {};
    const head = h("div", { class: "panel-head" },
      h("h3", null, t("setupres.project_title")), statePill(info.state),
      h("span", { class: "spacer" }),
      info.can_confirm && REVIEWABLE.has(info.state)
        ? btn(t("start.review"), "check", () => openSetupReview(state, "project", reload), "primary-btn sm") : null);
    const objective = h("textarea", { class: "input", rows: 2, maxlength: 4000, value: profile.objective || "",
      disabled: !info.can_confirm });
    objective.value = profile.objective || "";
    const policies = state.policies?.effective || {};
    const policyRow = h("div", { class: "setupres-policies" },
      ...Object.entries(policies).map(([key, value]) => h("span", {
        class: `pill ${value === "allow" ? "ok" : value === "deny" ? "bad" : "warn"}`,
        title: t("setupres.policy_hint", { project: state.policies.project?.[key] || "", mine: state.policies.user?.[key] || "—" }),
      }, `${key}: ${t(`setupres.policy.${value}`)}`)));
    const items = (state.project_layer?.profile?.resources || []).map((resource) => {
      const key = `project/${resource.id}`;
      return byKey().get(key) || { ...resource, key };
    });
    const list = h("div", { class: "setupres-list" },
      ...(items.length ? items.map((item) => resourceRow(item, {
        reason: item.reason === "no_location_on_host" ? "no_location_on_host" : "",
        actions: [
          state.layers?.user?.can_edit && item.details && !NO_LOCATION.has(item.kind)
            ? btn(t("setupres.add_location"), "plus", () => openLocationForm(state, item, reload)) : null,
          info.can_confirm && item.details ? btn(t("setupres.edit"), "edit", () => openResourceForm(state, { layer: "project", item, reload })) : null,
        ].filter(Boolean),
      })) : [h("div", { class: "empty sm" }, t("setupres.project_empty"))]));
    projectPanel.append(...[head,
      h("p", { class: "settings-lead" }, t(info.can_confirm ? "setupres.project_lead_admin" : "setupres.project_lead")),
      info.needs_reconfirmation ? h("div", { class: "notice" }, icon("alert"), h("div", null, t("setupres.reconfirm"))) : null,
      field(t("setupres.objective"), objective, true),
      info.can_confirm ? h("div", { class: "assistant-api-actions" },
        btn(t("setupres.save_objective"), "check", async () => {
          try {
            const data = await api.setupAction("draft", { layer: "project", merge: true, profile: { objective: objective.value.trim() } });
            toast(t("setupres.saved"), "ok");
            reload(data);
          } catch (err) { toast(err.message, "err"); }
        }),
        btn(t("setupres.add_project_resource"), "plus", () => openResourceForm(state, { layer: "project", reload })),
        PROPOSABLE.has(info.state)
          ? btn(t("setupres.propose"), "send", async () => {
            try { reload(await api.setupAction("propose", { layer: "project" })); toast(t("setupres.proposed"), "ok"); }
            catch (err) { toast(err.message, "err"); }
          }) : null) : null,
      h("div", { class: "setupres-sub" }, t("setupres.policies")), policyRow,
      h("div", { class: "setupres-sub" }, t("setupres.project_resources")), list].filter(Boolean));
  }

  function renderMine() {
    clear(minePanel);
    const info = state.layers?.user;
    if (!info) {
      minePanel.append(h("div", { class: "panel-head" }, h("h3", null, t("setupres.mine_title"))),
        h("p", { class: "settings-lead" }, t("setupres.mine_none")));
      return;
    }
    const me = info.owner;
    const prefix = `u:${me}/`;
    const map = byKey();
    const items = (state.mine?.profile?.resources || []).map((resource) =>
      map.get(prefix + resource.id) || { ...resource, key: prefix + resource.id });
    const host = state.host || {};
    const head = h("div", { class: "panel-head" },
      h("h3", null, t("setupres.mine_title")), statePill(info.state),
      h("span", { class: "spacer" }),
      info.state === PROPOSED
        ? btn(t("start.review"), "check", () => openSetupReview(state, "user", reload), "primary-btn sm") : null);
    const hostRow = h("div", { class: "setupres-host" },
      icon("cpu", "sm"),
      h("span", null, t("setupres.this_machine")),
      h("b", { "data-host-label": host.label }, host.label || ""),
      h("code", { class: "muted" }, host.id || ""),
      btn(t("setupres.rename_host"), "edit", () => {
        const input = h("input", { class: "input", maxlength: 60, value: host.label || "" });
        openModal({
          title: t("setupres.rename_host"),
          body: h("div", { class: "stack" }, h("p", { class: "settings-lead" }, t("setupres.host_lead")), input),
          actions: [{ spacer: true }, { label: t("action.cancel"), ghost: true }, {
            label: t("setupres.save"), primary: true,
            onClick: async () => {
              try { reload(await api.setupAction("host", { label: input.value.trim() })); }
              catch (err) { toast(err.message, "err"); return false; }
            },
          }],
        });
      }));
    const list = h("div", { class: "setupres-list" },
      ...(items.length ? items.map((item) => resourceRow(item, {
        showOwner: false,
        actions: [
          !NO_LOCATION.has(item.kind) ? btn(t("setupres.add_location"), "plus", () => openLocationForm(state, item, reload)) : null,
          btn(t("setupres.edit"), "edit", () => openResourceForm(state, { layer: "user", item, reload })),
          btn(t("setupres.remove"), "trash", async () => {
            const ok = await confirmDialog(t("setupres.remove_confirm", { name: item.name }), { danger: true });
            if (!ok) return;
            try { reload(await api.setupAction("remove", { id: item.id })); }
            catch (err) { toast(err.message, "err"); }
          }, "ghost-btn danger sm"),
        ].filter(Boolean),
      })) : [h("div", { class: "empty sm" }, t("setupres.mine_empty"))]));
    const verifyBtn = btn(t("setupres.verify"), "check", async () => {
      try {
        const data = await api.setupAction("verify", {});
        const failed = (data.verify?.checks || []).filter((c) => c.status === "fail").length;
        toast(failed ? t("setupres.verify_failed", { count: failed }) : t("setupres.verify_ok"), failed ? "err" : "ok");
        reload(data);
      } catch (err) { toast(err.message, "err"); }
    });
    minePanel.append(head,
      h("p", { class: "settings-lead" }, t("setupres.mine_lead")),
      hostRow,
      h("div", { class: "assistant-api-actions" },
        btn(t("setupres.add"), "plus", () => openResourceForm(state, { layer: "user", reload }), "primary-btn sm"),
        PROPOSABLE.has(info.state) && items.length ? btn(t("setupres.propose"), "send", async () => {
          try { reload(await api.setupAction("propose", { layer: "user" })); toast(t("setupres.proposed"), "ok"); }
          catch (err) { toast(err.message, "err"); }
        }) : null,
        verifyBtn),
      list);
    if (!info.can_edit) minePanel.querySelectorAll("button, input, select, textarea")
      .forEach((el) => { el.disabled = true; });
  }

  function renderOthers() {
    clear(othersPanel);
    const me = state.viewer?.user || "";
    const others = (state.resources || [])
      .filter((item) => item.layer === "user" && item.owner !== me);
    const list = h("div", { class: "setupres-list" },
      ...(others.length ? others.map((item) => {
        return resourceRow(item, {
          reason: item.usable_here ? "" : "no_location_on_host",
          actions: [
            state.layers?.user?.can_edit && !NO_LOCATION.has(item.kind)
              ? btn(t("setupres.add_location"), "plus", () => openLocationForm(state, item, reload)) : null,
          ].filter(Boolean),
        });
      }) : [h("div", { class: "empty sm" }, t("setupres.others_empty"))]));
    // Nothing to show until another member registers a resource.
    othersPanel.hidden = !others.length;
    othersPanel.append(
      h("div", { class: "panel-head" }, h("h3", null, t("setupres.others_title")),
        h("span", { class: "pill" }, String(others.length))),
      h("p", { class: "settings-lead" }, t("setupres.others_lead")),
      foldSection({ id: "setup-others", title: t("settings.fold.others_resources"),
        summary: t("settings.fold.count", { n: others.length }), body: list }).el);
  }

  reload();
  return [projectPanel, minePanel, othersPanel];
}
