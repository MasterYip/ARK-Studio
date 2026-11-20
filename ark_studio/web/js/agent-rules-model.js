// Pure helpers for the agent rules settings (views/agent-rules-panel.js).
// The server owns the catalog and the composition (internal/agentrules);
// this module only edits one scope's draft settings:
//   { rules: { <id>: { enabled?: bool, params?: { <param>: string } } },
//     custom: [{ id, enabled, text: { en, zh } }] }

export function pickText(text, lang) {
  if (!text) return "";
  return (lang === "zh" ? text.zh || text.en : text.en || text.zh) || "";
}

/** ARK's required protocol, in catalog order. */
export function requiredRules(catalog) {
  return (catalog?.rules || []).filter((rule) => rule.kind === "required");
}

/** Preference rules a scope ("project" or "personal") sets; "both" rules
 * appear in each (the project's choice overrides the person's). */
export function scopeRules(catalog, scope) {
  return (catalog?.rules || []).filter((rule) => rule.kind === "preference"
    && (rule.scope === scope || rule.scope === "both"));
}

/** The items of a multi param value ("a, b" -> ["a", "b"]). */
export function listItems(value) {
  return [...new Set(String(value || "").split(",").map((x) => x.trim()).filter(Boolean))];
}

/** Switch one item of a multi value; "all" stands alone. */
export function toggleListItem(value, item, on) {
  if (item === "all") return on ? "all" : "";
  const items = listItems(value).filter((x) => x !== "all" && x !== item);
  if (on) items.push(item);
  return items.join(", ");
}

const GIT_KINDS = new Set(["repository", "task_space"]);

/** Git repositories a project registered in its setup (GET /api/skills/setup/state):
 * repositories and task spaces of every layer, by resource id. */
export function gitRepoOptions(state) {
  const seen = new Map();
  const lists = [state?.resources, state?.project_layer?.profile?.resources, state?.mine?.profile?.resources];
  for (const list of lists) {
    for (const item of list || []) {
      const id = String(item?.id || item?.key || "").trim();
      if (!id || seen.has(id)) continue;
      if (!GIT_KINDS.has(item.kind) && !item.details?.remote) continue;
      seen.set(id, { id, name: item.name || id, kind: item.kind || "" });
    }
  }
  return [...seen.values()];
}

/** A deep copy of the stored settings of one scope, normalised. */
export function draftFrom(reply, scope) {
  const stored = reply?.[scope] || {};
  return {
    rules: Object.fromEntries(Object.entries(stored.rules || {}).map(([id, value]) =>
      [id, { ...(value.enabled === undefined ? {} : { enabled: Boolean(value.enabled) }),
        ...(value.params ? { params: { ...value.params } } : {}) }])),
    custom: (stored.custom || []).map((item) => ({ id: item.id, enabled: item.enabled !== false,
      text: { en: item.text?.en || "", zh: item.text?.zh || "" } })),
  };
}

export function ruleEnabled(draft, rule) {
  const value = draft?.rules?.[rule.id]?.enabled;
  return value === undefined ? Boolean(rule.default) : Boolean(value);
}

export function ruleParam(draft, rule, param) {
  const value = draft?.rules?.[rule.id]?.params?.[param.id];
  return value === undefined ? param.default || "" : String(value);
}

function withRule(draft, id, change) {
  const rules = { ...(draft.rules || {}) };
  rules[id] = change({ ...(rules[id] || {}) });
  return { ...draft, rules };
}

/** Switch a rule; going back to its default removes the stored choice. */
export function setEnabled(draft, rule, enabled) {
  return withRule(draft, rule.id, (entry) => {
    if (Boolean(enabled) === Boolean(rule.default)) delete entry.enabled;
    else entry.enabled = Boolean(enabled);
    return entry;
  });
}

/** Set a parameter; one line, trimmed; the default removes the stored value. */
export function setParam(draft, rule, param, value) {
  const text = String(value ?? "").replace(/[\r\n]+/g, " ").trim();
  return withRule(draft, rule.id, (entry) => {
    const params = { ...(entry.params || {}) };
    if (!text || text === param.default) delete params[param.id];
    else params[param.id] = text;
    if (Object.keys(params).length) entry.params = params;
    else delete entry.params;
    return entry;
  });
}

/** A new, empty custom rule with the next free id custom-<n>. */
export function addCustom(draft) {
  const used = new Set((draft.custom || []).map((item) => item.id));
  let n = 1;
  while (used.has(`custom-${n}`)) n += 1;
  return { ...draft, custom: [...(draft.custom || []), { id: `custom-${n}`, enabled: true, text: { en: "", zh: "" } }] };
}

export function updateCustom(draft, index, change) {
  const custom = (draft.custom || []).map((item, i) => (i === index ? { ...item, ...change } : item));
  return { ...draft, custom };
}

export function removeCustom(draft, index) {
  return { ...draft, custom: (draft.custom || []).filter((_, i) => i !== index) };
}

/** Settings ready to send: empty rule entries and empty custom rules dropped. */
export function cleanDraft(draft) {
  const rules = Object.fromEntries(Object.entries(draft.rules || {}).filter(([, value]) => Object.keys(value).length));
  const custom = (draft.custom || []).filter((item) => (item.text?.en || item.text?.zh || "").trim());
  return { rules, custom };
}

/** Rough prompt cost: about four characters per token, CJK about one each. */
export function estimateTokens(text) {
  const s = String(text || "");
  const cjk = (s.match(/[　-鿿＀-￯]/g) || []).length;
  return Math.ceil((s.length - cjk) / 4 + cjk);
}
