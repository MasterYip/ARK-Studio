// Settings → Project → Setup / Resources (docs/operations/multi-user-setup.md).
// One project may be maintained by several people: each registers their own
// resources and machine paths. Every project member sees the full register.
import { h, clear, richText } from "../dom.js";
import { t, hasKey, localizeServer } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import { openModal, toast, confirmDialog } from "../components.js";
import { userBadge } from "../users.js";
import { foldSection } from "../fold-section.js";
import { deviceRows, since } from "../setup-devices-model.js";
import { sizeBadge, refreshLocationSize, desktopDeviceId, measureOnDesktop } from "../location-size.js";
import { duePaths, isLocalPath } from "../location-size-model.js";
import { openContextMenu } from "../context-menu.js";
import { connectionStore, openMenuItems } from "../resource-open.js";
import { openRelocateDialog, openLogicalDialog } from "./setup-relocate.js";
import { reviewPages, visiblePages, stepLabel, reviewOutcome, plainSummary, confirmPlan, confirmLabelKey,
  changeGroups } from "../setup-review-model.js";

const KINDS = ["repository", "task_space", "compute", "environment", "storage", "command", "secret"];
const KIND_ICON = { repository: "git", task_space: "tasks", compute: "cpu", environment: "layers",
  storage: "database", command: "terminal", secret: "shield" };
const NO_LOCATION = new Set(["command", "secret", "environment"]);
const autoMeasured = new Set();
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
  compute: [["kind", ["ssh", "local", "slurm", "cloud"]], ["gpu_server", null], ["device_id", null], ["gpus", null],
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

// Why a layer in the review needs nothing from this user, or cannot be
// confirmed here; a proposed layer this user may confirm has no note.
function layerNote(group, confirmedNow) {
  if (confirmedNow) return ["check", "setupreview.mode.confirmed_now"];
  if (group.mode === "done") return ["check", "setupreview.mode.done"];
  if (group.mode === "readonly") return ["info", "setupreview.admin_only"];
  if (group.mode === "draft") return ["info", `setupreview.mode.${group.state === "stale" ? "stale" : "draft"}`];
  if (group.mode === "pending") return ["info", "setupreview.mode.pending"];
  return null;
}

// A policy key in plain words ("push to a remote"); unknown keys as written.
function policyWords(key) {
  const text = t(`setupreview.do.${key}`);
  return text === `setupreview.do.${key}` ? key.replaceAll("_", " ") : text;
}

// The plain-language summary of a layer on the overview page
// (setup-review-model plainSummary).
function plainSummaryView(state, layer) {
  const sum = plainSummary(state, layer);
  const row = (key, value, extra = {}) => h("div", { class: "setup-plain-row", ...extra },
    h("dt", null, t(key)), h("dd", null, value));
  const words = (keys) => keys.map(policyWords).join(t("setupreview.list_sep"));
  const rows = [
    sum.objective ? row("setupreview.plain.goal", sum.objective) : null,
    sum.allow.length ? row("setupreview.plain.allow", words(sum.allow), { "data-plain": "allow" }) : null,
    sum.ask.length ? row(layer === "user" ? "setupreview.plain.ask_mine" : "setupreview.plain.ask", words(sum.ask), { "data-plain": "ask" }) : null,
    sum.deny.length ? row("setupreview.plain.deny", words(sum.deny), { "data-plain": "deny" }) : null,
    sum.resources.length ? row(layer === "user" ? "setupreview.plain.mine" : "setupreview.plain.uses",
      sum.resources.map(({ kind, names }) => `${kindLabel(kind)}: ${names.join(", ")}`).join(" · ")) : null,
    sum.questions ? row("setupreview.plain.open", t("setupreview.plain.open_n", { n: sum.questions })) : null,
  ].filter(Boolean);
  if (!rows.length) return h("p", { class: "muted setup-plain-empty" }, t("setupreview.plain.empty"));
  return h("dl", { class: "setup-plain", "data-plain-summary": layer }, ...rows);
}

// Field names in the change list: resource detail fields, project fields
// and policies, each in its own words where ARK has them.
function fieldLabel(type, field) {
  if (type === "policy") {
    return hasKey(`setupres.policy_key.${field}`) ? t(`setupres.policy_key.${field}`) : field.replaceAll("_", " ");
  }
  for (const key of [`setupreview.field.${field}`, `setupres.field.${field}`]) {
    if (hasKey(key)) return t(key);
  }
  return field.replaceAll("_", " ");
}

function policyValue(value) {
  return value && hasKey(`setupres.policy.${value}`) ? t(`setupres.policy.${value}`) : value;
}

function changeMark(change) {
  return h("span", { class: `setup-mark ${change}`, "data-mark": change }, t(`setupreview.mark.${change}`));
}

// "old -> new" with the old value struck through; an added value alone; a
// removed value struck through alone.
function valueChange(old, value) {
  return h("span", { class: "setup-values" },
    old ? h("del", { class: "setup-old" }, old) : null,
    old && value ? h("span", { class: "setup-arrow", "aria-hidden": "true" }, "→") : null,
    value ? h("ins", { class: "setup-new" }, value) : null);
}

function locationValue(loc) {
  return loc.change === "removed" ? valueChange(loc.path, "") : valueChange("", loc.path);
}

function changeRowView(row) {
  if (row.type === "resource") {
    return h("li", { class: "setup-change", "data-change": row.change, "data-change-key": row.key },
      h("div", { class: "setup-change-head" },
        changeMark(row.change),
        h("span", { class: "setup-change-ic" }, icon(KIND_ICON[row.kind] || "layers", "sm")),
        h("b", { class: "setup-change-name" }, row.name),
        h("span", { class: "muted" }, kindLabel(row.kind))),
      row.fields.length || row.locations.length ? h("ul", { class: "setup-change-fields" },
        ...row.fields.map((f) => h("li", { "data-field": f.field },
          h("span", { class: "setup-field" }, fieldLabel("field", f.field)), valueChange(f.old, f.new))),
        ...row.locations.map((loc) => h("li", { "data-location-change": loc.change },
          h("span", { class: "setup-field" }, t("setupreview.location_on", { host: loc.host })), locationValue(loc))))
        : null);
  }
  if (row.type === "location") {
    return h("li", { class: "setup-change", "data-change": row.change, "data-change-key": row.resource },
      h("div", { class: "setup-change-head" },
        changeMark(row.change),
        h("span", { class: "setup-change-ic" }, icon("folder", "sm")),
        h("b", { class: "setup-change-name" }, row.name),
        h("span", { class: "muted" }, t("setupreview.location_on", { host: row.host }))),
      h("ul", { class: "setup-change-fields" }, h("li", null, locationValue(row))));
  }
  const show = row.type === "policy" ? policyValue : (v) => v;
  return h("li", { class: "setup-change", "data-change": row.change, "data-change-field": row.field },
    h("div", { class: "setup-change-head" }, changeMark(row.change),
      h("b", { class: "setup-change-name" }, fieldLabel(row.type, row.field)),
      valueChange(show(row.old), show(row.new))));
}

function layerSummary(state, layer) {
  const markdown = layer === "user" ? state.user_summary_markdown : state.summary_markdown;
  return String(markdown || "").split("\n")
    .map((line) => (line.startsWith("|")
      ? line.split(" | ").map((cell) => localizeServer(cell, "server.setup.")).join(" | ")
      : localizeServer(line, "server.setup."))).join("\n");
}

function layerHead(group, extra = null) {
  return h("div", { class: "setup-layer-head" },
    h("h4", null, t(`setupreview.layer.${group.layer}`)), statePill(group.state), extra);
}

function notice(iconName, text, attrs = {}) {
  return h("div", { class: "notice", ...attrs }, icon(iconName), h("div", null, text));
}

/**
 * "Review & confirm": page 1 an overview of the project layer and my own
 * layer, page 2 what changed in each since its last confirmation with the
 * full setup below it, and one Confirm that covers every proposed layer
 * this user may confirm (sent in one request).  A layer only a project
 * admin may confirm, or one that was not sent for confirmation, says so.
 */
export function openSetupReview(initialState, reload) {
  let state = initialState;
  let step = 0; // 0 overview, 1 changes, 2 result
  let confirmed = new Set();
  let outcome = [];
  let busy = false;
  const steps = h("div", { class: "setup-steps", "data-setup-steps": "" });
  const content = h("div", { class: "setup-review" });
  const hint = h("span", { class: "hint" });
  const back = h("button", { class: "btn ghost", "data-review-back": "" }, t("setupreview.back"));
  const confirm = h("button", { class: "btn primary", "data-review-confirm": "" });
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
  const scroller = dialog.modal.querySelector(".modal-body");
  const go = (to) => { step = to; render(); if (scroller) scroller.scrollTop = 0; };

  const overview = (groups) => [
    h("p", { class: "muted" }, t("setupreview.overview_lead")),
    ...groups.map((group) => {
      const note = layerNote(group, confirmed.has(group.layer));
      const count = group.rows.length;
      return h("section", { class: "setup-layer", "data-review-layer": group.layer },
        layerHead(group, count ? h("span", { class: "setup-count" }, t("setupreview.changes_n", { n: count })) : null),
        note ? notice(note[0], t(note[1]), { "data-review-mode": group.mode }) : null,
        group.mode === "pending" ? null : plainSummaryView(state, group.layer));
    }),
  ];

  const changesPage = (groups) => {
    const blocked = (state.readiness?.blocked || []).map((b) => `${b.capability}: ${localizeServer(b.reason)}`);
    return [
      h("p", { class: "muted" }, t("setupreview.changes_lead")),
      ...groups.map((group) => {
        const note = layerNote(group, confirmed.has(group.layer));
        const empty = group.baseline === "none"
          ? (group.mode === "pending" ? null : t("setupreview.first_time"))
          : group.rows.length ? null : t("setupreview.no_changes");
        const info = state.layers?.[group.layer] || {};
        return h("section", { class: "setup-layer", "data-review-layer": group.layer },
          layerHead(group),
          note ? notice(note[0], t(note[1]), { "data-review-mode": group.mode }) : null,
          info.needs_reconfirmation ? notice("info", t("setupres.reconfirm")) : null,
          group.rows.length ? h("ul", { class: "setup-changes", "data-changes": group.layer }, ...group.rows.map(changeRowView))
            : empty ? h("p", { class: "muted setup-changes-empty" }, empty) : null,
          group.mode === "pending" ? null : h("div", { class: "setup-full", "data-review-details": group.layer },
            h("div", { class: "setup-sub" }, t("setupreview.full")),
            h("div", { class: "setup-summary" }, richText(layerSummary(state, group.layer)))),
          blocked.length && group.layer === "project" ? notice("info", blocked.join(" · ")) : null);
      }),
    ];
  };

  const render = () => {
    const groups = changeGroups(state);
    const plan = confirmPlan(state);
    clear(steps);
    ["overview", "changes"].forEach((name, i) => {
      if (i) steps.appendChild(h("span", { class: "setup-steps-sep" }, "·"));
      steps.appendChild(h("button", {
        type: "button",
        class: `setup-step ${i === step ? "active" : ""} ${step === 2 || i < step ? "done" : ""}`.trim(),
        "data-step": name, disabled: step === 2 || busy,
        onclick: () => go(i),
      }, `${stepLabel(i, 2)} · ${t(`setupreview.step.${name}`)}`));
    });
    clear(content);
    if (step === 2) {
      content.append(h("p", { class: "muted" }, t("setupreview.result_lead")),
        h("ul", { class: "setup-review-result" }, ...outcome.map((row) =>
          h("li", { "data-result": `${row.layer}:${row.result}` },
            h("b", null, t(`setupreview.layer.${row.layer}`)), " ",
            h("span", { class: `pill ${row.result === "confirmed" || row.result === "already" ? "ok" : ""}`.trim() },
              t(`setupreview.result.${row.result}`))))));
      hint.textContent = "";
      back.hidden = true;
      confirm.hidden = true;
      next.hidden = false;
      next.textContent = t("action.close");
      next.className = "btn primary";
      return;
    }
    content.append(...(step === 0 ? overview(groups) : changesPage(groups)).filter(Boolean));
    if (step === 1 && !plan.confirm.length) {
      content.appendChild(notice("info", t(plan.admin.length ? "setupreview.admin_only" : "setupreview.nothing_to_confirm"),
        { "data-review-nothing": "" }));
    }
    hint.textContent = step === 1 && plan.confirm.length
      ? t("start.digest_hint", { digest: plan.confirm.map((item) => item.digest).join(" · ") }) : "";
    back.hidden = step === 0;
    // One Confirm, on the changes page only.
    const label = confirmLabelKey(plan);
    confirm.hidden = step !== 1 || !label;
    confirm.disabled = busy;
    confirm.textContent = label ? t(label) : "";
    next.hidden = step !== 0;
    next.textContent = t("setupreview.next");
    next.className = plan.confirm.length ? "btn primary" : "btn";
  };

  back.addEventListener("click", () => go(Math.max(0, step - 1)));
  next.addEventListener("click", () => {
    if (step === 2) { dialog.close(true); return; }
    go(1);
  });
  confirm.addEventListener("click", async () => {
    const plan = confirmPlan(state);
    if (!plan.confirm.length || busy) return;
    busy = true;
    render();
    let to = step;
    try {
      const data = await api.setupConfirm({ layers: plan.confirm });
      confirmed = new Set(plan.confirm.map((item) => item.layer));
      outcome = reviewOutcome(visiblePages(reviewPages(state)), confirmed);
      toast(t("start.confirmed"), "ok");
      if (data && data.layers) state = data;
      to = 2;
    } catch (err) {
      toast(err.message, "err");
      try { state = await api.setupState(); } catch { /* keep the old state */ }
    } finally {
      busy = false;
      go(to);
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

// device: one of the person's devices (Devices panel); default: the device
// this page talks for (state.host).
function openLocationForm(state, item, reload, device = null) {
  const host = device ? { id: device.id, label: device.label } : state.host || {};
  const path = h("input", { class: "input", maxlength: 500,
    value: (device ? item.path_or_ref : item.location?.path_or_ref) || "", placeholder: t("setupres.ph.path") });
  const label = h("input", { class: "input", maxlength: 60, value: host.label || "" });
  openModal({
    title: t("setupres.location_title", { name: item.name }),
    body: h("div", { class: "stack" },
      h("p", { class: "settings-lead" }, t("setupres.location_lead", { id: host.id || "" })),
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
              resource: item.key, path_or_ref: path.value.trim(), host_label: label.value.trim(),
              ...(device ? { host_id: device.id } : {}) });
            toast(t("setupres.saved"), "ok");
            reload?.(data);
          } catch (err) { toast(err.message, "err"); return false; }
        },
      },
    ],
  });
}

/* ------------------------------------------------------------------ rows */

// ⋯ next to a location or device: Open in VS Code / SSH, Copy path
// (resource-open.js); the items are built when the menu opens.
function moreButton(items) {
  return h("button", { class: "icon-btn sm setupres-more", type: "button", title: t("resources.open.more"),
    "aria-label": t("resources.open.more"), "aria-haspopup": "menu", "data-setupres-more": "1",
    onclick: (event) => {
      event.stopPropagation();
      const button = event.currentTarget;
      const list = items();
      if (!list.length) { toast(t("resources.open.none")); return; }
      const rect = button.getBoundingClientRect();
      openContextMenu({ x: rect.left, y: rect.bottom + 4, items: list, returnFocus: button });
    } }, icon("more", "sm"));
}

function resourceRow(item, { actions = [], showOwner = true, reason = "", sizeRefresh = null, more = null } = {}) {
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
          isLocalPath(loc.path_or_ref) ? sizeBadge(loc.stats, {
            onRefresh: sizeRefresh ? (button) => sizeRefresh(item, loc, button) : null }) : null,
          loc.added_by ? userBadge(loc.added_by) : null,
          more && loc.path_or_ref ? more(loc) : null)))),
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
  const devicesPanel = h("section", { class: "panel settings-panel setupres-panel", "data-setup-panel": "devices" });
  let state = null;
  // SSH connections of the project's devices (resource-open.js).
  const connections = connectionStore();
  const deviceOf = (hostId, label) => {
    const id = connections.deviceOfHost(hostId);
    return { id, label: label || hostId, kind: id === "ark" ? "ark" : id.startsWith("gpu:") ? "gpu-server" : "host" };
  };
  const more = (loc) => moreButton(() => openMenuItems(connections,
    { device: deviceOf(loc.host_id, loc.host_label), path: loc.path_or_ref }));

  const reload = async (data) => {
    try { state = data && data.layers ? data : await api.setupState(); }
    catch (err) {
      for (const panel of [projectPanel, minePanel, othersPanel]) clear(panel);
      projectPanel.appendChild(h("div", { class: "notice" }, icon("alert"), h("div", null, err.message)));
      return;
    }
    const hosts = new Map();
    for (const item of [...(state.accessible || []), ...(state.known_but_inaccessible || [])]) {
      for (const loc of item.locations || []) if (loc.host_id) hosts.set(loc.host_id, loc.host_label);
    }
    for (const device of state.devices || []) if (device.id) hosts.set(device.id, device.label);
    connections.refresh([...hosts].map(([id, label]) => deviceOf(id, label)));
    renderProject();
    renderMine();
    renderDevices();
    renderOthers();
  };

  // ↻ next to a size: the device that holds the path measures it.
  const refreshSize = async (target, button) => {
    button.disabled = true;
    try {
      const r = await refreshLocationSize(target);
      toast(r.message, r.done || r.queued ? "ok" : "");
      if (r.done) reload();
      else if (r.queued) setTimeout(() => reload(), 4000);
    } catch (err) { toast(err.message, "err"); }
    finally { button.disabled = false; }
  };
  const sizeRefresh = (item, loc, button) => refreshSize({ resource: item.key, path: loc.path_or_ref,
    hostId: loc.host_id, deviceLabel: loc.host_label }, button);

  // In the desktop app: measure this computer's paths whose size is missing
  // or older than a day, once per page.
  const autoMeasure = async (rows) => {
    if (!state.layers?.user?.can_edit) return;
    const mine = await desktopDeviceId();
    const row = mine && !autoMeasured.has(mine) ? rows.find((d) => d.id === mine) : null;
    if (!row) return;
    autoMeasured.add(mine);
    const due = duePaths(row.paths).slice(0, 20);
    if (!due.length) return;
    try {
      if (await measureOnDesktop(due.map((p) => ({ resource: p.resource, path: p.path_or_ref })), mine)) reload();
    } catch { /* a size is optional; the ↻ button shows errors */ }
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
        ? btn(t("start.review"), "check", () => openSetupReview(state, reload), "primary-btn sm") : null);
    const objective = h("textarea", { class: "input", rows: 2, maxlength: 4000, value: profile.objective || "",
      disabled: !info.can_confirm });
    objective.value = profile.objective || "";
    const policies = state.policies?.effective || {};
    const policyRow = h("div", { class: "setupres-policies" },
      ...Object.entries(policies).map(([key, value]) => h("span", {
        class: `pill ${value === "allow" ? "ok" : value === "deny" ? "bad" : "warn"}`,
        title: t("setupres.policy_hint", { project: state.policies.project?.[key] || "", mine: state.policies.user?.[key] || "—" }),
      }, `${hasKey(`setupres.policy_key.${key}`) ? t(`setupres.policy_key.${key}`) : key}: ${t(`setupres.policy.${value}`)}`)));
    const items = (state.project_layer?.profile?.resources || []).map((resource) => {
      const key = `project/${resource.id}`;
      return byKey().get(key) || { ...resource, key };
    });
    const list = h("div", { class: "setupres-list" },
      ...(items.length ? items.map((item) => resourceRow(item, {
        sizeRefresh,
        more,
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
        ? btn(t("start.review"), "check", () => openSetupReview(state, reload), "primary-btn sm") : null);
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
        sizeRefresh,
        more,
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
          sizeRefresh,
          more,
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

  // Each of the person's devices with its own paths: a new computer shows
  // what it still needs, and its paths are added without touching anyone's
  // confirmation.
  function renderDevices() {
    clear(devicesPanel);
    const rows = deviceRows(state);
    devicesPanel.hidden = !rows.length;
    if (!rows.length) return;
    const canEdit = Boolean(state.layers?.user?.can_edit);
    const list = h("div", { class: "setupres-list" }, ...rows.map((device) => h("div", {
      class: "setupres-device", "data-device": device.id },
    h("div", { class: "setupres-host" },
      icon("cpu", "sm"),
      h("b", null, device.label),
      h("code", { class: "muted" }, device.id),
      device.current ? h("span", { class: "pill ok" }, t("setupres.device_current")) : null,
      since(device.lastSeen) ? h("span", { class: "muted" }, t(`setupres.device_seen_${since(device.lastSeen).unit}`,
        { n: since(device.lastSeen).n })) : null,
      h("span", { class: `pill ${device.missing ? "warn" : "ok"}` },
        device.missing ? t("setupres.device_missing", { n: device.missing }) : t("setupres.device_complete")),
      canEdit ? btn(t("relocate.button"), "folder", () => openRelocateDialog({ device, reload: () => reload(),
        from: device.paths.find((p) => p.kind === "task_space" && isLocalPath(p.path_or_ref))?.path_or_ref || "" })) : null,
      moreButton(() => openMenuItems(connections, { device: deviceOf(device.id, device.label), onChange: () => reload() }))),
    h("div", { class: "stack tight" }, ...device.paths.map((p) => h("div", { class: "row tight setupres-device-path",
      "data-device-path": p.resource },
    h("span", null, p.name), h("span", { class: "muted" }, kindLabel(p.kind)),
    p.path_or_ref ? h("code", null, p.path_or_ref)
      : h("span", { class: p.missing ? "bad" : "muted" }, t("setupres.device_no_path")),
    isLocalPath(p.path_or_ref) ? sizeBadge(p.stats, { onRefresh: (button) => refreshSize({
      resource: p.resource, path: p.path_or_ref, hostId: device.id, deviceLabel: device.label }, button) }) : null,
    canEdit ? btn(p.path_or_ref ? t("setupres.edit_location") : t("setupres.add_path"), p.path_or_ref ? "edit" : "plus",
      () => openLocationForm(state, { key: p.resource, name: p.name, path_or_ref: p.path_or_ref }, reload, device)) : null,
    p.path_or_ref ? moreButton(() => openMenuItems(connections,
      { device: deviceOf(device.id, device.label), path: p.path_or_ref })) : null))))));
    devicesPanel.append(
      h("div", { class: "panel-head" }, h("h3", null, t("setupres.devices_title")),
        h("span", { class: "pill" }, String(rows.length))),
      h("p", { class: "settings-lead" }, t("setupres.devices_lead")),
      canEdit ? h("div", { class: "row tight" }, btn(t("relocate.logical_button"), "link", () => openLogicalDialog({ reload: () => reload() })),
        h("span", { class: "muted" }, t("relocate.logical_hint"))) : null,
      list);
    autoMeasure(rows);
  }

  reload();
  return [projectPanel, minePanel, devicesPanel, othersPanel];
}
