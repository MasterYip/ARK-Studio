// Settings › Appearance › Node markers (project, admins): the marker groups
// every collaborator's marker menus use.  Each group has a name, an
// "one per node" switch (exclusive) and its markers (glyph, colour, label).
// Saved with the Save bar as settings.markers (PATCH /api/project/settings);
// "Restore defaults" saves no setting, so the project follows the built-in
// groups.  An empty name or label shows the built-in, translated text.

import { h, clear } from "../dom.js";
import { t } from "../i18n.js";
import { icon } from "../icons.js";
import { api } from "../api.js";
import {
  effectiveMarkerGroups, cleanMarkerGroups, groupName, markerLabel, freeMarkerId, MARKER_LIMITS,
} from "../markers.js";

export const MARKERS_SECTION = "markers";

/**
 * The panel; `registerSave` / `setDirty` are the Settings save-bar hooks.
 * @returns {HTMLElement}
 */
export function markerSettingsPanel(ctx, { registerSave, setDirty, readOnly = false }) {
  const saved = () => ctx.S.project?.settings?.markers || null;
  let draft = effectiveMarkerGroups(saved());
  let reset = false;            // "Restore defaults" pending
  const list = h("div", { class: "marker-groups", "data-marker-groups": "" });
  const changed = () => { reset = false; setDirty(MARKERS_SECTION); paint(); };
  const takenMarkerIds = () => draft.flatMap((group) => group.markers.map((marker) => marker.id));

  const input = (value, attrs, onInput) => {
    const el = h("input", { class: "input", value, disabled: readOnly ? true : undefined, ...attrs });
    el.addEventListener("change", () => { onInput(el.value); setDirty(MARKERS_SECTION); });
    return el;
  };
  const iconButton = (name, title, onclick, attrs = {}) => h("button", {
    class: "ghost-btn sm", type: "button", title, "aria-label": title, disabled: readOnly ? true : undefined, onclick, ...attrs,
  }, icon(name, "sm"));

  function markerRow(group, marker, index) {
    const preview = h("span", { class: "marker-preview", style: { color: marker.color } }, marker.glyph);
    const color = h("input", { type: "color", value: marker.color, title: t("settings.markers.color"),
      "aria-label": t("settings.markers.color"), disabled: readOnly ? true : undefined });
    color.addEventListener("input", () => { marker.color = color.value; preview.style.color = color.value; setDirty(MARKERS_SECTION); });
    return h("div", { class: "marker-row", "data-marker-row": marker.id },
      preview,
      input(marker.glyph, { maxlength: "6", "aria-label": t("settings.markers.glyph"), title: t("settings.markers.glyph") },
        (value) => { marker.glyph = value.trim(); preview.textContent = marker.glyph; }),
      color,
      input(marker.label, { placeholder: markerLabel({ id: marker.id }), "aria-label": t("settings.markers.label"),
        title: `${t("settings.markers.label")} · marker:${marker.id}` }, (value) => { marker.label = value.trim(); }), // i18n-ignore: tag name
      h("div", { class: "row tight" },
        iconButton("arrowUp", t("settings.markers.move_up"), () => {
          if (index === 0) return;
          group.markers.splice(index - 1, 0, ...group.markers.splice(index, 1));
          changed();
        }, { disabled: readOnly || index === 0 ? true : undefined }),
        iconButton("trash", t("settings.markers.remove_marker"), () => {
          group.markers.splice(index, 1);
          changed();
        }, { disabled: readOnly || group.markers.length <= 1 ? true : undefined })));
  }

  function groupCard(group, index) {
    const exclusive = h("input", { type: "checkbox", checked: group.exclusive ? true : undefined,
      disabled: readOnly ? true : undefined, "data-group-exclusive": group.id });
    exclusive.addEventListener("change", () => { group.exclusive = exclusive.checked; setDirty(MARKERS_SECTION); });
    return h("div", { class: "marker-group", "data-marker-group": group.id },
      h("div", { class: "marker-group-head" },
        input(group.name, { placeholder: groupName({ id: group.id }), "aria-label": t("settings.markers.group_name") },
          (value) => { group.name = value.trim(); }),
        h("label", { class: "row tight", title: t("settings.markers.exclusive_hint") }, exclusive, t("settings.markers.exclusive")),
        h("div", { class: "spacer" }),
        iconButton("arrowUp", t("settings.markers.move_up"), () => {
          if (index === 0) return;
          draft.splice(index - 1, 0, ...draft.splice(index, 1));
          changed();
        }, { disabled: readOnly || index === 0 ? true : undefined }),
        iconButton("trash", t("settings.markers.remove_group"), () => {
          draft.splice(index, 1);
          changed();
        }, { disabled: readOnly || draft.length <= 1 ? true : undefined })),
      h("div", { class: "marker-rows" }, group.markers.map((marker, i) => markerRow(group, marker, i))),
      readOnly || group.markers.length >= MARKER_LIMITS.markers ? null : h("button", {
        class: "btn sm ghost", type: "button", "data-add-marker": group.id,
        onclick: () => {
          group.markers.push({ id: freeMarkerId(`${group.id}-${group.markers.length + 1}`, takenMarkerIds()),
            glyph: "●", color: "#64748b", label: t("settings.markers.new_marker") });
          changed();
        },
      }, icon("plus", "sm"), t("settings.markers.add_marker")));
  }

  function paint() {
    clear(list);
    list.append(...draft.map(groupCard));
  }

  const addGroup = h("button", {
    class: "btn sm ghost", type: "button", "data-add-marker-group": "", disabled: readOnly ? true : undefined,
    onclick: () => {
      if (draft.length >= MARKER_LIMITS.groups) return;
      const id = freeMarkerId("group", draft.map((group) => group.id));
      draft.push({ id, name: t("settings.markers.new_group"), exclusive: true,
        markers: [{ id: freeMarkerId(`${id}-1`, takenMarkerIds()), glyph: "●", color: "#64748b", label: t("settings.markers.new_marker") }] });
      changed();
    },
  }, icon("plus", "sm"), t("settings.markers.add_group"));
  const restore = h("button", {
    class: "ghost-btn sm", type: "button", "data-marker-defaults": "", disabled: readOnly ? true : undefined,
    title: t("settings.markers.defaults_hint"),
    onclick: () => {
      draft = effectiveMarkerGroups(null);
      paint();
      reset = true;
      setDirty(MARKERS_SECTION);
    },
  }, icon("refresh", "sm"), t("settings.markers.defaults"));

  registerSave(MARKERS_SECTION, {
    section: MARKERS_SECTION, label: t("settings.markers.title"),
    save: async () => {
      try {
        const value = reset ? null : cleanMarkerGroups(draft);
        const data = await api.updateSettings({ markers: value });
        ctx.applySettings(data);
        draft = effectiveMarkerGroups(saved());
        reset = false;
        paint();
        ctx.toast(t("settings.saved"), "ok");
        return true;
      } catch (err) {
        ctx.toast(err.message, "err");
        return false;
      }
    },
    discard: () => { draft = effectiveMarkerGroups(saved()); reset = false; paint(); },
  });
  paint();
  return h("section", { class: "panel settings-panel", "data-settings-markers": "" },
    h("div", { class: "panel-head" }, h("h3", null, t("settings.markers.title")),
      h("div", { class: "row tight" }, restore)),
    h("p", { class: "settings-lead" }, t("settings.markers.lead")),
    list,
    h("div", { class: "row tight" }, addGroup));
}
