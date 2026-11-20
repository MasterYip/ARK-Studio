// Settings > Agents (project) and Settings > Personal AI (personal): the
// rules an external coding agent follows in this project (T-0053).  ARK's
// required protocol is listed read-only; preference rules of this scope are
// switched and filled in here; custom rules are free text.  The preview is
// the server's composition of the draft (POST /api/agent-rules dry_run), the
// same lines the Home starter prompts carry and GET /api/agent/v1/rules
// returns to agents.  Model: ../agent-rules-model.js.
import { h, clear } from "../dom.js";
import { t, getLang } from "../i18n.js";
import { api } from "../api.js";
import { setComposedRules } from "./onboarding.js";
import {
  scopeRules, requiredRules, draftFrom, setEnabled, setParam, ruleEnabled, ruleParam,
  addCustom, updateCustom, removeCustom, cleanDraft, estimateTokens, pickText,
  listItems, toggleListItem, gitRepoOptions,
} from "../agent-rules-model.js";

export function agentRulesPanel(ctx, scope) {
  const lang = getLang() === "zh" ? "zh" : "en";
  const panel = h("section", { class: "panel settings-panel agent-rules-panel", "data-agent-rules": scope });
  const body = h("div", { class: "stack" });
  const status = h("span", { class: "settings-status" });
  panel.append(
    h("div", { class: "panel-head" }, h("h3", null, t(`settings.agent_rules.title_${scope}`))),
    h("p", { class: "settings-lead" }, t(`settings.agent_rules.lead_${scope}`)),
    body);

  let reply = null;
  let draft = null;
  let repoOptions = [];
  let previewTimer = 0;
  const preview = h("pre", { class: "code-box agent-rules-preview", "data-agent-rules-preview": "" });
  const previewMeta = h("div", { class: "muted", "data-agent-rules-version": "" });

  const showPreview = (effective) => {
    const lines = effective?.lines?.[lang] || [];
    preview.textContent = lines.join("\n");
    previewMeta.textContent = t("settings.agent_rules.preview_meta", {
      version: effective?.version || "", tokens: estimateTokens(lines.join("\n")),
    });
  };
  const refreshPreview = () => {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(async () => {
      try { showPreview((await api.previewAgentRules(scope, cleanDraft(draft))).effective); } catch (err) { status.textContent = err.message; }
    }, 250);
  };

  const editable = () => scope === "personal" || Boolean(reply?.can_edit_project);

  const render = () => {
    clear(body);
    const catalog = reply.catalog;
    const disabled = !editable();
    const required = h("details", { class: "agent-rules-required" },
      h("summary", null, t("settings.agent_rules.required", { n: requiredRules(catalog).length })),
      h("p", { class: "muted" }, t("settings.agent_rules.required_hint")),
      h("ol", null, requiredRules(catalog).map((rule) => h("li", null, h("b", null, pickText(rule.title, lang)), " ",
        h("span", { class: "muted" }, pickText(rule.text, lang))))));
    const prefs = h("div", { class: "stack agent-rules-prefs" }, scopeRules(catalog, scope).map((rule) => {
      const box = h("input", { type: "checkbox", checked: ruleEnabled(draft, rule), disabled: disabled || rule.silent,
        "data-rule": rule.id,
        onchange: () => { draft = setEnabled(draft, rule, box.checked); render(); refreshPreview(); } });
      const params = (rule.params || []).map((param) => {
        const value = ruleParam(draft, rule, param);
        if (param.multi) {
          // Multi-select from the project's registered repositories and task spaces.
          const options = [{ id: "all", name: t("settings.agent_rules.all_repos") },
            ...(param.source === "git_repos" ? repoOptions : [])];
          for (const item of listItems(value)) {
            if (!options.some((o) => o.id === item)) options.push({ id: item, name: item });
          }
          const chosen = new Set(listItems(value));
          const boxes = options.map((option) => h("label", { class: "row tight" },
            h("input", { type: "checkbox", disabled, checked: chosen.has(option.id),
              "data-rule-param": `${rule.id}.${param.id}`, "data-list-item": option.id,
              onchange: (event) => {
                draft = setParam(draft, rule, param, toggleListItem(ruleParam(draft, rule, param), option.id, event.target.checked));
                render(); refreshPreview();
              } }),
            h("span", null, option.name)));
          return h("div", { class: "field" }, h("span", null, pickText(param.label, lang)),
            h("div", { class: "stack tight" }, boxes));
        }
        const input = param.choices?.length
          ? h("select", { class: "select", disabled, "data-rule-param": `${rule.id}.${param.id}` },
            param.choices.map((choice) => h("option", { value: choice, selected: choice === value },
              t(`settings.agent_rules.choice.${choice}`))))
          : h("input", { class: "input", value, maxlength: param.max || 200, disabled, "data-rule-param": `${rule.id}.${param.id}` });
        const commit = () => { draft = setParam(draft, rule, param, input.value); refreshPreview(); };
        input.addEventListener(param.choices?.length ? "change" : "input", commit);
        return h("label", { class: "field" }, h("span", null, pickText(param.label, lang)), input);
      });
      const on = ruleEnabled(draft, rule);
      const text = on || !rule.off_text ? rule.text : rule.off_text;
      return h("div", { class: "agent-rule" },
        h("label", { class: "row tight" }, box, h("b", null, pickText(rule.title, lang))),
        text?.en ? h("p", { class: "muted" }, pickText(text, lang)) : null,
        ...params);
    }));
    const customList = h("div", { class: "stack agent-rules-custom" }, (draft.custom || []).map((item, index) => {
      const box = h("input", { type: "checkbox", checked: item.enabled !== false, disabled,
        onchange: () => { draft = updateCustom(draft, index, { enabled: box.checked }); refreshPreview(); } });
      const area = h("textarea", { class: "textarea", disabled, maxlength: 600, "data-custom-rule": item.id,
        oninput: () => { draft = updateCustom(draft, index, { text: { ...item.text, [lang]: area.value } }); refreshPreview(); } },
      item.text?.[lang] || item.text?.en || item.text?.zh || "");
      return h("div", { class: "agent-rule" },
        h("label", { class: "row tight" }, box, h("b", null, item.id)),
        area,
        disabled ? null : h("button", { class: "btn sm ghost", type: "button",
          onclick: () => { draft = removeCustom(draft, index); render(); refreshPreview(); } }, t("settings.agent_rules.remove")));
    }));
    const add = disabled ? null : h("button", { class: "btn sm ghost", type: "button", "data-add-custom-rule": "",
      onclick: () => { draft = addCustom(draft); render(); } }, t("settings.agent_rules.add_custom"));
    const save = disabled ? null : h("button", { class: "btn sm primary", type: "button", "data-save-agent-rules": "",
      onclick: async () => {
        try {
          reply = await api.saveAgentRules(scope, cleanDraft(draft));
          draft = draftFrom(reply, scope);
          setComposedRules(ctx.S.project?.id || "", reply.effective);
          status.textContent = t("settings.agent_rules.saved", { version: reply.effective?.version || "" });
          render();
          showPreview(reply.effective);
        } catch (err) { status.textContent = err.message; }
      } }, t("action.save"));
    body.append(
      required,
      h("div", { class: "settings-sub" }, t(`settings.agent_rules.preferences_${scope}`)),
      prefs,
      h("div", { class: "settings-sub" }, t("settings.agent_rules.custom")),
      customList,
      h("div", { class: "row tight" }, add, save, status),
      disabled ? h("p", { class: "muted" }, t("settings.agent_rules.admin_only")) : null,
      h("div", { class: "settings-sub" }, t("settings.agent_rules.preview")),
      previewMeta,
      preview);
  };

  api.setupState().then((state) => {
    repoOptions = gitRepoOptions(state);
    if (reply) render();
  }).catch(() => {});
  api.agentRules().then((data) => {
    reply = data;
    draft = draftFrom(reply, scope);
    render();
    showPreview(reply.effective);
  }).catch((err) => { body.append(h("p", { class: "muted" }, err.message)); });
  return panel;
}
