// Settings > Skills: discover the ARK-native protocol layer and render its
// templates.  Read-only except for an explicit "create task" action; all skill
// state lives in arkstudio/skills and the project, never in this browser.
import { h, clear } from "./dom.js";
import { icon } from "./icons.js";
import { api } from "./api.js";
import { t, hasKey } from "./i18n.js";
import { foldSection } from "./fold-section.js";

// Catalogue display text arrives in English; i18n_server.js translates it by
// skill/template id.  agent_prompt and rendered templates stay English (agents read them).
function skillText(skill, field, fallback) {
  const key = `server.skill.${skill.id}.${field}`;
  return hasKey(key) ? t(key) : (fallback || "");
}
const skillName = (skill) => skillText(skill, "name", skill.name);
const templateText = (skill, template, field) =>
  skillText(skill, `template.${template.id}.${field}`, template[field]);

export function renderSkillsPanel(ctx) {
  const panel = h("section", { class: "panel settings-panel" },
    h("div", { class: "panel-head" },
      h("h3", null, t("skills.title"))),
    h("p", { class: "settings-lead" }, t("skills.lead")));

  const status = h("div", { class: "settings-note" },
    icon("info", "sm"), h("div", null, t("skills.loading")));
  const cards = h("div", { class: "stack" });
  const catalog = foldSection({ id: "skills-catalog", title: t("settings.fold.skills_catalog"),
    summary: t("skills.loading"), body: cards });
  const outputHead = h("div", { class: "physics-name" }, t("skills.rendered"));
  const output = h("pre", { class: "code-box", style: { maxHeight: "360px" } },
    t("skills.choose_template"));
  // Hidden until a template or prompt is shown: no empty placeholder box.
  const outputWrap = h("div", { class: "stack", style: { marginTop: "12px" }, hidden: true },
    outputHead, output);

  const showOutput = (title, text) => {
    outputHead.textContent = title;
    output.textContent = text;
    outputWrap.hidden = false;
  };

  const copyPrompt = async (skill) => {
    const prompt = skill.agent_prompt || skill.prompt || "";
    try {
      await navigator.clipboard.writeText(prompt);
      ctx.toast(t("skills.prompt_copied", { name: skillName(skill) }), "ok");
    } catch {
      showOutput(t("skills.prompt_heading", { name: skillName(skill) }), prompt);
      ctx.toast(t("skills.clipboard_unavailable"), "err");
    }
  };

  const renderTemplate = async (skill, template) => {
    try {
      const data = await api.renderSkill(skill.id, { template: template.id, context: {} });
      showOutput(`${skillName(skill)} · ${templateText(skill, template, "title")}`, data.text);
      ctx.toast(t("skills.template_rendered", { title: templateText(skill, template, "title") }), "ok");
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  };

  const createTask = async (skill) => {
    try {
      const data = await api.skillTask(skill.id, {
        create: true,
        title: "Skills layer verification", // i18n-ignore: stored task data
        objective: "Create a real ARK task from the agent-team protocol to verify the integration.", // i18n-ignore: stored task data
        owner: "agent:settings",
        reviewer: "human:maintainer",
        repository: "ARK-Studio",
        base_revision: "working-tree",
        importance: "p3",
        deliverables: ["ARK task unit with a rendered goal manifest"],
        non_goals: ["modifying unrelated project data"],
        owned_paths: ["docs/agents/skills.md"],
        acceptance: [{ command: "inspect the task unit body", threshold: "manifest present",
          evidence: "task unit" }],
        gates: ["scope", "static", "unit"],
        stop_conditions: ["the task panel rejects the unit"],
      });
      if (data.ark_unit) {
        ctx.toast(t("skills.task_created", { id: data.manifest.task_id }), "ok");
        await ctx.refresh();
      } else {
        showOutput(t("skills.task_preview"), JSON.stringify(data, null, 2));
      }
    } catch (err) {
      ctx.toast(err.message, "err");
    }
  };

  const renderSkillCard = (skill) => {
    const source = skill.source || {};
    const actions = h("div", { class: "row tight", style: { flexWrap: "wrap", justifyContent: "flex-end" } });
    for (const template of skill.templates || []) {
      actions.appendChild(h("button", {
        class: "ghost-btn sm", type: "button",
        title: templateText(skill, template, "description"),
        onclick: () => renderTemplate(skill, template),
      }, icon("file", "sm"), templateText(skill, template, "title")));
    }
    actions.appendChild(h("button", {
      class: "ghost-btn sm", type: "button",
      onclick: () => copyPrompt(skill),
    }, icon("copy", "sm"), t("skills.agent_prompt")));
    if (skill.id === "agent-team") {
      actions.appendChild(h("button", {
        class: "primary-btn sm", type: "button",
        onclick: () => createTask(skill),
      }, icon("plus", "sm"), t("skills.create_task")));
    }
    const details = h("details", { class: "settings-lead" },
      h("summary", { style: { cursor: "pointer" } }, t("skills.source_mapping")),
      h("div", { style: { marginTop: "6px" } },
        h("div", null, `${source.upstream || "?"} · ${source.license || "?"} · ${source.upstream_ref || ""}`),
        h("div", null, t("skills.adapted_list", { items: (source.adapted || []).join("; ") || "—" })),
        h("div", null, t("skills.referenced_only", { items: (source.referenced_only || []).join("; ") || "—" })),
        h("div", null, `API: ${(skill.endpoints || []).join(" · ")}`)));
    return h("div", { class: "shortcut-row", style: { alignItems: "flex-start" } },
      h("div", { style: { minWidth: "0" } },
        h("div", { class: "row tight" },
          h("div", { class: "physics-name" }, skillName(skill)),
          h("span", { class: "pill ok" }, t("skills.adapted")),
          h("span", { class: "pill" }, skill.version)),
        h("div", { class: "physics-hint" }, skillText(skill, "summary", skill.summary)),
        details),
      actions);
  };

  const renderCatalog = (data) => {
    clear(cards);
    const protocol = data.protocol || {};
    const gateNames = (protocol.gates || []).map((gate) => gate.id).join(" → ");
    clear(status);
    status.append(
      icon("info", "sm"),
      h("div", null,
        h("b", null, t("skills.count", { n: (data.skills || []).length })),
        " · ",
        t("skills.protocol_version", { v: protocol.protocol_version || 1 }),
        gateNames ? t("skills.gates", { gates: gateNames }) : ""));
    for (const skill of data.skills || []) cards.appendChild(renderSkillCard(skill));
    catalog.setSummary(t("skills.count", { n: (data.skills || []).length }));
  };

  api.skills().then(renderCatalog).catch((err) => {
    clear(status);
    status.append(icon("info", "sm"),
      h("div", null, t("skills.unavailable", { error: err.message })));
  });

  panel.append(status, catalog.el, outputWrap);
  return panel;
}
