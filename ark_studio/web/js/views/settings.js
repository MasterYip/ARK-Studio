import { h, clear, fmtDate, relativeTime, copyText } from "../dom.js";
import { t, hasKey, localizeServer } from "../i18n.js";

// GPU adapter text comes from the server in English (see i18n_server.js).
const gpuText = (text) => localizeServer(text, "server.gpu.");
const gpuKind = (kind) => (kind && hasKey(`server.gpu.kind.${kind}`) ? t(`server.gpu.kind.${kind}`) : kind);
import { icon } from "../icons.js";
import { api, exportUrl } from "../api.js";
import { openModal, confirmDialog } from "../components.js";
import { gpuRegistrationNote, openSetupResources } from "../gpu-note.js";
import { formatRemaining } from "../mobile-core.js";
import { pwaSnapshot, pwaStatusRow, promptInstall } from "../mobile-ui.js";
import { GraphView, arrowGlyph } from "../graph.js";
import {
  ARROW_HEADS, ARROW_PLACEMENTS, ARROW_SIZE_RANGE, ARROW_SIZE_PRESETS,
  defaultArrowSettings, normalizeArrowSettings, resolveArrowStyle, relationReversed, REVERSED_BY_DEFAULT,
} from "../arrows.js";
import {
  GRAPH_DEFAULTS, GRAPH_RANGES, controlToRaw, rawToControl,
  normalizeGraphPhysics, formatPhysicsValue,
} from "../physics.js";
import {
  getShortcut, setShortcut, resetShortcuts, formatShortcut, shortcutConflict,
  shortcutFromEvent, shortcutIsUsable, SHORTCUT_ACTIONS, getTagShortcuts,
  setTagShortcut, removeTagShortcut,
} from "../shortcuts.js";
import {
  normalizeGraphDisplay, resolveGraphDisplay, setGraphDisplayValue, clearGraphDisplayTab,
} from "../graph-display.js";
import { GRAPH_SETTING_TABS, graphTabStrip, graphDisplayRows, graphDisplayPanel } from "../graph-display-panel.js";
import { foldSection } from "../fold-section.js";
import { settingsViewKey } from "../settings-sync-model.js";
import { renderSkillsPanel } from "../skills-panel.js";
import { renderProvidersPanel, renderWorktreesPanel } from "../providers-panel.js";
import { renderAdminPanel } from "../admin-panel.js";
import { renderMembersPanel, renderInviteSignupSetting } from "../invites.js";
import { canManageInvites } from "../invites-model.js";
import { agentTokensPanel, legacyServerTokenPanel } from "./agent-tokens.js";
import { trashPanel } from "./trash-panel.js";
import { agentRulesPanel } from "./agent-rules-panel.js";
import { setupResourcesPanels } from "./setup-resources.js";
import { renderProfilePanels, saveAppearanceOverride } from "../profile.js";
import { appearanceOverrides, APPEARANCE_CHANGED } from "../appearance-overrides.js";
import { homeFilterPanel } from "../home-filter-ui.js";
import { SETTINGS_TABS, DEFAULT_SETTINGS_TAB, GRAPH_SECTION, resolveSettingsTab } from "../settings-tabs.js";
import { markerSettingsPanel } from "./settings-markers.js";
import { appearancePanel } from "../appearance-panel.js";
import { paletteMode } from "../appearance.js";
import { applyAppearance } from "../appearance.js";
import { advancedEnabled, setAdvancedEnabled } from "../advanced.js";
import { agentMessagesEnabled, setAgentMessagesEnabled } from "../agent-messages.js";
import { getLang } from "../i18n.js";
import { themePref } from "../profile.js";
import { userBadge } from "../users.js";
import { cachePanel } from "../cache-panel.js";
import { pluginsPanel } from "../plugins-panel.js";
import { projectStoragePanel } from "../project-storage-panel.js";
import { aboutOverviewCard, aboutUpdateBlock, appOverviewCard, appSettingsPanel, settingsScopeLine } from "../about.js";
import { inDesktopApp } from "../desktop-update-model.js";

const FIELDS = [
  {
    key: "repulsion",
    label: "settings.repulsion",
    hint: "settings.repulsion_hint",
    step: 1,
    defaultControl: 59,
  },
  {
    key: "distance",
    label: "settings.distance",
    hint: "settings.distance_hint",
    step: 1,
    defaultControl: 0,
  },
  {
    key: "gravity",
    label: "settings.gravity",
    hint: "settings.gravity_hint",
    step: 0.0001,
    defaultControl: 95,
  },
  {
    key: "damping",
    label: "settings.damping",
    hint: "settings.damping_hint",
    step: 0.01,
    defaultControl: 30,
  },
];

const ARROW_RELATIONS = [
  "part_of", "derived_from", "supports", "contradicts", "depends_on", "blocks",
  "cites", "inspires", "targets", "produces", "validates", "assigned_to",
  "runs_on", "supersedes", "uses", "relates_to", "unset",
];
const ARROW_RELATION_SIZES = [0.75, 1, 1.5, 2];
const cloneArrows = (value) => JSON.parse(JSON.stringify(normalizeArrowSettings(value)));

/**
 * Visio-style line-end controls shared by the project panel and the personal
 * override.  `getDraft()` returns the arrow settings being edited (mutated in
 * place); `onChange()` runs after every edit.  Returns `{ elements, sync,
 * inputs }`; `sync()` re-reads the draft after it is replaced.
 */
function arrowControls({ getDraft, onChange, relations = ARROW_RELATIONS }) {
  const inputs = [];
  const syncs = [];
  const option = (value, label) => h("option", { value }, label);
  const headLabel = (id) => t(`settings.arrows.head.${id}`);
  const placementLabel = (id) => t(`settings.arrows.placement.${id}`);
  const changed = () => { onChange(); for (const sync of syncs) sync(); };
  const glyphSlot = (resolve) => {
    const slot = h("span", { class: "arrow-glyph-slot" });
    syncs.push(() => { clear(slot); slot.appendChild(arrowGlyph(resolve(), { width: 46, height: 16 })); });
    return slot;
  };

  // Default line end.
  const head = h("select", { class: "input sm", "data-arrow-default": "head", "aria-label": t("settings.arrows.head") },
    ARROW_HEADS.map((id) => option(id, headLabel(id))));
  const placement = h("select", { class: "input sm", "data-arrow-default": "placement", "aria-label": t("settings.arrows.placement") },
    ARROW_PLACEMENTS.map((id) => option(id, placementLabel(id))));
  head.addEventListener("change", () => { getDraft().style.head = head.value; changed(); });
  placement.addEventListener("change", () => { getDraft().style.placement = placement.value; changed(); });
  const [minSize, maxSize] = ARROW_SIZE_RANGE;
  const sizeRange = h("input", {
    type: "range", class: "physics-range", min: String(minSize), max: String(maxSize), step: "0.05",
    "data-arrow-default": "size", "aria-label": t("settings.arrows.size"),
  });
  const sizeValue = h("span", { class: "physics-raw" });
  const presetButtons = Object.entries(ARROW_SIZE_PRESETS).map(([id, value]) => h("button", {
    class: "seg-btn", type: "button", "data-arrow-size": id,
    onclick: () => { getDraft().style.size = value; changed(); },
  }, t(`settings.arrows.size_${id}`)));
  sizeRange.addEventListener("input", () => { getDraft().style.size = Number(sizeRange.value); changed(); });
  const mindTree = h("input", { type: "checkbox", "data-arrow-mind-tree": "" });
  mindTree.addEventListener("change", () => { getDraft().mind_tree = mindTree.checked; changed(); });
  inputs.push(head, placement, sizeRange, mindTree, ...presetButtons);
  syncs.push(() => {
    const { style, mind_tree: tree } = getDraft();
    head.value = style.head;
    placement.value = style.placement;
    sizeRange.value = String(style.size);
    clear(sizeValue);
    sizeValue.append(`${Math.round(style.size * 100)}%`);
    for (const button of presetButtons) {
      const on = Math.abs(ARROW_SIZE_PRESETS[button.dataset.arrowSize] - style.size) < 0.001;
      button.classList.toggle("on", on);
      button.setAttribute("aria-pressed", String(on));
    }
    mindTree.checked = Boolean(tree);
  });
  const row = (label, hint, ...controls) => h("div", { class: "physics-row arrow-row" },
    h("div", { class: "physics-head" },
      h("div", null, h("div", { class: "physics-name" }, label), h("div", { class: "physics-hint" }, hint)),
      h("div", { class: "arrow-controls" }, ...controls)));
  // The default glyph ignores per-relation overrides.
  const defaultGlyph = glyphSlot(() => resolveArrowStyle({ ...getDraft(), relations: {} }, "part_of"));

  // Per-relation overrides: "Default" keeps the project-wide value.
  const table = h("div", { class: "arrow-relations", role: "table", "aria-label": t("settings.arrows.relations") },
    h("div", { class: "arrow-relation-row head", role: "row" },
      h("span", { role: "columnheader" }, t("settings.arrows.relation")),
      h("span", { role: "columnheader" }, t("settings.arrows.head")),
      h("span", { role: "columnheader" }, t("settings.arrows.placement")),
      h("span", { role: "columnheader" }, t("settings.arrows.size")),
      h("span", { role: "columnheader", title: t("settings.arrows.reverse_hint") }, t("settings.arrows.reverse")),
      h("span", { role: "columnheader" }, t("settings.arrows.preview"))));
  for (const relation of relations) {
    const rowEl = h("div", { class: "arrow-relation-row", role: "row", "data-arrow-row": relation });
    const field = (key, values, label, inherit = t("settings.arrows.inherit")) => {
      const select = h("select", {
        class: "input sm", "data-arrow-relation": relation, "data-arrow-field": key,
        "aria-label": `${labelOr(`rel.${relation}`, relation)} · ${t(`settings.arrows.${key}`)}`,
      }, option("", inherit), values.map((value) => option(String(value), label(value))));
      select.addEventListener("change", () => {
        const draft = getDraft();
        const entry = { ...(draft.relations[relation] || {}) };
        if (select.value === "") delete entry[key];
        else entry[key] = key === "size" ? Number(select.value) : key === "reverse" ? select.value === "true" : select.value;
        if (Object.keys(entry).length) draft.relations[relation] = entry;
        else delete draft.relations[relation];
        changed();
      });
      syncs.push(() => {
        const value = getDraft().relations[relation]?.[key];
        select.value = value === undefined ? "" : String(value);
        rowEl.classList.toggle("custom", Boolean(getDraft().relations[relation]));
      });
      inputs.push(select);
      return select;
    };
    rowEl.append(
      h("span", { class: "arrow-relation-name", role: "cell" }, labelOr(`rel.${relation}`, relation)),
      field("head", ARROW_HEADS, headLabel),
      field("placement", ARROW_PLACEMENTS, placementLabel),
      field("size", ARROW_RELATION_SIZES, (value) => `${Math.round(value * 100)}%`),
      // Display only: draw and lay out the relation target -> source.
      field("reverse", [true, false], (value) => t(value ? "settings.arrows.reverse_on" : "settings.arrows.reverse_off"),
        t(REVERSED_BY_DEFAULT.has(relation) ? "settings.arrows.reverse_default_on" : "settings.arrows.reverse_default_off")),
      h("span", { role: "cell" }, glyphSlot(() => {
        const style = resolveArrowStyle(getDraft(), relation);
        return style && relationReversed(getDraft(), relation) ? { ...style, start: style.end, end: style.start } : style;
      })));
    table.appendChild(rowEl);
  }

  const elements = [
    row(t("settings.arrows.head"), t("settings.arrows.head_hint"), head, defaultGlyph),
    row(t("settings.arrows.placement"), t("settings.arrows.placement_hint"), placement),
    h("div", { class: "physics-row arrow-row" },
      h("div", { class: "physics-head" },
        h("div", null, h("div", { class: "physics-name" }, t("settings.arrows.size")),
          h("div", { class: "physics-hint" }, t("settings.arrows.size_hint"))),
        h("div", { class: "physics-values" }, sizeValue)),
      h("div", { class: "physics-slider arrow-size" },
        h("div", { class: "segmented", role: "group", "aria-label": t("settings.arrows.size") }, ...presetButtons),
        sizeRange)),
    h("label", { class: "shortcut-row arrow-row" },
      h("span", null, h("span", { class: "physics-name" }, t("settings.arrows.mind_tree")),
        h("span", { class: "physics-hint" }, t("settings.arrows.mind_tree_hint"))),
      mindTree),
    h("details", { class: "arrow-relations-box", "data-arrow-relations": "" },
      h("summary", null, t("settings.arrows.relations")),
      h("p", { class: "physics-hint" }, t("settings.arrows.relations_hint")),
      table),
  ];
  const sync = () => { for (const item of syncs) item(); };
  sync();
  return { elements, sync, inputs };
}

// The pairing code Settings › Mobile made last, per project (kept across
// repaints of the page so a repaint does not create another one).
const autoPairings = new Map();

export function renderSettings(host, ctx) {
  const guidedVisit = Boolean(ctx.guidance?.active);
  clear(host);
  const project = ctx.S.project || {};
  let saved = normalizeGraphPhysics(project.settings?.graph);
  let draft = { ...saved };
  // Line ends are saved together with the physics (one Save/Discard/Reset).
  let savedArrows = cloneArrows(project.settings?.arrows);
  let arrowDraft = cloneArrows(savedArrows);
  let dirty = false;
  let preview = null;
  let previewData = { nodes: [], edges: [] };
  const syncing = { active: false };

  const page = h("div", { class: "page" });
  // Physics and line ends save together through the tab save bar.
  async function saveGraph() {
    try {
      const data = await api.updateSettings({ graph: draft, arrows: arrowDraft });
      ctx.applySettings(data);
      saved = normalizeGraphPhysics(data.settings?.graph || draft);
      savedArrows = cloneArrows(data.settings?.arrows || arrowDraft);
      // The preview shows project values even when this person overrides them.
      preview?.setPhysics(draft);
      preview?.setArrows(arrowDraft);
      ctx.toast(t("settings.saved"), "ok");
      await ctx.refresh();
      return true;
    } catch (err) {
      ctx.toast(err.message, "err");
      return false;
    }
  }
  function discardGraph() {
    draft = { ...saved };
    arrowDraft = cloneArrows(savedArrows);
    syncAll();
    preview?.setPhysics(draft);
    preview?.setArrows(arrowDraft);
  }
  const resetBtn = h("button", {
    class: "ghost-btn sm", type: "button", "data-graph-reset": "",
    title: t("settings.reset_hint"),
    onclick: () => {
      draft = { ...GRAPH_DEFAULTS };
      arrowDraft = defaultArrowSettings();
      markDirty();
      syncAll();
      preview?.setPhysics(draft);
      preview?.setArrows(arrowDraft);
    },
  }, icon("refresh", "sm"), t("settings.reset"));
  const markDirty = () => setDirty("graph");

  // ------------------------------------------------------------ save bar
  // One place to save per tab: panels that need an explicit save register a
  // source; editing one shows a sticky bar at the top of the tab content with
  // Save / Discard.  Panels that apply immediately never register.
  const saveSources = new Map();
  const tabOfSection = (section) => resolveSettingsTab(section)?.tab || "";
  const saveBarText = h("span", { class: "settings-savebar-text" });
  const saveBarSave = h("button", { class: "primary-btn", type: "button", "data-savebar-save": "",
    onclick: () => saveActiveTab() }, icon("save", "sm"), t("action.save"));
  const saveBarDiscard = h("button", { class: "ghost-btn", type: "button", "data-savebar-discard": "",
    onclick: () => discardActiveTab() }, icon("arrowLeft", "sm"), t("settings.discard"));
  const saveBar = h("div", { class: "settings-savebar", hidden: true, role: "region",
    "aria-label": t("settings.unsaved") },
    h("i", { class: "status-dot idle" }), saveBarText, saveBarDiscard, saveBarSave);
  /** `save()` resolves false on failure; `discard()` restores the saved values. */
  function registerSave(id, { section, label, save, discard, root = null }) {
    if (typeof discard !== "function") throw new Error(`save source ${id} needs a discard`);
    saveSources.set(id, { id, section, label, save, discard, dirty: false });
    if (root) trackDirty(id, root);
  }
  function trackDirty(id, root) {
    const mark = (event) => {
      if (event.target?.closest?.("[data-no-dirty]")) return;
      if (event.target?.disabled) return;
      setDirty(id);
    };
    root.addEventListener("input", mark);
    root.addEventListener("change", mark);
  }
  function setDirty(id, value = true) {
    const source = saveSources.get(id);
    if (!source || syncing.active) return;
    source.dirty = Boolean(value);
    syncSaveBar();
  }
  const dirtyIn = (tab) => [...saveSources.values()].filter((source) => source.dirty && tabOfSection(source.section) === tab);
  function syncSaveBar() {
    const list = dirtyIn(activeTab);
    saveBar.hidden = !list.length;
    saveBarText.textContent = t("settings.savebar.unsaved", { names: list.map((source) => source.label).join(" · ") });
    for (const [key, button] of tabButtons) button.classList.toggle("dirty", dirtyIn(key).length > 0);
  }
  async function saveActiveTab() {
    if (saveBarSave.disabled) return;
    saveBarSave.disabled = true;
    try {
      for (const source of dirtyIn(activeTab)) {
        if (await source.save() !== false) source.dirty = false;
      }
    } finally {
      saveBarSave.disabled = false;
      syncSaveBar();
    }
  }
  // Discard only touches the panels listed in the bar; every source restores
  // its own last saved values, so drafts on other tabs survive.
  function discardActiveTab() {
    for (const source of dirtyIn(activeTab)) {
      source.discard();
      source.dirty = false;
    }
    syncSaveBar();
  }
  const onSaveKey = (event) => {
    if (!page.isConnected) { window.removeEventListener("keydown", onSaveKey); return; }
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "s" && !saveBar.hidden) {
      event.preventDefault();
      saveActiveTab();
    }
  };
  window.addEventListener("keydown", onSaveKey);
  registerSave("graph", { section: "graph", label: t("settings.graph_physics"), save: saveGraph, discard: discardGraph });

  const rows = [];
  const syncAll = () => {
    syncing.active = true;
    for (const row of rows) row.sync();
    projectArrows?.sync();
    syncing.active = false;
  };
  let projectArrows = null;

  const authSession = ctx.S.session || {};
  const demoProject = Boolean(ctx.S.project?.demo || ctx.S.project?.id === "ark-demo");
  // The page header is identical on every tab: eyebrow, title and (below)
  // the scope bar.  Tab-specific actions live in the tab content.
  const pageEyebrow = h("div", { class: "eyebrow" },
    `${t("nav.settings")} · ${t("settings.project_scope")}`);
  // Whose settings these are: this server's; inside the desktop app the
  // app's own settings are one click away (Settings › App).
  const desktopApp = inDesktopApp();
  page.appendChild(h("div", { class: "page-head settings-page-head" },
    h("div", null,
      pageEyebrow,
      h("h1", { class: "page-title" }, t("settings.title")),
      settingsScopeLine(() => jump("app")))));

  const grid = h("div", { class: "settings-grid" });

  // ------------------------------------------------------------ physics panel
  // The slider rows serve the project physics and the personal override.
  function physicsRows({ getDraft, onChange, list }) {
    const elements = [];
    for (const field of FIELDS) {
      const [min, max] = GRAPH_RANGES[field.key];
      const range = h("input", {
        type: "range", min: "0", max: "100", step: "1", class: "physics-range",
        "aria-label": t(field.label),
        value: Math.round(rawToControl(field.key, getDraft()[field.key])),
      });
      const number = h("input", {
        type: "number", class: "input physics-number",
        min: String(min), max: String(max), step: String(field.step),
        "aria-label": t(field.label),
      });
      const rawValue = h("span", { class: "physics-raw" });
      const controlValue = h("span", { class: "physics-control" });

      const sync = () => {
        const raw = getDraft()[field.key];
        range.value = String(Math.round(rawToControl(field.key, raw)));
        number.value = formatPhysicsInput(field.key, raw);
        clear(rawValue);
        rawValue.append(formatPhysicsValue(field.key, raw));
        clear(controlValue);
        controlValue.append(String(Math.round(rawToControl(field.key, raw))));
      };
      const setRaw = (raw) => {
        if (syncing.active) return;
        getDraft()[field.key] = Math.max(min, Math.min(max, Number(raw)));
        onChange();
        sync();
      };
      range.addEventListener("input", () => {
        setRaw(controlToRaw(field.key, Number(range.value)));
      });
      number.addEventListener("change", () => {
        const value = Number(number.value);
        if (!Number.isFinite(value)) { sync(); return; }
        setRaw(value);
      });
      list.push({ sync, inputs: [range, number] });

      elements.push(h("div", { class: "physics-row" },
        h("div", { class: "physics-head" },
          h("div", null,
            h("div", { class: "physics-name" }, t(field.label)),
            h("div", { class: "physics-hint" }, t(field.hint))),
          h("div", { class: "physics-values" },
            h("span", { class: "physics-raw" }, rawValue),
            h("span", { class: "physics-control" }, [
              "(", h("span", null, controlValue), " / 100)",
            ]))),
        h("div", { class: "physics-slider" },
          h("span", { class: "range-end" }, "0"),
          range,
          h("span", { class: "range-end" }, "100")),
        h("div", { class: "physics-raw-range" }, `${t("settings.raw_range")}: ${formatPhysicsValue(field.key, min)} – ${formatPhysicsValue(field.key, max)} · ${t("settings.default")}: ${formatPhysicsValue(field.key, GRAPH_DEFAULTS[field.key])}`)));
    }
    return elements;
  }
  const projectPhysicsNote = h("div", { class: "settings-note hidden", "data-override-note": "graph" });
  const physicsPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.graph_physics")),
      resetBtn),
    projectPhysicsNote,
    ...physicsRows({
      getDraft: () => draft,
      onChange: () => { markDirty(); preview?.setPhysics(draft); },
      list: rows,
    }));

  physicsPanel.appendChild(h("div", { class: "settings-note" },
    icon("info", "sm"),
    h("div", null,
      h("b", null, t("settings.info_hub_note_title")), " ",
      t("settings.info_hub_note"))));

  // ------------------------------------------------------------ line ends
  const projectArrowsNote = h("div", { class: "settings-note hidden", "data-override-note": "arrows" });
  projectArrows = arrowControls({
    getDraft: () => arrowDraft,
    onChange: () => { markDirty(); preview?.setArrows(arrowDraft); },
  });
  const arrowsPanel = h("section", { class: "panel settings-panel", "data-arrows-panel": "" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.arrows.title"))),
    h("p", { class: "settings-lead" }, t("settings.arrows.lead")),
    projectArrowsNote,
    ...projectArrows.elements);

  // ------------------------------------------------------------ shortcuts
  const shortcutEditors = SHORTCUT_ACTIONS.map((action) => {
    const button = h("button", { class: "shortcut-capture", type: "button" },
      formatShortcut(getShortcut(action.id)));
    let captureHandler = null;
    const stopCapture = () => {
      if (captureHandler) window.removeEventListener("keydown", captureHandler, true);
      captureHandler = null;
      button.classList.remove("listening");
      button.textContent = formatShortcut(getShortcut(action.id));
    };
    const startCapture = () => {
      if (captureHandler) return;
      button.classList.add("listening");
      button.textContent = t("settings.press_keys");
      captureHandler = (event) => {
        event.preventDefault();
        event.stopPropagation();
        if (event.key === "Escape") { stopCapture(); return; }
        const spec = shortcutFromEvent(event);
        if (!spec) return;
        if (!shortcutIsUsable(spec, action.id)) {
          stopCapture();
          ctx.toast(t("settings.shortcut_reserved"), "err");
          return;
        }
        const conflict = shortcutConflict(action.id, spec);
        if (conflict) {
          stopCapture();
          const other = SHORTCUT_ACTIONS.find((item) => item.id === conflict);
          ctx.toast(t("settings.shortcut_duplicate",
            { name: other ? t(other.labelKey) : conflict }), "err");
          return;
        }
        setShortcut(action.id, spec);
        stopCapture();
        ctx.toast(t("settings.shortcut_saved", { keys: formatShortcut(spec) }), "ok");
        ctx.refresh();
      };
      window.addEventListener("keydown", captureHandler, true);
    };
    button.addEventListener("click", startCapture);
    const row = h("div", { class: "shortcut-row" },
      h("div", null,
        h("div", { class: "physics-name" }, t(action.labelKey)),
        h("div", { class: "physics-hint" }, action.hintKey
          ? t(action.hintKey) : action.fallback)),
      h("div", { class: "row tight" }, button));
    return row;
  });

  const shortcutList = h("div", { class: "stack" }, ...shortcutEditors);

  // Values kept only in this browser (not shared with the project) carry one
  // badge style; everything else follows the scope picked above the tabs.
  const browserBadge = () => h("span", { class: "pill scope-badge", title: t("settings.browser_scope_hint") },
    t("settings.browser_scope"));

  // ------------------------------------------------------------ InfoHub source
  const hubPathInput = h("input", {
    class: "input", value: ctx.S.infoHubPath || "",
    placeholder: "…/info-hub/data/hub.db", // i18n-ignore: file path
  });
  const saveHubPath = () => {
    const value = hubPathInput.value.trim();
    ctx.S.infoHubPath = value;
    try {
      if (value) localStorage.setItem("ark.infoHubPath", value);
      else localStorage.removeItem("ark.infoHubPath");
    } catch { /* storage may be unavailable */ }
    ctx.toast(t(value ? "settings.infohub.saved" : "settings.infohub.cleared"), "ok");
    return true;
  };
  const hubPathClear = h("button", { class: "ghost-btn" }, t("settings.infohub.auto_detect"));
  hubPathClear.addEventListener("click", () => {
    hubPathInput.value = "";
    ctx.S.infoHubPath = "";
    try { localStorage.removeItem("ark.infoHubPath"); } catch { /* ignore */ }
    setDirty("infohub", false);
    ctx.toast(t("settings.infohub.auto_restored"), "ok");
  });
  const hubPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.infohub.title")),
      browserBadge()),
    h("p", { class: "settings-lead" },
      t("settings.infohub.lead")),
    h("div", { class: "field" }, h("label", null, t("settings.infohub.db_path")), hubPathInput),
    h("div", { class: "assistant-api-actions" }, hubPathClear));
  registerSave("infohub", { section: "infohubSource", label: t("settings.infohub.title"), save: saveHubPath,
    discard: () => { hubPathInput.value = ctx.S.infoHubPath || ""; }, root: hubPathInput });

  // ------------------------------------------------------------ remote project
  const storeValue = (key) => { try { return localStorage.getItem(key) || ""; } catch { return ""; } };
  const remoteHost = h("input", { class: "input", placeholder: "ssh.example.com", value: storeValue("ark.remoteSshHost") });
  const remoteUser = h("input", { class: "input", placeholder: "ubuntu", /* i18n-ignore: sample user */ value: storeValue("ark.remoteSshUser") });
  const remoteSshPort = h("input", { class: "input", placeholder: "22", value: storeValue("ark.remoteSshPort") || "22" });
  const remoteLocalPort = h("input", { class: "input", placeholder: "8890", value: storeValue("ark.remoteLocalPort") || "8890" });
  const remoteMount = h("input", { class: "input", placeholder: "/tmp/ark-project", value: storeValue("ark.remoteMount") });
  const remoteFacts = h("div", { class: "settings-lead", style: { whiteSpace: "pre-wrap" } }, t("settings.remote.loading"));
  const remoteCommand = h("textarea", { class: "input", rows: "3", readonly: true, style: { fontFamily: "ui-monospace, Consolas, monospace", fontSize: "10.5px" } });
  const remoteCopy = h("button", { class: "ghost-btn" }, icon("copy", "sm"), t("settings.remote.copy_tunnel"));
  const remoteMountCopy = h("button", { class: "ghost-btn" }, icon("copy", "sm"), t("settings.remote.copy_mount"));
  const refreshRemoteCommand = () => {
    const host = remoteHost.value.trim() || "<server>";
    const user = remoteUser.value.trim();
    const sshPort = remoteSshPort.value.trim() || "22";
    const localPort = remoteLocalPort.value.trim() || "8890";
    const target = user ? `${user}@${host}` : host;
    remoteCommand.value = `ssh -N -p ${sshPort} -L ${localPort}:127.0.0.1:8890 ${target}`;
    remoteMount.value = remoteMount.value || "";
  };
  const copyValue = async (value, message) => {
    try {
      await navigator.clipboard.writeText(value);
      ctx.toast(message, "ok");
    } catch { ctx.toast(t("settings.clipboard_unavailable"), "err"); }
  };
  [remoteHost, remoteUser, remoteSshPort, remoteLocalPort, remoteMount].forEach((input) => input.addEventListener("input", refreshRemoteCommand));
  const saveRemote = () => {
    try {
      localStorage.setItem("ark.remoteSshHost", remoteHost.value.trim());
      localStorage.setItem("ark.remoteSshUser", remoteUser.value.trim());
      localStorage.setItem("ark.remoteSshPort", remoteSshPort.value.trim());
      localStorage.setItem("ark.remoteLocalPort", remoteLocalPort.value.trim());
      localStorage.setItem("ark.remoteMount", remoteMount.value.trim());
    } catch { /* ignore */ }
    refreshRemoteCommand();
    ctx.toast(t("settings.remote.saved"), "ok");
    return true;
  };
  remoteCopy.addEventListener("click", () => copyValue(remoteCommand.value, t("settings.remote.tunnel_copied")));
  remoteMountCopy.addEventListener("click", () => {
    const host = remoteHost.value.trim() || "<server>";
    const user = remoteUser.value.trim();
    const target = user ? `${user}@${host}` : host;
    copyValue(`sshfs ${target}:${ctx.S.project?.path || "/remote/project"} ${remoteMount.value.trim() || "/tmp/ark-project"}`, t("settings.remote.mount_copied"));
  });
  refreshRemoteCommand();
  const remoteFold = foldSection({ id: "remote-facts", title: t("settings.fold.remote_facts"),
    summary: t("settings.fold.loading"), body: remoteFacts });
  api.remoteInfo().then((info) => {
    const urls = [info.request_url, ...(info.urls || [])].filter(Boolean);
    remoteFold.setSummary(t("settings.fold.urls", { n: new Set(urls).size }));
    remoteFacts.textContent = [
      t("settings.remote.fact_project", { name: info.project }),
      t("settings.remote.fact_path", { path: info.path }),
      t("settings.remote.fact_urls", { urls: [...new Set(urls)].join("  ") }),
      info.media_roots?.length ? t("settings.remote.fact_media_roots", { roots: info.media_roots.join("  ") }) : t("settings.remote.fact_media_project_only"),
      t("settings.remote.fact_shared"),
      t("settings.remote.fact_streamed"),
    ].join("\n");
  }).catch((err) => {
    remoteFacts.textContent = t("settings.remote.unavailable", { error: err.message });
    remoteFold.setSummary(t("settings.gpu.unavailable"));
  });
  const remotePanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.remote.title")),
      browserBadge()),
    h("p", { class: "settings-lead" },
      t("settings.remote.lead")),
    remoteFold.el,
    h("div", { class: "assistant-api-grid" },
      h("div", { class: "field" }, h("label", null, t("settings.remote.ssh_host")), remoteHost),
      h("div", { class: "field" }, h("label", null, t("settings.remote.ssh_user")), remoteUser),
      h("div", { class: "field" }, h("label", null, t("settings.remote.ssh_port")), remoteSshPort),
      h("div", { class: "field" }, h("label", null, t("settings.remote.local_port")), remoteLocalPort)),
    h("div", { class: "field" }, h("label", null, t("settings.remote.mirror_folder")), remoteMount),
    remoteCommand,
    h("div", { class: "assistant-api-actions" }, remoteCopy, remoteMountCopy),
    h("p", { class: "settings-lead" },
      t("settings.remote.mirror_hint")));
  registerSave("remote", { section: "remote", label: t("settings.remote.title"), save: saveRemote,
    discard: () => {
      remoteHost.value = storeValue("ark.remoteSshHost");
      remoteUser.value = storeValue("ark.remoteSshUser");
      remoteSshPort.value = storeValue("ark.remoteSshPort") || "22";
      remoteLocalPort.value = storeValue("ark.remoteLocalPort") || "8890";
      remoteMount.value = storeValue("ark.remoteMount");
      refreshRemoteCommand();
    } });
  for (const input of [remoteHost, remoteUser, remoteSshPort, remoteLocalPort, remoteMount]) trackDirty("remote", input);

  // ------------------------------------------- session uploads (consent)
  // Same project setting as the toggle in Execute > Runs > Session context
  // (docs/agents/session-context.md); saved through the unified save bar.
  const sessionUploadsSaved = () => Boolean(ctx.S.project?.settings?.sessions?.accept_uploads);
  const sessionUploadsAdmin = ctx.S.session?.project_permission === "admin" || !ctx.S.session?.project_permission;
  const sessionUploadsBox = h("input", { type: "checkbox", "data-session-uploads": "",
    checked: sessionUploadsSaved(), disabled: !sessionUploadsAdmin });
  // Remind idle ARK-launched agents of people's replies (absent = on; T-0053).
  const nudgeSaved = () => ctx.S.project?.settings?.sessions?.nudge_idle !== false;
  const nudgeBox = h("input", { type: "checkbox", "data-session-nudge": "", checked: nudgeSaved(), disabled: !sessionUploadsAdmin });
  const sessionUploadsPanel = h("section", { class: "panel settings-panel", "data-session-uploads-panel": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.session_uploads.title"))),
    h("p", { class: "settings-lead" }, t("settings.session_uploads.lead")),
    h("label", { class: "row tight settings-session-uploads" }, sessionUploadsBox, h("span", null, t("ctx.consent"))),
    h("label", { class: "row tight settings-session-nudge", title: t("settings.session_nudge.hint") }, nudgeBox,
      h("span", null, t("settings.session_nudge.label"))),
    sessionUploadsAdmin ? null : h("p", { class: "muted" }, t("settings.session_uploads.admin_only")));
  registerSave("sessionUploads", {
    section: "sessionUploads", label: t("settings.session_uploads.title"), root: sessionUploadsPanel,
    save: async () => {
      try {
        const result = await api.updateSettings({ sessions: { accept_uploads: sessionUploadsBox.checked, nudge_idle: nudgeBox.checked } });
        if (ctx.S.project) ctx.S.project.settings = result.settings || ctx.S.project.settings;
        ctx.toast(t(sessionUploadsBox.checked ? "ctx.consent_on" : "ctx.consent_off"), "ok");
        return true;
      } catch (err) {
        ctx.toast(err.message, "err");
        return false;
      }
    },
    discard: () => { sessionUploadsBox.checked = sessionUploadsSaved(); nudgeBox.checked = nudgeSaved(); },
  });

  // ------------------------------------------------------ mobile / pair phone
  const mobileState = {
    connection: null, pairs: [], created: null, timer: null,
    autoAttempted: false, autoBusy: false,
  };
  const mobileMode = h("span", { class: "pill" }, t("settings.mobile.checking"));
  const mobileFacts = h("div", { class: "sw-facts" });
  const mobileConnectionNote = h("p", { class: "settings-lead" });
  const mobileTunnelBox = h("div", { class: "stack", style: { gap: "5px" } });
  const mobileQrHost = h("div", { class: "mobile-pair-grid" });
  const mobilePairsHost = h("div", { class: "stack", style: { gap: "6px" } });
  const mobileError = h("div", { class: "settings-lead" });
  const mobileAccountBox = h("div", { class: "stack", style: { gap: "5px" } });
  let tunnelHost = "";
  let tunnelPort = "";
  try {
    tunnelHost = localStorage.getItem("ark.mobile.tunnelHost") || "";
    tunnelPort = localStorage.getItem("ark.mobile.tunnelPort") || "";
  } catch { /* storage unavailable */ }

  const tunnelHostInput = h("input", {
    class: "input", value: tunnelHost, placeholder: t("settings.mobile.tunnel_host_placeholder"),
    autocapitalize: "off", autocorrect: "off", spellcheck: "false",
  });
  const tunnelPortInput = h("input", {
    class: "input", inputmode: "numeric", value: tunnelPort, placeholder: "8891",
  });
  const mobileTunnelFields = h("div", { class: "assistant-api-grid mobile-tunnel-fields", style: { marginTop: "8px" } },
    h("div", { class: "field" }, h("label", null, t("settings.mobile.tunnel_host")), tunnelHostInput),
    h("div", { class: "field" }, h("label", null, t("settings.mobile.tunnel_port")), tunnelPortInput));
  const permissionSelect = h("select", { class: "select", "aria-label": t("settings.mobile.permission") },
    h("option", { value: "full", selected: true }, t("settings.mobile.permission_full")),
    h("option", { value: "read_only" }, t("settings.mobile.permission_read_only")));
  const sessionSelect = h("select", { class: "select", "aria-label": t("settings.mobile.session") },
    h("option", { value: "permanent", selected: true }, t("settings.mobile.session_permanent")),
    h("option", { value: String(30 * 24 * 3600) }, t("settings.mobile.days", { n: 30 })),
    h("option", { value: String(7 * 24 * 3600) }, t("settings.mobile.days", { n: 7 })),
    h("option", { value: String(12 * 3600) }, t("settings.mobile.hours", { n: 12 })));
  const pairingBody = () => ({
    permission: permissionSelect.value,
    expires_in: Number(expirySelect.value),
    session_ttl: sessionSelect.value,
  });
  const expirySelect = h("select", { class: "select", "aria-label": t("settings.mobile.code_lifetime") },
    h("option", { value: "300" }, t("settings.mobile.minutes", { n: 5 })),
    h("option", { value: "600", selected: true }, t("settings.mobile.minutes", { n: 10 })),
    h("option", { value: "900" }, t("settings.mobile.minutes", { n: 15 })));
  const generateQrBtn = h("button", { class: "primary-btn", type: "button", disabled: true },
    icon("qr", "sm"), t("settings.mobile.generate_qr"));
  const refreshMobileBtn = h("button", {
    class: "ghost-btn", type: "button",
    onclick: () => loadMobile(),
  }, icon("refresh", "sm"), t("action.refresh"));

  const tunnelCopyBtn = h("button", { class: "btn sm ghost", type: "button", disabled: true },
    icon("copy", "sm"), t("settings.mobile.copy_ssh"));

  function saveTunnelPrefs() {
    try {
      localStorage.setItem("ark.mobile.tunnelHost", tunnelHostInput.value.trim());
      localStorage.setItem("ark.mobile.tunnelPort", tunnelPortInput.value.trim());
    } catch { /* storage unavailable */ }
  }
  tunnelHostInput.addEventListener("input", () => { saveTunnelPrefs(); syncGenerateState(); });
  tunnelPortInput.addEventListener("input", () => { saveTunnelPrefs(); syncGenerateState(); });

  function syncGenerateState() {
    const connection = mobileState.connection;
    if (!connection) { generateQrBtn.disabled = true; return; }
    if (connection.mode !== "loopback") {
      // Public/LAN mode never asks for host/port; the server selects the URL.
      generateQrBtn.disabled = !connection.pairing_auto || mobileState.autoBusy;
      return;
    }
    const host = tunnelHostInput.value.trim();
    const port = Number(tunnelPortInput.value.trim() || connection.tunnel?.local_port || 0);
    generateQrBtn.disabled = !(host && port > 0 && port < 65536);
  }

  function fact(label, value) {
    return h("div", { class: "sw-fact" }, h("span", { class: "k" }, label),
      h("span", { class: "v mono" }, value));
  }

  function renderConnection() {
    const connection = mobileState.connection;
    clear(mobileFacts);
    clear(mobileTunnelBox);
    clear(mobileAccountBox);
    if (!connection) return;
    const autoMode = connection.mode !== "loopback";
    mobileMode.textContent = connection.mode === "public"
      ? t(connection.public_url ? "settings.mobile.mode_public_url" : "settings.mobile.mode_public_bind")
      : t(connection.mode === "lan" ? "settings.mobile.mode_lan" : "settings.mobile.mode_loopback");
    mobileMode.className = `pill ${connection.mode === "loopback" ? "warn" : "ok"}`;
    mobileFolds.connection.setSummary(`${mobileMode.textContent} · ${connection.bind_host}:${connection.port || "?"}`);
    mobileFacts.append(
      fact(t("settings.mobile.fact_bind"), `${connection.bind_host}:${connection.port || "?"}`),
      fact(t("settings.mobile.fact_mode"), hasKey(`server.mobile.mode.${connection.mode}`) ? t(`server.mobile.mode.${connection.mode}`) : connection.mode),
      fact(t("settings.mobile.fact_port"), String(connection.detected_port || connection.port || "?")),
      fact(t("settings.mobile.fact_detected_ip"), connection.detected_ip || connection.detected_public_ip || t("settings.mobile.not_detected")),
      fact(t("settings.mobile.fact_sign_in"), connection.auth_required
        ? (connection.auth_user ? t("settings.mobile.sign_in_required_user", { user: connection.auth_user }) : t("settings.mobile.sign_in_required"))
        : t("settings.mobile.not_configured")),
      fact("HTTPS", t(connection.https ? "settings.mobile.yes" : "settings.mobile.no")));
    if (connection.public_ip) {
      mobileFacts.appendChild(fact(t("settings.mobile.fact_public_ip"), `${connection.public_ip} (${connection.public_ip_source || t("settings.mobile.detected")})`));
    }
    for (const url of connection.advertised_urls || []) {
      mobileFacts.appendChild(fact(t("settings.mobile.fact_advertised"), url));
    }
    if (connection.mode === "public") {
      mobileConnectionNote.textContent = t(connection.public_url
        ? "settings.mobile.note_public_url" : "settings.mobile.note_public_bind")
        + (connection.auth_required ? t("settings.mobile.note_public_auth") : "");
    } else if (connection.mode === "lan") {
      mobileConnectionNote.textContent =
        t("settings.mobile.note_lan")
        + (connection.auth_required ? t("settings.mobile.note_lan_auth") : "");
    } else {
      mobileConnectionNote.textContent =
        t("settings.mobile.note_loopback");
      const command = connection.tunnel?.command || "";
      tunnelCopyBtn.disabled = !command;
      tunnelCopyBtn.onclick = async () => {
        const ok = await copyText(command);
        ctx.toast(t(ok ? "settings.mobile.ssh_copied" : "settings.clipboard_unavailable"), ok ? "ok" : "err");
      };
      mobileTunnelBox.append(
        h("div", { class: "settings-lead", style: { margin: 0 } }, t("settings.mobile.run_on_computer")),
        h("pre", { class: "mobile-tunnel-command" }, command || "—"),
        h("div", { class: "mobile-actions" }, tunnelCopyBtn,
          h("span", { class: "physics-hint", style: { flex: "1 1 220px" } },
            connection.tunnel?.helper || "")),
        h("div", { class: "settings-note" }, icon("alert", "sm"),
          h("div", null, h("b", null, t("settings.mobile.security_label")),
            localizeServer(connection.tunnel?.warning) || t("settings.mobile.tunnel_warning"))));
    }
    // Public/LAN pairing derives the base URL itself; the manual tunnel fields
    // are only meaningful for the documented loopback + SSH-forward workaround.
    mobileTunnelFields.classList.toggle("hidden", autoMode);
    tunnelHostInput.disabled = autoMode;
    tunnelPortInput.disabled = autoMode;
    if (!tunnelPortInput.value && connection.tunnel?.local_port) {
      tunnelPortInput.value = String(connection.tunnel.local_port);
    }
    clear(generateQrBtn);
    generateQrBtn.append(icon("qr", "sm"),
      t(autoMode ? "settings.mobile.regenerate_qr" : "settings.mobile.generate_qr"));
    if (connection.auth_required) {
      // Sign out lives in Personal > Profile & account and the account menu.
      mobileAccountBox.append(
        h("div", { class: "settings-note" }, icon("shield", "sm"),
          h("div", null, h("b", null, t("settings.mobile.signed_in_label")),
            t("settings.mobile.signed_in_detail", { user: connection.auth_user || t("settings.mobile.account") }))));
    }
    const pwa = pwaSnapshot();
    mobileFacts.appendChild(fact(t("settings.mobile.fact_service_worker"),
      t(pwa.registered ? "settings.mobile.sw_registered" : pwa.secure === false ? "settings.mobile.sw_requires_https" : "settings.mobile.sw_registering")));
    clear(mobileError);
    if (connection.loopback_only) {
      mobileError.appendChild(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, t("settings.mobile.loopback_error"))));
    } else if (!connection.pairing_auto) {
      mobileError.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, t("settings.mobile.no_base_url"))));
    }
    syncGenerateState();
    maybeAutoPair();
  }

  async function loadMobile() {
    clear(mobileError);
    mobileError.appendChild(h("div", { class: "settings-lead" }, t("settings.mobile.reading_state")));
    try {
      const [connection, pairs] = await Promise.all([api.connectionInfo(), api.pairings()]);
      mobileState.connection = connection;
      mobileState.pairs = pairs.pairs || [];
      renderConnection();
      renderMobilePairs();
      clear(mobileError);
    } catch (err) {
      clear(mobileError);
      mobileError.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, t("settings.mobile.qr_failed", { error: err.message }))));
    }
  }

  // Public/LAN mode: generate once when the panel opens so the user only scans
  // (or regenerates).  Loopback mode stays manual because it needs a tunnel
  // address that this server cannot discover for the phone.
  // Only while the Mobile section is on screen: a code is a credential, and
  // every tab of Settings is built at once.  A repaint of the page shows the
  // code made earlier while it is still open instead of making another.
  async function maybeAutoPair() {
    if (guidedVisit || ctx.guidance?.active) return;
    const connection = mobileState.connection;
    if (!connection || connection.mode === "loopback" || !connection.pairing_auto) return;
    if (mobileState.created || mobileState.autoAttempted || mobileState.autoBusy) return;
    if (!page.isConnected || activeTab !== tabOfSection("mobile")) return;
    mobileState.autoAttempted = true;
    const earlier = autoPairings.get(ctx.S.project?.id || "");
    const open = earlier && (mobileState.pairs || []).some((pair) => pair.id === earlier.pair?.id
      && !pair.revoked_at && !pair.claimed_at);
    if (open && Date.parse(earlier.pair.expires_at) - Date.now() > 60_000) {
      renderPairingResult(earlier);
      return;
    }
    await performPairing(pairingBody(), { auto: true });
  }

  async function performPairing(body, { auto = false } = {}) {
    if (mobileState.autoBusy) return;
    mobileState.autoBusy = true;
    generateQrBtn.disabled = true;
    clear(generateQrBtn);
    generateQrBtn.append(h("span", { class: "spinner" }), t("settings.mobile.generating"));
    try {
      const data = await api.createPairing(body);
      autoPairings.set(ctx.S.project?.id || "", data);
      renderPairingResult(data);
      const pairs = await api.pairings();
      mobileState.pairs = pairs.pairs || [];
      renderMobilePairs();
      if (!auto) ctx.toast(t("settings.mobile.qr_generated"), "ok");
    } catch (err) {
      clear(mobileQrHost);
      mobileQrHost.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, err.message)));
      if (!auto) ctx.toast(err.message, "err");
    } finally {
      mobileState.autoBusy = false;
      clear(generateQrBtn);
      const autoMode = mobileState.connection?.mode !== "loopback";
      generateQrBtn.append(icon("qr", "sm"),
        t(autoMode ? "settings.mobile.regenerate_qr" : "settings.mobile.generate_qr"));
      syncGenerateState();
    }
  }

  function renderMobilePairs() {
    clear(mobilePairsHost);
    const pairs = mobileState.pairs || [];
    mobileFolds.pairs.setSummary(t("settings.fold.devices", {
      active: pairs.filter((pair) => !pair.revoked_at).length, n: pairs.length }));
    if (!pairs.length) {
      mobilePairsHost.appendChild(h("div", { class: "physics-hint" }, t("settings.mobile.no_pairs")));
      return;
    }
    for (const pair of pairs) {
      const revoked = Boolean(pair.revoked_at);
      mobilePairsHost.appendChild(h("div", {
        class: "project-row", style: { alignItems: "center", gap: "8px" },
      },
        h("div", { class: "main" },
          h("div", { class: "t" }, `${t(pair.source === "desktop" ? "settings.mobile.desktop_link"
            : pair.permission === "full" ? "settings.mobile.full_access" : "settings.mobile.permission_read_only")} · ${pair.state}`),
          h("div", { class: "d" },
            (pair.claimed_at
              ? (pair.session_permanent ? t("settings.mobile.pair_until_revoked") : t("settings.mobile.pair_session_hours", { n: Math.round((pair.session_ttl || 0) / 3600) }))
              : t("settings.mobile.pair_code_expires", { date: fmtDate(pair.expires_at) }))
            + " · " + t("settings.mobile.pair_created", { date: fmtDate(pair.created_at) })
            + (pair.claimed_at ? " · " + t("settings.mobile.pair_claimed", { date: fmtDate(pair.claimed_at) }) : ""))),
        revoked ? h("span", { class: "pill" }, t("settings.mobile.revoked"))
          : h("button", {
            class: "btn sm danger", type: "button",
            onclick: () => revokeMobilePair(pair.id),
          }, icon("trash", "sm"), t("admin.revoke"))));
    }
  }

  async function revokeMobilePair(pairId) {
    try {
      const data = await api.revokePairing(pairId);
      mobileState.pairs = data.pairs || [];
      const projectKey = ctx.S.project?.id || "";
      if (autoPairings.get(projectKey)?.pair?.id === pairId) autoPairings.delete(projectKey);
      if (mobileState.created?.pair?.id === pairId) {
        mobileState.created = null;
        mobileState.autoAttempted = true;
        clear(mobileQrHost);
        mobileQrHost.appendChild(h("div", { class: "physics-hint" },
          t("settings.mobile.revoked_hint")));
        if (mobileState.timer) { clearInterval(mobileState.timer); mobileState.timer = null; }
      }
      renderMobilePairs();
      ctx.toast(t("settings.mobile.revoked_toast"), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  }

  function renderPairingResult(data) {
    mobileState.created = data;
    if (mobileState.timer) clearInterval(mobileState.timer);
    clear(mobileQrHost);
    const qrHost = h("div", { class: "mobile-pair-qr-host" });
    const qr = h("div", { class: "mobile-pair-qr" });
    // The SVG is generated locally by arkstudio.qr from the validated pairing
    // URL; no third-party QR service or script is involved.
    qr.innerHTML = data.qr_svg || "";
    const expiresAt = Date.parse(data.pair.expires_at);
    const countdown = h("span", { class: "pill warn" });
    const tick = () => {
      if (!countdown.isConnected) {
        if (mobileState.timer) { clearInterval(mobileState.timer); mobileState.timer = null; }
        return;
      }
      const remaining = formatRemaining(expiresAt);
      countdown.textContent = remaining === "expired"
        ? t("settings.mobile.expired") : t("settings.mobile.expires_in", { time: remaining });
      countdown.className = `pill ${remaining === "expired" ? "bad" : "warn"}`;
    };
    qrHost.appendChild(qr);
    qrHost.appendChild(h("div", { class: "mobile-pair-meta" }, countdown,
      h("span", { class: "pill" }, t(data.pair.permission === "full" ? "settings.mobile.pill_full" : "settings.mobile.pill_read_only")),
      h("span", { class: "pill" }, t("settings.mobile.pill_base", { source: data.base_source || t("settings.mobile.auto") }))));
    qrHost.appendChild(h("div", { class: "mobile-pair-meta" },
      h("span", null, data.auto
        ? t("settings.mobile.scan_auto")
        : t("settings.mobile.scan"))));
    if (data.requires_login) {
      qrHost.appendChild(h("div", { class: "settings-note" }, icon("shield", "sm"),
        h("div", null, h("b", null, t("settings.mobile.passwordless_label")),
          t("settings.mobile.passwordless_detail"))));
    }
    mobileQrHost.appendChild(qrHost);
    tick();
    mobileState.timer = setInterval(tick, 1000);

    const codeEl = h("div", { class: "mobile-pair-code" }, data.code);
    const urlEl = h("div", { class: "mobile-pair-url" }, data.pairing_url);
    const httpQr = String(data.pairing_url || "").startsWith("http://");
    const copyCode = h("button", { class: "btn sm", type: "button" }, icon("copy", "sm"), t("settings.mobile.copy_code"));
    copyCode.onclick = async () => {
      const ok = await copyText(data.code);
      ctx.toast(t(ok ? "settings.mobile.code_copied" : "settings.clipboard_unavailable"), ok ? "ok" : "err");
    };
    const copyUrl = h("button", { class: "btn sm ghost", type: "button" }, icon("copy", "sm"), t("action.copy_link"));
    copyUrl.onclick = async () => {
      const ok = await copyText(data.pairing_url);
      ctx.toast(t(ok ? "settings.mobile.link_copied" : "settings.clipboard_unavailable"), ok ? "ok" : "err");
    };
    const openBtn = h("a", {
      class: "btn sm ghost", href: data.pairing_url, target: "_blank", rel: "noopener noreferrer",
    }, icon("external", "sm"), t("editor.open_link"));
    const revoke = h("button", { class: "btn sm danger", type: "button" }, icon("trash", "sm"), t("settings.mobile.revoke_now"));
    revoke.onclick = () => revokeMobilePair(data.pair.id);
    mobileQrHost.appendChild(h("div", { class: "stack", style: { gap: "6px" } },
      h("div", { class: "physics-name" }, t("settings.mobile.code_title")),
      h("div", { class: "physics-hint" }, t("settings.mobile.code_hint")),
      codeEl,
      h("div", { class: "mobile-actions" }, copyCode, copyUrl, openBtn, revoke),
      h("div", { class: "physics-name", style: { marginTop: "5px" } }, t("settings.mobile.connection_url")),
      urlEl,
      httpQr ? h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, h("b", null, t("settings.mobile.plain_http_label")),
          t("settings.mobile.plain_http_detail"))) : null,
      h("div", { class: "settings-note" }, icon("shield", "sm"),
        h("div", null, h("b", null, t("settings.mobile.scope_label")),
          t(data.pair.permission === "full" ? "settings.mobile.scope_full" : "settings.mobile.scope_read_only")))));
  }

  generateQrBtn.addEventListener("click", async () => {
    const connection = mobileState.connection;
    if (!connection || mobileState.autoBusy) return;
    const body = pairingBody();
    if (connection.mode === "loopback") {
      const host = tunnelHostInput.value.trim();
      const port = Number(tunnelPortInput.value.trim() || connection.tunnel?.local_port || 0);
      if (!host || !(port > 0 && port < 65536)) {
        ctx.toast(t("settings.mobile.tunnel_required"), "err");
        tunnelHostInput.focus();
        return;
      }
      body.tunnel_host = host;
      body.tunnel_port = port;
    }
    await performPairing(body, { auto: false });
  });

  // Server facts, the pairing history and the app downloads are status, not
  // settings: they fold.  The QR controls stay visible.
  const mobilePwaRow = pwaStatusRow();
  const mobileFolds = {
    connection: foldSection({ id: "mobile-connection", title: t("settings.fold.mobile_connection"),
      summary: t("settings.mobile.checking"),
      body: [mobileFacts, mobileConnectionNote, mobileTunnelBox] }),
    pairs: foldSection({ id: "mobile-pairs", title: t("settings.mobile.active_pairs"), body: mobilePairsHost }),
    apps: foldSection({ id: "mobile-apps", title: t("settings.mobile.apps_title"), summary: mobilePwaRow,
      body: [h("p", { class: "settings-lead" }, ...tNodes("settings.mobile.apps_lead", {
        release: h("a", { href: RELEASE_PAGE, target: "_blank", rel: "noopener noreferrer" }, t("settings.mobile.apps_release")),
      })),
        androidAppsBox((apps) => {
          if (!apps?.native?.available) return;
          mobileFolds.apps.setSummary([mobilePwaRow, h("span", null, t("settings.fold.apps"))]);
        })] }),
  };
  const mobilePanel = h("section", { class: "panel settings-panel mobile-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.mobile.title")),
      mobileMode),
    h("p", { class: "settings-lead" },
      t("settings.mobile.lead")),
    mobileAccountBox,
    mobileTunnelFields,
    h("div", { class: "assistant-api-grid", style: { marginTop: "8px" } },
      h("div", { class: "field" }, h("label", null, t("settings.mobile.permission")), permissionSelect),
      h("div", { class: "field" }, h("label", null, t("settings.mobile.session")), sessionSelect),
      h("div", { class: "field" }, h("label", null, t("settings.mobile.qr_valid_for")), expirySelect)),
    h("div", { class: "assistant-api-actions" }, generateQrBtn, refreshMobileBtn),
    mobileError,
    mobileQrHost,
    h("div", { class: "settings-fold-stack" }, mobileFolds.connection.el, mobileFolds.pairs.el, mobileFolds.apps.el),
    h("div", { class: "assistant-api-actions" },
      h("button", {
        class: "btn ghost", type: "button",
        onclick: async () => {
          const ok = await promptInstall();
          ctx.toast(t(ok ? "settings.mobile.install_prompt_shown" : "settings.mobile.install_use_menu"),
            ok ? "ok" : "err");
        },
      }, icon("smartphone", "sm"), t("settings.mobile.install_web_app")),
      h("button", {
        class: "btn ghost", type: "button",
        onclick: () => { window.open("./manifest.webmanifest", "_blank", "noopener"); },
      }, icon("file", "sm"), t("settings.mobile.open_manifest"))),
    h("div", { class: "settings-note" }, icon("shield", "sm"),
      h("div", null,
        h("b", null, t("settings.mobile.security_boundary_label")),
        t("settings.mobile.security_boundary"))));
  loadMobile();

  const shortcutPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.shortcuts")),
      browserBadge()),
    shortcutList,
    h("div", { class: "assistant-api-actions" },
      h("button", {
        class: "ghost-btn sm",
        onclick: () => { resetShortcuts(); ctx.refresh(); },
      }, icon("refresh", "sm"), t("settings.reset_shortcuts"))),
    h("div", { class: "shortcut-readonly" },
      h("div", { class: "shortcut-help-row" },
        h("span", null, t("settings.mind_keys")),
        h("button", {
          class: "shortcut-help", type: "button", title: t("settings.graph_help.title"),
          onclick: () => openModal({
            title: t("settings.graph_help.title"),
            body: h("div", { class: "graph-help-help-modal" },
              [["Space", t("settings.graph_help.edit_title")], ["Enter", t("settings.graph_help.sibling_below")],
                ["Shift+Enter", t("settings.graph_help.sibling_above")], ["Tab", t("settings.graph_help.child_node")],
                ["Delete", t("settings.graph_help.delete_selected")], [t("settings.graph_help.alt_drag"), t("settings.graph_help.create_link")],
                [t("settings.graph_help.arrow_keys"), t("settings.graph_help.move_selection")], ["?", t("settings.graph_help.this_help")]].map(([key, text]) =>
                h("div", { class: "graph-help-row" }, h("kbd", null, key), h("span", null, text)))),
            actions: [{ label: t("action.close") }],
          }),
        }, "?"))),
    h("div", { class: "settings-note" },
      icon("info", "sm"),
      h("div", null, t("settings.shortcuts_note"))));

  // ------------------------------------------------------- common tag shortcuts
  const tagInput = h("input", { class: "input", placeholder: "topic:validation" });
  const tagCaptureBtn = h("button", { class: "shortcut-capture", type: "button" }, t("settings.tag_shortcuts.record"));
  const tagAddBtn = h("button", { class: "primary-btn", type: "button", disabled: true },
    icon("plus", "sm"), t("settings.tag_shortcuts.add"));
  const tagList = h("div", { class: "tag-shortcut-list" });
  let tagSpec = "";
  let tagCaptureHandler = null;

  function stopTagCapture() {
    if (tagCaptureHandler) window.removeEventListener("keydown", tagCaptureHandler, true);
    tagCaptureHandler = null;
    tagCaptureBtn.classList.remove("listening");
    tagCaptureBtn.textContent = tagSpec ? formatShortcut(tagSpec) : t("settings.tag_shortcuts.record");
  }

  function renderTagShortcuts() {
    clear(tagList);
    const entries = Object.entries(getTagShortcuts());
    if (!entries.length) {
      tagList.appendChild(h("div", { class: "settings-note" },
        icon("info", "sm"), h("div", null, t("settings.tag_shortcuts.empty"))));
      return;
    }
    for (const [tag, spec] of entries) {
      tagList.appendChild(
        h("div", { class: "shortcut-row" },
          h("div", null,
            h("div", { class: "physics-name" }, tag),
            h("div", { class: "physics-hint" }, t("settings.tag_shortcuts.graph_nodes"))),
          h("div", { class: "row tight" },
            h("kbd", null, formatShortcut(spec)),
            h("button", {
              class: "ghost-btn sm", type: "button", title: t("settings.tag_shortcuts.remove"),
              onclick: () => { removeTagShortcut(tag); renderTagShortcuts(); },
            }, icon("trash", "sm"))))
      );
    }
  }

  tagInput.addEventListener("input", () => {
    tagAddBtn.disabled = !tagInput.value.trim() || !tagSpec;
  });
  tagCaptureBtn.addEventListener("click", () => {
    stopTagCapture();
    tagCaptureBtn.classList.add("listening");
    tagCaptureBtn.textContent = t("settings.press_keys");
    tagCaptureHandler = (event) => {
      event.preventDefault();
      event.stopPropagation();
      if (event.key === "Escape") { stopTagCapture(); return; }
      const spec = shortcutFromEvent(event);
      if (!spec) return;
      if (!shortcutIsUsable(spec)) {
        ctx.toast(t("settings.shortcut_reserved"), "err");
        return;
      }
      tagSpec = spec;
      stopTagCapture();
      tagAddBtn.disabled = !tagInput.value.trim();
    };
    window.addEventListener("keydown", tagCaptureHandler, true);
  });
  tagAddBtn.addEventListener("click", () => {
    const tag = tagInput.value.trim();
    if (!tag || !tagSpec) return;
    const duplicate = Object.entries(getTagShortcuts()).find(([other, spec]) => other !== tag && spec === tagSpec);
    if (duplicate) {
      ctx.toast(t("settings.tag_shortcuts.duplicate", { tag: duplicate[0] }), "err");
      return;
    }
    try {
      setTagShortcut(tag, tagSpec);
      tagInput.value = "";
      tagSpec = "";
      tagAddBtn.disabled = true;
      stopTagCapture();
      renderTagShortcuts();
      ctx.toast(t("settings.tag_shortcuts.saved", { tag }), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  });
  renderTagShortcuts();
  const tagShortcutPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.tag_shortcuts.title")),
      browserBadge()),
    h("p", { class: "settings-lead" },
      t("settings.tag_shortcuts.lead")),
    h("div", { class: "field-row" },
      h("div", { class: "field" }, h("label", null, t("settings.tag_shortcuts.tag")), tagInput),
      h("div", { class: "field" }, h("label", null, t("settings.tag_shortcuts.shortcut")),
        h("div", { class: "row tight" }, tagCaptureBtn, tagAddBtn))),
    tagList);

  // ------------------------------------------------- graph display (project)
  // Settings › Appearance › Graphs: one small tab per graph type (General,
  // Force & node, Mind, Local graph).  These are the project defaults, saved
  // for everyone through the Save bar; a person's own value (Personal ›
  // Appearance & input › My graphs) wins for them, setting by setting.
  const savedProjectDisplay = () => normalizeGraphDisplay(ctx.S.project?.settings?.graph_display);
  let projectDisplayDraft = savedProjectDisplay();
  const projectDisplayRows = {};
  const projectDisplayPanels = {};
  const syncProjectDisplay = () => {
    syncing.active = true;
    for (const rows of Object.values(projectDisplayRows)) rows.sync();
    syncing.active = false;
  };
  for (const tab of GRAPH_SETTING_TABS) {
    projectDisplayRows[tab] = graphDisplayRows({
      tab,
      value: (key) => resolveGraphDisplay(projectDisplayDraft, {}).values[tab][key],
      onChange: (key, value) => {
        projectDisplayDraft = setGraphDisplayValue(projectDisplayDraft, tab, key, value);
        setDirty("graph-display");
      },
    });
    const reset = h("button", {
      class: "ghost-btn sm", type: "button", "data-graph-tab-reset": tab, title: t("settings.graphs.reset_tab_defaults_hint"),
      onclick: () => {
        projectDisplayDraft = clearGraphDisplayTab(projectDisplayDraft, tab);
        syncProjectDisplay();
        setDirty("graph-display");
      },
    }, icon("refresh", "sm"), t("settings.reset"));
    projectDisplayPanels[tab] = graphDisplayPanel({ tab, rows: projectDisplayRows[tab], actions: reset });
  }
  async function saveProjectDisplay() {
    try {
      const data = await api.updateSettings({ graph_display: projectDisplayDraft });
      ctx.applySettings(data);
      projectDisplayDraft = savedProjectDisplay();
      syncProjectDisplay();
      ctx.toast(t("settings.saved"), "ok");
      return true;
    } catch (err) {
      ctx.toast(err.message, "err");
      return false;
    }
  }
  registerSave("graph-display", { section: GRAPH_SECTION, label: t("settings.graphs.display_label"),
    save: saveProjectDisplay,
    discard: () => { projectDisplayDraft = savedProjectDisplay(); syncProjectDisplay(); } });
  syncProjectDisplay();
  const projectDisplayNote = h("div", { class: "settings-note hidden", "data-override-note": "graph_display" });

  const shortcutGuideRow = (keys, text) => h("div", { class: "shortcut-guide-row" },
    h("kbd", null, keys), h("span", null, text));
  const shortcutGuideGroup = (title, rows) => h("section", { class: "shortcut-guide-group" },
    h("h4", null, title), rows);
  const shortcutGuidePanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.guide.title"))),
    foldSection({ id: "shortcut-guide", title: t("settings.fold.reference"),
      summary: t("settings.fold.groups", { n: 6 }), body: h("div", { class: "shortcut-guide-grid" },
      shortcutGuideGroup(t("settings.guide.global"), [
        shortcutGuideRow(formatShortcut(getShortcut("palette")), t("settings.guide.open_palette")),
        shortcutGuideRow(formatShortcut(getShortcut("chat")), t("settings.guide.open_chat")),
        shortcutGuideRow(formatShortcut(getShortcut("newUnit")), t("settings.guide.create_unit")),
        shortcutGuideRow("Esc", t("settings.guide.close")),
      ]),
      shortcutGuideGroup(t("settings.guide.graph_navigation"), [
        shortcutGuideRow("R / E", t("settings.guide.read_edit")),
        shortcutGuideRow("←↑↓→", t("settings.guide.move_selection")),
        shortcutGuideRow("F", t("settings.guide.fit_graph")),
        shortcutGuideRow("?", t("settings.guide.graph_help")),
        shortcutGuideRow("Space / Enter", t("settings.guide.edit_or_sibling")),
      ]),
      shortcutGuideGroup(t("settings.guide.graph_editing"), [
        shortcutGuideRow("Shift+Enter / Tab", t("settings.guide.sibling_or_child")),
        shortcutGuideRow("Delete", t("settings.guide.delete_selected")),
        shortcutGuideRow(t("settings.graph_help.alt_drag"), t("settings.guide.create_link")),
        shortcutGuideRow(t("settings.guide.double_click_node"), t("settings.guide.edit_title_edit_mode")),
        shortcutGuideRow("Ctrl+C / Ctrl+V", t("settings.guide.copy_paste")),
        shortcutGuideRow("Ctrl+X", t("settings.guide.cut_subtree")),
        shortcutGuideRow("Ctrl+Shift+C", t("settings.guide.copy_subtree")),
        shortcutGuideRow("Ctrl+Z", t("settings.guide.undo")),
        shortcutGuideRow("Ctrl+Enter", t("settings.guide.insert_parent")),
        shortcutGuideRow("Shift+Enter", t("settings.guide.new_line")),
      ]),
      shortcutGuideGroup(t("settings.guide.mind"), [
        shortcutGuideRow("+ / -", t("settings.guide.expand_one")),
        shortcutGuideRow("* / /", t("settings.guide.expand_recursive")),
        shortcutGuideRow("Ctrl+A", t("settings.guide.select_all")),
        shortcutGuideRow("Ctrl+Shift+A", t("settings.guide.select_level")),
      ]),
      shortcutGuideGroup(t("settings.guide.mouse"), [
        shortcutGuideRow(t("settings.guide.left_drag"), t("settings.guide.box_select")),
        shortcutGuideRow(t("settings.guide.right_drag"), t("settings.guide.pan")),
        shortcutGuideRow(t("settings.guide.wheel"), t("settings.guide.zoom")),
        shortcutGuideRow(t("settings.guide.ctrl_wheel"), t("settings.guide.pan_vertical")),
        shortcutGuideRow(t("settings.guide.shift_wheel"), t("settings.guide.pan_horizontal")),
        shortcutGuideRow(t("settings.guide.right_click"), t("settings.guide.context_menu")),
      ]),
      shortcutGuideGroup(t("settings.guide.text_editing"), [
        shortcutGuideRow("Enter", t("settings.guide.commit_title")),
        shortcutGuideRow("Esc", t("settings.guide.cancel_title")),
      ])) }).el);

  // ------------------------------------------------------------ preview
  const previewHost = h("div", { class: "graph-host settings-preview-host" });
  const previewPanel = h("section", { class: "panel settings-preview" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.preview")),
      h("div", { class: "row tight" },
        h("button", {
          class: "btn sm ghost",
          onclick: () => { if (preview) { preview.reheat(); reloadPreviewData(); } },
        }, icon("refresh", "sm"), t("settings.relayout")))),
    h("div", { class: "settings-preview-note" }, t("settings.preview_hint")),
    previewHost);
  const infoPanel = h("section", { class: "panel settings-project" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.project"))),
    h("div", { class: "settings-project-body" },
      h("div", { class: "settings-project-name" }, project.name || t("common.untitled")),
      h("div", { class: "settings-project-path", title: project.path || "" }, project.path || "—"),
      h("div", { class: "settings-project-meta" },
        h("span", { class: "pill" }, `${t("common.updated")} ${relativeTime(project.updated)}`),
        h("span", { class: "pill" }, t("settings.travels"))),
      h("p", { class: "settings-project-note" }, t("settings.travels_hint"))),
    foldSection({ id: "project-json", title: t("settings.saved_values"), className: "settings-json",
      summary: "project.json · graph", // i18n-ignore: file and key names
      body: h("pre", { class: "code-box" }, JSON.stringify({ graph: saved }, null, 2)) }).el);

  // ------------------------------------------------------------ assistant API
  const apiBaseInput = h("input", { class: "input", placeholder: "https://tokenhub.tencentmaas.com/v1" });
  const apiModelInput = h("input", { class: "input", placeholder: "deepseek-v4.1-flash-1017" });
  const apiKeyInput = h("input", {
    class: "input", type: "password", autocomplete: "new-password",
    placeholder: t("settings.api.key_placeholder"),
  });
  const apiEffort = h("select", { class: "select" },
    // i18n-ignore: API values; labels come from settings.api.effort_*
    ["low", "medium", "high", "max"].map((value) => h("option", { value }, t(`settings.api.effort_${value}`))));
  const apiStatus = h("span", { class: "settings-status" });
  const apiMessage = h("div", { class: "settings-note" });
  const apiTest = h("button", { class: "ghost-btn" }, icon("check", "sm"), t("settings.api.test"));
  const apiClear = h("button", { class: "ghost-btn danger" }, t("settings.api.clear_key"));
  let apiConfig = null;

  function renderApiStatus() {
    clear(apiStatus);
    const source = apiConfig?.sources?.api_key || apiConfig?.source || "none";
    apiStatus.append(h("span", { class: `pill ${apiConfig?.configured ? "ok" : "warn"}` },
      h("i", { class: `status-dot ${apiConfig?.configured ? "" : "idle"}`.trim() }),
      t(apiConfig?.configured ? "settings.api.key_configured" : "settings.api.no_key")));
    apiStatus.append(h("span", { class: "pill" },
      `${t("settings.inherited")}: ${t("settings.source_" +
        (["personal", "project", "global"].includes(source) ? source : "none"))}`));
    clear(apiMessage);
    // The saved key hint already shows in the key field's placeholder.
    if (apiConfig?.personal_config_required) {
      apiMessage.append(icon("alert", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("settings.personal_config_required") })),
        t("settings.personal_config_required_hint")));
    }
  }

  async function loadAssistantConfig() {
    try {
      const data = await api.assistantConfig();
      apiConfig = data.config;
      apiBaseInput.value = apiConfig.base_url || "";
      apiModelInput.value = apiConfig.model || "";
      apiEffort.value = apiConfig.reasoning_effort || "high";
      apiKeyInput.value = "";
      apiKeyInput.placeholder = apiConfig.has_key ? t("settings.api.saved_key", { hint: apiConfig.key_hint }) : t("settings.api.paste_key");
      renderApiStatus();
    } catch (err) {
      apiConfig = { configured: false, path: "", has_key: false };
      renderApiStatus();
      apiStatus.append(h("span", { class: "pill warn" }, err.message));
    }
  }

  async function saveAssistantConfig() {
    apiTest.disabled = true;
    try {
      const body = {
        base_url: apiBaseInput.value.trim(),
        model: apiModelInput.value.trim(),
        reasoning_effort: apiEffort.value,
      };
      if (apiKeyInput.value.trim()) body.api_key = apiKeyInput.value.trim();
      const data = await api.saveAssistantConfig(body);
      apiConfig = data.config;
      apiKeyInput.value = "";
      apiKeyInput.placeholder = apiConfig.has_key ? t("settings.api.saved_key", { hint: apiConfig.key_hint }) : t("settings.api.paste_key");
      renderApiStatus();
      ctx.toast(t("settings.api.saved"), "ok");
      return true;
    } catch (err) {
      ctx.toast(err.message, "err");
      return false;
    } finally {
      apiTest.disabled = false;
    }
  }

  apiTest.addEventListener("click", async () => {
    if (!(await saveAssistantConfig())) return;
    setDirty("assistant", false);
    apiTest.disabled = true;
    try {
      const data = await api.testAssistantConfig();
      ctx.toast(`API OK · ${data.model} · ${data.latency_ms} ms`, "ok"); // i18n-ignore: status line
    } catch (err) {
      ctx.toast(t("settings.api.test_failed", { error: err.message }), "err");
    } finally {
      apiTest.disabled = false;
    }
  });
  apiClear.addEventListener("click", async () => {
    if (!window.confirm(t("settings.api.confirm_clear"))) return;
    try {
      const data = await api.saveAssistantConfig({ clear_key: true });
      apiConfig = data.config;
      apiKeyInput.value = "";
      renderApiStatus();
      ctx.toast(t("settings.api.key_cleared"), "ok");
    } catch (err) { ctx.toast(err.message, "err"); }
  });

  const assistantPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.personal_assistant"))),
    h("div", { class: "assistant-api-grid", "data-assistant-form": "" },
      h("div", { class: "field full" }, h("label", null, t("settings.api.base_url")), apiBaseInput),
      h("div", { class: "field" }, h("label", null, t("tasks.models.label")), apiModelInput),
      h("div", { class: "field" }, h("label", null, t("settings.api.effort")), apiEffort),
      h("div", { class: "field full" }, h("label", null, t("settings.api.key")), apiKeyInput)),
    h("div", { class: "assistant-api-actions" }, apiTest, apiClear),
    apiStatus, apiMessage);
  registerSave("assistant", { section: "assistant", label: t("settings.personal_assistant"),
    save: saveAssistantConfig, discard: () => { loadAssistantConfig(); },
    root: assistantPanel.querySelector("[data-assistant-form]") });
  loadAssistantConfig();

  const skillsPanel = renderSkillsPanel(ctx);

  // ------------------------------------------------------------ GPU servers
  const gpuState = { status: null, servers: [], registration: null, probe: null, queue: null, jobs: [] };
  const gpuPill = (text, kind = "") => h("span", { class: `pill ${kind}`.trim() }, text);
  const gpuStatus = h("span", { class: "settings-status" });
  const gpuFacts = h("div", { class: "settings-lead", style: { whiteSpace: "pre-wrap" } },
    t("settings.gpu.loading"));
  const gpuHint = h("div", { class: "settings-note" });
  const gpuServerList = h("div", { class: "gpu-list" });
  const gpuQueueStatus = h("span", { class: "settings-status" });
  const gpuQueueHost = h("div", { class: "stack" });
  const gpuQueueRefresh = h("button", { class: "ghost-btn sm", type: "button" },
    icon("refresh", "sm"), t("settings.gpu.refresh_queue"));
  const gpuProbeMsg = h("span", { class: "settings-status" });
  const gpuProbeLabel = h("span", null, t("settings.gpu.probe"));
  const gpuProbeBtn = h("button", { class: "primary-btn", type: "button", disabled: true },
    icon("cpu", "sm"), gpuProbeLabel);
  const gpuProbeResults = h("div", { class: "gpu-results" },
    h("div", { class: "settings-note" }, icon("info", "sm"),
      h("div", null, t("settings.gpu.no_probe"))));

  const gpuServerSelect = h("select", { class: "select", "aria-label": t("settings.gpu.server") });
  const gpuIndices = h("input", { class: "input", placeholder: t("settings.gpu.indices_placeholder") });
  const gpuJob = h("input", { class: "input", value: "ark-job" });
  const gpuTimeout = h("input", { class: "input", type: "number", min: "1", max: "1440", value: "60" });
  const gpuWorkDir = h("input", { class: "input", placeholder: t("settings.gpu.workdir_placeholder") });
  const gpuCommand = h("textarea", {
    class: "input", rows: "3", placeholder: "python train.py --cfg config.yaml", // i18n-ignore: command sample
    style: { fontFamily: "ui-monospace, Consolas, monospace", fontSize: "10.5px" },
  });
  const gpuDispatchOut = h("pre", { class: "code-box" }, t("settings.gpu.no_request"));
  const gpuDispatchNote = h("div", { class: "settings-note" }, icon("shield", "sm"),
    h("div", null,
      h("b", null, t("settings.gpu.dry_run_label")),
      t("settings.gpu.dry_run_detail")));
  const gpuDispatchLabel = h("span", null, t("settings.gpu.prepare"));
  const gpuDispatchBtn = h("button", { class: "primary-btn", type: "button", disabled: true },
    icon("file", "sm"), gpuDispatchLabel);
  const gpuCopyBtn = h("button", { class: "ghost-btn", type: "button", disabled: true },
    icon("copy", "sm"), t("settings.gpu.copy_request"));

  function renderGpuStatus() {
    const status = gpuState.status;
    clear(gpuStatus);
    if (!status) {
      gpuStatus.append(gpuPill(t("settings.gpu.unavailable"), "bad"));
      gpuFacts.textContent = t("settings.gpu.status_unavailable");
      return;
    }
    const stateLabel = t(!status.available ? "settings.gpu.adapter_missing"
      : status.enabled ? "settings.gpu.enabled" : "settings.gpu.disabled_default");
    gpuStatus.append(gpuPill(stateLabel,
      !status.available ? "bad" : status.enabled ? "ok" : "warn"));

    const lines = [];
    lines.push(status.vendor
      ? t("settings.gpu.fact_adapter", { name: status.vendor.name, version: status.vendor.version, path: status.vendor.path })
      : t("settings.gpu.fact_adapter_missing"));
    // Only the server admin (or local mode) gets the config path (T-0018).
    lines.push(status.config_path
      ? t(status.config_present ? "settings.gpu.fact_config_present" : "settings.gpu.fact_config_missing", { path: status.config_path })
      : t(status.config_present ? "settings.gpu.fact_config_admin" : "settings.gpu.fact_config_admin_missing"));
    if (status.config_error) lines.push(t("settings.gpu.config_error", { error: gpuText(status.config_error) }));
    lines.push(status.ssh.available ? t("settings.gpu.fact_ssh", { binary: status.ssh.binary }) : t("settings.gpu.fact_ssh_missing", { hint: gpuText(status.ssh.hint) }));
    lines.push(t("settings.gpu.fact_servers", { n: status.server_count }));
    lines.push(t(status.asyncssh.available ? "settings.gpu.fact_asyncssh" : "settings.gpu.fact_asyncssh_missing"));
    gpuFacts.textContent = lines.join("\n");
    gpuFolds.adapter.setSummary(`${stateLabel} · ${t("settings.gpu.fact_servers", { n: status.server_count })}`);

    clear(gpuHint);
    if (!status.enabled) {
      gpuHint.append(icon("info", "sm"), h("div", null,
        h("b", null, t("settings.gpu.disabled_label")),
        ...tNodes("settings.gpu.disabled_hint", {
          flag: h("code", null, "enabled: true"),
          env: h("code", null, "ARK_GPU_PROBE=1"),
        })));
    } else {
      gpuHint.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("settings.gpu.readonly_label")),
        t("settings.gpu.readonly_detail")));
    }
    gpuProbeBtn.disabled = !status.enabled || !status.ssh.available;
    gpuProbeBtn.title = !status.ssh.available ? gpuText(status.ssh.hint)
      : t(!status.enabled ? "settings.gpu.enable_first" : "settings.gpu.probe_title");
    gpuDispatchBtn.disabled = !status.enabled;
  }

  function renderGpuServers() {
    clear(gpuServerList);
    clear(gpuServerSelect);
    const servers = gpuState.servers || [];
    gpuFolds.servers.setSummary(gpuState.status?.enabled
      ? t("settings.fold.count", { n: servers.length }) : t("settings.gpu.disabled_default"));
    if (!gpuState.status?.enabled) {
      gpuServerList.append(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, gpuState.status?.config_error
          ? t("settings.gpu.config_error", { error: gpuText(gpuState.status.config_error) })
          : t("settings.gpu.enable_to_list"))));
      return;
    }
    if (!servers.length) {
      gpuServerList.append(gpuRegistrationNote(gpuState.registration, () => openSetupResources(ctx))
        || h("div", { class: "settings-note" }, icon("info", "sm"), h("div", null, t("settings.gpu.no_servers"))));
      return;
    }
    for (const server of servers) {
      gpuServerSelect.append(h("option", { value: server.name }, server.name));
      gpuServerList.append(h("div", { class: "gpu-row" },
        h("div", null,
          h("div", { class: "gpu-server-name" }, server.name),
          server.note ? h("div", { class: "physics-hint" }, server.note) : null),
        server.host ? h("div", { class: "gpu-server-host" },
          `${server.user ? server.user + "@" : ""}${server.host}:${server.port}`) : null,
        h("div", { class: "row tight" },
          ...(server.tags || []).map((tag) => gpuPill(tag)),
          server.enabled ? gpuPill(t("settings.gpu.pill_enabled"), "ok") : gpuPill(t("settings.gpu.pill_disabled"), "warn"))));
    }
  }

  function renderGpuProbe(data) {
    clear(gpuProbeResults);
    gpuFolds.probe.setSummary(t("settings.gpu.reachable_count", { ok: data.summary?.ok ?? 0, n: data.summary?.probed ?? 0 }));
    gpuFolds.probe.el.open = true;
    if (data.skipped?.length) {
      gpuProbeResults.append(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, t("settings.gpu.skipped", { servers: data.skipped.join(", ") }))));
    }
    for (const result of data.servers || []) {
      const block = h("div", { class: "gpu-result" },
        h("div", { class: "gpu-result-head" },
          h("b", null, result.server),
          result.host ? h("span", { class: "gpu-server-host" }, `${result.host}:${result.port}`) : null,
          result.ok ? gpuPill(t("settings.gpu.reachable"), "ok")
            : gpuPill(gpuKind(result.error?.kind) || t("settings.gpu.failed"), "bad"),
          h("span", { class: "settings-status" }, `${result.duration_ms} ms`)));
      if (!result.ok) {
        block.append(h("div", { class: "gpu-error" },
          `${gpuKind(result.error?.kind) || t("settings.gpu.error")}: ${gpuText(result.error?.message) || t("settings.gpu.probe_failed")}`));
      } else if (!result.gpus?.length) {
        block.append(h("div", { class: "gpu-error" }, t("settings.gpu.no_rows")));
      } else {
        for (const gpu of result.gpus) {
          block.append(h("div", { class: "gpu-gpu-row" },
            h("span", { class: "gpu-idx" }, `#${gpu.index}`),
            h("span", { class: "gpu-name", title: gpu.name }, gpu.name),
            h("span", null, gpu.free_memory_mb == null
              ? t("settings.gpu.free_none") : t("settings.gpu.free_mb", { n: Math.round(gpu.free_memory_mb) })),
            h("span", null, gpu.utilization_pct == null
              ? t("settings.gpu.util_none") : t("settings.gpu.util_pct", { n: gpu.utilization_pct })),
            h("span", { class: "gpu-score" }, gpu.memory_total_mb
              ? t("settings.gpu.mem_pct", { n: Math.round(100 * (gpu.memory_used_mb || 0) / gpu.memory_total_mb) }) : t("settings.gpu.mem_none"))));
        }
      }
      if (result.warnings?.length) {
        block.append(h("div", { class: "gpu-warn" }, result.warnings.join(" · ")));
      }
      gpuProbeResults.append(block);
    }
    if (data.ranked?.length) {
      gpuProbeResults.append(h("div", { class: "gpu-ranked" },
        h("div", { class: "physics-name" }, t("settings.gpu.ranked_title")),
        ...data.ranked.map((item) => h("div", { class: "gpu-ranked-row" },
          h("span", { class: "gpu-score" }, `#${item.rank ?? "—"}`),
          h("span", null, `${item.server}[${item.gpu_index}]`),
          h("span", { class: "gpu-name" }, item.gpu_name),
          h("span", null,
            `${t("settings.gpu.free_mb", { n: Math.round(item.free_memory_mb) })} · ${t("settings.gpu.util_pct", { n: item.utilization_pct ?? "—" })}`)))));
    }
  }

  function renderGpuProbeError(message) {
    clear(gpuProbeResults);
    gpuFolds.probe.setSummary(t("settings.gpu.failed"));
    gpuFolds.probe.el.open = true;
    gpuProbeResults.append(h("div", { class: "settings-note" }, icon("alert", "sm"),
      h("div", null, message)));
  }

  function renderGpuQueue() {
    clear(gpuQueueHost);
    const jobs = gpuState.jobs || [];
    const activeJobs = jobs.filter((job) => ["queued", "starting", "running"].includes(job.status)).length; // i18n-ignore: API status values
    gpuFolds.queue.setSummary([gpuQueueStatus,
      gpuState.queue?.enabled ? h("span", null, t("settings.fold.jobs", { n: jobs.length, active: activeJobs })) : null]);
    if (!gpuState.queue?.enabled) {
      gpuQueueHost.append(h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, t("settings.gpu.queue_disabled"))));
      return;
    }
    if (!jobs.length) {
      gpuQueueHost.append(h("div", { class: "empty sm" }, t("settings.gpu.no_jobs")));
      return;
    }
    for (const job of jobs) {
      gpuQueueHost.append(h("div", { class: "gpu-row" },
        h("div", null,
          h("div", { class: "gpu-server-name" }, `${job.job} · ${job.id}`),
          h("div", { class: "physics-hint" },
            `${job.server || t("settings.gpu.job_queued")}${job.gpu_indices?.length ? ` · GPU ${job.gpu_indices.join(",")}` : ""}`
            + ` · ${job.owner || t("settings.gpu.job_unknown_owner")}`)),
        h("div", { class: "row tight" },
          gpuPill(job.status, job.status === "completed" ? "ok"
            : job.status === "errored" ? "bad" : job.status === "running" ? "warn" : ""),
          ["queued", "starting", "running"].includes(job.status) // i18n-ignore: API status values
            ? h("button", {
              class: "ghost-btn sm danger", type: "button",
              onclick: async () => {
                try {
                  await api.gpuCancel(job.id);
                  await loadGpuQueue();
                } catch (err) { ctx.toast(err.message, "err"); }
              },
            }, icon("ban", "sm"), t("action.cancel")) : null)));
    }
  }

  async function loadGpuQueue() {
    try {
      gpuState.queue = await api.gpuQueueStatus();
      gpuState.jobs = gpuState.queue.enabled ? (await api.gpuJobs()).jobs || [] : [];
      clear(gpuQueueStatus);
      gpuQueueStatus.append(gpuPill(t(gpuState.queue.enabled ? "settings.gpu.queue_enabled" : "settings.gpu.queue_off"),
        gpuState.queue.enabled ? "ok" : "warn"));
      renderGpuQueue();
    } catch (err) {
      clear(gpuQueueStatus);
      gpuQueueStatus.append(gpuPill(t("settings.gpu.queue_unavailable"), "bad"));
      clear(gpuQueueHost);
      gpuQueueHost.append(h("div", { class: "settings-note" }, icon("alert", "sm"),
        h("div", null, err.message)));
    }
  }
  gpuQueueRefresh.addEventListener("click", loadGpuQueue);

  async function loadGpu() {
    try {
      gpuState.status = await api.gpuStatus();
      renderGpuStatus();
      if (gpuState.status.enabled) {
        const listed = await api.gpuServers();
        gpuState.servers = listed.servers || [];
        gpuState.registration = listed.registration || null;
      } else {
        gpuState.servers = [];
        gpuState.registration = null;
      }
      renderGpuServers();
      await loadGpuQueue();
    } catch (err) {
      gpuState.status = null;
      renderGpuStatus();
      clear(gpuHint);
      gpuHint.append(icon("alert", "sm"), h("div", null, err.message));
    }
  }

  gpuProbeBtn.addEventListener("click", async () => {
    gpuProbeBtn.disabled = true;
    gpuProbeLabel.textContent = t("settings.gpu.probing");
    clear(gpuProbeMsg);
    gpuProbeMsg.append(gpuPill(t("settings.gpu.probing_pill"), "warn"));
    try {
      const data = await api.gpuProbe({});
      gpuState.probe = data;
      renderGpuProbe(data);
      clear(gpuProbeMsg);
      gpuProbeMsg.append(gpuPill(t("settings.gpu.reachable_count", { ok: data.summary.ok, n: data.summary.probed }),
        data.summary.failed ? "warn" : "ok"));
      ctx.toast(t("settings.gpu.probed", { n: data.summary.probed }), "ok");
    } catch (err) {
      renderGpuProbeError(err.message);
      clear(gpuProbeMsg);
      gpuProbeMsg.append(gpuPill(t("settings.gpu.failed"), "bad"));
      ctx.toast(t("settings.gpu.probe_error", { error: err.message }), "err");
    } finally {
      gpuProbeLabel.textContent = t("settings.gpu.probe");
      gpuProbeBtn.disabled = !gpuState.status?.enabled || !gpuState.status?.ssh?.available;
    }
  });

  gpuDispatchBtn.addEventListener("click", async () => {
    const command = gpuCommand.value.trim();
    if (!command) {
      ctx.toast(t("settings.gpu.enter_command"), "err");
      return;
    }
    gpuDispatchBtn.disabled = true;
    gpuDispatchLabel.textContent = t("settings.gpu.preparing");
    try {
      const projectLabel = String(ctx.S.project?.name || "ark")
        .replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64) || "ark";
      const data = await api.gpuPrepareDispatch({
        server: gpuServerSelect.value || "",
        gpus: gpuIndices.value.trim(),
        job: gpuJob.value.trim() || "ark-job",
        project: projectLabel,
        timeout_minutes: Number(gpuTimeout.value) || 60,
        work_dir: gpuWorkDir.value.trim(),
        command,
      });
      gpuDispatchOut.textContent = JSON.stringify(data, null, 2);
      gpuFolds.dispatch.setSummary(t("settings.fold.prepared"));
      gpuCopyBtn.disabled = false;
      clear(gpuDispatchNote);
      gpuDispatchNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("settings.gpu.prepared_label")),
        t("settings.gpu.prepared_detail")));
      ctx.toast(t("settings.gpu.prepared_toast"), "ok");
    } catch (err) {
      clear(gpuDispatchNote);
      gpuDispatchNote.append(icon("alert", "sm"), h("div", null, err.message));
      ctx.toast(err.message, "err");
    } finally {
      gpuDispatchLabel.textContent = t("settings.gpu.prepare");
      gpuDispatchBtn.disabled = !gpuState.status?.enabled;
    }
  });

  gpuCopyBtn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(gpuDispatchOut.textContent);
      ctx.toast(t("settings.gpu.request_copied"), "ok");
    } catch { ctx.toast(t("settings.clipboard_unavailable"), "err"); }
  });

  // Status, server lists, probe output, the dry-run builder and the queue are
  // not settings: they fold to a one-line summary so the tab stays short.
  const gpuFolds = {
    adapter: foldSection({ id: "gpu-adapter", title: t("settings.fold.gpu_adapter"), body: gpuFacts }),
    servers: foldSection({ id: "gpu-servers", title: t("settings.gpu.configured_servers"), body: gpuServerList }),
    probe: foldSection({ id: "gpu-probe", title: t("settings.gpu.probe_results"),
      summary: t("settings.fold.not_run"),
      body: [h("p", { class: "physics-hint" }, t("settings.gpu.probe_results_hint")), gpuProbeResults] }),
    dispatch: foldSection({ id: "gpu-dispatch", title: t("settings.gpu.dispatch_title"),
      summary: t("settings.fold.dry_run"),
      body: [
        h("p", { class: "physics-hint" }, t("settings.gpu.dispatch_hint")),
        h("div", { class: "assistant-api-grid" },
          h("div", { class: "field" }, h("label", null, t("settings.gpu.field_server")), gpuServerSelect),
          h("div", { class: "field" }, h("label", null, t("settings.gpu.field_gpus")), gpuIndices),
          h("div", { class: "field" }, h("label", null, t("settings.gpu.field_job")), gpuJob),
          h("div", { class: "field" }, h("label", null, t("settings.gpu.field_timeout")), gpuTimeout),
          h("div", { class: "field full" }, h("label", null, t("settings.gpu.field_workdir")), gpuWorkDir),
          h("div", { class: "field full" }, h("label", null, t("settings.command")), gpuCommand)),
        h("div", { class: "assistant-api-actions" }, gpuDispatchBtn, gpuCopyBtn),
        gpuDispatchNote,
        gpuDispatchOut] }),
    queue: foldSection({ id: "gpu-queue", title: t("settings.gpu.queue_title"),
      summary: gpuQueueStatus,
      body: [h("p", { class: "settings-lead" }, t("settings.gpu.queue_lead")),
        h("div", { class: "assistant-api-actions" }, gpuQueueRefresh), gpuQueueHost] }),
  };
  const gpuPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.gpu.title")),
      gpuStatus),
    h("p", { class: "settings-lead" },
      t("settings.gpu.lead")),
    gpuHint,
    h("div", { class: "assistant-api-actions" }, gpuProbeBtn, gpuProbeMsg),
    h("div", { class: "settings-fold-stack" }, ...Object.values(gpuFolds).map((fold) => fold.el)));
  loadGpu();

  const providersPanel = renderProvidersPanel();
  const worktreesPanel = renderWorktreesPanel();

  // ------------------------------------------------ per-user / per-project agents
  const AGENT_ENV_DEFAULT = {
    claude: "ANTHROPIC_API_KEY", codex: "OPENAI_API_KEY", dsh: "TENCENTHUB_API_KEY",
    gemini: "GEMINI_API_KEY", cursor: "CURSOR_API_KEY",
  };
  const AGENT_IDS = ["dsh", "claude", "codex", "opencode", "cursor", "gemini"];
  let effectiveSettings = null;
  let agentRows = [];
  const personalAgentsList = h("div", { class: "stack" });
  const projectAgentsList = h("div", { class: "stack" });
  const personalAgentsStatus = h("span", { class: "settings-status" });
  const projectAgentsStatus = h("span", { class: "settings-status" });
  const personalAgentsNote = h("div", { class: "settings-note" });
  const projectAgentsNote = h("div", { class: "settings-note" });

  const sourcePill = (source) => {
    const known = ["personal", "project", "global", "none"];
    const key = known.includes(source) ? source : "none";
    return h("span", { class: `pill ${key === "none" ? "warn" : ""}`.trim() },
      `${t("settings.inherited")}: ${t("settings.source_" + key)}`);
  };

  function providerMeta(id) {
    return (effectiveSettings?.agents?.providers || []).find((item) => item.id === id) || {};
  }

  function renderAgentEditors() {
    agentRows = [];
    clear(personalAgentsList);
    clear(projectAgentsList);
    const personalProviders = effectiveSettings?.personal?.agents?.providers || {};
    const projectProviders = effectiveSettings?.project_settings?.agents?.providers || {};
    for (const id of AGENT_IDS) {
      const meta = providerMeta(id);
      const personal = personalProviders[id] || {};
      const project = projectProviders[id] || {};
      // ---- personal editor
      const personalCommand = h("input", {
        class: "input",
        placeholder: t("settings.inherit_placeholder") + (meta.command ? ` · ${meta.command.join(" ")}` : ""),
        value: demoProject ? "" : (Array.isArray(personal.command)
          ? personal.command.join(" ") : (personal.command || "")),
        disabled: demoProject,
      });
      const personalEnabled = h("input", { type: "checkbox" });
      personalEnabled.checked = !demoProject && personal.enabled !== false;
      personalEnabled.disabled = demoProject;
      const envNames = personal.env_names?.length
        ? personal.env_names
        : [AGENT_ENV_DEFAULT[id]].filter(Boolean);
      const envInputs = envNames.map((name) => ({
        name,
        input: h("input", {
          class: "input", type: "password", autocomplete: "new-password",
          placeholder: `${name} · ${personal.env_hints?.[name] || t("settings.credential")}`,
        }),
      }));
      const personalEnvHost = h("div", { class: "stack" },
        ...envInputs.map((item) => h("div", { class: "field" },
          h("label", null, item.name), item.input)));
      const resetBtn = h("button", {
        class: "ghost-btn sm", type: "button", disabled: demoProject,
        title: t("settings.agent_override.clear_hint"),
        onclick: async () => {
          // Saved immediately (unlike Graph physics' draft reset), so confirm first.
          const ok = await confirmDialog(t("settings.agent_override.clear_confirm", { name: meta.label || id }),
            { title: t("settings.agent_override.clear"), confirmLabel: t("settings.agent_override.clear") });
          if (!ok) return;
          try {
            await api.savePersonalSettings("agents", {
              providers: { [id]: { command: "", clear_env: envNames } },
            });
            ctx.toast(t("settings.personal_saved"), "ok");
            await loadEffective();
          } catch (err) { ctx.toast(err.message, "err"); }
        },
      }, icon("refresh", "sm"), t("settings.agent_override.clear"));
      personalAgentsList.appendChild(h("section", { class: "panel settings-panel" },
        h("div", { class: "panel-head" },
          h("h3", null, meta.label || id), h("div", { class: "row tight" },
            sourcePill(meta.command_source), resetBtn)),
        h("div", { class: "assistant-api-grid" },
          h("div", { class: "field full" }, h("label", null, t("settings.command")), personalCommand),
          h("div", { class: "field" }, h("label", null, t("settings.gpu.enabled")), personalEnabled),
          h("div", { class: "field full" }, h("label", null, t("settings.credential")), personalEnvHost))));
      agentRows.push({ id, mode: "personal", command: personalCommand,
                       enabled: personalEnabled, envInputs, envNames, meta });
      // ---- project editor: credential variable names only.  Commands are
      // personal settings by design (a project admin must not gain RCE).
      const projectEnv = h("input", {
        class: "input", value: demoProject ? "" : (project.env_names || []).join(", "),
        placeholder: AGENT_ENV_DEFAULT[id] || "ENV_NAME_1, ENV_NAME_2", // i18n-ignore: env var names
        disabled: demoProject,
      });
      const projectEnabled = h("input", { type: "checkbox" });
      projectEnabled.checked = !demoProject && project.enabled !== false;
      projectEnabled.disabled = demoProject;
      projectAgentsList.appendChild(h("section", { class: "panel settings-panel" },
        h("div", { class: "panel-head" },
          h("h3", null, meta.label || id)),
        h("div", { class: "assistant-api-grid" },
          h("div", { class: "field full" }, h("label", null, t("settings.credential")), projectEnv),
          h("div", { class: "field" }, h("label", null, t("settings.gpu.enabled")), projectEnabled))));
      agentRows.push({ id, mode: "project",
                       enabled: projectEnabled, envNames: projectEnv, meta });
    }
    clear(personalAgentsNote);
    if (demoProject) {
      personalAgentsNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("tasks.demo_disabled_title") })),
        t("tasks.demo_disabled")));
    } else if (effectiveSettings?.agents?.personal_config_required) {
      personalAgentsNote.append(icon("alert", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("settings.personal_config_required") })),
        t("settings.personal_config_required_hint")));
    } else {
      personalAgentsNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("common.label_lead", { text: t("settings.inherited") })),
        (effectiveSettings?.agents?.providers || [])
          .filter((item) => item.overridden || item.project_configured)
          .map((item) => `${item.id} (${item.command_source})`).join(", ")
        || t("settings.source_global")));
    }
    clear(projectAgentsNote);
    if (demoProject) {
      projectAgentsNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("tasks.demo_disabled_title") })),
        t("tasks.demo_disabled")));
    } else {
      projectAgentsNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("settings.project_agents_hint"))));
    }
  }

  async function loadEffective() {
    try {
      effectiveSettings = await api.effectiveSettings();
    } catch (err) {
      effectiveSettings = null;
      ctx.toast(t("settings.effective_unavailable", { error: err.message }), "err");
    }
    renderAgentEditors();
  }

  async function savePersonalAgents() {
    if (demoProject) {
      ctx.toast(t("tasks.demo_disabled"), "info");
      return;
    }
    const providers = {};
    for (const row of agentRows.filter((item) => item.mode === "personal")) {
      const command = row.command.value.trim();
      const env = {};
      for (const item of row.envInputs) {
        if (item.input.value.trim()) env[item.name] = item.input.value.trim();
      }
      providers[row.id] = { command, enabled: row.enabled.checked, env };
    }
    try {
      await api.savePersonalSettings("agents", { providers });
      ctx.toast(t("settings.personal_saved"), "ok");
      await loadEffective();
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  }

  async function saveProjectAgents() {
    if (demoProject) {
      ctx.toast(t("tasks.demo_disabled"), "info");
      return;
    }
    const providers = {};
    for (const row of agentRows.filter((item) => item.mode === "project")) {
      const names = String(row.envNames?.value || "")
        .split(",").map((name) => name.trim()).filter(Boolean);
      // An empty credential field still carries "enabled": dropping the row
      // whenever no names were typed lost an unticked box on every save.
      providers[row.id] = names.length || !row.enabled.checked
        ? { env_names: names, enabled: row.enabled.checked }
        : null;
    }
    try {
      await api.updateSettings({ agents: { providers } });
      ctx.toast(t("settings.saved"), "ok");
      ctx.refresh();
      await loadEffective();
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  }

  const personalAgentsPanel = h("div", { class: "stack" },
    h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" },
        h("h3", null, t("settings.personal_agents")), personalAgentsStatus),
      h("p", { class: "settings-lead" }, t("settings.personal_agents_hint")),
      personalAgentsNote),
    personalAgentsList);
  registerSave("personalAgents", { section: "personalAgents", label: t("settings.personal_agents"),
    save: savePersonalAgents, discard: () => renderAgentEditors(), root: personalAgentsList });

  const projectAgentsPanel = h("div", { class: "stack" },
    h("section", { class: "panel settings-panel" },
      h("div", { class: "panel-head" },
        h("h3", null, t("settings.project_agents")), projectAgentsStatus),
      projectAgentsNote),
    projectAgentsList);
  registerSave("projectAgents", { section: "projectAgents", label: t("settings.project_agents"),
    save: saveProjectAgents, discard: () => renderAgentEditors(), root: projectAgentsList });

  // ----------------------------------------------- project assistant settings
  const projectAssistant = project.settings?.assistant || {};
  const paBase = h("input", { class: "input", value: projectAssistant.base_url || "",
    placeholder: "https://project-endpoint.example/v1" });
  const paModel = h("input", { class: "input", value: projectAssistant.model || "",
    placeholder: "project-model" });
  const paEffort = h("select", { class: "select" },
    ["", "low", "medium", "high", "max"].map((value) => h("option", { value }, // i18n-ignore: API values
      value ? t(`settings.api.effort_${value}`) : t("settings.inherit_placeholder"))));
  paEffort.value = projectAssistant.reasoning_effort || "";
  const paEnv = h("input", { class: "input", value: projectAssistant.api_key_env || "",
    placeholder: t("settings.api.env_placeholder") });
  async function saveProjectAssistant() {
    try {
      await api.updateSettings({ assistant: {
        base_url: paBase.value.trim(), model: paModel.value.trim(),
        reasoning_effort: paEffort.value, api_key_env: paEnv.value.trim(),
      } });
      ctx.toast(t("settings.saved"), "ok");
      await ctx.refresh();
      await loadEffective();
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  }
  const projectAssistantForm = h("div", { class: "assistant-api-grid" },
      h("div", { class: "field full" }, h("label", null, t("settings.api.base_url")), paBase),
      h("div", { class: "field" }, h("label", null, t("tasks.models.label")), paModel),
      h("div", { class: "field" }, h("label", null, t("settings.api.effort")), paEffort),
      h("div", { class: "field full" }, h("label", null, t("settings.credential")), paEnv));
  const projectAssistantPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.project_assistant"))),
    projectAssistantForm);
  registerSave("projectAssistant", { section: "projectAssistant", label: t("settings.project_assistant"),
    save: saveProjectAssistant, root: projectAssistantForm,
    discard: () => {
      paBase.value = projectAssistant.base_url || "";
      paModel.value = projectAssistant.model || "";
      paEffort.value = projectAssistant.reasoning_effort || "";
      paEnv.value = projectAssistant.api_key_env || "";
    } });

  // --------------------------------------------------------- personal account
  const sessionInfo = authSession;
  const roleText = (role) => labelOr(`admin.role.${role}`, role);
  const accountPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.personal_account")),
      h("span", { class: "pill ok" }, roleText(sessionInfo.role || "member"))),
    h("div", { class: "settings-lead" },
      h("div", null, sessionInfo.user ? userBadge(sessionInfo.user, { size: "sm", showUsername: true })
        : h("b", null, "—")),
      h("div", null, `${sessionInfo.org?.name || "ARK"} · ` +
        t(sessionInfo.is_admin ? "admin.role.admin" : "admin.role.member")),
      h("div", null, t("settings.account.project_permission", {
        permission: sessionInfo.project_permission
          ? labelOr(`admin.permission.${sessionInfo.project_permission}`, sessionInfo.project_permission)
          : t("settings.account.permission_none"),
      }))),
    !sessionInfo.local && sessionInfo.authenticated
      ? h("div", { class: "assistant-api-actions" },
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: async () => {
            try { await api.logout(); } catch { /* cookie may already be gone */ }
            location.replace("./login");
          },
        }, icon("external", "sm"), t("settings.sign_out")),
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: async () => {
            try { await api.logout(); } catch { /* cookie may already be gone */ }
            location.replace("./login");
          },
        }, icon("user", "sm"), t("settings.switch_account")))
      : null,
    !sessionInfo.local && sessionInfo.authenticated
      ? h("div", { class: "settings-note" }, icon("info", "sm"),
        h("div", null, t("settings.account_switch_hint")))
      : null);

  // -------------------------------------------------- personal Linux account
  const linuxState = { data: null, loading: false };
  const linuxSelect = h("select", { class: "select" });
  const linuxStatus = h("span", { class: "settings-status" });
  const linuxNote = h("div", { class: "settings-note" });
  const linuxTest = h("button", { class: "ghost-btn", type: "button", disabled: true },
    icon("check", "sm"), t("settings.linux_test"));
  function renderLinuxAccount() {
    const data = linuxState.data || {};
    const capability = data.capability || {};
    const assigned = data.assigned || [];
    clear(linuxSelect);
    for (const account of assigned) {
      linuxSelect.append(h("option", {
        value: account, selected: account === data.selected,
      }, account));
    }
    linuxTest.disabled = !assigned.length;
    linuxSelect.disabled = !assigned.length;
    clear(linuxStatus);
    clear(linuxNote);
    linuxStatus.append(h("span", {
      class: `pill ${data.selected ? "ok" : "warn"}`.trim(),
    }, data.selected ? data.selected : t("settings.linux_not_selected")));
    if (capability.linux === false) {
      linuxNote.append(icon("info", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("settings.linux_only") })),
        t("settings.linux_capability_hint")));
    } else if (!assigned.length) {
      linuxNote.append(icon("info", "sm"), h("div", null,
        t("settings.linux_no_assignment")));
    } else {
      linuxNote.append(icon("shield", "sm"), h("div", null,
        h("b", null, t("common.sentence_lead", { text: t("settings.linux_selected_hint") })),
        t("settings.linux_sudo_hint")));
    }
  }
  async function loadLinuxAccount() {
    try {
      const response = await api.personalEnvironment();
      linuxState.data = response.settings || {};
    } catch (err) {
      linuxState.data = {};
      ctx.toast(err.message, "err");
    }
    renderLinuxAccount();
  }
  async function saveLinuxAccount() {
    try {
      const response = await api.savePersonalEnvironment({
        linux_account: linuxSelect.value || "",
      });
      linuxState.data = response.settings || linuxState.data;
      renderLinuxAccount();
      ctx.toast(t("settings.linux_saved"), "ok");
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  }
  linuxTest.addEventListener("click", async () => {
    linuxTest.disabled = true;
    try {
      const result = await api.savePersonalEnvironment({
        test: true, linux_account: linuxSelect.value || "",
      });
      const tested = result.settings || result;
      ctx.toast(`${t("settings.linux_test_ok")} · ${tested.account}` +
        (tested.home ? ` · ${tested.home}` : ""), "ok");
    } catch (err) {
      ctx.toast(`${t("settings.linux_test_failed")}: ${err.message}`, "err");
    } finally {
      linuxTest.disabled = !(linuxState.data?.assigned || []).length;
    }
  });
  const linuxPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.linux_account")),
      h("div", { class: "row tight" }, linuxStatus,
        h("button", {
          class: "ghost-btn", type: "button",
          onclick: () => loadLinuxAccount(),
        }, icon("refresh", "sm"), t("admin.refresh")))),
    h("p", { class: "settings-lead" }, t("settings.linux_hint")),
    h("div", { class: "field" }, h("label", null, t("settings.linux_account")), linuxSelect),
    h("div", { class: "assistant-api-actions" }, linuxTest),
    linuxNote);
  registerSave("environment", { section: "environment", label: t("settings.linux_account"),
    save: saveLinuxAccount, discard: () => renderLinuxAccount(), root: linuxSelect });
  loadLinuxAccount();

  const isOrgAdmin = Boolean(ctx.S.session?.is_admin ||
    ctx.S.session?.role === "owner" || ctx.S.session?.local);
  const adminPanel = isOrgAdmin ? renderAdminPanel(ctx) : null;

  const deleteProjectName = h("input", {
    class: "input", placeholder: ctx.S.project?.name || "",
    autocomplete: "off",
  });
  const deleteProjectPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("settings.delete.title")),
      h("span", { class: "pill warn" }, t("settings.delete.danger_zone"))),
    h("p", { class: "settings-lead" },
      t("settings.delete.lead")),
    h("div", { class: "field" },
      h("label", null, t("settings.delete.confirm_label")),
      deleteProjectName),
    h("div", { class: "assistant-api-actions" },
      h("button", {
        class: "btn danger", type: "button",
        onclick: async () => {
          if (deleteProjectName.value.trim() !== (ctx.S.project?.name || "")) {
            ctx.toast(t("settings.delete.mismatch"), "err");
            return;
          }
          try {
            const data = await api.deleteProject({
              name: deleteProjectName.value.trim(),
            });
            ctx.applyBootstrap(data);
            ctx.toast(t("settings.delete.deleted", { name: ctx.S.project?.name || t("settings.delete.project_fallback") }), "ok");
          } catch (err) {
            ctx.toast(err.message, "err");
          }
        },
      }, icon("trash", "sm"), t("settings.delete.button"))));

  // ------------------------------------------------------------ black box
  const blackboxList = h("div", { class: "stack", style: { gap: "6px" } },
    h("div", { class: "settings-lead" }, t("settings.snapshot.loading")));
  const blackboxNote = h("p", { class: "settings-lead" },
    t("settings.snapshot.lead"));
  const blackboxCreate = h("button", { class: "primary-btn", type: "button" },
    icon("archive", "sm"), t("settings.snapshot.create"));
  const formatBytes = (value) => {
    const size = Number(value) || 0;
    if (size < 1024) return `${size} B`; // i18n-ignore: unit
    if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
    if (size < 1024 * 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)} MB`;
    return `${(size / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  };
  let blackboxBusy = false;
  const blackboxReason = (reason) => (reason
    ? labelOr(`settings.snapshot.reason.${reason}`, reason)
    : t("settings.snapshot.reason_default"));
  const blackboxFold = foldSection({ id: "blackbox-history", title: t("settings.fold.snapshot_history"),
    summary: t("settings.fold.loading"), body: blackboxList });
  const loadBlackbox = async () => {
    try {
      const data = await api.blackbox();
      clear(blackboxList);
      const snapshots = data.snapshots || [];
      blackboxFold.setSummary(snapshots.length
        ? t("settings.fold.snapshots", { n: snapshots.length, date: fmtDate(snapshots[0].created_at) })
        : t("settings.snapshot.empty"));
      if (!snapshots.length) {
        blackboxList.appendChild(h("div", { class: "empty sm" }, t("settings.snapshot.empty")));
        return;
      }
      for (const snapshot of snapshots) {
        const canRestore = ctx.S.session?.project_permission === "admin";
        blackboxList.appendChild(h("div", {
          class: "row", style: { alignItems: "center", gap: "8px", padding: "6px 0", borderTop: "1px solid var(--line-soft)" },
        },
          h("div", { style: { minWidth: "0", flex: "1" } },
            h("div", { style: { fontWeight: "600", fontSize: "11px" } },
              `${blackboxReason(snapshot.reason)} · ${fmtDate(snapshot.created_at)}`),
            h("div", { class: "physics-hint" },
              t("settings.snapshot.meta", { units: snapshot.unit_count || 0, links: snapshot.link_count || 0, size: formatBytes(snapshot.snapshot_size) }))),
          h("span", { class: "pill" }, snapshot.integrity || "ok"), // i18n-ignore: integrity value
          canRestore ? h("button", {
            class: "btn sm ghost", type: "button", disabled: blackboxBusy,
            onclick: async () => {
              const label = `${blackboxReason(snapshot.reason)} ${fmtDate(snapshot.created_at)}`;
              if (!window.confirm(t("settings.snapshot.confirm_restore", { label }))) return;
              blackboxBusy = true;
              try {
                await api.blackboxRestore(snapshot.id);
                ctx.toast(t("settings.snapshot.restored"), "ok");
                await ctx.refresh();
                await loadBlackbox();
              } catch (err) {
                ctx.toast(err.message, "err");
              } finally {
                blackboxBusy = false;
              }
            },
          }, icon("refresh", "sm"), t("settings.snapshot.restore")) : null));
      }
    } catch (err) {
      clear(blackboxList);
      blackboxList.appendChild(h("div", { class: "settings-note" }, icon("alert", "sm"), h("div", null, err.message)));
    }
  };
  blackboxCreate.addEventListener("click", async () => {
    if (blackboxBusy) return;
    blackboxBusy = true;
    try {
      await api.blackboxSnapshot("manual");
      ctx.toast(t("settings.snapshot.created"), "ok");
      await loadBlackbox();
    } catch (err) {
      ctx.toast(err.message, "err");
    } finally {
      blackboxBusy = false;
    }
  });
  const blackboxPanel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.snapshot.title"))),
    blackboxNote,
    h("div", { class: "assistant-api-actions" }, blackboxCreate),
    blackboxFold.el);
  loadBlackbox();

  // ------------------------------------------------------------ palette panel
  // One editor serves the shared project palette and the person's own
  // override (Settings > Personal); only the save target differs.  A personal
  // editor can pick the project's saved themes but never writes the library.
  const palettePresets = ctx.S.palettePresets || {};
  const paletteFields = [
    ["accent", t("settings.palette.accent")],
    ["accent2", t("settings.palette.accent2")],
    ["background", t("settings.palette.background")],
    ["surface", t("settings.palette.surface")],
    ["text", t("settings.palette.text")],
    ["muted", t("settings.palette.muted")],
    ["line", t("settings.palette.line")],
    ["success", t("settings.palette.success")],
    ["warning", t("settings.palette.warning")],
    ["danger", t("settings.palette.danger")],
  ];
  const draftFromPalette = (value) => {
    const source = value || {};
    const out = { enabled: Boolean(source.enabled) };
    for (const mode of ["light", "dark"]) {
      const preset = mode === "light" ? "ark-light" : "ark";
      out[mode] = { ...(source[mode]
        || (paletteMode(source) === mode ? source : null)
        || { preset, ...palettePresets[preset] }) };
    }
    return out;
  };
  // Preview a light/dark palette on this page only; S.theme (your theme) stays.
  let themePreviewWatch = null;
  function previewThemeMode(mode) {
    document.documentElement.dataset.theme = mode;
    applyAppearance();
    if (themePreviewWatch) return;
    // In-app navigation uses pushState (no event), so check for the page
    // leaving the DOM instead.
    themePreviewWatch = setInterval(() => {
      if (page.isConnected) return;
      clearInterval(themePreviewWatch);
      themePreviewWatch = null;
      document.documentElement.dataset.theme = ctx.S.theme;
      ctx.applyAppearance ? ctx.applyAppearance() : applyAppearance();
      for (const graph of ctx.graphs || []) graph._draw?.();
    }, 400);
  }
  function buildPaletteEditor({ value, personal = false, onSave, title, pill, lead, enabledLabel, before = null }) {
    let paletteDraft = draftFromPalette(value);
    let locked = false;
    // Live preview of the draft; a locked (inherited) editor shows the effective look.
    const preview = () => (locked ? ctx.applyAppearance?.() : ctx.applyPalette(paletteDraft));
    let paletteEditMode = ctx.S.theme;
    const paletteEnabled = h("input", {
      type: "checkbox", checked: Boolean(paletteDraft.enabled),
      onchange: (event) => {
        paletteDraft.enabled = event.target.checked;
        preview();
      },
    });
    const palettePreset = h("select", { class: "input" });
    const paletteModeButtons = new Map();
    const modeTabs = h("div", { class: "segmented palette-mode-tabs", role: "group", "aria-label": t("profile.theme") });
    const paletteInputs = new Map();
    const paletteGrid = h("div", { class: "palette-grid" });
    for (const [key, label] of paletteFields) {
      const input = h("input", { type: "color", class: "palette-color", value: paletteDraft[paletteEditMode][key] || "#000000",
        "aria-label": label });
      input.addEventListener("input", () => {
        if (key === "background" && paletteMode({ background: input.value }) !== paletteEditMode) {
          input.value = paletteDraft[paletteEditMode].background;
          ctx.toast(t("settings.palette.mode_mismatch"), "err");
          return;
        }
        paletteDraft[paletteEditMode][key] = input.value;
        paletteDraft[paletteEditMode].preset = "custom";
        palettePreset.value = "custom";
        preview();
      });
      paletteInputs.set(key, input);
      paletteGrid.appendChild(h("label", { class: "palette-field" }, h("span", null, label), input));
    }
    const applyPresetDraft = (key) => {
      const preset = palettePresets[key];
      if (!preset) return;
      paletteDraft[paletteEditMode] = { preset: key, ...preset };
      paletteDraft.enabled = true;
      paletteEnabled.checked = true;
      for (const [field, input] of paletteInputs) input.value = paletteDraft[paletteEditMode][field] || "#000000";
      preview();
    };
    palettePreset.addEventListener("change", () => {
      if (palettePreset.value === "custom") {
        paletteDraft[paletteEditMode].preset = "custom";
      } else {
        applyPresetDraft(palettePreset.value);
      }
    });
    const syncPaletteMode = () => {
      palettePreset.replaceChildren();
      const keys = Object.keys(palettePresets).filter(key => paletteMode(palettePresets[key]) === paletteEditMode);
      for (const key of [...keys, "custom"]) palettePreset.appendChild(h("option", { value: key },
        key === "custom" ? t("settings.palette.custom") : key.replace(/-light$/, "")));
      palettePreset.value = paletteDraft[paletteEditMode].preset || "custom";
      for (const [field, input] of paletteInputs) input.value = paletteDraft[paletteEditMode][field] || "#000000";
      for (const [mode, button] of paletteModeButtons) {
        button.classList.toggle("active", mode === paletteEditMode);
        button.setAttribute("aria-pressed", String(mode === paletteEditMode));
      }
    };
    for (const mode of ["light", "dark"]) {
      // Picks which colour set you edit.  The page previews that set without
      // changing your own theme (restored when you leave Settings).
      const button = h("button", { type: "button", "data-palette-mode": mode, onclick: () => {
        paletteEditMode = mode;
        previewThemeMode(mode);
        syncPaletteMode();
        preview();
        for (const graph of ctx.graphs || []) graph._draw?.();
      } }, icon(mode === "dark" ? "moon" : "sun", "sm"), t(`settings.palette.edit_${mode}`));
      paletteModeButtons.set(mode, button);
      modeTabs.appendChild(button);
    }
    syncPaletteMode();
    let savedPalettes = { ...(ctx.S.project?.settings?.palette?.saved || {}) };
    const savedSelect = h("select", { class: "input", "aria-label": t("settings.palette.saved"), "data-palette-saved": "1" });
    const savedName = personal ? null : h("input", { class: "input", maxlength: 64, placeholder: t("settings.palette.name"),
      "aria-label": t("settings.palette.name"), "data-palette-name": "1" });
    const syncSaved = (selected = savedSelect.value) => {
      savedSelect.replaceChildren(h("option", { value: "" }, t("settings.palette.choose_saved")),
        ...Object.keys(savedPalettes).sort().map(name => h("option", { value: name }, name)));
      savedSelect.value = Object.hasOwn(savedPalettes, selected) ? selected : "";
      if (savedDelete) savedDelete.disabled = locked || !savedSelect.value;
    };
    savedSelect.addEventListener("change", () => {
      const name = savedSelect.value;
      if (savedDelete) savedDelete.disabled = !name;
      if (!Object.hasOwn(savedPalettes, name)) return;
      if (savedName) savedName.value = name;
      for (const mode of ["light", "dark"]) paletteDraft[mode] = { ...savedPalettes[name][mode] };
      paletteDraft.enabled = true;
      paletteEnabled.checked = true;
      syncPaletteMode();
      preview();
    });
    const saveNamed = personal ? null : h("button", { class: "icon-btn", type: "button", "data-palette-store": "1",
      title: t("settings.palette.store"), "aria-label": t("settings.palette.store"),
      onclick: async () => {
        const name = savedName.value.trim();
        if (!name) { savedName.focus(); return; }
        if (Object.hasOwn(savedPalettes, name) && !window.confirm(t("settings.palette.overwrite", { name }))) return;
        saveNamed.disabled = true;
        try {
          const pair = Object.fromEntries(["light", "dark"].map(mode => [mode,
            Object.fromEntries(["preset", ...paletteFields.map(([key]) => key), "accent_soft"]
              .filter(key => paletteDraft[mode][key] != null).map(key => [key, paletteDraft[mode][key]]))]));
          const data = await api.updateSettings({ palette: { ...paletteDraft, saved: { [name]: pair } } });
          savedPalettes = data.settings.palette.saved;
          ctx.applySettings(data);
          syncSaved(name);
          setDirty("palette", false);
          ctx.toast(t("settings.saved"), "ok");
        } catch (err) { ctx.toast(err.message, "err"); }
        finally { saveNamed.disabled = false; }
      } }, icon("save", "sm"));
    const savedDelete = personal ? null : h("button", { class: "icon-btn", type: "button", "data-palette-delete": "1", disabled: true,
      title: t("action.delete"), "aria-label": t("action.delete"), onclick: async () => {
        const name = savedSelect.value;
        if (!name || !window.confirm(t("settings.palette.delete_confirm", { name }))) return;
        savedDelete.disabled = true;
        try {
          const data = await api.updateSettings({ palette: { saved: { [name]: null } } });
          savedPalettes = data.settings.palette.saved;
          ctx.applySettings(data);
          preview();
          syncSaved();
          savedName.value = "";
        } catch (err) { ctx.toast(err.message, "err"); }
        finally { savedDelete.disabled = !savedSelect.value; }
      } }, icon("trash", "sm"));
    syncSaved();
    // Saved through the tab save bar; resolves false on failure.
    const save = async () => {
      try {
        await onSave(paletteDraft);
        return true;
      } catch (err) {
        ctx.toast(err.message, "err");
        return false;
      }
    };
    if (savedName) savedName.dataset.noDirty = "";
    const panel = h("section", { class: "panel settings-panel", "data-palette-editor": personal ? "personal" : "project" },
      h("div", { class: "panel-head" }, h("h3", null, title), pill),
      lead ? h("p", { class: "settings-lead" }, lead) : null,
      before,
      h("div", { class: "field" }, h("label", null, t("settings.palette.saved")),
        h("div", { class: "palette-library-row" }, savedSelect, savedDelete)),
      modeTabs,
      h("label", { class: "shortcut-row palette-enabled-row" },
        h("span", { class: "physics-name" }, enabledLabel), paletteEnabled),
      h("div", { class: "field" }, h("label", null, t("settings.palette.preset")), palettePreset),
      paletteGrid,
      personal ? null : h("div", { class: "palette-library-row" }, savedName, saveNamed));
    return {
      panel,
      save,
      draft: () => paletteDraft,
      /** Replace the draft (e.g. with the inherited project palette). */
      load(next) {
        paletteDraft = draftFromPalette(next);
        paletteEnabled.checked = Boolean(paletteDraft.enabled);
        syncSaved("");
        syncPaletteMode();
      },
      /** A locked editor shows inherited values read-only. */
      setLocked(next) {
        locked = Boolean(next);
        for (const control of [savedSelect, paletteEnabled, palettePreset, ...paletteInputs.values()]) {
          control.disabled = locked;
        }
        panel.classList.toggle("settings-inherited", locked);
      },
    };
  }
  const projectPaletteNote = h("div", { class: "settings-note hidden", "data-override-note": "palette" });
  const projectPaletteEditor = buildPaletteEditor({
    value: ctx.S.project?.settings?.palette,
    title: t("settings.palette.title"),
    pill: null,
    lead: "",
    enabledLabel: t("settings.palette.enabled"),
    before: projectPaletteNote,
    onSave: async (draft) => {
      const data = await api.updateSettings({ palette: draft });
      ctx.applySettings(data);
      ctx.toast(t("settings.saved"), "ok");
    },
  });
  const palettePanel = projectPaletteEditor.panel;
  registerSave("palette", { section: "appearance", label: t("settings.palette.title"),
    save: projectPaletteEditor.save, root: palettePanel,
    discard: () => {
      projectPaletteEditor.load(ctx.S.project?.settings?.palette);
      ctx.applyAppearance?.();
    } });

  const aboutLink = (label, path, glyph = "external") => h("a", {
    class: "ghost-btn", href: `https://github.com/MasterYip${path}`,
    target: "_blank", rel: "noopener noreferrer",
  }, icon(glyph, "sm"), label);
  const build = document.querySelector('meta[name="ark-build"]')?.content;
  const aboutBrand = ["light", "dark"].map(theme => h("img", {
    class: `ark-lockup ark-lockup-${theme}`, alt: "", width: 227.1584, height: 49,
    src: `./brand/ark-lockup-loading${theme === "dark" ? "-dark" : ""}.svg`,
  }));
  const aboutPanel = h("section", { class: "settings-about", "data-about": "" },
    h("div", { class: "settings-about-brand", role: "img", "aria-label": "ARK Studio" },
      ...aboutBrand),
    h("p", { class: "settings-lead" }, t("about.description")),
    h("dl", { class: "settings-about-details" },
      h("dt", null, t("about.author")),
      h("dd", null, aboutLink("MasterYip", "", "user")),
      h("dt", null, t("about.license")),
      h("dd", null, aboutLink("AGPL-3.0", "/ARK-Studio/blob/main/LICENSE", "book")),
      build ? [h("dt", null, t("about.build")), h("dd", null, h("code", null, build))] : null),
    aboutUpdateBlock(),
    h("div", { class: "settings-about-links" },
      aboutLink(t("about.repository"), "/ARK-Studio", "git"),
      aboutLink(t("about.latest_release"), "/ARK-Studio/releases/latest", "download"),
      aboutLink(t("about.documentation"), "/ARK-Studio/blob/main/docs/README.md", "book"),
      aboutLink(t("about.issues"), "/ARK-Studio/issues", "message"),
      aboutLink(t("about.attribution"), "/ARK-Studio/blob/main/docs/agents/skills-attribution.md", "info")));

  // Stored agent session transcripts are opt-in per download (docs/guide/project-export.md).
  const exportLink = h("a", { class: "btn primary", href: exportUrl("project", project.id), download: "",
    "data-download-project": "" }, icon("download", "sm"), t("settings.export.download"));
  const exportSessions = h("input", {
    type: "checkbox", "data-export-sessions": "",
    onchange: (event) => {
      const base = exportUrl("project", project.id);
      exportLink.href = event.target.checked ? `${base}&sessions=1` : base;
    },
  });
  const exportPanel = h("section", { class: "panel settings-panel", "data-project-export": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.export.title"))),
    h("p", { class: "settings-lead" }, t("settings.export.contents")),
    h("label", { class: "row tight settings-export-sessions" }, exportSessions,
      h("span", null, t("settings.export.sessions")),
      h("span", { class: "muted" }, t("settings.export.sessions_hint"))),
    h("div", { class: "settings-actions" }, exportLink));

  // ------------------------------------------- personal appearance overrides
  // Settings > Personal reuses the palette editor and physics rows above.
  // Resolution is project -> person per group: a saved override wins for this
  // person in every project; "Use project setting" clears it again.  These
  // controls only ever write the account profile, never project.json.
  const projectAppearance = () => ctx.S.project?.settings || {};
  const savedOverride = (group) => appearanceOverrides()[group] || null;
  const overrideModes = {
    palette: savedOverride("palette") ? "personal" : "project",
    graph: savedOverride("graph") ? "personal" : "project",
    arrows: savedOverride("arrows") ? "personal" : "project",
  };
  function overrideBar(group) {
    const status = h("span", { class: "pill", "data-override-status": group });
    const choice = (mode, label) => h("button", {
      class: "seg-btn", type: "button", "data-override-choice": mode,
      onclick: () => (mode === "project" ? useProjectValue(group) : startOverride(group)),
    }, label);
    const projectBtn = choice("project", t("settings.override.use_project"));
    const mineBtn = choice("personal", t("settings.override.override_mine"));
    const reset = h("button", {
      class: "ghost-btn sm", type: "button", "data-override-reset": group,
      title: t("settings.override.reset_hint"),
      onclick: () => useProjectValue(group),
    }, icon("refresh", "sm"), t("settings.override.reset"));
    const el = h("div", { class: "settings-override-bar", "data-override-group": group },
      h("div", { class: "segmented", role: "group", "aria-label": t("settings.override.choice_label") },
        projectBtn, mineBtn),
      reset);
    const sync = () => {
      const mode = overrideModes[group];
      const saved = Boolean(savedOverride(group));
      for (const [button, active] of [[projectBtn, mode === "project"], [mineBtn, mode === "personal"]]) {
        button.classList.toggle("on", active);
        button.setAttribute("aria-pressed", String(active));
      }
      status.className = saved ? "pill ok" : mode === "personal" ? "pill warn" : "pill";
      status.textContent = t(saved ? "settings.override.status_personal"
        : mode === "personal" ? "settings.override.status_unsaved" : "settings.override.status_project");
      reset.hidden = !saved;
    };
    return { el, status, sync };
  }

  const paletteBar = overrideBar("palette");
  const personalPaletteEditor = buildPaletteEditor({
    value: savedOverride("palette") || projectAppearance().palette,
    personal: true,
    title: t("settings.override.palette_title"),
    pill: paletteBar.status,
    lead: t("settings.override.palette_lead"),
    enabledLabel: t("settings.override.palette_enabled"),
    before: paletteBar.el,
    onSave: async (value) => {
      await saveAppearanceOverride({ palette: { enabled: value.enabled, light: value.light, dark: value.dark } });
      ctx.toast(t("settings.override.saved"), "ok");
    },
  });

  const graphBar = overrideBar("graph");
  let personalGraphDraft = { ...normalizeGraphPhysics(savedOverride("graph") || projectAppearance().graph) };
  const personalGraphRows = [];
  const savePersonalGraph = async () => {
    try {
      await saveAppearanceOverride({ graph: { ...personalGraphDraft } });
      ctx.toast(t("settings.override.saved"), "ok");
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  };
  const personalGraphPanel = h("section", { class: "panel settings-panel", "data-graph-override": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.override.graph_title")), graphBar.status),
    h("p", { class: "settings-lead" }, t("settings.override.graph_lead")),
    graphBar.el,
    ...physicsRows({ getDraft: () => personalGraphDraft, onChange: () => {}, list: personalGraphRows }));

  const arrowsBar = overrideBar("arrows");
  let personalArrowsDraft = cloneArrows(savedOverride("arrows") || projectAppearance().arrows);
  const personalArrows = arrowControls({ getDraft: () => personalArrowsDraft, onChange: () => {} });
  const savePersonalArrows = async () => {
    try {
      await saveAppearanceOverride({ arrows: cloneArrows(personalArrowsDraft) });
      ctx.toast(t("settings.override.saved"), "ok");
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  };
  const personalArrowsPanel = h("section", { class: "panel settings-panel", "data-arrows-override": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.override.arrows_title")), arrowsBar.status),
    h("p", { class: "settings-lead" }, t("settings.override.arrows_lead")),
    arrowsBar.el,
    ...personalArrows.elements);

  const overrideEditors = {
    arrows: {
      load: (value) => {
        personalArrowsDraft = cloneArrows(value);
        personalArrows.sync();
      },
      lock: (locked) => {
        for (const input of personalArrows.inputs) input.disabled = locked;
        personalArrowsPanel.classList.toggle("settings-inherited", locked);
      },
      project: () => projectAppearance().arrows,
      bar: arrowsBar,
    },
    palette: {
      load: (value) => personalPaletteEditor.load(value),
      lock: (locked) => personalPaletteEditor.setLocked(locked),
      project: () => projectAppearance().palette,
      bar: paletteBar,
    },
    graph: {
      load: (value) => {
        personalGraphDraft = { ...normalizeGraphPhysics(value) };
        syncing.active = true;
        for (const row of personalGraphRows) row.sync();
        syncing.active = false;
      },
      lock: (locked) => {
        for (const row of personalGraphRows) for (const input of row.inputs) input.disabled = locked;
        personalGraphPanel.classList.toggle("settings-inherited", locked);
      },
      project: () => projectAppearance().graph,
      bar: graphBar,
    },
  };
  const overrideSourceId = (group) => `override-${group}`;

  // Graph display overrides are per setting (graph-display.js): a changed
  // control becomes this person's value, the others keep following the
  // project.  Each row and each tab can go back to the project default.
  const savedDisplayOverride = () => normalizeGraphDisplay(appearanceOverrides().graph_display);
  let personalDisplayDraft = savedDisplayOverride();
  const personalDisplay = () => resolveGraphDisplay(projectAppearance().graph_display, personalDisplayDraft);
  const personalDisplayRows = {};
  const personalDisplayPanels = {};
  const displaySourceId = overrideSourceId("graph_display");
  const syncPersonalDisplay = () => {
    syncing.active = true;
    for (const rows of Object.values(personalDisplayRows)) rows.sync();
    syncing.active = false;
  };
  const sameAsSaved = () => JSON.stringify(personalDisplayDraft) === JSON.stringify(savedDisplayOverride());
  const setPersonalDisplay = (tab, key, value) => {
    personalDisplayDraft = setGraphDisplayValue(personalDisplayDraft, tab, key, value);
    personalDisplayRows[tab].sync();
    setDirty(displaySourceId, !sameAsSaved());
  };
  async function resetPersonalDisplayTab(tab) {
    const saved = savedDisplayOverride();
    personalDisplayDraft = clearGraphDisplayTab(personalDisplayDraft, tab);
    if (saved[tab]) {
      try {
        await saveAppearanceOverride({ graph_display: clearGraphDisplayTab(saved, tab) });
        ctx.toast(t("settings.override.cleared"), "ok");
      } catch (err) { ctx.toast(err.message, "err"); return; }
    }
    syncPersonalDisplay();
    setDirty(displaySourceId, !sameAsSaved());
  }
  for (const tab of GRAPH_SETTING_TABS) {
    personalDisplayRows[tab] = graphDisplayRows({
      tab,
      value: (key) => personalDisplay().values[tab][key],
      source: (key) => personalDisplay().source[tab][key],
      onChange: (key, value) => setPersonalDisplay(tab, key, value),
      onReset: (key) => setPersonalDisplay(tab, key, null),
    });
    const resetTab = h("button", {
      class: "ghost-btn sm", type: "button", "data-graph-tab-reset": tab, title: t("settings.graphs.reset_tab_hint"),
      onclick: () => resetPersonalDisplayTab(tab),
    }, icon("refresh", "sm"), t("settings.override.reset"));
    personalDisplayPanels[tab] = graphDisplayPanel({ tab, rows: personalDisplayRows[tab], actions: resetTab });
  }
  async function savePersonalDisplay() {
    try {
      await saveAppearanceOverride({ graph_display: Object.keys(personalDisplayDraft).length ? personalDisplayDraft : null });
      personalDisplayDraft = savedDisplayOverride();
      syncPersonalDisplay();
      ctx.toast(t("settings.override.saved"), "ok");
      return true;
    } catch (err) { ctx.toast(err.message, "err"); return false; }
  }
  registerSave(displaySourceId, { section: "graphOverride", label: t("settings.graphs.my_title"),
    save: savePersonalDisplay,
    discard: () => { personalDisplayDraft = savedDisplayOverride(); syncPersonalDisplay(); } });
  syncPersonalDisplay();
  function startOverride(group) {
    overrideModes[group] = "personal";
    overrideEditors[group].lock(false);
    overrideEditors[group].bar.sync();
    // Choosing "Override for me" is itself an unsaved change.
    if (!savedOverride(group)) setDirty(overrideSourceId(group));
  }
  async function useProjectValue(group) {
    if (savedOverride(group)) {
      try {
        await saveAppearanceOverride({ [group]: null });
        ctx.toast(t("settings.override.cleared"), "ok");
      } catch (err) { ctx.toast(err.message, "err"); return; }
    }
    overrideModes[group] = "project";
    const editor = overrideEditors[group];
    editor.load(editor.project());
    editor.lock(true);
    editor.bar.sync();
    setDirty(overrideSourceId(group), false);
    ctx.applyAppearance?.();
  }
  // Discarding returns to the saved override, or to the inherited value.
  function discardOverride(group) {
    const saved = savedOverride(group);
    overrideModes[group] = saved ? "personal" : "project";
    const editor = overrideEditors[group];
    editor.load(saved || editor.project());
    editor.lock(!saved);
    editor.bar.sync();
    ctx.applyAppearance?.();
  }
  for (const [group, label, save, root, section] of [
    ["palette", t("settings.override.palette_title"), personalPaletteEditor.save, personalPaletteEditor.panel, "appearanceOverride"],
    ["graph", t("settings.override.graph_title"), savePersonalGraph, personalGraphPanel, "graphOverride"],
    ["arrows", t("settings.override.arrows_title"), savePersonalArrows, personalArrowsPanel, "graphOverride"],
  ]) {
    registerSave(overrideSourceId(group), { section, label, save, root,
      discard: () => discardOverride(group) });
  }
  overrideEditors.graph.load(personalGraphDraft);
  for (const [group, editor] of Object.entries(overrideEditors)) {
    editor.lock(overrideModes[group] !== "personal");
    editor.bar.sync();
  }

  // The project editors say when this person's own view is overridden.
  const openPersonalOverride = (group) => {
    selectScope("personal");
    selectTab("display", group === "palette" ? "appearanceOverride" : "graphOverride");
    if (group === "graph") personalGraphTabs.select("force");
    else if (group === "arrows") personalGraphTabs.select("general");
  };
  const syncProjectOverrideNotes = () => {
    for (const [group, note, key] of [["palette", projectPaletteNote, "settings.override.project_note_palette"],
      ["graph", projectPhysicsNote, "settings.override.project_note_graph"],
      ["arrows", projectArrowsNote, "settings.override.project_note_arrows"],
      ["graph_display", projectDisplayNote, "settings.override.project_note_graph_display"]]) {
      const active = Boolean(savedOverride(group));
      note.classList.toggle("hidden", !active);
      clear(note);
      if (active) {
        note.append(icon("user", "sm"), h("div", null, t(key), " ",
          h("button", { class: "btn sm ghost", type: "button", "data-open-override": group, onclick: () => openPersonalOverride(group) },
            t("settings.override.open_mine"))));
      }
    }
  };
  syncProjectOverrideNotes();
  const onOverridesChanged = () => {
    if (!page.isConnected) { window.removeEventListener(APPEARANCE_CHANGED, onOverridesChanged); return; }
    syncProjectOverrideNotes();
    for (const editor of Object.values(overrideEditors)) editor.bar.sync();
    if (!saveSources.get(displaySourceId)?.dirty) personalDisplayDraft = savedDisplayOverride();
    syncPersonalDisplay();
    preview?.setPhysics(draft);
  };
  window.addEventListener(APPEARANCE_CHANGED, onOverridesChanged);
  const appearanceOverridePanels = [personalPaletteEditor.panel];
  // Personal › Appearance & input › My graphs: the same tabs as the project's.
  const personalGraphPanes = {
    general: h("div", { class: "stack" }, personalArrowsPanel, personalDisplayPanels.general),
    force: h("div", { class: "stack" }, personalGraphPanel, personalDisplayPanels.force),
    mind: h("div", { class: "stack" }, personalDisplayPanels.mind),
    local: h("div", { class: "stack" }, personalDisplayPanels.local),
  };
  const personalGraphTabs = graphTabStrip({ panes: personalGraphPanes, idPrefix: "my-graphs" });
  const graphOverridePanels = [
    h("section", { class: "panel settings-panel settings-graphs-head", "data-graphs": "personal" },
      h("div", { class: "panel-head" }, h("h3", null, t("settings.graphs.my_title"))),
      h("p", { class: "settings-lead" }, t("settings.graphs.my_lead")),
      personalGraphTabs.el),
    ...Object.values(personalGraphPanes),
  ];

  const guidancePanel = h("section", { class: "panel settings-panel", "data-guidance-settings": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("tour.title"))),
    h("p", { class: "settings-lead" }, t("tour.loop.settings_hint")),
    h("div", { class: "head-actions" },
      h("button", { class: "btn primary", type: "button", "data-restart-guidance": "",
        onclick: () => ctx.guidance?.start({ restart: true }) }, icon("refresh", "sm"), t("tour.restart")),
      h("button", { class: "btn", type: "button", "data-resume-guidance": "",
        onclick: () => ctx.guidance?.start() }, icon("play", "sm"), t("tour.resume"))));
  // Project › Appearance › Graphs: the tab strip, the tab's panels and the
  // live preview beside them.
  const projectGraphPanes = {
    general: h("div", { class: "stack" }, arrowsPanel, projectDisplayPanels.general),
    force: h("div", { class: "stack" }, physicsPanel, projectDisplayPanels.force),
    mind: h("div", { class: "stack" }, projectDisplayPanels.mind),
    local: h("div", { class: "stack" }, projectDisplayPanels.local),
  };
  const projectGraphTabs = graphTabStrip({ panes: projectGraphPanes, idPrefix: "project-graphs" });
  const graphLayout = h("div", { class: "stack settings-graphs" },
    h("section", { class: "panel settings-panel settings-graphs-head", "data-graphs": "project" },
      h("div", { class: "panel-head" }, h("h3", null, t("settings.graphs.title"))),
      h("p", { class: "settings-lead" }, t("settings.graphs.lead")),
      projectDisplayNote,
      projectGraphTabs.el),
    h("div", { class: "settings-graph-layout" },
      h("div", { class: "stack settings-graph-controls" }, ...Object.values(projectGraphPanes)),
      h("div", { class: "stack settings-graph-preview" }, previewPanel)));
  // Open a graph tab from elsewhere (the old location's pointer).
  function openGraphs(scope, tab) {
    selectScope(scope);
    if (scope === "project") {
      selectTab("appearance", GRAPH_SECTION);
      projectGraphTabs.select(tab);
    } else {
      selectTab("display", "graphOverride");
      personalGraphTabs.select(tab);
    }
  }
  // Section ids are the tab ids used before the regrouping; settings-tabs.js
  // maps them onto the tabs below so old deep links still land on the panel.
  // The profile card keeps its own save logic; Settings drives it from the bar.
  const profilePanels = renderProfilePanels(ctx);
  const profileCard = profilePanels.find((panel) => panel.arkSave);
  if (profileCard) {
    profileCard.querySelector("[data-profile-save]")?.remove();
    registerSave("profile", { section: "profile", label: t("profile.title"),
      save: () => profileCard.arkSave(), discard: () => profileCard.arkDiscard(), root: profileCard });
    profileCard.querySelector(".profile-avatar-controls")?.addEventListener("click", (event) => {
      if (event.target.closest(".profile-swatches button, button")) setDirty("profile");
    });
  }
  // ------------------------------------------------------------ advanced switch
  const advancedBox = h("input", { type: "checkbox", checked: advancedEnabled(),
    "data-advanced-toggle": "1", onchange: () => setAdvancedEnabled(advancedBox.checked) });
  const advancedPanel = h("section", { class: "panel settings-panel", "data-advanced-settings": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.advanced.title")), browserBadge()),
    h("p", { class: "settings-lead" }, t("settings.advanced.lead")),
    h("label", { class: "display-toggle" }, advancedBox, h("span", null, t("settings.advanced.toggle"))),
    // Experimental (T-0044): Message agent on Home and in the node menu; off by default.
    h("label", { class: "display-toggle" },
      h("input", { type: "checkbox", checked: agentMessagesEnabled(), "data-agent-messages-toggle": "1",
        onchange: (event) => setAgentMessagesEnabled(event.target.checked) }),
      h("span", null, t("settings.advanced.messages"))),
    h("p", { class: "muted small" }, t("settings.advanced.messages_hint")));

  // ------------------------------------------------------------ overview
  // First screen: where each kind of setting lives, and shortcuts to the
  // few that people look for first.  Each shortcut opens its own section.
  const jump = (id) => {
    const resolved = resolveSettingsTab(id);
    if (!resolved) return;
    selectScope(tabs.find((tab) => tab.id === resolved.tab)?.scope || "personal", "");
    selectTab(resolved.tab, resolved.section || id);
  };
  const themeName = () => t(`profile.theme_${themePref()}`);
  const overviewShortcut = (id, iconName, label, value) => h("button", {
    class: "settings-overview-link", type: "button", "data-settings-jump": id, onclick: () => jump(id),
  }, icon(iconName, "sm"), h("span", null, h("b", null, label), value ? h("small", null, value) : null), icon("chevronRight", "sm"));
  const scopeCard = (iconName, title, body, example) => h("div", { class: "settings-overview-scope" },
    h("div", { class: "settings-overview-scope-head" }, icon(iconName, "sm"), h("b", null, title)),
    h("p", null, body), h("small", null, example));
  const overviewPanel = h("section", { class: "panel settings-panel settings-overview", "data-settings-overview": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.overview.title"))),
    h("p", { class: "settings-lead" }, t("settings.overview.lead")),
    h("p", { class: "settings-lead settings-overview-where" },
      t(desktopApp ? "settings.overview.where_app" : "settings.overview.where_browser")),
    desktopApp ? appOverviewCard(() => jump("app")) : null,
    aboutOverviewCard(() => jump("about")),
    h("div", { class: "settings-overview-scopes" },
      scopeCard("folder", t("settings.scope_project"), t("settings.overview.project"), t("settings.overview.project_eg")),
      scopeCard("user", t("settings.scope_personal"), t("settings.overview.personal"), t("settings.overview.personal_eg")),
      scopeCard("monitor", t("settings.browser_scope"), t("settings.overview.browser"), t("settings.overview.browser_eg"))),
    h("div", { class: "settings-sub" }, t("settings.overview.shortcuts")),
    h("div", { class: "settings-overview-links" },
      overviewShortcut("agentAccess", "key", t("settings.overview.agent_access"), t("settings.overview.agent_access_hint")),
      overviewShortcut("display", "globe", t("settings.overview.language_theme"),
        `${getLang() === "zh" ? "中文" : "English"} · ${themeName()}`), // i18n-ignore: language names shown in their own language
      overviewShortcut("graph", "graph", t("settings.graphs.title"), t("settings.overview.graphs_hint")),
      overviewShortcut("advanced", "layers", t("settings.advanced.toggle"),
        t(advancedEnabled() ? "settings.overview.on" : "settings.overview.off")),
      overviewShortcut("plugins", "puzzle", t("plugins.title"), t("plugins.hint")),
      overviewShortcut("trash", "trash", t("trash.title"), t("trash.hint")),
      isOrgAdmin ? overviewShortcut("storage", "folder", t("storage.title"), t("settings.tab.storage_hint")) : null,
      overviewShortcut("guidance", "play", t("tour.title"), t("settings.overview.guide_hint"))));

  const sectionPanels = {
    appearance: [palettePanel],
    graph: [graphLayout],
    markers: [markerSettingsPanel(ctx, { registerSave, setDirty,
      readOnly: !ctx.S.session?.local && ctx.S.session?.project_permission !== "admin" })],
    projectAgents: [projectAgentsPanel],
    agentRules: [agentRulesPanel(ctx, "project")],
    projectAssistant: [projectAssistantPanel],
    providers: [providersPanel, worktreesPanel],
    skills: [skillsPanel],
    setupResources: setupResourcesPanels(ctx),
    gpu: [gpuPanel],
    remote: [remotePanel],
    mobile: [mobilePanel],
    sessionUploads: [sessionUploadsPanel],
    data: [exportPanel, infoPanel],
    blackbox: [blackboxPanel],
    trash: [trashPanel(ctx)],
    deleteProject: !demoProject && ctx.S.session?.project_permission === "admin" ? [deleteProjectPanel] : [],
    profile: profilePanels,
    account: [accountPanel, cachePanel()],
    environment: [linuxPanel],
    display: [appearancePanel(), ...(ctx.S.project?.id ? [homeFilterPanel(ctx)] : [])],
    advanced: [advancedPanel],
    overview: [overviewPanel],
    infohubSource: [hubPanel],
    appearanceOverride: appearanceOverridePanels,
    graphOverride: graphOverridePanels,
    shortcuts: [shortcutPanel, shortcutGuidePanel, tagShortcutPanel],
    guidance: [guidancePanel],
    assistant: [assistantPanel],
    personalAgents: [personalAgentsPanel],
    personalAgentRules: [agentRulesPanel(ctx, "personal")],
    agentAccess: [agentTokensPanel(ctx)],
    plugins: [pluginsPanel(ctx)],
    about: [aboutPanel],
    app: desktopApp ? [appSettingsPanel()] : [],
    projectStorage: isOrgAdmin ? [projectStoragePanel(ctx)] : [],
    admin: isOrgAdmin ? [adminPanel, ...(ctx.S.session?.local ? [] : [renderInviteSignupSetting(ctx)]), legacyServerTokenPanel()] : [],
    // Invite links need accounts: project admins on a server with sign-in.
    members: canManageInvites(ctx.S.session, ctx.S.project) ? [renderMembersPanel(ctx)] : [],
  };
  const tabMeta = {
    overview: { label: t("settings.tab.overview"), icon: "info", hint: t("settings.tab.overview_hint") },
    browser: { label: t("settings.browser_scope"), icon: "monitor", hint: t("settings.tab.browser_hint") },
    appearance: { label: t("settings.tab.appearance"), icon: "palette", hint: t("settings.tab.appearance_hint") },
    agents: { label: t("settings.tab.agents"), icon: "cpu", hint: t("settings.tab.agents_hint") },
    resources: { label: t("settings.tab.resources"), icon: "settings", hint: t("settings.tab.resources_hint") },
    access: { label: t("settings.tab.access"), icon: "smartphone", hint: t("settings.tab.access_hint") },
    data: { label: t("settings.tab.data"), icon: "database", hint: t("settings.tab.data_group_hint") },
    trash: { label: t("trash.title"), icon: "trash", hint: t("trash.hint") },
    members: { label: t("settings.tab.members"), icon: "user", hint: t("settings.tab.members_hint") },
    profile: { label: t("settings.tab.profile"), icon: "user", hint: t("settings.tab.profile_hint") },
    display: { label: t("settings.tab.display"), icon: "sun", hint: t("settings.tab.display_hint") },
    personalAI: { label: t("settings.tab.personal_ai"), icon: "sparkles", hint: t("settings.tab.personal_ai_hint") },
    plugins: { label: t("plugins.title"), icon: "puzzle", hint: t("plugins.hint") },
    about: { label: t("about.title"), icon: "info", hint: t("about.hint") },
    app: { label: t("app.title"), icon: "monitor", hint: t("app.hint") },
    storage: { label: t("settings.tab.storage"), icon: "folder", hint: t("settings.tab.storage_hint") },
    admin: { label: t("settings.scope_admin"), icon: "shield", hint: t("settings.scope_admin_hint") },
  };
  const tabs = SETTINGS_TABS
    .map((tab) => ({ ...tab, ...tabMeta[tab.id], sections: tab.sections.filter((id) => sectionPanels[id]?.length) }))
    .filter((tab) => tab.sections.length);
  const graphTab = tabs.find((tab) => tab.sections.includes(GRAPH_SECTION))?.id;
  // Top-level scope: shared project settings vs this person's own settings vs
  // organization administration (visible to owner/admin only).
  const scopes = isOrgAdmin ? ["overview", "project", "personal", "admin"]
    : ["overview", "project", "personal"];
  const scopeLabels = {
    overview: t("settings.tab.overview"),
    project: t("settings.scope_project"),
    personal: t("settings.scope_personal"),
    admin: t("settings.scope_admin"),
  };
  const scopeNotes = {
    overview: t("settings.tab.overview_hint"),
    project: t("settings.scope_project_hint"),
    personal: t("settings.scope_personal_hint"),
    admin: t("settings.scope_admin_hint"),
  };
  let activeScope = "overview";
  let requestedScope = "";
  try {
    const savedScope = localStorage.getItem("ark.settingsScope");
    if (savedScope && scopes.includes(savedScope)) activeScope = savedScope;
  } catch { /* storage unavailable */ }
  try {
    requestedScope = new URLSearchParams(location.search).get("scope")
      || new URLSearchParams(String(location.hash).split("?")[1] || "").get("scope")
      || "";
    if (requestedScope && scopes.includes(requestedScope)) activeScope = requestedScope;
  } catch { /* malformed hash */ }
  const scopeBar = h("div", { class: "settings-scope", role: "tablist",
    "aria-label": t("settings.scope_aria") });
  const scopeButtons = new Map();
  for (const scope of scopes) {
    const button = h("button", {
      class: "settings-scope-btn", type: "button", role: "tab",
      "data-settings-scope": scope,
      onclick: () => selectScope(scope),
      title: scopeNotes[scope] || "",
    }, icon(scope === "project" ? "folder" : scope === "admin" ? "shield" : scope === "overview" ? "info" : "user", "sm"),
      h("span", null, scopeLabels[scope]));
    scopeButtons.set(scope, button);
    scopeBar.appendChild(button);
  }
  const scopeNote = h("span", { class: "settings-scope-note" });
  scopeBar.appendChild(scopeNote);
  if (!ctx.S.session?.local && ctx.S.session?.project_permission !== "admin") {
    scopeNote.append(h("span", { class: "pill warn" }, t("settings.project_admin_required")));
  }
  const shell = h("div", { class: "settings-shell" });
  const nav = h("aside", { class: "settings-nav", "aria-label": t("settings.sections_aria") });
  const content = h("div", { class: "settings-content" });
  const tabButtons = new Map();
  // Old section ids (?settings=mobile, a remembered "gpu") open the tab that
  // now holds the section and scroll that section into view.
  let activeTab = DEFAULT_SETTINGS_TAB;
  let revealId = "";
  const pickTab = (id) => {
    const resolved = resolveSettingsTab(id);
    if (!resolved || !tabs.some((tab) => tab.id === resolved.tab)) return false;
    activeTab = resolved.tab;
    revealId = resolved.section;
    return true;
  };
  try { pickTab(localStorage.getItem("ark.settingsTab")); } catch { /* ignore */ }
  let requestedTab = false;
  try {
    const requested = new URLSearchParams(location.search).get("settings")
      || new URLSearchParams(String(location.hash).split("?")[1] || "").get("settings");
    if (requested && !pickTab(requested)) { activeTab = DEFAULT_SETTINGS_TAB; revealId = ""; }
    requestedTab = Boolean(requested);
  } catch { /* ignore */ }
  const activeTabDef = tabs.find((tab) => tab.id === activeTab);
  if (activeTabDef) activeScope = activeTabDef.scope;
  // A scope deep-link must win over the last tab remembered from another
  // scope, not over a tab the same link names (?scope=personal&settings=about
  // from before About moved to the top level still opens About).
  if (requestedScope && scopes.includes(requestedScope) && !requestedTab
      && activeTabDef?.scope !== requestedScope) {
    activeScope = requestedScope;
    const firstForScope = tabs.find((tab) => tab.scope === requestedScope);
    if (firstForScope) activeTab = firstForScope.id;
    revealId = "";
  }
  const panes = new Map();
  const sectionEls = new Map();
  const navGroups = [];
  for (const tab of tabs) {
    // Advanced tabs sit under one heading per scope, after the everyday ones.
    if (tab.advanced && !navGroups.some((group) => group.dataset.settingsScope === tab.scope)) {
      const group = h("div", { class: "settings-nav-group", "data-settings-scope": tab.scope }, t("settings.advanced_group"));
      navGroups.push(group);
      nav.appendChild(group);
    }
    const button = h("button", {
      class: "settings-nav-item", type: "button", "data-settings-tab": tab.id,
      "data-settings-scope": tab.scope,
      onclick: () => selectTab(tab.id),
    },
      icon(tab.icon, "sm"),
      h("span", { class: "settings-nav-copy" },
        h("b", null, tab.label),
        h("small", null, tab.hint)));
    tabButtons.set(tab.id, button);
    nav.appendChild(button);
    const paneBody = h("div", { class: "stack" }, tab.sections.map((id) => {
      const section = h("div", {
        class: id === GRAPH_SECTION ? "settings-section" : "stack settings-section",
        "data-settings-section": id,
      }, ...sectionPanels[id]);
      sectionEls.set(id, section);
      return section;
    }));
    const pane = h("div", { class: "settings-tab-pane" }, paneBody);
    panes.set(tab.id, pane);
    content.appendChild(pane);
  }
  // Panels above the target load asynchronously and grow; keep the target in
  // place until they settle or the user starts scrolling on their own.
  let stopReveal = null;
  function revealSection(id) {
    stopReveal?.();
    const section = sectionEls.get(id);
    if (!section) return;
    const go = () => {
      if (section.isConnected && !section.closest(".hidden")) section.scrollIntoView({ block: "start", behavior: "instant" });
    };
    requestAnimationFrame(go);
    if (typeof ResizeObserver !== "function") return;
    const observer = new ResizeObserver(() => requestAnimationFrame(go));
    const events = ["wheel", "touchstart", "keydown", "pointerdown"];
    const stop = () => {
      observer.disconnect();
      clearTimeout(timer);
      for (const name of events) window.removeEventListener(name, stop, true);
      if (stopReveal === stop) stopReveal = null;
    };
    const timer = setTimeout(stop, 2500);
    for (const name of events) window.addEventListener(name, stop, { capture: true, passive: true });
    observer.observe(content);
    stopReveal = stop;
  }
  function selectTab(id, reveal = "") {
    stopReveal?.();
    activeTab = tabs.some((tab) => tab.id === id) ? id
      : (tabs.find((tab) => tab.id === DEFAULT_SETTINGS_TAB) || tabs[0]).id;
    syncSaveBar();
    for (const [key, button] of tabButtons) {
      const active = key === activeTab;
      button.classList.toggle("active", active);
      button.setAttribute("aria-current", active ? "page" : undefined);
      if (active) {
        // Deep links (?settings=mobile) and the horizontal phone nav should
        // bring the active section into view without scrolling the page.
        requestAnimationFrame(() => {
          const navEl = button.parentElement;
          if (navEl && navEl.scrollWidth > navEl.clientWidth) {
            navEl.scrollTo({
              left: button.offsetLeft - navEl.clientWidth / 2 + button.clientWidth / 2,
              behavior: "auto",
            });
          }
        });
      }
    }
    for (const [key, pane] of panes) pane.classList.toggle("hidden", key !== activeTab);
    try { localStorage.setItem("ark.settingsTab", activeTab); } catch { /* ignore */ }
    if (activeTab === tabOfSection("mobile")) maybeAutoPair();
    if (activeTab === graphTab) requestAnimationFrame(() => preview?._resize?.());
    if (reveal && panes.get(activeTab)?.contains(sectionEls.get(reveal))) revealSection(reveal);
  }
  function selectScope(scope, reveal = "") {
    activeScope = scopes.includes(scope) ? scope : "overview";
    pageEyebrow.textContent = `${t("nav.settings")} · ${scopeLabels[activeScope]}`;
    for (const [key, button] of scopeButtons) {
      const active = key === activeScope;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", active ? "true" : "false");
    }
    for (const [key, button] of tabButtons) {
      button.classList.toggle("hidden", tabs.find((tab) => tab.id === key)?.scope !== activeScope);
    }
    for (const group of navGroups) group.classList.toggle("hidden", group.dataset.settingsScope !== activeScope);
    clear(scopeNote);
    if (activeScope === "project"
        && !ctx.S.session?.local
        && ctx.S.session?.project_permission !== "admin") {
      scopeNote.append(h("span", { class: "pill warn" },
        t("settings.project_admin_required")));
    }
    const inScope = tabs.filter((tab) => tab.scope === activeScope);
    if (!inScope.some((tab) => tab.id === activeTab)) selectTab(inScope[0].id);
    else selectTab(activeTab, reveal);
    try { localStorage.setItem("ark.settingsScope", activeScope); } catch { /* ignore */ }
  }
  content.prepend(saveBar);
  shell.append(nav, content);
  page.appendChild(scopeBar);
  page.appendChild(shell);
  selectScope(activeScope, revealId);
  host.appendChild(page);
  syncAll();
  loadEffective();
  // A change from elsewhere repaints this page only when the data it was
  // built from changed and nothing is unsaved (settings-sync-model.js).
  const shownKey = settingsViewKey(ctx.S);
  ctx.settingsPage = {
    mounted: () => page.isConnected,
    shownKey,
    dirty: () => [...saveSources.values()].some((source) => source.dirty),
  };

  // ------------------------------------------------------------ preview graph
  async function reloadPreviewData() {
    try {
      previewData = await api.graph({ limit: 40, context: 0 });
      if (!previewHost.isConnected) return;
      if (preview) { preview.destroy(); preview = null; }
      clear(previewHost);
      if (!previewData.nodes.length) {
        previewHost.appendChild(h("div", { class: "empty sm" }, t("map.empty")));
        return;
      }
      preview = new GraphView(previewHost, {
        physics: draft,
        arrows: arrowDraft,
        persistLayout: false,
        onSelect: (node, dbl, event) => { if (node?.uid) ctx.openUnit(node.uid, event ? { x: event.clientX, y: event.clientY } : null); },
      });
      ctx.trackGraph(preview);
      preview.setData(previewData.nodes, previewData.edges, { layout: "force" });
    } catch (err) {
      clear(previewHost);
      previewHost.appendChild(h("div", { class: "empty sm" }, err.message));
    }
  }
  setTimeout(reloadPreviewData, 20);
}

// Translates a dynamic key, falling back to the raw value when no key exists.
function labelOr(key, fallback) {
  const text = t(key);
  return text === key ? fallback : text;
}

// Splits a translated sentence on {name} placeholders and inserts DOM nodes.
function tNodes(key, nodes) {
  return t(key).split(/(\{\w+\})/).map((part) => {
    const match = /^\{(\w+)\}$/.exec(part);
    return match && match[1] in nodes ? nodes[match[1]] : part;
  }).filter((part) => part !== "");
}

function formatPhysicsInput(key, value) {
  if (key === "gravity") return Number(value).toFixed(5);
  if (key === "damping") return Number(value).toFixed(2);
  return String(Math.round(value));
}

// Settings -> Mobile: the Android app.  The server
// resolves the APK of the latest GitHub release (never the browser: GitHub
// rate-limits shared IPs) and sends their URLs and QR codes; without an
// absolute URL (an older server, a failed request) the release page is used.
const RELEASE_PAGE = "https://github.com/MasterYip/ARK-Studio/releases/latest";

function androidAppsBox(onLoad = null) {
  const box = h("div", { class: "android-apps" });
  // Labels are keys so they resolve on every render (language switch re-renders).
  const cards = [
    ["native", "settings.android.native_title", "settings.android.native_desc"],
  ];
  const render = (apps) => {
    clear(box);
    for (const [key, titleKey, descKey] of cards) {
      const app = apps?.[key] || {};
      const url = /^https:\/\//.test(app.url || "") ? app.url : RELEASE_PAGE;
      const direct = Boolean(app.direct) && url === app.url;
      const qr = app.qr_svg && url === app.url ? h("div", { class: "android-app-qr" }) : null;
      // The SVG is generated by the server's QR encoder from the URL above.
      if (qr) qr.innerHTML = app.qr_svg;
      box.appendChild(h("div", { class: "android-app-card" },
        h("div", { class: "android-app-head" }, icon("smartphone", "sm"), h("b", null, t(titleKey))),
        h("p", { class: "android-app-desc" }, t(descKey)),
        qr ? h("details", { class: "android-app-qr-fold" },
          h("summary", null, icon("qr", "sm"), t("settings.android.scan_qr")), qr) : null,
        h("div", { class: "android-app-foot" },
          h("span", { class: "muted" }, app.version
            ? t("settings.android.from_release_version", { version: app.version })
            : t("settings.android.from_release")),
          h("a", { class: "btn sm", href: url, target: "_blank", rel: "noopener noreferrer",
            title: direct ? t("settings.android.download_file", { file: app.file || url.split("/").pop() }) : url },
          icon(direct ? "download" : "external", "sm"),
          t(direct ? "settings.android.download_apk" : "settings.android.open_release")))));
    }
  };
  render(null);
  const all = { native: { available: true } };
  api.mobileApps().then((data) => { render(data.apps); onLoad?.(all); }).catch(() => { render(null); onLoad?.(all); });
  return box;
}
