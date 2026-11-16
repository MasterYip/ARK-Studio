import { SERVER_EN } from "./i18n_server.js";
// Bilingual UI strings. Data stays in whatever language the author wrote.
// Only the active language's strings are downloaded (half of the text): the
// English and Chinese UI dictionaries are separate modules, and Chinese
// server-text translations another.  The English server templates stay in
// i18n_server.js because server messages are matched against them in either
// language.  boot.js preloads the active language's files.
const LOADERS = {
  en: () => import("./i18n-en.js").then((module) => ({ ...module.EN, ...SERVER_EN })),
  zh: () => Promise.all([import("./i18n-zh.js"), import("./i18n_server_zh.js")])
    .then(([ui, server]) => ({ ...ui.ZH, ...server.SERVER_ZH })),
};
const DICTS = {};
const loadingLang = {};
/** Load a language's strings (once); resolves when t() can use them. */
export function ensureLang(lang) {
  if (DICTS[lang]) return Promise.resolve(DICTS[lang]);
  loadingLang[lang] ||= LOADERS[lang]().then((dict) => { DICTS[lang] = dict; return dict; });
  return loadingLang[lang];
}
// Initialize defaults without overwriting a choice made on the login page.
try {
  if (!localStorage.getItem("ark.prefsV2")) {
    localStorage.setItem("ark.prefsV2", "1");
    if (!localStorage.getItem("ark.lang")) localStorage.setItem("ark.lang", "en");
    if (!localStorage.getItem("ark.theme")) localStorage.setItem("ark.theme", "light");
  }
} catch { /* storage can be unavailable */ }
const queryLang = new URLSearchParams(location.search).get("lang");
let LANG = queryLang === "en" || queryLang === "zh"
  ? queryLang
  : (localStorage.getItem("ark.lang") || "en");
if (!LOADERS[LANG]) LANG = "en";
// Every module imports this one, so the app starts once the strings are here.
await ensureLang(LANG);
export function getLang() { return LANG; }
/**
 * Switch the UI language.  A language not loaded yet keeps the current one
 * until its strings arrive, then fires "ark:lang-loaded" ({lang}) so the app
 * can apply it; the return value is the language in effect now.
 */
export function setLang(lang) {
  const next = LOADERS[lang] ? lang : "en";
  try { localStorage.setItem("ark.lang", next); } catch { /* storage unavailable */ }
  if (!DICTS[next]) {
    ensureLang(next).then(() => window.dispatchEvent(new CustomEvent("ark:lang-loaded", { detail: { lang: next } })));
    return LANG;
  }
  LANG = next;
  document.documentElement.lang = LANG === "zh" ? "zh-CN" : "en";
  document.documentElement.dataset.lang = LANG;
  return LANG;
}
export function t(key, vars = {}) {
  const dict = DICTS[LANG] || {};
  let text = dict[key] ?? DICTS.en?.[key] ?? SERVER_EN[key] ?? key;
  for (const [k, v] of Object.entries(vars)) text = text.replaceAll(`{${k}}`, String(v));
  return text;
}
export function hasKey(key) {
  return Object.prototype.hasOwnProperty.call(DICTS[LANG] || {}, key);
}
// Server-originated English -> UI language, in one place.  The server keeps
// sending English; web/js/i18n_server.js holds its EN templates and the ZH
// translations.  A message is matched by its explicit code (server.error.<CODE>,
// or a {code, params, text} object) or by its English text against the EN
// templates; {name} parts capture values that are re-inserted (and themselves
// localised when they are known server phrases).  Unknown text stays English.
let serverPatterns = null;
function compileServerPatterns() {
  const rows = [];
  for (const [key, template] of Object.entries(SERVER_EN)) {
    const names = [];
    let literal = 0;
    const source = template.split(/(\{[A-Za-z_][A-Za-z0-9_]*\})/).map((part, index) => {
      if (index % 2 === 0) {
        literal += part.length;
        return part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      }
      const name = part.slice(1, -1);
      if (names.includes(name)) return `\\k<${name}>`;
      names.push(name);
      return `(?<${name}>.+?)`;
    }).join("");
    if (literal >= 3) rows.push({ key, literal, re: new RegExp(`^${source}$`, "s") });
  }
  // Most literal text first, so "{field} is required" never shadows
  // "Project name is required".
  return rows.sort((a, b) => b.literal - a.literal);
}
export function matchServerText(text, prefix = "server.") {
  const value = String(text ?? "");
  if (!value) return null;
  serverPatterns ||= compileServerPatterns();
  for (const row of serverPatterns) {
    if (!row.key.startsWith(prefix)) continue;
    const found = row.re.exec(value);
    if (found) return { key: row.key, params: { ...(found.groups || {}) } };
  }
  return null;
}
function renderServer(key, params, depth) {
  const local = {};
  for (const [name, value] of Object.entries(params || {})) {
    // Only phrases (with a space) are localised; ids such as "gates" stay as sent.
    local[name] = depth < 2 && typeof value === "string" && value.includes(" ")
      ? localizeServer(value, "server.", depth + 1) : value;
  }
  return t(key, local);
}
// English server text (hint, note, detail, reason, ...) in the UI language.
export function localizeServer(text, prefix = "server.", depth = 0) {
  if (text == null) return "";
  const english = String(text);
  if (LANG === "en" || !english) return english;
  const hit = matchServerText(english, prefix);
  return hit && hasKey(hit.key) ? renderServer(hit.key, hit.params, depth) : english;
}
// An error envelope {code, message} (or a chat-stream error event) -> text.
export function serverErrorText(error, fallback = "") {
  if (!error || typeof error !== "object") return error ? serverErrorText({ message: String(error) }) : fallback;
  const english = String(error.message || fallback || "");
  if (LANG === "en") return english;
  const byCode = error.code ? `server.error.${error.code}` : "";
  if (byCode && SERVER_EN[byCode] === english && hasKey(byCode)) return t(byCode);
  // Error templates first; some errors carry a reason that is also payload
  // text (e.g. the git "not inside a git working tree" reason).
  const asError = localizeServer(english, "server.error.");
  return asError !== english ? asError : localizeServer(english, "server.");
}
export function locale() { return LANG === "zh" ? "zh-CN" : "en-US"; }
export function typeLabel(id) { return t(`type.${id}`) === `type.${id}` ? id : t(`type.${id}`); }
export function statusLabel(id) { return t(`status.${id}`) === `status.${id}` ? id : t(`status.${id}`); }
export function impLabel(id) { return t(`imp.${id}`) === `imp.${id}` ? id.toUpperCase() : t(`imp.${id}`); }
export function relLabel(id) { return t(`rel.${id}`) === `rel.${id}` ? id.replaceAll("_", " ") : t(`rel.${id}`); }
export function originLabel(id) { return t(`origin.${id}`) === `origin.${id}` ? (id || "manual") : t(`origin.${id}`); }
export function logLabel(action) { return t(`log.${action}`) === `log.${action}` ? action : t(`log.${action}`); }
export function nsLabel(ns) {
  const map = { topic: "topic", stage: "stage", role: "role", channel: "channel", model: "model", host: "host",
    venue: "venue", year: "year", protocol: "protocol", outcome: "outcome", quality: "quality", risk: "risk",
    gate: "gate", kind: "kind", color: "color", font: "font", system: "system", access: "access",
    audience: "audience", component: "component", version: "version", task: "task", origin: "origin" };
  if (LANG === "zh") {
    const zh = { topic: "主题", stage: "阶段", role: "角色", channel: "渠道", model: "模型", host: "主机",
      venue: "会议", year: "年份", protocol: "协议", outcome: "结果", quality: "质量", risk: "风险",
      gate: "门", kind: "类别", color: "颜色", font: "字体", system: "系统", access: "访问",
      audience: "受众", component: "组件", version: "版本", task: "任务", origin: "来源" };
    if (zh[ns]) return zh[ns];
  }
  return map[ns] || ns;
}
setLang(LANG);
