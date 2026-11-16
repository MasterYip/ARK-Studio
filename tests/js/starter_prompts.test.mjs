// Run: node --experimental-vm-modules --test tests/js/starter_prompts.test.mjs
// The two Home starter prompts ("setup", "team") exist in English and
// Chinese; both languages must carry the same steps, standing rules and
// technical tokens.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

const MODULES = new Map();
async function loadModule(name) {
  if (MODULES.has(name)) return MODULES.get(name);
  const source = await readFile(new URL(`../../ark_studio/web/js/${name}`, import.meta.url), "utf8");
  const module = new vm.SourceTextModule(source, { identifier: name });
  MODULES.set(name, module);
  await module.link(async (specifier) => {
    const dep = specifier.replace(/^\.\//, "");
    if (dep !== "session-context-model.js") throw new Error(`${name} must stay DOM-free (imports ${specifier})`);
    return loadModule(dep);
  });
  return module;
}

const starter = await loadModule("starter-prompts.js");
await starter.evaluate();
const prompts = starter.namespace;
const session = MODULES.get("session-context-model.js").namespace;
const SHARED = prompts.START_SHARED;
const byId = (id) => prompts.STARTER_PROMPTS.find((item) => item.id === id);
const SETUP = byId("setup").sections;
const TEAM = byId("team").sections;
const P = { ...SHARED, ...SETUP };
const SECTIONS = [["shared", SHARED], ["setup", SETUP], ["team", TEAM]];

// Every line pair of the shared part, both prompts and the session block.
function allLines() {
  const out = [];
  for (const [prefix, sections] of SECTIONS) {
    for (const [part, lines] of Object.entries(sections)) lines.forEach((line, i) => out.push([`${prefix}.${part}[${i}]`, line]));
  }
  for (const [part, lines] of Object.entries(session.SESSION_PROMPT_LINES)) lines.forEach((line, i) => out.push([`session.${part}[${i}]`, line]));
  return out;
}

const TECH = [
  /\{\{\w+\}\}/g, // render-time interpolations
  /\b(?:GET|POST)\s+(?=[/{])[^\s,;()（）]*[\w}]/g, // method + path stay together
  /\/api\/agent\/v1\/\.\.\.|\/api\/[\w./{}<>-]*[\w}>]/g, // API paths
  /\?[\w-]+=[^\s)）]*/g, // query parameters
  /\b(?:Authorization|X-ARK-[A-Za-z]+)\b/g, // header names
  /"(?:Authorization: Bearer [^"<]*|X-ARK-[A-Za-z]+: (?:\{\{\w+\}\})?)/g, // quoted header lines
  /\b[\w-]+(?:[./:][\w<>{}*-]+)+/g, // dotted fields, file paths, env:NAME
  /\b[a-z]+_[a-z_]+\b|\b[A-Z]+_[A-Z_]+\b/g, // snake_case fields, ARK_TOKEN
  /\b\w+: (?:true|false|\[\]|"[^"\s]*")/g, // JSON literals
  /\{[^{}\n]*\}/g, // JSON objects
  /<[^<>\s]+>/g, // technical placeholders (<token>, <ID>, <TASK-ID>, <path>)
  /`[^`]+`/g, // code spans
  /--[a-z-]+/g, // command-line flags
  /\b(?:open|active|review|blocked|done)\b(?= →|$)|(?<=→ )(?:active|review|done)\b/g, // unit statuses
  /\b(?:scope|code|team|compute|commands|authority)\b(?=[,、 ]|$)(?=.*profile\.skipped)/g, // setup areas
  /\b(?:R\d|DSH|Codex|Claude Code|JSONL|SKILL\.md|ARK|GPU|conda|venv|docker|agent-team|404)\b/g,
];

// Descriptive placeholders (<idea, paper or link>, <objective>) are
// translated; technical ones are not: the names below, and any placeholder
// that is part of a path or query (/<path>, =<heading-slug>).
const TECH_PLACEHOLDERS = new Set(["token", "ID", "TASK-ID", "key", "lines", "heading-slug"]);
const plain = (text) => text.replace(/(^|[^/=])<([^<>\n]+)>/g,
  (all, before, name) => (TECH_PLACEHOLDERS.has(name) ? all : `${before}<…>`));

function techTokens(text) {
  const out = new Set();
  for (const re of TECH) for (const match of text.matchAll(re)) out.add(match[0]);
  return out;
}

const PROJECT = { id: "prj_1", name: "Demo", settings: { sessions: { accept_uploads: true } } };
const render = (lang, token = "", id = "setup") => prompts.starterPromptText(id, PROJECT,
  { url: "http://ark:9", token, sessionId: "ark-ses-abcdefghij", lang });

test("every line has an English and a Chinese version", () => {
  for (const [where, line] of allLines()) {
    if (typeof line === "string") continue; // command lines are shared verbatim
    assert.deepEqual(Object.keys(line).sort(), ["en", "zh"], where);
    assert.equal(typeof line.en, "string", where);
    assert.equal(typeof line.zh, "string", where);
    if (line.en.trim()) assert.match(line.zh, /[一-鿿]/, `${where} must be Chinese`);
  }
});

test("Chinese keeps every API path, header, field and interpolation verbatim", () => {
  for (const [where, line] of allLines()) {
    if (typeof line === "string") continue;
    const missing = [...techTokens(plain(line.en))].filter((token) => !plain(line.zh).includes(token));
    assert.deepEqual(missing, [], `${where}: missing in zh: ${JSON.stringify(missing)}`);
    assert.deepEqual([...line.zh.matchAll(/\{\{\w+\}\}/g)].map((m) => m[0]).sort(),
      [...line.en.matchAll(/\{\{\w+\}\}/g)].map((m) => m[0]).sort(), where);
  }
});

test("the drift check notices a dropped token", () => {
  const line = P.setup[3];
  const broken = line.zh.replace("propose: true}", "propose 为 true}");
  const missing = [...techTokens(plain(line.en))].filter((token) => !plain(broken).includes(token));
  assert.ok(missing.includes("{resources: [...], propose: true}"), JSON.stringify(missing));
});

test("steps, rules and options line up between the languages", () => {
  const marker = (text) => (text.match(/^\s*(?:R?\d+\.|-)(?=\s)/) || [""])[0];
  for (const [where, line] of allLines()) {
    if (typeof line === "string") continue;
    assert.equal(marker(line.zh), marker(line.en), `${where}: step/rule/option marker`);
    assert.equal(line.zh.match(/^\s*/)[0], line.en.match(/^\s*/)[0], `${where}: indent`);
  }
  for (const [prefix, sections] of SECTIONS) {
    for (const [part, lines] of Object.entries(sections)) {
      const markers = (lang) => lines.map((line) => marker(prompts.pickLine(line, lang))).filter(Boolean);
      assert.deepEqual(markers("zh"), markers("en"), `${prefix}.${part}`);
    }
  }
  assert.deepEqual(P.rules.map((line) => marker(line.en)).filter(Boolean), ["R1.", "R2.", "R3.", "R4.", "R5.", "R6.", "R7.", "R8.", "R9."]);
  assert.deepEqual(P.setup.map((line) => marker(line.en)).filter(Boolean), ["1.", "2.", "3.", "4.", "5."]);
  assert.equal(P.next.filter((line) => marker(line.en) === "-").length, 5);
  assert.deepEqual(TEAM.context.map((line) => marker(line.en)).filter(Boolean), ["1.", "2.", "3.", "4.", "5."]);
  assert.equal(TEAM.options.filter((line) => marker(line.en) === "-").length, 5);
});

const HEADS = {
  setup: { en: ["FIRST:", "NEXT:"], zh: ["第一步：", "接下来："] },
  team: { en: ["NOW:", "THEN:"], zh: ["现在：", "然后："] },
};

for (const id of ["setup", "team"]) test(`${id} prompt: docs, standing rules near the top, body, session block last`, () => {
  for (const lang of ["en", "zh"]) {
    for (const token of ["", "arka_secret"]) {
      const text = render(lang, token, id);
      const at = (needle) => {
        const index = text.indexOf(needle);
        assert.ok(index >= 0, `${lang}: missing ${needle}`);
        return index;
      };
      const docs = at("GET http://ark:9/api/agent/v1/docs");
      const rules = at(lang === "en" ? "STANDING RULES" : "长期规则");
      const setup = at(HEADS[id][lang][0]);
      const next = at(HEADS[id][lang][1]);
      const sessionId = at(lang === "en" ? "ARK session id for this conversation" : "本次对话的 ARK 会话 ID");
      assert.ok(docs < rules && rules < setup && setup < next && next < sessionId, lang);
      // Rules come before any step, within the first third of the prompt.
      assert.ok(rules < text.length / 3, `${id} ${lang}: rules at ${rules} of ${text.length}`);
      // The session block is the tail of the prompt.
      assert.ok(text.trimEnd().endsWith(lang === "en" ? "never paste credentials into the conversation." : "绝不要把凭据粘贴到对话中。"), lang);
      assert.doesNotMatch(text, /\{\{|\}\}/);
      assert.match(text, /X-ARK-Project: prj_1/);
      assert.match(text, /ark-ses-abcdefghij/);
      if (token) assert.match(text, /arka_secret/);
      else assert.doesNotMatch(text, /arka_/);
    }
    assert.equal(render(lang, "", id).split("\n").length, render("en", "", id).split("\n").length);
  }
  assert.match(render("zh", "", id), /^你是我的研究智能体/);
  assert.match(render("en", "", id), /^You are my research agent/);
});

test("the two prompts differ only after the shared part", () => {
  for (const lang of ["en", "zh"]) {
    const setup = render(lang, "", "setup");
    const team = render(lang, "", "team");
    const cut = setup.indexOf(HEADS.setup[lang][0]);
    assert.equal(team.slice(0, cut), setup.slice(0, cut));
    assert.ok(team.indexOf(HEADS.team[lang][0]) === cut);
  }
  assert.equal(prompts.starterPromptText("nope", PROJECT), "");
});

test("team prompt loads the research context, summarises, offers options, coordinates", () => {
  const context = TEAM.context.map((line) => line.en).join("\n");
  for (const needed of [
    "new session", "GET /api/agent/v1/context", "GET /api/agent/v1/setup", "readiness.ready",
    "do not start task work", "GET /api/agent/v1/units/<ID>", "GET /api/agent/v1/units/<ID>/comments",
    "GET /api/agent/v1/skills/agent-team", "under 15 lines", "Change nothing yet",
  ]) assert.ok(context.includes(needed), needed);
  const options = (lang) => TEAM.options.map((line) => prompts.pickLine(line, lang)).filter((line) => line.startsWith("- "));
  assert.deepEqual(options("en").map((line) => line.match(/^- "([^"]+)"/)?.[1].split(/[:<]/)[0].trim()),
    ["Start a research team on", "Continue", "Plan this idea", "Give me a status check", "Review the results of"]);
  for (const line of options("zh")) assert.match(line, /^- “[^”]+”：/);
  const all = TEAM.options.map((line) => line.en).join("\n");
  for (const needed of ["POST /api/skills/agent-team/tasks", "create: false", "create: true", ".worktrees/<TASK-ID>",
    "cheapest-first", "act as the coordinator", "instead of pretending workers are running"]) assert.ok(all.includes(needed), needed);
});

test("documentation: index first, on-demand reads, fallback for older servers", () => {
  const docs = P.docs.map((line) => line.en).join("\n");
  assert.match(docs, /GET \{\{url\}\}\/api\/agent\/v1\/docs\b/);
  assert.match(docs, /GET \/api\/agent\/v1\/docs\/<path>/);
  assert.match(docs, /\?section=<heading-slug>/);
  assert.match(docs, /Authorization and X-ARK-Project/);
  assert.match(docs, /404[^.]*GET \/api\/agent\/v1\/interface and GET \/api\/agent\/v1\/skills/);
});

test("standing rules cover reading, recording, evidence, summaries and offline notes", () => {
  const rules = P.rules.map((line) => line.en).join("\n");
  for (const needed of [
    "without me reminding you",
    "GET /api/agent/v1/context",
    "GET /api/agent/v1/units/<ID>",
    "straight away, without being asked",
    "POST /api/agent/v1/units with type: \"task\"",
    "open → active → review or blocked → done",
    "POST /api/agent/v1/units/<ID>/comments",
    "POST /api/agent/v1/media/upload",
    "POST /api/agent/v1/assets/upload",
    "POST /api/agent/v1/media ",
    "POST /api/agent/v1/links",
    "summary comment",
    "ARK is unreachable",
    "ark-sync-notes.md",
    "env:NAME",
    "readiness.requires_human",
  ]) assert.ok(rules.includes(needed), needed);
});

test("setup keeps its hard rules: the human confirms, no probing", () => {
  const setup = P.setup.map((line) => line.en).join("\n");
  for (const needed of [
    "GET /api/agent/v1/skills/setup", "GET /api/agent/v1/setup", "X-ARK-Host",
    "POST /api/agent/v1/setup/discover", "paths: []", "profile.skipped",
    "POST /api/agent/v1/setup/draft", "{layer: \"project\", profile: {...}, propose: true}",
    "POST /api/agent/v1/setup/location", "Home → Review & confirm",
    "never confirm yourself", "POST /api/agent/v1/setup/verify",
    "Never delete another member's records", "do not probe GPU servers",
  ]) assert.ok(setup.includes(needed), needed);
  assert.match(P.setup[4].zh, /绝不要自己确认/);
});

test("next steps offer example requests in the prompt language", () => {
  const options = (lang) => P.next.map((line) => prompts.pickLine(line, lang)).filter((line) => line.startsWith("- "));
  assert.deepEqual(options("en").map((line) => line.match(/^- "([^"]+)"/)?.[1].split(/[:<]/)[0].trim()),
    ["Plan this idea", "Start a research team on", "Give me a status check", "Record what we just did", "Import my existing task folder"]);
  for (const line of options("zh")) assert.match(line, /^- “[^”]+”：/);
  assert.match(options("zh")[0], /规划这个想法/);
});

test("prompts follow the interface language", () => {
  assert.equal(prompts.promptLang("zh"), "zh");
  assert.equal(prompts.promptLang("en"), "en");
  assert.equal(prompts.promptLang("fr"), "en");
  assert.equal(prompts.promptLang(undefined), "en");
});

test("session block in both languages", () => {
  const block = session.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://a", projectId: "p", uploads: true });
  assert.ok(block.includes("{\"offset\": <next byte offset>, \"content\": \"<lines>\\n\"}"));
  assert.ok(block.includes("  curl -fsS -H \"Authorization: Bearer $ARK_TOKEN\" -H \"X-ARK-Project: p\" http://a/api/agent/v1/sessions/watcher.py -o ark-session-watch.py"));
  // The downloadable watcher comes first; the installed alternative is the Go CLI.
  assert.ok(block.indexOf("watcher.py") < block.indexOf("ark-server session-watch"));
  assert.ok(block.includes("`ark-server session-watch --id ark-ses-abcdefghij --server http://a --project p`"));
  for (const lang of ["en", "zh"]) {
    const text = session.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://a", projectId: "p", uploads: true, lang });
    assert.doesNotMatch(text, /python -m arkstudio/, lang);
  }
  const off = session.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://ark", lang: "zh" });
  assert.match(off, /本次对话的 ARK 会话 ID：ark-ses-abcdefghij/);
  assert.match(off, /已关闭会话上传/);
  const on = session.sessionPromptBlock({ sessionId: "ark-ses-abcdefghij", url: "http://ark", projectId: "prj_1", uploads: true, lang: "zh" });
  assert.match(on, /--id ark-ses-abcdefghij --server http:\/\/ark --project prj_1 --interval 300/);
  assert.match(on, /每 5 分钟/);
});
